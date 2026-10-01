-- 0003_move_connections_to_file: the connection graph left SQLite.
--
-- accounts / bots / bindings / listeners / endpoints now live in connect.json,
-- which the hub generates and the WebUI edits. Two reasons:
--
--   * a connection set is a document an operator reviews, diffs and copies
--     between deployments; a table is none of those things;
--   * the relay reads its routing data from a file, so a damaged management
--     database degrades logging instead of taking the data plane down.
--
-- The migration is destructive by design: the data it drops was always
-- recreatable from the WebUI, and keeping two sources of truth for the same
-- connection would be far worse than re-entering a few entries. The file is
-- seeded from these tables once, before this runs (see store.ExportConnections).
DROP TABLE IF EXISTS bindings;
DROP TABLE IF EXISTS listeners;
DROP TABLE IF EXISTS endpoints;
DROP TABLE IF EXISTS bots;
DROP TABLE IF EXISTS accounts;
