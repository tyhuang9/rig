package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/hostd/hostd/internal/apicontract"
)

// Client is the complete controller surface used by the operator console.
// Implementations own HTTP cookies and CSRF rotation; the TUI never handles or
// renders raw credentials.
type Client interface {
	BootstrapStatus(context.Context) (apicontract.BootstrapStatus, error)
	Bootstrap(context.Context, apicontract.BootstrapRequest) (apicontract.SessionResponse, error)
	Login(context.Context, apicontract.LoginRequest) (apicontract.SessionResponse, error)
	Logout(context.Context) error
	Me(context.Context) (apicontract.MeResponse, error)
	Status(context.Context) (apicontract.SystemStatus, error)
	Applications(context.Context) (apicontract.ApplicationList, error)
	Deploy(context.Context, string, string) (apicontract.JobMutationResponse, error)
	Lifecycle(context.Context, string, string, string) (apicontract.JobMutationResponse, error)
	Jobs(context.Context) (apicontract.JobList, error)
	Job(context.Context, string) (apicontract.Job, error)
	FollowJob(context.Context, string, int64) (<-chan apicontract.JobEvent, <-chan error)
	CancelJob(context.Context, string, string) (apicontract.JobResponse, error)
	// ResumeJob remains on the adapter contract for hostctl/session regression
	// coverage. The Switchboard intentionally exposes no Resume action.
	ResumeJob(context.Context, string, string) (apicontract.JobResponse, error)
}

// SessionStore is deliberately opaque: controller client implementations own
// the session representation and the TUI only requests protected persistence.
type SessionStore interface {
	Load(context.Context) ([]byte, error)
	Save(context.Context, []byte) error
	Clear(context.Context) error
}

type ClientFactory func(SessionStore) (Client, error)
type SessionStoreFactory func() (SessionStore, error)

// HTTPError lets an injected client distinguish an expired session from an
// unavailable controller without coupling the UI to an HTTP implementation.
type HTTPError struct {
	Status int
	Code   string
	Detail string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return fmt.Sprintf("controller returned %d (%s): %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("controller returned %d: %s", e.Status, e.Detail)
}

func isUnauthenticated(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.Status == 401
}
