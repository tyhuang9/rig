// Package wss implements the authenticated, bounded version-one WebSocket
// session used by controllers to receive durable relay desired state.
package wss

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

type StateStore interface {
	CreateChallenge(context.Context, store.ChallengeInput) error
	LoadChallengeForAuthentication(context.Context, string) (store.AuthenticationChallenge, error)
	ConsumeChallenge(context.Context, string, time.Time) error
	AcquireLease(context.Context, string, time.Duration) (store.Lease, error)
	RenewLease(context.Context, store.Lease, time.Duration) (store.Lease, error)
	ReleaseLease(context.Context, store.Lease) error
	ApplySubscriptionsSync(context.Context, store.Lease, store.SessionCommand, uint64, []store.Subscription) (store.SessionCommandResult, error)
	ApplyDecisionProtocolError(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error)
	ApplySourceDecision(context.Context, store.Lease, store.SessionCommand, string, uint64, string, bool, string) (store.SessionCommandResult, error)
	ApplyAccessDecision(context.Context, store.Lease, store.SessionCommand, string, string, bool, string) (store.SessionCommandResult, error)
	ApplyBindingRemoval(context.Context, store.Lease, store.SessionCommand, int64, int64) (store.SessionCommandResult, error)
	ApplyKeyRevocation(context.Context, store.Lease, store.SessionCommand, string, string) (store.SessionCommandResult, error)
	ApplyControllerRevocation(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error)
	ApplyRotationProposal(context.Context, store.Lease, store.SessionCommand, store.RotationInput, time.Duration) (store.SessionCommandResult, error)
	ApplyRotationConfirmation(context.Context, store.Lease, store.SessionCommand, string, string) (store.SessionCommandResult, error)
	ApplyRotationFinalization(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error)
	PendingDesired(context.Context, store.Lease, int) ([]store.DesiredState, error)
	PendingAccess(context.Context, store.Lease, int) ([]store.PendingAccess, error)
}

type Config struct {
	HandshakeTimeout    time.Duration
	StoreTimeout        time.Duration
	WriteTimeout        time.Duration
	CloseTimeout        time.Duration
	ChallengeLifetime   time.Duration
	SessionLifetime     time.Duration
	LeaseDuration       time.Duration
	LeaseRenewInterval  time.Duration
	HeartbeatInterval   time.Duration
	IdleTimeout         time.Duration
	PollInterval        time.Duration
	InboundWindow       time.Duration
	HandshakeMaxBytes   int
	MaxEnvelopeBytes    int
	MaxSubscriptions    int
	MaxOutstanding      int
	OutboundQueue       int
	MaxConnections      int
	MaxInboundPerWindow int
}

const (
	maxHandshakeTimeout = time.Minute
	maxStoreTimeout     = 30 * time.Second
	maxWriteTimeout     = 30 * time.Second
	maxCloseTimeout     = 30 * time.Second
)

func DefaultConfig() Config {
	return Config{
		HandshakeTimeout:    10 * time.Second,
		StoreTimeout:        5 * time.Second,
		WriteTimeout:        5 * time.Second,
		CloseTimeout:        2 * time.Second,
		ChallengeLifetime:   time.Minute,
		SessionLifetime:     time.Hour,
		LeaseDuration:       30 * time.Second,
		LeaseRenewInterval:  10 * time.Second,
		HeartbeatInterval:   15 * time.Second,
		IdleTimeout:         45 * time.Second,
		PollInterval:        2 * time.Second,
		InboundWindow:       time.Second,
		HandshakeMaxBytes:   256 << 10,
		MaxEnvelopeBytes:    protocol.DefaultMaxEnvelopeBytes,
		MaxSubscriptions:    protocol.MaxArrayItems,
		MaxOutstanding:      64,
		OutboundQueue:       64,
		MaxConnections:      256,
		MaxInboundPerWindow: 64,
	}
}

type socket interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
	CloseNow() error
	SetReadLimit(int64)
	Subprotocol() string
}

type acceptSocket func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error)

type Handler struct {
	store      StateStore
	config     Config
	now        func() time.Time
	entropy    io.Reader
	logger     *slog.Logger
	accept     acceptSocket
	lifecycle  context.Context
	timers     TimerSource
	admissions chan struct{}
}

type Options struct {
	Now       func() time.Time
	Entropy   io.Reader
	Logger    *slog.Logger
	Lifecycle context.Context
	Timers    TimerSource
}

