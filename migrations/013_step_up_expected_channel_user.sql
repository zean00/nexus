alter table step_up_challenges
    add column if not exists expected_channel_user_id text not null default '';

