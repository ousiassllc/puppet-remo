package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Reason は Judge の判定の理由。Code は機械可読（監査の detail.judge_reason・テスト用）、Message は承認ダイアログに出す短い文。
// どちらにも入力した文字列の本文・キーの名前・パス・引数・クリップボードの内容を入れてはならない（FR-G23、NFR-S13）。
// Allow の理由は空でよい。
type Reason struct {
	Code    string
	Message string
}

// Judge の理由のコード。
const (
	JudgeReasonDenyProcess    = "deny_process"          // 対象ウィンドウのプロセスが agent.deny_processes に一致した
	JudgeReasonApproveProcess = "approve_process"       // 対象ウィンドウのプロセスが agent.approve_processes に一致した
	JudgeReasonError          = "judge_error"           // 判定の失敗（fail-closed で RequireApproval）
	JudgeReasonTimeout        = "judge_timeout"         // 判定が時間内に終わらなかった（fail-closed）
	JudgeReasonUnconfigured   = "judge_unconfigured"    // agent のアクションなのに Judge が組み込まれていない（fail-closed）
	JudgeReasonInvalid        = "judge_invalid_verdict" // 未知の判定値（fail-closed）
)

// Judge はエージェントの各アクションの直前に、実行してよいかを助言する（FR-G12、docs/components/overview.md §5.5）。
// w はアクションの対象ウィンドウ（座標のある操作はその座標のウィンドウ、キー・文字入力は前面のウィンドウ。どのウィンドウでもなければゼロ値）。
// 実装は Control が agent のときだけ呼ぶ。error は呼び出し側（FailClosed・Control）が RequireApproval に変えるので、緩い判定に倒してはならない。
// Jev（TypeSafe）は Judge ではない: 高速パスの Jev の回答もこの Judge を通り、Jev が判定を緩めることはない（NFR-S17）。
type Judge interface {
	Assess(ctx context.Context, a domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error)
}

// Chain は js を束ね、最も厳しい判定を採る（緩める方向には作用しない）。js のどれかが Deny なら Deny（そこで打ち切る）。
// 失敗した Judge があり、Deny が無ければ RequireApproval とその error を返す（Deny は失敗より優先）。
// 同じ厳しさなら先の Judge の理由を採る。nil は無視する。1 つも無ければ Allow。
func Chain(js ...Judge) Judge {
	live := make([]Judge, 0, len(js))
	for _, j := range js {
		if j != nil {
			live = append(live, j)
		}
	}
	return chain(live)
}

type chain []Judge

func (c chain) Assess(ctx context.Context, a domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
	verdict, reason := domain.VerdictAllow, Reason{}
	var firstErr error
	for _, j := range c {
		v, r, err := j.Assess(ctx, a, w)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			v, r = domain.VerdictRequireApproval, Reason{Code: JudgeReasonError, Message: "判定に失敗しました。"}
		}
		if !validVerdict(v) {
			v, r = domain.VerdictRequireApproval, Reason{Code: JudgeReasonInvalid, Message: "判定が不正でした。"}
		}
		if v > verdict {
			verdict, reason = v, r
		}
		if verdict == domain.VerdictDeny {
			return verdict, reason, nil
		}
	}
	return verdict, reason, firstErr
}

// FailClosed は j の失敗を RequireApproval に変える（fail-closed。FR-G12）。返す Judge は error を返さない:
// j の error・panic・timeout 超過・未知の判定値は RequireApproval（理由のコードは JudgeReason*）。j の判定を緩めることはなく、
// Allow・Deny・RequireApproval はそのまま返す。timeout が 0 以下なら時間では打ち切らない（ctx のキャンセルでだけ打ち切る）。
// j が ctx を無視して返らなくても、timeout で RequireApproval を返す（j の goroutine は結果を捨てて終わる）。
func FailClosed(j Judge, timeout time.Duration) Judge {
	return failClosed{j: j, timeout: timeout}
}

