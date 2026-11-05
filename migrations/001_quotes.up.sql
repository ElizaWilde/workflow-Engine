CREATE TABLE quotes (
    id varchar(32) PRIMARY KEY,
    reference text NOT NULL CHECK (octet_length(reference) BETWEEN 1 AND 200 AND btrim(reference) <> ''),
    currency varchar(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    owner text NOT NULL CHECK (octet_length(owner) BETWEEN 1 AND 200),
    state text NOT NULL CHECK (state IN ('draft', 'submitted', 'approved', 'rejected', 'issued', 'cancelled')),
    version bigint NOT NULL CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE quote_events (
    quote_id varchar(32) NOT NULL REFERENCES quotes(id),
    version bigint NOT NULL CHECK (version >= 1),
    payload jsonb NOT NULL,
    PRIMARY KEY (quote_id, version)
);

CREATE TABLE outbox (
    id bigserial PRIMARY KEY,
    quote_id varchar(32) NOT NULL,
    version bigint NOT NULL,
    payload jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token text,
    processed_at timestamptz,
    dead_at timestamptz,
    last_error text,
    FOREIGN KEY (quote_id, version) REFERENCES quote_events(quote_id, version),
    UNIQUE (quote_id, version)
);
CREATE INDEX outbox_due ON outbox (available_at, id) WHERE processed_at IS NULL AND dead_at IS NULL;
