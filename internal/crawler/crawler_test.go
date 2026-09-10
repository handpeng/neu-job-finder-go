package crawler

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neu-job-finder/internal/model"
)

func TestExtractDetailIDs(t *testing.T) {
	h := `<a href="/campus/view/id/561451">a</a><a href='/campus/view/id/561451'>dup</a><a href="/campus/view/id/561249">b</a>`
	ids := extractDetailIDs(h)
	if len(ids) != 2 || ids[0] != "561451" || ids[1] != "561249" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestParseKnownShape(t *testing.T) {
	h := `<html><h5>测试科技有限公司</h5><div>过期时间：2027-06-30</div><table><tr><th>序号</th></tr><tr><td>01</td><td>算法工程师<ul><li>20000-30000</li><li>北京市海淀区</li><li>全职</li><li>硕士</li></ul></td><td>〖硕士〗人工智能、计算机科学与技术</td><td>投递简历</td></tr></table><p>简历投递：hr@example.com</p><p>邮件标题：姓名＋学校＋应聘岗位</p><a href="https://careers.example.com/apply">网申投递</a></html>`
	a := parseDetail("123", "https://job.neu.edu.cn/campus/view/id/123", h)
	if a.Company != "测试科技有限公司" {
		t.Fatalf("company=%q", a.Company)
	}
	if a.Email != "hr@example.com" {
		t.Fatalf("email=%q", a.Email)
	}
	if len(a.Positions) != 1 {
		t.Fatalf("positions=%d %#v", len(a.Positions), a.Positions)
	}
	p := a.Positions[0]
	if p.Name != "算法工程师" || p.Salary != "20000-30000" || p.Degree != "硕士" {
		t.Fatalf("position=%#v", p)
	}
	if p.ExtractionQuality != model.ExtractionConfident || p.SourceText == "" || p.FieldQuality[model.PositionFieldDegree] != model.ExtractionConfident {
		t.Fatalf("known-shape diagnostics=%#v", p)
	}
}

func TestPositionExtractionMissingSalaryDoesNotShiftMetadata(t *testing.T) {
	body := `<table><tr><td>01</td><td>算法工程师<ul><li>北京市海淀区</li><li>全职</li><li>硕士</li></ul></td><td>需求专业：人工智能、计算机科学</td><td>投递简历</td></tr></table>`
	positions := extractPositions("901", body)
	if len(positions) != 1 {
		t.Fatalf("positions=%#v", positions)
	}
	p := positions[0]
	if p.Salary != "" || p.Location != "北京市海淀区" || p.EmploymentType != "全职" || p.Degree != "硕士" {
		t.Fatalf("missing salary shifted fields=%#v", p)
	}
	if p.ExtractionQuality != model.ExtractionFallback || p.FieldQuality[model.PositionFieldSalary] != model.ExtractionAmbiguous {
		t.Fatalf("missing salary diagnostics=%#v", p)
	}
}

func TestPositionExtractionLabelsHandleReorderedMetadata(t *testing.T) {
	body := `<table><tr><td>01</td><td>研发工程师<ul><li>地点：上海</li><li>学历：硕士</li><li>工作性质：全职</li><li>薪资：15K-25K</li></ul></td><td>专业要求：材料科学</td><td>投递简历</td></tr></table>`
	positions := extractPositions("902", body)
	if len(positions) != 1 {
		t.Fatalf("positions=%#v", positions)
	}
	p := positions[0]
	if p.Location != "上海" || p.Degree != "硕士" || p.EmploymentType != "全职" || p.Salary != "15K-25K" || p.Majors != "专业要求：材料科学" {
		t.Fatalf("reordered metadata=%#v", p)
	}
	if p.ExtractionQuality != model.ExtractionConfident || p.ExtractionNote != "" {
		t.Fatalf("reordered diagnostics=%#v", p)
	}
}

func TestPositionExtractionExtraMetadataIsAmbiguousButKeepsRecognizedFields(t *testing.T) {
	body := `<table><tr><td>01</td><td>算法工程师<ul><li>20000-30000</li><li>沈阳</li><li>全职</li><li>硕士</li><li>3年以上工作经验</li></ul></td><td>需求专业：人工智能</td><td>投递简历</td></tr></table>`
	positions := extractPositions("903", body)
	if len(positions) != 1 {
		t.Fatalf("positions=%#v", positions)
	}
	p := positions[0]
	if p.Salary != "20000-30000" || p.Location != "沈阳" || p.EmploymentType != "全职" || p.Degree != "硕士" {
		t.Fatalf("extra metadata shifted fields=%#v", p)
	}
	if p.ExtractionQuality != model.ExtractionAmbiguous || !strings.Contains(p.ExtractionNote, "无法识别元数据") || p.SourceText == "" {
		t.Fatalf("extra metadata diagnostics=%#v", p)
	}
}

func TestPositionExtractionSkipsMalformedRowsAndKeepsValidRows(t *testing.T) {
	body := `<table><tr><td>broken</td></tr><tr><td>01</td><td>数据工程师<ul><li>北京</li><li>全职</li><li>本科</li></ul></td><td>需求专业：计算机科学</td></tr></table>`
	positions := extractPositions("904", body)
	if len(positions) != 1 || positions[0].Name != "数据工程师" {
		t.Fatalf("mixed malformed/valid rows=%#v", positions)
	}
}

func TestGenericAnnouncementPositionPreservesFallbackDiagnostics(t *testing.T) {
	a := parseDetail("905", "https://example.test/campus/view/id/905", `<html><p>招聘公告正文：欢迎应聘人工智能相关岗位。</p></html>`)
	if len(a.Positions) != 1 {
		t.Fatalf("positions=%#v", a.Positions)
	}
	p := a.Positions[0]
	if p.Name != "招聘公告（岗位见正文）" || p.ExtractionQuality != model.ExtractionFallback || p.SourceText == "" || p.FieldQuality[model.PositionFieldDegree] != model.ExtractionAmbiguous {
		t.Fatalf("generic fallback=%#v", p)
	}
}

func TestSchoolEmailFiltered(t *testing.T) {
	text := "Email：neujiuye@163.com 企业投递 hr@corp.example.com"
	if got := extractApplicationEmail(text); got != "hr@corp.example.com" {
		t.Fatalf("got=%q", got)
	}
}

func TestExpandEmbeddedContent(t *testing.T) {
	expression := embeddedExpression(t, "<ul class='infoList'><li><a href='/campus/view/id/123'>测试公司</a></li><li>2026-09-09</li></ul>", 21, 17)
	expanded, err := expandEmbeddedContent("<script>" + expression + "</script>")
	if err != nil {
		t.Fatal(err)
	}
	entries := extractListEntries(expanded)
	if len(entries) != 1 || entries[0].ID != "123" || entries[0].PublishedDate != "2026-09-09" {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestSyncCompressedSiteShape(t *testing.T) {
	listContent := "<ul class='infoList'><li><a href='/campus/view/id/123'>测试科技有限公司</a></li><li>2026-09-09 10:00:00</li></ul>" +
		"<ul class='infoList'><li><a href='/campus/view/id/122'>过期公司</a></li><li>2026-08-01 10:00:00</li></ul>"
	detailContent := "<p>人工智能与机器学习岗位，简历投递邮箱：hr@example.com</p>" +
		"<p>邮件标题：姓名＋学校＋应聘岗位</p><p>网申投递：https://careers.example.com/campus/jobs?id=123</p>"

	listPage := fmt.Sprintf("<html><section id='content1'></section><script>%s</script></html>", embeddedExpression(t, listContent, 21, 21))
	detailPage := fmt.Sprintf("<html><title>测试科技有限公司</title><h5>测试科技有限公司</h5><div>过期时间：2026-12-31</div><div>发布时间：2026-09-09 10:00</div><table><tr><td>01</td><td>算法工程师<ul><li>20000-30000</li><li>北京市海淀区</li><li>全职</li><li>硕士</li></ul></td><td>【硕士】人工智能、计算机科学与技术</td><td>投递简历</td></tr></table><script>%s</script></html>", embeddedExpression(t, detailContent, 102, 77))
	client := New(Config{BaseURL: "http://example.test", Delay: time.Millisecond, MaxPages: 1})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := listPage
		if req.URL.Path == "/campus/view/id/123" {
			body = detailPage
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	progressItems := 0
	items, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate: "2026-09-09",
		Keyword:   "人工智能",
	}, func(event ProgressEvent) error {
		if event.Phase == "item" && event.Announcement != nil {
			progressItems++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d %#v", len(items), items)
	}
	if progressItems != 1 {
		t.Fatalf("streamed items=%d", progressItems)
	}
	item := items[0]
	if item.ID != "123" || item.PublishedDate != "2026-09-09" || item.Email != "hr@example.com" {
		t.Fatalf("item=%#v", item)
	}
	if !strings.Contains(item.ApplicationURL, "careers.example.com/campus/jobs") {
		t.Fatalf("application URL=%q", item.ApplicationURL)
	}
	if !strings.Contains(item.ApplicationRequirements, "邮件标题") {
		t.Fatalf("application requirements=%q", item.ApplicationRequirements)
	}
	if len(item.Positions) != 1 || item.Positions[0].Name != "算法工程师" {
		t.Fatalf("positions=%#v", item.Positions)
	}
}

func TestValidateSyncRequest(t *testing.T) {
	tests := []struct {
		name string
		req  SyncRequest
	}{
		{name: "end before start", req: SyncRequest{StartDate: "2026-09-10", EndDate: "2026-09-09"}},
		{name: "invalid start", req: SyncRequest{StartDate: "2026-02-30", EndDate: "2026-03-01"}},
		{name: "invalid end", req: SyncRequest{StartDate: "2026-03-01", EndDate: "not-a-date"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSyncRequest(tt.req); err == nil {
				t.Fatalf("validateSyncRequest(%#v) returned nil", tt.req)
			}
		})
	}
}

func TestPublishedInRangeIsInclusive(t *testing.T) {
	tests := []struct {
		date string
		want bool
	}{
		{date: "2026-09-01", want: true},
		{date: "2026-09-15", want: true},
		{date: "2026-09-30", want: true},
		{date: "2026-08-31", want: false},
		{date: "2026-10-01", want: false},
		{date: "", want: true},
	}
	for _, tt := range tests {
		if got := publishedInRange(tt.date, "2026-09-01", "2026-09-30"); got != tt.want {
			t.Errorf("publishedInRange(%q)=%v, want %v", tt.date, got, tt.want)
		}
	}
}

func TestPageIsOlderThanRequiresEveryEntryToBeDatedAndOld(t *testing.T) {
	start := "2026-09-01"
	if !pageIsOlderThan([]listEntry{{ID: "old", PublishedDate: "2026-08-31"}}, start) {
		t.Fatal("all dated old page should be safe to stop")
	}
	if pageIsOlderThan([]listEntry{{ID: "old", PublishedDate: "2026-08-31"}, {ID: "undated"}}, start) {
		t.Fatal("undated entry must prevent page stopping")
	}
	if pageIsOlderThan([]listEntry{{ID: "old", PublishedDate: "2026-08-31"}, {ID: "new", PublishedDate: "2026-09-02"}}, start) {
		t.Fatal("mixed old/new page must prevent page stopping")
	}
	if pageIsOlderThan([]listEntry{{ID: "old", PublishedDate: "2026-08-31"}}, "") {
		t.Fatal("without a lower bound the crawler must not stop by date")
	}
}

func TestFetchListQuerySemantics(t *testing.T) {
	var queries []url.Values
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 2})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		queries = append(queries, req.URL.Query())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("<html></html>")),
			Request:    req,
		}, nil
	})
	if _, err := client.fetchList(context.Background(), 2, SyncRequest{
		StartDate: "2026-09-01",
		EndDate:   "2026-09-30",
		Keyword:   "冶金",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.fetchList(context.Background(), 1, SyncRequest{
		StartDate: "2026-09-01",
		EndDate:   "2026-09-30",
		Keyword:   "冶金,人工智能",
	}); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries=%d", len(queries))
	}
	first := queries[0]
	if first.Get("page") != "2" || first.Get("starttime") != "2026-09-01" || first.Get("endtime") != "2026-09-30" || first.Get("keyword") != "冶金" {
		t.Fatalf("single-keyword query=%v", first)
	}
	second := queries[1]
	if second.Get("page") != "" || second.Get("starttime") != "2026-09-01" || second.Get("endtime") != "2026-09-30" || second.Get("keyword") != "" {
		t.Fatalf("multi-keyword query=%v", second)
	}
}

