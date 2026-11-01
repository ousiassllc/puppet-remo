# Issue #43: サーバー状態のカードから長い値がはみ出す

`task gui:demo`（`internal/router/internal/guidemo`）が、IPv4 と完全形の長い IPv6 の tailnet IP、`[IPv6]:ポート` の待ち受け、
ラベルが 63 文字ある長い MagicDNS 名（いずれも架空の値）で GUI ルーターを起動し、headless Chrome で撮影した。

| ファイル | 幅 | 内容 |
|---------|----|------|
| `before-wide.png` | 1280×760 | 修正前。値がカードの右端を越えて、隣のカードにはみ出す |
| `before-narrow.png` | 420×800 | 修正前。値がカードを越え、ページ全体が横に広がって右が切れる |
| `after-wide.png` | 1280×760 | 修正後。tailnet IP・MagicDNS 名・待ち受けがカード内で折り返される |
| `after-narrow.png` | 420×800 | 修正後。ラベルと値を縦に積み、ページは横に広がらない |

修正前後でブラウザ上でも計測した（カードの `scrollWidth <= clientWidth`、カード内の全要素の右端がカードの右端以内）。修正前は 1280 幅で
`scrollWidth` 639 > `clientWidth` 494 で 8 要素がはみ出し、修正後は 360・420・640・900・1024・1280 幅のすべてではみ出しが 0 件。
