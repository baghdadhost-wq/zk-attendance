// Package mobile مدخل تطبيق الآيفون والآيباد (يُبنى بـ gomobile bind).
// التطبيق يشغّل البرنامج داخله ويعرضه بمتصفح مدمج، ويشتغل كسيرفر على الواي فاي:
// الأجهزة الثانية تفتحه برابط الموبايل (مثل http://192.168.1.20:3000) طول ما التطبيق مفتوح.
// مثل برنامج الحاسبة: ما أكو دخول بدون اسم وكلمة سر، وأول فتح (من الموبايل نفسه) ينشئ حساب الصلاحية المطلقة.
package mobile

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
	_ "time/tzdata" // iOS ما بيه قاعدة مناطق زمنية متاحة لـ Go

	"zkattendance/server"
)

// المنفذ نفسه مال برنامج الحاسبة، حتى الرابط يبقى ثابت. إذا محجوز نجرب اللي بعده
const firstPort = 3000

var (
	mu  sync.Mutex
	srv *http.Server
)

// Start يشغّل البرنامج ويرجع عنوانه داخل الموبايل. dataDir: مجلد Documents مال التطبيق.
// timeZone: المنطقة الزمنية للجهاز (مثل Asia/Baghdad)، لأن Go على iOS يعتبر الوقت المحلي UTC.
// إذا انطلب مرة ثانية (iOS وقف البرنامج بالخلفية) يسكّر القديم ويرجع على نفس المنفذ.
func Start(dataDir, timeZone string) (string, error) {
	if timeZone != "" {
		if loc, err := time.LoadLocation(timeZone); err == nil {
			time.Local = loc
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		srv.Close()
		srv = nil
	}
	server.Configure(dataDir, false)
	users, err := server.LoadUsers()
	if err != nil {
		return "", err
	}
	users.AllowWebSetup() // أول حساب ينعمل من الموبايل نفسه بس (127.0.0.1)، مو من الأجهزة الثانية
	server.SetConnectHint(". بالآيفون والآيباد: تأكد إن الجهاز على نفس الواي فاي، وإن التطبيق مسموحله بالشبكة المحلية (الإعدادات ← الحضور ← الشبكة المحلية)")
	var ln net.Listener
	for p := firstPort; p < firstPort+10 && ln == nil; p++ {
		ln, _ = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "0.0.0.0:0"); err != nil {
			return "", err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	server.SetShareInfo(server.SharePhone, "http", port)
	srv = &http.Server{Handler: server.NewHandler(users), ReadHeaderTimeout: 20 * time.Second}
	go srv.Serve(ln)
	return fmt.Sprintf("http://127.0.0.1:%d/", port), nil
}

// Version رقم نسخة البرنامج
func Version() string { return server.Version }
