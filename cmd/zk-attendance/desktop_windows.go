//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"zkattendance/server"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

const (
	swHide    = 0
	swRestore = 9
	runKey    = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	runName   = "ZKAttendance"
)

func consoleWindow() uintptr {
	h, _, _ := procGetConsoleWindow.Call()
	return h
}

// hideConsole يخفي نافذة البرنامج (البرنامج يبقى يشتغل)
func hideConsole() error {
	h := consoleWindow()
	if h == 0 {
		return errors.New("ما لگيت نافذة البرنامج")
	}
	procShowWindow.Call(h, swHide)
	return nil
}

func showConsole() {
	if h := consoleWindow(); h != 0 {
		procShowWindow.Call(h, swRestore)
		procSetForeground.Call(h)
	}
}

func regCmd(args ...string) *exec.Cmd {
	c := exec.Command("reg", args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}

// desktopHooks: التشغيل التلقائي يكتب بالسجل (للمستخدم الحالي) أمر تشغيل البرنامج مخفي بنفس إعداداته
func desktopHooks(startArgs []string) *server.Desktop {
	return &server.Desktop{
		Hide: hideConsole,
		Show: showConsole,
		AutoStart: func() bool {
			return regCmd("query", runKey, "/v", runName).Run() == nil
		},
		SetAutoStart: func(on bool) error {
			if !on {
				if regCmd("query", runKey, "/v", runName).Run() != nil {
					return nil
				}
				return regCmd("delete", runKey, "/v", runName, "/f").Run()
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				exe = real
			}
			line := `"` + exe + `"`
			for _, a := range append(startArgs, "--no-browser", "--hidden") {
				if strings.ContainsAny(a, " \t") {
					a = `"` + a + `"`
				}
				line += " " + a
			}
			return regCmd("add", runKey, "/v", runName, "/t", "REG_SZ", "/d", line, "/f").Run()
		},
	}
}
