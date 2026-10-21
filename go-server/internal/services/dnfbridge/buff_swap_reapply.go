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
// skill:u16,count:u8,5-byte rows. Current1368 VA998890 resets the manager,
// then delegates rows to9985F0 ->9983F0(send=false). Current1367 consumes
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
	projection, err := buffswap.LoadResolvedOwned(ctx, repos, characterID, identity.accountID)
	if err == nil {
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
			if err := s.sendGameUpperRawClass(session, snapshotID, body, 0); err != nil {
				return err
			}
			s.logGameEvent(session, "game-buff-swap-native-reapply-sent", "source", source, "skill", state.Skill, "rows", len(state.Slots), "omitted", omitted, "msg_id", snapshotID, "classification", 0, "body_len", len(body))
		}
	}
	if err != nil {
		s.logGameEvent(session, "game-buff-swap-native-reapply-skipped", "source", source, "error", err)
	}
	return nil
}
