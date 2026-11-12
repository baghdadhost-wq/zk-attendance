package server

// رابط البرنامج للأعضاء: البرنامج يعرف عناوينه بالشبكة، والواجهة تعرضها فوق حتى تنسخها وترسلها.
// العناوين تنحسب وقت الطلب، فإذا تغيرت الشبكة (الموبايل انتقل لواي فاي ثاني) يطلع الرابط الجديد.

import (
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const (
	ShareNetwork = "network" // حاسبة أو سيرفر مفتوح لأجهزة الشبكة
	ShareLocal   = "local"   // يسمع من هذي الحاسبة بس
	SharePhone   = "phone"   // تطبيق الموبايل: يشتغل كسيرفر على الواي فاي طول ما هو مفتوح
)

var share struct {
	sync.Mutex
	mode, scheme string
	port         int
}

// SetShareInfo يحدد طريقة التشغيل والمنفذ اللي تفتحه الأجهزة الثانية
func SetShareInfo(mode, scheme string, port int) {
	share.Lock()
	share.mode, share.scheme, share.port = mode, scheme, port
	share.Unlock()
}

// LanIPs عناوين هذا الجهاز بالشبكة، الأرجح يشتغل أولاً: الشبكة الأساسية (اللي يطلع منها الاتصال)،
// بعدها العناوين الخاصة العادية، وبالأخير الشبكات الوهمية (VirtualBox، Hyper-V، WSL، Docker) والـ VPN.
// شبكة الموبايل (pdp_ip) ما تنحسب لأن الأجهزة الثانية ما توصلها.
func LanIPs() []string {
	primary := ""
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil { // UDP ما يرسل شي، بس يختار الواجهة الأساسية
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
			primary = a.IP.String()
		}
		c.Close()
	}
	type cand struct {
		ip    string
		score int
	}
	var list []cand
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		name := strings.ToLower(ifc.Name)
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || strings.HasPrefix(name, "pdp_ip") {
			continue
		}
		virtual := false
		for _, w := range []string{"vethernet", "virtualbox", "vmware", "vmnet", "docker", "wsl", "hyper-v", "vbox", "br-", "veth",
			"tailscale", "zerotier", "utun", "ipsec", "awdl", "llw", "bluetooth"} {
			virtual = virtual || strings.Contains(name, w)
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			ip, score := ipn.IP.String(), 2
			switch {
			case virtual || !ipn.IP.IsPrivate():
				score = 3
			case ip == primary:
				score = 0
			default:
				score = 1
			}
			list = append(list, cand{ip, score})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score < list[j].score })
	seen := map[string]bool{}
	var ips []string
	for _, c := range list {
		if !seen[c.ip] {
			seen[c.ip] = true
			ips = append(ips, c.ip)
		}
	}
	return ips
}

// ShareURLs روابط البرنامج للأجهزة الثانية (فارغة إذا يسمع من هذا الجهاز بس)
func ShareURLs() []string {
	share.Lock()
	mode, scheme, port := share.mode, share.scheme, share.port
	share.Unlock()
	if mode != ShareNetwork && mode != SharePhone {
		return nil
	}
	if scheme == "" {
		scheme = "http"
	}
	var urls []string
	for _, ip := range LanIPs() {
		urls = append(urls, fmt.Sprintf("%s://%s:%d", scheme, ip, port))
	}
	return urls
}

func handleShare(w http.ResponseWriter, r *http.Request) {
	share.Lock()
	mode := share.mode
	share.Unlock()
	if mode == "" {
		mode = ShareLocal
	}
	hint := "شغّله ويا --network"
	if runtime.GOOS == "windows" {
		hint = "شغّله من start-network.bat"
	}
	urls := ShareURLs()
	if urls == nil {
		urls = []string{}
	}
	writeJSON(w, 200, map[string]any{"mode": mode, "urls": urls, "hint": hint})
}
