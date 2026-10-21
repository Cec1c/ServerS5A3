package dnfbridge

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// 本文件实现「全队 op37 finish-loading 到齐之后再统一放行」这一条读条屏障
// —— 厂商自己的事件名就叫 `party_dungeon_shared_loading_barrier`。
//
// # 现场缺陷（操作者 2026-09-25 21:07 双开组队进图）
//
// "国服正常情况下双方的加载条是同步的（两个人都加载完才一起进图）"，这个服务器
// 读条"先不显示、然后一下跳到满"。客户端 trace 直接给出两个人**进图时刻差了 1.72 秒**：
//
//	pid=43840（队员 char4）21:07:50.811 lua enter_dungeon confirmed packet_len=20 key=4
//	pid=31416（队长 char3）21:07:52.531 lua enter_dungeon confirmed packet_len=20 key=3
//
// 服务端侧（`/opt/dnf90/logs/packet_log.txt`）两个人是**各自请求各自放行**：
//
//	:2636 game-upper-finish-loading-status-send conn=game-000006（队长 op37 请求）
//	:2639 game-main-finish-loading-state-send  conn=game-000006 source=request37_after_upper_status
//	:2671 game-upper-finish-loading-status-send conn=game-000003（队员 op37 请求）
//	:2674 game-main-finish-loading-state-send  conn=game-000003 source=request37_after_upper_status
//
// 也就是说：谁先加载完谁先进图，先到的人不会等队友。
//
// # 厂商对照（只读引用 D:\DNF90-test\server\runtime\logs\packet_log.txt）
//
// 进场那一次（`:5388` barrier 放行 op29 之后）**没有任何**
// `game-upper-post-start-map-player-placement-send` / `-finished`
// —— 组队副本里落位（op120/op3 那一批）被压到了读条屏障之后：
//
//	:5460 10:23:13.9624067 game-party-dungeon-loading-ready conn=game-000059 slot=0 cycle=1
//	      first=true release=false                     ← 队长先加载完，只回 ACK，不放行
//	:5468 10:23:14.0559351 game-party-dungeon-loading-ready conn=game-000061 slot=1 cycle=1
//	      first=true release=true                      ← 最后一名到齐
//	:5475 10:23:14.057717  game-main-finish-loading-state-send conn=game-000061
//	      source=party_dungeon_shared_loading_barrier
//	:5490 10:23:14.0607976 game-party-dungeon-loading-released conn=game-000061 char_id=2
//	      local_slot=1 loading_cycle=1
//	      packet_order=op120-each-stable-slot-op3-all-op37-op30-op19-op14-regis…
//	:5497 10:23:14.0618024 game-main-finish-loading-state-send conn=game-000059
//	      source=party_dungeon_shared_loading_barrier
//	:5513 10:23:14.0643165 game-party-dungeon-loading-released conn=game-000059 char_id=1
//
// 关键点（同一窗口里逐行核对过）：
//   - 两个人的 op37 请求（`:5457` conn59、`:5465` conn61）**都早于**自己的落位包
//     （conn61 的 `msg_id=120 ×2 / msg_id=3` 在 `:5477`-`:5480`，conn59 的在
//     `:5492`-`:5495`）。⇒ 客户端不需要先收 op120/op3 才会发 op37，屏障不会死锁。
//   - 只有**最后一名**成员到齐（`release=true`）之后，两个人才在 ~6ms 内一起拿到
//     `op120 ×2 → op3 → op37 主状态 → op30 → op19 → op1369 → op42`。
//
// # 我们的落地口径
//
//   - 组队副本（≥2 人 + 会话有活动副本运行时）里收到 op37 时：照旧立刻回
//     classification=1 的 ACK（`game-upper-finish-loading-status-send`），记一条
//     `game-party-dungeon-loading-ready`，但**不发** op37 主状态与后续；
//   - 全员到齐（或超时/掉线）后统一放行：先补被压住的落位
//     （`sendPendingPostStartMapPlayerPlacement`），再发
//     `sendCurrentFinishLoadingReleaseTail`（= 既有的 op37 主状态 + op30 + op19 +
//     post-finish 状态 + 队友 actor 补发），并记 `game-party-dungeon-loading-released`。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_LOADING_BARRIER`
//	  - `vendor`（默认，零值）= 厂商行为：组队副本的落位与 op37 主状态按全员到齐放行；
//	  - `legacy` = 改动前行为（落位紧跟 op29；op37 主状态按各自请求立即回）。
//
// 与既有开关的关系：本开关只改"组队副本里这两批包的**时机**"，不改任何 body、
// 不改 op36 fatigue / op9 房间槽位表 / op11 remotes 的位置与形状。
const partyDungeonLoadingBarrierEnv = "DNFBRIDGE_PARTY_DUNGEON_LOADING_BARRIER"

