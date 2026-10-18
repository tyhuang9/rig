package deploymenteffects

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
)

var ErrScopeUnavailable = errors.New("deployment effects scope is unavailable")

// Scope is an opaque, process-local proof that the cross-process deployment
// effects lease is currently held for one exact controller working directory.
// A zero value and copied values not backed by AcquireScoped are invalid.
type Scope struct{ state *scopeState }

type scopeState struct {
	mu               sync.Mutex
	condition        *sync.Cond
	workingDirectory string
	active           int
	closing          bool
	live             bool
}

type scopeContextKey struct{}

// AcquireScoped acquires the existing OS-backed deployment effects lease and
// couples it to an in-process scope. Release first prevents new operations,
// waits for bounded operations already inside the scope, invalidates it, and
// only then drops the OS lease. Callers must not enter unbounded operations.
func AcquireScoped(ctx context.Context, workingDirectory string) (Scope, func() error, error) {
	if filepath.Clean(workingDirectory) != workingDirectory {
		return Scope{}, nil, ErrScopeUnavailable
	}
	releaseLease, err := Acquire(ctx, workingDirectory)
	if err != nil {
		return Scope{}, nil, err
	}
	state := &scopeState{workingDirectory: workingDirectory, live: true}
	state.condition = sync.NewCond(&state.mu)
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			state.mu.Lock()
			state.closing = true
			for state.active != 0 {
				state.condition.Wait()
			}
			state.live = false
			state.mu.Unlock()
			releaseErr = releaseLease()
		})
		return releaseErr
	}
	return Scope{state: state}, release, nil
}

// WithScope binds a scope to parent without changing its cancellation or
// deadline. The scope remains useful only while its acquisition is live.
func WithScope(parent context.Context, scope Scope) context.Context {
	if parent == nil {
		return nil
	}
	return context.WithValue(parent, scopeContextKey{}, scope)
}

// EnterScope pins the lease for one bounded retained effect. It rejects
// missing, foreign, expired, or closing scopes. The returned leave function
// must be deferred around the complete inspect/check/command/reinspect unit.
func EnterScope(ctx context.Context, workingDirectory string) (func(), error) {
	if ctx == nil || ctx.Err() != nil || filepath.Clean(workingDirectory) != workingDirectory {
		return nil, ErrScopeUnavailable
	}
	scope, ok := ctx.Value(scopeContextKey{}).(Scope)
	if !ok || scope.state == nil {
		return nil, ErrScopeUnavailable
	}
	state := scope.state
	state.mu.Lock()
	if !state.live || state.closing || state.workingDirectory != workingDirectory {
		state.mu.Unlock()
		return nil, ErrScopeUnavailable
	}
	state.active++
	state.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			state.mu.Lock()
			state.active--
			if state.active == 0 {
				state.condition.Broadcast()
			}
			state.mu.Unlock()
		})
	}, nil
}
