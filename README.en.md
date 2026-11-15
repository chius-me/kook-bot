<div align="right">
  <a href="./README.md">简体中文</a> | <a href="#readme">English</a>
</div>

<div align="center">

# kook-bot

A self-hosted KOOK channel tool. Bind a bot in the web panel, type a line, and the bot says it in the pinned voice channel.

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/chius-me/kook-bot/actions/workflows/image.yml/badge.svg)](https://github.com/chius-me/kook-bot/actions/workflows/image.yml)
[![Docker GHCR](https://img.shields.io/badge/Docker-GHCR-2496ED?logo=docker&logoColor=white)](https://github.com/chius-me/kook-bot/pkgs/container/kook-bot)
[![Go](https://img.shields.io/badge/Language-Go-00ADD8?logo=go&logoColor=white)](https://go.dev/)

[Issues](https://github.com/chius-me/kook-bot/issues) · [Image](https://github.com/chius-me/kook-bot/pkgs/container/kook-bot)

</div>

---

This is not a chatbot, and it does not read channel messages to reply. One panel binds one bot. The bot stays in the pinned voice channel.

## ✨ Features

- **🎙️ Web speak:** type a line and the bot says it with its own account.
- **🔐 Credentials stay in the panel:** the bot token, room password, and TTS key are encrypted in SQLite. They are not environment variables and they are not in the image.
- **🔊 Pinned speech:** the image ships `edge-tts==7.3.1`. You can switch to a full OpenAI-compatible speech URL.
- **💬 Text mirror is off:** when enabled, the sender shown in KOOK is still the bot. The body says an operator asked for that line.
- **🐳 Immutable image tags:** `linux/amd64` and `linux/arm64`, tagged with the git short SHA. There is no `latest`.

## 🚀 Quick start

### Requirements

- Docker and Docker Compose
- A KOOK bot whose developer setting is **Websocket**

Invite it with: view channels, voice link, and speak. Add publish messages only if you want a text-channel copy. Voice admin and music playback are not required.

Do not run KOOK-TTS with the same token. The two processes fight over one Websocket and one voice room.

### Published image

```bash
git clone https://github.com/chius-me/kook-bot.git
cd kook-bot
cp compose.env.example .env
```

Edit `.env`. `KOOKBOT_SECRET` must be at least 32 bytes. Set `KOOKBOT_UI_PASSWORD`, then:

```bash
KOOKBOT_IMAGE=ghcr.io/chius-me/kook-bot
KOOKBOT_IMAGE_TAG=<git short SHA published by Actions>
```

```bash
docker compose pull
docker compose up -d
```

Open `http://127.0.0.1:8765`. The port is bound to localhost. Do not publish the panel on the public internet. From another machine, use an SSH tunnel.

## 💡 Usage

1. Sign in with `KOOKBOT_UI_PASSWORD`.
2. Paste the bot token in Settings, test it, then save.
3. Pick a voice channel. If the room has a password, enter it and save.
4. Preview one line in Settings. Preview does not join the voice channel.
5. Join that voice channel yourself, open Speak, and send one line.

The queue holds 8 lines. One line is at most 200 characters. Skip and stop drop unfinished speech. They do not leave the channel. The text mirror is in Settings and defaults to off. Pick a text channel before turning it on.

The container starts as root, gives `/data` to uid 65532, then drops privileges. The health check runs `kookbot healthcheck` and dials `127.0.0.1` plus the listen port inside the container.

### Build from this repo

```bash
export KOOKBOT_IMAGE_TAG=$(git rev-parse --short HEAD)
docker compose up -d --build
```

When `KOOKBOT_IMAGE` is unset, the local image name is `kookbot`.

### Run on the host

You also need Go 1.22, `ffmpeg` with libopus, and `edge-tts`.

```bash
cp .env.example .env
# Fill KOOKBOT_SECRET and KOOKBOT_UI_PASSWORD

CGO_ENABLED=0 go run .
```

The data directory defaults to `./data`. The image looks for `/opt/edge-tts/bin/edge-tts`. Point `KOOKBOT_EDGE_TTS` at your own binary.

### Backup

Stop the container first. SQLite uses WAL, so copying only `kookbot.sqlite` while it is running drops the newest writes.

```bash
docker compose stop
docker run --rm -v kookbot-data:/data -v "$PWD":/backup alpine \
  sh -c 'cp -a /data/kookbot.sqlite* /backup/'
docker compose start
```

After a clean stop, `-wal` and `-shm` may already be gone. Copy whichever files are there. The schema stays at `001`. Roll back by swapping the image. Leave the volume in place.

After `KOOKBOT_SECRET` changes, the old token and TTS key cannot be decrypted. The panel asks you to enter them again. Old ciphertext is not migrated.

### Speech and network

The default is `edge-tts==7.3.1` in the image. If Microsoft returns 403, bump the pin in the Dockerfile and at the top of `internal/tts/edge.go`, then rebuild. Do not change the CLI User-Agent.

Settings can point at an OpenAI-compatible speech API. Enter the full speech URL. The program does not append `/audio/speech`. The URL must be https, and it cannot target a private network, localhost, or a cloud metadata address.

Use the Docker bridge network. Do not enable host networking. Add `network_mode: host` only as a temporary comparison when the bridge drops outbound UDP, or when the source port changes between two sentences and KOOK drops the later packets. Remove it when you are done.

Logs must not contain `Authorization`, the token, the room password, or a `wss://` URL with a query string.

## ⚙️ Configuration

The bot token is not an environment variable. `.env` holds only the process secrets.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `KOOKBOT_SECRET` | yes | — | At least 32 bytes. Encrypts the token, room password, and TTS key |
| `KOOKBOT_UI_PASSWORD` | yes | — | Panel password. Not stored. Restart after changing it |
| `KOOKBOT_HTTP_ADDR` | no | `127.0.0.1:8765` | Must be `host:port`. Compose sets `0.0.0.0:8765` |
| `KOOKBOT_DATA_DIR` | no | `/data` | SQLite and audio. The local example uses `./data` |
| `KOOKBOT_BASE_URL` | no | `https://www.kookapp.cn/api` | Do not include `/v3` |
| `KOOKBOT_FFMPEG` | no | `ffmpeg` | Must encode libopus |
| `KOOKBOT_EDGE_TTS` | no | `/opt/edge-tts/bin/edge-tts` | Edge TTS command |
| `KOOKBOT_LOG_LEVEL` | no | `info` | `debug` logs the spoken text |
| `KOOKBOT_COOKIE_SECURE` | no | empty | Set to `1` to mark cookies Secure |
| `KOOKBOT_TTS_ALLOW_PRIVATE` | no | empty | Set to `1` to allow a private TTS URL |
| `KOOKBOT_IMAGE` | no | `kookbot` | Compose only. The published name is `ghcr.io/chius-me/kook-bot` |
| `KOOKBOT_IMAGE_TAG` | required by Compose | — | Git short SHA. Do not use `latest` |

## 🏗️ Architecture

One Go process serves the panel, the KOOK HTTP client, the gateway Websocket, speech synthesis, and the single voice session. Synthesis calls the `edge-tts` command in the image. A long-lived ffmpeg process sends Opus RTP. The panel is HTML templates in the same process. There is no separate frontend.

## 📄 License

Distributed under the [MIT](LICENSE) License.

<div align="center">
  <sub>Crafted with ❤️ by <a href="https://chius.cc">Chius</a> · Powered by Go</sub>
</div>
