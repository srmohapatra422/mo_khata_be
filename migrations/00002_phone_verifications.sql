-- +goose Up
-- One row per successful customer phone verification (points redemption).
-- A verification is valid until expires_at and can be used by one bill.
create table phone_verifications (
  id bigserial primary key,
  shop_id uuid not null references shops(id) on delete cascade,
  phone text not null,
  verified_by uuid not null,
  verified_at timestamptz not null default now(),
  expires_at timestamptz not null,
  used_at timestamptz,
  bill_id uuid
);
create index phone_verifications_lookup on phone_verifications (shop_id, phone, expires_at);
alter table phone_verifications enable row level security;

-- +goose Down
drop table if exists phone_verifications;
