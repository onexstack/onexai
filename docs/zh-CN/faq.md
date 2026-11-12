# FAQ

**为什么 `oai-chat-apiserver` 没有 `controlplane.kubeconfig` 就拒绝启动？**
命令授权与 PeerBinding 是声明式资源。再读一份 SQL 会成为第二个事实源，因此控制面
是必需的。值班组已改为 chat 服务 + PostgreSQL 自有的命令式资源，不再从控制面读取。

**可以不用 duty 后端吗？**
可以。不设置 `duty.provider`（或设为 `none`），也不配置 `backend.regen-endpoint`。
duty store 保持为空，工单仅保存在本地。

**用 PostgreSQL 还是 MySQL？**
主路径是 PostgreSQL（三个 schema）。MySQL 需设置 `coredb.dialect: mysql`，表名会带
服务前缀。

**端口是什么？**
默认值见 `configs/examples/`：agent apiserver `:8080`，chat apiserver `:8082`
（chart 中 MCP 监听 `:8091`），MCP server `:8080`，declserver `:6444`。

**密钥放哪里？**
不要入库。使用环境变量（`OAI_<SERVICE>_<PATH>`）、Helm chart 的 Kubernetes Secret，
或你的密钥管理服务。

**为什么这里没有 `onexai`/`harness` CLI？**
CLI 是独立的私有工具。本仓库只包含服务端平台及其公共包。

**工具提示 “requires SRE role”？**
这是工具自身的授权检查。把角色名单与命令授权配置成声明式资源；空名单意味着没有人拥有
该角色。
