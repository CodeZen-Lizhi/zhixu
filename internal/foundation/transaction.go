package foundation

import "context"

// TransactionIsolation is the database-independent isolation requested by an
// application transaction boundary.
type TransactionIsolation uint8

const (
	TransactionIsolationDefault TransactionIsolation = iota
	TransactionIsolationReadCommitted
	TransactionIsolationRepeatableRead
	TransactionIsolationSerializable
)

// TransactionOptions controls one database transaction without exposing a
// concrete database driver.
type TransactionOptions struct {
	Isolation TransactionIsolation
	ReadOnly  bool
}

// TransactionScope is an opaque capability for infrastructure adapters that
// must participate in the current database transaction.
type TransactionScope interface {
	TransactionScope()
}

// TransactionFunc performs work inside one transaction scope.
type TransactionFunc func(context.Context, TransactionScope) error

// UnitOfWork owns the commit or rollback decision for one database transaction.
type UnitOfWork interface {
	Within(context.Context, TransactionOptions, TransactionFunc) error
}
