package crawler

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
