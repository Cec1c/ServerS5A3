package dnfbridge

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件实现**队友/他人 USERINFO 里那两页技能表的可选下发**，只服务一条现场症状：
// 厂商修复清单第 54 条「修复队友的 BUFF 显示为 0 级（队友技能页没有下发）」。
//
// # 症状与根因（2026-09-28 操作者报障复现）
//
// 操作者原话：组队时队员享受不到队长的 buff —— 队长是 炽天使（女圣骑士，job 14 =
// `Priest/ATPriest.chr`）放 勇气祝福（skill id 14 = `skill/atpriest/blessofcourage.skl`，
// 现场等级 Lv10，见 dnf_skill_states character_id=3 skill_id=14），**特效双方都看得见**，
// 但队员面板不动、且队员自己的 buff 栏里这条 buff **显示为 0 级**。
//
// 根因分两半，分别有证据：
//
//  1. 技能使用本身根本不经过服务端：C2S type=38（ENUM_CMDPACKET_USE_SKILL）在本仓
//     （`game_dispatch_legacy.go:311-332` 落到 `handleGameLegacyFallbackCommand` 后只记一条
//     INFO，零下行）与厂商三个仓（`GameProtocolHandler.cs:705-715` 的 RegisterSkillHandlers
//     不含 0x0026；未注册命令走 `GameProtocolHandler.cs:296-302` 只记 "Unhandled CMD"）都**没有
//     任何 handler**；副本内同步走的是**原样转发的 UDP 中继**（本仓
//     `party_udp_relay.go:768/874`；厂商 `PartyUdpRelay.cs:823-826`）。
//     ⇒ **特效**是客户端之间 P2P 数据报直接画出来的，与服务端无关，所以"看得见特效"。
//     ⇒ 服务端没有任何"技能 id + 等级 + 目标"可用字段（厂商也没读过），
//     所以**修法不可能是服务端算属性**(option a)。
//
//  2. buff 的**等级与属性**由**收包方自己的客户端**用本地 PVF 技能数据算
//     （厂商侧唯一的 add-buff 包 NOTI 0x01E8 CHARACTER_ADD_BUFF 只带
//     `u8 count + i32 buffId + i32 f1 + i32 f2 + i32 f3`，既无 skill id、无施法者、无目标、无
//     时长，且 f1/f2/f3 全部写 0；见 `SpecialDungeonNotificationBuilder.cs:32-44` +
//     `DungeonBuffNotificationSender.cs:26-44`）。**客户端要算 勇气祝福 的效果，必须知道施法
//     者这一条技能的真实等级** —— 而等级只有一个来源：别人看这个 actor 的 USERINFO 里的技能页。
//     我们的队友 USERINFO 一直把两页技能写成 count=0（见下面的硬编码点）⇒ 队员客户端拿到
//     "技能表里没有这条技能/等级 0" ⇒ buff 图标按 id 出现（id 来自 P2P 中继的那条施法数据报）
//     但等级 0、属性增量算成 0。
//
// # 修复档位（env `DNFBRIDGE_PEER_SKILL_PAGE`）
//
//   - **默认档 `peer`（= 修复档）**：只在**发给队友的** actor body（副本内队友投影，见
//     `dungeon_party_copresence.go:sendDungeonPartyPeerActorState`）里写真实技能页；
//     发给本人的 body 与 subtype1/mode3 一个字节都不变（现行为）。
//     收窄到"队友"是**故意的**：`userinfo_current_modes.go` 同一位置我方 IDA 注记读作
//     `sub_2003420 selected resource group` + 两个组列表，与 A21 的"技能页"语义冲突
//     （见文件头旧注记）。发给本人的 body 一旦改字节，风险落在**本地 actor** 上（登录/选角/
//     进本都要用），而本档只动队友 body，本地风险为零。
//   - `off`（回退档，另接受 0/false/no/legacy/disable/disabled/none）：**逐字节回到改动前**
//     ——三个调用点都写原来的 `位置字节 + 0x00 + 0x00`，不读技能仓储、不写任何事件。
//   - `all`（另接受 on/1/true/yes，= 旧文档里的 on 档）：三个调用点（subtype1 / mode1 / mode3）
//     全部写技能页，供"本地 actor 是否回退"的 A/B 用。
//   - 非法/无法识别的取值 = **默认档（修复档）**：与同包默认开启族
//     （`party_dungeon_peer_position.go:80-81`）同一口径，只有显式 `off` 才回退。
//
// # 布局证据（A21 C#，只读参照，未改动）
//
//   - UserInfoSubtype3Builder.cs:47-49 与 UserInfoSubtype1Builder.cs:59-62 在同一位置写
//     `SkillTreeIndex` → `WriteSkillPage(0)` → `WriteSkillPage(1)` → `EquippedCreatureLevel`；
//     WriteSkillPage（UserInfoSubtype3Builder.cs:118-151）是 `u8 count` + count×{u16 skillId, u8 level}，
//     跳过 level <= 0，上限 byte.MaxValue=255；
//   - 厂商在进本时把带技能页的完整 USERINFO 推给每个队员：
//     DungeonEntryHandler.cs:1039-1052（LoadSyncedSkillState）+ :1071-1074。
//
// # 两页内容怎么分（本仓库既有规则）
//
// 本仓库对**同一对技能页**已有分页规则，就在 scene_skill_protocol.go:19-75：
// 构造 op19 技能信息时把同一棵树写进两页（:65-70 的注释：「Fresh characters need the same
// initial learned skills in both pages, matching the PVF/C# source」）。A21 侧同源：
// InitialCharacterSkills.cs:80-83 的 page1 是 page0 的逐条复制，CharacterSkillProfile.cs:116-131
// 的转职/觉醒送技也是「两页同步」。我们自己的 dnf_skills（mysql_skill.go:148-174）只有扁平的
// skill_id→level，没有「某技能属于哪一页」的归属列（dnf_skill_layouts 的 tree_id 只是快捷栏布局），
// 因此这里沿用既有规则：**page0 = page1 = 全部 level>0 的技能（skillId 升序，单页最多 255 条）**。
//
// # 语义冲突的残余不确定（本文件不解决，实机判据见下）
//
// 我方 IDA 注记把 mode1/mode3 的同一位置读作 `sub_2003420 selected resource group`
// （`userinfo_current_modes.go` 里那三行的既有注释），A21 把同一位置读作技能页。
// **布局位置完全相同、语义互相矛盾**。默认档只动"发给队友"的那一个 body，实机判据：
// `EVENT kind=game-dungeon-party-buff-skill-level-send` 里的 `level_sent` 是否变成真实等级、
// 且队员的 buff 栏等级与面板属性一起动；若队员看到队长 actor 回退（外观/宠物等尾部字段错位），
// 把 env 写成 `off` 即逐字节回到今天。
const peerSkillPageEnv = "DNFBRIDGE_PEER_SKILL_PAGE"

