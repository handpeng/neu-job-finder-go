package matcher

import (
	"neu-job-finder/internal/model"
	"strings"
	"testing"
	"time"
)

func TestNoProfileMeansUnscored(t *testing.T) {
	v := Score(model.Announcement{}, model.Position{Name: "算法工程师"}, model.Profile{})
	if v.Score != nil {
		t.Fatalf("expected nil score, got %v", *v.Score)
	}
}

func TestOptionalFieldsOnly(t *testing.T) {
	v := Score(model.Announcement{RawText: "机器学习 Python"}, model.Position{Name: "算法工程师", Location: "北京市", Degree: "硕士", Majors: "冶金工程 人工智能 Python"}, model.Profile{Skills: "Python", Roles: "算法工程师"})
	if v.Score == nil || *v.Score < 90 {
		t.Fatalf("score=%v", v.Score)
	}
}

func TestStrictDegreeMismatch(t *testing.T) {
	v := Score(model.Announcement{}, model.Position{Name: "岗位", Degree: "博士"}, model.Profile{Degree: "硕士", Strict: true})
	if len(v.HardMismatch) == 0 {
		t.Fatal("expected hard mismatch")
	}
}

func TestGenericFreshGraduateDoesNotRejectYear(t *testing.T) {
	v := Score(
		model.Announcement{RawText: "面向本科及以上应届毕业生"},
		model.Position{Name: "研发工程师"},
		model.Profile{GraduationYear: "2027", Strict: true},
	)
	if len(v.HardMismatch) != 0 {
		t.Fatalf("unexpected mismatch: %v", v.HardMismatch)
	}
}

func TestMissingCityIsNotHardMismatch(t *testing.T) {
	v := Score(
		model.Announcement{},
		model.Position{Name: "研发工程师"},
		model.Profile{Cities: "北京", Strict: true},
	)
	if len(v.HardMismatch) != 0 {
		t.Fatalf("unexpected mismatch: %v", v.HardMismatch)
	}
}

func TestAnnouncementOnlyRoleDoesNotRaiseOtherPosition(t *testing.T) {
	v := Score(
		model.Announcement{RawText: "同时招聘算法工程师和行政专员"},
		model.Position{Name: "行政专员"},
		model.Profile{Roles: "算法工程师"},
	)
	if v.Score == nil || *v.Score != 0 {
		t.Fatalf("score=%v matched=%v", v.Score, v.Matched)
	}
}

func TestAnnouncementWideSemanticEvidenceIsNotInherited(t *testing.T) {
	a := model.Announcement{
		RawText: "冶金智能算法工程师 Python 机器学习 转炉 数据驱动；行政专员 财务专员 销售 法务",
		Positions: []model.Position{
			{
				ID:     "a:1",
				Name:   "冶金智能算法工程师",
				Majors: "冶金工程",
				Evidence: []model.EvidenceFragment{{
					Text:       "Python 机器学习 转炉 数据驱动",
					Provenance: model.PositionLocal,
				}},
			},
			{ID: "a:2", Name: "行政专员"},
		},
	}
	p := model.Profile{Roles: "算法工程师", Skills: "Python 机器学习", Research: "转炉 数据驱动"}
	relevant := Score(a, a.Positions[0], p)
	unrelated := Score(a, a.Positions[1], p)
	if relevant.Score == nil || *relevant.Score == 0 {
		t.Fatalf("relevant position did not receive local evidence: score=%v evidence=%v", relevant.Score, relevant.MatchEvidence)
	}
	if unrelated.Score == nil || *unrelated.Score != 0 || len(unrelated.MatchEvidence) != 0 {
		t.Fatalf("unrelated position inherited announcement evidence: score=%v evidence=%v matched=%v", unrelated.Score, unrelated.MatchEvidence, unrelated.Matched)
	}
}

