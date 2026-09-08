# Contributing

Bug reports, small fixes, and clearer documentation are welcome. Start with what
you expected and what happened. For a recovery bug, a small synthetic journal and
a reproducible crash sequence are much more useful than real Discord messages.

Keep changes focused. Core uses the Go standard library; Collector and Core
communicate through files. Changes to recovery, retention, or delivery should
include a test that fails without the fix. Avoid adding services or dependencies
to solve a problem the existing design can handle.

## Local checks

Use Go 1.26+ and Node 22.13+ (native tests use TypeScript stripping).

```sh
go test -count=1 -timeout=60s ./...
go vet ./...
go list -m all
node collector/test/setup_failure_test.mjs
node collector/test/gc_readiness_test.mjs --require-safe
node collector/test/retention_evidence_test.mjs
```

The module listing should contain only `cordbrief`. These Node tests load actual
native/renderer code with synthetic inputs and disposable files; they do not log
into Discord. See [recovery](docs/RECOVERY_CONTRACT.md) for the contract they test.

Run lock and destructive-retention tests on Linux. One option after building the
setup image is the following (POSIX shell, repository root):

```sh
docker run --rm --network none --mount "type=bind,source=$PWD/collector,target=/collector,readonly" --entrypoint node docker-cordbrief-setup:latest /collector/test/retention_gc_test.mjs
```

Use the same container command for `retention_publish_test.mjs` and
`adversarial_lock_test.mjs`. Never mount production volumes into a test container.
The GC test deletes only disposable journals it creates. Test the affected path;
documentation changes do not need a live Discord session.

Please leave credentials, message bodies, session files, and private channel
details out of issues and pull requests. See [security](SECURITY.md).
