package app

import _ "embed"

// TrayIcon はトレイのアイコン（64×64 の PNG。ソースは build/appicon.png）。
//
//go:embed tray.png
var TrayIcon []byte
