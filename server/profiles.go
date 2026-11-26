package server

// البيانات الإضافية للموظفين (الهاتف، القسم...) تنحفظ بالحاسبة بملف data/employees.json
// نفس صيغة النسخة السابقة، فتگدر تنقل الملف بينهم.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var profileFields = []string{"fullName", "department", "title", "gender", "mobile", "officePhone", "homePhone",
	"birthday", "hireDate", "address", "nationalId", "notes",
	"attendance", "schedMode", "workStart", "workEnd", "offDays", "dayTimes", "exemptIn", "exemptOut"}

// دوام الموظف: يتبع النظام العام، أو إله نظام خاص (وقت دخول وخروج وأيام عطلة). والمو مشمول بالبصمة ما ينحسب عليه غياب ولا تأخير
const (
	attIncluded  = "مشمول"
	attExempt    = "غير مشمول"
	schedGeneral = "عام"
	schedCustom  = "خاص"
	noOffDays    = "بدون"
)

type profile = map[string]string

var profMu sync.Mutex

func profilesFile() string {
	name := "employees.json"
	if demoMode {
		name = "employees-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func loadProfiles() map[string]profile {
	all := map[string]profile{}
	b, err := os.ReadFile(profilesFile())
	if err == nil {
		json.Unmarshal(b, &all)
	}
	if all == nil {
		all = map[string]profile{}
	}
	return all
}

func saveProfiles(all map[string]profile) error {
	f := profilesFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(f); err == nil {
		os.WriteFile(f+".bak", old, 0o600)
	}
	b, err := json.MarshalIndent(all, "", " ")
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	var check map[string]profile
	if rb, err := os.ReadFile(tmp); err != nil || json.Unmarshal(rb, &check) != nil {
		return errors.New("فشل التحقق من ملف البيانات")
	}
	return os.Rename(tmp, f)
}

func cleanProfile(p map[string]any) profile {
	out := profile{}
	for _, k := range profileFields {
		v, ok := anyStr(p[k])
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		limit := 200
		if k == "notes" {
			limit = 1000
		}
		if r := []rune(v); len(r) > limit {
			v = string(r[:limit])
		}
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func normWord(v string) string {
	v = strings.ToLower(strings.Join(strings.Fields(v), " "))
	return strings.NewReplacer("ة", "ه", "أ", "ا", "إ", "ا", "آ", "ا", "ى", "ي").Replace(v)
}

// parseOffDays يقبل "5,6" أو أسماء الأيام ("الجمعة، السبت") أو "بدون"، ويرجع "5,6" أو "بدون"
func parseOffDays(v string) (string, error) {
	days, err := parseDaySet(v, "العطلة")
	if err != nil {
		return "", err
	}
	if days, err = cleanOffDays(days); err != nil {
		return "", err
	}
	return daySetString(days), nil
}

func daySetString(days []int) string {
	if len(days) == 0 {
		return noOffDays
	}
	out := make([]string, len(days))
	for i, d := range days {
		out[i] = strconv.Itoa(d)
	}
	return strings.Join(out, ",")
}

// dayOfWord رقم اليوم من رقمه أو اسمه («الخميس» أو «خميس»)، أو -1
func dayOfWord(part string) int {
	if n, err := strconv.Atoi(part); err == nil {
		if n >= 0 && n <= 6 {
			return n
		}
		return -1
	}
	w := normWord(strings.TrimPrefix(strings.TrimPrefix(part, "و"), "ال"))
	for i, name := range dayNamesAr {
		if normWord(strings.TrimPrefix(name, "ال")) == w {
			return i
		}
	}
	return -1
}

// parseDaySet قائمة أيام بدون تكرار ومرتبة (تقبل كل الأسبوع)
func parseDaySet(v, what string) ([]int, error) {
	v = strings.TrimSpace(toASCIIDigits(v))
	if v == "" || normWord(v) == normWord(noOffDays) || normWord(v) == "لا يوجد" {
		return []int{}, nil
	}
	if normWord(v) == normWord("كل الأيام") || normWord(v) == normWord("كل الايام") {
		return []int{0, 1, 2, 3, 4, 5, 6}, nil
	}
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '،' || r == '؛' || r == ';' || r == '/' || r == ' ' || r == '-'
	})
	seen := map[int]bool{}
	days := []int{}
	for _, part := range parts {
		if normWord(strings.TrimPrefix(strings.TrimPrefix(part, "و"), "ال")) == "" {
			continue // "الجمعة و السبت"
		}
		d := dayOfWord(part)
		if d < 0 {
			return nil, badReq("ما عرفت اليوم «" + part + "» بخانة " + what)
		}
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	sort.Ints(days)
	return days, nil
}

// parseDayTimes أوقات خاصة لأيام معيّنة: «4=08:00-13:00,0=09:00-14:00» أو «الخميس 08:00-13:00، الأحد 09:00-14:00»
// ويرجع الصيغة الموحدة مرتبة حسب اليوم، أو "" إذا ماكو.
func parseDayTimes(v string) (string, error) {
	v = strings.TrimSpace(toASCIIDigits(v))
	if v == "" || normWord(v) == normWord(noOffDays) {
		return "", nil
	}
	items := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '،' || r == '؛' || r == ';' || r == '\n' })
	got := map[int]string{}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		m := dayTimeRe.FindStringSubmatch(it)
		if m == nil {
			return "", badReq("وقت اليوم «" + it + "» مو مفهوم، اكتبه مثل: الخميس 08:00-13:00")
		}
		d := dayOfWord(strings.TrimSpace(m[1]))
		if d < 0 {
			return "", badReq("ما عرفت اليوم «" + m[1] + "»")
		}
		s, e, err := checkTimes(m[2], m[3])
		if err != nil {
			return "", badReq(dayNamesAr[d] + ": " + err.Error())
		}
		got[d] = s + "-" + e
	}
	var out []string
	for d := 0; d < 7; d++ {
		if t, ok := got[d]; ok {
			out = append(out, strconv.Itoa(d)+"="+t)
		}
	}
	return strings.Join(out, ","), nil
}

