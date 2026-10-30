package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestR195BuffProjectionFailureReasonsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{context.DeadlineExceeded, "owned_projection_deadline"},
		{context.Canceled, "owned_projection_cancelled"},
		{dnfrepo.ErrBuffLegacyOwnerMismatch, "owned_projection_owner_mismatch"},
		{dnfrepo.ErrBuffOwnedSnapshotUnavailable, "owned_projection_transaction_unavailable"},
		{dnfrepo.ErrBuffLegacyAmbiguous, "owned_projection_legacy_ambiguous"},
		{buffswap.ErrInvalid, "owned_projection_registration_invalid"},
		{&json.SyntaxError{}, "owned_projection_registration_malformed"},
		{errors.New("private repository detail"), "owned_projection_load_failed"},
	} {
		if got := buffSwapProjectionFailureReason(fmt.Errorf("private prefix: %w", tc.err)); got != tc.reason {
			t.Fatalf("reason=%s want=%s", got, tc.reason)
		}
	}
}

func r195BuffDiagnosticLine(t *testing.T, logs string, fragments ...string) string {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, `msg="dnfbridge buff swap diagnostic"`) {
			continue
		}
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(line, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return line
		}
	}
	t.Fatalf("missing Buff diagnostic %q in logs=%s", fragments, logs)
	return ""
}

// The diagnostic must be available when packetLog is disabled and must expose
// only the bounded send identity, actual msgID, canonical body digest and
// slot/list/index projection. Persistent instance tokens and account values
// must never be copied into this line.
func TestR195BuffSwapDiagnosticRegistrationWearSkipAndFailure(t *testing.T) {
	service, session, conn, repos := r191CacheFixture(t)
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewTextHandler(&logs, nil))
	session.connID = "buff-diagnostic"
	session.characterGeneration = 7
	service.gameSessions = map[uint16]*gameSession{session.selectedCharacterID: session}

	if err := service.handleGameCommand(session, 1, 1426, reviewCurrentBuffCommand(38, 12, 0, 54)); err != nil {
		t.Fatal(err)
	}
	drainUpperPackets(t, conn)
	registrationLine := r195BuffDiagnosticLine(t, logs.String(),
		"outcome=sent", "reason=wire_write_succeeded", "source=registration_changed",
		"char_id=1", "generation=7", "msg_id=1368", "skill=38",
		"canonical_body_len=168", "wire_body_len=168",
		"slots=12:0/54", "body_digest=", "omitted=0")
	if strings.Contains(registrationLine, "instance") || strings.Contains(registrationLine, "dnf:10001") {
		t.Fatalf("diagnostic leaked owner values: %s", registrationLine)
	}
	state, found, err := buffswap.Load(context.Background(), repos, "1", "dnf:10001")
	if err != nil || !found || state.Slots[12].Instance == "" {
		t.Fatalf("registration unavailable: state=%+v found=%t err=%v", state, found, err)
	}
	if strings.Contains(logs.String(), state.Slots[12].Instance) {
		t.Fatal("diagnostic leaked persistent Buff instance")
	}

	// Login/other automatic callers keep their old silence in the application
	// diagnostic stream; only explicit registration and equipment commits log.
	logs.Reset()
	if err := service.sendBuffSwapReapply(session, "login_like_refresh"); err != nil {
		t.Fatal(err)
	}
	drainUpperPackets(t, conn)
	if strings.Contains(logs.String(), "dnfbridge buff swap diagnostic") {
		t.Fatalf("automatic caller emitted Buff diagnostic: %s", logs.String())
	}

	logs.Reset()
	if err := service.sendBuffSwapReapplyAfterCommittedWear(session, "unrelated_actor_rebuild"); err != nil {
		t.Fatal(err)
	}
	drainUpperPackets(t, conn)
	r195BuffDiagnosticLine(t, logs.String(),
		"outcome=sent", "source=unrelated_actor_rebuild", "msg_id=1367",
		"skill=38", "canonical_body_len=168", "wire_body_len=166", "slots=12:0/54")

	logs.Reset()
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "unchanged_nonappearance"); err != nil {
		t.Fatal(err)
	}
	if packets := drainUpperPackets(t, conn); len(packets) != 0 {
		t.Fatalf("unchanged diagnostic path emitted packets: %+v", packets)
	}
	r195BuffDiagnosticLine(t, logs.String(),
		"outcome=skipped", "reason=unchanged_projection", "msg_id=1367",
		"source=unchanged_nonappearance", "canonical_body_len=168", "wire_body_len=166",
		"slots=12:0/54")

	ctx := context.Background()
	inventory, found, err := repos.Inventory.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("inventory unavailable: found=%t err=%v", found, err)
	}
	inventory.Slots["0:55"] = inventory.Slots["0:54"]
	delete(inventory.Slots, "0:54")
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}

	logs.Reset()
	session.conn = r191FailingConn{conn}
	if err := service.sendBuffSwapReapplyAfterNonAppearanceWear(session, "changed_projection_failure"); err == nil {
		t.Fatal("injected Buff write failure was accepted")
	}
	session.conn = conn
	r195BuffDiagnosticLine(t, logs.String(),
		"outcome=failed", "reason=socket_write_failed", "msg_id=1367",
		"source=changed_projection_failure", "canonical_body_len=168", "wire_body_len=166",
		"slots=12:0/55")
}
