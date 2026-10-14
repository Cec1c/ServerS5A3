package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件承载副职业门店（分解机 / 附魔师）的整套协议与会话状态。
//
// # 缺陷：其他玩家看不到你摆的分解机（2026-09-25 香港现场）
//
// 操作者原话：「分解机自己看正常 自己分解正常 **其他角色看不到你的分解机**」。
// 摆的人自己看得见、能进自己的店、能分解——那三件事分别由 op598 的 ACK（`01`）、
// op599 的 ACK、op235/op601 的 ACK 驱动，都与"别人的客户端"无关；别人要看见这台
// 机器，**只能**靠我们自己把 NOTI538（peer-only 投影）发给他。所以缺陷只在投影这一步。
//
// 取证（`/opt/dnf90/logs/packet_log.txt`，20:20 部署后的那一份，行号为原文行号）：
//
//	:554 game-town-presence-published conn=game-000003 char=3 town=38 area=0 inst=0 peers=0
//	:579 RECV type=598 CREATE_EXPERT_JOB_STORE conn=game-000003      ← A 在共享城镇 38/0 摆机器
//	:581 SEND msg_id=598 body=01 conn=game-000003                    ← 建店成功（A 自己看得见）
//	     … 这一段**没有任何 msg_id=538**：A 摆的那一刻区域里只有他自己（peers=0），
//	       建店瞬间的广播列表为空 ⇒ 一个字节都没发出去。
//	:828 game-000006 char=4 登录（先落在自己的私有赛丽亚房 38/1 inst=4）
//	:899 game-town-presence-published conn=game-000006 char=4 town=38 area=0 inst=0
//	     source=prev_village_return_from_private_seria_room same_area_peers=1
//	     ← B 走出了私房、走进了 A 所在的那个共享区域（A 的机器就摆在那儿）
//	     … 仍然**没有任何 msg_id=538** ⇒ B 的客户端从头到尾没收到过这台机器。
//	:917 RECV type=600 CLOSE_EXPERT_JOB_STORE conn=game-000003
//	:919/:921 SEND msg_id=539 → game-000003 与 game-000006（owner + 同区域对端）
//	     ← 关闭广播的"同区域扇出"是通的、两人的区域桶确实相同，坏的只有建店投影。
//
// 全服统计：`grep -ac msg_id=538 /opt/dnf90/logs/packet_log*.txt` 在**每一个**历史封包
// 日志里都是 0 —— 这台服务器从来没有成功给任何人投过一次分解机。
//
// 根因（分类②）：建店瞬间的广播只覆盖"那一刻已经在本区域"的会话快照；为后进者准备的
// 补投影 `replayCurrentExpertJobStores` 只挂在 op36 SET_USER_AREA 一条路径上
// （town_move.go:363），而 B 走的是赛丽亚房间出房门（town_copresence_actor_resend.go 的
// publishTownPresenceAfterSeriaRoomReturn）与出副本回城重绑（online_player_manager.go:636）
// ——这两条路径都不补。于是"摆的时候别人不在、别人来了又不补"⇒ 永远看不到。
//
// opcode 不是根因（**不要改**）：`s4a21_master_unzip/.../Network/Core/PacketTypesA21.cs`
// 的 NOTI 表里 USE_ENCHANT_STORE=0x0216(534) / CREATE_EXPERT_JOB_STORE=0x021B(539) /
// CLOSE_EXPERT_JOB_STORE=0x021C(540) / UPDATE_EXPERT_JOB_STORE=0x0221(545)，CMD 表里
// CREATE_EXPERT_JOB_STORE=0x0257(599)；本机 90CN 客户端活包日志实测 `type=598
// runtime_cmd_name=ENUM_CMDPACKET_CREATE_EXPERT_JOB_STORE` ⇒ 90CN 整张表 = A21 **减 1**，
// 逐条对上 538↔539、539↔540、544↔545、533↔534。`a21zigai/86JP` 的
// `BuildCloseNotification` 只写 u16 owner，与本文件 close body 逐字节一致，同族印证。
// 包体形状同样不是根因：建店投影 body 由 `buildCurrentExpertJobStoreCreateNotification`
// 产出（既有用例 `TestCurrentExpertJobStoreLayoutsMatch86JPReaders` 逐字节钉住），本次不动。
//
// 修法：把"补投影"挂到**所有**城镇入场路径的公共收口 `broadcastTownPlayerEnter`
// （op36 / 赛丽亚出房门 / 出副本回城重绑都调用它），按会话的"本次入场"序号幂等，
// 保证与 town_move.go:363 已有的那次重放互不重复；具名日志带 body_hex。
//
// 回退：`DNFBRIDGE_EXPERT_JOB_STORE_PROJECTION=off`（也接受 0/false/no/legacy）⇒
// 补投影一次都不发、`replayCurrentExpertJobStores` 恢复改动前的行为（逐字节不变）。
// 注意：`DNFBRIDGE_EXPERT_JOB_STORE_GOLD_*` / `DNFBRIDGE_EXPERT_JOB_GOLD_SOURCE`
// 等既有开关语义不受影响。
const currentExpertJobStoreProjectionEnv = "DNFBRIDGE_EXPERT_JOB_STORE_PROJECTION"

func currentExpertJobStoreProjectionEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentExpertJobStoreProjectionEnv)))
	switch value {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

const (
	currentExpertJobDisjointStoreKind byte = 0
	currentExpertJobEnchantStoreKind  byte = 3

	currentExpertJobStoreCreateNotification   uint16 = 538
	currentExpertJobStoreCloseNotification    uint16 = 539
	currentExpertJobStoreUpdateNotification   uint16 = 544
	currentExpertJobEnchantOwnerNotification  uint16 = 533
	currentExpertJobDisjointOwnerNotification uint16 = 203
	// NoPack 198AE70 stores this byte at +3C4 through 3138A60.
	// 2294BB0 branches on 31CF460: mode 2 creates a separate scene actor;
	// mode 1 resolves the existing owner via 6A6BC0 and attaches the store.
	// Player stores must follow the owner, otherwise peers see a second,
	// uncostumed actor at the same position. The owner ACK is independent.
	currentExpertJobStoreVisibleAttachMode byte = 1
)

type currentExpertJobStore struct {
	OwnerCharacterID uint16
	OwnerSession     *gameSession
	Kind             byte
	Name             []byte
	Cost             int64
	TownID           byte
	AreaID           byte
	// ChannelID 让"分解机/附魔师"这类地面店铺也按频道隔离：店铺是摆在本频道
	// 城镇地面上的可见物体，跨频道的玩家既看不到它也不应收到它的开/关/更新通知。
	ChannelID int
	// Instance 与 onlinePlayerInfo.Instance 同一口径：0 = 共享城镇区域，非 0 =
	// "每个角色一份"的私有房间（赛丽亚的房间）拥有者角色 ID。摆在私有房间里的
	// 机器只对同一个实例可见，不能投影进别人的私房，也不能投影进共享城镇。
	Instance         uint32
	PositionX        int16
	PositionY        int16
	OpaqueObjectLink uint16
	MachineGrade     int
	Endurance        int64
	Qualifications   []byte
}

type currentExpertJobStoreCreateRequest struct {
	Kind             byte
	Name             []byte
	Cost             int64
	PositionX        int16
	PositionY        int16
	OpaqueObjectLink uint16
}

type currentExpertJobStoreDisjointRequest struct {
	OwnerID    uint16
	TargetSlot int16
	TargetList byte
}

type currentExpertJobStoreEnchantRequest struct {
	OwnerID    uint16
	RecipeID   int64
	TargetList byte
	TargetSlot int16
	CardList   byte
	CardSlot   int16
}

type currentExpertJobStoreUseResult struct {
	RequesterGold  int64
	OwnerGold      int64
	Endurance      int64
	Experience     int64
	LevelChanged   bool
	Success        bool
	TargetList     byte
	TargetSlot     int16
	Materials      []currentExpertJobExtractionMaterial
	Qualifications []byte
}

func parseCurrentExpertJobStoreCreateRequest(body []byte) (currentExpertJobStoreCreateRequest, error) {
	if len(body) < 15 {
		return currentExpertJobStoreCreateRequest{}, fmt.Errorf("expert store create body=%d", len(body))
	}
	nameLength := int(int32(binary.LittleEndian.Uint32(body[1:5])))
	if nameLength < 0 || nameLength > math.MaxUint8 || len(body) != 15+nameLength {
		return currentExpertJobStoreCreateRequest{}, errors.New("expert store name boundary is invalid")
	}
	offset := 5 + nameLength
	request := currentExpertJobStoreCreateRequest{
		Kind: body[0], Name: append([]byte(nil), body[5:offset]...),
		Cost:             int64(binary.LittleEndian.Uint32(body[offset : offset+4])),
		PositionX:        int16(binary.LittleEndian.Uint16(body[offset+4 : offset+6])),
		PositionY:        int16(binary.LittleEndian.Uint16(body[offset+6 : offset+8])),
		OpaqueObjectLink: binary.LittleEndian.Uint16(body[offset+8 : offset+10]),
	}
	if nameLength == 0 || (request.Kind != currentExpertJobDisjointStoreKind && request.Kind != currentExpertJobEnchantStoreKind) {
		return currentExpertJobStoreCreateRequest{}, errors.New("expert store create values are invalid")
	}
	return request, nil
}

func parseCurrentExpertJobStoreDisjointRequest(body []byte) (currentExpertJobStoreDisjointRequest, error) {
	if len(body) != 5 {
		return currentExpertJobStoreDisjointRequest{}, fmt.Errorf("store disjoint body=%d", len(body))
	}
	request := currentExpertJobStoreDisjointRequest{OwnerID: binary.LittleEndian.Uint16(body[0:2]), TargetSlot: int16(binary.LittleEndian.Uint16(body[2:4])), TargetList: body[4]}
	if request.OwnerID == 0 || request.TargetSlot < 0 || request.TargetList != dnfrepo.MainInventoryListType {
		return currentExpertJobStoreDisjointRequest{}, errors.New("store disjoint values are invalid")
	}
	return request, nil
}

func parseCurrentExpertJobStoreEnchantRequest(body []byte) (currentExpertJobStoreEnchantRequest, error) {
	if len(body) != 13 {
		return currentExpertJobStoreEnchantRequest{}, fmt.Errorf("store enchant body=%d", len(body))
	}
	request := currentExpertJobStoreEnchantRequest{
		OwnerID: binary.LittleEndian.Uint16(body[0:2]), RecipeID: int64(int32(binary.LittleEndian.Uint32(body[2:6]))),
		TargetList: body[7], TargetSlot: int16(binary.LittleEndian.Uint16(body[8:10])), CardList: body[10], CardSlot: int16(binary.LittleEndian.Uint16(body[11:13])),
	}
	if request.OwnerID == 0 || request.RecipeID <= 0 || body[6] != 2 || request.TargetList != dnfrepo.MainInventoryListType || request.CardList != dnfrepo.MainInventoryListType || request.TargetSlot < 0 || request.CardSlot < 0 || request.TargetSlot == request.CardSlot {
		return currentExpertJobStoreEnchantRequest{}, errors.New("store enchant values are invalid")
	}
	return request, nil
}

func (s *Service) handleCurrentExpertJobStoreCreate(session *gameSession, opcode uint16, body []byte) error {
	request, err := parseCurrentExpertJobStoreCreateRequest(body)
	if err != nil {
		// 与 createCurrentExpertJobStore 里的拒绝分支一样：错误码 19 有好几种成因，
		// 必须留下可归因的一行，否则现场只能看到 "00 13"。
		s.logGameEvent(session, "game-expert-job-store-create-blocked",
			"char_id", sessionSelectedCharacterID(session),
			"reason", "store_create_request_body_invalid",
			"body_len", len(body),
			"error", err)
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	s.expertJobStoreOpMu.Lock()
	defer s.expertJobStoreOpMu.Unlock()
	store, err := s.createCurrentExpertJobStore(session, request)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentExpertJobStoreError(err))
	}
	createBody := buildCurrentExpertJobStoreCreateNotification(store)
	if err := s.sendGameUpperRawClass(session, opcode, []byte{1}, dnfproto.DefaultChannelClassification); err != nil {
		s.removeCurrentExpertJobStore(store.OwnerCharacterID, store.OwnerSession)
		return err
	}
	// op598 creates the owner's local object. NOTI544 attaches enchanter
	// metadata to that object, while NOTI538 is strictly the peer projection.
	if store.Kind == currentExpertJobEnchantStoreKind {
		if err := s.sendGameUpperRawClass(session, currentExpertJobStoreUpdateNotification, buildCurrentExpertJobStoreUpdateNotification(store), 0); err != nil {
			s.removeCurrentExpertJobStore(store.OwnerCharacterID, store.OwnerSession)
			return err
		}
	}
	// 建店瞬间只覆盖"此刻已经在本区域"的会话；之后进入该区域的人由
	// projectCurrentExpertJobStoresToNewcomer 补（见文件头缺陷取证）。
	s.broadcastCurrentExpertJobStoreCreate(store, createBody, "store_create")
	return nil
}

// logCurrentExpertJobStoreCreateBlocked 让"放置分解机失败"可归因。
//
// 背景（2026-09-25 香港现场）：操作者报「副职业分解机无法放置」。全量封包日志里
// op598 CREATE_EXPERT_JOB_STORE(Kind=0=DisjointMachine) 共 5 次，其中 4 次回
// `01`（成功）、1 次回 `00 13`（错误码 19）：
//
//	packet_log.20260925-165109.txt:10469  conn=game-000003 char=9
//	  body=00 01000000 01 01000000 8b02 c500 ffff
//	  → Kind=0 / nameLen=1 / cost=1 / pos=(651,197) / dir=-1
//	  回包 body=00 13（错误码 19）
//
// 但 createCurrentExpertJobStore 有 6 条不同的拒绝分支，最终都映射成错误码 19，
// 而且**一个字节的诊断都不写**，所以从日志无法判定是哪一条拒绝的（在副本刚返回城镇
// 期间尝试、组队中尝试、任务职业不匹配、机器已存在、配置缺失，现象完全一样）。
// 这里只补诊断，不改任何判定与回包：下一次复测用
// `grep game-expert-job-store-create-blocked` 就能直接看到 reason。
func (s *Service) logCurrentExpertJobStoreCreateBlocked(session *gameSession, request currentExpertJobStoreCreateRequest, reason string, extra ...any) {
	if s == nil {
		return
	}
	fields := []any{
		"char_id", sessionSelectedCharacterID(session),
		"kind", request.Kind,
		"cost", request.Cost,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"reason", reason,
	}
	fields = append(fields, extra...)
	s.logGameEvent(session, "game-expert-job-store-create-blocked", fields...)
}

func (s *Service) createCurrentExpertJobStore(session *gameSession, request currentExpertJobStoreCreateRequest) (*currentExpertJobStore, error) {
	// 复合条件拆成可归因的单一 reason，判定与顺序完全不变。
	switch {
	case s == nil || session == nil || session.selectedCharacterID == 0 || s.onlinePlayers == nil:
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "store_create_owner_context_unavailable")
		return nil, dnfexpertjob.ErrMachineInvalid
	case currentExpertJobSessionInDungeon(session):
		// 客户端只在城镇允许放置；这里仍把它当"在副本内"通常意味着副本返回还没收口
		// （副本 runtime 未清）。
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "session_still_owns_dungeon_runtime")
		return nil, dnfexpertjob.ErrMachineInvalid
	case runtimePartyStateSnapshot(session).PartyID != 0:
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "session_is_in_party",
			"party_id", runtimePartyStateSnapshot(session).PartyID)
		return nil, dnfexpertjob.ErrMachineInvalid
	}
	player, ok := s.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
	if !ok || player.Session != session || player.TownID == 0 {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "online_player_town_registration_missing",
			"player_found", ok, "session_matches", ok && player.Session == session, "town_id", player.TownID)
		return nil, dnfexpertjob.ErrMachineInvalid
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "repository_group_unavailable")
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	catalog, err := s.currentExpertJobCatalog()
	if err != nil {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "expert_job_catalog_unavailable", "error", err)
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil || !found || character.CharacterID != characterID {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "selected_character_not_found",
			"found", found, "error", err)
		return nil, dnfexpertjob.ErrCharacterNotFound
	}
	jobType, experience, err := currentExpertJobCharacterState(&character)
	if err != nil {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "selected_character_expert_job_type_invalid",
			"expert_job_type", character.Stats["expert_job_type"], "error", err)
		return nil, err
	}
	store := &currentExpertJobStore{OwnerCharacterID: session.selectedCharacterID, OwnerSession: session, Kind: request.Kind, Name: request.Name, Cost: request.Cost, TownID: player.TownID, AreaID: player.AreaID, ChannelID: player.ChannelID, Instance: player.Instance, PositionX: request.PositionX, PositionY: request.PositionY, OpaqueObjectLink: request.OpaqueObjectLink}
	switch request.Kind {
	case currentExpertJobDisjointStoreKind:
		config, ok := catalog.Disjointer()
		if !ok || jobType != dnfexpertjob.DisjointerType || request.Cost > config.MaximumStoreCharge {
			s.logCurrentExpertJobStoreCreateBlocked(session, request, "disjoint_store_requirement_not_met",
				"expert_job_type", jobType, "required_expert_job_type", dnfexpertjob.DisjointerType,
				"disjointer_config_found", ok, "maximum_store_charge", config.MaximumStoreCharge)
			return nil, dnfexpertjob.ErrMachineInvalid
		}
		store.MachineGrade = int(currentExpertJobStatDefault(character.Stats, currentExpertJobMachineGradeStat, 1))
		store.Endurance = currentExpertJobStatDefault(character.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance)
		if _, ok := config.RepairRule(store.MachineGrade); !ok || store.Endurance <= 0 {
			s.logCurrentExpertJobStoreCreateBlocked(session, request, "disjoint_machine_grade_or_endurance_unusable",
				"machine_grade", store.MachineGrade, "endurance", store.Endurance)
			return nil, dnfexpertjob.ErrMachineEndurance
		}
	case currentExpertJobEnchantStoreKind:
		config, ok := catalog.Enchanter()
		if !ok || jobType != dnfexpertjob.EnchanterType || request.Cost > config.MaximumStoreCharge {
			s.logCurrentExpertJobStoreCreateBlocked(session, request, "enchant_store_requirement_not_met",
				"expert_job_type", jobType, "required_expert_job_type", dnfexpertjob.EnchanterType,
				"enchanter_config_found", ok, "maximum_store_charge", config.MaximumStoreCharge)
			return nil, dnfexpertjob.ErrMachineInvalid
		}
		store.Endurance = currentExpertJobStatDefault(character.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance)
		store.Qualifications = config.Qualifications(experience)
		if store.Endurance <= 0 {
			s.logCurrentExpertJobStoreCreateBlocked(session, request, "enchant_store_endurance_unusable",
				"endurance", store.Endurance)
			return nil, dnfexpertjob.ErrMachineEndurance
		}
	default:
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "store_kind_unsupported")
		return nil, dnfexpertjob.ErrMachineInvalid
	}
	if err := s.validateCurrentExpertJobStorePlacement(player, request); err != nil {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "store_placement_not_allowed", "town_id", player.TownID, "area_id", player.AreaID, "error", err)
		return nil, err
	}
	s.expertJobStoreMu.Lock()
	defer s.expertJobStoreMu.Unlock()
	if s.expertJobStores == nil {
		s.expertJobStores = make(map[uint16]*currentExpertJobStore)
	}
	if _, exists := s.expertJobStores[store.OwnerCharacterID]; exists {
		s.logCurrentExpertJobStoreCreateBlocked(session, request, "store_already_placed_for_character")
		return nil, dnfexpertjob.ErrMachineInvalid
	}
	s.expertJobStores[store.OwnerCharacterID] = store
	return cloneCurrentExpertJobStore(store), nil
}

