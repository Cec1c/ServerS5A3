package dnfbridge

import (
	"fmt"
	"sort"
	"sync"
)

// ── 锦标赛赛制状态机 ─────────────────────────────────────────────────────────
//
// 逐条对拍厂商 Game\Dungeon\Tournament\TournamentDungeonRuntime.cs（575 行）：
//
//	15 支 NPC 队 + 1 支玩家队 = 16 队单败淘汰 = 4 轮（:402-488）。
//	Shuffle → Sort(TotalStrength) → PickNpcWinner（:410-426 / :532-540）。
//	PathActors 长度 = 4*partyCount（:490-515），**每条 IsBlocking = true**（:512）。
//	第 4 回合 actorType 升格：>=5 ⇒ 8（APC Boss），否则 ⇒ 3（怪物 Boss）（:505-506）。
//	死亡事件按 sequence 归轮：first = firstActorSequence + (round-1)*partyLimit（:288-305）。
//	第 4 轮全灭 ⇒ TournamentCompleted（:307-316）；玩家死亡 ⇒ Eliminated（:326-349）。

// tournamentActorSnapshot 对应厂商 TournamentDungeonRuntime.cs:8-29。
type tournamentActorSnapshot struct {
	Code      int64
	Strength  int64
	Name      string
	Level     byte
	ActorType byte
}

// tournamentTeamSnapshot 对应厂商 TournamentDungeonRuntime.cs:31-63。
type tournamentTeamSnapshot struct {
	Position      byte
	Members       []tournamentActorSnapshot
	IsPlayer      bool
	TotalStrength int64
}

func newTournamentTeamSnapshot(position byte, members []tournamentActorSnapshot, isPlayer bool) tournamentTeamSnapshot {
	frozen := append([]tournamentActorSnapshot(nil), members...)
	var strength int64
	for _, member := range frozen {
		if member.Strength > 0 {
			strength += member.Strength
		}
	}
	if strength > 0x7FFFFFFF {
		strength = 0x7FFFFFFF
	}
	return tournamentTeamSnapshot{
		Position:      position,
		Members:       frozen,
		IsPlayer:      isPlayer,
		TotalStrength: strength,
	}
}

func (t tournamentTeamSnapshot) withPosition(position byte) tournamentTeamSnapshot {
	return newTournamentTeamSnapshot(position, t.Members, t.IsPlayer)
}

// tournamentRoundSnapshot 对应厂商 TournamentDungeonRuntime.cs:65-80。
type tournamentRoundSnapshot struct {
	Number byte
	Teams  []tournamentTeamSnapshot
}

// tournamentPathActor 对应厂商 `GameWorld.Dungeon.MonsterSumInfo`
// 里锦标赛用到的那四个字段（:507-513）。
type tournamentPathActor struct {
	Code       int64
	Level      byte
	Type       byte
	IsBlocking bool
}

// tournamentTerminalResult 对应厂商 TournamentDungeonRuntime.cs:91-96。
type tournamentTerminalResult byte

const (
	tournamentActive tournamentTerminalResult = iota
	tournamentEliminated
	tournamentChampion
)

// tournamentActorDeathDisposition 对应厂商 TournamentDungeonRuntime.cs:82-89。
type tournamentActorDeathDisposition byte

const (
	tournamentDeathRejected tournamentActorDeathDisposition = iota
	tournamentDeathDuplicate
	tournamentDeathActorAccepted
	tournamentDeathRoundCompleted
	tournamentDeathTournamentCompleted
)

// tournamentActorDeathTransition 对应厂商 TournamentDungeonRuntime.cs:123-145。
type tournamentActorDeathTransition struct {
	Disposition    tournamentActorDeathDisposition
	CompletedRound byte
	CurrentRound   byte
}

func (t tournamentActorDeathTransition) accepted() bool {
	return t.Disposition >= tournamentDeathActorAccepted
}

func (t tournamentActorDeathTransition) roundCompleted() bool {
	return t.Disposition == tournamentDeathRoundCompleted ||
		t.Disposition == tournamentDeathTournamentCompleted
}

func (t tournamentActorDeathTransition) tournamentCompleted() bool {
	return t.Disposition == tournamentDeathTournamentCompleted
}

