package server

// تقرير الإجازات والزمنيات: يُرفع من نظام الإجازات (Excel ‎.xlsx‎ أو CSV) ويُستخدم لتصفية الغياب
// ونقص البصمة. الربط برقم البصمة فقط (الأسماء ممكن تختلف بالكتابة).
//   رقم بخانة الأيام  → إجازة (تبرّر غياب يوم كامل)
//   رقم بخانة الساعات → زمنية (تبرّر نقص بصمة الصباح أو المساء)
// بس الطلبات المعتمدة تبرّر؛ «بانتظار…» تنعرض بدون تبرير، و«مسحوب/مرفوض» تنهمل.

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type leaveRec struct {
	UserID     string  `json:"userId"`
	Name       string  `json:"name,omitempty"`
	Dept       string  `json:"dept,omitempty"`
	Kind       string  `json:"kind"` // day = إجازة ، hour = زمنية
	Type       string  `json:"type,omitempty"`
	From       string  `json:"from"` // 2006-01-02
	To         string  `json:"to"`
	Days       float64 `json:"days,omitempty"`
	Minutes    int     `json:"minutes,omitempty"`
	Status     string  `json:"status"` // approved | pending | withdrawn
	StatusText string  `json:"statusText,omitempty"`
	Row        int     `json:"row"`
}

type leaveImport struct {
	File      string `json:"file"`
	By        string `json:"by"`
	At        string `json:"at"`
	From      string `json:"from"`
	To        string `json:"to"`
	Rows      int    `json:"rows"`
	Approved  int    `json:"approved"`
	Pending   int    `json:"pending"`
	Withdrawn int    `json:"withdrawn"`
	Day       int    `json:"day"`
	Hour      int    `json:"hour"`
	Replaced  int    `json:"replaced"`
	IDColumn  string `json:"idColumn"`
}

type leaveData struct {
	Imports []leaveImport `json:"imports"`
	Leaves  []leaveRec    `json:"leaves"`
}

var leavesMu sync.Mutex

func leavesFile() string {
	name := "leaves.json"
	if demoMode {
		name = "leaves-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func loadLeaves() (leaveData, error) {
	d := leaveData{Imports: []leaveImport{}, Leaves: []leaveRec{}}
	b, err := os.ReadFile(leavesFile())
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("ملف الإجازات تالف: %v", err)
	}
	if d.Leaves == nil {
		d.Leaves = []leaveRec{}
	}
	if d.Imports == nil {
		d.Imports = []leaveImport{}
	}
	return d, nil
}

func saveLeaves(d leaveData) error {
	f := leavesFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(f); err == nil {
		os.WriteFile(f+".bak", old, 0o600)
	}
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// ---------- قراءة الملف (xlsx أو csv) ----------

type sheetCell struct {
	s   string
	num bool // خلية رقمية بالإكسل (تاريخ أو وقت ممكن يجي كرقم)
}

func readSheet(buf []byte) ([][]sheetCell, error) {
	switch {
	case bytes.HasPrefix(buf, []byte{0xD0, 0xCF, 0x11, 0xE0}):
		return nil, badReq("هذا ملف Excel قديم (.xls). افتحه بالإكسل واحفظه بصيغة .xlsx أو CSV وارفعه مرة ثانية")
	case bytes.HasPrefix(buf, []byte("PK")):
		return readXLSX(buf)
	default:
		return readCSVSheet(buf)
	}
}

type xRun struct {
	T string `xml:"t"`
}
type xSI struct {
	T string `xml:"t"`
	R []xRun `xml:"r"`
}

func (s xSI) text() string {
	var b strings.Builder
	b.WriteString(s.T)
	for _, r := range s.R {
		b.WriteString(r.T)
	}
	return b.String()
}

type xCell struct {
	R  string `xml:"r,attr"`
	T  string `xml:"t,attr"`
	V  string `xml:"v"`
	IS xSI    `xml:"is"`
}

type xRow struct {
	R int     `xml:"r,attr"`
	C []xCell `xml:"c"`
}

func zipFile(z *zip.Reader, name string) ([]byte, error) {
	for _, f := range z.File {
		if strings.EqualFold(f.Name, name) {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 200<<20))
		}
	}
	return nil, os.ErrNotExist
}

