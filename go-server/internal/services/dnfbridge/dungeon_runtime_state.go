package dnfbridge

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var (
	errDungeonWorldMapUnavailable        = errors.New("dnf dungeon worldmap runtime is unavailable")
	errDungeonMonsterCatalogUnavailable  = errors.New("dnf dungeon monster catalog is unavailable")
	errDungeonCharacterRequired          = errors.New("dnf dungeon selected character is required")
	errDungeonCharacterNotFound          = errors.New("dnf dungeon selected character was not found")
	errDungeonAccountMismatch            = errors.New("dnf dungeon selected character account mismatch")
	errDungeonQuestRepositoryUnavailable = errors.New("dnf dungeon quest repository is unavailable")
	errDungeonDifficultyInvalid          = errors.New("dnf dungeon difficulty is invalid")
	errDungeonLevelTooLow                = dnfdungeon.ErrEntryLevelTooLow
	errDungeonFatigueUnknown             = dnfdungeon.ErrEntryFatigueUnknown
	errDungeonFatigueExhausted           = dnfdungeon.ErrEntryFatigueExhausted
	errDungeonPartyLimit                 = dnfdungeon.ErrEntryPartyLimit
	errDungeonAlreadyActive              = errors.New("dnf dungeon session is already active")
	errDungeonChoiceRequired             = errors.New("dnf dungeon explicit PVF choice is required")
	errDungeonMonsterOwnerMismatch       = errors.New("dnf dungeon monster death related actor object key mismatch")
)

