package main

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"kademlia-demo/internal/nodeapi"
)

// startAPI serves the per-node HTTP API (internal/nodeapi)
// on addr, e.g. "127.0.0.1:8000". It returns a function that stops the
// server. Only called when --api is set; without it the node opens no
// HTTP socket at all.
func startAPI(addr string, cfg nodeapi.Config, logger *slog.Logger) (stop func(), err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           nodeapi.New(cfg).Handler(),
		ReadHeaderTimeout: 5 * time.Second, // don't let an idle client hold a connection forever
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api server stopped", "error", err)
		}
	}()
	logger.Info("api listening", "addr", ln.Addr().String())
	return func() { srv.Close() }, nil
}
