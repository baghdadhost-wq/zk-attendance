package server

// مستخدمو البرنامج وصلاحياتهم. كل مستخدم إله اسم وكلمة سر وصلاحية وحدة من ثلاث:
//
//	admin       صلاحية مطلقة: كل الأوامر حتى الخطرة والمخفية، وإدارة المستخدمين وسجل العمليات
//	supervisor  مشرف: أوامر الجهاز العادية وتعديل بيانات الموظفين
//	viewer      مشاهد: سحب البيانات والتقارير والتصدير بس
//
// كلمات السر تنحفظ مشفرة (PBKDF2-SHA256) بملف data/users.json، مو نص واضح.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	RoleAdmin      = "admin"
	RoleSupervisor = "supervisor"
	RoleViewer     = "viewer"

	pbkdf2Iter = 120000
	minPassLen = 6
)

var roleRank = map[string]int{RoleViewer: 1, RoleSupervisor: 2, RoleAdmin: 3}

var roleLabels = map[string]string{RoleAdmin: "مدير (صلاحية مطلقة)", RoleSupervisor: "مشرف", RoleViewer: "مشاهد"}

func validRole(r string) bool { return roleRank[r] > 0 }

// atLeast هل الصلاحية role تكفي لشي يحتاج need
func atLeast(role, need string) bool { return validRole(role) && roleRank[role] >= roleRank[need] }

type appUser struct {
	Username   string `json:"username"`
	Role       string `json:"role"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
	Created    string `json:"created,omitempty"`
	Updated    string `json:"updated,omitempty"`
	LastLogin  string `json:"lastLogin,omitempty"`
}

// UserStore كل مستخدمي البرنامج
type UserStore struct {
	mu         sync.Mutex
	users      []appUser
	allowSetup bool
	migrated   bool
	mod        time.Time // وقت وحجم الملف آخر مرة قريناه أو كتبناه،
	size       int64     // حتى إذا تغير من برّه (--reset-admin والبرنامج شغال) نقراه من جديد
}

func parseUsers(b []byte) ([]appUser, error) {
	var f struct {
		Users []appUser `json:"users"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	var out []appUser
	for _, u := range f.Users {
		if strings.TrimSpace(u.Username) != "" && validRole(u.Role) && u.Hash != "" && u.Iterations > 0 {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *UserStore) stampLocked() {
	if st, err := os.Stat(usersFile()); err == nil {
		s.mod, s.size = st.ModTime(), st.Size()
	}
}

// إذا الملف تغير من برنامج ثاني نقراه من جديد (وإذا تالف نبقى على اللي بالذاكرة)
func (s *UserStore) refreshLocked() {
	st, err := os.Stat(usersFile())
	if err != nil || (st.ModTime().Equal(s.mod) && st.Size() == s.size) {
		return
	}
	if b, err := os.ReadFile(usersFile()); err == nil {
		if users, err := parseUsers(b); err == nil {
			s.users = users
		}
	}
	s.mod, s.size = st.ModTime(), st.Size()
}

func usersFile() string {
	name := "users.json"
	if demoMode {
		name = "users-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func oldAuthFile() string { return filepath.Join(dataDir, "data", "auth.json") }

// حسابات وضع التجربة (بيانات وهمية بس، وتنحفظ بملف منفصل)
var demoAccounts = []map[string]string{
	{"username": "admin", "password": "123456", "role": RoleAdmin},
	{"username": "supervisor", "password": "123456", "role": RoleSupervisor},
	{"username": "viewer", "password": "123456", "role": RoleViewer},
}

// LoadUsers يقرأ المستخدمين. إذا ما أكو ملف مستخدمين وأكو كلمة سر من النسخة السابقة، تصير كلمة سر حساب admin.
func LoadUsers() (*UserStore, error) {
	s := &UserStore{}
	b, err := os.ReadFile(usersFile())
	switch {
	case err == nil:
		users, err := parseUsers(b)
		if err != nil {
			return nil, fmt.Errorf("ملف المستخدمين %s تالف (%v). رجّع نسخته users.json.bak إذا موجودة", usersFile(), err)
		}
		s.users = users
		s.stampLocked()
	case errors.Is(err, os.ErrNotExist):
		now := localISO(time.Now())
		if demoMode {
			for _, a := range demoAccounts {
				salt, hash := hashPassword(a["password"])
				s.users = append(s.users, appUser{Username: a["username"], Role: a["role"], Salt: salt, Hash: hash, Iterations: pbkdf2Iter, Created: now})
			}
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
			break
		}
		var old struct {
			Salt       string `json:"salt"`
			Hash       string `json:"hash"`
			Iterations int    `json:"iterations"`
		}
		if ob, err := os.ReadFile(oldAuthFile()); err == nil && json.Unmarshal(ob, &old) == nil && old.Hash != "" && old.Iterations > 0 {
			s.users = []appUser{{Username: "admin", Role: RoleAdmin, Salt: old.Salt, Hash: old.Hash, Iterations: old.Iterations, Created: now}}
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
			os.Rename(oldAuthFile(), oldAuthFile()+".old")
			os.Remove(sessionsFile()) // الجلسات القديمة ما بيها اسم مستخدم
			s.migrated = true
		}
	default:
		return nil, err
	}
	return s, nil
}

// Migrated صار هسه نقل كلمة السر القديمة لحساب admin
func (s *UserStore) Migrated() bool { return s.migrated }

// AllowWebSetup يسمح بإنشاء أول مدير من المتصفح (بس إذا ماكو مستخدمين، ومن نفس الحاسبة)
func (s *UserStore) AllowWebSetup() { s.mu.Lock(); s.allowSetup = true; s.mu.Unlock() }

func (s *UserStore) setupOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.allowSetup && len(s.users) == 0
}

// Count عدد المستخدمين
func (s *UserStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return len(s.users)
}

func (s *UserStore) saveLocked() error {
	f := usersFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(f); err == nil {
		os.WriteFile(f+".bak", old, 0o600)
	}
	b, err := json.MarshalIndent(map[string]any{"users": s.users}, "", " ")
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, f); err != nil {
		return err
	}
	s.stampLocked()
	return nil
}

// ---------- كلمات السر ----------

func derive(pass string, salt []byte, iter int) []byte {
	k, _ := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	return k
}

func hashPassword(pass string) (salt, hash string) {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b), hex.EncodeToString(derive(pass, b, pbkdf2Iter))
}

func (u *appUser) check(pass string) bool {
	salt, err1 := hex.DecodeString(u.Salt)
	want, err2 := hex.DecodeString(u.Hash)
	if err1 != nil || err2 != nil || u.Iterations <= 0 {
		return false
	}
	return subtle.ConstantTimeCompare(derive(pass, salt, u.Iterations), want) == 1
}

var (
	dummyOnce sync.Once
	dummyUser appUser
)

// نحسب كلمة سر وهمية إذا الاسم غلط، حتى الوقت ما يكشف هل الاسم موجود
func dummyCheck(pass string) {
	dummyOnce.Do(func() {
		salt, hash := hashPassword("unused-password")
		dummyUser = appUser{Salt: salt, Hash: hash, Iterations: pbkdf2Iter}
	})
	dummyUser.check(pass)
}

func cleanUsername(s string) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return "", badReq("اكتب اسم المستخدم")
	}
	if n > 32 {
		return "", badReq("اسم المستخدم طويل، الحد 32 حرف")
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_' && r != '-' {
			return "", badReq("اسم المستخدم يقبل حروف وأرقام و . _ - فقط، بدون فراغات")
		}
	}
	return s, nil
}