func TestGenericFallbackEvidenceIsExplicitAndCapped(t *testing.T) {
	v := Score(
		model.Announcement{RawText: "岗位见正文：机器学习 Python"},
		model.Position{Name: "招聘公告（岗位见正文）"},
		model.Profile{Skills: "机器学习 Python"},
	)
	if len(v.MatchEvidence) == 0 {
		t.Fatal("expected generic fallback evidence")
	}
	for _, evidence := range v.MatchEvidence {
		if evidence.Provenance != model.AnnouncementGlobalFallback {
			t.Fatalf("unexpected provenance: %#v", evidence)
		}
		if evidence.Confidence >= 1 {
			t.Fatalf("generic fallback was not confidence-capped: %#v", evidence)
		}
	}
}

func TestAmbiguousExtractedFieldsDoNotBecomeVerifiedEvidence(t *testing.T) {
	position := model.Position{
		Name:              "算法工程师",
		Degree:            "硕士",
		Location:          "北京",
		ExtractionQuality: model.ExtractionAmbiguous,
		FieldQuality: map[string]model.ExtractionQuality{
			model.PositionFieldName:     model.ExtractionConfident,
			model.PositionFieldDegree:   model.ExtractionAmbiguous,
			model.PositionFieldLocation: model.ExtractionAmbiguous,
		},
	}
	v := Score(model.Announcement{}, position, model.Profile{
		Query: model.BooleanQuery{Must: [][]string{{"硕士"}}},
	})
	if v.Eligible {
		t.Fatalf("ambiguous degree satisfied MUST query: %#v", v)
	}
	for _, evidence := range v.MatchEvidence {
		if evidence.Term == "硕士" || evidence.Provenance == model.PositionPrimary {
			t.Fatalf("ambiguous field became verified evidence: %#v", v.MatchEvidence)
		}
	}
}

func TestFallbackExtractionUsesDistinctEvidenceProvenance(t *testing.T) {
	v := Score(model.Announcement{}, model.Position{
		Name:              "研发工程师",
		ExtractionQuality: model.ExtractionFallback,
	}, model.Profile{Roles: "研发工程师"})
	if len(v.MatchEvidence) == 0 {
		t.Fatal("expected fallback evidence")
	}
	foundFallback := false
	for _, evidence := range v.MatchEvidence {
		if evidence.Provenance == model.PositionFallback {
			foundFallback = true
			if evidence.Confidence >= 1 {
				t.Fatalf("fallback evidence retained verified confidence: %#v", evidence)
			}
		}
	}
	if !foundFallback {
		t.Fatalf("fallback provenance missing: %#v", v.MatchEvidence)
	}
}

func TestGenericRecruitmentUsesAnnouncementRolesAndCities(t *testing.T) {
	v := Score(
		model.Announcement{
			Company: "中冶赛迪集团有限公司",
			RawText: "研发类岗位：人工智能、冶金等相关专业。工作地点：重庆、上海、北京。2027年应届硕博毕业生。",
		},
		model.Position{
			Name:     "中冶赛迪集团有限公司2027届校园招聘",
			Location: "重庆市市辖区",
			Degree:   "硕士",
			Majors:   "人工智能、冶金工程",
		},
		model.Profile{
			Research:       "转炉智能炼钢、工业人工智能",
			Degree:         "博士",
			GraduationYear: "2027",
			Major:          "冶金工程",
			Cities:         "北京",
			Roles:          "研发工程师、算法工程师",
			Strict:         true,
		},
	)
	if len(v.HardMismatch) != 0 {
		t.Fatalf("unexpected mismatch: %v", v.HardMismatch)
	}
	if v.Score == nil || *v.Score < 70 {
		t.Fatalf("score=%v matched=%v missing=%v", v.Score, v.Matched, v.Missing)
	}
}

func TestResearchAliases(t *testing.T) {
	v := Score(
		model.Announcement{},
		model.Position{Name: "钢铁工艺研发", Majors: "冶金工程"},
		model.Profile{Research: "转炉智能炼钢"},
	)
	if v.Score == nil || *v.Score == 0 {
		t.Fatalf("score=%v", v.Score)
	}
}

