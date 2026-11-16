package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestActivityArchiveCommandIsPureAndMatchesRepository(t *testing.T) {
	var out bytes.Buffer
	handled, err := activityArchiveCommand([]string{"--activity-archive-plan-v1", "fixture_schema", "arc_1"}, &out)
	if !handled || err != nil {
		t.Fatalf("plan: %t %v", handled, err)
	}
	var manifest struct {
		Format, Database, Prefix, ContractSHA256, Hot, Cold, Receipts, State string
		HotDDL, ReceiptDDL, StateDDL, BackfillSQL                            string
		Triggers                                                             []struct{ Name, DDL string }
	}
	if err := json.Unmarshal(out.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	plan, err := mysql.ActivityArchivePlan("fixture_schema", "arc_1")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Format != mysql.ActivityArchiveFormat || manifest.Database != plan.Database || manifest.Prefix != plan.Prefix ||
		manifest.ContractSHA256 != plan.ContractSHA256 || manifest.Hot != plan.Hot || manifest.Cold != plan.Cold ||
		manifest.Receipts != plan.Receipts || manifest.State != plan.State || manifest.HotDDL != plan.HotDDL() ||
		manifest.ReceiptDDL != plan.ReceiptDDL || manifest.StateDDL != plan.StateDDL || manifest.BackfillSQL != plan.BackfillSQL() ||
		len(manifest.Triggers) != len(plan.Triggers) {
		t.Fatal("binary plan differs from repository contract")
	}
	for i, got := range manifest.Triggers {
		if got.Name != plan.Triggers[i].Name || got.DDL != plan.TriggerDDL(plan.Triggers[i]) {
			t.Fatal("binary plan changes trigger definition")
		}
	}
	first := append([]byte(nil), out.Bytes()...)
	out.Reset()
	if _, err := activityArchiveCommand([]string{"--activity-archive-plan-v1", "fixture_schema", "arc_1"}, &out); err != nil || !reflect.DeepEqual(first, out.Bytes()) {
		t.Fatal("plan is nondeterministic", err)
	}
}

func TestActivityArchiveCommandRejectsMalformedRequestsBeforeStartup(t *testing.T) {
	for _, args := range [][]string{
		{"--activity-archive-plan-v1"}, {"--activity-archive-plan-v2", "db", "dnf"},
		{"--activity-archive-plan-v1=db", "dnf"}, {"--activity-archive-plan-v1", "db;unsafe", "dnf"},
		{"--activity-archive-plan-v1", "db", "dnf", "unexpected"},
	} {
		var out bytes.Buffer
		if handled, err := activityArchiveCommand(args, &out); !handled || err == nil || out.Len() != 0 {
			t.Fatalf("malformed maintenance request fell through to startup: %q %t %v", args, handled, err)
		}
	}
	for _, args := range [][]string{nil, {"--config", "config.yml"}} {
		var out bytes.Buffer
		if handled, err := activityArchiveCommand(args, &out); handled || err != nil || out.Len() != 0 {
			t.Fatalf("normal startup changed: %q %t %v", args, handled, err)
		}
	}
	broken := archivePlanBrokenWriter{}
	if handled, err := activityArchiveCommand([]string{"--activity-archive-plan-v1", "db", "dnf"}, broken); !handled || !errors.Is(err, errArchivePlanWrite) {
		t.Fatal("truncated output reported as success", err)
	}
}

var errArchivePlanWrite = errors.New("fixture writer failure")

type archivePlanBrokenWriter struct{}

func (archivePlanBrokenWriter) Write([]byte) (int, error) { return 0, errArchivePlanWrite }
