package tts

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestValidateSpeechURLRejectsPrivate(t *testing.T) {
	public := []net.IP{net.ParseIP("1.2.3.4")}
	private := []net.IP{net.ParseIP("10.1.2.3")}
	lookup := func(ips []net.IP, err error) Lookup {
		return func(string) ([]net.IP, error) { return ips, err }
	}
	cases := []struct {
		name   string
		raw    string
		lookup Lookup
	}{
		{name: "http", raw: "http://1.2.3.4/v1/audio/speech"},
		{name: "userinfo", raw: "https://user:pass@1.2.3.4/v1/audio/speech"},
		{name: "loopback", raw: "https://127.0.0.1/v1/audio/speech"},
		{name: "unspecified", raw: "https://0.0.0.0/v1/audio/speech"},
		{name: "private10", raw: "https://10.1.2.3/v1/audio/speech"},
		{name: "private172", raw: "https://172.16.0.1/v1/audio/speech"},
		{name: "private192", raw: "https://192.168.1.8/v1/audio/speech"},
		{name: "linklocal", raw: "https://169.254.169.254/latest"},
		{name: "cgnat", raw: "https://100.64.0.1/v1/audio/speech"},
		{name: "ipv6loop", raw: "https://[::1]/v1/audio/speech"},
		{name: "dns private", raw: "https://api.example.com/v1/audio/speech", lookup: lookup(private, nil)},
		{name: "dns error", raw: "https://api.example.com/v1/audio/speech", lookup: lookup(nil, errors.New("no such host"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSpeechURL(tc.raw, false, tc.lookup); !errors.Is(err, ErrBadURL) {
				t.Fatalf("err %v", err)
			}
			called := false
			o := OpenAI{
				URL:    tc.raw,
				APIKey: "secret",
				Model:  "tts-1",
				Lookup: tc.lookup,
				Client: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
					called = true
					return nil, errors.New("should not dial")
				})},
			}
			err := o.Synthesize(context.Background(), Request{Text: "你好", VoiceID: "alloy", Rate: 1}, filepathUnused(t))
			if !errors.Is(err, ErrBadURL) || called {
				t.Fatalf("err %v called %v", err, called)
			}
		})
	}
	if err := ValidateSpeechURL("https://1.2.3.4/v1/audio/speech", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpeechURL("https://api.example.com/v1/audio/speech", false, lookup(public, nil)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpeechURL("http://10.0.0.1/speech", true, nil); !errors.Is(err, ErrBadURL) {
		t.Fatalf("allowPrivate still needs https: %v", err)
	}
	if err := ValidateSpeechURL("https://user:pass@10.0.0.1/speech", true, nil); !errors.Is(err, ErrBadURL) {
		t.Fatalf("allowPrivate still rejects userinfo: %v", err)
	}
}

func TestOpenAISynthesizePublicURL(t *testing.T) {
	var gotPath, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write(bytesRepeat(300))
	}))
	defer srv.Close()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	out := filepathUnused(t)
	o := OpenAI{
		URL:    "https://api.example.com/v1/audio/speech",
		APIKey: "secret-key",
		Model:  "tts-1",
		Lookup: func(host string) ([]net.IP, error) {
			if host != "api.example.com" {
				t.Errorf("host %s", host)
			}
			return []net.IP{net.ParseIP("1.2.3.4")}, nil
		},
		Client: &http.Client{Transport: rewriteHost(base), CheckRedirect: refuseRedirect},
	}
	if err := o.Synthesize(context.Background(), Request{Text: "你好", VoiceID: "alloy", Rate: 1.25}, out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/audio/speech" {
		t.Fatalf("path %s", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("auth %s", gotAuth)
	}
	if body["model"] != "tts-1" || body["voice"] != "alloy" || body["response_format"] != "mp3" || body["speed"] != 1.25 {
		t.Fatalf("body %#v", body)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() < 256 || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", st, err)
	}
}

func TestCheckAPIVoice(t *testing.T) {
	if err := CheckAPIVoice("  "); !errors.Is(err, ErrBadAPIVoice) {
		t.Fatal(err)
	}
	if err := CheckAPIVoice(strings.Repeat("a", 65)); !errors.Is(err, ErrBadAPIVoice) {
		t.Fatal(err)
	}
	if err := CheckAPIVoice("bad voice"); !errors.Is(err, ErrBadAPIVoice) {
		t.Fatal(err)
	}
	if err := CheckAPIVoice("alloy"); err != nil {
		t.Fatal(err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func rewriteHost(base *url.URL) roundTripper {
	return func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = base.Scheme
		cloned.URL.Host = base.Host
		cloned.Host = base.Host
		return http.DefaultTransport.RoundTrip(cloned)
	}
}

func bytesRepeat(n int) []byte {
	return make([]byte, n)
}

func filepathUnused(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/out.mp3"
}
