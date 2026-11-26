package server

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *UserStore {
	t.Helper()
	Configure(t.TempDir(), false)
	s, err := LoadUsers()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLastSuperIsProtected(t *testing.T) {
	s := newStore(t)
	if _, err := s.create("boss", "password1", RoleSuper, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.remove("boss"); err == nil {
		t.Fatal("removed the only super account")
	}
	if _, err := s.update("boss", RoleCustom, "", "", nil); err == nil {
		t.Fatal("demoted the only super account")
	}
	if _, err := s.create("second", "password2", RoleSuper, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update("boss", RoleCustom, "", "", nil); err != nil {
		t.Fatal("could not demote with another super present:", err)
	}
	if err := s.remove("second"); err == nil {
		t.Fatal("removed the last super after demotion")
	}
	// المخصص مو مطلقة: حذفه مسموح
	s.create("mgr", "password3", RoleCustom, "", nil)
	if err := s.remove("mgr"); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeAccounts(t *testing.T) {
	s := newStore(t)
	if _, err := s.create("ali", "password1", RoleEmployee, "", nil); err == nil {
		t.Fatal("employee account without fingerprint number")
	}
	u, err := s.create("ali", "password1", RoleEmployee, " ٠١٢ ", nil)
	if err != nil || u.EmpID != "012" {
		t.Fatalf("create: %+v %v", u, err)
	}
	if _, err := s.create("ali2", "password1", RoleEmployee, "12", nil); err == nil || !strings.Contains(err.Error(), "ali") {
		t.Fatalf("same employee twice (012 = 12): %v", err)
	}
	if u, _ := s.create("boss", "password1", RoleSuper, "99", nil); u.EmpID != "" {
		t.Fatal("non-employee kept an employee number")
	}
	if u, err := s.update("ali", RoleCustom, "", "", nil); err != nil || u.EmpID != "" {
		t.Fatalf("role change kept the number: %+v %v", u, err)
	}
	if _, err := s.update("ali", RoleEmployee, "", "", nil); err == nil {
		t.Fatal("became employee without a number")
	}
	if u, err := s.update("ali", RoleEmployee, "", "7", nil); err != nil || u.EmpID != "7" {
		t.Fatalf("back to employee: %+v %v", u, err)
	}
	if !atLeast(RoleSuper, RoleCustom) || atLeast(RoleCustom, RoleSuper) || atLeast(RoleEmployee, RoleCustom) || !atLeast(RoleEmployee, RoleEmployee) {
		t.Fatal("role ranking is wrong")
	}
}

// ملف مستخدمين من قبل 2.8: "admin" كان الصلاحية المطلقة، فيصير "super" وينحفظ بالصيغة الجديدة
func TestOldAdminBecomesSuper(t *testing.T) {
	dir := t.TempDir()
	Configure(dir, false)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	salt, hash := hashPassword("old-pass1")
	old, _ := json.Marshal(map[string]any{"users": []map[string]any{
		{"username": "admin", "role": "admin", "salt": salt, "hash": hash, "iterations": pbkdf2Iter},
		{"username": "sup", "role": "supervisor", "salt": salt, "hash": hash, "iterations": pbkdf2Iter},
	}})
	os.WriteFile(filepath.Join(dir, "data", "users.json"), old, 0o600)
	s, err := LoadUsers()
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := s.authenticate("admin", "old-pass1"); !ok || u.Role != RoleSuper {
		t.Fatalf("old admin: %+v", u)
	}
	if u, _ := s.get("sup"); u.Role != RoleCustom || strings.Join(u.Perms, ",") != strings.Join(legacyPerms["supervisor"], ",") {
		t.Fatalf("supervisor must become custom with the same permissions: %+v", u)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "data", "users.json"))
	if !strings.Contains(string(b), `"schema": 3`) {
		t.Fatal("not saved in the new format")
	}
	// قراءة ثانية: يبقى مخصص (ما يصير مطلقة)
	s.create("mgr", "password9", "admin", "", nil)
	again, _ := LoadUsers()
	if u, _ := again.get("mgr"); u.Role != RoleCustom || !hasPerm(u.Role, u.Perms, "users.employees") || hasPerm(u.Role, u.Perms, "sched.edit") {
		t.Fatalf("new admin preset after reload: %+v", u)
	}
}

// ملف من 2.8 لـ 2.16 (schema 2): المدير والمشرف والأساسي يصيرون «مخصص» بنفس اللي كانوا يگدرونه
func TestSchema2RolesBecomeCustom(t *testing.T) {
	dir := t.TempDir()
	Configure(dir, false)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	salt, hash := hashPassword("pass-1234")
	old, _ := json.Marshal(map[string]any{"schema": 2, "users": []map[string]any{
		{"username": "boss", "role": "super", "salt": salt, "hash": hash, "iterations": pbkdf2Iter},
		{"username": "mgr", "role": "admin", "salt": salt, "hash": hash, "iterations": pbkdf2Iter},
		{"username": "view", "role": "viewer", "salt": salt, "hash": hash, "iterations": pbkdf2Iter},
		{"username": "emp", "role": "employee", "empId": "3", "salt": salt, "hash": hash, "iterations": pbkdf2Iter},
	}})
	os.WriteFile(filepath.Join(dir, "data", "users.json"), old, 0o600)
	s, err := LoadUsers()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"boss": RoleSuper, "mgr": RoleCustom, "view": RoleCustom, "emp": RoleEmployee}
	for n, r := range want {
		if u, _ := s.get(n); u.Role != r {
			t.Fatalf("%s became %s", n, u.Role)
		}
	}
	mgr, _ := s.get("mgr")
	view, _ := s.get("view")
	if !hasPerm(mgr.Role, mgr.Perms, "ctl.clearLogs") || hasPerm(view.Role, view.Perms, "ctl.clearLogs") || !hasPerm(view.Role, view.Perms, "conn.fetch") {
		t.Fatalf("presets: %v / %v", mgr.Perms, view.Perms)
	}
	if hasPerm(mgr.Role, mgr.Perms, "danger.clearAll") || hasPerm(mgr.Role, mgr.Perms, "sched.edit") {
		t.Fatal("old admin got super-only permissions")
	}
	// تغيير الصلاحيات، والمجهولة تنشال
	u, err := s.update("view", "", "", "", []string{"tab.daily", "nope", "tab.daily", "conn.fetch"})
	if err != nil || strings.Join(u.Perms, ",") != "conn.fetch,tab.daily" {
		t.Fatalf("perms update: %v %v", u.Perms, err)
	}
	// الصلاحيات تنشال إذا صار موظف أو مطلقة
	if u, _ := s.update("view", RoleEmployee, "", "9", nil); u.Perms != nil {
		t.Fatal("employee kept permissions")
	}
}

func TestResetAll(t *testing.T) {
	s := newStore(t)
	s.create("boss", "password1", RoleSuper, "", nil)
	s.create("emp", "password1", RoleEmployee, "5", nil)
	backup, n, err := s.ResetAll()
	if err != nil || n != 2 || s.Count() != 0 {
		t.Fatalf("reset: %v %d %d", err, n, s.Count())
	}
	if b, err := os.ReadFile(backup); err != nil || !strings.Contains(string(b), "boss") {
		t.Fatalf("backup: %v", err)
	}
	s.AllowWebSetup()
	if !s.setupOpen() {
		t.Fatal("first-run setup not offered after reset")
	}
	u, err := s.createFirstAdmin("owner1", "Owner#2026x")
	if err != nil || u.Role != RoleSuper {
		t.Fatalf("first account: %+v %v", u, err)
	}
}

func TestEnsureAdmin(t *testing.T) {
	s := newStore(t)
	created, changed, err := s.EnsureAdmin("admin", "first-pass")
	if err != nil || !created || changed {
		t.Fatalf("create: %v %v %v", created, changed, err)
	}
	if created, changed, err = s.EnsureAdmin("Admin", "first-pass"); err != nil || created || changed {
		t.Fatalf("same password should change nothing: %v %v %v", created, changed, err)
	}
	if _, changed, err = s.EnsureAdmin("admin", "second-pass"); err != nil || !changed {
		t.Fatalf("new password: %v %v", changed, err)
	}
	if _, ok := s.authenticate("admin", "second-pass"); !ok {
		t.Fatal("new password does not work")
	}
	s.create("helper", "helper-pass", RoleEmployee, "3", nil)
	if _, _, err = s.EnsureAdmin("helper", "helper-pass"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.get("helper"); u.Role != RoleSuper || u.EmpID != "" {
		t.Fatalf("EnsureAdmin did not promote an existing user: %+v", u)
	}
	// يرجع يقرا من الملف نفس الشي
	again, err := LoadUsers()
	if err != nil || again.Count() != 2 {
		t.Fatalf("reload: %v %d", err, again.Count())
	}
}

func TestMigrateOldPassword(t *testing.T) {
	dir := t.TempDir()
	Configure(dir, false)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	salt := []byte("0123456789abcdef")
	old, _ := json.Marshal(map[string]any{"salt": hex.EncodeToString(salt), "hash": hex.EncodeToString(derive("old-secret", salt, 1000)), "iterations": 1000})
	os.WriteFile(filepath.Join(dir, "data", "auth.json"), old, 0o600)
	s, err := LoadUsers()
	if err != nil || !s.Migrated() {
		t.Fatalf("migrate: %v %v", err, s != nil && s.Migrated())
	}
	if u, ok := s.authenticate("admin", "old-secret"); !ok || u.Role != RoleSuper {
		t.Fatal("old password does not log in with full permissions")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "auth.json.old")); err != nil {
		t.Fatal("auth.json not kept as .old")
	}
}

func TestCorruptUsersFileStops(t *testing.T) {
	dir := t.TempDir()
	Configure(dir, false)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "data", "users.json"), []byte("{broken"), 0o600)
	if _, err := LoadUsers(); err == nil {
		t.Fatal("a broken users file must stop the program, not open first-run setup")
	}
}

