// Package typesafe は TypeSafe（Jev）の HTTP クライアント（docs/components/overview.md §4.4。
// FR-G18・FR-G21、NFR-S18・NFR-PR04）。
//
// 高速パスの「候補表から ID を 1 つ選ぶ」問い合わせ（Choice 質問 next）と、目標がすでに
// 達成されていそうかの問い合わせ（Noul 質問 goal_maybe_done）を、1 回の
// POST /v1/systemone にまとめて送る。境界のインターフェース Chooser の背後に置き、
// service のテストはこれを偽の実装に差し替える（NFR-O06）。
//
// 送る内容は ChoiceRequest の欄に限られ、スクリーンショット・要素の値・クリップボード・
// 入力した文字列の本文・API キーを入れる欄はない（型で送れなくしている）。
// Jev の回答は自由文を持たず、受理する選択は送った選択肢の名前だけ（NFR-S17）。
package typesafe

import (
	"context"
	"errors"
	"time"
)

// Chooser は境界のインターフェース。FastPath が使い、テストではモックする（NFR-O06）。
type Chooser interface {
	// Choose は 1 回の問い合わせ。5 秒でタイムアウトし（NFR-S18）、自動再試行はしない。
	// ctx が先に取り消されたときは、ErrUnavailable ではなく ctx のエラー（context.Canceled など）を
	// 返す（呼び出し側のキャンセルは縮退ではないため）。
	Choose(ctx context.Context, req ChoiceRequest) (ChoiceResult, error)
}

// ChoiceRequest は 1 回の問い合わせの内容。NFR-PR04 が定める「送るもの」だけを欄に持つ。
type ChoiceRequest struct {
	Model    string          // agent.fastpath.model（既定 jev-1.13）
	Goal     string          // タスクの指示
	Window   ChoiceWindow    // タイトルは送る（NFR-PR04。文書名・URL などの機微な情報を含みうる）
	Elements []ChoiceElement // 候補。値・テキスト本文は持たない。focus_window の候補は Role "window"
	History  []string        // 高速ステップの短い文。直近 5 件だけを送る（入力した文字列の本文は含めない）
}

// ChoiceWindow はフォアグラウンドウィンドウのタイトルとプロセス名。
type ChoiceWindow struct {
	Title   string
	Process string
}

// ChoiceElement は候補表の 1 行。ID は選択肢の名前としてそのまま送り、回答もこの ID で返る。
type ChoiceElement struct {
	ID      string
	Role    string
	Label   string
	Enabled bool
}

// ChoiceResult は Jev の回答。Choice は送った候補の ID か、メタ候補の ReobserveChoice / AbstainChoice のいずれかに限る。
type ChoiceResult struct {
	Choice        string  // 候補の ID、"reobserve"、"abstain"
	Confidence    float64 // Choice 質問の確信度（0〜1）
	GoalMaybeDone float64 // Noul 質問 goal_maybe_done の確率（0〜1）
}

// メタ候補の名前。候補表の ID（ハッシュ）と衝突してはならない。
const (
	ReobserveChoice = "reobserve" // 画面がまだ変わっていない・判断材料が足りないので観測し直す
	AbstainChoice   = "abstain"   // 判断できない。OpenAI へ引き継ぐ
)

var (
	// ErrUnavailable はタイムアウト・ネットワークエラー・5xx・429・要求本文が MaxBody 超（送信前に判定）、
	// およびそれ以外の想定外の HTTP 状態。FastPath はそのステップを chooser_unavailable で引き継ぐ。
	ErrUnavailable = errors.New("typesafe: unavailable")
	// ErrUnauthorized は 401（キー無効）、またはキーが空・ヘッダーに使えない文字を含む。
	ErrUnauthorized = errors.New("typesafe: unauthorized")
	// ErrBadResponse は応答の解析に失敗した、または必要な項目が無い・範囲外・送っていない選択肢を返した。
	// FastPath は invalid_choice として扱う。
	ErrBadResponse = errors.New("typesafe: bad response")
)

// Config は New の設定。ゼロ値の欄は既定値になる。Endpoint の差し替えはテスト用の引数で、設定項目ではない
// （送信先は api.typesafe.ai の HTTPS のみ。NFR-S18）。
type Config struct {
	Endpoint string        // 既定: https://api.typesafe.ai/v1/systemone
	Timeout  time.Duration // 既定: 5 秒（NFR-S18）
	MaxBody  int           // 要求本文の上限（バイト）。既定: 256 KiB（NFR-S18）
}

const (
	defaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	defaultTimeout  = 5 * time.Second
	defaultMaxBody  = 256 << 10
)

// maxHistory は送る履歴の件数（FR-G18）。多いときは新しい方を残す。
const maxHistory = 5

// maxResponseBody は応答本文の読み取り上限。応答は小さな JSON なので、超えたものは異常として扱う。
const maxResponseBody = 1 << 20
