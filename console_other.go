//go:build !windows

package main

// 非 Windows 平台：终端本来就是 VT，输出侧无需额外准备。
func startConsole() {}

func restoreConsole() {}
