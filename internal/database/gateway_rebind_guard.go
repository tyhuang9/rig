package database

import (
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"sync"

	"modernc.org/sqlite"
)

const gatewayRebindGuardFunction = "rig_gateway_rebind_consume_v1"

// GatewayRebindTransitionGuard is the exact SQL command tuple authorized by
// one process-local capability. CanonicalPayload is compared as well as every
// stored command scalar; CommandDigest alone is never treated as authority.
type GatewayRebindTransitionGuard struct {
	OperationID            string
	Sequence               int64
	PreviousState          string
	PreviousSequence       int64
	NextState              string
	Purpose                string
	ProtectedRecordDigest  string
	TerminalReceiptDigest  string
	LocalAttestationDigest string
	TerminalDisposition    string
	CommandDigest          string
	CanonicalPayload       string
}

var gatewayRebindCapabilities = struct {
	sync.Mutex
	values map[string][]driver.Value
}{values: make(map[string][]driver.Value)}

func init() {
	if err := sqlite.RegisterFunction(gatewayRebindGuardFunction, &sqlite.FunctionImpl{
		NArgs:         13,
		Deterministic: false,
		Scalar:        consumeGatewayRebindCapability,
	}); err != nil {
		panic("register gateway rebind SQL guard: " + err.Error())
	}
}

// ArmGatewayRebindTransitionGuard mints one 256-bit capability. Callers must
// arm it only after BEGIN IMMEDIATE on the pinned connection. The returned
// revoker is idempotent and must be deferred; a consumed capability stays dead
// even when the SQL statement or surrounding transaction rolls back.
func ArmGatewayRebindTransitionGuard(value GatewayRebindTransitionGuard) (string, func(), error) {
	if value.OperationID == "" || value.Sequence <= 1 || value.PreviousState == "" ||
		value.PreviousSequence <= 0 || value.NextState == "" || value.Purpose == "" ||
		value.ProtectedRecordDigest == "" || value.TerminalDisposition == "" ||
		value.CommandDigest == "" || value.CanonicalPayload == "" {
		return "", nil, errors.New("invalid gateway rebind transition guard")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	nonce := hex.EncodeToString(raw)
	expected := []driver.Value{
		value.OperationID, value.Sequence, value.PreviousState, value.PreviousSequence,
		value.NextState, value.Purpose, value.ProtectedRecordDigest,
		value.TerminalReceiptDigest, value.LocalAttestationDigest,
		value.TerminalDisposition, value.CommandDigest, value.CanonicalPayload,
	}
	gatewayRebindCapabilities.Lock()
	if _, collision := gatewayRebindCapabilities.values[nonce]; collision {
		gatewayRebindCapabilities.Unlock()
		return "", nil, errors.New("gateway rebind transition capability collision")
	}
	gatewayRebindCapabilities.values[nonce] = expected
	gatewayRebindCapabilities.Unlock()
	revoke := func() {
		gatewayRebindCapabilities.Lock()
		delete(gatewayRebindCapabilities.values, nonce)
		gatewayRebindCapabilities.Unlock()
	}
	return nonce, revoke, nil
}

func consumeGatewayRebindCapability(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 13 {
		return int64(0), nil
	}
	nonce, ok := args[0].(string)
	if !ok || nonce == "" {
		return int64(0), nil
	}
	gatewayRebindCapabilities.Lock()
	expected, exists := gatewayRebindCapabilities.values[nonce]
	if exists {
		delete(gatewayRebindCapabilities.values, nonce)
	}
	gatewayRebindCapabilities.Unlock()
	if !exists || len(expected) != len(args)-1 {
		return int64(0), nil
	}
	for index := range expected {
		if !sameGatewayRebindGuardValue(expected[index], args[index+1]) {
			return int64(0), nil
		}
	}
	return int64(1), nil
}

func sameGatewayRebindGuardValue(left, right driver.Value) bool {
	switch value := left.(type) {
	case string:
		other, ok := right.(string)
		return ok && value == other
	case int64:
		other, ok := right.(int64)
		return ok && value == other
	default:
		return false
	}
}
