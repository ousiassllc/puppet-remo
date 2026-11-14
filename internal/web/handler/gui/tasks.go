package gui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルはエージェントのタスクの画面（M4。FR-H10・FR-H02）。一覧・詳細は読み取りで、中断（POST /tasks/{id}/cancel）だけが操作。
// 取得・中断・監査（操作主体 host）は service.TaskAdmin が行い、ここは描画と受け付けだけ。指示（prompt）・最終回答・ステップの文字列は
// 利用者・外部由来なので、すべて templ が HTML エスケープする。入力した文字列の本文・要素の値はステップに含まれない（FR-G23）。

// promptExcerptMaxRunes は一覧・ダッシュボードに出す指示の長さの上限（文字数）。
const promptExcerptMaxRunes = 80

// shortIDLen は画面に出すタスク ID の長さ（ULID の末尾。先頭は時刻なので、同じ時期のタスクでは区別にならない）。
const shortIDLen = 8

const (
	taskResultNote = "「完了」はモデル（OpenAI）の申告です。実際に目的を達成したかの確認は、依頼元（呼び出し側）の責任です（FR-G13）。"
	// taskCancelledToast・taskCancelingToast は中断の応答のトースト（info）。
	taskCancelledToast = "タスクを中断しました。"
	// taskEndedMessage は終了済みのタスクの文言。中断の応答のトースト（冪等）と、確認モーダルの 409 conflict のインラインアラートの両方に使う。
	taskEndedMessage   = "このタスクはすでに終了しています。"
	taskCancelingToast = "タスクの中断を要求しました。"
)

// taskStatusTexts は状態の表示（FR-G03）。completed はモデルの申告なので、詳細に注意書きを添える。
var taskStatusTexts = map[domain.TaskStatus]struct {
	label string
	tone  view.BadgeTone
}{
	domain.TaskPendingApproval: {"承認待ち", view.ToneWarn},
	domain.TaskRunning:         {"実行中", view.ToneOK},
	domain.TaskCompleted:       {"完了", view.ToneOK},
	domain.TaskFailed:          {"失敗", view.ToneDanger},
	domain.TaskCancelled:       {"中断", view.ToneNeutral},
	domain.TaskLimitReached:    {"上限に到達", view.ToneWarn},
	domain.TaskAborted:         {"異常終了", view.ToneWarn},
	domain.TaskDenied:          {"承認が拒否された", view.ToneWarn},
}

func taskStatusText(s domain.TaskStatus) (string, view.BadgeTone) {
	if t, ok := taskStatusTexts[s]; ok {
		return t.label, t.tone
	}
	return string(s), view.ToneNeutral
}

// taskHandoffTexts は高速パスから OpenAI への引き継ぎ理由（FR-G20 の 8 種）の説明。
var taskHandoffTexts = map[domain.HandoffReason]string{
	domain.HandoffAbstain:            "高速パスが判断を保留した",
	domain.HandoffLowConfidence:      "確信度がしきい値に届かなかった",
	domain.HandoffNoCandidates:       "操作できる候補が無かった",
	domain.HandoffInvalidChoice:      "選択が不正だった",
	domain.HandoffNoProgress:         "画面に変化がなく進まなかった",
	domain.HandoffGoalMaybeDone:      "目的を達成した可能性がある",
	domain.HandoffFastStepLimit:      "高速ステップの上限に達した",
	domain.HandoffChooserUnavailable: "選択器（TypeSafe）が利用できなかった",
}

// taskErrorTexts は失敗の理由（agent_tasks.error_code。docs/architecture/er.md）の説明。未知のコードはコードだけを出す。
var taskErrorTexts = map[string]string{
	"input_blocked":       "入力がブロックされた（管理者権限のウィンドウ・puppet-remo 自身のウィンドウなど）",
	"judge_denied":        "ルール（Judge）が操作を拒否した",
	"approval_denied":     "ホストが承認を拒否した（または時間切れ）",
	"safety_check_denied": "OpenAI の安全チェックをホストが拒否した（または時間切れ）",
	"unsupported_action":  "未対応のアクションが返された",
	"openai_error":        "OpenAI との通信またはモデルの応答でエラーが起きた",
}

