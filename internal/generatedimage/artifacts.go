// Package generatedimage owns compilation and keeps compatibility aliases for
// the generated image state leaf. Runtime authority packages import the leaf
// directly, avoiding a dependency on compiler and Docker adapter code.
package generatedimage

import (
	"database/sql"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedimagestate"
)

type ArtifactState = generatedimagestate.ArtifactState
type DiagnosticCode = generatedimagestate.DiagnosticCode
type Artifact = generatedimagestate.Artifact
type BeginArtifactInput = generatedimagestate.BeginArtifactInput
type ArtifactRepository = generatedimagestate.Repository

const (
	ArtifactBuilding    = generatedimagestate.ArtifactBuilding
	ArtifactReady       = generatedimagestate.ArtifactReady
	ArtifactFailed      = generatedimagestate.ArtifactFailed
	ArtifactCancelled   = generatedimagestate.ArtifactCancelled
	ArtifactUnavailable = generatedimagestate.ArtifactUnavailable

	DiagnosticBuildFailed              = generatedimagestate.DiagnosticBuildFailed
	DiagnosticBuildTimeout             = generatedimagestate.DiagnosticBuildTimeout
	DiagnosticBuildContextInvalid      = generatedimagestate.DiagnosticBuildContextInvalid
	DiagnosticBuildContextTooLarge     = generatedimagestate.DiagnosticBuildContextTooLarge
	DiagnosticBuildOutputTruncated     = generatedimagestate.DiagnosticBuildOutputTruncated
	DiagnosticBuildDiskExhausted       = generatedimagestate.DiagnosticBuildDiskExhausted
	DiagnosticBuildCapacityExceeded    = generatedimagestate.DiagnosticBuildCapacityExceeded
	DiagnosticRuntimeUnavailable       = generatedimagestate.DiagnosticRuntimeUnavailable
	DiagnosticSourceUnavailable        = generatedimagestate.DiagnosticSourceUnavailable
	DiagnosticSourceIntegrityFailed    = generatedimagestate.DiagnosticSourceIntegrityFailed
	DiagnosticProcessTerminationFailed = generatedimagestate.DiagnosticProcessTerminationFailed
	DiagnosticDaemonRestarted          = generatedimagestate.DiagnosticDaemonRestarted
	DiagnosticInternalError            = generatedimagestate.DiagnosticInternalError
	DiagnosticBuildCancelled           = generatedimagestate.DiagnosticBuildCancelled
	DiagnosticImageUnavailable         = generatedimagestate.DiagnosticImageUnavailable
)

var (
	ErrInvalidArtifact           = generatedimagestate.ErrInvalidArtifact
	ErrArtifactNotFound          = generatedimagestate.ErrArtifactNotFound
	ErrInvalidArtifactTransition = generatedimagestate.ErrInvalidArtifactTransition
	ErrReleaseNotBuildable       = generatedimagestate.ErrReleaseNotBuildable
)

func NewArtifactRepository(db *sql.DB) *ArtifactRepository {
	return generatedimagestate.NewRepository(db)
}

func validReleaseID(value string) bool {
	return canonicalUUID(value) || lowerHex(value, 32)
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validImageContentID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}
