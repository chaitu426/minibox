const express = require('express');
const multer = require('multer');
const path = require('path');
const fs = require('fs');
const http = require('http');
const axios = require('axios');

const app = express();
const PORT = 4000;
const MINIBOX_API = 'http://127.0.0.1:8080';

// Ensure uploads folder exists
const uploadsDir = path.join(__dirname, 'uploads');
if (!fs.existsSync(uploadsDir)) {
    fs.mkdirSync(uploadsDir, { recursive: true });
}
// Chmod to 777 to guarantee write permissions for container process
try { fs.chmodSync(uploadsDir, 0o777); } catch(e){}

// Set up Multer for file uploads
const storage = multer.diskStorage({
    destination: (req, file, cb) => {
        cb(null, uploadsDir);
    },
    filename: (req, file, cb) => {
        const ext = path.extname(file.originalname);
        const name = path.basename(file.originalname, ext).replace(/[^a-zA-Z0-9]/g, '_');
        cb(null, `${name}_${Date.now()}${ext}`);
    }
});
const upload = multer({ storage: storage });

app.use(express.static(path.join(__dirname, 'static')));
app.use(express.json());
// Serve uploads folder as static to easily fetch output files
app.use('/output', express.static(uploadsDir));

app.post('/api/process', upload.single('video'), async (req, res) => {
    try {
        if (!req.file) {
            return res.status(400).json({ error: 'No video file uploaded' });
        }

        const inputFilename = req.file.filename;
        const action = req.body.action || 'to_mp4';
        let customArgs = req.body.customArgs || '';
        
        let ext = '.mp4';
        let ffmpegArgs = [];
        
        if (action === 'to_mp4') {
            ext = '.mp4';
            ffmpegArgs = ['-c:v', 'libx264', '-crf', '23', '-c:a', 'aac', '-preset', 'ultrafast'];
        } else if (action === 'extract_audio') {
            ext = '.mp3';
            ffmpegArgs = ['-vn', '-acodec', 'libmp3lame', '-q:a', '2'];
        } else if (action === 'scale_720') {
            ext = '.mp4';
            ffmpegArgs = ['-vf', 'scale=-2:720', '-c:v', 'libx264', '-crf', '23', '-c:a', 'copy', '-preset', 'ultrafast'];
        } else if (action === 'custom') {
            // Split customArgs simply by space (could be improved)
            // Expecting user to provide format: -c:v libx264
            ffmpegArgs = customArgs.split(' ').filter(arg => arg.trim() !== '');
            // Attempt to glean extension from custom intention or default to .mp4
            ext = '.mp4'; 
        }

        const outputFilename = `out_${Date.now()}${ext}`;
        const inputPathContainer = `/workspace/${inputFilename}`;
        const outputPathContainer = `/workspace/${outputFilename}`;

        const command = ['sh', '-c', `ffmpeg -y -i ${inputPathContainer} ${ffmpegArgs.join(' ')} ${outputPathContainer} 2>&1`];
        
        console.log("Running FFmpeg with Command:", command);

        const runRequest = {
            image: 'custom-ffmpeg',
            command: command,
            interactive: false,
            detached: false, // We want to wait for it to finish and get logs
            volumes: {
                [uploadsDir]: '/workspace'
            },
            user: '1000:1000',
            name: `ffmpeg_job_${Date.now()}`,
            memory: 512,
            cpu: 100000 // 1 CPU
        };

        // Send to Minibox API
        const response = await axios.post(`${MINIBOX_API}/containers/run`, runRequest, {
            responseType: 'text' // Since minibox streams text back directly synchronously
        });
        
        console.log(`Minibox Response: ${response.data}`);

        // Check if output file was actually generated
        const hostOutputPath = path.join(uploadsDir, outputFilename);
        if (fs.existsSync(hostOutputPath)) {
            res.json({
                message: 'Processing successful',
                outputUrl: `/output/${outputFilename}`,
                logs: response.data
            });
        } else {
            res.status(500).json({
                error: 'Processing completed but output file not found.',
                logs: response.data
            });
        }

    } catch (err) {
        console.error('Error processing video:', err.message);
        res.status(500).json({ error: 'Failed to process video: ' + err.message });
    }
});

const server = app.listen(PORT, () => {
    console.log(`Server is running closely on http://localhost:${PORT}`);
});
server.setTimeout(600000);
server.keepAliveTimeout = 600000;
