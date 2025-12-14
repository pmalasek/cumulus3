let refreshInterval;

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function showAlert(message, type = 'info') {
    const alerts = document.getElementById('alerts');
    const alert = document.createElement('div');
    alert.className = 'alert alert-' + type;
    alert.textContent = message;
    alerts.appendChild(alert);
    setTimeout(() => alert.remove(), 5000);
}

async function loadStats() {
    try {
        const response = await fetch('/system/stats');
        const data = await response.json();

        document.getElementById('blob-count').textContent = data.blobs.count.toLocaleString();
        document.getElementById('blob-size').textContent = formatBytes(data.blobs.totalSize);
        document.getElementById('blob-raw-size').textContent = formatBytes(data.blobs.rawSize);
        document.getElementById('compression-ratio').textContent = data.blobs.compressionRatio.toFixed(2) + '%';

        document.getElementById('file-count').textContent = data.files.count.toLocaleString();
        document.getElementById('dedup-count').textContent = data.files.deduplicatedCount.toLocaleString();
        document.getElementById('dedup-ratio').textContent = data.files.deduplicationRatio.toFixed(2) + '%';

        document.getElementById('storage-total').textContent = formatBytes(data.storage.totalSize);
        document.getElementById('storage-used').textContent = formatBytes(data.storage.usedSize);
        document.getElementById('storage-deleted').textContent = formatBytes(data.storage.deletedSize);
        document.getElementById('storage-fragmentation').textContent = data.storage.fragmentationRatio.toFixed(2) + '%';
    } catch (error) {
        console.error('Failed to load stats:', error);
        showAlert('Failed to load statistics', 'error');
    }
}

async function loadVolumes() {
    try {
        const response = await fetch('/system/volumes');
        const volumes = await response.json();

        const list = document.getElementById('volumes-list');
        if (volumes.length === 0) {
            list.innerHTML = '<p>No volumes</p>';
            return;
        }

        list.innerHTML = volumes.map(vol => `
            <div class="volume-item">
                <div class="volume-header">
                    <span class="volume-id">Volume ${vol.id}</span>
                    <button class="button" onclick="compactVolume(${vol.id})">🔧 Compact</button>
                </div>
                <div class="volume-stats">
                    <div class="stat">
                        <span class="stat-label">Total:</span>
                        <span class="stat-value">${formatBytes(vol.totalSize)}</span>
                    </div>
                    <div class="stat">
                        <span class="stat-label">Used:</span>
                        <span class="stat-value">${formatBytes(vol.usedSize)}</span>
                    </div>
                    <div class="stat">
                        <span class="stat-label">Deleted:</span>
                        <span class="stat-value">${formatBytes(vol.deletedSize)}</span>
                    </div>
                    <div class="stat">
                        <span class="stat-label">Fragmentation:</span>
                        <span class="stat-value">${vol.fragmentation.toFixed(2)}%</span>
                    </div>
                </div>
                <div class="progress-bar">
                    <div class="progress-fill" style="width: ${100 - vol.fragmentation}%"></div>
                </div>
            </div>
        `).join('');
    } catch (error) {
        console.error('Failed to load volumes:', error);
        showAlert('Failed to load volumes', 'error');
    }
}

