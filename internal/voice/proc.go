package voice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

type cmdProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	tail   *tailBuf
	done   chan struct{}
	err    error
	alive  bool
	mu     sync.Mutex
	onceIn sync.Once
}

func startPush(bin string, info JoinInfo) (Process, error) {
	if bin == "" {
		bin = "ffmpeg"
	}
	args := info.Args(bin)
	cmd := exec.Command(args[0], args[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuf{max: 2048}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &cmdProc{cmd: cmd, stdin: stdin, tail: tail, done: make(chan struct{}), alive: true}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.alive = false
		if err != nil {
			msg := tail.String()
			if msg != "" {
				err = errors.New(msg)
			}
			p.err = err
		}
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

func (p *cmdProc) Write(b []byte) (int, error) { return p.stdin.Write(b) }

func (p *cmdProc) CloseStdin() error {
	var err error
	p.onceIn.Do(func() {
		if p.stdin != nil {
			err = p.stdin.Close()
		}
	})
	return err
}

func (p *cmdProc) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *cmdProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

func (p *cmdProc) Done() <-chan struct{} { return p.done }

func (p *cmdProc) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// PushLauncher launches ffmpeg with the checked join info.
func PushLauncher(bin string) func(context.Context, JoinInfo) (Process, error) {
	return func(_ context.Context, info JoinInfo) (Process, error) {
		return startPush(bin, info)
	}
}

type tailBuf struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// DecodePCM runs a short-lived ffmpeg and returns s16le PCM.
func DecodePCM(ctx context.Context, bin, absPath string, volume float64) ([]byte, error) {
	if bin == "" {
		bin = "ffmpeg"
	}
	args := decodeArgs(bin, absPath, volume)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout bytes.Buffer
	tail := &tailBuf{max: 2048}
	cmd.Stdout = &stdout
	cmd.Stderr = tail
	timer := time.AfterFunc(30*time.Second, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	if err := cmd.Run(); err != nil {
		msg := tail.String()
		if msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	if stdout.Len() == 0 {
		return nil, errors.New("empty pcm")
	}
	return stdout.Bytes(), nil
}
