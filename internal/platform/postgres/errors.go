package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLState 返回错误链中的 PostgreSQL 状态码，未包含数据库错误时返回空串。
func SQLState(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError != nil {
		return postgresError.Code
	}
	return ""
}

// ConstraintName 只投影约束名称，使业务 Adapter 不依赖驱动错误类型。
func ConstraintName(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError != nil {
		return postgresError.ConstraintName
	}
	return ""
}

// IsStatementTimeout 区分服务端语句超时与共用 57014 状态码的主动取消。
func IsStatementTimeout(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError != nil && postgresError.Code == "57014" &&
		strings.Contains(strings.ToLower(postgresError.Message), "statement timeout")
}
