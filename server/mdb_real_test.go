package server

import (
	"os"
	"regexp"
	"testing"
	"unicode"
)

// يفحص القارئ على ملف att2000.mdb حقيقي (المسار بالمتغير MDB). يطبع إحصائيات بس، بدون بيانات شخصية.
func TestRealMdb(t *testing.T) {
	path := os.Getenv("MDB")
	if path == "" {
		t.Skip("MDB not set")
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openJet(buf)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := db.tables()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tables: %d (encoded pages: %v)", len(tables), db.encKey != nil)
	for _, name := range []string{"USERINFO", "DEPARTMENTS", "CHECKINOUT"} {
		tp, ok := tables[name]
		if !ok {
			t.Logf("%s: missing", name)
			continue
		}
		tb, err := db.openTable(tp)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rows, err := tb.rows()
		if err != nil {
			t.Fatalf("%s rows: %v", name, err)
		}
		t.Logf("%s: TDEF rowCount=%d parsed=%d columns=%d dataPages=%d", name, tb.rowCount, len(rows), len(tb.cols), len(tb.dataPages))
		if tb.rowCount != len(rows) {
			t.Errorf("%s: row count mismatch", name)
		}
		if name == "USERINFO" {
			types := map[string]byte{}
			for _, c := range tb.cols {
				types[c.name] = c.typ
			}
			t.Logf("  column types: Badgenumber=%#x Name=%#x Gender=%#x PAGER=%#x BIRTHDAY=%#x DEFAULTDEPTID=%#x", types["Badgenumber"], types["Name"], types["Gender"], types["PAGER"], types["BIRTHDAY"], types["DEFAULTDEPTID"])
		}
		if name == "DEPARTMENTS" {
			for _, d := range rows {
				t.Logf("  dept id=%v parent=%v name=%q", d["DEPTID"], d["SUPDEPTID"], d["DEPTNAME"])
			}
		}
	}
	profiles, err := profilesFromAtt2000(buf)
	if err != nil {
		t.Fatal(err)
	}
	digits := regexp.MustCompile(`^\d+$`)
	date := regexp.MustCompile(`^(19|20)\d\d-\d\d-\d\d$`)
	filled := map[string]int{}
	numericIDs, arabicNames, latinNames, badText := 0, 0, 0, 0
	for id, p := range profiles {
		if digits.MatchString(id) {
			numericIDs++
		}
		for k, v := range p {
			filled[k]++
			if (k == "birthday" || k == "hireDate") && !date.MatchString(v) {
				t.Errorf("bad date format in %s", k)
			}
			for _, r := range v {
				if r == unicode.ReplacementChar || (r < 32 && r != '\t') {
					badText++
				}
			}
		}
		hasAr, hasLat := false, false
		for _, r := range p["fullName"] {
			if unicode.Is(unicode.Arabic, r) {
				hasAr = true
			} else if unicode.IsLetter(r) && r < 0x250 {
				hasLat = true
			}
		}
		if hasAr {
			arabicNames++
		}
		if hasLat {
			latinNames++
		}
	}
	t.Logf("profiles=%d numericIDs=%d arabicNames=%d latinNames=%d badChars=%d", len(profiles), numericIDs, arabicNames, latinNames, badText)
	t.Logf("filled fields: %v", filled)
	if badText > 0 {
		t.Error("found broken characters")
	}
}
