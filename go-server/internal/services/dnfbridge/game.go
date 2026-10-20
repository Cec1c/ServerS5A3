// 本文件负责 DNF bridge 的 game 端口连接、混合帧分发和登录前 wire 回包。
// 这里只处理客户端字节协议兼容，不持有玩家持久化状态或玩法规则。
package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dproto"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type gameSession struct {
	fiendWarPendingMove                 *currentFiendWarMovePlan // 由 townMu / 当前会话事件循环保护。
	fiendWarProjection                  currentFiendWarProjection
	pvpClientGameType                   uint32 // 本次客户端选择的匹配类型，原子读取与更新。
	auctionLastSearchOpcode             uint16
	auctionLastSearchBody               []byte
	auctionServiceOpenMask              byte
	guildBuffs                          guildBuffProjection
	guild                               guildSessionState
	initialTownGuildStateProjectionSent bool
	pendingTitleEnchantCharacterID      uint16
	pendingTitleEnchantRow              *currentItemListEntry
	pendingGuardianGemCharacterID       uint16
	pendingGuardianGemRow               *currentItemListEntry
	conn                                net.Conn
	connID                              string
	accountID                           string
	clientPID                           uint32
	pvpPrivateCreate                    *currentPVPPrivateIntent
	pvpRoom                             *currentPVPRoom // 由 Service.pvpMu 保护。
	// clientIP 只用于日志与排查（连接来源），**绝不参与账号判定** ——
	// 见 repository_access.go 的 accountIDForSession 与 client_account_ip.go。
	clientIP string
	// accountFromPID 表示账号来自"客户端进程号 → 账号"的本地登记
	// （仅 Windows 本机一键端可解析，见 client_account_registry.go）。
	accountFromPID bool
	// accountFromToken 表示账号来自启动器登录令牌，这是唯一权威来源。
	accountFromToken bool
	// loginTokenSeen 表示登录包里确实读到了 32 位十六进制令牌（无论是否解析出账号）。
	loginTokenSeen bool
	// loginPacketSeen 表示客户端已经交过 class1/op1 登录包：此后账号不会再变。
	loginPacketSeen bool
	// 只在令牌已验证后设置，用于关联同一登录会话的明确换线交接，不作为账号认证。
	loginChannelFlowKey [32]byte
	channelExitHandoff  *currentAuthenticatedChannelHandoff
	// sharedAccountFallbackReason 记录"为什么会落到共享兜底账号"，
	// 取值见 repository_access.go 的 sharedAccountReason* 常量。
	sharedAccountFallbackReason string
	// sharedAccountWarnOnce 保证每条会话只写一条"落到共享账号"的 WARN。
	sharedAccountWarnOnce sync.Once
	channel               channelcatalog.Channel
	residentChannel       channelcatalog.Channel
	coldChannelLanding    *currentColdChannelLanding // 首次选角前的受控频道更正，由会话事件循环保护。
	packetSeq             uint64
	sequence              uint32
	upperSeq              uint16
	ctx                   context.Context
	// wireMu owns the complete per-session wire boundary: sequence
	// allocation, DPROTO state transitions, and socket writes.  A gameplay
	// timer can send concurrently with the connection reader, so callers
	// must not allocate a sequence before acquiring this lock.
	wireMu            sync.Mutex
	dproto            dproto.Session
	channelSceneBatch *currentChannelSceneBatch // 仅普通换线；wireMu保护。

	// Image-communication identity is guarded by townMu. Scene resets advance
	// its generation; the nonce belongs only to this connection.
	imageCommunicationNonce           [32]byte
	imageCommunicationSceneGeneration uint64

	endpointHandshakeMu                     sync.Mutex
	currentChannelResidentNoticeSent        bool
	gameEndpointSuccessSent                 bool
	connectionTownActorOwnerChannel         byte
	events                                  *gameSessionEvents
	characterGeneration                     uint64
	raidProjection                          currentRaidProjectionOwner
	selectedCharacterID                     uint16
	questManualClearIdentity                boundGameSessionCharacter
	questManualClearReadyAt                 time.Time
	currentAccountFriendNames               map[string]struct{}
	currentAccountFriendInfo                currentAccountFriendSentView
	currentAdventureExpeditionView          currentAdventureExpeditionSentView
	rosterRequested                         bool
	currentRenameRoster                     []dnfrepo.CharacterRecord
	currentRenameRosterPrepared             []dnfrepo.CharacterRecord
	channelReconnect                        bool
	townActorOwnerChannel                   byte
	sceneBootstrapActorGeneration           uint64
	darkKnightCombo                         darkKnightComboSessionState
	initialTownGuardianShieldDeckSent       bool
	initialTownGuardianShieldAppearanceSent bool
	// loginWearReassertWindowOpenedAt / ...Consumed 界定"DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP
	// 兜底"可生效的登录引导窗口（开启=场景就绪 ack，关闭=一次性或超时）。
	// 开关默认关闭时两者始终为零值，行为与历史一致。
	// 见 equip_login_wear_window.go。
	equipLoginWearWindowOpenedAt time.Time
	equipLoginWearWindowConsumed bool
	// equipWornSlotClaimEpoch / equipWornSlotClaimCounts 是"声明装备槽穿戴态"那一族
	// op19 的**逐槽出现序号账本**：键 = 装备端点 list3 的槽位，值 = 该槽在
	// equipWornSlotClaimEpoch 这一纪元里出现过几条。
	//
	// epoch 边界（见 equip_worn_slot_claim_occurrence.go 的长注）：
	//   epoch = 连接（= 一次登录/选角）+ 场景就绪轮次（equipLoginWearReassertRound）。
	//   也就是说：换角色/重登（新 conn）与每次场景就绪 ack（新一轮）都会把它重置，
	//   所以"连续换角色时第 2 个角色的第 1 条"永远算第 1 条（不会把 09-28 那条老症状带回来）。
	// 只有本会话的 op19 读路径读写它。
	equipWornSlotClaimEpoch  string
	equipWornSlotClaimCounts map[int16]int
	// equipWornSlotClaimOccurrence 是**本条** op19 的序号（由 game_aligned.go 记一次后暂存），
	// 供桥侧吸收判定（equip_login_wear_family_window.go）与域侧请求字段共用。
	// 0 = 本条不是那一族形状 / 未计数 / 开关写 all。
	equipWornSlotClaimOccurrence int
	// equipWornSlotClaimBurst* 是"客户端复述批"的批延续判据（同落点 + 间隔 <0.5 s + 换槽）。
	// 见 equip_worn_slot_claim_occurrence.go。
	equipWornSlotClaimBurstSeen    bool
	equipWornSlotClaimBurstBagSlot int16
	equipWornSlotClaimBurstBagList byte
	equipWornSlotClaimBurstSlot    int16
	equipWornSlotClaimBurstAt      time.Time
	// mirrorReequip* 是"甲：镜像回穿抑制"的落库记录：我们刚把 (item, slot) 真落库脱下；
	// 若客户端随即（窗口内）发来把同一件穿回去的镜像包，就判 ClientEcho noop。
	// 见 equip_mirror_requip_echo.go。
	mirrorReequipItemID int32
	mirrorReequipSlot   int16
	mirrorReequipAt     time.Time
	// op19QuietPending / op19QuietResolving 是「静默窗」挂起态：一条 `tail==1` 的 op19 先挂起，
	// 只有它之后 quiet_ms 内没有第二条 op19 时才按厂商口径执行（否则按门禁路径吞掉）。
	// **只在读 goroutine 上读写**（挂起、结算、读循环都在同一条 goroutine）⇒ 不需要锁、
	// 也没有 goroutine/定时器 ⇒ 会话结束即随结构体丢弃，不会泄漏。
	// 见 equip_player_flag_quiet_window.go。
	op19QuietPending   []byte
	op19QuietResolving bool
	// equipLoginWearReassertSlots 是"每槽复述处理窗口"账本（键 = 装备端点 list3 的槽位，
	// 值 = 该槽本会话第一次被兜底吸收的时刻）。只在
	// DNFBRIDGE_EQUIP_LOGIN_REASSERT_PER_SLOT=on（默认修复档）时写入；
	// 回退档（PER_SLOT=off）下它始终是 nil，闸门判定与改动前逐字节相同。
	// 只有本会话的 op19 读路径读写它（与上面两个窗口字段同一访问口径）。
	// 见 equip_login_wear_reassert_anchor.go。
	equipLoginWearReassertSlots map[int16]loginWearReassertSlotEpoch
	// equipLoginWearReassertRound 是"复述轮次"计数器：每收到一次场景就绪 ack
	// （legacy type=1345，= 客户端一次场景进入）自增一次；该槽的每槽账本记的是
	// "上一次被吸收发生在哪一轮"，因此新的一轮里第一次重述同一句装备端点陈述时，
	// 该槽的复述处理窗口会重新开在本轮。默认档（DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND
	// =absorb）下它才有意义；回退档（session）下它恒为 0，每槽账本仍然是"每会话一次"。
	// 见 equip_login_wear_reassert_round.go。
	equipLoginWearReassertRound int
	// lastPublishedPartyLeader / lastPublishedPartyID 记录本会话**最近一次**收到的
	// 权威队伍名单（队长与队伍号）。用途只有一个：把客户端对同一次"委任队长"操作的
	// 重发 op121 认出来（幂等），不要再退役一代队伍记录。
	// 见 handleOnlineChangePartyHost 的幂等分支。
	lastPublishedPartyLeader uint16
	lastPublishedPartyID     int
	// dungeonPeerActorResent 是"副本内队友 actor 补发"的幂等表（键 = 房间 + 地图 + party）。
	// 只在 session.dungeonPartyRunMu 下读写，见 dungeon_party_peer_actor_resend.go。
	dungeonPeerActorResent map[dungeonPeerActorResendKey]bool
	// vendorInjectEmitted 是"厂商登录相位包注入"的每会话幂等表（键 = 注入表行号）。
	// 只在 session.wireMu 下读写，见 upper_vendor_inject.go。
	vendorInjectEmitted                  map[int]bool
	representAccountNamePending          bool
	representAccountNameRegistrationSent bool
	pendingCharacterRosterBootstrap      bool
	emptyRosterSlotProbePending          bool
	selectorAdventureInfoPending         bool
	selectorAdventureInfoSlot            uint16
	enterSelectDungeonSent               bool
	// tournamentRewardStateRequested 记录"本会话这一轮锦标赛里，客户端已经发来过体长为 0 的
	// 457（= 翻牌选奖界面已打开、在等选奖）"。它只在 session.dungeon.mu 下读写，只在
	// handleCurrentTournamentRewardCommand 的空 body 分支置位。3s 兜底自动选奖（source=timer）
	// 只有在它已置位时才会回 458 选中投影 —— 线上实证客户端从不主动发 2 字节选奖，
	// 不给兜底路径回 7 字节，翻牌界面永远推进不了（操作者"青龙卡翻牌只能返回城镇"）。
	// 见 dungeon_tournament_flow.go 的 sendCurrentTournamentRewardSelectionAckLocked。
	tournamentRewardStateRequested bool
	enterSelectDungeonAckSent      bool
	// itemListMigrationsDone 记录本会话是否已经跑过"账号金库合并/复活币钱包"
	// 这两个迁移；它们会开事务并读整份背包，不能每次重建主背包都跑。
	itemListMigrationsDone        bool
	enterSelectDungeonContextSent bool
	// partyDungeonEntryRoutedPendingCharacter 记录"这名角色的 op16 已经被回 ACK、正在等
	// 队长那条整队进图流程给出结果"（0 = 没有在等）。文档段 104：那条流程在提交之前被
	// 拒绝时，必须把本挂起态解掉并补一条既有形状的拒绝包，否则队员客户端只看到倒计时。
	// 只在 session.dungeon.mu 下读写，见 party_entry_refusal_before_commit.go。
	partyDungeonEntryRoutedPendingCharacter uint16
	// partyDungeonEntryRoutedPendingGeneration 是写入挂起态时的会话 generation（取自
	// boundGameSessionCharacterSnapshot，在 s.mu 下读；换角会让 generation 前进一步）。
	// partyDungeonEntryRoutedPendingSeq 是每次绑定都递增的请求序号：队长那条流程只应答
	// "自己那次请求"（角色号 + generation + seq 三者都匹配），因此同角色连续两次按进图时，
	// 旧那条流程的拒绝不会吞掉新那条请求的挂起态。三者都只在 session.dungeon.mu 下读写，
	// 见 party_entry_refusal_before_commit.go。
	partyDungeonEntryRoutedPendingGeneration uint64
	partyDungeonEntryRoutedPendingSeq        uint64
	// hellBlocked 是「深渊派对准入逐槽位判定」的会话缓存：只在
	// `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档有内容，blocked 槽位随下一条 class0/op27
	// 进本选择页下发（`List0`）。默认档恒为零值 ⇒ 下发体逐字节等于今天。
	// 见 dungeon_hell_blocked_slots.go。
	hellBlocked                          hellBlockedSlotRecord
	backToVillageEnterSelectPending      bool
	backToVillageEnterSelectArmedAt      time.Time
	completedTownSceneTailOwner          *currentCompletedTownSceneTailOwner
	sceneBootstrapTailDeferred           bool
	sceneBootstrapTailSent               bool
	sceneBootstrapTailPacketIndex        int
	sceneBootstrapTailObjectMode1Pending bool
	sceneBootstrapTailPostStage          currentDeferredSelectSceneTailPostStage
	runtimeAfterBlacklistSent            bool
	runtimeFinishLoadingGateSent         bool
	fpsFinishLoadingGateSent             bool
	selectedUserInfoRefreshSent          bool
	selectedUserInfoMode3Sent            bool
	currentSceneObjectListSent           bool
	selectedItemListRefreshSent          bool
	selectedItemListBootstrapCharacterID uint16
	selectedEquipmentUpdateSent          bool
	selectedCreatureStateTableSent       bool
	selectedRentalWalletStateSent        bool
	expertJobInfoCharacterID             uint16
	selectPreviewObjectStateSent         bool
	selectPreviewActorOwnerChannel       byte
	selectPreviewActorRemoved            bool
	preDungeonContextPlayerStateSent     bool
	postStartMapPlayerStateSent          bool
	// dungeonPartySlotTableSentOnStartMap 记录"本次 start-map（op29）提交里，class0/op9
	// kind0 槽位表已经发过一条"（厂商形状的房间槽位表，见 dungeon_party_room_slot_table.go）。
	// 进图那一次提交里既有的 `…_post_start_map` 名册帧据此让位
	// （DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE=vendor_only），避免同一个读条窗口里
	// 对同一个 actor 把队伍槽位表声明两次。每次 start-map 提交在
	// sendPartyDungeonEntryTailAfterOp28 开头复位。
	dungeonPartySlotTableSentOnStartMap bool
	// pendingPostStartMapPlacement 是"落位包已经该发、但被全队读条屏障压住"的记录；
	// pendingPostStartMapMu 保护它（屏障放行发生在另一个会话的 goroutine 上）。
	// 见 party_dungeon_loading_barrier.go。
	pendingPostStartMapMu              sync.Mutex
	pendingPostStartMapPlacement       *pendingPostStartMapPlacement
	deferredDungeonUserStateObjectKey  uint16
	currentFinishLoadingStateSent      bool
	currentFinishLoadingCompletionSent bool
	postFinishLoadingPlayerStateSent   bool
	returnTownFinishLoadingAckOnly     bool
	confirmedDungeonReturnStatePending bool
	initialTownRouteCharacterID        uint16
	initialTownRouteStage              currentInitialTownRouteStage
	initialTownActorSceneSnapshotSent  bool
	// gameOptionNoticeSent guards the once-per-entry class0/0x00AD + 0x01C7
	// account settings replay (game_option_settings.go).
	gameOptionNoticeSent                 bool
	selectedHotkeyNoticeCharacterID      uint16
	selectedHotkeyNoticeGeneration       uint64
	characterOptionNoticeSent            bool // per selected entry, independent of account replay
	initialTownLocationNotificationsSent bool
	initialTownQuestSnapshotsSent        bool
	initialTownSkillInfoPrepared         bool
	initialTownSkillInfoSent             bool
	initialTownSkillInfo                 currentSceneSkillInfoProjection
	initialTownLegacySceneReadyAccepted  bool
	// Snapshot of the monotonic town cursor when this selector route starts.
	initialTownRouteTownGeneration uint64
	// Completed returns share the town packet builder, but their actor was
	// already reconstructed and must not enter the cold-login replay hook.
	initialTownRouteCompletedReturn bool
	// loginActorGenerationRebuildArmed 在登录路径的 legacy type1345 场景就绪 ack 上布防，
	// loginActorGenerationRebuildSent 保证一次进场只重建一次 actor 世代。
	// 触发面 = **ack 之后的第一条客户端游戏报文**（`handleGameStreamPacket` 在派发前取
	// armed 快照，因此 ack 那一包自己不会触发）。判据、安全边界与开关见
	// login_actor_generation_rebuild.go。
	loginActorGenerationRebuildArmed bool
	loginActorGenerationRebuildSent  bool
	// loginMode1Trace 记录登录路由上**已经发出**的每一帧 class0/op2 mode=1
	// （`login_mode1_last_before_commit.go`）：它把"提交前最后一件 mode1 载体是哪一帧、
	// 带不带装备 create 行"变成 packet_log.txt 里可 grep 的事实；若最后一帧不带装备行，
	// 同一文件会在 typed op24 之前把**本会话已经发过的那一份装备 mode1** 原样重发为最后一帧。
	loginMode1Trace                         []loginMode1SendRecord
	initialTownAdventureOverheadRefreshSent bool
	initialTownCombatPowerAffixesSent       bool
	townPostTransition                      townPostTransitionState
	crystalContractMu                       sync.Mutex
	crystalContractTownUIReadyStateSent     bool
	auraSkinMu                              sync.Mutex
	auraSkinTownUIReadyStateSent            bool
	townSceneReadyCharacterID               uint16
	townPositionSnapshot                    currentTownPositionSnapshot
	townPrevVillageSnapshot                 currentTownPositionSnapshot
	// townSeriaRoomEntrySnapshot 记录"这一次是从哪个 (城镇, 区域, 坐标) 走门进赛丽亚房间的"，
	// 只在 op36 进房那条路径（town_move.go）绑定。出房间的落点用它回到**进来时所在的那个
	// 城镇**（操作者报障：从西海岸进的房间，出来却落到艾尔文防线）。
	// 它与 townPrevVillageSnapshot 刻意分开：后者在"登录直接落在房间"时来自持久化的
	// 上一位置（可能是副本门口 38/2），不是真正的进门来源。
	townSeriaRoomEntrySnapshot currentTownPositionSnapshot
	// townCopresenceResendKey 是"城镇同屏 actor 在收包方 finish-loading 之后补发一次"
	// 的幂等键（角色/城镇/区域/对端集合），见 town_copresence_actor_resend.go。
	townCopresenceResendMu          sync.Mutex
	townCopresenceResendKey         string
	townSelectorOriginSnapshot      currentTownPositionSnapshot
	townSelectorOriginBound         bool
	townTransportEnterSelectPending bool
	// townTransportEnterSelectArmedAt 是上面这条挂账的布防时刻（与兄弟 backToVillage-
	// EnterSelectArmedAt:115 同形）。挂账只应覆盖"城镇 transport 提交后紧跟的那条场景收尾
	// op15"；没有时刻就无法把"几分钟后玩家自己在副本门口撞的那条 op15"区分出来 ——
	// 线上实测（2026-09-28）20 次吞包中有 4 次发生在布防之后 132/299/942/1564 行日志，
	// 2 次吞掉之后玩家那一局再没有任何 op15（选图界面从来没出现过）。
	// 判据与窗口见 game_enter_select_dungeon.go 的 currentTownTransportEnterSelectWindow。
	townTransportEnterSelectArmedAt time.Time
	returnSelectTownReentryPending  bool
	// townTeleportPotionLandingRelaxed 只在 handleTeleportPotion 提交那一次 op36
	// 期间为 true（town_teleport.go 设置/清除，handleTownSetUserArea 读取），作用是不把
	// 目标区域的 `[need level]`/`[need quest]` 门槛套在道具传送上 —— 道具自己的 PVF
	// 说明是"传送到其他地区的任意位置"，门槛是给走门进图那条链路用的。
	// 开关：DNFBRIDGE_TELEPORT_POTION_AREA_GATES（默认 relaxed，回退值 strict）。
	townTeleportPotionLandingRelaxed bool
	questReplay                      questReplayState
	party                            partySessionState
	partyPeerMu                      sync.Mutex
	partyPeer                        currentPartyPeerEndpointRegistration
	lottery                          lotteryState
	dungeon                          dungeonSessionState
	// dungeonPartyRun 记录"这一场副本"的运行键（队伍 + 副本 + 迷宫 + 房间 + 地图 + 种子），
	// 用于给队友 actor 同屏投影把关；per-session 运行时下它是唯一的"同一场"判据。
	// 详见 dungeon_party_copresence.go。
	dungeonPartyRunMu    sync.Mutex
	dungeonPartyRun      dungeonPartyRunKey
	dungeonPartyRunValid bool
	dungeonPeerProjected map[uint16]bool
	// dungeonOwnerOverrideLogged 是 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE=channel 时
	// "owner 覆盖"单次日志的去重集合（键 = 队友 actor object_key）。与 dungeonPartyRun
	// 共用 dungeonPartyRunMu，换场时一起清空；默认 zero 时始终为空。
	dungeonOwnerOverrideLogged map[uint16]bool
	townMu                     sync.Mutex
	// Serializes an op19 owner mutation and its ACK/slot projections with a
	// taken appearance tail. Lock before party/town/avatar/wire locks; a
	// synchronous post-action already runs inside the mutation's lock.
	equipmentProjectionMu       sync.Mutex
	avatarAppearanceMu          sync.Mutex
	avatarAppearanceLastAt      time.Time
	avatarAppearanceTimer       string
	avatarAppearanceTimerSerial uint64
	petGrowth                   petGrowthClockState
	spendTime                   currentSpendTimeClockState
	// Owned by the serialized session event loop; full catalogs omit yesterday's flag.
	activityCatalogDayEvent uint16
	weeklyAttendance        currentWeeklyAttendanceProjection
	monthlyAttendance       currentMonthlyAttendanceProjection
	// collectbox* 是「收集箱」的会话级状态（见 collectbox_state.go）。
	// selectedIndex 以 PVF 箱号为准，0 = 尚未定过（初始化时按"最后一个未过期箱"取默认）；
	// initialized 只用于判断客户端开窗前是否已经收到过整组缓存。
	collectboxSelectedIndex int32
	collectboxInitialized   bool
	// boxOpenRewardListTypes / boxOpenRewardRecorded 是**最近一次开盒命令**实际发放到的
	// 奖励容器（list 类型，0 = 主背包）：只有
	// DNFBRIDGE_BOX_OPEN_CONTAINER_REFRESH=changed_only 时才会在
	// game_aligned.go 分发 op208/op1128 之前复位、并由包装后的奖励模板解析器写入，
	// 由同一个 goroutine 里的回包后置动作读取。默认档这两个字段始终是零值。
	// 见 box_open_container_refresh.go。
	boxOpenRewardListTypes []byte
	boxOpenRewardRecorded  bool
	// itemGrantNoticeArmed / itemGrantNoticeContainers 是**最近一次开盒子命令**在
	// delta 档（DNFBRIDGE_ITEM_GRANT_NOTICE 的默认档）下的发放通知计划：逐槽增量已经在
	// `sendAlignedUpperResponses` 里发过，这里只记"仍需整容器回放的例外容器"（首刀只有
	// list 38）供回包后置动作读取。containers 档下这两个字段始终是零值。
	// 见 item_grant_notice.go。
	itemGrantNoticeArmed      bool
	itemGrantNoticeContainers []byte
}

