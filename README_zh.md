<div align="center">

# Veloce

**开源 AI API 网关与平台运营后端。**

统一接入上游 AI 服务、路由兼容 API，并集中管理 AI 平台运营能力。

[English](README.md) · [简体中文](README_zh.md)

![Go](https://img.shields.io/badge/Go-1.26.3-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-AGPL--3.0-blue)

</div>

---

## 项目简介

Veloce 为 AI 平台与开发者服务提供后端基础能力，将上游模型接入、API 路由、身份与访问管理、用量统计、计费和运营工具整合到一个可自行部署的服务中。

本仓库包含 Go 后端。Web 前端由独立的 [`veloce-web`](https://github.com/veloce-ailab/veloce-web) 仓库维护；从源码构建时，需要将前端源码放在本仓库的同级目录。

## 功能特性

| 领域 | 能力 |
| --- | --- |
| **AI API 网关** | OpenAI 兼容接口，以及 Claude Messages、Gemini 兼容请求格式；支持流式请求和统一任务状态接口。 |
| **多模态 API** | 文本与对话、图像生成与编辑、音频、内容审核和视频任务接口；实际可用能力取决于上游渠道支持。 |
| **渠道与模型运营** | 管理上游渠道和模型，同步模型列表与价格，配置访问权限和路由，并查看渠道用量。 |
| **账户与访问控制** | 密码与 OIDC 登录、Passkey（WebAuthn）、API Key、用户/分组管理、访问控制和速率限制。 |
| **用量与计费** | 用量及审计日志、余额管理、模型定价、订阅、支付订单和用户用量统计。 |
| **运维与扩展** | 管理接口、状态监控、定时任务、公告、可选 Redis、多节点运行和基于 WebAssembly 的插件。 |

## 快速开始

### 环境要求

- Go **1.26.3**（版本见 [`go.mod`](go.mod)）
- 构建前端需要 Node.js **22** 和 Corepack/Yarn
- [`veloce-web`](https://github.com/veloce-ailab/veloce-web) 对应版本的源码

后端 `go.mod` 通过本地路径引用 `veloce-web`，目录结构应类似：

```text
workspace/
├── veloce/       # 本仓库
└── web/          # veloce-web 仓库
```

### 构建并运行

以下命令从 `veloce` 目录开始执行：

```bash
# 将前端仓库克隆到本仓库同级目录（如使用正式版本，请选择匹配的 tag）
git clone https://github.com/veloce-ailab/veloce-web.git ../web

# 构建前端
cd ../web
corepack enable
corepack yarn install --immutable
corepack yarn build

# 配置并启动后端
cd ../veloce
cp .env.example .env
# 编辑 .env；设置强随机 JWT_SECRET，并按需配置认证方式
go run .
```

默认监听端口为 `8080`，数据库使用 SQLite。服务启动后，API 地址为 `http://localhost:8080/v1`，Web 界面由后端提供。

### 示例：获取可用模型

在 Web 界面创建 API Key 后，即可通过网关查询模型：

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY"
```

## 配置说明

建议从 [`.env.example`](.env.example) 开始配置。常用选项包括：

- `PORT`：HTTP 监听端口，默认 `8080`
- `DB_DRIVER`：数据库类型，支持 `sqlite`（默认）、`postgres`、`mysql`
- `DB_PATH`：SQLite 数据库文件路径
- `DB_DSN` 或 `DATABASE_URL`：PostgreSQL/MySQL 连接字符串
- `JWT_SECRET`：用于签名管理后台会话的密钥
- `OIDC_*`：可选的 OIDC 身份提供方配置

使用 PostgreSQL 或 MySQL 时，在启动服务前设置数据库类型和连接字符串；应用启动时会自动创建或迁移表结构。

```dotenv
# PostgreSQL
DB_DRIVER=postgres
DB_DSN=host=127.0.0.1 user=flai password=change-me dbname=flai port=5432 sslmode=disable

# MySQL 8+
DB_DRIVER=mysql
DB_DSN=flai:change-me@tcp(127.0.0.1:3306)/flai?charset=utf8mb4&parseTime=True&loc=Local
```

Redis 为可选依赖。多节点部署可在系统设置中启用；启用前需要为每个实例配置唯一的 `NODE_NAME`。

### 从 SQLite 迁移至 PostgreSQL 或 MySQL

将 `DB_PATH` 指向源 SQLite 文件，将 `DB_DRIVER` 和 `DB_DSN` 设置为一个**空的**目标数据库，然后执行单向迁移：

```bash
DB_DRIVER=postgres \
DB_PATH=veloce.db \
DB_DSN='host=127.0.0.1 user=flai password=change-me dbname=flai port=5432 sslmode=disable' \
go run . --migrate
```

迁移过程只读取源 SQLite 文件；如果目标数据库已包含应用表，命令会拒绝执行。数据分批复制完成后程序退出，之后可继续使用目标数据库配置正常启动。

## 仓库结构

```text
cmd/flai/          备用应用入口
internal/adapters/ 上游协议适配器
internal/api/      HTTP API 处理器
internal/app/      应用启动、路由与服务端
internal/channel/  消息平台集成
internal/model/    数据模型与数据库迁移
internal/service/  网关、计费、认证、插件与运维服务
```

## 许可证

Veloce 使用 [GNU Affero 通用公共许可证 v3.0（AGPL-3.0）](LICENSE)。

## 致谢

感谢 [Linux.do](https://linux.do) 以及所有为项目做出贡献的朋友。
