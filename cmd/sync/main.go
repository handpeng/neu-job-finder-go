package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
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
	refreshMode := flag.String("refresh-mode", "incremental", "detail refresh mode: incremental or force")
	delay := flag.Duration("delay", 1200*time.Millisecond, "delay between source requests")
	maxPages := flag.Int("max-pages", 100, "maximum list pages")
	detailWorkers := flag.Int("detail-workers", 3, "maximum concurrent detail workers")
	maxConnections := flag.Int("max-connections", 0, "maximum HTTP connections per source host; default detail-workers")
	retryIDs := flag.String("retry-ids", "", "comma- or space-separated announcement IDs to retry")
	flag.Parse()
	mode := strings.ToLower(strings.TrimSpace(*refreshMode))
	if mode != "incremental" && mode != "force" {
		fatal(fmt.Errorf("-refresh-mode must be incremental or force"))
	}

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
	req := crawler.SyncRequest{
		StartDate:           *start,
		EndDate:             *end,
		Keyword:             *keyword,
		CachedAnnouncements: st.All(),
		ForceRefresh:        *forceRefresh || mode == "force",
		RetryIDs:            splitIDs(*retryIDs),
	}
	run := crawler.NewCrawlRun(req)
	req.RunID = run.RunID
	if err := st.BeginRun(run); err != nil {
		fatal(err)
	}
	var summary crawler.ProgressEvent
	items, syncErr := cr.SyncProgress(ctx, req, func(event crawler.ProgressEvent) error {
		summary = event
		return nil
	})
	run = crawler.FinalizeCrawlRun(run, summary, syncErr, ctx.Err())
	ins, upd, saveErr := st.UpsertBatchAndFinishRun(items, run)
	if saveErr != nil {
		fatal(saveErr)
	}
	if syncErr != nil && len(items) == 0 {
		fatal(syncErr)
	}
	if syncErr != nil {
		fmt.Fprintln(os.Stderr, "WARNING:", syncErr)
	}
	fmt.Printf("SYNC_OK run_id=%s published_since=%s published_until=%s fetched=%d inserted=%d updated=%d positions=%d new=%d refreshed=%d skipped_cached=%d details_attempted=%d details_succeeded=%d failed=%d\n", run.RunID, *start, *end, len(items), ins, upd, st.CountPositions(), summary.NewIDs, summary.RefreshedIDs, summary.SkippedCachedIDs, summary.DetailsAttempted, summary.DetailsSucceeded, summary.FailedIDs)
}

func splitIDs(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == ' ' || r == '\t' || r == '\n'
	})
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if _, ok := seen[part]; ok || part == "" {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
