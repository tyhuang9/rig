package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	idMessage      = "11111111-1111-4111-8111-111111111111"
	idController   = "22222222-2222-4222-8222-222222222222"
	idKey          = "33333333-3333-4333-8333-333333333333"
	idSession      = "44444444-4444-4444-8444-444444444444"
	idSubscription = "55555555-5555-4555-8555-555555555555"
	idDelivery     = "66666666-6666-4666-8666-666666666666"
	idEvent        = "77777777-7777-4777-8777-777777777777"
	idRotation     = "88888888-8888-4888-8888-888888888888"
	idNewKey       = "99999999-9999-4999-8999-999999999999"
)

var testTime = time.Date(2026, 8, 24, 15, 4, 5, 123456789, time.UTC)

func b64(size int, fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, size))
}
func envelope(t MessageType) Envelope { return NewEnvelope(t, idMessage, testTime) }

func validFrames() []Frame {
	source := &SourceTarget{SubscriptionID: idSubscription, Generation: 7}
	access := &AccessTarget{EventID: idEvent}
	return []Frame{
		&Hello{Envelope: envelope(TypeHello), ControllerID: idController, KeyID: idKey, ClientNonce: b64(NonceBytes, 1), ACKState: []ACKState{}},
		&Challenge{Envelope: envelope(TypeChallenge), SessionID: idSession, ServerNonce: b64(NonceBytes, 2), ACKDigest: b64(DigestBytes, 3), ExpiresAt: testTime.Add(time.Minute)},
		&Authenticate{Envelope: envelope(TypeAuthenticate), SessionID: idSession, Signature: b64(SignatureBytes, 4)},
		&Ready{Envelope: envelope(TypeReady), SessionID: idSession, HeartbeatIntervalSeconds: 30, MaxEnvelopeBytes: 1 << 20, MaxSubscriptions: 1000, MaxOutstanding: 100, SessionExpiresAt: testTime.Add(time.Hour), Reconnect: ReconnectPolicy{InitialDelayMillis: 500, MaximumDelayMillis: 30000, Multiplier: 2, JitterPercent: 20}},
		&SubscriptionsSync{Envelope: envelope(TypeSubscriptionsSync), Generation: 7, Subscriptions: []Subscription{{SubscriptionID: idSubscription, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main"}}},
		&SubscriptionsSynced{Envelope: envelope(TypeSubscriptionsSynced), TargetMessageID: idDelivery, Generation: 7, AcceptedCount: 1},
		&SourceDesired{Envelope: envelope(TypeSourceDesired), DeliveryID: idDelivery, SubscriptionID: idSubscription, Generation: 7, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main", ObservedSHA: strings.Repeat("a", 40), ObservedAt: testTime},
		&Ack{Envelope: envelope(TypeAck), TargetMessageID: idDelivery, Source: source},
		&Ack{Envelope: envelope(TypeAck), TargetMessageID: idDelivery, Access: access},
		&Reject{Envelope: envelope(TypeReject), TargetMessageID: idDelivery, Source: source, Code: "source.invalid_ref"},
		&Reject{Envelope: envelope(TypeReject), TargetMessageID: idDelivery, Access: access, Code: "access.denied"},
		&AccessChange{Envelope: envelope(TypeAccessChange), EventID: idEvent, InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: testTime, AckRequired: true},
		&Heartbeat{Envelope: envelope(TypeHeartbeat), Sequence: 1},
		&ProtocolError{Envelope: envelope(TypeProtocolError), TargetMessageID: idDelivery, Code: "frame.invalid", Fatal: true},
		&BindingRemove{Envelope: envelope(TypeBindingRemove), InstallationID: 1, RepositoryID: 2},
		&BindingRemoved{Envelope: envelope(TypeBindingRemoved), TargetMessageID: idDelivery, InstallationID: 1, RepositoryID: 2},
		&ControllerRevoke{Envelope: envelope(TypeControllerRevoke), ControllerID: idController},
		&ControllerRevoked{Envelope: envelope(TypeControllerRevoked), TargetMessageID: idDelivery, ControllerID: idController},
		&KeyRevoke{Envelope: envelope(TypeKeyRevoke), ControllerID: idController, KeyID: idKey},
		&KeyRevoked{Envelope: envelope(TypeKeyRevoked), TargetMessageID: idDelivery, ControllerID: idController, KeyID: idKey},
		&KeyRotationPropose{Envelope: envelope(TypeKeyRotationPropose), RotationID: idRotation, ControllerID: idController, OldKeyID: idKey, NewKeyID: idNewKey, NewPublicKey: b64(PublicKeyBytes, 5)},
		&KeyRotationChallenge{Envelope: envelope(TypeKeyRotationChallenge), TargetMessageID: idDelivery, RotationID: idRotation, ServerNonce: b64(NonceBytes, 6), ExpiresAt: testTime.Add(time.Minute)},
		&KeyRotationConfirm{Envelope: envelope(TypeKeyRotationConfirm), RotationID: idRotation, Signature: b64(SignatureBytes, 7)},
		&KeyRotationConfirmed{Envelope: envelope(TypeKeyRotationConfirmed), TargetMessageID: idDelivery, RotationID: idRotation},
		&KeyRotationFinalize{Envelope: envelope(TypeKeyRotationFinalize), RotationID: idRotation, RetireOldKey: true},
		&KeyRotationFinalized{Envelope: envelope(TypeKeyRotationFinalized), TargetMessageID: idDelivery, RotationID: idRotation, RetiredKeyID: idKey},
	}
}

func TestAllFrameTypesStrictRoundTrip(t *testing.T) {
	seen := map[MessageType]bool{}
	for _, input := range validFrames() {
		t.Run(string(input.base().Type)+reflect.TypeOf(input).String(), func(t *testing.T) {
			encoded, err := Encode(input, DefaultMaxEnvelopeBytes)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(encoded, &raw); err != nil {
				t.Fatal(err)
			}
			if raw["version"] != float64(Version) || raw["type"] != string(input.base().Type) || raw["messageId"] == nil || raw["sentAt"] == nil {
				t.Fatalf("base fields missing: %s", encoded)
			}
			decoded, err := Decode(encoded, DefaultMaxEnvelopeBytes)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(decoded) != reflect.TypeOf(input) {
				t.Fatalf("decoded %T, want %T", decoded, input)
			}
			seen[input.base().Type] = true
		})
	}
	if len(seen) != len(typeDirections) {
		t.Fatalf("covered %d unique types, contract has %d", len(seen), len(typeDirections))
	}
}

func TestDirectionAndSubprotocolMatrix(t *testing.T) {
	for messageType, want := range typeDirections {
		got, ok := DirectionFor(messageType)
		if !ok || got != want {
			t.Fatalf("DirectionFor(%q) = %v,%v", messageType, got, ok)
		}
	}
	if _, ok := DirectionFor("future"); ok {
		t.Fatal("unknown direction accepted")
	}
	if !RequiresSubprotocol([]string{"other", Subprotocol}) || RequiresSubprotocol([]string{"RIG.RELAY.V1"}) || RequiresSubprotocol(nil) {
		t.Fatal("subprotocol matching was not exact")
	}
}

func TestDecodeRejectsNonStrictJSON(t *testing.T) {
	valid, err := Encode(validFrames()[12], DefaultMaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		code ErrorCode
	}{
		{"empty", nil, CodeInvalidJSON},
		{"malformed", []byte(`{"version":1`), CodeInvalidJSON},
		{"unknown field", bytes.Replace(valid, []byte(`"sequence":1`), []byte(`"sequence":1,"unknown":true`), 1), CodeInvalidJSON},
		{"missing required field", bytes.Replace(valid, []byte(`,"sequence":1`), nil, 1), CodeInvalidJSON},
		{"case variant field", bytes.Replace(valid, []byte(`"version"`), []byte(`"Version"`), 1), CodeInvalidJSON},
		{"duplicate top field", bytes.Replace(valid, []byte(`"sequence":1`), []byte(`"sequence":1,"sequence":2`), 1), CodeInvalidJSON},
		{"trailing object", append(append([]byte(nil), valid...), []byte(` {}`)...), CodeInvalidJSON},
		{"trailing scalar", append(append([]byte(nil), valid...), []byte(` true`)...), CodeInvalidJSON},
		{"unknown type", bytes.Replace(valid, []byte(`"heartbeat"`), []byte(`"future"`), 1), CodeUnknownType},
		{"wrong version", bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":2`), 1), CodeInvalidEnvelope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(test.data, DefaultMaxEnvelopeBytes)
			assertProtocolCode(t, err, test.code)
		})
	}

	ack, _ := Encode(validFrames()[7], DefaultMaxEnvelopeBytes)
	nestedDuplicate := bytes.Replace(ack, []byte(`"generation":7`), []byte(`"generation":7,"generation":8`), 1)
	_, err = Decode(nestedDuplicate, DefaultMaxEnvelopeBytes)
	assertProtocolCode(t, err, CodeInvalidJSON)
}

func TestOperationalFrameBoundsAndRequiredSemantics(t *testing.T) {
	ready := validFrames()[3].(*Ready)
	mutations := []func(*Ready){
		func(v *Ready) { v.HeartbeatIntervalSeconds = 4 }, func(v *Ready) { v.HeartbeatIntervalSeconds = 301 },
		func(v *Ready) { v.MaxEnvelopeBytes = 4095 }, func(v *Ready) { v.MaxEnvelopeBytes = (8 << 20) + 1 },
		func(v *Ready) { v.MaxSubscriptions = 0 }, func(v *Ready) { v.MaxOutstanding = 0 },
		func(v *Ready) { v.MaxSubscriptions = MaxArrayItems + 1 },
		func(v *Ready) { v.Reconnect.InitialDelayMillis = 99 }, func(v *Ready) { v.Reconnect.MaximumDelayMillis = v.Reconnect.InitialDelayMillis - 1 },
		func(v *Ready) { v.Reconnect.Multiplier = 1 }, func(v *Ready) { v.Reconnect.JitterPercent = 51 },
	}
	for i, mutate := range mutations {
		copy := *ready
		mutate(&copy)
		if Validate(&copy) == nil {
			t.Fatalf("ready mutation %d accepted", i)
		}
	}
	if Validate(&Heartbeat{Envelope: envelope(TypeHeartbeat)}) == nil {
		t.Fatal("zero heartbeat sequence accepted")
	}
	if Validate(&AccessChange{Envelope: envelope(TypeAccessChange), EventID: idEvent, InstallationID: 1, ChangeCode: "access.removed", ObservedAt: testTime}) == nil {
		t.Fatal("access change without explicit ACK requirement accepted")
	}
	if Validate(&BindingRemove{Envelope: envelope(TypeBindingRemove), InstallationID: 1}) == nil {
		t.Fatal("broad binding removal accepted")
	}
	if Validate(&KeyRotationFinalize{Envelope: envelope(TypeKeyRotationFinalize), RotationID: idRotation}) == nil {
		t.Fatal("rotation finalize without old-key retirement accepted")
	}
	encoded, err := Encode(ready, DefaultMaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("maxAttempts")) || !bytes.Contains(encoded, []byte(`"jitterPercent":20`)) {
		t.Fatalf("reconnect contract is not jittered infinite retry: %s", encoded)
	}
}

func TestSubscriptionSetIsExactBoundedAndUnique(t *testing.T) {
	if Validate(&SubscriptionsSync{Envelope: envelope(TypeSubscriptionsSync), Generation: 1}) == nil {
		t.Fatal("null subscription set accepted")
	}
	duplicate := &SubscriptionsSync{Envelope: envelope(TypeSubscriptionsSync), Generation: 1, Subscriptions: []Subscription{{SubscriptionID: idSubscription, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main"}, {SubscriptionID: idSubscription, InstallationID: 3, RepositoryID: 4, Ref: "refs/heads/other"}}}
	if Validate(duplicate) == nil {
		t.Fatal("duplicate subscription accepted")
	}
	validEmpty := &SubscriptionsSync{Envelope: envelope(TypeSubscriptionsSync), Generation: 1, Subscriptions: []Subscription{}}
	if err := Validate(validEmpty); err != nil {
		t.Fatalf("explicit empty full set rejected: %v", err)
	}
}

func TestEnvelopeAndCollectionBounds(t *testing.T) {
	heartbeat := validFrames()[12]
	encoded, _ := Encode(heartbeat, DefaultMaxEnvelopeBytes)
	if _, err := Decode(append(encoded, bytes.Repeat([]byte{' '}, 100)...), len(encoded)-1); err == nil {
		t.Fatal("oversized decode accepted")
	}
	if _, err := Encode(heartbeat, 10); err == nil {
		t.Fatal("oversized encode accepted")
	}
	sync := &SubscriptionsSync{Envelope: envelope(TypeSubscriptionsSync), Generation: 1, Subscriptions: make([]Subscription, MaxArrayItems+1)}
	if err := Validate(sync); err == nil {
		t.Fatal("oversized subscriptions accepted")
	}
	ackState := make([]ACKState, MaxArrayItems+1)
	if err := ValidateACKState(ackState); err == nil {
		t.Fatal("oversized ACK state accepted")
	}
	reject := validFrames()[9].(*Reject)
	copy := *reject
	copy.Code = strings.Repeat("a", 129)
	if err := Validate(&copy); err == nil {
		t.Fatal("oversized code accepted")
	}
}

func TestIDsRefsSHAsAndFixedBase64URL(t *testing.T) {
	for _, id := range []string{"", "00000000-0000-0000-0000-000000000000", "11111111-1111-4111-8111-11111111111A", "{11111111-1111-4111-8111-111111111111}", "11111111111141118111111111111111"} {
		if validUUID("id", id) == nil {
			t.Fatalf("invalid UUID accepted: %q", id)
		}
	}
	validRefs := []string{"refs/heads/main", "refs/heads/feature/unit-8", "refs/heads/release.v1"}
	for _, ref := range validRefs {
		if err := ValidRef(ref); err != nil {
			t.Fatalf("valid ref %q: %v", ref, err)
		}
	}
	invalidRefs := []string{"main", "refs/tags/v1", "refs/heads/", "refs/heads/.hidden", "refs/heads/a..b", "refs/heads/a.lock", "refs/heads/a//b", "refs/heads/a@{b", "refs/heads/a b", "refs/heads/a~b", "refs/heads/a^b", "refs/heads/a:b", "refs/heads/a?b", "refs/heads/a*b", "refs/heads/a[b", "refs/heads/a\\b", "refs/heads/a."}
	for _, ref := range invalidRefs {
		if ValidRef(ref) == nil {
			t.Fatalf("invalid ref accepted: %q", ref)
		}
	}
	for _, sha := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("A", 40), strings.Repeat("g", 40)} {
		if ValidSHA(sha) == nil {
			t.Fatalf("invalid SHA accepted: %q", sha)
		}
	}
	for _, encoded := range []string{b64(31, 1), b64(33, 1), base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "+invalid/"} {
		if validB64("nonce", encoded, NonceBytes) == nil {
			t.Fatalf("invalid encoding accepted: %q", encoded)
		}
	}
}

func TestACKAndRejectRequireExactlyOneTypedTarget(t *testing.T) {
	for _, frame := range []Frame{
		&Ack{Envelope: envelope(TypeAck), TargetMessageID: idDelivery},
		&Ack{Envelope: envelope(TypeAck), TargetMessageID: idDelivery, Source: &SourceTarget{SubscriptionID: idSubscription, Generation: 1}, Access: &AccessTarget{EventID: idEvent}},
		&Reject{Envelope: envelope(TypeReject), TargetMessageID: idDelivery, Code: "invalid"},
		&Reject{Envelope: envelope(TypeReject), TargetMessageID: idDelivery, Source: &SourceTarget{SubscriptionID: idSubscription, Generation: 1}, Access: &AccessTarget{EventID: idEvent}, Code: "invalid"},
	} {
		if err := Validate(frame); err == nil {
			t.Fatalf("invalid target accepted: %#v", frame)
		}
	}
}

func TestACKStateMustBeSortedUniqueAndValid(t *testing.T) {
	a := ACKState{SubscriptionID: idSubscription, Generation: 1}
	b := ACKState{SubscriptionID: idDelivery, Generation: 2}
	sorted := SortACKState([]ACKState{a, b})
	if err := ValidateACKState(sorted); err != nil {
		t.Fatal(err)
	}
	for _, state := range [][]ACKState{{b, a}, {a, a}, {{SubscriptionID: idSubscription, Generation: 0}}, {{SubscriptionID: "bad", Generation: 1}}, nil} {
		if ValidateACKState(state) == nil {
			t.Fatalf("invalid ACK state accepted: %#v", state)
		}
	}
}

func assertProtocolCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var target *Error
	if !errors.As(err, &target) || target.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func FuzzDecode(f *testing.F) {
	for _, frame := range validFrames() {
		if data, err := Encode(frame, DefaultMaxEnvelopeBytes); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(`{"version":1,"type":"heartbeat","type":"ack"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > DefaultMaxEnvelopeBytes+1 {
			data = data[:DefaultMaxEnvelopeBytes+1]
		}
		frame, err := Decode(data, DefaultMaxEnvelopeBytes)
		if err == nil {
			if frame == nil {
				t.Fatal("nil frame without error")
			}
			if validationErr := Validate(frame); validationErr != nil {
				t.Fatalf("decoded invalid frame: %v", validationErr)
			}
		}
	})
}

func FuzzValidRef(f *testing.F) {
	for _, seed := range []string{"refs/heads/main", "main", "refs/heads/a..b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		err := ValidRef(value)
		if err == nil && !strings.HasPrefix(value, "refs/heads/") {
			t.Fatalf("invalid prefix accepted: %q", value)
		}
	})
}
