package monitor

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const limitKey = "limit"

type Filters struct {
	Query, Status, Severity, Project string
	From, To                         *time.Time
	Page, Limit                      int
}

func ParseFilters(values url.Values) (Filters, error) {
	filters := Filters{
		Query:    values.Get("q"),
		Status:   values.Get("status"),
		Severity: values.Get("severity"),
		Project:  values.Get("project"),
		Page:     1,
		Limit:    25,
	}
	if len(filters.Query) > 200 || len(filters.Project) > 256 {
		return filters, errors.New("search or project is too long")
	}
	if !oneOf(filters.Status, "", "all", "open", "acknowledged", "resolved") ||
		!oneOf(filters.Severity, "", "all", "critical", "warning", "info") {
		return filters, errors.New("invalid status or severity")
	}
	for key, target := range map[string]*int{"page": &filters.Page, limitKey: &filters.Limit} {
		if value := values.Get(key); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || (key == limitKey && number > 100) || number > 100000 {
				return filters, errors.New("invalid pagination")
			}
			*target = number
		}
	}
	for key, target := range map[string]**time.Time{"from": &filters.From, "to": &filters.To} {
		if value := values.Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return filters, errors.New("dates must be RFC3339 timestamps")
			}
			*target = &parsed
		}
	}
	if filters.From != nil && filters.To != nil && filters.From.After(*filters.To) {
		return filters, errors.New("from must precede to")
	}
	return filters, nil
}

func (filters Filters) Where() (string, []any) {
	clauses := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, strings.ReplaceAll(clause, "?", fmt.Sprintf("$%d", len(args))))
	}
	if filters.Query != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filters.Query)
		add(
			"(policy_name ILIKE ? OR summary ILIKE ? OR resource_name ILIKE ? OR incident_id ILIKE ?)",
			"%"+escaped+"%",
		)
	}
	if !oneOf(filters.Status, "", "all") {
		add("status = ?", filters.Status)
	}
	if !oneOf(filters.Severity, "", "all") {
		add("severity = ?", filters.Severity)
	}
	if filters.Project != "" {
		add("project_id = ?", filters.Project)
	}
	if filters.From != nil {
		add("started_at >= ?", *filters.From)
	}
	if filters.To != nil {
		add("started_at <= ?", *filters.To)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
