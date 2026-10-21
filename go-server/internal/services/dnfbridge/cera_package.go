package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentCeraPackageRequestHeaderSize = 3
	currentCeraPackageChoiceStride      = 5
	currentCeraPackageMaxChoices        = 32
	currentCeraPackageMaxRewardRows     = 128
	currentCeraPackageMaxRewardAmount   = 1_000_000
)

var (
	errCurrentCeraPackageRequestMalformed = errors.New("dnf cera package request is malformed")
	errCurrentCeraPackageOwnerUnavailable = errors.New("dnf cera package selected character is unavailable")
	errCurrentCeraPackageSourceMissing    = errors.New("dnf cera package source item is unavailable")
	errCurrentCeraPackagePVFInvalid       = errors.New("dnf cera package PVF data is invalid")
	errCurrentCeraPackageChoiceInvalid    = errors.New("dnf cera package selection is invalid")
	errCurrentCeraPackageExpired          = errors.New("dnf cera package has expired")
)

// currentCeraPackageOpenRequest is the exact current-EXE op518 body written by
// sub_1EC46D0: source slot, selected-avatar count, then item/option pairs.
// Item quantities never come from the client; they are resolved from PVF.
type currentCeraPackageOpenRequest struct {
	SourceSlot int16
	Choices    []currentCeraPackageChoice
}

type currentCeraPackageChoice struct {
	ItemID uint32
	Option byte
}

type currentCeraPackageDefinition struct {
	Source        dungeonDropItemDefinition
	Rewards       []currentCeraPackageReward
	AvatarItemIDs []uint32
}

type currentCeraPackageReward struct {
	Definition dungeonDropItemDefinition
	Count      uint32
	Avatar     bool
}

type currentCeraPackageCommitResult struct {
	ContractActivations []alignedcmd.PremiumContractActivation
	ChangedListTypes    []byte
	RewardRows          int
	RewardUnits         uint64
	OverflowMailID      string
	OverflowItems       int
	// GrantSlots 是本次 op518 在发放事务里**真正写过的 (list, slot)**：源格（list 0）与每个
	// 奖励落点（装扮 list 1、宠物 list 7、主背包 list 0；走邮件的溢出奖励不在这里）。
	// delta 档用它替代"逐个 ChangedListType 整容器回放"，见 item_grant_notice.go。
	GrantSlots []alignedcmd.ItemSlotRefresh
}

func parseCurrentCeraPackageOpenRequest(body []byte) (currentCeraPackageOpenRequest, error) {
	if len(body) < currentCeraPackageRequestHeaderSize {
		return currentCeraPackageOpenRequest{}, errCurrentCeraPackageRequestMalformed
	}
	count := int(body[2])
	// count == 0 是**合法**形状：礼包内容里没有装扮时客户端不做"选装扮"这一步，
	// 直接发 `[u16 源槽位][u8 0]`。旧代码把 count<1 当畸形，于是"欢乐暑假行程单"
	// （490702507，[package data] 只有 490702504 一个 [booster] 礼盒）永远只拿到
	// op518 失败体 4，玩家看到的就是"商城道具无效"。
	if count < 0 || count > currentCeraPackageMaxChoices || len(body) != currentCeraPackageRequestHeaderSize+count*currentCeraPackageChoiceStride {
		return currentCeraPackageOpenRequest{}, errCurrentCeraPackageRequestMalformed
	}
	request := currentCeraPackageOpenRequest{
		SourceSlot: int16(binary.LittleEndian.Uint16(body[0:2])),
		Choices:    make([]currentCeraPackageChoice, 0, count),
	}
	seen := make(map[uint32]struct{}, count)
	for index := 0; index < count; index++ {
		offset := currentCeraPackageRequestHeaderSize + index*currentCeraPackageChoiceStride
		itemID := binary.LittleEndian.Uint32(body[offset : offset+4])
		if itemID == 0 {
			return currentCeraPackageOpenRequest{}, errCurrentCeraPackageRequestMalformed
		}
		if _, duplicate := seen[itemID]; duplicate {
			return currentCeraPackageOpenRequest{}, fmt.Errorf("%w: duplicate item=%d", errCurrentCeraPackageChoiceInvalid, itemID)
		}
		seen[itemID] = struct{}{}
		request.Choices = append(request.Choices, currentCeraPackageChoice{ItemID: itemID, Option: body[offset+4]})
	}
	return request, nil
}

