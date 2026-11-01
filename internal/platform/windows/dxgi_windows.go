//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"image"
	"time"

	d3d "github.com/kirides/go-d3d"
	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/outputduplication"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// DXGI Desktop Duplication による frameSource。kirides/go-d3d v1.0.1（MIT、cgo なし）の OutputDuplicator を使う。
// go-d3d は変更矩形を公開しないので、Frame.Dirty は全面になる（差分の検出は Streamer のタイル比較）。
// OS が変化を伝えたときだけコマが得られる（AcquireNextFrame の待ちが時間切れなら変化なし）。

const (
	// recoverTries と recoverDelay は、DXGI を失ったあと（ACCESS_LOST など）に作り直す回数と、その間隔。
	// 1 秒ほど（200ms × 5 回）で戻らなければ（ロック画面・UAC など）GDI に切り替える。
	recoverTries = 5
	recoverDelay = 200 * time.Millisecond

	// dxgiPollMax は 1 回の AcquireNextFrame の待ちの上限。ctx の取り消しに、これ以上遅れずに応える。
	dxgiPollMax = 100 * time.Millisecond

	// maxDXGIOutputs は、モニターに対応する出力を探すときに調べる出力の数の上限。
	maxDXGIOutputs = 16
)

type dxgiSource struct {
	device    *d3d11.ID3D11Device
	deviceCtx *d3d11.ID3D11DeviceContext
	dup       *outputduplication.OutputDuplicator
	// spare は次の Next で使う撮影先。コマを返したら nil にして（受け手のものになる）、必要なときだけ作る。
	spare *image.RGBA
	size  image.Rectangle
}

// openDXGISource は m に対応する DXGI の出力の Desktop Duplication を開く。
// 出力は既定のアダプター（既定のデバイスのもの）から探し、位置と大きさ（物理ピクセル）が m と一致するものを使う。
// 見つからない・開けないなら error（呼び出し側が GDI に切り替える）。マルチ GPU でほかのアダプターに接続されたモニターはここでは見つからない。
func openDXGISource(m domain.Monitor) (frameSource, error) {
	device, deviceCtx, err := d3d11.NewD3D11Device()
	if err != nil {
		return nil, fmt.Errorf("dxgi: create device: %w", err)
	}
	want := image.Rect(m.Origin.X, m.Origin.Y, m.Origin.X+m.Size.W, m.Origin.Y+m.Size.H)

	var lastErr error
	for output := range uint(maxDXGIOutputs) {
		dup, err := outputduplication.NewIDXGIOutputDuplication(device, deviceCtx, output)
		if err != nil {
			if errors.Is(err, d3d.DXGI_ERROR_NOT_FOUND) {
				break // これより先に出力は無い
			}
			lastErr = err // ほかのプロセスが使用中などで開けない出力は飛ばす
			continue
		}
		bounds, err := dup.GetBounds()
		if err != nil || bounds != want {
			dup.Release()
			lastErr = err
			continue
		}
		return &dxgiSource{device: device, deviceCtx: deviceCtx, dup: dup, size: image.Rect(0, 0, want.Dx(), want.Dy())}, nil
	}
	deviceCtx.Release()
	device.Release()
	if lastErr == nil {
		lastErr = errors.New("no output matches the monitor")
	}
	return nil, fmt.Errorf("dxgi: open output: %w", lastErr)
}

// Next は OS が画面の変化を伝えるのを最大 wait（dxgiPollMax まで）待つ。
func (s *dxgiSource) Next(ctx context.Context, wait time.Duration) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	img := s.spare
	if img == nil {
		img = image.NewRGBA(s.size)
	}
	timeout := uint(max(min(wait, dxgiPollMax).Milliseconds(), 1))
	err := s.getImage(img, timeout)
	if errors.Is(err, outputduplication.ErrNoImageYet) {
		s.spare = img
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.spare = nil
	makeOpaque(img.Pix)
	return img, nil
}

// getImage は dup.GetImage を呼ぶ。go-d3d はデスクトップの大きさが img と合わないと範囲外アクセスで panic し得るので、
// panic は（アプリごと落とさず）エラーにして、呼び出し側に作り直させる。
func (s *dxgiSource) getImage(img *image.RGBA, timeoutMs uint) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("dxgi: capture panicked: %v", r)
		}
	}()
	return s.dup.GetImage(img, timeoutMs)
}

func (s *dxgiSource) Close() {
	s.dup.Release()
	s.deviceCtx.Release()
	s.device.Release()
}
