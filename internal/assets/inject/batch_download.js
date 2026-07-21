/**
 * @file 通用批量下载组件
 * 提供统一的视频列表弹窗和批量下载功能
 */
console.log('[batch_download.js] 加载通用批量下载模块');

var __wx_batch_default_oss_access_key_id__ = 'marketing-video-dashboard';
var __wx_batch_default_oss_access_key_secret__ = 'mvd_9XvKp7Qw2Lm8Nz4Rt6Yb3Hs5Fc1Da0';

// 为互动数据快照记录首次进入批量列表的时间。
function __ensure_batch_video_captured_at__(video, capturedAt) {
  if (!video || typeof video !== 'object') return video;
  if (!video.capturedAt) {
    video.capturedAt = capturedAt || new Date().toISOString();
  }
  return video;
}

// ==================== 通用批量下载管理器 ====================
window.__wx_batch_download_manager__ = {
  videos: [], // 当前视频列表
  selectedItems: {}, // 选中的项目 {id: true}
  currentPage: 1,
  pageSize: 50,
  maxItems: 100000, // Gopeed接管后取消限制 (原300)
  isVisible: false,
  title: '视频列表',
  isDownloading: false, // 是否正在下载
  stopSignal: false, // 取消下载信号
  batchRequestSequence: 0,
  activeBatchRequestSequence: 0,
  forceRedownload: false, // 强制重新下载
  ossUploadEnabled: true, // 默认在下载完成后同步上传 OSS
  ossAccessKeyId: __wx_batch_default_oss_access_key_id__, // 默认值或已缓存的 AccessKey ID
  ossSavedAccessKeyId: '',
  ossHasSavedSecret: false,
  ossConfigLoaded: false,
  ossConfigLoadPromise: null,
  abortController: null, // 当前请求的 AbortController

  // 设置视频数据
  setVideos: function (videos, title) {
    var capturedAt = new Date().toISOString();
    this.videos = videos.slice(0, this.maxItems).map(function (video) {
      return __ensure_batch_video_captured_at__(video, capturedAt);
    });
    this.selectedItems = {};
    this.currentPage = 1;
    if (title) this.title = title;
    console.log('[批量下载] 设置视频数据，共', this.videos.length, '个');
  },

  // 追加视频数据（去重）
  appendVideos: function (videos) {
    var existingIds = {};
    this.videos.forEach(function (v) {
      existingIds[v.id] = true;
    });

    var newCount = 0;
    var capturedAt = new Date().toISOString();
    for (var i = 0; i < videos.length && this.videos.length < this.maxItems; i++) {
      var video = __ensure_batch_video_captured_at__(videos[i], capturedAt);
      if (video.id && !existingIds[video.id]) {
        this.videos.push(video);
        existingIds[video.id] = true;
        newCount++;
      }
    }

    console.log('[批量下载] 追加', newCount, '个视频，总计:', this.videos.length);
    return newCount;
  },

  // 获取当前页的视频
  getCurrentPageVideos: function () {
    var start = (this.currentPage - 1) * this.pageSize;
    var end = start + this.pageSize;
    return this.videos.slice(start, end);
  },

  // 获取总页数
  getTotalPages: function () {
    return Math.ceil(this.videos.length / this.pageSize);
  },

  // 获取选中的视频
  getSelectedVideos: function () {
    var self = this;
    return this.videos.filter(function (video) {
      return self.selectedItems[video.id];
    });
  },

  // 切换选中状态
  toggleSelect: function (videoId, selected) {
    if (selected) {
      this.selectedItems[videoId] = true;
    } else {
      delete this.selectedItems[videoId];
    }
  },

  // 全选当前页
  selectAllCurrentPage: function (selected) {
    var pageVideos = this.getCurrentPageVideos();
    for (var i = 0; i < pageVideos.length; i++) {
      this.toggleSelect(pageVideos[i].id, selected);
    }
  }
};

function __wx_channels_batch_api_headers__() {
  var headers = { 'Content-Type': 'application/json' };
  if (window.__WX_LOCAL_TOKEN__) {
    headers['X-Local-Auth'] = window.__WX_LOCAL_TOKEN__;
  }
  return headers;
}

function __set_batch_oss_status__(message, isError) {
  var status = document.getElementById('batch-oss-config-status');
  if (!status) return;
  status.textContent = message || '';
  status.style.color = isError ? '#fa5151' : '#07c160';
}

function __set_batch_oss_panel_visible__(visible) {
  __wx_batch_download_manager__.ossUploadEnabled = !!visible;
  var panel = document.getElementById('batch-oss-config-panel');
  if (panel) panel.style.display = visible ? 'block' : 'none';
  var checkbox = document.getElementById('batch-oss-upload-enabled');
  if (checkbox) checkbox.checked = !!visible;
}

function __batch_oss_response_error__(result, fallbackMessage) {
  if (!result) return fallbackMessage;
  return result.error || result.message || fallbackMessage;
}

// 从本地后端读取缓存配置。后端只返回是否已保存 Secret；输入框中预置的
// 默认 Secret 会保留，保存后仍会清空并改用后端缓存。
async function __load_batch_oss_config__() {
  var accessKeyIdInput = document.getElementById('batch-oss-access-key-id');
  var accessKeySecretInput = document.getElementById('batch-oss-access-key-secret');
  if (!accessKeyIdInput || !accessKeySecretInput) return;
  var initialAccessKeyId = accessKeyIdInput.value.trim();

  __wx_batch_download_manager__.ossConfigLoaded = false;
  __set_batch_oss_status__('正在读取本地 OSS 配置...', false);
  try {
    var response = await fetch('/__wx_channels_api/oss_config', {
      method: 'GET',
      headers: __wx_channels_batch_api_headers__()
    });
    var result = await response.json();
    if (!response.ok || (typeof result.code === 'number' && result.code !== 0)) {
      throw new Error(__batch_oss_response_error__(result, '读取 OSS 配置失败'));
    }

    var data = result.data || result;
    var accessKeyId = String(data.accessKeyId || '').trim();
    var effectiveAccessKeyId = accessKeyId || __wx_batch_default_oss_access_key_id__;
    var hasSecret = !!data.hasSecret;
    __wx_batch_download_manager__.ossAccessKeyId = effectiveAccessKeyId;
    __wx_batch_download_manager__.ossSavedAccessKeyId = accessKeyId;
    __wx_batch_download_manager__.ossHasSavedSecret = hasSecret;
    __wx_batch_download_manager__.ossConfigLoaded = true;
    // 本地读取期间用户可能已开始输入，不能用旧缓存覆盖新输入。
    var currentAccessKeyId = accessKeyIdInput.value.trim();
    var initialValueWasDefault = !initialAccessKeyId ||
      initialAccessKeyId === __wx_batch_default_oss_access_key_id__;
    if (!currentAccessKeyId ||
        (initialValueWasDefault && currentAccessKeyId === initialAccessKeyId)) {
      accessKeyIdInput.value = effectiveAccessKeyId;
    }
    accessKeySecretInput.placeholder = hasSecret
      ? '已保存在本地，留空即可沿用'
      : '请输入 OSS_ACCESS_KEY_SECRET';
    __set_batch_oss_status__(accessKeyId && hasSecret ? '已加载本地保存的 OSS 配置' : '', false);
  } catch (err) {
    __wx_batch_download_manager__.ossConfigLoaded = true;
    __set_batch_oss_status__('读取失败：' + (err.message || err), true);
    console.warn('[批量下载] 读取 OSS 配置失败:', err);
  }
}

// 勾选 OSS 时保存配置。Secret 输入框留空代表继续使用后端已缓存的 Secret。
async function __persist_batch_oss_config_if_needed__() {
  var checkbox = document.getElementById('batch-oss-upload-enabled');
  if (!checkbox || !checkbox.checked) {
    __wx_batch_download_manager__.ossUploadEnabled = false;
    return { enabled: false };
  }

  __wx_batch_download_manager__.ossUploadEnabled = true;
  if (!__wx_batch_download_manager__.ossConfigLoaded &&
      __wx_batch_download_manager__.ossConfigLoadPromise) {
    await __wx_batch_download_manager__.ossConfigLoadPromise;
  }
  var accessKeyIdInput = document.getElementById('batch-oss-access-key-id');
  var accessKeySecretInput = document.getElementById('batch-oss-access-key-secret');
  var accessKeyId = accessKeyIdInput ? accessKeyIdInput.value.trim() : '';
  var accessKeySecret = accessKeySecretInput ? accessKeySecretInput.value.trim() : '';

  if (!accessKeyId) {
    __set_batch_oss_status__('请填写 OSS_ACCESS_KEY_ID', true);
    if (accessKeyIdInput) accessKeyIdInput.focus();
    return null;
  }
  var canReuseSavedSecret = __wx_batch_download_manager__.ossHasSavedSecret &&
    accessKeyId === __wx_batch_download_manager__.ossSavedAccessKeyId;
  if (!accessKeySecret && !canReuseSavedSecret) {
    __set_batch_oss_status__('请填写 OSS_ACCESS_KEY_SECRET', true);
    if (accessKeySecretInput) accessKeySecretInput.focus();
    return null;
  }

  __set_batch_oss_status__('正在保存 OSS 配置...', false);
  try {
    var response = await fetch('/__wx_channels_api/oss_config', {
      method: 'POST',
      headers: __wx_channels_batch_api_headers__(),
      body: JSON.stringify({
        accessKeyId: accessKeyId,
        accessKeySecret: accessKeySecret
      })
    });
    var result = await response.json();
    if (!response.ok || (typeof result.code === 'number' && result.code !== 0)) {
      throw new Error(__batch_oss_response_error__(result, '保存 OSS 配置失败'));
    }

    __wx_batch_download_manager__.ossAccessKeyId = accessKeyId;
    __wx_batch_download_manager__.ossSavedAccessKeyId = accessKeyId;
    __wx_batch_download_manager__.ossHasSavedSecret = true;
    __wx_batch_download_manager__.ossConfigLoaded = true;
    if (accessKeySecretInput) {
      accessKeySecretInput.value = '';
      accessKeySecretInput.placeholder = '已保存在本地，留空即可沿用';
    }
    __set_batch_oss_status__('OSS 配置已保存到本地', false);
    return { enabled: true };
  } catch (err) {
    __set_batch_oss_status__('保存失败：' + (err.message || err), true);
    __wx_log({ msg: '❌ OSS 配置保存失败：' + (err.message || err) });
    return null;
  }
}

