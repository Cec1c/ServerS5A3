package dnfbridge

import (
	"context"
	"errors"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentDungeonDeathBurstLimit = 32

// 与NPC已有读块收集共用拆包边界；坏帧或EOF交回原循环，先处理此前完整请求。
func (s *Service) collectCurrentDungeonDeathReadyPackets(session *gameSession, ahead *nativeResultReadAhead,
	packets []dnfproto.LatestGameStreamPacket, remaining []byte,
) ([]dnfproto.LatestGameStreamPacket, []byte, *nativeResultReadChunk) {
	budget := 16
	return s.collectCurrentDungeonDeathReadyPacketsBudget(session, ahead, packets, remaining, &budget)
}

func (s *Service) collectCurrentDungeonDeathReadyPacketsBudget(session *gameSession, ahead *nativeResultReadAhead,
	packets []dnfproto.LatestGameStreamPacket, remaining []byte, budget *int,
) ([]dnfproto.LatestGameStreamPacket, []byte, *nativeResultReadChunk) {
	if ahead == nil || len(packets) == 0 || !currentDungeonDeathBurstPacket(packets[0]) || currentDungeonDeathBurstSpan(packets) != len(packets) {
		return packets, remaining, nil
	}
	for *budget > 0 && len(packets) < currentDungeonDeathBurstLimit {
		chunk, ready := ahead.tryNext()
		if !ready {
			break
		}
		*budget--
		if chunk.err != nil {
			return packets, remaining, &chunk
		}
		joined := append(append([]byte(nil), remaining...), chunk.data...)
		more, tail, skipped, err := dnfproto.SplitLatestGameStream(joined, s.options.maxPacketBytes)
		if err != nil || skipped != 0 {
			return packets, remaining, &chunk
		}
		s.logCurrentGameReadWait(session, chunk)
		if len(chunk.data) > 0 {
			s.logGamePacket(session, "RECV", "game-raw", chunk.data)
		}
		packets, remaining = append(packets, more...), tail
		if currentDungeonDeathBurstSpan(packets) != len(packets) {
			break
		}
	}
	return packets, remaining, nil
}

// 仅在同一会话事件里消费已完整到达的连续请求。死亡、掉落及任务仍逐只
// 校验；经验事务推迟至死亡通知之后，未满级在每次升级边界先提交。
// 同一段经验逐只规划后合并事务，断线、错误及房间完成前排空；不使用异步计时器。
type currentDungeonDeathBurst struct {
	runtime          *runtimeDungeonState
	room             *runtimeDungeonRoom
	character        uint16
	generation       uint64
	pending          []currentDungeonDeathBurstAward
	projectedEXP     uint32
	levelBoundary    bool
	body             []byte
	last             currentDungeonMonsterExperienceCommitResult
	growth           uint32
	committed        int
	deferRewards     bool
	rewardsPending   bool
	adventurePending bool
}

type currentDungeonDeathBurstAward struct {
	monster runtimeDungeonMonster
	result  currentDungeonMonsterExperienceCommitResult
	tables  *progression.Tables
}

func currentDungeonDeathBurstPacket(packet dnfproto.LatestGameStreamPacket) bool {
	if packet.Kind != dnfproto.LatestGameStreamLegacy {
		return false
	}
	p, err := dnfproto.ParseLegacyGamePacket(packet.Data)
	return err == nil && p.Header.Cmd == 1 && p.Header.Type == uint16(dnfenum.CmdPacketDieMonster)
}

func currentDungeonDeathBurstSpan(packets []dnfproto.LatestGameStreamPacket) int {
	n := 0
	for n < len(packets) && n < currentDungeonDeathBurstLimit && currentDungeonDeathBurstPacket(packets[n]) {
		n++
	}
	if n < 2 {
		return 1
	}
	return n
}

func (s *Service) currentDungeonDeathBurstEnabled(session *gameSession) bool {
	runtime := session.dungeon.runtime
	return s.options.currentClientVersion == "official-24.9" && runtime != nil && runtime.Room != nil &&
		runtime.Tournament == nil && runtime.raidEntry == nil && runtime.guildPractice == nil && !currentFriendlyGuildPractice(runtime)
}

func (s *Service) activeCurrentDungeonDeathBurst(session *gameSession, runtime *runtimeDungeonState) *currentDungeonDeathBurst {
	b := session.dungeonDeathBurst
	if b == nil || b.runtime != runtime || session.dungeon.runtime != runtime ||
		b.room != runtime.Room || b.character != session.selectedCharacterID || b.generation != session.characterGeneration {
		return nil
	}
	return b
}

func (s *Service) handleCurrentDungeonDeathBurstPackets(session *gameSession, packets []dnfproto.LatestGameStreamPacket, prepare bool, readyAt ...time.Time) (resultErr error) {
	var at time.Time
	if len(readyAt) > 0 {
		at = readyAt[0]
	}
	_, resultErr = s.handleCurrentDungeonDeathBurstWithContinuation(session, packets, prepare, at, nil)
	return resultErr
}

// 调用方仍独占当前会话事件。单只处理返回后、经验提交前再检查已到达字节，
// 不等新包、不跨其它指令；新增帧各自保留就绪时间。每个事件最多32只、16个接续读块。
func (s *Service) handleCurrentDungeonDeathReadyPackets(session *gameSession, packets *[]dnfproto.LatestGameStreamPacket,
	index, span int, ahead *nativeResultReadAhead, remaining *[]byte, deferred **nativeResultReadChunk, readyAt *time.Time,
) (int, error) {
	budget := 16
	initialReadyAt := *readyAt
	continuedReadyAt := initialReadyAt
	next := func(consumed int) (dnfproto.LatestGameStreamPacket, time.Time, bool) {
		items := *packets
		cursor := index + consumed
		if cursor == len(items) && *deferred == nil && budget > 0 {
			extended, tail, held := s.collectCurrentDungeonDeathReadyPacketsBudget(session, ahead, items[index:], *remaining, &budget)
			*packets = append(items[:index], extended...)
			*remaining, *deferred = tail, held
			items = *packets
			continuedReadyAt = time.Now()
			*readyAt = continuedReadyAt
		}
		if cursor >= len(items) || !currentDungeonDeathBurstPacket(items[cursor]) {
			return dnfproto.LatestGameStreamPacket{}, time.Time{}, false
		}
		return items[cursor], continuedReadyAt, true
	}
	return s.handleCurrentDungeonDeathBurstWithContinuation(session, (*packets)[index:index+span], true, initialReadyAt, next)
}

func (s *Service) handleCurrentDungeonDeathBurstWithContinuation(session *gameSession, packets []dnfproto.LatestGameStreamPacket,
	prepare bool, readyAt time.Time, next func(int) (dnfproto.LatestGameStreamPacket, time.Time, bool),
) (consumed int, resultErr error) {
	runtime := session.dungeon.runtime
	if s.currentDungeonDeathBurstEnabled(session) && (len(packets) > 1 || next != nil) {
		session.dungeonDeathBurst = &currentDungeonDeathBurst{runtime: runtime, room: runtime.Room,
			character: session.selectedCharacterID, generation: session.characterGeneration,
			projectedEXP: statU32(runtime.Character, "exp", 0), deferRewards: true}
		defer func() {
			// 即使某次死亡通知写失败，已确认的死亡仍须执行原经验提交。
			b := session.dungeonDeathBurst
			resultErr = errors.Join(resultErr, s.flushCurrentDungeonDeathBurstLocked(session))
			session.dungeonDeathBurst = nil
			if b.rewardsPending {
				s.afterCurrentLevelUpRewardCommand(session)
			}
		}()
	}
	for {
		var packet dnfproto.LatestGameStreamPacket
		at := readyAt
		if consumed < len(packets) {
			packet = packets[consumed]
		} else {
			// 原处理入口在升级、清房及特殊对象前已排空pending。只在同一
			// 房间还有普通怪经验待提交时接续，不能越过这些生命周期边界。
			b := s.activeCurrentDungeonDeathBurst(session, runtime)
			if consumed >= currentDungeonDeathBurstLimit || next == nil || b == nil || len(b.pending) == 0 {
				break
			}
			var ok bool
			packet, at, ok = next(consumed)
			if !ok {
				break
			}
		}
		consumed++
		if b := s.activeCurrentDungeonDeathBurst(session, runtime); b != nil {
			b.deferRewards = true
		}
		// 每条请求都保留原callGameSession的替换登录隔离，不能因合并事件而
		// 在旧连接已撤销后继续接受后续击杀；此前接受的奖励仍由defer提交。
		if session.loginRevoked.Load() {
			return consumed, errLoginReplaced
		}
		if prepare {
			if err := s.prepareNativeResultDelivery(session, packet); err != nil {
				return consumed, err
			}
		}
		if !at.IsZero() {
			s.logCurrentDungeonDeathReadyWait(session, packet, at)
		}
		if err := s.handleGameStreamPacket(session, packet); err != nil {
			return consumed, err
		}
	}
	return consumed, nil
}

func (s *Service) queueCurrentDungeonDeathBurstExperience(session *gameSession, runtime *runtimeDungeonState, monster runtimeDungeonMonster) bool {
	b := s.activeCurrentDungeonDeathBurst(session, runtime)
	if b == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	resources, err := s.currentDungeonExperienceCommitResources()
	if err != nil {
		s.logGameEvent(session, "连续击杀经验准备失败", "monster_object_key", monster.ObjectKey, "error", err)
		return true
	}
	award, err := currentDungeonMonsterExperienceAwardFor(runtime, monster, resources.Sources)
	if err != nil {
		s.logGameEvent(session, "连续击杀经验准备失败", "monster_object_key", monster.ObjectKey, "error", err)
		return true
	}
	result := currentDungeonMonsterExperienceCommitResult{Award: award}
	if award.Gain > 0 {
		result, err = s.prepareCurrentDungeonMonsterExperienceBonuses(ctx, session, runtime, result)
		if err != nil {
			s.logGameEvent(session, "连续击杀经验准备失败", "monster_object_key", monster.ObjectKey, "error", err)
			return true
		}
	}
	b.pending = append(b.pending, currentDungeonDeathBurstAward{monster: monster, result: result, tables: resources.Progression})
	if award.Gain > 0 && runtime.Character.Level < currentAdventureCharacterLevelCap {
		// 这里只预测是否需要提交，不修改角色等级或给后续击杀使用虚拟状态。
		// 任一事务失败只会减少经验；到边界后按真实提交结果重新起算。
		gain := result.Award.Gain + result.GrowthContractBonus + result.GuildBonus + result.AttendanceMarbleBonus
		threshold, err := resources.Progression.ThresholdToNext(runtime.Character.Level)
		if err != nil {
			b.levelBoundary = true
			s.logGameEvent(session, "连续击杀升级边界预测失败，恢复即时提交", "error", err)
		} else {
			b.projectedEXP = saturatingCurrentDungeonUint32Add(b.projectedEXP, gain)
			b.levelBoundary = b.projectedEXP >= threshold
		}
	}
	return true
}

func (s *Service) sendCurrentDungeonMonsterExperienceState(session *gameSession, runtime *runtimeDungeonState, body []byte, result currentDungeonMonsterExperienceCommitResult) error {
	if b := s.activeCurrentDungeonDeathBurst(session, runtime); b != nil {
		b.body = append(b.body[:0], body...)
		b.last = result
		b.growth = saturatingCurrentDungeonUint32Add(b.growth, result.GrowthContractBonus)
		b.committed++
		b.adventurePending = b.adventurePending || result.AdventureExperienceGain > 0
		return nil
	}
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, body, 0); err != nil {
		return err
	}
	if result.AdventureExperienceGain > 0 {
		return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "monster_experience_committed")
	}
	return nil
}

