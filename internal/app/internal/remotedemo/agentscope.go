package main

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/service/agenttest"
)

// このファイルは -agent-demo -agent-scope=save|foreign の部品（#158。window_id つきの run_task の台本）。
// 偽のメモ帳（platformtest.FakeApp。T はメインウィンドウ window_id "1001"）を入力の宛先・グループ撮影の対象にする。
// 偽 platform の台本で、Windows 実機の成立性ゲート（S1〜S3）の代わりにならない（実機未確認。docs/operations/smoke-windows.md §3.10）。
//
//   - save: ① メニューバーの「ファイル」→ 項目の 2 回のクリックを 1 回の応答に入れる → 1 つ目でメニューが開いたので 2 つ目は実行されず中断
//     （agent.batch_yielded）→ ② 撮り直した画像で項目をクリック → 保存ダイアログが開く → ③ ファイル名の入力と Enter → ④ 最終回答
//   - foreign: 最初の応答のあと、別プロセスのウィンドウが前面に出る → キー入力は注入されず failed（input_blocked / target_not_foreground）

// scopeDemoScene は -agent-scope の偽の画面（偽のメモ帳と、前面に出す別のウィンドウ）。
type scopeDemoScene struct {
	windows *platformtest.FakeWindows
	app     *platformtest.FakeApp
	input   *platformtest.FakeInput
	capture *platformtest.FakeCapturer
}

// scopeDemoMonitor はデモのモニター（最大辺 1568 に収まるので縮小しない）。
var scopeDemoMonitor = domain.Monitor{Index: 0, Name: "demo-monitor", Primary: true, Size: domain.Size{W: 1024, H: 768}, DPI: 96}

func newScopeDemoScene(clip *platformtest.FakeClipboard) *scopeDemoScene {
	w := platformtest.NewFakeWindows()
	in := platformtest.NewFakeInput()
	c := platformtest.NewFakeCapturer(scopeDemoMonitor)
	a := platformtest.NewFakeApp(w, in, clip)
	c.AttachWindows(w)
	return &scopeDemoScene{windows: w, app: a, input: in, capture: c}
}

// scopeDemoModel は -agent-scope の台本どおりに答える偽の openai.Model。座標は合成画像（外接矩形の左上が原点。縮小しない）上の値。
type scopeDemoModel struct {
	scene *scopeDemoScene
	mode  string        // save | foreign
	delay time.Duration // 0 は demoStepDelay（テストだけが短くする）

	mu   sync.Mutex
	hist *agenttest.History
	n    int
}

func (m *scopeDemoModel) step(h *agenttest.History) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h != m.hist {
		m.hist, m.n = h, 0
	}
	m.n++
	return m.n
}

func scopeClick(x, y int) domain.Op {
	return domain.Click{At: &domain.Point{X: x, Y: y}, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}
}

func (m *scopeDemoModel) Step(ctx context.Context, in agenttest.StepInput) (agenttest.StepOutput, error) {
	select {
	case <-time.After(cmp.Or(m.delay, demoStepDelay)):
	case <-ctx.Done():
		return agenttest.StepOutput{}, ctx.Err()
	}
	n := m.step(in.History)
	if m.mode == "foreign" {
		if n == 1 {
			// 別プロセスのウィンドウが前面に出る（撮影のあと、入力の前）。
			m.scene.windows.Add(domain.WindowInfo{
				ID: "9001", Title: "Other app (demo)", Process: "other.exe", PID: 7000, ProcessStart: 7000, Class: "OtherClass",
				Rect: domain.Rect{X: 700, Y: 300, W: 220, H: 160}, Foreground: true,
			})
			return agenttest.Call("call_scope_1", domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}}), nil
		}
		return agenttest.Final("(not reached)"), nil
	}
	// T の矩形は (100,100)-(500,400)。「ファイル」は物理 (125,115)・項目は (180,137)。メニューが T の内側なので、外接矩形の原点は (100,100)。
	switch n {
	case 1:
		return agenttest.Call("call_scope_1", scopeClick(25, 15), scopeClick(80, 37)), nil
	case 2:
		return agenttest.Call("call_scope_2", scopeClick(80, 37)), nil
	case 3:
		return agenttest.Call("call_scope_3", domain.TypeText{Text: "demo-note.txt"}, domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}}), nil
	}
	return agenttest.Final("Saved the file through File > Save as. (demo, fake platform)"), nil
}