async function __clear_batch_oss_config__() {
  var clearButton = document.getElementById('batch-oss-clear-config');
  if (clearButton) clearButton.disabled = true;
  __set_batch_oss_status__('正在清除 OSS 配置...', false);
  try {
    var response = await fetch('/__wx_channels_api/oss_config', {
      method: 'DELETE',
      headers: __wx_channels_batch_api_headers__()
    });
    var result = await response.json();
    if (!response.ok || (typeof result.code === 'number' && result.code !== 0)) {
      throw new Error(__batch_oss_response_error__(result, '清除 OSS 配置失败'));
    }

    __wx_batch_download_manager__.ossAccessKeyId = __wx_batch_default_oss_access_key_id__;
    __wx_batch_download_manager__.ossSavedAccessKeyId = '';
    __wx_batch_download_manager__.ossHasSavedSecret = false;
    __wx_batch_download_manager__.ossConfigLoaded = true;
    var accessKeyIdInput = document.getElementById('batch-oss-access-key-id');
    var accessKeySecretInput = document.getElementById('batch-oss-access-key-secret');
    if (accessKeyIdInput) accessKeyIdInput.value = __wx_batch_default_oss_access_key_id__;
    if (accessKeySecretInput) {
      accessKeySecretInput.value = __wx_batch_default_oss_access_key_secret__;
      accessKeySecretInput.placeholder = '请输入 OSS_ACCESS_KEY_SECRET';
    }
    __set_batch_oss_status__('OSS 配置已清除', false);
    __wx_log({ msg: '🧹 已清除本地 OSS 配置' });
  } catch (err) {
    __set_batch_oss_status__('清除失败：' + (err.message || err), true);
    __wx_log({ msg: '❌ OSS 配置清除失败：' + (err.message || err) });
  } finally {
    if (clearButton) clearButton.disabled = false;
  }
}

function __merge_batch_oss_task_results__(tasks) {
  if (!Array.isArray(tasks) || tasks.length === 0) return 0;
  var videosById = {};
  __wx_batch_download_manager__.videos.forEach(function (video) {
    if (video && video.id !== undefined && video.id !== null) {
      videosById[String(video.id)] = video;
    }
  });

  var readyCount = 0;
  tasks.forEach(function (task) {
    if (!task || task.id === undefined || task.id === null) return;
    var video = videosById[String(task.id)];
    if (!video) return;
    video.ossUploadStatus = task.ossStatus || '';
    video.ossObjectKey = task.ossObjectKey || '';
    video.ossUploadError = task.ossError || '';
    if (task.ossUrl) {
      video.ossVideoUrl = task.ossUrl;
      readyCount++;
    } else if (Object.prototype.hasOwnProperty.call(task, 'ossUploadEnabled')) {
      video.ossVideoUrl = '';
    }
  });
  return readyCount;
}

async function __refresh_batch_oss_task_results__() {
  try {
    var response = await fetch('/__wx_channels_api/batch_progress', {
      method: 'POST',
      headers: __wx_channels_batch_api_headers__()
    });
    if (!response.ok) return 0;
    var result = await response.json();
    if (typeof result.code === 'number' && result.code !== 0) return 0;
    var data = result.data || result;
    return __merge_batch_oss_task_results__(data.tasks || []);
  } catch (err) {
    console.warn('[批量下载] 读取 OSS 上传结果失败:', err);
    return 0;
  }
}

