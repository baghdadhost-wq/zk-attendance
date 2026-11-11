package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	demoMode bool
	dataDir  string
	devMu    sync.Mutex // الجهاز يقبل اتصال واحد، فننفذ الطلبات بالدور
	validIP  = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)
)

const isoFmt = "2006-01-02T15:04:05"

func localISO(t time.Time) string { return t.Format(isoFmt) }

type apiErr struct {
	code int
	msg  string
}

func (e apiErr) Error() string { return e.msg }

func badReq(msg string) error { return apiErr{400, msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error, prefix string) {
	var ae apiErr
	if errors.As(err, &ae) {
		writeJSON(w, ae.code, map[string]any{"error": ae.msg})
		return
	}
	writeJSON(w, 500, map[string]any{"error": prefix + err.Error()})
}

// يقرأ رقم سواء وصل كنص أو كرقم
func anyInt(v any, def int) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

func anyStr(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}
	return "", false
}

func readJSONBody(r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return badReq("طلب غير صالح")
	}
	return nil
}

func withDevice(ip string, port int, key uint32, fn func(c *zkClient) (map[string]any, error)) (map[string]any, error) {
	devMu.Lock()
	defer devMu.Unlock()
	c, err := dialZK(ip, port, key, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.close()
	return fn(c)
}

// ---------- تحويل البيانات للواجهة ----------

func hasPin(u zkUser) bool {
	for _, b := range u.Password {
		if b != 0 {
			return true
		}
	}
	return false
}

func userJSON(u zkUser) map[string]any {
	return map[string]any{"uid": u.UID, "userId": u.UserID, "name": u.Name, "cardno": u.Card, "role": u.Role, "hasPassword": hasPin(u)}
}

func sizesJSON(s *zkSizes) any {
	if s == nil {
		return nil
	}
	m := map[string]any{"users": s.Users, "fingers": s.Fingers, "records": s.Records, "cards": s.Cards,
		"fingersCap": s.FingersCap, "usersCap": s.UsersCap, "recordsCap": s.RecordsCap,
		"fingersAv": s.FingersAv, "usersAv": s.UsersAv, "recordsAv": s.RecordsAv}
	if s.HasFaces {
		m["faces"], m["facesCap"] = s.Faces, s.FacesCap
	}
	return m
}

func logsJSON(logs []zkLog) []map[string]any {
	out := make([]map[string]any, len(logs))
	for i, l := range logs {
		out[i] = map[string]any{"userId": l.UserID, "time": localISO(l.Time)}
	}
	return out
}

func usersJSON(users []zkUser) []map[string]any {
	out := make([]map[string]any, len(users))
	for i, u := range users {
		out[i] = userJSON(u)
	}
	return out
}

func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ---------- النسخ الاحتياطية ----------

func saveJSONFile(v any, tag string) (string, error) {
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("2006-01-02_15-04-05.000")
	file := filepath.Join(dir, fmt.Sprintf("%s_%s.json", tag, stamp))
	for i := 2; ; i++ { // اسم فريد حتى ما تنكتب نسخة فوق نسخة
		if _, err := os.Stat(file); os.IsNotExist(err) {
			break
		}
		file = filepath.Join(dir, fmt.Sprintf("%s_%s_%d.json", tag, stamp, i))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(file, b, 0o600); err != nil { // بيها أرقام الموظفين السرية، فتنقرا من البرنامج بس
		return "", err
	}
	return file, nil
}

// الموظفين بصيغتهم الخام (فيها الرقم السري)، تبقى بالحاسبة بس حتى نگدر نرجعهم للجهاز
func usersRawJSON(users []zkUser) []map[string]any {
	bk := make([]map[string]any, len(users))
	for i, x := range users {
		bk[i] = map[string]any{"uid": x.UID, "userId": x.UserID, "name": x.Name, "raw": hex.EncodeToString(x.Raw)}
	}
	return bk
}

func backupUsers(users []zkUser, tag string) (string, error) {
	return saveJSONFile(map[string]any{"users": usersRawJSON(users)}, tag)
}

func writeLogsBackup(sizes *zkSizes, users []zkUser, logs []zkLog, tag string) (string, error) {
	return writeFullBackup(sizes, usersJSON(users), logs, tag)
}

// نسخة فيها السجل كامل، ونتأكد إن عدد البصمات بالملف مطابق قبل أي مسح
func writeFullBackup(sizes *zkSizes, users []map[string]any, logs []zkLog, tag string) (string, error) {
	file, err := saveJSONFile(map[string]any{"info": sizesJSON(sizes), "users": users, "logs": logsJSON(logs)}, tag)
	if err != nil {
		return "", err
	}
	var check struct {
		Logs []any `json:"logs"`
	}
	b, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(b, &check) != nil || len(check.Logs) != len(logs) {
		return "", errors.New("فشل التحقق من ملف النسخة الاحتياطية")
	}
	return file, nil
}

// ---------- الأوامر ----------

type actionFn func(c *zkClient, p map[string]any) (map[string]any, error)

func msg(s string) map[string]any { return map[string]any{"message": s} }

var actions = map[string]actionFn{
	"info": func(c *zkClient, p map[string]any) (map[string]any, error) {
		out := map[string]any{}
		if d, err := c.cmd(cmdGetVersion, nil); err == nil {
			out["firmware"] = trimSpace(string(cstrBytes(d)))
		} else {
			out["firmware"] = nil
		}
		opts := [][2]string{{"serial", "~SerialNumber"}, {"platform", "~Platform"}, {"deviceName", "~DeviceName"},
			{"mac", "MAC"}, {"faceVersion", "ZKFaceVersion"}, {"fpVersion", "~ZKFPVersion"},
			{"ip", "IPAddress"}, {"netmask", "NetMask"}, {"gateway", "GATEIPAddress"}}
		for _, o := range opts {
			out[o[0]] = strPtr(c.readOption(o[1]))
		}
		if d, err := c.cmd(cmdGetPinWidth, []byte(" P")); err == nil && len(d) > 0 {
			out["pinWidth"] = int(d[0])
		} else {
			out["pinWidth"] = nil
		}
		if s, err := c.readSizes(); err == nil {
			out["sizes"] = sizesJSON(s)
		} else {
			out["sizes"] = nil
		}
		if t, err := c.readTime(); err == nil {
			out["deviceTime"] = localISO(t)
		} else {
			out["deviceTime"] = nil
		}
		out["pcTime"] = localISO(time.Now())
		return out, nil
	},
	"getTime": func(c *zkClient, p map[string]any) (map[string]any, error) {
		t, err := c.readTime()
		if err != nil {
			return nil, err
		}
		return map[string]any{"deviceTime": localISO(t), "pcTime": localISO(time.Now())}, nil
	},
	"syncTime": func(c *zkClient, p map[string]any) (map[string]any, error) {
		if _, err := c.cmd(cmdSetTime, u32(encodeZKTime(time.Now()))); err != nil {
			return nil, err
		}
		if _, err := c.cmd(cmdRefreshData, nil); err != nil {
			return nil, err
		}
		t, err := c.readTime()
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": "تمت مزامنة وقت الجهاز مع الحاسبة", "deviceTime": localISO(t)}, nil
	},
	"refresh": simpleCmd(cmdRefreshData, "تم تحديث بيانات الجهاز الداخلية"),
	"testVoice": func(c *zkClient, p map[string]any) (map[string]any, error) {
		i := min(55, max(0, anyInt(p["index"], 0)))
		if _, err := c.cmd(cmdTestVoice, u32(uint32(i))); err != nil {
			return nil, err
		}
		return msg(fmt.Sprintf("تم تشغيل الصوت رقم %d", i)), nil
	},
	"unlock": func(c *zkClient, p map[string]any) (map[string]any, error) {
		s := min(60, max(1, anyInt(p["seconds"], 5)))
		if _, err := c.cmd(cmdUnlock, u32(uint32(s*10))); err != nil { // بوحدات 100 ملي ثانية
			return nil, err
		}
		return msg(fmt.Sprintf("تم فتح الباب لمدة %d ثانية", s)), nil
	},
	"lcdWrite": func(c *zkClient, p map[string]any) (map[string]any, error) {
		text, _ := anyStr(p["text"])
		if r := []rune(text); len(r) > 40 {
			text = string(r[:40])
		}
		head := make([]byte, 3)
		le.PutUint16(head, uint16(max(0, anyInt(p["line"], 0))))
		if _, err := c.cmd(cmdWriteLCD, append(head, []byte(" "+text)...)); err != nil {
			return nil, err
		}
		return msg("تم إرسال النص للشاشة"), nil
	},
	"lcdClear": simpleCmd(cmdClearLCD, "تم مسح النص من الشاشة"),
	"disable":  simpleCmd(cmdDisableDevice, "تم إيقاف البصمة مؤقتاً. الموظفون ما يگدرون يبصمون لحد ما تضغط \"إعادة تفعيل\""),
	"enable":   simpleCmd(cmdEnableDevice, "تم تفعيل الجهاز ويستقبل البصمات بشكل طبيعي"),
	"restart": func(c *zkClient, p map[string]any) (map[string]any, error) {
		c.cmdNoWait(cmdRestart)
		return msg("تم إرسال أمر إعادة التشغيل، الجهاز يرجع خلال دقيقة تقريباً"), nil
	},
	"powerOff": func(c *zkClient, p map[string]any) (map[string]any, error) {
		c.cmdNoWait(cmdPowerOff)
		return msg("تم إرسال أمر الإطفاء"), nil
	},
	"clearLogs":  actClearLogs,
	"updateUser": actUpdateUser,
}

func simpleCmd(code uint16, text string) actionFn {
	return func(c *zkClient, p map[string]any) (map[string]any, error) {
		if _, err := c.cmd(code, nil); err != nil {
			return nil, err
		}
		return msg(text), nil
	}
}

func actClearLogs(c *zkClient, p map[string]any) (map[string]any, error) {
	// نوقف البصمة أثناء العملية حتى ما تضيع بصمة تنسجل بين الحفظ والمسح
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	defer c.cmd(cmdEnableDevice, nil)
	sizes, err := c.readSizes()
	if err != nil {
		return nil, err
	}
	users, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	logs, err := c.getAttendance(users)
	if err != nil {
		return nil, errors.New("سحب السجلات ما اكتمل، فتم إلغاء المسح: " + err.Error())
	}
	if sizes.Records != len(logs) {
		return nil, fmt.Errorf("عدد البصمات المسحوبة (%d) لا يطابق عددها بالجهاز (%d)، فتم إلغاء المسح", len(logs), sizes.Records)
	}
	file, err := writeLogsBackup(sizes, users, logs, "before-clear")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdClearAttLog, nil); err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdRefreshData, nil); err != nil {
		return nil, err
	}
	return msg(fmt.Sprintf("انحفظت نسخة فيها %d بصمة بملف %s داخل مجلد backups، وبعدها انمسح السجل من الجهاز", len(logs), filepath.Base(file))), nil
}

