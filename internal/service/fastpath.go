package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/typesafe"
)

// このファイルは高速パス（FastPath。docs/components/overview.md §5.6.1、FR-G16〜G23、NFR-S17・S18・PR04）。
// Agent の内部の部品で、専用の goroutine を持たず、run の goroutine で 1 ステップずつ同期的に動く。
// Jev（TypeSafe）が選べるのは候補表の ID だけで、ツール名・座標・引数を生成させない。選んだ行動は Control.Do（検査の順序は他と同じ。
// 近道は無い）で実行する。

// 高速パスの定数（docs §5.6.1 の手順）。
const (
	// fastGoalMaybeDone は goal_maybe_done がこの確率以上なら、行動せずに OpenAI へ引き継ぐ（FR-G20）。
	fastGoalMaybeDone = 0.5
	// fastMaxReobserve は reobserve を連続で受け入れる回数。次の（3 回目の）reobserve は abstain として引き継ぐ。
	fastMaxReobserve = 2
	// fastMaxNoProgress は進捗なしが連続したらこの回数で no_progress として引き継ぐ（FR-G19）。
	fastMaxNoProgress = 3
	// fastSnapshotNodes は UIA のスナップショットの要素の上限（platform.UIAutomation.Snapshot の maxNodes）。
	fastSnapshotNodes = 1000
	// fastMaxChosenRunes は監査の chosen_id に残す、Jev が選んだ値の長さの上限（検証で拒否された値でも長さを抑える）。
	fastMaxChosenRunes = 32
	// fastMaxSummaryLabelRunes は OpenAI へ渡す高速ステップの要約に入れるラベルの長さの上限。
	fastMaxSummaryLabelRunes = 80
)

// FastpathDisabler は TypeSafe の HTTP 401 で高速パスを無効に戻す窓口（AgentSettingsService が満たす。FR-G21）。
type FastpathDisabler interface {
	DisableFastpath(ctx context.Context, by domain.Actor, errorCode string) error
}

// FastPath は Agent が使う高速パス。NewFastPath で作る。
type FastPath struct {
	UI       platform.UIAutomation // 読み取り（Snapshot）だけ。入力は注入しない
	Control  Control               // Windows()・Monitors()・MaxDimension()（観測と座標の変換）と Do()（実行）
	Chooser  typesafe.Chooser
	Settings AgentConfigReader // 設定と TypeSafe キーの登録の有無だけ（キーの値は読まない）
	Disabler FastpathDisabler  // nil なら 401 でも設定は戻さない（トーストと引き継ぎだけ）
	Audit    AuditRecorder     // 行動しないパス（引き継ぎ・reobserve）の監査。行動したステップは Control が記録する。nil なら記録しない
	Bus      *Bus              // 401 のトースト。nil なら通知しない
	Logger   *slog.Logger      // nil なら slog.Default()
}

// NewFastPath は fp の必須の項目（UI・Control・Chooser・Settings）を検査して返す。通信も goroutine も起動しない。
func NewFastPath(fp FastPath) (*FastPath, error) {
	if fp.UI == nil || fp.Control == nil || fp.Chooser == nil || fp.Settings == nil {
		return nil, errors.New("service: fast path requires UI, Control, Chooser and Settings")
	}
	if fp.Logger == nil {
		fp.Logger = slog.Default()
	}
	return &fp, nil
}

// Handoff は高速パスから OpenAI への引き継ぎ。
type Handoff struct {
	Reason domain.HandoffReason
	Steps  []string // 実行した高速ステップの文章要約（アクションの説明。type の本文は含めない）
}

// Enabled は高速パスを使うか。設定 agent.fastpath.enabled が true、TypeSafe キーが登録済み、agent.fastpath.consent_at がある、の
// 3 つがそろうときだけ true（どれかが欠ければ黙ってスキップする。FR-G22）。設定やキーを読めないときも false（TypeSafe を呼ばない）。
func (f *FastPath) Enabled(ctx context.Context) bool {
	cfg, err := f.Settings.Config(ctx)
	if err != nil {
		f.Logger.Warn("fast path is skipped: the settings could not be read", "error", err)
		return false
	}
	if !cfg.Fastpath.Enabled || cfg.Fastpath.ConsentAt == nil {
		return false
	}
	registered, err := f.Settings.TypeSafeKeyRegistered(ctx)
	if err != nil {
		f.Logger.Warn("fast path is skipped: the TypeSafe key could not be checked", "error", err)
		return false
	}
	return registered
}

