package postgres

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

const postgresIdentifierMaxLength = 63

func openGORMRoot(pool *pgxpool.Pool) (*sql.DB, *gorm.DB, error) {
	if pool == nil {
		return nil, nil, errors.New("open GORM root: PostgreSQL pool is nil")
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	database, err := gorm.Open(
		gormpostgres.New(gormpostgres.Config{Conn: sqlDB}),
		gormConfig(),
	)
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, errors.New("open GORM root: invalid database configuration")
	}
	return sqlDB, database, nil
}

func gormConfig() *gorm.Config {
	return &gorm.Config{
		SkipDefaultTransaction:                   true,
		NamingStrategy:                           schema.NamingStrategy{IdentifierMaxLength: postgresIdentifierMaxLength},
		Logger:                                   logger.Discard,
		NowFunc:                                  func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
		PrepareStmt:                              false,
		DefaultTransactionTimeout:                0,
		DefaultContextTimeout:                    0,
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		IgnoreRelationshipsWhenMigrating:         true,
		DisableNestedTransaction:                 false,
		AllowGlobalUpdate:                        false,
		TranslateError:                           false,
		CreateBatchSize:                          0,
	}
}
