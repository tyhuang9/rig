package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RouteActivationComponent is immutable roster identity retained by a sealed
// route execution. State is deliberately absent from its digest: the writer
// validates Healthy/Active and Active/Draining at the transaction boundary.
type RouteActivationComponent struct {
	DeploymentID          string `json:"deploymentId"`
	ComponentName         string `json:"componentName"`
	Slot                  string `json:"slot"`
	ImageArtifactID       string `json:"imageArtifactId"`
	ContainerName         string `json:"containerName"`
	ContainerID           string `json:"containerId"`
	CandidateReceiptID    string `json:"candidateReceiptId,omitempty"`
	CandidatePolicyDigest string `json:"candidatePolicyDigest,omitempty"`
}

type RouteActivationReceipt struct {
	ReceiptID                      string
	AppID                          string
	SuccessorDeploymentID          string
	SuccessorReleaseID             string
	SuccessorSlot                  string
	SuccessorGeneration            int64
	PredecessorDeploymentID        string
	PredecessorReleaseID           string
	PredecessorSlot                string
	PredecessorGeneration          int64
	PredecessorActivationReceiptID string
	CreatorJobID                   string
	CreatorWorkerAttempt           int
	WriterJobID                    string
	WriterWorkerAttempt            int
	CandidateRosterDigest          string
	PredecessorRosterDigest        string
	RouteIntentDigest              string
	CommittedRouteProofDigest      string
	ActivatedAt                    time.Time
}

// GeneratedDomainStartupRouteActivation is the read-only retained activation
// evidence included in the generated-domain startup census.
type GeneratedDomainStartupRouteActivation struct {
	ReceiptID                      string
	AppID                          string
	SuccessorDeploymentID          string
	SuccessorReleaseID             string
	SuccessorSlot                  string
	SuccessorGeneration            int64
	PredecessorDeploymentID        string
	PredecessorReleaseID           string
	PredecessorSlot                string
	PredecessorGeneration          int64
	PredecessorActivationReceiptID string
	CreatorJobID                   string
	CreatorWorkerAttempt           int
	WriterJobID                    string
	WriterWorkerAttempt            int
	CandidateRosterDigest          string
	PredecessorRosterDigest        string
	RouteIntentDigest              string
	CommittedRouteProofDigest      string
}

