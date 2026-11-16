package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"reflect"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
	repo "longheng.io/server/internal/modules/dnf/repository"
	mysqlrepo "longheng.io/server/internal/modules/dnf/repository/mysql"
)

const optimizationSchemaFlag = "--optimization-schema-v1"

type optimizationSchemaRequest struct {
	Mode           string                              `json:"mode"`
	DSN            string                              `json:"dsn,omitempty"`
	TablePrefix    string                              `json:"tablePrefix"`
	ReadDatabases  []string                            `json:"readDatabases"`
	WriteDatabases []string                            `json:"writeDatabases"`
	PlanSHA256     string                              `json:"planSha256,omitempty"`
	ExpectedBefore *mysqlrepo.OptimizationSchemaReport `json:"expectedBefore,omitempty"`
}

// This explicitly selected entry runs before application assembly. It starts
// no gameplay/listeners and reads connection credentials only from stdin.
func runOptimizationSchemaCommand(ctx context.Context, args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != optimizationSchemaFlag {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("optimization schema command takes one bounded JSON request on stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(raw) > 65536 {
		return true, errors.New("invalid optimization schema request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request optimizationSchemaRequest
	if err := decoder.Decode(&request); err != nil {
		return true, errors.New("invalid optimization schema request")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return true, errors.New("trailing optimization schema request")
	}
	if request.Mode != "plan" && request.Mode != "inspect" && request.Mode != "apply" {
		return true, errors.New("unknown optimization schema mode")
	}
	options := mysqlrepo.SchemaOptions{TablePrefix: request.TablePrefix, DatabasePlan: repo.DatabasePlan{ReadDatabases: request.ReadDatabases, WriteDatabases: request.WriteDatabases}}
	plan, err := mysqlrepo.PlanOptimizationSchema(options)
	if err != nil {
		return true, err
	}
	if request.Mode == "plan" {
		if request.DSN != "" || request.PlanSHA256 != "" || request.ExpectedBefore != nil {
			return true, errors.New("plan mode does not accept connection or approval fields")
		}
		return true, json.NewEncoder(output).Encode(struct {
			Plan   mysqlrepo.OptimizationSchemaPlan `json:"plan"`
			SHA256 string                           `json:"sha256"`
		}{plan, plan.SHA256()})
	}
	if request.PlanSHA256 != plan.SHA256() {
		return true, errors.New("optimization schema plan digest differs")
	}
	if (request.Mode == "apply") != (request.ExpectedBefore != nil) {
		return true, errors.New("apply requires the inspected before-state")
	}
	cfg, err := driver.ParseDSN(request.DSN)
	if err != nil || request.DSN == "" {
		return true, errors.New("invalid maintenance connection options")
	}
	local := false
	if cfg.Net == "tcp" {
		host, _, splitErr := net.SplitHostPort(cfg.Addr)
		ip := net.ParseIP(host)
		local = splitErr == nil && ip != nil && ip.IsLoopback()
	} else if cfg.Net == "unix" {
		local = path.IsAbs(cfg.Addr) && path.Clean(cfg.Addr) == cfg.Addr && !strings.ContainsRune(cfg.Addr, '\x00')
	}
	if !local || cfg.MultiStatements {
		return true, errors.New("maintenance requires loopback TCP or an absolute local Unix socket without multi-statements")
	}
	if cfg.DBName != "" {
		found := false
		for _, name := range append(append([]string{}, plan.ReadDatabases...), plan.WriteDatabases...) {
			found = found || name == cfg.DBName
		}
		if !found {
			return true, errors.New("connection default database is outside the maintenance plan")
		}
	}
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 2 * time.Minute
	cfg.WriteTimeout = 10 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return true, errors.New("cannot open maintenance connection")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	limit := 20 * time.Second
	if request.Mode == "apply" {
		limit = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	before, err := mysqlrepo.InspectOptimizationSchema(ctx, db, options)
	if err != nil {
		return true, fmt.Errorf("inspect optimization schema: %w", err)
	}
	if request.Mode == "inspect" {
		return true, json.NewEncoder(output).Encode(before)
	}
	if !reflect.DeepEqual(before, *request.ExpectedBefore) {
		return true, errors.New("optimization schema changed after inspection")
	}
	options.AutoCreate = true
	after, err := mysqlrepo.ApplyOptimizationSchema(ctx, db, options)
	if err != nil {
		return true, fmt.Errorf("apply optimization schema; inspect before a new repair task: %w", err)
	}
	if err := json.NewEncoder(output).Encode(after); err != nil {
		return true, err
	}
	if !after.Ready {
		return true, errors.New("write targets processed; read shard readiness is still pending")
	}
	return true, nil
}
