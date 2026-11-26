package server

// ملف Excel الشامل: تصدير كل التقارير والبيانات بأوراق، وقراءة كل أوراقه حتى نرجّعه (نسخة احتياطية قابلة للتعديل).
// الكتابة بسيطة: كل الخلايا نصوص (حتى يبقى الصفر بأول الهاتف وما يتغير شكل التاريخ)، والورقة من اليمين لليسار،
// وأول سطر عريض ومثبّت.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

type bookSheet struct {
	Name string     `json:"name"`
	Rows [][]string `json:"rows"`
}

const (
	bookMaxSheets = 40
	bookMaxCells  = 3_000_000
	bookMaxBody   = 60 << 20
)

func xmlEsc(s string) string {
	var b bytes.Buffer
	// نشيل محارف التحكم اللي XML ما يقبلها
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 {
			return r
		}
		return -1
	}, s)
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// اسم الورقة: 31 حرف بالكثير، وبدون : \ / ? * [ ]
func sheetName(n string, used map[string]bool) string {
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(n))
	if n == "" {
		n = "ورقة"
	}
	if utf8.RuneCountInString(n) > 31 {
		n = string([]rune(n)[:31])
	}
	base, k := n, 2
	for used[strings.ToLower(n)] {
		suf := fmt.Sprintf(" %d", k)
		r := []rune(base)
		if len(r)+len(suf) > 31 {
			r = r[:31-len(suf)]
		}
		n, k = string(r)+suf, k+1
	}
	used[strings.ToLower(n)] = true
	return n
}

