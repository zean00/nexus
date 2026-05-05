alter table artifacts
    add column if not exists artifact_type text not null default '',
    add column if not exists data_json jsonb not null default '{}'::jsonb;
