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
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--listen" && i+1 < len(os.Args) {
			listen = os.Args[i+1]
			break
		}
	}

	_ = configPath // TODO: load relay config from file

	srv := relay.NewServer()
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

	log.Printf("muster-cloud relay starting on %s", listen)
	log.Printf("tunnel endpoint: ws://%s/v1/tunnel", listen)
	log.Printf("health check:   http://%s/healthz", listen)

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}

	_ = ctx
}

func printUsage() {
	fmt.Println("Usage: muster-cloud [options]")
	fmt.Println()
	fmt.Println("Start the muster-cloud relay server.")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --listen <addr>   Listen address (default: :8443, or LISTEN env)")
	fmt.Println("  --config <path>   Config file path")
	fmt.Println("  version           Print version")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  LISTEN            Listen address")
}
