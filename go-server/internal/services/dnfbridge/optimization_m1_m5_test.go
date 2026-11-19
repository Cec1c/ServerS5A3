package dnfbridge

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/platform/config"
)

func TestVerboseDiagnosticsZeroValueAndProductionDefaults(t *testing.T) {
	literal := &Service{}
	if !literal.verboseDiagnosticsEnabled() {
		t.Fatal("literal zero-value Service must keep verboseDiagnosticsEnabled()=true for existing unit tests")
	}

	t.Setenv(verboseDiagnosticsEnv, "")
	opts := optionsFromConfig(config.ServiceConfig{})
	if !opts.verboseDiagnosticsDisabled || opts.verboseDiagnosticsEnvBad {
		t.Fatalf("empty env optionsFromConfig: disabled=%v bad=%v, want true/false",
			opts.verboseDiagnosticsDisabled, opts.verboseDiagnosticsEnvBad)
	}
	prod := &Service{options: opts}
	if prod.verboseDiagnosticsEnabled() {
		t.Fatal("production default optionsFromConfig must disable verbose per-packet diagnostics")
	}

	for _, truthy := range []string{"1", "true", "on", "verbose", "FULL"} {
		t.Setenv(verboseDiagnosticsEnv, truthy)
		enabledOpts := optionsFromConfig(config.ServiceConfig{})
		if enabledOpts.verboseDiagnosticsDisabled || enabledOpts.verboseDiagnosticsEnvBad {
			t.Fatalf("env=%q: disabled=%v bad=%v, want false/false",
				truthy, enabledOpts.verboseDiagnosticsDisabled, enabledOpts.verboseDiagnosticsEnvBad)
		}
	}

	t.Setenv(verboseDiagnosticsEnv, "unexpected_token")
	badOpts := optionsFromConfig(config.ServiceConfig{})
	if !badOpts.verboseDiagnosticsDisabled || !badOpts.verboseDiagnosticsEnvBad || badOpts.verboseDiagnosticsEnvRaw != "unexpected_token" {
		t.Fatalf("invalid env: disabled=%v bad=%v raw=%q, want true/true/unexpected_token",
			badOpts.verboseDiagnosticsDisabled, badOpts.verboseDiagnosticsEnvBad, badOpts.verboseDiagnosticsEnvRaw)
	}
}

func TestVerboseDiagnosticsQuietModeSuppressesNoiseAndKeepsAnomalies(t *testing.T) {
	t.Setenv(verboseDiagnosticsEnv, "")
	var logs bytes.Buffer
	quiet := &Service{
		options: optionsFromConfig(config.ServiceConfig{}),
		logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	}

	// Fast town actor construction (<20ms) must be suppressed before building args.
	quiet.logTownLoadingDiagnostic("game-town-copresence-actor-state-send",
		"conn_id", "c1", "construction_us", 1500, "send_us", 200)
	if logs.Len() != 0 {
		t.Fatalf("fast town loading diagnostic should be suppressed in quiet mode, got: %s", logs.String())
	}

	// Slow town actor construction (>=20ms) must still be emitted.
	quiet.logTownLoadingDiagnostic("game-town-copresence-actor-state-delivered",
		"conn_id", "c1", "construction_us", 24500, "send_us", 300)
	if !strings.Contains(logs.String(), "dnfbridge town loading diagnostic") ||
		!strings.Contains(logs.String(), "24500") {
		t.Fatalf("slow town loading diagnostic missing in quiet mode: %s", logs.String())
	}

	logs.Reset()
	// Routine raid/party peer frame diagnostics must be suppressed in quiet mode.
	quiet.logRaidPeerDiagnostic("game-dungeon-party-peer-actor-send", "conn_id", "c1", "party_id", 7)
	quiet.logRaidPeerDiagnostic("game-party-entry-confirmed", "conn_id", "c1", "party_id", 7)
	quiet.logCrossSceneProfileDiagnostic("game-peer-userinfo-profile-refresh-sent", "conn_id", "c1", "source_char_id", 11)
	if logs.Len() != 0 {
		t.Fatalf("routine raid/profile diagnostics should be suppressed in quiet mode, got: %s", logs.String())
	}

	// Rejections and failures must still be emitted in quiet mode.
	quiet.logRaidPeerDiagnostic("game-party-entry-rejected", "conn_id", "c1", "party_id", 7, "reason", "busy")
	quiet.logRaidPeerDiagnostic("game-party-dungeon-loading-released", "conn_id", "c1", "party_id", 7, "error", errors.New("PRIVATE_ERR"))
	quiet.logCrossSceneProfileDiagnostic("game-peer-userinfo-profile-refresh-failed", "conn_id", "c1", "error_kind", "repo_timeout")
	quiet.logRaidPeerDiagnostic(fiendWarPartyAdmissionDiagnosticKind, "reason", "existing_party_ack_write_failed")
	out := logs.String()
	for _, want := range []string{
		"game-party-entry-rejected",
		"game-party-dungeon-loading-released",
		"existing_party_ack_write_failed",
		`"call_ok":false`,
		"game-peer-userinfo-profile-refresh-failed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing retained abnormal event %q in %s", want, out)
		}
	}
	if strings.Contains(out, "PRIVATE_ERR") {
		t.Fatalf("private error text leaked into diagnostic log: %s", out)
	}

	logs.Reset()
	session := &gameSession{connID: "c1", selectedCharacterID: 42}
	req := dungeoncmd.DieMonsterRequest{
		RuntimeObjectKey: 9001,
		OwnerObjectKey:   777,
		CombatEntryCount: 1,
		CombatEntries:    make([]byte, dungeoncmd.Official249DieMonsterEntrySize),
	}
	logCurrentSentinelDeathShape(quiet, session, nil, req)
	if logs.Len() != 0 {
		t.Fatalf("sentinel death shape should be suppressed in quiet mode, got: %s", logs.String())
	}

	literal := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	logCurrentSentinelDeathShape(literal, session, nil, req)
	if !strings.Contains(logs.String(), "dnfbridge sentinel death request") {
		t.Fatalf("literal Service should preserve sentinel death diagnostic, got: %s", logs.String())
	}
}

