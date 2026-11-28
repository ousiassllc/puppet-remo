# Issue #246: ページタイトル（h1）40px / 600 → 22px / 600

`task gui:demo` と同じデモサーバー（`internal/router/internal/guidemo`。偽の service・架空のデータ）を、変更前（`b95a405`）と変更後でそれぞれビルドして配信し、
Chromium（Playwright）で撮った。Light / Dark は `prefers-color-scheme`、幅は 1280 と 390。命名は `{before|after}-{画面}-{light|dark}-{1280|390}.png`。

画面: `dashboard`（`/`）・`apps`（起動許可アプリ `/apps`）・`audit-detail`（監査ログの詳細 `/audit/60`）・`client-detail`（クライアント詳細 `/clients/client-001`。h1 はクライアント名）。

`getComputedStyle(h1)` の実測: before は 40px / 600、after は 22px / 600（最初の版は 28px / 700 で、下の調整で下げた）。色は Light が `rgb(5, 5, 5)`、Dark が `rgb(255, 255, 255)`（`--ink`）。

## 追加の調整（PR #248）: 28px / 700 → 22px / 600

ページタイトルを 22px / 600・行高 1.35 に下げたので、`after-*` を撮り直した（`before-*` は変更前 `b95a405` のまま）。同じデモサーバー・同じ 4 画面・Light / Dark・幅 1280 / 390。
`getComputedStyle(h1)` の実測: 22px / 600 / 行高 29.7px。色は Light が `rgb(5, 5, 5)`、Dark が `rgb(255, 255, 255)`（`--ink`）。
