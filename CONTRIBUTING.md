# Contributing

## Developer Certificate of Origin

Every commit must be signed off under the [DCO](https://developercertificate.org/):

```sh
git commit -s
```

This adds a `Signed-off-by: Your Name <you@example.com>` line. It certifies that you have the right to submit the change under the project's license.

## Development

- Go (see `go.mod` for the version), git 2.34+ and `ssh-keygen` (OpenSSH 8.2+).
- `go test ./...` runs unit and end-to-end tests. `go test -short ./...` runs only the unit tests.
- Run `gofmt` and `go vet ./...` before pushing.
- Keep dependencies to a minimum. This is a security tool that runs inside every agent's git calls.

## Sensitive areas

Changes to `internal/sign`, `internal/verify` and `internal/credential` need a review from a code owner. Please include tests that cover the refusal paths.
