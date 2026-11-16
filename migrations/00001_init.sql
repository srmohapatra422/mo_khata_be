-- +goose Up
create table shops (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  points_per_rupees numeric not null default 10,   -- 1 point per ₹10
  point_value numeric not null default 0.25,       -- 1 point = ₹0.25
  points_expiry_months int default 12,             -- null = never
  max_points_pct int not null default 50,          -- max % of a bill paid by points
  language text not null default 'or',
  created_at timestamptz not null default now()
);

create table members (
  shop_id uuid not null references shops(id) on delete cascade,
  user_id uuid not null references auth.users(id) on delete cascade,
  role text not null check (role in ('owner','cashier','farm_manager')),
  created_at timestamptz not null default now(),
  primary key (shop_id, user_id)
);

create table locations (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  name text not null,
  type text not null check (type in ('farm','shop')),
  created_at timestamptz not null default now()
);

create table items (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  category text not null check (category in ('chicken','fish','mutton','eggs','other')),
  name text not null,                 -- 'Broiler · Boneless', 'Rohu', 'Mutton'
  unit text not null check (unit in ('kg','piece','tray','dozen','bird')),
  aliases text[] not null default '{}',
  active boolean not null default true,
  created_at timestamptz not null default now()
);

create table item_prices (
  item_id uuid not null references items(id) on delete cascade,
  day date not null,
  price numeric(10,2) not null check (price >= 0),
  updated_by uuid,
  updated_at timestamptz not null default now(),
  primary key (item_id, day)
);

create table customers (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  phone text not null,
  name text,
  whatsapp_opt_in boolean not null default false,
  created_at timestamptz not null default now(),
  unique (shop_id, phone)
);

create table bills (
  id uuid primary key,                -- generated on the phone (offline-safe)
  shop_id uuid not null references shops(id),
  location_id uuid not null references locations(id),
  customer_id uuid references customers(id),
  total numeric(12,2) not null,
  points_used int not null default 0,
  points_discount numeric(12,2) not null default 0,
  points_earned int not null default 0,
  payable numeric(12,2) not null,
  payment_mode text not null check (payment_mode in ('cash','upi','card','credit')),
  status text not null default 'saved',
  created_by uuid not null,
  created_at timestamptz not null,    -- time on the phone
  synced_at timestamptz not null default now()
);

create table bill_lines (
  id uuid primary key default gen_random_uuid(),
  bill_id uuid not null references bills(id) on delete cascade,
  item_id uuid references items(id),  -- null for manual entry
  name text not null,
  qty numeric(10,3) not null check (qty > 0),
  rate numeric(10,2) not null check (rate >= 0),
  amount numeric(12,2) not null,
  price_edited boolean not null default false,
  note text
);

create table stock_ledger (
  id bigserial primary key,
  shop_id uuid not null references shops(id),
  location_id uuid not null references locations(id),
  item_id uuid not null references items(id),
  qty numeric(10,3) not null,         -- + in, - out
  kind text not null check (kind in ('purchase','production','transfer','sale','wastage','adjust')),
  ref_id uuid,
  created_by uuid,
  created_at timestamptz not null default now()
);

create table points_ledger (
  id bigserial primary key,
  shop_id uuid not null references shops(id),
  customer_id uuid not null references customers(id),
  points int not null,                -- +48 earned, -400 redeemed
  kind text not null check (kind in ('earn','redeem','expire','reverse','bonus')),
  bill_id uuid references bills(id),
  expires_on date,
  created_at timestamptz not null default now()
);

create index bills_shop_created on bills (shop_id, created_at desc);
create index stock_shop_item on stock_ledger (shop_id, location_id, item_id);
create index points_customer on points_ledger (customer_id);
create index customers_shop_phone on customers (shop_id, phone);

-- Safety net: RLS on, no public policies. Only the API (database role) reads/writes.
alter table shops enable row level security;
alter table members enable row level security;
alter table locations enable row level security;
alter table items enable row level security;
alter table item_prices enable row level security;
alter table customers enable row level security;
alter table bills enable row level security;
alter table bill_lines enable row level security;
alter table stock_ledger enable row level security;
alter table points_ledger enable row level security;

-- +goose Down
drop table if exists points_ledger, stock_ledger, bill_lines, bills, customers,
  item_prices, items, locations, members, shops cascade;
