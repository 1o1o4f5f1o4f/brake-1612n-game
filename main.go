package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"golang.org/x/term"
)

/*
 * 入口 / 主循环 / 生命周期
 *
 * 固定步长 1/60 s 的累加器驱动物理，60Hz 渲染一帧；
 * 帧长超过 maxFrameSec 就钳住，别让卡顿攒成死亡螺旋。
 */

const (
	fixedDt = 1.0 / 60.0
	// maxFrameSec 是单帧时间上限：卡顿后不会一次性补跑大量物理步。
	maxFrameSec = 0.25
)

var startTime = time.Now()

// nowMs 返回自进程启动以来的毫秒数。
func nowMs() float64 {
	return float64(time.Since(startTime).Nanoseconds()) / 1e6
}

// outWrite 写 stdout（整帧一次写入，减少闪烁）。
func outWrite(s string) {
	_, _ = os.Stdout.WriteString(s)
}

// termSize 取终端尺寸；拿不到时退回 80×24。
func termSize() (int, int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		return 80, 24
	}
	return cols, rows
}

func main() {
	sim := flag.Bool("sim", false, "运行闭环校准脚本")
	flag.Parse()
	if *sim {
		os.Exit(runSim())
	}
	os.Exit(runGame())
}

// runGame 准备终端与输入，跑完主循环后保证还原。
func runGame() (code int) {
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		disposeInput()
		restoreConsole()
		outWrite("\x1b[0m\x1b[?25h\x1b[?1049l")
	}
	// 正常退出、信号退出、panic 都要还原终端，否则备用屏缓冲区和 raw mode 会留在地上
	defer func() {
		cleanup()
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, r)
			code = 1
		}
	}()

	startConsole()
	startInput()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	outWrite("\x1b[?1049h\x1b[?25l\x1b[2J")
	return gameLoop(sig)
}

// gameLoop 同时等待按键、退出信号和渲染节拍，返回进程退出码。
func gameLoop(sig <-chan os.Signal) int {
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()

	acc := 0.0
	last := nowMs()
	for {
		select {
		case <-sig:
			return 0
		case ch, ok := <-keys:
			if !ok {
				keys = nil // stdin 结束（EOF）：这条分支从此不再触发
				continue
			}
			if handleKey(ch) {
				return 0
			}
		case <-ticker.C:
			now := nowMs()
			advanceFrame((now-last)/1000, &acc)
			last = now

			flushBeeps(now)
			cols, rows := termSize()
			outWrite(render(buildView(), cols, rows))
		}
	}
}

// advanceFrame 把真实帧长累加进固定步长累加器，按 fixedDt 推进物理。
func advanceFrame(frameSec float64, acc *float64) {
	if frameSec > maxFrameSec {
		frameSec = maxFrameSec
	}
	*acc += frameSec
	for *acc >= fixedDt {
		tick(fixedDt)
		*acc -= fixedDt
	}
}
