package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实PVF、内存角色仓储和缓冲连接。直接走生产1458/16/45/39处理器；
// 不启动游戏，不连接玩家数据库，不把缓冲收包当作客户端GUI验收。
func TestRealTagPlayActualEntryAndWholeDailyRun(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未提供真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tagtournament.LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = table, resolver, monsters
			s.tagTournamentCatalog = catalog
			s.initialEquipmentArchive = archive
			// 常见Lv90存档已开左右槽、耳环与光环，不能仅测栏位全零的新角色。
			for _, id := range []string{"29", "60"} {
				ch, _, err := g.Character.Load(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				ch.Stats["ex_equip_slot_stat"], ch.Stats["aura_flag"] = 0x13, 1
				if err = g.Character.Save(context.Background(), ch); err != nil {
					t.Fatal(err)
				}
			}
			// 首次购买后销毁连接挑战，模拟已付费重登只发送16的现场路径。
			if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(mode)}); err != nil || p.tagTournamentBattle == nil {
				t.Fatal("真实PVF购买准备失败", err)
			}
			request := p.tagTournamentBattle.prepared.runtime.Request
			s.retireCurrentTagTournamentBattle(p)
			p.conn.(*bufferConn).write.Reset()
			p.enterSelectDungeonContextSent = true
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil || p.tagTournamentBattle == nil || !p.tagTournamentBattle.prepared.entered {
				t.Fatal("真实PVF已付费重入没有恢复完整挑战", err)
			}
			tagPlayAdvanceClock(t, s, p)
			for round := range p.tagTournamentBattle.plan.Encounters {
				tagPlayMoveNext(t, s, p)
				if round == 0 {
					if mode == tagtournament.Normal {
						if err := s.handleCurrentTagTournamentDeath(p, []byte{0}); err != nil {
							t.Fatal(err)
						}
					}
					if mode == tagtournament.Hard {
						if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
							t.Fatal(err)
						}
					}
					if p.tagTournamentBattle.run.Snapshot().Active != 1 {
						t.Fatal("存活备用角色没有出战")
					}
				}
				if mode == tagtournament.Normal && round == 3 {
					b := p.tagTournamentBattle
					if err := b.run.ApplyVitals(b.now(), 1, 333, 44); err != nil {
						t.Fatal(err)
					}
				}
				if mode == tagtournament.Normal && round == 4 {
					snapshot := p.tagTournamentBattle.run.Snapshot()
					if snapshot.Vitals[0].HP != 0 || snapshot.Vitals[1].HP != snapshot.Vitals[1].MaxHP || snapshot.Vitals[1].MP != snapshot.Vitals[1].MaxMP || snapshot.Active != 1 {
						t.Fatal("真实王房恢复复活了死者或没有保持备用队员", snapshot)
					}
				}
				tagPlayKillBoss(t, s, p)
			}
			if p.tagTournamentBattle.run.Snapshot().Phase != tagtournament.Finished {
				t.Fatal("真实地图整场没有完成")
			}
			count, totals := tagBattleMailTotals(t, g)
			if mode == tagtournament.Normal && count != 5 || mode == tagtournament.Hard && count != 7 {
				t.Fatal("真实配置邮件数错误", count, totals)
			}
			t.Logf("真实PVF模式%d完成%d回合，邮件%d封，附件%v", mode, len(p.tagTournamentBattle.plan.Encounters), count, totals)
		})
	}
}
