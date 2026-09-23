-- Subset of the ZCode CLI database schema (~\.zcode\cli\db\db.sqlite,
-- ZCode 3.1.x, CLI 0.14.x): only the tables and columns Athar reads, with
-- the column order and types of the real schema. Synthetic test data only.
CREATE TABLE session (
  id text primary key,
  project_id text not null,
  workspace_id text,
  parent_id text,
  slug text not null,
  directory text not null,
  path text,
  title text not null,
  version text not null,
  time_created integer not null,
  time_updated integer not null,
  time_archived integer,
  task_type text not null default 'interactive',
  title_source text not null default 'first_input'
    check(title_source in ('default', 'first_input', 'generated', 'custom')),
  trace_id text
);
CREATE TABLE message (
  id text primary key,
  session_id text not null references session(id) on delete cascade,
  time_created integer not null,
  time_updated integer not null,
  data text not null
);
CREATE INDEX message_session_time_created_id_idx ON message(session_id, time_created, id);
CREATE TABLE part (
  id text primary key,
  message_id text not null references message(id) on delete cascade,
  session_id text not null,
  time_created integer not null,
  time_updated integer not null,
  data text not null
);
CREATE INDEX part_message_id_id_idx ON part(message_id, id);
CREATE TABLE input_history (
  id text primary key,
  project_id text not null,
  session_id text,
  text text not null,
  kind text not null,
  time_created integer not null,
  attachments text
);
