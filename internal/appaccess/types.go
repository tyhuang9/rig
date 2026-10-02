// Package appaccess persists administrator-approved LAN gateway profiles,
// per-application access revisions, and durable port ownership. Its profile
// and access heads are approved desired state, not observed or applied state.
// It does not enumerate host interfaces, publish listeners, reload routes, or
// attest that an interface identifier belongs to the current host. Callers
// must prove those facts before using a stored approval to perform an external
// action or reporting a LAN URL.
package appaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	GatewayPortStart uint16 = 8100
	GatewayPortEnd   uint16 = 8119
)

type ApprovalAction string

const (
	ActionConfigureGateway ApprovalAction = "configure_lan_gateway"
	ActionUpgradeGateway   ApprovalAction = "upgrade_generated_ingress"
	ActionEnableAppAccess  ApprovalAction = "enable_lan_access"
)

const (
	GatewayUpgradeTargetFormat    = 2
	GatewayUpgradeIdentityVersion = "v2"
)

type GatewayProfileUpgradeState string

const (
	GatewayProfileUpgradePrepared   GatewayProfileUpgradeState = "prepared"
	GatewayProfileUpgradeServing    GatewayProfileUpgradeState = "serving"
	GatewayProfileUpgradeUnresolved GatewayProfileUpgradeState = "unresolved"
	GatewayProfileUpgradeCommitted  GatewayProfileUpgradeState = "committed"
	GatewayProfileUpgradeRolledBack GatewayProfileUpgradeState = "rolled_back"
)

type AllocationState string

const (
	AllocationReserved  AllocationState = "reserved"
	AllocationActive    AllocationState = "active"
	AllocationUncertain AllocationState = "uncertain"
)

var (
	ErrInvalidInput        = errors.New("invalid LAN access input")
	ErrNotFound            = errors.New("LAN access record not found")
	ErrConflict            = errors.New("LAN access revision conflict")
	ErrIdempotencyMismatch = errors.New("LAN access idempotency payload mismatch")
	ErrApprovalRequired    = errors.New("LAN access administrator approval required")
	ErrPoolExhausted       = errors.New("LAN access port pool exhausted")
	ErrReservationReleased = errors.New("LAN access reservation was released")
	ErrInvalidTransition   = errors.New("invalid LAN allocation transition")
	ErrInvalidStoredState  = errors.New("invalid stored LAN access state")
)

// Approval binds an authenticated administrator to one canonical action
// digest. Persistence alone does not authorize or perform any network change.
type Approval struct {
	Action     ApprovalAction `json:"action"`
	SpecDigest string         `json:"specDigest"`
	ActorID    string         `json:"actorId"`
}

// GatewayProfileSpec records the exact advertised and bound RFC1918 IPv4 plus
// an opaque platform interface identifier. The future controller must
// enumerate the host and prove the identifier/address association both before
// mutation and during observation; this package deliberately cannot do that.
type GatewayProfileSpec struct {
	SelectedIPv4 string `json:"selectedIpv4"`
	InterfaceID  string `json:"interfaceId"`
	PortStart    uint16 `json:"portStart"`
	PortEnd      uint16 `json:"portEnd"`
}

type ConfigureGatewayInput struct {
	OperationID            string
	ExpectedRevisionNumber int64
	Spec                   GatewayProfileSpec
	Approval               Approval
}

type GatewayProfileRevision struct {
	ID             string
	RevisionNumber int64
	OperationID    string
	Spec           GatewayProfileSpec
	SpecDigest     string
	ApprovedBy     string
	ApprovedAt     time.Time
}

// GatewayProfileUpgradeSpec is the durable authorization boundary for the
// one-way generated-ingress v1-to-v2 migration. TargetFormat and
// IdentityVersion are intentionally fixed by GatewayProfileUpgradeSpecDigest;
// callers cannot approve a future format through this action by accident.
type GatewayProfileUpgradeSpec struct {
	ProfileRevisionID     string `json:"profileRevisionId"`
	ProfileRevisionNumber int64  `json:"profileRevisionNumber"`
	ProfileSpecDigest     string `json:"profileSpecDigest"`
}

type ClaimGatewayProfileUpgradeInput struct {
	OperationID string
	Spec        GatewayProfileUpgradeSpec
	Approval    Approval
}

type GatewayProfileUpgradeClaimOwner struct {
	OperationID           string
	ProfileRevisionID     string
	ProfileRevisionNumber int64
}

// GatewayProfileUpgradeClaim binds one operation and administrator approval
// to one exact profile revision. State changes are retained separately as an
// append-only event history. Prepared, serving, unresolved, and committed
// claims pin the profile; only an attested rolled-back claim releases it.
type GatewayProfileUpgradeClaim struct {
	OperationID           string
	RequestDigest         string
	ProfileRevisionID     string
	ProfileRevisionNumber int64
	ProfileSpecDigest     string
	ApprovedBy            string
	ApprovedAt            time.Time
	State                 GatewayProfileUpgradeState
	StateSequence         int64
	UpdatedAt             time.Time
}

type ReserveAppAccessInput struct {
	AppID                        string
	OperationID                  string
	ExpectedRevisionNumber       int64
	GatewayProfileRevisionID     string
	GatewayProfileRevisionNumber int64
}

type Allocation struct {
	ID                           string
	AppID                        string
	Port                         uint16
	OwnerOperationID             string
	OwnerRevisionID              string
	ReservationDigest            string
	GatewayProfileRevisionID     string
	GatewayProfileRevisionNumber int64
	State                        AllocationState
	ReservedAt                   time.Time
	ReleasedAt                   *time.Time
}

