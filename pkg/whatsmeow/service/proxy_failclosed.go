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
	"time"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
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