func TestSyncMixedPagesAndDiscoveryCounters(t *testing.T) {
	lists := map[string]string{
		"": listPage(
			listEntryHTML("100", "2026-09-10"),
			listEntryHTML("101", ""),
			listEntryHTML("102", "2026-08-31"),
		),
		"2": listPage(
			listEntryHTML("100", "2026-09-10"),
			listEntryHTML("103", "2026-09-01"),
			listEntryHTML("104", "2026-08-30"),
		),
		"3": listPage(listEntryHTML("105", "2026-08-29")),
	}
	detailDates := map[string]string{
		"100": "2026-09-10",
		"101": "2026-09-05",
		"103": "2026-09-01",
	}
	var pages []string
	var details []string
	var detailsMu sync.Mutex
	var done ProgressEvent
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 5})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			page := req.URL.Query().Get("page")
			pages = append(pages, page)
			body, ok := lists[page]
			if !ok {
				body = "<html></html>"
			}
			return responseFor(req, body), nil
		}
		id := strings.TrimPrefix(req.URL.Path, "/campus/view/id/")
		detailsMu.Lock()
		details = append(details, id)
		detailsMu.Unlock()
		return responseFor(req, detailPage(id, detailDates[id])), nil
	})

	items, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate: "2026-09-01",
		EndDate:   "2026-09-30",
	}, func(event ProgressEvent) error {
		if event.Phase == "done" {
			done = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pages, ",") != ",2,3" {
		t.Fatalf("pages=%v", pages)
	}
	detailsMu.Lock()
	gotDetails := append([]string(nil), details...)
	detailsMu.Unlock()
	sort.Strings(gotDetails)
	if strings.Join(gotDetails, ",") != "100,101,103" {
		t.Fatalf("details=%v", gotDetails)
	}
	if len(items) != 3 {
		t.Fatalf("items=%d %#v", len(items), items)
	}
	if done.EntriesSeen != 7 || done.UniqueIDs != 6 || done.InRangeIDs != 3 || done.DuplicateIDs != 1 || done.UndatedIDs != 1 {
		t.Fatalf("discovery counters=%+v", done)
	}
	if done.AcceptedIDs != 3 || done.FilteredIDs != 0 || done.FailedIDs != 0 {
		t.Fatalf("result counters=%+v", done)
	}
}

