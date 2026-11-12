package kademlia

import (
	"log/slog"
	"os"
)

// NewLogger builds a structured stdout logger, tagged with this node's ID
// so that in a multi-node demo you can tell whose log line is whose. We
// use log/slog's text handler: key=value pairs are both machine-parseable
// (grep/awk-friendly) and readable directly in a terminal, which is the
// "debugging tool, not a dashboard" the project calls for.
func NewLogger(id ID, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With("node", shortID(id))
}

// shortID truncates an ID to its first 8 hex characters for log lines,
// where the full 40-character ID would be noise.
func shortID(id ID) string {
	s := id.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
