# kore-plugin-process (plugin de gerenciamento de processos)

## O que faz

Lê dados reais de /proc do Linux e oferece listagem de processos, detalhes por processo, envio de sinais, árvore de processos e ajuste do valor nice. Grau de produção. kill e renice compartilham a mesma lista de proteção de processos. process.list calcula cpu_pct a partir de um snapshot de ticks interno, sem ticker em segundo plano (G-19).

Métodos somente leitura declaram system:read; métodos de escrita declaram system:admin.

## Pontos-chave

- Métodos: kore.ping, kore.reload, kore.shutdown, process.list, process.detail, process.kill, process.renice, process.tree, process.ports, process.history, process.trend, process.watchdog.suggest (tabelas completas em DETAIL.md).
- kill e renice exigem confirm=true e o nome do processo alvo; alvos na lista de proteção (o próprio plugin, threads do kernel, núcleo) são recusados.
- Apenas a dependência pluginapi mais a biblioteca padrão; stdout reservado para quadros do protocolo, logs em stderr.

## Instalação e verificação

Instale `process` no mercado de plugins ou importe dist/process-0.3.0.tar.gz gerado por make dist. Verifique com make check (gofmt, go vet, go test -timeout 200s), incluindo as suítes boundary e fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
