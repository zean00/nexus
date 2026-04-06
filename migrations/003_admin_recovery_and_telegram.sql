create extension if not exists pg_trgm;

create table if not exists audit_events (
    id text primary key,
    tenant_id text not null,
    session_id text,
    run_id text,
    await_id text,
    aggregate_type text not null,
    aggregate_id text not null,
    event_type text not null,
    payload_json jsonb not null,
    created_at timestamptz not null default now()
);

create table if not exists channel_surface_state (
    id text primary key,
    tenant_id text not null,
    channel_type text not null,
    surface_key text not null,
    active_session_id text references sessions(id),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (tenant_id, channel_type, surface_key)
);

create table if not exists session_aliases (
    id text primary key,
    tenant_id text not null,
    session_id text not null references sessions(id),
    channel_type text not null,
    surface_key text not null,
    alias text not null,
    created_at timestamptz not null,
    unique (tenant_id, channel_type, surface_key, alias)
);

create index if not exists idx_sessions_admin on sessions (tenant_id, updated_at desc, id desc);
create index if not exists idx_runs_admin on runs (started_at desc, id desc);
create index if not exists idx_runs_session on runs (session_id, started_at desc, id desc);
create index if not exists idx_messages_admin on messages (tenant_id, created_at desc, id desc);
create index if not exists idx_messages_session on messages (session_id, created_at desc, id desc);
create index if not exists idx_artifacts_created on artifacts (created_at desc, id desc);
create index if not exists idx_deliveries_admin on outbound_deliveries (tenant_id, updated_at desc, id desc);
create index if not exists idx_deliveries_session on outbound_deliveries (session_id, updated_at desc, id desc);
create index if not exists idx_awaits_session on awaits (session_id, expires_at desc, id desc);
create index if not exists idx_awaits_run on awaits (run_id, expires_at desc, id desc);
create index if not exists idx_audit_events_lookup on audit_events (tenant_id, created_at desc, id desc);
create index if not exists idx_audit_events_session on audit_events (session_id, created_at desc, id desc);
create index if not exists idx_audit_events_run on audit_events (run_id, created_at desc, id desc);
create index if not exists idx_queue_stuck on session_queue_items (status, started_at, enqueued_at);
create index if not exists idx_outbox_claimed on outbox_events (status, claimed_at, available_at);

create index if not exists idx_messages_text_preview_trgm on messages using gin (text_preview gin_trgm_ops);
create index if not exists idx_artifacts_name_trgm on artifacts using gin (name gin_trgm_ops);