func (s *Service) handleCurrentExpertJobStoreEnter(session *gameSession, opcode uint16, body []byte) error {
	if len(body) != 2 || session == nil || session.selectedCharacterID == 0 || currentExpertJobSessionInDungeon(session) {
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	ownerID := binary.LittleEndian.Uint16(body)
	store, ok := s.enterCurrentExpertJobStore(session, ownerID)
	if !ok {
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	return s.sendGameUpperRawClass(session, opcode, buildCurrentExpertJobStoreEnterSuccess(store), dnfproto.DefaultChannelClassification)
}

func (s *Service) enterCurrentExpertJobStore(session *gameSession, ownerID uint16) (*currentExpertJobStore, bool) {
	if s == nil || session == nil || ownerID == 0 || s.onlinePlayers == nil {
		return nil, false
	}
	visitor, visitorOK := s.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
	s.expertJobStoreMu.Lock()
	defer s.expertJobStoreMu.Unlock()
	store := s.expertJobStores[ownerID]
	if !visitorOK || visitor.Session != session || store == nil || store.OwnerSession == nil ||
		store.TownID != visitor.TownID || store.AreaID != visitor.AreaID || store.ChannelID != visitor.ChannelID {
		return nil, false
	}
	if s.expertJobVisitors == nil {
		s.expertJobVisitors = make(map[*gameSession]uint16)
	}
	s.expertJobVisitors[session] = ownerID
	return cloneCurrentExpertJobStore(store), true
}

func (s *Service) handleCurrentExpertJobStoreClose(session *gameSession, opcode uint16, body []byte) error {
	if len(body) != 0 || session == nil || session.selectedCharacterID == 0 {
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	s.expertJobStoreOpMu.Lock()
	defer s.expertJobStoreOpMu.Unlock()
	store := s.removeCurrentExpertJobStore(session.selectedCharacterID, session)
	if store != nil {
		s.broadcastCurrentExpertJobStoreClose(store, true)
		return nil
	}
	s.expertJobStoreMu.Lock()
	_, visitor := s.expertJobVisitors[session]
	delete(s.expertJobVisitors, session)
	s.expertJobStoreMu.Unlock()
	if visitor {
		return nil
	}
	return s.sendGameUpperFailure(session, opcode, 19)
}

func (s *Service) removeCurrentExpertJobStore(ownerID uint16, ownerSession *gameSession) *currentExpertJobStore {
	if s == nil || ownerID == 0 {
		return nil
	}
	s.expertJobStoreMu.Lock()
	defer s.expertJobStoreMu.Unlock()
	store := s.expertJobStores[ownerID]
	if store == nil || (ownerSession != nil && store.OwnerSession != ownerSession) {
		return nil
	}
	delete(s.expertJobStores, ownerID)
	for visitor, enteredOwner := range s.expertJobVisitors {
		if enteredOwner == ownerID {
			delete(s.expertJobVisitors, visitor)
		}
	}
	return cloneCurrentExpertJobStore(store)
}

func (s *Service) closeCurrentExpertJobStoreSession(session *gameSession, includeOwner bool) {
	if s == nil || session == nil {
		return
	}
	s.expertJobStoreOpMu.Lock()
	defer s.expertJobStoreOpMu.Unlock()
	store := s.removeCurrentExpertJobStore(session.selectedCharacterID, session)
	s.expertJobStoreMu.Lock()
	delete(s.expertJobVisitors, session)
	s.expertJobStoreMu.Unlock()
	if store != nil {
		s.broadcastCurrentExpertJobStoreClose(store, includeOwner)
	}
}

func (s *Service) broadcastCurrentExpertJobStoreClose(store *currentExpertJobStore, includeOwner bool) {
	if s == nil || store == nil || s.onlinePlayers == nil {
		return
	}
	body := buildCurrentExpertJobStoreCloseNotification(store.OwnerCharacterID)
	notified := make([]uint16, 0)
	for _, player := range s.onlinePlayers.GetAreaPlayers(store.TownID, store.AreaID, store.ChannelID) {
		if player.Session == nil || (!includeOwner && player.CharacterID == store.OwnerCharacterID) {
			continue
		}
		if err := s.sendGameUpperRawClass(player.Session, currentExpertJobStoreCloseNotification, body, 0); err != nil {
			s.logGameEvent(player.Session, "game-expert-job-store-close-broadcast-failed",
				"store_owner_char_id", store.OwnerCharacterID,
				"peer_char_id", player.CharacterID,
				"error", err)
			continue
		}
		notified = append(notified, player.CharacterID)
	}
	s.logGameEvent(store.OwnerSession, "game-expert-job-store-close-broadcast",
		"store_owner_char_id", store.OwnerCharacterID,
		"town_id", store.TownID,
		"area_id", store.AreaID,
		"area_key_channel", store.ChannelID,
		"notified_char_ids", notified,
		"include_owner", includeOwner,
		"msg_id", currentExpertJobStoreCloseNotification,
		"body_hex", hex.EncodeToString(body))
}

// broadcastCurrentExpertJobStoreCreate 把新摆好的门店投影给"摆的这一刻已经在本区域"的其他人。
//
// 只覆盖在场快照是本缺陷的另一半：那一刻不在的人（换线、从私房出来、刚登录、出副本
// 回城）由 projectCurrentExpertJobStoresToNewcomer 在他真正进场时补。两边都带具名日志
// （含 body_hex），复测时 `grep game-expert-job-store-broadcast` /
// `grep game-expert-job-store-projection` 就能看到"发给谁、发的是什么字节"。
func (s *Service) broadcastCurrentExpertJobStoreCreate(store *currentExpertJobStore, body []byte, source string) {
	if s == nil || store == nil || s.onlinePlayers == nil || len(body) == 0 {
		return
	}
	peers := s.onlinePlayers.PeersInSameArea(store.OwnerCharacterID)
	sent := make([]uint16, 0, len(peers))
	for _, peer := range peers {
		if peer.Session == nil {
			continue
		}
		if err := s.sendGameUpperRawClass(peer.Session, currentExpertJobStoreCreateNotification, body, 0); err != nil {
			s.logGameEvent(peer.Session, "game-expert-job-store-broadcast-failed",
				"source", source,
				"store_owner_char_id", store.OwnerCharacterID,
				"peer_char_id", peer.CharacterID,
				"error", err)
			continue
		}
		sent = append(sent, peer.CharacterID)
	}
	s.logGameEvent(store.OwnerSession, "game-expert-job-store-broadcast",
		"source", source,
		"store_owner_char_id", store.OwnerCharacterID,
		"store_kind", store.Kind,
		"town_id", store.TownID,
		"area_id", store.AreaID,
		"area_key_channel", store.ChannelID,
		"area_instance", store.Instance,
		"peer_char_ids", sent,
		"peer_count", len(sent),
		"msg_id", currentExpertJobStoreCreateNotification,
		"body_hex", hex.EncodeToString(body),
		"reason", "peer-only projection is sent at placement time for peers already in the area")
}

// collectCurrentExpertJobStoresForArea 取出"此刻应当投影给这个人的门店快照"。
//
// 判据与区域分桶键同源：同城镇 + 同区域 + 同频道；matchInstance 时再要求实例一致
// （赛丽亚的房间是每个角色一份的私有房间，摆在别人私房里的机器不该出现在本地场景里，
// 也不该把共享城镇的机器投进任何人的私房）。自己的店永远不投给自己。
func (s *Service) collectCurrentExpertJobStoresForArea(
	viewerCharacterID uint16,
	townID, areaID byte,
	channelID int,
	instance uint32,
	matchInstance bool,
) []*currentExpertJobStore {
	if s == nil || viewerCharacterID == 0 {
		return nil
	}
	s.expertJobStoreMu.Lock()
	defer s.expertJobStoreMu.Unlock()
	stores := make([]*currentExpertJobStore, 0, len(s.expertJobStores))
	for _, store := range s.expertJobStores {
		if store == nil || store.OwnerSession == nil || store.OwnerCharacterID == viewerCharacterID {
			continue
		}
		if store.TownID != townID || store.AreaID != areaID || store.ChannelID != channelID {
			continue
		}
		if matchInstance && store.Instance != instance {
			continue
		}
		stores = append(stores, cloneCurrentExpertJobStore(store))
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].OwnerCharacterID < stores[j].OwnerCharacterID })
	return stores
}

// projectCurrentExpertJobStoresToNewcomer 在"某人刚进入一个城镇区域"时，把该区域里
// 已经摆好的分解机/附魔师门店补投影给他。
//
// 调用点：`broadcastTownPlayerEnter`（online_player_manager.go）的末尾 —— 它是 op36
// SET_USER_AREA、赛丽亚房间出房门（town_copresence_actor_resend.go）、出副本回城重绑
// （online_player_manager.go 的 rebind）三条入场路径的公共出口，所以不必在每个文件里各加
// 一处（也就不会与正在改 town_copresence_*/party_* 的其它批次撞车）。
//
// 幂等不需要会话级状态：op36 那条路径上的 `replayCurrentExpertJobStores`（town_move.go:363）
// 就在 broadcastTownPlayerEnter 后面 1 行，修好档下必然已被本次补投影覆盖 ⇒ 那条重放直接
// 变成空操作（既不用新增会话字段，也不会把同一台机器投两次）。
func (s *Service) projectCurrentExpertJobStoresToNewcomer(
	session *gameSession,
	newPlayer *onlinePlayerInfo,
	source string,
) {
	if !currentExpertJobStoreProjectionEnabled() {
		return
	}
	if s == nil || session == nil || newPlayer == nil || newPlayer.Session != session {
		return
	}
	stores := s.collectCurrentExpertJobStoresForArea(
		newPlayer.CharacterID,
		newPlayer.TownID,
		newPlayer.AreaID,
		newPlayer.ChannelID,
		newPlayer.Instance,
		true,
	)
	if len(stores) == 0 {
		// 本区域没有别人的店：什么都不发。这条日志让复测能区分"补了但当时没人摆"
		// 与"根本没走到补投影这一步"。
		s.logGameEvent(session, "game-expert-job-store-projection-empty",
			"source", source,
			"char_id", newPlayer.CharacterID,
			"town_id", newPlayer.TownID,
			"area_id", newPlayer.AreaID,
			"area_key_channel", newPlayer.ChannelID,
			"area_instance", newPlayer.Instance)
		return
	}
	s.sendCurrentExpertJobStoreProjection(session, stores, source)
}

// sendCurrentExpertJobStoreProjection 把门店逐个投影给 session：只补对端、失败只记事件
// （`game-expert-job-store-projection-failed`），绝不影响主链路。
func (s *Service) sendCurrentExpertJobStoreProjection(
	session *gameSession,
	stores []*currentExpertJobStore,
	source string,
) bool {
	if s == nil || session == nil || len(stores) == 0 {
		return false
	}
	ownerIDs := make([]uint16, 0, len(stores))
	bodies := make([]string, 0, len(stores))
	for _, store := range stores {
		body := buildCurrentExpertJobStoreCreateNotification(store)
		if err := s.sendGameUpperRawClass(session, currentExpertJobStoreCreateNotification, body, 0); err != nil {
			s.logGameEvent(session, "game-expert-job-store-projection-failed",
				"source", source,
				"char_id", session.selectedCharacterID,
				"store_owner_char_id", store.OwnerCharacterID,
				"error", err)
			return false
		}
		ownerIDs = append(ownerIDs, store.OwnerCharacterID)
		bodies = append(bodies, hex.EncodeToString(body))
	}
	s.logGameEvent(session, "game-expert-job-store-projection",
		"source", source,
		"char_id", session.selectedCharacterID,
		"store_owner_char_ids", ownerIDs,
		"store_count", len(ownerIDs),
		"msg_id", currentExpertJobStoreCreateNotification,
		"body_hex", strings.Join(bodies, ","),
		"reason", "late area joiner needs the peer-only expert job store projection replayed")
	return true
}

// replayCurrentExpertJobStores 是 op36 SET_USER_AREA 那条路径上的重放入口
// （town_move.go:363）。
//
// 修好档：这条重放**必然**已被 broadcastTownPlayerEnter 末尾的补投影覆盖（同一个处理里
// 先调 broadcastTownPlayerEnter、紧接着才调本函数），所以这里直接返回 —— 不重复投同一台
// 机器（重复投影会让客户端把同一台机器建两次），也不再需要任何会话级幂等键。
// 回退档：保持改动前的逐字节行为（无实例过滤、无条件发送一次）。
func (s *Service) replayCurrentExpertJobStores(session *gameSession, townID, areaID byte) {
	if s == nil || session == nil {
		return
	}
	if currentExpertJobStoreProjectionEnabled() {
		return
	}
	// 重放只针对进图者自己的频道：别的频道地面上摆的店铺不该出现在本地场景里。
	channelID := currentTownPresenceChannelID(session)
	s.expertJobStoreMu.Lock()
	stores := make([]*currentExpertJobStore, 0)
	for _, store := range s.expertJobStores {
		if store.TownID == townID && store.AreaID == areaID && store.ChannelID == channelID && store.OwnerSession != session {
			stores = append(stores, cloneCurrentExpertJobStore(store))
		}
	}
	s.expertJobStoreMu.Unlock()
	for _, store := range stores {
		_ = s.sendGameUpperRawClass(session, currentExpertJobStoreCreateNotification, buildCurrentExpertJobStoreCreateNotification(store), 0)
	}
}

func cloneCurrentExpertJobStore(store *currentExpertJobStore) *currentExpertJobStore {
	if store == nil {
		return nil
	}
	clone := *store
	clone.Name = append([]byte(nil), store.Name...)
	clone.Qualifications = append([]byte(nil), store.Qualifications...)
	return &clone
}

func currentExpertJobSessionInDungeon(session *gameSession) bool {
	if session == nil {
		return true
	}
	session.dungeon.mu.Lock()
	active := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	return active
}

func buildCurrentExpertJobStoreCreateNotification(store *currentExpertJobStore) []byte {
	w := packetWriter{}
	w.writeByte(store.Kind)
	w.writeUint16(store.OwnerCharacterID)
	w.writeRawDstr(store.Name)
	w.writeByte(store.TownID)
	w.writeUint32(uint32(store.AreaID))
	w.writeUint16(uint16(store.PositionX))
	w.writeUint16(uint16(store.PositionY))
	w.writeUint32(uint32(store.Cost))
	w.writeByte(currentExpertJobStoreVisibleAttachMode)
	if store.Kind == currentExpertJobEnchantStoreKind {
		w.writeByte(byte(len(store.Qualifications)))
		w.writeBytes(store.Qualifications)
	}
	return w.bytes()
}

func buildCurrentExpertJobStoreUpdateNotification(store *currentExpertJobStore) []byte {
	w := packetWriter{}
	w.writeByte(store.Kind)
	w.writeUint16(store.OwnerCharacterID)
	w.writeRawDstr(store.Name)
	if store.Kind == currentExpertJobEnchantStoreKind {
		w.writeByte(byte(len(store.Qualifications)))
		w.writeBytes(store.Qualifications)
	}
	return w.bytes()
}

func (s *Service) broadcastCurrentExpertJobStoreUpdate(store *currentExpertJobStore) {
	if s == nil || store == nil || s.onlinePlayers == nil {
		return
	}
	body := buildCurrentExpertJobStoreUpdateNotification(store)
	for _, player := range s.onlinePlayers.GetAreaPlayers(store.TownID, store.AreaID, store.ChannelID) {
		if player.Session != nil {
			_ = s.sendGameUpperRawClass(player.Session, currentExpertJobStoreUpdateNotification, body, 0)
		}
	}
}

func buildCurrentExpertJobStoreCloseNotification(ownerID uint16) []byte {
	w := packetWriter{}
	w.writeUint16(ownerID)
	return w.bytes()
}

func buildCurrentExpertJobStoreEnterSuccess(store *currentExpertJobStore) []byte {
	w := packetWriter{}
	w.writeByte(1)
	w.writeByte(store.Kind)
	if store.Kind == currentExpertJobEnchantStoreKind {
		w.writeUint16(store.OwnerCharacterID)
		w.writeInt32(int(store.Endurance))
	} else {
		w.writeByte(byte(store.MachineGrade))
		w.writeInt32(int(store.Cost))
		w.writeInt32(int(store.Endurance))
	}
	return w.bytes()
}

func currentExpertJobStoreError(err error) byte {
	switch {
	case errors.Is(err, dnfexpertjob.ErrStorePointUnavailable):
		// The current client has no placement message for the old reference's
		// 190. Keep the existing generic create rejection for unavailable maps.
		return 19
	case errors.Is(err, dnfexpertjob.ErrStoreCommercialZone):
		return 82
	case errors.Is(err, dnfexpertjob.ErrInsufficientGold):
		return 21
	case errors.Is(err, dnfexpertjob.ErrLevelTooLow):
		return 14
	case errors.Is(err, dnfexpertjob.ErrMachineEndurance):
		return 189
	case errors.Is(err, dnfexpertjob.ErrMachineGradeTooLow):
		return 0xD4
	case errors.Is(err, errDungeonPickupInventoryFull), errors.Is(err, errCurrentDisjointRewardInvalid):
		return 4
	default:
		return 19
	}
}

func (s *Service) handleCurrentExpertJobStoreRepair(session *gameSession, opcode uint16, body []byte) error {
	if len(body) != 0 {
		return s.sendGameUpperFailure(session, opcode, 60)
	}
	s.expertJobStoreOpMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	result, jobType, err := s.commitCurrentExpertJobMachineRepair(ctx, session)
	cancel()
	s.expertJobStoreOpMu.Unlock()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentExpertJobRepairError(err))
	}
	w := packetWriter{}
	w.writeByte(1)
	w.writeInt32(int(result.Gold))
	w.writeInt32(int(result.Endurance))
	if err := s.sendGameUpperRawClass(session, opcode, w.bytes(), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	return s.sendCurrentExpertJobInfoFromRepository(session, jobType, true)
}

