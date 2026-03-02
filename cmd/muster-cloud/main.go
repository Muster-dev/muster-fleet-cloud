package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/internal/auth"
	"github.com/Muster-dev/muster-fleet-cloud/internal/relay"
)

var version = "0.1.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("muster-cloud %s\n", version)
			return
		case "--help", "-h", "help":
			printUsage()
			return
		case "token":
			runTokenCmd(os.Args[2:])
			return
		}
	}

	// Parse config path
	configPath := ""
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			break
		}
	}

	listen := ":8443"
	if addr := os.Getenv("LISTEN"); addr != "" {
		listen = addr
	}

	tlsCert := os.Getenv("TLS_CERT")
	tlsKey := os.Getenv("TLS_KEY")

	tokenStorePath := "tokens.json"
	if p := os.Getenv("TOKEN_STORE"); p != "" {
		tokenStorePath = p
	}

	for i := 1; i < len(os.Args); i++ {
		if i+1 >= len(os.Args) {
			break
		}
		switch os.Args[i] {
		case "--listen":
			listen = os.Args[i+1]
		case "--tls-cert":
			tlsCert = os.Args[i+1]
		case "--tls-key":
			tlsKey = os.Args[i+1]
		case "--token-store":
			tokenStorePath = os.Args[i+1]
		}
	}

	_ = configPath // TODO: load relay config from file

	useTLS := tlsCert != "" && tlsKey != ""
	if (tlsCert != "") != (tlsKey != "") {
		log.Fatal("both --tls-cert and --tls-key must be provided together")
	}

	tokenStore, err := auth.NewTokenStore(tokenStorePath)
	if err != nil {
		log.Fatalf("open token store: %v", err)
	}

	srv := relay.NewServer(tokenStore)
	handler := srv.Handler()

	httpSrv := &http.Server{
		Addr:         listen,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
		httpSrv.Shutdown(context.Background())
	}()

	scheme := "http"
	wsScheme := "ws"
	if useTLS {
		scheme = "https"
		wsScheme = "wss"
	}
	log.Printf("muster-cloud relay starting on %s", listen)
	log.Printf("tunnel endpoint: %s://%s/v1/tunnel", wsScheme, listen)
	log.Printf("health check:   %s://%s/healthz", scheme, listen)

	if useTLS {
		log.Printf("TLS enabled (cert=%s key=%s)", tlsCert, tlsKey)
		err := httpSrv.ListenAndServeTLS(tlsCert, tlsKey)
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	} else {
		err := httpSrv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}

	_ = ctx
}

func runTokenCmd(args []string) {
	if len(args) == 0 {
		printTokenUsage()
		os.Exit(1)
	}

	storePath := "tokens.json"
	if p := os.Getenv("TOKEN_STORE"); p != "" {
		storePath = p
	}

	// Extract --store flag from anywhere in args
	var filteredArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--store" && i+1 < len(args) {
			storePath = args[i+1]
			i++
			continue
		}
		filteredArgs = append(filteredArgs, args[i])
	}
	args = filteredArgs

	store, err := auth.NewTokenStore(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open token store: %v\n", err)
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		tokenCreate(store, args[1:])
	case "list":
		tokenList(store, args[1:])
	case "revoke":
		tokenRevoke(store, args[1:])
	default:
		printTokenUsage()
		os.Exit(1)
	}
}

