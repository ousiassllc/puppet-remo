// Command macprobe は Issue #186 の成立性（macOS 実機）を確かめる専用プローブ。製品には含めない。
//
// 確かめること（Issue #186 の項目）:
//
//	1 capture : ScreenCaptureKit の 1 枚撮りと継続キャプチャ（dirtyRects、Retina・複数ディスプレイの情報）
//	2 input   : CGEventPost のクリック・キー・Unicode 入力（アクセシビリティ・入力監視の許可の要否）
//	3 overlay : 自プロセスの透明オーバーレイが撮影に映るか（NSWindowSharingNone・SCContentFilter での除外・screencapture）
//	4 status  : 画面収録・アクセシビリティ・入力監視の許可の状態、署名の情報（署名を変えたときの許可は docs の手順）
//	5 windows : kCGWindowName を取るのに画面収録の許可がどこまで要るか
//
// 守ること: 撮るのはディスプレイとプローブ自身の窓だけ。結果に他アプリの窓タイトル・内容は書かない（数だけ）。
// 入力はプローブ自身の窓だけに向ける。画面ロック中は HID タップへの入力を一切送らない。
// 許可（TCC）は回避しない。足りなければ失敗として記録する。darwin 以外ではビルドだけ通る（実行は何もしない）。
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  macprobe run [status|request|windows|capture|input|overlay|all] [--out DIR] [--seconds N] [--allow-global-input]
      --out DIR              結果（result.json・report.txt・PNG）の出力先。既定は ./macprobe-out-<日時>
      --seconds N            継続キャプチャの 1 区間の秒数（既定 3）
      --allow-global-input   画面ロックされておらず、プローブの窓がキー窓のときだけ HID タップへ入力を送る
      request は画面収録・アクセシビリティの許可を OS に要求する（確認ダイアログが出る。all には含めない）
  macprobe fixture [--title T] [--seconds N]   run windows が内部で起動する（直接は使わない）
  macprobe version`

// probeVersion は出力に書くプローブの版。測定の項目や判定を変えたら上げる。
const probeVersion = "1"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "%s\n", usage)
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "run":
		code = runProbe(os.Args[2:])
	case "fixture":
		code = runFixture(os.Args[2:])
	case "version":
		fmt.Println("macprobe " + probeVersion)
	default:
		fmt.Fprintf(os.Stderr, "%s\n", usage)
		code = 2
	}
	os.Exit(code)
}
