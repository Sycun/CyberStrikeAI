<div align="center">
  <img src="images/logo.png" alt="CyberStrikeAI" width="144">
  <h1>CyberStrikeAI</h1>
  <p><strong>交代目标，让智能体执行、汇报，并在需要时向你请示。</strong></p>
  <p><strong>中文</strong> · <a href="README.md">English</a></p>
  <p>
    <a href="https://github.com/Sycun/CyberStrikeAI/actions/workflows/pi-runtime.yml"><img src="https://github.com/Sycun/CyberStrikeAI/actions/workflows/pi-runtime.yml/badge.svg?branch=main" alt="Pi runtime CI"></a>
    <a href="https://github.com/Sycun/CyberStrikeAI/actions/workflows/self-update.yml"><img src="https://github.com/Sycun/CyberStrikeAI/actions/workflows/self-update.yml/badge.svg?branch=main" alt="Build and regression CI"></a>
  </p>
</div>

CyberStrikeAI 是基于 **Go + Pi** 的网络安全智能体工作台。你描述希望完成的工作，智能体理解意图、规划步骤、调用工具并保存证据；执行中持续汇报进展，遇到缺失信息或需要你决定的问题时主动请示，得到答复后从原进度继续。

本仓库由 **Sycun** 维护，基于 [AIPentest/CyberStrikeAI](https://github.com/AIPentest/CyberStrikeAI) 持续开发，保留上游历史、许可证与版权声明。当前版本适合团队内测，新功能入口为 **「深度测评 → 交代任务」**。

[快速上手](#quick-start) · [体验任务闭环](#delegation) · [主要能力](#capabilities) · [架构](#architecture) · [文档](#documentation) · [反馈](#contributing)

<a id="quick-start"></a>

## 快速上手

### 1. 准备环境

| 依赖 | 要求 | 用途 |
| --- | --- | --- |
| Go | 1.25+，以 [go.mod](go.mod) 为准 | 构建服务 |
| Node.js 与 npm | Node.js 22.19+ | 构建和运行 Pi Worker |
| Python | 3.10+，可创建 venv | 启动脚本及 Python 工具依赖 |
| Git | 可用 | 获取源码与更新 |

还需要一个支持工具调用的模型渠道，以及本次任务实际使用的工具或 MCP 服务。仓库提供工具配方，**不会自动安装所有安全工具**。首次构建需要访问 Go、npm 和 Python 依赖源。

### 2. 获取源码并启动

以下命令用于 macOS / Linux 的 Bash 环境：

```bash
git clone --branch main https://github.com/Sycun/CyberStrikeAI.git
cd CyberStrikeAI
cp config.example.yaml config.yaml
```

本机体验时，先将 `config.yaml` 中已有的 `server.host` 改为 `127.0.0.1`，保留其他配置。然后启动：

```bash
bash run.sh --http
```

脚本会准备 Pi Worker、创建 Python 虚拟环境、安装依赖并构建服务。默认访问 [http://127.0.0.1:8080/](http://127.0.0.1:8080/)；端口以配置和终端输出为准。

不加 `--http` 时，`run.sh` 默认使用 HTTPS；未配置证书时使用本地自签证书。团队共享部署的监听地址、证书和访问控制见[部署指南](docs/zh-CN/deployment.md)。

### 3. 完成首次配置

1. 使用终端 `ADMIN SETUP REQUIRED` 提示中的初始 `admin` 密码登录，然后修改密码。
2. 在 **系统设置 → 基本设置 → AI 通道配置** 中填写提供商、Base URL、API Key 和模型，并设为默认渠道。示例配置中的凭据只是占位符。
3. 安装本次任务需要的本地工具，或接入可用的外部 MCP 服务。确认工具能正常运行后，再将它交给智能体使用。
4. 多人共用服务时，在 **平台权限 → 添加成员** 中为队友创建各自的账号和权限。

完整配置以 [config.example.yaml](config.example.yaml) 为准；模型渠道、本地模型与上下文设置见[配置参考](docs/zh-CN/configuration.md)和[配置画像](docs/zh-CN/configuration-profiles.md)。

<details>
<summary><strong>手动构建与启动</strong></summary>

完成上述配置后，也可以分别构建 Pi Worker 和 Go 服务：

```bash
bash scripts/build-pi-runtime.sh
go build -o cyberstrike-ai ./cmd/server
./cyberstrike-ai -config config.yaml --http
```

部署需要运行时源码及匹配的 Pi Worker 构建包，或在目标机器重新构建；只复制 Go 二进制不足以运行智能体。Python 工具依赖需另行准备。详见 [Pi 运行时说明](docs/zh-CN/pi-runtime.md)。

</details>

<a id="delegation"></a>

## 体验「交代工作 → 执行 → 汇报 → 请示 → 继续」

进入 **深度测评 → 交代任务**，选择项目（可选）、允许使用的工具、模型与预算，然后描述期望的结果。例如，在材料栏列出可读取的本地报告文件后：

> 帮我整理这个项目已有的测试材料，报告已验证的结果和证据缺口。只读取材料栏列出的本地文件，不访问新的目标。按模块组织报告，每完成一个阶段告诉我做了什么、还缺什么；需要我决定时直接问我。

```mermaid
flowchart LR
    A[描述目标] --> B[理解意图]
    B --> C[规划与执行]
    C --> D[阶段汇报]
    D --> C
    D --> E[验收与交付]
    B -->|信息不足| F[主动请示]
    C -->|需要决定| F
    F --> G[用户答复]
    G -->|接单澄清| B
    G -->|执行决策| C
```

- **描述不完整也能接单**：先形成任务理解草稿；关键目标、范围或验收条件不明确时，提出具体问题。
- **明确后自主执行**：使用所选工具推进工作，保存步骤状态、工具结果和执行记录。
- **阶段报告可核对**：展示已完成、未完成、阻碍和下一步，关联执行证据。
- **等待只影响相关工作**：某一步需要答复时，已有的独立步骤仍可继续；没有可继续的工作后释放运行资源。
- **答复后接着做**：点击「答复并继续」，恢复原步骤上下文；重复提交相同答复不会重复执行。
- **问答与进度持久化**：关闭页面不丢失任务；服务重启后仍可查看待答问题和已保存的结果。

普通请示与工具审批分开处理。当前闭环入口是工作台与 API；聊天和机器人自动分流、跨项目名称消歧、站外通知与 ARTEX 统一适配器属于后续工作。

使用方式、恢复语义和 API 示例见[任务委派与主动请示](docs/zh-CN/task-delegation.md)。

<a id="capabilities"></a>

## 主要能力

| 能力 | 可以做什么 | 说明 |
| --- | --- | --- |
| Pi 智能体 | 单智能体、Deep、Plan-Execute、Supervisor 编排 | [智能体与角色](docs/zh-CN/agent-and-role-guide.md) |
| 持久任务 | 规划、依赖步骤、有限并发、预算、暂停恢复与验收 | [任务委派](docs/zh-CN/task-delegation.md) |
| 工具与 Skills | YAML 工具配方、MCP 接入、角色工具集合、按需加载技能 | [工具](tools/README.md) · [Skills](skills/README.md) |
| 可视化工作流 | 组合智能体、工具、条件和人工审批节点 | [工作流](docs/zh-CN/workflow-graph.md) |
| 知识与证据 | RAG 检索、项目事实、工具记录、报告和攻击链分析 | [知识库](docs/zh-CN/knowledge-base.md) |
| 项目工作空间 | 资产、漏洞、对话、批量任务与跨会话资料管理 | [资产管理](docs/zh-CN/asset-management.md) |
| 人工监督 | RBAC、工具审批、调用拦截和审计日志 | [安全模型](docs/zh-CN/security-model.md) |
| 外部集成 | Burp Suite、浏览器扩展、机器人和外部 MCP | [插件](#integrations) · [MCP](docs/zh-CN/mcp-federation.md) |

专用操作还包括 [WebShell](docs/zh-CN/webshell.md) 和 [C2](docs/zh-CN/c2.md)。按任务配置实际需要的能力；工具配方不代表对应程序已经安装或通过了当前环境的验收。

<a id="architecture"></a>

## 架构

| 组成 | 职责 |
| --- | --- |
| **Go 服务与任务引擎** | 管理任务状态、依赖、预算、用户问答、工具执行与证据 |
| **Pi 运行时** | 模型交互、智能体工具循环、会话、上下文压缩、steering 与工具检索 |
| **工具网关** | 将调用交给 MCP 或本地工具，沿用角色、权限、审批和执行监控 |
| **SQLite 与 Pi 会话** | 分别保存业务状态与证据、模型会话与压缩记录 |
| **Web 工作台** | 展示任务、问题、阶段报告、执行轨迹与项目资料 |

当前仅使用 **Pi 1.5.2**，Eino 执行内核与依赖已移除。深度测评由 Go 持久任务引擎控制执行，Pi 提供单步模型提案；它与聊天智能体的自动工具循环共用运行时，但各自管理任务生命周期。

<a id="integrations"></a>

## 插件与界面

| 集成 | 使用方式 | 文档与下载 |
| --- | --- | --- |
| Burp Suite | 将选定 HTTP 请求交给 CyberStrikeAI 分析 | [安装说明](plugins/burp-suite/cyberstrikeai-burp-extension/README.zh-CN.md) · [JAR](plugins/burp-suite/cyberstrikeai-burp-extension/dist/cyberstrikeai-burp-extension.jar) |
| Chrome / Edge | 在 DevTools 的 CyberStrikeAI 标签页捕获并发送请求 | [安装说明](plugins/browser-extension/cyberstrikeai-browser-extension/README.zh-CN.md) · [ZIP](plugins/browser-extension/cyberstrikeai-browser-extension/dist/cyberstrikeai-browser-extension.zip) |

<details>
<summary><strong>查看工作台界面</strong></summary>

<table>
<tr>
<td width="50%"><img src="images/dashboard.png" alt="系统仪表盘"><br>系统仪表盘</td>
<td width="50%"><img src="images/dark.png" alt="深色主题"><br>深色主题</td>
</tr>
<tr>
<td width="50%"><img src="images/web-console.png" alt="对话控制台"><br>对话控制台</td>
<td width="50%"><img src="images/knowledge-base.png" alt="知识库"><br>知识库</td>
</tr>
<tr>
<td width="50%"><img src="images/mcp-management.png" alt="MCP 管理"><br>MCP 管理</td>
<td width="50%"><img src="images/plugins.png" alt="Burp Suite 插件"><br>Burp Suite 插件</td>
</tr>
</table>

仓库中的界面截图用于展示已有功能；任务委派的新入口和操作流程以[使用说明](docs/zh-CN/task-delegation.md)为准。

</details>

## 更新与迁移

新安装的 [config.example.yaml](config.example.yaml) 已将更新源设为本仓库：

```yaml
update:
  repo: https://github.com/Sycun/CyberStrikeAI
```

已有安装请在 **系统设置 → 一键更新** 核对来源。`update.repo` 留空仍会回到官方上游，不会自动选择这个个人维护版本。更新流程会构建匹配源码的 Pi Worker；部署步骤见[部署指南](docs/zh-CN/deployment.md)。

从 Eino 版本迁移时，先备份运行配置和数据，并完成或导出旧工作流：**旧 Eino 工作流二进制检查点不能在新版直接续跑**。旧 API 路由、配置键和历史记录只保留数据兼容，不提供旧引擎切换。详见[迁移说明](docs/zh-CN/pi-runtime.md)。

## 验证状态

当前版本已进行 Go 全量与竞态测试、Pi SDK 集成测试、前端回归及本地界面检查。隔离的真实模型验收走通了「模糊任务 → 澄清 → 用户答复 → 回声工具执行 → 阶段报告 → 完成」，输入和工具结果均为合成材料。

这些验证支持团队内测；复杂长任务、各模型渠道的兼容性，以及意图识别准确率、成本和效果仍需继续评估。最新检查见 [GitHub Actions](https://github.com/Sycun/CyberStrikeAI/actions)。

<a id="documentation"></a>

## 文档导航

| 你想做什么 | 从这里开始 |
| --- | --- |
| 部署并配置模型 | [部署](docs/zh-CN/deployment.md) · [配置](docs/zh-CN/configuration.md) · [排错](docs/zh-CN/troubleshooting.md) |
| 使用任务委派 | [操作与 API](docs/zh-CN/task-delegation.md) · [设计背景](docs/zh-CN/task-delegation-design.md) |
| 调整智能体和工具 | [Pi 运行时](docs/zh-CN/pi-runtime.md) · [智能体与角色](docs/zh-CN/agent-and-role-guide.md) · [工具配方](tools/README.md) |
| 接入自己的应用 | [API 参考](docs/zh-CN/api-reference.md) · [API 示例](docs/zh-CN/api-recipes.md) · [MCP](docs/zh-CN/mcp-federation.md) |
| 维护团队服务 | [安全加固](docs/zh-CN/security-hardening.md) · [运维手册](docs/zh-CN/runbooks.md) |
| 参与开发 | [开发者指南](docs/zh-CN/developer-guide.md) · [测试](docs/zh-CN/testing.md) · [仓库说明](docs/REPOSITORY_GUIDE.md) |

全部专题见[中文文档](docs/zh-CN/README.md)与[双语索引](docs/README.md)。

<a id="contributing"></a>

## 反馈与贡献

请在[本仓库 Issues](https://github.com/Sycun/CyberStrikeAI/issues)反馈内测问题。附上提交版本、操作系统、模型协议/渠道类型、复现步骤和脱敏日志；不要上传 API Key、登录凭据或真实项目的敏感材料。欢迎提交工具适配、体验改进与回归用例的 PR。

本仓库与官方上游独立发布；上游项目及其社区信息请参阅 [AIPentest/CyberStrikeAI](https://github.com/AIPentest/CyberStrikeAI)。感谢原项目作者、贡献者，以及 Pi、MCP 和其他开源项目。

## 许可证与使用范围

项目遵循 [Apache License 2.0](LICENSE)，第三方组件保留各自许可证与声明。仅用于自有系统或已获明确授权的安全工作；使用者应遵守授权范围和适用法律。安全问题报告方式见 [SECURITY.md](SECURITY.md)。
