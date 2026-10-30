package windows

import (
	"context"
	"strings"
)

// BackgroundArg はログオン時の自動起動でアプリに渡す引数。ウィンドウを開かず、トレイだけに常駐する
// （FR-H01。ログオンのたびにダッシュボードが前面に出ないようにする）。
const BackgroundArg = "--background"

// AutostartValueName はスタートアップ登録（Run キー）の値の名前。
const AutostartValueName = "puppet-remo"

// RunKey は HKCU\Software\Microsoft\Windows\CurrentVersion\Run の値の読み書き。
// 本物は registry を使う実装（NewAutostart）、テストは偽の実装に差し替える。
type RunKey interface {
	// Get は name の値を返す。無ければ ok=false。
	Get(name string) (value string, ok bool, err error)
	Set(name, value string) error
	// Delete は name の値を消す。無くてもエラーにしない。
	Delete(name string) error
}

// Autostart はログオン時の自動起動の登録（FR-S08）。service.Autostart を満たす。
// 有効かどうかは OS の登録（Run キー）が真実の源で、DB には持たない。
type Autostart struct {
	key     RunKey
	name    string
	command string
}

// NewAutostartWithKey は key の name という値に command を登録する Autostart を返す。
func NewAutostartWithKey(key RunKey, name, command string) *Autostart {
	return &Autostart{key: key, name: name, command: command}
}

// Command は実行ファイルのパスから、Run キーに登録するコマンドラインを作る（パスは引用符で囲む）。
func Command(exePath string) string {
	return `"` + exePath + `" ` + BackgroundArg
}

// Enabled は、このアプリのコマンドが登録されているかを返す。別のパス（移動前のインストール先など）を指す
// 登録は、ログオン時にこのアプリを起動しないので「無効」として扱う（有効にし直すと上書きされる）。
func (a *Autostart) Enabled(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	v, ok, err := a.key.Get(a.name)
	if err != nil || !ok {
		return false, err
	}
	return strings.EqualFold(v, a.command), nil
}

// Set は自動起動を有効（登録）・無効（削除）にする。
func (a *Autostart) Set(ctx context.Context, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if enabled {
		return a.key.Set(a.name, a.command)
	}
	return a.key.Delete(a.name)
}
