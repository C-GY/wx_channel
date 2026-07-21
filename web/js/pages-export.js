/**
 * CSV 导出记录与 OSS 上传队列页面。
 */

const exportRecordState = {
    records: [],
    page: 1,
    pageSize: 20,
    total: 0,
    totalPages: 1,
    stats: { total: 0, processing: 0, ready: 0, failed: 0 },
    creativeRadarSyncJob: { status: 'idle', totalRecords: 0, completedRecords: 0, successRecords: 0, failedRecords: 0 },
    activeCreativeRadarJobId: ''
};

let exportPagePollTimer = null;

function stopExportPagePolling() {
    if (exportPagePollTimer) {
        clearInterval(exportPagePollTimer);
        exportPagePollTimer = null;
    }
}

function startExportPagePolling(page) {
    stopExportPagePolling();
    exportPagePollTimer = setInterval(() => {
        if (typeof currentPage === 'undefined' || currentPage !== page) {
            stopExportPagePolling();
            return;
        }
        if (page === 'exports') {
            loadExportRecords(true);
        } else if (page === 'oss-queue') {
            loadOSSUploadQueue(true);
        }
    }, 2000);
}

function unwrapExportAPIResult(result) {
    if (result && typeof result.code === 'number' && result.code !== 0) {
        throw new Error(result.message || '请求失败');
    }
    return result && result.data !== undefined ? result.data : result;
}

async function loadExportRecords(silent = false) {
    const body = document.getElementById('exportRecordTableBody');
    if (!body) return;
    if (!silent) {
        body.innerHTML = '<tr><td colspan="8" style="text-align:center;padding:40px;color:var(--text-muted);">加载中...</td></tr>';
    }
    try {
        const result = unwrapExportAPIResult(await ApiClient.getExportRecords({
            page: exportRecordState.page,
            pageSize: exportRecordState.pageSize
        }));
        exportRecordState.records = result?.items || [];
        exportRecordState.total = result?.total || 0;
        exportRecordState.totalPages = result?.totalPages || 1;
        exportRecordState.stats = result?.stats || {
            total: exportRecordState.total,
            processing: 0,
            ready: 0,
            failed: 0
        };
        const previousJob = exportRecordState.creativeRadarSyncJob;
        const nextJob = result?.creativeRadarSyncJob || { status: 'idle' };
        exportRecordState.creativeRadarSyncJob = nextJob;
        if (exportRecordState.activeCreativeRadarJobId &&
            nextJob.id === exportRecordState.activeCreativeRadarJobId &&
            previousJob?.status === 'running' && nextJob.status === 'completed') {
            showMessage(nextJob.message || '创意雷达同步完成', Number(nextJob.failedRecords || 0) > 0 ? 'error' : 'success');
            exportRecordState.activeCreativeRadarJobId = '';
        }
        renderExportRecords();
        renderExportRecordPagination();
        updateExportRecordStats();
        renderCreativeRadarSyncJob();
        if (!exportPagePollTimer && typeof currentPage !== 'undefined' && currentPage === 'exports') {
            startExportPagePolling('exports');
        }
    } catch (error) {
        console.error('加载导出记录失败:', error);
        if (!silent) {
            body.innerHTML = `<tr><td colspan="8" style="text-align:center;padding:40px;color:var(--danger-color);">${escapeHtml(error.message)}</td></tr>`;
            showMessage('加载导出记录失败: ' + error.message, 'error');
        }
    }
}

