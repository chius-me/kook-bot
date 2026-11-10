package tts

// Voice is one Edge neural voice. The UI shows Alias. The API stores ID.
type Voice struct {
	Alias string `json:"alias"`
	ID    string `json:"id"`
}

// EdgeVoices is the built-in list. v1 does not shell out to --list-voices.
func EdgeVoices() []Voice {
	return []Voice{
		{Alias: "晓晓", ID: "zh-CN-XiaoxiaoNeural"},
		{Alias: "晓伊", ID: "zh-CN-XiaoyiNeural"},
		{Alias: "云希", ID: "zh-CN-YunxiNeural"},
		{Alias: "云扬", ID: "zh-CN-YunyangNeural"},
		{Alias: "云健", ID: "zh-CN-YunjianNeural"},
		{Alias: "云夏", ID: "zh-CN-YunxiaNeural"},
		{Alias: "晓北", ID: "zh-CN-liaoning-XiaobeiNeural"},
		{Alias: "晓妮", ID: "zh-CN-shaanxi-XiaoniNeural"},
		{Alias: "晓臻", ID: "zh-TW-HsiaoChenNeural"},
		{Alias: "晓雨", ID: "zh-TW-HsiaoYuNeural"},
		{Alias: "云哲", ID: "zh-TW-YunJheNeural"},
		{Alias: "晓佳", ID: "zh-HK-HiuGaaiNeural"},
		{Alias: "晓曼", ID: "zh-HK-HiuMaanNeural"},
		{Alias: "云龙", ID: "zh-HK-WanLungNeural"},
	}
}

const DefaultEdgeVoice = "zh-CN-XiaoxiaoNeural"

func EdgeAllowed(id string) bool {
	for _, v := range EdgeVoices() {
		if v.ID == id {
			return true
		}
	}
	return false
}

// OpenAIVoiceHints are form suggestions, not an allow-list.
func OpenAIVoiceHints() []Voice {
	return []Voice{
		{Alias: "alex", ID: "alex"},
		{Alias: "alloy", ID: "alloy"},
	}
}

// Preset fills the settings form. It is not an implicit request.
type Preset struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Model string `json:"model"`
	Voice string `json:"voice"`
}

func Presets() []Preset {
	return []Preset{
		{Name: "硅基流动 CosyVoice2", URL: "https://api.siliconflow.cn/v1/audio/speech", Model: "FunAudioLLM/CosyVoice2-0.5B", Voice: "alex"},
		{Name: "OpenAI", URL: "https://api.openai.com/v1/audio/speech", Model: "tts-1", Voice: "alloy"},
	}
}
