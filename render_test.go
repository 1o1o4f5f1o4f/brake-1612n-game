package main

import (
	"strings"
	"testing"
)

/* 渲染回归：布局可以用肉眼调，但“每行不超宽 / 总行数不超屏”是硬约束。 */

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func sampleView(phase Phase) View {
	car := createCar()
	car.Pos = 42
	car.Speed = 21.5
	car.Force = 1450
	car.PeakForce = 1520
	car.Time = 3.2
	car.Altitude = 2.1
	car.PeakAltitude = 2.4
	car.HoverLeft = 1.6

	v := View{
		Phase:      phase,
		Car:        car,
		DriveSpeed: 30,
		DriveGap:   150,
		Odometer:   123,
		PedalHeld:  phase == PhaseBraking,
		Warn:       phase == PhaseDriving,
		PauseLeft:  1.5,
		PauseTotal: Phys.SnapPause,
	}
	switch phase {
	case PhaseFlying:
		v.AscendHeld = true
	case PhaseStopped:
		r := evaluate(car, true, ReasonStopped, false)
		v.Result = &r
	case PhaseCrashed:
		r := evaluate(car, false, ReasonHit, false)
		v.Result = &r
	}
	return v
}

// renderFrame 跑一帧并拆成行（每行已去掉行尾的 \x1b[K）。
func renderFrame(t *testing.T, v View, cols, rows int) []string {
	t.Helper()
	frame := render(v, cols, rows)
	if !strings.HasPrefix(frame, "\x1b[H") {
		t.Fatalf("[%v %dx%d] 帧应以光标归位开头", v.Phase, cols, rows)
	}
	if !strings.HasSuffix(frame, "\x1b[J") {
		t.Fatalf("[%v %dx%d] 帧应以清屏结尾", v.Phase, cols, rows)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(frame, "\x1b[H"), "\x1b[J")
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\x1b[K")
	}
	return lines
}

// 所有阶段 × 各种窗口尺寸：每行恰好 frameW 宽、总行数不超过 rows-1、绝不横向溢出。
func TestRenderFitsTerminal(t *testing.T) {
	phases := []Phase{PhaseDriving, PhaseBraking, PhaseSnapPause, PhaseFlying, PhaseStopped, PhaseCrashed}
	colsList := []int{40, 52, 60, 80, 120, 200}
	rowsList := []int{10, 12, 16, 20, 24, 30, 50}

	for _, ph := range phases {
		for _, cols := range colsList {
			for _, rows := range rowsList {
				v := sampleView(ph)
				lines := renderFrame(t, v, cols, rows)
				if len(lines) > rows-1 {
					t.Errorf("[%v %dx%d] 行数 %d 超过 %d", ph, cols, rows, len(lines), rows-1)
				}
				fw := frameWidth(cols)
				left := (cols - fw) / 2
				if left < 0 {
					left = 0
				}
				for i, l := range lines {
					if got := dw(l); got != left+fw {
						t.Errorf("[%v %dx%d] 第 %d 行宽度 %d，应为 %d", ph, cols, rows, i, got, left+fw)
					}
					if dw(stripANSI(l)) > cols {
						t.Errorf("[%v %dx%d] 第 %d 行溢出终端：%q", ph, cols, rows, i, stripANSI(l))
					}
				}
			}
		}
	}
}

