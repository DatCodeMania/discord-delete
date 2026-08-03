<!-- Open an issue first for anything large or structural. Small fixes can go straight to a PR. -->

## What this changes

<!-- The problem and the fix, in a sentence or two. If it closes an issue, write "Fixes #123" here. -->

## How it was tested

<!-- Platform, and whether you ran it against a real data package. A dry run is enough. Say so if you only ran the tests. -->

## Checklist

- [ ] `gofmt -l .`, `go vet ./...`, and `go test ./...` pass locally
- [ ] New `.go` files come with a matching `_test.go`, and fixed bugs come with a regression test
- [ ] No editor config, build output, or data packages in the diff
- [ ] No new dependency that needs CGO
