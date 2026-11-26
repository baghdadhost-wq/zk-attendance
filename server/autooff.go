package server

// الإطفاء التلقائي لجهاز البصمة: البرنامج يكشف إذا الجهاز مشتغل، وإذا مشتغل يرسله أمر إطفاء بعد مدة.
// الكشف يدوي (زر)، أو بوقت معيّن كل يوم، أو كل فترة (مثلاً كل ساعة). يشتغل طول ما البرنامج مفتوح.
// الإعداد للصلاحية المطلقة بس، ويتسجل بسجل العمليات. كلمة سر الاتصال تنحفظ بالبرنامج وما ترجع للمتصفح.
// انتبه: الجهاز المطفي يحتاج أحد يشغّله بإيده.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type autoOffCfg struct {
	Enabled bool   `json:"enabled"`
	IP      string `json:"ip"`
	Port    int    `json:"port"`
	Key     uint32 `json:"key"`
	Mode    string `json:"mode"`  // time = بوقت معيّن كل يوم ، interval = كل فترة
	Time    string `json:"time"`  // 15:04
	Every   int    `json:"every"` // دقايق
	Delay   int    `json:"delay"` // دقايق بعد ما يلگاه مشتغل
}

type autoOffEvent struct {
	T    string `json:"t"`
	What string `json:"what"`
	OK   bool   `json:"ok"`
}

var autoOff struct {
	sync.Mutex
	started   bool
	cfg       *autoOffCfg
	lastCheck time.Time // آخر كشف تلقائي (للفترات)
	lastDay   string    // آخر يوم انكشف بيه بالوقت المعيّن
	pending   time.Time // موعد الإطفاء
	checking  bool
	events    []autoOffEvent
}

