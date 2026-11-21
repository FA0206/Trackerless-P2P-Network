package transfer

import (
	"fmt"
	"net/url"
	"strings"

	"kademlia-demo/internal/kademlia"
)

// A magnet link is a content key packaged for people to pass around: in a
// chat message, on a web page, as a QR code. The DHT finds *who has* a
// file; it can't tell you *which* files exist.
// Real trackerless BitTorrent works the same way: its magnet links (BEP 9)
// look like
//
//	magnet:?xt=urn:btih:<info hash>&dn=<display name>
//
// and ours have the same shape:
//
//	magnet:?xt=urn:tp2p:<40 hex chars>&dn=<file name>
//
// xt ("exact topic") is the content key, the only part a download needs.
// dn ("display name") is for people to read. Nothing checks it, so it is
// never used as a file path and never trusted: a link can lie about a
// file's name, but not about its bytes, because the content key verifies
// the manifest and the manifest verifies every piece.
//
// Why "tp2p" (Trackerless P2P) and not "btih"? btih means "BitTorrent info
// hash": SHA-1 of a bencoded info dictionary. Our key is SHA-256 of a JSON
// manifest, cut to 160 bits (ContentKey). Labelling it btih would send a
// real BitTorrent client looking for a torrent that doesn't exist.
const (
	magnetScheme = "magnet:"
	magnetURN    = "urn:tp2p:"
	btihURN      = "urn:btih:" // recognized only to explain why it's refused
)

// MagnetLink returns the magnet link for a content key. name becomes the
// dn parameter (URL-escaped, so a space is "+" and the link never contains
// a space); an empty name leaves dn out.
func MagnetLink(key kademlia.ID, name string) string {
	link := magnetScheme + "?xt=" + magnetURN + key.String()
	if name != "" {
		link += "&dn=" + url.QueryEscape(name)
	}
	return link
}

// ParseMagnet extracts the content key and display name from a magnet
// link. Following BEP 9 practice, parameters it doesn't know (tr, x.pe,
// ...) are ignored, the order of parameters doesn't matter, and dn is
// optional. Exactly one xt is required.
func ParseMagnet(link string) (key kademlia.ID, name string, err error) {
	u, err := url.Parse(link)
	if err != nil {
		return key, "", fmt.Errorf("transfer: not a magnet link: %w", err)
	}
	if u.Scheme != "magnet" { // url.Parse lowercases the scheme
		return key, "", fmt.Errorf("transfer: %q is not a magnet link (want magnet:?xt=%s<key>)", link, magnetURN)
	}
	params, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return key, "", fmt.Errorf("transfer: magnet link has a malformed parameter: %w", err)
	}
	xts := params["xt"]
	if len(xts) != 1 {
		return key, "", fmt.Errorf("transfer: magnet link has %d xt= parameters, want exactly one (xt=%s<key>)", len(xts), magnetURN)
	}
	xt := xts[0]
	switch lower := strings.ToLower(xt); {
	case strings.HasPrefix(lower, magnetURN):
	case strings.HasPrefix(lower, btihURN):
		return key, "", fmt.Errorf("transfer: this is a BitTorrent magnet link (xt=%s...): it names a torrent "+
			"in the BitTorrent network, not a file in this one, whose links use xt=%s<key>", btihURN, magnetURN)
	default:
		return key, "", fmt.Errorf("transfer: magnet link's xt=%q is not ours, want xt=%s<40 hex chars>", xt, magnetURN)
	}
	key, err = kademlia.IDFromHex(xt[len(magnetURN):])
	if err != nil {
		return key, "", fmt.Errorf("transfer: magnet link's content key: %w", err)
	}
	return key, params.Get("dn"), nil
}

// ParseKeyOrMagnet accepts what a user would paste into fetch: either a
// bare 40-hex content key or a magnet link. It returns only the key,
// because the key is all a download uses.
func ParseKeyOrMagnet(s string) (kademlia.ID, error) {
	s = strings.TrimSpace(s)
	if len(s) >= len(magnetScheme) && strings.EqualFold(s[:len(magnetScheme)], magnetScheme) {
		key, _, err := ParseMagnet(s)
		return key, err
	}
	key, err := kademlia.IDFromHex(s)
	if err != nil {
		return key, fmt.Errorf("transfer: want a 40-hex-char content key or a magnet:?xt=%s... link: %w", magnetURN, err)
	}
	return key, nil
}

// Magnet is this file's magnet link, named by its manifest.
func (f SharedFile) Magnet() string {
	return MagnetLink(f.Key, f.Name)
}
