//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

/*
 * Windows 控制台输出准备。
 *
 * x/term 的 MakeRaw 只管输入侧的 VT；输出侧如果要让老 conhost 解释 ANSI
 * 转义（颜色、备用屏、清屏），还得自己打开 ENABLE_VIRTUAL_TERMINAL_PROCESSING。
 * 顺带把输出代码页切到 UTF-8，保证中文按 UTF-8 字节写出去能正常显示。
 * Windows Terminal 本来就支持 VT，这里的设置是无害的。
 */

var (
	savedOutputCP uint32
	cpSaved       bool
)

// startConsole 打开 stdout 的 VT 处理并切到 UTF-8 输出代码页。
func startConsole() {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
	if cp, err := windows.GetConsoleOutputCP(); err == nil && cp != 65001 {
		savedOutputCP = cp
		cpSaved = true
		_ = windows.SetConsoleOutputCP(65001)
	}
}

// restoreConsole 还原输出代码页。
func restoreConsole() {
	if cpSaved {
		_ = windows.SetConsoleOutputCP(savedOutputCP)
		cpSaved = false
	}
}
