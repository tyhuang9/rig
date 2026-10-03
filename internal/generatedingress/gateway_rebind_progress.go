package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

const (
	gatewayRebindProgressVersion         = 1
	gatewayRebindProgressPurpose         = "hostd/generated-ingress/rebind/progress/v1"
	gatewayRebindProgressFilenamePrefix  = "gateway-rebind-progress.v1.g"
	gatewayRebindProgressSequenceDigits  = 2
	maxGatewayRebindProgressBytes        = 32 << 10
	gatewayRebindProgressMaximumSequence = 3
)

type gatewayRebindProgressPhase string

const (
	gatewayRebindProgressSuccessorIntent gatewayRebindProgressPhase = "successor_intent"
	gatewayRebindProgressStageIntent     gatewayRebindProgressPhase = "stage_intent"
)

// gatewayRebindProgressRecord is immutable history for the initial rebind.
// The first two records prepare only. Later records bind exact observed effect
// results; no record authorizes an administrator, releases a fence, writes
// SQLite, or publishes a route. Every effect path must freshly authenticate
// the pinned Docker image and topology while it holds its required locks and
// lease.
type gatewayRebindProgressRecord struct {
	Version               int                        `json:"version"`
	Purpose               string                     `json:"purpose"`
	Generation            uint64                     `json:"generation"`
	OperationID           string                     `json:"operationId"`
	Sequence              uint64                     `json:"sequence"`
	Phase                 gatewayRebindProgressPhase `json:"phase"`
	OccurredAt            string                     `json:"occurredAt"`
	ProtectedIntentDigest string                     `json:"protectedIntentDigest"`
	PreviousDigest        string                     `json:"previousDigest,omitempty"`
	Stage                 *gatewayRebindStageIntent  `json:"stage,omitempty"`
	Digest                string                     `json:"digest"`
}

// gatewayRebindStageIntent keeps the approved Caddy content digest distinct
// from the observed Docker image object ID. The latter is merely a pinned
// observation in this storage slice: validation proves its syntax and binding,
// never its current Docker provenance.
type gatewayRebindStageIntent struct {
	Identity                 gatewayRebindSuccessorIdentity      `json:"identity"`
	ApprovedCaddyImageDigest string                              `json:"approvedCaddyImageDigest"`
	ObservedDockerImageID    string                              `json:"observedDockerImageId"`
	NetworkPlan              gatewayRebindSuccessorIntentNetwork `json:"networkPlan"`
	NetworkPlanDigest        string                              `json:"networkPlanDigest"`
	NetworkTopologyDigest    string                              `json:"networkTopologyDigest"`
	// Network is populated only after the exact successor ingress network has
	// been observed under the effect locks. Sequence two must keep it empty;
	// sequence three may bind it once and every replay must be exact.
	Network *gatewayRebindStageNetworkBinding `json:"network,omitempty"`
}

type gatewayRebindStageNetworkBinding struct {
	ID              string `json:"id"`
	OwnershipDigest string `json:"ownershipDigest"`
}

// gatewayRebindStageIntentObservation is deliberately explicit so callers
// supply both the clock value and observed Docker identity. This slice does
// not generate time, IDs, or topology observations on its own.
type gatewayRebindStageIntentObservation struct {
	OccurredAt            time.Time
	ObservedDockerImageID string
	NetworkTopologyDigest string
}

type gatewayRebindProgressStore struct {
	directory   *stateStore
	dataRoot    string
	generation  uint64
	operationID string
	sequence    uint64
	path        string
	purpose     string
}

type gatewayRebindProgressSelection struct {
	Store      *gatewayRebindProgressStore
	Generation uint64
	Sequence   uint64
	Record     gatewayRebindProgressRecord
	Existing   bool
}

