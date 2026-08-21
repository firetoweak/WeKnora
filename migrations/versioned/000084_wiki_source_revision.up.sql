-- KB-level fingerprint for Wiki source material. Consumers (MCP wiki tools)
-- compare this integer to decide whether a cached analysis is stale.
-- Bumped on wiki ingest/finalize, wiki page content writes, and original
-- chunk body edits — not on bookkeeping-only link decoration.
ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS wiki_source_revision BIGINT NOT NULL DEFAULT 0;
