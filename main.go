// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/digineo/go-ping"
	mon "github.com/digineo/go-ping/monitor"

	"github.com/czerwonk/ping_exporter/config"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	inotify "gopkg.in/fsnotify.v1"
)

const (
	version   string = "1.3.0"
	indexHTML        = `<!doctype html>
<html>
<head>
	<meta charset="UTF-8">
	<title>ping Exporter (Version ` + version + `)</title>
</head>
<body>
	<h1>ping Exporter</h1>
	<p><a href="%s">Metrics</a></p>
	<h2>More information:</h2>
	<p><a href="https://github.com/czerwonk/ping_exporter">github.com/czerwonk/ping_exporter</a></p>
</body>
</html>
`
)

var (
	showVersion             = kingpin.Flag("version", "Print version information").Default().Bool()
	listenAddress           = kingpin.Flag("web.listen-address", "Address on which to expose metrics and web interface").Envar("PING_LISTEN_ADDRESS").Default(":9427").String()
	metricsPath             = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics").Envar("PING_METRICS_PATH").Default("/metrics").String()
	metricsToken            = kingpin.Flag("web.token", "Token (in http request headers of queries) which to expose metrics").Envar("PING_METRICS_TOKEN").Default("").String()
	serverUseTLS            = kingpin.Flag("web.tls.enabled", "Enable TLS for web server, default is false").Envar("PING_TLS_ENABLED").Default().Bool()
	serverTLSCertFile       = kingpin.Flag("web.tls.cert-file", "The certificate file for the web server").Envar("PING_TLS_CERT_FILE").Default("").String()
	serverTLSKeyFile        = kingpin.Flag("web.tls.key-file", "The key file for the web server").Envar("PING_TLS_KEY_FILE").Default("").String()
	serverMutualAuthEnabled = kingpin.Flag("web.tls.mutual-auth-enabled", "Enable TLS client mutual authentication, default is false").Envar("PING_TLS_MUTUAL_AUTH").Default().Bool()
	serverTLSCAFile         = kingpin.Flag("web.tls.ca-file", "The certificate authority file for client's certificate verification").Envar("PING_TLS_CA_FILE").Default("").String()
	configFile              = kingpin.Flag("config.path", "Path or HTTP(S) URL to config file (CONFIG_FILE is a legacy fallback)").Envar("PING_CONFIG_FILE").Default(os.Getenv("CONFIG_FILE")).String()
	configRefresh           = kingpin.Flag("config.refresh", "Polling interval for HTTP(S) config URLs (must be greater than 0)").Envar("PING_CONFIG_REFRESH").Default("1m").Duration()
	pingInterval            = kingpin.Flag("ping.interval", "Interval for ICMP echo requests").Envar("PING_INTERVAL").Default("5s").Duration()
	pingTimeout             = kingpin.Flag("ping.timeout", "Timeout for ICMP echo request").Envar("PING_TIMEOUT").Default("4s").Duration()
	pingSize                = kingpin.Flag("ping.size", "Payload size for ICMP echo requests").Envar("PING_SIZE").Default("56").Uint16()
	firewallMark            = kingpin.Flag("ping.fw-mark", "set socket mark (SO_MARK) to this value").Envar("PING_FW_MARK").Default("0").Uint()
	historySize             = kingpin.Flag("ping.history-size", "Number of results to remember per target").Envar("PING_HISTORY_SIZE").Default("10").Int()
	dnsRefresh              = kingpin.Flag("dns.refresh", "Interval for refreshing DNS records and updating targets accordingly (0 if disabled)").Envar("PING_DNS_REFRESH").Default("1m").Duration()
	dnsNameServer           = kingpin.Flag("dns.nameserver", "DNS server used to resolve hostname of targets").Envar("PING_DNS_NAMESERVER").Default("").String()
	dnsLookupTimeout        = kingpin.Flag("dns.timeout", "Timeout for DNS resolution").Envar("PING_DNS_TIMEOUT").Default("0s").Duration()
	disableIPv6             = kingpin.Flag("options.disable-ipv6", "Disable DNS from resolving IPv6 AAAA records").Envar("PING_DISABLE_IPV6").Default().Bool()
	disableIPv4             = kingpin.Flag("options.disable-ipv4", "Disable DNS from resolving IPv4 A records").Envar("PING_DISABLE_IPV4").Default().Bool()
	logLevel                = kingpin.Flag("log.level", "Only log messages with the given severity or above. Valid levels: [debug, info, warn, error, fatal]").Envar("PING_LOG_LEVEL").Default("info").String()
	targetFlag              = kingpin.Arg("targets", "A list of targets to ping (defaults to whitespace-separated PING_TARGETS)").Default(strings.Fields(os.Getenv("PING_TARGETS"))...).Strings()
	pushMetricsURLs         = kingpin.Flag("pushmetrics.url", "HTTP(S) Prometheus text import endpoint (repeatable, defaults to semicolon-separated PING_PUSHMETRICS_URLS)").Default(environmentList("PING_PUSHMETRICS_URLS")...).Strings()
	pushMetricsExtraLabels  = kingpin.Flag("pushmetrics.extraLabel", "Extra label in name=\"value\" format (repeatable, defaults to semicolon-separated PING_PUSHMETRICS_LABELS)").Default(environmentList("PING_PUSHMETRICS_LABELS")...).Strings()
	pushMetricsInterval     = kingpin.Flag("pushmetrics.interval", "Metrics snapshot and retry interval").Envar("PING_PUSHMETRICS_INTERVAL").Default("10s").Duration()
	pushMetricsTimeout      = kingpin.Flag("pushmetrics.timeout", "Timeout for each metrics push request").Envar("PING_PUSHMETRICS_TIMEOUT").Default("10s").Duration()
	pushMetricsBaseDir      = kingpin.Flag("pushmetrics.base-dir", "Base directory for push metrics related files (queue, cache, etc.)").Envar("PING_PUSHMETRICS_BASE_DIR").Default("/var/lib/ping_exporter").String()
	pushMetricsQueueDir     = kingpin.Flag("pushmetrics.queue-dir", "Persistent metrics queue directory; must not be shared between processes").Envar("PING_PUSHMETRICS_QUEUE_DIR").Default("").String()
	pushMetricsMaxDiskUsage = kingpin.Flag("pushmetrics.max-disk-usage", "Maximum queued payload bytes; new snapshots are dropped when full").Envar("PING_PUSHMETRICS_MAX_DISK_USAGE").Default("1073741824").Int64()
	pushMetricsMaxInmemory  = kingpin.Flag("pushmetrics.max-inmemory-requests", "Maximum in-memory batches before spilling to disk (0 always uses disk)").Envar("PING_PUSHMETRICS_MAX_INMEMORY").Default("100").Int()
)

