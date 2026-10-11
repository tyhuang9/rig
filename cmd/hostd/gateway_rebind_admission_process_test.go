package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/docker"
)

const (
	hostdAdmissionProcessModeEnvironment     = "RIG_TEST_HOSTD_ADMISSION_PROCESS_MODE"
	hostdAdmissionProcessManifestEnvironment = "RIG_TEST_HOSTD_ADMISSION_PROCESS_MANIFEST"
)

type hostdAdmissionProcessManifest struct {
	DataRoot         string `json:"dataRoot"`
	WorkingDirectory string `json:"workingDirectory"`
}

type hostdAdmissionProcessCommand struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	stderr   bytes.Buffer
	waitDone chan struct{}
	waitErr  error
	waitOnce sync.Once
	killOnce sync.Once
	killErr  error
}

func TestGatewayRebindAdmissionProcessSerializesHostdContenders(t *testing.T) {
	root := t.TempDir()
	directories, err := docker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := jobs.New(db)
	workerJob, _, err := service.Create("process-worker", "admission", "queued", "")
	if err != nil {
		t.Fatal(err)
	}
	recoveryJob, _, err := service.Create("process-recovery", "admission", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,
		progress_percent=20,checkpoint_json='{"phase":"running"}',started_at=?,updated_at=?
		WHERE id=? AND status='queued'`, stamp, stamp, recoveryJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("seed one recoverable job: rows=%d error=%v", rows, err)
	}
	workerEventsBefore := hostdAdmissionProcessEvents(t, service, workerJob.ID)
	recoveryEventsBefore := hostdAdmissionProcessEvents(t, service, recoveryJob.ID)

	manifestPath := filepath.Join(root, "hostd-admission-process.json")
	body, err := json.Marshal(hostdAdmissionProcessManifest{DataRoot: root, WorkingDirectory: directories.WorkingDirectory})
	if err != nil || os.WriteFile(manifestPath, body, 0o600) != nil {
		t.Fatalf("write private hostd admission manifest: %v", err)
	}
	admit, err := deploymentEffectsAdmission(db, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	startupRelease, err := admit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	startupHeld := true
	defer func() {
		if startupHeld {
			if releaseErr := startupRelease(); releaseErr != nil {
				t.Errorf("release startup admission during cleanup: %v", releaseErr)
			}
		}
	}()
	startup := hostdAdmissionStartProcess(t, "startup", manifestPath)
	hostdAdmissionReadMarker(t, ctx, startup, "STARTUP_ADMISSION_ATTEMPTED")
	hostdAdmissionReadMarker(t, ctx, startup, "STARTUP_ADMISSION_BLOCKED")
	if current, err := service.Get(recoveryJob.ID); err != nil || current.Status != string(jobs.Running) ||
		current.Attempt != 1 || !reflect.DeepEqual(recoveryEventsBefore, hostdAdmissionProcessEvents(t, service, recoveryJob.ID)) {
		t.Fatalf("startup recovery moved before admission: %#v error=%v", current, err)
	}
	startupHeld = false
	if err := startupRelease(); err != nil {
		t.Fatal(err)
	}
	if _, err := startup.stdin.Write([]byte{'c'}); err != nil {
		t.Fatal(err)
	}
	if err := startup.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	hostdAdmissionReadMarker(t, ctx, startup, "STARTUP_RECOVERY_SUCCEEDED")
	hostdAdmissionWaitSuccess(t, ctx, startup)
	recoveryEventsAfter := hostdAdmissionProcessEvents(t, service, recoveryJob.ID)
	if current, err := service.Get(recoveryJob.ID); err != nil || current.Status != string(jobs.Interrupted) ||
		current.Attempt != 1 || len(recoveryEventsAfter) != len(recoveryEventsBefore)+1 ||
		hostdAdmissionCountEvent(recoveryEventsAfter, "daemon_restarted") != 1 {
		t.Fatalf("startup recovery did not move exactly one interrupted job: %#v error=%v", current, err)
	}

	workerRelease, err := admit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workerHeld := true
	defer func() {
		if workerHeld {
			if releaseErr := workerRelease(); releaseErr != nil {
				t.Errorf("release worker admission during cleanup: %v", releaseErr)
			}
		}
	}()
	worker := hostdAdmissionStartProcess(t, "worker", manifestPath)
	hostdAdmissionReadMarker(t, ctx, worker, "WORKER_ADMISSION_ATTEMPTED")
	hostdAdmissionReadMarker(t, ctx, worker, "WORKER_ADMISSION_BLOCKED")
	if current, err := service.Get(workerJob.ID); err != nil || current.Status != string(jobs.Queued) ||
		current.Attempt != 0 || !reflect.DeepEqual(workerEventsBefore, hostdAdmissionProcessEvents(t, service, workerJob.ID)) {
		t.Fatalf("worker assigned before admission: %#v error=%v", current, err)
	}
	workerHeld = false
	if err := workerRelease(); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.stdin.Write([]byte{'c'}); err != nil {
		t.Fatal(err)
	}
	hostdAdmissionReadMarker(t, ctx, worker, "WORKER_EXECUTOR_ENTERED")
	assigned, err := service.Get(workerJob.ID)
	assignedEvents := hostdAdmissionProcessEvents(t, service, workerJob.ID)
	if err != nil || assigned.Status != string(jobs.Running) || assigned.Attempt != 1 ||
		hostdAdmissionCountEvent(assignedEvents, "job_assigned") != 1 {
		t.Fatalf("released worker did not make exactly one assignment: %#v events=%#v error=%v", assigned, assignedEvents, err)
	}
	if _, err := worker.stdin.Write([]byte{'f'}); err != nil {
		t.Fatal(err)
	}
	if err := worker.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	hostdAdmissionReadMarker(t, ctx, worker, "WORKER_SUCCEEDED")
	hostdAdmissionWaitSuccess(t, ctx, worker)
	completed, err := service.Get(workerJob.ID)
	completedEvents := hostdAdmissionProcessEvents(t, service, workerJob.ID)
	if err != nil || completed.Status != string(jobs.Succeeded) || completed.Attempt != 1 ||
		hostdAdmissionCountEvent(completedEvents, "job_assigned") != 1 {
		t.Fatalf("released worker did not complete exactly once: %#v events=%#v error=%v", completed, completedEvents, err)
	}
}

func TestGatewayRebindAdmissionProcessHostdHelper(t *testing.T) {
	mode := os.Getenv(hostdAdmissionProcessModeEnvironment)
	if mode == "" {
		return
	}
	manifest, err := hostdAdmissionReadManifest(os.Getenv(hostdAdmissionProcessManifestEnvironment))
	if err == nil {
		switch mode {
		case "startup":
			err = hostdAdmissionProcessStartup(manifest)
		case "worker":
			err = hostdAdmissionProcessWorker(manifest)
		default:
			err = errors.New("unknown hostd admission process mode")
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func hostdAdmissionProcessStartup(manifest hostdAdmissionProcessManifest) (resultErr error) {
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		return err
	}
	defer db.Close()
	admit, err := deploymentEffectsAdmission(db, manifest.WorkingDirectory)
	if err != nil {
		return err
	}
	blockedContext, cancelBlocked := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := hostdAdmissionPrintMarker("STARTUP_ADMISSION_ATTEMPTED"); err != nil {
		cancelBlocked()
		return err
	}
	blockedRelease, blockedErr := admit(blockedContext)
	cancelBlocked()
	if !errors.Is(blockedErr, context.DeadlineExceeded) || blockedRelease != nil {
		return fmt.Errorf("startup admission was not blocked: %w", blockedErr)
	}
	if err := hostdAdmissionPrintMarker("STARTUP_ADMISSION_BLOCKED"); err != nil {
		return err
	}
	if err := hostdAdmissionReadControl('c'); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	release, err := admit(ctx)
	if err != nil {
		return err
	}
	releasePending := true
	defer func() {
		if releasePending {
			resultErr = errors.Join(resultErr, release())
		}
	}()
	service := jobs.New(db)
	deploymentRepository := deployments.New(db)
	order := make([]string, 0, 4)
	releases := 0
	done, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		deployments: func(recoveryContext context.Context) error {
			if releases != 0 {
				return errors.New("deployment recovery followed admission release")
			}
			order = append(order, "deployments")
			return deploymentRepository.Recover(recoveryContext)
		},
		jobs: func() error {
			if releases != 0 {
				return errors.New("job recovery followed admission release")
			}
			order = append(order, "jobs")
			return service.RecoverInterrupted()
		},
		releaseAdmission: func() error {
			releases++
			order = append(order, "release")
			releasePending = false
			return release()
		},
	}, hostdAdmissionStartupExecutor{}, func(context.Context, jobs.Executor) error {
		if releases != 1 {
			return errors.New("normal worker started before startup admission release")
		}
		order = append(order, "worker")
		return nil
	}, nil)
	if err != nil {
		return err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return errors.New("normal worker did not start after startup recovery")
	}
	if releases != 1 || !reflect.DeepEqual(order, []string{"deployments", "jobs", "release", "worker"}) {
		return fmt.Errorf("startup recovery order=%v releases=%d", order, releases)
	}
	return hostdAdmissionPrintMarker("STARTUP_RECOVERY_SUCCEEDED")
}

type hostdAdmissionStartupExecutor struct{}

func (hostdAdmissionStartupExecutor) Execute(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	return jobs.ExecutionResult{}, nil
}

type hostdAdmissionWorkerExecutor struct {
	entered    chan<- struct{}
	finish     <-chan struct{}
	executions *atomic.Int64
}

func (e hostdAdmissionWorkerExecutor) Execute(_ context.Context, _ jobs.Job,
	reporter jobs.ProgressReporter,
) (jobs.ExecutionResult, error) {
	if e.executions.Add(1) != 1 {
		return jobs.ExecutionResult{}, errors.New("worker executor entered more than once")
	}
	if err := reporter.Report(jobs.ProgressUpdate{Status: jobs.Running, Phase: "running", Progress: 50, Code: "phase_started"}); err != nil {
		return jobs.ExecutionResult{}, err
	}
	close(e.entered)
	<-e.finish
	return jobs.ExecutionResult{}, nil
}

func hostdAdmissionProcessWorker(manifest hostdAdmissionProcessManifest) error {
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		return err
	}
	defer db.Close()
	admit, err := deploymentEffectsAdmission(db, manifest.WorkingDirectory)
	if err != nil {
		return err
	}
	service := jobs.New(db)
	blockedContext, cancelBlocked := context.WithTimeout(context.Background(), time.Second)
	blockedExecutorCalls := 0
	blockedErr := service.RunWorkerWithAdmission(blockedContext, hostdAdmissionExecutorFunc(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
		blockedExecutorCalls++
		return jobs.ExecutionResult{}, nil
	}), func(acquireContext context.Context) (func() error, error) {
		if err := hostdAdmissionPrintMarker("WORKER_ADMISSION_ATTEMPTED"); err != nil {
			return nil, err
		}
		return admit(acquireContext)
	})
	cancelBlocked()
	if blockedErr != nil || blockedExecutorCalls != 0 {
		return fmt.Errorf("blocked worker moved through admission: calls=%d error=%w", blockedExecutorCalls, blockedErr)
	}
	if err := hostdAdmissionPrintMarker("WORKER_ADMISSION_BLOCKED"); err != nil {
		return err
	}
	if err := hostdAdmissionReadControl('c'); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entered, finish := make(chan struct{}), make(chan struct{})
	var executions atomic.Int64
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorkerWithAdmission(ctx, hostdAdmissionWorkerExecutor{
			entered: entered, finish: finish, executions: &executions,
		}, admit)
	}()
	select {
	case <-entered:
		if err := hostdAdmissionPrintMarker("WORKER_EXECUTOR_ENTERED"); err != nil {
			return err
		}
	case <-ctx.Done():
		return errors.New("worker did not enter after normal admission release")
	}
	if err := hostdAdmissionReadControl('f'); err != nil {
		return err
	}
	close(finish)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status string
		if err := db.QueryRowContext(ctx, `SELECT status FROM jobs WHERE type='process-worker' AND resource_id='queued'`).Scan(&status); err != nil {
			return err
		}
		if status == string(jobs.Succeeded) {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return errors.New("worker did not persist success")
		}
	}
	cancel()
	select {
	case err := <-workerDone:
		if err != nil {
			return err
		}
	case <-time.After(20 * time.Second):
		return errors.New("successful worker was not reaped")
	}
	if executions.Load() != 1 {
		return fmt.Errorf("worker executor count=%d want=1", executions.Load())
	}
	return hostdAdmissionPrintMarker("WORKER_SUCCEEDED")
}

type hostdAdmissionExecutorFunc func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error)

func (f hostdAdmissionExecutorFunc) Execute(ctx context.Context, job jobs.Job,
	reporter jobs.ProgressReporter,
) (jobs.ExecutionResult, error) {
	return f(ctx, job, reporter)
}

func hostdAdmissionReadManifest(path string) (hostdAdmissionProcessManifest, error) {
	var manifest hostdAdmissionProcessManifest
	if !hostdAdmissionSafePath(path) || filepath.Ext(path) != ".json" {
		return manifest, errors.New("hostd admission manifest path is invalid")
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 4096 || json.Unmarshal(body, &manifest) != nil ||
		!hostdAdmissionSafePath(manifest.DataRoot) || !hostdAdmissionSafePath(manifest.WorkingDirectory) {
		return hostdAdmissionProcessManifest{}, errors.New("hostd admission manifest is invalid")
	}
	return manifest, nil
}

func hostdAdmissionSafePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func hostdAdmissionStartProcess(t *testing.T, mode, manifest string) *hostdAdmissionProcessCommand {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestGatewayRebindAdmissionProcessHostdHelper$", "-test.count=1")
	command.Env = append(os.Environ(), hostdAdmissionProcessModeEnvironment+"="+mode,
		hostdAdmissionProcessManifestEnvironment+"="+manifest)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &hostdAdmissionProcessCommand{
		command: command, stdin: stdin, stdout: bufio.NewScanner(stdout), waitDone: make(chan struct{}),
	}
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.stdin.Close()
		_ = process.kill()
		process.startWait()
		select {
		case <-process.waitDone:
		case <-time.After(10 * time.Second):
			t.Errorf("%s hostd child was not reaped during cleanup", mode)
		}
	})
	return process
}

func (p *hostdAdmissionProcessCommand) kill() error {
	p.killOnce.Do(func() { p.killErr = p.command.Process.Kill() })
	return p.killErr
}

func (p *hostdAdmissionProcessCommand) startWait() {
	p.waitOnce.Do(func() {
		go func() {
			p.waitErr = p.command.Wait()
			close(p.waitDone)
		}()
	})
}

func hostdAdmissionReadMarker(t *testing.T, ctx context.Context, process *hostdAdmissionProcessCommand, want string) {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		if process.stdout.Scan() {
			result <- process.stdout.Text()
			return
		}
		result <- ""
	}()
	select {
	case got := <-result:
		if got != want {
			_ = process.kill()
			hostdAdmissionAwaitProcess(t, process, 10*time.Second, "marker failure")
			t.Fatalf("hostd child marker=%q want=%q scan=%v stderr=%q", got, want, process.stdout.Err(), process.stderr.String())
		}
	case <-ctx.Done():
		_ = process.kill()
		hostdAdmissionAwaitProcess(t, process, 10*time.Second, "marker timeout")
		t.Fatalf("timed out waiting for hostd child marker %q", want)
	}
}

func hostdAdmissionWaitSuccess(t *testing.T, ctx context.Context, process *hostdAdmissionProcessCommand) {
	t.Helper()
	process.startWait()
	select {
	case <-process.waitDone:
		if process.waitErr != nil {
			t.Fatalf("hostd child failed: %v stderr=%q", process.waitErr, process.stderr.String())
		}
	case <-ctx.Done():
		_ = process.kill()
		hostdAdmissionAwaitProcess(t, process, 10*time.Second, "success timeout")
		t.Fatalf("timed out waiting for successful hostd child: %v stderr=%q", ctx.Err(), process.stderr.String())
	}
}

func hostdAdmissionAwaitProcess(t *testing.T, process *hostdAdmissionProcessCommand,
	timeout time.Duration, action string,
) {
	t.Helper()
	process.startWait()
	select {
	case <-process.waitDone:
	case <-time.After(timeout):
		t.Fatalf("hostd child was not reaped after %s", action)
	}
}

func hostdAdmissionReadControl(want byte) error {
	control := make([]byte, 1)
	if _, err := io.ReadFull(os.Stdin, control); err != nil || control[0] != want {
		return errors.New("hostd admission control signal is invalid")
	}
	return nil
}

func hostdAdmissionPrintMarker(marker string) error {
	if marker == "" || strings.ContainsAny(marker, "\r\n") {
		return errors.New("hostd process marker is invalid")
	}
	if _, err := fmt.Fprintln(os.Stdout, marker); err != nil {
		return err
	}
	return nil
}

func hostdAdmissionProcessEvents(t *testing.T, service *jobs.Service, jobID string) []jobs.Event {
	t.Helper()
	events, err := service.Events(jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func hostdAdmissionCountEvent(events []jobs.Event, code string) int {
	count := 0
	for _, event := range events {
		if event.Code == code {
			count++
		}
	}
	return count
}
