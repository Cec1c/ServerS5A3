// service.go 负责 DNF bridge 生命周期、目录加载和监听器管理。
// 这里不写玩法状态，只装配频道服与 game 端口的 transport 边界。
package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"

	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	dnfcharstat "longheng.io/server/internal/modules/dnf/charstat"
	dnfcombatpower "longheng.io/server/internal/modules/dnf/combatpower"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dproto"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/party"
	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/signupbenefits"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	appkit "longheng.io/server/internal/platform/app"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Service 是 DNF 最新客户端 bridge 的生命周期组件。
type Service struct {
	guildCreateMu sync.Mutex
	// Serializes warehouse ACKs and snapshots; never acquire while holding an
	// actor reservation. A snapshot must not overtake its transfer's ACK.
	guildCargoMu     sync.Mutex
	levelUpRewards   levelUpRewardRuntime
	activityControls *onlineevent.Controls
	logger           *slog.Logger
	options          options

	mu                         sync.Mutex
	catalog                    *channelcatalog.Catalog
	channelScript              []byte
	listeners                  []net.Listener
	conns                      map[net.Conn]struct{}
	gameSessions               map[uint16]*gameSession
	characterAssetReservations map[uint16]*currentCharacterAssetReservation
	// liveGameSessions 是"本进程里仍然活着的 game 传输"索引（conn_id → session），
	// 由 s.mu 保护。角色目录 gameSessions 每个角色只保留一条会话，因此它回答不了
	// "这个角色还有没有第二条活着的连接"；而同角色的第二条连接（当前客户端会给每个
	// 角色开两条传输，见 online_player_manager.go 的 LeaveAreaSession 注释）正是现场
	// 2026-09-26 的关键形态：目录 owner 那条 socket 关闭时，角色其实还在另一条 socket
	// 上玩。见 game_sessions.go 的 handoverRuntimePartyToLiveSession。
	liveGameSessions          map[string]*gameSession
	nextGameSessionGeneration uint64
	auctionMaintenanceContext context.Context
	auctionCategoryExtraOnce  sync.Once
	auctionCategoryExpertJobs map[uint32][]byte
	auctionCategoryExtractors map[uint32]bool
	auctionCategoryExtraErr   error
	auctionMu                 sync.Mutex
	auctionItemEligibility    func(uint32) bool
	auctionCategoryItem       func(uint16, uint32) bool
	tradeMu                   sync.Mutex
	itemTrades                map[uint16]*onlineItemTrade
	expertJobStoreMu          sync.Mutex
	expertJobStoreOpMu        sync.Mutex
	expertJobStores           map[uint16]*currentExpertJobStore
	expertJobVisitors         map[*gameSession]uint16
	raidMu                    sync.Mutex
	raids                     map[uint32]*runtimeRaidState
	currentRaidDisconnects    map[uint16]currentRaidDisconnectSeat
	// Protected by raidMu; bounds queued deadline cleanup to one worker per runtime.
	currentRaidDeadlineQueued map[*runtimeDungeonState]bool
	currentRaidApplications   map[uint16]currentRaidApplication
	currentRaidInvitations    map[uint16]currentRaidInvitation
	raidWeeklyIntents         map[string]*currentRaidWeeklyIntent
	raidWeeklyWorkRunning     bool
	antonRewardFlows          map[string]*currentAntonRewardFlow
	antonRewardWorkRunning    bool
	nextRaidSeq               uint32
	packetLog                 *packetLogger
	partyUDPRelay             *currentPartyUDPRelay
	// partyUDPRelayRemote 是 remote 档（厂商架构）的控制通道客户端：
	// 注册到独立进程 a21-relay 换取端口矩阵，本进程不 bind 任何中继 UDP 端口。
	// 与 partyUDPRelay 至多一个非 nil（由 newPartyUDPRelayBackends 决定）。
	// 见 party_udp_relay_remote.go。
	partyUDPRelayRemote *currentPartyUDPRelayRemote
	runtimePartyManager *party.RuntimePartyManager
	partyDungeons       *partyDungeonInstanceRegistry
	// partyDungeonLoadingCycles 是"全队 op37 finish-loading 到齐再统一放行"的组队状态
	// （每个队伍一条），由 partyDungeonLoadingMu 保护。
	// 见 party_dungeon_loading_barrier.go。
	partyDungeonLoadingMu     sync.Mutex
	partyDungeonLoadingCycles map[uint16]*partyDungeonLoadingCycle
	cancel                    context.CancelFunc
	wg                        sync.WaitGroup
	// partyForceRuntime 是"强制组队"测试触发器的运行时状态（记忆已处理的命令内容）。
	// 只有 DNFBRIDGE_PARTY_FORCE=on 时才会被创建。见 party_force_trigger.go。
	partyForceRuntime *partyForceRuntime
	// partyForceInviteRuntime 是"服务端代发真实组队邀请"测试触发器的运行时状态。
	// 只有 DNFBRIDGE_PARTY_FORCE_INVITE=on 时才会被创建。见 party_force_invite.go。
	partyForceInviteRuntime *partyForceInviteRuntime
	// forceSelectRuntime 是"模拟客户端打开副本选择器（组队选择器 fan-out）"测试触发器的
	// 运行时状态。只有 DNFBRIDGE_FORCE_SELECT=on 时才会被创建。见 force_select_trigger.go。
	forceSelectRuntime *forceSelectRuntime
	// forceTownExitRuntime 是"模拟客户端走出赛丽亚房间（op1425 回上一个村镇）"测试触发器的
	// 运行时状态。只有 DNFBRIDGE_FORCE_TOWN_EXIT=on 时才会被创建。见 force_town_exit_trigger.go。
	forceTownExitRuntime *forceTownExitRuntime
	packetID             uint64
	clientAccounts       clientAccountRegistry
	// loginTokens 保存启动器登录签发的令牌到账号的映射（见 account_login_token.go）。
	loginTokens       accountLoginTokenStore
	localLoginMu      sync.Mutex
	localLoginEpoch   uint64
	localLoginStopped bool
	localLoginTokens  map[string]struct{}
	accountAPI        *accountAPIServer
	signupBenefits    *signupbenefits.Store
	// 账号判定只有两条依据：启动器登录令牌（loginTokens）与本地客户端进程号登记
	// （clientAccounts）。原来"最近登录 IP → 账号"的内存表与 accountIPCache
	// 已于 2026-09-25 删除，见 client_account_ip.go。

	initialEquipmentMu      sync.Mutex
	initialEquipmentArchive *platformpvf.Archive
	initialEquipmentByJob   map[byte][]initialEquipmentEntry
	initialEquipmentLoadErr error
	// dungeonRuleSource lets focused tests provide the exact approved PVF texts
	// they exercise. Production leaves it nil and uses initialEquipmentArchive.
	dungeonRuleSource       initialEquipmentTextSource
	weeklyCatalogMu         sync.Mutex
	weeklyCatalog           *onlineevent.WeeklyCatalog
	monthlyCatalogMu        sync.Mutex
	monthlyCatalog          *onlineevent.MonthlyCatalog
	pvfItemCatalogMu        sync.Mutex
	pvfItemCatalog          *pvfDungeonDropCatalog
	pvfItemCatalogLoadErr   error
	joustCatalogMu          sync.Mutex
	joustCatalog            *dnfjoust.Catalog
	joustCatalogLoadErr     error
	joustHistoryMu          sync.Mutex
	joustOperationMu        sync.Mutex
	spendTimeCatalogMu      sync.Mutex
	spendTimeCatalogState   *currentSpendTimeRuntimeCatalog
	spendTimeCatalogLoadErr error
	expertJobMu             sync.Mutex
	expertJobCatalog        *dnfexpertjob.Catalog
	expertJobCatalogLoadErr error
	petCatalogMu            sync.Mutex
	petCatalog              *dnfpet.PVFCatalog
	petCatalogLoadErr       error
	premiumCatalogMu        sync.Mutex
	premiumCatalog          *currentPremiumCatalog
	premiumCatalogLoadErr   error
	repairCostMu            sync.Mutex
	repairCostCatalog       *currentRepairCostCatalog
	repairCostLoadErr       error
	npcShopCatalogMu        sync.Mutex
	npcShopCatalog          *currentNPCShopCatalog
	npcShopCatalogLoadErr   error
	adventureGroupMu        sync.Mutex
	adventureGroupLoginMu   sync.Mutex
	adventureGroupRuntimeMu sync.Mutex
	adventureGroupTable     *adventuregroup.Tables
	adventureGroupLoadErr   error
	// lastOnlineCharacter 是账号级写序列点，见 account_last_online_character.go。
	lastOnlineCharacter    lastOnlineCharacterState
	honorMu                sync.Mutex
	honorTable             *dnfhonor.Tables
	honorLoadErr           error
	initialSkillsMu        sync.Mutex
	initialSkillsByJob     map[byte][]initialSkillEntry
	skillCatalog           *dnfskill.Table
	initialSPTable         map[int]int
	initialTPTable         map[int]int
	professionMu           sync.Mutex
	professionProfiles     *dnfprofession.Profiles
	professionProfilesErr  error
	questCatalogMu         sync.Mutex
	questCatalog           *dnfquest.Catalog
	questCatalogLoadErr    error
	ceraShopMu             sync.Mutex
	ceraShopCatalog        *pvfCeraShopCatalog
	ceraShopCatalogLoadErr error
	ceraShopPurchaseMu     sync.Mutex
	deathTowerMu           sync.Mutex
	deathTowerStageMapIDs  []int
	deathTowerBasisLevel   int
	titleBookMappingMu     sync.Mutex
	titleBookMappingCache  map[int32]titleBookMappingEntry
	// collectboxCatalog* 缓存运行期 PVF 的收集箱目录（localization/collectbox/
	// collectboxinfo.etc），见 collectbox_state.go。
	collectboxCatalogMu      sync.Mutex
	collectboxCatalog        currentCollectboxCatalog
	collectboxCatalogLoaded  bool
	collectboxCatalogLoadErr error
	onlinePlayers            *OnlinePlayerManager
	characterStatsMu         sync.Mutex
	characterStats           *dnfcharstat.Table
	characterStatsLoadErr    error
	equipmentStatsMu         sync.Mutex
	equipmentStatPaths       map[int64]string
	equipmentStats           map[int64]dnfcharstat.Vector
	equipmentStatsLoadErr    error
	combatPowerMu            sync.Mutex
	combatPowerCatalog       *dnfcombatpower.Catalog
	combatPowerCatalogErr    error
	townCatalogMu            sync.RWMutex
	townCatalog              *dnftown.Table

	imageCommunicationMu        sync.Mutex
	imageCommunicationResources *currentImageCommunicationResources

	// seriaRoomDoors 缓存"赛丽亚房间外门口"的 PVF 解析结果（键 = 房间的城镇/区域），
	// 见 town_seria_room_door.go：每次 op1425 都要用，但 PVF 文档只需读一次。
	seriaRoomDoorMu sync.Mutex
	seriaRoomDoors  map[currentSeriaRoomDoorKey]currentSeriaRoomDoorEntry
	// seriaRoomDoorMapReader 是"读一份城镇区域地图文档"的注入点：生产为空（走运行时 PVF），
	// 单测用它喂两份坐标不同的地图，钉住落点随 PVF 变化、绝不是写死的坐标。
	seriaRoomDoorMapReader  func(area dnftown.Area) (worldmap.Map, bool)
	worldMapMu              sync.RWMutex
	worldMapTable           *worldmap.Table
	worldMapResolver        *worldmap.Resolver
	dungeonMonsterTable     *pvfDungeonMonsterCatalog
	dungeonAICharacterTable *pvfDungeonAICharacterCatalog
	dungeonTutorialScripts  *pvfDungeonTutorialScriptCatalog
	// dungeonHellPartyTable 是 `etc/hellparty.etc`（深渊波次表）的只读索引，与
	// `dungeonMonsterTable` 同一处预载、同一把锁保护；读不到就是 nil（= 没有波次，
	// 与厂商 `HellPartyData.ReadHellPartyScript` 的降级一致）。见 dungeon_hell_party_catalog.go。
	dungeonHellPartyTable *pvfDungeonHellPartyCatalog
	dungeonChoice         func(int) (int, error)
	dungeonSeed           func() (uint32, error)
	gameplayTimers        gameplayTimeQueue
	repositoryProvider    func() (dnfrepo.Group, bool)
	initialTownEntryWait  func(time.Duration)
	dprotoProvider        dproto.Provider
	// questFinishResourceLoader 是"完成任务所需的 progression 表 + 掉落目录"的注入点
	// （**仅单测**，与 dungeonChoice / dungeonSeed / initialTownEntryWait 同风格）：
	// 非 nil 时 `currentQuestFinishResources` 直接返回它的结果，生产恒为 nil（走
	// runtime PVF）。存在的唯一理由：专家职业那条加载失败分支位于本加载之后，单测
	// 必须能让本加载"成功"才能走到它（见 quest_finish_failure_reply_test.go）。
	questFinishResourceLoader func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error)
}