type runtimeDungeonState struct {
	durabilityLastSequence       uint64
	durabilitySequenceKnown      bool
	guildPractice                *currentGuildPracticeRun
	guildDungeonClear            *dnfdungeon.GuildDungeonClear
	settlementGuildDungeonSent   bool
	ridableReportLastSequence    uint64
	ridableReportChannelID       int
	ridableReportChannelType     byte
	huntMonsterReceipts          map[currentHuntMonsterReceiptKey]*currentRoomClearQuestReceipt
	roomClearQuestReceipts       map[runtimeDungeonRoomKey]*currentRoomClearQuestReceipt
	questDropReceipts            map[currentDungeonQuestDropEventKey]currentDungeonQuestDropReceipt
	passiveQuestDropReceipts     map[currentDungeonPassiveQuestDropEventKey]currentDungeonPassiveQuestDropReceipt
	passiveQuestDropSourceUsage  map[runtimeDungeonRoomKey]map[int64]int
	storyProcDeaths              map[uint32]dungeonStoryProcDeathReceipt
	missionInformerRetirements   map[uint32]dungeonMissionInformerRetirement
	entryCharacterLevel          int
	entryPartyCount              int
	petGrowthEntryPending        bool // follower runtime installed before the group commits
	settlementLuckyStar          dnfdungeon.LuckyStarResult
	settlementLuckyStarStateSent bool
	raidDeadlineReturn           *currentRaidDeadlineReturn
	lukeSanctumMaze              *currentLukeSanctumMazePreparation
	dimensionHiddenAdmission     *dnfdungeon.DimensionHiddenAdmission
	dimensionHiddenHighReward    bool
	semesterRun                  *currentSemesterRun
	epicEntry                    *currentEpicEntry
	raidEntry                    *currentRaidDungeonEntry
	fiendWarRetreatKeepsParty    bool
	antonMapReturnOwner          *currentAntonMapReturnOwner
	lukeClosedEnergyReturn       *currentLukeClosedEnergyReturnOwner
	antonHatcheryTransfer        *currentAntonHatcheryTransfer
	deathStateSent               bool
	riftLoot                     *currentRiftLootDefinition
	fatigueRooms                 map[worldmap.RoomCoordinate]bool
	fatigueStartDebited          bool
	fatigueSpent                 int64
	Request                      dungeoncmd.SelectDungeonRequest
	Dungeon                      worldmap.Dungeon
	MazeIndex                    int
	storyExperience              currentDungeonStoryExperienceProfile
	Character                    dnfrepo.CharacterRecord
	// AccountID 是**建这个 runtime 的那条会话**的账号（accountIDForSession，在请求
	// 边界冻结，与 currentEquipmentPlacementValidator.selectedAccountID 同一口径）。
	// 副本内所有**账号级**读取（成长契约/黑钻等 entitlement、掉落按账号取的加成）
	// 必须用它，不能回头读 Character.AccountID —— 角色行是"存储真值"，但账号级
	// 权益的归属口径是会话账号；两者在"角色建在共享兜底账号 dnf:1 上、会话已由
	// 令牌定案成 dnf:100xx"的现场形态下并不相同。
	AccountID         string
	Session           *worldmap.DungeonSession
	Room              *runtimeDungeonRoom
	randomizedObjects *currentDungeonRandomizedOwner
	DropOwner         *runtimeDungeonDropOwner
	NextObjectKey     uint32
	// MapOwnedPassiveObjects 是"本 run 每个房间的 map-owned 特殊被动对象注册表"
	// （房图 `[special passive object]` 下标 → 对象 code + 破坏台账 + 已投影的掉落行）。
	// 厂商对应物是 `DungeonInstance` 的 `Maze.SpecialPassiveObjects` +
	// `_mapOwnedActorDeaths`（`DungeonInstance.cs:378-404`）。两个开关都是 legacy 时
	// 这张表**恒为 nil**（一行都不建），见 dungeon_passive_object_drop.go。
	MapOwnedPassiveObjects  map[runtimeDungeonRoomKey]*currentDungeonMapOwnedPassiveObjectRegistration
	passiveItemDropsPlanned map[runtimeDungeonRoomKey]bool
	partyInstance           *dungeonPartyInstance
	BossCoordinate          worldmap.RoomCoordinate
	BossSet                 bool
	// HellRoom/HellRoomSet 是这条 runtime 冻结的深渊房间（见 dungeon_hell_room.go）。
	// HellRoomSet=false ⇒ op28 逐字节回到改动前的缺席投影。
	HellRoom    dungeonHellRoom
	HellRoomSet bool
	// StartMapHellPartyMode/StartMapHellFogFlag 是 op29 START_MAP offset 7 与其后的
	// fog 字节，在 runtime 创建那一刻按 DNFBRIDGE_HELL_ROOM 归一化后冻结
	// （厂商 DungeonMapHandler.cs:512-513 两行）。legacy 档下两字节恒为 0 = 改动前。
	StartMapHellPartyMode byte
	StartMapHellFogFlag   byte
	// HellPartyWaves / HellPartyDifficulty / HellPartyStarted 是深渊波次的**冻结态**
	// （厂商 `DungeonRun.HellPartyRoomInfo.Waves`，在准入那一刻按 `Dungeon.cs:1151-1194`
	// 装配一次，之后所有投影复用）。只有两个波次开关里至少一个是修复档时才非空；
	// 两个都 legacy ⇒ 恒为空 ⇒ 逐字节、逐随机序列等于改动前。见 dungeon_hell_party_wave.go。
	HellPartyWaves          []dungeonHellPartyWave
	HellPartyDifficulty     byte
	HellPartyStarted        bool
	hellEntryItemsCommitted bool
	// hellPillarPeerDestroyKey 记录"柱子破坏已经投影给队友"的那个房间
	// （`mapID:坐标`）。柱子本体的破坏客户端从不上报，服务端只能在
	// **本房间第一只波次怪退役**时投一次，所以需要这个每房间一次的哨兵。
	// 见 `dungeon_hell_party_wave_peer_notify.go`。
	hellPillarPeerDestroyKey             string
	LayeredMapIndex                      int
	LayeredMapActive                     bool
	LayerChains                          map[worldmap.RoomCoordinate]*runtimeDungeonLayerChain
	StoryStages                          []worldmap.DungeonStoryStage
	StoryStageIndex                      int
	Seed                                 uint32
	partyMemberIndex                     byte
	partyMemberIndexed                   bool
	Rooms                                map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit
	startedAt                            time.Time
	bossDieCheckPending                  bool
	bossDieCheckPendingRequest           dungeoncmd.BossDieCheckRequest
	bossDieCheckAccepted                 bool
	bossDieCheckRelatedActorObjectKey    uint16
	bossDieCheckTargetObjectKey          uint16
	bossDieCheckResponseSent             bool
	ordinaryFinalRoomClearAccepted       bool
	tutorialFinalRoomClearPending        bool
	tutorialFinalRoomClearAccepted       bool
	settlementPhase                      currentDungeonSettlementPhase
	settlementEntrySent                  bool
	settlementEntryAt                    time.Time
	settlementPlayResultReceived         bool
	settlementPlayResultBody             []byte
	settlementPlayResultDynamicRows      uint8
	settlementPlayResultOptionalField    bool
	settlementStatisticReceived          bool
	settlementStatisticBody              []byte
	settlementResultPlan                 *currentDungeonSettlementPacketPlan
	settlementResultNoticeSent           bool
	settlementCharacterStateSent         bool
	settlementPremiumServiceSent         bool
	settlementClearRewardSent            bool
	settlementDespairResultSent          bool
	settlementDespairItemsSent           bool
	settlementDespairRewards             *dnfdungeon.DespairRewards
	settlementGriefResultSent            bool
	settlementGriefItemsSent             bool
	settlementGriefRewards               *dnfdungeon.GriefRewards
	settlementCardScrollStateSent        bool
	settlementCardRightStateSent         bool
	settlementCardLayoutSent             bool
	settlementCardSelectionKnown         bool
	settlementCardSelected               byte
	settlementCardSelectionSent          bool
	settlementCardRewardCommitted        bool
	settlementCardSideSelectionKnown     [dungeonCardSideCount]bool
	settlementCardSideMember             [dungeonCardSideCount]byte
	settlementCardSideSelectionSent      [dungeonCardSideCount]bool
	settlementCardSideRewardCommitted    [dungeonCardSideCount]bool
	settlementCardAutoFlipGeneration     uint64
	settlementCardAutoFlipTimerName      string
	settlementCardExitAckSent            bool
	settlementTownReturnRequestMsgID     uint16 // 本局明确回城请求，0表示无待办。
	settlementCardRewardState            *dungeonCardState
	settlementPaidCardGrant              *dungeonCardAssetGrantResult
	settlementMonsterExperienceTotal     uint32
	settlementMonsterGrowthContractBonus uint32
	settlementChampionExperience         uint32
	settlementSuperChampionExperience    uint32
	settlementBossExperience             uint32
	tutorialCompletionPersisted          bool
	dungeonCycleClearCountCommitted      bool
	dungeonCycleClearNonceHi             int64
	dungeonCycleClearNonceLo             int64
	clearMapCompletionPhaseAPersisted    bool
	hellQuestCompletionKey               string
	hellQuestCompletionAt                time.Time
	hellQuestCompletionPersisted         bool
	hellQuestCompletionSnapshotSent      bool
	clearMapCompletionKey                string
	clearMapCompletionAt                 time.Time
	clearMapCompletionQuestIDs           []int64
	clearMapCompletionPendingQuestIDs    []int64
	clearMapCompletionActiveSnapshotSent bool
	clearMapCompletionNotificationClosed bool
	storyQuestAfterFreeCardSnapshotSent  bool
	tutorialCompletedReentry             bool
	tutorialCompletedReentryExitSent     bool
	tutorialFinalFlagAckSent             bool
	tutorialInitialUserStateSent         bool
	townReturnPending                    bool
	townReturnOp24Sent                   bool
	townReturnRequestMsgID               uint16
	townReturnAckSent                    bool
	townReturnSource                     string
	townReturnOrigin                     currentTownPositionSnapshot
	townReturnTransition                 currentDungeonTownTransition
	crystalContractTimerActive           bool
	crystalContractTimerGeneration       uint64
	crystalContractTimerName             string
	crystalContractTimerDueAt            time.Time
	lifecycleToken                       uint64
	deathReturnWaiting                   bool
	deathReturnGeneration                uint64
	deathReturnTimerName                 string
	deathReturnDueAt                     time.Time
	deathReturnTransition                currentDungeonTownTransition
	actorPositionX                       uint16
	actorPositionY                       uint16
	actorDirection                       byte
	actorPositionValid                   bool
	fiendWarSceneDiagnostic              fiendWarSceneDiagnosticState
	settlementDungeonPermissionSent      bool
	settlementLuckyStarResolved          bool
	suspiciousVillageElevator            currentDungeonElevatorState
	// Tournament 是本次 run 的锦标赛赛制运行时（厂商
	// `DungeonInstance.Mechanisms.Tournament`）。只有
	// `DNFBRIDGE_DUNGEON_TOURNAMENT=vendor` 且该 DGN 声明了 `[tournament dungeon]`
	// 时才非 nil；默认档恒为 nil，所有读它的分支都不成立。
	Tournament *tournamentDungeonRuntime
	// IgnoreDefaultDungeonClear 对应厂商 `DungeonRun.IgnoreDefaultDungeonClear`
	// （TournamentDungeonApplicationService.cs:70-72 显式置 true）。
	//
	// 已接线（本批）：`shouldDeferTournamentDefaultClear`
	// （dungeon_tournament_flow.go，对应厂商 DungeonCombatHandler.cs:260-267）
	// 在 `dungeon_boss_die_clear_decision.go` 的 `bossRoomClearReady:` 之后读它。
	// 默认档该字段恒为 false ⇒ 该门恒不生效，控制流逐字节等于改动前。
	IgnoreDefaultDungeonClear bool
	// tournamentReward 是本会话在赛制终局后冻结的参赛者奖励态
	// （厂商 `DungeonRun.Settlement.Tournament`）。默认档恒为 nil。
	// 只有 `beginCurrentTournamentRewardLocked` 会写它，且写之前要求
	// `Tournament.Terminal != Active` —— 冠军与被淘汰都会冻结
	// （厂商 `TournamentDungeonApplicationService.cs:153-197 TryCreateParticipantRewards`
	// 只要求 `IsTerminated`，并用 `IsChampion` 决定下排卡数）。
	tournamentReward *tournamentRewardState
	// tournamentExperience 是"通关经验真的落到角色账上"这件事的**一次性凭据**
	// （厂商 `TournamentParticipantRewardState._experienceState` /
	// `TournamentDungeonCoordinator.cs:607-662 TryDeliverExperienceAsync`）。
	//
	// 与 `tournamentReward` 分开保存的原因：奖励态在"客户端重发选奖态请求 /
	// 重连补发 389"这些路径上可能被重建或置空，而经验提交**必须**在一整局里
	// 只发生一次 —— 两张凭据的生命周期不同，不能共用一个字段。
	//
	// 默认档恒为零值：`beginCurrentTournamentRewardLocked` 的第一行
	// （`runtime.Tournament == nil`）就返回，本字段永远不会被读到。
	tournamentExperience tournamentExperienceReceipt
	// tournamentRewardAutoSelect* 是选奖自动选择的会话内一次性计时器
	// （厂商 `TournamentDungeonCoordinator.cs:400-441` 的
	// `DungeonRunTimerKeys.TournamentRewardAutoSelect`，3000ms 绝对 deadline）。
	// 默认档恒为零值；只有 `armCurrentTournamentRewardAutoSelectTimerLocked` 会写。
	tournamentRewardAutoSelectGeneration uint64
	tournamentRewardAutoSelectTimerName  string
	tournamentRewardAutoSelectDueAt      time.Time

	// 大富翁Buff仅绑定本次地下城，换房保留。
	attendanceMarbleBuffs                  byte
	attendanceMarbleBuffsLoaded            bool
	settlementMonsterAttendanceMarbleBonus uint32
}

