package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

// fakeAgentRelay plays relay + agent for one CLI session. After COMMAND it
// streams "running"; on COMMAND_CANCEL it reports exit 130 unless silent.
func fakeAgentRelay(t *testing.T, agent *crypto.KeyPair, silent bool, got chan<- protocol.CommandRequest, cancelled chan<- struct{}) string {
	t.Helper()
	srv := httptest.NewServer(AcceptHandler(func(ws *WSConn) {
		read := func() *protocol.Frame {
			data, err := ws.Read()
			if err != nil {
				return nil
			}
			f, _ := protocol.Decode(data)
			return f
		}
		f := read()
		p, _ := json.Marshal(protocol.AuthResponse{OK: true, OrgID: "org_t", Name: "cli-12345678"})
		_ = ws.Write(protocol.Encode(protocol.NewFrame(protocol.MsgAuthResponse, f.RequestID, protocol.RelayID, "cli-12345678", 0, p)))
		var cliPub [32]byte
		for {
			f := read()
			if f == nil {
				return
			}
			reply := func(typ uint8, v any) {
				raw, _ := json.Marshal(v)
				sealed, _ := crypto.Seal(raw, &cliPub, agent)
				_ = ws.Write(protocol.Encode(protocol.NewFrame(typ, f.RequestID, "prod-1", "cli-12345678", protocol.FlagEncrypted, sealed)))
			}
			switch f.MsgType {
			case protocol.MsgCommand:
				sender, pt, err := crypto.Open(f.Payload, agent)
				if err != nil {
					t.Errorf("open: %v", err)
					return
				}
				cliPub = sender
				var req protocol.CommandRequest
				_ = json.Unmarshal(pt, &req)
				got <- req
				_ = ws.Write(protocol.Encode(protocol.NewFrame(protocol.MsgCommandAck, f.RequestID, "prod-1", "cli-12345678", 0, nil)))
				reply(protocol.MsgStreamData, protocol.StreamLine{Line: "running", Stream: "stdout"})
			case protocol.MsgCommandCancel:
				cancelled <- struct{}{}
				if !silent {
					reply(protocol.MsgCommandResult, protocol.CommandResult{ExitCode: 130, Status: protocol.StatusCancelled})
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestExecCancelSendsCommandCancel(t *testing.T) {
	for _, silent := range []bool{false, true} {
		agent, _ := crypto.GenerateKeyPair()
		cliKeys, _ := crypto.GenerateKeyPair()
		got := make(chan protocol.CommandRequest, 1)
		cancelled := make(chan struct{}, 1)
		url := fakeAgentRelay(t, agent, silent, got, cancelled)

		s, err := DialSession(context.Background(), url, "mst_cli_x", SessionOptions{Keys: cliKeys})
		if err != nil {
			t.Fatal(err)
		}
		s.CancelGrace = 300 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		code, err := s.Exec(ctx, "prod-1", agent.PublicKey, protocol.CommandRequest{Action: "exec", Command: "muster logs api"}, func(l protocol.StreamLine) {
			if l.Line == "running" {
				cancel()
			}
		})
		s.Close()
		if code != protocol.ExitCancelled || !errors.Is(err, context.Canceled) {
			t.Fatalf("silent=%v: code %d err %v", silent, code, err)
		}
		select {
		case <-cancelled:
		default:
			t.Fatalf("silent=%v: agent never got COMMAND_CANCEL", silent)
		}
		if req := <-got; !req.PersistentKey {
			t.Error("persistent_key not set with a long-lived key")
		}
		if el := time.Since(start); silent && el < s.CancelGrace {
			t.Errorf("returned after %v, before the grace period", el)
		}
	}
}
