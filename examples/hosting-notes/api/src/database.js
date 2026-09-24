import tls from "node:tls";
import pg from "pg";
import { tlsCertificateAuthority, verifiedDatabaseUrl } from "./config.js";

const { Pool } = pg;

export function createDatabase(env) {
  const ca = tlsCertificateAuthority(env);
  const connectionString = verifiedDatabaseUrl(env);
  const urlHost = new URL(connectionString).hostname;
  const identityHost = urlHost.startsWith("[") && urlHost.endsWith("]") ? urlHost.slice(1, -1) : urlHost;
  return new Pool({
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
}
