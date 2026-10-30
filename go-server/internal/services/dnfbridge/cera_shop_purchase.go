package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfcargo "longheng.io/server/internal/modules/dnf/cargo"
	dnfcerashop "longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

const (
	currentCeraShopRequestHeaderSize = 4
	currentCeraShopRequestItemStride = 15
	currentCeraShopRequestMaxItems   = 64
	currentCeraShopUpdateMsgID       = 0x0035

	currentCeraShopPaymentModeCera         byte = 0
	currentCeraShopPaymentModeAvatarCoupon byte = 1

	// Current NoPack sub_232A9F0 groups list 7 as pet bodies 0..139,
	// pet equipment 140..188, and pet consumables 189..238 (inclusive).
	currentCeraShopPetConsumableSlotStart int16 = 189
	currentCeraShopPetConsumableSlotEnd   int16 = 238
)

var (
	errCurrentCeraShopRequestMalformed   = errors.New("dnf cera shop purchase request is malformed")
	errCurrentCeraShopPaymentUnsupported = errors.New("dnf cera shop payment mode is unsupported")
	errCurrentCeraShopCatalogUnavailable = errors.New("dnf cera shop catalog is unavailable")
	errCurrentCeraShopProductUnavailable = errors.New("dnf cera shop product is unavailable")
	errCurrentCeraShopOwnerUnavailable   = errors.New("dnf cera shop selected character is unavailable")
	errCurrentCeraShopCeraInsufficient   = errors.New("dnf cera shop cera balance is insufficient")
)

// currentCeraShopPurchaseRequest follows the current client's sendBuyPacket
// body.  The first cart row starts at byte 4, has a 15-byte stride, and owns
// its commodity number at +3.  Byte 4 is the client payment selector while
// byte 5 is the row's avatar-attribute selector; the two deliberately remain
// distinct even though the first row begins at byte 4.
type currentCeraShopPurchaseRequest struct {
	FestivalRequestID string
	FestivalDigest    string
	FestivalSerial    uint64
	FestivalOwner     boundGameSessionCharacter
	FestivalAccount   string
	PaymentMode       byte
	Items             []currentCeraShopPurchaseRequestItem
}

type currentCeraShopPurchaseRequestItem struct {
	CommodityID    uint32
	AttributeValue byte
}

type currentCeraShopCommitResult struct {
	CharacterRenamePending   bool
	Products                 []currentCeraShopProduct
	Updates                  []currentCeraShopItemUpdate
	MainInventoryExpansion   uint16
	PersonalCargoSlotCount   uint16
	AvatarInventoryExpansion uint16
	AccountCargoSelectionKey uint16
	PetInventoryChanged      bool
	// GuildMedalInventoryChanged 表示本次购买落进了 wire list 38（公会勋章/
	// 守护珠）。该容器有自己的权威快照构造器（buildCurrentItemListBodyForSession），
	// 不能用 list 0/1 的 op14 增量行刷。
	GuildMedalInventoryChanged bool
	CeraAfter                  int64
	WalletAfter                dnfcerashop.Wallet
	PremiumActivations         []currentCeraShopPremiumActivation
	NameTagActivation          *currentNameTagCardActivation
	SkillTreeExpanded          bool
}

// currentCeraShopPremiumActivation is one premium contract activated at
// checkout: the premium type (580+slot for devil perks) and its remaining
// seconds, used for the class0/op66 activation notification.
type currentCeraShopPremiumActivation struct {
	PremiumType      int64
	RemainingSeconds int64
}

// currentCeraShopItemUpdate is an actual changed inventory container.  A
// Cera package belongs in list 0, while a direct avatar product belongs in
// list 1.  Keep the container with the changed rows so a list-0 item update
// can never make a client interpret an avatar entry as a normal bag row.
type currentCeraShopItemUpdate struct {
	ListType byte
	Entries  []currentItemListEntry
}

type currentCeraShopResolvedPurchase struct {
	CharacterRenameRules        *currentRenameCardRules
	Product                     currentCeraShopProduct
	AttributeValue              byte
	MainInventoryUpgradeStage   uint16
	PersonalCargoUpgradeTarget  uint16
	PersonalCargoUpgradeFrom    uint16
	AvatarInventoryUpgradeStage uint16
	AccountCargoUpgrade         bool
	AccountCargoUpgradeFrom     int64
	AccountCargoUpgradeTarget   int64
	AvatarCouponItemID          int64
	// PremiumType > 0 means the purchase is consumed at checkout and activates
	// this account-level premium contract instead of entering any container
	// (86JP ConsumedOnPurchase). PremiumPackage activates every 魔王契约 devil
	// slot for the same duration ([charac premium package]).
	PremiumType            int64
	PremiumDurationSeconds int64
	PremiumPackage         bool
	NameTagDurationSeconds int64
	SkillTreeExpansion     bool
}

type currentCeraShopChangedSlot struct {
	ListType byte
	Slot     int16
	Product  currentCeraShopProduct
	// Bonus marks a slot filled by a one-plus-one IPG gift
	// (etc/chn_cerashop_multibonus.etc) rather than by the purchased product
	// itself; Product keeps the parent commodity for provenance while
	// Product.ItemID is the granted bonus item.
	Bonus bool
}

// currentCeraShopCartSummary renders the client's op64 cart as `commodity:属性`
// pairs. An avatar product's dialog ("选择职业→自动选择属性") is client-owned; the
// server only receives the commodity id and one attribute byte per row, so
// logging the exact cart is what lets one live attempt prove whether the
// delivered 时装 differed from the client's choice or never matched it.
func currentCeraShopCartSummary(items []currentCeraShopPurchaseRequestItem) string {
	if len(items) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, strconv.FormatUint(uint64(item.CommodityID), 10)+":"+strconv.FormatUint(uint64(item.AttributeValue), 10))
	}
	return strconv.Itoa(len(parts)) + "[" + strings.Join(parts, " ") + "]"
}

