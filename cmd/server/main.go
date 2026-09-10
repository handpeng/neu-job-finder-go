package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"neu-job-finder/internal/crawler"
	"neu-job-finder/internal/store"
	"neu-job-finder/internal/webapp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	data := flag.String("data", "data/store.json", "data file")
	base := flag.String("base-url", "http://job.neu.edu.cn", "source site base URL")
	delay := flag.Duration("delay", 1200*time.Millisecond, "delay between source requests")
	maxPages := flag.Int("max-pages", 100, "maximum list pages per sync")
	flag.Parse()

	logger := log.New(os.Stdout, "neu-job-finder ", log.LstdFlags|log.Lmicroseconds)
	st, err := store.Open(*data)
	if err != nil {
		logger.Fatal(err)
	}
	cr := crawler.New(crawler.Config{BaseURL: *base, Delay: *delay, MaxPages: *maxPages})
	app, err := webapp.New(st, cr, logger)
	if err != nil {
		logger.Fatal(err)
	}

	srv := &http.Server{Addr: *addr, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	logger.Printf("listening on http://%s", *addr)
	logger.Fatal(srv.ListenAndServe())
}
