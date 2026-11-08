<div align="center">

# Veloce

**An open-source AI API gateway and platform operations backend.**

Manage AI providers, route compatible APIs, and operate an AI service from one place.

[English](README.md) · [简体中文](README_zh.md)

![Go](https://img.shields.io/badge/Go-1.26.3-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-AGPL--3.0-blue)

</div>

---

## Overview

Veloce provides the backend foundation for AI platforms and developer services. It brings upstream model access, API routing, identity and access management, usage metering, billing, and operational tools together in one self-hostable service.

This repository contains the Go backend. The web frontend is maintained separately in [`veloce-web`](https://github.com/veloce-ailab/veloce-web) and is required as a sibling directory when building from source.

## Features

| Area | Capabilities |
| --- | --- |
| **AI API gateway** | OpenAI-compatible endpoints, plus Claude Messages and Gemini-compatible request formats; streaming and unified task-status endpoints. |
| **Multimodal APIs** | Text and chat, image generation and editing, audio, moderation, and video task endpoints, subject to upstream provider support. |
| **Provider operations** | Manage upstream channels and models, synchronize model lists and prices, configure access and routing, and monitor channel usage. |
| **Accounts and access** | Password and OIDC sign-in, Passkeys (WebAuthn), API keys, user/group management, access controls, and rate limiting. |
| **Usage and billing** | Usage and audit logs, balance management, pricing, subscriptions, payment orders, and user usage statistics. |
| **Operations and extensibility** | Admin APIs, status monitoring, scheduled jobs, announcements, optional Redis, multi-node operation, and WebAssembly-based plugins. |

## Quick start

### Requirements

- Go **1.26.3** (see [`go.mod`](go.mod))
- Node.js **22** and Corepack/Yarn for building the frontend
- The matching [`veloce-web`](https://github.com/veloce-ailab/veloce-web) source checkout

The backend's `go.mod` uses a local replacement for `veloce-web`, so the source layout should look like this:

```text
workspace/
├── veloce/       # this repository
└── web/          # veloce-web repository
```

### Build and run

Run these commands from the `veloce` directory:

```bash
# Check out the frontend beside this repository (use a matching release/tag when applicable)
git clone https://github.com/veloce-ailab/veloce-web.git ../web

# Build the frontend
cd ../web
corepack enable
corepack yarn install --immutable
corepack yarn build

# Configure and start the backend
cd ../veloce
cp .env.example .env
# Edit .env; set a strong JWT_SECRET and configure authentication as needed
go run .
```

By default, the service listens on port `8080` and uses SQLite. Once running, the API is available at `http://localhost:8080/v1` and the web interface is served by the backend.

### Example: list available models

Create an API key in the web interface, then use it with the gateway:

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY"
```

## Configuration

Start with [`.env.example`](.env.example). Common settings include:

- `PORT`: HTTP listen port (default: `8080`)
- `DB_DRIVER`: `sqlite` (default), `postgres`, or `mysql`
- `DB_PATH`: SQLite database path
- `DB_DSN` or `DATABASE_URL`: PostgreSQL/MySQL connection string
- `JWT_SECRET`: secret used to sign dashboard sessions
- `OIDC_*`: optional OIDC provider settings

For PostgreSQL or MySQL, set the driver and connection string before starting the service. The application creates or migrates its schema on startup.

```dotenv
# PostgreSQL
DB_DRIVER=postgres
DB_DSN=host=127.0.0.1 user=flai password=change-me dbname=flai port=5432 sslmode=disable

# MySQL 8+
DB_DRIVER=mysql
DB_DSN=flai:change-me@tcp(127.0.0.1:3306)/flai?charset=utf8mb4&parseTime=True&loc=Local
```

Redis is optional. Multi-node deployments can be enabled in the system settings; configure a unique `NODE_NAME` for each instance before enabling the mode.

### Migrate SQLite to PostgreSQL or MySQL

Set `DB_PATH` to the source SQLite file and `DB_DRIVER`/`DB_DSN` to an **empty** target database, then run the one-way migration:

```bash
DB_DRIVER=postgres \
DB_PATH=veloce.db \
DB_DSN='host=127.0.0.1 user=flai password=change-me dbname=flai port=5432 sslmode=disable' \
go run . --migrate
```

The source SQLite file is read-only. The command refuses a target that already contains application tables, copies data in batches, and exits when complete. The target settings can then be used for normal startup.

## Repository layout

```text
cmd/flai/          Alternate application entry point
internal/adapters/ Upstream protocol adapters
internal/api/      HTTP API handlers
internal/app/      Application startup, routes, and server
internal/channel/  Messaging-platform integrations
internal/model/    Database models and migrations
internal/service/  Gateway, billing, auth, plugin, and operations services
```

## License

Veloce is licensed under the [GNU Affero General Public License v3.0](LICENSE).

## Acknowledgements

Thanks to [Linux.do](https://linux.do) and everyone contributing to the project.
