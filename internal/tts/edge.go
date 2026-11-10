package tts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"time"
)

// edge-tts pin is 7.3.1, not 7.3.0. Do not change the CLI User-Agent.
// A Microsoft 403 means bump that pin and rebuild the image.

const synthTimeout = 20 * time.Second

// ErrBadVoice is an Edge voice id outside the built-in table.
var ErrBadVoice = errors.New("bad_voice")

// ErrSynth means the CLI failed or the file is too small.
var ErrSynth = errors.New("tts_failed")

// Runner executes the edge-tts argv. Tests record the slice and skip the CLI.
type Runner func(ctx context.Context, bin string, args []string) error

// Edge synthesizes with the pinned CLI.
type Edge struct {
	Bin string
	Run Runner
}

func (e Edge) ID() string { return "edge" }

func (e Edge) Synthesize(ctx context.Context, req Request, outPath string) error {
	if !EdgeAllowed(req.VoiceID) {
		return ErrBadVoice
	}
	bin := e.Bin
	if bin == "" {
		bin = "edge-tts"
	}
	args := EdgeArgs(req.VoiceID, req.Rate, req.Text, outPath)
	run := e.Run
	if run == nil {
		run = RunCLI
	}
	ctx, cancel := context.WithTimeout(ctx, synthTimeout)
	defer cancel()
	if err := run(ctx, bin, args); err != nil {
		return fmt.Errorf("%w: %v", ErrSynth, err)
	}
	st, err := os.Stat(outPath)
	if err != nil || st.Size() < 256 {
		return ErrSynth
	}
	return nil
}

// EdgeArgs is the CLI slice after the binary. A negative rate stays in one element.
func EdgeArgs(voice string, rate float64, text, outPath string) []string {
	return []string{
		"--voice", voice,
		"--rate=" + RateArg(rate),
		"--volume=+0%",
		"--text", text,
		"--write-media", outPath,
	}
}

// RateArg maps 0.5..2.0 onto -50%..+100%.
func RateArg(rate float64) string {
	pct := int(math.Round((rate - 1) * 100))
	if pct < -50 {
		pct = -50
	}
	if pct > 100 {
		pct = 100
	}
	if pct >= 0 {
		return fmt.Sprintf("+%d%%", pct)
	}
	return fmt.Sprintf("%d%%", pct)
}

// RunCLI starts edge-tts and kills the process group when ctx ends.
func RunCLI(ctx context.Context, bin string, args []string) error {
	cmd := exec.Command(bin, args...)
	setGroup(cmd)
	stderr := &limited{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			msg := stderr.string()
			if msg != "" {
				return errors.New(msg)
			}
			return err
		}
		return nil
	case <-ctx.Done():
		killGroup(cmd)
		<-wait
		return ctx.Err()
	}
}

type limited struct {
	buf []byte
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	if len(l.buf) > l.max {
		l.buf = append([]byte(nil), l.buf[len(l.buf)-l.max:]...)
	}
	return len(p), nil
}

func (l *limited) string() string { return string(l.buf) }