// partySessionState 持有组队投影与待处理邀请的会话状态。
// mu 守护 state 与 pending 邀请字段；state 的地址会传给 alignedcmd.Route，
// 迁移后仍为同一字段地址，锁序（party 锁内不嵌套 wireMu）保持不变。
// partySessionState is a per-client packet projection only. Membership,
// invitations, slots, leader replacement, and session generations are owned
// by party.RuntimePartyManager; this cache exists solely to shape the current
// EXE's existing party packets.
type partySessionState struct {
	mu    sync.Mutex
	state alignedcmd.PartyState
}

// questReplayState 持有任务接受/触发/完成三条回放的幂等去重状态。
// 每把锁各自守护对应 map 的懒初始化与读写，三把锁互不嵌套，保持原有临界区。
type questReplayState struct {
	acceptMu           sync.Mutex
	acceptAcknowledged map[currentAcceptQuestReplayKey]struct{}
	giveUpMu           sync.Mutex
	giveUpAnswered     map[currentGiveUpQuestReplayKey]struct{}
	triggerMu          sync.Mutex
	triggerNoop        map[currentSetQuestTriggerReplayKey]struct{}
	linkedTriggerValue map[currentSetQuestTriggerReplayKey]int64
	finishMu           sync.Mutex
	finishAnswered     map[currentFinishQuestReplayKey]struct{}
}

