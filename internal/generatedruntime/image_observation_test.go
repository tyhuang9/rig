package generatedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedImageObservationEvidence(t *testing.T) {
	if os.Getenv("RIG_TEST_IMAGE_OBSERVATION_EXIT") == "1" {
		os.Exit(1)
	}
	helperContext, cancelHelper := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelHelper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.CommandContext(helperContext, executable, "-test.run=^TestGeneratedImageObservationEvidence$")
	helper.Env = append(os.Environ(), "RIG_TEST_IMAGE_OBSERVATION_EXIT=1")
	exitErr, ok := helper.Run().(*exec.ExitError)
	if !ok || exitErr.ProcessState == nil || exitErr.ExitCode() != 1 {
		t.Fatal("test helper did not provide a completed exit-one process")
	}
	spec := candidateSpec()
	imageSpec := ImageSpec{
		AppID: spec.AppID, ReleaseID: spec.ReleaseID, ArtifactID: spec.ArtifactID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, ImageContentID: spec.ImageContentID,
		ComponentName: spec.ComponentName, Role: spec.Role, BuildDefinitionDigest: spec.BuildDefinitionDigest,
	}
	valid, err := json.Marshal(validImageInspection(spec))
	if err != nil {
		t.Fatal(err)
	}
	changeField := func(key string, value json.RawMessage, remove bool) []byte {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(valid, &fields); err != nil {
			t.Fatal(err)
		}
		if remove {
			delete(fields, key)
		} else {
			fields[key] = value
		}
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	canary := errors.New("image-observation-secret-canary")
	missing := "Error response from daemon: No such image: " + spec.ImageContentID
	type observationCase struct {
		name         string
		stdout       []byte
		stderr       string
		cause        error
		stdoutCut    bool
		stderrCut    bool
		cancel       bool
		wantCode     DiagnosticCode
		wantDefinite bool
	}
	cases := []observationCase{
		{name: "valid exact image", stdout: valid},
		{name: "exact missing daemon response", stderr: missing, cause: exitErr, wantCode: DiagnosticImageUnavailable, wantDefinite: true},
		{name: "exact missing CLI response", stderr: "Error: No such image: " + spec.ImageContentID, cause: exitErr, wantCode: DiagnosticImageUnavailable, wantDefinite: true},
		{name: "generic nonzero", stderr: missing, cause: canary, wantCode: DiagnosticImageUnavailable},
		{name: "transport", stderr: "Cannot connect to the Docker daemon; " + missing, cause: canary, wantCode: DiagnosticRuntimeUnavailable},
		{name: "wrong missing identity", stderr: "Error: No such image: sha256:" + strings.Repeat("f", 64), cause: exitErr, wantCode: DiagnosticImageUnavailable},
		{name: "partial missing wording", stderr: "No such image", cause: exitErr, wantCode: DiagnosticImageUnavailable},
		{name: "additional daemon error", stderr: missing + "\ntransport failed", cause: exitErr, wantCode: DiagnosticImageUnavailable},
		{name: "untrusted wrapped exit", stderr: missing, cause: fmt.Errorf("transport: %w", exitErr), wantCode: DiagnosticImageUnavailable},
		{name: "missing with stdout", stdout: valid, stderr: missing, cause: exitErr, wantCode: DiagnosticImageUnavailable},
		{name: "missing with cancelled context", stderr: missing, cause: canary, cancel: true, wantCode: DiagnosticCancelled},
		{name: "missing with cancellation cause", stderr: missing, cause: errors.Join(exitErr, context.Canceled), wantCode: DiagnosticCancelled},
		{name: "missing with deadline cause", stderr: missing, cause: errors.Join(exitErr, context.DeadlineExceeded), wantCode: DiagnosticRuntimeTimeout},
		{name: "missing with termination uncertainty", stderr: missing, cause: errors.Join(exitErr, runtimeprocess.ErrTerminationFailed), wantCode: DiagnosticProcessTerminationFailed},
		{name: "missing with stdout truncation", stderr: missing, cause: exitErr, stdoutCut: true, wantCode: DiagnosticRuntimeOutputTruncated},
		{name: "missing with stderr truncation", stderr: missing, cause: exitErr, stderrCut: true, wantCode: DiagnosticRuntimeOutputTruncated},
		{name: "valid result with truncation", stdout: valid, stdoutCut: true, wantCode: DiagnosticRuntimeOutputTruncated},
		{name: "malformed JSON", stdout: []byte(`{"id":`), wantCode: DiagnosticImageDriftDetected},
		{name: "trailing document", stdout: append(append([]byte(nil), valid...), []byte(` {}`)...), wantCode: DiagnosticImageDriftDetected},
		{name: "duplicate identity", stdout: append([]byte(`{"id":"sha256:`+strings.Repeat("f", 64)+`",`), valid[1:]...), wantCode: DiagnosticImageDriftDetected},
		{name: "invalid image identity", stdout: changeField("id", json.RawMessage(`"truncated"`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "negative size", stdout: changeField("size", json.RawMessage(`-1`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "null label value", stdout: changeField("labels", json.RawMessage(`{"io.rig.managed":null}`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "wrong label value type", stdout: changeField("labels", json.RawMessage(`{"io.rig.managed":true}`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "duplicate label key", stdout: changeField("labels", json.RawMessage(`{"io.rig.managed":"generated-image","io.rig.managed":"other"}`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "null entrypoint element", stdout: changeField("entrypoint", json.RawMessage(`[null]`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "wrong entrypoint element type", stdout: changeField("entrypoint", json.RawMessage(`[true]`), false), wantCode: DiagnosticImageDriftDetected},
		{name: "valid wrong identity", stdout: changeField("id", json.RawMessage(`"sha256:`+strings.Repeat("f", 64)+`"`), false), wantCode: DiagnosticImageDriftDetected, wantDefinite: true},
		{name: "valid zero size", stdout: changeField("size", json.RawMessage(`0`), false), wantCode: DiagnosticImageDriftDetected, wantDefinite: true},
		{name: "valid empty labels", stdout: changeField("labels", json.RawMessage(`{}`), false), wantCode: DiagnosticImageDriftDetected, wantDefinite: true},
		{name: "valid root user", stdout: changeField("user", json.RawMessage(`"root"`), false), wantCode: DiagnosticImageDriftDetected, wantDefinite: true},
		{name: "valid wrong working directory", stdout: changeField("workingDir", json.RawMessage(`"/"`), false), wantCode: DiagnosticImageDriftDetected, wantDefinite: true},
		{name: "valid empty entrypoint", stdout: changeField("entrypoint", json.RawMessage(`[]`), false), wantCode: DiagnosticImageDriftDetected, wantDefinite: true},
	}
	for _, field := range []string{"id", "size", "labels", "user", "workingDir", "entrypoint"} {
		cases = append(cases,
			observationCase{name: "missing field " + field, stdout: changeField(field, nil, true), wantCode: DiagnosticImageDriftDetected},
			observationCase{name: "null field " + field, stdout: changeField(field, json.RawMessage(`null`), false), wantCode: DiagnosticImageDriftDetected},
			observationCase{name: "wrong type " + field, stdout: changeField(field, json.RawMessage(`true`), false), wantCode: DiagnosticImageDriftDetected},
		)
	}
	engine, runner, _, _ := newRuntimeTestEngine(t, nil)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			runner.requests = nil
			result := runtimeprocess.CommandResult{Stdout: append([]byte(nil), test.stdout...), Stderr: []byte(test.stderr), StdoutTruncated: test.stdoutCut, StderrTruncated: test.stderrCut}
			runner.run = func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				if commandKind(request.Args) != "image inspect" || request.Args[len(request.Args)-1] != spec.ImageContentID {
					t.Fatal("image validation issued a different command or identity")
				}
				if test.cancel {
					cancel(canary)
				}
				return runtimeRequestResult{result: result, err: test.cause}
			}
			err := engine.ValidateImage(ctx, imageSpec)
			if test.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !IsCode(err, test.wantCode) {
				t.Fatalf("diagnostic=%v want=%s", err, test.wantCode)
			}
			if IsDefinitiveImageMismatch(err) != test.wantDefinite {
				t.Fatalf("definitive evidence=%t want=%t", IsDefinitiveImageMismatch(err), test.wantDefinite)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatal("original process cause was lost")
			}
			if test.cancel && (!errors.Is(err, context.Canceled) || !errors.Is(err, canary)) {
				t.Fatal("context cancellation or its cause was lost")
			}
			if err != nil && strings.Contains(err.Error(), canary.Error()) {
				t.Fatal("diagnostic exposed a raw process/context cause")
			}
			if len(runner.requests) != 1 {
				t.Fatal("image observation issued an additional command")
			}
			for _, stream := range [][]byte{result.Stdout, result.Stderr} {
				for _, value := range stream {
					if value != 0 {
						t.Fatal("process output was not cleared")
					}
				}
			}
			if test.wantDefinite && (!IsDefinitiveImageMismatch(fmt.Errorf("observation: %w", err)) || IsDefinitiveImageMismatch(errors.Join(err, canary))) {
				t.Fatal("single wrapping or joined uncertainty classification was incorrect")
			}
		})
	}
	for _, code := range []DiagnosticCode{DiagnosticImageUnavailable, DiagnosticImageDriftDetected} {
		if IsDefinitiveImageMismatch(&Error{Code: code}) {
			t.Fatal("caller-constructed diagnostic became physical evidence")
		}
	}
}

// This controlled context becomes cancelled at the final observation check.
// It exercises cancellation after a valid result has entered the attestor,
// without a timing race or a production callback into the decoder.
type finalImageObservationContext struct {
	context.Context
	checks atomic.Int32
	done   chan struct{}
	once   sync.Once
}

func (c *finalImageObservationContext) Done() <-chan struct{} { return c.done }

func (c *finalImageObservationContext) Err() error {
	if c.checks.Add(1) < 2 {
		return nil
	}
	c.once.Do(func() { close(c.done) })
	return context.Canceled
}

func TestGeneratedImageObservationCancellationAtFinalCheck(t *testing.T) {
	spec := candidateSpec()
	engine, _, _, _ := newRuntimeTestEngine(t, func(runtimeprocess.CommandRequest) runtimeRequestResult {
		return runtimeJSON(validImageInspection(spec))
	})
	ctx := &finalImageObservationContext{Context: context.Background(), done: make(chan struct{})}
	err := engine.ValidateImage(ctx, ImageSpec{
		AppID: spec.AppID, ReleaseID: spec.ReleaseID, ArtifactID: spec.ArtifactID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, ImageContentID: spec.ImageContentID,
		ComponentName: spec.ComponentName, Role: spec.Role, BuildDefinitionDigest: spec.BuildDefinitionDigest,
	})
	if !errors.Is(err, context.Canceled) || !IsCode(err, DiagnosticCancelled) || IsDefinitiveImageMismatch(err) {
		t.Fatalf("cancelled final observation accepted: %v", err)
	}
}
