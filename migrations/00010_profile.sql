-- +goose Up
-- Per-user profile (auth.users belongs to Supabase, so our own columns live here).
create table profiles (
  user_id uuid primary key references auth.users(id) on delete cascade,
  name text,
  photo_url text,
  language text not null default 'en' check (language in ('en','hi','or','hinglish')),
  updated_at timestamptz not null default now()
);

-- Staff: a removed member keeps the row (status='removed'); invites wait in staff_invites
-- until that phone logs in (members.user_id is part of the key, so it cannot be null).
alter table members add column status text not null default 'active' check (status in ('active','removed'));
alter table members add column name text;
alter table members add column invited_by uuid;

create table staff_invites (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  phone text not null,                 -- 10 digits
  name text,
  role text not null check (role in ('cashier','farm_manager')),
  invited_by uuid,
  created_at timestamptz not null default now(),
  unique (shop_id, phone)
);

-- Turning a location off is a soft delete: bills, stock and closings keep pointing at it.
alter table locations add column active boolean not null default true;

create table offers (
  shop_id uuid primary key references shops(id) on delete cascade,
  enabled boolean not null default false,
  type text not null default 'discount' check (type in ('discount','points')),
  discount_amount int not null default 20,
  min_bill int not null default 500,
  product text,
  multiplier int not null default 2,
  updated_at timestamptz not null default now()
);

create table subscriptions (
  shop_id uuid primary key references shops(id) on delete cascade,
  plan text not null default 'trial' check (plan in ('trial','basic','pro')),
  trial_ends_at timestamptz,
  current_period_end timestamptz,
  status text not null default 'active'
);
-- Every existing shop starts a 14 day trial from now.
insert into subscriptions (shop_id, plan, trial_ends_at)
  select id, 'trial', now() + interval '14 days' from shops on conflict do nothing;

create table device_status (
  shop_id uuid not null references shops(id) on delete cascade,
  device text not null check (device in ('whatsapp','printer','scale')),
  connected boolean not null default false,
  details jsonb not null default '{}',
  updated_at timestamptz not null default now(),
  primary key (shop_id, device)
);

create table backups (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  status text not null default 'queued' check (status in ('queued','done','failed')),
  file_url text,
  created_at timestamptz not null default now()
);
create index backups_shop on backups (shop_id, created_at desc);

create table data_exports (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  status text not null default 'queued' check (status in ('queued','ready','failed')),
  file_url text,                       -- storage path, signed on read
  created_at timestamptz not null default now()
);
create index data_exports_shop on data_exports (shop_id, created_at desc);
create index members_user on members (user_id);

alter table profiles enable row level security;
alter table staff_invites enable row level security;
alter table offers enable row level security;
alter table subscriptions enable row level security;
alter table device_status enable row level security;
alter table backups enable row level security;
alter table data_exports enable row level security;

-- +goose Down
drop table if exists data_exports, backups, device_status, subscriptions, offers, staff_invites, profiles;
alter table locations drop column if exists active;
alter table members drop column if exists invited_by, drop column if exists name, drop column if exists status;
