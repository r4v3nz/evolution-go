package instance_repository

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestConnectionStateQueriesHaveDeadlineAndAtomicUpdate(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	checkDeadline := func(tx *gorm.DB) {
		deadline, ok := tx.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > connectionStateTimeout {
			t.Error("connection state query has no bounded deadline")
		}
	}
	db.Callback().Query().Before("gorm:query").Register("check_query_deadline", checkDeadline)
	db.Callback().Update().Before("gorm:update").Register("check_update_deadline", checkDeadline)
	id := "383904c2-5971-41d1-b742-42569618a969"
	mock.ExpectQuery(`SELECT .* FROM "instances"`).WithArgs(id, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "connected", "disconnect_reason"}).AddRow(id, false, "Reconnecting"))
	repo := &instanceRepository{db: db}
	row, err := repo.GetInstanceByID(id)
	if err != nil || row == nil || row.DisconnectReason != "Reconnecting" {
		t.Fatalf("read state: %+v %v", row, err)
	}
	// The false flag and reason must be in the same SQL update, including zero values.
	mock.ExpectExec(`UPDATE "instances" SET "connected"=\$1,"disconnect_reason"=\$2 WHERE id = \$3`).WithArgs([]driver.Value{false, "connection retry", id}...).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateConnected(id, false, "connection retry"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
