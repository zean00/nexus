create table if not exists retention_policies (
    tenant_id text primary key,
    enabled boolean,
    payload_days integer,
    artifact_days integer,
    audit_days integer,
    relational_grace_days integer,
    updated_at timestamptz not null default now()
);

alter table messages
    add column if not exists payload_purged_at timestamptz;

alter table outbound_deliveries
    add column if not exists payload_purged_at timestamptz;

alter table outbox_events
    add column if not exists payload_purged_at timestamptz;

alter table awaits
    add column if not exists payload_purged_at timestamptz;

alter table await_responses
    add column if not exists payload_purged_at timestamptz;

alter table artifacts
    alter column storage_uri drop not null;

alter table artifacts
    add column if not exists blob_purged_at timestamptz;

create index if not exists idx_messages_retention on messages (tenant_id, created_at, payload_purged_at);
create index if not exists idx_deliveries_retention on outbound_deliveries (tenant_id, created_at, payload_purged_at);
create index if not exists idx_outbox_retention on outbox_events (tenant_id, available_at, payload_purged_at);
create index if not exists idx_awaits_retention on awaits (expires_at, payload_purged_at);
create index if not exists idx_await_responses_retention on await_responses (accepted_at, payload_purged_at);
create index if not exists idx_artifacts_retention on artifacts (created_at, blob_purged_at);
create index if not exists idx_audit_events_retention on audit_events (tenant_id, created_at);
