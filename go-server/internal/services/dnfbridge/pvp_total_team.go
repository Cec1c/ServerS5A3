package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// errPVPTotalTeamNameTaken 表示队伍名已被**别的账号**占用。
//
// 单独成哨兵错误是为了让调用处能把「重名」和「查询失败」分成两个响应码：
// 客户端对「重名」要提示"名称已存在"，对「查询失败」要提示"稍后重试"，
// 两者用同一个码时玩家看到的永远是后一种。
var errPVPTotalTeamNameTaken = errors.New("pvp total team name already registered")

// currentPVPTotalTeamNameDuplicateCode 是「名称重复」的响应码。
//
// 口径对齐同一台机器上的另外两处查重：角色名 checkNameCodeDuplicated = 0x14（20），
// 冒险团名 representAccountNameDuplicateCode = 20。
//
// 之前这里回的是 3，而 3 在冒险团那条链上是**重试码**（representAccountNameRetryCode），
// 客户端会把重名显示成"系统繁忙"，甚至不刷新界面。
const currentPVPTotalTeamNameDuplicateCode byte = 20

func parsePVPTotalTeamName(body []byte, tail int) (string, []byte, error) {
	if len(body) < 4 {
		return "", nil, fmt.Errorf("missing team name")
	}
	n := uint64(binary.LittleEndian.Uint32(body))
	if n == 0 || n > 31 || n+4+uint64(tail) != uint64(len(body)) {
		return "", nil, fmt.Errorf("invalid team name size")
	}
	raw := body[4 : 4+int(n)]
	name, err := decodeCharacterName(raw)
	if err != nil || bytes.IndexByte(raw, 0) >= 0 || !bytes.Equal(rosterNameBytes(name), raw) {
		return "", nil, fmt.Errorf("invalid team name encoding")
	}
	if err := dnfpvp.ValidateTotalTeamName(name); err != nil {
		return "", nil, err
	}
	return name, body[4+int(n):], nil
}

func (s *Service) checkPVPTotalTeamName(ctx context.Context, repos dnfrepo.Group, accountID, name string) error {
	finder, ok := repos.Account.(dnfrepo.PVPTotalTeamNameFinder)
	if !ok {
		return fmt.Errorf("team name index unavailable")
	}
	owner, found, err := finder.FindAccountIDByPVPTotalTeamName(ctx, name)
	if err != nil {
		return err
	}
	if found && owner != accountID {
		return errPVPTotalTeamNameTaken
	}
	return nil
}

