package agent

import (
	"encoding/json"
	"os"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
)

// StateFreshness is how recently a running agent must have refreshed its
// state file to count as connected (heartbeat acks refresh it every
// HeartbeatInterval).
const StateFreshness = 3 * HeartbeatInterval

// State is what a running agent records about its relay connection, so
// other processes (`muster-agent status --connected`, `muster cloud
// connect`) can tell whether it is online without connecting themselves
// (a second connection with the same credential would replace the
// daemon's).
type State struct {
	Connected      bool       `json:"connected"`
	Name           string     `json:"name"`
	OrgID          string     `json:"org_id"`
	Relay          string     `json:"relay"`
	PID            int        `json:"pid"`
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Live reports whether s describes a connection refreshed within
// StateFreshness of now.
func (s State) Live(now time.Time) bool {
	return s.Connected && now.Sub(s.UpdatedAt) < StateFreshness
}

// ReadState loads the state file that belongs to configPath.
func ReadState(configPath string) (State, error) {
	var s State
	raw, err := os.ReadFile(config.AgentStatePath(configPath))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}

// writeState records the connection state; failures are logged, never
// fatal (the state file is informational).
func (a *Agent) writeState(connected bool) {
	if a.configPath == "" {
		return
	}
	now := time.Now().UTC()
	a.mu.Lock()
	if connected && a.connectedSince == nil {
		a.connectedSince = &now
	}
	if !connected {
		a.connectedSince = nil
	}
	s := State{
		Connected:      connected,
		Name:           a.client.Identity(),
		OrgID:          a.cfg.Identity.OrgID,
		Relay:          a.client.URLs().WS,
		PID:            os.Getpid(),
		ConnectedSince: a.connectedSince,
		UpdatedAt:      now,
	}
	a.mu.Unlock()
	raw, _ := json.MarshalIndent(s, "", "  ")
	if err := config.WriteFileAtomic(config.AgentStatePath(a.configPath), append(raw, '\n'), 0644); err != nil {
		a.logf("write state file: %v", err)
	}
}
