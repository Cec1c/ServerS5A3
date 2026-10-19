package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentPVPFatigueReward struct {
	Experience uint32
	Gold       uint32
}

type currentPVPFatigueStandard struct {
	Level int
	Exp   int64
	Gold  int64
}

type currentPVPFatigueRules struct {
	ArcadeUse        int64
	ArcadeDifficulty map[byte]int64
	ColosseumUse     int64
	ColosseumExpPct  int64
	ColosseumGoldPct int64
	ExpCalcConst     int64
	GoldCalcConst    int64
	Standards        []currentPVPFatigueStandard
}

func currentPVPFatigueSectionInts(doc *dnfpvf.Document, start, end int) []int64 {
	values := make([]int64, 0)
	for _, token := range doc.Tokens[start:end] {
		if token.Kind == dnfpvf.TokenInt {
			values = append(values, token.Int)
		}
	}
	return values
}

func loadCurrentPVPFatigueRulesFromSource(source initialEquipmentTextSource) (*currentPVPFatigueRules, error) {
	text, err := source.ReadText("etc/pvpfatigueinfo.etc")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("etc/pvpfatigueinfo.etc", text)
	if err != nil {
		return nil, err
	}
	rules := &currentPVPFatigueRules{ArcadeDifficulty: make(map[byte]int64)}
	parent := ""
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		if section.Start < 0 || section.End < section.Start || section.End > len(doc.Tokens) {
			return nil, fmt.Errorf("invalid PVP fatigue section %q", section.Name)
		}
		values := currentPVPFatigueSectionInts(doc, section.Start, section.End)
		switch name {
		case "arcademode", "colosseum":
			parent = name
			continue
		case "/arcademode", "/colosseum":
			parent = ""
			continue
		case "fatigue accelerate use":
			if len(values) == 0 {
				continue
			}
			if len(values) != 1 || values[0] < 0 || values[0] > math.MaxInt32 {
				return nil, fmt.Errorf("invalid PVP fatigue use")
			}
			if parent == "arcademode" {
				rules.ArcadeUse = values[0]
			} else if parent == "colosseum" {
				rules.ColosseumUse = values[0]
			}
		case "difficulty":
			if len(values) == 0 {
				continue
			}
			if parent != "arcademode" || len(values) == 0 || len(values)%2 != 0 {
				return nil, fmt.Errorf("invalid PVP arcade difficulty table")
			}
			for i := 0; i < len(values); i += 2 {
				if values[i] < 0 || values[i] > math.MaxUint8 || values[i+1] < 0 || values[i+1] > math.MaxInt32 {
					return nil, fmt.Errorf("invalid PVP arcade difficulty row")
				}
				rules.ArcadeDifficulty[byte(values[i])] = values[i+1]
			}
		case "exp reward percent":
			if len(values) == 0 {
				continue
			}
			if parent == "colosseum" && len(values) == 1 {
				rules.ColosseumExpPct = values[0]
			}
		case "gold reward percent":
			if len(values) == 0 {
				continue
			}
			if parent == "colosseum" && len(values) == 1 {
				rules.ColosseumGoldPct = values[0]
			}
		case "fatigue and reward standard value":
			if len(values) == 0 {
				continue
			}
			if len(values) == 0 || len(values)%3 != 0 {
				return nil, fmt.Errorf("invalid PVP fatigue reward table len=%d values=%v", len(values), values)
			}
			for i := 0; i < len(values); i += 3 {
				if values[i] <= 0 || values[i] > 255 || values[i+1] < 0 || values[i+2] < 0 {
					return nil, fmt.Errorf("invalid PVP fatigue reward row")
				}
				rules.Standards = append(rules.Standards, currentPVPFatigueStandard{Level: int(values[i]), Exp: values[i+1], Gold: values[i+2]})
			}
		case "exp calc const":
			if len(values) == 0 {
				continue
			}
			if len(values) == 1 {
				rules.ExpCalcConst = values[0]
			}
		case "gold calc const":
			if len(values) == 0 {
				continue
			}
			if len(values) == 1 {
				rules.GoldCalcConst = values[0]
			}
		}
	}
	if rules.ArcadeUse <= 0 || len(rules.ArcadeDifficulty) == 0 || rules.ExpCalcConst <= 0 || rules.GoldCalcConst <= 0 || len(rules.Standards) == 0 {
		return nil, fmt.Errorf("incomplete PVP fatigue rules")
	}
	return rules, nil
}

func (s *Service) loadCurrentPVPFatigueRules() (*currentPVPFatigueRules, error) {
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.pvpFatigueRules != nil {
		return s.pvpFatigueRules, nil
	}
	archive, err := s.initialEquipmentArchiveLocked()
	if err != nil {
		return nil, err
	}
	rules, err := loadCurrentPVPFatigueRulesFromSource(archive)
	if err != nil {
		return nil, err
	}
	s.pvpFatigueRules = rules
	return rules, nil
}

