package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

/*
 * 闭环模拟
 *
 * 用和游戏**完全相同**的物理与踏板推断逻辑跑一遍，
 * 用来校准难度、评级线，也可以当回归工具。
 *
 * 运行：go run . -sim
 *
 * 脚本把自己当成一个「照着表盘控力」的玩家：力低了就补踩，力高了就抬脚，
 * 手指动作有反应延迟、按键有连发间隔（或干脆只能手点），松手同样是靠
 * pedalGrace 推断出来的 —— 和真人坐在终端前的处境一致。
 */

const (
	simDT    = 1.0 / 60.0
	simDTMs  = simDT * 1000
	simMaxMs = 60000.0
)

type finger struct {
	holding     bool
	lastEventAt float64
	holdUntil   float64
	nextEventAt float64
}

type pendingAction struct {
	down bool
	at   float64
}

type stage2 struct {
	belowSpeed float64
	target     float64
}

type simOptions struct {
	// target 目标刹车力 (N)；不填 = 全程不踩
	target *float64
	// alwaysHold 一直踩住不松（模拟「按死」）
	alwaysHold bool
	// repeatMs 连发/点按间隔 (ms)：33 ≈ 30Hz 连发，250 ≈ 键盘不连发、只能手一下下点
	repeatMs float64
	// reactionMs 人从「想改」到「手指动」的延迟 (ms)
	reactionMs float64
	// hysteresis 控力死区 (N)：低于 目标-死区/2 才补踩，高于 目标+死区/2 才松
	hysteresis float64
	// jitter 目标力抖动幅度 (N)，模拟手抖
	jitter float64
	// stage2 第二段控力：车速降到 belowSpeed 后改用这个目标力
	stage2 *stage2
	// fly 断板后是否尝试拉升飞越（false = 断板后什么都不做，多半撞上前车）
	fly bool
	// flyLead 拉升时机：剩余距离 ≤ max(4, 速度 × flyLead) 时开始拉升
	flyLead float64
}

func fptr(v float64) *float64 { return &v }

// simRunner 是一次闭环模拟的全部运行态。
type simRunner struct {
	opts    simOptions
	car     CarState
	fing    finger
	now     float64
	pending *pendingAction
	snapped bool
	flying  bool
}

func run(opts simOptions) Result {
	s := &simRunner{
		opts: opts,
		car:  createCar(),
		fing: finger{lastEventAt: math.Inf(-1), holdUntil: math.Inf(-1)},
	}

	for s.now < simMaxMs {
		s.now += simDTMs

		var (
			r    Result
			done bool
		)
		if s.flying {
			r, done = s.flyingTick()
		} else {
			r, done = s.drivingTick()
		}
		if done {
			return r
		}
	}

	reason := ReasonHit
	if s.snapped {
		reason = ReasonSnapped
	}
	return evaluate(s.car, false, reason, s.snapped)
}

// drivingTick 推进一帧驾车/刹车段：先按目标力决定手指动作，再积分物理并判定结局。
func (s *simRunner) drivingTick() (Result, bool) {
	s.steerPedal()
	s.emitPedalRepeat()
	stepPhysics(&s.car, simDT, s.now < s.fing.holdUntil)

	switch {
	case s.car.Force > Phys.Limit:
		return s.snap()
	case stopped(s.car):
		return evaluate(s.car, true, ReasonStopped, false), true
	case reachedObstacle(s.car):
		return evaluate(s.car, false, ReasonHit, false), true
	case predictImpactWithin(s.car, Phys.AutoFlyHorizon, s.now < s.fing.holdUntil):
		// 踏板还没断，但照当前开法 3 秒内必然撞上：提前接管飞行汽车（与游戏一致）。
		s.beginFlying()
	}
	return Result{}, false
}

// snap 处理踏板断裂：来不及飞就直接撞前车，否则进入飞车段。
func (s *simRunner) snap() (Result, bool) {
	s.snapped = true
	s.car.Force = 0
	if timeToObstacle(s.car) <= Phys.FlyArmTTC {
		return evaluate(s.car, false, ReasonSnapped, true), true
	}
	s.beginFlying()
	return Result{}, false
}

// beginFlying 切换到飞车段。缓冲期物理不推进，只把时间轴往前推 SnapPause，
// 用来对齐按下 W 的宽限期（和游戏一致）。
func (s *simRunner) beginFlying() {
	s.flying = true
	s.car.Force = 0
	s.car.HoverLeft = Phys.MaxHover
	s.car.CooldownLeft = 0
	s.now += Phys.SnapPause * 1000
}

