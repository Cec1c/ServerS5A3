package dnfbridge

import "testing"

func TestGameSessionAccountOverridesInstanceFallback(t *testing.T) {
	service := &Service{}
	service.options.accountID = "fallback-account"
	session := &gameSession{accountID: "session-account"}
	if got := service.accountIDForSession(session); got != "session-account" {
		t.Fatalf("session account=%q, want session-account", got)
	}
	if got := service.accountIDForSession(nil); got != "fallback-account" {
		t.Fatalf("fallback account=%q, want fallback-account", got)
	}
}

func TestClientAccountRegistryBindsDistinctPIDs(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	if err := service.RegisterClientAccount(101, "account-a"); err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterClientAccount(202, "account-b"); err != nil {
		t.Fatal(err)
	}
	if got, ok := service.registeredClientAccount(101); !ok || got != "account-a" {
		t.Fatalf("PID 101 account=%q found=%t", got, ok)
	}
	if got, ok := service.registeredClientAccount(202); !ok || got != "account-b" {
		t.Fatalf("PID 202 account=%q found=%t", got, ok)
	}
}

func TestClientAccountRegistryOwnsSpendTimeDescriptorPerPIDAndRegistrationResetsIt(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	if err := service.RegisterClientAccount(101, "account-a"); err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterClientAccount(101, "account-a"); err != nil {
		t.Fatal(err)
	}
	first := &gameSession{clientPID: 101}
	second := &gameSession{clientPID: 101}
	if send, ready := service.beginCurrentSpendTimeEventInfo(first); !send || !ready {
		t.Fatalf("first reservation send=%t ready=%t", send, ready)
	}
	if send, ready := service.beginCurrentSpendTimeEventInfo(second); send || ready {
		t.Fatalf("concurrent reservation send=%t ready=%t", send, ready)
	}
	service.finishCurrentSpendTimeEventInfo(first, true)
	if send, ready := service.beginCurrentSpendTimeEventInfo(second); send || !ready || !second.spendTime.eventInfoSent {
		t.Fatalf("same PID after send=%t ready=%t session_bit=%t", send, ready, second.spendTime.eventInfoSent)
	}
	if err := service.RegisterClientAccount(101, "account-a"); err != nil {
		t.Fatal(err)
	}
	third := &gameSession{clientPID: 101}
	if send, ready := service.beginCurrentSpendTimeEventInfo(third); !send || !ready {
		t.Fatalf("new process lifecycle did not reset descriptor send=%t ready=%t", send, ready)
	}
	service.finishCurrentSpendTimeEventInfo(third, false)
	if send, ready := service.beginCurrentSpendTimeEventInfo(third); !send || !ready {
		t.Fatalf("failed write did not release descriptor reservation send=%t ready=%t", send, ready)
	}
}

// TestClientAccountRegistryPIDZeroSendsDescriptorOncePerSession 是 2026-09-26
// 弹窗修复的聚焦用例：当前 90CN 部署拿不到客户端 PID（client_pid=0），旧写法
// 把所有会话都判成"已消费 op108"，活动目录从此只发过零次，客户端活动表为空 →
// 进城开出未初始化的韩文 BLUE/RED 竞技窗。修复后 PID=0 按**会话**去重：
// 同一会话只发一次，新会话重新发一次（与官方每会话一个 op108 一致）。
func TestClientAccountRegistryPIDZeroSendsDescriptorOncePerSession(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	first := &gameSession{}
	if send, ready := service.beginCurrentSpendTimeEventInfo(first); !send || !ready {
		t.Fatalf("PID zero first session descriptor send=%t ready=%t", send, ready)
	}
	service.finishCurrentSpendTimeEventInfo(first, true)
	if send, ready := service.beginCurrentSpendTimeEventInfo(first); send || !ready {
		t.Fatalf("PID zero same session must not resend send=%t ready=%t", send, ready)
	}
	if !first.spendTime.eventInfoSent {
		t.Fatal("PID zero session descriptor gate was not closed after send")
	}
	second := &gameSession{}
	if send, ready := service.beginCurrentSpendTimeEventInfo(second); !send || !ready {
		t.Fatalf("PID zero new session must get its own descriptor send=%t ready=%t", send, ready)
	}
}

// TestClientAccountRegistryPIDZeroReleasesFailedDescriptor 钉住失败重试：写失败
// 时 finish(sent=false) 不置会话位，下一次调用仍可再试一次。
func TestClientAccountRegistryPIDZeroReleasesFailedDescriptor(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	session := &gameSession{}
	if send, ready := service.beginCurrentSpendTimeEventInfo(session); !send || !ready {
		t.Fatalf("PID zero descriptor send=%t ready=%t", send, ready)
	}
	service.finishCurrentSpendTimeEventInfo(session, false)
	if send, ready := service.beginCurrentSpendTimeEventInfo(session); !send || !ready {
		t.Fatalf("failed write did not release PID zero descriptor send=%t ready=%t", send, ready)
	}
}

func TestClientAccountRegistryRecreatedForRunningPIDUsesProgressOnly(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	session := &gameSession{clientPID: 101}
	if send, ready := service.beginCurrentSpendTimeEventInfo(session); send || !ready {
		t.Fatalf("unknown running PID descriptor send=%t ready=%t", send, ready)
	}
	if !session.spendTime.eventInfoSent {
		t.Fatal("progress-only recovery did not close the session descriptor gate")
	}
}