const (
	partyDungeonLoadingBarrierValueVendor = "vendor"
	partyDungeonLoadingBarrierValueLegacy = "legacy"
)

// partyDungeonLoadingBarrierTimeout 是屏障的**超时放行**上限。
//
// 放行路径写死在下面两处，绝不长时间卡人：
//   - `partyDungeonLoadingBarrierArrive` 在第一名成员到达时起一个 `time.AfterFunc`；
//   - `deferPartyDungeonPostStartMapPlacement` 在压住落位时也起一个（防止"谁都没发
//     op37"这种极端情况把落位永久压住）。
//
// 到期一律走 `partyDungeonLoadingBarrierForceRelease`：给**所有**成员补发被压住的落位
// （他们要靠 op120/op3 才能进图，进而才会发 op37），给**已经上报过 op37 的**成员补发
// op37 释放尾段。10s 远大于厂商实测的 0.09s～1.5s，也不至于让玩家真的干等。
var partyDungeonLoadingBarrierTimeout = 10 * time.Second

// partyDungeonLoadingBarrierRegroupWindow 是"刚放行完又来一次 op37"的合并窗口。
//
// 客户端有可能在同一次读条里补发第二条 op37（重传 / legacy 与 protected 两条边界）。
// 若第二次被当成一个**新周期**，队友那条迟迟不来就会白等一整个超时。放行后 2s 内的
// 重复到达直接按"已放行"处理，不重新组队。
var partyDungeonLoadingBarrierRegroupWindow = 2 * time.Second

// partyDungeonLoadingBarrierDispatch 决定"放行动作"怎么派发。
//
// 线上必须在**另一个 goroutine** 上跑：触发放行的会话自己正跑在它的事件循环里，
// 同步放行会 `callGameSession(自己)` ⇒ 事件循环自等（互等到超时）。
// 回归用例把它换成同步执行，避免 goroutine 竞态。
var partyDungeonLoadingBarrierDispatch = func(run func()) { go run() }

// partyDungeonLoadingBarrierMode 是 partyDungeonLoadingBarrierEnv 的归一化取值。
type partyDungeonLoadingBarrierMode int

const (
	// partyDungeonLoadingBarrierVendor 是默认档（零值）= 厂商行为。
	partyDungeonLoadingBarrierVendor partyDungeonLoadingBarrierMode = iota
	// partyDungeonLoadingBarrierLegacy = 改动前行为（逐字节回退）。
	partyDungeonLoadingBarrierLegacy
)

func (mode partyDungeonLoadingBarrierMode) String() string {
	if mode == partyDungeonLoadingBarrierLegacy {
		return partyDungeonLoadingBarrierValueLegacy
	}
	return partyDungeonLoadingBarrierValueVendor
}

func (mode partyDungeonLoadingBarrierMode) holds() bool {
	return mode != partyDungeonLoadingBarrierLegacy
}

// readPartyDungeonLoadingBarrierMode 解析 DNFBRIDGE_PARTY_DUNGEON_LOADING_BARRIER。
// **默认 vendor（= 厂商行为）**：空/未设置/非法值都是 vendor；只有显式写 `legacy` 才回退。
func readPartyDungeonLoadingBarrierMode(value string) partyDungeonLoadingBarrierMode {
	if strings.EqualFold(strings.TrimSpace(value), partyDungeonLoadingBarrierValueLegacy) {
		return partyDungeonLoadingBarrierLegacy
	}
	return partyDungeonLoadingBarrierVendor
}

// partyDungeonLoadingBarrier 返回当前 Service 的归一化档位（零值 options = vendor）。
func (s *Service) partyDungeonLoadingBarrier() partyDungeonLoadingBarrierMode {
	if s == nil {
		return partyDungeonLoadingBarrierVendor
	}
	return s.options.partyDungeonLoadingBarrier
}

