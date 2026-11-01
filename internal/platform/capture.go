package platform

import (
	"context"
	"image"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Capturer は画面キャプチャ。モニターごとに 1 つ。継続キャプチャ（Watch）は DXGI Desktop Duplication を主、GDI BitBlt をフォールバックとし、1 枚撮り（Grab）は GDI BitBlt。
type Capturer interface {
	// Monitors はモニター一覧を返す。Index は一意で、ちょうど 1 台が Primary。
	Monitors(ctx context.Context) ([]domain.Monitor, error)
	// Grab は 1 枚を撮る（screenshot ツール・エージェント・サムネイル用）。呼び出しごとに完結する。
	// 画像の大きさはモニターの Size（物理ピクセル）で、原点は (0, 0)。未知の monitor は ErrInvalidArgument。
	Grab(ctx context.Context, monitor int) (*image.RGBA, error)
	// Watch は継続キャプチャを開始する（Streamer 用）。fps は 1 秒あたりに流すコマの上限。
	// monitor が未知、または fps が 0 以下なら ErrInvalidArgument（開始できないので goroutine は起動しない）。
	// モニターごとに 1 本の goroutine が撮り、ctx のキャンセルで停止して 2 つのチャネルを閉じる（閉じる前に goroutine は終わっている）。
	//
	// frames には画面が変化したときだけ流す（最初の 1 コマは必ず流す）。受け手が詰まっている間は撮るのを待たせる。
	// DXGI の ACCESS_LOST は内部で再初期化し、復旧できなければ ErrDesktopUnavailable を errs に流して GDI に切り替える。
	// errs は詰まっても撮るのを待たせない（受け手がいなければ捨てる）。同じエラーが続くときは最初の 1 回だけ流す。
	Watch(ctx context.Context, monitor int, fps int) (frames <-chan Frame, errs <-chan error, err error)
}

// Frame は継続キャプチャの 1 コマ。
//
// Image は受け手のもので、Capturer は流したあとに触らない（再利用しない）。大きさと原点は Grab と同じ
// （モニターの物理ピクセル、原点 (0, 0)）。解像度が変わったときは、変わったあとの大きさで流す。
// Dirty は前のコマから変化した矩形（Image の範囲内、空でない）。変化の範囲を OS から得られない実装は、全面（Image.Bounds()）を 1 つ入れる。
// タイル（64×64）単位の比較は Streamer が行う。Seq は Watch ごとに 1 から始まり、流す 1 コマごとに 1 ずつ増える（uint32 を超えたら 0 に戻る）。
type Frame struct {
	Image *image.RGBA
	Dirty []image.Rectangle
	Seq   uint32
}