func (rules *currentPVPFatigueRules) arcadeFatigue(difficulty byte) (int64, bool) {
	amount, ok := rules.ArcadeDifficulty[difficulty]
	return amount, ok
}

func (rules *currentPVPFatigueRules) arcadeReward(level int) currentPVPFatigueReward {
	if level < rules.Standards[0].Level {
		level = rules.Standards[0].Level
	}
	row := rules.Standards[len(rules.Standards)-1]
	for _, candidate := range rules.Standards {
		if candidate.Level > level {
			break
		}
		row = candidate
	}
	return currentPVPFatigueReward{
		Experience: uint32(min(max(row.Exp/rules.ExpCalcConst, 0), math.MaxUint32)),
		Gold:       uint32(min(max(row.Gold/rules.GoldCalcConst, 0), math.MaxUint32)),
	}
}

func buildCurrentPVPFatigueUseBody(success bool) []byte {
	if success {
		return []byte{1}
	}
	return []byte{0}
}

type currentPVPArcadeRun struct {
	startedAt       time.Time
	difficulty      byte
	usageAttempted  bool
	paid            bool
	round           byte
	total           byte
	roundEnded      bool
	roundWon        bool
	resultRequested bool
	won             bool
	finished        bool
	reward          *dnfpvp.ArcadeRewardSnapshot
}

func parseCurrentPVPFatigueUsage(body []byte) (byte, byte, bool) {
	if len(body) != 2 || (body[0] != 1 && body[0] != 2) ||
		(body[0] == 1 && (body[1] < 1 || body[1] > 3)) || (body[0] == 2 && body[1] != 0) {
		return 0, 0, false
	}
	if body[0] == 2 {
		return 2, 0, true
	}
	return 1, body[1] - 1, true
}

func parseCurrentPVPArcadeResult(body []byte) (byte, bool, byte, bool) {
	if len(body) != 3 || body[1] > 1 {
		return 0, false, 0, false
	}
	mode, difficulty, ok := parseCurrentPVPFatigueUsage([]byte{body[0], body[2]})
	return mode, body[1] == 1, difficulty, ok
}

// 本文件属于决斗场街机模式(op1271/1272/1285)的移植。参考实现里
// handleCurrentPVPFatigueAcceleration(op562 疲劳值加速勾选框)只是恰好写在同一
// 文件里,它本身不是决斗场功能:它需要 fatigue_burn.go 整套运行时同步
// (currentFatigueBurnRules / sendCurrentFatigueBurnState /
// currentFatigueBurnToggleCharacter)、returnee lucky 角色表,以及疲劳 owner 的
// BurnEnabled 双倍扣除语义。
//
// 本次只移植决斗场代码,所以 op562 维持本树原本的状态:不注册、不处理。
// 刻意不做"只存 fatigue_acceleration 并回成功"的降级版——那会让客户端的勾选框
// 显示为已开启而实际不双倍扣疲劳,比不响应更容易误导玩家。
// 移植时已经落地的决斗场部分不受影响:街机付费走 handleCurrentPVPFatigueUsage,
// 结算走 handleCurrentPVPArcadeRoundInfo / handleCurrentPVPArcadeResult。

func currentPVPArcadeRoom(session *gameSession) (*currentPVPRoom, bool) {
	if session == nil || session.selectedCharacterID == 0 {
		return nil, false
	}
	room := session.pvpRoom
	if room == nil || room.battleMode != 10 || room.request.dojoDifficulty < 1 || room.request.dojoDifficulty > 3 {
		return nil, false
	}
	seat := room.seatFor(session)
	if !room.combatant(seat) || room.members[seat].characterID != session.selectedCharacterID {
		return nil, false
	}
	return room, true
}

