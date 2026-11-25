# kore-plugin-process (плагин управления процессами)

## Что делает плагин

Читает реальные данные Linux /proc и предоставляет список процессов, подробности процесса, отправку сигналов, дерево процессов и изменение значения nice. Производственный уровень. kill и renice используют общий список защиты процессов. process.list вычисляет cpu_pct по внутреннему снимку тиков без фонового тикера (G-19).

Методы только для чтения объявляют system:read; изменяющие методы объявляют system:admin.

## Ключевые моменты

- Методы: kore.ping, kore.reload, kore.shutdown, process.list, process.detail, process.kill, process.renice, process.tree, process.ports, process.history, process.trend, process.watchdog.suggest (полные таблицы в DETAIL.md).
- kill и renice требуют confirm=true и имя целевого процесса; попадание в список защиты (сам плагин, потоки ядра, ядро) отклоняется.
-  Только зависимость pluginapi и стандартная библиотека; stdout зарезервирован для кадров протокола, журналы — в stderr.

## Установка и проверка

Установите `process` из маркетплейса плагинов или импортируйте dist/process-0.3.0.tar.gz, собранный через make dist. Проверка: make check (gofmt, go vet, go test -timeout 200s), включая наборы boundary и fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
