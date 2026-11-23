// A binary protocol fixture for exercising the real Rust agent and workerd
// together. Production encoding remains owned by Go/configstream and Rust/prost.
import assert from "node:assert/strict";
import { WebSocketServer } from "ws";
import { fixtureConfigEpoch } from "./entry-source.mjs";

function integer(value) {
  let rest = BigInt(value);
  const bytes = [];
  do {
    bytes.push(Number(rest & 127n) | (rest > 127n ? 128 : 0));
    rest >>= 7n;
  } while (rest);
  return Buffer.from(bytes);
}
const number = (field, value) => Buffer.concat([integer(field * 8), integer(value)]);
const bytes = (field, value) => {
  value = Buffer.from(value);
  return Buffer.concat([integer(field * 8 + 2), integer(value.length), value]);
};
function decode(raw) {
  let offset = 0;
  const read = () => {
    let value = 0n;
    let shift = 0n;
    for (;;) {
      const byte = raw[offset++];
      assert.notEqual(byte, undefined);
      value |= BigInt(byte & 127) << shift;
      if (!(byte & 128)) return Number(value);
      shift += 7n;
      assert.ok(shift < 70n);
    }
  };
  const out = new Map();
  while (offset < raw.length) {
    const tag = read();
    let value;
    if (tag % 8 === 2) {
      const length = read();
      value = raw.subarray(offset, offset + length);
      offset += length;
    } else {
      assert.equal(tag % 8, 0);
      value = read();
    }
    const field = Math.floor(tag / 8);
    out.set(field, [...(out.get(field) || []), value]);
  }
  return out;
}
const reference = (kind, id) =>
  Buffer.concat([bytes(1, kind), bytes(2, id), bytes(3, "fixture"), number(4, 1)]);
const resource = (kind, id, payload, dependencies = []) => {
  const ref = reference(kind, id);
  return {
    ref,
    wire: Buffer.concat([
      bytes(1, ref),
      bytes(3, JSON.stringify(payload)),
      ...dependencies.map(([kind, id]) => bytes(4, reference(kind, id))),
    ]),
  };
};

export function resourceStreamFixture(server, descriptor, acknowledged) {
  const websocket = new WebSocketServer({ noServer: true });
  let online = true;
  server.on("upgrade", (request, socket, head) => {
    if (!online) {
      socket.end("HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\n\r\n");
      return;
    }
    assert.equal(request.headers.authorization, "Bearer test");
    assert.equal(request.headers["x-runtime-pool"], "default");
    websocket.handleUpgrade(request, socket, head, (client) =>
      websocket.emit("connection", client),
    );
  });
  websocket.on("connection", (socket) => {
    let nonce = 0;
    let applied = false;
    const send = (type, fields = []) =>
      socket.send(
        Buffer.concat([
          bytes(1, type),
          bytes(3, fixtureConfigEpoch),
          number(13, ++nonce),
          ...fields,
        ]),
      );
    const timer = setInterval(() => {
      send("status", [number(8, Date.now()), number(9, 1), number(14, applied ? 1 : 0)]);
    }, 250);
    socket.on("close", () => clearInterval(timer));
    socket.on("message", (raw) => {
      const frame = decode(raw);
      const type = frame.get(1)?.[0].toString();
      if (type === "hello") send("welcome", [number(8, Date.now()), number(9, 1)]);
      if (type === "ack") {
        acknowledged();
        applied = true;
      }
      assert.notEqual(type, "nack", "native agent rejected resource fixture");
      if (type !== "resolve" && type !== "subscribe") return;
      const resources = new Map();
      const add = (kind, id, value, deps) =>
        resources.set(kind + ":" + id, resource(kind, id, value, deps));
      for (const wire of frame.get(5) || []) {
        const ref = decode(wire);
        const id = ref.get(2)[0].toString();
        const host = id.split("/")[1];
        add(
          "route",
          id,
          [
            {
              host,
              worker_id: descriptor.worker_id,
              environment: descriptor.environment,
              version: "",
              enabled: true,
              pool: "default",
              path: "/",
            },
          ],
          [
            ["scope", descriptor.worker_id],
            ["deployment", descriptor.worker_id + "/" + descriptor.environment],
          ],
        );
      }
      add("scope", descriptor.worker_id, { allowed: true }, [
        ["organization", descriptor.organization_id],
      ]);
      add("organization", descriptor.organization_id, { allowed: true });
      add(
        "deployment",
        descriptor.worker_id + "/" + descriptor.environment,
        { enabled: true, version: descriptor.id },
        [["version", descriptor.id]],
      );
      add("version", descriptor.id, descriptor, [["scope", descriptor.worker_id]]);
      send("snapshot", [
        number(2, frame.get(2)?.[0] || 0),
        ...[...resources.values()].flatMap((r) => [bytes(5, r.ref), bytes(6, r.wire)]),
      ]);
    });
  });
  const disconnect = () => {
    online = false;
    for (const client of websocket.clients) client.terminate();
  };
  return {
    disconnect,
    close() {
      disconnect();
      websocket.close();
    },
  };
}