// taskDuration は経過時間の表示。1 秒未満は「1 秒未満」。
func taskDuration(d time.Duration) string {
	if d < time.Second {
		return "1 秒未満"
	}
	return formatDuration(d)
}

// taskElapsed はタスクの経過時間: 開始（実行前は作成）から、終了（実行中は今）まで。終了状態なのに終了時刻が無ければ "-"。
func (h *Handler) taskElapsed(t domain.Task) string {
	start := t.CreatedAt
	if t.StartedAt != nil {
		start = *t.StartedAt
	}
	end := h.d.Clock.Now()
	switch {
	case t.FinishedAt != nil:
		end = *t.FinishedAt
	case t.Status.Terminal():
		return "-"
	}
	return taskDuration(max(0, end.Sub(start)))
}

func taskClientLabel(t domain.Task) string {
	switch {
	case t.ClientLabel != "":
		return t.ClientLabel
	case t.ClientID != "":
		return t.ClientID
	}
	return "（削除済み）"
}

func promptExcerpt(s string) string {
	return truncateRunes(strings.Join(strings.Fields(s), " "), promptExcerptMaxRunes)
}

func shortTaskID(id string) string {
	r := []rune(id)
	if len(r) <= shortIDLen {
		return id
	}
	return string(r[len(r)-shortIDLen:])
}

func taskDetailURL(id string) string { return "/tasks/" + url.PathEscape(id) }

// taskStatusDOMID は状態の 1 行の id。確認モーダルの確定の応答が、この要素を差し替える。
func taskStatusDOMID(id string) string { return "task-status-" + id }

func taskSteps(t domain.Task) string {
	return strconv.Itoa(t.StepCount) + " / " + strconv.Itoa(t.MaxSteps)
}

// taskStatusView は状態の 1 行。中断のボタン（CancelURL）は、終了していないタスクにだけ付ける（状態駆動）。
func (h *Handler) taskStatusView(t domain.Task) view.TaskStatus {
	label, tone := taskStatusText(t.Status)
	v := view.TaskStatus{DOMID: taskStatusDOMID(t.ID), Code: string(t.Status), Label: label, Tone: tone, Steps: taskSteps(t), Elapsed: h.taskElapsed(t)}
	if !t.Status.Terminal() {
		v.CancelURL = taskDetailURL(t.ID) + "/cancel"
		v.ConfirmURL = v.CancelURL + "/confirm"
	}
	return v
}

// taskTargetText は window_id つきのタスクの対象（プロセス名・pid。ダッシュボード用）。スコープなしは空。ウィンドウのタイトルは載せない。
func taskTargetText(t domain.Task) string {
	if !t.Scoped() {
		return ""
	}
	if t.TargetProcess == "" {
		return fmt.Sprintf("pid %d", t.TargetPID)
	}
	return fmt.Sprintf("%s（pid %d）", t.TargetProcess, t.TargetPID)
}

// taskTargetDetailText はタスク詳細の対象（プロセス名・pid・window_id）。スコープなしは空。
func taskTargetDetailText(t domain.Task) string {
	if !t.Scoped() {
		return ""
	}
	return fmt.Sprintf("%s ・ window_id %s", taskTargetText(t), t.TargetWindowID)
}

// runningTasksView はダッシュボードの実行中タスク（実行中・承認待ち。高々 1 件）。取得できなければ空として扱い、ログに残す
// （ダッシュボードは出す）。
func (h *Handler) runningTasksView(ctx context.Context) view.RunningTasks {
	tasks, err := h.d.Tasks.Running(ctx)
	if err != nil {
		h.log.Warn("list running tasks failed", "error", err)
	}
	var v view.RunningTasks
	for _, t := range tasks {
		v.Items = append(v.Items, view.RunningTask{
			ShortID: shortTaskID(t.ID), DetailURL: taskDetailURL(t.ID), Client: taskClientLabel(t),
			PromptExcerpt: promptExcerpt(t.Prompt), Target: taskTargetText(t), Status: h.taskStatusView(t),
		})
	}
	v.Rev = revision(v)
	return v
}