func (s *Service) handlePVPTotalTeam(session *gameSession, opcode uint16, body []byte) error {
	if session == nil || session.accountID == "" {
		return nil
	}
	tail := 0
	if opcode == 871 {
		tail = 3
	}
	if opcode == 873 {
		tail = 2
	}
	name, extra, err := parsePVPTotalTeamName(body, tail)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	if session.pvpRoom != nil {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	for _, queued := range s.pvpMatching {
		if queued.member.session == session {
			return s.sendGameUpperFailure(session, opcode, 1)
		}
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.sendGameUpperFailure(session, opcode, 159)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if err := s.checkPVPTotalTeamName(ctx, repos, session.accountID, name); err != nil {
		duplicated := errors.Is(err, errPVPTotalTeamNameTaken)
		s.logGameEvent(session, "game-pvp-total-team-name-check", "opcode", opcode,
			"available", false, "duplicated", duplicated, "error", err)
		if duplicated {
			return s.sendGameUpperFailure(session, opcode, currentPVPTotalTeamNameDuplicateCode)
		}
		return s.sendGameUpperFailure(session, opcode, 3)
	}
	if opcode == 872 {
		// ★ 成功时回显队伍名（2026-11-06）。
		//
		// 同一台机器上「冒险团名称查重」的成功响应就是 `[1] + rawDstr(名字)`
		//（account_represent_name.go:152-157）；PVP 这条之前只回 `[1]`，
		// 客户端按 `[1]+dstr` 解析时读到越界就静默失败 —— 现场表现正是
		// 「输入队伍名后点检查重复，界面毫无反应」，而服务端日志里 11 次请求
		// 全是成功（body_len=1）。
		//
		// ⚠️ 本改动**未经客户端实测**：若客户端实际只需要裸 `[1]`，
		// 多出来的 dstr 会被忽略；若它需要名字，这一版才修得好。
		var w packetWriter
		w.writeRawDstr(rosterNameBytes(name))
		s.logGameEvent(session, "game-pvp-total-team-name-check", "opcode", opcode, "available", true)
		return s.sendGameUpperSuccess(session, opcode, w.bytes())
	}
	if opcode == 873 {
		if session.selectedCharacterID == 0 {
			return s.sendGameUpperFailure(session, opcode, 1)
		}
		slot := binary.LittleEndian.Uint16(extra)
		if err := dnfpvp.RenameTotalTeam(ctx, repos, session.accountID, strconv.Itoa(int(session.selectedCharacterID)), name, slot, s.gameplayNow()); err != nil {
			s.logGameEvent(session, "game-pvp-total-rename-rejected", "error", err)
			return s.sendGameUpperFailure(session, opcode, 1)
		}
		var w packetWriter
		w.writeRawDstr(rosterNameBytes(name))
		if err := s.sendGameUpperSuccess(session, opcode, w.bytes()); err != nil {
			return err
		}
		return s.sendSelectedIncrementalItemSlotRefreshes(session, "pvp_total_rename", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: int16(slot)}})
	}
	if opcode != 871 {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	if session.selectedCharacterID == 0 {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	prior, err := dnfpvp.ReadTotalTeam(ctx, repos, session.accountID)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 159)
	}
	// Changing the roster never bypasses the separate native rename-card path.
	if prior.Team.Name != "" && prior.Team.Name != name {
		return s.sendGameUpperFailure(session, opcode, 4)
	}
	rows, err := repos.Character.ListByAccount(ctx, session.accountID, dnfrepo.DefaultCharacterSlotLimit)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 159)
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 159)
	}
	team, err := dnfpvp.ResolveTotalTeam(session.accountID, name, [3]byte(extra), rows, archive)
	if err != nil {
		s.logGameEvent(session, "game-pvp-total-team-rejected", "error", err)
		return s.sendGameUpperFailure(session, opcode, 7)
	}
	if err := dnfpvp.SaveTotalTeam(ctx, repos.Account, session.accountID, team.Team, s.gameplayNow()); err != nil {
		return err
	}
	s.logGameEvent(session, "game-pvp-total-team-saved", "characters", team.Team.Characters, "slots", team.Slots)
	// Native op871 (1CFD700) only closes the editor and requests op7 when
	// switching representatives. It never stores the saved team. Publish op801
	// while the current scene actor still exists, before that ACK can leave it.
	if err := s.sendGameUpperRawClass(session, 801, buildPVPTotalTeamSelection(team), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-pvp-total-team-state-sent", "source", "save_before_ack", "char_id", session.selectedCharacterID, "slots", team.Slots)
	return s.sendGameUpperSuccess(session, opcode, team.Slots[:])
}

func buildPVPTotalTeamSelection(team dnfpvp.TotalTeamSnapshot) []byte {
	var w packetWriter
	w.writeRawDstr(rosterNameBytes(team.Team.Name))
	w.writeBytes(team.Slots[:])
	return w.bytes()
}

func (s *Service) sendCurrentPVPTotalTeamSelection(session *gameSession) error {
	// Native op801 (1D589B0) dereferences the scene actor's +4CC state before
	// reading its body. Selector roster objects do not supply that actor.
	if session == nil || session.selectedCharacterID == 0 || !isCurrentPVPTotalChannel(session.channel) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("total-match repository unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	team, err := dnfpvp.ReadTotalTeam(ctx, repos, session.accountID)
	if err != nil {
		return err
	}
	if team.Team.Name == "" || team.Slots[0] == 255 || team.Slots[1] == 255 || team.Slots[2] == 255 {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, 801, buildPVPTotalTeamSelection(team), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-pvp-total-team-state-sent", "source", "scene_actor_ready", "char_id", session.selectedCharacterID, "slots", team.Slots)
	loaded, err := s.loadCurrentPVPTotalTeam(ctx, session)
	if err != nil {
		return err
	}
	return s.sendCurrentPVPTotalActors(session, []*currentPVPMember{{session: session, characterID: session.selectedCharacterID, total: loaded}})
}
