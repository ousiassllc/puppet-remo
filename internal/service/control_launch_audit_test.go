package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// launchOrderRecorder は Launch が呼ばれた時点で、「試行」の行が書かれていたかを控える AppLauncher。
type launchOrderRecorder struct {
	platform.AppLauncher
	audit *ctlAudit

	mu             sync.Mutex
	attemptsAtCall []int
}

func (l *launchOrderRecorder) Launch(ctx context.Context, spec platform.LaunchSpec) (uint32, error) {
	l.mu.Lock()
	l.attemptsAtCall = append(l.attemptsAtCall, len(l.audit.SyncEntries()))
	l.mu.Unlock()
	return l.AppLauncher.Launch(ctx, spec)
}

// controlWith は e の Control を、launcher と audit だけ差し替えて作る。
func (e *ctlEnv) controlWith(t *testing.T, launcher platform.AppLauncher, audit SyncAuditRecorder) Control {
	t.Helper()
	ctl, err := NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: launcher, Apps: e.apps, Leases: e.leases, Kill: e.kill, Settings: e.settings, Audit: audit, Clock: e.clock,
		Sleep: func(context.Context, time.Duration) error { return nil }, // 起動後のウィンドウの観測の待ちを実時間で待たない
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctl
}

// 起動の「試行」の行は、プロセスを作る前に同期で書かれる（mcp は kind = tool、agent は kind = agent でタスク・ステップに紐づく）。
// 試行の行は追記だけで、結果の行（tool.launch_app・agent.step）とは別に残る。パス・引数・作業ディレクトリ・pid は入らない。
func TestControlLaunchWritesTheAttemptRowBeforeStartingTheProcess(t *testing.T) {
	agent := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-1"}
	tests := []struct {
		name  string
		who   domain.Actor
		setup func(*testing.T, *ctlEnv) string // 保持者のリース ID を返す
		check func(*testing.T, repository.AuditEntry)
	}{
		{"mcp", ctlAlice, func(*testing.T, *ctlEnv) string { return "" }, func(t *testing.T, en repository.AuditEntry) {
			if en.Kind != "tool" || en.Event != "tool.launch_app.attempt" || en.ActorType != domain.ActorMCP || en.SessionID != ctlAlice.SessionID {
				t.Errorf("entry = %+v", en)
			}
		}},
		{"agent", agent, func(t *testing.T, e *ctlEnv) string { return mustAcquire(t, e, agent).ID }, func(t *testing.T, en repository.AuditEntry) {
			if en.Kind != "agent" || en.Event != "agent.launch_attempt" || en.ActorType != domain.ActorAgent || en.TaskID != agent.SessionID ||
				en.StepNo == nil || *en.StepNo != 3 {
				t.Errorf("entry = %+v", en)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAppEnv(t)
			app := registerNotepad(t, e)
			wantLease := tt.setup(t, e)
			e.audit.entries = nil
			order := &launchOrderRecorder{AppLauncher: e.launcher, audit: e.audit}
			ctl := e.controlWith(t, order, e.audit)

			ctx := WithAgentStep(context.Background(), AgentStep{No: 3, Route: AgentRouteOpenAI})
			res, err := ctl.Launch(ctx, tt.who, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(order.attemptsAtCall) != 1 || order.attemptsAtCall[0] != 1 {
				t.Fatalf("attempt rows already written when the launcher ran = %v, want [1]", order.attemptsAtCall)
			}
			rows := e.audit.SyncEntries()
			if len(rows) != 1 {
				t.Fatalf("sync rows = %+v, want exactly one attempt", rows)
			}
			en := rows[0]
			tt.check(t, en)
			h, _ := e.leases.Holder()
			if en.Tool != "launch_app" || en.Result != "ok" || en.ErrorCode != "" || en.LeaseID != h.LeaseID || (wantLease != "" && en.LeaseID != wantLease) {
				t.Errorf("entry = %+v", en)
			}
			if d := auditDetail(t, en); len(d) != 2 || d["app_id"] != app.ID || d["label"] != "メモ帳" {
				t.Errorf("detail = %v, want only app_id and label (no pid, path or arguments)", d)
			}
			assertNoSecrets(t, en, nil)
			// 結果の行は別に、非同期の経路で 1 件（試行の行をそれで置き換えない）。
			if got := lastLaunchEntry(t, e); got.Result != "ok" || got.DetailJSON == nil || auditDetail(t, got)["pid"] != float64(res.PID) {
				t.Errorf("result entry = %+v", got)
			}
			n := 0
			for _, x := range e.audit.Entries() {
				if x.Tool == "launch_app" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("launch_app result rows = %d, want 1", n)
			}
		})
	}
}

// 試行の行が書けなければ、起動しない（fail-closed）。エラーは audit_unavailable（HTTP 503）で、DB のエラーの中身は載せない。
// 結果の行は試行の行と別にそのまま書こうとし（error / audit_unavailable）、リースは奪わない。
func TestControlLaunchDoesNotStartWhenTheAttemptCannotBeWritten(t *testing.T) {
	agent := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-1"}
	for _, who := range []domain.Actor{ctlAlice, agent} {
		t.Run(string(who.Kind), func(t *testing.T) {
			e := newAppEnv(t)
			app := registerNotepad(t, e)
			if who.Kind == domain.ActorAgent {
				mustAcquire(t, e, who)
			}
			e.audit.entries = nil
			e.audit.SetSyncErr(errors.New(`insert audit_logs: database is locked (C:\secret\puppet-remo.db)`))

			_, err := e.control.Launch(context.Background(), who, app.ID)
			if !errors.Is(err, domain.ErrAuditUnavailable) || domain.CodeOf(err).HTTPStatus() != 503 {
				t.Fatalf("err = %v, want audit_unavailable (503)", err)
			}
			if n := len(e.launcher.CallsTo("Launch")); n != 0 {
				t.Fatalf("launched %d times although the attempt could not be recorded", n)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "locked") {
				t.Errorf("error leaks the storage failure: %v", err)
			}
			en := lastLaunchEntry(t, e)
			if en.Result != "error" || en.ErrorCode != "audit_unavailable" {
				t.Errorf("result entry = %+v, want error / audit_unavailable", en)
			}
			assertNoSecrets(t, en, err)

			// 書けるようになれば、同じ呼び出しで起動できる。
			e.audit.SetSyncErr(nil)
			if _, err := e.control.Launch(context.Background(), who, app.ID); err != nil {
				t.Fatalf("Launch after the audit recovered = %v", err)
			}
		})
	}
}

// 試行の行は、起動まで進む呼び出しだけが書く。キルスイッチ・リース・許可リストで先に拒否した呼び出しは、監査に書けなくても
// 元のエラーのまま（キルスイッチを audit_unavailable に化けさせない）。
func TestControlLaunchRejectedBeforeStartingKeepsItsErrorWhenTheAuditIsDown(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	e.audit.SetSyncErr(errors.New("db down"))
	ctx := context.Background()

	if _, err := e.leases.Acquire(ctx, ctlBob); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Launch(ctx, ctlAlice, app.ID); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("lease held by other: err = %v, want lease_held_by_other", err)
	}
	if _, err := e.control.Launch(ctx, ctlBob, "no-such-app"); !errors.Is(err, domain.ErrAppNotFound) {
		t.Errorf("unknown app: err = %v, want app_not_found", err)
	}
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control.Launch(ctx, ctlBob, app.ID); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("kill switch: err = %v, want kill_switch_active", err)
	}
	if n := len(e.audit.SyncEntries()); n != 0 {
		t.Errorf("attempt rows = %d, want none for calls rejected before the launch", n)
	}
}

