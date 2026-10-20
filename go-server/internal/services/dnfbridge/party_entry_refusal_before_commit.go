package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// 本文件是文档段 104「组队时，如果选择的的地图队伍里某个队员没有解锁，会导致返回城镇的
// 选项直接消失，要等倒计时才能自动退出」的服务端那一半：**整队进图在提交之前被拒绝时，
// 路由发起者那条会话必须收到既有形状的拒绝包，队员客户端停在"等进图"窗口里的那次路由
// 请求也要当场结清。**
//
// ── 现场形状（本仓可复现的那一半，逐包实测）────────────────────────────────
//
// 两名成员（队长 99 / 队员 100）成队，队员角色的等级低于本次所选副本的
// `[minimum required level]`（= 客户端上那张图对他是灰的、未解锁）。
//
//   ① 队长按"开始冒险"：`handleDungeonSelectUpperPlanned(leader, …)`
//      → `prepareRuntimePartyDungeonEntry` 逐个成员做同一套进图前置校验，
//        队员那条返回 `ErrEntryLevelTooLow`；改动前那一支只写一条
//        `game-party-dungeon-entry-blocked` 就静默 `return nil`，队长也收不到任何包。
//   ② 队员自己按进图时更糟：它那条 op16 会被路由给队长并**先收到成功 ACK**，随后队长那条
//      流程同样静默结束（只有 `game-party-dungeon-entry-routed-async-committed` 一条日志）。
//      队员客户端已经按"选图成功"进入等进图状态，此后再没有任何一条回包 ⇒ 现场口径就是
//      "返回城镇选项消失、只能等倒计时"（这是现场描述；客户端收到失败包之后具体怎么收尾
//      属于 EXE 行为，见下面"EXE 侧核实"一节）。
//
// ── 修法：一次路由请求一个令牌 ─────────────────────────────────────────────
//
//   bind    —— 队员自己那次 op16 被回 ACK 之后，在**队员自己的事件循环**上绑定挂起态并
//             拿到 `partyDungeonEntryRoutedRequest`（角色号 + generation + seq）。这个令牌
//             跟着这次路由一起投递给队长那条流程。
//   应答    —— 队长那条流程在提交之前给出任何一条拒绝时，只用**自己拿到的那个令牌**去应答：
//             令牌仍然指回它那次请求（身份 + seq 三者都匹配）才取走并回包；否则一包不发，
//             也不会动队员新挂上的那条请求。
//   结清    —— 整队进图真正提交之后，同样只结清"还是这个令牌"的那一条。
//
// 于是"一个 op16 对应一包结论"是**逐请求**成立的：同角色连续按两次进图时，旧那条流程的
// 拒绝不会吞掉新那条请求的挂起态（新请求由它自己的流程应答），也不会把拒绝包发给换角之后
// 的新角色。
//
// ── 身份取自既有安全快照，不裸读会话字段 ───────────────────────────────────
//
// `session.selectedCharacterID` / `session.characterGeneration` 由
// `bindGameSessionCharacter`（持 session.dungeon.mu + s.mu）、会话解绑（持 s.mu）与
// `return_select_character`（持 s.mu + session.dungeon.mu）改写，因此这里一律走仓库既有的
// `boundGameSessionCharacterSnapshot` / `boundGameSessionCharacterCurrent`（都在 s.mu 下读），
// 不裸读这两个字段。挂起态本身仍在 `session.dungeon.mu` 下读写；两把锁**不嵌套**
// （`bindGameSessionCharacter` 的顺序是 session.dungeon.mu → s.mu，嵌套会构成 ABBA）。
//
// ── 拒绝码与包形状（复用既有出口，不新增包型、不新增错误码）──────────────
//
// 发起者与队员拿到的都是 `sendSelectDungeonFailure` 的 `{0, reason, 0}`（msg id = op16、
// 普通频道分类），reason/class 由调用方按**该出口自己的**判定给出（等级不足 = 14、
// InvalidSelectionState = 0x13、DungeonUnavailable = 0x01 …），来源是 A21 参考实现
// `DungeonAdmissionRejectBuilder.cs:16-41`，与本仓已上线的 op16 失败码同一张表。
//
// ── 出口范围（这次拒绝发生在哪一步）────────────────────────────────────────
//
//   · 队员进图前置、raid 冻结、返回城镇原点、进图包前置、深渊派对入场消耗、安徒生孵化转移：
//     都在**队长自己的 runtime/场景提交之前**，这一队没有任何成员进过图。
//   · 两条"晚期错误"：队员自己的 runtime 已经提交（失败时由
//     `commitRuntimePartyPreparedFollowers` 内部回滚掉），或队长自己的 runtime 提交失败
//     （先回滚已提交的队员 runtime，再回包）。这两条在回包之前已经把队员的运行时摘掉 ——
//     队员客户端收到拒绝包时不在副本里，也才不会被 `refuseDungeonSelectEntry` 的
//     "已有活动运行时"静默口径吞掉。
//
// ── EXE 侧核实与不覆盖的范围（交父代理/用户）──────────────────────────────
//
//   · 当前客户端 EXE（20261005 构建，SHA256 1AC1264553F8F762E64F2C637C4C26D3BF5FF0FDC60A090CA8B077D452FD4331）
//     的入站准入位图（VA 0x6E92300，谓词 0x6E83610）对 opcode 16 置位、且无 class 维度 ⇒ op16
//     的成功 ACK 与拒绝（同一个 msg id）都会派发到客户端处理器。
//   · **未证**：客户端在"已经认为自己选图成功"之后再收到这条 op16 失败时是否一定退出等待
//     窗口（准入位图只证明包被派发，不证明状态机放行）。这一条需要实机抓包或更长的离线 RE，
//     按"协议不明交父代理"上报——本文件不放行任何未解锁地图、不改客户端、不改 PVF。
//   · "选图那条 op16 落在同角色的另一条连接上"（辅助/替换 socket）时，权威会话不是收 ACK
//     的那条：`boundGameSessionCharacterSnapshot` 取不到身份 ⇒ 这次不绑令牌，也就不可能把
//     应答发给错误 socket；那一支仍然不猜 socket 归属。

