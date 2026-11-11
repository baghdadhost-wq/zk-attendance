package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// نحاكي getprop مال أندرويد بسكربت يطبع المنطقة الزمنية
func TestZoneFromGetprop(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "getprop")
	os.WriteFile(fake, []byte("#!/bin/sh\necho Asia/Baghdad\n"), 0o755)
	loc, err := zoneFromGetprop(fake)
	if err != nil {
		t.Fatal(err)
	}
	_, off := time.Date(2026, 11, 11, 12, 0, 0, 0, loc).Zone()
	if loc.String() != "Asia/Baghdad" || off != 3*3600 {
		t.Fatalf("got %s offset %d", loc, off)
	}
	t.Logf("getprop -> %s (UTC%+d)", loc, off/3600)
}