// مسار الورقة الأولى حسب ترتيب الأوراق بالملف
func firstSheetPath(z *zip.Reader) string {
	wb, err := zipFile(z, "xl/workbook.xml")
	if err == nil {
		var w struct {
			Sheets []struct {
				ID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			} `xml:"sheets>sheet"`
		}
		if xml.Unmarshal(wb, &w) == nil && len(w.Sheets) > 0 {
			if rb, err := zipFile(z, "xl/_rels/workbook.xml.rels"); err == nil {
				var rels struct {
					R []struct {
						ID     string `xml:"Id,attr"`
						Target string `xml:"Target,attr"`
					} `xml:"Relationship"`
				}
				if xml.Unmarshal(rb, &rels) == nil {
					for _, r := range rels.R {
						if r.ID == w.Sheets[0].ID {
							t := r.Target
							if strings.HasPrefix(t, "/") {
								return strings.TrimPrefix(t, "/")
							}
							return path.Join("xl", t)
						}
					}
				}
			}
		}
	}
	return "xl/worksheets/sheet1.xml"
}

func colIndex(ref string) int {
	n := 0
	for _, ch := range ref {
		if ch >= 'A' && ch <= 'Z' {
			n = n*26 + int(ch-'A'+1)
		} else if ch >= 'a' && ch <= 'z' {
			n = n*26 + int(ch-'a'+1)
		} else {
			break
		}
	}
	return n - 1
}

