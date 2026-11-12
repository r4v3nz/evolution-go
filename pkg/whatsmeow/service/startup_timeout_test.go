package whatsmeow_service

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	_ "modernc.org/sqlite"
)

func TestStartupTimeoutCancelsStalledSOCKSHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	container, err := sqlstore.New(context.Background(), "sqlite", "file:"+t.TempDir()+"/session.db?_pragma=foreign_keys(1)", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	client := whatsmeow.NewClient(container.NewDevice(), nil)
	if err := client.SetProxyAddress("socks5://" + listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel, err := connectWithStartupTimeout(context.Background(), 100*time.Millisecond, client.ConnectContext)
	defer cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled SOCKS handshake must report its startup deadline, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stalled SOCKS handshake took %s", elapsed)
	}
	select {
	case conn := <-accepted:
		conn.Close()
	case <-time.After(time.Second):
		t.Fatal("test did not reach the SOCKS listener")
	}
}

func TestStartupTimeoutDoesNotExpireEstablishedSocketContext(t *testing.T) {
	var live context.Context
	cancel, err := connectWithStartupTimeout(context.Background(), 10*time.Millisecond, func(ctx context.Context) error { live = ctx; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case <-live.Done():
		t.Fatal("successful connection was cancelled by setup timer")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if live.Err() == nil {
		t.Fatal("connection lifetime must still end when caller closes it")
	}
}
