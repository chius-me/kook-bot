FROM golang:1.22-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
# Some proxies stall Go's HTTP/2 client midway through a large module zip.
ENV GODEBUG=http2client=0
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/kookbot .

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates \
		ffmpeg \
		python3 \
		python3-pip \
		python3-venv \
	&& rm -rf /var/lib/apt/lists/* \
	&& python3 -m venv /opt/edge-tts \
	&& /opt/edge-tts/bin/pip install --no-cache-dir edge-tts==7.3.1 \
	&& ffmpeg -hide_banner -encoders | grep -q libopus \
	&& /opt/edge-tts/bin/edge-tts --version | grep -q 7.3.1 \
	&& mkdir -p /data \
	&& chown 65532:65532 /data

COPY --from=build /out/kookbot /usr/local/bin/kookbot
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh

VOLUME /data
ENV KOOKBOT_DATA_DIR=/data \
	KOOKBOT_HTTP_ADDR=0.0.0.0:8765 \
	KOOKBOT_EDGE_TTS=/opt/edge-tts/bin/edge-tts \
	KOOKBOT_FFMPEG=ffmpeg \
	TZ=Asia/Shanghai
EXPOSE 8765
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
	CMD ["kookbot", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
