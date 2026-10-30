# Windows 実機スモーク手順（platform: キャプチャ・入力・ウィンドウ・クリップボード）

CI（`windows-latest`）には対話デスクトップがないため、GDI キャプチャ・`SendInput`・ウィンドウ列挙・クリップボードは CI では動かない（[コンポーネント設計](../components/overview.md) §10、NFR-O04）。本書は `internal/platform/windows` を**実機で**確かめる手順で、各 platform の機能ごとに確認する項目を並べる。M1 の範囲（FR-M01〜M13）だけを扱う。DXGI・UI Automation・ホットキー・オーバーレイは別の手順で確認する。

## 1. 準備

- 対話デスクトップのある Windows 10 / 11 の実機か VM。**使い捨ての環境**で行う（入力のテストは実際にカーソルを動かし、クリック・キー・文字入力を前面のウィンドウへ送る）。
- Go（`go.mod` の `go` 行のバージョン）をインストールする。cgo は不要。
- 確認したい構成を用意する: 単一モニターと、2 枚以上のモニター（左や上に副モニターを置いて座標が負になる配置を含む）、表示スケール 100% / 150% / 200%（設定 → ディスプレイ）。
- 前面に置くウィンドウとしてメモ帳を開き、空の文書にしておく。

## 2. 契約テスト（自動）

CI で偽の実装に流す契約テスト（`internal/platform/platformtest`）を、そのまま Windows 実装に流す。ビルドタグ `smoke` が必要。

```powershell
go test -tags smoke -v -count=1 ./internal/platform/windows/
```

| 環境変数 | 既定 | 意味 |
|---------|------|------|
| `PUPPET_REMO_SMOKE_INPUT` | 未設定 | `1` のときだけ入力のテスト（`TestSmokeInput`）を走らせる。カーソルを動かし、クリック・ホイール・キー・文字入力を前面へ送る |
| `PUPPET_REMO_SMOKE_X` / `PUPPET_REMO_SMOKE_Y` | プライマリモニターの中央 | 入力のテストがクリックしてよい座標（仮想デスクトップ上の物理座標）。害のない場所を指定する |
| `PUPPET_REMO_SMOKE_CLIPBOARD` | 未設定 | `1` のときだけクリップボードのテスト（`TestSmokeClipboard`）を走らせる。終了時にテキストを元に戻すが、テキスト以外の内容は失われる |

```powershell
$env:PUPPET_REMO_SMOKE_INPUT = "1"; $env:PUPPET_REMO_SMOKE_CLIPBOARD = "1"
go test -tags smoke -v -count=1 ./internal/platform/windows/
```

`TestSmokeCapturer` と `TestSmokeWindowManager` は画面を読むだけで、副作用はない。`go test` は成功（`PASS`）し、`SKIP` が出た項目は上の環境変数を設定して再実行する。

## 3. 機能ごとの確認項目（手動）

契約テストが見ない、実機でしか確かめられない項目。結果は PR のコメントか Issue に「確認済み / 未確認」で残す。

### 3.1 Capturer（GDI BitBlt。FR-M01・FR-M02）

| # | 確認項目 | 期待 |
|---|---------|------|
| C1 | 単一モニターで `Monitors` と `Grab` | 1 台、`Primary`、サイズが画面の物理解像度、`Grab` の画像が同じ大きさ |
| C2 | 表示スケール 150% / 200% | `Monitors` の `Size` が**物理ピクセル**（論理解像度ではない）。`DPI` が 144 / 192（`DPIScale` 1.5 / 2.0）。スクリーンショットがぼやけていない |
| C3 | マルチモニター（左・上に副モニター） | `Index` が左から右、上から下の順に 0 から連番。副モニターの `Origin` が負。各モニターの `Grab` がそのモニターの内容だけを写す（端が隣のモニターに食い込まない） |
| C4 | モニターごとに異なるスケール（例: 100% と 150%） | モニターごとの `DPI` が違う値、`Size` はそれぞれ物理ピクセル |
| C5 | メモ帳を開いて `Grab` した画像の色 | 色が R と B で入れ替わっていない。アルファが不透明（透過した画像にならない） |
| C6 | 画面をロック（Win+L）して `Grab` | 画面が写らず `desktop_unavailable`（黒い画像を成功として返さない）。ロック解除後は復旧 |
| C7 | レイヤードウィンドウ（半透明のウィンドウ・オーバーレイ） | `CAPTUREBLT` により写る |