func dungeonRuntimeOwnsCharacter(runtime *runtimeDungeonState, characterID uint16) bool {
	return runtime != nil &&
		dnfdungeon.RuntimeOwnsCharacter(runtime.Character.CharacterID, characterID)
}

func (s *Service) preloadDungeonWorldMap(ctx context.Context) error {
	if s == nil {
		return errDungeonWorldMapUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return fmt.Errorf("preload dungeon worldmap pvf: %w", err)
	}
	mapOptions := worldmap.Options{}
	if s.options.currentClientVersion == "official-24.9" {
		for route := byte(0); route < 2; route++ {
			available, routeErr := raid.ValidateSingleRaidDependenciesForRoute(archive, route)
			if routeErr != nil {
				s.logPacketEvent("dnf-single-raid-map-dependencies-unavailable", "route", route, "effect", "single_start_rejected_before_material_commit", "error", routeErr)
				continue
			}
			mapOptions.SupplementalDungeonListPath = available.SupplementalDungeonListPath
			mapOptions.SupplementalDungeonReferenceRoot = available.SupplementalDungeonReferenceRoot
			mapOptions.SupplementalDungeonIDs = append(mapOptions.SupplementalDungeonIDs, available.SupplementalDungeonIDs...)
		}
	}
	var table *worldmap.Table
	if s.options.lowMemory {
		table, err = worldmap.LoadSourceCached(ctx, archive, mapOptions, worldmap.DefaultMapCacheOptions())
	} else {
		table, err = worldmap.LoadSource(ctx, archive, mapOptions)
	}
	if err != nil {
		return fmt.Errorf("preload dungeon worldmap table: %w", err)
	}
	resolver, err := worldmap.NewResolverWithOptions(table, s.dungeonResolverOptions())
	if err != nil {
		return fmt.Errorf("preload dungeon worldmap resolver: %w", err)
	}
	monsterTable, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		return fmt.Errorf("preload dungeon monster table: %w", err)
	}
	aiCharacterTable, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		return fmt.Errorf("preload dungeon AI character table: %w", err)
	}
	tutorialScripts, err := newPVFDungeonTutorialScriptCatalog(archive)
	if err != nil {
		return fmt.Errorf("preload dungeon tutorial script table: %w", err)
	}
	tutorialScripts.indexBasicActionDestroyTargets(archive, table)

	// `etc/hellparty.etc`（深渊波次表）：与上面两张目录同一处预载、只读一次。
	// **读不到不阻断预载**：厂商 `HellPartyData.cs:141-149 ReadHellPartyScript` 读不到时
	// 同样只是"没有波次"（返回空内容），这里照抄该降级，但必须留一条具名事件，
	// 否则"深渊不出怪"会变成静默失败。见 dungeon_hell_party_catalog.go。
	hellPartyTable, hellPartyErr := newPVFDungeonHellPartyCatalog(archive)
	if hellPartyErr != nil {
		hellPartyTable = nil
		s.logPacketEvent("dnf-dungeon-hell-party-table-unavailable",
			"path", currentDungeonHellPartyPVFPath,
			"effect", "no_hell_party_wave_rows_and_no_noti_679_until_the_table_is_readable",
			"vendor_behavior", "HellPartyData_ReadHellPartyScript_returns_empty_when_the_file_cannot_be_read",
			"error", hellPartyErr)
	}

	// 深渊奖励的**厂商口径装备池**（`equipment/equipment.lst` + `.equ`）在这里预载：
	// 全树扫描是一次性的 O(14 万文件) 工作，放在启动期（此刻还没有玩家进图）就不必由
	// 第一次深渊击杀在包路径上承担；厂商 `DropService.cs:20` 也是启动期
	// `HellMonsterDropConfig.WarmUp()`。回退档（`DNFBRIDGE_HELL_PARTY_REWARD_POOL=itemdictionary`）
	// 不建池、零成本。扫描本身走 `ScanTextPrefix`（不填归档缓存），内存有界。
	// 建池失败**不阻断预载**：厂商 `HellMonsterDropConfig.cs:94-97` 同样是记一条
	// INIT FAILED 后继续，奖励路径届时按"没有池子"降级。
	s.logPacketEvent(hellPartyRewardPoolReadyEvent,
		hellPartyRewardPoolWarmUpFields(monsterTable)...)
	s.logPacketEvent("dnf-dungeon-clear-card-equipment-pool",
		clearCardEquipmentPoolWarmUpFields(monsterTable)...)

	if s.options.mapDefinitionCache && !s.options.lowMemory {
		if err := table.EnableMapCache(archive, worldmap.DefaultMapCacheOptions()); err != nil {
			return fmt.Errorf("准备地图按需缓存失败: %w", err)
		}
	}
	s.worldMapMu.Lock()
	s.worldMapTable = table
	s.worldMapResolver = resolver
	s.dungeonMonsterTable = monsterTable
	s.dungeonAICharacterTable = aiCharacterTable
	s.dungeonTutorialScripts = tutorialScripts
	s.dungeonHellPartyTable = hellPartyTable
	s.worldMapMu.Unlock()
	snapshot := table.Snapshot()
	tutorialSnapshot := tutorialScripts.Snapshot()
	s.logPacketEvent("dnf-dungeon-worldmap-loaded",
		"maps", snapshot.Maps,
		"areas", snapshot.Areas,
		"dungeons", snapshot.Dungeons,
		"mazes", snapshot.Mazes,
		"dungeon_refs", snapshot.DungeonRefs,
		"monsters", monsterTable.Count(),
		"AI_characters", aiCharacterTable.Count(),
		"hell_party_groups", hellPartyTable.Count(),
		"tutorial_cinematic_entries", tutorialSnapshot.CinematicEntries,
		"tutorial_cinematics_with_destroy_targets", tutorialSnapshot.CinematicsWithTargets,
		"tutorial_maps_with_destroy_targets", tutorialSnapshot.MapsWithTargets,
		"tutorial_monster_destroy_targets", tutorialSnapshot.MonsterTargets,
		"tutorial_cinematic_read_failures", tutorialSnapshot.ReadFailures,
		"tutorial_cinematic_parse_failures", tutorialSnapshot.ParseFailures,
		"tutorial_basic_action_entries", tutorialSnapshot.BasicActionEntries,
		"tutorial_basic_actions_with_destroy_targets", tutorialSnapshot.BasicActionsWithTargets,
		"tutorial_basic_action_maps", tutorialSnapshot.BasicActionMaps,
		"tutorial_basic_action_monster_targets", tutorialSnapshot.BasicActionTargets,
		"tutorial_basic_action_read_failures", tutorialSnapshot.BasicActionReadFailures,
		"tutorial_basic_action_parse_failures", tutorialSnapshot.BasicActionParseFailures)
	return nil
}

