package dnfbridge

import (
	"context"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 账号级记录按需补齐（account-level record bootstrap）。
//
// 现场（2026-09-25 香港 192.0.2.12，`/opt/dnf90/logs/packet_log.txt`
// conn_id=game-000014，服务端日志同毫秒）：
//
//	16:37:42.960 INFO  login token bound game session account_id=dnf:10013
//	16:37:44.733 INFO  latest upper select character received; sending csharp init stream
//	16:37:45.042 WARN  handle game packet failed
//	                   error="initialize selected town actor before transition:
//	                          load crystal contract state: crystal contract account was not found"
//	                   ← 同一毫秒 game-close：服务端关连接，客户端表现为"一选角色就网络中断"
//
// 链路：sendUpperCSharpSelectInit →（进场景）
// sendCurrentTownPlayerStateLocked（initial_town_player_state.go:115）→
// sendCurrentCrystalContractStateOnce → sendCurrentCrystalContractState
// （crystal_contract.go:112）→ crystalcontract.Owner.State
// （modules/dnf/crystalcontract/owner.go:125-131）在 Account.Load 返回
// found=false 时回 ErrAccountNotFound；crystal_contract.go:118-120 包成
// "load crystal contract state: …"，再经 initial_town_route_packets.go:144 的
// "initialize selected town actor before transition: …" 冒到
// handleGameStreamPacket，最后在 game.go:379-384 被 read loop 当作致命错误
// **直接 return 关连接**。
//
// 成因不是本包逻辑，而是数据：角色被迁到同名启动器账号时只改了
// `dnf_characters.account_id`（见 90CN_账号串号-不同账号同角色-取证与修复报告.md
// 第四节），目标账号在 `dnf_accounts` / `dnf_account_inventories` 里没有任何行。
// 服务端自己的建号路径（character_create_owner.go:36-42 的
// dnfcharacterdata.Creation.Account）**会**建账号行，迁移路径不会——于是"账号行
// 缺失"这类会话必然在进场景那一步断线。
//
// 因此这里补的是"进场景所依赖的账号级前置记录"，口径收紧到三条：
//   - **只对这条会话已经定案的账号**调用：调用点必须已经解析出
//     `session.accountID`（登录令牌 / 本地 PID 登记），或者会话已经**实际选中**
//     了一个角色（此时 `accountIDForSession` 的共享兜底账号就是它要用的账号）。
//     未定案的探测连接绝不触发，避免凭空造账号行。
//   - **已存在 ⇒ 零写入**：MySQL 走原子 CreateIfAbsent，竞争失败返回
//     created=false；账号、metadata 和 updated_at 均不更新。
//   - **缺失 ⇒ 在同一物理事务内按服务端建档口径创建账号与 metadata**，并写
//     WARN（这是数据缺口，不是正常路径，必须能在服务端日志里数出来）。
//   - **AccountInventory 不预建**：所有读取方按空容器处理，首次真实共享物品
//     写入才创建，避免空壳覆盖并发首写。
//
// 绝不写 represent_account_name：`dnf_accounts` 上
// `uk_dnf_accounts_represent_account_name` 是唯一键，凭空猜一个名字会把别人的
// 名字撞掉（现场 `dnf:1` 上残留的 `represent_account_name='sample-account-a'` 正是无法逐行
// 归因的共享账号残留，同一份报告的 4.2 C）。冒险团名字仍由客户端 op1437/op1444
// 显式注册，本函数不代劳。
//
// 失败一律 fail-open：Load/Create/Save 出错只记 WARN 并返回 false，**绝不**把它变成
// 会话级错误——宁可少一条账号行，也不能让玩家连选角都进不去。

const (
	// accountRecordBootstrapMetadataKey 标记这一行是"登录/选角时按需补出来的"，
	// 与建号时正常建出来的行区分开，供运维直接查 dnf_account_metadata 判读。
	accountRecordBootstrapMetadataKey = "account_record_bootstrap"
	// accountRecordBootstrapSourceMetadataKey 记录触发补齐的调用点。
	accountRecordBootstrapSourceMetadataKey = "account_record_bootstrap_source"
	// accountRecordBootstrapState 与 character_create_owner.go 的建档口径一致。
	accountRecordBootstrapState = "active"
)

// accountRecordBootstrapSelectSource / …LoginSource 是 source 字段的取值，
// 让线上日志能直接判断是哪条入口补的。
const (
	accountRecordBootstrapLoginSource  = "login_token_account_bound"
	accountRecordBootstrapSelectSource = "select_character_before_scene_enter"
	accountRecordBootstrapLegacySource = "select_character_state_before_scene_enter"
)

// ensureAccountRecordsForSelection 保证这条会话"即将进入场景"的那个账号拥有
// 账号级记录。调用点：登录令牌绑定账号之后、以及两条选角入口在解析出角色之后。
//
// 返回值只用于日志/测试判读（true = 账号行可用），**不是**错误信号：
// 失败时调用方必须继续按原路径往下走。
func (s *Service) ensureAccountRecordsForSelection(
	ctx context.Context,
	session *gameSession,
	source string,
) bool {
	if s == nil || session == nil {
		return false
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Account == nil {
		return false
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return false
	}
	return s.ensureAccountLevelRecords(ctx, repos, session, accountID, source)
}

// ensureAccountLevelRecords 是上面那条口径的唯一实现，方便在登录入口与两条
// 选角入口之间共用（三处都调用它，重复调用是幂等的）。
func (s *Service) ensureAccountLevelRecords(
	ctx context.Context,
	repos dnfrepo.Group,
	session *gameSession,
	accountID string,
	source string,
) bool {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || repos.Account == nil {
		return false
	}
	_, found, err := repos.Account.Load(ctx, accountID)
	if err != nil {
		// 读失败不等于"行不存在"（可能是数据库瞬断）：只告警，不写行，
		// 也不把错误升级成会话错误。
		s.logWarn("dnfbridge account record bootstrap load failed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"account_id", accountID,
			"source", source,
			"error", err,
			"impact", "account row was not verified; selected-character enter continues unchanged")
		return false
	}
	if found {
		return true
	}
	now := s.gameplayNow().UTC()
	record := dnfrepo.AccountRecord{
		AccountID: accountID,
		State:     accountRecordBootstrapState,
		// 与 character_create_owner.go:36-42 的建档元数据同构（source=dnfbridge），
		// 额外两个键只用于把"迁移后补出来的行"在库里标出来。
		Metadata: map[string]string{
			"source":                                "dnfbridge",
			accountRecordBootstrapMetadataKey:       "true",
			accountRecordBootstrapSourceMetadataKey: source,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	created := false
	if writer, ok := repos.Account.(dnfrepo.AccountCreateIfAbsentWriter); ok {
		var createErr error
		created, createErr = writer.CreateIfAbsent(ctx, record)
		if createErr != nil {
			s.logWarn("dnfbridge account record bootstrap create-if-absent failed",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"account_id", accountID,
				"source", source,
				"error", createErr,
				"impact", "account row was not verified; selected-character enter continues unchanged")
			return false
		}
	} else {
		if err := repos.Account.Save(ctx, record); err != nil {
			s.logWarn("dnfbridge account record bootstrap save failed",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"account_id", accountID,
				"source", source,
				"error", err,
				"impact", "account row stays missing; the session continues and may still abort later")
			return false
		}
		created = true
	}
	if !created {
		// 另一个登录/选角入口已提交了完整账号行。它的 metadata 是权威值；
		// 本入口不得用预构造的 bootstrap 快照覆盖，也不再补容器。
		return true
	}
	// AccountInventory 完全惰性：现有读取方把缺失视为空容器，首次真实物品
	// 写入再 upsert。不要在这里写空壳，否则会与并发首写互相覆盖。

	// 这是数据缺口而非正常路径 ⇒ WARN + packet 事件双写：WARN 不受包日志缓冲
	// 影响（可能没开包日志），packet 事件带 conn_id 便于与那次选角对齐。
	s.logWarn("dnfbridge account record was missing and is created on demand",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"account_id", accountID,
		"source", source,
		"account_created", true,
		"represent_account_name", "",
		"reason", "account_level_row_missing_would_abort_selected_character_enter",
		"impact", "crystal contract / adventure group / cargo account loads would abort the game connection")
	s.logGameEvent(session, "game-account-record-created-on-demand",
		"account_id", accountID,
		"source", source,
		"state", accountRecordBootstrapState,
		"represent_account_name_written", false,
		"reason", "account_level_row_missing_would_abort_selected_character_enter")
	return true
}
