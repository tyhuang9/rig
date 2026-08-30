package controller

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/sourceconnections"
)

func (s *Server) listSourceConnections(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalSources(w, r) {
		return
	}
	connections, err := s.Sources.List(r.Context(), sourceOwner(r))
	if err != nil {
		sourceProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apicontract.SourceConnectionList{Items: contractSourceConnections(connections, s.Sources.InstallURL())})
}

func (s *Server) startGitHubDeviceConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderSources(w, r) {
		return
	}
	started, err := s.Sources.Start(r.Context(), sourceOwner(r))
	if err != nil {
		sourceProblem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, apicontract.GitHubDeviceAuthorization{
		ConnectionID: started.ConnectionID, UserCode: started.UserCode, VerificationUri: started.VerificationURI,
		InstallUrl: started.InstallURL, ExpiresAt: started.ExpiresAt.Format(time.RFC3339Nano), PollIntervalSeconds: int(started.PollInterval / time.Second),
	})
}

func (s *Server) pollGitHubDeviceConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderSources(w, r) {
		return
	}
	owner, id := sourceOwner(r), r.PathValue("connectionId")
	connection, err := s.Sources.Poll(r.Context(), owner, id)
	if err == nil {
		writeJSON(w, http.StatusOK, contractSourceConnection(connection, s.Sources.InstallURL()))
		return
	}
	var serviceError *sourceconnections.Error
	if errors.As(err, &serviceError) && serviceError.Code == "authorization_pending" {
		pending, getErr := s.Sources.Get(r.Context(), owner, id)
		if getErr != nil {
			sourceProblem(w, r, getErr)
			return
		}
		setRetryAfter(w, serviceError.RetryAfter)
		writeJSON(w, http.StatusAccepted, contractSourceConnection(pending, s.Sources.InstallURL()))
		return
	}
	if errors.As(err, &serviceError) && serviceError.Code == "poll_too_soon" {
		setRetryAfter(w, serviceError.RetryAfter)
	}
	sourceProblem(w, r, err)
}

func (s *Server) refreshSourceConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderSources(w, r) {
		return
	}
	connection, err := s.Sources.Refresh(r.Context(), sourceOwner(r), r.PathValue("connectionId"))
	if err != nil {
		sourceProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contractSourceConnection(connection, s.Sources.InstallURL()))
}

func (s *Server) disconnectSourceConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalSources(w, r) {
		return
	}
	if err := s.Sources.Disconnect(r.Context(), sourceOwner(r), r.PathValue("connectionId")); err != nil {
		sourceProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderSources(w, r) {
		return
	}
	page, valid := boundedQueryInteger(r, "page", 1, 1, 10000)
	if !valid {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Page must be an integer between 1 and 10000", nil)
		return
	}
	perPage, valid := boundedQueryInteger(r, "perPage", 30, 1, 100)
	if !valid {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Per-page count must be an integer between 1 and 100", nil)
		return
	}
	providerPage, err := s.Sources.Installations(r.Context(), sourceOwner(r), r.PathValue("connectionId"), page, perPage)
	if err != nil {
		sourceProblem(w, r, err)
		return
	}
	items := make([]apicontract.GitHubInstallation, 0, len(providerPage.Installations))
	for _, installation := range providerPage.Installations {
		item := apicontract.GitHubInstallation{ID: installation.ID, AccountLogin: installation.AccountLogin, AccountType: installation.AccountType, TargetType: installation.TargetType, RepositorySelection: installation.RepositorySelection, CachedAt: installation.CachedAt.Format(time.RFC3339Nano)}
		if installation.SuspendedAt != nil {
			item.SuspendedAt = installation.SuspendedAt.Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, apicontract.GitHubInstallationPage{Page: providerPage.Page, PerPage: providerPage.PerPage, TotalCount: providerPage.TotalCount, Items: items})
}

func (s *Server) listGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderSources(w, r) {
		return
	}
	installationID, ok := positivePathInteger(r, "installationId")
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_source", "Invalid GitHub installation", nil)
		return
	}
	page, ok := boundedQueryInteger(r, "page", 1, 1, 10000)
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Page must be an integer between 1 and 10000", nil)
		return
	}
	perPage, ok := boundedQueryInteger(r, "perPage", 30, 1, 100)
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Per-page count must be an integer between 1 and 100", nil)
		return
	}
	providerPage, err := s.Sources.Repositories(r.Context(), sourceOwner(r), r.PathValue("connectionId"), installationID, page, perPage)
	if err != nil {
		sourceProblem(w, r, err)
		return
	}
	items := make([]apicontract.GitHubRepository, 0, len(providerPage.Repositories))
	for _, repository := range providerPage.Repositories {
		items = append(items, apicontract.GitHubRepository{ID: repository.ID, Owner: repository.Owner, Name: repository.Name, DefaultBranch: repository.DefaultBranch, Private: repository.Private, Archived: repository.Archived, Disabled: repository.Disabled})
	}
	writeJSON(w, http.StatusOK, apicontract.GitHubRepositoryPage{Page: page, PerPage: perPage, TotalCount: providerPage.TotalCount, Items: items})
}

