package dnfbridge

// The seller's 成交 letters. Both used to be one flat sentence that showed only
// the net ("远古遗愿已成交1个，成交款127990金币。" / "您的1百万已成交，代币券已
// 通过邮件送达。"), so the three numbers behind the payout - what the item sold
// for, the commission taken out, and the deposit that came home - were invisible.
//
// The operator's target format is the itemised statement the client shows:
//
//	[已售出 金刚石 1个。]                       - 已成功拍卖[1百万] -
//	出售价 : + 10000 金币                       成交价 : + 188 代币券
//	保证金 : + 10000 金币                       手续费 : - 56 代币券
//	手续费 : - 500 金币                         拍卖[1百万]后， 您获得了132代币券。
//	已售出 金刚石 并获得 19500 金币。
//
// The first line is the letter's title (it is what the mailbox list shows) and
// the rest is the body.
//
// The durability half of this - the breakdown riding the payout row so a
// deferred retry prints the same letter after the listing row is gone - is
// pinned in the domain package, next to the money it walks:
// TestOwnerSalePayoutFreezesTheStatementOnTheOutboxRow.

import (
	"strings"
	"testing"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
)

// The first line is both the subject and the body's first line, because the
// client's detail pane draws the body only - a subject on its own is invisible
// there, which is what the live letter showed - while the mailbox list draws the
// subject. Every expectation below therefore repeats it at the head of the body.

// TestCurrentAuctionSaleStatementMatchesTheTargetLetter reproduces the item
// auction case exactly: 金刚石 x1 at 10000, a 10000 deposit and a 5% commission.
func TestCurrentAuctionSaleStatementMatchesTheTargetLetter(t *testing.T) {
	payout := dnfauction.Payout{
		ItemID: 6001, SoldCount: 1, Gold: 19_500,
		Statement: dnfauction.SaleStatement{
			GrossGold: 10_000, FeeGold: 500, DepositGold: 10_000, NetGold: 19_500,
		},
		HasStatement: true,
	}
	title, body := currentAuctionSaleStatement(payout, "金刚石")
	if title != "[已售出 金刚石 1个。]" {
		t.Fatalf("title=%q", title)
	}
	want := "[已售出 金刚石 1个。]\n出售价 : + 10000 金币\n保证金 : + 10000 金币\n手续费 : - 500 金币\n已售出 金刚石 并获得 19500 金币。"
	if body != want {
		t.Fatalf("body=%q\nwant  %q", body, want)
	}
	if !strings.HasPrefix(body, title+"\n") {
		t.Fatalf("body does not start with the subject line: %q", body)
	}
}

// A partial sale keeps the deposit riding the remaining pieces, and a listing
// registered before deposits or commissions existed has neither, so those lines
// must not print a meaningless "+ 0 金币".
func TestCurrentAuctionSaleStatementOmitsLinesThatDidNotHappen(t *testing.T) {
	partial := dnfauction.Payout{
		ItemID: 6001, SoldCount: 3, Gold: 285,
		Statement:    dnfauction.SaleStatement{GrossGold: 300, FeeGold: 15, DepositGold: 0, NetGold: 285},
		HasStatement: true,
	}
	_, body := currentAuctionSaleStatement(partial, "无色小晶块")
	if want := "[已售出 无色小晶块 3个。]\n出售价 : + 300 金币\n手续费 : - 15 金币\n已售出 无色小晶块 并获得 285 金币。"; body != want {
		t.Fatalf("partial body=%q\nwant  %q", body, want)
	}

	legacy := dnfauction.Payout{
		ItemID: 6001, SoldCount: 1, Gold: 400,
		Statement:    dnfauction.SaleStatement{GrossGold: 400, FeeGold: 0, DepositGold: 0, NetGold: 400},
		HasStatement: true,
	}
	_, body = currentAuctionSaleStatement(legacy, "金刚石")
	if want := "[已售出 金刚石 1个。]\n出售价 : + 400 金币\n已售出 金刚石 并获得 400 金币。"; body != want {
		t.Fatalf("legacy body=%q\nwant  %q", body, want)
	}
}

// TestCurrentGoldConsignSaleStatementMatchesTheTargetLetter reproduces the
// consignment case: 188 点券 with a 30% commission leaves 132 代币券.
