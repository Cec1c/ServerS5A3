package mail

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrMailboxRepoMissing         = errors.New("mailbox repository is missing")
	ErrMailboxCharacterMissing    = errors.New("mailbox character is missing")
	ErrMailboxRecipientMissing    = errors.New("mailbox recipient is missing")
	ErrMailboxInvalidRequest      = errors.New("mailbox request is invalid")
	ErrMailboxMailNotFound        = errors.New("mailbox mail is not found")
	ErrMailboxAlreadyClaimed      = errors.New("mailbox mail is already claimed")
	ErrMailboxExpired             = errors.New("mailbox mail expired")
	ErrMailboxInventoryFull       = errors.New("mailbox inventory is full")
	ErrMailboxAssetsUOW           = errors.New("mailbox asset unit of work is missing")
	ErrMailboxAssetMismatch       = errors.New("mailbox attachment does not match durable inventory")
	ErrMailboxGoldInsufficient    = errors.New("mailbox sender gold is insufficient")
	ErrMailboxDeleteClaimable     = errors.New("mailbox mail still has claimable assets")
	ErrMailboxStateUnsupported    = errors.New("mailbox state is unsupported")
	ErrMailboxSpecialUnsupported  = errors.New("special mailbox send is unsupported")
	ErrMailboxItemResolverMissing = errors.New("mailbox item price resolver is missing")
	ErrMailboxSelfRecipient       = errors.New("mailbox recipient is the sender")
	ErrMailboxSavedFull           = errors.New("mailbox saved-letter storage is full")
)

const (
	mailboxNormalPageSize = 20
	mailboxSavedPageSize  = 10
	mailboxNormalLifetime = 15 * 24 * time.Hour

	// The current PVF main-inventory categories occupy item slots 3..359:
	// quick slots 3..8, equipment 9..64, then each tab's durable range.
	mailboxMainFirstItemSlot int16 = 3
	mailboxMainLastItemSlot  int16 = 359

	// mailboxEpicPieceMaxCredit 是单次领取里一件碎片的增量上限（u32 账本）。
	mailboxEpicPieceMaxCredit int64 = 1<<32 - 1
)

type Owner struct {
	premiumResolver alignedcmd.PremiumContractResolver
	repos           dnfrepo.Group
	itemResolver    alignedcmd.MailboxItemResolver
	now             func() time.Time
	// epicPieceCreditor 非 nil 时，解析结果标了 EpicPieceID 的附件改记账号级
	// 史诗图鉴账本、不写背包。为 nil 时一切照旧（碎片当普通 [material] 入包）。
	epicPieceCreditor alignedcmd.MailboxEpicPieceCreditor
	// warehouseCreditor 非 nil 时，解析结果标了 AccountWarehouseSlot 的附件改并入
	// **账号级晶块/灵魂仓库**的固定格、不写背包。为 nil 时一切照旧（晶块当普通
	// [material] 堆进材料页/快捷格）。见 dnfbridge/cube_warehouse_routing.go。
	warehouseCreditor alignedcmd.MailboxWarehouseCreditor
	// claimedLetter 是已领取信件的收尾档位（DNFBRIDGE_MAILBOX_CLAIMED_LETTER，
	// 见 claimed_letter.go）。零值 = 默认档 finish。
	claimedLetter ClaimedLetterMode
}

type OpenResult struct {
	CharacterID string
	Total       int
	Unread      int
	Claimable   int
	NotLoaded   int
	Mails       []dnfrepo.MailRecord
	ObservedAt  time.Time
}

type SendResult struct {
	MailID               string
	RecipientCharacterID string
}

type ClaimResult struct {
	ContractActivations []alignedcmd.PremiumContractActivation
	InventoryFull       bool
	ClaimObjects        []mailboxClaimObject
	WalletChanged       bool
	MailIDs             []uint32
	Gold                int64
	ItemCount           int
	InventoryOK         bool
	ItemSlotRefreshes   []alignedcmd.ItemSlotRefresh
	// EpicPieces 是本次领取已经计入账号级史诗图鉴账本的碎片增量（含入账后的
	// 绝对值）。这些行**没有**背包槽位，所以不在 ItemSlotRefreshes 里：桥接层
	// 必须按 EpicPieces 补发 class0/0x047D 账本通知。
	EpicPieces []alignedcmd.MailboxEpicPieceCredit
	// WarehouseCredits 是本次领取已经并入账号晶块仓库固定格的增量（含入账后的
	// 绝对值）。对应的槽位**同时**出现在 ItemSlotRefreshes 里（list 0 354..365），
	// 桥接层的 op14 下发会从账号仓库记录里重读这些格的权威行。
	WarehouseCredits []alignedcmd.MailboxWarehouseCredit
}

func NewOwner(repos dnfrepo.Group, itemResolvers ...alignedcmd.MailboxItemResolver) Owner {
	var itemResolver alignedcmd.MailboxItemResolver
	if len(itemResolvers) > 0 {
		itemResolver = itemResolvers[0]
	}
	return Owner{
		repos:         repos,
		itemResolver:  itemResolver,
		now:           time.Now,
		claimedLetter: ClaimedLetterModeFromEnv(),
	}
}

// WithEpicPieceCreditor 注入账号级史诗图鉴账本的入账闭包（由 dnfbridge 提供）。
// 为零值时返回的 Owner 语义与改动前逐字节相同。
func (o Owner) WithEpicPieceCreditor(creditor alignedcmd.MailboxEpicPieceCreditor) Owner {
	o.epicPieceCreditor = creditor
	return o
}

// WithWarehouseCreditor 注入账号级晶块/灵魂仓库的入账闭包（由 dnfbridge 提供）。
// 为零值时返回的 Owner 语义与改动前逐字节相同（晶块照旧进背包）。
func (o Owner) WithWarehouseCreditor(creditor alignedcmd.MailboxWarehouseCreditor) Owner {
	o.warehouseCreditor = creditor
	return o
}

// 已误投邮件的PVF契约直接叠加账号时长，和领取状态一起提交，不占背包格。
func (o Owner) WithPremiumContractResolver(resolver alignedcmd.PremiumContractResolver) Owner {
	o.premiumResolver = resolver
	return o
}

type mailboxContractCredit struct {
	contract premium.InventoryContract
	count    int64
}

// mailboxWarehouseCreditItem 是一个固定格里已经聚合好的待入账晶块。
type mailboxWarehouseCreditItem struct {
	itemID uint32
	count  int64
	extra  map[string]string
}

