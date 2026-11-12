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
	RoleSuper      = "super"      // صلاحية مطلقة بدون قيد
	RoleAdmin      = "admin"      // مدير: كل شي عدا تغيير الأوقات وخصائص الجهاز، ويدير حسابات الموظفين بس
	RoleSupervisor = "supervisor" // مشرف
	RoleViewer     = "viewer"     // أساسي
	RoleEmployee   = "employee"   // موظف: يشوف تقريره المحفوظ بس

	usersSchema = 2 // من 2.8: "admin" صار مدير محدود، والمطلقة اسمها "super"

	pbkdf2Iter = 120000
	minPassLen = 6
)

// الموظف ترتيبه صفر: ما يوصل لأي شي بالبرنامج غير تقريره
var roleRank = map[string]int{RoleEmployee: 0, RoleViewer: 1, RoleSupervisor: 2, RoleAdmin: 3, RoleSuper: 4}

var roleLabels = map[string]string{RoleSuper: "صلاحية مطلقة", RoleAdmin: "مدير", RoleSupervisor: "مشرف", RoleViewer: "أساسي", RoleEmployee: "موظف"}

func validRole(r string) bool { _, ok := roleRank[r]; return ok }

// atLeast هل الصلاحية role تكفي لشي يحتاج need
func atLeast(role, need string) bool {
	return validRole(role) && validRole(need) && roleRank[role] >= roleRank[need]
}

type appUser struct {
	Username   string `json:"username"`
	Role       string `json:"role"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
	EmpID      string `json:"empId,omitempty"` // حساب موظف: رقم بصمته
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

// parseUsers يقرا الملف. ملف من قبل 2.8: كان "admin" هو الصلاحية المطلقة، فيصير "super" (old = لازم ينحفظ بالصيغة الجديدة)
func parseUsers(b []byte) (users []appUser, old bool, err error) {
	var f struct {
		Schema int       `json:"schema"`
		Users  []appUser `json:"users"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false, err
	}
	old = f.Schema < usersSchema
	for _, u := range f.Users {
		if old && u.Role == RoleAdmin {
			u.Role = RoleSuper
		}
		if u.Role == RoleEmployee && strings.TrimSpace(u.EmpID) == "" {
			continue
		}
		if strings.TrimSpace(u.Username) != "" && validRole(u.Role) && u.Hash != "" && u.Iterations > 0 {
			out := u
			users = append(users, out)
		}
	}
	return users, old, nil
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
		if users, _, err := parseUsers(b); err == nil {
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
	{"username": "owner", "password": "123456", "role": RoleSuper},
	{"username": "admin", "password": "123456", "role": RoleAdmin},
	{"username": "supervisor", "password": "123456", "role": RoleSupervisor},
	{"username": "viewer", "password": "123456", "role": RoleViewer},
	{"username": "employee", "password": "123456", "role": RoleEmployee, "empId": "1"},
}

