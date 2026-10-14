// 本文件把已对齐的 DNF 旧客户端命令转交给模块化 handler。
// bridge 只负责分流和发包，不在这里补业务状态或伪造成功结果。
package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/avatartitle"
	"longheng.io/server/internal/modules/dnf/cargo"
	"longheng.io/server/internal/modules/dnf/character"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/mail"
	"longheng.io/server/internal/modules/dnf/packageitem"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/pet"
	"longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

var errAlignedPostCommitProjectionDeferred = errors.New("aligned post-commit projection deferred")

var gameAlignedRegistry = alignedcmd.NewRegistry(
	avatartitle.NewHandler(),
	cargo.NewHandler(),
	character.NewHandler(),
	dungeoncmd.NewHandler(),
	inventory.NewHandler(),
	itemlock.NewHandler(),
	mail.NewHandler(),
	packageitem.NewHandler(),
	party.NewHandler(),
	pet.NewHandler(),
	quest.NewHandler(),
	raid.NewHandler(),
	skillcmd.NewHandler(),
)

// handleAlignedGameCommand 把协议号已对齐、但尚未落业务实现的命令归入模块边界。
// 当前只记录分流证据，不伪造成功 ACK；具体回包必须等对应模块按 EXE/日志补齐。
func (s *Service) handleAlignedGameCommand(session *gameSession, cmd byte, typ uint16, body []byte) (bool, error) {
	if handled, err := s.handleCurrentMonsterCardCommand(session, typ, body); handled || err != nil {
		return handled, err
	}
	if session != nil && dnfenum.CmdPacket(typ) == dnfenum.CmdPacketMoveItemspace {
		// Keep the durable move, ACK, changed-slot rows and immediate appearance
		// projection in the same order as a taken 200ms appearance callback.
		// The queue never invokes its callback synchronously while scheduling.
		session.equipmentProjectionMu.Lock()
		defer session.equipmentProjectionMu.Unlock()
	}
	if typ == uint16(dnfenum.CmdPacketUseVendingMachine) {
		return true, s.handleCurrentVendingMachine(session, body)
	}
	// 锦标赛族上行 `DIE_AI_CHARACTER`（厂商 CmdPacketTypeA21 0x0090 = 144）。
	// **开关关闭时 `handleCurrentTournamentDieAICharacter` 返回 (false, nil)**：
	// 不记日志、不发包、不改状态，控制流逐字节回到改动前那条匿名桶路径。
	// 开关打开时也只做只读观测（op144 body 布局在本仓未确证，不推状态机）。
	//
	// 取证结论（本批）：厂商全树对 CMD 144 **没有** parser/handler，赛制推进走的是
	// CMD `DIE_MONSTER`（0x0027 = 39）。这里保留只读观测只为"到达即留证"，
	// **赛制不依赖它**（见 dungeon_tournament_flow.go 顶部）。
	if handled, err := s.handleCurrentTournamentDieAICharacter(session, typ, body); handled || err != nil {
		return handled, err
	}
	// 锦标赛选奖上行 CMD 458 / 459（厂商 `Network\Parsers\Dungeon\DungeonCommandParser.cs:54-85`）。
	// 开关关闭、或本会话没有等待选择的锦标赛奖励态时返回 (false, nil)：
	// 不记日志、不发包、不改状态，控制流逐字节回到改动前那条路径。
	if handled, err := s.handleCurrentTournamentRewardCommand(session, typ, body); handled || err != nil {
		return handled, err
	}
	if typ == uint16(dnfenum.CmdPacketUseStackable) {
		if handled, err := s.handleCurrentExpertJobRecipeLearning(session, body); handled || err != nil {
			return handled, err
		}
	}
	// 任务完成券（`[gm quest clear]` / `[lv clear]`）右键用的是 op515 action 167；
	// 这条动作号此前没有 handler，客户端因此等不到任何回包（"右键使用无反应"）。
	if typ == uint16(dnfenum.CmdPacketUseStackableAction) {
		if handled, err := s.handleCurrentFatiguePotionUse(session, body); handled || err != nil {
			return handled, err
		}
		if handled, err := s.handleCurrentQuestClearCouponUse(session, body); handled || err != nil {
			return handled, err
		}
	}
	// 同一个右键动作里客户端还会补发 op176 `//lvclear <N>`。它是通用 GM 调试
	// 命令行通道，只做只读观察，绝不作为清任务的授权（见 quest_coupon_clear.go）。
	if typ == uint16(dnfenum.CmdPacketGmdebugCommand) {
		if handled, err := s.handleCurrentQuestClearCouponDebugCommand(session, body); handled || err != nil {
			return handled, err
		}
	}
	if handled, err := s.handleOnlineItemTradeCommand(session, typ, body); handled || err != nil {
		return handled, err
	}
	if handled, err := s.handleOnlineRaidCommand(session, typ, body); handled || err != nil {
		return handled, err
	}
	if handled, err := s.handleOnlineSocialCommand(session, typ, body); handled || err != nil {
		return handled, err
	}
	if handled, err := s.handleOnlinePartyCommand(session, typ, body); handled || err != nil {
		return handled, err
	}
	// 队伍期的 P2P 打洞上报（op966）只观察、不回包：改动前这条 opcode 没有归属，
	// 现场 `msg=966 len=23`（队友在门口按空格的窗口里出现 3 次、op15 一次都没有）
	// 一路落进匿名 "unhandled latest game command" 桶被静默丢弃。
	// 放在这里与 `handleOnlinePartyCommand` 同层（队伍族），且在取仓储/加队伍锁之前，
	// 所以这条上报不会触发任何 DB 或队伍状态机。见 p2p_hole_punching_report.go。
	if handled, err := s.observeCurrentP2pHolePunchingReport(session, typ, body); handled || err != nil {
		return handled, err
	}
	repos, _ := s.repositoryGroup()
	accountID := s.accountIDForSession(session)
	if dnfenum.CmdPacket(typ) == dnfenum.CmdPacketMailboxOpen {
		s.reconcileCurrentJoustSettlementMailBeforeOpen(session)
	}
	// 领取（op95）也要跑同一次附件修补（默认档；见 mailbox_claim_type_repair.go）。
	//
	// 为什么：op96 打开邮箱时跑的修补只覆盖"那一刻已经在盘里的信"。GM 面板/系统
	// 在玩家**邮箱已经开着**的时候投递的新信从来没被修补过，于是它的 op97
	// (class0/0x61) 摘要行缺少 `[creature]` 分支多读的 5 个字节
	// （mail/body.go `mailboxAttachmentRequiresClass26` 判的是 Extra 里的
	// `mailbox_equipment_type`），客户端按错行宽解析整页邮件 ⇒ 闪退。
	// 这条修补把类型 token 按**运行时 PVF** 补进 Extra，使行宽与客户端的
	// 类型判定同源。
	if s.mailboxClaimTypeRepairEnabled() &&
		dnfenum.CmdPacket(typ) == dnfenum.CmdPacketMailboxExtractItem {
		s.reconcileCurrentJoustSettlementMailBeforeOpen(session)
	}
	var skillCatalog *dnfskill.Table
	var initialSkillLevels map[uint16]int
	var skillPointBaseline *dnfrepo.SkillPointState
	var equipmentPlacement alignedcmd.EquipmentPlacementValidator
	var petHatchResolver alignedcmd.PetHatchResolver
	if alignedSkillOwnerRulesRequired(dnfenum.CmdPacket(typ)) {
		if dnfenum.CmdPacket(typ) == dnfenum.CmdPacketSkillInit {
			skillCatalog, initialSkillLevels, skillPointBaseline = s.skillResetOwnerRules(context.Background(), repos, session.selectedCharacterID)
		} else {
			skillCatalog, initialSkillLevels, skillPointBaseline = s.skillOwnerRules(context.Background(), repos, session.selectedCharacterID)
		}
	}
	petHatchResolver, petCatalogErr := s.alignedPetHatchResolverForCommand(dnfenum.CmdPacket(typ))
	if petCatalogErr != nil {
		s.logPacketEvent("game-aligned-pet-hatch-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", petCatalogErr)
		s.logInfo("dnfbridge pet hatch resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", petCatalogErr)
	}
	enchantBeadResolver, enchantCatalogErr := s.alignedEnchantBeadResolverForCommand(dnfenum.CmdPacket(typ))
	if enchantCatalogErr != nil {
		s.logPacketEvent("game-aligned-enchant-bead-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", enchantCatalogErr)
		s.logInfo("dnfbridge enchant bead resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", enchantCatalogErr)
	}
	amplifyItemResolver, amplifyItemCatalogErr := s.alignedAmplifyItemResolverForCommand(dnfenum.CmdPacket(typ))
	if amplifyItemCatalogErr != nil {
		s.logPacketEvent("game-aligned-amplify-item-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", amplifyItemCatalogErr)
		s.logInfo("dnfbridge amplify item resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", amplifyItemCatalogErr)
	}
	randomOptionResolver, randomOptionCatalogErr := s.alignedRandomOptionResolverForCommand(dnfenum.CmdPacket(typ))
	if randomOptionCatalogErr != nil {
		s.logPacketEvent("game-aligned-random-option-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", randomOptionCatalogErr)
		s.logInfo("dnfbridge random option resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", randomOptionCatalogErr)
	}
	upgradeTicketResolver, upgradeTicketCatalogErr := s.alignedUpgradeTicketResolverForCommand(dnfenum.CmdPacket(typ))
	if upgradeTicketCatalogErr != nil {
		s.logPacketEvent("game-aligned-upgrade-ticket-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", upgradeTicketCatalogErr)
		s.logInfo("dnfbridge upgrade ticket resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", upgradeTicketCatalogErr)
	}
	upgradePolicyResolver := s.alignedUpgradePolicyResolver()
	// 成功率 buff 药水（强化的奥秘 / 增幅秘药 …）：op44/op251 使用时把 buff 记进角色
	// 记录，op50 掷点时按模式读账。见 upgrade_rate_buff.go。
	upgradeRateBuffResolver, upgradeRateBuffErr := s.alignedUpgradeRateBuffResolverForCommand(dnfenum.CmdPacket(typ))
	if upgradeRateBuffErr != nil {
		s.logPacketEvent("game-aligned-upgrade-rate-buff-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", upgradeRateBuffErr)
		s.logInfo("dnfbridge upgrade rate buff resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", upgradeRateBuffErr)
	}
	// op50「失败即摧毁」档的保护券真源（`[action type]` = `[protect equipment]` /
	// `[amplify protect equipment]`）。见 upgrade_protect_ticket_pvf.go：只有 op50
	// 会拿到这个解析器，其它命令连 PVF 目录都不开。
	upgradeProtectTicketResolver, upgradeProtectCatalogErr := s.alignedUpgradeProtectTicketResolverForCommand(dnfenum.CmdPacket(typ))
	if upgradeProtectCatalogErr != nil {
		s.logPacketEvent("game-aligned-upgrade-protect-ticket-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", upgradeProtectCatalogErr)
		s.logInfo("dnfbridge upgrade protect ticket resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", upgradeProtectCatalogErr)
	}
	magicBoxResolver, magicBoxRewardResolver, magicBoxCatalogErr := s.alignedMagicBoxResolversForCommand(dnfenum.CmdPacket(typ))
	if magicBoxCatalogErr != nil {
		s.logPacketEvent("game-aligned-magic-box-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", magicBoxCatalogErr)
		s.logInfo("dnfbridge magic box resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", magicBoxCatalogErr)
	}
	// 开盒子（op208/op1128）在 changed_only 档下要知道"这次开盒写到了哪些容器"：
	// 把 bridge 自己的奖励模板解析器包一层，packageitem 每解析一个**实际发放**的
	// 奖励就记一次目标容器。默认 current 档不装记录、不写任何会话字段。
	// 证据与残余不确定见 box_open_container_refresh.go。
	magicBoxRewardResolver = s.armBoxOpenContainerRecord(session, magicBoxRewardResolver)
	// 形状开关 delta 档（默认）下还要装一次"发放前槽位快照"：packageitem 的写入发生在它
	// 自己的事务里（`internal/modules/dnf/**` 不属于本次改动范围），bridge 只能靠提交前后的
	// 仓储差分拿到**精确的** (list, slot) 集合，用它替代"回包后 5 个整容器"。
	// containers 档下一次仓储都不多读。见 item_grant_notice.go。
	itemGrantSnapshot := s.armItemGrantSnapshot(session, repos, dnfenum.CmdPacket(typ))
	premiumContractResolver, premiumCatalogErr := s.alignedPremiumContractResolverForCommand(dnfenum.CmdPacket(typ))
	if premiumCatalogErr != nil {
		s.logPacketEvent("game-aligned-premium-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", premiumCatalogErr)
		s.logInfo("dnfbridge premium contract resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", premiumCatalogErr)
	}
	damageFontResolver, damageFontCatalogErr := s.alignedDamageFontResolverForCommand(dnfenum.CmdPacket(typ))
	if damageFontCatalogErr != nil {
		s.logPacketEvent("game-aligned-damage-font-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", damageFontCatalogErr)
		s.logInfo("dnfbridge damage-font resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", damageFontCatalogErr)
	}
	randomRewardItemResolver, randomRewardCatalogErr := s.alignedRandomRewardItemResolverForCommand(dnfenum.CmdPacket(typ))
	if randomRewardCatalogErr != nil {
		s.logPacketEvent("game-aligned-random-reward-item-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", randomRewardCatalogErr)
		s.logInfo("dnfbridge random-reward-item resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", randomRewardCatalogErr)
	}
	repairCostResolver, repairCostCatalogErr := s.alignedRepairCostResolverForCommand(dnfenum.CmdPacket(typ))
	if repairCostCatalogErr != nil {
		s.logPacketEvent("game-aligned-repair-cost-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", repairCostCatalogErr)
		s.logInfo("dnfbridge repair cost resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", repairCostCatalogErr)
	}
	mailboxItemResolver, mailboxCatalogErr := s.alignedMailboxItemResolverForCommand(dnfenum.CmdPacket(typ))
	if mailboxCatalogErr != nil {
		s.logPacketEvent("game-aligned-mailbox-item-resolver-unavailable",
			"conn_id", session.connID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", mailboxCatalogErr)
		s.logInfo("dnfbridge mailbox item resolver unavailable",
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"error", mailboxCatalogErr)
	}
	// loginWearWindowOpen 只在 op19 上求值，并透传到穿戴 owner。
	// 开关 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 默认关闭时为 false。
	// 形状作用域 + 可重复命中都在 equip_login_wear_family_window.go 里收口：
	//   - 只有"复述穿戴态"那一族形状的 op19 才消费/延续窗口（普通背包 op19 不吃窗口）；
	//   - 窗口在保护文件定义的 5s 时长内对该族可重复命中（现场 61423 判 noop 之后
	//     61575 逐字节同形却被真卸下，就是一次性消费造成的）。
	// 见 _dist/weapon-unequip-diagnosis.md §3.2 的两个缺口。
	loginWearWindowOpen := false
	// wornSlotClaimOccurrence 是"声明装备槽穿戴态"那一族 op19 在当前登录纪元里
	// **同一装备槽**的第几条（1 起算；0 = 非该族/未计数）。重试档（方案 C，默认）
	// 只吞第 1 条、第 2 条起按厂商口径执行成真·脱下。见 equip_worn_slot_claim_occurrence.go。
	wornSlotClaimOccurrence := 0
	// playerRequestFlagged reads legacy 1 or bounded current-image UI marker 2.
	playerRequestFlagged := false
	currentUIPlayerRequest := false
	// mirrorReequipEcho 是"甲"的判定结果：本条 op19 是我们刚脱下那件的镜像回穿。
	mirrorReequipEcho := false
	if dnfenum.CmdPacket(typ) == dnfenum.CmdPacketMoveItemspace {
		// 「家族 ↔ 分页」闸门（操作者问题清单第 48 条：物品栏可随意拖动至任意类别）。
		// 默认档 `log` 只记事件、包与写库逐字节不变；`enforce` 档在这里直接结束命令，
		// 因此**连登录引导窗口都不会被消费**（被拒的请求没有发生任何真实移动）。
		// 见 item_slot_family_guard.go。
		blocked, guardErr := s.applyItemSlotFamilyGuard(session, repos, body)
		if guardErr != nil {
			return true, guardErr
		}
		if blocked {
			return true, nil
		}
		// 玩家请求标志位（旧 1 / 有界当前 UI 2）：**玩家亲手发起的移动** ⇒ 直接绕过
		// "复述 vs 真搬运"那一族门禁，交给反转端点兜底按厂商口径执行真·脱下。
		// tail==0 的包（含客户端进场景后的手述批、金币/宠物/背包互换）走原路，行为逐字节不变。
		// 见 equip_player_request_flag.go。
		playerRequestFlagged = s.op19PlayerRequestFlagged(body)
		currentUIPlayerRequest = s.op19CurrentUIPlayerRequest(body)
		if playerRequestFlagged {
			// 旧标志 1 保留静默窗（默认 400 ms）；可靠的当前 UI 标志 2 立即执行。
			// 这样一串被误打标的自动突发最多只执行**第一条**（最坏 1 件，而不是 5 件）。
			// 挂起由读循环用临时读截止结算，见 equip_player_flag_quiet_window.go。
			if window := op19PlayerFlagQuietWindow(); !currentUIPlayerRequest && window > 0 &&
				!session.op19QuietResolving && !session.hasOp19QuietPending() {
				session.pendOp19QuietBody(body)
				s.logPacketEvent("equip-move/player-request-flag",
					"conn_id", session.connID,
					"decision", "pend",
					"reason", "quiet-window",
					"tail", 1,
					"quiet_ms", window.Milliseconds(),
					"switch_env", op19PlayerFlagQuietEnv)
				return true, nil
			}
			// 不参与逐槽账本：带标志的包不是复述族的一员
			// （这样 tail==0 包的窗口/occurrence 判据不会被它打乱）。
			wornSlotClaimOccurrence = 2
			flagTail := byte(1)
			flagEvidence := "legacy_client_module8_body26_eq_1"
			if currentUIPlayerRequest {
				flagTail = op19CurrentUIPlayerFlag
				flagEvidence = "current_image_manual_ui_direct_producer_caller"
			}
			s.logPacketEvent("equip-move/player-request-flag",
				"conn_id", session.connID,
				"decision", "apply-vendor",
				"reason", "player-request-flag",
				"tail", flagTail,
				"switch_env", op19PlayerFlagEnv,
				"evidence", flagEvidence)
		} else {
			// 逐槽出现序号（乙：有界窗口 + 窗口内 occurrence 第二道保险）：**只在本函数里记一次**，
			// 后面既喂给桥侧吸收判定，也透传给域侧两条登录闸门。
			// 见 equip_worn_slot_claim_occurrence.go。
			wornSlotClaimOccurrence = s.noteWornSlotClaimOccurrence(session, body)
		}
		session.equipWornSlotClaimOccurrence = wornSlotClaimOccurrence
		// 甲：镜像回穿（我们刚把那件脱下，客户端随即又把它穿回去）⇒ 判 ClientEcho noop。
		// 只看我们自己的落库记录 + 同一件 + 同一槽 + 窗口内的间隔。
		// 见 equip_mirror_requip_echo.go。
		if currentUIPlayerRequest {
			// A directly identified UI action can intentionally re-equip the item
			// just removed. The historical elapsed-time heuristic is ambiguous.
			// Consume its old mirror record so it cannot affect the next action.
			session.mirrorReequipAt = time.Time{}
		} else {
			mirrorReequipEcho = s.mirrorReequipEchoForOp19(session, body)
		}
		loginWearWindowOpen = s.equipLoginWearFamilyWindowOpenForOp19(session, body)
		if wornSlotClaimOccurrence > 1 {
			// 窗口外/窗口内重试（= 真请求）不是复述：域侧三条登录闸门必须惰性，
			// 好让反转端点兜底把它执行成厂商口径的真·脱下。
			loginWearWindowOpen = false
		}
		if currentPetMoveTouchesGrowthState(body) {
			if settleErr := s.settleCurrentPetGrowthClock(session, s.gameplayNow(), "current_exe_op19_before_pet_or_artifact_mutation"); settleErr != nil {
				s.logPacketEvent("game-pet-satiety-before-op19-deferred",
					"conn_id", session.connID,
					"selected_character_id", session.selectedCharacterID,
					"type", typ,
					"reason", "growth_clock_settlement_failed",
					"error", settleErr)
			}
		}
		placementContext, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		validator, placementErr := s.newCurrentEquipmentPlacementValidator(placementContext, accountID, session.selectedCharacterID, repos)
		cancel()
		if placementErr != nil {
			s.logPacketEvent("game-aligned-equipment-placement-validator-unavailable",
				"conn_id", session.connID,
				"account_id", accountID,
				"selected_character_id", session.selectedCharacterID,
				"type", typ,
				"error", placementErr)
		} else {
			equipmentPlacement = func(ctx context.Context, placement alignedcmd.EquipmentPlacementRequest) error {
				return validator.ValidateEquipmentPlacement(ctx, dnfequip.Placement{
					CharacterID:     placement.CharacterID,
					ItemID:          placement.ItemID,
					SourceListType:  placement.SourceListType,
					SourceSlotIndex: placement.SourceSlotIndex,
					TargetSlotIndex: placement.TargetSlotIndex,
				})
			}
		}
	}
	// 567r2：op19 在进入域处理之前，按唯一工厂做一次 missing-only 的容器状态准备。
	// 只对通过既有 class/路由/body/角色边界的 0/1/2 容器移动生效；其余一律不介入
	// （保持旧拒绝语义，不新写 Settings）。非法状态或读失败 → 拒绝本次移动。
	if dnfenum.CmdPacket(typ) == dnfenum.CmdPacketMoveItemspace {
		if err := s.prepareMoveContainerState(session, cmd, typ, repos, body); err != nil {
			s.logWarn("dnfbridge op19 container state rejected",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"err", err)
			return true, nil
		}
	}
	consumptionSnapshot := s.prepareDungeonConsumptionSnapshot(session, repos, typ, body)
	session.party.mu.Lock()
	result, ok, err := gameAlignedRegistry.Route(context.Background(), alignedcmd.Request{
		Command:                      cmd,
		CommandKnown:                 true,
		Opcode:                       typ,
		Body:                         body,
		AccountID:                    accountID,
		SelectedCharacterID:          session.selectedCharacterID,
		Repositories:                 repos,
		EquipmentPlacement:           equipmentPlacement,
		AvatarClassifier:             s.currentCloneAvatarClassifier(),
		NameTagChecker:               func(itemID uint32) bool { return s.currentIsNameTagItem(itemID) },
		PetHatchResolver:             petHatchResolver,
		EnchantBeadResolver:          enchantBeadResolver,
		AmplifyItemResolver:          amplifyItemResolver,
		RandomOptionResolver:         randomOptionResolver,
		UpgradeTicketResolver:        upgradeTicketResolver,
		UpgradePolicyResolver:        upgradePolicyResolver,
		UpgradeProtectTicketResolver: upgradeProtectTicketResolver,
		UpgradeRateBuffResolver:      upgradeRateBuffResolver,
		MagicBoxResolver:             magicBoxResolver,
		MagicBoxRewardItemResolver:   magicBoxRewardResolver,
		RandomRewardItemResolver:     randomRewardItemResolver,
		PremiumContractResolver:      premiumContractResolver,
		// op44/op251 的可用性判据改读运行时 PVF（为零 = 回到库存元数据判据，
		// 解析器装载失败时这里就是 nil 并已记事件）。见 stackable_use_provenance_pvf.go。
		StackableUseProvenanceResolver: s.alignedStackableUseProvenanceResolverForCommand(dnfenum.CmdPacket(typ)),
		DamageFontResolver:             damageFontResolver,
		DamageFontNow:                  s.gameplayNow(),
		RepairCostResolver:             repairCostResolver,
		MailboxItemResolver:            mailboxItemResolver,
		EpicPieceCreditor:              s.alignedEpicPieceCreditorForCommand(dnfenum.CmdPacket(typ), accountID),
		WarehouseCreditor:              s.alignedWarehouseCreditorForCommand(dnfenum.CmdPacket(typ), accountID),
		SkillCatalog:                   skillCatalog,
		InitialSkillLevels:             initialSkillLevels,
		SkillPointBaseline:             skillPointBaseline,
		Party:                          &session.party.state,
		LoginWearWindowOpen:            loginWearWindowOpen,
		EquipmentInventorySlotRefresh:  s.options.currentClientVersion == "official-24.9",
		// 同槽出现序号（乙：有界窗口 / occurrence）：≥2 时域侧两条登录闸门惰性 ⇒ 真·脱下。
		WornSlotClaimOccurrence: wornSlotClaimOccurrence,
		// 甲：镜像回穿 ⇒ 域侧判 ClientEcho noop（最高优先级的闸门）。
		MirrorReequipEcho: mirrorReequipEcho,
		// 玩家请求标志位（op19 尾字节 body[26]==1）⇒ 域侧 worn_slot_reassert 那一族门禁惰性。
		// **必须与上面那个 `wornSlotClaimOccurrence = 2` 一起透传**：只改 occurrence 不足以
		// 让域侧把这条包当成玩家请求（静默窗结算走的就是这条路）。
		PlayerRequestFlag: playerRequestFlagged,
		// noop 分支的权威刷新（默认 on；off = 只回 msg_id=19）。运行时读 env，
		// 见 equip_login_wear_family_window.go。
		LoginWearReassertNoopRefresh: s.equipLoginWearReassertNoopRefreshRequested(),
		EquipAppearanceSlotGateOff:   s.equipAppearanceSlotGateOffRequested(),
	})
	session.party.mu.Unlock()
	if err != nil || !ok {
		return ok, err
	}
	s.commitDungeonConsumptionSnapshot(session, consumptionSnapshot, result)
	// delta 档：模块已经把奖励写进库了，这里立刻算差分并把逐槽增量挂到
	// `result.ItemSlotRefreshes`（`sendAlignedUpperResponses` 会在 ACK 之后无条件发成
	// class0/msg14），同时把仍需整容器的例外容器记到会话上供后置动作读取。
	// 快照/差分不可用时什么都不做 ⇒ 后置动作回落到今天的整容器回放。见 item_grant_notice.go。
	s.applyItemGrantSnapshot(session, repos, itemGrantSnapshot, &result)
	decision := result.Decision
	if !result.Handled {
		return false, nil
	}
	switch decision.Action {
	case alignedcmd.ActionPendingModule:
		if result.ResponseAllowed && len(result.UpperResponses) > 0 {
			// 登录复述 noop 的权威收敛（默认档）：只有"这一族 op19 + 落在登录窗口内 +
			// ACK 的 moveValue==0"三条同时成立时才规划，规划本身不发包、不读库。
			// 补发发生在 ACK 之后（见下面 sendLoginEquipmentNoopSlotRows 的调用点），
			// 保证"op19 ACK 永远排第一"这条既有口径不变。
			// 判据、开关与"为什么不碰穿脱路径"见 login_equipment_container.go。
			loginEquipment := s.planLoginEquipmentNoopConvergence(session, body, loginWearWindowOpen, result)
			s.logPacketEvent("game-aligned-command-real-response",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"account_id", accountID,
				"selected_character_id", session.selectedCharacterID,
				"cmd", cmd,
				"type", typ,
				"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
				"domain", string(decision.Domain),
				"support", string(decision.Support),
				"evidence", decision.Evidence,
				"note", decision.Note,
				"operation", result.Operation,
				"response_count", len(result.UpperResponses),
				"reason", result.Reason,
				"body_len", len(body))
			// 强化/增幅失败摧毁装备的补偿道具落位（并入既有行还是新开格、哪个槽、
			// 开关取值）——领域层没有 logger，所以由 bridge 在 ACK 之前逐条记名。
			// 字段与事件名见 upgrade_destroy_bonus_event.go；现场缺陷
			// （角色 6 的 3243 四行同源）复测时 grep 这个名字即可。
			if grant := result.UpgradeDestroyBonusMerge; grant != nil {
				fields := []any{
					"conn_id", session.connID,
					"channel_id", session.channel.ID,
					"account_id", accountID,
					"selected_character_id", session.selectedCharacterID,
					"type", typ,
				}
				fields = append(fields, upgradeDestroyBonusMergeEventFields(grant)...)
				s.logPacketEvent(upgradeDestroyBonusMergeEventName, fields...)
			}
			// 保护券判定（消耗了哪一行 / 背包里没有券 / 开关取值）：见
			// upgrade_protect_ticket_event.go。复测时 grep
			// `game-upgrade-protect-ticket-consumed` 就是"这次真的用了券"。
			if fact := result.UpgradeProtectTicket; fact != nil {
				fields := []any{
					"conn_id", session.connID,
					"channel_id", session.channel.ID,
					"account_id", accountID,
					"selected_character_id", session.selectedCharacterID,
					"type", typ,
				}
				fields = append(fields, upgradeProtectTicketEventFields(fact)...)
				s.logPacketEvent(upgradeProtectTicketEventName(fact), fields...)
			}
			if err := s.sendAlignedUpperResponses(session, result); err != nil {
				return true, err
			}
			if loginEquipment.planned {
				s.sendLoginEquipmentNoopSlotRows(session, loginEquipment, "login_wear_noop_after_ack")
			}
			return true, nil
		}
		s.logPacketEvent("game-aligned-command-pending-module",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"cmd", cmd,
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"domain", string(decision.Domain),
			"support", string(decision.Support),
			"evidence", decision.Evidence,
			"note", decision.Note,
			"operation", result.Operation,
			"response_allowed", result.ResponseAllowed,
			"reason", result.Reason,
			"body_len", len(body))
		s.logInfo("dnfbridge aligned game command pending module implementation",
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"domain", string(decision.Domain),
			"support", string(decision.Support),
			"operation", result.Operation,
			"response_allowed", result.ResponseAllowed,
			"reason", result.Reason,
			"body_len", len(body))
		// op44/op251 是个例外：客户端点了"使用"就在等结果，不回包会把界面卡住
		// （"使用无效，之后任何操作都点不动"）。回一个失败 ACK，让客户端恢复。
		// op251 是带共享特效的消耗品（爆竹族）在当前客户端走的那条 opcode。
		if typ == uint16(dnfenum.CmdPacketUseStackable) {
			return true, s.answerRejectedUseStackable(session, body, result.Reason)
		}
		if typ == uint16(dnfenum.CmdPacketUseSharedEffectItem) {
			return true, s.answerRejectedUseStackableOpcode(
				session,
				dnfenum.CmdPacketUseSharedEffectItem,
				body,
				result.Reason,
			)
		}
		return true, nil
	case alignedcmd.ActionBlocked:
		s.logPacketEvent("game-legacy-migration-blocked-by-evidence",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"cmd", cmd,
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"domain", string(decision.Domain),
			"evidence", decision.Evidence,
			"operation", result.Operation,
			"reason", decision.Reason,
			"body_len", len(body))
		s.logInfo("dnfbridge legacy migration blocked by current exe evidence",
			"account_id", accountID,
			"selected_character_id", session.selectedCharacterID,
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"domain", string(decision.Domain),
			"operation", result.Operation,
			"body_len", len(body))
		return true, nil
	default:
		return false, nil
	}
}

func alignedSkillOwnerRulesRequired(opcode dnfenum.CmdPacket) bool {
	switch opcode {
	case dnfenum.CmdPacketChangeSkillslot, dnfenum.CmdPacketBuySkill, dnfenum.CmdPacketSkillInit:
		return true
	default:
		return false
	}
}

func (s *Service) skillOwnerRules(ctx context.Context, repos dnfrepo.Group, characterID uint16) (*dnfskill.Table, map[uint16]int, *dnfrepo.SkillPointState) {
	if s == nil || repos.Character == nil || characterID == 0 {
		return nil, nil, nil
	}
	record, ok, err := repos.Character.Load(ctx, fmt.Sprintf("%d", characterID))
	if err != nil || !ok {
		return nil, nil, nil
	}
	job, ok := characterJobByte(record)
	if !ok {
		return nil, nil, nil
	}
	entries, err := s.initialCharacterSkills(ctx, job)
	if err != nil {
		s.logInfo("dnfbridge skill rules unavailable", "character_id", characterID, "job", job, "error", err)
		return nil, nil, nil
	}
	points, err := s.initialSkillPoints(ctx, record.Level)
	if err != nil {
		s.logInfo("dnfbridge skill point baseline unavailable", "character_id", characterID, "level", record.Level, "error", err)
		return nil, nil, nil
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return nil, nil, nil
	}
	levels := make(map[uint16]int, len(entries))
	for _, entry := range entries {
		if entry.SkillID < 0 || entry.SkillID > 0xffff || entry.Level < 0 {
			continue
		}
		levels[uint16(entry.SkillID)] = entry.Level
	}
	baseline := &dnfrepo.SkillPointState{
		TotalSP: points.TotalSP, RemainingSP: points.TotalSP,
		TotalTP: points.TotalTP, RemainingTP: points.TotalTP,
		SyncedLevel: points.SyncedLevel,
	}
	return catalog, levels, baseline
}

// sendAlignedUpperResponses 发送模块返回的真实 upper 响应。
// 模块给出的 Body 已经是完整业务包体，不能再统一套成功字节。
func (s *Service) sendAlignedUpperResponses(session *gameSession, result alignedcmd.Result) error {
	authoritativeLists := currentAuthoritativeListRefreshSet(result.AuthoritativeListRefreshes)
	handledLists := make(map[byte]bool, len(authoritativeLists))
	for _, response := range result.UpperResponses {
		if listType, ok := currentAuthoritativeListRefreshListType(response, authoritativeLists); ok {
			handledLists[listType] = true
			body, source, entryCount, built := s.buildCurrentAuthoritativeListRefreshBody(session, listType)
			if !built {
				// 权威投影建不出来时绝不退回模块那份仓库快照：op13 是整表替换，那份快照
				// 正是 2026-10-14 现场抹掉钱包行/账号共享格/品级的来源。只跳过这一条刷新，
				// 后面的 class1 ACK 必须照发（落库已经完成，ACK 不能丢）。
				s.logPacketEvent("game-upper-authoritative-list-refresh-skipped",
					"conn_id", session.connID,
					"operation", result.Operation,
					"char_id", session.selectedCharacterID,
					"list_type", listType,
					"reason", "authoritative_projection_unavailable")
				continue
			}
			s.logPacketEvent("game-upper-authoritative-list-refresh-send",
				"conn_id", session.connID,
				"operation", result.Operation,
				"char_id", session.selectedCharacterID,
				"msg_id", response.MsgID,
				"classification", response.Classification,
				"list_type", listType,
				"entry_count", entryCount,
				"body_len", len(body),
				"body_source", source,
				"source", "session_projection_replaces_module_snapshot")
			if err := s.sendGameUpperRawClassCodec(session, response.MsgID, body, response.Classification, response.AllowCodec); err != nil {
				return err
			}
			continue
		}
		if err := s.sendGameUpperRawClassCodec(session, response.MsgID, response.Body, response.Classification, response.AllowCodec); err != nil {
			return err
		}
	}
	for listType := range authoritativeLists {
		if handledLists[listType] {
			continue
		}
		s.logPacketEvent("game-upper-authoritative-list-refresh-missing",
			"conn_id", session.connID,
			"operation", result.Operation,
			"char_id", session.selectedCharacterID,
			"list_type", listType,
			"reason", "no_matching_class0_refresh_response")
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, result.Operation, result.ItemSlotRefreshes); err != nil {
		return err
	}
	refreshCombatPowerAffixes := false
	for _, action := range result.PostActions {
		if alignedPostActionTouchesCombatPower(action) {
			refreshCombatPowerAffixes = true
		}
		if err := s.sendAlignedPostAction(session, result, action); err != nil {
			if errors.Is(err, errAlignedPostCommitProjectionDeferred) {
				s.logWarn("dnfbridge deferred remaining aligned post-commit projections",
					"conn_id", session.connID,
					"operation", result.Operation,
					"post_action", action,
					"char_id", session.selectedCharacterID,
					"error", err)
				break
			}
			return err
		}
	}
	if refreshCombatPowerAffixes {
		if err := s.sendSelectedCurrentCombatPowerAffixes(
			session,
			"aligned_"+result.Operation+"_after_ack_and_worn_state_commit",
		); err != nil {
			// The business mutation and ACK already succeeded. A private Lua
			// projection failure is retryable on the next panel/town refresh and
			// must not turn the committed item move into a false client failure.
			s.logWarn("dnfbridge combat-power affix post-action deferred",
				"conn_id", session.connID,
				"operation", result.Operation,
				"char_id", session.selectedCharacterID,
				"error", err)
		}
	}
	if result.MailboxAlarmRecipientID != 0 {
		if err := s.sendMailboxAlarmToOnlineRecipient(result.MailboxAlarmRecipientID); err != nil {
			// The durable send and sender ACK have already succeeded. A stale
			// recipient connection must not turn that committed operation into a
			// false failure; their next op96 projects the same mailbox state.
			s.logWarn("dnfbridge mailbox online alarm deferred to next mailbox open",
				"recipient_character_id", result.MailboxAlarmRecipientID,
				"operation", result.Operation,
				"error", err)
		}
	}
	// 契约奖励在发放事务里已被直接叠加成账号时长（不占格、不进邮件），没有物品行
	// 可刷，所以激活通知由这里补发；失败也不能把已提交的发放变成客户端失败。
	if err := s.sendAlignedPremiumContractActivations(session, result.Operation, result.PremiumContractActivations); err != nil {
		s.logWarn("dnfbridge premium contract activation notice deferred",
			"conn_id", session.connID,
			"operation", result.Operation,
			"char_id", session.selectedCharacterID,
			"activation_count", len(result.PremiumContractActivations),
			"error", err)
	}
	// 史诗碎片同理：它被记进**账号级**图鉴账本，没有任何物品行可刷，所以账本
	// 增量包（class0/0x047D）由这里补发。失败也不能把已提交的领取变成客户端失败
	// —— 账本已经落库，客户端下次登录会拿到全量快照。
	if err := s.sendCurrentEpicPieceCredits(
		session,
		result.EpicPieceCredits,
		"aligned_"+result.Operation+"_after_ack",
	); err != nil {
		s.logWarn("dnfbridge epic piece ledger notice deferred",
			"conn_id", session.connID,
			"operation", result.Operation,
			"char_id", session.selectedCharacterID,
			"credit_count", len(result.EpicPieceCredits),
			"error", err)
	}
	return nil
}

func alignedPostActionTouchesCombatPower(action alignedcmd.PostAction) bool {
	switch action {
	case alignedcmd.PostActionRefreshSelectedItemContainers,
		alignedcmd.PostActionRefreshSelectedWearContainers,
		alignedcmd.PostActionRefreshSelectedActorAppearance,
		alignedcmd.PostActionRefreshSelectedEquipmentSlots,
		alignedcmd.PostActionRefreshSelectedCreatureState:
		return true
	default:
		return false
	}
}

func (s *Service) sendAlignedPostAction(session *gameSession, result alignedcmd.Result, action alignedcmd.PostAction) error {
	operation := result.Operation
	switch action {
	case alignedcmd.PostActionRefreshSelectedItemContainers:
		// delta 档（DNFBRIDGE_ITEM_GRANT_NOTICE 的默认档）：本次开盒真正写过的
		// (list, slot) 已经在 `sendAlignedUpperResponses` 里发成 class0/msg14 逐槽增量
		// （客户端的分页高亮读的就是它），这里**只**补仍然必须整容器的例外容器——
		// 首刀只有 list 38（`inventory/handler.go:877-883`）；list 7 的写入由发送侧改期成
		// 1500 ms 整容器，不在这里。记录为空 = 这次没有任何行需要通知 ⇒ 一条整容器都不发。
		// 见 item_grant_notice.go。
		if isBoxOpenOperation(operation) {
			if containers, armed := session.takeItemGrantNoticeContainers(); armed {
				s.logPacketEvent("game-aligned-command-post-action-send",
					"conn_id", session.connID,
					"operation", operation,
					"post_action", action,
					"char_id", session.selectedCharacterID,
					"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
					"classification", 0,
					"list_types", containers,
					"sequence", "durable_owner_then_class1_box_ack_then_repository_backed_op14_rows_then_exception_containers",
					"item_grant_notice", itemGrantNoticeDelta,
					"evidence", "item_grant_notice_delta_replaces_full_container_replay")
				if len(containers) == 0 {
					return nil
				}
				if err := s.sendCurrentContainerListTypesWithRefresh(
					session,
					"aligned_"+operation+"_after_ack",
					true,
					containers,
				); err != nil {
					return fmt.Errorf("%w: refresh box-open exception containers: %v", errAlignedPostCommitProjectionDeferred, err)
				}
				return nil
			}
		}
		// 开盒子（op208/op1128）在 changed_only 档下只重发本次真正改变的容器
		// （源 list 0 + 本次实际发放到的奖励容器）。开关默认 current，此时这里
		// 直接落到下面的全量档，线格式逐字节不变。记录不完整也会回落全量档。
		// 证据、残余不确定与一键回退见 box_open_container_refresh.go。
		if isBoxOpenOperation(operation) {
			if changed, ok := s.boxOpenChangedContainerListTypes(session); ok {
				s.logPacketEvent("game-aligned-command-post-action-send",
					"conn_id", session.connID,
					"operation", operation,
					"post_action", action,
					"char_id", session.selectedCharacterID,
					"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
					"classification", 0,
					"list_types", changed,
					"sequence", "durable_owner_then_class1_box_ack_then_changed_containers_only",
					"evidence", "changlog.md:1103-1105_box_open_replayed_five_containers")
				if err := s.sendCurrentContainerListTypesWithRefresh(
					session,
					"aligned_"+operation+"_after_ack",
					true,
					changed,
				); err != nil {
					return fmt.Errorf("%w: refresh changed box-open containers: %v", errAlignedPostCommitProjectionDeferred, err)
				}
				return nil
			}
		}
		s.logPacketEvent("game-aligned-command-post-action-send",
			"conn_id", session.connID,
			"operation", operation,
			"post_action", action,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
			"classification", 0,
			"list_types", currentSelectedItemListTypes,
			"sequence", "durable_owner_then_class1_op19_ack_then_repository_backed_op13_list0_list1_list2")
		if err := s.sendSelectedCurrentContainerListsWithRefresh(
			session,
			"aligned_"+operation+"_after_ack",
			true,
		); err != nil {
			return fmt.Errorf("%w: refresh selected item containers: %v", errAlignedPostCommitProjectionDeferred, err)
		}
		return nil
	case alignedcmd.PostActionRefreshSelectedWearContainers:
		// 穿脱装备/时装专用：旧口径是 5 个整容器的 class0/op13 全量重发
		// （每次 ≈10.1 KB，且从不包含落点 list 3）。默认档按厂商 9.16 的
		// item_slot_refresh_count=0 口径整段不发；写 full 一键回退。
		// 证据见 equip_move_refresh.go。
		if !s.equipMoveContainerRefreshFullRequested() {
			s.logPacketEvent("game-aligned-command-post-action-send",
				"conn_id", session.connID,
				"operation", operation,
				"post_action", action,
				"char_id", session.selectedCharacterID,
				"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
				"classification", 0,
				"list_types", []byte{},
				"sequence", "suppressed_no_full_container_replay_after_wear_move",
				"evidence", "vendor_916_item_slot_refresh_count_0_and_86jp_move_itemspace_sends_no_item_list_refresh")
			return nil
		}
		s.logPacketEvent("game-aligned-command-post-action-send",
			"conn_id", session.connID,
			"operation", operation,
			"post_action", action,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
			"classification", 0,
			"list_types", currentSelectedItemListTypes,
			"sequence", "legacy_full_container_replay_rollback_full")
		if err := s.sendSelectedCurrentContainerListsWithRefresh(
			session,
			"aligned_"+operation+"_after_ack",
			true,
		); err != nil {
			return fmt.Errorf("%w: refresh selected wear containers: %v", errAlignedPostCommitProjectionDeferred, err)
		}
		return nil
	case alignedcmd.PostActionRefreshSelectedPetMoveSlots:
		// 宠物/神器穿戴（op19 落点 list 3 槽 26..29）专用的回包后刷新：
		// 旧口径是 5 个整容器的 class0/op13 全量重发（list 0/1/2/7/38，现场每次
		// ≈23.9 KB），而落点 list 3 从来不在这份名单里。默认档按厂商
		// PetInventoryMoveCoordinator.SendPetItemRefreshAsync 的逐槽口径只发本次
		// 真正变动的那一格（有货发权威行、空槽发 0xFFFFFFFF 空行）；
		// 写 full 一键回退到旧口径，写 none 则连这一格也不发。
		// 证据、现场取证与三档取舍见 equip_move_refresh.go。
		if s.petMoveContainerRefreshFullRequested() {
			s.logPacketEvent("game-aligned-command-post-action-send",
				"conn_id", session.connID,
				"operation", operation,
				"post_action", action,
				"char_id", session.selectedCharacterID,
				"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
				"classification", 0,
				"list_types", currentSelectedItemListTypes,
				"sequence", "legacy_pet_move_full_container_replay_rollback_full")
			if err := s.sendSelectedCurrentContainerListsWithRefresh(
				session,
				"aligned_"+operation+"_after_ack",
				true,
			); err != nil {
				return fmt.Errorf("%w: refresh selected pet move containers: %v", errAlignedPostCommitProjectionDeferred, err)
			}
			return nil
		}
		if s.petMoveContainerRefreshSuppressed() {
			s.logPacketEvent("game-aligned-command-post-action-send",
				"conn_id", session.connID,
				"operation", operation,
				"post_action", action,
				"char_id", session.selectedCharacterID,
				"slots", result.PetMoveSlotRefreshes,
				"sequence", "pet_move_container_and_slot_refresh_both_suppressed_rollback_none",
				"evidence", "client_native_op19_object_move_is_authoritative")
			return nil
		}
		if err := s.sendSelectedPetMoveSlotRefreshes(session, operation, result.PetMoveSlotRefreshes); err != nil {
			return fmt.Errorf("%w: refresh selected pet move slots: %v", errAlignedPostCommitProjectionDeferred, err)
		}
		return nil
	case alignedcmd.PostActionRefreshSelectedEquipmentSlots:
		s.logPacketEvent("game-aligned-command-post-action-send",
			"conn_id", session.connID,
			"operation", operation,
			"post_action", action,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
			"classification", 0,
			"list_type", currentSocketListEquipment,
			"sequence", "unsafe_op14_list3_equipment_rows_suppressed",
			"evidence", "86jp_move_itemspace_sends_ack_sortlock_pet_specific_refresh_then_noti2_no_generic_list3")
		return nil
	case alignedcmd.PostActionRefreshSelectedAccountCargo:
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		body, source, count, ok := s.buildCurrentItemListBodyForSession(ctx, session, 12)
		if !ok {
			return fmt.Errorf("aligned post action %q could not build account cargo", action)
		}
		s.logPacketEvent("game-aligned-command-post-action-send",
			"conn_id", session.connID,
			"operation", operation,
			"post_action", action,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
			"classification", 0,
			"list_type", 12,
			"entry_count", count,
			"body_len", len(body),
			"body_source", source,
			"sequence", "durable_account_owner_then_class1_ack_then_current_exe_op13_list12")
		return s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), body)
	case alignedcmd.PostActionRefreshSelectedActorAppearance:
		// 这个 post action 只允许作用在**已定型的本地 actor 代际**上：mode0 自投影会把
		// 对象重挂到当前 ctx，若此时 actor 还没被 op24/op29 定型（或 owner 上下文已经
		// 换成别的频道），包会落到错的代际上 —— 宁可延后，让下一次场景引导收敛。
		if !currentTownSelfActorAppearanceReady(session) {
			s.logWarn("dnfbridge deferred selected actor mode0 appearance",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", session.selectedCharacterID,
				"reason", "selected_town_actor_generation_or_native_owner_not_ready")
			return fmt.Errorf(
				"%w: selected town actor generation or native owner is not ready",
				errAlignedPostCommitProjectionDeferred,
			)
		}
		// 连点时装时不要每件都重建一次角色：窗口内的第一次照旧同步下发，
		// 紧随其后的并入一次尾随刷新（排队失败则回退同步，绝不丢刷新）。
		// 这条合并只属于 mode1 回退档：mode0 档一次穿脱就是一个小包（≈468 B /
		// 客户端 4 ms），延迟合并只会让"点一下立刻看到"落空。见 equip_move_refresh.go。
		if !s.equipAppearanceRefreshUsesMode0() {
			if s.shouldCoalesceAvatarAppearance(session) && s.scheduleTrailingAvatarAppearance(session, operation) {
				s.logPacketEvent("game-avatar-appearance-coalesced",
					"conn_id", session.connID,
					"operation", operation,
					"char_id", session.selectedCharacterID,
					"reason", "avatar_wear_burst_within_coalesce_window")
				return nil
			}
			if err := s.sendSelectedActorMode1AppearanceRefresh(
				session,
				operation,
				"durable_owner_then_class1_ack_then_repository_backed_native_owner_mode1_rollback",
			); err != nil {
				s.logWarn("dnfbridge deferred selected actor mode1 appearance",
					"conn_id", session.connID,
					"operation", operation,
					"char_id", session.selectedCharacterID,
					"error", err)
				return fmt.Errorf("%w: refresh selected actor mode1 appearance: %v", errAlignedPostCommitProjectionDeferred, err)
			}
			return nil
		}
		// 默认档（mode0）：**leading + trailing** 合并。
		//
		// 客户端 trace（2026-09-28，pid=3776）逐条对照证明外观只有 mode0 这条路会刷新
		// （mode0 → `mode0-owner-compare … branch=current-scene-create` → `local-actor-create`
		// → `game-options-apply` → `town current actor repair … visual_synced=1`），
		// mode1 只换装备数据快照 ⇒ mode1 档必须重新选角才看得到新时装。
		// mode0 同时更小更便宜（≈468 B / 客户端 dispatch 13–22 ms；op19 派发 5–20 ms、
		// mode0 回包比 ACK 晚 ≈6 ms），但它**每次都会让客户端整题重建本地角色**
		// （`local-actor-create`），所以 N 连点时装 = N 次重建 —— 这正是操作者
		// "穿上脱下的速度不太满意"的那一半。
		//
		// 修法：窗口内的**第一次**照旧同步下发（单件穿脱仍是"点一下立刻看到"，
		// 包数一个都不多），窗口内紧随其后的几次合并成**窗口末尾的一次**尾随刷新。
		// 目标：N 连点 ⇒ 最多 2 个 mode0（实测见
		// avatar_appearance_coalesce_test.go）。
		// 窗口 DNFBRIDGE_EQUIP_APPEARANCE_COALESCE_MS（默认 200 ms）；
		// 写 DNFBRIDGE_EQUIP_APPEARANCE_COALESCE=off 一键关掉，逐字节回到
		// "每次穿脱都同步发一个 mode0"。mode1 回退档不受影响（它有自己那段合并）。
		if s.avatarAppearanceBurstCoalesceEnabled() &&
			s.shouldCoalesceAvatarAppearance(session) &&
			s.scheduleTrailingAvatarAppearance(session, operation) {
			s.logPacketEvent("game-avatar-appearance-coalesced",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", session.selectedCharacterID,
				"mode", equipAppearanceRefreshMode0,
				"window_ms", s.avatarAppearanceCoalesceWindow().Milliseconds(),
				"reason", "avatar_wear_burst_within_coalesce_window_leading_then_trailing")
			return nil
		}
		if err := s.sendSelectedActorMode0AppearanceRefresh(
			session,
			operation,
			"durable_owner_then_class1_ack_then_repository_backed_native_owner_mode0",
		); err != nil {
			s.logWarn("dnfbridge deferred selected actor mode0 appearance",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", session.selectedCharacterID,
				"error", err)
			return fmt.Errorf("%w: refresh selected actor mode0 appearance: %v", errAlignedPostCommitProjectionDeferred, err)
		}
		return nil
	case alignedcmd.PostActionRefreshSelectedCreatureState:
		if err := s.sendSelectedCreatureStateAfterMoveAck(session, "aligned_"+operation+"_after_ack"); err != nil {
			// Creature state is an absolute post-ACK projection. Preserve the
			// already-committed endpoint move and let the next scene bootstrap
			// converge it instead of disconnecting after a successful ACK.
			s.logWarn("dnfbridge deferred selected creature state after move",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", session.selectedCharacterID,
				"error", err)
		}
		return nil
	case alignedcmd.PostActionRefreshCrystalContractState:
		return s.sendCurrentCrystalContractState(session, "aligned_"+operation+"_after_type97_activation")
	case alignedcmd.PostActionRefreshFatigueState:
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		character, err := s.updateCurrentCharacterFatigue(ctx, session, false)
		if err != nil {
			return err
		}
		return s.sendGameUpperRawClass(session, currentFatigueMsgID, buildCurrentFatigueBody(character, true), 0)
	case alignedcmd.PostActionRefreshSelectedDamageFontState:
		return s.sendSelectedDamageFontState(session, "aligned_"+operation+"_after_ack_and_item_refresh")
	case alignedcmd.PostActionRefreshSelectedPartyFrame:
		state, err := s.ensureManagedRuntimePartyForSession(session)
		if err != nil {
			return err
		}
		if err := s.sendRuntimePartySnapshot(session, state); err != nil {
			return err
		}
		return nil
	case alignedcmd.PostActionRefreshSelectedActorSkills:
		s.logPacketEvent("game-aligned-command-post-action-send",
			"conn_id", session.connID,
			"operation", operation,
			"post_action", action,
			"char_id", session.selectedCharacterID,
			"msg_id", currentSkillInfoMsgID,
			"classification", 0,
			"sequence", "durable_skill_reset_then_class1_op491_ack_then_class0_op19_refresh")
		return s.sendSelectedActorCurrentSceneSkillInfo(session, "aligned_"+operation+"_after_ack")
	default:
		return fmt.Errorf("aligned post action %q is not registered", action)
	}
}

