package dnfbridge

import (
	cryptorand "crypto/rand"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"math/big"
)

// 255 is the native category marker for the separate (level,percent) vector,
// never a literal reinforcement. Retain order and reject malformed tables.
type currentBoosterUpgradeChance struct{ Level, Percent byte }
type currentBoosterUpgradeTable [32]currentBoosterUpgradeChance

func parseCurrentBoosterUpgradeTable(doc *dnfpvf.Document, section dnfpvf.Section) (currentBoosterUpgradeTable, error) {
	var result currentBoosterUpgradeTable
	values := magicBoxSectionInts(doc, section)
	if len(values) == 0 || len(values)%2 != 0 || len(values) > 64 || len(values) != section.End-section.Start {
		return result, errCurrentBoosterPVFInvalid
	}
	seen := map[int64]bool{}
	total := int64(0)
	for i := range values {
		if doc.Tokens[section.Start+i].Kind != dnfpvf.TokenInt {
			return result, errCurrentBoosterPVFInvalid
		}
	}
	for i := 0; i < len(values); i += 2 {
		level, percent := values[i], values[i+1]
		if level < 0 || level > 31 || percent <= 0 || percent > 100 || seen[level] {
			return result, errCurrentBoosterPVFInvalid
		}
		seen[level] = true
		total += percent
		result[i/2] = currentBoosterUpgradeChance{byte(level), byte(percent)}
	}
	if total != 100 {
		return currentBoosterUpgradeTable{}, errCurrentBoosterPVFInvalid
	}
	return result, nil
}

func currentBoosterUpgradeAt(table currentBoosterUpgradeTable, roll uint32) (byte, error) {
	if roll >= 100 {
		return 0, errCurrentBoosterPVFInvalid
	}
	var total uint32
	for _, pair := range table {
		total += uint32(pair.Percent)
	}
	if total != 100 {
		return 0, errCurrentBoosterPVFInvalid
	}
	for _, pair := range table {
		if roll < uint32(pair.Percent) {
			return pair.Level, nil
		}
		roll -= uint32(pair.Percent)
	}
	return 0, errCurrentBoosterPVFInvalid
}

func newCurrentBoosterRandomUpgrade(table currentBoosterUpgradeTable) (byte, error) {
	roll, err := cryptorand.Int(cryptorand.Reader, big.NewInt(100))
	if err != nil {
		return 0, err
	}
	return currentBoosterUpgradeAt(table, uint32(roll.Uint64()))
}
