-- +goose Up
-- Shop timezone: every "today / week / month" calculation uses it, never UTC.
alter table shops add column timezone text not null default 'Asia/Kolkata';

create table categories (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  key text check (key in ('chicken','fish','eggs','mutton','dairy')),  -- null for custom
  name text not null,
  name_norm text not null,
  kind text not null check (kind in ('standard','custom')),
  created_at timestamptz not null default now(),
  unique (shop_id, name_norm)
);

-- items / item_prices are the "price items" and "price history": today's price is the row for
-- today, yesterday's is the latest row before today.
alter table items add column category_id uuid references categories(id);
alter table items add column name_norm text not null default '';
create unique index items_shop_category_name on items (shop_id, category_id, name_norm)
  where active and category_id is not null and name_norm <> '';
create index items_shop_active on items (shop_id) where active;
create index item_prices_day on item_prices (day);

-- Same Idempotency-Key + same shop returns the first (successful) response.
create table idempotency_keys (
  shop_id uuid not null references shops(id) on delete cascade,
  key text not null,
  method text not null,
  path text not null,
  status int not null,
  response text not null,
  created_at timestamptz not null default now(),
  primary key (shop_id, key)
);

-- WhatsApp messages waiting to be sent by a worker.
create table outbox (
  id bigserial primary key,
  shop_id uuid not null references shops(id) on delete cascade,
  kind text not null,
  payload jsonb not null,
  status text not null default 'pending',
  created_at timestamptz not null default now()
);
create index outbox_pending on outbox (status, created_at);

alter table categories enable row level security;
alter table idempotency_keys enable row level security;
alter table outbox enable row level security;

-- +goose Down
drop table if exists outbox;
drop table if exists idempotency_keys;
drop index if exists item_prices_day;
drop index if exists items_shop_active;
drop index if exists items_shop_category_name;
alter table items drop column if exists name_norm;
alter table items drop column if exists category_id;
drop table if exists categories;
alter table shops drop column if exists timezone;
