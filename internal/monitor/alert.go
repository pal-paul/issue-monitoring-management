package monitor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	statusOpen         = "open"
	stateClosed        = "closed"
	statusAcknowledged = "acknowledged"
	statusResolved     = "resolved"
	severityCritical   = "critical"
	severityWarning    = "warning"
	severityInfo       = "info"
)

type Incident struct {
	ID               string            `json:"incident_id"`
	State            string            `json:"state"`
	StartedAt        json.Number       `json:"started_at"`
	EndedAt          json.Number       `json:"ended_at"`
	Summary          string            `json:"summary"`
	PolicyName       string            `json:"policy_name"`
	PolicyID         string            `json:"policy_id"`
	ResourceName     string            `json:"resource_name"`
	URL              string            `json:"url"`
	Severity         string            `json:"severity"`
	PolicyUserLabels map[string]string `json:"policy_user_labels"`
	Condition        struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"condition"`
	Metric struct {
		Type        string            `json:"type"`
		DisplayName string            `json:"displayName"`
		Labels      map[string]string `json:"labels"`
	} `json:"metric"`
	Resource struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Documentation struct {
		Content string `json:"content"`
	} `json:"documentation"`
}

type Envelope struct {
	Message struct {
		ID   string `json:"messageId"`
		Data string `json:"data"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

type Alert struct {
	Envelope  Envelope
	Incident  Incident
	Payload   json.RawMessage
	Severity  string
	Project   string
	StartedAt time.Time
	EndedAt   *time.Time
	EventAt   time.Time
}

func (alert Alert) issueKey() string {
	incident := alert.Incident
	policy := incident.PolicyID
	if policy == "" {
		policy = incident.PolicyName
	}
	condition := incident.Condition.Name
	if condition == "" {
		condition = incident.Condition.DisplayName
	}
	metric := incident.Metric.Type
	if metric == "" {
		metric = incident.Metric.DisplayName
	}
	labels := make(map[string]string)
	hasResourceIdentity := false
	for name, value := range incident.Resource.Labels {
		if name != "project_id" &&
			(incident.Resource.Type != "cloud_run_revision" || name != "revision_name") {
			labels[name] = value
			if value != "" && name != "location" && name != "zone" && name != "region" {
				hasResourceIdentity = true
			}
		}
	}
	resource := incident.ResourceName
	if hasResourceIdentity {
		resource = ""
	}
	var key []byte
	metricLabels := incident.Metric.Labels
	if len(metricLabels) == 0 {
		metricLabels = nil
	}
	if policy == "" || policy == "Monitoring alert" || alert.Project == "Unknown project" ||
		(!hasResourceIdentity && (resource == "" || resource == "Unknown resource")) {
		key, _ = json.Marshal([]string{"occurrence", alert.Project, incident.ID})
	} else {
		key, _ = json.Marshal([]any{
			"issue-v1", alert.Project, policy, condition, metric,
			incident.Resource.Type, resource, labels, metricLabels,
		})
	}
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:])
}

func ParseAlert(body []byte) (Alert, error) {
	var alert Alert
	if err := json.Unmarshal(body, &alert.Envelope); err != nil {
		return alert, errors.New("invalid Pub/Sub envelope")
	}
	if alert.Envelope.Message.ID == "" || len(alert.Envelope.Message.ID) > 512 ||
		alert.Envelope.Subscription == "" ||
		len(alert.Envelope.Subscription) > 2048 {
		return alert, errors.New("messageId and subscription are required")
	}
	data := alert.Envelope.Message.Data
	if len(data) == 0 || len(data) > 1_000_000 {
		return alert, errors.New("alert data is empty or too large")
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil {
		return alert, errors.New("invalid base64 alert data")
	}
	var notification struct {
		Incident Incident `json:"incident"`
	}
	if err := json.Unmarshal(payload, &notification); err != nil {
		return alert, errors.New("invalid Monitoring notification")
	}
	incident := notification.Incident
	if incident.ID == "" || len(incident.ID) > 512 ||
		(incident.State != statusOpen && incident.State != stateClosed) {
		return alert, errors.New("incident_id and open/closed state are required")
	}
	started, err := unixTime(incident.StartedAt)
	if err != nil {
		return alert, fmt.Errorf("started_at: %w", err)
	}
	alert.StartedAt, alert.EventAt = started, started
	if incident.EndedAt != "" && incident.EndedAt != "0" {
		ended, err := unixTime(incident.EndedAt)
		if err != nil || ended.Before(started) {
			return alert, errors.New("invalid ended_at")
		}
		alert.EndedAt = &ended
	}
	if incident.State == stateClosed {
		if alert.EndedAt == nil {
			return alert, errors.New("closed incident requires ended_at")
		}
		alert.EventAt = *alert.EndedAt
	}
	if incident.PolicyName == "" {
		incident.PolicyName = "Monitoring alert"
	}
	if incident.ResourceName == "" {
		incident.ResourceName = "Unknown resource"
	}
	severity := incident.Severity
	if severity == "" {
		severity = incident.PolicyUserLabels["severity"]
	}
	switch strings.ToLower(severity) {
	case severityCritical, "error":
		alert.Severity = severityCritical
	case severityInfo, "informational":
		alert.Severity = severityInfo
	default:
		alert.Severity = severityWarning
	}
	alert.Project = incident.Resource.Labels["project_id"]
	if alert.Project == "" {
		alert.Project = "Unknown project"
	}
	if parsed, err := url.Parse(
		incident.URL,
	); err != nil || parsed.Scheme != "https" ||
		parsed.Host != "console.cloud.google.com" {
		incident.URL = ""
	}
	alert.Incident, alert.Payload = incident, payload
	return alert, nil
}

func unixTime(number json.Number) (time.Time, error) {
	seconds, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || seconds < 0 || seconds > 253402300799 {
		return time.Time{}, errors.New("expected Unix seconds")
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func ShouldApply(last time.Time, incoming Alert) bool {
	return incoming.EventAt.After(last) ||
		(incoming.EventAt.Equal(last) && incoming.Incident.State == stateClosed)
}
