package auction

import (
	"sort"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Keep only the latest limit valid samples for the requested item. Sorting the
// complete escrow history is unnecessary for a twenty-sale weighted average.
func recentAuctionSaleRecords(slots map[string]repo.ItemStack, itemID int64, limit int) []SaleRecord {
	if limit <= 0 {
		return nil
	}
	rows := make([]SaleRecord, 0, limit)
	for key, stack := range slots {
		if stack.ItemID != itemID {
			continue
		}
		row, valid := saleRecordFromStack(key, stack)
		if !valid {
			continue
		}
		index := sort.Search(len(rows), func(i int) bool {
			return row.SoldAt.After(rows[i].SoldAt) || row.SoldAt.Equal(rows[i].SoldAt) && row.ID > rows[i].ID
		})
		if index == limit {
			continue
		}
		if len(rows) < limit {
			rows = append(rows, SaleRecord{})
		}
		copy(rows[index+1:], rows[index:len(rows)-1])
		rows[index] = row
	}
	return rows
}
