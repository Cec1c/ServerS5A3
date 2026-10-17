package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

// ── 锦标赛包构造器 ───────────────────────────────────────────────────────────
//
// 逐字节对拍厂商 Network\Builders\Dungeon\TournamentPacketBuilder.cs（146 行）。
// opcode 一律用 dungeon_tournament_config.go 里标注了厂商出处的常量
// （NOTI 387/388/389、CMD 458/459）——**不要**改用 dnfenum 的同名枚举，
// 本仓 0x01Cx 区段整体 −1。

// buildTournamentInfoBody 对应厂商 `BuildTournamentInfo`
// （TournamentPacketBuilder.cs:9-52）：
//
//	uint32  DungeonId
//	byte    Difficulty
//	byte    PartyLimit
//	重复【round】：                    ← 第一段：对阵表
//	    byte  round.Number
//	    byte  round.Teams.Count
//	    重复【team】：
//	        byte    team.Position
//	        重复【member】：
//	            int32   member.Code
//	            uint16  clamp(member.Strength, 0, 65535)
//	重复【round】：                    ← 第二段：路径 actor
//	    byte  round.Number
//	    重复 PartyLimit 次：
//	        uint16  sequence（自 firstMonsterSequence 递增）
//	        int32   actor.Code
//	        byte    actor.Level
//	        byte    actor.Type
func buildTournamentInfoBody(
	runtime *tournamentDungeonRuntime,
	difficulty byte,
	firstMonsterSequence uint16,
) ([]byte, error) {
	if runtime == nil || runtime.Definition == nil {
		return nil, fmt.Errorf("tournament runtime is missing")
	}
	snapshot := runtime.snapshot()
	if len(snapshot.PathActors) != tournamentRoundCount*runtime.Definition.PartyLimit {
		return nil, fmt.Errorf(
			"tournament path actors=%d want=%d",
			len(snapshot.PathActors),
			tournamentRoundCount*runtime.Definition.PartyLimit,
		)
	}
	body := make([]byte, 0, 6+len(snapshot.Rounds)*2+tournamentTeamCount*4)
	body = binary.LittleEndian.AppendUint32(body, uint32(runtime.Definition.DungeonID))
	body = append(body, difficulty, byte(runtime.Definition.PartyLimit))
	for _, round := range snapshot.Rounds {
		body = append(body, round.Number, byte(len(round.Teams)))
		for _, team := range round.Teams {
			body = append(body, team.Position)
			for _, member := range team.Members {
				body = binary.LittleEndian.AppendUint32(body, uint32(int32(member.Code)))
				body = binary.LittleEndian.AppendUint16(body, clampTournamentStrength(member.Strength))
			}
		}
	}
	sequence := firstMonsterSequence
	actorIndex := 0
	for _, round := range snapshot.Rounds {
		body = append(body, round.Number)
		for member := 0; member < runtime.Definition.PartyLimit; member++ {
			actor := snapshot.PathActors[actorIndex]
			actorIndex++
			body = binary.LittleEndian.AppendUint16(body, sequence)
			sequence++
			body = binary.LittleEndian.AppendUint32(body, uint32(int32(actor.Code)))
			body = append(body, actor.Level, actor.Type)
		}
	}
	return body, nil
}

// clampTournamentStrength 对应厂商 TournamentPacketBuilder.cs:28-30 的
// `(ushort)Math.Max(0, Math.Min(ushort.MaxValue, member.Strength))`。
func clampTournamentStrength(value int64) uint16 {
	if value < 0 {
		return 0
	}
	if value > 0xFFFF {
		return 0xFFFF
	}
	return uint16(value)
}

// buildTournamentMapInfoBody 对应厂商 `BuildTournamentMapInfo`
// （TournamentPacketBuilder.cs:54-75）：
//
//	byte   x
//	byte   y
//	uint32 seed
//	byte   0
//	byte   revisit ? 0 : 1
//	若 !revisit：uint32 mapId + byte 0 + byte 0 + byte 0
func buildTournamentMapInfoBody(x, y byte, seed uint32, mapID int64, revisit bool) []byte {
	body := make([]byte, 0, 15)
	body = append(body, x, y)
	body = binary.LittleEndian.AppendUint32(body, seed)
	body = append(body, 0)
	if revisit {
		body = append(body, 0)
		return body
	}
	body = append(body, 1)
	body = binary.LittleEndian.AppendUint32(body, uint32(mapID))
	body = append(body, 0, 0, 0)
	return body
}