func resolveCurrentCeraPackageDefinition(
	catalog *pvfDungeonDropCatalog,
	sourceItemID uint32,
) (currentCeraPackageDefinition, error) {
	if catalog == nil || catalog.source == nil || sourceItemID == 0 {
		return currentCeraPackageDefinition{}, errCurrentCeraPackagePVFInvalid
	}
	sourceDefinition, err := catalog.ResolveItem(sourceItemID)
	if err != nil {
		return currentCeraPackageDefinition{}, fmt.Errorf("%w: resolve source item=%d: %v", errCurrentCeraPackagePVFInvalid, sourceItemID, err)
	}
	if sourceDefinition.Kind != dungeonDropItemStackable {
		return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d is not stackable", errCurrentCeraPackagePVFInvalid, sourceItemID)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, sourceDefinition.PVFPath)
	if err != nil {
		return currentCeraPackageDefinition{}, fmt.Errorf("%w: read source item=%d: %v", errCurrentCeraPackagePVFInvalid, sourceItemID, err)
	}
	stackableType, found := document.Text("stackable type")
	if !found || !strings.EqualFold(strings.TrimSpace(stackableType), "[usable cera package]") {
		return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d stackable_type=%q", errCurrentCeraPackagePVFInvalid, sourceItemID, stackableType)
	}
	values := document.Ints("package data")
	if len(values) == 0 || len(values)%2 != 0 || len(values)/2 > currentCeraPackageMaxRewardRows {
		return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d package_data_values=%d", errCurrentCeraPackagePVFInvalid, sourceItemID, len(values))
	}

	definition := currentCeraPackageDefinition{
		Source:  sourceDefinition,
		Rewards: make([]currentCeraPackageReward, 0, len(values)/2),
	}
	avatarIDs := make(map[uint32]struct{})
	var total uint64
	for offset := 0; offset < len(values); offset += 2 {
		itemValue, countValue := values[offset], values[offset+1]
		if itemValue <= 0 || itemValue > math.MaxUint32 || countValue <= 0 || countValue > currentCeraPackageMaxRewardAmount {
			return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d reward_pair=%d/%d", errCurrentCeraPackagePVFInvalid, sourceItemID, itemValue, countValue)
		}
		total += uint64(countValue)
		if total > currentCeraPackageMaxRewardAmount {
			return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d reward_total=%d", errCurrentCeraPackagePVFInvalid, sourceItemID, total)
		}
		rewardDefinition, resolveErr := catalog.ResolveItem(uint32(itemValue))
		if resolveErr != nil {
			return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d reward item=%d: %v", errCurrentCeraPackagePVFInvalid, sourceItemID, itemValue, resolveErr)
		}
		avatar, avatarErr := currentCeraPackageRewardIsAvatar(catalog.source, rewardDefinition)
		if avatarErr != nil {
			return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d reward item=%d: %v", errCurrentCeraPackagePVFInvalid, sourceItemID, itemValue, avatarErr)
		}
		reward := currentCeraPackageReward{Definition: rewardDefinition, Count: uint32(countValue), Avatar: avatar}
		definition.Rewards = append(definition.Rewards, reward)
		if avatar {
			if _, exists := avatarIDs[rewardDefinition.ItemID]; !exists {
				avatarIDs[rewardDefinition.ItemID] = struct{}{}
				definition.AvatarItemIDs = append(definition.AvatarItemIDs, rewardDefinition.ItemID)
			}
		}
	}
	// 装扮选项数是**上限**而不是下限：`[usable cera package]` 是一个 5454 件道具的
	// 大族，绝大多数（例如 490702507 欢乐暑假行程单 -> [package data] 490702504）
	// 的奖励里根本没有装扮。旧代码在这里要求 avatar_choices>0，于是这些礼包一律
	// 被判定为"PVF 非法"并以 op518 失败体 4 拒绝——现场就是"商城道具无效"。
	// 有装扮的礼包仍然要求客户端把每个装扮都选全（validateCurrentCeraPackageChoices）。
	if len(definition.AvatarItemIDs) > currentCeraPackageMaxChoices {
		return currentCeraPackageDefinition{}, fmt.Errorf("%w: source item=%d avatar_choices=%d", errCurrentCeraPackagePVFInvalid, sourceItemID, len(definition.AvatarItemIDs))
	}
	return definition, nil
}

// currentCeraPackageChoiceSummary renders the client's op518 selection as
// `item:option` pairs for the audit log. The package/礼包 dialog's "选择职业→
// 自动选择属性" widget lives entirely in the client (the job list is the reward
// avatars' PVF [usable job] and the attribute list its ability table); the
// server only ever receives (item id, option byte) per selected avatar. Logging
// the exact selection makes one live attempt enough to tell whether the client
// sent a subset, a duplicate record or an unknown id, instead of guessing.
func currentCeraPackageChoiceSummary(choices []currentCeraPackageChoice) string {
	if len(choices) == 0 {
		return "<none>"
	}
	var builder strings.Builder
	builder.WriteString(strconv.Itoa(len(choices)))
	builder.WriteString("[")
	for index, choice := range choices {
		if index > 0 {
			builder.WriteString(" ")
		}
		builder.WriteString(strconv.FormatUint(uint64(choice.ItemID), 10))
		builder.WriteString(":")
		builder.WriteString(strconv.FormatUint(uint64(choice.Option), 10))
	}
	builder.WriteString("]")
	return builder.String()
}

// currentCeraPackageExpectedSummary renders the package's own avatar rewards in
// PVF order so a rejected selection can be compared with what the client should
// have offered.
func currentCeraPackageExpectedSummary(definition currentCeraPackageDefinition) string {
	if len(definition.AvatarItemIDs) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(definition.AvatarItemIDs))
	for _, itemID := range definition.AvatarItemIDs {
		parts = append(parts, strconv.FormatUint(uint64(itemID), 10))
	}
	return strconv.Itoa(len(parts)) + "[" + strings.Join(parts, " ") + "]"
}

