package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

const (
	gatewayRebindEffectBoundaryEvidenceVersion = 1
	gatewayRebindEffectBoundaryEvidencePurpose = "hostd/generated-ingress/rebind/effect-boundary-attestation/v1"
)

var gatewayRebindAcquireDeploymentEffects = deploymenteffects.Acquire

// gatewayRebindEffectBoundaryEvidence is immutable, read-only evidence that
// the initial prepared rebind and all of its external observations matched
// while both effect locks were held. It does not authorize an effect. The
// evidence contains only scalar identities and digests so a caller cannot
// mutate an accepted observation through a returned slice or map. The
// unkeyed evidence becomes stale as soon as the locks are released and must
// never be used as standalone or replayable authorization. A future writer
// must repeat this attestation while holding its own effect locks.
type gatewayRebindEffectBoundaryEvidence struct {
	Version                   int
	Purpose                   string
	Generation                uint64
	OperationID               string
	ClaimRequestDigest        string
	DatabaseDigest            string
	ProtectedIntentDigest     string
	PredecessorStateDigest    string
	PredecessorJournalDigest  string
	PredecessorIdentityDigest string
	SourceDigest              string
	DockerObservationDigest   string
	NetworkObservationDigest  string
	Digest                    string
}

type gatewayRebindEffectBoundaryObservation struct {
	database       appaccess.GatewayRebindStartupSnapshot
	predecessor    gatewayUpgradeGenerationSelection
	intent         gatewayRebindProtectedIntent
	source         routeState
	docker         gatewayV2DockerObservation
	successor      gatewayRebindSuccessorPreflightObservation
	databaseDigest string
	sourceDigest   string
	dockerDigest   string
}

type gatewayRebindEffectBoundaryAnchor struct {
	database       appaccess.GatewayRebindStartupSnapshot
	predecessor    gatewayUpgradeGenerationSelection
	intent         gatewayRebindProtectedIntent
	source         routeState
	databaseDigest string
	sourceDigest   string
}

