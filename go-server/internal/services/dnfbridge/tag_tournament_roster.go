package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 固定57d8fbfc客户端：1456990发送四个u8，145fb50成功应答同序读取；
// 1460f90通知同样读取两组{server, compact-selector}。不能使用旧枚举1459。
const currentTagTournamentSetRosterOpcode uint16 = 1457
const currentTagTournamentRosterNotification uint16 = 1388
const currentTagTournamentCountsNotification uint16 = 1403
const currentTagTournamentRosterListOpcode uint16 = 1467

type currentTagTournamentRosterPair struct{ server, selector byte }

func decodeCurrentTagTournamentRoster(body []byte) ([2]currentTagTournamentRosterPair, error) {
	if len(body) != 4 {
		return [2]currentTagTournamentRosterPair{}, fmt.Errorf("帝国竞技场队员请求必须是两组服务器与角色序号")
	}
	return [2]currentTagTournamentRosterPair{{body[0], body[1]}, {body[2], body[3]}}, nil
}

// currentRenameRoster虽由重名弹窗保存，但其内容是共用op2成功写出的紧凑角色表；
// 帝国客户端36C4E80也按该表的位置取序号。只读复用，不重新按SQL Slot排序。
func (s *Service) currentTagTournamentSentRoster(p *gameSession) ([]repo.CharacterRecord, byte, error) {
	if !s.isCurrentTagTournamentChannel(p) || !p.loginPacketSeen || p.selectedCharacterID == 0 || p.accountID == "" || p.accountID != strings.TrimSpace(p.accountID) {
		return nil, 0, fmt.Errorf("帝国竞技场缺少已登录账号与角色")
	}
	server := s.channelServerID()
	if server <= 0 || server > 255 || p.channel.ServerID > 0 && p.channel.ServerID != server {
		return nil, 0, fmt.Errorf("帝国竞技场服务器编号不属于本机配套")
	}
	if err := s.validateCurrentSceneActorRoute(); err != nil {
		return nil, 0, err
	}
	sent := p.currentRenameRoster
	if len(sent) == 0 {
		sent = s.cachedCurrentTagTournamentSentRoster(p, byte(server))
	}
	if len(sent) == 0 || len(sent) > 255 {
		return nil, 0, fmt.Errorf("帝国竞技场缺少客户端已收到的角色表")
	}
	rows := make([]repo.CharacterRecord, len(sent))
	seen := map[string]bool{}
	for i, ch := range sent {
		if ch.AccountID != p.accountID || ch.CharacterID == "" || ch.Name == "" || ch.Slot < 0 || seen[ch.CharacterID] {
			return nil, 0, fmt.Errorf("帝国竞技场角色表归属或序号无效")
		}
		seen[ch.CharacterID] = true
		rows[i] = repo.CloneCharacter(ch)
	}
	if !seen[strconv.Itoa(int(p.selectedCharacterID))] {
		return nil, 0, fmt.Errorf("帝国竞技场当前角色不在该客户端已收到的角色表中")
	}
	// V195设队窗口从actor+0x3ac取本区角色表编号，与mode0尾部保持一致。
	// 对外频道route及内部channelServerID均不是该字段；缓存仍按内部服务器隔离。
	return rows, currentSceneActorLocalRosterServer, nil
}

func (s *Service) currentTagTournamentOwner() (*tagtournament.Owner, error) {
	c, err := s.currentTagTournamentCatalog()
	if err != nil {
		return nil, err
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return nil, fmt.Errorf("帝国竞技场仓储不可用")
	}
	return tagtournament.NewOwner(c, repositories)
}

