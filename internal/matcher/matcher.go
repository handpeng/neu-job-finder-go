package matcher

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"neu-job-finder/internal/model"
)

var cohortPattern = regexp.MustCompile("(?:20)?\\d{2}届")

var semanticAliasGroups = [][]string{
	{"人工智能", "ai", "机器学习", "深度学习", "算法工程师", "人工智能工程师", "智能化"},
	{"冶金", "钢铁", "炼钢", "转炉"},
	{"研发", "研发类", "研发岗位", "研发工程师"},
	{"过程建模", "建模", "模型"},
	{"数值模拟", "仿真", "模拟"},
	{"流体", "cfd", "fluent"},
	{"数据分析", "数据挖掘", "数据科学"},
}

type fieldScore struct {
	name     string
	weight   float64
	score    float64
	reason   string
	evidence []model.MatchEvidence
	active   bool
	hard     bool
	ok       bool
}

type evidenceSource struct {
	text       string
	provenance model.EvidenceProvenance
	confidence float64
}

type BooleanResult struct {
	Eligible   bool
	Matched    []string
	Missing    []string
	Exclusions []string
	Evidence   []model.MatchEvidence
}

func Flatten(items []model.Announcement, p model.Profile) []model.JobView {
	out := []model.JobView{}
	for _, a := range items {
		for _, pos := range a.Positions {
			v := Score(a, pos, p)
			if !v.Eligible {
				continue
			}
			if p.Strict && len(v.HardMismatch) > 0 {
				continue
			}
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == nil && out[j].Score != nil {
			return false
		}
		if out[i].Score != nil && out[j].Score == nil {
			return true
		}
		if out[i].Score != nil && out[j].Score != nil && *out[i].Score != *out[j].Score {
			return *out[i].Score > *out[j].Score
		}
		return out[i].Announcement.LastSeenAt.After(out[j].Announcement.LastSeenAt)
	})
	return out
}

func Score(a model.Announcement, pos model.Position, p model.Profile) model.JobView {
	v := model.JobView{Announcement: a, Position: pos, Eligible: true}
	generic := isGenericPosition(pos.Name, a.Company)
	positionText := strings.Join([]string{pos.Name, pos.Salary, pos.Location, pos.EmploymentType, pos.Degree, pos.Majors}, " ")
	positionSources := positionEvidence(positionText, pos, a.RawText, generic)
	roleSources := positionEvidence(pos.Name, pos, a.RawText, generic)
	majorSources := positionEvidence(pos.Majors, pos, a.RawText, generic)
	citySources := append([]evidenceSource{}, positionEvidence(pos.Location+" "+pos.Name, pos, a.RawText, generic)...)
	if strings.TrimSpace(a.CommonText) != "" {
		citySources = append(citySources, evidenceSource{text: a.CommonText, provenance: model.AnnouncementCommon, confidence: 1})
	}
	boolean := Evaluate(a, pos, p.Query)
	if !p.Query.HasClauses() {
		boolean = Evaluate(a, pos, legacyQuery(p))
	}
	v.Eligible = boolean.Eligible
	v.BooleanMatched = append(v.BooleanMatched, boolean.Matched...)
	v.BooleanMissing = append(v.BooleanMissing, boolean.Missing...)
	v.Exclusions = append(v.Exclusions, boolean.Exclusions...)
	v.MatchEvidence = append(v.MatchEvidence, boolean.Evidence...)
	if !boolean.Eligible {
		v.HardMismatch = append(v.HardMismatch, boolean.Missing...)
		v.HardMismatch = append(v.HardMismatch, boolean.Exclusions...)
	}
	fields := []fieldScore{
		semanticField("意向岗位", p.Roles, roleSources, 25),
		semanticField("核心技能", p.Skills, positionSources, 22),
		semanticField("研究方向", p.Research, append(append([]evidenceSource{}, roleSources...), majorSources...), 18),
		semanticField("专业", p.Major, majorSources, 13),
		cityField(p.Cities, pos.Location, citySources, 12),
		degreeField(p.Degree, pos.Degree, 10),
		gradYearField(p.GraduationYear, positionEvidence("", pos, a.RawText, generic), a.CommonText),
	}
	active := 0
	weightSum := 0.0
	weighted := 0.0
	for _, f := range fields {
		if !f.active {
			continue
		}
		active++
		weightSum += f.weight
		weighted += f.weight * f.score
		if f.ok && f.reason != "" {
			v.Matched = append(v.Matched, f.reason)
			v.MatchEvidence = append(v.MatchEvidence, f.evidence...)
		} else if f.hard {
			v.HardMismatch = append(v.HardMismatch, f.name+"：不匹配")
		} else {
			v.Missing = append(v.Missing, f.name+"：匹配证据不足")
		}
	}
	if active > 0 && weightSum > 0 {
		s := int(weighted/weightSum*100 + 0.5)
		if s < 0 {
			s = 0
		}
		if s > 100 {
			s = 100
		}
		v.Score = &s
	}
	return v
}

