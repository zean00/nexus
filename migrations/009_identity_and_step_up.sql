create table if not exists users (
    id text primary key,
    tenant_id text not null,
    primary_email text not null,
    primary_email_verified boolean not null default true,
    last_step_up_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (tenant_id, primary_email)
);

create table if not exists linked_identities (
    id text primary key,
    tenant_id text not null,
    user_id text not null references users(id) on delete cascade,
    channel_type text not null,
    channel_user_id text not null,
    status text not null default 'linked',
    linked_at timestamptz not null default now(),
    last_verified_at timestamptz,
    unique (tenant_id, channel_type, channel_user_id)
);

create index if not exists idx_linked_identities_user
    on linked_identities (tenant_id, user_id, channel_type);

create table if not exists step_up_challenges (
    id text primary key,
    tenant_id text not null,
    user_id text not null references users(id) on delete cascade,
    purpose text not null,
    channel_type text not null default '',
    code_hash text not null,
    expires_at timestamptz not null,
    consumed_at timestamptz,
    created_at timestamptz not null default now()
);

create index if not exists idx_step_up_challenges_lookup
    on step_up_challenges (tenant_id, user_id, purpose, channel_type, created_at desc);
