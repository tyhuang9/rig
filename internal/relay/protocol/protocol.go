// Package protocol defines the bounded version-one wire contract between a
// controller and the official webhook relay.
package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	Version                 = 1
	Subprotocol             = "rig.relay.v1"
	DefaultMaxEnvelopeBytes = 1 << 20
	MaxStringBytes          = 4096
	MaxArrayItems           = 1000
	NonceBytes              = 32
	DigestBytes             = 32
	PublicKeyBytes          = ed25519.PublicKeySize
	SignatureBytes          = ed25519.SignatureSize
)

type MessageType string

const (
	TypeHello                MessageType = "hello"
	TypeChallenge            MessageType = "challenge"
	TypeAuthenticate         MessageType = "authenticate"
	TypeReady                MessageType = "ready"
	TypeSubscriptionsSync    MessageType = "subscriptions.sync"
	TypeSubscriptionsSynced  MessageType = "subscriptions.synced"
	TypeSourceDesired        MessageType = "source.desired"
	TypeAck                  MessageType = "ack"
	TypeReject               MessageType = "reject"
	TypeAccessChange         MessageType = "access-change"
	TypeHeartbeat            MessageType = "heartbeat"
	TypeProtocolError        MessageType = "protocol-error"
	TypeBindingRemove        MessageType = "binding.remove"
	TypeBindingRemoved       MessageType = "binding.removed"
	TypeControllerRevoke     MessageType = "controller.revoke"
	TypeControllerRevoked    MessageType = "controller.revoked"
	TypeKeyRevoke            MessageType = "key.revoke"
	TypeKeyRevoked           MessageType = "key.revoked"
	TypeKeyRotationPropose   MessageType = "key.rotation.propose"
	TypeKeyRotationChallenge MessageType = "key.rotation.challenge"
	TypeKeyRotationConfirm   MessageType = "key.rotation.confirm"
	TypeKeyRotationConfirmed MessageType = "key.rotation.confirmed"
	TypeKeyRotationFinalize  MessageType = "key.rotation.finalize"
	TypeKeyRotationFinalized MessageType = "key.rotation.finalized"
)

type Direction uint8

const (
	ControllerToRelay Direction = iota + 1
	RelayToController
	Bidirectional
)

var typeDirections = map[MessageType]Direction{
	TypeHello: ControllerToRelay, TypeChallenge: RelayToController, TypeAuthenticate: ControllerToRelay, TypeReady: RelayToController,
	TypeSubscriptionsSync: ControllerToRelay, TypeSubscriptionsSynced: RelayToController, TypeSourceDesired: RelayToController,
	TypeAck: ControllerToRelay, TypeReject: ControllerToRelay, TypeAccessChange: RelayToController, TypeHeartbeat: Bidirectional,
	TypeProtocolError: Bidirectional, TypeBindingRemove: ControllerToRelay, TypeBindingRemoved: RelayToController,
	TypeControllerRevoke: ControllerToRelay, TypeControllerRevoked: RelayToController, TypeKeyRevoke: ControllerToRelay, TypeKeyRevoked: RelayToController,
	TypeKeyRotationPropose: ControllerToRelay, TypeKeyRotationChallenge: RelayToController, TypeKeyRotationConfirm: ControllerToRelay,
	TypeKeyRotationConfirmed: RelayToController, TypeKeyRotationFinalize: ControllerToRelay, TypeKeyRotationFinalized: RelayToController,
}

type ErrorCode string

const (
	CodeEnvelopeTooLarge ErrorCode = "envelope_too_large"
	CodeInvalidJSON      ErrorCode = "invalid_json"
	CodeUnknownType      ErrorCode = "unknown_type"
	CodeInvalidEnvelope  ErrorCode = "invalid_envelope"
)

type Error struct {
	Code  ErrorCode
	Field string
	cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "relay protocol error"
	}
	return fmt.Sprintf("relay protocol error: code=%s field=%s", e.Code, e.Field)
}
func (e *Error) Unwrap() error  { return e.cause }
func (e *Error) String() string { return e.Error() }
func (e *Error) LogValue() slog.Value {
	return slog.GroupValue(slog.String("code", string(e.Code)), slog.String("field", e.Field))
}

