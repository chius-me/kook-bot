<div align="right">
  <a href="#readme">简体中文</a> | <a href="./README.en.md">English</a>
</div>

<div align="center">

# kook-bot

自托管的 KOOK 频道工具。在网页里绑定机器人，输入文字，由机器人自己在钉住的语音频道里说出来。

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/chius-me/kook-bot/actions/workflows/image.yml/badge.svg)](https://github.com/chius-me/kook-bot/actions/workflows/image.yml)
[![Docker GHCR](https://img.shields.io/badge/Docker-GHCR-2496ED?logo=docker&logoColor=white)](https://github.com/chius-me/kook-bot/pkgs/container/kook-bot)
[![Go](https://img.shields.io/badge/Language-Go-00ADD8?logo=go&logoColor=white)](https://go.dev/)

[问题反馈](https://github.com/chius-me/kook-bot/issues) · [镜像](https://github.com/chius-me/kook-bot/pkgs/container/kook-bot)

</div>

---

它不是聊天机器人，也不读频道消息再回复。一个网页只绑定一只机器人，机器人留在钉住的语音频道里。

## ✨ 特性

- **🎙️ 网页代说**：填一句文字，机器人用自己的账号在语音频道里说出来。
- **🔐 凭证只在网页里保存**：Bot Token、房间密码和 TTS 密钥加密进 SQLite，不进环境变量，也不进镜像。
- **🔊 钉死的语音合成**：镜像里是 `edge-tts==7.3.1`。也可以改成一条完整的 OpenAI 兼容 speech URL。
- **💬 文字镜像默认关闭**：打开后，频道里的发送者仍是机器人，正文会标明这是操作者让它说的那一句。
- **🐳 固定标签的镜像**：`linux/amd64` 与 `linux/arm64`，标签是 git 短 SHA，不用 `latest`。

## 🚀 快速开始

### 前置要求

- Docker 与 Docker Compose
- 一只 KOOK 机器人，开发者后台的连接模式选 **Websocket**

邀请时给这些权限：查看频道、语音链接、说话。只有要在文字频道留一句原文时，再加「发布消息」。不需要语音管理，也不需要播放伴奏。

同一只 Token 不要同时跑 KOOK-TTS。两边会抢同一条 Websocket，也会抢同一个语音房间。

### 用已发布的镜像

```bash
git clone https://github.com/chius-me/kook-bot.git
cd kook-bot
cp compose.env.example .env
```

编辑 `.env`：`KOOKBOT_SECRET` 至少 32 字节，填上 `KOOKBOT_UI_PASSWORD`，并写上：

```bash
KOOKBOT_IMAGE=ghcr.io/chius-me/kook-bot
KOOKBOT_IMAGE_TAG=<Actions 发布的 git 短 SHA>
```

```bash
docker compose pull
docker compose up -d
```

打开 `http://127.0.0.1:8765`。端口只绑在本机。不要把面板映射到公网。人在别的机器上时，用 SSH 隧道进来。

## 💡 使用

1. 用 `KOOKBOT_UI_PASSWORD` 登录。
2. 在设置里粘贴 Bot Token，先测试再保存。
3. 选一个语音频道。房间有密码就填上，然后保存。
4. 在设置页试听一句。试听不会进语音频道。
5. 自己先进入那个语音频道，再打开「代说」，输入一句话发送。

队列最多 8 句，一句话最多 200 个字。跳过和停止只丢掉还没说完的句子，不会因此退出频道。文字镜像在设置页，默认关。打开前要先选文字频道。

容器先以 root 启动，把 `/data` 交给 uid 65532，再降权运行。健康检查执行 `kookbot healthcheck`，只拨容器内的 `127.0.0.1` 加端口。

### 在仓库里自己构建

```bash
export KOOKBOT_IMAGE_TAG=$(git rev-parse --short HEAD)
docker compose up -d --build
```

不设 `KOOKBOT_IMAGE` 时，本地镜像名是 `kookbot`。

### 本机直接跑

还需要 Go 1.22、带 libopus 的 `ffmpeg`，以及 `edge-tts`。

```bash
cp .env.example .env
# 填好 KOOKBOT_SECRET 和 KOOKBOT_UI_PASSWORD

CGO_ENABLED=0 go run .
```

数据目录默认是 `./data`。容器里的合成命令是 `/opt/edge-tts/bin/edge-tts`，本机用 `KOOKBOT_EDGE_TTS` 指到自己的路径。

### 备份

先停容器。SQLite 开了 WAL，运行中只拷 `kookbot.sqlite` 会缺最近的写入。

```bash
docker compose stop
docker run --rm -v kookbot-data:/data -v "$PWD":/backup alpine \
  sh -c 'cp -a /data/kookbot.sqlite* /backup/'
docker compose start
```

停干净之后，`-wal` 和 `-shm` 可能已经不在。有哪几个就拷哪几个。表结构停在 `001`。回滚时换回上一只镜像，volume 不动。

换 `KOOKBOT_SECRET` 之后，原来的 Token 和 TTS 密钥解不开。网页会要求重填。旧密文不会被自动迁移。

### 语音合成与网络

默认用镜像里的 `edge-tts==7.3.1`。微软接口如果返回 403，把 Dockerfile 里的 pin 和 `internal/tts/edge.go` 顶部的版本一起升掉，重新构建。不要改 CLI 的 User-Agent。

设置页可以改成兼容 OpenAI 的语音接口。填完整的 speech URL，程序不会自动补 `/audio/speech`。地址必须是 https，并且不能指到内网、本机或云主机元数据地址。

默认使用 Docker 桥接网络，不要开 host 网络。只有在桥接网络丢掉向外的 UDP，或同一条推流里前后两句的源端口变了、KOOK 把后面的包丢掉时，才临时加 `network_mode: host` 做对照。排完去掉。

日志里不应出现 `Authorization`、Token、房间密码或带 query 的 `wss://` 地址。

## ⚙️ 配置

Bot Token 不写在环境变量里。`.env` 只放启动口令。

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `KOOKBOT_SECRET` | 是 | — | 至少 32 字节。用来加密 Token、房间密码和 TTS 密钥 |
| `KOOKBOT_UI_PASSWORD` | 是 | — | 网页登录口令。不入库，修改后要重启进程 |
| `KOOKBOT_HTTP_ADDR` | 否 | `127.0.0.1:8765` | 必须是 `host:port`。Compose 里是 `0.0.0.0:8765` |
| `KOOKBOT_DATA_DIR` | 否 | `/data` | SQLite 与音频。本机示例是 `./data` |
| `KOOKBOT_BASE_URL` | 否 | `https://www.kookapp.cn/api` | 不要带 `/v3` |
| `KOOKBOT_FFMPEG` | 否 | `ffmpeg` | 需能编码 libopus |
| `KOOKBOT_EDGE_TTS` | 否 | `/opt/edge-tts/bin/edge-tts` | Edge TTS 命令路径 |
| `KOOKBOT_LOG_LEVEL` | 否 | `info` | `debug` 会记下代说文本 |
| `KOOKBOT_COOKIE_SECURE` | 否 | 空 | 设为 `1` 时，cookie 带 Secure |
| `KOOKBOT_TTS_ALLOW_PRIVATE` | 否 | 空 | 设为 `1` 才允许私网 TTS 地址 |
| `KOOKBOT_IMAGE` | 否 | `kookbot` | 只给 Compose 用。发布镜像写 `ghcr.io/chius-me/kook-bot` |
| `KOOKBOT_IMAGE_TAG` | Compose 必填 | — | git 短 SHA。不要写 `latest` |

## 🏗️ 架构

一个 Go 进程同时提供网页、KOOK HTTP、Gateway Websocket、语音合成和唯一的语音会话。合成默认调用镜像里的 `edge-tts` 命令。音频由常驻的 ffmpeg 推成 Opus RTP。网页是同一进程里的模板，没有单独的前端工程。

## 📄 License

基于 [MIT 许可证](LICENSE) 开源发布。

<div align="center">
  <sub>Crafted with ❤️ by <a href="https://chius.cc">Chius</a> · Powered by Go</sub>
</div>
