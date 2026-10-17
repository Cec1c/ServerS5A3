package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) experienceBuffResolver(opcode dnfenum.CmdPacket) func(int64) (experiencebuff.Effect, error) {
	if opcode != dnfenum.CmdPacketUseStackable {
		return nil
	}
	// Match the existing optional PVF resolvers: a profile without an item
	// catalog retains the ordinary-consumable provenance fallback. Once the
	// catalog exists, missing/malformed potion definitions must reject use.
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil || catalog.source == nil {
		s.logPacketEvent("game-experience-potion-resolver-unavailable", "error", err)
		return nil
	}
	return func(itemID int64) (experiencebuff.Effect, error) {
		if itemID <= 0 || itemID > 0xffffffff {
			return experiencebuff.Effect{}, experiencebuff.ErrEffect
		}
		item, err := catalog.ResolveItem(uint32(itemID))
		if err != nil {
			return experiencebuff.Effect{}, err
		}
		if item.Kind != dungeonDropItemStackable {
			return experiencebuff.Effect{}, nil
		}
		doc, err := parseDungeonCardPVFDocument(catalog.source, item.PVFPath)
		if err != nil {
			return experiencebuff.Effect{}, err
		}
		return experiencebuff.Parse(doc)
	}
}

func (s *Service) loadExperiencePotionCharacter(ctx context.Context, session *gameSession) (repo.CharacterRecord, bool, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || session == nil {
		return repo.CharacterRecord{}, false, fmt.Errorf("experience potion character repository unavailable")
	}
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err == nil && found && character.AccountID != s.accountIDForSession(session) {
		return repo.CharacterRecord{}, false, fmt.Errorf("experience potion character owner mismatch")
	}
	return character, found, err
}
