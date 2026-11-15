# macOS 実機の成立性プローブ（Issue #186）

puppet-remo（今は Windows 専用）を Mac のホストでも動かせるかを、本実装の前に Mac 実機で確かめた記録。製品コード（`internal/`）と仕様書（`docs/requirements`）には手を入れていない。プローブは `cmd/macprobe`（Go + cgo、`//go:build darwin`）。

**この版の結論: 項目 1〜4 は「未確認」、項目 5 は許可なしの側だけ確認済み。** 原因は、実機の画面収録・アクセシビリティの許可がまだ付いていないことと、画面がロック中であること（どちらも回避していない）。許可を付けたあとの再実行の手順は「5. 再実行の手順」にある。許可が付いたら、この文書の「結果」欄を実測値で更新する。

## 1. 測定環境

| 項目 | 値 |
| --- | --- |
| 機種 | Mac18,5（Apple M6、Apple Silicon・arm64） |
| macOS | 27.0.1（ビルド 26A434） |
| ディスプレイ | 1 枚、1920×1080、倍率 1（Retina ではない）。`NSScreen.backingScaleFactor` = 1、`CGDisplayPixelsWide/High` = 1920/1080 |
| ツール | Xcode 27.2、Go 1.27.1（darwin/arm64）。署名は ad-hoc のみ（コード署名用の証明書は 0 件） |
| 画面の状態 | **ロック中**（`CGSSessionScreenIsLocked` = true）、コンソールに yuhta がログイン済み。解除していない |
| 許可の状態 | 画面収録 = なし、アクセシビリティ = なし、入力監視 = なし（`CGPreflight*`・`AXIsProcessTrusted` で読み取り） |
| 起動方法 | ssh で入り、`MacProbe.app`（ad-hoc 署名の最小の .app。ID `com.ousiass.puppet-remo.macprobe`）を `open` で起動。素のバイナリを ssh から直接動かした場合も同じ結果だった |
| プローブの版 | `macprobe version` = 1（`result.json` の `probe_version`） |

ssh 経由でも、プローブは WindowServer に接続して窓を作れた（ロック中でも窓は作れるが、画面には出ない: `occlusion_visible` = false）。

## 2. 結果の要約

| 項目 | 判定 | 要点 |
| --- | --- | --- |
| 1. ScreenCaptureKit の撮影・継続キャプチャ・dirtyRects | **未確認** | 画面収録の許可がなく `SCShareableContent` が `-3801`（TCC 拒否）で失敗。コードは用意済み |
| 2. CGEventPost のクリック・キー・Unicode | **未確認** | アクセシビリティ未許可・画面ロック中のため、自プロセス宛ての入力が 1 件も届かなかった（許可のせいかロックのせいかは切り分けられない） |
| 3. オーバーレイの映り込み（NFR-S06） | **未確認** | 撮影できないので、映る・映らないを言えない。7 通りの撮り方を用意済み |
| 4. 許可の付け方・聞き直し・署名変更 | **一部確認** | 署名の指紋（cdhash）が作り直しで変わることは確認。許可が外れるかは、許可を付けてから確かめる |
| 5. `kCGWindowName` と画面収録の許可 | **許可なしの側は確認済み** | 許可なしでは、ほかのプロセスの通常の窓の名前は取れない。許可ありは未確認 |

## 3. 項目ごとの結果

### 3.1 ScreenCaptureKit の撮影（項目 1）

