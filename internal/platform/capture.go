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

// GroupCapturer は対象グループ（domain.Group）のメンバーだけを撮る、Capturer のオプションの拡張。
// Capturer には足さない（Windows 実装は GrabGroup（PrintWindow）を実装済みだが実機での成立性は未確認）。service は型アサーションで取り、実装がなければ
// 撮れないもの（capture_unavailable）として止める。**モニター撮影（Grab・Watch）やデスクトップの切り抜きに切り替えてはならない。**
type GroupCapturer interface {
	// GrabGroup は members を、各ウィンドウ自身の内容（PrintWindow など、ほかのウィンドウの画素が混ざらない方式）で撮り、
	// メンバーの可視部分の外接矩形の空（透明）のキャンバスの上に Z オーダーで置いて返す。
	// members は前面が先（後ろから順に描く）。同一性（domain.WindowRef の ID・PID・ProcessStart・Class）が一致しないウィンドウは撮らず、CaptureGone にする。
	// 個々のメンバーが撮れないことはエラーにせず GroupImage.Members の Status で返す（T が撮れないのかほかのメンバーなのかを service が見分けるため）。
	// error は呼び出し全体の失敗（ctx のキャンセル・デスクトップを使えない）だけ。
	GrabGroup(ctx context.Context, members []domain.WindowRef) (GroupImage, error)
}

// CaptureStatus はメンバー 1 つの撮影結果。
type CaptureStatus string

const (
	CaptureOK           CaptureStatus = "ok"            // 撮れて合成に入った
	CaptureNotPrintable CaptureStatus = "not_printable" // ウィンドウは見つかったが内容を撮れない（PrintWindow の失敗・最小化など）
	CaptureGone         CaptureStatus = "gone"          // ウィンドウがない・同一性が違う（HWND の再利用）
)

// MemberCapture は GrabGroup に渡したメンバー 1 つの結果（members と同じ順）。
type MemberCapture struct {
	Ref    domain.WindowRef
	Status CaptureStatus
	// Rect は Status が CaptureOK のとき、合成に置いたメンバーの矩形（仮想デスクトップの物理座標）。それ以外は零値。
	Rect domain.Rect
}

// GroupImage は GrabGroup の結果。
type GroupImage struct {
	// Image は撮れたメンバーだけを置いた透明のキャンバス。大きさは撮れたメンバーの矩形の外接矩形。撮れたメンバーがなければ nil。
	Image *image.RGBA
	// Origin は Image の左上（0, 0）の仮想デスクトップ上の物理座標。合成画像の座標 p → 物理座標は Origin + p。
	Origin  domain.Point
	Members []MemberCapture
}

// Status は id のメンバーの撮影結果を返す。渡していない id は ok=false。
func (g GroupImage) Status(id string) (MemberCapture, bool) {
	for _, m := range g.Members {
		if m.Ref.ID == id {
			return m, true
		}
	}
	return MemberCapture{}, false
}
