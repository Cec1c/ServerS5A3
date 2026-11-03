package dnfbridge

import (
	"sort"

	dnfdisjoint "longheng.io/server/internal/modules/dnf/disjoint"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The disassembly return policy is 30 corresponding pieces per equipment.
// PVF supplies the equipment-to-piece identity; epicpieceshop's 5/10 counts
// describe an unrelated exchange and must not be used here.
const currentDisjointEpicPieceCount = uint32(30)

func (c *pvfDungeonDropCatalog) currentDisjointEpicPieceID(definition dungeonDropItemDefinition, document *dnfpvf.Document) (uint32, error) {
	rarity, _ := document.Int("rarity")
	if rarity != 4 || definition.Kind != dungeonDropItemEquipment || currentDisjointDocumentIsAvatar(document) {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disjointEpicPieces == nil {
		book, err := loadCurrentEpicPieceCatalog(c.source)
		if err != nil {
			return 0, err
		}
		c.disjointEpicPieces = book
	}
	return c.disjointEpicPieces.pieceByOutput[definition.ItemID], nil
}

func creditCurrentDisjointEpicPiece(assets *dnfdisjoint.Assets, pieceID uint32, balances map[uint32]uint32) (uint32, error) {
	if pieceID == 0 {
		return 0, nil
	}
	var before uint32
	if assets != nil && assets.Account != nil {
		before = dnfrepo.ParseAccountEpicPieceBalance(assets.Account.Metadata[dnfrepo.AccountEpicPieceMetadataKey(pieceID)])
	}
	balance, err := dnfdisjoint.CreditEpicPiece(assets, pieceID, currentDisjointEpicPieceCount)
	if err != nil {
		return 0, err
	}
	balances[pieceID] = balance
	return balance - before, nil
}

// These rows describe already committed ledger deltas. They never participate
// in inventory placement or slot refresh. Paired clients recognize FFFF as a
// temporary result-window item; old clients must keep this option disabled.
const currentDisjointEpicDisplayEnv = "DNFBRIDGE_DISJOINT_EPIC_DISPLAY"

func currentDisjointEpicDisplayRow(piece, granted uint32) []currentDisjointRewardSlot {
	if piece == 0 || granted == 0 {
		return nil
	}
	return []currentDisjointRewardSlot{{Slot: -1, ItemID: piece, Granted: granted}}
}

func currentDisjointWithDisplay(rewards, display []currentDisjointRewardSlot) []currentDisjointRewardSlot {
	if envString(currentDisjointEpicDisplayEnv, "") != "paired-v1" || len(display) == 0 || len(rewards)+len(display) > 255 {
		return rewards
	}
	return append(append([]currentDisjointRewardSlot(nil), rewards...), display...)
}

func (s *Service) sendCurrentDisjointEpicPieces(session *gameSession, balances map[uint32]uint32) error {
	ids := make([]uint32, 0, len(balances))
	for id := range balances {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if err := s.sendCurrentEpicPieceBalanceUpdate(session, id, balances[id], "equipment_disjoint_after_commit"); err != nil {
			return err
		}
	}
	return nil
}