func TestUnhandledPacketFirstSeenBoundedSampling(t *testing.T) {
	t.Setenv(verboseDiagnosticsEnv, "")
	var logs bytes.Buffer
	quiet := &Service{
		options: optionsFromConfig(config.ServiceConfig{}),
		logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	}

	nameCalls := 0
	knownCalls := 0
	for i := 0; i < 10; i++ {
		quiet.logUnhandledGameCommand(1, 999, 12,
			func() string {
				nameCalls++
				return "TEST_CMD_999"
			},
			func() bool {
				knownCalls++
				return false
			},
		)
	}
	if nameCalls != 1 || knownCalls != 1 {
		t.Fatalf("lazy name/known closures invoked %d/%d times across 10 repeats, want 1/1", nameCalls, knownCalls)
	}
	if got := strings.Count(logs.String(), "dnfbridge unhandled latest game command"); got != 1 {
		t.Fatalf("logged %d lines for repeated unhandled game command, want 1: %s", got, logs.String())
	}
	if !strings.Contains(logs.String(), `"sampled_first_seen":true`) {
		t.Fatalf("first-seen marker missing: %s", logs.String())
	}

	// Feed 300 distinct upper opcodes; total sampled entries must stop at maxSampledUnhandledOpcodes (256).
	for opcode := uint16(1000); opcode < 1300; opcode++ {
		quiet.logUnhandledUpperPacket(0, opcode, 4,
			func() string { return "UPPER" },
			func() bool { return false },
		)
	}
	if got := quiet.unhandledOpcodeCount.Load(); got != maxSampledUnhandledOpcodes {
		t.Fatalf("sampled opcode count = %d, want cap %d", got, maxSampledUnhandledOpcodes)
	}
	totalLines := strings.Count(logs.String(), "\n")
	if totalLines != maxSampledUnhandledOpcodes {
		t.Fatalf("total sampled unhandled log lines = %d, want %d", totalLines, maxSampledUnhandledOpcodes)
	}
}