func (o Owner) Open(ctx context.Context, characterID string) (OpenResult, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return OpenResult{}, ErrMailboxCharacterMissing
	}
	if o.repos.Mailbox == nil {
		return OpenResult{}, ErrMailboxRepoMissing
	}
	record, _, err := o.loadOrCreateMailbox(ctx, characterID)
	if err != nil {
		return OpenResult{}, err
	}
	record, err = o.prepareClaimIDsForOpen(ctx, record)
	if err != nil {
		return OpenResult{}, err
	}
	// Older claim implementations retained transferred assets on a claimed
	// letter. A claimed mail is an empty, read receipt: repair those legacy
	// rows before projecting the mailbox so the client cannot render or select
	// a stale attachment again. 默认档同时清掉"已删除却还挂着附件行"的存量形状
	// （见 clearUnclaimableMailboxAssets）；这份清理由 MailboxFieldMails 的整表
	// 重写真正落到 dnf_mail_attachments / dnf_mail_attachment_extra 上。
	if o.clearUnclaimableMailboxAssets(&record) {
		record.UpdatedAt = o.clock()
		if err := dnfrepo.SaveMailboxFields(ctx, o.repos.Mailbox, record, dnfrepo.MailboxFieldMails); err != nil {
			return OpenResult{}, err
		}
	}
	result := OpenResult{CharacterID: characterID}
	now := o.clock()
	result.ObservedAt = now
	normal := make([]dnfrepo.MailRecord, 0, len(record.Mails))
	saved := make([]dnfrepo.MailRecord, 0, len(record.Mails))
	for _, mail := range record.Mails {
		if mail.Deleted || mailExpired(mail, now) {
			continue
		}
		result.Total++
		if !mail.Read {
			result.Unread++
		}
		if !mail.Claimed && (mail.Gold > 0 || len(mail.Attachments) > 0) {
			result.Claimable++
		}
		if mailboxMailSaved(mail) {
			saved = append(saved, cloneMailboxMail(mail))
		} else {
			normal = append(normal, cloneMailboxMail(mail))
		}
	}
	sortMailboxMails(normal)
	sortMailboxMails(saved)
	if len(normal) > mailboxNormalPageSize {
		result.NotLoaded += len(normal) - mailboxNormalPageSize
		normal = normal[:mailboxNormalPageSize]
	}
	if len(saved) > mailboxSavedPageSize {
		result.NotLoaded += len(saved) - mailboxSavedPageSize
		saved = saved[:mailboxSavedPageSize]
	}
	result.Mails = append(normal, saved...)
	return result, nil
}

func (o Owner) Send(ctx context.Context, senderCharacterID string, req SendRequest) (SendResult, error) {
	senderCharacterID = strings.TrimSpace(senderCharacterID)
	if senderCharacterID == "" {
		return SendResult{}, ErrMailboxCharacterMissing
	}
	if strings.TrimSpace(req.RecipientName) == "" {
		return SendResult{}, ErrMailboxInvalidRequest
	}
	if req.Special != 0 || req.Global {
		return SendResult{}, ErrMailboxSpecialUnsupported
	}
	if req.Gold > math.MaxInt32 {
		return SendResult{}, ErrMailboxInvalidRequest
	}
	if o.repos.Character == nil {
		return SendResult{}, ErrMailboxCharacterMissing
	}
	if o.repos.Mailbox == nil {
		return SendResult{}, ErrMailboxRepoMissing
	}
	if o.repos.MailboxAssets == nil {
		return SendResult{}, ErrMailboxAssetsUOW
	}

	recipientID, found, err := o.repos.Character.FindIDByName(ctx, req.RecipientName)
	if err != nil {
		return SendResult{}, err
	}
	if !found {
		return SendResult{}, ErrMailboxRecipientMissing
	}
	if recipientID == senderCharacterID {
		return SendResult{}, ErrMailboxSelfRecipient
	}
	var mailID string
	err = o.repos.MailboxAssets.WithinMailboxAssets(
		ctx,
		senderCharacterID,
		recipientID,
		func(
			characters dnfrepo.CharacterRepository,
			inventories dnfrepo.InventoryRepository,
			mailboxes dnfrepo.MailboxRepository,
		) error {
			sender, senderFound, loadErr := characters.Load(ctx, senderCharacterID)
			if loadErr != nil {
				return loadErr
			}
			if !senderFound {
				return ErrMailboxCharacterMissing
			}
			if sender.Stats == nil {
				sender.Stats = make(map[string]int64)
			}
			gold := int64(req.Gold)

			var inventory dnfrepo.InventoryRecord
			var inventoryFound bool
			if len(req.Attachments) > 0 {
				inventory, inventoryFound, loadErr = inventories.Load(ctx, senderCharacterID)
				if loadErr != nil {
					return loadErr
				}
				if !inventoryFound || inventory.Slots == nil {
					return ErrMailboxAssetMismatch
				}
			}
			attachments := make([]dnfrepo.MailAttachment, 0, len(req.Attachments))
			var attachmentValue int64
			for idx, requested := range req.Attachments {
				if requested.Count > math.MaxInt32 {
					return ErrMailboxInvalidRequest
				}
				key := fmt.Sprintf("%d:%d", requested.ListType, requested.SlotIndex)
				stack, ok := inventory.Slots[key]
				if !ok || stack.ItemID != int64(requested.ItemID) || stack.Count < int64(requested.Count) {
					return fmt.Errorf("%w: attachment=%d list=%d slot=%d", ErrMailboxAssetMismatch, idx, requested.ListType, requested.SlotIndex)
				}
				if o.itemResolver == nil {
					return ErrMailboxItemResolverMissing
				}
				resolution, resolveErr := o.itemResolver(requested.ItemID)
				if resolveErr != nil || resolution.Price < 0 {
					return fmt.Errorf("%w: attachment=%d item=%d resolve=%v", ErrMailboxAssetMismatch, idx, requested.ItemID, resolveErr)
				}
				count := int64(requested.Count)
				if resolution.Price > 0 && count > math.MaxInt64/resolution.Price {
					return ErrMailboxInvalidRequest
				}
				value := resolution.Price * count
				if attachmentValue > math.MaxInt64-value {
					return ErrMailboxInvalidRequest
				}
				attachmentValue += value
				attachment := dnfrepo.MailAttachment{
					ItemID:   stack.ItemID,
					Count:    count,
					Bind:     stack.Bind,
					ExpireAt: stack.ExpireAt,
					RawEntry: append([]byte(nil), stack.RawEntry...),
					Extra:    cloneStringMap(stack.Extra),
				}
				if attachment.Extra == nil {
					attachment.Extra = make(map[string]string)
				}
				attachment.Extra["mailbox_pvf_kind"] = resolution.Kind
				attachment.Extra["mailbox_equipment_type"] = resolution.EquipmentType
				attachment.Extra["mailbox_pvf_path"] = resolution.PVFPath
				attachments = append(attachments, attachment)
				if stack.Count == count {
					delete(inventory.Slots, key)
				} else {
					stack.Count -= count
					inventory.Slots[key] = stack
				}
			}
			postage, postageErr := currentMailboxPostage(gold, len(attachments), attachmentValue)
			if postageErr != nil || gold > math.MaxInt64-postage {
				return ErrMailboxInvalidRequest
			}
			totalDebit := gold + postage
			if sender.Stats["gold"] < totalDebit {
				return ErrMailboxGoldInsufficient
			}

			record, recordFound, loadErr := mailboxes.Load(ctx, recipientID)
			if loadErr != nil {
				return loadErr
			}
			if !recordFound {
				record = dnfrepo.MailboxRecord{CharacterID: recipientID}
			}
			if record.Mails == nil {
				record.Mails = make(map[string]dnfrepo.MailRecord)
			}
			mailID, loadErr = nextMailID(record.Mails)
			if loadErr != nil {
				return loadErr
			}
			now := o.clock()
			record.Mails[mailID] = dnfrepo.MailRecord{
				MailID:               mailID,
				SenderCharacterID:    senderCharacterID,
				SenderName:           sender.Name,
				RecipientCharacterID: recipientID,
				RecipientName:        req.RecipientName,
				Body:                 req.Body,
				Gold:                 gold,
				Attachments:          attachments,
				CreatedAt:            now,
				ExpireAt:             now.Add(mailboxNormalLifetime),
				Metadata: map[string]string{
					"source":       mailboxSendSource(req),
					"special":      strconv.FormatUint(uint64(req.Special), 10),
					"global":       strconv.FormatBool(req.Global),
					"postage_gold": strconv.FormatInt(postage, 10),
				},
			}
			record.UpdatedAt = now
			sender.Stats["gold"] -= totalDebit
			if err := dnfrepo.SaveCharacterFields(ctx, characters, sender, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
			if len(req.Attachments) > 0 {
				if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
					return err
				}
			}
			return dnfrepo.SaveMailboxFields(ctx, mailboxes, record, dnfrepo.MailboxFieldMails)
		},
	)
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{MailID: mailID, RecipientCharacterID: recipientID}, nil
}