// pendingPostStartMapPlacement 是"落位包已经该发、但被全员读条屏障压住"的记录。
// 记的是**当时的** runtime 指针，放行时原样复用。
type pendingPostStartMapPlacement struct {
	Runtime *runtimeDungeonState
	Source  string
}

// partyDungeonLoadingCycle 是一次"全队读条"的组队状态（每个队伍一条）。
type partyDungeonLoadingCycle struct {
	PartyID   uint16
	Cycle     int
	Ready     map[uint16]bool
	Released  bool
	released  time.Time
	Timer     *time.Timer
	membersAt []uint16
	// owners keeps the actual session, including a replacement socket.
	owners map[uint16]partyDungeonLoadingOwner
}

// A loading release is bound to the exact session and committed runtime it
// observed. Keeping the runtime alive does not make it the current scene.
type partyDungeonLoadingOwner struct {
	session *gameSession
	runtime *runtimeDungeonState
	token   uint64
}

func snapshotPartyDungeonLoadingOwner(session *gameSession) partyDungeonLoadingOwner {
	if session == nil {
		return partyDungeonLoadingOwner{}
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return partyDungeonLoadingOwner{session, session.dungeon.runtime, session.dungeon.runToken}
}

// Called before acquiring loadingMu: never acquire a dungeon lock beneath it.
func (s *Service) partyDungeonLoadingOwners(session *gameSession, members []uint16) map[uint16]partyDungeonLoadingOwner {
	owners := make(map[uint16]partyDungeonLoadingOwner, len(members))
	for _, id := range members {
		peer, _ := s.onlineGameSession(id)
		if id == session.selectedCharacterID {
			peer = session
		}
		if peer != nil {
			owners[id] = snapshotPartyDungeonLoadingOwner(peer)
		}
	}
	return owners
}

func (s *Service) ensurePartyDungeonLoadingCycleLocked(session *gameSession, partyID uint16, members []uint16, owners map[uint16]partyDungeonLoadingOwner) *partyDungeonLoadingCycle {
	cycles := s.partyDungeonLoadingBarrierCyclesLocked()
	current := cycles[partyID]
	owner := owners[session.selectedCharacterID]
	previous, known := partyDungeonLoadingOwner{}, false
	if current != nil {
		previous, known = current.owners[session.selectedCharacterID]
	}
	changed := known && previous != owner
	if current == nil || changed || (current.Released && time.Since(current.released) >= partyDungeonLoadingBarrierRegroupWindow) {
		next := 1
		ready := make(map[uint16]bool)
		if current != nil {
			next = current.Cycle + 1
			if current.Timer != nil {
				current.Timer.Stop()
			}
			// A peer can commit its replacement while another member's op37 is
			// already waiting. That unchanged member will not send op37 again.
			// Preserve only pending arrivals owned by the exact same scene;
			// released cycles and replaced owners must load afresh.
			if !current.Released {
				for _, id := range members {
					previousOwner, existed := current.owners[id]
					nextOwner, present := owners[id]
					if existed && present && nextOwner.runtime != nil && previousOwner == nextOwner && current.Ready[id] {
						ready[id] = true
					}
				}
			}
		}
		current = &partyDungeonLoadingCycle{PartyID: partyID, Cycle: next, Ready: ready, owners: owners}
		cycles[partyID] = current
	}
	if current.owners == nil {
		current.owners = make(map[uint16]partyDungeonLoadingOwner)
	}
	current.owners[session.selectedCharacterID] = owner
	current.membersAt = append([]uint16(nil), members...)
	if !current.Released && current.Timer == nil {
		expected := current
		current.Timer = time.AfterFunc(partyDungeonLoadingBarrierTimeout, func() {
			s.forceReleasePartyDungeonLoadingCycle(partyID, expected, "timeout_waiting_for_all_members_op37")
		})
	}
	return current
}

func (s *Service) currentPartyDungeonLoadingCycle(partyID uint16) *partyDungeonLoadingCycle {
	s.partyDungeonLoadingMu.Lock()
	defer s.partyDungeonLoadingMu.Unlock()
	return s.partyDungeonLoadingBarrierCyclesLocked()[partyID]
}

// partyDungeonPostLoadingRosterEnv 是 H3 的开关：**读条结束**（op37 主状态 + op30 + op19 +
// post-finish 状态 + 队友 actor 补发全部发完）之后，给刚进场景的这名成员**本人**再推一次
// 队伍名册（class0/op153 实时信息 + 自己那条 class0/op9 kind0 名册帧），
// 形状完全复用既有原语 `sendRuntimePartyRosterLocal`。
//
// # 现场缺口（2026-09-27，组队进图后**队员**客户端自己 op13 退队）
//
//	RECV cmd=1 type=13 ENUM_CMDPACKET_LEAVE_PARTY body_len=0
//	EVENT game-party-membership-removed source=client_leave_party_op13 char_id=4
//	      party_id_before=1 member_count_before=2 → member_count_after=1
//
// 退队前该会话收到的完整序列：`…309:37 310:30 311:19 312:120 313:2 314:2 315:9 → op13`。
// 逐包核对后的缺口是：名册只在**读条之前**推过（跟随者选择器转换 ①、start-map 提交尾段的
// 房间槽位表 ②、落位里的名册帧 ③），**读条结束之后一条 op153 都没有** —— 结尾那三条
// `313:2 314:2 315:9`（`game-dungeon-party-peer-actor-resend`）只描述**队友**的 actor，
// 不含实时名册。若客户端在"真正进入房间场景"这一刻用读条前那份名册重算队伍框，
// 就会与队友侧对不上。本开关按相位补上这一次重推（**只发给本人**，不 fan-out、不重发 op11）。
//
// 已排除的前两刀：H1（`…ENTRY_SLOT_TABLE=vendor_only`，实机仍退队 1 次）、
// 包级候选（owner 档 / 11+153 / 37+30+19 / 3 / 选择器 / 队友坐标）。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_POST_LOADING_ROSTER`
//	  - `off`（默认，零值）= 逐字节、逐包序与今天相同；
//	  - `on` / `1` / `true` / `yes` = 读条尾段之后给本人补一次 `op153 + 自己那条 op9`，
//	    并记 `game-dungeon-party-post-loading-roster-repush`（失败记 `-failed`，**绝不**影响放行）。
//
// 安全阀：只在"会话确实持有活动副本运行时 + 权威队伍 ≥2 人"时才推；单人与城镇一律惰性。
//
// 回退：把开关设回 `off`（或删掉该行）重启即可逐字节回到今天。
const partyDungeonPostLoadingRosterEnv = "DNFBRIDGE_PARTY_DUNGEON_POST_LOADING_ROSTER"

func partyDungeonPostLoadingRosterEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(partyDungeonPostLoadingRosterEnv))) {
	case "on", "1", "true", "yes":
		return true
	default:
		return false
	}
}