// 飞车段与普通段共用同一套表盘与状态行，不再有专属边框面板。
func TestRenderFlyingLayout(t *testing.T) {
	flying := stripANSI(strings.Join(renderFrame(t, sampleView(PhaseFlying), 80, 30), "\n"))
	for _, want := range []string{"速度", "高度", "悬空", "距前车"} {
		if !strings.Contains(flying, want) {
			t.Errorf("飞车段缺少表盘 %q", want)
		}
	}
	if strings.Contains(flying, "╔") {
		t.Error("飞车段不应再使用专属面板")
	}

	paused := sampleView(PhaseSnapPause)
	body := stripANSI(strings.Join(renderFrame(t, paused, 80, 30), "\n"))
	for _, want := range []string{"速度", "缓冲", "距前车", "1.5"} {
		if !strings.Contains(body, want) {
			t.Errorf("断板缓冲缺少表盘 %q", want)
		}
	}
	// 断板接管与自动接管的缓冲文案要区分
	if !strings.Contains(body, "来不及了") {
		t.Error("自动接管的缓冲应提示「来不及了」")
	}
	snappedPause := paused
	snappedPause.Snapped = true
	if body := stripANSI(strings.Join(renderFrame(t, snappedPause, 80, 30), "\n")); !strings.Contains(body, "踏板已断") {
		t.Error("断板接管的缓冲应提示「踏板已断」")
	}
}

// 飞车表盘里的关键读数（高度 / 悬空 / 距前车）不能少。
func TestRenderFlyingPanelFields(t *testing.T) {
	v := sampleView(PhaseFlying)
	body := stripANSI(strings.Join(renderFrame(t, v, 80, 30), "\n"))
	for _, want := range []string{"高度", "悬空", "距前车", "拉升中", "可飞越"} {
		if !strings.Contains(body, want) {
			t.Errorf("飞车表盘缺少 %q", want)
		}
	}

	// 高度不够时，表盘/状态行要提示飞越条件而不是「可飞越」
	low := sampleView(PhaseFlying)
	low.Car.Altitude = 0.8
	low.AscendHeld = false
	body = stripANSI(strings.Join(renderFrame(t, low, 80, 30), "\n"))
	if !strings.Contains(body, "飞越需高度") {
		t.Error("高度不足时面板应提示飞越条件")
	}
}

// 窄窗口降级时，标题和表盘仍然保留（先删空行、提示、路面）。
func TestRenderDegradesGracefully(t *testing.T) {
	for _, ph := range []Phase{PhaseBraking, PhaseFlying} {
		body := stripANSI(strings.Join(renderFrame(t, sampleView(ph), 80, 12), "\n"))
		if !strings.Contains(body, "刹 不 住 的 1612 N") {
			t.Errorf("[%v] 小窗口下标题不应被裁掉", ph)
		}
		if ph == PhaseFlying && !strings.Contains(body, "高度") {
			t.Error("[飞车段] 小窗口下飞车表盘不应被裁掉")
		}
	}
}

func TestTruncateRespectsWidth(t *testing.T) {
	cases := []struct {
		s string
		w int
	}{
		{"刹车力", 2},
		{"刹车力", 4},
		{fgRed + "刹车力" + fgReset, 2},
		{"abc", 10},
		{"", 4},
	}
	for _, c := range cases {
		got := truncate(c.s, c.w)
		if d := dw(got); d > c.w {
			t.Errorf("truncate(%q, %d) 宽度 %d 超限", c.s, c.w, d)
		}
	}
	// 截断后必须补回 reset，别把颜色漏到后面
	if got := truncate(fgRed+"刹车力"+fgReset, 4); !strings.HasSuffix(got, fgReset) {
		t.Errorf("截断后应以 reset 结尾，得到 %q", got)
	}
	// 未超宽的串原样返回（含转义）
	if got := truncate(fgRed+"OK"+fgReset, 10); got != fgRed+"OK"+fgReset {
		t.Errorf("未超宽不应改动，得到 %q", got)
	}
}

// 供 -v 时肉眼检查版式：go test -run TestRenderSnapshot -v
func TestRenderSnapshot(t *testing.T) {
	for _, ph := range []Phase{PhaseBraking, PhaseSnapPause, PhaseFlying, PhaseStopped} {
		lines := renderFrame(t, sampleView(ph), 80, 24)
		t.Logf("\n--- %v ---\n%s", ph, strings.Join(lines, "\n"))
	}
}
