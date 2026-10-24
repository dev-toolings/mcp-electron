// The pid-attach bridge, evaluated in an Electron main process through its Node
// inspector (MainClient.Eval: `require` and the electron modules are in scope).
//
// Chromium only opens --remote-debugging-port at startup, so an app launched
// without it has no CDP endpoint. This bridge makes one inside the app: a
// loopback WebSocket server speaking the browser-level part of CDP that rod
// and the engine use (Target.*, a few Browser.*), and relaying every
// session-level command and event to the window's webContents.debugger.
//
// The endpoint is ws://127.0.0.1:PORT/devtools/browser/TOKEN (TOKEN: 128 random
// bits); every other path is refused, /json/version included, so the token is
// never served to another local process. The bridge stops itself a few
// seconds after its last client leaves: debuggers it attached are detached,
// the server is closed, nothing stays in the app.
(async () => {
  const previous = globalThis.__mcpElectronBridge;
  if (previous && previous.alive()) return { port: previous.port, token: previous.token, reused: true };

  const http = require('http');
  const crypto = require('crypto');
  const { webContents, BrowserWindow, app } = require('electron');
  const token = crypto.randomBytes(16).toString('hex');
  const wsPath = '/devtools/browser/' + token;
  const GRACE_MS = 5000;

  const clients = new Set();
  const targets = new Map(); // targetId -> webContents
  const idOf = new Map(); // webContents.id -> targetId
  const ours = new Set(); // webContents.id whose debugger this bridge attached
  const sessions = new Map(); // sessionId -> { wc, inner, client }
  const cleanups = [];
  let discover = new Set(); // clients that asked for Target.setDiscoverTargets
  let idleTimer = null;
  let closed = false;

  const usable = (wc) => wc && !wc.isDestroyed() && ['window', 'browserView', 'webview'].includes(wc.getType());
  const send = (client, message) => { if (!client.closed) client.sendText(JSON.stringify(message)); };
  const info = (wc, targetId) => ({
    targetId, type: wc.getType() === 'webview' ? 'webview' : 'page', title: wc.getTitle(), url: wc.getURL(),
    attached: [...sessions.values()].some((s) => s.wc === wc), canAccessOpener: false, browserContextId: 'mcp-electron-bridge',
  });

  const forget = (wc, reason) => {
    const targetId = idOf.get(wc.id);
    for (const [sessionId, s] of sessions) {
      if (s.wc !== wc) continue;
      sessions.delete(sessionId);
      send(s.client, { method: 'Target.detachedFromTarget', params: { sessionId, targetId, reason } });
    }
    if (targetId === undefined) return;
    idOf.delete(wc.id);
    targets.delete(targetId);
    ours.delete(wc.id);
    for (const client of discover) send(client, { method: 'Target.targetDestroyed', params: { targetId } });
  };

  // Attaches the window's debugger once; its main frame id is the target id (rod uses it as the frame id).
  const pending = new Map();
  const ensure = (wc) => {
    if (idOf.has(wc.id)) return Promise.resolve(idOf.get(wc.id));
    if (pending.has(wc.id)) return pending.get(wc.id);
    const work = (async () => {
      const dbg = wc.debugger;
      if (!dbg.isAttached()) { dbg.attach('1.3'); ours.add(wc.id); }
      const onMessage = (_event, method, params, inner) => {
        if (inner) {
          const s = sessions.get(inner);
          if (s) send(s.client, { method, params, sessionId: inner });
        } else {
          for (const [sessionId, s] of sessions) {
            if (s.wc !== wc || s.inner) continue;
            // A child session (iframe, worker) the page auto-attached is addressed by its own id.
            if (method === 'Target.attachedToTarget' && params && params.sessionId) sessions.set(params.sessionId, { wc, inner: params.sessionId, client: s.client });
            if (method === 'Target.detachedFromTarget' && params && params.sessionId) sessions.delete(params.sessionId);
            send(s.client, { method, params, sessionId });
          }
        }
      };
      const onDetach = (_event, reason) => forget(wc, reason || 'detached');
      const onDestroyed = () => forget(wc, 'target closed');
      const onInfo = () => { const id = idOf.get(wc.id); if (id !== undefined) for (const client of discover) send(client, { method: 'Target.targetInfoChanged', params: { targetInfo: info(wc, id) } }); };
      dbg.on('message', onMessage);
      dbg.on('detach', onDetach);
      wc.once('destroyed', onDestroyed);
      wc.on('did-navigate', onInfo);
      wc.on('page-title-updated', onInfo);
      cleanups.push(() => {
        if (wc.isDestroyed()) return;
        dbg.removeListener('message', onMessage);
        dbg.removeListener('detach', onDetach);
        wc.removeListener('destroyed', onDestroyed);
        wc.removeListener('did-navigate', onInfo);
        wc.removeListener('page-title-updated', onInfo);
        if (ours.has(wc.id) && dbg.isAttached()) { try { dbg.detach(); } catch (_e) {} }
      });
      const { frameTree } = await dbg.sendCommand('Page.getFrameTree');
      const targetId = frameTree.frame.id;
      idOf.set(wc.id, targetId);
      targets.set(targetId, wc);
      return targetId;
    })();
    pending.set(wc.id, work);
    work.finally(() => pending.delete(wc.id)).catch(() => {});
    return work;
  };
  const all = async () => {
    const list = [];
    for (const wc of webContents.getAllWebContents()) {
      if (!usable(wc)) continue;
      try { list.push(info(wc, await ensure(wc))); } catch (_e) { /* a window that refuses a debugger stays out of the list */ }
    }
    return list;
  };
  const onCreated = (_event, wc) => {
    wc.once('dom-ready', () => {
      if (closed || !usable(wc)) return;
      ensure(wc).then((targetId) => { for (const client of discover) send(client, { method: 'Target.targetCreated', params: { targetInfo: info(wc, targetId) } }); }).catch(() => {});
    });
  };
  app.on('web-contents-created', onCreated);
  cleanups.push(() => app.removeListener('web-contents-created', onCreated));

  const windowOf = (targetId) => { const wc = targets.get(targetId); return wc && !wc.isDestroyed() ? BrowserWindow.fromWebContents(wc) : null; };
  const boundsOf = (win) => { const b = win.getBounds(); return { left: b.x, top: b.y, width: b.width, height: b.height, windowState: win.isMinimized() ? 'minimized' : win.isFullScreen() ? 'fullscreen' : win.isMaximized() ? 'maximized' : 'normal' }; };
  const fail = (message) => { const e = new Error(message); e.cdp = true; throw e; };

  const browserCommand = async (client, method, params) => {
    switch (method) {
      case 'Browser.getVersion':
        return { protocolVersion: '1.3', product: 'Electron/' + process.versions.electron + ' (mcp-electron bridge)', revision: '', userAgent: app.userAgentFallback, jsVersion: process.versions.v8 };
      case 'Browser.getBrowserCommandLine':
        return { arguments: process.argv };
      case 'Browser.setDownloadBehavior':
      case 'Target.setAutoAttach':
      case 'Target.activateTarget': // never steal the user's focus
        return {};
      case 'Target.setDiscoverTargets': {
        if (!params.discover) { discover.delete(client); return {}; }
        discover.add(client);
        for (const targetInfo of await all()) send(client, { method: 'Target.targetCreated', params: { targetInfo } });
        return {};
      }
      case 'Target.getTargets':
        return { targetInfos: await all() };
      case 'Target.getTargetInfo': {
        if (!params.targetId) return { targetInfo: { targetId: 'browser', type: 'browser', title: '', url: '', attached: true, canAccessOpener: false } };
        const wc = targets.get(params.targetId);
        if (!wc || wc.isDestroyed()) fail('No target with given id found');
        return { targetInfo: info(wc, params.targetId) };
      }
      case 'Target.attachToTarget': {
        const wc = targets.get(params.targetId);
        if (!wc || wc.isDestroyed()) fail('No target with given id found');
        const sessionId = crypto.randomBytes(16).toString('hex').toUpperCase();
        sessions.set(sessionId, { wc, inner: undefined, client });
        send(client, { method: 'Target.attachedToTarget', params: { sessionId, targetInfo: info(wc, params.targetId), waitingForDebugger: false } });
        return { sessionId };
      }
      case 'Target.detachFromTarget': {
        const s = sessions.get(params.sessionId);
        if (!s) fail('No session with given id');
        sessions.delete(params.sessionId);
        return {};
      }
      case 'Browser.getWindowForTarget': {
        const win = windowOf(params.targetId);
        if (!win) fail('Browser window not found');
        return { windowId: win.id, bounds: boundsOf(win) };
      }
      case 'Browser.getWindowBounds': {
        const win = BrowserWindow.fromId(params.windowId);
        if (!win) fail('Browser window not found');
        return { bounds: boundsOf(win) };
      }
      case 'Browser.setWindowBounds': {
        const win = BrowserWindow.fromId(params.windowId);
        if (!win) fail('Browser window not found');
        const b = params.bounds || {};
        const now = win.getBounds();
        win.setBounds({ x: b.left ?? now.x, y: b.top ?? now.y, width: b.width ?? now.width, height: b.height ?? now.height });
        return {};
      }
      default:
        fail(method + ' is not available when mcp-electron is attached by pid (no new windows, contexts or browser shutdown)');
    }
  };

  const handle = async (client, text) => {
    let message;
    try { message = JSON.parse(text); } catch (_e) { return; }
    const { id, method, params = {}, sessionId } = message;
    try {
      let result;
      if (!sessionId) result = await browserCommand(client, method, params);
      else {
        const s = sessions.get(sessionId);
        if (!s || s.wc.isDestroyed()) fail('Session with given id not found');
        result = await s.wc.debugger.sendCommand(method, params, s.inner);
      }
      send(client, { id, result: result || {}, ...(sessionId ? { sessionId } : {}) });
    } catch (e) {
      send(client, { id, error: { code: -32000, message: e && e.message ? e.message : String(e) }, ...(sessionId ? { sessionId } : {}) });
    }
  };

  // A minimal RFC 6455 server: text frames, fragmentation, ping/pong, close. Clients mask, the server does not.
  const frame = (opcode, payload) => {
    const length = payload.length;
    const head = length < 126 ? Buffer.from([0x80 | opcode, length]) : length < 65536 ? Buffer.from([0x80 | opcode, 126, length >> 8, length & 255]) : Buffer.concat([Buffer.from([0x80 | opcode, 127]), (() => { const b = Buffer.alloc(8); b.writeBigUInt64BE(BigInt(length)); return b; })()]);
    return Buffer.concat([head, payload]);
  };
  const accept = (socket) => {
    let buffer = Buffer.alloc(0);
    let parts = [];
    const client = {
      closed: false,
      sendText: (text) => { if (!client.closed) socket.write(frame(1, Buffer.from(text, 'utf8'))); },
      close: () => { if (client.closed) return; client.closed = true; try { socket.end(frame(8, Buffer.alloc(0))); } catch (_e) {} },
    };
    clients.add(client);
    clearTimeout(idleTimer);
    const gone = () => {
      if (!clients.delete(client)) return;
      client.closed = true;
      discover.delete(client);
      for (const [sessionId, s] of sessions) if (s.client === client) sessions.delete(sessionId);
      if (clients.size === 0) idleTimer = setTimeout(() => { if (clients.size === 0) bridge.close(); }, GRACE_MS);
    };
    socket.on('close', gone);
    socket.on('error', gone);
    socket.on('data', (chunk) => {
      buffer = Buffer.concat([buffer, chunk]);
      for (;;) {
        if (buffer.length < 2) return;
        const fin = (buffer[0] & 0x80) !== 0, opcode = buffer[0] & 0x0f, masked = (buffer[1] & 0x80) !== 0;
        let length = buffer[1] & 0x7f, offset = 2;
        if (length === 126) { if (buffer.length < 4) return; length = buffer.readUInt16BE(2); offset = 4; }
        else if (length === 127) { if (buffer.length < 10) return; length = Number(buffer.readBigUInt64BE(2)); offset = 10; }
        if (!masked) { client.close(); socket.destroy(); return; }
        if (buffer.length < offset + 4 + length) return;
        const mask = buffer.subarray(offset, offset + 4);
        const payload = Buffer.from(buffer.subarray(offset + 4, offset + 4 + length));
        for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
        buffer = buffer.subarray(offset + 4 + length);
        if (opcode === 8) { client.close(); socket.destroy(); return; }
        if (opcode === 9) { socket.write(frame(10, payload)); continue; }
        if (opcode === 10) continue;
        if (opcode === 1 || opcode === 0) {
          parts.push(payload);
          if (fin) { const text = Buffer.concat(parts).toString('utf8'); parts = []; handle(client, text); }
        }
      }
    });
  };

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === wsPath + '/json/version') {
      const port = server.address().port;
      // No keep-alive: a probe must never reach a bridge that has stopped through a connection it kept.
      res.writeHead(200, { 'Content-Type': 'application/json', Connection: 'close' });
      res.end(JSON.stringify({ Browser: 'Electron/' + process.versions.electron + ' (mcp-electron bridge)', 'Protocol-Version': '1.3', webSocketDebuggerUrl: 'ws://127.0.0.1:' + port + wsPath }));
      return;
    }
    res.writeHead(404, { Connection: 'close' });
    res.end();
  });
  server.on('upgrade', (req, socket) => {
    const key = req.headers['sec-websocket-key'];
    if (req.url !== wsPath || !key || closed) { socket.end('HTTP/1.1 403 Forbidden\r\n\r\n'); return; }
    const acceptKey = crypto.createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
    socket.setNoDelay(true);
    socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + acceptKey + '\r\n\r\n');
    accept(socket);
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  // Nobody connected within the grace period (the server gave up): stop, as after the last client.
  idleTimer = setTimeout(() => { if (clients.size === 0) bridge.close(); }, GRACE_MS * 6);

  const bridge = {
    port: server.address().port,
    token,
    alive: () => !closed && server.listening,
    close: () => {
      if (closed) return;
      closed = true;
      clearTimeout(idleTimer);
      for (const client of clients) client.close();
      clients.clear();
      sessions.clear();
      for (const cleanup of cleanups.splice(0)) { try { cleanup(); } catch (_e) {} }
      server.close();
      if (typeof server.closeAllConnections === 'function') server.closeAllConnections();
      if (globalThis.__mcpElectronBridge === bridge) delete globalThis.__mcpElectronBridge;
    },
  };
  globalThis.__mcpElectronBridge = bridge;
  return { port: bridge.port, token, reused: false };
})()
