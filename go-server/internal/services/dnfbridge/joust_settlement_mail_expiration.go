package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentJoustSettlementMailSource = "joust_settlement_multiplier_return"

// reconcileCurrentJoustSettlementMailAttachments repairs the previously
// created, still-claimable joust reward mails that had no item-instance
// expiration. The current client treats that missing timestamp as the retired
// event date even though the active PVF supplies a future deadline.
func (s *Service) reconcileCurrentJoustSettlementMailAttachments(
	ctx context.Context,
	characterID uint16,
) (int, error) {
	if s == nil || characterID == 0 {
		return 0, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.MailboxAssets == nil {
		return 0, dnfrepo.ErrRepoMissing
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return 0, err
	}
	characterKey := strconv.FormatUint(uint64(characterID), 10)
	now := s.gameplayNow().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	changed := 0
	err = repositories.MailboxAssets.WithinMailboxAssets(ctx, characterKey, characterKey, func(
		_ dnfrepo.CharacterRepository,
		_ dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		mailbox, found, loadErr := mailboxes.Load(ctx, characterKey)
		if loadErr != nil || !found {
			return loadErr
		}
		mailbox = dnfrepo.CloneMailbox(mailbox)
		for mailID, mail := range mailbox.Mails {
			if mail.Claimed || mail.Deleted ||
				!strings.EqualFold(strings.TrimSpace(mail.Metadata["source"]), currentJoustSettlementMailSource) {
				continue
			}
			mailChanged := false
			for index := range mail.Attachments {
				attachment := &mail.Attachments[index]
				if attachment.ItemID <= 0 || attachment.Count <= 0 || attachment.ItemID > int64(^uint32(0)) {
					return fmt.Errorf("joust settlement mail=%s attachment=%d is invalid", mailID, index)
				}
				definition, resolveErr := catalog.ResolveItem(uint32(attachment.ItemID))
				if resolveErr != nil {
					return fmt.Errorf("resolve joust settlement mail=%s attachment=%d item=%d: %w", mailID, index, attachment.ItemID, resolveErr)
				}
				if definition.Kind != dungeonDropItemStackable ||
					(!definition.ExpirationDate.IsZero() && !definition.ExpirationDate.After(now)) {
					return fmt.Errorf("joust settlement mail=%s attachment=%d item=%d is not a live stackable", mailID, index, attachment.ItemID)
				}
				if definition.ExpirationDate.IsZero() || attachment.ExpireAt.Equal(definition.ExpirationDate) {
					continue
				}
				attachment.ExpireAt = definition.ExpirationDate
				mailChanged = true
				changed++
			}
			if mailChanged {
				mailbox.Mails[mailID] = mail
			}
		}
		if changed == 0 {
			return nil
		}
		mailbox.UpdatedAt = now
		return dnfrepo.SaveMailboxFields(ctx, mailboxes, mailbox, dnfrepo.MailboxFieldMails)
	})
	return changed, err
}

func (s *Service) reconcileCurrentJoustSettlementMailBeforeOpen(session *gameSession) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	changed, err := s.reconcileCurrentJoustSettlementMailAttachments(ctx, session.selectedCharacterID)
	if err != nil {
		s.logGameEvent(session, "game-joust-settlement-mail-expiration-repair-deferred", "error", err)
		return
	}
	if changed > 0 {
		s.logGameEvent(session, "game-joust-settlement-mail-expiration-repaired", "attachments", changed)
	}
	repaired, err := s.reconcileCurrentMailAttachmentExpirations(ctx, session.selectedCharacterID)
	if err != nil {
		s.logGameEvent(session, "game-mail-attachment-expiration-repair-deferred", "error", err)
		return
	}
	if repaired > 0 {
		s.logGameEvent(session, "game-mail-attachment-expiration-repaired", "attachments", repaired)
	}
}

