---
category: 功能修复
---

# Docker 部署支持独立的公网公告地址

## 变更

- `deploy/docker/instance.json` 新增可选字段 `public_host`：设置后，频道目录、账号接口公告和组队 UDP 中继对外公告该公网 IPv4；Docker 端口绑定和服务监听仍使用私网 `host_ip`，不会绑定到公网或通配地址。
- `public_host` 必须是全局单播 IPv4 字面量。域名、私网、回环、链路本地、组播、CGNAT 和保留地址均在 `prepare`/`generate` 阶段拒绝，因为协议里的地址字段为定长 ASCII。
- 未设置或为空时行为与之前逐字节一致，现有部署不受影响。
- 使用端口映射时内外端口必须一致；7001、17002、18111 仍只绑定本机回环，不应映射到公网。

## 验证

- `python3 -m unittest discover -s deploy/docker -p "test_*.py"`。
