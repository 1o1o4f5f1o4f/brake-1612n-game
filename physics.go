package main

import "math"

/*
 * 刹不住的 1612N —— 物理 & 数值
 *
 * 刹车力是一个连续量：按住空格 → 力按 RampRate 上涨；松手 → 按 ForceDecay 衰减。
 * 一旦超过 Limit，踏板断裂。
 *
 * 滑行阻力（滚阻 + 风阻）始终生效：踏板断了或完全松开后，车会缓慢减速，而不是匀速滑行。
 *
 * 想调难度，改下面的 Phys：
 *   RampRate ↑         更难（涨得快，来不及松手）
 *   ForceDecay ↑       更难（掉得快，得一直踩着才够力）
 *   ObstacleDistance ↑ / StartSpeed ↑   需要更高的平均刹车力
 *   RollForce / AeroC2 ↑                滑行阻力更大（主要影响断板后的滑行）
 *
 * 注意：不要随意改动运算顺序，否则闭环模拟（sim.go）的结果会变。
 */

// PhysConst 物理与数值常量。
type PhysConst struct {
	Mass             float64 // 车辆质量 (kg)
	Boost            float64 // 真空助力比：把踏板上的力放大成实际制动力
	RollForce        float64 // 滚阻 + 传动损耗：恒定的滑行阻力 (N)，约 0.012 × m·g
	AeroC2           float64 // 空气阻力系数 ½·ρ·Cd·A (N/(m/s)²)：30 m/s 时约 380 N
	Limit            float64 // 踏板断裂阈值 (N) —— 超过它，啪。
	StartSpeed       float64 // 刹车段初速 (m/s) —— 30 m/s ≈ 108 km/h
	ObstacleDistance float64 // 从「开始刹车」到前车的距离 (m)
	RampRate         float64 // 踩下踏板时刹车力的上涨速率 (N/s)
	ForceDecay       float64 // 松开踏板时刹车力的衰减速率 (N/s)
	WarnForce        float64 // 进入危险警告的力 (N)
	PanicForce       float64 // 千钧一发：马上要断 (N)
	MinStopSpeed     float64 // 低于这个速度就算停住 (m/s)
	DriveDuration    float64 // 驾驶段时长 (s)
	DriveSpeedFrom   float64 // 驾驶段起始速度 (m/s)
	DriveGapFrom     float64 // 驾驶段屏幕上显示的起始距离 (m)

	// —— 飞车段（断板后接管的「前车」）——
	FrontCarHeight float64 // 前车高度 (m)：飞越时高度必须 ≥ 它
	MaxAltitude    float64 // 最高可拉升高度 (m)
	ClimbRate      float64 // 拉升速度 (m/s)
	DescendRate    float64 // 下降速度 (m/s)
	MaxHover       float64 // 单次悬空时长上限 (s)，耗尽后强制下降
	HoverCooldown  float64 // 悬空耗尽后的冷却 (s)，冷却结束可再次升起
	FlyArmTTC      float64 // 断板时若「到前车的时间」≤ 它，就来不及飞，直接撞上
	SnapPause      float64 // 断板后接管飞车前的缓冲时长 (s)：物理冻结，只走倒计时
	AutoFlyHorizon float64 // 预测到「horizon 秒内必然撞上前车」时，即使没断板也自动接管
}

// Phys 是全局唯一的一组物理常量。
var Phys = PhysConst{
	Mass:             1260,
	Boost:            6,
	RollForce:        150,
	AeroC2:           0.42,
	Limit:            1612,
	StartSpeed:       30,
	ObstacleDistance: 150,
	RampRate:         450,
	ForceDecay:       400,
	WarnForce:        1350,
	PanicForce:       1490,
	MinStopSpeed:     0.3,
	DriveDuration:    2.4,
	DriveSpeedFrom:   22,
	DriveGapFrom:     220,

	FrontCarHeight: 1.5,
	MaxAltitude:    3,
	ClimbRate:      6,
	DescendRate:    6,
	MaxHover:       3,
	HoverCooldown:  2,
	FlyArmTTC:      0.5,
	SnapPause:      3,
	AutoFlyHorizon: 3,
}

