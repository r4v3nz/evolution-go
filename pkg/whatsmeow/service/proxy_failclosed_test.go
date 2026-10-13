// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): tests for
// proxy_failclosed.go. Nothing here reaches the network: a fake SOCKS5 server
// on 127.0.0.1 records what the client asked for.

package whatsmeow_service

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	_ "modernc.org/sqlite"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

func TestResolveProxyAddress(t *testing.T) {
	env := &config.Config{ProxyProtocol: "socks5", ProxyHost: "10.66.0.2", ProxyPort: "10800",
		ProxyUsername: "evolution", ProxyPassword: "env-secret"}
	cases := []struct {
		name, instance string
		cfg            *config.Config
		want           string
		wantErr        bool
	}{
		{"no proxy anywhere", "", &config.Config{}, "", false},
		{"instance proxy", `{"protocol":"socks5","host":"10.66.0.2","port":"10800","username":"u","password":"p"}`,
			&config.Config{}, "socks5://u:p@10.66.0.2:10800", false},
		{"instance fields filled from env", `{"host":"10.66.0.3","port":"10801"}`, env,
			"socks5://evolution:env-secret@10.66.0.3:10801", false},
		{"env default for an instance without proxy", "", env, "socks5://evolution:env-secret@10.66.0.2:10800", false},
		{"env host+port without credentials still used", "", &config.Config{ProxyProtocol: "socks5", ProxyHost: "10.66.0.2", ProxyPort: "10800"},
			"socks5://10.66.0.2:10800", false},
		{"corrupt stored JSON", `{"host":`, env, "", true},
		{"stored proxy without host", `{"port":"10800","username":"u"}`, &config.Config{}, "", true},
		{"bad port", `{"protocol":"socks5","host":"10.66.0.2","port":"x y","username":"u","password":"s3cr3t"}`, &config.Config{}, "", true},
		{"unsupported protocol", `{"protocol":"ftp","host":"10.66.0.2","port":"10800"}`, &config.Config{}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveProxyAddress(c.instance, c.cfg)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil && (strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "env-secret")) {
				t.Fatalf("error leaks the password: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	if r := redactProxy("socks5://u:p@10.66.0.2:10800"); r != "socks5://10.66.0.2:10800" {
		t.Fatalf("redactProxy = %q", r)
	}
}

// fakeSOCKS5 is a username/password SOCKS5 server that never connects
// anywhere: it records the requested target and answers "host unreachable".
type fakeSOCKS5 struct {
	ln       net.Listener
	user     string
	password string

	mu       sync.Mutex
	authOK   int
	authFail int
	targets  []string
}

func newFakeSOCKS5(t *testing.T, user, password string) *fakeSOCKS5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSOCKS5{ln: ln, user: user, password: password}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return f
}

func (f *fakeSOCKS5) addr(user, password string) string {
	return fmt.Sprintf("socks5://%s:%s@%s", user, password, f.ln.Addr())
}

func (f *fakeSOCKS5) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, hdr[1])); err != nil {
		return
	}
	c.Write([]byte{5, 2}) // username/password required
	ver := make([]byte, 2)
	if _, err := io.ReadFull(c, ver); err != nil {
		return
	}
	user := make([]byte, ver[1])
	io.ReadFull(c, user)
	pl := make([]byte, 1)
	io.ReadFull(c, pl)
	pass := make([]byte, pl[0])
	io.ReadFull(c, pass)
	f.mu.Lock()
	ok := string(user) == f.user && string(pass) == f.password
	if ok {
		f.authOK++
	} else {
		f.authFail++
	}
	f.mu.Unlock()
	if !ok {
		c.Write([]byte{1, 1})
		return
	}
	c.Write([]byte{1, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		io.ReadFull(c, l)
		b := make([]byte, l[0])
		io.ReadFull(c, b)
		host = string(b)
	case 4:
		b := make([]byte, 16)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	}
	pb := make([]byte, 2)
	io.ReadFull(c, pb)
	f.mu.Lock()
	f.targets = append(f.targets, net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
	f.mu.Unlock()
	c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0}) // host unreachable
}

func (f *fakeSOCKS5) snapshot() (ok, fail int, targets []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authOK, f.authFail, append([]string(nil), f.targets...)
}

func TestWebVersionCheckGoesThroughProxy(t *testing.T) {
	cachedWebVersionMu.Lock()
	cachedWebVersion = nil
	cachedWebVersionMu.Unlock()

	px := newFakeSOCKS5(t, "evolution", "right")
	if _, err := fetchWhatsAppWebVersion(px.addr("evolution", "right")); err == nil {
		t.Fatal("expected an error: the fake proxy reaches nothing")
	}
	ok, _, targets := px.snapshot()
	// The hostname (not a locally resolved IP) is handed to the proxy.
	if ok != 1 || len(targets) != 1 || targets[0] != "web.whatsapp.com:443" {
		t.Fatalf("version check did not go through the proxy: auth ok=%d targets=%v", ok, targets)
	}
}

