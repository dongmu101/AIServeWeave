# AIServeWeave

[简体中文](README.md) | English

Weave inference services scattered across Macs, office workstations and GPU servers into a single entry point: one OpenAI / Anthropic-compatible API that schedules requests onto any Ollama, vLLM, ComfyUI or other node behind it.

Nodes don't need a public address — the Agent dials out and joins the platform over an mTLS tunnel.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.27-00ADD8.svg)

> **Status:** pre-1.0. Minor releases may still break the protocol, configuration, database schema or CLI flags, and several backends have not been verified against real deployments. See [STATUS.md](STATUS.md) before running it in production.

## Features

- **Compatible APIs**: OpenAI Chat Completions / Responses (including tool loops) / Embeddings / Images, Anthropic Messages and the native Ollama API, all with SSE streaming.
- **Many backends**: Ollama, vLLM, SGLang, TensorRT-LLM, TokenSpeed, ComfyUI; hosted providers (OpenAI, Claude, Gemini, DeepSeek, Qwen and more); local CLIs (Codex, Claude Code, Gemini CLI and more).
- **NAT traversal**: the Agent only makes outbound connections and never listens on a port; a runtime allowlist ensures that even a compromised Gateway cannot make a node reach an undeclared address.
- **Scheduling and routing**: logical model aliases, node labels, priority and weight, health filtering, circuit breaking, retry before the first token, prefix affinity.
- **ComfyUI workflows**: curated templates, asynchronous jobs, SSE progress, cancellation, artifacts persisted to object storage.
- **Multi-tenancy**: API keys, quotas and rate limits (optionally shared across replicas via Redis), auditing, revocable sessions.
- **Admin console**: fleet view, versioned model-route releases with rollback, templates, job history, metric charts.

## Architecture

```text
Console ──► ControlPlane ──► MySQL / Redis
                │  config, key revocation, job history
                ▼
Client ──► Gateway ──► Object storage
  (OpenAI /    │
  Anthropic)   │ bidirectional gRPC stream + mTLS (dialed out by the Agent)
               ▼
             Agent ──► Ollama / vLLM / ComfyUI / ...

Registry ◄── Agent enrollment and certificate renewal, Gateway replica directory
```

| Component | Responsibility |
| --- | --- |
| [Gateway](service/aiServeWeaveGateway/README.md) | Compatible API front door, authentication and quotas, scheduling, tunnel termination |
| [Agent](service/aiServeWeaveAgent/README.md) | Runs on compute nodes, discovers local backends and executes requests over the tunnel |
| [Registry](service/aiServeWeaveRegistry/README.md) | Node identity (one-time token exchanged for a certificate) and Gateway replica discovery |
| [ControlPlane](service/aiServeWeaveControlPlane/README.md) | Tenants, users, keys, quotas, route and template releases, auditing, job history |
| [Console](service/aiServeWeaveConsole/README.md) | Next.js admin UI |

The full design is in [docs/architecture.md](docs/architecture.md).

## Quick start

Requires Docker Compose v2; running the Agent on the host requires Go 1.27.

```bash
cd deploy
cp .env.example .env    # fill in secrets and the first account, see deploy/README.md
docker compose up -d --build
```

Open <http://127.0.0.1:3000>, sign in and create an API key, then follow [Connect an Agent](deploy/README.md#接一个-agent) to attach your local Ollama:

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $AISW_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen3:8b", "messages": [{"role": "user", "content": "hello"}], "stream": true}'
```

Any OpenAI / Anthropic SDK works once its base URL points at `http://127.0.0.1:8080/v1`.

## API

| Endpoint | Description |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat, with tool calling |
| `POST /v1/responses` | OpenAI Responses, usable by Codex CLI |
| `POST /v1/embeddings` · `GET /v1/models` | |
| `POST /v1/images/generations` | Mapped to a ComfyUI template or a CLI image backend |
| `POST /v1/messages` | Anthropic Messages (text + images) |
| `POST /api/chat` · `/api/generate` · `/api/embeddings` | Native Ollama inference endpoints |
| `POST /v1/workflows/{id}/runs` · `/v1/jobs/{id}/...` | Workflow jobs: submit, status, events, cancel, artifacts |

Unsupported parameters are rejected by name, never silently dropped. Per-endpoint limits are listed in [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md).

## Documentation

The documents below are currently written in Chinese.

- [Architecture](docs/architecture.md): protocol compatibility, ComfyUI, scheduling, data model, security, observability
- [Deployment](deploy/README.md) · [Database upgrade and recovery](deploy/database-recovery.md)
- [Tunnel protocol](service/aiServeWeaveAgent/tunnel/README.md)
- [Progress and roadmap](STATUS.md) · [Changelog](CHANGELOG.md)

## Contributing

Issues and pull requests are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md) (coding conventions and quality gates) first; report security issues privately as described in [SECURITY.md](SECURITY.md). Issues and PRs may be written in English or Chinese.

## License

[Apache License 2.0](LICENSE)
