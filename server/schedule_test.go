package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseOffDays(t *testing.T) {
	ok := map[string]string{
		"5":                  "5",
		"6,5":                "5,6",
		"الجمعة، السبت":      "5,6",
		"الجمعة و السبت":     "5,6",
		"الجمعة والسبت":      "5,6",
		"جمعة":               "5",
		"الاحد،الإثنين":      "0,1",
		"الأربعاء/الخميس":    "3,4",
		"٥،٦":                "5,6",
		"":                   noOffDays,
		"بدون":               noOffDays,
		"الجمعة، الجمعة":     "5",
		"السبت الاحد الجمعة": "0,5,6",
	}
	for in, want := range ok {
		got, err := parseOffDays(in)
		if err != nil || got != want {
			t.Errorf("parseOffDays(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"7", "الجمعه؟", "monday", "0,1,2,3,4,5,6"} {
		if got, err := parseOffDays(in); err == nil {
			t.Errorf("parseOffDays(%q) = %q, want an error", in, got)
		}
	}
}

func TestNormSchedFields(t *testing.T) {
	p := profile{"fullName": "x", "schedMode": "خاص", "workStart": "7:30", "workEnd": "13:00", "offDays": "الجمعة", "attendance": "مشمول"}
	if err := normSchedFields(p); err != nil {
		t.Fatal(err)
	}
	if p["workStart"] != "07:30" || p["workEnd"] != "13:00" || p["offDays"] != "5" || p["schedMode"] != schedCustom || p["attendance"] != attIncluded {
		t.Fatalf("not normalised: %v", p)
	}
	// النظام العام: الأوقات الخاصة تنشال
	p = profile{"schedMode": "النظام العام", "workStart": "07:30", "workEnd": "13:00", "offDays": "5", "attendance": "غير مشمولة"}
	if err := normSchedFields(p); err != nil {
		t.Fatal(err)
	}
	if _, has := p["workStart"]; has || p["schedMode"] != schedGeneral || p["attendance"] != attExempt {
		t.Fatalf("general schedule kept custom fields: %v", p)
	}
	bad := []profile{
		{"schedMode": "خاص", "workStart": "14:00", "workEnd": "08:00"},
		{"schedMode": "خاص", "workStart": "8", "workEnd": "14:00"},
		{"schedMode": "خاص", "workStart": "08:00"},
		{"schedMode": "ليلي"},
		{"attendance": "ربما"},
		{"schedMode": "خاص", "workStart": "08:00", "workEnd": "14:00", "offDays": "0,1,2,3,4,5,6"},
	}
	for _, b := range bad {
		if err := normSchedFields(b); err == nil {
			t.Errorf("accepted a bad schedule: %v", b)
		}
	}
}

func TestCheckSchedule(t *testing.T) {
	ws, err := checkSchedule(workSchedule{Start: "8:00", End: "14:30", Off: []int{6, 5, 5}, Grace: 10, Dedup: 3})
	if err != nil || ws.Start != "08:00" || len(ws.Off) != 2 || ws.Off[0] != 5 {
		t.Fatalf("got %+v, %v", ws, err)
	}
	for _, b := range []workSchedule{
		{Start: "14:00", End: "08:00"},
		{Start: "08:00", End: "14:00", Off: []int{7}},
		{Start: "08:00", End: "14:00", Grace: -1},
		{Start: "08:00", End: "14:00", Dedup: 500},
		{Start: "08:00", End: "14:00", Off: []int{0, 1, 2, 3, 4, 5, 6}},
	} {
		if _, err := checkSchedule(b); err == nil {
			t.Errorf("accepted %+v", b)
		}
	}
}

func TestSettingsSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	Configure(dir, false)
	s := getSettings()
	if s.ScheduleSaved || s.Schedule.Start != "08:00" {
		t.Fatalf("defaults wrong: %+v", s)
	}
	setMu.Lock()
	s.Schedule.Start, s.ScheduleSaved = "07:00", true
	if err := saveSettingsLocked(s); err != nil {
		t.Fatal(err)
	}
	setMu.Unlock()
	Configure(dir, false) // يمسح الذاكرة ويقرا من الملف
	if s = getSettings(); s.Schedule.Start != "07:00" || !s.ScheduleSaved {
		t.Fatalf("not reloaded: %+v", s)
	}
}

type tclient struct {
	t      *testing.T
	h      http.Handler
	c      *http.Cookie
	remote string // عنوان الجهاز اللي يطلب (الافتراضي جهاز ثاني مو 127.0.0.1)
}

func (c *tclient) do(method, path, body string) (int, map[string]any, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if c.remote != "" {
		r.RemoteAddr = c.remote
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.c != nil {
		r.AddCookie(c.c)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	var j map[string]any
	json.Unmarshal(w.Body.Bytes(), &j)
	return w.Code, j, w
}

func login(t *testing.T, h http.Handler, user, pass string) *tclient {
	t.Helper()
	c := &tclient{t: t, h: h}
	code, j, w := c.do("POST", "/api/login", `{"username":"`+user+`","password":"`+pass+`"}`)
	if code != 200 {
		t.Fatalf("login %s: %d %v", user, code, j)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == sessionCookie {
			if ck.MaxAge != 0 || !ck.Expires.IsZero() {
				t.Fatal("session cookie must end with the browser")
			}
			c.c = ck
		}
	}
	return c
}

// الصلاحية المطلقة والمدير والموظف، والتقارير المحفوظة للموظفين
func TestAccountsAndReports(t *testing.T) {
	store := newStore(t)
	store.AllowWebSetup()
	h := NewHandler(store)
	anon := &tclient{t: t, h: h, remote: "127.0.0.1:5000"}
	if code, j, _ := anon.do("GET", "/api/session", ""); j["setup"] != true || code != 200 {
		t.Fatalf("first run must ask for the first account: %v", j)
	}
	// أول دخول ينشئ الصلاحية المطلقة (من نفس الحاسبة)
	r := httptest.NewRequest("POST", "/api/setup", strings.NewReader(`{"username":"owner1","password":"Owner#2026x"}`))
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"super"`) {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	if code, _, _ := anon.do("POST", "/api/login", `{"username":"owner1","password":"wrong-one"}`); code != 401 {
		t.Fatal("wrong password must be refused")
	}
	if code, _, _ := anon.do("GET", "/api/data", ""); code != 401 {
		t.Fatal("no entry without login")
	}
	sup := login(t, h, "owner1", "Owner#2026x")
	if code, j, _ := sup.do("POST", "/api/users/save", `{"create":true,"username":"admin","password":"Mgr#2026xx","role":"admin"}`); code != 200 {
		t.Fatalf("super creates admin: %d %v", code, j)
	}
	sup.do("POST", "/api/users/save", `{"create":true,"username":"sup1","password":"sup12345","role":"supervisor"}`)
	adm := login(t, h, "admin", "Mgr#2026xx")

	// المدير يدير حسابات الموظفين بس
	for _, role := range []string{"admin", "super", "supervisor", "viewer"} {
		if code, _, _ := adm.do("POST", "/api/users/save", `{"create":true,"username":"x`+role+`","password":"xxxxxx99","role":"`+role+`"}`); code != 403 {
			t.Fatalf("admin created a %s account: %d", role, code)
		}
	}
	if code, j, _ := adm.do("POST", "/api/users/save", `{"create":true,"username":"zainab","password":"zain1234","role":"employee"}`); code != 400 {
		t.Fatalf("employee without number: %d %v", code, j)
	}
	if code, j, _ := adm.do("POST", "/api/users/save", `{"create":true,"username":"zainab","password":"zain1234","role":"employee","empId":"2"}`); code != 200 {
		t.Fatalf("admin creates employee: %d %v", code, j)
	}
	adm.do("POST", "/api/users/save", `{"create":true,"username":"ahmed","password":"ahmd1234","role":"employee","empId":"1"}`)
	_, j, _ := adm.do("GET", "/api/users", "")
	for _, u := range j["users"].([]any) {
		m := u.(map[string]any)
		if m["role"] != "employee" && m["me"] != true {
			t.Fatalf("admin sees %v", m)
		}
	}
	if code, _, _ := adm.do("POST", "/api/users/save", `{"username":"sup1","password":"newpass99"}`); code != 403 {
		t.Fatal("admin changed a supervisor password")
	}
	if code, _, _ := adm.do("POST", "/api/users/delete", `{"username":"owner1"}`); code != 403 {
		t.Fatal("admin deleted the super account")
	}
	if code, _, _ := adm.do("POST", "/api/users/save", `{"username":"zainab","role":"supervisor"}`); code != 403 {
		t.Fatal("admin promoted an employee")
	}
	if code, _, _ := adm.do("POST", "/api/users/save", `{"username":"zainab","password":"zain5678"}`); code != 200 {
		t.Fatal("admin could not reset an employee password")
	}

	// تغيير الأوقات للصلاحية المطلقة بس
	sched := `{"schedule":{"start":"07:30","end":"13:30","off":[5,6],"grace":5,"dedup":2}}`
	if code, _, _ := adm.do("POST", "/api/settings/schedule", sched); code != 403 {
		t.Fatal("admin changed the general schedule")
	}
	if code, _, _ := sup.do("POST", "/api/settings/schedule", sched); code != 200 {
		t.Fatal("super could not change the general schedule")
	}
	sup.do("POST", "/api/profiles/save", `{"userId":"2","profile":{"fullName":"زينب","schedMode":"خاص","workStart":"07:00","workEnd":"13:00","offDays":"4,5"}}`)
	if code, _, _ := adm.do("POST", "/api/profiles/save", `{"userId":"2","profile":{"fullName":"زينب","schedMode":"عام"}}`); code != 403 {
		t.Fatal("admin changed an employee schedule")
	}
	if code, j, _ := adm.do("POST", "/api/profiles/save", `{"userId":"2","profile":{"fullName":"زينب","mobile":"07701112233","schedMode":"خاص","workStart":"07:00","workEnd":"13:00","offDays":"4,5"}}`); code != 200 {
		t.Fatalf("admin could not edit the phone: %d %v", code, j)
	}
	if _, j, _ := adm.do("POST", "/api/profiles/import", `{"mode":"merge","profiles":{"2":{"schedMode":"عام","department":"IT"}}}`); j["schedIgnored"] != float64(1) {
		t.Fatalf("admin import must ignore schedule columns: %v", j)
	}
	if _, j, _ := sup.do("GET", "/api/profiles", ""); !strings.Contains(fmt.Sprint(j["profiles"]), "workStart:07:00") || !strings.Contains(fmt.Sprint(j["profiles"]), "IT") {
		t.Fatalf("schedule lost or department not imported: %v", j["profiles"])
	}
	if code, _, _ := adm.do("POST", "/api/manual/add", `{"userId":"2","date":"2026-10-05","in":"08:00","reason":"duty"}`); code != 403 {
		t.Fatal("admin added a manual punch")
	}

	// حفظ تقرير للموظفين
	rep := `{"from":"2026-10-01","to":"2026-10-31","general":"08:00 - 14:00","leaves":true,"employees":{
	  "1":{"name":"أحمد","summary":{"present":20,"absent":1},"days":[{"day":"2026-10-04","dow":"الأحد","in":"07:55","out":"14:05","status":"منتظم"}]},
	  "02":{"name":"زينب","summary":{"present":21,"lateCount":2},"days":[{"day":"2026-10-05","status":"غائب","result":"مبرّر بإجازة"}]}}}`
	if code, _, _ := sup.do("POST", "/api/reports/publish", strings.Replace(rep, "2026-10-04", "2026-11-04", 1)); code != 400 {
		t.Fatal("a day outside the period was accepted")
	}
	if code, j, _ := adm.do("POST", "/api/reports/publish", rep); code != 200 || j["employees"] != float64(2) {
		t.Fatalf("publish: %d %v", code, j)
	}
	emp := login(t, h, "zainab", "zain5678")
	if code, _, w := emp.do("GET", "/", ""); code != 302 || w.Header().Get("Location") != "/me" {
		t.Fatalf("employee home must be /me: %d", code)
	}
	if code, _, w := adm.do("GET", "/me", ""); code != 302 || w.Header().Get("Location") != "/" {
		t.Fatalf("admin /me must go home: %d", code)
	}
	for _, path := range []string{"/api/data", "/api/profiles", "/api/leaves", "/api/manual", "/api/settings", "/api/reports", "/api/users", "/api/audit"} {
		if code, _, _ := emp.do("GET", path, ""); code != 403 {
			t.Fatalf("employee reached %s: %d", path, code)
		}
	}
	if code, _, _ := emp.do("POST", "/api/cmd", `{"ip":"127.0.0.1","action":"info"}`); code != 403 {
		t.Fatal("employee ran a device command")
	}
	_, j, _ = emp.do("GET", "/api/my/reports", "")
	if l, _ := j["reports"].([]any); len(l) != 1 {
		t.Fatalf("my reports: %v", j)
	}
	code, j, w := emp.do("GET", "/api/my/report?id=2026-10-01_2026-10-31", "")
	if code != 200 || strings.Contains(w.Body.String(), "أحمد") || !strings.Contains(w.Body.String(), "زينب") || j["empId"] != "02" {
		t.Fatalf("my report must hold only her own data: %d %s", code, w.Body)
	}
	if code, _, _ := adm.do("GET", "/api/my/reports", ""); code != 403 {
		t.Fatal("my reports is for employees")
	}
	ahmed := login(t, h, "ahmed", "ahmd1234")
	if _, _, w := ahmed.do("GET", "/api/my/report?id=2026-10-01_2026-10-31", ""); strings.Contains(w.Body.String(), "زينب") {
		t.Fatal("an employee saw a colleague")
	}
	if code, _, _ := emp.do("GET", "/api/my/report?id=../../users", ""); code != 400 {
		t.Fatal("bad report id accepted")
	}
	// حذف التقرير: يبقى منه نسخة
	if code, _, _ := adm.do("POST", "/api/reports/delete", `{"id":"2026-10-01_2026-10-31"}`); code != 200 {
		t.Fatal("delete report")
	}
	if _, j, _ := emp.do("GET", "/api/my/reports", ""); len(j["reports"].([]any)) != 0 {
		t.Fatal("deleted report still listed")
	}
	if m, _ := filepath.Glob(filepath.Join(dataDir, "backups", "report_2026-10-01_2026-10-31_*.json")); len(m) == 0 {
		t.Fatal("no backup of the deleted report")
	}
	// صلاحيات الموظف تبطل وية تغيير رقمه أو حذفه
	sup.do("POST", "/api/users/delete", `{"username":"zainab"}`)
	if code, _, _ := emp.do("GET", "/api/my/reports", ""); code != 401 {
		t.Fatal("deleted employee still logged in")
	}
}

func TestSessionPrune(t *testing.T) {
	s := &sessionStore{m: map[string]sessEntry{}}
	for i := 0; i < maxSessions+50; i++ {
		s.create(appUser{Username: fmt.Sprint("u", i)})
	}
	if len(s.m) > maxSessions {
		t.Fatalf("%d sessions kept", len(s.m))
	}
}

func TestShareLinks(t *testing.T) {
	store := newStore(t)
	store.create("owner1", "Owner#2026x", RoleSuper, "")
	store.create("emp", "emp12345", RoleEmployee, "4")
	store.AllowWebSetup()
	h := NewHandler(store)
	SetShareInfo("", "", 0)
	sup := login(t, h, "owner1", "Owner#2026x")
	if _, j, _ := sup.do("GET", "/api/share", ""); j["mode"] != ShareLocal || len(j["urls"].([]any)) != 0 {
		t.Fatalf("default must be local without links: %v", j)
	}
	SetShareInfo(SharePhone, "http", 3000)
	_, j, _ := sup.do("GET", "/api/share", "")
	urls, _ := j["urls"].([]any)
	if j["mode"] != SharePhone {
		t.Fatalf("phone mode: %v", j)
	}
	for _, u := range urls {
		if s, _ := u.(string); !strings.HasPrefix(s, "http://") || !strings.HasSuffix(s, ":3000") || strings.Contains(s, "127.0.0.1") {
			t.Fatalf("bad link %q", s)
		}
	}
	if len(LanIPs()) != len(urls) {
		t.Fatalf("links %v do not match addresses %v", urls, LanIPs())
	}
	if code, _, _ := login(t, h, "emp", "emp12345").do("GET", "/api/share", ""); code != 403 {
		t.Fatal("employees do not need the members link")
	}
	SetShareInfo("", "", 0)
}

// أول حساب ينعمل من نفس الجهاز بس: الأجهزة الثانية تشوف صفحة دخول عادية
func TestSetupOnlyOnTheDeviceItself(t *testing.T) {
	store := newStore(t)
	store.AllowWebSetup()
	h := NewHandler(store)
	ask := func(remote string) map[string]any {
		r := httptest.NewRequest("GET", "/api/session", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var j map[string]any
		json.Unmarshal(w.Body.Bytes(), &j)
		return j
	}
	if ask("127.0.0.1:5000")["setup"] != true {
		t.Fatal("the device itself must be offered the first-run setup")
	}
	if ask("192.168.1.30:5000")["setup"] != false {
		t.Fatal("another device must not be offered the first-run setup")
	}
	r := httptest.NewRequest("POST", "/api/setup", strings.NewReader(`{"username":"x1","password":"xxxxxx11"}`))
	r.RemoteAddr = "192.168.1.30:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("remote setup: %d", w.Code)
	}
}

func TestPerDaySchedule(t *testing.T) {
	ws, err := checkSchedule(workSchedule{Start: "08:00", End: "14:00", Off: []int{5}, Grace: 10, Dedup: 3,
		Days: map[string]dayTime{"4": {"8:00", "13:00"}, "0": {"08:00", "14:00"}}})
	if err != nil || len(ws.Days) != 1 || ws.Days["4"] != (dayTime{"08:00", "13:00"}) {
		t.Fatalf("days: %+v %v", ws.Days, err)
	}
	if _, err := checkSchedule(workSchedule{Start: "08:00", End: "14:00", Days: map[string]dayTime{"9": {"08:00", "13:00"}}}); err == nil {
		t.Error("bad day accepted")
	}
	if _, err := checkSchedule(workSchedule{Start: "08:00", End: "14:00", Days: map[string]dayTime{"4": {"13:00", "08:00"}}}); err == nil {
		t.Error("reversed times accepted")
	}
	p := profile{"schedMode": "خاص", "workStart": "08:00", "workEnd": "14:00", "offDays": "5",
		"dayTimes": "الخميس 08:00-12:30، 0=09:00–15:00", "exemptIn": "الخميس", "exemptOut": "كل الأيام"}
	if err := normSchedFields(p); err != nil {
		t.Fatal(err)
	}
	if p["dayTimes"] != "0=09:00-15:00,4=08:00-12:30" || p["exemptIn"] != "4" || p["exemptOut"] != "0,1,2,3,4,5,6" {
		t.Errorf("custom: %v", p)
	}
	if s := schedSummary(p); !strings.Contains(s, "الخميس 08:00-12:30") || !strings.Contains(s, "بدون بصمة صباحية: الخميس") {
		t.Error(s)
	}
	g := profile{"schedMode": "عام", "dayTimes": "4=08:00-12:00", "exemptIn": "4"}
	normSchedFields(g)
	if _, ok := g["dayTimes"]; ok {
		t.Error("general mode kept custom day times")
	}
	if err := normSchedFields(profile{"schedMode": "خاص", "workStart": "08:00", "workEnd": "14:00", "dayTimes": "الخميس ثمانية"}); err == nil {
		t.Error("bad dayTimes accepted")
	}
}

func TestTransferCrypto(t *testing.T) {
	code := newXferCode()
	if len(code) != xferCodeLen || normXferCode(strings.ToLower(fmtXferCode(code))) != code {
		t.Fatalf("code %q", code)
	}
	box, err := sealXfer(code, []byte("hello users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(box, []byte("users.json")) {
		t.Fatal("plaintext visible")
	}
	if p, err := openXfer(code, box); err != nil || string(p) != "hello users.json" {
		t.Fatalf("open: %q %v", p, err)
	}
	other := newXferCode()
	if _, err := openXfer(other, box); !errors.Is(err, errXferCode) {
		t.Fatal("wrong code opened the box")
	}
	box[len(box)-1] ^= 1
	if _, err := openXfer(code, box); err == nil {
		t.Fatal("tampered box opened")
	}
	for in, want := range map[string]string{"192.168.1.20": "http://192.168.1.20:3000/api/transfer/push",
		"http://10.0.0.5:8080/": "http://10.0.0.5:8080/api/transfer/push", "١٩٢.١٦٨.١.٢٠:٣٠٠١": "http://192.168.1.20:3001/api/transfer/push"} {
		if got, err := xferTarget(in); err != nil || got != want {
			t.Errorf("%s → %s %v", in, got, err)
		}
	}
	for _, bad := range []string{"8.8.8.8", "example.com", "", "192.168.1.20:99999"} {
		if _, err := xferTarget(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTransferUnpackRejects(t *testing.T) {
	mk := func(files map[string]string) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for n, c := range files {
			f, _ := zw.Create(n)
			f.Write([]byte(c))
		}
		zw.Close()
		return b.Bytes()
	}
	users := `{"schema":2,"users":[{"username":"o","role":"super","salt":"aa","hash":"bb","iterations":1}]}`
	files, err := unpackData(mk(map[string]string{"users.json": users, "../evil": "x", "/abs": "x", "a/../../b": "x", "reports/2026-01-01_2026-01-31.json": "{}", "audit.log": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.rel)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "reports/2026-01-01_2026-01-31.json,users.json" {
		t.Errorf("kept %v", names)
	}
	if _, err := unpackData(mk(map[string]string{"settings.json": "{}"})); err == nil {
		t.Error("no accounts accepted")
	}
}

func TestBookRoundTrip(t *testing.T) {
	in := []bookSheet{{Name: "معلومات", Rows: [][]string{{"البند", "القيمة"}, {"ملاحظة", "سطر\nثاني & <رمز>"}}},
		{Name: "الموظفين", Rows: [][]string{{"رقم الموظف", "الهاتف"}, {"001", "07701234567"}, {"", ""}, {"5", "=1+1"}}},
		{Name: "اسم/غلط:طويل جداً جداً جداً جداً جداً جداً", Rows: [][]string{{"x"}}}}
	b, err := writeBook(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := readBook(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Name != "معلومات" || out[1].Name != "الموظفين" || utf8.RuneCountInString(out[2].Name) > 31 || strings.ContainsAny(out[2].Name, "/:") {
		t.Fatalf("names: %v", []string{out[0].Name, out[1].Name, out[2].Name})
	}
	if out[0].Rows[1][1] != "سطر\nثاني & <رمز>" || out[1].Rows[1][0] != "001" || out[1].Rows[1][1] != "07701234567" || out[1].Rows[3][1] != "=1+1" {
		t.Errorf("cells: %q", out[1].Rows)
	}
	// ورقة الإجازات أول ورقة؟ لا: readSheet (رفع الإجازات) يقرا الورقة الأولى بس
	if rows, err := readXLSX(b); err != nil || rows[0][0].s != "البند" {
		t.Errorf("first sheet: %v %v", rows, err)
	}
}

func TestAutoOffSchedule(t *testing.T) {
	Configure(t.TempDir(), false)
	day := func(hm string) time.Time { return atClock(time.Date(2026, 11, 26, 0, 0, 0, 0, time.Local), hm) }
	reset := func(rules ...autoOffRule) {
		autoOff.Lock()
		autoOff.cfg = &autoOffCfg{Enabled: true, IP: "127.0.0.1", Port: 1, Delay: 5, Rules: rules}
		autoOff.lastDay, autoOff.lastRun, autoOff.events, autoOff.pending = nil, nil, nil, time.Time{}
		autoOff.started0 = day("10:00")
		autoOff.Unlock()
	}
	events := func() []string {
		autoOff.Lock()
		defer autoOff.Unlock()
		var out []string
		for _, e := range autoOff.events {
			out = append(out, e.What)
		}
		return out
	}
	// كشف كل يوم الساعة 16:00
	reset(autoOffRule{On: true, Action: "check", When: "time", Time: "16:00"})
	autoOffTick(day("15:59"))
	if len(events()) != 0 {
		t.Fatal("checked before its time")
	}
	autoOffTick(day("16:00"))
	autoOffTick(day("16:01"))
	if len(events()) != 1 || !strings.Contains(events()[0], "الكشف") && !strings.Contains(events()[0], "كشف") {
		t.Fatalf("one check at 16:00: %v", events())
	}
	// البرنامج انفتح بعد الوقت بساعتين: ما يكشف متأخر
	reset(autoOffRule{On: true, Action: "check", When: "time", Time: "16:00"})
	autoOffTick(day("18:00"))
	if len(events()) != 0 {
		t.Fatal("late check long after the time")
	}
	// أكثر من طريقة سوه: كشف كل ساعة + إطفاء مباشر الساعة 11:00، والمطفية ما تشتغل
	reset(autoOffRule{On: true, Action: "check", When: "interval", Every: 60},
		autoOffRule{On: true, Action: "off", When: "time", Time: "11:00"},
		autoOffRule{On: false, Action: "off", When: "time", Time: "10:30"})
	autoOffTick(day("10:30"))
	if len(events()) != 0 {
		t.Fatalf("fired early or disabled rule ran: %v", events())
	}
	autoOffTick(day("11:00")) // الثنتين بنفس الوقت: الإطفاء المباشر بس
	if ev := events(); len(ev) != 1 || !strings.Contains(ev[0], "إطفاء مباشر") {
		t.Fatalf("direct off at 11:00 only: %v", ev)
	}
	autoOffTick(day("12:00"))
	if ev := events(); len(ev) != 2 || !strings.Contains(ev[0], "كل ساعة") {
		t.Fatalf("hourly check at 12:00: %v", ev)
	}
	// النسخة السابقة (طريقة وحدة) تتحول لطريقة بالقائمة
	autoOff.Lock()
	autoOff.cfg = nil
	autoOff.Unlock()
	os.MkdirAll(filepath.Dir(autoOffFile()), 0o700)
	os.WriteFile(autoOffFile(), []byte(`{"enabled":true,"ip":"1.2.3.4","port":4370,"mode":"interval","time":"16:00","every":120,"delay":3}`), 0o600)
	autoOff.Lock()
	c := autoOffCfgLocked()
	autoOff.Unlock()
	if len(c.Rules) != 1 || c.Rules[0] != (autoOffRule{On: true, Action: "check", When: "interval", Time: "16:00", Every: 120}) {
		t.Errorf("migrated: %+v", c.Rules)
	}
}
