package wss

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	commandMessage1 = "11111111-1111-4111-8111-111111111111"
	commandMessage2 = "22222222-2222-4222-8222-222222222222"
	commandID1      = "33333333-3333-4333-8333-333333333333"
	commandID2      = "44444444-4444-4444-8444-444444444444"
	commandID3      = "55555555-5555-4555-8555-555555555555"
	commandID4      = "66666666-6666-4666-8666-666666666666"
)

var commandTime = time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)

func commandEnvelope(messageType protocol.MessageType) protocol.Envelope {
	return protocol.NewEnvelope(messageType, commandMessage1, commandTime)
}

func assertCommandMutation(t *testing.T, original, mutated protocol.Frame) {
	t.Helper()
	left, err := canonicalSessionCommand(original, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		t.Fatalf("original: %v", err)
	}
	right, err := canonicalSessionCommand(mutated, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		t.Fatalf("mutated: %v", err)
	}
	if left.Digest == right.Digest {
		t.Fatal("security-field mutation did not alter command digest")
	}
}

func TestCanonicalSessionCommandBindsDecisionTargetsAndRejectCode(t *testing.T) {
	source := &protocol.Ack{Envelope: commandEnvelope(protocol.TypeAck), TargetMessageID: commandMessage2, Source: &protocol.SourceTarget{SubscriptionID: commandID1, Generation: 1}}
	mutatedTarget := *source
	mutatedTarget.TargetMessageID = commandID2
	assertCommandMutation(t, source, &mutatedTarget)
	mutatedSource := *source
	mutatedSource.Source = &protocol.SourceTarget{SubscriptionID: commandID2, Generation: 1}
	assertCommandMutation(t, source, &mutatedSource)
	mutatedGeneration := *source
	mutatedGeneration.Source = &protocol.SourceTarget{SubscriptionID: commandID1, Generation: 2}
	assertCommandMutation(t, source, &mutatedGeneration)

	access := &protocol.Ack{Envelope: commandEnvelope(protocol.TypeAck), TargetMessageID: commandMessage2, Access: &protocol.AccessTarget{EventID: commandID1}}
	mutatedAccess := *access
	mutatedAccess.Access = &protocol.AccessTarget{EventID: commandID2}
	assertCommandMutation(t, access, &mutatedAccess)

	reject := &protocol.Reject{Envelope: commandEnvelope(protocol.TypeReject), TargetMessageID: commandMessage2, Source: &protocol.SourceTarget{SubscriptionID: commandID1, Generation: 1}, Code: "deployment.failed"}
	mutatedCode := *reject
	mutatedCode.Code = "deployment.paused"
	assertCommandMutation(t, reject, &mutatedCode)
	mutatedRejectTarget := *reject
	mutatedRejectTarget.TargetMessageID = commandID2
	assertCommandMutation(t, reject, &mutatedRejectTarget)
	mutatedRejectSource := *reject
	mutatedRejectSource.Source = &protocol.SourceTarget{SubscriptionID: commandID2, Generation: 1}
	assertCommandMutation(t, reject, &mutatedRejectSource)
}

