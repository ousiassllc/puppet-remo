// Package licenses は、配布物に同梱する第三者のライセンス表記（THIRD_PARTY_NOTICES.txt）を持つ。
//
// 表記は static/scripts/third-party-notices.mjs が作る生成物で、アプリの「ライセンス表記」の画面（GUI の /licenses）と、
// Windows のインストーラ（build/windows/nsis/project.nsi。インストール先に置く）が同じファイルを使う。
// 更新の手順は docs/architecture/frontend.md §8.6。
package licenses

import _ "embed"

// ThirdPartyNotices はフォント・アイコン・フロントエンドのライブラリ・Go の依存のライセンスの全文。
//
//go:embed THIRD_PARTY_NOTICES.txt
var ThirdPartyNotices string