type AppAccessSpec struct {
	AppID                        string `json:"appId"`
	AllocationID                 string `json:"allocationId"`
	Port                         uint16 `json:"port"`
	GatewayProfileRevisionID     string `json:"gatewayProfileRevisionId"`
	GatewayProfileRevisionNumber int64  `json:"gatewayProfileRevisionNumber"`
}

type ApproveAppAccessInput struct {
	AppID                  string
	OperationID            string
	AllocationID           string
	ExpectedRevisionNumber int64
	Approval               Approval
}

type AppAccessRevision struct {
	ID             string
	AppID          string
	RevisionNumber int64
	OperationID    string
	SpecDigest     string
	ApprovedBy     string
	ApprovedAt     time.Time
	Allocation     Allocation
}

// AllocationOwner is required for every state-changing allocation operation.
// All four identities must match the stored owner; an allocation ID alone is
// intentionally insufficient.
type AllocationOwner struct {
	AllocationID     string
	AppID            string
	OperationID      string
	AccessRevisionID string
}

func GatewayProfileSpecDigest(spec GatewayProfileSpec) (string, error) {
	canonical, err := canonicalGatewaySpec(spec)
	if err != nil {
		return "", err
	}
	return digestJSON(struct {
		Version int                `json:"version"`
		Action  ApprovalAction     `json:"action"`
		Spec    GatewayProfileSpec `json:"spec"`
	}{Version: 1, Action: ActionConfigureGateway, Spec: canonical})
}

// GatewayProfileUpgradeSpecDigest is the single canonical digest shared by
// the database approval and the generated-ingress protected journal.
func GatewayProfileUpgradeSpecDigest(spec GatewayProfileUpgradeSpec) (string, error) {
	if !validUUID(spec.ProfileRevisionID) || spec.ProfileRevisionNumber <= 0 || !validDigest(spec.ProfileSpecDigest) {
		return "", ErrInvalidInput
	}
	return digestJSON(struct {
		Version               int            `json:"version"`
		Action                ApprovalAction `json:"action"`
		TargetFormat          int            `json:"targetFormat"`
		ProfileRevisionID     string         `json:"profileRevisionId"`
		ProfileRevisionNumber int64          `json:"profileRevisionNumber"`
		ProfileSpecDigest     string         `json:"profileSpecDigest"`
		IdentityVersion       string         `json:"identityVersion"`
	}{
		Version:               1,
		Action:                ActionUpgradeGateway,
		TargetFormat:          GatewayUpgradeTargetFormat,
		ProfileRevisionID:     spec.ProfileRevisionID,
		ProfileRevisionNumber: spec.ProfileRevisionNumber,
		ProfileSpecDigest:     spec.ProfileSpecDigest,
		IdentityVersion:       GatewayUpgradeIdentityVersion,
	})
}

func validGatewayProfileUpgradeState(value GatewayProfileUpgradeState) bool {
	switch value {
	case GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing, GatewayProfileUpgradeUnresolved, GatewayProfileUpgradeCommitted, GatewayProfileUpgradeRolledBack:
		return true
	default:
		return false
	}
}

func validGatewayProfileUpgradeTransition(from, to GatewayProfileUpgradeState) bool {
	switch from {
	case GatewayProfileUpgradePrepared:
		return to == GatewayProfileUpgradeServing || to == GatewayProfileUpgradeUnresolved || to == GatewayProfileUpgradeCommitted || to == GatewayProfileUpgradeRolledBack
	case GatewayProfileUpgradeServing:
		return to == GatewayProfileUpgradeCommitted || to == GatewayProfileUpgradeUnresolved || to == GatewayProfileUpgradeRolledBack
	case GatewayProfileUpgradeUnresolved:
		return to == GatewayProfileUpgradeCommitted || to == GatewayProfileUpgradeRolledBack
	default:
		return false
	}
}

func AppAccessSpecFor(allocation Allocation) AppAccessSpec {
	return AppAccessSpec{
		AppID:                        allocation.AppID,
		AllocationID:                 allocation.ID,
		Port:                         allocation.Port,
		GatewayProfileRevisionID:     allocation.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: allocation.GatewayProfileRevisionNumber,
	}
}

func AppAccessSpecDigest(spec AppAccessSpec) (string, error) {
	if !validUUID(spec.AppID) || !validUUID(spec.AllocationID) || spec.Port < GatewayPortStart || spec.Port > GatewayPortEnd || !validUUID(spec.GatewayProfileRevisionID) || spec.GatewayProfileRevisionNumber <= 0 {
		return "", ErrInvalidInput
	}
	return digestJSON(struct {
		Version int            `json:"version"`
		Action  ApprovalAction `json:"action"`
		Spec    AppAccessSpec  `json:"spec"`
	}{Version: 1, Action: ActionEnableAppAccess, Spec: spec})
}

func canonicalGatewaySpec(spec GatewayProfileSpec) (GatewayProfileSpec, error) {
	address, err := netip.ParseAddr(spec.SelectedIPv4)
	if err != nil || !address.Is4() || !address.IsPrivate() || address.String() != spec.SelectedIPv4 {
		return GatewayProfileSpec{}, ErrInvalidInput
	}
	if !validText(spec.InterfaceID, 512) || spec.PortStart < GatewayPortStart || spec.PortEnd < spec.PortStart || spec.PortEnd > GatewayPortEnd {
		return GatewayProfileSpec{}, ErrInvalidInput
	}
	return spec, nil
}

func validApproval(approval Approval, action ApprovalAction, digest string) bool {
	return approval.Action == action && approval.SpecDigest == digest && validOpaqueText(approval.ActorID, 256)
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validText(value string, maximum int) bool {
	if !validOpaqueText(value, maximum) || strings.TrimSpace(value) != value {
		return false
	}
	return true
}

func validOpaqueText(value string, maximum int) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > maximum || strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func digestJSON(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
