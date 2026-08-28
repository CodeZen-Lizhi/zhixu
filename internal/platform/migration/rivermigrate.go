package migration

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// migrateRiver 应用并校验 River schema；Goose Runner 与后续 Atlas Runner 共用。
// TODO 3 恢复暂存的 Atlas 文件时，须删除其中重复的 migrateRiver 副本。
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
