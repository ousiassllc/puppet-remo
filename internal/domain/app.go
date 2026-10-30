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
)

// ValidateAppInput の戻り値のキー（項目名）。service.AppRegistry の変更項目（`changed`）と同じ名前。
const (
	AppFieldLabel      = "label"
	AppFieldExePath    = "exe_path"
	AppFieldArgs       = "args"
	AppFieldWorkingDir = "working_dir"
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
//   - ExePath: 絶対パスのローカルの .exe（checkAbsolutePath の規則）
//   - Args: 32 個以内、空文字・NUL を含まず、合計 8,192 文字以内
//   - WorkingDir: 空、または絶対パスのローカルのパス（ExePath と同じ形の規則）
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
// 表示名は起動の安全に関わらないので見ない。
func CheckLaunchTarget(a LaunchableApp) error {
	if len(launchFormErrors(a.ExePath, a.Args, a.WorkingDir)) > 0 {
		return ErrAppNotFound
	}
	return nil
}

// launchFormErrors は ValidateAppInput と CheckLaunchTarget が共有する、起動に関わる 3 項目の検査。
func launchFormErrors(exePath string, args []string, workingDir string) map[string]string {
	errs := map[string]string{}
	if !isAbsoluteLocalPath(exePath) || !hasExeSuffix(exePath) {
		errs[AppFieldExePath] = "実行ファイルはドライブ文字で始まる絶対パスの .exe を指定してください（UNC・デバイスパス・相対パス・\"..\" は不可）"
	}
	if msg := checkArgs(args); msg != "" {
		errs[AppFieldArgs] = msg
	}
	if workingDir != "" && !isAbsoluteLocalPath(workingDir) {
		errs[AppFieldWorkingDir] = "作業ディレクトリは空か、ドライブ文字で始まる絶対パスを指定してください（UNC・デバイスパス・相対パス・\"..\" は不可）"
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
// UNC（`\\host\...`）・デバイスパス（`\\?\`・`\\.\`）・相対パスは偽。制御文字（NUL を含む）を含むパスと、
// ".." の要素（末尾の空白・ドットを除くと空になる要素。Windows はそれらを取り除くため "..." や ".. " を含む）を
// 含むパスも偽。純粋な文字列の検査で、実行 OS には依らない。
func isAbsoluteLocalPath(p string) bool {
	if len(p) < 3 || !isASCIILetter(p[0]) || p[1] != ':' || !isSeparator(p[2]) {
		return false
	}
	for i := range len(p) {
		if p[i] < 0x20 {
			return false
		}
	}
	for _, elem := range strings.FieldsFunc(p[3:], func(r rune) bool { return r == '\\' || r == '/' }) {
		if strings.TrimRight(elem, ". ") == "" {
			return false
		}
	}
	return true
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