func (o Owner) Claim(ctx context.Context, characterID string, req ExtractRequest) (ClaimResult, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return ClaimResult{}, ErrMailboxCharacterMissing
	}
	if o.repos.Mailbox == nil {
		return ClaimResult{}, ErrMailboxRepoMissing
	}
	if o.repos.MailboxAssets == nil {
		return ClaimResult{}, ErrMailboxAssetsUOW
	}
	if len(req.MailIDs) == 0 {
		return ClaimResult{}, ErrMailboxInvalidRequest
	}

	record, found, err := o.repos.Mailbox.Load(ctx, characterID)
	if err != nil {
		return ClaimResult{}, err
	}
	if !found {
		return ClaimResult{}, ErrMailboxMailNotFound
	}
	// 邮箱开着时也可能收到新奖励；先为新增信分配编号，保留已展示附件的编号。
	if mailboxHasStoredClaimIDs(record) {
		record, err = o.prepareClaimIDsForOpen(ctx, record)
		if err != nil {
			return ClaimResult{}, err
		}
	}
	hasAttachments, hasVouchers := false, false
	contracts := make(map[int64]premium.InventoryContract)
	preselected, err := selectMailboxClaims(record, req.MailIDs)
	if err != nil {
		return ClaimResult{}, err
	}
	for _, selection := range preselected {
		mail := selection.mail
		if mail.Claimed {
			continue
		}
		for _, item := range mail.Attachments {
			if shop.IsVoucher(item.ItemID) {
				hasVouchers = true
			} else {
				if o.premiumResolver != nil {
					contract, e := o.premiumResolver(item.ItemID)
					if e != nil {
						return ClaimResult{}, e
					}
					if contract.PremiumType != 0 || contract.DurationSeconds != 0 {
						if contract.ItemID != item.ItemID || contract.PremiumType <= 0 || contract.DurationSeconds <= 0 {
							return ClaimResult{}, ErrMailboxAssetMismatch
						}
						contracts[item.ItemID] = premium.InventoryContract{ItemID: item.ItemID, PremiumType: contract.PremiumType, DurationSeconds: contract.DurationSeconds}
						continue
					}
				}
				hasAttachments = true
			}
		}
	}
	if hasAttachments && o.itemResolver == nil {
		return ClaimResult{}, ErrMailboxItemResolverMissing
	}
	result := ClaimResult{}
	var accounts dnfrepo.AccountRepository
	var accountID string
	within := o.repos.MailboxAssets.WithinMailboxAssets
	if hasVouchers || len(contracts) > 0 {
		tx, ok := o.repos.MailboxAssets.(dnfrepo.AccountTradeMailboxUnitOfWork)
		if !ok || o.repos.Character == nil {
			return ClaimResult{}, ErrMailboxAssetsUOW
		}
		ch, found, e := o.repos.Character.Load(ctx, characterID)
		if e != nil {
			return ClaimResult{}, e
		}
		if !found || ch.AccountID == "" {
			return ClaimResult{}, ErrMailboxCharacterMissing
		}
		accountID = ch.AccountID
		within = func(ctx context.Context, _, _ string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
			return tx.WithinAccountTradeMailbox(ctx, accountID, []string{characterID}, characterID, func(a dnfrepo.AccountRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
				accounts = a
				return apply(c, i, m)
			})
		}
	}
	if err := within(ctx, characterID, characterID, func(
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		mailbox, mailboxFound, err := mailboxes.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !mailboxFound {
			return ErrMailboxMailNotFound
		}
		if mailboxHasStoredClaimIDs(mailbox) {
			if _, err := ensureMailboxClaimIDs(&mailbox); err != nil {
				return err
			}
		}
		now := o.clock()
		if len(contracts) > 0 {
			if accounts == nil {
				return ErrMailboxAssetsUOW
			}
			character, found, err := characters.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !found || character.AccountID != accountID {
				return ErrMailboxCharacterMissing
			}
		}
		type claimCandidate struct {
			mailboxClaimSelection
			// settled 为真表示这封信在本次请求之前就已经是"已领取"：它不再发货，
			// 只按 claimed_letter.go 的档位结清（默认档移出收件箱）。
			settled bool
		}
		selected := make([]claimCandidate, 0, len(req.MailIDs))
		selections, err := selectMailboxClaims(mailbox, req.MailIDs)
		if err != nil {
			return err
		}
		for _, selection := range selections {
			mail := selection.mail
			if mail.Deleted {
				// 默认档已经把这张空收据结清过（客户端双击 / 重复 op95 / 并发重放）。
				// 答复与改动前保持一致（0x04），不许变成"邮件不存在"。
				if o.finishesClaimedLetters() && claimedLetterSettled(mail) {
					continue
				}
				return ErrMailboxMailNotFound
			}
			if mail.Claimed {
				if !o.finishesClaimedLetters() {
					// 回退档：今天的逐字节行为 —— 已领取的信一律跳过。
					continue
				}
				// 已经领取过的信不再发一次货（附件在旧实现里就已经转出，只剩投影
				// 没清 —— 与 clearUnclaimableMailboxAssets 同口径），本次只把它结清。
				selected = append(selected, claimCandidate{mailboxClaimSelection: selection, settled: true})
				continue
			}
			if mailExpired(mail, now) {
				return ErrMailboxExpired
			}
			if mail.Gold < 0 {
				return ErrMailboxInvalidRequest
			}
			selected = append(selected, claimCandidate{mailboxClaimSelection: selection})
		}
		if len(selected) == 0 {
			return ErrMailboxAlreadyClaimed
		}

		selectedHasAttachments := false
		for _, candidate := range selected {
			if !candidate.settled && len(candidate.mail.Attachments) > 0 {
				selectedHasAttachments = true
				break
			}
		}
		inventory := dnfrepo.InventoryRecord{CharacterID: characterID}
		if selectedHasAttachments {
			loadedInventory, inventoryFound, loadErr := inventories.Load(ctx, characterID)
			if loadErr != nil {
				return loadErr
			}
			if !inventoryFound {
				inventory = dnfrepo.InventoryRecord{CharacterID: characterID}
			} else {
				inventory = loadedInventory
			}
			if err := dnfrepo.BindInventoryContainerState(ctx, o.repos.Settings, &inventory); err != nil {
				return err
			}
			if inventory.Slots == nil {
				inventory.Slots = make(map[string]dnfrepo.ItemStack)
			}
		}

		// 每个领取对象单独规划；同封多附件使用持久化编号，旧未投影邮件兼容整信领取。
		// 一个分页满格不回滚本批已放得下的对象，未接受对象仍留在原信中。
		accepted := make([]claimCandidate, 0, len(selected))
		var totalGold int64
		acceptedVouchers := shop.VoucherCounts{}
		var acceptedContracts []mailboxContractCredit
		inventoryChanged := false
		inventoryFull := false
		// acceptedPieces 是"已接受但还没入账"的碎片，按碎片 id 聚合。
		// 入账刻意放在整批候选定案之后：碎片不占格、不参与 ErrMailboxInventoryFull
		// 回退，如果边判定边入账，一个后面被回退掉的候选会留下无法撤销的账本增量。
		acceptedPieces := make(map[uint32]int64)
		// acceptedWarehouse 是"已接受但还没并入账号晶块仓库"的晶块，按固定格聚合。
		// 与 acceptedPieces 同样刻意放在整批候选定案之后（见上面的理由）。
		acceptedWarehouse := make(map[int16]mailboxWarehouseCreditItem)
		for _, candidate := range selected {
			if candidate.settled {
				// 已经领取过的信：不重复发货、不占格、不会因为背包满而被回退，
				// 直接进结清名单（默认档由下面的提交循环移出收件箱）。
				result.MailIDs = append(result.MailIDs, candidate.id)
				accepted = append(accepted, candidate)
				continue
			}
			candidateInventory := dnfrepo.CloneInventory(inventory)
			if candidateInventory.Slots == nil {
				candidateInventory.Slots = make(map[string]dnfrepo.ItemStack)
			}
			candidateRefreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(candidate.mail.Attachments))
			candidateItemCount := 0
			candidateVouchers := shop.VoucherCounts{}
			var candidateContracts []mailboxContractCredit
			candidateInventoryChanged := false
			candidatePieces := make(map[uint32]int64)
			candidateWarehouse := make(map[int16]mailboxWarehouseCreditItem)
			fits := true
			for _, attachment := range candidate.mail.Attachments {
				if attachment.ItemID <= 0 || attachment.Count <= 0 {
					return ErrMailboxInvalidRequest
				}
				if kind := shop.VoucherKindOf(attachment.ItemID); kind != shop.VoucherNone {
					if accounts == nil || attachment.Count > shop.MaxWalletBalance {
						return ErrMailboxAssetMismatch
					}
					target := &candidateVouchers.TokenCera
					if kind == shop.VoucherHappyTokenCera {
						target = &candidateVouchers.HappyTokenCera
					}
					if *target > shop.MaxWalletBalance-attachment.Count {
						return shop.ErrWalletOverflow
					}
					*target += attachment.Count
					candidateItemCount++
					continue
				}
				if contract, ok := contracts[attachment.ItemID]; ok {
					candidateContracts = append(candidateContracts, mailboxContractCredit{contract: contract, count: attachment.Count})
					candidateItemCount++
					continue
				}
				if o.itemResolver == nil {
					return ErrMailboxItemResolverMissing
				}
				resolution, resolveErr := o.itemResolver(uint32(attachment.ItemID))
				if resolveErr != nil {
					return fmt.Errorf("%w: attachment item=%d resolve=%v", ErrMailboxAssetMismatch, attachment.ItemID, resolveErr)
				}
				grantAnchor := candidate.mail.CreatedAt
				if !MailCreationAnchorsAttachment(candidate.mail) {
					grantAnchor = time.Time{}
				}
				attachment, resolveErr = mailboxAttachmentWithOriginalExpiry(attachment, resolution, grantAnchor)
				if resolveErr != nil {
					return resolveErr
				}
				// 史诗碎片：不进背包、不占格，也没有可刷新的槽位。它改记**账号级**
				// 史诗图鉴账本（跨角色共享），由桥接层在 ACK 之后补发 0x047D 增量。
				// 只有桥接层同时注入了入账闭包时才走这条路：否则（含开关回退档）
				// 保持改动前的入包行为，绝不静默丢弃附件。
				if resolution.EpicPieceID != 0 && o.epicPieceCreditor != nil {
					if candidatePieces[resolution.EpicPieceID] > math.MaxInt64-attachment.Count {
						return ErrMailboxInvalidRequest
					}
					candidatePieces[resolution.EpicPieceID] += attachment.Count
					candidateItemCount++
					continue
				}
				// 小晶块/灵魂：一律进账号级晶块仓库的固定格（dnfrepo 的唯一映射），
				// **不写任何背包行、不占任何格**。现场（2026-09-29）：GM 面板发
				// "无色小晶块"邮件 ⇒ 领取时按普通 [material] 落进主背包材料页
				// （DB 角色 1 `0:130` item 3037；packet_log 196766 op14 slot=130），
				// 客户端 1.3 s 后 connection reset；而同一客户端对**晶块仓库格
				// 354..365 的 op14 行一直正常**（packet_log 里数百条
				// `6601 dd0b0000 …` = slot 358 item 3037 的行，会话均存活）。
				// 只有桥接层同时注入入账闭包时才走这条路：否则（含开关回退档）
				// 保持改动前的入包行为，绝不静默丢弃附件。
				if resolution.AccountWarehouseSlot != 0 && o.warehouseCreditor != nil {
					itemID := attachment.ItemID
					// fail closed：目标格必须是账号共享段、且与唯一映射逐值一致
					// （item→slot 由 dnfrepo 给出，这里只做复核，不另造第二套口径）。
					fixed, mapped := dnfrepo.AccountWarehouseSlotForItem(itemID)
					if !mapped || fixed != resolution.AccountWarehouseSlot ||
						!dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, fixed) {
						return fmt.Errorf("%w: attachment item=%d warehouse slot=%d mapped=%v",
							ErrMailboxAssetMismatch, itemID, resolution.AccountWarehouseSlot, mapped)
					}
					if itemID <= 0 || itemID > math.MaxInt64 || uint32(itemID) == 0 {
						return ErrMailboxInvalidRequest
					}
					credit := candidateWarehouse[fixed]
					if credit.itemID != 0 && credit.itemID != uint32(itemID) {
						// 一个固定格只允许一件道具（映射是 1:1），否则 fail closed。
						return fmt.Errorf("%w: warehouse slot %d already holds item %d",
							ErrMailboxInvalidRequest, fixed, credit.itemID)
					}
					if credit.count > math.MaxInt64-attachment.Count {
						return ErrMailboxInvalidRequest
					}
					if credit.itemID == 0 {
						credit.itemID = uint32(itemID)
						credit.extra = mailboxAttachmentStack(attachment, attachment.Count, resolution).Extra
					}
					credit.count += attachment.Count
					candidateWarehouse[resolution.AccountWarehouseSlot] = credit
					candidateItemCount++
					continue
				}
				if !mailboxClaimSlotRangeValid(resolution) {
					return fmt.Errorf("%w: attachment item=%d resolve=%v", ErrMailboxAssetMismatch, attachment.ItemID, resolveErr)
				}
				refreshes, addErr := addAttachmentToInventory(&candidateInventory, attachment, resolution)
				if errors.Is(addErr, ErrMailboxInventoryFull) {
					fits = false
					inventoryFull = true
					break
				}
				if addErr != nil {
					return addErr
				}
				candidateRefreshes = append(candidateRefreshes, refreshes...)
				candidateInventoryChanged = true
				candidateItemCount++
			}
			if !fits {
				continue
			}
			if totalGold > math.MaxInt64-candidate.mail.Gold {
				return ErrMailboxInvalidRequest
			}
			totalGold += candidate.mail.Gold
			if acceptedVouchers.TokenCera > shop.MaxWalletBalance-candidateVouchers.TokenCera || acceptedVouchers.HappyTokenCera > shop.MaxWalletBalance-candidateVouchers.HappyTokenCera {
				return shop.ErrWalletOverflow
			}
			acceptedVouchers = acceptedVouchers.Add(candidateVouchers)
			acceptedContracts = append(acceptedContracts, candidateContracts...)
			inventory = candidateInventory
			inventoryChanged = inventoryChanged || candidateInventoryChanged
			for pieceID, count := range candidatePieces {
				if acceptedPieces[pieceID] > math.MaxInt64-count {
					return ErrMailboxInvalidRequest
				}
				acceptedPieces[pieceID] += count
			}
			for slot, credit := range candidateWarehouse {
				merged := acceptedWarehouse[slot]
				if merged.itemID != 0 && merged.itemID != credit.itemID {
					return fmt.Errorf("%w: warehouse slot %d mixed items %d/%d",
						ErrMailboxInvalidRequest, slot, merged.itemID, credit.itemID)
				}
				if merged.count > math.MaxInt64-credit.count {
					return ErrMailboxInvalidRequest
				}
				if merged.itemID == 0 {
					merged.itemID = credit.itemID
					merged.extra = credit.extra
				}
				merged.count += credit.count
				acceptedWarehouse[slot] = merged
			}
			result.ItemCount += candidateItemCount
			result.ItemSlotRefreshes = append(result.ItemSlotRefreshes, candidateRefreshes...)
			result.MailIDs = append(result.MailIDs, candidate.id)
			accepted = append(accepted, candidate)
		}
		if len(accepted) == 0 {
			if inventoryFull {
				return ErrMailboxInventoryFull
			}
			return ErrMailboxAlreadyClaimed
		}
		result.InventoryFull = inventoryFull
		result.Gold = totalGold
		if !acceptedVouchers.IsZero() || len(acceptedContracts) > 0 {
			account, found, e := accounts.Load(ctx, accountID)
			if e != nil {
				return e
			}
			if !found || account.AccountID != accountID {
				return ErrMailboxCharacterMissing
			}
			account = dnfrepo.CloneAccount(account)
			if _, e = shop.CreditVoucherCounts(&account, acceptedVouchers); e != nil {
				return e
			}
			for _, credit := range acceptedContracts {
				activation, err := premium.ActivateContractGrant(&account, credit.contract, credit.count, now)
				if err != nil {
					return err
				}
				result.ContractActivations = append(result.ContractActivations, alignedcmd.PremiumContractActivation{ItemID: activation.ItemID, PremiumType: activation.PremiumType, Units: activation.Units, ExpireAt: activation.ExpireAt})
			}
			account.UpdatedAt = now
			if e = accounts.Save(ctx, account); e != nil {
				return e
			}
			result.WalletChanged = !acceptedVouchers.IsZero()
		}

		if totalGold > 0 {
			character, ok, err := characters.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrMailboxCharacterMissing
			}
			if character.Stats == nil {
				character.Stats = make(map[string]int64)
			}
			if character.Stats["gold"] > math.MaxInt64-totalGold {
				return ErrMailboxInvalidRequest
			}
			character.Stats["gold"] += totalGold
			if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
		}

		if inventoryChanged {
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}

		// 史诗碎片入账。位置刻意放在"背包已提交、邮件尚未标记已领取"之间：
		//
		//   - 入账失败 ⇒ 直接返回错误，整个邮件资产事务回滚，邮件仍然可领
		//     （fail-closed，与 dnfrepo.AddAccountEpicPieceBalance 的取向一致：
		//     宁可让碎片留着，也不做读改写兜底把它吞掉）；
		//   - 入账成功但最后 SaveMailboxFields 失败 ⇒ 邮件仍是未领取，
		//     下一次领取会对同一封信再入账一次。这是账本与邮件不在同一个存储
		//     事务里带来的已知窗口：本仓库的 dnf_account_metadata 计数是原子
		//     自增，无法参与 MailboxAssets 事务。取舍是"可能重复"而不是"丢失"。
		// 顺序按碎片 id 升序，保证同一批附件的账本写入与 0x047D 行序稳定可做夹具。
		if len(acceptedPieces) > 0 {
			pieceIDs := make([]uint32, 0, len(acceptedPieces))
			for pieceID, count := range acceptedPieces {
				if pieceID != 0 && count > 0 {
					pieceIDs = append(pieceIDs, pieceID)
				}
			}
			sort.Slice(pieceIDs, func(i, j int) bool { return pieceIDs[i] < pieceIDs[j] })
			for _, pieceID := range pieceIDs {
				count := acceptedPieces[pieceID]
				if count <= 0 || count > mailboxEpicPieceMaxCredit {
					return fmt.Errorf("%w: epic piece %d count %d", ErrMailboxInvalidRequest, pieceID, count)
				}
				balance, creditErr := o.epicPieceCreditor(ctx, pieceID, count)
				if creditErr != nil {
					return fmt.Errorf("mailbox epic piece %d credit: %w", pieceID, creditErr)
				}
				result.EpicPieces = append(result.EpicPieces, alignedcmd.MailboxEpicPieceCredit{
					PieceID: pieceID,
					Count:   count,
					Balance: balance,
				})
			}
		}

		// 小晶块/灵魂并入账号晶块仓库。位置与取向和上面的史诗碎片账本一致
		// （dnf_account_inventory_items 是账号级、跨角色共享，不属于 MailboxAssets
		// 事务的三个仓储；闭包由 dnfbridge 注入，见 cube_warehouse_routing.go）：
		//
		//   - 入账失败 ⇒ 返回错误、整个邮件资产事务回滚，邮件仍然可领；
		//   - 入账成功但最后 SaveMailboxFields 失败 ⇒ 邮件仍是未领取，下一次领取会
		//     再并一次。已知窗口，取舍是"可能重复"而不是"丢失"。
		// 顺序按固定格升序，保证同一批附件的入账与 op14 行序稳定可做夹具。
		if len(acceptedWarehouse) > 0 {
			slots := make([]int16, 0, len(acceptedWarehouse))
			for slot := range acceptedWarehouse {
				slots = append(slots, slot)
			}
			sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
			for _, slot := range slots {
				credit := acceptedWarehouse[slot]
				if credit.itemID == 0 || credit.count <= 0 {
					return fmt.Errorf("%w: warehouse slot %d item=%d count=%d",
						ErrMailboxInvalidRequest, slot, credit.itemID, credit.count)
				}
				balance, creditErr := o.warehouseCreditor(ctx, slot, credit.itemID, credit.count, credit.extra)
				if creditErr != nil {
					return fmt.Errorf("mailbox warehouse slot %d credit: %w", slot, creditErr)
				}
				result.WarehouseCredits = append(result.WarehouseCredits, alignedcmd.MailboxWarehouseCredit{
					Slot:    slot,
					ItemID:  credit.itemID,
					Count:   credit.count,
					Balance: balance,
				})
				// 槽位刷新：桥接层的 sendSelectedIncrementalItemSlotRefreshes 对
				// 354..365 会从**账号仓库记录**重读权威行，所以这里只给 (list,slot)。
				result.ItemSlotRefreshes = append(result.ItemSlotRefreshes,
					alignedcmd.ItemSlotRefresh{
						ListType:  dnfrepo.MainInventoryListType,
						SlotIndex: slot,
					})
			}
		}

		for _, candidate := range accepted {
			parentID, err := mailboxWireID(candidate.mail.MailID)
			if err != nil {
				return err
			}
			result.ClaimObjects = append(result.ClaimObjects, mailboxClaimObject{MailID: parentID, ClaimID: candidate.id})
			mail := mailbox.Mails[candidate.key]
			// Attachments and gold have already been committed to the recipient's
			// character/inventory in this same unit of work. Do not retain a second
			// claimable projection on the mail itself.
			if err := settleMailboxClaim(&mail, candidate.mailboxClaimSelection); err != nil {
				return err
			}
			mail.Read = true
			// 领完就是完：这封信已经没有可领取的东西了，从收件箱移除。
			//
			// 现场（2026-09-28，角色 6 `test78`；packet_log 里 op95 全是单封 count=1）：
			// 客户端「全部接受」会在 op95 之后自己补一条 op134 删除，但**点开一封信再点
			// 收取**这条路不补 —— 玩家报的「邮件领取后还是有空邮件在邮箱」就是它：
			// 领完的信留成一张空收据，而"再领一次"在改动前是死胡同（0x04）。
			// 厂商的领取结果里本来就有 RemovedFromInbox 这一支
			// （MailboxHandler.cs:365-370：领取成功后告诉客户端这封信没了 + 刷列表），
			// 我们的客户端不会自己删，所以这里由服务端替它完成。
			//
			// 收藏（mailbox_saved）的信除外：玩家点过收藏就是要留着这封信。
			// 客户端随后补发的那条 op134 由 ChangeState 的幂等分支接住（不是错误）。
			if mail.Claimed && o.finishesClaimedLetters() && !mailboxMailSaved(mail) {
				mail.Deleted = true
			}
			mailbox.Mails[candidate.key] = mail
		}
		mailbox.UpdatedAt = now
		return dnfrepo.SaveMailboxFields(ctx, mailboxes, mailbox, dnfrepo.MailboxFieldMails)
	}); err != nil {
		return ClaimResult{}, err
	}
	result.InventoryOK = true
	return result, nil
}

