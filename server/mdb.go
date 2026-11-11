package server

// قارئ قواعد بيانات Access (صيغة Jet4، مثل att2000.mdb من برنامج ZKTeco) بدون أي مكتبة خارجية.
// مبني على توثيق mdbtools (HACKING) وعلى منطق مكتبة mdb-reader.

import (
	"crypto/rc4"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	jetPageData  = 0x01
	jetPageTDEF  = 0x02
	jetPageUsage = 0x05
)

type jetDB struct {
	buf      []byte
	pageSize int
	header   []byte
	encKey   []byte
}

type jetColumn struct {
	name       string
	typ        byte
	index      int
	varIndex   int
	fixedIndex int
	size       int
	fixed      bool
}

type jetTable struct {
	db          *jetDB
	firstPage   int
	rowCount    int
	varColCount int
	cols        []jetColumn
	dataPages   []int
}

func openJet(buf []byte) (*jetDB, error) {
	if len(buf) < 4096 || buf[0] != 0 {
		return nil, errors.New("الملف مو قاعدة بيانات Access")
	}
	magic := string(buf[4:19])
	if magic != "Standard Jet DB" && magic != "Standard ACE DB" {
		return nil, errors.New("الملف مو قاعدة بيانات Access")
	}
	if buf[0x14] == 0 {
		return nil, errors.New("صيغة Access 97 القديمة غير مدعومة. افتح الملف بـ Access واحفظه بصيغة أحدث")
	}
	db := &jetDB{buf: buf, pageSize: 4096}
	db.header = append([]byte(nil), buf[:db.pageSize]...)
	c, _ := rc4.NewCipher([]byte{0xc7, 0xda, 0x39, 0x6b})
	c.XORKeyStream(db.header[0x18:0x18+128], db.header[0x18:0x18+128])
	if buf[0x14] == 1 { // Jet4: ممكن الصفحات مرمزة بمفتاح
		key := db.header[0x3e:0x42]
		for _, b := range key {
			if b != 0 {
				db.encKey = append([]byte(nil), key...)
				break
			}
		}
	}
	return db, nil
}

func (db *jetDB) page(n int) ([]byte, error) {
	if n == 0 {
		return db.header, nil
	}
	off := n * db.pageSize
	if n < 0 || off+db.pageSize > len(db.buf) {
		return nil, fmt.Errorf("الصفحة %d غير موجودة، الملف ناقص أو تالف", n)
	}
	p := db.buf[off : off+db.pageSize]
	if db.encKey != nil {
		k := u32(uint32(n))
		for i := range k {
			k[i] ^= db.encKey[i]
		}
		c, _ := rc4.NewCipher(k)
		out := make([]byte, len(p))
		c.XORKeyStream(out, p)
		return out, nil
	}
	return p, nil
}

func (db *jetDB) pageRow(ptr uint32) ([]byte, error) {
	p, err := db.page(int(ptr >> 8))
	if err != nil {
		return nil, err
	}
	row := int(ptr & 0xff)
	start := int(le.Uint16(p[14+row*2:]))
	next := db.pageSize
	if row > 0 {
		next = int(le.Uint16(p[12+row*2:]))
	}
	start &= 0x1fff
	next &= 0x1fff
	if row > 0 && next == 0 {
		next = db.pageSize
	}
	if start > next || next > len(p) {
		return nil, errors.New("سجل تالف بالملف")
	}
	return p[start:next], nil
}

func bitSet(bm []byte, i int) bool {
	if i/8 >= len(bm) {
		return false
	}
	return bm[i/8]&(1<<uint(i%8)) != 0
}

