# Issue #188 — 製品名を Nefos Remote に変える（ホスト GUI と Web ビューアの、変更前と変更後）

**偽 platform のデモサーバーのスクリーンショットで、Windows 実機の代わりにならない。** ウィンドウのタイトル・トレイ・インストーラーは Windows 実機でしか見られず、ここには無い（未確認。[docs/operations/smoke-windows.md](../../operations/smoke-windows.md) §4.7 の P2〜P4）。
クライアント名・ホスト名・トークンはすべて架空。

## 撮り方

```sh
# 変更前は develop（a308155）、変更後はこのブランチで、同じデモサーバーを起動して撮った。
go build -o /tmp/remotedemo ./internal/app/internal/remotedemo
# ホスト GUI は 127.0.0.1:8899、Web ビューアは 127.0.0.1:8765（Host ヘッダーは偽の tailnet IP 100.64.0.1）。
# headless の Chromium（Playwright）で、colorScheme を light / dark にして 1280x800 で撮る。
```

## ファイル名

`<画面>-<before|after>-<light|dark>.png`。画面は `gui-dashboard`（ホスト GUI のダッシュボード）・`gui-update`（更新）・`gui-licenses`（ライセンス表記）・`web-viewer`（Web ビューア）。

変わるのは、ページのタイトル（`<title>` の「… - 製品名」）、ホスト GUI の左上の「製品名 ホスト」、Web ビューアのヘッダーの製品名だけ。ロゴ・アイコン・配色・配置は変えない。
