package main

import "math"

/*
 * 游戏状态机 / 按键时间推断 / 音效
 *
 * 每个阶段有独立的 tick<Phase> 推进函数，tick 只负责分派；
 * 结算统一走 Game.finish，避免各分支重复设置相位与 Result。
 */

// warnReleaseHysteresis 是刹车警报的回差 (N)：力要掉到 WarnForce 以下这么多才解除报警。
const warnReleaseHysteresis = 80

// Game 是一局游戏的全部状态。
type Game struct {
	Phase     Phase
	Car       CarState
	DriveTime float64
	Odometer  float64
	// HoldUntil 是踏板「保持按下」的截止时间戳（nowMs() 基准）
	HoldUntil float64
	// LastEventAt 是最近一次空格事件的时间戳
	LastEventAt float64
	// WarnLatch 表示是否已经在危险区里（用于边沿触发提示音）
	WarnLatch bool
	// 飞车段的拉升/下降按键，同样靠连发间隔推断「按住」
	AscendUntil    float64
	AscendEventAt  float64
	DescendUntil   float64
	DescendEventAt float64
	// HoverLatch 表示悬空刚耗尽、正在冷却（只在那一下响一次）
	HoverLatch bool
	// PauseLeft 是断板/自动接管缓冲的剩余秒数（PhaseSnapPause 期间物理冻结）
	PauseLeft float64
	// Snapped 表示本局踏板是否真的断过：自动接管时踏板完好，评分与结算文案要区分
	Snapped    bool
	Result     *Result
	FailReason Reason
}

func newGame() Game {
	return Game{
		Phase:          PhaseDriving,
		Car:            createCar(),
		HoldUntil:      math.Inf(-1),
		LastEventAt:    math.Inf(-1),
		AscendUntil:    math.Inf(-1),
		AscendEventAt:  math.Inf(-1),
		DescendUntil:   math.Inf(-1),
		DescendEventAt: math.Inf(-1),
	}
}

func lerp(a, b, t float64) float64 {
	return a + (b-a)*math.Min(1, math.Max(0, t))
}

// driveProgress 是巡航段进度 0..1。
func driveProgress() float64 {
	return math.Min(1, game.DriveTime/Phys.DriveDuration)
}

/* ------------------------------- 音效 ------------------------------- */

// lastBeep 初值 0；第一声提示音最早也要到刹车段，不会误伤。
var lastBeep float64

// pendingBeeps 是待补发的提示音时刻（ms），避免 goroutine 和渲染交错写 stdout。
var pendingBeeps []float64

// beep 写一声终端提示音；minGap 毫秒内的重复调用会被丢掉。
func beep(minGap float64) {
	now := nowMs()
	if now-lastBeep < minGap {
		return
	}
	lastBeep = now
	outWrite("\x07")
}

// fanfare 结算三连音（0ms / 110ms / 260ms）。
func fanfare() {
	outWrite("\x07")
	now := nowMs()
	pendingBeeps = append(pendingBeeps, now+110, now+260)
}

// flushBeeps 在每帧里补发到点的提示音。
func flushBeeps(now float64) {
	remaining := pendingBeeps[:0]
	for _, at := range pendingBeeps {
		if at <= now {
			outWrite("\x07")
		} else {
			remaining = append(remaining, at)
		}
	}
	pendingBeeps = remaining
}

/* ---------------------------- 按键时间推断 ---------------------------- */

// 终端收不到可靠的「松键」事件，所以所有「按住」状态都由事件间隔推断：
// 一次事件把「保持到」时间推到 now + pedalGrace(...)，之后靠时间戳判断。

var game = newGame()

func (g *Game) pedalHeld() bool   { return nowMs() < g.HoldUntil }
func (g *Game) ascendHeld() bool  { return nowMs() < g.AscendUntil }
func (g *Game) descendHeld() bool { return nowMs() < g.DescendUntil }

// onSpace 处理一次空格事件。
//
// 终端收不到可靠的「松键」，所以用事件间隔推断（细节见 physics.go 的 pedalGrace）。
// 宽限期给多少直接决定手感：太长会让力在你松手后又多涨一截（快贴到 1612 时会显得
// 不公平），太短又会在连发很慢的键盘上让踏板断断续续。所以：
//   - 冷启动给完整 TapGrace，单点也有蓄力、桥接 Windows 首连发延迟；
//   - 已经在蓄力时只按实测节奏给一小段，点按也能精细控力。
func onSpace() {
	// 只有驾驶/刹车阶段踏板才有意义：结算画面里空格既不喂踏板也不响，别污染按键节奏
	if game.Phase != PhaseDriving && game.Phase != PhaseBraking {
		return
	}

	now := nowMs()
	gap := now - game.LastEventAt
	game.LastEventAt = now
	game.HoldUntil = now + pedalGrace(gap, game.Car.Force)
	if gap >= Pedal.GroupGap {
		beep(60) // 每一「脚」给一点触觉反馈
	}
}

