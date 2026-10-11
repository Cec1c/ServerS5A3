package gmtool

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// J8：SP/TP **绝对值**设置（上游 `gm_extended.go:261-315` GMSetCharacterSkillBonus）。
//
// 为什么必须是新端点、而不是改既有路由 15：本仓 `POST /api/characters/{id}/sp`
// （api_characters.go:379 handleAdjustSpTp）的契约是**增量**（把输入加进
// bonus_sp/bonus_tp），上游是**绝对值**。改既有语义会改变既有前端与既有钉死用例
// 看到的行为（计划书 §5.2 C7），所以这里新增 `POST /api/characters/{id}/sp/set`，
// 路由 15 一个字节都不动。
//
// 与上游 `gm_extended.go:261-315` 的逐段对拍：
//
//	:262-265  角色号解析 + bonusSP/bonusTP 必须在 0..65535（越界 → 拒绝，零写入）
//	:266-268  角色在线 → 拒绝（本仓走 J0 守卫；默认档不检查，见 online_guard.go）
//	:269-272  事务能力缺失 → 明确失败（本仓 group.CharacterProgression）
//	:273      单事务 CharacterProgression.WithinCharacterProgression（同一仓储边界）
//	:274-280  事务内重载角色；不存在 → 拒绝
//	:281-287  事务内重载技能账（Skill.Load）；**缺失 → 拒绝**（不像 GMCharacterDetail
//	          :250 那样合成空账本 —— 那次是只读投影，这次是写）
//	:288-293  克隆角色/技能账；读旧 bonus_sp/bonus_tp 作为「旧值」
//	:294-296  按差额平移 TotalSP/RemainingSP/TotalTP/RemainingTP（保留已花点数）
//	:297-300  不变量门：Remaining<=Total、Total<=MaxUint16 等，越界 → 拒绝，零写入
//	:301-303  写回 Stats["bonus_sp"]/["bonus_tp"] 与技能账四点
//	:304-309  同一事务里 SaveCharacterFields(Stats) + SaveSkillFields(Points)
//	:311-314  提交后重读并返回
//
// 本仓与上游的两处**有意差异**（都是收紧，且都在"拒绝 = 零写入"的前提下）：
//
//	D1 请求体必须**同时**给出 bonus_sp 与 bonus_tp。上游 DTO 是两个 int64 字段，
//	   缺省字段被 Go 解成 0，于是"只想改 SP"会把 TP 悄悄清零。这里用指针区分
//	   "没给"与"给了 0"，缺任何一个都明确拒绝。显式传 0 仍然表示清零（与上游同义）。
//	D2 账本重算复用本仓既有的 `recomputeSkillPoints`（api_characters.go:470-511），
//	   它是"PVF 累计值 + bonus"的权威口径，并顺带把 SyncedLevel 对齐当前等级；
//	   上游是"在旧 TotalSP 上加减差额"。两者在账本与 PVF 同步时数值完全相同，
//	   账本陈旧时本仓会把总量纠正回 PVF 口径（差额平移不会）。上限门
//	   （Total<=MaxUint16）与上游 :297-298 一致，由本文件显式补上。
//
// 幂等：两个 bonus 都没变、且重算后的技能账与库里的逐字段相同时，**零写入**返回
// （上游无条件写库并刷新 UpdatedAt）。响应仍是同一份 SP/TP 载荷。
const (
	// spAbsoluteMaxBonus 与上游 :263-265 同界：bonus_sp/bonus_tp 是 u16。
	spAbsoluteMaxBonus = math.MaxUint16
)

var (
	// errSkillLedgerMissing：技能账不存在。上游 :286 的
	// "skill ledger is missing"，这里是**拒绝**而不是合成空账本。
	errSkillLedgerMissing = errors.New("技能点账本不存在（该角色还没有技能数据），无法设置绝对值")
	// errSPAbsoluteLedgerOverflow：平移后总量超出客户端 u16 线格式。
	errSPAbsoluteLedgerOverflow = errors.New("SP/TP 总量超出客户端上限（65535），已拒绝")
)

