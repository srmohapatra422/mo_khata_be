-- +goose Up
alter table locations add column lat double precision;
alter table locations add column lng double precision;
alter table locations add constraint locations_lat_range check (lat between -90 and 90);
alter table locations add constraint locations_lng_range check (lng between -180 and 180);
alter table locations add constraint locations_lat_lng_together check ((lat is null) = (lng is null));

-- +goose Down
alter table locations drop constraint locations_lat_lng_together;
alter table locations drop constraint locations_lng_range;
alter table locations drop constraint locations_lat_range;
alter table locations drop column lng;
alter table locations drop column lat;
