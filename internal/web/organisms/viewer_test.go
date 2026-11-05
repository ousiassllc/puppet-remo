package organisms_test

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// 状態駆動レンダリング（frontend.md §4.3）: リースパネルは、view の Can* が true の操作だけを HTML に出す。
// 保持者の種別・名前・状態は常に出し、UI から Ctrl+Alt+Del は送れない旨を明記する（FR-W08）。
func TestViewerLeasePanelRendersOnlyAvailableActions(t *testing.T) {
	const (
		acquire = `hx-post="/viewer/lease/acquire"`
		release = `hx-post="/viewer/lease/release"`
		revoke  = `hx-get="/viewer/lease/revoke/confirm"`
	)
	tests := []struct {
		name string
		v    view.ViewerLease
		has  []string
		not  []string
	}{
		{"空き", view.ViewerLease{CanAcquire: true}, []string{acquire, "空き", `data-lease-state="idle"`}, []string{release, revoke, "あなた"}},
		{"自分が保持", view.ViewerLease{Held: true, Mine: true, HolderLabel: "alice", KindLabel: "web", CanRelease: true},
			[]string{release, "alice", ">web<", "保持中", "あなた", `data-lease-state="mine"`}, []string{acquire, revoke}},
		{"他者が保持（剥奪できる）", view.ViewerLease{Held: true, HolderLabel: "bot", KindLabel: "mcp", CanRevoke: true},
			[]string{revoke, "bot", ">mcp<", `data-lease-state="other"`}, []string{acquire, release, "あなた"}},
		{"他者が保持（剥奪できない）", view.ViewerLease{Held: true, HolderLabel: "agent", KindLabel: "agent"},
			[]string{"agent", "保持中"}, []string{acquire, release, revoke, "<button", "data-lease-task"}},
		{"agent のタスク実行中", view.ViewerLease{Held: true, HolderLabel: "bot", KindLabel: "agent", CanRevoke: true, Task: &view.ViewerTask{
			Status: "実行中", Steps: "3 / 50", Elapsed: "45秒", MaxTime: "10分", StartedAt: "09:00:00", Requester: "<b>req</b>"}},
			[]string{revoke, "data-lease-task", `<h3 class="text-base font-semibold">AI エージェントのタスク実行中</h3>`, `<dl class="space-y-1 text-sm">`, "実行中", "3 / 50", "45秒（上限 10分）", "09:00:00", "&lt;b&gt;req&lt;/b&gt;"}, []string{"<b>req</b>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := webtest.Render(t, organisms.ViewerLeasePanel(tt.v))
			has(t, out, tt.has...)
			has(t, out, `id="lease-panel"`, `hx-get="/viewer/lease"`, `hx-trigger="remo:lease-changed from:#viewer-root"`, "Ctrl+Alt+Del は送れません")
			hasNot(t, out, tt.not...)
			if hiddenAttr.MatchString(out) {
				t.Errorf("hidden/disabled attribute rendered:\n%s", out)
			}
			webtest.AssertCSPCompliantHTML(t, out)
		})
	}
}

// クリップボードとショートカット送信は、操作権を持つとき（Enabled）だけ描画する。navigator.clipboard は使わない（HTTP 配信）。
func TestViewerOpsRendersOnlyWithTheLease(t *testing.T) {
	off := webtest.Render(t, organisms.ViewerOps(view.ViewerOps{Keys: []view.KeyMenuItem{{Label: "Alt+Tab", Keys: "alt+tab"}}}))
	has(t, off, `id="viewer-ops"`)
	hasNot(t, off, "clipboard", "key-menu", "/viewer/keys/send", "<button")

	on := webtest.Render(t, organisms.ViewerOps(view.ViewerOps{Enabled: true, Keys: []view.KeyMenuItem{{Label: "Alt+Tab", Keys: "alt+tab"}, {Label: "Esc", Keys: "escape"}}}))
	has(t, on, `id="clipboard-panel"`, `hx-post="/viewer/clipboard/send"`, `hx-post="/viewer/clipboard/fetch"`, `name="text"`,
		`id="key-menu"`, `hx-post="/viewer/keys/send"`, `hx-vals="{&#34;keys&#34;:&#34;alt+tab&#34;}"`, ">Alt+Tab<", ">Esc<")
	hasNot(t, on, "navigator.clipboard", "delete")
	webtest.AssertCSPCompliantHTML(t, on)

	oob := webtest.Render(t, organisms.ViewerOpsOOB(view.ViewerOps{}))
	has(t, oob, `id="viewer-ops"`, `hx-swap-oob="true"`)
}

func TestViewerToolbarRendersFieldsAndErrors(t *testing.T) {
	v := view.ViewerToolbar{
		Monitors: []view.Option{{Value: "0", Label: "モニター 1", Selected: false}, {Value: "1", Label: "モニター 2", Selected: true}},
		Quality:  "70", FPS: "10",
	}
	out := webtest.Render(t, organisms.ViewerToolbar(v))
	has(t, out, "rounded-lg border border-border bg-surface p-4") // S2: 小さいカードの padding は 16
	has(t, out, `id="viewer-toolbar"`, `hx-post="/viewer/settings"`, `hx-target-422="this"`, `<option value="1" selected>`, `name="quality"`, `min="1"`, `max="100"`,
		`name="fps"`, `max="30"`, `value="70"`, `value="10"`, " novalidate")
	hasNot(t, out, "data-field-error", "data-error-code")
	// スライダーの値（<output>）は補助の --mute。スライダー自体は入力欄の枠を持たない（P1）。
	has(t, out, `<output id="viewer-quality-out" for="viewer-quality" class="w-8 text-sm font-mono text-mute">`, `type="range" value="70" class="w-full min-w-0"`)
	hasNot(t, out, `text-sm font-mono text-ink`)
	webtest.AssertCSPCompliantHTML(t, out)

	v.Errors = view.ViewerToolbarErrors{Quality: "1〜100 の整数を指定してください。"}
	out = webtest.Render(t, organisms.ViewerToolbar(v))
	has(t, out, `data-field-error="viewer-quality"`, `data-error-code="invalid_argument"`, "1〜100 の整数を指定してください。", `aria-invalid`)
	hasNot(t, out, `data-field-error="viewer-fps"`)
}

func TestViewerRootPlacesTheViewerOutsideHTMXTargets(t *testing.T) {
	v := view.Viewer{ViewerID: "VID1", WSURL: "ws://h:1/ws/screen?viewer_id=VID1", Monitor: 1, Quality: 70, FPS: 10,
		Lease: view.ViewerLease{CanAcquire: true}}
	out := webtest.Render(t, organisms.ViewerRoot(v))
	has(t, out, `<main id="viewer-root"`, `X-Viewer-Id`, `VID1`, `<remo-screen-viewer`, `ws-url="ws://h:1/ws/screen?viewer_id=VID1"`,
		`monitor="1"`, `quality="70"`, `fps="10"`, " readonly")
	webtest.AssertCSPCompliantHTML(t, out)

	v.Lease = view.ViewerLease{Held: true, Mine: true, CanRelease: true}
	out = webtest.Render(t, organisms.ViewerRoot(v))
	hasNot(t, out, " readonly")
}
