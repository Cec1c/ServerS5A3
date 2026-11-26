# 武器幻化、组队初始化与渲染恢复源码

本目录公开 C01–C18 的原生客户端实现，以及 D01–D06 的队友职业初始化、设备丢失恢复、低内存保护、验证工具和累计构建记录。服务端与资源见上级目录。这里交付源码和精确版本配方，未提供新的通用客户端成品。

## 武器幻化与皮肤仓库

`weapon-skin/client-source/` 包含制作窗、跨职业选材及预览、右键确认、总览/武器/字体页、限速重试、选角及场景绑定、队友站街和战斗图层。保留既有来源通知；原始武器幻化配方沿用 duduqingrangyirang 的交付来源，后续累计适配见 `build_port.py`，不以本次公开改写原来源。

原始精确差量 `weapon-skin-95.exe-delta.gz` 仅接受 SHA-256 `965601a2f504bdab050c97a4064eb91377a300038c3f8121a5c6d34bbce275aa`，输出 `8cfaa6247f73a0c43328e9b1dad8d83a0bb5474a56cf4e6500777f8581ea8fe4`。使用新的输出路径：

```text
python weapon-skin/apply_client_patch.py original.exe weapon-skin/weapon-skin-95.exe-delta.gz candidate.exe
```

`build_port.py` 公布另一条已使用的静态移植过程：重定位既有 IAT、独立 UI/场景状态、原生入口适配、三份 C++ 编译、目录提取、节表合并和逐字节保留验证。它仅接受 `2111a6ea61395a51ed96ef5b3701f78e1b61b638d56c23f9168493ccb2df935d`，不能套用其他 EXE：

```text
python weapon-skin/build_port.py --input exact-2111.exe --work new-build --output-dir new-candidate --vcvars "C:\Program Files\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars32.bat"
```

MSVC 14.44 x86、Python 3.11+、pefile 和 Capstone 为此路径的依赖。编译器/链接器时间戳可能影响新产物身份；每次保留新的构建回执，不能直接宣称与历史成品逐字节一致。输出不自动安装或发布。

## 队友职业初始化与黑屏恢复

`party-render/build_party_v4.py` 在实际副本模式 2、远端技能页就绪后调用职业原生初始化。副本 epoch、去重、重试和 update 补查处理通知/技能页的先后顺序。13 组 Unicorn 用例覆盖身份、时序和重复调用。

`build_blackscreen.py` 包含 12 个原生挂点：统一设备恢复路径，检查 VB/IB 创建结果，部分恢复失败后的有界重试，SpriteBatch 空缓冲保护、Upscale 纹理回退、CNImg 与 ScreenEffect 低内存保护。`verify_blackscreen.py` 执行修改范围、PE、反汇编和 19 组 Unicorn 验证，覆盖 T01–T20，T11/T12 合并为一组。

```text
python -m pip install -r requirements.txt
python party-render/build_party_v4.py --input exact-d798.exe --output new-party.exe --receipt new-party.json
python party-render/verify_party_v4.py --baseline exact-d798.exe --candidate new-party.exe --build-receipt new-party.json
python party-render/build_blackscreen.py --input new-party.exe --output new-render.exe --receipt new-render.json
python party-render/verify_blackscreen.py --baseline new-party.exe --candidate new-render.exe --build-receipt new-render.json --report new-render-verify.json
```

精确输入链为 D798 `d798b52293f8994926b2574db3dee075f07136a2c5096636d42f2844f8b82f1b` → D777 `d7774894a291d3596f2f3b5c3833bafdecc9ba5c7eb95ea5e2ee98ec2a6135da` → FE6 `fe6a606f53ff4a496c56e78395d3288c897fbdc5a245441718414796c896bce0`。这些是版本身份，不是本目录提供的下载产物。

`party-render/lineage/` 和 `build_formal_release.py` 公开 c2→D798→D777→FE6 的增量范围、来源核验与固定输出包装流程。历史 47e 前驱包含其他未选玩法，且没有完整可公开重建链；因此该包装器明确要求操作者自行持有列出的四份精确前驱。范围清单只有偏移/长度/摘要，不携带这些其他玩法的二进制内容。它不能从公开 V217 独立生成 FE6。对其他版本的移植须重新核对原生锚点和全部配套资源。

## 已有组队及模型实现

现代队伍房间登记、迟到 mode1 补登记和回执、网络生命周期、原生准入、Buff 绑定及模型只读取证的实现已在公开主线 `scripts/client/`。`party-room-lifecycle-registration` 的构建器和验证器在本次基线已齐全，与私有版本 Git blob 相同，早期缺件记录不再代表当前状态。服务端本批剩余增量另见相应说明。

离线验证不等于渲染实机验收。历史配对的用户验收结果不自动适用于新的编译器、EXE/PVF/NPK 组合；应分别验证首次进图、换房、重进、返城、多人外观与资源恢复。
