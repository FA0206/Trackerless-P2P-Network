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
// A GET_PIECE reply's header also carries the piece's Merkle proof
// (merkle.go), which the downloader checks
// against the manifest's root.
//

const (
	opGetManifest = "GET_MANIFEST"
	opGetPiece    = "GET_PIECE"
	opGetBitfield = "GET_BITFIELD" // data = Bitfield of the pieces the provider has (bitfield.go)

	maxRequestFrame = 1024    // requests are tiny
	maxHeaderFrame  = 4096    // a response header: a proof is at most maxProofLen hex hashes
	maxDataFrame    = 8 << 20 // the largest manifest or piece we accept
	// maxProofLen is the longest proof that can be genuine: a tree of
	// maxPieces (2^16) leaves is 16 levels deep.
	maxProofLen = 16
)

type request struct {
	Op    string      `json:"op"`
	Key   kademlia.ID `json:"key"`
	Index int         `json:"index,omitempty"` // GET_PIECE only
}

type responseHeader struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Proof []string `json:"proof,omitempty"` // GET_PIECE only: hex sibling hashes, nearest first
}