function renderExportRecords() {
    const body = document.getElementById('exportRecordTableBody');
    if (!body) return;
    if (!exportRecordState.records.length) {
        body.innerHTML = '<tr><td colspan="8" style="text-align:center;padding:48px;color:var(--text-muted);">暂无 CSV 导出记录</td></tr>';
        return;
    }

    body.innerHTML = exportRecordState.records.map(record => {
        const total = Number(record.totalCount || 0);
        const completed = Number(record.completedCount || 0);
        const failed = Number(record.failedCount || 0);
        const progress = total > 0 ? Math.min(100, completed / total * 100) : 0;
        const ready = record.downloadReady === true || record.status === 'ready';
        const status = exportRecordStatus(record.status);
        const progressText = record.ossUploadEnabled
            ? `${completed}/${total}${failed ? `，失败 ${failed}` : ''}`
            : `${total}/${total}`;
        const disabledReason = record.status === 'failed'
            ? (record.errorMessage || '存在下载或 OSS 上传失败的视频')
            : '等待全部视频上传 OSS 完成';

        return `
            <tr>
                <td>
                    <div class="table-title" title="${escapeHtml(record.fileName || '')}">${escapeHtml(record.fileName || '-')}</div>
                    ${record.errorMessage ? `<div style="font-size:11px;color:var(--danger-color);margin-top:5px;max-width:360px;white-space:normal;">${escapeHtml(record.errorMessage)}</div>` : ''}
                </td>
                <td>${record.ossUploadEnabled ? '<span style="color:var(--primary-color);font-weight:600;">OSS 地址</span>' : '原始地址'}</td>
                <td>${formatNumber(total)}</td>
                <td>
                    <div class="csv-export-progress-track"><div class="csv-export-progress-fill ${record.status === 'failed' ? 'failed' : ''}" style="width:${record.status === 'ready' ? 100 : progress}%;"></div></div>
                    <div class="csv-export-progress-label">${escapeHtml(progressText)}</div>
                </td>
                <td><span class="download-status ${status.className}">${status.text}</span></td>
                <td>${renderCreativeRadarRecordSync(record)}</td>
                <td>${formatExportPageDate(record.createdAt)}</td>
                <td>
                    <button class="btn btn-primary" style="padding:7px 12px;font-size:12px;" onclick="downloadExportRecordCSV('${escapeHtml(record.id)}')"
                        ${ready ? '' : `disabled title="${escapeHtml(disabledReason)}"`}>
                        下载 CSV
                    </button>
                </td>
            </tr>`;
    }).join('');
}

function renderCreativeRadarRecordSync(record) {
    if (record.status !== 'ready') {
        return '<span class="download-status pending">等待可下载</span>';
    }
    const status = record.creativeRadarSyncStatus || 'not_synced';
    const total = Number(record.creativeRadarSyncTotal || record.totalCount || 0);
    const completed = Number(record.creativeRadarSyncCompleted || 0);
    const failed = Number(record.creativeRadarSyncFailed || 0);
    const inserted = Number(record.creativeRadarInserted || 0);
    const updated = Number(record.creativeRadarUpdated || 0);
    const progress = total > 0 ? clampExportProgress(completed / total * 100) : 0;

    if (status === 'success') {
        return `
            <span class="download-status completed">同步成功</span>
            <div class="creative-radar-result">${formatNumber(completed)} 条 · 新增 ${formatNumber(inserted)} / 更新 ${formatNumber(updated)}</div>
            ${record.creativeRadarSyncedAt ? `<div class="creative-radar-result">${formatExportPageDate(record.creativeRadarSyncedAt)}</div>` : ''}`;
    }
    if (status === 'failed') {
        return `
            <span class="download-status failed">同步失败</span>
            <div class="creative-radar-result">已处理 ${formatNumber(completed)}/${formatNumber(total)}${failed ? ` · 失败 ${formatNumber(failed)}` : ''}</div>
            ${record.creativeRadarSyncError ? `<div class="creative-radar-error" title="${escapeHtml(record.creativeRadarSyncError)}">${escapeHtml(record.creativeRadarSyncError)}</div>` : ''}`;
    }
    if (status === 'syncing') {
        return `
            <span class="download-status in_progress">正在同步</span>
            ${renderExportProgress(progress, `${completed}/${total}`, false)}`;
    }
    if (status === 'pending') {
        return '<span class="download-status pending">等待同步</span>';
    }
    return '<span class="download-status pending">未同步</span>';
}

function renderCreativeRadarSyncJob() {
    const panel = document.getElementById('creativeRadarSyncPanel');
    const title = document.getElementById('creativeRadarSyncTitle');
    const detail = document.getElementById('creativeRadarSyncDetail');
    const fill = document.getElementById('creativeRadarSyncProgressFill');
    const button = document.getElementById('creativeRadarSyncButton');
    const job = exportRecordState.creativeRadarSyncJob || { status: 'idle' };
    const running = job.status === 'running';

    if (button) {
        button.disabled = running;
        button.textContent = running ? '正在同步...' : '同步创意雷达系统';
    }
    if (!panel) return;
    if (job.status === 'idle') {
        panel.style.display = 'none';
        return;
    }
    panel.style.display = 'block';
    const total = Number(job.totalRecords || 0);
    const completed = Number(job.completedRecords || 0);
    const success = Number(job.successRecords || 0);
    const failed = Number(job.failedRecords || 0);
    const progress = total > 0 ? clampExportProgress(completed / total * 100) : 100;
    if (fill) {
        fill.style.width = `${progress}%`;
        fill.classList.toggle('failed', job.status === 'completed' && failed > 0);
    }
    if (title) {
        title.textContent = running ? `正在同步 ${completed}/${total} 个 CSV` : (job.message || '创意雷达同步完成');
    }
    if (detail) {
        detail.textContent = running
            ? `${job.currentFileName ? `当前：${job.currentFileName} · ` : ''}成功 ${success}，失败 ${failed}`
            : `共 ${total} 个 CSV，成功 ${success}，失败 ${failed}${job.finishedAt ? ` · ${formatExportPageDate(job.finishedAt)}` : ''}`;
    }
}

