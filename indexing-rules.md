---
title: AI Memory Indexing Rules
description: These rules define how Codex and Claude should store knowledge in db.sqlite.
type: architecture_note
status: active
index: false
canonical_key: ai-memory:indexing-rules:v4
tags:
- ai-memory
- sqlite
- indexing
- statements
- architecture_note
---
# AI Memory Indexing Rules

These rules define how Codex and Claude synchronize Markdown knowledge into `db.sqlite`.

Markdown is the canonical source for durable knowledge. SQLite is a derived, searchable index that can be rebuilt with the `--full` command of `ai-memory/sync-index`.

The model must stay universal. Do not create different schemas for commands, specs, tasks, reviews, cold emails, incidents, or code fixes.

## Core Model

Use only this durable structure for new memory:

```text
memory_items
memory_statements
memory_statement_evidence
memory_item_relations
memory_tags
memory_item_tags
memory_items_fts
memory_statements_fts
```

`memory_items` stores the object.

`memory_statements` stores the object's meaning as literal statements.

`memory_statement_evidence` stores proof.

## Universal Statement Shape

Every detailed memory row uses the same shape:

```text
statement_role
subject
predicate
object
details_md
scope
value_kind
typed value columns
status
scores
```

This is the only structure for details.

Examples:

```text
subject: Acme production deploy
predicate: uses command
object: make production-deploy
```

```text
subject: Stripe checkout expired cleanup
predicate: does not apply when
object: upgradeTrial
```

```text
subject: Acme beta outreach
predicate: has message
object: <email body or short label>
details_md: full email body
```

```text
subject: PR 123
predicate: has review finding
object: Missing test for expired checkout cleanup
```

The same columns work for every topic.

## Allowed Statement Roles

Use one of:

```text
identity
context
observation
problem
cause
diagnostic
decision
action
procedure
result
constraint
risk
evidence
todo
message
review
reference
note
```

These roles are universal. They are not tied to a specific item type.

## Item Types

`item_type` is only a coarse label for retrieval.

Allowed examples:

```text
session
project_context
command
runbook
spec
module_doc
task
todo
task_report
pull_request
review
decision
fact
incident
code_fix
solution_pattern
cold_email
campaign
marketing_asset
source
reference
personal_context
learning_note
machine_note
```

Do not add a new table for a new type.

If a new concept appears, store it as a `memory_items` row and describe it through `memory_statements`.

## Folder To Item Type Mapping

Use this mapping by default:

```text
projects/*/context.md      -> project_context
projects/*/specs/          -> spec | module_doc
projects/*/operations/     -> command | runbook
projects/*/tasks/          -> task | todo | task_report | pull_request | review
projects/*/decisions/      -> decision
projects/*/incidents/      -> incident | code_fix
projects/*/marketing/      -> cold_email | campaign | marketing_asset
projects/*/resources/      -> source | reference
knowledge/investments/    -> source | reference | learning_note
knowledge/business/       -> marketing_guide | campaign | marketing_asset | learning_note
knowledge/saas/           -> reference | architecture_note | learning_note
knowledge/it/             -> architecture_note | operational_guide | incident | code_fix | reference
self/                     -> personal_context
Clippings/                -> reference (draft + inbox)
```

Avoid new notes under vague folders such as `details/` when a more precise folder exists.

## Source Of Truth

Markdown is canonical for personal context, projects, knowledge, resources, decisions and instructions.

SQLite is a derived retrieval layer. It may contain machine-only session rows, but those rows must never override a source-backed Markdown item.

If Markdown and SQLite disagree:

1. Prefer source-backed evidence.
2. Prefer the newest trusted item.
3. Prefer the Markdown source when it has a stable `canonical_key`.
4. Mark older conflicting items as `status = obsolete`.
5. Do not delete by default.

## Required Markdown Frontmatter

Every indexable Markdown file should include:

```md
---
title: Paiement Stripe de Acme
description: Spécification du flux de paiement et de ses contraintes.
type: spec
project: acme
area: billing
status: active
index: true
canonical_key: spec:acme:billing-stripe-checkout
tags:
  - acme
  - billing
  - stripe
---
```

Required:

- `title`
- `description`
- `type`
- `status`
- `index`
- `canonical_key`
- `tags`

Recommended:

- `project`
- `area`
- `subtype`

Allowed lifecycle statuses:

```text
draft
active
archived
obsolete
```

`Clippings/` is the Obsidian Web Clipper inbox. Notes in this folder use `status: draft`, `processing: inbox`, and remain searchable until they are qualified and moved.

`self/private.md` and this indexing-rules document use `index: false`.

## Synchronization

Use the shared script:

```text
./ai-memory/sync-index --changed
./ai-memory/sync-index --full
./ai-memory/sync-index --check
```

On Windows, use `.\ai-memory\sync-index.exe`.

Rules:

1. Upsert by `canonical_key`, then by `source_path` as a fallback.
2. Preserve the existing item ID when a file moves.
3. Store a semantic content hash to skip unchanged notes.
4. Mark missing or explicitly non-indexed sources `obsolete`; never delete them automatically.
5. Preserve existing statements for unchanged notes.
6. Mark statements obsolete when their Markdown source changes semantically.
7. Update FTS and tags in the same transaction as the item.
8. Treat index failures as non-blocking in hooks, but record them in `ai-memory/logs/index-errors.log`.

