alter table runs add column if not exists acp_connection_id text;

create table if not exists agent_routing_rules (
    id text primary key,
    tenant_id text not null,
    priority integer not null,
    enabled boolean not null default true,
    condition_json jsonb not null default '{}'::jsonb,
    agent_profile_id text not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index if not exists idx_agent_routing_rules_lookup
    on agent_routing_rules (tenant_id, enabled, priority, id);

create table if not exists agent_route_overrides (
    id text primary key,
    tenant_id text not null,
    channel_type text not null,
    surface_key text not null,
    owner_user_id text not null,
    agent_profile_id text not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (tenant_id, channel_type, surface_key, owner_user_id)
);
