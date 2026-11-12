package server

// أوامر الصلاحية المطلقة: إضافة وحذف موظفين، صلاحية ورقم سري، مسح البيانات، والإعدادات المخفية.
// كلها أوامر حقيقية من مكتبة pyzk (وأرقامها نفسها بمكتبة node-zklib)،
// وكل أمر يمسح أو يغير شي يحفظ نسخة احتياطية قبله بمجلد backups.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ackError   = 2001
	privUser   = 0
	privAdmin  = 14 // نفس USER_ADMIN بمكتبة pyzk
	oldFmtNote = "جهازك يستخدم صيغة موظفين قديمة، وهذا الأمر غير مدعوم عليها"
)

func init() {
	for k, v := range map[string]actionFn{
		"setTime":       actSetTime,
		"addUser":       actAddUser,
		"setPrivilege":  actSetPrivilege,
		"setPin":        actSetPin,
		"deleteUser":    actDeleteUser,
		"deleteFingers": actDeleteFingers,
		"clearAdmins":   actClearAdmins,
		"clearOplog":    actClearOplog,
		"clearAccess":   actClearAccess,
		"clearAllData":  actClearAllData,
		"readOption":    actReadOption,
		"writeOption":   actWriteOption,
		"restoreUsers":  actRestoreUsers,
	} {
		actions[k] = v
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func u16(n uint16) []byte { b := make([]byte, 2); le.PutUint16(b, n); return b }

func requireRaw72(users []zkUser) error {
	for _, u := range users {
		if len(u.Raw) != 72 {
			return badReq(oldFmtNote)
		}
	}
	return nil
}

// عدد اللي عندهم صلاحية بقائمة الجهاز
func countAdmins(users []zkUser) int {
	n := 0
	for _, u := range users {
		if u.Role != privUser {
			n++
		}
	}
	return n
}

func privLabel(p int) string {
	if p == privAdmin {
		return "مدير"
	}
	return "مستخدم عادي"
}

func paramPriv(p map[string]any) (int, error) {
	priv := anyInt(p["privilege"], -1)
	if priv != privUser && priv != privAdmin {
		return 0, badReq("الصلاحية بالجهاز لازم تكون مستخدم عادي أو مدير")
	}
	return priv, nil
}

func paramPin(p map[string]any) (string, error) {
	pin, _ := anyStr(p["pin"])
	pin = trimSpace(pin)
	if pin != "" && (!isDigits(pin) || len(pin) > 8) {
		return "", badReq("الرقم السري لازم يكون أرقام فقط، لحد 8 أرقام")
	}
	return pin, nil
}

// يقرا الحجم والموظفين والسجل ويتأكد إن عدد البصمات مطابق، قبل أي مسح
func snapshotAll(c *zkClient) (*zkSizes, []zkUser, []zkLog, error) {
	sizes, err := c.readSizes()
	if err != nil {
		return nil, nil, nil, err
	}
	users, err := c.getUsers()
	if err != nil {
		return nil, nil, nil, err
	}
	logs, err := c.getAttendance(users)
	if err != nil {
		return nil, nil, nil, errors.New("سحب السجلات ما اكتمل، فتم إلغاء العملية: " + err.Error())
	}
	if sizes.Records != len(logs) {
		return nil, nil, nil, fmt.Errorf("عدد البصمات المسحوبة (%d) لا يطابق عددها بالجهاز (%d)، فتم إلغاء العملية", len(logs), sizes.Records)
	}
	return sizes, users, logs, nil
}

// ---------- الوقت ----------

func actSetTime(c *zkClient, p map[string]any) (map[string]any, error) {
	s, _ := anyStr(p["time"])
	s = trimSpace(s)
	var t time.Time
	err := errors.New("")
	for _, f := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err = time.ParseInLocation(f, s, time.Local); err == nil {
			break
		}
	}
	if err != nil {
		return nil, badReq("اختار التاريخ والوقت")
	}
	if t.Year() < 2000 || t.Year() > 2099 {
		return nil, badReq("السنة لازم تكون بين 2000 و 2099")
	}
	if _, err := c.cmd(cmdSetTime, u32(encodeZKTime(t))); err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdRefreshData, nil); err != nil {
		return nil, err
	}
	dt, err := c.readTime()
	if err != nil {
		return nil, err
	}
	return map[string]any{"message": "تم ضبط وقت الجهاز على " + t.Format("2006-01-02 15:04:05"),
		"deviceTime": localISO(dt), "pcTime": localISO(time.Now())}, nil
}

