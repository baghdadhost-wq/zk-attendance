package server

// تسجيل الدخول: صفحة عربية وجلسة (كوكي) لكل مستخدم، والتحقق من الصلاحية بكل طلب.
// ما أكو دخول بدون اسم وكلمة سر. الجلسات بالذاكرة بس: كل ما ينفتح البرنامج من جديد يطلب الدخول.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "zk_session"
	sessionTTL    = 12 * time.Hour
	maxSessions   = 5000
)

func sessionsFile() string {
	name := "sessions.json"
	if demoMode {
		name = "sessions-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

// الجلسة تنخزن ببصمة (hash) الرمز مو بالرمز نفسه
type sessEntry struct {
	User  string
	Exp   int64
	Stamp string // بصمة كلمة السر وقت الدخول
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]sessEntry
}

func newSessionStore() *sessionStore {
	os.Remove(sessionsFile()) // جلسات النسخ السابقة (كانت تبقى 30 يوم)
	return &sessionStore{m: map[string]sessEntry{}}
}

func tokenKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (s *sessionStore) add(e sessEntry) string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	e.Exp = time.Now().Add(sessionTTL).Unix()
	s.mu.Lock()
	s.pruneLocked()
	s.m[tokenKey(tok)] = e
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) create(u appUser) string {
	return s.add(sessEntry{User: u.Username, Stamp: u.passStamp()})
}

// يشيل المنتهية، وإذا بعدها هواية يشيل الأقدم
func (s *sessionStore) pruneLocked() {
	if len(s.m) < maxSessions {
		return
	}
	now := time.Now().Unix()
	for k, e := range s.m {
		if e.Exp < now {
			delete(s.m, k)
		}
	}
	for len(s.m) >= maxSessions {
		oldK, oldExp := "", int64(1<<62)
		for k, e := range s.m {
			if e.Exp < oldExp {
				oldK, oldExp = k, e.Exp
			}
		}
		delete(s.m, oldK)
	}
}

func (s *sessionStore) lookup(tok string) (sessEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := tokenKey(tok)
	e, ok := s.m[k]
	if !ok {
		return sessEntry{}, false
	}
	if time.Now().Unix() > e.Exp {
		delete(s.m, k)
		return sessEntry{}, false
	}
	return e, true
}

// بعد ما المستخدم يغير كلمة سره، جلسته الحالية تتحدث وتبقى
func (s *sessionStore) rebind(tok string, u appUser) {
	s.mu.Lock()
	if e, ok := s.m[tokenKey(tok)]; ok {
		e.Stamp = u.passStamp()
		s.m[tokenKey(tok)] = e
	}
	s.mu.Unlock()
}

func (s *sessionStore) remove(tok string) {
	s.mu.Lock()
	delete(s.m, tokenKey(tok))
	s.mu.Unlock()
}

// يطلّع مستخدم من كل الأجهزة (عدا الجلسة keep إذا موجودة)
func (s *sessionStore) removeUser(user, keep string) {
	s.mu.Lock()
	keepKey := ""
	if keep != "" {
		keepKey = tokenKey(keep)
	}
	for k, e := range s.m {
		if strings.EqualFold(e.User, user) && k != keepKey {
			delete(s.m, k)
		}
	}
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

func isLoopback(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}

// ---------- المستخدم الحالي بكل طلب ----------

type ctxKey struct{}

type reqUser struct {
	Name, Role string
	EmpID      string // حساب موظف: رقم بصمته
	token      string
}

func withUser(r *http.Request, u reqUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
}

func userOf(r *http.Request) (reqUser, bool) {
	u, ok := r.Context().Value(ctxKey{}).(reqUser)
	return u, ok
}

// allowed هل المستخدم الحالي عنده هذي الصلاحية
func allowed(r *http.Request, need string) bool {
	u, ok := userOf(r)
	return ok && atLeast(u.Role, need)
}

func deny(w http.ResponseWriter, r *http.Request, what string) {
	audit(r, "denied", map[string]any{"what": what}, errors.New("ما عنده صلاحية"))
	writeJSON(w, 403, map[string]any{"error": "ما عندك صلاحية لهذا الأمر. اطلبه من صاحب صلاحية أعلى", "forbidden": true})
}

// need يلف المسار بشرط صلاحية
func need(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r, role) {
			deny(w, r, r.URL.Path)
			return
		}
		h(w, r)
	}
}

// طلبات الكتابة لازم تجي من صفحة البرنامج نفسه، مو من موقع ثاني مفتوح بنفس المتصفح
func sameOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true // سكربتات وبرامج (ما ترسل Origin)
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// ملفات مسموحة بدون دخول (صفحة الدخول والأيقونات)
var publicPaths = map[string]bool{
	"/login": true, "/api/login": true, "/api/setup": true, "/api/session": true, "/manifest.webmanifest": true, "/sw.js": true,
	"/icon-192.png": true, "/icon-512.png": true, "/apple-touch-icon.png": true, "/favicon.ico": true,
}

