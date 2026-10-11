package gmtool

// D：角色改名（POST /api/characters/{id}/rename）。
//
// # 为什么要这一条路由（操作者批次 3 项 ①）
//
// 选角界面上被标成"违反用户协议"的角色会弹「请修改您的角色名!!」，点【修改】后
// **改名永远不生效**。根因不在本文件：客户端改名请求走游戏信道 class1/op135
// （ENUM_CMDPACKET_CHANGE_CHARAC_NAME），而本服务端与**厂商参考实现都**没有这条
// opcode 的 handler，因此没有可照抄的 S2C 回包形态，回包只能先取证再写
// （_dist/rename-delete-diagnosis.md §2.4/§2.5/§2.7）。
//
// 但本面板的移植源（上游 A21 GM 工具 `Temp\gm-upstream-v260910\`）**有**角色改名，
// 走的是 HTTP 面板接口、与 op135 无关。把这条现成实现补进面板，就能让操作者今天
// 就把那个不可用的角色（test78_copy / id 15 / dnf:10001）改成合法名，
// 全程零协议发明。客户端 op135 那条链路仍按 §2.7 取证后再做（本文件不碰它）。
//
// # 端点契约
//
//	POST /api/characters/{id}/rename
//	body   {"newName":"test88","expectedName":"test78_copy","characterOffline":true}
//	响应   恒 HTTP 200；成功 {"success":true,...}，失败 {"success":false,"error":"..."}
//
// 响应字段（成功）：
//
//	characterId  int    面板角色号（= 路径里的 id，与上游返回的 characterId 同义）
//	accountId    int    面板账号号；accountName string 启动器账号 id
//	name         string 改名**之后**的角色名（提交后重读得到的库里权威值）
//	previousName string 改名之前的角色名
//	applied      bool   本次是否真的写了库（幂等/并发已改好 = false）
//	idempotent   bool   本次是否命中"新名与旧名相同"的幂等短路
//	verified     bool   提交后重读回来确认 name 已经是新名
//	renameMode   string 恒 "enabled"（关闭档下这条路由根本不注册，见下）
//	notification string 恒 "character_renamed"
//	hint         string 一句人能读懂的说明
//
// 失败一律 `{"success":false,"error":"<中文原因>（未做任何写入）。"}` —— 与删除角色
// 同一条契约（本仓没有第二个业务失败码，绝不引入 4xx，见 online_guard.go:112-116）。
//
// # 开关（默认启用，可一键回退）
//
//	`DNFBRIDGE_GM_CHARACTER_RENAME_MODE`
//	  未设置 / 空 / 任何未知值 = **启用**（默认；本条路由注册，行为如上）
//	  `off` / `0` / `false` / `no` / `disable` / `disabled` / `none` / `legacy`
//	                           = **关闭**：routes() 里**一行都不注册**，请求继续落
//	                             `/api/` 兜底「该功能未移植到 90CN 面板…」，
//	                             与加这条路由之前**逐字节相同**。
//
// 解析见 CharacterRenameFromEnv；"默认启用 + 显式 off 字面量回退"与
// api_quest_mutations.go:110 的 QuestMutateFromEnv、api_character_delete.go:330 的
// chardelHardModeFromEnv 同一约定（新路由默认可用，回退必须写成显式字面量）。
// 开关自读环境（`os.Getenv`），不动改动最密集的 dnfbridge/options.go
// （online_guard.go:65-73 已经定下这个取舍）。
//
// # 与上游 `Services/GmService.CharacterRename.cs`（67 行）的逐段对拍
//
//	:18      在线守卫（request.CharacterOffline 必须为 true）
//	         → 本仓同（body.characterOffline），**并且**额外探一次真实对局会话
//	           （见 D2）。上游只信面板传来那个布尔值，本仓有真探针就不用白不用。
//	:19-21   Trim() + ValidateCharacterName → 本仓同（renameValidateCharacterName，
//	         规则逐条对齐 GmService.CharacterClone.cs:924-936，见 D3）。
//	:23      单事务 → 本仓同：`Group.CharacterSettlement.WithinCharacterSettlement`
//	         （api_quest_mutations.go:520/658/911/1100/1328 用的同一条仓储边界；
//	         mysql 侧是 BEGIN…COMMIT，内存侧是"全部缓冲后统一提交"）。
//	:28-32   事务内 `SELECT name FROM characters WHERE character_id=@id AND delete_flag=0`，
//	         空 → 角色不存在或已删除 → 本仓同：事务内 `tx.Character.Load` +
//	         delete_flag 判据（面板所有读面都用 delete_flag=0，见 api_character_delete.go
//	         文件头第 3 条）。
//	:34-35   乐观并发：ExpectedName 不符 → 「角色名字已变化，请刷新角色列表后重试」
//	         → 本仓同，且**查两遍**（进事务前一次用面板已加载的记录快速拒绝，
//	           事务内再用事务里重读的记录判一次；事务内那次才是权威判定）。
//	:36      幂等：新名与旧名相同直接返回成功 → 本仓同，并且是**零写入**：
//	         不开事务、不调 Save、不刷 UpdatedAt（上游这时已经开了事务但没写行）。
//	:37      查重 CharacterNameExists → 「角色名已存在：<name>」 → 本仓同口径，
//	         但走本仓自己的真源（见 D1）。
//	:41-44   `UPDATE characters SET name=@bytes,name_bytes=@bytes WHERE …`，
//	         ExecuteNonQuery()!=1 判失败 → 本仓同语义、不同实现（见 D4）。
//	:45-57   **同一事务内维护好友图两方向**（united_friend_relations 以名字为键，
//	         owner_name/friend_name 双向 CASE 改写）→ 本仓**没有对应表**：
//	         全仓 grep 不到 united_friend_relations / friend_name / dnf_friend*
//	         （dnf_characters 之外没有任何持久化好友图；90CN 的"加好友"是
//	         dnfbridge/online_social.go 的会话级 NOTI，不落库）。因此**没有任何行
//	           需要改写**：这一条不是省略，而是"表不存在 ⇒ 无事可做"，
//	           绝不凭空造一张表出来写（AGENTS.md：不为不确定的数据凭空造）。
//	:58-59   Commit → {success, characterId, name} → 本仓同 + 额外字段（见契约）。
//	:61-64   异常 → 回滚 + 「改名失败，修改已回滚…」 → 本仓同：事务边界负责回滚，
//	         失败文案带「（未做任何写入）」，与删除角色一致。
//
// # 本仓与上游的差异（全部是收紧或"跟随本仓真源"，逐条给理由）
//
//	D1 查重口径 = `group.Character.FindIDByName`（全局、delete_flag=0），
//	   不是上游那三列 OR。理由：那正是本仓客户端「检查角色名是否重复」（op692，
//	   dnfbridge/character_repository.go:30-36 → character_create_transport.go:73）
//	   用的**同一个方法**，也就是"客户端认为重名"的那把尺子；用别的尺子会出现
//	   「GM 说能改、客户端说重名」或反之。作用域：FindIDByName **不限账号**，
//	   与上游一致（上游 CharacterNameExists 也是全表 COUNT）——账号内重名是它的
//	   子集，因此"账号内唯一 + 全服唯一"两条都被覆盖。数据库层**没有**唯一约束
//	   （schema.go:239-240 只有 uk_dnf_characters_account_slot_active 与
//	   非唯一的 KEY idx_dnf_characters_name），所以这条唯一性由应用层保证，
//	   与上游同层；本文件如实记录这一点，不假装 DB 会兜底。
//	D2 在线判据 = 上游的 characterOffline 布尔 **加** 本进程真实对局会话探针
//	   （Deps.Online → dnfbridge 的 onlineGameSession）。探针说在线就拒绝，
//	   **不看 cfg.RequireOffline**：理由同删除角色（api_character_delete.go:193-199）
//	   —— 在线会话把整条 CharacterRecord 放在内存里，任何一次存档都会把内存里的
//	   记录写回来，把刚改好的名字原地改回去。探针未注入时安全降级为"未知/不在线"
//	   + 具名日志（online_guard.go:27-29 的既定口径），绝不因缺探针拒绝一切。
//	D3 名字规则 = 上游 GmService.CharacterClone.cs:924-936 的**逐条**移植：
//	   非空（TrimSpace）、GBK(936) 往返必须一致、不得含 NUL/控制字符、
//	   GBK 字节数 2..12。⚠️ 注意它与本面板 `GET /api/characters/name-available`
//	   （api_clone.go:90-91 的 2..18 字节 UTF-8）口径不同：那一条是**克隆**路径，
//	   2..18 是它的既有契约，本批**不动**。两者都能表达"6 个中文"，
//	   差别只在纯 ASCII 的 7..18 字节（克隆允许、改名按客户端 GBK 口径拒绝）。
//	   改名按客户端口径（12 字节）是刻意的：面板改完的名字要能被客户端正常显示。
//	D4 写库 = `dnfrepo.SaveCharacterFields(tx.Character, …, CharacterFieldBase)`。
//	   mysql 的 SaveFields 是 UPSERT（mysql_char.go:421 buildUpsert），因此上游
//	   "影响行数 != 1 即角色不存在"那种 fail-closed **不能照抄**；本仓改为
//	   "事务内先 Load，found=false 直接拒绝"——角色行不存在时**根本走不到写**，
//	   不会因为 upsert 凭空建出一行残缺角色。提交后再重读复核（verified 字段）。
//
// # 零写入保证
//
// 所有拒绝路径（确认位缺失、名字非法、ExpectedName 不符、幂等、重名、在线、
// 事务能力缺失）都发生在**调用 Save 之前**，事务边界保证回滚。成功路径只有
// 一次 SaveCharacterFields（一行 UPDATE，一个提交点）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/encoding/simplifiedchinese"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// renameModeEnv 是角色改名路由的开关名。**默认启用**；只有下面
	// CharacterRenameFromEnv 列出的显式字面量才关闭（= 路由不注册 =
	// 回到加这条路由之前的行为）。
	renameModeEnv = "DNFBRIDGE_GM_CHARACTER_RENAME_MODE"

	// renameNameMinBytes / renameNameMaxBytes 逐字移植上游
	// GmService.CharacterClone.cs:933-934 的 GBK(936) 字节数区间。
	renameNameMinBytes = 2
	renameNameMaxBytes = 12
)

