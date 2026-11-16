-- +goose Up
alter table shops add column logo_url text;
alter table shops add column categories text[] not null default '{}';

-- "Dairy" is now a category.
alter table items drop constraint items_category_check;
alter table items add constraint items_category_check
  check (category in ('chicken','fish','mutton','eggs','dairy','other'));

-- +goose Down
alter table items drop constraint items_category_check;
alter table items add constraint items_category_check
  check (category in ('chicken','fish','mutton','eggs','other'));
alter table shops drop column categories;
alter table shops drop column logo_url;
