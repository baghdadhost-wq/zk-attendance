package server

// وضع التجربة: بيانات وهمية بدون جهاز

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

type mockUser struct {
	UID     int
	UserID  string
	Name    string
	Card    uint32
	Role    int
	HasPass bool
}

var (
	mockMu      sync.Mutex
	mockOptions = map[string]string{"~SerialNumber": "DEMO123456789", "~Platform": "ZMM220_TFT", "~DeviceName": "iFace1000",
		"MAC": "00:17:61:00:00:01", "IPAddress": "192.168.1.201", "NetMask": "255.255.255.0", "GATEIPAddress": "192.168.1.1",
		"ZKFaceVersion": "7", "~ZKFPVersion": "10", "~ExtendFmt": "1", "FaceFunOn": "1"}
	mockUsers = func() []*mockUser {
		names := []string{"أحمد علي", "محمد حسن", "زينب كاظم", "علي جاسم", "نور الهدى", "حسين عباس", "مريم صالح", "عمر خالد"}
		out := make([]*mockUser, len(names))
		for i, n := range names {
			role := 0
			if i == 0 {
				role = 14
			}
			out[i] = &mockUser{UID: i + 1, UserID: fmt.Sprint(i + 1), Name: n, Role: role, HasPass: i == 0 || i == 4}
		}
		return out
	}()
)

func mockData() map[string]any {
	mockMu.Lock()
	defer mockMu.Unlock()
	users := make([]map[string]any, len(mockUsers))
	for i, u := range mockUsers {
		users[i] = map[string]any{"uid": u.UID, "userId": u.UserID, "name": u.Name, "cardno": u.Card, "role": u.Role, "hasPassword": u.HasPass}
	}
	logs := []map[string]any{}
	now := time.Now()
	for d := 30; d >= 0; d-- {
		day := now.AddDate(0, 0, -d)
		if day.Weekday() == time.Friday {
			continue
		}
		for _, u := range mockUsers {
			if rand.Float64() < 0.08 {
				continue
			}
			in := time.Date(day.Year(), day.Month(), day.Day(), 7, 40+rand.Intn(45), rand.Intn(60), 0, time.Local)
			out := time.Date(day.Year(), day.Month(), day.Day(), 13+rand.Intn(2), rand.Intn(60), 0, 0, time.Local)
			logs = append(logs, map[string]any{"userId": u.UserID, "time": localISO(in)})
			if rand.Float64() < 0.3 {
				logs = append(logs, map[string]any{"userId": u.UserID, "time": localISO(in.Add(time.Minute))})
			}
			if rand.Float64() > 0.05 {
				logs = append(logs, map[string]any{"userId": u.UserID, "time": localISO(out)})
			}
		}
	}
	return map[string]any{"info": map[string]any{"users": len(users), "records": len(logs), "mock": true}, "users": users, "logs": logs, "partial": false}
}