// ServiceOptions 描述 dnfbridge 对外注入的项目侧依赖。
// 这里只保存依赖入口，不直接创建 MySQL/Redis，也不把玩法状态塞进 bridge。
type ServiceOptions struct {
	RepositoryProvider func() (dnfrepo.Group, bool)
	DprotoProvider     dproto.Provider
}

// New 使用框架环境创建 DNF bridge 服务。
func New(env *appkit.Env) *Service {
	return NewWithOptions(env, ServiceOptions{})
}

// NewWithOptions 使用框架环境和项目侧依赖创建 DNF bridge 服务。
func NewWithOptions(env *appkit.Env, serviceOptions ServiceOptions) *Service {
	if env == nil {
		env = &appkit.Env{}
	}
	core := env.Core()
	service := &Service{
		logger:                  core.Logger,
		options:                 optionsFromConfig(core.Config),
		conns:                   make(map[net.Conn]struct{}),
		gameSessions:            make(map[uint16]*gameSession),
		liveGameSessions:        make(map[string]*gameSession),
		itemTrades:              make(map[uint16]*onlineItemTrade),
		expertJobStores:         make(map[uint16]*currentExpertJobStore),
		expertJobVisitors:       make(map[*gameSession]uint16),
		raids:                   make(map[uint32]*runtimeRaidState),
		initialEquipmentArchive: env.PVF,
		repositoryProvider:      serviceOptions.RepositoryProvider,
		dprotoProvider:          serviceOptions.DprotoProvider,
		clientAccounts:          newClientAccountRegistry(),
		localLoginStopped:       true,
		runtimePartyManager:     party.NewRuntimePartyManager(),
	}
	service.gameplayTimers = newProcessGameplayTimeQueue(func(name string, recovered any) {
		service.logWarn("dnfbridge gameplay timer callback panic", "timer_name", name, "recovered", recovered)
	})
	service.onlinePlayers = newOnlinePlayerManager()
	return service
}

