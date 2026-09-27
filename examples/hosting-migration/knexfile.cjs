const fs = require("node:fs");
const path = require("node:path");
const tls = require("node:tls");

const connectionString = process.env.DATABASE_URL;
if (!connectionString) {
  throw new Error("DATABASE_URL is required for the approved migration");
}

const certificatePath = path.join(__dirname, "test-ca.crt");
const certificateAuthority = fs.readFileSync(certificatePath, "utf8");
const hostname = new URL(connectionString).hostname;

module.exports = {
  client: "pg",
  connection: {
    connectionString,
    ssl: {
      rejectUnauthorized: true,
      ca: certificateAuthority,
      checkServerIdentity: (_reportedHost, certificate) => tls.checkServerIdentity(hostname, certificate)
    }
  },
  migrations: {
    directory: path.join(__dirname, "migrations"),
    tableName: "rig_migration_history",
    loadExtensions: [".cjs"]
  }
};