// repushDungeonPartyRosterAfterFinishLoading 是"读条结束后给本人补推一次名册"的落地。
//
// 默认 off ⇒ 立刻 return（零发包、零日志）。失败只记事件，不向上传播：放行尾段已经发出，
// 这条是**补推**，不能让它把读条放行的结果改写成失败。
func (s *Service) repushDungeonPartyRosterAfterFinishLoading(session *gameSession, source string) {
	if !partyDungeonPostLoadingRosterEnabled() {
		return
	}
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	if !s.sessionHasActiveDungeonRuntime(session) {
		return
	}
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return
	}
	if err := s.sendRuntimePartyRosterLocal(session, state, source+"_post_loading_roster"); err != nil {
		s.logGameEvent(session, "game-dungeon-party-post-loading-roster-repush-failed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"party_id", state.PartyID,
			"member_count", len(members),
			"switch", partyDungeonPostLoadingRosterEnv,
			"error", err)
		return
	}
	s.logGameEvent(session, "game-dungeon-party-post-loading-roster-repush",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"member_count", len(members),
		"switch", partyDungeonPostLoadingRosterEnv,
		"sequence", "op153_realtime_info_then_own_op9_party_record",
		"phase", "after_finish_loading_tail_before_the_client_rebuilds_its_party_frame",
		"reason", "the_loading_tail_only_reprojects_the_peer_actor_and_never_refreshes_the_realtime_roster",
		"scope", "receiver_only_no_fanout_no_op11")
}

