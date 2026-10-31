package main

import (
	"os"

	"golang.org/x/term"
)

/*
 * 原始模式读键
 *
 * x/term 的 MakeRaw 在 Windows 上会关掉回显/逐行输入并打开
 * ENABLE_VIRTUAL_TERMINAL_INPUT：
 * 长按产生重复字节流、没有可靠的松键事件、Ctrl+C 变成 0x03 字节。
 */

var (
	stdinState *term.State
	keys       = make(chan byte, 256)
)

// startInput 把 stdin 切到 raw 模式，并起一个 goroutine 逐字节投递到 keys。
// 非 TTY 环境（比如管道）忽略 raw 失败，仍可读。
func startInput() {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if st, err := term.MakeRaw(fd); err == nil {
			stdinState = st
		}
	}

	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			for i := 0; i < n; i++ {
				select {
				case keys <- buf[i]:
				default: // 队列满：丢弃，别阻塞读取
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
}

// disposeInput 恢复终端模式（幂等）。
func disposeInput() {
	if stdinState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), stdinState)
		stdinState = nil
	}
}
