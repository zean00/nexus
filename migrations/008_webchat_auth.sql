create table if not exists webchat_auth_challenges (
    id text primary key,
    tenant_id text not null,
    email text not null,
    otp_hash text not null,
    link_token_hash text not null unique,
    expires_at timestamptz not null,
    consumed_at timestamptz,
    attempt_count integer not null default 0,
    created_at timestamptz not null default now()
);

create index if not exists idx_webchat_auth_challenges_email
    on webchat_auth_challenges (tenant_id, email, created_at desc);

create table if not exists webchat_auth_sessions (
    id text primary key,
    tenant_id text not null,
    email text not null,
    csrf_token_hash text not null default '',
    expires_at timestamptz not null,
    last_seen_at timestamptz not null default now(),
    created_at timestamptz not null default now()
);

create index if not exists idx_webchat_auth_sessions_email
    on webchat_auth_sessions (tenant_id, email, expires_at desc);
