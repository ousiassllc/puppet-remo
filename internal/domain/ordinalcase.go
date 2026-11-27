package domain

import (
	"unicode"
	"unicode/utf8"
)

// ordinalUpper は r を、Windows の大文字小文字を無視した比較（.NET の StringComparison.OrdinalIgnoreCase・NTFS の $UpCase 表）と
// 同じ向きで、1 文字を 1 文字へ大文字にする（Unicode の単純な大文字化。unicode.ToUpper）。
//
// 比べる前に両辺をこの写像でそろえる。unicode.SimpleFold が作る「大文字小文字の等価類」（strings.EqualFold）で比べると、
// 次の文字で Windows とずれる（等価類は、小文字へ寄せて同じになる文字を全部まとめてしまう）ため、使わない。
//   - ß（U+00DF）と ẞ（U+1E9E）: ẞ は大文字化してもそのまま、ß も大文字がなくそのままなので別の文字（EqualFold は同じとみなす）
//   - K（U+212A KELVIN SIGN）と k・K: KELVIN SIGN は大文字化してもそのままなので、k とも K とも別の文字（EqualFold は 3 つとも同じ）
//   - ſ（U+017F LONG S）と s・S: 下の例外で別の文字（EqualFold は同じとみなす）
//   - İ（U+0130）と i・I: 別の文字（İ は大文字化してもそのまま）。ı（U+0131）と I・i も、下の例外で別の文字
//   - Σ・ς・σ: どれも Σ になるので同じ。ǅ・ǆ・Ǆ も同じ（EqualFold と同じ結果）
//
// 例外: ASCII 以外の文字は、大文字化しても ASCII にはならない（ı U+0131 → I、ſ U+017F → S は写さず、そのまま）。
// Windows は ASCII と ASCII 以外の文字を同じものとして扱わない（OrdinalIgnoreCase は "s" と "ſ" を別とする。dotnet/runtime#32247）。
func ordinalUpper(r rune) rune {
	u := unicode.ToUpper(r)
	if r >= utf8.RuneSelf && u < utf8.RuneSelf {
		return r
	}
	return u
}

// compareOrdinalIgnoreCase は a と b を、Windows の OrdinalIgnoreCase 相当（上の ordinalUpper を 1 文字ずつ当てて比べる）で比べる。
// decided が false のとき（どちらかが不正な UTF-8。比べる文字が決まらない）は、equal の値に意味はない。呼び出し側は安全な側へ倒す。
//
// 限界: NTFS の $UpCase 表はボリュームの作成時の Unicode の版で固定され、Go の標準ライブラリの Unicode の版とは別。
// 新しい版で足された大文字小文字の対応は、古いボリュームの比較とずれうる。表そのもの（OS・ボリューム依存）は持ち込まない。
// また、NTFS の表は UTF-16 の 1 単位ずつの対応なので BMP の外（補助面。Deseret など）は畳まないが、ここでは畳む
// （Windows が別物とする綴りを同じと判定する側。拒否の取りこぼしにならない）。
func compareOrdinalIgnoreCase(a, b string) (equal, decided bool) {
	if !utf8.ValidString(a) || !utf8.ValidString(b) {
		return false, false
	}
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb && ordinalUpper(ra) != ordinalUpper(rb) {
			return false, true
		}
		a, b = a[na:], b[nb:]
	}
	return a == "" && b == "", true
}

// EqualOrdinalIgnoreCase は a と b が、Windows の OrdinalIgnoreCase 相当の比較で同じかを返す（方針は ordinalUpper・
// compareOrdinalIgnoreCase のコメント）。不正な UTF-8 を含むときは false。
func EqualOrdinalIgnoreCase(a, b string) bool {
	equal, decided := compareOrdinalIgnoreCase(a, b)
	return decided && equal
}