func NewHandler(state StateStore, config Config, options Options) (*Handler, error) {
	if state == nil {
		return nil, errors.New("relay WSS store is required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Entropy == nil {
		options.Entropy = rand.Reader
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.Lifecycle == nil {
		options.Lifecycle = context.Background()
	}
	if options.Timers == nil {
		options.Timers = realTimerSource{}
	}
	return &Handler{
		store: state, config: config, now: options.Now, entropy: options.Entropy, logger: options.Logger,
		lifecycle:  options.Lifecycle,
		timers:     options.Timers,
		admissions: make(chan struct{}, config.MaxConnections),
		accept: func(w http.ResponseWriter, r *http.Request, options *websocket.AcceptOptions) (socket, error) {
			return websocket.Accept(w, r, options)
		},
	}, nil
}

type Timer interface {
	Chan() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

type Ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type TimerSource interface {
	NewTimer(time.Duration) Timer
	NewTicker(time.Duration) Ticker
}

type realTimerSource struct{}
type realTimer struct{ timer *time.Timer }
type realTicker struct{ ticker *time.Ticker }

func (realTimerSource) NewTimer(duration time.Duration) Timer {
	return &realTimer{timer: time.NewTimer(duration)}
}
func (realTimerSource) NewTicker(duration time.Duration) Ticker {
	return &realTicker{ticker: time.NewTicker(duration)}
}
func (t *realTimer) Chan() <-chan time.Time            { return t.timer.C }
func (t *realTimer) Stop() bool                        { return t.timer.Stop() }
func (t *realTimer) Reset(duration time.Duration) bool { return t.timer.Reset(duration) }
func (t *realTicker) Chan() <-chan time.Time           { return t.ticker.C }
func (t *realTicker) Stop()                            { t.ticker.Stop() }

func validateConfig(c Config) error {
	if c.HandshakeTimeout <= 0 || c.HandshakeTimeout > maxHandshakeTimeout || c.StoreTimeout <= 0 || c.StoreTimeout > maxStoreTimeout || c.WriteTimeout <= 0 || c.WriteTimeout > maxWriteTimeout || c.CloseTimeout <= 0 || c.CloseTimeout > maxCloseTimeout || c.ChallengeLifetime <= 0 || c.ChallengeLifetime > 5*time.Minute || c.HandshakeTimeout > c.ChallengeLifetime || c.SessionLifetime <= 0 || c.SessionLifetime > 24*time.Hour || c.LeaseDuration < time.Second || c.LeaseDuration > 10*time.Minute || c.LeaseRenewInterval <= 0 || c.LeaseRenewInterval > c.LeaseDuration/2 || c.StoreTimeout > c.LeaseRenewInterval/2 || c.StoreTimeout+c.WriteTimeout+c.LeaseRenewInterval >= c.LeaseDuration || c.HandshakeTimeout+c.StoreTimeout+2*c.WriteTimeout >= c.LeaseDuration || c.HeartbeatInterval < 5*time.Second || c.HeartbeatInterval > 5*time.Minute || c.HeartbeatInterval%time.Second != 0 || c.IdleTimeout <= c.HeartbeatInterval || c.PollInterval <= 0 || c.InboundWindow < 100*time.Millisecond || c.InboundWindow > time.Minute || c.HandshakeMaxBytes < 4096 || c.HandshakeMaxBytes > 256<<10 || c.MaxEnvelopeBytes < c.HandshakeMaxBytes || c.MaxEnvelopeBytes > protocol.DefaultMaxEnvelopeBytes || c.MaxSubscriptions < 1 || c.MaxSubscriptions > protocol.MaxArrayItems || c.MaxOutstanding < 1 || c.MaxOutstanding > protocol.MaxArrayItems || c.OutboundQueue < 1 || c.OutboundQueue > c.MaxOutstanding || c.MaxConnections < 1 || c.MaxConnections > 10000 || c.MaxInboundPerWindow < 1 || c.MaxInboundPerWindow > 1000 {
		return errors.New("invalid relay WSS configuration")
	}
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if !offeredSubprotocol(r.Header.Values("Sec-WebSocket-Protocol"), protocol.Subprotocol) {
		w.Header().Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
		http.Error(w, "required websocket subprotocol not offered", http.StatusBadRequest)
		return
	}
	select {
	case h.admissions <- struct{}{}:
		defer func() { <-h.admissions }()
	default:
		http.Error(w, "relay websocket capacity reached", http.StatusServiceUnavailable)
		return
	}
	conn, err := h.accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		h.logger.Warn("relay WSS admission failed", "code", "upgrade_failed")
		return
	}
	conn.SetReadLimit(int64(h.config.HandshakeMaxBytes))
	if conn.Subprotocol() != protocol.Subprotocol {
		_ = conn.CloseNow()
		return
	}
	s := newSession(h, conn)
	s.run(h.lifecycle)
}

func offeredSubprotocol(values []string, required string) bool {
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			if strings.TrimSpace(candidate) == required {
				return true
			}
		}
	}
	return false
}