// 档位字面量。`peer` 是默认档（修复档），`off` 是回退字面量，`all` 是旧的 on 档。
const (
	peerSkillPageValuePeer = "peer"
	peerSkillPageValueAll  = "all"
	peerSkillPageValueOff  = "off"
)

// peerSkillPageMode 是档位枚举。**零值 = peer = 默认档 = 修复档**（与同包默认开启族同口径）。
type peerSkillPageMode int

const (
	peerSkillPageModePeer peerSkillPageMode = iota
	peerSkillPageModeAll
	peerSkillPageModeOff
)

func (mode peerSkillPageMode) String() string {
	switch mode {
	case peerSkillPageModeAll:
		return peerSkillPageValueAll
	case peerSkillPageModeOff:
		return peerSkillPageValueOff
	default:
		return peerSkillPageValuePeer
	}
}

// readPeerSkillPageMode 解析档位：空/未设置/非法值都是**默认档（修复档）**，
// 只有显式 `off` 家族才回退，`all` 家族保留旧的 on 档行为。
func readPeerSkillPageMode(value string) peerSkillPageMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case peerSkillPageValueOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return peerSkillPageModeOff
	case peerSkillPageValueAll, "on", "1", "true", "yes":
		return peerSkillPageModeAll
	default:
		return peerSkillPageModePeer
	}
}

