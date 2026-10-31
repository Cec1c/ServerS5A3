package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentSpendTimeStageCount   = 4
	currentSpendTimeObserveTick  = 30 * time.Second
	currentSpendTimeResetHour    = 6
	currentSpendTimeWriteTimeout = 10 * time.Second
)

// currentSpendTimeJoustCatalogEnv 是 op108 在线活动目录里"要不要带马战
// （竞技大赛）事件 2365 那两行"的独立兜底开关。
//
// 现场症状：登录进城镇后客户端自动弹出韩文 BLUE/RED 竞技大赛窗。op1240/1241/
// 1242/1243 四条推送已经被 DNFBRIDGE_JOUST_EVENT_ENABLED=0 抑制，但客户端**能
// 单独从 op108 目录里的 2365 base+active 两行**把该活动判成全天开放，于是自己
// 开窗。这个开关就是那一条的服务端兜底：
//
//	auto（默认，零值同义）—— 跟随 DNFBRIDGE_JOUST_EVENT_ENABLED；
//	off                  —— 无论马战总开关如何，目录里都不出现 2365 两行；
//	on                   —— 强制出现 2365 两行（A/B 与将来重新启用马战用）。
//
// 取值归一化见 normalizeCurrentSpendTimeJoustCatalogMode，判据见
// Service.currentSpendTimeCatalogIncludesJoustEvent，启动日志见 Service.Start 的
// joust_event_catalog / joust_event_catalog_includes_2365 字段。
const currentSpendTimeJoustCatalogEnv = "DNFBRIDGE_JOUST_EVENT_CATALOG"

const (
	// currentSpendTimeJoustCatalogAuto 默认档：跟随马战总开关。
	currentSpendTimeJoustCatalogAuto = "auto"
	// currentSpendTimeJoustCatalogOff 显式不发 2365（无论马战总开关如何）。
	currentSpendTimeJoustCatalogOff = "off"
	// currentSpendTimeJoustCatalogOn 显式发 2365（无论马战总开关如何）。
	currentSpendTimeJoustCatalogOn = "on"
)

// normalizeCurrentSpendTimeJoustCatalogMode 归一化 DNFBRIDGE_JOUST_EVENT_CATALOG。
// 空值与未设置都是 auto；非法取值也回退 auto，但 bad=true，由 options 把它记进
// 启动日志（Service.Start），避免"写错了却静默跟随总开关"。
func normalizeCurrentSpendTimeJoustCatalogMode(raw string) (mode string, bad bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", currentSpendTimeJoustCatalogAuto, "default", "follow", "inherit":
		return currentSpendTimeJoustCatalogAuto, false
	case currentSpendTimeJoustCatalogOff, "0", "false", "no", "disable", "disabled", "exclude":
		return currentSpendTimeJoustCatalogOff, false
	case currentSpendTimeJoustCatalogOn, "1", "true", "yes", "enable", "enabled", "include":
		return currentSpendTimeJoustCatalogOn, false
	default:
		return currentSpendTimeJoustCatalogAuto, true
	}
}

// currentSpendTimeJoustCatalogMode 返回归一化取值，只用于启动日志与现场判读。
func (s *Service) currentSpendTimeJoustCatalogMode() string {
	if s == nil {
		return currentSpendTimeJoustCatalogAuto
	}
	switch s.options.joustEventCatalogMode {
	case currentSpendTimeJoustCatalogOff, currentSpendTimeJoustCatalogOn:
		return s.options.joustEventCatalogMode
	default:
		return currentSpendTimeJoustCatalogAuto
	}
}

// currentSpendTimeCatalogIncludesJoustEvent 是 op108 目录里"是否包含 2365"的
// 唯一判据：默认跟随马战总开关 DNFBRIDGE_JOUST_EVENT_ENABLED（Service.options.
// joustEventDisabled），DNFBRIDGE_JOUST_EVENT_CATALOG=off/on 可显式覆盖。
//
// 注意它只覆盖 2365；2347（在线时长/签到）的两行在任何取值下都逐字节不变。
func (s *Service) currentSpendTimeCatalogIncludesJoustEvent() bool {
	if s != nil && s.activityControls != nil {
		if enabled, overridden := s.activityControls.Override(uint16(currentJoustEventID)); overridden {
			return enabled
		}
	}
	switch s.currentSpendTimeJoustCatalogMode() {
	case currentSpendTimeJoustCatalogOff:
		return false
	case currentSpendTimeJoustCatalogOn:
		return true
	default:
		return s.joustEventEnabled()
	}
}

// currentTownNpcEventCatalogEnv 是 op108 在线活动目录里"要不要补上**城镇 NPC 可见性
// 事件 2395**那一对行"的开关（操作者清单第 56 条：NPC 红尾乔纳森未出现）。
//
// 现场症状：`npc/476_jonathan.npc`（红尾乔纳森，中央公园 3 区 地铁中心
// `map/town/centralpark/centralpark_metrocenter.map` (873,200)）文档里唯一的可见性
// 闸门是 `[open visible event] 2395`，而我们的 op108 目录只带 2347（+马战 2365）⇒
// 客户端从未把 2395 放进它的事件表，这只 NPC 就不会被构造出来。服务端**不投影任何
// 城镇 NPC 列表**（进城快照只有本地玩家自己一个 actor），所以这是服务端唯一能碰到的
// 那一环。
//
//	DNFBRIDGE_TOWN_NPC_EVENT_CATALOG
//	  - 默认（空）/其它任何值 = **今天的行为**：op108 体逐字节不变，零新增行；
//	  - on / 1 / true / yes / enable / enabled = 追加 2395 的通用 base+active 两行
//	    （与 2365 同构：不装点击动作、无时间窗）。
//
// 一键回退：删掉这个环境变量（或不带值）重启服务端即可逐字节回到今天。
// 全档取证：`[open visible event]` 在真档 1,057,625 个条目里共 203 处，**全部在
// `.npc` 文档**；取值为 2395 的只有 `npc/476_jonathan.npc` 一个 ⇒ 打开这一档在 PVF
// 侧只可能影响这一只 NPC（其它 NPC 的闸门是 225/2343/2365/… 等各自不同的事件号）。
const currentTownNpcEventCatalogEnv = "DNFBRIDGE_TOWN_NPC_EVENT_CATALOG"

