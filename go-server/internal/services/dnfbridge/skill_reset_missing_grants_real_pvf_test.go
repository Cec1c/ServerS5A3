package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	profession "longheng.io/server/internal/modules/dnf/profession"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// P67 补充验证（cross-review41 / G4 覆盖需求）：真实固定 QA PVF 里 job2 的授予包含
// catalog 中不存在的技能（本用例现场算出来，不硬编码、不伪造数据）。ResetGrants 必须
// fail-closed 拒绝这种分支，而不是部分授予或降级；同时 job0/剑魂 的正常路径必须成功。
//
// packed grow_type 的位序照真档 Decode：低半字节 = first grow type（分支），
// 高半字节 = awakening stage ⇒ packed = stage<<4 | branch。
//
// 依赖候选新增的 profession.(*Profiles).ResetGrants，因此本文件只在候选源码上编译
// （用 go -overlay 挂载，不改被审 lane 的任何文件）。
func TestRealPVFSkillResetRejectsMissingProfessionGrants(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run this real-PVF regression")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profession.LoadProfiles(ctx, archive, catalog)
	if err != nil {
		t.Fatal(err)
	}

	// 现场找出 job2 里授予存在、catalog 不存在的真实缺失项（按真实 packed 编码）。
	missingByPacked := map[byte][]uint16{}
	for branch := byte(0); branch <= 5; branch++ {
		for stage := byte(0); stage <= 2; stage++ {
			grants, ok := profiles.FreeGrants(2, branch, stage)
			if !ok {
				continue
			}
			missing := []uint16{}
			for _, g := range grants {
				if _, found := catalog.Find(2, g.SkillID); !found {
					missing = append(missing, g.SkillID)
				}
			}
			if len(missing) > 0 {
				missingByPacked[stage<<4|branch] = missing
			}
		}
	}
	if len(missingByPacked) == 0 {
		t.Fatal("expected real missing grants for job 2 (probe drift?)")
	}
	for packed, missing := range missingByPacked {
		_, err := profiles.ResetGrants(catalog, 2, packed, 90)
		if !errors.Is(err, profession.ErrSkillMissing) {
			t.Errorf("job2 packed=0x%02x missing=%v: ResetGrants err=%v, want ErrSkillMissing (must refuse, never partially grant)",
				packed, missing, err)
		}
	}
	t.Logf("job2 real missing-grant packed values=%d (all refused as ErrSkillMissing)", len(missingByPacked))

	// 反证：正常路径必须仍成功，并按真档重算自动技能等级（86=9、174=角色等级、87 不授予）。
	grants, err := profiles.ResetGrants(catalog, 0, 0x11, 90)
	if err != nil {
		t.Fatalf("job0 branch1 stage1 reset refused: %v", err)
	}
	levels := map[uint16]int{}
	for _, g := range grants {
		levels[g.SkillID] = g.Level
	}
	if levels[86] != 9 {
		t.Errorf("job0 branch1 reset 86=%d want 9 (grants=%v)", levels[86], grants)
	}
	if levels[174] != 90 {
		t.Errorf("job0 branch1 reset 174=%d want 90 (character level)", levels[174])
	}
	if lvl, present := levels[87]; present {
		t.Errorf("job0 branch1 reset granted another branch awakening 87 level=%d", lvl)
	}
}
