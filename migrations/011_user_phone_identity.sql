alter table users
	add column if not exists primary_phone text,
	add column if not exists primary_phone_normalized text,
	add column if not exists primary_phone_verified boolean not null default false,
	add column if not exists primary_phone_added_at timestamptz;

create index if not exists idx_users_tenant_phone_normalized
	on users (tenant_id, primary_phone_normalized)
	where primary_phone_normalized is not null and primary_phone_normalized <> '';
