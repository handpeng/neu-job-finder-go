package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"neu-job-finder/internal/crawler"
	exporter "neu-job-finder/internal/export"
	"neu-job-finder/internal/matcher"
	"neu-job-finder/internal/model"
	"neu-job-finder/internal/store"
)

//go:embed web/templates/*.html web/static/*
var embedded embed.FS

type App struct {
	store   *store.Store
	crawler *crawler.Client
	tpl     *template.Template
	logger  *log.Logger
}

type pageData struct {
	Profile       model.Profile
	Jobs          []model.JobView
	AnnouncementN int
	PositionN     int
	ScoredN       int
	StartDate     string
	EndDate       string
	Keyword       string
	ForceRefresh  bool
	Message       string
	Error         string
}

func New(st *store.Store, cr *crawler.Client, logger *log.Logger) (*App, error) {
	funcs := template.FuncMap{
		"scoreText": func(v *int) string {
			if v == nil {
				return "未评分"
			}
			return strconv.Itoa(*v)
		},
		"scoreClass": func(v *int) string {
			if v == nil {
				return "neutral"
			}
			if *v >= 80 {
				return "high"
			}
			if *v >= 60 {
				return "mid"
			}
			return "low"
		},
		"join": strings.Join,
	}
	tpl, err := template.New("index.html").Funcs(funcs).ParseFS(embedded, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	return &App{store: st, crawler: cr, tpl: tpl, logger: logger}, nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.handleIndex)
	mux.HandleFunc("POST /sync", a.handleSync)
	mux.HandleFunc("POST /sync/stream", a.handleSyncStream)
	mux.HandleFunc("GET /api/jobs", a.handleAPIJobs)
	mux.HandleFunc("GET /export.csv", a.handleExportCSV)
	mux.HandleFunc("GET /export.md", a.handleExportMD)
	mux.HandleFunc("GET /export.xlsx", a.handleExportXLSX)
	staticFS, _ := fs.Sub(embedded, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	return securityHeaders(mux)
}

type streamPayload struct {
	RunID            string
	Type             string
	Message          string
	Page             int
	Current          int
	Total            int
	PagesScanned     int
	EntriesSeen      int
	UniqueIDs        int
	InRangeIDs       int
	DuplicateIDs     int
	UndatedIDs       int
	FilteredIDs      int
	FailedIDs        int
	AcceptedIDs      int
	NewIDs           int
	RefreshedIDs     int
	SkippedCachedIDs int
	DetailsAttempted int
	DetailsSucceeded int
	FailedDetails    []model.CrawlFailure
	Company          string
	Positions        []string
	Inserted         int
	Updated          int
}

func (a *App) handleSyncStream(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	req := crawler.SyncRequest{
		StartDate:           r.Form.Get("published_since"),
		EndDate:             endDateFromValues(r.Form),
		Keyword:             r.Form.Get("keyword"),
		CachedAnnouncements: a.store.All(),
		ForceRefresh:        formTruthy(r.Form, "force_refresh"),
		RetryIDs:            splitIDs(firstNonEmpty(r.Form.Get("retry_ids"), r.Form.Get("retry"))),
	}
	run := crawler.NewCrawlRun(req)
	req.RunID = run.RunID
	if err := a.store.BeginRun(run); err != nil {
		http.Error(w, "无法开始同步记录："+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	send := func(payload streamPayload) error {
		if err := encoder.Encode(payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	_ = send(streamPayload{Type: "started", Message: "正在读取招聘公告列表"})

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	inserted := 0
	updated := 0
	collector := newAnnouncementBatchCollector(a.store, 20)
	var summary crawler.ProgressEvent
	_, syncErr := a.crawler.SyncProgress(ctx, req, func(event crawler.ProgressEvent) error {
		summary = event
		payload := streamPayloadFromEvent(event)
		if event.Announcement != nil {
			ins, upd, err := collector.Add(*event.Announcement)
			if err != nil {
				return fmt.Errorf("save %s: %w", event.Announcement.ID, err)
			}
			inserted += ins
			updated += upd
			payload.Company = event.Announcement.Company
			for _, position := range event.Announcement.Positions {
				payload.Positions = append(payload.Positions, position.Name)
			}
			payload.Inserted = ins
			payload.Updated = upd
		}
		return send(payload)
	})
	run = crawler.FinalizeCrawlRun(run, summary, syncErr, ctx.Err())
	pending := collector.Pending()
	finalInserted, finalUpdated, saveErr := a.store.UpsertBatchAndFinishRun(pending, run)
	inserted += finalInserted
	updated += finalUpdated
	if saveErr != nil {
		a.logger.Printf("stream persist: %v", saveErr)
		_ = send(streamPayload{RunID: run.RunID, Type: "error", Message: "保存同步记录失败：" + saveErr.Error(), Inserted: inserted, Updated: updated})
		return
	}
	if syncErr != nil {
		a.logger.Printf("stream sync: %v", syncErr)
		payload := streamPayloadFromEvent(summary)
		payload.Type = "error"
		payload.Message = "同步停止：" + syncErr.Error()
		payload.Inserted = inserted
		payload.Updated = updated
		_ = send(payload)
		return
	}
	payload := streamPayloadFromEvent(summary)
	payload.Type = "complete"
	payload.Message = fmt.Sprintf("同步完成：新增 %d，更新 %d。", inserted, updated)
	payload.Inserted = inserted
	payload.Updated = updated
	_ = send(payload)
}

func streamPayloadFromEvent(event crawler.ProgressEvent) streamPayload {
	return streamPayload{
		RunID:            event.RunID,
		Type:             event.Phase,
		Message:          event.Message,
		Page:             event.Page,
		Current:          event.Current,
		Total:            event.Total,
		PagesScanned:     event.PagesScanned,
		EntriesSeen:      event.EntriesSeen,
		UniqueIDs:        event.UniqueIDs,
		InRangeIDs:       event.InRangeIDs,
		DuplicateIDs:     event.DuplicateIDs,
		UndatedIDs:       event.UndatedIDs,
		FilteredIDs:      event.FilteredIDs,
		FailedIDs:        event.FailedIDs,
		AcceptedIDs:      event.AcceptedIDs,
		NewIDs:           event.NewIDs,
		RefreshedIDs:     event.RefreshedIDs,
		SkippedCachedIDs: event.SkippedCachedIDs,
		DetailsAttempted: event.DetailsAttempted,
		DetailsSucceeded: event.DetailsSucceeded,
		FailedDetails:    append([]model.CrawlFailure(nil), event.FailedDetails...),
	}
}

type announcementBatchCollector struct {
	store *store.Store
	limit int
	items []model.Announcement
}

func newAnnouncementBatchCollector(st *store.Store, limit int) *announcementBatchCollector {
	if limit <= 0 {
		limit = 20
	}
	return &announcementBatchCollector{store: st, limit: limit}
}

func (c *announcementBatchCollector) Add(item model.Announcement) (inserted, updated int, err error) {
	c.items = append(c.items, item)
	if len(c.items) < c.limit {
		return 0, 0, nil
	}
	return c.flush()
}

func (c *announcementBatchCollector) flush() (inserted, updated int, err error) {
	if len(c.items) == 0 {
		return 0, 0, nil
	}
	inserted, updated, err = c.store.UpsertBatch(c.items)
	if err != nil {
		return 0, 0, err
	}
	c.items = nil
	return inserted, updated, nil
}

func (c *announcementBatchCollector) Pending() []model.Announcement {
	items := append([]model.Announcement(nil), c.items...)
	c.items = nil
	return items
}

func splitIDs(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '|' || r == ' ' || r == '\t' || r == '\n'
	})
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	p := profileFromValues(r.URL.Query())
	jobs := matcher.Flatten(a.store.All(), p)
	jobs = filterKeyword(jobs, r.URL.Query().Get("q"))
	startDate := r.URL.Query().Get("published_since")
	if startDate == "" {
		startDate = r.URL.Query().Get("starttime")
	}
	if startDate == "" {
		now := time.Now()
		startDate = now.AddDate(0, 0, -30).Format("2006-01-02")
	}
	endDate := endDateFromValues(r.URL.Query())
	if endDate == "" {
		endDate = time.Now().Format("2006-01-02")
	}
	data := pageData{
		Profile: p, Jobs: jobs, AnnouncementN: len(a.store.All()), PositionN: a.store.CountPositions(),
		StartDate: startDate, EndDate: endDate, Keyword: r.URL.Query().Get("keyword"), ForceRefresh: formTruthy(r.URL.Query(), "force_refresh"),
		Message: r.URL.Query().Get("message"), Error: r.URL.Query().Get("error"),
	}
	for _, j := range jobs {
		if j.Score != nil {
			data.ScoredN++
		}
	}
	if err := a.tpl.ExecuteTemplate(w, "index.html", data); err != nil {
		a.logger.Printf("template: %v", err)
	}
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	endDate := endDateFromValues(r.Form)
	forceRefresh := formTruthy(r.Form, "force_refresh")
	req := crawler.SyncRequest{
		StartDate:           r.Form.Get("published_since"),
		EndDate:             endDate,
		Keyword:             r.Form.Get("keyword"),
		CachedAnnouncements: a.store.All(),
		ForceRefresh:        forceRefresh,
		RetryIDs:            splitIDs(firstNonEmpty(r.Form.Get("retry_ids"), r.Form.Get("retry"))),
	}
	run := crawler.NewCrawlRun(req)
	req.RunID = run.RunID
	if err := a.store.BeginRun(run); err != nil {
		http.Error(w, "无法开始同步记录："+err.Error(), http.StatusInternalServerError)
		return
	}
	var summary crawler.ProgressEvent
	items, syncErr := a.crawler.SyncProgress(ctx, req, func(event crawler.ProgressEvent) error {
		summary = event
		return nil
	})
	run = crawler.FinalizeCrawlRun(run, summary, syncErr, ctx.Err())
	q := url.Values{}
	copyProfile(q, r.Form)
	q.Set("published_since", r.Form.Get("published_since"))
	q.Set("published_until", endDate)
	q.Set("keyword", r.Form.Get("keyword"))
	if forceRefresh {
		q.Set("force_refresh", "1")
	}
	ins, upd, saveErr := a.store.UpsertBatchAndFinishRun(items, run)
	if saveErr != nil {
		a.logger.Printf("sync persist: %v", saveErr)
		q.Set("error", "保存失败："+saveErr.Error())
		http.Redirect(w, r, "/?"+q.Encode(), http.StatusSeeOther)
		return
	}
	if syncErr != nil && len(items) == 0 {
		a.logger.Printf("sync: %v", syncErr)
		q.Set("error", "抓取失败："+syncErr.Error())
		http.Redirect(w, r, "/?"+q.Encode(), http.StatusSeeOther)
		return
	}
	message := fmt.Sprintf("同步完成：抓取 %d 条公告，新增 %d，更新 %d。", len(items), ins, upd)
	if syncErr != nil {
		a.logger.Printf("sync warning: %v", syncErr)
		message += " 部分详情失败，成功结果已保存；请查看服务日志。"
	}
	q.Set("message", message)
	http.Redirect(w, r, "/?"+q.Encode(), http.StatusSeeOther)
}

func (a *App) handleAPIJobs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	p := profileFromValues(r.URL.Query())
	_ = json.NewEncoder(w).Encode(matcher.Flatten(a.store.All(), p))
}

func (a *App) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	jobs := matcher.Flatten(a.store.All(), profileFromValues(r.URL.Query()))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="neu-jobs.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	_ = exporter.CSV(w, jobs)
}

func (a *App) handleExportMD(w http.ResponseWriter, r *http.Request) {
	jobs := matcher.Flatten(a.store.All(), profileFromValues(r.URL.Query()))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="neu-jobs.md"`)
	_ = exporter.Markdown(w, jobs)
}

func (a *App) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	jobs := matcher.Flatten(a.store.All(), profileFromValues(r.URL.Query()))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="neu-jobs.xlsx"`)
	if err := exporter.XLSX(w, jobs); err != nil {
		a.logger.Printf("xlsx: %v", err)
	}
}

func profileFromValues(v url.Values) model.Profile {
	return model.Profile{Research: v.Get("research"), Skills: v.Get("skills"), Degree: v.Get("degree"), GraduationYear: v.Get("graduation_year"), Major: v.Get("major"), Cities: v.Get("cities"), Roles: v.Get("roles"), Strict: v.Get("strict") == "1" || v.Get("strict") == "on"}
}

func copyProfile(dst url.Values, src url.Values) {
	for _, k := range []string{"research", "skills", "degree", "graduation_year", "major", "cities", "roles", "strict"} {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

func endDateFromValues(v url.Values) string {
	for _, key := range []string{"published_until", "end_date", "endtime"} {
		if value := v.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func formTruthy(v url.Values, key string) bool {
	value := v.Get(key)
	return value == "1" || value == "on" || strings.EqualFold(value, "true")
}

func filterKeyword(jobs []model.JobView, q string) []model.JobView {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return jobs
	}
	out := jobs[:0]
	for _, j := range jobs {
		hay := strings.ToLower(strings.Join([]string{j.Announcement.Company, j.Position.Name, j.Position.Location, j.Position.Majors}, " "))
		if strings.Contains(hay, q) {
			out = append(out, j)
		}
	}
	return out
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
