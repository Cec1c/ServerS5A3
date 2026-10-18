package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFNPCTransferEpicSnapshotIsScopedToTransferAndKeepsZero(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("设置真实PVF后核对跨界扣除归零和独立刷新范围")
	}
	archive, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	def, err := (currentNPCEquipmentCatalog{items}).Resolve(100050203)
	if err != nil {
		t.Fatal(err)
	}
	if def.Rarity != 4 || def.MinimumLevel != 85 || def.EpicPieceID == 0 {
		t.Fatal("真实85级史诗样本不符", def)
	}
	for _, mode := range []epicPiecePushMode{epicPiecePushLoginOnly, epicPiecePushAlways, epicPiecePushOff, epicPiecePushVendorEmpty} {
		for _, other := range []uint32{0, 17} {
			t.Run(fmt.Sprintf("档位%s_其它碎片%d", mode, other), func(t *testing.T) {
				s, session, conn, g := npcEquipmentFixture(t, items, 100050203, 0, 0)
				s.options.epicPiecePush = mode
				ctx := context.Background()
				account, _, err := g.Account.Load(ctx, "a")
				if err != nil {
					t.Fatal(err)
				}
				account.Metadata[r.AccountEpicPieceMetadataKey(def.EpicPieceID)] = "135"
				account.Metadata[r.AccountEpicPieceMetadataKey(10300001)] = fmt.Sprint(other)
				if err := g.Account.Save(ctx, account); err != nil {
					t.Fatal(err)
				}
				if err := s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2)); err != nil {
					t.Fatal(err)
				}
				account, _, _ = g.Account.Load(ctx, "a")
				inv, _, _ := g.Inventory.Load(ctx, "19")
				mail, _, _ := g.Mailbox.Load(ctx, "42")
				if r.ParseAccountEpicPieceBalance(account.Metadata[r.AccountEpicPieceMetadataKey(def.EpicPieceID)]) != 0 || len(mail.Mails) != 1 || inv.Slots["0:9"].ItemID != 0 {
					t.Fatal("跨界资产未原子提交")
				}
				packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
				if len(packets) < 2 || packets[0].Header.MsgID != 557 || packets[0].Body[0] != 1 {
					t.Fatal("跨界成功回执顺序错误")
				}
				var snapshots int
				for _, packet := range packets {
					if packet.Header.MsgID != 1149 {
						continue
					}
					snapshots++
					body := packet.Body
					wantCount := uint32(0)
					if other > 0 {
						wantCount = 1
					}
					if packet.Header.Classification != 0 || len(body) != 5+int(wantCount)*8 || body[0] != 0 || binary.LittleEndian.Uint32(body[1:]) != wantCount {
						t.Fatalf("跨界未按已提交账本刷新全量并清除归零项：%x", body)
					}
					if other > 0 && (binary.LittleEndian.Uint32(body[5:]) != 10300001 || binary.LittleEndian.Uint32(body[9:]) != other) {
						t.Fatal("跨界刷新改变其它碎片余额")
					}
				}
				want := 1
				if mode == epicPiecePushOff || mode == epicPiecePushVendorEmpty {
					want = 0
				}
				if snapshots != want {
					t.Fatal("跨界刷新未遵守独立范围和显式关闭档", snapshots, want)
				}
				conn.write.Reset()
				if err := s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2)); err != nil {
					t.Fatal(err)
				}
				if conn.write.Len() != 0 {
					t.Fatal("重复跨界仍提交或发送成功刷新")
				}
			})
		}
	}
}
