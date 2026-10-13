// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): upstream
// answered "success" with no data when the WhatsApp query failed, which
// clients read as "not on WhatsApp" (and deleted leads, cached "no WhatsApp").

package user_service

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	_ "modernc.org/sqlite"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
)

func TestFailedWhatsAppCheckIsAnError(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite",
		"file:"+t.TempDir()+"/wa.db?_pragma=foreign_keys(1)", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := whatsmeow.NewClient(container.NewDevice(), nil) // never connected: the query fails
	u := &userService{loggerWrapper: logger_wrapper.NewLoggerManager(&config.Config{LogDirectory: t.TempDir()})}

	uc, retry, err := u.performCheckUser(client, []string{"5511999999999"}, false, "test")
	if err == nil || uc != nil || retry {
		t.Fatalf("a failed query must be an error, got uc=%v retry=%v err=%v", uc, retry, err)
	}
}