func writeBook(sheets []bookSheet) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, body string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, body)
		return err
	}
	used := map[string]bool{}
	var wbSheets, rels, ctypes strings.Builder
	for i, sh := range sheets {
		n := sheetName(sh.Name, used)
		fmt.Fprintf(&wbSheets, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEsc(n), i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		fmt.Fprintf(&ctypes, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
		// عرض الأعمدة حسب أطول نص (بحد)
		width := map[int]int{}
		for _, row := range sh.Rows {
			for c, v := range row {
				l := 0
				for _, line := range strings.Split(v, "\n") {
					l = max(l, utf8.RuneCountInString(line))
				}
				width[c] = max(width[c], l)
			}
		}
		var x strings.Builder
		x.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
		x.WriteString(`<sheetViews><sheetView rightToLeft="1" workbookViewId="0">`)
		if len(sh.Rows) > 1 {
			x.WriteString(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
		}
		x.WriteString(`</sheetView></sheetViews>`)
		if len(width) > 0 {
			x.WriteString(`<cols>`)
			for c := 0; c < len(width)+1; c++ {
				if w, ok := width[c]; ok {
					fmt.Fprintf(&x, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, c+1, c+1, min(max(w+3, 8), 60))
				}
			}
			x.WriteString(`</cols>`)
		}
		x.WriteString(`<sheetData>`)
		for r, row := range sh.Rows {
			fmt.Fprintf(&x, `<row r="%d">`, r+1)
			for c, v := range row {
				if v == "" {
					continue
				}
				style := ""
				if r == 0 {
					style = ` s="1"`
				} else if strings.Contains(v, "\n") {
					style = ` s="2"`
				}
				fmt.Fprintf(&x, `<c r="%s%d" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, colName(c), r+1, style, xmlEsc(v))
			}
			x.WriteString(`</row>`)
		}
		x.WriteString(`</sheetData></worksheet>`)
		if err := put(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), x.String()); err != nil {
			return nil, err
		}
	}
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` + ctypes.String() + `</Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":     `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView/></bookViews><sheets>` + wbSheets.String() + `</sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() +
			fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, len(sheets)+1) + `</Relationships>`,
		"xl/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<fonts count="2"><font><sz val="11"/><name val="Arial"/></font><font><b/><sz val="11"/><name val="Arial"/></font></fonts>` +
			`<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFE8EEF4"/></patternFill></fill></fills>` +
			`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
			`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
			`<cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
			`<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>` +
			`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment wrapText="1" vertical="top"/></xf></cellXfs>` +
			`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`,
	}
	for _, n := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml"} {
		if err := put(n, files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readBook يقرا كل أوراق ملف xlsx بأسمائها وترتيبها
func readBook(buf []byte) ([]bookSheet, error) {
	z, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, badReq("ملف Excel تالف أو مو بصيغة xlsx")
	}
	wb, err := zipFile(z, "xl/workbook.xml")
	if err != nil {
		return nil, badReq("الملف مو ملف Excel (xlsx)")
	}
	var w struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wb, &w); err != nil {
		return nil, badReq("ما گدرت أقرا ملف Excel")
	}
	targets := map[string]string{}
	if rb, err := zipFile(z, "xl/_rels/workbook.xml.rels"); err == nil {
		var rels struct {
			R []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		if xml.Unmarshal(rb, &rels) == nil {
			for _, r := range rels.R {
				t := r.Target
				if strings.HasPrefix(t, "/") {
					t = strings.TrimPrefix(t, "/")
				} else {
					t = path.Join("xl", t)
				}
				targets[r.ID] = t
			}
		}
	}
	var sst []string
	if sb, err := zipFile(z, "xl/sharedStrings.xml"); err == nil {
		var s struct {
			SI []xSI `xml:"si"`
		}
		if err := xml.Unmarshal(sb, &s); err != nil {
			return nil, badReq("ما گدرت أقرا نصوص ملف Excel")
		}
		for _, si := range s.SI {
			sst = append(sst, si.text())
		}
	}
	if len(w.Sheets) > bookMaxSheets {
		return nil, badReq("الملف بيه أوراق هواية")
	}
	cells := 0
	var out []bookSheet
	for i, s := range w.Sheets {
		p := targets[s.ID]
		if p == "" {
			p = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		}
		x, err := zipFile(z, p)
		if err != nil {
			continue
		}
		rows, err := parseSheetXML(x, sst)
		if err != nil {
			return nil, err
		}
		sh := bookSheet{Name: strings.TrimSpace(s.Name)}
		for _, row := range rows {
			line := make([]string, len(row))
			for c, v := range row {
				line[c] = v.s
			}
			cells += len(line)
			sh.Rows = append(sh.Rows, line)
		}
		if cells > bookMaxCells {
			return nil, badReq("الملف كبير هواية")
		}
		out = append(out, sh)
	}
	return out, nil
}

// POST /api/xlsx/write {name, sheets} ← ملف xlsx
func handleBookWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req struct {
		Name   string      `json:"name"`
		Sheets []bookSheet `json:"sheets"`
	}
	if err := readJSONBody(r, bookMaxBody, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	cells := 0
	for _, s := range req.Sheets {
		for _, row := range s.Rows {
			cells += len(row)
		}
	}
	if len(req.Sheets) == 0 || len(req.Sheets) > bookMaxSheets || cells > bookMaxCells {
		writeErr(w, badReq("البيانات كبيرة هواية للتصدير"), "")
		return
	}
	b, err := writeBook(req.Sheets)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "الحضور.xlsx"
	}
	audit(r, "bookExport", map[string]any{"file": name, "what": strconv.Itoa(len(req.Sheets)) + " ورقة"}, nil)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Write(b)
}

// POST /api/xlsx/read (الملف نفسه) ← {sheets}
func handleBookRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 30<<20))
	if err != nil || len(buf) == 0 {
		writeErr(w, badReq("ما گدرت أستلم الملف"), "")
		return
	}
	if !bytes.HasPrefix(buf, []byte("PK")) {
		writeErr(w, badReq("اختار ملف Excel الشامل (.xlsx) اللي طلّعه البرنامج"), "")
		return
	}
	sheets, err := readBook(buf)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"sheets": sheets})
}
