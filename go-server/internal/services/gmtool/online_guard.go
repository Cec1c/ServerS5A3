package gmtool

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/adminapi"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Independent GM queries the private runtime API and rejects guarded writes
// when it cannot establish an offline state. The legacy callback mode remains
// for fixtures; its historical opt-in behavior is preserved. Existing route
// guards retain their scope; this does not claim an account-wide login lease.
const requireOfflineEnv = "DNFBRIDGE_GM_REQUIRE_OFFLINE"

// RequireOfflineFromEnv 解析 DNFBRIDGE_GM_REQUIRE_OFFLINE。
// **只有显式 `on`（大小写不敏感、两端裁剪后逐字节相等）才启用**；未设置/空/其它
// 任何值（含 `true`/`1`/`yes`/`off`）都取默认档 false = 不检查在线。
func RequireOfflineFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

// RequireOfflineFromEnvironment 读取进程环境里的同一个开关。
//
// 仅保留兼容调用入口。独立 GM 装配直接启用 RequireOffline，不能用此环境变量关闭。
func RequireOfflineFromEnvironment() bool {
	return RequireOfflineFromEnv(os.Getenv(requireOfflineEnv))
}

// onlineStatus 是探针结果的三态：`known=false` 表示探针没被注入（能力缺失），
// 与"探针明确说不在线"区分开，便于日志判读与后续批次复用。
type onlineStatus struct {
	online bool
	known  bool
}

// characterOnline 询问注入的在线探针。未注入时返回 {false,false}。
func (s *Server) characterOnline(characterID uint16) onlineStatus {
	if s != nil && s.deps.Runtime != nil {
		result, err := s.deps.Runtime.Call(context.Background(), adminapi.Request{Action: "online", CharacterID: characterID})
		if err != nil {
			return onlineStatus{}
		}
		return onlineStatus{online: result.Online, known: true}
	}
	if s == nil || s.deps.Online == nil {
		return onlineStatus{}
	}
	return onlineStatus{online: s.deps.Online(characterID), known: true}
}

// probeCharacterID 把面板主键换算成对局会话表的 uint16 角色号。
// 超过 uint16 或 <=0 时返回 false = 无法探测（与上游 gmCharacterID 的
// `strconv.ParseUint(value, 10, 16)` 同界，gm.go:2174-2181）。
func probeCharacterID(panelID int) (uint16, bool) {
	if panelID <= 0 || panelID > math.MaxUint16 {
		return 0, false
	}
	return uint16(panelID), true
}

// logEvent 记一条具名事件。cfg.Logger 由 New 保证非 nil，这里再兜一次底，
// 好让测试里手工构造的 Server 也不会因为日志 panic。
func (s *Server) logEvent(kind string, fields ...any) {
	if s == nil || s.cfg.Logger == nil {
		return
	}
	s.cfg.Logger(kind, fields...)
}

// requireOffline 是写路由的在线守卫。返回 false 表示**已经写出失败响应**，
// 调用者必须立刻 return。
//
// 失败形态遵守本仓契约：恒 HTTP 200 + `success:false`（绝不引入 409 之类的新
// 业务失败码，否则会打断 wwwroot/js/core.js:19-23 的统一处理）。
//
// 默认档（cfg.RequireOffline == false，即 DNFBRIDGE_GM_REQUIRE_OFFLINE 未设/空/
// 非 `on`）：直接放行，不探针、不写日志 —— 逐字节今天的行为。
func (s *Server) requireOffline(w http.ResponseWriter, action string, panelID int) bool {
	if s == nil || !s.cfg.RequireOffline {
		return true
	}
	characterID, probeable := probeCharacterID(panelID)
	if !probeable {
		if s.deps.Runtime != nil {
			writeFailure(w, "无法确认角色在线状态，已拒绝写入")
			return false
		}
		s.logEvent("gm-online-guard-probe-missing", "action", action, "panel_id", panelID,
			"reason", "character_id_out_of_uint16_range")
		return true
	}
	status := s.characterOnline(characterID)
	if !status.known {
		if s.deps.Runtime != nil {
			writeFailure(w, "游戏管理接口不可用，无法确认离线，已拒绝写入")
			return false
		}
		s.logEvent("gm-online-guard-probe-missing", "action", action, "panel_id", panelID,
			"character_id", characterID, "reason", "probe_not_injected")
		return true
	}
	if !status.online {
		return true
	}
	s.logEvent("gm-online-guard-rejected", "action", action, "panel_id", panelID,
		"character_id", characterID, "reason", "character_online")
	writeFailure(w, fmt.Sprintf(
		"角色 %d 正在游戏内在线，已拒绝本次写入；请让玩家退到选人界面后重试。",
		panelID))
	return false
}

