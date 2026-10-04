package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// TestClientCardKeyMigrationOnPopulatedDatabase proves the 卡密 column is an
// additive migration on a real, already-populated panel database: AutoMigrate
// adds clients.card_key and its unique index, every existing row keeps working
// with a NULL key, and a client created afterwards still inserts without one.
//
// It never touches the database it is pointed at — the file is copied first —
// and it is skipped unless a copy is supplied:
//
//	cp /etc/ui3344/ui3344.db /tmp/ui3344-copy.db
//	XUI_CARDKEY_MIGRATION_DB=/tmp/ui3344-copy.db \
//	  go test ./internal/database/ -run CardKeyMigration -count=1 -v
func TestClientCardKeyMigrationOnPopulatedDatabase(t *testing.T) {
	source := strings.TrimSpace(os.Getenv("XUI_CARDKEY_MIGRATION_DB"))
	if source == "" {
		t.Skip("set XUI_CARDKEY_MIGRATION_DB to a copy of a populated panel database")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read %s: %v", source, err)
	}
	work := filepath.Join(t.TempDir(), "x-ui.db")
	if err := os.WriteFile(work, raw, 0o600); err != nil {
		t.Fatalf("copy to %s: %v", work, err)
	}
	t.Setenv("XUI_DB_FOLDER", filepath.Dir(work))
	if err := InitDB(work); err != nil {
		t.Fatalf("InitDB on the copy: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	db := GetDB()
	if !db.Migrator().HasColumn(&model.ClientRecord{}, "card_key") {
		t.Fatal("clients.card_key was not added to the existing database")
	}
	if !db.Migrator().HasIndex(&model.ClientRecord{}, "idx_clients_card_key") {
		t.Fatal("the clients.card_key unique index was not added")
	}
	var total, keyed int64
	if err := db.Model(&model.ClientRecord{}).Count(&total).Error; err != nil {
		t.Fatalf("count clients: %v", err)
	}
	if err := db.Model(&model.ClientRecord{}).Where("card_key IS NOT NULL").Count(&keyed).Error; err != nil {
		t.Fatalf("count keyed clients: %v", err)
	}
	if keyed != 0 {
		t.Fatalf("the migration invented %d card keys", keyed)
	}
	if total == 0 {
		t.Fatal("the supplied database has no clients; point the test at a populated copy")
	}

	// A client created after the migration must still be insertable without a
	// key, which is what breaks if the unique index is applied to ''.
	if err := db.Create(&model.ClientRecord{Email: "cardkey-migration-probe@local", Enable: true}).Error; err != nil {
		t.Fatalf("create a keyless client after the migration: %v", err)
	}
	if err := db.Create(&model.ClientRecord{Email: "cardkey-migration-probe-2@local", Enable: true}).Error; err != nil {
		t.Fatalf("create a second keyless client after the migration: %v", err)
	}
	t.Logf("migrated a copy of %s: %d existing clients, 0 card keys, column and unique index present", source, total)
}
