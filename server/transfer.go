package server

// نقل كل بيانات البرنامج لجهاز ثاني على نفس الشبكة (حسابات، بيانات الموظفين ودوامهم، كل الإعدادات،
// التقارير المحفوظة، البصمات اليدوية، الإجازات).
//
// المستلم يشغّل «وضع الاستلام» (الصلاحية المطلقة) فيطلع رابطه ورمز عشوائي من 10 حروف يبقى 15 دقيقة.
// المرسل يكتب الرابط والرمز. البيانات تنضغط وتتشفّر AES-256-GCM بمفتاح من الرمز (PBKDF2-SHA256)،
// فإذا أحد سحب الملف بالطريق ما يگدر يقراه بدون الرمز، والرمز ما ينرسل أبداً.
// 5 محاولات برمز غلط تطفي وضع الاستلام. قبل ما تنستبدل البيانات تنحفظ نسخة منها بـ backups.
// سجل العمليات ما ينتقل: كل جهاز يبقى إله سجله.

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	xferAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	xferCodeLen  = 10
	xferIter     = 300000
	xferTTL      = 15 * time.Minute
	xferMaxBody  = 64 << 20
	xferMaxFile  = 32 << 20
	xferMaxFiles = 5000
	xferMaxTries = 5
	xferMagic    = "ZKXFER1"
)

var xfer struct {
	sync.Mutex
	code  string
	until time.Time
	tries int
	last  string // وقت آخر استلام
}

// activeAuth الخادم الشغال: بعد ما تنستبدل البيانات تنقرا الحسابات من جديد وتطلع كل الجلسات
var (
	activeMu   sync.Mutex
	activeAuth *authServer
)

func (a *authServer) dataReplaced() {
	a.store.mu.Lock()
	a.store.mod = time.Time{}
	a.store.refreshLocked()
	a.store.mu.Unlock()
	a.sess.mu.Lock()
	a.sess.m = map[string]sessEntry{}
	a.sess.mu.Unlock()
}

func normXferCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(toASCIIDigits(s)) {
		if strings.ContainsRune(xferAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fmtXferCode(c string) string {
	if len(c) != xferCodeLen {
		return c
	}
	return c[:4] + "-" + c[4:8] + "-" + c[8:]
}

func newXferCode() string {
	b := make([]byte, xferCodeLen)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(xferAlphabet))))
		b[i] = xferAlphabet[n.Int64()]
	}
	return string(b)
}

func xferAEAD(code string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, code, salt, xferIter, 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func sealXfer(code string, plain []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	g, err := xferAEAD(code, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(xferMagic), salt...)
	out = append(out, nonce...)
	return g.Seal(out, nonce, plain, []byte(xferMagic)), nil
}

var errXferCode = errors.New("bad code")

func openXfer(code string, box []byte) ([]byte, error) {
	if len(box) < len(xferMagic)+16+12+16 || string(box[:len(xferMagic)]) != xferMagic {
		return nil, badReq("الملف المنقول مو من برنامج الحضور أو ناقص")
	}
	salt := box[len(xferMagic) : len(xferMagic)+16]
	g, err := xferAEAD(code, salt)
	if err != nil {
		return nil, err
	}
	rest := box[len(xferMagic)+16:]
	plain, err := g.Open(nil, rest[:g.NonceSize()], rest[g.NonceSize():], []byte(xferMagic))
	if err != nil {
		return nil, errXferCode
	}
	return plain, nil
}

// skipXfer ملفات ما تنتقل: سجل العمليات (لكل جهاز سجله)، النسخ الاحتياطية للملفات، والملفات المؤقتة
func skipXfer(rel string) bool {
	base := path.Base(rel)
	return strings.HasPrefix(base, "audit.log") || strings.HasSuffix(base, ".bak") || strings.HasSuffix(base, ".tmp") ||
		strings.HasPrefix(base, ".") || base == "sessions.json"
}

func dataRoot() string { return filepath.Join(dataDir, "data") }

// packData يضغط ملفات البيانات (all = كلشي، للنسخة الاحتياطية قبل الاستبدال)
func packData(all bool) ([]byte, int, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n := 0
	root := dataRoot()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !all && skipXfer(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := zw.Create(rel)
		if err != nil {
			return err
		}
		n++
		_, err = f.Write(b)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), n, nil
}

type xferFile struct {
	rel  string
	data []byte
}

// unpackData يفتح الأرشيف ويتأكد منه: مسارات سليمة، وملف حسابات صالح بيه صلاحية مطلقة
func unpackData(b []byte) ([]xferFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, badReq("الملف المنقول تالف")
	}
	if len(zr.File) > xferMaxFiles {
		return nil, badReq("الملف المنقول بيه ملفات هواية")
	}
	var files []xferFile
	var total int64
	hasUsers := false
	for _, f := range zr.File {
		rel := f.Name
		if rel == "" || strings.Contains(rel, "\\") || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") ||
			strings.HasSuffix(rel, "/") || skipXfer(rel) {
			continue
		}
		if f.UncompressedSize64 > xferMaxFile {
			return nil, badReq("ملف «" + rel + "» كبير هواية")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, badReq("الملف المنقول تالف")
		}
		data, err := io.ReadAll(io.LimitReader(rc, xferMaxFile+1))
		rc.Close()
		if err != nil || int64(len(data)) > xferMaxFile {
			return nil, badReq("الملف المنقول تالف")
		}
		total += int64(len(data))
		if total > 4*xferMaxBody {
			return nil, badReq("البيانات المنقولة كبيرة هواية")
		}
		if rel == "users.json" {
			users, _, err := parseUsers(data)
			if err != nil {
				return nil, badReq("ملف الحسابات المنقول تالف")
			}
			for _, u := range users {
				if u.Role == RoleSuper {
					hasUsers = true
				}
			}
		}
		files = append(files, xferFile{rel, data})
	}
	if !hasUsers {
		return nil, badReq("البيانات المنقولة ما بيها حساب صلاحية مطلقة")
	}
	return files, nil
}

