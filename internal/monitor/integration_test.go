package monitor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRecurringIncidents(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	ctx := context.Background()
	connection = isolatedDatabase(t, connection)
	makeOccurrence := func(incidentID, message, state string, started, ended int64) Alert {
		t.Helper()
		incident := fmt.Sprintf(`{"incident_id":%q,"state":%q,"started_at":%d,"ended_at":%d,
			"policy_id":"cpu-policy","policy_name":"CPU","condition":{"name":"cpu-condition"},
			"metric":{"type":"cpu"},"resource":{"type":"gce_instance",
			"labels":{"project_id":"recurrence-test","instance_id":"instance-1","zone":"europe-west1-b"}}}`,
			incidentID, state, started, ended)
		alert, err := ParseAlert(testEnvelope(incident))
		if err != nil {
			t.Fatal(err)
		}
		alert.Envelope.Message.ID = message
		return alert
	}
	initial := makeOccurrence("gcp-1", "initial", "closed", 1700000000, 1700000100)
	legacy, err := pgx.Connect(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacy.Close(ctx) }()
	if _, err := legacy.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	const legacyID = "existing-issue-link"
	if _, err := legacy.Exec(
		ctx,
		`INSERT INTO issues
		(id,incident_id,project_id,policy_name,summary,resource_name,resource_type,severity,status,
		started_at,ended_at,last_event_at,payload)
		VALUES ($1,$2,$3,'CPU','','instance-1','gce_instance','warning','resolved',$4,$5,$5,$6)`,
		legacyID,
		initial.Incident.ID,
		initial.Project,
		initial.StartedAt,
		initial.EndedAt,
		initial.Payload,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, `INSERT INTO issue_events (issue_id,kind,event_at,actor,body)
		VALUES ($1,'work_log',now(),'legacy-operator','Original investigation')`, legacyID); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()
	ingest := func(alert Alert, wantStatus, wantKind string) {
		t.Helper()
		id, duplicate, err := store.Ingest(ctx, alert)
		if err != nil || duplicate || id != legacyID {
			t.Fatalf(
				"recurrence did not retain the existing issue ID: %q %v %v",
				id,
				duplicate,
				err,
			)
		}
		issue, events, err := store.Detail(ctx, id)
		if err != nil || issue.Status != wantStatus || events[0].Kind != wantKind {
			t.Fatalf("incorrect recurrence state or timeline: %+v %+v %v", issue, events, err)
		}
	}
	reopened := makeOccurrence("gcp-2", "reopened", "open", 1700000200, 0)
	ingest(reopened, "open", "reopened")
	issue, events, err := store.Detail(ctx, legacyID)
	if err != nil || issue.IncidentID != "gcp-2" || !issue.StartedAt.Equal(reopened.StartedAt) ||
		issue.EndedAt != nil || issue.AcknowledgedAt != nil || len(events) != 2 ||
		events[1].Body != "Original investigation" || events[1].Actor != "legacy-operator" {
		t.Fatalf(
			"upgrade or reopening lost history or occurrence metadata: %+v %+v %v",
			issue,
			events,
			err,
		)
	}
	if err := store.SetAcknowledged(ctx, legacyID, true, "operator"); err != nil {
		t.Fatal(err)
	}
	ingest(
		makeOccurrence("gcp-1", "old-close", "closed", 1700000000, 1700000900),
		"acknowledged",
		"stale_delivery",
	)
	ingest(
		makeOccurrence("gcp-1", "old-open", "open", 1700000000, 0),
		"acknowledged",
		"stale_delivery",
	)
	ingest(
		makeOccurrence("gcp-2", "current-close", "closed", 1700000200, 1700000300),
		"resolved",
		"closed",
	)
	ingest(makeOccurrence("gcp-3", "next-open", "open", 1700000400, 0), "open", "reopened")
	if err := store.SetAcknowledged(ctx, legacyID, true, "operator"); err != nil {
		t.Fatal(err)
	}
	ingest(makeOccurrence("gcp-4", "new-after-ack", "open", 1700000500, 0), "open", "reopened")
	issue, _, _ = store.Detail(ctx, legacyID)
	if issue.AcknowledgedAt != nil {
		t.Fatal("new occurrence retained acknowledgement from the previous occurrence")
	}
	concurrent := makeOccurrence("gcp-5", "concurrent", "open", 1700000600, 0)
	var workers sync.WaitGroup
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id, _, err := store.Ingest(ctx, concurrent)
			if err != nil || id != legacyID {
				t.Errorf("concurrent recurrence split: %q %v", id, err)
			}
		}()
	}
	workers.Wait()
	_, events, err = store.Detail(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	var concurrentEvents int
	for _, event := range events {
		if event.MessageID == concurrent.Envelope.Message.ID {
			concurrentEvents++
		}
	}
	if concurrentEvents != 1 {
		t.Fatal("duplicate recurrence created multiple timeline events")
	}
	other := makeOccurrence("gcp-other", "other-resource", "open", 1700000600, 0)
	other.Incident.Resource.Labels["instance_id"] = "instance-2"
	otherID, _, err := store.Ingest(ctx, other)
	if err != nil || otherID == legacyID || otherID != other.issueKey() {
		t.Fatalf("distinct resources merged or new ID not deterministic: %q %v", otherID, err)
	}
	restarted, err := OpenStore(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Pool.Close()
	id, duplicate, err := restarted.Ingest(ctx, reopened)
	if err != nil || !duplicate || id != legacyID {
		t.Fatalf("restart lost historical GCP occurrence mapping: %q %v %v", id, duplicate, err)
	}
}

func TestPostgresWorkflow(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	ctx := context.Background()
	connection = isolatedDatabase(t, connection)
	store, err := OpenStore(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()
	project := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	started := time.Now().Add(-time.Hour).Unix()
	makeAlert := func(message, state string, ended int64) Alert {
		t.Helper()
		incident := map[string]any{
			"incident_id": "incident-1",
			"state":       state,
			"started_at":  started,
			"severity":    "CRITICAL",
			"summary":     "<script>alert(1)</script>",
			"policy_name": "CPU 99%",
			"resource":    map[string]any{"labels": map[string]string{"project_id": project}},
		}
		if ended > 0 {
			incident["ended_at"] = ended
		}
		payload, _ := json.Marshal(map[string]any{"incident": incident})
		body, _ := json.Marshal(
			map[string]any{
				"subscription": "projects/test/subscriptions/alerts",
				"message": map[string]string{
					"messageId": project + message,
					"data":      base64.StdEncoding.EncodeToString(payload),
				},
			},
		)
		alert, err := ParseAlert(body)
		if err != nil {
			t.Fatal(err)
		}
		return alert
	}
	open := makeAlert("open", "open", 0)
	var workers sync.WaitGroup
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, _, err := store.Ingest(ctx, open); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	id, duplicate, err := store.Ingest(ctx, open)
	if err != nil || !duplicate {
		t.Fatalf("duplicate not detected: %v %v", duplicate, err)
	}
	issue, events, err := store.Detail(ctx, id)
	if err != nil || len(events) != 1 || issue.Status != "open" {
		t.Fatalf("deduplication failed: %+v %v %v", issue, events, err)
	}
	if err := store.SetAcknowledged(ctx, id, true, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Ingest(ctx, makeAlert("repeat", "open", 0)); err != nil {
		t.Fatal(err)
	}
	issue, _, _ = store.Detail(ctx, id)
	if issue.Status != "acknowledged" {
		t.Fatal("open notification reset acknowledgement")
	}
	if _, _, err := store.Ingest(ctx, makeAlert("close", "closed", started+100)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Ingest(ctx, makeAlert("late-open", "open", 0)); err != nil {
		t.Fatal(err)
	}
	issue, _, _ = store.Detail(ctx, id)
	if issue.Status != "resolved" {
		t.Fatal("late open overwrote closed state")
	}
	if err := store.SetAcknowledged(ctx, id, true, "tester"); err != ErrConflict {
		t.Fatalf("resolved mutation not rejected: %v", err)
	}
	filters, _ := ParseFilters(
		url.Values{
			"project":  {project},
			"q":        {"99%"},
			"status":   {"resolved"},
			"severity": {"critical"},
		},
	)
	issues, total, err := store.List(ctx, filters)
	if err != nil || total != 1 || len(issues) != 1 {
		t.Fatalf("filters failed: %v %d %v", issues, total, err)
	}
	filters.Query = "' OR 1=1--"
	_, total, err = store.List(ctx, filters)
	if err != nil || total != 0 {
		t.Fatal("SQL filter safety failed", err)
	}
	if overview, err := store.Overview(ctx); err != nil || len(overview.Hours) != 24 {
		t.Fatal("overview failed", err)
	}
	_, err = store.SaveUser(
		ctx,
		"",
		AccountInput{
			Username: "operator",
			Name:     "Test admin",
			Password: "a-long-test-password",
			Role:     "admin",
			Enabled:  true,
		},
		"setup",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, csrf, _, err := store.Login(ctx, "operator", "a-long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store, LocalHTTP: true, Config: Config{Dev: true}}
	handler := server.Handler(
		fstest.MapFS{"index.html": {Data: []byte("<html>Signal Desk</html>")}},
	)
	check := func(method, path string, body []byte, basic, bearer bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		if basic {
			request.Header.Set("Cookie", "signaldesk_session="+token)
			request.Header.Set("X-CSRF-Token", csrf)
		}
		if bearer {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	for _, path := range []string{"/api/issues", "/api/issues/" + id, "/api/overview", "/api/connection"} {
		if response := check("GET", path, nil, false, false); response.Code != 401 {
			t.Fatalf("unprotected %s: %d", path, response.Code)
		}
		if response := check("GET", path, nil, true, false); response.Code != 200 {
			t.Fatalf("failed %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if response := check("GET", "/api/issues?limit=500", nil, true, false); response.Code != 400 {
		t.Fatal("invalid filter accepted")
	}
	if response := check(
		"PATCH",
		"/api/issues/"+id,
		[]byte(`{"acknowledged":true}`),
		true,
		false,
	); response.Code != 409 {
		t.Fatal("resolved issue modified over HTTP")
	}
	if response := check("GET", "/api/issues/missing", nil, true, false); response.Code != 404 {
		t.Fatal("missing issue not 404")
	}
	if response := check("POST", "/api/dev/messages", nil, false, false); response.Code != 401 {
		t.Fatal("sample injection accepted without identity")
	}
	pushBody, _ := json.Marshal(open.Envelope)
	if response := check("POST", "/api/dev/messages", pushBody, true, false); response.Code != 200 {
		t.Fatalf("authenticated sample failed: %d %s", response.Code, response.Body.String())
	}
	workLogPath := "/api/issues/" + id + "/worklogs"
	if response := check(
		"POST",
		workLogPath,
		[]byte(`{"body":"note"}`),
		false,
		false,
	); response.Code != 401 {
		t.Fatal("unauthenticated work log accepted")
	}
	for _, body := range []string{
		`{}`, `{"body":"  "}`, `{"body":1}`, `{"body":"note","actor":"spoofed"}`, `{"body":"note"} {}`,
		`{"body":"` + strings.Repeat("x", 10001) + `"}`,
		`{"body":"note","format":"html"}`,
	} {
		if response := check("POST", workLogPath, []byte(body), true, false); response.Code != 400 {
			t.Fatalf("invalid work log accepted: %d", response.Code)
		}
	}
	request := httptest.NewRequest("POST", workLogPath, strings.NewReader(`{"body":"note"}`))
	request.AddCookie(&http.Cookie{Name: "signaldesk_session", Value: token})
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("work log accepted without CSRF")
	}
	if response := check(
		"POST",
		"/api/issues/missing/worklogs",
		[]byte(`{"body":"note"}`),
		true,
		false,
	); response.Code != 404 {
		t.Fatal("missing issue work log not rejected")
	}
	if response := check(
		"POST",
		workLogPath,
		[]byte(`{"body":"  Checked deployment.\nInvestigating metrics.  "}`),
		true,
		false,
	); response.Code != 200 {
		t.Fatalf("work log failed: %d %s", response.Code, response.Body.String())
	}
	_, events, err = store.Detail(ctx, id)
	if err != nil || len(events) == 0 {
		t.Fatalf("work log missing: %v", err)
	}
	if events[0].Kind != "work_log" || events[0].Actor != "operator" ||
		events[0].Body != "Checked deployment.\nInvestigating metrics." || events[0].BodyFormat != "plain" {
		t.Fatalf("work log attribution or text lost: %+v", events[0])
	}
	const richNote = "## Investigation\n\n**CPU** improved.\n\n- Checked metrics\n\n```go\nreturn nil\n```"
	richBody, _ := json.Marshal(map[string]string{"body": richNote, "format": "markdown"})
	if response := check("POST", workLogPath, richBody, true, false); response.Code != 200 {
		t.Fatalf("formatted work log rejected: %d %s", response.Code, response.Body.String())
	}
	_, events, err = store.Detail(ctx, id)
	if err != nil || events[0].Body != richNote || events[0].BodyFormat != "markdown" {
		t.Fatal("formatted work log was not preserved", err)
	}
	initialEvents := len(events)
	for index := 0; index < 101; index++ {
		if err := store.AddWorkLog(
			ctx,
			id,
			fmt.Sprintf("Progress update %d", index),
			"operator",
			"",
			"",
		); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenStore(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Pool.Close()
	_, events, err = reopened.Detail(ctx, id)
	if err != nil || len(events) != initialEvents+101 {
		t.Fatalf("full timeline not persisted: %d events, %v", len(events), err)
	}
	if events[0].Body != "Progress update 100" ||
		events[len(events)-1].Kind != "open" || len(events[len(events)-1].Payload) == 0 {
		t.Fatal("work logs or original Monitoring update missing from timeline")
	}
	if events[101].Body != richNote || events[101].BodyFormat != "markdown" ||
		events[102].BodyFormat != "plain" {
		t.Fatal("restart lost rich-text format or reinterpreted the legacy plain-text note")
	}
	for index := 1; index < len(events); index++ {
		if events[index].ID >= events[index-1].ID {
			t.Fatal("timeline not ordered newest first")
		}
	}
	for _, body := range []string{
		`{"body":"Invalid transition","status":"blocked"}`,
		`{"body":"  ","status":"open"}`,
		`{"body":"Invalid type","status":true}`,
	} {
		if response := check("POST", workLogPath, []byte(body), true, false); response.Code != 400 {
			t.Fatalf("invalid status submission accepted: %d", response.Code)
		}
	}
	current, unchanged, err := store.Detail(ctx, id)
	if err != nil || current.Status != "resolved" || len(unchanged) != len(events) {
		t.Fatal("rejected status submission changed the issue or timeline", err)
	}
	for _, next := range []string{"open", "acknowledged", "resolved", "open", "open", ""} {
		previous, before, err := store.Detail(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(
			map[string]string{"body": "Investigated and updated status", "status": next},
		)
		if response := check("POST", workLogPath, body, true, false); response.Code != 200 {
			t.Fatalf("status %q failed: %d %s", next, response.Code, response.Body.String())
		}
		persisted, timeline, err := reopened.Detail(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus, extraEvents := previous.Status, 1
		if next != "" && next != previous.Status {
			wantStatus, extraEvents = next, 2
			if timeline[0].Kind != "status_changed" || timeline[0].Actor != "operator" ||
				timeline[0].Body != fmt.Sprintf(
					"Status changed from %s to %s",
					previous.Status,
					next,
				) {
				t.Fatalf("status change not attributed in timeline: %+v", timeline[0])
			}
		}
		if persisted.Status != wantStatus || len(timeline) != len(before)+extraEvents ||
			timeline[extraEvents-1].Kind != "work_log" {
			t.Fatalf("work log and status not persisted together: %+v", persisted)
		}
		if (persisted.AcknowledgedAt != nil) != (wantStatus == "acknowledged") ||
			(persisted.EndedAt != nil) != (wantStatus == "resolved") {
			t.Fatalf("incorrect status timestamps: %+v", persisted)
		}
	}
}

func isolatedDatabase(t *testing.T, connection string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	schemaName := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schemaName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(connection)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schemaName)
	parsed.RawQuery = params.Encode()
	return parsed.String()
}

func TestControlPersistenceAndRoles(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	connection = isolatedDatabase(t, connection)
	ctx := context.Background()
	server := &Server{DataDir: t.TempDir(), LocalHTTP: true}
	defer server.Close()
	assets := fstest.MapFS{"index.html": {Data: []byte("Signal Desk")}}
	handler := server.Handler(assets)
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, body any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		request := httptest.NewRequest(method, path, bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		if auth && cookie != nil {
			request.AddCookie(cookie)
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	setup := map[string]any{
		"databaseUrl": connection,
		"username":    "admin",
		"name":        "Workspace Admin",
		"password":    "a-long-test-password",
	}
	response := call("POST", "/api/setup", setup, false)
	if response.Code != 200 {
		t.Fatalf("setup: %d %s", response.Code, response.Body.String())
	}
	cookie = response.Result().Cookies()[0]
	var session struct {
		User User   `json:"user"`
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	csrf = session.CSRF
	alert, err := ParseAlert(
		testEnvelope(`{"incident_id":"worklog-role-test","state":"open","started_at":1700000000}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _, err := server.store().Ingest(ctx, alert)
	if err != nil {
		t.Fatal(err)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || csrf == "" {
		t.Fatal("unsafe session cookie")
	}
	if response := call("POST", "/api/setup", setup, false); response.Code != 409 {
		t.Fatalf("setup reopened: %d", response.Code)
	}
	if response := call(
		"PUT",
		"/api/settings",
		map[string]any{
			"subscription": "projects/example-project/subscriptions/alerts",
			"enabled":      false,
		},
		false,
	); response.Code != 401 {
		t.Fatal("unauthenticated settings")
	}
	savedCSRF := csrf
	csrf = ""
	if response := call(
		"PUT",
		"/api/settings",
		map[string]any{"subscription": "", "enabled": false},
		true,
	); response.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	csrf = savedCSRF
	if response := call(
		"PUT",
		"/api/settings",
		map[string]any{
			"subscription": "projects/example-project/subscriptions/alerts",
			"enabled":      false,
		},
		true,
	); response.Code != 200 {
		t.Fatalf("settings save: %d %s", response.Code, response.Body.String())
	}
	if response := call(
		"PATCH",
		"/api/users/"+session.User.ID,
		AccountInput{Username: "admin", Name: "Workspace Admin", Role: "viewer", Enabled: true},
		true,
	); response.Code != 409 {
		t.Fatal("last admin removed")
	}
	for _, role := range []string{"viewer", "operator"} {
		response := call(
			"POST",
			"/api/users",
			AccountInput{
				Username: role,
				Name:     role,
				Password: "a-long-test-password",
				Role:     role,
				Enabled:  true,
			},
			true,
		)
		if response.Code != 200 {
			t.Fatalf("create %s: %s", role, response.Body.String())
		}
		var user User
		_ = json.Unmarshal(response.Body.Bytes(), &user)
		token, userCSRF, _, err := server.store().Login(ctx, role, "a-long-test-password")
		if err != nil {
			t.Fatal(err)
		}
		adminCookie, adminCSRF := cookie, csrf
		cookie = &http.Cookie{Name: "signaldesk_session", Value: token}
		csrf = userCSRF
		if response := call("GET", "/api/issues", nil, true); response.Code != 200 {
			t.Fatalf("%s cannot read", role)
		}
		for _, path := range []string{"/api/users", "/api/settings"} {
			if response := call("GET", path, nil, true); response.Code != 403 {
				t.Fatalf("%s accesses %s", role, path)
			}
		}
		response = call("PATCH", "/api/issues/missing", map[string]bool{"acknowledged": true}, true)
		if role == "viewer" && response.Code != 403 || role == "operator" && response.Code != 404 {
			t.Fatalf("%s acknowledge permission: %d", role, response.Code)
		}
		response = call(
			"POST",
			"/api/issues/"+issueID+"/worklogs",
			map[string]string{"body": "Investigating", "status": "acknowledged"},
			true,
		)
		if role == "viewer" && response.Code != 403 || role == "operator" && response.Code != 200 {
			t.Fatalf("%s work log permission: %d %s", role, response.Code, response.Body.String())
		}
		cookie, csrf = adminCookie, adminCSRF
		if response := call(
			"PATCH",
			"/api/users/"+user.ID,
			AccountInput{Username: role, Name: role, Role: role, Enabled: false},
			true,
		); response.Code != 200 {
			t.Fatalf("disable: %s", response.Body.String())
		}
		if _, _, err := server.store().Session(ctx, token); err == nil {
			t.Fatal("disabled account session retained")
		}
	}
	request := httptest.NewRequest(
		"POST",
		"/api/login",
		bytes.NewBufferString(`{"username":"admin","password":"a-long-test-password"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	restarted := &Server{DataDir: server.DataDir, LocalHTTP: true}
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, _, err := restarted.store().Session(ctx, cookie.Value); err != nil {
		t.Fatal("session lost across application replacement", err)
	}
	settings, err := restarted.store().Settings(ctx)
	if err != nil || settings.Subscription != "projects/example-project/subscriptions/alerts" {
		t.Fatal("settings lost", err)
	}
	if users, err := restarted.store().Users(ctx); err != nil || len(users) != 3 {
		t.Fatal("users lost", err)
	}
	if response := call(
		"GET",
		"/api/settings",
		nil,
		true,
	); bytes.Contains(response.Body.Bytes(), []byte("databaseUrl")) ||
		bytes.Contains(response.Body.Bytes(), []byte("password")) {
		t.Fatal("database credentials disclosed")
	}
}

type fakePull struct {
	acknowledgements []string
	failAck          bool
}

func (source *fakePull) Pull(context.Context, string) ([]pulledMessage, error) { return nil, nil }
func (source *fakePull) Ack(_ context.Context, _ string, ids []string) error {
	if source.failAck {
		return errors.New("test ack failure")
	}
	source.acknowledgements = append(source.acknowledgements, ids...)
	return nil
}

func TestPullCommitBeforeAcknowledgement(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	connection = isolatedDatabase(t, connection)
	ctx := context.Background()
	store, err := OpenStore(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()
	server := &Server{Store: store}
	payload := fmt.Sprintf(
		`{"incident":{"incident_id":"pull-incident","state":"open","started_at":%d,`+
			`"resource":{"labels":{"project_id":"test"}}}}`,
		time.Now().Unix(),
	)
	message := pulledMessage{
		AckID:     "ack-1",
		MessageID: "message-1",
		Data:      base64.StdEncoding.EncodeToString([]byte(payload)),
	}
	source := &fakePull{failAck: true}
	if err := server.processMessages(
		ctx,
		"projects/example-project/subscriptions/alerts",
		source,
		[]pulledMessage{message},
	); err == nil {
		t.Fatal("ack failure ignored")
	}
	var count int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM issues").
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatal("message not committed before acknowledgement", err)
	}
	source.failAck = false
	if err := server.processMessages(
		ctx,
		"projects/example-project/subscriptions/alerts",
		source,
		[]pulledMessage{message},
	); err != nil {
		t.Fatal(err)
	}
	if len(source.acknowledgements) != 1 {
		t.Fatal("redelivery not acknowledged")
	}
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM issue_events").
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatal("redelivery duplicated event", err)
	}
	source.acknowledgements = nil
	if err := server.processMessages(
		ctx,
		"projects/example-project/subscriptions/alerts",
		source,
		[]pulledMessage{{AckID: "bad", MessageID: "bad", Data: "bad"}},
	); err == nil ||
		len(source.acknowledgements) > 0 {
		t.Fatal("malformed message acknowledged")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	message.MessageID = "new-message"
	if err := server.processMessages(
		canceled,
		"projects/example-project/subscriptions/alerts",
		source,
		[]pulledMessage{message},
	); err == nil ||
		len(source.acknowledgements) > 0 {
		t.Fatal("failed transaction acknowledged")
	}
}
