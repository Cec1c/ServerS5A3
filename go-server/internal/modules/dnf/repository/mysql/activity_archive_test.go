package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestActivityArchivePlanDoesNotAlterDefaultSchema(t *testing.T) {
	plan, err := ActivityArchivePlan("isolated_activity", "unit_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Triggers) != 8 || len(plan.ContractSHA256) != 64 || !strings.Contains(plan.HotDDL(), "COMMENT='activity-archive/v1'") {
		t.Fatal("incomplete archive contract")
	}
	for _, trigger := range plan.Triggers {
		if !strings.Contains(plan.TriggerDDL(trigger), "`isolated_activity`.") {
			t.Fatal("trigger escaped its database")
		}
	}
	statements, err := MySQLSchema(SchemaOptions{DatabasePlan: r.DatabasePlan{WriteDatabases: []string{"isolated_activity"}}, AutoCreate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if strings.Contains(statement, activityArchiveCold) || strings.Contains(statement, activityArchiveReceipts) || strings.Contains(statement, activityArchiveState) {
			t.Fatal("normal schema initialization installs archive data layout")
		}
	}
	for _, database := range []string{"a;b", "a.b", "a`b"} {
		if _, err := ActivityArchivePlan(database, "dnf"); err == nil {
			t.Fatal("unsafe schema name accepted")
		}
	}
}

func TestActivityArchiveQualificationIsReadOnlyAndCancellable(t *testing.T) {
	db := &fakeSQLDB{}
	options := MySQLGroupOptions{DatabasePlan: r.DatabasePlan{WriteDatabases: []string{"a"}, ReadDatabases: []string{"b"}}}
	qualified, err := QualifyActivityArchive(context.Background(), db, options)
	if err != nil || qualified.activityArchive {
		t.Fatalf("legacy qualification: %v", err)
	}
	for _, call := range db.execs {
		if !strings.HasPrefix(call.Query, "SELECT ") {
			t.Fatalf("qualification wrote: %s", call.Query)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The SQL double ignores Context, so explicitly check cancellation at the
	// public gate before it can issue any metadata request.
	_, err = QualifyActivityArchive(ctx, db, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was ignored: %v", err)
	}
}