// 调用者持有副本锁，或独占当前会话事件且所有死亡处理均已返回。
func (s *Service) flushCurrentDungeonDeathBurstLocked(session *gameSession) error {
	if err := s.commitCurrentDungeonDeathBurstLocked(session); err != nil {
		return err
	}
	b := session.dungeonDeathBurst
	if b == nil || len(b.body) == 0 {
		return nil
	}
	// 复用原构造器；等级、经验、SP/TP及名誉取最后一个成功事务的状态。
	body := buildCurrentFinishLoadingCharacterStateBodyWithPresentation(b.last.Character, b.last.Skill.Points,
		&currentFinishLoadingExperiencePresentation{GrowthContractBonus: b.growth})
	b.body = nil
	count := b.committed
	b.growth, b.committed = 0, 0
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "连续击杀经验汇总通知", "committed_count", count, "body_len", len(body))
	if b.adventurePending {
		b.adventurePending = false
		return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "monster_experience_batch_committed")
	}
	return nil
}

// 升级边界先提交本段事务，让后续击杀使用真实新等级；经验提示继续汇总。
func (s *Service) commitCurrentDungeonDeathBurstLocked(session *gameSession) error {
	b := session.dungeonDeathBurst
	if b == nil {
		return nil
	}
	runtime := b.runtime
	previousLevel := runtime.Character.Level
	pending := b.pending
	b.pending = nil
	var results []currentDungeonMonsterExperienceCommitResult
	if len(pending) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		var err error
		started := time.Now()
		results, err = s.commitCurrentDungeonExperienceBatch(ctx, session, runtime, pending)
		if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
			s.logGameEvent(session, "连续击杀经验提交耗时", "怪物数量", len(pending), "耗时毫秒", elapsed.Milliseconds(), "成功", err == nil)
		}
		cancel()
		if err != nil {
			s.logGameEvent(session, "连续击杀经验整段提交失败", "monster_count", len(pending), "error", err)
		}
	}
	for i, result := range results {
		monster := pending[i].monster
		recordCurrentDungeonMonsterExperience(runtime, monster, result)
		if result.Award.Gain == 0 {
			continue
		}
		runtime.Character = dnfrepo.CloneCharacter(result.Character)
		body := buildCurrentFinishLoadingCharacterStateBodyWithPresentation(result.Character, result.Skill.Points,
			&currentFinishLoadingExperiencePresentation{GrowthContractBonus: result.GrowthContractBonus})
		if err := s.sendCurrentDungeonMonsterExperienceState(session, runtime, body, result); err != nil {
			return err
		}
		s.logGameEvent(session, "连续击杀经验已提交", "monster_object_key", monster.ObjectKey,
			"experience_gain", result.Award.Gain, "growth_contract_bonus", result.GrowthContractBonus,
			"guild_bonus", result.GuildBonus, "attendance_marble_bonus", result.AttendanceMarbleBonus,
			"honor_expert_gain", result.HonorExpertGain, "new_total_exp", statU32(result.Character, "exp", 0))
	}
	b.projectedEXP = statU32(runtime.Character, "exp", 0)
	b.levelBoundary = false
	if runtime.Character.Level != previousLevel {
		// 本条命令末尾照常处理真实升级奖励，其他死亡只在整段末尾恢复检查。
		b.deferRewards = false
	}
	return nil
}
