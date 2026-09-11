package crawler

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"neu-job-finder/internal/model"
)

const defaultBaseURL = "http://job.neu.edu.cn"

const defaultRefreshWindow = 7 * 24 * time.Hour

const defaultDetailWorkers = 3

var (
	reDetailID          = regexp.MustCompile(`(?i)/campus/view/id/(\d+)`)
	reTag               = regexp.MustCompile(`(?is)<[^>]+>`)
	reScript            = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	reTR                = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	reTD                = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)
	reLI                = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	reH5                = regexp.MustCompile(`(?is)<h5[^>]*>(.*?)</h5>`)
	reTitle             = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reExpire            = regexp.MustCompile(`过期时间\s*[：:]\s*(\d{4}-\d{2}-\d{2})`)
	reDate              = regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2})\b`)
	reEmail             = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	reHref              = regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	reInfoList          = regexp.MustCompile(`(?is)<ul[^>]*class=["'][^"']*\binfoList\b[^"']*["'][^>]*>(.*?)</ul>`)
	reEmptyList         = regexp.MustCompile(`(?is)<div[^>]*class=["'][^"']*\bempty-container\b[^"']*["'][^>]*>.*?<p[^>]*>\s*暂无数据\s*</p>.*?</div>`)
	reEmbedded          = regexp.MustCompile(`(?is)Base64\.decode\s*\(\s*unzip\s*\(\s*["']([A-Za-z0-9+/=]+)["']\s*\)\s*\.substr\s*\(\s*(\d+)\s*\)\s*\)\s*\.substr\s*\(\s*(\d+)\s*\)`)
	rePlainURL          = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	reMetadataLabel     = regexp.MustCompile(`(?i)^\s*(薪资|薪酬|工资|待遇|salary|工作地点|工作地|地点|城市|所在地|location|用工形式|就业形式|职位性质|工作性质|类型|employment|学历|学历要求|最低学历|degree)\s*[：:]\s*(.*?)\s*$`)
	rePositionNameLabel = regexp.MustCompile(`(?i)^\s*(职位名称|岗位名称|职位|岗位|position|role)\s*[：:]\s*(.*?)\s*$`)
	reSalaryValue       = regexp.MustCompile(`(?i)\d+\s*[-~至]\s*\d+`)
)

type Config struct {
	BaseURL        string
	UserAgent      string
	Delay          time.Duration
	MaxPages       int
	HTTPTimeout    time.Duration
	MaxRetries     int
	RefreshWindow  time.Duration
	DetailWorkers  int
	MaxConnections int
}

type SyncRequest struct {
	StartDate           string
	EndDate             string
	Keyword             string
	CachedAnnouncements []model.Announcement
	ForceRefresh        bool
	RunID               string
	RetryIDs            []string
}

type Client struct {
	cfg    Config
	http   *http.Client
	pacer  *requestPacer
	syncMu sync.Mutex
}

type requestPacer struct {
	mu    sync.Mutex
	delay time.Duration
	next  time.Time
}

func (p *requestPacer) wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	start := now
	if p.next.After(start) {
		start = p.next
	}
	p.next = start.Add(p.delay)
	wait := start.Sub(now)
	p.mu.Unlock()
	return sleepContext(ctx, wait)
}

var ErrSyncInProgress = errors.New("a sync is already in progress")

const (
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusPartial   = "partial"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"
)

var runSequence uint64

func NewCrawlRun(req SyncRequest) model.CrawlRun {
	runID := req.RunID
	if runID == "" {
		runID = NewRunID()
	}
	return model.CrawlRun{
		RunID:              runID,
		RequestedStartDate: req.StartDate,
		RequestedEndDate:   req.EndDate,
		Keyword:            req.Keyword,
		RetryIDs:           append([]string(nil), req.RetryIDs...),
		ForceRefresh:       req.ForceRefresh,
		StartedAt:          time.Now().UTC(),
		Status:             RunStatusRunning,
	}
}

