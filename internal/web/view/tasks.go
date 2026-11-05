package view

// このファイルはエージェントのタスク（M4。FR-H10・FR-H02）の表示モデル。指示（prompt）・最終回答・エラー文は利用者・外部由来の
// 文字列なので、描画する側（templ）が HTML エスケープする。入力した文字列の本文・要素の値は持たない（FR-G23）。

// NavTasks はサイドバーの「タスク」。
const NavTasks Nav = "tasks"

// TaskStatus はタスクの状態の 1 行（状態バッジ・ステップ数・経過時間・中断のボタン）。ダッシュボードの実行中タスクと、タスクの詳細の
// 先頭が同じ部品を使い、POST /tasks/{id}/cancel の応答もこの部品（呼び出し元の同じ部品を差し替える）。
// 中断は元に戻せない破壊的な操作なので、ボタンは確認モーダル（GET ConfirmURL）を開き、確定のボタンだけが POST CancelURL を送る。
// CancelURL・ConfirmURL が空なら中断のボタンは出さない（終了済み。状態駆動）。
type TaskStatus struct {
	DOMID     string // 状態の 1 行の id（確認モーダルの確定が、結果を差し込む先）
	Code      string // domain.TaskStatus の値
	Label     string
	Tone      BadgeTone
	Steps     string // 「3 / 50」（実行したステップ数 / 上限）
	Elapsed   string
	CancelURL string
	// ConfirmURL は中断の確認モーダルの URL。
	ConfirmURL string
}

// TaskRow はタスクの一覧の 1 行。
type TaskRow struct {
	ShortID       string // 画面に出す短い ID（末尾 8 文字）
	DetailURL     string
	Client        string // 依頼元のクライアント名（スナップショット。無ければ「（削除済み）」）
	PromptExcerpt string // 指示の先頭（空白をたたみ、長ければ末尾を「…」にした 1 行）
	Code          string // domain.TaskStatus の値
	Label         string
	Tone          BadgeTone
	Steps         string
	Created       string
	Elapsed       string
}

// TaskList はタスクの一覧の 1 ページ（表・「次へ」）。NextURL が空なら次のページは無い。
type TaskList struct {
	Rows    []TaskRow
	NextURL string
}

// TaskListPage はタスク一覧のページ。
type TaskListPage struct {
	Chrome Chrome
	List   TaskList
}

// TaskStepRow はタスク詳細の 1 行（監査ログの行）。No は所属するステップの番号で、1 つのステップに複数の行が付きうる
// （複数のアクション・Judge の判定・安全チェック）。実行したステップとして番号を出すのはアクションの行だけで、
// それ以外の行は KindLabel（「Judge の判定」など）を付けて番号を出さない（重複したステップに見せない）。
type TaskStepRow struct {
	No         int
	KindLabel  string // アクション以外の行（pass・judge・safety_check）の種類。アクションの行は空
	Route      string // fast | openai
	RouteLabel string
	Event      string
	Result     string
	ResultTone BadgeTone
	Summary    string
	Args       []AuditArg // 秘密・入力した文字列の本文を伏せ終えた値
	AuditURL   string     // /audit/{id}。無ければ空
	ThumbURL   string     // 監査の画面と同じ経路のサムネイル。無ければ空
	At         string
}

// TaskDetail はタスク 1 件の詳細。
type TaskDetail struct {
	Chrome         Chrome
	ID             string
	Client         string
	Prompt         string
	Status         TaskStatus
	Model          string
	MaxSteps       int
	MaxDurationSec int
	StepCount      int
	FastSteps      int
	OpenAISteps    int
	Handoff        string // 引き継ぎ理由の説明（無ければ空）
	InputTokens    string
	OutputTokens   string
	ResultText     string
	ResultNote     string // 最終回答に添える、completed がモデルの申告であることの注意（FR-G13）。最終回答が無ければ空
	ErrorCode      string
	ErrorLabel     string // 既知の error_code の説明（未知なら空）
	Created        string
	Started        string
	Finished       string
	Steps          []TaskStepRow
	BackURL        string
}

// RunningTask はダッシュボードの実行中タスク（FR-H02）。
type RunningTask struct {
	ShortID       string
	DetailURL     string
	Client        string
	PromptExcerpt string
	Status        TaskStatus
}

// RunningTasks はダッシュボードの実行中タスクのパネル。Items が空ならパネルの中身は描画しない（隠し入力の版だけ残す）。
// Rev は表示の版（GET /poll が差し替えの要否に使う）。
type RunningTasks struct {
	Items []RunningTask
	Rev   string
}
