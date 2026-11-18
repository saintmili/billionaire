CREATE TABLE billionaire.app_metadata (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    mode TEXT NOT NULL CHECK (mode = 'paper'),
    installed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO billionaire.app_metadata (id, mode) VALUES (TRUE, 'paper');