// applyData يحفظ نسخة من البيانات الحالية، ويمسحها (إلا سجل العمليات)، ويكتب الجديدة
func applyData(files []xferFile) (string, error) {
	backup, _, err := packData(true)
	if err != nil {
		return "", err
	}
	bdir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(bdir, 0o700); err != nil {
		return "", err
	}
	bname := "before-transfer-" + time.Now().Format("20060102-150405") + ".zip"
	if err := os.WriteFile(filepath.Join(bdir, bname), backup, 0o600); err != nil {
		return "", err
	}
	root := dataRoot()
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "audit.log") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return bname, err
		}
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return bname, err
		}
		if err := os.WriteFile(p+".tmp", f.data, 0o600); err != nil {
			return bname, err
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			return bname, err
		}
	}
	setMu.Lock()
	setCache = nil
	setMu.Unlock()
	activeMu.Lock()
	a := activeAuth
	activeMu.Unlock()
	if a != nil {
		a.dataReplaced()
	}
	return bname, nil
}

func xferReceiving() (string, bool) {
	xfer.Lock()
	defer xfer.Unlock()
	if xfer.code == "" || time.Now().After(xfer.until) {
		xfer.code = ""
		return "", false
	}
	return xfer.code, true
}

// POST /api/transfer/receive {on} — يشغّل أو يطفي وضع الاستلام
func handleTransferReceive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST"})
		return
	}
	if demoMode {
		writeErr(w, badReq("وضع التجربة ما ينقل بيانات"), "")
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := readJSONBody(r, 1024, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	if !req.On {
		xfer.Lock()
		xfer.code = ""
		xfer.Unlock()
		writeJSON(w, 200, map[string]any{"receiving": false})
		return
	}
	urls := ShareURLs()
	if len(urls) == 0 {
		share.Lock()
		mode := share.mode
		share.Unlock()
		msg := "ما لگيت شبكة. وصّل الجهاز بالواي فاي وجرب مرة ثانية"
		if mode != ShareNetwork && mode != SharePhone {
			msg = "البرنامج شغال على هذي الحاسبة بس، والأجهزة الثانية ما توصله. سدّه وشغّله بوضع الشبكة (start-network.bat) حتى يستلم"
		}
		writeErr(w, badReq(msg), "")
		return
	}
	code := newXferCode()
	xfer.Lock()
	xfer.code, xfer.until, xfer.tries = code, time.Now().Add(xferTTL), 0
	xfer.Unlock()
	audit(r, "transferReceive", nil, nil)
	writeJSON(w, 200, map[string]any{"receiving": true, "code": fmtXferCode(code), "urls": urls, "seconds": int(xferTTL.Seconds())})
}

// GET /api/transfer/status
func handleTransferStatus(w http.ResponseWriter, r *http.Request) {
	_, on := xferReceiving()
	xfer.Lock()
	left, last := int(time.Until(xfer.until).Seconds()), xfer.last
	xfer.Unlock()
	if !on {
		left = 0
	}
	writeJSON(w, 200, map[string]any{"receiving": on, "seconds": left, "last": last})
}

// POST /api/transfer/push — يوصل من الجهاز المرسل (بدون تسجيل دخول: الرمز هو الحماية)
func handleTransferPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST"})
		return
	}
	code, on := xferReceiving()
	if !on {
		writeJSON(w, 409, map[string]any{"error": "الجهاز الثاني مو بوضع الاستلام، أو خلص وقته. شغّل «استلام» عليه من جديد"})
		return
	}
	box, err := io.ReadAll(io.LimitReader(r.Body, xferMaxBody+1))
	if err != nil || len(box) > xferMaxBody {
		writeJSON(w, 413, map[string]any{"error": "البيانات كبيرة هواية"})
		return
	}
	xfer.Lock()
	defer xfer.Unlock()
	if xfer.code != code { // انطفى بالنص
		writeJSON(w, 409, map[string]any{"error": "الجهاز الثاني طفّى وضع الاستلام"})
		return
	}
	plain, err := openXfer(code, box)
	if errors.Is(err, errXferCode) {
		xfer.tries++
		left := xferMaxTries - xfer.tries
		msg := fmt.Sprintf("الرمز غلط. باقي %d محاولات", left)
		if left <= 0 {
			xfer.code = ""
			msg = "الرمز غلط 5 مرات، فانطفى وضع الاستلام. شغّله من جديد ويطلع رمز جديد"
		}
		auditAs(r, "", "", "transferIn", map[string]any{"what": "رمز غلط"}, errors.New("bad code"))
		writeJSON(w, 403, map[string]any{"error": msg})
		return
	}
	if err != nil {
		writeErr(w, err, "")
		return
	}
	files, err := unpackData(plain)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	bname, err := applyData(files)
	xfer.code = ""
	if err != nil {
		auditAs(r, "", "", "transferIn", map[string]any{"backup": bname}, err)
		writeErr(w, err, "ما گدرت أحفظ البيانات: ")
		return
	}
	xfer.last = localISO(time.Now())
	auditAs(r, "", "", "transferIn", map[string]any{"what": fmt.Sprintf("%d ملف", len(files)), "backup": bname}, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "files": len(files)})
}

