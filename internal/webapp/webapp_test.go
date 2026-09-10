package webapp

import (
	"path/filepath"
	"testing"

	"neu-job-finder/internal/model"
	"neu-job-finder/internal/store"
)

func TestAnnouncementBatchCollectorFlushesBoundedBatches(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	collector := newAnnouncementBatchCollector(st, 2)
	if inserted, updated, err := collector.Add(model.Announcement{ID: "801"}); err != nil || inserted != 0 || updated != 0 {
		t.Fatalf("first add=%d/%d err=%v", inserted, updated, err)
	}
	if len(st.All()) != 0 {
		t.Fatal("collector persisted before reaching its batch limit")
	}
	if inserted, updated, err := collector.Add(model.Announcement{ID: "802"}); err != nil || inserted != 2 || updated != 0 {
		t.Fatalf("second add=%d/%d err=%v", inserted, updated, err)
	}
	if len(st.All()) != 2 {
		t.Fatalf("flushed announcements=%d", len(st.All()))
	}
	if _, _, err := collector.Add(model.Announcement{ID: "803"}); err != nil {
		t.Fatal(err)
	}
	pending := collector.Pending()
	if len(pending) != 1 || pending[0].ID != "803" {
		t.Fatalf("pending=%#v", pending)
	}
	if len(collector.Pending()) != 0 {
		t.Fatal("pending items were not drained")
	}
}
