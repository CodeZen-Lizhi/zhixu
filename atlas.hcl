// Atlas is the sole schema migration authority (ADR-0029). schema.sql is the
// declarative desired-state baseline exported from the real migration result;
// atlas/migrations is the only versioned migration source.
env "local" {
  url = getenv("ZHIXU_DATABASE_URL")
  dev = getenv("ZHIXU_ATLAS_DEV_URL")
  src = "file://atlas/schema.sql"

  migration {
    dir = "file://atlas/migrations"
  }
}
