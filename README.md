<div align="center">
  <img src="images/logo.png" alt="CyberStrikeAI" width="144">
  <h1>CyberStrikeAI</h1>
  <p><strong>Describe the goal. Let an agent do the work, report progress, and ask when it needs your judgment.</strong></p>
  <p><a href="README_CN.md">中文</a> · <strong>English</strong></p>
  <p>
    <a href="https://github.com/Sycun/CyberStrikeAI/actions/workflows/pi-runtime.yml"><img src="https://github.com/Sycun/CyberStrikeAI/actions/workflows/pi-runtime.yml/badge.svg?branch=main" alt="Pi runtime CI"></a>
    <a href="https://github.com/Sycun/CyberStrikeAI/actions/workflows/self-update.yml"><img src="https://github.com/Sycun/CyberStrikeAI/actions/workflows/self-update.yml/badge.svg?branch=main" alt="Build and regression CI"></a>
  </p>
</div>

CyberStrikeAI is a cybersecurity agent workbench built with **Go + Pi**. Describe the outcome you want: the agent interprets your intent, plans steps, calls tools, and records evidence. It reports progress as it works, asks for missing information or decisions, and continues from the saved state after you answer.

This repository is maintained by **Sycun**, building on [AIPentest/CyberStrikeAI](https://github.com/AIPentest/CyberStrikeAI) with the upstream history, license, and copyright notices preserved. The current version is ready for team trials. Start the new workflow from **Deep Assessment → Delegate work**.

[Quick start](#quick-start) · [Delegate your first task](#delegation) · [Capabilities](#capabilities) · [Architecture](#architecture) · [Documentation](#documentation) · [Feedback](#contributing)

<a id="quick-start"></a>

## Quick start

### 1. Prepare the environment

| Dependency | Requirement | Purpose |
| --- | --- | --- |
| Go | 1.25+; see [go.mod](go.mod) | Build the server |
| Node.js and npm | Node.js 22.19+ | Build and run the Pi worker |
| Python | 3.10+, with venv support | Launcher and Python tool dependencies |
| Git | Available on PATH | Clone and update the source |

You also need a model channel that supports tool calling, plus the tools or MCP services required by your task. The repository contains tool recipes; it **does not install every security tool automatically**. The first build needs access to Go, npm, and Python dependency sources.

### 2. Clone and start

The following commands use Bash on macOS or Linux:

```bash
git clone --branch main https://github.com/Sycun/CyberStrikeAI.git
cd CyberStrikeAI
cp config.example.yaml config.yaml
```

For a local trial, change the existing `server.host` in `config.yaml` to `127.0.0.1`, keeping the other settings. Then start the server:

```bash
bash run.sh --http
```

The script prepares the Pi worker, creates a Python virtual environment, installs dependencies, and builds the server. Open [http://127.0.0.1:8080/](http://127.0.0.1:8080/) by default; use the port shown in your configuration and terminal output.

Without `--http`, `run.sh` uses HTTPS and generates a local self-signed certificate when none is configured. For a shared deployment, see the [deployment guide](docs/en-US/deployment.md) for listening addresses, certificates, and access controls.

### 3. Complete the initial setup

1. Sign in with the initial `admin` password shown under `ADMIN SETUP REQUIRED` in the terminal, then change it.
2. In **System settings → Basic → AI channel config**, enter the provider, Base URL, API key, and model, then select a default channel. Credentials in the example configuration are placeholders.
3. Install the local tools needed for your task, or connect an available external MCP service. Verify that each tool works before assigning it to an agent.
4. For a shared service, create separate accounts and permissions for teammates under **Platform Access → Add Member**.

Use [config.example.yaml](config.example.yaml) as the configuration reference. See [configuration](docs/en-US/configuration.md) and [configuration profiles](docs/en-US/configuration-profiles.md) for model channels, local models, and context settings.

<details>
<summary><strong>Build and run manually</strong></summary>

After completing the configuration above, you can build the Pi worker and Go server separately:

```bash
bash scripts/build-pi-runtime.sh
go build -o cyberstrike-ai ./cmd/server
./cyberstrike-ai -config config.yaml --http
```

A deployment needs the runtime source and a matching Pi worker bundle, or a fresh build on the destination machine. Copying only the Go binary is not enough to run agents. Prepare Python tool dependencies separately. See the [Pi runtime guide](docs/en-US/pi-runtime.md).

</details>

<a id="delegation"></a>

## Delegate, execute, report, ask, and continue

Open **Deep Assessment → Delegate work**, select a project if needed, choose the available tools, model, and budget, then describe the desired outcome. For example, after listing readable local report files in the materials field:

> Organize this project's existing assessment materials into a report of verified results and evidence gaps. Read only the local files listed in the materials field; do not access new targets. Organize the report by module. At each milestone, tell me what is done and what remains. Ask me directly when a decision is needed.

```mermaid
flowchart LR
    A[Describe the goal] --> B[Interpret intent]
    B --> C[Plan and execute]
    C --> D[Report progress]
    D --> C
    D --> E[Review and deliver]
    B -->|Missing information| F[Ask the owner]
    C -->|Decision needed| F
    F --> G[Owner answers]
    G -->|Intake clarification| B
    G -->|Execution decision| C
```

- **Start with an incomplete description:** the agent creates an intake draft and asks specific questions when essential goals, scope, or acceptance criteria are unclear.
- **Execute once the task is clear:** selected tools advance the work, with step state, results, and execution records saved.
- **Inspect factual stage reports:** see completed work, remaining work, blockers, next actions, and supporting execution references.
- **Keep independent work moving:** a step awaiting an answer does not stop existing independent steps. When no work remains runnable, execution resources are released.
- **Resume after answering:** **Answer and continue** restores the original step context. Repeating an identical answer does not repeat execution.
- **Keep questions and progress:** closing the page does not lose the task; pending questions and saved results remain available after a server restart.

Owner questions and tool approvals are separate. The current entry points are the workbench and API. Automatic chat/robot routing, cross-project name resolution, external notifications, and ARTEX adapters remain future work.

See [delegated work and owner questions](docs/en-US/task-delegation.md) for usage, recovery behavior, and API details.

<a id="capabilities"></a>

## Capabilities

| Capability | What it provides | Details |
| --- | --- | --- |
| Pi agents | Single-agent, Deep, Plan-Execute, and Supervisor modes | [Agents and roles](docs/en-US/agent-and-role-guide.md) |
| Durable tasks | Planning, dependencies, bounded concurrency, budgets, pause/resume, and review | [Task delegation](docs/en-US/task-delegation.md) |
| Tools and skills | YAML recipes, MCP connections, role-specific tool sets, and on-demand skills | [Tools](tools/README_EN.md) · [Skills](skills/README.md) |
| Visual workflows | Compose agent, tool, condition, and approval nodes | [Workflows](docs/en-US/workflow-graph.md) |
| Knowledge and evidence | RAG retrieval, project facts, tool records, reports, and attack-chain analysis | [Knowledge base](docs/en-US/knowledge-base.md) |
| Project workspace | Assets, vulnerabilities, conversations, batch tasks, and shared project context | [Asset management](docs/en-US/asset-management.md) |
| Human oversight | RBAC, tool approvals, call guards, and audit logs | [Security model](docs/en-US/security-model.md) |
| Integrations | Burp Suite, browser extensions, bots, and external MCP services | [Plugins](#integrations) · [MCP](docs/en-US/mcp-federation.md) |

Specialized operations also include [WebShell](docs/en-US/webshell.md) and [C2](docs/en-US/c2.md). Configure the capabilities your task needs; a tool recipe does not mean its executable is installed or validated in your environment.

<a id="architecture"></a>

## Architecture

| Component | Responsibility |
| --- | --- |
| **Go server and task engine** | Task state, dependencies, budgets, owner questions, tool execution, and evidence |
| **Pi runtime** | Model interaction, agent tool loops, sessions, compaction, steering, and tool discovery |
| **Tool gateway** | Dispatch to MCP or local tools under role restrictions, permissions, approvals, and execution monitoring |
| **SQLite and Pi sessions** | Business state and evidence in SQLite; model conversations and compaction records in Pi sessions |
| **Web workbench** | Tasks, questions, stage reports, execution traces, and project materials |

**Pi 1.5.2 is the only agent runtime.** The Eino execution engine and dependencies have been removed. Durable assessments use the Go task engine to control execution and Pi for individual model proposals. They share the runtime with chat agents while managing their own task lifecycle.

An optional **DSH-Purge-inspired prompt profile** adds capability selection, main/child task anchors, and model-specific instructions. It can be disabled; Go retains tool permissions and approvals. Implementation and attribution are documented in the [Pi runtime guide](docs/en-US/pi-runtime.md) and [third-party notices](runtime/pi/THIRD_PARTY_NOTICES.md).

<a id="integrations"></a>

## Plugins and interface

| Integration | Usage | Documentation and download |
| --- | --- | --- |
| Burp Suite | Send selected HTTP requests to CyberStrikeAI for analysis | [Setup](plugins/burp-suite/cyberstrikeai-burp-extension/README.md) · [JAR](plugins/burp-suite/cyberstrikeai-burp-extension/dist/cyberstrikeai-burp-extension.jar) |
| Chrome / Edge | Capture and send requests from the CyberStrikeAI DevTools tab | [Setup](plugins/browser-extension/cyberstrikeai-browser-extension/README.md) · [ZIP](plugins/browser-extension/cyberstrikeai-browser-extension/dist/cyberstrikeai-browser-extension.zip) |

<details>
<summary><strong>Explore the workbench</strong></summary>

<table>
<tr>
<td width="50%"><img src="images/dashboard.png" alt="Dashboard"><br>Dashboard</td>
<td width="50%"><img src="images/dark.png" alt="Dark theme"><br>Dark theme</td>
</tr>
<tr>
<td width="50%"><img src="images/web-console.png" alt="Chat console"><br>Chat console</td>
<td width="50%"><img src="images/knowledge-base.png" alt="Knowledge base"><br>Knowledge base</td>
</tr>
<tr>
<td width="50%"><img src="images/mcp-management.png" alt="MCP management"><br>MCP management</td>
<td width="50%"><img src="images/plugins.png" alt="Burp Suite extension"><br>Burp Suite extension</td>
</tr>
</table>

These repository screenshots illustrate existing features. For the new task-delegation entry point and workflow, follow the [usage guide](docs/en-US/task-delegation.md).

</details>

## Updates and migration

For new installations, [config.example.yaml](config.example.yaml) points updates to this repository:

```yaml
update:
  repo: https://github.com/Sycun/CyberStrikeAI
```

For an existing installation, check the source under **Settings → One-click Update**. An empty `update.repo` still selects the official upstream, not this independently maintained version. The update process builds a Pi worker matching the source. See [deployment](docs/en-US/deployment.md) for the procedure.

Before migrating from an Eino version, back up runtime configuration and data, and finish or export existing workflows: **old Eino binary workflow checkpoints cannot resume in this version**. Legacy API routes, configuration keys, and historical records retain data compatibility; there is no old-engine switch. See the [migration guide](docs/en-US/pi-runtime.md).

## Validation status

The current implementation has been checked with full Go tests, race tests, Pi SDK integration tests, frontend regressions, and local browser checks. An isolated live-model smoke run completed the sequence **ambiguous request → clarification → owner answer → echo tool execution → stage reports → completion**, using synthetic inputs and tool results.

This supports team trials. Complex long-running tasks, compatibility across model channels, general intent accuracy, cost, and effectiveness still need further evaluation. See the latest checks in [GitHub Actions](https://github.com/Sycun/CyberStrikeAI/actions).

<a id="documentation"></a>

## Documentation

| What you want to do | Start here |
| --- | --- |
| Deploy and configure models | [Deployment](docs/en-US/deployment.md) · [Configuration](docs/en-US/configuration.md) · [Troubleshooting](docs/en-US/troubleshooting.md) |
| Delegate tasks | [Usage and API](docs/en-US/task-delegation.md) · [Design background, Chinese](docs/zh-CN/task-delegation-design.md) |
| Customize agents and tools | [Pi runtime](docs/en-US/pi-runtime.md) · [Agents and roles](docs/en-US/agent-and-role-guide.md) · [Tool recipes](tools/README_EN.md) |
| Integrate an application | [API reference](docs/en-US/api-reference.md) · [API examples](docs/en-US/api-recipes.md) · [MCP](docs/en-US/mcp-federation.md) |
| Operate a team service | [Security hardening](docs/en-US/security-hardening.md) · [Runbooks](docs/en-US/runbooks.md) |
| Contribute code | [Developer guide](docs/en-US/developer-guide.md) · [Testing](docs/en-US/testing.md) · [Repository guide](docs/REPOSITORY_GUIDE.md) |

Browse all topics in the [English documentation](docs/en-US/README.md) or [bilingual index](docs/README.md).

<a id="contributing"></a>

## Feedback and contributions

Report trial feedback in [this repository's Issues](https://github.com/Sycun/CyberStrikeAI/issues). Include the commit, operating system, model protocol/channel type, reproduction steps, and redacted logs. Do not upload API keys, credentials, or sensitive engagement materials. PRs for tool adapters, usability improvements, and regression cases are welcome.

This repository and the official upstream publish independently. For upstream community information, visit [AIPentest/CyberStrikeAI](https://github.com/AIPentest/CyberStrikeAI). Thanks to the original authors and contributors, and to Pi, MCP, and the other open-source projects used here.

## License and intended use

The project is licensed under [Apache License 2.0](LICENSE); third-party components retain their own licenses and notices. Use it for security work on systems you own or are explicitly authorized to assess, within the agreed scope and applicable law. See [SECURITY.md](SECURITY.md) for security reporting.