// currentCeraSelectionBodyHex renders a bounded hex dump of a purchase/open
// request body. The cera package (op518) and cera shop cart (op64) layouts were
// established from the current EXE, but both carry bytes the server does not
// consume yet (for example the eight trailing bytes of each 15-byte cart row).
// A rejection log that includes the raw body makes one live attempt enough to
// prove or disprove a wire-layout mistake without a client packet capture.
func currentCeraSelectionBodyHex(body []byte) string {
	const limit = 512
	if len(body) == 0 {
		return "<empty>"
	}
	if len(body) > limit {
		return hex.EncodeToString(body[:limit]) + fmt.Sprintf("..(+%d)", len(body)-limit)
	}
	return hex.EncodeToString(body)
}

func currentCeraPackageRewardIsAvatar(source dnfpvf.Source, definition dungeonDropItemDefinition) (bool, error) {
	if definition.Kind != dungeonDropItemEquipment {
		return false, nil
	}
	document, err := parseDungeonCardPVFDocument(source, definition.PVFPath)
	if err != nil {
		return false, err
	}
	equipmentType, found := document.Text("equipment type")
	if !found {
		return false, nil
	}
	rule, supported := currentEquipmentPlacementRuleForPVFType(equipmentType)
	return supported && rule.class == currentEquipmentPlacementClassAvatar, nil
}