type Envelope struct {
	Version   int         `json:"version"`
	Type      MessageType `json:"type"`
	MessageID string      `json:"messageId"`
	SentAt    time.Time   `json:"sentAt"`
}
type Frame interface {
	base() *Envelope
	validate() error
}
type ACKState struct {
	SubscriptionID string `json:"subscriptionId"`
	Generation     uint64 `json:"generation"`
}
type Hello struct {
	Envelope
	ControllerID string     `json:"controllerId"`
	KeyID        string     `json:"keyId"`
	ClientNonce  string     `json:"clientNonce"`
	ACKState     []ACKState `json:"ackState"`
}
type Challenge struct {
	Envelope
	SessionID   string    `json:"sessionId"`
	ServerNonce string    `json:"serverNonce"`
	ACKDigest   string    `json:"ackDigest"`
	ExpiresAt   time.Time `json:"expiresAt"`
}
type Authenticate struct {
	Envelope
	SessionID string `json:"sessionId"`
	Signature string `json:"signature"`
}
type ReconnectPolicy struct {
	InitialDelayMillis uint32 `json:"initialDelayMillis"`
	MaximumDelayMillis uint32 `json:"maximumDelayMillis"`
	Multiplier         uint8  `json:"multiplier"`
	JitterPercent      uint8  `json:"jitterPercent"`
}
type Ready struct {
	Envelope
	SessionID                string          `json:"sessionId"`
	HeartbeatIntervalSeconds uint32          `json:"heartbeatIntervalSeconds"`
	MaxEnvelopeBytes         uint32          `json:"maxEnvelopeBytes"`
	MaxSubscriptions         uint32          `json:"maxSubscriptions"`
	MaxOutstanding           uint32          `json:"maxOutstanding"`
	SessionExpiresAt         time.Time       `json:"sessionExpiresAt"`
	Reconnect                ReconnectPolicy `json:"reconnect"`
}
type Subscription struct {
	SubscriptionID string `json:"subscriptionId"`
	InstallationID int64  `json:"installationId"`
	RepositoryID   int64  `json:"repositoryId"`
	Ref            string `json:"ref"`
}
type SubscriptionsSync struct {
	Envelope
	Generation    uint64         `json:"generation"`
	Subscriptions []Subscription `json:"subscriptions"`
}
type SubscriptionsSynced struct {
	Envelope
	TargetMessageID string `json:"targetMessageId"`
	Generation      uint64 `json:"generation"`
	AcceptedCount   uint32 `json:"acceptedCount"`
}
type SourceDesired struct {
	Envelope
	DeliveryID     string    `json:"deliveryId"`
	SubscriptionID string    `json:"subscriptionId"`
	Generation     uint64    `json:"generation"`
	InstallationID int64     `json:"installationId"`
	RepositoryID   int64     `json:"repositoryId"`
	Ref            string    `json:"ref"`
	ObservedSHA    string    `json:"observedSha"`
	ObservedAt     time.Time `json:"observedAt"`
}
type SourceTarget struct {
	SubscriptionID string `json:"subscriptionId"`
	Generation     uint64 `json:"generation"`
}
type AccessTarget struct {
	EventID string `json:"eventId"`
}
type Ack struct {
	Envelope
	TargetMessageID string        `json:"targetMessageId"`
	Source          *SourceTarget `json:"source,omitempty"`
	Access          *AccessTarget `json:"access,omitempty"`
}
type Reject struct {
	Envelope
	TargetMessageID string        `json:"targetMessageId"`
	Source          *SourceTarget `json:"source,omitempty"`
	Access          *AccessTarget `json:"access,omitempty"`
	Code            string        `json:"code"`
}
type AccessChange struct {
	Envelope
	EventID        string    `json:"eventId"`
	InstallationID int64     `json:"installationId"`
	RepositoryID   int64     `json:"repositoryId,omitempty"`
	ChangeCode     string    `json:"changeCode"`
	ObservedAt     time.Time `json:"observedAt"`
	AckRequired    bool      `json:"ackRequired"`
}
type Heartbeat struct {
	Envelope
	Sequence uint64 `json:"sequence"`
}
type ProtocolError struct {
	Envelope
	TargetMessageID string `json:"targetMessageId,omitempty"`
	Code            string `json:"code"`
	Fatal           bool   `json:"fatal"`
}
type BindingRemove struct {
	Envelope
	InstallationID int64 `json:"installationId"`
	RepositoryID   int64 `json:"repositoryId"`
}
type BindingRemoved struct {
	Envelope
	TargetMessageID string `json:"targetMessageId"`
	InstallationID  int64  `json:"installationId"`
	RepositoryID    int64  `json:"repositoryId"`
}
type ControllerRevoke struct {
	Envelope
	ControllerID string `json:"controllerId"`
}
type ControllerRevoked struct {
	Envelope
	TargetMessageID string `json:"targetMessageId"`
	ControllerID    string `json:"controllerId"`
}
type KeyRevoke struct {
	Envelope
	ControllerID string `json:"controllerId"`
	KeyID        string `json:"keyId"`
}
type KeyRevoked struct {
	Envelope
	TargetMessageID string `json:"targetMessageId"`
	ControllerID    string `json:"controllerId"`
	KeyID           string `json:"keyId"`
}
type KeyRotationPropose struct {
	Envelope
	RotationID   string `json:"rotationId"`
	ControllerID string `json:"controllerId"`
	OldKeyID     string `json:"oldKeyId"`
	NewKeyID     string `json:"newKeyId"`
	NewPublicKey string `json:"newPublicKey"`
}
type KeyRotationChallenge struct {
	Envelope
	TargetMessageID string    `json:"targetMessageId"`
	RotationID      string    `json:"rotationId"`
	ServerNonce     string    `json:"serverNonce"`
	ExpiresAt       time.Time `json:"expiresAt"`
}
type KeyRotationConfirm struct {
	Envelope
	RotationID string `json:"rotationId"`
	Signature  string `json:"signature"`
}
type KeyRotationConfirmed struct {
	Envelope
	TargetMessageID string `json:"targetMessageId"`
	RotationID      string `json:"rotationId"`
}
type KeyRotationFinalize struct {
	Envelope
	RotationID   string `json:"rotationId"`
	RetireOldKey bool   `json:"retireOldKey"`
}
type KeyRotationFinalized struct {
	Envelope
	TargetMessageID string `json:"targetMessageId"`
	RotationID      string `json:"rotationId"`
	RetiredKeyID    string `json:"retiredKeyId"`
}