// Name 返回生命周期组件名。
func (s *Service) Name() string {
	return "dnfbridge-service"
}

// Start 加载外部 channel_info.etc，并启动频道服和 game 端口监听。
func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.validateCurrentSceneActorRoute(); err != nil {
		return err
	}
	if err := s.validateDprotoConfiguration(); err != nil {
		return err
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		return err
	}
	catalog, channelScript, err := s.loadChannelAssets()
	if err != nil {
		return err
	}
	packetLog, err := s.openPacketLog()
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.catalog = catalog
	s.channelScript = append([]byte(nil), channelScript...)
	s.cancel = cancel
	s.packetLog = packetLog
	if s.initialTownEntryWait == nil {
		// Initial-town visual delay is disabled. Production never blocks the
		// server packet stream at the retained test boundary.
		s.initialTownEntryWait = func(time.Duration) {}
	}
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.mu.Unlock()

	// PVF preload stages run in a fixed order before any listener opens. Keep
	// one row per stage: the order, each loader's own wrapped error text, and
	// the shared cleanup path are observable startup behavior. The loop must
	// not re-wrap loader errors or change what is reset on failure; name only
	// labels the stage for audit, it is never merged into the returned error.
	pvfPreloads := []struct {
		name string
		load func(context.Context) error
	}{
		{"initial equipment index", s.preloadInitialEquipmentIndex},
		{"item catalog", s.preloadPVFItemCatalog},
		{"expert job catalog", s.preloadExpertJobCatalog},
		{"adventure group table", s.preloadAdventureGroupTable},
		{"honor table", s.preloadHonorTable},
		{"town catalog", s.preloadTownCatalog},
		{"skill catalog", s.preloadSkillCatalog},
		{"profession profiles", s.preloadProfessionProfiles},
		{"quest catalog", s.preloadQuestCatalog},
		{"dungeon world map", s.preloadDungeonWorldMap},
		{"character stat table", s.preloadCharacterStatTable},
		{"equipment stat index", s.preloadEquipmentStatIndex},
		{"combat power affix catalog", s.preloadCombatPowerAffixCatalog},
		{"装备合成目录", s.preloadEquipmentRegenerationCatalog},
	}
	for _, preload := range pvfPreloads {
		if err := preload.load(runCtx); err != nil {
			cancel()
			closeErr := packetLog.Close()
			s.mu.Lock()
			s.catalog = nil
			s.cancel = nil
			s.packetLog = nil
			s.mu.Unlock()
			return errors.Join(err, closeErr)
		}
	}
	gamePorts := s.gameListenPorts(catalog)
	if len(gamePorts) == 0 {
		cancel()
		closeErr := packetLog.Close()
		s.mu.Lock()
		s.catalog = nil
		s.cancel = nil
		s.packetLog = nil
		s.mu.Unlock()
		return errors.Join(fmt.Errorf("dnfbridge has no game listen ports"), closeErr)
	}
	partyRelayRemote, partyRelay, err := s.newPartyUDPRelayBackends()
	if err != nil {
		cancel()
		closeErr := packetLog.Close()
		s.mu.Lock()
		s.catalog = nil
		s.cancel = nil
		s.packetLog = nil
		s.mu.Unlock()
		return errors.Join(err, closeErr)
	}
	s.mu.Lock()
	s.partyUDPRelay = partyRelay
	s.partyUDPRelayRemote = partyRelayRemote
	s.mu.Unlock()

	if err := s.listen(runCtx, s.options.channelListen, s.handleChannelConn); err != nil {
		cancel()
		_ = s.closePartyUDPRelayBackends()
		closeErr := packetLog.Close()
		s.mu.Lock()
		s.catalog = nil
		s.cancel = nil
		s.packetLog = nil
		s.partyUDPRelay = nil
		s.partyUDPRelayRemote = nil
		s.mu.Unlock()
		return errors.Join(err, closeErr)
	}
	s.startGameplayTimeQueue(runCtx)
	s.startCurrentAuctionClock(runCtx)
	s.startCurrentJoustScheduler(runCtx)
	s.startCurrentRaidClock(runCtx)
	s.startCurrentLevelUpRewardClock(runCtx)
	// 强制组队测试触发器：**默认 off**（off 档直接 return，零副作用）。
	// 见 party_force_trigger.go。
	s.startPartyForceTrigger(runCtx)
	// 服务端代发真实组队邀请测试触发器：**默认 off**（off 档直接 return，零副作用）。
	// 见 party_force_invite.go。
	s.startPartyForceInviteTrigger(runCtx)
	// 模拟客户端打开副本选择器测试触发器：**默认 off**（off 档直接 return，零副作用）。
	// 用于在"游戏内键鼠注入被 TP 拦截"的前提下复现/验证组队选择器 fan-out。
	// 见 force_select_trigger.go。
	s.startForceSelectTrigger(runCtx)
	// 模拟客户端走出赛丽亚房间测试触发器：**默认 off**（off 档直接 return，零副作用）。
	// 组队测试必须走这条生产路径（两人各自出房门 → 同一个共享城镇区域 + 存在感发布），
	// 而不是改角色数据到别的城镇。见 force_town_exit_trigger.go。
	s.startForceTownExitTrigger(runCtx)
	// 模拟队长选定副本（整队进图）测试触发器：**默认 off**（off 档直接 return，零副作用）。
	// 用于在"游戏内键鼠注入被 TP 拦截"的前提下复现/验证整队进图与副本内同步。
	// 见 force_dungeon_trigger.go。
	s.startForceDungeonTrigger(runCtx)

	if err := s.startAccountAPI(runCtx); err != nil {
		cancel()
		_ = s.Stop(context.Background())
		return err
	}

	for _, port := range gamePorts {
		addr := gameListenAddress(s.options.gameListenHost, port)
		if err := s.listen(runCtx, addr, s.handleGameConn); err != nil {
			cancel()
			_ = s.Stop(context.Background())
			return err
		}
	}
	if s.options.partyDungeonOwnerEnvBad {
		s.logInfo("dnfbridge party dungeon owner mode invalid; using default",
			"env", partyDungeonOwnerModeEnv,
			"value", s.options.partyDungeonOwnerEnvRaw,
			"fallback", partyDungeonOwnerSceneContext.String())
	}
	if s.options.partyDungeonUserStateEnvBad {
		s.logInfo("dnfbridge party dungeon all-member user state invalid; using default",
			"env", partyDungeonAllMemberUserStateEnv,
			"value", s.options.partyDungeonUserStateEnvRaw,
			"fallback", partyDungeonUserStateValueSelf)
	}
	if s.options.partyPeerRowAcc.bad {
		s.logInfo("dnfbridge party peer row acc invalid; using auto",
			"env", partyPeerRowAccEnv,
			"value", s.options.partyPeerRowAcc.raw,
			"fallback", partyPeerRowAccAutoValue)
	}
	if s.options.joustEventCatalogEnvBad {
		s.logInfo("dnfbridge joust event catalog mode invalid; using auto",
			"env", currentSpendTimeJoustCatalogEnv,
			"value", s.options.joustEventCatalogEnvRaw,
			"fallback", currentSpendTimeJoustCatalogAuto)
	}
	if mode, raw, bad := itemSlotFamilyGuardSetting(); bad {
		s.logInfo("dnfbridge item slot family guard invalid; using default",
			"env", itemSlotFamilyGuardEnv,
			"value", raw,
			"fallback", mode)
	}
	if _, raw, bad := cloneAvatarAppearanceSetting(); bad {
		s.logInfo("dnfbridge clone avatar appearance invalid; using default",
			"env", cloneAvatarAppearanceEnv,
			"value", raw,
			"fallback", cloneAvatarAppearanceOn)
	}
	if _, raw, bad := cloneAvatarRowClearIDSetting(); bad {
		s.logInfo("dnfbridge clone avatar row clear id invalid; using default",
			"env", cloneAvatarRowClearIDEnv,
			"value", raw,
			"fallback", cloneAvatarAppearanceOn)
	}
	s.logInfo("dnfbridge started",
		"channel_listen", s.options.channelListen,
		"channel_info", s.options.channelInfoFile,
		// 邮箱领域档位（开关本体在 internal/modules/dnf/mail/claimed_letter.go）：
		// finish = 已领取的空信在下次领取时结清并移出收件箱；keep = 逐字节回退。
		"mailbox_claimed_letter", dnfmail.ClaimedLetterModeFromEnv().String(),
		// 深渊页资格判定时机（开关本体在 dnfbridge/dungeon_hell_select_page.go）：
		// vendor（默认）= 开选择页时按 [dungeon gate] 先判逐槽位 blocked；legacy = 逐字节回退。
		"hell_select_page_eligibility", s.hellSelectPageEligibilityGate().String(),
		// 城镇 transport（世界地图/传送站、传送阵）之后那条场景收尾 op15 的抑制窗口（毫秒）：
		// 默认 500 = 修复档（窗口外那条 op15 照常应答 ⇒ 副本门口撞一次就弹选图界面；
		// 实测"提交 -> op15"最小 2054.7 ms，500 有 4 倍以上余量）；
		// `legacy`/`off`/`false`/`no` = 逐字节回到改动前的无时限恒吞（回退档，这里回显 0）。
		// 读法与档位见 game_enter_select_dungeon.go 的 currentTownTransportEnterSelectWindow。
		"town_transport_enter_select_window_ms", currentTownTransportEnterSelectWindow().Milliseconds(),
		// 起始房取图优先序（开关本体在 dnfbridge/dungeon_start_map_precedence.go）：
		// specification（默认）= PVF [map specification] 胜过文件名启发式；ownership = 逐字节回退。
		"dungeon_start_map_precedence", s.currentDungeonStartMapPrecedence(),
		// 稀有克隆装扮外观档（开关本体在 dnfbridge/clone_avatar.go）：
		// on（默认）= 克隆装扮显示被克隆时装的外观；off = 逐字节回退到改动前。
		"clone_avatar_appearance", cloneAvatarAppearanceMode(),
		// 外观记录 LinkItemId 档（开关本体同样在 dnfbridge/clone_avatar.go）：
		// on（默认）= 克隆件把"自己"写进外观列表的 LinkItemId u32；off = 回到 0。
		"clone_avatar_row_clear_id", cloneAvatarRowClearIDMode(),
		"channel_server_index", s.channelServerID(),
		"advertise_server_index", s.channelAdvertiseID(),
		"channel_info_body_mode", s.channelInfoBodyMode(),
		"game_initial_mode", s.gameInitialMode(),
		"game_pre_bootstrap", s.gamePreBootstrapMode(),
		"game_post_bootstrap", s.gamePostBootstrapMode(),
		"game_upper_header", s.gameUpperHeaderMode(),
		"game_upper_client_body_codec", s.gameUpperClientBodyCodecMode(),
		"game_dproto_mode", s.gameDprotoMode(),
		"game_listen_host", s.options.gameListenHost,
		"game_ports", len(gamePorts),
		"server_ip", s.options.serverIP,
		"party_udp_relay", s.partyRelayBackend() != nil,
		// 中继后端档：**默认 remote = 厂商架构**（注册到独立进程 a21-relay，本进程不绑中继口）；
		// `mode_requested=remote` 而 `mode=embedded` 就是"配置不全已回退"，原因见同时打印的
		// `party-udp-relay-remote-unavailable` / `party-udp-relay-remote-embedded-fallback` 事件。
		// 见 party_udp_relay_remote.go。
		"party_udp_relay_mode", s.partyUDPRelayEffectiveMode().String(),
		"party_udp_relay_mode_requested", s.options.partyUDPRelayMode.String(),
		"party_udp_relay_remote", s.options.partyUDPRelayRemoteEndpoint,
		"party_udp_relay_remote_secret", s.options.partyUDPRelayRemoteSecretState,
		"party_udp_relay_room_base", s.options.partyUDPRelayRemoteRoomBase,
		"party_udp_relay_port_start", s.options.partyUDPRelayPortStart,
		"party_udp_relay_port_count", s.options.partyUDPRelayPortCount,
		// 下面这些 probe_*/kick_*/fallback 字段**只对 embedded 档生效**（remote 档下本进程
		// 不 bind 任何中继 UDP 端口、不转发队友流量，端口全部由 a21-relay 分配）。
		"party_udp_relay_endpoint_fallback", s.partyUDPRelayEndpointFallbackMode().String(),
		"party_udp_relay_probe_replay", s.partyUDPRelayProbeReplayMode().String(),
		"party_udp_relay_probe_listen", s.partyUDPRelayProbeListenMode().String(),
		"party_udp_relay_probe_ports", formatPartyUDPRelayPorts(s.options.partyUDPRelayProbePorts),
		"party_udp_relay_seed_forward", s.partyUDPRelaySeedForwardMode().String(),
		"party_udp_relay_observed_endpoint_fanout", s.partyUDPRelayObservedEndpointFanoutMode(),
		"party_udp_relay_datagram_trace", s.partyUDPRelayDatagramTraceMode(),
		"party_udp_relay_memo_ttl_seconds", int(s.partyUDPRelayMemoTTL()/time.Second),
		"party_udp_relay_silent_peer_kick", s.partyUDPRelaySilentPeerKickMode(),
		"party_udp_relay_kick_min_age_seconds", int(s.partyUDPRelayKickTiming().minAge/time.Second),
		"party_udp_relay_resend_seconds", int(s.partyUDPRelayKickTiming().window/time.Second),
		"party_peer_self_endpoint", s.partyPeerSelfEndpoint().String(),
		"party_peer_port_endian", s.partyPeerPortEndian().String(),
		"party_peer_endpoint_include_self", s.partyPeerEndpointIncludeSelf().String(),
		// 同局域网直连档（**默认 relay = 改动前行为**）：见 party_lan_direct.go。
		"party_lan_direct", s.partyLANDirect().String(),
		"party_formation_order", s.partyFormationOrder().String(),
		"party_peer_endpoints_after_roster", s.partyPeerEndpointsAfterRoster().String(),
		"party_peer_endpoint_rebroadcast", s.partyPeerEndpointRebroadcast().String(),
		// 强制组队测试触发器（`on` 时下面两个路径才有意义）：见 party_force_trigger.go。
		"party_force", s.partyForce().String(),
		"party_force_invite", s.forceInvite().String(),
		"party_force_command_path", s.options.partyForceCommandPath,
		"party_force_result_path", s.options.partyForceResultPath,
		"party_peer_row_vendor_fields", s.partyPeerRowVendorFields().String(),
		"party_peer_row_acc", s.partyPeerRowAcc().String(),
		"party_dungeon_entry_peer_endpoints", s.partyDungeonEntryPeerEndpoints().String(),
		"party_dungeon_room_slot_table", s.dungeonPartyRoomSlotTable().String(),
		// 进图那一次提交里同一个 actor 的两条 kind0 槽位表（落位名册帧 + 房间槽位表）：
		// **默认 current = 都发（今天）**；`vendor_only` 让位那条重复的名册帧。
		// 见 dungeon_party_room_slot_table.go 的 dungeonPartyEntrySlotTableEnv。
		"party_dungeon_entry_slot_table", s.partyDungeonEntrySlotTable().String(),
		// 队员"跟随者选择器转换"那一批里的 op9 名册帧：**默认 current = 照发（今天）**；
		// `skip` 只让位那条 op9（op153 照发）。见 game_enter_select_dungeon.go。
		"party_dungeon_entry_follower_frame", s.partyDungeonEntryFollowerFrame().String(),
		// 读条结束后给本人补推一次实时名册（op153 + 自己那条 op9）：**默认 off**。
		// 见 party_dungeon_loading_barrier.go 的 partyDungeonPostLoadingRosterEnv。
		"party_dungeon_post_loading_roster", partyDungeonPostLoadingRosterEnabled(),
		"party_dungeon_start_map_fatigue", s.dungeonStartMapFatigue().String(),
		// 进图读条同步的两条屏障（厂商 `all-members-through-op28-before-any-op800-op29`
		// 与 `party_dungeon_shared_loading_barrier`）；见 party_dungeon_entry_barrier.go /
		// party_dungeon_loading_barrier.go。默认都是 vendor。
		"party_dungeon_entry_barrier", s.partyDungeonEntryBarrier().String(),
		"party_dungeon_loading_barrier", s.partyDungeonLoadingBarrier().String(),
		// 副本内 op3 USER_STATE 的成员口径：**默认 self = 逐字节今天**（条数恒 1、4 字节体）；
		// `all` = 条数 = 本场副本运行时里实际在场的成员（receiver-first + slot 升序）。
		// 见 dungeon_party_user_state_members.go。
		"party_dungeon_all_member_user_state", s.partyDungeonUserStateMode(),
		// op19 的「道具家族 ↔ 背包分页」闸门：**默认 log = 只记事件不拦**；
		// `enforce` = 真拒绝 + 回厂商 4 字节失败体；`off`/`0`/`false`/`legacy` = 逐字节回退。
		// 见 item_slot_family_guard.go。
		"item_slot_family_guard", itemSlotFamilyGuardMode(),
		// op39（死亡上报）的房间证明档位；见 dungeon_death_room_proof.go。
		// 默认 strict = 改动前行为（形状/owner 不可证就拒收，只记日志）。
		"dungeon_death_room_proof", s.dungeonDeathRoomProof().String(),
		"epic_piece_book", s.epicPieceBook().String(),
		// 碎片的领取期（邮件/GM）与登录清扫开关；见 epic_piece_grant.go。
		"epic_piece_grant", s.epicPieceGrant().String(),
		// 图鉴账本**下行线**档位（class0/1149 什么时候发）；见 epic_piece_push.go。
		// 默认 login_only：拾取/领取后不再补那条零厂商先例的增量包。
		"epic_piece_push", s.epicPiecePush().String(),
		// 收集箱（COLLECT_BOX_WINDOW）总开关与初始化时机；见 collectbox_state.go。
		// off = 修复前行为（op1021~1024 只记 game-collectbox-deferred）。
		"collectbox_mode", collectboxModeValue(s.options.collectboxMode),
		"collectbox_init", collectboxInitValue(s.options.collectboxInit),
		// 收集箱登记（op1022 放宝珠）开关；见 collectbox_add.go。
		// observe_only = 修复前行为（只记 game-collectbox-mutation-observed，不应答）。
		"collectbox_mutation", collectboxMutationValue(s.options.collectboxMutation),
		"joust_event_enabled", s.joustEventEnabled(),
		// 锦标赛/大会族（dungeon 120 黄龙 / 121 青龙 / 122 黑龙 + 123-124 魂·大会）总开关与
		// 选奖应答档：select（默认）= 客户端自己发来的 2 字节选奖回 458 选中投影（7 字节）+
		// 未完成时补 9 字节 457 选奖态；state = 一键回退到改动前（任何路径都不发 458）。
		// 现场（青龙大会卡翻牌）、真机 458 handler 派发表取证见 dungeon_tournament_config.go。
		"dungeon_tournament", currentDungeonTournamentMode(),
		"dungeon_tournament_reward_ack", s.dungeonTournamentRewardAck().String(),
		// op108 在线活动目录里马战事件 2365 两行的实际判据：catalog=auto 跟随
		// joust_event_enabled，off/on 显式覆盖；includes_2365 才是真正决定
		// base_catalog_count/active_count 是 2 还是 1 的布尔。
		// 见 Service.currentSpendTimeCatalogIncludesJoustEvent。
		"joust_event_catalog", s.currentSpendTimeJoustCatalogMode(),
		"joust_event_catalog_includes_2365", s.currentSpendTimeCatalogIncludesJoustEvent(),
		"story_digest_enabled", s.storyDigestEnabled(),
		"quest_clear_ticket_scope", string(s.questClearTicketScope()),
		"main_quest_exp_percent", s.options.mainQuestExperiencePercent,
		"main_quest_exp_multiplier", s.options.mainQuestExperienceMultiplier,
		// op34（完成任务）在**结算上下文超时/取消**时也必须回包：默认 on = 修复档
		// （回与业务失败完全相同的失败体 `00 16`）；字面量 `legacy` = 逐字节回到
		// 2026-09-27 的静默 `return nil`（那 4 次「点了没反应、每次空等 ≥3 秒」）。
		// 见 quest_finish_failure_reply.go。
		"quest_finish_failure_reply", currentQuestFinishFailureReplyMode(),
		// 包日志行尾 `t=<RFC3339Nano>`：默认 on（现场测逐包间隔必需）；
		// off/legacy = 逐字节回到改动前那行（无时间戳）。见 packet_log.go。
		"packet_log_timestamps", packetLogTimestampsEnabled(),
		// 会话事件循环慢事件告警阈值（毫秒）：默认 3000 = createWriteTimeout；
		// 排查期调到 300 就能看见 <3 秒的卡顿。见 game_session_events.go。
		"event_slow_threshold_ms", currentGameSessionEventSlowThresholdMillis(),
		// op34 三段计时事件 `game-upper-finish-quest-timing`（load/tx/ack）：
		// 默认 off（每个 op34 一行，只在测量时开）。见 quest_finish_timing.go。
		"quest_finish_timing", currentQuestFinishTimingEnabled(),
		// 穿脱（op19）回包后刷新口径与强化直播（576 家族）回包档。
		// 证据与一键回退见 equip_move_refresh.go / upgrade_room.go。
		"equip_move_container_refresh", s.equipMoveContainerRefreshMode(),
		"equip_appearance_refresh", s.equipAppearanceRefreshMode(),
		// 宠物/神器穿戴（op19 落点 list 3 槽 26..29）回包后的 list 7 刷新口径：
		// 默认 slots = 只发本次变动的那一格（厂商逐槽口径）；none = 不发；
		// full = 回退到五个整容器回放。见 equip_move_refresh.go。
		"pet_move_container_refresh", s.petMoveContainerRefreshMode(),
		// 时装穿脱（mode0 档）的连点合并口径与窗口：默认 leading_trailing / 200 ms，
		// N 连点最多 2 个 mode0；off = 回到"每次穿脱都同步发"。
		// 证据与判据见 avatar_appearance_coalesce.go / equip_move_refresh.go。
		"equip_appearance_coalesce", s.equipAppearanceCoalesceMode(),
		"equip_appearance_coalesce_ms", s.equipAppearanceCoalesceWindowMillis(),
		// 尾随刷新延迟（"收手之后多久看到最终外观"）。2026-09-28 从连点窗口里拆出来，
		// 未配置 ⇒ 等于窗口值（= 改动前口径）。见 avatar_appearance_coalesce.go。
		"equip_appearance_trailing_ms", s.equipAppearanceTrailingMillis(),
		// 深渊房间（操作者清单 batch-4 item 3）的三条开关，默认都是修复档、
		// 回退值一律字面量 `legacy`：
		//   wave_rows      = hidden（发波次隐藏模板行）| legacy（不发）
		//   monster_info   = noti（START_MAP 后补 NOTI 679）| legacy（不发）
		//   start_cmd      = accept（收下 op235「深渊派对开始」）| legacy（不接管）
		// 证据、行布局与厂商 file:line 见 dungeon_hell_party_wave.go / dungeon_hell_party_start.go。
		"hell_party_wave_rows", hellPartyWaveRowsModeFromEnv().String(),
		"hell_party_monster_info", hellPartyMonsterInfoModeFromEnv().String(),
		"hell_party_start_cmd", hellPartyStartCommandModeFromEnv().String(),
		// 特殊被动对象（柱子等）的**父对象行**是否由我们自己发：
		// map_owned（默认 = 修复，客户端从 map 自建父对象、我们只发行内 [monster] 模板行）
		// | start_map（一键回退，逐字节回到改动前那条 type-9 行）。
		// 现场症状「深渊房间两根柱子」与厂商原文见 dungeon_special_object_parent_actor.go。
		"special_object_parent_actor", specialObjectParentActorModeFromEnv().String(),
		// 外观相关性闸门：默认 on = 只在真正改变 mode0 纸娃娃外观（槽 0..13 / 时装列表 17）
		// 的穿脱上补 PostActionRefreshSelectedActorAppearance；off = 回退到"每次穿脱都发"。
		// 见 equip_move_refresh.go 的 equipAppearanceSlotGateEnv。
		"equip_appearance_slot_gate", s.equipAppearanceSlotGateMode(),
		// 登录引导窗口（DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 兜底）的档位与时长：
		// 默认 repeat/15000 = 窗口在整个时长内对该族 op19 可重复命中，且只由该族
		// 形状的 op19 消费；once/5000 = 一键回到今天（任何 op19 一次性消费 + 5s）。
		// 见 equip_login_wear_window.go（窗口语义）与 equip_login_wear_family_window.go
		// （形状作用域 + noop 刷新开关）。
		"equip_login_wear_window", s.loginWearReassertWindowMode(),
		"equip_login_wear_window_ms", loginWearReassertWindowDurationMillis(),
		// noop 分支是否补一次权威刷新（真落库路径那份容器/外观/战力刷新）：
		// 默认 on = 补（客户端本地穿戴视图收敛、不再重发该族 op19）；
		// off = 逐字节回到今天（只回一条 msg_id=19）。见 equip_login_wear_family_window.go。
		"equip_login_wear_noop_refresh", equipLoginWearReassertNoopRefreshMode(),
		// 登录兜底的范围收口（2026-09-28 第三轮，现场 +46 s/+62 s/+68 s 的同族包仍真卸）：
		//   anchor   = slot_refresh（默认，锚点 = 该装备槽最近一次权威刷新，覆盖窗口外晚到的复述）
		//            | scene_ready（一键回退到今天，只认上面那个会话窗口）
		//   per_slot = on（默认，每个装备槽每会话只有一个复述处理窗口，默认 5000 ms、起点不滑动）
		//            | off（一键回退到今天：窗口内每一条都吸收）
		// 见 equip_login_wear_reassert_anchor.go。
		"equip_login_wear_reassert_anchor", s.loginWearReassertAnchor(),
		"equip_login_wear_reassert_per_slot", s.loginWearReassertPerSlot(),
		"equip_login_wear_reassert_per_slot_window_ms", loginWearReassertEchoIntervalMillis,
		// 登录装备容器缺口（2026-09-28 第五轮）：
		//   login_equipment_container  = authoritative（默认，登录引导第七个容器 = list 3 已穿戴）
		//                              | suppressed（一键回退到今天那六个容器）
		//   login_equipment_slot_refresh = on（默认，登录复述 noop 补两端点权威逐槽行）
		//                              | off（一键回退：noop 只回一条 msg_id=19）
		// 现场证据、changlog.md:9986 的 scope 与"为什么登录路径安全"见 login_equipment_container.go。
		"login_equipment_container", loginEquipmentContainerSwitch(),
		"login_equipment_slot_refresh", loginEquipmentSlotRefreshSwitch(),
		// 穿脱装备/换时装的外观刷新（sendSelectedActorMode0AppearanceRefresh）那一次
		// 调用内的只读仓储记忆：默认 reuse = 实测 6 装备读 + 2 角色读 + 1 账号读
		// → 各 1 次；off = 每次读都真的落到仓储（逐字节回到今天）。
		// 证据、调用点清单与残余不确定见 equip_mode0_repository_memo.go。
		"equip_mode0_repository_memo", s.equipMode0RepositoryMemoMode(),
		"upgrade_room_reply", s.upgradeRoomReplyMode(),
		// 技能换装背包 / 「Buff 强化」换装栏（op1428）档：默认 shape_guard = 落库 +
		// 边界投影前先按客户端 1428 入站解析器校验包体形状（形状不符 ⇒ 0 下行）；
		// project（别名 verbatim）= 原样回放（取证/回退）；before_transition_only
		// （别名 early_only）= 改动前的四条边界档；record = 只落库不下发；
		// reference_drop = 逐字节回到改动前（收下即丢）。见 skill_switch_inventory.go。
		"skill_switch_inventory", s.skillSwitchInventoryMode(),
		// op1428 **上行记录读法**：默认 v4 = 修复档（真实包体 4 字节
		// `u8 slot | u8 mode | u16 value`；现行分帧交来的 6 字节按 dual-read 去前缀，
		// 落库写 4 字节规范形）；v6（别名 legacy/today/old/6）= 逐字节回到改动前。
		// 证据与映射见 internal/modules/dnf/skillswitch/record.go。
		"skill_switch_inventory_record_body", s.skillSwitchInventoryRecordBodyLayout().String(),
		// 下行 `(key,value)` 候选对**实验**：默认 off = 关（逐字节等于今天）；
		// identity12 = 在既有合法投影边界上多发一帧 `u8 count=12 + 12×(u32,u32)`
		// 恒等对（结构候选，**未被证明**）。见 skill_switch_inventory_candidate_pairs.go。
		"skill_switch_candidate_pairs", s.skillSwitchCandidatePairsMode(),
		// **已移除**的第五轮边界修法（在 actor 世代重建之后再补发一帧 msg_id=1428）：
		// 操作者真机验收证明它无效（`_dist/buff-swap-equip-persistence.md` §15.1 该帧确实发了
		// 两次、面板仍空），发送路径与调用点已整段删除。这里回显的只是
		// `DNFBRIDGE_LOGIN_SKILL_SWITCH_LOADOUT_AFTER_COMMIT` 这个键**被设成了什么**，
		// 前缀 `removed_ignored:` / 恒定 `removed_always_off` —— **任何取值都不会发出任何字节**。
		// 见 login_skill_switch_loadout_after_commit.go。
		"login_skill_switch_loadout_after_commit", loginSkillSwitchLoadoutAfterCommitSwitch(),
		// 开盒子（op208/op1128）回包后的容器刷新口径：默认 current = 逐字节保持今天
		// 的五个整容器回放；changed_only = 只重发本次真正改变的容器。
		// 证据、残余不确定与一键回退见 box_open_container_refresh.go。
		"box_open_container_refresh", s.boxOpenContainerRefreshMode(),
		"party_dungeon_owner_mode", s.options.partyDungeonOwner.String(),
		// 账号判定口径（2026-09-25）：没有按来源 IP 的绑定通道了；
		// 这里的两个值决定"不同账号会不会看到同一批角色"。
		"shared_account_id", s.accountID(),
		"game_proactive_endpoint_when_unbound", s.options.gameProactiveEndpointWhenUnbound,
		"account_api_login_token", s.options.accountAPILoginToken,
		// 切角色（op7）时是否摘掉**上一个角色**的城镇存在感并发 NOTI 0x0006：
		// 默认 on = 修复档（同账号切角色后不再看到自己的残影，op24 名册回到 len 28/rows 1）；
		// off/0/false/no/legacy = 逐字节回到改动前。见 return_select_character.go。
		"town_copresence_evict_on_character_select", currentTownCopresenceEvictOnCharacterSelectEnabled(),
		// 城镇同屏广播 0x0016 的最后一个 u16（厂商 motionState）：默认 client =
		// 透传移动方 op35 上报的值（修复档，观者看到的移动速度与移动方本机一致）；
		// legacy/0/off/false/no = 逐字节回到恒写 currentTownInitialMovementRate。
		// 证据与厂商对照见 initial_town_location_notifications.go。
		"town_peer_motion_state", currentTownPeerMotionStateMode())
	s.startLocalLoginTokens()
	return nil
}

func gameListenAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ":" + strconv.Itoa(port)
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// Stop 关闭监听并等待连接处理退出。
func (s *Service) Stop(ctx context.Context) error {
	s.stopLocalLoginTokens()
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	cancel := s.cancel
	listeners := append([]net.Listener(nil), s.listeners...)
	packetLog := s.packetLog
	partyRelay := s.partyUDPRelay
	partyRelayRemote := s.partyUDPRelayRemote
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.listeners = nil
	s.cancel = nil
	s.packetLog = nil
	s.partyUDPRelay = nil
	s.partyUDPRelayRemote = nil
	s.channelScript = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var stopErr error
	for _, listener := range listeners {
		stopErr = errors.Join(stopErr, listener.Close())
	}
	for _, conn := range conns {
		stopErr = errors.Join(stopErr, conn.Close())
	}
	if partyRelay != nil {
		stopErr = errors.Join(stopErr, partyRelay.Close())
	}
	if partyRelayRemote != nil {
		stopErr = errors.Join(stopErr, partyRelayRemote.Close())
	}
	stopErr = errors.Join(stopErr, s.stopAccountAPI())
	stopErr = errors.Join(stopErr, packetLog.Close())
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return errors.Join(stopErr, ctx.Err())
	case <-done:
		return stopErr
	}
}