func validateCurrentCeraPackageChoices(
	definition currentCeraPackageDefinition,
	choices []currentCeraPackageChoice,
) (map[uint32]byte, error) {
	if len(choices) != len(definition.AvatarItemIDs) {
		return nil, fmt.Errorf("%w: got=%d want=%d", errCurrentCeraPackageChoiceInvalid, len(choices), len(definition.AvatarItemIDs))
	}
	expected := make(map[uint32]struct{}, len(definition.AvatarItemIDs))
	for _, itemID := range definition.AvatarItemIDs {
		expected[itemID] = struct{}{}
	}
	options := make(map[uint32]byte, len(choices))
	for _, choice := range choices {
		if _, ok := expected[choice.ItemID]; !ok {
			return nil, fmt.Errorf("%w: item=%d is not an avatar reward", errCurrentCeraPackageChoiceInvalid, choice.ItemID)
		}
		if _, duplicate := options[choice.ItemID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate item=%d", errCurrentCeraPackageChoiceInvalid, choice.ItemID)
		}
		options[choice.ItemID] = choice.Option
	}
	for itemID := range expected {
		if _, ok := options[itemID]; !ok {
			return nil, fmt.Errorf("%w: avatar item=%d was not selected", errCurrentCeraPackageChoiceInvalid, itemID)
		}
	}
	return options, nil
}

func validateCurrentCeraPackageSourceExpiration(
	stack dnfrepo.ItemStack,
	source dungeonDropItemDefinition,
	now time.Time,
) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if expire := currentItemListStackExpire(stack); expire != 0 {
		if uint64(expire) <= uint64(now.Unix()) {
			return errCurrentCeraPackageExpired
		}
		return nil
	}
	if source.UsablePeriodDays > 0 {
		return errCurrentCeraPackageExpired
	}
	if !source.ExpirationDate.IsZero() && !now.Before(source.ExpirationDate) {
		return errCurrentCeraPackageExpired
	}
	return nil
}