func autoOffFile() string {
	name := "auto-off.json"
	if demoMode {
		name = "auto-off-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func defaultAutoOff() autoOffCfg {
	return autoOffCfg{Port: 4370, Mode: "time", Time: "16:00", Every: 60, Delay: 5}
}

// لازم autoOff مقفول
func autoOffCfgLocked() autoOffCfg {
	if autoOff.cfg == nil {
		c := defaultAutoOff()
		if b, err := os.ReadFile(autoOffFile()); err == nil {
			json.Unmarshal(b, &c)
		}
		autoOff.cfg = &c
	}
	return *autoOff.cfg
}

func autoOffLog(what string, ok bool) {
	autoOff.events = append([]autoOffEvent{{localISO(time.Now()), what, ok}}, autoOff.events...)
	if len(autoOff.events) > 30 {
		autoOff.events = autoOff.events[:30]
	}
}

func systemRequest() *http.Request {
	return &http.Request{RemoteAddr: "127.0.0.1:0", Header: http.Header{}}
}

func autoOffAudit(action string, detail map[string]any, err error) {
	auditAs(systemRequest(), "البرنامج (تلقائي)", "", action, detail, err)
}

// deviceOnline يحاول يتصل بالجهاز
func deviceOnline(c autoOffCfg) error {
	if demoMode {
		return nil
	}
	_, err := withDevice(c.IP, c.Port, c.Key, func(z *zkClient) (map[string]any, error) { return nil, nil })
	return err
}

// autoOffCheck الكشف: إذا مشتغل يحدد موعد الإطفاء (أو يطفيه هسه إذا المدة صفر)
func autoOffCheck(why string) (string, error) {
	autoOff.Lock()
	c := autoOffCfgLocked()
	if c.IP == "" {
		autoOff.Unlock()
		return "", badReq("اكتب IP جهاز البصمة واحفظ الإعداد أول")
	}
	if autoOff.checking {
		autoOff.Unlock()
		return "", badReq("الكشف شغال هسه، انتظر شوية")
	}
	autoOff.checking = true
	autoOff.Unlock()

	err := deviceOnline(c)

	autoOff.Lock()
	defer autoOff.Unlock()
	autoOff.checking = false
	detail := map[string]any{"device": c.IP, "what": why}
	if err != nil {
		msg := "الجهاز مطفي أو ما يوصل (" + why + ")"
		autoOffLog(msg, true)
		detail["result"] = "مطفي أو ما يوصل"
		autoOffAudit("autoOffCheck", detail, nil)
		return msg, nil
	}
	if !autoOff.pending.IsZero() {
		msg := "الجهاز مشتغل، والإطفاء محدد بـ " + autoOff.pending.Format("15:04")
		autoOffLog(msg+" ("+why+")", true)
		return msg, nil
	}
	autoOff.pending = time.Now().Add(time.Duration(c.Delay) * time.Minute)
	msg := fmt.Sprintf("الجهاز مشتغل، راح ينطفي الساعة %s (بعد %d دقيقة)", autoOff.pending.Format("15:04"), c.Delay)
	if c.Delay == 0 {
		msg = "الجهاز مشتغل، راح ينطفي هسه"
		go autoOffFire()
	}
	autoOffLog(msg+" ("+why+")", true)
	detail["result"] = "مشتغل"
	detail["off"] = autoOff.pending.Format("15:04")
	autoOffAudit("autoOffCheck", detail, nil)
	return msg, nil
}

// autoOffFire يرسل أمر الإطفاء إذا وصل موعده
func autoOffFire() {
	autoOff.Lock()
	if autoOff.pending.IsZero() || time.Now().Before(autoOff.pending) {
		autoOff.Unlock()
		return
	}
	autoOff.pending = time.Time{}
	c := autoOffCfgLocked()
	autoOff.Unlock()
	var err error
	if !demoMode {
		_, err = withDevice(c.IP, c.Port, c.Key, func(z *zkClient) (map[string]any, error) {
			z.cmdNoWait(cmdPowerOff)
			return nil, nil
		})
	}
	autoOff.Lock()
	if err != nil {
		autoOffLog("ما گدرت أرسل أمر الإطفاء: "+err.Error(), false)
	} else {
		autoOffLog("انرسل أمر الإطفاء للجهاز", true)
	}
	autoOff.Unlock()
	autoOffAudit("autoOffPowerOff", map[string]any{"device": c.IP}, err)
	log.Printf("[إطفاء تلقائي] %s: %v", c.IP, err)
}

// autoOffTick كل نص دقيقة: الكشف بوقته أو بفترته، والإطفاء إذا وصل موعده
func autoOffTick(now time.Time) {
	autoOffFire()
	autoOff.Lock()
	c := autoOffCfgLocked()
	due := ""
	if c.Enabled && c.IP != "" {
		switch c.Mode {
		case "time":
			day := now.Format("2006-01-02")
			if now.Format("15:04") >= c.Time && autoOff.lastDay != day && now.Sub(atClock(now, c.Time)) < 10*time.Minute {
				autoOff.lastDay, due = day, "الكشف اليومي "+c.Time
			}
		case "interval":
			if c.Every > 0 && now.Sub(autoOff.lastCheck) >= time.Duration(c.Every)*time.Minute {
				autoOff.lastCheck, due = now, fmt.Sprintf("الكشف كل %d دقيقة", c.Every)
			}
		}
	}
	autoOff.Unlock()
	if due != "" {
		autoOffCheck(due)
	}
}

func atClock(now time.Time, hm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", now.Format("2006-01-02")+" "+hm, now.Location())
	if err != nil {
		return now
	}
	return t
}

func startAutoOff() {
	autoOff.Lock()
	if autoOff.started {
		autoOff.Unlock()
		return
	}
	autoOff.started = true
	autoOff.lastCheck = time.Now() // أول كشف بالفترات بعد فترة كاملة من التشغيل
	autoOff.Unlock()
	go func() {
		for now := range time.Tick(30 * time.Second) {
			autoOffTick(now)
		}
	}()
}

func autoOffJSON() map[string]any {
	autoOff.Lock()
	defer autoOff.Unlock()
	c := autoOffCfgLocked()
	out := map[string]any{"enabled": c.Enabled, "ip": c.IP, "port": c.Port, "hasKey": c.Key != 0, "mode": c.Mode, "time": c.Time,
		"every": c.Every, "delay": c.Delay, "events": autoOff.events, "checking": autoOff.checking}
	if out["events"] == nil {
		out["events"] = []autoOffEvent{}
	}
	if !autoOff.pending.IsZero() {
		out["pending"] = localISO(autoOff.pending)
	}
	if c.Enabled && c.Mode == "interval" && c.Every > 0 {
		out["next"] = localISO(autoOff.lastCheck.Add(time.Duration(c.Every) * time.Minute))
	}
	return out
}

// GET /api/autooff
func handleAutoOff(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, autoOffJSON())
}

