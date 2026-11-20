-- The template a notification's content was made from (OPT-3, D22); NULL for plain content.
-- Templates are never deleted, so the reference always resolves.
ALTER TABLE notifications ADD COLUMN template TEXT REFERENCES templates (name);