type authServer struct {
	store   *UserStore
	sess    *sessionStore
	limiter *loginLimiter
	basic   sync.Map // بصمة ترويسة Basic ← اسم المستخدم وبصمة كلمة سره وقت التحقق
}

type basicHit struct{ user, hash string }

func newAuthServer(store *UserStore) *authServer {
	return &authServer{store: store, sess: newSessionStore(), limiter: &loginLimiter{fails: map[string][]time.Time{}}}
}

func (a *authServer) authUser(r *http.Request) (reqUser, bool) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if e, ok := a.sess.lookup(c.Value); ok {
			if u, ok := a.store.get(e.User); ok && e.Stamp == u.passStamp() {
				return reqUser{Name: u.Username, Role: u.Role, EmpID: u.EmpID, token: c.Value}, true
			}
			a.sess.remove(c.Value) // المستخدم انحذف أو تغيرت كلمة سره
		}
	}
	// للسكربتات: اسم المستخدم وكلمة السر بترويسة Basic (بدون ما نطلب من المتصفح نافذة)
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Basic ") {
		sum := sha256.Sum256([]byte(h))
		key := string(sum[:])
		if v, ok := a.basic.Load(key); ok {
			hit := v.(basicHit)
			if u, ok := a.store.get(hit.user); ok && u.Hash == hit.hash {
				return reqUser{Name: u.Username, Role: u.Role, EmpID: u.EmpID}, true
			}
			a.basic.Delete(key)
		}
		if a.limiter.blocked(clientIP(r)) {
			return reqUser{}, false
		}
		if raw, err := base64.StdEncoding.DecodeString(h[6:]); err == nil {
			name, pass, _ := strings.Cut(string(raw), ":")
			if u, ok := a.store.authenticate(name, pass); ok {
				a.basic.Store(key, basicHit{u.Username, u.Hash})
				return reqUser{Name: u.Username, Role: u.Role, EmpID: u.EmpID}, true
			}
			a.limiter.fail(clientIP(r))
		}
	}
	return reqUser{}, false
}

func (a *authServer) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeJSON(w, 403, map[string]any{"error": "طلب مرفوض: لازم يجي من صفحة البرنامج نفسه"})
			return
		}
		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if u, ok := a.authUser(r); ok {
			next.ServeHTTP(w, withUser(r, u))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, 401, map[string]any{"error": "سجّل الدخول من جديد", "login": true})
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// بدون تسجيل دخول (تطبيق الموبايل اللي يشتغل على نفس الجهاز): المستخدم عنده كل الصلاحيات
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeJSON(w, 403, map[string]any{"error": "طلب مرفوض: لازم يجي من صفحة البرنامج نفسه"})
			return
		}
		next.ServeHTTP(w, withUser(r, reqUser{Role: RoleSuper}))
	})
}

// كوكي الجلسة ينتهي ويا سدّ المتصفح، والجلسة نفسها تنتهي ويا إعادة تشغيل البرنامج أو بعد 12 ساعة
func (a *authServer) setCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// يشيل جلسة المتصفح الحالية قبل جلسة جديدة
func (a *authServer) dropCurrent(r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		a.sess.remove(c.Value)
	}
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	readJSONBody(r, 4096, &req)
	u, ok := a.store.authenticate(req.Username, req.Password)
	if !ok {
		a.limiter.fail(ip)
		time.Sleep(700 * time.Millisecond)
		name := strings.TrimSpace(req.Username)
		if rn := []rune(name); len(rn) > 40 {
			name = string(rn[:40])
		}
		auditAs(r, name, "", "loginFailed", nil, errors.New("اسم المستخدم أو كلمة السر غلط"))
		writeJSON(w, 401, map[string]any{"error": "اسم المستخدم أو كلمة السر غلط"})
		return
	}
	a.limiter.reset(ip)
	a.dropCurrent(r)
	a.setCookie(w, a.sess.create(u))
	a.store.touchLogin(u.Username)
	auditAs(r, u.Username, u.Role, "login", nil, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "user": userInfoJSON(u)})
}

// أول تشغيل على نفس الحاسبة: إنشاء حساب الصلاحية المطلقة من المتصفح
func (a *authServer) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	if !isLoopback(clientIP(r)) {
		writeErr(w, apiErr{403, "افتح البرنامج من نفس الحاسبة حتى تنشئ حساب الصلاحية المطلقة"}, "")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	readJSONBody(r, 4096, &req)
	u, err := a.store.createFirstAdmin(req.Username, req.Password)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	a.setCookie(w, a.sess.create(u))
	a.store.touchLogin(u.Username)
	auditAs(r, u.Username, u.Role, "setup", nil, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "user": userInfoJSON(u)})
}

func (a *authServer) handleSession(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"auth": true, "demo": demoMode, "version": Version}
	if u, ok := a.authUser(r); ok {
		au, _ := a.store.get(u.Name)
		out["user"] = userInfoJSON(au)
	} else {
		out["user"] = nil
		out["setup"] = a.store.setupOpen() && isLoopback(clientIP(r)) // الأجهزة الثانية تشوف صفحة الدخول العادية
		if demoMode {
			out["demoAccounts"] = demoAccounts
		}
	}
	writeJSON(w, 200, out)
}

