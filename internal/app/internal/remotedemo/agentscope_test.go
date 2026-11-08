package main

import (
	"context"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/service/agenttest"
)

// -agent-scope の台本（偽のメモ帳）の応答の並び: save は click→click の 1 回目の応答のあと、中断後の click・入力・最終回答。
func TestScopeDemoModelScripts(t *testing.T) {
	scene := newScopeDemoScene(platformtest.NewFakeClipboard())
	m := &scopeDemoModel{scene: scene, mode: "save", delay: time.Nanosecond}
	h := agenttest.NewHistory()
	var kinds []int
	for range 5 {
		out, err := m.Step(context.Background(), agenttest.StepInput{History: h})
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, len(out.Actions))
		if out.CallID == "" {
			break
		}
	}
	if want := []int{2, 1, 2, 0}; len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] || kinds[2] != want[2] || kinds[3] != want[3] {
		t.Errorf("actions per response = %v, want %v", kinds, want)
	}

	// foreign: 最初の応答のとき、別プロセスのウィンドウが前面に出る。
	f := &scopeDemoModel{scene: newScopeDemoScene(nil), mode: "foreign", delay: time.Nanosecond}
	if _, err := f.Step(context.Background(), agenttest.StepInput{History: agenttest.NewHistory()}); err != nil {
		t.Fatal(err)
	}
	fg, err := f.scene.windows.Foreground()
	if err != nil || fg.Foreground.ID != "9001" || fg.Foreground.Process != "other.exe" {
		t.Errorf("foreground = %+v, %v", fg.Foreground, err)
	}
}
