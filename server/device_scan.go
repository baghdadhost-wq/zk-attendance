package server

// فحص أمان جهاز البصمة: فحص قراءة فقط (ما يغيّر شي بالجهاز) يبيّن هل بيانات الجهاز محمية
// من التعديل غير المصرّح به. يجاوب عملياً على: "هل يقدر أحد يعدّل بيانات الجهاز؟"
//
// كل الفحوص عبر بروتوكول ZK القياسي وبأوامر قراءة فقط (نفس أوامر pyzk)، ولا يُكتب أي شي للجهاز.

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const scanTimeout = 6 * time.Second

// مُسجَّل حتى يمرّ فحص الوجود والصلاحية بـ handleCmd؛ التنفيذ الفعلي عبر scanDevice (مو هنا)
// لأنه يحتاج يفتح اتصالاً للفحص بلا مفتاح قبل الاتصال العادي، واتصالاً واحداً بالمرة.
func init() {
	stub := func(c *zkClient, p map[string]any) (map[string]any, error) {
		return nil, errors.New("internal: handled in handleCmd")
	}
	actions["deviceSecurityCheck"] = stub
	actions["setDeviceCommKey"] = stub
}

// يفحص هل الجهاز يقبل الاتصال بدون كلمة سر اتصال (Comm Key).
// keyless=true يعني الجهاز فتح بلا مصادقة → أي جهاز بالشبكة يقدر يتصل.
func probeKeyless(ip string, port int, timeout time.Duration) (keyless bool, reachable bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return false, false
	}
	defer conn.Close()
	c := &zkClient{conn: conn, r: bufio.NewReaderSize(conn, 1<<16), replyID: ushrtMax - 1, timeout: 2 * timeout}
	code, _, err := c.send(cmdConnect, nil)
	if err != nil {
		return false, true
	}
	if code == ackOK {
		c.send(cmdExit, nil)
		return true, true
	}
	return false, true // ackUnauth أو رفض → كلمة سر اتصال مطلوبة
}

type scanFinding struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"` // critical | high | medium | info | ok
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

var sevRank = map[string]int{"ok": 0, "info": 1, "medium": 2, "high": 3, "critical": 4}

// scanDevice يدير الاتصالات بالتسلسل (اتصال واحد بالمرة، مثل الجهاز الحقيقي):
// أولاً يجرّب اتصالاً بلا مفتاح لكشف كلمة سر الاتصال، وبعدها يتصل عادي للقراءات.
func scanDevice(ip string, port int, key uint32) (map[string]any, error) {
	devMu.Lock()
	defer devMu.Unlock()
	keyless, _ := probeKeyless(ip, port, scanTimeout)
	c, err := dialZK(ip, port, key, 10*time.Second)
	if err != nil {
		if errors.Is(err, errCommKey) {
			// كلمة سر اتصال مطلوبة لكن المكتوبة بالبرنامج غلط: الجهاز محمي، بس ما نگدر نكمل الباقي
			return map[string]any{
				"message": "الجهاز محمي بكلمة سر اتصال، بس كلمة السر المكتوبة بالبرنامج غلط فما گدرت أكمل باقي الفحص.",
				"risk":    "info",
				"findings": []scanFinding{{"commkey", "كلمة سر الاتصال (Comm Key)", "ok",
					"الجهاز محمي بكلمة سر اتصال.", "اكتب كلمة سر الاتصال الصحيحة بالبرنامج حتى يكمل الفحص باقي النقاط."}},
				"counts": map[string]int{"ok": 1}, "scanTime": localISO(time.Now()),
			}, nil
		}
		return nil, err
	}
	defer c.close()
	return buildScan(c, keyless), nil
}