var (
	enableDeprecatedMetrics = true // default may change in future
	deprecatedMetrics       = kingpin.Flag("metrics.deprecated", "Enable or disable deprecated metrics (`ping_rtt_ms{type=best|worst|mean|std_dev}`). Valid choices: [enable, disable]").Envar("PING_METRICS_DEPRECATED").Default("disable").String()

	rttMetricsScale  = rttInMills // might change in future
	rttMode          = kingpin.Flag("metrics.rttunit", "Export ping results as either seconds (default), or milliseconds (deprecated), or both (for migrations). Valid choices: [s, ms, both]").Envar("PING_METRICS_RTTUNIT").Default("s").String()
	desiredTargets   *targets
	configHTTPClient = &http.Client{Timeout: 10 * time.Second}
)

// Kingpin splits repeatable environment values on newlines, but our environment
// files use semicolons. Defaults preserve command-line precedence for lists.
func environmentList(name string) []string {
	value := os.Getenv(name)
	if value == "" {
		return nil
	}
	values := strings.Split(value, ";")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values
}

func main() {
	desiredTargets = &targets{}

	// CMD_FLAGS lets the container image pass extra flags without a shell to expand them.
	args := os.Args[1:]
	if extra := os.Getenv("CMD_FLAGS"); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	for index, arg := range args {
		if strings.HasPrefix(arg, "-pushmetrics.") {
			args[index] = "-" + arg
		}
	}
	kingpin.MustParse(kingpin.CommandLine.Parse(args))

	// Initialize pushMetricsQueueDir from base directory if not explicitly set
	if *pushMetricsQueueDir == "" {
		queueDir := filepath.Join(*pushMetricsBaseDir, "pushmetrics-queue")
		pushMetricsQueueDir = &queueDir
	}

	if *showVersion {
		printVersion()
		os.Exit(0)
	}

	setLogLevel(*logLevel)
	log.SetReportCaller(true)

	switch *deprecatedMetrics {
	case "enable":
		enableDeprecatedMetrics = true
	case "disable":
		enableDeprecatedMetrics = false
	default:
		kingpin.FatalUsage("metrics.deprecated must be `enable` or `disable`")
	}

	if rttMetricsScale = rttUnitFromString(*rttMode); rttMetricsScale == rttInvalid {
		kingpin.FatalUsage("metrics.rttunit must be `ms` for millis, or `s` for seconds, or `both`")
	}
	log.Infof("rtt units: %#v", rttMetricsScale)

	if mpath := *metricsPath; mpath == "" {
		log.Warnln("web.telemetry-path is empty, correcting to `/metrics`")
		mpath = "/metrics"
		metricsPath = &mpath
	} else if mpath[0] != '/' {
		mpath = "/" + mpath
		metricsPath = &mpath
	}

	if isHTTPConfig(*configFile) && *configRefresh <= 0 {
		kingpin.FatalUsage("config.refresh must be greater than 0")
	}
	if len(*pushMetricsURLs) > 0 {
		for idx, url := range *pushMetricsURLs {
			if err := validatePushMetrics(url, *pushMetricsInterval, *pushMetricsTimeout); err != nil {
				kingpin.FatalUsage("pushmetrics.url #%d: %v", idx+1, err)
			}
		}
		if *pushMetricsMaxDiskUsage <= 0 || *pushMetricsQueueDir == "" {
			kingpin.FatalUsage("pushmetrics.max-disk-usage must be greater than 0 and pushmetrics.queue-dir must not be empty")
		}
		if *pushMetricsMaxInmemory < 0 {
			kingpin.FatalUsage("pushmetrics.max-inmemory-requests must not be negative")
		}
	}
	if _, err := parsePushMetricsExtraLabels(*pushMetricsExtraLabels); err != nil {
		kingpin.FatalUsage("%v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		kingpin.FatalUsage("could not load config.path: %v", err)
	}

	if cfg.Ping.History < 1 {
		kingpin.FatalUsage("ping.history-size must be greater than 0")
	}

	if cfg.Ping.Size > 65500 {
		kingpin.FatalUsage("ping.size must be between 0 and 65500")
	}

	if cfg.Targets == nil {
		kingpin.FatalUsage("No targets specified")
	}

	runExporter(cfg)
}

func runInteractive(cfg *config.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runExporterContext(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func runExporterContext(ctx context.Context, cfg *config.Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	globalResolver := setupGlobalResolver(cfg)

	m, err := startMonitor(ctx, cfg, globalResolver)
	if err != nil {
		return err
	}
	defer m.Stop()

	collector := NewPingCollector(enableDeprecatedMetrics, rttMetricsScale, m, cfg)
	watchDone := make(chan error, 1)
	go func() {
		err := watchConfig(ctx, desiredTargets, globalResolver, m, collector, cfg)
		if err != nil {
			cancel()
		}
		watchDone <- err
	}()
	err = startServer(ctx, collector)
	cancel()
	return errors.Join(err, <-watchDone)
}

func printVersion() {
	fmt.Println("ping-exporter")
	fmt.Printf("Version: %s\n", version)
	fmt.Println("Author(s): Philip Berndroth, Daniel Czerwonk")
	fmt.Println("Metric exporter for go-icmp")
}

func startMonitor(ctx context.Context, cfg *config.Config, globalResolver Resolver) (*mon.Monitor, error) {
	var bind4, bind6 string
	if ln, err := net.Listen("tcp4", "127.0.0.1:0"); err == nil {
		// ipv4 enabled
		if err := ln.Close(); err != nil {
			return nil, fmt.Errorf("failed to close tcp4 listener: %w", err)
		}
		bind4 = "0.0.0.0"
	}
	if ln, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		// ipv6 enabled
		if err := ln.Close(); err != nil {
			return nil, fmt.Errorf("failed to close tcp6 listener: %w", err)
		}
		bind6 = "::"
	}
	pinger, err := ping.New(bind4, bind6)
	if err != nil {
		return nil, fmt.Errorf("cannot start monitoring: %w", err)
	}

	if pinger.PayloadSize() != cfg.Ping.Size {
		pinger.SetPayloadSize(cfg.Ping.Size)
	}

	if cfg.Ping.FirewallMark > 0 {
		err := pinger.SetMark(cfg.Ping.FirewallMark)
		if err != nil {
			return nil, fmt.Errorf("failed to set fwmark: %w", err)
		}
	}

	monitor := mon.New(pinger,
		cfg.Ping.Interval.Duration(),
		cfg.Ping.Timeout.Duration())
	monitor.HistorySize = cfg.Ping.History

	err = upsertTargets(desiredTargets, globalResolver, cfg, monitor)
	if err != nil {
		monitor.Stop()
		return nil, err
	}

	go startDNSAutoRefresh(ctx, cfg.DNS.Refresh.Duration(), desiredTargets, monitor, cfg)
	return monitor, nil
}

func upsertTargets(globalTargets *targets, globalResolver Resolver, cfg *config.Config, monitor *mon.Monitor) error {
	oldTargets := globalTargets.Targets()
	newTargets := make([]*target, len(cfg.Targets))
	var wg sync.WaitGroup
	var err error
	for i, t := range cfg.Targets {
		newTarget := globalTargets.Get(t.Addr)
		if newTarget == nil {
			resolver := globalResolver
			// check if there's a 'resolver' label in the target config
			// and if its set to 'k8s', then use the k8s resolver
			if r, ok := t.Labels["resolver"]; ok && r == "k8s" {
				resolver, err = NewK8sResolver()
				if err != nil {
					return fmt.Errorf("failed to create k8s resolver: %w", err)
				}
			}
			newTarget = &target{
				host:      t.Addr,
				addresses: make([]net.IPAddr, 0),
				delay:     time.Duration(10*i) * time.Millisecond,
				resolver:  resolver,
			}
		}

		newTargets[i] = newTarget

		wg.Go(func() {
			err := newTarget.addOrUpdateMonitor(monitor, targetOpts{
				disableIPv4: cfg.Options.DisableIPv4,
				disableIPv6: cfg.Options.DisableIPv6,
			}, cfg)
			if err != nil {
				log.Errorf("failed to setup target: %v", err)
			}
		})
	}
	wg.Wait()
	globalTargets.SetTargets(newTargets)

	removed := removedTargets(oldTargets, globalTargets)
	for _, removedTarget := range removed {
		log.Infof("remove target: %s", removedTarget.host)
		removedTarget.removeFromMonitor(monitor)
	}
	return nil
}

func watchConfig(ctx context.Context, globalTargets *targets, globalResolver Resolver, monitor *mon.Monitor, collector *pingCollector, initial *config.Config) error {
	if *configFile == "" {
		return nil
	}

	reload := func(cfg *config.Config) error {
		log.Infof("reloading config %s", *configFile)
		if err := upsertTargets(globalTargets, globalResolver, cfg, monitor); err != nil {
			return err
		}
		collector.UpdateConfig(cfg)
		return nil
	}

	if isHTTPConfig(*configFile) {
		watchHTTPConfig(ctx, initial, reload)
		return nil
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("unable to create file watcher: %w", err)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			log.Errorf("failed to close file watcher: %v", err)
		}
	}()

	err = watcher.Add(*configFile)
	if err != nil {
		return fmt.Errorf("unable to watch file: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("config watcher event channel closed")
			}
			log.Debugf("Got file inotify event: %s", event)
			// If the file is removed, the inotify watcher will lose track of the file. Add it again.
			if event.Op == inotify.Remove {
				if err = watcher.Add(*configFile); err != nil {
					return fmt.Errorf("failed to renew watch for file: %w", err)
				}
			}
			cfg, err := loadConfig()
			if err != nil {
				log.Errorf("unable to load config: %v", err)
				continue
			}
			// We get zero targets if the file was truncated. This happens if an automation tool rewrites
			// the complete file, instead of alternating only parts of it.
			if len(cfg.Targets) == 0 {
				continue
			}
			if err := reload(cfg); err != nil {
				log.Errorf("failed to reload config: %v", err)
				continue
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("config watcher error channel closed")
			}
			log.Errorf("watching file failed: %v", err)
		}
	}
}

