package db

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type statsRow struct {
	ID   int64
	Name string
}

func TestStatsCallbacksCountStatements(t *testing.T) {
	g, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=u dbname=d sslmode=disable",
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	if err := registerStatsCallbacks(g); err != nil {
		t.Fatalf("registerStatsCallbacks: %v", err)
	}

	s := reqstats.New()
	ctx := reqstats.With(context.Background(), s)

	if err := g.WithContext(ctx).Find(&[]statsRow{}).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if err := g.WithContext(ctx).Create(&statsRow{Name: "a"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := g.WithContext(ctx).Model(&statsRow{}).Where("id = ?", 1).Update("name", "b").Error; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := g.WithContext(ctx).Where("id = ?", 1).Delete(&statsRow{}).Error; err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := g.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if got := s.SQLCount(); got != 5 {
		t.Fatalf("SQLCount = %d, 期望 5", got)
	}
}

func TestStatsCallbacksWithoutStats(t *testing.T) {
	g, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=u dbname=d sslmode=disable",
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	if err := registerStatsCallbacks(g); err != nil {
		t.Fatalf("registerStatsCallbacks: %v", err)
	}

	if err := g.WithContext(context.Background()).Find(&[]statsRow{}).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}

	s := reqstats.New()
	if got := s.SQLCount(); got != 0 {
		t.Fatalf("SQLCount = %d, 期望 0", got)
	}
}