// ---------- تعديل موظف ----------
// نغير بايتات الاسم أو البطاقة فقط بالسجل الخام، وباقي الحقول (الرقم، الصلاحية، الرقم السري، المجموعة) ترجع مثل ما هي

const (
	nameOff      = 11
	nameLen      = 24
	cardOff      = 35
	maxNameBytes = 24 // نفس حد pyzk
)

func detectEncoding(users []zkUser) string {
	for _, u := range users {
		if u.NameEnc == "cp1256" {
			return "cp1256"
		}
	}
	for _, u := range users {
		if u.NameEnc == "utf8" {
			return "utf8"
		}
	}
	return ""
}

func hasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// ترميز الأسماء: حسب الأسماء الموجودة بالجهاز، أو اللي اختاره المستخدم
func chooseNameEncoding(users []zkUser, reqEnc, name string) (enc, note string) {
	detected := detectEncoding(users)
	enc = detected
	if reqEnc == "utf8" || reqEnc == "cp1256" {
		enc = reqEnc
	}
	if enc == "" {
		enc = "utf8"
	}
	if enc != "cp1256" && detected == "" && (reqEnc == "" || reqEnc == "auto") && hasNonASCII(name) {
		note = "كل الأسماء بالجهاز إنگليزية فما گدرت أعرف ترميزه، فاستخدمت UTF-8. تأكد من الاسم على شاشة الجهاز، وإذا طلع رموز غريبة عدّله مرة ثانية واختار Windows-1256."
	}
	return enc, note
}

