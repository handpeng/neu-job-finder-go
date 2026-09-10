package matcher

import (
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
	name   string
	weight float64
	score  float64
	reason string
	active bool
	hard   bool
	ok     bool
}

func Flatten(items []model.Announcement, p model.Profile) []model.JobView {
	out := []model.JobView{}
	for _, a := range items {
		for _, pos := range a.Positions {
			v := Score(a, pos, p)
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
	v := model.JobView{Announcement: a, Position: pos}
	positionText := strings.Join([]string{pos.Name, pos.Location, pos.Degree, pos.Majors}, " ")
	context := ""
	if isGenericPosition(pos.Name, a.Company) {
		context = a.RawText
	}
	fields := []fieldScore{
		semanticField("意向岗位", p.Roles, pos.Name, context, 25),
		semanticField("核心技能", p.Skills, positionText, a.RawText, 22),
		semanticField("研究方向", p.Research, pos.Name+" "+pos.Majors, a.RawText, 18),
		semanticField("专业", p.Major, pos.Majors, a.RawText, 13),
		cityField(p.Cities, pos.Location, pos.Name, context, 12),
		degreeField(p.Degree, pos.Degree, 10),
		gradYearField(p.GraduationYear, a.RawText),
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

func semanticField(name, query, primary, context string, weight float64) fieldScore {
	q := terms(query)
	if len(q) == 0 {
		return fieldScore{name: name}
	}
	score := 0.0
	hits := []string{}
	contextHits := []string{}
	for _, term := range q {
		if matchesTerm(primary, term) {
			score += 1
			hits = append(hits, term)
		} else if context != "" && matchesTerm(context, term) {
			score += 0.4
			contextHits = append(contextHits, term)
		}
	}
	score /= float64(len(q))
	if len(hits) > 4 {
		hits = hits[:4]
	}
	if len(contextHits) > 4 {
		contextHits = contextHits[:4]
	}
	reason := ""
	if len(hits) > 0 {
		reason = name + "命中（岗位）：" + strings.Join(hits, "、")
	}
	if len(contextHits) > 0 {
		if reason != "" {
			reason += "；"
		}
		reason += name + "命中（公告）：" + strings.Join(contextHits, "、")
	}
	return fieldScore{name: name, weight: weight, score: score, reason: reason, active: true, ok: score > 0}
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

func cityField(query, location, positionName, context string, weight float64) fieldScore {
	field := hardContainsField("城市", query, location+" "+positionName, weight)
	if !field.active || field.ok {
		return field
	}
	for _, term := range terms(query) {
		if context != "" && strings.Contains(strings.ToLower(context), strings.ToLower(term)) {
			return fieldScore{name: "城市", weight: weight, score: 0.7, reason: "城市命中（公告）：" + term, active: true, ok: true}
		}
	}
	if strings.TrimSpace(location) == "" {
		field.hard = false
		field.score = 0.5
	}
	return field
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

func gradYearField(year, target string) fieldScore {
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
	if strings.Contains(target, year+"届") || strings.Contains(target, short+"届") {
		return fieldScore{name: "毕业年份", weight: 8, score: 1, reason: "毕业年份命中：" + year + "届", active: true, hard: true, ok: true}
	}
	// Generic wording such as "应届毕业生" is not a numeric cohort restriction.
	if !cohortPattern.MatchString(target) {
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