func (s *Service) dungeonMonsterCatalog() (*pvfDungeonMonsterCatalog, error) {
	if s == nil {
		return nil, errDungeonWorldMapUnavailable
	}
	s.worldMapMu.RLock()
	defer s.worldMapMu.RUnlock()
	if s.dungeonMonsterTable == nil {
		return nil, errDungeonMonsterCatalogUnavailable
	}
	return s.dungeonMonsterTable, nil
}

func (s *Service) dungeonAICharacterCatalog() (*pvfDungeonAICharacterCatalog, error) {
	if s == nil {
		return nil, errDungeonWorldMapUnavailable
	}
	s.worldMapMu.RLock()
	defer s.worldMapMu.RUnlock()
	if s.dungeonAICharacterTable == nil {
		return nil, errDungeonAICharacterUnavailable
	}
	return s.dungeonAICharacterTable, nil
}

// dungeonHellPartyCatalog 读 `etc/hellparty.etc` 的只读索引。**不返回错误**：
// nil = 表不可用（预载已经发过 `dnf-dungeon-hell-party-table-unavailable`），
// 调用方按"没有波次"降级（厂商口径），绝不因此拒绝进图。
// currentHellPartyAICatalog 是**容忍缺失**的 AICharacter 目录读法：波次表里
// `EntityType == 1` 的 APC 才需要它，读不到就按厂商口径回落到副本基准等级
// （`Dungeon.cs:1210-1214`），所以这里不返回错误。
func (s *Service) currentHellPartyAICatalog() *pvfDungeonAICharacterCatalog {
	if s == nil {
		return nil
	}
	s.worldMapMu.RLock()
	defer s.worldMapMu.RUnlock()
	return s.dungeonAICharacterTable
}

// hellPartyWaveDetail 把冻结尾波次压成一行可对账文本：
// `order:group:code/type/level,...`（多个 order 用 `;` 分隔）。
func hellPartyWaveDetail(waves []dungeonHellPartyWave) string {
	if len(waves) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(waves))
	for _, wave := range waves {
		monsters := make([]string, 0, len(wave.Monsters))
		for _, monster := range wave.Monsters {
			monsters = append(monsters, fmt.Sprintf("%d/%d/%d", monster.Code, monster.Type, monster.Level))
		}
		parts = append(parts, fmt.Sprintf("%d:%d:%s", wave.Order, wave.GroupID, strings.Join(monsters, ",")))
	}
	return strings.Join(parts, ";")
}

func (s *Service) dungeonHellPartyCatalog() *pvfDungeonHellPartyCatalog {
	if s == nil {
		return nil
	}
	s.worldMapMu.RLock()
	defer s.worldMapMu.RUnlock()
	return s.dungeonHellPartyTable
}

func (s *Service) dungeonTutorialScriptCatalog() (*pvfDungeonTutorialScriptCatalog, bool) {
	if s == nil {
		return nil, false
	}
	s.worldMapMu.RLock()
	defer s.worldMapMu.RUnlock()
	if s.dungeonTutorialScripts == nil {
		return nil, false
	}
	return s.dungeonTutorialScripts, true
}

func (s *Service) dungeonWorldMap() (*worldmap.Table, *worldmap.Resolver, error) {
	if s == nil {
		return nil, nil, errDungeonWorldMapUnavailable
	}
	s.worldMapMu.RLock()
	defer s.worldMapMu.RUnlock()
	if s.worldMapTable == nil || s.worldMapResolver == nil {
		return nil, nil, errDungeonWorldMapUnavailable
	}
	return s.worldMapTable, s.worldMapResolver, nil
}

func (s *Service) prepareDungeonRuntime(
	ctx context.Context,
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
) (*runtimeDungeonState, worldmap.DungeonRoomScene, error) {
	return s.prepareDungeonRuntimePlanned(ctx, session, request, nil)
}

