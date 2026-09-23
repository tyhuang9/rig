import { createDatabase } from "./database.js";
import { fixtureSchema, quotedNotesTable } from "./config.js";

const database = createDatabase(process.env);
const schema = fixtureSchema(process.env);

try {
  // The schema identifier is validated by fixtureSchema. Values in the API
  // itself remain parameters; PostgreSQL does not support bound identifiers.
  await database.query(`CREATE SCHEMA IF NOT EXISTS "${schema}"`);
  await database.query(`CREATE TABLE IF NOT EXISTS ${quotedNotesTable(process.env)} (
    id BIGSERIAL PRIMARY KEY,
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
  )`);
  console.info("hosting-notes fixture schema is ready");
} finally {
  await database.end();
}
