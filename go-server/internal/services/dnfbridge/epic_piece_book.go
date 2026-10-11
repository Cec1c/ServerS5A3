package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件实现"史诗碎片 → 史诗图鉴（账号级进度）"。
//
// ── 症状 ────────────────────────────────────────────────────────────────
// 操作者：「史诗碎片进背包，应该进入史诗图鉴且账号进度同步」。
// 本服务端此前完全没有史诗图鉴：碎片只是一件普通 `[material]` 可堆叠品。
//
// ── 真档 PVF 取证（`runtime\data\dnf\Script.pvf`，1057625 个条目）──────────
//
//  1. 碎片本体：`stackable/epicPiece/<itemID>.stk`（例 `stackable/epicPiece/10300000.stk`
//     `[name] 普拉塔尼的黄金石碎片`、`[explain] 史诗装备碎片， 用于制作相应装备的材料。`、
//     `[rarity] 4`、`[stackable type] [material]`）。
//  2. 目录唯一真源：`etc/epicpieceinfo.etc` 的
//     `[equipment piece drop info] / [piece list]`，内容是一串
//     (成品装备 itemID, 碎片 itemID) 二元组；本档实测 966 组、966 个互不相同的
//     成品 id、966 个互不相同的碎片 id（碎片 id 区间 10300000..10304121）。
//  3. 碎片为什么会"进背包"：`Etc/Independent_Drop.etc` 里有 236 个碎片 id
//     （该表共 244 个 1030xxxx 道具 id）。本服务端的 `dungeon_drop_special.go` 会把这批
//     独立掉落原样投到地面，`dungeon_drop_pickup.go` 再把它们按普通材料堆进材料页
//     （`item_target_container.go:24` 的 `[material] → list 0 121..176`）——
//     这就是"史诗碎片进背包"的完整机理（真档夹具见
//     epic_piece_book_real_pvf_test.go）。
//
// ── 参考实现取证（"正确行为"长什么样）─────────────────────────────────
//
//	(a) 厂商 9.16 参考服务端（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，只读）：
//	    7/7 条 `EVENT kind=game-epic-book-snapshot-sent conn_id=… source=…after_selected_containers
//	    account_id=dnf:<hash> entries=0 wire_opcode=1149`，其中 entries=0 的
//	    body 逐字节是 `00 00 00 00 00`（5 字节）。同一档还有
//	    `EVENT kind=dnf-epic-book-catalog-loaded entries=966` —— 与真档 PVF 的 966 组
//	    二元组逐数相等，锁定目录真源。二进制里另有 `EpicBookRepository` /
//	    `mysqlEpicBookStore` / `EpicBookRows` / `EpicBookRecord` 等符号。
//	(b) C# A21（`a21zigai\ServerS4A21`）：`GenericBuilders.cs:104-137`
//	    `A21UsableCount0465BodyBuilder` = `u8 mode(0=全量/1=增量) + u32 行数 +
//	    行数×(u32 碎片 id, u32 绝对数量)`，全部小端；存 `accounts.epic_piece_counts`
//	    （LE `Int32 count[]`，按账号）；文档 `新版背包架构业务接入规范.md:337-338`
//	    明写"史诗碎片…是账号级史诗图鉴数量…不写 character_inventory_items /
//	    account_inventory_items"。
//	(c) 同血统 Go 分支（`90CN\90原神开源0.1.8\dnf90-source-oneclick-main`，
//	    `internal\services\dnfbridge\epic_piece_balance.go:19-23`）：
//	    `currentEpicPieceBalanceSyncOpcode = 0x047d`（=1149，注释给出当前 90CN 客户端
//	    的注册点 `DNF.exe sub_1D56FF0`），body 同上，落 `epic_piece:<id>` 账号元数据，
//	    拾取时"目的槽位"用 64（同档 `:21 currentEpicPiecePickupEffectSlot`）。
//
//	⇒ 三方一致：同一份 966 条目录 + 同一个 `u8 reset + u32 count + N×(u32,u32)`
//	  形状 + 按账号。本文件只实现这个形状，不发明新包。
//
// ── 边界 ──────────────────────────────────────────────────────────────
//   - 只做"碎片进图鉴 + 账号进度同步"；`[equipment recipe info]`（用碎片+材料制作成品，
//     客户端请求是 `CmdPacketEpicBookMakeItem = 788`）本批未实现，本文件保留
//     目录里的 `outputByPiece` 映射以便后续单批接入。
//   - 地面掉落保留（与参考实现一致：参考实现也先投地面、拾取时才入账），
//     只是拾取不再写背包。
//   - 邮件/GM 发放是另一条入包路径（`mailbox_*` 属别的改动域），本批未接；
//     本文件末尾的 `epicPieceCreditTarget` 就是给那条路径预留的单一判定点。

