import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { createServer } from "node:http";
import test from "node:test";
import { createApp } from "../src/app.js";
import { createDatabase } from "../src/database.js";
import { probeHttpsDependency } from "../src/https-probe.js";

async function serve({ database, dependencyProbe, env = {} }) {
  const server = createServer(createApp({ database, dependencyProbe, env, log: { info() {} } }));
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  return {
    request(path, init) { return fetch(`http://127.0.0.1:${port}${path}`, init); },
    close() { return new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve())); }
  };
}

function database({ fail = false } = {}) {
  const calls = [];
  const notes = [];
  return {
    calls,
    async query(sql, values = []) {
      calls.push({ sql, values });
      if (fail) throw new Error("postgresql://user:do-not-disclose@db.invalid failed");
      if (sql === "SELECT 1") return { rows: [{ "?column?": 1 }] };
      if (sql.startsWith("INSERT")) {
        const note = { id: notes.length + 1, body: values[0], createdAt: "2026-09-23T00:00:00.000Z" };
        notes.push(note);
        return { rows: [note] };
      }
      if (sql.startsWith("SELECT id, body")) return { rows: notes };
      throw new Error("unexpected SQL");
    }
  };
}

test("health remains live without querying the database", async () => {
  const fake = database();
  const fixture = await serve({ database: fake });
  try {
    const response = await fixture.request("/healthz");
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { status: "ok" });
    assert.equal(fake.calls.length, 0);
  } finally {
    await fixture.close();
  }
});

test("readiness reports a bounded safe failure without connection details", async () => {
  const fixture = await serve({ database: database({ fail: true }) });
  try {
    const response = await fixture.request("/readyz");
    assert.equal(response.status, 503);
    const body = await response.text();
    assert.equal(body, '{"status":"not_ready"}');
    assert.ok(!body.includes("do-not-disclose"));
  } finally {
    await fixture.close();
  }
});

test("notes use bound values and round-trip through the configured schema", async () => {
  const fake = database();
  const fixture = await serve({ database: fake, env: { FIXTURE_SCHEMA: "isolated_notes" } });
  try {
    const create = await fixture.request("/api/notes", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ body: "marker '); DROP TABLE notes; --" })
    });
    assert.equal(create.status, 201);
    assert.equal((await create.json()).note.body, "marker '); DROP TABLE notes; --");
    assert.match(fake.calls[0].sql, /"isolated_notes"\."notes"/);
    assert.match(fake.calls[0].sql, /VALUES \(\$1\)/);
    assert.deepEqual(fake.calls[0].values, ["marker '); DROP TABLE notes; --"]);

    const list = await fixture.request("/api/notes");
    assert.equal(list.status, 200);
    assert.deepEqual((await list.json()).notes.map((note) => note.body), ["marker '); DROP TABLE notes; --"]);
  } finally {
    await fixture.close();
  }
});

test("invalid note input does not reach the database", async () => {
  const fake = database();
  const fixture = await serve({ database: fake });
  try {
    const response = await fixture.request("/api/notes", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ body: "   " })
    });
    assert.equal(response.status, 400);
    assert.equal(fake.calls.length, 0);
  } finally {
    await fixture.close();
  }
});

test("version and presence probes expose markers but never secret values", async () => {
  const secret = "sentinel-must-not-escape";
  const fixture = await serve({ database: database(), env: {
    TEST_FIXTURE_MODE: "1",
    DATABASE_URL: "postgresql://fixture:password@postgres.fixture.test/notes",
    API_RUNTIME_MARKER: "runtime-A",
    TEST_SENTINEL_SECRET: secret,
    FIXTURE_SOURCE_VERSION: "source-A"
  } });
  try {
    const version = await fixture.request("/api/version");
    assert.deepEqual(await version.json(), { sourceVersion: "source-A", runtimeMarker: "runtime-A" });
    const presence = await fixture.request("/api/test/runtime-presence");
    const text = await presence.text();
    assert.equal(text, '{"databaseUrlConfigured":true,"runtimeMarkerConfigured":true,"sentinelConfigured":true}');
    assert.ok(!text.includes(secret));
  } finally {
    await fixture.close();
  }
});

test("database URLs cannot disable certificate verification", () => {
  for (const tlsMode of ["disable", "no-verify"]) {
    assert.throws(
      () => createDatabase({ DATABASE_URL: `postgresql://fixture:password@postgres.fixture.test/notes?sslmode=${tlsMode}` }),
      /unsupported TLS mode/
    );
  }
});