// tournamentDungeonRuntime 对应厂商 `TournamentDungeonRuntime`。
type tournamentDungeonRuntime struct {
	mu sync.Mutex

	Definition *tournamentDefinition
	Rounds     []tournamentRoundSnapshot
	PathActors []tournamentPathActor

	acceptedSequences map[int]struct{}
	currentRound      byte
	completedRounds   byte
	terminal          tournamentTerminalResult
	firstSequence     uint16
}

// newTournamentDungeonRuntime 是厂商
// `TournamentDungeonRuntimeFactory.TryCreate`（TournamentDungeonRuntime.cs:360-522）的移植。
//
// `next` 是"返回 [0,exclusiveMaximum) 的随机源"（厂商 `Func<int,int> next`）。
// 与厂商一致：`next` 越界**直接报错**（:564-573 抛异常），本仓返回 failureReason。
func newTournamentDungeonRuntime(
	definition *tournamentDefinition,
	partyCount int,
	next func(int) int,
) (*tournamentDungeonRuntime, string, bool) {
	if definition == nil {
		return nil, "tournament definition is missing", false
	}
	if partyCount != definition.PartyLimit {
		return nil, fmt.Sprintf(
			"party count %d does not match configured limit %d", partyCount, definition.PartyLimit), false
	}
	if next == nil {
		return nil, "random source is missing", false
	}

	// :389-400：按 PartyCount 分桶。
	candidates := make([]tournamentActorSnapshot, 0, len(definition.Candidates))
	for _, candidate := range definition.Candidates {
		if candidate.PartyCount != int64(partyCount) {
			continue
		}
		candidates = append(candidates, tournamentActorSnapshot{
			Code:      candidate.Code,
			Strength:  candidate.Strength,
			Name:      candidate.Name,
			Level:     candidate.Level,
			ActorType: candidate.ActorType,
		})
	}

	// :402-408：15*partyCount 硬门槛。
	requiredActors := tournamentNpcTeamCount * partyCount
	if len(candidates) < requiredActors {
		return nil, fmt.Sprintf("candidate pool has %d, requires %d", len(candidates), requiredActors), false
	}

	// :410-424：洗牌后按 partyCount 个一组切成 15 支 NPC 队。
	var shuffleErr error
	shuffle := func(values []tournamentActorSnapshot) {
		for index := len(values) - 1; index > 0; index-- {
			swap, err := tournamentNext(next, index+1)
			if err != nil {
				shuffleErr = err
				return
			}
			values[index], values[swap] = values[swap], values[index]
		}
	}
	shuffle(candidates)
	if shuffleErr != nil {
		return nil, shuffleErr.Error(), false
	}
	npcTeams := make([]tournamentTeamSnapshot, 0, tournamentNpcTeamCount)
	for teamIndex := 0; teamIndex < tournamentNpcTeamCount; teamIndex++ {
		members := make([]tournamentActorSnapshot, partyCount)
		for memberIndex := 0; memberIndex < partyCount; memberIndex++ {
			members[memberIndex] = candidates[teamIndex*partyCount+memberIndex]
		}
		npcTeams = append(npcTeams, newTournamentTeamSnapshot(0, members, false))
	}

	// :425-426：Strength 的唯一用途 —— 排序分档。**不是坐标**。
	sort.SliceStable(npcTeams, func(left, right int) bool {
		return npcTeams[left].TotalStrength < npcTeams[right].TotalStrength
	})

	// :428-437：玩家队伍是 partyCount 个空 actor（code/strength/level 全 0）。
	playerMembers := make([]tournamentActorSnapshot, partyCount)

	// :439-448：玩家位置随机，对手 = 玩家位置 ^ 1。
	playerPosition, err := tournamentNext(next, tournamentTeamCount)
	if err != nil {
		return nil, err.Error(), false
	}
	opponentPosition := playerPosition ^ 1
	firstRound := make([]tournamentTeamSnapshot, tournamentTeamCount)
	firstRound[playerPosition] = newTournamentTeamSnapshot(byte(playerPosition), playerMembers, true)
	firstRound[opponentPosition] = npcTeams[0].withPosition(byte(opponentPosition))

	// :450-461：剩下的 14 支队洗牌后依次填满空位。
	remainingTeams := append([]tournamentTeamSnapshot(nil), npcTeams[1:]...)
	shuffleTeams := func(values []tournamentTeamSnapshot) {
		for index := len(values) - 1; index > 0; index-- {
			swap, err := tournamentNext(next, index+1)
			if err != nil {
				shuffleErr = err
				return
			}
			values[index], values[swap] = values[swap], values[index]
		}
	}
	shuffleTeams(remainingTeams)
	if shuffleErr != nil {
		return nil, shuffleErr.Error(), false
	}
	remainingIndex := 0
	for position := 0; position < len(firstRound); position++ {
		// 厂商 :453-461 只填 `firstRound[position] == null` 的位置（玩家位与对手位已占）。
		if position == playerPosition || position == opponentPosition {
			continue
		}
		if remainingIndex >= len(remainingTeams) {
			return nil, "tournament bracket ran out of NPC teams", false
		}
		firstRound[position] = remainingTeams[remainingIndex].withPosition(byte(position))
		remainingIndex++
	}

	// :463-488：2..4 轮，玩家所在的那一对永远由玩家晋级；NPC 对由 TotalStrength 大者
	// 晋级，相等则随机（:532-540）。
	rounds := make([]tournamentRoundSnapshot, 0, tournamentRoundCount)
	rounds = append(rounds, tournamentRoundSnapshot{Number: 1, Teams: firstRound})
	previous := firstRound
	for round := byte(2); round <= tournamentRoundCount; round++ {
		nextRound := make([]tournamentTeamSnapshot, len(previous)/2)
		for match := 0; match < len(nextRound); match++ {
			left := previous[match*2]
			right := previous[match*2+1]
			var winner tournamentTeamSnapshot
			switch {
			case left.IsPlayer:
				winner = left
			case right.IsPlayer:
				winner = right
			default:
				picked, pickErr := tournamentPickNpcWinner(left, right, next)
				if pickErr != nil {
					return nil, pickErr.Error(), false
				}
				winner = picked
			}
			nextRound[match] = newTournamentTeamSnapshot(byte(match), winner.Members, winner.IsPlayer)
		}
		rounds = append(rounds, tournamentRoundSnapshot{Number: round, Teams: nextRound})
		previous = nextRound
	}

	// :490-515：PathActors = 每轮"玩家对面那一队"的成员，长度 4*partyCount，
	// **每条 IsBlocking = true**；第 4 轮 actorType 升格。
	pathActors := make([]tournamentPathActor, 0, tournamentRoundCount*partyCount)
	for _, round := range rounds {
		playerIndex := tournamentFindPlayerIndex(round.Teams)
		if playerIndex < 0 {
			return nil, fmt.Sprintf("round %d does not contain the player team", round.Number), false
		}
		for _, actor := range round.Teams[playerIndex^1].Members {
			actorType := actor.ActorType
			if round.Number == tournamentFinalRound {
				if actorType >= 5 {
					actorType = 8
				} else {
					actorType = 3
				}
			}
			pathActors = append(pathActors, tournamentPathActor{
				Code:       actor.Code,
				Level:      actor.Level,
				Type:       actorType,
				IsBlocking: true,
			})
		}
	}

	return &tournamentDungeonRuntime{
		Definition:        definition,
		Rounds:            rounds,
		PathActors:        pathActors,
		acceptedSequences: make(map[int]struct{}, len(pathActors)),
		currentRound:      1,
	}, "", true
}