func Evaluate(a model.Announcement, pos model.Position, query model.BooleanQuery) BooleanResult {
	query = normalizeQuery(query)
	result := BooleanResult{Eligible: true}
	if query.MinimumShouldMatch < 0 || query.MinimumShouldMatch > len(query.Should) {
		result.Eligible = false
		result.Missing = append(result.Missing, fmt.Sprintf("minimum_should_match 无效：需要 0 到 %d 个 SHOULD 组", len(query.Should)))
		return result
	}
	sources := booleanEvidence(a, pos)
	for i, group := range query.Must {
		hits := groupHits(sources, group)
		result.Evidence = append(result.Evidence, hits...)
		if len(hits) == 0 {
			result.Eligible = false
			result.Missing = append(result.Missing, fmt.Sprintf("MUST[%d] 未命中：%s", i+1, strings.Join(group, " OR ")))
			continue
		}
		result.Matched = append(result.Matched, fmt.Sprintf("MUST[%d] 命中：%s", i+1, evidenceTerms(hits)))
	}

	shouldMatches := 0
	for i, group := range query.Should {
		hits := groupHits(sources, group)
		result.Evidence = append(result.Evidence, hits...)
		if len(hits) == 0 {
			continue
		}
		shouldMatches++
		result.Matched = append(result.Matched, fmt.Sprintf("SHOULD[%d] 命中：%s", i+1, evidenceTerms(hits)))
	}
	if shouldMatches < query.MinimumShouldMatch {
		result.Eligible = false
		result.Missing = append(result.Missing, fmt.Sprintf("SHOULD 仅命中 %d/%d 组，至少需要 %d 组", shouldMatches, len(query.Should), query.MinimumShouldMatch))
	}

	for i, group := range query.MustNot {
		hits := groupHits(sources, group)
		if len(hits) == 0 {
			continue
		}
		result.Eligible = false
		result.Evidence = append(result.Evidence, hits...)
		result.Exclusions = append(result.Exclusions, fmt.Sprintf("MUST_NOT[%d] 命中：%s", i+1, evidenceTerms(hits)))
	}
	return result
}

func booleanEvidence(a model.Announcement, pos model.Position) []evidenceSource {
	primary := strings.Join([]string{pos.Name, pos.Salary, pos.Location, pos.EmploymentType, pos.Degree, pos.Majors}, " ")
	return positionEvidence(primary, pos, a.RawText, isGenericPosition(pos.Name, a.Company))
}

func legacyQuery(p model.Profile) model.BooleanQuery {
	groups := make([][]string, 0, 4)
	for _, value := range []string{p.Roles, p.Skills, p.Research, p.Major} {
		if strings.TrimSpace(value) != "" {
			groups = append(groups, []string{value})
		}
	}
	return model.BooleanQuery{Should: groups}
}

func normalizeQuery(query model.BooleanQuery) model.BooleanQuery {
	query.Must = normalizeGroups(query.Must)
	query.Should = normalizeGroups(query.Should)
	query.MustNot = normalizeGroups(query.MustNot)
	return query
}

func normalizeGroups(groups [][]string) [][]string {
	out := make([][]string, 0, len(groups))
	for _, group := range groups {
		seen := map[string]bool{}
		termsInGroup := make([]string, 0, len(group))
		for _, raw := range group {
			for _, term := range terms(raw) {
				key := strings.ToLower(term)
				if seen[key] {
					continue
				}
				seen[key] = true
				termsInGroup = append(termsInGroup, term)
			}
		}
		sort.Slice(termsInGroup, func(i, j int) bool {
			return strings.ToLower(termsInGroup[i]) < strings.ToLower(termsInGroup[j])
		})
		if len(termsInGroup) > 0 {
			out = append(out, termsInGroup)
		}
	}
	return out
}

func groupHits(sources []evidenceSource, group []string) []model.MatchEvidence {
	hits := make([]model.MatchEvidence, 0, len(group))
	for _, term := range group {
		if source, ok := bestEvidence(sources, term); ok {
			hits = append(hits, model.MatchEvidence{
				Field:      "BOOLEAN",
				Term:       term,
				Provenance: source.provenance,
				Confidence: source.confidence,
			})
		}
	}
	return hits
}