func NewEnvelope(t MessageType, messageID string, sentAt time.Time) Envelope {
	return Envelope{Version: Version, Type: t, MessageID: messageID, SentAt: sentAt.UTC()}
}
func (m *Hello) base() *Envelope                { return &m.Envelope }
func (m *Challenge) base() *Envelope            { return &m.Envelope }
func (m *Authenticate) base() *Envelope         { return &m.Envelope }
func (m *Ready) base() *Envelope                { return &m.Envelope }
func (m *SubscriptionsSync) base() *Envelope    { return &m.Envelope }
func (m *SubscriptionsSynced) base() *Envelope  { return &m.Envelope }
func (m *SourceDesired) base() *Envelope        { return &m.Envelope }
func (m *Ack) base() *Envelope                  { return &m.Envelope }
func (m *Reject) base() *Envelope               { return &m.Envelope }
func (m *AccessChange) base() *Envelope         { return &m.Envelope }
func (m *Heartbeat) base() *Envelope            { return &m.Envelope }
func (m *ProtocolError) base() *Envelope        { return &m.Envelope }
func (m *BindingRemove) base() *Envelope        { return &m.Envelope }
func (m *BindingRemoved) base() *Envelope       { return &m.Envelope }
func (m *ControllerRevoke) base() *Envelope     { return &m.Envelope }
func (m *ControllerRevoked) base() *Envelope    { return &m.Envelope }
func (m *KeyRevoke) base() *Envelope            { return &m.Envelope }
func (m *KeyRevoked) base() *Envelope           { return &m.Envelope }
func (m *KeyRotationPropose) base() *Envelope   { return &m.Envelope }
func (m *KeyRotationChallenge) base() *Envelope { return &m.Envelope }
func (m *KeyRotationConfirm) base() *Envelope   { return &m.Envelope }
func (m *KeyRotationConfirmed) base() *Envelope { return &m.Envelope }
func (m *KeyRotationFinalize) base() *Envelope  { return &m.Envelope }
func (m *KeyRotationFinalized) base() *Envelope { return &m.Envelope }