async function syncCreativeRadarRecords() {
    const button = document.getElementById('creativeRadarSyncButton');
    if (button) button.disabled = true;
    try {
        const job = unwrapExportAPIResult(await ApiClient.syncCreativeRadar());
        exportRecordState.creativeRadarSyncJob = job || { status: 'idle' };
        exportRecordState.activeCreativeRadarJobId = job?.status === 'running' ? (job.id || '') : '';
        renderCreativeRadarSyncJob();
        if (job?.totalRecords > 0) {
            showMessage(`已开始同步 ${job.totalRecords} 个可下载 CSV`, 'success');
        } else {
            showMessage(job?.message || '没有需要同步的可下载 CSV', 'info');
        }
        await loadExportRecords(true);
    } catch (error) {
        showMessage('启动创意雷达同步失败: ' + error.message, 'error');
        if (button) button.disabled = false;
        await loadExportRecords(true);
    }
}

function exportRecordStatus(status) {
    switch (status) {
        case 'ready': return { text: '可下载', className: 'completed' };
        case 'failed': return { text: '失败', className: 'failed' };
        default: return { text: '处理中', className: 'in_progress' };
    }
}

function updateExportRecordStats() {
    const stats = exportRecordState.stats;
    const setText = (id, value) => {
        const element = document.getElementById(id);
        if (element) element.textContent = formatNumber(value);
    };
    setText('exportStatTotal', stats.total);
    setText('exportStatProcessing', stats.processing);
    setText('exportStatReady', stats.ready);
    setText('exportStatFailed', stats.failed);
}

function renderExportRecordPagination() {
    const pagination = document.getElementById('exportRecordPagination');
    if (!pagination) return;
    const current = exportRecordState.page;
    const total = Math.max(1, exportRecordState.totalPages);
    pagination.innerHTML = `
        <button ${current <= 1 ? 'disabled' : ''} onclick="changeExportRecordPage(${current - 1})">上一页</button>
        <span class="pagination-info">第 ${current} / ${total} 页，共 ${exportRecordState.total} 条</span>
        <button ${current >= total ? 'disabled' : ''} onclick="changeExportRecordPage(${current + 1})">下一页</button>`;
}

function changeExportRecordPage(page) {
    if (page < 1 || page > exportRecordState.totalPages || page === exportRecordState.page) return;
    exportRecordState.page = page;
    loadExportRecords();
}

async function downloadExportRecordCSV(id) {
    try {
        const result = await ApiClient.downloadExportRecordCSV(id);
        const url = URL.createObjectURL(result.blob);
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = result.filename || 'batch_videos.csv';
        document.body.appendChild(anchor);
        anchor.click();
        anchor.remove();
        URL.revokeObjectURL(url);
        showMessage('CSV 下载已开始', 'success');
    } catch (error) {
        showMessage('下载 CSV 失败: ' + error.message, 'error');
        await loadExportRecords(true);
    }
}

async function loadOSSUploadQueue(silent = false) {
    const body = document.getElementById('ossUploadQueueTableBody');
    if (!body) return;
    if (!silent) {
        body.innerHTML = '<tr><td colspan="6" style="text-align:center;padding:40px;color:var(--text-muted);">加载中...</td></tr>';
    }
    try {
        const data = unwrapExportAPIResult(await ApiClient.getOSSUploadQueue()) || {};
        renderOSSUploadQueue(data.items || []);
        updateOSSUploadQueueStats(data.stats || {});
        if (!exportPagePollTimer && typeof currentPage !== 'undefined' && currentPage === 'oss-queue') {
            startExportPagePolling('oss-queue');
        }
    } catch (error) {
        console.error('加载 OSS 上传队列失败:', error);
        if (!silent) {
            body.innerHTML = `<tr><td colspan="6" style="text-align:center;padding:40px;color:var(--danger-color);">${escapeHtml(error.message)}</td></tr>`;
            showMessage('加载 OSS 上传队列失败: ' + error.message, 'error');
        }
    }
}