// tournamentFindPlayerIndex 对应厂商 TournamentDungeonRuntime.cs:542-551。
func tournamentFindPlayerIndex(teams []tournamentTeamSnapshot) int {
	for index, team := range teams {
		if team.IsPlayer {
			return index
		}
	}
	return -1
}

// tournamentPickNpcWinner 对应厂商 TournamentDungeonRuntime.cs:532-540。
func tournamentPickNpcWinner(left, right tournamentTeamSnapshot, next func(int) int) (tournamentTeamSnapshot, error) {
	if left.TotalStrength == right.TotalStrength {
		pick, err := tournamentNext(next, 2)
		if err != nil {
			return tournamentTeamSnapshot{}, err
		}
		if pick == 0 {
			return left, nil
		}
		return right, nil
	}
	if left.TotalStrength > right.TotalStrength {
		return left, nil
	}
	return right, nil
}

// tournamentNext 对应厂商 TournamentDungeonRuntime.cs:564-573：
// 随机源越界是**错误**（厂商抛 InvalidOperationException），不是夹取。
func tournamentNext(next func(int) int, exclusiveMaximum int) (int, error) {
	if next == nil {
		return 0, fmt.Errorf("tournament random source is missing")
	}
	if exclusiveMaximum <= 0 {
		return 0, fmt.Errorf("tournament random bound %d is not positive", exclusiveMaximum)
	}
	value := next(exclusiveMaximum)
	if value < 0 || value >= exclusiveMaximum {
		return 0, fmt.Errorf("tournament random source returned an out-of-range value")
	}
	return value, nil
}

