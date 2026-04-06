create table if not exists inbound_receipts (
    id text primary key,
    tenant_id text not null,
    channel_type text not null,
    provider_event_id text not null,
    provider_delivery_id text not null,
    interaction_type text not null,
    actor_channel_user_id text not null,
    first_seen_at timestamptz not null,
    last_seen_at timestamptz not null,
    duplicate_count integer not null default 0,
    status text not null,
    unique (tenant_id, channel_type, provider_event_id)
);

create table if not exists sessions (
    id text primary key,
    tenant_id text not null,
    owner_user_id text,
    agent_profile_id text,
    channel_type text not null,
    channel_scope_key text not null,
    acp_connection_id text,
    acp_server_url text,
    acp_agent_name text,
    acp_session_id text,
    mode text not null,
    state text not null,
    last_active_at timestamptz not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index if not exists idx_sessions_lookup on sessions (tenant_id, channel_type, channel_scope_key);
create index if not exists idx_sessions_owner on sessions (tenant_id, owner_user_id);

create table if not exists messages (
    id text primary key,
    tenant_id text not null,
    session_id text not null references sessions(id),
    direction text not null,
    channel_type text not null,
    channel_message_id text not null,
    role text not null,
    text_preview text not null,
    raw_payload_json jsonb not null,
    created_at timestamptz not null
);

create table if not exists session_queue_items (
    id text primary key,
    tenant_id text not null,
    session_id text not null references sessions(id),
    inbound_message_id text not null references messages(id),
    queue_position integer not null,
    status text not null,
    route_decision_json jsonb not null,
    enqueued_at timestamptz not null,
    started_at timestamptz,
    completed_at timestamptz,
    expires_at timestamptz not null
);

create index if not exists idx_queue_scan on session_queue_items (session_id, status, queue_position);

create table if not exists runs (
    id text primary key,
    session_id text not null references sessions(id),
    acp_run_id text not null,
    agent_name text not null,
    mode text not null,
    status text not null,
    started_at timestamptz not null,
    completed_at timestamptz,
    last_event_at timestamptz not null
);

create table if not exists awaits (
    id text primary key,
    run_id text not null references runs(id),
    session_id text not null references sessions(id),
    channel_type text not null,
    status text not null,
    schema_json jsonb not null,
    prompt_render_model_json jsonb not null,
    allowed_responder_ids_json jsonb not null default '[]'::jsonb,
    expires_at timestamptz not null,
    resolved_at timestamptz
);

create table if not exists await_responses (
    id text primary key,
    await_id text not null references awaits(id),
    actor_channel_user_id text not null,
    actor_identity_assurance text not null,
    response_payload_json jsonb not null,
    idempotency_key text not null,
    accepted_at timestamptz not null,
    rejected_reason text not null,
    unique (await_id, idempotency_key)
);

create table if not exists outbound_deliveries (
    id text primary key,
    tenant_id text not null,
    session_id text not null references sessions(id),
    run_id text not null,
    await_id text,
    logical_message_id text not null,
    channel_type text not null,
    delivery_kind text not null,
    provider_message_id text not null,
    provider_request_id text not null,
    status text not null,
    attempt_count integer not null default 0,
    last_error text not null,
    payload_json jsonb not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create table if not exists outbox_events (
    id text primary key,
    tenant_id text not null,
    event_type text not null,
    aggregate_type text not null,
    aggregate_id text not null,
    idempotency_key text not null unique,
    payload_json jsonb not null,
    status text not null,
    available_at timestamptz not null,
    claimed_at timestamptz,
    processed_at timestamptz,
    attempt_count integer not null default 0,
    last_error text
);

create index if not exists idx_outbox_scan on outbox_events (status, available_at);
