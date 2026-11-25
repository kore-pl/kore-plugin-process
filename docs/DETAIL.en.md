# kore-plugin-process (process management plugin)

## What it does

Reads real Linux /proc data and provides process listing, per-process detail, signal delivery, process trees and nice-value changes. Production grade. kill and renice share one process protection list. process.list computes cpu_pct from an in-process tick snapshot with no background ticker (G-19).

Read-only methods declare system:read; write methods declare system:admin.

## Key points

- Methods: kore.ping, kore.reload, kore.shutdown, process.list, process.detail, process.kill, process.renice, process.tree, process.ports, process.history, process.trend, process.watchdog.suggest (full tables in DETAIL.md).
- kill and renice require confirm=true plus the target process name; hitting the protection list (self, kernel threads, core) is refused.
- Only the pluginapi dependency plus the standard library; stdout is reserved for protocol frames, logs go to stderr.

## Installation and verification

Install `process` from the plugin marketplace or import dist/process-0.3.0.tar.gz built with make dist. Verify with make check (gofmt, go vet, go test -timeout 200s), including boundary and fault suites.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