// currentTownSelfActorAppearanceReady is stricter than the general scene-ready
// predicate. The in-place op357 redraw is allowed only for the already-finalized
// selected actor owned by the context committed for this town connection. This
// prevents an inventory action from targeting a remote/stale actor or crossing
// a town transition boundary.
//
// A live avatar-wear deferral (09:13, char 3, dungeon_id=9) proved the same
// redraw is also required inside a dungeon: resetDungeonEntrySceneGates returns
// the local actor to the scene-object context for the run, so the town
// predicates stay false for the whole dungeon and every equip op19 lost its
// appearance projection.
func currentTownSelfActorAppearanceReady(session *gameSession) bool {
	if session == nil || session.selectedCharacterID == 0 {
		return false
	}
	if currentDungeonSelfActorAppearanceReady(session) {
		return true
	}
	session.townMu.Lock()
	ready := session.townSceneReadyCharacterID == session.selectedCharacterID &&
		session.townActorOwnerChannel == currentConnectionTownActorOwnerContext(session)
	session.townMu.Unlock()
	if !ready {
		return false
	}
	session.townPostTransition.mu.Lock()
	stage := session.townPostTransition.stage
	session.townPostTransition.mu.Unlock()
	return stage == currentTownPostTransitionIdle ||
		stage == currentTownPostTransitionComplete
}

// currentDungeonSelfActorAppearanceReady reports whether the selected dungeon
// actor already exists in the owner context that the current run uses. The
// dungeon route deliberately returns the local actor to the scene-object
// context (resetDungeonEntrySceneGates), and the entry sequence binds that actor
// with mode0 before op27 and mode1 after op29, so a completed post-start-map
// placement means the op2 body repeats an existing actor instead of creating a
// second one. Requiring the scene-object owner context keeps the redraw from
// being addressed to the town owner channel while the town is detached.
func currentDungeonSelfActorAppearanceReady(session *gameSession) bool {
	if session == nil || session.selectedCharacterID == 0 {
		return false
	}
	if !currentDungeonSceneActive(session) || !session.postStartMapPlayerStateSent {
		return false
	}
	session.townMu.Lock()
	owner := session.townActorOwnerChannel
	session.townMu.Unlock()
	return owner == currentSceneObjectContext
}
