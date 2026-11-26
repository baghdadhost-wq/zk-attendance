// Package server: برنامج تقارير الحضور لأجهزة بصمة ZKTeco (الخادم والواجهة).
// يستخدمه برنامج الحاسبة (cmd/zk-attendance) وتطبيق الآيفون (mobile).
package server

import (
	"embed"
	"net/http"
	"strings"
)

// Version رقم نسخة البرنامج
const Version = "2.17.0"

//go:embed public
var publicFS embed.FS

// Configure يحدد مجلد البيانات ووضع التجربة. يُستدعى مرة وحدة قبل NewHandler.
func Configure(dir string, demo bool) {
	dataDir = dir
	demoMode = demo
	setMu.Lock()
	setCache = nil
	setMu.Unlock()
}

var connectHint string

// SetConnectHint نص يضاف لرسالة الخطأ إذا ما گدر يتصل بالجهاز (مثلاً تنبيه إذن الشبكة المحلية بالآيفون)
func SetConnectHint(s string) { connectHint = s }

// DataDir مجلد البيانات الحالي
func DataDir() string { return dataDir }

// DemoMode هل البرنامج بوضع التجربة
func DemoMode() bool { return demoMode }

// NewHandler يرجع الخادم كامل.
// users == nil يعني بدون تسجيل دخول وبكل الصلاحيات (تطبيق الموبايل اللي يشتغل على نفس الجهاز بس).
func NewHandler(users *UserStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data", needP(handleData, "conn.fetch"))
	mux.HandleFunc("/api/cmd", handleCmd) // كل أمر يتأكد من صلاحيته
	mux.HandleFunc("/api/profiles", staff(handleProfiles))
	mux.HandleFunc("/api/profiles/save", needP(handleProfileSave, "emp.edit", "emp.sched"))
	mux.HandleFunc("/api/profiles/import", needP(handleProfileImport, "emp.edit"))
	mux.HandleFunc("/api/backups", needP(handleBackups, "danger.restore"))
	mux.HandleFunc("/api/audit", needP(handleAudit, "audit.view"))
	mux.HandleFunc("/api/leaves", staff(handleLeaves))
	mux.HandleFunc("/api/leaves/import", needP(handleLeavesImport, "leaves.upload"))
	mux.HandleFunc("/api/leaves/clear", needP(handleLeavesClear, "leaves.clear"))
	mux.HandleFunc("/api/manual", staff(handleManual))
	// كل مسار يحتاج صلاحية من perms.go (الصلاحية المطلقة عندها كلشي)
	mux.HandleFunc("/api/manual/add", needP(handleManualAdd, "manual.punch"))
	mux.HandleFunc("/api/manual/delete", needP(handleManualDelete, "manual.punch"))
	mux.HandleFunc("/api/manual/replace", needP(handleManualReplace, "manual.punch"))
	// الإطفاء التلقائي لجهاز البصمة
	mux.HandleFunc("/api/autooff", needP(handleAutoOff, "autooff.manage"))
	mux.HandleFunc("/api/autooff/save", needP(handleAutoOffSave, "autooff.manage"))
	mux.HandleFunc("/api/autooff/check", needP(handleAutoOffCheck, "autooff.manage"))
	mux.HandleFunc("/api/autooff/cancel", needP(handleAutoOffCancel, "autooff.manage"))
	startAutoOff()
	// نسخة الحاسبة: الإخفاء بالخلفية والتشغيل ويا الحاسبة (من نفس الحاسبة بس)
	mux.HandleFunc("/api/app", staff(handleApp))
	mux.HandleFunc("/api/app/hide", needP(handleAppHide, "app.hide"))
	mux.HandleFunc("/api/app/autostart", needP(handleAppAutoStart, "app.autostart"))
	mux.HandleFunc("/api/app/show", handleAppShow)
	// ملف Excel الشامل: كل التقارير والبيانات بأوراق، وهو نفسه النسخة الاحتياطية
	mux.HandleFunc("/api/xlsx/write", needP(handleBookWrite, "conn.export"))
	mux.HandleFunc("/api/xlsx/read", needP(handleBookRead, "conn.import"))
	mux.HandleFunc("/api/settings", staff(handleSettings))
	mux.HandleFunc("/api/share", staff(handleShare))
	mux.HandleFunc("/api/settings/schedule", needP(handleScheduleSave, "sched.edit"))
	mux.HandleFunc("/api/settings/holidays", needP(handleHolidaysSave, "manual.holidays"))
	// التقارير المحفوظة للموظفين
	mux.HandleFunc("/api/reports", needP(handleReports, "reports.list"))
	mux.HandleFunc("/api/reports/publish", needP(handleReportPublish, "reports.publish"))
	mux.HandleFunc("/api/reports/delete", needP(handleReportDelete, "reports.list"))
	mux.HandleFunc("/api/my/reports", onlyEmployee(handleMyReports))
	mux.HandleFunc("/api/my/report", onlyEmployee(handleMyReport))
	// نقل كل البيانات لجهاز ثاني: الإرسال والاستلام بصلاحية «نقل البيانات»، والدفع محمي بالرمز والتشفير
	mux.HandleFunc("/api/transfer/receive", needP(handleTransferReceive, "xfer.use"))
	mux.HandleFunc("/api/transfer/status", needP(handleTransferStatus, "xfer.use"))
	mux.HandleFunc("/api/transfer/send", needP(handleTransferSend, "xfer.use"))
	mux.HandleFunc("/api/transfer/push", handleTransferPush)
	var as *authServer
	if users != nil {
		as = newAuthServer(users)
		activeMu.Lock()
		activeAuth = as
		activeMu.Unlock()
		mux.HandleFunc("/api/login", as.handleLogin)
		mux.HandleFunc("/api/setup", as.handleSetup)
		mux.HandleFunc("/api/session", as.handleSession)
		mux.HandleFunc("/logout", as.handleLogout)
		mux.HandleFunc("/api/me/password", as.handleMyPassword)
		mux.HandleFunc("/api/users", needP(as.handleUsers, "users.employees"))
		mux.HandleFunc("/api/users/save", needP(as.handleUserSave, "users.employees"))
		mux.HandleFunc("/api/users/delete", needP(as.handleUserDelete, "users.employees"))
	} else {
		mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"auth": false, "demo": demoMode, "version": Version,
				"user": map[string]any{"username": "", "role": RoleSuper, "roleLabel": roleLabels[RoleSuper]}})
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		file, ctype := "", ""
		me, _ := userOf(r)
		switch r.URL.Path {
		case "/", "/index.html":
			if me.Role == RoleEmployee { // الموظف يشوف تقريره بس
				http.Redirect(w, r, "/me", http.StatusFound)
				return
			}
			file, ctype = "index.html", "text/html; charset=utf-8"
		case "/me":
			if me.Role != RoleEmployee {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			file, ctype = "me.html", "text/html; charset=utf-8"
		case "/login":
			if as == nil {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			file, ctype = "login.html", "text/html; charset=utf-8"
		case "/manifest.webmanifest":
			file, ctype = "manifest.webmanifest", "application/manifest+json"
		case "/sw.js":
			file, ctype = "sw.js", "text/javascript; charset=utf-8"
		case "/icon-192.png", "/favicon.ico":
			file, ctype = "icon-192.png", "image/png"
		case "/icon-512.png", "/apple-touch-icon.png":
			file, ctype = r.URL.Path[1:], "image/png"
		default:
			http.NotFound(w, r)
			return
		}
		b, err := publicFS.ReadFile("public/" + file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		if strings.HasSuffix(file, ".png") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Write(b)
	})
	if as != nil {
		return as.middleware(mux)
	}
	return localOnly(mux)
}
