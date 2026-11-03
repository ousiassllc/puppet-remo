package domain

import (
	"strings"
	"time"
)

// 起動の方式（launch.mode）と拒否リスト（M1.5。FR-M18・M19・H12、NFR-S20）。外部 I/O を持たない値と判定だけ。

// LaunchMode は launch_app が何を起動できるかの方式。既定は LaunchModeAllowlist。
type LaunchMode string

const (
	// LaunchModeAllowlist（既定）は、起動許可リストに登録したアプリだけを app_id で起動でき、ほかはすべて拒否する。
	LaunchModeAllowlist LaunchMode = "allowlist"
	// LaunchModeDenylist は、拒否リストに当たらない固定ディスク上の .exe を path で起動できる（登録済みのアプリは app_id でも起動できる）。
	LaunchModeDenylist LaunchMode = "denylist"
)

// Valid は m が既知の方式かを返す。
func (m LaunchMode) Valid() bool { return m == LaunchModeAllowlist || m == LaunchModeDenylist }

// MaxDeniedApps は拒否リストの登録数の上限。
const MaxDeniedApps = 200

// DeniedKind は拒否リストの 1 件の照合の種類。
type DeniedKind string

const (
	// DeniedPath は実体のパス（シンボリックリンク・ジャンクション・8.3 短縮名を解決したもの）で照合する。
	DeniedPath DeniedKind = "path"
	// DeniedName は実行ファイル名（cmd.exe など）で照合する。どのフォルダーにあっても当たる。
	DeniedName DeniedKind = "name"
)

// DeniedApp は拒否リストの 1 件（launch_denylist 表の 1 行）。
type DeniedApp struct {
	ID string // ULID
	// Kind が DeniedPath のとき Value は登録時に解決した実体の絶対パス、DeniedName のとき Value は実行ファイル名（入力のまま）。
	Kind      DeniedKind
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// maxDeniedNameChars は実行ファイル名の長さの上限（NTFS の 1 要素の上限）。
const maxDeniedNameChars = 255

// DeniedKey は拒否リストの重複判定に使う、Kind と Value の正規形（大文字小文字を区別せず、パスは区切りを "\" にそろえる）。
func DeniedKey(kind DeniedKind, value string) string {
	if kind == DeniedPath {
		value = NormalizePathForMatch(value)
	}
	return string(kind) + ":" + strings.ToLower(value)
}

// NormalizePathForMatch は "/" を "\" にし、連続する区切りを 1 つにする（Windows は同じ場所として扱う）。
// 大文字小文字は畳まない（比較側が EqualFold を使う）。
func NormalizePathForMatch(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	prevSep := false
	for i := range len(p) {
		c := p[i]
		if c == '/' {
			c = '\\'
		}
		if c == '\\' && prevSep {
			continue
		}
		prevSep = c == '\\'
		b.WriteByte(c)
	}
	return b.String()
}

// ClassifyDenied は拒否リストの入力 value（前後の空白は除いてある前提）を、パス（区切りか「:」を含む）か実行ファイル名に
// 分け、形を検査する。msg は不備の説明（空なら有効）。メッセージに入力値は含めない。
//
//   - パス: 絶対パスのローカルの .exe（ValidateLaunchPath と同じ規則）
//   - 名前: 区切りを含まない .exe で終わるファイル名（予約デバイス名・使えない文字・制御文字は不可。255 文字以内）
func ClassifyDenied(value string) (kind DeniedKind, msg string) {
	if strings.ContainsAny(value, `\/:`) {
		if !ValidateLaunchPath(value) {
			return DeniedPath, "パスは、ドライブ文字で始まる絶対パスの .exe を指定してください（UNC・デバイスパス・相対パス・\"..\"・使えない文字・予約デバイス名は不可）。実行ファイル名だけなら、フォルダーを付けずに入力してください"
		}
		return DeniedPath, ""
	}
	if UTF16Len(value) > maxDeniedNameChars || !hasExeSuffix(value) || !isAbsoluteLocalPath(`C:\`+value) {
		return DeniedName, "実行ファイル名は、フォルダーを含まない .exe の名前（例: cmd.exe）を指定してください（使えない文字・予約デバイス名は不可）"
	}
	return DeniedName, ""
}

// ValidateLaunchPath は launch_app の path（拒否リスト方式の起動）が、絶対パスのローカルの .exe の形かを返す
// （純粋な文字列の検査。固定ディスク・実在の確認は platform.AppLauncher.Resolve が行う）。
func ValidateLaunchPath(p string) bool { return isAbsoluteLocalPath(p) && hasExeSuffix(p) }

// ExeBaseName は p（"\" か "/" 区切り）の最後の要素を返す。
func ExeBaseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// MatchDenied は、起動しようとする実行ファイルが拒否リストの entries のどれかに当たるかを返す。
// requested は呼び出し側が指定したパス、resolved は platform.AppLauncher.Resolve が返した実体のパス
// （シンボリックリンク・ジャンクション・8.3 短縮名を解決済み）。
//
//   - DeniedPath: 実体のパス（resolved）と一致（大文字小文字を区別しない）。登録の綴りと違っても、解決した実体が同じなら当たる
//   - DeniedName: resolved の末尾の名前と一致（大文字小文字を区別しない）。指定の綴りの末尾（requested）にも当たる
//     （解決の前後どちらかの名前が一致すれば拒否する。fail closed）
func MatchDenied(entries []DeniedApp, requested, resolved string) bool {
	resolvedNorm := NormalizePathForMatch(resolved)
	resolvedName, requestedName := ExeBaseName(resolved), ExeBaseName(requested)
	for _, e := range entries {
		switch e.Kind {
		case DeniedPath:
			if strings.EqualFold(NormalizePathForMatch(e.Value), resolvedNorm) {
				return true
			}
		case DeniedName:
			if strings.EqualFold(e.Value, resolvedName) || strings.EqualFold(e.Value, requestedName) {
				return true
			}
		}
	}
	return false
}
