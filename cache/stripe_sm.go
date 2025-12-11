package cache

import (
	"context"
	"sync"
	"time"

	"github.com/Monsterwi/razor/logger"
)

// StripeSM manages the background tasks for a Stripe, such as flushing the Aggregate Write Buffer.
// Corresponds to StripeSM in ATS.
type StripeSM struct {
	Stripe *Stripe

	// Sync intervals (align with ATS behavior: frequent flush, periodic metadata sync)
	FlushInterval time.Duration
	SaveInterval  time.Duration // Interval for periodic Save

	// Control
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Trigger channels for immediate requests
	flushCh chan struct{}
	saveCh  chan struct{}
}

func NewStripeSM(stripe *Stripe) *StripeSM {
	ctx, cancel := context.WithCancel(context.Background())
	return &StripeSM{
		Stripe:        stripe,
		FlushInterval: 1 * time.Second,  // Frequent flush like ATS default events
		SaveInterval:  60 * time.Second, // Periodic metadata sync
		ctx:           ctx,
		cancel:        cancel,
		flushCh:       make(chan struct{}, 1),
		saveCh:        make(chan struct{}, 1),
	}
}

// Start begins the background maintenance loop.
func (sm *StripeSM) Start() {
	sm.wg.Add(1)
	go sm.mainLoop()
}

// Stop signals the background loop to exit and waits for it to finish.
func (sm *StripeSM) Stop() {
	sm.cancel()
	sm.wg.Wait()
}

// TriggerFlush requests an immediate flush of the aggregation buffer.
func (sm *StripeSM) TriggerFlush() {
	select {
	case sm.flushCh <- struct{}{}:
	default:
		// Already triggered
	}
}

// TriggerSave requests an immediate save of the stripe metadata.
func (sm *StripeSM) TriggerSave() {
	select {
	case sm.saveCh <- struct{}{}:
	default:
		// Already triggered
	}
}

func (sm *StripeSM) mainLoop() {
	defer sm.wg.Done()

	flushTicker := time.NewTicker(sm.FlushInterval)
	defer flushTicker.Stop()

	saveTicker := time.NewTicker(sm.SaveInterval)
	defer saveTicker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			// Context cancelled, exit loop
			// Perform final flush and save before exiting
			sm.flushAggBuffer()
			sm.saveStripe()
			return
		case <-flushTicker.C:
			// Periodic flush
			sm.flushAggBuffer()
		case <-sm.flushCh:
			// Triggered flush
			sm.flushAggBuffer()
		case <-saveTicker.C:
			// Periodic save
			sm.saveStripe()
		case <-sm.saveCh:
			// Triggered save
			sm.saveStripe()
		}
	}
}

func (sm *StripeSM) flushAggBuffer() {
	// We need to lock the stripe to perform flush?
	// Stripe.FlushAggBuffer internally locks Stripe.mu.
	// However, we might want to check if there is anything to flush first to avoid lock contention?
	// Stripe.FlushAggBuffer checks IsEmpty() inside the lock.
	// We can check IsEmpty() outside lock slightly racily, but it's safe as a hint.

	// Optimization: Check if buffer is empty (lock-free read if possible, or minimal lock)
	// AggBuffer operations are not thread-safe without Stripe lock usually.
	// But Stripe.FlushAggBuffer handles the locking.

	if err := sm.Stripe.FlushAggBuffer(); err != nil {
		// Log error
		logger.Errorf("StripeSM: flush failed: %v", err)
	}
}

func (sm *StripeSM) saveStripe() {
	// Save stripe metadata (Header + Directory) to disk
	// If the stripe is already closed (Fd == nil), skip silently to avoid noisy errors during shutdown.
	if sm.Stripe == nil || sm.Stripe.Fd == nil {
		logger.Debugf("StripeSM: skip save (stripe not open)")
		return
	}

	if err := sm.Stripe.Save(); err != nil {
		logger.Errorf("StripeSM: save failed: %v", err)
	} else {
		logger.Debugf("StripeSM: periodic save completed")
	}
}
