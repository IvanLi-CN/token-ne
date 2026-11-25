-- +goose Up
-- A price-source catalog row remembers the owner's intent to keep its target
-- global model present. The mark survives model deletion: the catalog renders
-- such a row as off because the model is absent, not because the mark changed.
create table price_source_sync_marks (
  source_id text not null,
  source_model_id text not null,
  model_id text not null,
  marked_at timestamptz not null default now(),
  primary key (source_id, source_model_id)
);

create index price_source_sync_marks_model on price_source_sync_marks(model_id);

-- +goose Down
drop table if exists price_source_sync_marks;