// townNpcEventCatalogEnabled 只有显式打开值才算开（与工程其它实验阀同一口径：
// 打错字 = 保持今天）。
func townNpcEventCatalogEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentTownNpcEventCatalogEnv))) {
	case "on", "1", "true", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}

// currentSpendTimeCatalogIncludesTownNpcEvent 是 op108 目录里"是否追加 2395"的唯一判据。
// 它只覆盖 2395；2347 与 2365 的字节在任何取值下都不变。
func (s *Service) currentSpendTimeCatalogIncludesTownNpcEvent() bool {
	if s == nil {
		return false
	}
	if s.activityControls != nil {
		if enabled, overridden := s.activityControls.Override(uint16(currentTownNpcVisibleEventID)); overridden {
			return enabled
		}
	}
	return townNpcEventCatalogEnabled()
}

// 复用2347的账户在线时钟和历史账本。存在已核实的PCRoom资源时投影228
// 原生四加四面板；无该资源的旧配套继续使用2347的小进度条。
type currentSpendTimeRuntimeCatalog struct {
	nativePCRoom      *dnfonlineevent.PCRoomTimePointCatalog
	cumulative        *dnfonlineevent.CumulativeDefinition
	definition        dnfonlineevent.Definition
	rewardItemIDs     []uint32
	totalStageSeconds uint32
}

// currentSpendTimeClockState owns one selected-character observation cursor.
// Its timer callback is routed through the per-session event loop and also
// carries characterGeneration, so a stale callback cannot credit a replacement
// character or write to its socket.
type currentSpendTimeClockState struct {
	mu                             sync.Mutex
	eventInfoSent                  bool
	characterID                    uint16
	characterGeneration            uint64
	initializedCharacterGeneration uint64
	accountID                      string
	anchor                         time.Time
	generation                     uint64
	timerName                      string
	onlineSeconds                  uint64
	completedStages                uint32
	catalog                        *currentSpendTimeRuntimeCatalog
}

type currentSpendTimeSettlementResult struct {
	changed     bool
	rewardItems []dnfonlineevent.CommittedItem
	claimErr    error
}

func buildCurrentSpendTimeRuntimeCatalog(catalog *dnfonlineevent.AttendancePVFCatalog) (*currentSpendTimeRuntimeCatalog, error) {
	if catalog == nil {
		return nil, fmt.Errorf("current spend-time attendance catalog is nil")
	}
	snapshot := catalog.Snapshot()
	if len(snapshot.ProcessDurationsSeconds) != currentSpendTimeStageCount ||
		len(snapshot.RewardItems) != currentSpendTimeStageCount {
		return nil, fmt.Errorf(
			"current spend-time PVF shape durations=%d rewards=%d want=%d/%d",
			len(snapshot.ProcessDurationsSeconds),
			len(snapshot.RewardItems),
			currentSpendTimeStageCount,
			currentSpendTimeStageCount,
		)
	}

	definition := dnfonlineevent.Definition{
		ID:     strconv.FormatUint(uint64(currentSpendTimeEventID), 10),
		Stages: make([]dnfonlineevent.Stage, 0, currentSpendTimeStageCount),
	}
	rewardItemIDs := make([]uint32, 0, currentSpendTimeStageCount)
	var cumulative uint64
	for index := 0; index < currentSpendTimeStageCount; index++ {
		duration := snapshot.ProcessDurationsSeconds[index]
		reward := snapshot.RewardItems[index]
		if duration <= 0 || reward.ItemID <= 0 || reward.ItemID > math.MaxUint32 || reward.Count <= 0 {
			return nil, fmt.Errorf(
				"current spend-time PVF stage=%d duration=%d item=%d count=%d",
				index,
				duration,
				reward.ItemID,
				reward.Count,
			)
		}
		cumulative += uint64(duration)
		if cumulative > math.MaxUint32 {
			return nil, fmt.Errorf("current spend-time cumulative seconds=%d overflows u32", cumulative)
		}
		definition.Stages = append(definition.Stages, dnfonlineevent.Stage{
			ID:              fmt.Sprintf("stage-%d", index+1),
			RequiredSeconds: cumulative,
			Items: []dnfonlineevent.ItemReward{{
				ItemID: reward.ItemID,
				Count:  reward.Count,
			}},
		})
		rewardItemIDs = append(rewardItemIDs, uint32(reward.ItemID))
	}
	cumulativeDefinition, err := dnfonlineevent.AttendanceCumulativeDefinition(catalog, definition)
	if err != nil {
		return nil, fmt.Errorf("validate cumulative attendance definition: %w", err)
	}
	definition = cumulativeDefinition.Daily
	if err := definition.Validate(); err != nil {
		return nil, fmt.Errorf("validate current spend-time definition: %w", err)
	}
	return &currentSpendTimeRuntimeCatalog{
		cumulative:        &cumulativeDefinition,
		definition:        definition,
		rewardItemIDs:     append([]uint32(nil), rewardItemIDs...),
		totalStageSeconds: uint32(cumulative),
	}, nil
}

