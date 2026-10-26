package generatedingress

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/hostd/hostd/internal/generatedcapacity"
)

var errGeneratedDomainCapacityDebtState = errors.New("generated domain capacity debt state is invalid")

type generatedDomainCapacityPhase uint8

const (
	generatedDomainCapacityBootstrapOpen generatedDomainCapacityPhase = iota + 1
	generatedDomainCapacitySealedPending
	generatedDomainCapacityPublished
	generatedDomainCapacityAborting
	generatedDomainCapacityClosed
)

// generatedDomainCapacityCoordinator is the private capacity source and
// construction lifetime shared by one generated domain. Durable evidence never
// crosses this boundary as authority: only tokens minted by an all-or-none
// bootstrap batch can alter the process-local debt.
type generatedDomainCapacityCoordinator struct {
	manager *Manager

	mu      sync.Mutex
	changed *sync.Cond
	phase   generatedDomainCapacityPhase
	borrows int
	epoch   uint64
	memory  uint64
	disk    uint64
	tokens  map[*generatedDomainCapacityDebtToken]struct{}
}

type generatedDomainCapacityDebtToken struct {
	owner  *generatedDomainCapacityCoordinator
	epoch  uint64
	limits generatedcapacity.Limits
	active bool
}

type generatedDomainCapacityBorrow struct {
	owner *generatedDomainCapacityCoordinator
	once  sync.Once
}

func newGeneratedDomainCapacityCoordinator(manager *Manager) (*generatedDomainCapacityCoordinator, error) {
	if manager == nil {
		return nil, errGeneratedDomainCapacityDebtState
	}
	result := &generatedDomainCapacityCoordinator{manager: manager,
		phase:  generatedDomainCapacityBootstrapOpen,
		tokens: make(map[*generatedDomainCapacityDebtToken]struct{})}
	result.changed = sync.NewCond(&result.mu)
	return result, nil
}

