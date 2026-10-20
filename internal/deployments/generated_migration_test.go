package deployments

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"modernc.org/sqlite"
)

type generatedMigrationFixture struct {
	base          repositoryFixture
	guarded       *guardedRepositoryFixture
	repository    *Repository
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	stager        *generatedMigrationStagerFake
	docker        *generatedMigrationDockerFake
	request       GeneratedMigrationRequest
	plan          deploymentplans.Plan
	claim         deploymentexecution.Claim
}

type embeddedGeneratedMigrationInvocation struct{ GeneratedMigrationInvocation }

type generatedMigrationStagerFake struct {
	mu        sync.Mutex
	path      string
	operation string
	attempt   int
	contents  []byte
	cleaned   bool
}

func (s *generatedMigrationStagerFake) StageGeneratedMigration(operationID string, attempt int, contents []byte) (GeneratedMigrationEnvironmentLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operation = operationID
	s.attempt = attempt
	s.contents = append([]byte(nil), contents...)
	clear(contents)
	return &generatedMigrationLeaseFake{stager: s, path: s.path}, nil
}

type generatedMigrationLeaseFake struct {
	stager *generatedMigrationStagerFake
	path   string
}

type generatedMigrationCommitFault struct {
	armed              atomic.Bool
	trackTransitions   atomic.Bool
	transitionAttempts atomic.Int32
	cause              error
}

type generatedMigrationCommitConnector struct {
	dsn   string
	fault *generatedMigrationCommitFault
}

func (c *generatedMigrationCommitConnector) Connect(context.Context) (driver.Conn, error) {
	connection, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &generatedMigrationCommitConnection{Conn: connection, fault: c.fault}, nil
}

func (c *generatedMigrationCommitConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type generatedMigrationCommitConnection struct {
	driver.Conn
	fault *generatedMigrationCommitFault
}

func (c *generatedMigrationCommitConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &generatedMigrationCommitTransaction{Tx: tx, fault: c.fault}, nil
}

func (c *generatedMigrationCommitConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *generatedMigrationCommitConnection) ExecContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Result, error) {
	if c.fault.trackTransitions.Load() {
		normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
		if strings.HasPrefix(normalized, "update generated_runtime_deployments set migration_state=") {
			c.fault.transitionAttempts.Add(1)
		}
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, values)
}

func (c *generatedMigrationCommitConnection) QueryContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, values)
}

func (c *generatedMigrationCommitConnection) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *generatedMigrationCommitConnection) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *generatedMigrationCommitConnection) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

type generatedMigrationCommitTransaction struct {
	driver.Tx
	fault *generatedMigrationCommitFault
}

func (t *generatedMigrationCommitTransaction) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	if t.fault.armed.CompareAndSwap(true, false) {
		return t.fault.cause
	}
	return nil
}

func openGeneratedMigrationCommitDatabase(t *testing.T, fault *generatedMigrationCommitFault) *sql.DB {
	t.Helper()
	root := t.TempDir()
	db := sql.OpenDB(&generatedMigrationCommitConnector{dsn: filepath.Join(root, "control.db"), fault: fault})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func (l *generatedMigrationLeaseFake) Path() string { return l.path }
func (l *generatedMigrationLeaseFake) Cleanup() error {
	l.stager.mu.Lock()
	defer l.stager.mu.Unlock()
	l.stager.cleaned = true
	return nil
}

type generatedMigrationDockerFake struct {
	mu                        sync.Mutex
	requests                  []runtimeprocess.CommandRequest
	id                        string
	name                      string
	image                     string
	user                      string
	workdir                   string
	command                   string
	network                   string
	labels                    map[string]string
	status                    string
	startedAt                 string
	exitCode                  int
	removed                   bool
	loseCreateAcknowledgement bool
	loseStartAcknowledgement  bool
	exitBeforeStartInspection bool
	absenceErr                error
	removeErr                 error
	inspectCount              int
	imageObservation          generatedMigrationImageInspection
	networkObservation        generatedMigrationNetworkInspection
	afterInspect              func(int, runtimeprocess.CommandRequest)
	inspectOutput             func(int, generatedMigrationInspection) ([]byte, error)
	block                     chan struct{}
	entered                   chan struct{}
}

func (d *generatedMigrationDockerFake) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	d.mu.Lock()
	d.requests = append(d.requests, request)
	kind := strings.Join(request.Args[:min(2, len(request.Args))], " ")
	if kind == "container start" && d.block != nil {
		entered, block := d.entered, d.block
		d.mu.Unlock()
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-block:
		case <-ctx.Done():
			return runtimeprocess.CommandResult{}, ctx.Err()
		}
		d.mu.Lock()
	}
	switch kind {
	case "image inspect":
		body, err := json.Marshal(d.imageObservation)
		d.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: body}, err
	case "network inspect":
		body, err := json.Marshal(d.networkObservation)
		d.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: body}, err
	case "container create":
		d.captureCreate(request.Args)
		d.status = "created"
		id := d.id
		if d.loseCreateAcknowledgement {
			d.mu.Unlock()
			return runtimeprocess.CommandResult{}, errors.New("lost create acknowledgement")
		}
		d.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: []byte(id + "\n")}, nil
	case "container start":
		d.status = "running"
		d.startedAt = "2026-10-20T12:00:00.123456789Z"
		if d.exitBeforeStartInspection {
			d.status = "exited"
		}
		id := d.id
		if d.loseStartAcknowledgement {
			d.mu.Unlock()
			return runtimeprocess.CommandResult{}, errors.New("lost start acknowledgement")
		}
		d.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: []byte(id + "\n")}, nil
	case "container wait":
		d.status = "exited"
		exitCode := d.exitCode
		d.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: []byte(strconv.Itoa(exitCode) + "\n")}, nil
	case "container rm":
		d.removed = true
		removeErr := d.removeErr
		id := d.id
		d.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: []byte(id + "\n")}, removeErr
	case "container inspect":
		d.inspectCount++
		index := d.inspectCount
		hook := d.afterInspect
		if d.removed || d.status == "" {
			result := runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + request.Args[len(request.Args)-1])}
			err := d.absenceErr
			d.mu.Unlock()
			if hook != nil {
				hook(index, request)
			}
			return result, err
		}
		inspection := d.inspection()
		output := d.inspectOutput
		var body []byte
		var err error
		if output == nil {
			body, err = json.Marshal(inspection)
		} else {
			body, err = output(index, inspection)
		}
		d.mu.Unlock()
		if hook != nil {
			hook(index, request)
		}
		return runtimeprocess.CommandResult{Stdout: body}, err
	default:
		d.mu.Unlock()
		return runtimeprocess.CommandResult{}, errors.New("unexpected Docker command")
	}
}

func (d *generatedMigrationDockerFake) captureCreate(args []string) {
	d.name = generatedMigrationArg(args, "--name")
	d.user = generatedMigrationArg(args, "--user")
	d.workdir = generatedMigrationArg(args, "--workdir")
	d.network = generatedMigrationArg(args, "--network")
	d.labels = map[string]string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--label" {
			key, value, ok := strings.Cut(args[index+1], "=")
			if ok {
				d.labels[key] = value
			}
		}
	}
	d.image = args[len(args)-4]
	d.command = args[len(args)-1]
}

