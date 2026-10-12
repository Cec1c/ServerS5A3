package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// The synthetic runtime tree below mirrors what the normal controller renders for
// the local profile. channel_info.etc is rewritten per test so the catalog derives
// exactly one game port matching an owned ephemeral listener; only 127.0.0.1:0 is
// ever used.
const gmRegressServiceConfig = `[service]
name = "dnfbridge"
node_id = "dnfbridge-local-1"
environment = "local"
public_addr = "192.168.1.113:7001"
private_addr = "127.0.0.1:18111"

[cluster]
id = "local"
gid = 2
virtual_gid = 2
shard_id = 9999
zone = "local"
version = "local"
data_version = "local"

[admin]
listen = "127.0.0.1:18111"
token = "adm_test"

[metrics]
prometheus_enabled = true
require_admin_token = false

[pvf]
enabled = true
path = "data/dnf/Script.pvf"
max_bytes = 536870912
preload_chunks = false

[server_group]
enabled = true
store_kind = "file"
plan_file = "configs/servergroup/plan.json"

[topology]
enabled = false

[idempotency]
kind = "memory"

[registry]
kind = "memory"

[bus]
kind = "memory"

[cache]
kind = "memory"

[lock]
kind = "memory"

[presence]
kind = "memory"

[worker]
size = 8
queue = 256

[profile_store]
store_kind = "memory"
`

const gmRegressLogicConfig = `[repository]
enabled = true
mysql_dsn = "root:db_test@tcp(127.0.0.1:13306)/dnf_local?parseTime=true&charset=utf8mb4,utf8"
mysql_max_open_conns = 32
mysql_max_idle_conns = 8
mysql_conn_max_lifetime_seconds = 300
shard_id = "9999"
table_prefix = "dnf"
server_group_plan_file = "configs/servergroup/plan.json"
auto_create_schema = true
csharp_legacy_schema = true
create_databases = false
`

const gmRegressPlanJSON = `{
  "version": "dnf90-local-v1",
  "shards": [
    {
      "id": "9999",
      "group_id": "logic-local-1",
      "state": "open",
      "open_at": "2026-01-01T00:00:00Z",
      "public_open_at": "2026-01-01T00:00:00Z"
    }
  ],
  "groups": [
    {
      "id": "logic-local-1",
      "service": "logic",
      "member_id": "logic-local-1",
      "state": "open"
    }
  ],
  "routes": [
    {
      "feature": "dnf_repository",
      "shard_id": "9999",
      "group_id": "logic-local-1",
      "state": "open",
      "meta": {
        "dnf.repository.read_database": "dnf_local",
        "dnf.repository.write_database": "dnf_local"
      }
    }
  ]
}
`

func gmRegressListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func gmRegressDoctorRoot(t *testing.T, adminAddr, gameAddr string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"configs/dnfbridge.toml":        strings.Replace(gmRegressServiceConfig, `listen = "127.0.0.1:18111"`, `listen = "`+adminAddr+`"`, 1),
		"configs/dnf/logic.toml":        gmRegressLogicConfig,
		"configs/servergroup/plan.json": gmRegressPlanJSON,
		"data/dnf/Script.pvf":           "PUBLIC_SYNTHETIC_FILE_NOT_REAL_PVF",
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, port, err := net.SplitHostPort(gameAddr)
	if err != nil {
		t.Fatal(err)
	}
	numeric, err := strconv.Atoi(port)
	if err != nil || numeric <= dnfenum.GamePortBase {
		t.Fatal("owned ephemeral game port cannot map to a synthetic channel")
	}
	channel := fmt.Sprintf("[dungeon]\n[gmregress]\n<4::gmregress>\n100\n[/dungeon]\n[server]\n1\n%d <4::gmregress> 1 [gmregress] 0 0\n[/server]\n", numeric-dnfenum.GamePortBase)
	if err := os.WriteFile(filepath.Join(root, "data/dnf/channel_info.etc"), []byte(channel), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func gmRegressDoctorOptions(t *testing.T, admin, channel, game, gm string, skipPorts, expectListen bool) options {
	t.Helper()
	root := gmRegressDoctorRoot(t, admin, game)
	t.Setenv("DNFBRIDGE_GM_LISTEN", gm)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	return options{
		root:         root,
		servicePath:  "configs/dnfbridge.toml",
		logicPath:    "configs/dnf/logic.toml",
		channelPath:  "data/dnf/channel_info.etc",
		channelAddr:  channel,
		gameHost:     "127.0.0.1",
		timeout:      50 * time.Millisecond,
		skipDatabase: true,
		skipPorts:    skipPorts,
		expectListen: expectListen,
	}
}

// A fully distinct, free plan must pass the real doctor preflight.
func TestNormalGMDoctorAcceptsDistinctFreeEndpoints(t *testing.T) {
	admin, channel, game, gm := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
	opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), gm.Addr().String(), false, false)
	_ = admin.Close()
	_ = channel.Close()
	_ = game.Close()
	_ = gm.Close()
	if err := run(opts); err != nil {
		t.Fatalf("distinct free plan rejected: %v", err)
	}
}

