import tls from "node:tls";
import pg from "pg";
import { tlsCertificateAuthority, verifiedDatabaseUrl } from "./config.js";

const { Pool } = pg;

export function createDatabase(env, log = console) {
  const ca = tlsCertificateAuthority(env);
  const connectionString = verifiedDatabaseUrl(env);
  const urlHost = new URL(connectionString).hostname;
  const identityHost = urlHost.startsWith("[") && urlHost.endsWith("]") ? urlHost.slice(1, -1) : urlHost;
  const pool = new Pool({
    connectionString,
    connectionTimeoutMillis: 2_000,
    query_timeout: 2_000,
    max: 4,
    // pg omits TLS servername for numeric IPv4 hosts. Verify the configured
    // URL host with Node's standard identity checker while retaining chain
    // validation; this callback does not set SNI.
    ssl: {
      rejectUnauthorized: true,
      ...(ca ? { ca } : {}),
      checkServerIdentity: (_reportedHost, certificate) => tls.checkServerIdentity(identityHost, certificate)
    }
  });
  // pg reports dropped idle clients through the pool's error event. Keep the
  // API alive so its bounded request and readiness paths can report 503 while
  // the application-owned database is unavailable. Never log connection data.
  pool.on("error", () => log.warn("hosting-notes idle database connection lost"));
  return pool;
}