func TestRolesAndActions(t *testing.T) {
	// كل أمر للجهاز إله صلاحية من القائمة (حتى تبين بالتخصيص)
	for action := range actions {
		p, ok := actionPerm[action]
		if !ok {
			t.Fatalf("%s has no permission", action)
		}
		if !permKeys[p] {
			t.Fatalf("%s needs unknown permission %s", action, p)
		}
	}
	all := allPerms()
	for a := range actions {
		if hasPerm(RoleEmployee, all, actionPerm[a]) || hasPerm("", all, actionPerm[a]) || hasPerm("admin", all, actionPerm[a]) {
			t.Fatalf("non-custom role can run %s", a)
		}
		if !hasPerm(RoleSuper, nil, actionPerm[a]) || hasPerm(RoleCustom, nil, actionPerm[a]) || !hasPerm(RoleCustom, []string{actionPerm[a]}, actionPerm[a]) {
			t.Fatalf("permission check wrong for %s", a)
		}
	}
	seen := map[string]bool{}
	for _, g := range permCatalog {
		if seen[g.Key] || len(g.Items) == 0 {
			t.Fatalf("group %s", g.Key)
		}
		seen[g.Key] = true
		for _, it := range g.Items {
			if seen["i:"+it.Key] || it.Label == "" {
				t.Fatalf("item %s", it.Key)
			}
			seen["i:"+it.Key] = true
		}
	}
	for n, ps := range legacyPerms {
		if len(cleanPerms(ps)) != len(ps) {
			t.Fatalf("preset %s has unknown permissions", n)
		}
	}
}