// 拒绝文案。上游原文逐字照抄（改名的失败文案是对外契约的一部分：面板前端会把
// error 原样 toast 给操作者，测试也按这几句断言）。
const (
	renameErrNameEmpty   = "角色名不能为空"
	renameErrNameNotGBK  = "角色名包含 GBK（936）无法表示的字符"
	renameErrNameControl = "角色名不能包含控制字符"
	renameErrNameLength  = "角色名长度需为 2–12 字节：最多 6 个中文（全角）或 12 个字母、数字（半角），混合时全角按 2 字节计算"
	renameErrOffline     = "请确认当前角色不在线"
	renameErrExpected    = "角色名字已变化，请刷新角色列表后重试"
	renameErrGone        = "角色不存在或已删除"
	renameErrRepo        = "角色仓储未就绪"
	renameErrTxMissing   = "角色改名事务能力未就绪（Group.CharacterSettlement 未装配），已拒绝"
	renameErrOnline      = "角色正在游戏内在线，拒绝改名"

	// renameErrNameTakenPrefix 与上游 :37 的 `"角色名已存在：" + name` 逐字一致。
	renameErrNameTakenPrefix = "角色名已存在："

	// renameErrExpectedMissing 是"少给一个字段"这种**契约层面**的拒绝，
	// 上游没有对应文案（ASP.NET 会把缺省字段绑成 null，然后 ExpectedName 比较
	// 必然失败，报的是"名字已变化"——对操作者是误导）。这里说清楚。
	renameErrExpectedMissing = "缺少 expectedName（乐观并发要求逐字给出当前角色名，未做任何写入）。"
)

