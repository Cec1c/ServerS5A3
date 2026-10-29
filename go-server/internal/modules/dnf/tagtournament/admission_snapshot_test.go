package tagtournament

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAdmissionSnapshotRejectsChangeBeforeAnyFeeOrCredential(t *testing.T) {
	for _, field := range []string{"姓名", "职业", "转职", "金币", "位置", "等级"} {
		t.Run(field, func(t *testing.T) {
			o, g, _ := ownerFixture(t)
			ctx := context.Background()
			var expected [2]repo.CharacterRecord
			for i, id := range []string{"1", "2"} {
				expected[i], _, _ = g.Character.Load(ctx, id)
			}
			changed := repo.CloneCharacter(expected[1])
			switch field {
			case "姓名":
				changed.Name = "改名后"
			case "职业":
				changed.Job = "12"
			case "转职":
				changed.Stats["grow_type"] = 33
			case "金币":
				changed.Stats["gold"]--
			case "位置":
				changed.Slot++
			case "等级":
				changed.Level++
			}
			if err := g.Character.Save(ctx, changed); err != nil {
				t.Fatal(err)
			}
			account, _, _ := g.Account.Load(ctx, "a")
			if _, err := o.AdmitFromSnapshot(ctx, "a", "1", expected, Normal); err == nil {
				t.Fatal("接受过期双角色正文")
			}
			after, _, _ := g.Account.Load(ctx, "a")
			payer, _, _ := g.Character.Load(ctx, "1")
			if !reflect.DeepEqual(account, after) || !reflect.DeepEqual(payer, expected[0]) {
				t.Fatal("校验失败仍扣费或替换凭据")
			}
			fresh, err := o.Lobby(ctx, "a")
			if err != nil {
				t.Fatal(err)
			}
			admission, err := o.AdmitFromSnapshot(ctx, "a", "1", fresh.Characters, Normal)
			if err != nil || admission.Fee() != 120000 {
				t.Fatal("重新准备后不能正常提交", admission.Fee(), err)
			}
		})
	}
}