// ==================== 显示批量下载弹窗 ====================
function __show_batch_download_ui__(videos, title) {
  if (!videos || videos.length === 0) {
    __wx_log({ msg: '❌ 暂无视频数据' });
    return;
  }

  // 设置数据
  __wx_batch_download_manager__.setVideos(videos, title || '视频列表');

  // 移除已存在的弹窗
  var existingUI = document.getElementById('wx-batch-download-ui');
  if (existingUI) existingUI.remove();

  // 创建弹窗
  var ui = document.createElement('div');
  ui.id = 'wx-batch-download-ui';
  ui.style.cssText = 'position:fixed;top:60px;right:20px;background:#2b2b2b;color:#e5e5e5;padding:0;border-radius:8px;z-index:99999;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;font-size:14px;width:450px;max-height:80vh;box-shadow:0 8px 24px rgba(0,0,0,0.5);overflow-y:auto;overflow-x:hidden;';

  // 统计视频和直播数量
  var videoCount = 0;
  var liveCount = 0;
  videos.forEach(function (v) {
    if (v.type === 'live' || v.type === 'live_replay') {
      liveCount++;
    } else if (v.type === 'media' || !v.type) {
      videoCount++;
    }
  });

  // 根据页面类型构建统计文本
  var statsText = '';
  var currentPath = window.location.pathname;

  if (currentPath.includes('/pages/home')) {
    // Home页：显示"X 个视频"
    statsText = videoCount + ' 个视频';
    if (liveCount > 0) {
      statsText += ', ' + liveCount + ' 个直播';
    }
  } else if (currentPath.includes('/pages/profile')) {
    // Profile页：显示"X 个视频, Y 个直播回放"
    if (liveCount > 0) {
      statsText = videoCount + ' 个视频, ' + liveCount + ' 个直播回放';
    } else {
      statsText = videoCount + ' 个视频';
    }
  } else {
    // 其他页面：默认显示
    if (liveCount > 0) {
      statsText = videoCount + ' 个视频, ' + liveCount + ' 个直播';
    } else {
      statsText = videos.length + ' 个';
    }
  }

  ui.innerHTML =
    // 标题栏
    '<div style="padding:16px 20px;border-bottom:1px solid rgba(255,255,255,0.08);display:flex;justify-content:space-between;align-items:center;">' +
    '<div style="font-size:15px;font-weight:500;color:#fff;">' + __wx_batch_download_manager__.title + '</div>' +
    '<div style="display:flex;align-items:center;gap:12px;">' +
    '<div id="batch-total-count" style="font-size:13px;color:#999;">' + statsText + '</div>' +
    '<div id="batch-close-icon" style="cursor:pointer;color:#999;font-size:20px;line-height:1;padding:4px;" title="关闭">×</div>' +
    '</div>' +
    '</div>' +

    // 列表区域
    '<div id="batch-list-container" style="overflow-y:auto;padding:12px 20px;max-height:200px;">' +
    '<div id="batch-list" style="display:flex;flex-direction:column;gap:8px;"></div>' +
    '</div>' +

    // 分页
    '<div id="batch-pagination" style="padding:12px 20px;border-top:1px solid rgba(255,255,255,0.08);border-bottom:1px solid rgba(255,255,255,0.08);display:flex;justify-content:space-between;align-items:center;">' +
    '<div style="font-size:13px;color:#999;">第 <span id="batch-current-page">1</span> / <span id="batch-total-pages">1</span> 页</div>' +
    '<div style="display:flex;gap:8px;">' +
    '<button id="batch-prev-page" style="background:rgba(255,255,255,0.08);color:#999;border:none;padding:4px 12px;border-radius:4px;cursor:pointer;font-size:13px;">上一页</button>' +
    '<button id="batch-next-page" style="background:rgba(255,255,255,0.08);color:#999;border:none;padding:4px 12px;border-radius:4px;cursor:pointer;font-size:13px;">下一页</button>' +
    '</div>' +
    '</div>' +

    // 操作区
    '<div style="padding:16px 20px;">' +
    '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px;">' +
    '<label style="display:flex;align-items:center;cursor:pointer;font-size:13px;color:#999;user-select:none;">' +
    '<input type="checkbox" id="batch-select-all" style="margin-right:8px;cursor:pointer;" />' +
    '<span>全选当前页</span>' +
    '</label>' +
    '<span id="batch-selected-count" style="font-size:13px;color:#07c160;">已选 0 个</span>' +
    '</div>' +

    // 下载和取消按钮容器
    '<div style="display:flex;gap:8px;margin-bottom:12px;">' +
    '<button id="batch-download-btn" style="flex:1;background:#07c160;color:#fff;border:none;padding:8px 12px;border-radius:6px;cursor:pointer;font-size:14px;font-weight:500;transition:background 0.2s;">开始下载</button>' +
    '<button id="batch-cancel-btn" style="flex:0 0 25%;background:#fa5151;color:#fff;border:none;padding:8px 12px;border-radius:6px;cursor:pointer;font-size:14px;font-weight:500;display:none;">取消</button>' +
    '</div>' +

    // 下载进度
    '<div id="batch-download-progress" style="display:none;margin-bottom:12px;">' +
    '<div style="display:flex;justify-content:space-between;margin-bottom:8px;font-size:13px;color:#999;">' +
    '<span>下载进度</span>' +
    '<span id="batch-progress-text">0/0</span>' +
    '</div>' +
    '<div style="background:rgba(255,255,255,0.08);height:6px;border-radius:3px;overflow:hidden;">' +
    '<div id="batch-progress-bar" style="background:#07c160;height:100%;width:0%;border-radius:3px;transition:width 0.3s;"></div>' +
    '</div>' +
    '</div>' +

    // 强制重新下载选项
    '<label style="display:flex;align-items:center;cursor:pointer;font-size:13px;color:#999;user-select:none;">' +
    '<input type="checkbox" id="batch-force-redownload" style="margin-right:8px;cursor:pointer;" />' +
    '<span>强制重新下载</span>' +
    '</label>' +

    // OSS 同步上传选项
    '<label style="display:flex;align-items:center;cursor:pointer;font-size:13px;color:#999;user-select:none;margin-top:10px;">' +
    '<input type="checkbox" id="batch-oss-upload-enabled" style="margin-right:8px;cursor:pointer;" checked />' +
    '<span>视频同步上传OSS</span>' +
    '</label>' +
    '<div id="batch-oss-config-panel" style="display:none;margin-top:10px;padding:12px;background:rgba(255,255,255,0.04);border:1px solid rgba(255,255,255,0.08);border-radius:6px;">' +
    '<label for="batch-oss-access-key-id" style="display:block;font-size:12px;color:#aaa;margin-bottom:5px;">OSS_ACCESS_KEY_ID</label>' +
    '<input type="text" id="batch-oss-access-key-id" maxlength="256" autocomplete="off" spellcheck="false" value="marketing-video-dashboard" style="box-sizing:border-box;width:100%;margin-bottom:10px;background:#202020;color:#fff;border:1px solid rgba(255,255,255,0.14);padding:7px 9px;border-radius:4px;font-size:12px;outline:none;" placeholder="请输入 OSS_ACCESS_KEY_ID" />' +
    '<label for="batch-oss-access-key-secret" style="display:block;font-size:12px;color:#aaa;margin-bottom:5px;">OSS_ACCESS_KEY_SECRET</label>' +
    '<input type="password" id="batch-oss-access-key-secret" maxlength="1024" autocomplete="new-password" spellcheck="false" value="' + __wx_batch_default_oss_access_key_secret__ + '" style="box-sizing:border-box;width:100%;margin-bottom:10px;background:#202020;color:#fff;border:1px solid rgba(255,255,255,0.14);padding:7px 9px;border-radius:4px;font-size:12px;outline:none;" placeholder="请输入 OSS_ACCESS_KEY_SECRET" />' +
    '<div style="font-size:11px;line-height:1.5;color:#777;margin-bottom:9px;">开始下载后会同步上传；导出CSV会保存配置，并写入已完成的OSS视频地址。</div>' +
    '<div style="display:flex;align-items:center;justify-content:space-between;gap:8px;">' +
    '<span id="batch-oss-config-status" style="flex:1;font-size:11px;line-height:1.4;color:#07c160;"></span>' +
    '<button type="button" id="batch-oss-clear-config" style="flex:none;background:transparent;color:#fa5151;border:1px solid rgba(250,81,81,0.45);padding:5px 9px;border-radius:4px;cursor:pointer;font-size:11px;">清除OSS配置</button>' +
    '</div>' +
    '</div>' +
    '<button type="button" id="batch-sync-creative-radar-btn" style="box-sizing:border-box;width:100%;margin-top:12px;background:#07c160;color:#fff;border:none;padding:8px 12px;border-radius:6px;cursor:pointer;font-size:14px;font-weight:500;transition:background 0.2s;">同步创意雷达系统</button>' +
    '</div>' +

    // 次要操作区
    '<div style="padding:12px 20px;border-top:1px solid rgba(255,255,255,0.08);display:flex;gap:8px;">' +
    '<button id="batch-export-btn" title="有勾选项时导出勾选项，否则导出全部" style="flex:1;background:transparent;color:#999;border:1px solid rgba(255,255,255,0.12);padding:8px 12px;border-radius:6px;cursor:pointer;font-size:13px;transition:all 0.2s;">导出 JSON</button>' +
    '<button id="batch-export-csv-btn" title="导出视频信息及点赞、评论、收藏、转发数据" style="flex:1;background:transparent;color:#999;border:1px solid rgba(255,255,255,0.12);padding:8px 12px;border-radius:6px;cursor:pointer;font-size:13px;transition:all 0.2s;">导出 CSV</button>' +
    '<button id="batch-clear-btn" style="flex:1;background:transparent;color:#999;border:1px solid rgba(255,255,255,0.12);padding:8px 12px;border-radius:6px;cursor:pointer;font-size:13px;transition:all 0.2s;">清空列表</button>' +
    '</div>';

  document.body.appendChild(ui);

  __wx_batch_download_manager__.isVisible = true;

  // 渲染列表
  __render_batch_video_list__();

  // 绑定事件
  setTimeout(function () {
    // 分页
    document.getElementById('batch-prev-page').onclick = function () {
      if (__wx_batch_download_manager__.currentPage > 1) {
        __wx_batch_download_manager__.currentPage--;
        __render_batch_video_list__();
      }
    };

    document.getElementById('batch-next-page').onclick = function () {
      if (__wx_batch_download_manager__.currentPage < __wx_batch_download_manager__.getTotalPages()) {
        __wx_batch_download_manager__.currentPage++;
        __render_batch_video_list__();
      }
    };

    // 全选
    document.getElementById('batch-select-all').onchange = function () {
      __wx_batch_download_manager__.selectAllCurrentPage(this.checked);
      __render_batch_video_list__();
    };

    // 下载
    document.getElementById('batch-download-btn').onclick = function () {
      __batch_download_selected__();
    };

    // 取消下载
    document.getElementById('batch-cancel-btn').onclick = function () {
      __cancel_batch_download__();
    };

    // 强制重新下载
    document.getElementById('batch-force-redownload').onchange = function () {
      __wx_batch_download_manager__.forceRedownload = this.checked;
    };

    // OSS 同步上传与本地配置
    var ossUploadCheckbox = document.getElementById('batch-oss-upload-enabled');
    if (ossUploadCheckbox) {
      ossUploadCheckbox.checked = __wx_batch_download_manager__.ossUploadEnabled;
      ossUploadCheckbox.onchange = function () {
        __set_batch_oss_panel_visible__(this.checked);
      };
      __set_batch_oss_panel_visible__(ossUploadCheckbox.checked);
    }
    var clearOSSConfigBtn = document.getElementById('batch-oss-clear-config');
    if (clearOSSConfigBtn) {
      clearOSSConfigBtn.onclick = function () {
        __clear_batch_oss_config__();
      };
    }
    __wx_batch_download_manager__.ossConfigLoadPromise = __load_batch_oss_config__();

    var syncCreativeRadarBtn = document.getElementById('batch-sync-creative-radar-btn');
    if (syncCreativeRadarBtn) {
      syncCreativeRadarBtn.onclick = function () {
        __export_batch_video_csv__({
          autoSyncCreativeRadar: true,
          actionButton: syncCreativeRadarBtn
        });
      };
    }

    // 导出列表
    var exportBtn = document.getElementById('batch-export-btn');
    if (exportBtn) {
      exportBtn.addEventListener('mouseenter', function () {
        this.style.background = 'rgba(255,255,255,0.08)';
        this.style.color = '#fff';
      });
      exportBtn.addEventListener('mouseleave', function () {
        this.style.background = 'transparent';
        this.style.color = '#999';
      });
      exportBtn.addEventListener('click', function () {
        __export_batch_video_list__();
      });
    }

    // 导出 CSV（互动数据快照）
    var exportCsvBtn = document.getElementById('batch-export-csv-btn');
    if (exportCsvBtn) {
      exportCsvBtn.addEventListener('mouseenter', function () {
        this.style.background = 'rgba(255,255,255,0.08)';
        this.style.color = '#fff';
      });
      exportCsvBtn.addEventListener('mouseleave', function () {
        this.style.background = 'transparent';
        this.style.color = '#999';
      });
      exportCsvBtn.addEventListener('click', function () {
        __export_batch_video_csv__();
      });
    }

    // 清空列表
    var clearBtn = document.getElementById('batch-clear-btn');
    if (clearBtn) {
      clearBtn.addEventListener('mouseenter', function () {
        this.style.background = 'rgba(255,255,255,0.08)';
        this.style.color = '#fff';
      });
      clearBtn.addEventListener('mouseleave', function () {
        this.style.background = 'transparent';
        this.style.color = '#999';
      });
      clearBtn.addEventListener('click', function () {
        __clear_batch_video_list__();
      });
    }

    // 关闭
    document.getElementById('batch-close-icon').onclick = function () {
      __close_batch_download_ui__();
    };

    // 监听实时进度更新
    document.removeEventListener('wx_download_progress', __handle_download_progress__); // 防止重复绑定
    document.addEventListener('wx_download_progress', __handle_download_progress__);
  }, 100);
}

