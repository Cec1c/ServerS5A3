package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startup50SourceFunction(t *testing.T, file, name string) string {
	t.Helper()
	dir := os.Getenv("STARTUP50_SOURCE_DIR")
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fs := token.NewFileSet()
	tree, err := parser.ParseFile(fs, path, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range tree.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			a := fs.Position(fn.Pos()).Offset
			b := fs.Position(fn.End()).Offset
			return string(data[a:b])
		}
	}
	t.Fatalf("normal startup capability absent: %s/%s", file, name)
	return ""
}
func startup50Require(t *testing.T, text string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(text, f) {
			t.Fatalf("required normal startup contract absent: %s", f)
		}
	}
}
func TestStartup50ContractExplicitNormalStartFlag(t *testing.T) {
	text := startup50SourceFunction(t, "main.go", "parseStartOptions")
	startup50Require(t, text, "use-running-database", "false", "parseFlags")
	dispatch := startup50SourceFunction(t, "main.go", "runCLI")
	startup50Require(t, dispatch, "parseStartOptions(args[1:], stderr)", "controller.startWithOptions(ctx, opts)")
}
func TestStartup50ContractDoctorBeforeRealServerSpawn(t *testing.T) {
	text := startup50SourceFunction(t, "controller.go", "startWithOptions")
	startup50Require(t, text, "configureStartupDatabase(cfg, opts.useRunningDatabase)", "beginStartupDatabase(ctx, cfg, c.startDatabase)", "c.runDoctor(ctx, cfg, doctorOptions{})", "c.startServerProcess(cfg)", "if databaseStarted")
	if strings.Index(text, "c.runDoctor(ctx, cfg, doctorOptions{})") > strings.Index(text, "c.startServerProcess(cfg)") {
		t.Fatal("server spawned before normal DB preflight")
	}
}
func TestStartup50ContractNoSchemaInitializationOnReuse(t *testing.T) {
	text := startup50SourceFunction(t, "config.go", "renderLogicConfig")
	startup50Require(t, text, "auto_create_schema = %t", "!cfg.UseRunningDatabase", "create_databases = false")
}
func TestStartup50ContractStopUsesServerLifecycle(t *testing.T) {
	text := startup50SourceFunction(t, "controller.go", "stop")
	startup50Require(t, text, "c.loadManagedProcess", "c.gracefulStop", "shouldStopStartupDatabase(keepDatabase, found, state)")
}
func TestStartup50ContractOnlyActualServerStartupPersistsMode(t *testing.T) {
	text := startup50SourceFunction(t, "controller.go", "startServerProcess")
	startup50Require(t, text, "cmd.Start()", "UsesRunningDatabase:", "cfg.UseRunningDatabase", "c.verifyStartedProcess(&state)", "writeJSON(c.paths.processState")
	if strings.Index(text, "cmd.Start()") > strings.Index(text, "UsesRunningDatabase:") {
		t.Fatal("mode recorded before actual child start")
	}
	if strings.Index(text, "c.verifyStartedProcess(&state)") > strings.Index(text, "writeJSON(c.paths.processState") {
		t.Fatal("unverified process state persisted")
	}
}
func TestStartup50ContractActiveConfigUsesSavedMode(t *testing.T) {
	text := startup50SourceFunction(t, "controller.go", "validateRunningServerConfig")
	startup50Require(t, text, "configureStartupDatabase(cfg, state.UsesRunningDatabase)", "desiredServerRuntimeConfigSHA256(cfg)", "currentServerRuntimeConfigSHA256(c.paths, cfg)", "state.RuntimeConfigSHA256")
}
func TestStartup50ContractReuseDoesNotGenerateOwnedMySQLConfig(t *testing.T) {
	text := startup50SourceFunction(t, "config.go", "generateRuntimeConfigs")
	startup50Require(t, text, "if !cfg.UseRunningDatabase", "writeFile(paths.mysqlConfig")
}