- **結果**: 未確認。
- **証拠**: `SCShareableContent.getShareableContent…` が `SCStreamErrorDomain` コード `-3801`（「ユーザがアプリケーション、ウインドウ、ディスプレイ取り込みのTCCを拒否しました」）で失敗する。`CGPreflightScreenCaptureAccess()` = false。`macprobe run capture`・`run overlay` は、この時点で結果にエラーを書いて止まる。
- **用意したもの**（`run capture`）:
  - ディスプレイ一覧（`SCDisplay.frame`・`width/height`（ポイント）・`pointPixelScale`・`contentRect`）。撮影サイズは「ポイント × `pointPixelScale`」で指定し、実際の画像のピクセル数と突き合わせる（Retina の検証用）。
  - 全ディスプレイの 1 枚撮り（`SCScreenshotManager.captureImage`）。複数ディスプレイは全部撮る。
  - 継続キャプチャ（`SCStream`）3 区間: ① 画面の一部を毎フレーム塗り替えながら（`display_all_animating`）、② 塗り替えを止めて（`display_all_static`）、③ 塗り替える窓だけを合成して（`display_including_anim_window_only`）。各区間で、フレーム数・状態別の数（Complete/Idle/Blank）・実測 fps・`SCStreamFrameInfoDirtyRects` を持つフレームの数・dirty 矩形の和集合・合計面積を記録する。Windows の `Frame.Dirty` 相当が取れるかは、③の dirty 和集合が塗り替えた窓の矩形（`anim_rect_px`）に収まるかで判定する。
- **未確認（機材）**: Retina・複数ディスプレイ。この機材は 1 枚・倍率 1 のため、実機では確かめられない。必要なら Retina ディスプレイまたは 2 枚目のディスプレイがつながった Mac が要る。
- **許可を付けるのに必要なこと**: 「4.2 許可の付け方」の画面収録。

### 3.2 CGEventPost の入力（項目 2）

- **結果**: 未確認（切り分け不能な結果が 1 つ）。
- **証拠**（アクセシビリティ未許可、ロック中。`macprobe run input`）:
  - プローブ自身の窓（`macprobe-input`。キー・クリックを記録するだけの NSView）に向けて、`CGEventPostToPid(自分の PID)` でクリック・`a`・`日本語`（Unicode イベント）・絵文字（サロゲートペア）を送った → **届いた入力は 0 件**。ただし窓は key window にならず（`key` = false、`app_active` = false、`occlusion_visible` = false）、ロック中だったため、許可が無かったことが原因とは言えない。
  - 入力の「監視」（listen-only の `CGEventTap`）は、入力監視の許可なしでは作れなかった（`listen_only_tap_created` = false、`CGPreflightListenEventAccess()` = false）。送信（post）側は別の許可なので、**送信に入力監視が要るかは未確認**（アクセシビリティだけ付けた状態で `run input` を再実行して確かめる）。
- **やらなかったこと（安全）**: HID タップ（`CGEventPost(kCGHIDEventTap)`）は 1 回も送っていない。画面ロック中に送ると、ロック画面のパスワード欄に届くおそれがあるため、プローブは「ロックされている」「プローブの窓が key window でない」「`--allow-global-input` が無い」のどれかに当てはまれば送らないようにしてある（クリックのときはカーソルを元の位置に戻す）。
- **用意したもの**: `run input` は、自プロセス宛て（`CGEventPostToPid`）と HID タップ（`--allow-global-input` で明示したときだけ）の 2 通りで、クリック・キー（keycode 0）・Unicode（`日本語`・`😀`）を送り、プローブの窓が受け取った内容を `result.json` に書く。日本語は Unicode イベントとして直接送るため IME は通らない。IME を使う実アプリへの入力の挙動は、このプローブでは測れない（本実装での別の確認が要る）。
- **許可を付けるのに必要なこと**: アクセシビリティ（「4.2」）。入力監視は、監視の tap が要るかの確認にだけ要る（送信の確認には要らない見込みだが未確認）。

### 3.3 オーバーレイの映り込み（項目 3、NFR-S06）

