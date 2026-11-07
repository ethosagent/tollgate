package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ethosagent/tollgate"
)

func sampleMetric() tollgate.Metric {
	return tollgate.Metric{
		CallID: "c1", TenantID: "t1", SessionID: "s1", Provider: "openai", Model: "gpt-x",
		Stream: true, Status: 200, Finish: tollgate.FinishOK,
		Usage: tollgate.Usage{InputTokens: 10, OutputTokens: 20, CachedInputTokens: 3, ReasoningTokens: 4},
		Start: time.Date(2026, 11, 8, 12, 0, 0, 0, time.FixedZone("x", 3600)),
		TTFB:  150 * time.Millisecond, Duration: 2 * time.Second,
	}
}

func TestOptions_Validation(t *testing.T) {
	cases := []struct {
		table   string
		want    string // the sanitized SQL identifier
		wantErr bool
	}{
		{"", `"tollgate_metrics"`, false},
		{"my_metrics", `"my_metrics"`, false},
		{"_M1", `"_M1"`, false},
		{"1st-metrics", `"1st-metrics"`, false},
		{"ops.metrics", `"ops"."metrics"`, false},
		{"a.b.c", "", true},
		{".metrics", "", true},
		{"ops.", "", true},
		{`x"; DROP TABLE y; --`, "", true},
		{"tab\x00le", "", true},
		{"tab\nle", "", true},
		{strings.Repeat("a", 64), "", true},
		{strings.Repeat("a", 63), `"` + strings.Repeat("a", 63) + `"`, false},
	}
	for _, c := range cases {
		got, err := Options{Table: c.table}.table()
		if (err != nil) != c.wantErr || got.Sanitize() != c.want {
			t.Errorf("table(%q) = %q, %v; want %q, err=%v", c.table, got.Sanitize(), err, c.want, c.wantErr)
		}
	}
}

func TestNew_RejectsNilPoolAndBadTable(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Error("New(nil pool) succeeded, want error")
	}
	if _, err := New(&pgxpool.Pool{}, Options{Table: "no..way"}); err == nil {
		t.Error("New(bad table) succeeded, want error")
	}
	s, err := New(&pgxpool.Pool{}, Options{})
	if err != nil || s.table.Sanitize() != `"tollgate_metrics"` {
		t.Errorf("New default = %+v, %v", s, err)
	}
}

func TestRow_MapsEveryColumn(t *testing.T) {
	m := sampleMetric()
	row := rowOf(m)
	if len(row) != len(columns) {
		t.Fatalf("row has %d values for %d columns", len(row), len(columns))
	}
	want := []any{"c1", "t1", "s1", "openai", "gpt-x", true, int64(200), "ok",
		int64(10), int64(20), int64(3), int64(4), m.Start.UTC(), int64(150), int64(2000)}
	for i, w := range want {
		if g := row[i]; g != w {
			t.Errorf("column %s = %#v, want %#v", columns[i], g, w)
		}
	}
	if loc := row[12].(time.Time).Location(); loc != time.UTC {
		t.Errorf("started_at location = %v, want UTC", loc)
	}
}

type fakeCopier struct {
	table pgx.Identifier
	cols  []string
	rows  [][]any
	n     int64
	err   error
	calls int
}

func (f *fakeCopier) CopyFrom(_ context.Context, t pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	f.calls++
	f.table, f.cols = t, cols
	for src.Next() {
		v, _ := src.Values()
		f.rows = append(f.rows, v)
	}
	if f.n < 0 {
		return int64(len(f.rows)), f.err
	}
	return f.n, f.err
}

func newFake(f *fakeCopier) *Sink { return &Sink{copier: f, table: pgx.Identifier{"t_metrics"}} }

func TestWrite_OneCopyPerBatch(t *testing.T) {
	f := &fakeCopier{n: -1}
	s := newFake(f)
	if err := s.Write(context.Background(), []tollgate.Metric{sampleMetric(), sampleMetric()}); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || len(f.rows) != 2 || f.table[0] != "t_metrics" || len(f.cols) != len(columns) {
		t.Errorf("calls=%d rows=%d table=%v cols=%d", f.calls, len(f.rows), f.table, len(f.cols))
	}
}