// PedalParams 踏板输入的时间参数（毫秒）。
//
// 终端不会给你可靠的「松键」事件，所以我们靠键盘连发来推断：
//   - 连续两个事件间隔 < GroupGap  => 同一个按住过程（系统连发）
//   - 间隔 >= GroupGap             => 新的一次按压
//
// 新按压的宽限期分冷热两种（见 pedalGrace）：冷启动给完整 TapGrace，它同时解决了
//  1. 单独点一下也有可见的蓄力过程（而不是瞬间跳一格）
//  2. 桥接 Windows 首次连发前约 500ms 的延迟，长按不会断断续续
//
// 热手只给 WarmGrace 上限，避免「键盘不连发、只能点按」时每点一下力就窜一大截。
type PedalParams struct {
	GroupGap    float64 // 间隔超过它就认为是新的一次按压
	TapGrace    float64 // 冷启动（还没蓄上力）的新按压宽限期
	WarmGrace   float64 // 热手（已经在蓄力）时新按压的宽限期上限
	RepeatGrace float64 // 连发时的最小宽限期（略大于常见连发间隔）
	RepeatSlack float64 // 连发宽限期 = max(RepeatGrace, 实测间隔 × 这个系数)
}

// Pedal 是全局唯一的一组踏板时间参数。
var Pedal = PedalParams{
	GroupGap:    250,
	TapGrace:    650,
	WarmGrace:   260,
	RepeatGrace: 85,
	RepeatSlack: 1.6,
}

// pedalGrace 计算一次空格事件后，「踏板保持按下」的宽限期（毫秒）。
//
// 终端收不到可靠的松键事件，所以靠事件间隔推断：
//   - 间隔 < GroupGap：同一次按住（键盘连发）→ 只给略大于实测间隔的小宽限；
//   - 间隔 >= GroupGap：新的一次按压 →
//     冷启动（force≈0）给完整 TapGrace；热手只给 WarmGrace 上限，
//     避免每次点按都把力顶飞。
func pedalGrace(gap, force float64) float64 {
	repeat := math.Max(Pedal.RepeatGrace, gap*Pedal.RepeatSlack)
	if gap < Pedal.GroupGap {
		return repeat
	}
	if force <= 1 {
		return Pedal.TapGrace
	}
	return math.Min(Pedal.WarmGrace, repeat)
}

// Phase 是游戏阶段。
type Phase string

const (
	PhaseDriving   Phase = "driving"   // 巡航段（刹车还踩不下去）
	PhaseBraking   Phase = "braking"   // 刹车段
	PhaseSnapPause Phase = "snappause" // 踏板刚断：物理冻结 3 s，准备接管飞车
	PhaseFlying    Phase = "flying"    // 踏板已断，接管会飞的前车
	PhaseStopped   Phase = "stopped"   // 停住（赢）
	PhaseCrashed   Phase = "crashed"   // 撞上前车（输）
)

// Reason 是一次结算的原因："stopped" | "hit" | "snapped" | "flew"。
type Reason string

const (
	ReasonStopped  Reason = "stopped"
	ReasonHit      Reason = "hit"      // 刹不住，撞上前车
	ReasonSnapped  Reason = "snapped"  // 断板后又撞了前车
	ReasonFlewOver Reason = "flewover" // 断板后拉升，从上方飞越前车
)

// Rank 评级：S > A > B > C > D。
type Rank string

const (
	RankS Rank = "S"
	RankA Rank = "A"
	RankB Rank = "B"
	RankC Rank = "C"
	RankD Rank = "D"
)

// CarState 是车与踏板的状态。
type CarState struct {
	Pos       float64 // 自刹车开始后行驶的距离 (m)
	Speed     float64 // 当前速度 (m/s)
	Force     float64 // 当前踏板力 (N)
	PeakForce float64 // 本局踩到过的最大踏板力 (N)
	Time      float64 // 刹车阶段已耗时 (s)

	// —— 飞车段状态 ——
	Altitude     float64 // 当前高度 (m)
	PeakAltitude float64 // 本局飞到的最大高度 (m)
	HoverLeft    float64 // 本次悬空还剩多少秒（耗尽进入冷却）
	CooldownLeft float64 // 悬空耗尽后的冷却还剩多少秒
}

