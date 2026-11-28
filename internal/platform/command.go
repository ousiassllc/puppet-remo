package platform

import (
	"context"
	"time"
)

// CommandRunner は、許可リストの項目を実際のプロセスとして起動し、終了を待って結果を返す窓口（#265。MCP の run_command の実体。
// 許可・引数の検査・リース・監査は service.CommandExec が行い、ここは渡された実行ファイルと引数をそのまま起動するだけ）。
//
// 実装が守ること:
//   - シェルを使わない。Executable を引数の配列 Args で直接起動する（`cmd /c` などで包まない。引数は 1 つずつの文字列のまま渡す）
//   - 標準入力は空（すぐ EOF）。ウィンドウは出さない。環境変数はホストの環境のうち、動作に必要な最小限の名前だけを引き継ぐ
//   - Timeout を過ぎたら、プロセスを（Windows ではジョブオブジェクトで子孫ごと）終了させ、TimedOut を立てて返す（エラーにはしない）
//   - ctx が取り消されたときも同じように終了させ、ctx.Err() を返す
//   - stdout・stderr はそれぞれ MaxOutputBytes まで保持し、超えた分は捨てて Truncated を立てる（パイプを読み続け、プロセスを止めない）
//   - 起動できなかった（実行ファイルが無い・実行できない）ときは domain.ErrCommandStartFailed（メッセージ・詳細にパスを入れない）
//
// 非 0 の終了コードはエラーではなく、結果の ExitCode で返す。どの goroutine からも呼べる。
type CommandRunner interface {
	Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
}

// CommandSpec は CommandRunner.Run の指定。
type CommandSpec struct {
	Executable     string   // ローカルの絶対パス
	Args           []string // 実行ファイルを除く引数
	Dir            string   // 作業フォルダ。空ならプロセスの既定
	Timeout        time.Duration
	MaxOutputBytes int // stdout・stderr それぞれの保持の上限
}

// CommandResult は CommandRunner.Run の結果。タイムアウトで終了させたときの ExitCode は -1。
type CommandResult struct {
	ExitCode        int
	Stdout, Stderr  []byte
	StdoutTruncated bool
	StderrTruncated bool
	TimedOut        bool
	Duration        time.Duration
}
