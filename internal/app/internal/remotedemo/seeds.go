package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// seedTaskHistory は /tasks の確認用に、終了済みのタスクを n 件（既定の 1 ページ 50 件を超える数を渡すと、ページ送りが出る）入れる。
// 指示の長さ・状態・ステップ・所要時間を変えて並べる（新しい順の先頭は、390 幅で行を折り返す長い指示）。実在の指示・クライアントではない。
func seedTaskHistory(ctx context.Context, db *repository.DB, n int) error {
	prompts := []string{
		"メモ帳を開いて、今日の日付と「会議の議事録」という見出しを入力し、続けて本日の議題を三つ箇条書きにして、最後に名前を付けて保存してください。保存先はデスクトップです。",
		"電卓で 1280 × 720 を計算して結果を教えて",
		"Open Notes and type a short greeting",
		"ブラウザで天気予報のページを開き、明日の東京の最高気温と降水確率を調べて、最終回答にまとめてください。",
		"デスクトップのフォルダを整理して",
	}
	states := []struct {
		status domain.TaskStatus
		steps  int
	}{
		{domain.TaskCompleted, 6}, {domain.TaskCompleted, 12}, {domain.TaskFailed, 3},
		{domain.TaskCancelled, 2}, {domain.TaskLimitReached, 50}, {domain.TaskCompleted, 1},
	}
	base := time.Now().Add(-48 * time.Hour)
	for i := 0; i < n; i++ {
		st := states[i%len(states)]
		created := base.Add(time.Duration(i) * 7 * time.Minute)
		started := created.Add(time.Second)
		finished := started.Add(time.Duration(10+i%50) * time.Second)
		t := domain.Task{
			ID: fmt.Sprintf("01DEMOTASK%016d", i), ClientLabel: "demo-laptop", Prompt: prompts[(n-1-i)%len(prompts)], Status: domain.TaskRunning,
			Model: "demo-model", MaxSteps: 50, MaxDurationSec: 600, CreatedAt: created, StartedAt: &started,
		}
		if err := db.Tasks().Create(ctx, t); err != nil { // 作成できるのは実行中・承認待ちだけ。終わった状態へは Update で進める
			return err
		}
		t.Status, t.StepCount, t.FastSteps, t.FinishedAt = st.status, st.steps, st.steps, &finished
		if err := db.Tasks().Update(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// seedPendingRequest は /requests の項目カードの確認用に、未処理の接続要求を 1 件入れる（実在のデバイスではない）。
func seedPendingRequest(ctx context.Context, db *repository.DB) error {
	_, _, err := db.ConnRequests().UpsertPending(ctx, repository.ConnRequest{
		ID: "01DEMOREQUEST0000000000001", StableNodeID: "node-demo-9", LoginName: "guest@example.com", DisplayName: "demo-phone",
		RemoteIP: "100.64.0.9", RequestCount: 1, ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	return err
}
