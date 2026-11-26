package server

// خصائص نسخة الحاسبة (وندوز): التشغيل التلقائي ويا تشغيل الحاسبة، وإخفاء البرنامج بالخلفية.
// البرنامج نفسه (cmd) يعطينا الدوال؛ بالموبايل وباقي الأنظمة ما أكو، فما تطلع بالواجهة.
// كلها تشتغل بس من نفس الحاسبة (127.0.0.1)، مو من الأجهزة الثانية.

import (
	"net/http"
	"sync"
)

type Desktop struct {
	Hide         func() error
	Show         func()
	AutoStart    func() bool
	SetAutoStart func(on bool) error
}

var desk struct {
	sync.Mutex
	d      *Desktop
	hidden bool
}

func SetDesktop(d *Desktop) {
	desk.Lock()
	desk.d = d
	desk.Unlock()
}

// SetHidden البرنامج انفتح مخفي (التشغيل التلقائي)
func SetHidden(h bool) {
	desk.Lock()
	desk.hidden = h
	desk.Unlock()
}

func localDesktop(r *http.Request) *Desktop {
	desk.Lock()
	defer desk.Unlock()
	if desk.d == nil || !isLoopback(clientIP(r)) {
		return nil
	}
	return desk.d
}

// GET /api/app
func handleApp(w http.ResponseWriter, r *http.Request) {
	d := localDesktop(r)
	if d == nil {
		writeJSON(w, 200, map[string]any{"desktop": false})
		return
	}
	writeJSON(w, 200, map[string]any{"desktop": true, "autostart": d.AutoStart()})
}

// POST /api/app/hide — يخفي نافذة البرنامج ويبقى يشتغل بالخلفية
func handleAppHide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	d := localDesktop(r)
	if d == nil {
		writeErr(w, apiErr{403, "الإخفاء من نفس الحاسبة اللي شغّالة عليها البرنامج بس"}, "")
		return
	}
	err := d.Hide()
	if err == nil {
		SetHidden(true)
	}
	audit(r, "appHide", nil, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أخفي البرنامج: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /api/app/show — من النسخة الثانية لمن تفتح البرنامج وهو شغّال (بدون دخول، من نفس الحاسبة بس)
func handleAppShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	d := localDesktop(r)
	if d == nil {
		writeErr(w, apiErr{403, "من نفس الحاسبة بس"}, "")
		return
	}
	d.Show()
	SetHidden(false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /api/app/autostart {on}
func handleAppAutoStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	d := localDesktop(r)
	if d == nil {
		writeErr(w, apiErr{403, "هذا الإعداد من نفس الحاسبة اللي شغّالة عليها البرنامج بس"}, "")
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := readJSONBody(r, 256, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	err := d.SetAutoStart(req.On)
	what := "مطفي"
	if req.On {
		what = "يشتغل ويا تشغيل الحاسبة"
	}
	audit(r, "appAutoStart", map[string]any{"what": what}, err)
	if err != nil {
		writeErr(w, err, "ما گدرت أغيّر التشغيل التلقائي: ")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "autostart": d.AutoStart()})
}