func NewRunID() string {
	sequence := atomic.AddUint64(&runSequence, 1)
	return fmt.Sprintf("run-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), sequence)
}

func FinalizeCrawlRun(run model.CrawlRun, summary ProgressEvent, syncErr error, ctxErr error) model.CrawlRun {
	if summary.RunID != "" {
		run.RunID = summary.RunID
	}
	run.PagesScanned = summary.PagesScanned
	run.EntriesSeen = summary.EntriesSeen
	run.UniqueIDs = summary.UniqueIDs
	run.InRangeIDs = summary.InRangeIDs
	run.DuplicateIDs = summary.DuplicateIDs
	run.UndatedIDs = summary.UndatedIDs
	run.NewIDs = summary.NewIDs
	run.RefreshedIDs = summary.RefreshedIDs
	run.SkippedCachedIDs = summary.SkippedCachedIDs
	run.DetailsAttempted = summary.DetailsAttempted
	run.DetailsSucceeded = summary.DetailsSucceeded
	run.FilteredIDs = summary.FilteredIDs
	run.AcceptedIDs = summary.AcceptedIDs
	run.FailedIDs = summary.FailedIDs
	run.FailedDetails = append([]model.CrawlFailure(nil), summary.FailedDetails...)
	run.CancellationObserved = ctxErr != nil
	run.FinishedAt = time.Now().UTC()
	switch {
	case ctxErr != nil:
		run.Status = RunStatusCanceled
	case syncErr == nil:
		run.Status = RunStatusCompleted
	case summary.FailedIDs > 0:
		run.Status = RunStatusPartial
	default:
		run.Status = RunStatusFailed
	}
	if syncErr != nil {
		run.Error = syncErr.Error()
	} else {
		run.Error = ""
	}
	return run
}

type ProgressEvent struct {
	RunID            string
	Phase            string
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
	Announcement     *model.Announcement
	Message          string
}

type ProgressFunc func(ProgressEvent) error

type listEntry struct {
	ID            string
	Company       string
	PublishedDate string
}

type discoveryStats struct {
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
}

func (s discoveryStats) progress(runID, phase string, page, current, total int, announcement *model.Announcement, message string) ProgressEvent {
	return ProgressEvent{
		RunID:            runID,
		Phase:            phase,
		Page:             page,
		Current:          current,
		Total:            total,
		PagesScanned:     s.PagesScanned,
		EntriesSeen:      s.EntriesSeen,
		UniqueIDs:        s.UniqueIDs,
		InRangeIDs:       s.InRangeIDs,
		DuplicateIDs:     s.DuplicateIDs,
		UndatedIDs:       s.UndatedIDs,
		FilteredIDs:      s.FilteredIDs,
		FailedIDs:        s.FailedIDs,
		AcceptedIDs:      s.AcceptedIDs,
		NewIDs:           s.NewIDs,
		RefreshedIDs:     s.RefreshedIDs,
		SkippedCachedIDs: s.SkippedCachedIDs,
		DetailsAttempted: s.DetailsAttempted,
		DetailsSucceeded: s.DetailsSucceeded,
		FailedDetails:    append([]model.CrawlFailure(nil), s.FailedDetails...),
		Announcement:     announcement,
		Message:          message,
	}
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "NEUJobFinder/0.1 (+personal research; low-frequency crawler)"
	}
	if cfg.Delay <= 0 {
		cfg.Delay = 1200 * time.Millisecond
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 100
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 20 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 2
	}
	if cfg.RefreshWindow <= 0 {
		cfg.RefreshWindow = defaultRefreshWindow
	}
	if cfg.DetailWorkers <= 0 {
		cfg.DetailWorkers = defaultDetailWorkers
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = cfg.DetailWorkers
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = cfg.MaxConnections
	transport.MaxIdleConnsPerHost = cfg.MaxConnections
	transport.MaxConnsPerHost = cfg.MaxConnections
	transport.IdleConnTimeout = 90 * time.Second
	return &Client{
		cfg:   cfg,
		http:  &http.Client{Timeout: cfg.HTTPTimeout, Transport: transport},
		pacer: &requestPacer{delay: cfg.Delay},
	}
}

func (c *Client) Sync(ctx context.Context, req SyncRequest) ([]model.Announcement, error) {
	return c.SyncProgress(ctx, req, nil)
}

func (c *Client) SyncProgress(ctx context.Context, req SyncRequest, progress ProgressFunc) ([]model.Announcement, error) {
	if !c.syncMu.TryLock() {
		return nil, ErrSyncInProgress
	}
	defer c.syncMu.Unlock()
	if req.RunID == "" {
		req.RunID = NewRunID()
	}
	if err := validateSyncRequest(req); err != nil {
		return nil, err
	}

	entriesByID := make(map[string]listEntry)
	seenIDs := make(map[string]struct{})
	undatedIDs := make(map[string]struct{})
	keywords := splitKeywords(req.Keyword)
	stats := discoveryStats{}
	retryIDs := make(map[string]struct{}, len(req.RetryIDs))
	for _, id := range req.RetryIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			retryIDs[id] = struct{}{}
		}
	}
	cachedByID := make(map[string]model.Announcement, len(req.CachedAnnouncements))
	for _, cached := range req.CachedAnnouncements {
		if cached.ID != "" {
			cachedByID[cached.ID] = cached
		}
	}
	for page := 1; page <= c.cfg.MaxPages; page++ {
		body, err := c.fetchList(ctx, page, req)
		if err != nil {
			return nil, fmt.Errorf("fetch list page %d: %w", page, err)
		}
		pageEntries := extractListEntries(body)
		if len(extractInfoListEntries(body)) == 0 && len(extractDetailIDs(body)) > 0 {
			return nil, fmt.Errorf("list page contained stable announcement IDs but no decoded list entries")
		}
		if len(pageEntries) == 0 {
			if page == 1 {
				if !isValidEmptyList(body) {
					return nil, fmt.Errorf("list page did not contain decoded announcements; the source layout may have changed")
				}
				stats.PagesScanned = page
				if err := emitProgress(progress, stats.progress(req.RunID, "discovering", page, 0, 0, nil, "列表页确认为空，没有可抓取公告")); err != nil {
					return nil, err
				}
			}
			break
		}
		for _, entry := range pageEntries {
			stats.EntriesSeen++
			if entry.PublishedDate == "" {
				undatedIDs[entry.ID] = struct{}{}
				stats.UndatedIDs = len(undatedIDs)
			}
			if _, seen := seenIDs[entry.ID]; seen {
				stats.DuplicateIDs++
			} else {
				seenIDs[entry.ID] = struct{}{}
			}
			if !publishedInRange(entry.PublishedDate, req.StartDate, req.EndDate) {
				continue
			}
			if _, ok := entriesByID[entry.ID]; !ok {
				entriesByID[entry.ID] = entry
			}
		}
		stats.PagesScanned = page
		stats.UniqueIDs = len(seenIDs)
		stats.InRangeIDs = len(entriesByID)
		if err := emitProgress(progress, stats.progress(req.RunID, "discovering", page, 0, len(entriesByID), nil, fmt.Sprintf("已扫描第 %d 页，发现 %d 条待抓取公告", page, len(entriesByID)))); err != nil {
			return nil, err
		}
		if pageIsOlderThan(pageEntries, req.StartDate) {
			break
		}
	}
	for id := range retryIDs {
		if _, ok := entriesByID[id]; !ok {
			entriesByID[id] = listEntry{ID: id}
		}
	}
	stats.InRangeIDs = len(entriesByID)

	ordered := make([]string, 0, len(entriesByID))
	for id := range entriesByID {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := entriesByID[ordered[i]], entriesByID[ordered[j]]
		if left.PublishedDate != right.PublishedDate {
			return left.PublishedDate > right.PublishedDate
		}
		return left.ID < right.ID
	})

	detailIDs := make([]string, 0, len(ordered))
	policyNow := time.Now()
	for i, id := range ordered {
		_, retry := retryIDs[id]
		cached, ok := cachedByID[id]
		if !ok {
			stats.NewIDs++
			detailIDs = append(detailIDs, id)
			continue
		}
		if retry || req.ForceRefresh || cachedDetailNeedsRefresh(cached, policyNow, c.cfg.RefreshWindow) {
			stats.RefreshedIDs++
			detailIDs = append(detailIDs, id)
			continue
		}
		stats.SkippedCachedIDs++
		if err := emitProgress(progress, stats.progress(req.RunID, "cached", 0, i+1, len(ordered), nil, fmt.Sprintf("公告 %s 已有稳定缓存，跳过详情抓取", id))); err != nil {
			return nil, err
		}
	}

	out := make([]model.Announcement, 0, len(detailIDs))
	stats.DetailsAttempted = len(detailIDs)
	detailResults := c.fetchDetails(ctx, detailIDs)
	for i, result := range detailResults {
		id := result.id
		body, detailURL, err := result.body, result.detailURL, result.err
		if err != nil {
			stats.FailedDetails = append(stats.FailedDetails, model.CrawlFailure{ID: id, Reason: err.Error()})
			stats.FailedIDs = len(stats.FailedDetails)
			if err := emitProgress(progress, stats.progress(req.RunID, "failed", 0, i+1, len(detailIDs), nil, fmt.Sprintf("公告 %s 详情抓取失败", id))); err != nil {
				return out, err
			}
		} else {
			stats.DetailsSucceeded++
			a := parseDetail(id, detailURL, body)
			if a.PublishedDate == "" {
				a.PublishedDate = entriesByID[id].PublishedDate
			}
			if a.PublishedDate == "" {
				a.PublishedDate = cachedByID[id].PublishedDate
			}
			if !publishedInRange(a.PublishedDate, req.StartDate, req.EndDate) {
				stats.FilteredIDs++
				if err := emitProgress(progress, stats.progress(req.RunID, "filtered", 0, i+1, len(detailIDs), nil, "详情发布日期不在请求区间，已跳过")); err != nil {
					return out, err
				}
				continue
			}
			if announcementMatchesKeywords(a, keywords) {
				out = append(out, a)
				stats.AcceptedIDs++
				if err := emitProgress(progress, stats.progress(req.RunID, "item", 0, i+1, len(detailIDs), &a, fmt.Sprintf("已抓取 %s", a.Company))); err != nil {
					return out, err
				}
			} else {
				stats.FilteredIDs++
				if err := emitProgress(progress, stats.progress(req.RunID, "progress", 0, i+1, len(detailIDs), nil, "公告与抓取关键词不匹配，已跳过")); err != nil {
					return out, err
				}
			}
		}
	}
	if len(stats.FailedDetails) > 0 {
		if err := emitProgress(progress, stats.progress(req.RunID, "partial", 0, len(detailIDs), len(detailIDs), nil, fmt.Sprintf("同步部分完成，成功 %d 条，失败 %d 条", stats.AcceptedIDs, stats.FailedIDs))); err != nil {
			return out, err
		}
		failureMessages := make([]string, 0, len(stats.FailedDetails))
		for _, failure := range stats.FailedDetails {
			failureMessages = append(failureMessages, fmt.Sprintf("%s: %s", failure.ID, failure.Reason))
		}
		if len(failureMessages) > 3 {
			failureMessages = append(failureMessages[:3], fmt.Sprintf("and %d more", len(failureMessages)-3))
		}
		return out, fmt.Errorf("partial sync: %s", strings.Join(failureMessages, "; "))
	}
	if err := emitProgress(progress, stats.progress(req.RunID, "done", 0, len(detailIDs), len(detailIDs), nil, fmt.Sprintf("同步完成，共保留 %d 条公告", len(out)))); err != nil {
		return out, err
	}
	return out, nil
}

