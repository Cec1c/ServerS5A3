package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 本文件是 op447「每角色选项」登录回放的**可选厂商投影**（默认关闭）。
//
// 背景：本仓今天的回放是**原样回放**客户端发来的那一帧
// （`game_option_settings.go` 的 `characterOptionNoticeFrame` →
// `character_option_body_hex`，日志 `body_source=…verbatim_replay`）。
// 厂商（S4A21-完整包-20260923）在登录时走的是**投影**：
//
//	Network/Builders/Init/CharacterOptionBodyBuilder.cs:11-18
//	    var saved  = snapshot?.InitializationSnapshot?.CharacterOptionBlob;
//	    var bits   = snapshot?.CharacterRecord?.Subtype0Tail?.UserStateBits ?? (byte)3;
//	    var packed = snapshot?.InitializationSnapshot?.MainGameOptionBlob;
//	    body = AccountSettings.ProjectCharacterOptionBlob(saved, bits, packed);
//	Game/Settings/AccountSettings.cs:124-138  ProjectCharacterOptionBlob
//	Game/Settings/AccountSettings.cs:140-173  NormalizeCharacterOptionBlob
//
// 逐行读出来的厂商语义（**只写这三个 index，别的一个字节都不动**）：
//
//	NormalizeCharacterOptionBlob(saved)            // :140
//	  1. body = new byte[4 + 512]                  // :142  固定 516 B
//	  2. body[0..4] = u32 LE 512                   // :143  长度前缀恒为 512（即使 saved 更短）
//	  3. body[4..516] 全部填 0xFF（按 u16 逐个）    // :144-148  ⇒ 未写过的 option index == 0xFFFF
//	  4. body[4]=1, body[5]=0（index 0 = 1）        // :150-151
//	  5. saved 为空 ⇒ 直接返回（此时 index 0 真的是 1）// :153-154
//	  6. saved.Length >= 4 且 u32 头合法 ⇒ 把 saved[4..4+declared] 拷到 body[4..]
//	     （否则按"无前缀"整段拷）                   // :156-170
//	  7. **再次**写 body[0..4] = 512               // :171
//
//	⚠ 关键：第 6 步的整段拷贝**会覆盖**第 4 步写下的 index 0。所以
//
//	   **只要 saved 里有真实 payload，厂商投影下 index 0 就等于 saved 自己的
//	   word[0]，并不是 1。** 第 4 步的 `body[4]=1` 只在"没有存档"（第 5 步提前
//	   return）时才可见。
//	   厂商自己的 SelfTest 把这条语义钉死了：
//	   `SelfTests/A21StartupProtocolSelfTest.cs:430-440` 用 saved=null 断言
//	   index 0 == 1（值来自 :150）；而同一段注释 :432 "does not force 130/127"
//	   与 :436-439 断言 index 109/130/127 保持 0xFFFF。
//	   `SettingsHandler.cs:188-190`（写路径，只传 saved+bits）与
//	   `SqliteSelectCharacterDataSource.cs:473-476`（同样只传 saved+bits）都不会改变这一点。
//
//	ProjectCharacterOptionBlob(saved, visibleBits, packedMain)  // :124-138，在 normalize 之后
//	  a. WriteCharacterOptionU16(body, 126, (visibleBits & 0x08) == 0)
//	     // :128-131  index 126 = 1 或 0，依据 `HideFullAvatarMask = 1 << 3`（:24）
//	  b. if (packedMain != null)
//	         WriteCharacterOptionRawU16(body, 127, ReadU16(packedMain, 68))
//	     // :132-136  index 127 = **账号包 main_game_option 的 index 68** 的原值
//	     // （`CharacterHonorOpacityOptionId = 127` :14，`PackedHonorOpacityIndex = 68` :17）
//	  c. 没有任何"不满足就丢弃整帧"的分支：函数恒返回 516 B。
//	     `WriteCharacterOptionRawU16`（:180-187）只在 body 太短时**静默不写**。
//
// 因此本文件的可选投影**不是**"猜一个投影"，而是把上面这段逐行照搬；缺的输入
// （见下）如实按"本仓无从获得"处理，绝不编默认值。

