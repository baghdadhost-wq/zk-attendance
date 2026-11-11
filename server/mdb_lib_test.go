package server

import (
	"math"
	"os"
	"testing"
	"time"
)

// يقارن القارئ بالقيم المتوقعة المكتوبة باختبارات مكتبة mdb-reader على نفس الملفات
func libTable(t *testing.T, file, table string) *jetTable {
	dir := os.Getenv("MDBLIB")
	if dir == "" {
		t.Skip("MDBLIB not set")
	}
	buf, err := os.ReadFile(dir + "/" + file)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openJet(buf)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	tables, err := db.tables()
	if err != nil {
		t.Fatalf("%s tables: %v", file, err)
	}
	tb, err := db.openTable(tables[table])
	if err != nil {
		t.Fatalf("%s %s: %v", file, table, err)
	}
	return tb
}

func TestLibFiles(t *testing.T) {
	for _, c := range []struct {
		file    string
		reverse bool
		aLen    int
		t4Len   int
	}{{"V2000/test.mdb", false, 100, 100}, {"V2003/test.mdb", false, 100, 100}, {"V2007/test.accdb", true, 100, 100}, {"V2010/test.accdb", true, 100, 100}} {
		tb := libTable(t, c.file, "TABLE1")
		rows, err := tb.rows()
		if err != nil || len(rows) < 2 {
			t.Fatalf("%s rows: %v %d", c.file, err, len(rows))
		}
		if c.reverse {
			rows[0], rows[1] = rows[len(rows)-1], rows[len(rows)-2]
		}
		r0, r1 := rows[0], rows[1]
		check := func(name string, got, want any) {
			if g, ok := got.(float64); ok {
				if w, ok := want.(float64); ok && math.Abs(g-w) < 1e-9 {
					return
				}
			}
			if got != want {
				t.Errorf("%s %s: got %#v want %#v", c.file, name, got, want)
			}
		}
		check("A0", r0["A"], "abcdefg")
		check("B0", r0["B"], "hijklmnop")
		check("C0", r0["C"], int64(2))
		check("D0", r0["D"], int64(222))
		check("E0", r0["E"], int64(333333333))
		check("F0", r0["F"], 444.555)
		check("G0", r0["G"].(time.Time).UnixMilli(), int64(148953600000))
		check("H0", r0["H"], 3.5)
		check("I0", r0["I"], true)
		check("A1", r1["A"], "a")
		check("C1", r1["C"], int64(0))
		check("G1", r1["G"].(time.Time).UnixMilli(), int64(376963200000))
		check("I1", r1["I"], false)
		names := ""
		for _, col := range tb.cols {
			names += col.name
		}
		check("columns", names, "ABCDEFGHI")
		t2 := libTable(t, c.file, "TABLE2")
		if len(t2.cols) != 89 || t2.cols[0].name != "column1" || t2.cols[88].name != "column89" || t2.cols[5].size != c.t4Len {
			t.Errorf("%s Table2 columns wrong: %d", c.file, len(t2.cols))
		}
		t.Logf("%s ok (%d rows)", c.file, len(rows))
	}

	tb := libTable(t, "real/ASampleDatabase.accdb", "ASSET ITEMS")
	rows, _ := tb.rows()
	uniq := map[any]bool{}
	for _, r := range rows {
		uniq[r["Asset No"]] = true
	}
	if len(rows) != 65 || len(uniq) != 65 {
		t.Errorf("Asset Items: rows=%d unique=%d (want 65/65)", len(rows), len(uniq))
	}
	t.Logf("ASampleDatabase: rows=%d unique=%d pages=%d", len(rows), len(uniq), len(tb.dataPages))

	del, _ := libTable(t, "V2016/withdeletedcol.accdb", "TABLE1").rows()
	t.Logf("withdeletedcol: %v", del)
	if len(del) != 1 || del[0]["col1"] != int64(0) || del[0]["col5"] != int64(4) || del[0]["col8"] != int64(7) || len(del[0]) != 7 {
		t.Errorf("withdeletedcol wrong")
	}
	ins, _ := libTable(t, "V2016/withinsertedcol.accdb", "TABLE1").rows()
	t.Logf("withinsertedcol: %v", ins)
	if len(ins) != 1 || ins[0]["col1"] != true || ins[0]["col2"] != true || ins[0]["col3"] != false {
		t.Errorf("withinsertedcol wrong")
	}
	lt, _ := libTable(t, "V2016/longtext.accdb", "TABLE1").rows()
	if s, _ := lt[0]["LongText"].(string); len([]rune(s)) != 5000 {
		t.Errorf("longtext length %d (want 5000)", len([]rune(s)))
	} else {
		t.Logf("longtext: 5000 chars over multiple pages ok")
	}
}