func emitProgress(progress ProgressFunc, event ProgressEvent) error {
	if progress == nil {
		return nil
	}
	return progress(event)
}

func (c *Client) fetchList(ctx context.Context, page int, req SyncRequest) (string, error) {
	u, _ := url.Parse(c.cfg.BaseURL + "/campus/index/")
	q := u.Query()
	if page > 1 {
		q.Set("page", fmt.Sprintf("%d", page))
	}
	if req.StartDate != "" {
		q.Set("starttime", req.StartDate)
	}
	if req.EndDate != "" {
		q.Set("endtime", req.EndDate)
	}
	if keyword := sourceKeyword(req.Keyword); keyword != "" {
		q.Set("keyword", keyword)
	}
	u.RawQuery = q.Encode()
	body, err := c.fetch(ctx, u.String())
	if err != nil {
		return "", err
	}
	return expandEmbeddedContent(body)
}

func (c *Client) fetchDetail(ctx context.Context, id string) (body, detailURL string, err error) {
	detailURL = fmt.Sprintf("%s/campus/view/id/%s", c.cfg.BaseURL, id)
	body, err = c.fetch(ctx, detailURL)
	if err == nil {
		body, err = expandEmbeddedContent(body)
	}
	return
}

type detailJob struct {
	index int
	id    string
}

