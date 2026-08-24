package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/google/uuid"
)

const (
	domainEnrollment = "rig.relay.v1/enrollment-possession"
	domainWSSAuth    = "rig.relay.v1/wss-authentication"
	domainACKDigest  = "rig.relay.v1/ack-digest"
	domainRotation   = "rig.relay.v1/key-rotation-possession"
)

type EnrollmentProof struct {
	ControllerID   string
	KeyID          string
	PublicKey      string
	InstallationID int64
	RepositoryID   int64
	RequestNonce   string
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

type AuthenticationBinding struct {
	ControllerID string
	KeyID        string
	SessionID    string
	ClientNonce  string
	ServerNonce  string
	ACKDigest    string
	ExpiresAt    time.Time
}

type RotationProof struct {
	RotationID   string
	ControllerID string
	OldKeyID     string
	NewKeyID     string
	NewPublicKey string
	SessionID    string
	ServerNonce  string
	ExpiresAt    time.Time
}

func EnrollmentTranscript(p EnrollmentProof) ([]byte, error) {
	if err := first(validUUID("controllerId", p.ControllerID), validUUID("keyId", p.KeyID), validB64("publicKey", p.PublicKey, PublicKeyBytes), positive("installationId", p.InstallationID), positive("repositoryId", p.RepositoryID), validB64("requestNonce", p.RequestNonce, NonceBytes), validTime("issuedAt", p.IssuedAt), validTime("expiresAt", p.ExpiresAt)); err != nil {
		return nil, err
	}
	if !p.ExpiresAt.After(p.IssuedAt) {
		return nil, invalid("expiresAt")
	}
	return buildTranscript(domainEnrollment, encodeUint64(Version), []byte(p.ControllerID), []byte(p.KeyID), []byte(p.PublicKey), encodeInt64(p.InstallationID), encodeInt64(p.RepositoryID), []byte(p.RequestNonce), encodeTime(p.IssuedAt), encodeTime(p.ExpiresAt)), nil
}

func WSSAuthenticationTranscript(p AuthenticationBinding) ([]byte, error) {
	if err := first(validUUID("controllerId", p.ControllerID), validUUID("keyId", p.KeyID), validUUID("sessionId", p.SessionID), validB64("clientNonce", p.ClientNonce, NonceBytes), validB64("serverNonce", p.ServerNonce, NonceBytes), validB64("ackDigest", p.ACKDigest, DigestBytes), validTime("expiresAt", p.ExpiresAt)); err != nil {
		return nil, err
	}
	return buildTranscript(domainWSSAuth, encodeUint64(Version), []byte(p.ControllerID), []byte(p.KeyID), []byte(p.SessionID), []byte(p.ClientNonce), []byte(p.ServerNonce), []byte(p.ACKDigest), encodeTime(p.ExpiresAt)), nil
}

func KeyRotationTranscript(p RotationProof) ([]byte, error) {
	if p.OldKeyID == p.NewKeyID {
		return nil, invalid("newKeyId")
	}
	if err := first(validUUID("rotationId", p.RotationID), validUUID("controllerId", p.ControllerID), validUUID("oldKeyId", p.OldKeyID), validUUID("newKeyId", p.NewKeyID), validB64("newPublicKey", p.NewPublicKey, PublicKeyBytes), validUUID("sessionId", p.SessionID), validB64("serverNonce", p.ServerNonce, NonceBytes), validTime("expiresAt", p.ExpiresAt)); err != nil {
		return nil, err
	}
	return buildTranscript(domainRotation, encodeUint64(Version), []byte(p.RotationID), []byte(p.ControllerID), []byte(p.OldKeyID), []byte(p.NewKeyID), []byte(p.NewPublicKey), []byte(p.SessionID), []byte(p.ServerNonce), encodeTime(p.ExpiresAt)), nil
}

// CanonicalACKDigest accepts only strictly sorted, duplicate-free ACK state.
// Requiring canonical wire order prevents equivalent states from having
// multiple authenticated representations.
func CanonicalACKDigest(state []ACKState) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if err := ValidateACKState(state); err != nil {
		return zero, err
	}
	fields := make([][]byte, 0, 1+len(state)*2)
	fields = append(fields, encodeUint64(uint64(len(state))))
	for _, ack := range state {
		fields = append(fields, []byte(ack.SubscriptionID), encodeUint64(ack.Generation))
	}
	return sha256.Sum256(buildTranscript(domainACKDigest, fields...)), nil
}

