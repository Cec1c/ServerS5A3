package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是「查看他人信息（城镇交互菜单 → GET_USERINFO mode=3）时角色 ID / 职业文本
// 位置过于靠下」这一轮的**服务端落点**。它只给这一条链补一样东西：被查看角色**自己的
// USERINFO subtype0 身份体**（uid + 名字 + 职业 + 成长 + 等级 + 外观表），并且**先于**
// mode3 详情体下发。
//
// ── 为什么是这条链（现场证据，全部来自 packet_log.txt / 源码写出顺序） ─────────
//
//  1. 两条链的**载体不同**，而且是可逐字段对比的：
//     · 「看自己」：登录/选角走 `buildCSharpSelectedUserInfoBody` occurrence 0
//     （userinfo_selected_subtype0.go:69-86）→ 体头就是
//     `u8 subtype0, u16 version, u16 charID, dstr name, u8 job, u8 grow_type, u8 level, ...`
//     —— **uid / 职业 / 成长 三个字段都在这一条上**。
//     · 「看别人」：handleCurrentPeerUserInfoRequest（peer_userinfo.go）只发
//     `buildCurrentSelectedUserInfoMode3BodyInContext` 一条 mode3 体，而 mode3 体
//     （userinfo_current_modes.go:68-98）里**没有任何名字/职业/成长字节**，
//     它的头是 `u8 3, u16 1, u8 route, u8 ownerChannel, u32 manageLevel, u16 0,
//     u8 0, u8 0, u16 objectKey, u32 exp, u32 statLen, statBlob...`。
//     现场 2026-09-28T13:40:58.734Z 那条 1342 字节的 mode3 体逐字节就是
//     `03 01 00 00 0a 04 00 00 00 00 00 00 00 03 00 f6 47 3b 00 5c 00 00 00 ...`
//     （objectKey=3=test2、无名字、无职业）。
//  2. 「别人」那条身份载体今天**默认不发**：同屏/队友投影里的第二条外观载体
//     （peer_userinfo_carrier）被 clone 行窄门挡着 ——
//     `peerActorUserInfoSubtype0BodyForProjection`（party_peer_appearance_subtype0.go:156-185）
//     只在 `currentSceneObjectAppearanceBodyHasCloneLink(mode0Body)` 为真时才构造。
//     现场统计（packet_log.txt）：`appearance_subtype0_sent=false` 38 次 /
//     `=true` 27 次，且 `peer_char_id=3`（就是运维截图里那个 test2，job=14 grow=0x21）
//     **38 次里全是 false**，理由串是
//     `peer_has_no_clone_avatar_row_so_the_first_carrier_is_sufficient`。
//     ⇒ 看别人时，客户端手里只有 mode0 场景对象（objectKey/名字/职业/成长/等级）
//     和 mode1/mode3 属性体；**没有**一条"这个 uid 的完整角色信息"载体。
//  3. 客户端侧的窗口是静态布局（PVF `ui/characterinfowindow/characterinfo_cn.xui`）：
//     身份文本控件 ID 102 在 `Pos="VECTOR:80, 121"`、ID 103 在 `Pos="VECTOR:80, 136"`，
//     属性树 CNTree ID 104 在 `Pos="VECTOR:0, 156"`。截图换算（窗口 275x394 → 截图
//     339x506，比例 1.2327、标题栏偏移 27.4px）后：副职业/决斗/RP/决斗战绩四行静态文本
//     与 .xui 的 312/330/348/363 完全对上，而截图上 `Lv.90 test2`(≈222) 与
//     `[炽天使]`(≈247) 两行**落在属性树的顶部**（156 → 220），比 121/136 那对控件低约 46px。
//     **服务端没有"文本坐标"这个字段**，能影响它的只有"窗口从哪条记录取身份数据"。
//
// ── 本档做什么、不做什么 ───────────────────────────────────────────────
//
// 做：在 mode3 详情体**之前**多下一条被查看角色自己的 subtype0 身份体（与登录时发给
// 本人的那条**同源同构造点** `buildPeerUserInfoSubtype0AppearanceBody` →
// `userinfo_selected_subtype0.go`），让"他人信息"窗口和"自己信息"窗口拿到同一类身份载体。
//
// 不做：不改 mode3 体的任何一个字节（它的 92 字节属性块与装备创建行是现网验证过的，
// 挪一个字节就会让整段属性/装备错位）；默认档也不动同屏/队友投影的 clone 窄门
// （那是外观链，有自己的一组钉子测试）。
//
// ── 2026-09-29 第 4 次发版后的实机结论（默认档已上线，界面没变） ──────────
//
// 线上判读事件证明默认档**确实发出去了**：
//
//	EVENT kind=game-peer-userinfo-identity-carrier conn_id=game-000030 channel_id=10
//	  source_char_id=1 target_char_id=6 mode=3 env=DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER
//	  switch=on sent=true body_len=425 uid=6 name=test78 job=0 grow_type=33 level=90
//	  parsed=true order=subtype0_identity_then_mode3_detail t=2026-09-28T18:59:05.562317033Z
//	EVENT kind=game-peer-userinfo-response-send conn_id=game-000030 ... body_len=2098
//	  identity_carrier_sent=true identity_carrier_body_len=425 identity_carrier_switch=on
//	  t=2026-09-28T18:59:05.571686521Z
//
// 同一对"看的人=1 / 被看的人=6"在那之前**已经**从同屏投影里收到过被看角色自己的
// subtype0（同构造点、body_len=425、13 行外观、8 行克隆链接）**13 次**：
//
//	EVENT kind=game-party-peer-actor-appearance source=town_copresence_actor_state_existing_to_newcomer
//	  conn_id=game-000030 receiver_char_id=1 peer_char_id=6 ... appearance_subtype0_body_len=425
//	  appearance_subtype0_rows=13 appearance_subtype0_link_rows=8 appearance_subtype0_sent=true
//	  peer_userinfo_carrier=peer_userinfo_subtype0_appearance_list_sent t=2026-09-28T19:00:15.690898309Z
//
// 运维 19:01:25 的截图里 `Lv.90 test78` 与 `[剑魂]` 仍然压在属性面板上边、第二行被面板边
// **遮盖** ⇒ "客户端缺这条身份记录"这一族假设（含"每个同屏角色都补发"）被现场否掉：
// 载体已在，位置没动。`all_peers` 因此只作为**显式实验档**保留（用来把"载体已到位"坐实到
// 每个角色），**默认不打开**；真正要改的是客户端窗口的绘制分支（见报告 §9）。
//
// ── 开关（消费点自读 env，不进 options.go） ─────────────────────────────
//
//	DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER=on（默认，含未设置）
//	    查看他人信息时**先**发一条该角色的 USERINFO subtype0 身份体，再发 mode3 详情体；
//	    并额外记一条 `game-peer-userinfo-identity-carrier` 判读事件。
//	all_peers（实验档，**不是**默认档）
//	    额外把同屏/队友投影的 clone 外观窄门放开：每个被投影角色都补发他自己的 subtype0
//	    （厂商 `TownHandler.cs:502-515` 的口径就是无条件发）。默认档不动这条链，
//	    所以队友外观的下发序列一个字节都不变。
//	legacy（及 0/false/no/off/disable/disabled/none）
//	    一键回退：身份体根本不构造，下发序列与改动前**逐字节相同**（只有 mode3 那一条）。
const (
	// peerUserInfoIdentityCarrierEnv 是"查看他人信息是否补发目标身份体、以及同屏是否
	// 无条件补发"的开关名。
	peerUserInfoIdentityCarrierEnv = "DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER"

	peerUserInfoIdentityCarrierOn       = "on"
	peerUserInfoIdentityCarrierAllPeers = "all_peers"
	peerUserInfoIdentityCarrierLegacy   = "legacy"

	// peerUserInfoIdentityCarrierSource 是这条体的来源串（进判读事件与 sendGameUpperRawClass 的自述）。
	peerUserInfoIdentityCarrierSource = "peer_userinfo_identity_carrier_subtype0_before_mode3"
)

