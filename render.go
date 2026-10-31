package main

import (
	"math"
	"strconv"
	"strings"
)

/*
 * ANSI 渲染
 *
 * 文本度量（dw / pad / truncate / fit）见 text.go。
 * 布局约定：整屏只有一个内容宽度 frameW（见 frameWidth），标题、分隔线、表盘、
 * 路面、面板、提示全部按它对齐；每一行都经过 fit() 截断+补齐，保证在窄窗口里
 * 也只是被裁掉，而不会因为超宽自动换行把整个版面顶乱。
 */

// 前景色/样式（esc 与 fgReset 定义在 text.go）。
var (
	fgBold    = esc + "1m"
	fgRed     = esc + "91m"
	fgGreen   = esc + "92m"
	fgYellow  = esc + "93m"
	fgBlue    = esc + "94m"
	fgMagenta = esc + "95m"
	fgCyan    = esc + "96m"
	fgGray    = esc + "90m"
)

// View 是一帧画面的输入。
type View struct {
	Phase       Phase
	Car         CarState
	DriveSpeed  float64 // 驾驶段屏幕上显示的速度 (m/s)
	DriveGap    float64 // 驾驶段屏幕上显示的距离 (m)
	Odometer    float64 // 里程表（驱动路面纹理滚动）
	PedalHeld   bool    // 踏板当前是否踩下
	AscendHeld  bool    // 飞车段：拉升键是否按住
	DescendHeld bool    // 飞车段：下降键是否按住
	Warn        bool    // 驾驶段末段的闪烁警告
	PauseLeft   float64 // 断板/自动接管缓冲剩余秒数
	PauseTotal  float64 // 缓冲总时长（进度条分母）
	Snapped     bool    // 本局踏板是否真的断过（自动接管时为 false）
	Result      *Result
	FailReason  Reason
}

/* ---------------------------- 尺寸与进度条 ---------------------------- */

const (
	// 版式：左右各留一点边距，内容宽度限制在 [minFrameW, maxFrameW]。
	frameSideMargin = 4
	minFrameW       = 48
	maxFrameW       = 72

	// 表盘三列（标签 / 数值 / 单位）的固定宽度，数字变长时单位不漂移。
	gaugeLabelW = 8
	gaugeValueW = 7
	gaugeUnitW  = 9

	// 刹车力高于它就转绿（尚未进入警告区）；距离前车低于它就标红。
	forceGreenFrom  = 1200
	distanceDangerM = 15

	// 结算面板的最小总宽度（含左右边框）。
	minPanelW = 20
)

// frameWidth 是整屏唯一的内容宽度。窄于最小版式时退让给终端宽度，
// 由 fit() 负责截断，保证任何尺寸下都不会横向溢出。
func frameWidth(cols int) int {
	w := clampInt(cols-frameSideMargin, minFrameW, maxFrameW)
	if w > cols {
		w = cols
	}
	return w
}

// barWidthFor 按可用宽度选择条形长度，窄窗口下先缩短条形而不是挤坏布局。
func barWidthFor(frameW int) int {
	switch {
	case frameW >= 64:
		return 22
	case frameW >= 56:
		return 18
	default:
		return 14
	}
}

// barMarked 画进度条；marks 是 0..1 的位置，用灰色竖线标出关键刻度（如 1612 N）。
func barMarked(frac float64, w int, color string, marks ...float64) string {
	f := math.Max(0, math.Min(1, frac))
	filled := int(math.Round(f * float64(w)))
	markAt := func(i int) bool {
		for _, m := range marks {
			if m < 0 || m > 1 {
				continue
			}
			if int(math.Round(math.Max(0, math.Min(1, m))*float64(w))) == i {
				return true
			}
		}
		return false
	}
	var sb strings.Builder
	cur := ""
	for i := 0; i < w; i++ {
		ch, c := "░", fgGray
		if i < filled {
			ch, c = "█", color
		}
		if markAt(i) {
			ch, c = "│", fgGray
		}
		if c != cur {
			sb.WriteString(c)
			cur = c
		}
		sb.WriteString(ch)
	}
	sb.WriteString(fgReset)
	return sb.String()
}

func forceColor(f float64) string {
	switch {
	case f > Phys.Limit:
		return fgRed + fgBold
	case f > Phys.PanicForce:
		return fgYellow + fgBold
	case f > Phys.WarnForce:
		return fgYellow
	case f > forceGreenFrom:
		return fgGreen
	default:
		return fgCyan
	}
}

