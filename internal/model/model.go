package model

import "time"

type EvidenceProvenance string

const (
	PositionPrimary            EvidenceProvenance = "POSITION_PRIMARY"
	PositionLocal              EvidenceProvenance = "POSITION_LOCAL"
	AnnouncementCommon         EvidenceProvenance = "ANNOUNCEMENT_COMMON"
	AnnouncementGlobalFallback EvidenceProvenance = "ANNOUNCEMENT_GLOBAL_FALLBACK"
)

type EvidenceFragment struct {
	Text       string             `json:"text,omitempty"`
	Provenance EvidenceProvenance `json:"provenance"`
}

type MatchEvidence struct {
	Field      string             `json:"field"`
	Term       string             `json:"term"`
	Provenance EvidenceProvenance `json:"provenance"`
	Confidence float64            `json:"confidence"`
}

// BooleanQuery uses one or more term groups per clause. Terms within a group
// are ORed; groups in MUST are ANDed. A SHOULD group counts as one match when
// any of its terms matches, subject to MinimumShouldMatch.
type BooleanQuery struct {
	Must               [][]string `json:"must,omitempty"`
	Should             [][]string `json:"should,omitempty"`
	MustNot            [][]string `json:"must_not,omitempty"`
	MinimumShouldMatch int        `json:"minimum_should_match,omitempty"`
}

func (q BooleanQuery) HasClauses() bool {
	return len(q.Must) > 0 || len(q.Should) > 0 || len(q.MustNot) > 0 || q.MinimumShouldMatch != 0
}

type Announcement struct {
	ID                      string     `json:"id"`
	Company                 string     `json:"company"`
	DetailURL               string     `json:"detail_url"`
	ExpireDate              string     `json:"expire_date,omitempty"`
	PublishedDate           string     `json:"published_date,omitempty"`
	CommonText              string     `json:"common_text,omitempty"`
	ApplicationURL          string     `json:"application_url,omitempty"`
	Email                   string     `json:"email,omitempty"`
	EmailSubject            string     `json:"email_subject,omitempty"`
	ApplicationRequirements string     `json:"application_requirements,omitempty"`
	RawText                 string     `json:"raw_text,omitempty"`
	Positions               []Position `json:"positions"`
	FirstSeenAt             time.Time  `json:"first_seen_at"`
	LastSeenAt              time.Time  `json:"last_seen_at"`
}

type Position struct {
	ID             string             `json:"id"`
	AnnouncementID string             `json:"announcement_id"`
	Name           string             `json:"name"`
	Salary         string             `json:"salary,omitempty"`
	Location       string             `json:"location,omitempty"`
	EmploymentType string             `json:"employment_type,omitempty"`
	Degree         string             `json:"degree,omitempty"`
	Majors         string             `json:"majors,omitempty"`
	Evidence       []EvidenceFragment `json:"evidence,omitempty"`
}

type Profile struct {
	Research       string       `json:"research,omitempty"`
	Skills         string       `json:"skills,omitempty"`
	Degree         string       `json:"degree,omitempty"`
	GraduationYear string       `json:"graduation_year,omitempty"`
	Major          string       `json:"major,omitempty"`
	Cities         string       `json:"cities,omitempty"`
	Roles          string       `json:"roles,omitempty"`
	Strict         bool         `json:"strict"`
	Query          BooleanQuery `json:"query,omitempty"`
}

type JobView struct {
	Announcement   Announcement    `json:"announcement"`
	Position       Position        `json:"position"`
	Score          *int            `json:"score,omitempty"`
	Matched        []string        `json:"matched,omitempty"`
	MatchEvidence  []MatchEvidence `json:"match_evidence,omitempty"`
	BooleanMatched []string        `json:"boolean_matched,omitempty"`
	BooleanMissing []string        `json:"boolean_missing,omitempty"`
	Exclusions     []string        `json:"exclusions,omitempty"`
	Eligible       bool            `json:"eligible"`
	Missing        []string        `json:"missing,omitempty"`
	HardMismatch   []string        `json:"hard_mismatch,omitempty"`
}

type CrawlFailure struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type CrawlRun struct {
	RunID                string         `json:"run_id"`
	RequestedStartDate   string         `json:"requested_start_date,omitempty"`
	RequestedEndDate     string         `json:"requested_end_date,omitempty"`
	Keyword              string         `json:"keyword,omitempty"`
	RetryIDs             []string       `json:"retry_ids,omitempty"`
	ForceRefresh         bool           `json:"force_refresh"`
	StartedAt            time.Time      `json:"started_at"`
	FinishedAt           time.Time      `json:"finished_at,omitempty"`
	Status               string         `json:"status"`
	Error                string         `json:"error,omitempty"`
	PagesScanned         int            `json:"pages_scanned"`
	EntriesSeen          int            `json:"entries_seen"`
	UniqueIDs            int            `json:"unique_ids"`
	InRangeIDs           int            `json:"in_range_ids"`
	DuplicateIDs         int            `json:"duplicate_ids"`
	UndatedIDs           int            `json:"undated_ids"`
	NewIDs               int            `json:"new_ids"`
	RefreshedIDs         int            `json:"refreshed_ids"`
	SkippedCachedIDs     int            `json:"skipped_cached_ids"`
	DetailsAttempted     int            `json:"details_attempted"`
	DetailsSucceeded     int            `json:"details_succeeded"`
	FilteredIDs          int            `json:"filtered_ids"`
	AcceptedIDs          int            `json:"accepted_ids"`
	FailedIDs            int            `json:"failed_ids"`
	FailedDetails        []CrawlFailure `json:"failed_details,omitempty"`
	CancellationObserved bool           `json:"cancellation_observed"`
}