// CharacterRenameFromEnv 解析 DNFBRIDGE_GM_CHARACTER_RENAME_MODE。
//
// 与 QuestMutateFromEnv（api_quest_mutations.go:110）同一条默认档约定：
// **空 = on**（新路由默认可用）；只有显式 off/0/false/no/disable/disabled/none/legacy
// 才关闭。未知值（笔误）取默认档 on —— 与"回退必须写成显式字面量"一致。
func CharacterRenameFromEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no", "disable", "disabled", "none", "legacy":
		return false
	default:
		return true
	}
}

// CharacterRenameFromEnvironment 读取进程环境里的同一个开关。
func CharacterRenameFromEnvironment() bool {
	return CharacterRenameFromEnv(os.Getenv(renameModeEnv))
}

// characterRenameEnabled 是路由注册与 handler 共用的同一个判断（不重写第二套）。
func (s *Server) characterRenameEnabled() bool {
	return CharacterRenameFromEnvironment()
}

// renameRequest 是请求体。字段名与上游 CharacterRenameRequest 的 JSON 契约
// （ASP.NET Core 默认 camelCase）逐字一致：newName / expectedName / characterOffline。
//
// ExpectedName 用指针：本仓既有手法（api_sp_absolute.go:75-80 firstInt64）是
// "用指针区分『没给』与『给了零值』"。"没给"和"给错"都要拒绝，但原因要说清楚，
// 否则操作者会以为是名字真的变了。
type renameRequest struct {
	NewName          string  `json:"newName"`
	ExpectedName     *string `json:"expectedName"`
	CharacterOffline bool    `json:"characterOffline"`
}

