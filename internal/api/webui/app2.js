async function apiGet(path) {
  const r = await fetch(path, { headers: { 'Accept': 'application/json' } });
  if (!r.ok) throw new Error(await r.text());
  return await r.json();
}

async function apiPostJson(path, body) {
  const r = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
    body: JSON.stringify(body || {})
  });
  if (!r.ok) throw new Error(await r.text());
  return await r.json();
}

async function apiPutJson(path, body) {
  const r = await fetch(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
    body: JSON.stringify(body || {})
  });
  if (!r.ok) throw new Error(await r.text());
  return await r.json();
}

function el(tag, attrs = {}, children = []) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') n.className = v;
    else if (k === 'text') n.textContent = v;
    else n.setAttribute(k, v);
  }
  for (const c of children) n.appendChild(c);
  return n;
}

function fmtTime(ts) {
  if (!ts) return '';
  return String(ts).replace('T',' ').replace('Z','');
}

function _num(v) {
  const s = String(v == null ? '' : v).trim();
  const n = Number(s);
  if (Number.isFinite(n)) return n;
  const m = s.match(/\d+/);
  return m ? Number(m[0]) : 0;
}

function fmtSize(n) {
  const x = Number(n || 0);
  if (!Number.isFinite(x) || x <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = x;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 10 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

function setStatus(id, text) {
  const n = document.getElementById(id);
  if (n) n.textContent = text || '';
}

function showPage(page) {
  for (const sec of document.querySelectorAll('section[id^="page_"]')) sec.classList.add('hide');
  const target = document.getElementById(`page_${page}`);
  if (target) target.classList.remove('hide');
  for (const item of document.querySelectorAll('.navItem')) item.classList.toggle('active', item.dataset.page === page);
}

function renderCrumbs(id, fullPath, rootPath, onPick) {
  const box = document.getElementById(id);
  if (!box) return;
  box.innerHTML = '';
  const root = String(rootPath || '/');
  const current = String(fullPath || root);
  const rel = current.startsWith(root) ? current.slice(root.length) : '';
  const parts = rel.split('/').filter(Boolean);
  const chain = [{ label: root, path: root }];
  let acc = root;
  for (const p of parts) {
    acc = `${acc.replace(/\/$/, '')}/${p}`;
    chain.push({ label: p, path: acc });
  }
  chain.forEach((c, idx) => {
    const btn = el('span', { class: 'crumb', text: c.label });
    btn.onclick = () => onPick(c.path);
    box.appendChild(btn);
    if (idx < chain.length - 1) box.appendChild(el('span', { class: 'crumbSep', text: '›' }));
  });
}

async function restartNow() {
  setStatus('setStatus', 'Reiniciando...');
  try {
    await apiPostJson('/api/v1/restart', {});
    setStatus('setStatus', 'Reinicio solicitado');
  } catch (e) {
    setStatus('setStatus', 'Error: ' + String(e));
  }
}

async function loadUploadSettings() {
  const cfg = await apiGet('/api/v1/config');
  const up = cfg.upload || {};
  const ng = cfg.ngpost || {};
  const par = (up.par || {});
  const tm = ((cfg.metadata || {}).tmdb || {});
  const fb = (cfg.filebot || {});
  const backups = cfg.backups || {};

  const setVal = (id, v) => { const n = document.getElementById(id); if (n) n.value = v ?? ''; };
  const setChk = (id, v) => { const n = document.getElementById(id); if (n) n.checked = !!v; };

  setChk('setWatchMediaEnabled', !!((cfg.watch || {}).media || {}).enabled);
  setVal('setWatchMediaDir', ((cfg.watch || {}).media || {}).dir || '/host/inbox/media');
  setChk('setWatchMediaRecursive', !!((cfg.watch || {}).media || {}).recursive);

  setVal('setNntpHost', ng.host || '');
  setVal('setNntpPort', ng.port || 563);
  setChk('setNntpSSL', ng.ssl !== false);
  setVal('setNntpUser', ng.user || '');
  setVal('setNntpPass', ng.pass ? '********' : '');
  setVal('setNntpConnections', ng.connections || 20);
  setVal('setNntpThreads', ng.threads || 2);
  setVal('setNntpGroups', ng.groups || '');
  setChk('setNntpObfuscate', !!ng.obfuscate);

  setChk('setParEnabled', !!par.enabled);
  setVal('setParRedundancy', par.redundancy_percent || 20);
  setChk('setParKeepFiles', !!par.keep_parity_files);
  setVal('setParDir', par.dir || '/host/inbox/par2');
  setVal('setParMediaPathMode', par.media_path_mode || 'auto');

  setChk('setTMDBEnabled', !!tm.enabled);
  setVal('setTMDBApiKey', '');
  setVal('setTMDBLanguage', tm.language || 'es-ES');

  setVal('setFileBotDB', fb.db || 'TheMovieDB');
  setVal('setFileBotLanguage', fb.language || 'es');
  setVal('setFileBotMovieFormat', fb.movie_format || '');
  setVal('setFileBotSeriesFormat', fb.series_format || '');
  setVal('setFileBotBinary', fb.binary || '/usr/local/bin/filebot');

  setChk('setBackupsEnabled', !!backups.enabled);
  setChk('setBackupsCompress', !!backups.compress);
  setVal('setBackupsDir', backups.dir || '/backups');
  setVal('setBackupsEvery', backups.every_minutes || 0);
  setVal('setBackupsKeep', backups.keep || 30);

  setStatus('setStatus', '');
  refreshBackupsList().catch(() => {});
}

async function saveUploadSettings() {
  const cfg = await apiGet('/api/v1/config');
  const val = (id) => document.getElementById(id)?.value ?? '';
  const chk = (id) => !!document.getElementById(id)?.checked;

  cfg.upload = cfg.upload || {};
  cfg.upload.provider = 'nyuu';
  cfg.upload.par = cfg.upload.par || {};
  cfg.watch = cfg.watch || {};
  cfg.watch.media = cfg.watch.media || {};
  cfg.ngpost = cfg.ngpost || {};
  cfg.metadata = cfg.metadata || {};
  cfg.metadata.tmdb = cfg.metadata.tmdb || {};
  cfg.filebot = cfg.filebot || {};
  cfg.backups = cfg.backups || {};

  cfg.watch.media.enabled = chk('setWatchMediaEnabled');
  cfg.watch.media.dir = val('setWatchMediaDir').trim();
  cfg.watch.media.recursive = chk('setWatchMediaRecursive');

  cfg.ngpost.host = val('setNntpHost').trim();
  cfg.ngpost.port = _num(val('setNntpPort')) || 563;
  cfg.ngpost.ssl = chk('setNntpSSL');
  cfg.ngpost.user = val('setNntpUser');
  const pass = val('setNntpPass');
  if (String(pass).trim() && pass !== '********') cfg.ngpost.pass = pass;
  cfg.ngpost.connections = _num(val('setNntpConnections')) || 20;
  cfg.ngpost.threads = _num(val('setNntpThreads')) || 2;
  cfg.ngpost.groups = val('setNntpGroups').trim();
  cfg.ngpost.obfuscate = chk('setNntpObfuscate');

  cfg.upload.par.enabled = chk('setParEnabled');
  cfg.upload.par.redundancy_percent = _num(val('setParRedundancy')) || 20;
  cfg.upload.par.keep_parity_files = chk('setParKeepFiles');
  cfg.upload.par.dir = val('setParDir').trim();
  cfg.upload.par.media_path_mode = val('setParMediaPathMode') || 'auto';

  cfg.metadata.tmdb.enabled = chk('setTMDBEnabled');
  cfg.metadata.tmdb.language = val('setTMDBLanguage').trim();
  const tmdbKey = val('setTMDBApiKey').trim();
  if (tmdbKey) cfg.metadata.tmdb.api_key = tmdbKey;

  cfg.filebot.db = val('setFileBotDB').trim();
  cfg.filebot.language = val('setFileBotLanguage').trim();
  cfg.filebot.movie_format = val('setFileBotMovieFormat');
  cfg.filebot.series_format = val('setFileBotSeriesFormat');
  cfg.filebot.binary = val('setFileBotBinary').trim();

  cfg.backups.enabled = chk('setBackupsEnabled');
  cfg.backups.compress = chk('setBackupsCompress');
  cfg.backups.dir = val('setBackupsDir').trim();
  cfg.backups.every_minutes = _num(val('setBackupsEvery'));
  cfg.backups.keep = _num(val('setBackupsKeep')) || 30;

  setStatus('setStatus', 'Guardando...');
  await apiPutJson('/api/v1/config', cfg);
  setStatus('setStatus', 'Guardado. Reiniciando...');
  await apiPostJson('/api/v1/restart', {});
}

async function refreshLogsJobs() {
  const state = String(document.getElementById('logsStateFilter')?.value || '').trim();
  const filter = String(document.getElementById('logsFilter')?.value || '').trim().toLowerCase();
  const data = await apiGet(`/api/v1/jobs?limit=80${state ? `&state=${encodeURIComponent(state)}` : ''}`);
  const box = document.getElementById('logsJobs');
  if (!box) return;
  box.innerHTML = '';

  for (const j of (data || [])) {
    const path = (() => {
      try {
        const p = JSON.parse(j.payload || '{}');
        return p.path || p.input_path || '';
      } catch (_) { return ''; }
    })();
    const hay = `${j.id} ${j.type} ${j.state} ${path}`.toLowerCase();
    if (filter && !hay.includes(filter)) continue;

    const row = el('div', { class: 'listRow' });
    row.style.gridTemplateColumns = '90px 120px 110px 1fr 110px';
    row.appendChild(el('div', { class: 'mono', text: String(j.id || '').slice(0, 8) }));
    row.appendChild(el('div', { class: 'mono muted', text: j.type || '' }));
    row.appendChild(el('div', { class: 'mono muted', text: j.state || '' }));
    row.appendChild(el('div', { class: 'mono', text: path || '-' }));
    const btn = el('button', { class: 'btn', text: 'Ver logs' });
    btn.onclick = async () => {
      setStatus('logsStatus', 'Cargando logs...');
      const limit = _num(document.getElementById('logsLimit')?.value || 400) || 400;
      const resp = await apiGet(`/api/v1/jobs/${j.id}/logs?limit=${limit}`);
      const out = document.getElementById('logsOut');
      const title = document.getElementById('logsTitle');
      if (title) title.textContent = `${j.type} · ${String(j.id).slice(0,8)} · ${path || '-'}`;
      if (out) out.textContent = (resp.lines || []).slice().reverse().join('\n');
      setStatus('logsStatus', '');
    };
    const act = el('div'); act.appendChild(btn);
    row.appendChild(act);
    box.appendChild(row);
  }

  setStatus('logsStatus', `Jobs: ${box.children.length}`);
}

async function resetMediaRequeueMarks() {
  await apiPostJson('/api/v1/watch/media/requeue', {});
  setStatus('logsStatus', 'Marcas de requeue reseteadas');
}

async function refreshHealthScan() {
  setStatus('healthStatus', 'No usado en v2');
}

async function refreshBackupsList() {
  const sel = document.getElementById('setBackupsRestoreName');
  const st = document.getElementById('setBackupsStatus');
  if (!sel) return;
  try {
    const r = await apiGet('/api/v1/backups');
    const items = (r && r.items) ? r.items : [];
    sel.innerHTML = '';
    for (const it of items) {
      const o = document.createElement('option');
      o.value = it.name;
      const cfgTag = it.config_present ? ' +config' : '';
      o.textContent = `${it.name}${cfgTag} (${it.time || ''})`;
      sel.appendChild(o);
    }
    if (st) st.textContent = `Backups: ${items.length}`;
  } catch (e) {
    if (st) st.textContent = `Error backups: ${e}`;
  }
}

// Upload explorer (/inbox/media)
const UP_MEDIA_ROOT = '/inbox/media';
let upMediaPath = UP_MEDIA_ROOT;
let upMediaSelected = '';


async function refreshUpMedia() {
  setStatus('upMediaStatus', 'Cargando...');
  renderCrumbs('upMediaCrumbs', upMediaPath, UP_MEDIA_ROOT, (picked) => {
    if (!picked || !String(picked).startsWith(UP_MEDIA_ROOT)) picked = UP_MEDIA_ROOT;
    upMediaPath = picked;
    refreshUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
  });

  const data = await apiGet(`/api/v1/hostfs/list?path=${encodeURIComponent(upMediaPath)}`);
  const list = document.getElementById('upMediaList');
  if (!list) return;
  list.innerHTML = '';
  const filterText = (document.getElementById('upMediaFilter')?.value || '').trim().toLowerCase();

  for (const e of (data.entries || [])) {
    if (filterText && !String(e.name || '').toLowerCase().includes(filterText)) continue;
    const row = el('div', { class: 'listRow' });
    row.style.gridTemplateColumns = '1fr 110px 190px 90px';
    const icon = e.is_dir ? 'DIR' : 'VID';
    row.appendChild(el('div', { class: 'name' }, [
      el('div', { class: 'icon', text: icon }),
      el('div', { class: e.is_dir ? '' : 'mono', text: e.name })
    ]));
    row.appendChild(el('div', { class: 'mono muted', text: e.is_dir ? '' : fmtSize(e.size) }));
    row.appendChild(el('div', { class: 'mono muted', text: fmtTime(e.mod_time) }));

    const actionCell = el('div');
    if (e.is_dir) {
      const enterBtn = el('button', { class: 'btn', type: 'button', text: 'Entrar' });
      enterBtn.onclick = (ev) => {
        ev.stopPropagation();
        upMediaPath = e.path;
        refreshUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
      };
      actionCell.appendChild(enterBtn);
    }
    row.appendChild(actionCell);

    row.onclick = () => {
      upMediaSelected = e.path;
      const sel = document.getElementById('upMediaSel');
      if (sel) sel.textContent = `Seleccionado: ${e.name}`;
      const btn = document.getElementById('btnUpMediaEnqueue');
      if (btn) btn.disabled = false;
      refreshUploadPreview();
    };
    list.appendChild(row);
  }

  if (!upMediaPath.startsWith(UP_MEDIA_ROOT)) upMediaPath = UP_MEDIA_ROOT;
  setStatus('upMediaStatus', `OK (${(data.entries || []).length})`);
}

function goUpUpMedia() {
  if (upMediaPath === UP_MEDIA_ROOT) return;
  const p = upMediaPath.split('/').filter(Boolean);
  p.pop();
  upMediaPath = '/' + p.join('/');
  if (!upMediaPath.startsWith(UP_MEDIA_ROOT)) upMediaPath = UP_MEDIA_ROOT;
  refreshUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
}

async function enqueueSelectedUpMedia() {
  if (!upMediaSelected) return;
  const btn = document.getElementById('btnUpMediaEnqueue');
  if (btn) btn.disabled = true;
  try {
    await apiPostJson('/api/v1/jobs/enqueue/upload', { path: upMediaSelected });
    setStatus('upMediaStatus', 'Encolado (Queued)');
  } catch (e) {
    setStatus('upMediaStatus', 'Error: ' + String(e));
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function refreshUploadPreview() {
  const out = document.getElementById('upPreview');
  if (!out) return;
  const kind = (document.getElementById('upKind')?.value || 'movie');
  const seriesMode = (document.getElementById('upSeriesMode')?.value || 'episode');
  const src = upMediaSelected || '';
  if (!src) {
    out.textContent = 'Sin selección';
    return;
  }
  try {
    const prev = await apiPostJson('/api/v2/manual/preview', { path: src, kind, series_mode: seriesMode });
    out.textContent = [
      `origen: ${prev.path}`,
      `modo: ${prev.mode}`,
      `tipo: ${prev.kind}`,
      `modo_serie: ${prev.series_mode}`,
      `título: ${prev.resolved_title || '-'}`,
      `año: ${prev.resolved_year || '-'}`,
      `season/episode: ${prev.season || 0}/${prev.episode || 0}`,
      `preview_nombre: ${prev.name_preview}`,
      `salida_nzb_unico: ${prev.combined_nzb_output || prev.nzb_output}`,
      `par_previo: sí, antes de subir`,
      `ruta_par_local: ${prev.par_keep_dir || '-'}`,
      `elementos: ${prev.file_count || 1}`
    ].join('\n');
  } catch (e) {
    out.textContent = 'Error preview: ' + String(e);
  }
}

let uploadPanelsTimer = null;

async function refreshUploadPanels() {
  const data = await apiGet('/api/v1/uploads/summary');
  const items = (data && data.items) ? data.items : [];
  const active = items.filter(x => x.state === 'running' || x.state === 'queued');
  const recent = items.slice(0, 20);

  const activeBox = document.getElementById('upActive');
  const recentBox = document.getElementById('upRecent');
  if (activeBox) activeBox.innerHTML = '';
  if (recentBox) recentBox.innerHTML = '';

  const renderItem = (box, it) => {
    if (!box) return;
    const row = el('div', { class: 'listRow' });
    row.style.display = 'block';
    const pct = Math.max(0, Math.min(100, Number(it.progress || 0)));

    const top = el('div');
    top.style.display = 'flex';
    top.style.alignItems = 'center';
    top.style.justifyContent = 'space-between';
    top.style.gap = '12px';
    top.style.marginBottom = '8px';
    top.appendChild(el('div', { class: 'name' }, [el('div', { class: 'icon', text: 'UP' }), el('div', { class: 'mono', text: it.path || it.id })]));
    top.appendChild(el('div', { class: 'mono muted', text: `${pct}%` }));
    row.appendChild(top);

    const progressWrap = el('div', { class: 'progressWrap' });
    const progressBar = el('div', { class: 'progressBar' });
    progressBar.style.display = 'block';
    progressBar.style.width = '100%';
    progressBar.style.minHeight = '10px';
    const progressFill = el('div', { class: 'progressFill' });
    progressFill.style.display = 'block';
    progressFill.style.width = `${pct}%`;
    progressFill.style.minHeight = '10px';
    progressBar.appendChild(progressFill);
    progressWrap.appendChild(progressBar);
    progressWrap.appendChild(el('div', { class: 'progressMeta' }, [
      el('div', { class: 'mono muted', text: it.phase || it.state || '' }),
      el('div', { class: 'mono muted', text: it.updated_at || '' })
    ]));
    row.appendChild(progressWrap);
    box.appendChild(row);
  };

  active.forEach(it => renderItem(activeBox, it));
  recent.forEach(it => renderItem(recentBox, it));

  setStatus('upActiveStatus', `Activos: ${active.length}`);
  setStatus('upRecentStatus', `Últimos: ${recent.length}`);
}

window.addEventListener('DOMContentLoaded', () => {
  (async () => {
    try {
      const live = await apiGet('/live');
      const v = (live && live.version) ? String(live.version).trim() : '';
      if (v) {
        const pill = document.getElementById('buildVersionPill');
        if (pill) pill.textContent = `v${v}`;
      }
    } catch (_) {}

    // Nav
    for (const item of document.querySelectorAll('.navItem')) {
      item.onclick = () => showPage(item.dataset.page);
    }
    showPage('upload');
    await loadUploadSettings().catch(() => {});
    await refreshUploadPanels().catch(() => {});
    await refreshLogsJobs().catch(() => {});
    if (uploadPanelsTimer) clearInterval(uploadPanelsTimer);
    uploadPanelsTimer = setInterval(() => {
      refreshUploadPanels().catch(() => {});
    }, 5000);

  // v2 upload/par2 path

  // Import page
  if (document.getElementById('btnImpRefresh')) {
    document.getElementById('btnImpRefresh').onclick = () => refreshImport().catch(err => setStatus('impStatus', String(err)));
    document.getElementById('btnImpUp').onclick = () => goUpImport();
    document.getElementById('btnImpEnqueue').onclick = () => enqueueSelectedImport().catch(err => alert(err));
    const impFilter = document.getElementById('impFilter');
    if (impFilter) impFilter.oninput = () => refreshImport().catch(err => setStatus('impStatus', String(err)));

    // Upload NZB
    const up = document.getElementById('impUpload');
    if (up) {
      up.onchange = async () => {
        const f = up.files && up.files[0];
        if (!f) return;
        const fd = new FormData();
        fd.append('file', f, f.name);
        setStatus('impStatus', 'Subiendo a NZB inbox…');
        const r = await fetch('/api/v1/import/nzb/upload', { method: 'POST', body: fd });
        if (!r.ok) throw new Error(await r.text());
        setStatus('impStatus', 'OK (copiado a inbox)');
        up.value = '';
        await refreshImport();
      };
    }

    // init
    document.getElementById('btnImpEnqueue').disabled = true;
    refreshImport().catch(() => {});
  }

  // Upload media on Upload page
  const upMedia = document.getElementById('upUpload');
  if (upMedia) {
    upMedia.onchange = async () => {
      const st = document.getElementById('upActiveStatus');
      const set = (t) => { if (st) st.textContent = t || ''; };
      try {
        const f = upMedia.files && upMedia.files[0];
        if (!f) return;
        const fd = new FormData();
        fd.append('file', f, f.name);
        set('Subiendo media manual… (Uploading manual)');
        const r = await fetch('/api/v1/upload/media/manual', { method: 'POST', body: fd });
        if (!r.ok) throw new Error(await r.text());
        set('OK: encolado. (Queued)');
        upMedia.value = '';
        // Upload page will pick it up via polling.
      } catch (e) {
        set('Error: ' + String(e));
        throw e;
      }
    };
  }

  // Upload explorer controls
  if (document.getElementById('btnUpMediaRefresh')) {
    document.getElementById('btnUpMediaRefresh').onclick = () => refreshUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
    document.getElementById('btnUpMediaUp').onclick = () => goUpUpMedia();
    document.getElementById('btnUpMediaEnqueue').onclick = async () => {
      try {
        const kind = (document.getElementById('upKind')?.value || 'movie');
        const seriesMode = (document.getElementById('upSeriesMode')?.value || 'episode');
        await apiPostJson('/api/v2/manual/upload', { path: upMediaSelected, kind, series_mode: seriesMode });
        setStatus('upMediaStatus', 'Trabajo encolado');
        refreshUploadPanels().catch(() => {});
      } catch (err) {
        setStatus('upMediaStatus', String(err));
      }
    };
    const f = document.getElementById('upMediaFilter');
    if (f) f.oninput = () => refreshUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
    const upKind = document.getElementById('upKind');
    if (upKind) upKind.onchange = () => refreshUploadPreview();
    const upSeriesMode = document.getElementById('upSeriesMode');
    if (upSeriesMode) upSeriesMode.onchange = () => refreshUploadPreview();
    refreshUpMedia().catch(() => {});
  }


  document.getElementById('btnRestartTop').onclick = () => restartNow();

  // Settings
  if (document.getElementById('btnSetSave')) {
    document.getElementById('btnSetSave').onclick = () => saveUploadSettings().catch(() => {});
    document.getElementById('btnSetReload').onclick = () => loadUploadSettings().catch(() => {});
  }
  if (document.getElementById('setBackupsReload')) {
    document.getElementById('setBackupsReload').onclick = () => refreshBackupsList().catch(() => {});
  }
  if (document.getElementById('setBackupsRun')) {
    document.getElementById('setBackupsRun').onclick = async () => {
      const st = document.getElementById('setBackupsStatus');
      try {
        if (st) st.textContent = 'Ejecutando backup…';
        await apiPostJson('/api/v1/backups/run', { include_config: true });
        await refreshBackupsList();
        if (st) st.textContent = 'Backup manual completado';
      } catch (e) {
        if (st) st.textContent = 'Error backup: ' + String(e);
      }
    };
  }
  const bindRestore = (btnId, includeDB, includeConfig, label) => {
    const btn = document.getElementById(btnId);
    if (!btn) return;
    btn.onclick = async () => {
      const sel = document.getElementById('setBackupsRestoreName');
      const st = document.getElementById('setBackupsStatus');
      const name = sel ? String(sel.value || '').trim() : '';
      if (!name) return;
      const ok = confirm(`¿Restaurar backup ${name}?\n\nModo: ${label}. AlfredEDR se reiniciará.`);
      if (!ok) return;
      try {
        if (st) st.textContent = `Restaurando (${label})…`;
        await apiPostJson('/api/v1/backups/restore', {
          name,
          include_db: includeDB,
          include_config: includeConfig,
        });
        if (st) st.textContent = 'Restaurado. Reiniciando…';
      } catch (e) {
        if (st) st.textContent = 'Error restore: ' + String(e);
      }
    };
  };
  bindRestore('setBackupsRestoreAll', true, true, 'DB+config');
  bindRestore('setBackupsRestoreDB', true, false, 'solo DB');
  bindRestore('setBackupsRestoreConfig', false, true, 'solo config');
  if (document.getElementById('btnDBReset')) {
    document.getElementById('btnDBReset').onclick = async () => {
      const ok = confirm('¿Borrar SOLO la base de datos?\n\n- Se perderán imports/overrides/jobs\n- La configuración NO se borra\n- Reinicia el contenedor\n\n¿Continuar?');
      if (!ok) return;
      const st = document.getElementById('setStatus');
      if (st) st.textContent = 'Reseteando BD… (Resetting DB)';
      await apiPostJson('/api/v1/db/reset', {});
      if (st) st.textContent = 'Reiniciando… (Restarting)';
      await apiPostJson('/api/v1/restart', {});
    };
  }
  if (document.getElementById('btnFileBotTestLicense')) {
    document.getElementById('btnFileBotTestLicense').onclick = async () => {
      const st = document.getElementById('filebotTestStatus');
      try {
        if (st) st.textContent = 'Probando licencia...';
        const r = await apiPostJson('/api/v1/filebot/license/test', {});
        if (st) st.textContent = r && r.ok ? 'OK licencia FileBot' : 'Licencia no válida';
      } catch (e) {
        if (st) st.textContent = 'Error test licencia: ' + String(e);
      }
    };
  }

  // Health
  if (document.getElementById('btnHealthScan')) {
    document.getElementById('btnHealthScan').onclick = async () => {
      try {
        await apiPostJson('/api/v1/jobs/enqueue/health-scan', {});
        setStatus('healthStatus', 'Scan encolado (queued)');
        refreshHealthScan().catch(() => {});
      } catch (e) {
        setStatus('healthStatus', 'Error: ' + String(e));
      }
    };
  }
  if (document.getElementById('btnHealthRefresh')) {
    document.getElementById('btnHealthRefresh').onclick = () => refreshHealthScan().catch(() => {});
  }
  if (document.getElementById('btnHealthUp')) {
    document.getElementById('btnHealthUp').onclick = () => goUpHealth();
  }

  // Logs
  if (document.getElementById('btnLogsRefresh')) {
    document.getElementById('btnLogsRefresh').onclick = () => refreshLogsJobs().catch(() => {});
    const logsFilter = document.getElementById('logsFilter');
    if (logsFilter) logsFilter.oninput = () => refreshLogsJobs().catch(() => {});
    const logsStateFilter = document.getElementById('logsStateFilter');
    if (logsStateFilter) logsStateFilter.onchange = () => refreshLogsJobs().catch(() => {});
  }

  // Load imports + review initially
  })().catch(err => {
    console.error(err);
    alert(String(err));
  });
});
