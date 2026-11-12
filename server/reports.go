package server

// تقارير محفوظة للموظفين: المدير يحفظ تقرير الفترة بعد التصفية بالإجازات، وكل موظف يدخل بحسابه
// ويشوف تقريره هو بس. كل فترة بملف data/reports/<من>_<إلى>.json، وحفظ نفس الفترة مرة ثانية يبدّلها.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type pubDay struct {
	Day       string `json:"day"` // 2026-10-04
	Dow       string `json:"dow"`
	In        string `json:"in"`  // 07:55 أو فارغ
	Out       string `json:"out"` // 14:05 أو فارغ
	InManual  string `json:"inManual,omitempty"`
	OutManual string `json:"outManual,omitempty"`
	Worked    int    `json:"worked"` // دقائق
	Late      int    `json:"late"`
	Early     int    `json:"early"`
	Overtime  int    `json:"overtime"`
	Status    string `json:"status"`           // منتظم، متأخر، بدون خروج، غائب، يوم عطلة...
	Result    string `json:"result,omitempty"` // نتيجة التصفية: مبرّر بإجازة، غير مبرّر...
	Leave     string `json:"leave,omitempty"`  // الإجازة أو الزمنية
	Note      string `json:"note,omitempty"`
}

type pubSummary struct {
	Present    int  `json:"present"`
	Absent     int  `json:"absent"`
	LateCount  int  `json:"lateCount"`
	LateMin    int  `json:"lateMin"`
	EarlyMin   int  `json:"earlyMin"`
	Overtime   int  `json:"overtime"`
	MissingIn  int  `json:"missingIn"`
	MissingOut int  `json:"missingOut"`
	Worked     int  `json:"worked"`
	Manual     int  `json:"manual"`
	ExcLeave   int  `json:"excLeave"`
	ExcHour    int  `json:"excHour"`
	Pend       int  `json:"pend"`
	BadAbsent  int  `json:"badAbsent"`
	BadMissing int  `json:"badMissing"`
	Exempt     bool `json:"exempt"`
}

type pubEmployee struct {
	Name    string     `json:"name"`
	Dept    string     `json:"dept"`
	Sched   string     `json:"sched"`
	Summary pubSummary `json:"summary"`
	Days    []pubDay   `json:"days"`
}

type pubReport struct {
	ID        string                 `json:"id"`
	From      string                 `json:"from"`
	To        string                 `json:"to"`
	SavedAt   string                 `json:"savedAt"`
	SavedBy   string                 `json:"savedBy"`
	General   string                 `json:"general"` // النظام العام وقت الحفظ
	Leaves    bool                   `json:"leaves"`  // بيه تصفية بتقرير الإجازات
	Employees map[string]pubEmployee `json:"employees"`
}

const (
	maxReportEmployees = 5000
	maxReportDays      = 400
)

var repMu sync.Mutex

func reportsDir() string {
	name := "reports"
	if demoMode {
		name = "reports-demo"
	}
	return filepath.Join(dataDir, "data", name)
}

