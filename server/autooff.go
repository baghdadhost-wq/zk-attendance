package server

// الإطفاء التلقائي لجهاز البصمة. أكثر من طريقة تشتغل سوه:
//   - كشف ثم إطفاء: يكشف إذا الجهاز مشتغل، وإذا مشتغل يرسله أمر إطفاء بعد مدة.
//   - إطفاء مباشر: يرسل أمر الإطفاء أول ما يجي الوقت.
// وكل طريقة إما بوقت معيّن كل يوم أو كل فترة (مثلاً كل ساعة). وأكو زر «كشف الآن». يشتغل طول ما البرنامج مفتوح.
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
	Enabled bool          `json:"enabled"`
	IP      string        `json:"ip"`
	Port    int           `json:"port"`
	Key     uint32        `json:"key"`
	Delay   int           `json:"delay"` // دقايق بعد ما يلگاه مشتغل (للكشف)
	Rules   []autoOffRule `json:"rules"`
	// من النسخة السابقة (طريقة وحدة)
	Mode  string `json:"mode,omitempty"`
	Time  string `json:"time,omitempty"`
	Every int    `json:"every,omitempty"`
}

// autoOffRule طريقة: Action = check (كشف ثم إطفاء بعد المدة) أو off (إطفاء مباشر)،
// When = time (كل يوم بساعة Time) أو interval (كل Every دقيقة)
type autoOffRule struct {
	On     bool   `json:"on"`
	Action string `json:"action"`
	When   string `json:"when"`
	Time   string `json:"time,omitempty"`
	Every  int    `json:"every,omitempty"`
}

func (r autoOffRule) key() string {
	return fmt.Sprintf("%s|%s|%s|%d", r.Action, r.When, r.Time, r.Every)
}

func (r autoOffRule) text() string {
	a := "كشف ثم إطفاء"
	if r.Action == "off" {
		a = "إطفاء مباشر"
	}
	if r.When == "time" {
		return a + " كل يوم الساعة " + r.Time
	}
	return a + " " + everyText(r.Every)
}

func everyText(m int) string {
	switch {
	case m == 15:
		return "كل ربع ساعة"
	case m == 30:
		return "كل نص ساعة"
	case m == 60:
		return "كل ساعة"
	case m == 120:
		return "كل ساعتين"
	case m%60 == 0:
		return fmt.Sprintf("كل %d ساعات", m/60)
	}
	return fmt.Sprintf("كل %d دقيقة", m)
}

type autoOffEvent struct {
	T    string `json:"t"`
	What string `json:"what"`
	OK   bool   `json:"ok"`
}

var autoOff struct {
	sync.Mutex
	started  bool
	cfg      *autoOffCfg
	lastRun  map[string]time.Time // آخر مرة اشتغلت كل طريقة بالفترات
	lastDay  map[string]string    // آخر يوم اشتغلت بيه كل طريقة بالوقت المعيّن
	started0 time.Time
	pending  time.Time // موعد الإطفاء
	checking bool
	events   []autoOffEvent
}

func autoOffFile() string {
	name := "auto-off.json"
	if demoMode {
		name = "auto-off-demo.json"
	}
	return filepath.Join(dataDir, "data", name)
}

func defaultAutoOff() autoOffCfg {
	return autoOffCfg{Port: 4370, Delay: 5}
}

