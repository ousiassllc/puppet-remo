package main

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), c)
	return img
}

// PrintWindow が TRUE を返しても何も描かなかった画像（DIB が sentinel のまま）を、黒一色・内容ありと取り違えない。
func TestVerdictDistinguishesUntouchedBlackUniformContent(t *testing.T) {
	cases := []struct {
		name string
		img  *image.RGBA
		want string
	}{
		{"触られていない", solid(40, 40, sentinel), verdictUntouched},
		{"全面黒", solid(40, 40, color.RGBA{A: 255}), verdictAllBlack},
		{"全面ひと色", solid(40, 40, color.RGBA{R: 10, G: 20, B: 30, A: 255}), verdictUniform},
		{"模様", func() *image.RGBA {
			img := solid(40, 40, color.RGBA{R: 200, G: 40, B: 40, A: 255})
			fill(img, image.Rect(0, 0, 20, 40), color.RGBA{R: 40, G: 160, B: 40, A: 255})
			return img
		}(), verdictContent},
	}
	for _, c := range cases {
		if got := analyzePixels(c.img, nil).verdict(); got != c.want {
			t.Errorf("%s: verdict = %s, want %s", c.name, got, c.want)
		}
	}
}

// マゼンタ（interferer の色）が 1 画素でもあれば数える。模様の色はマゼンタに数えない。
func TestAnalyzePixelsCountsMagentaAndWatchedColors(t *testing.T) {
	img := solid(30, 30, color.RGBA{R: 200, G: 40, B: 40, A: 255})
	fill(img, image.Rect(0, 0, 3, 1), magenta)
	s := analyzePixels(img, []color.RGBA{{R: 200, G: 40, B: 40, A: 255}})
	if s.Magenta != 3 {
		t.Errorf("Magenta = %d, want 3", s.Magenta)
	}
	if got := s.Watched[colorKey(color.RGBA{R: 200, G: 40, B: 40})]; got != 900-3 {
		t.Errorf("watched = %d, want 897", got)
	}
}

func TestSameShare(t *testing.T) {
	a := solid(10, 10, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	b := solid(10, 10, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if got := sameShare(a, b); got != 1 {
		t.Errorf("同じ画像: %v", got)
	}
	fill(b, image.Rect(0, 0, 10, 1), color.RGBA{R: 9, A: 255})
	if got := sameShare(a, b); got != 0.9 {
		t.Errorf("1 行違う: %v, want 0.9", got)
	}
	if got := sameShare(a, solid(5, 5, color.RGBA{})); got != 0 {
		t.Errorf("大きさ違い: %v, want 0", got)
	}
}

func TestProtocolRoundTrip(t *testing.T) {
	line := formatMessage("EVT", "save-closed", "saved", "1", "name", "a b.a2p")
	m, err := parseMessage(line)
	if err != nil {
		t.Fatal(err)
	}
	if m.Verb != "EVT" || m.Name != "save-closed" || m.Fields["saved"] != "1" || m.Fields["name"] != "a_b.a2p" {
		t.Errorf("parse = %+v（空白は '_' に置き換わる）", m)
	}
	r, err := parseMessage(formatMessage("READY", "", "pid", "42", "t", "1001"))
	if err != nil || r.Verb != "READY" || r.Name != "" || r.Fields["pid"] != "42" {
		t.Errorf("READY = %+v %v", r, err)
	}
	for _, bad := range []string{"", "HELLO x=1", "EVT a =b"} {
		if _, err := parseMessage(bad); err == nil {
			t.Errorf("%q はエラーになるべき", bad)
		}
	}
}

func TestReportTextKeepsOnlyRecordedFacts(t *testing.T) {
	r := newReport()
	r.Env["os_version"] = "10.0.1"
	r.Add("M1.sys", "メニューがメンバー", StatusPass, "ok")
	r.Add("M3.sys", "写る", StatusFail, "verdict=%s", "all_black")
	r.Add("M7", "時間", StatusInfo, "")
	r.Add("M1.foreign", "別プロセス", StatusSkip, "作れない")
	r.Note("メモ")
	txt := r.Text()
	for _, want := range []string{"PASS=1 FAIL=1 INFO=1 SKIP=1", "verdict=all_black", "os_version = 10.0.1", "S1〜S3 の最終合格ではない"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report.txt に %q がない:\n%s", want, txt)
		}
	}
	if !r.Failed() {
		t.Error("FAIL があれば Failed")
	}
	js, err := r.JSON()
	if err != nil || !strings.Contains(string(js), `"status": "FAIL"`) {
		t.Errorf("json: %v %s", err, js)
	}
}

// 窓・撮影・リンクの記録の型に、タイトル・件数・他アプリを表す欄がないこと（他アプリの窓を保存しない方針を型で保つ）。
// 空値は omitempty で JSON から消えるので、JSON ではなく型のフィールドを調べる。
func TestRecordedTypesHaveNoFieldForTitlesOrCounts(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[WindowFact](), reflect.TypeFor[CaptureFact](), reflect.TypeFor[LinkFact]()} {
		for i := range typ.NumField() {
			f := typ.Field(i)
			name := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			for _, banned := range []string{"title", "count", "other", "document"} {
				if strings.Contains(name, banned) {
					t.Errorf("%s.%s: タイトル・件数・他アプリを記録する欄は持たない", typ.Name(), f.Name)
				}
			}
		}
	}
}

