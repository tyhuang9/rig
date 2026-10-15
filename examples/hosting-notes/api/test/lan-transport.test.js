import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";
import { createLanTransportServer } from "../fixtures/lan-transport-server.mjs";

const require = createRequire(import.meta.url);
const client = require("../fixtures/lan-transport-client.cjs");

async function start(options = {}) {
  const fixture = createLanTransportServer(options);
  await new Promise((resolve) => fixture.server.listen(0, "127.0.0.1", resolve));
  const { port } = fixture.server.address();
  return { fixture, port };
}

async function close(...servers) {
  await Promise.all(servers.map(({ fixture }) => fixture.close()));
}

test("shared LAN transport fixture preserves exact HTTP payloads, WebSockets, split SSE, and SPA assets", async () => {
  const server = await start({ marker: "fixture-A", spaAssets: true });
  try {
    await client.verifyTransport({ address: "127.0.0.1", port: server.port, marker: "fixture-A" });
    await client.verifyWebSocket({ address: "127.0.0.1", port: server.port, marker: "fixture-A" });
    await client.verifySSE({ address: "127.0.0.1", port: server.port, marker: "fixture-A", barrier: "split-frame" });
    await client.verifySPA({ address: "127.0.0.1", port: server.port, marker: "fixture-A", otherMarker: "fixture-B" });
    assert.equal(server.fixture.activeBarriers(), 0);
  } finally {
    await close(server);
  }
});

test("shared client rejects a wrong runtime marker or upload digest", async () => {
  const server = await start({ marker: "fixture-A" });
  try {
    await assert.rejects(
      client.verifyTransport({ address: "127.0.0.1", port: server.port, marker: "fixture-B" }),
      /marker mismatch/
    );
    await assert.rejects(
      client.verifyTransport({
        address: "127.0.0.1", port: server.port, marker: "fixture-A", expectedUploadDigest: "0".repeat(64)
      }),
      /upload forwarding changed/
    );
  } finally {
    await close(server);
  }
});

test("shared client rejects failed WebSocket upgrades, malformed messages, and wrong echoes without leaks", async () => {
  const rejected = await start({ marker: "fixture-A", webSocketMode: "reject_upgrade" });
  const malformed = await start({ marker: "fixture-A", webSocketMode: "malformed" });
  const wrongEcho = await start({ marker: "fixture-A", webSocketMode: "wrong_echo" });
  try {
    await assert.rejects(
      client.verifyWebSocket({ address: "127.0.0.1", port: rejected.port, marker: "fixture-A", timeoutMs: 500 }),
      /WebSocket/
    );
    await assert.rejects(
      client.verifyWebSocket({ address: "127.0.0.1", port: malformed.port, marker: "fixture-A", timeoutMs: 500 }),
      /WebSocket message was not JSON/
    );
    await assert.rejects(
      client.verifyWebSocket({ address: "127.0.0.1", port: wrongEcho.port, marker: "fixture-A", timeoutMs: 500 }),
      /WebSocket echo or clean close changed/
    );
    await malformed.fixture.waitForIdle({ timeoutMs: 1_000 });
    await wrongEcho.fixture.waitForIdle({ timeoutMs: 1_000 });
    assert.equal(malformed.fixture.activeWebSockets(), 0);
    assert.equal(wrongEcho.fixture.activeWebSockets(), 0);
  } finally {
    await close(rejected, malformed, wrongEcho);
  }
});

test("buffered SSE headers fail the bounded client deadline and clear their server barrier", async () => {
  const server = await start({ marker: "fixture-A", sseMode: "buffer" });
  try {
    await assert.rejects(
      client.verifySSE({ address: "127.0.0.1", port: server.port, marker: "fixture-A", barrier: "buffered", timeoutMs: 250 }),
      /SSE deadline exceeded/
    );
    await server.fixture.waitForIdle({ timeoutMs: 1_000 });
    assert.equal(server.fixture.activeBarriers(), 0);
  } finally {
    await close(server);
  }
});

test("bounded clients reject oversized HTTP and SSE responses and malformed multipart bodies", async () => {
  const response = await start({ marker: "fixture-A", versionPaddingBytes: 2_048 });
  const stream = await start({ marker: "fixture-A", sseMode: "oversized" });
  try {
    await assert.rejects(
      client.request({
        address: "127.0.0.1", port: response.port, host: "127.0.0.1", path: "/api/version", maxBodyBytes: 128
      }),
      /response exceeded bounded client limit/
    );
    const malformed = await client.request({
      address: "127.0.0.1", port: response.port, host: "127.0.0.1", method: "POST", path: "/api/upload",
      headers: { "content-type": "multipart/form-data; boundary=missing", "content-length": "3" }, body: Buffer.from("bad")
    });
    assert.equal(malformed.status, 400);
    await assert.rejects(
      client.verifySSE({
        address: "127.0.0.1", port: stream.port, marker: "fixture-A", barrier: "oversized", timeoutMs: 1_000, maxSSEBytes: 1_024
      }),
      /SSE response exceeded bounded client limit/
    );
    await stream.fixture.waitForIdle({ timeoutMs: 1_000 });
    assert.equal(stream.fixture.activeBarriers(), 0);
  } finally {
    await close(response, stream);
  }
});

test("SSE rejects a changed media type or event name and releases the stream", async () => {
  for (const [sseMode, expected] of [["wrong_type", /SSE content type changed/], ["wrong_event", /SSE event name changed/]]) {
    const server = await start({ marker: "fixture-A", sseMode });
    try {
      await assert.rejects(
        client.verifySSE({ address: "127.0.0.1", port: server.port, marker: "fixture-A", barrier: sseMode }),
        expected
      );
      await server.fixture.waitForIdle();
      assert.equal(server.fixture.activeBarriers(), 0);
    } finally {
      await close(server);
    }
  }
});

test("aborting an acknowledged-barrier candidate stream releases server state", async () => {
  const server = await start({ marker: "fixture-A" });
  const abort = new AbortController();
  const deadline = setTimeout(() => abort.abort(), 1_000);
  try {
    const response = await fetch(`http://127.0.0.1:${server.port}/api/events?barrier=disconnect`, { signal: abort.signal });
    assert.equal(response.status, 200);
    assert.ok(response.body);
    await response.body.getReader().read();
    abort.abort();
    await server.fixture.waitForIdle({ timeoutMs: 1_000 });
    assert.equal(server.fixture.activeBarriers(), 0);
  } finally {
    clearTimeout(deadline);
    abort.abort();
    await close(server);
  }
});
