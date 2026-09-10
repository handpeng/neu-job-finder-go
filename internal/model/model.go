package model

import "time"

type Announcement struct {
	ID                      string     `json:"id"`
	Company                 string     `json:"company"`
	DetailURL               string     `json:"detail_url"`
	ExpireDate              string     `json:"expire_date,omitempty"`
	PublishedDate           string     `json:"published_date,omitempty"`
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
	ID             string `json:"id"`
	AnnouncementID string `json:"announcement_id"`
	Name           string `json:"name"`
	Salary         string `json:"salary,omitempty"`
	Location       string `json:"location,omitempty"`
	EmploymentType string `json:"employment_type,omitempty"`
	Degree         string `json:"degree,omitempty"`
	Majors         string `json:"majors,omitempty"`
}

type Profile struct {
	Research       string `json:"research,omitempty"`
	Skills         string `json:"skills,omitempty"`
	Degree         string `json:"degree,omitempty"`
	GraduationYear string `json:"graduation_year,omitempty"`
	Major          string `json:"major,omitempty"`
	Cities         string `json:"cities,omitempty"`
	Roles          string `json:"roles,omitempty"`
	Strict         bool   `json:"strict"`
}

type JobView struct {
	Announcement Announcement `json:"announcement"`
	Position     Position     `json:"position"`
	Score        *int         `json:"score,omitempty"`
	Matched      []string     `json:"matched,omitempty"`
	Missing      []string     `json:"missing,omitempty"`
	HardMismatch []string     `json:"hard_mismatch,omitempty"`
}