func (o Owner) ChangeState(ctx context.Context, characterID string, req ChangeStateRequest) error {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return ErrMailboxCharacterMissing
	}
	if o.repos.Mailbox == nil {
		return ErrMailboxRepoMissing
	}
	if o.repos.MailboxAssets == nil {
		return ErrMailboxAssetsUOW
	}
	if req.Status != 0 && req.Status != 2 && req.Status != 3 {
		return fmt.Errorf("%w: %d", ErrMailboxStateUnsupported, req.Status)
	}
	return o.repos.MailboxAssets.WithinMailboxAssets(ctx, characterID, characterID, func(
		_ dnfrepo.CharacterRepository,
		_ dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		record, found, err := mailboxes.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || len(record.Mails) == 0 {
			return ErrMailboxMailNotFound
		}
		now := o.clock()
		savedCount := 0
		for _, candidate := range record.Mails {
			if !candidate.Deleted && !mailExpired(candidate, now) && mailboxMailSaved(candidate) {
				savedCount++
			}
		}
		for _, id := range req.MailIDs {
			key := strconv.FormatUint(uint64(id), 10)
			mail, ok := record.Mails[key]
			if !ok || mailExpired(mail, now) {
				return ErrMailboxMailNotFound
			}
			if mail.Deleted {
				// 默认档：这封信已经结清并移出收件箱（例如 op95 结清之后，客户端
				// 「全部接受」又照自己的选择列表补发了一条 op134）。按 GM 删除路径
				// gm.go:566-568 的幂等口径：无操作、如实回报，不让整批删除因为一封
				// 已经不在的信而全部失败。回退档保持改动前的"邮件不存在"。
				if req.Status == 0 && o.finishesClaimedLetters() {
					continue
				}
				return ErrMailboxMailNotFound
			}
			switch req.Status {
			case 0:
				if !mail.Claimed && (mail.Gold > 0 || len(mail.Attachments) > 0) {
					return ErrMailboxDeleteClaimable
				}
				mail.Deleted = true
				if o.finishesClaimedLetters() {
					// 删掉的信不许再留可领取资产：聚合里清干净，MailboxFieldMails
					// 的整表重写（mysql replaceMails）才会把 dnf_mail_attachments /
					// dnf_mail_attachment_extra 里这封信的行一起删掉，而不是像现场
					// 角色 3 的 mail 2/5 那样留下"已删除 + 还挂着附件行"的形状。
					// GM 删除路径（api_mailbox_delete.go 的 Deleted/Read/Gold/Attachments）
					// 本来就是这么写的。
					mail.Gold = 0
					mail.Attachments = nil
				}
			case 2:
				mail.Read = true
			case 3:
				if !mailboxMailSaved(mail) && savedCount >= mailboxSavedPageSize {
					return ErrMailboxSavedFull
				}
				if mail.Metadata == nil {
					mail.Metadata = make(map[string]string)
				}
				if !mailboxMailSaved(mail) {
					savedCount++
				}
				mail.Metadata["mailbox_saved"] = "true"
				mail.Read = true
			}
			record.Mails[key] = mail
		}
		record.UpdatedAt = now
		return dnfrepo.SaveMailboxFields(ctx, mailboxes, record, dnfrepo.MailboxFieldMails)
	})
}

