package store

import (
	"path/filepath"
	"testing"
	"time"

	"neu-job-finder/internal/model"
)

func TestUpsertPreservesFirstSeenAndPublishedDateOnRefresh(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	firstSeen := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	initial := model.Announcement{
		ID:            "501",
		PublishedDate: "2026-09-01",
		FirstSeenAt:   firstSeen,
		Positions:     []model.Position{{ID: "501:1", AnnouncementID: "501", Name: "初始岗位"}},
	}
	if _, _, err := st.Upsert([]model.Announcement{initial}); err != nil {
		t.Fatal(err)
	}

	refreshed := model.Announcement{
		ID:            "501",
		PublishedDate: "2026-09-01",
		Positions:     []model.Position{{ID: "501:1", AnnouncementID: "501", Name: "更新后的岗位"}},
	}
	if _, _, err := st.Upsert([]model.Announcement{refreshed}); err != nil {
		t.Fatal(err)
	}
	items := st.All()
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	got := items[0]
	if !got.FirstSeenAt.Equal(firstSeen) {
		t.Fatalf("first seen changed from %s to %s", firstSeen, got.FirstSeenAt)
	}
	if got.PublishedDate != initial.PublishedDate {
		t.Fatalf("published date changed from %q to %q", initial.PublishedDate, got.PublishedDate)
	}
	if got.LastSeenAt.IsZero() || !got.LastSeenAt.After(firstSeen) {
		t.Fatalf("last seen was not refreshed: %s", got.LastSeenAt)
	}
	if got.Positions[0].Name != "更新后的岗位" {
		t.Fatalf("refreshed position was not stored: %#v", got.Positions)
	}
}