// ---------- موظفي الجهاز ----------

func actAddUser(c *zkClient, p map[string]any) (map[string]any, error) {
	userID, _ := anyStr(p["userId"])
	userID = trimSpace(userID)
	if !isDigits(userID) {
		return nil, badReq("رقم الموظف لازم يكون أرقام فقط")
	}
	name, _ := anyStr(p["name"])
	name = trimSpace(name)
	if name == "" {
		return nil, badReq("اكتب اسم الموظف")
	}
	if p["privilege"] == nil || p["privilege"] == "" {
		p["privilege"] = privUser // بدون اختيار: موظف عادي
	}
	priv, err := paramPriv(p)
	if err != nil {
		return nil, err
	}
	if priv != privUser && !callerIsSuper(p) {
		return nil, apiErr{403, "إضافة مدير للجهاز تحتاج الصلاحية المطلقة"}
	}
	pin, err := paramPin(p)
	if err != nil {
		return nil, err
	}
	var card uint64
	if cs, ok := anyStr(p["card"]); ok && trimSpace(cs) != "" {
		if card, err = strconv.ParseUint(trimSpace(cs), 10, 32); err != nil {
			return nil, badReq("رقم البطاقة غير صحيح")
		}
	}
	width := 9
	if d, err := c.cmd(cmdGetPinWidth, []byte(" P")); err == nil && len(d) > 0 && d[0] > 0 {
		width = int(d[0])
	}
	if width = min(width, 24); len(userID) > width {
		return nil, badReq(fmt.Sprintf("رقم الموظف طويل، الجهاز يقبل لحد %d أرقام", width))
	}
	users, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	if err := requireRaw72(users); err != nil {
		return nil, err
	}
	if len(users) == 0 { // جهاز فارغ: نعرف الصيغة من إصدار البرنامج الثابت (الإصدار 6 يستخدم 28 بايت)
		if d, err := c.cmd(cmdGetVersion, nil); err == nil && strings.Contains(string(d), "Ver 6.") {
			return nil, badReq(oldFmtNote)
		}
	}
	maxUID := 0
	for _, u := range users {
		if u.UserID == userID {
			return nil, badReq(fmt.Sprintf("رقم الموظف %s موجود بالجهاز (%s)", userID, u.Name))
		}
		if card != 0 && u.Card == uint32(card) {
			return nil, badReq("رقم البطاقة مستخدم للموظف رقم " + u.UserID)
		}
		maxUID = max(maxUID, int(u.UID))
	}
	if maxUID >= 65534 {
		return nil, badReq("ما بقى رقم داخلي فارغ لموظف جديد بالجهاز")
	}
	reqEnc, _ := p["encoding"].(string)
	enc, note := chooseNameEncoding(users, reqEnc, name)
	nb, err := encodeName(name, enc)
	if err != nil {
		return nil, badReq(err.Error())
	}
	if len(nb) > maxNameBytes {
		return nil, badReq(fmt.Sprintf("الاسم طويل: %d بايت والحد %d. اختصره", len(nb), maxNameBytes))
	}
	uid := uint16(maxUID + 1)
	raw := make([]byte, 72) // '<HB8s24sIx7sx24s' نفس pyzk.set_user
	le.PutUint16(raw[0:], uid)
	raw[2] = byte(priv)
	copy(raw[3:11], pin)
	copy(raw[nameOff:nameOff+nameLen], nb)
	le.PutUint32(raw[cardOff:], uint32(card))
	copy(raw[40:47], "1") // المجموعة الافتراضية
	copy(raw[48:72], userID)
	backup, err := backupUsers(users, "users-before-add")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	_, werr := c.cmd(cmdUserWrq, raw)
	if werr == nil {
		_, werr = c.cmd(cmdRefreshData, nil)
	}
	c.cmd(cmdEnableDevice, nil)
	if werr != nil {
		return nil, werr
	}
	after, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	a := findUID(after, uid)
	if a == nil || a.UserID != userID {
		return nil, errors.New("الجهاز ما حفظ الموظف الجديد. تأكد من مساحة الذاكرة وحاول مرة ثانية")
	}
	if a.Role != priv || a.Card != uint32(card) || string(a.Raw[3:11]) != string(raw[3:11]) ||
		string(a.Raw[nameOff:nameOff+nameLen]) != string(raw[nameOff:nameOff+nameLen]) {
		note = strings.TrimSpace(note + " الجهاز حفظ الموظف بس بعض بياناته طلعت مختلفة، افتح «تعديل» الموظف وتأكد منها.")
	}
	return map[string]any{
		"message": fmt.Sprintf("انضاف الموظف رقم %s (%s) للجهاز. هسه سجّل بصمته أو وجهه من قائمة الجهاز", userID, a.Name),
		"note":    note, "backup": filepath.Base(backup), "user": userJSON(*a),
	}, nil
}

