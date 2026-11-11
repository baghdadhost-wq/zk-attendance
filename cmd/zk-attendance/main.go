package main

// برنامج تقارير الحضور لأجهزة بصمة ZKTeco، ملف تنفيذي واحد بدون Node وبدون تنصيب.
//
//   تشغيل عادي (على نفس الحاسبة):   zk-attendance
//   للشبكة (يطلب كلمة سر):          zk-attendance --network
//   وضع التجربة بدون جهاز:           zk-attendance --demo

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
		network   = flag.Bool("network", os.Getenv("HOST") == "0.0.0.0", "افتح البرنامج لأجهزة الشبكة (يحتاج كلمة سر)")
		port      = flag.Int("port", envInt("PORT", 3000), "منفذ البرنامج")
		demo      = flag.Bool("demo", os.Getenv("MOCK") == "1", "وضع التجربة ببيانات وهمية")
		data      = flag.String("data", "", "مجلد حفظ البيانات (الافتراضي: جنب البرنامج)")
		setPass   = flag.Bool("set-password", false, "غيّر كلمة سر الدخول")
		password  = flag.String("password", os.Getenv("APP_PASSWORD"), "حدد كلمة سر الدخول بدون سؤال (للخدمات)")
		noBrowser = flag.Bool("no-browser", false, "لا تفتح المتصفح تلقائياً")
	)
	flag.Parse()
	server.Configure(chooseDataDir(*data), *demo)
	log.SetFlags(log.Ltime)

	fmt.Printf("ZK Attendance %s\n", server.Version)
	fmt.Println("Data folder / مجلد البيانات:", server.DataDir())

	var auth *server.AuthConfig
	if *network {
		var err error
		auth, err = ensurePassword(*setPass, *password)
		if err != nil {
			fatal(err.Error())
		}
	} else if *setPass {
		if _, err := ensurePassword(true, *password); err != nil {
			fatal(err.Error())
		}
		fmt.Println("Password saved. / انحفظت كلمة السر.")
		return
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

	srv := &http.Server{Handler: server.NewHandler(auth), ReadHeaderTimeout: 20 * time.Second}

	fmt.Println()
	if server.DemoMode() {
		fmt.Println("DEMO MODE (fake data) / وضع التجربة - بيانات وهمية")
	}
	fmt.Println("Running / البرنامج يعمل:", localURL)
	if *network {
		fmt.Println("Open from any device on the network / افتحه من أي جهاز بالشبكة:")
		for _, ip := range lanIPs() {
			fmt.Printf("   http://%s:%d\n", ip, *port)
		}
		fmt.Println("Log in with the password you chose. / ادخل بكلمة السر اللي اخترتها.")
	} else {
		fmt.Println("Keep this window open while using the program. / خلي هذي النافذة مفتوحة طول ما تستخدم البرنامج.")
		if !*noBrowser {
			go func() { time.Sleep(400 * time.Millisecond); openBrowser(localURL) }()
		}
	}
	fmt.Println()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

func lanIPs() []string {
	var ips []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				ips = append(ips, ipn.IP.String())
			}
		}
	}
	return ips
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

// ---------- كلمة سر الدخول ----------

func ensurePassword(change bool, given string) (*server.AuthConfig, error) {
	if given != "" {
		if len(given) < 6 {
			return nil, errors.New("password must be 6+ characters / كلمة السر لازم تكون 6 أحرف أو أكثر")
		}
		return server.SetPassword(given)
	}
	if a := server.LoadAuth(); a != nil && !change {
		return a, nil
	}
	if !stdinIsTerminal() {
		return nil, errors.New("no password set. Run once in a terminal, or use --password / ما أكو كلمة سر. شغّل البرنامج مرة بنافذة الأوامر حتى تختارها")
	}
	in := bufio.NewReader(os.Stdin)
	for {
		p1, err1 := readSecret(in, "Choose a login password (6+ chars) / اختار كلمة سر للدخول (6 أحرف أو أكثر): ")
		p2, err2 := readSecret(in, "Repeat it / أعد كتابتها: ")
		if err1 != nil || err2 != nil {
			return nil, errors.New("password input closed / انقطع إدخال كلمة السر")
		}
		if len(p1) >= 6 && p1 == p2 {
			a, err := server.SetPassword(p1) // كلمة سر جديدة تطلع كل الأجهزة المسجلة
			if err != nil {
				return nil, err
			}
			fmt.Println("Password saved. / انحفظت كلمة السر.")
			return a, nil
		}
		fmt.Println("Too short or not matching, try again. / قصيرة أو غير متطابقة، حاول مرة ثانية.")
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
