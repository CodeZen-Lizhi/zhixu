package migration

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// migrateRiver 在项目 Atlas 迁移完成后应用并校验 River schema。
func migrateRiver(ctx context.Context, pool *pgxpool.Pool, riverSchema string) error {
	riverMigrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: riverSchema})
	if err != nil {
		return fmt.Errorf("create River migrator: %w", err)
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply River migrations: %w", err)
	}
	validation, err := riverMigrator.Validate(ctx, nil)
	if err != nil {
		return fmt.Errorf("validate River migrations: %w", err)
	}
	if validation == nil || !validation.OK {
		return fmt.Errorf("validate River migrations: %s", validationMessage(validation))
	}
	return nil
}

// validationMessage 汇总 River 迁移校验失败细节。
func validationMessage(result *rivermigrate.ValidateResult) string {
	if result == nil {
		return "empty validation result"
	}
	if len(result.Messages) == 0 {
		return "validation failed without details"
	}
	return strings.Join(result.Messages, "; ")
}
