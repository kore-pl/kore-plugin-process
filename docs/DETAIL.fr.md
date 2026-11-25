# kore-plugin-process (plugin de gestion des processus)

## Ce que fait le plugin

Lit les vraies données Linux /proc et fournit la liste des processus, le détail d'un processus, l'envoi de signaux, l'arborescence et le réglage de la valeur nice. Qualité production. kill et renice partagent une même liste de protection des processus. process.list calcule cpu_pct à partir d'un instantané de ticks en mémoire, sans ticker d'arrière-plan (G-19).

Les méthodes en lecture seule déclarent system:read ; les méthodes d'écriture déclarent system:admin.

## Points clés

- Méthodes : kore.ping, kore.reload, kore.shutdown, process.list, process.detail, process.kill, process.renice, process.tree, process.ports, process.history, process.trend, process.watchdog.suggest (tableaux complets dans DETAIL.md).
- kill et renice exigent confirm=true ainsi que le nom du processus cible ; une correspondance avec la liste de protection (soi-même, threads noyau, cœur) est refusée.
- Seule la dépendance pluginapi et la bibliothèque standard ; stdout réservé aux trames du protocole, journaux sur stderr.

## Installation et vérification

Installez `process` depuis le marché de plugins ou importez dist/process-0.3.0.tar.gz produit par make dist. Vérifiez avec make check (gofmt, go vet, go test -timeout 200s), y compris les suites boundary et fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
