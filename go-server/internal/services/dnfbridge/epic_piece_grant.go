package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件补上"史诗碎片进背包"的**另外两条入包路径**，并清掉已经躺进背包的存量碎片。
//
// ── 为什么只有"地面拾取"一条路径是不够的（2026-09-25 香港现场）────────────
//
// 上一批只把 `dungeon_drop_pickup.go`（地面拾取）接上了账号图鉴账本。但 GM 面板
// 的"发放物品"**不直写背包**（`gmtool.handleItemGrant` 明确拒绝 inventory 模式：
// "直写背包会与在线会话的存档互相覆盖"），它写一封带附件的系统邮件；玩家点"领取"
// 走 op95 `ENUM_CMDPACKET_MAILBOX_EXTRACT_ITEM` ⇒ `mail.Owner.Claim` ⇒
// `addAttachmentToInventory` 把附件按运行时 PVF 的家族堆进材料页
// （`stackable/epicPiece/*.stk` 的 `[stackable type]` 是 `[material]` ⇒ list 0
// 121..176）。
//
// 现场封包日志（`/opt/dnf90/logs/packet_log.txt`，20:20 部署之后的会话
// conn_id=game-000003，角色 3）:
//
//	line 987  RECV type=95 ENUM_CMDPACKET_MAILBOX_EXTRACT_ITEM body_len=8
//	line 991  SEND msg_id=95  (class1 ack)
//	line 996  SEND msg_id=14  body_hex=…00 01 00 7a 00 6f2d9d00 01000000…
//	          → list_type=0 slot=0x7a(122) item_id=0x009d2d6f(10300783) count=1
//	          EVENT game-aligned-command-item-slot-refresh-send operation=mailbox_extract_ack
//	line 1011 EVENT game-read-close … read: connection reset by peer（客户端已崩）
//
// 而 10300783 确实是史诗碎片：真档 `etc/epicpieceinfo.etc` 的
// `[piece list]` 里就有 `100312425 10300783`（成品 100312425 = 启明星的指引，
// 操作者背包 `0:11` 里正好有这个成品），`stackable/stackable.lst` 把它映射到
// `epicPiece/10300783.stk`。同一份日志里 **没有任何** `0x047D`/msg_id=1149
// 事件 ⇒ 这条入包路径确实一个字节都没走图鉴账本。
//
// ── 修法 ──────────────────────────────────────────────────────────────
//
//  1. **领取期**：`mail.Owner.Claim` 遇到 `resolution.EpicPieceID != 0` 的附件时
//     不写任何背包行，改调本文件注入的账号账本闭包（`alignedEpicPieceCreditorForCommand`），
//     并在 ACK 之后由桥接层补发 class0/0x047D 增量（`sendCurrentEpicPieceCredits`）。
//     GM 发放与邮件领取因此共用同一条修好的路径 —— GM 面板本身就是走邮件交付的。
//  2. **登录清扫**：`purgeCurrentInventoryEpicPieces` 在选角背包引导**之前**把角色
//     背包（含仓库）里已经存在的碎片按数量并入账号账本并从背包移除。操作者背包里
//     那一件 10300783 就是被这次事故放进去的，不清掉的话客户端下次登录还会渲染它。
//
// ── 开关 ──────────────────────────────────────────────────────────────
//
// 新行为用**独立**开关 `DNFBRIDGE_EPIC_PIECE_GRANT`（默认 on），不动
// `DNFBRIDGE_EPIC_PIECE_BOOK` 的既有三档语义：
//
//	DNFBRIDGE_EPIC_PIECE_BOOK=off           → 碎片照旧进背包（全局回退，含拾取）
//	DNFBRIDGE_EPIC_PIECE_GRANT=off          → 只回退本文件这两条路径（领取 + 清扫），
//	                                          地面拾取仍走图鉴账本
//	两个都 off                              → 完全回到改动前
//
// ── 覆盖范围（2026-09-25 取证后的边界）────────────────────────────────
//
// 已经接到账本的入包路径：
//
//	A. 地面拾取（`dungeon_drop_pickup.go`，上一批）；
//	B. 邮件附件领取（op95）。**GM 面板发放也走这一条**：`gmtool.handleItemGrant`
//	   只支持邮件交付（inventory 模式被显式拒绝："直写背包会与在线会话的存档
//	   互相覆盖"），而全仓库唯一的 `mail.Owner.Claim` 调用点就是
//	   `mail.handler.handleMailboxExtract` —— 所以这一处改动同时覆盖
//	   "GM 发碎片"与"玩家领附件"两种说法。
//	C. 存量清扫（登录期，本文件）。它同时是下面未接路径的**兜底**：任何还没接上的
//	   路径把碎片写进了背包，下一次选角登录都会把它并入账本并从背包移除。
//
// 2026-10-17：booster随机/自选礼包产出也直接进入账号账本，使用扩展的
// AccountAssets事务与开盒消耗、金币及其它奖励一起提交，ACK后发绝对数量。
// 图鉴制作已有独立事务及全量刷新路径，本轮保持。
const (
	// epicPieceGrantEnv 是本文件两条新路径的开关。
	epicPieceGrantEnv      = "DNFBRIDGE_EPIC_PIECE_GRANT"
	epicPieceGrantValueOn  = "on"
	epicPieceGrantValueOff = "off"
	// epicPieceGrantMaxCount 是单件堆叠可入账的上限（账本是 u32 绝对值）。
	epicPieceGrantMaxCount int64 = 1<<32 - 1
)

