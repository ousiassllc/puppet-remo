# Issue #181 — Tooltip を引き金の真上か真下に出す（変更前と変更後）

**偽 platform のデモサーバーのスクリーンショット**（実機の代わりにならない）。タスク・クライアント名・トークンはすべて架空の台本（`-agent-demo`）で、OpenAI は呼ばない。

## 撮り方

```sh
# 変更前は develop 相当（static/dist を #180・#181 の前のビルドにしたもの）、変更後はこのブランチ。同じデモサーバーのバイナリを、static/dist だけ替えて起動した。
go build -o /tmp/tip-demo ./internal/app/internal/remotedemo
# 偽のモデルの台本の 2 ステップ目に OpenAI の安全チェックを付ける（-agent-safety-check）と、タスクの詳細に番号の無い行（Tooltip の出す側「-」）ができる。
PUPPET_REMO_STATIC_DIR=<static/dist> /tmp/tip-demo -agent-demo -agent-safety-check
# run_task（remotedemo の先頭のコメントの curl）→ ホスト GUI の /approvals/{id}/allow で安全チェックを許可 → /tasks/{task_id} を開く。
# headless の Chromium（Playwright）で、「-」にホバーして Tooltip を出し、colorScheme を light / dark にして 1280x800 と 390x844 で撮った。
```

デモサーバーの待ち受けポートは 8765 固定なので、ほかのプロセスと重ならないよう、`unshare -rn`（ネットワーク名前空間）の中でサーバーとブラウザを動かした。

## ファイル名

`task-tooltip-<幅>-<before|after>-<light|dark>.png`。

- 変更前: Tooltip は出す側の左端にそろい、横に長く広がって、同じ行のアクション欄（「安全チェック … codes: …」）の文字にかぶる。
- 変更後: 出す側の真上に中央でそろい（横へずらさない）、アクション欄の文字にかぶらない。390 幅では左端で画面内に収まるよう寄り、画面の端から 16px（`margin-inline: var(--s-4)`）の余白が空く（撮り直し後の実測: 左端 16px）。上端で入りきらないときは真下に出る。

## 確認できなかった点

- 「Judge の判定」行そのものは、デモサーバーの台本（クリック → 文字入力）では作れない（台本に Judge の判定の行が出る設定を足していない）。番号の無い行の種類は `観察のみ`・`Judge の判定`・`安全チェック` の 3 つで、出す側・Tooltip の部品は同じ（`atoms.Tooltip`）なので、安全チェックの行で撮った。
