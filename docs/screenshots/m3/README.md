# M3 ホスト GUI のスクリーンショット

`go run ./internal/router/internal/guidemo`（`task frontend:build` のあと。`-addr 127.0.0.1:8899`）が、偽の service（`guitest`）に架空のデータを入れて
ホスト GUI を loopback に配信する。headless Chrome（幅 1280 px）で撮影した。実際のトークン・ホスト名・個人情報は含まない
（クライアント名・ホスト名・ユーザーはすべて架空で、サムネイルは架空の画面（グラデーションと矩形）を JPEG（長辺 320 px・品質 60）にしたもの）。

| ファイル | 内容 |
|---------|------|
| `audit-list.png` | 監査ログの一覧（`/audit`。#72）。絞り込みフォーム、`ts` 降順の表（結果バッジ・所要時間・サムネイルの有無）、「次へ」 |
| `audit-detail.png` | 監査ログの詳細（`/audit/60`。#72）。項目・引数のキー・値の表・サムネイルの `<img>` |
| `audit-detail-escaped.png` | 引数に HTML の断片（`<script>…`）があっても、エスケープされて文字としてそのまま表示される |
| `audit-invalid.png` | 不正な日時（`from=yesterday`）の絞り込み。項目の下にメッセージが出て、「該当なし」とは言わない（422） |
