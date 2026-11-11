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
	mockMu    sync.Mutex
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
	}
	if _, ok := actions[action]; !ok {
		return nil, errors.New("أمر غير معروف")
	}
	return map[string]any{"message": "(تجريبي) تم تنفيذ: " + action, "deviceTime": now}, nil
}
