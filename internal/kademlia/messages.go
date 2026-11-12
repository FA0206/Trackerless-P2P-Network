package kademlia

import "encoding/json"

// MessageType identifies which of the (currently two) Kademlia RPCs an
// Envelope carries. Paper Sec. 2.3 defines four: PING, STORE, FIND_NODE,
// FIND_VALUE. STORE/FIND_VALUE are out of scope for this pass -- see
// docs/05-iterative-parallel-lookup.md's "what's next" note and the
// top-level README.
type MessageType string

const (
	MsgPing         MessageType = "PING"
	MsgPong         MessageType = "PONG"
	MsgFindNode     MessageType = "FIND_NODE"
	MsgFindNodeResp MessageType = "FIND_NODE_RESP"
)

// Envelope is the outermost shape of every message we send. Every Kademlia
// message, request or reply, carries the sender's node ID (so the
// recipient can update its routing table, paper Sec. 2.2) and an RPC ID
// that replies must echo back (paper Sec. 2.3: "the recipient must echo a
// 160-bit random RPC ID", which here lets us match a reply to the request
// that caused it, and gives some resistance to off-path reply forgery).
type Envelope struct {
	Type     MessageType     `json:"type"`
	RPCID    ID              `json:"rpc_id"`
	SenderID ID              `json:"sender_id"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// FindNodeRequest is the payload of a FIND_NODE RPC: "who's closest to
// this ID that you know of?" (paper Sec. 2.3).
type FindNodeRequest struct {
	Target ID `json:"target"`
}

// FindNodeResponse is the payload of a FIND_NODE_RESP reply: the k closest
// contacts the responder knows of to the requested target.
type FindNodeResponse struct {
	Contacts []Contact `json:"contacts"`
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Every payload type here is a plain struct of JSON-safe fields;
		// a marshal failure would mean we changed a type to something
		// unmarshalable, which is a programming error, not a runtime one.
		panic("kademlia: failed to marshal payload: " + err.Error())
	}
	return b
}