type detailResult struct {
	index     int
	id        string
	body      string
	detailURL string
	err       error
}

func (c *Client) fetchDetails(ctx context.Context, ids []string) []detailResult {
	results := make([]detailResult, len(ids))
	if len(ids) == 0 {
		return results
	}
	workerCount := c.cfg.DetailWorkers
	if workerCount <= 0 {
		workerCount = defaultDetailWorkers
	}
	if workerCount > len(ids) {
		workerCount = len(ids)
	}

	jobs := make(chan detailJob)
	resultCh := make(chan detailResult, len(ids))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					body, detailURL, err := c.fetchDetail(ctx, job.id)
					resultCh <- detailResult{index: job.index, id: job.id, body: body, detailURL: detailURL, err: err}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i, id := range ids {
			select {
			case <-ctx.Done():
				return
			case jobs <- detailJob{index: i, id: id}:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(resultCh)
	}()

	completed := make([]bool, len(ids))
	for result := range resultCh {
		results[result.index] = result
		completed[result.index] = true
	}
	for i, id := range ids {
		if completed[i] {
			continue
		}
		err := ctx.Err()
		if err == nil {
			err = errors.New("detail worker did not return a result")
		}
		results[i] = detailResult{index: i, id: id, err: err}
	}
	return results
}

func (c *Client) fetch(ctx context.Context, u string) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		body, retry, err := c.fetchOnce(ctx, u)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry || attempt == c.cfg.MaxRetries {
			break
		}
		if err := sleepContext(ctx, time.Duration(attempt+1)*500*time.Millisecond); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

func (c *Client) fetchOnce(ctx context.Context, u string) (body string, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if c.pacer != nil {
		if err := c.pacer.wait(ctx); err != nil {
			return "", false, err
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if err != nil {
		return "", true, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return "", true, fmt.Errorf("empty HTTP response")
	}
	return string(b), false, nil
}

func expandEmbeddedContent(body string) (string, error) {
	matches := reEmbedded.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return body, nil
	}
	fragments := make([]string, 0, len(matches))
	for _, match := range matches {
		compressed, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			return "", fmt.Errorf("decode embedded compressed data: %w", err)
		}
		zr, err := zlib.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return "", fmt.Errorf("open embedded compressed data: %w", err)
		}
		inflated, readErr := io.ReadAll(io.LimitReader(zr, 8<<20))
		closeErr := zr.Close()
		if readErr != nil {
			return "", fmt.Errorf("inflate embedded data: %w", readErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close embedded data: %w", closeErr)
		}
		first, _ := strconv.Atoi(match[2])
		second, _ := strconv.Atoi(match[3])
		if first > len(inflated) {
			return "", fmt.Errorf("embedded prefix %d exceeds inflated payload", first)
		}
		encoded := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
				return -1
			}
			return r
		}, string(inflated[first:]))
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("decode embedded HTML: %w", err)
		}
		if second > len(decoded) {
			return "", fmt.Errorf("embedded HTML prefix %d exceeds payload", second)
		}
		fragments = append(fragments, string(decoded[second:]))
	}
	return body + "\n" + strings.Join(fragments, "\n"), nil
}

func extractListEntries(body string) []listEntry {
	out := extractInfoListEntries(body)
	if len(out) > 0 {
		return out
	}
	seen := make(map[string]bool)
	for _, id := range extractDetailIDs(body) {
		if !seen[id] {
			seen[id] = true
			out = append(out, listEntry{ID: id})
		}
	}
	return out
}

func extractInfoListEntries(body string) []listEntry {
	blocks := reInfoList.FindAllStringSubmatch(body, -1)
	out := make([]listEntry, 0, len(blocks))
	for _, block := range blocks {
		idMatch := reDetailID.FindStringSubmatch(block[1])
		linkMatch := reHref.FindStringSubmatch(block[1])
		if len(idMatch) < 2 || len(linkMatch) < 3 {
			continue
		}
		entry := listEntry{ID: idMatch[1], Company: strings.TrimSpace(cleanHTML(linkMatch[2]))}
		if dateMatch := reDate.FindStringSubmatch(cleanHTML(block[1])); len(dateMatch) > 1 {
			entry.PublishedDate = dateMatch[1]
		}
		out = append(out, entry)
	}
	return out
}

func isValidEmptyList(body string) bool {
	return len(extractDetailIDs(body)) == 0 &&
		len(extractInfoListEntries(body)) == 0 &&
		reEmptyList.MatchString(body)
}

