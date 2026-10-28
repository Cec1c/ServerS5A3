package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentAdventureGenericFailureCode = 1

// The current NoPack executable and its DNF90 content profile cap characters
// at level 90. The progression PVF contains later table rows for compatibility,
// so its row count is not a playable-level cap.
const currentAdventureCharacterLevelCap = 90

var (
	errCurrentAdventureRuntimeUnavailable = errors.New("current adventure-group runtime is unavailable")
	errCurrentAdventureRequestInvalid     = errors.New("current adventure-group request is invalid")
	errCurrentAdventureStateInvalid       = errors.New("current adventure-group state is invalid")
)

type currentAdventureSelectedState struct {
	Repositories dnfrepo.Group
	AccountID    string
	CharacterID  string
	Character    dnfrepo.CharacterRecord
	Account      dnfrepo.AccountRecord
	Characters   []dnfrepo.CharacterRecord
	Summary      adventuregroup.Summary
	Config       adventuregroup.RuntimeConfig
	Runtime      adventuregroup.RuntimeState
}

func (s *Service) currentAdventureSelectedState(
	ctx context.Context,
	session *gameSession,
) (currentAdventureSelectedState, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return currentAdventureSelectedState{}, errCurrentAdventureRuntimeUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil || repositories.Character == nil {
		return currentAdventureSelectedState{}, errCurrentAdventureRuntimeUnavailable
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return currentAdventureSelectedState{}, err
	}
	if !found || strings.TrimSpace(character.AccountID) != accountID {
		return currentAdventureSelectedState{}, errCurrentAdventureStateInvalid
	}
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil {
		return currentAdventureSelectedState{}, err
	}
	if !found || strings.TrimSpace(account.AccountID) != accountID {
		return currentAdventureSelectedState{}, errCurrentAdventureStateInvalid
	}
	state, err := s.currentAccountAdventureGroupState(ctx, character, true, session)
	if err != nil {
		return currentAdventureSelectedState{}, err
	}
	config := s.adventureGroupTable.Runtime()
	runtime, err := adventuregroup.ParseRuntimeState(account, config, s.gameplayNow())
	if err != nil {
		return currentAdventureSelectedState{}, err
	}
	return currentAdventureSelectedState{
		Repositories: repositories,
		AccountID:    accountID,
		CharacterID:  characterID,
		Character:    character,
		Account:      account,
		Characters:   state.Characters,
		Summary:      state.Summary,
		Config:       config,
		Runtime:      runtime,
	}, nil
}

func saveCurrentAdventureRuntime(
	ctx context.Context,
	accounts dnfrepo.AccountRepository,
	account dnfrepo.AccountRecord,
	state adventuregroup.RuntimeState,
	now time.Time,
) error {
	value, err := state.Marshal()
	if err != nil {
		return err
	}
	return dnfrepo.SaveAccountMetadataEntry(
		ctx,
		accounts,
		account,
		adventuregroup.RuntimeStateMetadataKey,
		value,
		now,
	)
}

func (s *Service) handleCurrentAdventureExpeditionInfo(session *gameSession, request []byte) error {
	if len(request) < 1 || int(request[0])+1 != len(request) {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryInfo), currentAdventureGenericFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.adventureGroupRuntimeMu.Lock()
	runtime, err := s.currentAdventureExpeditionInfoState(ctx, session)
	s.adventureGroupRuntimeMu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-adventure-expedition-info-rejected", "error", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryInfo), currentAdventureGenericFailureCode)
	}
	body, view, err := s.currentAdventureExpeditionRosterView(ctx, session, runtime)
	if err != nil || !s.currentAdventureExpeditionViewCurrent(session, view) {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryInfo), currentAdventureGenericFailureCode)
	}
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketMercenaryInfo), body); err != nil {
		return err
	}
	if s.currentAdventureExpeditionViewCurrent(session, view) {
		session.currentAdventureExpeditionView = view
	}
	return nil
}

