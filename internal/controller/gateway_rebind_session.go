package controller

import (
	"context"
	"net/http"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
)

// bindLANGatewayRebindSession captures one interactive request's identity. Its
// callback checks live session authority only; repository approval, exact
// physical proof and effect guards remain separate requirements. It must never
// be persisted or attached to the shared Manager or startup recovery path.
func (s *Server) bindLANGatewayRebindSession(r *http.Request) (string, func(context.Context) error, error) {
	if s == nil || s.Auth == nil || r == nil {
		return "", nil, appaccess.ErrApprovalRequired
	}
	requestContext := r.Context()
	requestPrincipal, ok := requestContext.Value(principalKey{}).(principal)
	if !ok || requestPrincipal.user.ID == "" || requestPrincipal.user.Role != "administrator" || requestContext.Err() != nil {
		return "", nil, appaccess.ErrApprovalRequired
	}
	var sessionToken string
	var sessionCount int
	for _, cookie := range r.Cookies() {
		if cookie.Name == auth.SessionCookie {
			sessionCount++
			sessionToken = cookie.Value
		}
	}
	csrfValues := r.Header.Values("X-CSRF-Token")
	if sessionCount != 1 || sessionToken == "" || len(csrfValues) != 1 || csrfValues[0] == "" {
		return "", nil, appaccess.ErrApprovalRequired
	}
	actorID, submittedCSRF, authentication := requestPrincipal.user.ID, csrfValues[0], s.Auth
	guard := func(ctx context.Context) error {
		if ctx == nil || ctx.Err() != nil || requestContext.Err() != nil {
			return appaccess.ErrApprovalRequired
		}
		current, csrfHash, err := authentication.Authenticate(sessionToken)
		if err != nil || current.ID != actorID || current.Role != "administrator" ||
			ctx.Err() != nil || requestContext.Err() != nil {
			return appaccess.ErrApprovalRequired
		}
		if !authentication.CheckCSRF(csrfHash, submittedCSRF) || ctx.Err() != nil || requestContext.Err() != nil {
			return appaccess.ErrApprovalRequired
		}
		return nil
	}
	if err := guard(requestContext); err != nil {
		return "", nil, err
	}
	return actorID, guard, nil
}
