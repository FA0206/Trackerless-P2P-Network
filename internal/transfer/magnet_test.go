package transfer

import (
	"strings"
	"testing"

	"kademlia-demo/internal/kademlia"
)

// testKeyHex is the content key of the 150 000-byte notes.txt worked
// through in docs/10-magnet-links.md.
const testKeyHex = "fb20c6c640716af0e3bded02f5c3f01b3dc9ab62"

func mustKey(t *testing.T) kademlia.ID {
	t.Helper()
	key, err := kademlia.IDFromHex(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestMagnetLinkFormat(t *testing.T) {
	key := mustKey(t)
	cases := []struct{ name, want string }{
		{"notes.txt", "magnet:?xt=urn:tp2p:" + testKeyHex + "&dn=notes.txt"},
		{"", "magnet:?xt=urn:tp2p:" + testKeyHex}, // no name: no dn
		{"my holiday photo.jpg", "magnet:?xt=urn:tp2p:" + testKeyHex + "&dn=my+holiday+photo.jpg"},
	}
	for _, c := range cases {
		if got := MagnetLink(key, c.name); got != c.want {
			t.Errorf("MagnetLink(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// A link must survive the console, which splits a line on whitespace, and
// come back with the same key and name.
func TestMagnetLinkRoundTrip(t *testing.T) {
	key := mustKey(t)
	for _, name := range []string{"notes.txt", "my holiday photo.jpg", "a&b=c?d#e%f+g.txt", "résumé.pdf", "", "../../etc/passwd"} {
		link := MagnetLink(key, name)
		if strings.ContainsAny(link, " \t\n") {
			t.Errorf("link for %q contains whitespace: %q", name, link)
		}
		gotKey, gotName, err := ParseMagnet(link)
		if err != nil {
			t.Errorf("ParseMagnet(%q): %v", link, err)
			continue
		}
		if gotKey != key || gotName != name {
			t.Errorf("round trip of %q: got key %s name %q", name, gotKey, gotName)
		}
	}
}

func TestParseMagnetAccepts(t *testing.T) {
	key := mustKey(t)
	cases := []struct{ link, wantName string }{
		{"MAGNET:?xt=urn:tp2p:" + testKeyHex + "&dn=x", "x"},                             // scheme in any case
		{"magnet:?xt=URN:TP2P:" + strings.ToUpper(testKeyHex), ""},                       // URN and hex in any case
		{"magnet:?dn=notes.txt&xt=urn:tp2p:" + testKeyHex, "notes.txt"},                  // order doesn't matter
		{"magnet:?xt=urn:tp2p:" + testKeyHex + "&tr=udp%3A%2F%2Fx&x.pe=1.2.3.4%3A5", ""}, // unknown parameters ignored
		{"magnet:?xt=urn:tp2p:" + testKeyHex + "&dn=caf%C3%A9", "café"},                  // percent-escaped UTF-8
	}
	for _, c := range cases {
		gotKey, gotName, err := ParseMagnet(c.link)
		if err != nil || gotKey != key || gotName != c.wantName {
			t.Errorf("ParseMagnet(%q) = %s, %q, %v; want %s, %q", c.link, gotKey, gotName, err, key, c.wantName)
		}
	}
}

func TestParseMagnetRejects(t *testing.T) {
	cases := []struct{ link, wantErr string }{
		{"magnet:?xt=urn:btih:" + testKeyHex + "&dn=x", "BitTorrent magnet link"},
		{"magnet:?dn=notes.txt", "0 xt= parameters"},
		{"magnet:?xt=urn:tp2p:" + testKeyHex + "&xt=urn:tp2p:" + testKeyHex, "2 xt= parameters"},
		{"magnet:?xt=urn:sha1:" + testKeyHex, "is not ours"},
		{"http://example.com/?xt=urn:tp2p:" + testKeyHex, "is not a magnet link"},
		{"magnet:?xt=urn:tp2p:" + testKeyHex[:38], "content key"},         // too short
		{"magnet:?xt=urn:tp2p:" + strings.Repeat("z", 40), "content key"}, // not hex
		{"magnet:?xt=urn:tp2p:" + testKeyHex + "&dn=%zz", "malformed parameter"},
	}
	for _, c := range cases {
		_, _, err := ParseMagnet(c.link)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("ParseMagnet(%q): error %v, want one containing %q", c.link, err, c.wantErr)
		}
	}
}

func TestParseKeyOrMagnet(t *testing.T) {
	key := mustKey(t)
	for _, s := range []string{testKeyHex, " " + testKeyHex + "\n", MagnetLink(key, "notes.txt"), "Magnet:?xt=urn:tp2p:" + testKeyHex} {
		got, err := ParseKeyOrMagnet(s)
		if err != nil || got != key {
			t.Errorf("ParseKeyOrMagnet(%q) = %s, %v; want %s", s, got, err, key)
		}
	}
	_, err := ParseKeyOrMagnet("notes.txt")
	if err == nil || !strings.Contains(err.Error(), "content key") || !strings.Contains(err.Error(), "magnet") {
		t.Errorf("ParseKeyOrMagnet(garbage): error %v, want one naming both accepted forms", err)
	}
	// A magnet link's own error comes through, not the hex-key one.
	_, err = ParseKeyOrMagnet("magnet:?xt=urn:btih:" + testKeyHex)
	if err == nil || !strings.Contains(err.Error(), "BitTorrent") {
		t.Errorf("ParseKeyOrMagnet(btih link): error %v, want the BitTorrent explanation", err)
	}
}