func evidenceTerms(hits []model.MatchEvidence) string {
	terms := make([]string, 0, len(hits))
	for _, hit := range hits {
		terms = append(terms, hit.Term)
	}
	return strings.Join(terms, "、")
}

func positionEvidence(primary string, pos model.Position, fallback string, allowFallback bool) []evidenceSource {
	sources := make([]evidenceSource, 0, len(pos.Evidence)+2)
	if strings.TrimSpace(primary) != "" {
		sources = append(sources, evidenceSource{text: primary, provenance: model.PositionPrimary, confidence: 1})
	}
	for _, fragment := range pos.Evidence {
		if strings.TrimSpace(fragment.Text) == "" {
			continue
		}
		provenance := fragment.Provenance
		if provenance == "" {
			provenance = model.PositionLocal
		}
		confidence := 0.85
		if provenance == model.PositionPrimary {
			confidence = 1
		}
		sources = append(sources, evidenceSource{text: fragment.Text, provenance: provenance, confidence: confidence})
	}
	if allowFallback && strings.TrimSpace(fallback) != "" {
		sources = append(sources, evidenceSource{
			text:       fallback,
			provenance: model.AnnouncementGlobalFallback,
			confidence: 0.4,
		})
	}
	return sources
}

func semanticField(name, query string, sources []evidenceSource, weight float64) fieldScore {
	q := terms(query)
	if len(q) == 0 {
		return fieldScore{name: name}
	}
	score := 0.0
	hits := []model.MatchEvidence{}
	for _, term := range q {
		if hit, ok := bestEvidence(sources, term); ok {
			score += hit.confidence
			hits = append(hits, model.MatchEvidence{
				Field:      name,
				Term:       term,
				Provenance: hit.provenance,
				Confidence: hit.confidence,
			})
		}
	}
	score /= float64(len(q))
	if len(hits) > 4 {
		hits = hits[:4]
	}
	return fieldScore{
		name:     name,
		weight:   weight,
		score:    score,
		reason:   evidenceReason(name, hits),
		evidence: hits,
		active:   true,
		ok:       score > 0,
	}
}

func bestEvidence(sources []evidenceSource, term string) (evidenceSource, bool) {
	best := evidenceSource{}
	found := false
	for _, source := range sources {
		if !matchesTerm(source.text, term) {
			continue
		}
		if !found || source.confidence > best.confidence {
			best = source
			found = true
		}
	}
	return best, found
}

func evidenceReason(name string, hits []model.MatchEvidence) string {
	if len(hits) == 0 {
		return ""
	}
	parts := make([]string, 0, len(hits))
	for _, hit := range hits {
		parts = append(parts, fmt.Sprintf("%s命中（%s）：%s", name, hit.Provenance, hit.Term))
	}
	return strings.Join(parts, "；")
}

func hardContainsField(name, query, target string, weight float64) fieldScore {
	q := terms(query)
	if len(q) == 0 {
		return fieldScore{name: name}
	}
	if strings.TrimSpace(target) == "" {
		return fieldScore{name: name, weight: weight, score: 0.5, reason: name + "：岗位未注明", active: true, hard: false, ok: false}
	}
	low := strings.ToLower(target)
	for _, term := range q {
		if strings.Contains(low, strings.ToLower(term)) {
			return fieldScore{name: name, weight: weight, score: 1, reason: name + "匹配：" + term, active: true, hard: true, ok: true}
		}
	}
	return fieldScore{name: name, weight: weight, score: 0, active: true, hard: true, ok: false}
}

func cityField(query, location string, sources []evidenceSource, weight float64) fieldScore {
	q := terms(query)
	if len(q) == 0 {
		return fieldScore{name: "城市"}
	}
	for _, term := range q {
		if hit, ok := bestEvidence(sources, term); ok {
			hard := hit.provenance == model.PositionPrimary || hit.provenance == model.PositionLocal || hit.provenance == model.AnnouncementCommon
			return fieldScore{
				name:     "城市",
				weight:   weight,
				score:    hit.confidence,
				reason:   fmt.Sprintf("城市命中（%s）：%s", hit.provenance, term),
				evidence: []model.MatchEvidence{{Field: "城市", Term: term, Provenance: hit.provenance, Confidence: hit.confidence}},
				active:   true,
				hard:     hard,
				ok:       true,
			}
		}
	}
	primaryEmpty := strings.TrimSpace(location) == ""
	return fieldScore{name: "城市", weight: weight, score: 0.5, active: true, hard: !primaryEmpty, ok: false}
}