func TestWrite_EmptyBatchSkipsRoundTrip(t *testing.T) {
	f := &fakeCopier{}
	if err := newFake(f).Write(context.Background(), nil); err != nil || f.calls != 0 {
		t.Errorf("err=%v calls=%d, want nil/0", err, f.calls)
	}
}

func TestWrite_CancelledContextFailsFast(t *testing.T) {
	f := &fakeCopier{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newFake(f).Write(ctx, []tollgate.Metric{sampleMetric()})
	if !errors.Is(err, context.Canceled) || f.calls != 0 {
		t.Errorf("err=%v calls=%d, want context.Canceled without a copy", err, f.calls)
	}
}

func TestWrite_ReturnsCopyErrorAndShortCopies(t *testing.T) {
	boom := errors.New("boom")
	err := newFake(&fakeCopier{err: boom}).Write(context.Background(), []tollgate.Metric{sampleMetric()})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapping boom", err)
	}
	err = newFake(&fakeCopier{n: 1}).Write(context.Background(), []tollgate.Metric{sampleMetric(), sampleMetric()})
	if err == nil {
		t.Error("short copy returned nil, want error")
	}
}

// requireDSN returns the integration DSN. Unset: skip loudly locally, fail in CI.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TOLLGATE_TEST_PG_DSN")
	if dsn != "" {
		return dsn
	}
	const msg = "TOLLGATE_TEST_PG_DSN is unset: the Postgres integration test is NOT running"
	if os.Getenv("CI") == "true" {
		t.Fatal(msg + " (CI=true requires a Postgres service container)")
	}
	t.Skip("SKIPPED LOUDLY: " + msg + ". Run it with e.g. TOLLGATE_TEST_PG_DSN='postgres://user@127.0.0.1:5432/db?sslmode=disable'")
	return ""
}

func TestPostgresSink_WritesBatch(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, requireDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	table := fmt.Sprintf("tg_test_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, strings.ReplaceAll(Schema, defaultTable, table)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer pool.Exec(ctx, "DROP TABLE IF EXISTS "+table)

	s, err := New(pool, Options{Table: table})
	if err != nil {
		t.Fatal(err)
	}
	m1, m2 := sampleMetric(), sampleMetric()
	m2.CallID, m2.Finish, m2.Status, m2.Stream = "c2", tollgate.FinishUpstreamError, 502, false
	if err := s.Write(ctx, []tollgate.Metric{m1, m2}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	qs, err := New(pool, Options{Table: "public." + table}) // schema-qualified
	if err != nil {
		t.Fatal(err)
	}
	m3 := sampleMetric()
	m3.CallID = "c3"
	if err := qs.Write(ctx, []tollgate.Metric{m3}); err != nil {
		t.Fatalf("Write schema-qualified: %v", err)
	}

	var n int
	var finish string
	var in, ttfb, dur int64
	var started time.Time
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 3 {
		t.Fatalf("count = %d, %v; want 3", n, err)
	}
	err = pool.QueryRow(ctx, "SELECT finish, input_tokens, started_at, ttfb_ms, duration_ms FROM "+table+" WHERE call_id='c2'").
		Scan(&finish, &in, &started, &ttfb, &dur)
	if err != nil || finish != "upstream_error" || in != 10 || ttfb != 150 || dur != 2000 || !started.Equal(m2.Start) {
		t.Errorf("row c2 = %q %d %v %d %d, %v", finish, in, started, ttfb, dur, err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Write(cctx, []tollgate.Metric{m1}); err == nil {
		t.Error("Write with cancelled ctx succeeded, want error")
	}
	if _, err := pool.Exec(ctx, "DROP TABLE "+table); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, []tollgate.Metric{m1}); err == nil {
		t.Error("Write to a missing table succeeded, want error (so the core counts drops)")
	}
}