func TestSyncDetailDateIsValidatedAgainstRequestedRange(t *testing.T) {
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 1})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			return responseFor(req, listPage(listEntryHTML("200", "2026-09-10"))), nil
		}
		return responseFor(req, detailPage("200", "2026-10-01")), nil
	})
	var done ProgressEvent
	items, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate: "2026-09-01",
		EndDate:   "2026-09-30",
	}, func(event ProgressEvent) error {
		if event.Phase == "done" {
			done = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("out-of-range detail was accepted: %#v", items)
	}
	if done.InRangeIDs != 1 || done.FilteredIDs != 1 || done.AcceptedIDs != 0 || done.FailedIDs != 0 {
		t.Fatalf("counters=%+v", done)
	}
}

func TestSyncReportsFailedDetailsSeparatelyFromAcceptedItems(t *testing.T) {
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 1})
	var partial ProgressEvent
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			return responseFor(req, listPage(
				listEntryHTML("201", "2026-09-10"),
				listEntryHTML("202", "2026-09-09"),
			)), nil
		}
		if strings.HasSuffix(req.URL.Path, "/201") {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("not found")),
				Request:    req,
			}, nil
		}
		return responseFor(req, detailPage("202", "2026-09-09")), nil
	})
	items, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate: "2026-09-01",
		EndDate:   "2026-09-30",
	}, func(event ProgressEvent) error {
		if event.Phase == "partial" {
			partial = event
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "201") {
		t.Fatalf("expected partial detail error containing 201, got %v", err)
	}
	if len(items) != 1 || items[0].ID != "202" {
		t.Fatalf("items=%#v", items)
	}
	if partial.InRangeIDs != 2 || partial.AcceptedIDs != 1 || partial.FailedIDs != 1 || partial.FilteredIDs != 0 {
		t.Fatalf("partial counters=%+v", partial)
	}
	if partial.DetailsAttempted != 2 || partial.DetailsSucceeded != 1 {
		t.Fatalf("detail counters=%+v", partial)
	}
	if len(partial.FailedDetails) != 1 || partial.FailedDetails[0].ID != "201" || !strings.Contains(partial.FailedDetails[0].Reason, "404") {
		t.Fatalf("failed details=%+v", partial.FailedDetails)
	}
}

