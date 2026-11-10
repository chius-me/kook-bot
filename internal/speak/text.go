package speak

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reject is a stable API error.
type Reject struct {
	Status  int
	Code    string
	Message string
}

func (e *Reject) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func reject(status int, code, message string) *Reject {
	return &Reject{Status: status, Code: code, Message: message}
}

// Clean strips NUL and control characters, folds newlines, and checks length.
func Clean(text string, max int) (string, *Reject) {
	text = strings.TrimSpace(text)
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r == 0 || unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	n := utf8.RuneCountInString(out)
	if n == 0 {
		return "", reject(http.StatusBadRequest, "empty_text", "请输入要说的话")
	}
	if max > 0 && n > max {
		msg := "一句话最多 200 个字"
		if max <= 80 {
			msg = "试听最多 80 个字"
		}
		return "", reject(http.StatusUnprocessableEntity, "text_too_long", msg)
	}
	return out, nil
}

func checkRate(v float64) *Reject {
	if v < 0.5 || v > 2 {
		return reject(http.StatusBadRequest, "bad_rate", "语速要在 0.5 到 2.0 之间")
	}
	return nil
}

func checkVolume(v float64) *Reject {
	if v < 0.05 || v > 2 {
		return reject(http.StatusBadRequest, "bad_volume", "音量要在 0.05 到 2.0 之间")
	}
	return nil
}