func ValidateACKState(state []ACKState) error {
	if state == nil {
		return invalid("ackState")
	}
	if len(state) > MaxArrayItems {
		return invalid("ackState")
	}
	previous := ""
	for i, ack := range state {
		if err := first(validUUID("ackState.subscriptionId", ack.SubscriptionID), positiveGeneration(ack.Generation)); err != nil {
			return err
		}
		if i > 0 && previous >= ack.SubscriptionID {
			return invalid("ackState")
		}
		previous = ack.SubscriptionID
	}
	return nil
}

func SortACKState(state []ACKState) []ACKState {
	canonical := append([]ACKState(nil), state...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].SubscriptionID < canonical[j].SubscriptionID })
	return canonical
}

// Issuer centralizes injected entropy and time for replay-sensitive frames.
type Issuer struct {
	Entropy io.Reader
	Now     func() time.Time
}

func (i Issuer) NewHello(controllerID, keyID string, state []ACKState) (*Hello, error) {
	if err := first(validUUID("controllerId", controllerID), validUUID("keyId", keyID), ValidateACKState(state)); err != nil {
		return nil, err
	}
	envelope, err := i.envelope(TypeHello)
	if err != nil {
		return nil, err
	}
	nonce, err := NewNonce(i.entropy())
	if err != nil {
		return nil, err
	}
	return &Hello{Envelope: envelope, ControllerID: controllerID, KeyID: keyID, ClientNonce: nonce, ACKState: append([]ACKState(nil), state...)}, nil
}

func (i Issuer) NewChallenge(lifetime time.Duration, state []ACKState) (*Challenge, error) {
	if err := ValidateACKState(state); err != nil {
		return nil, err
	}
	if lifetime <= 0 {
		return nil, invalid("lifetime")
	}
	envelope, err := i.envelope(TypeChallenge)
	if err != nil {
		return nil, err
	}
	sessionID, err := newUUID(i.entropy())
	if err != nil {
		return nil, err
	}
	nonce, err := NewNonce(i.entropy())
	if err != nil {
		return nil, err
	}
	digest, err := CanonicalACKDigest(state)
	if err != nil {
		return nil, err
	}
	return &Challenge{Envelope: envelope, SessionID: sessionID, ServerNonce: nonce, ACKDigest: base64.RawURLEncoding.EncodeToString(digest[:]), ExpiresAt: i.now().UTC().Add(lifetime)}, nil
}

func (i Issuer) envelope(t MessageType) (Envelope, error) {
	messageID, err := newUUID(i.entropy())
	if err != nil {
		return Envelope{}, err
	}
	return NewEnvelope(t, messageID, i.now()), nil
}
func NewNonce(entropy io.Reader) (string, error) {
	if entropy == nil {
		return "", errors.New("entropy source is required")
	}
	b := make([]byte, NonceBytes)
	if _, err := io.ReadFull(entropy, b); err != nil {
		return "", err
	}
	defer clear(b)
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func newUUID(entropy io.Reader) (string, error) {
	id, err := uuid.NewRandomFromReader(entropy)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func Sign(privateKey ed25519.PrivateKey, transcript []byte) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize || len(transcript) == 0 {
		return "", invalid("signature")
	}
	signature := ed25519.Sign(privateKey, transcript)
	defer clear(signature)
	return base64.RawURLEncoding.EncodeToString(signature), nil
}
func Verify(publicKey ed25519.PublicKey, transcript []byte, encodedSignature string) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(transcript) == 0 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	defer clear(signature)
	return ed25519.Verify(publicKey, transcript, signature)
}
func (i Issuer) entropy() io.Reader {
	if i.Entropy != nil {
		return i.Entropy
	}
	return rand.Reader
}
func (i Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}
func buildTranscript(domain string, fields ...[]byte) []byte {
	size := 8 + len(domain)
	for _, field := range fields {
		size += 8 + len(field)
	}
	b := make([]byte, 0, size)
	b = appendLengthPrefixed(b, []byte(domain))
	for _, field := range fields {
		b = appendLengthPrefixed(b, field)
	}
	return b
}
func appendLengthPrefixed(dst, value []byte) []byte {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	dst = append(dst, length[:]...)
	return append(dst, value...)
}
func encodeUint64(value uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], value)
	return b[:]
}
func encodeInt64(value int64) []byte    { return encodeUint64(uint64(value)) }
func encodeTime(value time.Time) []byte { return []byte(value.UTC().Format(time.RFC3339Nano)) }
