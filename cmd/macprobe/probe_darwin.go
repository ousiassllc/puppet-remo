//go:build darwin

package main

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework CoreGraphics -framework ApplicationServices -framework ImageIO -framework Security
#include <stdlib.h>
#include "probe_darwin.h"
*/
import "C"

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"
)

// buildTag は build_app.sh が -ldflags で入れる任意の印。同じ場所・同じ ID で中身だけ変えた署名の差し替え（許可が外れるか）を試すのに使う。
var buildTag = ""

// AppKit の窓・run loop は main スレッドでだけ扱える。Go の main goroutine を OS の main スレッドに固定する。
func init() { runtime.LockOSThread() }

// callJSON は mp_* の戻り値（JSON 文字列）を解放して map に読む。
func callJSON(p *C.char) map[string]any {
	defer C.mp_free(p)
	var m map[string]any
	if err := json.Unmarshal([]byte(C.GoString(p)), &m); err != nil {
		return map[string]any{"error": "invalid json from probe: " + err.Error()}
	}
	return m
}

// OverlayVariant は overlay の撮影 1 枚の結果。画素の判定は Go 側（analyze.go）で行う。
type OverlayVariant struct {
	Name         string  `json:"name"`
	Desc         string  `json:"desc"`
	File         string  `json:"file,omitempty"`
	Error        string  `json:"error,omitempty"`
	SharingType  int     `json:"sharing_type"`
	ExitStatus   *int    `json:"exit_status,omitempty"`
	ToolOutput   string  `json:"tool_output,omitempty"`
	ImageW       int     `json:"image_w,omitempty"`
	ImageH       int     `json:"image_h,omitempty"`
	MarkerShare  float64 `json:"marker_magenta_share"`
	ControlShare float64 `json:"control_green_share"`
	BlackShare   float64 `json:"black_share"`
	Verdict      string  `json:"verdict"`
}