func (m *Hello) validate() error {
	return first(validateBase(m.base(), TypeHello), validUUID("controllerId", m.ControllerID), validUUID("keyId", m.KeyID), validB64("clientNonce", m.ClientNonce, NonceBytes), ValidateACKState(m.ACKState))
}
func (m *Challenge) validate() error {
	return first(validateBase(m.base(), TypeChallenge), validUUID("sessionId", m.SessionID), validB64("serverNonce", m.ServerNonce, NonceBytes), validB64("ackDigest", m.ACKDigest, DigestBytes), validTime("expiresAt", m.ExpiresAt))
}
func (m *Authenticate) validate() error {
	return first(validateBase(m.base(), TypeAuthenticate), validUUID("sessionId", m.SessionID), validB64("signature", m.Signature, SignatureBytes))
}
func (m *Ready) validate() error {
	if err := first(validateBase(m.base(), TypeReady), validUUID("sessionId", m.SessionID), validTime("sessionExpiresAt", m.SessionExpiresAt)); err != nil {
		return err
	}
	if m.HeartbeatIntervalSeconds < 5 || m.HeartbeatIntervalSeconds > 300 || m.MaxEnvelopeBytes < 4096 || m.MaxEnvelopeBytes > 8<<20 || m.MaxSubscriptions < 1 || m.MaxSubscriptions > MaxArrayItems || m.MaxOutstanding < 1 || m.MaxOutstanding > 10000 {
		return invalid("ready.limits")
	}
	if m.Reconnect.InitialDelayMillis < 100 || m.Reconnect.MaximumDelayMillis < m.Reconnect.InitialDelayMillis || m.Reconnect.MaximumDelayMillis > 300000 || m.Reconnect.Multiplier < 2 || m.Reconnect.Multiplier > 4 || m.Reconnect.JitterPercent > 50 {
		return invalid("reconnect")
	}
	return nil
}
func (m *SubscriptionsSync) validate() error {
	if err := first(validateBase(m.base(), TypeSubscriptionsSync), positiveGeneration(m.Generation), boundedArray("subscriptions", len(m.Subscriptions))); err != nil {
		return err
	}
	if m.Subscriptions == nil {
		return invalid("subscriptions")
	}
	seen := map[string]struct{}{}
	for _, item := range m.Subscriptions {
		if err := first(validUUID("subscriptions.subscriptionId", item.SubscriptionID), positive("subscriptions.installationId", item.InstallationID), positive("subscriptions.repositoryId", item.RepositoryID), ValidRef(item.Ref)); err != nil {
			return err
		}
		if _, ok := seen[item.SubscriptionID]; ok {
			return invalid("subscriptions.subscriptionId")
		}
		seen[item.SubscriptionID] = struct{}{}
	}
	return nil
}
func (m *SubscriptionsSynced) validate() error {
	if err := first(validateBase(m.base(), TypeSubscriptionsSynced), validUUID("targetMessageId", m.TargetMessageID), positiveGeneration(m.Generation)); err != nil {
		return err
	}
	if m.AcceptedCount > MaxArrayItems {
		return invalid("acceptedCount")
	}
	return nil
}
func (m *SourceDesired) validate() error {
	return first(validateBase(m.base(), TypeSourceDesired), validUUID("deliveryId", m.DeliveryID), validUUID("subscriptionId", m.SubscriptionID), positiveGeneration(m.Generation), positive("installationId", m.InstallationID), positive("repositoryId", m.RepositoryID), ValidRef(m.Ref), ValidSHA(m.ObservedSHA), validTime("observedAt", m.ObservedAt))
}
func (m *Ack) validate() error {
	return first(validateBase(m.base(), TypeAck), validUUID("targetMessageId", m.TargetMessageID), validateTarget(m.Source, m.Access))
}
func (m *Reject) validate() error {
	return first(validateBase(m.base(), TypeReject), validUUID("targetMessageId", m.TargetMessageID), validateTarget(m.Source, m.Access), validCode(m.Code))
}
func (m *AccessChange) validate() error {
	if err := first(validateBase(m.base(), TypeAccessChange), validUUID("eventId", m.EventID), positive("installationId", m.InstallationID), validCode(m.ChangeCode), validTime("observedAt", m.ObservedAt)); err != nil {
		return err
	}
	if m.RepositoryID < 0 || !m.AckRequired {
		return invalid("access-change")
	}
	return nil
}
func (m *Heartbeat) validate() error {
	if err := validateBase(m.base(), TypeHeartbeat); err != nil {
		return err
	}
	if m.Sequence == 0 {
		return invalid("sequence")
	}
	return nil
}
func (m *ProtocolError) validate() error {
	if err := first(validateBase(m.base(), TypeProtocolError), validCode(m.Code)); err != nil {
		return err
	}
	if m.TargetMessageID != "" {
		return validUUID("targetMessageId", m.TargetMessageID)
	}
	return nil
}
func (m *BindingRemove) validate() error {
	return first(validateBase(m.base(), TypeBindingRemove), positive("installationId", m.InstallationID), positive("repositoryId", m.RepositoryID))
}
func (m *BindingRemoved) validate() error {
	return first(validateBase(m.base(), TypeBindingRemoved), validUUID("targetMessageId", m.TargetMessageID), positive("installationId", m.InstallationID), positive("repositoryId", m.RepositoryID))
}
func (m *ControllerRevoke) validate() error {
	return first(validateBase(m.base(), TypeControllerRevoke), validUUID("controllerId", m.ControllerID))
}
func (m *ControllerRevoked) validate() error {
	return first(validateBase(m.base(), TypeControllerRevoked), validUUID("targetMessageId", m.TargetMessageID), validUUID("controllerId", m.ControllerID))
}
func (m *KeyRevoke) validate() error {
	return first(validateBase(m.base(), TypeKeyRevoke), validUUID("controllerId", m.ControllerID), validUUID("keyId", m.KeyID))
}
func (m *KeyRevoked) validate() error {
	return first(validateBase(m.base(), TypeKeyRevoked), validUUID("targetMessageId", m.TargetMessageID), validUUID("controllerId", m.ControllerID), validUUID("keyId", m.KeyID))
}
func (m *KeyRotationPropose) validate() error {
	if m.OldKeyID == m.NewKeyID {
		return invalid("newKeyId")
	}
	return first(validateBase(m.base(), TypeKeyRotationPropose), validUUID("rotationId", m.RotationID), validUUID("controllerId", m.ControllerID), validUUID("oldKeyId", m.OldKeyID), validUUID("newKeyId", m.NewKeyID), validB64("newPublicKey", m.NewPublicKey, PublicKeyBytes))
}
func (m *KeyRotationChallenge) validate() error {
	return first(validateBase(m.base(), TypeKeyRotationChallenge), validUUID("targetMessageId", m.TargetMessageID), validUUID("rotationId", m.RotationID), validB64("serverNonce", m.ServerNonce, NonceBytes), validTime("expiresAt", m.ExpiresAt))
}
func (m *KeyRotationConfirm) validate() error {
	return first(validateBase(m.base(), TypeKeyRotationConfirm), validUUID("rotationId", m.RotationID), validB64("signature", m.Signature, SignatureBytes))
}
func (m *KeyRotationConfirmed) validate() error {
	return first(validateBase(m.base(), TypeKeyRotationConfirmed), validUUID("targetMessageId", m.TargetMessageID), validUUID("rotationId", m.RotationID))
}
func (m *KeyRotationFinalize) validate() error {
	if !m.RetireOldKey {
		return invalid("retireOldKey")
	}
	return first(validateBase(m.base(), TypeKeyRotationFinalize), validUUID("rotationId", m.RotationID))
}
func (m *KeyRotationFinalized) validate() error {
	return first(validateBase(m.base(), TypeKeyRotationFinalized), validUUID("targetMessageId", m.TargetMessageID), validUUID("rotationId", m.RotationID), validUUID("retiredKeyId", m.RetiredKeyID))
}