test("HTTPS probe sends scoped credentials through verified, bounded TLS options", async () => {
  let options;
  let timeout;
  const timer = Symbol("deadline");
  const clearedTimers = [];
  const request = (receivedOptions, callback) => {
    options = receivedOptions;
    const handle = new EventEmitter();
    handle.setTimeout = (milliseconds) => { timeout = milliseconds; };
    handle.destroy = (error) => queueMicrotask(() => handle.emit("error", error));
    handle.end = () => queueMicrotask(() => {
      const response = new EventEmitter();
      response.statusCode = 200;
      callback(response);
      response.emit("data", Buffer.from('{"status":"ok"}'));
      response.emit("end");
    });
    return handle;
  };
  const certificate = "-----BEGIN CERTIFICATE-----\nfixture CA\n-----END CERTIFICATE-----";
  await probeHttpsDependency({
    HTTPS_DEPENDENCY_URL_ENV: "CUSTOM_SERVICE_URL",
    CUSTOM_SERVICE_URL: "https://https.fixture.test:55443/probe?run=fixture",
    HTTPS_DEPENDENCY_TOKEN_ENV: "CUSTOM_SERVICE_TOKEN",
    CUSTOM_SERVICE_TOKEN: "token-must-stay-server-only",
    HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64: Buffer.from(certificate).toString("base64")
  }, {
    request,
    setTimer: (_callback, milliseconds) => {
      assert.equal(milliseconds, 2_000);
      return timer;
    },
    clearTimer: (receivedTimer) => clearedTimers.push(receivedTimer)
  });
  assert.equal(timeout, 2_000);
  assert.equal(options.hostname, "https.fixture.test");
  assert.equal(options.port, "55443");
  assert.equal(options.path, "/probe?run=fixture");
  assert.equal(options.servername, "https.fixture.test");
  assert.equal(options.headers.authorization, "Bearer token-must-stay-server-only");
  assert.equal(options.rejectUnauthorized, true);
  assert.equal(options.ca, certificate);
  assert.deepEqual(clearedTimers, [timer]);
});

test("HTTPS probe enforces an absolute deadline while a response trickles", async () => {
  let deadline;
  let destroyed = 0;
  const timer = Symbol("deadline");
  const clearedTimers = [];
  const request = (_options, callback) => {
    const handle = new EventEmitter();
    handle.setTimeout = () => {};
    handle.destroy = () => {
      destroyed += 1;
      queueMicrotask(() => handle.emit("error", new Error("request destroyed")));
    };
    handle.end = () => queueMicrotask(() => {
      const response = new EventEmitter();
      response.statusCode = 200;
      callback(response);
      response.emit("data", Buffer.from("first byte"));
      response.emit("data", Buffer.from("second byte"));
      // No end event: a peer can continue trickling bytes indefinitely.
    });
    return handle;
  };
  const probe = probeHttpsDependency({
    HTTPS_DEPENDENCY_URL: "https://https.fixture.test:55443/",
    HTTPS_DEPENDENCY_TOKEN: "fixture-token"
  }, {
    request,
    setTimer: (callback, milliseconds) => {
      assert.equal(milliseconds, 2_000);
      deadline = callback;
      return timer;
    },
    clearTimer: (receivedTimer) => clearedTimers.push(receivedTimer)
  });
  await new Promise((resolve) => queueMicrotask(resolve));
  deadline();
  await assert.rejects(probe, /exceeded its deadline/);
  assert.equal(destroyed, 1);
  assert.deepEqual(clearedTimers, [timer]);
});

test("dependency endpoint returns no configured secret in success or failure responses", async () => {
  const secret = "dependency-token-must-not-escape";
  const environment = {
    TEST_FIXTURE_MODE: "1",
    HTTPS_DEPENDENCY_URL: "https://https.fixture.test:55443/",
    HTTPS_DEPENDENCY_TOKEN: secret,
    HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64: Buffer.from("-----BEGIN CERTIFICATE-----\nfixture CA\n-----END CERTIFICATE-----").toString("base64")
  };
  const success = await serve({
    database: database(),
    env: environment,
    dependencyProbe: async (receivedEnvironment) => {
      assert.equal(receivedEnvironment.HTTPS_DEPENDENCY_TOKEN, secret);
    }
  });
  try {
    const response = await success.request("/api/test/dependency");
    const body = await response.text();
    assert.equal(body, '{"status":"ok","dependency":"reachable"}');
    assert.ok(!body.includes(secret));
  } finally {
    await success.close();
  }

  const failure = await serve({
    database: database(),
    env: environment,
    dependencyProbe: async () => { throw new Error(secret); }
  });
  try {
    const response = await failure.request("/api/test/dependency");
    const body = await response.text();
    assert.equal(response.status, 503);
    assert.equal(body, '{"error":"dependency_unavailable"}');
    assert.ok(!body.includes(secret));
  } finally {
    await failure.close();
  }
});
