package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现用原生回归状态为352字节；1370只保留为活动管理的逻辑编号。
const currentReturneeStateID uint16 = 1369
const currentReturneeTimeID uint16 = 304 // u32秒数、u8旧奖励标志；原生时间槽49。
const currentReturneeGuideID uint16 = 1429
const currentReturneeClaimID uint16 = 1430
const currentLuckyCharacterStateID uint16 = 1054 // class0：u32记录数，每条u32职业和u32转职/觉醒。
// Retained only when creating characters for compatibility with an older server rollback.
// Current eligibility does not read or backfill this historical marker.
const currentLuckyNewCharacterStat = "private_lucky_new_character_007"

type currentLuckyCatalog struct {
	Jobs                         [][2]uint32
	Monster, Clear, Party, Quest int64
	UniqueDrop, FatigueBurn      int64
	NoFatigueBurnDungeons        map[int64]bool
	Level1To89Only               bool
}

type currentReturneeProjection struct {
	Character       uint16
	Generation      uint64
	Body            [352]byte
	ActorGeneration uint64
	ExpiresAt       int64
}

func (s *Service) currentReturneeLuckyCatalogs(ctx context.Context) (*dnfonlineevent.ReturneeCatalog, *currentLuckyCatalog, error) {
	s.returneeLuckyMu.Lock()
	defer s.returneeLuckyMu.Unlock()
	if s.returneeCatalog != nil && s.luckyCharacterCatalog != nil {
		return s.returneeCatalog, s.luckyCharacterCatalog, nil
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return s.returneeCatalog, s.luckyCharacterCatalog, nil
	}
	if s.returneeCatalog == nil && s.currentPortedActivityConfigured(1370) {
		c, err := dnfonlineevent.LoadReturneeCatalog(ctx, archive)
		if err != nil {
			return nil, nil, err
		}
		items, err := s.currentPVFItemCatalog()
		if err != nil {
			return nil, nil, err
		}
		catalogs := []*dnfonlineevent.ReturneeCatalog{c}
		if c.Newbie != nil {
			catalogs = append(catalogs, c.Newbie)
		}
		for _, group := range catalogs {
			for _, m := range group.Missions {
				if _, err := items.ResolveItem(uint32(m.Reward.ItemID)); err != nil {
					return nil, nil, err
				}
			}
			for _, m := range group.Attendance {
				if m.ID != 0 {
					if _, err := items.ResolveItem(uint32(m.Reward.ItemID)); err != nil {
						return nil, nil, err
					}
				}
			}
		}
		s.returneeCatalog = c
	}
	if s.luckyCharacterCatalog == nil && s.currentPortedActivityConfigured(1054) {
		text, err := archive.ReadText("etc/chn_activatecharacter.etc")
		if err != nil {
			return nil, nil, err
		}
		c, err := parseCurrentLuckyCharacterCatalog(text)
		if err != nil {
			return nil, nil, err
		}
		policy := s.currentPortedPolicy()
		if policy.LuckyAllProfessions {
			c.Jobs = currentAllLuckyProfessions()
		}
		if c.FatigueBurn > 0 {
			blocked, err := archive.ReadText("etc/chn_fatigueaccelchkdungeon.etc")
			if err != nil {
				return nil, nil, err
			}
			c.NoFatigueBurnDungeons, err = parseCurrentLuckyFatigueBlacklist(blocked)
			if err != nil {
				return nil, nil, err
			}
		}
		s.luckyCharacterCatalog = c
	}
	return s.returneeCatalog, s.luckyCharacterCatalog, nil
}

