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
	"unicode"

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
	Profile        model.Profile
	Jobs           []model.JobView
	AnnouncementN  int
	PositionN      int
	ScoredN        int
	StartDate      string
	EndDate        string
	Keyword        string
	ForceRefresh   bool
	RefreshMode    string
	MinScore       string
	TopN           string
	ExcludeExpired bool
	Search         string
	ExportQuery    string
	LatestRun      *model.CrawlRun
	Message        string
	Error          string
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
		"join":          strings.Join,
		"groupsText":    formatGroups,
		"runStatusText": runStatusText,
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
	mux.HandleFunc("GET /api/runs", a.handleAPIRuns)
	mux.HandleFunc("GET /api/runs/{runID}", a.handleAPIRun)
	mux.HandleFunc("GET /export.csv", a.handleExportCSV)
	mux.HandleFunc("GET /export.md", a.handleExportMD)
	mux.HandleFunc("GET /export.xlsx", a.handleExportXLSX)
	staticFS, _ := fs.Sub(embedded, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	return securityHeaders(mux)
}

type streamPayload struct {
	RunID            string
	RunStatus        string
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
	if _, _, err := parseControls(r.Form); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	refreshMode, err := refreshModeFromValues(r.Form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	req := crawler.SyncRequest{
		StartDate:           startDateFromValues(r.Form),
		EndDate:             endDateFromValues(r.Form),
		Keyword:             r.Form.Get("keyword"),
		CachedAnnouncements: a.store.All(),
		ForceRefresh:        refreshMode == "force",
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
		_ = send(streamPayload{RunID: run.RunID, RunStatus: run.Status, Type: "error", Message: "保存同步记录失败：" + saveErr.Error(), Inserted: inserted, Updated: updated})
		return
	}
	if syncErr != nil {
		a.logger.Printf("stream sync: %v", syncErr)
		payload := streamPayloadFromEvent(summary)
		payload.Type = "error"
		payload.RunStatus = run.Status
		payload.Message = "同步停止：" + syncErr.Error()
		payload.Inserted = inserted
		payload.Updated = updated
		_ = send(payload)
		return
	}
	payload := streamPayloadFromEvent(summary)
	payload.Type = "complete"
	payload.RunStatus = run.Status
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
	values := r.URL.Query()
	startDate := startDateFromValues(values)
	if startDate == "" {
		now := time.Now()
		startDate = now.AddDate(0, 0, -30).Format("2006-01-02")
	}
	endDate := endDateFromValues(values)
	if endDate == "" {
		endDate = time.Now().Format("2006-01-02")
	}
	values.Set("published_since", startDate)
	values.Set("published_until", endDate)
	p, options, err := parseControls(values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jobs := matcher.FlattenWithOptions(a.store.All(), p, options)
	data := pageData{
		Profile: p, Jobs: jobs, AnnouncementN: len(a.store.All()), PositionN: a.store.CountPositions(),
		StartDate: startDate, EndDate: endDate, Keyword: values.Get("keyword"), ForceRefresh: refreshModeValue(values) == "force",
		RefreshMode: refreshModeValue(values), MinScore: valueOrEmpty(firstNonEmpty(values.Get("min_score"), values.Get("minScore"))),
		TopN: valueOrEmpty(firstNonEmpty(values.Get("top_n"), values.Get("topN"))), ExcludeExpired: options.ExcludeExpired,
		Search: values.Get("q"), ExportQuery: encodeResultQuery(p, options, values.Get("q")),
		Message: values.Get("message"), Error: values.Get("error"),
	}
	runs := a.store.Runs()
	if len(runs) > 0 {
		data.LatestRun = &runs[0]
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
	if _, _, err := parseControls(r.Form); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	refreshMode, err := refreshModeFromValues(r.Form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	endDate := endDateFromValues(r.Form)
	forceRefresh := refreshMode == "force"
	req := crawler.SyncRequest{
		StartDate:           startDateFromValues(r.Form),
		EndDate:             endDate,
		Keyword:             r.Form.Get("keyword"),
		CachedAnnouncements: a.store.All(),
		ForceRefresh:        refreshMode == "force",
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
	copyControlValues(q, r.Form)
	q.Set("published_since", startDateFromValues(r.Form))
	q.Set("published_until", endDate)
	q.Set("keyword", r.Form.Get("keyword"))
	q.Set("refresh_mode", refreshMode)
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
	jobs, err := a.resultSet(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(jobs)
}

func (a *App) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.resultSet(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="neu-jobs.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	if err := exporter.CSV(w, jobs); err != nil {
		a.logger.Printf("csv: %v", err)
	}
}

func (a *App) handleExportMD(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.resultSet(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="neu-jobs.md"`)
	if err := exporter.Markdown(w, jobs); err != nil {
		a.logger.Printf("markdown: %v", err)
	}
}

func (a *App) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.resultSet(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="neu-jobs.xlsx"`)
	if err := exporter.XLSX(w, jobs); err != nil {
		a.logger.Printf("xlsx: %v", err)
	}
}

func profileFromValues(v url.Values) model.Profile {
	p, _ := profileFromValuesE(v)
	return p
}

func (a *App) resultSet(values url.Values) ([]model.JobView, error) {
	p, options, err := parseControls(values)
	if err != nil {
		return nil, err
	}
	return matcher.FlattenWithOptions(a.store.All(), p, options), nil
}

func profileFromValuesE(v url.Values) (model.Profile, error) {
	query, err := booleanQueryFromValues(v)
	if err != nil {
		return model.Profile{}, err
	}
	return model.Profile{
		Research:       v.Get("research"),
		Skills:         v.Get("skills"),
		Degree:         v.Get("degree"),
		GraduationYear: v.Get("graduation_year"),
		Major:          v.Get("major"),
		Cities:         v.Get("cities"),
		Roles:          v.Get("roles"),
		Strict:         formTruthy(v, "strict"),
		Query:          query,
	}, nil
}

func parseControls(v url.Values) (model.Profile, matcher.ResultOptions, error) {
	p, err := profileFromValuesE(v)
	if err != nil {
		return model.Profile{}, matcher.ResultOptions{}, err
	}
	options, err := resultOptionsFromValues(v)
	if err != nil {
		return model.Profile{}, matcher.ResultOptions{}, err
	}
	return p, options, nil
}

func booleanQueryFromValues(v url.Values) (model.BooleanQuery, error) {
	query := model.BooleanQuery{}
	if raw := strings.TrimSpace(v.Get("query")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &query); err != nil {
			return model.BooleanQuery{}, fmt.Errorf("invalid query JSON: %w", err)
		}
	}
	var err error
	if values := clauseValues(v, "must"); len(values) > 0 {
		if query.Must, err = parseTermGroups(values); err != nil {
			return model.BooleanQuery{}, fmt.Errorf("invalid must groups: %w", err)
		}
	}
	if values := clauseValues(v, "should"); len(values) > 0 {
		if query.Should, err = parseTermGroups(values); err != nil {
			return model.BooleanQuery{}, fmt.Errorf("invalid should groups: %w", err)
		}
	}
	if values := clauseValues(v, "must_not"); len(values) > 0 {
		if query.MustNot, err = parseTermGroups(values); err != nil {
			return model.BooleanQuery{}, fmt.Errorf("invalid must_not groups: %w", err)
		}
	}
	if raw := firstNonEmpty(v.Get("minimum_should_match"), v.Get("min_should_match")); raw != "" {
		minimum, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || minimum < 0 {
			return model.BooleanQuery{}, fmt.Errorf("minimum_should_match must be an integer")
		}
		query.MinimumShouldMatch = minimum
	}
	if query.MinimumShouldMatch < 0 {
		return model.BooleanQuery{}, fmt.Errorf("minimum_should_match must be a non-negative integer")
	}
	return query, nil
}

func clauseValues(v url.Values, key string) []string {
	values := append([]string(nil), v[key]...)
	values = append(values, v[key+"[]"]...)
	return values
}

func parseTermGroups(values []string) ([][]string, error) {
	groups := make([][]string, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "[") {
			var encoded [][]string
			if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
				return nil, err
			}
			groups = append(groups, encoded...)
			continue
		}
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			terms := strings.FieldsFunc(line, func(r rune) bool {
				return unicode.IsSpace(r) || strings.ContainsRune(",，;；、|/()", r)
			})
			group := make([]string, 0, len(terms))
			for _, term := range terms {
				if strings.EqualFold(term, "or") || term == "或" {
					continue
				}
				if strings.TrimSpace(term) != "" {
					group = append(group, term)
				}
			}
			if len(group) > 0 {
				groups = append(groups, group)
			}
		}
	}
	return groups, nil
}

func resultOptionsFromValues(v url.Values) (matcher.ResultOptions, error) {
	start := startDateFromValues(v)
	end := endDateFromValues(v)
	if err := validateDateRange(start, end); err != nil {
		return matcher.ResultOptions{}, err
	}
	options := matcher.ResultOptions{StartDate: start, EndDate: end, Search: v.Get("q")}
	if raw := firstNonEmpty(v.Get("min_score"), v.Get("minScore")); raw != "" {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value < 0 || value > 100 {
			return matcher.ResultOptions{}, fmt.Errorf("min_score must be an integer from 0 to 100")
		}
		options.MinScore = &value
	}
	if raw := firstNonEmpty(v.Get("top_n"), v.Get("topN")); raw != "" {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value < 0 {
			return matcher.ResultOptions{}, fmt.Errorf("top_n must be a non-negative integer")
		}
		options.TopN = value
	}
	options.ExcludeExpired = formTruthy(v, "exclude_expired")
	if _, present := v["include_expired"]; present && !formTruthy(v, "include_expired") {
		options.ExcludeExpired = true
	}
	return options, nil
}

func validateDateRange(start, end string) error {
	if start != "" {
		if _, err := time.Parse("2006-01-02", start); err != nil {
			return fmt.Errorf("invalid publication start date %q; expected YYYY-MM-DD", start)
		}
	}
	if end != "" {
		if _, err := time.Parse("2006-01-02", end); err != nil {
			return fmt.Errorf("invalid publication end date %q; expected YYYY-MM-DD", end)
		}
	}
	if start != "" && end != "" && end < start {
		return fmt.Errorf("publication end date %q is before start date %q", end, start)
	}
	return nil
}

func startDateFromValues(v url.Values) string {
	for _, key := range []string{"published_since", "start_date", "starttime"} {
		if value := v.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func endDateFromValues(v url.Values) string {
	for _, key := range []string{"published_until", "end_date", "endtime"} {
		if value := v.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func refreshModeFromValues(v url.Values) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(v.Get("refresh_mode")))
	if mode == "" {
		if formTruthy(v, "force_refresh") {
			return "force", nil
		}
		return "incremental", nil
	}
	switch mode {
	case "incremental", "default":
		return "incremental", nil
	case "force", "full", "force_refresh":
		return "force", nil
	default:
		return "", fmt.Errorf("refresh_mode must be incremental or force")
	}
}

func refreshModeValue(v url.Values) string {
	mode, err := refreshModeFromValues(v)
	if err != nil {
		return "incremental"
	}
	return mode
}

func encodeResultQuery(p model.Profile, options matcher.ResultOptions, search string) string {
	values := url.Values{}
	for _, keyValue := range []struct{ key, value string }{
		{"research", p.Research}, {"skills", p.Skills}, {"degree", p.Degree},
		{"graduation_year", p.GraduationYear}, {"major", p.Major}, {"cities", p.Cities}, {"roles", p.Roles},
	} {
		if keyValue.value != "" {
			values.Set(keyValue.key, keyValue.value)
		}
	}
	if p.Strict {
		values.Set("strict", "1")
	}
	for _, group := range p.Query.Must {
		values.Add("must", strings.Join(group, "|"))
	}
	for _, group := range p.Query.Should {
		values.Add("should", strings.Join(group, "|"))
	}
	for _, group := range p.Query.MustNot {
		values.Add("must_not", strings.Join(group, "|"))
	}
	if p.Query.MinimumShouldMatch != 0 {
		values.Set("minimum_should_match", strconv.Itoa(p.Query.MinimumShouldMatch))
	}
	if options.MinScore != nil {
		values.Set("min_score", strconv.Itoa(*options.MinScore))
	}
	if options.TopN > 0 {
		values.Set("top_n", strconv.Itoa(options.TopN))
	}
	if options.ExcludeExpired {
		values.Set("exclude_expired", "1")
	}
	if options.StartDate != "" {
		values.Set("published_since", options.StartDate)
	}
	if options.EndDate != "" {
		values.Set("published_until", options.EndDate)
	}
	if strings.TrimSpace(search) != "" {
		values.Set("q", search)
	}
	return values.Encode()
}

func copyControlValues(dst, src url.Values) {
	keys := []string{
		"q", "research", "skills", "degree", "graduation_year", "major", "cities", "roles", "strict",
		"must", "should", "must_not", "query", "minimum_should_match", "min_should_match", "min_score", "top_n",
		"exclude_expired", "include_expired", "published_since", "published_until", "start_date", "end_date", "starttime", "endtime",
	}
	for _, key := range keys {
		for _, value := range src[key] {
			dst.Add(key, value)
		}
	}
}

func copyProfile(dst url.Values, src url.Values) {
	for _, key := range []string{"research", "skills", "degree", "graduation_year", "major", "cities", "roles", "strict"} {
		for _, value := range src[key] {
			dst.Add(key, value)
		}
	}
}

func valueOrEmpty(value string) string {
	return strings.TrimSpace(value)
}

func formatGroups(groups [][]string) string {
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group) > 0 {
			lines = append(lines, strings.Join(group, " OR "))
		}
	}
	return strings.Join(lines, "\n")
}

func runStatusText(status string) string {
	switch status {
	case crawler.RunStatusRunning:
		return "进行中"
	case crawler.RunStatusCompleted:
		return "已完成"
	case crawler.RunStatusPartial:
		return "部分完成"
	case crawler.RunStatusCanceled:
		return "已取消"
	case crawler.RunStatusFailed:
		return "失败"
	default:
		return status
	}
}

func (a *App) handleAPIRuns(w http.ResponseWriter, r *http.Request) {
	runs := a.store.Runs()
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		if limit < len(runs) {
			runs = runs[:limit]
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(runs)
}

func (a *App) handleAPIRun(w http.ResponseWriter, r *http.Request) {
	run, ok := a.store.Run(r.PathValue("runID"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(run)
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
