package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
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

func TestBattlePassCompactRequestBounds(t *testing.T) {
	b := make([]byte, 64)
	copy(b, "BPQ3")
	binary.LittleEndian.PutUint32(b[4:], 1)
	binary.LittleEndian.PutUint32(b[8:], 2)
	b[32] = 42
	r, ok := decodeBattlePassGame(b)
	if !ok || !r.compact || r.catalogDigest[0] != 42 {
		t.Fatal("新请求或摘要解析错误")
	}
	for _, n := range []int{0, 4, 31, 32, 33, 63, 65} {
		x := make([]byte, n)
		copy(x, b)
		if _, ok = decodeBattlePassGame(x); ok {
			t.Fatal("错误长度被接受", n)
		}
	}
	b[12] = 4
	b[13] = 0
	b[14] = 0
	binary.LittleEndian.PutUint32(b[16:], 20261029)
	binary.LittleEndian.PutUint32(b[20:], 20755)
	binary.LittleEndian.PutUint32(b[24:], bp.PremiumPrice)
	binary.LittleEndian.PutUint32(b[28:], 30|(40<<14))
	r, ok = decodeBattlePassGame(b)
	if !ok || r.quoteHappy != 30 || r.quoteToken != 40 {
		t.Fatal("购买报价丢失")
	}
	b[15] = 1
	if _, ok = decodeBattlePassGame(b); ok {
		t.Fatal("保留字段未拒绝")
	}
}

func TestBattlePassCompactRealPVF(t *testing.T) {
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
	digest := c.WireDigest()
	old := c.Missions[16]
	changed := old
	changed.Text += "变更"
	c.Missions[16] = changed
	if c.WireDigest() == digest {
		t.Fatal("名称变化未改变摘要")
	}
	c.Missions[16] = old
	if old.Text != "通关悲叹之塔" || c.Missions[8].Text != "通关绝望之塔任意一层" {
		t.Fatal("未使用已安装90任务PVF")
	}
	service := &Service{battlePassCatalog: c, battlePassFingerprint: "只读协议校验"}
	_, _, e = service.settleBattlePassGame(context.Background(), nil, localPlayerIdentity{}, boundGameSessionCharacter{}, gameBattlePassRequest{compact: true})
	if !errors.Is(e, errBattlePassCatalogMismatch) {
		t.Fatal("配置不一致未在玩家/资产操作前拒绝", e)
	}
	g := memory.NewMemoryGroup()
	account := repo.AccountRecord{AccountID: "a"}
	cerashop.SetBalance(&account, 19900)
	cerashop.SetTokenCeraBalance(&account, 4000)
	cerashop.SetHappyTokenCeraBalance(&account, 3000)
	if e = g.Account.Save(nil, account); e != nil {
		t.Fatal(e)
	}
	if e = g.Character.Save(nil, repo.CharacterRecord{CharacterID: "1", AccountID: "a"}); e != nil {
		t.Fatal(e)
	}
	start := time.Date(2026, 10, 29, 6, 0, 0, 0, time.FixedZone("China", 28800))
	policy := bp.Policy{ID: 20261029, Start: start, End: start.AddDate(0, 0, 100)}
	v, e := bp.Execute(context.Background(), g, c, policy, bp.Command{AccountID: "a", CharacterID: "1", Now: start.Add(time.Hour), Fingerprint: "配置协议隔离"})
	if e != nil {
		t.Fatal(e)
	}
	v.State.Level = 32
	v.State.XP = 300
	v.State.Premium = true
	v.State.RewardClaimed[0][0] = 2
	v.State.RewardClaimed[1][0] = 1
	v.Missions[3].Flags &^= 4
	v.Missions[3].GrantedXP *= c.PremiumRatio
	v.Missions[0].Flags = 1
	v.Missions[0].Progress = v.Missions[0].Target
	full := encodeBattlePassSnapshot(c, v)
	compact := encodeBattlePassCompactSnapshot(c, v)
	if len(compact) != 360 || string(compact[:4]) != "BPA6" || !bytes.Equal(full[4:96], compact[4:96]) || !bytes.Equal(compact[96:128], digest[:]) {
		t.Fatal("数值头或配置摘要不符")
	}
	for i, m := range v.Missions {
		q := compact[128+i*8:]
		if binary.LittleEndian.Uint16(q) != uint16(m.ID) || binary.LittleEndian.Uint16(q[2:]) != m.Flags || binary.LittleEndian.Uint32(q[4:]) != m.Progress {
			t.Fatal("任务数值丢失", i)
		}
	}
	for track := 0; track < 2; track++ {
		if !bytes.Equal(compact[336+track*10:346+track*10], v.State.RewardClaimed[track][:]) {
			t.Fatal("领取位丢失")
		}
	}
	if out := os.Getenv("BATTLEPASS_COMPACT_FIXTURE"); out != "" {
		if e = os.MkdirAll(filepath.Dir(out), 0755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(out, compact, 0644); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(out+".full", full, 0644); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("真实PVF静态配置摘要=%x；状态10240→360字节；未操作实际玩家数据库", digest)
}
