package deployments

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	generatedMigrationOwnerTimeout         = 70 * time.Minute
	generatedMigrationCommandTimeout       = 30 * time.Second
	generatedMigrationNetworkLabel         = "generated-runtime-network"
	generatedMigrationImageInspectFormat   = `{"id":{{json .ID}},"size":{{json .Size}},"labels":{{json .Config.Labels}},"user":{{json .Config.User}},"workingDir":{{json .Config.WorkingDir}},"entrypoint":{{json .Config.Entrypoint}}}`
	generatedMigrationNetworkInspectFormat = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"internal":{{json .Internal}},"labels":{{json .Labels}}}`
	// This projection deliberately excludes Config.Env and every other
	// secret-bearing field. The observer receives only the facts it attests.
	generatedMigrationInspectFormat = `{"id":{{json .ID}},"name":{{json .Name}},"image":{{json .Image}},"configImage":{{json .Config.Image}},"user":{{json .Config.User}},"workingDir":{{json .Config.WorkingDir}},"entrypoint":{{json .Config.Entrypoint}},"cmd":{{json .Config.Cmd}},"labels":{{json .Config.Labels}},"volumes":{{json .Config.Volumes}},"memory":{{json .HostConfig.Memory}},"memorySwap":{{json .HostConfig.MemorySwap}},"cpuPeriod":{{json .HostConfig.CPUPeriod}},"cpuQuota":{{json .HostConfig.CPUQuota}},"pidsLimit":{{json .HostConfig.PidsLimit}},"ulimits":{{json .HostConfig.Ulimits}},"init":{{json .HostConfig.Init}},"networkMode":{{json .HostConfig.NetworkMode}},"readonlyRootfs":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"capAdd":{{json .HostConfig.CapAdd}},"capDrop":{{json .HostConfig.CapDrop}},"securityOpt":{{json .HostConfig.SecurityOpt}},"binds":{{json .HostConfig.Binds}},"hostMounts":{{json .HostConfig.Mounts}},"portBindings":{{json .HostConfig.PortBindings}},"tmpfs":{{json .HostConfig.Tmpfs}},"logType":{{json .HostConfig.LogConfig.Type}},"logConfig":{{json .HostConfig.LogConfig.Config}},"restart":{{json .HostConfig.RestartPolicy.Name}},"status":{{json .State.Status}},"running":{{json .State.Running}},"paused":{{json .State.Paused}},"restarting":{{json .State.Restarting}},"dead":{{json .State.Dead}},"exitCode":{{json .State.ExitCode}},"startedAt":{{json .State.StartedAt}},"networks":{{if .NetworkSettings}}{{json .NetworkSettings.Networks}}{{else}}null{{end}},"mounts":{{json .Mounts}}}`
)

type GeneratedMigrationCommandExecution struct {
	state *generatedMigrationCommandState
}

type generatedMigrationCommandState struct {
	mu             sync.Mutex
	changed        *sync.Cond
	repo           *Repository
	owner          *generatedMigrationExecutionState
	invocation     *generatedMigrationInvocationState
	pinned         *deploymentexecution.Pinned
	cleanup        *deploymentexecution.GeneratedMigrationCleanup
	positiveCtx    context.Context
	cleanupCtx     context.Context
	cancelPositive context.CancelFunc
	cancelCleanup  context.CancelFunc

	active  bool
	closing bool
	left    bool

	prepared           bool
	command            string
	componentRole      string
	workingDirectory   string
	networkName        string
	containerName      string
	labels             map[string]string
	environment        GeneratedMigrationEnvironmentLease
	environmentPath    string
	environmentClean   bool
	createAttempted    bool
	createEntered      bool
	createAcknowledged bool
	startAttempted     bool
	startAcknowledged  bool
	waitAttempted      bool
	removeAttempted    bool
	positiveBlocked    bool
	containerID        string
	startedAt          string
	exitObserved       bool
	exitCode           int
	absenceObserved    bool
}

type generatedMigrationInspection struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Image            string                     `json:"image"`
	ConfigImage      string                     `json:"configImage"`
	User             string                     `json:"user"`
	WorkingDirectory string                     `json:"workingDir"`
	Entrypoint       []string                   `json:"entrypoint"`
	Command          []string                   `json:"cmd"`
	Labels           map[string]string          `json:"labels"`
	Volumes          map[string]json.RawMessage `json:"volumes"`
	Memory           int64                      `json:"memory"`
	MemorySwap       int64                      `json:"memorySwap"`
	CPUPeriod        int64                      `json:"cpuPeriod"`
	CPUQuota         int64                      `json:"cpuQuota"`
	PidsLimit        int64                      `json:"pidsLimit"`
	Ulimits          []generatedMigrationUlimit `json:"ulimits"`
	Init             bool                       `json:"init"`
	NetworkMode      string                     `json:"networkMode"`
	ReadonlyRootfs   bool                       `json:"readonlyRootfs"`
	Privileged       bool                       `json:"privileged"`
	CapAdd           []string                   `json:"capAdd"`
	CapDrop          []string                   `json:"capDrop"`
	SecurityOptions  []string                   `json:"securityOpt"`
	Binds            []string                   `json:"binds"`
	HostMounts       []json.RawMessage          `json:"hostMounts"`
	PortBindings     map[string]json.RawMessage `json:"portBindings"`
	Tmpfs            map[string]string          `json:"tmpfs"`
	LogType          string                     `json:"logType"`
	LogConfig        map[string]string          `json:"logConfig"`
	Restart          string                     `json:"restart"`
	Status           string                     `json:"status"`
	Running          bool                       `json:"running"`
	Paused           bool                       `json:"paused"`
	Restarting       bool                       `json:"restarting"`
	Dead             bool                       `json:"dead"`
	ExitCode         int                        `json:"exitCode"`
	StartedAt        string                     `json:"startedAt"`
	Networks         map[string]json.RawMessage `json:"networks"`
	Mounts           []generatedMigrationMount  `json:"mounts"`
}

type generatedMigrationUlimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

type generatedMigrationMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type generatedMigrationImageInspection struct {
	ID               string            `json:"id"`
	Size             int64             `json:"size"`
	Labels           map[string]string `json:"labels"`
	User             string            `json:"user"`
	WorkingDirectory string            `json:"workingDir"`
	Entrypoint       []string          `json:"entrypoint"`
}

type generatedMigrationNetworkInspection struct {
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Scope    string            `json:"scope"`
	Internal bool              `json:"internal"`
	Labels   map[string]string `json:"labels"`
}

var generatedMigrationInspectionFields = []string{
	"id", "name", "image", "configImage", "user", "workingDir", "entrypoint", "cmd", "labels", "volumes",
	"memory", "memorySwap", "cpuPeriod", "cpuQuota", "pidsLimit", "ulimits", "init", "networkMode", "readonlyRootfs", "privileged",
	"capAdd", "capDrop", "securityOpt", "binds", "hostMounts", "portBindings", "tmpfs", "logType", "logConfig", "restart",
	"status", "running", "paused", "restarting", "dead", "exitCode", "startedAt", "networks", "mounts",
}

var generatedMigrationInspectionScalarFields = []string{
	"id", "name", "image", "configImage", "user", "workingDir", "memory", "memorySwap", "cpuPeriod", "cpuQuota", "pidsLimit",
	"init", "networkMode", "readonlyRootfs", "privileged", "logType", "restart", "status", "running", "paused", "restarting", "dead", "exitCode", "startedAt",
}

var errIncompleteGeneratedMigrationInspection = errors.New("incomplete generated migration inspection")

func decodeGeneratedMigrationInspection(data []byte) (generatedMigrationInspection, error) {
	if !utf8.Valid(data) || generatedMigrationUniqueJSON(data) != nil {
		return generatedMigrationInspection{}, errIncompleteGeneratedMigrationInspection
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != len(generatedMigrationInspectionFields) {
		return generatedMigrationInspection{}, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	for _, name := range generatedMigrationInspectionFields {
		if _, exists := fields[name]; !exists {
			return generatedMigrationInspection{}, errIncompleteGeneratedMigrationInspection
		}
	}
	for _, name := range generatedMigrationInspectionScalarFields {
		if generatedMigrationJSONNull(fields[name]) {
			return generatedMigrationInspection{}, errIncompleteGeneratedMigrationInspection
		}
	}
	for _, name := range []string{"entrypoint", "cmd", "capAdd", "capDrop", "securityOpt", "binds"} {
		if !generatedMigrationCompleteStringArray(fields[name]) {
			return generatedMigrationInspection{}, errIncompleteGeneratedMigrationInspection
		}
	}
	for _, name := range []string{"labels", "tmpfs", "logConfig"} {
		if !generatedMigrationCompleteStringMap(fields[name]) {
			return generatedMigrationInspection{}, errIncompleteGeneratedMigrationInspection
		}
	}
	if !generatedMigrationCompleteUlimits(fields["ulimits"]) || !generatedMigrationCompleteMounts(fields["mounts"]) || !generatedMigrationCompleteNetworks(fields["networks"]) {
		return generatedMigrationInspection{}, errIncompleteGeneratedMigrationInspection
	}
	var inspection generatedMigrationInspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		return generatedMigrationInspection{}, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	return inspection, nil
}

func generatedMigrationUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := generatedMigrationUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	return nil
}

func generatedMigrationUniqueJSONValue(decoder *json.Decoder) error {
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
				return errIncompleteGeneratedMigrationInspection
			}
			seen[key] = true
			if err := generatedMigrationUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.Join(errIncompleteGeneratedMigrationInspection, err)
		}
	case '[':
		for decoder.More() {
			if err := generatedMigrationUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.Join(errIncompleteGeneratedMigrationInspection, err)
		}
	default:
		return errIncompleteGeneratedMigrationInspection
	}
	return nil
}

func generatedMigrationCompleteStringArray(data []byte) bool {
	if generatedMigrationJSONNull(data) {
		return true
	}
	var values []json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		var text string
		if generatedMigrationJSONNull(value) || json.Unmarshal(value, &text) != nil {
			return false
		}
	}
	return true
}

func generatedMigrationCompleteStringMap(data []byte) bool {
	if generatedMigrationJSONNull(data) {
		return true
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		var text string
		if generatedMigrationJSONNull(value) || json.Unmarshal(value, &text) != nil {
			return false
		}
	}
	return true
}

func generatedMigrationCompleteUlimits(data []byte) bool {
	if generatedMigrationJSONNull(data) {
		return true
	}
	var values []map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		if len(value) != 3 || generatedMigrationJSONNull(value["Name"]) || generatedMigrationJSONNull(value["Soft"]) || generatedMigrationJSONNull(value["Hard"]) {
			return false
		}
		var decoded generatedMigrationUlimit
		body, err := json.Marshal(value)
		if err != nil || json.Unmarshal(body, &decoded) != nil {
			return false
		}
	}
	return true
}

func generatedMigrationCompleteMounts(data []byte) bool {
	if generatedMigrationJSONNull(data) {
		return true
	}
	var values []map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		typeValue, typeExists := value["Type"]
		sourceValue, sourceExists := value["Source"]
		destinationValue, destinationExists := value["Destination"]
		if !typeExists || !sourceExists || !destinationExists || generatedMigrationJSONNull(typeValue) || generatedMigrationJSONNull(sourceValue) || generatedMigrationJSONNull(destinationValue) {
			return false
		}
		var decoded generatedMigrationMount
		body, err := json.Marshal(value)
		if err != nil || json.Unmarshal(body, &decoded) != nil {
			return false
		}
	}
	return true
}

func generatedMigrationCompleteNetworks(data []byte) bool {
	if generatedMigrationJSONNull(data) {
		return true
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
			return false
		}
	}
	return true
}

func generatedMigrationJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func decodeGeneratedMigrationImageInspection(data []byte) (generatedMigrationImageInspection, error) {
	fields, err := decodeGeneratedMigrationPhysicalFields(data, []string{"id", "size", "labels", "user", "workingDir", "entrypoint"})
	if err != nil || !generatedMigrationCompleteStringMap(fields["labels"]) || !generatedMigrationCompleteStringArray(fields["entrypoint"]) {
		return generatedMigrationImageInspection{}, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	var inspection generatedMigrationImageInspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		return generatedMigrationImageInspection{}, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	return inspection, nil
}

func decodeGeneratedMigrationNetworkInspection(data []byte) (generatedMigrationNetworkInspection, error) {
	fields, err := decodeGeneratedMigrationPhysicalFields(data, []string{"name", "driver", "scope", "internal", "labels"})
	if err != nil || !generatedMigrationCompleteStringMap(fields["labels"]) {
		return generatedMigrationNetworkInspection{}, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	var inspection generatedMigrationNetworkInspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		return generatedMigrationNetworkInspection{}, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	return inspection, nil
}

func decodeGeneratedMigrationPhysicalFields(data []byte, required []string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) || generatedMigrationUniqueJSON(data) != nil {
		return nil, errIncompleteGeneratedMigrationInspection
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != len(required) {
		return nil, errors.Join(errIncompleteGeneratedMigrationInspection, err)
	}
	for _, name := range required {
		value, exists := fields[name]
		if !exists || generatedMigrationJSONNull(value) {
			return nil, errIncompleteGeneratedMigrationInspection
		}
	}
	return fields, nil
}

func validGeneratedMigrationProcessOptions(options GeneratedMigrationProcessOptions) bool {
	return generatedMigrationCleanAbsolute(options.DockerExecutable) && generatedMigrationCleanAbsolute(options.DockerConfigDirectory) &&
		generatedMigrationCleanAbsolute(options.WorkingDirectory) && generatedMigrationLocalEndpoint(options.DockerEndpoint) &&
		options.Timeout >= time.Second && options.Timeout <= time.Hour && options.OutputLimit > 0 && options.OutputLimit <= runtimeprocess.DefaultOutputLimit
}

func (r *Repository) initializeGeneratedMigrationProcess() error {
	if err := generatedMigrationEmptyDirectory(r.migrationOptions.DockerConfigDirectory); err != nil {
		return err
	}
	values := map[string]string{"DOCKER_CONFIG": r.migrationOptions.DockerConfigDirectory}
	for _, key := range []string{"PATH", "PATHEXT", "SystemRoot", "TEMP", "TMP", "WINDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if r.migrationOptions.DockerEndpoint != "" {
		values["DOCKER_HOST"] = r.migrationOptions.DockerEndpoint
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	r.migrationDockerEnv = make([]string, 0, len(keys))
	for _, key := range keys {
		r.migrationDockerEnv = append(r.migrationDockerEnv, key+"="+values[key])
	}
	return nil
}

func (r *Repository) PinGeneratedMigrationCommand(invocation GeneratedMigrationInvocation) (*GeneratedMigrationCommandExecution, error) {
	if !r.migrationConfigured() || invocation == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	permit, ok := invocation.(*generatedMigrationInvocationPermit)
	if !ok || permit == nil {
		return nil, ErrInvalidTransition
	}
	state := permit.state
	if state == nil || state.owner == nil {
		return nil, ErrInvalidTransition
	}
	owner := state.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if owner.closing || owner.left || owner.active || owner.repo != r || owner.ctx == nil || owner.pinned == nil || owner.invocation != state ||
		state.repo != r || state.owner != owner || state.consumed || state.commandLeft || state.id == "" {
		return nil, ErrInvalidTransition
	}
	if err := contextError(owner.ctx); err != nil {
		return nil, err
	}
	// Capture the sole owner deadline immediately before deriving the fixed
	// migration cleanup child. Neither clock is ever recomputed or renewed.
	deadline := time.Now().Add(generatedMigrationOwnerTimeout)
	positiveCtx, cancelPositive := context.WithDeadline(owner.ctx, deadline)
	cleanupCtx, cancelCleanup := context.WithDeadline(context.WithoutCancel(owner.ctx), deadline)
	cleanup, err := owner.pinned.DeriveGeneratedMigrationCleanup(owner.request.Claim, owner.request.DeploymentID, state.id)
	if err != nil {
		cancelPositive()
		cancelCleanup()
		return nil, err
	}
	command := &generatedMigrationCommandState{
		repo: r, owner: owner, invocation: state, pinned: owner.pinned, cleanup: cleanup,
		positiveCtx: positiveCtx, cleanupCtx: cleanupCtx, cancelPositive: cancelPositive, cancelCleanup: cancelCleanup,
	}
	command.changed = sync.NewCond(&command.mu)
	state.consumed = true
	return &GeneratedMigrationCommandExecution{state: command}, nil
}

func (e *GeneratedMigrationCommandExecution) CreateAndInspect() error {
	s, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	if s.createAttempted || s.prepared || s.positiveBlocked {
		return ErrInvalidTransition
	}
	s.createAttempted = true
	if err := s.prepare(); err != nil {
		return err
	}
	if err := s.attestImage(s.positiveCtx); err != nil {
		s.positiveBlocked = true
		return err
	}
	if err := s.positiveGate(); err != nil {
		s.positiveBlocked = true
		return err
	}
	if err := s.attestNetwork(s.positiveCtx); err != nil {
		s.positiveBlocked = true
		return err
	}
	if err := s.positiveGate(); err != nil {
		s.positiveBlocked = true
		return err
	}
	if _, found, err := s.inspect(s.positiveCtx, s.containerName); err != nil {
		s.positiveBlocked = true
		return err
	} else if found {
		s.positiveBlocked = true
		return errors.New("generated migration container name is already present")
	}
	args := s.createArgs()
	if err := s.positiveGate(); err != nil {
		return err
	}
	if err := contextError(s.positiveCtx); err != nil {
		return err
	}
	s.createEntered = true
	result, runErr := s.runPrepared(s.positiveCtx, args, generatedMigrationCommandTimeout)
	containerID := strings.TrimSpace(string(result.Stdout))
	truncated := result.StdoutTruncated || result.StderrTruncated
	clearGeneratedMigrationResult(&result)
	if runErr != nil || truncated || !lowerHex(containerID, 64) {
		s.positiveBlocked = true
		if runErr != nil {
			return runErr
		}
		return errors.New("generated migration create acknowledgement is invalid")
	}
	inspection, found, err := s.inspect(s.positiveCtx, containerID)
	if err != nil || !found || !s.matchesConfigured(inspection, containerID) || !s.matchesCreatedState(inspection) {
		s.positiveBlocked = true
		if err != nil {
			return err
		}
		return errors.New("generated migration created container observation is invalid")
	}
	if err := s.positiveGate(); err != nil {
		s.positiveBlocked = true
		return err
	}
	s.containerID = containerID
	s.createAcknowledged = true
	return nil
}

func (e *GeneratedMigrationCommandExecution) StartAndInspect() error {
	s, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	if !s.createAcknowledged || s.startAttempted || s.positiveBlocked || s.containerID == "" {
		return ErrInvalidTransition
	}
	s.startAttempted = true
	inspection, found, err := s.inspect(s.positiveCtx, s.containerID)
	if err != nil || !found || !s.matchesConfigured(inspection, s.containerID) || !s.matchesCreatedState(inspection) {
		if err != nil {
			return err
		}
		return errors.New("generated migration pre-start container observation is invalid")
	}
	if err := s.positiveGate(); err != nil {
		return err
	}
	if err := contextError(s.positiveCtx); err != nil {
		return err
	}
	result, runErr := s.runPrepared(s.positiveCtx, []string{"container", "start", s.containerID}, generatedMigrationCommandTimeout)
	truncated := result.StdoutTruncated || result.StderrTruncated
	clearGeneratedMigrationResult(&result)
	if runErr != nil || truncated {
		s.positiveBlocked = true
		if runErr != nil {
			return runErr
		}
		return errors.New("generated migration start acknowledgement is invalid")
	}
	inspection, found, err = s.inspect(s.positiveCtx, s.containerID)
	if err != nil || !found || !s.matchesConfigured(inspection, s.containerID) || !s.matchesStartedState(inspection) {
		s.positiveBlocked = true
		if err != nil {
			return err
		}
		return errors.New("generated migration started container observation is invalid")
	}
	if err := s.positiveGate(); err != nil {
		s.positiveBlocked = true
		return err
	}
	s.startedAt = inspection.StartedAt
	s.startAcknowledged = true
	return nil
}

func (e *GeneratedMigrationCommandExecution) WaitAndInspectExited() (int, error) {
	s, done, err := e.begin()
	if err != nil {
		return 0, err
	}
	defer done()
	if !s.startAcknowledged || s.waitAttempted || s.positiveBlocked || s.startedAt == "" {
		return 0, ErrInvalidTransition
	}
	s.waitAttempted = true
	if err := s.positiveGate(); err != nil {
		return 0, err
	}
	if err := contextError(s.positiveCtx); err != nil {
		return 0, err
	}
	result, runErr := s.runPrepared(s.positiveCtx, []string{"container", "wait", s.containerID}, s.repo.migrationOptions.Timeout)
	exitText := strings.TrimSpace(string(result.Stdout))
	truncated := result.StdoutTruncated || result.StderrTruncated
	clearGeneratedMigrationResult(&result)
	if runErr != nil || truncated {
		if runErr != nil {
			return 0, runErr
		}
		return 0, errors.New("generated migration wait acknowledgement is invalid")
	}
	exitCode, err := strconv.Atoi(exitText)
	if err != nil || exitCode < 0 || exitCode > 255 {
		return 0, errors.New("generated migration exit code is invalid")
	}
	inspection, found, err := s.inspect(s.positiveCtx, s.containerID)
	if err != nil || !found || !s.matchesConfigured(inspection, s.containerID) || !s.matchesExitedState(inspection, exitCode) {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("generated migration exited container observation is invalid")
	}
	if err := s.positiveGate(); err != nil {
		return 0, err
	}
	s.exitObserved = true
	s.exitCode = exitCode
	s.invocation.mu.Lock()
	s.invocation.exitObserved = true
	s.invocation.exitCode = exitCode
	s.invocation.mu.Unlock()
	return exitCode, nil
}

func (e *GeneratedMigrationCommandExecution) RemoveAndConfirm() error {
	s, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	if s.removeAttempted {
		return ErrInvalidTransition
	}
	s.removeAttempted = true
	// The thin runner may always invoke cleanup after CreateAndInspect. When
	// preparation or the final positive gate failed before Docker create was
	// entered, there is no container identity to inspect or remove; only the
	// privately staged environment (if any) needs release.
	if !s.createEntered {
		return s.cleanupEnvironment()
	}
	if !s.prepared || (s.containerID == "" && !s.positiveBlocked) {
		return ErrInvalidTransition
	}
	identity := s.containerID
	if identity == "" {
		identity = s.containerName
	}
	inspection, found, err := s.inspect(s.cleanupCtx, identity)
	if err != nil {
		return err
	}
	if found {
		if !lowerHex(inspection.ID, 64) || (s.containerID != "" && inspection.ID != s.containerID) || !s.matchesConfigured(inspection, inspection.ID) {
			return errors.New("generated migration cleanup ownership observation is invalid")
		}
		s.containerID = inspection.ID
		if err := s.cleanupGate(false); err != nil {
			return err
		}
		result, removeErr := s.runPrepared(s.cleanupCtx, []string{"container", "rm", "--force", s.containerID}, generatedMigrationCommandTimeout)
		clearGeneratedMigrationResult(&result)
		_, stillPresent, inspectErr := s.inspect(s.cleanupCtx, s.containerID)
		if inspectErr != nil {
			return errors.Join(removeErr, inspectErr)
		}
		if stillPresent {
			return errors.Join(removeErr, errors.New("generated migration container removal is unconfirmed"))
		}
		if err := s.cleanupGate(true); err != nil {
			return errors.Join(removeErr, err)
		}
		if generatedMigrationUncertainMutation(removeErr) {
			return errors.Join(removeErr, s.cleanupEnvironment())
		}
		if err := s.finishCleanupEvidence(); err != nil {
			return errors.Join(removeErr, err)
		}
		return nil
	}
	if err := s.cleanupGate(true); err != nil {
		return err
	}
	return s.finishCleanupEvidence()
}

func (e *GeneratedMigrationCommandExecution) Leave() {
	if e == nil || e.state == nil {
		return
	}
	s := e.state
	s.mu.Lock()
	if s.closing {
		for !s.left {
			s.changed.Wait()
		}
		s.mu.Unlock()
		return
	}
	s.closing = true
	for s.active {
		s.changed.Wait()
	}
	_ = s.cleanupEnvironment()
	cleanup := s.cleanup
	s.cleanup = nil
	if s.cancelPositive != nil {
		s.cancelPositive()
	}
	if s.cancelCleanup != nil {
		s.cancelCleanup()
	}
	s.mu.Unlock()
	if cleanup != nil {
		cleanup.Leave()
	}
	s.invocation.mu.Lock()
	s.invocation.commandLeft = true
	s.invocation.environmentClean = s.environmentClean
	s.invocation.mu.Unlock()
	s.mu.Lock()
	s.repo = nil
	s.owner = nil
	s.left = true
	s.changed.Broadcast()
	s.mu.Unlock()
}

func (e *GeneratedMigrationCommandExecution) begin() (*generatedMigrationCommandState, func(), error) {
	if e == nil || e.state == nil {
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	s := e.state
	s.mu.Lock()
	if s.closing || s.left || s.active || s.repo == nil || s.owner == nil || s.invocation == nil || s.cleanup == nil {
		s.mu.Unlock()
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	s.active = true
	s.mu.Unlock()
	done := func() {
		s.mu.Lock()
		s.active = false
		s.changed.Broadcast()
		s.mu.Unlock()
	}
	return s, done, nil
}

func (s *generatedMigrationCommandState) prepare() error {
	if err := validateGeneratedMigrationRequest(s.owner.request); err != nil {
		return err
	}
	if !s.repo.migrationConfigured() || generatedMigrationEmptyDirectory(s.repo.migrationOptions.DockerConfigDirectory) != nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	revision, err := s.owner.protectedPlanContext(s.positiveCtx)
	if err != nil {
		return err
	}
	migration := revision.Plan.Migration
	if migration == nil || deploymentplans.ValidateCommand(migration.Command) != nil || !validGeneratedMigrationRoot(migration.RootDirectory) {
		return ErrInvalidDeployment
	}
	componentRole := ""
	for _, component := range revision.Plan.Components {
		if component.Name == s.owner.request.ComponentName {
			componentRole = component.Role
			break
		}
	}
	if !validGeneratedImageText(componentRole, 128) {
		return ErrInvalidDeployment
	}
	configuration, err := s.repo.migrationConfiguration.ExportComponentMigrationForExecution(s.positiveCtx, s.owner.request.Claim.ApplicationID(), s.owner.request.ConfigurationRevisionID, s.owner.request.ConfigurationRevisionNumber, s.owner.request.DeploymentPlanRevisionID, s.owner.request.DeploymentPlanRevisionNumber, s.owner.request.ComponentName, migration.EnvironmentKeys)
	if err != nil {
		return err
	}
	defer configuration.Clear()
	if s.repo.afterGeneratedMigrationConfigurationExport != nil {
		s.repo.afterGeneratedMigrationConfigurationExport(&configuration)
	}
	if configuration.RevisionID != s.owner.request.ConfigurationRevisionID || configuration.RevisionNumber != s.owner.request.ConfigurationRevisionNumber {
		return ErrInvalidDeployment
	}
	contents := configuration.Environment
	lease, err := s.repo.migrationEnvironment.StageGeneratedMigration(s.owner.request.DeploymentID, s.owner.request.Claim.Attempt(), contents)
	clear(contents)
	configuration.Environment = nil
	if err != nil || nilInterface(lease) {
		if !nilInterface(lease) {
			_ = lease.Cleanup()
		}
		if err != nil {
			return err
		}
		return ErrInvalidDeployment
	}
	environmentPath := lease.Path()
	if !generatedMigrationCleanAbsolute(environmentPath) {
		_ = lease.Cleanup()
		return ErrInvalidDeployment
	}
	network, err := generatedidentity.DescribeAppNetwork(s.owner.request.Claim.ApplicationID())
	if err != nil {
		_ = lease.Cleanup()
		return ErrInvalidDeployment
	}
	working := "/workspace"
	if migration.RootDirectory != "." {
		working += "/" + migration.RootDirectory
	}
	s.command = migration.Command
	s.componentRole = componentRole
	s.workingDirectory = working
	s.networkName = network.Name
	s.containerName = "rig-migration-" + strings.ReplaceAll(s.owner.request.DeploymentID, "-", "")
	s.labels = map[string]string{
		"io.rig.managed": "generated-migration", "io.rig.application": s.owner.request.Claim.ApplicationID(),
		"io.rig.release": s.owner.request.ReleaseID, "io.rig.deployment": s.owner.request.DeploymentID,
		"io.rig.artifact": s.owner.request.ArtifactID, "io.rig.plan": s.owner.request.DeploymentPlanRevisionID,
		"io.rig.component": s.owner.request.ComponentName, "io.rig.invocation": s.invocation.id,
	}
	s.environment = lease
	s.environmentPath = environmentPath
	s.prepared = true
	return nil
}

func (s *generatedMigrationCommandState) createArgs() []string {
	args := []string{"container", "create", "--name", s.containerName}
	keys := make([]string, 0, len(s.labels))
	for key := range s.labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+s.labels[key])
	}
	return append(args,
		"--network", s.networkName, "--env-file", s.environmentPath, "--user", "node", "--workdir", s.workingDirectory,
		"--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=67108864,mode=1777",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges=true",
		"--memory", "536870912", "--memory-swap", "536870912", "--cpu-period", "100000", "--cpu-quota", "100000", "--pids-limit", "256",
		"--ulimit", "nofile=1024:1024", "--init", "--log-driver", "json-file", "--log-opt", "max-size=1m", "--log-opt", "max-file=1",
		"--restart", "no", "--entrypoint", "/usr/local/bin/rig-entrypoint", s.owner.request.ImageContentID, "/bin/sh", "-lc", s.command,
	)
}

func (s *generatedMigrationCommandState) positiveGate() error {
	if err := contextError(s.positiveCtx); err != nil {
		return err
	}
	if err := generatedMigrationEmptyDirectory(s.repo.migrationOptions.DockerConfigDirectory); err != nil {
		return err
	}
	if _, err := s.owner.protectedPlanContext(s.positiveCtx); err != nil {
		return err
	}
	tx, err := s.repo.db.BeginTx(s.positiveCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gateDatabaseError(s.positiveCtx, err)
	}
	defer tx.Rollback()
	if err := s.pinned.CheckGeneratedImageResultTx(tx, s.owner.request.Claim, s.owner.request.DeploymentID); err != nil {
		return err
	}
	if err := s.owner.validateTx(s.positiveCtx, tx, generatedruntimestate.MigrationRunning); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.positiveCtx, err)
	}
	return contextError(s.positiveCtx)
}

func (s *generatedMigrationCommandState) cleanupGate(observed bool) error {
	if err := contextError(s.cleanupCtx); err != nil {
		return err
	}
	if err := generatedMigrationEmptyDirectory(s.repo.migrationOptions.DockerConfigDirectory); err != nil {
		return err
	}
	tx, err := s.repo.db.BeginTx(s.cleanupCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gateDatabaseError(s.cleanupCtx, err)
	}
	defer tx.Rollback()
	if observed {
		err = s.cleanup.CheckRemovedObservationTx(tx, s.owner.request.Claim, s.owner.request.DeploymentID, s.invocation.id)
	} else {
		err = s.cleanup.CheckBeforeRemoveTx(tx, s.owner.request.Claim, s.owner.request.DeploymentID, s.invocation.id)
	}
	if err != nil {
		return err
	}
	if err := s.owner.validateTx(s.cleanupCtx, tx, generatedruntimestate.MigrationRunning); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.cleanupCtx, err)
	}
	return contextError(s.cleanupCtx)
}

func (s *generatedMigrationCommandState) run(ctx context.Context, args []string, timeout time.Duration) (runtimeprocess.CommandResult, error) {
	if err := contextError(ctx); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if err := generatedMigrationEmptyDirectory(s.repo.migrationOptions.DockerConfigDirectory); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return s.runPrepared(ctx, args, timeout)
}

func (s *generatedMigrationCommandState) runPrepared(ctx context.Context, args []string, timeout time.Duration) (runtimeprocess.CommandResult, error) {
	if err := contextError(ctx); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return s.repo.migrationProcess.Run(ctx, runtimeprocess.CommandRequest{
		Executable: s.repo.migrationOptions.DockerExecutable, Args: append([]string(nil), args...),
		Directory: s.repo.migrationOptions.WorkingDirectory, Env: append([]string(nil), s.repo.migrationDockerEnv...),
		Timeout: timeout, OutputLimit: s.repo.migrationOptions.OutputLimit,
	})
}

func (s *generatedMigrationCommandState) attestImage(ctx context.Context) error {
	result, runErr := s.run(ctx, []string{"image", "inspect", "--format", generatedMigrationImageInspectFormat, s.owner.request.ImageContentID}, generatedMigrationCommandTimeout)
	defer clearGeneratedMigrationResult(&result)
	if runErr != nil {
		return errors.Join(errors.New("generated migration image inspection failed"), runErr, contextError(ctx))
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return errors.New("generated migration image inspection output is truncated")
	}
	inspection, err := decodeGeneratedMigrationImageInspection(result.Stdout)
	if err != nil {
		return err
	}
	expectedLabels := map[string]string{
		"io.rig.managed": "generated-image", "io.rig.application": s.owner.request.Claim.ApplicationID(),
		"io.rig.release": s.owner.request.ReleaseID, "io.rig.artifact": s.owner.request.ArtifactID,
		"io.rig.plan": s.owner.request.DeploymentPlanRevisionID, "io.rig.component": s.owner.request.ComponentName,
		"io.rig.role": s.componentRole, "io.rig.definition": s.owner.request.BuildDefinitionDigest,
	}
	if inspection.ID != s.owner.request.ImageContentID || inspection.Size < 1 || !generatedMigrationContainsLabels(inspection.Labels, expectedLabels) ||
		inspection.User != "node" || inspection.WorkingDirectory != "/workspace" || len(inspection.Entrypoint) != 1 || inspection.Entrypoint[0] != "/usr/local/bin/rig-entrypoint" {
		return errors.New("generated migration image observation is invalid")
	}
	return contextError(ctx)
}

func (s *generatedMigrationCommandState) attestNetwork(ctx context.Context) error {
	result, runErr := s.run(ctx, []string{"network", "inspect", "--format", generatedMigrationNetworkInspectFormat, s.networkName}, generatedMigrationCommandTimeout)
	defer clearGeneratedMigrationResult(&result)
	if runErr != nil {
		return errors.Join(errors.New("generated migration network inspection failed"), runErr, contextError(ctx))
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return errors.New("generated migration network inspection output is truncated")
	}
	inspection, err := decodeGeneratedMigrationNetworkInspection(result.Stdout)
	if err != nil {
		return err
	}
	expectedLabels := map[string]string{"io.rig.managed": generatedMigrationNetworkLabel, "io.rig.application": s.owner.request.Claim.ApplicationID()}
	if inspection.Name != s.networkName || inspection.Driver != "bridge" || inspection.Scope != "local" || inspection.Internal || !generatedMigrationContainsLabels(inspection.Labels, expectedLabels) {
		return errors.New("generated migration network observation is invalid")
	}
	return contextError(ctx)
}

func (s *generatedMigrationCommandState) inspect(ctx context.Context, identity string) (generatedMigrationInspection, bool, error) {
	result, runErr := s.run(ctx, []string{"container", "inspect", "--format", generatedMigrationInspectFormat, identity}, generatedMigrationCommandTimeout)
	defer clearGeneratedMigrationResult(&result)
	if runErr != nil {
		definitive := contextError(ctx) == nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) &&
			!errors.Is(runErr, runtimeprocess.ErrTerminationFailed) && !result.StdoutTruncated && !result.StderrTruncated && generatedMigrationDefiniteAbsence(result, runErr, identity)
		if definitive {
			return generatedMigrationInspection{}, false, nil
		}
		return generatedMigrationInspection{}, false, errors.Join(runErr, contextError(ctx))
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return generatedMigrationInspection{}, false, errors.New("generated migration inspection output is truncated")
	}
	inspection, err := decodeGeneratedMigrationInspection(result.Stdout)
	if err != nil {
		return generatedMigrationInspection{}, false, err
	}
	return inspection, true, nil
}

func (s *generatedMigrationCommandState) matchesConfigured(value generatedMigrationInspection, id string) bool {
	if value.ID != id || strings.TrimPrefix(value.Name, "/") != s.containerName || value.Image != s.owner.request.ImageContentID || value.ConfigImage != s.owner.request.ImageContentID || value.User != "node" || value.WorkingDirectory != s.workingDirectory ||
		len(value.Entrypoint) != 1 || value.Entrypoint[0] != "/usr/local/bin/rig-entrypoint" || len(value.Command) != 3 || value.Command[0] != "/bin/sh" || value.Command[1] != "-lc" || value.Command[2] != s.command || !generatedMigrationContainsLabels(value.Labels, s.labels) || len(value.Volumes) != 0 {
		return false
	}
	if value.Memory != 536870912 || value.MemorySwap != 536870912 || value.CPUPeriod != 100000 || value.CPUQuota != 100000 || value.PidsLimit != 256 || len(value.Ulimits) != 1 || value.Ulimits[0] != (generatedMigrationUlimit{Name: "nofile", Soft: 1024, Hard: 1024}) || !value.Init || value.NetworkMode != s.networkName || !value.ReadonlyRootfs || value.Privileged || len(value.CapAdd) != 0 || len(value.CapDrop) != 1 || !strings.EqualFold(value.CapDrop[0], "ALL") || !generatedMigrationNoNewPrivileges(value.SecurityOptions) || len(value.Binds) != 0 || len(value.HostMounts) != 0 || len(value.PortBindings) != 0 || value.LogType != "json-file" || value.LogConfig["max-size"] != "1m" || value.LogConfig["max-file"] != "1" || value.Restart != "no" || !generatedMigrationOnlyTmpfsMount(value.Mounts) {
		return false
	}
	tmpfs, ok := value.Tmpfs["/tmp"]
	return ok && len(value.Tmpfs) == 1 && generatedMigrationCommaValues(tmpfs, []string{"rw", "noexec", "nosuid", "nodev", "size=67108864", "mode=1777"})
}

func (s *generatedMigrationCommandState) matchesCreatedState(value generatedMigrationInspection) bool {
	return value.Status == "created" && !value.Running && !value.Paused && !value.Restarting && !value.Dead && value.StartedAt == "0001-01-01T00:00:00Z"
}

func (s *generatedMigrationCommandState) matchesStartedState(value generatedMigrationInspection) bool {
	if value.Paused || value.Restarting || value.Dead || !generatedMigrationActualStart(value.StartedAt) {
		return false
	}
	if !((value.Status == "running" && value.Running) || (value.Status == "exited" && !value.Running)) {
		return false
	}
	_, attached := value.Networks[s.networkName]
	return attached && len(value.Networks) == 1
}

func (s *generatedMigrationCommandState) matchesExitedState(value generatedMigrationInspection, exitCode int) bool {
	if value.Status != "exited" || value.Running || value.Paused || value.Restarting || value.Dead || value.ExitCode != exitCode || value.StartedAt != s.startedAt {
		return false
	}
	_, attached := value.Networks[s.networkName]
	return attached && len(value.Networks) == 1
}

func (s *generatedMigrationCommandState) cleanupEnvironment() error {
	if s.environment == nil {
		return nil
	}
	err := s.environment.Cleanup()
	s.environment = nil
	s.environmentPath = ""
	if err == nil {
		s.environmentClean = true
	}
	return err
}

func (s *generatedMigrationCommandState) finishCleanupEvidence() error {
	s.absenceObserved = true
	if err := s.cleanupEnvironment(); err != nil {
		return err
	}
	s.invocation.mu.Lock()
	s.invocation.absenceObserved = true
	s.invocation.environmentClean = true
	s.invocation.mu.Unlock()
	return nil
}

func generatedMigrationActualStart(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero()
}

func validGeneratedMigrationImageID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func validGeneratedMigrationRoot(value string) bool {
	return value != "" && value == path.Clean(value) && value != ".." && !strings.HasPrefix(value, "../") && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\")
}

func generatedMigrationCleanAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !pathsecurity.RejectWindowsNamespace(value)
}

func generatedMigrationLocalEndpoint(value string) bool {
	if value == "" || value == "npipe:////./pipe/docker_engine" {
		return true
	}
	return strings.HasPrefix(value, "unix://") && generatedMigrationCleanAbsolute(strings.TrimPrefix(value, "unix://"))
}

func generatedMigrationEmptyDirectory(directory string) error {
	if !generatedMigrationCleanAbsolute(directory) {
		return errors.New("invalid Docker configuration directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		return errors.New("Docker configuration directory must be empty")
	}
	return nil
}

func generatedMigrationContainsLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func generatedMigrationOnlyTmpfsMount(values []generatedMigrationMount) bool {
	for _, value := range values {
		if value.Type != "tmpfs" || value.Source != "" || value.Destination != "/tmp" {
			return false
		}
	}
	return len(values) <= 1
}

func generatedMigrationNoNewPrivileges(values []string) bool {
	if len(values) != 1 {
		return false
	}
	switch strings.ToLower(values[0]) {
	case "no-new-privileges", "no-new-privileges:true", "no-new-privileges=true":
		return true
	default:
		return false
	}
}

func generatedMigrationCommaValues(value string, expected []string) bool {
	actual := strings.Split(value, ",")
	if len(actual) != len(expected) {
		return false
	}
	sort.Strings(actual)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	for i := range want {
		if actual[i] != want[i] {
			return false
		}
	}
	return true
}

func generatedMigrationDefiniteAbsence(result runtimeprocess.CommandResult, runErr error, identity string) bool {
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

func generatedMigrationUncertainMutation(err error) bool {
	if err == nil {
		return false
	}
	exited, ok := err.(*exec.ExitError)
	return !ok || exited.ProcessState == nil || !exited.ProcessState.Exited()
}

func clearGeneratedMigrationResult(result *runtimeprocess.CommandResult) {
	clear(result.Stdout)
	clear(result.Stderr)
	result.Stdout = nil
	result.Stderr = nil
}
