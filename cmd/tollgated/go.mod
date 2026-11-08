module github.com/ethosagent/tollgate/cmd/tollgated

go 1.26

require (
	github.com/ethosagent/tollgate v0.0.0
	go.yaml.in/yaml/v3 v3.0.6
)

// Bootstrap only (ADR 0017): until the root v0.1.0 tag exists. The release
// bump requires the root release and drops this; go.work then builds against
// the checkout. scripts/check-release.sh refuses a tag while it is here.
replace github.com/ethosagent/tollgate => ../..
