package controller

import (
	"context"
	"net/http"

	"github.com/hostd/hostd/internal/apicontract"
)

const operationGetLANRecoveryHead = "getLANRecoveryHead"

// LANRecoveryHead is the read-only operator projection of the exact recovery
// operation pinned before the recovery-only listener started. It deliberately
// contains no address or URL.
type LANRecoveryHead struct {
	Kind                 string
	AppID                string
	OperationID          string
	Batch                bool
	BatchPosition        int
	BatchCount           int
	ClaimState           string
	AccessRevisionID     string
	AccessRevisionNumber int64
	AllocationID         string
	OwnerOperationID     string
	Port                 uint16
	ApprovalDigest       string
}

// LANRecoveryHeadService repeats the complete cross-store recovery proof. A
// successful read is safe to display to any current administrator; it does not
// depend on the identity of the administrator who approved the immutable
// claim.
type LANRecoveryHeadService interface {
	ReadLANRecoveryHead(context.Context) (LANRecoveryHead, error)
}

func (s *Server) getLANRecoveryHead(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetLANRecoveryHead, "lan_access_forbidden", "Administrator access is required to inspect LAN recovery") {
		return
	}
	if r.URL.RawQuery != "" || !s.RecoveryOnly ||
		(s.RecoveryKind != RecoveryLANGrant && s.RecoveryKind != RecoveryLANDisable) ||
		s.LANRecoveryHeads == nil {
		s.lanRecoveryUnavailable(w, r)
		return
	}
	head, err := s.LANRecoveryHeads.ReadLANRecoveryHead(r.Context())
	if err != nil || !s.validPinnedLANRecoveryHead(head) {
		s.lanRecoveryUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, apicontract.LANRecoveryHead{
		Kind: head.Kind, AppID: head.AppID, OperationID: head.OperationID,
		Batch: head.Batch, BatchPosition: head.BatchPosition, BatchCount: head.BatchCount,
		ClaimState: head.ClaimState, AccessRevisionID: head.AccessRevisionID,
		AccessRevisionNumber: head.AccessRevisionNumber, AllocationID: head.AllocationID,
		OwnerOperationID: head.OwnerOperationID, Port: int(head.Port), ApprovalDigest: head.ApprovalDigest,
	})
}

func (s *Server) validPinnedLANRecoveryHead(head LANRecoveryHead) bool {
	if head.Kind != s.RecoveryKind || head.AppID != s.RecoveryAppID ||
		head.OperationID != s.RecoveryOperationID || head.Batch != s.RecoveryLANBatch ||
		!validCanonicalUUID(head.AppID) || !validCanonicalUUID(head.OperationID) ||
		!validCanonicalUUID(head.AccessRevisionID) || head.AccessRevisionNumber <= 0 ||
		!validCanonicalUUID(head.AllocationID) || !validCanonicalUUID(head.OwnerOperationID) ||
		head.Port < 8100 || head.Port > 8119 || !validLowerHex(head.ApprovalDigest, 64) ||
		head.BatchPosition < 1 || head.BatchCount < 1 || head.BatchPosition > head.BatchCount {
		return false
	}
	if head.Batch {
		if head.BatchPosition != s.RecoveryBatchHead+1 || head.BatchCount != s.RecoveryBatchCount {
			return false
		}
	} else if head.BatchPosition != 1 || head.BatchCount != 1 {
		return false
	}
	switch head.Kind {
	case RecoveryLANGrant:
		switch head.ClaimState {
		case "prepared", "applying", "db_active", "uncertain", "committed", "rolled_back":
			return true
		}
	case RecoveryLANDisable:
		switch head.ClaimState {
		case "prepared", "withdrawing", "uncertain", "committed":
			return true
		}
	}
	return false
}

func (s *Server) lanRecoveryUnavailable(w http.ResponseWriter, r *http.Request) {
	s.handlerProblem(w, r, operationGetLANRecoveryHead, http.StatusServiceUnavailable,
		"lan_access_unavailable", "LAN recovery head could not be safely proved", 0)
}
