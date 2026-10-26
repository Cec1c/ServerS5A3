# 艾肯 individual custom 升级

2026-10-22：用户已在 v154 实机验收艾肯装备升级，并授权合并。验收范围是 op1342 升档；op1340 改造属性和 op1341 套装赋予仍单独修复。接收适配已随 v151 发布并保留在 v154，当前 EXE 摘要为 9327087d147576314dd95c6c0b0a638b1525bca48734658fe4f9b97dc33f82ea；本次源码合并不另发 EXE。下文候选/未部署描述保留为初始实现的历史边界。

下列零状态、1340 与早期1341未实现说明保留为历史取证；当前累计实现另有1341套装事务及双framing分派，实际范围以源码和对应回归为准。

## 新发放掉落实例的零状态

只读现场快照中的四件真实艾肯模板 100120561、100220536、100352922、100390016 的 119B 行均满足 `raw[0x72:0x77] == 0`，来源为 `dungeon_pvf_drop_pickup`。因此全零五字节是当前发放路径的真实初始载体：D 档、两个 option 槽未选，而不是缺载体。

原 1342 owner 在扣料前要求两个 option 都属于 PVF option 集合，原 1340 owner 也要求两个槽已合法；这会先于材料检查拒绝新发放装备。现 1342 只以 grade 和升档材料为业务门槛，升档时保持 option 字节不变；1340 允许槽值 0 表示未选，非零值仍必须是 PVF option，另一槽相同 option 仍拒绝。

回归使用真实 PVF 模板 100120561（`warriors dungeon`、`custom ability count 2`）及真实材料 10161536（D→C 11）和 10161533/34/35（option 13 D 档 23、option 1 D 档 28）贯通 1340 两次选槽、1342 升档与 NOTI14/ACK。测试代码不复制账号数据；原始现场证据单独保留，不进入公共仓库。

未实现 1341；未证明首次选槽与 `[disjoint]` 表的历史计费差异，当前 1340 继续使用客户端证据指向的 `[info]` 选项目标档材料。未运行 Go、未部署，主线程串行验证前不得宣称实机通过。

op1342 原先没有实际资产 owner。当前候选新增独立事务入口：请求只含主背包槽 u16；以角色仓储当前 grade 决定下一档，保留两个 option，消耗真实 individualcustominfo 表的目标档材料10161536×11/18/34。D/C/B/A=0/1/2/3，A档及无效 grade/option 拒绝。当前资源模型为确定性同option升grade，不添加金币、随机率或普通强化buff；这不是对历史原服隐藏规则的绝对证明。变化的升级价目、未知升级字段或不支持的option参数结构拒绝。

账号/角色、唯一槽端点、Count=1、真实 equipment 的 warriors dungeon/count2、锁定、到期状态均在专用owner事务内核对。容器从当前PVF placement和repository扩容记录取得；包括合法快捷格与每页基础32格+0/8/16/24，不另写固定拒绝范围。锁定复用IsLockedAt，期限复用既有expired策略，使用gameplayNow；未来有效期限不因非永久被拒。材料按合法槽排序，跳过锁定/过期栈，缺料不保存任何部分扣量。

数量以ItemStack.Count为真源，同步现有amount_or_count/amount/count/stack/quantity与有效119B raw别名；count_or_instance是实例字段，保持原样。装备只递增119B的0x76；0x72/0x73两个option、0x74第三option、0x75/BF和强化/附魔/镶嵌等保持。所有已提供的有效raw/tail/mode1别名必须先一致，升级后同步并保存显式mode1_raw_e_hex，使公开master亦可冷投影新grade。

canonical边界：repository ItemID是权威，119B raw的ID为0可以沿已有patchCore投影；非零冲突ID拒绝。缺raw但存在有效五字节状态时，复用既有currentItemListEntryFromStack构建持久行；完全缺少状态、非119B源raw、短/无效或冲突别名保守拒绝。不能据此宣称所有历史实例都覆盖。native_buff_instance若存在，检查同角色背包/仓库/穿戴及账号仓库重复；不把质量seed或raw+0E猜成唯一ID。未携带持久ID的两个独立槽不据raw相同合并/拒绝。