func buildUserRecord(u zkUser, users []zkUser, p map[string]any) ([]byte, []string, string, error) {
	if len(u.Raw) != 72 {
		return nil, nil, "", badReq("جهازك يستخدم صيغة موظفين قديمة، والتعديل غير مدعوم عليها")
	}
	raw := append([]byte(nil), u.Raw...)
	var changes []string
	note := ""
	if name, ok := p["name"].(string); ok && trimSpace(name) != u.Name {
		name = trimSpace(name)
		if name == "" {
			return nil, nil, "", badReq("الاسم ما يصير يكون فارغ")
		}
		reqEnc, _ := p["encoding"].(string)
		var enc string
		enc, note = chooseNameEncoding(users, reqEnc, name)
		nb, err := encodeName(name, enc)
		if err != nil {
			return nil, nil, "", badReq(err.Error())
		}
		if len(nb) > maxNameBytes {
			return nil, nil, "", badReq(fmt.Sprintf("الاسم طويل: %d بايت والحد %d. اختصره", len(nb), maxNameBytes))
		}
		for i := nameOff; i < nameOff+nameLen; i++ {
			raw[i] = 0
		}
		copy(raw[nameOff:], nb)
		changes = append(changes, "الاسم")
	}
	if cs, ok := anyStr(p["card"]); ok && trimSpace(cs) != "" {
		card, err := strconv.ParseUint(trimSpace(cs), 10, 32)
		if err != nil {
			return nil, nil, "", badReq("رقم البطاقة غير صحيح")
		}
		if uint32(card) != le.Uint32(raw[cardOff:]) {
			if card != 0 {
				for _, x := range users {
					if x.UID != u.UID && x.Card == uint32(card) {
						return nil, nil, "", badReq("رقم البطاقة مستخدم للموظف رقم " + x.UserID)
					}
				}
			}
			le.PutUint32(raw[cardOff:], uint32(card))
			changes = append(changes, "رقم البطاقة")
		}
	}
	return raw, changes, note, nil
}