func validateSyncRequest(req SyncRequest) error {
	if req.StartDate != "" {
		if _, err := time.Parse("2006-01-02", req.StartDate); err != nil {
			return fmt.Errorf("invalid publication start date %q; expected YYYY-MM-DD", req.StartDate)
		}
	}
	if req.EndDate != "" {
		if _, err := time.Parse("2006-01-02", req.EndDate); err != nil {
			return fmt.Errorf("invalid publication end date %q; expected YYYY-MM-DD", req.EndDate)
		}
	}
	if req.StartDate != "" && req.EndDate != "" && req.EndDate < req.StartDate {
		return fmt.Errorf("publication end date %q is before start date %q", req.EndDate, req.StartDate)
	}
	return nil
}

func publishedInRange(date, start, end string) bool {
	if date == "" {
		return true
	}
	if start != "" && date < start {
		return false
	}
	if end != "" && date > end {
		return false
	}
	return true
}

func publishedOnOrAfter(date, start string) bool {
	return publishedInRange(date, start, "")
}

// cachedDetailNeedsRefresh keeps recent source postings observable without
// refetching stable older postings on every range sync. LastSeenAt controls
// the refresh interval; PublishedDate decides whether an item is considered
// stable enough to skip indefinitely.
func cachedDetailNeedsRefresh(cached model.Announcement, now time.Time, window time.Duration) bool {
	if window <= 0 {
		window = defaultRefreshWindow
	}
	if cached.PublishedDate != "" {
		published, err := time.Parse("2006-01-02", cached.PublishedDate)
		if err == nil && published.Before(now.Add(-window)) {
			return false
		}
	}
	if cached.LastSeenAt.IsZero() {
		return true
	}
	return now.Sub(cached.LastSeenAt) >= window
}

func pageIsOlderThan(entries []listEntry, start string) bool {
	if start == "" {
		return false
	}
	hasDate := false
	for _, entry := range entries {
		if entry.PublishedDate == "" {
			return false
		}
		hasDate = true
		if entry.PublishedDate >= start {
			return false
		}
	}
	return hasDate
}

func announcementMatchesKeyword(a model.Announcement, keyword string) bool {
	return announcementMatchesKeywords(a, splitKeywords(keyword))
}

func announcementMatchesKeywords(a model.Announcement, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	parts := []string{a.Company, a.RawText}
	for _, p := range a.Positions {
		parts = append(parts, p.Name, p.Location, p.Majors)
	}
	haystack := strings.ToLower(strings.Join(parts, " "))
	for _, keyword := range keywords {
		if strings.Contains(haystack, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func splitKeywords(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",，;；、|", r)
	})
	seen := map[string]bool{}
	keywords := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := strings.ToLower(part)
		if part == "" || seen[key] {
			continue
		}
		seen[key] = true
		keywords = append(keywords, part)
	}
	return keywords
}

func sourceKeyword(value string) string {
	keywords := splitKeywords(value)
	if len(keywords) == 1 {
		return keywords[0]
	}
	return ""
}

func extractDetailIDs(body string) []string {
	m := reDetailID.FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(m))
	for _, x := range m {
		if !seen[x[1]] {
			seen[x[1]] = true
			out = append(out, x[1])
		}
	}
	return out
}

func parseDetail(id, detailURL, body string) model.Announcement {
	text := cleanHTML(body)
	a := model.Announcement{
		ID:        id,
		DetailURL: detailURL,
		Company:   extractCompany(body),
		RawText:   truncate(text, 24000),
	}
	if m := reExpire.FindStringSubmatch(text); len(m) > 1 {
		a.ExpireDate = m[1]
	}
	a.PublishedDate = inferPublishedDate(text, a.ExpireDate)
	a.Email = extractApplicationEmail(text)
	a.ApplicationURL = extractApplicationURL(body)
	a.EmailSubject = extractEmailSubject(text)
	a.ApplicationRequirements = extractApplicationRequirements(text)
	a.Positions = extractPositions(id, body)
	if len(a.Positions) == 0 {
		a.Positions = []model.Position{fallbackPosition(id, body)}
	}
	return a
}

func extractCompany(body string) string {
	for _, re := range []*regexp.Regexp{reH5, reTitle} {
		if m := re.FindStringSubmatch(body); len(m) > 1 {
			s := strings.TrimSpace(cleanHTML(m[1]))
			s = strings.TrimSuffix(s, "-东北大学毕业生就业信息网")
			if s != "" && !strings.Contains(s, "招聘公告详情") {
				return s
			}
		}
	}
	return "未知单位"
}