// POST /api/autooff/save
func handleAutoOffSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req struct {
		Enabled bool   `json:"enabled"`
		IP      string `json:"ip"`
		Port    any    `json:"port"`
		Key     any    `json:"key"`
		KeySet  bool   `json:"keySet"` // false = تبقى كلمة السر القديمة
		Mode    string `json:"mode"`
		Time    string `json:"time"`
		Every   any    `json:"every"`
		Delay   any    `json:"delay"`
	}
	if err := readJSONBody(r, 4096, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	ip := strings.TrimSpace(req.IP)
	if ip != "" && !validIP.MatchString(ip) {
		writeErr(w, badReq("عنوان IP غير صحيح"), "")
		return
	}
	if req.Enabled && ip == "" {
		writeErr(w, badReq("اكتب IP جهاز البصمة"), "")
		return
	}
	port := anyInt(req.Port, 4370)
	if port < 1 || port > 65535 {
		writeErr(w, badReq("المنفذ غلط"), "")
		return
	}
	if req.Mode != "time" && req.Mode != "interval" {
		writeErr(w, badReq("اختار طريقة الكشف"), "")
		return
	}
	hm := ""
	if req.Mode == "time" {
		t, err := time.Parse("15:04", strings.TrimSpace(toASCIIDigits(req.Time)))
		if err != nil {
			writeErr(w, badReq("وقت الكشف غلط"), "")
			return
		}
		hm = t.Format("15:04")
	}
	every, delay := anyInt(req.Every, 60), anyInt(req.Delay, 5)
	if req.Mode == "interval" && (every < 5 || every > 24*60) {
		writeErr(w, badReq("الفترة بين كل كشف من 5 دقايق لـ 24 ساعة"), "")
		return
	}
	if delay < 0 || delay > 24*60 {
		writeErr(w, badReq("مدة الانتظار قبل الإطفاء من 0 لـ 1440 دقيقة"), "")
		return
	}
	autoOff.Lock()
	c := autoOffCfgLocked()
	n := autoOffCfg{Enabled: req.Enabled, IP: ip, Port: port, Key: c.Key, Mode: req.Mode, Time: hm, Every: every, Delay: delay}
	if n.Time == "" {
		n.Time = c.Time
	}
	if req.KeySet {
		n.Key = commKey(req.Key)
	}
	b, _ := json.MarshalIndent(n, "", "  ")
	err := os.MkdirAll(filepath.Dir(autoOffFile()), 0o700)
	if err == nil {
		err = writeFileAtomic(autoOffFile(), b)
	}
	if err == nil {
		autoOff.cfg = &n
		if n.Mode == "interval" && (c.Mode != "interval" || c.Every != n.Every || !c.Enabled) {
			autoOff.lastCheck = time.Now()
		}
		if n.Mode == "time" && n.Time != c.Time {
			autoOff.lastDay = ""
		}
	}
	autoOff.Unlock()
	what := "مطفي"
	if n.Enabled {
		if n.Mode == "time" {
			what = "كل يوم الساعة " + n.Time
		} else {
			what = fmt.Sprintf("كل %d دقيقة", n.Every)
		}
	}
	audit(r, "autoOffSave", map[string]any{"device": n.IP, "what": what, "delay": fmt.Sprintf("%d دقيقة", n.Delay)}, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أحفظ: ")
		return
	}
	writeJSON(w, 200, autoOffJSON())
}

// POST /api/autooff/check — كشف يدوي
func handleAutoOffCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	msg, err := autoOffCheck("كشف يدوي")
	if err != nil {
		writeErr(w, err, "")
		return
	}
	out := autoOffJSON()
	out["message"] = msg
	writeJSON(w, 200, out)
}

// POST /api/autooff/cancel — يلغي الإطفاء المحدد
func handleAutoOffCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	autoOff.Lock()
	had := !autoOff.pending.IsZero()
	autoOff.pending = time.Time{}
	if had {
		autoOffLog("انلغى الإطفاء المحدد", true)
	}
	autoOff.Unlock()
	if had {
		audit(r, "autoOffCancel", nil, nil)
	}
	writeJSON(w, 200, autoOffJSON())
}

func writeFileAtomic(f string, b []byte) error {
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}