func Validate(frame Frame) error {
	if frame == nil {
		return invalid("frame")
	}
	value := reflect.ValueOf(frame)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return invalid("frame")
	}
	return frame.validate()
}
func DirectionFor(t MessageType) (Direction, bool) { d, ok := typeDirections[t]; return d, ok }
func RequiresSubprotocol(offered []string) bool {
	for _, candidate := range offered {
		if candidate == Subprotocol {
			return true
		}
	}
	return false
}
func Encode(frame Frame, maximum int) ([]byte, error) {
	maximum = normalizedMaximum(maximum)
	if err := Validate(frame); err != nil {
		return nil, err
	}
	b, err := json.Marshal(frame)
	if err != nil {
		return nil, &Error{Code: CodeInvalidEnvelope, Field: "frame", cause: err}
	}
	if len(b) > maximum {
		return nil, &Error{Code: CodeEnvelopeTooLarge, Field: "frame"}
	}
	return b, nil
}
func Decode(data []byte, maximum int) (Frame, error) {
	maximum = normalizedMaximum(maximum)
	if len(data) == 0 {
		return nil, &Error{Code: CodeInvalidJSON, Field: "frame"}
	}
	if len(data) > maximum {
		return nil, &Error{Code: CodeEnvelopeTooLarge, Field: "frame"}
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	var header struct {
		Version int         `json:"version"`
		Type    MessageType `json:"type"`
	}
	if err := decodeHeader(data, &header); err != nil {
		return nil, err
	}
	frame := frameForType(header.Type)
	if frame == nil {
		return nil, &Error{Code: CodeUnknownType, Field: "type"}
	}
	if err := validateExactJSONFields(data, reflect.TypeOf(frame)); err != nil {
		return nil, err
	}
	if err := decodeOne(data, frame); err != nil {
		return nil, err
	}
	if err := frame.validate(); err != nil {
		return nil, err
	}
	return frame, nil
}
func frameForType(t MessageType) Frame {
	switch t {
	case TypeHello:
		return &Hello{}
	case TypeChallenge:
		return &Challenge{}
	case TypeAuthenticate:
		return &Authenticate{}
	case TypeReady:
		return &Ready{}
	case TypeSubscriptionsSync:
		return &SubscriptionsSync{}
	case TypeSubscriptionsSynced:
		return &SubscriptionsSynced{}
	case TypeSourceDesired:
		return &SourceDesired{}
	case TypeAck:
		return &Ack{}
	case TypeReject:
		return &Reject{}
	case TypeAccessChange:
		return &AccessChange{}
	case TypeHeartbeat:
		return &Heartbeat{}
	case TypeProtocolError:
		return &ProtocolError{}
	case TypeBindingRemove:
		return &BindingRemove{}
	case TypeBindingRemoved:
		return &BindingRemoved{}
	case TypeControllerRevoke:
		return &ControllerRevoke{}
	case TypeControllerRevoked:
		return &ControllerRevoked{}
	case TypeKeyRevoke:
		return &KeyRevoke{}
	case TypeKeyRevoked:
		return &KeyRevoked{}
	case TypeKeyRotationPropose:
		return &KeyRotationPropose{}
	case TypeKeyRotationChallenge:
		return &KeyRotationChallenge{}
	case TypeKeyRotationConfirm:
		return &KeyRotationConfirm{}
	case TypeKeyRotationConfirmed:
		return &KeyRotationConfirmed{}
	case TypeKeyRotationFinalize:
		return &KeyRotationFinalize{}
	case TypeKeyRotationFinalized:
		return &KeyRotationFinalized{}
	default:
		return nil
	}
}

func decodeHeader(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(target); err != nil {
		return &Error{Code: CodeInvalidJSON, Field: "frame", cause: err}
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &Error{Code: CodeInvalidJSON, Field: "frame"}
	}
	return nil
}
func decodeOne(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return &Error{Code: CodeInvalidJSON, Field: "frame", cause: err}
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &Error{Code: CodeInvalidJSON, Field: "frame"}
	}
	return nil
}
func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid object key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		default:
			return errors.New("invalid delimiter")
		}
	}
	if err := walk(); err != nil {
		return &Error{Code: CodeInvalidJSON, Field: "frame", cause: err}
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return &Error{Code: CodeInvalidJSON, Field: "frame"}
	}
	return nil
}
func validateExactJSONFields(data []byte, target reflect.Type) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return &Error{Code: CodeInvalidJSON, Field: "frame", cause: err}
	}
	return exactJSONValue(value, target)
}
func exactJSONValue(value any, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == reflect.TypeFor[time.Time]() {
		if _, ok := value.(string); !ok {
			return invalid("frame")
		}
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return invalid("frame")
		}
		fields := map[string]jsonField{}
		collectJSONFields(target, fields)
		for name, child := range object {
			field, exists := fields[name]
			if !exists {
				return &Error{Code: CodeInvalidJSON, Field: name}
			}
			if err := exactJSONValue(child, field.typ); err != nil {
				return err
			}
		}
		for name, field := range fields {
			if field.required {
				if _, exists := object[name]; !exists {
					return &Error{Code: CodeInvalidJSON, Field: name}
				}
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return invalid("frame")
		}
		for _, child := range array {
			if err := exactJSONValue(child, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

type jsonField struct {
	typ      reflect.Type
	required bool
}

func collectJSONFields(target reflect.Type, fields map[string]jsonField) {
	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		if !field.IsExported() {
			continue
		}
		parts := strings.Split(field.Tag.Get("json"), ",")
		name := parts[0]
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			collectJSONFields(field.Type, fields)
			continue
		}
		if name == "" {
			name = field.Name
		}
		required := true
		for _, option := range parts[1:] {
			if option == "omitempty" {
				required = false
			}
		}
		fields[name] = jsonField{typ: field.Type, required: required}
	}
}

func validateBase(base *Envelope, expected MessageType) error {
	if base == nil || base.Version != Version {
		return invalid("version")
	}
	if base.Type != expected {
		return invalid("type")
	}
	return first(validUUID("messageId", base.MessageID), validTime("sentAt", base.SentAt))
}
func validateTarget(source *SourceTarget, access *AccessTarget) error {
	if (source == nil) == (access == nil) {
		return invalid("target")
	}
	if source != nil {
		return first(validUUID("source.subscriptionId", source.SubscriptionID), positiveGeneration(source.Generation))
	}
	return validUUID("access.eventId", access.EventID)
}
func validUUID(field, value string) error {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return invalid(field)
	}
	return nil
}
func validB64(field, value string, size int) error {
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) != size || base64.RawURLEncoding.EncodeToString(b) != value {
		return invalid(field)
	}
	return nil
}
func validTime(field string, value time.Time) error {
	if value.IsZero() {
		return invalid(field)
	}
	return nil
}
func positive(field string, value int64) error {
	if value <= 0 {
		return invalid(field)
	}
	return nil
}
func positiveGeneration(value uint64) error {
	if value == 0 {
		return invalid("generation")
	}
	return nil
}
func boundedArray(field string, n int) error {
	if n > MaxArrayItems {
		return invalid(field)
	}
	return nil
}
func validCode(value string) error {
	if !validToken(value, 128) {
		return invalid("code")
	}
	return nil
}
func validToken(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
func invalid(field string) error { return &Error{Code: CodeInvalidEnvelope, Field: field} }
func first(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
func normalizedMaximum(maximum int) int {
	if maximum <= 0 || maximum > 8<<20 {
		return DefaultMaxEnvelopeBytes
	}
	return maximum
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ValidSHA(value string) error {
	if !shaPattern.MatchString(value) {
		return invalid("observedSha")
	}
	return nil
}
func ValidRef(value string) error {
	const prefix = "refs/heads/"
	if !strings.HasPrefix(value, prefix) || len(value) <= len(prefix) || len(value) > 255 {
		return invalid("ref")
	}
	tail := strings.TrimPrefix(value, prefix)
	if strings.HasPrefix(tail, "/") || strings.HasSuffix(tail, "/") || strings.HasSuffix(tail, ".") || strings.Contains(tail, "//") || strings.Contains(tail, "..") || strings.Contains(tail, "@{") || tail == "@" {
		return invalid("ref")
	}
	for _, part := range strings.Split(tail, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return invalid("ref")
		}
	}
	for _, r := range tail {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return invalid("ref")
		}
	}
	return nil
}
