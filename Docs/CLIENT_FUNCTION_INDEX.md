# 客户端函数与协议索引

本索引帮助从服务端问题定位到匹配客户端的代码，再用客户端约定约束服务端修复。入口可能在 EXE、DLL 或函数中间的一段代码；文件名、导出名和工具自动命名都不能单独证明业务含义。

本索引经授权公开，允许随仓库同步。仅收录模块摘要、地址、脱敏分析结论及公共代码依据；客户端文件、完整反编译、玩家报文、截图、私人环境资料仍留在被忽略的目录。此处不声明任何远端服务版本或 PR/MR 合并状态。

## 查找与版本约束

1. 按现象或请求 ID 查下表，注意客户端请求、应答和通知是不同方向/类别。
2. **先匹配模块完整 SHA256，再使用 RVA。** 文件名或显示版本相同仍可能有不同补丁；摘要不同就新建版本记录，不沿用旧地址。
3. 用对应模块的基址加 RVA 定位。表中 VA 是 PE 首选基址下的静态地址；运行时若重定位，应使用实际模块基址。RVA 不是文件偏移。
4. 阅读条目的证据范围及关联服务端回归；未知语义先取证，不据此放宽房间、身份或资产校验。

首次核验：2026-10-14。机器可读数据：[paired-20261013.json](client-functions/paired-20261013.json)。`paired-20261013` 是本索引的版本标签，不是官方版本号或整个客户端/PVF 的兼容性认证。各模块单独按摘要识别，不要求每次查询都加载全部 DLL。

| 模块 | 字节数 | 架构 / PE 首选基址 | SHA256 |
| --- | ---: | --- | --- |
| DNF.exe | 111734784 | x86 / `0x00400000` | `2af985b6e344e5fdbb78f6598d40924f152af924a156f5d45b671bed350a9675` |
| ijl15.dll | 352256 | x86 / `0x60000000` | `41d37bfb0a39017497e0aa373dc01f71ac9c5046c75f7cd269bee52112a5f3a1` |
| RS.dll | 114688 | x86 / `0x10000000` | `5269d40e0cf1770f90f9ed907d2a179e23e405d23f694c9454c0ed977a352000` |

## 已定位的 DNF.exe 代码

下列名称是分析标签，不是恢复的源码符号。`function_entry` 表示已见函数序言及调用/注册依据；除 JSON 明确给出结束 RVA 的条目外，不声称已经确定完整函数边界。

| ID | 用途 | 方向与协议 ID（十进制） | RVA / 静态 VA | 位置性质 |
| --- | --- | --- | --- | --- |
| CF-001 | 怪物死亡请求的 owner 与贡献列表 | C→S command 39 | `0x02358b10` / `0x02758b10` | 函数内片段，入口未定 |
| CF-002 | 金币携带上限升级请求 | C→S command 978 | `0x01993000` / `0x01d93000` | 函数入口 |
| CF-003 | 携带上限升级应答 | S→C command ACK 978 | `0x01bd4b00` / `0x01fd4b00` | 函数入口 |
| CF-004 | 持久携带档位通知 | S→C notification 947 | `0x01c3ae60` / `0x0203ae60` | 函数入口 |
| CF-005 | 好友列表与上线提示 | S→C notification 1371 | `0x01c1fe50` / `0x0201fe50` | 函数入口 |
| CF-006 | 包缓冲追加 1 字节 | 通用写入器 | `0x0334f390` / `0x0374f390` | 函数入口 |
| CF-007 | 包缓冲追加 2 字节 | 通用写入器 | `0x0334f3c0` / `0x0374f3c0` | 函数入口 |
| CF-008 | 包缓冲追加 4 字节 | 通用写入器 | `0x0334f3f0` / `0x0374f3f0` | 函数入口 |
| CF-009 | 包缓冲追加 8 字节 | 通用写入器 | `0x0334f420` / `0x0374f420` | 函数入口 |

### CF-001：APC 击杀后的死亡结算