// Run は引き継ぐ（Handoff を返す）か、終了する（error を返す）まで 1 ステップずつ進める。error は ctx の取り消し・Control の拒否
// （handoff_reason は NULL のまま）・全体の上限（errLimitReached）。r の steps・fastSteps を進め、永続化する。min_confidence・
// max_steps・excluded_labels はタスクの開始時（r.cfg）のものを使う。一方、高速パスを使えるか（設定・キー・同意。Enabled）は
// 毎ステップ読み直し、タスクの途中で同意の取り消し・無効化・キーの削除があれば、TypeSafe を呼ばずに chooser_unavailable で
// 引き継ぐ。ステップの経路・番号は r の step_no（アクションを実行したステップだけ数える。行動しないパスの番号は recordPass）。
func (f *FastPath) Run(ctx context.Context, r *run) (Handoff, error) {
	fp := r.cfg.Fastpath
	var (
		steps      []string
		reobserves int
		noProgress int
		next       *domain.UISnapshot // 直前の再観測のスナップショット（次の観測で取り直さない）
	)
	handoff := func(reason domain.HandoffReason, extra map[string]any) (Handoff, error) {
		extra["handoff_reason"] = string(reason)
		f.recordPass(ctx, r, extra)
		return Handoff{Reason: reason, Steps: steps}, nil
	}

	for {
		if err := r.checkBeforeStep(); err != nil {
			return Handoff{}, err
		}
		if !f.Enabled(ctx) { // タスクの途中で同意の取り消し・無効化があった（Enabled は読めないときも false）
			return handoff(domain.HandoffChooserUnavailable, map[string]any{"candidate_count": 0, "dropped_count": 0})
		}

		// 1. 観測
		snap, wins, err := f.observe(ctx, next)
		next = nil
		extra := map[string]any{"candidate_count": 0, "dropped_count": 0}
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, domain.ErrKillSwitchActive) {
				return Handoff{}, err
			}
			f.Logger.Warn("fast path could not observe the screen", "task_id", r.id, "error", err)
			return handoff(domain.HandoffNoCandidates, extra)
		}
		table := domain.BuildCandidateTable(domain.CandidateSource{Snapshot: snap, Windows: wins},
			domain.CandidateOptions{ExcludedLabels: fp.ExcludedLabels})
		extra["candidate_count"], extra["dropped_count"] = table.Len(), table.Dropped()
		// 自分のウィンドウ（ホスト GUI・承認ダイアログ）が前面のときは、InputGuard が必ず拒否するので行動しない。
		if table.Len() == 0 || snap.Window.Own && !snap.Window.Overlay {
			return handoff(domain.HandoffNoCandidates, extra)
		}

		// 2. 問い合わせ
		res, err := f.Chooser.Choose(ctx, chooseRequest(r, fp, snap, table, steps))
		if err != nil {
			if ctx.Err() != nil { // 呼び出し側のキャンセルは縮退ではない
				return Handoff{}, ctx.Err()
			}
			switch {
			case errors.Is(err, typesafe.ErrBadResponse):
				return handoff(domain.HandoffInvalidChoice, extra)
			case errors.Is(err, typesafe.ErrUnauthorized):
				f.onUnauthorized(ctx)
			}
			return handoff(domain.HandoffChooserUnavailable, extra) // ErrUnavailable（タイムアウト・通信・5xx・429）ほか
		}
		extra["chosen_id"] = sanitizeChosen(res.Choice)
		extra["confidence"] = res.Confidence
		extra["goal_maybe_done"] = res.GoalMaybeDone

		// 3. 検証（同じ表に対して、この順）
		cand, verr := table.Validate(res.Choice)
		switch {
		case verr != nil, !unitInterval(res.Confidence), !unitInterval(res.GoalMaybeDone):
			return handoff(domain.HandoffInvalidChoice, extra)
		case res.GoalMaybeDone >= fastGoalMaybeDone:
			return handoff(domain.HandoffGoalMaybeDone, extra)
		case cand.Kind == domain.CandidateAbstain:
			return handoff(domain.HandoffAbstain, extra)
		case res.Confidence < fp.MinConfidence:
			return handoff(domain.HandoffLowConfidence, extra)
		case cand.Kind == domain.CandidateReobserve:
			if reobserves++; reobserves > fastMaxReobserve {
				return handoff(domain.HandoffAbstain, extra)
			}
			f.recordPass(ctx, r, extra)
			continue
		}
		reobserves = 0

		// 4. 実行（1 ステップに 1 アクション）
		op, line, err := f.plan(ctx, cand)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, domain.ErrKillSwitchActive) {
				return Handoff{}, err
			}
			return handoff(domain.HandoffInvalidChoice, extra) // 実行できない候補（モニター・座標を決められない）
		}
		before := fastSignature{hash: snap.Hash, window: snap.Window.ID}
		r.countFastStep()
		actx := WithAgentStep(ctx, AgentStep{No: r.task.StepCount, Route: AgentRouteFast, ExtraDetail: extra})
		if _, err := f.Control.Do(actx, r.actor, op); err != nil {
			return Handoff{}, err // 拒否・ブロックは OpenAI でも救済せず、タスクを終える（FR-G07）
		}
		steps = append(steps, line)

		// 5. 再観測と進捗
		after, aerr := f.UI.Snapshot(ctx, "", fastSnapshotNodes)
		if ctx.Err() != nil {
			return Handoff{}, ctx.Err()
		}
		progressed := false
		if aerr == nil {
			next = &after
			progressed = fastSignature{hash: after.Hash, window: after.Window.ID} != before
		}
		if r.overLimit() { // 高速ステップも agent.max_steps・max_duration_sec に数える。上限は引き継がずに終わる
			return Handoff{}, errLimitReached
		}
		counts := map[string]any{"candidate_count": extra["candidate_count"], "dropped_count": extra["dropped_count"]}
		if progressed {
			noProgress = 0
		} else if noProgress++; noProgress >= fastMaxNoProgress {
			return handoff(domain.HandoffNoProgress, counts) // 最後の観測の候補数（アクションを選んだ回の表）
		}
		if r.task.FastSteps >= fp.MaxSteps {
			return handoff(domain.HandoffFastStepLimit, counts)
		}
	}
}