func rankColor(rank Rank) string {
	switch rank {
	case RankS:
		return fgYellow + fgBold
	case RankA:
		return fgGreen
	case RankB:
		return fgCyan
	case RankC:
		return fgGray
	default:
		return fgRed
	}
}

// gaugeRow 是一行表盘：标签 / 条形 / 右对齐数值 / 单位。
// 数值列宽度固定，数字变长时单位不会左右漂移。
func gaugeRow(label string, frac float64, color, value, unit string, barW int, marks ...float64) string {
	return "  " + pad(label, gaugeLabelW) + barMarked(frac, barW, color, marks...) + "  " +
		color + fgBold + padLeft(value, gaugeValueW) + fgReset + " " +
		fgGray + pad(unit, gaugeUnitW) + fgReset
}

// statusLine 按当前阶段给出一行状态/操作提示。
func statusLine(v View, force float64) string {
	driving := v.Phase == PhaseDriving
	flying := v.Phase == PhaseFlying
	paused := v.Phase == PhaseSnapPause

	if paused {
		if v.Snapped {
			return "  " + fgYellow + fgBold + "** 踏板已断 —— 3 秒后开始操控，先看清距离 **" + fgReset
		}
		return "  " + fgYellow + fgBold + "** 来不及了 —— 3 秒后接管飞行汽车，先看清距离 **" + fgReset
	}
	if driving {
		if v.PedalHeld {
			if v.Warn {
				return "  " + fgYellow + fgBold + "!! 前方有情况 —— 保持住踏板 !!" + fgReset
			}
			return "  " + fgGreen + "巡航中…… 踏板已踩住，刹车点一到立刻生效" + fgReset
		}
		if v.Warn {
			return "  " + fgYellow + fgBold + "!! 前方有情况 —— 现在开始踩，别松手 !!" + fgReset
		}
		return "  " + fgGray + "巡航中…… 还没到刹车点（等警告亮起再踩）" + fgReset
	}
	if flying {
		head := "  " + fgYellow + fgBold + "** 踏板断了 —— 接管会飞的前车 **" + fgReset
		if v.Car.CooldownLeft > 0 {
			return head + "  " + fgRed + "悬空耗尽 —— 强制下降，冷却 " + fmt1(v.Car.CooldownLeft) + "s" + fgReset
		}
		action := fgGray + "悬停中" + fgReset
		if v.AscendHeld {
			action = fgGreen + "↑ 拉升中" + fgReset
		} else if v.DescendHeld {
			action = fgYellow + "↓ 下降中" + fgReset
		}
		ready := fgGray + "飞越需高度 ≥ " + fmt1(Phys.FrontCarHeight) + " m" + fgReset
		if v.Car.Altitude >= Phys.FrontCarHeight {
			ready = fgGreen + fgBold + "可飞越！" + fgReset
		}
		return head + "  " + action + "  " + ready
	}
	pedal := fgGray + "[踏板 ○ 松开]" + fgReset
	if v.PedalHeld {
		pedal = fgGreen + "[踏板 ● 踩下]" + fgReset
	}
	if force > Phys.PanicForce {
		return "  " + pedal + "  " + fgYellow + fgBold + "!! 马上到 1612 N —— 松手！!" + fgReset
	}
	if force > Phys.WarnForce {
		return "  " + pedal + "  " + fgYellow + "警告区 —— 稳住，别再涨了" + fgReset
	}
	if v.PedalHeld {
		return "  " + pedal + "  " + fgGreen + "力在上涨……盯紧表盘" + fgReset
	}
	if force < 300 {
		return "  " + pedal + "  " + fgCyan + "按住空格，把刹车踩下去！" + fgReset
	}
	return "  " + pedal + "  " + fgCyan + "力在衰减，适时再踩" + fgReset
}

