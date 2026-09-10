package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"testing"

	"neu-job-finder/internal/model"
)

func TestXLSXCreatesValidPackage(t *testing.T) {
	score := 88
	jobs := []model.JobView{{
		Announcement: model.Announcement{
			Company:                 "测试科技有限公司",
			DetailURL:               "http://job.neu.edu.cn/campus/view/id/123",
			ExpireDate:              "2026-12-31",
			ApplicationURL:          "https://careers.example.com/jobs/123",
			Email:                   "hr@example.com",
			ApplicationRequirements: "邮件标题：姓名+岗位；简历以附件发送",
		},
		Position: model.Position{Name: "算法工程师", Location: "北京", Degree: "硕士", Salary: "20000-30000"},
		Score:    &score,
	}}

	var output bytes.Buffer
	if err := XLSX(&output, jobs); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{
		"[Content_Types].xml":      false,
		"xl/workbook.xml":          false,
		"xl/worksheets/sheet1.xml": false,
	}
	for _, file := range reader.File {
		if _, ok := required[file.Name]; !ok {
			continue
		}
		required[file.Name] = true
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			XMLName xml.Name
		}
		if err := xml.Unmarshal(data, &document); err != nil {
			t.Fatalf("%s is not valid XML: %v", file.Name, err)
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("missing XLSX part %s", name)
		}
	}
}
