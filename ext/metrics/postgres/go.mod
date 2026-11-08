module github.com/ethosagent/tollgate/ext/metrics/postgres

go 1.26

require (
	github.com/ethosagent/tollgate v0.0.0
	github.com/jackc/pgx/v5 v5.11.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// Bootstrap only (ADR 0017): until the root v0.1.0 tag exists. The release
// bump requires the root release and drops this; go.work then builds against
// the checkout. scripts/check-release.sh refuses a tag while it is here.
replace github.com/ethosagent/tollgate => ../../..