// spAbsoluteLedgerRejection 包住 recomputeSkillPoints 的业务/表错误：
// 它同样是"写库之前就失败"（零写入），但原因需要原样带进审计事件与响应，
// 不要被归到"写库失败"那一类。
type spAbsoluteLedgerRejection struct{ err error }

func (e *spAbsoluteLedgerRejection) Error() string { return e.err.Error() }
func (e *spAbsoluteLedgerRejection) Unwrap() error { return e.err }

// spAbsoluteRequest 是绝对值请求体。
//
// 同时接受上游风格的下划线（bonus_sp/bonus_tp）与本仓前端的 camelCase
// （bonusSp/bonusTp）；指针用于区分"字段缺省"与"显式 0"。
type spAbsoluteRequest struct {
	BonusSP    *int64 `json:"bonus_sp"`
	BonusTP    *int64 `json:"bonus_tp"`
	BonusSPAlt *int64 `json:"bonusSp"`
	BonusTPAlt *int64 `json:"bonusTp"`
}

// spAbsoluteBoundsRejection：请求体层面的拒绝（越界/缺字段），用于具名审计事件。
type spAbsoluteBoundsRejection struct {
	reason  string
	message string
}

func (e *spAbsoluteBoundsRejection) Error() string { return e.message }

func (s *Server) handleSetSpAbsolute(w http.ResponseWriter, r *http.Request) {
	// 顺序与上游 gm_extended.go:262-273 一致：解析角色 → 校验请求体 → 在线守卫 → 事务。
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[spAbsoluteRequest](w, r)
	if !ok {
		return
	}

	bonusSP, bonusTP, err := spAbsoluteValues(body)
	if err != nil {
		var rejection *spAbsoluteBoundsRejection
		if errors.As(err, &rejection) {
			s.logEvent("gm-sp-set-rejected",
				"panel_id", panelID,
				"character_id", character.CharacterID,
				"account_id", character.AccountID,
				"reason", rejection.reason,
				"old_bonus_sp", statInt(character, "bonus_sp"),
				"old_bonus_tp", statInt(character, "bonus_tp"),
				"result", "rejected",
			)
		}
		writeFailure(w, err.Error())
		return
	}

	// J0 在线守卫：默认档（DNFBRIDGE_GM_REQUIRE_OFFLINE 未设/空/非 on）直接放行。
	if !s.requireOffline(w, "sp-set", panelID) {
		return
	}

	if group.CharacterProgression == nil {
		s.logEvent("gm-sp-set-rejected", "panel_id", panelID, "character_id", character.CharacterID,
			"account_id", character.AccountID, "reason", "progression_transaction_unavailable",
			"result", "rejected")
		writeFailure(w, "设置 SP/TP 失败: 角色成长事务能力未就绪")
		return
	}

	var (
		updatedCharacter dnfrepo.CharacterRecord
		updatedSkill     dnfrepo.SkillRecord
		previous         dnfrepo.SkillPointState
		next             dnfrepo.SkillPointState
		oldBonusSP       int64
		oldBonusTP       int64
		applied          bool
	)
	// 单事务：与上游 :273 同一个仓储边界（角色数值 + 技能账一个提交点）。
	err = group.CharacterProgression.WithinCharacterProgression(ctx, character.CharacterID, func(
		characters dnfrepo.CharacterRepository,
		skills dnfrepo.SkillRepository,
	) error {
		record, found, loadErr := characters.Load(ctx, character.CharacterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errCharacterMissing
		}
		// 防串号：事务里的记录必须仍属于请求解析出来的那个账号。
		if owner := record.AccountID; owner != "" && owner != character.AccountID {
			return errCharacterMissing
		}
		skill, found, loadErr := skills.Load(ctx, character.CharacterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errSkillLedgerMissing
		}

		record = dnfrepo.CloneCharacter(record)
		skill = dnfrepo.CloneSkill(skill)
		if record.Stats == nil {
			record.Stats = make(map[string]int64)
		}
		previous = skill.Points
		oldBonusSP, oldBonusTP = statInt(record, "bonus_sp"), statInt(record, "bonus_tp")

		// 绝对值写进 Stats，再按本仓权威口径重算总量（保留已花点数）。
		record.Stats["bonus_sp"] = bonusSP
		record.Stats["bonus_tp"] = bonusTP
		points, recomputeErr := s.recomputeSkillPoints(record, previous)
		if recomputeErr != nil {
			return &spAbsoluteLedgerRejection{err: recomputeErr}
		}
		if points.TotalSP > spAbsoluteMaxBonus || points.TotalTP > spAbsoluteMaxBonus {
			return fmt.Errorf("%w: total_sp=%d total_tp=%d", errSPAbsoluteLedgerOverflow, points.TotalSP, points.TotalTP)
		}
		if points.RemainingSP > points.TotalSP || points.RemainingTP > points.TotalTP {
			return fmt.Errorf("%w: remaining_sp=%d total_sp=%d remaining_tp=%d total_tp=%d",
				errSPAbsoluteLedgerOverflow, points.RemainingSP, points.TotalSP, points.RemainingTP, points.TotalTP)
		}
		skill.Points = points
		updatedCharacter, updatedSkill, next = record, skill, points

		// 幂等：两个 bonus 与整份账本都没变 -> 零写入（不刷新 UpdatedAt）。
		if oldBonusSP == bonusSP && oldBonusTP == bonusTP && points == previous {
			return nil
		}

		now := time.Now().UTC()
		record.UpdatedAt, skill.UpdatedAt = now, now
		if saveErr := dnfrepo.SaveCharacterFields(ctx, characters, record, dnfrepo.CharacterFieldStats); saveErr != nil {
			return saveErr
		}
		if saveErr := dnfrepo.SaveSkillFields(ctx, skills, skill, dnfrepo.SkillFieldPoints); saveErr != nil {
			return saveErr
		}
		applied = true
		return nil
	})
	if err != nil {
		s.reportSpAbsoluteFailure(w, panelID, character, bonusSP, bonusTP, oldBonusSP, oldBonusTP, err)
		return
	}

	// 事务提交后重读，响应用库里的权威值（上游 :311-314 也是重读）。
	if reloaded, loadErr := s.loadCharacter(ctx, group, panelID); loadErr == nil {
		updatedCharacter = reloaded
	}
	if reloadedSkill, found, loadErr := group.Skill.Load(ctx, character.CharacterID); loadErr == nil && found {
		updatedSkill = reloadedSkill
	}

	result := "noop"
	if applied {
		result = "applied"
	}
	s.logEvent("gm-sp-set",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"old_bonus_sp", oldBonusSP,
		"bonus_sp", bonusSP,
		"old_bonus_tp", oldBonusTP,
		"bonus_tp", bonusTP,
		"total_sp", next.TotalSP,
		"remaining_sp", next.RemainingSP,
		"total_tp", next.TotalTP,
		"remaining_tp", next.RemainingTP,
		"result", result,
	)
	writeOK(w, s.spTpPayload(updatedCharacter, updatedSkill))
}

