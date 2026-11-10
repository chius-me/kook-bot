package voice

import (
	"bytes"
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var numID = regexp.MustCompile(`^[0-9]{1,10}$`)

// JoinInfo is a checked voice/join payload. Strings from KOOK never reach argv.
type JoinInfo struct {
	IP       net.IP
	Port     int
	RTCPPort int
	RTCPMux  *bool
	Kbps     int
	SSRC     string
	PT       string
}

func (j JoinInfo) Args(bin string) []string {
	return pushArgs(bin, j.IP, j.Port, j.SSRC, j.PT, j.Kbps, j.RTCPMux, j.RTCPPort)
}

// ParseJoin accepts port, rtcp_port, and bitrate as either JSON strings or numbers.
func ParseJoin(raw []byte) (JoinInfo, error) {
	var body struct {
		IP       string          `json:"ip"`
		Port     json.RawMessage `json:"port"`
		RTCPPort json.RawMessage `json:"rtcp_port"`
		RTCPMux  *bool           `json:"rtcp_mux"`
		Bitrate  json.RawMessage `json:"bitrate"`
		SSRC     string          `json:"audio_ssrc"`
		PT       string          `json:"audio_pt"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return JoinInfo{}, err
	}
	ip, err := parseRTPIP(body.IP)
	if err != nil {
		return JoinInfo{}, err
	}
	port, err := parsePort(body.Port)
	if err != nil {
		return JoinInfo{}, err
	}
	rtcp, err := parsePort(body.RTCPPort)
	if err != nil {
		return JoinInfo{}, err
	}
	ssrc := body.SSRC
	if ssrc == "" {
		ssrc = "1111"
	}
	pt := body.PT
	if pt == "" {
		pt = "111"
	}
	if !numID.MatchString(ssrc) || !numID.MatchString(pt) {
		return JoinInfo{}, errBadMedia
	}
	return JoinInfo{
		IP:       ip,
		Port:     port,
		RTCPPort: rtcp,
		RTCPMux:  body.RTCPMux,
		Kbps:     kbpsOf(body.Bitrate),
		SSRC:     ssrc,
		PT:       pt,
	}, nil
}

var errBadMedia = errString("bad voice media")

type errString string

func (e errString) Error() string { return string(e) }

func parseRTPIP(s string) (net.IP, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "%") {
		return nil, errBadMedia
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, errBadMedia
	}
	return ip, nil
}

func parsePort(raw json.RawMessage) (int, error) {
	n, err := rawInt(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, errBadMedia
	}
	return n, nil
}

func kbpsOf(raw json.RawMessage) int {
	n, err := rawInt(raw)
	if err != nil {
		return 48
	}
	kbps := n / 1000
	if kbps < 16 {
		return 48
	}
	return kbps
}

func rawInt(raw json.RawMessage) (int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, errBadMedia
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(s))
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		f, err2 := strconv.ParseFloat(string(raw), 64)
		if err2 != nil {
			return 0, err
		}
		return int(f), nil
	}
	return n, nil
}
