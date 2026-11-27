package codextest

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// Proc は Fake を appserver.Process として包む（io.Pipe でつなぐ）。
type Proc struct {
	inW     *io.PipeWriter // クライアントが書く（Fake が読む）
	outR    *io.PipeReader // クライアントが読む（Fake が書く）
	outW    *io.PipeWriter
	done    chan struct{}
	stopped atomic.Bool
	crashed atomic.Bool
	once    sync.Once
}

// Start は f を動かし、つないだ Proc を返す。Serve が終わる（標準入力を閉じた・Crash した）とプロセスは終了したことになる。
func Start(f *Fake) *Proc {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &Proc{inW: inW, outR: outR, outW: outW, done: make(chan struct{})}
	go func() {
		f.Serve(inR, outW)
		p.end()
	}()
	return p
}

func (p *Proc) end() {
	p.once.Do(func() {
		_ = p.outW.Close() // クライアントの読み取りは EOF になる
		close(p.done)
	})
}

// Stdin・Stdout・Done・ExitInfo・Stop は appserver.Process の実装。
func (p *Proc) Stdin() io.Writer      { return p.inW }
func (p *Proc) Stdout() io.Reader     { return p.outR }
func (p *Proc) Done() <-chan struct{} { return p.done }

// ExitInfo は終了後の説明（終了前は ""）。
func (p *Proc) ExitInfo() string {
	select {
	case <-p.done:
	default:
		return ""
	}
	if p.crashed.Load() {
		return "codextest: crashed"
	}
	return "codextest: exited"
}

// Stop は標準入力を閉じて、終了を待つ。
func (p *Proc) Stop() {
	p.stopped.Store(true)
	_ = p.inW.Close()
	<-p.done
}

// Stopped は Stop が呼ばれたか。
func (p *Proc) Stopped() bool { return p.stopped.Load() }

// Crash は誰も頼んでいない終了を起こす: 出力が閉じ、読み取りの Serve も終わる。
func (p *Proc) Crash() {
	p.crashed.Store(true)
	_ = p.inW.CloseWithError(io.ErrClosedPipe)
	p.end()
}

// HelperMain は、本物の子プロセスの代わりにテストバイナリ自身を app-server として動かすための入口。
// TestMain の先頭で呼ぶ。環境変数 CODEXTEST_HELPER=1 のときだけ動き、標準入出力で Fake を提供して終了する
// （それ以外のときは何もせず戻る）。CODEXTEST_REPORT のパスへ、受けた引数と環境変数の名前（値は書かない）を JSON で残す。
func HelperMain() {
	if os.Getenv("CODEXTEST_HELPER") != "1" {
		return
	}
	if path := os.Getenv("CODEXTEST_REPORT"); path != "" {
		writeReport(path)
	}
	f := &Fake{CodexHome: os.Getenv("CODEX_HOME")}
	f.Serve(os.Stdin, os.Stdout)
	os.Exit(0)
}
