package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestOptimizationSchemaPlanKeepsReadReplicasReadOnly(t *testing.T) {
	options := SchemaOptions{DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"replica", "primary"}, WriteDatabases: []string{"primary"}}}
	plan, err := PlanOptimizationSchema(options)
	if err != nil || len(plan.Statements) != 5 || plan.TablePrefix != "dnf" || len(plan.SHA256()) != 64 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, query := range plan.Statements {
		if strings.Contains(query, "`replica`") || !strings.Contains(query, "`primary`") {
			t.Fatalf("unexpected write target: %s", query)
		}
	}
	options.DatabasePlan.ReadDatabases = []string{"primary", "replica"}
	again, err := PlanOptimizationSchema(options)
	if err != nil || again.SHA256() != plan.SHA256() {
		t.Fatal("canonical plan is unstable", err)
	}
	if plan.Statements[4] != recoveryIndexDDL("primary", "dnf") {
		t.Fatal("maintenance index differs from normal additive schema")
	}
	if _, err := InspectOptimizationSchema(context.Background(), nil, options); !errors.Is(err, ErrMySQLDBRequired) {
		t.Fatal(err)
	}
	if _, err := ApplyOptimizationSchema(context.Background(), nil, options); !errors.Is(err, ErrSchemaConfigInvalid) {
		t.Fatal("unapproved apply", err)
	}
}

func TestOptimizationSchemaPlanRejectsAmbiguousScope(t *testing.T) {
	for name, options := range map[string]SchemaOptions{
		"no_reads":        {DatabasePlan: r.DatabasePlan{WriteDatabases: []string{"a"}}},
		"no_writes":       {DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"a"}}},
		"bad_database":    {DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"a"}, WriteDatabases: []string{"a;DROP"}}},
		"bad_prefix":      {TablePrefix: strings.Repeat("x", 64), DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"a"}, WriteDatabases: []string{"a"}}},
		"create_database": {CreateDatabases: true, DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"a"}, WriteDatabases: []string{"a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanOptimizationSchema(options); err == nil {
				t.Fatal("invalid maintenance scope accepted")
			}
		})
	}
}
