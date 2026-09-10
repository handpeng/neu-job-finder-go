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
	"time"
	"unicode"

	"neu-job-finder/internal/model"
)

const defaultBaseURL = "http://job.neu.edu.cn"

var (
	reDetailID = regexp.MustCompile(`(?i)/campus/view/id/(\d+)`)
	reTag      = regexp.MustCompile(`(?is)<[^>]+>`)
	reScript   = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	reTR       = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	reTD       = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)
	reLI       = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	reH5       = regexp.MustCompile(`(?is)<h5[^>]*>(.*?)</h5>`)
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reExpire   = regexp.MustCompile(`过期时间\s*[：:]\s*(\d{4}-\d{2}-\d{2})`)
	reDate     = regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2})\b`)
	reEmail    = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	reHref     = regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	reInfoList = regexp.MustCompile(`(?is)<ul[^>]*class=["'][^"']*\binfoList\b[^"']*["'][^>]*>(.*?)</ul>`)
	reEmbedded = regexp.MustCompile(`(?is)Base64\.decode\s*\(\s*unzip\s*\(\s*["']([A-Za-z0-9+/=]+)["']\s*\)\s*\.substr\s*\(\s*(\d+)\s*\)\s*\)\s*\.substr\s*\(\s*(\d+)\s*\)`)
	rePlainURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
)

type Config struct {
	BaseURL     string
	UserAgent   string
	Delay       time.Duration
	MaxPages    int
	HTTPTimeout time.Duration
	MaxRetries  int
}

type SyncRequest struct {
	StartDate string
	EndDate   string
	Keyword   string
}

type Client struct {
	cfg    Config
	http   *http.Client
	syncMu sync.Mutex
}

var ErrSyncInProgress = errors.New("a sync is already in progress")

type ProgressEvent struct {
	Phase        string
	Page         int
	Current      int
	Total        int
	PagesScanned int
	EntriesSeen  int
	UniqueIDs    int
	InRangeIDs   int
	DuplicateIDs int
	UndatedIDs   int
	FilteredIDs  int
	FailedIDs    int
	AcceptedIDs  int
	Announcement *model.Announcement
	Message      string
}

type ProgressFunc func(ProgressEvent) error

type listEntry struct {
	ID            string
	Company       string
	PublishedDate string
}

type discoveryStats struct {
	PagesScanned int
	EntriesSeen  int
	UniqueIDs    int
	InRangeIDs   int
	DuplicateIDs int
	UndatedIDs   int
	FilteredIDs  int
	FailedIDs    int
	AcceptedIDs  int
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
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.HTTPTimeout}}
}

func (c *Client) Sync(ctx context.Context, req SyncRequest) ([]model.Announcement, error) {
	return c.SyncProgress(ctx, req, nil)
}