func (a *authServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	audit(r, "logout", nil, nil)
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sess.remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// كل مستخدم يگدر يغير كلمة سره بعد ما يكتب الحالية
func (a *authServer) handleMyPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	me, _ := userOf(r)
	ip := clientIP(r)
	if a.limiter.blocked(ip) {
		writeJSON(w, 429, map[string]any{"error": "محاولات غلط كثيرة. انتظر 10 دقائق وحاول مرة ثانية"})
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	readJSONBody(r, 4096, &req)
	if _, ok := a.store.authenticate(me.Name, req.Current); !ok {
		a.limiter.fail(ip)
		time.Sleep(700 * time.Millisecond)
		writeErr(w, badReq("كلمة السر الحالية غلط"), "")
		return
	}
	if req.New == req.Current {
		writeErr(w, badReq("كلمة السر الجديدة نفس القديمة"), "")
		return
	}
	u, err := a.store.update(me.Name, "", req.New, "")
	if err != nil {
		writeErr(w, err, "")
		return
	}
	a.sess.removeUser(me.Name, me.token) // يطلع من باقي الأجهزة، ويبقى بهذا
	a.sess.rebind(me.token, u)
	audit(r, "myPassword", nil, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "message": "تغيرت كلمة السر، وانطلعت من باقي الأجهزة"})
}

// ---------- إدارة المستخدمين ----------
// الصلاحية المطلقة تدير كل الحسابات. المدير يدير حسابات الموظفين بس (ينشئها، يغير كلمة سرها، يحذفها).

// manages هل المستخدم الحالي يگدر يدير حساب بهذي الصلاحية
func manages(me reqUser, role string) bool {
	if me.Role == RoleSuper {
		return true
	}
	return me.Role == RoleAdmin && role == RoleEmployee
}

func (a *authServer) handleUsers(w http.ResponseWriter, r *http.Request) {
	me, _ := userOf(r)
	out := []map[string]any{}
	for _, u := range a.store.list() {
		if !manages(me, u.Role) && !strings.EqualFold(u.Username, me.Name) {
			continue
		}
		m := userInfoJSON(u)
		m["me"] = strings.EqualFold(u.Username, me.Name)
		out = append(out, m)
	}
	roles := map[string]string{RoleEmployee: roleLabels[RoleEmployee]}
	if me.Role == RoleSuper {
		roles = roleLabels
	}
	writeJSON(w, 200, map[string]any{"users": out, "roles": roles})
}

func (a *authServer) handleUserSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	me, _ := userOf(r)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		EmpID    string `json:"empId"`
		Create   bool   `json:"create"`
	}
	if err := readJSONBody(r, 4096, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	var (
		u   appUser
		err error
	)
	target := strings.TrimSpace(req.Username)
	detail := map[string]any{"target": target, "newRole": req.Role, "empId": strings.TrimSpace(req.EmpID)}
	if req.Create {
		if !manages(me, req.Role) {
			deny(w, r, "userCreate:"+req.Role)
			return
		}
		u, err = a.store.create(req.Username, req.Password, req.Role, req.EmpID)
		audit(r, "userCreate", detail, err)
	} else {
		if strings.EqualFold(target, me.Name) {
			writeErr(w, badReq("حسابك أنت غيّر كلمة سره من «تغيير كلمة السر»، وصلاحيته يغيرها حساب ثاني بصلاحية مطلقة"), "")
			return
		}
		cur, ok := a.store.get(target)
		if !ok {
			writeErr(w, badReq("المستخدم غير موجود"), "")
			return
		}
		if !manages(me, cur.Role) || (req.Role != "" && !manages(me, req.Role)) {
			deny(w, r, "userUpdate:"+cur.Role)
			return
		}
		u, err = a.store.update(target, req.Role, req.Password, req.EmpID)
		detail["passwordChanged"] = req.Password != ""
		audit(r, "userUpdate", detail, err)
		if err == nil && req.Password != "" { // الصلاحية ورقم البصمة ينقرون من جديد بكل طلب، فتتطبق فوراً بدون ما يطلع
			a.sess.removeUser(u.Username, "")
		}
	}
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "user": userInfoJSON(u)})
}

func (a *authServer) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	me, _ := userOf(r)
	var req struct {
		Username string `json:"username"`
	}
	readJSONBody(r, 4096, &req)
	target := strings.TrimSpace(req.Username)
	if strings.EqualFold(target, me.Name) {
		writeErr(w, badReq("ما تگدر تحذف حسابك أنت"), "")
		return
	}
	if cur, ok := a.store.get(target); ok && !manages(me, cur.Role) {
		deny(w, r, "userDelete:"+cur.Role)
		return
	}
	err := a.store.remove(target)
	audit(r, "userDelete", map[string]any{"target": target}, err)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	a.sess.removeUser(target, "")
	writeJSON(w, 200, map[string]any{"ok": true})
}