func (s *Service) commitCurrentExpertJobMachineRepair(ctx context.Context, session *gameSession) (dnfexpertjob.MachineRepairPlan, byte, error) {
	jobCatalog, _, owner, accountID, characterID, err := s.currentExpertJobMutationContext(session)
	if err != nil || currentExpertJobSessionInDungeon(session) {
		return dnfexpertjob.MachineRepairPlan{}, 0, dnfexpertjob.ErrMachineInvalid
	}
	s.expertJobStoreMu.Lock()
	_, busy := s.expertJobStores[session.selectedCharacterID]
	s.expertJobStoreMu.Unlock()
	if busy {
		return dnfexpertjob.MachineRepairPlan{}, 0, dnfexpertjob.ErrMachineInvalid
	}
	var result dnfexpertjob.MachineRepairPlan
	var resultJobType byte
	err = owner.Machine(ctx, dnfexpertjob.Command{AccountID: accountID, CharacterID: characterID, UpdatedAt: s.gameplayNow(), Project: func(assets *dnfexpertjob.Assets) (dnfexpertjob.Changes, error) {
		jobType, experience, stateErr := currentExpertJobCharacterState(assets.Character)
		if stateErr != nil || (jobType != dnfexpertjob.EnchanterType && jobType != dnfexpertjob.DisjointerType) {
			return dnfexpertjob.Changes{}, dnfexpertjob.ErrJobUnsupported
		}
		grade := 1
		var rule dnfexpertjob.RepairRule
		var configured bool
		initial := int64(0)
		if jobType == dnfexpertjob.DisjointerType {
			config, ok := jobCatalog.Disjointer()
			if !ok {
				return dnfexpertjob.Changes{}, dnfexpertjob.ErrJobUnsupported
			}
			grade = int(currentExpertJobStatDefault(assets.Character.Stats, currentExpertJobMachineGradeStat, 1))
			rule, configured = config.RepairRule(grade)
			initial = config.InitialEndurance
		} else {
			config, ok := jobCatalog.Enchanter()
			if !ok {
				return dnfexpertjob.Changes{}, dnfexpertjob.ErrJobUnsupported
			}
			level := config.Recipes.Level(experience)
			if level > len(config.RepairRules) {
				level = len(config.RepairRules)
			}
			if level > 0 {
				rule, configured = config.RepairRules[level-1], true
			}
			initial = config.InitialEndurance
		}
		if !configured {
			return dnfexpertjob.Changes{}, dnfexpertjob.ErrMachineInvalid
		}
		gold, goldErr := s.currentExpertJobWalletGold(assets.Character, assets.Inventory)
		if goldErr != nil {
			return dnfexpertjob.Changes{}, goldErr
		}
		current := currentExpertJobStatDefault(assets.Character.Stats, currentExpertJobMachineEnduranceStat, initial)
		plan, planErr := dnfexpertjob.PlanMachineRepair(gold, current, rule)
		if planErr != nil {
			return dnfexpertjob.Changes{}, planErr
		}
		s.currentExpertJobSetWalletGold(assets.Character, assets.Inventory, plan.Gold)
		if assets.Character.Stats == nil {
			assets.Character.Stats = make(map[string]int64)
		}
		assets.Character.Stats[currentExpertJobMachineEnduranceStat] = plan.Endurance
		result, resultJobType = plan, jobType
		return dnfexpertjob.Changes{Character: true, Inventory: true}, nil
	}})
	return result, resultJobType, err
}

