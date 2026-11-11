package server

import (
	"os"
	"strings"
	"testing"
)

func parseFile(t *testing.T, path string) *leaveParse {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readSheet(b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseLeaveSheet(rows)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func byRow(p *leaveParse) map[int]leaveRec {
	m := map[int]leaveRec{}
	for _, r := range p.recs {
		m[r.Row] = r
	}
	return m
}

func TestLeavesFixture(t *testing.T) {
	p := parseFile(t, "testdata/leaves_sample.xlsx")
	if p.cols.idHeader != "رقم البصمة" {
		t.Fatalf("id column = %q", p.cols.idHeader)
	}
	r := byRow(p)
	check := func(row int, id, kind, from, to, status string, days float64, mins int) {
		t.Helper()
		x, ok := r[row]
		if !ok {
			t.Fatalf("row %d missing (warnings: %v)", row, p.warnings)
		}
		if x.UserID != id || x.Kind != kind || x.From != from || x.To != to || x.Status != status || x.Days != days || x.Minutes != mins {
			t.Fatalf("row %d = %+v", row, x)
		}
	}
	// العنوان بالسطر 1 والرؤوس بالسطر 3، فالبيانات من السطر 4
	check(4, "1", "day", "2026-10-07", "2026-10-07", "approved", 1, 0)
	check(5, "2", "day", "2026-10-06", "2026-10-08", "approved", 3, 0)    // يوم/شهر/سنة
	check(6, "3", "hour", "2026-10-09", "2026-10-09", "approved", 0, 105) // 1:45
	check(7, "4", "hour", "2026-10-10", "2026-10-10", "approved", 0, 180) // تاريخ إكسل رقمي + 0.125 يوم
	check(8, "5", "day", "2026-10-14", "2026-10-14", "pending", 1, 0)     // أرقام عربية + بانتظار
	check(9, "5", "day", "2026-10-14", "2026-10-14", "withdrawn", 1, 0)
	check(10, "6", "hour", "2026-10-15", "2026-10-15", "approved", 0, 150) // «موافقة نهائية» = معتمدة، والساعات 2.5
	check(14, "8", "day", "2026-10-18", "2026-10-20", "approved", 3, 0)    // من/إلى معكوسة
	check(15, "9", "day", "2026-10-21", "2026-10-21", "pending", 1, 0)     // حالة غير معروفة → بانتظار
	if _, ok := r[11]; ok {
		t.Fatal("non-numeric id accepted")
	}
	if _, ok := r[12]; ok {
		t.Fatal("invalid date accepted")
	}
	if p.skipped != 2 {
		t.Fatalf("skipped = %d, warnings %v", p.skipped, p.warnings)
	}
	joined := strings.Join(p.warnings, " | ")
	if !strings.Contains(joined, "تحت التدقيق") {
		t.Fatalf("unknown status not reported: %s", joined)
	}
}

func TestLeaveStatus(t *testing.T) {
	cases := map[string]string{
		"معتمد": "approved", "موافق": "approved", "بانتظار المسؤول المباشر": "pending", "بانتظار الموافقة": "pending",
		"بانتظار البديل": "pending", "مسحوب": "withdrawn", "مرفوض": "withdrawn", "ملغى": "withdrawn", "": "pending",
		"Approved": "approved", "تحت التدقيق": "pending",
	}
	for in, want := range cases {
		if got := classifyLeaveStatus(in, true); got != want {
			t.Errorf("%q → %s, want %s", in, got, want)
		}
	}
	if classifyLeaveStatus("", false) != "approved" {
		t.Error("no status column should mean approved")
	}
}

func TestLeaveValues(t *testing.T) {
	for in, want := range map[string]int{"1:00": 60, "1:45": 105, "2.5": 150, "3": 180, "٢:٣٠": 150, "0:00": 0} {
		got, _ := parseLeaveHours(sheetCell{s: arDigits.Replace(in)})
		if got != want {
			t.Errorf("hours %q = %d, want %d", in, got, want)
		}
	}
	if m, ok := parseLeaveHours(sheetCell{s: "0.0416666667", num: true}); !ok || m != 60 {
		t.Errorf("excel time fraction = %d", m)
	}
	for in, want := range map[string]string{"2026/10/29": "2026-10-29", "29/10/2026": "2026-10-29", "2026-10-29 08:00": "2026-10-29",
		"10/29/2026": "2026-10-29", "29.10.26": "2026-10-29"} {
		if got, err := parseLeaveDate(sheetCell{s: in}); err != nil || got != want {
			t.Errorf("date %q = %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"2026/02/30", "hello", "2026/10", ""} {
		if _, err := parseLeaveDate(sheetCell{s: bad}); err == nil {
			t.Errorf("bad date %q accepted", bad)
		}
	}
}

func TestLeavesCSV(t *testing.T) {
	csv := "\xef\xbb\xbfرقم البصمة;الاسم;من;إلى;الساعات;الأيام;الحالة\r\n12;علي;2026/10/05;2026/10/05;;1;معتمد\r\n13;زهراء;2026/10/06;2026/10/06;2:00;;معتمد\r\n"
	rows, err := readSheet([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseLeaveSheet(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.recs) != 2 || p.recs[0].Kind != "day" || p.recs[1].Kind != "hour" || p.recs[1].Minutes != 120 {
		t.Fatalf("csv = %+v", p.recs)
	}
	// Windows-1256 (ملفات CSV العربية من الإكسل القديم)
	enc, _ := encodeName("رقم البصمة,من,الأيام\r\n7,2026/10/05,1\r\n", "cp1256")
	rows, err = readSheet(enc)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := parseLeaveSheet(rows); err != nil || len(p.recs) != 1 || p.recs[0].UserID != "7" {
		t.Fatalf("cp1256 csv: %v %+v", err, p)
	}
}

func TestLeavesRejects(t *testing.T) {
	if _, err := readSheet([]byte{0xD0, 0xCF, 0x11, 0xE0, 0, 0}); err == nil || !strings.Contains(err.Error(), "xls") {
		t.Fatalf("old xls: %v", err)
	}
	rows, _ := readSheet([]byte("الاسم,القسم\r\nعلي,ق1\r\n"))
	if _, err := parseLeaveSheet(rows); err == nil {
		t.Fatal("file without id/date columns accepted")
	}
}

// الملف الحقيقي (فيه أسماء حقيقية فما ينحفظ بالمستودع): LEAVES=/path/file.xlsx go test -run Real
func TestLeavesRealFile(t *testing.T) {
	path := os.Getenv("LEAVES")
	if path == "" {
		t.Skip("LEAVES not set")
	}
	p := parseFile(t, path)
	counts := map[string]int{}
	for _, r := range p.recs {
		counts[r.Status]++
		counts[r.Kind]++
	}
	t.Logf("id column=%q rows=%d recs=%d skipped=%d counts=%v warnings=%v", p.cols.idHeader, p.rows, len(p.recs), p.skipped, counts, p.warnings)
	if p.cols.idHeader != "رقم الطلب" || len(p.recs) != 47 || counts["approved"] != 30 || counts["pending"] != 11 ||
		counts["withdrawn"] != 6 || counts["hour"] != 16 || counts["day"] != 31 || p.skipped != 0 {
		t.Fatal("unexpected parse of the real file")
	}
	multi := 0
	for _, r := range p.recs {
		if r.From != r.To {
			multi++
		}
		if r.Kind == "hour" && r.Minutes <= 0 {
			t.Fatalf("hourly leave without minutes: %+v", r)
		}
	}
	if multi != 4 {
		t.Fatalf("multi-day rows = %d", multi)
	}
}