// ─────────────────────────── 开关 ───────────────────────────

const (
	epicPieceBookEnv = "DNFBRIDGE_EPIC_PIECE_BOOK"

	// epicPieceBookValueOn 是默认档：碎片入账号图鉴；角色选择引导在账号有
	// 非零进度时补一条全量快照（reset=0），拾取时发增量（reset=1）。
	epicPieceBookValueOn = "on"
	// epicPieceBookValueAlways 让全量快照无条件下发（含 entries=0 的 5 字节空表）。
	// 这就是厂商 9.16 的逐字节形状（7/7 条 entries=0 也照发）。因为本服务端的选角
	// 序列是自己演化的、顺序敏感，默认不采用；留档供实机 A/B。
	epicPieceBookValueAlways = "always"
	// epicPieceBookValueOff 是一键回退：碎片照旧当普通材料进背包，且一个图鉴包都不发。
	epicPieceBookValueOff = "off"
)

// epicPieceBookMode 是 epicPieceBookEnv 的归一化取值。
type epicPieceBookMode int

const (
	// epicPieceBookOn 默认档（零值）。
	epicPieceBookOn epicPieceBookMode = iota
	epicPieceBookAlways
	epicPieceBookOff
)

func (mode epicPieceBookMode) String() string {
	switch mode {
	case epicPieceBookAlways:
		return epicPieceBookValueAlways
	case epicPieceBookOff:
		return epicPieceBookValueOff
	default:
		return epicPieceBookValueOn
	}
}

// enabled 表示拾取史诗碎片时是否改走账号图鉴（off 档 = 回退到改动前的背包行为）。
func (mode epicPieceBookMode) enabled() bool {
	return mode != epicPieceBookOff
}

// snapshotEvenWhenEmpty 表示全量快照是否无条件下发（厂商形状）。
func (mode epicPieceBookMode) snapshotEvenWhenEmpty() bool {
	return mode == epicPieceBookAlways
}

// readEpicPieceBookMode 解析 DNFBRIDGE_EPIC_PIECE_BOOK。
// 默认 on：空/未设置/非法值都按 on；只有显式写 `off`（或 `0`/`false`/`legacy`）
// 才回到"碎片进背包"的旧行为。
func readEpicPieceBookMode(value string) epicPieceBookMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case epicPieceBookValueAlways, "vendor", "vendor_wire":
		return epicPieceBookAlways
	case epicPieceBookValueOff, "0", "false", "legacy", "disabled":
		return epicPieceBookOff
	default:
		return epicPieceBookOn
	}
}

// epicPieceBook 返回当前 Service 的归一化档位（零值 options 的测试/探针路径 = on）。
func (s *Service) epicPieceBook() epicPieceBookMode {
	if s == nil {
		return epicPieceBookOn
	}
	return s.options.epicPieceBook
}

// ─────────────────────── 碎片目录（PVF 唯一真源）───────────────────────

const currentEpicPieceInfoPath = "etc/epicpieceinfo.etc"

var errCurrentEpicPieceCatalogEmpty = fmt.Errorf("%s has no [piece list] entries", currentEpicPieceInfoPath)

// currentEpicPieceCatalog 是 `etc/epicpieceinfo.etc` 的只读索引。
// 顺序（`pieces`）按 `[piece list]` 原序保留，便于后续按"PVF 下标"对齐参考实现
// 的位置数组；本文件自己的持久化用 itemID 键，不受顺序影响。
type currentEpicPieceCatalog struct {
	pieces        []uint32
	pieceSet      map[uint32]struct{}
	outputByPiece map[uint32]uint32
	pieceByOutput map[uint32]uint32
}

func (catalog *currentEpicPieceCatalog) count() int {
	if catalog == nil {
		return 0
	}
	return len(catalog.pieces)
}

// isEpicPiece 判断一个 itemID 是否是史诗图鉴用的碎片。
func (catalog *currentEpicPieceCatalog) isEpicPiece(itemID uint32) bool {
	if catalog == nil || itemID == 0 {
		return false
	}
	_, found := catalog.pieceSet[itemID]
	return found
}