func (s *Service) handleCurrentExpertJobStoreUpgrade(session *gameSession, opcode uint16, body []byte) error {
	if len(body) != 0 {
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	s.expertJobStoreOpMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	result, err := s.commitCurrentDisjointerMachineUpgrade(ctx, session)
	cancel()
	s.expertJobStoreOpMu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-expert-job-machine-upgrade-rejected", "char_id", sessionSelectedCharacterID(session), "reason", err)
		return s.sendGameUpperFailure(session, opcode, currentExpertJobUpgradeError(err))
	}
	w := packetWriter{}
	w.writeByte(1)
	w.writeInt32(int(result.Gold))
	w.writeInt32(result.Grade)
	w.writeInt32(int(result.Endurance))
	if err := s.sendGameUpperRawClass(session, opcode, w.bytes(), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	return s.sendCurrentExpertJobInfoFromRepository(session, dnfexpertjob.DisjointerType, true)
}

func (s *Service) commitCurrentDisjointerMachineUpgrade(ctx context.Context, session *gameSession) (dnfexpertjob.DisjointerUpgradePlan, error) {
	jobCatalog, _, owner, accountID, characterID, err := s.currentExpertJobMutationContext(session)
	if err != nil || currentExpertJobSessionInDungeon(session) {
		return dnfexpertjob.DisjointerUpgradePlan{}, dnfexpertjob.ErrMachineInvalid
	}
	s.expertJobStoreMu.Lock()
	_, busy := s.expertJobStores[session.selectedCharacterID]
	s.expertJobStoreMu.Unlock()
	if busy {
		return dnfexpertjob.DisjointerUpgradePlan{}, dnfexpertjob.ErrMachineInvalid
	}
	var result dnfexpertjob.DisjointerUpgradePlan
	err = owner.Machine(ctx, dnfexpertjob.Command{AccountID: accountID, CharacterID: characterID, UpdatedAt: s.gameplayNow(), Project: func(assets *dnfexpertjob.Assets) (dnfexpertjob.Changes, error) {
		jobType, experience, stateErr := currentExpertJobCharacterState(assets.Character)
		if stateErr != nil || jobType != dnfexpertjob.DisjointerType {
			return dnfexpertjob.Changes{}, dnfexpertjob.ErrJobUnsupported
		}
		config, ok := jobCatalog.Disjointer()
		if !ok {
			return dnfexpertjob.Changes{}, dnfexpertjob.ErrJobUnsupported
		}
		gold, goldErr := s.currentExpertJobWalletGold(assets.Character, assets.Inventory)
		if goldErr != nil {
			return dnfexpertjob.Changes{}, goldErr
		}
		grade := int(currentExpertJobStatDefault(assets.Character.Stats, currentExpertJobMachineGradeStat, 1))
		endurance := currentExpertJobStatDefault(assets.Character.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance)
		plan, planErr := config.PlanUpgrade(gold, experience, endurance, grade, assets.Character.Level)
		if planErr != nil {
			return dnfexpertjob.Changes{}, planErr
		}
		s.currentExpertJobSetWalletGold(assets.Character, assets.Inventory, plan.Gold)
		if assets.Character.Stats == nil {
			assets.Character.Stats = make(map[string]int64)
		}
		assets.Character.Stats[currentExpertJobMachineGradeStat], assets.Character.Stats[currentExpertJobMachineEnduranceStat] = int64(plan.Grade), plan.Endurance
		result = plan
		return dnfexpertjob.Changes{Character: true, Inventory: true}, nil
	}})
	return result, err
}

// currentExpertJobWalletGold / currentExpertJobSetWalletGold 已迁到
// expert_job_gold_source.go：金币口径改成"读 Character.Stats["gold]（= dnf_characters.gold）"
// 并挂上 DNFBRIDGE_EXPERT_JOB_GOLD_SOURCE 开关（默认修正后行为，
// `inventory_wallet` 一键回退到改动前的背包伪槽读法）。文件头有香港现场与 DB 证据。

func currentExpertJobRepairError(err error) byte {
	if errors.Is(err, dnfexpertjob.ErrMachineInvalid) {
		return 60
	}
	return 22
}

func currentExpertJobUpgradeError(err error) byte {
	if errors.Is(err, dnfexpertjob.ErrLevelTooLow) {
		return 14
	}
	if errors.Is(err, dnfexpertjob.ErrInsufficientGold) {
		return 22
	}
	return 19
}

func currentExpertJobStoreStringContainsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func currentExpertJobStoreIntContains(values []int64, needle int64) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func (s *Service) handleCurrentExpertJobStoreDisjoint(session *gameSession, opcode uint16, body []byte) error {
	requests, err := parseCurrentExpertJobStoreDisjointBatch(body)
	if err != nil {
		s.logGameEvent(session, "game-expert-job-store-disjoint-rejected", "reason", "invalid_request", "error", err, "body_len", len(body))
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	request := requests[0]
	s.expertJobStoreOpMu.Lock()
	store, ok := s.currentExpertJobEnteredStore(session, request.OwnerID, currentExpertJobDisjointStoreKind)
	if !ok && s.options.currentClientVersion == "official-24.9" {
		store, err = s.currentPersonalDisjointerContext(session, request.OwnerID)
		ok = err == nil && store != nil
	}
	if !ok {
		s.expertJobStoreOpMu.Unlock()
		s.logGameEvent(session, "game-expert-job-store-disjoint-rejected", "reason", "store_or_personal_context_missing", "owner_id", request.OwnerID, "slot", request.TargetSlot, "error", err)
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	result, err := s.commitCurrentExpertJobStoreDisjointBatch(ctx, session, store, requests)
	cancel()
	if err == nil {
		s.updateCurrentExpertJobStoreMachine(store, result.Endurance, store.MachineGrade)
	}
	s.expertJobStoreOpMu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-expert-job-store-disjoint-rejected", "reason", "asset_transaction_rejected", "owner_id", request.OwnerID, "slot", request.TargetSlot, "count", len(requests), "error", err)
		return s.sendGameUpperFailure(session, opcode, currentExpertJobStoreError(err))
	}
	if err := s.sendGameUpperRawClass(session, opcode, buildCurrentExpertJobStoreDisjointSuccessBody(request, result), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	if s.options.currentClientVersion == "official-24.9" && len(requests) > 1 {
		// The aggregate ACK names only the first native source slot. Clear all
		// consumed slots in one repository-backed op14 before rebuilding the
		// containers, so the client cannot retain staged equipment ghosts.
		refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(requests))
		for _, consumed := range requests {
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: consumed.TargetSlot})
		}
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "expert_job_batch_consumed_slots_after_ack", refreshes); err != nil {
			return err
		}
	}
	if err := s.sendSelectedCurrentContainerListsRefresh(session, "expert_job_store_disjoint_after_ack"); err != nil {
		return err
	}
	if store.OwnerSession != nil {
		if store.OwnerSession != session {
			// Native NOTI203 (registered at 1992C42) consumes success/u32
			// absolute wallet/u32 endurance. Its 167F360 call increments the
			// store income counter by the configured charge. A generic op14
			// wallet refresh alone cannot update that counter.
			owner := packetWriter{}
			owner.writeByte(1)
			owner.writeUint32(uint32(result.OwnerGold))
			owner.writeUint32(uint32(result.Endurance))
			// Native 203 increments the owner's income by one configured fee.
			// Each absolute-wallet notification accounts for one paid item;
			// requester rewards are still sent only once for the whole batch.
			for range requests {
				_ = s.sendGameUpperRawClass(store.OwnerSession, currentExpertJobDisjointOwnerNotification, owner.bytes(), 0)
			}
			_ = s.sendGameUpperRawClass(
				store.OwnerSession,
				uint16(dnfenum.CmdPacketWalkoutPartyMember),
				buildCurrentGoldStateBody(result.OwnerGold),
				0,
			)
		}
		_ = s.sendCurrentExpertJobInfoFromRepository(store.OwnerSession, dnfexpertjob.DisjointerType, true)
	}
	fee := int64(0)
	if session.selectedCharacterID != store.OwnerCharacterID {
		fee = store.Cost * int64(len(requests))
	}
	s.logGameEvent(session, "game-expert-job-store-disjoint-committed", "owner_id", store.OwnerCharacterID, "requester_id", session.selectedCharacterID, "count", len(requests), "fee", fee, "owner_gold", result.OwnerGold, "requester_gold", result.RequesterGold, "experience_gain", result.Experience, "endurance", result.Endurance)
	return nil
}

