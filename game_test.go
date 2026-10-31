package main

import (
	"math"
	"testing"
)

// 巡航段 → 刹车段 → 一路踩死断板 → 接管飞车段 → 不拉升撞前车 → R 重开。
func TestGameFlowSnapThenCrash(t *testing.T) {
	game = newGame()

	// 巡航段：一直跑到切换刹车段的那一帧
	ticks := 0
	for ; ticks < 1000 && game.Phase == PhaseDriving; ticks++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseBraking {
		t.Fatalf("巡航 2.4s 后应进入刹车段，得到 %v", game.Phase)
	}
	if dt := float64(ticks) * fixedDt; dt < Phys.DriveDuration || dt > Phys.DriveDuration+fixedDt {
		t.Errorf("巡航段应约 %.2fs，得到 %.2fs", Phys.DriveDuration, dt)
	}
	if game.Car.Speed != Phys.StartSpeed {
		t.Fatalf("刹车段应以初速开始，得到 %v", game.Car.Speed)
	}

	// 一直踩住（每帧补一次按键，等价连发）：约 3.58s 后断板 → 3 s 缓冲 → 接管飞车
	snappedAt := -1.0
	for i := 0; i < 400; i++ {
		onSpace()
		tick(fixedDt)
		if game.Phase == PhaseSnapPause {
			snappedAt = game.Car.Time
			break
		}
	}
	if snappedAt < 0 {
		t.Fatalf("一直踩住应当断板并进入缓冲段：相位 %v，力 %.1f", game.Phase, game.Car.Force)
	}
	if snappedAt < 3.5 || snappedAt > 3.7 {
		t.Errorf("断板时间应约 3.58s，得到 %.2f", snappedAt)
	}
	if game.Car.Force != 0 {
		t.Errorf("断板后踏板力应归零，得到 %v", game.Car.Force)
	}

	// 缓冲期：物理完全冻结，倒计时走完才进入飞车段
	frozenPos, frozenSpeed, frozenTime := game.Car.Pos, game.Car.Speed, game.Car.Time
	if game.PauseLeft != Phys.SnapPause {
		t.Errorf("断板瞬间缓冲应为 %.1fs，得到 %.2f", Phys.SnapPause, game.PauseLeft)
	}
	pauseTicks := int(math.Round(Phys.SnapPause / fixedDt))
	for i := 0; i < pauseTicks-1; i++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseSnapPause {
		t.Fatalf("缓冲未走完不应切到飞车段，得到 %v", game.Phase)
	}
	if game.Car.Pos != frozenPos || game.Car.Speed != frozenSpeed || game.Car.Time != frozenTime {
		t.Errorf("缓冲期物理应冻结：pos %.3f→%.3f speed %.3f→%.3f time %.3f→%.3f",
			frozenPos, game.Car.Pos, frozenSpeed, game.Car.Speed, frozenTime, game.Car.Time)
	}
	tick(fixedDt)
	if game.Phase != PhaseFlying {
		t.Fatalf("缓冲结束后应进入飞车段，得到 %v", game.Phase)
	}
	if game.Car.HoverLeft != Phys.MaxHover {
		t.Errorf("进入飞车段悬空时长应回满，得到 %v", game.Car.HoverLeft)
	}
	if game.Car.Time != frozenTime {
		t.Errorf("缓冲不应推进评分计时：%.3f → %.3f", frozenTime, game.Car.Time)
	}

	// 不拉升：贴着地面滑到前车跟前 → 撞上前车（原因 snapped）
	for i := 0; i < 3000 && game.Phase == PhaseFlying; i++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseCrashed || game.FailReason != ReasonSnapped {
		t.Fatalf("断板不拉升应撞前车，得到 %v/%v", game.Phase, game.FailReason)
	}
	if game.Result == nil || game.Result.Rank != RankD {
		t.Fatalf("撞前车应拿 D 级，得到 %+v", game.Result)
	}

	// R 重开
	if handleKey('r') {
		t.Fatal("R 不应该退出")
	}
	if game.Phase != PhaseDriving || game.Result != nil {
		t.Fatalf("R 应当重开，得到 %v", game.Phase)
	}
}

// 断板 → 飞车段 → 贴脸拉升 → 从上方飞越前车（赢，单独一套飞越评分）。
func TestGameFlowSnapThenFlyOver(t *testing.T) {
	game = newGame()
	for game.Phase != PhaseBraking {
		tick(fixedDt)
	}
	for game.Phase == PhaseBraking {
		onSpace()
		tick(fixedDt)
	}
	if game.Phase != PhaseSnapPause {
		t.Fatalf("一直踩住应当先进入断板缓冲，得到 %v", game.Phase)
	}
	// 走完 3 s 缓冲：这段时间物理冻结，不能当成飞车段来推进
	for i := 0; i < 1000 && game.Phase == PhaseSnapPause; i++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseFlying {
		t.Fatalf("缓冲结束应当进入飞车段，得到 %v", game.Phase)
	}

	// 先贴地滑行，等离前车只剩 8 m 再拉升（悬空只有 3s，早了会掉下来）
	for game.Phase == PhaseFlying && Phys.ObstacleDistance-game.Car.Pos > 8 {
		tick(fixedDt)
	}
	if game.Phase != PhaseFlying {
		t.Fatalf("拉升前不应结束，得到 %v", game.Phase)
	}
	game.AscendUntil = math.Inf(1) // 模拟一直按住 W
	for i := 0; i < 3000 && game.Phase == PhaseFlying; i++ {
		tick(fixedDt)
	}

	if game.Phase != PhaseStopped || game.Result == nil {
		t.Fatalf("及时拉升应当飞越前车取胜，得到 %v", game.Phase)
	}
	if !game.Result.Won || game.Result.Reason != ReasonFlewOver || !game.Result.Flew {
		t.Fatalf("应当是飞越取胜，得到 %+v", game.Result)
	}
	if game.Result.PeakAltitude < Phys.FrontCarHeight {
		t.Errorf("飞越高度应不低于前车 %.1f m，得到 %.2f", Phys.FrontCarHeight, game.Result.PeakAltitude)
	}
}