// outputFor 返回碎片对应的成品装备 itemID（`[equipment recipe info]` 的 `[output]`）。
func (catalog *currentEpicPieceCatalog) outputFor(pieceID uint32) (uint32, bool) {
	if catalog == nil {
		return 0, false
	}
	output, found := catalog.outputByPiece[pieceID]
	return output, found
}

type currentEpicPieceCatalogCacheEntry struct {
	catalog *currentEpicPieceCatalog
	err     error
}

// currentEpicPieceCatalogs 缓存目录。
//
// 键必须是 `*pvfDungeonMonsterCatalog` 指针，不能是 `dnfpvf.Source`：
// 测试与部分装配路径里的 PVF source 是 `map[string]string`（不可比较类型），
// 拿它当 `sync.Map` 的键会在第一次 `Load` 就 panic（hash of unhashable type）。
// 这与 `currentDungeonSpecialDropsByMonsterCatalog` 的取键方式一致。
var currentEpicPieceCatalogs sync.Map

func currentEpicPieceCatalogFor(
	monsterCatalog *pvfDungeonMonsterCatalog,
) (*currentEpicPieceCatalog, error) {
	if monsterCatalog == nil || monsterCatalog.source == nil {
		return nil, errDungeonDropSourceRequired
	}
	if cached, found := currentEpicPieceCatalogs.Load(monsterCatalog); found {
		entry := cached.(currentEpicPieceCatalogCacheEntry)
		return entry.catalog, entry.err
	}
	loaded, err := loadCurrentEpicPieceCatalog(monsterCatalog.source)
	entry := currentEpicPieceCatalogCacheEntry{catalog: loaded, err: err}
	actual, _ := currentEpicPieceCatalogs.LoadOrStore(monsterCatalog, entry)
	entry = actual.(currentEpicPieceCatalogCacheEntry)
	return entry.catalog, entry.err
}

// loadCurrentEpicPieceCatalog 解析 `etc/epicpieceinfo.etc` 的
// `[equipment piece drop info] / [piece list]` 二元组。
// 只收正数、只收 (成品, 碎片) 都在 u32 内的行；重复的成品或碎片都按首次出现为准
// （与 C# `EpicPieceCatalogService.Parse` 的去重口径一致）。
func loadCurrentEpicPieceCatalog(source dnfpvf.Source) (*currentEpicPieceCatalog, error) {
	if source == nil {
		return nil, errDungeonDropSourceRequired
	}
	text, err := source.ReadText(currentEpicPieceInfoPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", currentEpicPieceInfoPath, err)
	}
	document, err := dnfpvf.Parse(currentEpicPieceInfoPath, text)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", currentEpicPieceInfoPath, err)
	}
	tokens, found := document.Section("piece list")
	if !found {
		return nil, fmt.Errorf("%s is missing [piece list]", currentEpicPieceInfoPath)
	}
	values := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			values = append(values, token.Int)
		}
	}
	if len(values) == 0 || len(values)%2 != 0 {
		return nil, fmt.Errorf("%s malformed [piece list] values=%d", currentEpicPieceInfoPath, len(values))
	}
	catalog := &currentEpicPieceCatalog{
		pieces:        make([]uint32, 0, len(values)/2),
		pieceSet:      make(map[uint32]struct{}, len(values)/2),
		outputByPiece: make(map[uint32]uint32, len(values)/2),
		pieceByOutput: make(map[uint32]uint32, len(values)/2),
	}
	for index := 0; index+1 < len(values); index += 2 {
		outputID, pieceID := values[index], values[index+1]
		if outputID <= 0 || outputID > 0xffffffff || pieceID <= 0 || pieceID > 0xffffffff {
			continue
		}
		output, piece := uint32(outputID), uint32(pieceID)
		if _, duplicate := catalog.pieceSet[piece]; duplicate {
			continue
		}
		if _, duplicate := catalog.pieceByOutput[output]; duplicate {
			continue
		}
		catalog.pieces = append(catalog.pieces, piece)
		catalog.pieceSet[piece] = struct{}{}
		catalog.outputByPiece[piece] = output
		catalog.pieceByOutput[output] = piece
	}
	if len(catalog.pieces) == 0 {
		return nil, errCurrentEpicPieceCatalogEmpty
	}
	return catalog, nil
}

// currentEpicPieceCatalogUnavailableLogged 保证"目录加载失败"每个 PVF 只告警一次
// （拾取是高频路径，不能每捡一件刷一条）。键同样用 monster catalog 指针。
var currentEpicPieceCatalogUnavailableLogged sync.Map