// RouteActivationRoster is a read-only exact roster observation. It grants no
// candidate, route, receipt, or state transition authority.
func (r *Repository) RouteActivationRoster(ctx context.Context, appID, deploymentID string,
	want ComponentState,
) ([]RouteActivationComponent, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) ||
		(want != ComponentHealthy && want != ComponentActive && want != ComponentDraining) {
		return nil, ErrInvalidState
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generated_runtime_deployments
		WHERE deployment_id=? AND app_id=?)`, deploymentID, appID).Scan(&exists); err != nil || exists != 1 {
		return nil, errors.Join(ErrNotFound, err)
	}
	values, err := readActivationRosterTx(ctx, tx, deploymentID, want)
	if err != nil || len(values) == 0 {
		return nil, errors.Join(ErrInvalidState, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func readActivationRosterTx(ctx context.Context, tx *sql.Tx, deploymentID string,
	want ComponentState,
) ([]RouteActivationComponent, error) {
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
	result := []RouteActivationComponent{}
	for rows.Next() {
		var value RouteActivationComponent
		var state ComponentState
		if err := rows.Scan(&value.DeploymentID, &value.ComponentName, &value.Slot,
			&value.ImageArtifactID, &value.ContainerName, &value.ContainerID, &state,
			&value.CandidateReceiptID, &value.CandidatePolicyDigest); err != nil {
			return nil, err
		}
		if state != want || !validRouteActivationComponent(value) {
			return nil, ErrInvalidState
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func validRouteActivationComponent(value RouteActivationComponent) bool {
	return canonicalUUID(value.DeploymentID) && validText(value.ComponentName, 256) &&
		(value.Slot == "blue" || value.Slot == "green") && canonicalUUID(value.ImageArtifactID) &&
		validContainerName(value.ContainerName) && lowerHex(value.ContainerID, 64) &&
		((value.CandidateReceiptID == "" && value.CandidatePolicyDigest == "") ||
			(canonicalUUID(value.CandidateReceiptID) && lowerHex(value.CandidatePolicyDigest, 64)))
}

// ReadRouteActivationReceiptTx reads one immutable activation receipt in the
// caller-owned transaction. It grants no writer, route, or execution
// authority; callers must separately validate their current scoped claim.
func (r *Repository) ReadRouteActivationReceiptTx(ctx context.Context, tx *sql.Tx,
	receiptID string,
) (RouteActivationReceipt, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !canonicalUUID(receiptID) {
		return RouteActivationReceipt{}, ErrInvalidState
	}
	return readRouteActivationReceiptTx(ctx, tx, receiptID)
}

type routeActivationRowScanner interface {
	Scan(dest ...any) error
}

func readRouteActivationReceiptTx(ctx context.Context, tx *sql.Tx, receiptID string) (RouteActivationReceipt, error) {
	if ctx == nil || tx == nil || !canonicalUUID(receiptID) {
		return RouteActivationReceipt{}, ErrInvalidState
	}
	return scanRouteActivationReceipt(tx.QueryRowContext(ctx, routeActivationSelect+` WHERE id=?`, receiptID))
}

func scanRouteActivationReceipt(row routeActivationRowScanner) (RouteActivationReceipt, error) {
	var value RouteActivationReceipt
	var activatedAt string
	var writerJobID sql.NullString
	var writerWorkerAttempt sql.NullInt64
	err := row.Scan(
		&value.ReceiptID, &value.AppID, &value.SuccessorDeploymentID, &value.SuccessorReleaseID,
		&value.SuccessorSlot, &value.SuccessorGeneration, &value.PredecessorDeploymentID,
		&value.PredecessorReleaseID, &value.PredecessorSlot, &value.PredecessorGeneration,
		&value.PredecessorActivationReceiptID, &value.CreatorJobID, &value.CreatorWorkerAttempt,
		&writerJobID, &writerWorkerAttempt,
		&value.CandidateRosterDigest, &value.PredecessorRosterDigest, &value.RouteIntentDigest,
		&value.CommittedRouteProofDigest, &activatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteActivationReceipt{}, ErrNotFound
	}
	if err != nil {
		return RouteActivationReceipt{}, err
	}
	if writerJobID.Valid != writerWorkerAttempt.Valid {
		return RouteActivationReceipt{}, ErrInvalidState
	}
	if writerJobID.Valid {
		if writerWorkerAttempt.Int64 < 1 || writerWorkerAttempt.Int64 > int64(^uint(0)>>1) {
			return RouteActivationReceipt{}, ErrInvalidState
		}
		value.WriterJobID, value.WriterWorkerAttempt = writerJobID.String, int(writerWorkerAttempt.Int64)
	} else {
		value.WriterJobID, value.WriterWorkerAttempt = value.CreatorJobID, value.CreatorWorkerAttempt
	}
	value.ActivatedAt, err = parseTime(activatedAt)
	if err != nil || !validRouteActivationReceipt(value) {
		return RouteActivationReceipt{}, ErrInvalidState
	}
	return value, nil
}

func validRouteActivationReceipt(value RouteActivationReceipt) bool {
	return canonicalUUID(value.ReceiptID) && canonicalUUID(value.AppID) &&
		canonicalUUID(value.SuccessorDeploymentID) && validReleaseID(value.SuccessorReleaseID) &&
		(value.SuccessorSlot == "blue" || value.SuccessorSlot == "green") && value.SuccessorGeneration > 0 &&
		value.PredecessorGeneration == value.SuccessorGeneration-1 &&
		((value.PredecessorGeneration == 0 && value.PredecessorDeploymentID == "" && value.PredecessorReleaseID == "" &&
			value.PredecessorSlot == "" && value.PredecessorActivationReceiptID == "") ||
			(value.PredecessorGeneration > 0 && canonicalUUID(value.PredecessorDeploymentID) &&
				validReleaseID(value.PredecessorReleaseID) && (value.PredecessorSlot == "blue" || value.PredecessorSlot == "green") &&
				(value.PredecessorActivationReceiptID == "" || canonicalUUID(value.PredecessorActivationReceiptID)))) &&
		canonicalUUID(value.CreatorJobID) && value.CreatorWorkerAttempt > 0 &&
		canonicalUUID(value.WriterJobID) && value.WriterJobID == value.CreatorJobID &&
		value.WriterWorkerAttempt >= value.CreatorWorkerAttempt &&
		lowerHex(value.CandidateRosterDigest, 64) && lowerHex(value.PredecessorRosterDigest, 64) &&
		lowerHex(value.RouteIntentDigest, 64) && lowerHex(value.CommittedRouteProofDigest, 64) &&
		!value.ActivatedAt.IsZero()
}

const routeActivationSelect = `SELECT id,app_id,successor_deployment_id,successor_release_id,successor_slot,
	successor_generation,predecessor_deployment_id,predecessor_release_id,predecessor_slot,
	predecessor_generation,predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,
	writer_job_id,writer_worker_attempt,
	candidate_roster_digest,predecessor_roster_digest,route_intent_digest,committed_route_proof_digest,
	activated_at FROM generated_route_activations`

func readGeneratedDomainStartupRouteActivations(ctx context.Context, tx *sql.Tx) ([]GeneratedDomainStartupRouteActivation, error) {
	rows, err := tx.QueryContext(ctx, routeActivationSelect+` ORDER BY app_id,successor_generation,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []GeneratedDomainStartupRouteActivation{}
	for rows.Next() {
		receipt, err := scanRouteActivationReceipt(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, GeneratedDomainStartupRouteActivation{
			ReceiptID: receipt.ReceiptID, AppID: receipt.AppID,
			SuccessorDeploymentID: receipt.SuccessorDeploymentID, SuccessorReleaseID: receipt.SuccessorReleaseID,
			SuccessorSlot: receipt.SuccessorSlot, SuccessorGeneration: receipt.SuccessorGeneration,
			PredecessorDeploymentID: receipt.PredecessorDeploymentID, PredecessorReleaseID: receipt.PredecessorReleaseID,
			PredecessorSlot: receipt.PredecessorSlot, PredecessorGeneration: receipt.PredecessorGeneration,
			PredecessorActivationReceiptID: receipt.PredecessorActivationReceiptID,
			CreatorJobID:                   receipt.CreatorJobID, CreatorWorkerAttempt: receipt.CreatorWorkerAttempt,
			WriterJobID: receipt.WriterJobID, WriterWorkerAttempt: receipt.WriterWorkerAttempt,
			CandidateRosterDigest: receipt.CandidateRosterDigest, PredecessorRosterDigest: receipt.PredecessorRosterDigest,
			RouteIntentDigest: receipt.RouteIntentDigest, CommittedRouteProofDigest: receipt.CommittedRouteProofDigest,
		})
	}
	return result, rows.Err()
}
