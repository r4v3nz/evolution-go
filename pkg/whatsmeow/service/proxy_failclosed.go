// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): this file is
// not part of upstream Evolution Go.
//
// Upstream connects WITHOUT the proxy when the proxy configuration cannot be
// applied or when the proxy rejects the credentials, exposing the server IP
// to WhatsApp. Here every connection to WhatsApp either uses the configured
// proxy or does not happen at all ("fail closed").

package whatsmeow_service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	utils "github.com/evolution-foundation/evolution-go/pkg/utils"
)

var errProxyRequired = errors.New("PROXY_REQUIRED=true but the instance has no proxy configured")

// resolveProxyAddress returns the proxy URL an instance must use, or "" when
// it has none. instanceProxy is the JSON stored on the instance; empty fields
// are filled from the PROXY_* environment, and an instance without its own
// proxy uses PROXY_HOST/PROXY_PORT when they are set. Any inconsistency is an
// error: the caller must then refuse to connect.
func resolveProxyAddress(instanceProxy string, cfg *config.Config) (string, error) {
	var pc ProxyConfig
	if instanceProxy != "" {
		if err := json.Unmarshal([]byte(instanceProxy), &pc); err != nil {
			return "", errors.New("stored proxy configuration is not valid JSON")
		}
	}
	if pc.Host == "" && (cfg.ProxyHost == "" || cfg.ProxyPort == "") {
		if instanceProxy != "" && (pc.Port != "" || pc.Username != "" || pc.Protocol != "") {
			return "", errors.New("stored proxy configuration has no host")
		}
		return "", nil
	}
	fill := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	// Upstream silently turns an unknown protocol (e.g. a typo) into "http".
	switch protocol := strings.ToLower(strings.TrimSpace(fill(pc.Protocol, cfg.ProxyProtocol))); protocol {
	case "", "http", "https", "socks", "socks5":
	default:
		return "", fmt.Errorf("unsupported proxy protocol %q (use socks5, http or https)", protocol)
	}
	addr, err := utils.BuildProxyAddress(
		fill(pc.Protocol, cfg.ProxyProtocol),
		fill(pc.Host, cfg.ProxyHost),
		fill(pc.Port, cfg.ProxyPort),
		fill(pc.Username, cfg.ProxyUsername),
		fill(pc.Password, cfg.ProxyPassword),
	)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", errors.New("proxy host/port do not form a valid address") // err would contain the password
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil || u.Hostname() == "" {
		return "", errors.New("proxy host/port do not form a valid address")
	}
	return addr, nil
}

// redactProxy returns scheme://host:port (never the credentials) for logs.
func redactProxy(addr string) string {
	u, err := url.Parse(addr)
	if err != nil {
		return "<invalid>"
	}
	return u.Scheme + "://" + u.Host
}

// webVersionClient is the HTTP client for the WhatsApp Web version check: it
// goes through the instance proxy when there is one, never around it.
func webVersionClient(proxyAddress string) (*http.Client, error) {
	if proxyAddress == "" {
		return http.DefaultClient, nil
	}
	u, err := url.Parse(proxyAddress)
	if err != nil {
		return nil, errors.New("invalid proxy address")
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(u), ForceAttemptHTTP2: true},
	}, nil
}

// refuseWithoutProxy records why the instance was not connected.
func (w whatsmeowService) refuseWithoutProxy(cd *ClientData, reason error) {
	w.loggerWrapper.GetLogger(cd.Instance.Id).LogError("[%s] Proxy problem, NOT connecting without it (fail-closed): %v", cd.Instance.Id, reason)
	cd.Instance.Connected = false
	cd.Instance.DisconnectReason = fmt.Sprintf("proxy: %v", reason)
	if err := w.instanceRepository.UpdateConnected(cd.Instance.Id, false, cd.Instance.DisconnectReason); err != nil {
		w.loggerWrapper.GetLogger(cd.Instance.Id).LogError("[%s] Error updating instance: %s", cd.Instance.Id, err)
	}
}

