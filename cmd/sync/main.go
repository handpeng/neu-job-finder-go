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
	keyword := flag.String("keyword", "", "optional source keyword")
	delay := flag.Duration("delay", 1200*time.Millisecond, "delay between source requests")
	maxPages := flag.Int("max-pages", 100, "maximum list pages")
	flag.Parse()

	now := time.Now()
	if *start == "" {
		*start = now.AddDate(0, 0, -30).Format("2006-01-02")
	}
	st, err := store.Open(*data)
	if err != nil {
		fatal(err)
	}
	cr := crawler.New(crawler.Config{BaseURL: *base, Delay: *delay, MaxPages: *maxPages})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	items, err := cr.Sync(ctx, crawler.SyncRequest{StartDate: *start, Keyword: *keyword})
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
	fmt.Printf("SYNC_OK published_since=%s fetched=%d inserted=%d updated=%d positions=%d\n", *start, len(items), ins, upd, st.CountPositions())
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