// The fork refuses to connect when Connect fails with this text; make sure
// whatsmeow really reports a rejected proxy password this way, and that the
// client never tries anything but the proxy.
func TestRejectedProxyPasswordIsDetected(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite",
		"file:"+t.TempDir()+"/wa.db?_pragma=foreign_keys(1)", nil)
	if err != nil {
		t.Fatal(err)
	}
	px := newFakeSOCKS5(t, "evolution", "right")
	client := whatsmeow.NewClient(container.NewDevice(), nil)
	if err := client.SetProxyAddress(px.addr("evolution", "wrong")); err != nil {
		t.Fatal(err)
	}
	err = client.Connect()
	if err == nil {
		client.Disconnect()
		t.Fatal("Connect succeeded through a proxy that rejects the password")
	}
	if !strings.Contains(err.Error(), "username/password authentication failed") {
		t.Fatalf("unexpected error text (the fail-closed branch would not match): %v", err)
	}
	ok, fail, targets := px.snapshot()
	if ok != 0 || fail < 1 || len(targets) != 0 {
		t.Fatalf("auth ok=%d fail=%d targets=%v", ok, fail, targets)
	}
}

func TestProxyRetryBackoff(t *testing.T) {
	r := &proxyRetrier{attempts: map[string]int{}, pending: map[string]bool{}, base: 10 * time.Second, max: 2 * time.Minute}
	r.max = 30 * time.Second
	want := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		d, ok := r.next("a")
		if !ok || d != w {
			t.Fatalf("attempt %d: got %v %v, want %v", i+1, d, ok, w)
		}
		if _, ok := r.next("a"); ok {
			t.Fatalf("attempt %d: a second retry was scheduled while one is pending", i+1)
		}
		r.done("a")
	}
	if d, _ := r.next("b"); d != 10*time.Second {
		t.Fatalf("instances must have independent backoff: %v", d)
	}
	r.done("a")
	r.reset("a")
	if d, _ := r.next("a"); d != 10*time.Second {
		t.Fatalf("reset must restart the backoff: %v", d)
	}
}

func TestShouldRetryProxy(t *testing.T) {
	waiting := func() *instance_model.Instance {
		return &instance_model.Instance{Jid: "5511999999999@s.whatsapp.net",
			DisconnectReason: proxyRetryReason + " (retry in 10s): socks connect: connection refused"}
	}
	if !shouldRetryProxy(waiting(), false) {
		t.Fatal("a logged-in instance waiting for its proxy must be retried")
	}
	cases := map[string]func(*instance_model.Instance) (*instance_model.Instance, bool){
		"logged out":          func(i *instance_model.Instance) (*instance_model.Instance, bool) { i.Jid = ""; return i, false },
		"connected meanwhile": func(i *instance_model.Instance) (*instance_model.Instance, bool) { i.Connected = true; return i, false },
		"state changed": func(i *instance_model.Instance) (*instance_model.Instance, bool) {
			i.DisconnectReason = "Reconnecting"
			return i, false
		},
		"client connected": func(i *instance_model.Instance) (*instance_model.Instance, bool) { return i, true },
		"wrong password": func(i *instance_model.Instance) (*instance_model.Instance, bool) {
			i.DisconnectReason = "proxy: the proxy rejected the username/password"
			return i, false
		},
		"instance deleted (nil)": func(*instance_model.Instance) (*instance_model.Instance, bool) { return nil, false },
	}
	for name, mod := range cases {
		inst, connected := mod(waiting())
		if shouldRetryProxy(inst, connected) {
			t.Errorf("%s: must not retry", name)
		}
	}
}

func TestProxyUnavailableSignal(t *testing.T) {
	id := "fast-fail-test"
	proxyRetry.reset(id)
	if ProxyUnavailable(id) {
		t.Fatal("no failed attempt yet")
	}
	if _, ok := proxyRetry.next(id); !ok {
		t.Fatal("first failure must schedule a retry")
	}
	if !ProxyUnavailable(id) {
		t.Fatal("after a failure through the proxy the instance must fail fast")
	}
	if !strings.Contains(ErrProxyUnavailable.Error(), "not connected") || !strings.Contains(ErrProxyUnavailable.Error(), "nothing was sent") {
		t.Fatalf("clients match on these words: %q", ErrProxyUnavailable)
	}
	proxyRetry.done(id)
	proxyRetry.reset(id) // a successful connection
	if ProxyUnavailable(id) {
		t.Fatal("a successful connection must clear it")
	}
}
