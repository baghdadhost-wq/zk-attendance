package server

// رابط البرنامج للأعضاء: البرنامج يعرف عناوينه بالشبكة، والواجهة تعرضها فوق حتى تنسخها وترسلها.

import (
	"net/http"
	"runtime"
	"sync"
)

const (
	ShareNetwork = "network" // مفتوح لأجهزة الشبكة: أكو روابط
	ShareLocal   = "local"   // يسمع من هذي الحاسبة بس
	ShareApp     = "app"     // تطبيق الموبايل: يشتغل داخل الموبايل بس
)

var share struct {
	sync.Mutex
	mode string
	urls []string
}

// SetShareInfo يحدد طريقة التشغيل وروابط البرنامج بالشبكة (الأول هو الأرجح يشتغل)
func SetShareInfo(mode string, urls []string) {
	share.Lock()
	share.mode, share.urls = mode, append([]string(nil), urls...)
	share.Unlock()
}

func handleShare(w http.ResponseWriter, r *http.Request) {
	share.Lock()
	mode, urls := share.mode, append([]string{}, share.urls...)
	share.Unlock()
	if mode == "" {
		mode = ShareLocal
	}
	hint := "شغّله ويا --network"
	if runtime.GOOS == "windows" {
		hint = "شغّله من start-network.bat"
	}
	writeJSON(w, 200, map[string]any{"mode": mode, "urls": urls, "hint": hint})
}
