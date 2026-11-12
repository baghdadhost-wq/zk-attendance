package main

// برنامج تقارير الحضور لأجهزة بصمة ZKTeco، ملف تنفيذي واحد بدون Node وبدون تنصيب.
//
//   تشغيل عادي (على نفس الحاسبة):   zk-attendance
//   للشبكة:                          zk-attendance --network
//   وضع التجربة بدون جهاز:           zk-attendance --demo
//   نسيت كلمة سر الصلاحية المطلقة:   zk-attendance --reset-admin
//   حذف كل حسابات البرنامج:           zk-attendance --reset-users  (أول دخول بعدها يطلب إنشاء حساب الصلاحية المطلقة)
//
// الدخول دائماً باسم مستخدم وكلمة سر، وكل مستخدم إله صلاحية: مطلقة، مدير، مشرف، أساسي، أو موظف (يشوف تقريره بس).

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"zkattendance/server"
)

func main() {
	setupConsole()
	var (
		network     = flag.Bool("network", os.Getenv("HOST") == "0.0.0.0", "افتح البرنامج لأجهزة الشبكة")
		port        = flag.Int("port", envInt("PORT", 3000), "منفذ البرنامج")
		demo        = flag.Bool("demo", os.Getenv("MOCK") == "1", "وضع التجربة ببيانات وهمية")
		data        = flag.String("data", "", "مجلد حفظ البيانات (الافتراضي: جنب البرنامج)")
		resetAdm    = flag.Bool("reset-admin", false, "أنشئ حساب بصلاحية مطلقة أو غيّر كلمة سره (إذا نسيتها)")
		resetUsers  = flag.Bool("reset-users", false, "احذف كل حسابات البرنامج (بعد نسخة احتياطية). أول دخول بعدها يطلب إنشاء حساب الصلاحية المطلقة")
		setPass     = flag.Bool("set-password", false, "نفس --reset-admin (من النسخ السابقة)")
		adminUser   = flag.String("admin-user", "admin", "اسم حساب الصلاحية المطلقة ويا --password أو --reset-admin")
		password    = flag.String("password", os.Getenv("APP_PASSWORD"), "كلمة سر حساب الصلاحية المطلقة بدون سؤال (للخدمات)")
		noBrowser   = flag.Bool("no-browser", false, "لا تفتح المتصفح تلقائياً")
		verifyAudit = flag.Bool("verify-audit", false, "تحقق من سلامة سجل التدقيق (hash chain) واخرج")
		auditMirror = flag.String("audit-mirror", os.Getenv("AUDIT_MIRROR"), "انسخ كل قيد تدقيق لهذا الملف أيضاً (يُفضّل مجلد إلحاق-فقط أو جهاز آخر)")
		tlsCert     = flag.String("tls-cert", os.Getenv("TLS_CERT"), "ملف شهادة TLS لتشغيل البرنامج على https (يُنصح به بوضع الشبكة)")
		tlsKey      = flag.String("tls-key", os.Getenv("TLS_KEY"), "ملف مفتاح TLS")
	)
	flag.Parse()
	server.Configure(chooseDataDir(*data), *demo)
	log.SetFlags(log.Ltime)

	fmt.Printf("ZK Attendance %s\n", server.Version)
	fmt.Println("Data folder / مجلد البيانات:", server.DataDir())

	if *verifyAudit {
		n, ok, detail := server.VerifyAudit()
		fmt.Println(detail)
		if !ok {
			fmt.Fprintf(os.Stderr, "Audit log integrity CHECK FAILED / فشل فحص سلامة سجل التدقيق (%d قيد مفحوص)\n", n)
			os.Exit(1)
		}
		return
	}
	if *auditMirror != "" {
		server.SetAuditMirror(*auditMirror)
	}

	users, err := server.LoadUsers()
	if err != nil {
		fatal(err.Error())
	}
	if users.Migrated() {
		fmt.Println("Your old password now belongs to user \"admin\" (full permissions). / كلمة السر القديمة صارت لحساب admin (صلاحية مطلقة).")
	}
	if *resetUsers {
		backup, n, err := users.ResetAll()
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("Deleted all %d program users. Backup: %s\n", n, backup)
		fmt.Printf("انحذفت كل حسابات البرنامج (%d). نسختها: %s\n", n, backup)
		fmt.Println("Next start asks you to create the full-permission account. / أول ما تشغّل البرنامج يطلب إنشاء حساب الصلاحية المطلقة.")
		if !*network && *password == "" {
			return
		}
	}
	switch {
	case *resetAdm || *setPass:
		name := *adminUser
		if *password == "" && !flagGiven("admin-user") {
			name = "" // بالسؤال: ما نقترح "admin" لأنه صار اسم حساب المدير المحدود
		}
		if err := resetAdmin(users, name, *password); err != nil {
			fatal(err.Error())
		}
		if !*network {
			return
		}
	case *password != "":
		created, changed, err := users.EnsureAdmin(*adminUser, *password)
		if err != nil {
			fatal(err.Error())
		}
		if created {
			fmt.Printf("Full-permission account %q created. / انعمل حساب الصلاحية المطلقة %s.\n", *adminUser, *adminUser)
		} else if changed {
			fmt.Printf("Password of %q changed. / تغيرت كلمة سر %s.\n", *adminUser, *adminUser)
		}
	}
	webSetup := false
	if users.Count() == 0 {
		if *network {
			if err := createFirstAdmin(users); err != nil {
				fatal(err.Error())
			}
		} else {
			users.AllowWebSetup() // البرنامج يسمع من نفس الحاسبة بس، فنخلي إنشاء أول حساب من المتصفح
			webSetup = true
		}
	}

	host := "127.0.0.1"
	if *network {
		host = "0.0.0.0"
	}
	addr := net.JoinHostPort(host, fmt.Sprint(*port))
	ln, err := net.Listen("tcp", addr)
	localURL := fmt.Sprintf("http://localhost:%d", *port)
	if err != nil {
		if isAddrInUse(err) && !*network {
			fmt.Println("The program is already running. Opening it... / البرنامج شغال من قبل، أفتحه بالمتصفح...")
			openBrowser(localURL)
			time.Sleep(2 * time.Second)
			return
		}
		fatal(fmt.Sprintf("Cannot use port %d / ما گدرت أستخدم المنفذ %d: %v", *port, *port, err))
	}

	srv := &http.Server{Handler: server.NewHandler(users), ReadHeaderTimeout: 20 * time.Second}
	useTLS := *tlsCert != "" && *tlsKey != ""
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	localURL = fmt.Sprintf("%s://localhost:%d", scheme, *port)

	fmt.Println()
	if useTLS {
		fmt.Println("TLS enabled (https) / التشفير مفعّل")
	} else if *network {
		fmt.Println("Note: network mode over http is not encrypted. Use --tls-cert/--tls-key or a VPN. / تنبيه: وضع الشبكة على http غير مشفّر، استخدم شهادة TLS أو VPN.")
	}
	if server.DemoMode() {
		fmt.Println("DEMO MODE (fake data) / وضع التجربة - بيانات وهمية")
		fmt.Println("Demo accounts / حسابات التجربة: owner, admin, supervisor, viewer, employee  (password / كلمة السر: 123456)")
	}
	fmt.Println("Running / البرنامج يعمل:", localURL)
	if *network {
		server.SetShareInfo(server.ShareNetwork, scheme, *port)
		fmt.Println("Open from any device on the network / افتحه من أي جهاز بالشبكة:")
		for _, u := range server.ShareURLs() {
			fmt.Println("  ", u)
		}
		fmt.Println("Log in with your username and password. / ادخل باسم المستخدم وكلمة السر.")
		fmt.Println("The link is also at the top of the program, tap it to copy. / الرابط موجود فوق بالبرنامج، اضغطه حتى تنسخه.")
	} else {
		server.SetShareInfo(server.ShareLocal, scheme, *port)
		if webSetup {
			fmt.Println("First run: create the full-permission account in the browser. / أول تشغيل: أنشئ حساب الصلاحية المطلقة بالمتصفح.")
		}
		fmt.Println("Keep this window open while using the program. / خلي هذي النافذة مفتوحة طول ما تستخدم البرنامج.")
		if !*noBrowser {
			go func() { time.Sleep(400 * time.Millisecond); openBrowser(localURL) }()
		}
	}
	fmt.Println()
	serve := func() error {
		if useTLS {
			return srv.ServeTLS(ln, *tlsCert, *tlsKey)
		}
		return srv.Serve(ln)
	}
	if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err.Error())
	}
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil {
			return n
		}
	}
	return def
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "\nERROR / خطأ:", msg)
	pauseIfDoubleClicked()
	os.Exit(1)
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(strings.ToLower(err.Error()), "address already in use") ||
		strings.Contains(err.Error(), "Only one usage of each socket address")
}