var dayTimeRe = regexp.MustCompile(`^\s*([^\d=:\s][^=:]*?|\d)\s*[=:]?\s*(\d{1,2}:\d{2})\s*(?:-|–|—|إلى|الى|to)\s*(\d{1,2}:\d{2})\s*$`)

// normSchedFields يوحّد حقول الدوام ويتأكد منها. النظام العام ما يحتاج أوقات، فتنشال
func normSchedFields(p profile) error {
	if v, ok := p["attendance"]; ok {
		switch normWord(v) {
		case normWord(attIncluded), normWord("مشمولة"), "نعم", "yes", "included":
			p["attendance"] = attIncluded
		case normWord(attExempt), normWord("غير مشمولة"), "لا", "no", "exempt":
			p["attendance"] = attExempt
		default:
			return badReq("خانة البصمة لازم تكون «مشمول» أو «غير مشمول»")
		}
	}
	if v, ok := p["schedMode"]; ok {
		switch normWord(v) {
		case normWord(schedGeneral), normWord("النظام العام"), "general":
			p["schedMode"] = schedGeneral
		case normWord(schedCustom), normWord("نظام خاص"), "custom":
			p["schedMode"] = schedCustom
		default:
			return badReq("نظام الدوام لازم يكون «عام» أو «خاص»")
		}
	}
	if p["schedMode"] != schedCustom {
		for _, k := range []string{"workStart", "workEnd", "offDays", "dayTimes", "exemptIn", "exemptOut"} {
			delete(p, k)
		}
		return nil
	}
	s, e, err := checkTimes(p["workStart"], p["workEnd"])
	if err != nil {
		return err
	}
	off, err := parseOffDays(p["offDays"])
	if err != nil {
		return err
	}
	p["workStart"], p["workEnd"], p["offDays"] = s, e, off
	dt, err := parseDayTimes(p["dayTimes"])
	if err != nil {
		return err
	}
	setOrDel(p, "dayTimes", dt)
	for k, what := range map[string]string{"exemptIn": "استثناء البصمة الصباحية", "exemptOut": "استثناء البصمة المسائية"} {
		days, err := parseDaySet(p[k], what)
		if err != nil {
			return err
		}
		setOrDel(p, k, map[bool]string{true: "", false: daySetString(days)}[len(days) == 0])
	}
	return nil
}

