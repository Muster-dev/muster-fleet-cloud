package tunnel

import "testing"

func TestNormalizeRelayURL(t *testing.T) {
	cases := map[string]RelayURLs{
		"wss://relay.getmuster.dev":           {WS: "wss://relay.getmuster.dev", HTTP: "https://relay.getmuster.dev"},
		"https://relay.getmuster.dev/":        {WS: "wss://relay.getmuster.dev", HTTP: "https://relay.getmuster.dev"},
		"wss://relay.getmuster.dev/v1/tunnel": {WS: "wss://relay.getmuster.dev", HTTP: "https://relay.getmuster.dev"},
		"relay.getmuster.dev":                 {WS: "wss://relay.getmuster.dev", HTTP: "https://relay.getmuster.dev"},
		"ws://127.0.0.1:8443":                 {WS: "ws://127.0.0.1:8443", HTTP: "http://127.0.0.1:8443"},
		"http://localhost:8080/v1/tunnel":     {WS: "ws://localhost:8080", HTTP: "http://localhost:8080"},
	}
	for in, want := range cases {
		got, err := NormalizeRelayURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRelayURL(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	t.Setenv("MUSTER_CLOUD_INSECURE", "")
	for _, in := range []string{"ws://relay.example.com", "http://10.0.0.5:8443", "ftp://x", ""} {
		if _, err := NormalizeRelayURL(in); err == nil {
			t.Errorf("NormalizeRelayURL(%q) accepted", in)
		}
	}
}
