// Package server: برنامج تقارير الحضور لأجهزة بصمة ZKTeco (الخادم والواجهة).
// يستخدمه برنامج الحاسبة (cmd/zk-attendance) وتطبيق الآيفون (mobile).
package server

import (
	"embed"
	"net/http"
	"strings"
)

// Version رقم نسخة البرنامج
const Version = "2.8.0"

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
	mux.HandleFunc("/api/data", need(RoleViewer, handleData))
	mux.HandleFunc("/api/cmd", handleCmd) // كل أمر يتأكد من صلاحيته
	mux.HandleFunc("/api/profiles", need(RoleViewer, handleProfiles))
	mux.HandleFunc("/api/profiles/save", need(RoleSupervisor, handleProfileSave))
	mux.HandleFunc("/api/profiles/import", need(RoleSupervisor, handleProfileImport))
	mux.HandleFunc("/api/import-mdb", need(RoleSupervisor, handleImportMdb))
	mux.HandleFunc("/api/backups", need(RoleAdmin, handleBackups))
	mux.HandleFunc("/api/audit", need(RoleAdmin, handleAudit))
	mux.HandleFunc("/api/leaves", need(RoleViewer, handleLeaves))
	mux.HandleFunc("/api/leaves/import", need(RoleAdmin, handleLeavesImport))
	mux.HandleFunc("/api/leaves/clear", need(RoleAdmin, handleLeavesClear))
	mux.HandleFunc("/api/manual", need(RoleViewer, handleManual))
	// البصمات اليدوية والنظام العام للدوام = تغيير الأوقات: للصلاحية المطلقة بس
	mux.HandleFunc("/api/manual/add", need(RoleSuper, handleManualAdd))
	mux.HandleFunc("/api/manual/delete", need(RoleSuper, handleManualDelete))
	mux.HandleFunc("/api/settings", need(RoleViewer, handleSettings))
	mux.HandleFunc("/api/settings/schedule", need(RoleSuper, handleScheduleSave))
	// التقارير المحفوظة للموظفين
	mux.HandleFunc("/api/reports", need(RoleAdmin, handleReports))
	mux.HandleFunc("/api/reports/publish", need(RoleAdmin, handleReportPublish))
	mux.HandleFunc("/api/reports/delete", need(RoleAdmin, handleReportDelete))
	mux.HandleFunc("/api/my/reports", onlyEmployee(handleMyReports))
	mux.HandleFunc("/api/my/report", onlyEmployee(handleMyReport))
	var as *authServer
	if users != nil {
		as = newAuthServer(users)
		mux.HandleFunc("/api/login", as.handleLogin)
		mux.HandleFunc("/api/setup", as.handleSetup)
		mux.HandleFunc("/api/session", as.handleSession)
		mux.HandleFunc("/logout", as.handleLogout)
		mux.HandleFunc("/api/me/password", as.handleMyPassword)
		mux.HandleFunc("/api/users", need(RoleAdmin, as.handleUsers))
		mux.HandleFunc("/api/users/save", need(RoleAdmin, as.handleUserSave))
		mux.HandleFunc("/api/users/delete", need(RoleAdmin, as.handleUserDelete))
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
