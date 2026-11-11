//go:build android

package server

import (
	"os"
	"time"
	_ "time/tzdata" // قاعدة المناطق الزمنية داخل البرنامج، لأن مكانها يتغير بين نسخ أندرويد
)

// Go على أندرويد يعتبر الوقت المحلي UTC، فنقرأ المنطقة الزمنية من الموبايل نفسه
func init() {
	if tz := os.Getenv("TZ"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			time.Local = loc
			return
		}
	}
	if loc, err := zoneFromGetprop("/system/bin/getprop"); err == nil {
		time.Local = loc
	}
}