func buildScan(c *zkClient, keyless bool) map[string]any {
	var findings []scanFinding
	add := func(id, title, sev, detail, fix string) {
		findings = append(findings, scanFinding{id, title, sev, detail, fix})
	}

	// 1) كلمة سر الاتصال (Comm Key)
	if keyless {
		add("commkey", "كلمة سر الاتصال (Comm Key)", "critical",
			"الجهاز يقبل الاتصال من أي جهاز على الشبكة بدون كلمة سر اتصال. أي شخص يوصل للجهاز يقدر يقرا ويعدّل الموظفين والبطاقات والأرقام السرية والصلاحيات، ويمسح سجل البصمات.",
			"عيّن كلمة سر اتصال من قائمة الجهاز (Comm/COMKEY أو «كلمة مرور الاتصال») واكتبها بالبرنامج بخانة كلمة سر الاتصال.")
	} else {
		add("commkey", "كلمة سر الاتصال (Comm Key)", "ok",
			"الجهاز محمي بكلمة سر اتصال؛ ما يقبل اتصالاً بدونها.", "")
	}

	// 2) مدير على الجهاز: لو ماكو، أي شخص واقف عند الجهاز يفتح القائمة ويغيّر الإعدادات
	admins := -1
	if users, err := c.getUsers(); err == nil {
		admins = countAdmins(users)
	}
	switch {
	case admins == 0:
		add("deviceadmin", "مدير على الجهاز", "high",
			"ماكو ولا مستخدم بصلاحية مدير على الجهاز. أي شخص واقف عند الجهاز يقدر يفتح القائمة ويغيّر الإعدادات أو يلغي كلمة سر الاتصال أو يسجّل بصمات.",
			"سجّل موظفاً واحداً على الأقل بصلاحية «مدير» على الجهاز (من «تعديل» الموظف ← الصلاحية بالجهاز: مدير).")
	case admins > 0:
		add("deviceadmin", "مدير على الجهاز", "ok",
			fmt.Sprintf("أكو %d مستخدم بصلاحية مدير على الجهاز، فقائمة الجهاز محمية.", admins), "")
	}

	// 3) الشبكة والتشفير (بطبيعة البروتوكول)
	add("network", "تعرّض الجهاز على الشبكة", "medium",
		"منفذ 4370 مفتوح لكل من يوصل للجهاز، وبروتوكول ZK غير مشفّر وتمويه كلمة سر الاتصال ضعيف. يعني الحماية الأساسية هي مين يوصل للجهاز بالشبكة.",
		"اعزل الجهاز على شبكة إدارة منفصلة أو VLAN، واحجب المنفذ 4370 عن غير سيرفر الحضور، ولا تعرّض الجهاز للإنترنت إطلاقاً.")

	// 4) حماية سجل البصمات
	add("attlog", "سجل البصمات بالجهاز", "info",
		"سجل البصمات يُقرأ أو يُمسح كاملاً بأمر واحد ممن يملك الوصول، لكن ما يُعدّل سجل مفرد ولا يُزوّر عبر البروتوكول (ما أكو أمر لذلك). فالخطر هو المسح الكامل مو التزوير الدقيق.",
		"فعّل سحباً دورياً للنسخ الاحتياطية بالبرنامج، وقيّد الوصول للشبكة. البرنامج يحفظ نسخة كاملة قبل أي مسح.")

	// 5) البرنامج الثابت (Firmware) — معلومة للمتابعة
	dev := map[string]any{}
	if v, err := c.cmd(cmdGetVersion, nil); err == nil {
		dev["firmware"] = strings.TrimRight(string(v), "\x00")
	}
	for key, field := range map[string]string{"~DeviceName": "name", "~SerialNumber": "serial", "~Platform": "platform"} {
		if v := c.readOption(key); v != nil {
			dev[field] = *v
		}
	}
	fw, _ := dev["firmware"].(string)
	add("firmware", "البرنامج الثابت للجهاز", "info",
		strings.TrimSpace("إصدار البرنامج الثابت: "+fw)+". الأجهزة القديمة ممكن بيها ثغرات معروفة.",
		"تابع تحديثات البرنامج الثابت من المورّد (ZKTeco) وثبّتها إذا توفّرت.")

	// ترتيب حسب الخطورة
	for i := 0; i < len(findings); i++ {
		for j := i + 1; j < len(findings); j++ {
			if sevRank[findings[j].Severity] > sevRank[findings[i].Severity] {
				findings[i], findings[j] = findings[j], findings[i]
			}
		}
	}
	counts := map[string]int{}
	worst := "ok"
	for _, f := range findings {
		counts[f.Severity]++
		if sevRank[f.Severity] > sevRank[worst] {
			worst = f.Severity
		}
	}
	risky := counts["critical"] + counts["high"] + counts["medium"]
	msg := "فحص الجهاز خلص. "
	if risky == 0 {
		msg += "ما لگينا مخاطر مهمة بإعدادات الجهاز."
	} else {
		msg += fmt.Sprintf("لگينا %d نقطة تحتاج انتباه (أعلاها: %s).", risky, sevLabel(worst))
	}
	return map[string]any{
		"message":  msg,
		"device":   dev,
		"risk":     worst,
		"findings": findings,
		"counts":   counts,
		"scanTime": localISO(time.Now()),
	}
}