// spAbsoluteValues 解析并校验请求体；任何不合法都在**触碰仓储之前**返回。
func spAbsoluteValues(body spAbsoluteRequest) (int64, int64, error) {
	bonusSP, spGiven := firstInt64(body.BonusSP, body.BonusSPAlt)
	bonusTP, tpGiven := firstInt64(body.BonusTP, body.BonusTPAlt)
	switch {
	case !spGiven && !tpGiven:
		return 0, 0, &spAbsoluteBoundsRejection{
			reason:  "missing_both",
			message: "缺少 bonus_sp 与 bonus_tp（绝对值模式必须同时给出两个值，未做任何写入）。",
		}
	case !spGiven:
		return 0, 0, &spAbsoluteBoundsRejection{
			reason:  "missing_bonus_sp",
			message: "缺少 bonus_sp（绝对值模式必须同时给出 bonus_sp 与 bonus_tp，未做任何写入）。",
		}
	case !tpGiven:
		return 0, 0, &spAbsoluteBoundsRejection{
			reason:  "missing_bonus_tp",
			message: "缺少 bonus_tp（绝对值模式必须同时给出 bonus_sp 与 bonus_tp，未做任何写入）。",
		}
	}
	if bonusSP < 0 || bonusSP > spAbsoluteMaxBonus {
		return 0, 0, &spAbsoluteBoundsRejection{
			reason:  "bonus_sp_out_of_range",
			message: fmt.Sprintf("bonus_sp 必须在 0..%d 之间，当前 %d（未做任何写入）。", spAbsoluteMaxBonus, bonusSP),
		}
	}
	if bonusTP < 0 || bonusTP > spAbsoluteMaxBonus {
		return 0, 0, &spAbsoluteBoundsRejection{
			reason:  "bonus_tp_out_of_range",
			message: fmt.Sprintf("bonus_tp 必须在 0..%d 之间，当前 %d（未做任何写入）。", spAbsoluteMaxBonus, bonusTP),
		}
	}
	return bonusSP, bonusTP, nil
}