// epicPieceCatalog 用当前会话的运行时 PVF source 取目录。
//
// 取不到目录时不改变行为（碎片照旧走背包路径），但必须留一条 WARN：否则
// "碎片又进背包了"会被误判成开关没生效。绝不用 id 区间兜底——10303749 就在
// 10300000..10304121 区间里，却是 `装备增幅保护券`，区间判断会误伤。
func (s *Service) epicPieceCatalog() (*currentEpicPieceCatalog, error) {
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	catalog, catalogErr := currentEpicPieceCatalogFor(monsterCatalog)
	if catalogErr == nil {
		return catalog, nil
	}
	if _, logged := currentEpicPieceCatalogUnavailableLogged.LoadOrStore(monsterCatalog, struct{}{}); !logged {
		s.logWarn("dnfbridge epic piece catalog unavailable",
			"path", currentEpicPieceInfoPath,
			"env", epicPieceBookEnv,
			"fallback", "epic fragments keep the legacy character-bag [material] path",
			"error", catalogErr)
	}
	return nil, catalogErr
}

// ─────────────────────── 账号账本 + 下行包 ───────────────────────

const (
	// currentEpicPieceBalanceSyncOpcode 是史诗图鉴账本通知。
	//
	// 证据链：
	//   - 厂商 9.16 参考服务端实测 `SEND msg_id=1149 classification=0 len=21
	//     hex=007d0415000000… 0000000000`，事件名
	//     `game-epic-book-snapshot-sent … wire_opcode=1149`（7/7 条）。
	//   - 同血统 Go 分支 `epic_piece_balance.go:19`：`0x047d`，注释给出当前 90CN
	//     客户端注册点 `DNF.exe sub_1D56FF0`；并注明 `0x047E` 是本客户端另一条
	//     定长 5 字节通知，会静默丢弃史诗碎片行。
	currentEpicPieceBalanceSyncOpcode uint16 = 0x047d
	// currentEpicPieceBalanceMinBodySize 是 entries=0 的空表长度
	// （厂商实测 body 逐字节 `00 00 00 00 00`）。
	currentEpicPieceBalanceMinBodySize = 5
	// currentEpicPiecePickupEffectSlot 是碎片拾取回包里的"目的槽位"。
	// 碎片不进背包，参考实现固定写一个非 0 的哨兵槽位（0.1.8 同名字段 = 64；
	// C# `DropItemBuilder.BuildPickupEpicPiece` 写 0）。本服务端的 op39 回包
	// 校验 `destinationSlot != 0`，所以取 64。
	currentEpicPiecePickupEffectSlot uint16 = 64
	// currentEpicPieceBalanceMaxEntries 是单条快照允许的最大行数。
	// 真档目录 966 条，留 4 倍余量；超过就拒绝整包（不截断，避免客户端把
	// "截断后的账本"当真值）。
	currentEpicPieceBalanceMaxEntries = 4096
)