func TestSyncRetryIDsBypassStableCacheWithoutRefetchingOtherIDs(t *testing.T) {
	now := time.Now()
	date := now.Format("2006-01-02")
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 1})
	var detailRequests []string
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			return responseFor(req, listPage(listEntryHTML("702", date))), nil
		}
		id := strings.TrimPrefix(req.URL.Path, "/campus/view/id/")
		detailRequests = append(detailRequests, id)
		return responseFor(req, detailPage(id, date)), nil
	})
	var summary ProgressEvent
	items, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate: date,
		EndDate:   date,
		RetryIDs:  []string{"701"},
		CachedAnnouncements: []model.Announcement{
			{ID: "701", PublishedDate: date, LastSeenAt: now},
			{ID: "702", PublishedDate: date, LastSeenAt: now},
		},
	}, func(event ProgressEvent) error {
		if event.Phase == "done" {
			summary = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "701" {
		t.Fatalf("retry items=%#v", items)
	}
	if strings.Join(detailRequests, ",") != "701" {
		t.Fatalf("retry detail requests=%v", detailRequests)
	}
	if summary.NewIDs != 0 || summary.RefreshedIDs != 1 || summary.SkippedCachedIDs != 1 {
		t.Fatalf("retry cache counters=%+v", summary)
	}
	if summary.DetailsAttempted != 1 || summary.DetailsSucceeded != 1 || summary.FailedIDs != 0 {
		t.Fatalf("retry detail counters=%+v", summary)
	}
}

func TestFinalizeCrawlRunCopiesTerminalEvidence(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	run := model.CrawlRun{RunID: "run-test", StartedAt: started, Status: RunStatusRunning}
	summary := ProgressEvent{
		RunID:            run.RunID,
		PagesScanned:     2,
		EntriesSeen:      4,
		UniqueIDs:        3,
		InRangeIDs:       2,
		DetailsAttempted: 2,
		DetailsSucceeded: 1,
		FailedIDs:        1,
		FailedDetails:    []model.CrawlFailure{{ID: "702", Reason: "timeout"}},
	}
	final := FinalizeCrawlRun(run, summary, errors.New("partial sync: 702"), nil)
	if final.Status != RunStatusPartial || final.RunID != run.RunID || final.FinishedAt.IsZero() {
		t.Fatalf("final run=%+v", final)
	}
	if final.DetailsAttempted != 2 || final.DetailsSucceeded != 1 || len(final.FailedDetails) != 1 {
		t.Fatalf("terminal evidence=%+v", final)
	}
	if final.StartedAt != started {
		t.Fatalf("started time changed: %s", final.StartedAt)
	}
}

func TestSyncUsesCacheAwareRefreshPolicyAndForceRefresh(t *testing.T) {
	now := time.Now()
	dateToday := now.Format("2006-01-02")
	dateRecent := now.AddDate(0, 0, -1).Format("2006-01-02")
	dateStable := now.AddDate(0, 0, -30).Format("2006-01-02")
	list := listPage(
		listEntryHTML("301", dateToday),
		listEntryHTML("302", dateRecent),
		listEntryHTML("303", dateStable),
	)
	dates := map[string]string{"301": dateToday, "302": dateRecent, "303": dateStable}
	var detailRequests []string
	var detailMu sync.Mutex
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 1, RefreshWindow: 7 * 24 * time.Hour})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			return responseFor(req, list), nil
		}
		id := strings.TrimPrefix(req.URL.Path, "/campus/view/id/")
		detailMu.Lock()
		detailRequests = append(detailRequests, id)
		detailMu.Unlock()
		return responseFor(req, detailPage(id, dates[id])), nil
	})

	stable := model.Announcement{ID: "303", PublishedDate: dateStable, LastSeenAt: now}
	recent := model.Announcement{ID: "302", PublishedDate: dateRecent, LastSeenAt: now.Add(-8 * 24 * time.Hour)}
	var firstSummary ProgressEvent
	first, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate:           dateStable,
		EndDate:             dateToday,
		CachedAnnouncements: []model.Announcement{stable, recent},
	}, func(event ProgressEvent) error {
		if event.Phase == "done" {
			firstSummary = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	detailMu.Lock()
	firstRequests := append([]string(nil), detailRequests...)
	detailMu.Unlock()
	sort.Strings(firstRequests)
	if strings.Join(firstRequests, ",") != "301,302" {
		t.Fatalf("first detail requests=%v", firstRequests)
	}
	if len(first) != 2 || first[0].PublishedDate != dateToday || first[1].PublishedDate != dateRecent {
		t.Fatalf("first items=%#v", first)
	}
	if firstSummary.NewIDs != 1 || firstSummary.RefreshedIDs != 1 || firstSummary.SkippedCachedIDs != 1 {
		t.Fatalf("first refresh counters=%+v", firstSummary)
	}

	detailMu.Lock()
	detailRequests = nil
	detailMu.Unlock()
	allCached := []model.Announcement{
		{ID: "301", PublishedDate: dateToday, LastSeenAt: now},
		{ID: "302", PublishedDate: dateRecent, LastSeenAt: now},
		{ID: "303", PublishedDate: dateStable, LastSeenAt: now},
	}
	var secondSummary ProgressEvent
	second, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate:           dateStable,
		EndDate:             dateToday,
		CachedAnnouncements: allCached,
	}, func(event ProgressEvent) error {
		if event.Phase == "done" {
			secondSummary = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	detailMu.Lock()
	secondRequestCount := len(detailRequests)
	detailMu.Unlock()
	if secondRequestCount != 0 || len(second) != 0 {
		t.Fatalf("unchanged cache was refetched: requests=%d items=%#v", secondRequestCount, second)
	}
	if secondSummary.NewIDs != 0 || secondSummary.RefreshedIDs != 0 || secondSummary.SkippedCachedIDs != 3 {
		t.Fatalf("second refresh counters=%+v", secondSummary)
	}

	detailMu.Lock()
	detailRequests = nil
	detailMu.Unlock()
	var forcedSummary ProgressEvent
	forced, err := client.SyncProgress(context.Background(), SyncRequest{
		StartDate:           dateStable,
		EndDate:             dateToday,
		CachedAnnouncements: allCached,
		ForceRefresh:        true,
	}, func(event ProgressEvent) error {
		if event.Phase == "done" {
			forcedSummary = event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	detailMu.Lock()
	forcedRequests := append([]string(nil), detailRequests...)
	detailMu.Unlock()
	sort.Strings(forcedRequests)
	if strings.Join(forcedRequests, ",") != "301,302,303" || len(forced) != 3 {
		t.Fatalf("force refresh requests=%v items=%d", forcedRequests, len(forced))
	}
	if forcedSummary.NewIDs != 0 || forcedSummary.RefreshedIDs != 3 || forcedSummary.SkippedCachedIDs != 0 {
		t.Fatalf("force refresh counters=%+v", forcedSummary)
	}
	for _, item := range forced {
		if item.PublishedDate != dates[item.ID] {
			t.Fatalf("refresh changed source publication date: %#v", item)
		}
		if !item.FirstSeenAt.IsZero() || !item.LastSeenAt.IsZero() {
			t.Fatalf("crawler fabricated cache timestamps: %#v", item)
		}
	}
}

func TestRefreshPreservesCachedPublicationDateWhenDetailOmitsIt(t *testing.T) {
	now := time.Now()
	date := now.AddDate(0, 0, -1).Format("2006-01-02")
	client := New(Config{BaseURL: "http://example.test", Delay: time.Nanosecond, MaxPages: 1})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			return responseFor(req, listPage(listEntryHTML("401", ""))), nil
		}
		return responseFor(req, detailPage("401", "")), nil
	})
	items, err := client.Sync(context.Background(), SyncRequest{
		StartDate: date,
		EndDate:   date,
		CachedAnnouncements: []model.Announcement{{
			ID:            "401",
			PublishedDate: date,
			LastSeenAt:    now.Add(-8 * 24 * time.Hour),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PublishedDate != date {
		t.Fatalf("cached publication date was lost: %#v", items)
	}
}

func TestNewConfiguresDetailWorkerAndConnectionBounds(t *testing.T) {
	client := New(Config{DetailWorkers: 4, MaxConnections: 2})
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport=%T, want *http.Transport", client.http.Transport)
	}
	if client.cfg.DetailWorkers != 4 || client.cfg.MaxConnections != 2 {
		t.Fatalf("config=%+v", client.cfg)
	}
	if transport.MaxConnsPerHost != 2 || transport.MaxIdleConnsPerHost != 2 {
		t.Fatalf("transport connection bounds: max=%d idle=%d", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
}

func TestSyncDetailsAreBoundedAndAggregatedDeterministically(t *testing.T) {
	ids := []string{"601", "602", "603", "604", "605"}
	dates := map[string]string{
		"601": "2026-09-10",
		"602": "2026-09-09",
		"603": "2026-09-08",
		"604": "2026-09-07",
		"605": "2026-09-06",
	}
	responseDelay := map[string]time.Duration{
		"601": 35 * time.Millisecond,
		"602": 5 * time.Millisecond,
		"603": 25 * time.Millisecond,
		"604": 1 * time.Millisecond,
		"605": 15 * time.Millisecond,
	}
	var active, maxActive int32
	var requestMu sync.Mutex
	var detailRequests []string
	client := New(Config{
		BaseURL:        "http://example.test",
		Delay:          time.Nanosecond,
		MaxPages:       1,
		DetailWorkers:  3,
		MaxConnections: 3,
	})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/campus/index/" {
			entries := make([]string, 0, len(ids))
			for _, id := range ids {
				entries = append(entries, listEntryHTML(id, dates[id]))
			}
			return responseFor(req, listPage(entries...)), nil
		}
		id := strings.TrimPrefix(req.URL.Path, "/campus/view/id/")
		requestMu.Lock()
		detailRequests = append(detailRequests, id)
		requestMu.Unlock()
		current := atomic.AddInt32(&active, 1)
		for {
			previous := atomic.LoadInt32(&maxActive)
			if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
				break
			}
		}
		time.Sleep(responseDelay[id])
		atomic.AddInt32(&active, -1)
		return responseFor(req, detailPage(id, dates[id])), nil
	})
	items, err := client.Sync(context.Background(), SyncRequest{StartDate: "2026-09-01", EndDate: "2026-09-30"})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&maxActive) < 2 || atomic.LoadInt32(&maxActive) > 3 {
		t.Fatalf("max detail concurrency=%d", maxActive)
	}
	requestMu.Lock()
	gotRequests := append([]string(nil), detailRequests...)
	requestMu.Unlock()
	if len(gotRequests) != len(ids) {
		t.Fatalf("detail requests=%v", gotRequests)
	}
	if len(items) != len(ids) {
		t.Fatalf("items=%d", len(items))
	}
	for i, item := range items {
		if item.ID != ids[i] {
			t.Fatalf("items[%d]=%s, want %s; items=%#v", i, item.ID, ids[i], items)
		}
	}
}

func TestSyncPacingIsAggregateAcrossWorkers(t *testing.T) {
	ids := []string{"611", "612", "613"}
	var startsMu sync.Mutex
	var starts []time.Time
	client := New(Config{
		BaseURL:        "http://example.test",
		Delay:          20 * time.Millisecond,
		MaxPages:       1,
		DetailWorkers:  3,
		MaxConnections: 3,
	})
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		startsMu.Lock()
		starts = append(starts, time.Now())
		startsMu.Unlock()
		if req.URL.Path == "/campus/index/" {
			return responseFor(req, listPage(
				listEntryHTML("611", "2026-09-10"),
				listEntryHTML("612", "2026-09-09"),
				listEntryHTML("613", "2026-09-08"),
			)), nil
		}
		id := strings.TrimPrefix(req.URL.Path, "/campus/view/id/")
		return responseFor(req, detailPage(id, map[string]string{"611": "2026-09-10", "612": "2026-09-09", "613": "2026-09-08"}[id])), nil
	})
	if _, err := client.Sync(context.Background(), SyncRequest{StartDate: "2026-09-01", EndDate: "2026-09-30"}); err != nil {
		t.Fatal(err)
	}
	startsMu.Lock()
	gotStarts := append([]time.Time(nil), starts...)
	startsMu.Unlock()
	if len(gotStarts) != 1+len(ids) {
		t.Fatalf("request starts=%d, want %d", len(gotStarts), 1+len(ids))
	}
	for i := 1; i < len(gotStarts); i++ {
		if gap := gotStarts[i].Sub(gotStarts[i-1]); gap < 15*time.Millisecond {
			t.Fatalf("request gap %s at index %d is below aggregate pacing", gap, i)
		}
	}
}