func parseCurrentLuckyCharacterCatalog(text string) (*currentLuckyCatalog, error) {
	doc, err := dnfpvf.Parse("etc/chn_activatecharacter.etc", text)
	if err != nil {
		return nil, err
	}
	c := &currentLuckyCatalog{}
	tokens, ok := doc.Section("activate character schedule")
	if !ok || len(tokens) == 0 || len(tokens)%4 != 0 {
		return nil, fmt.Errorf("invalid lucky character schedule")
	}
	// The user explicitly enabled this test edition for all configured professions.
	// Zero PVF dates are not interpreted as an existing live schedule.
	for i := 0; i < len(tokens); i += 4 {
		job, grow := tokens[i+2], tokens[i+3]
		if job.Kind != dnfpvf.TokenInt || grow.Kind != dnfpvf.TokenInt || job.Int < 0 || job.Int > int64(jobmap.MaxID) || grow.Int < 0 || grow.Int > 127 {
			return nil, fmt.Errorf("invalid lucky profession")
		}
		c.Jobs = append(c.Jobs, [2]uint32{uint32(job.Int), uint32(grow.Int)})
	}
	for name, dst := range map[string]*int64{"monster kill exp buff ratio": &c.Monster, "dungeon clear exp buff ratio": &c.Clear, "party play buff ratio": &c.Party, "quest clear buff ratio": &c.Quest} {
		v, ok := doc.Section(name)
		if !ok || len(v) != 1 || v[0].Kind != dnfpvf.TokenInt || v[0].Int < 0 || v[0].Int > 1000 {
			return nil, fmt.Errorf("invalid lucky ratio %s", name)
		}
		*dst = v[0].Int
	}
	for name, dst := range map[string]*int64{"unique drop ratio": &c.UniqueDrop, "fatigue accel percent": &c.FatigueBurn} {
		v, ok := doc.Section(name)
		if !ok { // 旧资源未声明时保持无额外效果。
			continue
		}
		if len(v) != 1 || v[0].Kind != dnfpvf.TokenInt || v[0].Int < 0 || v[0].Int > 1000 {
			return nil, fmt.Errorf("幸运角色配置[%s]无效", name)
		}
		*dst = v[0].Int
	}
	if v, ok := doc.Section("private new characters only"); ok {
		if len(v) != 1 || v[0].Kind != dnfpvf.TokenInt || v[0].Int != 1 {
			return nil, fmt.Errorf("invalid private lucky new-character policy")
		}
		// Legacy private PVF tag: retain its validated level range while opening
		// that range to existing and new ordinary characters alike.
		c.Level1To89Only = true
	}
	return c, nil
}

func currentLuckyCharacterMatches(c *currentLuckyCatalog, ch dnfrepo.CharacterRecord) bool {
	if c == nil || ch.CharacterID == "" || ch.Stats["is_hardcore_mode"] != 0 {
		return false
	}
	// Eligibility follows the current level, independent of creation time,
	// reservation status, or historical new-character markers.
	if c.Level1To89Only && (dnfpvp.IsPrivateCharacter(ch) || ch.Level < 1 || ch.Level >= 90) {
		return false
	}
	job, err := strconv.Atoi(ch.Job)
	if err != nil {
		return false
	}
	grow := ch.Stats["grow_type"]
	for _, row := range c.Jobs {
		if int(row[0]) == job && int64(row[1]) == grow {
			return true
		}
	}
	return false
}

func (s *Service) currentLuckyCharacterEffect(session *gameSession, ch dnfrepo.CharacterRecord) *currentLuckyCatalog {
	if s == nil || session == nil || !s.currentPortedActivityEnabled(1054) || !s.currentPortedPolicy().LuckyCharacter.ActiveAt(s.gameplayNow()) || currentLuckyCharacterPVP(session) {
		return nil
	}
	s.returneeLuckyMu.Lock()
	c := s.luckyCharacterCatalog
	s.returneeLuckyMu.Unlock()
	if !currentLuckyCharacterMatches(c, ch) {
		return nil
	}
	return c
}

func currentLuckyCharacterPVP(session *gameSession) bool {
	if isPVPChannel(session.channel) {
		return true
	}
	switch session.channel.Type {
	case 4, 5, 7, 8, 9, 10, 13, 14, 20, 24, 31, 34, 35, 41:
		return true
	}
	return false
}

func (s *Service) currentReturneeWelcomeExperienceBonus(ctx context.Context, session *gameSession, character dnfrepo.CharacterRecord, base uint32) (uint32, error) {
	if s == nil || session == nil || base == 0 || currentLuckyCharacterPVP(session) {
		return 0, nil
	}
	accountID := s.accountIDForSession(session)
	if strings.TrimSpace(character.AccountID) != accountID || character.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) {
		return 0, fmt.Errorf("returnee experience character ownership mismatch")
	}
	if !s.currentPortedActivityEnabled(1370) || session.returneePreparedCharacter != session.selectedCharacterID || session.returneePreparedGeneration != session.characterGeneration || session.returneePreparedExpiresAt <= s.gameplayNow().Unix() {
		return 0, nil
	}
	return currentPercentageBonusUint32(base, s.currentPortedPolicy().ReturneeExperiencePercent), nil
}