// peerSkillPageModeNow 每次调用现读 env（options.go 不可改，走同包既有的"调用点自读"口径，
// 见 peerSkillPageEnabled 旧实现与 skillGrowTypeAuditEnabled）。
func peerSkillPageModeNow() peerSkillPageMode {
	return readPeerSkillPageMode(os.Getenv(peerSkillPageEnv))
}

// peerSkillPageEnabled 是 subtype1（userinfo_selected_subtype1.go:60-66）与 mode3
// （userinfo_current_modes.go:128-134）两个**未收窄**调用点的口径：只有显式 `all`
// （含旧文档的 on/1/true/yes）才写技能页；默认档 `peer` 与回退档 `off` 都不动这两个 body。
func peerSkillPageEnabled() bool {
	return peerSkillPageModeNow() == peerSkillPageModeAll
}

// peerSkillPageTargetKey 是"这条 ctx 正在构造的是**发给队友**的 actor body"的标记键。
// 值 = 收包者（target）角色 id，只用于档位判定与日志。
type peerSkillPageTargetKey struct{}

// withDungeonPartyPeerSkillPageTarget 标记一条 ctx：由它产出的 actor USERINFO 会发给**队友**
// （不是发给这个 actor 自己的会话）。调用点只有副本内队友 actor 投影一处
// （dungeon_party_copresence.go:sendDungeonPartyPeerActorState）。
func withDungeonPartyPeerSkillPageTarget(ctx context.Context, targetCharacterID uint16) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, peerSkillPageTargetKey{}, targetCharacterID)
}

// dungeonPartyPeerSkillPageTarget 读出队友标记与收包者角色 id。
func dungeonPartyPeerSkillPageTarget(ctx context.Context) (uint16, bool) {
	if ctx == nil {
		return 0, false
	}
	value, ok := ctx.Value(peerSkillPageTargetKey{}).(uint16)
	return value, ok
}

// peerSkillPageMaxEntries 是单页条目上限（A21 WriteSkillPage 的 byte.MaxValue）。
const peerSkillPageMaxEntries = 0xff

// peerSkillPageSelectedResourceGroup 是 mode1/mode3 里两页技能之前的那个位置字节。
// 我方 IDA 注记读作 `sub_2003420 selected resource group`（-1 = 保持当前/默认），
// A21 在同一位置写 SkillTreeIndex。**所有档位都照旧写 0xff**（本文件只改它后面那两个计数字节）。
const peerSkillPageSelectedResourceGroup = 0xff

// peerSkillPageEntry 是一条技能页记录（A21 SkillInfoEntrySnapshot 的线格式形态）。
type peerSkillPageEntry struct {
	SkillID uint16
	Level   byte
}

// peerSkillPageEntries 把角色技能行规整成**一页**的内容：
//   - 跳过 level <= 0（A21 WriteSkillPage 只发有等级的条目）；**这条同时就是
//     "特效发了等级就必须非 0"不变式的实现**：页里不可能出现 0 级条目；
//   - 跳过无法编码的技能 id（<0 或 >0xffff）；
//   - level 超过 u8 时饱和到 255（线格式只有 u8，不做回绕）；
//   - 按 skillId 升序（确定性顺序，避免 map 遍历顺序影响包字节）；
//   - 最多 peerSkillPageMaxEntries 条。
//
// 不看 SkillState.Enabled：A21 的技能页没有这个旗标，同一位置的 op19
// （scene_skill_protocol.go:19-31）也只按 level>0 过滤。
func peerSkillPageEntries(skills map[int64]dnfrepo.SkillState) []peerSkillPageEntry {
	if len(skills) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(skills))
	for skillID, state := range skills {
		if state.Level <= 0 || skillID < 0 || skillID > 0xffff {
			continue
		}
		ids = append(ids, skillID)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	if len(ids) > peerSkillPageMaxEntries {
		ids = ids[:peerSkillPageMaxEntries]
	}
	entries := make([]peerSkillPageEntry, 0, len(ids))
	for _, skillID := range ids {
		level := skills[skillID].Level
		if level > 0xff {
			level = 0xff
		}
		entries = append(entries, peerSkillPageEntry{SkillID: uint16(skillID), Level: byte(level)})
	}
	return entries
}

