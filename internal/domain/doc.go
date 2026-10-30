// Package domain は puppet-remo の純粋なモデルとルールを持つ。
//
// 状態を持たない値と純粋関数、および外部 I/O をしない状態機械だけを置く。
// 時刻と ID は Clock / IDGen を注入して決定的にテストできるようにする。
//
// 不変条件: domain は他の内部パッケージを import しない
// （scripts/check-imports.sh が CI で検査する）。
package domain
