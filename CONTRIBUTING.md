# Contributing to metakill

Thanks for helping. A few ground rules keep metakill trustworthy.

## Principles

- **Never silently leave metadata behind.** If a handler cannot remove
  something, it must say so with `report.Unclean(...)`. That sets
  `fully_clean: false` and makes the run exit with code 3.
- **Never corrupt a file.** Prefer in-place blanking (zeroing, Void/JUNK/free
  elements) over restructuring when offsets elsewhere in the file depend on
  layout.
- **Pure Go only.** No cgo, no shelling out, no network access. A test fails
  if any metakill package imports `net`.
- **Every user-facing flag has a short and a long form.**

## Layout

```
cmd/metakill/          CLI wiring (cobra) and the os/exec integration tests
internal/cli/          orchestration: walking, prompting, safe writes, selftest
internal/handlers/     one file per format family + handler.go (interface, registry)
internal/meta/         low-level parsers/writers (exif, xmp, iptc, id3, ape,
                       vorbis, isobmff, classify, memio)
internal/report/       Report types and the text/JSON emitters
internal/fixtures/     Go generators for test fixtures
testdata/fixtures/     generated fixtures (make fixtures)
testdata/golden/       expected dry-run output (make golden)
```

## Adding or changing a format

1. **Handler.** Add `internal/handlers/<format>.go` implementing `Handler`
   and call `Register` in `init()`. If your magic bytes overlap another
   format (ZIP, RIFF, ISO-BMFF), make `CanHandle` specific enough to tell
   them apart.
2. **One code path.** Implement Inspect and Strip on top of one shared
   "plan" or "process" function, so they can never disagree.
3. **Validation.** After Strip, Inspect of the output must report zero
   fields. The CLI enforces this and refuses to replace the original if it
   doesn't hold. Kept items belong in `rep.Keep(...)`, not `rep.Add(...)`.
4. **Classification.** Map new keys to categories in
   `internal/meta/classify` so categories and `-k` behave the same across
   formats.
5. **Fixture.** Add a generator in `internal/fixtures` that plants known
   values (reuse the constants in `build.go`), then run `make fixtures`.
6. **Tests.** Add a table-driven round-trip test in
   `internal/handlers/*_test.go`. It must check that the planted values are
   gone, that content is intact (decode or compare payloads), and the
   behaviour of `-k`, `-t` and `-c` where relevant.
7. **Golden output.** Run `make golden` and review the diff in
   `testdata/golden`.
8. **Docs.** Update the format matrix in `README.md`.

## Before sending a pull request

```console
make lint test
```

CI runs the tests on all five target platforms, plus gofmt, go vet,
golangci-lint, govulncheck and the race detector.