func TestShortAIDoesNotMatchEmail(t *testing.T) {
	v := Score(
		model.Announcement{RawText: "联系邮箱 hr@example.com"},
		model.Position{Name: "行政专员"},
		model.Profile{Research: "AI"},
	)
	if v.Score == nil || *v.Score != 0 {
		t.Fatalf("score=%v matched=%v", v.Score, v.Matched)
	}
}

func TestBooleanTruthTable(t *testing.T) {
	a := model.Announcement{}
	pos := model.Position{
		Name: "冶金算法工程师",
		Evidence: []model.EvidenceFragment{{
			Text:       "钢铁 机器学习 Python",
			Provenance: model.PositionLocal,
		}},
	}
	cases := []struct {
		name  string
		query model.BooleanQuery
		want  bool
	}{
		{
			name:  "must groups form AND and terms form OR",
			query: model.BooleanQuery{Must: [][]string{{"冶金", "钢铁"}, {"人工智能", "机器学习"}}},
			want:  true,
		},
		{
			name:  "must miss",
			query: model.BooleanQuery{Must: [][]string{{"行政"}}},
			want:  false,
		},
		{
			name:  "should optional",
			query: model.BooleanQuery{Should: [][]string{{"行政"}}},
			want:  true,
		},
		{
			name:  "should threshold",
			query: model.BooleanQuery{Should: [][]string{{"冶金"}, {"机器学习"}, {"行政"}}, MinimumShouldMatch: 3},
			want:  false,
		},
		{
			name:  "must not miss",
			query: model.BooleanQuery{MustNot: [][]string{{"销售", "行政"}}},
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(a, pos, tc.query).Eligible; got != tc.want {
				t.Fatalf("eligible=%v, want %v; query=%#v", got, tc.want, tc.query)
			}
		})
	}

	excluded := Evaluate(a, model.Position{Name: "销售", Evidence: []model.EvidenceFragment{{Text: "Python", Provenance: model.PositionLocal}}}, model.BooleanQuery{
		Should:  [][]string{{"Python"}},
		MustNot: [][]string{{"销售"}},
	})
	if excluded.Eligible || len(excluded.Exclusions) != 1 {
		t.Fatalf("MUST_NOT should fail closed: %#v", excluded)
	}
}

func TestBooleanTermOrderDoesNotChangeEligibility(t *testing.T) {
	a := model.Announcement{}
	pos := model.Position{Name: "算法工程师", Evidence: []model.EvidenceFragment{{Text: "机器学习", Provenance: model.PositionLocal}}}
	left := Evaluate(a, pos, model.BooleanQuery{
		Must:               [][]string{{"冶金", "钢铁", "炼钢"}, {"深度学习", "机器学习"}},
		MustNot:            [][]string{{"销售", "行政"}},
		Should:             [][]string{{"Python", "算法工程师"}},
		MinimumShouldMatch: 1,
	})
	right := Evaluate(a, pos, model.BooleanQuery{
		Must:               [][]string{{"钢铁", "炼钢", "冶金"}, {"机器学习", "深度学习"}},
		MustNot:            [][]string{{"行政", "销售"}},
		Should:             [][]string{{"算法工程师", "Python"}},
		MinimumShouldMatch: 1,
	})
	if left.Eligible != right.Eligible || strings.Join(left.Matched, "|") != strings.Join(right.Matched, "|") {
		t.Fatalf("term order changed result: left=%#v right=%#v", left, right)
	}
}

func TestBooleanAliasMatch(t *testing.T) {
	result := Evaluate(model.Announcement{}, model.Position{Name: "算法工程师"}, model.BooleanQuery{
		Must: [][]string{{"人工智能"}},
	})
	if !result.Eligible || len(result.Matched) == 0 {
		t.Fatalf("expected alias match: %#v", result)
	}
}

func TestLegacyProfileMapsToOptionalBooleanGroups(t *testing.T) {
	v := Score(model.Announcement{}, model.Position{Name: "算法工程师"}, model.Profile{Roles: "算法工程师", Skills: "Python"})
	if !v.Eligible || v.Score == nil || len(v.BooleanMatched) == 0 {
		t.Fatalf("legacy profile compatibility failed: %#v", v)
	}
}