// writePeerSkillPage 写一页技能：`u8 count` + count×{u16 skillId, u8 level}。
func writePeerSkillPage(w *packetWriter, entries []peerSkillPageEntry) {
	if len(entries) > peerSkillPageMaxEntries {
		entries = entries[:peerSkillPageMaxEntries]
	}
	w.writeByte(byte(len(entries)))
	for _, entry := range entries {
		w.writeUint16(entry.SkillID)
		w.writeByte(entry.Level)
	}
}

// writePeerSkillPageBlock 写完整的「技能页块」：`u8 skillTreeIndex` + page0 + page1。
//
// skillTreeIndex 就是调用点原有的那个位置字节，取值与偏移都不变：
//   - subtype1（userinfo_selected_subtype1.go:57）传 `skill_tree_index` 行值；
//   - mode1/mode3（userinfo_current_modes.go:127 / :219）传 peerSkillPageSelectedResourceGroup
//     （0xff，我方 IDA 注记的 selected resource group，-1 保持默认）。
//
// 两页同内容，规则与理由见文件头「两页内容怎么分」。
func writePeerSkillPageBlock(w *packetWriter, skillTreeIndex byte, skills map[int64]dnfrepo.SkillState) {
	writePeerSkillPageBlockEntries(w, skillTreeIndex, peerSkillPageEntries(skills))
}

// writePeerSkillPageBlockEntries 是 writePeerSkillPageBlock 的"已规整条目"形态：
// 调用方已经算过 entries（要拿去写事件）时走这条，避免重复规整。
func writePeerSkillPageBlockEntries(w *packetWriter, skillTreeIndex byte, entries []peerSkillPageEntry) {
	w.writeByte(skillTreeIndex)
	writePeerSkillPage(w, entries) // page 0
	writePeerSkillPage(w, entries) // page 1：与 page0 同内容（既有两页同步规则）
}

// peerSkillPageWriteForMode1Body 判定 mode1 尾块的技能页槽位要不要写**真实**技能页：
// 默认档 `peer` 只认"这条 ctx 产出的是发给队友的 body"（见 withDungeonPartyPeerSkillPageTarget），
// `all` 档不区分收包者，`off` 档一律不写。
func (r *csharpLegacyUserInfoReader) peerSkillPageWriteForMode1Body() bool {
	switch peerSkillPageModeNow() {
	case peerSkillPageModeAll:
		return true
	case peerSkillPageModeOff:
		return false
	default:
		if r == nil {
			return false
		}
		_, peerDirected := dungeonPartyPeerSkillPageTarget(r.ctx)
		return peerDirected
	}
}

// writePeerSkillPageForMode1Body 读**目标 actor** 的技能行、写 mode1 尾块的技能页块，
// 并（仅在队友档下）写一条可判读事件。
func (r *csharpLegacyUserInfoReader) writePeerSkillPageForMode1Body(w *packetWriter) {
	entries := peerSkillPageEntries(r.loadPeerSkillPageSkills(r.characterID))
	writePeerSkillPageBlockEntries(w, peerSkillPageSelectedResourceGroup, entries)
	r.logDungeonPartyBuffSkillPage(entries)
}

