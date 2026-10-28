---
title: Vault SQLite Index
description: Commands and behavior of the derived search index used by AI coding agents.
type: architecture_note
status: active
index: true
canonical_key: ai-memory:readme
tags:
  - ai-memory
  - sqlite
  - retrieval
---

# Vault SQLite Index

`db.sqlite` speeds up search, but the Markdown files remain the canonical source. HTML documents in the vault are indexed too: the indexer extracts their visible text and keeps no CSS, JavaScript or HTML comments.

Each Markdown note is also split by headings (h1 to h3; blocks over 4000 characters are split again) into `memory_sections_fts`. `--search` returns the best section of each file: heading path, start and end lines, excerpt. Read those lines instead of the whole file. HTML notes have no sections. `--check` fails when a non-empty Markdown note has no sections (stale index: run `--full`). A query with a note-only column filter (`project:<name>`) returns `section: null`.

Each sync also indexes Obsidian `[[...]]` links between notes in `memory_item_relations` (`relation = wikilink`). These relations make context and related sources queryable without changing the Markdown files. Search first tries all terms together; with no result, it retries with the terms separated, to tolerate different wording of the same facts. It also drops results whose indexed content is identical, so a local copy of a document does not crowd the top results.

The indexer is a standalone Go binary for macOS, Linux and Windows. It indexes `.md` files with their frontmatter and `.html` files with inferred metadata (title, description and project). It embeds SQLite with FTS5 and needs no Python, server or separate SQLite install.

## Commands

```bash
# Install or refresh the hooks with this machine's paths
./ai-memory/sync-index --install-hooks

# Sync only what changed
./ai-memory/sync-index --changed

# Rescan every note and rebuild its FTS entry
./ai-memory/sync-index --full

# Check SQLite, note coverage and canonical keys
./ai-memory/sync-index --check

# Search active notes and drafts (FTS5)
./ai-memory/sync-index --search 'deploy OR runbook' --limit 10

# Mark the index dirty after a write
./ai-memory/sync-index --mark-dirty

# Sync only if a write marked the index dirty
./ai-memory/sync-index --if-dirty
```

On Windows:

```powershell
.\ai-memory\sync-index.exe --install-hooks
.\ai-memory\sync-index.exe --full
.\ai-memory\sync-index.exe --check
```

## Build from source

The program code deliberately stays in a single file, `src/main.go`. Go 1.25 or later is needed only to build:

```bash
cd ai-memory/src
go build -trimpath -ldflags="-s -w" -o ../sync-index .
```

On Windows:

```powershell
Set-Location ai-memory\src
go build -trimpath -ldflags="-s -w" -o ..\sync-index.exe .
```

Section splitting tests: `cd ai-memory/src && go test ./...`.

The `modernc.org/sqlite` driver is CGO-free: no C compiler is needed.

## Hooks

Hooks keep the index up to date without running commands by hand. `--install-hooks` writes them once into the vault's project settings: `.claude/settings.local.json` for Claude Code and `.codex/hooks.json` for Codex. Both point to this binary's path on the current machine; run it again after moving the vault.

| Event | Command | Effect |
| --- | --- | --- |
| `SessionStart` | `--changed` | Syncs files changed outside the agent (new web clippings, manual edits). |
| `UserPromptSubmit` | `--changed` | Same check before each prompt, so a search sees the latest files. |
| `PostToolUse` (file edit) | `--mark-dirty` | Only flags the index; no sync after each edit. |
| `Stop` | `--if-dirty` | Syncs once at the end of the turn if a file was edited. |

Several agents can run at the same time: SQLite queues their writes (`BEGIN IMMEDIATE`, 30-second wait).

When Obsidian Web Clipper creates a note with only `title`, `source`, `author`, `published`, `created`, `description` and `tags`, the sync fills in the normalized properties of that note in `Clippings/`. The captured body stays unchanged.

## Generated files

- `.index-dirty`: temporary marker, removed after a successful sync.
- `logs/index-errors.log`: non-blocking errors hit by a hook. The `logs/` folder is created on the first error.
- `backups/`: SQLite dumps taken before a schema overhaul.

## Layout

```text
ai-memory/
  schema/schema.sql   SQLite schema applied on every sync
  src/main.go         indexer source
  logs/               hook errors, created on demand
  backups/            dumps taken before a schema overhaul
  sync-index          compiled binary
```

`schema/schema.sql` also serves as a marker to locate the vault root when `SECOND_BRAIN_ROOT` is not set.

These files are not knowledge sources, must not be indexed and stay out of the Git repository.
