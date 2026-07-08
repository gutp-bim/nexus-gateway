// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"net/http"

	dockerclient "github.com/docker/docker/client"

	"nexus-gateway/connector/sim"
	"nexus-gateway/internal/adminapi"
	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/egress"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/pointsync"
	"nexus-gateway/internal/provisioning"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/transport"
	"nexus-gateway/internal/uplink"
)

func main() {
	natsURL := flag.String("nats", envOrDefault("NATS_URL", nats.DefaultURL), "NATS URL")
	bosAddr := flag.String("bos", envOrDefault("BOS_ADDR", "localhost:50051"), "Building OS gRPC address (default for both ingress and egress; overridden by --bos-ingress-addr / --bos-egress-addr)")
	bosIngressAddr := flag.String("bos-ingress-addr", envOrDefault("BOS_INGRESS_ADDR", ""), "Building OS GatewayIngress gRPC address for telemetry (overrides --bos; env BOS_INGRESS_ADDR)")
	bosEgressAddr := flag.String("bos-egress-addr", envOrDefault("BOS_EGRESS_ADDR", ""), "Building OS GatewayEgress gRPC address for control (overrides --bos; env BOS_EGRESS_ADDR)")
	gatewayID := flag.String("gateway-id", envOrDefault("GATEWAY_ID", "gw-001"), "Gateway ID")
	adminAddr := flag.String("admin-addr", envOrDefault("ADMIN_ADDR", ":8080"), "Admin API listen address")
	jwksURL := flag.String("admin-jwks-url", envOrDefault("KEYCLOAK_JWKS_URL", ""), "Keycloak JWKS URL (empty = auth disabled)")
	adminAudience := flag.String("admin-audience", envOrDefault("KEYCLOAK_AUDIENCE", "account"), "Expected JWT audience")
	adminIssuer := flag.String("admin-issuer", envOrDefault("KEYCLOAK_ISSUER", ""), "Expected JWT issuer")
	plFile := flag.String("point-list", envOrDefault("POINT_LIST_FILE", "fixtures/point_list.json"), "Bootstrap fixture point list (used when both --provisioning-url and --provisioning-file are empty)")
	plPersist := flag.String("point-list-persist", envOrDefault("POINT_LIST_PERSIST", "data/point_list.json"), "Path to persist the synced point list")
	provURL := flag.String("provisioning-url", envOrDefault("PROVISIONING_URL", ""), "Provisioning API base URL (empty = fixture only)")
	provFile := flag.String("provisioning-file", envOrDefault("PROVISIONING_FILE", ""), "File-backed Point List provisioning source (.csv or .json); overridden by --provisioning-url")
	provConnID := flag.String("provisioning-connector-id", envOrDefault("PROVISIONING_CONNECTOR_ID", "bacnet-01"), "Connector id stamped on entries loaded from a provisioning CSV")
	connectorMapStr := flag.String("connector-map", envOrDefault("CONNECTOR_MAP", ""),
		`Comma-separated protocol:connectorID pairs, shared by both the HTTP and
file provisioning paths (e.g. "bacnet:bacnet-01,opcua:opcua-01,mqtt:mqtt-01").
A row/point whose protocol has no entry here falls back to
--provisioning-connector-id. When empty entirely, falls back to
bacnet:<provisioning-connector-id>.`)
	sfDB := flag.String("sf-db", envOrDefault("SF_DB", "data/storeforward.db"), "Store-and-Forward SQLite database path")
	sfCap := flag.Int("sf-cap", 100_000, "Store-and-Forward ring buffer capacity (frames)")
	devSim := flag.Bool("dev-sim", envOrDefault("DEV_SIM", "") == "true", "Run an in-process sim connector (dev/smoke only, non-production; ADR-0001)")
	devSimInterval := flag.Duration("dev-sim-interval", 60*time.Second, "Publish interval for --dev-sim (1-min default; lower for fast local feedback)")
	allowAdhocUpgrade := flag.Bool("allow-adhoc-upgrade", envOrDefault("ALLOW_ADHOC_UPGRADE", "") == "true", "Enable the dev-only POST /connectors/{id}/upgrade?image= action; MVP update path is catalog-driven (ADR-0006)")
	syncInterval := flag.Duration("point-sync-interval", 10*time.Minute, "Point List poll interval after the initial sync (the list is near-static, ADR-0003)")
	bosInsecure := flag.Bool("bos-insecure", envOrDefault("BOS_INSECURE", "") == "true", "Dial Building OS over plaintext h2c (no TLS) — dev/CI only (ADR-0007)")
	bosCA := flag.String("bos-ca", envOrDefault("BOS_CA_FILE", ""), "PEM CA bundle to verify the Building OS server cert (empty = system roots)")
	bosCert := flag.String("bos-cert", envOrDefault("BOS_CERT_FILE", ""), "Client certificate for mTLS to Building OS (CN/SAN = gateway_id)")
	bosKey := flag.String("bos-key", envOrDefault("BOS_KEY_FILE", ""), "Client private key for mTLS to Building OS")
	bosServerName := flag.String("bos-servername", envOrDefault("BOS_SERVER_NAME", ""), "Override the server name verified in the Building OS cert")
	catalogFile := flag.String("catalog-file", envOrDefault("CATALOG_FILE", ""), "File-backed Connector Catalog (JSON []Manifest); enables POST /connectors/{name}/install")
	catalogURL := flag.String("catalog-url", envOrDefault("CATALOG_URL", ""), "Remote Connector Catalog base URL; overrides --catalog-file when set")
	catalogAllowlist := flag.String("catalog-allowlist", envOrDefault("CATALOG_ALLOWLIST", "ghcr.io"), "Comma-separated list of allowed OCI registries (ADR-0006)")
	catalogPollInterval := flag.Duration("catalog-poll-interval", 10*time.Minute, "How often the Updater polls the catalog for new connector versions (ADR-0006)")
	cosignKey := flag.String("cosign-key", envOrDefault("COSIGN_KEY_FILE", ""), "Path to cosign public key for signature verification (ADR-0006); empty = keyless")
	cosignIdentity := flag.String("cosign-identity", envOrDefault("COSIGN_IDENTITY", ""), "Expected certificate identity for keyless cosign verification (ADR-0006)")
	cosignOIDCIssuer := flag.String("cosign-oidc-issuer", envOrDefault("COSIGN_OIDC_ISSUER", ""), "Expected OIDC issuer for keyless cosign verification (ADR-0006)")
	flag.Parse()
	*bosIngressAddr = resolveBOSAddr(*bosAddr, *bosIngressAddr)
	*bosEgressAddr = resolveBOSAddr(*bosAddr, *bosEgressAddr)

	// Resolve the protocol→connectorID map, shared by both the HTTP and file
	// provisioning paths. Falls back to {"bacnet": provConnID} when
	// CONNECTOR_MAP is unset for backward compatibility.
	cmap, err := parseConnectorMap(*connectorMapStr)
	if err != nil {
		slog.Error("invalid --connector-map / CONNECTOR_MAP", "err", err)
		os.Exit(1)
	}
	if len(cmap) == 0 {
		cmap = map[string]string{"bacnet": *provConnID}
	}

	// Build the gRPC transport credentials for both Building OS links (ADR-0007).
	bosCreds, err := transport.ClientCredentials(transport.Config{
		Insecure:   *bosInsecure,
		CAFile:     *bosCA,
		CertFile:   *bosCert,
		KeyFile:    *bosKey,
		ServerName: *bosServerName,
	})
	if err != nil {
		slog.Error("Building OS transport credentials", "err", err)
		os.Exit(1)
	}
	if *bosInsecure {
		slog.Warn("Building OS link is plaintext h2c (--bos-insecure) — dev/CI only")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to NATS
	nc, err := nats.Connect(*natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		slog.Error("NATS connect failed", "err", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("JetStream init failed", "err", err)
		os.Exit(1)
	}

	// Provision EVENTS stream (ADR-0005)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "EVENTS",
		Subjects:  []string{"evt.>"},
		MaxAge:    48 * time.Hour,
		MaxBytes:  2 * 1024 * 1024 * 1024,
		Discard:   jetstream.DiscardOld,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
	}); err != nil {
		slog.Error("EVENTS stream provision failed", "err", err)
		os.Exit(1)
	}

	// Build the live point list resolver. Source precedence (ADR-0003): an
	// authoritative provisioning source (HTTP API, or a file-backed stand-in)
	// always overrides the local fixture bootstrap once synced.
	resolver := pointlist.NewSynced(nil)
	var provClient provisioning.Client
	switch {
	case *provURL != "":
		provClient = provisioning.NewHTTPClient(*provURL, *gatewayID, cmap)
	case *provFile != "":
		// Fail fast on a bad path rather than spinning the startup wait and then
		// running with an empty Point List.
		switch fi, err := os.Stat(*provFile); {
		case err != nil:
			slog.Error("provisioning file not readable", "path", *provFile, "err", err)
			os.Exit(1)
		case !fi.Mode().IsRegular():
			slog.Error("provisioning file is not a regular file", "path", *provFile)
			os.Exit(1)
		}
		provClient = provisioning.NewFileClient(*provFile, *provConnID, cmap)
	}
	// Ensure the persist directory exists before the sync loop tries to write.
	if *plPersist != "" {
		if err := os.MkdirAll(filepath.Dir(*plPersist), 0o755); err != nil {
			slog.Error("point list persist dir create failed", "err", err)
			os.Exit(1)
		}
	}

	// revalidatePL is signalled by the egress agent on EgressDown.point_list_update (#224/push).
	revalidatePL := make(chan struct{}, 1)
	if provClient != nil {
		// Real sync loop against the provisioning source (ADR-0003)
		syncLoop := pointsync.New(
			provClient,
			resolver,
			pointsync.Config{Interval: *syncInterval, PersistPath: *plPersist},
		).WithRevalidate(revalidatePL)
		go syncLoop.Run(ctx)
		// Wait for the first sync to complete (Ready() closes on success or failure).
		select {
		case <-syncLoop.Ready():
		case <-time.After(30 * time.Second):
		}
		if len(resolver.Snapshot()) == 0 {
			// Proceeding with an empty resolver means every Common Event resolves to a
			// point-list miss and is dropped (ADR-0002). Make that loud rather than silent.
			slog.Error("point list: initial sync did not complete within 30s — starting with an empty Point List; telemetry will be dropped as point-list misses until sync succeeds")
		}
	} else {
		// Bootstrap from fixture file (dev / no provisioning API)
		entries, err := loadFixtureEntries(*plFile)
		if err != nil {
			slog.Error("load point list failed", "err", err)
			os.Exit(1)
		}
		resolver.Update(entries)
	}

	// Start Normalizer
	norm, err := normalizer.New(ctx, js, resolver, *gatewayID)
	if err != nil {
		slog.Error("normalizer init failed", "err", err)
		os.Exit(1)
	}

	// Open Store-and-Forward buffer (create parent directory if needed)
	if err := os.MkdirAll(filepath.Dir(*sfDB), 0o755); err != nil {
		slog.Error("storeforward dir create failed", "err", err)
		os.Exit(1)
	}
	buf, err := storeforward.Open(*sfDB, *sfCap)
	if err != nil {
		slog.Error("storeforward open failed", "err", err)
		os.Exit(1)
	}
	var pumpWg sync.WaitGroup
	pumpWg.Add(1)
	go func() {
		defer pumpWg.Done()
		storeforward.Pump(ctx, norm.Frames(), buf)
	}()

	// Start Ingress uplink
	ul, err := uplink.NewIngress(ctx, *bosIngressAddr, *gatewayID, buf, uplink.DefaultConfig, bosCreds)
	if err != nil {
		slog.Error("uplink init failed", "err", err)
		os.Exit(1)
	}
	go ul.Run(ctx)

	// Start Egress agent (control path, ADR-0004); also signals revalidatePL on PointListUpdate.
	d := dispatch.New(nc, resolver, 5*time.Second)
	egressAgent := egress.New(*bosEgressAddr, *gatewayID, d, bosCreds, revalidatePL)
	go egressAgent.Run(ctx)

	// Start Admin API
	connRegistry := lifecycle.NewRegistry()
	docker, dockerErr := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if dockerErr != nil {
		slog.Warn("admin: Docker client unavailable — lifecycle actions disabled", "err", dockerErr)
	}
	var dockerCC lifecycle.ContainerClient
	if docker != nil {
		dockerCC = docker
	}
	connMgr := lifecycle.NewManager(dockerCC, connRegistry)
	healthMon := lifecycle.NewHealthMonitor(dockerCC, connRegistry)

	// Build the Connector Catalog installer if a catalog source is configured (ADR-0006).
	var catalogInstaller adminapi.ConnectorInstaller
	var catalogSrc adminapi.CatalogSource
	{
		var catalogClient catalog.Client
		switch {
		case *catalogURL != "":
			catalogClient = catalog.NewHTTPClient(*catalogURL)
		case *catalogFile != "":
			catalogClient = catalog.NewFileClient(*catalogFile)
		}
		if catalogClient != nil {
			allowlist := splitComma(*catalogAllowlist)
			var verifier catalog.Verifier
			if *cosignKey != "" || *cosignIdentity != "" {
				verifier = catalog.CosignVerifier{
					KeyPath:    *cosignKey,
					Identity:   *cosignIdentity,
					OIDCIssuer: *cosignOIDCIssuer,
				}
				slog.Info("catalog: cosign verification enabled", "key", *cosignKey, "identity", *cosignIdentity)
			} else {
				verifier = catalog.NoopVerifier{}
				slog.Warn("catalog: cosign verification disabled — set --cosign-key or --cosign-identity before production use (ADR-0006)")
			}
			gi := &gatewayInstaller{
				mgr:       connMgr,
				client:    catalogClient,
				verifier:  verifier,
				allowlist: allowlist,
				gwVersion: "0.1.0",
			}
			catalogInstaller = gi
			catalogSrc = gi
			// Start the background update loop (ADR-0006 poll-only model).
			updater := lifecycle.NewUpdater(connMgr, connRegistry, catalogClient, verifier, allowlist, "0.1.0",
				lifecycle.UpdaterConfig{SoakWindow: 30 * time.Second})
			go updater.Run(ctx, *catalogPollInterval)
			slog.Info("catalog: connector install + update enabled", "allowlist", allowlist, "poll_interval", *catalogPollInterval)
		}
	}

	adminOpts := adminapi.ServerOptions{
		Installer:         catalogInstaller,
		Catalog:           catalogSrc,
		PointList:         resolver,
		Telemetry:         buf,
		Logger:            connMgr,
		AllowAdhocUpgrade: *allowAdhocUpgrade,
	}
	var adminSrv *adminapi.Server
	if *jwksURL != "" {
		adminSrv = adminapi.NewSecureServer(connMgr, healthMon, adminOpts,
			adminapi.JWTConfig{JWKSURL: *jwksURL, Audience: *adminAudience, Issuer: *adminIssuer})
	} else {
		slog.Warn("admin: JWT auth disabled — set KEYCLOAK_JWKS_URL before exposing this port")
		adminSrv = adminapi.NewServer(connMgr, healthMon, adminOpts)
	}
	httpSrv := &http.Server{Addr: *adminAddr, Handler: adminSrv}
	go func() {
		slog.Info("admin: listening", "addr", *adminAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("admin: server error", "err", err)
		}
	}()

	// The in-process sim connector is dev/smoke only and off by default: the
	// default build runs no in-process connector, keeping connector isolation
	// (ADR-0001). Real protocol simulators (EP-009) supersede it.
	if *devSim {
		slog.Warn("dev-sim enabled — in-process sim connector running (non-production, ADR-0001)")
		startDevSim(ctx, js, connRegistry, *devSimInterval)
	}

	slog.Info("gateway started", "gateway_id", *gatewayID, "nats", *natsURL, "bos-ingress", *bosIngressAddr, "bos-egress", *bosEgressAddr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	slog.Info("gateway shutting down")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		slog.Warn("admin: shutdown error", "err", err)
	}
	if adminSrv != nil {
		adminSrv.Shutdown()
	}
	pumpWg.Wait()
	buf.Close()
}

