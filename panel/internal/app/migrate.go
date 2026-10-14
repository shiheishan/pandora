package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"text/tabwriter"

	migrations "github.com/aegispanel/aegis/db/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/cobra"
)

func newMigrateCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "数据库迁移（goose SQL）",
	}
	cmd.AddCommand(newMigrateUpCmd(configPath))
	cmd.AddCommand(newMigrateDownCmd(configPath))
	cmd.AddCommand(newMigrateStatusCmd(configPath))
	return cmd
}

func newMigrateUpCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "执行全部待执行的迁移",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, closeFn, err := openMigrationDB(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer closeFn()
			if err := migrations.Up(cmd.Context(), db); err != nil {
				return fmt.Errorf("迁移 up: %w", err)
			}
			return nil
		},
	}
}

func newMigrateDownCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "回退一个迁移版本",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, closeFn, err := openMigrationDB(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer closeFn()
			if err := migrations.DownOne(cmd.Context(), db); err != nil {
				return fmt.Errorf("迁移 down: %w", err)
			}
			return nil
		},
	}
}

func newMigrateStatusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "列出迁移版本与执行状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, closeFn, err := openMigrationDB(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer closeFn()
			versions, err := migrations.Status(cmd.Context(), db)
			if err != nil {
				return fmt.Errorf("查询迁移状态: %w", err)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "版本\t名字\t已执行")
			for _, v := range versions {
				applied := "否"
				if v.Applied {
					applied = "是"
				}
				fmt.Fprintf(w, "%d\t%s\t%s\n", v.Version, v.Name, applied)
			}
			return w.Flush()
		},
	}
}

func openMigrationDB(ctx context.Context, configPath string) (*sql.DB, func(), error) {
	cfg, _, err := loadBase(configPath)
	if err != nil {
		return nil, nil, err
	}
	dsn := cfg.Database.MigrationURL.Reveal()
	if dsn == "" {
		return nil, nil, fmt.Errorf("迁移需要 PANDORA_DATABASE_MIGRATION_URL")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("打开迁移数据库: %w", err)
	}
	closeFn := func() { _ = db.Close() }
	if err := db.PingContext(ctx); err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("连接迁移数据库: %w", err)
	}
	return db, closeFn, nil
}
