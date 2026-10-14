package whatsmeow_service

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/store/sqlstore"

	"github.com/evolution-foundation/evolution-go/pkg/config"
)

// Against a real Postgres (EVO_TEST_POSTGRES=postgres://..., ideally with a
// low max_connections): many starts keep the connection count flat, where
// upstream's sqlstore.New per start runs Postgres out of connections.
func TestDeviceContainerKeepsPostgresConnectionsBounded(t *testing.T) {
	dsn := os.Getenv("EVO_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("EVO_TEST_POSTGRES not set")
	}
	resetSharedDevices(t)
	t.Cleanup(func() { resetSharedDevices(t) })

	probe, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	probe.SetMaxOpenConns(1)
	count := func() int {
		var n int
		if err := probe.QueryRow("SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'").Scan(&n); err != nil {
			t.Fatalf("count connections: %v", err)
		}
		return n
	}

	w := whatsmeowService{config: &config.Config{PostgresAuthDB: dsn}}
	for i := 0; i < 60; i++ {
		container, err := w.deviceContainer()
		if err != nil {
			t.Fatalf("start %d: %v (connections: %d)", i, err, count())
		}
		if _, err := container.GetAllDevices(context.Background()); err != nil {
			t.Fatalf("start %d: query: %v", i, err)
		}
	}
	if n := count(); n > 5 {
		t.Fatalf("connections grew with the starts: %d", n)
	}

	// Upstream: a new pool per start, never closed.
	var leaked []*sqlstore.Container
	defer func() {
		for _, c := range leaked {
			_ = c.Close()
		}
	}()
	for i := 0; i < 60; i++ {
		c, err := sqlstore.New(context.Background(), "postgres", dsn, nil)
		if err != nil {
			if !strings.Contains(err.Error(), "too many clients") {
				t.Fatalf("upstream start %d failed for another reason: %v", i, err)
			}
			t.Logf("upstream pattern ran Postgres out of connections after %d starts", i)
			return
		}
		leaked = append(leaked, c)
	}
	t.Fatalf("upstream pattern did not exhaust connections; raise the start count or lower max_connections")
}