// partyDungeonEntryRoutedRequest 是一条"队员把选图转交给队长执行"的请求令牌。
// 由 `bindPartyDungeonEntryRoutedPending` 在队员自己的事件循环上创建，只用来回答它自己
// 那一次请求：队长那条流程拿到的令牌若已被更新的请求覆盖（seq 变了），就一包不发。
type partyDungeonEntryRoutedRequest struct {
	session     *gameSession
	characterID uint16
	generation  uint64
	seq         uint64
}

// bindPartyDungeonEntryRoutedPending 记下"这名角色的 op16 已经被回 ACK、正在等队长那条
// 整队进图流程给结果"，并返回这次请求的令牌（nil = 没绑上，例如这条会话不是该角色的权威
// 会话）。角色号/generation 取既有安全快照，不裸读会话字段；seq 每次绑定递增。
func (s *Service) bindPartyDungeonEntryRoutedPending(session *gameSession) *partyDungeonEntryRoutedRequest {
	if s == nil || session == nil {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.character == 0 {
		return nil
	}
	session.dungeon.mu.Lock()
	session.partyDungeonEntryRoutedPendingSeq++
	seq := session.partyDungeonEntryRoutedPendingSeq
	session.partyDungeonEntryRoutedPendingCharacter = identity.character
	session.partyDungeonEntryRoutedPendingGeneration = identity.generation
	session.dungeon.mu.Unlock()
	return &partyDungeonEntryRoutedRequest{
		session:     session,
		characterID: identity.character,
		generation:  identity.generation,
		seq:         seq,
	}
}

// clearPartyDungeonEntryRoutedPending 清掉这条会话的挂起态（幂等，仅测试/收尾用）。
func clearPartyDungeonEntryRoutedPending(session *gameSession) {
	if session == nil {
		return
	}
	session.dungeon.mu.Lock()
	session.partyDungeonEntryRoutedPendingCharacter = 0
	session.partyDungeonEntryRoutedPendingGeneration = 0
	session.dungeon.mu.Unlock()
}

// consumePartyDungeonEntryRoutedRequest 是挂起态的唯一消费点：比较（角色号, generation, seq）
// 后清掉。requireCurrentIdentity 为真时还会先核"这条会话当前仍是那个角色 + 那条 generation"
// （走既有安全快照，在 s.mu 下读）。不匹配时**不动**挂起态 —— 那是别人（更新的请求、或换角后
// 的新角色）的挂起态。
func (s *Service) consumePartyDungeonEntryRoutedRequest(token *partyDungeonEntryRoutedRequest, requireCurrentIdentity bool) bool {
	if s == nil || token == nil || token.session == nil {
		return false
	}
	if requireCurrentIdentity && !s.boundGameSessionCharacterCurrent(boundGameSessionCharacter{
		session:    token.session,
		character:  token.characterID,
		generation: token.generation,
	}) {
		return false
	}
	session := token.session
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.partyDungeonEntryRoutedPendingCharacter != token.characterID ||
		session.partyDungeonEntryRoutedPendingGeneration != token.generation ||
		session.partyDungeonEntryRoutedPendingSeq != token.seq {
		return false
	}
	session.partyDungeonEntryRoutedPendingCharacter = 0
	session.partyDungeonEntryRoutedPendingGeneration = 0
	return true
}

// takePartyDungeonEntryRoutedRequest 取走"令牌对应的那一次请求"，返回是否取到（取到才允许回包）。
func (s *Service) takePartyDungeonEntryRoutedRequest(token *partyDungeonEntryRoutedRequest) bool {
	return s.consumePartyDungeonEntryRoutedRequest(token, true)
}

// discardPartyDungeonEntryRoutedRequest 结清"令牌对应的那一次请求"但不回包（整队进图已经
// 真正提交、队员按那次请求的结果就是进图）。
func (s *Service) discardPartyDungeonEntryRoutedRequest(token *partyDungeonEntryRoutedRequest) bool {
	return s.consumePartyDungeonEntryRoutedRequest(token, true)
}

// takePartyDungeonEntryRoutedPendingForSession 是"没有令牌"那一支（队长不在线、路由无法执行
// 时对队员自己那条 op16 的应答）用的：只要这条会话还挂着活动身份、挂起态属于它，就取走。
func (s *Service) takePartyDungeonEntryRoutedPendingForSession(session *gameSession) bool {
	if s == nil || session == nil {
		return false
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.character == 0 {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.partyDungeonEntryRoutedPendingCharacter != identity.character ||
		session.partyDungeonEntryRoutedPendingGeneration != identity.generation {
		return false
	}
	session.partyDungeonEntryRoutedPendingCharacter = 0
	session.partyDungeonEntryRoutedPendingGeneration = 0
	return true
}

// answerRoutedPartyEntryOwnerOnRefusal 是整队进图这条流程上"提交前拒绝"时对**路由发起者**
// 的应答：只有令牌仍然指回它那一次请求（身份 + seq 都匹配）才取走并回包。
// true 表示在调用期限内确认发送完成；false 也可能是已开始的写在超时后才完成，不能据此重发。
//
// 现场写仍在**这名队员自己的事件循环**上（callGameSession），发送前再核一次权威身份 —— 投递
// 期间换角就不会把拒绝包发给新角色。既有静默口径（队员已有活动副本运行时 / 深渊派对 legacy 档）
// 吞掉的不计入返回值。
func (s *Service) answerRoutedPartyEntryOwnerOnRefusal(
	ctx context.Context,
	requester *gameSession,
	state alignedcmd.PartyState,
	token *partyDungeonEntryRoutedRequest,
	request dungeoncmd.SelectDungeonRequest,
	siteReason string,
	class string,
	reason byte,
	err error,
) bool {
	if s == nil || token == nil {
		return false
	}
	peer := token.session
	if peer == nil {
		return false
	}
	requesterIdentity, _ := s.boundGameSessionCharacterSnapshot(requester)
	delivered := false
	sendErr := s.callGameSession(ctx, peer, "doc104-awaiting-member-entry-refusal", func() error {
		// 取走这一次请求必须落在**这条事件循环的 callback 内、发送之前**：投递期间队员可能又按了
		// 一次进图（同角色、新 seq）或换了角 —— 那时这次请求已经作废，取不到令牌就一包都不发，
		// 把新请求留给它自己那条流程应答。take 内部同时核身份（s.mu 下的安全快照）与 (角色号,
		// generation, seq) 三者，所以这里不再重复判身份。
		if !s.takePartyDungeonEntryRoutedRequest(token) {
			return nil
		}
		if suppressed, _ := s.dungeonSelectRefusalSuppression(peer, request); suppressed {
			return nil
		}
		refuseErr := s.refuseDungeonSelectEntry(
			peer,
			request,
			siteReason,
			class,
			reason,
			err,
			"party_id", state.PartyID,
			"leader_char_id", state.UserID,
			"requester_char_id", requesterIdentity.character,
			"member_char_id", token.characterID,
			"routed_request_seq", token.seq,
			"released_awaiting_routed_entry", true,
		)
		if refuseErr != nil {
			// This routed callback has no peer read-loop caller to close the
			// connection on an outbound error. A partial frame cannot be
			// retried safely; finish this peer through the normal disconnect.
			if peer.conn != nil {
				_ = peer.conn.Close()
			}
			return refuseErr
		}
		delivered = true
		return nil
	})
	if sendErr != nil {
		s.logGameEvent(requester, "game-party-dungeon-entry-awaiting-member-answer-failed",
			"party_id", state.PartyID,
			"leader_char_id", state.UserID,
			"member_char_id", token.characterID,
			"routed_request_seq", token.seq,
			"reason", "member_awaiting_entry_refusal_send_failed",
			"site_reason", siteReason,
			"error", sendErr)
		return false
	}
	return delivered
}

// answerPartyDungeonEntryRoutedMemberWhenLeaderCannotServe 处理"队员自己按了进图、但这次
// 路由确实无法被执行"（队长不在线 / 同一条会话）这一支：挂起态必须当场解掉，并且用既有
// 拒绝形状给队员一条回包，而不是让它一直等下去。
//
// reason 取厂商 default `DungeonUnavailable`（`currentSelectDungeonUnavailableReason` = 0x01，
// `DungeonAdmissionRejectBuilder.cs:16-41` 的 Unknown/DungeonUnavailable 档），**不新增错误码**：
// 这一支没有任何前置校验失败可映射。本函数只负责把"这条选图没被接受"这条既有形状的失败
// 回包发出去、并把挂起态解掉；客户端据此怎么收尾属于 EXE 行为（见文件头"EXE 侧核实"）。
func (s *Service) answerPartyDungeonEntryRoutedMemberWhenLeaderCannotServe(
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	siteReason string,
	state alignedcmd.PartyState,
) error {
	if s == nil || session == nil {
		return nil
	}
	if !s.takePartyDungeonEntryRoutedPendingForSession(session) {
		return nil
	}
	return s.refuseDungeonSelectEntry(
		session,
		request,
		siteReason,
		"entry_unavailable",
		currentSelectDungeonUnavailableReason,
		nil,
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"released_awaiting_routed_entry", true,
	)
}