func setOrDel(p profile, k, v string) {
	if v == "" {
		delete(p, k)
	} else {
		p[k] = v
	}
}

func daysText(v string) string {
	days := []int{}
	for _, x := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(x); err == nil {
			days = append(days, n)
		}
	}
	return offDaysText(days)
}

// schedSummary وصف دوام الموظف للسجل
func schedSummary(p profile) string {
	out := "النظام العام"
	if p["schedMode"] == schedCustom {
		days := []int{}
		if p["offDays"] != noOffDays {
			for _, x := range strings.Split(p["offDays"], ",") {
				if n, err := strconv.Atoi(x); err == nil {
					days = append(days, n)
				}
			}
		}
		out = "خاص " + p["workStart"] + "-" + p["workEnd"] + "، العطلة: " + offDaysText(days)
		if p["dayTimes"] != "" {
			var parts []string
			for _, it := range strings.Split(p["dayTimes"], ",") {
				d, t, _ := strings.Cut(it, "=")
				if n, err := strconv.Atoi(d); err == nil && n >= 0 && n < 7 {
					parts = append(parts, dayNamesAr[n]+" "+t)
				}
			}
			out += "، أوقات خاصة: " + strings.Join(parts, " و")
		}
		if p["exemptIn"] != "" {
			out += "، بدون بصمة صباحية: " + daysText(p["exemptIn"])
		}
		if p["exemptOut"] != "" {
			out += "، بدون بصمة مسائية: " + daysText(p["exemptOut"])
		}
	}
	if p["attendance"] == attExempt {
		out = "غير مشمول بالبصمة، " + out
	}
	return out
}

// keepSched يرجّع حقول الدوام مثل ما محفوظة (للي ما عنده صلاحية تغيير الأوقات)
func keepSched(p, before profile) {
	for _, k := range schedKeys {
		if v, ok := before[k]; ok {
			p[k] = v
		} else {
			delete(p, k)
		}
	}
}

var schedKeys = []string{"attendance", "schedMode", "workStart", "workEnd", "offDays", "dayTimes", "exemptIn", "exemptOut"}

// mode: merge = القيم الجديدة تكتب فوق القديمة | fill = تملأ الحقول الفارغة بس
func importProfiles(incoming map[string]map[string]any, mode string) (map[string]any, error) {
	profMu.Lock()
	defer profMu.Unlock()
	all := loadProfiles()
	added, updated, unchanged, schedBad := 0, 0, 0, 0
	for rawID, p := range incoming {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		add := cleanProfile(p)
		if len(add) == 0 {
			continue
		}
		before, existed := all[id]
		next := profile{}
		for k, v := range before {
			next[k] = v
		}
		for k, v := range add {
			if mode == "merge" || next[k] == "" {
				next[k] = v
			}
		}
		if normSchedFields(next) != nil { // دوام غلط بالملف: يبقى دوامه القديم
			schedBad++
			for _, k := range schedKeys {
				if v, ok := before[k]; ok {
					next[k] = v
				} else {
					delete(next, k)
				}
			}
		}
		switch {
		case !existed:
			added++
		case !reflect.DeepEqual(before, next):
			updated++
		default:
			unchanged++
		}
		all[id] = next
	}
	if err := saveProfiles(all); err != nil {
		return nil, err
	}
	return map[string]any{"added": added, "updated": updated, "unchanged": unchanged, "schedErrors": schedBad, "profiles": all}, nil
}