func (s *Service) handleCurrentCeraPackageOpen(session *gameSession, body []byte) error {
	request, err := parseCurrentCeraPackageOpenRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-cera-package-open-rejected", "body_len", len(body), "body_hex", currentCeraSelectionBodyHex(body), "reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenCerapackage), 4)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logGameEvent(session, "game-cera-package-open-rejected", "source_slot", request.SourceSlot, "reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenCerapackage), 4)
	}
	definition, err := s.prepareCurrentCeraPackage(ctx, session, catalog, request)
	if err != nil {
		s.logGameEvent(session, "game-cera-package-open-rejected", "source_slot", request.SourceSlot, "choice_count", len(request.Choices), "choices", currentCeraPackageChoiceSummary(request.Choices), "reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenCerapackage), 4)
	}
	result, err := s.commitCurrentCeraPackage(ctx, session, catalog, definition, request)
	if err != nil {
		s.logGameEvent(session, "game-cera-package-open-rejected", "source_slot", request.SourceSlot, "source_item", definition.Source.ItemID, "choice_count", len(request.Choices), "choices", currentCeraPackageChoiceSummary(request.Choices), "expected_avatars", currentCeraPackageExpectedSummary(definition), "reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketOpenCerapackage), 4)
	}
	var success packetWriter
	success.writeUint16(uint16(request.SourceSlot))
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketOpenCerapackage), success.bytes()); err != nil {
		return err
	}
	// 形状开关（默认 delta = 修复档）：只对**本次真正写过的 (list, slot)** 发
	// class0/msg14 逐槽增量，奖励容器不再回放整容器 —— 装扮页（list 1）的分页高亮读的
	// 就是这条逐槽行（见 item_grant_notice.go）。list 38 例外仍走整容器；list 7 的写入进
	// Delta 后由发送侧改期成 1500 ms 整容器。写 containers 一键回退到今天的线格式。
	grantContainerLists := result.ChangedListTypes
	if itemGrantNoticeUsesDelta() {
		plan := buildItemGrantNoticePlan(result.GrantSlots)
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "open_cera_package", plan.Delta); err != nil {
			return err
		}
		grantContainerLists = plan.ContainerLists
		s.logPacketEvent("game-item-grant-notice-planned",
			"conn_id", session.connID,
			"operation", "open_cera_package",
			"char_id", session.selectedCharacterID,
			"mode", itemGrantNoticeDelta,
			"delta_slots", currentItemSlotRefreshesSummary(plan.Delta),
			"container_lists", plan.ContainerLists,
			"legacy_changed_lists", result.ChangedListTypes)
	}
	for _, listType := range grantContainerLists {
		listBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, listType)
		if !ok {
			return errCurrentCeraPackageOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), listBody); err != nil {
			return err
		}
	}
	if err := s.sendAlignedPremiumContractActivations(session, "open_cera_package", result.ContractActivations); err != nil {
		return err
	}
	if result.OverflowMailID != "" {
		if err := s.sendMailboxAlarmToOnlineRecipient(session.selectedCharacterID); err != nil {
			// The package, its directly placed rewards, and the overflow mail
			// have already committed together. A missed live alarm is harmless:
			// the durable mailbox snapshot will still show the attachment when
			// the player next opens the mailbox.
			s.logWarn("cera package mailbox alarm deferred to next mailbox open",
				"character_id", session.selectedCharacterID,
				"mail_id", result.OverflowMailID,
				"error", err)
		}
	}
	s.logGameEvent(session, "game-cera-package-open-committed",
		"source_slot", request.SourceSlot,
		"source_item", definition.Source.ItemID,
		"choice_count", len(request.Choices),
		"choices", currentCeraPackageChoiceSummary(request.Choices),
		"expected_avatars", currentCeraPackageExpectedSummary(definition),
		"reward_rows", result.RewardRows,
		"reward_units", result.RewardUnits,
		"overflow_mail_id", result.OverflowMailID,
		"overflow_items", result.OverflowItems,
		"changed_lists", fmt.Sprint(result.ChangedListTypes),
		"container_lists", fmt.Sprint(grantContainerLists),
		"item_grant_notice", itemGrantNoticeMode(),
		"ack_body", "current_exe_op518_success_u16_slot",
		"inventory_refresh", "current_exe_op13_real_repository_lists")
	return nil
}

func (s *Service) prepareCurrentCeraPackage(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	request currentCeraPackageOpenRequest,
) (currentCeraPackageDefinition, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || request.SourceSlot < 0 || catalog == nil {
		return currentCeraPackageDefinition{}, errCurrentCeraPackageOwnerUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return currentCeraPackageDefinition{}, errCurrentCeraPackageOwnerUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return currentCeraPackageDefinition{}, err
	}
	if !found || inventory.CharacterID != characterID {
		return currentCeraPackageDefinition{}, errCurrentCeraPackageOwnerUnavailable
	}
	stack, found := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, request.SourceSlot)]
	if !found || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 {
		return currentCeraPackageDefinition{}, errCurrentCeraPackageSourceMissing
	}
	definition, err := resolveCurrentCeraPackageDefinition(catalog, uint32(stack.ItemID))
	if err != nil {
		return currentCeraPackageDefinition{}, err
	}
	now := time.Now().UTC()
	if err := validateCurrentCeraPackageSourceExpiration(stack, definition.Source, now); err != nil {
		return currentCeraPackageDefinition{}, err
	}
	if _, err := validateCurrentCeraPackageChoices(definition, request.Choices); err != nil {
		return currentCeraPackageDefinition{}, err
	}
	return definition, nil
}