func eqRange(a, b []byte, s, e int) bool {
	return len(a) >= e && len(b) >= e && string(a[s:e]) == string(b[s:e])
}

// الموظف المطلوب بالجهاز، بشرط رقمه الداخلي ورقمه الوظيفي ما تغيروا من آخر سحب
func loadTarget(c *zkClient, p map[string]any) ([]zkUser, *zkUser, error) {
	uid := anyInt(p["uid"], -1)
	userID, _ := anyStr(p["userId"])
	users, err := c.getUsers()
	if err != nil {
		return nil, nil, err
	}
	for i := range users {
		if int(users[i].UID) == uid && users[i].UserID == userID {
			return users, &users[i], nil
		}
	}
	return nil, nil, badReq("بيانات هذا الموظف تغيرت بالجهاز، اسحب البيانات من جديد وحاول مرة ثانية")
}

func findUID(users []zkUser, uid uint16) *zkUser {
	for i := range users {
		if users[i].UID == uid {
			return &users[i]
		}
	}
	return nil
}

// نفس السجل؟ (نتجاهل بايت الحشو 39 اللي ممكن الجهاز يغيره)
func sameRecord(a, b []byte) bool {
	if len(a) != 72 || len(b) != 72 {
		return false
	}
	return string(a[:39]) == string(b[:39]) && string(a[40:]) == string(b[40:])
}

