# Issue #178 — 390 幅のタスク一覧を 1 件 1 カードの縦積みにする

偽 platform のデモサーバーのスクリーンショットで、Windows 実機の代わりにならない。タスク・クライアント名・ID はすべて架空（`internal/app/internal/remotedemo/seeds.go` の種）。

## 撮り方

```sh
(cd static && bun install --frozen-lockfile && bun run build)
go build -o /tmp/remotedemo ./internal/app/internal/remotedemo
PUPPET_REMO_STATIC_DIR=$PWD/static/dist /tmp/remotedemo -task-history 110 -gui-addr 127.0.0.1:18178
# headless の Chromium（Playwright）で、colorScheme を light / dark にして /tasks を撮る（390x844 と 1280x800）。
```

## ファイル

| ファイル | 内容 |
|---|---|
| `tasks-390-light.png` / `tasks-390-dark.png` | 390 幅。1 件 1 枚のカード（3 段）の縦積み。1 件目がすでに長い指示で、2 行で「…」になる例を兼ねる（長い指示だけの撮影は重複なので置かない） |
| `tasks-1280-light.png` / `tasks-1280-dark.png` | 1280 幅。表のまま（変えていない） |

実測（390 幅）: 画面全体・`#task-list` とも横スクロールなし（`scrollWidth` = `clientWidth`）。カードの内側の余白は 16px。キーボードの Tab は ID のリンクに止まり、フォーカスリングはカードの外周（2px・`--focus`）に出る。カードの空いた所を押すと詳細（`/tasks/{id}`）へ移る。
