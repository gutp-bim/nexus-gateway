// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.Options;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

public class Main {

    private static final Logger log = LoggerFactory.getLogger(Main.class);

    public static void main(String[] args) throws Exception {
        Config cfg = Config.fromEnv();
        log.info("opcua: starting connector={} endpoint={}", cfg.connectorId(), cfg.opcuaEndpoint());

        Options natsOpts = new Options.Builder()
            .server(cfg.natsUrl())
            .connectionListener((conn, type) -> log.info("nats: {}", type))
            .errorListener(new io.nats.client.ErrorListener() {})
            .build();

        Connection nats = Nats.connect(natsOpts);
        var js = nats.jetStream();

        Connector.Publisher publisher = (subject, data) -> js.publish(subject, data);

        MiloOpcUaClientFacade miloClient = new MiloOpcUaClientFacade(cfg.opcuaEndpoint());
        Connector connector = new Connector(cfg, miloClient, publisher);

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
        }));

        connector.run();
        try { dispatcher.unsubscribe(cmdSubject); } catch (Exception ignored) {}
        nats.close();
    }
}
