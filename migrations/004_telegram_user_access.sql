create table if not exists telegram_user_access (
    tenant_id text not null,
    telegram_user_id text not null,
    display_name text not null default '',
    allowed boolean not null default true,
    added_by text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    primary key (tenant_id, telegram_user_id)
);

create index if not exists idx_telegram_user_access_allowed on telegram_user_access (tenant_id, allowed, updated_at desc);
