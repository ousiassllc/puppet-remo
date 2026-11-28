-- v7: タスクの接続方式（agent_tasks.connection。FR-G25・#49・#247）。タスクの開始時の agent.connection を固定して記録する。
-- chatgpt = 公式の Sign in with ChatGPT、chatgpt-codex = Codex の app-server、openai_api_key = OpenAI API キー。
-- 既存の行（#49 より前）はすべて OpenAI API キーの経路なので、DEFAULT の openai_api_key が入る。

ALTER TABLE agent_tasks ADD COLUMN connection TEXT NOT NULL DEFAULT 'openai_api_key'
	CHECK (connection IN ('chatgpt', 'chatgpt-codex', 'openai_api_key'));
