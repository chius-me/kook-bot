<div align="center">

# kook-bot

自托管的 KOOK 频道工具。在网页里填好机器人 Token 和语音频道，输入文字，机器人用自己的账号在语音频道里把这句话说出来。

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![image](https://github.com/chius-me/kook-bot/actions/workflows/image.yml/badge.svg)](https://github.com/chius-me/kook-bot/actions/workflows/image.yml)
![Language Go](https://img.shields.io/badge/Language-Go-00ADD8.svg?logo=go&logoColor=white)

</div>

---

它不是聊天机器人，也不会读频道里别人的消息再回复。文字镜像默认关闭。打开之后，频道里看到的发送者仍是机器人，正文会标明这是操作者让它说的那一句。

## 准备

在 KOOK 开发者后台把连接模式选成 **Websocket**。不要选 Webhook，这个进程收不到 Webhook。

邀请机器人时给这些权限：

- 查看频道
- 语音链接
- 说话

只有要在文字频道里留一句原文时，再加「发布消息」。不需要语音管理，也不需要播放伴奏。

同一只机器人 Token 不要同时跑 KOOK-TTS。两边会抢同一条 Websocket，也会抢同一个语音房间。

## 用 Docker 跑

在仓库目录：

```bash
cp compose.env.example .env
# 编辑 .env：KOOKBOT_SECRET 至少 32 字节，再填 KOOKBOT_UI_PASSWORD
# 镜像标签用 git 短 SHA，不用 latest
export KOOKBOT_IMAGE_TAG=$(git rev-parse --short HEAD)

docker compose up -d --build
```

然后打开 `http://127.0.0.1:8765`。端口只绑在本机。不要把面板映射到公网。人在别的机器上时，用 SSH 隧道进来。

推到 `main` 之后，GitHub Actions 把镜像推到 `ghcr.io/chius-me/kook-bot:<git 短 SHA>`，同时有 `linux/amd64` 和 `linux/arm64`。拉请求只构建 `linux/amd64` 并做一次不带 Token 的冒烟，不推送。标签不用 `latest`。仓库是公开的，这个包也是公开的，服务器上不用登录就能拉。

```bash
# .env 里写 KOOKBOT_IMAGE=ghcr.io/chius-me/kook-bot
# KOOKBOT_IMAGE_TAG 用 Actions 发布的那条短 SHA
docker compose pull
docker compose up -d
```

容器里的监听地址是 `0.0.0.0:8765`，写在 `docker-compose.yml` 里。健康检查执行 `kookbot healthcheck`，它只拨容器内的 `127.0.0.1` 加端口，不拨 `0.0.0.0`。

`.env` 里只有启动口令。Bot Token、房间密码和 TTS 密钥在网页里填写，加密后放在 volume `kookbot-data` 的 `/data/kookbot.sqlite`。容器先以 root 启动，把 `/data` 交给 uid 65532，再降权运行。

## 网页里做什么

1. 用 `KOOKBOT_UI_PASSWORD` 登录。
2. 打开设置，粘贴 Bot Token，先测试再保存。
3. 选一个语音频道。有房间密码就填上。保存。
4. 在设置页试听一句。试听不会进语音频道。
5. 自己先进入那个 KOOK 语音频道，再打开「代说」，输入一句话发送。

机器人会留在钉住的语音频道里。队列最多 8 句，一句话最多 200 个字。跳过和停止只丢掉还没说完的句子，不会因此退出频道。

文字镜像在设置页，默认关。打开前要先选文字频道。

## 本机直接跑

```bash
cp .env.example .env
# 同样填好 KOOKBOT_SECRET 和 KOOKBOT_UI_PASSWORD

CGO_ENABLED=0 go run .
```

本地数据目录默认是 `./data`。本机还要自己装好 `ffmpeg`（要有 libopus）和 `edge-tts`。容器里的命令路径是 `/opt/edge-tts/bin/edge-tts`，本机可以用 `KOOKBOT_EDGE_TTS` 指到别的路径。

```bash
CGO_ENABLED=0 go build -o kookbot .
./kookbot healthcheck
```

## 备份

先停容器，再拷数据库。SQLite 开了 WAL，运行中只拷 `kookbot.sqlite` 会缺最近的写入。

```bash
docker compose stop
docker run --rm -v kookbot-data:/data -v "$PWD":/backup alpine \
  sh -c 'cp -a /data/kookbot.sqlite* /backup/'
docker compose start
```

停干净之后，`-wal` 和 `-shm` 可能已经不在。有哪几个就拷哪几个。

这个版本的表结构停在 `001`。回滚时换回上一只镜像，volume 不动。镜像标签用构建时的 git 短 SHA，不要靠 `latest` 回头。

换 `KOOKBOT_SECRET` 之后，原来的 Token 和 TTS 密钥解不开。网页会要求重填。旧密文不会被自动迁移。

## 语音合成

默认用镜像里的 `edge-tts==7.3.1`。微软接口如果返回 403，把 Dockerfile 里的 pin 和 `internal/tts/edge.go` 顶部的版本一起升掉，重新构建。不要改 CLI 的 User-Agent，也不要在 Go 里重写那套协议。

设置页可以改成兼容 OpenAI 语音接口的地址。填完整的 speech URL，程序不会自动在后面加 `/audio/speech`。预置按钮只帮你填表。地址必须是 https，并且不能指到内网、本机或云主机元数据地址。

## 网络排障

默认使用 Docker 桥接网络，不要开 host 网络。

只有在确认是下面两种情况时，才临时加 `network_mode: host` 做对照：

- 桥接网络把向外的 UDP 丢掉了
- 同一条推流里，前后两句的源端口变了，KOOK 把后面的包丢掉

先看 `docker compose logs` 里的 ffmpeg 错误，以及机器上的收发包计数。排完把 host 网络去掉。

日志里不应出现 `Authorization`、Token、房间密码或带 query 的 `wss://` 地址。

## 许可

[MIT](LICENSE)。

<div align="center">
  <sub>Crafted with ❤️ by <a href="https://chius.cc">Chius</a> · Powered by Go</sub>
</div>