// fastSignature は進捗の判定に使う（UIA ツリーのハッシュ、前面ウィンドウの ID）の組。
type fastSignature struct{ hash, window string }

// observe は Windows（focus_window 用）と UI スナップショット（前面ウィンドウ）を取る。reuse があればそれを使い、取り直さない。
func (f *FastPath) observe(ctx context.Context, reuse *domain.UISnapshot) (domain.UISnapshot, []domain.WindowInfo, error) {
	wins, err := f.Control.Windows(ctx)
	if err != nil {
		return domain.UISnapshot{}, nil, err
	}
	if reuse != nil {
		return *reuse, wins, nil
	}
	snap, err := f.UI.Snapshot(ctx, "", fastSnapshotNodes)
	return snap, wins, err
}

// chooseRequest は Jev へ送る内容を組み立てる。要素は ID・役割・ラベル・有効だけで、値・スクリーンショットを入れる欄は無い（NFR-PR04）。
func chooseRequest(r *run, fp FastpathConfig, snap domain.UISnapshot, table domain.CandidateTable, steps []string) typesafe.ChoiceRequest {
	cands := table.Candidates()
	els := make([]typesafe.ChoiceElement, len(cands))
	for i, c := range cands {
		els[i] = typesafe.ChoiceElement{ID: c.ID, Role: string(c.Role), Label: c.Label, Enabled: c.Enabled}
	}
	return typesafe.ChoiceRequest{
		Model:    fp.Model,
		Goal:     r.task.Prompt,
		Window:   typesafe.ChoiceWindow{Title: snap.Window.Title, Process: snap.Window.Process},
		Elements: els,
		History:  slices.Clone(steps),
	}
}

