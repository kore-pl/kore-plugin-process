# kore-plugin-process (Plugin für Prozessverwaltung)

## Was es tut

Liest echte Linux-/proc-Daten und bietet Prozessliste, Detailansicht, Signalversand, Prozessbaum und Nice-Wert-Anpassung. Produktionsreif. kill und renice teilen sich eine Prozessschutzliste. process.list berechnet cpu_pct aus einem internen Tick-Snapshot ohne Hintergrund-Ticker (G-19).

Nur-Lese-Methoden deklarieren system:read; schreibende Methoden deklarieren system:admin.

## Wichtige Punkte

- Methoden: kore.ping, kore.reload, kore.shutdown, process.list, process.detail, process.kill, process.renice, process.tree, process.ports, process.history, process.trend, process.watchdog.suggest (vollständige Tabellen in DETAIL.md).
- kill und renice erfordern confirm=true sowie den Namen des Zielprozesses; ein Treffer in der Schutzliste (Selbst, Kernel-Threads, Kern) wird abgelehnt.
- Nur die pluginapi-Abhängigkeit plus Standardbibliothek; stdout für Protokollrahmen reserviert, Logs nach stderr.

## Installation und Prüfung

Installieren Sie `process` über den Plugin-Markt oder importieren Sie das mit make dist erzeugte dist/process-0.3.0.tar.gz. Prüfen Sie mit make check (gofmt, go vet, go test -timeout 200s), einschließlich boundary- und fault-Tests.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
