# 四方向松键补丁：固定 v157 独立复建

构建脚本仅使用 Python 标准库，自带 PE 解析、x86 发码和 PE 校验和实现。无发布、安装、网络或生产替换功能，不导入旧机器仓库。部署到公开源码时，本目录可整体放到 `scripts/client/direction-key-release/`。

输入只接受 SHA256 `cd3b8976a586fb5870a4a0ec854e1fce86c6c50af78d5b6a816660a4130571d0`。重复打补丁、输入版本变化、节头不足、签名证书、ASLR 或 overlay 等不符合条件时立即失败。

```text
python -B scripts/client/direction-key-release/build_candidate.py --input PATH/TO/v157/DNF.exe --output-dir PATH/TO/OUTPUT
python -B scripts/client/direction-key-release/verify_candidate.py --input PATH/TO/v157/DNF.exe --candidate PATH/TO/OUTPUT/DNF-v157-direction-release-candidate.exe --output-dir PATH/TO/OUTPUT
```

验证器显式依赖 `unicorn==2.1.4`、`capstone==5.0.7`，可使用已有环境，或在独立虚拟环境通过下列命令安装。构建器不需要这些包。

```text
python -m pip install -r scripts/client/direction-key-release/requirements-validation.txt
```

预期候选 SHA256：`a751cccfe3fb9f7a3730393fdfb32d0546222b55a5a254fc4f2416f6869fbdf6`。

原始修复来源是用户提供的 2026-10-21 构建脚本和 v2 回执。脚本摘要 `37f2e4156119a6928336142d28b3866d208ce4d4134c100d6cea32e16e27ccde`，回执摘要 `64422bec76c4c44aa5a39bba07e975c652a9636a2e078c0e19363ff31d3baf34`。旧回执的完整 107 字节代码嵌入构建器，并在旧 VA 处逐字节验证生成结果；复建无需这两个旧文件。当前新增节从实际节表计算，不使用旧 VA 0x6EA9000。

功能范围：仅在既有输入设备、活动窗口和消息缓存检查通过、即将补位时，对 VK 0x25..0x28 查询 GetAsyncKeyState 最高位。已松开则清该 VK 缓存和 256 项映射表中的所有同 VK 当前帧字节，保留 previous，跳过本次补位计数；实际按住和其它键保留原 OR 路径。旧 IAT 槽继续通过当前已有包装器调用真实 API。

验证执行实际 x86 指令，关键 current/cache 写入未被 stub：3072 组矩阵，每组运行原循环、旧回执 helper 和新 helper；8 个单别名残留对照；全部验证寄存器、EFLAGS、栈、previous、设备/窗口 gate 和原生后处理入口。Win32 返回值及设备/窗口状态为夹具，原生后处理函数体未模拟，不替代输入法/游戏实机验收。原测试脚本/向量未随旧文件提供，本矩阵为明确重新构造，不能称为旧测试向量复用。Windows 会额外使用 ImageHlp 独立检查 PE 校验和，非 Windows 明确标记该项不可用。

`build-receipt.json` 保存所有允许原文件差异的偏移、前后字节及 40 个原节保留摘要；`validation-receipt.json` 保存矩阵定义、结果、复建一致性和限制。输出只存在所指定候选目录，公开发布需由主控独立复建并使用既有发布流程。
