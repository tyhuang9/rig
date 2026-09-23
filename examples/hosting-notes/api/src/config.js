const schemaNamePattern = /^[a-z_][a-z0-9_]{0,62}$/;
const environmentKeyPattern = /^[A-Z][A-Z0-9_]*$/;
const permittedTlsModes = new Set(["require", "verify-ca", "verify-full"]);
const tlsParameters = new Set(["ssl", "sslmode", "sslcert", "sslkey", "sslrootcert"]);

export function fixtureSchema(env) {
  const schema = env.FIXTURE_SCHEMA || "rig_fixture_notes";
  if (!schemaNamePattern.test(schema)) {
    throw new Error("FIXTURE_SCHEMA must be a lowercase PostgreSQL identifier");
  }
  return schema;
}

export function quotedNotesTable(env) {
  return `"${fixtureSchema(env)}"."notes"`;
}

export function databaseUrl(env) {
  return configuredValue(env, "DATABASE_URL_ENV", "DATABASE_URL", "database URL");
}

export function configuredValue(env, selectorKey, defaultKey, description) {
  const key = env[selectorKey] || defaultKey;
  if (!environmentKeyPattern.test(key)) {
    throw new Error(`${selectorKey} must name an environment variable`);
  }
  const value = env[key];
  if (!value) {
    throw new Error(`The configured ${description} is missing`);
  }
  return value;
}

// pg parses TLS parameters embedded in a connection URL. Keep the driver
// configuration authoritative: reject modes that weaken verification and
// remove all remaining TLS URL parameters before creating the Pool.
export function verifiedDatabaseUrl(env) {
  let parsed;
  try {
    parsed = new URL(databaseUrl(env));
  } catch {
    throw new Error("The configured database URL is invalid");
  }
  if (parsed.protocol !== "postgres:" && parsed.protocol !== "postgresql:") {
    throw new Error("The configured database URL must use PostgreSQL");
  }
  for (const [key, value] of [...parsed.searchParams]) {
    const normalizedKey = key.toLowerCase();
    if (!tlsParameters.has(normalizedKey)) continue;
    if (normalizedKey === "sslmode" && !permittedTlsModes.has(value.toLowerCase())) {
      throw new Error("The configured database URL requests an unsupported TLS mode");
    }
    if (normalizedKey === "ssl" && value.toLowerCase() === "false") {
      throw new Error("The configured database URL requests an unsupported TLS mode");
    }
    parsed.searchParams.delete(key);
  }
  return parsed.toString();
}

export function tlsCertificateAuthority(env, key = "DATABASE_TLS_CA_PEM_BASE64") {
  const encoded = env[key];
  if (!encoded) return undefined;
  const certificate = Buffer.from(encoded, "base64").toString("utf8");
  if (!certificate.includes("BEGIN CERTIFICATE")) {
    throw new Error(`${key} is not a PEM certificate`);
  }
  return certificate;
}

export function runtimeMetadata(env) {
  return {
    sourceVersion: env.FIXTURE_SOURCE_VERSION || "hosting-notes-v1",
    runtimeMarker: env.API_RUNTIME_MARKER || "unset"
  };
}