### 3.2 InputInjector（`SendInput`。FR-M03〜M09）

| # | 確認項目 | 期待 |
|---|---------|------|
| I1 | `MoveTo` の座標精度（縮小 → 物理の往復。[FR-M16](../requirements/functional.md)） | `CursorPos` が指定した座標と**一致**する（各モニターの四隅の画素を含む。丸めで 1 px ずれたら `SetCursorPos` で補正される）。副モニターの負の座標、150% / 200% でも同じ |
| I2 | 仮想デスクトップの外への `MoveTo` | `invalid_coordinates`。カーソルは動かない |
| I3 | `Click`（左・右・中・ダブル・トリプル） | メモ帳のテキストの行でトリプルクリックすると行全体が選択される。ダブルクリックで単語が選択される。右クリックでコンテキストメニュー |
| I4 | 修飾キーつきの `Click` | `ctrl` + クリックなどが効く。クリック後に修飾キーが押しっぱなしにならない（直後に `a` を打って `A` や Ctrl 動作にならない） |
| I5 | `Drag` | メモ帳の文字列をドラッグして選択できる。`shift` つきでも動く。終了後にボタンが押しっぱなしにならない |
| I6 | `Scroll`（上下左右）と `Wheel`（小数のノッチ） | 長い文書で 1 ノッチが 1 回のスクロール（既定で 3 行）。`dy=0.5` の連続でも端数が失われずスクロールする。横スクロールが横長の表示（Excel など）で効く |
| I7 | `Key`（`ctrl+a`、`ctrl+shift+t`、`Enter`、`F5`、矢印・Home / End・PageUp / PageDown） | キーが効く。矢印・Home / End・Delete・Insert が**テンキーではなく**専用キーとして動く（`Extended` フラグ）。`win+left` でウィンドウがスナップする。記号（`ctrl+-`、`ctrl+;` など）は現在のキーボード配列で効く（日本語配列と英語配列の両方で確認） |
| I8 | 配列にない記号を `Key` に渡す | `invalid_argument`（AltGr が要る文字など） |
| I9 | `KeyEvent` の down / up（Web UI 用） | `ShiftLeft` の down → `KeyA` の down / up → `ShiftLeft` の up で `A` が入る。`ReleaseAll` で押下中のキーが離れる |
| I10 | `Text`: 英数字、日本語（「こんにちは、世界」）、絵文字（😀。サロゲートペア）、改行を含む文字列 | メモ帳にそのまま入る。IME がオンでも変換されずに入る。改行は Enter になる。タブは Tab になる。10,000 文字でも欠けない |
| I11 | `Text` の中断（`ctx` の取り消し） | 途中で止まり、押しっぱなしのキーが残らない |
| I12 | 管理者として起動したメモ帳へ入力する（アプリは標準権限） | `list_windows` で `Elevated` が真。入力は UIPI で届かない。`service.InputGuard` が `input_blocked` で先に拒否する（platform 単体では SendInput が成功を返すことがある） |
| I13 | `Ctrl+Alt+Del` | platform は送れない（SAS）。`service.InputGuard` が拒否する（NFR-S06）。platform の `Key` に渡しても OS が受け付けないことだけ確認する |
| I14 | ロック画面・UAC のセキュアデスクトップ・RDP の切断中 | `SendInput` の失敗が `desktop_unavailable`（`input_blocked` ではない） |

### 3.3 WindowManager（FR-M11・FR-M12）

