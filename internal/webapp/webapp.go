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
	Keyword       string
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
	Type      string
	Message   string
	Page      int
	Current   int
	Total     int
	Company   string
	Positions []string
	Inserted  int
	Updated   int
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
	_, syncErr := a.crawler.SyncProgress(ctx, crawler.SyncRequest{
		StartDate: r.Form.Get("published_since"),
		Keyword:   r.Form.Get("keyword"),
	}, func(event crawler.ProgressEvent) error {
		payload := streamPayload{
			Type:    event.Phase,
			Message: event.Message,
			Page:    event.Page,
			Current: event.Current,
			Total:   event.Total,
		}
		if event.Announcement != nil {
			ins, upd, err := a.store.Upsert([]model.Announcement{*event.Announcement})
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
	if syncErr != nil {
		a.logger.Printf("stream sync: %v", syncErr)
		_ = send(streamPayload{
			Type:     "error",
			Message:  "同步停止：" + syncErr.Error(),
			Inserted: inserted,
			Updated:  updated,
		})
		return
	}
	_ = send(streamPayload{
		Type:     "complete",
		Message:  fmt.Sprintf("同步完成：新增 %d，更新 %d。", inserted, updated),
		Inserted: inserted,
		Updated:  updated,
	})
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
	data := pageData{
		Profile: p, Jobs: jobs, AnnouncementN: len(a.store.All()), PositionN: a.store.CountPositions(),
		StartDate: startDate, Keyword: r.URL.Query().Get("keyword"),
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
	items, err := a.crawler.Sync(ctx, crawler.SyncRequest{StartDate: r.Form.Get("published_since"), Keyword: r.Form.Get("keyword")})
	q := url.Values{}
	copyProfile(q, r.Form)
	q.Set("published_since", r.Form.Get("published_since"))
	q.Set("keyword", r.Form.Get("keyword"))
	if err != nil && len(items) == 0 {
		a.logger.Printf("sync: %v", err)
		q.Set("error", "抓取失败："+err.Error())
		http.Redirect(w, r, "/?"+q.Encode(), http.StatusSeeOther)
		return
	}
	syncErr := err
	ins, upd, saveErr := a.store.Upsert(items)
	if saveErr != nil {
		q.Set("error", "保存失败："+saveErr.Error())
	} else {
		message := fmt.Sprintf("同步完成：抓取 %d 条公告，新增 %d，更新 %d。", len(items), ins, upd)
		if syncErr != nil {
			a.logger.Printf("sync warning: %v", syncErr)
			message += " 部分详情失败，成功结果已保存；请查看服务日志。"
		}
		q.Set("message", message)
	}
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
