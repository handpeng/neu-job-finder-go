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
	UpdatedAt     time.Time            `json:"updated_at"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	byID map[string]model.Announcement
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, byID: make(map[string]model.Announcement)}
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
	return nil
}

func (s *Store) Upsert(items []model.Announcement) (inserted, updated int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return inserted, updated, s.saveLocked()
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
	b, err := json.MarshalIndent(diskState{Announcements: items, UpdatedAt: time.Now()}, "", "  ")
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
