package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentLuckyFatigueStateID uint16 = 490
const currentLuckyFatigueToggleID uint16 = 562

func parseCurrentLuckyFatigueBlacklist(text string) (map[int64]bool, error) {
	doc, err := dnfpvf.Parse("etc/chn_fatigueaccelchkdungeon.etc", text)
	if err != nil {
		return nil, err
	}
	values, found := doc.Section("unable dungeon")
	if !found {
		return nil, fmt.Errorf("缺少幸运角色疲劳加速禁用副本配置")
	}
	blocked := make(map[int64]bool)
	for _, v := range values {
		if v.Kind != dnfpvf.TokenInt || v.Int <= 0 {
			return nil, fmt.Errorf("幸运角色疲劳加速禁用副本无效")
		}
		blocked[v.Int] = true
	}
	return blocked, nil
}

type currentLuckyFatigueProjection struct {
	Character                   uint16
	Generation, ActorGeneration uint64
	Body                        [3]byte
}

// 当前原生490读取u16经验加速百分比、u8状态。原生复选框刷新函数中
// 0=可操作且未勾选，1=可操作且已勾选，3=禁用；不能与全局场景枚举混用。
func buildCurrentLuckyFatigueBody(percent int64, available, enabled bool) [3]byte {
	body := [3]byte{0, 0, 3}
	if available && percent > 0 && percent <= 1000 {
		binary.LittleEndian.PutUint16(body[:], uint16(percent))
		body[2] = 0
		if enabled {
			body[2] = 1
		}
	}
	return body
}

func (s *Service) currentLuckyFatigueCharacter(ctx context.Context, session *gameSession) (repo.CharacterRecord, repo.Group, error) {
	if session == nil || !s.currentPortedSessionCurrent(session, session.selectedCharacterID) {
		return repo.CharacterRecord{}, repo.Group{}, fmt.Errorf("幸运角色会话已失效")
	}
	r, ok := s.repositoryGroup()
	if !ok || r.Character == nil {
		return repo.CharacterRecord{}, r, fmt.Errorf("幸运角色仓储不可用")
	}
	ch, found, err := r.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return ch, r, err
	}
	if !found || ch.AccountID != s.accountIDForSession(session) || ch.Stats["delete_flag"] != 0 {
		return ch, r, fmt.Errorf("幸运角色归属无效")
	}
	return ch, r, nil
}

// 仅两个成功选角入口调用；复用角色疲劳事务清除上次勾选，不扣疲劳，
// 不在场景刷新、每日刷新或490状态投影时重置本次登录的新选择。
func (s *Service) resetCurrentLuckyFatigueOnSelect(ctx context.Context, session *gameSession) (repo.CharacterRecord, error) {
	if session == nil || session.selectedCharacterID == 0 || session.loginRevoked.Load() {
		return repo.CharacterRecord{}, fmt.Errorf("疲劳燃烧选角会话无效")
	}
	id, generation, account := session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session)
	character, err := s.updateCurrentCharacterFatigueWithDebit(ctx, session, func(current *repo.CharacterRecord) error {
		if session.selectedCharacterID != id || session.characterGeneration != generation ||
			s.accountIDForSession(session) != account || session.loginRevoked.Load() {
			return fmt.Errorf("疲劳燃烧重置期间选角已变化")
		}
		// 未设置本来就是关闭，不向普通选角快照追加无用字段。
		if current.Stats["fatigue_acceleration"] != 0 {
			current.Stats["fatigue_acceleration"] = 0
		}
		return nil
	})
	if err != nil {
		return repo.CharacterRecord{}, err
	}
	session.luckyFatigueProjection = currentLuckyFatigueProjection{}
	s.logGameEvent(session, "选角已关闭疲劳燃烧", "角色编号", id, "会话代次", generation)
	return character, nil
}

func (s *Service) sendCurrentLuckyFatigueState(session *gameSession, force bool) error {
	if session == nil || (!s.currentPortedActivityConfigured(1054) && session.luckyFatigueProjection.Character == 0) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	ch, _, err := s.currentLuckyFatigueCharacter(ctx, session)
	if err != nil {
		return err
	}
	c := s.currentLuckyCharacterEffect(session, ch)
	var percent int64
	if c != nil {
		percent = c.FatigueBurn
	}
	p := currentLuckyFatigueProjection{Character: session.selectedCharacterID, Generation: session.characterGeneration, ActorGeneration: session.sceneBootstrapActorGeneration,
		Body: buildCurrentLuckyFatigueBody(percent, percent > 0, ch.Stats["fatigue_acceleration"] == 1)}
	if !force && p == session.luckyFatigueProjection {
		return nil
	}
	if !s.currentPortedSessionCurrent(session, p.Character) {
		return fmt.Errorf("幸运角色状态代次已变化")
	}
	if err := s.sendGameUpperRawClass(session, currentLuckyFatigueStateID, p.Body[:], 0); err != nil {
		return err
	}
	session.luckyFatigueProjection = p
	return nil
}

