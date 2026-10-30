package windows

import "unicode/utf16"

// encodeClipboardText は CF_UNICODETEXT の中身（UTF-16、NUL 終端）にする。改行は変換しない（読み戻しで同じ文字列になる）。
// 不正な UTF-8 は U+FFFD になる。
func encodeClipboardText(s string) []uint16 {
	return append(utf16.Encode([]rune(s)), 0)
}

// decodeClipboardText は CF_UNICODETEXT の中身を文字列にする。最初の NUL までを読む（確保されたメモリは NUL の後ろに余白を持ちうる）。
// 対にならないサロゲートは U+FFFD になる。
func decodeClipboardText(units []uint16) string {
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
	}
	return string(utf16.Decode(units))
}