func parseCurrentCeraShopPurchaseRequest(body []byte) (currentCeraShopPurchaseRequest, error) {
	if len(body) < currentCeraShopRequestHeaderSize+currentCeraShopRequestItemStride {
		return currentCeraShopPurchaseRequest{}, errCurrentCeraShopRequestMalformed
	}
	count := int(body[2])
	if count == 0 {
		count = 1
	}
	if count < 1 || count > currentCeraShopRequestMaxItems ||
		len(body) < currentCeraShopRequestHeaderSize+count*currentCeraShopRequestItemStride {
		return currentCeraShopPurchaseRequest{}, errCurrentCeraShopRequestMalformed
	}
	request := currentCeraShopPurchaseRequest{
		PaymentMode: body[4],
		Items:       make([]currentCeraShopPurchaseRequestItem, 0, count),
	}
	for index := 0; index < count; index++ {
		itemOffset := currentCeraShopRequestHeaderSize + index*currentCeraShopRequestItemStride
		commodityID := binary.LittleEndian.Uint32(body[itemOffset+3 : itemOffset+7])
		if commodityID == 0 {
			return currentCeraShopPurchaseRequest{}, errCurrentCeraShopRequestMalformed
		}
		request.Items = append(request.Items, currentCeraShopPurchaseRequestItem{
			CommodityID:    commodityID,
			AttributeValue: body[itemOffset+1],
		})
	}
	return request, nil
}