func mockAction(action string, p map[string]any) (map[string]any, error) {
	now := localISO(time.Now())
	switch action {
	case "getTime":
		return map[string]any{"deviceTime": localISO(time.Now().Add(-125 * time.Second)), "pcTime": now}, nil
	case "info":
		return map[string]any{"firmware": "Ver 8.0.4.3 (تجريبي)", "serial": "DEMO123456789", "platform": "ZMM220_TFT",
			"deviceName": "iFace1000", "mac": "00:17:61:00:00:01", "faceVersion": "7", "fpVersion": "10",
			"ip": "192.168.1.201", "netmask": "255.255.255.0", "gateway": "192.168.1.1", "pinWidth": 9,
			"deviceTime": now, "pcTime": now,
			"sizes": map[string]any{"users": 8, "usersCap": 10000, "fingers": 14, "fingersCap": 4000, "records": 4410,
				"recordsCap": 100000, "cards": 0, "faces": 8, "facesCap": 3000}}, nil
	case "updateUser":
		mockMu.Lock()
		defer mockMu.Unlock()
		uid := anyInt(p["uid"], -1)
		userID, _ := anyStr(p["userId"])
		var target *mockUser
		fake := make([]zkUser, len(mockUsers))
		for i, u := range mockUsers {
			raw := make([]byte, 72)
			le.PutUint16(raw, uint16(u.UID))
			copy(raw[nameOff:], []byte(u.Name))
			le.PutUint32(raw[cardOff:], u.Card)
			enc := "ascii"
			if hasNonASCII(u.Name) {
				enc = "utf8"
			}
			fake[i] = zkUser{UID: uint16(u.UID), Name: u.Name, NameEnc: enc, Card: u.Card, UserID: u.UserID, Raw: raw}
			if u.UID == uid {
				target = u
			}
		}
		if target == nil || target.UserID != userID {
			return nil, badReq("بيانات هذا الموظف تغيرت، اسحب البيانات من جديد")
		}
		raw, changes, _, err := buildUserRecord(fake[target.UID-1], fake, p)
		if err != nil {
			return nil, err
		}
		if len(changes) == 0 {
			return msg("ما أكو أي تغيير"), nil
		}
		target.Name, _ = decodeName(raw[nameOff : nameOff+nameLen])
		target.Card = le.Uint32(raw[cardOff:])
		return map[string]any{"message": fmt.Sprintf("(تجريبي) تم تعديل %s للموظف رقم %s", strings.Join(changes, " و"), target.UserID),
			"user": map[string]any{"uid": target.UID, "userId": target.UserID, "name": target.Name, "cardno": target.Card, "role": target.Role, "hasPassword": target.HasPass}}, nil
	case "clearLogs":
		return msg("(تجريبي) انحفظت نسخة وبعدها انمسح السجل"), nil
	case "addUser", "setPrivilege", "setPin", "deleteUser", "deleteFingers", "clearAdmins":
		return mockUserAction(action, p)
	case "setTime":
		s, _ := anyStr(p["time"])
		return map[string]any{"message": "(تجريبي) تم ضبط وقت الجهاز على " + strings.Replace(s, "T", " ", 1), "deviceTime": now, "pcTime": now}, nil
	case "readOption":
		key, err := cleanOptionKey(p)
		if err != nil {
			return nil, err
		}
		mockMu.Lock()
		v, ok := mockOptions[key]
		mockMu.Unlock()
		if !ok {
			return map[string]any{"key": key, "value": nil, "message": "(تجريبي) الجهاز ما رجّع قيمة للإعداد " + key}, nil
		}
		return map[string]any{"key": key, "value": v, "message": "(تجريبي) " + key + " = " + v}, nil
	case "writeOption":
		key, err := cleanOptionKey(p)
		if err != nil {
			return nil, err
		}
		v, _ := anyStr(p["value"])
		mockMu.Lock()
		mockOptions[key] = strings.TrimSpace(v)
		mockMu.Unlock()
		return map[string]any{"key": key, "message": fmt.Sprintf("(تجريبي) انكتب الإعداد %s = %s", key, strings.TrimSpace(v))}, nil
	case "clearAllData":
		return msg("(تجريبي) هنا ينمسح كل شي بالجهاز بعد حفظ نسخة. بالتجربة ما انمسح شي"), nil
	case "clearOplog", "clearAccess", "restoreUsers":
		return msg("(تجريبي) تم تنفيذ الأمر"), nil
	case "deviceSecurityCheck":
		return mockDeviceScan(), nil
	case "setDeviceCommKey":
		change, _ := p["change"].(bool)
		n, err := parseNewCommKey(p["password"], change)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return map[string]any{"applied": true, "key": 0, "disabled": true,
				"message": "(تجريبي) انعطّلت كلمة سر اتصال الجهاز."}, nil
		}
		verb := "ضبط"
		if change {
			verb = "تغيير"
		}
		return map[string]any{"applied": true, "key": n,
			"message": "(تجريبي) تم " + verb + " كلمة سر الاتصال بالجهاز وحفظها بالبرنامج."}, nil
	}
	if _, ok := actions[action]; !ok {
		return nil, errors.New("أمر غير معروف")
	}
	return map[string]any{"message": "(تجريبي) تم تنفيذ: " + action, "deviceTime": now}, nil
}

func mockUserJSON(u *mockUser) map[string]any {
	return map[string]any{"uid": u.UID, "userId": u.UserID, "name": u.Name, "cardno": u.Card, "role": u.Role, "hasPassword": u.HasPass}
}

