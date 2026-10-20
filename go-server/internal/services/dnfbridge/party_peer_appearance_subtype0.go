package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件补上"队友（同屏/同副本）外观"缺的**第二条载体**：被投影角色自己的
// USERINFO subtype0 外观列表（`msg_id=2`，23 字节外观行）。
//
// # 上一轮修了什么、为什么还是看不到（2026-09-28 夜，线上复测）
//
// 上一轮把**场景对象 mode0**（19 字节外观行）的 `LinkItemId` 补上了克隆来源
// （`DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK`，见 party_peer_appearance.go 文件头）。
// 线上日志证实它**确实生效**：
//
//	packet_log.txt:106749 EVENT kind=game-party-peer-actor-appearance
//	  source=party_dungeon_shared_loading_barrier_party_peer_copresence_self_actor_to_peer
//	  receiver_char_id=3 peer_char_id=6 object_key=6 mode0_body_len=498 mode1_body_len=2461
//	  appearance_rows=15 appearance_link_rows=8 clone_link_mode=on
//
// 但操作者复测仍然是「3 时装还是看不到，还是默认时装」。逐字节比对**收包方**与
// **本人**收到的那份 19 字节外观表（同一角色 6、同一条 mode0 载体）：
//
//	SEND conn_id=game-000008 pkt_seq=518  msg_id=2 plain_body_len=498  (14:27:22 发给队长 3)
//	  row00 slot= 0 display=101550581 link=42601 packed=0x00
//	  row03 slot= 3 display=101500759 link=42609 packed=0x00
//	  row12 slot=12 display=101040493 link=0     packed=0x1e
//	  ...（15 行 + 尾随 116 字节后缀）
//	SEND conn_id=game-000011 pkt_seq=2018 msg_id=2 plain_body_len=498  (14:30:41 发给本人 6)
//	  row00 slot= 0 display=101550581 link=42601 packed=0x00
//	  row03 slot= 3 display=101500759 link=42609 packed=0x00
//	  row12 slot=12 display=101040493 link=0     packed=0x1e
//	  ...（逐字节相同，含尾随后缀）
//
// ⇒ **这条载体的 delta 已经是 0**：收包方拿到的外观表和本人拿到的一字不差。
// 既然本人屏幕上时装是对的、收包方屏幕上还是默认，缺的东西就**不在这条载体里**，
// 再加多少链接字段都不会改变客户端行为。
//
// # 缺的是哪一条载体（厂商口径，本轮补的就是它）
//
// 厂商参考实现里，同屏"别人看某人"用的**唯一**外观载体是**被看的那个人自己的
// USERINFO subtype0 体**（不是场景对象 mode0）：
//
//	R10675\Network\Handlers\TownHandler.cs:502-515
//	  foreach (var o in others) {
//	      await session.SendPacketAsync(GamePacketEnvelopeBuilder.Build(0x00, 0x0002,
//	          AppearanceService.BuildNoti2Body(o.Player, _database)));
//	      ... BuildCoPresenceInsert(...)                       // = 0x0017
//	  }
//	R10675\Network\Handlers\TownHandler.cs:540-553  // 同一个 selfAppearance 推给每个已在场玩家
//
// 该体的外观行是 **23 字节**（`ExpansionLen=4` + 4 字节染色数据常驻，
// `UserInfoSubtype0Builder.cs:232-241`；尺寸权威 `CharacterDataSelfHeal.cs:47`
// `AppearanceEntrySize = 23`），克隆件的两个字段仍与 mode1 相反：
// `DisplayItemId = ClearAvatarId`（被克隆件）、`LinkItemId = 克隆件自己`
// （`Noti2InventoryProjectionBuilder.cs:250-275`）。本仓的
// `userinfo_selected_subtype0.go`（`writeCSharpAppearanceEntry`）**已经**按这个形状写，
// 但那条载体只在**登录/选角**时发给本人 —— 队友投影的 `omitted` 自述里就一直挂着
// `noti2_roster_appearance_table(login_only_carrier)`。
//
// 本文件把这条载体按厂商口径补进两条队友投影（城镇同屏 + 副本队友），**追加**在既有
// mode0/mode1 之后：不删除、不修改任何既有字节，只在开关为修复档时多一条 `msg_id=2`。
//
// # 开关
//
//	`DNFBRIDGE_PEER_APPEARANCE_SUBTYPE0_LIST`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：队友投影额外发一条该角色自己的
//	    subtype0 外观列表；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前的下发序列
//	    （少这一条包，其余一字不差）。
//
// 与同包既有开关一致的取值方式：**每次调用读一次 env**，改 env + 重启即生效，
// 不需要重编译，也不经过 options.go。
const currentPeerAppearanceSubtype0Env = "DNFBRIDGE_PEER_APPEARANCE_SUBTYPE0_LIST"