func (s *Service) handleCurrentCeraShopPurchase(session *gameSession, body []byte, sequences ...*uint64) error {
	request, err := parseCurrentCeraShopPurchaseRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-cera-shop-purchase-rejected", "body_len", len(body), "body_hex", currentCeraSelectionBodyHex(body), "reason", err)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}
	for _, item := range request.Items {
		if isCurrentGuildHongbaoCommodity(item.CommodityID) {
			var sequence *uint64
			if len(sequences) > 0 {
				sequence = sequences[0]
			}
			return s.handleCurrentGuildHongbaoPurchase(session, request, body, sequence)
		}
	}
	if request.PaymentMode != currentCeraShopPaymentModeCera && request.PaymentMode != currentCeraShopPaymentModeAvatarCoupon {
		s.logGameEvent(session, "game-cera-shop-purchase-rejected",
			"body_len", len(body), "item_count", len(request.Items), "payment_mode", request.PaymentMode,
			"cart", currentCeraShopCartSummary(request.Items), "body_hex", currentCeraSelectionBodyHex(body),
			"reason", errCurrentCeraShopPaymentUnsupported)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}
	catalog, err := s.currentCeraShopCatalog()
	if err != nil {
		s.logGameEvent(session, "game-cera-shop-purchase-rejected", "body_len", len(body), "reason", err)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}
	if err := s.preparePrivateFestivalPurchase(session, catalog, &request, body, sequences); err != nil {
		s.logGameEvent(session, "private-festival-purchase-rejected", "error", err)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	var renameReservation *currentCharacterAssetReservation
	if s.options.currentClientVersion == "official-24.9" && !characterRenameLegacy() && session != nil {
		for _, requested := range request.Items {
			product, found := catalog.Product(requested.CommodityID)
			if !found {
				continue
			}
			definition, definitionErr := catalog.items.ResolveItem(product.ItemID)
			if definitionErr != nil || !strings.EqualFold(strings.ReplaceAll(definition.PVFPath, "\\", "/"), "stackable/cash/chn_character_rename.stk") {
				continue
			}
			s.mu.Lock()
			identity := boundGameSessionCharacter{session: session, character: session.selectedCharacterID, generation: session.characterGeneration}
			account := session.accountID
			s.mu.Unlock()
			if !session.loginPacketSeen || account == "" {
				return s.sendCurrentCeraShopPurchaseFailure(session)
			}
			renameReservation, err = s.reserveCurrentCharacterAssets(ctx, identity, account)
			if err != nil {
				return s.sendCurrentCeraShopPurchaseFailure(session)
			}
			defer s.releaseCurrentCharacterAssets(renameReservation)
			break
		}
	}
	result, err := s.commitCurrentCeraShopPurchase(ctx, session, catalog, request)
	if err != nil {
		s.logGameEvent(session, "game-cera-shop-purchase-rejected",
			"body_len", len(body), "item_count", len(request.Items),
			"cart", currentCeraShopCartSummary(request.Items), "body_hex", currentCeraSelectionBodyHex(body),
			"reason", err)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}

	if result.NameTagActivation != nil {
		activation := result.NameTagActivation
		s.logGameEvent(session, "game-name-tag-card-applied",
			"char_id", session.selectedCharacterID,
			"action", activation.Action,
			"item_id", activation.ItemID,
			"prev_item_id", activation.PreviousItemID,
			"expire_time", activation.ExpireAt,
			"prev_expire_time", activation.PreviousExpire)
	}

	if err := s.sendCurrentCeraShopCargoRefresh(ctx, session, result); err != nil {
		return err
	}
	for index, product := range result.Products {
		commodityID := product.CommodityID
		if (result.PersonalCargoSlotCount != 0 || result.AccountCargoSelectionKey != 0) && index < len(request.Items) {
			// 顺延档位按新商品计价；ACK仍对应客户端这次请求的商品，结束原购买状态。
			commodityID = request.Items[index].CommodityID
		}
		if err := s.sendGameUpperRawClass(
			session,
			uint16(dnfenum.CmdPacketBuyCerashopItem),
			buildCurrentCeraShopPurchaseSuccessBodyWithCount(commodityID, product.Count, catalog.bonusByCommodity[product.CommodityID]...),
			dnfproto.DefaultChannelClassification,
		); err != nil {
			return err
		}
	}
	// The current client commits the Cera-shop result before it applies the
	// typed actor refresh. Sending mode0 before the class1/op64 success ACK can
	// race the shop UI owner and leave endpoint 30 empty until the next login.
	if result.NameTagActivation != nil {
		if err := s.currentSendNameTagRefresh(ctx, session); err != nil {
			s.logGameEvent(session, "game-name-tag-card-refresh-failed", "err", err)
		}
	}
	if result.SkillTreeExpanded {
		if err := s.currentSendSkillTreeExpansionRefresh(ctx, session); err != nil {
			s.logWarn("技能类型扩展已保存，客户端刷新待下次登录收敛", "character_id", session.selectedCharacterID, "error", err)
		}
	}
	notifiedPremiumActivations := 0
	devilPremiumActivated := false
	crystalPremiumActivated := false
	// Current EXE class0/op66 (sub_1D61460, sub-op 2) reads a one-byte premium
	// type. Ordinary PVF premium types use that route. Devil service slots are
	// internal account types 580..587; sending them here truncates the type to
	// 68..75 and displays unrelated Korean tarot-card effects.
	for _, activation := range result.PremiumActivations {
		if premium.IsDevilSlotType(activation.PremiumType) {
			devilPremiumActivated = true
		}
		if activation.PremiumType == premium.TypeCrystal {
			crystalPremiumActivated = true
		}
		if !premium.CanNotifyActivation(activation.PremiumType) {
			continue
		}
		if err := s.sendGameUpperRawClass(
			session,
			currentPremiumActivatedMsgID,
			buildCurrentPremiumActivatedBody(activation.PremiumType, activation.RemainingSeconds),
			0,
		); err != nil {
			return err
		}
		notifiedPremiumActivations++
	}
	if crystalPremiumActivated {
		s.requestCurrentCrystalContractTimerRefresh(session)
		// Current sub_1D61460 records premium type 97, but the crystal panel is
		// rebuilt by the raw class0/op898 handler sub_1E7FAD0. Without this
		// immediate follow-up a purchase made after scene bootstrap remains
		// locally disabled and the client never emits its op535 selection.
		if err := s.sendCurrentCrystalContractState(session, "cera_shop_purchase_after_crystal_contract_activation"); err != nil {
			return err
		}
	}
	for _, update := range result.Updates {
		if len(update.Entries) == 0 {
			continue
		}
		body := buildCurrentItemUpdateBody(update.ListType, update.Entries)
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
			return err
		}
	}
	if result.MainInventoryExpansion != 0 {
		mainBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, currentExeInventoryMainListType)
		if !ok {
			return errCurrentCeraShopOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), mainBody); err != nil {
			return err
		}
	}
	if result.AvatarInventoryExpansion != 0 {
		// 装扮栏(list 1)容量变了：客户端只在 op13 头部读 avatar_list_param16，
		// 所以必须整份重发装扮列表，否则玩家要重登才看到新格子。
		avatarBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, currentExeInventoryAvatarListType)
		if !ok {
			return errCurrentCeraShopOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), avatarBody); err != nil {
			return err
		}
	}
	if result.PetInventoryChanged {
		petBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, currentPetInventoryListType)
		if !ok {
			return errCurrentCeraShopOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), petBody); err != nil {
			return err
		}
	}
	if result.GuildMedalInventoryChanged {
		guildMedalBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, currentGuildMedalInventoryListType)
		if !ok {
			return errCurrentCeraShopOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), guildMedalBody); err != nil {
			return err
		}
	}
	if devilPremiumActivated {
		if err := s.sendCurrentPremiumServiceState(session, "cera_shop_purchase_after_devil_contract_activation"); err != nil {
			return err
		}
	}
	if err := s.sendGameUpperRawClass(session, currentCeraShopUpdateMsgID, buildCurrentCeraShopWalletBody(result.CeraAfter, result.WalletAfter.TokenCera, result.WalletAfter.HappyTokenCera), 0); err != nil {
		return err
	}
	if request.FestivalRequestID != "" {
		if _, _, err := s.settlePrivateFestival(ctx, session); err != nil {
			s.logGameEvent(session, "private-festival-mail-pending", "error", err)
		} else if err := s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "private_festival"); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-cera-shop-purchase-committed",
		"item_count", len(result.Products), "cart", currentCeraShopCartSummary(request.Items), "payment_mode", request.PaymentMode, "container_update_count", len(result.Updates), "main_inventory_expansion", result.MainInventoryExpansion, "personal_cargo_slot_count", result.PersonalCargoSlotCount, "avatar_inventory_expansion", result.AvatarInventoryExpansion, "account_cargo_selection_key", result.AccountCargoSelectionKey, "cera_after", result.CeraAfter, "premium_activation_count", len(result.PremiumActivations),
		"premium_notification_count", notifiedPremiumActivations,
		"crystal_state_refreshed", crystalPremiumActivated,
		"ack_body", "current_exe_op64_sub_CD9490", "item_update_body", "current_exe_op14_raw77", "cera_body", "current_exe_op53_u8_u32_u32_u32", "premium_body", "current_exe_class0_op66_u8_types_only_internal_devil_slots_suppressed")
	if result.CharacterRenamePending {
		if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketUseRenameCard), nil); err != nil {
			return err
		}
		s.releaseCurrentCharacterAssets(renameReservation)
		return s.handleUpperReturnSelectCharacter(session, nil)
	}
	return nil
}