- **結果**: 未確認。スクリーンショットは撮れていない（画面収録の許可がないため）。`/home/ousiass/0_B3-puppet186-shots/` に PNG は無い。
- **前提（公開情報。実機では未確認）**:
  - Apple のドキュメントは `NSWindowSharingNone` を「macOS が使わない旧い定数」とし、「内容をキャプチャから隠す目的で使わない」ように案内している（[NSWindow.SharingType.none](https://developer.apple.com/documentation/appkit/nswindow/sharingtype-swift.enum/none)）。
  - Apple Developer Forums に「macOS 15.4+ で `kCGWindowSharingStateSharingNone` の窓が ScreenCaptureKit に撮られる」という題のスレッドがある（[thread 792152](https://developer.apple.com/forums/thread/792152)。本文は確認できていない）。
  - したがって、`sharingType = none` だけで NFR-S06 を守る設計は避け、ScreenCaptureKit の `SCContentFilter`（`excludingWindows`）で「自分のキャプチャから自分の窓を外す」のを本命として確かめる。
- **用意したもの**（`run overlay`）: 画面全体の透明なオーバーレイ窓（最前面・クリック透過。洋紅の札を 1 枚だけ描く。製品の「縁の枠と札」の代用）と、対照の通常窓（緑）を出し、次の 7 通りで撮る。洋紅の札の矩形に洋紅がどれだけ写ったかを、PNG の画素から Go 側で判定する（`visible`・`hidden`・`partial`・`indeterminate`）。対照の緑が写っていなければ `indeterminate`（ロック画面に隠れた場合など、「札が無い」を根拠にしない）。

| 撮り方 | 何を確かめるか |
| --- | --- |
| v1 ディスプレイ全体（`sharingType` = ReadOnly） | 普通に撮ると映るか |
| v2 ディスプレイ全体（`sharingType` = None） | `NSWindowSharingNone` が ScreenCaptureKit に効くか（Apple の案内どおり効かないか） |
| v3 ディスプレイ全体からオーバーレイ窓を `SCContentFilter` で除外 | 自分のキャプチャから自分の窓を外せるか（本命） |
| v4 対照窓とオーバーレイだけを合成（ReadOnly） | オーバーレイが撮れる状態にあることの対照 |
| v5 v4 と同じ（`sharingType` = None） | 部分合成でも `None` が効かないか |
| v6 `/usr/sbin/screencapture -x`（OS 標準のスクリーンショット） | 利用者が OS のスクショを撮ると映るか |
| v7 v6 と同じ（`sharingType` = None） | OS のスクショに `None` が効くか |

- **許可を付けるのに必要なこと**: 画面収録（「4.2」）。さらに、**画面がロック解除されている**こと（ロック中は、通常の窓がロック画面の下に隠れるので、v1・v2・v6・v7 は対照が写らず `indeterminate` になる見込み。v3〜v5 は窓を指定して合成するので、ロック中でも測れる可能性があるが、未確認）。
- **守れなかった場合の代替案**（悠太さんが選ぶ。結果が出たら、この中から絞れる）:

| 案 | 内容 | 長所 | 短所 |
| --- | --- | --- | --- |
| A | `SCContentFilter(excludingWindows:)` で自分のキャプチャからオーバーレイ窓を外す。NFR-S06 は「puppet-remo のキャプチャ経路に写らない」に言い換える | puppet-remo が撮る画像（AI・ライブ配信）には写さないことを、自分で保証できる（v3 で確認する） | ホストの人が OS のスクショ・他の録画ソフトで撮ると映る。NFR-S06 の文言を変える必要がある（別 Issue） |
| B | Mac ではオーバーレイを出さない（操作中の表示は、メニューバーのアイコン・通知などの別の方法に寄せる） | 映り込みの問題が起きない | FR-H07（画面全体に枠と札）の体験が Mac では無くなる |
| C | Mac では映り込みを保証せず、ドキュメントに「映る場合がある」と書く | 実装が軽い | 画像にオーバーレイが写り、AI の判断を乱すおそれ（要件の趣旨に反する） |
| D | `sharingType = none` に頼る | 実装が最も軽い | Apple が非推奨。macOS 15.4 以降は効かないという報告がある。採らない |

### 3.4 許可の付け方・聞き直し・署名の変更（項目 4）

#### 4.1 確認済みのこと

- **署名の指紋**: ad-hoc 署名の `MacProbe.app` の designated requirement は `cdhash H"…"`（コードの指紋そのもの）。同じ場所・同じ ID のまま中身だけ変えて作り直すと cdhash が変わった（`build_app.sh` の `BUILD_TAG` を変えて確認。v1: `327a33b3…` → v2: `db5ed39d…`）。ad-hoc 署名の許可は cdhash に結び付くので、**作り直すたびに許可が外れる見込み**。ただし、許可が外れるかの実測は、許可を付けてからでないとできないので未確認。
- **許可の要求**: `macprobe run request` は `CGRequestScreenCaptureAccess()` と `AXIsProcessTrustedWithOptions(prompt)` を呼ぶ（OS の確認ダイアログを出す標準の方法）。いずれも false を返した。ダイアログが出たかは、画面ロック中で目視できていないので未確認。

#### 4.2 許可の付け方（悠太さんが Mac の前でやること。要る許可は回避していない）

「システム設定 > プライバシーとセキュリティ」で、次の 2 つをオンにする。

1. **画面収録**（macOS 15 以降の名称は「画面とシステムオーディオの録音」。macOS 27 での名称は未確認）: `/Users/yuhta/omp-work/puppet-186/MacProbe.app`（`MacProbe`）をオンにする。一覧に出ていなければ、`+` で上の .app を追加する。オンにしたあと、アプリの再起動を求められることがある。
2. **アクセシビリティ**: 同じ `/Users/yuhta/omp-work/puppet-186/MacProbe.app` をオンにする。
3. （任意）**入力監視**: 入力の「監視」の tap（`listen_only_tap_created`）の確認にだけ要る。送信の確認には要らない見込み。

また、**画面のロックを解除した状態で**再実行する必要がある（ロック解除・パスワード入力はプローブでは行わない）。

#### 4.3 macOS 15 以降の聞き直し（未確認）

- 公開情報（[9to5Mac, 2024-08](https://9to5mac.com/2024/08/14/macos-sequoia-screen-recording-prompt-monthly/)、[Eternal Storms, 2024-09](https://blog.eternalstorms.at/2024/09/23/psa-no-more-macos-15-sequoia-monthly-screen-recording-permission-reminders)）では、macOS 15 は画面収録を使うアプリに、約 1 か月ごとに「継続して許可するか」を聞き直す。承認の記録は `~/Library/Group Containers/group.com.apple.replayd/ScreenCaptureApprovals.plist` にある、とされている。
- この Mac（macOS 27.0.1）で同じ聞き直しが出るか、周期はいくつかは、**実機では確認していない**（許可を付けてから 1 か月ほど経たないと見えない）。
- 影響（推測）: 人が席にいないホストでは、聞き直しのダイアログが誰にも押されず、撮影が止まるおそれがある。代替案: ホストの人に定期的に押してもらう運用にする／聞き直しを減らす Apple の枠（永続的なコンテンツ取り込みの entitlement。Apple の承認が要る可能性があり、未確認）を調べる／Mac は「人が席にいるとき限定」として仕様に書く。

#### 4.4 署名を変えたときに許可が外れるか（未確認）

許可を付けたあと、`build_app.sh <出力先> v3`（`BUILD_TAG` だけ変えて同じ場所に作り直し）→ `macprobe run status` を実行し、`screen_capture_preflight` と `accessibility_trusted` が true のままかを見る。外れた場合は、配布の署名（本実装の範囲外）を決めるときの根拠になる。コード署名の証明書はこの Mac に無く、ad-hoc 以外との比較（署名者の切り替え）は未確認。

### 3.5 `kCGWindowName` と画面収録の許可（項目 5）

- **結果（許可なし）**: ほかのプロセスの通常の窓の名前は取れない。自プロセスの窓の名前は取れる。窓の所有者名（`kCGWindowOwnerName`）はほぼ取れる。許可ありは未確認。
- **証拠**（画面収録なし。`macprobe run windows`。プローブが別プロセスのテスト窓（タイトル `macprobe-fixture-title`）を 1 枚出して照合する。他アプリの窓のタイトルは記録せず、数だけを数えた）:

| 数えたもの | 件数 |
| --- | --- |
| 窓の総数（`CGWindowListCopyWindowInfo`、全窓） | 47 |
| 自プロセスの窓で `kCGWindowName` が取れた | 1 / 1 |
| 別プロセスのテスト窓（fixture）で `kCGWindowName` が取れた | **0 / 1** |
| その他のプロセスの窓 | 45 |
| その他のうち `kCGWindowOwnerName` が取れた | 44 |
| その他のうち layer 0（通常の窓）で `kCGWindowName` が取れた | **0 / 27** |
| その他のうち `kCGWindowName` が取れた（すべて layer 0 以外の OS の窓） | 2 |
| `SCShareableContent` の窓一覧 | 取得不可（`-3801`） |

- **読み取り**: 画面収録の許可がなければ、ほかのアプリの窓のタイトルは取れず（macOS の既知の挙動と一致）、ScreenCaptureKit の窓一覧も使えない。「許可なしでウィンドウ一覧のタイトルを使う」設計は成り立たない。所有者名とウィンドウ番号・PID・座標は許可なしでも取れる見込みだが、座標などの取得は本プローブでは数えていないので、必要なら追加で測る。
- **未確認**: 許可ありのとき、`kCGWindowName` と `SCWindow.title` が全窓（別プロセスのテスト窓を含む）で取れるか。画面収録をオンにしてから `macprobe run windows` を再実行すると、`fixture_name_matches` と `scshareablecontent.fixture_title_matches` が 1 になるかで分かる。

## 4. 守れない・確かめられない項目への対応のまとめ

| 項目 | いま言えること | 悠太さんの判断に要るもの |
| --- | --- | --- |
| 1・2・3 | 許可と画面のロック解除が無いので、測れていない | 許可を付けてもらい（4.2）、ロックを解除した状態で再実行する。オーバーレイは 3.3 の案 A〜D から選ぶ |
| 4 | 署名は ad-hoc のみ。許可は cdhash に結び付き、作り直しで外れる見込み | 配布の署名・公証（範囲外）を別 Issue で決める。聞き直しは 4.3 の代替案から選ぶ |
| 5 | 許可なしでは他アプリの窓タイトルは取れない | 窓の名前を使う機能は、画面収録の許可を前提にする |

## 5. 再実行の手順

Mac 上の作業場所は `~/omp-work/puppet-186/` だけ（ソースは `src/` に scp で置いた。Mac に git の認証は入れていない）。

```sh
# 1. .app を作る（Go が要る。ad-hoc 署名）。BUILD_TAG は任意の印
cd ~/omp-work/puppet-186/src && ./cmd/macprobe/build_app.sh ~/omp-work/puppet-186 v1

# 2. 許可の確認ダイアログを出す（任意。一覧に出てこないときに使う）
open -W -n -a ~/omp-work/puppet-186/MacProbe.app --stdout ~/omp-work/puppet-186/out-request.txt --args run request --out ~/omp-work/puppet-186/out-request

# 3. システム設定で許可を付ける（4.2）。画面のロックを解除しておく

# 4. 測る（all は status・windows・capture・input・overlay）。結果は <out>/result.json・report.txt・PNG
open -W -n -a ~/omp-work/puppet-186/MacProbe.app --stdout ~/omp-work/puppet-186/out-all/stdout.txt --args run all --out ~/omp-work/puppet-186/out-all

# 5. HID タップへの入力も測る場合（ロック解除済みで、Mac を触らない間に）
open -W -n -a ~/omp-work/puppet-186/MacProbe.app --args run input --allow-global-input --out ~/omp-work/puppet-186/out-input
```

`macprobe run overlay` は、各撮り方の PNG と、洋紅の札・対照の緑の画素の割合（`marker_magenta_share`・`control_green_share`）・判定を `result.json` に書く。PNG には、ほかのアプリの画面が写り込むおそれがあるので、リポジトリにはコミットしない。

## 6. この調査で Mac に加えた変更

- `~/omp-work/puppet-186/` を作成: `src/`（ソース）、`MacProbe.app`、`macprobe`（素のバイナリ）、`out-*/`（実行結果）。`~/omp-work/` の外には何も作っていない。
- システム設定・TCC・署名証明書・ログイン項目・電源設定は変更していない。sudo は使っていない。Mac に追加でインストールしたものは無い（既存の Xcode・Go を使った）。
- 許可の確認ダイアログを要求した（`run request`）ため、画面のロック解除後に「`MacProbe` が画面を録画しようとしています」「アクセシビリティの許可」のダイアログが出ている可能性がある。
