package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// このファイルはアプリの版の解釈と比較（FR-H09、NFR-S10。docs/operations/release.md「版の決め方」）。純粋関数だけで、I/O を持たない。
//
// 版の書式（main.version）:
//   - 正式版: X.Y.Z（各 0〜65535。NSIS の VIProductVersion の制約）
//   - ナイトリー: X.Y.Z-nightly.YYYYMMDD+<sha 7〜40 桁>（X.Y.Z は「次に出る正式版」。日付は commit の日付 UTC）
//   - dev: ソースからのビルド。更新の対象外

// DevVersion は main.version の既定値（ソースからのビルド）。更新の確認・適用の対象外。
const DevVersion = "dev"

// Channel は更新の追い方（設定 update.channel。FR-S09）。
type Channel string

const (
	// ChannelStable は正式版（main）だけを追う。既定。
	ChannelStable Channel = "stable"
	// ChannelNightly はナイトリー（develop のプレリリース）を追う。
	ChannelNightly Channel = "nightly"
)

// Valid は c が既知のチャネルかを返す。
func (c Channel) Valid() bool { return c == ChannelStable || c == ChannelNightly }

// ErrInvalidVersion は版の文字列が書式に合わないときのエラー（errors.Is で判定する。domain.ErrInvalidArgument としても扱える）。
var ErrInvalidVersion = fmt.Errorf("%w: malformed version", ErrInvalidArgument)

// maxVersionPart は各数字の上限（Windows のファイル版は 16 ビット）。
const maxVersionPart = 65535

// Version は解釈した版。比較は CompareVersions を使う（== は SHA も比べるので、同じ日の別 commit が等しくならない）。
type Version struct {
	Major, Minor, Patch int
	// Nightly は X.Y.Z-nightly.YYYYMMDD+<sha> の形のとき true。
	Nightly bool
	// Date は Nightly のときの YYYYMMDD。
	Date string
	// SHA は Nightly のときの commit の短い sha（小文字の 16 進。7〜40 桁）。順序を持たない。
	SHA string
}

