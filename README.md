---
title: Index SQLite du SECOND_BRAIN
description: Commandes et comportement de l'index dérivé utilisé par Claude Code et Codex.
type: architecture_note
status: active
index: true
canonical_key: ai-memory:readme
tags:
  - ai-memory
  - sqlite
  - retrieval
---

# Index SQLite du SECOND_BRAIN

`db.sqlite` accélère la recherche, mais les fichiers Markdown restent la source canonique. Les documents HTML du vault sont également indexés pour la recherche : l'indexeur extrait leur texte visible et ne conserve ni CSS, ni JavaScript, ni commentaires HTML.

Chaque synchronisation indexe aussi les liens Obsidian `[[...]]` entre les notes dans `memory_item_relations` (`relation = wikilink`). Ces relations rendent le contexte et les sources associées interrogeables sans modifier les fichiers Markdown. La recherche essaie d'abord tous les termes ensemble ; sans résultat, elle réessaie avec les termes séparés afin de tolérer une formulation voisine, lorsque les mêmes faits sont formulés différemment. Elle élimine aussi les résultats dont le contenu indexé est identique, afin qu'une copie locale du même document ne monopolise pas les premiers résultats.

L'indexeur est un binaire Go autonome pour macOS, Linux et Windows. Il indexe les fichiers `.md` avec leur frontmatter et les `.html` avec des métadonnées inférées (titre, description et projet). Il embarque SQLite avec FTS5 et ne demande ni Python, ni serveur, ni installation SQLite séparée.

## Commandes

```bash
# Installer ou actualiser les hooks avec les chemins de cet ordinateur
./ai-memory/sync-index --install-hooks

# Synchroniser uniquement ce qui a changé
./ai-memory/sync-index --changed

# Rescanner toutes les notes et reconstruire leur entrée FTS
./ai-memory/sync-index --full

# Vérifier SQLite, la couverture des notes et les clés canoniques
./ai-memory/sync-index --check

# Rechercher les notes actives et les brouillons (FTS5)
./ai-memory/sync-index --search 'deploy OR runbook' --limit 10

# Marquer l'index comme sale après une écriture
./ai-memory/sync-index --mark-dirty

# Synchroniser seulement si une écriture l'a marqué comme sale
./ai-memory/sync-index --if-dirty
```

Sous Windows :

```powershell
.\ai-memory\sync-index.exe --install-hooks
.\ai-memory\sync-index.exe --full
.\ai-memory\sync-index.exe --check
```

## Compiler depuis les sources

Le code du programme reste volontairement dans un seul fichier, `src/main.go`. Go 1.25 ou plus est nécessaire uniquement pour compiler :

```bash
cd ai-memory/src
go build -trimpath -ldflags="-s -w" -o ../sync-index .
```

Sous Windows :

```powershell
Set-Location ai-memory\src
go build -trimpath -ldflags="-s -w" -o ..\sync-index.exe .
```

Le pilote `modernc.org/sqlite` est sans CGO : aucun compilateur C n'est nécessaire.

## Hooks

- `SessionStart` : synchronisation incrémentale, notamment pour récupérer les nouveaux Clippings.
- `UserPromptSubmit` : seconde vérification des changements externes avant une recherche.
- `PostToolUse` : marque l'index comme sale après une écriture.
- `Stop` : synchronise seulement si l'index est sale.

Claude Code et Codex appellent le même binaire détecté pendant `--install-hooks`. SQLite sérialise lui-même les écritures concurrentes avec une transaction `BEGIN IMMEDIATE` et un délai d'attente de 30 secondes.

Si Obsidian Web Clipper crée une note avec seulement `title`, `source`, `author`, `published`, `created`, `description` et `tags`, la synchronisation complète automatiquement les propriétés normalisées de cette note dans `Clippings/`. Le corps capturé reste inchangé.

## Fichiers générés

- `.index-dirty` : marqueur temporaire, retiré après une synchronisation réussie.
- `logs/index-errors.log` : erreurs non bloquantes rencontrées par un hook. Le dossier `logs/` est créé à la première erreur.
- `backups/` : dumps SQLite antérieurs à une refonte de schéma.

## Arborescence

```text
ai-memory/
  schema/schema.sql   schéma SQLite appliqué à chaque synchronisation
  src/main.go         source de l'indexeur
  logs/               erreurs de hook, créé à la demande
  backups/            dumps antérieurs à une refonte de schéma
  sync-index          binaire compilé
```

`schema/schema.sql` sert aussi de marqueur pour localiser la racine du vault quand `SECOND_BRAIN_ROOT` n'est pas défini.

Ces fichiers ne sont pas des sources de connaissance, ne doivent pas être indexés et restent hors du dépôt Git.
