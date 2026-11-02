package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/experienceitem"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) tryCurrentExperienceCapsule(ctx context.Context, p *gameSession, slot int16, itemID int64) (bool, error) {
	items, err := s.currentPVFItemCatalog()
	if err != nil || items == nil || items.source == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return false, nil
	}
	item, err := items.ResolveItem(uint32(itemID))
	if err != nil || item.Kind != dungeonDropItemStackable {
		return false, nil
	}
	doc, err := parseDungeonCardPVFDocument(items.source, item.PVFPath)
	if err != nil {
		return false, nil
	}
	d, recognized, err := experienceitem.ParseCapsule(itemID, doc)
	if !recognized {
		return false, nil
	}
	reject := func(cause error) (bool, error) {
		return true, s.answerRejectedExperienceItem(p, slot, itemID, "experience_capsule_rejected", cause)
	}
	if err != nil {
		return reject(err)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 {
		return reject(fmt.Errorf("experience capsule character is not bound"))
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	tables, err := progression.Load(ctx, items.source)
	if err != nil {
		return reject(err)
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return reject(fmt.Errorf("experience capsule repositories unavailable"))
	}
	if !p.townMu.TryLock() {
		return reject(fmt.Errorf("town scene is changing"))
	}
	if !p.dungeon.mu.TryLock() {
		p.townMu.Unlock()
		return reject(fmt.Errorf("dungeon scene is changing"))
	}
	if !s.pvpMu.TryLock() {
		p.dungeon.mu.Unlock()
		p.townMu.Unlock()
		return reject(fmt.Errorf("PVP state is changing"))
	}
	r, err := func() (experienceitem.CapsuleResult, error) {
		defer s.pvpMu.Unlock()
		defer p.dungeon.mu.Unlock()
		defer p.townMu.Unlock()
		if p.pvpRoom != nil {
			return experienceitem.CapsuleResult{}, experienceitem.ErrCapsule
		}
		for _, q := range s.pvpMatching {
			if q != nil && q.member != nil && q.member.session == p {
				return experienceitem.CapsuleResult{}, experienceitem.ErrCapsule
			}
		}
		place := "[dungeon]"
		if p.dungeon.runtime == nil {
			area, known := s.townArea(int64(p.townPositionSnapshot.TownID), int64(p.townPositionSnapshot.AreaID))
			if p.townSceneReadyCharacterID != identity.character || !p.townPositionSnapshot.PositionValid || !known || area.MapPath == "" {
				return experienceitem.CapsuleResult{}, experienceitem.ErrCapsule
			}
			place = "[village]"
			if townMapPathLooksLikeSeria(area.MapPath) {
				place = "[seria room]"
			}
		}
		if len(d.Places) > 0 && !slices.Contains(d.Places, place) {
			return experienceitem.CapsuleResult{}, experienceitem.ErrCapsule
		}
		now := s.gameplayNow()
		result, useErr := experienceitem.UseCapsule(ctx, g, tables, experienceitem.CapsuleCommand{
			AccountID: s.accountIDForSession(p), CharacterID: strconv.Itoa(int(identity.character)), SlotIndex: slot, LevelCap: currentAdventureCharacterLevelCap, Definition: d, Now: now,
			Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) },
			LevelStats: s.characterPVFStatValuesForLevel,
			ProjectStack: func(stack repo.ItemStack) (repo.ItemStack, error) {
				if item.DailyDeleteItem {
					_, _, expired, err := itemexpiration.ReconcileDailyDelete(stack, now)
					if err != nil {
						return stack, err
					}
					if expired {
						return stack, experienceitem.ErrCapsule
					}
				}
				stack, _ = applyCurrentPVFItemExpirationAt(stack, item, now)
				if item.UsablePeriodDays > 0 && stack.ExpireAt.IsZero() {
					return stack, experienceitem.ErrCapsule
				}
				return stack, nil
			},
		})
		if useErr == nil && p.dungeon.runtime != nil {
			p.dungeon.runtime.Character = repo.CloneCharacter(result.Character)
		}
		return result, useErr
	}()
	if err != nil {
		return reject(err)
	}
	if err = s.sendGameUpperSuccess(p, 30, successExperienceItemBody(identity.character)); err != nil {
		return true, err
	}
	if err = s.sendSelectedIncrementalItemSlotRefreshes(p, "experience_capsule", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return true, err
	}
	if r.Progression.Experience.LevelsGained > 0 {
		if err = s.sendCurrentLevelUpTicketActorRefresh(ctx, p, identity, inventory.LevelUpTicketResult{Character: r.Character, Skill: r.Skill, Progression: r.Progression}); err != nil {
			return true, err
		}
	}
	if err = s.sendGameUpperRawClass(p, currentDungeonCharacterStateMsgID, buildCurrentFinishLoadingCharacterStateBody(r.Character, r.Skill.Points), 0); err != nil {
		return true, err
	}
	if r.Progression.Experience.LevelsGained > 0 {
		if err = s.sendCommittedDarkKnightSkillRefresh(p, r.Character, r.Skill, "experience_capsule"); err != nil {
			return true, err
		}
	}
	s.logGameEvent(p, "experience-capsule-committed", "item_id", r.ItemID, "gain", r.Progression.Experience.Gain, "previous_level", r.Progression.Experience.PreviousLevel, "level", r.Character.Level, "remaining", r.Remaining)
	return true, nil
}
