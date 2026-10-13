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
