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

// clientReconnecting suppresses duplicate reconnect work when a websocket
// close and a proxy retry report the same outage at nearly the same time.
var clientReconnecting sync.Map // instance id -> struct{}

// remoteLoggedOut records an authoritative LoggedOut event until an operator
// explicitly starts the instance again (usually to scan a new QR code).
var remoteLoggedOut sync.Map // instance id -> struct{}

// clientStopping marks an intentional local stop so its resulting
// events.Disconnected is not mistaken for a transport failure to reconnect.
var clientStopping sync.Map // instance id -> struct{}

func beginClientStart(instanceId string) bool {
	_, loaded := clientStarting.LoadOrStore(instanceId, struct{}{})
	return !loaded
}

func MarkClientStopping(instanceId string) {
	clientStopping.Store(instanceId, struct{}{})
}

// SignalClientStop queues a stop request for the instance loop. The channel is
// buffered, and a full channel already means a stop request is pending.
func SignalClientStop(killSignal chan bool) {
	if killSignal == nil {
		return
	}
	select {
	case killSignal <- true:
	default:
	}
}

func clearClientLifecycleStop(instanceId string) {
	clientStopping.Delete(instanceId)
	remoteLoggedOut.Delete(instanceId)
}

func isClientStopping(instanceId string) bool {
	_, ok := clientStopping.Load(instanceId)
	return ok
}

func isRemoteLoggedOut(instanceId string) bool {
	_, ok := remoteLoggedOut.Load(instanceId)
	return ok
}

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