// tournamentCardReward 对应厂商 `ClearRewardGenerator.CardReward`
// 在 `BuildTournamentClearReward` 里被读到的三个字段（:96-101）。
type tournamentCardReward struct {
	IsGold     bool
	ItemID     int64
	GoldAmount int64
	StackCount int64
}

// tournamentRewardState 是厂商 `TournamentParticipantRewardState`
// （TournamentParticipantRewardState.cs:20-193）里与包构造相关的部分。
type tournamentRewardState struct {
	PartyCount         byte
	LocalPartySlot     byte
	RewardExperience   uint32
	CompletedRounds    byte
	CompletedAllRounds bool
	// Cards 是 [CardTypeCount][CardsPerType]，即 2×2 四张卡。
	Cards [tournamentCardTypeCount][tournamentCardsPerType]tournamentCardReward
	// Selected 是每类已选中的卡下标（未选 = tournamentUnselectedCard）。
	Selected [tournamentCardTypeCount]byte
	// Set only after this run's requested command-state reply was sent.
	// The current official client initializes its card slots in that handler.
	commandStateReady bool
}

// tournamentUnselectedCard 对应 TournamentParticipantRewardState.cs:25 的
// `Unselected = byte.MaxValue`。
const tournamentUnselectedCard = 0xFF

// newTournamentRewardState 对应厂商 `TournamentParticipantRewardState` 构造
// （TournamentParticipantRewardState.cs:37-71）。要求恰好 4 张卡。
func newTournamentRewardState(
	cards []tournamentCardReward,
	partyCount int,
	localPartySlot int,
	rewardExperience uint32,
	completedRounds byte,
	completedAllRounds bool,
) (tournamentRewardState, error) {
	state := tournamentRewardState{}
	if len(cards) != tournamentCardTypeCount*tournamentCardsPerType {
		return state, fmt.Errorf("tournament reward state requires four cards")
	}
	if partyCount < 1 || partyCount > tournamentPartySlotMax {
		return state, fmt.Errorf("tournament reward party count %d is out of range", partyCount)
	}
	if localPartySlot < 0 || localPartySlot >= partyCount {
		return state, fmt.Errorf("tournament reward local party slot %d is out of range", localPartySlot)
	}
	state.PartyCount = byte(partyCount)
	state.LocalPartySlot = byte(localPartySlot)
	state.RewardExperience = rewardExperience
	if completedRounds > tournamentRoundCount {
		completedRounds = tournamentRoundCount
	}
	state.CompletedRounds = completedRounds
	state.CompletedAllRounds = completedAllRounds
	for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
		for index := 0; index < tournamentCardsPerType; index++ {
			state.Cards[cardType][index] = cards[cardType*tournamentCardsPerType+index]
		}
		state.Selected[cardType] = tournamentUnselectedCard
	}
	return state, nil
}

// cardCount 对应 TournamentParticipantRewardState.cs:62-65：
// 第 1 类恒为 CardsPerType；第 2 类只在"打满 4 轮"时才有。
func (s tournamentRewardState) cardCount(cardType int) byte {
	if cardType < 0 || cardType >= tournamentCardTypeCount {
		return 0
	}
	if cardType == 0 {
		return tournamentCardsPerType
	}
	if s.CompletedAllRounds {
		return tournamentCardsPerType
	}
	return 0
}

func (s tournamentRewardState) isCardTypeEnabled(cardType int) bool {
	return s.cardCount(cardType) > 0
}

func (s tournamentRewardState) isPartySlotPresent(slot int) bool {
	return slot >= 0 && slot < int(s.PartyCount)
}

// isSelectionComplete 对应厂商 `TournamentParticipantRewardState.IsSelectionComplete`
// （TournamentParticipantRewardState.cs:73-85：每个**已启用**卡类都必须选完）。
func (s tournamentRewardState) isSelectionComplete() bool {
	for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
		if !s.isCardTypeEnabled(cardType) {
			continue
		}
		if s.Selected[cardType] == tournamentUnselectedCard {
			return false
		}
	}
	return true
}