// buildCurrentEpicPieceBalanceSyncBody 序列化史诗图鉴账本通知：
// `u8 reset(=0 全量/清空后重建, =1 增量) + u32 行数 + 行数×(u32 碎片 id, u32 绝对数量)`，
// 行按碎片 id 升序（与 0.1.8 `buildCurrentEpicPieceBalanceSyncBody` 逐字节一致，
// 也让同一份账本的字节输出稳定、可做夹具）。
func buildCurrentEpicPieceBalanceSyncBody(balances map[uint32]uint32, resetClient bool) ([]byte, error) {
	ids := make([]uint32, 0, len(balances))
	for pieceID, balance := range balances {
		if pieceID != 0 && balance != 0 {
			ids = append(ids, pieceID)
		}
	}
	if len(ids) > currentEpicPieceBalanceMaxEntries {
		return nil, fmt.Errorf(
			"dnf epic piece balance rows=%d exceed %d",
			len(ids),
			currentEpicPieceBalanceMaxEntries,
		)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var writer packetWriter
	if resetClient {
		writer.writeByte(0)
	} else {
		writer.writeByte(1)
	}
	writer.writeUint32(uint32(len(ids)))
	for _, pieceID := range ids {
		writer.writeUint32(pieceID)
		writer.writeUint32(balances[pieceID])
	}
	return writer.bytes(), nil
}

// addCurrentEpicPieceBalance 在账号级账本上原子加 amount，返回新绝对值。
func addCurrentEpicPieceBalance(
	ctx context.Context,
	repository dnfrepo.AccountRepository,
	accountID string,
	pieceID uint32,
	amount uint32,
	now time.Time,
) (uint32, error) {
	return dnfrepo.AddAccountEpicPieceBalance(ctx, repository, accountID, pieceID, amount, now)
}

// sendCurrentEpicPieceBalanceSnapshot 在角色选择引导里补发全量账本（reset=0）。
//
// 位置与厂商参考一致：厂商的 `source=` 字符串是
// `initial_town_before_final_transition_after_actor_before_full_player_state_after_selected_containers`，
// 即"actor 已绑定、六个容器之后、完整 mode1 之前"——本服务端对应的插入点就是
// `sendCurrentSelectInventoryBootstrap` 里物品锁快照之后。
func (s *Service) sendCurrentEpicPieceBalanceSnapshot(
	ctx context.Context,
	session *gameSession,
	source string,
) error {
	if session == nil {
		return nil
	}
	mode := s.epicPieceBook()
	if !mode.enabled() {
		s.logGameEvent(session, "game-epic-piece-balance-snapshot-skipped",
			"source", source,
			"reason", "epic_piece_book_disabled",
			"env", epicPieceBookEnv)
		return nil
	}
	// 下行线档位（DNFBRIDGE_EPIC_PIECE_PUSH）：off 档一个图鉴包都不发。
	// 账本与背包口径不受影响 —— 这里只决定线上有没有这个包。
	push := s.epicPiecePush()
	if !push.snapshotEnabled() {
		s.logGameEvent(session, "game-epic-piece-balance-snapshot-skipped",
			"source", source,
			"reason", "epic_piece_push_suppressed",
			"env", epicPiecePushEnv,
			"mode", push.String())
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil {
		return dnfrepo.ErrAccountEpicPieceCounterUnavailable
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	balances := map[uint32]uint32{}
	found := false
	if accountID != "" {
		account, accountFound, err := repositories.Account.Load(ctx, accountID)
		if err != nil {
			return fmt.Errorf("load account epic pieces: %w", err)
		}
		found = accountFound
		if accountFound {
			balances = dnfrepo.AccountEpicPieceBalances(account)
		}
	}
	if push.forceEmptySnapshot() {
		// 厂商逐字节形状：body 恒为 `00 00 00 00 00`（reset=0 + 行数 0）。
		// 账本里的进度只留在服务端，客户端这一档拿不到任何非空行。
		body, err := buildCurrentEpicPieceBalanceSyncBody(nil, true)
		if err != nil {
			return err
		}
		s.logGameEvent(session, "game-epic-piece-balance-snapshot-send",
			"source", source,
			"account_id", accountID,
			"entries", 0,
			"ledger_entries", len(balances),
			"body_len", len(body),
			"msg_id", currentEpicPieceBalanceSyncOpcode,
			"classification", 0,
			"reset", true,
			"mode", push.String(),
			"state_source", "vendor_9_16_empty_table_shape")
		return s.sendGameUpperRawClass(session, currentEpicPieceBalanceSyncOpcode, body, 0)
	}
	if len(balances) == 0 && !mode.snapshotEvenWhenEmpty() && !push.snapshotEvenWhenEmpty() {
		// 空账本对客户端没有任何信息量，而选角序列是顺序敏感区：默认档不追加
		// 这个包（`always` 档才逐字节复刻厂商 7/7 条 entries=0 的形状）。
		s.logGameEvent(session, "game-epic-piece-balance-snapshot-skipped",
			"source", source,
			"account_id", accountID,
			"reason", "account_has_no_epic_piece_balance",
			"account_found", found,
			"mode", mode.String(),
			"push_mode", push.String())
		return nil
	}
	body, err := buildCurrentEpicPieceBalanceSyncBody(balances, true)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-epic-piece-balance-snapshot-send",
		"source", source,
		"account_id", accountID,
		"entries", len(balances),
		"body_len", len(body),
		"msg_id", currentEpicPieceBalanceSyncOpcode,
		"classification", 0,
		"reset", true,
		"mode", push.String(),
		"state_source", "account_metadata_epic_piece_counters_and_runtime_pvf_epicpieceinfo")
	return s.sendGameUpperRawClass(session, currentEpicPieceBalanceSyncOpcode, body, 0)
}

// sendCurrentEpicPieceBalanceUpdate 发增量账本（reset=1）；balance 必须是绝对
// 数量（客户端据此覆盖该碎片那一行的值）。
//
// 它是**账本下行线唯一的增量出口**（地面拾取尾巴与邮件/GM 领取补发都走这里），
// 所以 DNFBRIDGE_EPIC_PIECE_PUSH 的档位判定只放在这一处：只有 `always` 档
// （= 改动前的逐字节行为）才真的发；其余档位记账照旧、包一个不发。
func (s *Service) sendCurrentEpicPieceBalanceUpdate(
	session *gameSession,
	pieceID uint32,
	balance uint32,
	source string,
) error {
	if session == nil {
		return nil
	}
	push := s.epicPiecePush()
	if !push.updateEnabled() {
		s.logGameEvent(session, "game-epic-piece-balance-update-skipped",
			"source", source,
			"reason", "epic_piece_push_suppressed",
			"account_id", strings.TrimSpace(s.accountIDForSession(session)),
			"piece_id", pieceID,
			"absolute_balance", balance,
			"msg_id", currentEpicPieceBalanceSyncOpcode,
			"classification", 0,
			"env", epicPiecePushEnv,
			"mode", push.String())
		return nil
	}
	body, err := buildCurrentEpicPieceBalanceSyncBody(
		map[uint32]uint32{pieceID: balance},
		false,
	)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-epic-piece-balance-update-send",
		"source", source,
		"account_id", strings.TrimSpace(s.accountIDForSession(session)),
		"piece_id", pieceID,
		"absolute_balance", balance,
		"body_len", len(body),
		"msg_id", currentEpicPieceBalanceSyncOpcode,
		"classification", 0,
		"reset", false,
		"mode", push.String())
	return s.sendGameUpperRawClass(session, currentEpicPieceBalanceSyncOpcode, body, 0)
}

// epicPiecePickupFollowups 是"碎片已入账"之后的下行尾巴：只发增量账本。
//
// 与 0.1.8 的 `sendCurrentEpicPiecePickupFollowups` 的唯一差别是不发主背包刷新：
// 参考实现那一版会先补一条 list 0 的整容器刷新，而本服务端这条路径一个背包槽位都没动
// （碎片不写 character_items），补刷新只会让客户端重建一次无变化的材料页。
// 拾取本身的"到手特效"由 op39 回包（目的槽位 64）承担，形状与参考一致。
func (s *Service) epicPiecePickupFollowups(
	session *gameSession,
	pieceID uint32,
	balance uint32,
	source string,
) error {
	if session == nil || pieceID == 0 || balance == 0 {
		return nil
	}
	if s.options.currentClientVersion == "official-24.9" {
		// Native NOTI39 202EC26..202EC55 inserts the scene item into its
		// destination slot. A book-only pickup's sentinel 64 is not an asset
		// destination: restore that slot from the real repository, including
		// its pre-existing item, so no client-only fragment stack remains.
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "epic_piece_pickup_authoritative_sentinel_slot", []alignedcmd.ItemSlotRefresh{{ListType: dnfrepo.MainInventoryListType, SlotIndex: int16(currentEpicPiecePickupEffectSlot)}}); err != nil {
			return err
		}
	}
	return s.sendCurrentEpicPieceBalanceUpdate(session, pieceID, balance, source)
}

