package server

// تسجيل الدخول بصفحة عربية وجلسة محفوظة (كوكي)، بدل نافذة المتصفح الإنگليزية
// اللي ما تشتغل زين من أيقونة الشاشة الرئيسية بالآيفون.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "zk_session"
	sessionTTL    = 30 * 24 * time.Hour
)

func sessionsFile() string { return filepath.Join(dataDir, "data", "sessions.json") }

// الجلسات تنحفظ بالملف كبصمة (hash) مو كالرمز نفسه
type sessionStore struct {
	mu sync.Mutex
	m  map[string]int64
}

func newSessionStore() *sessionStore {
	s := &sessionStore{m: map[string]int64{}}
	if b, err := os.ReadFile(sessionsFile()); err == nil {
		json.Unmarshal(b, &s.m)
	}
	now := time.Now().Unix()
	for k, exp := range s.m {
		if exp < now {
			delete(s.m, k)
		}
	}
	return s
}

func tokenKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (s *sessionStore) saveLocked() {
	os.MkdirAll(filepath.Dir(sessionsFile()), 0o755)
	b, _ := json.Marshal(s.m)
	os.WriteFile(sessionsFile(), b, 0o600)
}

func (s *sessionStore) create() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.m[tokenKey(tok)] = time.Now().Add(sessionTTL).Unix()
	s.saveLocked()
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := tokenKey(tok)
	exp, ok := s.m[k]
	if !ok {
		return false
	}
	if time.Now().Unix() > exp {
		delete(s.m, k)
		s.saveLocked()
		return false
	}
	return true
}

func (s *sessionStore) remove(tok string) {
	s.mu.Lock()
	delete(s.m, tokenKey(tok))
	s.saveLocked()
	s.mu.Unlock()
}

// يوقف التخمين: 10 محاولات غلط خلال 10 دقائق من نفس الجهاز توقف الدخول منه مؤقتاً
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func (l *loginLimiter) recent(ip string) []time.Time {
	cut := time.Now().Add(-10 * time.Minute)
	var keep []time.Time
	for _, t := range l.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.fails[ip] = keep
	return keep
}

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) >= 10
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	l.fails[ip] = append(l.recent(ip), time.Now())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ملفات مسموحة بدون دخول (صفحة الدخول والأيقونات)
var publicPaths = map[string]bool{
	"/login": true, "/api/login": true, "/api/session": true, "/manifest.webmanifest": true, "/sw.js": true,
	"/icon-192.png": true, "/icon-512.png": true, "/apple-touch-icon.png": true, "/favicon.ico": true,
}

type authServer struct {
	cfg     *AuthConfig
	sess    *sessionStore
	limiter *loginLimiter
}

func newAuthServer(cfg *AuthConfig) *authServer {
	return &authServer{cfg: cfg, sess: newSessionStore(), limiter: &loginLimiter{fails: map[string][]time.Time{}}}
}

func (a *authServer) loggedIn(r *http.Request) bool {
	if c, err := r.Cookie(sessionCookie); err == nil && a.sess.valid(c.Value) {
		return true
	}
	// للسكربتات: كلمة السر بترويسة Basic (بدون ما نطلب من المتصفح نافذة)
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Basic ") {
		sum := sha256.Sum256([]byte(h))
		key := string(sum[:])
		if _, ok := a.cfg.ok.Load(key); ok {
			return true
		}
		if raw, err := base64.StdEncoding.DecodeString(h[6:]); err == nil {
			pass := string(raw)
			if i := strings.IndexByte(pass, ':'); i >= 0 {
				pass = pass[i+1:]
			}
			if a.cfg.check(pass) {
				a.cfg.ok.Store(key, true)
				return true
			}
		}
	}
	return false
}

func (a *authServer) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPaths[r.URL.Path] || a.loggedIn(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, 401, map[string]any{"error": "سجّل الدخول من جديد", "login": true})
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

func (a *authServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	ip := clientIP(r)
	if a.limiter.blocked(ip) {
		writeJSON(w, 429, map[string]any{"error": "محاولات غلط كثيرة. انتظر 10 دقائق وحاول مرة ثانية"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	readJSONBody(r, 4096, &req)
	if req.Password == "" || !a.cfg.check(req.Password) {
		a.limiter.fail(ip)
		time.Sleep(700 * time.Millisecond)
		writeJSON(w, 401, map[string]any{"error": "كلمة السر غلط"})
		return
	}
	a.limiter.reset(ip)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: a.sess.create(), Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *authServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sess.remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}