// --- Automatic retry while the proxy is unreachable -----------------------
//
// Upstream never retries a failed connection, so a short proxy outage (e.g. a
// mobile uplink renewing its IP) left logged-in instances offline until
// someone reconnected them by hand. Here a logged-in instance whose proxy is
// unreachable is retried with backoff (10 s, 20 s, then every 30 s), always
// through the proxy, so it is back at most ~30 s after the proxy. A rejected
// password is not retried. While it waits, the send/user/message endpoints
// fail at once with ErrProxyUnavailable instead of reconnecting through a
// proxy known to be down (upstream spent ~36 s on that, past most clients'
// HTTP timeouts, leaving them unsure whether the message went out).

const proxyRetryReason = "proxy unavailable"

// ErrProxyUnavailable: the instance is not connected because its proxy is
// unreachable, so nothing was sent. The text keeps "not connected" for
// clients that recognise a lost session by it.
var ErrProxyUnavailable = errors.New("proxy unavailable: instance not connected until its proxy is reachable again; nothing was sent")

// ProxyUnavailable reports whether the instance's last connection attempt
// failed because its proxy was unreachable, with no successful one since.
func ProxyUnavailable(instanceId string) bool {
	proxyRetry.mu.Lock()
	defer proxyRetry.mu.Unlock()
	return proxyRetry.attempts[instanceId] > 0
}

type proxyRetrier struct {
	mu       sync.Mutex
	attempts map[string]int
	pending  map[string]bool
	base     time.Duration
	max      time.Duration
}

var proxyRetry = &proxyRetrier{attempts: map[string]int{}, pending: map[string]bool{},
	base: 10 * time.Second, max: 30 * time.Second}

// next returns the delay of the next retry, or false if one is already pending.
func (r *proxyRetrier) next(id string) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending[id] {
		return 0, false
	}
	r.attempts[id]++
	d := r.base << min(r.attempts[id]-1, 6)
	if d > r.max {
		d = r.max
	}
	r.pending[id] = true
	return d, true
}

func (r *proxyRetrier) done(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

func (r *proxyRetrier) reset(id string) {
	r.mu.Lock()
	delete(r.attempts, id)
	r.mu.Unlock()
}

// shouldRetryProxy: still logged in, still offline and nothing else changed
// the instance state since the retry was scheduled (a manual reconnect,
// logout or a successful connection all overwrite the reason).
func shouldRetryProxy(inst *instance_model.Instance, connectedNow bool) bool {
	return inst != nil && inst.Jid != "" && !inst.Connected && !connectedNow &&
		strings.HasPrefix(inst.DisconnectReason, proxyRetryReason)
}

func (w whatsmeowService) retryWhenProxyIsBack(cd *ClientData, cause error) {
	id := cd.Instance.Id
	delay, scheduled := proxyRetry.next(id)
	// The reason is written even when a retry is already pending: an
	// automatic reconnect in between (e.g. upstream's on "Disconnected")
	// overwrites it with "Reconnecting", and the pending retry would then
	// take that for a manual change and give up for good.
	reason := fmt.Sprintf("%s (retrying): %v", proxyRetryReason, cause)
	if scheduled {
		reason = fmt.Sprintf("%s (retry in %s): %v", proxyRetryReason, delay, cause)
	}
	w.loggerWrapper.GetLogger(id).LogWarn("[%s] %s", id, reason)
	cd.Instance.Connected = false
	cd.Instance.DisconnectReason = reason
	if err := w.instanceRepository.UpdateConnected(id, false, reason); err != nil {
		w.loggerWrapper.GetLogger(id).LogError("[%s] Error updating instance: %s", id, err)
	}
	if !scheduled {
		return
	}
	time.AfterFunc(delay, func() {
		proxyRetry.done(id)
		inst, err := w.instanceRepository.GetInstanceByID(id)
		c := w.clientPointer[id]
		if err != nil || !shouldRetryProxy(inst, c != nil && c.IsConnected()) {
			return
		}
		w.loggerWrapper.GetLogger(id).LogInfo("[%s] Retrying the connection through the proxy", id)
		if err := w.ReconnectClient(id); err != nil {
			w.loggerWrapper.GetLogger(id).LogError("[%s] Retry failed to start: %v", id, err)
		}
	})
}