// currentSpendTimeRuntimeCatalogIfAvailable lazily projects the same archive
// already loaded by the startup PVF stages. Hand-assembled unit-test services
// without an archive keep the feature absent unless the test injects a proved
// projection; production Start always establishes the archive first.
func (s *Service) currentSpendTimeRuntimeCatalogIfAvailable(ctx context.Context) (*currentSpendTimeRuntimeCatalog, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}

	s.spendTimeCatalogMu.Lock()
	defer s.spendTimeCatalogMu.Unlock()
	if s.spendTimeCatalogLoadErr != nil {
		return nil, true, s.spendTimeCatalogLoadErr
	}
	if s.spendTimeCatalogState != nil {
		return s.spendTimeCatalogState, true, nil
	}

	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return nil, false, nil
	}
	var state *currentSpendTimeRuntimeCatalog
	rawCatalog, err := dnfonlineevent.LoadAttendancePVFCatalog(ctx, archive)
	if err == nil {
		state, err = buildCurrentSpendTimeRuntimeCatalog(rawCatalog)
	}
	if err == nil {
		// 缺少这套独立资源的旧版PVF继续使用2347；存在但损坏的配置不能静默降级。
		if _, exists := archive.FindFile(dnfonlineevent.PCRoomTimePointPVFPath); exists {
			state.nativePCRoom, err = dnfonlineevent.LoadPCRoomTimePointCatalog(ctx, archive)
		}
	}
	if err != nil {
		// An interrupted session load is not an immutable PVF failure.
		// Let a fresh session retry while retaining real resource errors.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.spendTimeCatalogLoadErr = err
		}
		return nil, true, err
	}
	s.spendTimeCatalogState = state
	s.logPacketEvent("dnf-spend-time-catalog-loaded",
		"event_id", currentSpendTimeEventID,
		"reward_count", len(state.rewardItemIDs),
		"total_stage_seconds", state.totalStageSeconds,
		"source", dnfonlineevent.AttendanceEventPVFPath)
	return state, true, nil
}

func (s *Service) currentSpendTimeOwner() (*dnfonlineevent.Owner, error) {
	repositories, ok := s.repositoryGroup()
	if !ok {
		return nil, dnfonlineevent.ErrOwnerUnavailable
	}
	return dnfonlineevent.NewOwner(repositories)
}

// 先发送活动目录和所属状态，再启动服务端时钟。原生228使用561，旧2347
// 使用1206；其它活动快照及旧自动发奖的库存更新均在该状态之后发送。
func (s *Service) sendCurrentSpendTimeInitialStateOnce(
	session *gameSession,
	source string,
) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	characterID := session.selectedCharacterID
	characterGeneration := session.characterGeneration

	session.spendTime.mu.Lock()
	alreadyInitialized := session.spendTime.characterID == characterID &&
		session.spendTime.initializedCharacterGeneration == characterGeneration &&
		session.spendTime.catalog != nil && !session.spendTime.anchor.IsZero()
	session.spendTime.mu.Unlock()
	if alreadyInitialized {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(ctx)
	if err != nil {
		return fmt.Errorf("load current spend-time catalog: %w", err)
	}
	if !available || catalog == nil {
		s.logGameEvent(session, "game-spend-time-initialization-deferred",
			"source", source,
			"char_id", characterID,
			"reason", "runtime_pvf_archive_not_loaded")
		return nil
	}
	if !s.currentSpendTimeEventEnabled() {
		sendDescriptor, ready := s.beginCurrentSpendTimeEventInfo(session)
		if !ready {
			return fmt.Errorf("activity descriptor is being sent by another session")
		}
		sent, sendErr := s.sendCurrentSpendTimeProtocolState(session, catalog, 0, 0, sendDescriptor)
		if sendDescriptor {
			s.finishCurrentSpendTimeEventInfo(session, sent)
		}
		return sendErr
	}
	owner, err := s.currentSpendTimeOwner()
	if err != nil {
		return fmt.Errorf("create current spend-time owner: %w", err)
	}
	now := s.gameplayNow()
	accountID := s.accountIDForSession(session)
	characterIDText := strconv.Itoa(int(characterID))

	session.spendTime.mu.Lock()
	defer session.spendTime.mu.Unlock()
	if session.spendTime.characterID == characterID &&
		session.spendTime.initializedCharacterGeneration == characterGeneration &&
		session.spendTime.catalog != nil && !session.spendTime.anchor.IsZero() {
		return nil
	}
	if session.spendTime.characterID != 0 && session.spendTime.characterID != characterID {
		return fmt.Errorf("current spend-time clock still owns character %d", session.spendTime.characterID)
	}
	snapshot, err := owner.Status(ctx, dnfonlineevent.StatusCommand{
		AccountID:   accountID,
		CharacterID: characterIDText,
		Definition:  catalog.definition,
		ObservedAt:  now,
	})
	if err != nil {
		return fmt.Errorf("load current spend-time status: %w", err)
	}
	snapshot, rewardItems, claimErr := s.claimCurrentSpendTimeUnlockedStages(
		ctx,
		owner,
		accountID,
		characterIDText,
		catalog,
		snapshot,
		now,
	)
	if claimErr != nil {
		s.logGameEvent(session, "game-spend-time-initial-auto-award-deferred",
			"source", source,
			"char_id", characterID,
			"online_seconds", snapshot.OnlineSeconds,
			"error", claimErr)
	}
	completedStages := currentSpendTimeClaimedStages(catalog.definition, snapshot)
	sendDescriptor, descriptorReady := s.beginCurrentSpendTimeEventInfo(session)
	if !descriptorReady {
		return fmt.Errorf("current spend-time first descriptor is being sent by another session for client pid=%d", session.clientPID)
	}
	descriptorSent, sendErr := s.sendCurrentSpendTimeProtocolState(
		session,
		catalog,
		snapshot.OnlineSeconds,
		completedStages,
		sendDescriptor,
	)
	if sendDescriptor {
		// 目录写入成功即记录去重，所属状态或后续活动快照失败可另行重试。
		s.finishCurrentSpendTimeEventInfo(session, descriptorSent)
	}
	if sendErr != nil {
		return sendErr
	}
	if !descriptorSent {
		s.logGameEvent(session, "game-spend-time-descriptor-skipped",
			"source", source,
			"char_id", characterID,
			"client_pid", session.clientPID,
			"already_sent", session.spendTime.eventInfoSent,
			"reason", "unchanged_service_day_and_initial_catalog_already_sent")
	}
	if err := s.sendCurrentSpendTimeRewardItemUpdates(session, rewardItems, source+"_initial_auto_award"); err != nil {
		return err
	}

	session.spendTime.characterID = characterID
	session.spendTime.characterGeneration = characterGeneration
	session.spendTime.initializedCharacterGeneration = characterGeneration
	session.spendTime.accountID = accountID
	// Keep the exact observation start. onlineevent.Owner conservatively
	// ceil(from)/floor(to); truncating here would manufacture up to one second.
	session.spendTime.anchor = now
	session.spendTime.generation++
	session.spendTime.onlineSeconds = snapshot.OnlineSeconds
	session.spendTime.completedStages = completedStages
	session.spendTime.catalog = catalog
	if err := s.armCurrentSpendTimeTickLocked(session); err != nil {
		session.spendTime.characterID = 0
		session.spendTime.characterGeneration = 0
		session.spendTime.initializedCharacterGeneration = 0
		session.spendTime.accountID = ""
		session.spendTime.anchor = time.Time{}
		session.spendTime.timerName = ""
		session.spendTime.onlineSeconds = 0
		session.spendTime.completedStages = 0
		session.spendTime.catalog = nil
		return err
	}
	displayEventID, stateMessageID := currentSpendTimeEventID, currentSpendTimeProgressMsgID
	if catalog.nativePCRoom != nil {
		displayEventID, stateMessageID = uint32(currentPCRoomTimePointEventID), currentPCRoomTimePointStateID
	}
	s.logGameEvent(session, "game-spend-time-initial-state-sent",
		"source", source,
		"char_id", characterID,
		"event_id", displayEventID,
		"state_msg_id", stateMessageID,
		"online_seconds", snapshot.OnlineSeconds,
		"completed_stages", completedStages,
		"reward_item_updates", len(rewardItems),
		// op108 目录里 2365（竞技大赛/马战）两行的实际下发判据：off 档下
		// joust_catalog_includes_2365=false 且 base_catalog_count/active_count 都是 1。
		"joust_catalog", s.currentSpendTimeJoustCatalogMode(),
		"joust_catalog_includes_2365", s.currentSpendTimeCatalogIncludesJoustEvent(),
		"joust_catalog_event_id", currentJoustEventID,
		// 红尾乔纳森（NPC 476）的 `[open visible event] 2395` 那一对行：默认不发。
		"town_npc_event_catalog", currentTownNpcEventCatalogEnv,
		"town_npc_event_catalog_includes_2395", s.currentSpendTimeCatalogIncludesTownNpcEvent(),
		"anton_channel_event_id", currentAntonChannelEventID,
		"anton_channel_event_in_first_descriptor", true,
		"sequence", "活动目录后立即发送所属活动状态，再发送其它活动快照和已提交库存更新")
	return nil
}