If `index: false`, do not insert into `memory_items`.

## Examples

### Command

Item:

```text
item_type: command
item_subtype: make
project: acme
area: deploy
title: Déployer Acme en production
body_md: make production-deploy
canonical_key: command:acme:production-deploy
```

Statements:

```text
role=context
subject=Acme production deploy
predicate=uses command
object=make production-deploy
```

```text
role=context
subject=Acme production deploy
predicate=runs in directory
object=/srv/acme/docker_prod
```

```text
role=context
subject=Acme production deploy
predicate=targets environment
object=production
```

```text
role=risk
subject=Acme production deploy
predicate=has risk level
object=high
```

```text
role=reference
subject=production_deploy
predicate=is alias of
object=make production-deploy
```

### General Solution

Item:

```text
item_type: solution_pattern
project: null
title: Nettoyer une entité draft après expiration Stripe Checkout
```

Statements:

```text
role=problem
subject=Stripe checkout expired cleanup
predicate=has symptom
object=Draft entity remains after checkout expiry
```

```text
role=cause
subject=Stripe checkout expired cleanup
predicate=has root cause
object=Cleanup did not distinguish create from upgrade/resubscribe
```

```text
role=action
subject=Stripe checkout expired cleanup
predicate=fixes by
object=Delete only draft entities created by mode=create
```

```text
role=context
subject=Stripe checkout expired cleanup
predicate=applies to
object=Laravel Stripe Checkout webhook
```

```text
role=constraint
subject=Stripe checkout expired cleanup
predicate=does not apply when
object=upgradeTrial
```

This kind of item belongs in the most relevant domain under `knowledge/` when it is useful beyond one project, for example `knowledge/it/software-development/` for a reusable software fix.

It does not need to be proven in multiple projects. It must simply be clear about scope, constraints, and evidence.

### Spec

```text
role=context
subject=Stripe Checkout billing flow
predicate=receives input
object=checkout.session.completed
```

```text
role=result
subject=Stripe Checkout billing flow
predicate=produces output
object=agency.stripe_id
```

```text
role=constraint
subject=Stripe Checkout billing flow
predicate=must preserve
object=Draft cleanup applies only to mode=create
```

### Task And Todo

```text
role=todo
subject=Maintenance mode task
predicate=requires
object=SPA displays a clear 503 screen
```

```text
role=result
subject=Maintenance mode task
predicate=is done when
object=Tests and manual verification passed
```

Todos can also be separate `item_type = todo` items linked with:

```text
task -> has_todo -> todo
```

### Cold Email

```text
role=message
subject=Acme beta outreach
predicate=has subject
object=<email subject line>
```

```text
role=message
subject=Acme beta outreach
predicate=has message
object=beta outreach email body
details_md=<full email body>
```

```text
role=context
subject=Acme beta outreach
predicate=targets recipient type
object=<recipient segment>
```

## Relations

Use `memory_item_relations` for graph links between items.

Recommended relations:

```text
has_todo
has_report
implemented_by
has_review
describes
fixes
caused_by
reused_solution
applies_to
supersedes
derived_from
```

Examples:

```text
task -> has_todo -> todo
task -> has_report -> task_report
task -> implemented_by -> pull_request
pull_request -> has_review -> review
solution_pattern -> fixes -> incident
project task -> reused_solution -> solution_pattern
spec -> describes -> module
command -> applies_to -> project
```

## Retrieval Rules

When answering a technical question:

1. Search `memory_items_fts`.
2. Search `memory_statements_fts`.
3. Filter by `status = active` by default; include `draft` for inbox triage and `archived` only when history is requested.
4. Prefer exact `project` matches.
5. Also search global `solution_pattern`, `code_fix`, `incident`, `spec`, `operational_guide`, `marketing_guide`, and `command`.
6. Prefer `confidence_score >= 7`.
7. Cite `memory_statement_evidence` when available.

For command questions:

1. Search `item_type = command`.
2. Search statements where `predicate` includes command or alias meaning.
3. Return canonical command, working directory, risk level, and evidence.
4. If no verified command exists, say no verified command was found.

For similar bug questions:

1. Search current project first.
2. Search global `solution_pattern`, `incident`, and `code_fix`.
3. Present similar fixes as reusable solutions, not guaranteed copy-paste fixes.
4. Include constraints and scope.

## Scoring Rules

Use:

```text
relevant_score 0-10
confidence_score 0-10
```

Guidelines:

- `9-10`: verified, source-backed, directly useful.
- `7-8`: useful and likely correct, but may need context.
- `5-6`: keep but do not prioritize.
- `<5`: mark `status = obsolete` or do not index.

Do not delete low-score items automatically.

## Insert Rules

For every indexed Markdown or HTML item, `ai-memory/sync-index`:

1. Insert or update `memory_items`.
2. Insert or replace the corresponding row in `memory_items_fts`.
3. Add tags through `memory_tags` and `memory_item_tags`.
4. Preserve existing statement and evidence rows while the semantic source hash is unchanged.
5. Mark statements obsolete if their source changes; statement extraction remains an explicit semantic operation.

When an assistant extracts durable literal statements, it must then:

1. Insert them into `memory_statements`.
2. Insert proof into `memory_statement_evidence`.
3. Insert search rows into `memory_statements_fts`.
4. Add relations through `memory_item_relations` when needed.

Do not use free-form `data_json` for new memory.

Do not create per-topic schemas.
