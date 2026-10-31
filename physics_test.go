package main

import (
	"math"
	"testing"
)

// 滑行阻力回归：踏板力为 0 时速度必须下降。
func TestCoastHasResistance(t *testing.T) {
	car := createCar()
	car.Force = 0
	v0 := car.Speed
	stepPhysics(&car, simDT, false)
	if !(car.Speed < v0) {
		t.Fatalf("滑行时应当有阻力：初速 %v → %v", v0, car.Speed)
	}
}

// 踏板宽限期：冷/热、连发/点按各分支的边界。
func TestPedalGrace(t *testing.T) {
	cases := []struct {
		name  string
		gap   float64
		force float64
		want  float64
	}{
		{"连发（间隔远小于 GroupGap）", 33, 0, 85},
		{"连发（间隔较大，按 1.6 倍给）", 200, 900, 320},
		{"冷启动的新按压", 250, 0, 650},
		{"热手的新按压（上限 260）", 400, 900, 260},
		{"开局第一脚（间隔无穷）", math.Inf(1), 0, 650},
		{"热手且间隔很大（仍限 260）", 1000, 900, 260},
	}
	for _, c := range cases {
		if got := pedalGrace(c.gap, c.force); got != c.want {
			t.Errorf("%s: pedalGrace(%v, %v) = %v, want %v", c.name, c.gap, c.force, got, c.want)
		}
	}
}

// 自动接管的外推判定：照当前开法会不会在 horizon 秒内撞上前车。
func TestPredictImpactWithin(t *testing.T) {
	near := CarState{Pos: Phys.ObstacleDistance - 20, Speed: 25, Force: 0}
	if !predictImpactWithin(near, Phys.AutoFlyHorizon, false) {
		t.Error("距前车 20m、25 m/s 且没踩刹车时，应预测 3 秒内撞上")
	}

	far := CarState{Pos: 0, Speed: Phys.StartSpeed, Force: 1200}
	if predictImpactWithin(far, Phys.AutoFlyHorizon, true) {
		t.Error("刚起步、刹车力足够时不应预测撞上")
	}

	coasting := CarState{Pos: 0, Speed: Phys.StartSpeed, Force: 0}
	if predictImpactWithin(coasting, Phys.AutoFlyHorizon, false) {
		t.Error("距前车 150m 起步时 3 秒内还到不了，不应触发")
	}
}

func TestEvaluateRanks(t *testing.T) {
	full := CarState{Pos: Phys.ObstacleDistance, Speed: 0, Force: 0, PeakForce: Phys.Limit}
	if r := evaluate(full, true, ReasonStopped, false); r.Score != 3500 || r.Rank != RankS {
		t.Errorf("贴线停住应为 S/3500，得到 %v/%v", r.Rank, r.Score)
	}
	if r := evaluate(full, true, ReasonStopped, true); r.Score != 1500 || r.Rank != RankC {
		t.Errorf("断板滑停应为 C/1500，得到 %v/%v", r.Rank, r.Score)
	}
	if r := evaluate(full, false, ReasonHit, false); r.Score != 0 || r.Rank != RankD || r.Won {
		t.Errorf("失败应为 D/0，得到 %v/%v", r.Rank, r.Score)
	}
	over := full
	over.PeakForce = 3000
	if r := evaluate(over, true, ReasonStopped, false); r.Score != 3500 {
		t.Errorf("峰值超限应当按极限封顶，得到 %v", r.Score)
	}
}

// 闭环模拟的参考解：与 README 的标定表对齐。
func TestSimReferenceScenarios(t *testing.T) {
	auto := simOptions{repeatMs: 33, reactionMs: 90, hysteresis: 70, jitter: 20}

	if r := run(simOptions{repeatMs: 33}); r.Reason != ReasonHit {
		t.Errorf("「不踩」应当撞前车，得到 %v", r.Reason)
	}
	if r := run(simOptions{alwaysHold: true, repeatMs: 33}); r.Reason != ReasonSnapped {
		t.Errorf("「按死」应当断踏板，得到 %v", r.Reason)
	}

	// 骑 1400 N → 峰值 1540N / 余量 42.2m / A 2624
	o := auto
	o.target = fptr(1400)
	r := run(o)
	if r.Score != 2624 || r.Rank != RankA {
		t.Errorf("「骑 1400 N」应为 A/2624，得到 %v/%v", r.Rank, r.Score)
	}
	if math.Abs(r.PeakForce-1540) > 0.5 {
		t.Errorf("「骑 1400 N」峰值应为 1540N，得到 %.1f", r.PeakForce)
	}
	if math.Abs(r.Margin-42.2) > 0.05 {
		t.Errorf("「骑 1400 N」余量应为 42.2m，得到 %.1f", r.Margin)
	}

	// 贴线 1500 N → S 可达（教学关：S 只属于贴线的人）
	o2 := simOptions{target: fptr(1500), repeatMs: 33, reactionMs: 40, hysteresis: 30, jitter: 15}
	if s := run(o2); s.Score != 3041 || s.Rank != RankS {
		t.Errorf("「贴线 1500 N」应为 S/3041，得到 %v/%v", s.Rank, s.Score)
	}

	// 断板后及时拉升 → 飞越前车（赢）
	fl := run(simOptions{alwaysHold: true, repeatMs: 33, fly: true, flyLead: 0.5})
	if !fl.Won || fl.Reason != ReasonFlewOver {
		t.Errorf("「断板及时拉升」应当飞越前车取胜，得到 %v/%v", fl.Reason, fl.Won)
	}
	// 断板后不拉升 → 撞上前车（输）
	if nf := run(simOptions{alwaysHold: true, repeatMs: 33}); nf.Won || nf.Reason != ReasonSnapped {
		t.Errorf("「断板不拉升」应当撞上前车，得到 %v/%v", nf.Reason, nf.Won)
	}
}

