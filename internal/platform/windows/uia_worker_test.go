package windows

import (
	"context"
	"errors"
	"testing"
	"time"
)

const uiaTestWait = 5 * time.Second

func TestUIAWorkerInitAndCleanupRunOnTheWorkerThread(t *testing.T) {
	order := make(chan string, 3)
	w, err := startUIAWorker(func() (func(), error) {
		order <- "init"
		return func() { order <- "cleanup" }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Do(context.Background(), func() error { order <- "job"; return nil }); err != nil {
		t.Fatal(err)
	}
	w.Close()
	w.Close()
	for _, want := range []string{"init", "job", "cleanup"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(uiaTestWait):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestUIAWorkerInitFailure(t *testing.T) {
	boom := errors.New("boom")
	if _, err := startUIAWorker(func() (func(), error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("error = %v, want boom", err)
	}
}

func TestUIAWorkerSerializesJobs(t *testing.T) {
	w, err := startUIAWorker(func() (func(), error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var running, maxRunning, total int // ワーカーのスレッドだけが触る（同期は Do の完了待ちで足りる）
	const callers = 20
	errs := make(chan error, callers)
	for range callers {
		go func() {
			errs <- w.Do(context.Background(), func() error {
				running++
				maxRunning = max(maxRunning, running)
				total++
				running--
				return nil
			})
		}()
	}
	for range callers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if maxRunning != 1 || total != callers {
		t.Errorf("max concurrent jobs %d, total %d; want 1 and %d", maxRunning, total, callers)
	}
}

func TestUIAWorkerReturnsJobError(t *testing.T) {
	w, err := startUIAWorker(func() (func(), error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	boom := errors.New("boom")
	if err := w.Do(context.Background(), func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("error = %v, want boom", err)
	}
}

func TestUIAWorkerCanceledContextRunsNothing(t *testing.T) {
	w, err := startUIAWorker(func() (func(), error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	if err := w.Do(ctx, func() error { ran = true; return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	// ここで Do を 1 回通せば、先の仕事が（もし投入されていたら）すでに実行済みになる
	if err := w.Do(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("a job ran for a canceled context")
	}
}

func TestUIAWorkerCancelWhileWaitingForABusyWorker(t *testing.T) {
	w, err := startUIAWorker(func() (func(), error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	started, release := make(chan struct{}), make(chan struct{})
	busyDone := make(chan error, 1)
	go func() {
		busyDone <- w.Do(context.Background(), func() error { close(started); <-release; return nil })
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	ran := false
	go func() { waiting <- w.Do(ctx, func() error { ran = true; return nil }) }()
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(uiaTestWait):
		t.Fatal("a canceled Do stayed blocked behind a busy worker")
	}
	close(release)
	if err := <-busyDone; err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("the canceled job ran")
	}
}

func TestUIAWorkerDoAfterClose(t *testing.T) {
	w, err := startUIAWorker(func() (func(), error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := w.Do(context.Background(), func() error { return nil }); !errors.Is(err, errUIAClosed) {
		t.Errorf("error = %v, want errUIAClosed", err)
	}
}
