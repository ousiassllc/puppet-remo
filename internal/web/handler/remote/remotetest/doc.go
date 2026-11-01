// Package remotetest は Web UI（handler/remote）のテストが使う偽の依存先（インメモリ）。本番のバイナリには含まれない。
// クリップボードの内容などは実行時にテストが渡す値だけを持ち、固定の秘密情報を持たない。
package remotetest
