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
	ActionEnableAppAccess  ApprovalAction = "enable_lan_access"
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
