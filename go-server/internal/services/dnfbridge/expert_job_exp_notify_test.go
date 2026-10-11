package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 副职业经验下行的聚焦回归（运维第 8 条 B 半）。
//
// 契约与现场证据见 expert_job_exp_notify.go 文件头；这里只钉三件事：
//  1. 开关归一化（默认 = 修复档，只有显式回退字面量才不发）；
//  2. 修复档发出的**那一条**包的形状：class 0 / msg_id=2（= 厂商 NOTI 0x0002
//     USERINFO）、体是 subtype0、尾里 expert_job_type/expert_job_exp 是**新的**值；
//  3. 回退档与"副本内"两条路径一个字节都不发。
//
// 另有一条 real-PVF 冒烟用例把钩子钉在真正的漏斗 `sendCurrentExpertJobInfoForCharacter`
// 上（没有 PVF 时按本仓库既有口径 skip，见 catalog_real_pvf_test.go:12-16）。

// expertJobExpNotifySubtype0BodyOffsets 从 subtype0 体的写出顺序算出两个字段的偏移
// （`userinfo_selected_subtype0.go`）：
//
//	u8 subtype0 | u16 version | u16 charID | u32 nameLen | name
//	| job | grow | level | pvp_grade | pvp_rating_grade | user_state | u8 appearanceCount
//	| tail: nameTagItemID u32 … expert_job_type u8(+41) expert_job_exp u32(+42)
//
// 与 userinfo_legacy_test.go:562-583 的既有断言同源（那里 name="hero" ⇒ tail 起点 20）。
func expertJobExpNotifySubtype0BodyOffsets(name string) (typeOffset int, expOffset int) {
	// Native mode0: route/context, raw47, object key, DSTR name, then
	// the empty equipment/creature/organization tail. 1C05B70 consumes the
	// Latest v56 22BF580 adds organization u16 before the event argument;
	// 22C1650 consumes disguise at tail+59 and 22BF840 profession at +61.
	tail := 5 + 0x47 + 2 + 4 + len(name)
	return tail + 61, tail + 62
}

func TestCurrentExpertJobExpNotifyEnvNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: currentExpertJobExpNotifyOn},
		{value: "typo", want: currentExpertJobExpNotifyOn},
		{value: "on", want: currentExpertJobExpNotifyOn},
		{value: "  ON  ", want: currentExpertJobExpNotifyOn},
		{value: "legacy", want: currentExpertJobExpNotifyLegacy},
		{value: "  LEGACY ", want: currentExpertJobExpNotifyLegacy},
		{value: "off", want: currentExpertJobExpNotifyLegacy},
		{value: "0", want: currentExpertJobExpNotifyLegacy},
		{value: "false", want: currentExpertJobExpNotifyLegacy},
		{value: "no", want: currentExpertJobExpNotifyLegacy},
		{value: "disabled", want: currentExpertJobExpNotifyLegacy},
		{value: "none", want: currentExpertJobExpNotifyLegacy},
	} {
		t.Run("DNFBRIDGE_EXPERT_JOB_EXP_NOTIFY="+test.value, func(t *testing.T) {
			t.Setenv(currentExpertJobExpNotifyEnv, test.value)
			if got := currentExpertJobExpNotifyMode(); got != test.want {
				t.Fatalf("mode=%q want %q", got, test.want)
			}
			if got := currentExpertJobExpNotifyEnabled(); got != (test.want == currentExpertJobExpNotifyOn) {
				t.Fatalf("enabled=%t want %t", got, test.want == currentExpertJobExpNotifyOn)
			}
		})
	}
}

