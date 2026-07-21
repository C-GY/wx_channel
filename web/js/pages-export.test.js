const fs = require('fs');
const path = require('path');
const vm = require('vm');

function assert(condition, message) {
    if (!condition) throw new Error(message);
}

function createSandbox() {
    const elements = {};
    for (const id of [
        'exportRecordTableBody',
        'exportRecordPagination',
        'exportStatTotal',
        'exportStatProcessing',
        'exportStatReady',
        'exportStatFailed',
        'creativeRadarSyncPanel',
        'creativeRadarSyncTitle',
        'creativeRadarSyncDetail',
        'creativeRadarSyncProgressFill',
        'creativeRadarSyncButton',
        'ossUploadQueueTableBody',
        'ossQueueStatTotal',
        'ossQueueStatPending',
        'ossQueueStatUploading',
        'ossQueueStatDone',
        'ossQueueStatFailed',
    ]) {
        elements[id] = {
            innerHTML: '',
            textContent: '',
            style: {},
            disabled: false,
            classList: { toggle() {} },
        };
    }

    const sandbox = {
        console: { log() {}, warn() {}, error() {} },
        document: {
            getElementById(id) { return elements[id] || null; },
            createElement() { return { href: '', download: '', click() {}, remove() {} }; },
            body: { appendChild() {} },
        },
        URL: { createObjectURL() { return 'blob:test'; }, revokeObjectURL() {} },
        ApiClient: {},
        currentPage: 'exports',
        setInterval() { return 1; },
        clearInterval() {},
        escapeHtml(value) {
            return String(value ?? '')
                .replace(/&/g, '&amp;')
                .replace(/</g, '&lt;')
                .replace(/>/g, '&gt;')
                .replace(/"/g, '&quot;');
        },
        formatNumber(value) { return Number(value || 0).toLocaleString('en-US'); },
        formatBytes(value) { return `${Number(value || 0)} B`; },
        showMessage() {},
    };
    vm.createContext(sandbox);
    const source = fs.readFileSync(path.resolve(__dirname, 'pages-export.js'), 'utf8');
    vm.runInContext(source, sandbox, { filename: 'pages-export.js' });
    return { sandbox, elements };
}

function main() {
    const { sandbox, elements } = createSandbox();

    vm.runInContext(`
        exportRecordState.records = [{
            id: 'record-processing',
            fileName: 'processing.csv',
            status: 'processing',
            ossUploadEnabled: true,
            totalCount: 2,
            completedCount: 1,
            failedCount: 0,
            downloadReady: false,
            createdAt: '2026-07-17T07:23:25Z'
        }];
    `, sandbox);
    sandbox.renderExportRecords();
    assert(elements.exportRecordTableBody.innerHTML.includes('disabled'), 'processing OSS export download button should be disabled');
    assert(elements.exportRecordTableBody.innerHTML.includes('1/2'), 'processing OSS export should show completed count');

    vm.runInContext(`
        exportRecordState.records = [{
            id: 'record-ready',
            fileName: 'ready.csv',
            status: 'ready',
            ossUploadEnabled: true,
            totalCount: 2,
            completedCount: 2,
            failedCount: 0,
            downloadReady: true,
            creativeRadarSyncStatus: 'success',
            creativeRadarSyncTotal: 2,
            creativeRadarSyncCompleted: 2,
            creativeRadarInserted: 1,
            creativeRadarUpdated: 1,
            createdAt: '2026-07-17T07:23:25Z'
        }];
    `, sandbox);
    sandbox.renderExportRecords();
    assert(!elements.exportRecordTableBody.innerHTML.includes('disabled'), 'ready OSS export download button should be enabled');
    assert(elements.exportRecordTableBody.innerHTML.includes("downloadExportRecordCSV('record-ready')"), 'ready export should download by record ID');
    assert(elements.exportRecordTableBody.innerHTML.includes('同步成功'), 'ready export should render Creative Radar sync success');
    assert(elements.exportRecordTableBody.innerHTML.includes('新增 1 / 更新 1'), 'sync result should show inserted and updated counts');

    vm.runInContext(`
        exportRecordState.creativeRadarSyncJob = {
            id: 'job-1',
            status: 'running',
            totalRecords: 3,
            completedRecords: 1,
            successRecords: 1,
            failedRecords: 0,
            currentFileName: 'second.csv'
        };
    `, sandbox);
    sandbox.renderCreativeRadarSyncJob();
    assert(elements.creativeRadarSyncPanel.style.display === 'block', 'running sync should show the progress panel');
    assert(elements.creativeRadarSyncTitle.textContent.includes('1/3'), 'running sync should show CSV-level progress');
    assert(elements.creativeRadarSyncDetail.textContent.includes('second.csv'), 'running sync should show the current CSV');
    assert(elements.creativeRadarSyncButton.disabled, 'sync button should be disabled while a job is running');

    sandbox.renderOSSUploadQueue([{
        exportRecordId: 'record-processing',
        exportFileName: 'processing.csv',
        videoId: 'video-1',
        title: 'test video',
        author: 'author',
        downloadStatus: 'done',
        downloadProgress: 100,
        downloadedMB: 5,
        totalMB: 5,
        ossStatus: 'uploading',
        ossProgress: 42.5,
        ossUploadedBytes: 425,
        ossTotalBytes: 1000,
        updatedAt: '2026-07-17T07:23:25Z',
    }]);
    assert(elements.ossUploadQueueTableBody.innerHTML.includes('42.5%'), 'OSS queue should render byte upload progress');
    assert(elements.ossUploadQueueTableBody.innerHTML.includes('上传中'), 'OSS queue should render the uploading state');
}

main();
