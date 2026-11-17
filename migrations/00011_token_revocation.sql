-- +goose Up
-- Logout: the hash of a token that must no longer be accepted, kept until the token would expire anyway.
create table revoked_tokens (
  token_hash text primary key,       -- sha256 of the raw bearer token, hex
  user_id uuid not null,
  expires_at timestamptz not null,
  revoked_at timestamptz not null default now()
);
create index revoked_tokens_expires on revoked_tokens (expires_at);
alter table revoked_tokens enable row level security;

-- "What you sell" now hides things on the server, so shops must list every standard category
-- they already have items in (otherwise those items would vanish).
update shops s set categories = (
  select coalesce(array_agg(distinct k), '{}') from (
    select unnest(s.categories) as k
    union
    select i.category from items i where i.shop_id = s.id and i.active and i.category in ('chicken','fish','eggs','mutton','dairy')
  ) t
);

-- +goose Down
drop table if exists revoked_tokens;
