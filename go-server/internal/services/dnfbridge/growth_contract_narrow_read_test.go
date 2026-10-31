package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type growthContractFullReadProbe struct {
	r.AccountRepository
	record r.AccountRecord
	loads  int
}

func (p *growthContractFullReadProbe) Load(context.Context, string) (r.AccountRecord, bool, error) {
	p.loads++
	return p.record, true, nil
}

type growthContractNarrowReadProbe struct {
	*growthContractFullReadProbe
	value        string
	found        bool
	err          error
	calls        int
	account, key string
}

func (p *growthContractNarrowReadProbe) LoadMetadataEntry(ctx context.Context, account, key string) (string, bool, error) {
	p.calls++
	p.account, p.key = account, key
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return p.value, p.found, p.err
}

func growthContractProbeService(accounts r.AccountRepository) *Service {
	return &Service{repositoryProvider: func() (r.Group, bool) { return r.Group{Account: accounts}, true },
		premiumCatalog: &currentPremiumCatalog{effectsByType: map[int64]currentPremiumEffectInfo{premium.TypeBonusExp: {BonusExperiencePercent: 20, IndependentDropRatePercents: []int64{20}}}}}
}

func TestGrowthContractNarrowReadKeepsExpiryRenewalAndFallback(t *testing.T) {
	full := &growthContractFullReadProbe{}
	narrow := &growthContractNarrowReadProbe{growthContractFullReadProbe: full, found: true}
	s := growthContractProbeService(narrow)
	for _, raw := range []string{strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10), "0", "bad", "-1", strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10), strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)} {
		narrow.value = raw
		full.record = r.AccountRecord{AccountID: "account-1", Metadata: map[string]string{premium.MetadataKey(premium.TypeBonusExp): raw, "unrelated": "保留无关字段"}}
		fallback := growthContractProbeService(full)
		want := fallback.currentGrowthContractBonusExp(context.Background(), "account-1", 201)
		previousLoads := full.loads
		got := s.currentGrowthContractBonusExp(context.Background(), " account-1 ", 201)
		if got != want || full.loads != previousLoads || narrow.account != "account-1" || narrow.key != premium.MetadataKey(premium.TypeBonusExp) {
			t.Fatalf("窄读取改变奖励、账号范围或仍完整加载：raw=%s got=%d want=%d", raw, got, want)
		}
	}
	if narrow.calls != 6 {
		t.Fatal("契约续费、失效未逐次实时读取", narrow.calls)
	}
	before := narrow.calls
	if s.currentGrowthContractBonusExp(context.Background(), "account-1", 0) != 0 || narrow.calls != before {
		t.Fatal("零基础经验仍查库")
	}
}

func TestGrowthContractNarrowReadFailureDoesNotReloadOrReuseOldEntitlement(t *testing.T) {
	full := &growthContractFullReadProbe{record: r.AccountRecord{AccountID: "a", Metadata: map[string]string{premium.MetadataKey(premium.TypeBonusExp): "4102444800"}}}
	narrow := &growthContractNarrowReadProbe{growthContractFullReadProbe: full, value: "4102444800", found: true}
	s := growthContractProbeService(narrow)
	if s.currentGrowthContractBonusExp(context.Background(), "a", 200) != 40 {
		t.Fatal("有效契约未生效")
	}
	narrow.err = errors.New("读取失败")
	if s.currentGrowthContractBonusExp(context.Background(), "a", 200) != 0 || full.loads != 0 {
		t.Fatal("错误后回放旧资格或重复完整读取")
	}
	narrow.err = nil
	narrow.found = false
	if s.currentGrowthContractBonusExp(context.Background(), "a", 200) != 0 {
		t.Fatal("缺失仍沿用旧资格")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := s.currentGrowthContractEffect(ctx, "a"); ok {
		t.Fatal("取消后仍激活契约")
	}
}