// يكتب سجل موظف (72 بايت) بعد نسخة احتياطية، ويقرا من جديد حتى يتأكد،
// وإذا الجهاز ما حفظه صح يرجّع السجل الأصلي
func writeUserVerified(c *zkClient, users []zkUser, u *zkUser, raw []byte, tag string) (*zkUser, string, error) {
	backup, err := backupUsers(users, tag)
	if err != nil {
		return nil, "", err
	}
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, backup, err
	}
	_, werr := c.cmd(cmdUserWrq, raw)
	if werr == nil {
		_, werr = c.cmd(cmdRefreshData, nil)
	}
	c.cmd(cmdEnableDevice, nil)
	if werr != nil {
		return nil, backup, werr
	}
	after, err := c.getUsers()
	if err != nil {
		return nil, backup, err
	}
	if a := findUID(after, u.UID); a != nil && a.UserID == u.UserID && sameRecord(a.Raw, raw) {
		return a, backup, nil
	}
	restored := false
	if _, err := c.cmd(cmdUserWrq, u.Raw); err == nil {
		if _, err := c.cmd(cmdRefreshData, nil); err == nil {
			restored = true
		}
	}
	if restored {
		return nil, backup, errors.New("الجهاز ما حفظ التعديل بشكل صحيح، فرجعت بيانات الموظف الأصلية")
	}
	return nil, backup, errors.New("الجهاز ما حفظ التعديل بشكل صحيح، وما گدرت أرجع البيانات الأصلية تلقائياً. النسخة الأصلية محفوظة بملف " + filepath.Base(backup))
}

func actUpdateUser(c *zkClient, p map[string]any) (map[string]any, error) {
	users, u, err := loadTarget(c, p)
	if err != nil {
		return nil, err
	}
	raw, changes, note, err := buildUserRecord(*u, users, p)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return msg("ما أكو أي تغيير"), nil
	}
	a, backup, err := writeUserVerified(c, users, u, raw, "users-before-edit")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"message": fmt.Sprintf("تم تعديل %s للموظف رقم %s وتأكدت من الحفظ بالجهاز", strings.Join(changes, " و"), u.UserID),
		"note":    note, "backup": filepath.Base(backup), "user": userJSON(*a),
	}, nil
}

// ---------- مسارات الخادم ----------

