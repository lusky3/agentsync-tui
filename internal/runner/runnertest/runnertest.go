// Package runnertest is the scripted runner.Runner the screen tests share:
// responses are keyed by the space-joined argv, every call and its context
// are recorded, and unscripted calls can be made to block until their
// context ends (to test cancellation).
package runnertest

import (
	"context"
	"strings"
	"sync"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// Response is what one scripted command returns.
type Response struct {
	Res runner.Result
	Err error
}

// Fake is a scripted runner. The zero value answers every call with an
// empty successful result; New returns one ready to script.
type Fake struct {
	mu    sync.Mutex
	by    map[string]Response
	def   Response
	block bool
	calls []string
	ctxs  []context.Context
	// Guard, when set, is called with each argv before it is answered; a
	// test uses it to panic on a command that must never reach the runner.
	Guard func(argv string)
}

// New returns an empty Fake.
func New() *Fake { return &Fake{by: map[string]Response{}} }

// OK returns a Fake whose default response is stdout with exit 0.
func OK(stdout string) *Fake { return New().Default(runner.Result{Stdout: stdout}, nil) }

// On scripts the response for one command (space-joined argv).
func (f *Fake) On(argv string, res runner.Result, err error) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.by == nil {
		f.by = map[string]Response{}
	}
	f.by[argv] = Response{Res: res, Err: err}
	return f
}

// Default scripts the response for every command On did not.
func (f *Fake) Default(res runner.Result, err error) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.def = Response{Res: res, Err: err}
	return f
}

// Fail makes every unscripted command return err.
func (f *Fake) Fail(err error) *Fake { return f.Default(runner.Result{}, err) }

// Block makes unscripted commands wait until their context ends and
// return its error, so a test can check that a read is cancelled.
func (f *Fake) Block() *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = true
	return f
}

// Run implements runner.Runner.
func (f *Fake) Run(ctx context.Context, args ...string) (runner.Result, error) {
	key := strings.Join(args, " ")
	if f.Guard != nil {
		f.Guard(key)
	}
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.ctxs = append(f.ctxs, ctx)
	r, scripted := f.by[key]
	if !scripted {
		r = f.def
	}
	block := f.block && !scripted
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return runner.Result{}, ctx.Err()
	}
	return r.Res, r.Err
}

// Calls returns every command run so far, space-joined, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Ctxs returns the context of every call, in order.
func (f *Fake) Ctxs() []context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]context.Context(nil), f.ctxs...)
}

// Matching returns the calls that start with prefix.
func (f *Fake) Matching(prefix string) []string {
	var out []string
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}