// flyingTick 推进一帧飞车段，并在到达前车/停住时结算。
func (s *simRunner) flyingTick() (Result, bool) {
	// 离前车还剩 max(4, 速度×flyLead) 时开始拉升，飞越它。
	remaining := Phys.ObstacleDistance - s.car.Pos
	ascend := s.opts.fly && s.car.HoverLeft > 0 && s.car.CooldownLeft == 0 &&
		s.car.Altitude < Phys.MaxAltitude*0.98 &&
		remaining <= math.Max(4, s.car.Speed*s.opts.flyLead)
	stepFlying(&s.car, simDT, ascend, false)

	switch {
	case reachedObstacle(s.car):
		if s.car.Altitude >= Phys.FrontCarHeight {
			return evaluate(s.car, true, ReasonFlewOver, s.snapped), true
		}
		// 没断板就被自动接管的话，原因是「撞上前车」而不是「断板撞车」
		reason := ReasonSnapped
		if !s.snapped {
			reason = ReasonHit
		}
		return evaluate(s.car, false, reason, s.snapped), true
	case stopped(s.car):
		return evaluate(s.car, true, ReasonStopped, s.snapped), true
	}
	return Result{}, false
}

// steerPedal 是模拟玩家的控力逻辑：按死 / 目标力 + 死区 / 不踩。
func (s *simRunner) steerPedal() {
	stageTarget := s.opts.target
	if s.opts.stage2 != nil && s.car.Speed <= s.opts.stage2.belowSpeed {
		stageTarget = fptr(s.opts.stage2.target)
	}
	target := 0.0
	if stageTarget != nil {
		target = *stageTarget + s.opts.jitter*math.Sin(s.now/700)
	}

	if s.opts.alwaysHold {
		if !s.fing.holding {
			s.press(true)
		}
		return
	}
	if s.opts.target == nil {
		return
	}

	half := s.opts.hysteresis / 2
	want := s.fing.holding
	if s.car.Force <= target-half {
		want = true
	} else if s.car.Force >= target+half {
		want = false
	}
	// 反应期内目标又转回当前状态：撤销这次待执行的动作，别多做一次空拍
	if s.pending != nil && want == s.fing.holding {
		s.pending = nil
	}
	if want != s.fing.holding && s.pending == nil {
		s.pending = &pendingAction{down: want, at: s.now + s.opts.reactionMs}
	}
	if s.pending != nil && s.now >= s.pending.at {
		s.press(s.pending.down)
		s.pending = nil
	}
}

// press 模拟一次按下/抬起；按下后按 repeatMs 安排下一次连发事件。
func (s *simRunner) press(down bool) {
	s.fing.holding = down
	if !down {
		return
	}
	s.chargePedalEvent()
	s.fing.nextEventAt = s.now + s.opts.repeatMs
}

// emitPedalRepeat 在按住期间按 repeatMs 持续发事件（键盘连发 / 手连续点按）。
func (s *simRunner) emitPedalRepeat() {
	if s.fing.holding && s.now >= s.fing.nextEventAt {
		s.chargePedalEvent()
		s.fing.nextEventAt = s.now + s.opts.repeatMs
	}
}

// chargePedalEvent 刷新一次按键事件，并按事件间隔重算踏板宽限期。
func (s *simRunner) chargePedalEvent() {
	gap := s.now - s.fing.lastEventAt
	s.fing.lastEventAt = s.now
	s.fing.holdUntil = s.now + pedalGrace(gap, s.car.Force)
}

/* ------------------------------- 报告 ------------------------------- */

func outcome(r Result) string {
	if r.Won {
		if r.Flew {
			if !r.Snapped {
				return "自动接管飞越 ✔"
			}
			return "飞越 ✔"
		}
		if r.Snapped {
			return "停住 ✔（断板滑停）"
		}
		return "停住 ✔"
	}
	end := fmt.Sprintf("%.0f km/h", r.Speed*3.6) // 结束速度：撞上前车的速度
	prefix := "撞前车 ✘"
	if r.Reason == ReasonSnapped {
		prefix = "断板撞车 ✘"
	}
	return prefix + "  " + end
}

var simHead = pad("打法", 14) + pad("峰值", 8) + pad("余量", 9) +
	pad("耗时", 8) + pad("得分", 7) + "评级  结果"

func show(label string, opts simOptions) Result {
	r := run(opts)
	margin := "—"
	if r.Won {
		if r.Flew {
			margin = "飞越"
		} else {
			margin = fmt1(r.Margin) + "m"
		}
	}
	fmt.Println(
		pad(label, 14) +
			pad(fmtRound(r.PeakForce)+"N", 8) +
			pad(margin, 9) +
			pad(fmt2(r.Time)+"s", 8) +
			pad(strconv.Itoa(r.Score), 7) +
			pad(string(r.Rank), 6) +
			outcome(r))
	return r
}

