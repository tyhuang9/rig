package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainStartupDisposition uint8

const (
	generatedDomainStartupRefused generatedDomainStartupDisposition = iota
	generatedDomainStartupEmpty
	generatedDomainStartupStable
	generatedDomainStartupRecoveryRequired
)

type generatedDomainStartupReason uint8

const (
	generatedDomainStartupReasonInvalid generatedDomainStartupReason = iota
	generatedDomainStartupReasonEmpty
	generatedDomainStartupReasonStable
	generatedDomainStartupReasonRecovery
	generatedDomainStartupReasonColdAccounting
	generatedDomainStartupReasonAmbiguous
)

type generatedDomainStartupRecoveryKind uint8

const (
	generatedDomainStartupRecoveryUpgrade generatedDomainStartupRecoveryKind = iota + 1
	generatedDomainStartupRecoveryGrant
	generatedDomainStartupRecoveryDisable
	generatedDomainStartupRecoveryBatch
	generatedDomainStartupRecoveryRebind
)

type generatedDomainStartupRecovery struct {
	kind        generatedDomainStartupRecoveryKind
	operationID string
}

type generatedDomainStartupClassification struct {
	disposition generatedDomainStartupDisposition
	reason      generatedDomainStartupReason
	recovery    []generatedDomainStartupRecovery
}

// generatedDomainStartupObservation is one complete, repeatedly checked
// read-only startup observation. It is retained only by the private domain
// construction so an exact completed join can reattest the same durable and
// physical state without recreating a Manager, budget, or candidate owner.
type generatedDomainStartupObservation struct {
	classification generatedDomainStartupClassification
	database       deployments.GeneratedDomainStartupSnapshot
	protected      generatedDomainProtectedStartupSnapshot
	docker         generatedDomainDockerStartupSnapshot
}

const generatedDomainStartupObservationTimeout = v2ObservationTimeout

var (
	errGeneratedDomainStartupInvalid = errors.New("generated domain startup input is invalid")
	errGeneratedDomainStartupRead    = errors.New("generated domain startup observation failed")
	errGeneratedDomainStartupChanged = errors.New("generated domain startup observation changed")
)

// inspectGeneratedDomainStartup is deliberately private and descriptive. It
// owns no Manager, lock, reservation, recovery driver, or effect callback.
func inspectGeneratedDomainStartup(ctx context.Context, stores *deployments.GeneratedOwnerStores,
	runner runtimeprocess.CommandRunner, options generatedDomainOptions,
) (generatedDomainStartupClassification, error) {
	observation, err := inspectGeneratedDomainStartupObservation(ctx, stores, runner, options)
	return observation.classification, err
}

