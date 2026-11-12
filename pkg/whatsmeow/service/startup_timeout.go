package whatsmeow_service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	sessionLoadTimeout     = 20 * time.Second
	connectionStartTimeout = 30 * time.Second
)

// Bound connection setup without giving the established websocket a deadline.
// ConnectContext also owns the read loop, so callers keep cancel until shutdown.
func connectWithStartupTimeout(parent context.Context, timeout time.Duration, connect func(context.Context) error) (context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.AfterFunc(timeout, cancel)
	err := connect(ctx)
	if !timer.Stop() {
		err = fmt.Errorf("connection startup exceeded %s: %w", timeout, errors.Join(context.DeadlineExceeded, err))
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
	}
	return cancel, err
}
