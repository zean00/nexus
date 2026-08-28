-- Tenant registry: one row per laju instance served by this nexus.
-- Tokens:
--   admin_token_hash  sha256 hex of the tenant's bearer token, presented by the
--                     tenant's laju instance when calling the nexus admin API
--                     (verification only → safe to hash).
--   laju_bearer_token plaintext bearer nexus presents to that tenant's laju
--                     inbound endpoint; must equal the instance's NEXUS_TOKEN.
--                     Cannot be hashed because nexus presents (not verifies) it;
--                     treat the column with the same care as an env secret.
create table if not exists tenants (
    tenant_id text primary key,
    display_name text not null default '',
    laju_base_url text not null default '',
    laju_bearer_token text not null default '',
    inbound_webhook_url text not null default '',
    admin_token_hash text not null default '',
    webchat_account_key text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create index if not exists idx_tenants_admin_token_hash
    on tenants (admin_token_hash)
    where admin_token_hash <> '';