func (d *generatedMigrationDockerFake) inspection() generatedMigrationInspection {
	value := generatedMigrationInspection{
		ID: d.id, Name: "/" + d.name, Image: d.image, ConfigImage: d.image, User: d.user, WorkingDirectory: d.workdir,
		Entrypoint: []string{"/usr/local/bin/rig-entrypoint"}, Command: []string{"/bin/sh", "-lc", d.command}, Labels: cloneStringMap(d.labels),
		Memory: 536870912, MemorySwap: 536870912, CPUPeriod: 100000, CPUQuota: 100000, PidsLimit: 256,
		Ulimits: []generatedMigrationUlimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, Init: true, NetworkMode: d.network,
		ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges"},
		Tmpfs:   map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864,mode=1777"},
		LogType: "json-file", LogConfig: map[string]string{"max-size": "1m", "max-file": "1"}, Restart: "no",
		Status: d.status, StartedAt: "0001-01-01T00:00:00Z",
	}
	if d.status == "running" {
		value.Running = true
		value.StartedAt = d.startedAt
		value.Networks = map[string]json.RawMessage{d.network: json.RawMessage(`{}`)}
	}
	if d.status == "exited" {
		value.ExitCode = d.exitCode
		value.StartedAt = d.startedAt
		value.Networks = map[string]json.RawMessage{d.network: json.RawMessage(`{}`)}
	}
	return value
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func containsArgument(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func generatedMigrationArg(args []string, name string) string {
	for index := range args {
		if args[index] == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func generatedMigrationRequestByKind(t *testing.T, requests []runtimeprocess.CommandRequest, kind string) runtimeprocess.CommandRequest {
	t.Helper()
	for _, request := range requests {
		if strings.HasPrefix(strings.Join(request.Args, " "), kind+" ") || strings.Join(request.Args, " ") == kind {
			return request
		}
	}
	t.Fatalf("Docker request %q was not observed", kind)
	return runtimeprocess.CommandRequest{}
}

func TestGeneratedMigrationExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_GENERATED_MIGRATION_EXIT") == "1" {
		os.Exit(1)
	}
}

func generatedMigrationCompletedExitOne(t *testing.T) *exec.ExitError {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestGeneratedMigrationExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_GENERATED_MIGRATION_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected completed exit one, got %T %v", err, err)
	}
	return exited
}

func TestGeneratedMigrationRepositoryRequiresExactConcreteComposition(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	if _, err := fixture.repository.PinGeneratedMigrationCommand(nil); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("nil invocation accepted: %v", err)
	}
	if _, err := New(fixture.base.repository.db).PinGeneratedMigrationCommand(&generatedMigrationInvocationPermit{}); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("unconfigured repository accepted command: %v", err)
	}
	if _, err := fixture.repository.PinGeneratedMigrationCommand(&generatedMigrationInvocationPermit{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("zero invocation accepted: %v", err)
	}
	if _, err := fixture.repository.PinGeneratedMigrationCommand(&embeddedGeneratedMigrationInvocation{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("embedded nil invocation accepted: %v", err)
	}
	foreign, err := database.Open(filepath.Join(t.TempDir(), "foreign"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	foreignPlans, err := deploymentplans.New(foreign, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithExecutionGuardAndGeneratedMigration(fixture.base.repository.db, fixture.guarded.guard, foreignPlans, fixture.configuration, fixture.stager, fixture.docker, fixture.repository.migrationOptions); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("foreign plan store accepted: %v", err)
	}
	var nilRunner *generatedMigrationDockerFake
	if _, err := NewWithExecutionGuardAndGeneratedMigration(fixture.base.repository.db, fixture.guarded.guard, fixture.plans, fixture.configuration, fixture.stager, nilRunner, fixture.repository.migrationOptions); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("typed nil process accepted: %v", err)
	}
}

func TestGeneratedMigrationInspectionProjectionIsValidAndSecretFree(t *testing.T) {
	projection, err := template.New("inspect").Funcs(template.FuncMap{"json": func(value any) (string, error) {
		body, err := json.Marshal(value)
		return string(body), err
	}}).Parse(generatedMigrationInspectFormat)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"ID": "id", "Name": "/name", "Image": "image",
		"State":           map[string]any{"Status": "created", "Running": false, "Paused": false, "Restarting": false, "Dead": false, "ExitCode": 0, "StartedAt": "0001-01-01T00:00:00Z"},
		"Config":          map[string]any{"Image": "image", "User": "node", "WorkingDir": "/workspace", "Entrypoint": []string{"entry"}, "Cmd": []string{"cmd"}, "Labels": map[string]string{}, "Volumes": map[string]any{}, "Env": []string{"DATABASE_URL=sentinel-secret"}},
		"HostConfig":      map[string]any{"Memory": int64(1), "MemorySwap": int64(1), "CpuPeriod": int64(1), "CpuQuota": int64(1), "PidsLimit": int64(1), "Ulimits": []any{}, "Init": true, "NetworkMode": "network", "ReadonlyRootfs": true, "Privileged": false, "CapAdd": []string{}, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Binds": []string{}, "Mounts": []any{}, "PortBindings": map[string]any{}, "Tmpfs": map[string]string{}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{}}, "RestartPolicy": map[string]string{"Name": "no"}},
		"NetworkSettings": nil, "Mounts": []any{},
	}
	var rendered strings.Builder
	if err := projection.Execute(&rendered, input); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), "sentinel-secret") || strings.Contains(generatedMigrationInspectFormat, ".Config.Env") {
		t.Fatal("inspection projection exposed environment values")
	}
	decoded, err := decodeGeneratedMigrationInspection([]byte(rendered.String()))
	if err != nil {
		t.Fatalf("inspection projection is not JSON: %v", err)
	}
	if decoded.Networks != nil {
		t.Fatal("missing network state was not rendered as null")
	}
}

