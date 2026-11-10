# 修复累计配对客户端候选

将擂台骰子收包与松键修复，以及婚姻收包准入适配到当前 Buff 两组登记配对客户端。构建器只生成独立离线候选，不声明正式发布或游戏验收。

旧公开配方假定的收包回落地址与当前累计文件不同。本配方保留现有 `0x6F88500` 回落链，在 `.gld127` 原有零填充区增加 `0x6F88AC0` 辅助函数。放行 C0/112、113，以及婚姻所需 41 个显式分类项；其中 C0/997、1000 已经开放，因此实际新增 41 项。C0/1010 保持原状，C1/1054 沿用原有准入。松键复核范围沿 PR74 扩展为 VK `0x08..0xFE`。

| 文件 | SHA256 |
|---|---|
| 精确输入（已有 Buff 配对） | `0618b0db618224079a3d28fadff448b7c35cc463788554810861e6781be3264f` |
| 仅增加收包准入 | `99c4cd6d840eb9a61caadeb4da13bd6f5ac8672494a7e39525bda7430fbac657` |
| 最终累计候选 | `748406242876acbddfecbe7b74d528a1b78b775e984e725e0ac51df55b5bec82` |

三者长度均为 112607232 字节。仅更改已声明的收包调用、辅助代码零区、所属节虚拟长度、松键比较指令及 PE 校验和；其他字节与输入完全一致，包括现有 Buff 发送器和两组行处理器。

构建只需 Python 标准库，输出目录须不存在，未知输入拒绝：

```text
python scripts/client/mr-cumulative/build_candidate.py --input <PAIRED_BUFF_DIR>/DNF.exe --output-dir <NEW_CANDIDATE_DIR>
```

原生验证另需 `capstone`、`pefile`、`unicorn`。`--base` 所在目录需保留原 Buff 配方的 `build.json`，`--buff-base` 为该配方的原始 `92763484…` 客户端：

```text
python scripts/client/mr-cumulative/verify_native.py --base <PAIRED_BUFF_DIR>/DNF.exe --buff-base <BASE_927_EXE> --candidate-dir <NEW_CANDIDATE_DIR> --report <NEW_EVIDENCE_DIR>/native.json
python scripts/client/activeness-ui/Verify-ClientImage.py <NEW_CANDIDATE_DIR>/DNF.exe
```

实际已完成：C0/C1/C2 各全部 u16 操作码前后准入共 393216 次原生调用，新增分类项恰为 41；350 个调用点 ABI 样本、608 个松键场景、34 组既有 Buff 原生专项和 Windows `SEC_IMAGE_NO_EXECUTE` 映像检查通过。其余分类采用明确回落结构与样本核对，没有宣称执行所有 256 类的完整矩阵。

原生验证仍含明确的 STL、网络、物品查询和 UI 回调模拟边界，不替代联网、窗口绘制及战斗效果验收。工具不安装或运行客户端、不发布玩家通道、不更改 PVF，也不重建索引。正式发布继续遵循配对资源与授权流程。