// ==================== 处理进度更新 ====================
function __handle_download_progress__(e) {
  var data = e.detail;
  if (!data) return;

  // 仅在批量下载UI显示时更新
  if (!__wx_batch_download_manager__.isVisible || !__wx_batch_download_manager__.isDownloading) return;

  var progressText = document.getElementById('batch-progress-text');
  var progressBar = document.getElementById('batch-progress-bar');

  if (progressText && progressBar && data.percentage > 0) {
    // 获取当前处理索引（从文本解析或通过其他方式）
    // 这里简单地在当前文本后追加百分比
    // data.total 是单个文件的总大小，不是批量任务的总数
    // 我们可以显示 "1/5 (45%)"

    // 尝试读取当前的进度文本 "1/5"
    var currentText = progressText.textContent.split(' ')[0]; // 取第一部分 n/m
    if (currentText && currentText.includes('/')) {
      var details = data.percentage.toFixed(1) + '%';
      if (data.total > 0) {
        var downMB = (data.downloaded / (1024 * 1024)).toFixed(1);
        var totalMB = (data.total / (1024 * 1024)).toFixed(1);
        details += ' ' + downMB + '/' + totalMB + ' MB';
      }
      progressText.textContent = currentText + ' (' + details + ')';
    }

    // 更新进度条宽度
    progressBar.style.width = data.percentage + '%';
  }
}

// ==================== 关闭弹窗 ====================
function __close_batch_download_ui__() {
  var ui = document.getElementById('wx-batch-download-ui');
  if (ui) ui.remove();
  __wx_batch_download_manager__.isVisible = false;
}

// ==================== 取消下载 ====================
function __cancel_batch_download__() {
  if (__wx_batch_download_manager__.isDownloading) {
    __wx_batch_download_manager__.stopSignal = true;
    __wx_log({ msg: '⏹️ 正在取消下载...' });

    var cancelBtn = document.getElementById('batch-cancel-btn');
    if (cancelBtn) {
      cancelBtn.textContent = '取消中...';
      cancelBtn.disabled = true;
    }

    // 立即终止当前请求
    if (__wx_batch_download_manager__.abortController) {
      try {
        __wx_batch_download_manager__.abortController.abort();
        console.log('[批量下载] 已触发 HTTP 请求中断');
      } catch (e) {
        console.warn('[批量下载] 中断请求失败:', e);
      }
    }
  }
}

// ==================== 导出视频列表 ====================
function __get_batch_export_videos__() {
  var selectedVideos = __wx_batch_download_manager__.getSelectedVideos();
  if (selectedVideos.length > 0) {
    return selectedVideos;
  }
  return __wx_batch_download_manager__.videos.slice();
}

function __get_batch_export_scope_label__() {
  return __wx_batch_download_manager__.getSelectedVideos().length > 0 ? '勾选' : '全部';
}

// 统一筛选并格式化可下载视频，确保 OSS 导出记录和实际批量任务使用完全相同的视频集合。
function __prepare_batch_download_entries__(videos) {
  var entries = [];
  var sourceVideos = Array.isArray(videos) ? videos : [];

  for (var i = 0; i < sourceVideos.length; i++) {
    var video = sourceVideos[i];
    if (!video || video.canDownload === false || video.type === 'live') {
      continue;
    }

    var formatted = null;
    var alreadyFormatted = false;
    if (video.url && video.key !== undefined) {
      formatted = video;
      alreadyFormatted = true;
    } else if (
      video.objectDesc &&
      typeof WXU !== 'undefined' &&
      WXU &&
      typeof WXU.format_feed === 'function'
    ) {
      formatted = WXU.format_feed(video);
    }

    if (formatted && formatted.canDownload !== false && (alreadyFormatted || formatted.type === 'media')) {
      entries.push({ source: video, formatted: formatted });
    }
  }

  return entries;
}

function __extract_batch_source_info__(spec) {
  var info = { cgiId: '', sourceType: '' };

  try {
    if (spec && spec.length > 0 && spec[0].bypass) {
      var bypassStr = spec[0].bypass;
      var cgiMatch = bypassStr.match(/"cgi_id":(\d+)/) || bypassStr.match(/cgi_id:(\d+)/);

      if (cgiMatch) {
        info.cgiId = cgiMatch[1];
        if (info.cgiId === '6638') {
          info.sourceType = 'Home';
        } else if (info.cgiId === '8060') {
          info.sourceType = 'Other';
        } else {
          info.sourceType = 'Unknown_' + info.cgiId;
        }
      }
    }
  } catch (e) {
    console.error('[batch_download.js] 解析 bypass 失败', e);
  }

  return info;
}

function __batch_first_present__(values) {
  for (var i = 0; i < values.length; i++) {
    if (values[i] !== undefined && values[i] !== null && values[i] !== '') {
      return values[i];
    }
  }
  return '';
}

function __get_batch_interaction_count__(video, field) {
  if (video && video[field] !== undefined && video[field] !== null) {
    return video[field];
  }

  var countInfo = video && video.objectExtend && video.objectExtend.monotonicData &&
    video.objectExtend.monotonicData.countInfo;
  if (countInfo && countInfo[field] !== undefined && countInfo[field] !== null) {
    return countInfo[field];
  }

  return '';
}

function __get_batch_csv_columns__(ossUploadEnabled) {
  return [
    { key: 'id', label: '视频ID' },
    { key: 'title', label: '视频标题' },
    { key: 'nickname', label: '作者昵称' },
    { key: 'createTime', label: '发布时间' },
    {
      key: 'videoUrl',
      label: ossUploadEnabled ? '视频链接（OSS地址）' : '视频链接（原始地址）'
    },
    { key: 'coverUrl', label: '视频封面链接' },
    { key: 'duration', label: '视频时长' },
    { key: 'sizeMB', label: '文件大小' },
    { key: 'likeCount', label: '点赞数' },
    { key: 'commentCount', label: '评论数' },
    { key: 'favCount', label: '收藏数' },
    { key: 'forwardCount', label: '转发数' },
    { key: 'capturedAt', label: '数据采集时间' }
  ];
}

// 默认保持 OSS 模式，供旧调用和调试脚本读取；实际导出时按勾选状态动态生成。
var __wx_batch_csv_columns__ = __get_batch_csv_columns__(true);

var __wx_batch_csv_keys__ = __wx_batch_csv_columns__.map(function (column) {
  return column.key;
});

var __wx_batch_csv_headers__ = __wx_batch_csv_columns__.map(function (column) {
  return column.label;
});

function __format_batch_csv_duration__(durationMs) {
  if (durationMs === undefined || durationMs === null || durationMs === '') return '';
  var text = String(durationMs).trim();
  if (/^\d+:\d{1,2}(?::\d{1,2})?$/.test(text)) return text;

  var milliseconds = Number(durationMs);
  if (!isFinite(milliseconds) || milliseconds < 0) return '';
  var totalSeconds = Math.floor(milliseconds / 1000);
  var hours = Math.floor(totalSeconds / 3600);
  var minutes = Math.floor((totalSeconds % 3600) / 60);
  var seconds = totalSeconds % 60;
  var pad = function (value) { return value < 10 ? '0' + value : String(value); };

  if (hours > 0) return hours + ':' + pad(minutes) + ':' + pad(seconds);
  return minutes + ':' + pad(seconds);
}

function __format_batch_csv_size__(sizeBytes, sizeMB) {
  var bytes = Number(sizeBytes);
  if (sizeBytes !== undefined && sizeBytes !== null && sizeBytes !== '' && isFinite(bytes) && bytes >= 0) {
    return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
  }

  if (sizeMB === undefined || sizeMB === null || sizeMB === '') return '';
  var megabytes = parseFloat(String(sizeMB).replace(/\s*MB\s*$/i, ''));
  if (!isFinite(megabytes) || megabytes < 0) return '';
  return megabytes.toFixed(2) + ' MB';
}

function __format_batch_csv_captured_at__(value) {
  var date = __parse_batch_date__(value);
  if (!date) return '';
  var pad = function (number) { return number < 10 ? '0' + number : String(number); };
  return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate()) + ' ' +
    pad(date.getHours()) + ':' + pad(date.getMinutes()) + ':' + pad(date.getSeconds());
}

function __get_batch_original_video_url__(video) {
  var media = video && video.objectDesc && video.objectDesc.media && video.objectDesc.media[0];
  var mediaURL = media && media.url ? media.url + (media.urlToken || '') : '';
  return __batch_first_present__([
    video && video.originalVideoUrl,
    video && video.videoUrl,
    video && video.url,
    mediaURL
  ]);
}