// 1271 precedes the first 1285 start report. Only this explicit request
// authorizes payment; tutorial rounds and ordinary 1v1 never consume fatigue.
func (s *Service) handleCurrentPVPFatigueUsage(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	mode, difficulty, ok := parseCurrentPVPFatigueUsage(body)
	if !ok {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room, valid := currentPVPArcadeRoom(session)
	if !valid {
		return nil
	}
	if mode != 1 || difficulty+1 != room.request.dojoDifficulty {
		return s.sendGameUpperRawClass(session, 1271, []byte{0}, 1)
	}
	if room.arcade == nil {
		room.arcade = &currentPVPArcadeRun{startedAt: s.gameplayNow(), difficulty: difficulty}
	}
	run := room.arcade
	if run.round != 0 && !run.usageAttempted || run.finished {
		return s.sendGameUpperRawClass(session, 1271, []byte{0}, 1)
	}
	if !run.usageAttempted {
		rules, err := s.loadCurrentPVPFatigueRules()
		if err != nil {
			return err
		}
		amount, found := rules.arcadeFatigue(difficulty)
		if !found {
			return s.sendGameUpperRawClass(session, 1271, []byte{0}, 1)
		}
		repos, found := s.repositoryGroup()
		if !found {
			return dnfrepo.ErrRentalAssetTransactionUnavailable
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		character, exists, err := repos.Character.Load(ctx, fmt.Sprint(session.selectedCharacterID))
		if err != nil {
			return err
		}
		if !exists || character.AccountID != s.accountIDForSession(session) {
			return fmt.Errorf("决斗角色不存在")
		}
		quests, exists, err := repos.Quest.Load(ctx, character.CharacterID)
		if err != nil {
			return err
		}
		if !exists || !dnfpvp.ArcadeTrainingCompleted(quests) {
			s.logGameEvent(session, "game-pvp-arcade-usage-rejected", "reason", "windbell_training_incomplete")
			run.usageAttempted = true
			return s.sendGameUpperRawClass(session, 1271, []byte{0}, 1)
		}
		result, err := dnfpvp.ConsumeArcadeFatigue(ctx, repos, dnfpvp.ConsumeArcadeCommand{
			AccountID: character.AccountID, CharacterID: character.CharacterID,
			Amount: amount, RequireFull: true, UpdatedAt: s.gameplayNow(),
		})
		if err != nil {
			if errors.Is(err, dnfpvp.ErrFatigueInsufficient) {
				run.usageAttempted = true
				return s.sendGameUpperRawClass(session, 1271, []byte{0}, 1)
			}
			return err
		}
		// Commit the in-memory receipt before any socket write.
		run.usageAttempted, run.paid = true, true
		s.logGameEvent(session, "game-pvp-arcade-fatigue-consumed", "room_id", room.id,
			"amount", result.Consumed, "remaining", result.After, "difficulty", difficulty)
	}
	if run.paid {
		repos, _ := s.repositoryGroup()
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		character, found, err := repos.Character.Load(ctx, fmt.Sprint(session.selectedCharacterID))
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("决斗角色不存在")
		}
		if err := s.sendGameUpperRawClass(session, currentFatigueMsgID, buildCurrentFatigueBody(character, true), 0); err != nil {
			return err
		}
	}
	return s.sendGameUpperRawClass(session, 1271, buildCurrentPVPFatigueUseBody(run.paid), 1)
}

func parseCurrentPVPArcadeRoundInfo(body []byte) (end, won bool, ok bool) {
	if len(body) != 19 || body[0] > 1 || body[5] == 0 || body[6] == 0 ||
		body[6] > body[5] || body[12] > 1 || (body[0] == 0 && body[12] != 0) {
		return false, false, false
	}
	return body[0] == 1, body[12] == 1, true
}

func (s *Service) handleCurrentPVPArcadeRoundInfo(session *gameSession, body []byte) error {
	end, won, ok := parseCurrentPVPArcadeRoundInfo(body)
	if !ok {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room, ok := currentPVPArcadeRoom(session)
	if !ok {
		return nil
	}
	if room.arcade == nil {
		if end || body[6] != 1 {
			return nil
		}
		room.arcade = &currentPVPArcadeRun{startedAt: s.gameplayNow(), difficulty: room.request.dojoDifficulty - 1}
	}
	run := room.arcade
	if run.finished || (run.total != 0 && run.total != body[5]) {
		return nil
	}
	if end {
		if run.round != body[6] || run.roundEnded {
			return nil
		}
		run.roundEnded, run.roundWon = true, won
	} else {
		if run.round != 0 && (!run.roundEnded ||
			(run.roundWon && body[6] != run.round+1) || (!run.roundWon && body[6] != run.round)) {
			return nil
		}
		run.total, run.round, run.roundEnded = body[5], body[6], false
		room.started = true
	}
	s.logGameEvent(session, "game-pvp-arcade-round", "round", run.round, "total", run.total,
		"ended", end, "won", won, "paid", run.paid)
	if run.resultRequested {
		return s.settleCurrentPVPArcadeReward(session, room)
	}
	return nil
}

func currentPVPArcadeRewardReceipt(room *currentPVPRoom) string {
	if room == nil || room.arcade == nil {
		return "pvp_arcade_reward_invalid"
	}
	return fmt.Sprintf("pvp_arcade_reward_%d_%d", room.id, room.arcade.startedAt.UnixNano())
}

func (s *Service) handleCurrentPVPArcadeResult(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	mode, won, difficulty, ok := parseCurrentPVPArcadeResult(body)
	if !ok {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room, ok := currentPVPArcadeRoom(session)
	if !ok || mode != 1 || room.arcade == nil || !room.arcade.paid || room.arcade.difficulty != difficulty {
		return s.sendGameUpperRawClass(session, 1272, []byte{0}, 1)
	}
	run := room.arcade
	if run.resultRequested && run.won != won {
		return s.sendGameUpperRawClass(session, 1272, []byte{0}, 1)
	}
	run.resultRequested, run.won = true, won
	return s.settleCurrentPVPArcadeReward(session, room)
}

func (s *Service) settleCurrentPVPArcadeReward(session *gameSession, room *currentPVPRoom) error {
	run := room.arcade
	if !run.won {
		run.finished = true
		return s.sendGameUpperRawClass(session, 1272, []byte{0}, 1)
	}
	if !run.roundEnded || !run.roundWon || run.round != run.total {
		return nil
	}
	if run.reward == nil {
		rules, err := s.loadCurrentPVPFatigueRules()
		if err != nil {
			return err
		}
		repos, ok := s.repositoryGroup()
		if !ok {
			return dnfrepo.ErrRentalAssetTransactionUnavailable
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		result, err := dnfpvp.ApplyArcadeReward(ctx, repos, s.accountIDForSession(session),
			fmt.Sprint(session.selectedCharacterID), currentPVPArcadeRewardReceipt(room),
			func(character dnfrepo.CharacterRecord, skill dnfrepo.SkillRecord) (dnfpvp.ArcadeRewardSnapshot, error) {
				return s.planCurrentPVPArcadeReward(ctx, rules, character, skill)
			}, s.gameplayNow())
		if err != nil {
			return err
		}
		run.reward = &result
		run.finished = true
		s.logGameEvent(session, "game-pvp-arcade-reward-committed", "room_id", room.id,
			"character_level", result.Character.Level, "experience", result.Reward.Experience, "gold", result.Reward.Gold)
	}
	result := run.reward
	if err := s.sendGameUpperRawClass(session, 14, buildCurrentGoldStateBody(result.Character.Stats["gold"]), 0); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID,
		buildCurrentFinishLoadingCharacterStateBody(result.Character, result.Skill.Points), 0); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 1272,
		buildCurrentPVPFatigueRewardBody(true, result.Reward.Experience, result.Reward.Gold), 1)
}

func (s *Service) planCurrentPVPArcadeReward(ctx context.Context, rules *currentPVPFatigueRules,
	character dnfrepo.CharacterRecord, skill dnfrepo.SkillRecord) (dnfpvp.ArcadeRewardSnapshot, error) {
	var result dnfpvp.ArcadeRewardSnapshot
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return result, err
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		return result, err
	}
	reward := rules.arcadeReward(character.Level)
	exp, exists := character.Stats["exp"]
	gold := character.Stats["gold"]
	if !exists || exp < 0 || exp > math.MaxUint32 || gold < 0 || gold > math.MaxInt32 {
		return result, fmt.Errorf("invalid arcade character experience or gold")
	}
	reward.Gold = uint32(min(int64(reward.Gold), math.MaxInt32-gold))
	planned, err := planCurrentDungeonSettlementProgression(tables, character.Level, uint32(exp), reward.Experience, skill.Points)
	if err != nil {
		return result, err
	}
	honorGain, err := currentHonorExpertExperienceGain(tables, character.Level, uint32(exp), reward.Experience)
	if err != nil {
		return result, err
	}
	if honorGain > 0 {
		honorTables, err := s.loadHonorTable(ctx)
		if err != nil {
			return result, err
		}
		honor, err := planCurrentHonorExpertProgress(honorTables, character, honorGain)
		if err != nil {
			return result, err
		}
		for key, value := range currentHonorExpertStats(honor) {
			character.Stats[key] = value
		}
	}
	if planned.Experience.LevelsGained > 0 {
		stats, err := s.characterPVFStatValuesForLevel(character, planned.Experience.NewLevel)
		if err != nil {
			return result, err
		}
		for key, value := range stats {
			character.Stats[key] = value
		}
	}
	character.Level = planned.Experience.NewLevel
	character.Stats["exp"] = int64(planned.Experience.NewExperience)
	character.Stats["gold"] = gold + int64(reward.Gold)
	skill.Points = planned.SkillPoints.New
	return dnfpvp.ArcadeRewardSnapshot{Character: character, Skill: skill,
		Reward: dnfpvp.ArcadeReward{Experience: reward.Experience, Gold: reward.Gold}}, nil
}

func buildCurrentPVPFatigueRewardBody(success bool, experience, gold uint32) []byte {
	if !success {
		return []byte{0}
	}
	var w packetWriter
	w.writeByte(1)
	w.writeUint32(experience)
	w.writeUint32(gold)
	return w.bytes()
}
