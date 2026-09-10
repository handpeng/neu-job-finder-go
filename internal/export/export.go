package export

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"neu-job-finder/internal/model"
)

func CSV(w io.Writer, jobs []model.JobView) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(headers()); err != nil {
		return err
	}
	for _, j := range jobs {
		if err := cw.Write(row(j)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func Markdown(w io.Writer, jobs []model.JobView) error {
	fmt.Fprintln(w, "# 东北大学就业网岗位匹配报告")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "|匹配分|状态|单位|岗位|地点|学历|薪资|专业|截止时间|投递邮箱|网申链接|投递要求|详情|")
	fmt.Fprintln(w, "|---:|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, j := range jobs {
		score := "未评分"
		if j.Score != nil {
			score = fmt.Sprintf("%d", *j.Score)
		}
		applicationURL := ""
		if j.Announcement.ApplicationURL != "" {
			applicationURL = "[网申](" + j.Announcement.ApplicationURL + ")"
		}
		fmt.Fprintf(w, "|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|[原文](%s)|\n",
			escapeMD(score), escapeMD(j.Status), escapeMD(j.Announcement.Company), escapeMD(j.Position.Name),
			escapeMD(j.Position.Location), escapeMD(j.Position.Degree), escapeMD(j.Position.Salary),
			escapeMD(j.Position.Majors), escapeMD(j.Announcement.ExpireDate), escapeMD(j.Announcement.Email),
			applicationURL, escapeMD(j.Announcement.ApplicationRequirements), j.Announcement.DetailURL)
	}
	return nil
}

func XLSX(w io.Writer, jobs []model.JobView) error {
	zw := zip.NewWriter(w)
	files := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="岗位匹配" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`,
		"xl/styles.xml":              `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Arial"/></font><font><b/><sz val="11"/><name val="Arial"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs></styleSheet>`,
	}
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, content); err != nil {
			return err
		}
	}
	f, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return err
	}
	if err := writeSheet(f, jobs); err != nil {
		return err
	}
	return zw.Close()
}

func writeSheet(w io.Writer, jobs []model.JobView) error {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><cols><col min="1" max="1" width="10" customWidth="1"/><col min="2" max="3" width="28" customWidth="1"/><col min="4" max="6" width="18" customWidth="1"/><col min="7" max="7" width="48" customWidth="1"/><col min="8" max="12" width="24" customWidth="1"/></cols><sheetData>`)
	writeXMLRow(&b, 1, headers(), 1)
	for i, j := range jobs {
		writeXMLRow(&b, i+2, row(j), 0)
	}
	b.WriteString(`</sheetData><autoFilter ref="A1:N1"/></worksheet>`)
	_, err := w.Write(b.Bytes())
	return err
}

func writeXMLRow(b *bytes.Buffer, rowNum int, values []string, style int) {
	fmt.Fprintf(b, `<row r="%d">`, rowNum)
	for i, v := range values {
		cell := cellRef(i+1, rowNum)
		fmt.Fprintf(b, `<c r="%s" t="inlineStr" s="%d"><is><t xml:space="preserve">`, cell, style)
		_ = xml.EscapeText(b, []byte(v))
		b.WriteString(`</t></is></c>`)
	}
	b.WriteString(`</row>`)
}

func cellRef(col, row int) string {
	name := ""
	for col > 0 {
		col--
		name = string(rune('A'+col%26)) + name
		col /= 26
	}
	return fmt.Sprintf("%s%d", name, row)
}

func headers() []string {
	return []string{"匹配分", "状态", "单位", "岗位", "地点", "学历", "薪资", "需求专业", "截止时间", "投递邮箱", "网申链接", "邮件标题要求", "投递要求", "详情页"}
}

func row(j model.JobView) []string {
	score := "未评分"
	if j.Score != nil {
		score = fmt.Sprintf("%d", *j.Score)
	}
	return []string{score, j.Status, j.Announcement.Company, j.Position.Name, j.Position.Location, j.Position.Degree, j.Position.Salary, j.Position.Majors, j.Announcement.ExpireDate, j.Announcement.Email, j.Announcement.ApplicationURL, j.Announcement.EmailSubject, j.Announcement.ApplicationRequirements, j.Announcement.DetailURL}
}

func escapeMD(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