func actSetPrivilege(c *zkClient, p map[string]any) (map[string]any, error) {
	priv, err := paramPriv(p)
	if err != nil {
		return nil, err
	}
	users, u, err := loadTarget(c, p)
	if err != nil {
		return nil, err
	}
	if len(u.Raw) != 72 {
		return nil, badReq(oldFmtNote)
	}
	if u.Role == priv {
		return msg("ما أكو أي تغيير"), nil
	}
	raw := append([]byte(nil), u.Raw...)
	raw[2] = byte(priv)
	a, backup, err := writeUserVerified(c, users, u, raw, "users-before-privilege")
	if err != nil {
		return nil, err
	}
	note := ""
	if priv == privUser {
		others := 0
		for _, x := range users {
			if x.UID != u.UID && x.Role != privUser {
				others++
			}
		}
		if others == 0 {
			note = "ما بقى مدير بالجهاز، فأي شخص يگدر يفتح قائمة الجهاز"
		}
	}
	return map[string]any{"message": fmt.Sprintf("صارت صلاحية الموظف رقم %s بالجهاز: %s، وتأكدت من الحفظ", u.UserID, privLabel(priv)),
		"note": note, "backup": filepath.Base(backup), "user": userJSON(*a)}, nil
}

func actSetPin(c *zkClient, p map[string]any) (map[string]any, error) {
	pin, err := paramPin(p)
	if err != nil {
		return nil, err
	}
	users, u, err := loadTarget(c, p)
	if err != nil {
		return nil, err
	}
	if len(u.Raw) != 72 {
		return nil, badReq(oldFmtNote)
	}
	raw := append([]byte(nil), u.Raw...)
	for i := 3; i < 11; i++ {
		raw[i] = 0
	}
	copy(raw[3:11], pin)
	if string(raw[3:11]) == string(u.Raw[3:11]) {
		return msg("ما أكو أي تغيير"), nil
	}
	a, backup, err := writeUserVerified(c, users, u, raw, "users-before-pin")
	if err != nil {
		return nil, err
	}
	text := "تغير الرقم السري للموظف رقم %s، وتأكدت من الحفظ"
	if pin == "" {
		text = "انمسح الرقم السري للموظف رقم %s، وتأكدت من الحفظ"
	}
	return map[string]any{"message": fmt.Sprintf(text, u.UserID), "backup": filepath.Base(backup), "user": userJSON(*a)}, nil
}

