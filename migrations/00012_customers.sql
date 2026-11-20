-- +goose Up
-- Customers: business flag and who agreed to WhatsApp (and when).
alter table customers
  add column is_business boolean not null default false,
  add column whatsapp_opt_in_at timestamptz,
  add column whatsapp_opt_in_by uuid;

-- Points ledger: a note, and a kind index for the history and the expiry sweep.
alter table points_ledger add column note text;
create index points_ledger_shop_customer_created on points_ledger (shop_id, customer_id, created_at);

-- Credit payments: who took the money and an optional note.
alter table credit_payments
  add column note text,
  add column created_by uuid;
create index credit_payments_shop_customer on credit_payments (shop_id, customer_id);

create index bills_shop_customer_created on bills (shop_id, customer_id, created_at);

-- +goose Down
drop index if exists bills_shop_customer_created;
drop index if exists credit_payments_shop_customer;
alter table credit_payments drop column if exists created_by, drop column if exists note;
drop index if exists points_ledger_shop_customer_created;
alter table points_ledger drop column if exists note;
alter table customers drop column if exists whatsapp_opt_in_by, drop column if exists whatsapp_opt_in_at, drop column if exists is_business;
