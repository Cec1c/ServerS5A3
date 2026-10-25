package dnfbridge

import (
	"encoding/binary"
	"math"

	"longheng.io/server/internal/modules/dnf/guild"
)

const currentGuildRankingOpcode uint16 = 1503

// V166 CMD1503 reads exactly 108 bytes. The first DWORD retains the native
// zero initializer; it is not a guild ID. History has two totals but one set
// of factors, for the immediately preceding calendar month.
func buildCurrentGuildRankingBody(rows [3]guild.RankingRow) ([]byte, error) {
	body := make([]byte, 108)
	for i, row := range rows {
		total, err := row.Factors.Total()
		if err != nil || total != row.Score || row.Rank > math.MaxInt32 {
			return nil, guild.ErrRankingRules
		}
		binary.LittleEndian.PutUint32(body[4+8*i:], row.Rank)
		binary.LittleEndian.PutUint32(body[8+8*i:], math.Float32bits(row.Score))
	}
	for period, base := range []int{28, 52} {
		factors := rows[period].Factors.Values()
		for i := 0; i < 6; i++ {
			binary.LittleEndian.PutUint32(body[base+4*i:], math.Float32bits(factors[i]))
		}
		for i := 6; i < 10; i++ {
			binary.LittleEndian.PutUint32(body[76+16*period+4*(i-6):], math.Float32bits(factors[i]))
		}
	}
	return body, nil
}
