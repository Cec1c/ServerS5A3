# 艾肯 individual custom 升级

op1342 原先没有实际资产 owner。当前候选新增独立事务入口：请求只含主背包槽 u16；以角色仓储当前 grade 决定下一档，保留两个 option，消耗真实 individualcustominfo 表的目标档材料10161536×11/18/34。D/C/B/A=0/1/2/3，A档及无效 grade/option 拒绝。当前资源模型为确定性同option升grade，不添加金币、随机率或普通强化buff；这不是对历史原服隐藏规则的绝对证明。变化的升级价目、未知升级字段或不支持的option参数结构拒绝。

账号/角色、唯一槽端点、Count=1、真实 equipment 的 warriors dungeon/count2、锁定、到期状态均在专用owner事务内核对。容器从当前PVF placement和repository扩容记录取得；包括合法快捷格与每页基础32格+0/8/16/24，不另写固定拒绝范围。锁定复用IsLockedAt，期限复用既有expired策略，使用gameplayNow；未来有效期限不因非永久被拒。材料按合法槽排序，跳过锁定/过期栈，缺料不保存任何部分扣量。

数量以ItemStack.Count为真源，同步现有amount_or_count/amount/count/stack/quantity与有效119B raw别名；count_or_instance是实例字段，保持原样。装备只递增119B的0x76；0x72/0x73两个option、0x74第三option、0x75/BF和强化/附魔/镶嵌等保持。所有已提供的有效raw/tail/mode1别名必须先一致，升级后同步并保存显式mode1_raw_e_hex，使公开master亦可冷投影新grade。

canonical边界：repository ItemID是权威，119B raw的ID为0可以沿已有patchCore投影；非零冲突ID拒绝。缺raw但存在有效五字节状态时，复用既有currentItemListEntryFromStack构建持久行；完全缺少状态、非119B源raw、短/无效或冲突别名保守拒绝。不能据此宣称所有历史实例都覆盖。native_buff_instance若存在，检查同角色背包/仓库/穿戴及账号仓库重复；不把质量seed或raw+0E猜成唯一ID。未携带持久ID的两个独立槽不据raw相同合并/拒绝。

事务提交成功后发送class0 NOTI14，再发class1 op1342成功payload01；失败payload0000，不猜错误码消息。nil session直接返回，事务有createWriteTimeout。后续通知失败只返回错误，不重新扣料、不补发、不伪造成功。无nonce的相同合法请求是当前grade下一次点击，逐次付费升1；顶级拒绝，不增加时间窗吞请求。

针对性回归使用固定真实PVF摘要25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348，真实资源测试默认不启用：DNFBRIDGE_AIKEN_REAL_PVF_SMOKE和兼容回退DNFBRIDGE_REAL_PVF_SMOKE均未设置或为空时t.Skip，使默认全套/CI不依赖私有PVF。优先使用艾肯专属变量，仅专属变量为空时回退旧变量；显式选择后文件缺失、读取失败或摘要错误均Fatal，不回退、不skip。主线定向验证只设置专属变量，避免启用其他旧real集合；报告中的0skip仅指显式选择真实资源的定向运行。完整legacy/upper三档、31模板×3档、两option位置×256值、容器/期限/锁定/别名、Save失败与UOW提交前最终边界故障、通知失败、同包逐级收费、Save/Load和mode1序列化均在独立内存仓储测试。UOW故障注入不是实际MySQL COMMIT网络失败；冷投影不是完整客户端登录或实际穿戴GUI。

客户端依赖：发布v150原接收gate拒绝1342。主线本地class1/1342接收适配器是独立未发布候选；原生ACK消费者证据在gate以下，不能替代实际分派/NOTI14/GUI联验。当前服务端候选和测试不代表P106实机已解决，未部署、未修改玩家/EXE/PVF。

独立审查补充：消费材料前按现有投影规则计入同物理端点的所有文本别名，包括不同物品、空行和空白/前导加号；只拒绝实际目标或被消费槽的歧义，不影响不同列表或未触及槽。目标模板已由PVF确认为装备，升档时校准item_kind，使缺失或历史冲突元数据不会把NOTI14的质量seed按堆叠数量1覆盖；原RawEntry除grade字节外保持，其他属性不改。独立边界原4通过/12失败，修正16通过，均0skip。