// epicPieceGrantMode 是 epicPieceGrantEnv 的归一化取值。
type epicPieceGrantMode int

const (
	// epicPieceGrantOn 默认档（零值）：领取/清扫都改走账号图鉴账本。
	epicPieceGrantOn epicPieceGrantMode = iota
	// epicPieceGrantOff 回退档：领取照旧把碎片堆进材料页，清扫不执行。
	epicPieceGrantOff
)

func (mode epicPieceGrantMode) String() string {
	if mode == epicPieceGrantOff {
		return epicPieceGrantValueOff
	}
	return epicPieceGrantValueOn
}

// enabled 表示"碎片不进背包"的领取期与清扫期行为是否生效。
func (mode epicPieceGrantMode) enabled() bool {
	return mode != epicPieceGrantOff
}

// readEpicPieceGrantMode 解析 DNFBRIDGE_EPIC_PIECE_GRANT。
// 默认 on：空/未设置/非法值都按 on；只有显式写 off（或 0/false/legacy/disabled）
// 才回退到"碎片照旧进背包"。
func readEpicPieceGrantMode(value string) epicPieceGrantMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case epicPieceGrantValueOff, "0", "false", "legacy", "disabled":
		return epicPieceGrantOff
	default:
		return epicPieceGrantOn
	}
}

// epicPieceGrant 返回当前 Service 的归一化档位（零值 options = on）。
func (s *Service) epicPieceGrant() epicPieceGrantMode {
	if s == nil {
		return epicPieceGrantOn
	}
	return s.options.epicPieceGrant
}

// epicPieceGrantEnabled 是"领取期与清扫期都改走账本"的唯一判定：它同时要求
// 图鉴总开关与发放开关都在 on 档。两个开关任一为 off 都回到旧行为。
func (s *Service) epicPieceGrantEnabled() bool {
	return s.epicPieceBook().enabled() && s.epicPieceGrant().enabled()
}

// ─────────────────────── 领取期：账号账本入账闭包 ───────────────────────