func buildCurrentLuckyCharacterBody(c *currentLuckyCatalog) []byte {
	var w packetWriter
	if c == nil {
		w.writeUint32(0)
		return w.bytes()
	}
	w.writeUint32(uint32(len(c.Jobs)))
	for _, row := range c.Jobs {
		w.writeUint32(row[0])
		w.writeUint32(row[1])
	}
	return w.bytes()
}

func buildCurrentReturneeBodyAt(c *dnfonlineevent.ReturneeCatalog, state dnfonlineevent.ReturneeState, now time.Time) [352]byte {
	var b [352]byte
	// 原生0属于新手，1..5属于不同离线时长的回归用户，6为非参与者。
	binary.LittleEndian.PutUint16(b[:], 6)
	binary.LittleEndian.PutUint64(b[8:], state.Guide)
	copy(b[288:], state.GuideExtra[:])
	if c == nil || !state.ActiveAt(now) {
		return b
	}
	kind := state.UserType
	if state.Newbie {
		c = c.Newbie
		if c == nil {
			return b
		}
		kind = 0
	} else if kind < 1 || kind > 5 {
		kind = 5
	}
	binary.LittleEndian.PutUint16(b[:], kind)
	for i, m := range c.Missions {
		o := 16 + i*16
		binary.LittleEndian.PutUint32(b[o:], m.ID)
		binary.LittleEndian.PutUint32(b[o+4:], state.Progress[i])
		if state.Progress[i] >= m.Required {
			b[o+12] = 1
		}
		if state.Claimed&(1<<i) != 0 {
			b[o+13] = 1
		}
	}
	// 原生后七项沿用16字节任务行；末尾64字节是模式1的512位扩展引导。
	for i, m := range c.Attendance {
		o := 176 + i*16
		binary.LittleEndian.PutUint32(b[o:], m.ID)
		if m.ID != 0 && len(state.AttendanceDays) > i {
			binary.LittleEndian.PutUint32(b[o+4:], m.Required)
			b[o+12] = 1
		}
		if state.AttendanceClaimed&(1<<i) != 0 {
			b[o+13] = 1
		}
	}
	return b
}

func buildCurrentReturneeBody(c *dnfonlineevent.ReturneeCatalog, state dnfonlineevent.ReturneeState) [352]byte {
	return buildCurrentReturneeBodyAt(c, state, time.Now())
}

func (s *Service) settleCurrentReturnee(session *gameSession, login bool, claim uint32, guide *uint32) (dnfonlineevent.ReturneeState, []string, error) {
	return s.settleCurrentReturneeWithGuide(session, login, claim, guide, nil)
}

func (s *Service) settleCurrentReturneeWithGuide(session *gameSession, login bool, claim uint32, guide, extraGuide *uint32) (dnfonlineevent.ReturneeState, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	c, _, err := s.currentReturneeLuckyCatalogs(ctx)
	if err != nil {
		return dnfonlineevent.ReturneeState{}, nil, err
	}
	if c == nil {
		if claim != 0 || guide != nil || extraGuide != nil {
			return dnfonlineevent.ReturneeState{}, nil, dnfonlineevent.ErrOwnerUnavailable
		}
		return dnfonlineevent.ReturneeState{}, nil, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfonlineevent.ReturneeState{}, nil, dnfonlineevent.ErrOwnerUnavailable
	}
	characterID := session.selectedCharacterID
	if characterID == 0 {
		characterID = session.returneePreparedCharacter
	}
	charID := strconv.Itoa(int(characterID))
	o := dnfonlineevent.ReturneeObservation{}
	if repos.Settings != nil {
		registry, err := dnfguild.Load(ctx, repos.Settings)
		if err != nil {
			return dnfonlineevent.ReturneeState{}, nil, err
		}
		_, _, o.Guild = registry.GuildForCharacter(charID)
	}
	now := s.gameplayNow()
	return dnfonlineevent.SettleReturnee(ctx, repos, c, dnfonlineevent.ReturneeCommand{
		AccountID: s.accountIDForSession(session), CharacterID: charID, Now: now, Login: login, Claim: claim, Guide: guide, GuideExtra: extraGuide, Observation: o,
		Policy:   s.portedActivityPolicy,
		Validate: s.currentPortedRequestValidator(session, characterID, false),
		Deliver:  s.currentPortedMailDelivery(ctx, session, charID, c.MailTitle, c.MailBody, "seria_returnee"),
	})
}