func actDeleteUser(c *zkClient, p map[string]any) (map[string]any, error) {
	users, u, err := loadTarget(c, p)
	if err != nil {
		return nil, err
	}
	backup, err := backupUsers(users, "users-before-delete")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	_, werr := c.cmd(cmdDeleteUser, u16(u.UID))
	if werr == nil {
		_, werr = c.cmd(cmdRefreshData, nil)
	}
	c.cmd(cmdEnableDevice, nil)
	if werr != nil {
		return nil, werr
	}
	after, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	if a := findUID(after, u.UID); a != nil && a.UserID == u.UserID {
		return nil, errors.New("الجهاز رد بالموافقة بس الموظف بعده موجود. حاول مرة ثانية")
	}
	return map[string]any{
		"message": fmt.Sprintf("انحذف الموظف رقم %s (%s) من الجهاز ويا بصماته ووجهه. سجل حضوره القديم يبقى. بياناته محفوظة بملف %s", u.UserID, u.Name, filepath.Base(backup)),
		"backup":  filepath.Base(backup),
	}, nil
}

func actDeleteFingers(c *zkClient, p map[string]any) (map[string]any, error) {
	f := anyInt(p["finger"], -1)
	if f < -1 || f > 9 {
		return nil, badReq("رقم الإصبع لازم يكون من 0 إلى 9")
	}
	_, u, err := loadTarget(c, p)
	if err != nil {
		return nil, err
	}
	fids := []int{f}
	if f == -1 {
		fids = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	}
	before, _ := c.readSizes()
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	deleted, unknown := 0, 0
	for _, fid := range fids { // الأجهزة الحديثة: الأمر 134 برقم الموظف ورقم الإصبع
		pl := make([]byte, 25)
		copy(pl, u.UserID)
		pl[24] = byte(fid)
		code, _, err := c.send(cmdDelUserTemp, pl)
		if err != nil {
			return nil, err
		}
		switch {
		case code == ackOK:
			deleted++
		case code != ackError:
			unknown++
		}
	}
	if deleted == 0 && unknown == len(fids) { // جهاز قديم ما يعرف 134: الأمر 19 بالرقم الداخلي
		for _, fid := range fids {
			code, _, err := c.send(cmdDeleteUTemp, append(u16(u.UID), byte(fid)))
			if err != nil {
				return nil, err
			}
			if code == ackOK {
				deleted++
			}
		}
	}
	c.cmd(cmdRefreshData, nil)
	c.cmd(cmdEnableDevice, nil)
	after, _ := c.readSizes()
	counts := ""
	if before != nil && after != nil {
		counts = fmt.Sprintf(" (بصمات الأصابع بالجهاز كانت %d وهسه %d)", before.Fingers, after.Fingers)
	}
	if deleted == 0 {
		return msg(fmt.Sprintf("ما لقيت بصمات أصابع للموظف رقم %s بالجهاز%s", u.UserID, counts)), nil
	}
	return map[string]any{
		"message": fmt.Sprintf("انحذفت %d بصمة إصبع للموظف رقم %s%s. حتى يبصم بالإصبع لازم يسجل بصمة جديدة من قائمة الجهاز", deleted, u.UserID, counts),
		"note":    "الوجه والبطاقة والرقم السري ما تأثروا",
	}, nil
}

// ---------- المسح والتصفير ----------

func actClearAdmins(c *zkClient, p map[string]any) (map[string]any, error) {
	users, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	before := countAdmins(users)
	backup, err := backupUsers(users, "users-before-clear-admins")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	_, werr := c.cmd(cmdClearAdmin, nil)
	if werr == nil {
		_, werr = c.cmd(cmdRefreshData, nil)
	}
	c.cmd(cmdEnableDevice, nil)
	if werr != nil {
		return nil, werr
	}
	after, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	n := countAdmins(after)
	res := map[string]any{"backup": filepath.Base(backup),
		"message": fmt.Sprintf("انلغت صلاحية المدير بالجهاز (كانوا %d وهسه %d). هسه أي شخص يگدر يفتح قائمة الجهاز، فرجّع مدير من «تعديل» الموظف", before, n)}
	if n > 0 && n == before {
		res["note"] = "الجهاز رد بالموافقة بس الصلاحيات ما تغيرت، ممكن جهازك ما يدعم هذا الأمر"
	}
	return res, nil
}