type failClosed struct {
	j       Judge
	timeout time.Duration
}

type judgeResult struct {
	v   domain.Verdict
	r   Reason
	err error
}

func (f failClosed) Assess(ctx context.Context, a domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
	if f.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}
	done := make(chan judgeResult, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- judgeResult{err: fmt.Errorf("judge panicked: %v", p)}
			}
		}()
		v, r, err := f.j.Assess(ctx, a, w)
		done <- judgeResult{v: v, r: r, err: err}
	}()
	select {
	case res := <-done:
		switch {
		case res.err != nil:
			return domain.VerdictRequireApproval, Reason{Code: JudgeReasonError, Message: "判定に失敗しました。"}, nil
		case !validVerdict(res.v):
			return domain.VerdictRequireApproval, Reason{Code: JudgeReasonInvalid, Message: "判定が不正でした。"}, nil
		}
		return res.v, res.r, nil
	case <-ctx.Done():
		return domain.VerdictRequireApproval, Reason{Code: JudgeReasonTimeout, Message: "判定が時間内に終わりませんでした。"}, nil
	}
}

func validVerdict(v domain.Verdict) bool {
	return v >= domain.VerdictAllow && v <= domain.VerdictDeny
}

// RuleJudge は既定の Judge（ルールベース。FR-G12、FR-S13）。対象ウィンドウのプロセス名が agent.deny_processes に
// 一致すれば Deny、agent.approve_processes に一致すれば RequireApproval（両方に一致すれば Deny）、それ以外は Allow。
// 設定は Assess のたびに読む（GUI の変更が次のアクションから効く）。設定が読めなければ RequireApproval と error（fail-closed）。
//
// プロセス名の比較は、WindowInfo.Process（プロセスのイメージ名。例 notepad.exe。パスは含まない）と設定の各項目を
// 前後の空白を除き、パスの区切り（\ と /）より後ろだけを残して、大文字小文字を区別せず完全一致で比べる。
// 部分一致・拡張子の補完はしない（keepass は KeePass.exe に一致しない）。空の項目・プロセス名が空のウィンドウ（デスクトップ）は一致しない。
type RuleJudge struct{ Config AgentConfigReader }

// NewRuleJudge は設定の窓口 cfg から RuleJudge を作る。
func NewRuleJudge(cfg AgentConfigReader) *RuleJudge { return &RuleJudge{Config: cfg} }

func (r *RuleJudge) Assess(ctx context.Context, _ domain.Action, w domain.WindowInfo) (domain.Verdict, Reason, error) {
	cfg, err := r.Config.Config(ctx)
	if err != nil {
		return domain.VerdictRequireApproval, Reason{Code: JudgeReasonError, Message: "エージェントの設定を読めませんでした。"}, fmt.Errorf("rule judge: read agent config: %w", err)
	}
	proc := normalizeProcessName(w.Process)
	switch {
	case proc == "":
		return domain.VerdictAllow, Reason{}, nil
	case matchesProcess(cfg.DenyProcesses, proc):
		return domain.VerdictDeny, Reason{Code: JudgeReasonDenyProcess, Message: "拒否リストのプロセスです: " + w.Process}, nil
	case matchesProcess(cfg.ApproveProcesses, proc):
		return domain.VerdictRequireApproval, Reason{Code: JudgeReasonApproveProcess, Message: "承認リストのプロセスです: " + w.Process}, nil
	}
	return domain.VerdictAllow, Reason{}, nil
}

// normalizeProcessName はプロセス名の比較用の形（前後の空白を除き、最後のパス区切りより後ろを小文字にする）。
func normalizeProcessName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, `\/`); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// matchesProcess は list のどれかが proc（normalizeProcessName 済み）と一致するか。
func matchesProcess(list []string, proc string) bool {
	for _, p := range list {
		if n := normalizeProcessName(p); n != "" && n == proc {
			return true
		}
	}
	return false
}