// Official 24.9 sends CMD441 after authenticated channel reconnect, before
// CMD4 selects a character (production game-000071, 2026-10-06 10:36 UTC).
// Expeditions belong to the account, so this read may use the authenticated
// account's durable runtime without selecting or inventing a character.
// Mutations retain currentAdventureSelectedState's character ownership check.
func (s *Service) currentAdventureExpeditionInfoState(ctx context.Context, session *gameSession) (adventuregroup.RuntimeState, error) {
	if s == nil || session == nil {
		return adventuregroup.RuntimeState{}, errCurrentAdventureRuntimeUnavailable
	}
	if session.selectedCharacterID != 0 || s.options.currentClientVersion != "official-24.9" {
		state, err := s.currentAdventureSelectedState(ctx, session)
		return state.Runtime, err
	}
	accountID := strings.TrimSpace(session.accountID)
	if accountID == "" || !session.accountFromToken || !session.loginPacketSeen || s.adventureGroupTable == nil {
		return adventuregroup.RuntimeState{}, errCurrentAdventureRuntimeUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil {
		return adventuregroup.RuntimeState{}, errCurrentAdventureRuntimeUnavailable
	}
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil {
		return adventuregroup.RuntimeState{}, err
	}
	if !found || strings.TrimSpace(account.AccountID) != accountID {
		return adventuregroup.RuntimeState{}, errCurrentAdventureStateInvalid
	}
	return adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), s.gameplayNow())
}

type currentAdventureExpeditionStartRequest struct {
	Area            byte
	DurationSeconds uint32
	Members         []currentAdventureExpeditionMemberRequest
}

type currentAdventureExpeditionMemberRequest struct {
	Selector byte
	Job      byte
	GrowType byte
}

func parseCurrentAdventureExpeditionStartRequest(body []byte) (currentAdventureExpeditionStartRequest, error) {
	if len(body) < 7 {
		return currentAdventureExpeditionStartRequest{}, errCurrentAdventureRequestInvalid
	}
	count := int(binary.LittleEndian.Uint16(body[5:7]))
	if count <= 0 || count > 4 || len(body) != 7+count*3 {
		return currentAdventureExpeditionStartRequest{}, errCurrentAdventureRequestInvalid
	}
	request := currentAdventureExpeditionStartRequest{
		Area:            body[0],
		DurationSeconds: binary.LittleEndian.Uint32(body[1:5]),
		Members:         make([]currentAdventureExpeditionMemberRequest, count),
	}
	for index := range request.Members {
		offset := 7 + index*3
		request.Members[index] = currentAdventureExpeditionMemberRequest{
			Selector: body[offset],
			Job:      body[offset+1],
			GrowType: body[offset+2],
		}
	}
	return request, nil
}

func (s *Service) handleCurrentAdventureExpeditionStart(session *gameSession, body []byte) error {
	request, err := parseCurrentAdventureExpeditionStartRequest(body)
	if err != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), currentAdventureGenericFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := s.gameplayNow()
	s.adventureGroupRuntimeMu.Lock()
	defer s.adventureGroupRuntimeMu.Unlock()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), currentAdventureGenericFailureCode)
	}
	area, found := state.Config.Area(request.Area)
	if !found || state.Summary.ManageLevel < area.RequiredManageLevel {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), 7)
	}
	if _, exists := state.Runtime.Expeditions[adventuregroup.ExpeditionKey(request.Area)]; exists {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), 14)
	}
	if _, found := area.RewardRates[request.DurationSeconds]; !found {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), 18)
	}
	if !s.validateCurrentAdventureExpeditionSentMembers(session, request.Members, state.Characters) {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), 22)
	}
	members, rewardInputs, ok := resolveCurrentAdventureExpeditionMembers(
		request.Members,
		state.Characters,
		state.Runtime,
	)
	if !ok {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), 22)
	}
	reward, ok := state.Config.ExpeditionReward(request.Area, request.DurationSeconds, rewardInputs, now)
	if !ok {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), 22)
	}
	startedAt := now.Unix()
	state.Runtime.Expeditions[adventuregroup.ExpeditionKey(request.Area)] = adventuregroup.Expedition{
		Area:       request.Area,
		State:      1,
		StartedAt:  startedAt,
		EndsAt:     startedAt + int64(request.DurationSeconds),
		Attributes: state.Config.AreaAttributes(request.Area, now),
		Members:    members,
		Reward:     reward,
	}
	if err := saveCurrentAdventureRuntime(ctx, state.Repositories.Account, state.Account, state.Runtime, now); err != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMercenaryCompetition), currentAdventureGenericFailureCode)
	}
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketMercenaryCompetition), nil); err != nil {
		return err
	}
	return s.sendCurrentAdventureExpeditionStatePush(session, state.Runtime, now)
}

