package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository/mysql"
)

// This command returns the pinned binary's maintenance contract before any
// configuration loading, SQL connection, component startup or network listener.
// It never executes the returned statements. Schema installation belongs to an
// explicit stopped-writer maintenance workflow with its own backup and journal.
func activityArchiveCommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "--activity-archive-") {
		return false, nil
	}
	if args[0] != "--activity-archive-plan-v1" || len(args) != 3 {
		return true, errors.New("usage: --activity-archive-plan-v1 DATABASE TABLE_PREFIX")
	}
	plan, err := mysql.ActivityArchivePlan(args[1], args[2])
	if err != nil {
		return true, err
	}
	type trigger struct {
		Name string `json:"name"`
		DDL  string `json:"ddl"`
	}
	manifest := struct {
		Format         string    `json:"format"`
		Database       string    `json:"database"`
		Prefix         string    `json:"prefix"`
		ContractSHA256 string    `json:"contractSHA256"`
		Hot            string    `json:"hot"`
		Cold           string    `json:"cold"`
		Receipts       string    `json:"receipts"`
		State          string    `json:"state"`
		HotDDL         string    `json:"hotDDL"`
		ReceiptDDL     string    `json:"receiptDDL"`
		StateDDL       string    `json:"stateDDL"`
		BackfillSQL    string    `json:"backfillSQL"`
		Triggers       []trigger `json:"triggers"`
	}{Format: mysql.ActivityArchiveFormat, Database: plan.Database, Prefix: plan.Prefix,
		ContractSHA256: plan.ContractSHA256, Hot: plan.Hot, Cold: plan.Cold,
		Receipts: plan.Receipts, State: plan.State, HotDDL: plan.HotDDL(),
		ReceiptDDL: plan.ReceiptDDL, StateDDL: plan.StateDDL, BackfillSQL: plan.BackfillSQL()}
	for _, definition := range plan.Triggers {
		manifest.Triggers = append(manifest.Triggers, trigger{definition.Name, plan.TriggerDDL(definition)})
	}
	return true, json.NewEncoder(out).Encode(manifest)
}