// lotteryState 持有魔盒抽奖待确认流程的会话状态。
// mu 守护待确认标记、栏位、双倍标记与发起时间，消费时清零的时机保持不变。
type lotteryState struct {
	mu            sync.Mutex
	pending       bool
	pendingSlot   int16
	pendingDouble bool
	pendingAt     time.Time
}

// petGrowthClockState 持有宠物饱食度时钟的会话状态。
// mu 守护时钟模式、锚点、代数与定时器名的全部读写（含 timequeue 回调），
// pending保留尚未成功结算的模式边界，重试不覆盖原时间点。
type petGrowthClockState struct {
	mu          sync.Mutex
	mode        currentPetGrowthClockMode
	characterID uint16
	anchor      time.Time
	generation  uint64
	timerName   string
	pending     []currentPetGrowthClockTransition
}

// townPostTransitionState 持有城镇入场后过渡流程的会话状态。
// mu 守护过渡代数、角色、属主频道、阶段与来源标记，arm/推进/归零时机保持不变。
type townPostTransitionState struct {
	mu           sync.Mutex
	generation   uint64
	characterID  uint16
	ownerChannel byte
	stage        currentTownPostTransitionStage
	source       string
}

// dungeonSessionState 持有单个地下城运行周期的会话状态。
// mu 守护 runtime、runToken 与 deathTower 的全部读写（含定时器回调与
// handler 路径），原有 dungeonMu 的临界区与锁序惯例保持不变。
type dungeonSessionState struct {
	mu         sync.Mutex
	runtime    *runtimeDungeonState
	runToken   uint64
	deathTower *deathTowerRuntime
}

