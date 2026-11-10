package tts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEdgeArgsRateIsOneElement(t *testing.T) {
	sentence := "今天要说的话"
	args := EdgeArgs("zh-CN-XiaoxiaoNeural", 0.8, sentence, "/tmp/a.mp3")
	var rate string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--rate=") {
			if rate != "" {
				t.Fatalf("rate split across args: %#v", args)
			}
			rate = arg
		}
		if arg == "-20%" || arg == "--rate" {
			t.Fatalf("rate was split: %#v", args)
		}
	}
	if rate != "--rate=-20%" {
		t.Fatalf("rate %q args %#v", rate, args)
	}
	if !strings.Contains(strings.Join(args, "\n"), sentence) {
		t.Fatal("text left the argv")
	}
}

func TestEdgeBadVoiceDoesNotRun(t *testing.T) {
	ran := false
	edge := Edge{Run: func(context.Context, string, []string) error {
		ran = true
		return nil
	}}
	err := edge.Synthesize(context.Background(), Request{
		Text:    "今天要说的话",
		VoiceID: "zh-CN-XiaoxiaoNeural;touch /tmp/pwn",
		Rate:    1,
	}, filepath.Join(t.TempDir(), "a.mp3"))
	if !errors.Is(err, ErrBadVoice) || ran {
		t.Fatalf("err %v ran %v", err, ran)
	}
}

func TestEdgeWritesMedia(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "a.mp3")
	edge := Edge{Run: func(_ context.Context, bin string, args []string) error {
		if bin != "edge-bin" {
			t.Fatalf("bin %s", bin)
		}
		return os.WriteFile(out, bytesOf(300), 0o600)
	}, Bin: "edge-bin"}
	if err := edge.Synthesize(context.Background(), Request{Text: "你好", VoiceID: DefaultEdgeVoice, Rate: 1}, out); err != nil {
		t.Fatal(err)
	}
}

func bytesOf(n int) []byte {
	return make([]byte, n)
}