func TestGeneratedMigrationInspectionRequiresCompleteUniqueTypedProjection(t *testing.T) {
	valid := generatedMigrationInspection{
		ID: "id", Name: "/name", Image: "image", ConfigImage: "image", User: "node", WorkingDirectory: "/workspace",
		Entrypoint: []string{"entry"}, Command: []string{"command"}, Labels: map[string]string{"x": "y"}, Volumes: map[string]json.RawMessage{},
		Memory: 1, MemorySwap: 1, CPUPeriod: 1, CPUQuota: 1, PidsLimit: 1,
		Ulimits: []generatedMigrationUlimit{{Name: "nofile", Soft: 1, Hard: 1}}, Init: true, NetworkMode: "network", ReadonlyRootfs: true,
		CapAdd: []string{}, CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges"}, Binds: []string{}, HostMounts: []json.RawMessage{},
		PortBindings: map[string]json.RawMessage{}, Tmpfs: map[string]string{"/tmp": "rw"}, LogType: "json-file", LogConfig: map[string]string{"max-size": "1m"}, Restart: "no",
		Status: "exited", ExitCode: 0, StartedAt: "2026-10-20T12:00:00Z", Networks: map[string]json.RawMessage{"network": json.RawMessage(`{}`)},
		Mounts: []generatedMigrationMount{{Type: "tmpfs", Destination: "/tmp"}},
	}
	body, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGeneratedMigrationInspection(body); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range generatedMigrationInspectionFields {
		t.Run("missing_"+name, func(t *testing.T) {
			copy := cloneGeneratedMigrationRawFields(fields)
			delete(copy, name)
			candidate, _ := json.Marshal(copy)
			if _, err := decodeGeneratedMigrationInspection(candidate); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
				t.Fatalf("missing %s accepted: %v", name, err)
			}
		})
	}
	for _, name := range generatedMigrationInspectionScalarFields {
		t.Run("null_"+name, func(t *testing.T) {
			copy := cloneGeneratedMigrationRawFields(fields)
			copy[name] = json.RawMessage("null")
			candidate, _ := json.Marshal(copy)
			if _, err := decodeGeneratedMigrationInspection(candidate); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
				t.Fatalf("null %s accepted: %v", name, err)
			}
		})
	}
	dockerMountFields := cloneGeneratedMigrationRawFields(fields)
	dockerMountFields["mounts"] = json.RawMessage(`[{"Type":"tmpfs","Source":"","Destination":"/tmp","Mode":"","RW":true,"Propagation":""}]`)
	dockerMountBody, _ := json.Marshal(dockerMountFields)
	if _, err := decodeGeneratedMigrationInspection(dockerMountBody); err != nil {
		t.Fatalf("legitimate Docker mount fields rejected: %v", err)
	}
	missingMountSource := cloneGeneratedMigrationRawFields(fields)
	missingMountSource["mounts"] = json.RawMessage(`[{"Type":"tmpfs","Destination":"/tmp","Extra":"x"}]`)
	missingMountBody, _ := json.Marshal(missingMountSource)
	if _, err := decodeGeneratedMigrationInspection(missingMountBody); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
		t.Fatalf("mount without Source accepted: %v", err)
	}
	for _, name := range []string{"entrypoint", "cmd", "labels", "volumes", "ulimits", "capAdd", "capDrop", "securityOpt", "binds", "hostMounts", "portBindings", "tmpfs", "logConfig", "networks", "mounts"} {
		fields[name] = json.RawMessage("null")
	}
	nullCollections, _ := json.Marshal(fields)
	if _, err := decodeGeneratedMigrationInspection(nullCollections); err != nil {
		t.Fatalf("legitimate Docker null collections rejected: %v", err)
	}
	for name, candidate := range map[string][]byte{
		"flat_duplicate":   []byte(strings.Replace(string(body), `{"id":`, `{"id":"shadow","id":`, 1)),
		"nested_duplicate": []byte(strings.Replace(string(body), `"labels":{"x":"y"}`, `"labels":{"x":"y","x":"z"}`, 1)),
		"array_duplicate":  []byte(strings.Replace(string(body), `"Name":"nofile"`, `"Name":"shadow","Name":"nofile"`, 1)),
		"malformed":        append(append([]byte(nil), body...), '{'),
		"trailing":         append(append([]byte(nil), body...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGeneratedMigrationInspection(candidate); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
				t.Fatalf("ambiguous projection accepted: %v", err)
			}
		})
	}
}

func TestGeneratedMigrationPhysicalProjectionRequiresCompleteUniqueTypedFields(t *testing.T) {
	image := generatedMigrationImageInspection{
		ID: "sha256:" + strings.Repeat("1", 64), Size: 1, Labels: map[string]string{"x": "y"}, User: "node",
		WorkingDirectory: "/workspace", Entrypoint: []string{"/usr/local/bin/rig-entrypoint"},
	}
	imageBody, err := json.Marshal(image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGeneratedMigrationImageInspection(imageBody); err != nil {
		t.Fatalf("valid image projection: %v", err)
	}
	assertGeneratedMigrationPhysicalProjectionRejections(t, imageBody, []string{"id", "size", "labels", "user", "workingDir", "entrypoint"}, decodeGeneratedMigrationImageForTest)

	network := generatedMigrationNetworkInspection{Name: "network", Driver: "bridge", Scope: "local", Labels: map[string]string{"x": "y"}}
	networkBody, err := json.Marshal(network)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGeneratedMigrationNetworkInspection(networkBody); err != nil {
		t.Fatalf("valid network projection: %v", err)
	}
	assertGeneratedMigrationPhysicalProjectionRejections(t, networkBody, []string{"name", "driver", "scope", "internal", "labels"}, decodeGeneratedMigrationNetworkForTest)
}

func assertGeneratedMigrationPhysicalProjectionRejections(t *testing.T, body []byte, fields []string, decode func([]byte) error) {
	t.Helper()
	var values map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		t.Run("missing_"+field, func(t *testing.T) {
			candidate := cloneGeneratedMigrationRawFields(values)
			delete(candidate, field)
			encoded, _ := json.Marshal(candidate)
			if err := decode(encoded); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
				t.Fatalf("missing %s accepted: %v", field, err)
			}
		})
		t.Run("null_"+field, func(t *testing.T) {
			candidate := cloneGeneratedMigrationRawFields(values)
			candidate[field] = json.RawMessage("null")
			encoded, _ := json.Marshal(candidate)
			if err := decode(encoded); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
				t.Fatalf("null %s accepted: %v", field, err)
			}
		})
	}
	flatDuplicate := []byte(strings.Replace(string(body), "{", `{"shadow":false,"shadow":true,`, 1))
	if err := decode(flatDuplicate); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
		t.Fatalf("flat duplicate accepted: %v", err)
	}
	nestedDuplicate := []byte(strings.Replace(string(body), `"labels":{"x":"y"}`, `"labels":{"x":"y","x":"z"}`, 1))
	if err := decode(nestedDuplicate); !errors.Is(err, errIncompleteGeneratedMigrationInspection) {
		t.Fatalf("nested duplicate accepted: %v", err)
	}
}

func decodeGeneratedMigrationImageForTest(data []byte) error {
	_, err := decodeGeneratedMigrationImageInspection(data)
	return err
}

func decodeGeneratedMigrationNetworkForTest(data []byte) error {
	_, err := decodeGeneratedMigrationNetworkInspection(data)
	return err
}

func cloneGeneratedMigrationRawFields(input map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(input))
	for name, value := range input {
		result[name] = append(json.RawMessage(nil), value...)
	}
	return result
}

func TestGeneratedMigrationAbsenceRequiresCanonicalCompletedExitOne(t *testing.T) {
	exited := generatedMigrationCompletedExitOne(t)
	identity := "rig-migration-exact"
	canonical := runtimeprocess.CommandResult{Stderr: []byte("Error response from daemon: No such container: " + identity)}
	if !generatedMigrationDefiniteAbsence(canonical, exited, identity) {
		t.Fatal("canonical completed exit one was not accepted")
	}
	for name, test := range map[string]struct {
		result runtimeprocess.CommandResult
		err    error
	}{
		"generic_error":       {result: canonical, err: errors.New("inspect transport failed")},
		"wrapped_exit":        {result: canonical, err: errors.Join(errors.New("wrapper"), exited)},
		"wrong_identity":      {result: canonical, err: exited},
		"stdout":              {result: runtimeprocess.CommandResult{Stdout: []byte("unexpected"), Stderr: canonical.Stderr}, err: exited},
		"truncated":           {result: runtimeprocess.CommandResult{Stderr: canonical.Stderr, StderrTruncated: true}, err: exited},
		"misleading_message":  {result: runtimeprocess.CommandResult{Stderr: []byte("transport: No such container: " + identity)}, err: exited},
		"termination_failure": {result: canonical, err: errors.Join(runtimeprocess.ErrTerminationFailed, exited)},
		"cancellation":        {result: canonical, err: errors.Join(context.Canceled, exited)},
	} {
		t.Run(name, func(t *testing.T) {
			query := identity
			if name == "wrong_identity" {
				query = "different"
			}
			if generatedMigrationDefiniteAbsence(test.result, test.err, query) {
				t.Fatal("uncertain absence was accepted")
			}
		})
	}
}

func TestGeneratedMigrationInspectionPreservesUncertainAbsenceCause(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	transport := errors.New("inspect transport failed")
	fixture.docker.absenceErr = transport
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); !errors.Is(err, transport) {
		t.Fatalf("uncertain inspection cause was not preserved: %v", err)
	}
	if err := command.RemoveAndConfirm(); err != nil {
		t.Fatalf("pre-create environment cleanup: %v", err)
	}
	command.Leave()
}

