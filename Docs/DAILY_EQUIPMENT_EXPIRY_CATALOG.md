# D17 每日装备期限与全类型目录边界

> R162 第二阶段已修复、待主代理验证：每日邮件回调新增独立可信门，要求 AppendSystemMail 创建时写入 attachment_grant_anchor=system_mail_created_at_v1、非空 source、system_mail=true 与 sender=0，并排除玩家/拍卖转移。旧邮件不补凭据；旧 raw/hex 实例无期限不重建来源，原始行须匹配物品身份。Extra 合并先复制。新增 source/raw 控制、Extra 隔离、op95 三类故障及邮件→op19→到期链测试，均未编译/执行；旧 PASS 仅对应上一阶段。详见 worker/CURRENT.md 与 evidence/manifest.json。

## 固定范围

基底 `c1d8727205e8392c85413647c08855d6e1e7f859`。PVF SHA256 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。本候选未部署，不触碰旧玩家数据或迁移旧实例。

全目录审计覆盖232662份equipment/stackable的.equ/.stk，208946条清单引用、774种header，解析错误/缺引用均0。字段存在不等于删除许可；详细矩阵及全部原始枚举留在独立运行证据中，不打包进业务补丁。

## 实证问题及修复

真实项链400300075（耀之荣光项链，租赁）声明 `[daily delete item] 1`。原catalog只读取stackable的该字段；实际grant事务产出实例没有期限。全资源中装备含该字段248份，其中247份值1且无其他绝对/相对期限冲突，另一份400330115值14，本候选保持拒绝。

仅识别精确值1、无绝对/相对期限冲突的每日装备。既有PVF grant的BuildNew记录 `runtime_pvf_daily_equipment_grant` 来源；继续使用现有下一次06:00（UTC+8）边界，不发明首次穿戴起算。清理须同时满足真实PVF路径/模板、来源、ExpireAt、daily_delete_at、expire_time、expire_unix一致，且期限是整秒06:00。加载不建立新来源；旧租赁、GM、缺失或冲突来源保持。

邮件领取（op95）在同一 mail owner 资产事务内补齐这项证据。alignedcmd 的领取解析增加逐附件回调；dnfbridge 只对精确的每日装备模板计算，使用可信 `mail.CreatedAt` 的下一次06:00，并在附件 Extra 写入来源与三项一致期限。回调不修改共享 DefaultExtra，因此同一封邮件里的新附件和已有 GM/转移实例可以各自保留。附件已有的 ExpireAt、Extra 或原始 119B 行期限优先；玩家邮件、拍卖返回、GM 覆盖及旧实例不迁移。缺创建锚或来源与期限冲突时，领取事务失败，背包和邮件都不提交。装备耐久仍只来自 PVF durability 证据，期限不写入 0x0B。

库存和普通穿戴actor12..25共用已有资产事务、每日时钟及提交后op14/穿戴后置通知。失败事务不发清槽；旧代次不提交。没有新增定时器、包号或自制属性更新。装备期限不能覆盖0x0B耐久：每日投影只对stackable写倒计时，通用行兜底也跳过PVF明确equipment（零耐久仍是零）。

## 有意义的验证

新测试从真实模板经过既有grant事务获得来源和期限，再执行明确玩家op19、合法项链槽19、到期回调、持久删除、16字节头下行list3 op14和重复清理。另含库存保留/错误owner/旧代次、穿戴保存失败回滚再续办及零耐久与stackable/无元数据旧行兼容控制。未从测试直接seed有效grant来源。

R162 邮件回归使用真实 400300075、AppendSystemMail→正式 op95：新附件得到原创建时刻的下一06:00并可清理；同封 GM 附件保留独立期限；玩家 op94 与拍卖投递的既有期限不产生 daily 来源；缺创建锚回滚；满包保留邮件，腾格后重领成功；旧创建时刻的邮件不会把期限滑到领取时刻。零耐久断言只检查没有伪造 durability/期限来源。

普通CI无真实资源时资源测试Skip；显式坏资源路径Fatal。真实目标命令：

```powershell
$env:DNFBRIDGE_DAILY_EQUIPMENT_PVF='<approved Script.pvf>'
$env:DNFBRIDGE_RELATIVE_EXPIRY_PVF=$env:DNFBRIDGE_DAILY_EQUIPMENT_PVF
$env:GOMAXPROCS='3'
$env:GOFLAGS='-mod=readonly -p=2'
# use the approved toolchain/cache in the local runner
& '<go.exe>' test -buildvcs=false -count=1 ./internal/services/dnfbridge -run '^(TestDailyEquipmentReal.*|TestExpiryEquipmentZeroDurabilityKeepsCarrier|TestRelativeTrialWornPreservationAndRollback|TestRelativeTrialOnlineWornExpiryAndProjectionRetry|TestDailyDeleteGrantPersistsBoundaryAndSeparatesNextDay|TestDailyDeleteInvalidDeadlineRollsBackAllContainerDeletion|TestDailyDeleteOnlineAtSixUsesCommittedOp14AndRejectsStaleCharacter)$'
```

相对期限补丁保留；目录扫描及报表不作为默认CI测试。

## 保持开放

- 旧实例没有可信grant来源，不能由模板名/日历反推发放日批删。
- op95 邮件领取已覆盖；其他独立 reward owner 仍需要逐入口证据，不能把本项称为全部发放入口覆盖。领取前的邮箱展示修补没有扩大为每日装备迁移，旧附件仍按原证据保留。
- 相对装扮/宠物等穿戴槽不在12..25清理范围；各自原生对象、属性/技能回收未证明。
- 绝对活动日期沿既定续期政策，尚无通用绝对到期删除闭环；27份绝对/相对混合、28份relative加usable expired item不得统一删除。
- 解封起算26075、带期限三元礼包84份、usable datetime98份、title expiry date87份及usable expired item419份均有明确目录证据，但未知原生语义不能由字段名推断。usable period另有两份日期字符串畸形资源490007562/490008814，未猜转换。
- D156晶块槽错误准入另案。合法op19测试从已就绪测试会话开始，不代表完整游戏登录或NPC租赁购买；op14解析绿不等于GUI/套装/附魔/被动回收实机通过。
