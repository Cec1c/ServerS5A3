package dnfbridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-11-03 90CN：PVF 的 etc/pcroomtimepoint.etc 每日奖励写成 5 档后，
// 每一次“选角 → 城镇”都断线，日志只剩脱敏摘要，原文为
//
//	initialize selected town actor before transition: load current
//	spend-time catalog: pcroom [daily reward items] has invalid shape
//
// 目录损坏只能让在线活动不可用，登录和选角页流程必须继续，并留下可 grep 的
// 降级事件；同时不能把缓存的错误吞掉或改用 2347 伪造奖励。
func TestInvalidSpendTimeCatalogNeverBreaksLoginOrSelector(t *testing.T) {
	cause := errors.New("pcroom [daily reward items] has invalid shape")
	logPath := filepath.Join(t.TempDir(), "packet.log")
	plog, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatalf("open packet logger: %v", err)
	}
	t.Cleanup(func() { _ = plog.Close() })
	s := &Service{packetLog: plog, spendTimeCatalogLoadErr: cause}
	session := &gameSession{conn: &bufferConn{}, connID: "pcroom-invalid", selectedCharacterID: 1}

	if err := s.sendCurrentSpendTimeInitialStateOnce(session, "test_initial_town"); err != nil {
		t.Fatalf("initial town spend-time err=%v, want nil (invalid catalog must not break login)", err)
	}
	if err := s.sendCurrentPortedSelectorCatalog(session); err != nil {
		t.Fatalf("selector catalog err=%v, want nil (invalid catalog must not break selector)", err)
	}
	if session.spendTime.catalog != nil || !session.spendTime.anchor.IsZero() {
		t.Fatalf("spend-time clock started from an invalid catalog: %+v", session.spendTime.catalog)
	}
	if _, _, err := s.currentSpendTimeRuntimeCatalogIfAvailable(nil); !errors.Is(err, cause) {
		t.Fatalf("cached catalog error was masked: %v", err)
	}

	_ = plog.Close()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read packet log: %v", err)
	}
	out := string(raw)
	for _, want := range []string{
		"game-spend-time-initialization-deferred",
		"online_activity_unavailable_login_continues",
		"game-ported-selector-catalog-deferred",
		"has invalid shape",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("packet log missing %q:\n%s", want, out)
		}
	}
}