func (s *Service) listen(ctx context.Context, addr string, handler func(context.Context, net.Conn)) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, listener)
	s.mu.Unlock()
	s.wg.Add(1)
	go s.acceptLoop(ctx, listener, handler)
	return nil
}

func (s *Service) acceptLoop(ctx context.Context, listener net.Listener, handler func(context.Context, net.Conn)) {
	defer s.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logWarn("dnfbridge accept failed", "addr", listener.Addr().String(), "error", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.trackConn(conn)
			defer s.untrackConn(conn)
			handler(ctx, conn)
		}()
	}
}

func (s *Service) loadChannelAssets() (*channelcatalog.Catalog, []byte, error) {
	raw, err := osReadFile(s.options.channelInfoFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load dnf channel info: %w", err)
	}
	index, err := channelinfo.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse dnf channel info: %w", err)
	}
	catalog, err := channelcatalog.New(index, channelcatalog.Options{ServerID: s.channelServerID()})
	if err != nil {
		return nil, nil, err
	}
	script, err := s.buildChannelScript(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("build 90CN online channel script: %w", err)
	}
	return catalog, script, nil
}

func (s *Service) buildChannelScript(raw []byte) ([]byte, error) {
	return channelinfo.Build90CNOnlineScript(
		raw,
		s.channelServerID(),
		s.channelAdvertiseID(),
		dnfenum.BootstrapChannelID,
	)
}

