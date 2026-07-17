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
        'ossUploadQueueTableBody',
        'ossQueueStatTotal',
        'ossQueueStatPending',
        'ossQueueStatUploading',
        'ossQueueStatDone',
        'ossQueueStatFailed',
    ]) {
        elements[id] = { innerHTML: '', textContent: '' };
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
            createdAt: '2026-07-17T07:23:25Z'
        }];
    `, sandbox);
    sandbox.renderExportRecords();
    assert(!elements.exportRecordTableBody.innerHTML.includes('disabled'), 'ready OSS export download button should be enabled');
    assert(elements.exportRecordTableBody.innerHTML.includes("downloadExportRecordCSV('record-ready')"), 'ready export should download by record ID');

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