func actClearOplog(c *zkClient, p map[string]any) (map[string]any, error) {
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	defer c.cmd(cmdEnableDevice, nil)
	// بعض المصادر تگول هذا الأمر يمسح وياه سجل البصمات، فنحفظ السجل كامل قبله احتياطاً
	sizes, users, logs, err := snapshotAll(c)
	if err != nil {
		return nil, err
	}
	file, err := writeFullBackup(sizes, usersJSON(users), logs, "before-clear-oplog")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdClearOplog, nil); err != nil {
		return nil, err
	}
	c.cmd(cmdRefreshData, nil)
	text := "انمسح سجل عمليات الجهاز (منو دخل قائمة الجهاز وشنو غيّر)."
	if after, err := c.readSizes(); err == nil && after.Records < sizes.Records {
		text += fmt.Sprintf(" تنبيه: الجهاز مسح وياه سجل البصمات (كان %d وهسه %d)، والنسخة الكاملة محفوظة بملف %s", sizes.Records, after.Records, filepath.Base(file))
	} else {
		text += " سجل البصمات ما تأثر."
	}
	return map[string]any{"message": text, "backup": filepath.Base(file)}, nil
}

func actClearAccess(c *zkClient, p map[string]any) (map[string]any, error) {
	if _, err := c.cmd(cmdClearAcc, nil); err != nil {
		return nil, err
	}
	c.cmd(cmdRefreshData, nil)
	return msg("رجعت إعدادات التحكم بالأبواب للوضع الافتراضي (المناطق الزمنية والمجموعات وتركيبات الفتح)"), nil
}

func actClearAllData(c *zkClient, p map[string]any) (map[string]any, error) {
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	defer c.cmd(cmdEnableDevice, nil)
	sizes, users, logs, err := snapshotAll(c)
	if err != nil {
		return nil, err
	}
	file, err := writeFullBackup(sizes, usersRawJSON(users), logs, "before-clear-all")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdClearData, nil); err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdRefreshData, nil); err != nil {
		return nil, err
	}
	text := fmt.Sprintf("انمسحت كل بيانات الجهاز. قبل المسح انحفظت نسخة بملف %s فيها %d موظف و%d بصمة حضور. "+
		"بصمات الأصابع والوجوه نفسها ما تنحفظ بالنسخة، لازم الموظفين يسجلونها من جديد. "+
		"تگدر ترجع الموظفين (أسماءهم وبطاقاتهم وصلاحياتهم) من «استرجاع الموظفين من نسخة»", filepath.Base(file), len(users), len(logs))
	if after, err := c.readSizes(); err == nil {
		text += fmt.Sprintf(". هسه بالجهاز: %d موظف، %d بصمة إصبع، %d سجل حضور", after.Users, after.Fingers, after.Records)
	}
	return map[string]any{"message": text, "backup": filepath.Base(file)}, nil
}

// ---------- الإعدادات المخفية بالجهاز ----------

func cleanOptionKey(p map[string]any) (string, error) {
	k, _ := anyStr(p["key"])
	k = strings.TrimSpace(k)
	if k == "" || len(k) > 40 {
		return "", badReq("اكتب اسم الإعداد، مثل ~DeviceName")
	}
	for _, r := range k {
		if r <= ' ' || r > '~' || r == '=' {
			return "", badReq("اسم الإعداد يقبل حروف إنگليزية وأرقام ورموز بدون فراغات وبدون =")
		}
	}
	return k, nil
}

func actReadOption(c *zkClient, p map[string]any) (map[string]any, error) {
	key, err := cleanOptionKey(p)
	if err != nil {
		return nil, err
	}
	v := c.readOption(key)
	if v == nil {
		return map[string]any{"key": key, "value": nil,
			"message": "الجهاز ما رجّع قيمة للإعداد " + key + " (ممكن الاسم غلط، أو الجهاز ما يدعمه، أو قيمته فارغة)"}, nil
	}
	return map[string]any{"key": key, "value": *v, "message": key + " = " + *v}, nil
}

