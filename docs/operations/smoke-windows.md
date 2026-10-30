# Windows 実機スモーク手順（M1: platform・ホットキー・UI Automation・Wails シェル）

CI（`windows-latest`）には対話デスクトップがないため、GDI キャプチャ・`SendInput`・ウィンドウ列挙・クリップボード・UI Automation・グローバルホットキー・Wails のウィンドウ／トレイは CI では動かない（[コンポーネント設計](../components/overview.md) §10、NFR-O04）。本書は M1 の実装を**実機で**確かめる唯一の手順書で、§1〜§3 は `internal/platform/windows`（FR-M01〜M14）、§4 は Wails のシェルと結線（FR-H01・FR-H06・FR-S08、S11）、§5 は既知の制約。DXGI・オーバーレイは M2 以降の手順で確認する。

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

### 3.5 UI Automation（`get_ui_tree`。FR-M14）

契約テスト（§2 の `TestSmokeUIAutomation`）は、前面にメモ帳など 3 ノード以上のウィンドウがあることを前提にする。

| # | 確認項目 | 期待 |
|---|---------|------|
| U1 | `go test -tags smoke -run TestSmokeUIAutomation -v ./internal/platform/windows/`（メモ帳を前面に） | `PASS`。`SKIP` / `FAIL` にならない |
| U2 | メモ帳・エクスプローラー・ブラウザの `Tree`（深さ 5・500 ノード） | 役割・名前・外接矩形（物理座標）が得られる。ノード数・深さの上限で打ち切られ、打ち切りが分かる |
| U3 | パスワード入力欄を含む画面（ブラウザのログインフォーム） | パスワード欄の `Value` が空（値を読まない） |
| U4 | UIA を呼んでいる最中に対象アプリが固まっている | 呼び出しがタイムアウトして戻る（`get_ui_tree` が 30 秒で打ち切られる。アプリ全体が固まらない） |
| U5 | 管理者権限のウィンドウが前面 | 失敗が `input_blocked` ではなく読み取りの失敗として返る（ツリーを返せない場合は原因つきのエラー） |

### 3.6 グローバルホットキー（FR-H06・FR-S02）

