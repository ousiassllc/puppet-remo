// Package view は Templ に渡す表示用モデル（docs/architecture/frontend.md §3）。
//
// Templ は domain / service を import せず、このパッケージの型だけを受け取る。状態の分岐・権限の判定（どの操作を出すか）は
// ハンドラが行い、結果を bool（Can*）として渡す。テンプレートは「その bool が true なら描画する」だけで、
// 押せない操作は HTML に出さない（状態駆動レンダリング）。時刻・件数などの表示用の文字列も、ハンドラが整えて渡す。
package view
