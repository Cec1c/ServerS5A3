package dnfbridge

import (
	"context"
	"encoding/binary"
	"log/slog"
	"reflect"
	"testing"
)

// Candidate-only: baseline04 has no owned send identity API.
func TestReviewBuffOwnedAccountSnapshotUsesSessionOwner(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	_, s, session, _ := reviewOwnedSendSeed(t)
	session.accountID = "  dnf:10001  "
	session.characterGeneration = 11
	s.gameSessions = map[uint16]*gameSession{1: session}
	identity, ok := s.captureBuffOwnedSendIdentity(session)
	if !ok || identity.accountID != "dnf:10001" || identity.rawAccountID != "  dnf:10001  " || !s.buffOwnedSendIdentityCurrent(identity) {
		t.Fatalf("fixed session owner lost: ok=%v identity=%+v", ok, identity)
	}
}

func TestReviewBuffOwnedAccountSnapshotRechecksRawIdentity(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	for _, name := range []string{"stable", "raw_account_whitespace", "login_flag", "generation", "character", "replaced_peer"} {
		t.Run(name, func(t *testing.T) {
			_, s, session, _ := reviewOwnedSendSeed(t)
			session.characterGeneration = 11
			s.gameSessions = map[uint16]*gameSession{1: session}
			identity, ok := s.captureBuffOwnedSendIdentity(session)
			if !ok {
				t.Fatal("fixture current identity not captured")
			}
			s.mu.Lock()
			switch name {
			case "raw_account_whitespace":
				session.accountID = " " + session.accountID
			case "login_flag":
				session.loginPacketSeen = !session.loginPacketSeen
			case "generation":
				session.characterGeneration++
			case "character":
				session.selectedCharacterID = 2
			case "replaced_peer":
				s.gameSessions[1] = &gameSession{selectedCharacterID: 1, characterGeneration: 12}
			}
			s.mu.Unlock()
			if got := s.buffOwnedSendIdentityCurrent(identity); got != (name == "stable") {
				t.Fatalf("raw identity recheck=%v for %s", got, name)
			}
		})
	}
}

type reviewBuffFallbackLockHandler struct {
	service  *Service
	warnings int
	lockHeld bool
}

func (*reviewBuffFallbackLockHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *reviewBuffFallbackLockHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "dnfbridge game account fell back to shared account" {
		return nil
	}
	h.warnings++
	if h.service.mu.TryLock() {
		h.service.mu.Unlock()
	} else {
		h.lockHeld = true
	}
	return nil
}
func (h *reviewBuffFallbackLockHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *reviewBuffFallbackLockHandler) WithGroup(string) slog.Handler      { return h }
func TestReviewBuffOwnedAccountSnapshotFallbackWarnOutsideLockOnce(t *testing.T) {
	for _, name := range []string{"matching_owner", "foreign_owner"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			r, s, session, conn := reviewOwnedSendSeed(t)
			if name == "foreign_owner" {
				s.options.accountID = "dnf:fallback-other"
			}
			owner, found, err := r.Character.Load(context.Background(), "1")
			if err != nil || !found || owner.CharacterID != "1" {
				t.Fatalf("owner fixture: found=%v error=%v owner=%+v", found, err, owner)
			}
			if (owner.AccountID == s.options.accountID) != (name == "matching_owner") {
				t.Fatalf("owner/fallback fixture mismatch: owner=%q configured=%q mode=%s", owner.AccountID, s.options.accountID, name)
			}
			before, found, err := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			if err != nil || !found {
				t.Fatalf("before settings missing: found=%v error=%v", found, err)
			}
			session.accountID = ""
			session.loginPacketSeen = true
			handler := &reviewBuffFallbackLockHandler{service: s}
			s.logger = slog.New(handler)
			identity, ok := s.captureBuffOwnedSendIdentity(session)
			if !ok || !identity.sharedFallback || identity.accountID != s.options.accountID || handler.warnings != 0 || !s.buffOwnedSendIdentityCurrent(identity) {
				t.Fatalf("detached capture WARN/owner: ok=%v identity=%+v warns=%d", ok, identity, handler.warnings)
			}
			for i := 0; i < 2; i++ {
				if err := s.sendBuffSwapReapply(session, "fallback_lock_control"); err != nil {
					t.Fatal(err)
				}
			}
			if handler.warnings != 1 || handler.lockHeld {
				t.Fatalf("WARN once/lock: warnings=%d held=%v", handler.warnings, handler.lockHeld)
			}
			after, found, err := r.Settings.Load(context.Background(), "character:1:buff_swap_native")
			if err != nil || !found || !reflect.DeepEqual(before, after) {
				t.Fatalf("fallback changed settings: found=%v error=%v", found, err)
			}
			if name == "foreign_owner" {
				if conn.write.Len() != 0 {
					t.Fatalf("foreign owner emitted %d bytes", conn.write.Len())
				}
				return
			}
			packets := drainUpperPackets(t, conn)
			if len(packets) != 2 {
				t.Fatalf("matching owner snapshot count=%d", len(packets))
			}
			for n, packet := range packets {
				body := packet.body
				if packet.msgID != 1368 || len(body) != 168 || binary.LittleEndian.Uint16(body[:2]) != 38 || body[2] != 33 {
					t.Fatalf("snapshot %d header/body wrong: msg=%d bytes=%x", n, packet.msgID, body)
				}
				for slot := 0; slot < 33; slot++ {
					row := body[3+slot*5 : 3+(slot+1)*5]
					flag, list, index := byte(0), byte(45), uint16(65535)
					if slot == 12 {
						flag, list, index = 1, 0, 54
					}
					if row[0] != flag || row[1] != byte(slot) || row[2] != list || binary.LittleEndian.Uint16(row[3:]) != index {
						t.Fatalf("snapshot%d row%d=%x expected flag%d/list%d/index%d", n, slot, row, flag, list, index)
					}
				}
			}
		})
	}
}
