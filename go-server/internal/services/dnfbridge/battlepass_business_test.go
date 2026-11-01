package dnfbridge

import (
	"context"
	"encoding/binary"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
	"longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBattlePassBusinessRequestBounds(t *testing.T) {
	b := make([]byte, 32)
	copy(b, "BPQ2")
	binary.LittleEndian.PutUint32(b[4:], 1)
	binary.LittleEndian.PutUint32(b[8:], 9)
	if _, ok := decodeBattlePassGame(b); !ok {
		t.Fatal("状态请求不被接受")
	}
	for n := 0; n < len(b); n++ {
		if _, ok := decodeBattlePassGame(b[:n]); ok {
			t.Fatal("接受截断", n)
		}
	}
	for _, at := range []int{12, 13, 14, 15, 16, 20, 24, 28} {
		x := append([]byte(nil), b...)
		x[at] = 255
		if _, ok := decodeBattlePassGame(x); ok {
			t.Fatal("接受无效字段", at)
		}
	}
	binary.LittleEndian.PutUint32(b[16:], 20261029)
	binary.LittleEndian.PutUint32(b[20:], bp.DayNumber(time.Date(2026, 10, 29, 7, 0, 0, 0, time.FixedZone("China", 28800))))
	b[12], b[13], b[14] = 1, 2, 17
	binary.LittleEndian.PutUint32(b[24:], 70)
	r, ok := decodeBattlePassGame(b)
	if !ok || r.target != 70 || r.week != r.day-(r.day+3)%7 {
		t.Fatal("赛季槽位或周日界错误")
	}
	b[14] = 18
	if _, ok = decodeBattlePassGame(b); ok {
		t.Fatal("接受第19个赛季任务")
	}
	b[12], b[13], b[14] = 4, 0, 0
	binary.LittleEndian.PutUint32(b[24:], bp.PremiumPrice)
	if r, ok = decodeBattlePassGame(b); !ok || r.action != 4 || r.target != 100 {
		t.Fatal("购买请求错误")
	}
	for _, at := range []int{13, 14, 24} {
		bad := append([]byte(nil), b...)
		bad[at] ^= 1
		if _, ok = decodeBattlePassGame(bad); ok {
			t.Fatal("接受无效购买参数", at)
		}
	}
}
func TestBattlePassBusinessWalletQuote(t *testing.T) {
	b := make([]byte, 32)
	copy(b, "BPQ2")
	binary.LittleEndian.PutUint32(b[4:], 1)
	binary.LittleEndian.PutUint32(b[8:], 1)
	b[12] = 4
	binary.LittleEndian.PutUint32(b[24:], 100)
	binary.LittleEndian.PutUint32(b[28:], 30|(40<<14))
	r, ok := decodeBattlePassGame(b)
	if !ok || r.quoteHappy != 30 || r.quoteToken != 40 {
		t.Fatal("多钱包报价解码不符")
	}
	for _, bad := range []uint32{1 << 28, 101, 80 | (80 << 14)} {
		binary.LittleEndian.PutUint32(b[28:], bad)
		if _, ok := decodeBattlePassGame(b); ok {
			t.Fatal("接受无效扣款分配", bad)
		}
	}
	b[12] = 0
	binary.LittleEndian.PutUint32(b[24:], 0)
	binary.LittleEndian.PutUint32(b[28:], 1)
	if _, ok := decodeBattlePassGame(b); ok {
		t.Fatal("普通查询保留字段未校验")
	}
}
func TestBattlePassBusinessRealPVFAndClientFixture(t *testing.T) {
	path := os.Getenv("BATTLEPASS_REAL_PVF")
	if path == "" {
		t.Skip("需显式提供真实战令PVF")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := bp.Load(a)
	if e != nil {
		t.Fatal(e)
	}
	g := memory.NewMemoryGroup()
	account := repo.AccountRecord{AccountID: "a"}
	cerashop.SetBalance(&account, 19900)
	cerashop.SetTokenCeraBalance(&account, 4000)
	cerashop.SetHappyTokenCeraBalance(&account, 3000)
	_ = g.Account.Save(nil, account)
	_ = g.Character.Save(nil, repo.CharacterRecord{CharacterID: "1", AccountID: "a"})
	start := time.Date(2026, 10, 29, 6, 0, 0, 0, time.FixedZone("China", 28800))
	p := bp.Policy{ID: 20261029, Start: start, End: start.AddDate(0, 0, 100)}
	v, e := bp.Execute(context.Background(), g, c, p, bp.Command{AccountID: "a", CharacterID: "1", Now: start.Add(time.Hour), Fingerprint: "真实PVF协议夹具", Counters: []bp.Counter{{Day: "2026-10-29", Kind: 17, Param: -1, Amount: 3}}})
	if e != nil {
		t.Fatal(e)
	}
	b := encodeBattlePassSnapshot(c, v)
	if len(b) != 10240 || string(b[:4]) != "BPA5" || binary.LittleEndian.Uint32(b[44:]) != 26 || binary.LittleEndian.Uint32(b[48:]) != 80 || binary.LittleEndian.Uint32(b[96+26*144+8:]) == 2680773 || binary.LittleEndian.Uint32(b[60:]) != 100 || binary.LittleEndian.Uint64(b[64:]) != 19900 || binary.LittleEndian.Uint64(b[72:]) != 4000 || binary.LittleEndian.Uint64(b[80:]) != 3000 || binary.LittleEndian.Uint64(b[88:]) != 0 {
		t.Fatal("快照仍使用样例奖励")
	}
	for track := 0; track < 2; track++ {
		for level := 1; level <= 80; level++ {
			q := b[96+26*144+(track*80+level-1)*40:]
			rs := c.Rewards[track][level]
			if int(q[0]) != len(rs) || q[2] != byte(level) || q[3] != byte(track) {
				t.Fatal("奖励格件数或等级轨道错误", track, level)
			}
			for i := 0; i < 4; i++ {
				id, count := uint32(0), uint32(0)
				if i < len(rs) {
					id, count = rs[i].ItemID, rs[i].Count
				}
				if binary.LittleEndian.Uint32(q[8+i*8:]) != id || binary.LittleEndian.Uint32(q[12+i*8:]) != count {
					t.Fatal("完整奖励列表丢失或空项未清零", track, level, i)
				}
			}
		}
	}
	if len(c.Rewards[0][32]) != 2 || len(c.Rewards[1][32]) != 3 || c.Tier[8] != 0 || c.Tier[9] != 1 || c.Tier[80] != 9 {
		t.Fatal("95多项奖励或徽章边界不符")
	}
	for i, m := range v.Missions {
		at := 96 + i*144
		if binary.LittleEndian.Uint16(b[at:]) != uint16(m.ID) || binary.LittleEndian.Uint32(b[at+4:]) != m.Progress {
			t.Fatal("任务编码不符")
		}
	}
	if out := os.Getenv("BATTLEPASS_CLIENT_FIXTURE"); out != "" {
		if e = os.MkdirAll(filepath.Dir(out), 0755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(out, b, 0644); e != nil {
			t.Fatal(e)
		}
	}
}
