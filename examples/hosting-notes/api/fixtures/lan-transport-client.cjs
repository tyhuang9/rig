const { createHash } = require("node:crypto");
const http = require("node:http");

const defaultTimeoutMs = 6_000;
const defaultMaxResponseBytes = 4 * 1024 * 1024;
const defaultMaxSSEBytes = 64 * 1024;

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function fail(message) {
  throw new Error(`LAN transport assertion failed: ${message}`);
}

function request({
  address, port, method = "GET", path, host, headers = {}, body, timeoutMs = defaultTimeoutMs,
  maxBodyBytes = defaultMaxResponseBytes, signal
}) {
  return new Promise((resolve, reject) => {
    let settled = false;
    let req;
    let deadline;
    const onAbort = () => req?.destroy(new Error("HTTP request aborted"));
    const finish = (callback, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(deadline);
      signal?.removeEventListener("abort", onAbort);
      callback(value);
    };
    req = http.request({
      host: address,
      port: Number(port),
      method,
      path,
      headers: { host, connection: "close", ...headers }
    }, (response) => {
      const chunks = [];
      let length = 0;
      response.on("data", (chunk) => {
        length += chunk.length;
        if (length > maxBodyBytes) {
          req.destroy(new Error("response exceeded bounded client limit"));
          finish(reject, new Error("response exceeded bounded client limit"));
          return;
        }
        chunks.push(chunk);
      });
      response.once("end", () => finish(resolve, { status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks) }));
      response.once("error", (error) => finish(reject, error));
    });
    deadline = setTimeout(() => req.destroy(new Error("HTTP deadline exceeded")), timeoutMs);
    req.once("error", (error) => finish(reject, error));
    if (signal?.aborted) {
      onAbort();
      return;
    }
    signal?.addEventListener("abort", onAbort, { once: true });
    req.end(body);
  });
}

function parseJSON(response, label) {
  try {
    return JSON.parse(response.body.toString("utf8"));
  } catch {
    fail(`${label} response was not JSON`);
  }
}

function assertStatus(response, status, label) {
  if (response.status !== status) fail(`${label} status=${response.status} want=${status}`);
}

function assertMarker(response, marker, label) {
  const value = parseJSON(response, label);
  if (value.runtimeMarker !== marker) fail(`${label} marker mismatch`);
  return value;
}

async function verifyTransport({ address, port, host = address, marker, timeoutMs, expectedUploadDigest }) {
  const transportBody = Buffer.from([0x00, 0xff, 0x61, 0x62, 0x0a, 0x7f]);
  const transportPath = "/api/transport?repeat=one&repeat=two&encoded=one%20two";
  const transport = await request({
    address, port, host, timeoutMs, method: "POST", path: transportPath, body: transportBody,
    headers: {
      "content-type": "application/octet-stream",
      "content-length": String(transportBody.length),
      "x-lan-transport": ["first", "second"]
    }
  });
  assertStatus(transport, 200, "transport");
  const observed = assertMarker(transport, marker, "transport");
  if (observed.method !== "POST" || observed.url !== transportPath ||
      JSON.stringify(observed.query) !== JSON.stringify([["repeat", "one"], ["repeat", "two"], ["encoded", "one two"]]) ||
      JSON.stringify(observed.headerValues) !== JSON.stringify(["first", "second"]) ||
      observed.contentType !== "application/octet-stream" || observed.contentLength !== String(transportBody.length) ||
      observed.bodyLength !== transportBody.length || observed.sha256 !== sha256(transportBody)) {
    fail("body/query/header forwarding changed");
  }

  const boundary = "----rig-lan-transport-boundary";
  const uploadFile = Buffer.from([0x72, 0x69, 0x67, 0x00, 0xff, 0x0a]);
  const uploadBody = Buffer.concat([
    Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="artifact"; filename="fixture.bin"\r\nContent-Type: application/octet-stream\r\n\r\n`, "latin1"),
    uploadFile,
    Buffer.from(`\r\n--${boundary}--\r\n`, "latin1")
  ]);
  const uploadPath = "/api/upload?file=fixture.bin&file=ignored";
  const upload = await request({
    address, port, host, timeoutMs, method: "POST", path: uploadPath, body: uploadBody,
    headers: {
      "content-type": `multipart/form-data; boundary=${boundary}`,
      "content-length": String(uploadBody.length)
    }
  });
  assertStatus(upload, 201, "upload");
  const uploaded = assertMarker(upload, marker, "upload");
  const uploadDigest = expectedUploadDigest || sha256(uploadFile);
  if (uploaded.method !== "POST" || uploaded.url !== uploadPath || uploaded.contentType !== `multipart/form-data; boundary=${boundary}` ||
      uploaded.bodyLength !== uploadBody.length || uploaded.sha256 !== sha256(uploadBody) ||
      uploaded.multipart?.boundary !== boundary || uploaded.multipart?.name !== "artifact" || uploaded.multipart?.filename !== "fixture.bin" ||
      uploaded.multipart?.contentType !== "application/octet-stream" || uploaded.multipart?.bodyLength !== uploadFile.length ||
      uploaded.multipart?.sha256 !== uploadDigest) {
    fail("upload forwarding changed");
  }
}

async function verifyWebSocket({ address, port, marker, timeoutMs = defaultTimeoutMs }) {
  if (typeof WebSocket !== "function") fail("Node built-in WebSocket is unavailable");
  const payload = "rig-lan-websocket";
  await new Promise((resolve, reject) => {
    let settled = false;
    let socket;
    const finish = (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(deadline);
      if (error) {
        try { socket?.close(); } catch {}
        reject(error);
        return;
      }
      resolve();
    };
    const deadline = setTimeout(() => {
      finish(new Error("WebSocket deadline exceeded"));
    }, timeoutMs);
    try {
      socket = new WebSocket(`ws://${address}:${port}/api/ws`);
    } catch {
      finish(new Error("WebSocket upgrade failed"));
      return;
    }
    let message;
    socket.addEventListener("open", () => socket.send(payload));
    socket.addEventListener("message", (event) => {
      try {
        message = JSON.parse(String(event.data));
      } catch {
        finish(new Error("WebSocket message was not JSON"));
      }
    });
    socket.addEventListener("error", () => finish(new Error("WebSocket upgrade failed")));
    socket.addEventListener("close", (event) => {
      if (!message) {
        finish(new Error(`WebSocket closed before its echo (${event.code})`));
        return;
      }
      if (event.code !== 1000 || message.echo !== payload || message.runtimeMarker !== marker) {
        finish(new Error("WebSocket echo or clean close changed"));
        return;
      }
      finish();
    });
  });
}

