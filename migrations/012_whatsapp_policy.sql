create table if not exists whatsapp_contact_policies (
    tenant_id text not null,
    channel_user_id text not null,
    last_inbound_at timestamptz,
    window_expires_at timestamptz,
    consent_status text not null default 'unknown',
    consent_updated_at timestamptz,
    last_template_sent_at timestamptz,
    last_policy_blocked_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    primary key (tenant_id, channel_user_id),
    check (consent_status in ('unknown', 'opted_in', 'opted_out'))
);

create index if not exists idx_whatsapp_contact_policies_window
    on whatsapp_contact_policies (tenant_id, window_expires_at);

create index if not exists idx_whatsapp_contact_policies_consent
    on whatsapp_contact_policies (tenant_id, consent_status);
