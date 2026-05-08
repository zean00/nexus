create table if not exists web_push_subscriptions (
    id text primary key,
    tenant_id text not null,
    user_id text not null,
    acp_session_id text not null,
    session_id text not null references sessions(id),
    endpoint text not null,
    p256dh text not null,
    auth text not null,
    user_agent text not null default '',
    status text not null default 'active',
    fail_count integer not null default 0,
    last_error text not null default '',
    last_seen_at timestamptz not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (tenant_id, endpoint)
);

create index if not exists idx_web_push_subscriptions_user on web_push_subscriptions (tenant_id, user_id, status);
create index if not exists idx_web_push_subscriptions_session on web_push_subscriptions (tenant_id, session_id, status);
create index if not exists idx_web_push_subscriptions_acp on web_push_subscriptions (tenant_id, acp_session_id, status);
