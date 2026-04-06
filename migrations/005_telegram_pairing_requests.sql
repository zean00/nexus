alter table telegram_user_access
    add column if not exists status text not null default 'approved';

alter table telegram_user_access
    add column if not exists requested_at timestamptz;

create index if not exists idx_telegram_user_access_status on telegram_user_access (tenant_id, status, updated_at desc);