// CheckUsername يتأكد إن اسم المستخدم مقبول
func CheckUsername(name string) error {
	_, err := cleanUsername(name)
	return err
}

func checkNewPassword(p string) error {
	n := utf8.RuneCountInString(p)
	if n < minPassLen {
		return badReq(fmt.Sprintf("كلمة السر لازم تكون %d أحرف أو أكثر", minPassLen))
	}
	if n > 128 {
		return badReq("كلمة السر طويلة، الحد 128 حرف")
	}
	return nil
}

// ---------- العمليات ----------

func (s *UserStore) findLocked(name string) int {
	name = strings.TrimSpace(name)
	for i, u := range s.users {
		if strings.EqualFold(u.Username, name) {
			return i
		}
	}
	return -1
}

func (s *UserStore) adminsLocked() int {
	n := 0
	for _, u := range s.users {
		if u.Role == RoleAdmin {
			n++
		}
	}
	return n
}

func (s *UserStore) get(name string) (appUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if i := s.findLocked(name); i >= 0 {
		return s.users[i], true
	}
	return appUser{}, false
}

func (s *UserStore) authenticate(name, pass string) (appUser, bool) {
	u, ok := s.get(name)
	if !ok || pass == "" {
		dummyCheck(pass)
		return appUser{}, false
	}
	if !u.check(pass) {
		return appUser{}, false
	}
	return u, true
}

func (s *UserStore) list() []appUser {
	s.mu.Lock()
	s.refreshLocked()
	out := append([]appUser(nil), s.users...)
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if roleRank[out[i].Role] != roleRank[out[j].Role] {
			return roleRank[out[i].Role] > roleRank[out[j].Role]
		}
		return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username)
	})
	return out
}