func (s *Service) commitCurrentExpertJobStoreDisjoint(ctx context.Context, session *gameSession, store *currentExpertJobStore, request currentExpertJobStoreDisjointRequest) (currentExpertJobStoreUseResult, error) {
	return s.commitCurrentExpertJobStoreDisjointBatch(ctx, session, store, []currentExpertJobStoreDisjointRequest{request})
}

func (s *Service) commitCurrentExpertJobStoreDisjointBatch(ctx context.Context, session *gameSession, store *currentExpertJobStore, requests []currentExpertJobStoreDisjointRequest) (currentExpertJobStoreUseResult, error) {
	if len(requests) == 0 || len(requests) > currentDisjointBatchMaxItems {
		return currentExpertJobStoreUseResult{}, dnfexpertjob.ErrExtractionInvalid
	}
	jobCatalog, err := s.currentExpertJobCatalog()
	if err != nil {
		return currentExpertJobStoreUseResult{}, err
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return currentExpertJobStoreUseResult{}, err
	}
	var result currentExpertJobStoreUseResult
	err = s.mutateCurrentExpertJobStoreAssets(ctx, session, store, true, func(requesterCharacter *dnfrepo.CharacterRecord, requesterInventory *dnfrepo.InventoryRecord, ownerCharacter *dnfrepo.CharacterRecord, ownerInventory *dnfrepo.InventoryRecord, warehouse *dnfrepo.AccountInventoryRecord) error {
		expectedEndurance := store.Endurance
		for _, request := range requests {
			if request.OwnerID != store.OwnerCharacterID || request.TargetList != 0 || request.TargetSlot < 0 {
				return dnfexpertjob.ErrExtractionInvalid
			}
			jobType, experience, stateErr := currentExpertJobCharacterState(ownerCharacter)
			if stateErr != nil || jobType != dnfexpertjob.DisjointerType {
				return dnfexpertjob.ErrJobUnsupported
			}
			config, configured := jobCatalog.Disjointer()
			if !configured {
				return dnfexpertjob.ErrJobUnsupported
			}
			grade := int(currentExpertJobStatDefault(ownerCharacter.Stats, currentExpertJobMachineGradeStat, 1))
			endurance := currentExpertJobStatDefault(ownerCharacter.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance)
			if grade != store.MachineGrade || endurance != expectedEndurance {
				return dnfexpertjob.ErrMachineInvalid
			}
			targetKey := currentCeraShopInventorySlotKey(request.TargetList, request.TargetSlot)
			target, found := requesterInventory.Slots[targetKey]
			if !found || target.ItemID <= 0 || target.ItemID > math.MaxUint32 || target.Count != 1 || currentNPCShopItemLocked(target) {
				return dnfexpertjob.ErrExtractionInvalid
			}
			metadata, metadataErr := jobCatalog.Equipment(target.ItemID)
			if metadataErr != nil || metadata.DisjointForbidden || metadata.AttachType == "trade delete" {
				return dnfexpertjob.ErrExtractionInvalid
			}
			metadata.State = currentExpertJobEquipmentState(target)
			plan, planErr := jobCatalog.PlanDisjointer(experience, grade, metadata, requesterCharacter.CharacterID == ownerCharacter.CharacterID, rand.IntN)
			if planErr != nil {
				return planErr
			}
			if endurance < plan.EnduranceReduction {
				return dnfexpertjob.ErrMachineEndurance
			}
			requesterGold, ownerGold, feeErr := s.currentExpertJobSettleStoreFee(requesterCharacter, requesterInventory, ownerCharacter, ownerInventory, store.Cost)
			if feeErr != nil {
				return feeErr
			}
			delete(requesterInventory.Slots, targetKey)
			rewards := make([]currentDisjointReward, 0, len(plan.Materials)+1)
			for _, reward := range plan.Materials {
				if reward.ItemID <= 0 || reward.ItemID > math.MaxUint32 || reward.Count <= 0 || reward.Count > math.MaxUint32 {
					return dnfexpertjob.ErrExtractionInvalid
				}
				currentDisjointMergeReward(&rewards, currentDisjointReward{ItemID: uint32(reward.ItemID), Count: uint32(reward.Count)})
			}
			// Machine cube/big-win rules stay in expertjob. Soul rules come from
			// the same runtime PVF as town disassembly, rather than a legacy table.
			definition, err := items.ResolveItem(uint32(target.ItemID))
			if err != nil {
				return err
			}
			document, err := parseDungeonCardPVFDocument(items.source, definition.PVFPath)
			if err != nil {
				return err
			}
			townConfig, err := loadCurrentDisjointPVFConfig(items.source)
			if err != nil {
				return err
			}
			townRewards, err := currentEquipmentDisjointRewards(townConfig, document)
			if err != nil {
				return err
			}
			for _, reward := range townRewards {
				if slot, soul := currentDisjointWarehouseFixedSlots[reward.ItemID]; soul && slot >= 360 {
					currentDisjointMergeReward(&rewards, reward)
				}
			}
			rows, _, err := currentGrantDisjointRewards(requesterInventory, warehouse, items, rewards)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if err := mergeCurrentExpertJobBatchMaterial(&result.Materials, currentExpertJobExtractionMaterial{Slot: row.Slot, ItemID: row.ItemID, Count: row.Granted}); err != nil {
					return err
				}
			}
			if ownerCharacter.Stats == nil {
				ownerCharacter.Stats = make(map[string]int64)
			}
			ownerCharacter.Stats["expert_job_exp"] = plan.FinalExperience
			ownerCharacter.Stats[currentExpertJobMachineEnduranceStat] = endurance - plan.EnduranceReduction
			result.RequesterGold, result.OwnerGold, result.Endurance = requesterGold, ownerGold, endurance-plan.EnduranceReduction
			result.Experience += plan.ExperienceGain
			result.LevelChanged = result.LevelChanged || plan.LevelChanged
			expectedEndurance = result.Endurance
		}
		return nil
	})
	return result, err
}