// currentCharacterOptionProjectionEnv 是 op447 登录回放的投影档开关。
//
//	未设置 / 空 / 任何其它值（含 on/true/1/yes/verbatim） —— **原样回放 = 逐字节今天**
//	project                                              —— 走厂商投影
//
// 默认档刻意放在"原样回放"：投影会改写 index 126（以及 127），那是**线上可见的
// 行为变化**，必须有实机 A/B 才配翻转默认；本开关只负责把那条路径做成可验证的
// 选项，不是替线上做决定。
const currentCharacterOptionProjectionEnv = "DNFBRIDGE_CHARACTER_OPTION_PROJECTION"

// currentCharacterOptionProjectionValue 是唯一被承认的投影取值。
const currentCharacterOptionProjectionValue = "project"

// currentCharacterOptionProjectMode 解析 DNFBRIDGE_CHARACTER_OPTION_PROJECTION。
// **只有显式 `project` 才生效**；未设置/空/其它任何值都是默认档（原样回放），
// 与 `currentLoginFallbackKeepsTown` / `readHellBlockedSlotsMode` 同一约定：
// os.Getenv 直接读，零值语义 == 线上默认。
func currentCharacterOptionProjectMode() string {
	if strings.EqualFold(
		strings.TrimSpace(os.Getenv(currentCharacterOptionProjectionEnv)),
		currentCharacterOptionProjectionValue,
	) {
		return currentCharacterOptionProjectionValue
	}
	return characterOptionProjectionVerbatim
}

// characterOptionProjectionVerbatim / characterOptionProjectionProject 是
// character_option_projection 日志字段的两个取值（可 grep）。
const (
	characterOptionProjectionVerbatim = "verbatim"
	characterOptionProjectionProject  = "project"
)

// 厂商 op447 字段表的三个常量（AccountSettings.cs:14/:17/:24）。
const (
	vendorCharacterOptionPayloadLength = 512
	vendorCharacterFullAvatarIndex     = 126
	vendorCharacterHonorOpacityIndex   = 127
	vendorPackedHonorOpacityIndex      = 68
	vendorHideFullAvatarMask           = 1 << 3
)

// vendorCharacterOptionInvalidWord 是厂商给"没有值的 option index"填的字。
const vendorCharacterOptionInvalidWord uint16 = 0xFFFF

// characterOptionProjectionContext 是厂商投影的全部输入。
//
// 三个字段与厂商三个入参一一对应（AccountSettings.cs:124 的 saved/visibleBits/
// packedMain），其中 **后两个就是"我们当前缺的输入"**：
//
//   - VisibleBitsKnown：厂商 `Subtype0Tail.UserStateBits ?? 3` 有兜底 3，本仓
//     也存了同一个镜像列（`mysql_char.go:90` 的 `user_state_bits`，fallback 3；
//     读取口径见 `current_actor_user_state.go:55-63`，同为 "user_state_bits"）。
//     线上只读 SELECT 实测 `character:3`（test2）该列 = **3**。
//     注意别和 `dnf_characters.user_state` 混起来：那是另一个列，char 3 上是 0。
//     这里仍然要求"取不到就显式 Known=false"，而不是偷偷用厂商的兜底 3。
//   - PackedMain：厂商拿的是 `InitializationSnapshot.MainGameOptionBlob`（账号
//     主选项包，"00C5"）。本仓**今天从不落这份数据**：op197 的
//     `handleSaveGameOption1` 只在 `gameOptionSaveWindowOpen` 为真时才写
//     `account:<id>:game_option` 的 `main_game_option_hex`，而现场该表从未有过行
//     （见 game_option_settings.go:30-32 的取证注释）。因此走投影时只能照厂商
//     `packedMain == null` 的那条分支处理：**index 127 保持 normalize 后的
//     0xFFFF 原值不动**，并在日志里标明 `packed_main=absent`。
//     **不允许**为了"看起来完整"而编一个 150 B 的默认包。
type characterOptionProjectionContext struct {
	// VisibleBits 是厂商 `Subtype0Tail.UserStateBits`；只在 VisibleBitsKnown 为真时有效。
	VisibleBits byte
	// VisibleBitsKnown 为假时 index 126 无法按厂商口径写。
	VisibleBitsKnown bool
	// PackedMain 是厂商 `InitializationSnapshot.MainGameOptionBlob`（账号主选项包）。
	// 本仓当前无从获得，恒为 nil；见上面的字段说明。
	PackedMain []byte
}