func (s *Service) prepareCurrentReturnee(session *gameSession) error {
	if !s.currentPortedActivityEnabled(1370) {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 || (session.returneePreparedCharacter == session.selectedCharacterID && session.returneePreparedGeneration == session.characterGeneration) {
		return nil
	}
	state, _, err := s.settleCurrentReturnee(session, !session.returneeConnectionObserved, 0, nil)
	if err != nil {
		return err
	}
	session.returneeConnectionObserved = true
	session.returneePreparedCharacter, session.returneePreparedGeneration = session.selectedCharacterID, session.characterGeneration
	session.returneeActive = state.ActiveAt(s.gameplayNow())
	if session.returneeActive {
		session.returneePreparedExpiresAt = state.ExpiresAt.Unix()
	} else {
		session.returneePreparedExpiresAt = 0
	}
	return nil
}

const currentReturneeSceneFlagKey = "wire_seria_returnee_active"

func currentReturneeSceneFlag(character dnfrepo.CharacterRecord, hasCharacter bool) byte {
	if hasCharacter && character.Stats[currentReturneeSceneFlagKey] == 1 {
		return 1
	}
	return 0
}

// currentReturneeSceneCharacter只投影已认证的当前角色资格，不保存临时Stats。
func (s *Service) currentReturneeSceneCharacter(session *gameSession, charID uint16, character dnfrepo.CharacterRecord, hasCharacter bool) (dnfrepo.CharacterRecord, error) {
	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats[currentReturneeSceneFlagKey] = 0
	if s.options.currentClientVersion != "official-24.9" || !hasCharacter || session == nil ||
		charID == 0 || charID != session.selectedCharacterID ||
		character.CharacterID != strconv.Itoa(int(charID)) || character.AccountID != s.accountIDForSession(session) ||
		!s.currentPortedActivityEnabled(1370) {
		return character, nil
	}
	if err := s.prepareCurrentReturnee(session); err != nil {
		return character, fmt.Errorf("准备原生回归入口: %w", err)
	}
	if session.returneePreparedCharacter == charID && session.returneePreparedGeneration == session.characterGeneration &&
		session.returneeActive && session.returneePreparedExpiresAt > s.gameplayNow().Unix() {
		character.Stats[currentReturneeSceneFlagKey] = 1
	}
	return character, nil
}

func (s *Service) saveCurrentReturneeLastSeen(session *gameSession) {
	if session == nil || session.returneePreparedCharacter == 0 {
		return
	}
	if _, _, err := s.settleCurrentReturnee(session, false, 0, nil); err != nil {
		s.logGameEvent(session, "回归离线时间保存失败", "error", err)
	}
}

func (s *Service) sendCurrentReturneeState(session *gameSession, force bool) error {
	if !s.currentPortedActivityEnabled(1370) {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	state, _, err := s.settleCurrentReturnee(session, false, 0, nil)
	if err != nil {
		return err
	}
	s.returneeLuckyMu.Lock()
	c := s.returneeCatalog
	s.returneeLuckyMu.Unlock()
	if c == nil {
		return nil
	}
	now := s.gameplayNow()
	session.returneeActive = state.ActiveAt(now)
	session.returneePreparedExpiresAt = 0
	if session.returneeActive {
		session.returneePreparedExpiresAt = state.ExpiresAt.Unix()
	}
	p := currentReturneeProjection{
		Character:       session.selectedCharacterID,
		Generation:      session.characterGeneration,
		Body:            buildCurrentReturneeBodyAt(c, state, now),
		ActorGeneration: session.sceneBootstrapActorGeneration,
	}
	if session.returneeActive {
		p.ExpiresAt = state.ExpiresAt.Unix()
	}
	// The op304 timer lives in the global premium manager, across actor rebuilds.
	// A positive value also calls sub_2102530 and restarts ALL contract notices.
	// Sync only on character selection or a real period change; scene transitions
	// restore the actor/mission state through their existing packets.
	sameTimeOwner := p.Character == session.returneeTimeCharacter && p.Generation == session.returneeTimeGeneration
	timeChanged := !sameTimeOwner || p.ExpiresAt != session.returneeTimeExpiresAt
	if timeChanged && (p.ExpiresAt != 0 || (session.returneeTimeExpiresAt != 0 && sameTimeOwner)) {
		var body [5]byte
		if session.returneeActive {
			seconds := (state.ExpiresAt.Sub(now) + time.Second - 1) / time.Second
			binary.LittleEndian.PutUint32(body[:], uint32(seconds))
		}
		// Legacy return-gift availability is separate from the 1370 missions.
		if err := s.sendGameUpperRawClass(session, currentReturneeTimeID, body[:], 0); err != nil {
			return err
		}
	}
	// Commit the timer separately so a failed 1370 retry cannot replay notices.
	session.returneeTimeCharacter, session.returneeTimeGeneration = p.Character, p.Generation
	session.returneeTimeExpiresAt = p.ExpiresAt
	if force || p != session.returneeProjection {
		if err := s.sendGameUpperRawClass(session, currentReturneeStateID, p.Body[:], 0); err != nil {
			return err
		}
		session.returneeProjection = p
		s.logGameEvent(session, "回归任务状态下发", "active", session.returneeActive,
			"expires_at", state.ExpiresAt, "claimed", state.Claimed, "time_changed", timeChanged)
	}
	return nil
}

func (s *Service) sendCurrentLuckyCharacterState(session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	enabled := s.currentPortedActivityEnabled(1054)
	if !enabled && session.luckyPreparedCharacter == 0 {
		return nil
	}
	_, c, err := s.currentReturneeLuckyCatalogs(context.Background())
	if err != nil || c == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	ch, _, err := s.currentLuckyFatigueCharacter(ctx, session)
	if err != nil {
		return err
	}
	active := s.currentLuckyCharacterEffect(session, ch) != nil
	if !active {
		c = nil
	}
	if session.luckyPreparedCharacter != session.selectedCharacterID || session.luckyPreparedGeneration != session.characterGeneration || session.luckyPreparedActorGeneration != session.sceneBootstrapActorGeneration || session.luckyPreparedActive.Load() != active {
		if err := s.sendGameUpperRawClass(session, currentLuckyCharacterStateID, buildCurrentLuckyCharacterBody(c), 0); err != nil {
			return err
		}
		session.luckyPreparedCharacter, session.luckyPreparedGeneration = session.selectedCharacterID, session.characterGeneration
		session.luckyPreparedActorGeneration = session.sceneBootstrapActorGeneration
		session.luckyPreparedActive.Store(active)
	}
	return s.sendCurrentLuckyFatigueState(session, false)
}

func (s *Service) handleCurrentReturneeCommand(session *gameSession, opcode uint16, body []byte) error {
	if session == nil {
		return nil
	}
	if !s.currentPortedActivityEnabled(1370) || !s.currentPortedSessionCurrent(session, session.selectedCharacterID) {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, 3}, 1)
	}
	mode := byte(0)
	if opcode == currentReturneeGuideID {
		// 现用发送器先写u8模式0，再写u32引导位；不能作为旧版u32清主线请求。
		if len(body) != 5 || body[0] > 1 {
			return s.sendGameUpperRawClass(session, opcode, []byte{0, 3}, 1)
		}
		mode = body[0]
		body = body[1:]
	} else if opcode != currentReturneeClaimID || len(body) != 4 {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, 3}, 1)
	}
	id := binary.LittleEndian.Uint32(body)
	claim := id
	var guide *uint32
	var extraGuide *uint32
	if opcode == currentReturneeClaimID && id == 0 {
		return s.sendGameUpperRawClass(session, opcode, []byte{0, 3}, 1)
	}
	if opcode == currentReturneeGuideID {
		if (mode == 0 && id >= 46) || (mode == 1 && id >= 512) {
			return s.sendGameUpperRawClass(session, opcode, []byte{0, 3}, 1)
		}
		claim = 0
		if mode == 0 {
			guide = &id
		} else {
			extraGuide = &id
		}
	}
	_, mails, err := s.settleCurrentReturneeWithGuide(session, false, claim, guide, extraGuide)
	if err != nil {
		s.logGameEvent(session, "回归任务请求拒绝", "opcode", opcode, "mission", id, "error", err)
		return s.sendGameUpperRawClass(session, opcode, []byte{0, 22}, 1)
	}
	if err := s.sendGameUpperRawClass(session, opcode, []byte{1}, 1); err != nil {
		return err
	}
	if err := s.sendCurrentReturneeState(session, true); err != nil {
		return err
	}
	if len(mails) > 0 {
		return s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "seria_returnee")
	}
	return nil
}