function decodeSSE(buffer) {
  const frames = [];
  let remainder = buffer;
  while (true) {
    const index = remainder.indexOf("\n\n");
    if (index === -1) return { frames, remainder };
    const raw = remainder.slice(0, index);
    remainder = remainder.slice(index + 2);
    const lines = raw.split("\n");
    const data = lines.filter((line) => line.startsWith("data: ")).map((line) => line.slice(6)).join("\n");
    const event = lines.filter((line) => line.startsWith("event: ")).map((line) => line.slice(7)).at(-1);
    if (data) frames.push({ event, data: JSON.parse(data) });
  }
}

async function verifySSE({
  address, port, host = address, marker, timeoutMs = defaultTimeoutMs, barrier = "rig-lan-sse",
  maxSSEBytes = defaultMaxSSEBytes
}) {
  const abort = new AbortController();
  let reader;
  let clearDeadline;
  const timed = new Promise((_, reject) => {
    const timer = setTimeout(() => {
      abort.abort();
      reject(new Error("SSE deadline exceeded before the next event"));
    }, timeoutMs);
    clearDeadline = () => clearTimeout(timer);
  });
  try {
    const response = await Promise.race([
      fetch(`http://${address}:${port}/api/events?barrier=${encodeURIComponent(barrier)}`, { signal: abort.signal }),
      timed
    ]);
    if (response.status !== 200 || !response.body) fail("SSE stream did not open");
    if (response.headers.get("content-type")?.split(";")[0].trim().toLowerCase() !== "text/event-stream") {
      fail("SSE content type changed");
    }
    reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffered = "";
    let acknowledged = false;
    let responseBytes = 0;
    const consume = (async () => {
      while (true) {
        const { done, value } = await reader.read();
        responseBytes += value?.byteLength || 0;
        if (responseBytes > maxSSEBytes) fail("SSE response exceeded bounded client limit");
        const decoded = decodeSSE(buffered + decoder.decode(value || new Uint8Array(), { stream: !done }));
        buffered = decoded.remainder;
        for (const frame of decoded.frames) {
          if (frame.event !== "fixture") fail("SSE event name changed");
          const event = frame.data;
          if (event.runtimeMarker !== marker || event.barrier !== barrier) fail("SSE marker or barrier changed");
          if (event.sequence === 1 && !acknowledged) {
            acknowledged = true;
            const acknowledgement = await request({
              address, port, host, method: "POST", path: `/api/events/ack?barrier=${encodeURIComponent(barrier)}`,
              timeoutMs, signal: abort.signal
            });
            assertStatus(acknowledgement, 204, "SSE acknowledgement");
            continue;
          }
          if (event.sequence === 2 && acknowledged) return;
          fail("SSE event sequence changed");
        }
        if (done) fail("SSE stream ended before acknowledgement release");
      }
    })();
    await Promise.race([consume, timed]);
  } finally {
    abort.abort();
    await reader?.cancel().catch(() => {});
    clearDeadline();
  }
}