func (o Owner) loadOrCreateMailbox(ctx context.Context, characterID string) (dnfrepo.MailboxRecord, bool, error) {
	record, found, err := o.repos.Mailbox.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.MailboxRecord{}, false, err
	}
	if found {
		if record.Mails == nil {
			record.Mails = make(map[string]dnfrepo.MailRecord)
		}
		return record, true, nil
	}
	record = dnfrepo.MailboxRecord{
		CharacterID: characterID,
		Mails:       make(map[string]dnfrepo.MailRecord),
		UpdatedAt:   o.clock(),
	}
	if err := o.repos.Mailbox.Save(ctx, record); err != nil {
		return dnfrepo.MailboxRecord{}, false, err
	}
	return record, false, nil
}

// clearUnclaimableMailboxAssets 清掉"再也领不到资产"的信上的可领取投影，并报告
// 是否有改动（有改动时调用方必须落库，否则聚合与 dnf_mail_attachments 会继续
// 各说一套）：
//
//   - **已领取**的信：旧领取实现把已经转出的资产留在了信上，参考实现（`90原神开源
//     0.1.8` 的同名函数）与 Owner.Open 的口径都是"已领取的信 = 一张空的已读收据"，
//     所以这里只清投影、**绝不二次发货**；
//   - **已删除**的信（默认档）：本档的删除路径（ChangeState case 0）已经会清，
//     但存量行还在（现场角色 3 的 mail 2/5：`deleted_flag=1` 却还挂着附件行）。
//     Claim 对已删除的信回 `ErrMailboxMailNotFound`、ChangeState 也不会再删第二次，
//     这些资产**永远发不出去**；留着只会在排障时被误读成"某个角色的信还挂着附件"。
//     回退档（keep）不做这一步，逐字节保留改动前的行为。
func (o Owner) clearUnclaimableMailboxAssets(record *dnfrepo.MailboxRecord) bool {
	if record == nil || len(record.Mails) == 0 {
		return false
	}
	includeDeleted := o.finishesClaimedLetters()
	changed := false
	for key, mail := range record.Mails {
		if mail.Gold == 0 && len(mail.Attachments) == 0 {
			continue
		}
		if !mail.Claimed && !(includeDeleted && mail.Deleted) {
			continue
		}
		mail.Gold = 0
		mail.Attachments = nil
		record.Mails[key] = mail
		changed = true
	}
	return changed
}

