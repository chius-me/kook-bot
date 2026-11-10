package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"bsdx/internal/logx"
)

// ErrBadURL means the speech URL failed the public-https check.
var ErrBadURL = errors.New("bad_url")

// ErrBadAPIVoice means an OpenAI voice is empty, too long, or has whitespace.
var ErrBadAPIVoice = errors.New("bad_api_voice")

const maxSpeechBody = 5 << 20

// Lookup resolves a host after the URL shape is accepted.
type Lookup func(host string) ([]net.IP, error)

// OpenAI posts the operator's full speech URL. It does not append /audio/speech.
type OpenAI struct {
	URL          string
	APIKey       string
	Model        string
	AllowPrivate bool
	Lookup       Lookup
	Client       *http.Client
}

func (o OpenAI) ID() string { return "openai" }

func (o OpenAI) Synthesize(ctx context.Context, req Request, outPath string) error {
	if err := ValidateSpeechURL(o.URL, o.AllowPrivate, o.Lookup); err != nil {
		return err
	}
	if err := CheckAPIVoice(req.VoiceID); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"model":           o.Model,
		"input":           req.Text,
		"voice":           req.VoiceID,
		"response_format": "mp3",
		"speed":           req.Rate,
	})
	if err != nil {
		return ErrSynth
	}
	ctx, cancel := context.WithTimeout(ctx, synthTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL, bytes.NewReader(payload))
	if err != nil {
		return ErrBadURL
	}
	httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: synthTimeout, CheckRedirect: refuseRedirect}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSynth, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSpeechBody+1))
	if err != nil {
		return ErrSynth
	}
	if len(body) > maxSpeechBody {
		return ErrSynth
	}
	if resp.StatusCode != http.StatusOK {
		n := body
		if len(n) > 200 {
			n = n[:200]
		}
		return fmt.Errorf("%w: %s", ErrSynth, logx.Redact(string(n)))
	}
	if len(body) < 256 {
		return ErrSynth
	}
	return os.WriteFile(outPath, body, 0o600)
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// CheckAPIVoice accepts a trimmed voice of 1..64 runes with no whitespace or controls.
func CheckAPIVoice(voice string) error {
	voice = strings.TrimSpace(voice)
	if voice == "" {
		return ErrBadAPIVoice
	}
	n := 0
	for _, r := range voice {
		n++
		if r <= 0x20 || r == 0x7f {
			return ErrBadAPIVoice
		}
	}
	if n > 64 {
		return ErrBadAPIVoice
	}
	return nil
}

// ValidateSpeechURL allows https only. Private, link-local, loopback, multicast,
// CGNAT, and the metadata address are rejected unless allowPrivate is set.
func ValidateSpeechURL(raw string, allowPrivate bool, lookup Lookup) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil {
		return ErrBadURL
	}
	host := u.Hostname()
	if host == "" {
		return ErrBadURL
	}
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivate && blockedIP(ip) {
			return ErrBadURL
		}
		return nil
	}
	if allowPrivate {
		return nil
	}
	if lookup == nil {
		lookup = func(h string) ([]net.IP, error) { return net.LookupIP(h) }
	}
	ips, err := lookup(host)
	if err != nil || len(ips) == 0 {
		return ErrBadURL
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return ErrBadURL
		}
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	// CGNAT 100.64.0.0/10 and the cloud metadata address.
	if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true
	}
	if ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	return false
}
