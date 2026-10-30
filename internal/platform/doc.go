// Package platform は OS 依存の機能のインターフェースを持つ（NFR-O03。docs/components/overview.md §3）。
//
// 実装は Windows が internal/platform/windows に、テスト用の偽の実装と契約テストが
// internal/platform/platformtest にある。1 つのインターフェースに 1 つの責務を持たせ、
// 許可・拒否の判定はしない（事実を返すだけ。判定は service.InputGuard）。
//
// goroutine を内部で起動するのは Capturer.Watch（モニターごとに 1 本）と Hotkey.Register
// （Win32 のメッセージループ用に OS スレッド 1 本）、FilePicker.PickExecutable（ダイアログを開いている間だけ ctx の監視に 1 本）だけで、それ以外は呼び出し元の goroutine で同期的に動く。
// InputInjector と UIAutomation の実装は、スレッドアフィニティのため専用スレッドへ呼び出しを直列化する
// （呼び出し側から見ると、スレッドセーフな同期呼び出し）。
//
// エラーの規約（契約テストが検査する）:
//   - 引数の不正は domain.ErrInvalidArgument
//   - 存在しないウィンドウ ID は domain.ErrWindowNotFound
//   - デスクトップを使えない（ロック中・セッション切断）は domain.ErrDesktopUnavailable
//   - AppLauncher.Launch の失敗は *LaunchError（理由は domain.LaunchFailure。メッセージにパス・引数を含めない）、
//     AppLauncher.Inspect の対象が無いときは ErrNotFound
//   - ctx が取り消し済みなら、副作用を起こさずに ctx.Err() を（errors.Is で判定できる形で）返す
package platform
