package dnfbridge

import (
	"os"
	"strings"
)

// 本文件处理操作者第 3 条报障的**第二次进图**那一半：
//
//	「时装问题在返回城镇第二次进图后再次触发。」
//
// # 现场（packet_log.txt，2026-09-28，队长 char1 / 队员 char6 = 8 件稀有克隆装扮）
//
// 第一次进图（instance 1 / party 3，16:10:39）**两个方向都投影了**：
//
//	:148303 EVENT kind=game-party-peer-actor-appearance conn_id=game-000007
//	        source=party_dungeon_shared_loading_barrier_party_peer_copresence_peer_actor_to_self
//	        receiver_char_id=6 peer_char_id=1 ... appearance_subtype0_sent=false
//	:148320 EVENT kind=game-party-peer-actor-appearance conn_id=game-000003
//	        source=party_dungeon_shared_loading_barrier_party_peer_copresence_self_actor_to_peer
//	        receiver_char_id=1 peer_char_id=6 ... appearance_link_rows=8
//	        appearance_subtype0_sent=true appearance_subtype0_link_rows=8
//
// 第二次（instance 2 / party 4，16:11:4x）与第三次（instance 3 / party 5，16:13:1x）进图：
// **一条 `party_dungeon_shared_loading_barrier_*_peer_actor_to_self/self_actor_to_peer` 都没有**
// （全日志 appearance 事件里 16:10:40 之后只剩城镇与 finish-loading 两类）。同一个角色的
// 时装载体（op2 mode0 的 8 行克隆链接 + subtype0 外观列表）因此整批没发，
// 客户端当然还是默认时装 —— **开关确实 fire 过，是"这一次根本没轮到它发"**。
//
// # 成因（file:line，一条幂等集合跨世代残留）
//
// 队友 actor 投影是**每个 (会话, 队友) 只画一次**的幂等集合：
//
//	claimDungeonPeerProjection      dungeon_party_copresence.go:250  ← 命中就一个字节都不发
//	session.dungeonPeerProjected    game.go:231
//
// 唯一清空它的地方是"运行键变了"：
//
//	recordDungeonPartyRunKey        dungeon_party_copresence.go:76
//	  if session.dungeonPartyRunValid && session.dungeonPartyRun != key {
//	      session.dungeonPeerProjected = nil          ← 只有**上一条键还有效**时才清
//	  }
//
// 而"回城/解散"这条收口点把键**作废**（`dungeonPartyRunValid=false`），不清投影集合：
//
//	forgetDungeonPartyRunKey        dungeon_party_run_key_town_reset.go:90
//	  session.dungeonPartyRun = dungeonPartyRunKey{}
//	  session.dungeonPartyRunValid = false            ← 投影集合原样留着
//
// 现场那两次回城都是**放弃回城 ⇒ 解散**，走 `forgetRuntimePartyRunKeysOnDisband`
// （同文件 :139），日志逐行：
//
//	:149371/:149372 EVENT kind=game-party-dungeon-run-key-cleared conn_id=game-000003 / game-000007
//	        source=back_to_village_committed
//	        reason=the_runtime_party_was_disbanded_so_the_recorded_run_key_has_no_owner
//	:151192/:151193 同型（party 4）
//
// ⇒ 下一次进图时 `recordDungeonPartyRunKey` 看到的是"上一条键**已作废**"，
// `dungeonPartyRunValid &&` 那一半为假 ⇒ 投影集合**跨世代残留** ⇒
// `claimDungeonPeerProjection` 对同一名队友恒返回 false ⇒ 整场不再投影。
// 第一次进图（连接期内还没有作废过）因此正常，第二次起全部静默跳过 ——
// 与操作者"第二次进图才复发"完全一致。
//
// # 修法
//
// 让"投影世代"跟着运行键走：
//
//  1. 运行键被**作废**（回城失效 / 解散清键）时，同一个锁里把本会话的投影世代一起结束
//     （`dungeonPeerProjected` / `dungeonOwnerOverrideLogged` / `dungeonPeerActorResent`）；
//  2. `recordDungeonPartyRunKey` 在"上一条键已作废"时**也**结束上一世代——覆盖任何
//     绕过 (1) 的路径（例如换角色 / 断线重连后重新落位）。
//
// 只影响"下一次进图多发一批本来就该发的队友 actor 帧"，不新增任何机制、不改包形状。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_PEER_PROJECTION_GENERATION`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：作废/换代时结束上一投影世代；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（集合跨世代残留、
//	    不记本文件的事件）。
//
// 与同包既有开关一致的取值方式：每次调用读一次 env（不改 options.go）。
const dungeonPartyPeerProjectionGenerationEnv = "DNFBRIDGE_PARTY_DUNGEON_PEER_PROJECTION_GENERATION"

func dungeonPartyPeerProjectionGenerationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyPeerProjectionGenerationEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// dungeonPartyPeerProjectionGenerationMode 是判读事件里的档位串。
func dungeonPartyPeerProjectionGenerationMode() string {
	if dungeonPartyPeerProjectionGenerationEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// dungeonPeerProjectionGenerationCounts 是"结束这一世代时丢掉了多少条幂等记录"，
// 全部为 0 表示本来就干净（干净会话一个字段都不改、一行日志都不记）。
type dungeonPeerProjectionGenerationCounts struct {
	projected int
	overrides int
	resends   int
}

func (counts dungeonPeerProjectionGenerationCounts) empty() bool {
	return counts.projected == 0 && counts.overrides == 0 && counts.resends == 0
}

// dropDungeonPeerProjectionGenerationLocked 在**已持有** session.dungeonPartyRunMu 时
// 结束本会话的队友投影世代。调用方负责日志。
func dropDungeonPeerProjectionGenerationLocked(session *gameSession) dungeonPeerProjectionGenerationCounts {
	if session == nil {
		return dungeonPeerProjectionGenerationCounts{}
	}
	counts := dungeonPeerProjectionGenerationCounts{
		projected: len(session.dungeonPeerProjected),
		overrides: len(session.dungeonOwnerOverrideLogged),
		resends:   len(session.dungeonPeerActorResent),
	}
	session.dungeonPeerProjected = nil
	session.dungeonOwnerOverrideLogged = nil
	session.dungeonPeerActorResent = nil
	return counts
}

// resetDungeonPeerProjectionGeneration 结束本会话当前的队友投影世代（回城失效 /
// 解散清键 / 换角色时调用）。返回是否真的丢掉过记录。
func (s *Service) resetDungeonPeerProjectionGeneration(session *gameSession, source string) bool {
	if session == nil || !dungeonPartyPeerProjectionGenerationEnabled() {
		return false
	}
	session.dungeonPartyRunMu.Lock()
	counts := dropDungeonPeerProjectionGenerationLocked(session)
	session.dungeonPartyRunMu.Unlock()
	if counts.empty() {
		return false
	}
	s.logGameEvent(session, "game-party-dungeon-peer-projection-generation-reset",
		"source", source,
		"char_id", session.selectedCharacterID,
		"conn_id", session.connID,
		"dropped_projected_peers", counts.projected,
		"dropped_owner_override_rows", counts.overrides,
		"dropped_resend_keys", counts.resends,
		"reason", "the_dungeon_run_was_invalidated_so_every_peer_must_be_projected_again_on_the_next_entry",
		"switch", dungeonPartyPeerProjectionGenerationEnv,
		"mode", dungeonPartyPeerProjectionGenerationMode())
	return true
}