func TestCanonicalSessionCommandBindsEnvelopeAndFullSubscriptionSet(t *testing.T) {
	sync := &protocol.SubscriptionsSync{Envelope: commandEnvelope(protocol.TypeSubscriptionsSync), Generation: 1, Subscriptions: []protocol.Subscription{{SubscriptionID: commandID1, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}}}
	mutatedMessage := *sync
	mutatedMessage.MessageID = commandMessage2
	assertCommandMutation(t, sync, &mutatedMessage)
	mutatedTime := *sync
	mutatedTime.SentAt = commandTime.Add(time.Second)
	assertCommandMutation(t, sync, &mutatedTime)
	mutatedGeneration := *sync
	mutatedGeneration.Generation = 2
	assertCommandMutation(t, sync, &mutatedGeneration)
	for _, mutate := range []func(*protocol.Subscription){
		func(v *protocol.Subscription) { v.SubscriptionID = commandID2 },
		func(v *protocol.Subscription) { v.InstallationID = 11 },
		func(v *protocol.Subscription) { v.RepositoryID = 21 },
		func(v *protocol.Subscription) { v.Ref = "refs/heads/other" },
	} {
		clone := *sync
		clone.Subscriptions = append([]protocol.Subscription(nil), sync.Subscriptions...)
		mutate(&clone.Subscriptions[0])
		assertCommandMutation(t, sync, &clone)
	}
	mutatedMembership := *sync
	mutatedMembership.Subscriptions = append(append([]protocol.Subscription(nil), sync.Subscriptions...), protocol.Subscription{SubscriptionID: commandID2, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"})
	assertCommandMutation(t, sync, &mutatedMembership)
}

func TestCanonicalSessionCommandBindsAdministrativeIdentity(t *testing.T) {
	binding := &protocol.BindingRemove{Envelope: commandEnvelope(protocol.TypeBindingRemove), InstallationID: 10, RepositoryID: 20}
	mutatedBinding := *binding
	mutatedBinding.RepositoryID = 21
	assertCommandMutation(t, binding, &mutatedBinding)
	mutatedInstallation := *binding
	mutatedInstallation.InstallationID = 11
	assertCommandMutation(t, binding, &mutatedInstallation)

	controller := &protocol.ControllerRevoke{Envelope: commandEnvelope(protocol.TypeControllerRevoke), ControllerID: commandID1}
	mutatedController := *controller
	mutatedController.ControllerID = commandID2
	assertCommandMutation(t, controller, &mutatedController)

	key := &protocol.KeyRevoke{Envelope: commandEnvelope(protocol.TypeKeyRevoke), ControllerID: commandID1, KeyID: commandID2}
	mutatedKeyController := *key
	mutatedKeyController.ControllerID = commandID3
	assertCommandMutation(t, key, &mutatedKeyController)
	mutatedKey := *key
	mutatedKey.KeyID = commandID3
	assertCommandMutation(t, key, &mutatedKey)
}

func TestCanonicalSessionCommandBindsEveryRotationField(t *testing.T) {
	public1 := bytes.Repeat([]byte{1}, protocol.PublicKeyBytes)
	public2 := bytes.Repeat([]byte{2}, protocol.PublicKeyBytes)
	propose := &protocol.KeyRotationPropose{Envelope: commandEnvelope(protocol.TypeKeyRotationPropose), RotationID: commandID1, ControllerID: commandID2, OldKeyID: commandID3, NewKeyID: commandID4, NewPublicKey: base64.RawURLEncoding.EncodeToString(public1)}
	mutations := []*protocol.KeyRotationPropose{}
	for _, mutate := range []func(*protocol.KeyRotationPropose){
		func(v *protocol.KeyRotationPropose) { v.RotationID = commandID4 },
		func(v *protocol.KeyRotationPropose) { v.ControllerID = commandID4 },
		func(v *protocol.KeyRotationPropose) { v.OldKeyID = commandMessage2 },
		func(v *protocol.KeyRotationPropose) { v.NewKeyID = commandID1 },
		func(v *protocol.KeyRotationPropose) { v.NewPublicKey = base64.RawURLEncoding.EncodeToString(public2) },
	} {
		clone := *propose
		mutate(&clone)
		mutations = append(mutations, &clone)
	}
	for _, mutation := range mutations {
		assertCommandMutation(t, propose, mutation)
	}

	confirm := &protocol.KeyRotationConfirm{Envelope: commandEnvelope(protocol.TypeKeyRotationConfirm), RotationID: commandID1, Signature: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, protocol.SignatureBytes))}
	mutatedRotation := *confirm
	mutatedRotation.RotationID = commandID2
	assertCommandMutation(t, confirm, &mutatedRotation)
	mutatedSignature := *confirm
	mutatedSignature.Signature = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, protocol.SignatureBytes))
	assertCommandMutation(t, confirm, &mutatedSignature)

	finalize := &protocol.KeyRotationFinalize{Envelope: commandEnvelope(protocol.TypeKeyRotationFinalize), RotationID: commandID1, RetireOldKey: true}
	mutatedFinalize := *finalize
	mutatedFinalize.RotationID = commandID2
	assertCommandMutation(t, finalize, &mutatedFinalize)
	mutatedFinalize.RetireOldKey = false
	if _, err := canonicalSessionCommand(&mutatedFinalize, protocol.DefaultMaxEnvelopeBytes); err == nil {
		t.Fatal("retireOldKey=false was not rejected")
	}
}
