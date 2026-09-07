package sub

import (
	"context"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// heartbeatSender sends heartbeat to leader.
type heartbeatSender struct {
	agentID      string
	interval     time.Duration
	stopCh       chan struct{}
	stopOnce     sync.Once
	doneCh       chan struct{} // Done channel to signal goroutine exit
	heartbeatMon *ahp.HeartbeatMonitor
}

// NewHeartbeatSender creates a new HeartbeatSender.
func NewHeartbeatSender(agentID string, interval time.Duration, hbMon *ahp.HeartbeatMonitor) *heartbeatSender {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if hbMon == nil {
		log.Warn("NewHeartbeatSender: nil heartbeat monitor")
	}
	return &heartbeatSender{
		agentID:      agentID,
		interval:     interval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		heartbeatMon: hbMon,
	}
}

// Start starts sending heartbeats.
//
// NOTE: This method blocks until context is cancelled or Stop() is called.
// Callers should run this in a goroutine and use Done() to wait for exit.
func (s *heartbeatSender) Start(ctx context.Context) {
	defer close(s.doneCh)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			if s.heartbeatMon != nil {
				s.heartbeatMon.RecordHeartbeat(s.agentID)
			}
		}
	}
}

// Stop stops sending heartbeats.
// This method is idempotent and safe to call multiple times.
func (s *heartbeatSender) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// Done returns a channel that is closed when the heartbeat goroutine exits.
// This allows callers to wait for graceful shutdown.
func (s *heartbeatSender) Done() <-chan struct{} {
	return s.doneCh
}
