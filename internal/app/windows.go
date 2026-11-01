package app

import "github.com/ousiassllc/puppet-remo/internal/domain"

// このファイルは、Wails のウィンドウの生成・破棄の窓口（Windows）。承認ダイアログを開閉する購読側は approval_dialogs.go、
// 操作中オーバーレイを出し入れする購読側は overlay.go。
// Wails に触れる実装は wails_windows.go（実機でしか確かめられない）。ここは Wails に依存せず、偽の Windows（FakeWindows）でテストできる
// （docs/components/overview.md §7・§10。NFR-O03/O04）。

// Window は開いているウィンドウ 1 つ。
type Window interface {
	// Close はウィンドウを閉じる。すでに閉じている（ユーザーが閉じた・破棄済み）ときは何もしない。
	Close()
}

// Windows はウィンドウの生成・破棄（Wails v3 のラッパ）。
type Windows interface {
	// Main はホスト GUI のメインウィンドウ（閉じてもトレイに残る。Shell が使う）。
	Main() MainWindow
	// OpenApproval は承認要求 id の承認ダイアログ（専用のウィンドウ。URL は /approvals/{id}）を開いて返す。
	// ウィンドウはキャプチャ除外（ContentProtectionEnabled。NFR-S06）で作り、閉じるのは approvalDialogs（承認の終了・シャットダウン）。
	OpenApproval(id string) Window
	// ShowOverlay は操作中オーバーレイ（#69）を monitors の全モニターに出す。クリックスルー・キャプチャ除外・フォーカスを奪わない
	// ウィンドウを 1 モニターに 1 枚、モニターの物理座標の原点・大きさに合わせて作る。label は枠に添える短い文言。
	// すでに出ていれば、モニター構成が同じなら label だけを更新し、違えば作り直す（出している間のモニター構成の変化は、
	// 次の ShowOverlay で反映する）。monitors が空なら何もしない。失敗してもリースの取得は妨げない（実装がログに残す）。
	ShowOverlay(monitors []domain.Monitor, label string)
	// HideOverlay は出しているオーバーレイをすべて閉じる。出していなければ何もしない。
	HideOverlay()
}