// 修复档：一条 class0 / msg_id=2 的 subtype0，尾里是新的 job_type 与新经验。
func TestCurrentExpertJobExpNotifySendsSubtype0CarryingFreshExperience(t *testing.T) {
	const (
		charID     = 10011
		name       = "hunter"
		jobType    = byte(3) // 分解机
		experience = int64(10)
	)
	t.Setenv(currentExpertJobExpNotifyEnv, "")
	connection := &bufferConn{}
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{characterStats: testCharacterStatTable(t), packetLog: logger}
	t.Cleanup(func() { _ = logger.Close() })
	session := &gameSession{conn: connection, selectedCharacterID: charID}
	character := dnfrepo.CharacterRecord{
		CharacterID: "10011",
		AccountID:   "dnf:10011",
		Name:        name,
		Job:         "11",
		Level:       90,
		Stats: map[string]int64{
			"expert_job_type":              int64(jobType),
			"expert_job_exp":               experience,
			"expert_job_machine_grade":     1,
			"expert_job_machine_endurance": 273,
		},
	}
	service.notifyCurrentExpertJobExperience(session, character, jobType, experience)

	packets := splitAllGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 {
		t.Fatalf("修复档必须只发一条包，实际 %d 条：%x", len(packets), connection.write.Bytes())
	}
	packet := packets[0]
	if packet.Header.Classification != 0 {
		t.Fatalf("classification=%d want 0（厂商 NOTI USERINFO 是 class0）", packet.Header.Classification)
	}
	if packet.Header.MsgID != 2 {
		t.Fatalf("msg_id=%d want 2（ENUM_NOTIPACKET_USERINFO / UpperMsgCharacterRoster）", packet.Header.MsgID)
	}
	body := packet.Body
	if len(body) == 0 || body[0] != 0 {
		t.Fatalf("subtype0 头=%x want 首字节 0", body)
	}
	if got := binary.LittleEndian.Uint16(body[1:3]); got != 1 {
		t.Fatalf("subtype0 version=%d want 1", got)
	}
	if got := binary.LittleEndian.Uint16(body[76:78]); got != charID {
		t.Fatalf("subtype0 charID=%d want %d", got, charID)
	}
	if got := string(body[82 : 82+len(name)]); got != name {
		t.Fatalf("subtype0 name=%q want %q", got, name)
	}
	typeOffset, expOffset := expertJobExpNotifySubtype0BodyOffsets(name)
	if expOffset+4 > len(body) {
		t.Fatalf("subtype0 体过短：%d 字节，exp 偏移 %d", len(body), expOffset)
	}
	if got := body[typeOffset]; got != jobType {
		t.Fatalf("尾 expert_job_type=%d want %d（体偏移 %d）", got, jobType, typeOffset)
	}
	if got := binary.LittleEndian.Uint32(body[expOffset : expOffset+4]); got != uint32(experience) {
		t.Fatalf("尾 expert_job_exp=%d want %d（体偏移 %d）", got, experience, expOffset)
	}

	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, want := range []string{
		"kind=game-current-expert-job-exp-userinfo-send",
		"char_id=10011",
		"job_type=3",
		"experience=10",
		"msg_id=2",
		"classification=0",
		"switch=on",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("判读日志缺 %q：\n%s", want, text)
		}
	}
}

// 回退档：一个字节都不补（= 改动前的下发序列）。
func TestCurrentExpertJobExpNotifyRollbackSendsNothing(t *testing.T) {
	t.Setenv(currentExpertJobExpNotifyEnv, currentExpertJobExpNotifyLegacy)
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection, selectedCharacterID: 10011}
	service.notifyCurrentExpertJobExperience(session, dnfrepo.CharacterRecord{
		CharacterID: "10011",
		Name:        "hunter",
		Stats:       map[string]int64{"expert_job_type": 3, "expert_job_exp": 10},
	}, 3, 10)
	if got := connection.write.Len(); got != 0 {
		t.Fatalf("回退档写了 %d 字节：%x", got, connection.write.Bytes())
	}
}