func flavor(r Result, reason Reason) string {
	if r.Won {
		if r.Flew {
			if r.Snapped {
				return fgCyan + "刹车没了，你干脆开着飞车从它头顶飞了过去。" + fgReset
			}
			return fgCyan + "来不及了 —— 你干脆接管飞行汽车，从它头顶飞了过去。" + fgReset
		}
		if r.Snapped {
			return fgCyan + "踏板都踩断了，你居然靠滑行蹭到了前车跟前 —— 算你赢。" + fgReset
		}
		switch r.Rank {
		case RankS:
			return fgYellow + "教科书级的一脚。踏板完好无损。" + fgReset
		case RankA:
			return fgGreen + "漂亮。你和 1612 N 擦肩而过。" + fgReset
		case RankB:
			return fgCyan + "稳。还能再贪一点。" + fgReset
		default:
			return fgGray + "停是停住了……但你根本没敢踩。" + fgReset
		}
	}
	if reason == ReasonSnapped {
		return fgRed + "1612 N 不是建议，是物理法则。" + fgReset
	}
	return fgRed + "刹车力不够，前车替你做决定。" + fgReset
}

/* ------------------------------- 面板工具 ------------------------------- */

// panel 是一块竖线边框的面板（结算面板用）。
type panel struct {
	left string
	w    int // 面板总宽度（含左右边框）
}

func newPanel(leftPad string, w int) panel {
	if w < minPanelW {
		w = minPanelW
	}
	return panel{left: leftPad, w: w}
}

func (p panel) inner() int { return p.w - 2 }

func (p panel) top() string {
	return p.left + fgGray + "╔" + strings.Repeat("═", p.inner()) + "╗" + fgReset
}

func (p panel) mid() string {
	return p.left + fgGray + "╠" + strings.Repeat("═", p.inner()) + "╣" + fgReset
}

func (p panel) bot() string {
	return p.left + fgGray + "╚" + strings.Repeat("═", p.inner()) + "╝" + fgReset
}

// row 左对齐内容行。
func (p panel) row(s string) string {
	return p.left + fgGray + "║" + fgReset + fit(s, p.inner()) + fgGray + "║" + fgReset
}

// crow 居中内容行。
func (p panel) crow(s string) string {
	return p.left + fgGray + "║" + fgReset + centerIn(s, p.inner()) + fgGray + "║" + fgReset
}

/* ------------------------------ 游玩画面 ------------------------------ */

// row 是一帧里的一行，opt 越大越先被裁掉（窄窗口降级用）。
type screenRow struct {
	s   string
	opt int
}

// 裁剪优先级：空行先走，其次提示，再次路面；标题/表盘/面板/状态永远保留。
const (
	optMust  = 0
	optRoad  = 1
	optHint  = 2
	optBlank = 3
)

// trimRows 按 opt 从大到小删行，直到不超过 avail；实在还超就截尾。
func trimRows(rows []screenRow, avail int) []screenRow {
	if avail < 1 {
		avail = 1
	}
	out := rows
	for len(out) > avail {
		worst, worstOpt := -1, optMust
		for i, r := range out {
			if r.opt > worstOpt {
				worst, worstOpt = i, r.opt
			}
		}
		if worst < 0 {
			out = out[:avail]
			break
		}
		out = append(out[:worst], out[worst+1:]...)
	}
	return out
}

func roadRows(v View, frameW int, driving bool) []screenRow {
	interiorW := frameW - 4
	if interiorW < 20 {
		interiorW = 20
	}
	const (
		carStart = 2
		carLen   = 5
		wallLen  = 5
		// 路面纹理随里程滚动：每米推进 0.6 格。
		scrollPerMeter = 0.6
	)

	remaining := math.Max(0, Phys.ObstacleDistance-v.Car.Pos)
	if driving {
		remaining = v.DriveGap
	}
	frac := math.Max(0, math.Min(1, remaining/Phys.DriveGapFrom))

	wallStart := 9 + int(math.Round(frac*float64(interiorW-15)))
	if wallStart < carStart+carLen+1 {
		wallStart = carStart + carLen + 1
	}
	if wallStart > interiorW-wallLen {
		wallStart = interiorW - wallLen
	}

	off := int(math.Floor(v.Odometer * scrollPerMeter))
	fill := func(from, to int) string {
		var sb strings.Builder
		for i := from; i < to; i++ {
			if ((((i - off) % 8) + 8) % 8) < 3 {
				sb.WriteString("─")
			} else {
				sb.WriteString("·")
			}
		}
		return sb.String()
	}

	if wallStart < carStart+carLen {
		wallStart = carStart + carLen
	}

	interior := fgGray +
		fill(0, carStart) +
		fgReset +
		fgCyan +
		"[CAR]" +
		fgReset +
		fgGray +
		fill(carStart+carLen, wallStart) +
		fgReset +
		fgRed + fgBold +
		"[CAR]" +
		fgReset +
		fgGray +
		fill(wallStart+wallLen, interiorW) +
		fgReset

	return []screenRow{
		{"  " + fgGray + "┌" + strings.Repeat("─", interiorW) + "┐" + fgReset, optRoad},
		{"  " + fgGray + "│" + fgReset + interior + fgGray + "│" + fgReset, optRoad},
		{"  " + fgGray + "└" + strings.Repeat("─", interiorW) + "┘" + fgReset, optRoad},
	}
}