func watchHTTPConfig(ctx context.Context, initial *config.Config, reload func(*config.Config) error) {
	ticker := time.NewTicker(*configRefresh)
	defer ticker.Stop()

	current := initial
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A stale disk cache must not roll back an already running configuration.
			cfg, err := loadConfigContext(ctx, false)
			if err != nil {
				log.Errorf("unable to load HTTP config: %v", err)
				continue
			}
			if reflect.DeepEqual(current, cfg) {
				continue
			}
			if err := reload(cfg); err != nil {
				log.Errorf("failed to reload HTTP config: %v", err)
				continue
			}
			current = cfg
		}
	}
}

func removedTargets(old []*target, new *targets) []*target {
	var ret []*target
	for _, oldTarget := range old {
		if !new.Contains(oldTarget) {
			ret = append(ret, oldTarget)
		}
	}
	return ret
}

func startDNSAutoRefresh(ctx context.Context, interval time.Duration, tar *targets, monitor *mon.Monitor, cfg *config.Config) {
	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshDNS(tar, monitor, cfg)
		}
	}
}

func refreshDNS(tar *targets, monitor *mon.Monitor, cfg *config.Config) {
	log.Infoln("refreshing DNS")
	for _, t := range tar.Targets() {
		go func(ta *target) {
			err := ta.addOrUpdateMonitor(monitor, targetOpts{
				disableIPv4: cfg.Options.DisableIPv4,
				disableIPv6: cfg.Options.DisableIPv6,
			}, cfg)
			if err != nil {
				log.Errorf("could not refresh dns: %v", err)
			}
		}(t)
	}
}