// tournamentRoundForSequence 报告某个 actor sequence 属于第几轮（1-based，0 = 不在本副本）。
func (r *tournamentDungeonRuntime) tournamentRoundForSequence(sequence int) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstSequence == 0 {
		return 0
	}
	first := int(r.firstSequence)
	for round := 1; round <= tournamentRoundCount; round++ {
		start := first + (round-1)*r.Definition.PartyLimit
		if sequence >= start && sequence < start+r.Definition.PartyLimit {
			return round
		}
	}
	return 0
}

// tryBindFirstActorSequence 对应厂商 TournamentDungeonRuntime.cs:213-232。
func (r *tournamentDungeonRuntime) tryBindFirstActorSequence(firstActorSequence uint16) bool {
	if r == nil || firstActorSequence == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstSequence == 0 {
		finalSequence := int(firstActorSequence) + len(r.PathActors) - 1
		if finalSequence > 0xFFFF {
			return false
		}
		r.firstSequence = firstActorSequence
		return true
	}
	return r.firstSequence == firstActorSequence
}

// canAcceptActorDeath 对应厂商 TournamentDungeonRuntime.cs:234-255。
func (r *tournamentDungeonRuntime) canAcceptActorDeath(sequenceID int) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstSequence == 0 {
		return false
	}
	if _, accepted := r.acceptedSequences[sequenceID]; accepted {
		return true
	}
	if r.terminal != tournamentActive {
		return false
	}
	return r.tournamentRoundForSequenceLocked(sequenceID) == int(r.currentRound)
}

func (r *tournamentDungeonRuntime) tournamentRoundForSequenceLocked(sequence int) int {
	first := int(r.firstSequence)
	for round := 1; round <= tournamentRoundCount; round++ {
		start := first + (round-1)*r.Definition.PartyLimit
		if sequence >= start && sequence < start+r.Definition.PartyLimit {
			return round
		}
	}
	return 0
}