| # | 確認項目 | 期待 |
|---|---------|------|
| W1 | `List` の項目 | メモ帳・ブラウザなどで ID（HWND の 10 進）、タイトル、プロセス名（`notepad.exe`）、PID、矩形、`Monitor`、`Foreground` が正しい。タイトルのないウィンドウと、別の仮想デスクトップのウィンドウ（cloaked）は含まれない。前面のウィンドウが先頭付近 |
| W2 | `Rect` | 影を含まない見た目の枠（DWM の拡張フレーム）。150% / 200% やマルチモニターで、`At` の判定と見た目が合う。最大化したウィンドウの矩形がモニターを覆う |
| W3 | `Minimized` | 最小化したウィンドウが `Minimized` で、`At` の結果に含まれない |
| W4 | `Elevated` | 管理者として起動したウィンドウ（管理者のメモ帳、タスクマネージャー）が真。通常のウィンドウは偽 |
| W5 | `Own` | このアプリ（ホスト GUI）自身のウィンドウが真。ほかは偽 |
| W6 | `Foreground` | クリックしたウィンドウが `Foreground` になる。デスクトップをクリックした直後などウィンドウがないときは `window_not_found` |
| W7 | `At` | 重なったウィンドウでは最前面のものを返す。モニターの外は「なし」 |
| W8 | `Focus` | 背面のウィンドウを前面にする。最小化されたウィンドウを元に戻して前面にする。別のアプリが前面でもフォーカスが移る（フォアグラウンドロックを `AttachThreadInput` で回避） |
| W9 | `Focus` の失敗 | 存在しない ID・閉じたウィンドウの ID は `window_not_found`。管理者ウィンドウ（こちらは標準権限）は `input_blocked` |
| W10 | 保護されたプロセス・システムのウィンドウ | `List` が失敗せず、プロセス名が空になることがある。`Elevated` は真（開けないものは入力できない扱い） |

### 3.4 Clipboard（FR-M13）

| # | 確認項目 | 期待 |
|---|---------|------|
| B1 | 往復（`WriteText` → `ReadText`） | 英数字・日本語・絵文字・改行（`\r\n`）・タブ・前後の空白・空文字列が同じ文字列で戻る。Win+V の履歴や貼り付けでも同じ内容 |
| B2 | 他のアプリが書いた内容の読み取り | メモ帳でコピーした日本語が読める |
| B3 | テキスト以外（画像）がクリップボードにあるとき | `ReadText` は空文字列（エラーにしない）。`WriteText` は画像を置き換える |
| B4 | 他のプロセスがクリップボードを開いている間（クリップボード管理ツールなど） | 約 0.5 秒まで再試行して成功する。それでも開けなければ原因つきのエラー |
| B5 | `ctx` の取り消し | 待ちの途中でも `context.Canceled` を返す |
| B6 | サイズ上限（1 MiB。NFR-S09） | platform は上限を持たない。上限は `service` / `mcp` の責務なので、ここでは確認しない |

### 3.5 全体

| # | 確認項目 | 期待 |
|---|---------|------|
| G1 | プロセスの DPI 設定 | アプリのマニフェストで Per-Monitor DPI Aware V2 を宣言する（[アーキテクチャ](../architecture/overview.md)）。`NewCapturer` / `NewInput` / `NewWindowManager` は、未設定のときだけ `EnableDPIAwareness` で設定する。マニフェストなしの `go test` でも物理ピクセルで動く |
| G2 | `GOOS=windows go build ./...` と `go vet` | 通る（実機のビルドでも同じ） |
| G3 | `go test -race -tags smoke` | データ競合なし（`Input` の専用スレッド、`Capturer` / `WindowManager` の列挙） |

## 4. 既知の制約（M1）

- キャプチャは GDI `BitBlt` のみ。DXGI（Desktop Duplication）と継続キャプチャ（`Watch`）は M2。排他フルスクリーンのゲームや DRM 保護された動画は写らないことがある。
- UIPI: 管理者権限のウィンドウへは標準権限のプロセスから入力できない。`SendInput` は UIPI に捨てられても成功を返すことがあるため、`WindowInfo.Elevated` を `service.InputGuard` が先に見て `input_blocked` にする。
- `Key` の記号キーは現在のキーボード配列に依存する（`VkKeyScanEx`）。Ctrl / Alt（AltGr）が要る文字は `invalid_argument`。文字そのものを入力したいときは `Text` を使う。