// epicPieceBookFields 把"这件掉落是史诗碎片"的固定字段拼进调用方自己的 key/value 列表。
func epicPieceBookFields(
	catalog *currentEpicPieceCatalog,
	pieceID uint32,
	fields ...any,
) []any {
	output, found := catalog.outputFor(pieceID)
	return append([]any{
		"epic_piece", true,
		"epic_piece_output_item_id", output,
		"epic_piece_output_known", found,
		"epic_piece_catalog_entries", catalog.count(),
	}, fields...)
}

// epicPieceCreditTarget 是"这件道具要不要改走史诗图鉴"的唯一判定点。
//
// 之所以单独抽出来：碎片入包不止"地面拾取"一条路径（邮件附件领取、GM 发放、
// 盒子/礼包产出都在别处）。任何一条路径只要在写入背包之前调用它、并按结果改走
// `epicPiecePickupFollowups` + 账号账本，语义就与参考实现一致，不需要各自再判一次 PVF。
// 返回 (pieceID, true) 表示这件道具是图鉴碎片。
func epicPieceCreditTarget(
	service *Service,
	itemID uint32,
) (uint32, bool) {
	if service == nil || itemID == 0 || !service.epicPieceBook().enabled() {
		return 0, false
	}
	catalog, err := service.epicPieceCatalog()
	if err != nil || !catalog.isEpicPiece(itemID) {
		return 0, false
	}
	return itemID, true
}