// LoadUsers يقرأ المستخدمين. إذا ما أكو ملف مستخدمين وأكو كلمة سر من النسخة السابقة، تصير كلمة سر حساب admin.
func LoadUsers() (*UserStore, error) {
	s := &UserStore{}
	b, err := os.ReadFile(usersFile())
	switch {
	case err == nil:
		users, old, err := parseUsers(b)
		if err != nil {
			return nil, fmt.Errorf("ملف المستخدمين %s تالف (%v). رجّع نسخته users.json.bak إذا موجودة", usersFile(), err)
		}
		s.users = users
		s.stampLocked()
		if old && demoMode { // حسابات التجربة القديمة: نسويها من جديد بالصلاحيات الجديدة
			s.users = nil
			now := localISO(time.Now())
			for _, a := range demoAccounts {
				salt, hash := hashPassword(a["password"])
				s.users = append(s.users, appUser{Username: a["username"], Role: a["role"], EmpID: a["empId"], Salt: salt, Hash: hash, Iterations: pbkdf2Iter, Created: now})
			}
		}
		if old {
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
		}
	case errors.Is(err, os.ErrNotExist):
		now := localISO(time.Now())
		if demoMode {
			for _, a := range demoAccounts {
				salt, hash := hashPassword(a["password"])
				s.users = append(s.users, appUser{Username: a["username"], Role: a["role"], EmpID: a["empId"], Salt: salt, Hash: hash, Iterations: pbkdf2Iter, Created: now})
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
			s.users = []appUser{{Username: "admin", Role: RoleSuper, Salt: old.Salt, Hash: old.Hash, Iterations: old.Iterations, Created: now}}
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

// AllowWebSetup يسمح بإنشاء أول حساب (صلاحية مطلقة) من المتصفح (بس إذا ماكو مستخدمين، ومن نفس الحاسبة)
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
	b, err := json.MarshalIndent(map[string]any{"schema": usersSchema, "users": s.users}, "", " ")
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

// عدد حسابات الصلاحية المطلقة (لازم يبقى واحد على الأقل)
func (s *UserStore) supersLocked() int {
	n := 0
	for _, u := range s.users {
		if u.Role == RoleSuper {
			n++
		}
	}
	return n
}

// cleanEmpID رقم بصمة الموظف لحسابه: حروف وأرقام بس
func cleanEmpID(v string) (string, error) {
	v = strings.TrimSpace(toASCIIDigits(v))
	if v == "" {
		return "", badReq("اكتب رقم بصمة الموظف")
	}
	if utf8.RuneCountInString(v) > 24 {
		return "", badReq("رقم البصمة طويل")
	}
	for _, r := range v {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", badReq("رقم البصمة يقبل أرقام وحروف بس")
		}
	}
	return v, nil
}

// nid رقم البصمة بدون أصفار بالبداية (حتى 012 و 12 يكونون نفس الموظف)
func nid(v string) string {
	v = strings.TrimLeft(strings.TrimSpace(v), "0")
	if v == "" {
		return "0"
	}
	return v
}

func (s *UserStore) empTakenLocked(empID, except string) string {
	for _, u := range s.users {
		if u.Role == RoleEmployee && nid(u.EmpID) == nid(empID) && !strings.EqualFold(u.Username, except) {
			return u.Username
		}
	}
	return ""
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

func (s *UserStore) create(name, pass, role, empID string) (appUser, error) {
	name, err := cleanUsername(name)
	if err != nil {
		return appUser{}, err
	}
	if !validRole(role) {
		return appUser{}, badReq("اختار الصلاحية")
	}
	if role == RoleEmployee {
		if empID, err = cleanEmpID(empID); err != nil {
			return appUser{}, err
		}
	} else {
		empID = ""
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
	if role == RoleEmployee {
		if other := s.empTakenLocked(empID, ""); other != "" {
			return appUser{}, badReq("الموظف رقم " + empID + " عنده حساب من قبل: " + other)
		}
	}
	salt, hash := hashPassword(pass)
	u := appUser{Username: name, Role: role, EmpID: empID, Salt: salt, Hash: hash, Iterations: pbkdf2Iter, Created: localISO(time.Now())}
	s.users = append(s.users, u)
	if err := s.saveLocked(); err != nil {
		s.users = s.users[:len(s.users)-1]
		return appUser{}, err
	}
	return u, nil
}

// أول حساب (صلاحية مطلقة) من صفحة الإعداد: ينجح بس إذا ماكو ولا مستخدم
func (s *UserStore) createFirstAdmin(name, pass string) (appUser, error) {
	s.mu.Lock()
	s.refreshLocked()
	open := s.allowSetup && len(s.users) == 0
	s.mu.Unlock()
	if !open {
		return appUser{}, apiErr{403, "حساب الصلاحية المطلقة موجود من قبل، سجّل الدخول"}
	}
	u, err := s.create(name, pass, RoleSuper, "")
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

// update يغير الصلاحية أو كلمة السر أو رقم بصمة حساب الموظف (الفارغ يبقى مثل ما هو)
func (s *UserStore) update(name, role, pass, empID string) (appUser, error) {
	if role != "" && !validRole(role) {
		return appUser{}, badReq("صلاحية غير معروفة")
	}
	if empID != "" {
		var err error
		if empID, err = cleanEmpID(empID); err != nil {
			return appUser{}, err
		}
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
		if u.Role == RoleSuper && s.supersLocked() == 1 {
			return appUser{}, badReq("هذا آخر حساب بصلاحية مطلقة. أضف حساب ثاني بصلاحية مطلقة قبل ما تغير صلاحيته")
		}
		u.Role = role
	}
	if u.Role == RoleEmployee {
		if empID != "" {
			u.EmpID = empID
		}
		if u.EmpID == "" {
			return appUser{}, badReq("حساب الموظف يحتاج رقم بصمته")
		}
		if other := s.empTakenLocked(u.EmpID, u.Username); other != "" {
			return appUser{}, badReq("الموظف رقم " + u.EmpID + " عنده حساب من قبل: " + other)
		}
	} else {
		u.EmpID = ""
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
	if s.users[i].Role == RoleSuper && s.supersLocked() == 1 {
		return badReq("ما يصير تحذف آخر حساب بصلاحية مطلقة")
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

// EnsureAdmin ينشئ حساب بصلاحية مطلقة بهذا الاسم وكلمة السر، أو يرجّع صلاحيته مطلقة ويغير كلمة سره.
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
		_, err := s.create(name, pass, RoleSuper, "")
		return err == nil, false, err
	}
	samePass := u.check(pass)
	if samePass && u.Role == RoleSuper {
		return false, false, nil
	}
	p := pass
	if samePass {
		p = ""
	}
	s.mu.Lock()
	s.refreshLocked()
	if i := s.findLocked(name); i >= 0 {
		s.users[i].Role = RoleSuper // الترقية للمطلقة مسموحة دائماً (ما تقلل عددهم)
		s.users[i].EmpID = ""
	}
	s.mu.Unlock()
	_, err = s.update(name, "", p, "") // جلساته القديمة تبطل وحدها لأن كل جلسة مربوطة بكلمة السر
	return false, !samePass, err
}

// بصمة قصيرة من كلمة السر تنحفظ ويا الجلسة: إذا تغيرت كلمة السر (من البرنامج أو من --reset-admin) تبطل الجلسة
func (u appUser) passStamp() string {
	if len(u.Hash) < 16 {
		return u.Hash
	}
	return u.Hash[:16]
}

// ResetAll يحذف كل حسابات البرنامج (بعد ما يحفظ نسخة من ملفهم بمجلد backups).
// بعدها أول دخول يطلب إنشاء حساب الصلاحية المطلقة.
func (s *UserStore) ResetAll() (backup string, n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	n = len(s.users)
	if b, rerr := os.ReadFile(usersFile()); rerr == nil {
		dir := filepath.Join(dataDir, "backups")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", 0, err
		}
		backup = filepath.Join(dir, "program-users_"+time.Now().Format("2006-01-02_15-04-05")+".json")
		if err := os.WriteFile(backup, b, 0o600); err != nil {
			return "", 0, fmt.Errorf("ما گدرت أحفظ نسخة من المستخدمين، فما حذفت شي: %v", err)
		}
	}
	old := s.users
	s.users = nil
	if err := s.saveLocked(); err != nil {
		s.users = old
		return "", 0, err
	}
	return backup, n, nil
}

func userInfoJSON(u appUser) map[string]any {
	return map[string]any{"username": u.Username, "role": u.Role, "roleLabel": roleLabels[u.Role], "empId": u.EmpID,
		"created": u.Created, "updated": u.Updated, "lastLogin": u.LastLogin}
}
