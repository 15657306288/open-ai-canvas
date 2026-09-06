package database

import (
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

func deploymentMigrationDatabase(t *testing.T) *gorm.DB {
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
	for _, item := range schemaMigrations {
		if item.version > 12 {
			break
		}
		if err := item.apply(db); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"playback_status", "playback_object_key", "playback_error"} {
		if db.Migrator().HasColumn(&model.Resource{}, column) {
			if err := db.Migrator().DropColumn(&model.Resource{}, column); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func TestMigrateSchemaPreservesDeploymentHistory(t *testing.T) {
	db := deploymentMigrationDatabase(t)
	var before []schemaMigration
	if err := db.Order("version").Find(&before).Error; err != nil {
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
	var after []schemaMigration
	if err := db.Where("version <= 12").Order("version").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("historical record count changed: %d to %d", len(before), len(after))
	}
	for index, record := range before {
		current := after[index]
		if record.Version != current.Version || record.Name != current.Name || record.Checksum != current.Checksum || !record.AppliedAt.Equal(current.AppliedAt) {
			t.Fatalf("historical record changed: before=%+v after=%+v", record, current)
		}
	}
	var playback schemaMigration
	if err := db.First(&playback, "version = 13").Error; err != nil {
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

func TestMigrateSchemaRejectsUnknownDeploymentLineage(t *testing.T) {
	for _, scenario := range []string{"checksum", "name", "version-six", "upstream-version-four"} {
		t.Run(scenario, func(t *testing.T) {
			db := deploymentMigrationDatabase(t)
			switch scenario {
			case "checksum":
				if err := db.Model(&schemaMigration{}).Where("version = 9").Update("checksum", "unknown").Error; err != nil {
					t.Fatal(err)
				}
			case "name":
				if err := db.Model(&schemaMigration{}).Where("version = 9").Update("name", "unknown").Error; err != nil {
					t.Fatal(err)
				}
			case "version-six":
				if err := db.Create(&schemaMigration{Version: 6, Name: "asset_library_folders", Checksum: assetLibraryFoldersChecksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
					t.Fatal(err)
				}
			case "upstream-version-four":
				if err := db.Model(&schemaMigration{}).Where("version = 4").Updates(map[string]any{
					"name": "resource_upload_key", "checksum": "sha256:resource-upload-key-v4-20260901",
				}).Error; err != nil {
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
