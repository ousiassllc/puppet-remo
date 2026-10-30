// Package platformtest は internal/platform のテスト支援を持つ。
//
//   - 偽の実装（Fake*）: 呼び出しを記録し（Recorder）、結果とエラーを注入できる。service など
//     利用側のテストが platform の本物の実装の代わりに使う。
//   - 契約テスト（Run*Contract）: 実装が満たすべき振る舞いの共通テスト。偽の実装（CI で走る）にも
//     Windows 実装（実機のスモーク）にも同じものを流す。
//
// Windows 実装のスモークテストは、internal/platform/windows に `//go:build windows && smoke` の
// テストファイルを置き、実装を返すファクトリを渡して契約テストを呼ぶ:
//
//	func TestWindowManagerContract(t *testing.T) {
//		platformtest.RunWindowManagerContract(t, func(t *testing.T) platform.WindowManager { return windows.NewWindowManager() })
//	}
//
// `go test -tags smoke ./internal/platform/windows/` で実行する。入力・クリップボード・ホットキーの契約は
// 実際のデスクトップを操作するため、使い捨ての環境で実行する。
package platformtest
