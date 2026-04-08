create table if not exists trust_policies (
    tenant_id text not null,
    agent_profile_id text not null,
    require_linked_identity_for_execution boolean not null default false,
    require_linked_identity_for_approval boolean not null default false,
    require_recent_step_up_for_approval boolean not null default false,
    allowed_approval_channels_json jsonb not null default '[]'::jsonb,
    updated_at timestamptz not null default now(),
    primary key (tenant_id, agent_profile_id)
);

alter table awaits
    add column if not exists trust_policy_json jsonb not null default '{}'::jsonb;

alter table await_responses
    add column if not exists actor_user_id text not null default '';

