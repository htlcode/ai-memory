PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS memory_items (
  id TEXT PRIMARY KEY,

  item_type TEXT NOT NULL,
  item_subtype TEXT,

  project TEXT,
  area TEXT,
  title TEXT NOT NULL,
  summary TEXT,
  body_md TEXT,

  status TEXT NOT NULL DEFAULT 'active',
  priority TEXT,
  canonical_key TEXT UNIQUE,

  source_tool TEXT,
  source_path TEXT,
  source_hash TEXT,
  source_line_start INTEGER,
  source_line_end INTEGER,
  source_page INTEGER,
  source_url TEXT,
  repo_path TEXT,

  relevant_score INTEGER CHECK (relevant_score BETWEEN 0 AND 10),
  confidence_score INTEGER CHECK (confidence_score BETWEEN 0 AND 10),
  tags_json TEXT,

  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  indexed_at TEXT,
  archived_at TEXT
);

CREATE TABLE IF NOT EXISTS memory_statements (
  id TEXT PRIMARY KEY,
  item_id TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,

  statement_role TEXT NOT NULL DEFAULT 'note' CHECK (
    statement_role IN (
      'identity',
      'context',
      'observation',
      'problem',
      'cause',
      'diagnostic',
      'decision',
      'action',
      'procedure',
      'result',
      'constraint',
      'risk',
      'evidence',
      'todo',
      'message',
      'review',
      'reference',
      'note'
    )
  ),

  subject TEXT NOT NULL,
  predicate TEXT NOT NULL,
  object TEXT,
  details_md TEXT,
  scope TEXT,

  value_kind TEXT NOT NULL DEFAULT 'text' CHECK (
    value_kind IN (
      'text',
      'markdown',
      'number',
      'integer',
      'boolean',
      'date',
      'item_ref'
    )
  ),
  value_text TEXT,
  value_number REAL,
  value_integer INTEGER,
  value_bool INTEGER CHECK (value_bool IN (0, 1)),
  value_date TEXT,
  value_item_id TEXT,

  relevant_score INTEGER CHECK (relevant_score BETWEEN 0 AND 10),
  confidence_score INTEGER CHECK (confidence_score BETWEEN 0 AND 10),
  status TEXT NOT NULL DEFAULT 'active',

  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CHECK (
    (value_text IS NOT NULL) +
    (value_number IS NOT NULL) +
    (value_integer IS NOT NULL) +
    (value_bool IS NOT NULL) +
    (value_date IS NOT NULL) +
    (value_item_id IS NOT NULL) >= 1
  ),

  FOREIGN KEY (item_id) REFERENCES memory_items(id) ON DELETE CASCADE,
  FOREIGN KEY (value_item_id) REFERENCES memory_items(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS memory_statement_evidence (
  id TEXT PRIMARY KEY,
  item_id TEXT NOT NULL,
  statement_id TEXT,

  source_type TEXT NOT NULL DEFAULT 'file',
  source_path TEXT,
  source_url TEXT,
  source_line_start INTEGER,
  source_line_end INTEGER,
  source_page INTEGER,
  quote_md TEXT,
  proof_note TEXT,

  confidence_score INTEGER CHECK (confidence_score BETWEEN 0 AND 10),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,

  FOREIGN KEY (item_id) REFERENCES memory_items(id) ON DELETE CASCADE,
  FOREIGN KEY (statement_id) REFERENCES memory_statements(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS memory_item_relations (
  id TEXT PRIMARY KEY,
  from_item_id TEXT NOT NULL,
  relation TEXT NOT NULL,
  to_item_id TEXT NOT NULL,
  relation_note TEXT,
  confidence_score INTEGER CHECK (confidence_score BETWEEN 0 AND 10),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,

  FOREIGN KEY (from_item_id) REFERENCES memory_items(id) ON DELETE CASCADE,
  FOREIGN KEY (to_item_id) REFERENCES memory_items(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS memory_tags (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL DEFAULT 'general',
  description TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS memory_item_tags (
  item_id TEXT NOT NULL,
  tag_id TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (item_id, tag_id),

  FOREIGN KEY (item_id) REFERENCES memory_items(id) ON DELETE CASCADE,
  FOREIGN KEY (tag_id) REFERENCES memory_tags(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_items_fts USING fts5(
  item_id UNINDEXED,
  item_type,
  item_subtype,
  project,
  area,
  title,
  summary,
  body_md,
  tags
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_statements_fts USING fts5(
  statement_id UNINDEXED,
  item_id UNINDEXED,
  statement_role,
  subject,
  predicate,
  object,
  details_md,
  scope
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_sections_fts USING fts5(
  item_id UNINDEXED,
  position UNINDEXED,
  line_start UNINDEXED,
  line_end UNINDEXED,
  heading_path,
  body_md
);

CREATE INDEX IF NOT EXISTS idx_items_type_project ON memory_items(item_type, project);
CREATE INDEX IF NOT EXISTS idx_items_project_status ON memory_items(project, status);
CREATE INDEX IF NOT EXISTS idx_items_status_score ON memory_items(status, relevant_score);
CREATE INDEX IF NOT EXISTS idx_items_canonical_key ON memory_items(canonical_key);
CREATE INDEX IF NOT EXISTS idx_items_source_path ON memory_items(source_path);
CREATE INDEX IF NOT EXISTS idx_items_source_hash ON memory_items(source_hash);

CREATE INDEX IF NOT EXISTS idx_statements_item ON memory_statements(item_id, statement_role);
CREATE INDEX IF NOT EXISTS idx_statements_subject ON memory_statements(subject);
CREATE INDEX IF NOT EXISTS idx_statements_predicate ON memory_statements(predicate);
CREATE INDEX IF NOT EXISTS idx_statements_value_item ON memory_statements(value_item_id);
CREATE INDEX IF NOT EXISTS idx_statements_status_score ON memory_statements(status, relevant_score);

CREATE INDEX IF NOT EXISTS idx_statement_evidence_item ON memory_statement_evidence(item_id);
CREATE INDEX IF NOT EXISTS idx_statement_evidence_statement ON memory_statement_evidence(statement_id);
CREATE INDEX IF NOT EXISTS idx_statement_evidence_source ON memory_statement_evidence(source_path);

CREATE INDEX IF NOT EXISTS idx_item_relations_from ON memory_item_relations(from_item_id, relation);
CREATE INDEX IF NOT EXISTS idx_item_relations_to ON memory_item_relations(to_item_id, relation);
CREATE INDEX IF NOT EXISTS idx_tags_name ON memory_tags(name);
CREATE INDEX IF NOT EXISTS idx_item_tags_tag ON memory_item_tags(tag_id);

CREATE VIEW IF NOT EXISTS v_memory_commands AS
SELECT * FROM memory_items WHERE item_type = 'command';

CREATE VIEW IF NOT EXISTS v_memory_tasks AS
SELECT * FROM memory_items WHERE item_type IN ('task', 'todo');

CREATE VIEW IF NOT EXISTS v_memory_specs AS
SELECT * FROM memory_items WHERE item_type IN ('spec', 'module_doc');

CREATE VIEW IF NOT EXISTS v_memory_solutions AS
SELECT * FROM memory_items WHERE item_type IN ('solution_pattern', 'code_fix', 'incident', 'operational_guide', 'marketing_guide');

CREATE VIEW IF NOT EXISTS v_ai_sessions AS
SELECT * FROM memory_items WHERE item_type = 'session';
