exports.up = async function up(knex) {
  await knex.schema.createTable("rig_migration_ledger", (table) => {
    table.bigIncrements("run_id").primary();
    table.text("migration_key").notNullable();
    table.timestamp("applied_at", { useTz: true }).notNullable().defaultTo(knex.fn.now());
  });
  await knex.schema.createTable("rig_migration_counter", (table) => {
    table.text("counter_key").primary();
    table.integer("value").notNullable();
  });
  await knex("rig_migration_ledger").insert({ migration_key: "202609270001_add_counter" });
  await knex("rig_migration_counter").insert({ counter_key: "approved_migration", value: 1 });
};

exports.down = async function down() {
  throw new Error("application rollback must not execute a down migration");
};
