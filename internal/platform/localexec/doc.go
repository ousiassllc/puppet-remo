// Package localexec は platform.CommandRunner の実装（許可リストの項目を、シェルを使わず argv で直接起動する。#265）。
//
// 起動・タイムアウト・出力の上限・環境の絞り込みは標準ライブラリ（os/exec）だけで書き、OS ごとに違うのは「子孫プロセスごと終了させる」
// 部分だけ（tree_windows.go はジョブオブジェクト、tree_other.go はプロセスグループ）。そのため Linux でも同じ実装を実プロセスで試せる
// （テストはテストバイナリ自身を再実行して使う）。
package localexec
