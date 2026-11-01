package app

import _ "embed"

// TrayIcon はトレイのアイコン（64×64 の PNG。build/appicon-small.svg から `task common:generate:icons` で作る）。
//
//go:embed tray.png
var TrayIcon []byte
