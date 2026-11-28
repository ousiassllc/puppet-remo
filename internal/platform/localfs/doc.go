// Package localfs は platform.FileStore の実装（ホストのローカルのフォルダ 1 つの中だけでファイルを扱う。#264）。
//
// 検査（ルートの中に収まる・リンクを経由しない）は標準ライブラリだけで書き、OS ごとに違うのは 2 点だけ:
// リパースポイントの判定（reparse_windows.go・reparse_other.go）と、リンクをたどらずにファイルを開く方法（open_windows.go・open_other.go）。
// そのため Linux でも同じ実装を実ファイルシステムで試せる（本物のシンボリックリンクを作ってテストする）。
package localfs
