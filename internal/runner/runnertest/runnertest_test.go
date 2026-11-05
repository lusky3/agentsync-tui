package runnertest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

func TestScriptedResponsesAndLog(t *testing.T) {
	f := OK("default").On("status --json", runner.Result{ExitCode: 2, Stdout: "{}"}, nil)
	res, err := f.Run(context.Background(), "status", "--json")
	if err != nil || res.ExitCode != 2 || res.Stdout != "{}" {
		t.Fatalf("scripted: %+v %v", res, err)
	}
	res, err = f.Run(context.Background(), "agent", "list")
	if err != nil || res.Stdout != "default" {
		t.Fatalf("default: %+v %v", res, err)
	}
	if got := f.Calls(); len(got) != 2 || got[0] != "status --json" || got[1] != "agent list" {
		t.Fatalf("calls %v", got)
	}
	if got := f.Matching("agent"); len(got) != 1 || got[0] != "agent list" {
		t.Fatalf("matching %v", got)
	}
	boom := errors.New("boom")
	if _, err := New().Fail(boom).Run(context.Background(), "x"); !errors.Is(err, boom) {
		t.Fatalf("Fail: %v", err)
	}
	var zero Fake
	if res, err := zero.Run(context.Background(), "x"); err != nil || res.ExitCode != 0 {
		t.Fatalf("zero value: %+v %v", res, err)
	}
}

func TestBlockUntilCancelled(t *testing.T) {
	f := New().Block().On("version", runner.Result{Stdout: "v"}, nil)
	if res, _ := f.Run(context.Background(), "version"); res.Stdout != "v" {
		t.Fatal("scripted call must not block")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.Run(ctx, "status"); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("unscripted call returned before cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("after cancel: %v", err)
	}
	if ctxs := f.Ctxs(); len(ctxs) != 2 || ctxs[1].Err() == nil {
		t.Fatalf("ctxs %v", ctxs)
	}
}

func TestGuard(t *testing.T) {
	f := OK("")
	f.Guard = func(argv string) {
		if argv == "secret get K" {
			panic("secret get reached the runner")
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("guard did not fire")
		}
	}()
	_, _ = f.Run(context.Background(), "secret", "get", "K")
}