func (s *Service) sendCurrentSpendTimeProtocolState(
	session *gameSession,
	catalog *currentSpendTimeRuntimeCatalog,
	onlineSeconds uint64,
	completedStages uint32,
	includeFirstProcessDescriptor bool,
) (bool, error) {
	if s == nil || session == nil || catalog == nil {
		return false, nil
	}
	var eventInfoTransport []byte
	fiendWarAvailable := false
	dayDungeonEvent := s.currentDayDungeonEvent()
	includeFirstProcessDescriptor = includeFirstProcessDescriptor || dayDungeonEvent != session.activityCatalogDayEvent
	joustEventEnabled := s.joustEventEnabled()
	includeJoust := s.currentSpendTimeCatalogIncludesJoustEvent()
	joustCatalogAvailable := false
	if joustEventEnabled || (includeFirstProcessDescriptor && includeJoust) {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		_, joustCatalogAvailable = s.currentJoustCatalogForPublishing(
			ctx,
			session,
			"op108_catalog_op1240_op1243",
		)
		cancel()
	}
	if includeJoust && !joustCatalogAvailable {
		includeJoust = false
	}
	if includeFirstProcessDescriptor {
		// 2365 的两行跟随 DNFBRIDGE_JOUST_EVENT_ENABLED（可被
		// DNFBRIDGE_JOUST_EVENT_CATALOG 覆盖）。此前它们是无条件的，客户端因此
		// 单独从本目录就能把竞技大赛判成全天开放并自动开窗。
		// 2395（红尾乔纳森的可见性闸门）默认**不发**，只有
		// DNFBRIDGE_TOWN_NPC_EVENT_CATALOG 显式打开才追加两行。
		// 客户端完整替换活动目录；更新后紧接发送本在线活动所属状态。
		season, weeklyErr := s.currentWeeklySeason(session)
		includeWeekly := weeklyErr == nil && season.Week(s.gameplayNow()) >= 0
		// Optional image town-NPC rows are resolved independently. The operator
		// policy is read first: with every optional switch off there is nothing
		// to validate and the resource archive is not read at all. When any
		// switch is on, a missing, unreadable or mismatched entry is skipped
		// with a structured diagnostic; it must never abort this catalog.
		// Aborting here previously dropped the whole op108 directory and, with
		// it, the op1206 pair that owns the existing 2347/签到 HUD.
		var enabledTownActivities []uint16
		for _, activity := range currentImageTownActivities() {
			if s.options.currentClientVersion == "official-24.9" && s.activityControls != nil && s.activityControls.Enabled(activity.id) {
				enabledTownActivities = append(enabledTownActivities, activity.id)
			}
		}
		var townArchive dnfpvf.Source
		if len(enabledTownActivities) > 0 {
			s.initialEquipmentMu.Lock()
			archive, loadErr := s.initialEquipmentArchiveLocked()
			s.initialEquipmentMu.Unlock()
			if loadErr == nil {
				townArchive = archive
			}
			// loadErr != nil leaves townArchive nil: the archive itself is
			// unusable, so every enabled optional entry is reported as
			// skipped and the legacy catalog is kept.
		}
		townActivities := s.logCurrentImageTownActivitySkips(townArchive, enabledTownActivities, session, "current_spend_time_op108_catalog")
		fiendWarAvailable = s.currentFiendWarChannelAvailable(session)
		eventInfoBody, err := buildCurrentActivityEventInfoBodyWithRuntimeActivitiesAndMarble(
			catalog.rewardItemIDs,
			catalog.totalStageSeconds,
			includeJoust,
			s.currentSpendTimeCatalogIncludesTownNpcEvent(),
			true,
			s.currentAntonModeSelectionAvailable(session),
			s.currentSpendTimeEventEnabled(),
			dayDungeonEvent,
			s.currentLukeNormalChannelAvailable(session),
			s.currentMonthlyAttendanceEnabled(),
			includeWeekly,
			s.currentOrdinaryAttendanceEnabled(),
			s.currentEpicRoadActivityAvailable(),
			townActivities,
			fiendWarAvailable,
			catalog.nativePCRoom != nil,
			true, // Keep the native Seria luck entry alongside online rewards.
			s.currentAttendanceMarbleAvailable(),
		)
		if err != nil {
			return false, err
		}
		eventInfoBody, err = s.withPrivateFestivalCalendar(eventInfoBody)
		if err != nil {
			return false, fmt.Errorf("festival calendar: %w", err)
		}
		eventInfoTransport, err = zlibCompress(eventInfoBody)
		if err != nil {
			return false, fmt.Errorf("compress current spend-time op108: %w", err)
		}
	}
	progressBody, err := buildCurrentSpendTimeProgressBody(onlineSeconds, completedStages)
	if err != nil {
		return false, err
	}
	descriptorSent := false
	if includeFirstProcessDescriptor {
		if err := s.sendCurrentProtectedClass0Packet(
			session,
			currentSpendTimeEventInfoMsgID,
			eventInfoTransport,
			currentSpendTimeEventInfoCodec,
			"current_op108_spend_time_catalog",
		); err != nil {
			return false, err
		}
		descriptorSent = true
		s.logGameEvent(session, "超时空频道开放状态下发", "event_id", currentFiendWarChannelEventID, "available", fiendWarAvailable)
		session.activityCatalogDayEvent = dayDungeonEvent
		if dayDungeonEvent != 0 {
			s.logGameEvent(session, "game-day-dungeon-catalog-sent", "event_id", dayDungeonEvent, "char_id", session.selectedCharacterID, "reset", "06:00_UTC+8", "reason", "full_catalog_replaces_previous_weekday")
		}
	}
	// 两套在线活动的UI及协议不同，不能用旧1206替代原生展开面板的561。
	if s.currentSpendTimeEventEnabled() && catalog.nativePCRoom != nil {
		if err := s.sendCurrentPCRoomTimePoint(session, catalog, 0, -1); err != nil {
			return descriptorSent, err
		}
	} else if s.currentSpendTimeEventEnabled() {
		if err := s.sendGameUpperRawClass(session, currentSpendTimeProgressMsgID, progressBody, 0); err != nil {
			return descriptorSent, err
		}
	}
	// 保留108与所属在线活动状态紧邻，然后同步周常逐图开放表。
	// 同一进程省略108时，换角仍需恢复855；写失败重试时发送整组状态。
	if err := s.sendCurrentDayDungeonSchedule(session, dayDungeonEvent, "活动目录与所属状态之后"); err != nil {
		return descriptorSent, err
	}
	// Current NoPack creates the joust owners while consuming op108. Its owner
	// 609 open gate then reads the state set by the exact three-byte class-0
	// op1240 handler before it can use the class-0 op1241 roster and op1242
	// support pool. Repeat the opening snapshot for
	// later character selections in the same client process so a failed or
	// reconnected session can recover even though op108 is process-lifetime only.
	if joustCatalogAvailable {
		if err := s.sendCurrentJoustOpeningState(session); err != nil {
			return descriptorSent, err
		}
		if err := s.sendCurrentJoustOpeningRoster(session); err != nil {
			return descriptorSent, err
		}
	}
	if err := s.armCurrentDayDungeonCatalogRefresh(session); err != nil {
		return descriptorSent, err
	}
	if err := s.recoverQuestMaterials(session); err != nil {
		s.logGameEvent(session, "game-quest-material-bootstrap-deferred", "error", err)
	}
	if err := s.armQuestMaterialRecovery(session); err != nil {
		s.logGameEvent(session, "game-quest-material-retry-deferred", "error", err)
	}
	if s.currentMonthlyAttendanceEnabled() {
		if err := s.sendCurrentMonthlyAttendance(session, false); err != nil {
			s.logGameEvent(session, "game-monthly-attendance-bootstrap-deferred", "error", err)
		}
		if err := s.armCurrentMonthlyAttendanceTick(session); err != nil {
			s.logGameEvent(session, "game-monthly-attendance-retry-deferred", "error", err)
		}
	}
	if s.currentWeeklyAttendanceEnabled() {
		if err := s.sendCurrentWeeklyAttendance(session, false, false); err != nil {
			s.logGameEvent(session, "game-weekly-attendance-bootstrap-deferred", "error", err)
		}
		if err := s.armCurrentWeeklyAttendanceTick(session); err != nil {
			s.logGameEvent(session, "game-weekly-attendance-retry-deferred", "error", err)
		}
	}
	if s.currentOrdinaryAttendanceEnabled() {
		if err := s.sendCurrentOrdinaryAttendance(session); err != nil {
			s.logGameEvent(session, "game-ordinary-attendance-bootstrap-deferred", "error", err)
		}
		if err := s.armCurrentOrdinaryAttendanceTick(session); err != nil {
			s.logGameEvent(session, "game-ordinary-attendance-retry-deferred", "error", err)
		}
	}
	if s.currentEpicBenefitsEnabled() {
		if err := s.sendCurrentEpicState(session); err != nil {
			s.logGameEvent(session, "game-epic-bootstrap-deferred", "error", err)
		}
	}
	if s.currentSemesterCumulativeEnabled() {
		if identity, ok := s.boundGameSessionCharacterSnapshot(session); ok {
			if err := s.sendCurrentSemesterCumulative(session, identity); err != nil {
				s.logGameEvent(session, "game-semester216-bootstrap-deferred", "error", err)
			}
		}
		if err := s.armCurrentSemesterCumulativeTick(session); err != nil {
			return descriptorSent, err
		}
	}
	return descriptorSent, nil
}

