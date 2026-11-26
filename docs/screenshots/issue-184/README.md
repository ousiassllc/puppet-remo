# Issue #184 — 項目カードの枠の余白をガイドの設定画面パーツ 1 にそろえる

偽 platform のデモサーバーのスクリーンショット。クライアント・アプリ・接続要求・拒否リストの値はすべて架空。

## 撮り方

```sh
# 変更前は origin の develop 相当（統合ブランチ b95a405）、変更後はこのブランチで、同じ種のデモサーバーを起動して撮った。
go build -o /tmp/remotedemo ./internal/app/internal/remotedemo   # 変更前のビルドには seeds.go と main.go のフラグだけを足した
PUPPET_REMO_STATIC_DIR=<その版の static/dist> /tmp/remotedemo -task-history 110 -pending-request -gui-addr 127.0.0.1:18178
# /apps にアプリ 2 件と拒否リスト 2 件を、アプリ自身の POST /apps・POST /apps/denied（X-Puppet-Remo: 1）で登録してから、
# headless の Chromium（Playwright）で colorScheme を light / dark、1280 と 390 幅で fullPage を撮る。
```

## ファイル名

`<画面>-<before|after>-<幅>-<light|dark>.png`。画面は `clients`（ClientCard）・`apps`（AppCard・拒否リストの行）・`requests`（接続要求）。

## 実測（Playwright の computed style・1280 幅・ライト）

| 要素 | 変更前の padding | 変更後の padding | 見出し付きカードの本文（ガイド パーツ 1 = `--pad-card`） |
|---|---|---|---|
| ClientCard（`[data-client]`） | 16px | 24px | 24px |
| AppCard（`[data-app]`） | 16px | 24px | 24px |
| 接続要求（`[data-request]`） | 16px | 24px | 24px |
| 拒否リストの行（`[data-denied]`） | 16px | 24px | 24px |

枠は変更前後とも 1px `--border`・角丸 12px（`--r-lg`）。390 幅は 4 画面とも横スクロールなし。変更前の拒否リストの行は、390 幅で値の `<code>` が 1 文字ずつ縦に折れていた（`after` では `basis-40` で値が行を替えて出る）。