func resolveCurrentAdventureExpeditionMembers(
	requests []currentAdventureExpeditionMemberRequest,
	characters []dnfrepo.CharacterRecord,
	runtime adventuregroup.RuntimeState,
) ([]adventuregroup.ExpeditionMember, []adventuregroup.ExpeditionMemberInput, bool) {
	used := make(map[byte]struct{}, len(requests))
	active, known := currentAdventureExpeditionBusyIdentities(runtime, characters)
	if !known {
		return nil, nil, false
	}
	usedIdentities := make(map[string]bool)
	members := make([]adventuregroup.ExpeditionMember, 0, len(requests))
	inputs := make([]adventuregroup.ExpeditionMemberInput, 0, len(requests))
	for _, request := range requests {
		if _, duplicate := used[request.Selector]; duplicate {
			return nil, nil, false
		}
		used[request.Selector] = struct{}{}
		var character dnfrepo.CharacterRecord
		found := false
		for _, candidate := range characters {
			if candidate.Slot == int(request.Selector) {
				if found {
					return nil, nil, false
				}
				character = candidate
				found = true
			}
		}
		job := byte(numericCharacterStat(character.Job))
		grow, validGrow := currentAdventureExpeditionBranch(character)
		if !found || !validGrow || character.CharacterID == "" || job != request.Job || grow != request.GrowType || character.Level <= 0 || usedIdentities[character.CharacterID] {
			return nil, nil, false
		}
		memberID := uint16(request.Selector)
		if active[character.CharacterID] {
			return nil, nil, false
		}
		usedIdentities[character.CharacterID] = true
		members = append(members, adventuregroup.ExpeditionMember{
			CharacterRecordID: character.CharacterID,
			CharacterID:       memberID,
			Name:              character.Name,
			Level:             currentAdventureInfoRosterHonorLevel(character),
			Job:               job,
			GrowType:          grow,
		})
		inputs = append(inputs, adventuregroup.ExpeditionMemberInput{
			Level: character.Level, Job: job, GrowType: grow,
		})
	}
	return members, inputs, true
}

func (s *Service) handleCurrentAdventureExpeditionCancel(session *gameSession, body []byte) error {
	if len(body) != 1 {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionCancelMsgID, currentAdventureGenericFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := s.gameplayNow()
	s.adventureGroupRuntimeMu.Lock()
	defer s.adventureGroupRuntimeMu.Unlock()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionCancelMsgID, currentAdventureGenericFailureCode)
	}
	key := adventuregroup.ExpeditionKey(body[0])
	if _, found := state.Runtime.Expeditions[key]; !found {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionCancelMsgID, currentAdventureGenericFailureCode)
	}
	delete(state.Runtime.Expeditions, key)
	if err := saveCurrentAdventureRuntime(ctx, state.Repositories.Account, state.Account, state.Runtime, now); err != nil {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionCancelMsgID, currentAdventureGenericFailureCode)
	}
	if err := s.sendGameUpperSuccess(session, currentAdventureExpeditionCancelMsgID, nil); err != nil {
		return err
	}
	return s.sendCurrentAdventureExpeditionStatePush(session, state.Runtime, now)
}

