package raid

import "testing"

func TestFiendWarRefundHasSeparateAuthorityAndReceipt(t *testing.T) {
	_, owner, cmd, calls := rewardOwnerFixture(t)
	cmd.Mode, cmd.Kind, cmd.MinimumClears, cmd.OwnedClears = 3, 4, 0, 0
	if _, err := owner.Grant(nil, cmd); err != ErrRewardReceipt {
		t.Fatal("返票不能走通关领取入口", err)
	}
	if result, err := owner.RefundFiendWarEntry(nil, cmd); err != nil || result.Replayed {
		t.Fatal(result, err)
	}
	if result, err := owner.RefundFiendWarEntry(nil, cmd); err != nil || !result.Replayed || calls.Load() != 1 {
		t.Fatal("失败回调重复返票", result, err, calls.Load())
	}
	bad := cmd
	bad.Mode = 1
	if _, err := owner.RefundFiendWarEntry(nil, bad); err != ErrRewardReceipt {
		t.Fatal("返票影响了安徒恩", err)
	}
	bad = cmd
	bad.OwnedClears = 1
	if _, err := owner.RefundFiendWarEntry(nil, bad); err != ErrRewardReceipt {
		t.Fatal("混用了通关资格", err)
	}
}
