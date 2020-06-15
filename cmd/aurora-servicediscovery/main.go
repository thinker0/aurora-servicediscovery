package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/go-akka/configuration"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	LOG "github.com/sirupsen/logrus"
	"github.com/thinker0/aurora-servicediscovery/v1/internal/discovery"
	"github.com/urfave/negroni"
	_ "go.uber.org/automaxprocs"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	Version      string = ""
	GitTag       string = ""
	GitCommit    string = ""
	GitTreeState string = ""
)

type App struct {
	server       *http.Server
	adminServer  *http.Server
	isHealthy    int32
	hosts		 []string
	context      context.Context
	cancel       context.CancelFunc
	cache        *discovery.ServiceSetsCache
}

func (app *App) handleEvent(w http.ResponseWriter, r *http.Request) {
	var statusCode = http.StatusInternalServerError
	vars := mux.Vars(r)
	path := fmt.Sprintf("%s/%s/%s", vars["role"], vars["env"], vars["service"])
	services, err := app.cache.GetServerSetEntity(path)
	if err != nil {
		LOG.Errorf("Not Found Service %s", vars, err)
		statusCode = http.StatusMethodNotAllowed
		return
	}

	// create request when the stack exits
	defer func() {
		SetCacheControlJson(w)
		if statusCode == http.StatusOK {
			w.WriteHeader(statusCode)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			body, _ := json.Marshal(services)
			w.Write(body)
		} else {
			http.Error(w, http.StatusText(statusCode), statusCode)
		}
	}()

	// check the request method is GET
	if r.Method != http.MethodGet {
		statusCode = http.StatusMethodNotAllowed
		return
	}

	// parse url query
	//params, err := url.ParseQuery(r.URL.RawQuery)
	//if err != nil {
	//	LOG.Warnf("ParseForm Error %v %s", r, err)
	//	statusCode = http.StatusBadRequest
	//	return
	//}

	statusCode = http.StatusOK
}

func (app *App) handleQuit(w http.ResponseWriter, r *http.Request) {
	app.Stop()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (app *App) createHandler() http.Handler {
	router := mux.NewRouter()
	router.Handle("/", DefaultIndex())
	router.Handle("/health", Healthz(&app.isHealthy))
	router.HandleFunc("/v/{role:[a-zA-Z0-9-_]+}/{env:[a-zA-Z0-9-_]+}/{service:[a-zA-Z0-9-_]+}", app.handleEvent)
	router.HandleFunc("/r/{role:[\\w]+}/{env:\\w+}/{service:\\w+}", app.handleEvent)

	n := negroni.Classic() // Includes some default middlewares
	n.UseHandler(router)

	return n
}

func (app *App) createAdminHandler() http.Handler {
	adminRouter := http.NewServeMux()
	adminRouter.Handle("/", http.RedirectHandler("/metrics", http.StatusSeeOther))
	adminRouter.Handle("/health", Healthz(&app.isHealthy))
	adminRouter.Handle("/metrics", promhttp.Handler())
	adminRouter.HandleFunc("/quitquitquit", app.handleQuit)
	adminRouter.HandleFunc("/abortabortabort", app.handleQuit)
	adminRouter.HandleFunc("/debug/pprof/", pprof.Index)
	adminRouter.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	adminRouter.HandleFunc("/debug/pprof/profile", pprof.Profile)
	adminRouter.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	adminRouter.HandleFunc("/debug/pprof/trace", pprof.Trace)

	adminN := negroni.Classic()
	adminN.UseHandler(adminRouter)

	return adminN
}

func (app *App) Start() {
	app.context, app.cancel = context.WithCancel(context.Background())

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	LOG.Println("Server is starting...")

	// start http server
	go func() {
		if err := app.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			LOG.Fatalf("Could not listen on %s: %v\n", app.server.Addr, err)
			app.cancel()
		}
	}()

	// start admin server
	go func() {
		if err := app.adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			LOG.Fatalf("Could not listen on %s: %v\n", app.adminServer.Addr, err)
			app.cancel()
		}
	}()

	atomic.StoreInt32(&app.isHealthy, 1)

	// waits SIGINT/SIGTERM or other exit signals(ex. /quitquitquit)
	select {
	case <-quit:
		app.cancel()
	case <-app.context.Done():
	}

	LOG.Println("Server is shutting down...")
	atomic.StoreInt32(&app.isHealthy, 0)

	app.server.SetKeepAlivesEnabled(false)
	ctx, _ := context.WithTimeout(context.Background(), 30*time.Second)
	if err := app.server.Shutdown(ctx); err != nil {
		LOG.Fatalf("Could not gracefully shutdown the httpServer: %v\n", err)
	}

	app.adminServer.SetKeepAlivesEnabled(false)
	ctx, _ = context.WithTimeout(context.Background(), 30*time.Second)
	if err := app.adminServer.Shutdown(ctx); err != nil {
		LOG.Fatalf("Could not gracefully shutdown the adminServer: %v\n", err)
	}

	LOG.Info("Server stopped")
}

