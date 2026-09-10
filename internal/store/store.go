package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"neu-job-finder/internal/model"
)

type diskState struct {
	Announcements []model.Announcement `json:"announcements"`
	Runs          []model.CrawlRun     `json:"runs,omitempty"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	byID map[string]model.Announcement
	runs map[string]model.CrawlRun
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, byID: make(map[string]model.Announcement), runs: make(map[string]model.CrawlRun)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st diskState
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	for _, a := range st.Announcements {
		s.byID[a.ID] = a
	}
	for _, run := range st.Runs {
		if run.RunID != "" {
			s.runs[run.RunID] = run
		}
	}
	return nil
}

func (s *Store) Upsert(items []model.Announcement) (inserted, updated int, err error) {
	return s.UpsertBatch(items)
}

func (s *Store) UpsertBatch(items []model.Announcement) (inserted, updated int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inserted, updated = s.upsertBatchLocked(items)
	if inserted+updated == 0 {
		return 0, 0, nil
	}
	return inserted, updated, s.saveLocked()
}

func (s *Store) upsertBatchLocked(items []model.Announcement) (inserted, updated int) {
	items = uniqueAnnouncements(items)
	if len(items) == 0 {
		return 0, 0
	}
	now := time.Now()
	for _, a := range items {
		old, ok := s.byID[a.ID]
		if ok {
			a.FirstSeenAt = old.FirstSeenAt
			if a.FirstSeenAt.IsZero() {
				a.FirstSeenAt = now
			}
			a.LastSeenAt = now
			s.byID[a.ID] = a
			updated++
			continue
		}
		if a.FirstSeenAt.IsZero() {
			a.FirstSeenAt = now
		}
		a.LastSeenAt = now
		s.byID[a.ID] = a
		inserted++
	}
	return inserted, updated
}

func uniqueAnnouncements(items []model.Announcement) []model.Announcement {
	byID := make(map[string]model.Announcement, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if _, exists := byID[item.ID]; !exists {
			order = append(order, item.ID)
		}
		byID[item.ID] = item
	}
	out := make([]model.Announcement, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func (s *Store) BeginRun(run model.CrawlRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.RunID == "" {
		return errors.New("crawl run ID is required")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if run.Status == "" {
		run.Status = "running"
	}
	if s.runs == nil {
		s.runs = make(map[string]model.CrawlRun)
	}
	s.runs[run.RunID] = cloneRun(run)
	return s.saveLocked()
}

func (s *Store) FinishRun(run model.CrawlRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.finishRunLocked(run); err != nil {
		return err
	}
	return s.saveLocked()
}

func (s *Store) UpsertBatchAndFinishRun(items []model.Announcement, run model.CrawlRun) (inserted, updated int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.RunID == "" {
		return 0, 0, errors.New("crawl run ID is required")
	}
	inserted, updated = s.upsertBatchLocked(items)
	if err := s.finishRunLocked(run); err != nil {
		return inserted, updated, err
	}
	return inserted, updated, s.saveLocked()
}

func (s *Store) finishRunLocked(run model.CrawlRun) error {
	if run.RunID == "" {
		return errors.New("crawl run ID is required")
	}
	if previous, ok := s.runs[run.RunID]; ok {
		if run.StartedAt.IsZero() {
			run.StartedAt = previous.StartedAt
		}
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now().UTC()
	}
	if run.Status == "" {
		run.Status = "completed"
	}
	if s.runs == nil {
		s.runs = make(map[string]model.CrawlRun)
	}
	s.runs[run.RunID] = cloneRun(run)
	return nil
}

func (s *Store) Runs() []model.CrawlRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runs := make([]model.CrawlRun, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, cloneRun(run))
	}
	sort.Slice(runs, func(i, j int) bool {
		left, right := runs[i], runs[j]
		if left.StartedAt.Equal(right.StartedAt) {
			return left.RunID > right.RunID
		}
		return left.StartedAt.After(right.StartedAt)
	})
	return runs
}

func (s *Store) Run(runID string) (model.CrawlRun, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok {
		return model.CrawlRun{}, false
	}
	return cloneRun(run), true
}

func cloneRun(run model.CrawlRun) model.CrawlRun {
	run.RetryIDs = append([]string(nil), run.RetryIDs...)
	run.FailedDetails = append([]model.CrawlFailure(nil), run.FailedDetails...)
	return run
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	items := make([]model.Announcement, 0, len(s.byID))
	for _, a := range s.byID {
		items = append(items, a)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastSeenAt.After(items[j].LastSeenAt)
	})
	runs := make([]model.CrawlRun, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, cloneRun(run))
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	b, err := json.MarshalIndent(diskState{Announcements: items, Runs: runs, UpdatedAt: time.Now()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) All() []model.Announcement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]model.Announcement, 0, len(s.byID))
	for _, a := range s.byID {
		items = append(items, a)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].PublishedDate != items[j].PublishedDate {
			return items[i].PublishedDate > items[j].PublishedDate
		}
		return items[i].LastSeenAt.After(items[j].LastSeenAt)
	})
	return items
}

func (s *Store) CountPositions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, a := range s.byID {
		n += len(a.Positions)
	}
	return n
}
