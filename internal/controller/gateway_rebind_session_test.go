package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
)

type rebindSessionAuthFake struct {
	controllerAuthFake
	authenticate func(string) (auth.User, string, error)
	checkCSRF    func(string, string) bool
}

func (fake *rebindSessionAuthFake) Authenticate(token string) (auth.User, string, error) {
	return fake.authenticate(token)
}

func (fake *rebindSessionAuthFake) CheckCSRF(hash, token string) bool {
	return fake.checkCSRF(hash, token)
}

func newRebindSessionFixture() (*Server, *http.Request, *rebindSessionAuthFake) {
	fake := &rebindSessionAuthFake{
		authenticate: func(token string) (auth.User, string, error) {
			if token != "original-session" {
				return auth.User{}, "", errors.New("invalid test session")
			}
			return auth.User{ID: "original-administrator", Role: "administrator"}, "live-csrf-hash", nil
		},
		checkCSRF: func(hash, token string) bool { return hash == "live-csrf-hash" && token == "original-csrf" },
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/system/lan-gateway-rebind", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "original-session"})
	r.Header.Set("X-CSRF-Token", "original-csrf")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{
		user: auth.User{ID: "original-administrator", Role: "administrator"}, csrfHash: "stale-csrf-hash",
	}))
	return &Server{Auth: fake}, r, fake
}

func TestLANGatewayRebindSessionBindsOriginalRequest(t *testing.T) {
	s, r, _ := newRebindSessionFixture()
	actor, guard, err := s.bindLANGatewayRebindSession(r)
	if err != nil || actor != "original-administrator" || guard == nil {
		t.Fatalf("bind actor=%q guard present=%v err=%v", actor, guard != nil, err)
	}
	// The principal's old CSRF hash cannot authorize the new request. The live
	// authentication result above supplies the hash used at every check.
	if err := guard(context.Background()); err != nil {
		t.Fatalf("live CSRF validation failed: %v", err)
	}
	r.Header.Set("Cookie", auth.SessionCookie+"=replacement-session")
	r.Header.Set("X-CSRF-Token", "replacement-csrf")
	*r = *r.WithContext(context.WithValue(context.Background(), principalKey{}, principal{
		user: auth.User{ID: "replacement-administrator", Role: "administrator"},
	}))
	s.Auth = &rebindSessionAuthFake{
		authenticate: func(string) (auth.User, string, error) {
			t.Fatal("guard changed authentication service")
			return auth.User{}, "", nil
		},
	}
	if err := guard(r.Context()); err != nil {
		t.Fatalf("request mutation replaced the original binding: %v", err)
	}
}

