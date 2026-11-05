-- Destructive rollback: removes all quote, audit, and delivery data.
DROP TABLE outbox;
DROP TABLE quote_events;
DROP TABLE quotes;
