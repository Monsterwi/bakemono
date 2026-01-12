package main

import (
	"flag"
	"fmt"
	"os"

	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
	"github.com/Monsterwi/razor/server"
	"github.com/sevlyar/go-daemon"
)

// Command line flags
var (
	daemonMode  = flag.Bool("d", false, "Run in daemon mode")
	configDir   = flag.String("c", "conf", "Path to config directory")
	pidFile     = flag.String("p", "/var/run/bakemono.pid", "PID file")
	showHelp    = flag.Bool("h", false, "Show help message")
	showVersion = flag.Bool("v", false, "Show version information")
)

const (
	Version   = "1.0.0"
	BuildDate = "2026-01-06"
)

func main() {
	// Parse command line flags
	flag.Parse()

	// Show help
	if *showHelp {
		printHelp()
		return
	}

	// Show version
	if *showVersion {
		printVersion()
		return
	}

	// If daemon mode is requested
	if *daemonMode {
		cntxt := &daemon.Context{}
		cntxt.PidFileName = *pidFile

		d, err := cntxt.Reborn()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
			os.Exit(1)
		}

		// Parent process: release resources and exit
		if d != nil {
			fmt.Printf("Bakemono started in background (PID: %d)\n", d.Pid)
			return
		}

		// Child process: release parent resources, continue execution
		defer cntxt.Release()
	}

	// 1. Load configuration from records.config
	cfg, err := appconfig.Load(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// 2. Initialize logger
	if err := initLogger(cfg, *daemonMode); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	// 3. Initialize Cache
	globalCache, err := NewCache(cfg)
	if err != nil {
		logger.Fatalf("Failed to start cache: %v", err)
	}
	// Note: We transfer ownership of closing the cache to the Server

	logger.Infof("Cache initialized with %d stripes", len(globalCache.Stripes))

	// 4. Initialize Upstream
	httpProxy, err := proxy.NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo)
	if err != nil {
		logger.Fatalf("Failed to init proxy: %v", err)
	}

	// 5. Start Server
	srv := server.New(cfg, globalCache, httpProxy)
	if err := srv.Run(); err != nil {
		logger.Fatalf("Server exited with error: %v", err)
	}

	logger.Info("Server shutdown complete")
}