// Result 是一次结算。
type Result struct {
	Won       bool
	Reason    Reason
	Snapped   bool // 本局踏板是否断过（断板后靠滑行停住也算赢，但拿不到峰值奖励）
	PeakForce float64
	Margin    float64 // 停下时离前车还剩多少米（失败时为负）
	Time      float64
	Speed     float64 // 结束时的速度 (m/s)
	Score     int
	Rank      Rank

	// —— 飞车段结算 ——
	Flew         bool    // 是否通过飞越前车取胜
	PeakAltitude float64 // 本局飞到的最大高度 (m)
	HoverLeft    float64 // 结算时剩余的悬空时长 (s)
}

// createCar 返回起步状态的车辆。
func createCar() CarState {
	return CarState{
		Pos:          0,
		Speed:        Phys.StartSpeed,
		Force:        0,
		PeakForce:    0,
		Time:         0,
		Altitude:     0,
		PeakAltitude: 0,
		HoverLeft:    Phys.MaxHover,
		CooldownLeft: 0,
	}
}

/* --------------------------- 积分原语 --------------------------- */

// pedalRate 返回踏板力每秒的变化量：踩下时上涨，松开时衰减。
func pedalRate(pedalDown bool) float64 {
	if pedalDown {
		return Phys.RampRate
	}
	return -Phys.ForceDecay
}

// rollingResistance 是始终存在的滑行阻力：滚阻恒定 + 风阻随速度平方上涨。
// 踏板断了（force = 0）之后就是它在让车缓慢减速，而不是匀速滑到前车跟前。
func rollingResistance(speed float64) float64 {
	return Phys.RollForce + Phys.AeroC2*speed*speed
}

// advanceLongitudinal 只推进纵向运动：减速度由当前踏板力与滑行阻力决定。
func advanceLongitudinal(car *CarState, dt float64) {
	decel := (car.Force*Phys.Boost + rollingResistance(car.Speed)) / Phys.Mass
	car.Speed = math.Max(0, car.Speed-decel*dt)
	car.Pos += car.Speed * dt
}

// 判定原语：到达前车 / 停住 / 到前车还剩多少秒。
func reachedObstacle(car CarState) bool { return car.Pos >= Phys.ObstacleDistance }
func stopped(car CarState) bool         { return car.Speed <= Phys.MinStopSpeed }

func timeToObstacle(car CarState) float64 {
	return (Phys.ObstacleDistance - car.Pos) / math.Max(car.Speed, 1e-6)
}

// stepPhysics 推进一个时间步。pedalDown 为 true 时踏板力上涨，否则衰减。
func stepPhysics(car *CarState, dt float64, pedalDown bool) {
	car.Time += dt
	car.Force = math.Max(0, car.Force+pedalRate(pedalDown)*dt)
	if car.Force > car.PeakForce {
		car.PeakForce = car.Force
	}
	advanceLongitudinal(car, dt)
}

// stepFlying 推进飞车段一个时间步。
//
// 踏板已断（力恒为 0），只剩滑行阻力在减速；竖直方向由玩家控制：
//   - hoverLeft > 0 且按住拉升键 → 上升；
//   - 按住下降键 → 下降；
//   - 什么都不按 → 悬停（保持高度，但照样消耗悬空时长）。
//
// 悬空时长耗尽后进入冷却：强制下降、拉升键失效；冷却结束后悬空时长回满，
// 可以再次升起（对应「强制下降后 2s 冷却，之后可再升」）。
func stepFlying(car *CarState, dt float64, ascend, descend bool) {
	car.Time += dt
	car.Force = 0

	// 悬空资源：只在空中计时；耗尽后进冷却，冷却结束回满。
	if car.CooldownLeft > 0 {
		car.CooldownLeft = math.Max(0, car.CooldownLeft-dt)
		if car.CooldownLeft == 0 {
			car.HoverLeft = Phys.MaxHover
		}
	} else if car.Altitude > 0 {
		car.HoverLeft = math.Max(0, car.HoverLeft-dt)
		if car.HoverLeft == 0 {
			car.CooldownLeft = Phys.HoverCooldown
		}
	}

	cooldown := car.CooldownLeft > 0
	switch {
	case ascend && !cooldown:
		car.Altitude += Phys.ClimbRate * dt
	case descend || cooldown:
		car.Altitude -= Phys.DescendRate * dt
	}
	car.Altitude = math.Max(0, math.Min(Phys.MaxAltitude, car.Altitude))
	if car.Altitude > car.PeakAltitude {
		car.PeakAltitude = car.Altitude
	}

	// 前进方向：踏板断了，只剩滑行阻力。
	advanceLongitudinal(car, dt)
}