// tryApplyActorDeath 对应厂商 TournamentDungeonRuntime.cs:257-324。
//
// 厂商用 `Guid sourceEventId` 做幂等（同一事件重复上报返回 Duplicate）。本仓的
// 上行 op144 没有可用的 Guid，因此幂等键退化为 **sequence 本身**：同一轮里
// 同一个 sequence 只接受一次，重复上报返回 Duplicate。这是**简化**，不是等价物，
// 已在交付报告里显式标注。
func (r *tournamentDungeonRuntime) tryApplyActorDeath(sequenceID int) tournamentActorDeathTransition {
	if r == nil {
		return tournamentActorDeathTransition{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstSequence == 0 {
		return tournamentActorDeathTransition{}
	}
	if _, accepted := r.acceptedSequences[sequenceID]; accepted {
		return tournamentActorDeathTransition{
			Disposition:  tournamentDeathDuplicate,
			CurrentRound: r.currentRound,
		}
	}
	if r.terminal != tournamentActive {
		return tournamentActorDeathTransition{CurrentRound: r.currentRound}
	}
	if r.tournamentRoundForSequenceLocked(sequenceID) != int(r.currentRound) {
		// 不是本轮的 actor（含已过去轮次）——厂商 :291-292 直接 default(拒绝)。
		return tournamentActorDeathTransition{CurrentRound: r.currentRound}
	}

	r.acceptedSequences[sequenceID] = struct{}{}
	first := int(r.firstSequence) + (int(r.currentRound)-1)*r.Definition.PartyLimit
	for sequence := first; sequence < first+r.Definition.PartyLimit; sequence++ {
		if _, accepted := r.acceptedSequences[sequence]; !accepted {
			return tournamentActorDeathTransition{
				Disposition:  tournamentDeathActorAccepted,
				CurrentRound: r.currentRound,
			}
		}
	}

	completedRound := r.currentRound
	if r.currentRound == tournamentFinalRound {
		r.completedRounds = tournamentFinalRound
		r.terminal = tournamentChampion
		return tournamentActorDeathTransition{
			Disposition:    tournamentDeathTournamentCompleted,
			CompletedRound: completedRound,
			CurrentRound:   r.currentRound,
		}
	}
	r.currentRound++
	return tournamentActorDeathTransition{
		Disposition:    tournamentDeathRoundCompleted,
		CompletedRound: completedRound,
		CurrentRound:   r.currentRound,
	}
}

// tryEliminate 对应厂商 TournamentDungeonRuntime.cs:326-349：
// 玩家死亡 ⇒ CompletedRounds = max(0, currentRound-1)，终局 Eliminated。
func (r *tournamentDungeonRuntime) tryEliminate() (byte, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal == tournamentEliminated {
		return r.completedRounds, false
	}
	if r.terminal != tournamentActive {
		return r.completedRounds, false
	}
	if r.currentRound > 0 {
		r.completedRounds = r.currentRound - 1
	} else {
		r.completedRounds = 0
	}
	r.terminal = tournamentEliminated
	return r.completedRounds, true
}

// snapshot 返回只读快照（包构造器与状态查询用）。
type tournamentRuntimeSnapshot struct {
	CurrentRound    byte
	CompletedRounds byte
	Terminal        tournamentTerminalResult
	FirstSequence   uint16
	Rounds          []tournamentRoundSnapshot
	PathActors      []tournamentPathActor
}

func (r *tournamentDungeonRuntime) snapshot() tournamentRuntimeSnapshot {
	if r == nil {
		return tournamentRuntimeSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rounds := make([]tournamentRoundSnapshot, len(r.Rounds))
	copy(rounds, r.Rounds)
	return tournamentRuntimeSnapshot{
		CurrentRound:    r.currentRound,
		CompletedRounds: r.completedRounds,
		Terminal:        r.terminal,
		FirstSequence:   r.firstSequence,
		Rounds:          rounds,
		PathActors:      append([]tournamentPathActor(nil), r.PathActors...),
	}
}

func (r *tournamentDungeonRuntime) isTournamentTerminated() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal != tournamentActive
}

func (r *tournamentDungeonRuntime) isChampion() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal == tournamentChampion
}

// isEliminated 对应厂商 `TournamentDungeonRuntime.IsEliminated`
// （TerminalResult == Eliminated）。终止态不可回退（`TournamentDungeonRuntime.cs:326-349`）。
func (r *tournamentDungeonRuntime) isEliminated() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal == tournamentEliminated
}

// isActive 对应厂商 `!IsTerminated`（`_terminalResult == Active`）。
// 本仓用来表达"赛制还没落定"：`shouldDeferTournamentClearCommit` 与
// `shouldDeferTournamentRewardAutoSelect` 都要求它是 false。
func (r *tournamentDungeonRuntime) isActive() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal == tournamentActive
}

func (r *tournamentDungeonRuntime) currentRoundNumber() byte {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentRound
}

// boundFirstSequence 返回已经绑定的 firstSequence（0 = 还没绑）。
// 387 的第一段路径 actor 编号（厂商 `TournamentPacketBuilder.cs:9-52` 的 sequence 起点）
// 必须与状态机归轮用的同一个起点，否则客户端上报的 actor id 对不上轮次。
func (r *tournamentDungeonRuntime) boundFirstSequence() uint16 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstSequence
}

// shouldClearDungeon 对应厂商
// `DungeonCombatHandler.ShouldClearDungeon`（DungeonCombatHandler.cs:260-267）：
//
//	return clearConditionMatched || (reachedBossEndpoint && !ignoreDefaultDungeonClear);
//
// 锦标赛在 `TournamentDungeonApplicationService.cs:72` 把
// `run.IgnoreDefaultDungeonClear` 置 true，因此**默认通关被显式关掉**，副本级通关
// 只能由状态机（第 4 轮全灭 + 选奖结束）驱动。
//
// ⚠️ **本批只落判定、未接线**：本函数有逐条对拍用例，`runtimeDungeonState` 上也
// 已按厂商语义写好 `IgnoreDefaultDungeonClear`，但既有通关路径尚未读它。
// 原因见交付报告"未做/未确证"第 3 条。
func shouldClearDungeon(clearConditionMatched, reachedBossEndpoint, ignoreDefaultDungeonClear bool) bool {
	return clearConditionMatched || (reachedBossEndpoint && !ignoreDefaultDungeonClear)
}
