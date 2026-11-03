package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// 起動許可アプリ（M1.5。FR-M18・M19・H12、NFR-S20）。外部 I/O を持たない値と検証だけ。

const (
	// MaxLaunchableApps は起動許可リストの登録数の上限（NFR-C06）。
	MaxLaunchableApps = 50

	maxAppLabelChars = 64
	maxAppArgs       = 32
	maxAppArgsChars  = 8192

	// maxPathUnits は ExePath・WorkingDir の長さの上限（UTF-16 のコードユニット数）。Windows の上限 32,767 は
	// 終端の NUL を含むので、文字列は 32,766 ユニットまで。
	maxPathUnits = 32766
)

// ValidateAppInput の戻り値のキー（項目名）。service.AppRegistry の変更項目（`changed`）と同じ名前。
const (
	AppFieldLabel      = "label"
	AppFieldExePath    = "exe_path"
	AppFieldArgs       = "args"
	AppFieldWorkingDir = "working_dir"
	// AppFieldDeniedValue・AppFieldLaunchMode は拒否リストの入力と、方式の切り替えの項目名。
	AppFieldDeniedValue = "value"
	AppFieldLaunchMode  = "mode"
)

// LaunchableApp は起動許可リストの 1 件（launchable_apps 表の 1 行）。
type LaunchableApp struct {
	ID         string // ULID。MCP の app_id
	Label      string
	ExePath    string
	Args       []string // 登録時に固定
	WorkingDir string   // 空なら実行ファイルのあるディレクトリ
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Corrupt は保存済みの行が読めない（args_json が文字列の配列でない。手で書き換えられた）ことを表す。DB には無い。
	// このとき Args は空で、起動は CheckLaunchTarget が拒否する。ホスト所有者は一覧で見て削除・上書きできる。
	Corrupt bool
}

// AppSummary は MCP に出せる許可アプリの情報。パス・引数・作業ディレクトリは型に無い（漏れを型で防ぐ）。
type AppSummary struct {
	ID    string
	Label string
}

// Summary は a から MCP に出せる項目だけを取り出す。
func (a LaunchableApp) Summary() AppSummary {
	return AppSummary{ID: a.ID, Label: a.Label}
}

// AppInput はホスト GUI のフォームの入力。ID と時刻は service が付ける。
type AppInput struct {
	Label      string
	ExePath    string
	Args       []string
	WorkingDir string
}

// ValidateAppInput は in の形を検証する（純粋。ファイルの存在は見ない）。戻り値は項目名 → メッセージで、空なら有効。
// メッセージに入力値（パス・引数）は含めない。
//
//   - Label: 前後の空白を除いて 1〜64 文字
//   - ExePath: 絶対パスのローカルの .exe（isAbsoluteLocalPath の規則）。32,766 文字（UTF-16）以内
//   - Args: 32 個以内、空文字・NUL を含まず、合計 8,192 文字以内
//   - WorkingDir: 空、または絶対パスのローカルのパス（ExePath と同じ形の規則）。32,766 文字（UTF-16）以内
func ValidateAppInput(in AppInput) map[string]string {
	errs := map[string]string{}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Label)); n < 1 || n > maxAppLabelChars {
		errs[AppFieldLabel] = "表示名は 1〜64 文字で入力してください"
	}
	for field, msg := range launchFormErrors(in.ExePath, in.Args, in.WorkingDir) {
		errs[field] = msg
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// CheckLaunchTarget は起動のたびの再検査（NFR-S20 (3)）。ValidateAppInput と同じ形の規則（実行ファイル・引数・
// 作業ディレクトリ）を保存済みの行 a に当て、満たさなければ ErrAppNotFound を返す（fail closed）。
// 読めない行（Corrupt）も満たさない。表示名は起動の安全に関わらないので見ない。
func CheckLaunchTarget(a LaunchableApp) error {
	if a.Corrupt || len(launchFormErrors(a.ExePath, a.Args, a.WorkingDir)) > 0 {
		return ErrAppNotFound
	}
	return nil
}

// launchFormErrors は ValidateAppInput と CheckLaunchTarget が共有する、起動に関わる 3 項目の検査。
func launchFormErrors(exePath string, args []string, workingDir string) map[string]string {
	errs := map[string]string{}
	if !isAbsoluteLocalPath(exePath) || !hasExeSuffix(exePath) {
		errs[AppFieldExePath] = "実行ファイルは、ドライブ文字で始まる絶対パスの .exe を指定してください（UNC・デバイスパス・相対パス・\"..\"・ドライブ指定以外の「:」・「<>\"|?*」・制御文字・予約デバイス名（CON など）・32,766 文字を超えるパスは不可）"
	}
	if msg := checkArgs(args); msg != "" {
		errs[AppFieldArgs] = msg
	}
	if workingDir != "" && !isAbsoluteLocalPath(workingDir) {
		errs[AppFieldWorkingDir] = "作業ディレクトリは空か、ドライブ文字で始まる絶対パスを指定してください（UNC・デバイスパス・相対パス・\"..\"・ドライブ指定以外の「:」・「<>\"|?*」・制御文字・予約デバイス名（CON など）・32,766 文字を超えるパスは不可）"
	}
	return errs
}

// hasExeSuffix は p の末尾が ".exe"（大文字小文字は区別しない）で、拡張子の前にファイル名があるかを返す。
func hasExeSuffix(p string) bool {
	const ext = ".exe"
	if len(p) <= len(ext) || !strings.EqualFold(p[len(p)-len(ext):], ext) {
		return false
	}
	last := p[len(p)-len(ext)-1]
	return last != '\\' && last != '/'
}

// isAbsoluteLocalPath は p が、ドライブ文字と区切り（`C:\` または `C:/`）で始まるローカルの絶対パスかを返す。
// 次のパスは偽（純粋な文字列の検査で、実行 OS には依らない）:
//   - UNC（`\\host\...`）・デバイスパス（`\\?\`・`\\.\`）・相対パス
//   - 32,766 文字（UTF-16 のコードユニット。終端の NUL を含めて 32,767 に収める）を超えるパス
//   - ドライブ指定（2 文字目）以外の ":"（代替データストリーム `a.exe:x`・`a.exe::$DATA`）、`<>"|?*`、
//     C0 制御文字（NUL を含む）と DEL
//   - ".." の要素（末尾の空白・ドットを除くと空になる要素。Windows はそれらを取り除くため "..." や ".. " を含む）
//   - 予約デバイス名（CON・PRN・AUX・NUL・COM1〜9・LPT1〜9、上付き数字の COM¹²³・LPT¹²³、コンソールの CONIN$・CONOUT$）の要素。
//     大文字小文字を区別せず、末尾のドット・空白を落とした後の、最初の "." より前（"con.txt"・"NUL .x"・"CONOUT$.exe" も該当）で判定する。
//     ディレクトリの要素にも効く
func isAbsoluteLocalPath(p string) bool {
	if len(p) < 3 || !isASCIILetter(p[0]) || p[1] != ':' || !isSeparator(p[2]) {
		return false
	}
	if UTF16Len(p) > maxPathUnits {
		return false
	}
	for _, r := range p[2:] {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`:<>"|?*`, r) {
			return false
		}
	}
	for _, elem := range strings.FieldsFunc(p[3:], func(r rune) bool { return r == '\\' || r == '/' }) {
		elem = strings.TrimRight(elem, ". ")
		if elem == "" || isReservedDeviceName(elem) {
			return false
		}
	}
	return true
}

