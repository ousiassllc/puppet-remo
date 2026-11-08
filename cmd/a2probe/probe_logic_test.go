package main

import (
	"image"
	"image/color"
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

// 合成は空（透明）のキャンバスに置く。メンバーの外は透明のまま（デスクトップの画素を下地にしない）、
// 前面（先頭）のメンバーが後ろのメンバーの上に来る。座標は origin + p。
func TestCompositePlacesMembersOnTransparentCanvas(t *testing.T) {
	green := color.RGBA{G: 200, A: 255}
	red := color.RGBA{R: 200, A: 255}
	// T: (100,100)-(200,200)。メニュー: T の右にはみ出す (180,120)-(260,180)。メニューが前面（先頭）。
	tw := placed{Frame: image.Rect(100, 100, 200, 200), Window: image.Rect(95, 95, 205, 205), Img: solid(110, 110, green)}
	menu := placed{Frame: image.Rect(180, 120, 260, 180), Window: image.Rect(180, 120, 260, 180), Img: solid(80, 60, red)}
	img, origin := composite([]placed{menu, tw})
	if origin != image.Pt(100, 100) {
		t.Fatalf("origin = %v, want (100,100)", origin)
	}
	if img.Bounds().Dx() != 160 || img.Bounds().Dy() != 100 {
		t.Fatalf("size = %v, want 160x100", img.Bounds().Size())
	}
	at := func(px, py int) color.RGBA { return img.RGBAAt(px-origin.X, py-origin.Y) }
	if got := at(150, 150); got != green {
		t.Errorf("T の内側 = %v", got)
	}
	if got := at(190, 150); got != red {
		t.Errorf("T とメニューが重なる点は前面のメニュー = %v", got)
	}
	if got := at(250, 150); got != red {
		t.Errorf("T の外のメニュー = %v", got)
	}
	if got := at(250, 110); got.A != 0 {
		t.Errorf("メンバーのない角は透明のまま = %v", got)
	}
}

func TestCompositeEmptyAndUnprintedMembers(t *testing.T) {
	if img, _ := composite(nil); img != nil {
		t.Error("メンバーなし: nil")
	}
	// 画像のないメンバー（撮れなかった）は外接矩形にも入らない。
	ok := placed{Frame: image.Rect(0, 0, 10, 10), Window: image.Rect(0, 0, 10, 10), Img: solid(10, 10, color.RGBA{R: 1, A: 255})}
	missing := placed{Frame: image.Rect(500, 500, 600, 600), Window: image.Rect(500, 500, 600, 600)}
	img, origin := composite([]placed{missing, ok})
	if img == nil || img.Bounds().Dx() != 10 || origin != (image.Point{}) {
		t.Errorf("撮れなかったメンバーを外接矩形に含めた: %v %v", img.Bounds(), origin)
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

// 測定結果の型に、他アプリの窓のタイトル・件数を書く欄がないこと（保存しない方針を型で保つ）。
func TestReportHasNoFieldForForeignWindowTitles(t *testing.T) {
	r := newReport()
	r.Windows = append(r.Windows, WindowFact{Role: "main"})
	js, _ := r.JSON()
	for _, banned := range []string{`"title"`, `"count"`, `"others"`} {
		if strings.Contains(string(js), banned) {
			t.Errorf("result.json に %s の欄がある", banned)
		}
	}
}
