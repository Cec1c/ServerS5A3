# BUFF 1428 入站归属与物理实例边界（R168 静态审查）

## 结论

当前客户端 `official-24.9` 在 `DNFBRIDGE_BUFF_SWAP_REAPPLY=on` 时，op1428 仍必须由 Buff 强化 loadout 解析；op1426/1368 继续由 native BUFF owner 管理。历史六字节 `00 00 slot mode value` 一旦进入 `handleNativeBuffSwapRegistration`，就会被 `buffswap.Parse` 读成 skill0/list0/index，形成位置绑定。角色 1006 的 13 条记录正是这种形态：无 `wire_profile`、无实例、来源 `current_exe_3C9FA0`。

R167 候选同时把 `sendSkillSwitchInventoryProjection` 和 `answerSkillSwitchRegistration` 也切到新谓词，等于在 native reapply 开启时重新启用两条 op1428 下行。当前没有证据证明该客户端接受这两条下行，而且它们会落在 op19 穿脱边界和登记哨兵上。因此 R168 只保留入站路由修复：这两条路径继续由 `buffSwapReapplyEnabled()` 关闭；op19 不补发 1428。

## 不做的部分

- 不迁移、不删除 1006 的既有污染记录，不按模板 ID 猜造物理实例。
- 不把 1426 的物理实例绑定改为按模板或位置匹配。
- 不对非 `official-24.9` profile 改 1428 路由；这些 profile 保留历史 1428/1369 路径。
- 不在 op19 后重发 1428，不用补发掩盖实物绑定问题。

## 只读证据

- 1047 的 9 条、2046 的 16 条 current1426 登记均带 `native_buff_instance`，且每条唯一匹配当前背包或装备行；包括跨容器 `1:32` 和同模板不同实例。
- 1006 的 13 条无实例记录可由 1428 六字节体直接解释；其中 8 条存储位置已变化。当前只是唯一模板回退还能解析，不能证明物理实例。
- `22f93dc3` 与 `ca828183` 在本补丁触达的生产文件上完全相同；原候选只需处理 changelog 顶部冲突。
- R165 的 `TestR165SkillSwitchWearReplayUsesCommittedRegistration` 在 `ca828183`/`22f93dc3` 树中不存在；6e5fe0a 的旧红灯不再适用。当前没有测试要求 op19 回放 1428。

## 静态检查重点

- `currentClientLoadoutOwnsBuffSwap1428` 只用于 `handleSkillSwitchInventoryCommand` 的入站分派。
- `sendSkillSwitchInventoryProjection` 与 `answerSkillSwitchRegistration` 仍以 `buffSwapReapplyEnabled()` 为关闭条件。
- 新增回归先落一条 binding-bearing loadout 记录，再检查 op19 边界、投影和哨兵均为 0 字节；空设置库不会让“无 1428”断言空过。
- profile 前提：control 的 `native-channel-profile-official-24.9` 会设置 `DNFBRIDGE_CURRENT_CLIENT_VERSION=official-24.9`。若实例实际使用 `90cn` 或 `90cn-decode-bypass-v1` 且没有显式设置该变量，本补丁不会改变其 1428 路由；在没有当前客户端证据前不应扩大谓词。

## 主线程验证（2026-10-21）

候选新增5项与相关122叶回归均通过，0失败、0跳过，包含真实PVF穿脱与冷会话恢复。串行运行最低可用物理内存分别19.21/17.84 GiB。只读线上容器配置确认 `DNFBRIDGE_CURRENT_CLIENT_VERSION=official-24.9` 且 `DNFBRIDGE_BUFF_SWAP_REAPPLY=on`；进程环境读取工具不可用，容器配置不冒充进程环境实读。

已并入本地累计候选，尚待累计全量检查；未部署、未实机验收。此修复防止新的1428错误归属，不能据此宣称历史无实例登记或所有穿脱显示问题已解决。
