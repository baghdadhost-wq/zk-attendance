package server

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestLastAdminIsProtected(t *testing.T) {
	s := newStore(t)
	if _, err := s.create("boss", "password1", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.remove("boss"); err == nil {
		t.Fatal("removed the only admin")
	}
	if _, err := s.update("boss", RoleViewer, ""); err == nil {
		t.Fatal("demoted the only admin")
	}
	if _, err := s.create("second", "password2", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update("boss", RoleSupervisor, ""); err != nil {
		t.Fatal("could not demote with another admin present:", err)
	}
	if err := s.remove("second"); err == nil {
		t.Fatal("removed the last admin after demotion")
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
	s.create("helper", "helper-pass", RoleViewer)
	if _, _, err = s.EnsureAdmin("helper", "helper-pass"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.get("helper"); u.Role != RoleAdmin {
		t.Fatal("EnsureAdmin did not promote an existing user")
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
	if u, ok := s.authenticate("admin", "old-secret"); !ok || u.Role != RoleAdmin {
		t.Fatal("old password does not log in as admin")
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
	for _, a := range []string{"powerOff", "clearLogs", "clearAllData", "deleteUser", "writeOption", "addUser", "setPin", "restoreUsers"} {
		if actionNeeds(a) != RoleAdmin {
			t.Fatalf("%s must be admin only", a)
		}
	}
	if atLeast("", RoleViewer) || atLeast("root", RoleViewer) || !atLeast(RoleAdmin, RoleSupervisor) || atLeast(RoleViewer, RoleSupervisor) {
		t.Fatal("role ranking is wrong")
	}
}
