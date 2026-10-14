// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): this file is
// not part of upstream Evolution Go.
//
// Upstream calls sqlstore.New on every StartClient, i.e. every instance start
// and every reconnect. sqlstore.New opens a new *sql.DB (a new connection pool,
// unlimited open connections) and nothing ever closes the previous one, so
// every reconnect leaks a pool. With many instances reconnecting at once (a
// proxy changing IP), Postgres runs out of connections ("sorry, too many
// clients already"), and from then on no instance can start: it stays
// disconnected and /instance/qr never has a QR code.
//
// A whatsmeow Container holds any number of devices, so one Container (one
// bounded pool) serves every instance for the life of the process.

package whatsmeow_service

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Bounds for the shared device store pool. The device store sees short
// queries; 30 connections leave room for the other pools of the process
// within Postgres' max_connections.
const (
	authDBMaxOpenConns    = 30
	authDBMaxIdleConns    = 10
	authDBConnMaxIdleTime = 5 * time.Minute
)

var sharedDevices = struct {
	sync.Mutex
	container *sqlstore.Container
}{}

// deviceContainer returns the process-wide device store, opening it on first
// use. A failed open is not cached: the next StartClient tries again.
func (w whatsmeowService) deviceContainer() (*sqlstore.Container, error) {
	sharedDevices.Lock()
	defer sharedDevices.Unlock()

	if sharedDevices.container != nil {
		return sharedDevices.container, nil
	}

	var dbLog waLog.Logger
	if w.config.WaDebug != "" {
		dbLog = waLog.Stdout("Database", w.config.WaDebug, true)
	}

	container, err := openDeviceContainer(w.config.PostgresAuthDB, w.exPath, dbLog)
	if err != nil {
		return nil, err
	}

	sharedDevices.container = container
	return container, nil
}

// openDeviceContainer opens the device store like sqlstore.New, with a bounded
// pool: Postgres when postgresAuthDB is set, otherwise the local SQLite file.
func openDeviceContainer(postgresAuthDB, exPath string, dbLog waLog.Logger) (*sqlstore.Container, error) {
	dialect, address := "postgres", postgresAuthDB
	if address == "" {
		dialect = "sqlite"
		address = fmt.Sprintf("file:%s/dbdata/main.db?_pragma=foreign_keys(1)&_busy_timeout=5000&cache=shared&mode=rwc&_journal_mode=WAL", exPath)
	}

	db, err := sql.Open(dialect, address)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(authDBMaxOpenConns)
	db.SetMaxIdleConns(authDBMaxIdleConns)
	db.SetConnMaxIdleTime(authDBConnMaxIdleTime)

	container := sqlstore.NewWithDB(db, dialect, dbLog)
	if err := container.Upgrade(context.Background()); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("failed to upgrade database: %w", err)
	}

	return container, nil
}