async function loadJobs() {
    try {
        const response = await fetch('/system/jobs');
        const jobs = await response.json();

        const list = document.getElementById('jobs-list');
        if (jobs.length === 0) {
            list.innerHTML = '<p>No jobs</p>';
            return;
        }

        jobs.sort((a, b) => new Date(b.startedAt) - new Date(a.startedAt));

        list.innerHTML = jobs.slice(0, 10).map(job => {
            const statusClass = 'job-' + job.status;
            const startTime = new Date(job.startedAt).toLocaleString('en-US');
            const duration = job.completedAt 
                ? ((new Date(job.completedAt) - new Date(job.startedAt)) / 1000).toFixed(1) + 's'
                : 'running...';
            
            let progressHTML = '';
            if (job.progress) {
                try {
                    const progressData = JSON.parse(job.progress);
                    if (progressData.orphanedBlobs !== undefined) {
                        progressHTML = `
                            <div style="margin-top: 8px; padding: 8px; background: #0f172a; border-radius: 4px; font-size: 12px;">
                                <div style="color: ${progressData.status === 'ok' ? '#10b981' : progressData.status === 'warning' ? '#f59e0b' : '#ef4444'};">
                                    Status: ${progressData.status.toUpperCase()}
                                </div>
                                ${progressData.orphanedBlobs > 0 ? '<div style="color: #fbbf24;">⚠️ Orphaned blobs: ' + progressData.orphanedBlobs + '</div>' : ''}
                                ${progressData.missingBlobs > 0 ? '<div style="color: #f87171;">❌ Missing blobs: ' + progressData.missingBlobs + '</div>' : ''}
                                ${progressData.missingVolumes && progressData.missingVolumes.length > 0 ? '<div style="color: #f87171;">❌ Missing volumes: ' + progressData.missingVolumes.join(', ') + '</div>' : ''}
                                ${progressData.unreadableBlobs !== undefined && progressData.unreadableBlobs > 0 ? '<div style="color: #f87171;">❌ Unreadable blobs: ' + progressData.unreadableBlobs + '</div>' : ''}
                                ${progressData.totalBlobsChecked !== undefined ? '<div style="color: #94a3b8;">Checked: ' + progressData.totalBlobsChecked + ' blobs</div>' : ''}
                            </div>
                        `;
                    } else {
                        progressHTML = '<div style="margin-top: 5px; color: #e2e8f0;">' + job.progress + '</div>';
                    }
                } catch (e) {
                    progressHTML = '<div style="margin-top: 5px; color: #e2e8f0;">' + job.progress + '</div>';
                }
            }

            return `
                <div class="job-item ${statusClass}">
                    <div style="display: flex; justify-content: space-between; margin-bottom: 5px;">
                        <strong>${job.type}${job.volumeId ? ' (Volume ' + job.volumeId + ')' : ''}</strong>
                        <span>${job.status}</span>
                    </div>
                    <div style="font-size: 12px; color: #94a3b8;">
                        Started: ${startTime} | Duration: ${duration}
                    </div>
                    ${progressHTML}
                    ${job.error ? '<div style="margin-top: 5px; color: #f87171;">Error: ' + job.error + '</div>' : ''}
                </div>
            `;
        }).join('');

        const hasRunningJobs = jobs.some(job => job.status === 'running' || job.status === 'pending');
        if (hasRunningJobs && !refreshInterval) {
            startAutoRefresh();
        } else if (!hasRunningJobs && refreshInterval) {
            stopAutoRefresh();
        }
    } catch (error) {
        console.error('Failed to load jobs:', error);
        showAlert('Failed to load jobs', 'error');
    }
}

async function compactVolume(volumeId) {
    if (!confirm('Are you sure you want to compact volume ' + volumeId + '?')) {
        return;
    }

    try {
        const response = await fetch('/system/compact', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({volumeId: volumeId})
        });
        const data = await response.json();
        showAlert('Compaction started: ' + data.jobId, 'success');
        setTimeout(() => {
            loadJobs();
            startAutoRefresh();
        }, 1000);
    } catch (error) {
        console.error('Failed to compact volume:', error);
        showAlert('Failed to start compaction', 'error');
    }
}

async function compactAll() {
    if (!confirm('Are you sure you want to compact all volumes?')) {
        return;
    }

    try {
        const response = await fetch('/system/compact', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({all: true})
        });
        const data = await response.json();
        showAlert('Compacting all volumes started: ' + data.jobId, 'success');
        setTimeout(() => {
            loadJobs();
            startAutoRefresh();
        }, 1000);
    } catch (error) {
        console.error('Failed to compact all:', error);
        showAlert('Failed to start compaction', 'error');
    }
}

async function checkIntegrity(deep = false) {
    try {
        const url = deep ? '/system/integrity?deep=true' : '/system/integrity';
        const response = await fetch(url);
        const data = await response.json();
        const checkType = deep ? 'Deep integrity check' : 'Quick integrity check';
        showAlert(`${checkType} started: ${data.jobId}`, 'info');
        setTimeout(() => {
            loadJobs();
            startAutoRefresh();
        }, 1000);
    } catch (error) {
        console.error('Failed to check integrity:', error);
        showAlert('Failed to start integrity check', 'error');
    }
}

function refreshVolumes() {
    loadVolumes();
    loadStats();
    showAlert('Volumes refreshed', 'success');
}

function refreshJobs() {
    loadJobs();
    showAlert('Jobs refreshed', 'success');
}

function startAutoRefresh() {
    if (refreshInterval) return;
    refreshInterval = setInterval(() => {
        loadJobs();
        loadStats();
        loadVolumes();
    }, 3000);
}

function stopAutoRefresh() {
    if (refreshInterval) {
        clearInterval(refreshInterval);
        refreshInterval = null;
    }
}

loadStats();
loadVolumes();
loadJobs();

setInterval(() => {
    if (!refreshInterval) {
        loadStats();
    }
}, 10000);