function __build_batch_csv_rows__(videos, fallbackCapturedAt, ossUploadEnabled) {
  if (ossUploadEnabled === undefined) ossUploadEnabled = true;
  return videos.map(function (v) {
    var media = v.objectDesc && v.objectDesc.media && v.objectDesc.media[0];
    var createtime = __batch_first_present__([v.createtime, v.createTime]);
    var durationMs = __batch_first_present__([
      v.duration,
      media && media.durationMs,
      media && media.spec && media.spec[0] && media.spec[0].durationMs,
      media && media.videoPlayLen !== undefined && media.videoPlayLen !== null
        ? media.videoPlayLen * 1000
        : ''
    ]);
    var sizeBytes = __batch_first_present__([v.size, v.sizeBytes, media && media.fileSize]);

    return [
      __batch_first_present__([v.id]),
      __batch_first_present__([v.title, v.objectDesc && v.objectDesc.description, '无标题']),
      __batch_first_present__([v.nickname, v.contact && v.contact.nickname]),
      __format_batch_create_time__(createtime),
      ossUploadEnabled
        ? __batch_first_present__([v.ossVideoUrl])
        : __get_batch_original_video_url__(v),
      __batch_first_present__([
        v.coverUrl,
        v.thumbUrl,
        v.fullThumbUrl,
        media && media.thumbUrl,
        media && media.coverUrl,
        media && media.fullThumbUrl
      ]),
      __format_batch_csv_duration__(durationMs),
      __format_batch_csv_size__(sizeBytes, v.sizeMB),
      // 视频号主页数据中的 likeCount / favCount 与页面展示语义相反。
      __get_batch_interaction_count__(v, 'favCount'),
      __get_batch_interaction_count__(v, 'commentCount'),
      __get_batch_interaction_count__(v, 'likeCount'),
      __get_batch_interaction_count__(v, 'forwardCount'),
      __format_batch_csv_captured_at__(__batch_first_present__([v.capturedAt, fallbackCapturedAt]))
    ];
  });
}

function __escape_batch_csv_cell__(value) {
  var text = value === undefined || value === null ? '' : String(value);

  // 防止标题、昵称等外部文本在 Excel 中被当作公式执行。
  if (/^[\t\r\n ]*[=+\-@]/.test(text)) {
    text = "'" + text;
  }

  return '"' + text.replace(/"/g, '""') + '"';
}

function __build_batch_csv_content__(videos, fallbackCapturedAt, ossUploadEnabled) {
  if (ossUploadEnabled === undefined) ossUploadEnabled = true;
  var headers = __get_batch_csv_columns__(ossUploadEnabled).map(function (column) {
    return column.label;
  });
  var rows = [headers].concat(__build_batch_csv_rows__(videos, fallbackCapturedAt, ossUploadEnabled));
  return rows.map(function (row) {
    return row.map(__escape_batch_csv_cell__).join(',');
  }).join('\r\n');
}

function __batch_count_as_number__(value) {
  var number = Number(value);
  return isFinite(number) && number >= 0 ? Math.floor(number) : 0;
}

function __build_batch_export_record_items__(videos, fallbackCapturedAt) {
  return videos.map(function (video) {
    var media = video.objectDesc && video.objectDesc.media && video.objectDesc.media[0];
    var durationMs = __batch_first_present__([
      video.duration,
      media && media.durationMs,
      media && media.spec && media.spec[0] && media.spec[0].durationMs,
      media && media.videoPlayLen !== undefined && media.videoPlayLen !== null
        ? media.videoPlayLen * 1000
        : ''
    ]);
    var fileSize = __batch_first_present__([video.size, video.sizeBytes, media && media.fileSize]);
    return {
      videoId: String(__batch_first_present__([video.id])),
      title: __batch_first_present__([video.title, video.objectDesc && video.objectDesc.description, '无标题']),
      author: __batch_first_present__([video.nickname, video.contact && video.contact.nickname]),
      publishTime: __format_batch_create_time__(__batch_first_present__([video.createtime, video.createTime])),
      originalVideoUrl: __get_batch_original_video_url__(video),
      coverUrl: __batch_first_present__([
        video.coverUrl,
        video.thumbUrl,
        video.fullThumbUrl,
        media && media.thumbUrl,
        media && media.coverUrl,
        media && media.fullThumbUrl
      ]),
      durationMs: __batch_count_as_number__(durationMs),
      fileSize: __batch_count_as_number__(fileSize),
      likeCount: __batch_count_as_number__(__get_batch_interaction_count__(video, 'likeCount')),
      commentCount: __batch_count_as_number__(__get_batch_interaction_count__(video, 'commentCount')),
      favCount: __batch_count_as_number__(__get_batch_interaction_count__(video, 'favCount')),
      forwardCount: __batch_count_as_number__(__get_batch_interaction_count__(video, 'forwardCount')),
      capturedAt: __format_batch_csv_captured_at__(__batch_first_present__([video.capturedAt, fallbackCapturedAt]))
    };
  });
}

function __batch_export_filename__(date) {
  var pad = function (value) { return value < 10 ? '0' + value : String(value); };
  return 'batch_videos_' + date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate()) +
    '_' + pad(date.getHours()) + '-' + pad(date.getMinutes()) + '-' + pad(date.getSeconds()) + '.csv';
}

async function __create_batch_export_record__(videos, ossUploadEnabled, exportedAt, autoSyncCreativeRadar) {
  var exportDate = __parse_batch_date__(exportedAt) || new Date();
  var response = await fetch('/api/export-records', {
    method: 'POST',
    headers: __wx_channels_batch_api_headers__(),
    body: JSON.stringify({
      fileName: __batch_export_filename__(exportDate),
      ossUploadEnabled: !!ossUploadEnabled,
      autoSyncCreativeRadar: autoSyncCreativeRadar === true,
      videos: __build_batch_export_record_items__(videos, exportedAt)
    })
  });
  var result = await response.json().catch(function () { return {}; });
  if (!response.ok || (typeof result.code === 'number' && result.code !== 0)) {
    throw new Error(result.message || result.error || '创建导出记录失败');
  }
  return result.data || result;
}

async function __mark_batch_export_failed__(exportRecordId, message) {
  if (!exportRecordId) return;
  try {
    await fetch('/api/export-records/' + encodeURIComponent(exportRecordId) + '/fail', {
      method: 'POST',
      headers: __wx_channels_batch_api_headers__(),
      body: JSON.stringify({ message: message || '启动批量下载失败' })
    });
  } catch (error) {
    console.warn('[批量下载] 标记导出记录失败状态失败:', error);
  }
}

async function __download_batch_export_record_csv__(record) {
  var response = await fetch('/api/export-records/' + encodeURIComponent(record.id) + '/csv', {
    method: 'GET',
    headers: __wx_channels_batch_api_headers__()
  });
  if (!response.ok) {
    var result = await response.json().catch(function () { return {}; });
    throw new Error(result.message || result.error || '下载 CSV 失败');
  }
  var blob = await response.blob();
  var url = URL.createObjectURL(blob);
  var anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = record.fileName || 'batch_videos.csv';
  anchor.click();
  URL.revokeObjectURL(url);
}

function __batch_wait__(milliseconds) {
  return new Promise(function (resolve) { setTimeout(resolve, milliseconds); });
}

async function __get_batch_export_record_detail__(exportRecordId) {
  var response = await fetch('/api/export-records/' + encodeURIComponent(exportRecordId), {
    method: 'GET',
    headers: __wx_channels_batch_api_headers__()
  });
  var result = await response.json().catch(function () { return {}; });
  if (!response.ok || (typeof result.code === 'number' && result.code !== 0)) {
    throw new Error(result.message || result.error || '读取 CSV 生成状态失败');
  }
  var data = result.data || result;
  return data.record || data;
}

async function __wait_batch_export_and_sync_creative_radar__(exportRecordId) {
  var deadline = Date.now() + 2 * 60 * 60 * 1000;
  while (Date.now() < deadline) {
    var record = await __get_batch_export_record_detail__(exportRecordId);
    if (record.status === 'failed') {
      throw new Error(record.errorMessage || 'CSV 生成失败，无法同步创意雷达');
    }
    if (record.creativeRadarSyncStatus === 'success') {
      return record;
    }
    if (record.creativeRadarSyncStatus === 'failed') {
      throw new Error(record.creativeRadarSyncError || '创意雷达同步失败');
    }
    await __batch_wait__(2000);
  }
  throw new Error('等待 CSV 生成并同步创意雷达超时');
}

function __export_batch_video_list__() {
  var videos = __get_batch_export_videos__();

  if (videos.length === 0) {
    __wx_log({ msg: '⚠️ 没有可导出的视频' });
    return;
  }

  // 格式化导出数据
  var exportData = videos.map(function (v) {
    var media = v.objectDesc && v.objectDesc.media && v.objectDesc.media[0];
    var spec = v.spec || (media && media.spec) || [];

    var sourceInfo = __extract_batch_source_info__(spec);

    return {
      id: v.id,
      title: v.title || (v.objectDesc && v.objectDesc.description) || '无标题',
      sourceType: sourceInfo.sourceType,
      cgiId: sourceInfo.cgiId,
      url: v.url || (media && (media.url + (media.urlToken || ''))),
      key: v.key || (media && (media.decodeKey || media.decryptKey)) || '',
      coverUrl: v.coverUrl || v.thumbUrl || (media && media.thumbUrl),
      duration: v.duration || (media && (media.videoPlayLen * 1000 || media.durationMs)),
      size: v.size || (media && media.fileSize),
      nickname: v.nickname || (v.contact && v.contact.nickname) || '',
      createtime: v.createtime,
      // 额外信息
      spec: spec,
      width: (spec[0] && spec[0].width) || (media && media.width) || 0,
      height: (spec[0] && spec[0].height) || (media && media.height) || 0
    };
  });

  var blob = new Blob([JSON.stringify(exportData, null, 2)], { type: 'application/json' });
  var url = URL.createObjectURL(blob);
  var a = document.createElement('a');
  a.href = url;
  a.download = 'batch_videos_' + new Date().toISOString().slice(0, 10) + '.json';
  a.click();
  URL.revokeObjectURL(url);

  __wx_log({ msg: '📤 已导出' + __get_batch_export_scope_label__() + ' ' + exportData.length + ' 个视频（JSON）' });
}

