-- +goose Up
create table expenses (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  location_id uuid not null references locations(id) on delete cascade,
  amount numeric(12,2) not null check (amount > 0),
  note text,
  category text,
  created_by uuid not null,
  created_at timestamptz not null default now()
);
create index expenses_shop_loc_created on expenses (shop_id, location_id, created_at);

-- Credit collected from customers. location_id says at which counter it was collected;
-- close-day only counts rows for the location being closed.
create table credit_payments (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  customer_id uuid not null references customers(id) on delete cascade,
  location_id uuid references locations(id) on delete cascade,
  amount numeric(12,2) not null check (amount > 0),
  method text not null check (method in ('cash','upi','card')),
  created_at timestamptz not null default now()
);
create index credit_payments_shop_loc_created on credit_payments (shop_id, location_id, created_at);

create table day_closings (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  location_id uuid not null references locations(id) on delete cascade,
  business_date date not null,
  expected_cash numeric(12,2) not null,
  counted_cash numeric(12,2) not null,
  difference numeric(12,2) not null,   -- counted - expected (negative = short)
  reason text,
  summary jsonb not null default '{}',
  closed_by uuid not null,
  created_at timestamptz not null default now(),
  unique (shop_id, location_id, business_date)
);

create table day_closing_stock (
  closing_id uuid not null references day_closings(id) on delete cascade,
  stock_item_id uuid not null references stock_items(id) on delete cascade,
  expected numeric(14,3) not null,
  actual numeric(14,3) not null,
  wastage numeric(14,3) not null default 0,
  primary key (closing_id, stock_item_id)
);

alter table expenses enable row level security;
alter table credit_payments enable row level security;
alter table day_closings enable row level security;
alter table day_closing_stock enable row level security;

-- +goose Down
drop table if exists day_closing_stock;
drop table if exists day_closings;
drop table if exists credit_payments;
drop table if exists expenses;
