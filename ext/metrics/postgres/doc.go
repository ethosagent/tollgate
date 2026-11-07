// Package postgres is a tollgate.MetricSink that stores one row per completed
// call in Postgres, written with a single COPY per batch. It lives in its own
// Go module so hosts that do not use it never download pgx (docs/decisions/0011).
//
// Create the table once with schema.sql (also exported as Schema), then wire it:
//
//	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
//	if err != nil { /* bad DSN */ }
//	defer pool.Close()
//	if _, err := pool.Exec(ctx, postgres.Schema); err != nil { /* migrate */ }
//	sink, err := postgres.New(pool, postgres.Options{Table: "tollgate_metrics"})
//	if err != nil { /* bad table name */ }
//	p, err := tollgate.New(tollgate.Options{Metrics: sink})
//
// Write honours the context deadline the core gives it and returns any error
// (connection, constraint, short copy), so the core counts the batch as
// dropped; it never retries or blocks a request. A Sink is safe for concurrent
// use. The table name (optionally "schema.table") is quoted part by part by pgx,
// so it is safe by construction; metric values go through COPY, never
// through string-built SQL. The pool, its TLS and its credentials belong to
// the host.
//
// Release tags for this module are path-prefixed: ext/metrics/postgres/vX.Y.Z.
package postgres