func newGatewayRebindSuccessorIntentProgress(intent gatewayRebindProtectedIntent,
	occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor progress input")
	}
	value := gatewayRebindProgressRecord{
		Version:    gatewayRebindProgressVersion,
		Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 1, Phase: gatewayRebindProgressSuccessorIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	digest, err := gatewayRebindProgressDigest(value)
	if err != nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor progress input")
	}
	value.Digest = digest
	if !validGatewayRebindProgressRecord(value) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor progress input")
	}
	return value, nil
}

func newGatewayRebindStageIntentProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, observation gatewayRebindStageIntentObservation,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) ||
		!validGatewayRebindProgressRecord(previous) || previous.Generation != intent.Generation ||
		previous.OperationID != intent.OperationID || previous.Sequence != 1 ||
		previous.Phase != gatewayRebindProgressSuccessorIntent || previous.ProtectedIntentDigest != intent.Digest ||
		previous.PreviousDigest != "" || previous.Stage != nil || !validGatewayRebindProgressTime(observation.OccurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !observation.OccurredAt.After(previousAt) || !validSHA256(observation.ObservedDockerImageID) ||
		!validSHA256(observation.NetworkTopologyDigest) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	stage := gatewayRebindStageIntent{
		Identity:                 intent.Intent.Identity,
		ApprovedCaddyImageDigest: intent.Intent.Identity.CaddyImageDigest,
		ObservedDockerImageID:    observation.ObservedDockerImageID,
		NetworkPlan:              intent.Intent.Network, NetworkPlanDigest: intent.Intent.NetworkDigest,
		NetworkTopologyDigest: observation.NetworkTopologyDigest,
	}
	value := gatewayRebindProgressRecord{
		Version:    gatewayRebindProgressVersion,
		Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 2, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: observation.OccurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	digest, err := gatewayRebindProgressDigest(value)
	if err != nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	value.Digest = digest
	if !validGatewayRebindProgressRecord(value) || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	return value, nil
}

func newGatewayRebindStageNetworkProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, networkID, ownershipDigest string, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 2 || previous.Phase != gatewayRebindProgressStageIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.Network != nil || !gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validContainerID(networkID) || normalizeID(networkID) != networkID || !validSHA256(ownershipDigest) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage network progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage network progress input")
	}
	stage := *previous.Stage
	stage.Network = &gatewayRebindStageNetworkBinding{ID: networkID, OwnershipDigest: ownershipDigest}
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 3, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage network progress input")
	}
	return value, nil
}

func gatewayRebindProgressDigest(value gatewayRebindProgressRecord) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindProgressRecord(value gatewayRebindProgressRecord) bool {
	if value.Version != gatewayRebindProgressVersion || value.Generation == 0 || value.Generation == math.MaxUint64 ||
		!validCanonicalUUID(value.OperationID) || value.Sequence == 0 || value.Sequence > gatewayRebindProgressMaximumSequence ||
		!validSHA256(value.ProtectedIntentDigest) || !validSHA256(value.Digest) {
		return false
	}
	_, expectedPurpose := gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	if value.Purpose != expectedPurpose {
		return false
	}
	if _, err := parseGatewayRebindProgressTime(value.OccurredAt); err != nil {
		return false
	}
	switch value.Phase {
	case gatewayRebindProgressSuccessorIntent:
		if value.Sequence != 1 || value.PreviousDigest != "" || value.Stage != nil {
			return false
		}
	case gatewayRebindProgressStageIntent:
		if (value.Sequence != 2 && value.Sequence != 3) || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	default:
		return false
	}
	digest, err := gatewayRebindProgressDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindStageIntent(value gatewayRebindStageIntent) bool {
	identityDigest, err := gatewayRebindSuccessorIdentityDigest(value.Identity)
	return err == nil && identityDigest == value.Identity.Digest &&
		value.ApprovedCaddyImageDigest == value.Identity.CaddyImageDigest &&
		value.ApprovedCaddyImageDigest == gatewayV2CaddyImageDigest &&
		validSHA256(value.ObservedDockerImageID) && validGatewayV2NetworkPlan(gatewayV2NetworkPlan(value.NetworkPlan)) &&
		validSHA256(value.NetworkPlanDigest) && validSHA256(value.NetworkTopologyDigest) &&
		(value.Network == nil || (validContainerID(value.Network.ID) && normalizeID(value.Network.ID) == value.Network.ID &&
			validSHA256(value.Network.OwnershipDigest)))
}

func gatewayRebindStageIntentMatchesProtectedIntent(value gatewayRebindProgressRecord,
	intent gatewayRebindProtectedIntent,
) bool {
	return value.Stage != nil && value.Generation == intent.Generation && value.OperationID == intent.OperationID &&
		value.ProtectedIntentDigest == intent.Digest && value.Stage.Identity == intent.Intent.Identity &&
		value.Stage.ApprovedCaddyImageDigest == intent.Intent.Identity.CaddyImageDigest &&
		value.Stage.NetworkPlan == intent.Intent.Network && value.Stage.NetworkPlanDigest == intent.Intent.NetworkDigest
}

func validGatewayRebindProgressTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, err := parseGatewayRebindProgressTime(value.UTC().Format(time.RFC3339Nano))
	return err == nil
}

func parseGatewayRebindProgressTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("invalid generated ingress rebind progress timestamp")
	}
	return parsed.UTC(), nil
}

func newGatewayRebindProgressStore(dataRoot string, generation uint64, operationID string,
	sequence uint64,
) (*gatewayRebindProgressStore, error) {
	if generation == 0 || generation == math.MaxUint64 || !validCanonicalUUID(operationID) || sequence == 0 || sequence > gatewayRebindProgressMaximumSequence {
		return nil, errors.New("invalid generated ingress rebind progress store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindProgressName(generation, operationID, sequence)
	return &gatewayRebindProgressStore{
		directory: directory, dataRoot: dataRoot, generation: generation, operationID: operationID,
		sequence: sequence, path: filepath.Join(directory.root, name), purpose: purpose,
	}, nil
}

func gatewayRebindProgressName(generation uint64, operationID string, sequence uint64) (string, string) {
	token := fmt.Sprintf("%0*d.%s.s%0*d", gatewayV2GenerationDigits, generation, operationID,
		gatewayRebindProgressSequenceDigits, sequence)
	scope := fmt.Sprintf("generation/%0*d/%s/sequence/%0*d", gatewayV2GenerationDigits, generation,
		operationID, gatewayRebindProgressSequenceDigits, sequence)
	return gatewayRebindProgressFilenamePrefix + token + ".bundle", gatewayRebindProgressPurpose + "/" + scope
}

// installExact uses create-only storage, exact readback, and a fresh strict
// rebind history scan before it reports success. An ambiguous post-install
// error remains an error. A retry can succeed only if the newly observed,
// complete protected history proves this exact record.
func (s *gatewayRebindProgressStore) installExact(ctx context.Context, value gatewayRebindProgressRecord) error {
	if s == nil || s.directory == nil || ctx == nil || ctx.Err() != nil ||
		s.generation != value.Generation || s.operationID != value.OperationID || s.sequence != value.Sequence ||
		!validGatewayRebindProgressRecord(value) {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("invalid generated ingress rebind progress install")
	}
	manager := &Manager{store: s.directory, options: Options{DataRoot: s.dataRoot}}
	before, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !gatewayRebindProgressInstallPermitted(before, value) {
		return errors.New("generated ingress rebind progress install is not the current exact replay")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayRebindProgressBytes); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return fmt.Errorf("generated ingress rebind progress history readback failed: %w", err)
	}
	for _, installed := range history.Progress {
		if installed.Generation == value.Generation && installed.Sequence == value.Sequence &&
			installed.Store != nil && installed.Store.path == s.path && reflect.DeepEqual(installed.Record, value) {
			return nil
		}
	}
	return errors.New("generated ingress rebind progress readback mismatch")
}

func gatewayRebindProgressInstallPermitted(history gatewayRebindProtectedIntentHistory,
	value gatewayRebindProgressRecord,
) bool {
	if len(history.Intents) != 1 || history.Intents[0].Generation != value.Generation ||
		history.Intents[0].Intent.OperationID != value.OperationID ||
		history.Intents[0].Intent.Digest != value.ProtectedIntentDigest {
		return false
	}
	previous := make([]gatewayRebindProgressSelection, 0, value.Sequence-1)
	for _, existing := range history.Progress {
		if existing.Generation != value.Generation {
			return false
		}
		if existing.Sequence == value.Sequence {
			if !gatewayRebindProgressMatchesIntent(value, history.Intents[0].Intent, previous) {
				return false
			}
			return reflect.DeepEqual(existing.Record, value)
		}
		if existing.Sequence < value.Sequence {
			previous = append(previous, existing)
		}
	}
	return value.Sequence == uint64(len(history.Progress)+1) &&
		gatewayRebindProgressMatchesIntent(value, history.Intents[0].Intent, previous)
}

func (s *gatewayRebindProgressStore) load() (gatewayRebindProgressRecord, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind progress store")
	}
	var value gatewayRebindProgressRecord
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayRebindProgressBytes, &value); err != nil ||
		value.Generation != s.generation || value.OperationID != s.operationID || value.Sequence != s.sequence ||
		!validGatewayRebindProgressRecord(value) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind progress")
	}
	return value, nil
}

