package dnfbridge

import (
	"context"
	"fmt"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	quest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFPVPTrainingInitialGoals(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要指定本机真实PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	source := questListTestSource{}
	list := ""
	for i := 1; i <= 5; i++ {
		p := fmt.Sprintf("pvp/pvp_tookuk_%02d.qst", i)
		list += fmt.Sprintf("%d `%s`\n", 2669+i, p)
		source["n_quest/"+p], err = archive.ReadText("n_quest/" + p)
		if err != nil {
			t.Fatal(err)
		}
	}
	source[quest.DefaultList] = list
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]uint32{2672: 0x40201, 2673: 1, 2674: 0x40201} {
		states := map[int64]repo.QuestState{}
		for prev := int64(2670); prev < id; prev++ {
			states[prev] = repo.QuestState{Status: "completed"}
		}
		plan, err := catalog.PlanAccept(quest.CharacterEligibility{Level: 90, Job: 14, GrowType: 36}, repo.QuestRecord{States: states}, id)
		if err != nil || plan.InitTrigger != want {
			t.Fatalf("真实PVF训练%d：初始值%d，期望%d，错误%v", id, plan.InitTrigger, want, err)
		}
	}
}
