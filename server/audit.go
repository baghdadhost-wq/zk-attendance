package server

// سجل العمليات: مين سوّى شنو ومتى ومن أي جهاز. ينحفظ بملف data/audit.log (سطر JSON لكل عملية)
// وما ينكتب بيه أي كلمة سر أو رقم سري.
//
// كل سطر مربوط بسلسلة سلامة (hash chain): كل قيد يحمل رقماً متسلسلاً seq وبصمة h محسوبة
// من بصمة القيد السابق + محتوى القيد. فأي حذف أو تعديل أو إعادة ترتيب لقيد قديم يكسر السلسلة
// ويُكتشف بأمر التحقق (--verify-audit). ملاحظة: من يملك صلاحية الكتابة على الملف يقدر يعيد
// بناء السلسلة كاملة أو يقص آخرها؛ للحماية الكاملة صدّر السجل لمكان "إلحاق فقط" أو جهاز آخر.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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

const auditMaxSize = 5 << 20

var (
	auditMu     sync.Mutex
	auditInit   bool
	auditPrev   string // بصمة آخر قيد (سلسلة السلامة)
	auditSeq    uint64 // الرقم المتسلسل لآخر قيد
	auditMirror string // مسار اختياري "إلحاق فقط" يُنسخ إليه كل قيد (للحماية خارج الجهاز)
)

// SetAuditMirror يحدد ملفاً إضافياً (يُفضّل على قرص/مجلد "إلحاق فقط") يُلحق إليه كل قيد تدقيق
func SetAuditMirror(path string) { auditMirror = path }

func auditFile() string {
	name := "audit.log"
	if demoMode {
		name = "audit-demo.log"
	}
	return filepath.Join(dataDir, "data", name)
}

// hashEntry يحسب بصمة قيد من بصمة سابقة + تمثيل القيد (بدون حقل h)
func hashEntry(prev string, e map[string]any) string {
	cp := make(map[string]any, len(e))
	for k, v := range e {
		if k != "h" {
			cp[k] = v
		}
	}
	b, _ := json.Marshal(cp) // Go يرتّب مفاتيح الخريطة، فالتمثيل ثابت
	sum := sha256.Sum256(append([]byte(prev+"\n"), b...))
	return hex.EncodeToString(sum[:])
}

// يسترجع seq وبصمة آخر قيد من آخر سطر بالملف الحالي أو المدوّر، عند أول كتابة بعد التشغيل
func loadAuditTailLocked() {
	if auditInit {
		return
	}
	auditInit = true
	last := ""
	for _, p := range []string{auditFile() + ".1", auditFile()} {
		if lines := readAuditLines(p); len(lines) > 0 {
			last = lines[len(lines)-1]
		}
	}
	if last != "" {
		var e map[string]any
		if json.Unmarshal([]byte(last), &e) == nil {
			if h, ok := e["h"].(string); ok {
				auditPrev = h
			}
			if s, ok := e["seq"].(float64); ok {
				auditSeq = uint64(s)
			}
		}
	}
}

func audit(r *http.Request, action string, detail map[string]any, err error) {
	u, _ := userOf(r)
	auditAs(r, u.Name, u.Role, action, detail, err)
}

func auditAs(r *http.Request, user, role, action string, detail map[string]any, err error) {
	e := map[string]any{"t": localISO(time.Now()), "user": user, "role": role, "ip": clientIP(r), "action": action}
	for k, v := range detail {
		if v != nil && v != "" {
			e[k] = v
		}
	}
	e["ok"] = err == nil
	if err != nil {
		e["error"] = err.Error()
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	loadAuditTailLocked()
	auditSeq++
	e["seq"] = auditSeq
	e["h"] = hashEntry(auditPrev, e)
	b, _ := json.Marshal(e)
	f := auditFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		log.Println("audit:", err)
		auditSeq-- // ما انكتب، نرجّع العدّاد
		return
	}
	if st, err := os.Stat(f); err == nil && st.Size() > auditMaxSize {
		os.Remove(f + ".1")
		os.Rename(f, f+".1")
	}
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Println("audit:", err)
		auditSeq--
		return
	}
	line := append(b, '\n')
	fh.Write(line)
	fh.Close()
	auditPrev = e["h"].(string)
	if auditMirror != "" {
		if mf, err := os.OpenFile(auditMirror, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			mf.Write(line)
			mf.Close()
		} else {
			log.Println("audit mirror:", err)
		}
	}
}