func (s *Service) prepareDungeonRuntimePlanned(
	ctx context.Context,
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	partyPlan *runtimePartyDungeonEntryPlan,
) (*runtimeDungeonState, worldmap.DungeonRoomScene, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return nil, worldmap.DungeonRoomScene{}, errDungeonCharacterRequired
	}
	if request.DungeonID == 0 {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("%w: dungeon=0", worldmap.ErrDungeonNotIndexed)
	}
	if request.Difficulty > 4 {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("%w: got=%d max=4", errDungeonDifficultyInvalid, request.Difficulty)
	}
	table, resolver, err := s.dungeonWorldMap()
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	dungeon, ok := table.FindDungeon(int64(request.DungeonID))
	if !ok {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("%w: dungeon=%d", worldmap.ErrDungeonNotIndexed, request.DungeonID)
	}
	if s.currentRiftEntryEnabled(dungeon.ID) && len(dungeon.Metadata.DesignatedDifficulties) != 0 {
		allowed := false
		for _, difficulty := range dungeon.Metadata.DesignatedDifficulties {
			allowed = allowed || difficulty == int64(request.Difficulty)
		}
		if !allowed {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("%w: rift=%d got=%d designated=%v", errDungeonDifficultyInvalid, dungeon.ID, request.Difficulty, dungeon.Metadata.DesignatedDifficulties)
		}
	}
	if err := s.validateCurrentRiftUnlock(ctx, session, dungeon); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	if err := s.validateCurrentLukeNormalThroneEntry(ctx, session, dungeon); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	character, err := s.dungeonCharacter(ctx, session.selectedCharacterID, session)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	if floor, err := currentDespairFloor(dungeon); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	} else if floor != 0 {
		next, err := dnfdungeon.DespairNextFloor(character.Stats)
		if err != nil || floor != next {
			return nil, worldmap.DungeonRoomScene{}, dnfdungeon.ErrSettlementStateInvalid
		}
	}
	if floor, err := currentGriefFloor(dungeon); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	} else if floor != 0 {
		next, err := dnfdungeon.GriefNextFloor(character.Stats)
		if err != nil || floor != next {
			return nil, worldmap.DungeonRoomScene{}, dnfdungeon.ErrSettlementStateInvalid
		}
	}
	character, err = s.updateCurrentCharacterFatigue(ctx, session, false)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	// FiendWar movement is session-owned and deliberately not persisted. The
	// fatigue refresh reloads the stored character, so restore its confirmed
	// battlefield position before follower return-origin preflight compares it.
	// This only changes the local runtime copy; fatigue and admission stay live.
	applyCurrentFiendWarCharacterPosition(&character, session, session.selectedCharacterID)
	if err := s.validateDungeonEntryWithTournament(ctx, character, dungeon, session); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	// 锦标赛 `[required item]` 的**入场消耗**（`Flag == 1` ⇒ ConsumeOnEntry）：
	// 与上面的准入校验在同一个函数、同一个准入边界里，校验通过即扣。
	// 扣减失败（持仓不足 / 事务失败）⇒ 明确拒绝入场（错误冒到
	// `refuseDungeonSelectEntryValidation` 的既有拒绝回包），**绝不静默放行**。
	// 默认档 / 未声明 `[required item]` 的 DGN ⇒ 一行都不执行、不读背包、不写库。
	if !s.options.mapDefinitionCache {
		if err := s.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); err != nil {
			return nil, worldmap.DungeonRoomScene{}, err
		}
	}
	questRecord, questRecordFound, err := s.dungeonQuestRecord(ctx, session.selectedCharacterID)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	if reconciled, reconcileErr := s.reconcileActiveQuestClearLinkedSubQuestsForDungeon(ctx, session, "select_dungeon_before_maze_selection"); reconcileErr != nil {
		s.logGameEvent(session, "game-dungeon-quest-clear-linked-subquest-reconcile-failed",
			"dungeon_id", request.DungeonID,
			"difficulty", request.Difficulty,
			"source", "select_dungeon_before_maze_selection",
			"error", reconcileErr)
	} else if reconciled {
		questRecord, questRecordFound, err = s.dungeonQuestRecord(ctx, session.selectedCharacterID)
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, err
		}
	}

	var antonTransfer *currentAntonHatcheryTransfer
	session.dungeon.mu.Lock()
	if session.dungeon.runtime != nil && session.dungeon.runtime.Session != nil {
		snapshot := session.dungeon.runtime.Session.Snapshot().Run
		if snapshot.Status == worldmap.DungeonRunActive {
			antonTransfer = s.captureCurrentAntonHatcheryTransferLocked(session, request)
			if antonTransfer == nil {
				session.dungeon.mu.Unlock()
				return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("%w: dungeon=%d maze=%d", errDungeonAlreadyActive, snapshot.DungeonID, snapshot.MazeIndex)
			}
		}
	}
	session.dungeon.mu.Unlock()

	chooseMaze := func() (dungeonMazeSelection, error) {
		if partyPlan != nil {
			if partyPlan.dungeonID != dungeon.ID || partyPlan.mazeIndex < 0 || partyPlan.mazeIndex >= len(dungeon.Mazes) {
				return dungeonMazeSelection{}, fmt.Errorf("party leader dungeon selection is invalid: dungeon=%d maze=%d", partyPlan.dungeonID, partyPlan.mazeIndex)
			}
			return dungeonMazeSelection{Index: partyPlan.mazeIndex, Reason: "ordinary_party_leader_frozen_selection"}, nil
		}
		if isCurrentFiendWarChannel(session) {
			return s.selectCurrentFiendWarMaze(session, dungeon)
		}
		return selectDungeonMaze(dungeon.Mazes, request.Difficulty, questRecord, s.chooseDungeonIndex)
	}
	var dimensionHiddenAdmission *dnfdungeon.DimensionHiddenAdmission
	chooseWithDimension := func() (dungeonMazeSelection, error) {
		selected, plan, err := s.selectCurrentDimensionHiddenMaze(session, dungeon, character, chooseMaze, partyPlan != nil)
		dimensionHiddenAdmission = plan
		return selected, err
	}
	mazeSelection, lukeSanctumMaze, err := s.selectCurrentLukeSanctumMaze(session, dungeon, chooseWithDimension, partyPlan != nil)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("choose dungeon maze: %w", err)
	}
	mazeIndex := mazeSelection.Index
	storyExperience := s.captureCurrentDungeonStoryExperience(ctx, dungeon, mazeSelection, request.Difficulty, questRecord)
	if partyPlan != nil && partyPlan.storyQuestID > 0 {
		storyExperience = s.captureCurrentDungeonStoryExperience(ctx, dungeon,
			dungeonMazeSelection{Index: mazeIndex, Reason: "active_quest_connection", QuestID: partyPlan.storyQuestID}, request.Difficulty, questRecord)
	}
	s.logGameEvent(session, "game-dungeon-maze-selected",
		"dungeon_id", request.DungeonID,
		"difficulty", request.Difficulty,
		"maze_index", mazeIndex,
		"selection_reason", mazeSelection.Reason,
		"quest_id", mazeSelection.QuestID,
		"story_experience_quest_id", storyExperience.QuestID,
		"story_experience_rate_percent", storyExperience.RatePercent,
		"quest_record_found", questRecordFound,
		"body_source", "runtime_pvf_quest_connection_and_persisted_quest_state")
	chooser := func(choice worldmap.DungeonMapChoice) (int64, error) {
		if partyPlan != nil {
			mapID, ok := partyPlan.maps[choice.Coordinate]
			if !ok {
				return 0, fmt.Errorf("party leader has no frozen map for room %s", choice.Coordinate)
			}
			for _, candidate := range choice.Candidates {
				if candidate.ID == mapID {
					return mapID, nil
				}
			}
			return 0, fmt.Errorf("party leader map %d is not a candidate for room %s", mapID, choice.Coordinate)
		}
		index, chooseErr := s.chooseDungeonIndex(len(choice.Candidates))
		if chooseErr != nil {
			return 0, chooseErr
		}
		return choice.Candidates[index].ID, nil
	}
	// 深渊房间（操作者清单第 ⑤ 条后半）：必须在 `BuildDungeonLayout` 之前算出来，
	// 因为**房图覆盖**要落在 `BuildDungeonLayout` 与 `NewDungeonRun` 之间
	// （与锦标赛同一处边界，见 dungeon_tournament_entry.go:285）。
	hellMode := s.hellRoomMode()
	// 深渊请求标志按档位取字节：默认读 body[7]（厂商 Flag1），回退档读 body[5:7]。
	// 线上 245/245 次真实 op16 的 body[5:7] 恒为 00 00，所以回退档等于"永远不是深渊"。
	hellRequest := dungeonHellRequestOption(request)
	hellRoom, hellRoomSet, hellRoomErr := s.resolveDungeonHellRoomForEntryRead(
		hellMode, hellRequest, dungeon, mazeIndex, table)
	if hellRoomErr != nil {
		return nil, worldmap.DungeonRoomScene{}, hellRoomErr
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, dungeon.ID, mazeIndex, chooser)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("build dungeon layout: %w", err)
	}
	// 深渊房间那一间换成 `[seal door map index]` 那张图 —— 柱子就长在那张图上。
	// 只有修复档 + 真深渊请求 + 三条 PVF 判据全过（hellRoomSet）才会走到这里；
	// legacy 档或普通进图时 topology 一个字节都不动。
	if hellRoomSet {
		hellCoordinate := worldmap.RoomCoordinate{X: int64(hellRoom.X), Y: int64(hellRoom.Y)}
		beforeMapID := int64(0)
		if before, found := topology.Room(hellCoordinate); found && before.Map != nil {
			beforeMapID = before.Map.Map.ID
		}
		if err := applyDungeonHellRoomMap(topology, table, hellRoom); err != nil {
			return nil, worldmap.DungeonRoomScene{}, err
		}
		afterMapID := int64(0)
		if applied, found := topology.Room(hellCoordinate); found && applied.Map != nil {
			afterMapID = applied.Map.Map.ID
		}
		// 现场证据：`room_map_id_after` 必须等于 `hell_room_map_id`（地轨中心 310 是 60083），
		// 而它换掉的正是迷宫 `[map specification]` 给的那张（310 的 (3,0) 是 36760）。
		s.logGameEvent(session, "dungeon-hell-room-map-override",
			"dungeon_id", request.DungeonID,
			"maze_index", mazeIndex,
			"room", hellCoordinate.String(),
			"hell_room_map_id", hellRoom.MapID,
			"room_map_id_before", beforeMapID,
			"room_map_id_after", afterMapID,
			"source", worldmap.ResolutionHellSealDoor)
	}
	// 深渊波次（`etc/hellparty.etc`）：与深渊房间**同一处、同一时刻**冻结
	// （厂商 `Dungeon.cs:1114-1142 BuildHellPartyRoomInfo` 在准入时一次算完 `Waves`）。
	// 两个波次开关都是 legacy 时**连随机都不抽**（`hellPartyWaveRowsMode` 判据在
	// `dungeonHellPartyWavesNeeded` 里），回退档因此逐字节、逐随机序列等于改动前。
	var hellPartyWaves []dungeonHellPartyWave
	hellPartyDifficulty := resolveDungeonHellPartyMode(hellRequest)
	if hellRoomSet && dungeonHellPartyWavesNeeded() {
		var waveErr error
		hellPartyWaves, waveErr = s.resolveDungeonHellPartyWaves(
			table,
			s.dungeonHellPartyCatalog(),
			s.currentHellPartyAICatalog(),
			hellRoom.MapID,
			dungeon.Metadata.BasisLevel,
			hellPartyDifficulty,
		)
		if waveErr != nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("resolve hell party waves: %w", waveErr)
		}
		s.logGameEvent(session, "dungeon-hell-party-waves-resolved",
			"dungeon_id", request.DungeonID,
			"maze_index", mazeIndex,
			"hell_room_map_id", hellRoom.MapID,
			"hell_party_difficulty", hellPartyDifficulty,
			"group_key", string(dungeonHellPartyGroupKey(hellPartyDifficulty)),
			"waves", len(hellPartyWaves),
			"wave_rows_switch", hellPartyWaveRowsModeFromEnv().String(),
			"monster_info_switch", hellPartyMonsterInfoModeFromEnv().String(),
			"wave_detail", hellPartyWaveDetail(hellPartyWaves),
			"source", "vendor_Dungeon_cs_1151_1239_BuildHellPartyWaves_over_etc_hellparty_etc")
	}
	storyStages, storyErr := resolver.StoryStages(dungeon.ID, mazeIndex)
	if storyErr != nil && !errors.Is(storyErr, worldmap.ErrStoryStageMissing) {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("resolve dungeon story stages: %w", storyErr)
	}
	var bossCoordinate worldmap.RoomCoordinate
	if len(storyStages) != 0 {
		bossCoordinate = storyStages[len(storyStages)-1].Coordinate
	} else {
		bossIndex := 0
		if partyPlan != nil {
			bossIndex = -1
			for index, coordinate := range topology.Bosses {
				if coordinate == partyPlan.boss {
					bossIndex = index
					break
				}
			}
			if bossIndex < 0 {
				return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("party leader boss room %s is not in follower topology", partyPlan.boss)
			}
		} else {
			var err error
			bossIndex, err = s.chooseDungeonIndex(len(topology.Bosses))
			if err != nil {
				return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("choose dungeon boss coordinate: %w", err)
			}
		}
		bossCoordinate = topology.Bosses[bossIndex]
	}
	// 锦标赛：建好赛制运行时并把当轮对手投影成 start 房间的 blocking 怪。
	// 开关关闭时 `prepareTournamentDungeonRun` 返回 (nil, nil)，topology 不被改动，
	// `NewDungeonRun` 之后的一切逐字节等于改动前。
	tournamentRuntime, err := s.prepareTournamentDungeonRun(topology, dungeon, currentDungeonPartyCount(session))
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("create dungeon run: %w", err)
	}
	dungeonSession, err := worldmap.NewDungeonSession(run)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("create dungeon session: %w", err)
	}
	scene, ok := dungeonSession.Scene()
	if !ok {
		return nil, worldmap.DungeonRoomScene{}, errors.New("created dungeon session has no current scene")
	}
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	room, nextObjectKey, err := newRuntimeDungeonRoom(
		scene,
		monsterCatalog,
		firstDungeonMonsterObjectKey,
	)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("create dungeon monster owner: %w", err)
	}
	// 锦标赛：把赛制的 `firstSequence` 绑到本房间第一个已投影 actor 的 object key 上。
	//
	// 厂商在 START_MAP 提交时绑 `instanceRoom.FirstActorSequenceId`
	// （DungeonMapHandler.cs:508-515 / :597-604），而 `DungeonInstance.cs:691` 的
	// `sequenceId = FirstActorSequenceId + index` 说明"sequence = 公告出去的 actor id"。
	// 普通怪和 extended AICharacter 都从 `firstDungeonMonsterObjectKey` 起连续分配
	// `ObjectKey`；op39 上报的 `RuntimeObjectKey` 正是同一个值。APC-only 的 122
	// 必须等 extended actors 挂载后再绑定，不能假定 `room.monsters[0]` 存在。
	// 默认档 `tournamentRuntime == nil` ⇒ 这一整段不可达。
	var aiCharacterCatalog *pvfDungeonAICharacterCatalog
	if len(scene.AICharacters) != 0 {
		aiCharacterCatalog, err = s.dungeonAICharacterCatalog()
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, err
		}
	}
	extendedPlan, err := planRuntimeDungeonExtendedActors(
		scene,
		monsterCatalog,
		aiCharacterCatalog,
		dungeon.Metadata.BasisLevel,
		nextObjectKey,
	)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("create dungeon extended actor owner: %w", err)
	}
	// 深渊波次隐藏模板行：只有"起始房就是深渊房间"时才落在这里（本仓真档
	// `[seal door pos]` 都不是起始房，真正落点是 `prepareRuntimeDungeonTargetRoom`），
	// 两条路径共用同一个追加函数，避免两处口径漂移。
	if hellRoomSet && currentHellPartyMapIsHellRoom(hellRoom, hellRoomSet, scene.Map.Map.ID) {
		if err := s.appendCurrentHellPartyTemplateRows(hellPartyWaves, scene, monsterCatalog, &extendedPlan); err != nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("append hell party template rows: %w", err)
		}
	}
	if err := room.AttachExtendedActors(extendedPlan); err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("attach dungeon extended actor owner: %w", err)
	}
	if tournamentRuntime != nil {
		firstObjectKey, err := tournamentFirstActorObjectKey(room.Snapshot(), tournamentRuntime.snapshot().PathActors)
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, err
		}
		if firstObjectKey == 0 ||
			!tournamentRuntime.tryBindFirstActorSequence(uint16(firstObjectKey)) {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf(
				"bind tournament actor sequence: first_object_key=%d path_actors=%d",
				firstObjectKey, len(tournamentRuntime.PathActors))
		}
	}
	if _, err := s.configurePVFTutorialBasicActionRoom(dungeon, scene, room); err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("configure tutorial basic-action room: %w", err)
	}
	nextObjectKey = extendedPlan.NextObjectKey
	seed := uint32(0)
	if partyPlan != nil {
		seed = partyPlan.seed
	} else {
		var err error
		seed, err = s.chooseDungeonSeed()
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("create dungeon room seed: %w", err)
		}
	}
	var frozenRandomized *currentDungeonRandomizedOwner
	if partyPlan != nil {
		frozenRandomized = partyPlan.randomizedObjects
		if len(dungeon.Mazes[mazeIndex].RandomizedObjects) != 0 && frozenRandomized == nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("party randomized objects were not frozen by leader")
		}
	}
	randomizedObjects, err := s.prepareCurrentDungeonRandomizedOwner(dungeon, mazeIndex, topology, frozenRandomized)
	if err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("prepare randomized dungeon objects before admission: %w", err)
	}
	runtime := &runtimeDungeonState{
		antonHatcheryTransfer:    antonTransfer,
		randomizedObjects:        randomizedObjects,
		lukeSanctumMaze:          lukeSanctumMaze,
		dimensionHiddenAdmission: dimensionHiddenAdmission,
		Request:                  request, Dungeon: dungeon, MazeIndex: mazeIndex,
		storyExperience: storyExperience,
		Character:       character, AccountID: s.accountIDForSession(session),
		entryCharacterLevel: character.Level,
		entryPartyCount:     min(4, max(1, currentDungeonPartyCount(session))),
		Session:             dungeonSession, Room: room, NextObjectKey: nextObjectKey,
		BossCoordinate: bossCoordinate, BossSet: true,
		LayeredMapIndex: -1, StoryStageIndex: -1, Seed: seed,
		LayerChains: make(map[worldmap.RoomCoordinate]*runtimeDungeonLayerChain),
		StoryStages: append([]worldmap.DungeonStoryStage(nil), storyStages...),
		Rooms:       make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
		startedAt:   s.gameplayNow(),
	}
	// 深渊房间（操作者清单第 ⑤ 条后半、"已完成任务角色开深渊看不到深渊房间/柱子"）：
	// 厂商 Dungeon.cs:904-1029 FindHellMapRoom 只在**这条 op16 真是深渊派对请求**
	// （低字节 = HellPartyRequestFlag，DungeonEntryHandler.cs:825-831）时才算，
	// 判据三条逐条保留在 resolveDungeonHellRoom 里。算不出来就保持缺席投影
	// （HellRoomSet=false），绝不发明坐标。legacy 档下两个字节都恒为 0。
	runtime.HellRoom = hellRoom
	runtime.HellRoomSet = hellRoomSet
	// 波次冻结态（见 dungeon_hell_party_wave.go）：只有 hellRoomSet 且至少一个波次开关
	// 是修复档时才非空。房间 actor 表由 `prepareRuntimeDungeonTargetRoom` 追加
	// （真正进深渊房间那一间走的是这条路）。
	runtime.HellPartyWaves = hellPartyWaves
	runtime.HellPartyDifficulty = hellPartyDifficulty
	if hellMode.enabled() {
		runtime.StartMapHellPartyMode, runtime.StartMapHellFogFlag =
			dungeonStartMapHellState(hellMode, hellRequest)
	}
	runtime.Tournament = tournamentRuntime
	// 厂商 TournamentDungeonApplicationService.cs:70-72：锦标赛有自己的终局/选奖门，
	// 普通"到达 Boss 终点即通关"必须**显式关掉**。默认档 tournamentRuntime == nil
	// ⇒ 该字段恒为 false，`shouldClearDungeon` 的判定与改动前完全一致。
	runtime.IgnoreDefaultDungeonClear = tournamentRuntime != nil
	if _, err := s.applyPVFTutorialBasicActionRoomToSession(
		session,
		runtime,
		scene,
		"initial_room_before_start_map",
	); err != nil {
		return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("apply tutorial basic-action initial room: %w", err)
	}
	scene, _ = dungeonSession.Scene()
	if isPVFTutorialDungeon(runtime) && hasPersistedDungeonTutorialCompletion(character) {
		runtime.tutorialCompletionPersisted = true
		runtime.tutorialCompletedReentry = true
	}
	runtime.Rooms[runtimeDungeonRoomKeyFromScene(scene)] = &runtimeDungeonRoomVisit{
		Scene:   scene,
		Room:    room,
		Seed:    seed,
		DropRNG: seed,
	}
	if s.currentRiftEntryEnabled(runtime.Dungeon.ID) {
		runtime.riftLoot, err = s.prepareCurrentRiftLoot(request.Difficulty)
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("prepare rift loot before ticket/count commit: %w", err)
		}
	}
	if s.currentTimeRiftLootEnabled(runtime.Dungeon.ID) {
		runtime.riftLoot, err = s.prepareCurrentTimeRiftLoot(runtime.Dungeon, request.Difficulty)
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("prepare time rift loot before admission: %w", err)
		}
	}
	if s.currentEpicRoadLootEnabled(runtime.Dungeon.ID) {
		runtime.riftLoot, err = s.prepareCurrentEpicRoadLoot(runtime.Dungeon.ID)
		if err != nil {
			return nil, worldmap.DungeonRoomScene{}, fmt.Errorf("prepare epic road boss loot before admission: %w", err)
		}
	}
	if err := s.freezeCurrentEpicEntry(session, runtime); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	if err := s.freezeCurrentSemesterRun(session, runtime); err != nil {
		return nil, worldmap.DungeonRoomScene{}, err
	}
	// 按需地图和运行时准备成功后再消费原有票据；未启用保持原消费位置。
	if s.options.mapDefinitionCache {
		if err := s.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); err != nil {
			return nil, worldmap.DungeonRoomScene{}, err
		}
	}
	return runtime, scene, nil
}

