document.addEventListener('DOMContentLoaded', () => {
    const dropZone = document.getElementById('drop-zone');
    const fileInput = document.getElementById('file-input');
    const fileInfo = document.getElementById('file-info');
    const filenameDisplay = document.getElementById('filename');
    const removeFileBtn = document.getElementById('remove-file');
    const startBtn = document.getElementById('start-btn');
    
    const optionCards = document.querySelectorAll('.option-card');
    const customArgsContainer = document.getElementById('custom-args-container');
    const customArgsInput = document.getElementById('custom-args');
    
    const resultSection = document.getElementById('result-section');
    const loader = document.getElementById('loader');
    const terminalView = document.getElementById('terminal-view');
    const logsBody = document.getElementById('logs-body');
    const outputPresentation = document.getElementById('output-presentation');
    const resultVideo = document.getElementById('result-video');
    const resultAudio = document.getElementById('result-audio');
    const downloadLink = document.getElementById('download-link');

    let currentFile = null;
    let selectedAction = 'to_mp4';

    // File Drag & Drop
    dropZone.addEventListener('click', () => fileInput.click());

    dropZone.addEventListener('dragover', (e) => {
        e.preventDefault();
        dropZone.classList.add('dragover');
    });

    dropZone.addEventListener('dragleave', () => {
        dropZone.classList.remove('dragover');
    });

    dropZone.addEventListener('drop', (e) => {
        e.preventDefault();
        dropZone.classList.remove('dragover');
        if (e.dataTransfer.files.length) {
            handleFileSelect(e.dataTransfer.files[0]);
        }
    });

    fileInput.addEventListener('change', (e) => {
        if (e.target.files.length) {
            handleFileSelect(e.target.files[0]);
        }
    });

    function handleFileSelect(file) {
        currentFile = file;
        filenameDisplay.textContent = file.name;
        dropZone.classList.add('hidden');
        fileInfo.classList.remove('hidden');
        startBtn.disabled = false;
    }

    removeFileBtn.addEventListener('click', () => {
        currentFile = null;
        fileInput.value = '';
        fileInfo.classList.add('hidden');
        dropZone.classList.remove('hidden');
        startBtn.disabled = true;
    });

    // Options Selection
    optionCards.forEach(card => {
        card.addEventListener('click', () => {
            optionCards.forEach(c => c.classList.remove('active'));
            card.classList.add('active');
            selectedAction = card.dataset.action;

            if (selectedAction === 'custom') {
                customArgsContainer.classList.remove('hidden');
            } else {
                customArgsContainer.classList.add('hidden');
            }
        });
    });

    // Submit Processing
    startBtn.addEventListener('click', async () => {
        if (!currentFile) return;

        // Reset UI State for processing
        resultSection.classList.remove('hidden');
        loader.classList.remove('hidden');
        terminalView.classList.add('hidden');
        outputPresentation.classList.add('hidden');
        logsBody.textContent = 'Uploading to Server and Preparing Container...';
        
        startBtn.disabled = true;
        // Scroll to results
        resultSection.scrollIntoView({ behavior: 'smooth' });

        const formData = new FormData();
        formData.append('video', currentFile);
        formData.append('action', selectedAction);
        
        if (selectedAction === 'custom') {
            formData.append('customArgs', customArgsInput.value);
        }

        try {
            const response = await fetch('/api/process', {
                method: 'POST',
                body: formData
            });

            const data = await response.json();
            
            loader.classList.add('hidden');
            terminalView.classList.remove('hidden');
            logsBody.textContent = data.logs || 'No logs returned';
            
            if (response.ok) {
                outputPresentation.classList.remove('hidden');
                downloadLink.href = data.outputUrl;
                
                // Determine if audio or video based on action and extension
                const isAudio = data.outputUrl.endsWith('.mp3');
                if (isAudio) {
                    resultVideo.classList.add('hidden');
                    resultAudio.classList.remove('hidden');
                    resultAudio.src = data.outputUrl;
                    resultAudio.load();
                } else {
                    resultAudio.classList.add('hidden');
                    resultVideo.classList.remove('hidden');
                    resultVideo.src = data.outputUrl;
                    resultVideo.load();
                }
            } else {
                console.error("Error from API:", data.error);
                logsBody.textContent += `\n\nERROR: ${data.error}`;
            }

        } catch (error) {
            console.error('Network block or execution error:', error);
            loader.classList.add('hidden');
            terminalView.classList.remove('hidden');
            logsBody.textContent = `CRITICAL ERROR: ${error.message}`;
        } finally {
            startBtn.disabled = false;
        }
    });
});