func (s *Service) commitCurrentCeraPackage(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	definition currentCeraPackageDefinition,
	request currentCeraPackageOpenRequest,
) (currentCeraPackageCommitResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.source == nil || definition.Source.ItemID == 0 {
		return currentCeraPackageCommitResult{}, errCurrentCeraPackageOwnerUnavailable
	}
	options, err := validateCurrentCeraPackageChoices(definition, request.Choices)
	if err != nil {
		return currentCeraPackageCommitResult{}, err
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.MailboxAssets == nil {
		return currentCeraPackageCommitResult{}, errCurrentCeraPackageOwnerUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	now := time.Now().UTC()
	contracts := s.currentPremiumRewardContracts()
	hasContracts := false
	for _, reward := range definition.Rewards {
		if _, ok := contracts[int64(reward.Definition.ItemID)]; ok {
			hasContracts = true
		}
	}
	within := func(apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
		if hasContracts {
			tx, ok := repositories.MailboxAssets.(dnfrepo.AccountTradeMailboxUnitOfWork)
			if !ok || accountID == "" {
				return errCurrentCeraPackageOwnerUnavailable
			}
			return tx.WithinAccountTradeMailbox(ctx, accountID, []string{characterID}, characterID, apply)
		}
		return repositories.MailboxAssets.WithinMailboxAssets(ctx, characterID, characterID, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
			return apply(nil, c, i, m)
		})
	}
	var result currentCeraPackageCommitResult
	err = within(func(
		accounts dnfrepo.AccountRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || accountID == "" || strings.TrimSpace(character.AccountID) != accountID {
			return errCurrentCeraPackageOwnerUnavailable
		}
		var account dnfrepo.AccountRecord
		if hasContracts {
			if accounts == nil {
				return errCurrentCeraPackageOwnerUnavailable
			}
			account, found, err = accounts.Load(ctx, accountID)
			if err != nil {
				return err
			}
			if !found || account.AccountID != accountID {
				return errCurrentCeraPackageOwnerUnavailable
			}
			account = dnfrepo.CloneAccount(account)
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != characterID {
			return errCurrentCeraPackageOwnerUnavailable
		}
		if err := dnfrepo.BindInventoryContainerState(ctx, repositories.Settings, &inventory); err != nil {
			return err
		}
		inventory = dnfrepo.CloneInventory(inventory)
		if inventory.Slots == nil {
			return errCurrentCeraPackageSourceMissing
		}
		sourceKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, request.SourceSlot)
		sourceStack, found := inventory.Slots[sourceKey]
		if !found || sourceStack.ItemID != int64(definition.Source.ItemID) || sourceStack.Count <= 0 {
			return errCurrentCeraPackageSourceMissing
		}
		if !definition.Source.ExpirationDate.IsZero() {
			sourceStack, _ = applyCurrentPVFItemExpirationAt(sourceStack, definition.Source, now)
		}
		if err := validateCurrentCeraPackageSourceExpiration(sourceStack, definition.Source, now); err != nil {
			return err
		}

		changed := map[byte]struct{}{dnfrepo.MainInventoryListType: {}}
		rewardSlots := make(map[string]struct{})
		overflow := make([]dnfrepo.MailAttachment, 0)
		if sourceStack.Count == 1 {
			delete(inventory.Slots, sourceKey)
		} else {
			sourceStack.Count--
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, request.SourceSlot, sourceStack)
			sourceStack.RawEntry = append([]byte(nil), entry.data[:]...)
			inventory.Slots[sourceKey] = sourceStack
		}

		var rewardUnits uint64
		for _, reward := range definition.Rewards {
			rewardUnits += uint64(reward.Count)
			grantDefinition, expirationErr := currentPVFItemDefinitionForNestedRewardGrantAt(reward.Definition, sourceStack, now)
			if expirationErr != nil {
				return fmt.Errorf("%w: reward item=%d expiration: %v", errCurrentCeraPackagePVFInvalid, reward.Definition.ItemID, expirationErr)
			}
			if contract, ok := contracts[int64(reward.Definition.ItemID)]; ok {
				activation, err := premium.ActivateContractGrant(&account, contract, int64(reward.Count), now)
				if err != nil {
					return err
				}
				result.ContractActivations = append(result.ContractActivations, alignedcmd.PremiumContractActivation{ItemID: activation.ItemID, PremiumType: activation.PremiumType, Units: activation.Units, ExpireAt: activation.ExpireAt})
				continue
			}
			if reward.Avatar {
				option, selected := options[reward.Definition.ItemID]
				if !selected {
					return fmt.Errorf("%w: avatar item=%d", errCurrentCeraPackageChoiceInvalid, reward.Definition.ItemID)
				}
				for count := uint32(0); count < reward.Count; count++ {
					slot, grantErr := grantCurrentCeraShopAvatar(
						&inventory,
						catalog.source,
						grantDefinition,
						currentCeraShopProduct{ItemID: grantDefinition.ItemID, Count: 1, Section: "avatar"},
						option,
					)
					if grantErr != nil {
						return grantErr
					}
					key := currentCeraShopInventorySlotKey(1, slot)
					rewardSlots[key] = struct{}{}
				}
				changed[1] = struct{}{}
				continue
			}
			if isCurrentCeraShopCreatureItem(grantDefinition) {
				for count := uint32(0); count < reward.Count; count++ {
					slot, grantErr := grantCurrentCeraShopPet(&inventory, grantDefinition)
					if grantErr != nil {
						return grantErr
					}
					rewardSlots[currentCeraShopInventorySlotKey(currentPetInventoryListType, slot)] = struct{}{}
				}
				changed[currentPetInventoryListType] = struct{}{}
				continue
			}
			if isCurrentCeraShopPetConsumable(grantDefinition) {
				slots, grantErr := grantCurrentCeraShopPetConsumable(&inventory, grantDefinition, reward.Count)
				if grantErr != nil {
					return grantErr
				}
				for _, slot := range slots {
					rewardSlots[currentCeraShopInventorySlotKey(currentPetInventoryListType, slot)] = struct{}{}
				}
				changed[currentPetInventoryListType] = struct{}{}
				continue
			}
			// Product placement can merge part of a stack before discovering a
			// full category. Probe a clone first, so the fallback either grants
			// the whole reward directly or sends the whole row to mail, never a
			// duplicated or half-consumed remainder.
			candidate := dnfrepo.CloneInventory(inventory)
			slots, grantErr := grantCurrentCeraShopProduct(&candidate, grantDefinition, reward.Count)
			if grantErr != nil {
				if !errors.Is(grantErr, errDungeonPickupInventoryFull) {
					return grantErr
				}
				attachments, attachmentErr := currentCeraPackageOverflowAttachments(
					grantDefinition,
					reward.Count,
					definition.Source.ItemID,
				)
				if attachmentErr != nil {
					return attachmentErr
				}
				overflow = append(overflow, attachments...)
				continue
			}
			inventory = candidate
			for _, slot := range slots {
				rewardSlots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, int16(slot))] = struct{}{}
			}
		}

		for key := range rewardSlots {
			stack, found := inventory.Slots[key]
			if !found || stack.ItemID <= 0 {
				return errCurrentCeraPackagePVFInvalid
			}
			if stack.Extra == nil {
				stack.Extra = make(map[string]string, 5)
			}
			stack.Extra["source"] = "cera_package"
			stack.Extra["last_grant_source"] = "cera_package"
			stack.Extra["cera_package_source_item"] = strconv.FormatUint(uint64(definition.Source.ItemID), 10)
			listType, slot, parsed := parseSceneInventorySlotKey(key)
			if !parsed {
				return errCurrentCeraPackagePVFInvalid
			}
			entry := currentItemListEntryFromStack(listType, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			inventory.Slots[key] = stack
		}
		// delta 档的发放通知集合 = 被扣的源格 + 每个奖励落点（键已在上面逐个校验过存在）。
		// 走邮件的溢出奖励不占格，因此不在集合里。
		grantSlots := make(map[string]struct{}, len(rewardSlots)+1)
		grantSlots[sourceKey] = struct{}{}
		for key := range rewardSlots {
			grantSlots[key] = struct{}{}
		}
		grantSlotRefreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(grantSlots))
		for key := range grantSlots {
			listType, slot, parsed := parseSceneInventorySlotKey(key)
			if !parsed {
				return errCurrentCeraPackagePVFInvalid
			}
			grantSlotRefreshes = append(grantSlotRefreshes, alignedcmd.ItemSlotRefresh{
				ListType:  listType,
				SlotIndex: slot,
			})
		}
		inventory.UpdatedAt = now
		if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		if len(result.ContractActivations) > 0 {
			account.UpdatedAt = now
			if err := accounts.Save(ctx, account); err != nil {
				return err
			}
		}
		if len(overflow) > 0 {
			mailID, mailErr := dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{
				RecipientCharacterID: characterID,
				Title:                "背包已满：礼包奖励",
				Body:                 "背包空间不足，礼包中未能放入背包的奖励已通过邮件发送。请清理对应道具分页后领取。",
				Source:               "cera_package_main_inventory_full",
				Attachments:          overflow,
				CreatedAt:            now,
			})
			if mailErr != nil {
				return mailErr
			}
			result.OverflowMailID = mailID
			result.OverflowItems = len(overflow)
		}

		listTypes := make([]byte, 0, len(changed))
		for listType := range changed {
			listTypes = append(listTypes, listType)
		}
		sort.Slice(listTypes, func(left, right int) bool { return listTypes[left] < listTypes[right] })
		result = currentCeraPackageCommitResult{
			ContractActivations: result.ContractActivations,
			ChangedListTypes:    listTypes,
			RewardRows:          len(definition.Rewards),
			RewardUnits:         rewardUnits,
			OverflowMailID:      result.OverflowMailID,
			OverflowItems:       result.OverflowItems,
			GrantSlots:          grantSlotRefreshes,
		}
		return nil
	})
	if err != nil {
		return currentCeraPackageCommitResult{}, err
	}
	return result, nil
}