func TestGeneratedMigrationRequiresNameAbsenceBeforeCreate(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	fixture.docker.status = "created"
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); err == nil {
		t.Fatal("preexisting migration container name was accepted")
	}
	if err := command.RemoveAndConfirm(); err != nil {
		t.Fatalf("pre-create environment cleanup: %v", err)
	}
	command.Leave()
	fixture.docker.mu.Lock()
	defer fixture.docker.mu.Unlock()
	for _, request := range fixture.docker.requests {
		if strings.Join(request.Args[:min(2, len(request.Args))], " ") == "container create" {
			t.Fatal("Docker create ran despite a preexisting name")
		}
	}
}

func TestGeneratedMigrationRequiresCanonicalImageAndNetworkBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*generatedMigrationDockerFake)
	}{
		{name: "image_identity", mutate: func(docker *generatedMigrationDockerFake) {
			docker.imageObservation.ID = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "image_metadata", mutate: func(docker *generatedMigrationDockerFake) {
			docker.imageObservation.Labels["io.rig.definition"] = strings.Repeat("0", 64)
		}},
		{name: "network_identity", mutate: func(docker *generatedMigrationDockerFake) { docker.networkObservation.Name = "foreign" }},
		{name: "network_metadata", mutate: func(docker *generatedMigrationDockerFake) {
			docker.networkObservation.Labels["io.rig.application"] = uuid.NewString()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedMigrationFixture(t)
			test.mutate(fixture.docker)
			owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Leave()
			invocation, _, err := owner.Begin()
			if err != nil {
				t.Fatal(err)
			}
			command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := command.CreateAndInspect(); err == nil {
				t.Fatal("noncanonical physical precondition was accepted")
			}
			if err := command.RemoveAndConfirm(); err != nil {
				t.Fatalf("pre-create environment cleanup: %v", err)
			}
			command.Leave()
			fixture.docker.mu.Lock()
			defer fixture.docker.mu.Unlock()
			for _, request := range fixture.docker.requests {
				if strings.Join(request.Args[:min(2, len(request.Args))], " ") == "container create" {
					t.Fatal("Docker create ran after physical precondition drift")
				}
			}
		})
	}
}

func TestGeneratedMigrationRevalidatesOwnedContainerBeforeStartAndRemove(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		fixture := newGeneratedMigrationFixture(t)
		owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Leave()
		invocation, _, err := owner.Begin()
		if err != nil {
			t.Fatal(err)
		}
		command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.CreateAndInspect(); err != nil {
			t.Fatal(err)
		}
		fixture.docker.mu.Lock()
		fixture.docker.image = "sha256:" + strings.Repeat("0", 64)
		fixture.docker.mu.Unlock()
		if err := command.StartAndInspect(); err == nil {
			t.Fatal("substituted pre-start container was accepted")
		}
		command.Leave()
		fixture.docker.mu.Lock()
		defer fixture.docker.mu.Unlock()
		for _, request := range fixture.docker.requests {
			if strings.Join(request.Args[:min(2, len(request.Args))], " ") == "container start" {
				t.Fatal("Docker start ran after ownership substitution")
			}
		}
	})

	t.Run("remove", func(t *testing.T) {
		fixture := newGeneratedMigrationFixture(t)
		owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Leave()
		invocation, _, err := owner.Begin()
		if err != nil {
			t.Fatal(err)
		}
		command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.CreateAndInspect(); err != nil {
			t.Fatal(err)
		}
		if err := command.StartAndInspect(); err != nil {
			t.Fatal(err)
		}
		if _, err := command.WaitAndInspectExited(); err != nil {
			t.Fatal(err)
		}
		fixture.docker.mu.Lock()
		fixture.docker.labels["io.rig.invocation"] = "substituted"
		fixture.docker.mu.Unlock()
		if err := command.RemoveAndConfirm(); err == nil {
			t.Fatal("substituted cleanup container was accepted")
		}
		command.Leave()
		fixture.docker.mu.Lock()
		defer fixture.docker.mu.Unlock()
		for _, request := range fixture.docker.requests {
			if strings.Join(request.Args[:min(2, len(request.Args))], " ") == "container rm" {
				t.Fatal("Docker rm ran after ownership substitution")
			}
		}
	})
}

func TestGeneratedMigrationPrivateObserverDerivesProtectedInputsAndPersistsSuccess(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	if _, err := fixture.plans.Replace(context.Background(), fixture.guarded.appID, fixture.guarded.actorID, deploymentplans.ReplaceInput{ExpectedRevisionNumber: 1, Plan: fixture.plan}); err != nil {
		t.Fatalf("supersede pinned plan: %v", err)
	}
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, running, err := owner.Begin()
	if err != nil || running.MigrationState != generatedruntimestate.MigrationRunning {
		t.Fatalf("begin migration: state=%s err=%v", running.MigrationState, err)
	}
	identity := invocation.Identity()
	if identity.DeploymentID != fixture.request.DeploymentID || identity.Claim.Attempt() != fixture.claim.Attempt() || identity.DeploymentPlanRevisionID != fixture.request.DeploymentPlanRevisionID {
		t.Fatalf("copied identity=%+v", identity)
	}
	if _, err := owner.RecordSucceeded(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("fabricated success accepted: %v", err)
	}
	if _, err := owner.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("fabricated failure accepted: %v", err)
	}
	foreignRepository, err := NewWithExecutionGuardAndGeneratedMigration(fixture.base.repository.db, fixture.guarded.guard, fixture.plans, fixture.configuration, fixture.stager, fixture.docker, fixture.repository.migrationOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignRepository.PinGeneratedMigrationCommand(invocation); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("foreign repository accepted permit: %v", err)
	}
	if _, err := fixture.repository.PinGeneratedMigrationCommand(&embeddedGeneratedMigrationInvocation{GeneratedMigrationInvocation: invocation}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("embedded genuine invocation accepted: %v", err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.PinGeneratedMigrationCommand(invocation); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("single-use invocation replay accepted: %v", err)
	}
	if err := command.CreateAndInspect(); err != nil {
		t.Fatal(err)
	}
	if err := command.StartAndInspect(); err != nil {
		t.Fatal(err)
	}
	if code, err := command.WaitAndInspectExited(); err != nil || code != 0 {
		t.Fatalf("wait: code=%d err=%v", code, err)
	}
	if err := command.RemoveAndConfirm(); err != nil {
		t.Fatal(err)
	}
	command.Leave()
	finished, err := owner.RecordSucceeded()
	if err != nil || finished.MigrationState != generatedruntimestate.MigrationSucceeded {
		t.Fatalf("record success: state=%s err=%v", finished.MigrationState, err)
	}
	prepared, err := owner.FinishPreparation()
	if err != nil || prepared.Phase != generatedruntimestate.PhaseStartingCandidate {
		t.Fatalf("finish preparation: phase=%s err=%v", prepared.Phase, err)
	}
	fixture.stager.mu.Lock()
	operation, attempt, contents, cleaned := fixture.stager.operation, fixture.stager.attempt, string(fixture.stager.contents), fixture.stager.cleaned
	fixture.stager.mu.Unlock()
	if operation != fixture.request.DeploymentID || attempt != fixture.claim.Attempt() || !strings.Contains(contents, "DATABASE_URL=postgres://application-owned") || strings.Contains(contents, "IGNORED") || !cleaned {
		t.Fatalf("stage operation=%s attempt=%d contents=%q cleaned=%t", operation, attempt, contents, cleaned)
	}
	fixture.docker.mu.Lock()
	commandText, labels := fixture.docker.command, cloneStringMap(fixture.docker.labels)
	requests := append([]runtimeprocess.CommandRequest(nil), fixture.docker.requests...)
	fixture.docker.mu.Unlock()
	if commandText != "npm run migrate -- --locked" || labels["io.rig.invocation"] == "" || labels["io.rig.deployment"] != fixture.request.DeploymentID {
		t.Fatalf("derived command=%q labels=%v", commandText, labels)
	}
	if len(requests) == 0 {
		t.Fatal("Docker create command was not observed")
	}
	if len(requests) < 4 {
		t.Fatalf("physical precreate request count=%d", len(requests))
	}
	for index, expected := range []string{"image inspect", "network inspect", "container inspect", "container create"} {
		if actual := strings.Join(requests[index].Args[:min(2, len(requests[index].Args))], " "); actual != expected {
			t.Fatalf("precreate request %d=%q want %q", index, actual, expected)
		}
	}
	create := generatedMigrationRequestByKind(t, requests, "container create").Args
	for flag, want := range map[string]string{
		"--network": fixture.docker.network, "--env-file": fixture.stager.path, "--user": "node", "--workdir": "/workspace/apps/api",
		"--tmpfs": "/tmp:rw,noexec,nosuid,nodev,size=67108864,mode=1777", "--cap-drop": "ALL", "--security-opt": "no-new-privileges=true",
		"--memory": "536870912", "--memory-swap": "536870912", "--cpu-period": "100000", "--cpu-quota": "100000", "--pids-limit": "256",
		"--ulimit": "nofile=1024:1024", "--log-driver": "json-file", "--restart": "no", "--entrypoint": "/usr/local/bin/rig-entrypoint",
	} {
		if got := generatedMigrationArg(create, flag); got != want {
			t.Fatalf("create %s=%q want %q", flag, got, want)
		}
	}
	for _, flag := range []string{"--read-only", "--init"} {
		if !containsArgument(create, flag) {
			t.Fatalf("create omitted %s", flag)
		}
	}
}

