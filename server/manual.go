package server

// البصمات اليدوية: لموظف نسى يبصم أو كان بواجب.
// تنحفظ بالبرنامج (data/manual-punches.json) مو بالجهاز، لأن ما أكو أمر يكتب بسجل الجهاز،
// وتطلع بالتقارير مكتوب يمها «يدوية» ويا السبب ومنو ضافها، وكل إضافة وحذف ينسجل بسجل العمليات.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type manualPunch struct {
	ID      string `json:"id"`
	UserID  string `json:"userId"`
	Time    string `json:"time"` // وقت محلي مثل سجل الجهاز: 2006-01-02T15:04:05
	Type    string `json:"type"` // in = دخول، out = خروج
	Reason  string `json:"reason"`
	Note    string `json:"note,omitempty"`
	By      string `json:"by"`
	Created string `json:"created"`
}

var manualMu sync.Mutex

var manualReasons = map[string]string{"forgot": "نسى يبصم", "duty": "واجب رسمي", "device": "عطل بالجهاز", "other": "سبب آخر"}

func manualFile() string {
	name := "manual-punches.json"
	if demoMode {
		name = "manual-punches-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func loadManual() ([]manualPunch, error) {
	b, err := os.ReadFile(manualFile())
	if errors.Is(err, os.ErrNotExist) {
		return []manualPunch{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Punches []manualPunch `json:"punches"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("ملف البصمات اليدوية تالف: %v", err)
	}
	if f.Punches == nil {
		f.Punches = []manualPunch{}
	}
	return f.Punches, nil
}

func saveManual(list []manualPunch) error {
	f := manualFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(f); err == nil {
		os.WriteFile(f+".bak", old, 0o600)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time < list[j].Time })
	b, err := json.MarshalIndent(map[string]any{"punches": list}, "", " ")
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

func handleManual(w http.ResponseWriter, r *http.Request) {
	manualMu.Lock()
	list, err := loadManual()
	manualMu.Unlock()
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"punches": list, "reasons": manualReasons})
}

func handleManualAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req struct {
		UserID string `json:"userId"`
		Date   string `json:"date"`
		In     string `json:"in"`
		Out    string `json:"out"`
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if err := readJSONBody(r, 8192, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	added, err := addManual(r, req.UserID, req.Date, req.In, req.Out, req.Reason, req.Note)
	detail := map[string]any{"userId": strings.TrimSpace(req.UserID), "reason": manualReasons[req.Reason], "note": strings.TrimSpace(req.Note)}
	if len(added) > 0 {
		times := make([]string, len(added))
		for i, p := range added {
			times[i] = p.Time
		}
		detail["time"] = strings.Join(times, " و ")
	} else {
		detail["time"] = strings.TrimSpace(req.Date + " " + req.In + " " + req.Out)
	}
	audit(r, "manualAdd", detail, err)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "added": added})
}

func addManual(r *http.Request, userID, date, in, out, reasonKey, note string) ([]manualPunch, error) {
	userID, note = strings.TrimSpace(userID), strings.TrimSpace(note)
	if !isDigits(userID) || len(userID) > 24 {
		return nil, badReq("اختار الموظف (رقمه أرقام فقط)")
	}
	reason, ok := manualReasons[reasonKey]
	if !ok {
		return nil, badReq("اختار السبب")
	}
	if utf8.RuneCountInString(note) > 200 {
		return nil, badReq("الملاحظة طويلة، الحد 200 حرف")
	}
	if reasonKey == "other" && note == "" {
		return nil, badReq("اكتب السبب بالملاحظة")
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), time.Local)
	if err != nil || day.Year() < 2000 || day.Year() > 2099 {
		return nil, badReq("اختار التاريخ")
	}
	parse := func(hm string) (time.Time, bool, error) {
		hm = strings.TrimSpace(hm)
		if hm == "" {
			return time.Time{}, false, nil
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+hm[:min(5, len(hm))], time.Local)
		if err != nil {
			return time.Time{}, false, badReq("الوقت غير صحيح")
		}
		return t, true, nil
	}
	tin, hasIn, err := parse(in)
	if err != nil {
		return nil, err
	}
	tout, hasOut, err := parse(out)
	if err != nil {
		return nil, err
	}
	if !hasIn && !hasOut {
		return nil, badReq("اكتب وقت الدخول أو الخروج")
	}
	if hasIn && hasOut && !tout.After(tin) {
		return nil, badReq("وقت الخروج لازم يكون بعد وقت الدخول")
	}
	// ما نقبل وقت بالمستقبل (نسمح بيوم احتياط إذا توقيت السيرفر يختلف عن توقيت الموظفين)
	limit := time.Now().Add(26 * time.Hour)
	if (hasIn && tin.After(limit)) || (hasOut && tout.After(limit)) {
		return nil, badReq("ما يصير تضيف بصمة لوقت ما وصلناه بعد")
	}
	u, _ := userOf(r)
	now := localISO(time.Now())
	var add []manualPunch
	mk := func(t time.Time, typ string) {
		b := make([]byte, 8)
		rand.Read(b)
		add = append(add, manualPunch{ID: hex.EncodeToString(b), UserID: userID, Time: localISO(t), Type: typ,
			Reason: reason, Note: note, By: u.Name, Created: now})
	}
	if hasIn {
		mk(tin, "in")
	}
	if hasOut {
		mk(tout, "out")
	}
	manualMu.Lock()
	defer manualMu.Unlock()
	list, err := loadManual()
	if err != nil {
		return nil, err
	}
	for _, a := range add {
		for _, x := range list {
			if x.UserID == a.UserID && x.Time == a.Time {
				return nil, badReq("أكو بصمة يدوية لهذا الموظف بنفس الوقت " + strings.Replace(a.Time, "T", " ", 1))
			}
		}
	}
	if err := saveManual(append(list, add...)); err != nil {
		return nil, err
	}
	return add, nil
}

func handleManualDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	readJSONBody(r, 4096, &req)
	manualMu.Lock()
	list, err := loadManual()
	var gone *manualPunch
	if err == nil {
		for i := range list {
			if list[i].ID == req.ID {
				g := list[i]
				gone = &g
				list = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		if gone == nil {
			err = badReq("البصمة غير موجودة، حدّث الصفحة")
		} else {
			err = saveManual(list)
		}
	}
	manualMu.Unlock()
	detail := map[string]any{}
	if gone != nil {
		detail = map[string]any{"userId": gone.UserID, "time": gone.Time, "reason": gone.Reason, "note": gone.Note}
	}
	audit(r, "manualDelete", detail, err)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