func (s *Service) handleCurrentTagTournamentSetRoster(p *gameSession, body []byte) error {
	stage := "decode"
	var pairs [2]currentTagTournamentRosterPair
	var server byte
	var rows []repo.CharacterRecord
	fail := func(err error) error {
		s.logGameEvent(p, "帝国竞技场队员设置已拒绝，未扣费", "error", err,
			"stage", stage, "body_len", len(body), "server", server, "count", len(rows),
			"first_server", pairs[0].server, "first_selector", pairs[0].selector,
			"second_server", pairs[1].server, "second_selector", pairs[1].selector,
			"rejection", currentEmpireRosterRejection(err))
		return s.sendGameUpperFailure(p, currentTagTournamentSetRosterOpcode, 3)
	}
	pairs, err := decodeCurrentTagTournamentRoster(body)
	if err != nil {
		return fail(err)
	}
	stage = "sent_roster"
	rows, server, err = s.currentTagTournamentSentRoster(p)
	if err != nil {
		return fail(err)
	}
	stage = "session"
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		return fail(fmt.Errorf("帝国竞技场角色会话已失效"))
	}
	var expected [2]repo.CharacterRecord
	stage = "selector"
	for i, pair := range pairs {
		if pair.server != server || int(pair.selector) >= len(rows) {
			return fail(fmt.Errorf("帝国竞技场队员服务器或角色序号不在已下发表中"))
		}
		expected[i] = rows[pair.selector]
	}
	current := strconv.Itoa(int(identity.character))
	stage = "current_member"
	if expected[0].CharacterID == expected[1].CharacterID || expected[0].CharacterID != current && expected[1].CharacterID != current {
		return fail(fmt.Errorf("帝国竞技场需设置包含当前角色的两名不同队员"))
	}
	// 1388恢复队伍可能先于1467引发原生1457回写；同区合法队员请求也能
	// 确认已经写出24的离场。非法序号不能退休地图，确认后再执行原事务。
	if err = s.confirmCurrentTagTournamentRosterTownReturn(p); err != nil {
		return err
	}
	stage = "lobby_ready"
	if !currentTagTournamentLobbyInteractionReady(p) {
		return fail(fmt.Errorf("帝国竞技场只能在角色就绪的休息室设置队员"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	stage = "asset_reservation"
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, p.accountID)
	if err != nil {
		return fail(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	stage = "owner"
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		return fail(err)
	}
	repositories, _ := s.repositoryGroup()
	stage = "current_record"
	ch, found, err := repositories.Character.Load(ctx, current)
	if err != nil {
		return fail(err)
	}
	// 实际城镇落点由commitCurrentReconnectLocation写入Stats；Location是独立镜像，
	// 可能仍停留在上一城镇或副本，不能据此误拒绝已就绪的休息室角色。
	stage = "current_town"
	if !found || ch.AccountID != p.accountID || ch.Stats["town_id"] != 48 {
		return fail(fmt.Errorf("帝国竞技场当前角色不在休息室"))
	}
	stage = "register_transaction"
	if err = owner.RegisterRosterFromSnapshot(ctx, p.accountID, current, expected); err != nil {
		return fail(err)
	}
	s.logGameEvent(p, "帝国竞技场队员设置已保存", "server", server, "first_selector", pairs[0].selector, "second_selector", pairs[1].selector)
	s.releaseCurrentCharacterAssets(reservation)
	// ACK自身已更新客户端分组；不能再发1388，否则其自动1457回写会循环。
	if err = s.sendGameUpperRaw(p, currentTagTournamentSetRosterOpcode, upperSuccessBody(body)); err != nil {
		return err
	}
	clearCurrentTagTournamentReturnRoster(p, identity)
	return nil
}

// 本区选角资料已由普通op2安装。1403枚举额外区服，不能再次把本区放入type12。
// 损坏/过期角色名单只阻止该名单的投影，不把它覆盖为空队或阻断正常登录。
func (s *Service) sendCurrentTagTournamentLobby(p *gameSession) error {
	if !s.isCurrentTagTournamentChannel(p) {
		return nil
	}
	rows, server, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场休息室角色表尚未就绪", "error", err)
		return nil
	}
	_, err = s.currentTagTournamentLobbyCharacters(p, rows)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场选择框角色明细未就绪", "error", err)
		return nil
	}
	// 1403会直接打开DA窗口，不能把它当成无副作用的入频道初始化。
	// 进入休息室只静默恢复已保存队伍；本区名单继续复用普通选角资料。
	if err = s.sendCurrentTagTournamentDailyLobby(p, time.Now()); err != nil {
		return err
	}
	if err = s.sendCurrentTagTournamentSavedRoster(p, rows, server); err != nil {
		return err
	}
	return s.sendCurrentTagTournamentWeeklyInfo(p)
}