// currentCeraPackageOverflowAttachments makes mail-owned instances for a
// package row that cannot fit its PVF inventory category. The item instances
// retain the same expiration and PVF metadata used by the direct placement
// path, so claiming them later is equivalent to receiving them immediately.
func currentCeraPackageOverflowAttachments(
	definition dungeonDropItemDefinition,
	count uint32,
	sourceItemID uint32,
) ([]dnfrepo.MailAttachment, error) {
	if definition.ItemID == 0 || count == 0 || sourceItemID == 0 {
		return nil, errCurrentCeraPackagePVFInvalid
	}
	attachments := make([]dnfrepo.MailAttachment, 0, 1)
	remaining := count
	for remaining > 0 {
		chunk := remaining
		switch definition.Kind {
		case dungeonDropItemEquipment:
			chunk = 1
		case dungeonDropItemStackable:
			if definition.StackLimit > 0 && int64(chunk) > definition.StackLimit {
				chunk = uint32(definition.StackLimit)
			}
		default:
			return nil, errCurrentCeraShopProductUnavailable
		}
		// The mailbox wire builder replaces the location field. Build with a
		// valid PVF category slot solely to obtain the exact item-instance
		// payload and expiration fields.
		stack, err := buildCurrentDungeonPickupStack(definition.SlotStart, definition, chunk)
		if err != nil {
			return nil, err
		}
		if stack.Extra == nil {
			stack.Extra = make(map[string]string, 4)
		}
		stack.Extra["source"] = "cera_package"
		stack.Extra["last_grant_source"] = "cera_package"
		stack.Extra["cera_package_source_item"] = strconv.FormatUint(uint64(sourceItemID), 10)
		stack.Extra["cera_package_overflow_mail"] = "true"
		attachments = append(attachments, dnfrepo.MailAttachment{
			ItemID:   stack.ItemID,
			Count:    stack.Count,
			Bind:     stack.Bind,
			ExpireAt: stack.ExpireAt,
			RawEntry: append([]byte(nil), stack.RawEntry...),
			Extra:    stack.Extra,
		})
		remaining -= chunk
	}
	return attachments, nil
}
