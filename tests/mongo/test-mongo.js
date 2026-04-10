/**
 * Smoke test: connect, insert one document, read it back.
 *
 * Default URI matches docs/CLI.md `db-test/mongo` example:
 *   MONGO_INITDB_ROOT_USERNAME=root MONGO_INITDB_ROOT_PASSWORD=secret
 *
 * If `127.0.0.1:27017` fails (host port mapping / iptables), point at the
 * container IP instead, e.g.:
 *   MONGODB_URI='mongodb://root:secret@172.19.0.4:27017' node test-mongo.js
 */
const { MongoClient } = require('mongodb');

const uri =
  process.env.MONGODB_URI ||
  'mongodb://root:chaitu@127.0.0.1:27017/?authSource=admin';

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
  console.log('Connecting to MongoDB at:', redactUri(uri), '...');
  const client = new MongoClient(uri);
  try {
    await client.connect();
    console.log('✅ Successfully connected to MongoDB!');

    const db = client.db('minibox_smoke');
    const col = db.collection('kv');

    await col.deleteMany({ _key: 'minibox-test' });
    const ins = await col.insertOne({
      _key: 'minibox-test',
      value: 'working',
      at: new Date(),
    });
    console.log('✅ Insert OK (insertedId:', ins.insertedId.toString() + ')');

    const doc = await col.findOne({ _key: 'minibox-test' });
    if (!doc || doc.value !== 'working') {
      throw new Error(`findOne mismatch: ${JSON.stringify(doc)}`);
    }
    console.log('✅ Find OK (value:', doc.value + ')');

    await col.deleteMany({ _key: 'minibox-test' });
    console.log('Test completed successfully and connection closed.');
  } finally {
    await client.close();
  }
}

main().catch((err) => {
  console.error('MongoDB client error:', err.message);
  process.exit(1);
});
