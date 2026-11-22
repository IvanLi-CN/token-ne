-- +goose Up
alter table core_versions add column digest_pinned boolean not null default false;
update core_versions set digest_pinned = image_ref like '%@%';

-- +goose Down
alter table core_versions drop column digest_pinned;