func actWriteOption(c *zkClient, p map[string]any) (map[string]any, error) {
	key, err := cleanOptionKey(p)
	if err != nil {
		return nil, err
	}
	val, _ := anyStr(p["value"])
	val = strings.TrimSpace(val)
	if len(val) > 100 || strings.ContainsAny(val, "\x00\r\n") {
		return nil, badReq("القيمة طويلة أو بيها رموز غير مسموحة")
	}
	before := c.readOption(key)
	if _, err := c.cmd(cmdOptionsWrq, []byte(key+"="+val)); err != nil { // نفس pyzk: "Key=Value" بدون صفر بالنهاية
		return nil, err
	}
	old := c.timeout
	c.timeout = 6 * time.Second // إذا الإعداد غيّر عنوان الجهاز ما ننتظر 20 ثانية
	c.send(cmdRefreshOption, nil)
	after := c.readOption(key)
	c.timeout = old
	res := map[string]any{"key": key, "before": strPtr(before), "after": strPtr(after)}
	if after != nil && *after == val {
		res["message"] = fmt.Sprintf("انكتب الإعداد %s = %s وتأكدت منه بالجهاز. بعض الإعدادات ما تشتغل إلا بعد إعادة تشغيل الجهاز", key, val)
	} else {
		got := "ولا شي"
		if after != nil {
			got = *after
		}
		res["message"] = fmt.Sprintf("الجهاز قبل الإعداد %s = %s، بس لما قريته من جديد رجع: %s", key, val, got)
	}
	return res, nil
}

// ---------- استرجاع الموظفين من نسخة احتياطية ----------

func isRestorableName(n string) bool {
	return strings.HasSuffix(n, ".json") && (strings.HasPrefix(n, "users-before-") || strings.HasPrefix(n, "before-clear-all"))
}

