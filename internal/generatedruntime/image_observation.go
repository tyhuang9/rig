package generatedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"unicode/utf8"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// Image observation evidence is created only by the canonical attestor. Public
// diagnostic codes alone cannot authorize invalidating an immutable artifact.
type imageObservationError struct {
	diagnostic *Error
	cause      error
	definitive bool
}

func (e *imageObservationError) Error() string { return e.diagnostic.Error() }

func (e *imageObservationError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.diagnostic}
	}
	return []error{e.diagnostic, e.cause}
}

// IsDefinitiveImageMismatch reports a complete canonical observation of an
// absent or contradictory image. It grants no deployment authority. Joined
// failures are uncertain even when one branch carries positive evidence.
func IsDefinitiveImageMismatch(err error) bool {
	for err != nil {
		if observed, ok := err.(*imageObservationError); ok {
			return observed.definitive
		}
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return false
		}
		err = errors.Unwrap(err)
	}
	return false
}

func imageObservationFailure(ctx context.Context, code DiagnosticCode, cause error, definitive bool) error {
	if ctx.Err() != nil {
		code = networkDiagnostic(ctx, ctx.Err(), code)
		cause = errors.Join(cause, ctx.Err(), context.Cause(ctx))
		definitive = false
	}
	return &imageObservationError{diagnostic: &Error{Code: code}, cause: cause, definitive: definitive}
}

func (e *Engine) inspectImageSpec(ctx context.Context, spec ImageSpec) (imageInspection, error) {
	result, runErr := e.run(ctx, []string{"image", "inspect", "--format", imageInspectFormat, spec.ImageContentID}, e.options.CommandTimeout)
	defer clearResult(&result)
	if runErr != nil || ctx.Err() != nil || result.StdoutTruncated || result.StderrTruncated {
		code := e.commandDiagnostic(ctx, result, runErr, DiagnosticImageUnavailable)
		// Uncertainty takes priority over any missing-image wording. In
		// particular, a completed exit is different from a start/transport or
		// process-tree termination error, even if stderr looks familiar.
		definitive := ctx.Err() == nil && !errors.Is(runErr, context.Canceled) &&
			!errors.Is(runErr, context.DeadlineExceeded) && !errors.Is(runErr, runtimeprocess.ErrTerminationFailed) &&
			!result.StdoutTruncated && !result.StderrTruncated && definiteImageAbsence(result, runErr, spec.ImageContentID)
		return imageInspection{}, imageObservationFailure(ctx, code, runErr, definitive)
	}
	image, decodeErr := decodeImageObservation(result.Stdout)
	if decodeErr != nil {
		return imageInspection{}, imageObservationFailure(ctx, DiagnosticImageDriftDetected, decodeErr, false)
	}
	expected := map[string]string{
		"io.rig.managed":     "generated-image",
		"io.rig.application": spec.AppID,
		"io.rig.release":     spec.ReleaseID,
		"io.rig.artifact":    spec.ArtifactID,
		"io.rig.plan":        spec.DeploymentPlanRevisionID,
		"io.rig.component":   spec.ComponentName,
		"io.rig.role":        spec.Role,
		"io.rig.definition":  spec.BuildDefinitionDigest,
	}
	mismatch := image.ID != spec.ImageContentID || image.Size < 1 || !containsLabels(image.Labels, expected) ||
		image.User != "node" || image.WorkingDirectory != "/workspace" || len(image.Entrypoint) != 1 ||
		image.Entrypoint[0] != "/usr/local/bin/rig-entrypoint"
	// This check deliberately follows parsing and comparison, including on
	// the successful path. An observation cannot outlive its caller.
	if ctx.Err() != nil {
		return imageInspection{}, imageObservationFailure(ctx, DiagnosticCancelled, nil, false)
	}
	if mismatch {
		return imageInspection{}, imageObservationFailure(ctx, DiagnosticImageDriftDetected, nil, true)
	}
	return image, nil
}

func definiteImageAbsence(result runtimeprocess.CommandResult, runErr error, contentID string) bool {
	exited, ok := runErr.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 || len(bytes.TrimSpace(result.Stdout)) != 0 {
		return false
	}
	message := strings.TrimSpace(string(result.Stderr))
	return message == "Error response from daemon: No such image: "+contentID ||
		message == "Error: No such image: "+contentID
}

var errIncompleteImageObservation = errors.New("incomplete generated image observation")

func decodeImageObservation(data []byte) (imageInspection, error) {
	if !utf8.Valid(data) {
		return imageInspection{}, errIncompleteImageObservation
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return imageInspection{}, errors.Join(errIncompleteImageObservation, err)
	}
	fields := make(map[string]bool, 6)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return imageInspection{}, err
		}
		key, ok := token.(string)
		if !ok || fields[key] {
			return imageInspection{}, errIncompleteImageObservation
		}
		switch key {
		case "id", "size", "labels", "user", "workingDir", "entrypoint":
		default:
			return imageInspection{}, errIncompleteImageObservation
		}
		fields[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return imageInspection{}, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return imageInspection{}, errIncompleteImageObservation
		}
		if key == "labels" && !completeImageLabels(value) {
			return imageInspection{}, errIncompleteImageObservation
		}
		if key == "entrypoint" {
			var entries []*string
			if err := json.Unmarshal(value, &entries); err != nil {
				return imageInspection{}, err
			}
			for _, entry := range entries {
				if entry == nil {
					return imageInspection{}, errIncompleteImageObservation
				}
			}
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(fields) != 6 {
		return imageInspection{}, errors.Join(errIncompleteImageObservation, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return imageInspection{}, errors.Join(errIncompleteImageObservation, err)
	}
	var image imageInspection
	if err := json.Unmarshal(data, &image); err != nil {
		return imageInspection{}, err
	}
	if !validImageID(image.ID) || image.Size < 0 {
		return imageInspection{}, errIncompleteImageObservation
	}
	return image, nil
}

func completeImageLabels(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return false
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		value, err := decoder.Token()
		if err != nil {
			return false
		}
		if _, ok := value.(string); !ok {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}
