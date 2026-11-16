-- +goose Up
-- Steps the owner ticked by hand. A step is also done when the shop's data proves it (see internal/setup).
create table setup_steps (
  shop_id uuid not null references shops(id) on delete cascade,
  step text not null check (step in ('shop','prices','stock','bill','cashier','whatsapp')),
  done boolean not null default false,
  updated_at timestamptz not null default now(),
  primary key (shop_id, step)
);
alter table setup_steps enable row level security;

-- +goose Down
drop table if exists setup_steps;
