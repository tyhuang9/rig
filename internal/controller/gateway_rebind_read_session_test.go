package controller

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
)

func TestLANGatewayRebindReadSessionRetainsLiveAdministrator(t *testing.T) {
	for _, mode := range []string{"valid", "session revoked", "actor replaced", "role downgraded", "request cancelled", "invocation cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, request, authentication := newRebindSessionFixture()
			request.Method = http.MethodGet
			request.Header.Del("X-CSRF-Token")
			authentication.checkCSRF = func(string, string) bool {
				t.Fatal("GET status invoked mutation CSRF validation")
				return false
			}
			requestCtx, cancelRequest := context.WithCancel(request.Context())
			defer cancelRequest()
			request = request.WithContext(requestCtx)
			actor, guard, err := s.bindLANGatewayRebindReadSession(request)
			if err != nil || guard == nil || actor != "original-administrator" {
				t.Fatalf("read binding: actor=%q guard=%t error=%v", actor, guard != nil, err)
			}
			// Later request headers cannot replace the captured session identity.
			request.Header.Set("Cookie", auth.SessionCookie+"=replacement-session")
			invocation, cancelInvocation := context.WithCancel(context.Background())
			defer cancelInvocation()
			switch mode {
			case "request cancelled":
				cancelRequest()
			case "invocation cancelled":
				cancelInvocation()
			case "valid":
			default:
				rebindSessionRevocations()[mode](authentication)
			}
			err = guard(invocation)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, appaccess.ErrApprovalRequired) {
				t.Fatalf("changed authority disclosed status: %v", err)
			}
		})
	}
}

func TestLANGatewayRebindReadSessionRefusesAmbiguousOrMutationRequest(t *testing.T) {
	for _, mode := range []string{"POST", "HEAD", "OPTIONS", "nil request", "duplicate session", "missing session"} {
		t.Run(mode, func(t *testing.T) {
			s, request, authentication := newRebindSessionFixture()
			request.Method = http.MethodGet
			request.Header.Del("X-CSRF-Token")
			switch mode {
			case "nil request":
				request = nil
			case "duplicate session":
				request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "original-session"})
			case "missing session":
				request.Header.Del("Cookie")
			default:
				request.Method = mode
			}
			authentication.authenticate = func(string) (auth.User, string, error) {
				t.Fatal("invalid request reached authentication")
				return auth.User{}, "", nil
			}
			actor, guard, err := s.bindLANGatewayRebindReadSession(request)
			if actor != "" || guard != nil || !errors.Is(err, appaccess.ErrApprovalRequired) {
				t.Fatalf("invalid read request accepted: actor=%q guard=%t err=%v", actor, guard != nil, err)
			}
		})
	}
}