func (s *Service) handleCurrentExpertJobStoreEnchant(session *gameSession, opcode uint16, body []byte) error {
	request, err := parseCurrentExpertJobStoreEnchantRequest(body)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	s.expertJobStoreOpMu.Lock()
	store, ok := s.currentExpertJobEnteredStore(session, request.OwnerID, currentExpertJobEnchantStoreKind)
	if !ok {
		s.expertJobStoreOpMu.Unlock()
		return s.sendGameUpperFailure(session, opcode, 19)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	result, err := s.commitCurrentExpertJobStoreEnchant(ctx, session, store, request)
	cancel()
	var updatedStore, closedStore *currentExpertJobStore
	qualificationsChanged := false
	if err == nil {
		s.expertJobStoreMu.Lock()
		live := s.expertJobStores[store.OwnerCharacterID]
		if live != nil && live.OwnerSession == store.OwnerSession {
			qualificationsChanged = !bytes.Equal(live.Qualifications, result.Qualifications)
			live.Endurance = result.Endurance
			live.Qualifications = append([]byte(nil), result.Qualifications...)
			updatedStore = cloneCurrentExpertJobStore(live)
		}
		s.expertJobStoreMu.Unlock()
		if result.Endurance == 0 {
			closedStore = s.removeCurrentExpertJobStore(store.OwnerCharacterID, store.OwnerSession)
		}
	}
	s.expertJobStoreOpMu.Unlock()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentExpertJobStoreError(err))
	}
	if err := s.sendGameUpperRawClass(session, opcode, buildCurrentExpertJobStoreEnchantSuccessBody(result), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	if err := s.sendSelectedCurrentContainerListsRefresh(session, "expert_job_store_enchant_after_ack"); err != nil {
		return err
	}
	if store.OwnerSession != nil && store.OwnerSession != session {
		owner := packetWriter{}
		owner.writeInt32(int(result.OwnerGold))
		owner.writeInt32(int(result.Endurance))
		_ = s.sendGameUpperRawClass(store.OwnerSession, currentExpertJobEnchantOwnerNotification, owner.bytes(), 0)
		_ = s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentGoldStateBody(result.RequesterGold), 0)
		_ = s.sendGameUpperRawClass(store.OwnerSession, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentGoldStateBody(result.OwnerGold), 0)
	}
	if store.OwnerSession != nil {
		_ = s.sendCurrentExpertJobInfoFromRepository(store.OwnerSession, dnfexpertjob.EnchanterType, true)
	}
	if qualificationsChanged && closedStore == nil && updatedStore != nil {
		s.broadcastCurrentExpertJobStoreUpdate(updatedStore)
	}
	if closedStore != nil {
		s.broadcastCurrentExpertJobStoreClose(closedStore, true)
	}
	return nil
}

func (s *Service) commitCurrentExpertJobStoreEnchant(ctx context.Context, session *gameSession, store *currentExpertJobStore, request currentExpertJobStoreEnchantRequest) (currentExpertJobStoreUseResult, error) {
	jobCatalog, err := s.currentExpertJobCatalog()
	if err != nil {
		return currentExpertJobStoreUseResult{}, err
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return currentExpertJobStoreUseResult{}, err
	}
	s.initialEquipmentMu.Lock()
	source, sourceErr := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if sourceErr != nil {
		return currentExpertJobStoreUseResult{}, sourceErr
	}
	var result currentExpertJobStoreUseResult
	err = s.mutateCurrentExpertJobStoreAssets(ctx, session, store, true, func(requesterCharacter *dnfrepo.CharacterRecord, requesterInventory *dnfrepo.InventoryRecord, ownerCharacter *dnfrepo.CharacterRecord, ownerInventory *dnfrepo.InventoryRecord, warehouse *dnfrepo.AccountInventoryRecord) error {
		jobType, experience, stateErr := currentExpertJobCharacterState(ownerCharacter)
		if stateErr != nil || jobType != dnfexpertjob.EnchanterType {
			return dnfexpertjob.ErrJobUnsupported
		}
		config, configured := jobCatalog.Enchanter()
		if !configured {
			return dnfexpertjob.ErrJobUnsupported
		}
		endurance := currentExpertJobStatDefault(ownerCharacter.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance)
		if endurance != store.Endurance {
			return dnfexpertjob.ErrMachineInvalid
		}
		targetKey := currentCeraShopInventorySlotKey(request.TargetList, request.TargetSlot)
		cardKey := currentCeraShopInventorySlotKey(request.CardList, request.CardSlot)
		target, targetFound := requesterInventory.Slots[targetKey]
		card, cardFound := requesterInventory.Slots[cardKey]
		if !targetFound || target.ItemID <= 0 || target.ItemID > math.MaxUint32 || target.Count != 1 || currentNPCShopItemLocked(target) || !cardFound || card.ItemID <= 0 || card.ItemID > math.MaxUint32 || card.Count <= 0 || currentNPCShopItemLocked(card) {
			return dnfexpertjob.ErrExtractionInvalid
		}
		plan, planErr := jobCatalog.PlanEnchanterStore(experience, request.RecipeID, card.ItemID, endurance, rand.IntN)
		if planErr != nil {
			return planErr
		}
		resolution, resolutionErr := resolveCurrentEnchantCardMetadata(items, source, card.ItemID, target.ItemID)
		if resolutionErr != nil {
			return resolutionErr
		}
		upgradeCount := currentExpertJobEnchantUpgradeCount(card)
		if resolution.CardItemID != card.ItemID || resolution.TargetKind != string(dungeonDropItemEquipment) || !currentExpertJobStoreStringContainsFold(resolution.AllowedEquipmentTypes, resolution.TargetEquipmentType) || (len(resolution.UpgradeCounts) > 0 && !currentExpertJobStoreIntContains(resolution.UpgradeCounts, int64(upgradeCount))) || (len(resolution.UpgradeCounts) == 0 && upgradeCount != 0) {
			return dnfexpertjob.ErrExtractionInvalid
		}
		// The requester supplies the card and recipe materials. Consume both
		// within the same owner transaction, including fixed account slots.
		if _, consumeErr := s.consumeCurrentRecipeMaterials(&dnfexpertjob.Assets{
			Character: requesterCharacter, Inventory: requesterInventory, AccountInventory: warehouse,
		}, plan.Recipe.Materials, 0); consumeErr != nil {
			return consumeErr
		}
		requesterGold, ownerGold, feeErr := s.currentExpertJobSettleStoreFee(requesterCharacter, requesterInventory, ownerCharacter, ownerInventory, store.Cost)
		if feeErr != nil {
			return feeErr
		}
		if plan.Success {
			currentExpertJobSetEnchantFields(&target, card.ItemID, upgradeCount)
			requesterInventory.Slots[targetKey] = target
		}
		card.Count--
		if card.Count == 0 {
			delete(requesterInventory.Slots, cardKey)
		} else {
			entry := currentItemListEntryFromStack(request.CardList, request.CardSlot, card)
			card.RawEntry = append([]byte(nil), entry.data[:]...)
			requesterInventory.Slots[cardKey] = card
		}
		if ownerCharacter.Stats == nil {
			ownerCharacter.Stats = make(map[string]int64)
		}
		ownerCharacter.Stats["expert_job_exp"] = plan.FinalExperience
		ownerCharacter.Stats[currentExpertJobMachineEnduranceStat] = endurance - plan.EnduranceReduction
		result = currentExpertJobStoreUseResult{RequesterGold: requesterGold, OwnerGold: ownerGold, Endurance: endurance - plan.EnduranceReduction, Experience: plan.FinalExperience, LevelChanged: plan.LevelChanged, Success: plan.Success, TargetList: request.TargetList, TargetSlot: request.TargetSlot, Qualifications: config.Qualifications(plan.FinalExperience)}
		return nil
	})
	return result, err
}