// predictImpactWithin 预测在 horizon 秒内是否会撞上前车。
//
// 用当前状态外推：pedalDown 表示按当前踏板状态继续（力按 stepPhysics 的规则涨/落），
// 力一旦超过 Limit 视为踏板断裂、力归零，之后只剩滑行阻力 —— 和真实分支一致。
// 返回 true 表示「照现在这么开，horizon 秒内一定到前车」，此时即使踏板没断
// 也应该提前接管飞行汽车（见 game.tick 的自动接管分支）。
func predictImpactWithin(c CarState, horizon float64, pedalDown bool) bool {
	const dt = 1.0 / 60.0
	car := c
	snapped := false
	for t := 0.0; t < horizon-1e-9; t += dt {
		car.Force = math.Max(0, car.Force+pedalRate(pedalDown && !snapped)*dt)
		if car.Force > Phys.Limit {
			snapped = true
			car.Force = 0
		}
		advanceLongitudinal(&car, dt)
		if reachedObstacle(car) {
			return true
		}
		if stopped(car) {
			return false
		}
	}
	return false
}

// evaluate 结算一局。
func evaluate(car CarState, won bool, reason Reason, snapped bool) Result {
	margin := Phys.ObstacleDistance - car.Pos
	peak := car.PeakForce

	score := 0
	if won {
		score += 1000
		switch {
		case reason == ReasonFlewOver:
			// 飞越评分（单独一套）：飞得越高余量越大，悬空省得越多越干净。
			// 两者互相拉扯：想飞高就得多耗悬空，想省悬空就得贴地飞。
			span := math.Max(1e-6, Phys.MaxAltitude-Phys.FrontCarHeight)
			clearance := math.Min(1, math.Max(0, (car.PeakAltitude-Phys.FrontCarHeight)/span))
			hoverSave := math.Min(1, math.Max(0, car.HoverLeft/Phys.MaxHover))
			score += int(math.Round(2000*clearance)) + int(math.Round(500*hoverSave))
		case snapped:
			// 断板后连飞都没飞、靠滑停蹭到前车前：峰值奖励照旧作废，最高只能 C。
			score += int(math.Round(500 * math.Max(0, 1-math.Max(0, margin)/80)))
		default:
			// 峰值越贴近 1612 N，奖励越高（8 次方，贴线才有高分）。
			score += int(math.Round(2000 * math.Pow(math.Min(1, peak/Phys.Limit), 8)))
			// 停车位置离前车越近越好
			score += int(math.Round(500 * math.Max(0, 1-math.Max(0, margin)/80)))
		}
	}

	rank := RankD
	if won {
		switch {
		case score >= 2750:
			rank = RankS
		case score >= 2200:
			rank = RankA
		case score >= 1600:
			rank = RankB
		default:
			rank = RankC
		}
	}

	return Result{
		Won:          won,
		Reason:       reason,
		Snapped:      snapped,
		PeakForce:    peak,
		Margin:       margin,
		Time:         car.Time,
		Speed:        car.Speed,
		Score:        score,
		Rank:         rank,
		Flew:         reason == ReasonFlewOver,
		PeakAltitude: car.PeakAltitude,
		HoverLeft:    car.HoverLeft,
	}
}