func sevLabel(s string) string {
	return map[string]string{"critical": "حرجة", "high": "عالية", "medium": "متوسطة", "info": "معلومة", "ok": "سليم"}[s]
}

// ---------- ضبط كلمة سر اتصال الجهاز (Comm Key) إذا ما كانت موجودة ----------

// setDeviceCommKey يضبط أو يغيّر أو يعطّل كلمة سر اتصال الجهاز (Comm Key) — للصلاحية المطلقة فقط.
// يكتبها عبر خيار COMKey (نفس آلية كتابة الخيارات بـ pyzk) ويتحقق فعلياً من النتيجة.
//
//	جهاز مفتوح + change=false → يضبط كلمة سر جديدة (ما يبدّل موجودة).
//	جهاز عليه كلمة سر + change=true → يبدّلها (يستخدم الحالية currentKey للاتصال)، و newKey=0 يعطّلها.
func setDeviceCommKey(ip string, port int, currentKey, newKey uint32, change bool) (map[string]any, error) {
	devMu.Lock()
	defer devMu.Unlock()

	keyless, reachable := probeKeyless(ip, port, scanTimeout)
	if !reachable {
		if _, err := dialZK(ip, port, currentKey, scanTimeout); err != nil {
			return nil, fmt.Errorf("ما گدرت أتصل بالجهاز %s:%d%s", ip, port, connectHint)
		}
	}

	if keyless {
		// جهاز مفتوح: نضبط كلمة سر جديدة
		if newKey == 0 {
			return map[string]any{"already": false, "message": "الجهاز مفتوح أصلاً (بلا كلمة سر اتصال)، ما أكو شي أعطّله."}, nil
		}
		c, err := dialZK(ip, port, 0, 10*time.Second)
		if err != nil {
			return nil, err
		}
		werr := writeCommKey(c, newKey)
		c.close()
		if werr != nil {
			return nil, werr
		}
		return verifyCommKey(ip, port, newKey, "تم ضبط كلمة سر الاتصال بالجهاز")
	}

	// جهاز عليه كلمة سر اتصال
	if !change {
		return map[string]any{
			"already": true,
			"message": "الجهاز عليه كلمة سر اتصال. حتى تبدّلها، فعّل «بدّل كلمة السر الموجودة»، وتأكد إن الحالية مكتوبة بخانة «كلمة سر الاتصال».",
		}, nil
	}
	// نتصل بالمفتاح الحالي حتى نقدر نبدّله
	c, err := dialZK(ip, port, currentKey, 10*time.Second)
	if err != nil {
		if errors.Is(err, errCommKey) {
			return nil, badReq("كلمة سر الاتصال الحالية المكتوبة بالبرنامج غلط، فما گدرت أتأكد منها ولا بدّلت شي. اكتب الحالية الصحيحة بخانة «كلمة سر الاتصال» وحاول مرة ثانية.")
		}
		return nil, err
	}
	werr := writeCommKey(c, newKey)
	c.close()
	if werr != nil {
		return nil, werr
	}
	if newKey == 0 {
		// تعطيل: لازم الجهاز يصير مفتوح
		time.Sleep(600 * time.Millisecond)
		if nowKeyless, _ := probeKeyless(ip, port, scanTimeout); nowKeyless {
			return map[string]any{"applied": true, "key": 0, "disabled": true,
				"message": "انعطّلت كلمة سر اتصال الجهاز، وصار مفتوح. ⚠️ هذا ينقص الأمان — أي جهاز على الشبكة يقدر يتحكم بالجهاز. امسح كلمة السر من خانة البرنامج."}, nil
		}
		return map[string]any{"applied": false,
			"message": "حاولت أعطّل كلمة سر الاتصال بس الجهاز بعده يطلبها. ممكن يحتاج إعادة تشغيل، أو سوّيها من قائمة الجهاز."}, nil
	}
	return verifyCommKey(ip, port, newKey, "تم تغيير كلمة سر الاتصال بالجهاز")
}