func (s *Service) commitCurrentCeraShopPurchase(
	ctx context.Context,
	session *gameSession,
	catalog *pvfCeraShopCatalog,
	request currentCeraShopPurchaseRequest,
) (currentCeraShopCommitResult, error) {
	if s == nil {
		return currentCeraShopCommitResult{}, errCurrentCeraShopOwnerUnavailable
	}
	// Settings records are routed by their scope while wallet/inventory rows
	// are routed by character id.  Those keys can live on different database
	// shards, so serialize this checkout owner and compensate the saved header
	// if the character settlement rejects; do not pretend the two shards share
	// one SQL transaction.
	s.ceraShopPurchaseMu.Lock()
	defer s.ceraShopPurchaseMu.Unlock()
	if session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.items == nil || len(request.Items) == 0 {
		return currentCeraShopCommitResult{}, errCurrentCeraShopOwnerUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil || repositories.RentalAssets == nil {
		return currentCeraShopCommitResult{}, errCurrentCeraShopOwnerUnavailable
	}
	purchases := make([]currentCeraShopResolvedPurchase, 0, len(request.Items))
	var totalCost uint64
	switch request.PaymentMode {
	case currentCeraShopPaymentModeCera:
	case currentCeraShopPaymentModeAvatarCoupon:
		// 装扮兑换券结账**必须支持购物车里多件装扮**（操作者报障"商城购买装扮
		// 放在购物车无法批量购买，提示为物品栏空间不足"）：每一行都在下面按自己的
		// (grade, duration_index) 解析兑换券并在结账事务里各消耗一张，券不够时整单
		// 原子失败。旧代码在这里硬性要求 len(Items)==1，于是整批购买只会拿到
		// op64 的失败体（错误码 4）。
	default:
		return currentCeraShopCommitResult{}, errCurrentCeraShopPaymentUnsupported
	}
	// Any purchased item can be a premiumlist contract item (86JP
	// IsContractItem). The premium catalog is loaded lazily; both catalogs
	// come from the same runtime archive in production. The two contract
	// sections are meaningless without it and fail closed, while ordinary
	// sections tolerate a load failure (logged) and keep their normal
	// delivery path.
	var premiumCatalog *currentPremiumCatalog
	premiumCatalogFailed := false
	loadPremiumCatalog := func() error {
		if premiumCatalog != nil {
			return nil
		}
		loaded, err := s.currentPremiumCatalog()
		if err != nil {
			return err
		}
		premiumCatalog = loaded
		return nil
	}
	for _, requestItem := range request.Items {
		product, found := catalog.Product(requestItem.CommodityID)
		if !found {
			return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d", errCurrentCeraShopProductUnavailable, requestItem.CommodityID)
		}
		product, err := catalog.resolvePurchase(product)
		if err != nil {
			return currentCeraShopCommitResult{}, err
		}
		resolved := currentCeraShopResolvedPurchase{Product: product, AttributeValue: requestItem.AttributeValue}
		if request.PaymentMode == currentCeraShopPaymentModeAvatarCoupon {
			couponItemID, err := catalog.resolveAvatarCouponItemID(product)
			if err != nil {
				return currentCeraShopCommitResult{}, err
			}
			resolved.AvatarCouponItemID = couponItemID
		} else {
			if uint64(product.CeraPrice) > math.MaxUint64-totalCost {
				return currentCeraShopCommitResult{}, errCurrentCeraShopProductUnavailable
			}
			totalCost += uint64(product.CeraPrice)
		}
		switch {
		case product.Section == "selectable character premium":
			if err := loadPremiumCatalog(); err != nil {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: premium catalog: %v", errCurrentCeraShopProductUnavailable, err)
			}
			slot, ok := premiumCatalog.devilSlots[product.CommodityID]
			if !ok {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d is not a purchasable devil contract slot", errCurrentCeraShopProductUnavailable, product.CommodityID)
			}
			resolved.PremiumType = premium.DevilSlotType(slot.Slot)
			resolved.PremiumDurationSeconds = slot.Days * 86400
			purchases = append(purchases, resolved)
			continue
		case product.Section == "charac premium package":
			if product.PremiumPackageDays == 0 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d has no contract day count", errCurrentCeraShopProductUnavailable, product.CommodityID)
			}
			resolved.PremiumPackage = true
			resolved.PremiumDurationSeconds = int64(product.PremiumPackageDays) * 86400
			purchases = append(purchases, resolved)
			continue
		}
		if err := loadPremiumCatalog(); err != nil {
			if !premiumCatalogFailed {
				premiumCatalogFailed = true
				s.logPacketEvent("game-cera-shop-premium-catalog-unavailable", "error", err)
			}
		} else if contract, ok := premiumCatalog.contractsByItem[int64(product.ItemID)]; ok && int64(product.ItemID) != premium.BlackDiamondThirtyDayItemID {
			resolved.PremiumType = contract.PremiumType
			resolved.PremiumDurationSeconds = contract.DurationSeconds
			purchases = append(purchases, resolved)
			continue
		}
		if product.ItemID == currentSkillTreeExpansionItemID {
			if product.Count != 1 || len(request.Items) != 1 || request.PaymentMode != currentCeraShopPaymentModeCera || len(catalog.bonusByCommodity[product.CommodityID]) != 0 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: 技能类型扩展券需要单独购买一件", errCurrentCeraShopProductUnavailable)
			}
			definition, err := catalog.items.ResolveItem(product.ItemID)
			if err != nil || !currentIsSkillTreeExpansionItem(definition) {
				return currentCeraShopCommitResult{}, errCurrentCeraShopProductUnavailable
			}
			resolved.SkillTreeExpansion = true
			purchases = append(purchases, resolved)
			continue
		}
		definition, definitionErr := catalog.items.ResolveItem(product.ItemID)
		if definitionErr != nil {
			return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d item=%d: %v", errCurrentCeraShopProductUnavailable, product.CommodityID, product.ItemID, definitionErr)
		}
		if s.options.currentClientVersion == "official-24.9" && !characterRenameLegacy() && strings.EqualFold(strings.ReplaceAll(definition.PVFPath, "\\", "/"), "stackable/cash/chn_character_rename.stk") {
			if product.Count != 1 || len(request.Items) != 1 || request.PaymentMode != currentCeraShopPaymentModeCera || len(catalog.bonusByCommodity[product.CommodityID]) != 0 || session.townSceneReadyCharacterID != session.selectedCharacterID {
				return currentCeraShopCommitResult{}, errCurrentCeraShopProductUnavailable
			}
			if _, inRaid := s.runtimeRaidForMember(session.selectedCharacterID); inRaid {
				return currentCeraShopCommitResult{}, errCurrentCeraShopProductUnavailable
			}
			rules, err := currentRenameCardRulesFromPVF(catalog, product.ItemID)
			if err != nil {
				return currentCeraShopCommitResult{}, err
			}
			resolved.CharacterRenameRules = &rules
			purchases = append(purchases, resolved)
			continue
		}
		if durationSeconds, isNameTag := currentNameTagCardDuration(catalog.source, definition); isNameTag {
			resolved.NameTagDurationSeconds = durationSeconds
			purchases = append(purchases, resolved)
			continue
		}
		if stage, isMainInventoryUpgrade := currentCeraShopMainInventoryUpgradeStage(definition); isMainInventoryUpgrade {
			if stage == 0 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d item=%d has an unsupported main inventory upgrade tier", errCurrentCeraShopProductUnavailable, product.CommodityID, product.ItemID)
			}
			if product.Count != 1 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d main inventory upgrades require count=1", errCurrentCeraShopProductUnavailable, product.CommodityID)
			}
			resolved.MainInventoryUpgradeStage = stage
		}
		if target, isPersonalCargoUpgrade := catalog.personalCargoUpgradeTarget(definition); isPersonalCargoUpgrade {
			if target == 0 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d item=%d has an unsupported personal cargo upgrade tier", errCurrentCeraShopProductUnavailable, product.CommodityID, product.ItemID)
			}
			if product.Count != 1 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d personal cargo upgrades require count=1", errCurrentCeraShopProductUnavailable, product.CommodityID)
			}
			resolved.PersonalCargoUpgradeTarget = target
		}
		if catalog.isAccountCargoUpgradeTool(definition) {
			if product.Count != 1 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d account cargo upgrades require count=1", errCurrentCeraShopProductUnavailable, product.CommodityID)
			}
			resolved.AccountCargoUpgrade = true
		}
		if stage, isAvatarInventoryUpgrade := currentCeraShopAvatarInventoryUpgradeStage(definition); isAvatarInventoryUpgrade {
			if stage == 0 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d item=%d has an unsupported avatar inventory upgrade tier", errCurrentCeraShopProductUnavailable, product.CommodityID, product.ItemID)
			}
			if product.Count != 1 {
				return currentCeraShopCommitResult{}, fmt.Errorf("%w: commodity=%d avatar inventory upgrades require count=1", errCurrentCeraShopProductUnavailable, product.CommodityID)
			}
			resolved.AvatarInventoryUpgradeStage = stage
		}
		purchases = append(purchases, resolved)
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if err := resolveCurrentCeraShopCargoPurchases(ctx, repositories, catalog, accountID, characterID, purchases); err != nil {
		return currentCeraShopCommitResult{}, err
	}
	// 旧窗口重复点击时可能顺延到价格更高的档位，必须重新按实际商品计价。
	totalCost = 0
	if request.PaymentMode == currentCeraShopPaymentModeCera {
		for index, purchase := range purchases {
			if uint64(purchase.Product.CeraPrice) > math.MaxUint64-totalCost {
				return currentCeraShopCommitResult{}, errCurrentCeraShopProductUnavailable
			}
			totalCost += uint64(purchase.Product.CeraPrice)
			if purchase.Product.CommodityID != request.Items[index].CommodityID {
				s.logGameEvent(session, "仓库升级购买按当前档位计价", "requested_commodity", request.Items[index].CommodityID,
					"charged_commodity", purchase.Product.CommodityID, "cera_cost", purchase.Product.CeraPrice)
			}
		}
	}
	if totalCost > math.MaxInt64 {
		return currentCeraShopCommitResult{}, errCurrentCeraShopProductUnavailable
	}

	checkoutOwner, err := dnfcerashop.NewOwner(repositories)
	if err != nil {
		return currentCeraShopCommitResult{}, errCurrentCeraShopOwnerUnavailable
	}
	var ceraOnlyCost, tokenOnlyCost int64
	for _, purchase := range purchases {
		definition, e := catalog.items.ResolveItem(purchase.Product.ItemID)
		if e != nil {
			// 契约目录可提供服务型商品，无法读取普通道具限制时保留原来的纯点券扣费。
			ceraOnlyCost += int64(purchase.Product.CeraPrice)
			continue
		}
		if definition.CeraOnly {
			ceraOnlyCost += int64(purchase.Product.CeraPrice)
		} else if definition.TokenCeraOnly {
			tokenOnlyCost += int64(purchase.Product.CeraPrice)
		}
	}
	accountCargoUpgrades := 0
	for _, purchase := range purchases {
		if purchase.AccountCargoUpgrade {
			accountCargoUpgrades++
		}
	}
	if accountCargoUpgrades != 0 {
		if accountCargoUpgrades != 1 || len(purchases) != 1 {
			return currentCeraShopCommitResult{}, fmt.Errorf("%w: account cargo upgrade must be purchased alone", errCurrentCeraShopProductUnavailable)
		}
		return commitCurrentCeraShopAccountCargoUpgrade(ctx, checkoutOwner, accountID, characterID, purchases[0], int64(totalCost), s.gameplayNow(), ceraOnlyCost, tokenOnlyCost)
	}
	includeSkills := len(purchases) == 1 && purchases[0].SkillTreeExpansion
	var skillOptions skillcmd.OwnerOptions
	if includeSkills {
		skillOptions, err = s.currentSkillTreeExpansionRules(ctx, repositories, session.selectedCharacterID)
		if err != nil {
			return currentCeraShopCommitResult{}, err
		}
	}
	var result currentCeraShopCommitResult
	checkoutNow := s.gameplayNow().UTC()
	checkoutResult, err := checkoutOwner.Checkout(ctx, dnfcerashop.CheckoutCommand{
		AccountID:     accountID,
		CharacterID:   characterID,
		SettingsScope: dnfrepo.CharacterContainerStateScope(characterID),
		Cost:          int64(totalCost),
		PayPolicy:     dnfcerashop.PayPolicyWaterfall, CeraOnlyCost: ceraOnlyCost, TokenOnlyCost: tokenOnlyCost,
		UpdatedAt:     checkoutNow,
		IncludeSkills: includeSkills,
		Preflight: func(account *dnfrepo.AccountRecord) error {
			return s.privateFestivalPreflight(session, account, request)
		},
		Project: func(assets *dnfcerashop.CheckoutAssets) (dnfcerashop.CheckoutChanges, error) {
			if assets == nil || assets.Account == nil || assets.Character == nil || assets.Inventory == nil ||
				assets.Equipment == nil || assets.Settings == nil {
				return dnfcerashop.CheckoutChanges{}, errCurrentCeraShopOwnerUnavailable
			}
			containerStateAfter, containerUpgrades, hasContainerUpgrade, err :=
				currentCeraShopPrepareContainerUpgrades(
					assets.Settings,
					assets.SettingsFound,
					characterID,
					purchases,
					checkoutNow,
				)
			if err != nil {
				return dnfcerashop.CheckoutChanges{}, err
			}
			changes := dnfcerashop.CheckoutChanges{Settings: hasContainerUpgrade}
			if hasContainerUpgrade {
				*assets.Settings = containerStateAfter
			}
			account := assets.Account
			character := assets.Character
			inventory := assets.Inventory
			if assets.SettingsFound || hasContainerUpgrade {
				state, stateErr := dnfrepo.CharacterContainerStateFromSettings(*assets.Settings, characterID)
				if stateErr != nil {
					return dnfcerashop.CheckoutChanges{}, stateErr
				}
				inventory.ContainerState = &state
			}
			equipment := assets.Equipment
			changedSlots := make(map[string]currentCeraShopChangedSlot)
			entriesByList := make(map[byte][]currentItemListEntry)
			premiumActivations := make([]currentCeraShopPremiumActivation, 0, len(purchases))
			petInventoryChanged := false
			guildMedalInventoryChanged := false
			inventoryChanged := false
			var nameTagActivation *currentNameTagCardActivation
			premiumNow := checkoutNow
			for _, purchase := range purchases {
				product := purchase.Product
				if rules := purchase.CharacterRenameRules; rules != nil {
					if character.Level < int(rules.minimumLevel) || character.Stats[characterdata.RenameEntitlementStat] != 0 || character.Stats[characterdata.RenameNextAllowedUnixStat] > checkoutNow.Unix() || character.Location.DungeonID != 0 {
						return dnfcerashop.CheckoutChanges{}, characterdata.ErrRenameUnavailable
					}
					if character.Stats == nil {
						character.Stats = make(map[string]int64)
					}
					character.Stats[characterdata.RenameEntitlementStat] = 1
					character.Stats[characterdata.RenameCardSlotStat] = -1
					character.Stats[characterdata.RenameMinimumLevelStat] = rules.minimumLevel
					character.Stats[characterdata.RenameCooldownSecondsStat] = rules.cooldownSeconds
					character.Stats[characterdata.RenameReservationSecondsStat] = rules.reservationSeconds
					changes.Character = true
					continue
				}
				if purchase.SkillTreeExpansion {
					if assets.Skill == nil {
						return dnfcerashop.CheckoutChanges{}, errCurrentCeraShopOwnerUnavailable
					}
					expanded, err := skillcmd.ExpandSkillTree(*character, *assets.Skill, skillOptions)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					*assets.Skill = expanded
					if character.Stats == nil {
						character.Stats = make(map[string]int64)
					}
					character.Stats["skill_tree_index"] = 0
					character.Stats["skill_tree_expanded"] = 1
					changes.Character, changes.Skill = true, true
					continue
				}
				if purchase.AvatarCouponItemID != 0 {
					entry, err := consumeCurrentCeraShopAvatarCoupon(inventory, purchase.AvatarCouponItemID)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					entriesByList[dnfrepo.MainInventoryListType] = append(entriesByList[dnfrepo.MainInventoryListType], entry)
					inventoryChanged = true
				}
				if purchase.MainInventoryUpgradeStage != 0 || purchase.PersonalCargoUpgradeTarget != 0 ||
					purchase.AvatarInventoryUpgradeStage != 0 {
					// Container upgrades are consumed at checkout. They have no
					// inventory row: the real effect is the persisted op13 header
					// state prepared above.
					continue
				}
				if purchase.PremiumPackage {
					// [charac premium package]: consumed at checkout; every 魔王契约
					// devil slot renews for the package day count (86JP
					// ConsumedOnPurchase + UpsertPremiumExpire per slot).
					for devilSlot := int64(0); devilSlot < premium.DevilSlotCount; devilSlot++ {
						premiumType := premium.DevilSlotType(devilSlot)
						premium.Upsert(account, premiumType, purchase.PremiumDurationSeconds, 1, premiumNow)
						premiumActivations = append(premiumActivations, currentCeraShopPremiumActivation{
							PremiumType:      premiumType,
							RemainingSeconds: premium.ExpireAt(*account, premiumType) - premiumNow.Unix(),
						})
					}
					continue
				}
				if purchase.PremiumType > 0 {
					// Devil contract slot or premiumlist contract item: consumed at
					// checkout, account-level premium renews; nothing enters a bag.
					premium.Upsert(account, purchase.PremiumType, purchase.PremiumDurationSeconds, 1, premiumNow)
					premiumActivations = append(premiumActivations, currentCeraShopPremiumActivation{
						PremiumType:      purchase.PremiumType,
						RemainingSeconds: premium.ExpireAt(*account, purchase.PremiumType) - premiumNow.Unix(),
					})
					continue
				}
				if purchase.NameTagDurationSeconds > 0 {
					if nameTagActivation == nil {
						activation, err := applyCurrentNameTagCardAssets(
							character,
							equipment,
							product.ItemID,
							purchase.NameTagDurationSeconds,
							premiumNow,
						)
						if err != nil {
							return dnfcerashop.CheckoutChanges{}, err
						}
						nameTagActivation = &activation
						changes.Character = true
						changes.Equipment = true
					}
					continue
				}
				definition, err := catalog.items.ResolveItem(product.ItemID)
				if err != nil {
					return dnfcerashop.CheckoutChanges{}, fmt.Errorf("%w: commodity=%d item=%d: %v", errCurrentCeraShopProductUnavailable, product.CommodityID, product.ItemID, err)
				}
				definition, err = currentCeraShopProductDefinitionForGrantAt(definition, product, premiumNow)
				if err != nil {
					return dnfcerashop.CheckoutChanges{}, fmt.Errorf("%w: commodity=%d item=%d expiration: %v", errCurrentCeraShopProductUnavailable, product.CommodityID, product.ItemID, err)
				}
				// 装扮栏(list 1)判定用**运行时 PVF 家族**，不用商品所在的目录段：
				// 现场报障"商城购买装扮放在购物车无法批量购买，提示为物品栏空间
				// 不足"就是旧代码按 `product.Section == "avatar"` 分流的后果——
				// 从 [item]/[package]/[visual] 段卖出的装扮会掉进 grantCurrentCeraShopProduct
				// 的主背包装备页 9..64，主背包满时整单被拒，而 op64 的失败体只有
				// 一个错误码 4（客户端渲染成"物品栏空间不足"）。
				if currentPVFItemTargetContainer(definition).Avatar {
					slot, err := grantCurrentCeraShopAvatar(inventory, catalog.source, definition, product, purchase.AttributeValue)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					change := currentCeraShopChangedSlot{ListType: 1, Slot: slot, Product: product}
					changedSlots[currentCeraShopInventorySlotKey(change.ListType, change.Slot)] = change
					inventoryChanged = true
					continue
				}
				// 容器判定唯一真源 = currentPVFItemTargetContainer：
				// 宠物本体 → list 7 0..139；宠物消耗品（[feed] 饲料 + [creature] 更名卡）
				// → list 7 189..238。旧代码只看 isCurrentCeraShopPetConsumable（只认
				// [feed]），于是商城新买的更名卡落到了主背包消耗品页（65..120）。
				switch container := currentPVFItemTargetContainer(definition); {
				case container.PetBody:
					slot, err := grantCurrentCeraShopPet(inventory, definition)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					change := currentCeraShopChangedSlot{ListType: container.ListType, Slot: slot, Product: product}
					changedSlots[currentCeraShopInventorySlotKey(change.ListType, change.Slot)] = change
					petInventoryChanged = true
					inventoryChanged = true
					continue
				case container.PetConsumable:
					slots, err := grantCurrentCeraShopPetConsumable(inventory, definition, product.Count)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					for _, slot := range slots {
						change := currentCeraShopChangedSlot{ListType: container.ListType, Slot: slot, Product: product}
						changedSlots[currentCeraShopInventorySlotKey(change.ListType, change.Slot)] = change
					}
					petInventoryChanged = true
					inventoryChanged = true
					continue
				case container.GuildMedal, container.GuardianGem:
					// 公会勋章 / 守护珠的容器是 wire list 38（勋章 0..48、守护珠 49..97）。
					slots, err := grantCurrentGuildMedalPageItem(inventory, definition, container, product.Count)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					for _, slot := range slots {
						change := currentCeraShopChangedSlot{ListType: container.ListType, Slot: slot, Product: product}
						changedSlots[currentCeraShopInventorySlotKey(change.ListType, change.Slot)] = change
					}
					guildMedalInventoryChanged = true
					inventoryChanged = true
					continue
				}
				slots, err := grantCurrentCeraShopProduct(inventory, definition, product.Count)
				if err != nil {
					return dnfcerashop.CheckoutChanges{}, err
				}
				inventoryChanged = true
				for _, slot := range slots {
					change := currentCeraShopChangedSlot{ListType: dnfrepo.MainInventoryListType, Slot: int16(slot), Product: product}
					changedSlots[currentCeraShopInventorySlotKey(change.ListType, change.Slot)] = change
				}
				// One-plus-one IPG gifts: absolute per-purchase counts from the
				// runtime multibonus document, granted into the same container and
				// transaction as the purchased product.
				for _, bonus := range catalog.bonusByCommodity[product.CommodityID] {
					bonusDefinition, err := catalog.items.ResolveItem(bonus.ItemID)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, fmt.Errorf("%w: commodity=%d bonus item=%d: %v", errCurrentCeraShopProductUnavailable, product.CommodityID, bonus.ItemID, err)
					}
					bonusDefinition, err = currentPVFItemDefinitionForGrantAt(bonusDefinition, premiumNow)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, fmt.Errorf("%w: commodity=%d bonus item=%d expiration: %v", errCurrentCeraShopProductUnavailable, product.CommodityID, bonus.ItemID, err)
					}
					bonusSlots, err := grantCurrentCeraShopProduct(inventory, bonusDefinition, bonus.Count)
					if err != nil {
						return dnfcerashop.CheckoutChanges{}, err
					}
					inventoryChanged = true
					bonusProduct := currentCeraShopProduct{CommodityID: product.CommodityID, ItemID: bonus.ItemID, Section: product.Section}
					for _, slot := range bonusSlots {
						change := currentCeraShopChangedSlot{ListType: dnfrepo.MainInventoryListType, Slot: int16(slot), Product: bonusProduct, Bonus: true}
						changedSlots[currentCeraShopInventorySlotKey(change.ListType, change.Slot)] = change
					}
				}
			}

			for _, change := range changedSlots {
				key := currentCeraShopInventorySlotKey(change.ListType, change.Slot)
				stack, found := inventory.Slots[key]
				if !found || stack.ItemID <= 0 || (change.ListType != 1 && stack.Count <= 0) {
					return dnfcerashop.CheckoutChanges{}, errCurrentCeraShopProductUnavailable
				}
				entry := currentItemListEntryFromStack(change.ListType, change.Slot, stack)
				stack.RawEntry = append([]byte(nil), entry.data[:]...)
				if stack.Extra == nil {
					stack.Extra = make(map[string]string, 5)
				}
				stack.Extra["last_grant_source"] = "cera_shop"
				stack.Extra["last_cera_shop_commodity"] = strconv.FormatUint(uint64(change.Product.CommodityID), 10)
				stack.Extra["last_cera_shop_section"] = change.Product.Section
				if change.Bonus {
					stack.Extra["last_cera_shop_bonus"] = "one_plus_one_ipg"
				}
				if change.Product.DurationDays > 0 {
					stack.Extra["cera_shop_duration_days"] = strconv.FormatUint(uint64(change.Product.DurationDays), 10)
					stack.Extra["expiration_source"] = "runtime_pvf_cera_shop_duration_grant"
				}
				inventory.Slots[key] = stack
				entriesByList[change.ListType] = append(entriesByList[change.ListType], entry)
			}
			updates := make([]currentCeraShopItemUpdate, 0, len(entriesByList))
			for _, listType := range []byte{dnfrepo.MainInventoryListType, 1, currentGuildMedalInventoryListType} {
				entries := entriesByList[listType]
				if len(entries) == 0 {
					continue
				}
				sort.Slice(entries, func(left, right int) bool {
					return binary.LittleEndian.Uint16(entries[left].data[0:2]) < binary.LittleEndian.Uint16(entries[right].data[0:2])
				})
				updates = append(updates, currentCeraShopItemUpdate{ListType: listType, Entries: entries})
			}
			products := make([]currentCeraShopProduct, 0, len(purchases))
			for _, purchase := range purchases {
				products = append(products, purchase.Product)
			}
			result = currentCeraShopCommitResult{
				CharacterRenamePending:     len(purchases) == 1 && purchases[0].CharacterRenameRules != nil,
				Products:                   products,
				Updates:                    updates,
				MainInventoryExpansion:     containerUpgrades.MainInventoryExpansion,
				PersonalCargoSlotCount:     containerUpgrades.PersonalCargoSlotCount,
				AvatarInventoryExpansion:   containerUpgrades.AvatarInventoryExpansion,
				PetInventoryChanged:        petInventoryChanged,
				GuildMedalInventoryChanged: guildMedalInventoryChanged,
				PremiumActivations:         premiumActivations,
				NameTagActivation:          nameTagActivation,
				SkillTreeExpanded:          includeSkills,
			}
			changes.Inventory = inventoryChanged
			if err := s.privateFestivalPreflight(session, account, request); err != nil {
				return dnfcerashop.CheckoutChanges{}, err
			}
			if err := projectPrivateFestivalPurchase(catalog, account, characterID, request, purchases); err != nil {
				return dnfcerashop.CheckoutChanges{}, err
			}
			return changes, nil
		},
	})
	if err != nil {
		return currentCeraShopCommitResult{}, err
	}
	result.CeraAfter = checkoutResult.CeraAfter
	result.WalletAfter = checkoutResult.WalletAfter
	if request.FestivalRequestID != "" {
		s.mu.Lock()
		session.privateFestivalCommittedSerial = request.FestivalSerial
		s.mu.Unlock()
	}
	return result, nil
}

