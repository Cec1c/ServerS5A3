package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	mysqlrepo "longheng.io/server/internal/modules/dnf/repository/mysql"
)

type optimizationUnreadableInput struct{}

func (optimizationUnreadableInput) Read([]byte) (int, error) {
	panic("ordinary startup read maintenance input")
}

func TestOptimizationSchemaCommandDoesNotEnterOrdinaryStartup(t *testing.T) {
	for _, args := range [][]string{nil, {"-config", "normal.toml"}} {
		handled, err := runOptimizationSchemaCommand(context.Background(), args, optimizationUnreadableInput{}, io.Discard)
		if handled || err != nil {
			t.Fatal(handled, err)
		}
	}
}

func TestOptimizationSchemaCommandPlanAndRefusals(t *testing.T) {
	request := optimizationSchemaRequest{Mode: "plan", ReadDatabases: []string{"fixture"}, WriteDatabases: []string{"fixture"}}
	raw, _ := json.Marshal(request)
	var output bytes.Buffer
	handled, err := runOptimizationSchemaCommand(context.Background(), []string{optimizationSchemaFlag}, bytes.NewReader(raw), &output)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	var plan struct {
		Plan   mysqlrepo.OptimizationSchemaPlan `json:"plan"`
		SHA256 string                           `json:"sha256"`
	}
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil || plan.SHA256 != plan.Plan.SHA256() {
		t.Fatal("invalid pure plan output", err)
	}
	for name, input := range map[string]string{
		"unknown_field": `{"mode":"plan","sql":"DROP TABLE anything"}`,
		"trailing":      string(raw) + `{}`,
		"large":         strings.Repeat(" ", 65537),
		"bad_mode":      `{"mode":"all"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runOptimizationSchemaCommand(context.Background(), []string{optimizationSchemaFlag}, strings.NewReader(input), io.Discard); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	for name, change := range map[string]func(*optimizationSchemaRequest){
		"digest": func(r *optimizationSchemaRequest) { r.Mode = "inspect"; r.PlanSHA256 = "wrong" },
		"remote": func(r *optimizationSchemaRequest) {
			r.Mode = "inspect"
			r.PlanSHA256 = plan.SHA256
			r.DSN = "root:secret-not-for-output@tcp(192.0.2.1:3306)/"
		},
		"multi": func(r *optimizationSchemaRequest) {
			r.Mode = "inspect"
			r.PlanSHA256 = plan.SHA256
			r.DSN = "root:secret-not-for-output@tcp(127.0.0.1:3306)/?multiStatements=true"
		},
		"relative_socket": func(r *optimizationSchemaRequest) {
			r.Mode = "inspect"
			r.PlanSHA256 = plan.SHA256
			r.DSN = "root:secret-not-for-output@unix(relative.sock)/"
		},
		"socket_parent": func(r *optimizationSchemaRequest) {
			r.Mode = "inspect"
			r.PlanSHA256 = plan.SHA256
			r.DSN = "root:secret-not-for-output@unix(/tmp/../db.sock)/"
		},
		"unbound_apply":    func(r *optimizationSchemaRequest) { r.Mode = "apply"; r.PlanSHA256 = plan.SHA256 },
		"plan_credentials": func(r *optimizationSchemaRequest) { r.DSN = "root:secret-not-for-output@tcp(127.0.0.1:3306)/" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			change(&candidate)
			value, _ := json.Marshal(candidate)
			var reply bytes.Buffer
			_, err := runOptimizationSchemaCommand(context.Background(), []string{optimizationSchemaFlag}, bytes.NewReader(value), &reply)
			if err == nil || strings.Contains(err.Error(), "secret-not-for-output") || reply.Len() != 0 {
				t.Fatalf("refusal leaked or failed: %v", err)
			}
		})
	}
}