func (db *jetDB) usagePages(m []byte) ([]int, error) {
	var pages []int
	if len(m) < 5 {
		return nil, errors.New("خريطة صفحات الجدول تالفة")
	}
	switch m[0] {
	case 0:
		start := int(le.Uint32(m[1:]))
		bm := m[5:]
		for i := 0; i < len(bm)*8; i++ {
			if bitSet(bm, i) {
				pages = append(pages, start+i)
			}
		}
	case 1:
		bitmapLen := (db.pageSize - 4) * 8
		for mi := 0; 1+mi*4+4 <= len(m); mi++ {
			pn := int(le.Uint32(m[1+mi*4:]))
			if pn == 0 {
				continue
			}
			p, err := db.page(pn)
			if err != nil {
				return nil, err
			}
			if p[0] != jetPageUsage {
				return nil, errors.New("خريطة صفحات الجدول تالفة")
			}
			bm := p[4:]
			for i := 0; i < len(bm)*8; i++ {
				if bitSet(bm, i) {
					pages = append(pages, mi*bitmapLen+i)
				}
			}
		}
	default:
		return nil, errors.New("نوع خريطة صفحات غير معروف")
	}
	return pages, nil
}

func jetText(b []byte) string {
	if len(b) > 2 && b[0] == 0xff && b[1] == 0xfe { // نص Unicode مضغوط
		var u []uint16
		compressed := true
		for i := 2; i < len(b); {
			switch {
			case b[i] == 0:
				compressed = !compressed
				i++
			case compressed:
				u = append(u, uint16(b[i]))
				i++
			case i+1 < len(b):
				u = append(u, le.Uint16(b[i:]))
				i += 2
			default:
				i = len(b)
			}
		}
		return string(utf16.Decode(u))
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func (db *jetDB) openTable(first int) (*jetTable, error) {
	var def []byte
	seen := map[int]bool{}
	for next := first; next > 0; {
		if seen[next] {
			return nil, errors.New("تعريف جدول تالف")
		}
		seen[next] = true
		p, err := db.page(next)
		if err != nil {
			return nil, err
		}
		if p[0] != jetPageTDEF {
			return nil, errors.New("تعريف جدول تالف")
		}
		if def == nil {
			def = append([]byte(nil), p...)
		} else {
			def = append(def, p[8:]...)
		}
		next = int(le.Uint32(p[4:]))
	}
	t := &jetTable{db: db, firstPage: first,
		rowCount:    int(le.Uint32(def[16:])),
		varColCount: int(le.Uint16(def[43:]))}
	colCount := int(le.Uint16(def[45:]))
	realIdx := int(int32(le.Uint32(def[51:])))
	um, err := db.pageRow(le.Uint32(def[55:]))
	if err != nil {
		return nil, err
	}
	if t.dataPages, err = db.usagePages(um); err != nil {
		return nil, err
	}
	pos := 63 + realIdx*12
	names := pos + colCount*25
	for i := 0; i < colCount; i++ {
		c := def[pos : pos+25]
		nl := int(le.Uint16(def[names:]))
		names += 2
		col := jetColumn{
			name:       jetText(def[names : names+nl]),
			typ:        c[0],
			index:      int(c[5]),
			varIndex:   int(c[7]),
			fixedIndex: int(le.Uint16(c[21:])),
			size:       int(le.Uint16(c[23:])),
			fixed:      c[15]&0x01 != 0,
		}
		if col.typ == 0x01 {
			col.size = 0
		}
		names += nl
		t.cols = append(t.cols, col)
		pos += 25
	}
	sort.Slice(t.cols, func(a, b int) bool { return t.cols[a].index < t.cols[b].index })
	return t, nil
}

func (t *jetTable) value(p []byte, col jetColumn) any {
	switch col.typ {
	case 0x02:
		return int64(p[0])
	case 0x03:
		return int64(int16(le.Uint16(p)))
	case 0x04:
		return int64(int32(le.Uint32(p)))
	case 0x05:
		return float64(int64(le.Uint64(p))) / 10000
	case 0x06:
		return float64(math.Float32frombits(le.Uint32(p)))
	case 0x07:
		return math.Float64frombits(le.Uint64(p))
	case 0x08: // أيام من 1899-12-30
		days := math.Float64frombits(le.Uint64(p))
		return time.UnixMilli(int64(math.Round((days - 25569) * 86400000))).UTC()
	case 0x0a:
		return jetText(p)
	case 0x0c:
		if b := t.memo(p); b != nil {
			return jetText(b)
		}
	}
	return nil
}

// نص طويل (Memo): مضمن بالسجل أو بصفحات منفصلة
func (t *jetTable) memo(p []byte) []byte {
	if len(p) < 12 {
		return nil
	}
	l := int(le.Uint32(p) & 0x3fffffff)
	flag := le.Uint32(p) & 0xc0000000
	switch flag {
	case 0x80000000:
		if 12+l <= len(p) {
			return p[12 : 12+l]
		}
	case 0x40000000:
		if r, err := t.db.pageRow(le.Uint32(p[4:])); err == nil && l <= len(r) {
			return r[:l]
		}
	default:
		var out []byte
		ptr := le.Uint32(p[4:])
		for guard := 0; ptr != 0 && len(out) < l && guard < 10000; guard++ {
			r, err := t.db.pageRow(ptr)
			if err != nil || len(r) < 4 {
				return nil
			}
			ptr = le.Uint32(r)
			out = append(out, r[4:]...)
		}
		if len(out) >= l {
			return out[:l]
		}
	}
	return nil
}

func (t *jetTable) rows() ([]map[string]any, error) {
	ps := t.db.pageSize
	var out []map[string]any
	for _, pn := range t.dataPages {
		pg, err := t.db.page(pn)
		if err != nil {
			return nil, err
		}
		if pg[0] != jetPageData || int(le.Uint32(pg[4:])) != t.firstPage {
			continue
		}
		count := int(le.Uint16(pg[12:]))
		for r := 0; r < count; r++ {
			start := int(le.Uint16(pg[14+r*2:]))
			if start&0x4000 != 0 { // سجل محذوف
				continue
			}
			start &= 0x1fff
			next := ps
			if r > 0 {
				next = int(le.Uint16(pg[12+r*2:])) & 0x1fff
			}
			if row, ok := t.parseRow(pg, start, next-1); ok {
				out = append(out, row)
			}
		}
	}
	return out, nil
}

func (t *jetTable) parseRow(pg []byte, start, end int) (row map[string]any, ok bool) {
	defer func() {
		if recover() != nil { // سجل تالف: نتجاوزه
			row, ok = nil, false
		}
	}()
	if start < 14 || end >= len(pg) || end <= start {
		return nil, false
	}
	colCount := int(le.Uint16(pg[start:]))
	bm := (colCount + 7) / 8
	varCount := 0
	var varOffs []int
	if t.varColCount > 0 {
		varCount = int(le.Uint16(pg[end-bm-1:]))
		for i := 0; i <= varCount; i++ {
			varOffs = append(varOffs, int(le.Uint16(pg[end-bm-3-i*2:])))
		}
	}
	fixedCount := colCount - varCount
	nullMask := pg[end-bm+1 : end+1]
	fixedFound := 0
	row = map[string]any{}
	for _, c := range t.cols {
		notNull := bitSet(nullMask, c.index)
		var s, n int
		has := true
		switch {
		case c.fixed && fixedFound < fixedCount:
			s, n = start+c.fixedIndex+2, c.size
			fixedFound++
		case !c.fixed && c.varIndex < varCount:
			s = start + varOffs[c.varIndex]
			n = varOffs[c.varIndex+1] - varOffs[c.varIndex]
		default:
			has = false
		}
		switch {
		case c.typ == 0x01:
			row[c.name] = notNull
		case !has || !notNull || n < 0 || s+n > len(pg):
			row[c.name] = nil
		default:
			row[c.name] = t.value(pg[s:s+n], c)
		}
	}
	return row, true
}

// جداول المستخدم من MSysObjects (الصفحة 2)
func (db *jetDB) tables() (map[string]int, error) {
	sys, err := db.openTable(2)
	if err != nil {
		return nil, err
	}
	rows, err := sys.rows()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		name, _ := r["Name"].(string)
		typ, _ := r["Type"].(int64)
		id, _ := r["Id"].(int64)
		if name != "" && typ&0x7f == 1 {
			out[strings.ToUpper(name)] = int(id & 0x00ffffff)
		}
	}
	return out, nil
}

// ---------- قراءة الموظفين من att2000.mdb ----------

func pickVal(r map[string]any, cols ...string) any {
	for _, c := range cols {
		for k, v := range r {
			if strings.EqualFold(k, c) && v != nil {
				if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
					continue
				}
				return v
			}
		}
	}
	return nil
}