func TestGeneratedMigrationStartAcceptsCanonicalAlreadyExitedObservation(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	fixture.docker.exitBeforeStartInspection = true
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); err != nil {
		t.Fatal(err)
	}
	if err := command.StartAndInspect(); err != nil {
		t.Fatalf("short migration start observation: %v", err)
	}
	if code, err := command.WaitAndInspectExited(); err != nil || code != 0 {
		t.Fatalf("short migration wait: code=%d err=%v", code, err)
	}
	if err := command.RemoveAndConfirm(); err != nil {
		t.Fatal(err)
	}
	command.Leave()
	if _, err := owner.RecordSucceeded(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(generatedMigrationInspectFormat, ".Config.Env") || strings.Contains(generatedMigrationInspectFormat, "Environment") {
		t.Fatal("inspection projection includes environment values")
	}
}

func TestGeneratedMigrationBeginHydrationCancellationIsFailClosed(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	ctx, cancel := context.WithCancel(fixture.guarded.context)
	owner, err := fixture.repository.PinGeneratedMigration(ctx, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	fixture.repository.afterGeneratedMigrationBeginCommit = cancel
	invocation, _, err := owner.Begin()
	if invocation != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-commit hydration: invocation=%v err=%v", invocation, err)
	}
	durable, err := generatedruntimestate.New(fixture.base.repository.db).Get(context.Background(), fixture.claim.ApplicationID(), fixture.request.DeploymentID)
	if err != nil || durable.MigrationState != generatedruntimestate.MigrationRunning {
		t.Fatalf("durable post-commit state=%s err=%v", durable.MigrationState, err)
	}
	recovery, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Leave()
	if replay, _, err := recovery.Begin(); replay != nil || !errors.Is(err, generatedruntimestate.ErrInvalidTransition) {
		t.Fatalf("durable running migration replay: invocation=%v err=%v", replay, err)
	}
}

func TestGeneratedMigrationPrivateNonzeroWitnessCannotBeForged(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	fixture.docker.exitCode = 7
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("caller error forged failure witness: %v", err)
	}
	if err := command.CreateAndInspect(); err != nil {
		t.Fatal(err)
	}
	if err := command.StartAndInspect(); err != nil {
		t.Fatal(err)
	}
	if code, err := command.WaitAndInspectExited(); err != nil || code != 7 {
		t.Fatalf("wait: code=%d err=%v", code, err)
	}
	command.Leave()
	failed, err := owner.RecordObservedFailure()
	if err != nil || failed.MigrationState != generatedruntimestate.MigrationFailed {
		t.Fatalf("record failure: state=%s err=%v", failed.MigrationState, err)
	}
	if _, err := owner.RecordSucceeded(); err == nil {
		t.Fatal("nonzero witness authorized success")
	}
}

func TestGeneratedMigrationRejectsConfigurationIdentityMismatchBeforeStaging(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	fixture.repository.afterGeneratedMigrationConfigurationExport = func(configuration *appconfig.ExecutionConfiguration) {
		configuration.RevisionID = uuid.NewString()
	}
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("configuration mismatch err=%v", err)
	}
	if err := command.CreateAndInspect(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pre-mutation create retry accepted: %v", err)
	}
	if err := command.RemoveAndConfirm(); err != nil {
		t.Fatalf("pre-mutation cleanup: %v", err)
	}
	command.Leave()
	fixture.stager.mu.Lock()
	operation := fixture.stager.operation
	fixture.stager.mu.Unlock()
	if operation != "" {
		t.Fatalf("mismatched configuration was staged for %s", operation)
	}
}

func TestGeneratedMigrationLostMutationAcknowledgementAllowsCleanupOnly(t *testing.T) {
	for _, test := range []struct {
		name       string
		loseCreate bool
		loseStart  bool
	}{
		{name: "create", loseCreate: true},
		{name: "start", loseStart: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedMigrationFixture(t)
			fixture.docker.loseCreateAcknowledgement = test.loseCreate
			fixture.docker.loseStartAcknowledgement = test.loseStart
			owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Leave()
			invocation, _, err := owner.Begin()
			if err != nil {
				t.Fatal(err)
			}
			command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
			if err != nil {
				t.Fatal(err)
			}
			createErr := command.CreateAndInspect()
			if test.loseCreate {
				if createErr == nil {
					t.Fatal("lost create acknowledgement accepted")
				}
			} else {
				if createErr != nil {
					t.Fatal(createErr)
				}
				if err := command.StartAndInspect(); err == nil {
					t.Fatal("lost start acknowledgement accepted")
				}
			}
			if err := command.StartAndInspect(); err == nil {
				t.Fatal("positive start remained available after uncertainty")
			}
			if err := command.CreateAndInspect(); err == nil {
				t.Fatal("create was replayed after uncertainty")
			}
			if _, err := command.WaitAndInspectExited(); err == nil {
				t.Fatal("uncertain mutation minted exit evidence")
			}
			if err := command.RemoveAndConfirm(); err != nil {
				t.Fatalf("exact owned cleanup failed: %v", err)
			}
			command.Leave()
			if _, err := owner.RecordSucceeded(); err == nil {
				t.Fatal("uncertain mutation authorized success")
			}
			if _, err := owner.RecordObservedFailure(); err == nil {
				t.Fatal("uncertain mutation authorized failure")
			}
		})
	}
}

