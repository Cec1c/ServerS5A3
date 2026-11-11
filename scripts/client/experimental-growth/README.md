# 统一成长券客户端候选（实验性）

实验性、无诊断日志客户端候选。输入为正式 V212 累积基线 EXE，SHA256 `69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544`；按本配方复现的输出 EXE SHA256 `250d78d27e115be21240959db1415f7eddabb0c26c255c9ad452a8841f8abc0c`。

相对前一版成长券客户端配方（`growth.cpp` 与 7 个钩子保持不变），本版仅改变 `begin()` 的模板索引：把 `k=get32(state,32)-990033101` 改为 `k=state[39]?1:0`。所有成长券（商店新券 990033101，旧 990033102～990033105 保留兼容）一律按服务端预览 `b[39]` 的真实原始属性选择模板：`0` 走原生强化窗口（k0），`1..4` 走原生通用增幅窗口（k1）；`metadataID=990034000+k*100+state[38]` 不变。等级与异次元属性赋值业务由服务端守卫，客户端不改协议。旧按 ID 的模板逻辑已废弃。

必须与新版统一券的服务端与 PVF 配对（增幅态把券自身异次元属性+等级赋予目标；仅购买时是普通券）。

## 复现

前置：Visual Studio 2026（18.x）Community 的 x86 原生工具（`vcvars32.bat`），以及 `pefile`、`ordlookup`、`unicorn`（见 `requirements.txt`）。为保证字节级一致，建议 `set VCVARS32=<vcvars32.bat 全路径>` 指向已核对的编译工具链。

```text
cmd /c build.cmd
python build_candidate.py --base <69ea3a8b 基线 DNF.exe> --payload growth.payload --out <新 DNF.exe>
python verify_native.py --candidate <新 DNF.exe> --receipt <新 DNF.exe>.json --map growth.map --report <新回执.json>
```

`build_candidate.py` 只接受精确 69ea3a8b 基线，保持 7 个钩子（Getter/Gate/Validate/Action/HoverGate/FindSource/NativeSend）与入口点、导入表、TLS 不变，其余映射字节守恒，输出独立 EXE。`verify_native.py` 不携带 EXE/payload 二进制，默认读取本目录产物，也可用上述 CLI 显式路径。

## 验证范围与边界

`verify_native.py` 在 unicorn 模拟器执行 235 个有界用例：同 ID 990033101 在属性 0..4 × 等级 1..31 均选对原生强化/增幅窗口，旧 4 ID 按真实属性，旧 ID 属性 0 仍走强化（不新增客户端限制），另覆盖确认派发、不消耗负测、原生蹦床与后台引导接收。仿真只验证模板选择与 ABI/PE 结构，不是全部实机穷举；实际购买、使用、属性赋予、画面与稳定性仍须配对服务端/PVF 实机验收。

本目录不含任何 EXE、payload、`.obj/.map/.lib/.exp` 二进制；候选产物另存独立目录。