// 断板那一刻「到前车的时间」太短就直接撞上（来不及飞）。
func TestImminentCrashSkipsFlying(t *testing.T) {
	near := CarState{Pos: Phys.ObstacleDistance - 1, Speed: 10}
	if !imminentCrash(near) {
		t.Error("贴着前车高速断板应判定为来不及飞")
	}
	far := CarState{Pos: 90, Speed: 10}
	if imminentCrash(far) {
		t.Error("离前车还远时不应判定为来不及飞")
	}
}

// 飞车段的 W/S 只在飞车段生效。
func TestVerticalKeysOnlyInFlying(t *testing.T) {
	game = newGame()
	game.Phase = PhaseBraking
	game.AscendUntil = math.Inf(-1)
	game.DescendUntil = math.Inf(-1)
	handleKey('w')
	handleKey('s')
	if game.AscendUntil != math.Inf(-1) || game.DescendUntil != math.Inf(-1) {
		t.Errorf("刹车段按 W/S 不应改变飞车按键：ascend=%v descend=%v", game.AscendUntil, game.DescendUntil)
	}
}

// 巡航段提前踩住：刹车点一到立刻生效（README 里「抢时间」的玩法）。
func TestPreHeldPedalTakesEffectAtBrakePoint(t *testing.T) {
	game = newGame()

	for i := 0; i < 120; i++ { // 2.0s
		tick(fixedDt)
	}
	if game.Phase != PhaseDriving {
		t.Fatalf("2.0s 时仍应在巡航段，得到 %v", game.Phase)
	}

	// 跨过 2.4s 刹车点，全程保持按住
	for i := 0; i < 40; i++ {
		onSpace()
		tick(fixedDt)
	}
	if game.Phase != PhaseBraking {
		t.Fatalf("应已进入刹车段，得到 %v", game.Phase)
	}
	if game.Car.Force <= 0 {
		t.Fatalf("提前踩住的踏板应当在刹车点立刻生效，力=%.1f", game.Car.Force)
	}
}

// 预测到「3 秒内必然撞上」时，即使踏板没断也自动接管飞行汽车。
func TestAutoTakeoverWhenImpactImminent(t *testing.T) {
	game = newGame()
	game.Phase = PhaseBraking
	game.Car = createCar()
	game.Car.Pos = Phys.ObstacleDistance - 20
	game.Car.Speed = 25
	game.Car.Force = 0
	game.Car.PeakForce = 800
	game.HoldUntil = math.Inf(-1) // 完全不踩

	for i := 0; i < 600 && game.Phase == PhaseBraking; i++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseSnapPause {
		t.Fatalf("预测到 3 秒内必撞时应自动接管，得到 %v", game.Phase)
	}
	if game.Snapped {
		t.Error("踏板没断，不应标记为断板")
	}
	if game.PauseLeft <= 0 || game.PauseLeft > Phys.SnapPause {
		t.Errorf("自动接管也应先走 3 秒缓冲，得到 %.2f", game.PauseLeft)
	}

	for i := 0; i < 1000 && game.Phase == PhaseSnapPause; i++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseFlying {
		t.Fatalf("缓冲结束应进入飞车段，得到 %v", game.Phase)
	}

	// 不拉升 → 撞上前车；原因应记「撞上前车」，而不是「断板撞车」
	for i := 0; i < 3000 && game.Phase == PhaseFlying; i++ {
		tick(fixedDt)
	}
	if game.Phase != PhaseCrashed || game.FailReason != ReasonHit {
		t.Fatalf("自动接管后不拉升应撞前车（ReasonHit），得到 %v/%v", game.Phase, game.FailReason)
	}
	if game.Result == nil || game.Result.Snapped {
		t.Errorf("结算不应标记为断板，得到 %+v", game.Result)
	}
}

func TestKeysOutsidePlay(t *testing.T) {
	game = newGame()
	game.Phase = PhaseCrashed
	game.Car.Force = 777

	before := game.HoldUntil
	onSpace()
	if game.HoldUntil != before {
		t.Errorf("结算画面里空格不应改变踏板状态，HoldUntil=%v", game.HoldUntil)
	}

	if quit := handleKey('q'); !quit {
		t.Error("Q 应当退出")
	}
	if quit := handleKey(0x03); !quit {
		t.Error("Ctrl+C 应当退出")
	}
}
