// Package static はフロントエンドの成果物（static/dist）を配信する（docs/architecture/frontend.md §8）。
//
// 本番ビルド（-tags production）は go:embed した dist を、開発ビルドはソースツリーの static/dist をディスクから配信する。
// 配信は Cache-Control: no-cache + ETag（ビルド ID。dist 全体の内容のハッシュ）で、If-None-Match が一致すれば 304 を返す。
// ファイル名にハッシュを付けないため、アプリの更新後に古い JS を長期保持させない（§2）。
package static