func parseDay(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil && t.Format("2006-01-02") == s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

func clampInt(v int) int { return max(0, min(v, 1_000_000)) }

// checkReport يتأكد من التقرير قبل الحفظ ويقص أي نص طويل
func checkReport(rep *pubReport) error {
	from, ok1 := parseDay(rep.From)
	to, ok2 := parseDay(rep.To)
	if !ok1 || !ok2 {
		return badReq("حدد فترة التقرير (من تاريخ وإلى تاريخ)")
	}
	if to.Before(from) {
		return badReq("تاريخ النهاية قبل تاريخ البداية")
	}
	if to.Sub(from) > maxReportDays*24*time.Hour {
		return badReq(fmt.Sprintf("الفترة طويلة، الحد %d يوم", maxReportDays))
	}
	if len(rep.Employees) == 0 {
		return badReq("ما أكو موظفين بالتقرير. اسحب البيانات أول")
	}
	if len(rep.Employees) > maxReportEmployees {
		return badReq("عدد الموظفين بالتقرير كبير")
	}
	rep.General = clip(rep.General, 300)
	clean := make(map[string]pubEmployee, len(rep.Employees))
	for id, e := range rep.Employees {
		cid, err := cleanEmpID(id)
		if err != nil {
			return badReq("رقم موظف غير صحيح بالتقرير")
		}
		if len(e.Days) > maxReportDays+1 {
			return badReq("أيام الموظف " + cid + " أكثر من الفترة")
		}
		e.Name, e.Dept, e.Sched = clip(e.Name, 100), clip(e.Dept, 100), clip(e.Sched, 200)
		s := &e.Summary
		for _, v := range []*int{&s.Present, &s.Absent, &s.LateCount, &s.LateMin, &s.EarlyMin, &s.Overtime, &s.MissingIn,
			&s.MissingOut, &s.Worked, &s.Manual, &s.ExcLeave, &s.ExcHour, &s.Pend, &s.BadAbsent, &s.BadMissing} {
			*v = clampInt(*v)
		}
		for i := range e.Days {
			d := &e.Days[i]
			t, ok := parseDay(d.Day)
			if !ok || t.Before(from) || t.After(to) {
				return badReq("يوم خارج فترة التقرير للموظف " + cid)
			}
			d.Dow, d.In, d.Out = clip(d.Dow, 20), clip(d.In, 8), clip(d.Out, 8)
			d.InManual, d.OutManual = clip(d.InManual, 200), clip(d.OutManual, 200)
			d.Status, d.Result, d.Leave, d.Note = clip(d.Status, 100), clip(d.Result, 100), clip(d.Leave, 300), clip(d.Note, 300)
			d.Worked, d.Late, d.Early, d.Overtime = clampInt(d.Worked), clampInt(d.Late), clampInt(d.Early), clampInt(d.Overtime)
		}
		sort.Slice(e.Days, func(i, j int) bool { return e.Days[i].Day < e.Days[j].Day })
		clean[cid] = e
	}
	rep.Employees = clean
	rep.ID = rep.From + "_" + rep.To
	return nil
}

func reportFile(id string) (string, error) {
	from, to, ok := strings.Cut(id, "_")
	if _, ok1 := parseDay(from); !ok || !ok1 {
		return "", badReq("تقرير غير معروف")
	}
	if _, ok2 := parseDay(to); !ok2 {
		return "", badReq("تقرير غير معروف")
	}
	return filepath.Join(reportsDir(), id+".json"), nil
}

// نسخة من التقرير القديم بمجلد backups قبل ما ينكتب فوقه أو ينحذف
func backupReport(f, id string) error {
	b, err := os.ReadFile(f)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report_"+id+"_"+time.Now().Format("2006-01-02_15-04-05.000")+".json"), b, 0o600)
}

func saveReport(rep *pubReport) error {
	repMu.Lock()
	defer repMu.Unlock()
	f, err := reportFile(rep.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	if err := backupReport(f, rep.ID); err != nil {
		return fmt.Errorf("ما گدرت أحفظ نسخة من التقرير القديم، فما بدلته: %v", err)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	var check pubReport
	if rb, err := os.ReadFile(tmp); err != nil || json.Unmarshal(rb, &check) != nil {
		os.Remove(tmp)
		return errors.New("فشل التحقق من ملف التقرير")
	}
	return os.Rename(tmp, f)
}

func loadReport(id string) (*pubReport, error) {
	f, err := reportFile(id)
	if err != nil {
		return nil, err
	}
	repMu.Lock()
	b, err := os.ReadFile(f)
	repMu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil, apiErr{404, "التقرير مو موجود (يمكن انحذف)"}
	}
	if err != nil {
		return nil, err
	}
	var rep pubReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, errors.New("ملف التقرير تالف")
	}
	return &rep, nil
}

// كل التقارير المحفوظة (بدون أيام الموظفين)، الأحدث فترةً أولاً
type repHead struct {
	ID, From, To, SavedAt, SavedBy string
	IDs                            []string
}

func listReports() []repHead {
	repMu.Lock()
	ents, _ := os.ReadDir(reportsDir())
	repMu.Unlock()
	var out []repHead
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		f, err := reportFile(id)
		if err != nil {
			continue
		}
		repMu.Lock()
		b, err := os.ReadFile(f)
		repMu.Unlock()
		if err != nil {
			continue
		}
		var h struct {
			From, To, SavedAt, SavedBy string
			Employees                  map[string]json.RawMessage
		}
		if json.Unmarshal(b, &h) != nil {
			continue
		}
		ids := make([]string, 0, len(h.Employees))
		for k := range h.Employees {
			ids = append(ids, k)
		}
		out = append(out, repHead{ID: id, From: h.From, To: h.To, SavedAt: h.SavedAt, SavedBy: h.SavedBy, IDs: ids})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].To != out[j].To {
			return out[i].To > out[j].To
		}
		return out[i].From > out[j].From
	})
	return out
}

