package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/ImJustRicky/muster-fleet-cloud/internal/protocol"
	"github.com/ImJustRicky/muster-fleet-cloud/internal/tunnel"
)

// Server is the muster-cloud relay server.
type Server struct {
	router *Router
	// TODO: add auth store
}

// NewServer creates a relay server.
func NewServer() *Server {
	return &Server{
		router: NewRouter(),
	}
}

// Router returns the server's connection router.
func (s *Server) Router() *Router {
	return s.router
}

// Handler returns the HTTP handler for the relay.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/tunnel", tunnel.AcceptHandler(s.handleTunnel))
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/v1/agents", s.handleListAgents)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"agents":  s.router.AgentCount(),
		"version": "0.1.0",
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	// TODO: auth middleware
	orgID := r.URL.Query().Get("org_id")
	agents := s.router.ListAgents(orgID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agents": agents,
	})
}

func (s *Server) handleTunnel(ws *tunnel.WSConn) {
	// Read first frame — must be AUTH_REQUEST
	data, err := ws.Read()
	if err != nil {
		log.Printf("read auth frame: %v", err)
		return
	}

	frame, err := protocol.Decode(data)
	if err != nil {
		log.Printf("decode auth frame: %v", err)
		return
	}

	if frame.MsgType != protocol.MsgAuthRequest {
		log.Printf("expected AUTH_REQUEST, got %s", protocol.MsgTypeName(frame.MsgType))
		return
	}

	// Parse auth payload
	var authPayload struct {
		Token      string `json:"token"`
		ClientType string `json:"client_type"` // "agent" or "cli"
		OrgID      string `json:"org_id"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(frame.Payload, &authPayload); err != nil {
		log.Printf("parse auth payload: %v", err)
		return
	}

	// TODO: validate token against auth store

	// Send AUTH_RESPONSE
	authResp, _ := json.Marshal(map[string]interface{}{
		"ok":         true,
		"session_id": "sess_" + authPayload.Name,
	})
	respFrame := protocol.NewFrame(
		protocol.MsgAuthResponse,
		frame.RequestID,
		"relay",
		authPayload.OrgID+"/"+authPayload.Name,
		0,
		authResp,
	)
	ws.Write(protocol.Encode(respFrame))

	identity := authPayload.OrgID + "/" + authPayload.Name
	conn := &Conn{
		WS:          ws,
		Identity:    identity,
		ClientType:  authPayload.ClientType,
		OrgID:       authPayload.OrgID,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}

	if authPayload.ClientType == "agent" {
		// Wait for AGENT_HELLO
		helloData, err := ws.Read()
		if err != nil {
			log.Printf("read agent hello: %v", err)
			return
		}
		helloFrame, err := protocol.Decode(helloData)
		if err != nil {
			log.Printf("decode agent hello: %v", err)
			return
		}

		if helloFrame.MsgType == protocol.MsgAgentHello {
			var hello struct {
				Version   string `json:"version"`
				PublicKey string `json:"public_key"`
			}
			json.Unmarshal(helloFrame.Payload, &hello)
			conn.Version = hello.Version
			conn.PublicKey = hello.PublicKey
		}

		s.router.RegisterAgent(identity, conn)
		defer s.router.Unregister(identity)

		// Send RELAY_ACK
		ack := protocol.NewFrame(
			protocol.MsgRelayAck,
			helloFrame.RequestID,
			"relay",
			identity,
			0,
			nil,
		)
		ws.Write(protocol.Encode(ack))

		log.Printf("agent registered: %s", identity)
	} else {
		s.router.RegisterClient(identity, conn)
		defer s.router.Unregister(identity)
		log.Printf("cli connected: %s", identity)
	}

	// Message routing loop
	s.routeLoop(ws, conn)
}

func (s *Server) routeLoop(ws *tunnel.WSConn, conn *Conn) {
	for {
		data, err := ws.Read()
		if err != nil {
			return
		}

		frame, err := protocol.Decode(data)
		if err != nil {
			log.Printf("decode frame from %s: %v", conn.Identity, err)
			continue
		}

		conn.LastSeen = time.Now()

		switch frame.MsgType {
		case protocol.MsgHeartbeat:
			ack := protocol.NewFrame(
				protocol.MsgHeartbeatAck,
				frame.RequestID,
				"relay",
				conn.Identity,
				0,
				nil,
			)
			ws.Write(protocol.Encode(ack))

		case protocol.MsgAgentListRequest:
			agents := s.router.ListAgents(conn.OrgID)
			payload, _ := json.Marshal(map[string]interface{}{
				"agents": agents,
			})
			resp := protocol.NewFrame(
				protocol.MsgAgentList,
				frame.RequestID,
				"relay",
				conn.Identity,
				0,
				payload,
			)
			ws.Write(protocol.Encode(resp))

		default:
			// Route to destination — relay never reads payload
			destID := protocol.ParseID(frame.DestID)
			destConn, ok := s.router.Route(destID)
			if !ok {
				errPayload, _ := json.Marshal(map[string]string{
					"code":    protocol.ErrNotConnected,
					"message": "agent " + destID + " is not connected",
				})
				errFrame := protocol.NewFrame(
					protocol.MsgError,
					frame.RequestID,
					"relay",
					conn.Identity,
					0,
					errPayload,
				)
				ws.Write(protocol.Encode(errFrame))
				continue
			}

			// Forward frame unchanged
			destConn.mu.Lock()
			destConn.WS.Write(data)
			destConn.mu.Unlock()
		}
	}
}
