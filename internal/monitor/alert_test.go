package monitor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testEnvelope(incident string) []byte {
	encoded, _ := json.Marshal(
		map[string]any{
			"subscription": "projects/test/subscriptions/alerts",
			"message": map[string]string{
				"messageId": "message-1",
				"data": base64.StdEncoding.EncodeToString(
					[]byte(`{"incident":` + incident + `}`),
				),
			},
		},
	)
	return encoded
}

func TestParseAlert(t *testing.T) {
	alert, err := ParseAlert(
		testEnvelope(
			`{"incident_id":"inc-1","state":"open","started_at":"1700000000",` +
				`"severity":"ERROR","resource":{"labels":{"project_id":"demo"}}}`,
		),
	)
	if err != nil || alert.Severity != "critical" || alert.Project != "demo" ||
		alert.StartedAt.Unix() != 1700000000 {
		t.Fatalf("unexpected alert: %+v, %v", alert, err)
	}
}

func TestInvalidAlerts(t *testing.T) {
	for _, incident := range []string{
		`{}`,
		`{"incident_id":"id","state":"closed","started_at":1700000000}`,
		`{"incident_id":"id","state":"open","started_at":-1}`,
		`{"incident_id":"id","state":"open","started_at":999999999999999}`,
	} {
		if _, err := ParseAlert(testEnvelope(incident)); err == nil {
			t.Errorf("accepted invalid incident %s", incident)
		}
	}
	if _, err := ParseAlert(
		[]byte(`{"message":{"messageId":"id","data":"!!!!"},"subscription":"sub"}`),
	); err == nil {
		t.Fatal("accepted invalid base64")
	}
}

func TestEventOrdering(t *testing.T) {
	alert, _ := ParseAlert(
		testEnvelope(`{"incident_id":"id","state":"open","started_at":1700000000}`),
	)
	if ShouldApply(alert.EventAt, alert) || ShouldApply(alert.EventAt.Add(time.Minute), alert) {
		t.Fatal("late open event overwrote newer state")
	}
	alert.Incident.State = "closed"
	if !ShouldApply(alert.EventAt, alert) {
		t.Fatal("close must win timestamp ties")
	}
}

func TestStableIssueKey(t *testing.T) {
	base := `{"incident_id":"%s","state":"open","started_at":1700000000,
		"policy_id":"policy-1","policy_name":"CPU","condition":{"name":"condition-1"},
		"metric":{"type":"cpu","labels":{"device":"%s"}},
		"resource":{"type":"cloud_run_revision","labels":{
		"project_id":"%s","service_name":"%s","revision_name":"%s"}}}`
	parse := func(id, device, project, service, revision string) Alert {
		t.Helper()
		alert, err := ParseAlert(
			testEnvelope(fmt.Sprintf(base, id, device, project, service, revision)),
		)
		if err != nil {
			t.Fatal(err)
		}
		return alert
	}
	first := parse("gcp-1", "disk-1", "project-1", "api", "rev-1").issueKey()
	if first != parse("gcp-2", "disk-1", "project-1", "api", "rev-2").issueKey() {
		t.Fatal("recurrence or Cloud Run revision changed the stable issue ID")
	}
	for _, other := range []Alert{
		parse("gcp-2", "disk-2", "project-1", "api", "rev-1"),
		parse("gcp-2", "disk-1", "project-2", "api", "rev-1"),
		parse("gcp-2", "disk-1", "project-1", "worker", "rev-1"),
	} {
		if first == other.issueKey() {
			t.Fatal("distinct time series merged")
		}
	}
	for _, dimension := range []string{"policy", "condition", "metric"} {
		other := parse("gcp-1", "disk-1", "project-1", "api", "rev-1")
		switch dimension {
		case "policy":
			other.Incident.PolicyID = "other-policy"
		case "condition":
			other.Incident.Condition.Name = "other-condition"
		case "metric":
			other.Incident.Metric.Type = "other-metric"
		}
		if first == other.issueKey() {
			t.Errorf("different %s merged", dimension)
		}
	}
	minimal, err := ParseAlert(
		testEnvelope(`{"incident_id":"first","state":"open","started_at":1700000000,
		"policy_name":"CPU","resource_name":"api","resource":{"labels":{"project_id":"demo","zone":"west"}}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	other := minimal
	other.Incident.ID = "second"
	other.Incident.Metric.Labels = map[string]string{}
	if minimal.issueKey() != other.issueKey() {
		t.Fatal("named resource recurrence or empty labels changed identity")
	}
	other.Incident.ResourceName = "worker"
	if minimal.issueKey() == other.issueKey() {
		t.Fatal("location-only labels merged different named resources")
	}
	minimal.Incident.ResourceName = "Unknown resource"
	other = minimal
	other.Incident.ID = "second"
	if minimal.issueKey() == other.issueKey() {
		t.Fatal("incomplete resource identity merged unrelated occurrences")
	}
}

func TestFilters(t *testing.T) {
	filters, err := ParseFilters(
		url.Values{"q": {"cpu%' OR 1=1--"}, "status": {"open"}, "page": {"2"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	where, args := filters.Where()
	if strings.Contains(where, "1=1") || len(args) != 2 ||
		!strings.Contains(args[0].(string), `\%`) {
		t.Fatalf("unsafe filter: %s %v", where, args)
	}
	for _, values := range []url.Values{
		{"limit": {"500"}},
		{"status": {"oops"}},
		{"from": {"bad"}},
		{"from": {"2026-02-02T00:00:00Z"}, "to": {"2026-01-01T00:00:00Z"}},
	} {
		if _, err := ParseFilters(values); err == nil {
			t.Errorf("accepted invalid filters: %v", values)
		}
	}
}