func startServer(ctx context.Context, collector prometheus.Collector) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var err error
	log.Infof("Starting ping exporter (Version: %s)", version)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !hasValidToken(r, w) {
			return
		}

		if _, err := fmt.Fprintf(w, indexHTML, *metricsPath); err != nil {
			log.Errorf("failed to write response: %v", err)
		}
	})

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)
	if len(*pushMetricsURLs) > 0 {
		extraLabels, err := parsePushMetricsExtraLabels(*pushMetricsExtraLabels)
		if err != nil {
			return fmt.Errorf("invalid metrics push labels: %w", err)
		}

		pushWorkers := make([]*pushMetricsWorker, len(*pushMetricsURLs))
		for idx, url := range *pushMetricsURLs {
			queueDir := filepath.Join(*pushMetricsQueueDir, strconv.Itoa(idx))
			queue, err := openMetricsQueue(queueDir, *pushMetricsMaxDiskUsage, *pushMetricsMaxInmemory)
			if err != nil {
				return fmt.Errorf("could not open metrics queue for endpoint #%d: %w", idx+1, err)
			}
			pushWorkers[idx] = &pushMetricsWorker{
				endpoint: url,
				queue:    queue,
				index:    idx,
			}
		}

		pushDone := make(chan error, 1)
		go func() {
			pushDone <- runPushMetricsMulti(ctx, reg, pushWorkers, *pushMetricsInterval, *pushMetricsTimeout, extraLabels)
		}()
		defer func() {
			cancel()
			result = errors.Join(result, <-pushDone)
		}()
		log.Infof("Metrics push enabled for %d endpoint(s) (interval %s, queue directory %s)",
			len(*pushMetricsURLs), *pushMetricsInterval, *pushMetricsQueueDir)
	}

	l := log.New()
	l.Level = log.ErrorLevel

	h := promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorLog:      l,
		ErrorHandling: promhttp.ContinueOnError,
	})
	mux.HandleFunc(*metricsPath, func(w http.ResponseWriter, r *http.Request) {
		if !hasValidToken(r, w) {
			return
		}

		h.ServeHTTP(w, r)
	})

	server := http.Server{
		Addr:              *listenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if *serverUseTLS {
		err = configureTLS(&server)
		if err != nil {
			return fmt.Errorf("could not configure TLS: %w", err)
		}
	}
	serverDone := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			err := server.Shutdown(shutdownCtx)
			if err != nil {
				err = errors.Join(err, server.Close())
			}
			shutdownDone <- err
		case <-serverDone:
			shutdownDone <- nil
		}
	}()

	if *serverUseTLS {
		log.Infof("Listening for %s on %s (HTTPS)", *metricsPath, *listenAddress)
		err = server.ListenAndServeTLS("", "")
	} else {
		log.Infof("Listening for %s on %s (HTTP)", *metricsPath, *listenAddress)
		err = server.ListenAndServe()
	}
	close(serverDone)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, <-shutdownDone)
}