// xferTarget رابط المستلم: IP بالشبكة المحلية (مع المنفذ، والافتراضي 3000)
func xferTarget(s string) (string, error) {
	s = strings.TrimSpace(toASCIIDigits(s))
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return "", badReq("اكتب رابط الجهاز المستلم مثل 192.168.1.20:3000")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return "", badReq("اكتب IP الجهاز المستلم بالشبكة المحلية (مثل 192.168.1.20:3000) مثل ما يطلع عنده")
	}
	port := 3000
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", badReq("المنفذ غلط")
		}
		port = n
	}
	scheme := "http"
	if u.Scheme == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/transfer/push", scheme, net.JoinHostPort(ip.String(), strconv.Itoa(port))), nil
}

// POST /api/transfer/send {target, code}
func handleTransferSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST"})
		return
	}
	if demoMode {
		writeErr(w, badReq("وضع التجربة ما ينقل بيانات"), "")
		return
	}
	var req struct {
		Target string `json:"target"`
		Code   string `json:"code"`
	}
	if err := readJSONBody(r, 4096, &req); err != nil {
		writeErr(w, err, "")
		return
	}
	code := normXferCode(req.Code)
	if len(code) != xferCodeLen {
		writeErr(w, badReq("اكتب الرمز اللي طالع بالجهاز المستلم (10 حروف وأرقام)"), "")
		return
	}
	target, err := xferTarget(req.Target)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	plain, n, err := packData(false)
	if err != nil {
		writeErr(w, err, "ما گدرت أجهّز البيانات: ")
		return
	}
	box, err := sealXfer(code, plain)
	if err != nil {
		writeErr(w, err, "")
		return
	}
	if len(box) > xferMaxBody {
		writeErr(w, badReq("البيانات كبيرة هواية للنقل"), "")
		return
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Post(target, "application/octet-stream", bytes.NewReader(box))
	if err != nil {
		audit(r, "transferOut", map[string]any{"to": req.Target}, err)
		writeErr(w, badReq("ما گدرت أوصل للجهاز المستلم. تأكد إنكم على نفس الواي فاي، وإن البرنامج مفتوح عليه، والرابط مكتوب صح"), "")
		return
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode != 200 {
		msg, _ := out["error"].(string)
		if msg == "" {
			msg = fmt.Sprintf("الجهاز المستلم رفض (%d)", resp.StatusCode)
		}
		audit(r, "transferOut", map[string]any{"to": req.Target}, errors.New(msg))
		writeJSON(w, 502, map[string]any{"error": msg})
		return
	}
	audit(r, "transferOut", map[string]any{"to": req.Target, "what": fmt.Sprintf("%d ملف", n)}, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "files": n, "bytes": len(box)})
}
