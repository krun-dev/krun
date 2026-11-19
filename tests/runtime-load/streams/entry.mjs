import { WorkerEntrypoint } from "cloudflare:workers";

export class Private extends WorkerEntrypoint {
  fetch(request) {
    if (request.headers.get("upgrade")?.toLowerCase() === "websocket") {
      const [client, server] = Object.values(new WebSocketPair());
      server.accept();
      server.addEventListener("message", ({ data }) => server.send(data));
      return new Response(null, { status: 101, webSocket: client });
    }
    if (new URL(request.url).pathname === "/sse") {
      let heartbeat;
      const encoder = new TextEncoder();
      const body = new ReadableStream({
        start(controller) {
          const write = () => controller.enqueue(encoder.encode("data: test-token\n\n"));
          write();
          heartbeat = setInterval(write, 1000);
        },
        cancel() {
          clearInterval(heartbeat);
        },
      });
      return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
    }
    return new Response("ok");
  }
}

export class Management extends WorkerEntrypoint {
  fetch() {
    return new Response('{"status":"ready"}', {
      headers: {
        "x-krun-config-epoch": "0".repeat(32),
        "x-krun-config-revision": "1",
      },
    });
  }
}

export class Metrics extends WorkerEntrypoint {
  fetch() {
    return new Response("stream_fixture_ready 1\n");
  }
}
