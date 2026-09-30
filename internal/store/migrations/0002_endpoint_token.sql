-- 0002_endpoint_token: outbound connections need a credential.
--
-- The hub must present the token in the clear when it dials, so unlike Bot and
-- account tokens (which are only ever stored hashed) this column holds it in
-- plaintext. Operators who prefer to keep secrets in a file can leave it empty
-- and configure the endpoint in config.yaml instead; the WebUI says so.

ALTER TABLE endpoints ADD COLUMN token TEXT NOT NULL DEFAULT '';
