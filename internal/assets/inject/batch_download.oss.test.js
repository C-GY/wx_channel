const fs = require('fs');
const path = require('path');
const vm = require('vm');

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}\nactual:   ${actual}\nexpected: ${expected}`);
  }
}

function response(data) {
  return {
    ok: true,
    status: 200,
    async json() {
      return { code: 0, message: 'success', data };
    },
  };
}

function blobResponse(blob) {
  return {
    ok: true,
    status: 200,
    async json() { return {}; },
    async blob() { return blob; },
  };
}

function createElementState(extra = {}) {
  return Object.assign({
    value: '',
    checked: false,
    disabled: false,
    placeholder: '',
    textContent: '',
    style: {},
    focus() { this.focused = true; },
  }, extra);
}

function loadBatchDownloadModule() {
  const source = fs.readFileSync(path.resolve(__dirname, 'batch_download.js'), 'utf8');
  const defaultSecret = 'mvd_9XvKp7Qw2Lm8Nz4Rt6Yb3Hs5Fc1Da0';
  const elements = {
    'batch-oss-upload-enabled': createElementState({ checked: true }),
    'batch-oss-config-panel': createElementState(),
    'batch-oss-access-key-id': createElementState({ value: 'marketing-video-dashboard' }),
    'batch-oss-access-key-secret': createElementState({ value: defaultSecret }),
    'batch-oss-config-status': createElementState(),
    'batch-oss-clear-config': createElementState(),
    'batch-sync-creative-radar-btn': createElementState(),
  };
  const requests = [];
  const logs = [];
  const blobs = [];
  let anchorClicked = false;
  let exportSequence = 0;
  const creativeRadarSyncStates = {};

  class SandboxURL extends URL {}
  SandboxURL.createObjectURL = function () { return 'blob:test'; };
  SandboxURL.revokeObjectURL = function () {};

  const sandbox = {
    console: { log() {}, error() {}, warn() {} },
    window: {},
    document: {
      body: { appendChild() {} },
      createElement(tag) {
        if (tag === 'a') {
          return {
            style: {},
            click() { anchorClicked = true; },
          };
        }
        return createElementState({
          addEventListener() {},
          appendChild() {},
          click() {},
          remove() {},
        });
      },
      getElementById(id) { return elements[id] || null; },
      querySelector() { return null; },
    },
    location: {
      href: 'https://channels.weixin.qq.com/web/pages/profile',
      origin: 'https://channels.weixin.qq.com',
      pathname: '/web/pages/profile',
    },
    navigator: { userAgent: 'node-test' },
    Blob: function Blob(parts, options) {
      this.parts = parts;
      this.options = options;
      blobs.push(this);
    },
    URL: SandboxURL,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    Date,
    AbortController,
    confirm() { return true; },
    WXU: {
      format_feed(video) {
        const media = video.objectDesc.media[0];
        return {
          id: video.id,
          type: 'media',
          title: video.objectDesc.description,
          nickname: 'test author',
          url: media.url,
          key: '',
          duration: media.durationMs,
          size: media.fileSize,
        };
      },
    },
    async fetch(url, options = {}) {
      const method = options.method || 'GET';
      requests.push({ url, method, options });
      if (method === 'GET' && url.endsWith('/oss_config')) {
        return response({ accessKeyId: 'cached-id', hasSecret: true });
      }
      if (method === 'GET' && /\/api\/export-records\/[^/]+\/csv$/.test(url)) {
        return blobResponse({ type: 'text/csv', marker: 'backend-csv' });
      }
      if (method === 'GET' && /\/api\/export-records\/[^/]+$/.test(url)) {
        const exportRecordId = decodeURIComponent(url.split('/').at(-1));
        const synchronized = creativeRadarSyncStates[exportRecordId] === 'success';
        return response({
          record: {
            id: exportRecordId,
            status: 'ready',
            creativeRadarSyncStatus: synchronized ? 'success' : 'not_synced',
            creativeRadarSyncCompleted: synchronized ? 1 : 0,
            creativeRadarInserted: synchronized ? 1 : 0,
            creativeRadarUpdated: 0,
          },
          items: [],
        });
      }
      if (method === 'POST' && url.endsWith('/batch_progress')) {
        return response({
          total: 1,
          done: 1,
          failed: 0,
          running: 0,
          tasks: [{
            id: 'video-1',
            ossStatus: 'done',
            ossObjectKey: 'wechat_channel/2026-07-17/video-1.mp4',
            ossUrl: 'https://signed.example/video-1?token=temporary',
            ossError: '',
          }],
        });
      }
      if (method === 'POST' && url.endsWith('/oss_config')) {
        const body = JSON.parse(options.body);
        return response({ accessKeyId: body.accessKeyId, hasSecret: true, saved: true });
      }
      if (method === 'POST' && url === '/api/export-records') {
        exportSequence += 1;
        const body = JSON.parse(options.body);
        const exportRecordId = `export-${exportSequence}`;
        if (body.autoSyncCreativeRadar) {
          creativeRadarSyncStates[exportRecordId] = 'success';
        }
        return response({
          id: exportRecordId,
          fileName: body.fileName,
          status: body.ossUploadEnabled ? 'processing' : 'ready',
          downloadReady: !body.ossUploadEnabled,
          ossUploadEnabled: body.ossUploadEnabled,
        });
      }
      if (method === 'POST' && url.endsWith('/batch_start')) {
        const body = JSON.parse(options.body);
        return response({
          total: body.videos.length,
          concurrency: 5,
          exportRecordId: body.exportRecordId,
          batchId: body.exportRecordId || 'batch-test',
          queued: false,
        });
      }
      if (method === 'POST' && /\/api\/export-records\/[^/]+\/fail$/.test(url)) {
        return response({ failed: true });
      }
      if (method === 'POST' && /\/api\/export-records\/[^/]+\/creative-radar-sync$/.test(url)) {
        const exportRecordId = decodeURIComponent(url.split('/').at(-2));
        creativeRadarSyncStates[exportRecordId] = 'success';
        return response({ status: 'running', totalRecords: 1 });
      }
      if (method === 'DELETE') {
        return response({ cleared: true });
      }
      throw new Error(`unexpected request: ${method} ${url}`);
    },
    __wx_log(entry) { logs.push(entry); },
  };

  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox, { filename: 'batch_download.js' });
  return {
    sandbox,
    elements,
    requests,
    logs,
    blobs,
    anchorWasClicked: () => anchorClicked,
  };
}

async function main() {
  const fixture = loadBatchDownloadModule();
  const { sandbox, elements, requests, blobs } = fixture;
  const manager = sandbox.__wx_batch_download_manager__;

  assertEqual(manager.ossAccessKeyId, 'marketing-video-dashboard', 'AccessKey ID should have the requested default value');
  assert(manager.ossUploadEnabled, 'OSS upload should be enabled by default');
  assertEqual(elements['batch-oss-access-key-secret'].value, 'mvd_9XvKp7Qw2Lm8Nz4Rt6Yb3Hs5Fc1Da0', 'AccessKey Secret should have the requested default value');

  await sandbox.__load_batch_oss_config__();
  assertEqual(elements['batch-oss-access-key-id'].value, 'cached-id', 'GET should restore the cached AccessKey ID');
  assertEqual(elements['batch-oss-access-key-secret'].value, 'mvd_9XvKp7Qw2Lm8Nz4Rt6Yb3Hs5Fc1Da0', 'GET should preserve a Secret the page already initialized');
  assert(manager.ossHasSavedSecret, 'GET should retain only the hasSecret marker');
  assert(elements['batch-oss-access-key-secret'].placeholder.includes('留空'), 'saved Secret should be represented by a placeholder');

  let state = await sandbox.__persist_batch_oss_config_if_needed__();
  assert(state && state.enabled, 'saved credentials should enable OSS upload');
  let post = requests.filter((request) => request.method === 'POST' && request.url.endsWith('/oss_config')).at(-1);
  let body = JSON.parse(post.options.body);
  assertEqual(body.accessKeyId, 'cached-id', 'POST should retain the cached AccessKey ID');
  assertEqual(body.accessKeySecret, 'mvd_9XvKp7Qw2Lm8Nz4Rt6Yb3Hs5Fc1Da0', 'POST should save the requested default Secret');

  elements['batch-oss-access-key-id'].value = 'changed-id';
  elements['batch-oss-access-key-secret'].value = '';
  const postCount = requests.filter((request) => request.method === 'POST' && request.url.endsWith('/oss_config')).length;
  state = await sandbox.__persist_batch_oss_config_if_needed__();
  assertEqual(state, null, 'changing the AccessKey ID should require a matching new Secret');
  assertEqual(
    requests.filter((request) => request.method === 'POST' && request.url.endsWith('/oss_config')).length,
    postCount,
    'invalid credentials should not be sent',
  );

  elements['batch-oss-access-key-secret'].value = 'changed-secret';
  state = await sandbox.__persist_batch_oss_config_if_needed__();
  assert(state && state.enabled, 'a complete replacement credential pair should be saved');
  post = requests.filter((request) => request.method === 'POST' && request.url.endsWith('/oss_config')).at(-1);
  body = JSON.parse(post.options.body);
  assertEqual(body.accessKeySecret, 'changed-secret', 'POST should send a newly entered Secret');
  assertEqual(elements['batch-oss-access-key-secret'].value, '', 'the Secret field should be cleared after saving');

  await sandbox.__clear_batch_oss_config__();
  assert(requests.some((request) => request.method === 'DELETE'), 'clear should call the local DELETE endpoint');
  assertEqual(elements['batch-oss-access-key-id'].value, 'marketing-video-dashboard', 'clear should restore the default AccessKey ID');
  assertEqual(elements['batch-oss-access-key-secret'].value, 'mvd_9XvKp7Qw2Lm8Nz4Rt6Yb3Hs5Fc1Da0', 'clear should restore the default AccessKey Secret');
  assertEqual(manager.ossAccessKeyId, 'marketing-video-dashboard', 'clear should restore the manager default AccessKey ID');
  assertEqual(manager.ossSavedAccessKeyId, '', 'clear should still remove the cached AccessKey ID');
  assert(!manager.ossHasSavedSecret, 'clear should reset the saved Secret marker');

  // The CSV action is the requested persistence trigger. In OSS mode it creates
  // a record and starts the linked download/upload task, but does not download CSV yet.
  elements['batch-oss-upload-enabled'].checked = true;
  elements['batch-oss-access-key-id'].value = 'csv-id';
  elements['batch-oss-access-key-secret'].value = 'csv-secret';
  manager.setVideos([{
    id: 'video-1',
    objectDesc: {
      description: 'test video',
      media: [{
        url: 'https://finder.video.qq.com/video-1.mp4',
        durationMs: 123000,
        fileSize: 1024,
      }],
    },
    coverUrl: 'https://finder.video.qq.com/video-1-cover.jpg',
    likeCount: 10,
  }], 'test');
  await sandbox.__export_batch_video_csv__();
  post = requests.filter((request) => request.method === 'POST' && request.url.endsWith('/oss_config')).at(-1);
  body = JSON.parse(post.options.body);
  assertEqual(body.accessKeyId, 'csv-id', 'Export CSV should cache the current OSS configuration');
  assertEqual(body.accessKeySecret, 'csv-secret', 'Export CSV should cache the current OSS Secret');
  const createOSSRequest = requests.find(request => request.method === 'POST' && request.url === '/api/export-records');
  const createOSSBody = JSON.parse(createOSSRequest.options.body);
  assert(createOSSBody.ossUploadEnabled, 'OSS export record should be marked as an OSS export');
  assertEqual(createOSSBody.videos.length, 1, 'OSS export record should contain the same downloadable videos as the batch task');
  assertEqual(createOSSBody.videos[0].coverUrl, 'https://finder.video.qq.com/video-1-cover.jpg', 'OSS export record should preserve coverUrl for Creative Radar');
  const batchStart = requests.find(request => request.method === 'POST' && request.url.endsWith('/batch_start'));
  const batchBody = JSON.parse(batchStart.options.body);
  assertEqual(batchBody.exportRecordId, 'export-1', 'batch task should be linked to its export record');
  assert(batchBody.ossUploadEnabled, 'CSV action should enable OSS upload on the linked batch task');
  const linkedProgressRequest = requests.find(request => {
    if (request.method !== 'POST' || !request.url.endsWith('/batch_progress') || !request.options.body) return false;
    return JSON.parse(request.options.body).batchId === 'export-1';
  });
  assert(linkedProgressRequest, 'progress polling should target the batch returned by batch_start');
  assertEqual(
    batchBody.videos[0].capturedAt,
    createOSSBody.videos[0].capturedAt,
    'batch task should keep the source collection time even when WXU.format_feed returns a new object',
  );
  assert(!fixture.anchorWasClicked(), 'OSS export must not download CSV before every upload is ready');
  assert(!requests.some(request => request.method === 'GET' && request.url.includes('/api/export-records/export-1/csv')), 'OSS export must not request CSV immediately');

  elements['batch-oss-upload-enabled'].checked = false;
  const requestCount = requests.length;
  state = await sandbox.__persist_batch_oss_config_if_needed__();
  assert(state && !state.enabled, 'unchecked OSS option should disable upload');
  assertEqual(requests.length, requestCount, 'unchecked OSS option should not access cached credentials');

  await sandbox.__export_batch_video_csv__();
  const createRequests = requests.filter(request => request.method === 'POST' && request.url === '/api/export-records');
  const nonOSSBody = JSON.parse(createRequests.at(-1).options.body);
  assert(!nonOSSBody.ossUploadEnabled, 'non-OSS click should create an original-link export record');
  assert(requests.some(request => request.method === 'GET' && request.url.includes('/api/export-records/export-2/csv')), 'non-OSS export should request its ready CSV immediately');
  assert(fixture.anchorWasClicked(), 'non-OSS export should immediately trigger the browser download');
  assertEqual(blobs.length, 0, 'CSV is generated by the backend and must not expose credentials in a page-created Blob');

  elements['batch-oss-upload-enabled'].checked = true;
  await sandbox.__export_batch_video_csv__({
    autoSyncCreativeRadar: true,
    actionButton: elements['batch-sync-creative-radar-btn'],
  });
  const autoCreateRequests = requests.filter(request => request.method === 'POST' && request.url === '/api/export-records');
  const autoExportId = `export-${autoCreateRequests.length}`;
  const autoCreateBody = JSON.parse(autoCreateRequests.at(-1).options.body);
  assert(autoCreateBody.autoSyncCreativeRadar, 'Creative Radar button should persist the automatic-sync request on the export record');
  assert(
    !requests.some(request => request.method === 'POST' && request.url === `/api/export-records/${autoExportId}/creative-radar-sync`),
    'automatic Creative Radar synchronization should be owned by the backend rather than a page polling request',
  );
  assertEqual(elements['batch-sync-creative-radar-btn'].textContent, '同步创意雷达系统', 'Creative Radar button text should reset after completion');

  const startsBeforeQueueTest = requests.filter(request => request.method === 'POST' && request.url.endsWith('/batch_start')).length;
  await Promise.all([
    sandbox.__batch_download_selected__({
      videos: manager.videos.slice(),
      ossState: { enabled: true },
      exportRecordId: 'queue-export-1',
    }),
    sandbox.__batch_download_selected__({
      videos: manager.videos.slice(),
      ossState: { enabled: true },
      exportRecordId: 'queue-export-2',
    }),
  ]);
  const queueStarts = requests.filter(request => request.method === 'POST' && request.url.endsWith('/batch_start')).slice(startsBeforeQueueTest);
  assertEqual(queueStarts.length, 2, 'a second batch click should be submitted even while the first click is still active');
  assertEqual(manager.activeBatchRequestSequence, 0, 'only the newest batch should own and reset the shared progress UI');
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
