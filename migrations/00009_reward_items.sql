-- +goose Up
-- Free-item rewards ("1 tray brown eggs = 300 points"). The points rules themselves
-- (spend per point, point value, expiry, max share) already live on shops.
create table reward_items (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  name text not null,
  name_norm text not null,
  points int not null check (points > 0),
  active boolean not null default true,
  created_at timestamptz not null default now()
);
create unique index reward_items_shop_name on reward_items (shop_id, name_norm) where active;
alter table reward_items enable row level security;

-- +goose Down
drop table if exists reward_items;
