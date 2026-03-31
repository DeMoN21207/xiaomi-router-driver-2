package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xiomi-router-driver/internal/api"
	"xiomi-router-driver/internal/appdir"
	"xiomi-router-driver/internal/automation"
	"xiomi-router-driver/internal/blacklist"
	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/events"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/sqlitedb"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
	"xiomi-router-driver/internal/ui"
)

func main() {
	executablePath, err := os.Executable()
	if err != nil {
		log.Fatalf("resolve executable path: %v", err)
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		log.Fatalf("resolve absolute executable path: %v", err)
	}
	paths, err := appdir.Resolve(executablePath)
	if err != nil {
		log.Fatalf("resolve application directories: %v", err)
	}
	if err := appdir.EnsureDataLayout(paths); err != nil {
		log.Fatalf("prepare data directory layout: %v", err)
	}
	dbPath := filepath.Join(paths.DataDir, "vpn-manager.db")
	db, err := sqlitedb.Open(dbPath)
	if err != nil {
		log.Fatalf("open sqlite database: %v", err)
	}
	defer db.Close()

	port := os.Getenv("VPN_MANAGER_PORT")
	if port == "" {
		port = "8080"
	}

	routingScriptPath, err := routing.EnsureGeneratedScript(paths.DataDir)
	if err != nil {
		log.Fatalf("prepare routing script: %v", err)
	}
	blacklistScriptPath, err := blacklist.EnsureGeneratedScript(paths.DataDir)
	if err != nil {
		log.Fatalf("prepare blacklist script: %v", err)
	}

	stateManager := config.NewManager(db, filepath.Join(paths.DataDir, "vpn-state.json"))
	domainManager := domains.NewManager(db, filepath.Join(paths.DataDir, ".vpn-manager", "domains.list"), filepath.Join(paths.DataDir, "domains.list"))
	eventStore := events.NewStore(db, filepath.Join(paths.DataDir, "events.json"))
	recordEvent := func(level string, kind string, message string) {
		_, _ = eventStore.Add(level, kind, message)
	}
	routingRunner := routing.NewRunner(routingScriptPath)
	automationManager := automation.NewManager(paths.AppDir, executablePath, port)
	openvpnManager := openvpn.NewManager(paths.AppDir, paths.DataDir, db, routingRunner, recordEvent)
	subscriptionManager := subscription.NewManager(paths.AppDir, paths.DataDir, db, routingRunner, recordEvent)
	statusService := status.NewService(
		stateManager,
		domainManager,
		openvpnManager,
		subscriptionManager,
		routingScriptPath,
		paths.AppDir,
		paths.DataDir,
		db,
		filepath.Join(paths.DataDir, "traffic-history.json"),
	)
	blacklistManager := blacklist.NewManager(db, filepath.Join(paths.DataDir, ".vpn-manager"))
	blacklistRunner := blacklist.NewRunner(blacklistScriptPath)

	if _, err := stateManager.Load(); err != nil {
		log.Fatalf("bootstrap state store: %v", err)
	}
	if _, err := blacklistManager.List(); err != nil {
		log.Fatalf("bootstrap blacklist store: %v", err)
	}
	if _, err := domainManager.List(); err != nil {
		log.Fatalf("bootstrap domains store: %v", err)
	}
	if _, _, err := eventStore.List(1, 0); err != nil {
		log.Fatalf("bootstrap events store: %v", err)
	}
	if _, err := statusService.TrafficHistory("1d"); err != nil {
		log.Fatalf("bootstrap traffic history store: %v", err)
	}
	if err := appdir.ArchiveLegacyData(paths); err != nil {
		log.Printf("archive legacy data files: %v", err)
	}

	apiHandler := api.NewHandler(api.Dependencies{
		State:         stateManager,
		Domains:       domainManager,
		Events:        eventStore,
		Routing:       routingRunner,
		Automation:    automationManager,
		OpenVPN:       openvpnManager,
		Subscriptions: subscriptionManager,
		Status:          statusService,
		Blacklist:       blacklistManager,
		BlacklistRunner: blacklistRunner,
		DataDir:         paths.DataDir,
	})
	supervisor := automation.NewSupervisor(stateManager, statusService, apiHandler.ApplyCurrentRules, recordEvent)
	go supervisor.Run(context.Background())
	go statusService.RunTrafficSampler(context.Background())
	go statusService.RunDomainTrafficSampler(context.Background())
	go statusService.RunSiteTrafficSampler(context.Background())

	staticFS, err := fs.Sub(ui.Files, "static")
	if err != nil {
		log.Fatalf("load embedded UI: %v", err)
	}
	fileServer := http.FileServer(http.FS(staticFS))

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}

		// Try to serve a static file first; fall back to index.html for SPA routes.
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			if _, err := fs.Stat(staticFS, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		http.ServeFileFS(w, r, staticFS, "index.html")
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("vpn-manager listening on http://0.0.0.0:%s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