func playRows(v View, frameW, barW int) []screenRow {
	driving := v.Phase == PhaseDriving
	flying := v.Phase == PhaseFlying
	paused := v.Phase == PhaseSnapPause

	speed := v.Car.Speed
	force := v.Car.Force
	if driving {
		speed = v.DriveSpeed
		force = 0
	}
	remaining := math.Max(0, Phys.ObstacleDistance-v.Car.Pos)
	if driving {
		remaining = v.DriveGap
	}

	rows := []screenRow{
		{"", optBlank},
		{centerIn(fgMagenta+fgBold+"≡  刹 不 住 的 1612 N  ≡"+fgReset, frameW), optMust},
		{fgGray + strings.Repeat("─", frameW) + fgReset, optBlank},
		{centerIn(fgGray+"速 度 与 距 离"+fgReset, frameW), optBlank},
	}

	speedFrac := math.Min(1, speed/Phys.StartSpeed)
	rows = append(rows,
		screenRow{gaugeRow("速度", speedFrac, fgCyan, fmt1(speed*3.6), "km/h", barW), optMust},
	)

	// 三个阶段共用同一套表盘：速度 / 当前操纵量 / 距前车。
	// 飞车段用「高度 + 悬空」代替刹车力，缓冲期用「缓冲」代替刹车力，
	// 位置、宽度与对齐方式和普通汽车完全一致，切换阶段时版面不跳。
	switch {
	case paused:
		total := v.PauseTotal
		if total <= 0 {
			total = Phys.SnapPause
		}
		frac := math.Max(0, math.Min(1, v.PauseLeft/total))
		rows = append(rows, screenRow{gaugeRow("缓冲", frac, fgYellow, fmt1(v.PauseLeft), "s", barW), optMust})
	case flying:
		altFrac := v.Car.Altitude / Phys.MaxAltitude
		altColor := fgCyan
		if v.Car.Altitude >= Phys.FrontCarHeight {
			altColor = fgGreen
		}
		rows = append(rows, screenRow{
			gaugeRow("高度", altFrac, altColor, fmt1(v.Car.Altitude), "m", barW,
				Phys.FrontCarHeight/Phys.MaxAltitude),
			optMust,
		})
		if v.Car.CooldownLeft > 0 {
			rows = append(rows, screenRow{gaugeRow("悬空", 0, fgRed, fmt1(v.Car.CooldownLeft), "s 冷却", barW), optMust})
		} else {
			rows = append(rows, screenRow{gaugeRow("悬空", v.Car.HoverLeft/Phys.MaxHover, fgCyan, fmt1(v.Car.HoverLeft), "s", barW), optMust})
		}
	default:
		forceFrac := force / Phys.Limit
		rows = append(rows, screenRow{
			gaugeRow("刹车力", forceFrac, forceColor(force), fmtRound(force), "/ "+jsNum(Phys.Limit)+" N", barW,
				Phys.WarnForce/Phys.Limit, Phys.PanicForce/Phys.Limit),
			optMust,
		})
	}

	// 和下面的路面共用 0..DriveGapFrom 这一套刻度：进入刹车段时表盘不会突然跳满
	distFrac := math.Min(1, remaining/Phys.DriveGapFrom)
	distColor := fgBlue
	if remaining < distanceDangerM {
		distColor = fgRed
	}
	rows = append(rows, screenRow{gaugeRow("距前车", distFrac, distColor, fmt1(remaining), "m", barW), optMust})

	rows = append(rows, screenRow{"", optBlank})
	rows = append(rows, roadRows(v, frameW, driving)...)
	rows = append(rows, screenRow{"", optBlank})

	rows = append(rows, screenRow{statusLine(v, force), optMust})
	rows = append(rows, screenRow{"", optBlank})

	hint := "[空格] 按住 = 踩死刹车，松手 = 抬起     [Q] 退出"
	if flying {
		hint = "[W] 拉升 / [S] 下降 飞越前车     [R] 再来一次     [Q] 退出"
	} else if paused {
		hint = "接管准备中……     [R] 再来一次     [Q] 退出"
	}
	rows = append(rows, screenRow{"  " + fgGray + hint + fgReset, optHint})
	return rows
}