func luckyFatigueGameplayModule() gameplayModuleDefinition {
	h := func(s *Service, session *gameSession, q gameplayRequest) error {
		if !requireDefaultGameplayClassification(s, session, q, "幸运角色疲劳加速请求拒绝", "疲劳加速请求分类不符") {
			return nil
		}
		return s.handleCurrentLuckyFatigueToggle(session, q.Body)
	}
	return gameplayModuleDefinition{Name: "lucky-fatigue", UpperHandlers: map[uint16]gameplayHandler{currentLuckyFatigueToggleID: h},
		LegacyHandlers:    map[uint16]gameplayHandler{currentLuckyFatigueToggleID: h},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{currentLuckyFatigueToggleID: func(body []byte) []byte { return stripLegacyTransportTrailer(body, 1) }}}
}

func (s *Service) handleCurrentLuckyFatigueToggle(session *gameSession, body []byte) error {
	fail := func() error { return s.sendGameUpperRawClass(session, currentLuckyFatigueToggleID, []byte{0, 0x16}, 1) }
	if len(body) != 1 || body[0] > 1 {
		return fail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	_, c, err := s.currentReturneeLuckyCatalogs(ctx)
	if err != nil || c == nil {
		return fail()
	}
	ch, r, err := s.currentLuckyFatigueCharacter(ctx, session)
	if err != nil || r.CharacterSettlement == nil {
		return fail()
	}
	id := session.selectedCharacterID
	valid := s.currentPortedRequestValidator(session, id, false)
	err = r.CharacterSettlement.WithinCharacterSettlement(ctx, ch.CharacterID, func(tx repo.Group) error {
		if !valid() {
			return fmt.Errorf("疲劳加速会话已变化")
		}
		current, found, err := tx.Character.Load(ctx, ch.CharacterID)
		if err != nil {
			return err
		}
		if !found || current.AccountID != s.accountIDForSession(session) {
			return fmt.Errorf("疲劳加速角色归属无效")
		}
		if body[0] == 1 {
			effect := s.currentLuckyCharacterEffect(session, current)
			if effect == nil || effect.FatigueBurn == 0 {
				return fmt.Errorf("当前角色没有疲劳加速资格")
			}
		}
		current = repo.CloneCharacter(current)
		if current.Stats == nil {
			current.Stats = make(map[string]int64)
		}
		current.Stats["fatigue_acceleration"] = int64(body[0])
		current.UpdatedAt = s.gameplayNow()
		if !valid() {
			return fmt.Errorf("疲劳加速提交代次已变化")
		}
		return repo.SaveCharacterFields(ctx, tx.Character, current, repo.CharacterFieldStats)
	})
	if err != nil {
		s.logGameEvent(session, "幸运角色疲劳加速保存失败", "error", err)
		return fail()
	}
	// 原生562成功应答只有通用成功标志，实际开关状态由490通知同步。
	if err := s.sendGameUpperRawClass(session, currentLuckyFatigueToggleID, []byte{1}, 1); err != nil {
		return err
	}
	return s.sendCurrentLuckyFatigueState(session, true)
}

type currentLuckyRoomFatigueReceipt struct{ BonusPercent, Cost, Remaining int64 }

// 只在普通逐房间扣费事务内调用；默认关闭，开启后实际支付第二点才享受本房间燃烧经验。
func (s *Service) debitCurrentLuckyRoomFatigue(session *gameSession, ch *repo.CharacterRecord, dungeon worldmap.Dungeon) currentLuckyRoomFatigueReceipt {
	before := ch.Stats["fatigue"]
	var bonus int64
	if c := s.currentLuckyCharacterEffect(session, *ch); c != nil && c.FatigueBurn > 0 && ch.Stats["fatigue_acceleration"] == 1 && before >= 2 && !currentDungeonUsesNoFatigue(dungeon) && !currentGuildDungeonEntryID(dungeon.ID) && !c.NoFatigueBurnDungeons[dungeon.ID] {
		bonus = c.FatigueBurn
	}
	dnffatigue.Debit(ch)
	if bonus > 0 {
		dnffatigue.Debit(ch)
	}
	return currentLuckyRoomFatigueReceipt{BonusPercent: bonus, Cost: before - ch.Stats["fatigue"], Remaining: ch.Stats["fatigue"]}
}

func recordCurrentLuckyFatigueRoom(runtime *runtimeDungeonState, coordinate worldmap.RoomCoordinate, percent int64) {
	if percent <= 0 {
		return
	}
	if runtime.luckyFatigueRooms == nil {
		runtime.luckyFatigueRooms = make(map[worldmap.RoomCoordinate]int64)
	}
	runtime.luckyFatigueRooms[coordinate] = percent
}

func (s *Service) currentLuckyRoomBurnPercent(runtime *runtimeDungeonState) int64 {
	if runtime == nil || runtime.Session == nil {
		return 0
	}
	// The level-limited benefit ends at90 even inside a room whose previous debit
	// recorded a burn bonus. Replays of already committed awards stay frozen.
	s.returneeLuckyMu.Lock()
	c := s.luckyCharacterCatalog
	s.returneeLuckyMu.Unlock()
	if c != nil && c.Level1To89Only && runtime.Character.Level >= 90 {
		return 0
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		return 0
	}
	return runtime.luckyFatigueRooms[scene.Coordinate]
}

func (s *Service) currentLuckyUniqueDropPercent(session *gameSession, ch repo.CharacterRecord) int64 {
	if session == nil || session.loginRevoked.Load() || ch.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || ch.AccountID != s.accountIDForSession(session) {
		return 0
	}
	if c := s.currentLuckyCharacterEffect(session, ch); c != nil {
		return c.UniqueDrop
	}
	return 0
}
