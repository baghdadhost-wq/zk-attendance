// Package mobile مدخل تطبيق الآيفون والآيباد (يُبنى بـ gomobile bind).
// التطبيق يشغّل البرنامج داخله على 127.0.0.1 ويعرضه بمتصفح مدمج.
// مثل برنامج الحاسبة: ما أكو دخول بدون اسم وكلمة سر، وأول فتح ينشئ حساب الصلاحية المطلقة.
package mobile

import (
	"fmt"
	"net"
	"net/http"
	"time"
	_ "time/tzdata" // iOS ما بيه قاعدة مناطق زمنية متاحة لـ Go

	"zkattendance/server"
)

// Start يشغّل البرنامج ويرجع عنوانه. dataDir: مجلد Documents مال التطبيق.
// timeZone: المنطقة الزمنية للجهاز (مثل Asia/Baghdad)، لأن Go على iOS يعتبر الوقت المحلي UTC.
func Start(dataDir, timeZone string) (string, error) {
	if timeZone != "" {
		if loc, err := time.LoadLocation(timeZone); err == nil {
			time.Local = loc
		}
	}
	server.Configure(dataDir, false)
	users, err := server.LoadUsers()
	if err != nil {
		return "", err
	}
	users.AllowWebSetup() // البرنامج يسمع من الموبايل نفسه بس (127.0.0.1)
	server.SetConnectHint(". بالآيفون والآيباد: تأكد إن الجهاز على نفس الواي فاي، وإن التطبيق مسموحله بالشبكة المحلية (الإعدادات ← الحضور ← الشبكة المحلية)")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: server.NewHandler(users), ReadHeaderTimeout: 20 * time.Second}
	go srv.Serve(ln)
	return fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port), nil
}

// Version رقم نسخة البرنامج
func Version() string { return server.Version }
