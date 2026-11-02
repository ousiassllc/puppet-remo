//go:build e2e

// puppet-remo-e2e は Playwright の E2E（static/e2e。docs/architecture/frontend.md §9）が使うテスト専用サーバー。
//
// ビルドタグ e2e を付けたときだけコンパイルされる（`go build ./...` は無視する）ので、本番のバイナリ（cmd/puppet-remo）には入らない。
// 本物の service・ルーター・WebSocket（app.New）を、偽の platform・偽の tailnet・一時ディレクトリの SQLite で動かし、
// リモートルーターを loopback（127.0.0.1）で待ち受ける。ホスト側の操作（接続要求の許可・強制剥奪・キルスイッチ）と、偽の injector に
// 届いた入力の読み取りは、別のソケット（-control。loopback のみ）の制御エンドポイントで行う（internal/app/e2e.go）。
//
//	go run -tags e2e ./cmd/puppet-remo-e2e -port 18765 -control 127.0.0.1:18766
//
// 準備ができると、-control の GET /ready が 200 を返す。SIGINT・SIGTERM で止まり、一時ディレクトリを消す。
// 静的ファイルは static/dist（`cd static && bun run build` の成果物）を読む。場所は PUPPET_REMO_STATIC_DIR で指せる。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ousiassllc/puppet-remo/internal/app"
	"github.com/ousiassllc/puppet-remo/static"
)

func main() {
	port := flag.Int("port", 18765, "リモートルーターのポート（loopback。設定 server.port と Host 検査のポート）")
	control := flag.String("control", "127.0.0.1:18766", "制御エンドポイントの待ち受けアドレス（loopback の IP リテラルだけ）")
	flag.Parse()

	handler, err := static.Handler()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.RunE2E(ctx, app.E2EOptions{Port: *port, ControlAddr: *control, Static: handler}); err != nil {
		stop()
		log.Fatal(err)
	}
}
