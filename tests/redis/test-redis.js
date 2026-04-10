const { createClient } = require('redis');

/**
 * Redis Connection Tester
 * 
 * This script attempts to connect to a Redis server running on localhost:6379
 * (expected to be mapped from a container) and performs basic SET/GET operations.
 */
async function testRedis() {
    const redisUrl = process.env.REDIS_URL || 'redis://localhost:6379';
    console.log(`Connecting to Redis at: ${redisUrl}...`);

    const client = createClient({
        url: redisUrl
    });

    client.on('error', (err) => {
        console.error('\x1b[31m%s\x1b[0m', 'Redis Client Error:', err.message);
    });

    try {
        await client.connect();
        console.log('\x1b[32m%s\x1b[0m', '✅ Successfully connected to Redis!');

        // 1. SET
        await client.set('minibox_test', 'working');
        console.log('✅ SET operation successful');

        // 2. GET
        const value = await client.get('minibox_test');
        if (value === 'working') {
            console.log('\x1b[32m%s\x1b[0m', `✅ GET operation successful (Value: ${value})`);
        } else {
            console.warn('\x1b[33m%s\x1b[0m', `⚠️ GET returned unexpected value: ${value}`);
        }

        // 3. CLEANUP
        await client.del('minibox_test');
        
        await client.disconnect();
        console.log('Test completed successfully and connection closed.');
    } catch (err) {
        console.error('\x1b[31m%s\x1b[0m', '❌ Connection failed!');
        console.error(err);
        process.exit(1);
    }
}

testRedis();