// attestGatewayRebindPreparedEffectBoundary is the private, read-only
// composition point for a future initial-rebind writer. It acquires the
// deployment-effects lease before the raw gateway lock, repeats the complete
// SQLite/protected/Docker/network observation twice, and then rereads the
// SQLite and protected anchors after the second external observation. It does
// not create an authorization, write SQLite, install protected state, mutate
// Docker, or publish a route.
func (m *Manager) attestGatewayRebindPreparedEffectBoundary(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, checkpoint func(),
) (result gatewayRebindEffectBoundaryEvidence, resultErr error) {
	if m == nil || ctx == nil || repository == nil || inspectDocker == nil ||
		reads.network.candidates == nil || reads.network.host == nil ||
		reads.network.docker == nil || reads.dockerIDs == nil {
		return gatewayRebindEffectBoundaryEvidence{}, &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return gatewayRebindEffectBoundaryEvidence{}, gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRaw(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			return gatewayRebindEffectBoundaryEvidence{}, &Error{Code: DiagnosticRouteUnresolved}
		}
		return gatewayRebindEffectBoundaryEvidence{}, err
	}
	defer func() {
		if err := releaseGateway(); err != nil {
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
		if err := releaseEffects(); err != nil {
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
		if resultErr != nil {
			result = gatewayRebindEffectBoundaryEvidence{}
		}
	}()

	first, err := m.readGatewayRebindEffectBoundaryObservation(ctx, repository, reads, inspectDocker)
	if err != nil {
		return gatewayRebindEffectBoundaryEvidence{}, err
	}
	defer clearGatewayV2DockerObservation(&first.docker)
	if checkpoint != nil {
		checkpoint()
	}
	if ctx.Err() != nil {
		return gatewayRebindEffectBoundaryEvidence{}, gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := m.readGatewayRebindEffectBoundaryObservation(ctx, repository, reads, inspectDocker)
	if err != nil {
		return gatewayRebindEffectBoundaryEvidence{}, err
	}
	defer clearGatewayV2DockerObservation(&second.docker)
	if ctx.Err() != nil || !gatewayRebindEffectBoundaryObservationsEqual(first, second) {
		return gatewayRebindEffectBoundaryEvidence{}, gatewayRebindEffectBoundaryError(ctx)
	}

	final, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || ctx.Err() != nil || !gatewayRebindEffectBoundaryAnchorMatchesObservation(final, first) {
		return gatewayRebindEffectBoundaryEvidence{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return newGatewayRebindEffectBoundaryEvidence(first)
}

func (m *Manager) readGatewayRebindEffectBoundaryObservation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector,
) (gatewayRebindEffectBoundaryObservation, error) {
	anchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil {
		return gatewayRebindEffectBoundaryObservation{}, err
	}
	docker, err := inspectDocker(ctx, anchor.source, anchor.predecessor.State, anchor.predecessor.Journal)
	if err != nil || ctx.Err() != nil ||
		!validGatewayRebindPredecessorDocker(anchor.source, anchor.predecessor.State, anchor.predecessor.Journal, docker) {
		clearGatewayV2DockerObservation(&docker)
		return gatewayRebindEffectBoundaryObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	successor, err := readGatewayRebindSuccessorPreflightObservation(ctx, repository, reads)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(anchor.database, successor.database) {
		clearGatewayV2DockerObservation(&docker)
		return gatewayRebindEffectBoundaryObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expected, err := newGatewayRebindProtectedIntent(anchor.database, anchor.predecessor,
		successor, anchor.intent.Generation)
	if err != nil || !reflect.DeepEqual(expected, anchor.intent) {
		clearGatewayV2DockerObservation(&docker)
		return gatewayRebindEffectBoundaryObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerDigest, err := canonicalDigest(docker)
	if err != nil {
		clearGatewayV2DockerObservation(&docker)
		return gatewayRebindEffectBoundaryObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindEffectBoundaryObservation{
		database: anchor.database, predecessor: anchor.predecessor, intent: anchor.intent,
		source: anchor.source, docker: docker, successor: successor,
		databaseDigest: anchor.databaseDigest, sourceDigest: anchor.sourceDigest, dockerDigest: dockerDigest,
	}, nil
}

func (m *Manager) readGatewayRebindEffectBoundaryAnchor(ctx context.Context,
	repository *appaccess.Repository,
) (gatewayRebindEffectBoundaryAnchor, error) {
	if ctx.Err() != nil {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	database, err := repository.GatewayRebindStartupSnapshot(ctx)
	if err != nil || len(database.Claims) != 1 {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	selection := history.Intents[0]
	intent := selection.Intent
	if selection.Store == nil || !selection.Existing || selection.Generation != intent.Generation ||
		selection.Store.generation != intent.Generation || selection.Store.operationID != intent.OperationID ||
		intent.OperationID != database.Claims[0].Claim.Spec.OperationID ||
		!gatewayRebindPredecessorMatches(database, history.Predecessor.State, history.Predecessor.Journal) {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	databaseDigest, err := canonicalDigest(database)
	if err != nil || databaseDigest != intent.DatabaseDigest {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	source, err := m.store.load()
	if err != nil || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil {
		return gatewayRebindEffectBoundaryAnchor{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindEffectBoundaryAnchor{
		database: database, predecessor: history.Predecessor, intent: intent, source: source,
		databaseDigest: databaseDigest, sourceDigest: sourceDigest,
	}, nil
}

func gatewayRebindEffectBoundaryAnchorMatchesObservation(anchor gatewayRebindEffectBoundaryAnchor,
	observation gatewayRebindEffectBoundaryObservation,
) bool {
	return reflect.DeepEqual(anchor.database, observation.database) &&
		gatewayRebindGenerationSelectionEqual(anchor.predecessor, observation.predecessor) &&
		reflect.DeepEqual(anchor.intent, observation.intent) &&
		reflect.DeepEqual(anchor.source, observation.source) &&
		anchor.databaseDigest == observation.databaseDigest && anchor.sourceDigest == observation.sourceDigest
}

func gatewayRebindEffectBoundaryObservationsEqual(left, right gatewayRebindEffectBoundaryObservation) bool {
	return reflect.DeepEqual(left.database, right.database) &&
		gatewayRebindGenerationSelectionEqual(left.predecessor, right.predecessor) &&
		reflect.DeepEqual(left.intent, right.intent) && reflect.DeepEqual(left.source, right.source) &&
		reflect.DeepEqual(left.docker, right.docker) && reflect.DeepEqual(left.successor, right.successor) &&
		left.databaseDigest == right.databaseDigest && left.sourceDigest == right.sourceDigest &&
		left.dockerDigest == right.dockerDigest
}

func gatewayRebindGenerationSelectionEqual(left, right gatewayUpgradeGenerationSelection) bool {
	return left.Generation == right.Generation && left.Existing == right.Existing &&
		left.PartialState == right.PartialState && left.Retired == right.Retired && left.Aborted == right.Aborted &&
		left.operationID == right.operationID && reflect.DeepEqual(left.State, right.State) &&
		reflect.DeepEqual(left.Journal, right.Journal) && left.Store != nil && right.Store != nil &&
		left.Store.directory != nil && right.Store.directory != nil &&
		left.Store.directory.root == right.Store.directory.root &&
		sameGatewayV2PreparationAbortStore(left.Store, right.Store)
}

func newGatewayRebindEffectBoundaryEvidence(observation gatewayRebindEffectBoundaryObservation) (gatewayRebindEffectBoundaryEvidence, error) {
	value := gatewayRebindEffectBoundaryEvidence{
		Version: gatewayRebindEffectBoundaryEvidenceVersion, Purpose: gatewayRebindEffectBoundaryEvidencePurpose,
		Generation: observation.intent.Generation, OperationID: observation.intent.OperationID,
		ClaimRequestDigest: observation.intent.Intent.Claim.RequestDigest,
		DatabaseDigest:     observation.databaseDigest, ProtectedIntentDigest: observation.intent.Digest,
		PredecessorStateDigest:    observation.intent.Intent.Predecessor.StateDigest,
		PredecessorJournalDigest:  observation.intent.Intent.Predecessor.JournalDigest,
		PredecessorIdentityDigest: observation.intent.Intent.Predecessor.IdentityDigest,
		SourceDigest:              observation.sourceDigest, DockerObservationDigest: observation.dockerDigest,
		NetworkObservationDigest: observation.intent.NetworkObservationDigest,
	}
	var err error
	value.Digest, err = gatewayRebindEffectBoundaryEvidenceDigest(value)
	if err != nil || !validGatewayRebindEffectBoundaryEvidence(value) {
		return gatewayRebindEffectBoundaryEvidence{}, errors.New("invalid generated ingress rebind effect-boundary evidence")
	}
	return value, nil
}

func gatewayRebindEffectBoundaryEvidenceDigest(value gatewayRebindEffectBoundaryEvidence) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindEffectBoundaryEvidence(value gatewayRebindEffectBoundaryEvidence) bool {
	if value.Version != gatewayRebindEffectBoundaryEvidenceVersion ||
		value.Purpose != gatewayRebindEffectBoundaryEvidencePurpose || value.Generation == 0 ||
		!validCanonicalUUID(value.OperationID) || !validSHA256(value.ClaimRequestDigest) ||
		!validSHA256(value.DatabaseDigest) || !validSHA256(value.ProtectedIntentDigest) ||
		!validSHA256(value.PredecessorStateDigest) || !validSHA256(value.PredecessorJournalDigest) ||
		!validSHA256(value.PredecessorIdentityDigest) || !validSHA256(value.SourceDigest) ||
		!validSHA256(value.DockerObservationDigest) || !validSHA256(value.NetworkObservationDigest) ||
		!validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindEffectBoundaryEvidenceDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayRebindEffectBoundaryError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}