func commitCurrentCeraShopAccountCargoUpgrade(
	ctx context.Context,
	owner *dnfcerashop.Owner,
	accountID string,
	characterID string,
	purchase currentCeraShopResolvedPurchase,
	cost int64,
	now time.Time,
	restrictedCosts ...int64,
) (currentCeraShopCommitResult, error) {
	if owner == nil || accountID == "" || characterID == "" || cost < 0 {
		return currentCeraShopCommitResult{}, errCurrentCeraShopOwnerUnavailable
	}
	var result currentCeraShopCommitResult
	var cashOnly, tokenOnly int64
	if len(restrictedCosts) >= 2 {
		cashOnly, tokenOnly = restrictedCosts[0], restrictedCosts[1]
	}
	checkout, err := owner.Checkout(ctx, dnfcerashop.CheckoutCommand{
		AccountID:     accountID,
		CharacterID:   characterID,
		SettingsScope: dnfrepo.CharacterContainerStateScope(characterID),
		Cost:          cost,
		PayPolicy:     dnfcerashop.PayPolicyWaterfall, CeraOnlyCost: cashOnly, TokenOnlyCost: tokenOnly,
		UpdatedAt: now,
		Project: func(assets *dnfcerashop.CheckoutAssets) (dnfcerashop.CheckoutChanges, error) {
			if assets == nil || assets.Account == nil || assets.Character == nil ||
				assets.Character.Stats == nil {
				return dnfcerashop.CheckoutChanges{}, errCurrentCeraShopOwnerUnavailable
			}
			account := assets.Account
			if account.Metadata == nil {
				account.Metadata = make(map[string]string, 4)
			}
			previous, err := strconv.ParseInt(strings.TrimSpace(account.Metadata["account_cargo_level"]), 10, 64)
			if err != nil || previous <= 0 {
				return dnfcerashop.CheckoutChanges{}, errCurrentCeraShopProductUnavailable
			}
			next, ok := dnfcargo.NextAccountCargoTier(previous)
			if !ok || next <= previous || next > math.MaxUint16 || previous != purchase.AccountCargoUpgradeFrom || next != purchase.AccountCargoUpgradeTarget {
				return dnfcerashop.CheckoutChanges{}, errCurrentCeraShopProductUnavailable
			}
			account.Metadata["account_cargo_created"] = "true"
			account.Metadata["account_cargo_level"] = strconv.FormatInt(next, 10)
			if strings.TrimSpace(account.Metadata["account_cargo_gold"]) == "" {
				account.Metadata["account_cargo_gold"] = "0"
			}
			result = currentCeraShopCommitResult{
				Products:                 []currentCeraShopProduct{purchase.Product},
				AccountCargoSelectionKey: uint16(next),
			}
			return dnfcerashop.CheckoutChanges{}, nil
		},
	})
	if err != nil {
		return currentCeraShopCommitResult{}, err
	}
	result.CeraAfter = checkout.CeraAfter
	result.WalletAfter = checkout.WalletAfter
	return result, nil
}