func (s *UserStore) create(name, pass, role string) (appUser, error) {
	name, err := cleanUsername(name)
	if err != nil {
		return appUser{}, err
	}
	if !validRole(role) {
		return appUser{}, badReq("اختار الصلاحية")
	}
	if err := checkNewPassword(pass); err != nil {
		return appUser{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if s.findLocked(name) >= 0 {
		return appUser{}, badReq("اسم المستخدم «" + name + "» موجود من قبل")
	}
	salt, hash := hashPassword(pass)
	u := appUser{Username: name, Role: role, Salt: salt, Hash: hash, Iterations: pbkdf2Iter, Created: localISO(time.Now())}
	s.users = append(s.users, u)
	if err := s.saveLocked(); err != nil {
		s.users = s.users[:len(s.users)-1]
		return appUser{}, err
	}
	return u, nil
}

// أول مدير من صفحة الإعداد: ينجح بس إذا ماكو ولا مستخدم
func (s *UserStore) createFirstAdmin(name, pass string) (appUser, error) {
	s.mu.Lock()
	s.refreshLocked()
	open := s.allowSetup && len(s.users) == 0
	s.mu.Unlock()
	if !open {
		return appUser{}, apiErr{403, "حساب المدير موجود من قبل، سجّل الدخول"}
	}
	u, err := s.create(name, pass, RoleAdmin)
	if err == nil {
		s.mu.Lock()
		if len(s.users) > 1 { // طلبين بنفس اللحظة: نخلي الأول بس
			s.users = s.users[:1]
			s.saveLocked()
		}
		s.mu.Unlock()
	}
	return u, err
}

// update يغير الصلاحية أو كلمة السر (الفارغ يبقى مثل ما هو)
func (s *UserStore) update(name, role, pass string) (appUser, error) {
	if role != "" && !validRole(role) {
		return appUser{}, badReq("صلاحية غير معروفة")
	}
	if pass != "" {
		if err := checkNewPassword(pass); err != nil {
			return appUser{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	i := s.findLocked(name)
	if i < 0 {
		return appUser{}, badReq("المستخدم غير موجود")
	}
	old := s.users[i]
	u := old
	if role != "" && role != u.Role {
		if u.Role == RoleAdmin && s.adminsLocked() == 1 {
			return appUser{}, badReq("هذا آخر مدير بالبرنامج. أضف مدير ثاني قبل ما تغير صلاحيته")
		}
		u.Role = role
	}
	if pass != "" {
		u.Salt, u.Hash = hashPassword(pass)
		u.Iterations = pbkdf2Iter
	}
	u.Updated = localISO(time.Now())
	s.users[i] = u
	if err := s.saveLocked(); err != nil {
		s.users[i] = old
		return appUser{}, err
	}
	return u, nil
}

func (s *UserStore) remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	i := s.findLocked(name)
	if i < 0 {
		return badReq("المستخدم غير موجود")
	}
	if s.users[i].Role == RoleAdmin && s.adminsLocked() == 1 {
		return badReq("ما يصير تحذف آخر مدير بالبرنامج")
	}
	old := append([]appUser(nil), s.users...)
	s.users = append(s.users[:i:i], s.users[i+1:]...)
	if err := s.saveLocked(); err != nil {
		s.users = old
		return err
	}
	return nil
}

func (s *UserStore) touchLogin(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if i := s.findLocked(name); i >= 0 {
		s.users[i].LastLogin = localISO(time.Now())
		s.saveLocked()
	}
}

// EnsureAdmin ينشئ حساب مدير بهذا الاسم وكلمة السر، أو يرجّع صلاحيته مطلقة ويغير كلمة سره.
// إذا الحساب موجود بنفس كلمة السر والصلاحية ما يغير شي (حتى ما تطلع الجلسات مع كل تشغيل).
// changed = هل تغيرت كلمة السر (فلازم نطلّع جلساته)
func (s *UserStore) EnsureAdmin(name, pass string) (created, changed bool, err error) {
	name, err = cleanUsername(name)
	if err != nil {
		return false, false, err
	}
	if err := checkNewPassword(pass); err != nil {
		return false, false, err
	}
	u, ok := s.get(name)
	if !ok {
		_, err := s.create(name, pass, RoleAdmin)
		return err == nil, false, err
	}
	samePass := u.check(pass)
	if samePass && u.Role == RoleAdmin {
		return false, false, nil
	}
	p := pass
	if samePass {
		p = ""
	}
	s.mu.Lock()
	s.refreshLocked()
	if i := s.findLocked(name); i >= 0 {
		s.users[i].Role = RoleAdmin // ترقية لمدير مسموحة دائماً (ما تقلل عدد المدراء)
	}
	s.mu.Unlock()
	_, err = s.update(name, "", p) // جلساته القديمة تبطل وحدها لأن كل جلسة مربوطة بكلمة السر
	return false, !samePass, err
}

// بصمة قصيرة من كلمة السر تنحفظ ويا الجلسة: إذا تغيرت كلمة السر (من البرنامج أو من --reset-admin) تبطل الجلسة
func (u appUser) passStamp() string {
	if len(u.Hash) < 16 {
		return u.Hash
	}
	return u.Hash[:16]
}

func userInfoJSON(u appUser) map[string]any {
	return map[string]any{"username": u.Username, "role": u.Role, "roleLabel": roleLabels[u.Role],
		"created": u.Created, "updated": u.Updated, "lastLogin": u.LastLogin}
}