func (h *Handler) taskRows(tasks []domain.Task) []view.TaskRow {
	rows := make([]view.TaskRow, 0, len(tasks))
	for _, t := range tasks {
		label, tone := taskStatusText(t.Status)
		rows = append(rows, view.TaskRow{
			ShortID: shortTaskID(t.ID), DetailURL: taskDetailURL(t.ID), Client: taskClientLabel(t), PromptExcerpt: promptExcerpt(t.Prompt),
			Code: string(t.Status), Label: label, Tone: tone, Steps: taskSteps(t), Created: h.formatTime(t.CreatedAt), Elapsed: h.taskElapsed(t),
		})
	}
	return rows
}

// Tasks は GET /tasks?cursor=（新しい順に 50 件。FR-H10）。HX-Request のフラグメントは一覧（#task-list）、HX-Boosted はページの本文、
// それ以外（履歴の復元を含む）はフルページ。cursor が不正なら 422（サービスが検査する）。読み出しの失敗は 500。
func (h *Handler) Tasks(c *gin.Context) {
	ctx := c.Request.Context()
	trail, ok := parsePageTrail(c)
	if !ok {
		h.pageFail(c, fmt.Errorf("%w: back は cursor と一緒に、%d 件までで指定してください", domain.ErrInvalidArgument, maxPageTrail))
		return
	}
	page, err := h.d.Tasks.List(ctx, service.TaskListQuery{Cursor: trail.Cursor})
	if err != nil {
		h.pageFail(c, err)
		return
	}
	// cursor は不透明な文字列なので、URL には pageURLs がクエリとしてエスケープして載せる。
	prev, next := pageURLs("/tasks", nil, trail, page.Next, page.Next != "")
	list := view.TaskList{Rows: h.taskRows(page.Tasks), Page: trail.Page(), PrevURL: prev, NextURL: next}
	switch {
	case boostedRequest(c):
		h.render(c, http.StatusOK, pages.TasksBoosted(view.TaskListPage{Chrome: h.chrome(ctx, view.NavTasks), List: list}))
	case fragmentRequest(c):
		h.render(c, http.StatusOK, organisms.TaskList(list))
	default:
		h.render(c, http.StatusOK, pages.Tasks(view.TaskListPage{Chrome: h.chrome(ctx, view.NavTasks), List: list}))
	}
}

// stepKindLabel はアクション以外の行の種類の表示（アクションの行は空。Kind が空の行もアクション扱い）。
func stepKindLabel(k service.TaskStepKind) string {
	switch k {
	case service.TaskStepPass:
		return "観察のみ（行動なし）"
	case service.TaskStepJudge:
		return "Judge の判定"
	case service.TaskStepSafetyCheck:
		return "安全チェック"
	}
	return ""
}

func routeLabel(route string) string {
	switch route {
	case "fast":
		return "高速パス"
	case "openai":
		return "OpenAI"
	}
	return route
}

