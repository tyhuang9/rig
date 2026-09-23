import pg from "pg";
import { tlsCertificateAuthority, verifiedDatabaseUrl } from "./config.js";

const { Pool } = pg;

export function createDatabase(env) {
  const ca = tlsCertificateAuthority(env);
  return new Pool({
    connectionString: verifiedDatabaseUrl(env),
    connectionTimeoutMillis: 2_000,
    query_timeout: 2_000,
    max: 4,
    // Node validates both the CA chain and the database hostname. A test CA,
    // when supplied, augments that check; it never disables it.
    ssl: { rejectUnauthorized: true, ...(ca ? { ca } : {}) }
  });
}
