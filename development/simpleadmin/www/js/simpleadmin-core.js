(function (global) {
  'use strict';

  const root = global.SimpleAdmin || {};

  root.Api = root.Api || (function () {
    const wsPath = '/api/ws';
    let ws = null;
    let connectPromise = null;
    let nextId = 1;
    const pending = new Map();

    function wsUrl() {
      const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
      return `${scheme}://${location.host}${wsPath}`;
    }

    function rejectAll(error) {
      pending.forEach((item) => item.reject(error));
      pending.clear();
    }

    function makeResponse(message) {
      const status = Number(message.status) || 0;
      const body = message.body || '';
      return {
        ok: status >= 200 && status < 300,
        status,
        headers: message.headers || {},
        text() {
          return Promise.resolve(body);
        },
        json() {
          return Promise.resolve().then(() => (body ? JSON.parse(body) : null));
        }
      };
    }


    function dispatchServerEvent(message) {
      if (typeof global.dispatchEvent !== 'function' || typeof global.CustomEvent !== 'function') return;
      global.dispatchEvent(new CustomEvent('simpleadmin:server-event', { detail: message }));
      global.dispatchEvent(new CustomEvent(`simpleadmin:${message.event}`, { detail: message.data || {} }));
    }

    function connect() {
      if (ws && ws.readyState === WebSocket.OPEN) return Promise.resolve(ws);
      if (connectPromise) return connectPromise;

      connectPromise = new Promise((resolve, reject) => {
        const socket = new WebSocket(wsUrl());
        ws = socket;

        socket.onopen = () => {
          connectPromise = null;
          resolve(socket);
        };
        socket.onerror = () => {
          const error = new Error('WebSocket connection failed');
          connectPromise = null;
          reject(error);
          rejectAll(error);
        };
        socket.onclose = () => {
          if (ws === socket) ws = null;
          connectPromise = null;
          rejectAll(new Error('WebSocket connection closed'));
        };
        socket.onmessage = (event) => {
          let message;
          try {
            message = JSON.parse(String(event.data || '{}'));
          } catch (error) {
            console.error('Invalid WebSocket API response:', error);
            return;
          }
          if (message.type === 'event' && message.event) {
            dispatchServerEvent(message);
            return;
          }
          const item = pending.get(message.id);
          if (!item) return;
          pending.delete(message.id);
          if (message.error && !message.status) {
            item.reject(new Error(message.error));
            return;
          }
          item.resolve(makeResponse(message));
        };
      });

      return connectPromise;
    }

    function normalizeHeaders(headers) {
      const normalized = {};
      if (!headers) return normalized;
      if (headers instanceof Headers) {
        headers.forEach((value, key) => { normalized[key] = value; });
        return normalized;
      }
      Object.keys(headers).forEach((key) => {
        if (headers[key] !== undefined && headers[key] !== null) {
          normalized[key] = String(headers[key]);
        }
      });
      return normalized;
    }

    function serializeBody(body) {
      if (body === undefined || body === null) return '';
      if (body instanceof URLSearchParams) return body.toString();
      if (typeof body === 'string') return body;
      return String(body);
    }

    function request(path, options = {}) {
      if (!String(path || '').startsWith('/api/')) {
        return fetch(path, options);
      }
      const method = String(options.method || 'GET').toUpperCase();
      const headers = normalizeHeaders(options.headers);
      const body = serializeBody(options.body);
      if (body && !Object.keys(headers).some((key) => key.toLowerCase() === 'content-type')) {
        headers['Content-Type'] = 'application/x-www-form-urlencoded; charset=UTF-8';
      }

      return connect().then((socket) => new Promise((resolve, reject) => {
        const id = String(nextId++);
        pending.set(id, { resolve, reject });
        try {
          socket.send(JSON.stringify({ id, method, path, headers, body }));
        } catch (error) {
          pending.delete(id);
          reject(error);
        }
      }));
    }

    function postForm(path, params = {}) {
      const body = params instanceof URLSearchParams ? params : new URLSearchParams(params);
      return request(path, {
        method: 'POST',
        cache: 'no-store',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
        body
      });
    }

    return {
      request,
      text(url, options) {
        return request(url, options).then((response) => response.text());
      },
      json(url, options) {
        return request(url, options).then((response) => response.json());
      },
      url(path, params) {
        const query = params ? new URLSearchParams(params).toString() : '';
        return query ? `${path}?${query}` : path;
      },
      postForm,
      postJSON(path, params = {}) {
        return postForm(path, params).then((response) => response.json());
      },
      getDashboardData(params = {}) {
        return this.postJSON('/api/dashboard_data', Object.assign({ action: 'get' }, params));
      },
      mockAT(params = {}) {
        return this.postJSON('/api/mock_at', params);
      },
      getDeviceInfo(params = {}) {
        return this.postJSON('/api/device_info_data', Object.assign({ action: 'get' }, params));
      },
      setDeviceImei(imei) {
        return this.postJSON('/api/settings_data', { action: 'set_imei', imei });
      },
      networkData(params = {}) {
        return this.postJSON('/api/network_data', params);
      },
      settingsData(params = {}) {
        return this.postJSON('/api/settings_data', params);
      },
      smsData(params = {}) {
        return this.postJSON('/api/sms_data', params);
      },
      getAT(atcmd, options = {}) {
        const params = new URLSearchParams({ atcmd });
        if (options.force) params.set('force', '1');
        if (options.wait !== undefined) params.set('wait', options.wait ? '1' : '0');
        return request('/api/get_atcache', {
          method: 'POST',
          cache: 'no-store',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
          body: params
        });
      },
      refreshAT(atcmd) {
        return this.getAT(atcmd, { force: true, wait: true });
      },
      getATText(atcmd, options = {}) {
        return this.getAT(atcmd, options).then((response) => response.text());
      },
      getUptime() {
        return request('/api/get_uptime');
      },
      getPing() {
        return request('/api/get_ping');
      },
      getTTLStatus() {
        return request('/api/get_ttl_status');
      },
      setTTL(ttlvalue) {
        return request(this.url('/api/set_ttl', { ttlvalue }));
      },
      getLanguage() {
        return request('/api/get_language', { cache: 'no-store' });
      },
      setLanguage(language) {
        return request(this.url('/api/set_language', { language }), { method: 'POST' });
      },
      setPassword(currentPassword, newPassword, confirmPassword) {
        const params = new URLSearchParams({
          current_password: currentPassword || '',
          new_password: newPassword || '',
          confirm_password: confirmPassword || ''
        });
        return fetch('/api/set_password', {
          method: 'POST',
          cache: 'no-store',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
          body: params
        });
      }
    };
  })();

  root.Brand = root.Brand || (function () {
    const storageKey = 'simpleadmin.moduleModelName';
    let currentName = '';
    let currentPageTitle = '';
    let refreshPromise = null;

    function normalizeModel(value) {
      const text = String(value === undefined || value === null ? '' : value).replace(/\s+/g, ' ').trim();
      if (!text || text === '-' || text === '未知' || text === '获取中...') return '';
      return text;
    }

    function cachedModel() {
      try {
        return normalizeModel(global.localStorage.getItem(storageKey));
      } catch (_) {
        return '';
      }
    }

    function cacheModel(value) {
      const model = normalizeModel(value);
      if (!model) return;
      try {
        global.localStorage.setItem(storageKey, model);
      } catch (_) {
        // localStorage 不可用时只更新当前页面显示。
      }
    }

    function markFromName(name) {
      const model = normalizeModel(name);
      const chars = Array.from(model.replace(/[^0-9A-Za-z\u4e00-\u9fa5]/g, ''));
      if (chars.length === 0) return '--';
      return chars.slice(0, 2).join('').toUpperCase();
    }

    function setText(selector, value) {
      document.querySelectorAll(selector).forEach((element) => {
        element.textContent = value;
      });
    }

    function updateDocumentTitle() {
      const brandName = currentName || cachedModel() || 'SimpleAdmin';
      if (currentPageTitle) {
        document.title = `${currentPageTitle} - ${brandName}`;
        return;
      }
      document.title = brandName;
    }

    function apply(name) {
      const model = normalizeModel(name) || cachedModel() || 'SimpleAdmin';
      currentName = model;
      cacheModel(model);
      setText('[data-simpleadmin-brand-title]', model);
      setText('[data-simpleadmin-brand-mark]', markFromName(model));
      updateDocumentTitle();
      return model;
    }

    function refresh(force) {
      if (refreshPromise) return refreshPromise;
      const url = `/api/module_model${force === false ? '' : '?force=1'}`;
      refreshPromise = fetch(url, {
        cache: 'no-store',
        credentials: 'same-origin'
      }).then((response) => {
        if (!response.ok) throw new Error(`module model status ${response.status}`);
        return response.json();
      }).then((data) => {
        const model = normalizeModel(data && data.model);
        if (model) apply(model);
        return model || currentName || cachedModel() || '';
      }).catch(() => {
        if (!currentName) apply(cachedModel() || 'SimpleAdmin');
        return currentName || '';
      }).finally(() => {
        refreshPromise = null;
      });
      return refreshPromise;
    }

    function init() {
      apply(cachedModel() || '获取中...');
      refresh(true);
    }

    function setPageTitle(title) {
      currentPageTitle = String(title || '').trim();
      updateDocumentTitle();
    }

    return {
      init,
      refresh,
      apply,
      setPageTitle,
      getName() {
        return currentName || cachedModel() || '';
      }
    };
  })();

  root.Text = root.Text || {
    lines(value) {
      return String(value || '')
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter(Boolean);
    },
    findStartsWith(lines, prefix) {
      return (lines || []).find((line) => line.startsWith(prefix));
    },
    findAllStartsWith(lines, prefix) {
      return (lines || []).filter((line) => line.startsWith(prefix));
    },
    splitCsv(value) {
      return (String(value || '').match(/"[^"]*"|[^,]+/g) || [])
        .map((item) => item.trim().replace(/^"|"$/g, ''));
    },
    hexToUtf16BE(hex) {
      const clean = String(hex || '').replace(/\s+/g, '');
      if (!clean || clean.length % 2 !== 0 || !/^[0-9a-fA-F]+$/.test(clean)) return '';
      let text = '';
      for (let i = 0; i < clean.length; i += 4) {
        const code = parseInt(clean.substr(i, 4), 16);
        if (!Number.isNaN(code)) text += String.fromCharCode(code);
      }
      return text;
    },
    compactHex(value) {
      return String(value || '').replace(/\s+/g, '');
    }
  };

  root.Mask = root.Mask || (function () {
    const emptyValues = new Set(['', '-', '未知', '未插卡', '无本机号码', '获取中...']);
    const sensitiveVisibleStorageKey = 'zbimsSensitiveVisible';

    function normalize(value) {
      return String(value === undefined || value === null ? '' : value).trim();
    }

    function shouldKeepRaw(value) {
      const text = normalize(value);
      return emptyValues.has(text);
    }

    function getSensitiveVisible() {
      try {
        return global.localStorage.getItem(sensitiveVisibleStorageKey) === '1';
      } catch (error) {
        return false;
      }
    }

    function setSensitiveVisible(visible) {
      const nextVisible = Boolean(visible);
      try {
        global.localStorage.setItem(sensitiveVisibleStorageKey, nextVisible ? '1' : '0');
      } catch (error) {
        // localStorage 不可用时只保留当前页面状态。
      }
      return nextVisible;
    }

    function maskMiddle(value, left = 3, right = 4) {
      const text = normalize(value);
      if (shouldKeepRaw(text)) return text || '-';
      if (text.length <= left + right) return '●'.repeat(Math.max(4, text.length));
      return `${text.slice(0, left)}${'●'.repeat(6)}${text.slice(-right)}`;
    }

    function maskIPv4(value) {
      const text = normalize(value);
      if (shouldKeepRaw(text)) return text || '-';
      return text.replace(/\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b/g, '$1.$2.●●●.●●●');
    }

    function maskIPv6(value) {
      const text = normalize(value);
      if (shouldKeepRaw(text)) return text || '-';
      const parts = text.split(':');
      if (parts.length < 3) return maskMiddle(text, 4, 4);
      return `${parts.slice(0, 2).join(':')}:●●●●:${parts.slice(-1)[0]}`;
    }

    function maskPhone(value) {
      return maskMiddle(value, 3, 4);
    }

    function format(visible, value, type) {
      const text = normalize(value);
      if (visible || shouldKeepRaw(text)) return text || '-';
      switch (type) {
        case 'ipv4':
          return maskIPv4(text);
        case 'ipv6':
          return maskIPv6(text);
        case 'phone':
          return maskPhone(text);
        case 'digit':
          return maskMiddle(text, 3, 4);
        case 'id':
        default:
          return maskMiddle(text, 3, 3);
      }
    }

    return {
      format,
      getSensitiveVisible,
      setSensitiveVisible
    };
  })();

  root.Time = root.Time || {
    parseSmsDate(value) {
      const dateStr = String(value || '').replace(/\+\d{2}$/, '');
      const parts = dateStr.split(',');
      if (parts.length !== 2) return new Date(NaN);
      const dateParts = parts[0].split('/').map(Number);
      const timeParts = parts[1].split(':').map(Number);
      if (dateParts.length !== 3 || timeParts.length !== 3) return new Date(NaN);
      const [day, month, year] = dateParts;
      const [hour, minute, second] = timeParts;
      return new Date(Date.UTC(2000 + year, month - 1, day, hour, minute, second));
    },
    formatDateTime(date) {
      const pad = (value) => value.toString().padStart(2, '0');
      return `${date.getUTCFullYear()}/${pad(date.getUTCMonth() + 1)}/${pad(date.getUTCDate())} ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())}`;
    }
  };

  root.Sms = root.Sms || {
    parseConcatHeader(hex) {
      const normalized = root.Text.compactHex(hex).toUpperCase();
      if (!normalized || !/^[0-9A-F]+$/.test(normalized)) return null;
      if (normalized.startsWith('050003') && normalized.length > 12) {
        const total = parseInt(normalized.slice(8, 10), 16);
        const seq = parseInt(normalized.slice(10, 12), 16);
        if (total > 1 && seq >= 1 && seq <= total) {
          return { ref: normalized.slice(6, 8), total, seq, bodyHex: normalized.slice(12) };
        }
      }
      if (normalized.startsWith('060804') && normalized.length > 14) {
        const total = parseInt(normalized.slice(10, 12), 16);
        const seq = parseInt(normalized.slice(12, 14), 16);
        if (total > 1 && seq >= 1 && seq <= total) {
          return { ref: normalized.slice(6, 10), total, seq, bodyHex: normalized.slice(14) };
        }
      }
      return null;
    },
    decodeUcs2(hex) {
      return root.Text.hexToUtf16BE(hex);
    },
    encodeUcs2(text) {
      return Array.from(String(text || ''))
        .map((char) => char.charCodeAt(0).toString(16).padStart(4, '0').toUpperCase())
        .join('');
    }
  };

  root.Lang = root.Lang || (function () {
    const storageKey = 'simpleadmin.language';
    const defaultLanguage = 'zh-CN';
    const supportedLanguages = ['zh-CN', 'en'];
    let currentLanguage = defaultLanguage;

    const translations = {
      en: {
        '首页': 'Home',
        '网络': 'Network',
        '设置': 'Settings',
        '短信': 'SMS',
        '控制台': 'Console',
        '设备信息': 'Device Info',
        '总览': 'Overview',
        '网络与小区': 'Network and Cells',
        '系统设置': 'System Settings',
        '短信服务': 'SMS Service',
        '退出登录': 'Log Out',
        '菜单': 'Menu',
        '暗夜模式': 'Night Mode',
        '浅色模式': 'Light Mode',
        '温度': 'Temperature',
        'SIM 卡': 'SIM Card',
        '信号百分比': 'Signal Percentage',
        '互联网连接': 'Internet Connection',
        'CPU 使用率': 'CPU Usage',
        'RAM 使用量': 'RAM Usage',
        '实时负载': 'Live Load',
        '网络信息': 'Network Info',
        '精简显示': 'Compact View',
        '完整显示': 'Full View',
        '激活 SIM': 'Active SIM',
        '运营商': 'Carrier',
        '网络模式': 'Network Mode',
        '频段': 'Bands',
        '带宽': 'Bandwidth',
        '在线时长': 'Uptime',
        '刷新频率（最少 2 秒）': 'Refresh rate (minimum 2 seconds)',
        '信号信息': 'Signal Info',
        '信号评估': 'Signal Assessment',
        '累计流量': 'Total Traffic',
        '速率': 'Speed',
        '更新时间': 'Last Updated',
        '刷新': 'Refresh',
        '制造商': 'Manufacturer',
        '型号名称': 'Model Name',
        '固件版本': 'Firmware Version',
        '电话号码': 'Phone Number',
        '局域网IP': 'LAN IP',
        '广域网IPv': 'WAN IPv',
        '版本': 'Version',
        '更新': 'Update',
        '访问': 'Visit',
        '代码库': 'repository',
        '或': 'or',
        '文档': 'documentation',
        '以获取更多信息。版权所有。2024': 'for more information. Copyright. 2024',
        '这将重启调制解调器。': 'This will reboot the modem.',
        '继续吗？': 'Continue?',
        '继续？': 'Continue?',
        '重启': 'Reboot',
        '取消': 'Cancel',
        '重启中...': 'Rebooting...',
        '设备可能已经开始重启，正在显示重启倒计时。': 'The device may have started rebooting, showing the reboot countdown.',
        '正在禁用 IP 透传，网口会重启，请等待倒计时结束。': 'Disabling IP passthrough. The network port will restart. Please wait for the countdown to finish.',
        '请等待': 'Please wait',
        '秒。': 'seconds.',
        '加载中...': 'Loading...',
        'AT 终端': 'AT Terminal',
        'AT 命令': 'AT Command',
        'AT 命令输出': 'AT Command Output',
        '用分号（;）分隔多个命令，后端会逐条发送，示例：AT+CFUN?;+CCID': 'Separate multiple commands with semicolons (;). The backend sends them one by one. Example: AT+CFUN?;+CCID',
        '提交': 'Submit',
        '清除': 'Clear',
        '一键实用工具': 'Quick Tools',
        '重置 AT&F': 'Reset AT&F',
        '重置': 'Reset',
        'IP 透传': 'IP Passthrough',
        '当前：已启用': 'Current: Enabled',
        '当前：未启用': 'Current: Disabled',
        '当前：': 'Current:',
        '未指定': 'Unspecified',
        'USB 协议': 'USB Protocol',
        'ECM (推荐)': 'ECM (Recommended)',
        '更改': 'Change',
        'DMZ 设置': 'DMZ Settings',
        '启用': 'Enable',
        '禁用': 'Disable',
        'LAN IP 设置': 'LAN IP Settings',
        '保存': 'Save',
        '其他设置': 'Other Settings',
        'TTL 设置': 'TTL Settings',
        'TTL 状态和值': 'TTL Status and Value',
        '重启 / 重置 AT&F': 'Reboot / Reset AT&F',
        'TTL 已激活': 'TTL Enabled',
        'TTL 未激活': 'TTL Disabled',
        '设置 TTL 值为 0 以禁用。': 'Set TTL value to 0 to disable.',
        '语言设置': 'Language Settings',
        '界面语言': 'Interface Language',
        '中文': 'Chinese',
        '已保存': 'Saved',
        '保存失败': 'Save failed',
        '语言已保存。': 'Language saved.',
        '登录密码': 'Login Password',
        '账户安全': 'Account Security',
        '当前密码': 'Current Password',
        '新密码': 'New Password',
        '确认新密码': 'Confirm New Password',
        '修改密码': 'Change Password',
        '更改登录密码': 'Change Login Password',
        '请输入当前密码和新密码': 'Enter the current password and new password',
        '两次输入的新密码不一致': 'The new passwords do not match',
        '密码已保存，请使用新密码重新登录。': 'Password saved. Please log in again with the new password.',
        '密码保存失败': 'Password save failed',
        '当前密码不正确': 'Current password is incorrect',
        '新密码不能为空': 'New password cannot be empty',
        '密码确认不一致': 'Password confirmation mismatch',
        '请输入当前登录密码': 'Enter current login password',
        '请输入新的登录密码': 'Enter new login password',
        '请再次输入新的登录密码': 'Enter new login password again',
        '网络工具': 'Network Tools',
        'IP 协议类型': 'IP Protocol Type',
        '保存更改': 'Save Changes',
        '小区锁定': 'Cell Lock',
        '选择扫描模式': 'Select scan mode',
        '当前：选择扫描模式': 'Current: Select scan mode',
        '全面扫描': 'Full Scan',
        '仅LTE': 'LTE Only',
        '仅NR5G': 'NR5G Only',
        '开始扫描': 'Start Scan',
        '扫描中... 请稍候.': 'Scanning... Please wait.',
        '锁定': 'Lock',
        '小区扫描需要几分钟完成，请勿离开次页面。': 'Cell scanning may take a few minutes. Do not leave this page.',
        '小区数量': 'Number of Cells',
        '锁定LTE小区': 'Lock LTE Cell',
        '锁定NR5G-SA小区': 'Lock NR5G-SA Cell',
        '解锁LTE小区': 'Unlock LTE Cell',
        '解锁NR5G-SA小区': 'Unlock NR5G-SA Cell',
        '初始化网络...': 'Initializing network...',
        '锁定频段': 'Band Lock',
        '频段锁定': 'Band Lock',
        '复选框将会在这里生成': 'Checkboxes will be generated here',
        '获取支持的频段中...': 'Getting supported bands...',
        '锁定当前选中': 'Lock Selected',
        '启用所有': 'Enable All',
        '小区扫描': 'Cell Scan',
        '小区扫描将扫描您所在区域的所有LTE和NR5G-SA小区。扫描可能会断开您的网络连接，并需要几分钟完成。': 'Cell scan will scan all LTE and NR5G-SA cells in your area. The scan may interrupt your network connection and may take a few minutes.',
        '选择': 'Select',
        '频点': 'ARFCN',
        '信号': 'Signal',
        '收件箱': 'Inbox',
        '读取短信...': 'Reading SMS...',
        '无短信': 'No SMS',
        '全选': 'Select All',
        '取消选中': 'Deselect',
        '发件人:': 'Sender:',
        '日期和时间:': 'Date and Time:',
        '删除': 'Delete',
        '发信息': 'Send Message',
        '收件人号码': 'Recipient Number',
        '短信内容': 'Message Content',
        '发送短信': 'Send SMS',
        '正在注销...': 'Logging out...',
        '主导航': 'Main navigation',
        '切换导航': 'Toggle navigation',
        '新的 IMEI': 'New IMEI',
        'DMZ IP 地址': 'DMZ IP Address',
        'TTL 值': 'TTL Value',
        '网关 IP 地址': 'Gateway IP Address',
        '起始地址': 'Start Address',
        '结束地址': 'End Address',
        '输入收件人号码': 'Enter recipient number',
        '输入短信内容': 'Enter message content',
        '优秀': 'Excellent',
        '良好': 'Good',
        '一般': 'Fair',
        '差': 'Poor',
        '无信号': 'No Signal',
        '已激活': 'Active',
        '未激活': 'Inactive',
        '已连接': 'Connected',
        '未连接': 'Disconnected',
        '测试模式': 'Test Mode',
        '未知': 'Unknown',
        '未知时间': 'Unknown Time',
        '未插卡': 'No SIM',
        '没有提供新的 IMEI。': 'No new IMEI was provided.',
        'IMEI 无效': 'Invalid IMEI',
        '新的 IMEI 与当前 IMEI 相同。': 'The new IMEI is the same as the current IMEI.',
        'IMEI 与当前 IMEI 相同': 'The IMEI is the same as the current IMEI',
        '请输入新的 IMEI': 'Enter new IMEI',
        '获取IMEI中...': 'Getting IMEI...',
        '请输入所有必填字段': 'Please fill in all required fields',
        '请输入至少一个有效的earfcn和pci对': 'Enter at least one valid EARFCN and PCI pair',
        '请输入要锁定的小区数量': 'Enter the number of cells to lock',
        '请至少选择一个小区进行锁定.': 'Please select at least one cell to lock.',
        '最多只能选择 10 条小区进行锁定': 'You can select up to 10 cells to lock',
        '选择的网络模式无效': 'Invalid network mode selection',
        '没有选中任何频段，请选择至少一个频段！': 'No bands selected. Select at least one band.',
        '没有做出更改': 'No changes were made',
        '某些必填字段缺失，请检查小区数据': 'Some required fields are missing. Check the cell data.',
        '网关 IP 地址格式无效！': 'Invalid gateway IP address format.',
        '请输入有效的网关 IP 地址和起始、结束 IP 地址！': 'Enter a valid gateway IP address, start address, and end address.',
        '未指定 IP 透传模式': 'IP passthrough mode is not specified',
        '无效的 IP 透传模式': 'Invalid IP passthrough mode',
        '未指定 USB 网络模式': 'USB network mode is not specified',
        'USB 网络模式无效': 'Invalid USB network mode',
        '未检测到 SIM 卡': 'No SIM card detected',
        '短信发送成功！': 'SMS sent successfully.',
        '未知错误': 'Unknown error',
        '没有有效的短信索引': 'No valid SMS index',
        '短信索引未正确初始化或为空': 'SMS indexes were not initialized correctly or are empty',
        '自动': 'Auto',
        '选择首选网络': 'Select preferred network',
        'NR5G模式控制': 'NR5G Mode Control',
        '获取中...': 'Loading...',
        '已启用': 'Enabled',
        '未启用': 'Disabled',
        '禁用NR5G-NSA': 'Disable NR5G-NSA',
        '禁用NR5G-SA': 'Disable NR5G-SA',
        '解锁LTE': 'Unlock LTE',
        '解锁NR5G-SA': 'Unlock NR5G-SA',
        '活动频段:': 'Active Bands:',
        '开始小区扫描': 'Start Cell Scan',
        '空': 'Empty',
        '数据': 'Data',
        '网络已断开': 'Network disconnected',
        '正在解析LTE数据': 'Parsing LTE data',
        '正在解析NR5G-SA数据': 'Parsing NR5G-SA data',
        'PCC PCI值:': 'PCC PCI:',
        'SCC PCI值:': 'SCC PCI:',
        '天': 'days',
        '小时': 'hours',
        '分钟': 'minutes',
        '未锁定': 'Unlocked',
        '已锁定4G': '4G Locked',
        '已锁定5G': '5G Locked',
        '已锁定4G和5G': '4G and 5G Locked',
        '未禁用': 'Not Disabled',
        '禁用NSA': 'NSA Disabled',
        '禁用SA': 'SA Disabled',
        '获取频段中...': 'Getting bands...',
        '全部选中': 'Select All',
        '中国移动': 'China Mobile',
        '中国联通': 'China Unicom',
        '中国电信': 'China Telecom',
        '中国广电': 'China Broadnet',
        '中国铁通': 'China Tietong',
        '中国卫通': 'China Satcom',
        '国家电网': 'State Grid',
        '号码或内容不能为空': 'Phone number or message cannot be empty',
        '短信发送失败：': 'SMS sending failed:',
        'AT命令执行结果:': 'AT command result:',
        '发送AT命令失败:': 'Failed to send AT command:',
        '错误:': 'Error:'
      }
    };

    function normalizeLanguage(language) {
      const value = String(language || '').trim().toLowerCase();
      if (value === 'en' || value === 'en-us' || value === 'english') return 'en';
      if (value === 'zh' || value === 'zh-cn' || value === 'cn' || value === 'chinese') return 'zh-CN';
      return '';
    }

    function normalizeText(value) {
      return String(value || '').replace(/\s+/g, ' ').trim();
    }

    function translateForLanguage(key, language) {
      const source = String(key || '');
      const lang = normalizeLanguage(language) || defaultLanguage;
      if (lang === 'zh-CN') return source;

      const table = translations[lang] || {};
      if (Object.prototype.hasOwnProperty.call(table, source)) return table[source];

      if (source.startsWith('当前：') || source.startsWith('当前:')) {
        const marker = source.startsWith('当前：') ? '当前：' : '当前:';
        const value = source.slice(marker.length).trim();
        return 'Current: ' + (value ? translateForLanguage(value, lang) : '');
      }

      if (source.startsWith('短信发送失败：')) {
        const value = source.slice('短信发送失败：'.length).trim();
        return 'SMS sending failed: ' + (value ? translateForLanguage(value, lang) : '');
      }

      const gettingMatch = source.match(/^获取(.+)中\.\.\.$/);
      if (gettingMatch) return 'Getting ' + translateForLanguage(gettingMatch[1], lang) + '...';

      return source;
    }

    function translate(key) {
      return translateForLanguage(key, currentLanguage);
    }

    function isRenderedFromKey(key, renderedText) {
      const rendered = normalizeText(renderedText);
      if (!key || !rendered) return false;
      if (rendered === normalizeText(key)) return true;
      return supportedLanguages.some((language) => rendered === normalizeText(translateForLanguage(key, language)));
    }

    function resolveI18nKey(currentValue, storedKey) {
      const current = normalizeText(currentValue);
      if (!current) return '';
      if (storedKey && isRenderedFromKey(storedKey, current)) return storedKey;
      return current;
    }

    function withOriginalWhitespace(value, replacement) {
      const text = String(value || '');
      const prefix = (text.match(/^\s*/) || [''])[0];
      const suffix = (text.match(/\s*$/) || [''])[0];
      return prefix + replacement + suffix;
    }

    function skipTextElement(element) {
      if (!element || element.nodeType !== 1) return false;
      const tag = element.tagName.toLowerCase();
      return ['script', 'style', 'svg', 'path', 'code', 'pre', 'textarea'].includes(tag)
        || Boolean(element.closest('[data-no-i18n]'));
    }

    function skipAttributeElement(element) {
      if (!element || element.nodeType !== 1) return false;
      const tag = element.tagName.toLowerCase();
      return ['script', 'style', 'svg', 'path', 'code', 'pre'].includes(tag)
        || Boolean(element.closest('[data-no-i18n]'));
    }

    let applying = false;
    let observer = null;
    let applyTimer = null;
    let autoLoadStarted = false;

    function translateTextNodes(rootNode) {
      if (!rootNode || typeof document === 'undefined') return;
      const start = rootNode.nodeType === 9 ? rootNode.body : rootNode;
      if (!start) return;
      const walker = document.createTreeWalker(start, NodeFilter.SHOW_TEXT, {
        acceptNode(node) {
          const parent = node.parentElement;
          if (!parent || skipTextElement(parent) || !normalizeText(node.nodeValue)) {
            return NodeFilter.FILTER_REJECT;
          }
          return NodeFilter.FILTER_ACCEPT;
        }
      });
      const nodes = [];
      while (walker.nextNode()) nodes.push(walker.currentNode);
      nodes.forEach((node) => {
        const parentKey = node.parentElement && node.parentElement.dataset
          ? node.parentElement.dataset.simpleadminI18nKey
          : '';
        const key = parentKey || resolveI18nKey(node.nodeValue, node.__simpleadminI18nKey);
        if (!key) return;
        node.__simpleadminI18nKey = key;
        const translated = translate(key);
        const nextValue = withOriginalWhitespace(node.nodeValue, translated);
        if (node.nodeValue !== nextValue) node.nodeValue = nextValue;
      });
    }

    function translateAttributes(rootNode) {
      const start = rootNode && rootNode.nodeType === 1 ? rootNode : document;
      if (!start || typeof start.querySelectorAll !== 'function') return;
      const attributes = ['aria-label', 'placeholder', 'title'];
      const elements = [];
      if (start.nodeType === 1) elements.push(start);
      start.querySelectorAll('*').forEach((element) => elements.push(element));
      elements.forEach((element) => {
        if (skipAttributeElement(element)) return;
        attributes.forEach((attr) => {
          if (!element.hasAttribute(attr)) return;
          const storeName = 'simpleadminI18n' + attr.replace(/[^a-z0-9]/gi, '');
          const key = resolveI18nKey(element.getAttribute(attr), element.dataset[storeName]);
          if (!key) return;
          element.dataset[storeName] = key;
          const translated = translate(key);
          if (element.getAttribute(attr) !== translated) element.setAttribute(attr, translated);
        });
      });
    }

    function apply(rootNode) {
      if (typeof document === 'undefined') return currentLanguage;
      applying = true;
      try {
        document.documentElement.setAttribute('lang', currentLanguage);
        translateTextNodes(rootNode || document);
        translateAttributes(rootNode || document);
      } finally {
        applying = false;
      }
      return currentLanguage;
    }

    function scheduleApply(rootNode) {
      if (typeof document === 'undefined') return;
      if (applyTimer) clearTimeout(applyTimer);
      applyTimer = setTimeout(() => {
        applyTimer = null;
        apply(rootNode || document);
      }, 0);
    }

    function startObserver() {
      if (typeof document === 'undefined' || typeof MutationObserver === 'undefined' || !document.body) return;
      if (observer) observer.disconnect();
      observer = new MutationObserver(() => {
        if (!applying) scheduleApply(document);
      });
      observer.observe(document.body, {
        childList: true,
        subtree: true,
        characterData: true,
        attributes: true,
        attributeFilter: ['aria-label', 'placeholder', 'title']
      });
    }

    function setCurrentLanguage(language) {
      const normalized = normalizeLanguage(language) || defaultLanguage;
      currentLanguage = supportedLanguages.includes(normalized) ? normalized : defaultLanguage;
      try { localStorage.setItem(storageKey, currentLanguage); } catch (_) { /* ignore storage errors */ }
      if (typeof document !== 'undefined') apply(document);
      if (typeof window !== 'undefined' && typeof window.dispatchEvent === 'function') {
        window.dispatchEvent(new CustomEvent('simpleadmin:language-changed', { detail: { language: currentLanguage } }));
      }
      return currentLanguage;
    }

    function readLanguageFromResponse(response) {
      if (!response || !response.ok) throw new Error('language response failed');
      return response.json().then((data) => normalizeLanguage(data.language));
    }

    function load() {
      try {
        const stored = normalizeLanguage(localStorage.getItem(storageKey));
        if (stored) currentLanguage = stored;
      } catch (_) { /* ignore storage errors */ }

      const fromApi = root.Api.getLanguage().then(readLanguageFromResponse);

      return fromApi
        .then((language) => {
          if (language) setCurrentLanguage(language);
          else apply(document);
          return currentLanguage;
        })
        .catch(() => {
          apply(document);
          return currentLanguage;
        });
    }

    function save(language) {
      const normalized = setCurrentLanguage(language);
      return root.Api.setLanguage(normalized)
        .then((response) => {
          if (!response.ok) throw new Error('language save failed');
          return response.json();
        })
        .catch((error) => {
          console.warn('Language saved locally but server save failed:', error);
          return { language: normalized, localOnly: true };
        })
        .then(() => normalized);
    }

    function translateMessage(message) {
      return typeof message === 'string' ? translate(message) : message;
    }

    function wrapNativeDialogs() {
      if (typeof window === 'undefined' || window.__simpleadminI18nDialogsBound) return;
      window.__simpleadminI18nDialogsBound = true;

      const nativeAlert = window.alert;
      if (typeof nativeAlert === 'function') {
        window.alert = function (message) {
          return nativeAlert.call(window, translateMessage(message));
        };
      }

      const nativeConfirm = window.confirm;
      if (typeof nativeConfirm === 'function') {
        window.confirm = function (message) {
          return nativeConfirm.call(window, translateMessage(message));
        };
      }
    }

    function startAutoLoad() {
      if (autoLoadStarted || typeof window === 'undefined' || typeof document === 'undefined') return;
      autoLoadStarted = true;
      wrapNativeDialogs();

      if (typeof window.addEventListener === 'function') {
        window.addEventListener('simpleadmin:vue-mounted', () => scheduleApply(document));
      }

      const run = () => {
        load()
          .catch(() => currentLanguage)
          .then(() => {
            startObserver();
            scheduleApply(document);
          });
      };

      if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', run, { once: true });
      } else {
        run();
      }
    }

    startAutoLoad();

    return {
      supportedLanguages,
      normalizeLanguage,
      getCurrentLanguage() { return currentLanguage; },
      t: translate,
      apply,
      load,
      setLanguage(language, options) {
        if (options && options.save) return save(language);
        return Promise.resolve(setCurrentLanguage(language));
      }
    };
  })();


  root.UI = root.UI || {
    setText(selectorOrElement, value) {
      const element = typeof selectorOrElement === 'string'
        ? document.querySelector(selectorOrElement)
        : selectorOrElement;
      if (element) element.textContent = value;
    },
    initDarkMode(buttonId) {
      const html = document.documentElement;
      if (!html) return;

      const storageKey = 'theme';
      const normalizeTheme = (theme) => theme === 'dark' ? 'dark' : 'light';
      const applyTheme = (theme) => {
        const normalized = normalizeTheme(theme);
        html.setAttribute('data-bs-theme', normalized);
        html.style.colorScheme = normalized;
        html.classList.toggle('theme-dark', normalized === 'dark');
        html.classList.toggle('theme-light', normalized === 'light');
        if (document.body) {
          document.body.setAttribute('data-bs-theme', normalized);
          document.body.classList.toggle('theme-dark', normalized === 'dark');
          document.body.classList.toggle('theme-light', normalized === 'light');
        }
        localStorage.setItem(storageKey, normalized);

        const label = normalized === 'dark' ? '浅色模式' : '暗夜模式';
        document.querySelectorAll('.sa-titlebar-theme-toggle').forEach((currentToggle) => {
          currentToggle.dataset.simpleadminI18nKey = label;
          currentToggle.textContent = root.Lang ? root.Lang.t(label) : label;
        });
        const legacyToggle = document.getElementById(buttonId || 'darkModeToggle');
        if (legacyToggle) {
          legacyToggle.dataset.simpleadminI18nKey = label;
          legacyToggle.textContent = root.Lang ? root.Lang.t(label) : label;
        }
      };

      applyTheme(localStorage.getItem(storageKey) || html.getAttribute('data-bs-theme') || 'light');

      const toggles = Array.from(document.querySelectorAll('.sa-titlebar-theme-toggle'));
      const legacyToggle = document.getElementById(buttonId || 'darkModeToggle');
      if (legacyToggle && !toggles.includes(legacyToggle)) toggles.push(legacyToggle);
      toggles.forEach((toggle) => {
        if (!toggle || toggle.dataset.simpleadminDarkModeBound === '1') return;
        toggle.dataset.simpleadminDarkModeBound = '1';
        toggle.addEventListener('click', () => {
          applyTheme(html.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark');
        });
      });
    }
  };

  root.MockAT = root.MockAT || (function () {
    function normalizePayload(payload) {
      if (Array.isArray(payload)) return payload.join('\n');
      return String(payload == null ? '' : payload);
    }

    function logResult(label, result) {
      if (label === 'parse') {
        console.log('[SimpleAdmin MockAT parse]', result);
      } else {
        console.log('[SimpleAdmin MockAT]', result);
      }
      return result;
    }

    function request(params) {
      return root.Api.mockAT(params).then((result) => {
        if (result && result.ok === false) {
          console.warn('[SimpleAdmin MockAT]', result.error || result);
        }
        return result;
      });
    }

    function refreshDashboard() {
      const apps = root.Vue && root.Vue.apps;
      const app = (apps && apps['#dashboardApp']) || (root.Vue && root.Vue.currentApp);
      if (app && typeof app.fetchNetworkInfo === 'function') {
        app.fetchNetworkInfo();
      }
    }

    const api = {
      help() {
        const text = [
          'SimpleAdmin MockAT browser console commands:',
          '  SimpleAdmin.MockAT.at(`paste full dashboard AT response here`)',
          '  SimpleAdmin.MockAT.qca(`paste QCAINFO response here`)',
          '  SimpleAdmin.MockAT.qeng(`paste QENG response here`)',
          '  SimpleAdmin.MockAT.parse()   // print backend parsed dashboard JSON',
          '  SimpleAdmin.MockAT.show()    // show current mock payload status',
          '  SimpleAdmin.MockAT.clear()   // clear all; or clear("at"/"qca"/"qeng")',
          'Aliases: saAt(`...`), saQca(`...`), saQeng(`...`), saParseAT(), saShowAT(), saClearAT()'
        ].join('\n');
        console.log(text);
        return text;
      },
      set(kind, payload) {
        return request({ action: 'set', kind, payload: normalizePayload(payload) })
          .then((result) => {
            refreshDashboard();
            return logResult('set', result);
          });
      },
      at(payload) { return this.set('at', payload); },
      dashboard(payload) { return this.set('dashboard', payload); },
      qca(payload) { return this.set('qca', payload); },
      qcainfo(payload) { return this.set('qcainfo', payload); },
      qeng(payload) { return this.set('qeng', payload); },
      clear(kind = '') {
        return request({ action: 'clear', kind })
          .then((result) => {
            refreshDashboard();
            return logResult('clear', result);
          });
      },
      show() {
        return request({ action: 'show' }).then((result) => logResult('show', result));
      },
      parse(options = {}) {
        const params = Object.assign({ action: 'parse' }, options || {});
        return request(params).then((result) => logResult('parse', result));
      }
    };

    return api;
  })();

  global.saAt = function (payload) { return root.MockAT.at(payload); };
  global.saQca = function (payload) { return root.MockAT.qca(payload); };
  global.saQeng = function (payload) { return root.MockAT.qeng(payload); };
  global.saParseAT = function (options) { return root.MockAT.parse(options); };
  global.saShowAT = function () { return root.MockAT.show(); };
  global.saClearAT = function (kind) { return root.MockAT.clear(kind); };


  root.Logout = root.Logout || (function () {
    function start() {
      const finish = () => { global.location.replace('/login.html'); };
      if (global.fetch) {
        global.fetch('/api/logout', {
          method: 'POST',
          cache: 'no-store',
          credentials: 'same-origin'
        }).then(finish).catch(finish);
        return;
      }
      global.location.replace('/api/logout');
    }

    return { start };
  })();

  root.Vue = root.Vue || {};


  global.SimpleAdmin = root;

  if (root.Brand && typeof root.Brand.init === 'function') {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', root.Brand.init, { once: true });
    } else {
      root.Brand.init();
    }
  }

})(window);