var (
	stableVersionRe  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	nightlyVersionRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-nightly\.([0-9]{8})\+([0-9a-f]{7,40})$`)
	// installerAssetRe は Release に添付するインストーラの名前（scripts/ci/next-version.sh の asset）。
	installerAssetRe = regexp.MustCompile(`^puppet-remo-(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-nightly\.([0-9]{8})\.([0-9a-f]{7,40}))?-amd64-installer\.exe$`)
)

// ParseVersion は main.version の文字列を解釈する。dev・空・書式違い・範囲外の数字・存在しない日付は ErrInvalidVersion。
func ParseVersion(s string) (Version, error) {
	if m := stableVersionRe.FindStringSubmatch(s); m != nil {
		return newVersion(m[1], m[2], m[3], "", "")
	}
	if m := nightlyVersionRe.FindStringSubmatch(s); m != nil {
		return newVersion(m[1], m[2], m[3], m[4], m[5])
	}
	return Version{}, ErrInvalidVersion
}

// ParseInstallerAsset は Release のインストーラのファイル名から版を解釈する（Release のタグにはナイトリーの X.Y.Z がないので、
// 名前が版の持ち主）。書式に合わない名前は ErrInvalidVersion。
func ParseInstallerAsset(name string) (Version, error) {
	m := installerAssetRe.FindStringSubmatch(name)
	if m == nil {
		return Version{}, ErrInvalidVersion
	}
	return newVersion(m[1], m[2], m[3], m[4], m[5])
}

// newVersion は正規表現で切り出した文字列から Version を作り、範囲（数字 0〜65535・実在する日付）を検査する。
// date・sha が空なら正式版。
func newVersion(major, minor, patch, date, sha string) (Version, error) {
	v := Version{Nightly: date != "" || sha != "", Date: date, SHA: sha}
	for _, p := range []struct {
		dst *int
		src string
	}{{&v.Major, major}, {&v.Minor, minor}, {&v.Patch, patch}} {
		n, err := strconv.Atoi(p.src)
		if err != nil || n > maxVersionPart {
			return Version{}, ErrInvalidVersion
		}
		*p.dst = n
	}
	if v.Nightly {
		if _, err := time.Parse("20060102", date); err != nil {
			return Version{}, ErrInvalidVersion
		}
	}
	return v, nil
}

// String は main.version の書式で返す。
func (v Version) String() string {
	core := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if !v.Nightly {
		return core
	}
	return core + "-nightly." + v.Date + "+" + v.SHA
}

// ReleaseTag はこの版の Release のタグ（正式版は v<X.Y.Z>、ナイトリーは nightly-YYYYMMDD-<sha>）。
func (v Version) ReleaseTag() string {
	if v.Nightly {
		return "nightly-" + v.Date + "-" + v.SHA
	}
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// InstallerAsset はこの版のインストーラのファイル名。
func (v Version) InstallerAsset() string {
	core := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Nightly {
		return "puppet-remo-" + core + "-nightly." + v.Date + "." + v.SHA + "-amd64-installer.exe"
	}
	return "puppet-remo-" + core + "-amd64-installer.exe"
}

// CompareVersions は a と b の新しさを比べる（a が新しければ正、古ければ負、同じなら 0）。
// 順序は、X.Y.Z → 同じ X.Y.Z なら正式版がナイトリーより新しい（semver の前リリース） → ナイトリー同士は日付。
// 同じ日のナイトリーは、sha が順序を持たないので同じ（0）として扱う。
func CompareVersions(a, b Version) int {
	for _, p := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if p[0] != p[1] {
			return cmpInt(p[0], p[1])
		}
	}
	switch {
	case a.Nightly && !b.Nightly:
		return -1
	case !a.Nightly && b.Nightly:
		return 1
	case a.Nightly && b.Nightly:
		return cmpString(a.Date, b.Date) // YYYYMMDD は文字列の辞書順が日付順
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// UpdateDecision は現行の版に対する候補の版の判定。
type UpdateDecision int

const (
	// UpdateAvailable は候補が現行より新しい（適用の対象）。
	UpdateAvailable UpdateDecision = iota
	// UpdateSame は現行と同じ（新版なし）。
	UpdateSame
	// UpdateDowngrade は候補が現行より古い（拒否する。NFR-S10）。
	UpdateDowngrade
	// UpdateOffChannel は候補がチャネルの対象でない（正式版チャネルへのナイトリー）。
	UpdateOffChannel
)

func (d UpdateDecision) String() string {
	switch d {
	case UpdateAvailable:
		return "available"
	case UpdateSame:
		return "same"
	case UpdateDowngrade:
		return "downgrade"
	case UpdateOffChannel:
		return "off_channel"
	}
	return "unknown"
}

// DecideUpdate は current から candidate への更新を判定する。
// 正式版チャネルはナイトリーを候補にしない。どのチャネルでも、現行より古い版はダウングレード、同じ版は同じ
// （正式版 X.Y.Z を使っていて、同じ X.Y.Z 以下のナイトリーが来ても、ナイトリーへは切り替わらない）。
func DecideUpdate(ch Channel, current, candidate Version) UpdateDecision {
	if ch != ChannelNightly && candidate.Nightly {
		return UpdateOffChannel
	}
	switch c := CompareVersions(candidate, current); {
	case c > 0:
		return UpdateAvailable
	case c < 0:
		return UpdateDowngrade
	}
	return UpdateSame
}

// ErrNotUpdatable は dev ビルド（更新の対象外）を表す。
var ErrNotUpdatable = errors.New("domain: this build is not updatable")

// ParseCurrentVersion は実行中の版（main.version）を解釈する。dev は ErrNotUpdatable、書式違いは ErrInvalidVersion。
func ParseCurrentVersion(s string) (Version, error) {
	if s == DevVersion {
		return Version{}, ErrNotUpdatable
	}
	return ParseVersion(s)
}