func (o Owner) clock() time.Time {
	if o.now == nil {
		return time.Now().UTC()
	}
	return o.now().UTC()
}

func addAttachmentToInventory(inventory *dnfrepo.InventoryRecord, attachment dnfrepo.MailAttachment, resolution alignedcmd.MailboxItemResolution) ([]alignedcmd.ItemSlotRefresh, error) {
	if inventory == nil || attachment.ItemID <= 0 || attachment.Count <= 0 || !mailboxClaimSlotRangeValid(resolution) {
		return nil, ErrMailboxInvalidRequest
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	remaining := attachment.Count
	stack := mailboxAttachmentStack(attachment, remaining, resolution)
	changedSlots := make([]alignedcmd.ItemSlotRefresh, 0, 1)
	markChanged := func(slot int16) {
		refresh := alignedcmd.ItemSlotRefresh{ListType: resolution.TargetListType, SlotIndex: slot}
		for _, previous := range changedSlots {
			if previous.ListType == refresh.ListType && previous.SlotIndex == refresh.SlotIndex {
				return
			}
		}
		changedSlots = append(changedSlots, refresh)
	}
	policy := mailboxClaimSlotPolicy(resolution).ForInventory(inventory)
	for _, slot := range policy.MergeSlots() {
		if remaining <= 0 {
			break
		}
		key := mailboxSlotKey(resolution.TargetListType, slot)
		existing, occupied := inventory.Slots[key]
		if !occupied || !canMailStack(existing, stack) || existing.Count < 0 {
			continue
		}
		capacity := remaining
		// 堆叠上限只看运行时 PVF 声明（StackLimit == 0 = 无上限）。旧代码额外要求
		// "两边都没有 0x77 raw 行"才认上限，等于带 raw 行的邮件附件可以把每格上限
		// 100 的消耗品并成无限大一格——宠物饲料这类 [feed] 正是 raw 行随行下发的。
		if resolution.StackLimit > 0 {
			if existing.Count >= resolution.StackLimit {
				continue
			}
			capacity = resolution.StackLimit - existing.Count
			if capacity > remaining {
				capacity = remaining
			}
		}
		if capacity <= 0 || existing.Count > math.MaxInt64-capacity {
			return nil, ErrMailboxInvalidRequest
		}
		existing.Count += capacity
		inventory.Slots[key] = existing
		markChanged(slot)
		remaining -= capacity
	}
	for _, slot := range policy.EmptySlots() {
		if remaining <= 0 {
			break
		}
		key := mailboxSlotKey(resolution.TargetListType, slot)
		if _, occupied := inventory.Slots[key]; occupied {
			continue
		}
		count := remaining
		if resolution.StackLimit > 0 && count > resolution.StackLimit {
			count = resolution.StackLimit
		}
		inventory.Slots[key] = mailboxAttachmentStack(attachment, count, resolution)
		markChanged(slot)
		remaining -= count
	}
	if remaining > 0 {
		return nil, ErrMailboxInventoryFull
	}
	return changedSlots, nil
}

// mailboxClaimSlotRangeValid 校验解析器给出的目标容器与槽位区间。
// 主背包仍按原规则限制在物品页；宠物背包等其它容器由解析器给出区间
// （list 7 的宠物消耗品段），只要求区间自洽且非负。
func mailboxClaimSlotRangeValid(resolution alignedcmd.MailboxItemResolution) bool {
	if resolution.TargetListType != dnfrepo.MainInventoryListType {
		return resolution.SlotStart >= 0 && resolution.SlotEnd >= resolution.SlotStart
	}
	return resolution.SlotStart >= mailboxMainFirstItemSlot &&
		resolution.SlotEnd >= resolution.SlotStart &&
		resolution.SlotEnd <= mailboxMainLastItemSlot
}

func mailboxClaimSlotPolicy(resolution alignedcmd.MailboxItemResolution) dnfplacement.SlotPolicy {
	return dnfplacement.SlotPolicy{ListType: resolution.TargetListType,
		Start: resolution.SlotStart, End: resolution.SlotEnd,
		AllowQuickSlots: resolution.AllowQuickSlots, PreferQuickSlots: resolution.PreferQuickSlot}
}

func mailboxClaimStackSlots(resolution alignedcmd.MailboxItemResolution) []int16 {
	return mailboxClaimSlotPolicy(resolution).MergeSlots()
}

func mailboxAttachmentStack(attachment dnfrepo.MailAttachment, count int64, resolution alignedcmd.MailboxItemResolution) dnfrepo.ItemStack {
	// 已有装备优先保留原始行中的实例值，新造装备默认值不能覆盖旧品级和耐久。
	if resolution.Kind == "equipment" && resolution.TargetListType == 0 {
		RestoreEquipmentAttachmentInstanceExtra(&attachment)
	}
	extra := cloneStringMap(attachment.Extra)
	// These three keys are mailbox-envelope framing evidence captured when a
	// player sends an item. They are not durable item state, so retaining them
	// would prevent a returned stackable from merging with the same item in the
	// recipient's real inventory page.
	delete(extra, "mailbox_pvf_kind")
	delete(extra, "mailbox_equipment_type")
	delete(extra, "mailbox_pvf_path")
	// 时装（皮肤/光环）的创建期徽章孔由 dnfbridge 按运行时 PVF 声明给出向量。
	// 当前客户端的装扮栏(list 1)行把孔位放在固定 0x77 行之后的可选 blob 里
	// （该 blob 由 dnfbridge 从 Extra 的 avatar_socket_data 生成），所以这里必须
	// 写进 Extra；烘进固定行字节客户端不会读，收到的时装就没有孔。
	if len(resolution.AvatarSocketData) > 0 {
		if extra == nil {
			extra = make(map[string]string, 1)
		}
		extra["avatar_socket_data"] = hex.EncodeToString(resolution.AvatarSocketData)
	}
	// 附件自己没有实例到期时间时（GM 面板邮件就是这样），用 dnfbridge 按运行时 PVF
	// 算好的到期投影补上：当前客户端把"到期时间为 0"渲染成"已超过使用期限"，而
	// 附件带着正确到期时间的邮件不会走到这里（不覆盖已有实例时间戳）。
	if attachment.ExpireAt.IsZero() && !resolution.ExpireAt.IsZero() {
		attachment.ExpireAt = resolution.ExpireAt
	}
	for key, value := range resolution.DefaultExtra {
		if value == "" {
			continue
		}
		if extra == nil {
			extra = make(map[string]string, len(resolution.DefaultExtra))
		}
		if _, exists := extra[key]; !exists {
			extra[key] = value
		}
	}
	if resolution.UsablePeriodDays > 0 && !attachment.ExpireAt.IsZero() {
		if extra == nil {
			extra = make(map[string]string, 2)
		}
		// The resolver's default describes a new grant. A claim transfers an
		// existing instance, so its durable fields and projected row must agree.
		expires := strconv.FormatInt(attachment.ExpireAt.Unix(), 10)
		extra["expire_time"] = expires
		extra["expire_unix"] = expires
	}
	stack := dnfrepo.ItemStack{
		ItemID:   attachment.ItemID,
		Count:    count,
		Bind:     attachment.Bind,
		ExpireAt: attachment.ExpireAt,
		RawEntry: append([]byte(nil), attachment.RawEntry...),
		Extra:    extra,
	}
	return stack
}

func mailboxSlotKey(listType byte, slot int16) string {
	return fmt.Sprintf("%d:%d", listType, slot)
}

// canMailStack 判断领取到的堆栈能不能并进已有那一行。
//
// 判据只看"会不会改变道具语义"的东西：物品、绑定、实例到期时间，以及 PVF 身份键
// （pvf_path / item_kind / stackable_type / expire_time / expire_unix）。
// 发放来源之类的记账键（source / last_grant_source / cera_package_source_item /
// booster_source_item …）和缓存的 0x77 行镜像（RawEntry）都不参与：
//
// 现场（角色 3，2026-09-24）：更名卡停在 `7:190`（点券套餐发放：带 raw 行 + 6 个记账键）
// 与 `7:191`（邮件领取：只有身份键）两行里，玩家必须手动把两张卡拖到一起；
// 爆竹同理（`0:65` 有 raw 行 → 领到的 5 个另开一格）。原来要求
// `reflect.DeepEqual(Extra)` + 两边 raw 都为空，等于"只有同一路径发出来的才会堆叠"。
//
// 判据本体在 dnfrepo.CanMergeItemStack（仓库层唯一实现）：邮件、背包随机奖励、盒子礼包、
// 点券/套餐发放共用同一套，避免各自漂移（宠物饲料在套餐路径被拆行就是因为那条路径
// 另写了一套以"PVF 必须有 [stack limit]"为前提的合并逻辑）。
func canMailStack(left, right dnfrepo.ItemStack) bool {
	return dnfrepo.CanMergeItemStack(left, right)
}

func nextMailID(mails map[string]dnfrepo.MailRecord) (string, error) {
	var max uint64
	for key := range mails {
		value, err := strconv.ParseUint(strings.TrimSpace(key), 10, 32)
		if err != nil {
			continue
		}
		if value > max {
			max = value
		}
	}
	if max >= math.MaxUint32 {
		return "", ErrMailboxInvalidRequest
	}
	return strconv.FormatUint(max+1, 10), nil
}

func mailboxSendSource(req SendRequest) string {
	if len(req.Attachments) > 1 {
		return "op315"
	}
	return "op94"
}

func currentMailboxPostage(gold int64, attachmentCount int, attachmentValue int64) (int64, error) {
	if gold < 0 || attachmentCount < 0 || attachmentValue < 0 {
		return 0, ErrMailboxInvalidRequest
	}
	if attachmentCount > (math.MaxInt64-100)/1000 {
		return 0, ErrMailboxInvalidRequest
	}
	postage := int64(100 + attachmentCount*1000)
	for _, surcharge := range []int64{gold / 20, attachmentValue / 20} {
		if postage > math.MaxInt64-surcharge {
			return 0, ErrMailboxInvalidRequest
		}
		postage += surcharge
	}
	return postage, nil
}

func mailExpired(mail dnfrepo.MailRecord, now time.Time) bool {
	if mailboxMailSaved(mail) || now.IsZero() {
		return false
	}
	expiresAt := mail.ExpireAt
	if expiresAt.IsZero() && !mail.CreatedAt.IsZero() {
		expiresAt = mail.CreatedAt.Add(mailboxNormalLifetime)
	}
	return !expiresAt.IsZero() && now.After(expiresAt)
}

func mailboxMailSaved(mail dnfrepo.MailRecord) bool {
	return strings.EqualFold(strings.TrimSpace(mail.Metadata["mailbox_saved"]), "true")
}

func sortMailboxMails(mails []dnfrepo.MailRecord) {
	sort.SliceStable(mails, func(i, j int) bool {
		left, right := mails[i], mails[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		leftID, leftErr := strconv.ParseUint(left.MailID, 10, 32)
		rightID, rightErr := strconv.ParseUint(right.MailID, 10, 32)
		if leftErr == nil && rightErr == nil {
			return leftID < rightID
		}
		return left.MailID < right.MailID
	})
}

func cloneMailboxMail(in dnfrepo.MailRecord) dnfrepo.MailRecord {
	out := in
	out.Attachments = append([]dnfrepo.MailAttachment(nil), in.Attachments...)
	for index := range out.Attachments {
		out.Attachments[index].RawEntry = append([]byte(nil), in.Attachments[index].RawEntry...)
		out.Attachments[index].Extra = cloneStringMap(in.Attachments[index].Extra)
	}
	out.Metadata = cloneStringMap(in.Metadata)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func characterIDFromRequest(reqCharacterID uint16) (string, error) {
	if reqCharacterID == 0 {
		return "", ErrMailboxCharacterMissing
	}
	return strconv.FormatUint(uint64(reqCharacterID), 10), nil
}

func mailboxErrorCode(err error) byte {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrMailboxRecipientMissing):
		return 2
	case errors.Is(err, ErrMailboxMailNotFound):
		return 3
	case errors.Is(err, ErrMailboxAlreadyClaimed):
		return 4
	case errors.Is(err, ErrMailboxExpired):
		return 5
	case errors.Is(err, ErrMailboxInventoryFull):
		return 6
	case errors.Is(err, ErrMailboxGoldInsufficient), errors.Is(err, ErrMailboxAssetMismatch):
		return 7
	case errors.Is(err, ErrMailboxSelfRecipient):
		return 7
	case errors.Is(err, ErrMailboxSavedFull):
		return 22
	case errors.Is(err, ErrMailboxItemResolverMissing), errors.Is(err, ErrMailboxSpecialUnsupported):
		return 7
	default:
		return 1
	}
}

func isMissingRepositoryError(err error) bool {
	return errors.Is(err, ErrMailboxRepoMissing) ||
		errors.Is(err, ErrMailboxAssetsUOW) ||
		errors.Is(err, dnfrepo.ErrMailboxAssetTransactionUnavailable)
}
