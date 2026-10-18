package quest

import (
	"errors"
	"fmt"
	"testing"
)

func TestRealPVFAllGuardianShieldQuestsRespectProfessionAndAwakening(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)
	shieldBranches := map[int64]int{12758: 1, 12782: 2, 12735: 1, 12786: 2, 2631: 1, 2632: 2, 12746: 1, 12791: 2}
	for id, branch := range shieldBranches {
		definition, found := catalog.Find(id)
		if !found || len(definition.TargetCharacterRules) == 0 {
			t.Fatalf("缺少真实盾牌任务%d的职业限制", id)
		}
		for _, rule := range definition.TargetCharacterRules {
			if rule.JobTag != "[knight]" || rule.FirstGrowType != branch {
				t.Fatalf("盾牌任务%d的真实职业限制已变更：%+v", id, rule)
			}
		}
		for _, grow := range []int{0, 1, 2, 3, 4, 0x11, 0x12, 0x13, 0x14, 0x21, 0x22, 0x23, 0x24} {
			t.Run(fmt.Sprintf("quest_%d_grow_%02x", id, grow), func(t *testing.T) {
				character := CharacterEligibility{Level: 90, Job: 12, GrowType: grow}
				want := grow&0xf == branch
				if id == 12735 || id == 12786 {
					want = want && grow>>4 >= 1
				}
				if id == 2631 || id == 2632 {
					want = want && grow>>4 == 2
				}
				record := level15BranchRecord(12764)
				listed := level15BranchListed(catalog, 90, 12, grow, record)
				decision, err := catalog.DecideAccept(character, record, id)
				if listed[id] != want || (want && (err != nil || decision != AcceptNew)) ||
					(!want && (!errors.Is(err, ErrQuestNotAcceptable) || decision != AcceptRejected)) {
					t.Fatalf("盾牌任务%d，grow=%02x：列表=%t 接取=%v err=%v，期望允许=%t", id, grow, listed[id], decision, err, want)
				}
			})
		}
	}
}
