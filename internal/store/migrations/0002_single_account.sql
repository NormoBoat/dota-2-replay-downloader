-- One own account without proxies: no stored password, GC journal without route.
ALTER TABLE steam_accounts ALTER COLUMN password_enc DROP NOT NULL;
ALTER TABLE gc_requests ALTER COLUMN proxy_id DROP NOT NULL;