func (s *Server) listGitHubBranches(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderSources(w, r) {
		return
	}
	installationID, ok := positivePathInteger(r, "installationId")
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_source", "Invalid GitHub installation", nil)
		return
	}
	repositoryID, ok := positivePathInteger(r, "repositoryId")
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_source", "Invalid GitHub repository", nil)
		return
	}
	page, ok := boundedQueryInteger(r, "page", 1, 1, 10000)
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Page must be an integer between 1 and 10000", nil)
		return
	}
	perPage, ok := boundedQueryInteger(r, "perPage", 30, 1, 100)
	if !ok {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Per-page count must be an integer between 1 and 100", nil)
		return
	}
	providerPage, err := s.Sources.Branches(r.Context(), sourceOwner(r), r.PathValue("connectionId"), installationID, repositoryID, page, perPage)
	if err != nil {
		sourceProblem(w, r, err)
		return
	}
	items := make([]apicontract.GitHubBranch, 0, len(providerPage.Branches))
	for _, branch := range providerPage.Branches {
		items = append(items, apicontract.GitHubBranch{Name: branch.Name, Sha: branch.SHA, Protected: branch.Protected})
	}
	writeJSON(w, http.StatusOK, apicontract.GitHubBranchPage{Page: page, PerPage: perPage, Items: items})
}

func positivePathInteger(r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return value, err == nil && value > 0
}

func (s *Server) requireLocalSources(w http.ResponseWriter, r *http.Request) bool {
	if s.Sources == nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Source connection storage is unavailable", nil)
		return false
	}
	return true
}

func (s *Server) requireProviderSources(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireLocalSources(w, r) {
		return false
	}
	if !s.Sources.ProviderEnabled() {
		problem(w, r, http.StatusServiceUnavailable, "provider_unavailable", "GitHub connections are not configured", nil)
		return false
	}
	return true
}

func sourceOwner(r *http.Request) string {
	return r.Context().Value(principalKey{}).(principal).user.ID
}

func sourceProblem(w http.ResponseWriter, r *http.Request, err error) {
	var serviceError *sourceconnections.Error
	if !errors.As(err, &serviceError) {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not process the source connection", nil)
		return
	}
	status, detail := http.StatusInternalServerError, "Could not process the source connection"
	switch serviceError.Code {
	case "connection_not_found":
		status, detail = http.StatusNotFound, "Source connection was not found"
	case "poll_too_soon":
		status, detail = http.StatusTooManyRequests, "Wait before polling this source connection again"
	case "authorization_denied":
		status, detail = http.StatusConflict, "GitHub authorization was denied"
	case "authorization_expired":
		status, detail = http.StatusGone, "GitHub authorization expired"
	case "identity_already_connected":
		status, detail = http.StatusConflict, "This GitHub identity already has an active local connection"
	case "source_access_lost":
		status, detail = http.StatusConflict, "GitHub source access was lost; authorize the connection again"
	case "authentication_required":
		status, detail = http.StatusForbidden, "GitHub authorization does not permit this operation"
	case "rate_limited":
		status, detail = http.StatusTooManyRequests, "GitHub temporarily rate limited this operation"
	case "provider_unavailable":
		status, detail = http.StatusServiceUnavailable, "GitHub is temporarily unavailable"
	case "invalid_source":
		status, detail = http.StatusUnprocessableEntity, "GitHub source is invalid or no longer exists"
	case "source_too_large":
		status, detail = http.StatusRequestEntityTooLarge, "GitHub source exceeds inspection limits"
	case "invalid_connection_state":
		status, detail = http.StatusConflict, "Source connection is not in the required state"
	}
	problem(w, r, status, serviceError.Code, detail, nil)
}

func contractSourceConnection(connection sourceconnections.Connection, installURL string) apicontract.SourceConnection {
	result := apicontract.SourceConnection{ID: connection.ID, Provider: "github", Status: connection.Status, ProviderUserID: connection.ProviderUserID, ProviderLogin: connection.ProviderLogin, CredentialGeneration: connection.CredentialGeneration, InstallUrl: installURL, LastErrorCode: connection.LastErrorCode, CreatedAt: connection.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: connection.UpdatedAt.Format(time.RFC3339Nano)}
	for value, target := range map[*time.Time]*string{connection.PendingExpiresAt: &result.PendingExpiresAt, connection.NextPollAt: &result.NextPollAt, connection.AccessExpiresAt: &result.AccessExpiresAt, connection.RefreshExpiresAt: &result.RefreshExpiresAt, connection.ConnectedAt: &result.ConnectedAt, connection.DisconnectedAt: &result.DisconnectedAt} {
		if value != nil {
			*target = value.Format(time.RFC3339Nano)
		}
	}
	return result
}

func contractSourceConnections(connections []sourceconnections.Connection, installURL string) []apicontract.SourceConnection {
	result := make([]apicontract.SourceConnection, 0, len(connections))
	for _, connection := range connections {
		result = append(result, contractSourceConnection(connection, installURL))
	}
	return result
}

func boundedQueryInteger(r *http.Request, name string, defaultValue, minimum, maximum int) (int, bool) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return defaultValue, true
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed >= minimum && parsed <= maximum
}

func setRetryAfter(w http.ResponseWriter, duration time.Duration) {
	seconds := int(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}