// 现用36C35F0在DA窗口需要资料时发送1467空正文，且以+105抑制等待中的重发。
// 只有收到该请求后才回1403；已保存队伍先下发，避免窗口先画出空队伍。
func (s *Service) handleCurrentTagTournamentRosterList(p *gameSession, body []byte) error {
	s.logGameEvent(p, "帝国竞技场队伍框资料请求已收到", "body_len", len(body))
	if len(body) != 0 {
		s.logGameEvent(p, "帝国竞技场队伍框资料请求已忽略", "body_len", len(body), "reason", "角色未就绪或请求正文无效")
		return nil
	}
	if _, ok := s.boundGameSessionCharacterSnapshot(p); !ok {
		s.logGameEvent(p, "帝国竞技场队伍框资料请求已忽略", "reason", "角色会话已失效")
		return nil
	}
	if err := s.confirmCurrentTagTournamentRosterTownReturn(p); err != nil {
		return err
	}
	if !currentTagTournamentLobbyInteractionReady(p) {
		s.logGameEvent(p, "帝国竞技场队伍框资料请求已忽略", "reason", "休息室角色状态尚未完成")
		return nil
	}
	rows, server, err := s.currentTagTournamentSentRoster(p)
	if err == nil {
		_, err = s.currentTagTournamentLobbyCharacters(p, rows)
	}
	if err != nil {
		s.logGameEvent(p, "帝国竞技场队伍框资料请求已忽略", "error", err)
		return nil
	}
	if err = s.sendCurrentTagTournamentSavedRoster(p, rows, server); err != nil {
		return err
	}
	// 本机没有额外区服账号资料；零区服使本区名单就绪，不复制本区角色到type12。
	if err = s.sendGameUpperRawClass(p, currentTagTournamentCountsNotification, []byte{0}, 0); err != nil {
		return err
	}
	s.logGameEvent(p, "帝国竞技场应组队设置请求打开队伍框", "server", server, "count", len(rows), "other_servers", 0)
	return nil
}

func (s *Service) sendCurrentTagTournamentSavedRoster(p *gameSession, rows []repo.CharacterRecord, server byte) error {
	if b := p.tagTournamentReturnRoster; b != nil {
		if !s.boundGameSessionCharacterCurrent(b.identity) || b.identity.character != p.selectedCharacterID {
			p.tagTournamentReturnRoster = nil
		} else if !currentTagTournamentLobbyInteractionReady(p) || p.tagTournamentBattle != nil {
			return fmt.Errorf("帝国竞技场回城尚未得到真实城镇确认，不能重建队伍对象")
		}
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		s.logGameEvent(p, "帝国竞技场休息室队员资料未投影", "error", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	view, err := owner.Lobby(ctx, p.accountID)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场已注册队员需重新核对", "error", err)
		return nil
	}
	if !view.Registered {
		return nil
	}
	body, err := buildCurrentTagTournamentSavedRosterBody(view.Characters, rows, server)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场已注册队员不在当前客户端角色表中", "error", err)
		return nil
	}
	err = s.sendGameUpperRawClass(p, currentTagTournamentRosterNotification, body, 0)
	if err == nil {
		if identity, ok := s.boundGameSessionCharacterSnapshot(p); ok {
			clearCurrentTagTournamentReturnRoster(p, identity)
		}
		s.logGameEvent(p, "帝国竞技场已恢复保存队伍", "server", server, "first_selector", body[1], "second_selector", body[3])
	}
	return err
}

func clearCurrentTagTournamentReturnRoster(p *gameSession, identity boundGameSessionCharacter) {
	if b := p.tagTournamentReturnRoster; b != nil && b.identity == identity {
		b.returnRosterSent = true
		p.tagTournamentReturnRoster = nil
	}
}

func buildCurrentTagTournamentSavedRosterBody(characters [2]repo.CharacterRecord, rows []repo.CharacterRecord, server byte) ([]byte, error) {
	var body [4]byte
	for i, ch := range characters {
		selector := -1
		for n, sent := range rows {
			if sent.CharacterID == ch.CharacterID && sent.Slot == ch.Slot && sent.Name == ch.Name {
				selector = n
				break
			}
		}
		if selector < 0 {
			return nil, fmt.Errorf("帝国竞技场队员%s不在客户端已收名单中", ch.CharacterID)
		}
		body[2*i], body[2*i+1] = server, byte(selector)
	}
	return body[:], nil
}
