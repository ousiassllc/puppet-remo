package windows

// 拡張ウィンドウスタイル（GWL_EXSTYLE）の値。
const (
	wsExTransparent = 0x00000020 // WS_EX_TRANSPARENT: マウスを下のウィンドウへ通す（クリックスルー）
	wsExLayered     = 0x00080000 // WS_EX_LAYERED
)

// isOverlayStyle は拡張スタイル ex が、操作中オーバーレイ（#69）の「クリックスルー」（WS_EX_TRANSPARENT かつ WS_EX_LAYERED）かを返す。
// 自プロセスのウィンドウのときだけ domain.WindowInfo.Overlay に使う（他プロセスのクリックスルーのウィンドウは Overlay にしない）。
func isOverlayStyle(ex uintptr) bool {
	const both = wsExTransparent | wsExLayered
	return ex&both == both
}