func extractPositions(announcementID, body string) []model.Position {
	rows := reTR.FindAllStringSubmatch(body, -1)
	out := []model.Position{}
	seq := 0
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		cellsRaw := reTD.FindAllStringSubmatch(row[1], -1)
		if len(cellsRaw) < 2 {
			continue
		}
		cells := make([]string, 0, len(cellsRaw))
		for _, c := range cellsRaw {
			cells = append(cells, strings.TrimSpace(cleanHTML(c[1])))
		}
		if isPositionHeader(cells) {
			continue
		}

		infoIdx := positionInfoIndex(cellsRaw, cells)
		if infoIdx >= len(cells) {
			continue
		}
		infoRaw := cellsRaw[infoIdx][1]
		infoLines := extractListItems(infoRaw)
		if len(infoLines) == 0 {
			infoLines = splitUsefulLines(cells[infoIdx])
		}
		if len(infoLines) == 0 || strings.Contains(infoLines[0], "招聘公告详情") {
			continue
		}
		name := normalizePositionName(infoLines[0])
		if name == "" || strings.Contains(name, "操作") {
			continue
		}
		seq++
		p := model.Position{
			ID:                fmt.Sprintf("%s:%d", announcementID, seq),
			AnnouncementID:    announcementID,
			Name:              name,
			SourceText:        truncate(strings.TrimSpace(cleanHTML(row[1])), 4000),
			ExtractionQuality: model.ExtractionConfident,
			FieldQuality: map[string]model.ExtractionQuality{
				model.PositionFieldName:           model.ExtractionConfident,
				model.PositionFieldSalary:         model.ExtractionAmbiguous,
				model.PositionFieldLocation:       model.ExtractionAmbiguous,
				model.PositionFieldEmploymentType: model.ExtractionAmbiguous,
				model.PositionFieldDegree:         model.ExtractionAmbiguous,
				model.PositionFieldMajors:         model.ExtractionAmbiguous,
			},
		}

		assignments, qualities, extractionQuality, notes := parsePositionMetadata(infoLines[1:])
		p.ExtractionQuality = mergeExtractionQuality(p.ExtractionQuality, extractionQuality)
		for field, quality := range qualities {
			p.FieldQuality[field] = quality
		}
		p.Salary = assignments[model.PositionFieldSalary]
		p.Location = assignments[model.PositionFieldLocation]
		p.EmploymentType = assignments[model.PositionFieldEmploymentType]
		p.Degree = assignments[model.PositionFieldDegree]

		major, majorQuality, majorAmbiguous, majorNote := extractMajorCell(cells[infoIdx+1:])
		if major != "" {
			p.Majors = major
			p.FieldQuality[model.PositionFieldMajors] = majorQuality
		}
		if majorAmbiguous {
			p.FieldQuality[model.PositionFieldMajors] = model.ExtractionAmbiguous
			p.ExtractionQuality = mergeExtractionQuality(p.ExtractionQuality, model.ExtractionAmbiguous)
		}
		if majorNote != "" {
			notes = append(notes, majorNote)
		}
		p.ExtractionNote = strings.Join(uniqueNotes(notes), "; ")
		if p.ExtractionNote != "" && p.ExtractionQuality == model.ExtractionConfident {
			p.ExtractionQuality = model.ExtractionFallback
		}
		out = append(out, p)
	}
	return out
}

func fallbackPosition(announcementID, body string) model.Position {
	return model.Position{
		ID:                announcementID + ":1",
		AnnouncementID:    announcementID,
		Name:              "招聘公告（岗位见正文）",
		SourceText:        truncate(strings.TrimSpace(cleanHTML(body)), 4000),
		ExtractionQuality: model.ExtractionFallback,
		ExtractionNote:    "未发现可安全解析的岗位表格，保留公告级岗位占位",
		FieldQuality: map[string]model.ExtractionQuality{
			model.PositionFieldName:           model.ExtractionFallback,
			model.PositionFieldSalary:         model.ExtractionAmbiguous,
			model.PositionFieldLocation:       model.ExtractionAmbiguous,
			model.PositionFieldEmploymentType: model.ExtractionAmbiguous,
			model.PositionFieldDegree:         model.ExtractionAmbiguous,
			model.PositionFieldMajors:         model.ExtractionAmbiguous,
		},
	}
}

func positionInfoIndex(cellsRaw [][]string, cells []string) int {
	infoIdx := 0
	if len(cells) >= 3 && isSequence(cells[0]) {
		infoIdx = 1
	}
	for i := infoIdx; i < len(cellsRaw); i++ {
		fragment := strings.ToLower(cellsRaw[i][1])
		if strings.Contains(fragment, "<li") || strings.Contains(fragment, "岗位名称") || strings.Contains(fragment, "职位名称") {
			return i
		}
	}
	return infoIdx
}

func isPositionHeader(cells []string) bool {
	joined := strings.Join(cells, " | ")
	return (strings.Contains(joined, "职位信息") && strings.Contains(joined, "需求专业")) ||
		(strings.Contains(joined, "序号") && strings.Contains(joined, "职位"))
}

func normalizePositionName(value string) string {
	value = strings.TrimSpace(value)
	if match := rePositionNameLabel.FindStringSubmatch(value); len(match) > 2 {
		value = strings.TrimSpace(match[2])
	}
	return value
}

func parsePositionMetadata(values []string) (map[string]string, map[string]model.ExtractionQuality, model.ExtractionQuality, []string) {
	assignments := make(map[string]string)
	qualities := make(map[string]model.ExtractionQuality)
	fields := []string{model.PositionFieldSalary, model.PositionFieldLocation, model.PositionFieldEmploymentType, model.PositionFieldDegree}
	for _, field := range fields {
		qualities[field] = model.ExtractionAmbiguous
	}
	notes := []string{}
	if len(values) == 0 {
		return assignments, qualities, model.ExtractionFallback, []string{"岗位元数据缺失"}
	}

	unknown := false
	duplicate := false
	unlabeledKinds := make([]string, 0, len(values))
	allUnlabeled := true
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			unknown = true
			notes = append(notes, "存在空白元数据项")
			continue
		}
		field, extracted, labeled := metadataField(value)
		if labeled {
			allUnlabeled = false
		} else {
			unlabeledKinds = append(unlabeledKinds, field)
		}
		if field == "" || extracted == "" {
			unknown = true
			notes = append(notes, "无法识别元数据："+value)
			continue
		}
		if _, exists := assignments[field]; exists {
			duplicate = true
			qualities[field] = model.ExtractionAmbiguous
			notes = append(notes, "元数据字段重复："+field)
			continue
		}
		assignments[field] = extracted
		if labeled {
			qualities[field] = model.ExtractionConfident
		} else {
			qualities[field] = model.ExtractionFallback
		}
	}
	for _, field := range fields {
		if _, ok := assignments[field]; !ok {
			notes = append(notes, "元数据字段缺失："+field)
		}
	}
	if unknown || duplicate {
		return assignments, qualities, model.ExtractionAmbiguous, notes
	}
	allPresent := len(assignments) == len(fields)
	if allPresent && allUnlabeled && sameStrings(unlabeledKinds, fields) {
		for _, field := range fields {
			qualities[field] = model.ExtractionConfident
		}
		return assignments, qualities, model.ExtractionConfident, notes
	}
	if allPresent {
		for _, field := range fields {
			if qualities[field] != model.ExtractionConfident {
				return assignments, qualities, model.ExtractionFallback, notes
			}
		}
		return assignments, qualities, model.ExtractionConfident, notes
	}
	return assignments, qualities, model.ExtractionFallback, notes
}