// currentSubtype0AppearanceEntryBytes 是厂商 CharacterAppearanceEntry 的线上宽度
// （`CharacterDataSelfHeal.cs:47` AppearanceEntrySize = 23）：
// slot(1) + DisplayItemId(4) + ExpansionLen(4) + ExpansionData(4) + State(1) +
// LinkItemId(4) + EnchantValue(4) + Flag20(1)。
const currentSubtype0AppearanceEntryBytes = 23

// currentPeerAppearanceSubtype0Setting 解析 DNFBRIDGE_PEER_APPEARANCE_SUBTYPE0_LIST。
// 解析规则与 currentPeerAppearanceCloneLinkSetting（party_peer_appearance.go:89）完全一致。
func currentPeerAppearanceSubtype0Setting() (enabled bool, raw string, bad bool) {
	raw = strings.TrimSpace(os.Getenv(currentPeerAppearanceSubtype0Env))
	switch strings.ToLower(raw) {
	case "", cloneAvatarAppearanceOn, "1", "true", "yes", "current", "fix", "fixed":
		return true, raw, false
	case cloneAvatarAppearanceOff, "0", "false", "no", cloneAvatarAppearanceLegacy:
		return false, raw, false
	default:
		return true, raw, true
	}
}

func currentPeerAppearanceSubtype0Enabled() bool {
	enabled, _, _ := currentPeerAppearanceSubtype0Setting()
	return enabled
}

