# v142 活动相关通知接收位候选

输入是完整 v141 `26abf551753c2745c2e5b349fb24336422a189187ff847c995e0df9a68871079`，输出 `b86000548b09bf438b666b807429c56120130e35f5c89fed4b001da9f4c392ca`，长度均为 111812608 字节。只改变原接收位图的 1303、1386、1455 三位与 Windows PE checksum；不包含尚未发布的 296/812 候选。

`deploy/client/activity_receive_gate.py` 固定完整输入摘要、门控指令、位图、操作号上限和精确文件改动范围；拒绝重复应用与覆盖输入。门控对所有报文类别共用，不能将新增位描述为仅影响 class0。

| 类别/操作号 | 当前原生回调 VA | 包体与用途 |
| --- | --- | --- |
| class0/1303 | 0x1a5ae80 | 三条 `(key:u8, used:u8, limit:u8, auxiliary:u8)`，精确 12 字节；清除并替换管理器 +0x1e4 的次数表。辅助字节含义尚未确定。 |
| class0/1386 | 0xb3f7f0 | `u8 canClaim,u32 days,u32 claimed`，9 字节；普通签到状态、界面刷新与可领取通知。 |
| class0/1455 | 0x8ff9d0 | `u32 progress,u32 claimed`，8 字节；事件 350 的史诗之路状态。 |
| class1/1303 | 0xf48200 | 公会红包应答；失败分支不读后续包体，成功分支读固定 12 字节。内部结果 0 会继续请求 1300/1301，结果 4/34 提示失败，其他结果返回。原业务未修改。 |

1303 次数消费者 0x1a58080 将物品 10162064、10162065、490007856 对应 key 1/2/3，已有条目使用无符号 `used < limit`。缺少条目沿用原生允许分支，不从活动在线时长构造次数。0x1a58210 返回上限或缺省 0。本 MR 不新增服务端发送器，也不改变这些物品的持久计数。当前主干已由 `raid_daily_owner.go` 为 official-24.9 发送持久日账本的三行12字节快照；`raid_daily_availability.go` 中的15字节厂商样本仅供旧档，不能把它与当前分支混用。

验证命令（需要 pefile 与 Unicorn）：

```text
python deploy/client/activity_receive_gate.py ORIGINAL.exe NEW.exe --receipt patch.json
python scripts/verify_client_item_use_count_1303.py --exe ORIGINAL.exe --candidate NEW.exe --receipt counts-native.json
python scripts/verify_client_activity_notifications.py --exe ORIGINAL.exe --candidate NEW.exe --receipt activity-native.json
```

真实门控执行原版/候选各 65536 次，唯一准入变化为这三项。1303 的 8 组通知回放执行真实字节读取、容器清理、查找、lower_bound/end 与物品次数判定；树分配/插入平衡、free 和 security cookie 是 fixture。8 组公会应答执行实际分支与 12 字节读包，UI、字符串、格式化与网络叶子为 fixture。1386/1455 共 18 组实际回调验证空对象/空 UI、布尔归一化和 u32 边界；UI 查找、RTTI 与刷新是 fixture。完整网络派发与画面渲染不在回放范围。

累计在线 2347 的 108/1206 接收位在 v141 已开启，不能以此三位补丁宣称在线 UI 修复。现场只读检查已推进至原生窗口、奖励/阶段控件和资源布局；窗口显示标志不等于画面验收。活动列表闪退尚未取得当前版本可对应的异常地址，不进行猜测性的空指针或活动目录改写。两项继续开放。

2026-10-27 正常合入权威主干 `ed09f60640ddc0963be3ed223859fb369a8ce09a` 后，固定代码提交 `572118720ea4435d75fd6a955acbf8c35da2d20e` 重新从真实 v141 输入构建，输出仍精确等于上述 b860 摘要。26组通知、8组公会红包应答、65536编号原生门控及服务端边界检查均通过。Go 子树与该主干完全相同；后续文档提交不改变被测脚本。

工具仍只支持历史 v141 输入，不能直接用于当前累计客户端。保持 Draft，候选仅作内部验证，尚未进入预览或稳定玩家通道；活动界面及闪退问题继续开放。后继客户端适配必须固定新的完整输入，完成本次 anti-RE 加锁与最终兼容验证，再遵守 `CLIENT_RELEASE_SYNC.md` 的双 Release、镜像和实际玩家通道核验顺序。