// 自动接管在闭环模拟里同样生效：不踩但会拉升 → 应当飞越，而不是直接撞车。
func TestAutoTakeoverInSim(t *testing.T) {
	r := run(simOptions{repeatMs: 33, fly: true, flyLead: 0.5})
	if !r.Won || r.Reason != ReasonFlewOver {
		t.Errorf("不踩但自动接管后拉升，应当飞越前车，得到 %v/won=%v", r.Reason, r.Won)
	}
	if r.Snapped {
		t.Error("自动接管不应标记为踏板断过")
	}
}

// 飞越评分：飞得越高 + 悬空省得越多，分越高（单独一套评分，可达 S）。
func TestEvaluateFlewOver(t *testing.T) {
	full := CarState{Pos: Phys.ObstacleDistance, PeakForce: Phys.Limit,
		PeakAltitude: Phys.MaxAltitude, HoverLeft: Phys.MaxHover}
	if r := evaluate(full, true, ReasonFlewOver, true); r.Score != 3500 || r.Rank != RankS || !r.Flew {
		t.Errorf("满高度满悬空飞越应为 S/3500，得到 %v/%v", r.Rank, r.Score)
	}
	low := CarState{Pos: Phys.ObstacleDistance, PeakForce: Phys.Limit,
		PeakAltitude: Phys.FrontCarHeight, HoverLeft: 0}
	if r := evaluate(low, true, ReasonFlewOver, true); r.Score != 1000 || r.Rank != RankC {
		t.Errorf("贴地飞越应为 C/1000，得到 %v/%v", r.Rank, r.Score)
	}
}

// 悬空耗尽 → 强制下降进冷却 → 冷却结束回满、可再次升起。
func TestHoverExhaustForcesDescentThenRecharges(t *testing.T) {
	car := createCar()
	car.Speed = 0

	// 拉升到顶（0.5s × 6 m/s = 3 m）
	for i := 0; i < 30; i++ {
		stepFlying(&car, simDT, true, false)
	}
	if math.Abs(car.Altitude-Phys.MaxAltitude) > 1e-6 {
		t.Fatalf("0.5s 应升到最高 %.1f m，得到 %.2f", Phys.MaxAltitude, car.Altitude)
	}

	// 继续拉升直到悬空耗尽
	for i := 0; i < 1000 && car.CooldownLeft == 0; i++ {
		stepFlying(&car, simDT, true, false)
	}
	if car.HoverLeft != 0 {
		t.Fatalf("悬空应当耗尽，得到 %.2f", car.HoverLeft)
	}
	if car.CooldownLeft <= 0 {
		t.Fatalf("悬空耗尽应进入冷却，得到 %.2f", car.CooldownLeft)
	}

	// 冷却期间强制下降，拉升键失效
	altBefore := car.Altitude
	stepFlying(&car, simDT, true, false)
	if !(car.Altitude < altBefore) {
		t.Fatalf("冷却期间应当强制下降：%.2f → %.2f", altBefore, car.Altitude)
	}

	// 冷却结束：悬空回满
	for i := 0; i < 1000 && car.CooldownLeft > 0; i++ {
		stepFlying(&car, simDT, true, false)
	}
	if car.CooldownLeft != 0 {
		t.Fatalf("冷却应当结束，得到 %.2f", car.CooldownLeft)
	}
	if car.HoverLeft != Phys.MaxHover {
		t.Fatalf("冷却结束后悬空应回满，得到 %.2f", car.HoverLeft)
	}

	// 冷却结束后可以再次升起
	car.Altitude = 0
	stepFlying(&car, simDT, true, false)
	if car.Altitude <= 0 {
		t.Fatalf("冷却结束后应当可以再次升起，得到 %.2f", car.Altitude)
	}
}