// لازم autoOff مقفول
func autoOffCfgLocked() autoOffCfg {
	if autoOff.cfg == nil {
		c := defaultAutoOff()
		if b, err := os.ReadFile(autoOffFile()); err == nil {
			json.Unmarshal(b, &c)
		}
		if c.Rules == nil && c.Mode != "" { // من النسخة السابقة: طريقة وحدة للكشف
			c.Rules = []autoOffRule{{On: true, Action: "check", When: c.Mode, Time: c.Time, Every: c.Every}}
		}
		if c.Rules == nil {
			c.Rules = []autoOffRule{}
		}
		c.Mode, c.Time, c.Every = "", "", 0
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

// autoOffDirect إطفاء مباشر: يرسل أمر الإطفاء هسه (إذا الجهاز يوصل)
func autoOffDirect(why string) {
	autoOff.Lock()
	c := autoOffCfgLocked()
	autoOff.pending = time.Time{}
	autoOff.Unlock()
	err := powerOffNow(c)
	autoOff.Lock()
	if err != nil {
		autoOffLog("إطفاء مباشر: الجهاز مطفي أو ما يوصل ("+why+")", true)
	} else {
		autoOffLog("إطفاء مباشر: انرسل أمر الإطفاء للجهاز ("+why+")", true)
	}
	autoOff.Unlock()
	detail := map[string]any{"device": c.IP, "what": why}
	if err != nil {
		detail["result"] = "مطفي أو ما يوصل"
	}
	autoOffAudit("autoOffPowerOff", detail, nil)
}

func powerOffNow(c autoOffCfg) error {
	if demoMode {
		return nil
	}
	_, err := withDevice(c.IP, c.Port, c.Key, func(z *zkClient) (map[string]any, error) {
		z.cmdNoWait(cmdPowerOff)
		return nil, nil
	})
	return err
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
	err := powerOffNow(c)
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

// autoOffTick كل نص دقيقة: كل طريقة مفعّلة تشتغل بوقتها أو بفترتها، والإطفاء المحدد إذا وصل موعده
func autoOffTick(now time.Time) {
	autoOffFire()
	autoOff.Lock()
	c := autoOffCfgLocked()
	if autoOff.lastRun == nil {
		autoOff.lastRun, autoOff.lastDay = map[string]time.Time{}, map[string]string{}
	}
	type job struct{ action, why string }
	var due []job
	if c.Enabled && c.IP != "" {
		day := now.Format("2006-01-02")
		for _, r := range c.Rules {
			if !r.On {
				continue
			}
			k := r.key()
			switch r.When {
			case "time":
				if now.Format("15:04") >= r.Time && autoOff.lastDay[k] != day && now.Sub(atClock(now, r.Time)) < 10*time.Minute {
					autoOff.lastDay[k] = day
					due = append(due, job{r.Action, r.text()})
				}
			case "interval":
				last, ok := autoOff.lastRun[k]
				if !ok {
					last = autoOff.started0
					autoOff.lastRun[k] = last
				}
				if r.Every > 0 && now.Sub(last) >= time.Duration(r.Every)*time.Minute {
					autoOff.lastRun[k] = now
					due = append(due, job{r.Action, r.text()})
				}
			}
		}
	}
	autoOff.Unlock()
	// الإطفاء المباشر أولاً؛ وإذا طريقتين بنفس الوقت ما نكرر
	did := map[string]bool{}
	for _, a := range []string{"off", "check"} {
		for _, j := range due {
			if j.action != a || did[a] {
				continue
			}
			did[a] = true
			if a == "off" {
				autoOffDirect(j.why)
			} else if !did["off"] {
				autoOffCheck(j.why)
			}
		}
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
	autoOff.started0 = time.Now() // أول مرة بالفترات بعد فترة كاملة من التشغيل
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
	out := map[string]any{"enabled": c.Enabled, "ip": c.IP, "port": c.Port, "hasKey": c.Key != 0, "rules": c.Rules,
		"delay": c.Delay, "events": autoOff.events, "checking": autoOff.checking}
	if out["events"] == nil {
		out["events"] = []autoOffEvent{}
	}
	if !autoOff.pending.IsZero() {
		out["pending"] = localISO(autoOff.pending)
	}
	// موعد كل طريقة الجاي، والأقرب
	if c.Enabled {
		now := time.Now()
		var next time.Time
		nextWhat := ""
		for _, r := range c.Rules {
			if !r.On {
				continue
			}
			var t time.Time
			if r.When == "time" {
				t = atClock(now, r.Time)
				if !t.After(now) || autoOff.lastDay[r.key()] == now.Format("2006-01-02") {
					t = atClock(now.AddDate(0, 0, 1), r.Time)
				}
			} else if r.Every > 0 {
				last, ok := autoOff.lastRun[r.key()]
				if !ok {
					last = autoOff.started0
				}
				t = last.Add(time.Duration(r.Every) * time.Minute)
			}
			if !t.IsZero() && (next.IsZero() || t.Before(next)) {
				next, nextWhat = t, r.text()
			}
		}
		if !next.IsZero() {
			out["next"], out["nextWhat"] = localISO(next), nextWhat
		}
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
		Enabled bool          `json:"enabled"`
		IP      string        `json:"ip"`
		Port    any           `json:"port"`
		Key     any           `json:"key"`
		KeySet  bool          `json:"keySet"` // false = تبقى كلمة السر القديمة
		Delay   any           `json:"delay"`
		Rules   []autoOffRule `json:"rules"`
	}
	if err := readJSONBody(r, 16<<10, &req); err != nil {
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
	delay := anyInt(req.Delay, 5)
	if delay < 0 || delay > 24*60 {
		writeErr(w, badReq("مدة الانتظار قبل الإطفاء من 0 لـ 1440 دقيقة"), "")
		return
	}
	if len(req.Rules) > 20 {
		writeErr(w, badReq("الطرق هواية (20 بالكثير)"), "")
		return
	}
	rules := []autoOffRule{}
	seen := map[string]bool{}
	for i, x := range req.Rules {
		row := fmt.Sprintf("الطريقة %d: ", i+1)
		if x.Action != "check" && x.Action != "off" {
			writeErr(w, badReq(row+"اختار كشف ثم إطفاء أو إطفاء مباشر"), "")
			return
		}
		n := autoOffRule{On: x.On, Action: x.Action, When: x.When}
		switch x.When {
		case "time":
			t, err := time.Parse("15:04", strings.TrimSpace(toASCIIDigits(x.Time)))
			if err != nil {
				writeErr(w, badReq(row+"الوقت غلط"), "")
				return
			}
			n.Time = t.Format("15:04")
		case "interval":
			if x.Every < 5 || x.Every > 24*60 {
				writeErr(w, badReq(row+"الفترة من 5 دقايق لـ 24 ساعة"), "")
				return
			}
			n.Every = x.Every
		default:
			writeErr(w, badReq(row+"اختار بوقت معيّن أو كل فترة"), "")
			return
		}
		if seen[n.key()] {
			continue
		}
		seen[n.key()] = true
		rules = append(rules, n)
	}
	autoOff.Lock()
	c := autoOffCfgLocked()
	n := autoOffCfg{Enabled: req.Enabled, IP: ip, Port: port, Key: c.Key, Delay: delay, Rules: rules}
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
		if !c.Enabled && n.Enabled { // الفترات تبدي تحسب من هسه
			autoOff.started0 = time.Now()
			autoOff.lastRun = map[string]time.Time{}
		}
	}
	autoOff.Unlock()
	var parts []string
	for _, x := range rules {
		if x.On {
			parts = append(parts, x.text())
		}
	}
	what := "مطفي"
	if n.Enabled {
		what = strings.Join(parts, "، ")
		if what == "" {
			what = "مفعّل بدون طرق"
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