func metadataField(value string) (field, extracted string, labeled bool) {
	if match := reMetadataLabel.FindStringSubmatch(value); len(match) > 2 {
		field = fieldForMetadataLabel(match[1])
		return field, strings.TrimSpace(match[2]), true
	}
	field = classifyMetadataValue(value)
	return field, strings.TrimSpace(value), false
}

func fieldForMetadataLabel(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.Contains(label, "薪"), strings.Contains(label, "工资"), strings.Contains(label, "待遇"), label == "salary":
		return model.PositionFieldSalary
	case strings.Contains(label, "地点"), strings.Contains(label, "城市"), strings.Contains(label, "所在地"), label == "location":
		return model.PositionFieldLocation
	case strings.Contains(label, "用工"), strings.Contains(label, "就业"), strings.Contains(label, "职位性质"), strings.Contains(label, "工作性质"), label == "类型", label == "employment":
		return model.PositionFieldEmploymentType
	case strings.Contains(label, "学历"), label == "degree":
		return model.PositionFieldDegree
	default:
		return ""
	}
}

func classifyMetadataValue(value string) string {
	low := strings.ToLower(strings.TrimSpace(value))
	switch {
	case containsAnyText(low, "本科", "硕士", "博士", "研究生", "大专", "专科", "中专", "学历不限", "不限学历"):
		return model.PositionFieldDegree
	case containsAnyText(low, "全职", "兼职", "实习", "劳务", "校招", "社招", "合同制", "正式员工"):
		return model.PositionFieldEmploymentType
	case containsAnyText(low, "面议", "面谈", "年薪", "月薪", "薪资", "待遇") || (strings.ContainsAny(low, "元万k") && containsDigit(low)) || reSalaryValue.MatchString(low):
		return model.PositionFieldSalary
	case looksLikeLocation(low):
		return model.PositionFieldLocation
	default:
		return ""
	}
}

func looksLikeLocation(value string) bool {
	if containsAnyText(value, "北京", "上海", "天津", "重庆", "沈阳", "大连", "长春", "哈尔滨", "南京", "杭州", "武汉", "广州", "深圳", "成都", "西安", "济南", "青岛") {
		return true
	}
	return strings.HasSuffix(value, "省") || strings.HasSuffix(value, "市") || strings.HasSuffix(value, "自治区") || strings.HasSuffix(value, "州") || strings.HasSuffix(value, "区") || strings.HasSuffix(value, "县") || strings.HasSuffix(value, "路") || strings.HasSuffix(value, "园")
}

func containsAnyText(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func containsDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func extractMajorCell(cells []string) (value string, quality model.ExtractionQuality, ambiguous bool, note string) {
	candidates := make([]string, 0, len(cells))
	marked := make([]string, 0, len(cells))
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" || isActionCell(cell) {
			continue
		}
		candidates = append(candidates, cell)
		if looksLikeMajorCell(cell) {
			marked = append(marked, cell)
		}
	}
	if len(marked) == 1 {
		if len(candidates) > 1 {
			return marked[0], model.ExtractionConfident, true, "专业列之外存在未识别单元格"
		}
		return marked[0], model.ExtractionConfident, false, ""
	}
	if len(marked) > 1 {
		return "", model.ExtractionAmbiguous, true, "存在多个候选专业单元格"
	}
	if len(candidates) == 1 {
		return candidates[0], model.ExtractionFallback, false, "专业列未带明确标签"
	}
	if len(candidates) > 1 {
		return "", model.ExtractionAmbiguous, true, "无法在多个单元格中确认专业列"
	}
	return "", model.ExtractionAmbiguous, false, ""
}

func looksLikeMajorCell(value string) bool {
	return containsAnyText(value, "需求专业", "专业要求", "专业：", "专业:", "〖", "【")
}

func isActionCell(value string) bool {
	return containsAnyText(value, "投递", "申请", "操作")
}

func mergeExtractionQuality(left, right model.ExtractionQuality) model.ExtractionQuality {
	if left == model.ExtractionAmbiguous || right == model.ExtractionAmbiguous {
		return model.ExtractionAmbiguous
	}
	if left == model.ExtractionFallback || right == model.ExtractionFallback {
		return model.ExtractionFallback
	}
	return model.ExtractionConfident
}

