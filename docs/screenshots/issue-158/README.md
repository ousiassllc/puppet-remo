# Issue #158 — `run_task` の `window_id`（CUA を 1 ウィンドウと関連ダイアログ・メニューに限る）

**これは偽 platform のスクリーンショットで、Windows 実機の成立性ゲート（S1〜S3）の代わりにならない。**
実機未確認（成立性ゲート待ち。[docs/operations/smoke-windows.md](../../operations/smoke-windows.md) §3.10・§3.10.4）。ここにある成功経路（メニューを開く → 保存ダイアログ → 保存）は
偽のメモ帳（`platformtest.FakeApp`。単色のウィンドウ）と台本つきの偽モデルで動かしたもので、実 OpenAI も実 Windows も使っていない。
クライアント名・ホスト名・指示・ファイル名・トークンはすべて架空（ダミートークンは `prm_DEMO-dummy-token-…` で、実在しない）。

## 撮り方

```sh
# 偽のデスクトップ・偽のメモ帳（window_id 1001）・台本つきの偽モデル。実 API は呼ばない
go run ./internal/app/internal/remotedemo -agent-demo -agent-scope=save      # 保存まで（1 バッチ内の click→click が中断される）
go run ./internal/app/internal/remotedemo -agent-demo -agent-scope=foreign   # 前面が別プロセスに替わって failed
# MCP の run_task に window_id "1001" を渡し（loopback。Host ヘッダーは偽の tailnet IP）、ホスト GUI（/tasks/{id}・/audit・/）を headless の Chrome で撮った。
# 偽モデルに渡った画像は、service のテスト（TestAgentScopedDumpModelImages。PUPPET_REMO_DUMP_DIR を指定したときだけ書く）が PNG に書き出したもの。
```

## 一覧

| ファイル | 何を撮ったか |
|----------|--------------|
| `01-task-detail-target-completed.png` | `run_task`（`window_id` つき）が終わったあとのタスク詳細。**対象ウィンドウ**（`fakenotepad.exe`・pid・window_id。ウィンドウのタイトルは出ない）、`fast_steps` 0、`agent.step` の `target_window_id` |
| `02-dashboard-running-target.png` | 実行中のダッシュボード。実行中のタスクの**対象**（プロセス名・pid）と、種別 `エージェント` のリース |
| `03-task-detail-failed-foreign-foreground.png` | 前面が別プロセスのウィンドウに替わり `failed`（`input_blocked`）になったタスク詳細。キー入力は注入されていない。reason（`target_not_foreground`）は監査 `agent.task_end` の `detail.block_reason` に残る |
| `04-audit-target-halted-foreign.png` | 同じ場面の監査ログ。`lease.target_set` → `agent.step`（denied）→ `input.target_halted`（denied）→ `agent.task_end`（denied） |
| `05-audit-batch-yielded.png` | 1 回の応答 `[click(ファイル), click(名前を付けて保存)]` の 1 手目でメニューが開き、2 手目を実行せずに中断した監査ログ。`agent.batch_yielded`（`ok`。失敗ではない）が残り、続くステップが新しい画像から再計画された。中断された 2 手目の `agent.step` は `result = ok`・`detail.reason = group_changed`（拒否ではない。注入していない）で並ぶ |
| `model-image-1-initial.png` | 偽モデルに渡った最初の画像（グループ合成。T だけ） |
| `model-image-2-menu-opened.png` | 中断後に撮り直した画像（「ファイル」のメニューが写る。単色の偽ウィンドウ） |
| `model-image-3-save-dialog-outside-T.png` | 保存ダイアログが開いた画像。**ダイアログが T の矩形の外へはみ出しても写り**、非メンバー・デスクトップの画素は含まれない（空のキャンバス上の合成） |

## 試験

- `internal/service/agent_scope_test.go`・`agent_scope_format_test.go`（偽 platform + 台本つきの偽モデル / ループバックのモックの `/v1/responses`）
- `internal/openai/history_test.go`・`yield_test.go`・`pairing_test.go`（`call_id` の対応検査・user メッセージの形・再試行なし）
- `internal/repository/migrate_test.go`・`tasks_test.go`（`0006_agent_task_target.sql`）

実機で確認できていないこと: S1〜S3 のエージェント版、実 OpenAI が `computer_call_output` の直後の `role: "user"` メッセージを受理すること、
メニューが採用した撮影方式（PrintWindow）で写ること。手順は smoke-windows.md §3.10.4。
