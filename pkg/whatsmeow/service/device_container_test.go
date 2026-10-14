package whatsmeow_service

import (
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/evolution-foundation/evolution-go/pkg/config"
)

func resetSharedDevices(t *testing.T) {
	t.Helper()
	sharedDevices.Lock()
	if sharedDevices.container != nil {
		_ = sharedDevices.container.Close()
	}
	sharedDevices.container = nil
	sharedDevices.Unlock()
}

// Every start and reconnect used to open a new pool; now they all share one.
func TestDeviceContainerIsSharedAcrossStarts(t *testing.T) {
	resetSharedDevices(t)
	t.Cleanup(func() { resetSharedDevices(t) })

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dbdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := whatsmeowService{config: &config.Config{}, exPath: dir}

	first, err := w.deviceContainer()
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := w.deviceContainer()
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("open %d returned a new container: every start would leak a pool", i)
		}
	}
}

// A failed open (database full, down) must not stick: the next start retries.
func TestDeviceContainerRetriesAfterFailedOpen(t *testing.T) {
	resetSharedDevices(t)
	t.Cleanup(func() { resetSharedDevices(t) })

	broken := whatsmeowService{config: &config.Config{PostgresAuthDB: "postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"}}
	if _, err := broken.deviceContainer(); err == nil {
		t.Fatal("expected the unreachable database to fail")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dbdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	working := whatsmeowService{config: &config.Config{}, exPath: dir}
	if container, err := working.deviceContainer(); err != nil || container == nil {
		t.Fatalf("open after a failure: container=%v err=%v", container, err)
	}
}
