package speak

import (
	"context"
	"encoding/json"
	"strings"

	"bsdx/internal/kook"
	"bsdx/internal/store"
)

// MirrorFunc runs after playback has started. A nil func sends nothing.
type MirrorFunc func(ctx context.Context, job Job) error

// EscapeKMarkdown breaks mention and link markers. (met) must not survive raw.
func EscapeKMarkdown(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"~", `\~`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		">", `\>`,
	).Replace(s)
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// KMarkdown is the type 9 body. The KOOK author is still the bot.
func KMarkdown(text string) string {
	return "**（操作者代说）**\n> " + EscapeKMarkdown(oneLine(text))
}

// CardContent is a JSON array encoded as one string for message type 10.
func CardContent(text string) (string, error) {
	line := "**（操作者代说）**\n> " + EscapeKMarkdown(oneLine(text))
	card := []map[string]any{{
		"type":  "card",
		"theme": "secondary",
		"size":  "lg",
		"modules": []map[string]any{{
			"type": "section",
			"text": map[string]string{
				"type":    "kmarkdown",
				"content": line,
			},
		}},
	}}
	raw, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// NewMirror sends one message when the binding is enabled. Failure is the KOOK message.
func NewMirror(client *kook.Client, token func() (string, bool), load func(context.Context) (store.Binding, error)) MirrorFunc {
	return func(ctx context.Context, job Job) error {
		if client == nil || load == nil || token == nil {
			return nil
		}
		binding, err := load(ctx)
		if err != nil || !binding.MirrorEnabled || binding.TextChannelID == "" {
			return nil
		}
		tok, ok := token()
		if !ok || tok == "" {
			return nil
		}
		typ := 9
		content := KMarkdown(job.Text)
		if binding.MirrorFormat == "card" {
			typ = 10
			content, err = CardContent(job.Text)
			if err != nil {
				return err
			}
		}
		return client.CreateMessage(ctx, tok, typ, binding.TextChannelID, content)
	}
}
