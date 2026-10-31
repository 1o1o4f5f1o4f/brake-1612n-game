package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

/*
 * 终端文本度量与格式化
 *
 * 中文按 2 列显示宽度计（runeWidth / dw）；所有动态行都必须经过
 * truncate/fit，保证窄窗口里只是被裁掉，而不会自动换行顶乱版面。
 */

const esc = "\x1b["

// fgReset 在截断后补上，避免尾部颜色转义被切掉后污染后续内容。
var fgReset = esc + "0m"

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// runeWidth 单个字符的终端显示宽度：CJK 记 2 列，其余记 1 列。
func runeWidth(r rune) int {
	wide := (r >= 0x1100 && r <= 0x115f) ||
		(r >= 0x2e80 && r <= 0xa4cf) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe4f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x20000 && r <= 0x3fffd)
	if wide {
		return 2
	}
	return 1
}

// dw 终端显示宽度（忽略颜色转义）。
func dw(s string) int {
	w := 0
	for _, r := range ansiRe.ReplaceAllString(s, "") {
		w += runeWidth(r)
	}
	return w
}

// pad / padLeft / centerIn 按显示宽度补齐。
func pad(s string, w int) string {
	if d := w - dw(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - dw(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

func centerIn(s string, w int) string {
	n := (w - dw(s)) / 2
	if n < 0 {
		n = 0
	}
	return pad(strings.Repeat(" ", n)+s, w)
}

// truncate 把 s 截到显示宽度 w：ANSI 转义原样保留（不计宽），CJK 记 2 列。
// 一旦发生截断就补一个 reset，避免尾部被切掉颜色转义后污染后续内容。
func truncate(s string, w int) string {
	if dw(s) <= w {
		return s
	}
	var sb strings.Builder
	used := 0
	sawANSI := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if loc := ansiRe.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
				sb.WriteString(s[i : i+loc[1]])
				sawANSI = true
				i += loc[1]
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if used+rw > w {
			break
		}
		used += rw
		sb.WriteString(s[i : i+size])
		i += size
	}
	if sawANSI {
		sb.WriteString(fgReset)
	}
	return sb.String()
}

// fit 截断到 w 再补齐到 w：所有动态行都必须过这一道。
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return pad(truncate(s, w), w)
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// fmt1 / fmt2 / fmtRound 分别格式化为 1 位小数、2 位小数和整数。
func fmt1(x float64) string { return fmt.Sprintf("%.1f", x) }
func fmt2(x float64) string { return fmt.Sprintf("%.2f", x) }
func fmtRound(x float64) string {
	return fmt.Sprintf("%.0f", math.Round(x))
}

// jsNum 按最短格式输出数字（整数不带小数点，如 1612 / 2.4 / 0.42）。
func jsNum(x float64) string {
	return strconv.FormatFloat(x, 'f', -1, 64)
}