// أوامر موظفي الجهاز بوضع التجربة: تغير القائمة الوهمية حتى تبين النتيجة بالتقارير
func mockUserAction(action string, p map[string]any) (map[string]any, error) {
	mockMu.Lock()
	defer mockMu.Unlock()
	if action == "clearAdmins" {
		for _, u := range mockUsers {
			u.Role = 0
		}
		return msg("(تجريبي) انلغت صلاحية المدير من كل الموظفين"), nil
	}
	if action == "addUser" {
		id, _ := anyStr(p["userId"])
		id = trimSpace(id)
		name, _ := anyStr(p["name"])
		name = trimSpace(name)
		if !isDigits(id) {
			return nil, badReq("رقم الموظف لازم يكون أرقام فقط")
		}
		if name == "" {
			return nil, badReq("اكتب اسم الموظف")
		}
		if p["privilege"] == nil || p["privilege"] == "" {
			p["privilege"] = privUser
		}
		priv, err := paramPriv(p)
		if err != nil {
			return nil, err
		}
		pin, err := paramPin(p)
		if err != nil {
			return nil, err
		}
		maxUID := 0
		for _, u := range mockUsers {
			if u.UserID == id {
				return nil, badReq("رقم الموظف " + id + " موجود بالجهاز")
			}
			maxUID = max(maxUID, u.UID)
		}
		card, _ := anyStr(p["card"])
		cn := anyInt(card, 0)
		u := &mockUser{UID: maxUID + 1, UserID: id, Name: name, Card: uint32(max(0, cn)), Role: priv, HasPass: pin != ""}
		mockUsers = append(mockUsers, u)
		return map[string]any{"message": fmt.Sprintf("(تجريبي) انضاف الموظف رقم %s (%s)", id, name), "user": mockUserJSON(u)}, nil
	}
	uid := anyInt(p["uid"], -1)
	userID, _ := anyStr(p["userId"])
	idx := -1
	for i, u := range mockUsers {
		if u.UID == uid && u.UserID == userID {
			idx = i
		}
	}
	if idx < 0 {
		return nil, badReq("بيانات هذا الموظف تغيرت، اسحب البيانات من جديد")
	}
	u := mockUsers[idx]
	switch action {
	case "setPrivilege":
		priv, err := paramPriv(p)
		if err != nil {
			return nil, err
		}
		u.Role = priv
		return map[string]any{"message": "(تجريبي) صارت الصلاحية: " + privLabel(priv), "user": mockUserJSON(u)}, nil
	case "setPin":
		pin, err := paramPin(p)
		if err != nil {
			return nil, err
		}
		u.HasPass = pin != ""
		return map[string]any{"message": "(تجريبي) تغير الرقم السري", "user": mockUserJSON(u)}, nil
	case "deleteUser":
		mockUsers = append(mockUsers[:idx:idx], mockUsers[idx+1:]...)
		return msg(fmt.Sprintf("(تجريبي) انحذف الموظف رقم %s (%s)", u.UserID, u.Name)), nil
	}
	return msg("(تجريبي) انحذفت بصمات الأصابع للموظف رقم " + u.UserID), nil
}

// فحص أمان الجهاز بوضع التجربة: نتيجة واقعية فيها مخاطر حتى تبين شكل التقرير
func mockDeviceScan() map[string]any {
	f := func(id, title, sev, detail, fix string) map[string]any {
		return map[string]any{"id": id, "title": title, "severity": sev, "detail": detail, "fix": fix}
	}
	findings := []map[string]any{
		f("commkey", "كلمة سر الاتصال (Comm Key)", "critical",
			"(تجريبي) الجهاز يقبل الاتصال بدون كلمة سر اتصال. أي جهاز بالشبكة يقدر يقرا ويعدّل ويمسح.",
			"عيّن كلمة سر اتصال من قائمة الجهاز واكتبها بالبرنامج."),
		f("deviceadmin", "مدير على الجهاز", "high",
			"(تجريبي) ماكو مدير على الجهاز، فأي شخص يفتح القائمة ويغيّر الإعدادات.",
			"سجّل موظفاً بصلاحية مدير على الجهاز."),
		f("network", "تعرّض الجهاز على الشبكة", "medium",
			"(تجريبي) منفذ 4370 مفتوح والبروتوكول غير مشفّر.",
			"اعزل الجهاز على شبكة إدارة وحجب المنفذ عن غير السيرفر."),
		f("attlog", "سجل البصمات بالجهاز", "info",
			"(تجريبي) يُمسح كاملاً بأمر واحد، لكن ما يُعدّل سجل مفرد.",
			"فعّل نسخاً احتياطية دورية."),
		f("firmware", "البرنامج الثابت للجهاز", "info",
			"(تجريبي) إصدار البرنامج الثابت: Ver 6.60. تابع تحديثات المورّد.", "تابع تحديثات ZKTeco."),
	}
	return map[string]any{
		"message":  "(تجريبي) فحص الجهاز خلص. لگينا 3 نقاط تحتاج انتباه (أعلاها: حرجة).",
		"device":   map[string]any{"name": "iFace1000", "serial": "DEMO123456789", "platform": "ZMM220_TFT", "firmware": "Ver 6.60"},
		"risk":     "critical",
		"findings": findings,
		"counts":   map[string]int{"critical": 1, "high": 1, "medium": 1, "info": 2},
		"scanTime": localISO(time.Now()),
	}
}
