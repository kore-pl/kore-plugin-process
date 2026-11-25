# kore-plugin-process (plugin de gestión de procesos)

## Qué hace

Lee datos reales de /proc en Linux y ofrece listado de procesos, detalle por proceso, envío de señales, árbol de procesos y ajuste de nice. Grado de producción. kill y renice comparten una misma lista de protección de procesos. process.list calcula cpu_pct a partir de una instantánea de ticks en el propio proceso, sin ticker en segundo plano (G-19).

Los métodos de solo lectura declaran system:read; los de escritura declaran system:admin.

## Puntos clave

- Métodos: kore.ping, kore.reload, kore.shutdown, process.list, process.detail, process.kill, process.renice, process.tree, process.ports, process.history, process.trend, process.watchdog.suggest (tablas completas en DETAIL.md).
- kill y renice requieren confirm=true y el nombre del proceso destino; si coincide con la lista de protección (uno mismo, hilos del kernel, núcleo) se rechaza.
- Solo la dependencia pluginapi más la biblioteca estándar; stdout reservado a tramas del protocolo, registros a stderr.

## Instalación y verificación

Instale `process` desde el mercado de plugins o importe dist/process-0.3.0.tar.gz generado con make dist. Verifique con make check (gofmt, go vet, go test -timeout 200s), incluidas las suites boundary y fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
