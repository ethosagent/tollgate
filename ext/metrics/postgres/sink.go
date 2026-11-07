package postgres

import (
	"context"
	_ "embed" // Schema
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ethosagent/tollgate"
)

// Schema is the DDL in schema.sql: the default table and its index.
//
//go:embed schema.sql
var Schema string

const defaultTable = "tollgate_metrics"

// maxIdent is Postgres's identifier length limit (NAMEDATALEN-1), in bytes.
const maxIdent = 63

// columns is the COPY column list, in rowOf order.
var columns = []string{
	"call_id", "tenant_id", "session_id", "provider", "model", "stream", "status", "finish",
	"input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens",
	"started_at", "ttfb_ms", "duration_ms",
}

// Options configures a Sink.
type Options struct {
	// Table is the destination table, optionally schema-qualified
	// ("schema.table"); default "tollgate_metrics". Each part is quoted as given
	// (so it is case-sensitive), must be 1 to 63 bytes and must not contain a
	// double quote or a control character.
	Table string
}

// table returns the quoted-by-construction identifier for Table. pgx quotes
// every part, so this rejects only names Postgres cannot store or that would
// read ambiguously in logs.
func (o Options) table() (pgx.Identifier, error) {
	if o.Table == "" {
		return pgx.Identifier{defaultTable}, nil
	}
	parts := strings.Split(o.Table, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("postgres sink: table %q has more than schema.table", o.Table)
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxIdent || strings.ContainsFunc(part, badIdentRune) {
			return nil, fmt.Errorf("postgres sink: invalid table name %q", o.Table)
		}
	}
	return pgx.Identifier(parts), nil
}

func badIdentRune(r rune) bool { return r == '"' || r < 0x20 || r == 0x7f }

// copier is the one pool method the sink uses; tests substitute a fake.
type copier interface {
	CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error)
}

// Sink is a tollgate.MetricSink that writes each batch with one COPY. Its
// fields are fixed at New and the pool is goroutine-safe, so Write is safe for
// concurrent use.
type Sink struct {
	copier copier
	table  pgx.Identifier
}

var _ tollgate.MetricSink = (*Sink)(nil)

// New validates opts once and returns a Sink over pool. The caller owns the
// pool and closes it after the proxy has drained.
func New(pool *pgxpool.Pool, opts Options) (*Sink, error) {
	if pool == nil {
		return nil, errors.New("postgres sink: nil pool")
	}
	table, err := opts.table()
	if err != nil {
		return nil, err
	}
	return &Sink{copier: pool, table: table}, nil
}

// Write inserts the batch in one COPY round trip, bounded by ctx. Any error
// (including a short copy) is returned so the core counts the batch as dropped.
// Safe for concurrent use.
func (s *Sink) Write(ctx context.Context, batch []tollgate.Metric) error {
	if len(batch) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres sink: %w", err)
	}
	rows := make([][]any, len(batch))
	for i, m := range batch {
		rows[i] = rowOf(m)
	}
	n, err := s.copier.CopyFrom(ctx, s.table, columns, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("postgres sink: copy %d rows: %w", len(batch), err)
	}
	if n != int64(len(batch)) {
		return fmt.Errorf("postgres sink: copied %d of %d rows", n, len(batch))
	}
	return nil
}

// rowOf maps a metric to the values for columns.
func rowOf(m tollgate.Metric) []any {
	return []any{
		m.CallID, m.TenantID, m.SessionID, m.Provider, m.Model, m.Stream, int64(m.Status), string(m.Finish),
		m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CachedInputTokens, m.Usage.ReasoningTokens,
		m.Start.UTC(), m.TTFB.Milliseconds(), m.Duration.Milliseconds(),
	}
}
