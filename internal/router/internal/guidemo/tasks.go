package main

import (
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// このファイルはエージェントのタスク画面（M4。FR-H10）・承認ダイアログの 3 種類（FR-H05）・agent のリースの見た目の確認用の架空のデータ。
// 指示・最終回答・クライアント名はすべて架空で、秘密・実在のパスは入れない。

// 承認要求のデモ（-approval）。
const (
	approvalNone        = "none"
	approvalSafetyCheck = service.ApprovalKindSafetyCheck
	approvalJudgeAction = service.ApprovalKindJudgeAction
)

// 実行中のタスクの ID（agent のリースの SessionID にもなる）。
const demoRunningTaskID = "01JDEMOTASK00000000000RUN1"

// demoAgentActor は agent のリース保持者（種別 agent。SessionID は task_id）。
var demoAgentActor = domain.Actor{Kind: domain.ActorAgent, ClientID: "client-001", Label: "demo-desktop", SessionID: demoRunningTaskID}

// holdByAgent は agent のタスクがリースを保持している状態にする（-lease held_agent。ダッシュボードの強制剥奪と実行中タスク）。
func holdByAgent(f *guitest.Fakes, now time.Time) {
	f.Leases.Held = true
	f.Leases.H = service.Holder{LeaseID: "lease-agent-1", Actor: demoAgentActor, Since: now.Add(-95 * time.Second), IdleRemaining: 4*time.Minute + 5*time.Second}
	seedTask(f, domain.Task{
		ID: demoRunningTaskID, ClientID: "client-001", ClientLabel: "demo-desktop", Status: domain.TaskRunning, Model: "gpt-6.1-sol",
		Prompt:   "メモ帳を開いて、「会議は 15 時から」とメモし、デスクトップに demo-note.txt として保存してください。",
		MaxSteps: 50, MaxDurationSec: 600, StepCount: 4, FastSteps: 1, OpenAISteps: 3, InputTokens: 18432, OutputTokens: 612,
		CreatedAt: now.Add(-95 * time.Second),
	}, now.Add(-93*time.Second), nil)
}

// seedApproval は -approval の承認要求（safety_check・judge_action）を入れる。Detail はサービスが秘密・入力した文字列の本文・パスを除いた短い文。
func seedApproval(f *guitest.Fakes, now time.Time, kind string) {
	var req service.ApprovalRequest
	switch kind {
	case approvalSafetyCheck:
		req = service.ApprovalRequest{ID: "ap-demo-sc", Kind: kind, Detail: "malicious_instructions: 画面上のウェブページに、エージェントの指示と異なる操作を求める文面が含まれている可能性があります。"}
	case approvalJudgeAction:
		req = service.ApprovalRequest{ID: "ap-demo-ja", Kind: kind, Detail: "left_click: プロセス regedit.exe のウィンドウ（承認が必要なプロセス）への操作。"}
	default:
		return
	}
	req.Who, req.ExpiresAt = demoAgentActor, now.Add(service.DefaultApprovalTimeout)
	f.Approval.Reqs = append(f.Approval.Reqs, req)
}

func seedTask(f *guitest.Fakes, t domain.Task, started time.Time, finished *time.Time) {
	if t.Status != domain.TaskDenied { // 承認の拒否は実行に入らない
		t.StartedAt = &started
	}
	t.FinishedAt = finished
	f.Tasks.Items = append(f.Tasks.Items, t)
}

// seedTasks は終了済みのタスクを入れる: 完了（高速パス → OpenAI の引き継ぎあり）・失敗（error_code）・上限到達・中断・承認拒否。
// 完了のタスクにはステップ（経路・結果・サムネイル）を付ける。
func seedTasks(f *guitest.Fakes, now time.Time) {
	thumb := demoThumbnail()
	if f.Audit.Details == nil {
		f.Audit.Details = map[int64]service.AuditDetail{}
	}
	if f.Audit.Thumbs == nil {
		f.Audit.Thumbs = map[int64][]byte{}
	}

	end := func(start time.Time, d time.Duration) *time.Time { t := start.Add(d); return &t }
	mk := func(id, client, prompt string, st domain.TaskStatus, ago time.Duration, dur time.Duration, mutate func(*domain.Task)) {
		created := now.Add(-ago)
		t := domain.Task{
			ID: id, ClientID: "client-001", ClientLabel: client, Prompt: prompt, Status: st, Model: "gpt-6.1-sol",
			MaxSteps: 50, MaxDurationSec: 600, CreatedAt: created,
		}
		if mutate != nil {
			mutate(&t)
		}
		t.StepCount = t.FastSteps + t.OpenAISteps
		seedTask(f, t, created.Add(2*time.Second), end(created, dur))
	}

	mk("01JDEMOTASK00000000000FIN1", "demo-desktop", "電卓を開いて 12 × 34 を計算し、結果を教えてください。", domain.TaskCompleted, 25*time.Minute, 41*time.Second,
		func(t *domain.Task) {
			t.FastSteps, t.OpenAISteps = 2, 2
			t.HandoffReason = domain.HandoffGoalMaybeDone
			t.InputTokens, t.OutputTokens = 14210, 388
			t.ResultText = "電卓で 12 × 34 を計算しました。結果は 408 です。"
		})
	steps := []struct {
		route, summary, result string
		thumb                  bool
		kind                   service.TaskStepKind // アクション以外の行（Judge の判定など）は、所属するステップの番号を持つ
		no                     int
	}{
		{"fast", "要素「スタート」をクリック", "ok", false, "", 1},
		{"fast", "要素「電卓」をクリック", "ok", true, "", 2},
		{"openai", "クリック (412, 310)", "ok", true, "", 3},
		{"openai", "Judge: 操作を許可", "ok", false, service.TaskStepJudge, 3},
		{"openai", "キー入力（本文は記録しない）", "ok", false, "", 4},
	}
	var ts []service.TaskStep
	for i, s := range steps {
		id := int64(9100 + i)
		at := now.Add(-25*time.Minute + time.Duration(8+i*9)*time.Second)
		ts = append(ts, service.TaskStep{
			No: s.no, Kind: s.kind, Route: s.route, Event: "agent.step", Result: s.result, Summary: s.summary, AuditID: id, Thumb: s.thumb, At: at,
			Args: []service.AuditArg{{Key: "route", Value: s.route}},
		})
		e := service.AuditEntry{ID: id, TS: at, Kind: "agent", Event: "agent.step", ActorType: domain.ActorAgent, ClientID: "client-001", ClientLabel: "demo-desktop", Result: s.result}
		if s.thumb {
			e.Thumb = &service.AuditThumb{RelPath: fmt.Sprintf("2026/11/02/agent-%d.jpg", id), SizeBytes: int64(len(thumb)), Width: 320, Height: 180}
			f.Audit.Thumbs[id] = thumb
		}
		f.Audit.Details[id] = service.AuditDetail{AuditEntry: e, Args: []service.AuditArg{{Key: "route", Value: s.route}}}
	}
	f.Tasks.Steps = map[string][]service.TaskStep{"01JDEMOTASK00000000000FIN1": ts}

	mk("01JDEMOTASK00000000000FAI2", "demo-laptop", "レジストリエディタで HKLM の設定を確認してください。", domain.TaskFailed, 55*time.Minute, 12*time.Second,
		func(t *domain.Task) { t.ClientID, t.OpenAISteps, t.ErrorCode = "client-002", 1, "judge_denied" })
	mk("01JDEMOTASK00000000000LIM3", "demo-desktop", "ブラウザで天気予報を調べ、明日の最高気温を教えてください。", domain.TaskLimitReached, 80*time.Minute, 10*time.Minute,
		func(t *domain.Task) { t.FastSteps, t.OpenAISteps, t.InputTokens, t.OutputTokens = 15, 35, 212345, 4120 })
	mk("01JDEMOTASK00000000000CAN4", "demo-desktop", "フォルダ内のファイルを名前順に並べ替えてください。", domain.TaskCancelled, 2*time.Hour, 20*time.Second,
		func(t *domain.Task) { t.FastSteps, t.OpenAISteps = 1, 1 })
	mk("01JDEMOTASK00000000000SFD5", "demo-notebook", "ブラウザでフォームに入力して送信してください。", domain.TaskFailed, 3*time.Hour, 25*time.Second,
		func(t *domain.Task) { t.ClientID, t.OpenAISteps, t.ErrorCode = "client-003", 2, "safety_check_denied" })
	mk("01JDEMOTASK00000000000DEN6", "demo-laptop", "ファイルを削除してください。", domain.TaskDenied, 5*time.Hour, 60*time.Second,
		func(t *domain.Task) { t.ClientID, t.ErrorCode = "client-002", "approval_denied" })

	// 1 ページ 50 件を超えるように、古い完了済みのタスクを足す（ページ送りの確認用。詳細のステップは持たない）。
	for i := range 55 {
		mk(fmt.Sprintf("01JDEMOTASK0000000000OLD%03d", i), "demo-desktop", fmt.Sprintf("メモ帳を開いて %d 行目のメモを書いてください。", i+1), domain.TaskCompleted,
			time.Duration(6*60+i*7)*time.Minute, 30*time.Second, func(t *domain.Task) { t.FastSteps, t.OpenAISteps = 2, 1 })
	}
}