func (s *Service) channelServerID() int {
	if s != nil {
		if s.options.channelServerID > 0 {
			return s.options.channelServerID
		}
	}
	return 1
}

func (s *Service) channelAdvertiseID() int {
	if s != nil {
		if s.options.channelAdvertiseID >= 0 {
			return s.options.channelAdvertiseID
		}
	}
	return s.channelServerID()
}

func (s *Service) channelInfoBodyMode() string {
	if s == nil {
		return channelInfoBodyLatest
	}
	return normalizeChannelInfoBodyMode(s.options.channelInfoBodyMode)
}

func (s *Service) currentCatalog() *channelcatalog.Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog
}

func (s *Service) currentChannelScript() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.channelScript...)
}

func (s *Service) openPacketLog() (*packetLogger, error) {
	if !s.options.packetLogEnabled {
		return nil, nil
	}
	return openPacketLogger(s.options.packetLogPath)
}

func (s *Service) logPacket(direction string, kind string, data []byte, fields ...any) {
	if suppressPacketLogLine(direction, kind, fields...) {
		return
	}
	s.mu.Lock()
	logger := s.packetLog
	s.mu.Unlock()
	logger.Log(direction, kind, data, fields...)
}

func (s *Service) logPacketEvent(kind string, args ...any) {
	if suppressPacketLogEvent(kind, args...) {
		return
	}
	s.mu.Lock()
	logger := s.packetLog
	s.mu.Unlock()
	logger.Event(kind, args...)
}

