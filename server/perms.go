package server

// صلاحيات الحساب «المخصص»: كل قائمة بالبرنامج وكل شي داخلها يتفعّل أو ينطفي لوحده.
// الصلاحية المطلقة عندها كلشي، والموظف يشوف تقريره بس، والمخصص عنده بس اللي ينفعّله إله.
// الخادم يتأكد من الصلاحية بكل طلب (مو بس الصفحة تخفي الأزرار).

import (
	"net/http"
	"sort"
	"strings"
)

type permItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Danger bool   `json:"danger,omitempty"`
}

type permGroup struct {
	Key   string     `json:"key"`
	Label string     `json:"label"`
	Items []permItem `json:"items"`
}

// بنفس ترتيب البرنامج
var permCatalog = []permGroup{
	{"app", "البرنامج على هذي الحاسبة", []permItem{
		{"app.hide", "إخفاء البرنامج بالخلفية", false},
		{"app.autostart", "التشغيل التلقائي ويا الحاسبة", false},
	}},
	{"conn", "الاتصال بالجهاز", []permItem{
		{"conn.fetch", "سحب البيانات من الجهاز", false},
		{"conn.export", "تصدير Excel شامل", false},
		{"conn.import", "استيراد النسخة الشاملة", true},
	}},
	{"ctl", "التحكم بالجهاز", []permItem{
		{"ctl.info", "معلومات الجهاز ووقته وتحديثه", false},
		{"ctl.syncTime", "مزامنة وقت الجهاز مع الحاسبة", true},
		{"ctl.door", "فتح الباب", false},
		{"ctl.voice", "تشغيل الصوت", false},
		{"ctl.lcd", "رسالة على الشاشة", false},
		{"ctl.disable", "إيقاف وتفعيل البصمة", false},
		{"ctl.restart", "إعادة تشغيل الجهاز", false},
		{"ctl.powerOff", "إطفاء الجهاز", true},
		{"ctl.clearLogs", "مسح سجل البصمات (بعد حفظ نسخة)", true},
	}},
	{"scan", "فحص أمان الجهاز", []permItem{
		{"scan.run", "فحص أمان الجهاز", false},
	}},
	{"danger", "الأوامر الخطرة والمخفية", []permItem{
		{"danger.addUser", "إضافة موظف للجهاز", false},
		{"danger.pin", "الرقم السري للموظف بالجهاز", false},
		{"danger.fingers", "حذف بصمات أصابع موظف", true},
		{"danger.deleteUser", "حذف موظف من الجهاز", true},
		{"danger.restore", "استرجاع الموظفين من نسخة احتياطية", false},
		{"danger.devAdmins", "مدراء الجهاز (تعيين وإلغاء وتعديل)", true},
		{"danger.setTime", "ضبط وقت الجهاز على وقت محدد", true},
		{"danger.options", "إعدادات الجهاز المخفية", true},
		{"danger.commKey", "كلمة سر اتصال الجهاز", true},
		{"danger.clearOplog", "مسح سجل عمليات الجهاز", true},
		{"danger.clearAccess", "تصفير إعدادات الأبواب", true},
		{"danger.clearAll", "مسح كل بيانات الجهاز", true},
	}},
	{"autooff", "الإطفاء التلقائي لجهاز البصمة", []permItem{
		{"autooff.manage", "عرض وتعديل الإطفاء التلقائي و«كشف الآن»", true},
	}},
	{"manual", "البصمات اليدوية والعطل الرسمية", []permItem{
		{"manual.punch", "إضافة وحذف البصمات اليدوية", true},
		{"manual.holidays", "إضافة وحذف العطل الرسمية", true},
	}},
	{"users", "مستخدمو البرنامج", []permItem{
		{"users.employees", "حسابات الموظفين (إضافة وتغيير كلمة السر وحذف)", false},
	}},
	{"reports", "التقارير المحفوظة للموظفين", []permItem{
		{"reports.publish", "حفظ التقرير للموظفين", false},
		{"reports.list", "عرض وحذف التقارير المحفوظة", false},
	}},
	{"xfer", "نقل البيانات لجهاز ثاني", []permItem{
		{"xfer.use", "استلام وإرسال كل البيانات (تنستبدل بيانات الجهاز المستلم)", true},
	}},
	{"audit", "سجل العمليات", []permItem{
		{"audit.view", "عرض سجل العمليات", false},
	}},
	{"sched", "النظام العام للدوام", []permItem{
		{"sched.edit", "تعديل وحفظ النظام العام", true},
	}},
	{"tabs", "التقارير", []permItem{
		{"tab.daily", "التقرير اليومي", false},
		{"tab.summary", "ملخص الموظفين", false},
		{"tab.raw", "سجل البصمات", false},
		{"tab.person", "تقرير موظف", false},
		{"tab.print", "طباعة / PDF", false},
	}},
	{"emps", "الموظفون", []permItem{
		{"tab.users", "عرض قائمة الموظفين", false},
		{"emp.edit", "تعديل الاسم والبطاقة والبيانات الإضافية", false},
		{"emp.sched", "دوام الموظف و«مشمول بالبصمة»", true},
	}},
	{"leaves", "التصفية والإجازات", []permItem{
		{"tab.leaves", "عرض التصفية والإجازات", false},
		{"leaves.upload", "رفع تقرير الإجازات", false},
		{"leaves.clear", "مسح بيانات الإجازات", true},
	}},
}

var permKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, g := range permCatalog {
		for _, it := range g.Items {
			m[it.Key] = true
		}
	}
	return m
}()

func allPerms() []string {
	out := []string{}
	for _, g := range permCatalog {
		for _, it := range g.Items {
			out = append(out, it.Key)
		}
	}
	return out
}

// cleanPerms يشيل المكرر وأي اسم مو معروف، ويرتبهن
func cleanPerms(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if permKeys[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// الصلاحيات القديمة (قبل 2.17) تصير «مخصص» بنفس اللي كانت تگدره
var legacyPerms = func() map[string][]string {
	viewer := []string{"app.hide", "conn.fetch", "conn.export", "ctl.info", "ctl.door", "ctl.voice", "ctl.lcd", "ctl.disable",
		"ctl.restart", "ctl.powerOff", "scan.run", "tab.daily", "tab.summary", "tab.raw", "tab.person", "tab.print", "tab.users", "tab.leaves"}
	sup := append(append([]string{}, viewer...), "conn.import", "emp.edit")
	admin := append(append([]string{}, sup...), "danger.addUser", "danger.pin", "danger.fingers", "danger.deleteUser", "danger.restore",
		"ctl.clearLogs", "leaves.upload", "leaves.clear", "reports.publish", "reports.list", "audit.view", "users.employees")
	return map[string][]string{"viewer": cleanPerms(viewer), "supervisor": cleanPerms(sup), "admin": cleanPerms(admin)}
}()

var legacyLabels = map[string]string{"admin": "مدير", "supervisor": "مشرف", "viewer": "أساسي"}

func hasPerm(role string, perms []string, p string) bool {
	if role == RoleSuper {
		return true
	}
	if role != RoleCustom {
		return false
	}
	for _, x := range perms {
		if x == p {
			return true
		}
	}
	return false
}

// can هل المستخدم الحالي عنده هذي الصلاحية
func can(r *http.Request, p string) bool {
	u, ok := userOf(r)
	return ok && hasPerm(u.Role, u.Perms, p)
}

// canAny عنده وحدة منهن على الأقل
func canAny(r *http.Request, ps ...string) bool {
	for _, p := range ps {
		if can(r, p) {
			return true
		}
	}
	return false
}

// needP يلف المسار بشرط صلاحية وحدة من هذني
func needP(h http.HandlerFunc, ps ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !canAny(r, ps...) {
			deny(w, r, r.URL.Path)
			return
		}
		h(w, r)
	}
}

// staff أي حساب بالبرنامج عدا الموظف (قراءة الأشياء العامة: الإعدادات وأسماء الموظفين)
func staff(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := userOf(r); !ok || (u.Role != RoleSuper && u.Role != RoleCustom) {
			deny(w, r, r.URL.Path)
			return
		}
		h(w, r)
	}
}

// الصلاحية اللي يحتاجها كل أمر للجهاز. أي أمر مو مذكور هنا للصلاحية المطلقة بس.
var actionPerm = map[string]string{
	"info": "ctl.info", "getTime": "ctl.info", "refresh": "ctl.info", "syncTime": "ctl.syncTime",
	"unlock": "ctl.door", "testVoice": "ctl.voice", "lcdWrite": "ctl.lcd", "lcdClear": "ctl.lcd",
	"disable": "ctl.disable", "enable": "ctl.disable", "restart": "ctl.restart", "powerOff": "ctl.powerOff",
	"clearLogs": "ctl.clearLogs", "deviceSecurityCheck": "scan.run",
	"updateUser": "emp.edit",
	"addUser":    "danger.addUser", "setPin": "danger.pin", "deleteFingers": "danger.fingers", "deleteUser": "danger.deleteUser",
	"restoreUsers": "danger.restore", "setPrivilege": "danger.devAdmins", "clearAdmins": "danger.devAdmins",
	"setTime": "danger.setTime", "readOption": "danger.options", "writeOption": "danger.options", "setDeviceCommKey": "danger.commKey",
	"clearOplog": "danger.clearOplog", "clearAccess": "danger.clearAccess", "clearAllData": "danger.clearAll",
}

func canAction(r *http.Request, action string) bool {
	if p, ok := actionPerm[action]; ok {
		return can(r, p)
	}
	return allowed(r, RoleSuper)
}
