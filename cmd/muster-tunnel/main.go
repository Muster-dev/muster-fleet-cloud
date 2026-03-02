package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/Muster-dev/muster-fleet-cloud/internal/protocol"
	"github.com/Muster-dev/muster-fleet-cloud/internal/tunnel"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "exec":
		cmdExec()
	case "push":
		cmdPush()
	case "ping":
		cmdPing()
	case "agents":
		cmdAgents()
	case "version":
		fmt.Printf("muster-tunnel %s\n", version)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: muster-tunnel <command> [options]")
	fmt.Println()
	fmt.Println("CLI helper for muster cloud transport. Used by 'muster fleet' for cloud machines.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  exec     Execute a command on a remote agent")
	fmt.Println("  push     Push a hook script to a remote agent")
	fmt.Println("  ping     Test connectivity to a remote agent")
	fmt.Println("  agents   List connected agents")
	fmt.Println("  version  Print version")
}

// commonFlags parses the standard --relay, --token, --org, --agent flags.
type commonFlags struct {
	relay string
	token string
	org   string
	agent string
}

func parseCommonFlags(args []string) (commonFlags, []string) {
	var f commonFlags
	var remaining []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--relay":
			i++
			if i < len(args) {
				f.relay = args[i]
			}
		case "--token":
			i++
			if i < len(args) {
				f.token = args[i]
			}
		case "--org":
			i++
			if i < len(args) {
				f.org = args[i]
			}
		case "--agent":
			i++
			if i < len(args) {
				f.agent = args[i]
			}
		default:
			remaining = append(remaining, args[i])
		}
	}
	return f, remaining
}

func connectAndAuth(f commonFlags, clientName string) (*tunnel.WSConn, error) {
	url := f.relay + "/v1/tunnel"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+f.token)

	conn, err := tunnel.Dial(url, headers)
	if err != nil {
		return nil, fmt.Errorf("connect to relay: %w", err)
	}

	// Send AUTH_REQUEST
	authPayload, _ := json.Marshal(map[string]string{
		"token":       f.token,
		"client_type": "cli",
		"org_id":      f.org,
		"name":        clientName,
	})

	var reqID [16]byte
	frame := protocol.NewFrame(
		protocol.MsgAuthRequest,
		reqID,
		f.org+"/"+clientName,
		"relay",
		0,
		authPayload,
	)

	if err := conn.Write(protocol.Encode(frame)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send auth: %w", err)
	}

	// Read AUTH_RESPONSE
	data, err := conn.Read()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read auth response: %w", err)
	}

	respFrame, err := protocol.Decode(data)
	if err != nil || respFrame.MsgType != protocol.MsgAuthResponse {
		conn.Close()
		return nil, fmt.Errorf("invalid auth response")
	}

	return conn, nil
}

func cmdExec() {
	flags, remaining := parseCommonFlags(os.Args[2:])

	var cmd string
	for i := 0; i < len(remaining); i++ {
		if remaining[i] == "--cmd" && i+1 < len(remaining) {
			cmd = remaining[i+1]
			break
		}
	}

	if flags.relay == "" || flags.token == "" || flags.org == "" || flags.agent == "" || cmd == "" {
		fmt.Fprintln(os.Stderr, "error: --relay, --token, --org, --agent, and --cmd are required")
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	conn, err := connectAndAuth(flags, "cli-"+hostname)
	if err != nil {
		log.Fatalf("auth failed: %v", err)
	}
	defer conn.Close()

	// Send COMMAND
	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"action":  "exec",
		"command": cmd,
		"stream":  true,
	})

	destID := flags.org + "/" + flags.agent
	var reqID [16]byte
	frame := protocol.NewFrame(
		protocol.MsgCommand,
		reqID,
		flags.org+"/cli-"+hostname,
		destID,
		0,
		cmdPayload,
	)

	if err := conn.Write(protocol.Encode(frame)); err != nil {
		log.Fatalf("send command: %v", err)
	}

	// Read responses until COMMAND_RESULT or COMMAND_ERROR
	for {
		data, err := conn.Read()
		if err != nil {
			log.Fatalf("read response: %v", err)
		}

		respFrame, err := protocol.Decode(data)
		if err != nil {
			continue
		}

		switch respFrame.MsgType {
		case protocol.MsgStreamData:
			var line struct {
				Line string `json:"line"`
			}
			json.Unmarshal(respFrame.Payload, &line)
			fmt.Println(line.Line)

		case protocol.MsgCommandResult:
			var result struct {
				ExitCode int `json:"exit_code"`
			}
			json.Unmarshal(respFrame.Payload, &result)
			os.Exit(result.ExitCode)

		case protocol.MsgCommandError:
			var errResp struct {
				Message string `json:"message"`
			}
			json.Unmarshal(respFrame.Payload, &errResp)
			fmt.Fprintln(os.Stderr, errResp.Message)
			os.Exit(1)

		case protocol.MsgError:
			var errResp struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			json.Unmarshal(respFrame.Payload, &errResp)
			fmt.Fprintf(os.Stderr, "%s: %s\n", errResp.Code, errResp.Message)
			os.Exit(1)

		case protocol.MsgCommandAck:
			// Command received, waiting for output
		}
	}
}