// 撮影の安全規則: 照合済みでない PID・別プロセス（interferer・foreign）の同じクラスの窓は撮らない。
func TestCaptureAllowedOnlyVerifiedFixtureWindows(t *testing.T) {
	verified := map[uint32]string{100: "main", 200: "interferer", 300: "foreign"}
	named := map[uintptr]string{1: "t", 2: "interferer"}
	tests := []struct {
		name  string
		pid   uint32
		hwnd  uintptr
		class string
		want  bool
	}{
		{"main の名乗った窓", 100, 1, "A2ProbeTarget", true},
		{"main のメニュー（名乗っていない）", 100, 50, "#32768", true},
		{"main のダイアログ（名乗っていない）", 100, 51, "#32770", true},
		{"main の名乗っていない別クラスの窓は撮らない", 100, 52, "SomethingElse", false},
		{"interferer の名乗った窓", 200, 2, "A2ProbeInterferer", true},
		{"interferer のプロセスの #32768 は撮らない", 200, 60, "#32768", false},
		{"foreign のプロセスの #32770 は撮らない", 300, 61, "#32770", false},
		{"照合していない PID の #32768 は撮らない", 999, 70, "#32768", false},
		{"HWND が別プロセスに再利用されたら（PID が違う）名乗った HWND でも撮らない", 999, 1, "A2ProbeTarget", false},
	}
	for _, tt := range tests {
		if _, got := captureAllowed(verified, named, tt.pid, tt.hwnd, tt.class); got != tt.want {
			t.Errorf("%s: allowed = %t, want %t", tt.name, got, tt.want)
		}
	}
}

// マゼンタの許容差の境界（±16 は数え、±17 は数えない）。模様の色（赤など）は遠いので数えない。
func TestMagentaToleranceBoundary(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 255 - magentaTolerance, G: magentaTolerance, B: 255 - magentaTolerance, A: 255})
	img.SetRGBA(1, 0, color.RGBA{R: 255 - magentaTolerance - 1, G: 0, B: 255, A: 255})
	img.SetRGBA(2, 0, color.RGBA{R: 200, G: 40, B: 40, A: 255})
	if got := analyzePixels(img, nil).Magenta; got != 1 {
		t.Errorf("Magenta = %d, want 1（許容差ちょうどだけ数える）", got)
	}
}

// result.json は、fixture だけの部分測定であってゲートの最終合格ではないことを機械可読に持つ（checks の PASS をゲートの合格と読ませない）。
func TestResultJSONNeverClaimsTheFinalGate(t *testing.T) {
	r := newReport()
	r.Add("M1", "x", StatusPass, "ok")
	js, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		GateFinal *bool  `json:"gate_final"`
		GateNote  string `json:"gate_note"`
	}
	if err := json.Unmarshal(js, &got); err != nil {
		t.Fatal(err)
	}
	if got.GateFinal == nil || *got.GateFinal || !strings.Contains(got.GateNote, "最終合格ではない") {
		t.Errorf("gate_final/gate_note = %v / %q, want an explicit false and the note", got.GateFinal, got.GateNote)
	}
}

// ファイルのエラーの文字列に、ユーザー名を含みうるパスを入れない。
func TestErrTextDropsThePath(t *testing.T) {
	err := &fs.PathError{Op: "open", Path: `C:\Users\someone\AppData\Local\Temp\a2probe-out\01.png`, Err: errors.New("access denied")}
	if got := errText(err); got != "open: access denied" || strings.Contains(got, "someone") {
		t.Errorf("errText = %q", got)
	}
	if got := errText(errors.New("plain")); got != "plain" {
		t.Errorf("errText(plain) = %q", got)
	}
}