func TestRankingUsesPublishedDateThenStableID(t *testing.T) {
	oldSeen := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	newSeen := oldSeen.Add(24 * time.Hour)
	items := []model.Announcement{
		{ID: "old", PublishedDate: "2026-09-01", LastSeenAt: newSeen, Positions: []model.Position{{ID: "old:1", Name: "算法工程师"}}},
		{ID: "new", PublishedDate: "2026-09-02", LastSeenAt: oldSeen, Positions: []model.Position{{ID: "new:1", Name: "算法工程师"}}},
		{ID: "same", PublishedDate: "2026-09-02", Positions: []model.Position{{ID: "same:1", Name: "算法工程师"}}},
	}
	jobs := Flatten(items, model.Profile{Roles: "算法工程师"})
	if len(jobs) != 3 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	if jobs[0].Announcement.ID != "new" || jobs[1].Announcement.ID != "same" || jobs[2].Announcement.ID != "old" {
		t.Fatalf("unexpected order: %s, %s, %s", jobs[0].Announcement.ID, jobs[1].Announcement.ID, jobs[2].Announcement.ID)
	}
}

func TestMinScoreAndTopNOnlyLimitResults(t *testing.T) {
	items := []model.Announcement{
		{ID: "a", PublishedDate: "2026-09-01", Positions: []model.Position{{ID: "a:1", Name: "算法工程师"}}},
		{ID: "b", PublishedDate: "2026-09-02", Positions: []model.Position{{ID: "b:1", Name: "算法工程师"}}},
		{ID: "c", PublishedDate: "2026-09-03", Positions: []model.Position{{ID: "c:1", Name: "算法工程师"}}},
	}
	minimum := 90
	jobs := FlattenWithOptions(items, model.Profile{Roles: "算法工程师"}, ResultOptions{MinScore: &minimum, TopN: 2})
	if len(items) != 3 {
		t.Fatalf("result controls changed source input length: %d", len(items))
	}
	if len(jobs) != 2 || jobs[0].Announcement.ID != "c" || jobs[1].Announcement.ID != "b" {
		t.Fatalf("jobs=%#v", jobs)
	}
	low := 101
	if jobs := FlattenWithOptions(items, model.Profile{Roles: "算法工程师"}, ResultOptions{MinScore: &low}); len(jobs) != 0 {
		t.Fatalf("min_score should filter every result: %#v", jobs)
	}
}

func TestBroadProfileDoesNotMechanicallyDiluteRelevantEvidence(t *testing.T) {
	items := []model.Announcement{
		{
			ID: "relevant",
			Positions: []model.Position{{
				ID:     "relevant:1",
				Name:   "钢铁冶金智能制造算法工程师",
				Majors: "冶金工程",
				Evidence: []model.EvidenceFragment{{
					Text:       "Python 机器学习 工业人工智能 转炉 数据驱动",
					Provenance: model.PositionLocal,
				}},
			}},
		},
		{ID: "unrelated", Positions: []model.Position{{ID: "unrelated:1", Name: "行政专员"}}},
	}
	p := model.Profile{
		Roles:    "算法工程师、研发工程师、工艺工程师",
		Skills:   "Python、Java、Go、Rust、SQL、Kubernetes、Docker",
		Research: "冶金、转炉、机器学习、深度学习、数字孪生、数据驱动",
		Major:    "冶金工程、材料工程、计算机科学",
	}
	jobs := Flatten(items, p)
	if len(jobs) != 2 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	if jobs[0].Announcement.ID != "relevant" {
		t.Fatalf("relevant job was not ranked first: %#v", jobs)
	}
	if jobs[0].Score == nil || jobs[1].Score == nil || *jobs[0].Score <= *jobs[1].Score {
		t.Fatalf("scores do not separate controls: relevant=%v unrelated=%v", jobs[0].Score, jobs[1].Score)
	}
}
