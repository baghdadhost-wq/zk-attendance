package server

// إعدادات البرنامج العامة، تنحفظ بالحاسبة بملف data/settings.json حتى كل الأجهزة تحسب بنفس الشي:
// النظام العام للدوام: بداية ونهاية الدوام، أيام العطلة الأسبوعية، سماحية التأخير، وتجاهل البصمة المكررة.
// يتعدل بالصلاحية المطلقة بس (تغيير الأوقات).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type workSchedule struct {
	Start string `json:"start"` // "08:00"
	End   string `json:"end"`   // "14:00"
	Off   []int  `json:"off"`   // أيام العطلة: 0 الأحد ... 5 الجمعة، 6 السبت
	Grace int    `json:"grace"` // سماحية التأخير بالدقائق
	Dedup int    `json:"dedup"` // تجاهل البصمة المكررة خلال كم دقيقة
}

type appSettings struct {
	Schedule      workSchedule `json:"schedule"`
	ScheduleSaved bool         `json:"scheduleSaved"` // انحفظ النظام العام مرة على الأقل (قبلها كان بكل متصفح لحاله)
	ScheduleBy    string       `json:"scheduleBy,omitempty"`
	ScheduleAt    string       `json:"scheduleAt,omitempty"`
}

var (
	setMu    sync.Mutex
	setCache *appSettings
)

func defaultSettings() appSettings {
	return appSettings{Schedule: workSchedule{Start: "08:00", End: "14:00", Off: []int{5}, Grace: 10, Dedup: 3}}
}

func settingsFile() string {
	name := "settings.json"
	if demoMode {
		name = "settings-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

// getSettings الإعدادات الحالية (نسخة، فالتعديل عليها ما يأثر)
func getSettings() appSettings {
	setMu.Lock()
	defer setMu.Unlock()
	return copySettings(loadSettingsLocked())
}

func copySettings(s appSettings) appSettings {
	s.Schedule.Off = append([]int{}, s.Schedule.Off...)
	return s
}

func loadSettingsLocked() appSettings {
	if setCache != nil {
		return *setCache
	}
	s := defaultSettings()
	if b, err := os.ReadFile(settingsFile()); err == nil {
		var f appSettings
		if json.Unmarshal(b, &f) == nil {
			if ws, err := checkSchedule(f.Schedule); err == nil {
				s.Schedule = ws
				s.ScheduleSaved = f.ScheduleSaved
				s.ScheduleBy, s.ScheduleAt = f.ScheduleBy, f.ScheduleAt
			}
		}
	}
	setCache = &s
	return s
}

func saveSettingsLocked(s appSettings) error {
	f := settingsFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	var check appSettings
	if rb, err := os.ReadFile(tmp); err != nil || json.Unmarshal(rb, &check) != nil {
		os.Remove(tmp)
		return errors.New("فشل التحقق من ملف الإعدادات")
	}
	if err := os.Rename(tmp, f); err != nil {
		return err
	}
	c := copySettings(s)
	setCache = &c
	return nil
}

// ---------- التحقق من أوقات الدوام وأيام العطلة ----------

// parseHHMM يقبل "8:00" و "08:00" ويرجع الدقائق من بداية اليوم والصيغة الموحدة "08:00"
func parseHHMM(s string) (int, string, bool) {
	s = strings.TrimSpace(toASCIIDigits(s))
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) == 0 || len(h) > 2 || len(m) != 2 {
		return 0, "", false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, "", false
	}
	return hh*60 + mm, fmt.Sprintf("%02d:%02d", hh, mm), true
}

func toASCIIDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		}
		return r
	}, s)
}

// checkTimes يتأكد من وقت الدخول والخروج (دوام نهاري: الخروج بعد الدخول بنفس اليوم)
func checkTimes(start, end string) (string, string, error) {
	sm, s, ok1 := parseHHMM(start)
	em, e, ok2 := parseHHMM(end)
	if !ok1 {
		return "", "", badReq("وقت بداية الدوام غلط، اكتبه مثل 08:00")
	}
	if !ok2 {
		return "", "", badReq("وقت نهاية الدوام غلط، اكتبه مثل 14:00")
	}
	if em <= sm {
		return "", "", badReq("نهاية الدوام لازم تكون بعد بدايته بنفس اليوم")
	}
	return s, e, nil
}

func cleanOffDays(days []int) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, d := range days {
		if d < 0 || d > 6 {
			return nil, badReq("يوم عطلة غلط")
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) == 7 {
		return nil, badReq("ما يصير كل أيام الأسبوع عطلة، خلي يوم دوام واحد على الأقل")
	}
	sort.Ints(out)
	if out == nil {
		out = []int{}
	}
	return out, nil
}

func checkSchedule(ws workSchedule) (workSchedule, error) {
	s, e, err := checkTimes(ws.Start, ws.End)
	if err != nil {
		return ws, err
	}
	off, err := cleanOffDays(ws.Off)
	if err != nil {
		return ws, err
	}
	if ws.Grace < 0 || ws.Grace > 240 {
		return ws, badReq("سماحية التأخير لازم تكون بين 0 و 240 دقيقة")
	}
	if ws.Dedup < 0 || ws.Dedup > 120 {
		return ws, badReq("مدة تجاهل البصمة المكررة لازم تكون بين 0 و 120 دقيقة")
	}
	return workSchedule{Start: s, End: e, Off: off, Grace: ws.Grace, Dedup: ws.Dedup}, nil
}

// ---------- الواجهة ----------

func settingsJSON(s appSettings) map[string]any {
	return map[string]any{"schedule": s.Schedule, "scheduleSaved": s.ScheduleSaved, "scheduleBy": s.ScheduleBy, "scheduleAt": s.ScheduleAt}
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, settingsJSON(getSettings()))
}

// حفظ النظام العام للدوام (الصلاحية المطلقة)
func handleScheduleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req struct {
		Schedule workSchedule `json:"schedule"`
	}
	if err := readJSONBody(r, 4096, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	ws, err := checkSchedule(req.Schedule)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	setMu.Lock()
	s := copySettings(loadSettingsLocked())
	old := s.Schedule
	me, _ := userOf(r)
	s.Schedule, s.ScheduleSaved, s.ScheduleBy, s.ScheduleAt = ws, true, me.Name, localISO(time.Now())
	err = saveSettingsLocked(s)
	setMu.Unlock()
	audit(r, "scheduleSave", map[string]any{"old": scheduleText(old), "new": scheduleText(ws)}, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أحفظ النظام العام: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "settings": settingsJSON(s)})
}

var dayNamesAr = []string{"الأحد", "الاثنين", "الثلاثاء", "الأربعاء", "الخميس", "الجمعة", "السبت"}

func offDaysText(off []int) string {
	if len(off) == 0 {
		return "بدون عطلة"
	}
	names := make([]string, len(off))
	for i, d := range off {
		names[i] = dayNamesAr[d]
	}
	return strings.Join(names, " و")
}

func scheduleText(ws workSchedule) string {
	return fmt.Sprintf("%s-%s، العطلة: %s، السماحية %d د، المكرر %d د", ws.Start, ws.End, offDaysText(ws.Off), ws.Grace, ws.Dedup)
}