// peerSkillPagePartyBuffSkill 是**队友增益技能**的只读命名表：
// 技能 id + 中文名 + 客户端会为**收包队友**移动的属性向量 + PVF 出处。
//
// 证据（只读 PVF 读取，未改任何 PVF）：
//   - 14 `skill/atpriest/blessofcourage.skl` `[name] 勇气祝福`，
//     `[explain]` 原文："祝福一定范围内的队友，增加队友的物理攻击力、魔法攻击力、独立攻击力、
//     力量、智力、命中率…"，`[pre required skill] 21 1`（= 需要 守护祝福 Lv1，与操作者工具提示一致）；
//   - 21 `skill/atpriest/blessofprotection.skl` `[name] 守护祝福`，
//     `[explain]`："增加队友的HP最大值、 MP最大值、 物防、 魔防、 体力、 精神"；
//   - 24 `skill/atpriest/ariaofcourage.skl` `[name] 勇气圣歌`，`[explain]`："增幅队友的Buff效果"。
//
// 本表**只用于日志判读**，不参与任何字节构造；不在表里的技能不影响技能页本身的正确性
// （事件里的 `entries` 字段带全量 id:level）。
type peerSkillPagePartyBuffSkill struct {
	SkillID int64
	Name    string
	Stats   string
	PVFPath string
}

var peerSkillPagePartyBuffSkills = []peerSkillPagePartyBuffSkill{
	{
		SkillID: 14,
		Name:    "勇气祝福",
		Stats:   "physical_attack,magical_attack,independent_attack,strength,intelligence,hit_rate",
		PVFPath: "skill/atpriest/blessofcourage.skl",
	},
	{
		SkillID: 21,
		Name:    "守护祝福",
		Stats:   "hp_max,mp_max,physical_defense,magical_defense,vitality,spirit",
		PVFPath: "skill/atpriest/blessofprotection.skl",
	},
	{
		SkillID: 24,
		Name:    "勇气圣歌",
		Stats:   "valor_blessing_amplify",
		PVFPath: "skill/atpriest/ariaofcourage.skl",
	},
}

// peerSkillPageEntriesText 把技能页条目渲染成稳定的 `id:level,...`（按 id 升序，已规整），
// 超过 limit 条时截断并追加 `+N`。事件里的"等级向量"判读就靠它。
func peerSkillPageEntriesText(entries []peerSkillPageEntry, limit int) string {
	if len(entries) == 0 {
		return "none"
	}
	if limit <= 0 || limit > len(entries) {
		limit = len(entries)
	}
	var builder strings.Builder
	for index := 0; index < limit; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.Itoa(int(entries[index].SkillID)))
		builder.WriteByte(':')
		builder.WriteString(strconv.Itoa(int(entries[index].Level)))
	}
	if remaining := len(entries) - limit; remaining > 0 {
		builder.WriteString(",+")
		builder.WriteString(strconv.Itoa(remaining))
	}
	return builder.String()
}

// logDungeonPartyBuffSkillPage 写队友档的两条具名事件（线上 grep 锚点）：
//
//	game-dungeon-party-skill-page-send        一次队友 actor body 的技能页投递（全量等级向量）
//	game-dungeon-party-buff-skill-level-send  每一条已知队友增益技能一行：
//	                                          caster / target / skill_id / level_sent / 效果向量
//
// 只在"发给队友"的 body 上写（ctx 标记存在时）；发给本人的 body 一条都不写。
// `conn_id` 是**施法者**的会话（reader 持有的是被投影的 actor 的 session），
// caster/target 两个角色 id 都在字段里，判读不依赖 conn_id。
func (r *csharpLegacyUserInfoReader) logDungeonPartyBuffSkillPage(entries []peerSkillPageEntry) {
	if r == nil || r.service == nil {
		return
	}
	targetCharacterID, peerDirected := dungeonPartyPeerSkillPageTarget(r.ctx)
	if !peerDirected {
		return
	}
	casterCharacterID := strings.TrimSpace(r.characterID)
	mode := peerSkillPageModeNow().String()
	r.service.logGameEvent(r.session, "game-dungeon-party-skill-page-send",
		"caster_char_id", casterCharacterID,
		"target_char_id", targetCharacterID,
		"entry_count", len(entries),
		"entries", peerSkillPageEntriesText(entries, 24),
		"page_count", 2,
		"body_source", "a21_write_skill_page_layout",
		"env", peerSkillPageEnv,
		"mode", mode)
	levels := make(map[int64]byte, len(entries))
	for _, entry := range entries {
		levels[int64(entry.SkillID)] = entry.Level
	}
	for _, buff := range peerSkillPagePartyBuffSkills {
		level, present := levels[buff.SkillID]
		if !present {
			continue
		}
		r.service.logGameEvent(r.session, "game-dungeon-party-buff-skill-level-send",
			"caster_char_id", casterCharacterID,
			"target_char_id", targetCharacterID,
			"skill_id", buff.SkillID,
			"skill_name", buff.Name,
			"level_sent", level,
			"level_without_fix", 0,
			"effect_applied", buff.Stats,
			"effect_owner", "target_client_local_pvf",
			"pvf_path", buff.PVFPath,
			"env", peerSkillPageEnv,
			"mode", mode)
	}
}