func currentSpendTimeReachedStages(definition dnfonlineevent.Definition, onlineSeconds uint64) uint32 {
	var completed uint32
	for _, stage := range definition.Stages {
		if onlineSeconds < stage.RequiredSeconds {
			break
		}
		completed++
	}
	return completed
}

func currentSpendTimeClaimedStages(definition dnfonlineevent.Definition, snapshot dnfonlineevent.Snapshot) uint32 {
	claimed := make(map[string]struct{}, len(snapshot.ClaimedStageIDs))
	for _, stageID := range snapshot.ClaimedStageIDs {
		claimed[stageID] = struct{}{}
	}
	var completed uint32
	for _, stage := range definition.Stages {
		if _, ok := claimed[stage.ID]; !ok {
			break
		}
		completed++
	}
	return completed
}

func (s *Service) claimCurrentSpendTimeUnlockedStages(
	ctx context.Context,
	owner *dnfonlineevent.Owner,
	accountID string,
	characterID string,
	catalog *currentSpendTimeRuntimeCatalog,
	snapshot dnfonlineevent.Snapshot,
	claimedAt time.Time,
) (dnfonlineevent.Snapshot, []dnfonlineevent.CommittedItem, error) {
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	if s == nil || owner == nil || accountID == "" || characterID == "" || catalog == nil {
		return snapshot, nil, dnfonlineevent.ErrOwnerUnavailable
	}
	claimed := make(map[string]struct{}, len(snapshot.ClaimedStageIDs))
	for _, stageID := range snapshot.ClaimedStageIDs {
		claimed[stageID] = struct{}{}
	}
	itemCatalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return snapshot, nil, err
	}
	if catalog.nativePCRoom != nil {
		_, err := s.settleCurrentPCRoomTimePoint(ctx, accountID, characterID, catalog, claimedAt, 0, -1, nil)
		return snapshot, nil, err
	}
	items := make([]dnfonlineevent.CommittedItem, 0, currentSpendTimeStageCount)
	for _, stage := range catalog.definition.Stages {
		if snapshot.OnlineSeconds < stage.RequiredSeconds {
			break
		}
		if _, alreadyClaimed := claimed[stage.ID]; alreadyClaimed {
			continue
		}
		claim, claimErr := owner.Claim(ctx, dnfonlineevent.ClaimCommand{
			AccountID:   accountID,
			CharacterID: characterID,
			Definition:  catalog.definition,
			StageID:     stage.ID,
			ClaimedAt:   claimedAt,
			Allocate:    currentSpendTimeItemAllocator(itemCatalog, claimedAt, stage.ID),
		})
		if claimErr != nil {
			return snapshot, items, claimErr
		}
		snapshot = claim.PostSnapshot
		claimed[stage.ID] = struct{}{}
		items = append(items, currentSpendTimeClaimItemsForCharacter(claim, characterID)...)
	}
	if catalog.cumulative != nil {
		status, statusErr := owner.CumulativeStatus(ctx, dnfonlineevent.CumulativeStatusCommand{
			AccountID: accountID, CharacterID: characterID, Definition: *catalog.cumulative, ObservedAt: claimedAt,
		})
		if statusErr != nil {
			return snapshot, items, statusErr
		}
		for _, stage := range catalog.cumulative.Stages {
			if uint64(len(status.QualifiedDates)) < uint64(stage.RequiredDays) {
				break
			}
			claim, claimErr := owner.ClaimCumulative(ctx, dnfonlineevent.CumulativeClaimCommand{
				AccountID: accountID, CharacterID: characterID, Definition: *catalog.cumulative,
				StageID: stage.ID, ClaimedAt: claimedAt,
				Allocate: currentSpendTimeItemAllocator(itemCatalog, claimedAt, stage.ID),
			})
			if claimErr != nil {
				return snapshot, items, claimErr
			}
			items = append(items, currentSpendTimeClaimItemsForCharacter(claim, characterID)...)
		}
	}
	return snapshot, items, nil
}