// alignedEpicPieceCreditorForCommand 只在"领取邮件附件"（op95）这条会写背包的
// 路径上返回账号级史诗图鉴账本的入账闭包；其它命令一律返回 nil，
// mail 模块因此对它们保持逐字节不变的行为。
//
// 返回 nil 的三种情况都**不改变行为**（碎片照旧入包），并且都在调用方留下日志：
// 开关回退、账号未绑定、账号仓储不支持原子自增。
func (s *Service) alignedEpicPieceCreditorForCommand(
	opcode dnfenum.CmdPacket,
	accountID string,
) alignedcmd.MailboxEpicPieceCreditor {
	if s == nil || opcode != dnfenum.CmdPacketMailboxExtractItem {
		return nil
	}
	if !s.epicPieceGrantEnabled() {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	return func(ctx context.Context, pieceID uint32, count int64) (uint32, error) {
		if pieceID == 0 || count <= 0 || count > epicPieceGrantMaxCount {
			return 0, fmt.Errorf(
				"dnf epic piece credit rejected piece=%d count=%d",
				pieceID,
				count,
			)
		}
		return addCurrentEpicPieceBalance(
			ctx,
			repositories.Account,
			accountID,
			pieceID,
			uint32(count),
			time.Now().UTC(),
		)
	}
}

// sendCurrentEpicPieceCredits 把领取期已经落账的碎片按 class0/0x047D 增量下发。
//
// 行序 = 碎片 id 升序（mail 模块已排好），每条写**绝对值**（客户端覆盖该行）。
// 发送失败不回滚任何东西：账本已经提交，客户端下一次登录会拿到全量快照
// （`sendCurrentEpicPieceBalanceSnapshot`），所以调用方只记 WARN。
func (s *Service) sendCurrentEpicPieceCredits(
	session *gameSession,
	credits []alignedcmd.MailboxEpicPieceCredit,
	source string,
) error {
	for _, credit := range credits {
		if credit.PieceID == 0 || credit.Balance == 0 {
			continue
		}
		if err := s.sendCurrentEpicPieceBalanceUpdate(session, credit.PieceID, credit.Balance, source); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────── 登录期：存量碎片清扫 ───────────────────────

// purgeCurrentInventoryEpicPieces 把角色背包（含仓库）里已有的史诗碎片按数量
// 并入账号级图鉴账本，并从背包移除。它是**幂等**的：第二次跑找不到碎片就一个字节
// 都不写。
//
// 为什么必须做：操作者背包 `0:122` 里那件 10300783 是这次事故被塞进去的，而
// 客户端每次登录都会把整个材料页渲染一遍。不清掉，截图里就永远还有"碎片在材料里"。
//
// 事务范式复用仓库既有的 `dnfrepo.CharacterItems.WithinCharacterItems`
// （与 `consumeCurrentQuestClearCoupon` 同一路径），不手搓 UPDATE。
//
// 顺序取舍（与 mail 领取期一致）：**先入账、后写背包**。反过来一旦写背包成功而
// 入账失败，碎片就真的丢了；先入账最坏是进程在两步之间死掉导致重复计入 —— 这与
// 仓库对史诗碎片"宁可留着、绝不用读改写兜底吞掉"的取向一致。
//
// 返回 (清扫的行数, 错误)。调用方按"可归因事件"记日志；这里不自己发 0x047D，
// 因为唯一的调用点在选角背包引导之前，客户端还没见过任何容器。
func (s *Service) purgeCurrentInventoryEpicPieces(
	ctx context.Context,
	session *gameSession,
	source string,
) (int, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return 0, nil
	}
	if !s.epicPieceGrantEnabled() {
		s.logGameEvent(session, "game-epic-piece-inventory-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "epic_piece_grant_disabled",
			"env", epicPieceGrantEnv)
		return 0, nil
	}
	catalog, catalogErr := s.epicPieceCatalog()
	if catalogErr != nil || catalog == nil {
		s.logWarn("dnfbridge epic piece inventory purge deferred",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"reason", "epic_piece_catalog_unavailable",
			"error", catalogErr)
		return 0, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil || repositories.CharacterItems == nil {
		s.logGameEvent(session, "game-epic-piece-inventory-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "repository_group_unavailable",
			"account_repository", ok && repositories.Account != nil,
			"character_items_uow", ok && repositories.CharacterItems != nil)
		return 0, nil
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		// fail-closed：账号未绑定就不动背包（宁可留着碎片，也不能把它记到一个
		// 错误的账号账本上）。
		s.logGameEvent(session, "game-epic-piece-inventory-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "account_id_missing")
		return 0, nil
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	now := time.Now().UTC()
	purged := 0
	err := repositories.CharacterItems.WithinCharacterItems(ctx, characterID, func(
		inventory dnfrepo.InventoryRepository,
		_ dnfrepo.EquipmentRepository,
	) error {
		record, found, err := inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || (len(record.Slots) == 0 && len(record.Warehouse) == 0) {
			return nil
		}
		slots, slotTotal := currentEpicPiecePurgeCandidates(record.Slots, catalog)
		warehouse, warehouseTotal := currentEpicPiecePurgeCandidates(record.Warehouse, catalog)
		if len(slots) == 0 && len(warehouse) == 0 {
			return nil
		}
		balances := make(map[uint32]int64, len(slotTotal)+len(warehouseTotal))
		for pieceID, count := range slotTotal {
			balances[pieceID] += count
		}
		for pieceID, count := range warehouseTotal {
			balances[pieceID] += count
		}
		before := currentEpicPieceBalancesForAccount(ctx, repositories.Account, accountID)
		pieceIDs := make([]uint32, 0, len(balances))
		for pieceID, count := range balances {
			if pieceID != 0 && count > 0 {
				pieceIDs = append(pieceIDs, pieceID)
			}
		}
		sort.Slice(pieceIDs, func(i, j int) bool { return pieceIDs[i] < pieceIDs[j] })
		for _, pieceID := range pieceIDs {
			count := balances[pieceID]
			if count > epicPieceGrantMaxCount {
				return fmt.Errorf("epic piece purge %d count %d exceeds u32", pieceID, count)
			}
			after, creditErr := addCurrentEpicPieceBalance(
				ctx,
				repositories.Account,
				accountID,
				pieceID,
				uint32(count),
				now,
			)
			if creditErr != nil {
				return fmt.Errorf("epic piece purge credit %d: %w", pieceID, creditErr)
			}
			s.logGameEvent(session, "game-epic-piece-inventory-purge-credited",
				"source", source,
				"char_id", session.selectedCharacterID,
				"account_id", accountID,
				"item_id", pieceID,
				"count", count,
				"balance_before", before[pieceID],
				"balance_after", after,
				"ledger_key", dnfrepo.AccountEpicPieceMetadataKey(pieceID))
		}
		updated := dnfrepo.CloneInventory(record)
		for _, key := range slots {
			delete(updated.Slots, key)
			purged++
		}
		for _, key := range warehouse {
			delete(updated.Warehouse, key)
			purged++
		}
		updated.UpdatedAt = now
		// MySQL persists only the explicitly dirty containers. Saving slots alone
		// leaves warehouse fragments in place and credits them again next login.
		fields := make([]dnfrepo.InventoryField, 0, 2)
		if len(slots) != 0 {
			fields = append(fields, dnfrepo.InventoryFieldSlots)
		}
		if len(warehouse) != 0 {
			fields = append(fields, dnfrepo.InventoryFieldWarehouse)
		}
		if err := dnfrepo.SaveInventoryFields(ctx, inventory, updated, fields...); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return purged, nil
}

// currentEpicPiecePurgeCandidates 返回一个容器 map 里所有"是史诗碎片"的槽位键
// （升序，便于日志与夹具稳定）以及按碎片 id 聚合的数量。
func currentEpicPiecePurgeCandidates(
	slots map[string]dnfrepo.ItemStack,
	catalog *currentEpicPieceCatalog,
) ([]string, map[uint32]int64) {
	if len(slots) == 0 || catalog == nil {
		return nil, nil
	}
	keys := make([]string, 0, 4)
	total := make(map[uint32]int64, 4)
	for key, stack := range slots {
		if stack.ItemID <= 0 || stack.Count <= 0 || stack.ItemID > int64(^uint32(0)) {
			continue
		}
		itemID := uint32(stack.ItemID)
		if !catalog.isEpicPiece(itemID) {
			continue
		}
		keys = append(keys, key)
		total[itemID] += stack.Count
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Strings(keys)
	return keys, total
}

// currentEpicPieceBalancesForAccount 读账号账本的当前余额（只用于日志的
// before/after）。读失败按空账本处理：日志缺一个数字，不该拦住清扫。
func currentEpicPieceBalancesForAccount(
	ctx context.Context,
	repository dnfrepo.AccountRepository,
	accountID string,
) map[uint32]uint32 {
	if repository == nil {
		return nil
	}
	account, found, err := repository.Load(ctx, accountID)
	if err != nil || !found {
		return nil
	}
	return dnfrepo.AccountEpicPieceBalances(account)
}