// loadPeerSkillPageSkills 读**目标角色**（正在被生成 USERINFO 的那个角色，不是收包者）的技能行。
//
// 三个调用点手上的 csharpLegacyUserInfoReader 只有 legacy 仓储（userinfo_legacy_dispatch.go:58-67
// 的 repo 字段）和角色记录，技能行不在其中，必须现读一次：
//   - 访问范式复用同包邻居——userinfo_selected_subtype1.go:78-98 的 loadSubtype1EquippedEntries
//     用 r.service.repositoryGroup() 取仓储组、再用 r.ctx 读；
//   - 这次读额外套 createWriteTimeout（repository_timeouts.go:6，3s）做有界超时，
//     r.ctx 本身更短时以更短者为准，避免队友 USERINFO 被慢查询拖住；
//   - 任何失败（无仓储组、ctx 已取消、查库报错、角色没有技能行）都只记一条日志并降级为
//     空技能页，绝不阻断 USERINFO 本身的构造与发送。
//
// characterID 为空时回退到 reader 构造时登记的 charID（userinfo_current_entry.go:29/89/178/247，
// peer 路径传入的正是目标角色 id：peer_userinfo.go:57-65 的 targetID、
// dungeon_party_copresence.go:410-419 的 actor selectedCharacterID）。
func (r *csharpLegacyUserInfoReader) loadPeerSkillPageSkills(characterID string) map[int64]dnfrepo.SkillState {
	if r == nil || r.service == nil {
		return nil
	}
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		characterID = strings.TrimSpace(r.characterID)
	}
	if characterID == "" {
		return nil
	}
	repos, ok := r.service.repositoryGroup()
	if !ok || repos.Skill == nil {
		return nil
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	loadCtx, cancel := context.WithTimeout(ctx, createWriteTimeout)
	defer cancel()
	record, found, err := repos.Skill.Load(loadCtx, characterID)
	if err != nil {
		r.service.logGameEvent(r.session, "game-peer-skill-page-load-failed",
			"character_id", characterID,
			"error", err)
		return nil
	}
	if !found {
		return nil
	}
	// 技能页投递判读日志：A/B 时按 character_id 对照客户端 BUFF 等级。
	r.service.logGameEvent(r.session, "game-peer-skill-page-projected",
		"character_id", characterID,
		"learned_rows", len(record.Skills),
		"page_entries", len(peerSkillPageEntries(record.Skills)),
		"page_count", 2,
		"max_entries_per_page", peerSkillPageMaxEntries,
		"body_source", "a21_write_skill_page_layout")
	return record.Skills
}

// peerSkillPageTownEnv 是"城镇同屏/查看他人时要不要也写真实技能页"的开关。
// 默认档 = 关（逐字节等于现状）；只有显式 on/1/true/yes 才在城镇那条同屏路上打队友标记。
const peerSkillPageTownEnv = "DNFBRIDGE_PEER_SKILL_PAGE_TOWN"

func peerSkillPageTownEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(peerSkillPageTownEnv))) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}