func writeCommKey(c *zkClient, key uint32) error {
	if _, err := c.cmd(cmdOptionsWrq, []byte(fmt.Sprintf("COMKey=%d", key))); err != nil {
		return err
	}
	c.send(cmdRefreshOption, nil)
	return nil
}

// verifyCommKey يتأكد إن الجهاز صار يرفض الاتصال بلا مفتاح، وإن المفتاح الجديد يشتغل
func verifyCommKey(ip string, port int, newKey uint32, okPrefix string) (map[string]any, error) {
	time.Sleep(600 * time.Millisecond)
	if stillKeyless, _ := probeKeyless(ip, port, scanTimeout); stillKeyless {
		return map[string]any{
			"applied": false,
			"message": "كتبت كلمة سر الاتصال، بس الجهاز بعده يقبل اتصالاً بدونها. بعض الأجهزة ما تضبطها عبر الشبكة أو تحتاج إعادة تشغيل. سوّيها من قائمة الجهاز: Menu ← Comm ← Security (أو Comm Key)، وبعدها اكتبها بالبرنامج.",
		}, nil
	}
	c2, err := dialZK(ip, port, newKey, 10*time.Second)
	if err != nil {
		return map[string]any{"applied": true, "warn": true,
			"message": okPrefix + "، بس ما گدرت أتأكد من الاتصال بالمفتاح الجديد. إذا انقطع الاتصال اكتب المفتاح بالبرنامج أو سوّيه من قائمة الجهاز."}, nil
	}
	c2.close()
	return map[string]any{"applied": true, "key": newKey,
		"message": fmt.Sprintf("%s وتأكدت إنه يشتغل. حفظته بالبرنامج حتى يكمل الاتصال.\n⚠️ اكتب الرقم (%d) بمكان آمن — تحتاجه لو غيّرت الحاسبة أو أعدت ضبط الجهاز.", okPrefix, newKey)}, nil
}

// parseNewCommKey يتحقق من كلمة سر الاتصال الجديدة: أرقام فقط، 1..999999 (مدى أجهزة ZK)
func parseNewCommKey(v any, allowZero bool) (uint32, error) {
	s, _ := anyStr(v)
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, badReq("اكتب كلمة سر الاتصال (رقم)")
	}
	n, err := strconv.Atoi(s)
	lo := 1
	if allowZero {
		lo = 0
	}
	if err != nil || n < lo || n > 999999 {
		if allowZero {
			return 0, badReq("كلمة سر الاتصال لازم تكون رقم بين 0 و 999999 (0 = تعطيل)")
		}
		return 0, badReq("كلمة سر الاتصال لازم تكون رقم بين 1 و 999999")
	}
	return uint32(n), nil
}
