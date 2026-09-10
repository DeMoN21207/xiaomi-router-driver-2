package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"xiomi-router-driver/internal/api"
	"xiomi-router-driver/internal/appdir"
	"xiomi-router-driver/internal/automation"
	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/dnsproxy"
	"xiomi-router-driver/internal/doctor"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/events"
	"xiomi-router-driver/internal/lifecycle"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/sqlitedb"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
	"xiomi-router-driver/internal/ui"
	"xiomi-router-driver/internal/update"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		os.Exit(doctor.Run(os.Stdout))
	}

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
	if err := update.RecoverInterruptedUpdate(paths.AppDir); err != nil {
		log.Fatalf("recover interrupted update: %v", err)
	}
	rootCtx, cancelWorkers := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelWorkers()
	dbPath := filepath.Join(paths.DataDir, "vpn-manager.db")
	db, err := sqlitedb.Open(dbPath)
	if err != nil {
		log.Fatalf("open sqlite database: %v", err)
	}

	port := os.Getenv("VPN_MANAGER_PORT")
	if port == "" {
		port = "8080"
	}

	routingScriptPath, err := routing.EnsureGeneratedScript(paths.DataDir)
	if err != nil {
		log.Fatalf("prepare routing script: %v", err)
	}

	dnsProxyServer := ""
	var dnsProxy *dnsproxy.Server
	if dnsproxy.EnabledFromEnv() {
		proxy, err := dnsproxy.Start(rootCtx, dnsproxy.ConfigFromEnv())
		if err != nil {
			log.Printf("dns proxy disabled: %v", err)
		} else {
			dnsProxy = proxy
			dnsProxyServer = proxy.DnsmasqServer()
			log.Printf("dns proxy listening on %s for routed domains", dnsProxyServer)
		}
	}

	stateManager := config.NewManager(db, filepath.Join(paths.DataDir, "vpn-state.json"))
	domainManager := domains.NewManager(db, filepath.Join(paths.DataDir, ".vpn-manager", "domains.list"), filepath.Join(paths.DataDir, "domains.list"))
	eventStore := events.NewStore(db, filepath.Join(paths.DataDir, "events.json"))
	recordEvent := func(level string, kind string, message string) {
		_, _ = eventStore.Add(level, kind, message)
	}
	routingRunner := routing.NewRunner(routingScriptPath)
	routingRunner.SetDNSProxyServer(dnsProxyServer)
	automationManager := automation.NewManager(paths.AppDir, executablePath, port)
	openvpnManager := openvpn.NewManager(paths.AppDir, paths.DataDir, db, routingRunner, recordEvent)
	subscriptionManager := subscription.NewManager(paths.AppDir, paths.DataDir, db, routingRunner, recordEvent)
	restartRequests := make(chan update.InstallResult, 1)
	updateManager := update.NewManager(update.Options{
		AppDir:      paths.AppDir,
		DataDir:     paths.DataDir,
		State:       stateManager,
		RecordEvent: recordEvent,
		Restart: func(result update.InstallResult) {
			select {
			case restartRequests <- result:
			default:
			}
		},
	})
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

	if _, err := stateManager.Load(); err != nil {
		log.Fatalf("bootstrap state store: %v", err)
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
	if err := sqlitedb.Maintain(db, dbPath); err != nil {
		log.Printf("initial sqlite maintenance failed: %v", err)
	}
	if err := appdir.ArchiveLegacyData(paths); err != nil {
		log.Printf("archive legacy data files: %v", err)
	}

	apiHandler := api.NewHandler(api.Dependencies{
		Context:       rootCtx,
		State:         stateManager,
		Domains:       domainManager,
		Events:        eventStore,
		Routing:       routingRunner,
		Automation:    automationManager,
		OpenVPN:       openvpnManager,
		Subscriptions: subscriptionManager,
		Status:        statusService,
		Update:        updateManager,
		DataDir:       paths.DataDir,
	})
	supervisor := automation.NewSupervisor(stateManager, statusService, apiHandler.ApplyCurrentRules, apiHandler.ApplyRulesFromState, recordEvent, paths.DataDir)
	apiHandler.SetFailoverStatusProvider(supervisor.FailoverStatus)
	apiHandler.SetPriorityRuntime(supervisor.PriorityStatus, supervisor.SetPriorityOverride, supervisor.ClearPriorityOverride, supervisor.ApplyPriorityPolicies)
	var workers sync.WaitGroup
	startWorker := func(run func(context.Context)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			run(rootCtx)
		}()
	}
	startWorker(supervisor.Run)
	startWorker(statusService.RunTrafficSampler)
	startWorker(statusService.RunDomainTrafficSampler)
	startWorker(statusService.RunDomainHealthSampler)
	startWorker(statusService.RunSiteTrafficSampler)
	startWorker(func(ctx context.Context) {
		sqlitedb.RunMaintenance(ctx, db, dbPath, 6*time.Hour, func(err error) {
			log.Printf("sqlite maintenance failed: %v", err)
			recordEvent("error", "storage.maintenance_failed", err.Error())
		})
	})

	staticFS, err := fs.Sub(ui.Files, "static")
	if err != nil {
		log.Fatalf("load embedded UI: %v", err)
	}
	fileServer := http.FileServer(http.FS(staticFS))
	apiToken := api.LoadAPIToken(paths.DataDir)
	apiHandlerWithSecurity := api.LimitRequestBody(api.BearerAuth(apiHandler, apiToken), 128<<20)
	if apiToken != "" {
		log.Printf("API bearer authentication enabled for mutating requests")
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandlerWithSecurity)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandlerWithSecurity.ServeHTTP(w, r)
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

	listenAddress := strings.TrimSpace(os.Getenv("VPN_MANAGER_LISTEN_ADDR"))
	if listenAddress == "" {
		listenAddress = "0.0.0.0"
	}
	server := &http.Server{
		Addr:              listenAddress + ":" + port,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("vpn-manager listening on http://%s:%s", listenAddress, port)
	serverErrors := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if err == http.ErrServerClosed {
			err = nil
		}
		serverErrors <- err
	}()

	var restartResult *update.InstallResult
	var serveErr error
	select {
	case <-rootCtx.Done():
	case result := <-restartRequests:
		restartResult = &result
	case serveErr = <-serverErrors:
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	if err := lifecycle.Shutdown(shutdownCtx, lifecycle.ShutdownHooks{
		CancelWorkers: cancelWorkers,
		ShutdownHTTP:  server.Shutdown,
		CloseDNS: func() error {
			if dnsProxy == nil {
				return nil
			}
			return dnsProxy.Close()
		},
		WaitWorkers: workers.Wait,
		CheckpointDB: func() error {
			return sqlitedb.Checkpoint(db)
		},
		CloseDB: db.Close,
	}); err != nil {
		log.Printf("graceful shutdown: %v", err)
	}
	if serveErr != nil {
		log.Fatalf("HTTP server stopped: %v", serveErr)
	}
	if restartResult != nil {
		env := setEnvironment(os.Environ(), "VPN_MANAGER_ROOT", paths.AppDir)
		env = setEnvironment(env, "VPN_MANAGER_PORT", port)
		binaryPath := filepath.Join(paths.AppDir, filepath.Base(executablePath))
		if err := lifecycle.ExecWithRollback(lifecycle.RestartHooks{
			Exec:    syscall.Exec,
			Restore: update.RestoreRuntimeBackup,
		}, paths.AppDir, restartResult.BackupDir, binaryPath, os.Args, env); err != nil {
			log.Fatalf("restart after update: %v", err)
		}
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		response := &responseStatusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(response, r)
		duration := time.Since(start)
		if shouldLogRequest(r.Method, response.status, duration) {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, response.status, duration.Round(time.Millisecond))
		}
	})
}

type responseStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseStatusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func shouldLogRequest(method string, status int, duration time.Duration) bool {
	return (method != http.MethodGet && method != http.MethodHead) || status >= http.StatusBadRequest || duration >= 2*time.Second
}

func setEnvironment(environ []string, key string, value string) []string {
	prefix := key + "="
	for index, entry := range environ {
		if strings.HasPrefix(entry, prefix) {
			environ[index] = prefix + value
			return environ
		}
	}
	return append(environ, prefix+value)
}