// A distinct but occupied GM endpoint must be reported.
func TestNormalGMDoctorReportsDistinctOccupiedGM(t *testing.T) {
	admin, channel, game, gm := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
	opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), gm.Addr().String(), false, false)
	_ = admin.Close()
	_ = channel.Close()
	_ = game.Close()
	err := run(opts)
	if err == nil || !strings.Contains(err.Error(), gm.Addr().String()) {
		t.Fatalf("doctor did not report the occupied GM endpoint: %v", err)
	}
}

// A distinct but absent GM endpoint must fail the expect-listening preflight.
func TestNormalGMDoctorReportsMissingGMWhenExpectListening(t *testing.T) {
	admin, channel, game, gm := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
	opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), gm.Addr().String(), false, true)
	// Channel, admin and game stay listening so the expect-listening failure is
	// attributable to the GM endpoint alone; only the owned GM listener closes.
	_ = gm.Close()
	err := run(opts)
	if err == nil || !strings.Contains(err.Error(), gm.Addr().String()) {
		t.Fatalf("doctor did not report the missing GM endpoint: %v", err)
	}
}

// GM must not reuse the admin endpoint, and one generic listener must not stand in
// for GM in expect-listening mode.
func TestNormalGMDoctorRejectsGMEndpointCollision(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		admin, channel, game := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
		opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), admin.Addr().String(), false, false)
		_ = admin.Close()
		_ = channel.Close()
		_ = game.Close()
		if err := run(opts); err == nil {
			t.Fatal("doctor accepted a plan where GM reuses the admin endpoint")
		}
	})
	t.Run("expect_listening", func(t *testing.T) {
		admin, channel, game := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
		opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), admin.Addr().String(), false, true)
		if err := run(opts); err == nil {
			t.Fatal("doctor reported success with one generic listener standing in for GM")
		}
	})
}

// Leading-zero numeric aliases of the admin or channel endpoint must be rejected
// before any bind, using only owned ephemeral addresses.
func TestNormalGMDoctorRejectsNumericAliasOfRoleEndpoint(t *testing.T) {
	admin, channel, game := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
	_, adminPort, err := net.SplitHostPort(admin.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), "127.0.0.1:0"+adminPort, false, true)
	if err := run(opts); err == nil || !strings.Contains(err.Error(), "resolve to the same address") {
		t.Fatal("doctor accepted a leading-zero numeric alias of the admin endpoint")
	}
}

// Explicit skip-ports keeps its contract and does not inspect any listener.
func TestNormalGMDoctorSkipPortsSkipsCollisionChecks(t *testing.T) {
	admin, channel, game, gm := gmRegressListener(t), gmRegressListener(t), gmRegressListener(t), gmRegressListener(t)
	opts := gmRegressDoctorOptions(t, admin.Addr().String(), channel.Addr().String(), game.Addr().String(), gm.Addr().String(), true, false)
	if err := run(opts); err != nil {
		t.Fatalf("explicit skip-ports must not fail on owned collisions: %v", err)
	}
}

// Scope note: the role-endpoint check also compares the channel and admin
// endpoints when GM is disabled, so the pre-existing channel/admin duplicate plan
// is rejected as well. This test pins that behaviour explicitly; if the channel
// and admin duplicate plan must stay unvalidated, narrow both this test and the
// corresponding production comparison together.
func TestNormalGMDoctorDisabledGMStillRejectsChannelAdminCollision(t *testing.T) {
	admin, game := gmRegressListener(t), gmRegressListener(t)
	opts := gmRegressDoctorOptions(t, admin.Addr().String(), admin.Addr().String(), game.Addr().String(), "", false, false)
	_ = admin.Close()
	_ = game.Close()
	if err := run(opts); err == nil || !strings.Contains(err.Error(), "resolve to the same address") {
		t.Fatal("doctor accepted a plan where the channel reuses the admin endpoint with GM disabled")
	}
}