func (h *Handler) taskStepRows(steps []service.TaskStep) []view.TaskStepRow {
	rows := make([]view.TaskStepRow, 0, len(steps))
	for _, s := range steps {
		r := view.TaskStepRow{
			No: s.No, KindLabel: stepKindLabel(s.Kind), Route: s.Route, RouteLabel: routeLabel(s.Route), Event: s.Event, Result: s.Result, ResultTone: resultTone(s.Result),
			Summary: s.Summary, At: s.At.In(h.loc).Format(auditTimeLayout),
		}
		for _, a := range s.Args {
			r.Args = append(r.Args, view.AuditArg{Key: a.Key, Value: a.Value})
		}
		if s.AuditID > 0 {
			id := strconv.FormatInt(s.AuditID, 10)
			r.AuditURL = "/audit/" + id
			if s.Thumb {
				r.ThumbURL = "/audit/" + id + "/thumbnail"
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func (h *Handler) taskDetailView(c *gin.Context, d service.TaskDetail) view.TaskDetail {
	v := view.TaskDetail{
		Chrome: h.chrome(c.Request.Context(), view.NavTasks), ID: d.ID, Client: taskClientLabel(d.Task), Prompt: d.Prompt,
		Status: h.taskStatusView(d.Task), Model: d.Model, MaxSteps: d.MaxSteps, MaxDurationSec: d.MaxDurationSec,
		StepCount: d.StepCount, FastSteps: d.FastSteps, OpenAISteps: d.OpenAISteps,
		InputTokens: strconv.FormatInt(d.InputTokens, 10), OutputTokens: strconv.FormatInt(d.OutputTokens, 10),
		ResultText: d.ResultText, ErrorCode: d.ErrorCode, ErrorLabel: taskErrorTexts[d.ErrorCode],
		Created: h.formatTime(d.CreatedAt), Started: h.formatTimePtr(d.StartedAt, ""), Finished: h.formatTimePtr(d.FinishedAt, ""),
		Steps: h.taskStepRows(d.Steps), BackURL: "/tasks",
	}
	if d.HandoffReason != "" {
		v.Handoff = string(d.HandoffReason)
		if text, ok := taskHandoffTexts[d.HandoffReason]; ok {
			v.Handoff = text + "（" + string(d.HandoffReason) + "）"
		}
	}
	v.Target = taskTargetDetailText(d.Task)
	if d.CapturePartial {
		v.CapturePartial = "所有関係を確かめられないウィンドウをモデルへ渡す画像から除外した（capture_partial。停止ではない）"
	}
	if d.ResultText != "" && d.Status == domain.TaskCompleted {
		v.ResultNote = taskResultNote
	}
	return v
}

// TaskDetail は GET /tasks/:id（指示・状態・ステップごとの経路とサムネイル・最終回答。FR-H10・FR-G23）。無いタスクは 404。
func (h *Handler) TaskDetail(c *gin.Context) {
	d, err := h.d.Tasks.Detail(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.pageFail(c, err)
		return
	}
	v := h.taskDetailView(c, d)
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.TaskDetailBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.TaskDetail(v))
}

// CancelTask は POST /tasks/:id/cancel（中断のボタン。FR-H02・H10）。操作主体は host で、監査は TaskAdmin が記録する。
// 応答はタスクの状態の 1 行（呼び出し元の同じ部品を差し替える）と完了のトースト。終了済みなら現在の状態をそのまま返す（冪等）。
// 無いタスクは 404（エラーのアラート）。
func (h *Handler) CancelTask(c *gin.Context) {
	t, err := h.d.Tasks.CancelByHost(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	toast := taskCancelingToast
	switch t.Status {
	case domain.TaskCancelled:
		toast = taskCancelledToast
	default:
		if t.Status.Terminal() {
			toast = taskEndedMessage
		}
	}
	h.render(c, http.StatusOK, organisms.TaskStatus(h.taskStatusView(t)), organisms.ModalClear(), organisms.ToastOOB(infoToast(toast)))
}

// ConfirmCancelTask は GET /tasks/:id/cancel/confirm（中断の確認モーダル）。中断できる（終了していない）タスクだけ描画する。
// 無いタスクは 404、終了済みは 409（モーダルなし）。確定のボタンが POST /tasks/:id/cancel を送る。
func (h *Handler) ConfirmCancelTask(c *gin.Context) {
	d, err := h.d.Tasks.Detail(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	if d.Status.Terminal() {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), taskEndedMessage)
		return
	}
	h.confirm(c, view.ConfirmModal{
		Title:      "タスクを中断しますか？",
		Message:    "実行中のエージェントタスクを止めます。途中までの操作は元に戻りません。",
		ConfirmURL: taskDetailURL(d.ID) + "/cancel", Method: "post", Target: "#" + taskStatusDOMID(d.ID), Swap: "outerHTML", Label: "中断する", Destructive: true,
	})
}
