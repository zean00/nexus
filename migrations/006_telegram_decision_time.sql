alter table telegram_user_access
    add column if not exists decided_at timestamptz;

update telegram_user_access
set decided_at = coalesce(decided_at, updated_at)
where status in ('approved', 'denied');

create index if not exists idx_telegram_user_access_decided
    on telegram_user_access (tenant_id, status, decided_at desc, telegram_user_id asc);