func (s *Service) currentExpertJobEnteredStore(session *gameSession, ownerID uint16, kind byte) (*currentExpertJobStore, bool) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || currentExpertJobSessionInDungeon(session) || s.onlinePlayers == nil {
		return nil, false
	}
	visitor, visitorOK := s.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
	s.expertJobStoreMu.Lock()
	defer s.expertJobStoreMu.Unlock()
	store := s.expertJobStores[ownerID]
	if !visitorOK || visitor.Session != session || s.expertJobVisitors[session] != ownerID || store == nil || store.Kind != kind || store.OwnerSession == nil || store.TownID != visitor.TownID || store.AreaID != visitor.AreaID {
		return nil, false
	}
	return cloneCurrentExpertJobStore(store), true
}

func (s *Service) updateCurrentExpertJobStoreMachine(snapshot *currentExpertJobStore, endurance int64, grade int) {
	if s == nil || snapshot == nil {
		return
	}
	s.expertJobStoreMu.Lock()
	defer s.expertJobStoreMu.Unlock()
	store := s.expertJobStores[snapshot.OwnerCharacterID]
	if store != nil && store.OwnerSession == snapshot.OwnerSession {
		store.Endurance, store.MachineGrade = endurance, grade
	}
}

func buildCurrentExpertJobStoreDisjointSuccessBody(request currentExpertJobStoreDisjointRequest, result currentExpertJobStoreUseResult) []byte {
	w := packetWriter{}
	w.writeByte(1)
	w.writeUint16(uint16(request.TargetSlot))
	w.writeByte(request.TargetList)
	w.writeUint16(request.OwnerID)
	w.writeByte(byte(len(result.Materials)))
	for _, material := range result.Materials {
		w.writeUint16(uint16(material.Slot))
		w.writeUint32(material.ItemID)
		w.writeUint32(material.Count)
	}
	w.writeUint32(uint32(result.RequesterGold))
	w.writeUint32(uint32(result.Endurance))
	return w.bytes()
}

func buildCurrentExpertJobStoreEnchantSuccessBody(result currentExpertJobStoreUseResult) []byte {
	w := packetWriter{}
	w.writeByte(1)
	if result.Success {
		w.writeByte(1)
	} else {
		w.writeByte(0)
	}
	w.writeUint32(uint32(result.Experience))
	w.writeByte(0)
	w.writeUint32(uint32(result.Endurance))
	return w.bytes()
}

func (s *Service) mutateCurrentExpertJobStorePair(ctx context.Context, session *gameSession, store *currentExpertJobStore, apply func(*dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord, *dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord) error) error {
	if apply == nil {
		return dnfexpertjob.ErrOwnerUnavailable
	}
	return s.mutateCurrentExpertJobStoreAssets(ctx, session, store, false, func(rc *dnfrepo.CharacterRecord, ri *dnfrepo.InventoryRecord, oc *dnfrepo.CharacterRecord, oi *dnfrepo.InventoryRecord, _ *dnfrepo.AccountInventoryRecord) error {
		return apply(rc, ri, oc, oi)
	})
}

func (s *Service) mutateCurrentExpertJobStoreAssets(ctx context.Context, requesterSession *gameSession, store *currentExpertJobStore, withWarehouse bool, apply func(*dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord, *dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord, *dnfrepo.AccountInventoryRecord) error) error {
	if s == nil || requesterSession == nil || store == nil || apply == nil || requesterSession.selectedCharacterID == 0 {
		return dnfexpertjob.ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return dnfexpertjob.ErrOwnerUnavailable
	}
	requesterID := strconv.FormatUint(uint64(requesterSession.selectedCharacterID), 10)
	ownerID := strconv.FormatUint(uint64(store.OwnerCharacterID), 10)
	if requesterID == ownerID {
		owner, ownerErr := dnfexpertjob.NewOwner(repositories)
		if ownerErr != nil {
			return ownerErr
		}
		accountID := strings.TrimSpace(s.accountIDForSession(requesterSession))
		return owner.Machine(ctx, dnfexpertjob.Command{AccountID: accountID, CharacterID: requesterID, UpdatedAt: s.gameplayNow(), Project: func(assets *dnfexpertjob.Assets) (dnfexpertjob.Changes, error) {
			beforeWarehouse := dnfrepo.CloneAccountInventory(*assets.AccountInventory)
			if err := apply(assets.Character, assets.Inventory, assets.Character, assets.Inventory, assets.AccountInventory); err != nil {
				return dnfexpertjob.Changes{}, err
			}
			return dnfexpertjob.Changes{Character: true, Inventory: true, AccountInventory: withWarehouse && currentExpertJobStoreWarehouseChanged(beforeWarehouse, *assets.AccountInventory)}, nil
		}})
	}
	if repositories.CharacterTrade == nil {
		return dnfrepo.ErrCharacterTradeTransactionUnavailable
	}
	run := func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository, accounts dnfrepo.AccountInventoryRepository) error {
		ids := []string{requesterID, ownerID}
		if ids[1] < ids[0] {
			ids[0], ids[1] = ids[1], ids[0]
		}
		characterRows := make(map[string]dnfrepo.CharacterRecord, 2)
		inventoryRows := make(map[string]dnfrepo.InventoryRecord, 2)
		for _, id := range ids {
			character, found, err := characters.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || character.CharacterID != id {
				return dnfexpertjob.ErrCharacterNotFound
			}
			inventory, found, err := inventories.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || inventory.CharacterID != id {
				return dnfexpertjob.ErrInventoryNotFound
			}
			// A persisted empty inventory is valid; clone/save may encode it
			// as nil. The source item is still checked inside the transaction.
			if inventory.Slots == nil {
				inventory.Slots = make(map[string]dnfrepo.ItemStack)
			}
			characterRows[id], inventoryRows[id] = dnfrepo.CloneCharacter(character), dnfrepo.CloneInventory(inventory)
		}
		requesterCharacter, requesterInventory := characterRows[requesterID], inventoryRows[requesterID]
		ownerCharacter, ownerInventory := characterRows[ownerID], inventoryRows[ownerID]
		var warehouse *dnfrepo.AccountInventoryRecord
		if withWarehouse {
			if accounts == nil {
				return dnfrepo.ErrAccountCharacterAssetTransactionUnavailable
			}
			accountID := strings.TrimSpace(requesterCharacter.AccountID)
			record, found, err := accounts.Load(ctx, accountID)
			if err != nil {
				return err
			}
			if !found {
				record = dnfrepo.AccountInventoryRecord{AccountID: accountID}
			}
			record = dnfrepo.CloneAccountInventory(record)
			if record.Slots == nil {
				record.Slots = make(map[string]dnfrepo.ItemStack)
			}
			warehouse = &record
		}
		var beforeWarehouse dnfrepo.AccountInventoryRecord
		if warehouse != nil {
			beforeWarehouse = dnfrepo.CloneAccountInventory(*warehouse)
		}
		if err := apply(&requesterCharacter, &requesterInventory, &ownerCharacter, &ownerInventory, warehouse); err != nil {
			return err
		}
		characterRows[requesterID], inventoryRows[requesterID], characterRows[ownerID], inventoryRows[ownerID] = requesterCharacter, requesterInventory, ownerCharacter, ownerInventory
		now := s.gameplayNow()
		for _, id := range ids {
			character := characterRows[id]
			character.UpdatedAt = now
			if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
			inventory := inventoryRows[id]
			inventory.UpdatedAt = now
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		// Loading shared assets must not rewrite unrelated warehouse rows.
		if warehouse != nil && currentExpertJobStoreWarehouseChanged(beforeWarehouse, *warehouse) {
			warehouse.UpdatedAt = now
			if err := accounts.Save(ctx, *warehouse); err != nil {
				return err
			}
		}
		return nil
	}
	if withWarehouse {
		unit, ok := repositories.CharacterTrade.(dnfrepo.CharacterTradeAssetsUnitOfWork)
		if !ok {
			return dnfrepo.ErrAccountCharacterAssetTransactionUnavailable
		}
		return unit.WithinCharacterTradeAssets(ctx, requesterID, ownerID, run)
	}
	return repositories.CharacterTrade.WithinCharacterTrade(ctx, requesterID, ownerID, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository) error { return run(c, i, nil) })
}

func currentExpertJobSetEnchantFields(stack *dnfrepo.ItemStack, cardItemID int64, upgradeCount byte) {
	if stack == nil {
		return
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 4)
	}
	stack.Extra["value_a"], stack.Extra["enchant_card_id"] = strconv.FormatInt(cardItemID, 10), strconv.FormatInt(cardItemID, 10)
	stack.Extra["byte_12"], stack.Extra["enchant_upgrade_count"] = strconv.Itoa(int(upgradeCount)), strconv.Itoa(int(upgradeCount))
	if len(stack.RawEntry) == currentItemListEntryWireSize {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		value := uint32(cardItemID)
		binary.LittleEndian.PutUint32(stack.RawEntry[0x0E:0x12], value)
		stack.RawEntry[0x12] = upgradeCount
	}
}

// Repository cloning can normalize an empty slot map to nil. Neither form
// represents a material change and neither should cause an account write.
func currentExpertJobStoreWarehouseChanged(before, after dnfrepo.AccountInventoryRecord) bool {
	if len(before.Slots) == 0 && len(after.Slots) == 0 {
		return false
	}
	return !reflect.DeepEqual(before.Slots, after.Slots)
}