// onVertical 处理飞车段的拉升/下降按键事件（W/S）。
// 和踏板一样，终端没有可靠的松键，用事件间隔推断按住（复用 pedalGrace）。
func onVertical(until, eventAt *float64) {
	if game.Phase != PhaseFlying {
		return
	}
	now := nowMs()
	gap := now - *eventAt
	*eventAt = now
	*until = now + pedalGrace(gap, 0)
}

/* ------------------------------ 阶段转移 ------------------------------ */

// beginPause 在「踏板断掉」或「预测到必然撞上」的瞬间进入缓冲段：
// 物理完全冻结 SnapPause 秒，给玩家时间看清距离、准备接管飞车。
// snapped 记录本局踏板是否真的断过（自动接管时为 false）。
func beginPause(snapped bool) {
	game.Phase = PhaseSnapPause
	game.PauseLeft = Phys.SnapPause
	game.Car.Force = 0 // 接管后踏板力不再生效；峰值已记在 PeakForce 里，不影响评分
	game.Snapped = snapped
	outWrite("\x07\x07")
}

// beginFlying 把「断板但还来得及」的车切换成会飞的前车。
func beginFlying() {
	game.Phase = PhaseFlying
	game.Car.Force = 0
	game.Car.Altitude = 0
	game.Car.PeakAltitude = 0
	game.Car.HoverLeft = Phys.MaxHover
	game.Car.CooldownLeft = 0
}

// imminentCrash 判断断板那一刻是否已经来不及飞（到前车的时间太短）。
func imminentCrash(c CarState) bool {
	return timeToObstacle(c) <= Phys.FlyArmTTC
}

// finish 结算本局：写 Result 并切到赢/输对应的结束阶段。
// snapped 显式传入：断板瞬间就直接撞车时，Game.Snapped 还没置位。
func (g *Game) finish(won bool, reason Reason, snapped bool) {
	r := evaluate(g.Car, won, reason, snapped)
	g.Result = &r
	if won {
		g.Phase = PhaseStopped
		fanfare()
		return
	}
	g.Phase = PhaseCrashed
	g.FailReason = reason
}

/* ------------------------------ 每帧推进 ------------------------------ */

func tick(dt float64) {
	switch game.Phase {
	case PhaseSnapPause:
		tickPause(dt)
	case PhaseDriving:
		tickDriving(dt)
	case PhaseBraking:
		tickBraking(dt)
	case PhaseFlying:
		tickFlying(dt)
	}
}

// tickPause：只走倒计时，物理一行都不推进（位置/速度/评分计时全部冻结）。
func tickPause(dt float64) {
	g := &game
	g.PauseLeft -= dt
	if g.PauseLeft <= 1e-9 { // 浮点累加误差：到点即切换，别卡在最后一帧
		g.PauseLeft = 0
		beginFlying()
	}
}

// tickDriving：巡航段，刹车还踩不下去；结束时把车重置到刹车起点。
func tickDriving(dt float64) {
	g := &game
	g.DriveTime += dt
	g.Odometer += lerp(Phys.DriveSpeedFrom, Phys.StartSpeed, driveProgress()) * dt
	if g.DriveTime >= Phys.DriveDuration {
		g.Phase = PhaseBraking
		g.Car = createCar()
		// 里程表不清零：路面纹理相位保持连续，切换时不会横跳
	}
}

// tickFlying：断板/自动接管后的飞车段，判定飞越或撞车。
func tickFlying(dt float64) {
	g := &game
	stepFlying(&g.Car, dt, g.ascendHeld(), g.descendHeld())
	g.Odometer += g.Car.Speed * dt
	g.updateHoverAlarm()

	switch {
	case reachedObstacle(g.Car):
		if g.Car.Altitude >= Phys.FrontCarHeight {
			// 从上方飞越前车 —— 没撞上就是赢
			g.finish(true, ReasonFlewOver, g.Snapped)
			return
		}
		// 高度不够，撞上前车；没断板时原因记「撞上前车」而不是「断板撞车」
		reason := ReasonSnapped
		if !g.Snapped {
			reason = ReasonHit
		}
		g.finish(false, reason, g.Snapped)
	case stopped(g.Car):
		// 断板/接管后靠滑行在前车前停下 —— 没撞上就是赢
		g.finish(true, ReasonStopped, g.Snapped)
	}
}