func TestGeneratedMigrationPostObservationGatesRefuseDrift(t *testing.T) {
	t.Run("identity_after_create", func(t *testing.T) {
		fixture := newGeneratedMigrationFixture(t)
		fixture.docker.afterInspect = func(index int, _ runtimeprocess.CommandRequest) {
			if index == 2 {
				if _, err := fixture.base.repository.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, fixture.claim.JobID()); err != nil {
					t.Fatal(err)
				}
			}
		}
		owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Leave()
		invocation, _, err := owner.Begin()
		if err != nil {
			t.Fatal(err)
		}
		command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.CreateAndInspect(); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("identity drift after create inspection: %v", err)
		}
		if err := command.RemoveAndConfirm(); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("cleanup after identity drift: %v", err)
		}
		command.Leave()
		fixture.docker.mu.Lock()
		requests := append([]runtimeprocess.CommandRequest(nil), fixture.docker.requests...)
		fixture.docker.mu.Unlock()
		for _, request := range requests {
			kind := strings.Join(request.Args[:min(2, len(request.Args))], " ")
			if kind == "container start" || kind == "container wait" || kind == "container rm" {
				t.Fatalf("production command %q ran after identity drift", kind)
			}
		}
	})

	t.Run("cancellation_after_exit", func(t *testing.T) {
		fixture := newGeneratedMigrationFixture(t)
		ctx, cancel := context.WithCancel(fixture.guarded.context)
		fixture.docker.afterInspect = func(index int, _ runtimeprocess.CommandRequest) {
			if index == 5 {
				cancel()
			}
		}
		owner, err := fixture.repository.PinGeneratedMigration(ctx, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Leave()
		invocation, _, err := owner.Begin()
		if err != nil {
			t.Fatal(err)
		}
		command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.CreateAndInspect(); err != nil {
			t.Fatal(err)
		}
		if err := command.StartAndInspect(); err != nil {
			t.Fatal(err)
		}
		if _, err := command.WaitAndInspectExited(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation after exit inspection: %v", err)
		}
		if err := command.RemoveAndConfirm(); err != nil {
			t.Fatalf("bounded cleanup after cancellation: %v", err)
		}
		command.Leave()
		if _, err := owner.RecordSucceeded(); err == nil {
			t.Fatalf("canceled observation minted success proof: %v", err)
		}
		if _, err := owner.RecordObservedFailure(); err == nil {
			t.Fatalf("canceled observation minted failure proof: %v", err)
		}
	})

	t.Run("policy_after_start", func(t *testing.T) {
		fixture := newGeneratedMigrationFixture(t)
		fixture.docker.afterInspect = func(index int, _ runtimeprocess.CommandRequest) {
			if index == 4 {
				if _, err := fixture.base.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), fixture.request.DeploymentID, "v1", "network", "app", strings.Repeat("9", 64), "allowed", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			}
		}
		owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Leave()
		invocation, _, err := owner.Begin()
		if err != nil {
			t.Fatal(err)
		}
		command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.CreateAndInspect(); err != nil {
			t.Fatal(err)
		}
		if err := command.StartAndInspect(); err == nil {
			t.Fatal("policy drift after start inspection was accepted")
		}
		if err := command.RemoveAndConfirm(); err == nil {
			t.Fatal("policy drift retained cleanup authority")
		}
		command.Leave()
	})
}

func TestGeneratedMigrationRemovePreservesUncertainMutationCause(t *testing.T) {
	for _, test := range []struct {
		name        string
		removeError func(error) error
		termination bool
	}{
		{name: "generic", removeError: func(cause error) error { return cause }},
		{name: "termination", removeError: func(cause error) error { return errors.Join(cause, runtimeprocess.ErrTerminationFailed) }, termination: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedMigrationFixture(t)
			owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Leave()
			invocation, _, err := owner.Begin()
			if err != nil {
				t.Fatal(err)
			}
			command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := command.CreateAndInspect(); err != nil {
				t.Fatal(err)
			}
			if err := command.StartAndInspect(); err != nil {
				t.Fatal(err)
			}
			if _, err := command.WaitAndInspectExited(); err != nil {
				t.Fatal(err)
			}
			transport := errors.New("remove transport failed")
			fixture.docker.mu.Lock()
			fixture.docker.removeErr = test.removeError(transport)
			fixture.docker.mu.Unlock()
			if err := command.RemoveAndConfirm(); !errors.Is(err, transport) || (test.termination && !errors.Is(err, runtimeprocess.ErrTerminationFailed)) {
				t.Fatalf("uncertain remove cause was not preserved: %v", err)
			}
			fixture.stager.mu.Lock()
			cleaned := fixture.stager.cleaned
			fixture.stager.mu.Unlock()
			if !cleaned {
				t.Fatal("uncertain removal did not release the staged environment")
			}
			command.Leave()
			if _, err := owner.RecordSucceeded(); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("uncertain removal minted success proof: %v", err)
			}
			durable, err := generatedruntimestate.New(fixture.base.repository.db).Get(context.Background(), fixture.claim.ApplicationID(), fixture.request.DeploymentID)
			if err != nil || durable.MigrationState != generatedruntimestate.MigrationRunning {
				t.Fatalf("uncertain removal changed durable state=%s err=%v", durable.MigrationState, err)
			}
		})
	}
}

func TestGeneratedMigrationCanceledPositiveContextRetainsBoundedCleanup(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	ctx, cancel := context.WithCancel(fixture.guarded.context)
	owner, err := fixture.repository.PinGeneratedMigration(ctx, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := command.StartAndInspect(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled positive start err=%v", err)
	}
	if err := command.RemoveAndConfirm(); err != nil {
		t.Fatalf("bounded cleanup after cancellation: %v", err)
	}
	command.Leave()
}

func TestGeneratedMigrationCommandLeaveJoinsActiveProcess(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); err != nil {
		t.Fatal(err)
	}
	fixture.docker.block = make(chan struct{})
	fixture.docker.entered = make(chan struct{}, 1)
	startDone := make(chan error, 1)
	go func() { startDone <- command.StartAndInspect() }()
	<-fixture.docker.entered
	leaveDone := make(chan struct{})
	go func() { command.Leave(); close(leaveDone) }()
	ownerLeaveDone := make(chan struct{})
	go func() { owner.Leave(); close(ownerLeaveDone) }()
	select {
	case <-leaveDone:
		t.Fatal("command Leave returned before active process joined")
	default:
	}
	select {
	case <-ownerLeaveDone:
		t.Fatal("owner Leave returned before child process joined")
	default:
	}
	close(fixture.docker.block)
	if err := <-startDone; err != nil && !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatal(err)
	}
	<-leaveDone
	<-ownerLeaveDone
}

func TestGeneratedMigrationCleanupContinuesWhileParentIsClosing(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	invocation, _, err := owner.Begin()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CreateAndInspect(); err != nil {
		t.Fatal(err)
	}
	if err := command.StartAndInspect(); err != nil {
		t.Fatal(err)
	}
	if _, err := command.WaitAndInspectExited(); err != nil {
		t.Fatal(err)
	}
	inspectEntered := make(chan struct{}, 1)
	releaseInspect := make(chan struct{})
	fixture.docker.mu.Lock()
	fixture.docker.afterInspect = func(index int, _ runtimeprocess.CommandRequest) {
		if index == 6 {
			inspectEntered <- struct{}{}
			<-releaseInspect
		}
	}
	fixture.docker.mu.Unlock()
	removeDone := make(chan error, 1)
	go func() { removeDone <- command.RemoveAndConfirm() }()
	<-inspectEntered
	ownerLeaveDone := make(chan struct{})
	go func() { owner.Leave(); close(ownerLeaveDone) }()
	waitGeneratedMigrationOwnerClosing(t, owner)
	close(releaseInspect)
	if err := <-removeDone; err != nil {
		t.Fatalf("cleanup while parent closing: %v", err)
	}
	select {
	case <-ownerLeaveDone:
		t.Fatal("parent Leave returned before cleanup child Leave")
	default:
	}
	command.Leave()
	<-ownerLeaveDone
}