- 静态 VA `0x02758b39` 将 owner 缺省设为 `0xffff`；后续条件分支可以替换它。`0x02758b4b` 开始构造 command 39，分别写入目标和 owner。
- 静态 VA `0x02758f29` 另取贡献数量，通过 CF-006 写入，再循环使用 CF-007/009，按 `2 + 2 + 8 + 2` 字节写贡献项。owner 和贡献数量分别生成，不能由 `owner == 0xffff` 推断贡献列表必为空。
- 公共回归交叉验证了友好 APC 单独/混合击杀、重复死亡、发送失败重试及拒绝路径。服务端仍需验证 APC 已宣告且存活、目标为当前房间合法怪物；本条不是所有 sentinel 报文的放行依据。
- 公共依据：[MR !19](https://gitgud.io/16ww/servers5a3/-/merge_requests/19)、[固定提交中的回归](https://gitgud.io/Cecic/servers5a3/-/blob/2607d652fa9c44ceb9b6a5b61a807f0f3f843e3b/go-server/internal/services/dnfbridge/dungeon_friendly_apc_death_test.go)，尤其 `TestOfficialFriendlyAPCSentinelDeathAndReplay` 与 `TestOfficialFriendlyAPCDeathRejectsUnprovenRequests`。
- 限制：这是函数内部两个窗口；没有得到该函数的完整 REA 伪代码或完整调用图。公共索引不附私人现场日志，也不把服务端回归称为本次游戏验收。

### CF-002～004：金币携带上限

- CF-002 发 command 978（`0x3d2`），传出 14 字节；本函数只显式赋值末字节为档位加一。不要将其余栈字节当作稳定字段或要求固定为零。
- CF-003 读取 `u8 stage + u32 result`，ACK 成功标志通过调度参数传入。服务端对应整个 ACK body 为 `u8 flag + u8 stage + u32 result`。注册锚点静态 VA `0x01fe86c1` 同时引用处理入口和 command 978。
- CF-004 读取一字节 stage；注册锚点静态 VA `0x020469b4` 绑定 notification 947。CF-003/004 共同写入档位全局 `0x057b7008`（RVA `0x053b7008`，是数据位置，不是函数）。
- 公共依据：[MR !8](https://gitgud.io/16ww/servers5a3/-/merge_requests/8)、[请求/应答适配](https://gitgud.io/Cecic/servers5a3/-/blob/d57711c6e2182ab2989a08bd9a180d03bc7fffa1/go-server/internal/services/dnfbridge/carry_gold_upgrade.go)、[回归](https://gitgud.io/Cecic/servers5a3/-/blob/d57711c6e2182ab2989a08bd9a180d03bc7fffa1/go-server/internal/services/dnfbridge/carry_gold_upgrade_test.go)。重复请求不重复扣费、失败应答和重新选角刷新各有测试。
- 限制：字段读取和升级入口不能证明所有金币入口都已遵守携带上限；客户端 UI 表现、服务器事务与全入口额度政策应分别验证。

### CF-005：重复好友上线提示

- 静态 VA `0x02032a7e` 将 notification 1371（`0x55b`）注册到 `0x0201fe50`。处理器先记录好友集合是否已有内容，再读取本次行数。
- 静态 VA `0x02020098` 的提示分支检查已有集合、频道非零、行数不大于一及名字非空，并引用 DSTR `100000083`。重复发送相同单行在线列表可能再次触发提示。
- 公共依据：[MR !12](https://gitgud.io/16ww/servers5a3/-/merge_requests/12)、[固定提交中的回归](https://gitgud.io/Cecic/servers5a3/-/blob/51a19f30fa47276a93be9bf3e805b83bbf77964a/go-server/internal/services/dnfbridge/account_friend_unchanged_presence_test.go)：相同投影去重，同时保留在线状态变化、选角、重连和发送失败后的刷新。
- 限制：这不是停止发送好友列表的依据，也未建立其他社交通知的完整语义。

### CF-006～009：报文宽度辅助定位

四个封装先检查缓冲启用标记，再分别增加 `1/2/4/8` 字节长度，以同一长度调用缓冲写入器 `0x038c7f70`。通过调用顺序可复核 CF-001 的字段宽度；宽度本身不证明字段含义、符号性、单位或业务可信度。

## DLL 导出导航：业务语义待核实

下表只证明 PE 导出目录中的符号、序号和 RVA。没有把导出名解释成网络协议或补丁职责；`function_start_rva` 留空表示尚未核实实际函数边界。所列导出均不是转发导出。

| ID | 模块 | 导出名称 | 序号 | RVA |
| --- | --- | --- | ---: | --- |
| CF-010 | ijl15.dll | ijlErrorStr | 6 | `0x0002b120` |
| CF-011 | ijl15.dll | ijlFree | 3 | `0x000291f0` |
| CF-012 | ijl15.dll | ijlGetLibVersion | 1 | `0x0002b110` |
| CF-013 | ijl15.dll | ijlInit | 2 | `0x00028a60` |
| CF-014 | ijl15.dll | ijlRead | 4 | `0x00029be0` |
| CF-015 | ijl15.dll | ijlWrite | 5 | `0x0002a700` |
| CF-016 | RS.dll | ClearRSInfo | 1 | `0x00001020` |
| CF-017 | RS.dll | ClearRSInfo2 | 2 | `0x00001020` |
| CF-018 | RS.dll | PutRSInfo | 3 | `0x00001000` |
| CF-019 | RS.dll | PutRSInfo2 | 4 | `0x00001000` |

RS.dll 有两组不同导出名指向相同 RVA，不应重复推断为四套实现。ijl15.dll 的导出名也不能排除入口、内部函数或其他加载路径上的附加逻辑。后续需要结合导入者、交叉引用及实际函数体取证。

## 离线复核与工具选择

本次使用 pefile `2023.2.7` 读取 PE 元数据，Capstone `5.0.7` 静态复核指令。JSON 为 14 个代码窗口保存 `rva`、`file_offset`、字节数和 SHA256；窗口长度不等于函数长度。指令摘要匹配只能证明原始字节一致，语义仍需读相关指令和回归。

持有匹配客户端时，可将下段保存为本机 `verify_index.py`，在仓库根目录执行 `python verify_index.py "客户端目录"`。只读三个文件并核对摘要，不执行客户端；该检查不验证反编译语义或游戏行为。

```python
import hashlib
import json
import sys
from pathlib import Path

index = json.loads(Path("Docs/client-functions/paired-20261013.json").read_text(encoding="utf-8"))
client = Path(sys.argv[1])
for module in index["modules"]:
    path = client / module["file_name"]
    with path.open("rb") as stream:
        digest = hashlib.sha256()
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
        if path.stat().st_size != module["size_bytes"] or digest.hexdigest() != module["sha256"]:
            raise SystemExit(f"版本不匹配：{module['file_name']}")
        for entry in index["entries"]:
            if entry["module"] != module["id"]:
                continue
            for anchor in entry["anchors"]:
                stream.seek(int(anchor["file_offset"], 16))
                data = stream.read(anchor["length_bytes"])
                if len(data) != anchor["length_bytes"] or hashlib.sha256(data).hexdigest() != anchor["sha256"]:
                    raise SystemExit(f"代码窗口不匹配：{entry['id']} {anchor['rva']}")
    print(f"摘要匹配：{module['file_name']}")
```

已知地址的小窗口优先使用 Capstone；需要伪代码、字符串或跨函数关系时使用 REA 配合已配置的 IDA/Ghidra 后端。反汇编从已核实的指令边界开始，不从任意搜索命中的前若干字节推断函数。REA 的 IDA 提供者依赖对应 MCP 后端，不因使用 REA 就移除该依赖。

当前这份大 EXE 的 REA/IDA 全量导入曾超时，因此本索引的 DNF 条目来自有界静态核对及既有服务端回归，没有声称完整反编译已经成功。需要完整分析时再评估可复用数据库或其他分析路线。默认不启动游戏/登录器；REA 会话结束显式关闭，并核实所属 worker 释放。

## 后续增补规则

- 同一语义沿用稳定 ID；新模块摘要新建版本 JSON，保留旧记录及其适用范围。不同版本位置尚未对应时，不猜测复用 ID 的语义关系。
- 每条记录至少包含：模块摘要、架构/基址、RVA、位置性质、命名来源、用途、协议方向/类别/ID（若已知）、证据等级、复核日期、限制；有公开修复时附固定提交、文件及测试名称。
- `export_only` 只证明导出位置；`static` 证明所述指令/调用；`static_and_server_regression` 额外关联既有服务端回归，不意味着原生游戏验收已完成。推测放入“待核实”说明，不冒充已确认条目。
- `function_start_rva`/`function_end_rva_exclusive` 未确定时使用 `null`，结束地址为不含端点的 RVA。代码窗口可位于注册表调用处等相关位置，不必属于该条目主函数。
- 补充公开结论时脱敏；不提交私人绝对路径、账号/角色/主机信息、密钥、原始网络报文、二进制、完整伪代码或数据库。原始证据保存在被忽略的 `.cache/` 或 `local-notes/`，公开文件无需依赖这些目录才能理解结论。
- 文档变更核对 JSON、地址换算、源文件/测试引用、链接和公开 diff。服务端测试按实际代码改动运行；不因索引更新而宣称重新部署或重新验收。
