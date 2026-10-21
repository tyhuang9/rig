// Package generatedidentity owns the dependency-free deterministic identity
// codec shared by generated runtime execution and restart recovery.
package generatedidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Slot string

const (
	SlotBlue  Slot = "blue"
	SlotGreen Slot = "green"

	// NetworkOwnershipLabelValue identifies the controller-owned application
	// network shared by the generated runtime and ingress gateway.
	NetworkOwnershipLabelValue = "generated-runtime-network"
)

type CandidateDescription struct {
	Slot          Slot
	ContainerName string
	NetworkName   string
	NetworkAlias  string
}

type AppNetworkDescription struct{ Name string }

func InactiveSlot(active Slot) (Slot, error) {
	switch active {
	case "", SlotGreen:
		return SlotBlue, nil
	case SlotBlue:
		return SlotGreen, nil
	default:
		return "", errors.New("invalid generated runtime slot")
	}
}

func DescribeInactiveCandidate(appID, component string, active Slot) (CandidateDescription, error) {
	if !canonicalUUID(appID) || !validText(component, 256) {
		return CandidateDescription{}, errors.New("invalid generated runtime candidate identity")
	}
	slot, err := InactiveSlot(active)
	if err != nil {
		return CandidateDescription{}, err
	}
	return CandidateDescription{
		Slot:          slot,
		ContainerName: ContainerName(appID, component, slot),
		NetworkName:   NetworkName(appID),
		NetworkAlias:  ContainerAlias(component, slot),
	}, nil
}

func DescribeAppNetwork(appID string) (AppNetworkDescription, error) {
	if !canonicalUUID(appID) {
		return AppNetworkDescription{}, errors.New("invalid generated runtime application identity")
	}
	return AppNetworkDescription{Name: NetworkName(appID)}, nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validText(value string, maximum int) bool {
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

func NetworkName(appID string) string { return "rig-a-" + strings.ReplaceAll(appID, "-", "") }

func componentDigest(component string) string {
	sum := sha256.Sum256([]byte(component))
	return hex.EncodeToString(sum[:])[:12]
}

func ContainerName(appID, component string, slot Slot) string {
	return NetworkName(appID) + "-c-" + componentDigest(component) + "-" + string(slot)
}

func ContainerAlias(component string, slot Slot) string {
	return "rig-c-" + componentDigest(component) + "-" + string(slot)
}
