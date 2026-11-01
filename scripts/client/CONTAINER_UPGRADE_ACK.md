# 金库与背包扩容回包许可

分类：功能修复。

当前累计客户端 `534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24` 会发送金库 op408、背包 op430，但 class1 接收门拒绝这两个回包，无法清除对应请求的 pending 状态。408 有原生回调；430 无专有回调，仍需要公共调度器清除 pending。容量本身由独立列表/容量通知投影。

`patch_container_upgrade_ack.py` 只接受上述完整 SHA256 和长度，生成独立候选 `7a3be8183b164a0d47b00564ca90e11d384baca854b5f730796840d1f7a3b18f`。它将原接收门调用指向既有补丁节空白位置的短助手，仅对 class1 的408/430返回许可，其他输入回落原门。变更范围为一个调用、49字节助手、节虚拟长度与PE校验和；入口、导入、TLS、来源通知及其他累计代码保持原字节。未知版本须重新适配，不能仅修改输入摘要。

```text
python scripts/client/patch_container_upgrade_ack.py --input <匹配输入DNF.exe> --output <新候选DNF.exe>
python scripts/client/verify_container_upgrade_ack.py --before <匹配输入DNF.exe> --after <候选DNF.exe> --out <新证据目录>
```

验证工具需要 Python 3 与 `unicorn==2.1.4`，Windows 上另验证 `SEC_IMAGE_NO_EXECUTE` 映射。它不执行 PE 入口、不启动游戏、不连接服务器。检查发送包、实际 ACK 调度器、pending 清理、408错误读取，以及class0/1/2/255各65536个opcode的许可与ABI；其他class抽查相关opcode。检查未知、截断、损坏与已修补输入拒绝。

服务端的扩容券补丁依赖该许可。源码合并不代表玩家已更新：部署前必须从当时最新累计成品核对本修补仍被包含，先完成EXE Release及镜像、玩家通道签名和实际下载读回，再上线依赖服务端。当前候选只有自动化验证，游戏内验收另行记录。
