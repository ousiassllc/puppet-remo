package domain

// OwnRegion は puppet-remo 自身のウィンドウが 1 台のモニター上に占める範囲。
// puppet-remo のウィンドウはキャプチャから外される（NFR-S06）ためスクリーンショットには写らず、
// そこへの入力は ErrInputBlocked（reason: own_window）で拒否される。AI が避けられるよう、この範囲を知らせる。
//
// Rect は Monitor の縮小スクリーンショットの座標（左上が原点、1 単位が縮小画像の 1 px）で、物理座標ではない。
// 名前・プロセス・ID などウィンドウの中身を示す値は持たない。
type OwnRegion struct {
	Monitor int
	Rect    Rect
}

// OwnWindowRegions は windows のうち自ウィンドウ（Own）が占める範囲を、モニターごとに縮小座標で返す。
// 複数のモニターにまたがるウィンドウはモニターごとに 1 件ずつ（各モニターの範囲へ切り詰めて）返す。
// 最小化されたウィンドウ、クリックスルーのオーバーレイ（Overlay。全モニターを覆うが入力を拒否せず、避ける範囲ではない）、どのモニターとも重ならないウィンドウ（座標を対応づけられないもの）は含めない。
// 順序は windows の順（Z オーダー）で、同じウィンドウ内ではモニターの順。該当がなければ nil。
func OwnWindowRegions(windows []WindowInfo, monitors []Monitor, maxDim int) []OwnRegion {
	var out []OwnRegion
	for _, w := range windows {
		if !w.Own || w.Overlay || w.Minimized {
			continue
		}
		for _, m := range monitors {
			if r, ok := m.ScaledRect(w.Rect, maxDim); ok {
				out = append(out, OwnRegion{Monitor: m.Index, Rect: r})
			}
		}
	}
	return out
}
