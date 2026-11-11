//go:build !windows

package main

import (
	"os"
	"os/exec"
)

func setupConsole() {}

// نخفي كلمة السر وقت الكتابة
func disableEcho() func() {
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if off.Run() != nil {
		return func() {}
	}
	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		on.Run()
	}
}

func pauseIfDoubleClicked() {}

// نافذة أوامر حقيقية؟ (/dev/null مثلاً مو نافذة حتى لو النظام يعتبره جهاز)
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	c := exec.Command("stty", "-g")
	c.Stdin = os.Stdin
	return c.Run() == nil
}
