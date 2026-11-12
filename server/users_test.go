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
	if _, err := s.create("boss", "password1", RoleSuper, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.remove("boss"); err == nil {
		t.Fatal("removed the only super account")
	}
	if _, err := s.update("boss", RoleAdmin, "", ""); err == nil {
		t.Fatal("demoted the only super account")
	}
	if _, err := s.create("second", "password2", RoleSuper, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update("boss", RoleSupervisor, "", ""); err != nil {
		t.Fatal("could not demote with another super present:", err)
	}
	if err := s.remove("second"); err == nil {
		t.Fatal("removed the last super after demotion")
	}
	// المدير (admin) مو مطلقة: حذفه مسموح
	s.create("mgr", "password3", RoleAdmin, "")
	if err := s.remove("mgr"); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeAccounts(t *testing.T) {
	s := newStore(t)
	if _, err := s.create("ali", "password1", RoleEmployee, ""); err == nil {
		t.Fatal("employee account without fingerprint number")
	}
	u, err := s.create("ali", "password1", RoleEmployee, " ٠١٢ ")
	if err != nil || u.EmpID != "012" {
		t.Fatalf("create: %+v %v", u, err)
	}
	if _, err := s.create("ali2", "password1", RoleEmployee, "12"); err == nil || !strings.Contains(err.Error(), "ali") {
		t.Fatalf("same employee twice (012 = 12): %v", err)
	}
	if u, _ := s.create("boss", "password1", RoleSuper, "99"); u.EmpID != "" {
		t.Fatal("non-employee kept an employee number")
	}
	if u, err := s.update("ali", RoleViewer, "", ""); err != nil || u.EmpID != "" {
		t.Fatalf("role change kept the number: %+v %v", u, err)
	}
	if _, err := s.update("ali", RoleEmployee, "", ""); err == nil {
		t.Fatal("became employee without a number")
	}
	if u, err := s.update("ali", RoleEmployee, "", "7"); err != nil || u.EmpID != "7" {
		t.Fatalf("back to employee: %+v %v", u, err)
	}
	if !atLeast(RoleSuper, RoleAdmin) || atLeast(RoleAdmin, RoleSuper) || atLeast(RoleEmployee, RoleViewer) || !atLeast(RoleEmployee, RoleEmployee) {
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
	if u, _ := s.get("sup"); u.Role != RoleSupervisor {
		t.Fatal("supervisor changed")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "data", "users.json"))
	if !strings.Contains(string(b), `"schema": 2`) {
		t.Fatal("not saved in the new format")
	}
	// قراءة ثانية: "admin" الجديد يبقى مدير (ما يصير مطلقة مرة ثانية)
	s.create("mgr", "password9", RoleAdmin, "")
	again, _ := LoadUsers()
	if u, _ := again.get("mgr"); u.Role != RoleAdmin {
		t.Fatalf("new admin became %s after reload", u.Role)
	}
}

func TestResetAll(t *testing.T) {
	s := newStore(t)
	s.create("boss", "password1", RoleSuper, "")
	s.create("emp", "password1", RoleEmployee, "5")
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
	s.create("helper", "helper-pass", RoleEmployee, "3")
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
	for action := range actions {
		need := actionNeeds(action)
		if !validRole(need) {
			t.Fatalf("%s has no role", action)
		}
	}
	// وقت الجهاز وخصائصه والمسح الكامل ومدراء الجهاز: للصلاحية المطلقة بس
	for _, a := range []string{"setTime", "syncTime", "writeOption", "readOption", "setDeviceCommKey", "clearAllData",
		"clearAdmins", "clearAccess", "clearOplog", "setPrivilege"} {
		if actionNeeds(a) != RoleSuper {
			t.Fatalf("%s must be super only", a)
		}
	}
	// المدير: موظفين الجهاز (مو مدراءه) ومسح سجل البصمات
	for _, a := range []string{"addUser", "deleteUser", "deleteFingers", "setPin", "clearLogs", "restoreUsers"} {
		if actionNeeds(a) != RoleAdmin {
			t.Fatalf("%s should need admin", a)
		}
	}
	if actionNeeds("updateUser") != RoleSupervisor {
		t.Fatal("updateUser must need a supervisor")
	}
	for _, a := range []string{"info", "getTime", "refresh", "unlock", "testVoice", "lcdWrite", "lcdClear",
		"disable", "enable", "restart", "powerOff", "deviceSecurityCheck"} {
		if actionNeeds(a) != RoleViewer {
			t.Fatalf("%s should be a basic command", a)
		}
	}
	// أي أمر جديد ما مذكور بالجدول يصير للصلاحية المطلقة تلقائياً
	if actionNeeds("someFutureCommand") != RoleSuper {
		t.Fatal("unknown commands must default to super")
	}
	// الموظف ما يوصل لولا أمر
	for a := range actions {
		if atLeast(RoleEmployee, actionNeeds(a)) {
			t.Fatalf("employee can run %s", a)
		}
	}
	if atLeast("", RoleViewer) || atLeast("root", RoleViewer) || !atLeast(RoleAdmin, RoleSupervisor) || atLeast(RoleViewer, RoleSupervisor) || atLeast("", RoleEmployee) {
		t.Fatal("role ranking is wrong")
	}
}
