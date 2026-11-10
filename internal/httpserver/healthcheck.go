package httpserver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// LoopbackAddr is the address healthcheck dials. It always uses 127.0.0.1
// and the port from the listen address, including when the process binds 0.0.0.0.
func LoopbackAddr(listen string) (string, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return "", errors.New("healthcheck: bad listen address")
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

// RunHealthcheck requests /healthz on the loopback port. The error text has no response body.
func RunHealthcheck(listen string) error {
	dest, err := LoopbackAddr(listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dest+"/healthz", nil)
	if err != nil {
		return errors.New("healthcheck: bad request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("healthcheck: unreachable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return errors.New("healthcheck: bad status")
	}
	return nil
}