事务提交成功后发送class0 NOTI14，再发class1 op1342成功payload01；失败payload0000，不猜错误码消息。nil session直接返回，事务有createWriteTimeout。后续通知失败只返回错误，不重新扣料、不补发、不伪造成功。无nonce的相同合法请求是当前grade下一次点击，逐次付费升1；顶级拒绝，不增加时间窗吞请求。

针对性回归使用固定真实PVF摘要25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348，真实资源测试默认不启用：DNFBRIDGE_AIKEN_REAL_PVF_SMOKE和兼容回退DNFBRIDGE_REAL_PVF_SMOKE均未设置或为空时t.Skip，使默认全套/CI不依赖私有PVF。优先使用艾肯专属变量，仅专属变量为空时回退旧变量；显式选择后文件缺失、读取失败或摘要错误均Fatal，不回退、不skip。主线定向验证只设置专属变量，避免启用其他旧real集合；报告中的0skip仅指显式选择真实资源的定向运行。完整legacy/upper三档、31模板×3档、两option位置×256值、容器/期限/锁定/别名、Save失败与UOW提交前最终边界故障、通知失败、同包逐级收费、Save/Load和mode1序列化均在独立内存仓储测试。UOW故障注入不是实际MySQL COMMIT网络失败；冷投影不是完整客户端登录或实际穿戴GUI。

客户端依赖：发布v150原接收gate拒绝1342。主线本地class1/1342接收适配器是独立未发布候选；原生ACK消费者证据在gate以下，不能替代实际分派/NOTI14/GUI联验。当前服务端候选和测试不代表P106实机已解决，未部署、未修改玩家/EXE/PVF。

独立审查补充：消费材料前按现有投影规则计入同物理端点的所有文本别名，包括不同物品、空行和空白/前导加号；只拒绝实际目标或被消费槽的歧义，不影响不同列表或未触及槽。目标模板已由PVF确认为装备，升档时校准item_kind，使缺失或历史冲突元数据不会把NOTI14的质量seed按堆叠数量1覆盖；原RawEntry除grade字节外保持，其他属性不改。独立边界原4通过/12失败，修正16通过，均0skip。

## op1340 属性选项变更

当前 v151 `10C759D..10C7657` 对两个 option 位置分别发送 4 字节 body：`u16 槽, u8 位置(0/1), u8 选中option`；仅当选中值大于0且不同于当前实例时发送。三个 sender 均经 `374F321` 写 class1。接收 gate 虽放行 1340 的 class0/1/2，但 class0 同号属于冒险团，因此本模块只注册 class1/1340，不增加 legacy route。

发送前 `10C6650` 按 `(individual [info], 选中option, 当前grade)` 汇总材料，并由 `E66F30` 做库存校验。`[info]` 的每组固定在 index 后给出 D/C/B/A 四个 `[grade]+[list item count]`；任何未知index、重复/缺档、非正数量或额外结构均使资源拒绝。当前真实表 option13 的成本为 D/C/B/A 23/33/48/78，材料ID 10161534。

专用 owner 在同一资产事务中核对角色/账号/容器、唯一0行端点、Count=1、PVF装备模板、锁与期限、合法五字节状态和两position。选中值必须属于16个option、不同于当前值与另一位置；按选中option和当前grade扣对应材料的合法背包栈，锁定/逾期栈跳过，部分不足不保存任何扣量。目标装备只改 `0x72+position`，grade/另一option/第五字节以及强化、附魔、镶嵌等其他实例字节保持，并同步有效RawEntry/tail/mode1别名。

成功后先发class0 NOTI14携带目标装备与材料最终数量，再发class1/1340成功`01`；失败`00 00`。保存或最终提交失败回滚；通知失败不重跑事务、不补成功ACK。`R160` 真实PVF目标组覆盖两position、四grade、实际成本、缺料/部分不足、锁/逾期、坏包/越界、重复、保存回滚、通知失败、载体同步与class0隔离；既有Aiken及完整dnfbridge包通过。

边界：没有启动游戏或执行完整原生GUI；未证明两option是否必须来自不同group，因此只拒绝完全相同的另一position，不虚构跨组消费规则。op1341套装仍未实现：其两段数组到已穿戴件、费用和持久字节尚无完整证据。