// peerUserInfoIdentityCarrierEnabled 回答"要不要在 mode3 详情体之前补发目标身份体"。
// 只有**显式**写 legacy 及其别名才回退；其余取值（含未设置、all_peers、写错的字串）都发，
// 与同包既有开关同口径（party_peer_appearance_subtype0.go:90-100）。
func peerUserInfoIdentityCarrierEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(peerUserInfoIdentityCarrierEnv))) {
	case peerUserInfoIdentityCarrierLegacy, "off", "0", "false", "no", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// peerUserInfoIdentityCarrierAllPeersEnabled 回答"要不要把同屏/队友投影的 clone 外观窄门
// 也放开"（实验档）。只有**显式**写 all_peers 及其别名才打开；未设置/写错的字串都保持
// 既有窄门 ⇒ 默认档下队友外观链的下发逐字节不变。
func peerUserInfoIdentityCarrierAllPeersEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(peerUserInfoIdentityCarrierEnv))) {
	case peerUserInfoIdentityCarrierAllPeers, "all-peers", "all", "peers", "every_peer", "every-peer":
		return true
	default:
		return false
	}
}

// peerUserInfoIdentityCarrierMode 只用于事件/诊断。
func peerUserInfoIdentityCarrierMode() string {
	switch {
	case !peerUserInfoIdentityCarrierEnabled():
		return peerUserInfoIdentityCarrierLegacy
	case peerUserInfoIdentityCarrierAllPeersEnabled():
		return peerUserInfoIdentityCarrierAllPeers
	default:
		return peerUserInfoIdentityCarrierOn
	}
}

