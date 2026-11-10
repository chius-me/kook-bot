package httpserver

// RuntimeStatus is filled in by later packages. PR1 has no gateway and no voice session.
type RuntimeStatus interface {
	Gateway() string
	Voice() string
}

// DownStatus reports a process that is up but not connected to KOOK.
type DownStatus struct{}

func (DownStatus) Gateway() string { return "down" }
func (DownStatus) Voice() string   { return "idle" }
