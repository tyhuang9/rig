package generatedingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const maximumGeneratedRouteActivationDocumentBytes = 256 << 10

// generatedRouteActivationInput is deliberately package private. Only a
// sealed GeneratedRouteExecution can derive its canonical physical proof and
// invoke the fixed writer below.
type generatedRouteActivationInput struct {
	receiptID             string
	appID                 string
	successorDeploymentID string
	successorReleaseID    string
	successorSlot         string
	expectedHead          generatedruntimestate.ActiveHead
	candidateRoster       []generatedruntimestate.RouteActivationComponent
	predecessorRoster     []generatedruntimestate.RouteActivationComponent
	routeIntent           []byte
	committedRouteProof   []byte
}

type generatedRouteActivationResult struct {
	head    generatedruntimestate.ActiveHead
	receipt generatedruntimestate.RouteActivationReceipt
	changed bool
}

// activateRoute is the sole activation writer. The original Claim, exact SQL
// rosters, retained receipt and active head change are checked and committed
// in this one fixed transaction. It exposes no transaction, callback, proof
// setter or generic success capability.
func (a *GeneratedRouteAuthority) activateRoute(ctx context.Context, pinned *deploymentexecution.Pinned,
	claim deploymentexecution.Claim, input generatedRouteActivationInput,
) (generatedRouteActivationResult, error) {
	if !a.configured() || ctx == nil || pinned == nil || !validGeneratedRouteActivationInput(input, claim) {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation input: %w", generatedruntimestate.ErrInvalidState)
	}
	candidate, ok := canonicalGeneratedRouteActivationRoster(input.candidateRoster)
	if !ok || len(candidate) == 0 {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation candidate roster: %w", generatedruntimestate.ErrInvalidState)
	}
	predecessor, ok := canonicalGeneratedRouteActivationRoster(input.predecessorRoster)
	if !ok || (input.expectedHead.DeploymentID == "") != (len(predecessor) == 0) {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation predecessor roster: %w", generatedruntimestate.ErrInvalidState)
	}
	intent, ok := canonicalGeneratedRouteActivationDocument(input.routeIntent)
	if !ok {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation intent: %w", generatedruntimestate.ErrInvalidState)
	}
	proof, ok := canonicalGeneratedRouteActivationDocument(input.committedRouteProof)
	if !ok {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation proof: %w", generatedruntimestate.ErrInvalidState)
	}
	tx, err := a.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return generatedRouteActivationResult{}, err
	}
	defer tx.Rollback()
	if err := pinned.CheckRouteActivationTx(tx, claim, input.successorDeploymentID); err != nil {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation claim: %w", err)
	}
	creatorJobID, creatorWorkerAttempt, err := readGeneratedRouteActivationCreatorTx(ctx, tx,
		input.successorDeploymentID, candidate, claim)
	if err != nil {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation creator: %w", err)
	}
	receipt := generatedruntimestate.RouteActivationReceipt{
		ReceiptID: input.receiptID, AppID: input.appID,
		SuccessorDeploymentID: input.successorDeploymentID, SuccessorReleaseID: input.successorReleaseID,
		SuccessorSlot: input.successorSlot, SuccessorGeneration: input.expectedHead.Generation + 1,
		PredecessorDeploymentID: input.expectedHead.DeploymentID,
		PredecessorReleaseID:    input.expectedHead.ReleaseID, PredecessorSlot: input.expectedHead.Slot,
		PredecessorGeneration:          input.expectedHead.Generation,
		PredecessorActivationReceiptID: input.expectedHead.ActivationReceiptID,
		CreatorJobID:                   creatorJobID, CreatorWorkerAttempt: creatorWorkerAttempt,
		WriterJobID: claim.JobID(), WriterWorkerAttempt: claim.Attempt(),
		CandidateRosterDigest:     generatedRouteActivationRosterDigest(candidate),
		PredecessorRosterDigest:   generatedRouteActivationRosterDigest(predecessor),
		RouteIntentDigest:         generatedRouteActivationDocumentDigest(intent),
		CommittedRouteProofDigest: generatedRouteActivationDocumentDigest(proof),
	}
	current, err := readGeneratedRouteActivationHeadTx(ctx, tx, input.appID)
	if err != nil {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation head: %w", err)
	}
	if current.DeploymentID == input.successorDeploymentID {
		return a.resolveRouteActivationReplay(ctx, tx, pinned, claim, input, candidate, predecessor, receipt, current)
	}
	if !sameActivationHeadForRouteAuthority(current, input.expectedHead) {
		return generatedRouteActivationResult{}, generatedruntimestate.ErrConflict
	}
	if err := validateGeneratedRouteActivationDeploymentTx(ctx, tx, input); err != nil {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation deployment: %w", err)
	}
	observedCandidate, err := readGeneratedRouteActivationRosterTx(ctx, tx,
		input.successorDeploymentID, generatedruntimestate.ComponentHealthy)
	if err != nil || !equalGeneratedRouteActivationRoster(observedCandidate, candidate) {
		return generatedRouteActivationResult{}, fmt.Errorf("generated route activation candidate observation: %w",
			errors.Join(generatedruntimestate.ErrConflict, err))
	}
	if input.expectedHead.DeploymentID != "" {
		observed, readErr := readGeneratedRouteActivationRosterTx(ctx, tx,
			input.expectedHead.DeploymentID, generatedruntimestate.ComponentActive)
		if readErr != nil || !equalGeneratedRouteActivationRoster(observed, predecessor) {
			return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, readErr)
		}
	}
	receipt.ActivatedAt = time.Now().UTC()
	stamp := receipt.ActivatedAt.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO generated_route_activations(
		id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
		predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
		predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,writer_job_id,
		writer_worker_attempt,candidate_roster_digest,predecessor_roster_digest,route_intent_digest,
		committed_route_proof_digest,activated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.ReceiptID, receipt.AppID, receipt.SuccessorDeploymentID, receipt.SuccessorReleaseID,
		receipt.SuccessorSlot, receipt.SuccessorGeneration, receipt.PredecessorDeploymentID,
		receipt.PredecessorReleaseID, receipt.PredecessorSlot, receipt.PredecessorGeneration,
		receipt.PredecessorActivationReceiptID, receipt.CreatorJobID, receipt.CreatorWorkerAttempt,
		receipt.WriterJobID, receipt.WriterWorkerAttempt,
		receipt.CandidateRosterDigest, receipt.PredecessorRosterDigest, receipt.RouteIntentDigest,
		receipt.CommittedRouteProofDigest, stamp); err != nil {
		return generatedRouteActivationResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot=?,generation=generation+1,activation_receipt_id=?,updated_at=?
		WHERE app_id=? AND generation=? AND COALESCE(deployment_id,'')=? AND COALESCE(release_id,'')=?
		  AND COALESCE(slot,'')=? AND COALESCE(activation_receipt_id,'')=?`,
		input.successorDeploymentID, input.successorReleaseID, input.successorSlot, input.receiptID, stamp,
		input.appID, input.expectedHead.Generation, input.expectedHead.DeploymentID, input.expectedHead.ReleaseID,
		input.expectedHead.Slot, input.expectedHead.ActivationReceiptID)
	if err != nil || exactGeneratedRouteRowsAffected(result, 1) != nil {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	result, err = tx.ExecContext(ctx, `UPDATE generated_runtime_components SET state='active',updated_at=?
		WHERE deployment_id=? AND state='healthy'`, stamp, input.successorDeploymentID)
	if err != nil || exactGeneratedRouteRowsAffected(result, int64(len(candidate))) != nil {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	if input.expectedHead.DeploymentID != "" {
		result, err = tx.ExecContext(ctx, `UPDATE generated_runtime_components SET state='draining',updated_at=?
			WHERE deployment_id=? AND state='active'`, stamp, input.expectedHead.DeploymentID)
		if err != nil || exactGeneratedRouteRowsAffected(result, int64(len(predecessor))) != nil {
			return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
		}
	}
	result, err = tx.ExecContext(ctx, `UPDATE generated_runtime_deployments SET phase='draining',updated_at=?
		WHERE deployment_id=? AND app_id=? AND phase='switching_route'`, stamp,
		input.successorDeploymentID, input.appID)
	if err != nil || exactGeneratedRouteRowsAffected(result, 1) != nil {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	if err := pinned.CheckRouteActivationTx(tx, claim, input.successorDeploymentID); err != nil {
		return generatedRouteActivationResult{}, err
	}
	head, err := readGeneratedRouteActivationHeadTx(ctx, tx, input.appID)
	if err != nil || head.ActivationReceiptID != input.receiptID || head.Generation != receipt.SuccessorGeneration {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	if err := tx.Commit(); err != nil {
		return generatedRouteActivationResult{}, err
	}
	return generatedRouteActivationResult{head: head, receipt: receipt, changed: true}, nil
}

// readGeneratedRouteActivationCreatorTx derives the uniform immutable 041
// creator from the exact candidate roster. It is read-only: the current Claim
// remains the 042 writer and is checked at both transaction boundaries.
func readGeneratedRouteActivationCreatorTx(ctx context.Context, tx *sql.Tx, deploymentID string,
	candidate []generatedruntimestate.RouteActivationComponent, claim deploymentexecution.Claim,
) (string, int, error) {
	if ctx == nil || tx == nil || !validAppID(deploymentID) || len(candidate) == 0 ||
		claim.JobID() == "" || claim.Attempt() < 1 {
		return "", 0, generatedruntimestate.ErrInvalidState
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.component_name,i.id,i.creator_job_id,i.creator_worker_attempt
		FROM generated_runtime_components c
		JOIN generated_candidate_intents i
		  ON i.deployment_id=c.deployment_id AND i.component_name=c.component_name
		WHERE c.deployment_id=? ORDER BY c.component_name`, deploymentID)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	creatorJobID, creatorWorkerAttempt := "", 0
	index := 0
	for rows.Next() {
		var componentName, receiptID, observedJobID string
		var observedAttempt int
		if err := rows.Scan(&componentName, &receiptID, &observedJobID, &observedAttempt); err != nil {
			return "", 0, err
		}
		if index >= len(candidate) || candidate[index].DeploymentID != deploymentID ||
			candidate[index].ComponentName != componentName ||
			candidate[index].CandidateReceiptID != receiptID || observedJobID != claim.JobID() ||
			observedAttempt < 1 || observedAttempt > claim.Attempt() {
			return "", 0, generatedruntimestate.ErrConflict
		}
		if index == 0 {
			creatorJobID, creatorWorkerAttempt = observedJobID, observedAttempt
		} else if creatorJobID != observedJobID || creatorWorkerAttempt != observedAttempt {
			return "", 0, generatedruntimestate.ErrConflict
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	if index != len(candidate) || creatorJobID == "" || creatorWorkerAttempt < 1 {
		return "", 0, generatedruntimestate.ErrConflict
	}
	return creatorJobID, creatorWorkerAttempt, nil
}

func (a *GeneratedRouteAuthority) resolveRouteActivationReplay(ctx context.Context, tx *sql.Tx,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim, input generatedRouteActivationInput,
	candidate, predecessor []generatedruntimestate.RouteActivationComponent,
	expected generatedruntimestate.RouteActivationReceipt, current generatedruntimestate.ActiveHead,
) (generatedRouteActivationResult, error) {
	if current.ActivationReceiptID == "" || current.ActivationReceiptID != input.receiptID ||
		current.Generation != expected.SuccessorGeneration || current.ReleaseID != input.successorReleaseID ||
		current.Slot != input.successorSlot {
		return generatedRouteActivationResult{}, generatedruntimestate.ErrConflict
	}
	receipt, err := a.runtime.ReadRouteActivationReceiptTx(ctx, tx, input.receiptID)
	if err != nil || !sameGeneratedRouteActivationReceiptExceptTime(receipt, expected) {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	observedCandidate, err := readGeneratedRouteActivationRosterTx(ctx, tx,
		input.successorDeploymentID, generatedruntimestate.ComponentActive)
	if err != nil || !equalGeneratedRouteActivationRoster(observedCandidate, candidate) {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	if input.expectedHead.DeploymentID != "" {
		observed, readErr := readGeneratedRouteActivationRosterTx(ctx, tx,
			input.expectedHead.DeploymentID, generatedruntimestate.ComponentDraining)
		if readErr != nil || !equalGeneratedRouteActivationRoster(observed, predecessor) {
			return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, readErr)
		}
	}
	var phase generatedruntimestate.Phase
	if err := tx.QueryRowContext(ctx, `SELECT phase FROM generated_runtime_deployments
		WHERE deployment_id=? AND app_id=?`, input.successorDeploymentID, input.appID).Scan(&phase); err != nil || phase != generatedruntimestate.PhaseDraining {
		return generatedRouteActivationResult{}, errors.Join(generatedruntimestate.ErrConflict, err)
	}
	if err := pinned.CheckRouteActivationTx(tx, claim, input.successorDeploymentID); err != nil {
		return generatedRouteActivationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedRouteActivationResult{}, err
	}
	return generatedRouteActivationResult{head: current, receipt: receipt, changed: false}, nil
}

func validateGeneratedRouteActivationDeploymentTx(ctx context.Context, tx *sql.Tx,
	input generatedRouteActivationInput,
) error {
	var releaseID, slot, previousDeploymentID, previousSlot string
	var phase generatedruntimestate.Phase
	err := tx.QueryRowContext(ctx, `SELECT release_id,candidate_slot,COALESCE(previous_active_deployment_id,''),
		COALESCE(previous_active_slot,''),phase FROM generated_runtime_deployments
		WHERE deployment_id=? AND app_id=?`, input.successorDeploymentID, input.appID).
		Scan(&releaseID, &slot, &previousDeploymentID, &previousSlot, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return generatedruntimestate.ErrNotFound
	}
	if err != nil {
		return err
	}
	if releaseID != input.successorReleaseID || slot != input.successorSlot ||
		previousDeploymentID != input.expectedHead.DeploymentID || previousSlot != input.expectedHead.Slot ||
		phase != generatedruntimestate.PhaseSwitchingRoute {
		return generatedruntimestate.ErrConflict
	}
	return nil
}

func readGeneratedRouteActivationHeadTx(ctx context.Context, tx *sql.Tx, appID string) (generatedruntimestate.ActiveHead, error) {
	var value generatedruntimestate.ActiveHead
	var updatedAt sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT app_id,COALESCE(deployment_id,''),COALESCE(release_id,''),
		COALESCE(slot,''),generation,COALESCE(activation_receipt_id,''),updated_at
		FROM generated_runtime_active_heads WHERE app_id=?`, appID).Scan(
		&value.AppID, &value.DeploymentID, &value.ReleaseID, &value.Slot,
		&value.Generation, &value.ActivationReceiptID, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return value, generatedruntimestate.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if value.AppID != appID || value.Generation > 0 && !updatedAt.Valid {
		return generatedruntimestate.ActiveHead{}, generatedruntimestate.ErrInvalidState
	}
	return value, nil
}

func readGeneratedRouteActivationRosterTx(ctx context.Context, tx *sql.Tx, deploymentID string,
	want generatedruntimestate.ComponentState,
) ([]generatedruntimestate.RouteActivationComponent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.deployment_id,c.component_name,c.slot,c.image_artifact_id,
		c.container_name,c.container_id,c.state,COALESCE(i.id,''),COALESCE(i.candidate_policy_digest,'')
		FROM generated_runtime_components c
		LEFT JOIN generated_candidate_intents i
		  ON i.deployment_id=c.deployment_id AND i.component_name=c.component_name
		WHERE c.deployment_id=? ORDER BY c.component_name`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []generatedruntimestate.RouteActivationComponent{}
	for rows.Next() {
		var value generatedruntimestate.RouteActivationComponent
		var state generatedruntimestate.ComponentState
		if err := rows.Scan(&value.DeploymentID, &value.ComponentName, &value.Slot,
			&value.ImageArtifactID, &value.ContainerName, &value.ContainerID, &state,
			&value.CandidateReceiptID, &value.CandidatePolicyDigest); err != nil {
			return nil, err
		}
		if state != want || !validGeneratedRouteActivationComponent(value) {
			return nil, generatedruntimestate.ErrInvalidState
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func canonicalGeneratedRouteActivationRoster(values []generatedruntimestate.RouteActivationComponent) ([]generatedruntimestate.RouteActivationComponent, bool) {
	result := append([]generatedruntimestate.RouteActivationComponent(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].ComponentName < result[j].ComponentName })
	for index, value := range result {
		if !validGeneratedRouteActivationComponent(value) || index > 0 && result[index-1].ComponentName == value.ComponentName {
			return nil, false
		}
	}
	return result, true
}

func validGeneratedRouteActivationComponent(value generatedruntimestate.RouteActivationComponent) bool {
	return validAppID(value.DeploymentID) && validGeneratedRouteText(value.ComponentName, 256) &&
		(value.Slot == "blue" || value.Slot == "green") && validAppID(value.ImageArtifactID) &&
		validGeneratedRouteContainerName(value.ContainerName) && lowerHex(value.ContainerID, 64) &&
		((value.CandidateReceiptID == "" && value.CandidatePolicyDigest == "") ||
			(validAppID(value.CandidateReceiptID) && lowerHex(value.CandidatePolicyDigest, 64)))
}

func validGeneratedRouteText(value string, maximum int) bool {
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

func validGeneratedRouteContainerName(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || index > 0 && (character == '_' || character == '.' || character == '-') {
			continue
		}
		return false
	}
	return true
}

func equalGeneratedRouteActivationRoster(left, right []generatedruntimestate.RouteActivationComponent) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func generatedRouteActivationRosterDigest(values []generatedruntimestate.RouteActivationComponent) string {
	body, _ := json.Marshal(values)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func canonicalGeneratedRouteActivationDocument(value []byte) ([]byte, bool) {
	if len(value) == 0 || len(value) > maximumGeneratedRouteActivationDocumentBytes || !json.Valid(value) {
		return nil, false
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return nil, false
	}
	canonical, err := json.Marshal(decoded)
	return canonical, err == nil && bytes.Equal(value, canonical)
}

func generatedRouteActivationDocumentDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validGeneratedRouteActivationInput(input generatedRouteActivationInput, claim deploymentexecution.Claim) bool {
	return validAppID(input.receiptID) && validAppID(input.appID) &&
		validAppID(input.successorDeploymentID) && validReleaseID(input.successorReleaseID) &&
		(input.successorSlot == "blue" || input.successorSlot == "green") &&
		claim.JobID() != "" && claim.ApplicationID() == input.appID && claim.Attempt() > 0 &&
		input.expectedHead.AppID == input.appID && input.expectedHead.Generation >= 0 &&
		((input.expectedHead.Generation == 0 && input.expectedHead.DeploymentID == "" &&
			input.expectedHead.ReleaseID == "" && input.expectedHead.Slot == "" && input.expectedHead.ActivationReceiptID == "") ||
			(input.expectedHead.Generation > 0 && validAppID(input.expectedHead.DeploymentID) &&
				validReleaseID(input.expectedHead.ReleaseID) &&
				(input.expectedHead.Slot == "blue" || input.expectedHead.Slot == "green") &&
				(input.expectedHead.ActivationReceiptID == "" || validAppID(input.expectedHead.ActivationReceiptID))))
}

func exactGeneratedRouteRowsAffected(result sql.Result, want int64) error {
	if result == nil {
		return generatedruntimestate.ErrConflict
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != want {
		return generatedruntimestate.ErrConflict
	}
	return nil
}

func sameGeneratedRouteActivationReceiptExceptTime(left, right generatedruntimestate.RouteActivationReceipt) bool {
	left.ActivatedAt = time.Time{}
	right.ActivatedAt = time.Time{}
	return left == right
}
