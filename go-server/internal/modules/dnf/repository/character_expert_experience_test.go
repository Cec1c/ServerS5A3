package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type expertExperienceFallbackFixture struct {
	CharacterRepository
	record CharacterRecord
	saves  int
	err    error
}

func (f *expertExperienceFallbackFixture) Load(context.Context, string) (CharacterRecord, bool, error) {
	return CloneCharacter(f.record), f.record.CharacterID != "", f.err
}
func (f *expertExperienceFallbackFixture) Save(_ context.Context, record CharacterRecord) error {
	f.record = CloneCharacter(record)
	f.saves++
	return f.err
}

func TestCharacterExpertExperienceFallbackKeepsOtherStats(t *testing.T) {
	f := &expertExperienceFallbackFixture{record: CharacterRecord{CharacterID: "19", Stats: map[string]int64{"expert_job_exp": 20, "gold": 500, "grow_type": 2}}}
	stale := CharacterRecord{CharacterID: "19", Stats: map[string]int64{"expert_job_exp": 23, "gold": 0, "grow_type": 0}}
	if err := SaveCharacterExpertExperience(context.Background(), f, stale); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.record.Stats, map[string]int64{"expert_job_exp": 23, "gold": 500, "grow_type": 2}) || f.saves != 1 || stale.Stats["gold"] != 0 {
		t.Fatal("经验兼容保存改变其他属性或输入", f.record)
	}
	for _, stats := range []map[string]int64{nil, {"expert_job_exp": -1}} {
		if err := SaveCharacterExpertExperience(context.Background(), f, CharacterRecord{CharacterID: "19", Stats: stats}); !errors.Is(err, ErrCharacterExpertExperience) {
			t.Fatal("无效经验未拒绝", err)
		}
	}
	want := errors.New("读取失败")
	f.err = want
	if err := SaveCharacterExpertExperience(context.Background(), f, stale); !errors.Is(err, want) || f.saves != 1 {
		t.Fatal("失败被吞掉或继续写入", err)
	}
}
