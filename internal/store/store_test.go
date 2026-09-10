package store

import (
	"os"
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

func TestUpsertBatchAndRunLedgerPersistAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	run := model.CrawlRun{
		RunID:     "run-batch",
		StartedAt: started,
		Status:    "running",
		RetryIDs:  []string{"701"},
		Keyword:   "人工智能",
	}
	if err := st.BeginRun(run); err != nil {
		t.Fatal(err)
	}
	inserted, updated, err := st.UpsertBatch([]model.Announcement{
		{ID: "701", Company: "旧名称"},
		{ID: "701", Company: "最终名称"},
		{ID: "702", Company: "第二家公司"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 2 || updated != 0 {
		t.Fatalf("batch counts=%d/%d", inserted, updated)
	}
	got := st.All()
	if len(got) != 2 {
		t.Fatalf("batch contents=%#v", got)
	}
	for _, item := range got {
		if item.ID == "701" && item.Company != "最终名称" {
			t.Fatalf("duplicate ID was not reduced to final batch item: %#v", got)
		}
	}

	final := run
	final.Status = "partial"
	final.FinishedAt = time.Now().UTC()
	final.DetailsAttempted = 2
	final.DetailsSucceeded = 1
	final.FailedIDs = 1
	final.FailedDetails = []model.CrawlFailure{{ID: "701", Reason: "temporary HTTP failure"}}
	inserted, updated, err = st.UpsertBatchAndFinishRun([]model.Announcement{{ID: "702", Company: "更新名称"}}, final)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 0 || updated != 1 {
		t.Fatalf("final batch counts=%d/%d", inserted, updated)
	}
	gotRun, ok := st.Run("run-batch")
	if !ok || gotRun.Status != "partial" || len(gotRun.FailedDetails) != 1 {
		t.Fatalf("run=%+v exists=%v", gotRun, ok)
	}
	if gotRun.StartedAt != started {
		t.Fatalf("started time changed: %s", gotRun.StartedAt)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.All()) != 2 {
		t.Fatalf("reopened announcements=%d", len(reopened.All()))
	}
	reopenedRun, ok := reopened.Run("run-batch")
	if !ok || len(reopenedRun.FailedDetails) != 1 || reopenedRun.FailedDetails[0].ID != "701" {
		t.Fatalf("reopened run=%+v exists=%v", reopenedRun, ok)
	}
}

func TestOpenReadsLegacyStoreWithoutRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	legacy := []byte(`{"announcements":[{"id":"legacy-1","company":"旧数据"}],"updated_at":"2026-09-10T00:00:00Z"}`)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.All()) != 1 || st.All()[0].ID != "legacy-1" {
		t.Fatalf("legacy announcements=%#v", st.All())
	}
	if len(st.Runs()) != 0 {
		t.Fatalf("legacy runs=%#v", st.Runs())
	}
}
