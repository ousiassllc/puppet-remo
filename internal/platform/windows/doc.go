// Package windows は internal/platform のインターフェースの Windows 実装（cgo なし、golang.org/x/sys/windows と user32 / gdi32 などの直接呼び出し）。
//
// 実装:
//   - Capturer: Grab は GDI BitBlt（マルチモニター・DPI）。Watch（継続キャプチャ）は DXGI Desktop Duplication（kirides/go-d3d）を主、GDI をフォールバックとする。制御（再初期化・切り替え・fps）は OS 非依存の watch.go。
//   - Input（platform.InputInjector）: SendInput。呼び出しは専用の OS スレッドへ直列化する。
//   - WindowManager: ウィンドウの列挙・前面・フォーカス（昇格ウィンドウへの入力は UIPI のため input_blocked）。
//   - Clipboard: CF_UNICODETEXT の読み書き。
//   - Launcher（platform.AppLauncher）: CreateProcess（lpApplicationName に ExePath を明示・シェル非経由・標準権限・ハンドルを閉じる）。
//     コマンドラインの組み立て（buildCommandLine）と失敗の分類（classifyLaunchError）、存在確認（inspectApp）は純粋なロジックで、Linux でもテストする。
//   - FilePicker（platform.FilePicker）: GetOpenFileNameW（`.exe` のフィルター、親はメインウィンドウ、ctx の取り消しで WM_CLOSE）。
//     親の HWND は NewFilePicker に関数で渡す（Wails のメインウィンドウの NativeWindow()。関数は internal/app の WailsHost.MainWindowHandle で、cmd/puppet-remo/main.go が渡す）。
//   - FolderOpener（platform.FolderOpener）: ShellExecute の "open" で、実在するディレクトリ（絶対パス）だけをエクスプローラーで開く（#54。ログ画面のボタン）。
//     動詞は固定・引数なしで、シェル・コマンドラインを経由しない。ファイルは開かず、エラーにパスを含めない。開くパスは呼び出し元（app.LogReader）が固定する。
//   - Power: SetThreadExecutionState によるスリープ・画面オフの抑止（NFR-A05）。スレッドごとの状態なので専用の OS スレッドで設定・解除する。
//
// Win32 を呼ぶコードは *_windows.go（//go:build windows）に置く。キー名・修飾キーの変換表、座標の正規化、
// ホイールの端数の持ち越し、入力の組み立て（Input の本体）、ピクセルの変換などの純粋なロジックは
// タグなしのファイルに置き、Linux でもテストする。Input は driver インターフェースの背後に Win32 を隠し、
// Windows では SendInput の実装を、テストでは偽の実装を差し込む。
//
// 実機でしか確かめられない動作（実キャプチャ、実際の入力、UIPI、ロック画面）の手順は
// docs/operations/smoke-windows.md、実機で動かす契約テストはビルドタグ `smoke` の smoke_test.go。
package windows
