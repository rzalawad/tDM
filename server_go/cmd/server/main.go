package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/rzalawad/tdm/server_go/pkg/api"
	"github.com/rzalawad/tdm/server_go/pkg/core"
	"github.com/rzalawad/tdm/server_go/pkg/daemon"
)

func main() {
	// Parse command line arguments
	configPath := flag.String("config", "", "Path to configuration file")
	host := flag.String("host", "", "Server host (overrides config file)")
	port := flag.Int("port", 0, "Server port (overrides config file)")
	dbPath := flag.String("db-path", "", "Database path (overrides config file)")
	flag.Parse()

	// Initialize configuration
	config, err := core.InitializeConfig(*configPath)
	if err != nil {
		log.Fatalf("Error initializing configuration: %v", err)
	}

	// Override with command line arguments if provided
	if *host != "" {
		config.Server.Host = *host
	}
	if *port != 0 {
		config.Server.Port = *port
	}
	if *dbPath != "" {
		config.DatabasePath = *dbPath
	}

	// Setup database
	dbFullPath, err := filepath.Abs(config.DatabasePath)
	if err != nil {
		log.Fatalf("Error resolving database path: %v", err)
	}

	// Ensure database directory exists
	dbDir := filepath.Dir(dbFullPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Fatalf("Error creating database directory: %v", err)
	}

	if err := core.InitDB(dbFullPath); err != nil {
		log.Fatalf("Error initializing database: %v", err)
	}

	// Create and start daemon
	downloadDaemon := daemon.NewAria2DownloadDaemon(&config.Daemon)
	if err := downloadDaemon.Start(); err != nil {
		log.Fatalf("Error starting download daemon: %v", err)
	}

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Setup API server
	if config.Environment != core.EnvProduction {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.Default()
	api.SetupRoutes(router)

	// Start server in a goroutine
	serverAddr := fmt.Sprintf("%s:%d", config.Server.Host, config.Server.Port)
	log.Printf("Starting server on %s", serverAddr)

	go func() {
		if err := router.Run(serverAddr); err != nil && err.Error() != "http: Server closed" {
			log.Fatalf("Error starting server: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	log.Println("Shutting down...")

	// Stop the daemon
	if err := downloadDaemon.Stop(); err != nil {
		log.Printf("Error stopping daemon: %v", err)
	} else {
		log.Println("Daemon stopped")
	}

	log.Println("Server stopped")
}