func TestLANGatewayRebindSessionRejectsAmbiguousRequest(t *testing.T) {
	for name, mutate := range map[string]func(**Server, **http.Request, *rebindSessionAuthFake){
		"nil server":  func(s **Server, _ **http.Request, _ *rebindSessionAuthFake) { *s = nil },
		"nil auth":    func(s **Server, _ **http.Request, _ *rebindSessionAuthFake) { (*s).Auth = nil },
		"nil request": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) { *r = nil },
		"missing principal": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			*r = (*r).WithContext(context.Background())
		},
		"wrong principal type": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			*r = (*r).WithContext(context.WithValue(context.Background(), principalKey{}, "administrator"))
		},
		"empty actor": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			*r = (*r).WithContext(context.WithValue(context.Background(), principalKey{}, principal{user: auth.User{Role: "administrator"}}))
		},
		"viewer principal": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			*r = (*r).WithContext(context.WithValue(context.Background(), principalKey{}, principal{user: auth.User{ID: "original-administrator", Role: "viewer"}}))
		},
		"missing cookie": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) { (*r).Header.Del("Cookie") },
		"empty cookie": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			(*r).Header.Set("Cookie", auth.SessionCookie+"=")
		},
		"duplicate cookie": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			(*r).AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "original-session"})
		},
		"duplicate cookie header": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			(*r).Header.Add("Cookie", auth.SessionCookie+"=another-session")
		},
		"missing csrf": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) { (*r).Header.Del("X-CSRF-Token") },
		"empty csrf":   func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) { (*r).Header.Set("X-CSRF-Token", "") },
		"duplicate csrf": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			(*r).Header.Add("X-CSRF-Token", "original-csrf")
		},
		"cancelled request": func(_ **Server, r **http.Request, _ *rebindSessionAuthFake) {
			ctx, cancel := context.WithCancel((*r).Context())
			cancel()
			*r = (*r).WithContext(ctx)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, fake := newRebindSessionFixture()
			fake.authenticate = func(string) (auth.User, string, error) {
				t.Fatal("invalid request reached authentication")
				return auth.User{}, "", nil
			}
			mutate(&s, &r, fake)
			actor, guard, err := s.bindLANGatewayRebindSession(r)
			if actor != "" || guard != nil || err != appaccess.ErrApprovalRequired {
				t.Fatalf("invalid request accepted: actor=%q guard present=%v err=%v", actor, guard != nil, err)
			}
		})
	}
	for name, mutate := range rebindSessionRevocations() {
		t.Run("initial "+name, func(t *testing.T) {
			s, r, fake := newRebindSessionFixture()
			mutate(fake)
			actor, guard, err := s.bindLANGatewayRebindSession(r)
			if actor != "" || guard != nil || err != appaccess.ErrApprovalRequired {
				t.Fatalf("invalid authority accepted: actor=%q guard present=%v err=%v", actor, guard != nil, err)
			}
		})
	}
}

func rebindSessionRevocations() map[string]func(*rebindSessionAuthFake) {
	return map[string]func(*rebindSessionAuthFake){
		"session revoked": func(fake *rebindSessionAuthFake) {
			fake.authenticate = func(string) (auth.User, string, error) {
				return auth.User{}, "", errors.New("sensitive backend failure must not escape")
			}
		},
		"actor replaced": func(fake *rebindSessionAuthFake) {
			fake.authenticate = func(string) (auth.User, string, error) {
				return auth.User{ID: "another-administrator", Role: "administrator"}, "live-csrf-hash", nil
			}
		},
		"role downgraded": func(fake *rebindSessionAuthFake) {
			fake.authenticate = func(string) (auth.User, string, error) {
				return auth.User{ID: "original-administrator", Role: "viewer"}, "live-csrf-hash", nil
			}
		},
		"csrf rotated": func(fake *rebindSessionAuthFake) {
			fake.authenticate = func(string) (auth.User, string, error) {
				return auth.User{ID: "original-administrator", Role: "administrator"}, "rotated-csrf-hash", nil
			}
		},
		"csrf refused": func(fake *rebindSessionAuthFake) { fake.checkCSRF = func(string, string) bool { return false } },
	}
}

