package kademlia

import "encoding/json"

// MessageType identifies which of the four Kademlia RPCs (paper Sec. 2.3:
// PING, STORE, FIND_NODE, FIND_VALUE), or which reply to one, an Envelope
// carries.
type MessageType string

const (
	MsgPing          MessageType = "PING"
	MsgPong          MessageType = "PONG"
	MsgFindNode      MessageType = "FIND_NODE"
	MsgFindNodeResp  MessageType = "FIND_NODE_RESP"
	MsgStore         MessageType = "STORE"
	MsgStoreResp     MessageType = "STORE_RESP"
	MsgFindValue     MessageType = "FIND_VALUE"
	MsgFindValueResp MessageType = "FIND_VALUE_RESP"
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

// StoreRequest is the payload of a STORE RPC: "please keep this value
// under this key" (paper Sec. 2.3). Value is opaque bytes to the DHT; it
// travels base64-encoded in JSON. A string would be more readable in a
// packet capture, but JSON strings can't carry arbitrary binary data
// without mangling it.
type StoreRequest struct {
	Key   ID     `json:"key"`
	Value []byte `json:"value"`
}

// StoreResponse acknowledges a STORE. The paper doesn't require a reply,
// but having one tells the publisher how many of the k nodes really kept
// the value (and every reply refreshes the publisher's routing table).
type StoreResponse struct {
	Stored bool   `json:"stored"`
	Reason string `json:"reason,omitempty"` // why not, when Stored is false
}

// FindValueRequest is the payload of a FIND_VALUE RPC.
type FindValueRequest struct {
	Key ID `json:"key"`
}

// FindValueResponse is FIND_NODE's reply with one exception (paper
// Sec. 2.3): if the responder holds values for the key it returns them
// in Values; otherwise it returns its k closest contacts, exactly like
// FIND_NODE, so the lookup can keep going.
type FindValueResponse struct {
	Values   [][]byte  `json:"values,omitempty"`
	Contacts []Contact `json:"contacts,omitempty"`
}
