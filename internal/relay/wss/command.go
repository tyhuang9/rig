package wss

import (
	"crypto/sha256"
	"errors"

	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

// canonicalSessionCommand is the only constructor used for durable controller
// commands. It hashes the exact strict-decoder frame after canonical protocol
// encoding, so every security-relevant field is bound to replay identity.
func canonicalSessionCommand(frame protocol.Frame, maximum int) (store.SessionCommand, error) {
	encoded, err := protocol.Encode(frame, maximum)
	if err != nil {
		return store.SessionCommand{}, err
	}
	var messageID string
	var commandType store.SessionCommandType
	switch value := frame.(type) {
	case *protocol.SubscriptionsSync:
		messageID, commandType = value.MessageID, store.CommandSubscriptionsSync
	case *protocol.Ack:
		messageID = value.MessageID
		if value.Source != nil {
			commandType = store.CommandAckSource
		} else {
			commandType = store.CommandAckAccess
		}
	case *protocol.Reject:
		messageID = value.MessageID
		if value.Source != nil {
			commandType = store.CommandRejectSource
		} else {
			commandType = store.CommandRejectAccess
		}
	case *protocol.BindingRemove:
		messageID, commandType = value.MessageID, store.CommandBindingRemove
	case *protocol.ControllerRevoke:
		messageID, commandType = value.MessageID, store.CommandControllerRevoke
	case *protocol.KeyRevoke:
		messageID, commandType = value.MessageID, store.CommandKeyRevoke
	case *protocol.KeyRotationPropose:
		messageID, commandType = value.MessageID, store.CommandRotationPropose
	case *protocol.KeyRotationConfirm:
		messageID, commandType = value.MessageID, store.CommandRotationConfirm
	case *protocol.KeyRotationFinalize:
		messageID, commandType = value.MessageID, store.CommandRotationFinalize
	default:
		return store.SessionCommand{}, errors.New("unsupported durable relay command")
	}
	return store.SessionCommand{MessageID: messageID, Type: commandType, Digest: sha256.Sum256(encoded)}, nil
}
