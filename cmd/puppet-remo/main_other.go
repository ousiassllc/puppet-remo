//go:build !windows

// puppet-remo は Windows 専用のアプリ。Windows 以外では何もせず、非ゼロで終了する
// （`go build ./...` と `go vet ./...` を Linux / macOS の CI でも通すためのスタブ）。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "puppet-remo: Windows only")
	os.Exit(1)
}