// ---------- للمدير ----------

func handleReports(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, h := range listReports() {
		list = append(list, map[string]any{"id": h.ID, "from": h.From, "to": h.To, "savedAt": h.SavedAt, "savedBy": h.SavedBy, "employees": len(h.IDs)})
	}
	writeJSON(w, 200, map[string]any{"reports": list})
}

func handleReportPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var rep pubReport
	if err := readJSONBody(r, 40<<20, &rep); err != nil {
		writeErr(w, err, "")
		return
	}
	if err := checkReport(&rep); err != nil {
		writeErr(w, err, "")
		return
	}
	me, _ := userOf(r)
	rep.SavedAt, rep.SavedBy = localISO(time.Now()), me.Name
	err := saveReport(&rep)
	audit(r, "reportPublish", map[string]any{"from": rep.From, "to": rep.To, "employees": len(rep.Employees)}, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أحفظ التقرير: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": rep.ID, "employees": len(rep.Employees), "savedAt": rep.SavedAt})
}

func handleReportDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	readJSONBody(r, 1024, &req)
	f, err := reportFile(req.ID)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	repMu.Lock()
	err = backupReport(f, req.ID)
	if err == nil {
		err = os.Remove(f)
		if errors.Is(err, os.ErrNotExist) {
			err = apiErr{404, "التقرير مو موجود"}
		}
	}
	repMu.Unlock()
	audit(r, "reportDelete", map[string]any{"report": req.ID}, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أحذف التقرير: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- للموظف: تقريره هو بس ----------

func onlyEmployee(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := userOf(r); !ok || u.Role != RoleEmployee || u.EmpID == "" {
			deny(w, r, r.URL.Path)
			return
		}
		h(w, r)
	}
}

func findEmp(ids []string, empID string) (string, bool) {
	for _, id := range ids {
		if nid(id) == nid(empID) {
			return id, true
		}
	}
	return "", false
}

func handleMyReports(w http.ResponseWriter, r *http.Request) {
	me, _ := userOf(r)
	list := []map[string]any{}
	for _, h := range listReports() {
		if _, ok := findEmp(h.IDs, me.EmpID); ok {
			list = append(list, map[string]any{"id": h.ID, "from": h.From, "to": h.To, "savedAt": h.SavedAt})
		}
	}
	writeJSON(w, 200, map[string]any{"reports": list, "empId": me.EmpID})
}

func handleMyReport(w http.ResponseWriter, r *http.Request) {
	me, _ := userOf(r)
	rep, err := loadReport(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, err, "")
		return
	}
	ids := make([]string, 0, len(rep.Employees))
	for k := range rep.Employees {
		ids = append(ids, k)
	}
	key, ok := findEmp(ids, me.EmpID)
	if !ok {
		writeErr(w, apiErr{404, "ما أكو إلك تقرير بهذي الفترة"}, "")
		return
	}
	writeJSON(w, 200, map[string]any{"id": rep.ID, "from": rep.From, "to": rep.To, "savedAt": rep.SavedAt,
		"general": rep.General, "leaves": rep.Leaves, "empId": key, "me": rep.Employees[key]})
}
