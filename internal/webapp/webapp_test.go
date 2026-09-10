package webapp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neu-job-finder/internal/crawler"
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

func TestBooleanControlsAndResultOptionsParseFromWebValues(t *testing.T) {
	values := url.Values{
		"must":                 {"冶金|钢铁\n人工智能 OR 机器学习"},
		"should":               {"Python,Go", "数据分析"},
		"must_not":             {"销售|行政"},
		"minimum_should_match": {"1"},
		"min_score":            {"60"},
		"top_n":                {"5"},
		"exclude_expired":      {"1"},
		"published_since":      {"2026-09-01"},
		"published_until":      {"2026-09-30"},
		"q":                    {"算法"},
	}
	p, options, err := parseControls(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Query.Must) != 2 || len(p.Query.Must[0]) != 2 || len(p.Query.Should) != 2 || len(p.Query.MustNot) != 1 {
		t.Fatalf("query=%#v", p.Query)
	}
	if p.Query.MinimumShouldMatch != 1 || options.MinScore == nil || *options.MinScore != 60 || options.TopN != 5 || !options.ExcludeExpired || options.Search != "算法" {
		t.Fatalf("options=%#v query=%#v", options, p.Query)
	}
	if options.StartDate != "2026-09-01" || options.EndDate != "2026-09-30" {
		t.Fatalf("date range=%#v", options)
	}

	if _, _, err := parseControls(url.Values{"min_score": {"bad"}}); err == nil {
		t.Fatal("invalid min_score was accepted")
	}
	if _, _, err := parseControls(url.Values{"published_since": {"2026-10-01"}, "published_until": {"2026-09-01"}}); err == nil {
		t.Fatal("reversed result date range was accepted")
	}
}

