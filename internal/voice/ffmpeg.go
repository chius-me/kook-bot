package voice

import (
	"fmt"
	"net"
	"strconv"
)

const chunkBytes = 19200

func rtpURL(ip net.IP, port int) string {
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("rtp://%s:%d", ip4.String(), port)
	}
	return fmt.Sprintf("rtp://[%s]:%d", ip.String(), port)
}

func rtpArgs(bin string, ip net.IP, port int, ssrc, pt string, kbps int) []string {
	tee := fmt.Sprintf("[select=a:f=rtp:ssrc=%s:payload_type=%s]%s", ssrc, pt, rtpURL(ip, port))
	return []string{
		bin, "-hide_banner", "-loglevel", "warning", "-nostdin",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:0",
		"-map", "0:a:0",
		"-acodec", "libopus", "-b:a", strconv.Itoa(kbps) + "k",
		"-ac", "2", "-ar", "48000",
		"-f", "tee", tee,
	}
}

func decodeArgs(bin, absPath string, volume float64) []string {
	vol := strconv.FormatFloat(volume, 'f', 2, 64)
	return []string{
		bin, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", absPath,
		"-filter:a", "volume=" + vol,
		"-f", "s16le", "-acodec", "pcm_s16le", "-ac", "2", "-ar", "48000",
		"pipe:1",
	}
}

// pushArgs is rtpArgs plus ?rtcpport when the join response explicitly disabled mux.
func pushArgs(bin string, ip net.IP, port int, ssrc, pt string, kbps int, mux *bool, rtcpPort int) []string {
	args := rtpArgs(bin, ip, port, ssrc, pt, kbps)
	if mux != nil && !*mux {
		args[len(args)-1] += "?rtcpport=" + strconv.Itoa(rtcpPort)
	}
	return args
}
