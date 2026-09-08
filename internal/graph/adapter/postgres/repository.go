// Package postgres implements Graph read projections over canonical Knowledge facts.
package postgres

import "time"

const defaultStatementTimeout = 1500 * time.Millisecond

const setLocalStatementTimeoutSQL = `SELECT pg_catalog.set_config('statement_timeout',(@p1),true)`