// partyDungeonLoadingBarrierMembers 判断这次 op37 是否要进屏障，并给出**要等的成员**。
//
// 只等"在线且当前有活动副本运行时"的队友：队友掉线、或者人还在城镇（没有副本运行时）
// 时不能被算进集合，否则每人一发 op37 都要白等一个超时。
func (s *Service) partyDungeonLoadingBarrierMembers(session *gameSession) (uint16, []uint16, bool) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return 0, nil, false
	}
	if !s.partyDungeonLoadingBarrier().holds() {
		return 0, nil, false
	}
	if !currentDungeonSceneActive(session) {
		return 0, nil, false
	}
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return 0, nil, false
	}
	peerIDs := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		if member.UserID == session.selectedCharacterID {
			peerIDs = append(peerIDs, member.UserID)
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil || !currentDungeonSceneActive(peer) {
			continue
		}
		peerIDs = append(peerIDs, member.UserID)
	}
	if len(peerIDs) < 2 {
		return 0, nil, false
	}
	return uint16(state.PartyID), peerIDs, true
}

// partyDungeonLoadingBarrierCyclesLocked 返回屏障表（惰性建）。调用方须持锁。
func (s *Service) partyDungeonLoadingBarrierCyclesLocked() map[uint16]*partyDungeonLoadingCycle {
	if s.partyDungeonLoadingCycles == nil {
		s.partyDungeonLoadingCycles = make(map[uint16]*partyDungeonLoadingCycle)
	}
	return s.partyDungeonLoadingCycles
}

// partyDungeonLoadingBarrierArrive 记录一名成员到达读条屏障。
//
// 返回值：release = 是否由本次到达触发放行；first = 该成员在本周期内是否首次到达；
// cycle = 周期号（从 1 开始，与厂商 `cycle=1` 对齐）；slot = 该成员在队伍里的槽位。
func (s *Service) partyDungeonLoadingBarrierArrive(
	session *gameSession,
	partyID uint16,
	members []uint16,
) (release bool, first bool, cycle int, slot byte, expected *partyDungeonLoadingCycle) {
	if s == nil || session == nil || partyID == 0 {
		return true, false, 0, 0, nil
	}
	charID := session.selectedCharacterID
	if index, indexed := runtimePartyMemberIndex(charID, s.runtimePartyStateForSession(session)); indexed {
		slot = index
	}
	owners := s.partyDungeonLoadingOwners(session, members)
	s.partyDungeonLoadingMu.Lock()
	defer s.partyDungeonLoadingMu.Unlock()
	current := s.ensurePartyDungeonLoadingCycleLocked(session, partyID, members, owners)
	now := time.Now()
	if current.Released {
		// 合并窗口内的重复到达：按已放行处理，不再重新组队。
		return true, false, current.Cycle, slot, current
	}
	if !current.Ready[charID] {
		current.Ready[charID] = true
		first = true
	}
	for _, memberID := range members {
		if !current.Ready[memberID] {
			return false, first, current.Cycle, slot, current
		}
	}
	current.Released = true
	current.released = now
	if current.Timer != nil {
		current.Timer.Stop()
		current.Timer = nil
	}
	return true, first, current.Cycle, slot, current
}

// partyDungeonLoadingBarrierForceRelease 是超时/异常路径的统一放行入口。
func (s *Service) partyDungeonLoadingBarrierForceRelease(partyID uint16, reason string) {
	s.forceReleasePartyDungeonLoadingCycle(partyID, nil, reason)
}

func (s *Service) forceReleasePartyDungeonLoadingCycle(partyID uint16, expected *partyDungeonLoadingCycle, reason string) {
	if s == nil || partyID == 0 {
		return
	}
	s.partyDungeonLoadingMu.Lock()
	current := s.partyDungeonLoadingBarrierCyclesLocked()[partyID]
	if current == nil || current.Released || (expected != nil && current != expected) {
		s.partyDungeonLoadingMu.Unlock()
		return
	}
	current.Released = true
	current.released = time.Now()
	if current.Timer != nil {
		current.Timer.Stop()
		current.Timer = nil
	}
	members := append([]uint16(nil), current.membersAt...)
	ready := make([]uint16, 0, len(current.Ready))
	for charID := range current.Ready {
		ready = append(ready, charID)
	}
	s.partyDungeonLoadingMu.Unlock()
	// 超时路径：所有人补落位（他们要靠 op120/op3 才能进图、进而才会发 op37），
	// 只有**上报过 op37 的**成员才补 op37 释放尾段。
	s.releasePartyDungeonLoadingCycle(partyID, current, members, ready, reason)
}

