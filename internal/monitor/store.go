package monitor

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct{ Pool *pgxpool.Pool }

func OpenStore(ctx context.Context, connection string) (*Store, error) {
	config, err := pgxpool.ParseConfig(connection)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 8
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	store := &Store{Pool: pool}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73519342)"); err == nil {
		_, err = tx.Exec(ctx, schema)
	}
	if err == nil {
		err = backfillIssueKeys(ctx, tx)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func backfillIssueKeys(ctx context.Context, tx pgx.Tx) error {
	type legacyIssue struct {
		ID      string
		Project string
		Payload json.RawMessage
	}
	rows, err := tx.Query(ctx, `SELECT id,project_id,payload FROM issues
		WHERE NOT EXISTS (SELECT 1 FROM issue_occurrences WHERE issue_id=issues.id)
		ORDER BY started_at DESC,id DESC`)
	if err != nil {
		return err
	}
	issues, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (legacyIssue, error) {
		var issue legacyIssue
		err := row.Scan(&issue.ID, &issue.Project, &issue.Payload)
		return issue, err
	})
	if err != nil {
		return err
	}
	for _, issue := range issues {
		var notification struct {
			Incident Incident `json:"incident"`
		}
		if err := json.Unmarshal(issue.Payload, &notification); err != nil {
			return err
		}
		alert := Alert{Project: issue.Project, Incident: notification.Incident}
		if err := rememberIssue(ctx, tx, issue.ID, alert); err != nil {
			return err
		}
	}
	return nil
}

func rememberIssue(ctx context.Context, tx pgx.Tx, id string, alert Alert) error {
	if _, err := tx.Exec(ctx, `INSERT INTO issue_keys (fingerprint,issue_id)
		VALUES ($1,$2) ON CONFLICT DO NOTHING`, alert.issueKey(), id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO issue_occurrences (project_id,incident_id,issue_id)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, alert.Project, alert.Incident.ID, id)
	return err
}

type Issue struct {
	ID             string          `json:"id"`
	IncidentID     string          `json:"incidentId"`
	Project        string          `json:"project"`
	PolicyName     string          `json:"policyName"`
	Summary        string          `json:"summary"`
	ResourceName   string          `json:"resourceName"`
	ResourceType   string          `json:"resourceType"`
	Severity       string          `json:"severity"`
	Status         string          `json:"status"`
	StartedAt      time.Time       `json:"startedAt"`
	EndedAt        *time.Time      `json:"endedAt"`
	ReceivedAt     time.Time       `json:"receivedAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	AcknowledgedAt *time.Time      `json:"acknowledgedAt"`
	MonitoringURL  string          `json:"monitoringUrl"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

type Event struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	EventAt    time.Time       `json:"eventAt"`
	ReceivedAt time.Time       `json:"receivedAt"`
	MessageID  string          `json:"messageId"`
	Actor      string          `json:"actor"`
	Body       string          `json:"body,omitempty"`
	BodyFormat string          `json:"bodyFormat,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

const columns = `id, incident_id, project_id, policy_name, summary, resource_name, resource_type,
	severity, status, started_at, ended_at, received_at, updated_at, acknowledged_at, monitoring_url`

func scanIssue(row pgx.Row, issue *Issue, detail bool) error {
	targets := []any{
		&issue.ID,
		&issue.IncidentID,
		&issue.Project,
		&issue.PolicyName,
		&issue.Summary,
		&issue.ResourceName,
		&issue.ResourceType,
		&issue.Severity,
		&issue.Status,
		&issue.StartedAt,
		&issue.EndedAt,
		&issue.ReceivedAt,
		&issue.UpdatedAt,
		&issue.AcknowledgedAt,
		&issue.MonitoringURL,
	}
	if detail {
		targets = append(targets, &issue.Payload)
	}
	return row.Scan(targets...)
}

func (store *Store) Ingest(ctx context.Context, alert Alert) (string, bool, error) {
	fingerprint := alert.issueKey()
	id := fingerprint
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fingerprint,
	); err != nil {
		return "", false, err
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT issue_id FROM (
		SELECT issue_id,0 AS priority FROM issue_occurrences WHERE project_id=$1 AND incident_id=$2
		UNION ALL SELECT issue_id,1 FROM issue_keys WHERE fingerprint=$3
		) candidates ORDER BY priority LIMIT 1`, alert.Project, alert.Incident.ID, fingerprint).Scan(&existingID)
	if err == nil {
		id = existingID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	result, err := tx.Exec(
		ctx,
		`INSERT INTO deliveries (subscription, message_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		alert.Envelope.Subscription,
		alert.Envelope.Message.ID,
	)
	if err != nil {
		return "", false, err
	}
	if result.RowsAffected() == 0 {
		return id, true, tx.Commit(ctx)
	}
	var last, started time.Time
	var status, incidentID string
	err = tx.QueryRow(ctx, `SELECT last_event_at,status,incident_id,started_at FROM issues WHERE id=$1 FOR UPDATE`, id).
		Scan(&last, &status, &incidentID, &started)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	kind := alert.Incident.State
	newOccurrence := exists && incidentID != alert.Incident.ID
	if exists && ((newOccurrence && !alert.StartedAt.After(started)) ||
		(!newOccurrence && !ShouldApply(last, alert))) {
		kind = "stale_delivery"
	} else {
		nextStatus := statusOpen
		if alert.Incident.State == stateClosed {
			nextStatus = statusResolved
		} else if status == statusAcknowledged && !newOccurrence {
			nextStatus = status
		}
		if exists && newOccurrence && alert.Incident.State == statusOpen {
			kind = "reopened"
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO issues (id, incident_id, project_id, policy_name, summary, resource_name,
			resource_type, severity, status, started_at, ended_at, last_event_at, payload, monitoring_url)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (id) DO UPDATE SET incident_id=EXCLUDED.incident_id,started_at=EXCLUDED.started_at,
			policy_name=EXCLUDED.policy_name, summary=EXCLUDED.summary,
			resource_name=EXCLUDED.resource_name,
			resource_type=EXCLUDED.resource_type, severity=EXCLUDED.severity, status=EXCLUDED.status, ended_at=EXCLUDED.ended_at,
			last_event_at=EXCLUDED.last_event_at, payload=EXCLUDED.payload,
			acknowledged_at=CASE WHEN EXCLUDED.status='acknowledged' THEN issues.acknowledged_at ELSE NULL END,
			monitoring_url=EXCLUDED.monitoring_url, updated_at=now()`,
			id,
			alert.Incident.ID,
			alert.Project,
			alert.Incident.PolicyName,
			alert.Incident.Summary,
			alert.Incident.ResourceName,
			alert.Incident.Resource.Type,
			alert.Severity,
			nextStatus,
			alert.StartedAt,
			alert.EndedAt,
			alert.EventAt,
			alert.Payload,
			alert.Incident.URL,
		)
		if err != nil {
			return "", false, err
		}
	}
	_, err = tx.Exec(
		ctx,
		`INSERT INTO issue_events (issue_id, kind, event_at, message_id, payload) VALUES ($1,$2,$3,$4,$5)`,
		id,
		kind,
		alert.EventAt,
		alert.Envelope.Message.ID,
		alert.Payload,
	)
	if err != nil {
		return "", false, err
	}
	if err := rememberIssue(ctx, tx, id, alert); err != nil {
		return "", false, err
	}
	return id, false, tx.Commit(ctx)
}