// startDevSim registers and runs the in-process sim connector (dev/smoke only).
// It is invoked only under --dev-sim; the connector runs as a goroutine (no
// container), so its ContainerID stays empty and Docker inspection is skipped.
func startDevSim(ctx context.Context, js jetstream.JetStream, reg *lifecycle.Registry, interval time.Duration) {
	reg.Register(lifecycle.ConnectorSpec{ID: "sim-01", Image: "sim:dev"})
	reg.SetRunning("sim-01", "", true)
	connector := sim.New("sim-01", js, interval, []sim.Point{
		{LocalID: "sim://ahu-01/supply_air_temp", DeviceRef: "sim://ahu-01", Unit: "Cel", BaseValue: 22.0, Amplitude: 3.0},
		{LocalID: "sim://ahu-01/fan_run", DeviceRef: "sim://ahu-01", Unit: "", BaseValue: 1.0, Amplitude: 0.0},
	})
	go connector.Run(ctx)
	// Reflect the connector's lifetime in the registry so the Admin UI does not show
	// sim-01 as running after shutdown.
	go func() {
		<-ctx.Done()
		reg.SetRunning("sim-01", "", false)
	}()
}

func loadFixtureEntries(path string) ([]pointlist.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []pointlist.Entry
	return entries, json.Unmarshal(data, &entries)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// resolveBOSAddr returns override when non-empty, otherwise falls back to bosAddr.
// This allows BOS_INGRESS_ADDR / BOS_EGRESS_ADDR to override the shared BOS_ADDR default.
func resolveBOSAddr(bosAddr, override string) string {
	if override != "" {
		return override
	}
	return bosAddr
}

// parseConnectorMap parses a comma-separated "protocol:connectorID" string into a map.
// Empty string and trailing/extra commas are tolerated (empty entries are skipped).
// Returns an error for malformed entries (missing colon, empty key, or empty value after trim).
func parseConnectorMap(s string) (map[string]string, error) {
	m := make(map[string]string)
	for _, pair := range splitComma(s) { // splitComma handles empty entries and outer whitespace
		k, v, ok := strings.Cut(pair, ":")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("invalid connector-map entry %q: must be protocol:connectorID", pair)
		}
		m[k] = v
	}
	return m, nil
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// gatewayInstaller implements adminapi.ConnectorInstaller by fetching a manifest
// from the Connector Catalog and delegating to lifecycle.Manager.Install.
type gatewayInstaller struct {
	mgr       *lifecycle.Manager
	client    catalog.Client
	verifier  catalog.Verifier
	allowlist []string
	gwVersion string
}

func (gi *gatewayInstaller) Install(ctx context.Context, name string) error {
	m, err := gi.client.Fetch(ctx, name)
	if err != nil {
		return err
	}
	return gi.mgr.Install(ctx, m, gi.verifier, gi.allowlist, gi.gwVersion)
}

// ListAll satisfies adminapi.CatalogSource — lists all connectors available in the catalog.
func (gi *gatewayInstaller) ListAll(ctx context.Context) ([]catalog.Manifest, error) {
	return gi.client.List(ctx)
}

// Update satisfies adminapi.CatalogSource — fetches the latest manifest and applies it.
func (gi *gatewayInstaller) Update(ctx context.Context, id string) error {
	m, err := gi.client.Fetch(ctx, id)
	if err != nil {
		return err
	}
	return gi.mgr.Update(ctx, id, m, gi.verifier, gi.allowlist, gi.gwVersion, 30*time.Second)
}
