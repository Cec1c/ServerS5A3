package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/buffswap"
)

const buffSwapReapplyEnv = "DNFBRIDGE_BUFF_SWAP_REAPPLY"
const nativeBuffSwapSnapshotID uint16 = 1369
const currentBuffSwapSnapshotID uint16 = 1368
const currentBuffSwapRowsID uint16 = 1367
const currentBuffSwapRegistrationID uint16 = 1426

// Default off preserves all legacy registration and packet behavior.
func buffSwapReapplyEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(buffSwapReapplyEnv))) {
	case "on", "1", "true":
		return true
	default:
		return false
	}
}

// currentClientLoadoutOwnsBuffSwap1428 reports whether op1428 is the current
// client's Buff-enhancement loadout stream. DNFBRIDGE_BUFF_SWAP_REAPPLY owns the
// native 1426/1368 producer; it must not consume the separate 1428 loadout
// record and persist it as a positional native binding. Other profiles retain
// the historical 1428/1369 native path.
//
// This predicate is deliberately limited to the inbound ownership decision.
// Under native reapply the late-boundary projection and sentinel answer remain
// suppressed until the current client's 1428 downlink semantics are proved.
func (s *Service) currentClientLoadoutOwnsBuffSwap1428() bool {
	return s != nil && strings.TrimSpace(s.options.currentClientVersion) == "official-24.9"
}

// With the existing switch enabled,1426 belongs to the proven current BUFF
// producer. Invalid lengths and fields are consumed/rejected;
// they cannot complete an FPS loading gate or create registration state.
func (s *Service) handleCurrentBuffSwap1426Command(session *gameSession, typ uint16, body []byte) (bool, error) {
	if s == nil || session == nil || typ != currentBuffSwapRegistrationID || !buffSwapReapplyEnabled() {
		return false, nil
	}
	return s.handleBuffSwapRegistration(session, body, true)
}

func (s *Service) handleNativeBuffSwapRegistration(session *gameSession, body []byte) (bool, error) {
	return s.handleBuffSwapRegistration(session, body, false)
}

func (s *Service) handleBuffSwapRegistration(session *gameSession, body []byte, current bool) (bool, error) {
	c, err := buffswap.Parse(body)
	if current {
		c, err = buffswap.ParseCurrent(body)
		if session.selectedCharacterID == 0 || strings.TrimSpace(session.accountID) == "" {
			err = buffswap.ErrInvalid
		}
	}
	if err == nil {
		repos, ok := s.repositoryGroup()
		if !ok {
			err = errSettingsRepositoryUnavailable
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			if current {
				err = buffswap.ApplyCurrentWithWornResolver(ctx, repos, strconv.Itoa(int(session.selectedCharacterID)), s.accountIDForSession(session), c, currentEXEActorEquipmentSlot)
			} else {
				err = buffswap.Apply(ctx, repos, strconv.Itoa(int(session.selectedCharacterID)), s.accountIDForSession(session), c)
			}
		}
	}
	event := "game-buff-swap-native-recorded"
	if err != nil {
		event = "game-buff-swap-native-rejected"
	}
	wireProfile := "historical1428-1369"
	if current {
		wireProfile = buffswap.CurrentWireProfile
	}
	s.logGameEvent(session, event, "char_id", session.selectedCharacterID, "skill", c.Skill, "equipment_slot", c.Slot, "source_list", c.List, "source_slot", c.Index, "wire_profile", wireProfile, "error", err)
	if err == nil {
		return true, s.sendBuffSwapReapply(session, "registration_changed")
	}
	return true, nil
}

// Historical8a7 noti1369 and currentbb428/82e noti1368 both consume
// skill:u16,count:u8,5-byte rows. Fixed v151 registers VA998890 for1368 at
// VA43A2732; it reads the skill then calls9985F0. Current1367 consumes
// rows only and must not receive this skill-prefixed snapshot.
func nativeBuffSwapSnapshotBody(state buffswap.State) []byte {
	body := make([]byte, 3+5*buffswap.SlotCount)
	binary.LittleEndian.PutUint16(body, state.Skill)
	body[2] = buffswap.SlotCount
	for slot := byte(0); slot < buffswap.SlotCount; slot++ {
		i := 3 + 5*int(slot)
		body[i+1], body[i+2] = slot, 44
		if state.WireProfile == buffswap.CurrentWireProfile {
			body[i+2] = 45
		}
		binary.LittleEndian.PutUint16(body[i+3:], 0xffff)
		if b, ok := state.Slots[slot]; ok {
			body[i], body[i+2] = 1, b.List
			binary.LittleEndian.PutUint16(body[i+3:], b.Index)
		}
	}
	return body
}