func TestWebAPIAndExportsShareBooleanRankingAndExpiredControls(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	items := []model.Announcement{
		{ID: "old", Company: "过期技术", PublishedDate: "2026-09-01", ExpireDate: "2000-01-01", DetailURL: "https://source.test/old", Positions: []model.Position{{ID: "old:1", AnnouncementID: "old", Name: "算法工程师"}}},
		{ID: "excluded", Company: "销售技术", PublishedDate: "2026-09-02", ExpireDate: "2099-01-01", DetailURL: "https://source.test/excluded", Positions: []model.Position{{ID: "excluded:1", AnnouncementID: "excluded", Name: "销售算法工程师"}}},
		{ID: "first", Company: "第一技术", PublishedDate: "2026-09-03", ExpireDate: "2099-01-01", DetailURL: "https://source.test/first", Positions: []model.Position{{ID: "first:1", AnnouncementID: "first", Name: "算法工程师"}}},
		{ID: "second", Company: "第二技术", PublishedDate: "2026-09-04", ExpireDate: "2099-01-01", DetailURL: "https://source.test/second", Positions: []model.Position{{ID: "second:1", AnnouncementID: "second", Name: "算法工程师"}}},
	}
	if _, _, err := st.Upsert(items); err != nil {
		t.Fatal(err)
	}
	app, err := New(st, crawler.New(crawler.Config{BaseURL: "https://source.test", Delay: time.Nanosecond}), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	query := "must=%E7%AE%97%E6%B3%95%E5%B7%A5%E7%A8%8B%E5%B8%88&must_not=%E9%94%80%E5%94%AE&exclude_expired=1&top_n=2&published_since=2026-09-01&published_until=2026-09-30"

	api := serveRequest(app.Handler(), http.MethodGet, "/api/jobs?"+query, "")
	if api.Code != http.StatusOK {
		t.Fatalf("api status=%d body=%s", api.Code, api.Body.String())
	}
	var jobs []model.JobView
	if err := json.Unmarshal(api.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].Announcement.ID != "second" || jobs[1].Announcement.ID != "first" {
		t.Fatalf("api jobs=%#v", jobs)
	}
	if jobs[0].Status != model.PostingStatusActive || jobs[0].Expired {
		t.Fatalf("api status projection=%#v", jobs[0])
	}
	page := serveRequest(app.Handler(), http.MethodGet, "/?"+query, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="must"`) || !strings.Contains(page.Body.String(), "第二技术") || strings.Contains(page.Body.String(), "过期技术") {
		t.Fatalf("web list does not share API result set: status=%d body=%s", page.Code, page.Body.String())
	}

	csvResponse := serveRequest(app.Handler(), http.MethodGet, "/export.csv?"+query, "")
	mdResponse := serveRequest(app.Handler(), http.MethodGet, "/export.md?"+query, "")
	xlsxResponse := serveRequest(app.Handler(), http.MethodGet, "/export.xlsx?"+query, "")
	for _, response := range []*httptest.ResponseRecorder{csvResponse, mdResponse, xlsxResponse} {
		if response.Code != http.StatusOK {
			t.Fatalf("export status=%d body=%s", response.Code, response.Body.String())
		}
	}
	for name, body := range map[string]string{"csv": csvResponse.Body.String(), "markdown": mdResponse.Body.String()} {
		if !strings.Contains(body, "第二技术") || !strings.Contains(body, "第一技术") || strings.Contains(body, "过期技术") || strings.Contains(body, "销售技术") {
			t.Fatalf("%s does not share API result set: %s", name, body)
		}
		if strings.Index(body, "第二技术") > strings.Index(body, "第一技术") {
			t.Fatalf("%s ordering differs: %s", name, body)
		}
	}
	reader, err := zip.NewReader(bytes.NewReader(xlsxResponse.Body.Bytes()), int64(xlsxResponse.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var sheet []byte
	for _, file := range reader.File {
		if file.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		sheet, err = io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(sheet), "第二技术") || !strings.Contains(string(sheet), "第一技术") || strings.Contains(string(sheet), "过期技术") || strings.Contains(string(sheet), "销售技术") {
		t.Fatalf("xlsx does not share API result set: %s", sheet)
	}
}

func TestRunStatusAPIAndEndToEndCrawlFixture(t *testing.T) {
	var listQuery url.Values
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/campus/index/" {
			listQuery = r.URL.Query()
			_, _ = io.WriteString(w, `<ul class="infoList"><li><a href="/campus/view/id/9101">Fixture Tech</a></li><li>2026-09-10 09:00:00</li></ul>`)
			return
		}
		if r.URL.Path == "/campus/view/id/9101" {
			_, _ = io.WriteString(w, `<html><h5>Fixture Tech</h5><div>发布时间：2026-09-10 09:00</div><div>过期时间：2099-12-31</div><table><tr><td>01</td><td>算法工程师<ul><li>20000-30000</li><li>北京</li><li>全职</li><li>硕士</li></ul></td><td>需求专业：人工智能</td><td>投递简历</td></tr></table></html>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer source.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	cr := crawler.New(crawler.Config{BaseURL: source.URL, Delay: time.Nanosecond, MaxPages: 1})
	req := crawler.SyncRequest{StartDate: "2026-09-10", EndDate: "2026-09-10", RunID: "run-e2e"}
	run := crawler.NewCrawlRun(req)
	req.RunID = run.RunID
	if err := st.BeginRun(run); err != nil {
		t.Fatal(err)
	}
	var summary crawler.ProgressEvent
	items, syncErr := cr.SyncProgress(context.Background(), req, func(event crawler.ProgressEvent) error {
		summary = event
		return nil
	})
	final := crawler.FinalizeCrawlRun(run, summary, syncErr, nil)
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	if len(items) != 1 || items[0].Positions[0].Name != "算法工程师" {
		t.Fatalf("crawl fixture items=%#v", items)
	}
	if listQuery.Get("starttime") != "2026-09-10" || listQuery.Get("endtime") != "2026-09-10" {
		t.Fatalf("source query=%v", listQuery)
	}
	if _, _, err := st.UpsertBatchAndFinishRun(items, final); err != nil {
		t.Fatal(err)
	}
	app, err := New(st, crawler.New(crawler.Config{BaseURL: source.URL, Delay: time.Nanosecond}), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	api := serveRequest(app.Handler(), http.MethodGet, "/api/jobs?must=%E7%AE%97%E6%B3%95%E5%B7%A5%E7%A8%8B%E5%B8%88", "")
	if api.Code != http.StatusOK {
		t.Fatalf("e2e api status=%d body=%s", api.Code, api.Body.String())
	}
	var jobs []model.JobView
	if err := json.Unmarshal(api.Body.Bytes(), &jobs); err != nil || len(jobs) != 1 || jobs[0].Announcement.ID != "9101" {
		t.Fatalf("e2e api jobs=%#v err=%v", jobs, err)
	}
	runs := serveRequest(app.Handler(), http.MethodGet, "/api/runs", "")
	if runs.Code != http.StatusOK || !strings.Contains(runs.Body.String(), "run-e2e") {
		t.Fatalf("run status response=%d body=%s", runs.Code, runs.Body.String())
	}
	runDetail := serveRequest(app.Handler(), http.MethodGet, "/api/runs/run-e2e", "")
	if runDetail.Code != http.StatusOK || !strings.Contains(runDetail.Body.String(), `"status":"completed"`) {
		t.Fatalf("run detail response=%d body=%s", runDetail.Code, runDetail.Body.String())
	}
	for _, path := range []string{"/export.csv?must=%E7%AE%97%E6%B3%95%E5%B7%A5%E7%A8%8B%E5%B8%88", "/export.md?must=%E7%AE%97%E6%B3%95%E5%B7%A5%E7%A8%8B%E5%B8%88", "/export.xlsx?must=%E7%AE%97%E6%B3%95%E5%B7%A5%E7%A8%8B%E5%B8%88"} {
		response := serveRequest(app.Handler(), http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("e2e export %s status=%d", path, response.Code)
		}
	}
}

func serveRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