// failingAfterStore は先頭の ok 件の Append だけ成功し、以後は失敗する repository.Audit。
type failingAfterStore struct {
	ctlAuditStore
	mu2 sync.Mutex
	ok  int
	n   int
}

func (s *failingAfterStore) Append(ctx context.Context, e repository.AuditEntry) (int64, error) {
	s.mu2.Lock()
	s.n++
	fail := s.n > s.ok
	s.mu2.Unlock()
	if fail {
		return 0, errors.New("db locked")
	}
	return s.ctlAuditStore.Append(ctx, e)
}

// 結果の行が書けなくても、起動は取り消さず（成功を返す）、運用ログに Warn を出す。試行の行は残っている。
func TestControlLaunchKeepsTheLaunchWhenTheResultRowCannotBeWritten(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	store := &failingAfterStore{ok: 1} // 1 件目（試行。同期）だけ書ける
	var logs bytes.Buffer
	audit, err := NewAudit(AuditConfig{Store: store, Clock: e.clock, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close(context.Background()) })
	ctl := e.controlWith(t, e.launcher, audit)

	res, err := ctl.Launch(context.Background(), ctlAlice, app.ID)
	if err != nil || res.PID == 0 {
		t.Fatalf("Launch = %+v, %v; want success although the result row cannot be written", res, err)
	}
	if err := audit.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 1 {
		t.Errorf("launched %d times, want 1", n)
	}
	got := store.Entries()
	if len(got) != 1 || got[0].Event != "tool.launch_app.attempt" {
		t.Errorf("stored rows = %+v, want only the attempt", got)
	}
	out := logs.String()
	if !strings.Contains(out, "audit append failed") || !strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Errorf("logs = %s, want a WARN for the lost result row", out)
	}
}

// 試行も結果も追記だけ（同じ呼び出しの 2 行は別の行で、更新しない）。実際の Audit の経路で、順に 2 行になる。
func TestControlLaunchWritesAttemptThenResultAsSeparateRows(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	store := &ctlAuditStore{}
	audit := newTestAudit(t, store, 0, nil)
	ctl := e.controlWith(t, e.launcher, audit)
	if _, err := ctl.Launch(context.Background(), ctlAlice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := audit.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.Entries()
	if len(got) != 2 || got[0].Event != "tool.launch_app.attempt" || got[1].Event != "tool.launch_app" || got[1].Result != "ok" {
		t.Fatalf("stored rows = %+v, want the attempt, then the result", got)
	}
}