// 副本内不发（厂商 UserInfoBroadcastService.cs:19 明写「⚠ 副本内禁发 subtype0」）。
func TestCurrentExpertJobExpNotifySkipsInsideDungeon(t *testing.T) {
	t.Setenv(currentExpertJobExpNotifyEnv, currentExpertJobExpNotifyOn)
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection, selectedCharacterID: 10011}
	session.dungeon.runtime = &runtimeDungeonState{}
	service.notifyCurrentExpertJobExperience(session, dnfrepo.CharacterRecord{
		CharacterID: "10011",
		Name:        "hunter",
		Stats:       map[string]int64{"expert_job_type": 3, "expert_job_exp": 10},
	}, 3, 10)
	if got := connection.write.Len(); got != 0 {
		t.Fatalf("副本内写了 %d 字节：%x", got, connection.write.Bytes())
	}
}

// 漏斗接线（real PVF，缺 PVF 时 skip）：`sendCurrentExpertJobInfoForCharacter(force=true)`
// 必须**同时**下发 0x00CD（分解机档只有 grade+endurance，没有经验）与 subtype0（带新经验）。
// 这是"分解完经验没增加"的现场同构复现：修复前只有第一条。
func TestCurrentExpertJobExpNotifyIsWiredIntoTheInfoFunnelRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the expert-job exp notify funnel")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfexpertjob.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	const (
		name       = "hunter"
		experience = int64(10)
	)
	character := dnfrepo.CharacterRecord{
		CharacterID: "10011",
		AccountID:   "dnf:10011",
		Name:        name,
		Job:         "11",
		Level:       90,
		Stats: map[string]int64{
			"expert_job_type":              int64(dnfexpertjob.DisjointerType),
			"expert_job_exp":               experience,
			"expert_job_machine_grade":     1,
			"expert_job_machine_endurance": 273,
		},
	}
	for _, test := range []struct {
		name        string
		env         string
		wantUserInf bool
	}{
		{name: "default_on", env: "", wantUserInf: true},
		{name: "rollback_legacy", env: currentExpertJobExpNotifyLegacy, wantUserInf: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(currentExpertJobExpNotifyEnv, test.env)
			connection := &bufferConn{}
			service := Service{expertJobCatalog: catalog}
			session := &gameSession{conn: connection, selectedCharacterID: 10011}
			if err := service.sendCurrentExpertJobInfoForCharacter(session, character, true); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, connection.write.Bytes())
			var infoPackets, userInfoPackets int
			for _, packet := range packets {
				switch packet.Header.MsgID {
				case currentExpertJobInfoNotification:
					infoPackets++
					// Native 10-byte prefix, followed by the deployed DLL's EJXP/u32 cache footer.
					if len(packet.Body) != 18 || packet.Body[0] != dnfexpertjob.DisjointerType || packet.Body[1] != dnfexpertjob.DisjointerType ||
						string(packet.Body[10:14]) != "EJXP" || binary.LittleEndian.Uint32(packet.Body[14:18]) != uint32(experience) {
						t.Fatalf("0x00CD 体=%x want 00 03 01000000 11010000", packet.Body)
					}
				case 2:
					if packet.Header.Classification != 0 {
						t.Fatalf("subtype0 classification=%d want 0", packet.Header.Classification)
					}
					userInfoPackets++
					typeOffset, expOffset := expertJobExpNotifySubtype0BodyOffsets(name)
					if expOffset+4 > len(packet.Body) || packet.Body[typeOffset] != dnfexpertjob.DisjointerType ||
						binary.LittleEndian.Uint32(packet.Body[expOffset:expOffset+4]) != uint32(experience) {
						t.Fatalf("subtype0 体=%x 没带 type=%d exp=%d", packet.Body, dnfexpertjob.DisjointerType, experience)
					}
				default:
					t.Fatalf("漏斗写了预期之外的包 msg_id=%d body=%x", packet.Header.MsgID, packet.Body)
				}
			}
			if infoPackets != 1 {
				t.Fatalf("0x00CD 条数=%d want 1", infoPackets)
			}
			want := 0
			if test.wantUserInf {
				want = 1
			}
			if userInfoPackets != want {
				t.Fatalf("subtype0 条数=%d want %d（env=%q）", userInfoPackets, want, test.env)
			}
		})
	}
}