| # | 確認項目 | 期待 |
|---|---------|------|
| K1 | `go test -tags smoke -run TestSmokeHotkey -v ./internal/platform/windows/` | `PASS`（契約テストが登録・重複・解除を確認する） |
| K2 | アプリ起動後に既定の `Ctrl+Alt+Shift+K` を押す（§4.5） | キルスイッチが発動する |
| K3 | 他のアプリが同じキーを登録済みの状態でアプリを起動 | アプリは起動し、ログ（`%LOCALAPPDATA%\puppet-remo\logs\`）にホットキー登録の失敗が残る。ダッシュボード・トレイからは発動できる |
| K4 | 設定画面でホットキーを別のキーに変更して保存 | 新しいキーで発動し、古いキーでは発動しない。登録できないキーを保存したときは、古い登録が残る |

### 3.7 全体
 
 | # | 確認項目 | 期待 |
 |---|---------|------|

| # | 確認項目 | 期待 |
|---|---------|------|
| G1 | プロセスの DPI 設定 | アプリのマニフェストで Per-Monitor DPI Aware V2 を宣言する（[アーキテクチャ](../architecture/overview.md)）。`NewCapturer` / `NewInput` / `NewWindowManager` は、未設定のときだけ `EnableDPIAwareness` で設定する。マニフェストなしの `go test` でも物理ピクセルで動く |
| G2 | `GOOS=windows go build ./...` と `go vet` | 通る（実機のビルドでも同じ） |
| G3 | `go test -race -tags smoke` | データ競合なし（`Input` の専用スレッド、`Capturer` / `WindowManager` の列挙） |

## 4. Wails のシェルと結線（S11。実機のみ）

`task windows:build`（または Windows で `go build -tags production -ldflags "-H windowsgui" ./cmd/puppet-remo`）で作った `puppet-remo.exe` を、**tailscaled が動いていて tailnet にログイン済み**の対話デスクトップで実行する。WebView2 ランタイムが要る（インストーラが導入する）。通信相手になる tailnet 上の別端末（MCP クライアント）を 1 台用意する。

### 4.1 ウィンドウ・トレイ・単一インスタンス（FR-H01）

| # | 確認項目 | 期待 |
|---|---------|------|
| S1 | 起動 | メインウィンドウにダッシュボードが表示され、トレイにアイコンが出る。リッスン用のソケットは GUI のために開かない（`netstat -ano` に GUI 用のポートがない。開くのは tailnet IP の設定ポートだけ） |
| S2 | ウィンドウの閉じるボタン | ウィンドウが隠れるだけでプロセスは終了しない（タスクマネージャーで確認）。トレイから開き直せる |
| S3 | トレイメニュー | 「ダッシュボードを開く」「キルスイッチを発動」「終了」がある。接続要求が保留中のときだけ「接続要求を確認」が出て、押すと該当画面が開く。発動中は「キルスイッチ」の項目が押せない |
| S4 | トレイアイコンのクリック | ダッシュボードが前面に出る |
| S5 | トレイの「終了」 | プロセスが終わり、tailnet IP のポートが閉じ、トレイのアイコンが消える。MCP セッションが切れる |
| S6 | 2 つ目を起動 | 2 つ目は終了し、1 つ目のウィンドウが前面に出る（最小化・非表示からも復帰）。`puppet-remo.exe` のプロセスが 1 つのまま |
| S7 | 管理者として起動した 2 つ目 | 同様に単一インスタンスになる（確認結果を残す） |

### 4.2 ログオン時の自動起動（FR-S08）

| # | 確認項目 | 期待 |
|---|---------|------|
| A1 | 設定画面で自動起動を有効にして保存 | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` に `puppet-remo` の値ができる。値は `"<exe のパス>" --background` |
| A2 | サインアウト → サインイン | アプリが起動し、ウィンドウは開かずトレイだけに常駐する（`--background`）。トレイから開ける |
| A3 | 設定画面で無効にして保存 | Run キーの値が消える。設定画面を開き直すと無効と表示される |
| A4 | exe を別の場所へ移した後の設定画面 | 古いパスを指す登録は「無効」と表示され、有効にし直すと上書きされる |
| A5 | アンインストール | データ（`%LOCALAPPDATA%\puppet-remo`）は残る。自動起動を有効にしたまま消すと Run キーが残るため、先に無効にする（インストーラは Run キーを触らない） |

### 4.3 WebView2 での表示（フロントエンド設計 §11）

| # | 確認項目 | 期待 |
|---|---------|------|
| W1 | ダッシュボード・接続要求・クライアント・設定の各画面 | 表示され、操作できる。DevTools（本番ビルドでは無効）が使える開発ビルドのコンソールに **CSP 違反が出ない**。Wails が HTML にインラインのスクリプト・スタイルを注入しない。`/wails/runtime.js` が同一オリジンで読み込まれる |
| W2 | `wails3 dev`（`task dev`） | `FRONTEND_DEVSERVER_URL` の待ち受け確認（esbuild の serve）を通過し、GUI が表示される。CSP の `'self'` が開発サーバーのオリジンで効く |
| W3 | **GUI の自動更新（`GET /poll`）** | Wails v3.0.0 の Windows の AssetServer は応答をストリーミングしない（`responsewriter_windows.go` は本文を `Finish` まで溜める）ため、GUI は SSE（`/events`）ではなく 3 秒ごとのポーリング（`GET /poll`）で更新する。ダッシュボードを開いたまま、tailnet 側のセッション・接続要求・キルスイッチの変化が約 3 秒以内に、再読み込みなしでウィンドウ内に反映される（セッション・リース・サーバー状態・ヘッダーの状態バッジ・接続要求のバッジと一覧・キルスイッチのパネル）。接続要求のページで許可フォームに入力している間、ポーリングで入力が消えない |
| W4 | トークンのクリップボードコピー | 発行直後の「コピー」ボタンでクリップボードにトークンが入る（WebView2 の `navigator.clipboard` が使える）。コピー後、他のアプリへ貼り付けて確認する |
| W5 | ウィンドウのキャプチャ除外 | メイン GUI は `ContentProtectionEnabled`（`WDA_EXCLUDEFROMCAPTURE`）で、トークン発行画面を出した状態で MCP の `screenshot` を呼んでも、ウィンドウの内容が写らない。また `Own` ウィンドウとして `InputGuard` が入力を拒否する（§3.3 W5） |