func pickStr(r map[string]any, cols ...string) string {
	switch v := pickVal(r, cols...).(type) {
	case string:
		return strings.TrimSpace(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func pickInt(r map[string]any, col string) int64 {
	switch v := pickVal(r, col).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func pickDate(r map[string]any, col string) string {
	t, ok := pickVal(r, col).(time.Time)
	if !ok || t.Year() < 1901 {
		return ""
	}
	return t.Format("2006-01-02")
}

func profilesFromAtt2000(buf []byte) (out map[string]profile, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, errors.New("الملف تالف أو صيغته غير مدعومة")
		}
	}()
	db, err := openJet(buf)
	if err != nil {
		return nil, err
	}
	tables, err := db.tables()
	if err != nil {
		return nil, errors.New("ما گدرت أقرا جداول الملف: " + err.Error())
	}
	up, ok := tables["USERINFO"]
	if !ok {
		return nil, errors.New("الملف ما بيه جدول الموظفين USERINFO. تأكد إنه ملف att2000.mdb من برنامج ZKTeco")
	}
	depts := map[int64]string{}
	if dp, ok := tables["DEPARTMENTS"]; ok {
		if dt, err := db.openTable(dp); err == nil {
			if rows, err := dt.rows(); err == nil {
				parents := map[int64]bool{}
				for _, d := range rows {
					if p := pickInt(d, "SUPDEPTID"); p != 0 {
						parents[p] = true
					}
				}
				for _, d := range rows {
					id := pickInt(d, "DEPTID")
					// القسم الجذر (اسم الشركة) ما نعتبره قسم إذا تحته أقسام
					if pickInt(d, "SUPDEPTID") == 0 && parents[id] {
						continue
					}
					depts[id] = pickStr(d, "DEPTNAME")
				}
			}
		}
	}
	ut, err := db.openTable(up)
	if err != nil {
		return nil, err
	}
	rows, err := ut.rows()
	if err != nil {
		return nil, err
	}
	out = map[string]profile{}
	for _, r := range rows {
		id := pickStr(r, "Badgenumber")
		if id == "" {
			continue
		}
		gender := ""
		switch strings.ToUpper(pickStr(r, "Gender")) {
		case "M":
			gender = "ذكر"
		case "F":
			gender = "أنثى"
		}
		var addr []string
		for _, c := range []string{"street", "CITY", "STATE"} {
			if v := pickStr(r, c); v != "" {
				addr = append(addr, v)
			}
		}
		out[id] = cleanProfile(map[string]any{
			"fullName":    pickStr(r, "Name"),
			"department":  depts[pickInt(r, "DEFAULTDEPTID")],
			"title":       pickStr(r, "TITLE"),
			"gender":      gender,
			"mobile":      pickStr(r, "PAGER"),
			"officePhone": pickStr(r, "OPHONE"),
			"homePhone":   pickStr(r, "FPHONE"),
			"birthday":    pickDate(r, "BIRTHDAY"),
			"hireDate":    pickDate(r, "HIREDDAY"),
			"address":     strings.Join(addr, "، "),
			"nationalId":  pickStr(r, "SSN"),
		})
	}
	return out, nil
}
