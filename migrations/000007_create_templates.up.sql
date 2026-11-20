-- Message templates with {{name}} placeholders (OPT-3, D22). They never change once created,
-- so the name is the key clients refer to.
CREATE TABLE templates (
    name       TEXT        PRIMARY KEY,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
