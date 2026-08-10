<!-- Keep a PR to one concern. See CONTRIBUTING.md. -->

## What and why

<!-- What changes, and what real case motivated it. -->

## Checklist

- [ ] `go build ./... && go vet ./... && gofmt -l . && go test ./...` is clean
- [ ] Tests cover the behavior this PR changes
- [ ] Unresolvable cases are emitted as `uncertain` — nothing guessed, nothing dropped
- [ ] `protocol`, `detection`, and `confidence` are all set on any new edge
- [ ] Output stays byte-stable (nothing iterates a map into output)

## Output impact

<!-- Does this change the emitted JSON? If yes, describe the shape change —
     downstream consumers diff on it. If no, say "none". -->
