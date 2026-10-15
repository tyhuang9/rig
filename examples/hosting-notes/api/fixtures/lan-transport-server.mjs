import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { WebSocketServer } from "ws";

const maxNoteBytes = 4 * 1024;
const maxTransportBytes = 64 * 1024;
const socketDeadlineMs = 5_000;
const defaultIdleDeadlineMs = 1_000;

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function json(response, status, value) {
  response.writeHead(status, { "content-type": "application/json" });
  response.end(JSON.stringify(value));
}

function headerValues(request, name) {
  const values = [];
  for (let index = 0; index < request.rawHeaders.length; index += 2) {
    if (request.rawHeaders[index].toLowerCase() === name) values.push(request.rawHeaders[index + 1]);
  }
  return values;
}

function readBody(request, limit) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    let settled = false;
    const finish = (callback, value) => {
      if (settled) return;
      settled = true;
      callback(value);
    };
    request.on("data", (chunk) => {
      size += chunk.length;
      if (size > limit) {
        request.destroy();
        finish(reject, new Error("body exceeds bounded fixture limit"));
        return;
      }
      chunks.push(chunk);
    });
    request.once("end", () => finish(resolve, Buffer.concat(chunks)));
    request.once("error", (error) => finish(reject, error));
  });
}

function multipartUpload(body, contentType) {
  const boundaryMatch = /^multipart\/form-data;\s*boundary=(?:"([^"]+)"|([^;\s]+))$/i.exec(contentType);
  const boundary = boundaryMatch?.[1] || boundaryMatch?.[2];
  if (!boundary) return undefined;
  const opening = Buffer.from(`--${boundary}\r\n`, "latin1");
  const closing = Buffer.from(`\r\n--${boundary}--\r\n`, "latin1");
  if (!body.subarray(0, opening.length).equals(opening) || !body.subarray(-closing.length).equals(closing)) return undefined;
  const headerEnd = body.indexOf(Buffer.from("\r\n\r\n", "latin1"), opening.length);
  if (headerEnd === -1) return undefined;
  const headers = new Map();
  for (const line of body.subarray(opening.length, headerEnd).toString("latin1").split("\r\n")) {
    const separator = line.indexOf(":");
    if (separator <= 0) return undefined;
    headers.set(line.slice(0, separator).toLowerCase(), line.slice(separator + 1).trim());
  }
  const disposition = /^form-data;\s*name="([^"]+)";\s*filename="([^"]+)"$/i.exec(headers.get("content-disposition") || "");
  const file = body.subarray(headerEnd + 4, body.length - closing.length);
  if (!disposition || headers.get("content-type") !== "application/octet-stream") return undefined;
  return {
    boundary,
    name: disposition[1],
    filename: disposition[2],
    contentType: headers.get("content-type"),
    bodyLength: file.length,
    sha256: sha256(file)
  };
}

function eventFrame(marker, barrier, sequence, paddingBytes = 0) {
  const payload = { runtimeMarker: marker, barrier, sequence };
  if (paddingBytes > 0) payload.padding = "x".repeat(paddingBytes);
  return `event: fixture\ndata: ${JSON.stringify(payload)}\n\n`;
}

// The controller's Docker acceptance stages this exact module as api/src/server.js.
// It intentionally keeps state in memory: durable application behavior belongs to
// the normal fixture, while this only exercises LAN transport preservation.
export function createLanTransportServer({
  marker = process.env.API_RUNTIME_MARKER || "unset",
  webSocketMode = process.env.LAN_TRANSPORT_WS_MODE || "echo",
  sseMode = process.env.LAN_TRANSPORT_SSE_MODE || "barrier",
  spaAssets = false,
  versionPaddingBytes = 0
} = {}) {
  const notes = [];
  const barriers = new Map();
  const connections = new Set();
  const idleWaiters = new Set();
  const sockets = new WebSocketServer({ noServer: true });
  const activeWebSockets = new Set();

  function notifyIdle() {
    if (barriers.size !== 0 || activeWebSockets.size !== 0) return;
    for (const resolve of idleWaiters) resolve();
    idleWaiters.clear();
  }

  function clearBarrier(barrier, entry) {
    if (barriers.get(barrier) !== entry) return;
    barriers.delete(barrier);
    notifyIdle();
  }

  function releaseBarrier(barrier, entry) {
    clearBarrier(barrier, entry);
    if (entry.released || entry.response.writableEnded) return;
    entry.released = true;
    entry.response.write(eventFrame(marker, barrier, 2));
    entry.response.end();
  }

  const server = createServer(async (request, response) => {
    const target = new URL(request.url, "http://fixture.invalid");
    const pathname = target.pathname;
    if (spaAssets && pathname === "/") {
      response.writeHead(200, { "content-type": "text/html" });
      response.end('<!doctype html><script type="module" src="/assets/fixture.js"></script>');
      return;
    }
    if (spaAssets && pathname === "/assets/fixture.js") {
      response.writeHead(200, { "content-type": "application/javascript" });
      response.end(`export const marker=${JSON.stringify(marker)};`);
      return;
    }
    if (pathname === "/readyz" || pathname === "/healthz") {
      json(response, 200, { status: "ready" });
      return;
    }
    if (pathname === "/api/version") {
      const version = { sourceVersion: "lan-fixture", runtimeMarker: marker };
      if (versionPaddingBytes > 0) version.padding = "x".repeat(versionPaddingBytes);
      json(response, 200, version);
      return;
    }
    if (pathname === "/api/notes" && request.method === "GET") {
      json(response, 200, { notes });
      return;
    }
    if (pathname === "/api/notes" && request.method === "POST") {
      try {
        const parsed = JSON.parse((await readBody(request, maxNoteBytes)).toString("utf8"));
        const body = typeof parsed.body === "string" ? parsed.body.trim() : "";
        if (!body || body.length > 500) {
          json(response, 400, { error: "note_body_invalid" });
          return;
        }
        const note = { id: notes.length + 1, body, createdAt: new Date().toISOString() };
        notes.push(note);
        json(response, 201, { note });
      } catch {
        json(response, 400, { error: "note_body_invalid" });
      }
      return;
    }
    if (pathname === "/api/transport" && request.method === "POST") {
      try {
        const body = await readBody(request, maxTransportBytes);
        json(response, 200, {
          runtimeMarker: marker,
          method: request.method,
          url: request.url,
          query: [...target.searchParams.entries()],
          headerValues: headerValues(request, "x-lan-transport"),
          contentType: request.headers["content-type"] || "",
          contentLength: request.headers["content-length"] || "",
          bodyLength: body.length,
          sha256: sha256(body)
        });
      } catch {
        json(response, 413, { error: "transport_body_invalid" });
      }
      return;
    }
    if (pathname === "/api/upload" && request.method === "POST") {
      try {
        const body = await readBody(request, maxTransportBytes);
        const multipart = multipartUpload(body, request.headers["content-type"] || "");
        if (!multipart) {
          json(response, 400, { error: "upload_body_invalid" });
          return;
        }
        json(response, 201, {
          runtimeMarker: marker,
          method: request.method,
          url: request.url,
          contentType: request.headers["content-type"] || "",
          bodyLength: body.length,
          sha256: sha256(body),
          multipart
        });
      } catch {
        json(response, 413, { error: "upload_body_invalid" });
      }
      return;
    }
    if (pathname === "/api/events" && request.method === "GET") {
      const requestedBarrier = target.searchParams.get("barrier");
      if (!requestedBarrier) {
        response.writeHead(200, { "cache-control": "no-store", "content-type": "text/event-stream" });
        response.end(`event: fixture\ndata: ${JSON.stringify({ runtimeMarker: marker })}\n\n`);
        return;
      }
      const barrier = requestedBarrier;
      if (barriers.has(barrier)) {
        json(response, 409, { error: "barrier_already_active" });
        return;
      }
      const entry = { response, released: false };
      barriers.set(barrier, entry);
      response.once("close", () => clearBarrier(barrier, entry));
      response.writeHead(200, {
        "cache-control": "no-store",
        "content-type": sseMode === "wrong_type" ? "application/json" : "text/event-stream"
      });
      if (sseMode === "buffer") return;
      if (sseMode === "oversized") {
        response.write(eventFrame(marker, barrier, 1, maxTransportBytes + 1));
        return;
      }
      const first = eventFrame(marker, barrier, 1).replace("event: fixture", sseMode === "wrong_event" ? "event: wrong" : "event: fixture");
      response.write(first.slice(0, 17));
      response.write(first.slice(17));
      return;
    }
    if (pathname === "/api/events/ack" && request.method === "POST") {
      const barrier = target.searchParams.get("barrier");
      const entry = barrier ? barriers.get(barrier) : undefined;
      if (!entry) {
        json(response, 404, { error: "barrier_not_found" });
        return;
      }
      response.writeHead(204);
      response.end();
      releaseBarrier(barrier, entry);
      return;
    }
    response.writeHead(404);
    response.end();
  });

  server.on("connection", (connection) => {
    connections.add(connection);
    connection.once("close", () => connections.delete(connection));
  });
  server.on("upgrade", (request, socket, head) => {
    const target = new URL(request.url, "http://fixture.invalid");
    if (target.pathname !== "/api/ws" || webSocketMode === "reject_upgrade") {
      socket.destroy();
      return;
    }
    sockets.handleUpgrade(request, socket, head, (websocket) => sockets.emit("connection", websocket, request));
  });
  sockets.on("connection", (socket) => {
    activeWebSockets.add(socket);
    const timeout = setTimeout(() => socket.close(1008, "message timeout"), socketDeadlineMs);
    socket.once("message", (message) => {
      clearTimeout(timeout);
      if (message.length > 1_024) {
        socket.close(1009, "message too large");
        return;
      }
      const echo = webSocketMode === "wrong_echo" ? `wrong:${message.toString()}` : message.toString();
      socket.send(webSocketMode === "malformed" ? "not-json" : JSON.stringify({ echo, runtimeMarker: marker }));
      socket.close(1000);
    });
    socket.once("close", () => {
      clearTimeout(timeout);
      activeWebSockets.delete(socket);
      notifyIdle();
    });
  });

  return {
    server,
    activeBarriers() { return barriers.size; },
    activeWebSockets() { return activeWebSockets.size; },
    waitForIdle({ timeoutMs = defaultIdleDeadlineMs } = {}) {
      if (barriers.size === 0 && activeWebSockets.size === 0) return Promise.resolve();
      return new Promise((resolve, reject) => {
        let waiter;
        const deadline = setTimeout(() => {
          idleWaiters.delete(waiter);
          reject(new Error("LAN transport fixture did not become idle before its deadline"));
        }, timeoutMs);
        waiter = () => {
          clearTimeout(deadline);
          resolve();
        };
        idleWaiters.add(waiter);
      });
    },
    close() {
      for (const [barrier, entry] of barriers) {
        clearBarrier(barrier, entry);
        entry.response.end();
      }
      for (const socket of sockets.clients) socket.close(1001, "fixture stopping");
      for (const connection of connections) connection.destroy();
      return new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    }
  };
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const fixture = createLanTransportServer();
  fixture.server.listen(Number(process.env.PORT || 3000), "0.0.0.0");
  const close = (signal) => {
    void fixture.close().then(() => process.exit(0), () => process.exit(1));
    console.info(`received ${signal}; stopping LAN transport fixture`);
  };
  process.once("SIGINT", () => close("SIGINT"));
  process.once("SIGTERM", () => close("SIGTERM"));
}