func currentSpendTimeClaimItemsForCharacter(
	claim dnfonlineevent.ClaimResult,
	characterID string,
) []dnfonlineevent.CommittedItem {
	if claim.Replayed || claim.CharacterID != characterID {
		return nil
	}
	items := make([]dnfonlineevent.CommittedItem, len(claim.Items))
	for index, item := range claim.Items {
		items[index] = item
		items[index].RawEntry = append([]byte(nil), item.RawEntry...)
	}
	return items
}

func currentSpendTimeItemAllocator(
	catalog *pvfDungeonDropCatalog,
	grantedAt time.Time,
	stageID string,
) dnfonlineevent.ItemAllocator {
	return func(record *dnfrepo.InventoryRecord, reward dnfonlineevent.ItemReward) (dnfonlineevent.CommittedItem, error) {
		if catalog == nil || record == nil || reward.ItemID <= 0 || reward.ItemID > math.MaxUint32 ||
			reward.Count <= 0 || reward.Count > math.MaxUint32 {
			return dnfonlineevent.CommittedItem{}, errDungeonPickupItemInvalid
		}
		definition, err := catalog.ResolveItem(uint32(reward.ItemID))
		if err != nil {
			return dnfonlineevent.CommittedItem{}, err
		}
		definition, err = currentPVFItemDefinitionForGrantAt(definition, grantedAt.UTC())
		if err != nil {
			return dnfonlineevent.CommittedItem{}, err
		}
		slot, err := addCurrentDungeonPickupToInventory(record, definition, uint32(reward.Count))
		if err != nil {
			return dnfonlineevent.CommittedItem{}, err
		}
		key := currentDungeonPickupMainSlotKey(int16(slot))
		stack, found := record.Slots[key]
		if !found || stack.ItemID != reward.ItemID || stack.Count < reward.Count {
			return dnfonlineevent.CommittedItem{}, errDungeonPickupItemInvalid
		}
		entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, int16(slot), stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		if stack.Extra == nil {
			stack.Extra = make(map[string]string, 5)
		}
		if strings.TrimSpace(stack.Extra["source"]) == "" {
			stack.Extra["source"] = "online_event_spend_time"
		}
		stack.Extra["last_grant_source"] = "online_event_spend_time"
		stack.Extra["last_grant_event_id"] = strconv.FormatUint(uint64(currentSpendTimeEventID), 10)
		stack.Extra["last_grant_stage_id"] = stageID
		record.Slots[key] = stack
		return dnfonlineevent.CommittedItem{
			SlotKey: key, SlotIndex: slot, ItemID: reward.ItemID,
			Delta: reward.Count, PostCount: stack.Count,
			RawEntry: append([]byte(nil), entry.data[:]...),
		}, nil
	}
}