// reconcileCurrentMailAttachmentExpirations repairs **every** still-claimable mail
// attachment that carries no item-instance timestamp while its runtime PVF item
// declares a future absolute deadline.
//
// Why: the current client renders an attachment row whose raw+0x38 deadline is
// zero as the retired event date, i.e. "已超过使用期限", even though the PVF
// supplies a future `[expiration date]`. The joust settlement mail hit this and
// got its own repair (reconcileCurrentJoustSettlementMailAttachments); GM panel
// mails hit exactly the same thing — the 春节欢庆爆竹 (490701730) mailed to a
// character shows "已超过使用期限" in the mailbox while its PVF deadline is
// 2028-04-05 and the claimed stack lands with that same deadline.
//
// The repair only ever writes the PVF's own deadline (or, for a retired absolute
// [expiration date] that is already in the past, the operator's renewal horizon
// 2028-01-01 — see currentPVFRetiredExpirationRenewalDate; that covers the
// retired 完成券/活动礼包 whose PVF date stopped in 2017 and used to be skipped
// here, leaving the attachment row showing 已超过使用期限). Unresolvable items
// and non-stackables (equipment/pets keep their own instance semantics) are
// skipped untouched. A definition that only declares a relative `[usable period]`
// is resolved through the same currentPVFItemDefinitionForGrantAt helper every
// other grant path uses, with the mail's own CreatedAt as the anchor, so the
// repair is idempotent.
func (s *Service) reconcileCurrentMailAttachmentExpirations(
	ctx context.Context,
	characterID uint16,
) (int, error) {
	if s == nil || characterID == 0 {
		return 0, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.MailboxAssets == nil {
		return 0, dnfrepo.ErrRepoMissing
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return 0, err
	}
	characterKey := strconv.FormatUint(uint64(characterID), 10)
	now := s.gameplayNow().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	changed := 0
	anyMailChanged := false
	err = repositories.MailboxAssets.WithinMailboxAssets(ctx, characterKey, characterKey, func(
		_ dnfrepo.CharacterRepository,
		_ dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		mailbox, found, loadErr := mailboxes.Load(ctx, characterKey)
		if loadErr != nil || !found {
			return loadErr
		}
		mailbox = dnfrepo.CloneMailbox(mailbox)
		for mailID, mail := range mailbox.Mails {
			if mail.Claimed || mail.Deleted {
				continue
			}
			mailChanged := false
			for index := range mail.Attachments {
				attachment := &mail.Attachments[index]
				if attachment.ItemID <= 0 || attachment.Count <= 0 || attachment.ItemID > int64(^uint32(0)) {
					continue
				}
				definition, resolveErr := catalog.ResolveItem(uint32(attachment.ItemID))
				if resolveErr != nil {
					// 未收录/不可解析的道具保持原样：这里是修复，不是校验。
					continue
				}
				container := currentPVFItemTargetContainer(definition)
				if definition.Kind == dungeonDropItemEquipment && !container.Avatar && !container.PetBody && !container.PetArtifact {
					if dnfmail.RestoreEquipmentAttachmentInstanceExtra(attachment) {
						mailChanged = true
						changed++
					}
				}
				// 退役绝对日期策略（见 currentPVFRetiredExpirationRenewalDate）：
				// 附件行 0x38 写的就是下面的 expiration，所以先按统一策略把"已经过去的
				// 绝对 [expiration date]"续期到 2028-01-01。邮件/背包/仓库/领取共用同一个
				// 常量与同一个判据，同一道具在各处得到的到期时间必然一致；重复修补幂等。
				definition.ExpirationDate = currentPVFRenewedRetiredExpirationDate(definition.ExpirationDate, now)
				if initializeCurrentSystemMailEquipmentSeal(mail, attachment, definition) {
					mailChanged = true
					changed++
				}
				// 附件行是按"道具类型"分叉编码的：当前客户端的 [creature] 附件走 type-26
				// 分支，会先多读一个 dword + 一个 flag。GM 面板发的邮件不带任何 Extra，
				// 于是那个分支不触发、客户端按错误长度解析整封邮件列表 —— 线上实测就是
				// "点开邮箱客户端卡死"。这里在打开邮箱前按运行时 PVF 把类型补齐。
				if currentMailAttachmentTypeExtraMissing(attachment.Extra) {
					if kind, token, ok := currentMailAttachmentTypeTokens(definition); ok {
						if attachment.Extra == nil {
							attachment.Extra = make(map[string]string, 2)
						}
						if attachment.Extra["mailbox_pvf_kind"] == "" {
							attachment.Extra["mailbox_pvf_kind"] = kind
							mailChanged = true
						}
						if attachment.Extra["mailbox_equipment_type"] == "" {
							attachment.Extra["mailbox_equipment_type"] = token
							mailChanged = true
						}
					}
				}
				if definition.Kind != dungeonDropItemStackable {
					// 装备/时装：附件缺耐久证据时，按运行时 PVF 的 [durability] 补
					// durability + max_durability（与掉落拾取、翻牌奖励、点券套餐发放
					// 写进堆栈的耐久同源）。不补的话，存量旧 GM 邮件领取后仍是 0 耐久，
					// 修理路径会以"缺证据"失败关闭 —— 这里修的是证据本身，不是放宽校验。
					if definition.Durability > 0 && currentMailAttachmentDurabilityExtraMissing(attachment.Extra) {
						attachment.Extra = mergeCurrentMailAttachmentDurabilityExtra(attachment.Extra, definition)
						mailChanged = true
						changed++
					}
					continue
				}
				// P7b：GM 面板发放的到期覆盖（expiration_source=gm_override）不得被这里
				// 按 PVF 重算回去 —— 这个循环正是"重登后 GM 给的 30 天变回 PVF 期限"的
				// 现场（P7 报告 §5.2 点名的重算点）。判定复用消费端同一份实现
				// （item_expiration_projection.go 的 currentGMExpirationOverride*），
				// 同一个开关 DNFBRIDGE_GM_MAIL_OPTIONS：off 档这里连标记都不看，
				// 附件修补与 P7b 之前逐字节相同。
				//
				// 位置有意放在"非堆叠物 continue"之后：装备/时装（宠物同理）本来就不
				// 走下面的 PVF 重算，所以带不带标记都一个字节不动（P7 §5.2 的那条性质
				// 由 TestGMExpirationOverrideLeavesEquipmentAttachmentRepairAlone 钉死）。
				if currentGMExpirationOverrideEnabled() && currentGMExpirationOverrideMarked(attachment.Extra) {
					if currentGMExpirationOverrideInstanceExpire(attachment.Extra, attachment.ExpireAt) != 0 {
						// 保留 GM 期限。**含"GM 期限已经过去"的情形**：那是操作者要的
						// 语义，不能借 PVF 的期限把它静默续活（本文件下面那句
						// `!expiration.After(now)` 是 PVF 侧的口径，不适用于 GM 覆盖）。
						continue
					}
					// 标记在、期限读不出来：**回退到今天的行为**（继续往下按 PVF 补），
					// 但绝不静默 —— 记一条具名事件（reason=missing_deadline）。
					reportCurrentGMExpirationOverrideInvalid("missing_deadline", attachment.ItemID, attachment.Extra, attachment.ExpireAt)
				}
				expiration := definition.ExpirationDate
				if definition.UsablePeriodDays > 0 && !definition.DailyDeleteItem {
					// Match grant resolution: a declared relative period owns its
					// instance date even when the document also has an event date.
					expiration = time.Time{}
				}
				if expiration.IsZero() && definition.UsablePeriodDays > 0 && !definition.DailyDeleteItem &&
					!currentMailboxOriginalAttachmentExpiry(*attachment).IsZero() {
					// A relative template cannot replace an existing instance's
					// deadline before the claim owner has transferred that instance.
					continue
				}
				if expiration.IsZero() {
					// 相对 [usable period]：这份 PVF 没有绝对 [expiration date]，
					// 直接看 ExpirationDate 会判成"永久"而漏到 0。整批"完成类型"券
					// 正是这种声明（Lv1~90主线任务完成券 10164630 =
					// stackable/10164001/10164630.stk、Lv1~90主线/成就任务完成券
					// 10301410 = stackable/10301001/10301410.stk，均为
					// [stackable type] `[etc]` + [usable period] 1），GM 面板邮件发出来
					// 的附件因此一直是 0，客户端在邮箱里就显示"已超过使用期限"。
					//
					// 锚点是邮件自己的 CreatedAt（= 该附件所属实例的发放时刻），不是 now，
					// 否则每次登录都会把期限往后推（"静默续期"）；缺 CreatedAt 的旧邮件
					// 保持原样，绝不凭空造时间。
					if mail.CreatedAt.IsZero() || !dnfmail.MailCreationAnchorsAttachment(mail) {
						continue
					}
					granted, grantErr := currentPVFItemDefinitionForGrantAt(definition, mail.CreatedAt.UTC())
					if grantErr != nil {
						continue
					}
					expiration = granted.ExpirationDate
				}
				if expiration.IsZero() || !expiration.After(now) {
					continue
				}
				if attachment.ExpireAt.Equal(expiration) {
					continue
				}
				attachment.ExpireAt = expiration
				mailChanged = true
				changed++
			}
			if mailChanged {
				mailbox.Mails[mailID] = mail
				anyMailChanged = true
			}
		}
		// 保存门禁看的是"有没有邮件被修补"，不是"修补计数是否为 0"：类型 Extra
		// （mailbox_pvf_kind / mailbox_equipment_type）这类修补不计入 changed，
		// 过去因此被这句丢掉 —— 于是 GM 面板发出的 [creature] 附件的老邮件，
		// 登录期补齐类型的修复实际上从未落库。
		if !anyMailChanged {
			return nil
		}
		mailbox.UpdatedAt = now
		return dnfrepo.SaveMailboxFields(ctx, mailboxes, mailbox, dnfrepo.MailboxFieldMails)
	})
	return changed, err
}

// currentMailAttachmentTypeExtraMissing 报告附件是否还缺"类型"Extra。
func currentMailAttachmentTypeExtraMissing(extra map[string]string) bool {
	if extra == nil {
		return true
	}
	return extra["mailbox_pvf_kind"] == "" || extra["mailbox_equipment_type"] == ""
}

// currentMailAttachmentTypeTokens 把运行时 PVF 的道具类型换算成 mail 模块使用的
// 两个字符串：kind 是 stackable/equipment 这类原值，token 是去掉方括号与反引号、
// 转小写的类型名（例如 [creature] -> creature）——客户端按 token 决定附件行分支。
func currentMailAttachmentTypeTokens(definition dungeonDropItemDefinition) (string, string, bool) {
	kind := strings.TrimSpace(string(definition.Kind))
	token := definition.EquipmentType
	if strings.TrimSpace(token) == "" {
		token = definition.StackableType
	}
	token = strings.ToLower(strings.Trim(strings.TrimSpace(token), "[]` "))
	if kind == "" && token == "" {
		return "", "", false
	}
	return kind, token, true
}
