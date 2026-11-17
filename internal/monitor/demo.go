package monitor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

func (store *Store) SeedDemo(ctx context.Context) error {
	const (
		resourceCloudRun = "cloud_run_revision"
		resourceCompute  = "gce_instance"
		projectCommerce  = "commerce-production"
		projectPlatform  = "platform-production"
	)
	var total int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM issues`).Scan(&total); err != nil {
		return err
	}
	if total > 0 {
		return nil
	}
	templates := []struct{ policy, resource, resourceType, project, severity, summary string }{
		{
			"High CPU utilization",
			"checkout-api / europe-west1",
			resourceCloudRun,
			projectCommerce,
			severityCritical,
			"CPU utilization exceeded 90% for 5 minutes. Current value: 96.4%. " +
				"The checkout service is experiencing sustained compute pressure.",
		},
		{
			"Elevated error rate",
			"payments-api / europe-west1",
			resourceCloudRun,
			projectCommerce,
			severityCritical,
			"HTTP 5xx responses exceeded 2% of requests over a 5-minute rolling window. Current error rate: 4.8%.",
		},
		{
			"Database connections near limit",
			"inventory-db / europe-west4",
			resourceCompute,
			projectPlatform,
			severityWarning,
			"Active PostgreSQL connections reached 176 of 200 available connections. Review connection pool limits.",
		},
		{
			"High request latency",
			"catalog-api / us-central1",
			resourceCloudRun,
			projectCommerce,
			severityWarning,
			"The 95th percentile response time exceeded 1.5 seconds. Current value: 2.3 seconds.",
		},
		{
			"Memory utilization above threshold",
			"worker-pool / europe-west1",
			resourceCompute,
			projectPlatform,
			severityWarning,
			"Memory utilization exceeded 80% for 10 minutes. Current usage: 86.2%.",
		},
		{
			"Uptime check failed",
			"storefront / global",
			"uptime_url",
			"commerce-staging",
			severityCritical,
			"The HTTPS uptime check failed from two probe locations. The endpoint returned HTTP 503.",
		},
		{
			"Disk space running low",
			"logging-node-02 / europe-west4",
			resourceCompute,
			projectPlatform,
			severityWarning,
			"The data volume has less than 15% free disk space. Current free space: 11.6%.",
		},
		{
			"Deployment availability changed",
			"search-api / europe-west1",
			resourceCloudRun,
			"commerce-staging",
			severityInfo,
			"A revision availability condition changed during the latest deployment rollout.",
		},
	}
	now := time.Now().UTC()
	for index := 0; index < 38; index++ {
		template := templates[index%len(templates)]
		started := now.Add(-time.Duration(index*37+3) * time.Minute)
		incident := map[string]any{
			"incident_id":   fmt.Sprintf("demo-%04d", index+1),
			"state":         statusOpen,
			"started_at":    started.Unix(),
			"policy_name":   template.policy,
			"resource_name": template.resource,
			"severity":      template.severity,
			"summary":       template.summary,
			"resource": map[string]any{
				"type": template.resourceType,
				"labels": map[string]string{
					"project_id":    template.project,
					"location":      "europe-west1",
					"demo_resource": fmt.Sprintf("resource-%02d", index),
				},
			},
			"documentation": map[string]string{
				"content": "Review the affected resource metrics, recent deployments, and upstream dependencies. " +
					"Escalate to the owning service team if the condition persists.",
			},
		}
		if index%5 == 4 || index >= 24 {
			incident["state"] = stateClosed
			incident["ended_at"] = started.Add(20 * time.Minute).Unix()
		}
		payload, _ := json.Marshal(map[string]any{"version": "1.2", "incident": incident})
		body, _ := json.Marshal(
			map[string]any{
				"subscription": "projects/demo/subscriptions/monitoring-alerts",
				"message": map[string]string{
					"messageId": fmt.Sprintf("demo-message-%d", index),
					"data":      base64.StdEncoding.EncodeToString(payload),
				},
			},
		)
		alert, err := ParseAlert(body)
		if err != nil {
			return err
		}
		id, _, err := store.Ingest(ctx, alert)
		if err != nil {
			return err
		}
		if index < 24 && index%5 == 2 {
			if err := store.SetAcknowledged(ctx, id, true, "demo-operator"); err != nil {
				return err
			}
		}
	}
	return nil
}
