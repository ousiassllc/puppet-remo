// Command a2probe は Issue #156 A-2 の成立性（S1〜S3）を Windows 実機で確かめる専用プローブ。製品には含めない。
//
// 1 本の exe が 2 つの役を持つ:
//   - probe: 司令塔。専用のテスト窓（fixture）を自分で起動し、READY で名乗った PID・HWND を OS の事実と照合してから、
//     所有関係（GW_OWNER・hwndMenuOwner の鎖）と PrintWindow での写り方を測る。結果を report.txt・result.json・PNG に出す。
//   - fixture: テスト窓を出す側。メニューバー・右クリックメニュー・保存ダイアログ（同一プロセス）を持つ窓 T、同一プロセスの無関係な窓、
//     別プロセスの最前面窓（マゼンタ一色）、別プロセスが T に所有された窓を出す。標準入出力の行プロトコルで probe の指示を受ける。
//
// 守ること: 撮るのは照合済みの fixture の窓だけ（PrintWindow）。デスクトップ・画面領域・モニターの撮影は一切しない。
// グローバル入力（SendInput・カーソル移動）は使わない。fixture へはウィンドウメッセージだけを送る。ネットワークは使わない。
// 失敗したら失敗として記録し、別の方式に黙って切り替えない。
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  a2probe probe [--out DIR] [--timeout 120s] [--monitor N]   成立性の測定を実行する（既定の出力先: %TEMP%\a2probe-out-<日時>。N は fixture を置くモニターの Index。既定はプライマリ）
  a2probe fixture --role=main|interferer|foreign ...   probe が内部で起動する（直接は使わない）
  a2probe version`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "%s\n", usage)
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "probe":
		code = runProbe(os.Args[2:])
	case "fixture":
		code = runFixture(os.Args[2:])
	case "version":
		fmt.Println("a2probe " + probeVersion)
	default:
		fmt.Fprintf(os.Stderr, "%s\n", usage)
		code = 2
	}
	os.Exit(code)
}

// probeVersion は出力（report.txt・result.json）に書くプローブの版。測定の項目や判定を変えたら上げる。
const probeVersion = "2"
