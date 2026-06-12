# Contributing to MyceDrive

Thanks for your interest! Bug reports, fixes and feature proposals are welcome
through GitHub issues and pull requests.

## Repository layout

Three Go modules linked by `go.work`:

| Module | Purpose |
|--------|---------|
| `operator/` | Kubernetes operator: CRDs, reconcilers, Migration Coordinator REST API, dashboard |
| `go-agent/` | Execution Agent embedded in application containers |
| `tests/functional/` | Cross-module functional tests (agent ↔ operator wire contract) |

`deployment/operator` holds the Helm chart; `dmtcp/` the sidecar image;
`scripts/make-migratable.sh` the StatefulSet onboarding tool.

## Building and testing

Requires Go ≥ 1.23, Docker, Helm v3.

```sh
make test        # unit + functional tests, no cluster needed
make lint        # go vet on all modules
make build       # docker images (operator, go-agent, dmtcp)
helm lint deployment/operator
```

Run a single module's tests from its directory with `go test ./...`.

## Pull requests

- Target `main`. CI (`PR Checks`) must pass: gofmt, build, vet and tests for
  all three modules plus Helm lint/template.
- Keep the legacy REST endpoints (`/register`, `/remove`, `/copy`, `/migrate`)
  byte-compatible — new response fields must be additive (`omitempty`).
- Add or extend tests for behavior changes; the functional suite in
  `tests/functional/` is the right place for anything crossing the
  agent/operator boundary.
- Run `gofmt -w` before pushing.

## Reporting issues

Use the issue templates. For suspected security problems, see
[SECURITY.md](SECURITY.md) instead of opening a public issue.
