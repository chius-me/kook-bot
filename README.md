<div align="center">

# kook-bot

A minimal KOOK (开黑啦) channel bot built with Go and WebSocket.

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/chius-me/kook-bot/actions/workflows/codeql.yml/badge.svg)](https://github.com/chius-me/kook-bot/actions)
![Language Go](https://img.shields.io/badge/Language-Go-00ADD8.svg?logo=go&logoColor=white)

[Setup](#setup) · [Run](#run) · [Build](#build)

</div>

---

A minimal KOOK bot built with Go and WebSocket.

## Setup

```bash
cp .env.example .env
# Edit .env and fill in your BOT_TOKEN
```

## Run

```bash
go run .
```

## Build

```bash
go build -o bsdx .
```

## Dependencies

- [golang-bot](https://github.com/kaiheila/golang-bot) — KOOK official Go SDK (local replace)

---

## 📄 License

Distributed under the [MIT](LICENSE) License.

<div align="center">
  <sub>Crafted with ❤️ by <a href="https://chius.cc">Chius</a> · Powered by Go</sub>
</div>