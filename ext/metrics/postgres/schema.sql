-- Table for the tollgate Postgres metric sink (ext/metrics/postgres).
-- Apply once with your migration tool, or run postgres.Schema directly.
-- To use another table name, pass Options{Table: "..."} and rename it here.
CREATE TABLE IF NOT EXISTS tollgate_metrics (
    call_id          text        NOT NULL,
    tenant_id        text        NOT NULL,
    session_id       text        NOT NULL,
    provider         text        NOT NULL,
    model            text        NOT NULL,
    stream           boolean     NOT NULL,
    status           integer     NOT NULL,
    finish           text        NOT NULL,
    input_tokens     bigint      NOT NULL,
    output_tokens    bigint      NOT NULL,
    cached_tokens    bigint      NOT NULL,
    reasoning_tokens bigint      NOT NULL,
    started_at       timestamptz NOT NULL,
    ttfb_ms          bigint      NOT NULL,
    duration_ms      bigint      NOT NULL
);

CREATE INDEX IF NOT EXISTS tollgate_metrics_tenant_started_idx
    ON tollgate_metrics (tenant_id, started_at DESC);
