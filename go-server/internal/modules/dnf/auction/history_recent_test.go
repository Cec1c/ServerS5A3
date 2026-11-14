package auction

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func recentHistorySlots(count int) map[string]repo.ItemStack {
	slots := make(map[string]repo.ItemStack, count)
	for i := 1; i <= count; i++ {
		itemID := int64(6001)
		if i%7 == 0 {
			itemID++
		}
		recordAuctionSale(slots, Listing{ID: uint64(i), SellerID: "seller", Item: repo.ItemStack{ItemID: itemID}}, "buyer", int64(i%3+1), int64(100+i), 0, time.Unix(1800000000+int64(i/4), 0), false)
	}
	return slots
}

func TestRecentHistoryBoundedSelectionMatchesCompleteOrdering(t *testing.T) {
	slots := recentHistorySlots(1000)
	slots["auction:history:invalid"] = repo.ItemStack{ItemID: 6001, Count: 100, Extra: map[string]string{"sold_at": "999999999999999999"}}
	slots["auction:listing:uncommitted"] = repo.ItemStack{ItemID: 6001, Count: 100}
	for _, limit := range []int{1, 20, 2000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var want []SaleRecord
			for _, row := range auctionSaleRecords(slots) {
				if row.ItemID == 6001 && len(want) < limit {
					want = append(want, row)
				}
			}
			for range 10 {
				if got := recentAuctionSaleRecords(slots, 6001, limit); !reflect.DeepEqual(got, want) {
					t.Fatal("bounded latest selection changed time/ID ordering or accepted invalid records")
				}
			}
		})
	}
	if len(recentAuctionSaleRecords(slots, 6001, 0)) != 0 || len(recentAuctionSaleRecords(slots, 9999, 20)) != 0 {
		t.Fatal("empty selection returned samples")
	}
}

var recentHistoryBenchmarkSink []SaleRecord

func BenchmarkRecentHistorySelection(b *testing.B) {
	slots := recentHistorySlots(10000)
	b.Run("complete-sort", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			recentHistoryBenchmarkSink = auctionSaleRecords(slots)
		}
	})
	b.Run("latest-20", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			recentHistoryBenchmarkSink = recentAuctionSaleRecords(slots, 6001, 20)
		}
	})
}
