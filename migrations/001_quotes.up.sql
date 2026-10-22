CREATE TABLE quotes (
    id VARCHAR2(32 BYTE) PRIMARY KEY,
    reference VARCHAR2(200 BYTE) NOT NULL,
    currency VARCHAR2(3 BYTE) NOT NULL,
    amount_minor NUMBER(19, 0) NOT NULL CHECK (amount_minor >= 0),
    state VARCHAR2(20 BYTE) NOT NULL CHECK (state IN ('draft', 'submitted', 'approved', 'rejected', 'issued', 'cancelled')),
    version NUMBER(19, 0) NOT NULL CHECK (version >= 1),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE TABLE quote_events (
    quote_id VARCHAR2(32 BYTE) NOT NULL REFERENCES quotes(id),
    version NUMBER(19, 0) NOT NULL CHECK (version >= 1),
    from_state VARCHAR2(20 BYTE),
    to_state VARCHAR2(20 BYTE) NOT NULL,
    actor VARCHAR2(200 BYTE) NOT NULL,
    occurred_at TIMESTAMP WITH TIME ZONE NOT NULL,
    PRIMARY KEY (quote_id, version)
);