// peerUserInfoIdentityCarrierBodyForInspect 构造"被查看角色自己的 USERINFO subtype0 身份体"。
// 复用队友投影的同一个构造点，因此身份字段（uid/名字/职业/成长/等级）的形状与本人登录时
// 收到的那条**同源**；回退档返回 nil，调用方按"少一条包"处理。
func (s *Service) peerUserInfoIdentityCarrierBodyForInspect(
	ctx context.Context,
	receiver *gameSession,
	actor *gameSession,
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	charID uint16,
	charName string,
) []byte {
	if !peerUserInfoIdentityCarrierEnabled() {
		return nil
	}
	body, ok := s.buildPeerUserInfoSubtype0AppearanceBody(
		ctx,
		receiver,
		actor,
		character,
		hasCharacter,
		charID,
		charName,
	)
	if !ok {
		return nil
	}
	// mode0 预演闸门（DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER_MODE0_GUARD，默认 off）：
	// 这条体的首字节是 0x00，客户端会把它当 class0/msg_id=2 的 **mode0 场景对象体** 读
	// （client-patch/90CN.cpp:1675-1684），而 mode0 那条 walk 会按 19 字节/行 + u32 raw 长度
	// 去读「装备摘要表」。现场 9/9 次崩溃（eip=0x03B10747，memcpy 内联体）都紧跟这次 walk，
	// 且只有"名字长度读得下、表却读出体"的那一族会崩（char 4 vs char 6 的 A/B）。
	// 默认档不预演、不扣包、不写事件 ⇒ 下发与日志逐字节等于改动前；
	// 打开后每次"查看他人信息"都写一条判读行（decision=keep|skip），档位与判据见 mode0_guard 文件头。
	if peerUserInfoIdentityCarrierMode0GuardEnabled() {
		walk := peerUserInfoIdentityCarrierMode0GuardSkip(body)
		if s != nil && receiver != nil {
			fields := []any{"target_char_id", charID, "char_name", charName}
			if walk.Skip {
				fields = append(fields, "decision", "skip")
			} else {
				fields = append(fields, "decision", "keep")
			}
			fields = append(fields, "safe", walk.Safe)
			s.logGameEvent(receiver, "game-peer-userinfo-identity-carrier-mode0-guard",
				append(fields, peerUserInfoIdentityCarrierMode0GuardFields(walk, len(body))...)...)
		}
		if walk.Skip {
			return nil
		}
	}
	return body
}

// currentPeerUserInfoIdentityForLog 只从字节里读这条身份体的头部身份字段，
// 让"这一次到底把谁的 uid/职业/成长 发出去了"从 packet_log.txt 就能判定。
//
// 偏移来自写出顺序（userinfo_selected_subtype0.go:69-86）：
// u8 subtype0 + u16 version + u16 charID + u32 nameLen + name + u8 job + u8 grow + u8 level
// + u8 pvp_grade + u8 pvp_rating_grade + u8 user_state + u8 appearanceCount ...
func currentPeerUserInfoIdentityForLog(body []byte) (uid uint16, name string, job byte, grow byte, level byte, ok bool) {
	const fixedPrefix = 5
	if len(body) < fixedPrefix+4 {
		return 0, "", 0, 0, 0, false
	}
	if body[0] != 0 {
		return 0, "", 0, 0, 0, false
	}
	nameLen := int(binary.LittleEndian.Uint32(body[fixedPrefix : fixedPrefix+4]))
	pos := fixedPrefix + 4
	if nameLen < 0 || pos+nameLen+3 > len(body) {
		return 0, "", 0, 0, 0, false
	}
	name = string(body[pos : pos+nameLen])
	pos += nameLen
	return binary.LittleEndian.Uint16(body[3:5]), name, body[pos], body[pos+1], body[pos+2], true
}
