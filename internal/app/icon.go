package app

import _ "embed"

// トレイのアイコン（素材 Nefos Remote v5c の tray/*.ico。16・20・24・32px を 1 つにまとめた ICO。DPI 100%・125%・150%・200% に対応）。
// 素材はそのまま使い、加工しない。タスクバーの明るさで 2 つから選ぶ（選ぶのは Wails。wailsTray を参照）。
//
//go:embed tray-light-taskbar.ico
var TrayIconLightTaskbar []byte // 明るいタスクバー用。インク #050505（黒）・透過

//go:embed tray-dark-taskbar.ico
var TrayIconDarkTaskbar []byte // 暗いタスクバー用。白 #ffffff・透過
