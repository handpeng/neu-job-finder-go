package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"neu-job-finder/internal/crawler"
	"neu-job-finder/internal/store"
)

func main() {
	data := flag.String("data", "data/store.json", "data file")
	base := flag.String("base-url", "http://job.neu.edu.cn", "source site base URL")
	start := flag.String("start", "", "publication start date YYYY-MM-DD; default 30 days ago")
	end := flag.String("end", "", "publication end date YYYY-MM-DD; default today")
	keyword := flag.String("keyword", "", "optional source keyword")
	forceRefresh := flag.Bool("force-refresh", false, "refetch cached announcement details")
	delay := flag.Duration("delay", 1200*time.Millisecond, "delay between source requests")
	maxPages := flag.Int("max-pages", 100, "maximum list pages")
	detailWorkers := flag.Int("detail-workers", 3, "maximum concurrent detail workers")
	maxConnections := flag.Int("max-connections", 0, "maximum HTTP connections per source host; default detail-workers")
	flag.Parse()

	now := time.Now()
	if *start == "" {
		*start = now.AddDate(0, 0, -30).Format("2006-01-02")
	}
	if *end == "" {
		*end = now.Format("2006-01-02")
	}
	st, err := store.Open(*data)
	if err != nil {
		fatal(err)
	}
	cr := crawler.New(crawler.Config{BaseURL: *base, Delay: *delay, MaxPages: *maxPages, DetailWorkers: *detailWorkers, MaxConnections: *maxConnections})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var summary crawler.ProgressEvent
	items, err := cr.SyncProgress(ctx, crawler.SyncRequest{
		StartDate:           *start,
		EndDate:             *end,
		Keyword:             *keyword,
		CachedAnnouncements: st.All(),
		ForceRefresh:        *forceRefresh,
	}, func(event crawler.ProgressEvent) error {
		if event.Phase == "done" || event.Phase == "partial" {
			summary = event
		}
		return nil
	})
	if err != nil && len(items) == 0 {
		fatal(err)
	}
	syncErr := err
	ins, upd, saveErr := st.Upsert(items)
	if saveErr != nil {
		fatal(saveErr)
	}
	if syncErr != nil {
		fmt.Fprintln(os.Stderr, "WARNING:", syncErr)
	}
	fmt.Printf("SYNC_OK published_since=%s published_until=%s fetched=%d inserted=%d updated=%d positions=%d new=%d refreshed=%d skipped_cached=%d\n", *start, *end, len(items), ins, upd, st.CountPositions(), summary.NewIDs, summary.RefreshedIDs, summary.SkippedCachedIDs)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
