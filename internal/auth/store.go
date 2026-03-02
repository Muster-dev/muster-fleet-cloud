package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// TokenStore manages hashed token records on disk.
type TokenStore struct {
	mu   sync.RWMutex
	path string
	data storeData
}

type storeData struct {
	Tokens []Token `json:"tokens"`
}

// NewTokenStore opens (or creates) a JSON token store at the given path.
func NewTokenStore(path string) (*TokenStore, error) {
	s := &TokenStore{path: path}

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s.data = storeData{}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read token store: %w", err)
	}

	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("parse token store: %w", err)
	}
	return s, nil
}

// CreateToken adds a new hashed token record and persists to disk.
// Returns the token ID.
func (s *TokenStore) CreateToken(hash string, tokenType TokenType, orgID, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("tok_%d", time.Now().UnixNano())
	tok := Token{
		ID:        id,
		Hash:      hash,
		OrgID:     orgID,
		TokenType: tokenType,
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
	s.data.Tokens = append(s.data.Tokens, tok)

	if err := s.persist(); err != nil {
		return "", err
	}
	return id, nil
}

// ValidateToken checks a raw token against the store.
// Returns the matching Token record if valid, or an error.
func (s *TokenStore) ValidateToken(raw string) (*Token, error) {
	hash := HashToken(raw)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.data.Tokens {
		if s.data.Tokens[i].Hash == hash {
			tok := &s.data.Tokens[i]
			if tok.Used {
				return nil, fmt.Errorf("token already used")
			}
			return tok, nil
		}
	}
	return nil, fmt.Errorf("invalid token")
}

// MarkUsed marks a single-use token as used and persists.
func (s *TokenStore) MarkUsed(tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Tokens {
		if s.data.Tokens[i].ID == tokenID {
			s.data.Tokens[i].Used = true
			s.data.Tokens[i].LastUsedAt = time.Now().UTC()
			return s.persist()
		}
	}
	return fmt.Errorf("token %s not found", tokenID)
}

// RevokeToken removes a token by ID and persists.
func (s *TokenStore) RevokeToken(tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Tokens {
		if s.data.Tokens[i].ID == tokenID {
			s.data.Tokens = append(s.data.Tokens[:i], s.data.Tokens[i+1:]...)
			return s.persist()
		}
	}
	return fmt.Errorf("token %s not found", tokenID)
}

// ListTokens returns all tokens for an org (or all if orgID is empty).
func (s *TokenStore) ListTokens(orgID string) []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Token
	for _, tok := range s.data.Tokens {
		if orgID == "" || tok.OrgID == orgID {
			result = append(result, tok)
		}
	}
	return result
}

func (s *TokenStore) persist() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token store: %w", err)
	}
	if err := os.WriteFile(s.path, raw, 0600); err != nil {
		return fmt.Errorf("write token store: %w", err)
	}
	return nil
}
