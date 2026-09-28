const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const tls = require("node:tls");
const { Pool } = require("pg");

const connectionString = process.env.DATABASE_URL;
if (!connectionString) {
  throw new Error("DATABASE_URL is required");
}

const hostname = new URL(connectionString).hostname;
const pool = new Pool({
  connectionString,
  connectionTimeoutMillis: 2_000,
  query_timeout: 2_000,
  max: 2,
  ssl: {
    rejectUnauthorized: true,
    ca: fs.readFileSync(path.join(__dirname, "..", "test-ca.crt"), "utf8"),
    checkServerIdentity: (_reportedHost, certificate) => tls.checkServerIdentity(hostname, certificate)
  }
});

async function counter() {
  const result = await pool.query("SELECT value FROM rig_migration_counter WHERE counter_key='approved_migration'");
  const value = Number(result.rows[0]?.value);
  if (result.rows.length !== 1 || !Number.isInteger(value) || value < 1) {
    throw new Error("approved migration counter is unavailable");
  }
  return value;
}

const server = http.createServer(async (request, response) => {
  if (request.url !== "/readyz" && request.url !== "/counter") {
    response.writeHead(404, { "content-type": "application/json" });
    response.end('{"error":"not_found"}');
    return;
  }
  try {
    const value = await counter();
    response.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
    response.end(JSON.stringify(request.url === "/readyz" ? { status: "ready" } : { value }));
  } catch {
    response.writeHead(503, { "content-type": "application/json" });
    response.end('{"status":"not_ready"}');
  }
});

server.listen(Number.parseInt(process.env.PORT || "3000", 10), "0.0.0.0");

async function close() {
  server.close(() => void pool.end());
}

process.once("SIGINT", close);
process.once("SIGTERM", close);