func hasValidToken(r *http.Request, w http.ResponseWriter) bool {
	if *metricsToken == "" {
		return true
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("token")
	}

	if token != *metricsToken {
		w.WriteHeader(http.StatusForbidden)
		if _, err := fmt.Fprint(w, "wrong token"); err != nil {
			log.Errorf("failed to write response: %v", err)
		}
		return false
	}

	return true
}

func configureTLS(server *http.Server) error {
	if *serverTLSCertFile == "" || *serverTLSKeyFile == "" {
		return fmt.Errorf("'web.tls.cert-file' and 'web.tls.key-file' must be defined")
	}

	server.TLSConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	var err error
	server.TLSConfig.Certificates = make([]tls.Certificate, 1)
	server.TLSConfig.Certificates[0], err = tls.LoadX509KeyPair(*serverTLSCertFile, *serverTLSKeyFile)
	if err != nil {
		return fmt.Errorf("loading certificates error: %v", err)
	}

	if *serverMutualAuthEnabled {
		err = initMutualAuth(server)
		if err != nil {
			return fmt.Errorf("could not initialize mutual auth: %w", err)
		}
	} else {
		server.TLSConfig.ClientAuth = tls.NoClientCert
	}

	return nil
}

func initMutualAuth(server *http.Server) error {
	server.TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert

	if *serverTLSCAFile != "" {
		var err error
		var ca []byte
		if ca, err = os.ReadFile(*serverTLSCAFile); err != nil {
			return fmt.Errorf("loading CA error: %v", err)
		} else {
			server.TLSConfig.ClientCAs = x509.NewCertPool()
			server.TLSConfig.ClientCAs.AppendCertsFromPEM(ca)
		}
	}

	return nil
}

