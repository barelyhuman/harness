package stream

import (
	"context"
	"sync"
)

// Credit is a byte-window flow controller. Acquire blocks until n bytes of
// window are available; Release (driven by client acks) returns credit.
type Credit struct {
	mu     sync.Mutex
	avail  int
	closed bool
	wake   chan struct{}
}

func NewCredit(window int) *Credit {
	return &Credit{avail: window, wake: make(chan struct{})}
}

// Acquire reserves n bytes. A request larger than the whole window is allowed
// to proceed once the window is completely free (avoids deadlock).
func (c *Credit) Acquire(ctx context.Context, n int, window int) error {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return ErrClosed
		}
		need := n
		if need > window {
			need = window
		}
		if c.avail >= need {
			c.avail -= n
			c.mu.Unlock()
			return nil
		}
		w := c.wake
		c.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Release returns n bytes of credit.
func (c *Credit) Release(n int) {
	c.mu.Lock()
	c.avail += n
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
}

// Available reports current credit (may be negative after an oversize send).
func (c *Credit) Available() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.avail
}

// Close wakes all waiters with ErrClosed.
func (c *Credit) Close() {
	c.mu.Lock()
	c.closed = true
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
}
