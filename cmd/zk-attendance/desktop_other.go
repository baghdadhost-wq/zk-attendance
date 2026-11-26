//go:build !windows

package main

import "zkattendance/server"

// الإخفاء والتشغيل التلقائي لنسخة الوندوز بس
func hideConsole() error                              { return nil }
func showConsole()                                    {}
func desktopHooks(startArgs []string) *server.Desktop { return nil }