// releasePartyDungeonLoadingCycle 给名单里的每个成员统一下发被压住的落位；
// 对 `releaseTailMembers` 里的人在落位之后补发 op37 释放尾段，并记
// `game-party-dungeon-loading-released`。
//
// 每个成员都在**自己的事件循环**上执行（`callGameSession`），所以从 goroutine
// 里调用是安全的：触发放行的那名成员，它自己的 `sendFinishLoadingStatus` 正跑在
// 它的事件循环上，会在调度完这个 goroutine 之后立刻返回。
func (s *Service) releasePartyDungeonLoadingCycle(
	partyID uint16,
	expected *partyDungeonLoadingCycle,
	members []uint16,
	releaseTailMembers []uint16,
	trigger string,
) {
	if s == nil || partyID == 0 || len(members) == 0 {
		return
	}
	releaseTail := make(map[uint16]bool, len(releaseTailMembers))
	for _, charID := range releaseTailMembers {
		releaseTail[charID] = true
	}
	// Freeze the owners of this exact cycle before queuing any session work.
	s.partyDungeonLoadingMu.Lock()
	if expected == nil || s.partyDungeonLoadingCycles[partyID] != expected {
		s.partyDungeonLoadingMu.Unlock()
		return
	}
	owners := make(map[uint16]partyDungeonLoadingOwner, len(expected.owners))
	for id, owner := range expected.owners {
		owners[id] = owner
	}
	s.partyDungeonLoadingMu.Unlock()
	for _, charID := range members {
		owner, known := owners[charID]
		if !known || owner.session == nil || owner.runtime == nil {
			continue
		}
		session := owner.session
		wantTail := releaseTail[charID]
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		var delivered atomic.Bool
		err := s.callGameSession(ctx, session, "runtime-party-dungeon-loading-release", func() error {
			if s.currentPartyDungeonLoadingCycle(partyID) != expected || snapshotPartyDungeonLoadingOwner(session) != owner {
				return nil
			}
			delivered.Store(true)
			if err := s.sendPendingPostStartMapPlayerPlacement(
				session,
				"party_dungeon_shared_loading_barrier",
			); err != nil {
				return err
			}
			if !wantTail {
				return nil
			}
			return s.sendCurrentFinishLoadingReleaseTail(session, "request37_after_upper_status")
		})
		cancel()
		if !delivered.Load() && err == nil {
			continue
		}
		slot := byte(0)
		if index, indexed := runtimePartyMemberIndex(charID, s.runtimePartyStateForSession(session)); indexed {
			slot = index
		}
		// 放行本身已经执行（落位与释放尾段都按上面的顺序试过了）；尾段里某一步失败时
		// 仍记同一条 `-released`，只是带上 error —— 单看日志就能分清"没放行"和
		// "放行了但后续某步失败"。
		fields := []any{
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"party_id", partyID,
			"char_id", charID,
			"local_slot", slot,
			"trigger", trigger,
			"release_tail", wantTail,
			"packet_order", "op120-each-stable-slot-op3-all-op37-op30-op19-op14",
			"reason", "vendor_releases_every_member_only_after_the_last_op37_arrived",
		}
		if err != nil {
			fields = append(fields, "error", err)
		}
		s.logGameEvent(session, "game-party-dungeon-loading-released", fields...)
	}
}

// deferPartyDungeonFinishLoadingForSharedBarrier 是 `sendFinishLoadingStatus` 的屏障钩子。
//
// 返回 true 表示"这次 op37 已经被屏障接管，调用方不要再自行下发主状态"。
// 组队副本以外（城镇 / 单人 / legacy 档）一律返回 false，行为与改动前逐字节相同。
func (s *Service) deferPartyDungeonFinishLoadingForSharedBarrier(session *gameSession) (bool, error) {
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(session)
	if !ok {
		return false, nil
	}
	release, first, cycle, slot, expected := s.partyDungeonLoadingBarrierArrive(session, partyID, members)
	s.logGameEvent(session, "game-party-dungeon-loading-ready",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"party_id", partyID,
		"char_id", session.selectedCharacterID,
		"slot", slot,
		"cycle", cycle,
		"first", first,
		"release", release,
		"member_count", len(members),
		"reason", "vendor_waits_for_every_member_op37_before_releasing_the_shared_loading_screen")
	if release {
		members := append([]uint16(nil), members...)
		partyDungeonLoadingBarrierDispatch(func() {
			s.releasePartyDungeonLoadingCycle(partyID, expected, members, members, "last_member_op37")
		})
	}
	return true, nil
}