// مجلد البيانات: جنب البرنامج، وإذا ما ينكتب بيه نستخدم مجلد المستخدم
func chooseDataDir(flagDir string) string {
	if flagDir != "" {
		os.MkdirAll(flagDir, 0o755)
		abs, _ := filepath.Abs(flagDir)
		return abs
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		test := filepath.Join(dir, ".write-test")
		if os.WriteFile(test, []byte("ok"), 0o644) == nil {
			os.Remove(test)
			return dir
		}
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		dir := filepath.Join(cfg, "zk-attendance")
		os.MkdirAll(dir, 0o755)
		return dir
	}
	return "."
}

func lookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", url)
	case lookPath("termux-open-url"): // أندرويد داخل Termux
		cmd = exec.Command("termux-open-url", url)
	case runtime.GOOS == "android":
		cmd = exec.Command("/system/bin/am", "start", "-a", "android.intent.action.VIEW", "-d", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

// flagGiven هل انكتب هذا الخيار صراحة بأمر التشغيل
func flagGiven(name string) bool {
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// ---------- حسابات الدخول ----------

func createFirstAdmin(users *server.UserStore) error {
	if !stdinIsTerminal() {
		return errors.New("no users yet. Run once in a terminal, or use --password / ما أكو مستخدمين. شغّل البرنامج مرة بنافذة الأوامر حتى تنشئ حساب الصلاحية المطلقة، أو استخدم --password")
	}
	fmt.Println()
	fmt.Println("Create the full-permission account / إنشاء حساب الصلاحية المطلقة (يدير كل شي ويضيف كل أنواع المستخدمين)")
	return promptAdmin(users, "")
}

func resetAdmin(users *server.UserStore, name, given string) error {
	if given != "" {
		if _, _, err := users.EnsureAdmin(name, given); err != nil {
			return err
		}
		fmt.Printf("Full-permission account %q saved. / انحفظ حساب الصلاحية المطلقة %s.\n", name, name)
		return nil
	}
	if !stdinIsTerminal() {
		return errors.New("run it in a terminal, or add --password / شغّله بنافذة الأوامر، أو أضف --password")
	}
	fmt.Println("Create a full-permission account, or give it a new password / إنشاء حساب بصلاحية مطلقة، أو كلمة سر جديدة إله")
	return promptAdmin(users, name)
}

func promptAdmin(users *server.UserStore, defName string) error {
	in := bufio.NewReader(os.Stdin)
	name := ""
	for {
		if defName != "" {
			fmt.Printf("Username [%s] / اسم المستخدم [%s]: ", defName, defName)
		} else {
			fmt.Print("Username / اسم المستخدم: ")
		}
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return errors.New("input closed / انقطع الإدخال")
		}
		if name = strings.TrimSpace(line); name == "" {
			name = defName
		}
		if err := server.CheckUsername(name); err != nil {
			fmt.Println(err.Error())
			continue
		}
		break
	}
	for {
		p1, err1 := readSecret(in, "Password (6+ chars) / كلمة السر (6 أحرف أو أكثر): ")
		p2, err2 := readSecret(in, "Repeat it / أعد كتابتها: ")
		if err1 != nil || err2 != nil {
			return errors.New("password input closed / انقطع إدخال كلمة السر")
		}
		if p1 != p2 {
			fmt.Println("Not matching, try again. / غير متطابقة، حاول مرة ثانية.")
			continue
		}
		if _, _, err := users.EnsureAdmin(name, p1); err != nil {
			fmt.Println(err.Error())
			continue
		}
		fmt.Printf("Saved. Log in as %q. / انحفظ. ادخل باسم %s.\n", name, name)
		return nil
	}
}

func readSecret(in *bufio.Reader, prompt string) (string, error) {
	restore := disableEcho() // نطفي العرض قبل ما يظهر السؤال، حتى ما تبين ولا حرف
	fmt.Print(prompt)
	line, err := in.ReadString('\n')
	restore()
	fmt.Println()
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