func (s *Service) sendCurrentSpendTimeRewardItemUpdates(
	session *gameSession,
	items []dnfonlineevent.CommittedItem,
	source string,
) error {
	if len(items) == 0 {
		return nil
	}
	entries := make([]currentItemListEntry, 0, len(items))
	for _, item := range items {
		if item.ItemID <= 0 || item.ItemID > math.MaxUint32 || item.PostCount < item.Delta ||
			len(item.RawEntry) != currentItemListEntryWireSize ||
			binary.LittleEndian.Uint16(item.RawEntry[:2]) != item.SlotIndex ||
			binary.LittleEndian.Uint32(item.RawEntry[2:6]) != uint32(item.ItemID) {
			return fmt.Errorf("current spend-time committed item has invalid wire receipt: %+v", item)
		}
		var entry currentItemListEntry
		copy(entry.data[:], item.RawEntry)
		entries = append(entries, entry)
	}
	body := buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, entries)
	s.logGameEvent(session, "game-spend-time-reward-item-update-send",
		"source", source,
		"event_id", currentSpendTimeEventID,
		"entry_count", len(entries),
		"body_len", len(body),
		"body_source", "online_event_atomic_claim_receipts_op14_raw77")
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		body,
		0,
	)
}

func (s *Service) armCurrentSpendTimeTickLocked(session *gameSession) error {
	if s == nil || session == nil || session.spendTime.characterID == 0 || session.spendTime.catalog == nil {
		return nil
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	characterID, characterGeneration, err := gameSessionCharacterEventIdentity(session, session.spendTime.characterID)
	if err != nil {
		return err
	}
	timerName := fmt.Sprintf("dnf-spend-time:%s:%d", session.connID, characterID)
	generation := session.spendTime.generation
	session.spendTime.timerName = timerName
	return queue.ScheduleAfter(timerName, currentSpendTimeObserveTick, func(time.Time) {
		err := s.postGameSessionCharacterEvent(
			session,
			"spend-time-observation-tick",
			characterID,
			characterGeneration,
			func() error {
				s.runCurrentSpendTimeTick(session, generation)
				return nil
			},
		)
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logPacketEvent("game-session-event-submit-failed",
				"conn_id", session.connID,
				"source", "spend-time-observation-tick",
				"char_id", characterID,
				"character_generation", characterGeneration,
				"error", err)
		}
	})
}

func (s *Service) runCurrentSpendTimeTick(session *gameSession, generation uint64) {
	if s == nil || session == nil {
		return
	}
	now := s.gameplayNow()
	session.spendTime.mu.Lock()
	defer session.spendTime.mu.Unlock()
	if session.spendTime.characterID == 0 || session.spendTime.catalog == nil ||
		session.spendTime.generation != generation {
		return
	}
	settlement, err := s.settleCurrentSpendTimeClockLocked(session, now)
	if err != nil {
		s.logGameEvent(session, "game-spend-time-observation-deferred",
			"char_id", session.spendTime.characterID,
			"generation", generation,
			"error", err)
	} else if settlement.changed && session.spendTime.catalog.nativePCRoom != nil {
		if sendErr := s.sendCurrentPCRoomTimePoint(session, session.spendTime.catalog, 0, -1); sendErr != nil {
			s.logGameEvent(session, "原生在线奖励状态重试", "error", sendErr)
		}
	} else if settlement.changed && spendTimeProgressShouldSend(session.connID, session.spendTime.characterID, session.spendTime.onlineSeconds, session.spendTime.completedStages) {
		body, buildErr := buildCurrentSpendTimeProgressBody(
			session.spendTime.onlineSeconds,
			session.spendTime.completedStages,
		)
		if buildErr != nil {
			s.logGameEvent(session, "game-spend-time-progress-build-failed", "error", buildErr)
		} else if sendErr := s.sendGameUpperRawClass(session, currentSpendTimeProgressMsgID, body, 0); sendErr != nil {
			s.logGameEvent(session, "game-spend-time-progress-send-failed", "error", sendErr)
		}
	}
	// Inventory deltas belong to committed claims, independently of the HUD's
	// resend cadence or a later service-day observation failure.
	if sendErr := s.sendCurrentSpendTimeRewardItemUpdates(
		session,
		settlement.rewardItems,
		"online_observation_tick",
	); sendErr != nil {
		s.logGameEvent(session, "game-spend-time-reward-item-update-failed", "error", sendErr)
	}
	if settlement.claimErr != nil {
		s.logGameEvent(session, "game-spend-time-auto-award-deferred",
			"char_id", session.spendTime.characterID,
			"online_seconds", session.spendTime.onlineSeconds,
			"error", settlement.claimErr)
	}
	if err := s.armCurrentSpendTimeTickLocked(session); err != nil {
		s.logGameEvent(session, "game-spend-time-observation-rearm-deferred",
			"char_id", session.spendTime.characterID,
			"generation", generation,
			"error", err)
	}
}