func (app *App) Stop() {
	app.cancel()
}

func DefaultIndex() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
}

func Healthz(healthy *int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(healthy) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintln(w, "ok")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
}

func SetCacheControl(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-discovery, no-store, max-age=0, must-revalidate")
	w.Header().Set("Pragma", "no-discovery")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func SetCacheControlJson(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-discovery, no-store, max-age=0, must-revalidate")
	w.Header().Set("Pragma", "no-discovery")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func NewApp(
	serverAddr string,
	adminAddr string, hosts []string) (*App, error) {

	app := App{
		isHealthy: 0,
		hosts: hosts,
	}

	// create web servers
	logger := log.New(os.Stderr, "http: ", log.LstdFlags)
	handler := app.createHandler()
	app.server = &http.Server{
		Addr:         serverAddr,
		Handler:      handler,
		ErrorLog:     logger,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	adminHandler := app.createAdminHandler()
	app.adminServer = &http.Server{
		Addr:         adminAddr,
		Handler:      adminHandler,
		ErrorLog:     logger,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	app.cache = discovery.New(hosts)

	return &app, nil
}

func main() {
	LOG.Println("Aurora Service httpServer")
	LOG.Println("Version:", Version)
	LOG.Println("GitTag:", GitTag)
	LOG.Println("GitCommit:", GitCommit)
	LOG.Println("GitTreeState:", GitTreeState)

	var configFile, serverAddr, adminAddr string

	// parse flags and generate config
	flag.StringVar(&configFile, "conf", "configs/application.conf", "Configuration file")
	flag.StringVar(&serverAddr, "http.port", ":5000", "httpServer listen address")
	flag.StringVar(&adminAddr, "admin.port", ":9000", "admin listen address")
	flag.Parse()

	// generate config
	config := configuration.LoadConfig(configFile)
	if config == nil {
		LOG.Panic("parsing config failed")
	}

	// initialize sentry
	err := sentry.Init(sentry.ClientOptions{
		Dsn:         config.GetString("sentry.dsn"),
		DebugWriter: os.Stderr,
		Debug:       true,
		Environment: config.GetString("project.env"),
		Release:     Version,
		SampleRate:  0.01,
	})
	if err != nil {
		LOG.Panic(err, "sentry initialization failed")
	}

	// get zookeeper hosts from config
	clusters := config.GetStringList("zookeeper.hosts")
	if clusters == nil || len(clusters) == 0 {
		LOG.Panic("no kafka cluster in config")
	}
	for _, brokers := range clusters {
		clusterInfo := strings.Split(brokers, ":")
		if len(clusterInfo) != 2 {
			LOG.Panicf("invalid cluster config: %s", brokers)
		}
	}

	// create and start app
	app, err := NewApp(serverAddr, adminAddr, clusters)
	if err != nil {
		LOG.Panic(err, "app initialization failed")
	}

	app.Start()
}