func (s *Service) commitDungeonRuntime(session *gameSession, runtime *runtimeDungeonState) error {
	return s.commitDungeonRuntimeWithPetClock(session, runtime, true)
}

func (s *Service) commitDungeonRuntimeWithPetClock(session *gameSession, runtime *runtimeDungeonState, startPetClock bool) error {
	if session == nil || runtime == nil || runtime.Session == nil {
		return errDungeonWorldMapUnavailable
	}
	if err := s.validateCurrentRaidDungeonEntry(session, runtime); err != nil {
		return err
	}
	if !isPVFTutorialDungeon(runtime) {
		characterID := session.selectedCharacterID
		townID, areaID, err := currentSceneTransitionLocation(runtime.Character, true)
		if err != nil {
			return fmt.Errorf("%w: %v", errCurrentDungeonTownReturnOriginUnavailable, err)
		}
		if err := validateCurrentDungeonTownReturnOrigin(runtime.townReturnOrigin, characterID, townID, areaID); err != nil {
			return err
		}
	}
	session.dungeon.mu.Lock()
	petClockCommitted := false
	defer func() {
		session.dungeon.mu.Unlock()
		if petClockCommitted {
			s.flushCurrentPetGrowthClockTransitions(session, "dungeon_runtime_committed")
		}
	}()
	if runtime.epicEntry != nil || s.currentEpicEntryEnabled(runtime.Dungeon.ID) {
		if err := s.commitCurrentEpicEntryLocked(session, runtime); err != nil {
			return err
		}
		runtime.petGrowthEntryPending = !startPetClock
		if startPetClock {
			s.queueCurrentPetGrowthDungeonEntryClock(session, runtime)
			petClockCommitted = true
		}
		return nil
	}
	if session.dungeon.runtime != nil && session.dungeon.runtime.Session != nil {
		snapshot := session.dungeon.runtime.Session.Snapshot().Run
		if snapshot.Status == worldmap.DungeonRunActive {
			return fmt.Errorf("%w: dungeon=%d maze=%d", errDungeonAlreadyActive, snapshot.DungeonID, snapshot.MazeIndex)
		}
		s.cancelCurrentDungeonDeathReturnLocked(session, session.dungeon.runtime, "new_dungeon_run_replaces_previous_runtime")
		s.cancelCurrentCrystalContractTimerLocked(session, session.dungeon.runtime, "new_dungeon_run_replaces_previous_runtime")
		s.cancelCurrentDungeonCardAutoFlipLocked(session, session.dungeon.runtime, "new_dungeon_run_replaces_previous_runtime")
		// 被替换掉的旧运行时必须先脱离队伍实例，否则它的成员名额会一直挂着。
		s.releasePartyDungeonInstance(session.dungeon.runtime, session.selectedCharacterID, "runtime_replaced_by_new_run")
	}
	session.dungeon.runToken = nextCurrentDungeonDeathGeneration(session.dungeon.runToken)
	scene, hasScene := runtime.Session.Scene()
	dailyCountEntry, err := s.currentDailyCountEntryEnabled(runtime.Dungeon)
	if err != nil {
		return err
	}
	if hasScene && !dailyCountEntry {
		if err := s.debitDungeonRoomFatigue(session, runtime, scene); err != nil {
			return err
		}
	}
	if err := s.consumeCurrentRiftEntry(session, runtime); err != nil {
		return err
	}
	if err := s.consumeCurrentDayDungeonEntry(session, runtime); err != nil {
		return err
	}
	if dailyCountEntry {
		if err := s.consumeCurrentDailyCountEntry(session, runtime, scene, hasScene); err != nil {
			return err
		}
	}
	runtime.lifecycleToken = session.dungeon.runToken
	runtime.ridableReportChannelID = session.channel.ID
	runtime.ridableReportChannelType = session.channel.Type
	session.dungeon.runtime = runtime
	// 阶段二：同一队伍的成员进图时解析到同一个实例键（队伍+副本+迷宫+房间+地图+种子）
	// 就接入同一个共享实例；单人/无队伍/房间对不上时返回 nil，per-session 路径不变。
	// 这里只改共享对象归属，不额外发包，op24/op29 的既有顺序不受影响。
	if scene, ok := runtime.Session.Scene(); ok {
		s.adoptPartyDungeonInstance(session, runtime, scene)
		attachCurrentSemesterPartyMember(session, runtime)
	}
	runtime.petGrowthEntryPending = !startPetClock
	if startPetClock {
		s.queueCurrentPetGrowthDungeonEntryClock(session, runtime)
		petClockCommitted = true
	}
	return nil
}