func waitGeneratedMigrationOwnerClosing(t *testing.T, owner *GeneratedMigrationExecution) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		owner.state.mu.Lock()
		closing := owner.state.closing
		owner.state.mu.Unlock()
		if closing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("parent did not enter closing state")
		}
		runtime.Gosched()
	}
}

func TestGeneratedMigrationBeginPreservesCommittedRunningStateWhenCommitAcknowledgementIsLost(t *testing.T) {
	sentinel := errors.New("migration begin commit acknowledgement lost")
	fault := &generatedMigrationCommitFault{cause: sentinel}
	db := openGeneratedMigrationCommitDatabase(t, fault)
	fixture := newGeneratedMigrationFixtureWithBase(t, newGeneratedMigrationRepositoryFixtureOnDatabase(t, db), []string{"api"}, []string{"api"})
	owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Leave()
	fault.trackTransitions.Store(true)
	fault.armed.Store(true)
	invocation, _, err := owner.Begin()
	if invocation != nil || !errors.Is(err, sentinel) {
		t.Fatalf("lost begin acknowledgement: invocation=%v err=%v", invocation, err)
	}
	if attempts := fault.transitionAttempts.Load(); attempts != 1 {
		t.Fatalf("begin transition attempts=%d, want 1", attempts)
	}
	fault.trackTransitions.Store(false)
	persisted, err := generatedruntimestate.New(db).Get(context.Background(), fixture.claim.ApplicationID(), fixture.request.DeploymentID)
	if err != nil || persisted.MigrationState != generatedruntimestate.MigrationRunning {
		t.Fatalf("committed Running state was not retained: state=%s err=%v", persisted.MigrationState, err)
	}
	if replay, _, err := owner.Begin(); replay != nil || !errors.Is(err, generatedruntimestate.ErrInvalidTransition) {
		t.Fatalf("committed begin was replayed: invocation=%v err=%v", replay, err)
	}
}

func TestGeneratedMigrationOutcomePreservesCommittedTerminalStateWhenCommitAcknowledgementIsLost(t *testing.T) {
	for _, test := range []struct {
		name     string
		exitCode int
		expected generatedruntimestate.MigrationState
	}{
		{name: "succeeded", expected: generatedruntimestate.MigrationSucceeded},
		{name: "failed", exitCode: 7, expected: generatedruntimestate.MigrationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			sentinel := errors.New("migration outcome commit acknowledgement lost")
			fault := &generatedMigrationCommitFault{cause: sentinel}
			db := openGeneratedMigrationCommitDatabase(t, fault)
			fixture := newGeneratedMigrationFixtureWithBase(t, newGeneratedMigrationRepositoryFixtureOnDatabase(t, db), []string{"api"}, []string{"api"})
			fixture.docker.exitCode = test.exitCode
			owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Leave()
			invocation, _, err := owner.Begin()
			if err != nil {
				t.Fatal(err)
			}
			command, err := fixture.repository.PinGeneratedMigrationCommand(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := command.CreateAndInspect(); err != nil {
				t.Fatal(err)
			}
			if err := command.StartAndInspect(); err != nil {
				t.Fatal(err)
			}
			if code, err := command.WaitAndInspectExited(); err != nil || code != test.exitCode {
				t.Fatalf("wait: code=%d err=%v", code, err)
			}
			if test.exitCode == 0 {
				if err := command.RemoveAndConfirm(); err != nil {
					t.Fatal(err)
				}
			}
			command.Leave()
			fault.trackTransitions.Store(true)
			fault.armed.Store(true)
			if test.exitCode == 0 {
				_, err = owner.RecordSucceeded()
			} else {
				_, err = owner.RecordObservedFailure()
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("lost outcome acknowledgement: %v", err)
			}
			if attempts := fault.transitionAttempts.Load(); attempts != 1 {
				t.Fatalf("outcome transition attempts=%d, want 1", attempts)
			}
			fault.trackTransitions.Store(false)
			persisted, err := generatedruntimestate.New(db).Get(context.Background(), fixture.claim.ApplicationID(), fixture.request.DeploymentID)
			if err != nil || persisted.MigrationState != test.expected {
				t.Fatalf("committed terminal state=%s want=%s err=%v", persisted.MigrationState, test.expected, err)
			}
			if test.exitCode == 0 {
				_, err = owner.RecordSucceeded()
			} else {
				_, err = owner.RecordObservedFailure()
			}
			if err == nil {
				t.Fatal("committed terminal outcome was replayed")
			}
		})
	}
}

func TestGeneratedMigrationRequiresExactAcceptedComponentSet(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*generatedMigrationFixture) error
	}{
		{name: "missing", mutate: func(fixture *generatedMigrationFixture) error {
			if _, err := fixture.base.repository.db.Exec(`DROP TRIGGER generated_runtime_component_no_delete`); err != nil {
				return err
			}
			_, err := fixture.base.repository.db.Exec(`DELETE FROM generated_runtime_components WHERE deployment_id=? AND component_name='worker'`, fixture.request.DeploymentID)
			return err
		}},
		{name: "substitution", mutate: func(fixture *generatedMigrationFixture) error {
			if _, err := fixture.base.repository.db.Exec(`DROP TRIGGER generated_runtime_component_identity_immutable`); err != nil {
				return err
			}
			_, err := fixture.base.repository.db.Exec(`UPDATE generated_runtime_components SET component_name='rogue' WHERE deployment_id=? AND component_name='worker'`, fixture.request.DeploymentID)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedMigrationFixtureWithComponents(t, []string{"api", "worker"}, []string{"api", "worker"})
			if err := test.mutate(fixture); err != nil {
				t.Fatal(err)
			}
			owner, err := fixture.repository.PinGeneratedMigration(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Leave()
			if invocation, _, err := owner.Begin(); invocation != nil || !errors.Is(err, ErrInvalidDeployment) {
				t.Fatalf("component %s mismatch: invocation=%v err=%v", test.name, invocation, err)
			}
			persisted, err := generatedruntimestate.New(fixture.base.repository.db).Get(context.Background(), fixture.claim.ApplicationID(), fixture.request.DeploymentID)
			if err != nil || persisted.MigrationState != generatedruntimestate.MigrationPending {
				t.Fatalf("component mismatch changed durable state=%s err=%v", persisted.MigrationState, err)
			}
		})
	}
}

func newGeneratedMigrationRepositoryFixtureOnDatabase(t *testing.T, db *sql.DB) repositoryFixture {
	t.Helper()
	fixture := repositoryFixture{repository: New(db), dataRoot: t.TempDir(), appA: uuid.NewString(), appB: uuid.NewString(), actor: uuid.NewString(), jobA: uuid.NewString(), jobB: uuid.NewString()}
	machine := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, fixture.actor, "owner", "hash", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machine, "local", now, now); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{fixture.appA, fixture.appB} {
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?, 'draft',?,?)`, app, app, app, machine, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{fixture.jobA, fixture.appA}, {fixture.jobB, fixture.appB}} {
		if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,?,?)`, pair[0], pair[1], fixture.actor, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func newGeneratedMigrationFixture(t *testing.T) *generatedMigrationFixture {
	t.Helper()
	return newGeneratedMigrationFixtureWithBase(t, newRepositoryFixture(t), []string{"api"}, []string{"api"})
}

