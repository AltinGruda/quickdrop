/*
 * QuickDrop phone client.
 *
 * Upload protocol summary:
 *   1. Each picked file gets a client-generated uploadId (UUIDv4), persisted in
 *      localStorage, and registered with the server via /upload/init. The
 *      server owns all progress state — the client only ever asks "what do you
 *      have?" (/upload/<id>/status) and sends individual chunks
 *      (/upload/<id>/chunk/<index>).
 *   2. Files are sent at most 3 at a time, one chunk in flight per file. The
 *      rest queue with a visible "waiting" state.
 *   3. Every chunk contributes to an incremental SHA-256. On completion the
 *      client sends the expected hash, which the server verifies against its
 *      own running hash before doing the atomic rename.
 *   4. Any transient failure (dropped Wi-Fi, phone lock, backgrounded Safari)
 *      re-queries status and resumes by sending only the missing chunks.
 *
 * This is a plain script: no build step, no dependencies.
 */
(function () {
  'use strict';

  var BASE = window.QD_BASE || '/';
  var TOKEN = window.QD_TOKEN || '';
  var CHUNK = 2 * 1024 * 1024;       // 2 MiB fixed for v1
  var MAX_ACTIVE = 3;                // concurrent file uploads
  var MAX_RETRIES = 8;               // per-file retry budget
  var LS_PREFIX = 'quickdrop:';

  var fileInput = document.getElementById('fileInput');
  var picker = document.getElementById('picker');
  var list = document.getElementById('list');
  var summary = document.getElementById('summary');
  var allDone = document.getElementById('allDone');
  var someFailed = document.getElementById('someFailed');

  var queue = [];
  var active = 0;
  var totals = { total: 0, done: 0, failed: 0 };

  // ------------------------------------------------------------------ helpers

  function url(path) { return BASE + path; }

  function wait(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  function humanSize(n) {
    if (n >= 1073741824) return (n / 1073741824).toFixed(1) + ' GB';
    if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
    if (n >= 1024) return Math.round(n / 1024) + ' KB';
    return n + ' B';
  }

  function uuid() {
    var b = new Uint8Array(16);
    crypto.getRandomValues(b);
    b[6] = (b[6] & 0x0f) | 0x40; // version 4
    b[8] = (b[8] & 0x3f) | 0x80; // variant 10
    var h = '';
    for (var i = 0; i < 16; i++) {
      if (i === 4 || i === 6 || i === 8 || i === 10) h += '-';
      h += (b[i] < 16 ? '0' : '') + b[i].toString(16);
    }
    return h;
  }

  // An "ApiError" distinguishes transient (retry) from terminal (show it) cases.
  function apiError(message, status, transient) {
    var e = new Error(message);
    e.api = true;
    e.status = status;
    e.transient = !!transient;
    return e;
  }

  function pluckMessage(resp, fallback) {
    return resp.json()
      .then(function (body) {
        if (body && typeof body.message === 'string' && body.message) return body.message;
        return fallback;
      })
      .catch(function () { return fallback; });
  }

  function parseJSON(resp, fallback) {
    return resp.json().catch(function () { return fallback || {}; });
  }

  function readChunk(file, index, chunkSize) {
    var start = index * chunkSize;
    var end = Math.min(start + chunkSize, file.size);
    return file.slice(start, end).arrayBuffer().then(function (ab) {
      return { bytes: new Uint8Array(ab), length: ab.byteLength };
    });
  }

  // ------------------------------------------------------------------- state

  function lsKey(id) { return LS_PREFIX + TOKEN + ':' + id; }

  function persistId(id, name, size) {
    try {
      localStorage.setItem(lsKey(id), JSON.stringify({ name: name, size: size, ts: Date.now() }));
    } catch (e) { /* private mode etc — in-page resume still works */ }
  }

  function dropId(id) {
    try { localStorage.removeItem(lsKey(id)); } catch (e) { /* noop */ }
  }

  // Self-correcting: on page load, discard local upload IDs the server no
  // longer remembers (e.g. this is a fresh session with a new token).
  function cleanStaleEntries() {
    if (!TOKEN) return;
    try {
      for (var i = localStorage.length - 1; i >= 0; i--) {
        var k = localStorage.key(i);
        if (!k || k.indexOf(LS_PREFIX + TOKEN + ':') !== 0) continue;
        var id = k.substring(LS_PREFIX.length + TOKEN.length + 1);
        fetch(url('/upload/' + encodeURIComponent(id) + '/status'), {
          headers: { Accept: 'application/json' }
        }).then(function (r) {
          if (r.status === 404) localStorage.removeItem(k);
        }).catch(function () { /* offline; leave it */ });
      }
    } catch (e) { /* noop */ }
  }

  // ------------------------------------------------------------------ row UI

  function makeItem(file) {
    var id = uuid();
    var row = document.createElement('div');
    row.className = 'row';
    row.innerHTML =
      '<div class="name"></div>' +
      '<div class="meta"></div>' +
      '<div class="bar"><i></i></div>' +
      '<div class="status">Waiting…</div>';
    row.querySelector('.name').textContent = file.name;
    row.querySelector('.meta').textContent = humanSize(file.size);
    list.appendChild(row);
    return {
      file: file,
      id: id,
      row: row,
      chunkSize: CHUNK,
      nChunks: Math.max(1, Math.ceil(file.size / CHUNK)),
      state: 'waiting',
      retries: 0
    };
  }

  function setStatus(item, text) {
    item.row.querySelector('.status').textContent = text;
  }

  function setLive(item, bytesReceived, total) {
    var pct = total > 0 ? Math.round(bytesReceived / total * 100) : 0;
    item.row.querySelector('.bar > i').style.width = pct + '%';
    setStatus(item, pct + '% of ' + humanSize(total));
  }

  function markDone(item, finalName) {
    item.state = 'done';
    item.row.classList.add('done');
    item.row.querySelector('.bar > i').style.width = '100%';
    var note = 'Done';
    if (finalName && finalName !== item.file.name) note += ' (saved as ' + finalName + ')';
    setStatus(item, note);
    totals.done++;
    dropId(item.id);
    reconcile();
  }

  function markFailed(item, message) {
    item.state = 'failed';
    item.row.classList.add('error');
    setStatus(item, message);
    totals.failed++;
    dropId(item.id);
    reconcile();
  }

  function reconcile() {
    var settled = totals.done + totals.failed;
    if (settled === 0) {
      summary.textContent = '';
    } else if (settled < totals.total) {
      summary.textContent = 'Sending ' + settled + ' of ' + totals.total + ' files…';
    } else {
      summary.textContent = '';
      if (totals.failed === 0) {
        allDone.style.display = 'block';
        picker.disabled = false;
      } else {
        someFailed.style.display = 'block';
        picker.disabled = false;
      }
    }
  }

  // ---------------------------------------------------------------- protocol

  function establish(item) {
    return fetch(url('/upload/' + encodeURIComponent(item.id) + '/status'), {
      headers: { Accept: 'application/json' }
    }).then(function (resp) {
      if (resp.ok) return parseJSON(resp, {}).then(function (st) {
        if (st.state === 'failed' || st.state === 'expired') {
          // The server's copy is dead; start again with a fresh identity.
          item.id = uuid();
          dropId(item.id);
          return register(item).then(function (init) {
            return freshState(init, item);
          });
        }
        return st; // resume from wherever the server is
      });
      if (resp.status === 404) {
        // Unknown to the server (fresh session, expired state): create it.
        return register(item).then(function (init) {
          return freshState(init, item);
        });
      }
      return pluckMessage(resp, 'Could not start the upload.').then(function (msg) {
        throw apiError(msg, resp.status, false);
      });
    });
  }

  function freshState(init, item) {
    return {
      chunkSize: init.chunkSize || item.chunkSize,
      totalSize: init.totalSize,
      receivedChunkCount: 0,
      nextChunk: 0,
      complete: false,
      state: init.state
    };
  }

  function register(item) {
    var body = JSON.stringify({
      uploadId: item.id,
      filename: item.file.name,
      totalSize: item.file.size,
      chunkSize: item.chunkSize
    });
    return fetch(url('/upload/init'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: body
    }).then(function (resp) {
      if (resp.ok) {
        persistId(item.id, item.file.name, item.file.size);
        return parseJSON(resp, {});
      }
      if (resp.status === 409) {
        // uploadId collided with existing state for different parameters —
        // back off with a brand-new identity and try once.
        item.id = uuid();
        dropId(item.id);
        return register(item);
      }
      return pluckMessage(resp, 'Could not start the upload.').then(function (msg) {
        throw apiError(msg, resp.status, false);
      });
    });
  }

  function refreshStatus(item) {
    return fetch(url('/upload/' + encodeURIComponent(item.id) + '/status'), {
      headers: { Accept: 'application/json' }
    }).then(function (resp) {
      if (resp.ok) return parseJSON(resp, {});
      return pluckMessage(resp, 'Lost the connection.').then(function (msg) {
        throw apiError(msg, resp.status, true);
      });
    });
  }

  // Sends the whole file. Every attempt recomputes the hash deterministically
  // by (re-)feeding the chunks the server already confirms it has, then the
  // missing ones — so the final hash always matches the phone-side file.
  function transfer(item, st) {
    var hash = sha256();
    var chunkSize = st.chunkSize || item.chunkSize;
    var totalSize = st.totalSize || item.file.size;
    var nChunks = Math.max(1, Math.ceil(totalSize / chunkSize));
    var idx = (typeof st.nextChunk === 'number') ? st.nextChunk : (st.receivedChunkCount || 0);

    function prefeed(upTo) {
      // Feed chunks 0..upTo-1 into the hash (they're already on the server from
      // a previous attempt; re-hashing from the file keeps the digest exact).
      var p = Promise.resolve();
      for (var i = 0; i < upTo; i++) {
        (function (i) {
          p = p.then(function () {
            return readChunk(item.file, i, chunkSize).then(function (c) {
              hash.update(c.bytes);
            });
          });
        })(i);
      }
      return p;
    }

    return prefeed(idx).then(function () {
      function next() {
        if (idx >= nChunks) return complete();
        return readChunk(item.file, idx, chunkSize).then(function (c) {
          hash.update(c.bytes);
          var chunkURL = url('/upload/' + encodeURIComponent(item.id) + '/chunk/' + idx);
          return fetch(chunkURL, {
            method: 'POST',
            headers: { 'Content-Type': 'application/octet-stream' },
            body: c.bytes
          }).then(function (resp) {
            if (resp.ok) {
              setLive(item, c.length + idx * chunkSize, totalSize);
              idx++;
              return next();
            }
            if (resp.status === 409) {
              // Out of sync (e.g. a duplicate or gap): let the server tell us
              // where to continue, then re-run the deterministic transfer.
              if (item.retries >= MAX_RETRIES) throw apiError('Couldn\'t finish — please try again.', 409, false);
              item.retries++;
              setStatus(item, 'Resuming…');
              return refreshStatus(item).then(function (ns) {
                return transfer(item, ns);
              });
            }
            if (resp.status === 400 || resp.status === 413 || resp.status === 422) {
              return pluckMessage(resp, 'Sending failed — please try again.').then(function (msg) {
                throw apiError(msg, resp.status, false);
              });
            }
            if (resp.status === 404) {
              return pluckMessage(resp, 'This session ended — start a new one.').then(function (msg) {
                throw apiError(msg, 404, false);
              });
            }
            return pluckMessage(resp, 'Sending failed.').then(function (msg) {
              throw apiError(msg, resp.status, true);
            });
          });
        });
      }

      function complete() {
        var expected = hex(hash.digest());
        return fetch(url('/upload/' + encodeURIComponent(item.id) + '/complete'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hash: expected })
        }).then(function (resp) {
          if (resp.ok) return parseJSON(resp, {}).then(function (r) {
            markDone(item, r.finalName);
          });
          if (resp.status === 409) {
            // A few chunks didn't land; ask status and finish them.
            if (item.retries >= MAX_RETRIES) throw apiError('Couldn\'t finish — please try again.', 409, false);
            item.retries++;
            return refreshStatus(item).then(function (ns) {
              return transfer(item, ns);
            });
          }
          return pluckMessage(resp, 'Transfer failed — please try again.').then(function (msg) {
            throw apiError(msg, resp.status, false);
          });
        });
      }

      return next();
    });
  }

  function startUpload(item) {
    item.state = 'uploading';
    return establish(item).then(function (st) {
      return transfer(item, st);
    }).catch(function (e) {
      var transient = e && e.api ? e.transient : true;
      if (transient && item.retries < MAX_RETRIES) {
        item.retries++;
        var delay = Math.min(1000 * Math.pow(1.7, item.retries - 1), 15000);
        setStatus(item, 'Reconnecting…');
        return wait(delay).then(function () {
          return startUpload(item);
        });
      }
      var msg = (e && e.message) ? e.message : 'Couldn\'t finish — please try again.';
      markFailed(item, msg);
    });
  }

  // ---------------------------------------------------------------- scheduler

  function kick() {
    while (active < MAX_ACTIVE && queue.length > 0) {
      var item = queue.shift();
      active++;
      startUpload(item).then(function () {
        active--;
        kick();
      });
    }
  }

  function pickFiles(files) {
    var added = false;
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      if (!f || f.size <= 0) continue;
      var item = makeItem(f);
      queue.push(item);
      totals.total++;
      added = true;
    }
    if (added) {
      picker.disabled = true;
      allDone.style.display = 'none';
      someFailed.style.display = 'none';
      summarizePending();
      kick();
    }
  }

  function summarizePending() {
    var queued = queue.length;
    if (queued > 0) summary.textContent = 'Waiting to send ' + queued + (queued === 1 ? ' file…' : ' files…');
  }

  // ------------------------------------------------------------------- boot

  picker.addEventListener('click', function () { fileInput.click(); });

  fileInput.addEventListener('change', function () {
    var files = Array.prototype.slice.call(fileInput.files || []);
    fileInput.value = '';
    if (files.length) pickFiles(files);
  });

  // Connectivity handshake: phones that load the page immediately ping the
  // server; the PC uses this to detect Wi-Fi networks that isolate devices.
  fetch(url('/ping'), { credentials: 'omit' }).catch(function () { /* ignored */ });

  cleanStaleEntries();

  // ------------------------------------------------------------------- sha256

  function rotr(x, n) { return (x >>> n) | (x << (32 - n)); }

  function sha256() {
    var H = new Int32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
                            0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    var K = new Int32Array([0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
                            0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
                            0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
                            0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
                            0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
                            0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
                            0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
                            0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
                            0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
                            0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
                            0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
                            0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
                            0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
                            0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
                            0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
                            0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2]);
    var buf = new Uint8Array(64);
    var bufLen = 0;
    var byteLen = 0;

    function processBlock() {
      var w = new Int32Array(64);
      for (var i = 0; i < 16; i++) {
        w[i] = (buf[i * 4] << 24) | (buf[i * 4 + 1] << 16) | (buf[i * 4 + 2] << 8) | buf[i * 4 + 3];
      }
      for (var i = 16; i < 64; i++) {
        var s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
        var s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) | 0;
      }
      var a = H[0], b = H[1], c = H[2], d = H[3], e = H[4], f2 = H[5], g = H[6], h = H[7];
      for (var i = 0; i < 64; i++) {
        var S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
        var ch = (e & f2) ^ (~e & g);
        var t1 = (h + S1 + ch + K[i] + w[i]) | 0;
        var S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
        var maj = (a & b) ^ (a & c) ^ (b & c);
        var t2 = (S0 + maj) | 0;
        h = g; g = f2; f2 = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      H[0] = (H[0] + a) | 0; H[1] = (H[1] + b) | 0; H[2] = (H[2] + c) | 0; H[3] = (H[3] + d) | 0;
      H[4] = (H[4] + e) | 0; H[5] = (H[5] + f2) | 0; H[6] = (H[6] + g) | 0; H[7] = (H[7] + h) | 0;
    }

    function update(input) {
      var n = input.length;
      byteLen += n;
      var off = 0;
      if (bufLen > 0) {
        var take = Math.min(64 - bufLen, n);
        buf.set(input.subarray(0, take), bufLen);
        bufLen += take;
        off += take;
        if (bufLen === 64) { processBlock(); bufLen = 0; }
      }
      while (off + 64 <= n) {
        buf.set(input.subarray(off, off + 64));
        processBlock();
        off += 64;
      }
      if (off < n) {
        var rest = n - off;
        buf.set(input.subarray(off, off + rest), 0);
        bufLen = rest;
      }
    }

    function digest() {
      var high = Math.floor(byteLen / 536870912);   // bit length, high 32 bits
      var low = (byteLen << 3) >>> 0;               // bit length, low 32 bits
      buf[bufLen++] = 0x80;
      if (bufLen > 56) {
        buf.fill(0, bufLen, 64);
        bufLen = 64;
        processBlock();
        bufLen = 0;
      }
      buf.fill(0, bufLen, 56);
      buf[56] = (high >>> 24) & 0xff;
      buf[57] = (high >>> 16) & 0xff;
      buf[58] = (high >>> 8) & 0xff;
      buf[59] = high & 0xff;
      buf[60] = (low >>> 24) & 0xff;
      buf[61] = (low >>> 16) & 0xff;
      buf[62] = (low >>> 8) & 0xff;
      buf[63] = low & 0xff;
      bufLen = 64;
      processBlock();

      var out = new Uint8Array(32);
      for (var i = 0; i < 8; i++) {
        var v = H[i];
        out[i * 4] = (v >>> 24) & 0xff;
        out[i * 4 + 1] = (v >>> 16) & 0xff;
        out[i * 4 + 2] = (v >>> 8) & 0xff;
        out[i * 4 + 3] = v & 0xff;
      }
      return out;
    }

    return { update: update, digest: digest };
  }

  function hex(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i++) {
      var x = bytes[i];
      s += (x < 16 ? '0' : '') + x.toString(16);
    }
    return s;
  }
})();