// renameRejection 是"拒绝、零写入"的结构化原因：message 原样回给面板，
// reason 进具名审计事件供自动化断言（与 questMutationRejection 同一形态）。
type renameRejection struct {
	reason  string
	message string
}

func (e *renameRejection) Error() string { return e.message }

// renameValidateCharacterName 逐条移植上游 GmService.CharacterClone.cs:924-936。
// 返回空串 = 合法；否则返回可直接回给前端的失败文案。
//
// 顺序与上游完全相同（空 → GBK 往返 → 控制字符 → 长度），因为文案与顺序都是
// 可观察行为：\x01 这类控制字符能正常往返 GBK，必须由第三条拦住。
func renameValidateCharacterName(name string) string {
	if strings.TrimSpace(name) == "" {
		return renameErrNameEmpty
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(name))
	if err != nil {
		return renameErrNameNotGBK
	}
	// 往返一致 = 这个字符串真的能被客户端按 GBK(936) 表达出来（无法表示的字符
	// 会被编码器换成 '?'，或解码成 U+FFFD，两种都会让往返比较失败）。
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(encoded)
	if err != nil || string(decoded) != name {
		return renameErrNameNotGBK
	}
	if strings.ContainsRune(name, '\x00') {
		return renameErrNameNotGBK
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return renameErrNameControl
		}
	}
	if len(encoded) < renameNameMinBytes || len(encoded) > renameNameMaxBytes {
		return renameErrNameLength
	}
	return ""
}

// renameRejectionReason 把校验文案映射成稳定的 reason 串（审计聚合用）。
func renameRejectionReason(message string) string {
	switch message {
	case renameErrNameEmpty:
		return "name_empty"
	case renameErrNameNotGBK:
		return "name_not_gbk936"
	case renameErrNameControl:
		return "name_control_character"
	case renameErrNameLength:
		return "name_length_out_of_range"
	default:
		return "name_invalid"
	}
}

// renamePrecheck 是"进事务之前"的快速预检（零写入）：角色还在不在、
// ExpectedName 是否相符。返回 nil = 通过（仍要查重、在线探针、写库）。
func renamePrecheck(character dnfrepo.CharacterRecord, expected string) *renameRejection {
	if statInt(character, "delete_flag") != 0 {
		return &renameRejection{reason: "character_deleted", message: renameErrGone}
	}
	if character.Name != expected {
		return &renameRejection{reason: "expected_name_changed", message: renameErrExpected}
	}
	return nil
}

func (s *Server) handleCharacterRename(w http.ResponseWriter, r *http.Request) {
	// 顺序与上游 :18-23 一致：解析角色 → 校验请求体 → 在线守卫 → 单事务。
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[renameRequest](w, r)
	if !ok {
		return
	}
	// 上游 :18：request == null || !request.CharacterOffline → 「请确认当前角色不在线」。
	if !body.CharacterOffline {
		s.rejectRename(w, panelID, character, &renameRejection{
			reason:  "offline_confirmation_missing",
			message: renameErrOffline,
		})
		return
	}
	// 上游 :19 先 Trim() 再校验，落库的也是 Trim 之后的值。
	newName := strings.TrimSpace(body.NewName)
	if message := renameValidateCharacterName(newName); message != "" {
		s.rejectRename(w, panelID, character, &renameRejection{
			reason:  renameRejectionReason(message),
			message: message,
		})
		return
	}
	if body.ExpectedName == nil {
		s.rejectRename(w, panelID, character, &renameRejection{
			reason:  "expected_name_missing",
			message: renameErrExpectedMissing,
		})
		return
	}
	// 上游 :34 是**逐字**比较（StringComparison.Ordinal），这里不做 Trim。
	s.renameApply(w, ctx, group, panelID, character, *body.ExpectedName, newName)
}