func TestPartyUDPSocketBufferDualPathAndReadback(t *testing.T) {
	t.Setenv(partyUDPSocketBufferBytesEnv, "")
	defOpts := optionsFromConfig(config.ServiceConfig{})
	if defOpts.partyUDPSocketBufferBytes != defaultPartyUDPSocketBufferBytes || defOpts.partyUDPSocketBufferEnvBad {
		t.Fatalf("default UDP socket buffer = %d (bad=%v), want %d/false",
			defOpts.partyUDPSocketBufferBytes, defOpts.partyUDPSocketBufferEnvBad, defaultPartyUDPSocketBufferBytes)
	}

	t.Setenv(partyUDPSocketBufferBytesEnv, "0")
	zeroOpts := optionsFromConfig(config.ServiceConfig{})
	if zeroOpts.partyUDPSocketBufferBytes != 0 || zeroOpts.partyUDPSocketBufferEnvBad {
		t.Fatalf("zero opt-out UDP socket buffer = %d (bad=%v), want 0/false",
			zeroOpts.partyUDPSocketBufferBytes, zeroOpts.partyUDPSocketBufferEnvBad)
	}

	t.Setenv(partyUDPSocketBufferBytesEnv, "1024") // below 64 KiB floor -> invalid fallback
	badOpts := optionsFromConfig(config.ServiceConfig{})
	if badOpts.partyUDPSocketBufferBytes != defaultPartyUDPSocketBufferBytes || !badOpts.partyUDPSocketBufferEnvBad {
		t.Fatalf("sub-floor UDP socket buffer = %d (bad=%v), want %d/true",
			badOpts.partyUDPSocketBufferBytes, badOpts.partyUDPSocketBufferEnvBad, defaultPartyUDPSocketBufferBytes)
	}

	// Reserve a dynamic non-conflicting port block on 127.0.0.1 for two relay legs.
	probeTemp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen temp UDP: %v", err)
	}
	basePort := probeTemp.LocalAddr().(*net.UDPAddr).Port
	_ = probeTemp.Close()
	if basePort+32 > 65535 {
		basePort = 42000
	}

	const requestedBytes = 256 * 1024
	var warnings []string
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:           true,
		advertiseIP:       "127.0.0.1",
		bindHost:          "127.0.0.1",
		portStart:         basePort,
		portCount:         24,
		probeListen:       true,
		probePorts:        []int{0},
		socketBufferBytes: requestedBytes,
		logWarn: func(msg string, args ...any) {
			if strings.Contains(msg, "socket buffer") {
				warnings = append(warnings, msg)
			}
		},
	})
	if err != nil {
		t.Fatalf("newCurrentPartyUDPRelayWithSettings: %v", err)
	}
	defer func() { _ = relay.Close() }()

	if relay.probeListener == nil || len(relay.probeListener.conns) != 1 {
		t.Fatalf("expected 1 bound probe listener socket, got %+v", relay.probeListener)
	}
	for port, conn := range relay.probeListener.conns {
		rcv, snd, err := readUDPSocketBufferSizes(conn)
		if err != nil || rcv <= 0 || snd <= 0 {
			t.Fatalf("probe listener port %d getsockopt readback: rcv=%d snd=%d err=%v", port, rcv, snd, err)
		}
	}

	ok := relay.Sync(1, []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 102, generation: 1, address: [4]byte{127, 0, 0, 1}},
	})
	if !ok {
		t.Fatal("relay.Sync(1, 2 members) failed")
	}
	relay.mu.Lock()
	legCount := len(relay.legs)
	legs := make([]*currentPartyUDPRelayLeg, 0, legCount)
	for _, leg := range relay.legs {
		legs = append(legs, leg)
	}
	relay.mu.Unlock()
	if legCount != 2 {
		t.Fatalf("allocated %d legs, want 2", legCount)
	}
	for _, leg := range legs {
		rcv, snd, err := readUDPSocketBufferSizes(leg.conn)
		if err != nil || rcv <= 0 || snd <= 0 {
			t.Fatalf("relay leg port %d getsockopt readback: rcv=%d snd=%d err=%v", leg.port, rcv, snd, err)
		}
	}
	if relay.lastEffectiveRcvbuf.Load() <= 0 || relay.lastEffectiveSndbuf.Load() <= 0 {
		t.Fatalf("relay atomic effective buffer readback not recorded: rcv=%d snd=%d",
			relay.lastEffectiveRcvbuf.Load(), relay.lastEffectiveSndbuf.Load())
	}
	if len(warnings) > 1 {
		t.Fatalf("socket buffer warnings must be bounded to at most 1 per issue, got %v", warnings)
	}
}
