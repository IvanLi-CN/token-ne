-- +goose Up
create index usage_events_request_id_cursor on usage_events (request_id, occurred_at desc, event_id desc);

-- +goose Down
drop index usage_events_request_id_cursor;
