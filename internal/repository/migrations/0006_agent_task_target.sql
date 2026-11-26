-- v6: run_task の対象ウィンドウ（window_id。#158）。スコープなしのタスクは 3 列とも NULL（従来どおり）。
-- target_window_id は選んだウィンドウの ID（HWND の 10 進文字列）、target_process はそのプロセス名、target_pid は PID。
-- ウィンドウのタイトルは持たない。capture_partial は、所有関係を確かめられず、モデルへ渡す画像から除外したウィンドウがあったタスクで 1
-- （通知 capture_partial の記録。停止の理由ではない）。既存の行は NULL / 0 のまま（再計算しない）。

ALTER TABLE agent_tasks ADD COLUMN target_window_id TEXT;
ALTER TABLE agent_tasks ADD COLUMN target_process TEXT;
ALTER TABLE agent_tasks ADD COLUMN target_pid INTEGER CHECK (target_pid IS NULL OR target_pid >= 0);
ALTER TABLE agent_tasks ADD COLUMN capture_partial INTEGER NOT NULL DEFAULT 0 CHECK (capture_partial IN (0, 1));
