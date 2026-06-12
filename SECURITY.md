# Security Policy

## Reporting a vulnerability

Please do **not** open a public issue for security problems.

Report vulnerabilities privately via
[GitHub Security Advisories](https://github.com/paulosouzajr/mycedrive-k8s/security/advisories/new).
You should receive a response within two weeks.

## Scope notes

- The Migration Coordinator REST API is unauthenticated by design and intended
  for in-cluster use only — do not expose the operator Service outside the
  cluster. Reports about hardening it are welcome.
- The checkpoint transfer protocol moves raw process memory between nodes;
  treat checkpoint directories and transfers as sensitive data.

## Supported versions

Only the latest released version receives security fixes.