func newGeneratedMigrationFixtureWithComponents(t *testing.T, planComponents, runtimeComponents []string) *generatedMigrationFixture {
	t.Helper()
	return newGeneratedMigrationFixtureWithBase(t, newRepositoryFixture(t), planComponents, runtimeComponents)
}

func newGeneratedMigrationFixtureWithBase(t *testing.T, base repositoryFixture, planComponentNames, runtimeComponentNames []string) *generatedMigrationFixture {
	t.Helper()
	guarded := newGuardedRepositoryFixture(t, base)
	plans, err := deploymentplans.New(base.repository.db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(base.repository.db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "runCommand", "internalPort", "healthProbe"}
	components := make([]deploymentplans.Component, 0, len(planComponentNames))
	provenance := make([]deploymentplans.FieldProvenance, 0, len(planComponentNames)*len(fields))
	configComponents := make([]appconfig.ComponentTarget, 0, len(planComponentNames))
	for _, name := range planComponentNames {
		root := "apps/" + name
		components = append(components, deploymentplans.Component{Name: name, Role: "server", RootDirectory: root, PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: root, NodeVersion: "24", RunCommand: "npm start", InternalPort: 3000, HealthProbe: "/health"})
		configComponents = append(configComponents, appconfig.ComponentTarget{Name: name, Role: "server"})
		for _, field := range fields {
			provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components." + name + "." + field, Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
		}
	}
	plan := deploymentplans.Plan{
		Strategy:   deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "package-json", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:     deploymentplans.SourceIdentity{Provider: "github", RepositoryID: 7, ResolvedDigest: strings.Repeat("c", 64)},
		Components: components, FieldProvenance: provenance,
		Migration: &deploymentplans.Migration{ComponentName: "api", RootDirectory: "apps/api", Command: "npm run migrate -- --locked", EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: strings.Repeat("b", 64), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}},
	}
	revision, err := plans.Replace(context.Background(), guarded.appID, guarded.actorID, deploymentplans.ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := plans.ApproveMigration(context.Background(), guarded.appID, revision.ID, revision.RevisionNumber, 0, guarded.actorID); err != nil {
		t.Fatal(err)
	}
	secret := "postgres://application-owned"
	ignored := "not-selected"
	config, err := configuration.ReplaceScoped(context.Background(), guarded.appID, guarded.actorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: revision.ID, PlanRevisionNumber: revision.RevisionNumber,
		Components: configComponents,
		Entries: []appconfig.ScopedValueInput{
			{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseMigration, Component: "api", Key: "DATABASE_URL"}, Sensitivity: appconfig.SensitivitySecret, Value: &secret},
			{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: "api", Key: "IGNORED"}, Sensitivity: appconfig.SensitivityPublic, Value: &ignored},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := guarded.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: guarded.appID, IdempotencyKey: "generated-migration", RequestedBy: guarded.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create deployment job: created=%t err=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := base.repository.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=2,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	releaseID := uuid.NewString()
	workspace := strings.Repeat("f", 64)
	if _, err := base.repository.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'github',7,?,'ready',?,?,?, ?,?)`, releaseID, guarded.appID, stamp, strings.Repeat("c", 64), workspace, config.RevisionID, config.RevisionNumber, revision.ID, revision.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	deployment, _, err := guarded.repository.GetOrCreateByJob(context.Background(), guarded.appID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = guarded.repository.InitializeRuntime(context.Background(), guarded.appID, deployment.ID, releaseID, config.RevisionID, config.RevisionNumber, RuntimeGeneratedNode, revision.ID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, guarded.appID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := guarded.repository.Gate(guarded.context, GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatal(err)
	}
	runtimeState := generatedruntimestate.New(base.repository.db)
	runtimeDeployment, wasCreated, err := runtimeState.Begin(context.Background(), generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: guarded.appID, ReleaseID: releaseID, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber, ComponentNames: append([]string(nil), runtimeComponentNames...)})
	if err != nil || !wasCreated {
		t.Fatalf("begin generated runtime: created=%t err=%v", wasCreated, err)
	}
	runtimeDeployment, err = runtimeState.Advance(context.Background(), guarded.appID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatal(err)
	}
	buildDigest := strings.Repeat("d", 64)
	imageID := "sha256:" + strings.Repeat("e", 64)
	artifactID := ""
	for _, name := range runtimeComponentNames {
		componentArtifactID := uuid.NewString()
		if _, err := base.repository.db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,'test-compiler',?,3,?,'ready',?,?,?)`, componentArtifactID, releaseID, revision.ID, revision.RevisionNumber, name, buildDigest, imageID, stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeState.SetImageReady(context.Background(), guarded.appID, deployment.ID, name, componentArtifactID); err != nil {
			t.Fatal(err)
		}
		if name == "api" {
			artifactID = componentArtifactID
		}
	}
	if artifactID == "" {
		t.Fatal("runtime fixture requires the migration component api")
	}
	if _, err := runtimeState.Advance(context.Background(), guarded.appID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseMigrating, ""); err != nil {
		t.Fatal(err)
	}
	dockerConfig := t.TempDir()
	stager := &generatedMigrationStagerFake{path: filepath.Join(t.TempDir(), "migration.env")}
	networkIdentity, err := generatedidentity.DescribeAppNetwork(guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	docker := &generatedMigrationDockerFake{
		id: strings.Repeat("1", 64), absenceErr: generatedMigrationCompletedExitOne(t),
		imageObservation: generatedMigrationImageInspection{
			ID: imageID, Size: 1, User: "node", WorkingDirectory: "/workspace", Entrypoint: []string{"/usr/local/bin/rig-entrypoint"},
			Labels: map[string]string{
				"io.rig.managed": "generated-image", "io.rig.application": guarded.appID, "io.rig.release": releaseID,
				"io.rig.artifact": artifactID, "io.rig.plan": revision.ID, "io.rig.component": "api", "io.rig.role": "server", "io.rig.definition": buildDigest,
			},
		},
		networkObservation: generatedMigrationNetworkInspection{
			Name: networkIdentity.Name, Driver: "bridge", Scope: "local",
			Labels: map[string]string{"io.rig.managed": generatedMigrationNetworkLabel, "io.rig.application": guarded.appID},
		},
	}
	options := GeneratedMigrationProcessOptions{DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerConfigDirectory: dockerConfig, WorkingDirectory: t.TempDir(), Timeout: time.Minute, OutputLimit: 16 << 10}
	repository, err := NewWithExecutionGuardAndGeneratedMigration(base.repository.db, guarded.guard, plans, configuration, stager, docker, options)
	if err != nil {
		t.Fatal(err)
	}
	request := GeneratedMigrationRequest{
		Claim: claim, DeploymentID: deployment.ID, ReleaseID: releaseID,
		ConfigurationRevisionID: config.RevisionID, ConfigurationRevisionNumber: config.RevisionNumber,
		DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber,
		ComponentName: "api", CompilerVersion: "test-compiler", BuildDefinitionDigest: buildDigest,
		PlanCanonicalDigest: revision.CanonicalDigest, MigrationEvidenceDigest: strings.Repeat("b", 64),
		SourceProvider: "github", SourceRepositoryID: 7, SourceResolvedDigest: strings.Repeat("c", 64),
		PlanSourceProvider: "github", PlanSourceRepositoryID: 7, PlanSourceResolvedDigest: strings.Repeat("c", 64),
		WorkspaceTreeSHA256: workspace, ArtifactID: artifactID, ArtifactAttemptNumber: 3, ImageContentID: imageID,
	}
	return &generatedMigrationFixture{base: base, guarded: guarded, repository: repository, plans: plans, configuration: configuration, stager: stager, docker: docker, request: request, plan: plan, claim: claim}
}
