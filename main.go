package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"bsdx/internal/config"
	"bsdx/internal/httpserver"
	"bsdx/internal/kook"
	"bsdx/internal/logx"
	"bsdx/internal/module"
	"bsdx/internal/secret"
	"bsdx/internal/speak"
	"bsdx/internal/store"
	"bsdx/internal/voice"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("KOOKBOT_HTTP_ADDR")
		if addr == "" {
			addr = config.DefaultHTTPAddr
		}
		if err := httpserver.RunHealthcheck(addr); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed")
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run() error {
	logger := logx.New(os.Stdout, slog.LevelInfo)
	if err := loadDotEnv(); err != nil {
		logger.Error("dotenv", "component", "http", "err", err)
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "component", "http", "err", err)
		return err
	}
	logger = logx.New(os.Stdout, cfg.LogLevel)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Chmod(0o600); err != nil {
		return err
	}
	if err := noteSecretFingerprint(logger, st, cfg.Secret); err != nil {
		return err
	}

	box, err := secret.New([]byte(cfg.Secret))
	if err != nil {
		return err
	}
	creds, err := httpserver.LoadCredentials(context.Background(), st, box)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "audio", "cache"), 0o700); err != nil {
		return fmt.Errorf("create audio cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "audio", "tmp"), 0o700); err != nil {
		return fmt.Errorf("create audio tmp: %w", err)
	}

	client := kook.New(cfg.BaseURL, logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	live := httpserver.NewLiveStatus()
	session := voice.New(voiceHooks(client, creds, cfg.FFmpeg))
	session.SetBooting()
	svc := speak.New(st, box, speak.VoiceRoom{Session: session}, cfg.EdgeTTS, cfg.DataDir, cfg.TTSAllowPrivate, logger)
	svc.SetMirror(speak.NewMirror(client, creds.Token, st.Binding))
	session.SetBusy(svc.Busy)
	session.SetSelfID(creds.BotID)

	host := module.New()
	if err := host.Register(speak.Module{Note: session.Note}); err != nil {
		return err
	}
	gw := &kook.Gateway{
		Fetch:    kook.HTTPClientFetch(client),
		Token:    creds.Token,
		Sessions: st,
		Host:     host,
		Log:      logger,
		Status:   live.SetGateway,
	}
	creds.SetOnChange(gw.Restart)
	gw.Start(ctx)
	go creds.Refresh(ctx, client)
	go session.Boot(ctx)
	go session.WatchIdle(ctx)
	go watchVoice(ctx, live, session)

	srv, err := httpserver.New(httpserver.Deps{
		Config:   cfg,
		Store:    st,
		Log:      logger,
		Status:   live,
		Host:     host,
		Sessions: httpserver.NewSessions(),
		Box:      box,
		Kook:     client,
		Creds:    creds,
		Speak:    svc,
		Voice:    session,
	}, []httpserver.MountFunc{httpserver.MountPanel, httpserver.MountTTS, httpserver.MountSpeak})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "component", "http", "addr", cfg.HTTPAddr)
		err := httpSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func voiceHooks(client *kook.Client, creds *httpserver.Credentials, ffmpeg string) voice.Hooks {
	need := func() (string, error) {
		token, ok := creds.Token()
		if !ok || token == "" {
			return "", errors.New("token missing")
		}
		return token, nil
	}
	return voice.Hooks{
		Join: func(ctx context.Context, channelID, password string) (json.RawMessage, error) {
			token, err := need()
			if err != nil {
				return nil, err
			}
			return client.VoiceJoin(ctx, token, channelID, password)
		},
		Leave: func(ctx context.Context, channelID string) error {
			token, err := need()
			if err != nil {
				return err
			}
			return client.VoiceLeave(ctx, token, channelID)
		},
		KeepAlive: func(ctx context.Context, channelID string) error {
			token, err := need()
			if err != nil {
				return err
			}
			return client.VoiceKeepAlive(ctx, token, channelID)
		},
		UserList: func(ctx context.Context, channelID string) ([]voice.Person, error) {
			token, err := need()
			if err != nil {
				return nil, err
			}
			users, err := client.ChannelUsers(ctx, token, channelID)
			if err != nil {
				return nil, err
			}
			people := make([]voice.Person, 0, len(users))
			for _, user := range users {
				name := user.Username
				if user.IdentifyNum != "" {
					name += "#" + user.IdentifyNum
				}
				people = append(people, voice.Person{ID: user.ID, Name: name})
			}
			return people, nil
		},
		List: func(ctx context.Context) ([]string, error) {
			token, err := need()
			if err != nil {
				return nil, err
			}
			return client.VoiceList(ctx, token)
		},
		StartRTP: voice.PushLauncher(ffmpeg),
	}
}

func watchVoice(ctx context.Context, live *httpserver.LiveStatus, session *voice.Session) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		live.SetVoice(session.State())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func loadDotEnv() error {
	err := godotenv.Load()
	if err == nil {
		return nil
	}
	var pe *os.PathError
	if errors.As(err, &pe) && os.IsNotExist(pe.Err) {
		return nil
	}
	return errors.New("cannot read .env")
}

func noteSecretFingerprint(logger *slog.Logger, st *store.Store, raw string) error {
	fp := secret.Fingerprint([]byte(raw))
	stored, err := st.SecretFP(context.Background())
	if err != nil {
		return err
	}
	if stored == "" {
		return st.SetSecretFP(context.Background(), fp)
	}
	if stored != fp {
		logger.Error("secret fingerprint mismatch", "component", "secret", "code", "secret_mismatch")
	}
	return nil
}
