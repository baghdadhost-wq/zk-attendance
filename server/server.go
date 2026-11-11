// Package server: برنامج تقارير الحضور لأجهزة بصمة ZKTeco (الخادم والواجهة).
// يستخدمه برنامج الحاسبة (cmd/zk-attendance) وتطبيق الآيفون (mobile).
package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Version رقم نسخة البرنامج
const Version = "2.2.0"

//go:embed public
var publicFS embed.FS

// Configure يحدد مجلد البيانات ووضع التجربة. يُستدعى مرة وحدة قبل NewHandler.
func Configure(dir string, demo bool) {
	dataDir = dir
	demoMode = demo
}

var connectHint string

// SetConnectHint نص يضاف لرسالة الخطأ إذا ما گدر يتصل بالجهاز (مثلاً تنبيه إذن الشبكة المحلية بالآيفون)
func SetConnectHint(s string) { connectHint = s }

// DataDir مجلد البيانات الحالي
func DataDir() string { return dataDir }

// DemoMode هل البرنامج بوضع التجربة
func DemoMode() bool { return demoMode }

// NewHandler يرجع الخادم كامل. auth == nil يعني بدون تسجيل دخول (للاستخدام على نفس الجهاز).
func NewHandler(auth *AuthConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data", handleData)
	mux.HandleFunc("/api/cmd", handleCmd)
	mux.HandleFunc("/api/profiles", handleProfiles)
	mux.HandleFunc("/api/profiles/save", handleProfileSave)
	mux.HandleFunc("/api/profiles/import", handleProfileImport)
	mux.HandleFunc("/api/import-mdb", handleImportMdb)
	var as *authServer
	if auth != nil {
		as = newAuthServer(auth)
		mux.HandleFunc("/api/login", as.handleLogin)
		mux.HandleFunc("/logout", as.handleLogout)
	}
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"auth": as != nil})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		file, ctype := "", ""
		switch r.URL.Path {
		case "/", "/index.html":
			file, ctype = "index.html", "text/html; charset=utf-8"
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
	return mux
}

// ---------- كلمة سر الدخول (تنحفظ مشفرة بـ PBKDF2، مو نص واضح) ----------

// AuthConfig كلمة سر الدخول بصيغتها المشفرة
type AuthConfig struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
	ok         sync.Map
}

func authFile() string { return filepath.Join(dataDir, "data", "auth.json") }

func derive(pass string, salt []byte, iter int) []byte {
	k, _ := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	return k
}

func (a *AuthConfig) check(pass string) bool {
	salt, err1 := hex.DecodeString(a.Salt)
	want, err2 := hex.DecodeString(a.Hash)
	if err1 != nil || err2 != nil {
		return false
	}
	return subtle.ConstantTimeCompare(derive(pass, salt, a.Iterations), want) == 1
}

// LoadAuth يقرأ كلمة السر المحفوظة، ويرجع nil إذا ما أكو
func LoadAuth() *AuthConfig {
	b, err := os.ReadFile(authFile())
	if err != nil {
		return nil
	}
	var a AuthConfig
	if json.Unmarshal(b, &a) != nil || a.Hash == "" || a.Iterations == 0 {
		return nil
	}
	return &a
}

// SetPassword يحفظ كلمة سر جديدة ويطلّع كل الأجهزة المسجلة
func SetPassword(pass string) (*AuthConfig, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	iter := 120000
	a := &AuthConfig{Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(derive(pass, salt, iter)), Iterations: iter}
	if err := os.MkdirAll(filepath.Dir(authFile()), 0o755); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(a, "", " ")
	if err := os.WriteFile(authFile(), b, 0o600); err != nil {
		return nil, err
	}
	os.Remove(sessionsFile())
	return a, nil
}