// isReservedDeviceName は elem（末尾のドット・空白を落とした 1 要素）の、最初の "." より前が予約デバイス名かを返す。
func isReservedDeviceName(elem string) bool {
	base, _, _ := strings.Cut(elem, ".")
	base = strings.TrimRight(base, " ")
	// CONIN$・CONOUT$ はコンソールの入力・出力バッファを指すデバイス名で、CON・NUL などと同じく、拡張子が付いても
	// （"CONOUT$.exe"）予約名として扱う。Windows 実機での挙動は未確認で、迷うときは拒否する側に倒す（fail closed）。
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	// COM1〜9・LPT1〜9 と、上付き数字の COM¹²³・LPT¹²³（Windows はこれらも予約名として扱う）。
	if len(base) > 3 {
		prefix := strings.ToUpper(base[:3])
		suffix := base[3:]
		if (prefix == "COM" || prefix == "LPT") && utf8.RuneCountInString(suffix) == 1 && strings.Contains("123456789¹²³", suffix) {
			return true
		}
	}
	return false
}

// UTF16Len は s を UTF-16 にしたときのコードユニット数を返す（割り当てなし）。パスとコマンドラインの上限（Windows は
// UTF-16 のコードユニットで数える）の検査に、domain と platform が共通で使う。
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isSeparator(c byte) bool { return c == '\\' || c == '/' }

// checkArgs は引数の数・長さ・内容を検査し、違反があればメッセージを返す。
func checkArgs(args []string) string {
	if len(args) > maxAppArgs {
		return "引数は 32 個までです"
	}
	total := 0
	for _, a := range args {
		if a == "" {
			return "空の引数は登録できません"
		}
		if strings.ContainsRune(a, 0) {
			return "引数に NUL 文字は使えません"
		}
		total += utf8.RuneCountInString(a)
	}
	if total > maxAppArgsChars {
		return "引数は合計 8,192 文字までです"
	}
	return ""
}

// ParseArgsLines はフォームの引数欄（1 行 1 引数）を分割する。改行は LF・CRLF のどちらでもよい。
// 空行は捨てる（空文字の引数は登録できない）。空白だけの行は引数として残す。
// 戻り値は常に非 nil（JSON にしたとき `[]` になる）。
func ParseArgsLines(s string) []string {
	args := []string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			args = append(args, line)
		}
	}
	return args
}

// LaunchFailure は launch_failed の理由（API 仕様 §2。FR-AU06）。
type LaunchFailure string

const (
	LaunchFileMissing       LaunchFailure = "file_missing"
	LaunchAccessDenied      LaunchFailure = "access_denied"
	LaunchElevationRequired LaunchFailure = "elevation_required"
	LaunchOther             LaunchFailure = "other"
)

// Valid は f が API 仕様 §2 の reason かを返す。
func (f LaunchFailure) Valid() bool {
	switch f {
	case LaunchFileMissing, LaunchAccessDenied, LaunchElevationRequired, LaunchOther:
		return true
	}
	return false
}

// NewLaunchFailed は reason つきの ErrLaunchFailed を返す。reason が未知の値なら LaunchOther にする
// （Detail に任意の文字列、特にパスや引数が入らないようにするため）。
func NewLaunchFailed(reason LaunchFailure) *Error {
	if !reason.Valid() {
		reason = LaunchOther
	}
	return ErrLaunchFailed.WithDetail("reason", reason)
}
