package raid

import "fmt"

// NOTI601's actual reader consumes these ten bytes before any result UI work.
// Only the observed success/failure business types are exposed by this builder.
func BuildResultBody(failed bool, phase byte, clearSeconds uint32, deaths uint16, clientRank byte, rewardOption byte) ([]byte, error) {
	if phase > 1 || clientRank > 5 || rewardOption > 1 {
		return nil, fmt.Errorf("raid result field outside configured/native range")
	}
	var w packetWriter
	result := byte(0)
	if failed {
		result = 1
	}
	w.writeByte(result)
	w.writeByte(phase)
	w.writeUint32(clearSeconds)
	w.writeUint16(deaths)
	w.writeByte(clientRank)
	w.writeByte(rewardOption)
	return w.bytes(), nil
}

// The reference maps configured rank0..3 to client rank2..5. Keep that
// conversion explicit; a raw wire rank is not the reward-pool rank.
func BuildAntonResultBody(failed bool, phase byte, clearSeconds uint32, deaths uint16, domainRank byte, rewardOption byte) ([]byte, error) {
	if domainRank > 3 {
		return nil, fmt.Errorf("Anton domain result rank invalid")
	}
	return BuildResultBody(failed, phase, clearSeconds, deaths, domainRank+2, rewardOption)
}

type RewardDisplayItem struct {
	ObjectKind byte
	ItemID     uint32
	Quantity   uint16
	Flags      byte
}
type RewardDisplayCard struct {
	CharacterID uint16
	CardType    byte
	Items       []RewardDisplayItem
}

// Current NOTI600 -> 0202F9E0 is nested: each player's card carries a count of
// item descriptors. The old reference's flat 10-byte card record is incompatible.
// An empty Items slice encodes a card with no item descriptors. This body is
// only a UI projection; it does not grant inventory.
func BuildRewardListBody(kind byte, cards []RewardDisplayCard) ([]byte, error) {
	if kind > 3 || len(cards) > MaxAttackPartyMembers {
		return nil, fmt.Errorf("raid reward list exceeds native raid limits")
	}
	var w packetWriter
	w.writeByte(kind)
	w.writeByte(byte(len(cards)))
	seen := map[uint16]bool{}
	for _, card := range cards {
		if card.CharacterID == 0 || seen[card.CharacterID] || len(card.Items) > 255 {
			return nil, fmt.Errorf("raid reward card owner/count invalid")
		}
		seen[card.CharacterID] = true
		w.writeUint16(card.CharacterID)
		w.writeByte(card.CardType)
		w.writeByte(byte(len(card.Items)))
		for _, item := range card.Items {
			if item.ItemID == 0 || item.Quantity == 0 || item.Flags > 1 {
				return nil, fmt.Errorf("raid reward display item unavailable")
			}
			w.writeByte(item.ObjectKind)
			w.writeUint32(item.ItemID)
			w.writeUint16(item.Quantity)
			w.writeByte(item.Flags)
		}
	}
	return w.bytes(), nil
}
