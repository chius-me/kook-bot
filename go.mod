module bsdx

go 1.22

require github.com/kaiheila/golang-bot v0.0.0

require (
	github.com/avast/retry-go/v4 v4.3.3 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.14.2 // indirect
	github.com/bytedance/sonic/loader v0.5.1 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/gookit/event v1.0.6 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/looplab/fsm v1.0.1 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/sirupsen/logrus v1.9.1 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	golang.org/x/arch v0.0.0-20210923205945-b76863e36670 // indirect
	golang.org/x/sys v0.22.0 // indirect
)

replace (
	github.com/bytedance/sonic => github.com/bytedance/sonic v1.15.1
	github.com/bytedance/sonic/loader => github.com/bytedance/sonic/loader v0.5.1
	github.com/kaiheila/golang-bot => ../golang-bot
)
