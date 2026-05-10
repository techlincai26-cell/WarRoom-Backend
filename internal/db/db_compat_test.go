package db

import (
	"strings"
	"testing"

	"war-room-backend/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestEnsureAssessmentCompatibilitySchemaRepairsMissingDomainColumn(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}

	DB = gdb
	t.Cleanup(func() {
		DB = nil
	})

	if err := gdb.AutoMigrate(&models.Assessment{}); err != nil {
		t.Fatalf("auto migrate assessment: %v", err)
	}

	if !gdb.Migrator().HasColumn(&models.Assessment{}, "Domain") {
		t.Fatalf("expected domain column to exist after auto migrate")
	}

	if err := gdb.Migrator().DropColumn(&models.Assessment{}, "Domain"); err != nil {
		t.Fatalf("drop domain column: %v", err)
	}

	if gdb.Migrator().HasColumn(&models.Assessment{}, "Domain") {
		t.Fatalf("expected domain column to be removed from legacy schema")
	}

	legacyAssessment := models.Assessment{
		ID:        uuid.New().String(),
		UserID:    uuid.New().String(),
		Level:     1,
		Status:    "IN_PROGRESS",
		BatchCode: "legacy-batch",
		Domain:    "Retail",
	}

	if err := gdb.Create(&legacyAssessment).Error; err == nil {
		t.Fatalf("expected create to fail before compatibility migration")
	} else if !strings.Contains(strings.ToLower(err.Error()), "domain") {
		t.Fatalf("expected missing-domain error, got: %v", err)
	}

	if err := EnsureAssessmentCompatibilitySchema(); err != nil {
		t.Fatalf("ensure compatibility schema: %v", err)
	}

	if !gdb.Migrator().HasColumn(&models.Assessment{}, "Domain") {
		t.Fatalf("expected domain column to be restored by compatibility migration")
	}

	repairedAssessment := models.Assessment{
		ID:        uuid.New().String(),
		UserID:    uuid.New().String(),
		Level:     1,
		Status:    "IN_PROGRESS",
		BatchCode: "legacy-batch",
		Domain:    "Retail",
	}

	if err := gdb.Create(&repairedAssessment).Error; err != nil {
		t.Fatalf("expected create to succeed after compatibility migration: %v", err)
	}

	var saved models.Assessment
	if err := gdb.First(&saved, "id = ?", repairedAssessment.ID).Error; err != nil {
		t.Fatalf("fetch repaired assessment: %v", err)
	}
	if saved.Domain != "Retail" {
		t.Fatalf("expected saved domain to be %q, got %q", "Retail", saved.Domain)
	}
}
