//go:build !windows

package appserver

import (
	"os"
	"os/exec"
)

// prepare・adopt は Windows 専用の処理（コンソール窓の抑止・アプリと一緒に終わるジョブ）の置き場所。
// 実際の app-server は Windows だけで動かす（他の OS は unsupported）ので、ここでは何もしない（開発・テストで動けばよい）。
func prepare(*exec.Cmd) {}

func adopt(*os.Process) error { return nil }