func (s *Service) dungeonCharacter(ctx context.Context, characterID uint16, sessions ...*gameSession) (dnfrepo.CharacterRecord, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return dnfrepo.CharacterRecord{}, dungeoncmd.ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(characterID)))
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if !found {
		return dnfrepo.CharacterRecord{}, errDungeonCharacterNotFound
	}
	character = dnfrepo.CloneCharacter(character)
	accountID := strings.TrimSpace(s.accountIDForSession(sessions...))
	if stored := strings.TrimSpace(character.AccountID); stored != "" && accountID != "" && stored != accountID {
		return dnfrepo.CharacterRecord{}, fmt.Errorf("%w: selected=%s stored=%s", errDungeonAccountMismatch, accountID, stored)
	}
	for _, p := range sessions {
		if isCurrentFiendWarChannel(p) && p.selectedCharacterID == characterID {
			applyCurrentFiendWarCharacterPosition(&character, p, characterID)
			break
		}
	}
	return character, nil
}

func applyCurrentFiendWarCharacterPosition(character *dnfrepo.CharacterRecord, session *gameSession, characterID uint16) {
	if character == nil || characterID == 0 || !isCurrentFiendWarChannel(session) || session.selectedCharacterID != characterID {
		return
	}
	session.townMu.Lock()
	pos := session.townPositionSnapshot
	session.townMu.Unlock()
	if pos.CharacterID != characterID {
		return
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["town_id"], character.Stats["area_id"] = int64(pos.TownID), int64(pos.AreaID)
	if pos.PositionValid {
		character.Stats["pos_x"], character.Stats["pos_y"] = int64(pos.PositionX), int64(pos.PositionY)
		character.Stats["direction"] = int64(pos.MovementCode)
		character.Stats["area_state"] = 0
	}
}

func (s *Service) dungeonQuestRecord(ctx context.Context, characterID uint16) (dnfrepo.QuestRecord, bool, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Quest == nil {
		return dnfrepo.QuestRecord{}, false, errDungeonQuestRepositoryUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	characterKey := strconv.Itoa(int(characterID))
	record, found, err := repositories.Quest.Load(ctx, characterKey)
	if err != nil {
		return dnfrepo.QuestRecord{}, false, err
	}
	if !found {
		return dnfrepo.QuestRecord{CharacterID: characterKey}, false, nil
	}
	return dnfrepo.CloneQuest(record), true, nil
}

func validateDungeonEntry(character dnfrepo.CharacterRecord, dungeon worldmap.Dungeon, session *gameSession) error {
	partyCount := currentDungeonPartyCount(session)
	fatigue, fatigueKnown := dnfdungeon.CharacterStat(character.Stats, "fatigue", "fp", "疲劳")
	fatigueCost, _, err := currentDungeonStartFatigueRule(dungeon)
	if err != nil {
		return err
	}
	return dnfdungeon.ValidateEntry(dnfdungeon.EntryPolicy{
		CharacterLevel:      character.Level,
		MinimumLevel:        dungeon.Metadata.MinimumRequiredLevel.Value,
		MinimumLevelSet:     dungeon.Metadata.MinimumRequiredLevel.Set,
		PartyCount:          partyCount,
		PartyLimit:          dungeon.Metadata.LimitPartyCount.Value,
		PartyLimitSet:       dungeon.Metadata.LimitPartyCount.Set,
		NoFatigue:           currentDungeonUsesNoFatigue(dungeon),
		EnterWithoutFatigue: dungeon.Metadata.Flags["enter without fatigue"],
		Fatigue:             fatigue,
		FatigueKnown:        fatigueKnown,
		FatigueCost:         fatigueCost,
	})
}

// currentDungeonPartyCount 取当前会话的队伍人数（至少 1）。
// 从 validateDungeonEntry 原样抽出，供锦标赛运行时的 partyCount 复用同一口径。
func currentDungeonPartyCount(session *gameSession) int {
	partyCount := 1
	if session != nil {
		session.party.mu.Lock()
		if len(session.party.state.Members) > partyCount {
			partyCount = len(session.party.state.Members)
		}
		session.party.mu.Unlock()
	}
	return partyCount
}

func (s *Service) chooseDungeonIndex(limit int) (int, error) {
	if limit <= 0 {
		return 0, worldmap.ErrMazeNotFound
	}
	if limit == 1 {
		return 0, nil
	}
	if s != nil && s.dungeonChoice != nil {
		index, err := s.dungeonChoice(limit)
		if err != nil {
			return 0, err
		}
		if index < 0 || index >= limit {
			return 0, fmt.Errorf("dungeon choice index %d outside [0,%d)", index, limit)
		}
		return index, nil
	}
	index, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, fmt.Errorf("choose dungeon random index: %w", err)
	}
	return int(index.Int64()), nil
}

func (s *Service) chooseDungeonSeed() (uint32, error) {
	if s != nil && s.dungeonSeed != nil {
		return s.dungeonSeed()
	}
	var data [4]byte
	if _, err := cryptorand.Read(data[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(data[:]), nil
}