// partyDungeonPostStartMapPlacementPending 报告这个会话的落位包是否正被屏障压住。
//
// `sendFinishLoadingStatus` 的 actor-ready 门闩要靠它放行：落位被压住时
// `postStartMapPlayerStateSent` 还是 false，但 actor 本身早已就绪。
func (s *Service) partyDungeonPostStartMapPlacementPending(session *gameSession) bool {
	if s == nil || session == nil {
		return false
	}
	if !s.partyDungeonLoadingBarrier().holds() {
		return false
	}
	session.pendingPostStartMapMu.Lock()
	pending := session.pendingPostStartMapPlacement != nil
	session.pendingPostStartMapMu.Unlock()
	return pending
}

// sendPendingPostStartMapPlayerPlacement 下发被屏障压住的落位包。
//
// 没有压住的记录时**什么都不做**（单人 / legacy / 已经发过）。
func (s *Service) sendPendingPostStartMapPlayerPlacement(session *gameSession, source string) error {
	if s == nil || session == nil {
		return nil
	}
	session.pendingPostStartMapMu.Lock()
	pending := session.pendingPostStartMapPlacement
	session.pendingPostStartMapPlacement = nil
	session.pendingPostStartMapMu.Unlock()
	if pending == nil {
		return nil
	}
	if pending.Runtime == nil || pending.Runtime.Session == nil {
		return nil
	}
	if snapshotPartyDungeonLoadingOwner(session).runtime != pending.Runtime {
		return nil
	}
	scene, ok := pending.Runtime.Session.Scene()
	if !ok {
		s.logGameEvent(session, "game-party-dungeon-post-start-map-placement-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "deferred_scene_unavailable_at_loading_barrier_release")
		return nil
	}
	s.logGameEvent(session, "game-party-dungeon-post-start-map-placement-released",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"deferred_source", pending.Source,
		"barrier", "party_dungeon_shared_loading_barrier")
	return s.sendCurrentPostStartMapPlayerPlacement(
		session,
		pending.Runtime,
		scene,
		"party_dungeon_shared_loading_barrier",
	)
}

// deferPartyDungeonPostStartMapPlacement 决定这一次落位要不要压到读条屏障之后。
//
// 返回 true 表示已压住（调用方不要再直接发送）。压住时会同时给本队起一个超时看门狗
// —— 万一没有任何成员发 op37，落位也不会被永久压住（见
// partyDungeonLoadingBarrierTimeout 的说明）。
func (s *Service) deferPartyDungeonPostStartMapPlacement(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) bool {
	if s == nil || session == nil {
		return false
	}
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(session)
	if !ok {
		return false
	}
	session.pendingPostStartMapMu.Lock()
	session.pendingPostStartMapPlacement = &pendingPostStartMapPlacement{
		Runtime: runtime,
		Source:  source,
	}
	session.pendingPostStartMapMu.Unlock()
	s.logGameEvent(session, "game-party-dungeon-post-start-map-placement-deferred",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", partyID,
		"barrier", "party_dungeon_shared_loading_barrier",
		"deferred_until", "every_party_member_op37_finish_loading",
		"reason", "vendor_sends_op120_op3_only_at_the_shared_loading_barrier_release")
	s.partyDungeonLoadingBarrierArmWatchdog(session, partyID, members)
	return true
}

// partyDungeonLoadingBarrierArmWatchdog 在没有成员到达时也能超时放行。
func (s *Service) partyDungeonLoadingBarrierArmWatchdog(session *gameSession, partyID uint16, members []uint16) {
	if s == nil || session == nil || partyID == 0 {
		return
	}
	owners := s.partyDungeonLoadingOwners(session, members)
	s.partyDungeonLoadingMu.Lock()
	defer s.partyDungeonLoadingMu.Unlock()
	s.ensurePartyDungeonLoadingCycleLocked(session, partyID, members, owners)
}