async function __export_batch_video_csv__(options) {
  options = options || {};
  var autoSyncCreativeRadar = options.autoSyncCreativeRadar === true;
  var videos = __get_batch_export_videos__();

  if (videos.length === 0) {
    __wx_log({ msg: '⚠️ 没有可导出的视频' });
    return;
  }

  // 用户要求在导出 CSV 时缓存 OSS 配置。未勾选时不会读取或保存凭证。
  var ossState = await __persist_batch_oss_config_if_needed__();
  if (ossState === null) return;
  if (autoSyncCreativeRadar && !ossState.enabled) {
    __wx_log({ msg: '⚠️ 请先勾选“视频同步上传OSS”再同步创意雷达系统' });
    return;
  }
  var preparedEntries = null;
  var exportVideos = videos;
  if (ossState.enabled) {
    preparedEntries = __prepare_batch_download_entries__(videos);
    exportVideos = preparedEntries.map(function (entry) { return entry.source; });
    if (exportVideos.length === 0) {
      __wx_log({ msg: '⚠️ 没有可下载并上传 OSS 的视频' });
      return;
    }
  }

  var exportButton = options.actionButton || document.getElementById('batch-export-csv-btn');
  if (exportButton) {
    exportButton.disabled = true;
    exportButton.textContent = autoSyncCreativeRadar
      ? '正在准备同步...'
      : (ossState.enabled ? '正在创建任务...' : '正在导出...');
  }

  var exportedAt = new Date().toISOString();
  var exportRecord;
  try {
    exportRecord = await __create_batch_export_record__(exportVideos, ossState.enabled, exportedAt, autoSyncCreativeRadar);
    if (!ossState.enabled) {
      await __download_batch_export_record_csv__(exportRecord);
      __wx_log({
        msg: '📊 已导出' + __get_batch_export_scope_label__() + ' ' + exportVideos.length +
          ' 个视频（CSV，使用原始视频链接），并写入控制台“导出记录”'
      });
      return;
    }

    var started = await __batch_download_selected__({
      videos: exportVideos,
      preparedEntries: preparedEntries,
      ossState: ossState,
      exportRecordId: exportRecord.id
    });
    if (!started) {
      await __mark_batch_export_failed__(exportRecord.id, '未能启动关联的下载及 OSS 上传任务');
      return;
    }
    if (autoSyncCreativeRadar) {
      __wx_log({ msg: '☁️ OSS 导出任务已创建，上传完成后将自动同步创意雷达系统。' });
      var syncResult = await __wait_batch_export_and_sync_creative_radar__(exportRecord.id);
      __wx_log({
        msg: '✅ 创意雷达同步成功：' + Number(syncResult.creativeRadarSyncCompleted || exportVideos.length) +
          ' 条，新增 ' + Number(syncResult.creativeRadarInserted || 0) +
          ' 条，更新 ' + Number(syncResult.creativeRadarUpdated || 0) + ' 条'
      });
    } else {
      __wx_log({
        msg: '☁️ OSS 导出任务已创建，不会立即下载 CSV。请到控制台“导出记录”等待全部视频上传完成后下载；上传进度可在“OSS上传队列”查看。'
      });
    }
  } catch (error) {
    if (exportRecord && exportRecord.id) {
      await __mark_batch_export_failed__(exportRecord.id, error.message || String(error));
    }
    __wx_log({
      msg: (autoSyncCreativeRadar ? '❌ 自动同步创意雷达失败：' : '❌ 创建 CSV 导出任务失败：') +
        (error.message || error)
    });
  } finally {
    if (exportButton) {
      exportButton.disabled = false;
      exportButton.textContent = autoSyncCreativeRadar ? '同步创意雷达系统' : '导出 CSV';
    }
  }
}

// ==================== 清空视频列表 ====================
function __clear_batch_video_list__() {
  if (__wx_batch_download_manager__.isDownloading) {
    __wx_log({ msg: '⚠️ 下载中，无法清空' });
    return;
  }

  var count = __wx_batch_download_manager__.videos.length;

  if (count === 0) {
    __wx_log({ msg: '⚠️ 列表已经是空的' });
    return;
  }

  // 确认清空
  if (!confirm('确定要清空 ' + count + ' 个视频吗？')) {
    return;
  }

  __wx_batch_download_manager__.videos = [];
  __wx_batch_download_manager__.selectedItems = {};
  __wx_batch_download_manager__.currentPage = 1;

  // 更新UI
  var countElement = document.getElementById('batch-total-count');
  if (countElement) {
    countElement.textContent = '0 个';
  }

  __render_batch_video_list__();

  __wx_log({ msg: '🗑️ 已清空 ' + count + ' 个视频' });
}

// ==================== 更新弹窗 ====================
function __update_batch_download_ui__(videos, title) {
  if (!__wx_batch_download_manager__.isVisible) return;

  // 追加新视频
  var newCount = __wx_batch_download_manager__.appendVideos(videos);

  if (title) {
    __wx_batch_download_manager__.title = title;
  }

  // 统计视频和直播数量
  var allVideos = __wx_batch_download_manager__.videos;
  var videoCount = 0;
  var liveCount = 0;
  allVideos.forEach(function (v) {
    if (v.type === 'live' || v.type === 'live_replay') {
      liveCount++;
    } else if (v.type === 'media' || !v.type) {
      videoCount++;
    }
  });

  // 更新总数
  var countElement = document.getElementById('batch-total-count');
  if (countElement) {
    var statsText = '';
    var currentPath = window.location.pathname;

    if (currentPath.includes('/pages/home')) {
      // Home页：显示"X 个视频"
      statsText = videoCount + ' 个视频';
      if (liveCount > 0) {
        statsText += ', ' + liveCount + ' 个直播';
      }
    } else if (currentPath.includes('/pages/profile')) {
      // Profile页：显示"X 个视频, Y 个直播回放"
      if (liveCount > 0) {
        statsText = videoCount + ' 个视频, ' + liveCount + ' 个直播回放';
      } else {
        statsText = videoCount + ' 个视频';
      }
    } else {
      // 其他页面：默认显示
      if (liveCount > 0) {
        statsText = videoCount + ' 个视频, ' + liveCount + ' 个直播';
      } else {
        statsText = allVideos.length + ' 个';
      }
    }

    countElement.textContent = statsText;
  }

  // 重新渲染列表
  __render_batch_video_list__();

  if (newCount > 0) {
    console.log('[批量下载] UI已更新，新增', newCount, '个视频');
  }
}

