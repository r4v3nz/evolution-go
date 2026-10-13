// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): this file is
// not part of upstream Evolution Go.
//
// Upstream leaves a client whose connection failed in memory, "running" but
// never connected: /instance/connect answers "already running" and
// /instance/qr "no QR code available yet" forever. These helpers let those
// endpoints tell a client that is still starting from one that is stuck, and
// restart the stuck one (at most every restartInterval per instance).

package whatsmeow_service

import (
	"sync"
	"time"
)

var clientStarting sync.Map // instance id -> struct{}: StartClient in progress

// ClientStarting reports whether StartClient is still setting up the
// instance (version check, proxy, websocket dial).
func ClientStarting(instanceId string) bool {
	_, ok := clientStarting.Load(instanceId)
	return ok
}

const restartInterval = 30 * time.Second

var lastRestart = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// AllowRestart reports whether a stuck client may be restarted now, and
// records it: repeated calls (e.g. QR polling) restart at most once per
// restartInterval.
func AllowRestart(instanceId string, now time.Time) bool {
	lastRestart.Lock()
	defer lastRestart.Unlock()
	if t, ok := lastRestart.at[instanceId]; ok && now.Sub(t) < restartInterval {
		return false
	}
	lastRestart.at[instanceId] = now
	return true
}