// requireAccountOffline 是**账号级**写路由的在线守卫：账号内只要有任意一个角色
// 仍在对局会话里就拒绝。上游的对应实现是 `gmAccountOnline`（gm_extended.go:909-923），
// 调用点 GMAccountCubes(UP :441) 与 GMSetAccountValue(UP :174-178)。
//
// 为什么要账号级：账号共享槽（晶块 354-359、灵魂 360-365）与账号金库金币都被在线
// 会话的内存投影缓存着，只写库会让在线客户端继续看到旧值（UP :420-421 的注释）。
//
// 与 requireOffline 完全同一条契约：
//   - 默认档（cfg.RequireOffline=false）直接放行：不拉角色列表、不探针、不写日志；
//   - 失败形态恒 HTTP 200 + success:false；
//   - 独立 Runtime 模式遇到探针失败、无效身份或不完整角色列表时拒绝写入；
//   - 旧回调模式保留原来的缺失探针放行与日志行为，仅用于兼容调用。
//
// 返回 false 表示已经写出失败响应，调用者必须立刻 return。
func (s *Server) requireAccountOffline(
	ctx context.Context,
	w http.ResponseWriter,
	action string,
	group dnfrepo.Group,
	accountID string,
	panelID int,
) bool {
	if s == nil || !s.cfg.RequireOffline {
		return true
	}
	accountID = strings.TrimSpace(accountID)
	if s.deps.Runtime != nil {
		if accountID == "" || group.Character == nil {
			writeFailure(w, "无法核对账号角色，已拒绝写入")
			return false
		}
		records, err := group.Character.ListByAccount(ctx, accountID, dnfrepo.DefaultCharacterSlotLimit+1)
		if err != nil || len(records) > dnfrepo.DefaultCharacterSlotLimit {
			writeFailure(w, "无法完整核对账号角色，已拒绝写入")
			return false
		}
		for _, record := range records {
			id, err := strconv.Atoi(record.CharacterID)
			if err != nil || record.AccountID != accountID {
				writeFailure(w, "账号角色数据不一致，已拒绝写入")
				return false
			}
			if !s.requireOffline(w, action, id) {
				return false
			}
		}
		return true
	}
	if accountID == "" {
		s.logEvent("gm-online-guard-probe-missing", "action", action, "panel_id", panelID,
			"reason", "account_id_missing")
		return true
	}
	if group.Character == nil {
		s.logEvent("gm-online-guard-probe-missing", "action", action, "panel_id", panelID,
			"account_id", accountID, "reason", "character_repository_missing")
		return true
	}
	records, err := group.Character.ListByAccount(ctx, accountID, dnfrepo.DefaultCharacterSlotLimit)
	if err != nil {
		s.logEvent("gm-online-guard-probe-missing", "action", action, "panel_id", panelID,
			"account_id", accountID, "reason", "character_list_failed", "error", err.Error())
		return true
	}
	online := make([]string, 0, 4)
	for _, record := range records {
		panelCharacterID, parseErr := strconv.Atoi(strings.TrimSpace(record.CharacterID))
		if parseErr != nil {
			continue
		}
		characterID, probeable := probeCharacterID(panelCharacterID)
		if !probeable {
			continue
		}
		status := s.characterOnline(characterID)
		if !status.known {
			s.logEvent("gm-online-guard-probe-missing", "action", action, "panel_id", panelID,
				"account_id", accountID, "character_id", characterID, "reason", "probe_not_injected")
			return true
		}
		if status.online {
			online = append(online, record.CharacterID)
		}
	}
	if len(online) == 0 {
		return true
	}
	s.logEvent("gm-online-guard-rejected", "action", action, "panel_id", panelID,
		"account_id", accountID, "online_characters", strings.Join(online, ","),
		"reason", "account_character_online")
	writeFailure(w, fmt.Sprintf(
		"账号 %s 内仍有角色在线（角色 %s），账号级数据会被在线会话缓存，已拒绝本次写入；请让玩家退到选人界面后重试。",
		accountID, strings.Join(online, ", ")))
	return false
}
