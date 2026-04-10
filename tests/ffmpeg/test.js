const { execSync } = require('child_process');
const fs = require('fs');

const OUTPUT_FILE = 'test_output.mp4';

function logSection(title) {
  console.log(`\n=== ${title} ===`);
}

function success(msg) {
  console.log(`✅ ${msg}`);
}

function fail(msg) {
  console.error(`❌ ${msg}`);
  process.exit(1);
}

function info(msg) {
  console.log(`ℹ️  ${msg}`);
}

function cleanup() {
  if (fs.existsSync(OUTPUT_FILE)) {
    fs.unlinkSync(OUTPUT_FILE);
    info('Old output file removed');
  }
}

try {
  console.log('🚀 Minibox FFmpeg Smoke Test');
  console.log('------------------------------------');

  // 1️⃣ Check FFmpeg installation
  logSection('FFmpeg Check');

  const versionInfo = execSync('ffmpeg -version', { encoding: 'utf8' });
  const versionLine = versionInfo.split('\n')[0];

  success(`FFmpeg detected → ${versionLine}`);

  // 2️⃣ Cleanup previous file
  logSection('Cleanup');
  cleanup();

  // 3️⃣ Generate test video
  logSection('Video Generation');

  info('Generating 2-second test video...');

  const start = Date.now();

  execSync(
    'ffmpeg -f lavfi -i testsrc=duration=2:size=320x240:rate=10 -c:v libx264 -y test_output.mp4',
    { stdio: 'ignore' }
  );

  const duration = Date.now() - start;

  // 4️⃣ Validate output
  logSection('Validation');

  if (!fs.existsSync(OUTPUT_FILE)) {
    fail('Output file was not created');
  }

  const stats = fs.statSync(OUTPUT_FILE);

  if (stats.size === 0) {
    fail('Output file is empty');
  }

  success(`Video generated successfully`);
  info(`File: ${OUTPUT_FILE}`);
  info(`Size: ${stats.size} bytes`);
  info(`Time: ${duration} ms`);

  console.log('\n🎉 FFmpeg test passed successfully!\n');

} catch (err) {
  fail(`FFmpeg execution failed → ${err.message}`);
}