// ==================== 渲染视频列表 ====================
function __render_batch_video_list__() {
  var pageVideos = __wx_batch_download_manager__.getCurrentPageVideos();
  var listContainer = document.getElementById('batch-list');
  if (!listContainer) return;

  listContainer.innerHTML = '';

  for (var i = 0; i < pageVideos.length; i++) {
    var video = pageVideos[i];
    var isSelected = __wx_batch_download_manager__.selectedItems[video.id];

    // 调试：打印视频类型和下载状态
    if (i === 0) {
      console.log('[批量下载] 第一个视频调试信息:', {
        id: video.id,
        title: (video.title || '').substring(0, 30),
        type: video.type,
        canDownload: video.canDownload,
        hasUrl: !!video.url,
        hasKey: video.key !== undefined
      });
    }

    var item = document.createElement('div');
    item.style.cssText = 'display:flex;align-items:flex-start;padding:8px;background:rgba(255,255,255,0.05);border-radius:6px;cursor:pointer;transition:background 0.2s;gap:10px;';
    item.onmouseover = function () { this.style.background = 'rgba(255,255,255,0.08)'; };
    item.onmouseout = function () { this.style.background = 'rgba(255,255,255,0.05)'; };

    // 提取视频信息（兼容多种数据格式）
    var media = video.objectDesc && video.objectDesc.media && video.objectDesc.media[0];

    // 判断是否是直播（不能下载）- 必须在使用前定义
    var isLive = video.type === 'live';
    // 只有明确标记为 false 才不能下载，其他情况（undefined、true）都可以下载
    var canDownload = video.canDownload !== false && video.type !== 'live';

    // 复选框
    var checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.checked = isSelected;
    checkbox.style.cssText = 'margin-top:4px;cursor:pointer;flex-shrink:0;';
    checkbox.dataset.videoId = video.id;
    // 如果是直播或不能下载，禁用复选框
    if (isLive || !canDownload) {
      checkbox.disabled = true;
      checkbox.style.opacity = '0.5';
      checkbox.style.cursor = 'not-allowed';
    }
    checkbox.onclick = function (e) {
      e.stopPropagation();
      if (!this.disabled) {
        __wx_batch_download_manager__.toggleSelect(this.dataset.videoId, this.checked);
        __update_batch_ui__();
      }
    };

    // 封面URL
    var coverUrl = video.thumbUrl || video.coverUrl || video.fullThumbUrl ||
      (media && media.thumbUrl) || '';

    // 标题
    var title = video.title ||
      (video.objectDesc && video.objectDesc.description) ||
      '无标题';

    // 时长（毫秒）
    var duration = video.duration ||
      (media && (media.videoPlayLen * 1000 || media.durationMs)) || 0;

    // 文件大小（字节）
    var size = video.size ||
      (media && (media.fileSize || media.cdnFileSize)) || 0;

    // 作者
    var nickname = video.nickname ||
      (video.contact && video.contact.nickname) || '';

    // 创建时间
    var createtime = video.createtime || 0;

    // 格式化时长
    var durationStr = '';
    if (duration) {
      var seconds = Math.floor(duration / 1000);
      var minutes = Math.floor(seconds / 60);
      seconds = seconds % 60;
      durationStr = minutes + ':' + (seconds < 10 ? '0' : '') + seconds;
    }

    // 格式化文件大小
    var sizeStr = '';
    if (size) {
      var mb = size / (1024 * 1024);
      sizeStr = mb.toFixed(1) + ' MB';
    }

    // 格式化发布时间
    var publishTime = '';
    if (createtime) {
      var date = new Date(createtime * 1000);
      var month = date.getMonth() + 1;
      var day = date.getDate();
      publishTime = month + '月' + day + '日';
    }

    // 封面容器（带时长标签）
    var thumbContainer = document.createElement('div');
    thumbContainer.style.cssText = 'width:60px;height:40px;border-radius:4px;overflow:hidden;background:#1a1a1a;flex-shrink:0;position:relative;';

    if (coverUrl) {
      var thumbImg = document.createElement('img');
      thumbImg.src = coverUrl;
      thumbImg.style.cssText = 'width:100%;height:100%;object-fit:cover;';
      thumbContainer.appendChild(thumbImg);
    } else {
      var noThumb = document.createElement('div');
      noThumb.style.cssText = 'width:100%;height:100%;display:flex;align-items:center;justify-content:center;color:#666;font-size:12px;';
      noThumb.textContent = '无封面';
      thumbContainer.appendChild(noThumb);
    }

    // 直播标签（左上角）
    if (isLive) {
      var liveLabel = document.createElement('div');
      liveLabel.style.cssText = 'position:absolute;top:4px;left:4px;background:#fa5151;color:#fff;font-size:10px;padding:2px 4px;border-radius:2px;font-weight:500;';
      liveLabel.textContent = '直播';
      thumbContainer.appendChild(liveLabel);
    }

    // 时长标签（右下角）
    if (durationStr && !isLive) {
      var durationLabel = document.createElement('div');
      durationLabel.style.cssText = 'position:absolute;bottom:4px;right:4px;background:rgba(0,0,0,0.8);color:#fff;font-size:11px;padding:2px 4px;border-radius:2px;';
      durationLabel.textContent = durationStr;
      thumbContainer.appendChild(durationLabel);
    }

    // 信息容器
    var info = document.createElement('div');
    info.style.cssText = 'flex:1;min-width:0;display:flex;flex-direction:column;gap:4px;';

    // 标题
    var titleDiv = document.createElement('div');
    titleDiv.style.cssText = 'font-size:13px;color:#fff;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;line-height:1.4;';
    titleDiv.textContent = title;

    // 如果是直播回放，添加回放标签
    if (video.type === 'live_replay') {
      var replayBadge = document.createElement('span');
      replayBadge.style.cssText = 'display:inline-block;margin-left:6px;background:#fa5151;color:#fff;font-size:10px;padding:2px 4px;border-radius:2px;vertical-align:middle;';
      replayBadge.textContent = '回放';
      titleDiv.appendChild(replayBadge);
    }
    // 如果是直播且不能下载，添加提示
    else if (isLive || !canDownload) {
      titleDiv.style.color = '#999';
      var tipSpan = document.createElement('span');
      tipSpan.style.cssText = 'color:#fa5151;font-size:11px;margin-left:6px;';
      tipSpan.textContent = '(暂不支持下载)';
      titleDiv.appendChild(tipSpan);
    }
    info.appendChild(titleDiv);

    // 详细信息（大小、日期、作者）
    var detailDiv = document.createElement('div');
    detailDiv.style.cssText = 'display:flex;gap:8px;font-size:11px;color:#999;flex-wrap:wrap;';

    var details = [];
    if (sizeStr) details.push('<span>' + sizeStr + '</span>');
    if (publishTime) details.push('<span>' + publishTime + '</span>');
    if (nickname) details.push('<span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:100px;">@' + nickname + '</span>');

    detailDiv.innerHTML = details.join('');
    info.appendChild(detailDiv);

    // 组装列表项
    item.appendChild(checkbox);
    item.appendChild(thumbContainer);
    item.appendChild(info);

    item.onclick = function () {
      // 如果是直播或不能下载，不响应点击
      if (isLive || !canDownload) return;

      var cb = this.querySelector('input[type="checkbox"]');
      cb.checked = !cb.checked;
      __wx_batch_download_manager__.toggleSelect(cb.dataset.videoId, cb.checked);
      __update_batch_ui__();
    };

    listContainer.appendChild(item);
  }

  __update_batch_ui__();
}

function __update_batch_ui__() {
  // 更新页码
  document.getElementById('batch-current-page').textContent = __wx_batch_download_manager__.currentPage;
  document.getElementById('batch-total-pages').textContent = __wx_batch_download_manager__.getTotalPages();

  // 更新选中数量
  var selectedCount = __wx_batch_download_manager__.getSelectedVideos().length;
  document.getElementById('batch-selected-count').textContent = '已选 ' + selectedCount + ' 个';

  // 更新全选状态
  var pageVideos = __wx_batch_download_manager__.getCurrentPageVideos();
  var allSelected = pageVideos.length > 0 && pageVideos.every(function (video) {
    return __wx_batch_download_manager__.selectedItems[video.id];
  });
  var selectAllCheckbox = document.getElementById('batch-select-all');
  if (selectAllCheckbox) {
    selectAllCheckbox.checked = allSelected;
  }
}

function __parse_batch_date__(value) {
  if (value === undefined || value === null || value === '') return null;
  var date;
  var text = String(value).trim();
  if (/^-?\d+(?:\.\d+)?$/.test(text)) {
    var timestamp = Number(text);
    if (!isFinite(timestamp)) return null;
    date = new Date(Math.abs(timestamp) < 1000000000000 ? timestamp * 1000 : timestamp);
  } else {
    var localMatch = text.match(/^(\d{4})[-\/]([01]?\d)[-\/]([0-3]?\d)(?:[ T](\d{1,2}):(\d{1,2})(?::(\d{1,2}))?)?$/);
    if (localMatch) {
      date = new Date(
        Number(localMatch[1]),
        Number(localMatch[2]) - 1,
        Number(localMatch[3]),
        Number(localMatch[4] || 0),
        Number(localMatch[5] || 0),
        Number(localMatch[6] || 0)
      );
    } else {
      date = new Date(text);
    }
  }
  return isNaN(date.getTime()) ? null : date;
}

function __format_batch_create_time__(value) {
  var date = __parse_batch_date__(value);
  if (!date) return '';
  var pad = function(value) {
    return value < 10 ? '0' + value : String(value);
  };
  return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate()) +
    ' ' + pad(date.getHours()) + ':' + pad(date.getMinutes()) + ':' + pad(date.getSeconds());
}

function __format_batch_size_mb__(bytes) {
  if (!bytes) {
    return '';
  }
  return (bytes / (1024 * 1024)).toFixed(2) + 'MB';
}