func isGenericPosition(name, company string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == strings.TrimSpace(company) {
		return true
	}
	for _, marker := range []string{"校园招聘", "招聘公告", "招聘简章", "岗位见正文"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func degreeField(query, target string, weight float64) fieldScore {
	query = strings.TrimSpace(query)
	if query == "" {
		return fieldScore{name: "学历"}
	}
	if strings.TrimSpace(target) == "" || target == "不限" {
		return fieldScore{name: "学历", weight: weight, score: 0.7, reason: "学历：岗位未限制/信息不足", active: true, hard: false, ok: true}
	}
	ok := degreeRank(query) >= degreeRank(target)
	if degreeRank(query) == 0 || degreeRank(target) == 0 {
		ok = strings.Contains(target, query) || strings.Contains(query, target)
	}
	if ok {
		return fieldScore{name: "学历", weight: weight, score: 1, reason: "学历满足：" + target, active: true, hard: true, ok: true}
	}
	return fieldScore{name: "学历", weight: weight, score: 0, active: true, hard: true, ok: false}
}

func gradYearField(year string, sources []evidenceSource, commonText string) fieldScore {
	year = strings.TrimSpace(year)
	if year == "" {
		return fieldScore{name: "毕业年份"}
	}
	if _, err := strconv.Atoi(year); err != nil {
		return fieldScore{name: "毕业年份", active: false}
	}
	short := year
	if len(year) == 4 {
		short = year[2:]
	}
	commonSources := append([]evidenceSource{}, sources...)
	if strings.TrimSpace(commonText) != "" {
		commonSources = append(commonSources, evidenceSource{text: commonText, provenance: model.AnnouncementCommon, confidence: 1})
	}
	for _, source := range commonSources {
		if !strings.Contains(source.text, year+"届") && !strings.Contains(source.text, short+"届") {
			continue
		}
		hard := source.provenance != model.AnnouncementGlobalFallback
		return fieldScore{
			name:     "毕业年份",
			weight:   8,
			score:    source.confidence,
			reason:   fmt.Sprintf("毕业年份命中（%s）：%s届", source.provenance, year),
			evidence: []model.MatchEvidence{{Field: "毕业年份", Term: year + "届", Provenance: source.provenance, Confidence: source.confidence}},
			active:   true,
			hard:     hard,
			ok:       true,
		}
	}
	// Generic wording such as "应届毕业生" is not a numeric cohort restriction.
	hasCohort := false
	for _, source := range commonSources {
		if cohortPattern.MatchString(source.text) {
			hasCohort = true
			break
		}
	}
	if !hasCohort {
		return fieldScore{name: "毕业年份", weight: 8, score: 0.5, reason: "毕业年份：公告未明确限制", active: true, hard: false, ok: true}
	}
	return fieldScore{name: "毕业年份", weight: 8, score: 0, active: true, hard: true, ok: false}
}

func matchesTerm(target, term string) bool {
	target = strings.ToLower(target)
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return false
	}
	if containsTerm(target, term) {
		return true
	}
	for _, group := range semanticAliasGroups {
		related := false
		for _, alias := range group {
			alias = strings.ToLower(alias)
			if containsTerm(term, alias) || containsTerm(alias, term) {
				related = true
				break
			}
		}
		if !related {
			continue
		}
		for _, alias := range group {
			if containsTerm(target, strings.ToLower(alias)) {
				return true
			}
		}
	}
	return false
}

func containsTerm(target, term string) bool {
	if term == "" {
		return false
	}
	if !shortASCIIWord(term) {
		return strings.Contains(target, term)
	}
	for offset := 0; offset < len(target); {
		index := strings.Index(target[offset:], term)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isASCIIWordByte(target[index-1])
		after := index + len(term)
		afterOK := after == len(target) || !isASCIIWordByte(target[after])
		if beforeOK && afterOK {
			return true
		}
		offset = index + 1
	}
	return false
}

func shortASCIIWord(s string) bool {
	if len(s) == 0 || len(s) > 3 {
		return false
	}
	for i := range len(s) {
		if !isASCIIWordByte(s[i]) {
			return false
		}
	}
	return true
}

func isASCIIWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func degreeRank(s string) int {
	switch {
	case strings.Contains(s, "博士"):
		return 3
	case strings.Contains(s, "硕士"), strings.Contains(s, "研究生"):
		return 2
	case strings.Contains(s, "本科"):
		return 1
	default:
		return 0
	}
}

func terms(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",，;；、/|+\n\t", r)
	})
	seen := map[string]bool{}
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len([]rune(p)) < 2 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