### 4.4 リモートルーターと tailnet（FR-A01〜A02、NFR-S01・A03）

| # | 確認項目 | 期待 |
|---|---------|------|
| R1 | 起動後の `netstat -ano \| findstr 8765` | tailnet の IPv4（`100.x`）・IPv6（`fd7a:`）だけで LISTENING。`127.0.0.1`・`0.0.0.0`・`::` では待ち受けない |
| R2 | tailnet 上の別端末から `curl http://<tailnet-ip>:8765/mcp` | 未登録なら 403（接続要求が作られ、トレイ・ダッシュボードに出る）。`/viewer`・`/api/v1` は 404（M1 にない） |
| R3 | 接続要求を許可 → トークン発行 → MCP クライアントで接続 | `initialize` と `tools/list` が通り、ダッシュボードの「接続中セッション」に出る。切断すると消える |
| R4 | tailscaled を停止・再起動 | 待ち受けが止まり（理由がダッシュボードに出る）、再起動後 10 秒以内に復旧する |
| R5 | 設定画面でポートを変更して保存 | 新しいポートで待ち受け直し、古いポートは閉じる |
| R6 | リースのアイドル時間を設定画面で変更 | 保持中のリースにも新しい値が適用される |

### 4.5 キルスイッチ（FR-H06）

| # | 確認項目 | 期待 |
|---|---------|------|
| X1 | MCP クライアントで操作リースを取った状態で `Ctrl+Alt+Shift+K` | 1 秒以内にリースが解放され、MCP セッションが切断され、以降の要求が 503 `kill_switch_active`。ダッシュボードに発動中と出る |
| X2 | トレイメニューの「キルスイッチ」・ダッシュボードのボタン | 同じ結果 |
| X3 | 発動中にアプリを再起動 | 発動状態のまま起動する（解除はダッシュボードの確認操作だけ） |
| X4 | 発動中に未登録デバイスからリクエスト | 503 で、接続要求は作られない |

### 4.6 ビルドとインストーラ

| # | 確認項目 | 期待 |
|---|---------|------|
| P1 | `task windows:build`（Linux からのクロスビルドでも可） | `bin/puppet-remo.exe`（GUI サブシステム）ができる。エクスプローラーのプロパティにバージョン情報があり、マニフェストが Per-Monitor DPI Aware V2 |
| P2 | `task package`（`makensis` が必要） | `bin/puppet-remo-amd64-installer.exe` ができる。インストール → スタートメニューにショートカット → 起動できる。WebView2 ランタイムが無い環境で導入される。署名はしない（初回に SmartScreen の警告が出る。NFR-O07） |

## 5. 既知の制約（M1）

- キャプチャは GDI `BitBlt` のみ。DXGI（Desktop Duplication）と継続キャプチャ（`Watch`）は M2。排他フルスクリーンのゲームや DRM 保護された動画は写らないことがある。
- UIPI: 管理者権限のウィンドウへは標準権限のプロセスから入力できない。`SendInput` は UIPI に捨てられても成功を返すことがあるため、`WindowInfo.Elevated` を `service.InputGuard` が先に見て `input_blocked` にする。
- `Key` の記号キーは現在のキーボード配列に依存する（`VkKeyScanEx`）。Ctrl / Alt（AltGr）が要る文字は `invalid_argument`。文字そのものを入力したいときは `Text` を使う。
- **GUI の自動更新は SSE ではなくポーリング**: Wails v3.0.0 の Windows の AssetServer は応答を本文の完了までバッファするため、`text/event-stream` の逐次配信が WebView 内では届かない（`internal/assetserver/webview/responsewriter_windows.go`）。ホスト GUI のレイアウトは SSE（`/events`）を購読せず、`GET /poll`（3 秒ごと）の OOB で更新する（frontend.md §5.3）。画面が隠れている間もポーリングは続く。