// runSim 跑完整套校准与断言，返回进程退出码。
func runSim() int {
	fmt.Printf("前车 %s m | 初速 %s m/s | 涨 %s N/s | 衰 %s N/s | 极限 %s N\n",
		jsNum(Phys.ObstacleDistance), jsNum(Phys.StartSpeed), jsNum(Phys.RampRate),
		jsNum(Phys.ForceDecay), jsNum(Phys.Limit))
	fmt.Println(simHead)
	fmt.Println(strings.Repeat("-", utf8.RuneCountInString(simHead)+6))

	auto := simOptions{repeatMs: 33, reactionMs: 90, hysteresis: 70, jitter: 20}

	show("不踩", simOptions{repeatMs: 33, reactionMs: 0, hysteresis: 0})
	show("按死", simOptions{alwaysHold: true, repeatMs: 33, reactionMs: 0, hysteresis: 0})

	fmt.Println("")
	fmt.Println("—— 30Hz 连发键盘 ——")
	for _, t := range []float64{800, 900, 1000, 1100, 1200, 1300, 1400, 1430, 1460, 1500} {
		o := auto
		o.target = fptr(t)
		show("骑 "+jsNum(t)+" N", o)
	}

	fmt.Println("")
	fmt.Println("—— 键盘不连发，只能手点（每 250ms 一下）——")
	for _, t := range []float64{1000, 1200, 1400} {
		show("手点 "+jsNum(t)+" N", simOptions{target: fptr(t), repeatMs: 250, reactionMs: 90, hysteresis: 70, jitter: 20})
	}

	fmt.Println("")
	fmt.Println("—— 稳手贴线（反应 40ms / 死区 30N），证明 S 可达 ——")
	for _, t := range []float64{1500, 1540, 1570} {
		show("贴线 "+jsNum(t)+" N", simOptions{target: fptr(t), repeatMs: 33, reactionMs: 40, hysteresis: 30, jitter: 15})
	}

	fmt.Println("")
	fmt.Println("—— 断板接飞车（断板后接管会飞的前车：W 拉升 / S 下降，没撞上就算赢）——")
	noFly := show("断板不拉升", simOptions{alwaysHold: true, repeatMs: 33, reactionMs: 0, hysteresis: 0})
	tooEarly := show("断板过早拉升", simOptions{alwaysHold: true, repeatMs: 33, reactionMs: 0, hysteresis: 0, fly: true, flyLead: 10})
	flyOK := show("断板及时拉升", simOptions{alwaysHold: true, repeatMs: 33, reactionMs: 0, hysteresis: 0, fly: true, flyLead: 0.5})
	show("断板贴脸才拉", simOptions{alwaysHold: true, repeatMs: 33, reactionMs: 0, hysteresis: 0, fly: true, flyLead: 0.05})

	fmt.Println("")
	fmt.Println("—— 自动接管（踏板没断，但 3 秒内必然撞上）——")
	show("不踩但会拉升", simOptions{repeatMs: 33, fly: true, flyLead: 0.5})

	fmt.Println("")
	fmt.Println("评级线：S >= 2750 > A >= 2200 > B >= 1600 > C")

	/* ------------------------------ 断言 ------------------------------ */

	noBrake := run(simOptions{repeatMs: 33, reactionMs: 0, hysteresis: 0})
	fullBrake := run(simOptions{alwaysHold: true, repeatMs: 33, reactionMs: 0, hysteresis: 0})
	var failures []string
	if noBrake.Reason != ReasonHit {
		failures = append(failures, "「不踩」应当撞前车")
	}
	if fullBrake.Reason != ReasonSnapped || fullBrake.Won {
		failures = append(failures, "「按死」应当断板并撞上前车")
	}
	if fullBrake.Won {
		failures = append(failures, "「按死」不拉升不应当赢")
	}
	{
		o := auto
		o.target = fptr(1000)
		if !run(o).Won {
			failures = append(failures, "「骑 1000 N」应当能停住")
		}
	}
	if !noFly.Won && noFly.Reason != ReasonSnapped {
		failures = append(failures, "「断板不拉升」应当断板（并撞上前车）")
	}
	if !noFly.Snapped || noFly.Won {
		failures = append(failures, "「断板不拉升」应当断板且没撞上不算赢")
	}
	if !(flyOK.Won && flyOK.Reason == ReasonFlewOver) {
		failures = append(failures, "「断板及时拉升」应当飞越前车取胜")
	}
	if tooEarly.Won {
		failures = append(failures, "「断板过早拉升」不应当赢（悬空 3s 会在飞越前耗尽）")
	}

	// 滑行阻力回归：踏板力为 0 时速度必须下降（否则断板后又变成匀速滑行）
	coast := createCar()
	coast.Force = 0
	coastV0 := coast.Speed
	stepPhysics(&coast, simDT, false)
	if !(coast.Speed < coastV0) {
		failures = append(failures, "滑行时应当有阻力，速度必须下降")
	}

	fmt.Println("")
	fmt.Printf("滑行阻力：初速 %s m/s → 1 帧后 %s m/s\n", fmt1(coastV0), fmt2(coast.Speed))

	fmt.Println("")
	if len(failures) > 0 {
		fmt.Fprintln(os.Stderr, "断言失败：\n  - "+strings.Join(failures, "\n  - "))
		return 1
	}
	fmt.Println("断言通过：不踩必撞、按死断板必撞、骑 1000 N 可稳定停住、断板及时拉升可飞越。")
	return 0
}
