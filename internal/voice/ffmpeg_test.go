package voice

import (
	"net"
	"strings"
	"testing"
)

func TestRTPArgs(t *testing.T) {
	sentence := "今天要说的话"
	malicious := "音色;rm -rf /"
	ip := net.ParseIP("2001:db8::1")
	args := rtpArgs("ffmpeg", ip, 1000, "1111", "111", 48)
	joined := strings.Join(args, "\n")
	if strings.Contains(joined, "\n-re\n") || strings.HasPrefix(joined, "-re\n") || containsArg(args, "-re") {
		t.Fatalf("has -re: %#v", args)
	}
	if !strings.Contains(joined, "rtp://[2001:db8::1]:1000") {
		t.Fatalf("ipv6 %s", joined)
	}
	if !strings.Contains(joined, "48k") || strings.Contains(joined, "48000k") {
		t.Fatalf("bitrate %s", joined)
	}
	if strings.Contains(joined, sentence) || strings.Contains(joined, malicious) {
		t.Fatalf("user text leaked: %s", joined)
	}

	v4 := rtpArgs("ffmpeg", net.ParseIP("203.0.113.8"), 1000, "1111", "111", 48)
	if !strings.Contains(strings.Join(v4, "\n"), "rtp://203.0.113.8:1000") {
		t.Fatalf("ipv4 %#v", v4)
	}

	raw := []byte(`{"ip":"203.0.113.10","port":"1000","rtcp_port":"10001","bitrate":48000,"audio_ssrc":"1111","audio_pt":"111","rtcp_mux":false}`)
	info, err := ParseJoin(raw)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kbps != 48 || info.Port != 1000 || info.RTCPPort != 10001 {
		t.Fatalf("%+v", info)
	}
	pushed := info.Args("ffmpeg")
	last := pushed[len(pushed)-1]
	if !strings.Contains(last, "rtp://203.0.113.10:1000?rtcpport=10001") {
		t.Fatalf("rtcp %s", last)
	}
	if strings.Contains(strings.Join(pushed, "\n"), sentence) {
		t.Fatal("sentence leaked into push args")
	}
	decoded := decodeArgs("ffmpeg", "/data/audio/tmp/ab.mp3", 0.8)
	if strings.Contains(strings.Join(decoded, "\n"), sentence) || containsArg(decoded, "-re") {
		t.Fatalf("decode %#v", decoded)
	}

	if _, err := ParseJoin([]byte(`{"ip":"fe80::1%eth0","port":1,"rtcp_port":1}`)); err == nil {
		t.Fatal("zone id accepted")
	}
	if _, err := ParseJoin([]byte(`{"ip":"not-an-ip","port":1,"rtcp_port":1}`)); err == nil {
		t.Fatal("hostname accepted")
	}
	num := []byte(`{"ip":"203.0.113.10","port":1000,"rtcp_port":10001,"bitrate":"48000"}`)
	info, err = ParseJoin(num)
	if err != nil || info.Kbps != 48 {
		t.Fatalf("%+v %v", info, err)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
