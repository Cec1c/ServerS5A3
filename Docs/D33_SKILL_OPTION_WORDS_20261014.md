# D33 技能选项（锁定 / CD 提示）字段取证与保存回放可观测性

2026-10-14，装备席。本文件覆盖 DOC-060、DOC-114 与 DOC-053 里的三条技能设置症状（技能锁定重选重置、攻击类型物理/魔法重选变普通、CD 提示重选需再激活）。结论分三档写清楚：已证的字段、已覆盖的源码路径、仍缺证据的部分。**不关闭任何 DOC 条目** —— 源码与受控回归不等于实机验收。

## 1. 已证：这些设置存在哪里（固定客户端 EXE）

固定磁盘客户端 DNF.exe，SHA256 bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531（与 45 批原生取证用的同一份）。读写 helper 的基址都是 owner+0x1CC，也就是 512B 每角色选项 blob 本身。原始反汇编与地址保留在调查资料的 43 批目录（REPORT-SKILL-LOCK-PROTOCOL-43.md 与 skill-lock-protocol43/*.asm.txt），本轮没有重新反汇编。

| 设置 | blob 位置 | 写入路径 | 恢复路径 | 边界 |
| --- | --- | --- | --- | --- |
| 技能锁定（SEAL） | word 128..191 = 树0、192..255 = 树1，每树 64 项；值 = 客户端技能槽号，空项 0xFFFF | 写 0x39A6BA0；导出 0x2255AA0（面板 0x293AFD0 的 SEAL_ON/SEAL_OFF 分支在 0x293B24F 调用它，导出同时写 word0/1） | 读 0x39A6C00；恢复 0x2255880（S2C390 读取端 0x2255DB0 在复制 512B 后调用；NOTI19 处理器 0x2020660 在 0x2020DE1 再次调用） | 值是客户端槽号，不是 PVF 模板技能 ID |
| CD 提示列表 | word 2..21 = 树0、22..41 = 树1，每树 20 项，空项 0xFFFF | 导出 0x22555F0（面板 0x1E923E4..3F9 读 0x292AFA0 后调 0x292AFC0(flag,1)） | 0x2254FC0（先对全部 512 槽清标记，再按两树列表恢复，含技能可用性保护） | 没有取得中文菜单标签与实际点击前后包，因此报告口径只称“CD 提示候选路径” |
| 攻击类型（物理/魔法） | **未证** | 无 | 无 | 本轮没有任何字段或包证据；不猜、不实现、不用其它字段代替 |
| 其它已知、非本轮范围 | word43 = ExportScenarioDifficulty（写端 0x2255CA0）；word126/127 = 全身时装可见 / 称号不透明度 | | | 只读，不改写、不清理 |

真实捕获：45 批原生取证里，角色 q7a（character:31）在技能面板点「锁定」后客户端实发 op447（body 516 = u32 512 + 512B）并被逐字节落库。该 payload 里只有四个非 0xFFFF word：0、1（锁表导出会写）、43（难度=0）、128=5（树0 锁表第一项，客户端技能槽号 5）。本仓把它作为固定 fixture（sha256 4fe7d38648f9d5a19c372a73c684c7d66bd473dc9db6245da8bc0e9a3d7e297b）。

## 2. 源码现状（基线 5fc6807 已含的部分）

- 保存：handleSaveCharacterOption 把 op447 的完整帧逐字节写入 character:<id>:character_option 的 character_option_body_hex；official-24.9 只接受 512（与客户端读取端同条件）；进场窗口守卫挡住入场模板，避免把工厂默认值冲掉玩家存档。
- 回放：每次真实选角（op4 生命周期，以及返回选角 op7/op8 后重进）按 characterOptionNoticeSent 闸门回放一次 class0/390，且 390 在 19 之前；账号档 0x00AD/0x01C7 仍按 P55 保持每连接一次。
- 45 批原生复现的「同连接返回选角重进同一角色：19 有、390 无」正是旧的一次性**连接**闸门；该缺陷已由 31e2b51 拆出**选角**闸门修复并合入基线。
- 因此锁定与 CD 提示的**数据载体**在基线上已经“整帧存取 + 每次选角回放”，本轮没有发现新的字节级缺陷；缺的是现场可读的字段对应与实机验收。

## 3. 本轮改动（只读解码 + 日志可观测性 + 回归；不改一个字节的线上行为）

- 新增 go-server/internal/services/dnfbridge/character_option_words.go：按上面已证的 word 索引做只读解码（锁表 / CD 列表、word 差异、清空检测、payload sha256）。明确边界：不重定义其它 word、不做保存门禁、不参与任何写入决策。
- game_option_settings.go：op447 保存事件的日志接上解码，新增 previous_saved / payload_sha256 / option_words_decoded / changed_word_count / changed_word_indexes / lock_tree0_slots / lock_tree1_slots / cd_tree0_slots / cd_tree1_slots / lock_slot_count / cd_slot_count / option_word_encoding；390 回放事件新增同一套字段；并新增具名事件 game-character-option-skill-lists-cleared（上一份存档里的锁定 / CD 列表在新存档里消失时）。落库字节与回放字节都不变。
- 新增 character_option_words_test.go：真实捕获帧解码；四张列表按树分开且槽号 0 合法；非 512 形状一律“不知道”；word 差异；清空检测；端到端（真实 op4 选角 + 真实 447 分发 + 冷进入与同连接再选角的 390 逐字节回放 + 日志字段）。

## 4. 测试与 RED→GREEN

旧失败复现没有落在仓库里：用临时切片把角色选项回放改回旧的一次性连接闸门（跑完按 SHA256 校验还原，ab5d9043f208249d51c9fb9e44f2f7c0ad7412ea073b14fb9a24d3661e6553f7 前后一致），得到：

- TestCharacterOptionEntryRegressionReturnSelectReplaysNewCharacter390：390/19 order: option=-1 skills=30
- TestCharacterOptionEntryRegressionAccountReplayGateUnchangedAndSameCharacterReentry：同样 option=-1 skills=30
- TestCharacterOptionEntryRegressionNormalGameSwitchABARestoresWholeBlob：normal-A-to-B 与 normal-B-to-A 均 actual390=-1 actual19=30
- TestCharacterOptionEntryRegressionSameEntryRouteRetryDoesNotDuplicate390：emitted390=0 want1
- 本轮新增的端到端用例：character_option_words_test.go 的 390/19 order: option=-1 skills=30

还原后 GREEN：-run TestCharacterOption 全部通过（含 6 个新用例与既有投影/守卫/回放回归）。命令口径：固定 go C:/Users/16ww/go-tc/go/bin/go.exe，GOTOOLCHAIN=local、GOPROXY=off、GOSUMDB=off、GOENV=off、GOWORK=off、-mod=readonly -buildvcs=false -count=1，GOCACHE 用本席独立目录。未跑全套重测试（主代理负责集成）。

## 5. 现场 A/B 怎么用这些新字段闭环

43 批报告留下的三条取证目标（锁操作是否发 447 的 516B、锁表 word128..255 是否对应菜单、CD 提示是否改 word2..41）现在可以直接从日志读出来：

1. 让操作者在真实客户端对目标角色做一次动作（点一次锁定，或切一次 CD 提示），两次之间退出到选角再进同一角色。服务端日志里：
   - game-character-option-saved 的 changed_word_indexes 就是这次动作改了哪些 word（锁定应落在 128..255 段，CD 提示应落在 2..41 段）；
   - lock_tree0_slots / lock_tree1_slots / cd_tree0_slots / cd_tree1_slots 给出改动后的槽号，lock_slot_count / cd_slot_count 给出项数；
   - 若出现 game-character-option-skill-lists-cleared，说明这一份新存档把上一份的锁定 / CD 列表刷没了 —— 这就是“被重置”的服务端证据（本改动只记录、不拦保存）。
2. 同一角色冷登录或返回选角重进时，game-character-option-notice-send 的 payload_sha256 必须等于保存时的 payload_sha256；不等或没有该事件才是问题。
3. 同时记录 client version 与 DNFBRIDGE_CHARACTER_OPTION_NOTI / DNFBRIDGE_CHARACTER_OPTION_PROJECTION / DNFBRIDGE_GAME_OPTION_ENTRY_GUARD 的实际取值，以及 390 与 19 的先后。

## 6. 仍未闭合（本条目不关闭 DOC-060/114/053）

- 三条症状的原生 GUI 验收都还没在改后源码上做过：本轮没有任何实机结论。
- 「攻击类型（物理/魔法）」没有取得任何字段或包证据：既不在锁表 / CD 列表里，也没有找到 EXE 侧写入点；在拿到证据前不实现、不用其它字段代替。
- 同一连接再选角时客户端是否重推 op447：45 批实测在重进同一角色时**没有** 447（只有服务端 390/19），因此本轮不基于“入场模板覆盖”假设去改保存门禁；若后续现场出现“被默认模板刷掉”，game-character-option-skill-lists-cleared 会先把它抓出来。
- 冷连接（全新客户端进程）的 390/19 顺序与内容：源码回归覆盖，实机未验。

## 7. 交付边界

只在本席独立工作树（E:/90/bugs-round2-20261014/equipment，基线 5fc6807，分支 work/bugs-round2-equipment）改动；未部署、未推送、未改其它工作树 / 用户 DOCX / 原台账 / 生产资产，也未改装备或任务领域。改动路径：

- go-server/internal/services/dnfbridge/character_option_words.go（新增：只读解码与日志字段）
- go-server/internal/services/dnfbridge/game_option_settings.go（保存 / 回放日志接上解码；字节与门禁不变）
- go-server/internal/services/dnfbridge/character_option_words_test.go（新增回归）
- Docs/D33_SKILL_OPTION_WORDS_20261014.md（本文件）
- changlog.md（记录）