func (store *Store) List(ctx context.Context, filters Filters) ([]Issue, int, error) {
	where, args := filters.Where()
	var total int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM issues`+where, args...).
		Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, filters.Limit, (filters.Page-1)*filters.Limit)
	query := `SELECT ` + columns + ` FROM issues` + where + fmt.Sprintf(
		` ORDER BY started_at DESC, id LIMIT $%d OFFSET $%d`,
		len(args)-1,
		len(args),
	)
	rows, err := store.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	issues := []Issue{}
	for rows.Next() {
		var issue Issue
		if err := scanIssue(rows, &issue, false); err != nil {
			return nil, 0, err
		}
		issues = append(issues, issue)
	}
	return issues, total, rows.Err()
}

func (store *Store) Detail(ctx context.Context, id string) (Issue, []Event, error) {
	var issue Issue
	if err := scanIssue(
		store.Pool.QueryRow(ctx, `SELECT `+columns+`, payload FROM issues WHERE id=$1`, id),
		&issue,
		true,
	); err != nil {
		return issue, nil, err
	}
	rows, err := store.Pool.Query(
		ctx,
		`SELECT id, kind, event_at, received_at, message_id, actor, body, body_format, payload FROM issue_events
		WHERE issue_id=$1 ORDER BY received_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return issue, nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(
			&event.ID,
			&event.Kind,
			&event.EventAt,
			&event.ReceivedAt,
			&event.MessageID,
			&event.Actor,
			&event.Body,
			&event.BodyFormat,
			&event.Payload,
		); err != nil {
			return issue, nil, err
		}
		events = append(events, event)
	}
	return issue, events, rows.Err()
}

var (
	ErrConflict       = errors.New("resolved incidents require a work log to change status")
	ErrInvalidStatus  = errors.New("status must be open, acknowledged or resolved")
	ErrInvalidFormat  = errors.New("work log format must be plain or markdown")
	ErrInvalidWorkLog = errors.New(
		"work log must contain between 1 and 10000 characters of valid text",
	)
)