// plan は受理した候補を Control.Do に渡す Op と、OpenAI への要約の 1 文にする。click は中心座標（物理）を
// Monitor.PhysicalToScaled で縮小座標にして左・1 回のクリック、focus_window は FocusWindow。
func (f *FastPath) plan(ctx context.Context, c domain.Candidate) (domain.Op, string, error) {
	label := domain.TruncateRunes(c.Label, fastMaxSummaryLabelRunes)
	switch c.Kind {
	case domain.CandidateFocusWindow:
		return domain.FocusWindow{ID: c.WindowID}, fmt.Sprintf("Brought the window \"%s\" to the foreground.", label), nil
	case domain.CandidateClick:
		mons, err := f.Control.Monitors(ctx)
		if err != nil {
			return nil, "", err
		}
		maxDim, err := f.Control.MaxDimension(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, m := range mons {
			if m.Index != c.Monitor {
				continue
			}
			if !m.ContainsPhysical(c.At) {
				break
			}
			p := m.PhysicalToScaled(c.At, maxDim)
			role := strings.ReplaceAll(string(c.Role), "_", " ")
			return domain.Click{At: &p, Button: domain.ButtonLeft, Count: 1, Monitor: m.Index},
				fmt.Sprintf("Clicked the %s labeled \"%s\".", role, label), nil
		}
	}
	return nil, "", errors.New("the candidate has no executable target")
}

// onUnauthorized は TypeSafe の 401（キー無効）を GUI へ知らせ、高速パスを無効に戻す（FR-G21。引き継ぎは呼び出し側）。
func (f *FastPath) onUnauthorized(ctx context.Context) {
	if f.Bus != nil {
		f.Bus.Publish(Event{Topic: TopicToast, Data: Toast{
			Level:   "error",
			Message: "TypeSafe の API キーが無効です（401）。高速パスを無効にしました。エージェント設定でキーを登録し直してください。",
		}})
	}
	if f.Disabler == nil {
		return
	}
	if err := f.Disabler.DisableFastpath(ctx, domain.Actor{Kind: domain.ActorSystem, Label: systemLabel}, FastpathDisableUnauthorized); err != nil {
		f.Logger.Error("fast path could not be disabled after a 401", "error", err)
	}
}

// recordPass は行動しないパス（引き継ぎ・reobserve）を agent.step として記録する（route = fast、extra、pass = true）。
// 行動したステップは Control が AgentStep.ExtraDetail つきで記録する。入力した文字列・要素の値は extra に入らない。
// パスの step_no は「直前に実行したステップの番号」（r.task.StepCount。まだ実行していなければ 0 で、step_no は記録しない）。
// 次に実行するステップ（高速・OpenAI）は StepCount + 1 を使うので、パスの行がそれと衝突しない（pass で区別する）。
func (f *FastPath) recordPass(ctx context.Context, r *run, extra map[string]any) {
	if f.Audit == nil {
		return
	}
	sctx := WithAgentStep(ctx, AgentStep{No: r.task.StepCount, Route: AgentRouteFast})
	e := agentAuditBase(sctx, r.actor, agentStepAuditEvent, "")
	e.Result = auditOK
	detail := maps.Clone(extra)
	detail["route"] = AgentRouteFast
	detail["pass"] = true // 行動しないパス（アクションのステップではない）
	e.DetailJSON = marshalDetail(detail)
	f.Audit.Record(context.WithoutCancel(ctx), e)
}

// sanitizeChosen は Jev が返した選択を、監査に残せる形（制御文字を ? にして長さを抑える）にする。
func sanitizeChosen(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
	return domain.TruncateRunes(s, fastMaxChosenRunes)
}

// unitInterval は v が 0〜1 の有限の数か（NaN・無限大・範囲外の確信度で行動しない）。
func unitInterval(v float64) bool {
	return !math.IsNaN(v) && v >= 0 && v <= 1
}