func (s *Service) handleGameConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	connID := s.nextPacketConnID("game")
	channel, ok := s.channelForConn(conn)
	if !ok {
		s.logPacketEvent("game-connect-rejected",
			"conn_id", connID,
			"remote", conn.RemoteAddr().String(),
			"local", conn.LocalAddr().String(),
			"reason", "local_port_not_in_channel_catalog")
		s.logWarn("dnfbridge rejected game connection for unknown channel port",
			"remote", conn.RemoteAddr().String(),
			"local", conn.LocalAddr().String())
		return
	}
	// 2026-09-26 操作者要求：「特殊频道先留着，但不要登录就直接去」。客户端停在
	// [crack]/[deathtower] 这类特殊家族频道上时不会发登录包（现场 508 的
	// shared-account 回落、角色列表为空即此），所以会话的**落点频道**必须换成
	// catalog 里第一个普通城镇频道；op1 CHANNELINFO 随后把普通频道告诉客户端，
	// 由客户端完成换线。普通频道连接时 ResidentFor 原样返回自身（逐字节不变）。
	residentChannel := channel
	if catalog := s.currentCatalog(); catalog != nil {
		if candidate, ok := catalog.ResidentFor(channel); ok {
			residentChannel = candidate
		}
	}
	session := &gameSession{
		conn:            conn,
		connID:          connID,
		channel:         channel,
		residentChannel: residentChannel,
		// The numeric channel belongs to the transport/session.  Until the
		// current EXE commits a different scene context through its native
		// protocol, the selected local actor is owned by context zero.
		townActorOwnerChannel: currentSceneObjectContext,
		ctx:                   ctx,
	}
	if residentChannel.ID != channel.ID {
		s.logPacketEvent("game-resident-channel-replaced",
			"conn_id", connID,
			"connected_channel", channel.ID,
			"connected_type", channel.Type,
			"connected_group", channel.Group,
			"resident_channel", residentChannel.ID,
			"resident_type", residentChannel.Type,
			"resident_group", residentChannel.Group,
			"reason", "special_bootstrap_channel_is_not_a_login_landing")
	}
	sessionStartedAt := time.Now().UTC()
	// 账号解析必须在会话建立的最早时刻定下来，而且只允许两条**与来源 IP 无关**的
	// 依据：① 客户端进程号（本地 Windows 一键端，client_account_registry.go）；
	// ② 客户端登录包里的启动器令牌（稍后到达，account_login_token.go）。
	// 来源 IP 只写进 session.clientIP 供日志判读，绝不参与账号判定 ——
	// 2026-09-25 香港取证：玩家的启动器登录出口（203.198.28.63）与游戏连接来源
	// （加速器出口 175.43.105.6 等，会话间还会漂移）不是同一个地址。
	session.clientIP = remoteIPOf(conn)
	s.registerLiveGameSession(session)
	defer s.unregisterLiveGameSession(session)
	if pid, found := localTCPConnectionOwnerPID(conn); found {
		session.clientPID = pid
		if accountID, registeredAt, registered := s.registeredClientAccountForPID(pid); registered {
			session.accountID = accountID
			session.accountFromPID = true
			if registeredAt.After(sessionStartedAt) {
				// 登记晚于连接建立：可能是启动器异步登记，也可能是 PID 复用。
				// 不禁用（否则本地一键端的正常排序会被误伤），但要留下判读串；
				// 令牌到达后 confirmClientAccountPID 会把权威账号写回去。
				s.logWarn("dnfbridge client PID account binding was registered after the session started",
					"conn_id", session.connID,
					"client_pid", pid,
					"account_id", accountID,
					"registered_at", registeredAt.Format(time.RFC3339Nano),
					"session_started_at", sessionStartedAt.Format(time.RFC3339Nano),
					"impact", "binding is used only until a launcher login token resolves the account")
			}
		}
	}
	s.logInfo("dnfbridge game connection accepted",
		"conn_id", session.connID,
		"remote", conn.RemoteAddr().String(),
		"local", conn.LocalAddr().String(),
		"channel_id", channel.ID,
		"channel_type", channel.Type,
		"game_port", channel.Port,
		"client_pid", session.clientPID,
		"client_ip", session.clientIP,
		"account_from_pid", session.accountFromPID,
		"session_account_bound", session.accountID != "")
	s.logPacketEvent("game-connect",
		"conn_id", session.connID,
		"remote", conn.RemoteAddr().String(),
		"local", conn.LocalAddr().String(),
		"channel_id", channel.ID,
		"channel_type", channel.Type,
		"game_port", channel.Port)
	if err := s.startGameSessionEvents(ctx, session); err != nil {
		s.logPacketEvent("game-session-events-start-failed", "conn_id", session.connID, "error", err)
		s.logWarn("dnfbridge start game session events failed", "remote", conn.RemoteAddr().String(), "error", err)
		return
	}
	dprotoOpened := false
	defer func() {
		s.shutdownGameSessionEvents(session, dprotoOpened)
	}()
	if err := s.callGameSession(ctx, session, "game-dproto-open", func() error {
		return s.openGameDprotoSession(ctx, session)
	}); err != nil {
		s.logPacketEvent("game-dproto-open-failed", "conn_id", session.connID, "error", err)
		s.logWarn("dnfbridge open native dproto session failed", "remote", conn.RemoteAddr().String(), "error", err)
		return
	}
	dprotoOpened = true
	defer s.logPacketEvent("game-close",
		"conn_id", session.connID,
		"remote", conn.RemoteAddr().String(),
		"local", conn.LocalAddr().String(),
		"channel_id", channel.ID,
		"game_port", channel.Port)
	if err := s.callGameSession(ctx, session, "game-connection-bootstrap", func() error {
		return s.sendGameConnectionBootstrap(session, time.Now())
	}); err != nil {
		s.logPacketEvent("game-connection-bootstrap-failed",
			"conn_id", session.connID,
			"remote", conn.RemoteAddr().String(),
			"local", conn.LocalAddr().String(),
			"channel_id", channel.ID,
			"error", err)
		s.logWarn("dnfbridge send game connection bootstrap failed", "remote", conn.RemoteAddr().String(), "error", err)
		return
	}

	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	var streamWaitTrace gameStreamWaitTrace
	for {
		n, err := conn.Read(chunk)
		if err != nil {
			// 「静默窗」读截止到期：**这不是连接错误**（真正的断线/EOF/ECONNRESET 走下面的原路）。
			// 只有"本会话确实有一条挂起的 tail==1 op19" + 错误是超时，才判成我们自己的截止。
			// 到期 = 挂起那条之后 quiet_ms 内没有第二条 op19 ⇒ 玩家亲手发起 ⇒ 按厂商口径执行。
			if isOp19QuietReadDeadline(session, err) {
				s.resolveOp19QuietPending(session, true)
				clearOp19QuietReadDeadline(conn)
				s.logPacketEvent("game-read-quiet-window-deadline",
					"conn_id", session.connID,
					"decision", "resolve-as-player-request",
					"reason", "op19-quiet-window-elapsed",
					"quiet_ms", op19PlayerFlagQuietWindow().Milliseconds())
				continue
			}
			quiet := isQuietGameReadError(err)
			s.logPacketEvent("game-read-close",
				"conn_id", session.connID,
				"remote", conn.RemoteAddr().String(),
				"local", conn.LocalAddr().String(),
				"channel_id", channel.ID,
				"pending_bytes", len(buffer),
				"quiet", quiet,
				"error", err)
			if !quiet && ctx.Err() == nil {
				s.logWarn("dnfbridge read game packet failed", "remote", conn.RemoteAddr().String(), "error", err)
			}
			return
		}
		if n > 0 {
			s.logGamePacket(session, "RECV", "game-raw", chunk[:n])
		}
		buffer = append(buffer, chunk[:n]...)
		packets, remaining, skipped, err := dnfproto.SplitLatestGameStream(buffer, s.options.maxPacketBytes)
		if err != nil {
			s.logPacketEvent("game-split-error",
				"conn_id", session.connID,
				"remote", conn.RemoteAddr().String(),
				"local", conn.LocalAddr().String(),
				"channel_id", channel.ID,
				"buffer_bytes", len(buffer),
				"error", err)
			s.logWarn("dnfbridge split game stream failed", "remote", conn.RemoteAddr().String(), "error", err)
			return
		}
		if skipped > 0 {
			s.logWarn("dnfbridge game frame resynced", "remote", conn.RemoteAddr().String(), "skipped", skipped)
		}
		if s.options.gameStreamWaitTrace {
			if metadata, report := streamWaitTrace.observe(time.Now(), remaining, len(remaining) < len(buffer), s.options.maxPacketBytes); report {
				fields := []any{"conn_id", session.connID, "channel_id", channel.ID, "pending_bytes", metadata.pendingBytes, "header_shape", metadata.headerShape}
				if metadata.hasChannelHeader {
					fields = append(fields, "head_class", metadata.classification, "head_opcode", metadata.opcode, "head_declared_length", metadata.declaredLength)
				}
				s.logPacketEvent("game-stream-wait", fields...)
			}
		}
		buffer = remaining
		for _, packet := range packets {
			packet := packet
			// 「静默窗」挂起期间来了新包 ⇒ **先结挂起（按门禁路径）再处理本包**：
			// 顺序不变、一个包都不丢（同一批 SplitLatestGameStream 拆出的多包逐包适用）。
			if session.hasOp19QuietPending() {
				s.resolveOp19QuietPending(session, false)
				clearOp19QuietReadDeadline(conn)
			}
			if err := s.callGameSession(ctx, session, "game-stream-packet", func() error {
				return s.handleGameStreamPacket(session, packet)
			}); err != nil {
				s.logWarn("dnfbridge handle game packet failed", "remote", conn.RemoteAddr().String(), "error", err)
				return
			}
			// 本包刚挂起了一条 ⇒ 给它挂上静默窗读截止（会话本来没有读截止，结算后清掉即可）。
			if session.hasOp19QuietPending() {
				armOp19QuietReadDeadline(conn, op19PlayerFlagQuietWindow())
			}
		}
	}
}