type cmdRequest struct {
	IP     string         `json:"ip"`
	Port   any            `json:"port"`
	Key    any            `json:"key"`
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

func commKey(v any) uint32 {
	n := anyInt(v, 0)
	if n < 0 {
		return 0
	}
	return uint32(n)
}

func handleData(w http.ResponseWriter, r *http.Request) {
	if demoMode {
		d := mockData()
		audit(r, "data", map[string]any{"device": "demo"}, nil)
		writeJSON(w, 200, d)
		return
	}
	q := r.URL.Query()
	ip := q.Get("ip")
	if !validIP.MatchString(ip) {
		writeErr(w, badReq("عنوان IP غير صحيح"), "")
		return
	}
	port := anyInt(q.Get("port"), 4370)
	res, err := withDevice(ip, port, commKey(q.Get("key")), func(c *zkClient) (map[string]any, error) {
		sizes, _ := c.readSizes()
		users, err := c.getUsers()
		if err != nil {
			return nil, err
		}
		logs, err := c.getAttendance(users)
		if err != nil {
			return nil, err
		}
		return map[string]any{"info": sizesJSON(sizes), "users": usersJSON(users), "logs": logsJSON(logs), "partial": false}, nil
	})
	detail := map[string]any{"device": ip}
	if err == nil {
		detail["users"], detail["logs"] = len(res["users"].([]map[string]any)), len(res["logs"].([]map[string]any))
	}
	audit(r, "data", detail, err)
	if err != nil {
		log.Println("data:", err)
		writeErr(w, err, "تعذر سحب البيانات: ")
		return
	}
	writeJSON(w, 200, res)
}

func handleCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, apiErr{405, "طريقة غير مسموحة"}, "")
		return
	}
	var req cmdRequest
	if err := readJSONBody(r, 1<<17, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	fn, ok := actions[req.Action]
	if !ok {
		writeErr(w, badReq("أمر غير معروف"), "")
		return
	}
	if !allowed(r, actionNeeds(req.Action)) {
		deny(w, r, req.Action)
		return
	}
	if req.Params == nil {
		req.Params = map[string]any{}
	}
	if phrase, ok := confirmPhrase[req.Action]; ok {
		if got, _ := anyStr(req.Params["confirm"]); strings.TrimSpace(got) != phrase {
			writeErr(w, badReq("حتى تأكد، اكتب العبارة: "+phrase), "")
			return
		}
	}
	log.Printf("[أمر] %s → %s", req.Action, req.IP)
	var res map[string]any
	var err error
	if demoMode {
		res, err = mockAction(req.Action, req.Params)
	} else {
		if !validIP.MatchString(req.IP) {
			writeErr(w, badReq("عنوان IP غير صحيح"), "")
			return
		}
		if req.Action == "deviceSecurityCheck" {
			res, err = scanDevice(req.IP, anyInt(req.Port, 4370), commKey(req.Key))
		} else if req.Action == "setDeviceCommKey" {
			change, _ := req.Params["change"].(bool)
			nk, verr := parseNewCommKey(req.Params["password"], change)
			if verr != nil {
				writeErr(w, verr, "")
				return
			}
			res, err = setDeviceCommKey(req.IP, anyInt(req.Port, 4370), commKey(req.Key), nk, change)
		} else {
			res, err = withDevice(req.IP, anyInt(req.Port, 4370), commKey(req.Key), func(c *zkClient) (map[string]any, error) {
				return fn(c, req.Params)
			})
		}
	}
	if req.Action != "info" && req.Action != "getTime" {
		detail := safeParams(req.Action, req.Params)
		detail["device"] = req.IP
		if demoMode {
			detail["device"] = "demo"
		}
		if res != nil && res["backup"] != nil {
			detail["backup"] = res["backup"]
		}
		if res != nil && res["risk"] != nil {
			detail["risk"] = res["risk"]
		}
		if res != nil && res["applied"] != nil {
			detail["applied"] = res["applied"]
		}
		audit(r, req.Action, detail, err)
	}
	if err != nil {
		log.Println("cmd:", err)
		writeErr(w, err, "فشل تنفيذ الأمر: ")
		return
	}
	writeJSON(w, 200, res)
}

// الصلاحية اللي يحتاجها كل أمر. أي أمر مو مذكور هنا (الإطفاء والمسح والحذف والإعدادات المخفية...) للصلاحية المطلقة بس
var actionRole = map[string]string{
	"info": RoleViewer, "getTime": RoleViewer,
	"syncTime": RoleSupervisor, "refresh": RoleSupervisor, "testVoice": RoleSupervisor, "unlock": RoleSupervisor,
	"lcdWrite": RoleSupervisor, "lcdClear": RoleSupervisor, "disable": RoleSupervisor, "enable": RoleSupervisor,
	"restart": RoleSupervisor, "updateUser": RoleSupervisor, "deviceSecurityCheck": RoleSupervisor,
	"setDeviceCommKey": RoleAdmin,
}

func actionNeeds(action string) string {
	if r, ok := actionRole[action]; ok {
		return r
	}
	return RoleAdmin
}

// أوامر ما تتنفذ إلا إذا انكتبت عبارة التأكيد حرفياً
var confirmPhrase = map[string]string{"clearAllData": "امسح الكل"}
