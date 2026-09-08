package postgres

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

type graphSQLScanner interface {
	Scan(...any) error
}

func graphGORMScan(scanner graphSQLScanner, destinations ...any) error {
	if scanner == nil {
		return errors.New("graph GORM scanner is unavailable")
	}
	transformed := append([]any(nil), destinations...)
	for index, destination := range destinations {
		if values, ok := destination.(*[]string); ok {
			transformed[index] = &graphStringArrayDestination{destination: values}
		}
	}
	return scanner.Scan(transformed...)
}

type graphStringArrayDestination struct {
	destination *[]string
}

func (destination *graphStringArrayDestination) Scan(source any) error {
	if destination == nil || destination.destination == nil {
		return errors.New("graph array destination is unavailable")
	}
	var value graphStringArray
	if err := value.Scan(source); err != nil {
		return err
	}
	*destination.destination = append((*destination.destination)[:0], []string(value)...)
	return nil
}

type graphSQLRow struct {
	row *sql.Row
	err error
}

func (row graphSQLRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.row == nil {
		return errors.New("graph GORM row query returned no row handle")
	}
	return graphGORMScan(row.row, destinations...)
}

type graphSQLRows struct {
	rows *sql.Rows
	err  error
}

func (rows *graphSQLRows) Close() error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	if rows.err != nil {
		return rows.err
	}
	rows.err = rows.rows.Close()
	return rows.err
}

func (rows *graphSQLRows) Err() error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	if rows.err != nil {
		return rows.err
	}
	return rows.rows.Err()
}

func (rows *graphSQLRows) Next() bool {
	return rows != nil && rows.rows != nil && rows.err == nil && rows.rows.Next()
}

func (rows *graphSQLRows) Scan(destinations ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	return graphGORMScan(rows.rows, destinations...)
}

func gormRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (graphSQLScanner, error) {
	row, err := gormGraphRawRow(ctx, database, query, arguments...)
	if err != nil {
		return nil, err
	}
	return graphSQLRow{row: row}, nil
}

func gormRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*graphSQLRows, error) {
	rows, err := gormGraphRawRows(ctx, database, query, arguments...)
	if err != nil {
		return nil, err
	}
	return &graphSQLRows{rows: rows}, nil
}

func gormGraphRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGraphGORMDatabase(database) {
		return nil, errors.New("graph GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if statement.Error != nil {
		return nil, statement.Error
	}
	if row == nil {
		return nil, errors.New("graph GORM row query returned no row handle")
	}
	return row, nil
}

func gormGraphRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validGraphGORMDatabase(database) {
		return nil, errors.New("graph GORM rows query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("graph GORM rows query returned no rows handle")
	}
	return rows, nil
}

func gormGraphExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validGraphGORMDatabase(database) {
		return 0, errors.New("graph GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// gormQueryRow 保留单行扫描时的查询错误，不暴露驱动 Rows 或事务类型。
func gormQueryRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) graphSQLRow {
	row, err := gormGraphRawRow(ctx, database, query, arguments...)
	return graphSQLRow{row: row, err: err}
}