func handleProfiles(w http.ResponseWriter, r *http.Request) {
	profMu.Lock()
	all := loadProfiles()
	profMu.Unlock()
	writeJSON(w, 200, map[string]any{"profiles": all, "fields": profileFields})
}

func handleProfileSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  any            `json:"userId"`
		Profile map[string]any `json:"profile"`
	}
	if err := readJSONBody(r, 1<<17, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	id, _ := anyStr(req.UserID)
	id = strings.TrimSpace(id)
	if id == "" {
		writeErr(w, badReq("رقم الموظف مفقود"), "")
		return
	}
	profMu.Lock()
	defer profMu.Unlock()
	all := loadProfiles()
	clean := cleanProfile(req.Profile)
	if err := normSchedFields(clean); err != nil {
		writeErr(w, err, "")
		return
	}
	// الدوام و«مشمول بالبصمة» يحتاج صلاحية «دوام الموظف»، والباقي «تعديل الموظفين». اللي ما يحق إله يبقى مثل ما هو
	if !can(r, "emp.edit") {
		if !sameExceptSched(clean, all[id]) {
			deny(w, r, "profileEdit")
			return
		}
	}
	if !can(r, "emp.sched") {
		if schedSummary(clean) != schedSummary(all[id]) {
			deny(w, r, "profileSchedule")
			return
		}
		keepSched(clean, all[id])
	}
	detail := map[string]any{"userId": id}
	if st := schedSummary(clean); st != schedSummary(all[id]) {
		detail["sched"] = st
	}
	if len(clean) > 0 {
		all[id] = clean
	} else {
		delete(all, id)
	}
	err := saveProfiles(all)
	audit(r, "profileSave", detail, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أحفظ البيانات: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "profile": clean})
}

func handleProfileImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profiles map[string]map[string]any `json:"profiles"`
		Mode     string                    `json:"mode"`
	}
	if err := readJSONBody(r, 20<<20, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	mode := "fill"
	if req.Mode == "merge" {
		mode = "merge"
	}
	schedIgnored := 0
	if !can(r, "emp.sched") { // أعمدة الدوام بالملف تنهمل (تحتاج صلاحية «دوام الموظف»)
		for _, p := range req.Profiles {
			had := false
			for _, k := range schedKeys {
				if _, ok := p[k]; ok {
					delete(p, k)
					had = true
				}
			}
			if had {
				schedIgnored++
			}
		}
	}
	res, err := importProfiles(req.Profiles, mode)
	if err == nil {
		res["schedIgnored"] = schedIgnored
	}
	detail := map[string]any{"mode": mode}
	if err == nil {
		detail["added"], detail["updated"] = res["added"], res["updated"]
	}
	audit(r, "profileImport", detail, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أستورد البيانات: ")
		return
	}
	writeJSON(w, 200, res)
}

func handleImportMdb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<30))
	if err != nil {
		writeErr(w, badReq("ما گدرت أستلم الملف"), "")
		return
	}
	if len(buf) < 4096 {
		writeErr(w, badReq("الملف فارغ أو مو قاعدة بيانات"), "")
		return
	}
	profiles, err := profilesFromAtt2000(buf)
	if err != nil {
		writeErr(w, apiErr{500, err.Error()}, "")
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": profiles, "count": len(profiles)})
}

// sameExceptSched هل البيانات نفسها بغض النظر عن حقول الدوام (لصاحب «دوام الموظف» بدون «تعديل الموظفين»)
func sameExceptSched(a, b profile) bool {
	isSched := map[string]bool{}
	for _, k := range schedKeys {
		isSched[k] = true
	}
	for k, v := range a {
		if !isSched[k] && b[k] != v {
			return false
		}
	}
	for k, v := range b {
		if !isSched[k] && a[k] != v {
			return false
		}
	}
	return true
}
