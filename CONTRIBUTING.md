# Contributing

Thanks for helping. This project stores legal records, so changes to hashing, storage or archive behavior need extra care.

## Setup

Requires Go 1.27+, Node (syntax check only) and optionally Helm.

```sh
make check      # go vet + JavaScript syntax
make test       # go test -race ./...
make helm-check # chart lint/template
make e2e        # browser tests (Playwright, downloads Chromium)
```

## Guidelines

- Keep the event schema, canonical JSON hashing and GCS object names backward compatible. Add a fixture under `internal/journal/testdata` for any format change.
- No deletion or in-place edit features for journal events; corrections are new events.
- Render user data with `textContent`, never as HTML. The CSP forbids inline scripts and styles.
- Never log request bodies or principal data.
- Add tests with behavior changes and run `gofmt`.
- Legal-requirement changes should update `docs/LEGAL_REVIEW.md` with a source citation.

Open an issue before large changes. By contributing you agree your work is released under the MIT license.
