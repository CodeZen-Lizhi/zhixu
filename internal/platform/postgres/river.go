package postgres

import (
	"errors"

	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
)

// RiverSQLDriver returns an insert-capable River driver backed by the shared
// database/sql facade. Worker and listener runtimes must keep using riverpgxv5.
func (p *Pool) RiverSQLDriver() (*riverdatabasesql.Driver, error) {
	if p == nil || p.sqlDB == nil || p.closed.Load() {
		return nil, errors.New("PostgreSQL database/sql facade is not initialized")
	}
	return riverdatabasesql.New(p.sqlDB), nil
}