func TestFetchDetailsCancellationConverges(t *testing.T) {
	client := New(Config{DetailWorkers: 3, Delay: time.Nanosecond})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	results := client.fetchDetails(ctx, []string{"621", "622", "623", "624"})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}
	if len(results) != 4 {
		t.Fatalf("results=%d", len(results))
	}
	for _, result := range results {
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("result=%+v, want context cancellation", result)
		}
	}
}

func TestAnnouncementKeywordUsesLocalOR(t *testing.T) {
	a := model.Announcement{Company: "测试公司", RawText: "人工智能岗位", Positions: []model.Position{{Name: "研发工程师"}}}
	if !announcementMatchesKeyword(a, "冶金,人工智能") {
		t.Fatal("one matching crawler keyword should be sufficient")
	}
	if announcementMatchesKeyword(a, "冶金,销售") {
		t.Fatal("non-matching crawler keywords should not match")
	}
	if got := sourceKeyword("冶金"); got != "冶金" {
		t.Fatalf("sourceKeyword(single)=%q", got)
	}
	if got := sourceKeyword("冶金,人工智能"); got != "" {
		t.Fatalf("sourceKeyword(multiple)=%q", got)
	}
}

func listPage(entries ...string) string {
	return "<html>" + strings.Join(entries, "") + "</html>"
}

func listEntryHTML(id, date string) string {
	dateItem := ""
	if date != "" {
		dateItem = "<li>" + date + " 09:00:00</li>"
	}
	return fmt.Sprintf("<ul class='infoList'><li><a href='/campus/view/id/%s'>公司%s</a></li>%s</ul>", id, id, dateItem)
}

func detailPage(id, date string) string {
	return fmt.Sprintf("<html><title>公司%s</title><div>发布时间：%s</div><p>公开招聘公告</p></html>", id, date)
}

func responseFor(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func embeddedExpression(t *testing.T, content string, inflatePrefix, htmlPrefix int) string {
	t.Helper()
	encodedHTML := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("H", htmlPrefix) + content))
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write([]byte(strings.Repeat("Z", inflatePrefix) + encodedHTML)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(compressed.Bytes())
	return fmt.Sprintf("Base64.decode(unzip(%q).substr(%d)).substr(%d)", payload, inflatePrefix, htmlPrefix)
}
