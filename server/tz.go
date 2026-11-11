package server

import (
	"os/exec"
	"strings"
	"time"
)

// يقرأ المنطقة الزمنية من نظام أندرويد (getprop persist.sys.timezone)
func zoneFromGetprop(getprop string) (*time.Location, error) {
	out, err := exec.Command(getprop, "persist.sys.timezone").Output()
	if err != nil {
		return nil, err
	}
	return time.LoadLocation(strings.TrimSpace(string(out)))
}
