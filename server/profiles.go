package server

// البيانات الإضافية للموظفين (الهاتف، القسم...) تنحفظ بالحاسبة بملف data/employees.json
// نفس صيغة النسخة السابقة، فتگدر تنقل الملف بينهم.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

var profileFields = []string{"fullName", "department", "title", "gender", "mobile", "officePhone", "homePhone",
	"birthday", "hireDate", "address", "nationalId", "notes"}

type profile = map[string]string

var profMu sync.Mutex

func profilesFile() string {
	name := "employees.json"
	if demoMode {
		name = "employees-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func loadProfiles() map[string]profile {
	all := map[string]profile{}
	b, err := os.ReadFile(profilesFile())
	if err == nil {
		json.Unmarshal(b, &all)
	}
	if all == nil {
		all = map[string]profile{}
	}
	return all
}

func saveProfiles(all map[string]profile) error {
	f := profilesFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(f); err == nil {
		os.WriteFile(f+".bak", old, 0o644)
	}
	b, err := json.MarshalIndent(all, "", " ")
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	var check map[string]profile
	if rb, err := os.ReadFile(tmp); err != nil || json.Unmarshal(rb, &check) != nil {
		return errors.New("فشل التحقق من ملف البيانات")
	}
	return os.Rename(tmp, f)
}

func cleanProfile(p map[string]any) profile {
	out := profile{}
	for _, k := range profileFields {
		v, ok := anyStr(p[k])
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		limit := 200
		if k == "notes" {
			limit = 1000
		}
		if r := []rune(v); len(r) > limit {
			v = string(r[:limit])
		}
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// mode: merge = القيم الجديدة تكتب فوق القديمة | fill = تملأ الحقول الفارغة بس
func importProfiles(incoming map[string]map[string]any, mode string) (map[string]any, error) {
	profMu.Lock()
	defer profMu.Unlock()
	all := loadProfiles()
	added, updated, unchanged := 0, 0, 0
	for rawID, p := range incoming {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		add := cleanProfile(p)
		if len(add) == 0 {
			continue
		}
		before, existed := all[id]
		next := profile{}
		for k, v := range before {
			next[k] = v
		}
		for k, v := range add {
			if mode == "merge" || next[k] == "" {
				next[k] = v
			}
		}
		switch {
		case !existed:
			added++
		case !reflect.DeepEqual(before, next):
			updated++
		default:
			unchanged++
		}
		all[id] = next
	}
	if err := saveProfiles(all); err != nil {
		return nil, err
	}
	return map[string]any{"added": added, "updated": updated, "unchanged": unchanged, "profiles": all}, nil
}

func handleProfiles(w http.ResponseWriter, r *http.Request) {
	profMu.Lock()
	all := loadProfiles()
	profMu.Unlock()
	writeJSON(w, 200, map[string]any{"profiles": all, "fields": profileFields})
}

func handleProfileSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  any            `json:"userId"`
		Profile map[string]any `json:"profile"`
	}
	if err := readJSONBody(r, 1<<17, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	id, _ := anyStr(req.UserID)
	id = strings.TrimSpace(id)
	if id == "" {
		writeErr(w, badReq("رقم الموظف مفقود"), "")
		return
	}
	profMu.Lock()
	defer profMu.Unlock()
	all := loadProfiles()
	clean := cleanProfile(req.Profile)
	if len(clean) > 0 {
		all[id] = clean
	} else {
		delete(all, id)
	}
	if err := saveProfiles(all); err != nil {
		writeErr(w, err, "ما گدرت أحفظ البيانات: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "profile": clean})
}

func handleProfileImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profiles map[string]map[string]any `json:"profiles"`
		Mode     string                    `json:"mode"`
	}
	if err := readJSONBody(r, 20<<20, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	mode := "fill"
	if req.Mode == "merge" {
		mode = "merge"
	}
	res, err := importProfiles(req.Profiles, mode)
	if err != nil {
		writeErr(w, err, "ما گدرت أستورد البيانات: ")
		return
	}
	writeJSON(w, 200, res)
}

func handleImportMdb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<30))
	if err != nil {
		writeErr(w, badReq("ما گدرت أستلم الملف"), "")
		return
	}
	if len(buf) < 4096 {
		writeErr(w, badReq("الملف فارغ أو مو قاعدة بيانات"), "")
		return
	}
	profiles, err := profilesFromAtt2000(buf)
	if err != nil {
		writeErr(w, apiErr{500, err.Error()}, "")
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": profiles, "count": len(profiles)})
}