func (s *Service) channelForConn(conn net.Conn) (channelcatalog.Channel, bool) {
	port := 0
	if endpoint, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		port = endpoint.Port
	}
	return s.channelForPort(port)
}

func (s *Service) channelForPort(port int) (channelcatalog.Channel, bool) {
	catalog := s.currentCatalog()
	if catalog == nil {
		return channelcatalog.Channel{}, false
	}
	return catalog.ForPort(port)
}

func (s *Service) gameListenPorts(catalog *channelcatalog.Catalog) []int {
	if catalog == nil {
		return nil
	}
	return catalog.GamePorts()
}

func (s *Service) handleGameStreamPacket(session *gameSession, packet dnfproto.LatestGameStreamPacket) error {
	defer s.afterCurrentLevelUpRewardCommand(session)
	// 登录路径的 actor 世代重建钩子：布防发生在 type1345 那一包的**派发过程中**，
	// 所以"派发前已经布防"恰好等价于"这是场景就绪 ack 之后的第一条客户端报文"。
	// 见 login_actor_generation_rebuild.go。
	rebuildArmedBeforeDispatch := loginActorGenerationRebuildArmed(session)
	var err error
	switch packet.Kind {
	case dnfproto.LatestGameStreamTransport:
		err = s.handleGameFrame(session, packet.Data)
	case dnfproto.LatestGameStreamUpper:
		err = s.handleGameUpper(session, packet.Data)
	case dnfproto.LatestGameStreamLegacy:
		err = s.handleLegacyGamePacket(session, packet.Data)
	case dnfproto.LatestGameStreamDproto:
		err = s.handleGameDprotoPacket(session, packet.Data)
	default:
		return dnfproto.ErrPacketLength
	}
	if err != nil {
		return err
	}
	if rebuildArmedBeforeDispatch {
		s.afterLoginSceneReadyFirstClientPacket(session, loginActorGenerationRebuildTrigger(packet))
	}
	return nil
}

// loginActorGenerationRebuildTrigger 只把"这一次是谁触发的"写进事件：
// legacy 给 type、upper 给 msg_id/classification、其余给流名。解析失败不改变任何行为。
func loginActorGenerationRebuildTrigger(packet dnfproto.LatestGameStreamPacket) string {
	switch packet.Kind {
	case dnfproto.LatestGameStreamLegacy:
		if parsed, err := dnfproto.ParseLegacyGamePacket(packet.Data); err == nil {
			return "legacy_type=" + strconv.Itoa(int(parsed.Header.Type))
		}
		return "legacy_unparsed"
	case dnfproto.LatestGameStreamUpper:
		if parsed, err := dnfproto.ParseChannelPacketUnchecked(packet.Data); err == nil {
			return "upper_msg_id=" + strconv.Itoa(int(parsed.Header.MsgID)) +
				"_classification=" + strconv.Itoa(int(parsed.Header.Classification))
		}
		return "upper_unparsed"
	case dnfproto.LatestGameStreamTransport:
		return "transport"
	case dnfproto.LatestGameStreamDproto:
		return "dproto"
	default:
		return "unknown"
	}
}

func isQuietGameReadError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET)
}