/* ------------------------------ 结算画面 ------------------------------ */

func resultRows(r Result, reason Reason, frameW int) []screenRow {
	p := newPanel("  ", frameW-2)
	title := "游 戏 失 败"
	titleColor := fgRed + fgBold
	if r.Won {
		title = "刹 停 成 功"
		titleColor = fgGreen + fgBold
		if r.Flew {
			title = "飞 越 成 功"
		}
	}

	rows := []screenRow{
		{"", optBlank},
		{p.top(), optMust},
		{p.crow(titleColor + title + fgReset), optMust},
		{p.crow(rankColor(r.Rank) + "RANK " + string(r.Rank) + fgReset + fgGray + "   SCORE " + strconv.Itoa(r.Score) + fgReset), optMust},
		{p.mid(), optMust},
	}

	peakExtra := ""
	if r.Snapped {
		peakExtra = fgRed + "  （踏板已断）" + fgReset
	}
	rows = append(rows, screenRow{p.row("峰值刹车力   " + rankColor(r.Rank) + fmtRound(r.PeakForce) + " N" + fgReset + fgGray + "  / " + jsNum(Phys.Limit) + " N" + fgReset + peakExtra), optMust})
	if r.Flew {
		rows = append(rows,
			screenRow{p.row("飞越高度     " + fgGreen + fmt1(r.PeakAltitude) + " m" + fgReset + fgGray + "  （前车 " + fmt1(Phys.FrontCarHeight) + " m）" + fgReset), optMust},
			screenRow{p.row("悬空剩余     " + fgCyan + fmt1(r.HoverLeft) + " s" + fgReset), optMust},
		)
		// 飞越取胜：上面已经显示了飞越高度/悬空剩余，不再显示停车余量
	} else if r.Won || r.Margin > 0 {
		rows = append(rows, screenRow{p.row("停车余量     " + fmt1(r.Margin) + " m"), optMust})
	} else {
		rows = append(rows, screenRow{p.row("停车余量     " + fgRed + "撞上了" + fgReset), optMust})
	}
	rows = append(rows,
		screenRow{p.row("刹车耗时     " + fmt2(r.Time) + " s"), optMust},
		screenRow{p.row("结束速度     " + fmt1(r.Speed*3.6) + " km/h"), optMust},
		screenRow{p.bot(), optMust},
		screenRow{"", optBlank},
		screenRow{"  " + flavor(r, reason), optHint},
		screenRow{"", optBlank},
		screenRow{"  " + fgGray + "[R] 再来一次      [Q] 退出" + fgReset, optHint},
	)
	return rows
}

// render 生成一整帧（含 \x1b[H 归位与 \x1b[J 清屏底）。
func render(v View, cols, rows int) string {
	frameW := frameWidth(cols)
	left := (cols - frameW) / 2
	if left < 0 {
		left = 0
	}
	indent := strings.Repeat(" ", left)
	barW := barWidthFor(frameW)

	avail := rows - 1
	if avail < 1 {
		avail = 1
	}

	var screen []screenRow
	if showsResult(v) {
		screen = trimRows(resultRows(*v.Result, v.FailReason, frameW), avail)
	} else {
		screen = trimRows(playRows(v, frameW, barW), avail)
	}

	// 每行结尾擦到行尾（\x1b[K），整帧结尾再擦到屏底（\x1b[J）：
	// 否则上一帧更长时（结算画面比游玩画面长）会在下方留下残影
	body := make([]string, 0, len(screen))
	for _, r := range screen {
		body = append(body, indent+fit(r.s, frameW)+"\x1b[K")
	}
	return "\x1b[H" + strings.Join(body, "\n") + "\x1b[J"
}

// showsResult 表示这一帧应该画结算画面。
func showsResult(v View) bool {
	return (v.Phase == PhaseStopped || v.Phase == PhaseCrashed) && v.Result != nil
}
