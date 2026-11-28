package localexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

type specT = platform.CommandSpec

const generous = time.Minute

func TestRunExitCodeAndOutput(t *testing.T) {
	res, err := New().Run(context.Background(), helperSpec(generous, "echo", "hello", "oops", "3"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "hello" || string(res.Stderr) != "oops" || res.TimedOut || res.StdoutTruncated || res.StderrTruncated {
		t.Fatalf("result = %+v", res)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v", res.Duration)
	}
}

func TestRunPassesArgsVerbatim(t *testing.T) {
	// シェルを通さないので、メタ文字・空白・引用符は 1 つの引数のまま届く。
	weird := []string{"a & calc", "$(x)", "| more", `"quoted" arg`, "x;y", ">out.txt"}
	res, err := New().Run(context.Background(), helperSpec(generous, append([]string{"args"}, weird...)...))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimRight(string(res.Stdout), "\r\n"), "\n"); len(got) != len(weird) {
		t.Fatalf("args came back as %q", got)
	} else {
		for i, w := range weird {
			if strings.TrimRight(got[i], "\r") != w {
				t.Errorf("arg %d = %q, want %q", i, got[i], w)
			}
		}
	}
}

func TestRunTimeoutKillsAndReportsTimedOut(t *testing.T) {
	start := time.Now()
	res, err := New().Run(context.Background(), helperSpec(300*time.Millisecond, "sleep"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("result = %+v", res)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("took %v: the process was not killed at the timeout", took)
	}
}

func TestRunOutputCapTruncatesAndFlags(t *testing.T) {
	spec := helperSpec(generous, "flood", "20000")
	spec.MaxOutputBytes = 1000
	res, err := New().Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || len(res.Stdout) != 1000 || len(res.Stderr) != 1000 || !res.StdoutTruncated || !res.StderrTruncated {
		t.Fatalf("lens = %d/%d, truncated = %v/%v, exit = %d", len(res.Stdout), len(res.Stderr), res.StdoutTruncated, res.StderrTruncated, res.ExitCode)
	}
	// 上限ちょうどは切り詰めではない。
	spec = helperSpec(generous, "flood", "1000")
	spec.MaxOutputBytes = 1000
	if res, err = New().Run(context.Background(), spec); err != nil || res.StdoutTruncated || res.StderrTruncated || len(res.Stdout) != 1000 {
		t.Fatalf("exact fit = %d bytes, truncated %v/%v, %v", len(res.Stdout), res.StdoutTruncated, res.StderrTruncated, err)
	}
}

func TestRunStdinIsEmptyAndEnvIsFiltered(t *testing.T) {
	t.Setenv("PUPPET_REMO_TEST_SECRET", "s3cret")
	res, err := New().Run(context.Background(), helperSpec(generous, "stdin"))
	if err != nil || string(res.Stdout) != "0" {
		t.Fatalf("stdin read = %q, %v", res.Stdout, err)
	}
	res, err = New().Run(context.Background(), helperSpec(generous, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Stdout), "PUPPET_REMO_TEST_SECRET") {
		t.Errorf("a non-allowlisted environment variable reached the child: %q", res.Stdout)
	}
}

func TestRunStartFailureHasNoPath(t *testing.T) {
	spec := helperSpec(generous, "echo", "", "", "0")
	spec.Executable = "/nonexistent-dir/definitely-missing-tool"
	_, err := New().Run(context.Background(), spec)
	if !errors.Is(err, domain.ErrCommandStartFailed) {
		t.Fatalf("err = %v, want command_start_failed", err)
	}
	if strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("the error leaks the path: %v", err)
	}
}

func TestRunContextCancelKillsAndReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := New().Run(ctx, helperSpec(generous, "sleep"))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the context was canceled")
	}
}

func TestRunRejectsIncompleteSpec(t *testing.T) {
	for name, mutate := range map[string]func(*specT){
		"no executable": func(s *specT) { s.Executable = "" },
		"no timeout":    func(s *specT) { s.Timeout = 0 },
		"no cap":        func(s *specT) { s.MaxOutputBytes = 0 },
	} {
		spec := helperSpec(generous, "echo", "", "", "0")
		mutate(&spec)
		if _, err := New().Run(context.Background(), spec); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCapBuffer(t *testing.T) {
	c := &capBuffer{limit: 5}
	for _, in := range []string{"abc", "def", "ghi"} {
		if n, err := c.Write([]byte(in)); n != len(in) || err != nil {
			t.Fatalf("Write(%q) = %d, %v: must report the whole write (the writer must never block the process)", in, n, err)
		}
	}
	if string(c.buf) != "abcde" || !c.truncated {
		t.Errorf("buf = %q, truncated = %v", c.buf, c.truncated)
	}
}

func TestFilterEnv(t *testing.T) {
	got := filterEnv([]string{"PATH=/bin", "Path=C:\\x", "SECRET_TOKEN=x", "NOEQUALS", "=C:=C:\\", "SystemRoot=C:\\Windows"})
	want := []string{"PATH=/bin", "Path=C:\\x", "SystemRoot=C:\\Windows"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("filterEnv = %q, want %q", got, want)
	}
}