func (s *Service) handleCurrentAdventureExpeditionReward(session *gameSession, body []byte) error {
	if len(body) != 1 {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionRewardMsgID, currentAdventureGenericFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := s.gameplayNow()
	s.adventureGroupRuntimeMu.Lock()
	defer s.adventureGroupRuntimeMu.Unlock()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionRewardMsgID, currentAdventureGenericFailureCode)
	}
	key := adventuregroup.ExpeditionKey(body[0])
	expedition, found := state.Runtime.Expeditions[key]
	if !found || expedition.EndsAt > now.Unix() || expedition.Reward == 0 {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionRewardMsgID, currentAdventureGenericFailureCode)
	}
	delete(state.Runtime.Expeditions, key)
	state.Runtime.AddGlory(state.Config, expedition.Reward)
	if err := saveCurrentAdventureRuntime(ctx, state.Repositories.Account, state.Account, state.Runtime, now); err != nil {
		return s.sendGameUpperFailure(session, currentAdventureExpeditionRewardMsgID, currentAdventureGenericFailureCode)
	}
	var response packetWriter
	response.writeUint32(expedition.Reward)
	if err := s.sendGameUpperSuccess(session, currentAdventureExpeditionRewardMsgID, response.bytes()); err != nil {
		return err
	}
	if err := s.sendCurrentAdventureExpeditionStatePush(session, state.Runtime, now); err != nil {
		return err
	}
	return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "expedition_reward")
}

func (s *Service) handleCurrentAdventurePointRecalculate(session *gameSession, body []byte) error {
	if len(body) != 0 {
		return s.sendGameUpperFailure(session, currentAdventurePointRecalculateMsgID, currentAdventureGenericFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.adventureGroupRuntimeMu.Lock()
	state, err := s.currentAdventureSelectedState(ctx, session)
	s.adventureGroupRuntimeMu.Unlock()
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventurePointRecalculateMsgID, currentAdventureGenericFailureCode)
	}
	var response packetWriter
	response.writeUint32(state.Runtime.ShopPoints[adventuregroup.ShopPointGlory])
	response.writeUint32(state.Runtime.ShopPoints[adventuregroup.ShopPointBrave])
	response.writeByte(0)
	return s.sendGameUpperSuccess(session, currentAdventurePointRecalculateMsgID, response.bytes())
}

func buildCurrentAdventureExpeditionStateBody(state adventuregroup.RuntimeState, now time.Time) []byte {
	keys := make([]int, 0, len(state.Expeditions))
	for key := range state.Expeditions {
		value, err := strconv.Atoi(key)
		if err == nil && value >= 0 && value <= math.MaxUint8 {
			keys = append(keys, value)
		}
	}
	sort.Ints(keys)
	if len(keys) > math.MaxUint8 {
		keys = keys[:math.MaxUint8]
	}
	var writer packetWriter
	writer.writeByte(byte(len(keys)))
	for _, areaValue := range keys {
		expedition := state.Expeditions[strconv.Itoa(areaValue)]
		expeditionState := expedition.State
		if areaValue > 0 && expedition.EndsAt > 0 && expedition.EndsAt <= now.Unix() {
			expeditionState = 2
		}
		writer.writeByte(byte(areaValue))
		writer.writeByte(expeditionState)
		writer.writeUint32(clampAdventureUnix(expedition.StartedAt))
		writer.writeUint32(clampAdventureUnix(expedition.EndsAt))
		attributeCount := len(expedition.Attributes)
		if attributeCount > math.MaxUint8 {
			attributeCount = math.MaxUint8
		}
		writer.writeByte(byte(attributeCount))
		for _, attribute := range expedition.Attributes[:attributeCount] {
			writer.writeByte(attribute)
		}
		memberCount := len(expedition.Members)
		if memberCount > math.MaxUint16 {
			memberCount = math.MaxUint16
		}
		writer.writeUint16(uint16(memberCount))
		for _, member := range expedition.Members[:memberCount] {
			writer.writeUint16(member.CharacterID)
			writer.writeRawDstr(rosterNameBytes(member.Name))
			writer.writeUint32(member.Level)
			writer.writeByte(member.Job)
			writer.writeByte(member.GrowType)
			writer.writeByte(member.Status)
		}
	}
	return writer.bytes()
}