// characterOptionProjectionResult 是投影结果 + 可 grep 的证据字段。
type characterOptionProjectionResult struct {
	// Body 是投影后的完整帧；失败时为 nil（调用方回落原样回放）。
	Body []byte
	// ChangedIndexes 是被改写的 option index 列表（"变化的 index 列表"，可 grep）。
	ChangedIndexes []int
	// ChangedHex 是每个被改写 index 的 `idx:old->new` 摘要。
	ChangedHex []string
}

// projectCharacterOptionFrame 是**纯函数**形式的厂商投影：
// 输入一帧（`u32 len + 512B blob`）与上下文，输出一帧。
//
// 它不碰网络、不碰仓储、不写日志、不改输入（saved 只读）。
// 语义完全照 `AccountSettings.ProjectCharacterOptionBlob` + `NormalizeCharacterOptionBlob`
// （AccountSettings.cs:124-173），包括"index 0 来自 saved 而不是被强制成 1"这一点。
//
// 返回 error 的情形（调用方必须回落原样回放，**绝不发半截帧**）：
//   - saved 长度不足以构成 `u32 len + payload`；
//   - VisibleBitsKnown 为假（index 126 缺依据）；
//   - 输出长度不是厂商的 516。
func projectCharacterOptionFrame(saved []byte, ctx characterOptionProjectionContext) (characterOptionProjectionResult, error) {
	if len(saved) < 4 {
		return characterOptionProjectionResult{},
			fmt.Errorf("character option frame %d B is shorter than the u32 length prefix", len(saved))
	}
	if !ctx.VisibleBitsKnown {
		return characterOptionProjectionResult{},
			fmt.Errorf("character option projection requires the character %q stat", "user_state_bits")
	}
	declared := int(binary.LittleEndian.Uint32(saved[0:4]))
	if declared < 0 || declared > len(saved)-4 {
		return characterOptionProjectionResult{},
			fmt.Errorf("character option frame declares %d payload bytes inside a %d-byte frame", declared, len(saved))
	}

	// AccountSettings.cs:142-151：定长 516、payload 全部 0xFFFF、index 0 = 1。
	body := make([]byte, 4+vendorCharacterOptionPayloadLength)
	binary.LittleEndian.PutUint32(body[0:4], vendorCharacterOptionPayloadLength)
	for i := 4; i+1 < len(body); i += 2 {
		body[i] = 0xFF
		body[i+1] = 0xFF
	}
	body[4] = 1
	body[5] = 0

	// AccountSettings.cs:156-170：整段拷贝 saved 的 payload —— 这一步会覆盖 index 0。
	copyLength := declared
	if copyLength > vendorCharacterOptionPayloadLength {
		copyLength = vendorCharacterOptionPayloadLength
	}
	if copyLength > 0 {
		copy(body[4:], saved[4:4+copyLength])
	}
	// AccountSettings.cs:171：长度前缀恒为 512。
	binary.LittleEndian.PutUint32(body[0:4], vendorCharacterOptionPayloadLength)

	// AccountSettings.cs:128-131：index 126 = (bits & 0x08) == 0 ? 1 : 0。
	result := characterOptionProjectionResult{Body: body}
	writeVendorCharacterOptionU16(body, vendorCharacterFullAvatarIndex,
		(ctx.VisibleBits&vendorHideFullAvatarMask) == 0)
	// AccountSettings.cs:132-136：只有 packedMain != null 才写 index 127。
	// 本仓 packedMain 恒为 nil（无从获得），所以这一段今天不会执行 —— 保留它是为了
	// 让"厂商语义"完整可审计，而不是假装我们已经拿到账号包。
	if len(ctx.PackedMain) >= vendorPackedHonorOpacityIndex*2+2 {
		writeVendorCharacterOptionRawU16(body, vendorCharacterHonorOpacityIndex,
			binary.LittleEndian.Uint16(ctx.PackedMain[vendorPackedHonorOpacityIndex*2:]))
	}

	result.ChangedIndexes, result.ChangedHex = characterOptionFrameDiff(saved, body)
	if len(body) != 4+vendorCharacterOptionPayloadLength {
		return characterOptionProjectionResult{},
			fmt.Errorf("character option projection produced %d B, want %d", len(body), 4+vendorCharacterOptionPayloadLength)
	}
	return result, nil
}