// analyzeOverlay は overlay の JSON の variants[] に、PNG を読んだ画素の判定を書き足す。
func analyzeOverlay(res map[string]any, outDir string) {
	raw, err := json.Marshal(res)
	if err != nil {
		return
	}
	var parsed struct {
		MarkerPx  Rect             `json:"marker_px"`
		ControlPx Rect             `json:"control_px"`
		Variants  []OverlayVariant `json:"variants"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return
	}
	for i := range parsed.Variants {
		v := &parsed.Variants[i]
		if v.File == "" {
			v.Verdict = "no_image"
			continue
		}
		f, err := os.Open(filepath.Join(outDir, v.File))
		if err != nil {
			v.Error = err.Error()
			v.Verdict = "no_image"
			continue
		}
		img, err := png.Decode(f)
		_ = f.Close()
		if err != nil {
			v.Error = "png: " + err.Error()
			v.Verdict = "no_image"
			continue
		}
		v.ImageW, v.ImageH = img.Bounds().Dx(), img.Bounds().Dy()
		v.MarkerShare = share(img, parsed.MarkerPx, isMagenta)
		v.ControlShare = share(img, parsed.ControlPx, isGreen)
		v.BlackShare = blackShare(img)
		v.Verdict = classifyOverlay(v.MarkerShare, v.ControlShare)
	}
	res["variants"] = parsed.Variants
}

// spawnFixture は別プロセスのテスト窓を起動し、READY で名乗った PID を返す。stop で終了させる。
func spawnFixture(title string) (pid int, stop func(), err error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, nil, err
	}
	cmd := exec.Command(exe, "fixture", "--title", title, "--seconds", "60")
	in, err := cmd.StdinPipe()
	if err != nil {
		return 0, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	stop = func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(out).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		var wn int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "READY %d %d", &pid, &wn); err != nil {
			stop()
			return 0, nil, fmt.Errorf("fixture が READY を返さない: %q", line)
		}
		return pid, stop, nil
	case <-time.After(15 * time.Second):
		stop()
		return 0, nil, fmt.Errorf("fixture が 15 秒以内に READY を返さない")
	}
}

func runFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	title := fs.String("title", "macprobe-fixture-title", "窓のタイトル")
	seconds := fs.Float64("seconds", 30, "最長の待ち秒数")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ct := C.CString(*title)
	defer C.free(unsafe.Pointer(ct))
	return int(C.mp_fixture(ct, C.double(*seconds)))
}

func runProbe(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	out := fs.String("out", "", "出力先")
	seconds := fs.Float64("seconds", 3, "継続キャプチャの 1 区間の秒数")
	global := fs.Bool("allow-global-input", false, "HID タップへの入力を許す（ロック中は送らない）")
	// flag は最初の非フラグで止まるので、種別を先に取る。
	which := "all"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		which, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		*out = "macprobe-out-" + time.Now().Format("20060102-150405")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cOut := C.CString(*out)
	defer C.free(unsafe.Pointer(cOut))

	want := func(name string) bool { return which == "all" || which == name }
	results := map[string]any{}
	switch which {
	case "all", "status", "request", "windows", "capture", "input", "overlay":
	default:
		fmt.Fprintf(os.Stderr, "%s\n", usage)
		return 2
	}
	if want("status") {
		results["status"] = callJSON(C.mp_status())
	}
	if which == "request" { // all には含めない（確認ダイアログが出るため）
		results["request"] = callJSON(C.mp_request())
	}
	if want("windows") {
		const title = "macprobe-fixture-title"
		pid, stop, err := spawnFixture(title)
		if err != nil {
			results["windows"] = map[string]any{"error": err.Error()}
		} else {
			ct := C.CString(title)
			results["windows"] = callJSON(C.mp_windows(C.int(pid), ct))
			C.free(unsafe.Pointer(ct))
			stop()
		}
	}
	if want("capture") {
		results["capture"] = callJSON(C.mp_capture(cOut, C.double(*seconds)))
	}
	if want("input") {
		g := 0
		if *global {
			g = 1
		}
		results["input"] = callJSON(C.mp_input(cOut, C.int(g)))
	}
	if want("overlay") {
		res := callJSON(C.mp_overlay(cOut))
		analyzeOverlay(res, *out)
		results["overlay"] = res
	}

	doc := map[string]any{"probe_version": probeVersion, "build_tag": buildTag, "finished_at": time.Now().Format(time.RFC3339), "results": results}
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "result.json"), append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	summary := summarize(results)
	_ = os.WriteFile(filepath.Join(*out, "report.txt"), []byte(summary), 0o644)
	fmt.Print(summary)
	fmt.Println("出力先:", *out)
	return 0
}

// summarize は人が読む要点だけを出す（全データは result.json）。
func summarize(results map[string]any) string {
	var sb strings.Builder
	if st, ok := results["status"].(map[string]any); ok {
		fmt.Fprintf(&sb, "status: macOS %v (%v) %v %v | 画面収録=%v アクセシビリティ=%v 入力監視=%v | ロック=%v\n",
			st["macos_version"], st["macos_build"], st["machine"], st["model"],
			st["screen_capture_preflight"], st["accessibility_trusted"], st["input_monitoring_preflight"], st["screen_locked"])
	}
	for _, k := range []string{"windows", "capture", "input"} {
		if m, ok := results[k].(map[string]any); ok {
			if e, ok := m["error"]; ok {
				fmt.Fprintf(&sb, "%s: エラー: %v\n", k, e)
			} else {
				fmt.Fprintf(&sb, "%s: 完了（詳細は result.json）\n", k)
			}
		}
	}
	if ov, ok := results["overlay"].(map[string]any); ok {
		if e, ok := ov["error"]; ok {
			fmt.Fprintf(&sb, "overlay: エラー: %v\n", e)
		}
		if vs, ok := ov["variants"].([]OverlayVariant); ok {
			for _, v := range vs {
				fmt.Fprintf(&sb, "overlay %-46s sharing=%d 札=%.2f 対照=%.2f 黒=%.2f → %s %s\n", v.Name, v.SharingType, v.MarkerShare, v.ControlShare, v.BlackShare, v.Verdict, v.Error)
			}
		}
	}
	return sb.String()
}
