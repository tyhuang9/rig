package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	generatedCandidateImageInspectFormat   = `{"id":{{json .ID}},"size":{{json .Size}},"labels":{{json .Config.Labels}},"user":{{json .Config.User}},"workingDir":{{json .Config.WorkingDir}},"entrypoint":{{json .Config.Entrypoint}}}`
	generatedCandidateNetworkInspectFormat = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"internal":{{json .Internal}},"labels":{{json .Labels}}}`
)

type generatedCandidateImageInspection struct {
	ID               string            `json:"id"`
	Size             int64             `json:"size"`
	Labels           map[string]string `json:"labels"`
	User             string            `json:"user"`
	WorkingDirectory string            `json:"workingDir"`
	Entrypoint       []string          `json:"entrypoint"`
}

type generatedCandidateNetworkInspection struct {
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Scope    string            `json:"scope"`
	Internal bool              `json:"internal"`
	Labels   map[string]string `json:"labels"`
}

var errIncompleteGeneratedCandidateInspection = errors.New("incomplete generated candidate inspection")

func decodeGeneratedCandidateImageInspection(data []byte) (generatedCandidateImageInspection, error) {
	fields, err := decodeGeneratedCandidatePhysicalFields(data, []string{"id", "size", "labels", "user", "workingDir", "entrypoint"})
	if err != nil || !generatedCandidateCompleteStringMap(fields["labels"]) || !generatedCandidateCompleteStringArray(fields["entrypoint"]) {
		return generatedCandidateImageInspection{}, errors.Join(errIncompleteGeneratedCandidateInspection, err)
	}
	var inspection generatedCandidateImageInspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		return generatedCandidateImageInspection{}, errors.Join(errIncompleteGeneratedCandidateInspection, err)
	}
	return inspection, nil
}

func decodeGeneratedCandidateNetworkInspection(data []byte) (generatedCandidateNetworkInspection, error) {
	fields, err := decodeGeneratedCandidatePhysicalFields(data, []string{"name", "driver", "scope", "internal", "labels"})
	if err != nil || !generatedCandidateCompleteStringMap(fields["labels"]) {
		return generatedCandidateNetworkInspection{}, errors.Join(errIncompleteGeneratedCandidateInspection, err)
	}
	var inspection generatedCandidateNetworkInspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		return generatedCandidateNetworkInspection{}, errors.Join(errIncompleteGeneratedCandidateInspection, err)
	}
	return inspection, nil
}

func decodeGeneratedCandidatePhysicalFields(data []byte, required []string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) || generatedCandidateUniqueJSON(data) != nil {
		return nil, errIncompleteGeneratedCandidateInspection
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != len(required) {
		return nil, errors.Join(errIncompleteGeneratedCandidateInspection, err)
	}
	for _, name := range required {
		value, exists := fields[name]
		if !exists || generatedCandidateJSONNull(value) {
			return nil, errIncompleteGeneratedCandidateInspection
		}
	}
	return fields, nil
}

func generatedCandidateUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := generatedCandidateUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.Join(errIncompleteGeneratedCandidateInspection, err)
	}
	return nil
}

func generatedCandidateUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errIncompleteGeneratedCandidateInspection
			}
			seen[key] = true
			if err := generatedCandidateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.Join(errIncompleteGeneratedCandidateInspection, err)
		}
	case '[':
		for decoder.More() {
			if err := generatedCandidateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.Join(errIncompleteGeneratedCandidateInspection, err)
		}
	default:
		return errIncompleteGeneratedCandidateInspection
	}
	return nil
}

func generatedCandidateCompleteStringArray(data []byte) bool {
	if generatedCandidateJSONNull(data) {
		return true
	}
	var values []json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		var text string
		if generatedCandidateJSONNull(value) || json.Unmarshal(value, &text) != nil {
			return false
		}
	}
	return true
}

func generatedCandidateCompleteStringMap(data []byte) bool {
	if generatedCandidateJSONNull(data) {
		return true
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		var text string
		if generatedCandidateJSONNull(value) || json.Unmarshal(value, &text) != nil {
			return false
		}
	}
	return true
}

func generatedCandidateJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(deploymentexecution.ErrCanceled, err)
	}
	return nil
}

func gateDatabaseError(ctx context.Context, err error) error {
	if canceled := contextError(ctx); canceled != nil {
		return errors.Join(canceled, err)
	}
	return errors.Join(deploymentexecution.ErrUnavailable, err)
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
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

func validReleaseID(value string) bool {
	return uuid.Validate(value) == nil || lowerHex(value, 32)
}

func generatedCandidateLocalEndpoint(value string) bool {
	if value == "" || value == "npipe:////./pipe/docker_engine" {
		return true
	}
	path := strings.TrimPrefix(value, "unix://")
	return path != value && path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!pathsecurity.RejectWindowsNamespace(path)
}

func generatedCandidateDefiniteAbsence(result runtimeprocess.CommandResult, runErr error, identity string) bool {
	exited, ok := runErr.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 ||
		result.StdoutTruncated || result.StderrTruncated || len(bytes.TrimSpace(result.Stdout)) != 0 {
		return false
	}
	message := strings.TrimSpace(string(result.Stderr))
	return message == "Error response from daemon: No such container: "+identity ||
		message == "Error: No such container: "+identity ||
		message == "Error: No such object: "+identity
}

func clearGeneratedCandidateResult(result *runtimeprocess.CommandResult) {
	clear(result.Stdout)
	clear(result.Stderr)
	result.Stdout = nil
	result.Stderr = nil
}

func generatedCandidateContainsLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func validGeneratedImageText(value string, maximum int) bool {
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

func validGeneratedMigrationImageID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func validGeneratedSlotRelationship(candidateSlot, previousDeploymentID, previousSlot string) bool {
	if candidateSlot != "blue" && candidateSlot != "green" {
		return false
	}
	if (previousDeploymentID == "") != (previousSlot == "") {
		return false
	}
	if previousSlot == "" {
		return candidateSlot == "blue"
	}
	return (previousSlot == "blue" || previousSlot == "green") && previousSlot != candidateSlot
}
