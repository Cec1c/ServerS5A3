> 本次公开与最新验收状态见 [范围说明](../../../Docs/SELECTED_PUBLICATION_20261108.md)。以下为原技术记录；不把历史“未部署/未提交”当作当前状态。

# 局部 leader-conditioned render 候选（v2 本地文件冻结）

输入是精确 ACK 候选 SHA256 `0e4a99cb278bb4ec37fc90c027903417eb2f790915941000538fcf2209b81872`；**已冻结的本地 v2 候选** SHA256 `7861cd3c546e73651ddfd28047686a29e580f5bebd38b1a7fd681d3b36fc24e0`。先前 `924a6367…25a5de` 已因缺少 leader actor 实时归属检查而撤回，不能合成或上线。构建器只把 `0x17E985B` 的原 getter call 指向 `0x6F9DC40`，放置 286 字节 x86 helper，更新 `.krtext` VirtualSize 与 PE 校验和；逐字节核对其余输入不变。未碰全局 `0x2973140`，亦未碰服务端源码。大体积候选在被 Git 忽略的 `experimental/`。这里的“冻结”只表示源码、候选字节与本地取证固定，**不代表部署或玩家实机验收**；未提交、未部署、未启动游戏。

helper 保留非易失寄存器与调用栈。只有本机 record 存在、manager `+0x68` 与本机 **leader slot** 一致、leader UID 可解析、该 leader actor 的实时 `+0x4C8` record 八槽 UID 与本机名册逐槽一致、固定八槽 UID 无重复，且本机名册中**恰好一名**实时 actor 的 `+0x4C8` record 指针与被查询 record 相等，才让此绘制调用从本机 record 读取 leader actor；其余情形照原 record 调原 getter。self record 直接走原 getter。这一方案保留生产同队 peer-op9 skip，不发送网络包，也不重建队伍记录。

`test_native_commit_offset.py` 用 ACK 输入的真实 `0x2018090 → 0xB39D20 → 0x20180A1 → 0x2974CDF` 机器码执行，停在 manager store 后。`0xB39D20` 实际返回 `record+4`，所以 `[eax+0x48]` 是 `record+0x4C`。own slot/leader slot 故意设为 1/3、5/2、0/7，manager `+0x68` 实测分别写入 3、2、7，而非 own slot。冻结复跑证据为 `native-host-roster/native-commit-offset-freeze.json`；该执行结果排除“manager+68 提交 record+48 own slot”的说法。独立复核已确认此偏移；同名单异指针本就是需要纠正的逐 actor 缓存情形，没有据此证明错误队长或越界访问，相关阻断意见已撤回。当前 op9 没有独立 PartyID/组队代次字段，guard 不声称能单独识别同名单旧组代次。

`test_native_guard.py` 要求显式 `--input`、`--deps`、`--output`。它从 ACK 输入在内存中构建候选，执行候选真实 x86 callsite/helper、原生 `0x296E590` 本机 record getter、`0x29614D0` 指针函数、`0x2973140` leader getter及绘制处 `cmp eax,esi`。只有 manager→actor `0x296DD80` 与 UID→actor `0x296D730` 是受控夹具叶。测试全用合成 UID `1001..1004`，不含实际角色资料。

四轮 leader 槽 1/2/3/0（最后一轮为从旧槽 3 转回槽 0）、各四端 self 与四 actor 查询，共 64 个相同场景的 ACK 基线/候选对照通过：基线 peer getter 保留旧槽，候选在保护条件成立时返回本机新 leader。十二种失配回退（含 leader actor 断开与异队 record）、原 getter 返回 0 的两种情形、离队→重组→异记录三阶段、真实 `cmp` 分支与非易失寄存器/ESP/队伍内存不变检查均通过。冻结复跑 JSON 在任务取证目录 `native-host-roster/native-guard-evidence-freeze.json`，不进入源码仓库；构建回执为 `native-host-roster/authority-build-receipt-v2.json`。三个 Python 文件的语法编译通过，候选 EXE 磁盘 SHA 已与回执重新核对。

这证明受 leader 判定影响的一条**绘制分支**可在受控内存中一致，不证明资源实际名称、屏幕位置、所有 UI 或玩家实机图标已修复。最终可见效果需要配套客户端后的四人实机验收；当前没有追加截图或闪退日志要求。

复跑命令示例（将路径换成本机实际位置）：

```powershell
python scripts/client/party-host-authority/test_native_guard.py --input <ACK候选.exe> --deps <python依赖目录> --output <任务取证目录/native-guard-evidence.json>
python scripts/client/party-host-authority/test_native_commit_offset.py --input <ACK候选.exe> --deps <python依赖目录> --output <任务取证目录/native-commit-offset.json>
python scripts/client/party-host-authority/build_candidate.py --input <ACK候选.exe> --output <新的独立候选.exe>
```