func scanGatewayRebindProgressForIntent(dataRoot string, intent gatewayRebindProtectedIntent,
	artifacts gatewayRebindHistoryGeneration,
) ([]gatewayRebindProgressSelection, error) {
	sequences := make([]uint64, 0, len(artifacts.progress))
	for sequence := range artifacts.progress {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	result := make([]gatewayRebindProgressSelection, 0, len(sequences))
	for index, sequence := range sequences {
		if sequence != uint64(index+1) || sequence > gatewayRebindProgressMaximumSequence {
			return nil, errors.New("generated ingress rebind progress history has a sequence gap")
		}
		artifact := artifacts.progress[sequence]
		store, err := newGatewayRebindProgressStore(dataRoot, intent.Generation, intent.OperationID, sequence)
		if err != nil || store.path != artifact.path {
			return nil, errors.New("generated ingress rebind progress history path is invalid")
		}
		value, err := store.load()
		if err != nil || !gatewayRebindProgressMatchesIntent(value, intent, result) {
			return nil, errors.New("generated ingress rebind progress history is invalid")
		}
		result = append(result, gatewayRebindProgressSelection{
			Store: store, Generation: intent.Generation, Sequence: sequence, Record: value, Existing: true,
		})
	}
	return result, nil
}

func gatewayRebindProgressMatchesIntent(value gatewayRebindProgressRecord, intent gatewayRebindProtectedIntent,
	previous []gatewayRebindProgressSelection,
) bool {
	if !validGatewayRebindProgressRecord(value) || value.Generation != intent.Generation ||
		value.OperationID != intent.OperationID || value.ProtectedIntentDigest != intent.Digest {
		return false
	}
	switch value.Sequence {
	case 1:
		return value.Phase == gatewayRebindProgressSuccessorIntent && value.PreviousDigest == "" && value.Stage == nil
	case 2:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 1 ||
			value.PreviousDigest != previous[0].Record.Digest || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[0].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt) && value.Stage.Network == nil
	case 3:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 2 ||
			value.PreviousDigest != previous[1].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[1].Record.Stage == nil || previous[1].Record.Stage.Network != nil {
			return false
		}
		ownershipDigest, digestErr := gatewayRebindStageNetworkOwnershipDigest(intent)
		if digestErr != nil || value.Stage.Network.OwnershipDigest != ownershipDigest {
			return false
		}
		stageWithoutNetwork := *value.Stage
		stageWithoutNetwork.Network = nil
		if !reflect.DeepEqual(stageWithoutNetwork, *previous[1].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[1].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	default:
		return false
	}
}