// firstInt64 返回第一个非 nil 的取值（下划线优先，其次 camelCase）。
func firstInt64(values ...*int64) (int64, bool) {
	for _, value := range values {
		if value != nil {
			return *value, true
		}
	}
	return 0, false
}

// reportSpAbsoluteFailure 把事务里的失败翻译成响应 + 具名审计事件。
// 所有拒绝路径都发生在写库之前（不变量门/账本缺失/角色缺失），因此都是零写入。
func (s *Server) reportSpAbsoluteFailure(
	w http.ResponseWriter,
	panelID int,
	character dnfrepo.CharacterRecord,
	bonusSP, bonusTP, oldBonusSP, oldBonusTP int64,
	err error,
) {
	fields := []any{
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"old_bonus_sp", oldBonusSP,
		"bonus_sp", bonusSP,
		"old_bonus_tp", oldBonusTP,
		"bonus_tp", bonusTP,
		"result", "rejected",
	}
	switch {
	case errors.Is(err, errSkillLedgerMissing):
		s.logEvent("gm-sp-set-rejected", append(fields, "reason", "skill_ledger_missing")...)
		writeFailure(w, err.Error()+"（未做任何写入）。")
	case errors.Is(err, errSPAbsoluteLedgerOverflow):
		s.logEvent("gm-sp-set-rejected", append(fields, "reason", "ledger_overflow")...)
		writeFailure(w, err.Error()+"（未做任何写入）。")
	case errors.Is(err, errCharacterMissing):
		s.logEvent("gm-sp-set-rejected", append(fields, "reason", "character_missing")...)
		writeFailure(w, fmt.Sprintf("角色不存在: %d（未做任何写入）。", panelID))
	default:
		// recomputeSkillPoints 的"减少 SP 会使剩余点数为负数"、PVF 表读取失败等
		// 都发生在克隆/保存之前，同样是零写入；真因写进事件的 error 字段。
		var recompute *spAbsoluteLedgerRejection
		if errors.As(err, &recompute) {
			s.logEvent("gm-sp-set-rejected", append(fields,
				"reason", "ledger_recompute", "error", recompute.err.Error())...)
			writeFailure(w, "设置 SP/TP 被拒绝: "+recompute.err.Error()+"（未做任何写入）。")
			return
		}
		s.logEvent("gm-sp-set-failed", append(fields, "reason", "write_failed", "error", err.Error())...)
		writeFailure(w, "设置 SP/TP 失败: "+err.Error())
	}
}
