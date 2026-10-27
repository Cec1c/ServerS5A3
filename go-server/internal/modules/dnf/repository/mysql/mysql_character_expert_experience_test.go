package mysql

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLExpertExperienceOnlyWritesOwnedExperience(t *testing.T) {
	db := &fakeSQLDB{}
	repos := newTestMySQLGroup(t, db)
	scoped := &characterSettlementScopedCharacterRepository{characterID: "19", base: repos.Character}
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	record := repository.CharacterRecord{CharacterID: "19", AccountID: "account-1", UpdatedAt: now, Stats: map[string]int64{"expert_job_exp": 123, "gold": 0, "grow_type": 0}}
	for i := 0; i < 54; i++ {
		record.Stats[fmt.Sprintf("现有额外属性%d", i)] = int64(i)
	}
	if err := repository.SaveCharacterFields(context.Background(), repos.Character, record, repository.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	baseline := len(db.execs)
	db.execs = nil
	if err := repository.SaveCharacterExpertExperience(context.Background(), scoped, record); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 1 || baseline < 55 {
		t.Fatal("经验仍重写整份属性", baseline, len(db.execs))
	}
	call := db.execs[0]
	if !strings.HasPrefix(call.Query, "UPDATE ") || !strings.HasSuffix(call.Query, " SET expert_job_exp = ?, updated_at = ? WHERE character_id = ?") || !reflect.DeepEqual(call.Args, []any{int64(123), now, "19"}) {
		t.Fatal("写入范围或绑定参数改变", call)
	}
	t.Logf("54个额外属性夹具：角色属性写语句%d→%d，仅更新经验及时间", baseline, len(db.execs))
	record.CharacterID = "其他角色"
	if err := repository.SaveCharacterExpertExperience(context.Background(), scoped, record); err == nil || len(db.execs) != 1 {
		t.Fatal("跨角色经验更新未拒绝", err)
	}
	record.CharacterID = "19"
	db.execErr = errors.New("数据库失败")
	if err := repository.SaveCharacterExpertExperience(context.Background(), scoped, record); !errors.Is(err, db.execErr) {
		t.Fatal("经验写入失败被吞掉", err)
	}
}