// tickBraking：刹车段，判定顺序为 撞前车 > 停住 > 断踏板 > 自动接管。
func tickBraking(dt float64) {
	g := &game
	stepPhysics(&g.Car, dt, g.pedalHeld())
	g.Odometer += g.Car.Speed * dt
	g.updateBrakeAlarm()

	// 关键在于「停住」早于「断踏板」：同一个 tick 里既踩过 1612 N 又刚好停下时，
	// 车确实停在了前车跟前，按停住算，不会因为一次超调被判断裂。
	switch {
	case reachedObstacle(g.Car):
		g.finish(false, ReasonHit, false)
	case stopped(g.Car):
		g.finish(true, ReasonStopped, false)
	case g.Car.Force > Phys.Limit:
		if imminentCrash(g.Car) {
			// 来不及飞：直接撞上前车
			g.finish(false, ReasonSnapped, true)
		} else {
			// 还来得及：先冻结 SnapPause 秒缓冲，再切换到会飞的前车
			beginPause(true)
		}
	case predictImpactWithin(g.Car, Phys.AutoFlyHorizon, g.pedalHeld()):
		// 踏板还没断，但照现在的开法 horizon 秒内必然撞上前车：提前接管飞行汽车。
		// 同样先冻结片刻，让玩家看清距离、准备拉升。
		beginPause(false)
	}
}

// updateBrakeAlarm 只在「刚冲进危险区」和「千钧一发」时给提示音，避免全程滴滴滴。
func (g *Game) updateBrakeAlarm() {
	switch {
	case g.Car.Force > Phys.WarnForce && !g.WarnLatch:
		g.WarnLatch = true
		beep(0)
	case g.Car.Force < Phys.WarnForce-warnReleaseHysteresis:
		g.WarnLatch = false
	}
	if g.Car.Force > Phys.PanicForce {
		beep(420)
	}
}

// updateHoverAlarm 在悬空刚耗尽、进入冷却的那一下响一声。
func (g *Game) updateHoverAlarm() {
	if g.Car.CooldownLeft > 0 {
		if !g.HoverLatch {
			g.HoverLatch = true
			beep(0)
		}
		return
	}
	g.HoverLatch = false
}

/* -------------------------------- 视图 -------------------------------- */

func buildView() View {
	g := &game
	p := driveProgress()
	return View{
		Phase:      g.Phase,
		Car:        g.Car,
		DriveSpeed: lerp(Phys.DriveSpeedFrom, Phys.StartSpeed, p),
		DriveGap:   lerp(Phys.DriveGapFrom, Phys.ObstacleDistance, p),
		Odometer:   g.Odometer,
		// 巡航段也把踏板状态暴露给界面，好提示「已经踩住、刹车点一到就生效」
		PedalHeld:   g.pedalHeld(),
		AscendHeld:  g.ascendHeld(),
		DescendHeld: g.descendHeld(),
		Warn:        g.Phase == PhaseDriving && p > 0.5 && int(g.DriveTime*8)%2 == 0,
		PauseLeft:   g.PauseLeft,
		PauseTotal:  Phys.SnapPause,
		Snapped:     g.Snapped,
		Result:      g.Result,
		FailReason:  g.FailReason,
	}
}

/* -------------------------------- 按键 -------------------------------- */

// canRestart 表示当前阶段按 R 可以重开。
// 飞车/缓冲期也允许重开：踏板断掉后车还要滑/飞一段，这时候 R 必须有效，否则像是卡死。
func (g *Game) canRestart() bool {
	switch g.Phase {
	case PhaseStopped, PhaseCrashed, PhaseFlying, PhaseSnapPause:
		return true
	default:
		return false
	}
}

// handleKey 处理一个按键；返回 true 表示要退出游戏。
func handleKey(ch byte) (quit bool) {
	switch ch {
	case 0x03, 'q', 'Q': // Ctrl+C / Q
		return true
	case ' ':
		onSpace()
	case 'w', 'W':
		onVertical(&game.AscendUntil, &game.AscendEventAt)
	case 's', 'S':
		onVertical(&game.DescendUntil, &game.DescendEventAt)
	case 'r', 'R':
		if game.canRestart() {
			game = newGame()
		}
	}
	return false
}
