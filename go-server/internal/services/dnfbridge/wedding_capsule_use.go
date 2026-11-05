package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/experienceitem"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// Preserve the complete native sequence for capsule uses; unrelated books and
// revive coins continue through the existing increase-status implementation.
func currentWeddingCapsuleUseOrExperienceItemHandler(s *Service, session *gameSession, request gameplayRequest) error {
	if currentExperienceItemUseDisabled() {
		return nil
	}
	if session == nil || len(request.Body) != 2 && len(request.Body) != 6 {
		return s.handleCurrentExperienceItemUse(session, request.Body)
	}
	slot := int16(binary.LittleEndian.Uint16(request.Body))
	if slot < 3 || slot > 288 {
		return s.handleCurrentExperienceItemUse(session, request.Body)
	}
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.handleCurrentExperienceItemUse(session, request.Body)
	}
	// A successful use may have removed the final source stack. Resolve its
	// receipted retransmission before looking at today's contents of that slot.
	if request.Sequence != nil {
		s.wedding.mu.Lock()
		previous := s.wedding.extensionRequests[actor.character]
		matched := previous != nil && previous.identity == actor && previous.opcode == 30 && previous.sequence == *request.Sequence
		var oldItem int64
		if matched {
			oldItem = previous.itemID
		}
		s.wedding.mu.Unlock()
		if matched {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			repos, _ := s.repositoryGroup()
			return s.useCurrentWeddingCapsuleExperience(ctx, repos, actor, slot, oldItem, request)
		}
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Inventory == nil {
		return s.handleCurrentExperienceItemUse(session, request.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	bag, found, e := repos.Inventory.Load(ctx, strconv.Itoa(int(actor.character)))
	if e != nil || !found {
		return s.handleCurrentExperienceItemUse(session, request.Body)
	}
	stack := bag.Slots[experienceItemMainSlotKey(slot)]
	rules, e := s.currentWeddingExtensionRules()
	if e != nil || rules == nil || stack.ItemID != rules.Capsule.ItemID {
		return s.handleCurrentExperienceItemUse(session, request.Body)
	}
	return s.useCurrentWeddingCapsuleExperience(ctx, repos, actor, slot, stack.ItemID, request)
}

func (s *Service) resolveCurrentWeddingCapsuleExperience(itemID int64) (experienceitem.ExperienceConsumable, error) {
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return experienceitem.ExperienceConsumable{}, e
	}
	definition, e := catalog.ResolveItem(uint32(itemID))
	if e != nil {
		return experienceitem.ExperienceConsumable{}, e
	}
	if definition.Kind != dungeonDropItemStackable || strings.Trim(definition.StackableType, "[] ") != "etc" {
		return experienceitem.ExperienceConsumable{}, wedding.ErrItem
	}
	doc, e := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if e != nil {
		return experienceitem.ExperienceConsumable{}, e
	}
	tokens, found := doc.Section("increase status type")
	minimum, m1 := doc.Int("minimum level")
	maximum, m2 := doc.Int("maximum level")
	if !found || len(tokens) != 2 || tokens[0].Kind != pvf.TokenString || strings.Trim(tokens[0].Value, "[] ") != "expUp" || tokens[1].Kind != pvf.TokenInt || tokens[1].Int <= 0 || tokens[1].Int > 0xffffffff || !m1 || !m2 || minimum < 1 || maximum < minimum || maximum > 255 || definition.StackLimit <= 0 {
		return experienceitem.ExperienceConsumable{}, wedding.ErrItem
	}
	return experienceitem.ExperienceConsumable{ItemID: itemID, Amount: uint32(tokens[1].Int), MinimumLevel: int(minimum), MaximumUseLevel: int(maximum), StackLimit: definition.StackLimit, PVFPath: definition.PVFPath, Slots: currentPVFItemPlacementPolicy(definition)}, nil
}

func (s *Service) useCurrentWeddingCapsuleExperience(ctx context.Context, repos r.Group, actor boundGameSessionCharacter, slot int16, itemID int64, request gameplayRequest) error {
	reject := func(reason string, e error) error {
		return s.answerRejectedExperienceItem(actor.session, slot, itemID, reason, e)
	}
	if request.Sequence == nil {
		return reject("capsule_sequence_missing", wedding.ErrReplay)
	}
	fingerprint := fmt.Sprintf("30:%d:%d", slot, itemID)
	s.wedding.mu.Lock()
	if s.wedding.extensionRequests == nil {
		s.wedding.extensionRequests = map[uint16]*currentWeddingExtensionRequestState{}
	}
	if previous := s.wedding.extensionRequests[actor.character]; previous != nil && previous.identity == actor {
		if *request.Sequence < previous.sequence || *request.Sequence == previous.sequence && (previous.opcode != 30 || previous.fingerprint != fingerprint) {
			s.wedding.mu.Unlock()
			return reject("capsule_replay_mismatch", wedding.ErrReplay)
		}
		if *request.Sequence == previous.sequence {
			code := previous.semantic
			s.wedding.mu.Unlock()
			if code != 0 {
				return reject("capsule_failed_replay", wedding.ErrReplay)
			}
			return s.publishCurrentWeddingCapsuleExperience(actor, slot)
		}
	}
	receipt := &currentWeddingExtensionRequestState{identity: actor, sequence: *request.Sequence, opcode: 30, fingerprint: fingerprint, semantic: -1, itemID: itemID, slot: slot}
	s.wedding.extensionRequests[actor.character] = receipt
	s.wedding.mu.Unlock()
	definition, e := s.resolveCurrentWeddingCapsuleExperience(itemID)
	if e != nil {
		return reject("capsule_pvf", e)
	}
	tables, _, e := s.currentQuestFinishResources(ctx)
	if e != nil {
		return reject("capsule_progression_tables", e)
	}
	nonce, e := actor.session.guild.purchaseSequence.identity(0)
	if e != nil {
		return reject("capsule_nonce", e)
	}
	owner, e := experienceitem.NewOwner(repos)
	if e != nil {
		return reject("capsule_owner", e)
	}
	release, e := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{actor})
	if e != nil {
		return reject("capsule_actor_busy", e)
	}
	_, e = owner.UseExperience(ctx, experienceitem.ExperienceCommand{
		AccountID: s.accountIDForSession(actor.session), CharacterID: strconv.Itoa(int(actor.character)), Slot: slot, Definition: definition, Tables: tables, PlayableCap: currentAdventureCharacterLevelCap,
		Now: s.gameplayNow(), Session: nonce, Sequence: *request.Sequence,
		Revalidate: func() bool { return !actor.session.loginRevoked.Load() && s.boundGameSessionCharacterCurrent(actor) },
		LevelStats: s.characterPVFStatValuesForLevel,
	})
	release() // Never wait for wedding.mu while retaining the asset reservation.
	if e != nil {
		return reject("capsule_experience_transaction", e)
	}
	s.wedding.mu.Lock()
	receipt.semantic = 0
	s.wedding.mu.Unlock()
	return s.publishCurrentWeddingCapsuleExperience(actor, slot)
}

func (s *Service) publishCurrentWeddingCapsuleExperience(actor boundGameSessionCharacter, slot int16) error {
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	repos, available := s.repositoryGroup()
	if !available || repos.Skill == nil {
		return wedding.ErrUnavailable
	}
	id := strconv.Itoa(int(actor.character))
	skill, found, e := repos.Skill.Load(ctx, id)
	if e != nil {
		return e
	}
	if !found || skill.CharacterID != id {
		return wedding.ErrState
	}
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	if e = s.sendGameUpperSuccess(actor.session, 30, successExperienceItemBody(actor.character)); e != nil {
		return e
	}
	if e = s.sendSelectedIncrementalItemSlotRefreshes(actor.session, "wedding_capsule_experience", []alignedcmd.ItemSlotRefresh{{ListType: r.MainInventoryListType, SlotIndex: slot}}); e != nil {
		return e
	}
	return s.sendCurrentExperienceItemCharacterState(actor.session, id, experienceitem.Result{Points: skill.Points})
}
