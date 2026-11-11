//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procSetOutputCP    = kernel32.NewProc("SetConsoleOutputCP")
	procSetCP          = kernel32.NewProc("SetConsoleCP")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
	procGetProcessList = kernel32.NewProc("GetConsoleProcessList")
)

// نخلي نافذة الأوامر تعرض العربي (UTF-8)
func setupConsole() {
	procSetOutputCP.Call(65001)
	procSetCP.Call(65001)
}

// نافذة أوامر حقيقية؟
func stdinIsTerminal() bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(os.Stdin.Fd()), &mode) == nil
}

// نخفي كلمة السر وقت الكتابة
func disableEcho() func() {
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	const enableEchoInput = 0x0004
	procSetConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput))
	return func() { procSetConsoleMode.Call(uintptr(h), uintptr(mode)) }
}

// إذا البرنامج انفتح بدبل كلك (النافذة بيها عملية وحدة)، نخليها مفتوحة حتى يقرا الخطأ
func pauseIfDoubleClicked() {
	var ids [4]uint32
	r, _, _ := procGetProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	if r == 1 {
		fmt.Print("\nPress Enter to close / اضغط Enter حتى تسكر...")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
}
