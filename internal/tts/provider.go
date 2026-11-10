package tts

import "context"

// Request is one synthesis. Volume is applied later, at decode.
type Request struct {
	Text    string
	VoiceID string
	Rate    float64
	Volume  float64
}

// Provider writes an mp3 to outPath.
type Provider interface {
	ID() string
	Synthesize(ctx context.Context, req Request, outPath string) error
}