func (store *Store) AddWorkLog(
	ctx context.Context,
	id, body, actor, newStatus, format string,
) error {
	body = strings.TrimSpace(body)
	if body == "" || !utf8.ValidString(body) || utf8.RuneCountInString(body) > 10000 {
		return ErrInvalidWorkLog
	}
	if !oneOf(newStatus, "", statusOpen, statusAcknowledged, statusResolved) {
		return ErrInvalidStatus
	}
	if format == "" {
		format = "plain"
	}
	if !oneOf(format, "plain", "markdown") {
		return ErrInvalidFormat
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var previousStatus string
	if err := tx.QueryRow(
		ctx,
		"UPDATE issues SET updated_at=now() WHERE id=$1 RETURNING status",
		id,
	).Scan(&previousStatus); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO issue_events (issue_id,kind,event_at,actor,body,body_format)
		VALUES ($1,'work_log',now(),$2,$3,$4)`,
		id,
		actor,
		body,
		format,
	); err != nil {
		return err
	}
	if newStatus != "" && newStatus != previousStatus {
		if _, err := tx.Exec(ctx, `UPDATE issues SET status=$2,
			acknowledged_at=CASE WHEN $2='acknowledged' THEN now() ELSE NULL END,
			ended_at=CASE WHEN $2='resolved' THEN COALESCE(ended_at,now()) ELSE NULL END WHERE id=$1`,
			id, newStatus); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO issue_events (issue_id,kind,event_at,actor,body)
			VALUES ($1,'status_changed',now(),$2,$3)`,
			id,
			actor,
			fmt.Sprintf("Status changed from %s to %s", previousStatus, newStatus),
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (store *Store) SetAcknowledged(
	ctx context.Context,
	id string,
	acknowledged bool,
	actor string,
) error {
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM issues WHERE id=$1 FOR UPDATE`, id).
		Scan(&status); err != nil {
		return err
	}
	if status == statusResolved {
		return ErrConflict
	}
	next, kind := statusOpen, "unacknowledged"
	if acknowledged {
		next, kind = statusAcknowledged, statusAcknowledged
	}
	if status == next {
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(
		ctx,
		`UPDATE issues SET status=$2, acknowledged_at=CASE WHEN $3 THEN now() ELSE NULL END, updated_at=now() WHERE id=$1`,
		id,
		next,
		acknowledged,
	)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO issue_events (issue_id,kind,event_at,actor) VALUES ($1,$2,now(),$3)`,
		id,
		kind,
		actor,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type HourBucket struct {
	Hour     time.Time `json:"hour"`
	Critical int       `json:"critical"`
	Warning  int       `json:"warning"`
	Info     int       `json:"info"`
}

type Overview struct {
	Total        int          `json:"total"`
	Open         int          `json:"open"`
	Acknowledged int          `json:"acknowledged"`
	Resolved     int          `json:"resolved"`
	Critical     int          `json:"critical"`
	LastReceived *time.Time   `json:"lastReceived"`
	Projects     []string     `json:"projects"`
	Hours        []HourBucket `json:"hours"`
}

func (store *Store) Overview(ctx context.Context) (Overview, error) {
	result := Overview{Projects: []string{}, Hours: []HourBucket{}}
	err := store.Pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='open'),
		count(*) FILTER (WHERE status='acknowledged'), count(*) FILTER (WHERE status='resolved'),
		count(*) FILTER (WHERE severity='critical' AND status='open'),
		(SELECT max(received_at) FROM deliveries) FROM issues`).
		Scan(&result.Total, &result.Open, &result.Acknowledged, &result.Resolved, &result.Critical, &result.LastReceived)
	if err != nil {
		return result, err
	}
	rows, err := store.Pool.Query(ctx, `SELECT DISTINCT project_id FROM issues ORDER BY project_id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			rows.Close()
			return result, err
		}
		result.Projects = append(result.Projects, project)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	rows, err = store.Pool.Query(
		ctx,
		`SELECT bucket, count(i.id) FILTER (WHERE severity='critical'),
		count(i.id) FILTER (WHERE severity='warning'), count(i.id) FILTER (WHERE severity='info')
		FROM generate_series(date_trunc('hour',now())-interval '23 hours',date_trunc('hour',now()),interval '1 hour') bucket
		LEFT JOIN issues i ON i.started_at >= bucket AND i.started_at < bucket+interval '1 hour'
		GROUP BY bucket ORDER BY bucket`,
	)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket HourBucket
		if err := rows.Scan(
			&bucket.Hour,
			&bucket.Critical,
			&bucket.Warning,
			&bucket.Info,
		); err != nil {
			return result, err
		}
		result.Hours = append(result.Hours, bucket)
	}
	return result, rows.Err()
}