func inspectGeneratedDomainStartupObservation(ctx context.Context, stores *deployments.GeneratedOwnerStores,
	runner runtimeprocess.CommandRunner, options generatedDomainOptions,
) (generatedDomainStartupObservation, error) {
	var empty generatedDomainStartupObservation
	if ctx == nil || stores == nil || !generatedCandidatePointerIdentity(runner) {
		return empty, errGeneratedDomainStartupInvalid
	}
	normalized, ok := normalizeGeneratedDomainOptions(options)
	if !ok {
		return empty, errGeneratedDomainStartupInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	readCtx, cancel := context.WithTimeout(ctx, generatedDomainStartupObservationTimeout)
	defer cancel()

	storesCopy := *stores
	identity, paths, err := inspectGeneratedDomainPhysicalIdentity(readCtx, &storesCopy, runner, normalized)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	if _, err := paths.recheck(false); err != nil {
		return empty, errGeneratedDomainStartupChanged
	}
	reader, err := newGeneratedDomainStartupReader(runner, normalized, identity.manager.daemonID)
	if err != nil {
		return empty, errGeneratedDomainStartupInvalid
	}

	database1, err := storesCopy.GeneratedDomainStartupSnapshot(readCtx)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	protected1, err := readGeneratedDomainProtectedStartup(readCtx, normalized.DataRoot)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	expectation1, err := resolveGeneratedDomainGatewayExpectation(database1, protected1)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	recovery1 := resolveGeneratedDomainRecoveryDockerExpectation(database1, protected1)
	census1, err := resolveGeneratedDomainDockerCensus(protected1)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	reader.expectation = generatedDomainStartupObservationGateway(expectation1, recovery1)
	reader.census = census1
	reader.recovery = recovery1
	docker1, err := readGeneratedDomainDockerStartup(readCtx, reader)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	database2, err := storesCopy.GeneratedDomainStartupSnapshot(readCtx)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	protected2, err := readGeneratedDomainProtectedStartup(readCtx, normalized.DataRoot)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	expectation2, err := resolveGeneratedDomainGatewayExpectation(database2, protected2)
	recovery2 := resolveGeneratedDomainRecoveryDockerExpectation(database2, protected2)
	census2, censusErr := resolveGeneratedDomainDockerCensus(protected2)
	if err != nil || censusErr != nil || !reflect.DeepEqual(expectation1, expectation2) ||
		!reflect.DeepEqual(recovery1, recovery2) || !reflect.DeepEqual(census1, census2) {
		return empty, errGeneratedDomainStartupChanged
	}
	reader.expectation = generatedDomainStartupObservationGateway(expectation2, recovery2)
	reader.census = census2
	reader.recovery = recovery2
	docker2, err := readGeneratedDomainDockerStartup(readCtx, reader)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	if !reflect.DeepEqual(database1, database2) ||
		!sameGeneratedDomainProtectedStartup(protected1, protected2) ||
		!sameGeneratedDomainDockerStartup(docker1, docker2) {
		return empty, errGeneratedDomainStartupChanged
	}
	if _, err := paths.recheck(false); err != nil ||
		generatedCandidateValidateCommandDirectories(generatedDomainCandidateOptions(normalized)) != nil {
		return empty, errGeneratedDomainStartupChanged
	}
	daemonID, err := inspectGeneratedDockerDaemonIdentity(readCtx, runner, normalized.DockerExecutable,
		reader.environment, normalized.CommandTimeout)
	if err != nil || daemonID != identity.manager.daemonID {
		return empty, errGeneratedDomainStartupChanged
	}
	database3, err := storesCopy.GeneratedDomainStartupSnapshot(readCtx)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	protected3, err := readGeneratedDomainProtectedStartup(readCtx, normalized.DataRoot)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	if !reflect.DeepEqual(database2, database3) ||
		!sameGeneratedDomainProtectedStartup(protected2, protected3) || readCtx.Err() != nil {
		return empty, errGeneratedDomainStartupChanged
	}
	classification, err := classifyGeneratedDomainStartup(database3, protected3, docker2)
	if err != nil {
		return empty, errGeneratedDomainStartupRead
	}
	classification.recovery = canonicalGeneratedDomainStartupRecoveries(classification.recovery)
	return generatedDomainStartupObservation{classification: classification, database: database3,
		protected: protected3, docker: docker2}, nil
}

func sameGeneratedDomainStartupObservation(left, right generatedDomainStartupObservation) bool {
	return reflect.DeepEqual(left.classification, right.classification) &&
		reflect.DeepEqual(left.database, right.database) &&
		sameGeneratedDomainProtectedStartup(left.protected, right.protected) &&
		sameGeneratedDomainDockerStartup(left.docker, right.docker)
}

func canonicalGeneratedDomainStartupRecoveries(values []generatedDomainStartupRecovery) []generatedDomainStartupRecovery {
	result := append([]generatedDomainStartupRecovery(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].kind != result[j].kind {
			return result[i].kind < result[j].kind
		}
		return result[i].operationID < result[j].operationID
	})
	if len(result) == 0 {
		return nil
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func generatedDomainStartupCommandTimeout(options generatedDomainOptions, remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	if options.CommandTimeout < remaining {
		return options.CommandTimeout
	}
	return remaining
}
