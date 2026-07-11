// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.Options;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;

public class Main {

    private static final Logger log = LoggerFactory.getLogger(Main.class);

    /** JetStream stream owned by the gateway; the connector waits for it, never creates it. */
    static final String EVENTS_STREAM = "EVENTS";

    public static void main(String[] args) throws Exception {
        Config cfg = Config.fromEnv();
        log.info("opcua: starting connector={} endpoint={}", cfg.connectorId(), cfg.opcuaEndpoint());

        int healthPort = healthPortFromEnv();

        Options natsOpts = new Options.Builder()
            .server(cfg.natsUrl())
            .connectionListener((conn, type) -> log.info("nats: {}", type))
            .errorListener(new io.nats.client.ErrorListener() {})
            .build();

        Connection nats = Nats.connect(natsOpts);
        var js = nats.jetStream();

        Connector.Publisher publisher = (subject, data) -> js.publish(subject, data);

        MiloOpcUaClientFacade miloClient = new MiloOpcUaClientFacade(cfg.opcuaEndpoint(), cfg.security());
        Connector connector = new Connector(cfg, miloClient, publisher);

        HealthServer health = new HealthServer(healthPort, miloClient::isConnected);
        health.start();

        WriteHandler writeHandler = new WriteHandler(
            cfg, miloClient,
            (replyTo, data) -> nats.publish(replyTo, data)
        );

        String cmdSubject = "cmd.opcua." + cfg.connectorId();
        var dispatcher = nats.createDispatcher(msg -> writeHandler.handle(msg.getData(), msg.getReplyTo()));
        dispatcher.subscribe(cmdSubject);
        log.info("opcua: write handler subscribed to {}", cmdSubject);

        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            log.info("opcua: shutdown signal received");
            connector.stop();
            health.stop();
        }));

        // The gateway owns the EVENTS stream; wait for it before publishing so no events are lost.
        awaitEventsStream(nats, Duration.ofSeconds(5));

        connector.run();
        health.stop();
        try { dispatcher.unsubscribe(cmdSubject); } catch (Exception ignored) {}
        nats.close();
    }

    /** {@code HEALTH_PORT} env var (default 8080). Kept off the Config record to preserve its constructor. */
    static int healthPortFromEnv() {
        String raw = System.getenv("HEALTH_PORT");
        if (raw == null || raw.isBlank()) return 8080;
        try {
            return Integer.parseInt(raw.trim());
        } catch (NumberFormatException e) {
            log.warn("opcua: invalid HEALTH_PORT '{}' — falling back to 8080", raw);
            return 8080;
        }
    }

    /**
     * Block until the JetStream {@value #EVENTS_STREAM} stream exists, polling with the given interval.
     *
     * <p>The stream is provisioned by the gateway; the connector must not create it, only wait. Mirrors
     * the BACnet connector's {@code _await_stream}.
     */
    static void awaitEventsStream(Connection nats, Duration pollInterval) throws InterruptedException {
        while (true) {
            try {
                nats.jetStreamManagement().getStreamInfo(EVENTS_STREAM);
                return;
            } catch (Exception e) {
                log.info("opcua: {} stream not ready — waiting {}s (start the gateway, or create the stream manually)",
                    EVENTS_STREAM, pollInterval.toSeconds());
                Thread.sleep(pollInterval.toMillis());
            }
        }
    }
}