func (s *Service) settleCurrentSpendTimeClockLocked(
	session *gameSession,
	now time.Time,
) (currentSpendTimeSettlementResult, error) {
	if session == nil || session.spendTime.characterID == 0 || session.spendTime.catalog == nil ||
		session.spendTime.anchor.IsZero() {
		return currentSpendTimeSettlementResult{}, nil
	}
	intervalTo := now.Truncate(time.Second)
	if !session.spendTime.anchor.Before(intervalTo) {
		return currentSpendTimeSettlementResult{}, nil
	}
	owner, err := s.currentSpendTimeOwner()
	if err != nil {
		return currentSpendTimeSettlementResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	definition := session.spendTime.catalog.definition
	characterID := strconv.Itoa(int(session.spendTime.characterID))
	cursor := session.spendTime.anchor
	previousCompleted := session.spendTime.completedStages
	changed := false
	rewardItems := make([]dnfonlineevent.CommittedItem, 0, currentSpendTimeStageCount)
	var snapshot dnfonlineevent.Snapshot
	var claimErr error
	partialResult := func() currentSpendTimeSettlementResult {
		return currentSpendTimeSettlementResult{
			changed:     changed || previousCompleted != session.spendTime.completedStages || len(rewardItems) != 0,
			rewardItems: rewardItems,
			claimErr:    claimErr,
		}
	}
	for cursor.Before(intervalTo) {
		nextBoundary := currentSpendTimeNextServiceBoundary(definition, cursor)
		segmentEnd := intervalTo
		endsAtBoundary := false
		if !nextBoundary.After(segmentEnd) {
			segmentEnd = nextBoundary
			endsAtBoundary = true
		}
		result, err := owner.Observe(ctx, dnfonlineevent.ObserveCommand{
			AccountID:    session.spendTime.accountID,
			CharacterID:  characterID,
			Definition:   definition,
			IntervalFrom: cursor,
			IntervalTo:   segmentEnd,
		})
		if err != nil {
			return partialResult(), err
		}
		changed = changed || result.Changed
		claimAt := segmentEnd
		if endsAtBoundary {
			// Claim the just-finished service day before a following Observe or
			// Status resets its daily ledger at exactly 06:00.
			claimAt = segmentEnd.Add(-time.Nanosecond)
		}
		var segmentItems []dnfonlineevent.CommittedItem
		snapshot, segmentItems, claimErr = s.claimCurrentSpendTimeUnlockedStages(
			ctx,
			owner,
			session.spendTime.accountID,
			characterID,
			session.spendTime.catalog,
			result.Snapshot,
			claimAt,
		)
		rewardItems = append(rewardItems, segmentItems...)
		session.spendTime.onlineSeconds = snapshot.OnlineSeconds
		session.spendTime.completedStages = currentSpendTimeClaimedStages(definition, snapshot)
		if claimErr != nil {
			// Do not cross the reset boundary until the old day's unlocked
			// rewards are durably settled. Re-observing the retained interval is
			// idempotent because the owner stores an interval union.
			if !endsAtBoundary {
				session.spendTime.anchor = segmentEnd
			}
			return currentSpendTimeSettlementResult{
				changed:     changed || previousCompleted != session.spendTime.completedStages || len(rewardItems) != 0,
				rewardItems: rewardItems,
				claimErr:    claimErr,
			}, nil
		}
		session.spendTime.anchor = segmentEnd
		cursor = segmentEnd

		if endsAtBoundary && !cursor.Before(intervalTo) {
			priorDate := snapshot.CalendarDate
			snapshot, err = owner.Status(ctx, dnfonlineevent.StatusCommand{
				AccountID:   session.spendTime.accountID,
				CharacterID: characterID,
				Definition:  definition,
				ObservedAt:  segmentEnd,
			})
			if err != nil {
				return partialResult(), err
			}
			changed = changed || snapshot.CalendarDate != priorDate
			session.spendTime.onlineSeconds = snapshot.OnlineSeconds
			session.spendTime.completedStages = currentSpendTimeClaimedStages(definition, snapshot)
		}
	}
	return currentSpendTimeSettlementResult{
		changed:     changed || previousCompleted != session.spendTime.completedStages || len(rewardItems) != 0,
		rewardItems: rewardItems,
		claimErr:    claimErr,
	}, nil
}

func currentSpendTimeNextServiceBoundary(
	definition dnfonlineevent.Definition,
	at time.Time,
) time.Time {
	location := definition.Calendar
	if location == nil {
		location = dnfonlineevent.ChinaCalendar
	}
	boundary := dnfonlineevent.DailyBoundary{Hour: currentSpendTimeResetHour}
	if definition.Boundary != nil {
		boundary = *definition.Boundary
	}
	local := at.In(location)
	next := time.Date(
		local.Year(),
		local.Month(),
		local.Day(),
		boundary.Hour,
		boundary.Minute,
		boundary.Second,
		0,
		location,
	)
	if !at.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (s *Service) stopCurrentSpendTimeClock(session *gameSession, source string) {
	if s == nil || session == nil {
		return
	}
	session.spendTime.mu.Lock()
	if session.spendTime.characterID == 0 || session.spendTime.catalog == nil {
		session.spendTime.mu.Unlock()
		return
	}
	settlement, settleErr := s.settleCurrentSpendTimeClockLocked(session, s.gameplayNow())
	characterID := session.spendTime.characterID
	timerName := session.spendTime.timerName
	session.spendTime.generation++
	session.spendTime.characterID = 0
	session.spendTime.characterGeneration = 0
	session.spendTime.initializedCharacterGeneration = 0
	session.spendTime.accountID = ""
	session.spendTime.anchor = time.Time{}
	session.spendTime.timerName = ""
	session.spendTime.onlineSeconds = 0
	session.spendTime.completedStages = 0
	session.spendTime.catalog = nil
	session.spendTime.mu.Unlock()
	if timerName != "" {
		if queue := s.ensureGameplayTimeQueue(); queue != nil {
			queue.Cancel(timerName)
		}
	}
	if settleErr != nil {
		s.logGameEvent(session, "game-spend-time-stop-settlement-failed",
			"char_id", characterID,
			"source", source,
			"error", settleErr)
	}
	if settlement.claimErr != nil {
		s.logGameEvent(session, "game-spend-time-stop-auto-award-deferred",
			"char_id", characterID,
			"source", source,
			"error", settlement.claimErr)
	}
}
