package database

import (
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

func legacyFolderMigrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range schemaMigrations[:5] {
		if err := item.apply(db); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateSchemaV7(db); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"playback_status", "playback_object_key", "playback_error"} {
		if db.Migrator().HasColumn(&model.Resource{}, column) {
			if err := db.Migrator().DropColumn(&model.Resource{}, column); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&schemaMigration{Version: 6, Name: "asset_library_folders", Checksum: assetLibraryFoldersChecksum, AppliedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateSchemaPreservesLegacyFolderMigration(t *testing.T) {
	db := legacyFolderMigrationDatabase(t)
	var before schemaMigration
	if err := db.First(&before, "version = 6").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO asset_folders (id, user_id, name) VALUES ('kept-folder', 'owner', 'Keep me')").Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := MigrateSchema(db); err != nil {
			t.Fatal(err)
		}
		if err := RequireSchemaVersion(db); err != nil {
			t.Fatal(err)
		}
	}
	var after schemaMigration
	if err := db.First(&after, "version = 6").Error; err != nil {
		t.Fatal(err)
	}
	if before.Name != after.Name || before.Checksum != after.Checksum || !before.AppliedAt.Equal(after.AppliedAt) {
		t.Fatalf("historical record changed: before=%+v after=%+v", before, after)
	}
	var playback schemaMigration
	if err := db.First(&playback, "version = 7").Error; err != nil {
		t.Fatal(err)
	}
	if playback.Name != "resource_playback_variant" || playback.Checksum != resourcePlaybackChecksum {
		t.Fatalf("unexpected playback migration: %+v", playback)
	}
	for _, column := range []string{"playback_status", "playback_object_key", "playback_error"} {
		if !db.Migrator().HasColumn(&model.Resource{}, column) {
			t.Fatalf("missing playback column %s", column)
		}
	}
	var name string
	if err := db.Raw("SELECT name FROM asset_folders WHERE id = 'kept-folder'").Scan(&name).Error; err != nil || name != "Keep me" {
		t.Fatalf("folder data changed: %q %v", name, err)
	}
}

func TestMigrateSchemaRejectsUnknownLegacyLineage(t *testing.T) {
	for _, scenario := range []string{"checksum", "name", "version-seven"} {
		t.Run(scenario, func(t *testing.T) {
			db := legacyFolderMigrationDatabase(t)
			switch scenario {
			case "checksum":
				if err := db.Model(&schemaMigration{}).Where("version = 6").Update("checksum", "unknown").Error; err != nil {
					t.Fatal(err)
				}
			case "name":
				if err := db.Model(&schemaMigration{}).Where("version = 6").Update("name", "unknown").Error; err != nil {
					t.Fatal(err)
				}
			case "version-seven":
				if err := db.Create(&schemaMigration{Version: 7, Name: "asset_library_folders", Checksum: assetLibraryFoldersChecksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := MigrateSchema(db); err == nil || !strings.Contains(err.Error(), "不一致") {
				t.Fatalf("expected lineage rejection, got %v", err)
			}
			if db.Migrator().HasColumn(&model.Resource{}, "playback_status") {
				t.Fatal("rejected migration changed resource schema")
			}
		})
	}
}

func TestMigrateSchemaPreservesLegacyPluginMigrationLineage(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:legacy-plugin-migration-lineage?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range schemaMigrations {
		if item.version > 27 {
			break
		}
		if item.version == 24 {
			item = migration{version: 24, name: "channel_model_description", checksum: legacyChannelModelDescriptionV24Checksum, apply: migrateChannelModelDescription}
		}
		if err := item.apply(db); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []migration{
		{version: 28, name: "channel_model_label", checksum: legacyChannelModelLabelV28Checksum, apply: migrateChannelModelLabel},
		{version: 29, name: "agent_execution_journal", checksum: "sha256:agent-execution-journal-v28", apply: migrationMustFind(t, 28).apply},
		{version: 30, name: "agent_resource_leases", checksum: "sha256:agent-resource-leases-v29-20260919", apply: migrationMustFind(t, 29).apply},
	} {
		if err := item.apply(db); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("schema = %#v, %v", status, err)
	}
	for version, expected := range map[int64]string{24: "channel_model_description", 28: "channel_model_label", 29: "agent_execution_journal", 30: "agent_resource_leases", 31: "builtin_tools", 32: "tool_favorites"} {
		var record schemaMigration
		if err := db.First(&record, "version = ?", version).Error; err != nil {
			t.Fatal(err)
		}
		if record.Name != expected {
			t.Fatalf("migration %d changed lineage: %#v", version, record)
		}
	}
}

func migrationMustFind(t *testing.T, version int64) migration {
	t.Helper()
	item, found := migrationByVersion(version)
	if !found {
		t.Fatalf("missing migration %d", version)
	}
	return item
}