func (c *generatedDomainCapacityCoordinator) Snapshot(ctx context.Context) (generatedcapacity.Snapshot, error) {
	if c == nil || c.manager == nil || ctx == nil {
		return generatedcapacity.Snapshot{}, errGeneratedDomainCapacityDebtState
	}
	// Preserve the established Budget lock -> physical Source ordering. No debt
	// or lifetime lock spans the Manager observation.
	physical, err := c.manager.Snapshot(ctx)
	if err != nil {
		return generatedcapacity.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != generatedDomainCapacitySealedPending && c.phase != generatedDomainCapacityPublished {
		return generatedcapacity.Snapshot{}, errGeneratedDomainCapacityDebtState
	}
	if physical.MemoryAvailableBytes > c.memory {
		physical.MemoryAvailableBytes -= c.memory
	} else {
		physical.MemoryAvailableBytes = 0
	}
	if physical.DiskAvailableBytes > c.disk {
		physical.DiskAvailableBytes -= c.disk
	} else {
		physical.DiskAvailableBytes = 0
	}
	return physical, nil
}

func (c *generatedDomainCapacityCoordinator) installBootstrapBatch(limits []generatedcapacity.Limits) (
	[]*generatedDomainCapacityDebtToken, error,
) {
	if c == nil || len(limits) == 0 {
		return nil, errGeneratedDomainCapacityDebtState
	}
	var memory, disk uint64
	for _, value := range limits {
		if value.MemoryBytes == 0 || value.DiskBytes == 0 ||
			memory > math.MaxUint64-value.MemoryBytes || disk > math.MaxUint64-value.DiskBytes {
			return nil, errGeneratedDomainCapacityDebtState
		}
		memory += value.MemoryBytes
		disk += value.DiskBytes
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != generatedDomainCapacityBootstrapOpen || c.memory != 0 || c.disk != 0 || len(c.tokens) != 0 ||
		uint64(len(limits)) > math.MaxUint64-c.epoch {
		return nil, errGeneratedDomainCapacityDebtState
	}
	result := make([]*generatedDomainCapacityDebtToken, len(limits))
	for index, value := range limits {
		c.epoch++
		token := &generatedDomainCapacityDebtToken{owner: c, epoch: c.epoch, limits: value, active: true}
		c.tokens[token] = struct{}{}
		result[index] = token
	}
	c.memory, c.disk = memory, disk
	return result, nil
}

func (c *generatedDomainCapacityCoordinator) sealBootstrap() error {
	if c == nil {
		return errGeneratedDomainCapacityDebtState
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != generatedDomainCapacityBootstrapOpen {
		return errGeneratedDomainCapacityDebtState
	}
	c.phase = generatedDomainCapacitySealedPending
	return nil
}

func (c *generatedDomainCapacityCoordinator) borrowAcquire() (*generatedDomainCapacityBorrow, error) {
	if c == nil {
		return nil, errGeneratedDomainCapacityDebtState
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != generatedDomainCapacitySealedPending && c.phase != generatedDomainCapacityPublished {
		return nil, errGeneratedDomainCapacityDebtState
	}
	c.borrows++
	return &generatedDomainCapacityBorrow{owner: c}, nil
}

func (b *generatedDomainCapacityBorrow) release() {
	if b == nil || b.owner == nil {
		return
	}
	b.once.Do(func() {
		b.owner.mu.Lock()
		if b.owner.borrows > 0 {
			b.owner.borrows--
		}
		b.owner.changed.Broadcast()
		b.owner.mu.Unlock()
	})
}

func (c *generatedDomainCapacityCoordinator) publish() error {
	if c == nil {
		return errGeneratedDomainCapacityDebtState
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != generatedDomainCapacitySealedPending || c.borrows != 0 {
		return errGeneratedDomainCapacityDebtState
	}
	c.phase = generatedDomainCapacityPublished
	return nil
}

func (c *generatedDomainCapacityCoordinator) readyToPublish() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase == generatedDomainCapacitySealedPending && c.borrows == 0
}

func (c *generatedDomainCapacityCoordinator) abortUnpublished(releaseRaw func()) error {
	if c == nil {
		return errGeneratedDomainCapacityDebtState
	}
	c.mu.Lock()
	if c.phase == generatedDomainCapacityPublished {
		c.mu.Unlock()
		return errGeneratedDomainCapacityDebtState
	}
	if c.phase == generatedDomainCapacityClosed {
		c.mu.Unlock()
		return nil
	}
	if c.phase != generatedDomainCapacityBootstrapOpen && c.phase != generatedDomainCapacitySealedPending {
		c.mu.Unlock()
		return errGeneratedDomainCapacityDebtState
	}
	c.phase = generatedDomainCapacityAborting
	for c.borrows != 0 {
		c.changed.Wait()
	}
	c.mu.Unlock()

	// Detach raw candidate charges without holding the coordinator. The ledger
	// implementation releases the resulting opaque leases after its own locks.
	if releaseRaw != nil {
		releaseRaw()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for token := range c.tokens {
		token.active = false
	}
	clear(c.tokens)
	c.memory, c.disk = 0, 0
	c.phase = generatedDomainCapacityClosed
	c.changed.Broadcast()
	return nil
}

func (t *generatedDomainCapacityDebtToken) configured() bool {
	if t == nil || t.owner == nil || t.epoch == 0 {
		return false
	}
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	_, found := t.owner.tokens[t]
	return found && t.active && t.owner.phase != generatedDomainCapacityClosed
}

func (t *generatedDomainCapacityDebtToken) published() bool {
	if !t.configured() {
		return false
	}
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	return t.active && t.owner.phase == generatedDomainCapacityPublished
}

func (t *generatedDomainCapacityDebtToken) release() {
	if t == nil || t.owner == nil {
		return
	}
	c := t.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != generatedDomainCapacityPublished || !t.active {
		return
	}
	if _, found := c.tokens[t]; !found || c.memory < t.limits.MemoryBytes || c.disk < t.limits.DiskBytes {
		return
	}
	t.active = false
	delete(c.tokens, t)
	c.memory -= t.limits.MemoryBytes
	c.disk -= t.limits.DiskBytes
}