func TestLANGatewayRebindSessionRechecksLiveAuthority(t *testing.T) {
	for name, mutate := range rebindSessionRevocations() {
		t.Run(name, func(t *testing.T) {
			s, r, fake := newRebindSessionFixture()
			_, guard, err := s.bindLANGatewayRebindSession(r)
			if err != nil {
				t.Fatal(err)
			}
			mutate(fake)
			if err := guard(context.Background()); err != appaccess.ErrApprovalRequired {
				t.Fatalf("authority change accepted: %v", err)
			}
		})
	}
	for _, point := range []string{"before", "authenticate", "csrf"} {
		for _, original := range []bool{false, true} {
			name := "invocation " + point
			if original {
				name = "original request " + point
			}
			t.Run(name, func(t *testing.T) {
				s, r, fake := newRebindSessionFixture()
				requestCtx, requestCancel := context.WithCancel(r.Context())
				defer requestCancel()
				r = r.WithContext(requestCtx)
				_, guard, err := s.bindLANGatewayRebindSession(r)
				if err != nil {
					t.Fatal(err)
				}
				invocationCtx, invocationCancel := context.WithCancel(context.Background())
				defer invocationCancel()
				cancel := invocationCancel
				if original {
					cancel = requestCancel
				}
				authCalls, csrfCalls := 0, 0
				authenticate, csrf := fake.authenticate, fake.checkCSRF
				fake.authenticate = func(token string) (auth.User, string, error) {
					authCalls++
					if point == "authenticate" {
						cancel()
					}
					return authenticate(token)
				}
				fake.checkCSRF = func(hash, token string) bool {
					csrfCalls++
					if point == "csrf" {
						cancel()
					}
					return csrf(hash, token)
				}
				if point == "before" {
					cancel()
				}
				if err := guard(invocationCtx); err != appaccess.ErrApprovalRequired {
					t.Fatalf("cancellation accepted: %v", err)
				}
				wantAuth, wantCSRF := 1, 1
				if point == "before" {
					wantAuth, wantCSRF = 0, 0
				} else if point == "authenticate" {
					wantCSRF = 0
				}
				if authCalls != wantAuth || csrfCalls != wantCSRF {
					t.Fatalf("calls after cancellation: auth=%d csrf=%d", authCalls, csrfCalls)
				}
				if original {
					if err := guard(context.WithoutCancel(requestCtx)); err != appaccess.ErrApprovalRequired {
						t.Fatalf("detached context revived request: %v", err)
					}
				}
			})
		}
	}
	t.Run("nil invocation context", func(t *testing.T) {
		s, r, fake := newRebindSessionFixture()
		_, guard, err := s.bindLANGatewayRebindSession(r)
		if err != nil {
			t.Fatal(err)
		}
		fake.authenticate = func(string) (auth.User, string, error) {
			t.Fatal("nil context reached authentication")
			return auth.User{}, "", nil
		}
		if err := guard(nil); err != appaccess.ErrApprovalRequired {
			t.Fatalf("nil context accepted: %v", err)
		}
	})
}

func TestLANGatewayRebindSessionIsolation(t *testing.T) {
	var firstRevoked atomic.Bool
	fake := &rebindSessionAuthFake{
		authenticate: func(token string) (auth.User, string, error) {
			if token != "first" && token != "second" {
				return auth.User{}, "", errors.New("invalid test session")
			}
			if token == "first" && firstRevoked.Load() {
				return auth.User{}, "", errors.New("revoked test session")
			}
			return auth.User{ID: token + "-actor", Role: "administrator"}, token + "-hash", nil
		},
		checkCSRF: func(hash, token string) bool {
			return (hash == "first-hash" && token == "first-csrf") || (hash == "second-hash" && token == "second-csrf")
		},
	}
	s := &Server{Auth: fake}
	guards := make([]func(context.Context) error, 0, 2)
	for _, token := range []string{"first", "second"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/system/lan-gateway-rebind", nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		r.Header.Set("X-CSRF-Token", token+"-csrf")
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{user: auth.User{ID: token + "-actor", Role: "administrator"}}))
		actor, guard, err := s.bindLANGatewayRebindSession(r)
		if err != nil || actor != token+"-actor" || guard == nil {
			t.Fatalf("bind %q: actor=%q err=%v", token, actor, err)
		}
		guards = append(guards, guard)
	}
	for _, revoked := range []bool{false, true} {
		firstRevoked.Store(revoked)
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for i, guard := range guards {
			workers.Add(1)
			go func(i int, guard func(context.Context) error) {
				defer workers.Done()
				<-start
				err := guard(context.Background())
				if i == 0 && revoked {
					if err != appaccess.ErrApprovalRequired {
						results <- errors.New("first revoked request retained authority")
					}
				} else if err != nil {
					results <- errors.New("one request changed another request's binding")
				}
			}(i, guard)
		}
		close(start)
		workers.Wait()
		close(results)
		for err := range results {
			t.Error(err)
		}
	}
}
