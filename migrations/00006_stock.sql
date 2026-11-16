-- +goose Up
create table stock_items (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  location_id uuid not null references locations(id) on delete cascade,
  name text not null,
  name_norm text not null,
  unit text not null check (unit in ('kg','birds','trays','pieces')),
  quantity numeric(14,3) not null default 0,
  reorder_level numeric(14,3) check (reorder_level >= 0),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (shop_id, location_id, name_norm, unit)
);
create index stock_items_location on stock_items (shop_id, location_id);

-- quantity is signed: + in, - out (sale, wastage), +/- for adjustment and transfer.
create table stock_movements (
  id bigserial primary key,
  shop_id uuid not null references shops(id) on delete cascade,
  location_id uuid not null references locations(id) on delete cascade,
  stock_item_id uuid not null references stock_items(id) on delete cascade,
  kind text not null check (kind in ('in','sale','wastage','adjustment','transfer')),
  quantity numeric(14,3) not null,
  rate numeric(12,2),
  source text not null check (source in ('form','voice','bill','close_day')),
  note text,
  ref_id uuid,
  created_by uuid,
  created_at timestamptz not null default now()
);
create index stock_movements_item on stock_movements (stock_item_id, kind, id desc);
create index stock_movements_shop_kind on stock_movements (shop_id, kind, created_at);

-- Bill lines remember their unit so a sale can be matched to a stock item.
alter table bill_lines add column unit text;

alter table stock_items enable row level security;
alter table stock_movements enable row level security;

-- +goose Down
alter table bill_lines drop column if exists unit;
drop table if exists stock_movements;
drop table if exists stock_items;
