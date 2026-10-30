package platform

import (
	"context"
	"image"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Capturer は画面キャプチャ。モニターごとに 1 つ。M1 は GDI BitBlt のみ（DXGI は M2）。
type Capturer interface {
	// Monitors はモニター一覧を返す。Index は一意で、ちょうど 1 台が Primary。
	Monitors(ctx context.Context) ([]domain.Monitor, error)
	// Grab は 1 枚を撮る（screenshot ツール・エージェント・サムネイル用）。呼び出しごとに完結する。
	// 画像の大きさはモニターの Size（物理ピクセル）で、原点は (0, 0)。未知の monitor は ErrInvalidArgument。
	Grab(ctx context.Context, monitor int) (*image.RGBA, error)
	// Watch は継続キャプチャを開始する（Streamer 用。M2）。frames は変化した矩形つきで流れる。
	// ctx のキャンセルで停止し、2 つのチャネルを閉じる。DXGI の ACCESS_LOST は内部で再初期化し、
	// 復旧できなければ ErrDesktopUnavailable を errs に流して GDI に切り替える。
	Watch(ctx context.Context, monitor int, fps int) (frames <-chan Frame, errs <-chan error, err error)
}

// Frame は継続キャプチャの 1 コマ。
type Frame struct {
	Image *image.RGBA
	Dirty []image.Rectangle
	Seq   uint32
}