// rejectRename 写出一条"拒绝、零写入"的响应 + 具名审计事件；返回 true
// 表示调用者必须立刻 return（永远返回 true，存在的意义是让调用点写法统一）。
func (s *Server) rejectRename(
	w http.ResponseWriter,
	panelID int,
	character dnfrepo.CharacterRecord,
	rejection *renameRejection,
) bool {
	if rejection == nil {
		return false
	}
	s.logEvent("gm-character-rename-rejected",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"name", character.Name,
		"reason", rejection.reason,
		"result", "rejected")
	writeFailure(w, rejection.message+"（未做任何写入）。")
	return true
}

// renameNameTaken 报告 newName 是否已被**别的**角色占用。
//
// 口径见文件头 D1：与客户端 op692 查重同一个方法（FindIDByName，全局、
// delete_flag=0）。found 返回的就是**自己**时不算冲突（防御性：正常流程走不到，
// 因为"新名 == 旧名"已经被幂等短路了）。
func (s *Server) renameNameTaken(
	ctx context.Context,
	group dnfrepo.Group,
	selfCharacterID string,
	name string,
) (bool, error) {
	if group.Character == nil {
		return false, errors.New(renameErrRepo)
	}
	existing, found, err := group.Character.FindIDByName(ctx, name)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return strings.TrimSpace(existing) != strings.TrimSpace(selfCharacterID), nil
}

// renameApply 是"请求体已过检"之后的全部行为（单独抽出来，方便单测直接调用）。
func (s *Server) renameApply(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	panelID int,
	character dnfrepo.CharacterRecord,
	expected string,
	newName string,
) {
	if rejection := renamePrecheck(character, expected); rejection != nil {
		s.rejectRename(w, panelID, character, rejection)
		return
	}
	previous := character.Name

	// 幂等短路（上游 :36 同义），但**零写入**：连事务都不开。
	if previous == newName {
		s.finishRename(w, ctx, group, panelID, character, previous, newName, false, true)
		return
	}
	if group.Character == nil {
		s.logEvent("gm-character-rename-failed",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", character.AccountID,
			"reason", "character_repository_missing", "result", "failed")
		writeFailure(w, renameErrRepo)
		return
	}

	// 查重（进事务前的快速拒绝；事务内还会再判一次，那次才是权威的）。
	if taken, err := s.renameNameTaken(ctx, group, character.CharacterID, newName); err != nil {
		s.logEvent("gm-character-rename-failed",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", character.AccountID,
			"reason", "name_lookup_failed", "error", err.Error(), "result", "failed")
		writeFailure(w, "改名失败: 无法确认角色名是否重复: "+err.Error())
		return
	} else if taken {
		s.rejectRename(w, panelID, character, &renameRejection{
			reason:  "name_taken",
			message: renameErrNameTakenPrefix + newName,
		})
		return
	}

	// 在线守卫（本仓收紧，见 D2）：**不看 cfg.RequireOffline**，探针说在线就拒绝。
	// 理由与删除角色相同：在线会话内存里的 CharacterRecord 会在下一次存档时把
	// 名字写回来（api_character_delete.go:193-199）。
	if status := s.characterOnline(chardelProbeID(panelID)); status.known {
		if status.online {
			s.logEvent("gm-character-rename-rejected",
				"panel_id", panelID, "character_id", character.CharacterID,
				"account_id", character.AccountID, "name", previous,
				"reason", "character_online", "result", "rejected")
			writeFailure(w, fmt.Sprintf(
				"%s：请先在客户端切到别的角色（退出该角色/回到选人界面）再改名（未做任何写入）。",
				renameErrOnline))
			return
		}
	} else {
		s.logEvent("gm-character-rename-probe-missing",
			"panel_id", panelID, "character_id", character.CharacterID,
			"reason", "probe_not_injected_or_out_of_range")
	}

	if group.CharacterSettlement == nil {
		s.logEvent("gm-character-rename-failed",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", character.AccountID,
			"reason", "settlement_transaction_unavailable", "result", "failed")
		writeFailure(w, renameErrTxMissing)
		return
	}

	applied := false
	err := group.CharacterSettlement.WithinCharacterSettlement(ctx, character.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil {
			return errors.New(renameErrRepo)
		}
		record, found, loadErr := tx.Character.Load(ctx, character.CharacterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return &renameRejection{reason: "character_missing", message: renameErrGone}
		}
		if statInt(record, "delete_flag") != 0 {
			return &renameRejection{reason: "character_deleted", message: renameErrGone}
		}
		// 防串号：事务里的记录必须仍属于请求解析出来的那个账号。
		if owner := strings.TrimSpace(record.AccountID); owner != "" && owner != character.AccountID {
			return &renameRejection{reason: "character_account_changed", message: renameErrGone}
		}
		// 乐观并发（权威判定）：事务里重读到的名字必须仍等于调用者给的那一个。
		if record.Name != expected {
			return &renameRejection{reason: "expected_name_changed", message: renameErrExpected}
		}
		if record.Name == newName {
			// 并发下另一个请求已经改好了：本次不写库，仍然算成功（幂等）。
			return nil
		}
		if taken, takenErr := s.renameNameTaken(ctx, group, character.CharacterID, newName); takenErr != nil {
			return takenErr
		} else if taken {
			return &renameRejection{reason: "name_taken", message: renameErrNameTakenPrefix + newName}
		}
		updated := dnfrepo.CloneCharacter(record)
		updated.Name = newName
		updated.UpdatedAt = time.Now().UTC()
		// 唯一的一次写：一行 UPDATE（base 字段含 name），提交点由事务边界负责。
		if saveErr := dnfrepo.SaveCharacterFields(ctx, tx.Character, updated, dnfrepo.CharacterFieldBase); saveErr != nil {
			return saveErr
		}
		applied = true
		return nil
	})
	if err != nil {
		var rejection *renameRejection
		if errors.As(err, &rejection) {
			s.rejectRename(w, panelID, character, rejection)
			return
		}
		s.logEvent("gm-character-rename-failed",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", character.AccountID,
			"name", previous, "new_name", newName,
			"reason", "write_failed", "error", err.Error(), "result", "failed")
		writeFailure(w, "改名失败（修改已回滚，库里没有任何变化）: "+err.Error())
		return
	}
	s.finishRename(w, ctx, group, panelID, character, previous, newName, applied, false)
}