func uniqueNotes(notes []string) []string {
	seen := make(map[string]struct{}, len(notes))
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		if note == "" {
			continue
		}
		if _, ok := seen[note]; ok {
			continue
		}
		seen[note] = struct{}{}
		out = append(out, note)
	}
	return out
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func extractListItems(fragment string) []string {
	ms := reLI.FindAllStringSubmatch(fragment, -1)
	out := make([]string, 0, len(ms)+1)
	prefix := fragment
	if loc := reLI.FindStringIndex(fragment); loc != nil {
		prefix = fragment[:loc[0]]
	}
	if s := strings.TrimSpace(cleanHTML(prefix)); s != "" {
		out = append(out, s)
	}
	for _, m := range ms {
		if s := strings.TrimSpace(cleanHTML(m[1])); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitUsefulLines(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' })
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func extractApplicationEmail(text string) string {
	bad := []string{"neujiuye@163.com", "neu83681260@163.com", "neucareer@163.com"}
	seen := map[string]bool{}
	best := ""
	bestScore := -1
	for _, loc := range reEmail.FindAllStringIndex(text, -1) {
		e := text[loc[0]:loc[1]]
		low := strings.ToLower(e)
		if seen[low] {
			continue
		}
		seen[low] = true
		reject := false
		for _, b := range bad {
			if low == b {
				reject = true
				break
			}
		}
		if reject {
			continue
		}
		context := strings.ToLower(surroundingText(text, loc[0], loc[1], 180))
		score := 0
		if containsAny(context, []string{"投递", "简历", "应聘", "申请"}) {
			score += 5
		}
		if containsAny(context, []string{"邮箱", "邮件", "email", "e-mail"}) {
			score += 2
		}
		if strings.Contains(context, "咨询") && !strings.Contains(context, "投递") {
			score--
		}
		if score > bestScore {
			best = e
			bestScore = score
		}
	}
	return best
}

func extractApplicationURL(body string) string {
	for _, m := range reHref.FindAllStringSubmatch(body, -1) {
		href := html.UnescapeString(strings.TrimSpace(m[1]))
		label := cleanHTML(m[2])
		if isApplicationURL(href, label) {
			return href
		}
	}
	for _, loc := range rePlainURL.FindAllStringIndex(body, -1) {
		href := html.UnescapeString(strings.TrimRight(body[loc[0]:loc[1]], ".,;:，。；：）)]}"))
		context := cleanHTML(surroundingText(body, loc[0], loc[1], 220))
		if isApplicationURL(href, context) {
			return href
		}
	}
	return ""
}

func isApplicationURL(href, context string) bool {
	low := strings.ToLower(strings.TrimSpace(href))
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return false
	}
	if strings.Contains(low, "job.neu.edu.cn") || strings.Contains(low, "jysd.com") {
		return false
	}
	return containsAny(low+" "+strings.ToLower(context), []string{
		"apply", "career", "campus", "jobs", "job", "recruit", "zhaopin", "xiaozhao",
		"招聘", "网申", "投递", "应聘", "简历",
	})
}

func surroundingText(s string, start, end, radius int) string {
	if start > radius {
		start -= radius
	} else {
		start = 0
	}
	if end+radius < len(s) {
		end += radius
	} else {
		end = len(s)
	}
	return s[start:end]
}

func extractEmailSubject(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if containsAny(line, []string{"邮件标题", "邮件主题", "邮件命名", "主题格式", "命名格式"}) {
			return truncate(line, 220)
		}
		if strings.Contains(line, "姓名") && strings.Contains(line, "学校") && containsAny(line, []string{"岗位", "职位"}) {
			return truncate(line, 220)
		}
		if i+1 < len(lines) && containsAny(line, []string{"邮件", "主题", "命名"}) {
			next := strings.TrimSpace(lines[i+1])
			if strings.Contains(next, "姓名") && containsAny(next, []string{"岗位", "职位"}) {
				return truncate(next, 220)
			}
		}
	}
	return ""
}

func extractApplicationRequirements(text string) string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if containsAny(line, []string{"投递方式", "应聘方式", "申请方式", "应聘流程", "简历投递"}) {
			start = i
			break
		}
	}
	if start >= 0 {
		selected := make([]string, 0, 10)
		for _, line := range lines[start:] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(selected) > 0 && containsAny(line, []string{"审核：", "职位列表", "用人单位招聘服务", "更多咨询"}) {
				break
			}
			selected = append(selected, line)
			if len(selected) == 10 {
				break
			}
		}
		return truncate(strings.Join(selected, "；"), 800)
	}
	selected := make([]string, 0, 4)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if containsAny(line, []string{"邮件标题", "邮件主题", "邮件命名", "命名格式", "简历以附件", "投递邮箱"}) {
			selected = append(selected, line)
		}
		if len(selected) == 4 {
			break
		}
	}
	return truncate(strings.Join(selected, "；"), 800)
}

func inferPublishedDate(text, expire string) string {
	all := reDate.FindAllString(text, -1)
	for _, d := range all {
		if d != expire {
			return d
		}
	}
	return ""
}

func cleanHTML(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	replacements := []struct{ old, new string }{
		{"<br>", "\n"}, {"<br/>", "\n"}, {"<br />", "\n"},
		{"</p>", "\n"}, {"</div>", "\n"}, {"</li>", "\n"}, {"</tr>", "\n"}, {"</h5>", "\n"},
	}
	low := s
	for _, r := range replacements {
		low = strings.ReplaceAll(low, r.old, r.new)
		low = strings.ReplaceAll(low, strings.ToUpper(r.old), r.new)
	}
	low = reTag.ReplaceAllString(low, " ")
	low = html.UnescapeString(low)
	lines := strings.Split(low, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func isSequence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > 4 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
