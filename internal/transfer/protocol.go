package transfer

import "kademlia-demo/internal/kademlia"

// The TCP wire protocol between a downloader (client) and a provider
// (server). One connection carries any number of exchanges, one at a time:
//
//	client -> server:  frame( JSON request )
//	server -> client:  frame( JSON responseHeader )
//	                   frame( raw bytes )           only if header.OK
//
// Every frame is [4-byte length][body] from internal/framing. TCP is a
// byte stream with no message boundaries of its own, so the length prefix
// is what tells the reader where one message ends. The data frame is raw
// bytes rather than JSON, so a piece isn't inflated by base64.
// See docs/07-file-transfer-over-tcp.md.

const (
	opGetManifest = "GET_MANIFEST"
	opGetPiece    = "GET_PIECE"

	maxRequestFrame = 1024    // requests and headers are tiny
	maxDataFrame    = 8 << 20 // the largest manifest or piece we accept
)

type request struct {
	Op    string      `json:"op"`
	Key   kademlia.ID `json:"key"`
	Index int         `json:"index,omitempty"` // GET_PIECE only
}

type responseHeader struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