func readXLSX(buf []byte) ([][]sheetCell, error) {
	z, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, badReq("ملف Excel تالف أو مو بصيغة xlsx")
	}
	if _, err := zipFile(z, "xl/workbook.xml"); err != nil {
		return nil, badReq("الملف مو ملف Excel (xlsx)")
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
	sheetXML, err := zipFile(z, firstSheetPath(z))
	if err != nil {
		return nil, badReq("ما لگيت ورقة بيانات داخل ملف Excel")
	}
	var ws struct {
		Rows []xRow `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(sheetXML, &ws); err != nil {
		return nil, badReq("ما گدرت أقرا ورقة Excel")
	}
	out := make([][]sheetCell, 0, len(ws.Rows))
	for _, row := range ws.Rows {
		// الإكسل ما يكتب الأسطر الفارغة؛ نخلي الترقيم مطابق لأرقام أسطر الإكسل
		for row.R > 0 && len(out) < row.R-1 && len(out) < 200000 {
			out = append(out, nil)
		}
		var cells []sheetCell
		for i, c := range row.C {
			idx := i
			if c.R != "" {
				idx = colIndex(c.R)
			}
			if idx < 0 || idx > 500 {
				continue
			}
			for len(cells) <= idx {
				cells = append(cells, sheetCell{})
			}
			var v sheetCell
			switch c.T {
			case "s":
				if k, err := strconv.Atoi(strings.TrimSpace(c.V)); err == nil && k >= 0 && k < len(sst) {
					v.s = sst[k]
				}
			case "inlineStr":
				v.s = c.IS.text()
			case "str", "d":
				v.s = c.V
			case "b":
				v.s = c.V
			case "e":
				v.s = ""
			default: // رقم
				v.s, v.num = strings.TrimSpace(c.V), c.V != ""
			}
			cells[idx] = v
		}
		out = append(out, cells)
	}
	return out, nil
}

func readCSVSheet(buf []byte) ([][]sheetCell, error) {
	buf = bytes.TrimPrefix(buf, []byte("\xef\xbb\xbf"))
	text := string(buf)
	if !utf8.Valid(buf) {
		text = decodeCP1256(buf) // ملفات CSV العربية من الإكسل القديم
	}
	first := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		first = text[:i]
	}
	delim := ','
	if strings.Count(first, ";") > strings.Count(first, ",") {
		delim = ';'
	}
	if strings.Count(first, "\t") > strings.Count(first, string(delim)) {
		delim = '\t'
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = delim, -1, true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, badReq("ما گدرت أقرا ملف CSV: " + err.Error())
	}
	out := make([][]sheetCell, len(recs))
	for i, rec := range recs {
		out[i] = make([]sheetCell, len(rec))
		for j, v := range rec {
			out[i][j] = sheetCell{s: v}
		}
	}
	return out, nil
}

// ---------- الأعمدة ----------

var arNoise = regexp.MustCompile("[ً-ْٰـ]")
var arDigits = strings.NewReplacer("٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9",
	"۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9", "٫", ".", "٬", ",")
var arLetters = strings.NewReplacer("أ", "ا", "إ", "ا", "آ", "ا", "ٱ", "ا", "ى", "ي", "ة", "ه")

func normText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = arNoise.ReplaceAllString(s, "")
	s = arLetters.Replace(arDigits.Replace(s))
	return strings.Join(strings.Fields(s), " ")
}

type leaveCols struct {
	id, name, dept, typ, from, to, hours, days, status int
	idHeader                                           string
}

var colCandidates = map[string][]string{
	"id":     {"رقم البصمة", "رقم البصمه", "رقم الموظف", "الرقم الوظيفي", "رقم الطلب", "الرقم", "رقم", "id", "user id", "emp id", "employee id"},
	"name":   {"الاسم", "اسم الموظف", "الاسم الكامل", "name"},
	"dept":   {"القسم", "الشعبة", "الوحدة", "department"},
	"typ":    {"النوع", "نوع الاجازة", "نوع الإجازة", "type"},
	"from":   {"من", "من تاريخ", "تاريخ البداية", "تاريخ البدء", "البداية", "from", "start", "start date"},
	"to":     {"الى", "إلى", "الى تاريخ", "إلى تاريخ", "تاريخ النهاية", "النهاية", "to", "end", "end date"},
	"hours":  {"الساعات", "ساعات", "عدد الساعات", "المدة بالساعات", "hours"},
	"days":   {"الايام", "الأيام", "ايام", "أيام", "عدد الايام", "عدد الأيام", "المدة بالايام", "days"},
	"status": {"الحالة", "حالة الطلب", "status"},
}

// أعمدة ما لازم تنطابق كأيام/ساعات (الرصيد المتبقي مثلاً)
func excludedHeader(h string) bool {
	for _, w := range []string{"متبقي", "رصيد", "المتبقي", "remaining", "balance"} {
		if strings.Contains(h, w) {
			return true
		}
	}
	return false
}

func detectColumns(header []sheetCell) (leaveCols, int) {
	cols := leaveCols{-1, -1, -1, -1, -1, -1, -1, -1, -1, ""}
	norm := make([]string, len(header))
	for i, c := range header {
		norm[i] = normText(c.s)
	}
	used := map[int]bool{}
	pick := func(field string) int {
		for _, cand := range colCandidates[field] {
			nc := normText(cand)
			for i, h := range norm {
				if !used[i] && h == nc && !excludedHeader(h) {
					used[i] = true
					return i
				}
			}
		}
		// بداية العنوان (مثل «من تاريخ الطلب») — مو للرقم
		if field != "id" {
			for _, cand := range colCandidates[field] {
				nc := normText(cand)
				if utf8.RuneCountInString(nc) < 3 {
					continue
				}
				for i, h := range norm {
					if !used[i] && strings.HasPrefix(h, nc) && !excludedHeader(h) {
						used[i] = true
						return i
					}
				}
			}
		}
		return -1
	}
	// الترتيب مهم: الرقم أول حتى ما ياخذ «الرقم» عمود ثاني
	cols.id = pick("id")
	if cols.id >= 0 {
		cols.idHeader = strings.TrimSpace(header[cols.id].s)
	}
	cols.from, cols.to = pick("from"), pick("to")
	cols.hours, cols.days = pick("hours"), pick("days")
	cols.status, cols.typ = pick("status"), pick("typ")
	cols.name, cols.dept = pick("name"), pick("dept")
	score := 0
	for _, v := range []int{cols.id, cols.from, cols.to, cols.hours, cols.days, cols.status, cols.typ, cols.name} {
		if v >= 0 {
			score++
		}
	}
	return cols, score
}

// ---------- القيم ----------

func cellAt(row []sheetCell, i int) sheetCell {
	if i < 0 || i >= len(row) {
		return sheetCell{}
	}
	return sheetCell{s: strings.TrimSpace(arDigits.Replace(row[i].s)), num: row[i].num}
}

var dateSplit = regexp.MustCompile(`[/\-.]`)

// يقبل: 2026/10/29 ، 2026-10-29 ، 29/10/2026 ، تاريخ إكسل رقمي، ويا وقت بعده أو بدونه
func parseLeaveDate(c sheetCell) (string, error) {
	s := c.s
	if s == "" {
		return "", errors.New("تاريخ فارغ")
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && (c.num || (f > 20000 && f < 80000)) {
		if f < 20000 || f > 80000 {
			return "", errors.New("تاريخ غير مفهوم: " + s)
		}
		t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(math.Floor(f)))
		return t.Format("2006-01-02"), nil
	}
	if i := strings.IndexAny(s, " T"); i > 0 {
		s = s[:i]
	}
	p := dateSplit.Split(s, -1)
	if len(p) != 3 {
		return "", errors.New("تاريخ غير مفهوم: " + c.s)
	}
	n := make([]int, 3)
	for i, x := range p {
		v, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return "", errors.New("تاريخ غير مفهوم: " + c.s)
		}
		n[i] = v
	}
	var y, m, d int
	switch {
	case len(strings.TrimSpace(p[0])) == 4: // سنة/شهر/يوم
		y, m, d = n[0], n[1], n[2]
	case len(strings.TrimSpace(p[2])) == 4 || len(strings.TrimSpace(p[2])) == 2: // يوم/شهر/سنة (الأصل بالعراق)
		d, m, y = n[0], n[1], n[2]
		if y < 100 {
			y += 2000
		}
		if m > 12 && d <= 12 { // شهر/يوم/سنة
			d, m = m, d
		}
	default:
		return "", errors.New("تاريخ غير مفهوم: " + c.s)
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d || y < 2000 || y > 2100 {
		return "", errors.New("تاريخ غير صحيح: " + c.s)
	}
	return t.Format("2006-01-02"), nil
}

var firstNumber = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// الساعات: «1:45» أو «1.5» أو وقت إكسل (كسر من اليوم)
func parseLeaveHours(c sheetCell) (int, bool) {
	s := c.s
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ":") {
		p := strings.Split(s, ":")
		h, err1 := strconv.Atoi(strings.TrimSpace(firstNumber.FindString(p[0])))
		m := 0
		var err2 error
		if len(p) > 1 {
			m, err2 = strconv.Atoi(strings.TrimSpace(firstNumber.FindString(p[1])))
		}
		if err1 != nil || err2 != nil || h < 0 || m < 0 || m > 59 {
			return 0, false
		}
		t := h*60 + m
		return t, t > 0
	}
	f, err := strconv.ParseFloat(strings.Replace(firstNumber.FindString(s), ",", ".", 1), 64)
	if err != nil || f <= 0 {
		return 0, false
	}
	if c.num && f < 1 {
		return int(math.Round(f * 1440)), true
	}
	return int(math.Round(f * 60)), true
}

func parseLeaveDays(c sheetCell) (float64, bool) {
	f, err := strconv.ParseFloat(strings.Replace(firstNumber.FindString(c.s), ",", ".", 1), 64)
	if err != nil || f <= 0 {
		return 0, false
	}
	return f, true
}

func classifyLeaveStatus(text string, hasCol bool) string {
	if !hasCol {
		return "approved"
	}
	n := normText(text)
	if n == "" {
		return "pending"
	}
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(n, normText(w)) {
				return true
			}
		}
		return false
	}
	switch {
	case has("مسحوب", "مرفوض", "رفض", "ملغي", "ملغى", "الغاء", "إلغاء", "withdrawn", "rejected", "cancel", "denied"):
		return "withdrawn"
	case has("بانتظار", "انتظار", "قيد", "pending", "waiting"):
		return "pending"
	case has("معتمد", "موافق", "مقبول", "منفذ", "approved", "accepted"):
		return "approved"
	}
	return "pending"
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type leaveParse struct {
	recs     []leaveRec
	cols     leaveCols
	rows     int
	skipped  int
	warnings []string
}

func parseLeaveSheet(rows [][]sheetCell) (*leaveParse, error) {
	hdr, best := -1, 0
	var cols leaveCols
	for i := 0; i < len(rows) && i < 15; i++ {
		c, score := detectColumns(rows[i])
		if score > best && c.id >= 0 && c.from >= 0 {
			hdr, best, cols = i, score, c
		}
	}
	if hdr < 0 || best < 3 {
		var found []string
		if len(rows) > 0 {
			for _, c := range rows[0] {
				if strings.TrimSpace(c.s) != "" {
					found = append(found, strings.TrimSpace(c.s))
				}
			}
		}
		return nil, badReq("ما لگيت أعمدة الرقم والتاريخ بالملف. لازم يكون بيه عمود «رقم البصمة» (أو «رقم الطلب») وعمود «من». الأعمدة الموجودة: " + strings.Join(found, "، "))
	}
	if cols.hours < 0 && cols.days < 0 && cols.typ < 0 {
		return nil, badReq("ما لگيت عمود «الأيام» ولا «الساعات» ولا «النوع» بالملف، فما أگدر أميّز الإجازة من الزمنية")
	}
	p := &leaveParse{cols: cols}
	note := func(format string, a ...any) {
		if len(p.warnings) < 25 {
			p.warnings = append(p.warnings, fmt.Sprintf(format, a...))
		}
	}
	warn := func(format string, a ...any) { // سطر انهمل
		p.skipped++
		note(format, a...)
	}
	unknownStatus := map[string]bool{}
	for i := hdr + 1; i < len(rows) && i <= hdr+100000; i++ {
		row := rows[i]
		empty := true
		for _, c := range row {
			if strings.TrimSpace(c.s) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		p.rows++
		line := i + 1
		idc := cellAt(row, cols.id)
		id := idc.s
		if idc.num {
			if f, err := strconv.ParseFloat(id, 64); err == nil && f == math.Trunc(f) {
				id = strconv.FormatInt(int64(f), 10)
			}
		}
		if !digitsOnly(id) {
			warn("السطر %d: رقم البصمة «%s» مو رقم، انهمل", line, idc.s)
			continue
		}
		from, err := parseLeaveDate(cellAt(row, cols.from))
		if err != nil {
			warn("السطر %d: %v، انهمل", line, err)
			continue
		}
		to := from
		if tc := cellAt(row, cols.to); tc.s != "" {
			if t, err := parseLeaveDate(tc); err == nil {
				to = t
			} else {
				note("السطر %d: تاريخ «إلى» %v، اعتبرته نفس «من»", line, err)
			}
		}
		if to < from {
			from, to = to, from
		}
		f1, _ := time.Parse("2006-01-02", from)
		f2, _ := time.Parse("2006-01-02", to)
		span := int(f2.Sub(f1).Hours()/24) + 1
		if span > 366 {
			warn("السطر %d: مدة الإجازة أطول من سنة، انهمل", line)
			continue
		}
		mins, hasH := parseLeaveHours(cellAt(row, cols.hours))
		days, hasD := parseLeaveDays(cellAt(row, cols.days))
		typ := cellAt(row, cols.typ).s
		isHourType := strings.Contains(normText(typ), "زمني")
		kind := ""
		switch {
		case hasH && !hasD:
			kind = "hour"
		case hasD && !hasH:
			kind = "day"
		case hasH && hasD:
			kind = map[bool]string{true: "hour", false: "day"}[isHourType]
		case isHourType:
			kind = "hour"
		case typ != "" || cols.hours < 0:
			kind, days = "day", float64(span)
		default:
			warn("السطر %d: ما بيه أيام ولا ساعات، انهمل", line)
			continue
		}
		stText := cellAt(row, cols.status).s
		st := classifyLeaveStatus(stText, cols.status >= 0)
		if st == "pending" && stText != "" {
			n := normText(stText)
			if !strings.Contains(n, "انتظار") && !strings.Contains(n, "قيد") && !strings.Contains(n, "pending") && !unknownStatus[stText] {
				unknownStatus[stText] = true
				note("حالة غير معروفة «%s» اعتبرتها «بانتظار الموافقة» (ما تبرّر)", stText)
			}
		}
		rec := leaveRec{UserID: id, Name: cellAt(row, cols.name).s, Dept: cellAt(row, cols.dept).s, Kind: kind, Type: typ,
			From: from, To: to, Status: st, StatusText: stText, Row: line}
		if kind == "hour" {
			rec.Minutes = mins
		} else {
			rec.Days = days
		}
		p.recs = append(p.recs, rec)
	}
	if len(p.recs) == 0 {
		return nil, badReq("ما لگيت ولا طلب إجازة صالح بالملف")
	}
	return p, nil
}

// ---------- مسارات الخادم ----------

func handleLeaves(w http.ResponseWriter, r *http.Request) {
	leavesMu.Lock()
	d, err := loadLeaves()
	leavesMu.Unlock()
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, d)
}

func handleLeavesImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	name := strings.TrimSpace(filepath.Base(r.URL.Query().Get("name")))
	if rn := []rune(name); len(rn) > 120 {
		name = string(rn[:120])
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil || len(buf) == 0 {
		writeErr(w, badReq("ما گدرت أستلم الملف"), "")
		return
	}
	res, err := importLeaves(r, buf, name)
	detail := map[string]any{"file": name}
	if res != nil {
		detail["rows"], detail["approved"], detail["time"] = res.Rows, res.Approved, res.From+" → "+res.To
	}
	audit(r, "leavesImport", detail, err)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "import": res, "warnings": res.warnings, "skipped": res.skipped})
}

type importResult struct {
	leaveImport
	warnings []string
	skipped  int
}

func (r *importResult) MarshalJSON() ([]byte, error) { return json.Marshal(r.leaveImport) }

func importLeaves(r *http.Request, buf []byte, name string) (*importResult, error) {
	rows, err := readSheet(buf)
	if err != nil {
		return nil, err
	}
	p, err := parseLeaveSheet(rows)
	if err != nil {
		return nil, err
	}
	u, _ := userOf(r)
	imp := leaveImport{File: name, By: u.Name, At: localISO(time.Now()), Rows: p.rows, IDColumn: p.cols.idHeader}
	imp.From, imp.To = p.recs[0].From, p.recs[0].To
	for _, l := range p.recs {
		imp.From, imp.To = min(imp.From, l.From), max(imp.To, l.To)
		switch l.Status {
		case "approved":
			imp.Approved++
		case "pending":
			imp.Pending++
		default:
			imp.Withdrawn++
		}
		if l.Kind == "hour" {
			imp.Hour++
		} else {
			imp.Day++
		}
	}
	leavesMu.Lock()
	defer leavesMu.Unlock()
	d, err := loadLeaves()
	if err != nil {
		return nil, err
	}
	// الملف الجديد يحل محل الطلبات المحفوظة اللي تقع ضمن فترته، وتبقى الفترات الأقدم
	kept := d.Leaves[:0:0]
	for _, l := range d.Leaves {
		if l.From <= imp.To && l.To >= imp.From {
			imp.Replaced++
			continue
		}
		kept = append(kept, l)
	}
	d.Leaves = append(kept, p.recs...)
	sort.SliceStable(d.Leaves, func(i, j int) bool {
		if d.Leaves[i].From != d.Leaves[j].From {
			return d.Leaves[i].From < d.Leaves[j].From
		}
		return d.Leaves[i].UserID < d.Leaves[j].UserID
	})
	d.Imports = append(d.Imports, imp)
	if len(d.Imports) > 30 {
		d.Imports = d.Imports[len(d.Imports)-30:]
	}
	if err := saveLeaves(d); err != nil {
		return nil, err
	}
	return &importResult{leaveImport: imp, warnings: p.warnings, skipped: p.skipped}, nil
}

func handleLeavesClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	leavesMu.Lock()
	d, err := loadLeaves()
	n := len(d.Leaves)
	if err == nil {
		err = saveLeaves(leaveData{Imports: []leaveImport{}, Leaves: []leaveRec{}})
	}
	leavesMu.Unlock()
	audit(r, "leavesClear", map[string]any{"rows": n}, err)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed": n})
}