func clampAdventureUnix(value int64) uint32 {
	if value <= 0 {
		return 0
	}
	if value > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(value)
}

func (s *Service) sendCurrentAdventureExpeditionStatePush(
	session *gameSession,
	state adventuregroup.RuntimeState,
	now time.Time,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	viewBody, view, err := s.currentAdventureExpeditionRosterView(ctx, session, state)
	if err != nil {
		return err
	}
	if !s.currentAdventureExpeditionViewCurrent(session, view) {
		return errCurrentAdventureStateInvalid
	}
	var body packetWriter
	body.writeByte(1)
	body.writeBytes(viewBody)
	if err := s.sendGameUpperRawClass(session, currentAdventureExpeditionStateMsgID, body.bytes(), 0); err != nil {
		return err
	}
	if s.currentAdventureExpeditionViewCurrent(session, view) {
		session.currentAdventureExpeditionView = view
	}
	return nil
}

type currentAdventureShopCommit struct {
	Inventory dnfrepo.InventoryRecord
	Slots     []uint16
}

func (s *Service) handleCurrentAdventureShopPurchase(session *gameSession, body []byte) error {
	if len(body) != 5 {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	categoryIndex := body[0]
	selector := binary.LittleEndian.Uint32(body[1:5])
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := s.gameplayNow()

	s.adventureGroupRuntimeMu.Lock()
	defer s.adventureGroupRuntimeMu.Unlock()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil || state.Repositories.CeraShopAssets == nil {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	category, found := state.Config.Shop(categoryIndex)
	if !found {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	product, found := resolveCurrentAdventureShopProduct(category, selector)
	if !found || state.Summary.ManageLevel < product.RequiredManageLevel {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	definition, err := catalog.ResolveItem(product.ItemID)
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	// The catalog retains relative PVF periods. Freeze the purchase deadline
	// before granting, as the other reward owners do; a raw relative definition
	// otherwise creates a row with a zero deadline and appears already expired.
	definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	var result currentAdventureShopCommit
	err = state.Repositories.CeraShopAssets.WithinCeraShopAssets(
		ctx,
		state.AccountID,
		state.CharacterID,
		"adventure_group_shop:"+state.CharacterID,
		func(
			accounts dnfrepo.AccountRepository,
			characters dnfrepo.CharacterRepository,
			inventories dnfrepo.InventoryRepository,
			_ dnfrepo.EquipmentRepository,
			_ dnfrepo.SettingsRepository,
		) error {
			account, found, err := accounts.Load(ctx, state.AccountID)
			if err != nil || !found {
				return errCurrentAdventureStateInvalid
			}
			character, found, err := characters.Load(ctx, state.CharacterID)
			if err != nil || !found || strings.TrimSpace(character.AccountID) != state.AccountID {
				return errCurrentAdventureStateInvalid
			}
			inventory, found, err := inventories.Load(ctx, state.CharacterID)
			if err != nil || !found {
				return errCurrentAdventureStateInvalid
			}
			runtime, err := adventuregroup.ParseRuntimeState(account, state.Config, now)
			if err != nil {
				return err
			}
			if err := runtime.Spend(category, product); err != nil {
				return err
			}
			if err := dnfrepo.BindInventoryContainerState(ctx, state.Repositories.Settings, &inventory); err != nil {
				return err
			}
			slots, err := grantCurrentCeraShopProductAt(&inventory, definition, 1, now)
			if err != nil {
				return err
			}
			encoded, err := runtime.Marshal()
			if err != nil {
				return err
			}
			account = dnfrepo.CloneAccount(account)
			if account.Metadata == nil {
				account.Metadata = make(map[string]string)
			}
			account.Metadata[adventuregroup.RuntimeStateMetadataKey] = encoded
			account.UpdatedAt = now.UTC()
			inventory.UpdatedAt = now.UTC()
			if err := accounts.Save(ctx, account); err != nil {
				return err
			}
			if err := inventories.Save(ctx, inventory); err != nil {
				return err
			}
			result = currentAdventureShopCommit{
				Inventory: dnfrepo.CloneInventory(inventory),
				Slots:     append([]uint16(nil), slots...),
			}
			return nil
		},
	)
	if err != nil {
		s.logGameEvent(session, "game-adventure-shop-purchase-rejected",
			"category", categoryIndex, "selector", selector, "item_id", product.ItemID, "error", err)
		return s.sendGameUpperFailure(session, currentAdventureShopPurchaseMsgID, currentAdventureGenericFailureCode)
	}
	if err := s.sendGameUpperSuccess(session, currentAdventureShopPurchaseMsgID, nil); err != nil {
		return err
	}
	if err := sendCurrentAdventureInventoryUpdates(s, session, result); err != nil {
		return err
	}
	return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "adventure_shop_purchase")
}

func resolveCurrentAdventureShopProduct(category adventuregroup.ShopCategory, selector uint32) (adventuregroup.ShopProduct, bool) {
	for _, product := range category.Products {
		if product.ItemID == selector {
			return product, true
		}
	}
	if uint64(selector) < uint64(len(category.Products)) {
		return category.Products[selector], true
	}
	return adventuregroup.ShopProduct{}, false
}

func sendCurrentAdventureInventoryUpdates(
	service *Service,
	session *gameSession,
	result currentAdventureShopCommit,
) error {
	entries := make([]currentItemListEntry, 0, len(result.Slots))
	for _, slot := range result.Slots {
		signedSlot := int16(slot)
		stack, found := result.Inventory.Slots[currentDungeonPickupMainSlotKey(signedSlot)]
		if !found {
			continue
		}
		entries = append(entries, currentItemListEntryFromStack(dnfrepo.MainInventoryListType, signedSlot, stack))
	}
	if len(entries) == 0 {
		return nil
	}
	body := buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, entries)
	return service.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}

type currentAdventureGrowthResult struct {
	Character dnfrepo.CharacterRecord
	Skill     dnfrepo.SkillRecord
}

func (s *Service) handleCurrentAdventureGrowthCapsule(session *gameSession, body []byte) error {
	if len(body) != 0 {
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := s.gameplayNow()
	s.adventureGroupRuntimeMu.Lock()
	defer s.adventureGroupRuntimeMu.Unlock()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil || state.Repositories.CharacterSettlement == nil {
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	preAwardExperience, present := state.Character.Stats["exp"]
	if !present || preAwardExperience < 0 || uint64(preAwardExperience) > math.MaxUint32 {
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	preAwardHonorExpertGain, err := currentHonorExpertExperienceGain(
		tables,
		state.Character.Level,
		uint32(preAwardExperience),
		state.Config.Capsule.GrantedExperience,
	)
	if err != nil {
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	var honorTables *dnfhonor.Tables
	if preAwardHonorExpertGain > 0 {
		honorTables, err = s.loadHonorTable(ctx)
		if err != nil {
			return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
		}
	}
	var result currentAdventureGrowthResult
	err = state.Repositories.CharacterSettlement.WithinCharacterSettlement(ctx, state.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Account == nil {
			return errCurrentAdventureRuntimeUnavailable
		}
		account, found, err := tx.Account.Load(ctx, state.AccountID)
		if err != nil || !found {
			return errCurrentAdventureStateInvalid
		}
		character, found, err := tx.Character.Load(ctx, state.CharacterID)
		if err != nil || !found || strings.TrimSpace(character.AccountID) != state.AccountID {
			return errCurrentAdventureStateInvalid
		}
		skill, found, err := tx.Skill.Load(ctx, state.CharacterID)
		if err != nil || !found || skill.Points.SyncedLevel != character.Level {
			return errCurrentAdventureStateInvalid
		}
		capsule := state.Config.Capsule
		if character.Level < capsule.MinimumLevel || character.Level > capsule.MaximumLevel {
			return errCurrentAdventureRequestInvalid
		}
		runtime, err := adventuregroup.ParseRuntimeState(account, state.Config, now)
		if err != nil || !runtime.ConsumeCapsule(state.Config) {
			return errCurrentAdventureRequestInvalid
		}
		currentExperience := character.Stats["exp"]
		if currentExperience < 0 || uint64(currentExperience) > math.MaxUint32 {
			return errCurrentAdventureStateInvalid
		}
		plan, err := dnfdungeon.PlanSettlementProgressionAtCap(
			tables,
			character.Level,
			uint32(currentExperience),
			capsule.GrantedExperience,
			skill.Points,
			currentAdventureCharacterLevelCap,
		)
		if err != nil {
			return err
		}
		honorExpertGain, err := currentHonorExpertExperienceGain(
			tables,
			character.Level,
			uint32(currentExperience),
			capsule.GrantedExperience,
		)
		if err != nil {
			return err
		}
		character = dnfrepo.CloneCharacter(character)
		skill = dnfrepo.CloneSkill(skill)
		account = dnfrepo.CloneAccount(account)
		if err := progression.ApplyCharacterExperience(&character, &skill, plan.Experience.NewLevel, plan.Experience.NewExperience, plan.SkillPoints.New, now); err != nil {
			return err
		}
		if honorExpertGain > 0 {
			nextHonor, err := planCurrentHonorExpertProgress(honorTables, character, honorExpertGain)
			if err != nil {
				return err
			}
			for key, value := range currentHonorExpertStats(nextHonor) {
				character.Stats[key] = value
			}
		}
		character.UpdatedAt = now.UTC()
		skill.Points = plan.SkillPoints.New
		skill.UpdatedAt = now.UTC()
		encoded, err := runtime.Marshal()
		if err != nil {
			return err
		}
		if account.Metadata == nil {
			account.Metadata = make(map[string]string)
		}
		account.Metadata[adventuregroup.RuntimeStateMetadataKey] = encoded
		account.UpdatedAt = now.UTC()
		if err := dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		if err := dnfrepo.SaveSkillFields(ctx, tx.Skill, skill, dnfrepo.SkillFieldPoints); err != nil {
			return err
		}
		if err := tx.Account.Save(ctx, account); err != nil {
			return err
		}
		result = currentAdventureGrowthResult{Character: character, Skill: skill}
		return nil
	})
	if err != nil {
		s.logGameEvent(session, "game-adventure-growth-capsule-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentAdventureGrowthCapsuleMsgID, currentAdventureGenericFailureCode)
	}
	// 胶囊可推进角色经验；后续副本击杀必须从同一已提交状态继续规划。
	session.dungeon.mu.Lock()
	if runtime := session.dungeon.runtime; runtime != nil && runtime.Character.CharacterID == result.Character.CharacterID {
		runtime.Character = dnfrepo.CloneCharacter(result.Character)
	}
	session.dungeon.mu.Unlock()
	if err := s.sendGameUpperSuccess(session, currentAdventureGrowthCapsuleMsgID, nil); err != nil {
		return err
	}
	characterBody := buildCurrentFinishLoadingCharacterStateBody(result.Character, result.Skill.Points)
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, characterBody, 0); err != nil {
		return err
	}
	return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "adventure_growth_capsule")
}