// VerifyAudit يتحقق من سلامة سلسلة سجل التدقيق. يرجّع عدد القيود ووصف أول كسر إن وُجد.
func VerifyAudit() (count int, ok bool, detail string) {
	auditMu.Lock()
	lines := append(readAuditLines(auditFile()+".1"), readAuditLines(auditFile())...)
	auditMu.Unlock()
	prev := ""
	var lastSeq uint64
	for i, ln := range lines {
		var e map[string]any
		if json.Unmarshal([]byte(ln), &e) != nil {
			return i, false, fmt.Sprintf("السطر %d غير صالح (JSON تالف)", i+1)
		}
		h, _ := e["h"].(string)
		if h == "" {
			return i, false, fmt.Sprintf("السطر %d بلا بصمة سلامة (قيد قديم قبل تفعيل السلسلة أو محقون)", i+1)
		}
		if got := hashEntry(prev, e); got != h {
			return i, false, fmt.Sprintf("كُسرت السلسلة عند القيد رقم %v (السطر %d): القيد عُدّل أو حُذف قيد قبله", e["seq"], i+1)
		}
		if sq, ok := e["seq"].(float64); ok {
			if i > 0 && uint64(sq) != lastSeq+1 {
				return i, false, fmt.Sprintf("فجوة بالترقيم عند السطر %d: متوقع %d ووُجد %v (حُذفت قيود)", i+1, lastSeq+1, e["seq"])
			}
			lastSeq = uint64(sq)
		}
		prev = h
	}
	return len(lines), true, fmt.Sprintf("سلسلة سجل التدقيق سليمة: %d قيد متصل بلا كسر", len(lines))
}

func readAuditLines(path string) []string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// آخر العمليات، الأحدث أول
func handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := min(2000, max(1, anyInt(r.URL.Query().Get("limit"), 300)))
	auditMu.Lock()
	lines := append(readAuditLines(auditFile()+".1"), readAuditLines(auditFile())...)
	auditMu.Unlock()
	out := make([]map[string]any, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		var e map[string]any
		if json.Unmarshal([]byte(lines[i]), &e) == nil {
			out = append(out, e)
		}
	}
	writeJSON(w, 200, map[string]any{"entries": out, "total": len(lines)})
}

// معلومات آمنة من طلب الأمر للسجل: بدون رقم سري ولا عبارة التأكيد، وقيمة الإعدادات السرية مخفية
func safeParams(action string, p map[string]any) map[string]any {
	keep := []string{"uid", "userId", "name", "card", "privilege", "seconds", "index", "text", "key", "value", "time", "finger", "file"}
	out := map[string]any{}
	for _, k := range keep {
		v, ok := p[k]
		if !ok || v == nil || v == "" {
			continue
		}
		if s, ok := v.(string); ok {
			if r := []rune(s); len(r) > 80 {
				v = string(r[:80]) + "…"
			}
		}
		out[k] = v
	}
	if action == "writeOption" || action == "readOption" {
		if k, _ := anyStr(p["key"]); isSecretOption(k) {
			if _, ok := out["value"]; ok {
				out["value"] = "***"
			}
		}
	}
	if action == "setPin" {
		pin, _ := anyStr(p["pin"])
		out["cleared"] = strings.TrimSpace(pin) == ""
	}
	return out
}

func isSecretOption(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "key") || strings.Contains(k, "pass") || strings.Contains(k, "pwd")
}
