package adventuregroup

import (
	"math"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCapsuleAccrualContinuesPastFirstCompletedCapsule(t *testing.T) {
	config := RuntimeConfig{Capsule: CapsuleConfig{MinimumExperience: 11_691_495, MaximumExperience: 116_914_950, MaximumCount: 10, GrantedExperience: 1_000_000}}
	now := time.Now()
	account := dnfrepo.AccountRecord{Metadata: map[string]string{RuntimeStateMetadataKey: `{"version":1,"growth_experience":116914950}`}}
	state, err := ParseRuntimeState(account, config, now)
	if err != nil {
		t.Fatal(err)
	}
	state.AddMaximumLevelExperience(config, 38_011)
	if state.GrowthExperience != 116_952_961 || CapsuleClaimExperience(config.Capsule, state.GrowthExperience) != 1_000_000 {
		t.Fatalf("首个胶囊完成后停止累计：%+v", state)
	}
	state.GrowthExperience = 234_000_000
	encoded, err := state.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	account.Metadata[RuntimeStateMetadataKey] = encoded
	state, err = ParseRuntimeState(account, config, now)
	if err != nil || state.GrowthExperience != 234_000_000 || CapsuleClaimExperience(config.Capsule, state.GrowthExperience) != 1_000_000 {
		t.Fatalf("重载丢失第二个胶囊进度：%+v %v", state, err)
	}
	state.AddMaximumLevelExperience(config, math.MaxUint32)
	if state.GrowthExperience != 1_169_149_500 || CapsuleClaimExperience(config.Capsule, state.GrowthExperience) != 1_000_000 {
		t.Fatalf("十个胶囊上限错误：%+v", state)
	}
	state.AddMaximumLevelExperience(config, 38_011)
	if state.GrowthExperience != 1_169_149_500 {
		t.Fatal("已满十个胶囊仍超额累计")
	}
	if !state.ConsumeCapsule(config) || state.GrowthExperience != 1_157_458_005 || CapsuleClaimExperience(config.Capsule, state.GrowthExperience) != 1_000_000 {
		t.Fatalf("领取一档后经验或次数错误：%+v", state)
	}
	state.AddMaximumLevelExperience(config, 38_011)
	if state.GrowthExperience != 1_157_496_016 {
		t.Fatal("十个胶囊领取后无法继续累计")
	}
}

func TestCapsuleCapacityAndClaimExperienceHandleBounds(t *testing.T) {
	for _, config := range []CapsuleConfig{{}, {MinimumExperience: 20, MaximumExperience: 10, MaximumCount: 10, GrantedExperience: 1_000_000}} {
		if CapsuleClaimExperience(config, math.MaxUint64) != 0 {
			t.Fatal("无效胶囊配置生成领取经验")
		}
	}
	config := CapsuleConfig{MinimumExperience: 10, MaximumExperience: math.MaxUint64, MaximumCount: math.MaxUint32, GrantedExperience: 1_000_000}
	if CapsuleTotalCapacity(config) != math.MaxUint64 || CapsuleClaimExperience(config, math.MaxUint64) != 1_000_000 {
		t.Fatal("胶囊容量溢出或把领取次数当作经验点数")
	}
	if CapsuleClaimExperience(config, 9) != 0 || CapsuleClaimExperience(config, 10) != 1_000_000 {
		t.Fatal("未到10%领取门槛就显示可领取经验，或恰好达到门槛仍为零")
	}
}