// characterOptionFrameDiff 逐个 option index 比较两帧，返回被改写的 index 列表。
// 索引口径与厂商一致：optionIndex 的字节偏移 = 4 + 2*idx（AccountSettings.cs:182）。
func characterOptionFrameDiff(before, after []byte) ([]int, []string) {
	indexes := make([]int, 0, 4)
	details := make([]string, 0, 4)
	limit := len(before)
	if len(after) < limit {
		limit = len(after)
	}
	for offset := 4; offset+1 < limit; offset += 2 {
		oldWord := binary.LittleEndian.Uint16(before[offset:])
		newWord := binary.LittleEndian.Uint16(after[offset:])
		if oldWord == newWord {
			continue
		}
		index := (offset - 4) / 2
		indexes = append(indexes, index)
		details = append(details, fmt.Sprintf("%d:0x%04X->0x%04X", index, oldWord, newWord))
	}
	return indexes, details
}

func boolToVendorOptionWord(enabled bool) uint16 {
	if enabled {
		return 1
	}
	return 0
}

// writeVendorCharacterOptionRawU16 是 AccountSettings.cs:180-187 的逐行等价物：
// 越界时**静默不写**（厂商没有"丢弃整帧"的分支）。
func writeVendorCharacterOptionRawU16(body []byte, optionIndex int, value uint16) {
	offset := 4 + optionIndex*2
	if len(body) < offset+2 {
		return
	}
	body[offset] = byte(value)
	body[offset+1] = byte(value >> 8)
}

func writeVendorCharacterOptionU16(body []byte, optionIndex int, enabled bool) {
	writeVendorCharacterOptionRawU16(body, optionIndex, boolToVendorOptionWord(enabled))
}

// characterOptionNoticeFrameForProjection 把"回放帧的选取"（原样帧 / 旧口径重建）
// 与"是否投影"合到一起，供 sendCharacterOptionNotice 调用。
//
// 失败一律 fail-safe：任何 error 都回落 `frame`（原样回放）并把
// `character_option_projection=verbatim` + `projection_error` 记进日志，
// 绝不发一条半截帧。返回值里第二个是**可 grep 的投影档字段**，
// 第三/四是变化 index 列表与 `idx:old->new` 明细。
func characterOptionNoticeFrameForProjection(
	frame []byte,
	frameSource string,
	ctx characterOptionProjectionContext,
	mode string,
) (body []byte, projection string, changedIndexes []int, changedHex []string, failure error) {
	if mode != characterOptionProjectionProject {
		return frame, characterOptionProjectionVerbatim, nil, nil, nil
	}
	result, err := projectCharacterOptionFrameSafely(frame, ctx)
	if err != nil {
		return frame, characterOptionProjectionVerbatim, nil, nil, err
	}
	if len(result.Body) == 0 {
		return frame, characterOptionProjectionVerbatim, nil, nil,
			fmt.Errorf("character option projection returned an empty body for frame_source=%s", frameSource)
	}
	return result.Body, characterOptionProjectionProject, result.ChangedIndexes, result.ChangedHex, nil
}

// projectCharacterOptionFrameSafely 给纯函数加一道 panic 闸门。
// 投影跑在登录回放的热路径上，"引擎不会 panic"不是可以不兜的理由：
// 任何 panic 都必须变成 error，好让调用方回到原样回放那条逐字节已知安全的路上。
func projectCharacterOptionFrameSafely(saved []byte, ctx characterOptionProjectionContext) (result characterOptionProjectionResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = characterOptionProjectionResult{}
			err = fmt.Errorf("character option projection panicked: %v", recovered)
		}
	}()
	return projectCharacterOptionFrame(saved, ctx)
}

// characterOptionProjectionU8 把仓储里存的数值（map[string]string，十进制或 0x 十六进制）
// 归一化成 u8。与 current_actor_user_state.go:55-63 同口径（那里用的是 int64 版）。
func characterOptionProjectionU8(value string) (byte, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(trimmed, 0, 8)
	if err != nil {
		return 0, false
	}
	return byte(parsed), true
}
