CREATE TABLE IF NOT EXISTS issues (
    id text PRIMARY KEY,
    incident_id text NOT NULL,
    project_id text NOT NULL,
    policy_name text NOT NULL,
    summary text NOT NULL,
    resource_name text NOT NULL,
    resource_type text NOT NULL,
    severity text NOT NULL CHECK (severity IN ('critical', 'warning', 'info')),
    status text NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved')),
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    last_event_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    acknowledged_at timestamptz,
    payload jsonb NOT NULL,
    monitoring_url text NOT NULL DEFAULT '',
    UNIQUE (project_id, incident_id)
);
CREATE INDEX IF NOT EXISTS issues_started_idx ON issues (started_at DESC, id);
CREATE INDEX IF NOT EXISTS issues_status_severity_idx ON issues (status, severity, started_at DESC);
CREATE INDEX IF NOT EXISTS issues_project_idx ON issues (project_id, started_at DESC);
CREATE TABLE IF NOT EXISTS issue_keys (
    fingerprint text PRIMARY KEY,
    issue_id text NOT NULL REFERENCES issues(id)
);
CREATE TABLE IF NOT EXISTS issue_occurrences (
    project_id text NOT NULL,
    incident_id text NOT NULL,
    issue_id text NOT NULL REFERENCES issues(id),
    PRIMARY KEY (project_id, incident_id)
);
CREATE INDEX IF NOT EXISTS issue_occurrences_issue_idx ON issue_occurrences(issue_id);
CREATE TABLE IF NOT EXISTS deliveries (
    subscription text NOT NULL,
    message_id text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subscription, message_id)
);
CREATE TABLE IF NOT EXISTS issue_events (
    id bigserial PRIMARY KEY,
    issue_id text NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    kind text NOT NULL,
    event_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    message_id text NOT NULL DEFAULT '',
    actor text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    payload jsonb
);
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS body text NOT NULL DEFAULT '';
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS body_format text NOT NULL DEFAULT 'plain'
    CHECK (body_format IN ('plain', 'markdown'));
CREATE INDEX IF NOT EXISTS issue_events_issue_idx ON issue_events (issue_id, received_at DESC);
CREATE TABLE IF NOT EXISTS app_users (
    id text PRIMARY KEY,
    username text NOT NULL UNIQUE,
    name text NOT NULL,
    password_hash bytea NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS app_sessions (
    token_hash text PRIMARY KEY,
    user_id text NOT NULL REFERENCES app_users(id),
    csrf text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS app_sessions_user_idx ON app_sessions(user_id);
CREATE TABLE IF NOT EXISTS app_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    workspace_id text NOT NULL,
    subscription text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS app_audit (
    id bigserial PRIMARY KEY,
    actor text NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS app_login_rate (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    window_start timestamptz NOT NULL,
    attempts integer NOT NULL
);