// currentPeerAppearanceSubtype0Mode 是判读事件里的档位串（on = 修复档 / off = 回退档）。
func currentPeerAppearanceSubtype0Mode() string {
	if currentPeerAppearanceSubtype0Enabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// buildPeerUserInfoSubtype0AppearanceBody 构造**被投影角色**自己的 USERINFO subtype0 体。
//
// 复用本人登录时用的同一个构造点（`buildCSharpSelectedUserInfoBody` occurrence 0
// → `userinfo_selected_subtype0.go` → `writeCSharpAppearanceEntry`），因此外观行的
// 23 字节形状、克隆件的 Display/Link 配对与本人那条**同源**，不会出现两套写法的分叉。
// `receiver` 只作为构造上下文（channel 展示字段沿用收包方会话，与城镇同屏的
// owner 口径一致）；失败时返回 false，调用方按"少一条包"处理，绝不阻断既有投影。
func (s *Service) buildPeerUserInfoSubtype0AppearanceBody(
	ctx context.Context,
	receiver *gameSession,
	actor *gameSession,
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	charID uint16,
	charName string,
) ([]byte, bool) {
	// Official 24.9 routes NOTI2/mode0 to the native scene-object reader.
	// The old roster carrier has a name at +5 and 23-byte appearance rows;
	// that reader expects a name at +0x4e and 19-byte scene rows. Sending
	// it to peers can replace the local actor with malformed equipment.
	// Native mode0/mode1 projections already carry the peer's identity.
	if s != nil && s.options.currentClientVersion == "official-24.9" {
		return nil, false
	}
	if s == nil || receiver == nil || actor == nil || !hasCharacter || charID == 0 {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var legacyRepo dnfrepo.LegacyUserInfoRepository
	if repositories, ok := s.repositoryGroup(); ok {
		legacyRepo = repositories.LegacyUserInfo
	}
	body := s.buildCSharpSelectedUserInfoBody(ctx, receiver, legacyRepo, 0, character, true, charID, charName)
	if len(body) == 0 {
		return nil, false
	}
	return body, true
}

// peerActorUserInfoSubtype0BodyForProjection 是两条队友投影共用的取值点。
//
// **只在被投影角色的外观表里真的有克隆行（`LinkItemId != 0`）时才补这条载体**：
// 普通装扮行本来就能靠 display 件自己画出来（现场：队员看得到队长的普通时装），
// 只有克隆件需要"显示件 + 链接件 + 23 字节外观列表"这一组语义。这条窄门同时保证
// **没有克隆件的角色下发序列一个字节都不变**（既有测试与现场都按这个口径）。
//
// 开关为回退档、对端没有克隆行、或构造失败时一律返回 nil（少一条包，不阻断既有投影）。
//
// 2026-09-29 追加：`DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER=all_peers`（显式实验档，
// 默认不打开）时跳过下面这道 clone 窄门，让**每个**被投影角色都补发他自己的 subtype0
// （厂商 TownHandler.cs:502-515 就是这个口径）。默认档（未设置/on/legacy）走原判据，
// 队友外观链的下发逐字节不变。为什么默认不打开：现场已经证明"载体在、位置不动"
// （见 peer_userinfo_identity_carrier.go 文件头与 _dist/peer-info-text-position.md §9）。
func (s *Service) peerActorUserInfoSubtype0BodyForProjection(
	ctx context.Context,
	receiver *gameSession,
	actor *gameSession,
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	charID uint16,
	charName string,
	mode0Body []byte,
) []byte {
	if !currentPeerAppearanceSubtype0Enabled() {
		return nil
	}
	hasCloneLink := currentSceneObjectAppearanceBodyHasCloneLink(mode0Body)
	if !hasCloneLink && !peerUserInfoIdentityCarrierAllPeersEnabled() {
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
	return body
}

// currentSceneObjectAppearanceBodyHasCloneLink 报告这条 mode0 体的 19 字节外观表里
// 有没有任何一行的 LinkItemId 非 0（= 这个角色穿了克隆装扮）。只读字节，不查库。
func currentSceneObjectAppearanceBodyHasCloneLink(mode0Body []byte) bool {
	composition, ok := currentSceneAppearanceBodyCompositionForLog(mode0Body)
	return ok && composition.LinkRows > 0
}

// currentPeerActorSubtype0ReasonText 自述这一次为什么发/没发第二条载体，
// 让下一次线上复测只看事件就能判掉"不需要"与"构造失败"。
func currentPeerActorSubtype0ReasonText(enabled bool, body []byte, closeLinkRows int) string {
	switch {
	case !enabled:
		return "switch_off"
	case closeLinkRows == 0 && !peerUserInfoIdentityCarrierAllPeersEnabled():
		return "peer_has_no_clone_avatar_row_so_the_first_carrier_is_sufficient"
	case len(body) == 0:
		return "repository_or_character_record_unavailable"
	case closeLinkRows == 0:
		// all_peers 实验档：没有克隆行也照发，理由串必须说清这不是"克隆件需要"。
		return "identity_carrier_env_all_peers_sends_peer_subtype0_without_clone_rows"
	default:
		return "peer_wears_clone_avatar_rows_so_the_second_carrier_is_required"
	}
}

// sendPeerUserInfoSubtype0Appearance 把上面那条体发给收包方（`msg_id=2`、class 0）。
// 失败只记事件、不影响既有投影链（与 `game-town-copresence-actor-state-failed` 同口径）。
func (s *Service) sendPeerUserInfoSubtype0Appearance(receiver *gameSession, body []byte, source string, charID uint16) bool {
	if s == nil || receiver == nil || len(body) == 0 {
		return false
	}
	if err := s.sendGameUpperRawClass(
		receiver,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		body,
		0,
	); err != nil {
		s.logGameEvent(receiver, "game-peer-appearance-subtype0-send-failed",
			"source", source,
			"char_id", charID,
			"body_len", len(body),
			"error", err)
		return false
	}
	return true
}

// currentSubtype0AppearanceBodyCompositionForLog 只从字节里读"这条 subtype0 外观列表
// 实际发了什么"：行数、写了链接字段的行数、写了显示件的行数。
//
// 偏移来自同文件的写出顺序（`userinfo_selected_subtype0.go:69-84`）：
// u8(0) + u16(1) + u16(charID) + dstr(name) + job/grow/level/pvp_grade/pvp_rating_grade/
// user_state(共 6 字节) + u8(count) + count × 23 字节外观行。
func currentSubtype0AppearanceBodyCompositionForLog(body []byte) (rows int, linkRows int, displayRows int, ok bool) {
	const fixedPrefix = 5
	if len(body) < fixedPrefix+4 {
		return 0, 0, 0, false
	}
	nameLen := int(binary.LittleEndian.Uint32(body[fixedPrefix : fixedPrefix+4]))
	pos := fixedPrefix + 4 + nameLen
	if nameLen < 0 || pos+6 >= len(body) {
		return 0, 0, 0, false
	}
	pos += 6
	count := int(body[pos])
	pos++
	for idx := 0; idx < count; idx++ {
		if pos+currentSubtype0AppearanceEntryBytes > len(body) {
			return 0, 0, 0, false
		}
		entry := body[pos : pos+currentSubtype0AppearanceEntryBytes]
		display := binary.LittleEndian.Uint32(entry[1:5])
		link := binary.LittleEndian.Uint32(entry[14:18])
		if display != 0 && display != currentActorMode0AppearanceEmptyItem {
			displayRows++
		}
		if link != 0 {
			linkRows++
		}
		pos += currentSubtype0AppearanceEntryBytes
	}
	return count, linkRows, displayRows, true
}