// finishRename 是成功路径的统一收尾：提交后重读复核 → 具名事件 → 响应。
//
// 为什么非要在提交后重读：事务提交只证明"我们发过 UPDATE"，不证明"库里现在是新名"
// （比如 DSN 指到别的库、或并发又把它改回去）。读回来不一致时**不谎报成功**：
// 响应里带 verified=false 与一句明确提示，同时记 gm-character-rename-unverified。
func (s *Server) finishRename(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	panelID int,
	character dnfrepo.CharacterRecord,
	previous string,
	newName string,
	applied bool,
	idempotent bool,
) {
	verified := false
	authoritative := newName
	reloaded, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		s.logEvent("gm-character-rename-unverified",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", character.AccountID,
			"new_name", newName, "reason", "reload_failed", "error", err.Error())
	} else {
		authoritative = reloaded.Name
		verified = reloaded.Name == newName
		if !verified {
			s.logEvent("gm-character-rename-unverified",
				"panel_id", panelID, "character_id", character.CharacterID, "account_id", character.AccountID,
				"new_name", newName, "stored_name", reloaded.Name, "reason", "stored_name_differs")
		}
	}

	result := "noop"
	if applied {
		result = "applied"
	}
	s.logEvent("gm-character-rename",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"previous_name", previous,
		"name", authoritative,
		"applied", applied,
		"idempotent", idempotent,
		"verified", verified,
		"uniqueness_scope", "global_find_id_by_name_op692_source",
		"friend_graph_rewrite", "not_applicable_no_persisted_friend_table",
		"result", result)

	payload := map[string]any{
		"characterId":   panelIDOfCharacter(character),
		"accountId":     panelIDFromAccountID(character.AccountID),
		"accountName":   character.AccountID,
		"name":          authoritative,
		"previousName":  previous,
		"applied":       applied,
		"idempotent":    idempotent,
		"verified":      verified,
		"renameMode":    "enabled",
		"notification":  "character_renamed",
		"uniquenessKey": "character_id",
		"hint": "角色名已在**单个事务**里改成新名（dnf_characters.name；角色号/账号/槽位不变）。" +
			"查重与客户端 op692「检查角色名是否重复」同源（FindIDByName，全服 + 账号内都唯一）。" +
			"上游的好友图双向改写在本仓**不适用**：90CN 没有持久化好友关系表（加好友是会话级 NOTI）。",
	}
	if !verified {
		payload["hint"] = "写入已提交，但提交后重读没有确认到新名（见 verified=false）：请刷新角色列表核对；" +
			"若列表里仍是旧名，请把该角色退回选人界面后重试（改名是幂等的，重试不会再写一次）。"
		payload["warning"] = "rename_unverified"
	}
	writeOK(w, payload)
}
