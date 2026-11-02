package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// stubJudge は固定の判定を返す（呼び出しを数える）。fn があればそれが優先。
type stubJudge struct {
	v     domain.Verdict
	r     Reason
	err   error
	fn    func(ctx context.Context, a domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error)
	calls atomic.Int32
	last  atomic.Value // domain.WindowInfo
}

func (j *stubJudge) Assess(ctx context.Context, a domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
	j.calls.Add(1)
	j.last.Store(w)
	if j.fn != nil {
		return j.fn(ctx, a, w)
	}
	return j.v, j.r, j.err
}

var testAction = domain.Action{Op: domain.TypeText{Text: "x"}, Source: "agent"}

func TestChainTakesTheStrictestVerdict(t *testing.T) {
	allow := &stubJudge{v: domain.VerdictAllow}
	approve := &stubJudge{v: domain.VerdictRequireApproval, r: Reason{Code: "a"}}
	deny := &stubJudge{v: domain.VerdictDeny, r: Reason{Code: "d"}}
	failing := &stubJudge{err: errors.New("boom")}
	tests := []struct {
		name     string
		js       []Judge
		want     domain.Verdict
		wantCode string
		wantErr  bool
	}{
		{"empty is allow", nil, domain.VerdictAllow, "", false},
		{"nil judges are ignored", []Judge{nil, allow}, domain.VerdictAllow, "", false},
		{"allow only", []Judge{allow, allow}, domain.VerdictAllow, "", false},
		{"approve beats allow", []Judge{allow, approve}, domain.VerdictRequireApproval, "a", false},
		{"allow cannot loosen a later chain member", []Judge{approve, allow}, domain.VerdictRequireApproval, "a", false},
		{"deny beats approve", []Judge{approve, deny}, domain.VerdictDeny, "d", false},
		{"deny first", []Judge{deny, allow}, domain.VerdictDeny, "d", false},
		{"a failure requires approval and is reported", []Judge{allow, failing}, domain.VerdictRequireApproval, JudgeReasonError, true},
		{"deny beats a failure", []Judge{failing, deny}, domain.VerdictDeny, "d", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, r, err := Chain(tt.js...).Assess(context.Background(), testAction, domain.WindowInfo{})
			if v != tt.want || r.Code != tt.wantCode || (err != nil) != tt.wantErr {
				t.Fatalf("got (%v, %q, %v), want (%v, %q, err=%v)", v, r.Code, err, tt.want, tt.wantCode, tt.wantErr)
			}
		})
	}
}

func TestChainTreatsAnUnknownVerdictAsRequireApproval(t *testing.T) {
	v, r, err := Chain(&stubJudge{v: domain.Verdict(99)}).Assess(context.Background(), testAction, domain.WindowInfo{})
	if v != domain.VerdictRequireApproval || r.Code != JudgeReasonInvalid || err != nil {
		t.Fatalf("got (%v, %q, %v)", v, r.Code, err)
	}
}

func TestFailClosed(t *testing.T) {
	// 文脈を無視する Judge は、テストの終了まで待たせる（固定時間の sleep ではなく、チャネルで解く）。
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	tests := []struct {
		name     string
		j        Judge
		timeout  time.Duration
		want     domain.Verdict
		wantCode string
	}{
		{"allow passes", &stubJudge{v: domain.VerdictAllow}, time.Second, domain.VerdictAllow, ""},
		{"approve passes", &stubJudge{v: domain.VerdictRequireApproval, r: Reason{Code: "a"}}, time.Second, domain.VerdictRequireApproval, "a"},
		{"deny is never loosened", &stubJudge{v: domain.VerdictDeny, r: Reason{Code: "d"}}, time.Second, domain.VerdictDeny, "d"},
		{"error becomes approval", &stubJudge{err: errors.New("boom")}, time.Second, domain.VerdictRequireApproval, JudgeReasonError},
		{"error with an allow verdict still requires approval", &stubJudge{v: domain.VerdictAllow, err: errors.New("boom")}, time.Second, domain.VerdictRequireApproval, JudgeReasonError},
		{"unknown verdict becomes approval", &stubJudge{v: domain.Verdict(-1)}, time.Second, domain.VerdictRequireApproval, JudgeReasonInvalid},
		{"panic becomes approval", &stubJudge{fn: func(context.Context, domain.Action, domain.WindowInfo) (domain.Verdict, Reason, error) {
			panic("boom")
		}}, time.Second, domain.VerdictRequireApproval, JudgeReasonError},
		{"a judge that ignores its context times out", &stubJudge{fn: func(ctx context.Context, _ domain.Action, _ domain.WindowInfo) (domain.Verdict, Reason, error) {
			<-release
			return domain.VerdictAllow, Reason{}, nil
		}}, 20 * time.Millisecond, domain.VerdictRequireApproval, JudgeReasonTimeout},
		{"a judge that honours its context times out", &stubJudge{fn: func(ctx context.Context, _ domain.Action, _ domain.WindowInfo) (domain.Verdict, Reason, error) {
			<-ctx.Done()
			return domain.VerdictAllow, Reason{}, ctx.Err()
		}}, 20 * time.Millisecond, domain.VerdictRequireApproval, JudgeReasonTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, r, err := FailClosed(tt.j, tt.timeout).Assess(context.Background(), testAction, domain.WindowInfo{})
			if err != nil {
				t.Fatalf("FailClosed must not return an error: %v", err)
			}
			if v != tt.want || r.Code != tt.wantCode {
				t.Fatalf("got (%v, %q), want (%v, %q)", v, r.Code, tt.want, tt.wantCode)
			}
		})
	}
}

func TestFailClosedCancelledContextRequiresApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := &stubJudge{fn: func(ctx context.Context, _ domain.Action, _ domain.WindowInfo) (domain.Verdict, Reason, error) {
		<-ctx.Done()
		return domain.VerdictAllow, Reason{}, nil
	}}
	v, _, _ := FailClosed(j, 0).Assess(ctx, testAction, domain.WindowInfo{})
	if v != domain.VerdictRequireApproval {
		t.Fatalf("verdict = %v, want require_approval", v)
	}
}

// agentSettingsEnv は ctlSettings・FakeSecretStore の上の AgentSettingsService。
func newTestAgentSettings(t *testing.T) (*AgentSettingsService, *ctlSettings, *platformtest.FakeSecretStore, *ctlAudit) {
	t.Helper()
	set, secrets, audit := newCtlSettings(), platformtest.NewFakeSecretStore(), &ctlAudit{}
	s, err := NewAgentSettings(AgentSettingsConfig{Settings: set, Secrets: secrets, Audit: audit, Clock: newCtlClock()})
	if err != nil {
		t.Fatal(err)
	}
	return s, set, secrets, audit
}

func TestRuleJudge(t *testing.T) {
	s, set, _, _ := newTestAgentSettings(t)
	set.put(SettingAgentDenyProcesses, `["KeePass.exe"," Secret.EXE ","C:\\Tools\\Vault.exe",""]`)
	set.put(SettingAgentApproveProcesses, `["notepad.exe","keepass.exe"]`)
	j := NewRuleJudge(s)
	tests := []struct {
		name     string
		process  string
		want     domain.Verdict
		wantCode string
	}{
		{"deny exact", "KeePass.exe", domain.VerdictDeny, JudgeReasonDenyProcess},
		{"deny is case-insensitive", "KEEPASS.EXE", domain.VerdictDeny, JudgeReasonDenyProcess},
		{"configured entry is trimmed and case-folded", "secret.exe", domain.VerdictDeny, JudgeReasonDenyProcess},
		{"a configured path is reduced to its image name", "vault.exe", domain.VerdictDeny, JudgeReasonDenyProcess},
		{"a path in the window's process is reduced too", `C:\Program Files\KeePass\KeePass.exe`, domain.VerdictDeny, JudgeReasonDenyProcess},
		{"approve", "Notepad.exe", domain.VerdictRequireApproval, JudgeReasonApproveProcess},
		{"deny wins over approve", "keepass.exe", domain.VerdictDeny, JudgeReasonDenyProcess},
		{"no partial match", "keepass", domain.VerdictAllow, ""},
		{"no suffix match", "notkeepass.exe", domain.VerdictAllow, ""},
		{"other process", "calc.exe", domain.VerdictAllow, ""},
		{"desktop (no process) is allowed", "", domain.VerdictAllow, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, r, err := j.Assess(context.Background(), testAction, domain.WindowInfo{Process: tt.process})
			if err != nil || v != tt.want || r.Code != tt.wantCode {
				t.Fatalf("got (%v, %q, %v), want (%v, %q)", v, r.Code, err, tt.want, tt.wantCode)
			}
		})
	}
}

// 設定は Assess のたびに読む（GUI の変更が次のアクションから効く）。
func TestRuleJudgeReadsTheConfigOnEveryAssess(t *testing.T) {
	s, set, _, _ := newTestAgentSettings(t)
	j := NewRuleJudge(s)
	w := domain.WindowInfo{Process: "calc.exe"}
	if v, _, _ := j.Assess(context.Background(), testAction, w); v != domain.VerdictAllow {
		t.Fatalf("before = %v", v)
	}
	set.put(SettingAgentDenyProcesses, `["calc.exe"]`)
	if v, _, _ := j.Assess(context.Background(), testAction, w); v != domain.VerdictDeny {
		t.Fatalf("after the change = %v, want deny", v)
	}
	set.put(SettingAgentDenyProcesses, `[]`)
	set.put(SettingAgentApproveProcesses, `["calc.exe"]`)
	if v, _, _ := j.Assess(context.Background(), testAction, w); v != domain.VerdictRequireApproval {
		t.Fatalf("approve list = %v, want require_approval", v)
	}
}

// 既定の拒否リスト（パスワードマネージャー）は設定が無くても効く。
func TestRuleJudgeDefaultDenyList(t *testing.T) {
	s, _, _, _ := newTestAgentSettings(t)
	for _, p := range []string{"KeePass.exe", "KeePassXC.exe", "1Password.exe", "Bitwarden.exe"} {
		if v, _, _ := NewRuleJudge(s).Assess(context.Background(), testAction, domain.WindowInfo{Process: p}); v != domain.VerdictDeny {
			t.Errorf("%s = %v, want deny", p, v)
		}
	}
}

// 設定を読めない（保存の失敗）ときは緩めず、RequireApproval と error を返す。FailClosed が error を吸収する。
func TestRuleJudgeFailsClosedWhenTheConfigIsUnreadable(t *testing.T) {
	set := failingSettings{Settings: newCtlSettings()}
	s, err := NewAgentSettings(AgentSettingsConfig{Settings: set, Secrets: platformtest.NewFakeSecretStore(), Clock: newCtlClock()})
	if err != nil {
		t.Fatal(err)
	}
	j := NewRuleJudge(s)
	v, r, err := j.Assess(context.Background(), testAction, domain.WindowInfo{Process: "calc.exe"})
	if v != domain.VerdictRequireApproval || err == nil || r.Code != JudgeReasonError {
		t.Fatalf("got (%v, %q, %v)", v, r.Code, err)
	}
	v, _, err = FailClosed(j, time.Second).Assess(context.Background(), testAction, domain.WindowInfo{Process: "calc.exe"})
	if v != domain.VerdictRequireApproval || err != nil {
		t.Fatalf("FailClosed: got (%v, %v)", v, err)
	}
}
