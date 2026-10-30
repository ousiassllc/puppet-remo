package windows

import (
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// buildCommandLine は CreateProcess の lpCommandLine を組み立てる: 引用した exePath の後に、args を Windows の引数規則
// （CommandLineToArgvW の逆）で並べる。
//
// exePath は常に二重引用符で囲む（argv[0] は円記号をエスケープとして扱わず、次の二重引用符までが 1 つのトークン）。
// 引数は、空・空白（空白・タブ・改行）・二重引用符を含むときだけ引用し、二重引用符の直前の円記号は 2 倍にして
// 二重引用符を円記号でエスケープし、閉じる引用符の直前の円記号は 2 倍にする。& や % などシェルの特殊文字は
// CreateProcess がシェルを経由しないので、そのまま渡す。
//
// exePath が空・二重引用符を含む、exePath や args に NUL を含むものは、コマンドラインにできないので
// domain.ErrInvalidArgument（値はメッセージに含めない）。
func buildCommandLine(exePath string, args []string) (string, error) {
	if exePath == "" || strings.ContainsAny(exePath, "\"\x00") {
		return "", domain.ErrInvalidArgument.WithMsg("executable path cannot be put in a command line")
	}
	n := len(exePath) + 2
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return "", domain.ErrInvalidArgument.WithMsg("argument contains a NUL character")
		}
		n += len(a) + 3
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteByte('"')
	b.WriteString(exePath)
	b.WriteByte('"')
	for _, a := range args {
		b.WriteByte(' ')
		writeArg(&b, a)
	}
	return b.String(), nil
}

// writeArg は引数 1 つを、CommandLineToArgvW が元の文字列に戻す形で b に書く。
func writeArg(b *strings.Builder, s string) {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\r\"") {
		b.WriteString(s)
		return
	}
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			backslashes++
		case '"':
			writeBackslashes(b, backslashes*2+1)
			b.WriteByte('"')
			backslashes = 0
		default:
			writeBackslashes(b, backslashes)
			b.WriteByte(c)
			backslashes = 0
		}
	}
	writeBackslashes(b, backslashes*2) // 閉じる引用符を、直前の円記号がエスケープしないように
	b.WriteByte('"')
}

func writeBackslashes(b *strings.Builder, n int) {
	for ; n > 0; n-- {
		b.WriteByte('\\')
	}
}