// buildTournamentClearRewardBody 对应厂商 `BuildTournamentClearReward`
// （TournamentPacketBuilder.cs:77-105）：
//
//	byte   CompletedRounds
//	uint32 RewardExperience
//	byte   0
//	重复【type = 0..CardTypeCount-1（=2）】：
//	    byte  cardCount
//	    重复 cardCount 次：
//	        int32   reward.IsGold ? 0 : ItemId
//	        int32   reward.IsGold ? GoldAmount : StackCount
//	        uint16  0
func buildTournamentClearRewardBody(state tournamentRewardState) []byte {
	body := make([]byte, 0, 8+tournamentCardTypeCount*(1+tournamentCardsPerType*10))
	body = append(body, state.CompletedRounds)
	body = binary.LittleEndian.AppendUint32(body, state.RewardExperience)
	body = append(body, 0)
	for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
		count := state.cardCount(cardType)
		body = append(body, count)
		for index := 0; index < int(count); index++ {
			reward := state.Cards[cardType][index]
			if reward.IsGold {
				body = binary.LittleEndian.AppendUint32(body, 0)
				body = binary.LittleEndian.AppendUint32(body, uint32(int32(reward.GoldAmount)))
			} else {
				body = binary.LittleEndian.AppendUint32(body, uint32(int32(reward.ItemID)))
				body = binary.LittleEndian.AppendUint32(body, uint32(int32(reward.StackCount)))
			}
			body = binary.LittleEndian.AppendUint16(body, 0)
		}
	}
	return body
}

// buildTournamentRewardSelectStateBody 对应厂商 `BuildTournamentRewardSelectState`
// （TournamentPacketBuilder.cs:107-127）：
//
//	byte 1
//	重复【type = 0..1】×【slot = 0..PartySlotCount-1（=4）】：
//	    byte 可用且该位有队员 ? 1 : 0xFF
func buildTournamentRewardSelectStateBody(state tournamentRewardState) []byte {
	body := make([]byte, 0, 1+tournamentCardTypeCount*tournamentPartySlotMax)
	body = append(body, 1)
	for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
		for slot := 0; slot < tournamentPartySlotMax; slot++ {
			if state.isCardTypeEnabled(cardType) && state.isPartySlotPresent(slot) {
				body = append(body, 1)
				continue
			}
			body = append(body, 0xFF)
		}
	}
	return body
}

// buildTournamentRewardSelectionBody 对应厂商 `BuildTournamentRewardSelection`
// （TournamentPacketBuilder.cs:129-144）：
//
//	byte 1
//	重复【type = 0..1】：
//	    byte cardCount
//	    重复 cardCount 次：byte selection
//
// `selection` = 该卡被本槽位选中时写 LocalPartySlot，否则 0xFF
// （TournamentParticipantRewardState.cs:137-146 GetSelection）。
//
// ⚠️ 本仓的发送门槛：**默认档下只有 solicited（客户端自己发来的 2 字节选奖）才发这条
// 7 字节体**（`sendCurrentTournamentRewardSelectionAckLocked`）。另外两条都是危险档、
// 必须显式写 env 才成立：`DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=select+timer`
// 与 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT=state-request`。
// 未受邀的 458 之后真机客户端异常退出（0xC0000005 / RST，4/4 局实证：2026-09-27 22:23–22:25
// dungeon 123，2026-09-29 06:05Z `conn_id=game-002721` RST 于 +1.265s）；而一条 458 都没发的
// 6 个会话全部存活。取证块见 `dungeon_tournament_config.go` 的 `REWARD_SOLICIT` 段。
//
// 真机 reader `sub_1CF5010`（RVA 0x18F5010）逐字段证明了这个形状：
// 读 N1 → N1 个 1 字节 → `setSlot(row=0)`，再读 N2 → N2 个 1 字节 → `setSlot(row=1)`；
// `setSlot`（sub_1D81E00）在该卡 `slot[0] == -1` 且入参 != 0xFF 时把"拿走它的队伍槽位"
// 写回卡槽并刷新卡面。
func buildTournamentRewardSelectionBody(state tournamentRewardState) []byte {
	body := make([]byte, 0, 1+tournamentCardTypeCount*(1+tournamentCardsPerType))
	body = append(body, 1)
	for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
		count := state.cardCount(cardType)
		body = append(body, count)
		for index := 0; index < int(count); index++ {
			if int(state.Selected[cardType]) == index {
				body = append(body, state.LocalPartySlot)
				continue
			}
			body = append(body, tournamentUnselectedCard)
		}
	}
	return body
}