func (c *Client) SyncProgress(ctx context.Context, req SyncRequest, progress ProgressFunc) ([]model.Announcement, error) {
	if !c.syncMu.TryLock() {
		return nil, ErrSyncInProgress
	}
	defer c.syncMu.Unlock()
	if err := validateSyncRequest(req); err != nil {
		return nil, err
	}

	entriesByID := make(map[string]listEntry)
	seenIDs := make(map[string]struct{})
	undatedIDs := make(map[string]struct{})
	keywords := splitKeywords(req.Keyword)
	stats := discoveryStats{}
	for page := 1; page <= c.cfg.MaxPages; page++ {
		body, err := c.fetchList(ctx, page, req)
		if err != nil {
			return nil, fmt.Errorf("fetch list page %d: %w", page, err)
		}
		pageEntries := extractListEntries(body)
		if len(pageEntries) == 0 {
			if page == 1 {
				return nil, fmt.Errorf("list page did not contain decoded announcements; the source layout may have changed")
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
		if err := emitProgress(progress, ProgressEvent{
			Phase:        "discovering",
			Page:         page,
			Total:        len(entriesByID),
			PagesScanned: stats.PagesScanned,
			EntriesSeen:  stats.EntriesSeen,
			UniqueIDs:    stats.UniqueIDs,
			InRangeIDs:   stats.InRangeIDs,
			DuplicateIDs: stats.DuplicateIDs,
			UndatedIDs:   stats.UndatedIDs,
			FilteredIDs:  stats.FilteredIDs,
			FailedIDs:    stats.FailedIDs,
			AcceptedIDs:  stats.AcceptedIDs,
			Message:      fmt.Sprintf("已扫描第 %d 页，发现 %d 条待抓取公告", page, len(entriesByID)),
		}); err != nil {
			return nil, err
		}
		if pageIsOlderThan(pageEntries, req.StartDate) {
			break
		}
		if err := sleepContext(ctx, c.cfg.Delay); err != nil {
			return nil, err
		}
	}

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

	out := make([]model.Announcement, 0, len(ordered))
	failed := make([]string, 0)
	for i, id := range ordered {
		body, detailURL, err := c.fetchDetail(ctx, id)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", id, err))
			stats.FailedIDs++
			if err := emitProgress(progress, ProgressEvent{
				Phase:        "failed",
				Current:      i + 1,
				Total:        len(ordered),
				PagesScanned: stats.PagesScanned,
				EntriesSeen:  stats.EntriesSeen,
				UniqueIDs:    stats.UniqueIDs,
				InRangeIDs:   stats.InRangeIDs,
				DuplicateIDs: stats.DuplicateIDs,
				UndatedIDs:   stats.UndatedIDs,
				FilteredIDs:  stats.FilteredIDs,
				FailedIDs:    stats.FailedIDs,
				AcceptedIDs:  stats.AcceptedIDs,
				Message:      fmt.Sprintf("公告 %s 详情抓取失败", id),
			}); err != nil {
				return out, err
			}
		} else {
			a := parseDetail(id, detailURL, body)
			if a.PublishedDate == "" {
				a.PublishedDate = entriesByID[id].PublishedDate
			}
			if !publishedInRange(a.PublishedDate, req.StartDate, req.EndDate) {
				stats.FilteredIDs++
				if err := emitProgress(progress, ProgressEvent{
					Phase:        "filtered",
					Current:      i + 1,
					Total:        len(ordered),
					PagesScanned: stats.PagesScanned,
					EntriesSeen:  stats.EntriesSeen,
					UniqueIDs:    stats.UniqueIDs,
					InRangeIDs:   stats.InRangeIDs,
					DuplicateIDs: stats.DuplicateIDs,
					UndatedIDs:   stats.UndatedIDs,
					FilteredIDs:  stats.FilteredIDs,
					FailedIDs:    stats.FailedIDs,
					AcceptedIDs:  stats.AcceptedIDs,
					Message:      "详情发布日期不在请求区间，已跳过",
				}); err != nil {
					return out, err
				}
				continue
			}
			if announcementMatchesKeywords(a, keywords) {
				out = append(out, a)
				stats.AcceptedIDs++
				if err := emitProgress(progress, ProgressEvent{
					Phase:        "item",
					Current:      i + 1,
					Total:        len(ordered),
					PagesScanned: stats.PagesScanned,
					EntriesSeen:  stats.EntriesSeen,
					UniqueIDs:    stats.UniqueIDs,
					InRangeIDs:   stats.InRangeIDs,
					DuplicateIDs: stats.DuplicateIDs,
					UndatedIDs:   stats.UndatedIDs,
					FilteredIDs:  stats.FilteredIDs,
					FailedIDs:    stats.FailedIDs,
					AcceptedIDs:  stats.AcceptedIDs,
					Announcement: &a,
					Message:      fmt.Sprintf("已抓取 %s", a.Company),
				}); err != nil {
					return out, err
				}
			} else {
				stats.FilteredIDs++
				if err := emitProgress(progress, ProgressEvent{
					Phase:        "progress",
					Current:      i + 1,
					Total:        len(ordered),
					PagesScanned: stats.PagesScanned,
					EntriesSeen:  stats.EntriesSeen,
					UniqueIDs:    stats.UniqueIDs,
					InRangeIDs:   stats.InRangeIDs,
					DuplicateIDs: stats.DuplicateIDs,
					UndatedIDs:   stats.UndatedIDs,
					FilteredIDs:  stats.FilteredIDs,
					FailedIDs:    stats.FailedIDs,
					AcceptedIDs:  stats.AcceptedIDs,
					Message:      "公告与抓取关键词不匹配，已跳过",
				}); err != nil {
					return out, err
				}
			}
		}
		if i < len(ordered)-1 {
			if err := sleepContext(ctx, c.cfg.Delay); err != nil {
				return out, err
			}
		}
	}
	if len(failed) > 0 {
		if err := emitProgress(progress, ProgressEvent{
			Phase:        "partial",
			Current:      len(ordered),
			Total:        len(ordered),
			PagesScanned: stats.PagesScanned,
			EntriesSeen:  stats.EntriesSeen,
			UniqueIDs:    stats.UniqueIDs,
			InRangeIDs:   stats.InRangeIDs,
			DuplicateIDs: stats.DuplicateIDs,
			UndatedIDs:   stats.UndatedIDs,
			FilteredIDs:  stats.FilteredIDs,
			FailedIDs:    stats.FailedIDs,
			AcceptedIDs:  stats.AcceptedIDs,
			Message:      fmt.Sprintf("同步部分完成，成功 %d 条，失败 %d 条", stats.AcceptedIDs, stats.FailedIDs),
		}); err != nil {
			return out, err
		}
		if len(failed) > 3 {
			failed = append(failed[:3], fmt.Sprintf("and %d more", len(failed)-3))
		}
		return out, fmt.Errorf("partial sync: %s", strings.Join(failed, "; "))
	}
	if err := emitProgress(progress, ProgressEvent{
		Phase:        "done",
		Current:      len(ordered),
		Total:        len(ordered),
		PagesScanned: stats.PagesScanned,
		EntriesSeen:  stats.EntriesSeen,
		UniqueIDs:    stats.UniqueIDs,
		InRangeIDs:   stats.InRangeIDs,
		DuplicateIDs: stats.DuplicateIDs,
		UndatedIDs:   stats.UndatedIDs,
		FilteredIDs:  stats.FilteredIDs,
		FailedIDs:    stats.FailedIDs,
		AcceptedIDs:  stats.AcceptedIDs,
		Message:      fmt.Sprintf("同步完成，共保留 %d 条公告", len(out)),
	}); err != nil {
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
	seen := make(map[string]bool)
	if len(out) > 0 {
		return out
	}
	for _, id := range extractDetailIDs(body) {
		if !seen[id] {
			seen[id] = true
			out = append(out, listEntry{ID: id})
		}
	}
	return out
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
		a.Positions = []model.Position{{ID: id + ":1", AnnouncementID: id, Name: "招聘公告（岗位见正文）"}}
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
		cellsRaw := reTD.FindAllStringSubmatch(row[1], -1)
		if len(cellsRaw) < 2 {
			continue
		}
		cells := make([]string, 0, len(cellsRaw))
		for _, c := range cellsRaw {
			cells = append(cells, strings.TrimSpace(cleanHTML(c[1])))
		}
		joined := strings.Join(cells, " | ")
		if strings.Contains(joined, "职位信息") && strings.Contains(joined, "需求专业") {
			continue
		}

		infoIdx := 0
		if len(cells) >= 3 && isSequence(cells[0]) {
			infoIdx = 1
		}
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
		seq++
		p := model.Position{
			ID:             fmt.Sprintf("%s:%d", announcementID, seq),
			AnnouncementID: announcementID,
			Name:           infoLines[0],
		}
		if len(infoLines) > 1 {
			p.Salary = infoLines[1]
		}
		if len(infoLines) > 2 {
			p.Location = infoLines[2]
		}
		if len(infoLines) > 3 {
			p.EmploymentType = infoLines[3]
		}
		if len(infoLines) > 4 {
			p.Degree = infoLines[4]
		}

		majorIdx := infoIdx + 1
		if majorIdx < len(cells) {
			p.Majors = cells[majorIdx]
		}
		if p.Name != "" && !strings.Contains(p.Name, "操作") {
			out = append(out, p)
		}
	}
	return out
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
