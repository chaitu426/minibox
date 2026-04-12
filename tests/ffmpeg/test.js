const { spawn } = require('child_process');
const fs = require('fs');

const INPUT_FILE = 'input.avi';              // 👈 your AVI file
const OUTPUT_FILE = 'compressed_output.mp4';

// 🎯 Tune this
const CRF = 20;         // 18 = near lossless, 20 = best balance
const PRESET = 'slow';  // slower = better compression

// ------------------ UI ------------------

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

function formatGB(bytes) {
  return (bytes / (1024 ** 3)).toFixed(2) + ' GB';
}

function cleanup() {
  if (fs.existsSync(OUTPUT_FILE)) {
    fs.unlinkSync(OUTPUT_FILE);
    info('Old output removed');
  }
}

// ------------------ Main ------------------

function compressVideo() {
  console.log('🚀 AVI → MP4 High Compression');
  console.log('------------------------------------');

  // 1️⃣ Input check
  logSection('Input Check');

  if (!fs.existsSync(INPUT_FILE)) {
    fail('AVI file not found');
  }

  const inputStats = fs.statSync(INPUT_FILE);
  info(`Input: ${INPUT_FILE}`);
  info(`Size: ${formatGB(inputStats.size)}`);

  // 2️⃣ Cleanup
  logSection('Cleanup');
  cleanup();

  // 3️⃣ Compression
  logSection('Compression');

  const ffmpegArgs = [
    '-i', INPUT_FILE,

    // 🎥 Video compression (H.265)
    '-c:v', 'libx265',
    '-preset', PRESET,
    '-crf', CRF.toString(),

    // 🎨 Preserve color (important!)
    '-pix_fmt', 'yuv420p10le',

    // 🔊 Audio compression (AVI audio is huge)
    '-c:a', 'aac',
    '-b:a', '128k',

    // Keep all streams
    '-map', '0',

    '-y',
    OUTPUT_FILE
  ];

  info(`CRF: ${CRF}, Preset: ${PRESET}`);
  info('Compressing...\n');

  const start = Date.now();

  const ffmpeg = spawn('ffmpeg', ffmpegArgs);

  ffmpeg.stderr.on('data', (data) => {
    const msg = data.toString();
    if (msg.includes('frame=')) {
      process.stdout.write(msg);
    }
  });

  ffmpeg.on('close', (code) => {
    logSection('Result');

    if (code !== 0) {
      fail(`FFmpeg failed with code ${code}`);
    }

    const outputStats = fs.statSync(OUTPUT_FILE);
    const time = (Date.now() - start) / 1000;

    success('Compression completed');

    info(`Output: ${OUTPUT_FILE}`);
    info(`Size: ${formatGB(outputStats.size)}`);
    info(`Time: ${time.toFixed(2)} sec`);

    const reduction = (
      (1 - outputStats.size / inputStats.size) * 100
    ).toFixed(2);

    info(`Reduced: ${reduction}%`);

    console.log('\n🎉 Done!\n');
  });

  ffmpeg.on('error', (err) => {
    fail(`Error → ${err.message}`);
  });
}

// ------------------ Run ------------------

compressVideo();