// ==================== 批量下载 ====================
async function __batch_download_selected__(options) {
  options = options || {};
  var selectedVideos = Array.isArray(options.videos)
    ? options.videos
    : __wx_batch_download_manager__.getSelectedVideos();

  if (selectedVideos.length === 0) {
    __wx_log({ msg: '❌ 请先选择要下载的视频' });
    return false;
  }

  // OSS 导出会预先传入同一份筛选结果，避免导出记录与下载任务数量不一致。
  var preparedEntries = Array.isArray(options.preparedEntries)
    ? options.preparedEntries
    : __prepare_batch_download_entries__(selectedVideos);
  var formattedVideos = preparedEntries.map(function (entry) { return entry.formatted; });

  if (formattedVideos.length === 0) {
    __wx_log({ msg: '❌ 没有可下载的视频' });
    return false;
  }

  var batchRequestSequence = (__wx_batch_download_manager__.batchRequestSequence || 0) + 1;
  __wx_batch_download_manager__.batchRequestSequence = batchRequestSequence;
  __wx_batch_download_manager__.activeBatchRequestSequence = batchRequestSequence;
  var isActiveBatchUI = function () {
    return __wx_batch_download_manager__.activeBatchRequestSequence === batchRequestSequence;
  };

  // 首次使用时也在开始下载前保存配置，确保本批次可立即同步上传。
  var ossState = options.ossState !== undefined
    ? options.ossState
    : await __persist_batch_oss_config_if_needed__();
  if (ossState === null) return false;

  // 设置下载状态
  if (isActiveBatchUI()) {
    __wx_batch_download_manager__.isDownloading = true;
    __wx_batch_download_manager__.stopSignal = false;
  }

  __wx_log({ msg: '🚀 开始批量下载 ' + formattedVideos.length + ' 个视频（后端并发）...' });

  // 显示进度和取消按钮
  var progressDiv = document.getElementById('batch-download-progress');
  var progressText = document.getElementById('batch-progress-text');
  var progressBar = document.getElementById('batch-progress-bar');
  var downloadBtn = document.getElementById('batch-download-btn');
  var cancelBtn = document.getElementById('batch-cancel-btn');

  if (progressDiv && isActiveBatchUI()) progressDiv.style.display = 'block';
  if (downloadBtn && isActiveBatchUI()) {
    downloadBtn.textContent = '下载中...';
    downloadBtn.style.opacity = '0.7';
    downloadBtn.style.cursor = 'not-allowed';
  }
  if (cancelBtn && isActiveBatchUI()) {
    cancelBtn.style.display = 'block';
    cancelBtn.textContent = '取消';
    cancelBtn.disabled = false;
  }

  try {
    // 构建批量下载请求数据
    var batchVideos = preparedEntries.map(function(entry) {
      var video = entry.formatted;
      var sourceVideo = entry.source || video;
      var authorName = video.nickname || (video.contact && video.contact.nickname) || '未知作者';
      var normalizedDownload = typeof __wx_channels_normalize_video_download__ === 'function'
        ? __wx_channels_normalize_video_download__(video, null)
        : {
          mode: 'original',
          url: video.url || '',
          resolution: '',
          width: 0,
          height: 0,
          fileFormat: ''
        };

      return {
        id: video.id || '',
        url: normalizedDownload.url || video.url || '',
        title: video.title || video.id || String(Date.now()),
        author: authorName,
        headers: {
          Referer: location.href,
          Origin: location.origin || 'https://channels.weixin.qq.com'
        },
        userAgent: navigator.userAgent || '',
        sourceUrl: location.href,
        key: video.key || '',
        resolution: normalizedDownload.resolution || '',
        width: normalizedDownload.width || 0,
        height: normalizedDownload.height || 0,
        fileFormat: normalizedDownload.fileFormat || '',
        durationMs: video.duration || 0,
        size: video.size || 0,
        sizeMB: __format_batch_size_mb__(video.size || 0),
        createTime: __format_batch_create_time__(video.createtime || 0),
        // WXU.format_feed may return a new object. Keep the collection snapshot
        // from the source item so the export row and OSS object date stay aligned.
        capturedAt: __format_batch_csv_captured_at__(sourceVideo.capturedAt || video.capturedAt || new Date().toISOString())
      };
    });

    // 调用后端批量下载接口
    var response = await fetch('/__wx_channels_api/batch_start', {
      method: 'POST',
      headers: __wx_channels_batch_api_headers__(),
      body: JSON.stringify({
        videos: batchVideos,
        forceRedownload: __wx_batch_download_manager__.forceRedownload,
        ossUploadEnabled: ossState.enabled,
        exportRecordId: options.exportRecordId || ''
      })
    });

    if (!response.ok) {
      throw new Error('HTTP ' + response.status + ': ' + response.statusText);
    }

    var result = await response.json();
    
    // 检查响应格式（兼容两种格式）
    var data = result.data || result;
    if (!result.success && result.code !== 0) {
      throw new Error(result.error || result.message || '启动批量下载失败');
    }
    var batchId = data.batchId || options.exportRecordId || '';
    var progressRequestBody = JSON.stringify({ batchId: batchId });

    __wx_log({
      msg: (data.queued
        ? '✅ 已进入下载同步队列，前方 ' + (data.queuePosition || 1) + ' 批'
        : '✅ 批量下载已启动，并发数: ' + (data.concurrency || 5)) +
        (ossState.enabled ? '，完成后将同步上传 OSS' : '')
    });

    // 等待100ms后立即查询一次进度（避免错过快速完成的下载）
    await new Promise(function(resolve) { setTimeout(resolve, 100); });
    
    // 立即查询一次进度
    try {
      var progressRes = await fetch('/__wx_channels_api/batch_progress', {
        method: 'POST',
        headers: __wx_channels_batch_api_headers__(),
        body: progressRequestBody
      });
      if (progressRes.ok) {
        var progressData = await progressRes.json();
        var data = progressData.data || progressData;
        if (progressData.success || progressData.code === 0 || data.total !== undefined) {
          __merge_batch_oss_task_results__(data.tasks || []);
          var total = data.total || 0;
          var done = data.done || 0;
          var failed = data.failed || 0;
          
          // 如果已经完成，直接显示结果并返回
          if (total > 0 && done + failed >= total) {
            __wx_log({ msg: '✅ 批量下载完成: 成功 ' + done + ' 个, 失败 ' + failed + ' 个' });
            __reset_batch_download_ui__(batchRequestSequence);
            return true;
          }
        }
      }
    } catch (e) {
      console.error('[批量下载] 初始进度查询失败:', e);
    }

    // 开始轮询进度
    var pollInterval = setInterval(async function() {
      // 检查取消信号
      if (isActiveBatchUI() && __wx_batch_download_manager__.stopSignal) {
        clearInterval(pollInterval);
        // 调用取消接口
        try {
          await fetch('/__wx_channels_api/batch_cancel', {
            method: 'POST',
            headers: __wx_channels_batch_api_headers__(),
            body: progressRequestBody
          });
          __wx_log({ msg: '⏹️ 批量下载已取消' });
        } catch (e) {
          console.error('[批量下载] 取消失败:', e);
        }
        __reset_batch_download_ui__(batchRequestSequence);
        return;
      }

      try {
        var progressRes = await fetch('/__wx_channels_api/batch_progress', {
          method: 'POST',
          headers: __wx_channels_batch_api_headers__(),
          body: progressRequestBody
        });

        if (progressRes.ok) {
          var progressData = await progressRes.json();
          console.log('[批量下载] 进度数据:', progressData);
          
          // 兼容两种响应格式
          var data = progressData.data || progressData;
          if (progressData.success || progressData.code === 0 || data.total !== undefined) {
            __merge_batch_oss_task_results__(data.tasks || []);
            var total = data.total || 0;
            var done = data.done || 0;
            var failed = data.failed || 0;
            var running = data.running || 0;

            console.log('[批量下载] 解析后:', { total: total, done: done, failed: failed, running: running });

            // 更新进度显示
            if (progressText && isActiveBatchUI()) {
              progressText.textContent = data.queued
                ? '排队中（前方 ' + (data.queuePosition || 1) + ' 批）'
                : done + '/' + total;
              if (running > 0) {
                progressText.textContent += ' (并发: ' + running + ')';
              }
              
              // 显示当前正在下载的任务的详细进度
              if (data.currentTasks && data.currentTasks.length > 0) {
                var currentTask = data.currentTasks[0];
                if (currentTask.progress > 0) {
                  progressText.textContent += ' - ' + currentTask.progress.toFixed(1) + '%';
                }
                if (currentTask.ossStatus === 'uploading') {
                  progressText.textContent += ' - 正在上传OSS';
                } else if (currentTask.ossStatus === 'retrying') {
                  progressText.textContent += ' - OSS上传重试中';
                }
              }
            }
            if (progressBar && isActiveBatchUI()) {
              // 计算总体进度：已完成的 + 正在下载的平均进度
              var overallProgress = 0;
              if (total > 0) {
                // 已完成的视频占比
                overallProgress = (done / total) * 100;
                
                // 加上正在下载的视频的平均进度
                if (data.currentTasks && data.currentTasks.length > 0) {
                  var downloadingProgress = 0;
                  for (var i = 0; i < data.currentTasks.length; i++) {
                    downloadingProgress += (data.currentTasks[i].progress || 0);
                  }
                  // 平均进度
                  var avgProgress = downloadingProgress / data.currentTasks.length;
                  // 正在下载的视频占总数的比例
                  var downloadingRatio = data.currentTasks.length / total;
                  // 加到总进度中
                  overallProgress += (avgProgress * downloadingRatio);
                }
              }
              
              progressBar.style.width = overallProgress + '%';
              console.log('[批量下载] 进度条宽度:', overallProgress.toFixed(1) + '%');
            }

            // 检查是否完成
            if (total > 0 && done + failed >= total && running === 0) {
              clearInterval(pollInterval);
              __wx_log({ msg: '✅ 批量下载完成: 成功 ' + done + ' 个, 失败 ' + failed + ' 个' });
              __reset_batch_download_ui__(batchRequestSequence);
            }
          } else {
            console.warn('[批量下载] 无效的进度数据格式:', progressData);
          }
        } else {
          console.error('[批量下载] 进度查询失败:', progressRes.status);
        }
      } catch (e) {
        console.error('[批量下载] 轮询进度失败:', e);
      }
    }, 2000); // 每2秒轮询一次
    return true;

  } catch (err) {
    __wx_log({ msg: '❌ 批量下载失败: ' + (err.message || err) });
    console.error('[批量下载] 错误:', err);
    __reset_batch_download_ui__(batchRequestSequence);
    return false;
  }
}

// 重置批量下载UI状态
function __reset_batch_download_ui__(batchRequestSequence) {
  if (batchRequestSequence &&
      __wx_batch_download_manager__.activeBatchRequestSequence !== batchRequestSequence) {
    return;
  }
  __wx_batch_download_manager__.activeBatchRequestSequence = 0;
  __wx_batch_download_manager__.isDownloading = false;
  __wx_batch_download_manager__.stopSignal = false;

  var downloadBtn = document.getElementById('batch-download-btn');
  var cancelBtn = document.getElementById('batch-cancel-btn');
  var progressDiv = document.getElementById('batch-download-progress');
  var progressBar = document.getElementById('batch-progress-bar');

  if (downloadBtn) {
    downloadBtn.textContent = '开始下载';
    downloadBtn.style.opacity = '1';
    downloadBtn.style.cursor = 'pointer';
  }
  if (cancelBtn) {
    cancelBtn.style.display = 'none';
  }

  // 延迟隐藏进度条
  setTimeout(function () {
    if (progressDiv) progressDiv.style.display = 'none';
    if (progressBar) progressBar.style.width = '0%';
  }, 3000);
}

console.log('[batch_download.js] 通用批量下载模块加载完成');
