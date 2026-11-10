package httpserver

import "sync/atomic"

// LiveStatus is the process gateway and voice state. /healthz stays 200 when the gateway is down.
type LiveStatus struct {
	gateway atomic.Value
	voice   atomic.Value
}

func NewLiveStatus() *LiveStatus {
	s := &LiveStatus{}
	s.gateway.Store("down")
	s.voice.Store("idle")
	return s
}

func (s *LiveStatus) SetGateway(state string) {
	if s == nil || state == "" {
		return
	}
	s.gateway.Store(state)
}

func (s *LiveStatus) SetVoice(state string) {
	if s == nil || state == "" {
		return
	}
	s.voice.Store(state)
}

func (s *LiveStatus) Gateway() string {
	if s == nil {
		return "down"
	}
	v, _ := s.gateway.Load().(string)
	if v == "" {
		return "down"
	}
	return v
}

func (s *LiveStatus) Voice() string {
	if s == nil {
		return "idle"
	}
	v, _ := s.voice.Load().(string)
	if v == "" {
		return "idle"
	}
	return v
}