func loadConfig() (*config.Config, error) {
	return loadConfigContext(context.Background(), true)
}

func isHTTPConfig(path string) bool {
	path = strings.ToLower(path)
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

func loadConfigContext(ctx context.Context, allowCached bool) (*config.Config, error) {
	if *configFile == "" {
		cfg := config.Config{}
		addFlagToConfig(&cfg)

		return &cfg, nil
	}

	var f io.ReadCloser
	var err error
	if isHTTPConfig(*configFile) {
		f, err = fetchConfigWithCache(ctx, *configFile, getConfigCachePath(*configFile), allowCached)
	} else {
		f, err = os.Open(*configFile)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot load config file: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Errorf("failed to close config file: %v", err)
		}
	}()

	cfg, err := config.FromYAML(f)
	if err == nil {
		addFlagToConfig(cfg)
	}

	return cfg, err
}

func setupGlobalResolver(cfg *config.Config) Resolver {
	if cfg.DNS.Nameserver == "" {
		resolver := net.DefaultResolver
		return resolver
	}

	if _, _, err := net.SplitHostPort(cfg.DNS.Nameserver); err != nil {
		// No port present — add default DNS port, handling IPv6 correctly
		cfg.DNS.Nameserver = net.JoinHostPort(cfg.DNS.Nameserver, "53")
	}
	dialer := func(ctx context.Context, _, _ string) (net.Conn, error) {
		d := net.Dialer{}
		return d.DialContext(ctx, "udp", cfg.DNS.Nameserver)
	}

	return &net.Resolver{PreferGo: true, Dial: dialer}
}

// addFlagToConfig updates cfg with command line flag values, unless the
// config has non-zero values.
func addFlagToConfig(cfg *config.Config) {
	if cfg.Targets == nil && len(*targetFlag) > 0 {
		cfg.Targets = make([]config.TargetConfig, len(*targetFlag))
		for i, t := range *targetFlag {
			cfg.Targets[i] = config.TargetConfig{
				Addr: t,
			}
		}
	}
	if cfg.Ping.History == 0 {
		cfg.Ping.History = *historySize
	}
	if cfg.Ping.Interval == 0 {
		cfg.Ping.Interval.Set(*pingInterval)
	}
	if cfg.Ping.Timeout == 0 {
		cfg.Ping.Timeout.Set(*pingTimeout)
	}
	if cfg.Ping.Size == 0 {
		cfg.Ping.Size = *pingSize
	}
	if cfg.Ping.FirewallMark == 0 {
		cfg.Ping.FirewallMark = *firewallMark
	}
	if cfg.DNS.Refresh == 0 {
		cfg.DNS.Refresh.Set(*dnsRefresh)
	}
	if cfg.DNS.Nameserver == "" {
		cfg.DNS.Nameserver = *dnsNameServer
	}
	if !cfg.Options.DisableIPv6 {
		cfg.Options.DisableIPv6 = *disableIPv6
	}
	if !cfg.Options.DisableIPv4 {
		cfg.Options.DisableIPv4 = *disableIPv4
	}
	if cfg.DNS.Timeout == 0 {
		cfg.DNS.Timeout.Set(*dnsLookupTimeout)
	}
}
