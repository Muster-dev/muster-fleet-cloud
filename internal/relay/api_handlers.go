package relay

import (
	"encoding/json"
	"net/http"

	"github.com/Muster-dev/muster-fleet-cloud/internal/auth"
)

type createTokenRequest struct {
	Type  string `json:"type"`
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

type tokenResponse struct {
	ID        string `json:"id"`
	Token     string `json:"token,omitempty"`
	Type      string `json:"type"`
	OrgID     string `json:"org_id"`
	Name      string `json:"name"`
	Used      bool   `json:"used"`
	CreatedAt string `json:"created_at"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.OrgID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "org_id is required"})
		return
	}

	var prefix string
	var tt auth.TokenType
	switch req.Type {
	case "agent-join":
		prefix = auth.PrefixAgentJoin
		tt = auth.TypeAgentJoin
	case "cli":
		prefix = auth.PrefixCLI
		tt = auth.TypeCLI
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "type must be 'agent-join' or 'cli'",
		})
		return
	}

	if req.Name == "" {
		req.Name = req.Type
	}

	raw, hash, err := auth.GenerateToken(prefix)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	id, err := s.tokenStore.CreateToken(hash, tt, req.OrgID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to store token"})
		return
	}

	tok := s.findTokenByID(id)
	writeJSON(w, http.StatusCreated, tokenResponse{
		ID:        id,
		Token:     raw,
		Type:      string(tt),
		OrgID:     req.OrgID,
		Name:      req.Name,
		CreatedAt: tok.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	tokens := s.tokenStore.ListTokens(orgID)

	result := make([]tokenResponse, 0, len(tokens))
	for _, tok := range tokens {
		result = append(result, tokenResponse{
			ID:        tok.ID,
			Type:      string(tok.TokenType),
			OrgID:     tok.OrgID,
			Name:      tok.Name,
			Used:      tok.Used,
			CreatedAt: tok.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"tokens": result})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token id required"})
		return
	}

	if err := s.tokenStore.RevokeToken(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) findTokenByID(id string) *auth.Token {
	tokens := s.tokenStore.ListTokens("")
	for i := range tokens {
		if tokens[i].ID == id {
			return &tokens[i]
		}
	}
	return &auth.Token{}
}
