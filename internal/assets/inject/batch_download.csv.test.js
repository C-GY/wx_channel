const fs = require('fs');
const path = require('path');
const vm = require('vm');

function loadBatchDownloadModule() {
  const file = path.resolve(__dirname, 'batch_download.js');
  const source = fs.readFileSync(file, 'utf8');

  class SandboxURL extends URL {}
  SandboxURL.createObjectURL = function () { return 'blob:test'; };
  SandboxURL.revokeObjectURL = function () {};

  const sandbox = {
    console: { log() {}, error() {}, warn() {} },
    window: {},
    document: {
      body: { appendChild() {} },
      createElement() {
        return {
          style: {},
          addEventListener() {},
          appendChild() {},
          click() {},
          remove() {},
        };
      },
      getElementById() { return null; },
      querySelector() { return null; },
    },
    location: {
      href: 'https://channels.weixin.qq.com/web/pages/profile',
      origin: 'https://channels.weixin.qq.com',
      pathname: '/web/pages/profile',
    },
    navigator: { userAgent: 'node-test' },
    Blob: function Blob() {},
    URL: SandboxURL,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    Date,
    AbortController,
    confirm() { return true; },
    fetch() {
      throw new Error('fetch should not be called in CSV tests');
    },
    __wx_log() {},
  };

  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox, { filename: file });
  return sandbox;
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}\nactual:   ${actual}\nexpected: ${expected}`);
  }
}

function valueByKey(sandbox, row, name) {
  const index = Array.from(sandbox.__wx_batch_csv_keys__).indexOf(name);
  assert(index >= 0, `missing CSV key: ${name}`);
  return row[index];
}

function main() {
  const sandbox = loadBatchDownloadModule();
  const manager = sandbox.__wx_batch_download_manager__;

  manager.setVideos([{ id: 'one' }, { id: 'two' }], 'test');
  assert(/T.*Z$/.test(manager.videos[0].capturedAt), 'setVideos should stamp capturedAt');
  manager.toggleSelect('two', true);
  const selected = sandbox.__get_batch_export_videos__();
  assertEqual(selected.length, 1, 'export should use selected videos when selection exists');
  assertEqual(selected[0].id, 'two', 'export should preserve the selected video');

  const capturedAt = '2026-07-16T09:00:00.000Z';
  const video = {
    id: '14966390340339567064',
    objectNonceId: 'nonce-1',
    title: '标题,含"引号"',
    nickname: '作者',
    username: 'author@finder',
    type: 'media',
    createtime: 1784179489,
    capturedAt,
    ossVideoUrl: 'https://signed.example/video.mp4?token=temporary',
    url: 'https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc123&hy=SH&idx=1&token=tok456&sign=sig789',
    coverUrl: 'https://finder.video.qq.com/cover.jpg',
    key: '1139646746',
    duration: 17534,
    size: 3778766,
    width: 576,
    height: 1024,
    likeCount: 0,
    commentCount: 164,
    favCount: 747,
    forwardCount: 100,
    spec: [{ bypass: '{"cgi_id":6624}', width: 576, height: 1024 }],
  };

  const rows = sandbox.__build_batch_csv_rows__([video], 'fallback');
  const row = rows[0];
  const expectedKeys = [
    'id',
    'title',
    'nickname',
    'createTime',
    'videoUrl',
    'coverUrl',
    'duration',
    'sizeMB',
    'likeCount',
    'commentCount',
    'favCount',
    'forwardCount',
    'capturedAt',
  ];
  const expectedHeaders = [
    '视频ID',
    '视频标题',
    '作者昵称',
    '发布时间',
    '视频链接（OSS地址）',
    '视频封面链接',
    '视频时长',
    '文件大小',
    '点赞数',
    '评论数',
    '收藏数',
    '转发数',
    '数据采集时间',
  ];
  assertEqual(JSON.stringify(Array.from(sandbox.__wx_batch_csv_keys__)), JSON.stringify(expectedKeys), 'CSV field order should match the requested order');
  assertEqual(JSON.stringify(Array.from(sandbox.__wx_batch_csv_headers__)), JSON.stringify(expectedHeaders), 'CSV headers should match the requested Chinese fields');
  assertEqual(row.length, 13, 'CSV should contain exactly 13 fields');

  assertEqual(valueByKey(sandbox, row, 'id'), video.id, 'CSV should preserve the video ID');
  assertEqual(valueByKey(sandbox, row, 'title'), video.title, 'CSV should preserve the title');
  assertEqual(valueByKey(sandbox, row, 'nickname'), video.nickname, 'CSV should preserve the author nickname');
  assertEqual(valueByKey(sandbox, row, 'videoUrl'), video.ossVideoUrl, 'CSV should use the OSS address as the video link');
  assertEqual(valueByKey(sandbox, row, 'coverUrl'), video.coverUrl, 'CSV should include the cover URL');
  assertEqual(valueByKey(sandbox, row, 'duration'), '0:17', 'CSV should format milliseconds as minute:second');
  assertEqual(valueByKey(sandbox, row, 'sizeMB'), '3.60 MB', 'CSV should format bytes as MB with two decimals');
  assertEqual(valueByKey(sandbox, row, 'likeCount'), 747, 'CSV 点赞数 should use the source favCount value');
  assertEqual(valueByKey(sandbox, row, 'commentCount'), 164, 'CSV should include commentCount');
  assertEqual(valueByKey(sandbox, row, 'favCount'), 0, 'CSV 收藏数 should use the source likeCount value and preserve zero');
  assertEqual(valueByKey(sandbox, row, 'forwardCount'), 100, 'CSV should include forwardCount');
  assert(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(valueByKey(sandbox, row, 'createTime')), 'publish time should use YYYY-MM-DD HH:mm:ss');
  assert(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(valueByKey(sandbox, row, 'capturedAt')), 'capturedAt should use YYYY-MM-DD HH:mm:ss');
  assertEqual(
    sandbox.__format_batch_csv_captured_at__('2026-07-17 15:23:25'),
    '2026-07-17 15:23:25',
    'a local capturedAt value should not be shifted by a timezone conversion',
  );
  assertEqual(sandbox.__format_batch_csv_duration__(211000), '3:31', 'duration should match the requested 3:31 example');
  assertEqual(sandbox.__format_batch_csv_size__('', '194.61 MB'), '194.61 MB', 'size should match the requested MB example');

  const nestedRows = sandbox.__build_batch_csv_rows__([{
    id: 'nested',
    objectExtend: {
      monotonicData: {
        countInfo: {
          likeCount: 9,
          commentCount: 8,
          favCount: 7,
          forwardCount: 6,
        },
      },
    },
  }], capturedAt);
  assertEqual(valueByKey(sandbox, nestedRows[0], 'likeCount'), 7, 'CSV should swap nested like/favorite counts too');
  assertEqual(valueByKey(sandbox, nestedRows[0], 'favCount'), 9, 'CSV should use nested likeCount as 收藏数');
  assertEqual(valueByKey(sandbox, nestedRows[0], 'forwardCount'), 6, 'CSV should include nested forwardCount');

  const csv = sandbox.__build_batch_csv_content__([video], capturedAt);
  assert(csv.includes('"标题,含""引号"""'), 'CSV should escape commas and quotes');
  assert(csv.startsWith('"视频ID","视频标题","作者昵称","发布时间","视频链接（OSS地址）"'), 'CSV should export fields in the requested order');
  assertEqual(
    sandbox.__escape_batch_csv_cell__('=1+1'),
    '"\'=1+1"',
    'CSV should neutralize spreadsheet formulas',
  );

  const originalRows = sandbox.__build_batch_csv_rows__([video], capturedAt, false);
  assertEqual(valueByKey(sandbox, originalRows[0], 'videoUrl'), video.url, 'non-OSS CSV should use the original video URL');
  const originalCSV = sandbox.__build_batch_csv_content__([video], capturedAt, false);
  assert(originalCSV.startsWith('"视频ID","视频标题","作者昵称","发布时间","视频链接（原始地址）"'), 'non-OSS CSV should use the dynamic original-address header');

  const recordItems = sandbox.__build_batch_export_record_items__([video], '2026-07-17 15:23:25');
  assertEqual(recordItems[0].capturedAt, sandbox.__format_batch_csv_captured_at__(capturedAt), 'export record should persist the formatted capturedAt value');
  const nestedCoverItems = sandbox.__build_batch_export_record_items__([{
    id: 'nested-cover',
    objectDesc: { media: [{ coverUrl: 'https://finder.video.qq.com/nested-cover.jpg' }] },
  }], capturedAt);
  assertEqual(nestedCoverItems[0].coverUrl, 'https://finder.video.qq.com/nested-cover.jpg', 'export record should retain a nested media cover URL');

  const prepared = sandbox.__prepare_batch_download_entries__([
    video,
    { id: 'live', type: 'live', url: 'https://example.com/live', key: '' },
    { id: 'disabled', type: 'media', canDownload: false, url: 'https://example.com/no', key: '' },
  ]);
  assertEqual(prepared.length, 1, 'OSS export preparation should exclude live and unavailable entries');
  assertEqual(prepared[0].source.id, video.id, 'prepared entry should retain the source record for CSV metadata');
}

main();