func readRawUsers(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, badReq("ما گدرت أقرا ملف النسخة")
	}
	var f struct {
		Users []struct {
			Raw string `json:"raw"`
		} `json:"users"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil, badReq("ملف النسخة تالف")
	}
	var out [][]byte
	for _, u := range f.Users {
		if raw, err := hex.DecodeString(u.Raw); err == nil && len(raw) == 72 {
			out = append(out, raw)
		}
	}
	return out, nil
}

func actRestoreUsers(c *zkClient, p map[string]any) (map[string]any, error) {
	name, _ := anyStr(p["file"])
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || !isRestorableName(name) {
		return nil, badReq("اختار نسخة احتياطية من القائمة")
	}
	recs, err := readRawUsers(filepath.Join(dataDir, "backups", name))
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, badReq("النسخة ما بيها موظفين يگدر البرنامج يرجعهم")
	}
	users, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	if err := requireRaw72(users); err != nil {
		return nil, err
	}
	have, used, maxUID := map[string]bool{}, map[uint16]bool{}, 0
	for _, u := range users {
		have[u.UserID], used[u.UID] = true, true
		maxUID = max(maxUID, int(u.UID))
	}
	var toWrite [][]byte
	var ids []string
	skipped, skippedAdmins := 0, 0
	for _, raw := range recs {
		id := string(cstrBytes(raw[48:72]))
		if id == "" || have[id] {
			skipped++
			continue
		}
		if raw[2] != privUser && !callerIsSuper(p) { // مدراء الجهاز يرجعهم صاحب الصلاحية المطلقة بس
			skippedAdmins++
			continue
		}
		uid := le.Uint16(raw[0:])
		if uid == 0 || used[uid] { // رقمه الداخلي صار لموظف ثاني: ناخذ رقم فارغ
			maxUID++
			uid = uint16(maxUID)
			le.PutUint16(raw[0:], uid)
		}
		used[uid], have[id] = true, true
		maxUID = max(maxUID, int(uid))
		toWrite, ids = append(toWrite, raw), append(ids, id)
	}
	if len(toWrite) == 0 {
		if skippedAdmins > 0 {
			return nil, apiErr{403, fmt.Sprintf("اللي ناقص بالجهاز من هذي النسخة %d مدير جهاز، وإرجاعهم يحتاج الصلاحية المطلقة", skippedAdmins)}
		}
		return msg(fmt.Sprintf("كل الموظفين اللي بالنسخة (%d) موجودين بالجهاز، ما أكو شي يرجع", skipped)), nil
	}
	if maxUID > 65534 {
		return nil, badReq("ما بقى أرقام داخلية فارغة بالجهاز")
	}
	backup, err := backupUsers(users, "users-before-restore")
	if err != nil {
		return nil, err
	}
	if _, err := c.cmd(cmdDisableDevice, nil); err != nil {
		return nil, err
	}
	var werr error
	for _, raw := range toWrite {
		if _, werr = c.cmd(cmdUserWrq, raw); werr != nil {
			break
		}
	}
	if _, err := c.cmd(cmdRefreshData, nil); err != nil && werr == nil {
		werr = err
	}
	c.cmd(cmdEnableDevice, nil)
	after, err := c.getUsers()
	if err != nil {
		return nil, err
	}
	now := map[string]bool{}
	for _, u := range after {
		now[u.UserID] = true
	}
	restored := 0
	for _, id := range ids {
		if now[id] {
			restored++
		}
	}
	if restored == 0 && werr != nil {
		return nil, werr
	}
	text := fmt.Sprintf("رجع %d موظف من النسخة (أسماءهم وبطاقاتهم وصلاحياتهم وأرقامهم السرية)، بدون بصمات الأصابع والوجوه: لازم يسجلونها من جديد من قائمة الجهاز", restored)
	if skipped > 0 {
		text += fmt.Sprintf(". %d كانوا موجودين بالجهاز فما تغيروا", skipped)
	}
	if skippedAdmins > 0 {
		text += fmt.Sprintf(". %d مدير جهاز ما رجعتهم، يحتاجون الصلاحية المطلقة", skippedAdmins)
	}
	if restored < len(toWrite) {
		text += fmt.Sprintf(". %d ما انحفظوا، حاول مرة ثانية", len(toWrite)-restored)
	}
	return map[string]any{"message": text, "backup": filepath.Base(backup)}, nil
}

func backupKind(n string) string {
	kinds := [][2]string{{"before-clear-all", "قبل مسح كل بيانات الجهاز"}, {"users-before-delete", "قبل حذف موظف"},
		{"users-before-edit", "قبل تعديل موظف"}, {"users-before-add", "قبل إضافة موظف"}, {"users-before-privilege", "قبل تغيير صلاحية"},
		{"users-before-pin", "قبل تغيير رقم سري"}, {"users-before-clear-admins", "قبل إلغاء المدراء"}, {"users-before-restore", "قبل استرجاع موظفين"}}
	for _, k := range kinds {
		if strings.HasPrefix(n, k[0]) {
			return k[1]
		}
	}
	return "نسخة موظفين"
}

// النسخ اللي بيها موظفين يگدر البرنامج يرجعهم للجهاز، الأحدث أول
func handleBackups(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	if demoMode {
		writeJSON(w, 200, map[string]any{"backups": out})
		return
	}
	dir := filepath.Join(dataDir, "backups")
	ents, _ := os.ReadDir(dir)
	type item struct {
		name string
		mod  time.Time
		size int64
	}
	var list []item
	for _, e := range ents {
		if e.IsDir() || !isRestorableName(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			list = append(list, item{e.Name(), info.ModTime(), info.Size()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].mod.After(list[j].mod) })
	for _, it := range list[:min(50, len(list))] {
		recs, err := readRawUsers(filepath.Join(dir, it.name))
		if err != nil || len(recs) == 0 {
			continue
		}
		out = append(out, map[string]any{"name": it.name, "time": localISO(it.mod), "size": it.size, "users": len(recs), "kind": backupKind(it.name)})
	}
	writeJSON(w, 200, map[string]any{"backups": out})
}
