create table if not exists schema_migrations (
    version text primary key,
    applied_at timestamptz not null default now()
);

create table if not exists artifacts (
    id text primary key,
    message_id text references messages(id),
    direction text not null,
    name text not null,
    mime_type text not null,
    size_bytes bigint not null,
    sha256 text not null,
    storage_uri text not null,
    created_at timestamptz not null
);

create index if not exists idx_artifacts_message on artifacts (message_id);