func cmdPush() {
	flags, remaining := parseCommonFlags(os.Args[2:])

	var hookFile, envStr string
	for i := 0; i < len(remaining); i++ {
		switch remaining[i] {
		case "--hook":
			if i+1 < len(remaining) {
				hookFile = remaining[i+1]
				i++
			}
		case "--env":
			if i+1 < len(remaining) {
				envStr = remaining[i+1]
				i++
			}
		}
	}

	if flags.relay == "" || flags.token == "" || flags.org == "" || flags.agent == "" || hookFile == "" {
		fmt.Fprintln(os.Stderr, "error: --relay, --token, --org, --agent, and --hook are required")
		os.Exit(1)
	}

	script, err := os.ReadFile(hookFile)
	if err != nil {
		log.Fatalf("read hook file: %v", err)
	}

	hostname, _ := os.Hostname()
	conn, err := connectAndAuth(flags, "cli-"+hostname)
	if err != nil {
		log.Fatalf("auth failed: %v", err)
	}
	defer conn.Close()

	// Build env map from comma-separated string
	env := make(map[string]string)
	if envStr != "" {
		// Simple KEY=val,KEY2=val2 parsing
		for _, pair := range splitEnv(envStr) {
			if idx := indexOf(pair, '='); idx > 0 {
				env[pair[:idx]] = pair[idx+1:]
			}
		}
	}

	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"action":  "push_hook",
		"command": string(script),
		"env":     env,
		"stream":  true,
	})

	destID := flags.org + "/" + flags.agent
	var reqID [16]byte
	frame := protocol.NewFrame(
		protocol.MsgCommand,
		reqID,
		flags.org+"/cli-"+hostname,
		destID,
		0,
		cmdPayload,
	)

	if err := conn.Write(protocol.Encode(frame)); err != nil {
		log.Fatalf("send command: %v", err)
	}

	// Read responses
	for {
		data, err := conn.Read()
		if err != nil {
			log.Fatalf("read response: %v", err)
		}

		respFrame, err := protocol.Decode(data)
		if err != nil {
			continue
		}

		switch respFrame.MsgType {
		case protocol.MsgStreamData:
			var line struct {
				Line string `json:"line"`
			}
			json.Unmarshal(respFrame.Payload, &line)
			fmt.Println(line.Line)
		case protocol.MsgCommandResult:
			var result struct {
				ExitCode int `json:"exit_code"`
			}
			json.Unmarshal(respFrame.Payload, &result)
			os.Exit(result.ExitCode)
		case protocol.MsgCommandError, protocol.MsgError:
			var errResp struct {
				Message string `json:"message"`
			}
			json.Unmarshal(respFrame.Payload, &errResp)
			fmt.Fprintln(os.Stderr, errResp.Message)
			os.Exit(1)
		case protocol.MsgCommandAck:
			// OK
		}
	}
}

func cmdPing() {
	flags, _ := parseCommonFlags(os.Args[2:])

	if flags.relay == "" || flags.token == "" || flags.org == "" || flags.agent == "" {
		fmt.Fprintln(os.Stderr, "error: --relay, --token, --org, and --agent are required")
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	conn, err := connectAndAuth(flags, "cli-"+hostname)
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()

	// Send a heartbeat to the agent via relay
	destID := flags.org + "/" + flags.agent
	var reqID [16]byte
	frame := protocol.NewFrame(
		protocol.MsgHeartbeat,
		reqID,
		flags.org+"/cli-"+hostname,
		destID,
		0,
		nil,
	)

	if err := conn.Write(protocol.Encode(frame)); err != nil {
		os.Exit(1)
	}

	// Wait for response (HeartbeatAck or Error)
	data, err := conn.Read()
	if err != nil {
		os.Exit(1)
	}

	respFrame, err := protocol.Decode(data)
	if err != nil {
		os.Exit(1)
	}

	if respFrame.MsgType == protocol.MsgHeartbeatAck {
		fmt.Println("ok")
		os.Exit(0)
	}

	if respFrame.MsgType == protocol.MsgError {
		var errResp struct {
			Message string `json:"message"`
		}
		json.Unmarshal(respFrame.Payload, &errResp)
		fmt.Fprintln(os.Stderr, errResp.Message)
	}
	os.Exit(1)
}

func cmdAgents() {
	flags, _ := parseCommonFlags(os.Args[2:])

	if flags.relay == "" || flags.token == "" || flags.org == "" {
		fmt.Fprintln(os.Stderr, "error: --relay, --token, and --org are required")
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	conn, err := connectAndAuth(flags, "cli-"+hostname)
	if err != nil {
		log.Fatalf("auth failed: %v", err)
	}
	defer conn.Close()

	// Send AGENT_LIST_REQUEST
	var reqID [16]byte
	frame := protocol.NewFrame(
		protocol.MsgAgentListRequest,
		reqID,
		flags.org+"/cli-"+hostname,
		"relay",
		0,
		nil,
	)

	if err := conn.Write(protocol.Encode(frame)); err != nil {
		log.Fatalf("send request: %v", err)
	}

	// Read response
	data, err := conn.Read()
	if err != nil {
		log.Fatalf("read response: %v", err)
	}

	respFrame, err := protocol.Decode(data)
	if err != nil {
		log.Fatalf("decode response: %v", err)
	}

	if respFrame.MsgType == protocol.MsgAgentList {
		// Print as JSON (muster bash CLI can parse this)
		fmt.Println(string(respFrame.Payload))
	} else {
		fmt.Fprintln(os.Stderr, "unexpected response")
		os.Exit(1)
	}
}

func splitEnv(s string) []string {
	var result []string
	current := ""
	for _, c := range s {
		if c == ',' {
			if current != "" {
				result = append(result, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