func (s *Service) sendBuffSwapReapply(session *gameSession, source string) error {
	return s.sendBuffSwapReapplyOwned(session, source, false, false)
}

// Equipment movement does not create a native registration. When this owner
// has never been stored, leave the separate current-client loadout alone.
// Explicitly cleared or unresolved stored registrations still publish their
// empty snapshot, as do login and character-switch boundaries.
func (s *Service) sendBuffSwapReapplyAfterCommittedWear(session *gameSession, source string) error {
	return s.sendBuffSwapReapplyOwned(session, source, s.currentClientLoadoutOwnsBuffSwap1428(), false)
}

// Non-appearance equipment commits do not rebuild the actor. Replaying an
// identical registered projection still makes 1368/1369 reset the native Buff
// manager, so an unrelated wear would visibly disturb the registration. Only
// suppress this path when the resolved wire state is byte-identical; a moved
// registered instance changes its list/index and is still sent. Actor
// appearance rebuilds keep the unconditional committed-wear replay.
func (s *Service) sendBuffSwapReapplyAfterNonAppearanceWear(session *gameSession, source string) error {
	current := s.currentClientLoadoutOwnsBuffSwap1428()
	return s.sendBuffSwapReapplyOwned(session, source, current, current)
}

// Every 1368/1369 producer, including explicit registration and clear, passes
// through this function. The replay cache is therefore updated by direct sends
// as well; only a successful socket write is recorded.
func (s *Service) sendBuffSwapReapplyOwned(session *gameSession, source string, skipUnregistered, skipUnchanged bool) error {
	if !buffSwapReapplyEnabled() || s == nil || session == nil {
		return nil
	}
	identity, current := s.captureBuffOwnedSendIdentity(session)
	if !current {
		return nil
	}
	if identity.sharedFallback {
		s.warnSharedAccountFallback(session)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(identity.character))
	projection, err := buffswap.LoadResolvedOwnedMigratingCurrentInstances(ctx, repos, characterID, identity.accountID)
	if err == nil {
		if skipUnregistered && !projection.Found {
			s.logGameEvent(session, "game-buff-swap-native-reapply-skipped", "source", source, "reason", "current_native_registration_absent")
			return nil
		}
		state, omitted := currentBuffOwnedWireState(projection), projection.Omitted
		if !s.buffOwnedSendIdentityCurrent(identity) {
			s.logGameEvent(session, "game-buff-swap-native-reapply-skipped", "source", source, "reason", "stale_owner_generation")
			return nil
		}
		{
			body := nativeBuffSwapSnapshotBody(state)
			snapshotID := nativeBuffSwapSnapshotID
			if state.WireProfile == buffswap.CurrentWireProfile {
				snapshotID = currentBuffSwapSnapshotID
			}
			if skipUnchanged && s.buffReplayUnchanged(session, identity, snapshotID, body) {
				s.logGameEvent(session, "game-buff-swap-native-reapply-skipped", "source", source, "reason", "unchanged_projection_after_unrelated_wear", "msg_id", snapshotID)
				return nil
			}
			wireID, wireBody := snapshotID, body
			// v153 registers 1367 directly to 9985F0. It resolves current item
			// objects and replaces registration rows without 1368's 997210
			// manager reset. Rebuilds must refresh these pointers even if their
			// list/index bytes did not change. Explicit registration and login
			// keep the full snapshot, as does a skill not yet sent to this owner.
			if skipUnregistered && snapshotID == currentBuffSwapSnapshotID && s.buffReplaySkillCurrent(session, identity, state.Skill) {
				wireID, wireBody = currentBuffSwapRowsID, body[2:]
			}
			if err := s.sendGameUpperRawClass(session, wireID, wireBody, 0); err != nil {
				return err
			}
			// Cache the canonical logical state after either successful wire
			// form. A failed row update must remain eligible for retry.
			s.rememberBuffReplay(session, identity, snapshotID, body)
			s.logGameEvent(session, "game-buff-swap-native-reapply-sent", "source", source, "skill", state.Skill, "rows", len(state.Slots), "omitted", omitted, "msg_id", wireID, "classification", 0, "body_len", len(wireBody))
		}
	}
	if err != nil {
		s.logGameEvent(session, "game-buff-swap-native-reapply-skipped", "source", source, "error", err)
	}
	return nil
}
