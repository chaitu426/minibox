/**
 * Smoke test: connect, insert one row, read it back.
 *
 * Default URI:
 *   postgres://postgres:minibox@127.0.0.1:5432/app
 *
 * If 127.0.0.1 fails, use container IP:
 *   PG_URI='postgres://postgres:minibox@172.x.x.x:5432/app' node test-postgres.js
 */

const { Client } = require('pg');

const uri =
  process.env.PG_URI ||
  'postgres://postgres:minibox@127.0.0.1:5432/app';

function redactUri(u) {
  try {
    const x = new URL(u);
    if (x.password) x.password = '****';
    return x.toString();
  } catch {
    return u.replace(/:[^@/]+@/, ':****@');
  }
}

async function main() {
  console.log('Connecting to PostgreSQL at:', redactUri(uri), '...');

  const client = new Client({ connectionString: uri });

  try {
    await client.connect();
    console.log('✅ Successfully connected to PostgreSQL!');

    // Create table
    await client.query(`
      CREATE TABLE IF NOT EXISTS kv (
        _key TEXT PRIMARY KEY,
        value TEXT,
        at TIMESTAMP
      )
    `);
    console.log('✅ Table ready');

    // Clean old
    await client.query(`DELETE FROM kv WHERE _key = $1`, ['minibox-test']);

    // Insert
    await client.query(
      `INSERT INTO kv (_key, value, at) VALUES ($1, $2, NOW())`,
      ['minibox-test', 'working']
    );
    console.log('✅ Insert OK');

    // Read
    const res = await client.query(
      `SELECT value FROM kv WHERE _key = $1`,
      ['minibox-test']
    );

    if (!res.rows.length || res.rows[0].value !== 'working') {
      throw new Error(`find mismatch: ${JSON.stringify(res.rows)}`);
    }

    console.log('✅ Find OK (value:', res.rows[0].value + ')');

    // Cleanup
    await client.query(`DELETE FROM kv WHERE _key = $1`, ['minibox-test']);

    console.log('Test completed successfully and connection closed.');
  } finally {
    await client.end();
  }
}

main().catch((err) => {
  console.error('PostgreSQL client error:', err.message);
  process.exit(1);
});