async function verifySPA({ address, port, host = address, marker, otherMarker, timeoutMs }) {
  const index = await request({ address, port, host, path: "/", timeoutMs });
  assertStatus(index, 200, "SPA index");
  const asset = /src="(\/assets\/[^"?]+\.js)"/.exec(index.body.toString("utf8"));
  if (!asset) fail("SPA has no built JavaScript asset");
  const bundle = await request({ address, port, host, path: asset[1], timeoutMs });
  assertStatus(bundle, 200, "SPA asset");
  const source = bundle.body.toString("utf8");
  if (!source.includes(marker) || (otherMarker && source.includes(otherMarker))) fail("SPA asset marker isolation changed");
}

async function verifyNamespaceDenials({ address, portA, portB, unusedPort, controllerPort, appAID, appBID, markerA, markerB, timeoutMs, verifySPAAssets = true }) {
  const apps = [
    { port: portA, marker: markerA, otherMarker: markerB },
    { port: portB, marker: markerB, otherMarker: markerA }
  ];
  for (const app of apps) {
    const version = await request({ address, port: app.port, host: address, path: "/api/version", timeoutMs });
    assertStatus(version, 200, "LAN version");
    assertMarker(version, app.marker, "LAN version");
    const explicitHost = await request({ address, port: app.port, host: `${address}:${app.port}`, path: "/api/version", timeoutMs });
    assertStatus(explicitHost, 200, "LAN explicit Host version");
    assertMarker(explicitHost, app.marker, "LAN explicit Host version");
    const wrongHost = await request({ address, port: app.port, host: "wrong.invalid", path: "/api/version", timeoutMs });
    assertStatus(wrongHost, 404, "wrong Host");
    const controllerPath = await request({ address, port: app.port, host: address, path: "/api/v1/system/lan-gateway-profile", timeoutMs });
    assertStatus(controllerPath, 404, "controller path");
    if (verifySPAAssets) await verifySPA({ address, port: app.port, host: address, marker: app.marker, otherMarker: app.otherMarker, timeoutMs });
    await verifyTransport({ address, port: app.port, host: address, marker: app.marker, timeoutMs });
    await verifyWebSocket({ address, port: app.port, marker: app.marker, timeoutMs });
    await verifySSE({ address, port: app.port, host: address, marker: app.marker, timeoutMs, barrier: `rig-lan-${app.marker}` });
  }
  for (const appID of [appAID, appBID]) {
    for (const port of [portA, portB]) {
      const local = await request({ address, port, host: `${appID}.rig.localhost`, path: "/api/version", timeoutMs });
      assertStatus(local, 404, "controller-local Host on LAN");
    }
  }
  for (const [host, path] of [[address, "/"], [`${address}:${unusedPort}`, "/"]]) {
    const unused = await request({ address, port: unusedPort, host, path, timeoutMs });
    assertStatus(unused, 404, "unassigned LAN port");
  }
  for (const appID of [appAID, appBID]) {
    await assertRejected(request({ address, port: 8080, host: `${appID}.rig.localhost`, path: "/api/version", timeoutMs }), "controller loopback route");
  }
  await assertRejected(request({ address, port: controllerPort, host: address, path: "/api/v1/system/lan-gateway-profile", timeoutMs }), "controller admin route");
}

async function assertRejected(promise, label) {
  try {
    await promise;
  } catch {
    return;
  }
  fail(`${label} was reachable`);
}

async function runLanTransportClient(options) {
  await verifyNamespaceDenials(options);
}

module.exports = {
  request,
  runLanTransportClient,
  verifyNamespaceDenials,
  verifySSE,
  verifySPA,
  verifyTransport,
  verifyWebSocket
};

if (process.argv[1] === "--rig-lan-transport-client") {
  const [address, portA, portB, unusedPort, controllerPort, appAID, appBID, markerA, markerB] = process.argv.slice(2);
  runLanTransportClient({ address, portA, portB, unusedPort, controllerPort, appAID, appBID, markerA, markerB })
    .catch((error) => {
      console.error(error.message);
      process.exitCode = 1;
    });
}
