package appaccess

import (
	"context"
	"encoding/json"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type hostingGatewayStartupStage uint8

const (
	hostingGatewayStartupStageUnknown hostingGatewayStartupStage = iota
	hostingGatewayStartupStageBegin
	hostingGatewayStartupStageUpgrade
	hostingGatewayStartupStageGrants
	hostingGatewayStartupStageDisables
	hostingGatewayStartupStageRebind
	hostingGatewayStartupStageRuntimeHeads
	hostingGatewayStartupStageRuntimeComponents
	hostingGatewayStartupStageActiveApprovalAuthority
	hostingGatewayStartupStageCommit
)

type hostingGatewayStartupCause uint8

const (
	hostingGatewayStartupCauseUnknown hostingGatewayStartupCause = iota
	hostingGatewayStartupCauseInvalidStoredState
	hostingGatewayStartupCauseCanceled
	hostingGatewayStartupCauseDeadlineExceeded
	hostingGatewayStartupCauseSQLiteBusy
	hostingGatewayStartupCauseSQLiteLocked
	hostingGatewayStartupCauseSQLiteError
	hostingGatewayStartupCauseSQLiteCorrupt
	hostingGatewayStartupCauseSQLiteIO
	hostingGatewayStartupCauseSQLiteOther
)

type hostingGatewayStartupDiagnosticError struct {
	stage hostingGatewayStartupStage
	cause hostingGatewayStartupCause
	err   error
}

func newHostingGatewayStartupDiagnosticError(stage hostingGatewayStartupStage, err error) error {
	return &hostingGatewayStartupDiagnosticError{
		stage: stage,
		cause: classifyHostingGatewayStartupCause(err),
		err:   err,
	}
}

func (e *hostingGatewayStartupDiagnosticError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *hostingGatewayStartupDiagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *hostingGatewayStartupDiagnosticError) MarshalJSON() ([]byte, error) {
	if e == nil {
		return json.Marshal(nil)
	}
	return json.Marshal(e.err)
}

// HostingGatewayStartupDiagnostic returns finite diagnostic metadata only for
// a direct error produced by HostingGatewayStartupSnapshot. It does not walk
// error chains or invoke caller-provided error methods.
func HostingGatewayStartupDiagnostic(err error) (stage, cause string) {
	diagnostic, ok := err.(*hostingGatewayStartupDiagnosticError)
	if !ok || diagnostic == nil {
		return "unknown", "unknown"
	}
	return hostingGatewayStartupStageName(diagnostic.stage),
		hostingGatewayStartupCauseName(diagnostic.cause)
}

func classifyHostingGatewayStartupCause(err error) hostingGatewayStartupCause {
	switch err {
	case ErrInvalidStoredState:
		return hostingGatewayStartupCauseInvalidStoredState
	case context.Canceled:
		return hostingGatewayStartupCauseCanceled
	case context.DeadlineExceeded:
		return hostingGatewayStartupCauseDeadlineExceeded
	}
	sqliteErr, ok := err.(*sqlite.Error)
	if !ok || sqliteErr == nil {
		return hostingGatewayStartupCauseUnknown
	}
	return classifyHostingGatewayStartupSQLiteCause(sqliteErr.Code() & 0xff)
}

func classifyHostingGatewayStartupSQLiteCause(code int) hostingGatewayStartupCause {
	switch code {
	case sqlite3.SQLITE_BUSY:
		return hostingGatewayStartupCauseSQLiteBusy
	case sqlite3.SQLITE_LOCKED:
		return hostingGatewayStartupCauseSQLiteLocked
	case sqlite3.SQLITE_ERROR:
		return hostingGatewayStartupCauseSQLiteError
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return hostingGatewayStartupCauseSQLiteCorrupt
	case sqlite3.SQLITE_IOERR:
		return hostingGatewayStartupCauseSQLiteIO
	default:
		return hostingGatewayStartupCauseSQLiteOther
	}
}

func hostingGatewayStartupStageName(stage hostingGatewayStartupStage) string {
	switch stage {
	case hostingGatewayStartupStageBegin:
		return "begin"
	case hostingGatewayStartupStageUpgrade:
		return "upgrade"
	case hostingGatewayStartupStageGrants:
		return "grants"
	case hostingGatewayStartupStageDisables:
		return "disables"
	case hostingGatewayStartupStageRebind:
		return "rebind"
	case hostingGatewayStartupStageRuntimeHeads:
		return "runtime_heads"
	case hostingGatewayStartupStageRuntimeComponents:
		return "runtime_components"
	case hostingGatewayStartupStageActiveApprovalAuthority:
		return "active_approval_authority"
	case hostingGatewayStartupStageCommit:
		return "commit"
	default:
		return "unknown"
	}
}

func hostingGatewayStartupCauseName(cause hostingGatewayStartupCause) string {
	switch cause {
	case hostingGatewayStartupCauseInvalidStoredState:
		return "invalid_stored_state"
	case hostingGatewayStartupCauseCanceled:
		return "canceled"
	case hostingGatewayStartupCauseDeadlineExceeded:
		return "deadline_exceeded"
	case hostingGatewayStartupCauseSQLiteBusy:
		return "sqlite_busy"
	case hostingGatewayStartupCauseSQLiteLocked:
		return "sqlite_locked"
	case hostingGatewayStartupCauseSQLiteError:
		return "sqlite_error"
	case hostingGatewayStartupCauseSQLiteCorrupt:
		return "sqlite_corrupt"
	case hostingGatewayStartupCauseSQLiteIO:
		return "sqlite_io"
	case hostingGatewayStartupCauseSQLiteOther:
		return "sqlite_other"
	default:
		return "unknown"
	}
}