func suppressPacketLogLine(direction string, kind string, fields ...any) bool {
	if kind == "game-raw" {
		return true
	}
	msgID, hasMsgID := packetLogUintField(fields, "msg_id")
	typ, hasType := packetLogUintField(fields, "type")
	if hasMsgID {
		switch msgID {
		case 1276, 1516, 1518: // heartbeat / antibot / DPROTO callback noise.
			return true
		case 13:
			// op13 full container refreshes already have concise EVENT evidence.
			return direction == "SEND"
		}
	}
	if hasType {
		switch typ {
		case 35: // SET_USER_POSITION, emitted continuously while walking.
			return true
		}
	}
	return false
}

func suppressPacketLogEvent(kind string, fields ...any) bool {
	switch kind {
	case "game-town-set-user-position-captured":
		return true
	case "game-upper-check-user-connection-selected-success":
		return true
	}
	msgID, hasMsgID := packetLogUintField(fields, "msg_id")
	typ, hasType := packetLogUintField(fields, "type")
	if hasMsgID {
		switch msgID {
		case 1276, 1516, 1518:
			return true
		case 13:
			return kind == "game-upper-send-meta"
		}
	}
	if hasType && typ == 35 {
		return kind == "game-legacy-meta"
	}
	return false
}

func packetLogUintField(fields []any, name string) (uint64, bool) {
	for i := 0; i+1 < len(fields); i += 2 {
		if fmt.Sprint(fields[i]) != name {
			continue
		}
		switch v := fields[i+1].(type) {
		case uint8:
			return uint64(v), true
		case uint16:
			return uint64(v), true
		case uint32:
			return uint64(v), true
		case uint64:
			return v, true
		case uint:
			return uint64(v), true
		case int8:
			if v >= 0 {
				return uint64(v), true
			}
		case int16:
			if v >= 0 {
				return uint64(v), true
			}
		case int32:
			if v >= 0 {
				return uint64(v), true
			}
		case int64:
			if v >= 0 {
				return uint64(v), true
			}
		case int:
			if v >= 0 {
				return uint64(v), true
			}
		}
	}
	return 0, false
}

func (s *Service) trackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.conns[conn] = struct{}{}
}

func (s *Service) untrackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *Service) nextPacketConnID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packetID++
	return fmt.Sprintf("%s-%06d", prefix, s.packetID)
}

func (s *Service) aesKey() string {
	return time.Now().Format("20060102") + "000006"
}

func (s *Service) logInfo(msg string, args ...any) {
	if s.logger != nil {
		s.logger.Info(msg, redactPacketDiagnosticErrors(args)...)
	}
}

func (s *Service) logWarn(msg string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, redactPacketDiagnosticErrors(args)...)
	}
}
