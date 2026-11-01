# Issue #45: アプリのログをホスト GUI で見られるようにする

`task gui:demo`（`internal/router/internal/guidemo`）が GUI ルーターを偽の service で起動し、headless Chrome で撮影した（幅 1280）。
ログはデモ用の架空のデータ（成功の INFO・注意の WARN・失敗の ERROR が混ざった 60 件。1 ページ 50 件を超えるので、ページ送りが出る）。
ホスト名・IP・トークンの ID は架空で、トークンの平文・ハッシュは入れていない。

| ファイル | 内容 |
|---------|------|
| `logs-all.png` | サイドバーの「ログ」（`/logs`）。日時・レベル・内容（主な属性つき）を新しい順に出す。成功（INFO。リースの取得・トークンの発行・接続要求の許可・ツールの成功）と、WARN（認証の失敗・キルスイッチの発動・ツールの拒否）、ERROR（ツールの失敗・待ち受けの停止）が並ぶ。見出しの下にログファイルの場所と、レベルの絞り込み（すべて / WARN 以上 / ERROR のみ） |
| `logs-warn-and-above.png` | 「WARN 以上」（`?level=warn`）。INFO が消え、WARN と ERROR だけが残る |
| `logs-error-only.png` | 「ERROR のみ」（`?level=error`）。ボタンのクリックで表だけが差し替わり、URL も更新される |
| `logs-page2.png` | 「すべて」の 2 ページ目（`?page=2`。「次へ」のクリックで差し替わる）。残りの 10 件と「前へ」 |