func tokenCreate(store *auth.TokenStore, args []string) {
	var tokenType, orgID, name string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			break
		}
		switch args[i] {
		case "--type":
			tokenType = args[i+1]
			i++
		case "--org":
			orgID = args[i+1]
			i++
		case "--name":
			name = args[i+1]
			i++
		}
	}

	if tokenType == "" || orgID == "" {
		fmt.Fprintln(os.Stderr, "error: --type and --org are required")
		fmt.Fprintln(os.Stderr, "usage: muster-cloud token create --type <admin|agent-join|cli> --org <org_id> [--name <name>]")
		os.Exit(1)
	}

	var prefix string
	var tt auth.TokenType
	switch tokenType {
	case "admin":
		prefix = auth.PrefixAdmin
		tt = auth.TypeAdmin
	case "agent-join":
		prefix = auth.PrefixAgentJoin
		tt = auth.TypeAgentJoin
	case "cli":
		prefix = auth.PrefixCLI
		tt = auth.TypeCLI
	default:
		fmt.Fprintf(os.Stderr, "error: unknown token type %q (use admin, agent-join, or cli)\n", tokenType)
		os.Exit(1)
	}

	if name == "" {
		name = tokenType
	}

	raw, hash, err := auth.GenerateToken(prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: generate token: %v\n", err)
		os.Exit(1)
	}

	id, err := store.CreateToken(hash, tt, orgID, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: store token: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Token created successfully.\n")
	fmt.Printf("  ID:   %s\n", id)
	fmt.Printf("  Type: %s\n", tokenType)
	fmt.Printf("  Org:  %s\n", orgID)
	fmt.Printf("\n")
	fmt.Printf("  Token: %s\n", raw)
	fmt.Printf("\n")
	fmt.Printf("Save this token now — it cannot be retrieved later.\n")
}

func tokenList(store *auth.TokenStore, args []string) {
	var orgID string
	for i := 0; i < len(args); i++ {
		if args[i] == "--org" && i+1 < len(args) {
			orgID = args[i+1]
			i++
		}
	}

	tokens := store.ListTokens(orgID)
	if len(tokens) == 0 {
		fmt.Println("No tokens found.")
		return
	}

	fmt.Printf("%-24s %-14s %-12s %-10s %-6s %s\n", "ID", "TYPE", "ORG", "NAME", "USED", "CREATED")
	for _, tok := range tokens {
		used := "no"
		if tok.Used {
			used = "yes"
		}
		fmt.Printf("%-24s %-14s %-12s %-10s %-6s %s\n",
			tok.ID, tok.TokenType, tok.OrgID, tok.Name, used,
			tok.CreatedAt.Format("2006-01-02"),
		)
	}
}

func tokenRevoke(store *auth.TokenStore, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: token ID required")
		fmt.Fprintln(os.Stderr, "usage: muster-cloud token revoke <token_id>")
		os.Exit(1)
	}

	if err := store.RevokeToken(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Token %s revoked.\n", args[0])
}

func printTokenUsage() {
	fmt.Println("Usage: muster-cloud token <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  create   Create a new token")
	fmt.Println("  list     List tokens")
	fmt.Println("  revoke   Revoke a token by ID")
	fmt.Println()
	fmt.Println("Create options:")
	fmt.Println("  --type <type>    Token type: admin, agent-join, cli (required)")
	fmt.Println("  --org <org_id>   Organization ID (required)")
	fmt.Println("  --name <name>    Token name (optional)")
	fmt.Println("  --store <path>   Token store path (default: tokens.json, or TOKEN_STORE env)")
}

func printUsage() {
	fmt.Println("Usage: muster-cloud [options]")
	fmt.Println("       muster-cloud token <command> [options]")
	fmt.Println()
	fmt.Println("Start the muster-cloud relay server, or manage tokens.")
	fmt.Println()
	fmt.Println("Server options:")
	fmt.Println("  --listen <addr>       Listen address (default: :8443, or LISTEN env)")
	fmt.Println("  --tls-cert <path>     TLS certificate file (or TLS_CERT env)")
	fmt.Println("  --tls-key <path>      TLS private key file (or TLS_KEY env)")
	fmt.Println("  --token-store <path>  Token store path (default: tokens.json, or TOKEN_STORE env)")
	fmt.Println("  --config <path>       Config file path")
	fmt.Println("  version               Print version")
	fmt.Println()
	fmt.Println("Token commands:")
	fmt.Println("  token create   Create a new auth token")
	fmt.Println("  token list     List tokens")
	fmt.Println("  token revoke   Revoke a token")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  LISTEN            Listen address")
	fmt.Println("  TLS_CERT          TLS certificate file path")
	fmt.Println("  TLS_KEY           TLS private key file path")
	fmt.Println("  TOKEN_STORE       Token store file path")
}
