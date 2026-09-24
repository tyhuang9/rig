import express from "express";
import { quotedNotesTable, runtimeMetadata } from "./config.js";
import { probeHttpsDependency } from "./https-probe.js";

const readinessTimeoutMs = 2_000;

async function bounded(operation, timeoutMs = readinessTimeoutMs) {
  let timer;
  try {
    return await Promise.race([
      operation,
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("database timeout")), timeoutMs); })
    ]);
  } finally {
    clearTimeout(timer);
  }
}

function databaseFailure(response, readiness = false) {
  response.status(503).json(readiness ? { status: "not_ready" } : { error: "database_unavailable" });
}

function testMode(env) {
  return env.TEST_FIXTURE_MODE === "1";
}

export function createApp({ database, dependencyProbe = probeHttpsDependency, env = process.env, log = console }) {
  const app = express();
  const table = quotedNotesTable(env);
  const metadata = runtimeMetadata(env);

  app.disable("x-powered-by");
  app.use(express.json({ limit: "4kb" }));

  app.get("/healthz", (_request, response) => response.json({ status: "ok" }));

  app.get("/readyz", async (_request, response) => {
    try {
      await bounded(database.query("SELECT 1"));
      response.json({ status: "ready" });
    } catch {
      databaseFailure(response, true);
    }
  });

  app.get("/api/version", (_request, response) => response.json(metadata));

  app.get("/api/notes", async (_request, response) => {
    try {
      const result = await bounded(database.query(
        `SELECT id, body, created_at AS "createdAt" FROM ${table} ORDER BY id ASC LIMIT $1`,
        [100]
      ));
      response.json({ notes: result.rows });
    } catch {
      databaseFailure(response);
    }
  });

  app.post("/api/notes", async (request, response) => {
    const body = typeof request.body?.body === "string" ? request.body.body.trim() : "";
    if (!body || body.length > 500) {
      response.status(400).json({ error: "note_body_invalid" });
      return;
    }
    try {
      const result = await bounded(database.query(
        `INSERT INTO ${table} (body) VALUES ($1) RETURNING id, body, created_at AS "createdAt"`,
        [body]
      ));
      response.status(201).json({ note: result.rows[0] });
    } catch {
      databaseFailure(response);
    }
  });

  app.get("/api/events", (_request, response) => {
    response.set({ "cache-control": "no-store", "content-type": "text/event-stream" });
    response.write(`event: fixture\ndata: ${JSON.stringify(metadata)}\n\n`);
    response.end();
  });

  if (testMode(env)) {
    app.get("/api/test/runtime-presence", (_request, response) => {
      response.json({
        databaseUrlConfigured: Boolean(env[env.DATABASE_URL_ENV || "DATABASE_URL"]),
        runtimeMarkerConfigured: Boolean(env.API_RUNTIME_MARKER),
        sentinelConfigured: Boolean(env.TEST_SENTINEL_SECRET)
      });
    });
    app.get("/api/test/delay", async (request, response) => {
      const requested = Number.parseInt(request.query.ms, 10);
      const delay = Number.isFinite(requested) ? Math.min(Math.max(requested, 0), 1_000) : 0;
      await new Promise((resolve) => setTimeout(resolve, delay));
      response.json({ delayedMs: delay });
    });
    app.post("/api/test/log", (_request, response) => {
      log.info("hosting-notes fixture synthetic log event");
      response.status(204).end();
    });
    app.get("/api/test/dependency", async (_request, response) => {
      try {
        await dependencyProbe(env);
        response.json({ status: "ok", dependency: "reachable" });
      } catch {
        response.status(503).json({ error: "dependency_unavailable" });
      }
    });
  }

  return app;
}
