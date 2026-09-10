package matcher

import (
	"neu-job-finder/internal/model"
	"testing"
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