function renderOSSUploadQueue(items) {
    const body = document.getElementById('ossUploadQueueTableBody');
    if (!body) return;
    if (!items.length) {
        body.innerHTML = '<tr><td colspan="6" style="text-align:center;padding:48px;color:var(--text-muted);">暂无 OSS 上传任务</td></tr>';
        return;
    }
    body.innerHTML = items.map(item => {
        const downloadProgress = clampExportProgress(item.downloadProgress);
        const ossProgress = item.ossStatus === 'done' ? 100 : clampExportProgress(item.ossProgress);
        const status = ossUploadStatus(item.ossStatus, item.downloadStatus);
        const downloadSize = Number(item.totalMB || 0) > 0
            ? `${Number(item.downloadedMB || 0).toFixed(2)} / ${Number(item.totalMB || 0).toFixed(2)} MB`
            : `${downloadProgress.toFixed(1)}%`;
        const ossSize = Number(item.ossTotalBytes || 0) > 0
            ? `${formatBytes(Number(item.ossUploadedBytes || 0))} / ${formatBytes(Number(item.ossTotalBytes || 0))}`
            : `${ossProgress.toFixed(1)}%`;
        return `
            <tr class="${item.ossStatus === 'failed' ? 'error-row' : ''}">
                <td>
                    <div class="table-title" title="${escapeHtml(item.title || '')}">${escapeHtml(item.title || item.videoId || '-')}</div>
                    <div style="font-size:12px;color:var(--text-muted);margin-top:4px;">${escapeHtml(item.author || '未知作者')} · ${escapeHtml(item.videoId || '')}</div>
                    ${item.errorMessage ? `<div style="font-size:11px;color:var(--danger-color);margin-top:5px;white-space:normal;">${escapeHtml(item.errorMessage)}</div>` : ''}
                </td>
                <td title="${escapeHtml(item.exportFileName || '')}">${escapeHtml(item.exportFileName || '-')}</td>
                <td>${renderExportProgress(downloadProgress, downloadSize, item.downloadStatus === 'failed')}</td>
                <td>${renderExportProgress(ossProgress, ossSize, item.ossStatus === 'failed')}</td>
                <td><span class="download-status ${status.className}">${status.text}</span></td>
                <td>${formatExportPageDate(item.updatedAt)}</td>
            </tr>`;
    }).join('');
}

function renderExportProgress(progress, label, failed) {
    return `
        <div class="csv-export-progress-track"><div class="csv-export-progress-fill ${failed ? 'failed' : ''}" style="width:${progress}%;"></div></div>
        <div class="csv-export-progress-label">${escapeHtml(label)}</div>`;
}

function clampExportProgress(value) {
    const number = Number(value || 0);
    if (!Number.isFinite(number)) return 0;
    return Math.max(0, Math.min(100, number));
}

function ossUploadStatus(ossStatus, downloadStatus) {
    if (ossStatus === 'done') return { text: '已上传', className: 'completed' };
    if (ossStatus === 'failed' || downloadStatus === 'failed') return { text: '失败', className: 'failed' };
    if (ossStatus === 'uploading') return { text: '上传中', className: 'in_progress' };
    if (ossStatus === 'retrying') return { text: '重试中', className: 'in_progress' };
    if (downloadStatus === 'downloading') return { text: '下载中', className: 'in_progress' };
    if (downloadStatus === 'paused' || ossStatus === 'paused') return { text: '已暂停', className: 'pending' };
    return { text: '等待中', className: 'pending' };
}

function updateOSSUploadQueueStats(stats) {
    const setText = (id, value) => {
        const element = document.getElementById(id);
        if (element) element.textContent = formatNumber(Number(value || 0));
    };
    setText('ossQueueStatTotal', stats.total);
    setText('ossQueueStatPending', stats.pending);
    setText('ossQueueStatUploading', stats.uploading);
    setText('ossQueueStatDone', stats.done);
    setText('ossQueueStatFailed', stats.failed);
}

function formatExportPageDate(value) {
    if (!value) return '-';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return escapeHtml(String(value));
    const pad = number => String(number).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
