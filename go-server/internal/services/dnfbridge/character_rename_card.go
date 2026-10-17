package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentRenameCardRules struct {
	definition                          dungeonDropItemDefinition
	minimumLevel                        int64
	jobs                                []string
	cooldownSeconds, reservationSeconds int64
}

func currentRenameCardRulesFromPVF(catalog *pvfCeraShopCatalog, itemID uint32) (currentRenameCardRules, error) {
	if catalog == nil || catalog.items == nil || catalog.source == nil {
		return currentRenameCardRules{}, characterdata.ErrRenameUnavailable
	}
	d, err := catalog.items.ResolveItem(itemID)
	if err != nil {
		return currentRenameCardRules{}, err
	}
	if d.Kind != dungeonDropItemStackable {
		return currentRenameCardRules{}, characterdata.ErrRenameUnavailable
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, d.PVFPath)
	if err != nil {
		return currentRenameCardRules{}, err
	}
	r := currentRenameCardRules{definition: d}
	path := strings.ToLower(strings.ReplaceAll(d.PVFPath, "\\", "/"))
	shopCard := path == "stackable/cash/chn_character_rename.stk"
	if shopCard {
		if d.StackableType != "[etc]" {
			return r, characterdata.ErrRenameUnavailable
		}
		r.cooldownSeconds = int64(characterdata.RenameCooldown / time.Second)
		r.reservationSeconds = int64(characterdata.PreviousNameReservation / time.Second)
	} else {
		if d.StackableType != "[waste]" || d.ActionType != "[character name change]" {
			return r, characterdata.ErrRenameUnavailable
		}
		places := doc.Texts("action usable place")
		village := false
		for _, p := range places {
			switch p {
			case "[village]":
				village = true
			case "[seria room]", "[dont use assult]":
			default:
				return r, fmt.Errorf("%w: unknown rename place %s", characterdata.ErrRenameUnavailable, p)
			}
		}
		if !village {
			return r, characterdata.ErrRenameUnavailable
		}
	}
	minimum, found := doc.Int("minimum level")
	if !found || minimum < 1 || minimum > 100 {
		return r, characterdata.ErrRenameUnavailable
	}
	r.minimumLevel = minimum
	r.jobs = doc.Texts("usable job")
	if len(r.jobs) == 0 {
		return r, characterdata.ErrRenameUnavailable
	}
	for _, j := range r.jobs {
		if j != "[all]" {
			valid := false
			for job := 0; job <= jobmap.MaxID; job++ {
				for _, tag := range jobmap.QuestTags(job) {
					valid = valid || tag == j
				}
			}
			if !valid {
				return r, characterdata.ErrRenameUnavailable
			}
		}
	}
	return r, nil
}

func commitCurrentRenameCard(ctx context.Context, repos dnfrepo.Group, catalog *pvfCeraShopCatalog, accountID, characterID string, slot int16, now time.Time) ([]currentItemListEntry, error) {
	result, err := characterdata.UseRenameCard(ctx, repos, accountID, characterID, slot, now, func(c dnfrepo.CharacterRecord, held dnfrepo.ItemStack) (characterdata.RenameCardPolicy, dnfrepo.ItemStack, error) {
		if currentDungeonDiscardStackLocked(held) {
			return characterdata.RenameCardPolicy{}, held, characterdata.ErrRenameUnavailable
		}
		rules, err := currentRenameCardRulesFromPVF(catalog, uint32(held.ItemID))
		if err != nil {
			return characterdata.RenameCardPolicy{}, held, err
		}
		projected, _ := applyCurrentPVFItemExpirationAt(held, rules.definition, now)
		if currentDungeonDiscardStackExpired(projected, now) {
			return characterdata.RenameCardPolicy{}, held, characterdata.ErrRenameUnavailable
		}
		allowedJob := false
		for _, job := range rules.jobs {
			allowedJob = allowedJob || job == "[all]" || jobmap.MatchesQuestTag(job, numericCharacterStat(c.Job))
		}
		if !allowedJob {
			return characterdata.RenameCardPolicy{}, held, characterdata.ErrRenameUnavailable
		}
		// need material is the NPC exchange recipe. It is not a second use fee.
		policy := characterdata.RenameCardPolicy{MinimumLevel: rules.minimumLevel, CooldownSeconds: rules.cooldownSeconds, ReservationSeconds: rules.reservationSeconds, Slots: itemplacement.SlotPolicy{ListType: dnfrepo.MainInventoryListType, Start: rules.definition.SlotStart, End: rules.definition.SlotEnd, AllowQuickSlots: rules.definition.StackableType == "[waste]"}}
		return policy, projected, nil
	})
	if err != nil {
		return nil, err
	}
	if !result.Changed {
		return nil, nil
	}
	var entry currentItemListEntry
	if result.Removed {
		entry.patchCore(slot, math.MaxUint32, 0)
	} else {
		entry = currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, result.Stack)
	}
	return []currentItemListEntry{entry}, nil
}

func (s *Service) handleCurrentRenameCard(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketUseRenameCard)
	fail := func(err error) error {
		s.logGameEvent(session, "game-character-rename-card-rejected", "error", err)
		return s.sendGameUpperFailure(session, op, 2)
	}
	if session == nil || len(body) != 2 || !session.loginPacketSeen || session.accountID == "" {
		return fail(characterdata.ErrRenameUnavailable)
	}
	s.mu.Lock()
	identity := boundGameSessionCharacter{session: session, character: session.selectedCharacterID, generation: session.characterGeneration}
	account := session.accountID
	s.mu.Unlock()
	if identity.character == 0 || session.townSceneReadyCharacterID != identity.character {
		return fail(characterdata.ErrRenameUnavailable)
	}
	if _, inRaid := s.runtimeRaidForMember(identity.character); inRaid {
		return fail(characterdata.ErrRenameUnavailable)
	}
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return fail(characterdata.ErrRenameUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		return fail(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	catalog, err := s.currentCeraShopCatalog()
	if err != nil {
		return fail(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(errCharacterRepositoryMissing)
	}
	slot := binary.LittleEndian.Uint16(body)
	if slot > math.MaxInt16 {
		return fail(characterdata.ErrRenameUnavailable)
	}
	updates, err := commitCurrentRenameCard(ctx, repos, catalog, account, strconv.Itoa(int(identity.character)), int16(slot), s.gameplayNow().UTC())
	if err != nil {
		return fail(err)
	}
	if len(updates) > 0 {
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, updates), 0); err != nil {
			return err
		}
	}
	if err := s.sendGameUpperSuccess(session, op, nil); err != nil {
		return err
	}
	// resetGameSessionForCharacterSelect obtains the binding mutation lock;
	// release our reservation before entering that existing lifecycle owner.
	s.releaseCurrentCharacterAssets(reservation)
	return s.handleUpperReturnSelectCharacter(session, nil)
}
