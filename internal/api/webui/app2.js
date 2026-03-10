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

function updateDLStreamsHint() {
  const connEl = document.getElementById('setDL_CONN');
  const preEl = document.getElementById('setDL_PREFETCH');
  const hint = document.getElementById('dlStreamsHint');
  if (!connEl || !preEl || !hint) return;
  const conn = _num(connEl.value || connEl.placeholder || 0);
  const pre = _num(preEl.value || preEl.placeholder || 0);
  const streams = (conn > 0 && pre > 0) ? Math.max(1, Math.floor(conn / pre)) : 0;
  hint.textContent = `Streams simultáneos estimados: ~${streams}`;
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

// --- Manual UI (DB-backed) ---
// NOTE: appended here for now; can be refactored into separate file later.
async function refreshManual() {
  const statusId = 'manStatus';
  const listId = 'manList';
  const crumbsId = 'manCrumbs';

  setStatus(statusId, 'Cargando...');

  // crumbs
  try {
    const path = await apiGet(`/api/v1/manual/path?dir_id=${encodeURIComponent(manualDirId)}`);
    const box = document.getElementById(crumbsId);
    if (box) {
      box.innerHTML = '';
      for (let i = 0; i < path.length; i++) {
        const d = path[i];
        const b = el('button', { class: 'crumb', type: 'button', text: d.name });
        b.onclick = () => { manualDirId = d.id; refreshManual().catch(err => setStatus(statusId, String(err))); };
        box.appendChild(b);
        if (i !== path.length - 1) box.appendChild(el('span', { class: 'crumbSep', text: '›' }));
      }
    }
  } catch (e) {
    // ignore
  }

  const dirs = await apiGet(`/api/v1/manual/dirs?parent_id=${encodeURIComponent(manualDirId)}`);
  const items = await apiGet(`/api/v1/manual/items?dir_id=${encodeURIComponent(manualDirId)}`);

  const list = document.getElementById(listId);
  list.innerHTML = '';

  // folders
  for (const d of (dirs || [])) {
    const row = el('div', { class: 'listRow' });
    row.appendChild(el('div', { class: 'name' }, [
      el('div', { class: 'icon', text: 'DIR' }),
      el('div', { text: d.name })
    ]));
    row.appendChild(el('div', { class: 'mono muted', text: '' }));
    row.appendChild(el('div', { class: 'mono muted', text: '' }));

    const cell = el('div');
    const btn = el('button', { class: 'btn', type: 'button', text: '⋮' });
    btn.style.padding = '6px 10px';
    btn.onclick = async (ev) => {
      ev.stopPropagation();
      const choice = prompt('Acción carpeta:\n1 = Renombrar\n2 = Mover (cambia parent_id)\n3 = Borrar (si vacía)\n\nEscribe 1,2,3');
      if (!choice) return;
      if (String(choice).trim() === '1') {
        const name = prompt('Nuevo nombre', d.name);
        if (!name) return;
        await apiPutJson(`/api/v1/manual/dirs/${encodeURIComponent(d.id)}`, { name });
        await refreshManual();
      } else if (String(choice).trim() === '2') {
        const parent_id = prompt('Nuevo parent_id (dir id). root=raíz', d.parent_id || 'root');
        if (!parent_id) return;
        await apiPutJson(`/api/v1/manual/dirs/${encodeURIComponent(d.id)}`, { parent_id });
        await refreshManual();
      } else if (String(choice).trim() === '3') {
        const ok = confirm('¿Borrar carpeta? Solo si está vacía.');
        if (!ok) return;
        const rr = await fetch(`/api/v1/manual/dirs/${encodeURIComponent(d.id)}`, { method: 'DELETE' });
        if (!rr.ok) throw new Error(await rr.text());
        await refreshManual();
      }
    };
    cell.appendChild(btn);
    row.appendChild(cell);

    row.onclick = () => { manualDirId = d.id; refreshManual().catch(err => setStatus(statusId, String(err))); };
    list.appendChild(row);
  }

  // items
  for (const it of (items || [])) {
    const row = el('div', { class: 'listRow' });
    row.appendChild(el('div', { class: 'name' }, [
      el('div', { class: 'icon', text: 'FILE' }),
      el('div', { class: 'mono', text: it.label || it.filename || '(item)' })
    ]));
    row.appendChild(el('div', { class: 'mono muted', text: fmtSize(it.bytes || 0) }));
    row.appendChild(el('div', { class: 'mono muted', text: '' }));

    const cell = el('div');
    const btn = el('button', { class: 'btn', type: 'button', text: '⋮' });
    btn.style.padding = '6px 10px';
    btn.onclick = async (ev) => {
      ev.stopPropagation();
      const choice = prompt('Acción:\n1 = Renombrar (label)\n2 = Mover (dir_id)\n3 = Quitar de este montaje\n4 = Borrar global (BD)\n5 = Borrado completo (.trash)\n\nEscribe 1..5');
      if (!choice) return;
      const c = String(choice).trim();
      if (c === '1') {
        const label = prompt('Nuevo nombre', it.label || it.filename || '');
        if (!label) return;
        await apiPutJson(`/api/v1/manual/items/${encodeURIComponent(it.id)}`, { label });
        await refreshManual();
      } else if (c === '2') {
        const dir_id = prompt('Nuevo dir_id (carpeta). root=raíz', it.dir_id || 'root');
        if (!dir_id) return;
        await apiPutJson(`/api/v1/manual/items/${encodeURIComponent(it.id)}`, { dir_id });
        await refreshManual();
      } else if (c === '3') {
        const ok = confirm('¿Quitar de este montaje (Manual)?');
        if (!ok) return;
        const rr = await fetch(`/api/v1/manual/items/${encodeURIComponent(it.id)}`, { method: 'DELETE' });
        if (!rr.ok) throw new Error(await rr.text());
        await refreshManual();
      } else if (c === '4') {
        const ok = confirm('¿Borrar global? Desaparece de auto+manual. No borra NZB/PAR2.');
        if (!ok) return;
        await apiPostJson('/api/v1/catalog/imports/delete', { id: it.import_id });
        await refreshManual();
        await refreshList('auto');
      } else if (c === '5') {
        const ok = confirm('⚠ Borrado completo\n\nBD + mover NZB+PAR2 a .trash\n\n¿Continuar?');
        if (!ok) return;
        const typed = prompt('Escribe BORRAR para confirmar');
        if ((typed || '').trim().toUpperCase() !== 'BORRAR') return;
        await apiPostJson('/api/v1/catalog/imports/delete_full', { id: it.import_id });
        await refreshManual();
        await refreshList('auto');
      }
    };
    cell.appendChild(btn);
    row.appendChild(cell);

    list.appendChild(row);
  }

  setStatus(statusId, `OK (${(dirs||[]).length} dirs, ${(items||[]).length} items)`);
}

function goUpManual() {
  if (manualDirId === 'root') return;
  apiGet(`/api/v1/manual/path?dir_id=${encodeURIComponent(manualDirId)}`).then(path => {
    if (!path || path.length < 2) {
      manualDirId = 'root';
    } else {
      manualDirId = path[path.length-2].id || 'root';
    }
    refreshManual().catch(err => setStatus('manStatus', String(err)));
  }).catch(() => {
    manualDirId = 'root';
    refreshManual().catch(err => setStatus('manStatus', String(err)));
  });
}

function fmtSize(n) {
  if (n == null || n === '') return '';
  const x = Number(n);
  if (!isFinite(x)) return String(n);
  const units = ['B','KB','MB','GB','TB'];
  let v = x;
  let i = 0;
  while (v >= 1024 && i < units.length-1) { v /= 1024; i++; }
  const s = (i === 0) ? String(Math.round(v)) : v.toFixed(1);
  return s + ' ' + units[i];
}

// Pages
let __uploadTimer = null;
let __logsLoadedOnce = false;
function showPage(name) {
  for (const id of ['library','upload','import','settings','logs']) {
    const page = document.getElementById('page_' + id);
    if (page) page.classList.toggle('hide', id !== name);
  }
  for (const item of document.querySelectorAll('.navItem')) {
    item.classList.toggle('active', item.dataset.page === name);
  }

  // Only poll upload status while on Upload page.
  if (__uploadTimer) {
    clearInterval(__uploadTimer);
    __uploadTimer = null;
  }
  if (name === 'upload') {
    refreshUploadPanels().catch(() => {});
    __uploadTimer = setInterval(() => refreshUploadPanels().catch(() => {}), 2500);
  }

  if (name === 'settings') {
    // Always reload settings to reflect persisted values after save/restart.
    loadUploadSettings().catch(() => {});
  }

  if (name === 'health') {
    refreshHealthScan().catch(() => {});
  }

  if (name === 'logs') {
    if (!__logsLoadedOnce) {
      __logsLoadedOnce = true;
      refreshLogsJobs().catch(() => {});
    }
  }
}

// Library explorer (FUSE)
// NOTE: the actual mount root inside the container is typically /host/mount/*.
// We discover it from the backend to avoid hardcoding /mount/* which breaks on Unraid.
let AUTO_ROOT = '/mount/library';
let MAN_ROOT = '/mount/library-manual';
let autoPath = AUTO_ROOT;
let manPath = MAN_ROOT;
let manualDirId = 'root';

async function initLibraryRoots() {
  // Prefer server-provided roots (matches cfg.paths.mount_point).
  try {
    const r = await apiGet('/api/v1/library/auto/root');
    if (r && r.root) {
      AUTO_ROOT = r.root;
      // Manual root is the sibling mount under the same mount_point.
      MAN_ROOT = String(r.root).replace(/library-auto\s*$/, 'library-manual');
    }
  } catch (e) {
    // Fallback that works for default container config.
    AUTO_ROOT = '/host/mount/library-auto';
    MAN_ROOT = '/host/mount/library-manual';
  }

  // If we were still on the old hardcoded root, reset to discovered root.
  if (!autoPath || autoPath === '/mount/library' || autoPath.startsWith('/mount/library/') || autoPath === '/mount/library-auto' || autoPath.startsWith('/mount/library-auto/')) {
    autoPath = AUTO_ROOT;
  }
}

function renderCrumbs(boxId, path, root, onPick) {
  const box = document.getElementById(boxId);
  box.innerHTML = '';

  // Never allow navigating above root (otherwise backend returns: path outside library-auto).
  if (root && typeof root === 'string') {
    if (!path || !String(path).startsWith(root)) path = root;
  }

  const rootParts = (root || '').split('/').filter(Boolean);
  const parts = String(path).split('/').filter(Boolean);

  // Always render a root crumb first (label = last segment of root).
  if (rootParts.length) {
    const rootLabel = rootParts[rootParts.length - 1];
    const bRoot = el('button', { class: 'crumb', type: 'button', text: rootLabel });
    bRoot.onclick = () => onPick('/' + rootParts.join('/'));
    box.appendChild(bRoot);
  }

  // Render only crumbs under root.
  const rest = parts.slice(rootParts.length);
  let acc = '/' + rootParts.join('/');
  for (let i = 0; i < rest.length; i++) {
    if (acc) box.appendChild(el('span', { class: 'crumbSep', text: '›' }));
    acc += '/' + rest[i];
    const target = acc;
    const b = el('button', { class: 'crumb', type: 'button', text: rest[i] });
    b.onclick = () => onPick(target);
    box.appendChild(b);
  }
}

function setStatus(id, t) {
  document.getElementById(id).textContent = t || '';
}

async function refreshList(kind) {
  const isAuto = kind === 'auto';
  const path = isAuto ? autoPath : manPath;
  const root = isAuto ? AUTO_ROOT : MAN_ROOT;
  const crumbsId = isAuto ? 'autoCrumbs' : 'manCrumbs';
  const listId = isAuto ? 'autoList' : 'manList';
  const statusId = isAuto ? 'autoStatus' : 'manStatus';

  setStatus(statusId, 'Cargando...');
  renderCrumbs(crumbsId, path, root, (picked) => {
    // Guard: never allow crumbs to pick above root.
    if (!picked || !String(picked).startsWith(root)) picked = root;
    if (isAuto) autoPath = picked; else manPath = picked;
    refreshList(kind).catch(err => setStatus(statusId, String(err)));
  });

  const data = isAuto
    ? await apiGet(`/api/v1/library/auto/list?path=${encodeURIComponent(path)}`)
    : await apiGet(`/api/v1/hostfs/list?path=${encodeURIComponent(path)}`);
  const list = document.getElementById(listId);
  list.innerHTML = '';
  const filterText = (document.getElementById(isAuto ? 'autoFilter' : 'manFilter')?.value || '').trim().toLowerCase();

  for (const e of (data.entries || [])) {
    if (filterText && !String(e.name || '').toLowerCase().includes(filterText)) continue;
    const row = el('div', { class: 'listRow' });
    const icon = e.is_dir ? 'DIR' : 'FILE';
    row.appendChild(el('div', { class: 'name' }, [
      el('div', { class: 'icon', text: icon }),
      el('div', { class: e.is_dir ? '' : 'mono', text: e.name })
    ]));
    row.appendChild(el('div', { class: 'mono muted', text: e.is_dir ? '' : fmtSize(e.size) }));
    row.appendChild(el('div', { class: 'mono muted', text: fmtTime(e.mod_time) }));

    // Action cell (auto list)
    if (isAuto) {
      const cell = el('div');
      if (!e.is_dir && e.import_id) {
        const actions = el('button', { class: 'btn', type: 'button', text: '⋮' });
        actions.style.padding = '6px 10px';
        actions.onclick = async (ev) => {
          ev.stopPropagation();
          const choice = prompt('Acción:\n1 = Borrar global (BD)\n2 = Borrado completo (BD+NZB+PAR2)\n3 = Corregir identificación (FileBot/TMDB)\n\nEscribe 1..3');
          if (!choice) return;
          const c = String(choice).trim();
          if (c === '1') {
            const ok = confirm('¿Borrar global?\n\nDesaparece de biblioteca. No borra NZB/PAR2.');
            if (!ok) return;
            await apiPostJson('/api/v1/catalog/imports/delete', { id: e.import_id });
            await refreshList('auto');
            return;
          }
          if (c === '2') {
            const ok = confirm('⚠ Borrado completo\n\nBD + mover NZB+PAR2 a .trash\n\n¿Continuar?');
            if (!ok) return;
            const typed = prompt('Escribe BORRAR para confirmar');
            if ((typed || '').trim().toUpperCase() !== 'BORRAR') return;
            await apiPostJson('/api/v1/catalog/imports/delete_full', { id: e.import_id });
            await refreshList('auto');
            return;
          }
          if (c === '3') {
            const baseName = String(e.name || '').replace(/\.[^.]+$/, '');
            const title = prompt('Título correcto', baseName || '');
            if (!title) return;
            const yearRaw = prompt('Año (opcional)', '');
            const year = parseInt(String(yearRaw || '').trim(), 10);
            const quality = prompt('Calidad', '1080p');
            if (!quality) return;
            const tmdbRaw = prompt('TMDB ID (opcional)', '');
            const tmdb = parseInt(String(tmdbRaw || '').trim(), 10);

            const applyAll = confirm('¿Aplicar a toda la importación (todos los ficheros de este NZB)?\n\nAceptar = sí, Cancelar = solo este archivo.');
            if (applyAll) {
              await apiPostJson('/api/v1/library/override/import', {
                import_id: e.import_id,
                title: String(title).trim(),
                year: isFinite(year) ? year : 0,
                quality: String(quality).trim(),
                tmdb_id: isFinite(tmdb) ? tmdb : 0,
              });
            } else {
              await apiPostJson('/api/v1/library/override', {
                import_id: e.import_id,
                file_idx: e.file_idx || 0,
                kind: 'movie',
                title: String(title).trim(),
                year: isFinite(year) ? year : 0,
                quality: String(quality).trim(),
                tmdb_id: isFinite(tmdb) ? tmdb : 0,
              });
            }
            setStatus(statusId, 'Identificación corregida. Reprocesado aplicado.');
            await refreshList('auto');
            return;
          }
        };
        cell.appendChild(actions);
      }
      row.appendChild(cell);
    }

    if (e.is_dir) {
      row.onclick = () => {
        if (isAuto) autoPath = e.path; else manPath = e.path;
        refreshList(kind).catch(err => setStatus(statusId, String(err)));
      };
    }

    list.appendChild(row);
  }

  // Guard: if user navigated out of expected root, snap back.
  if (!path.startsWith(root)) {
    if (isAuto) autoPath = root; else manPath = root;
  }

  setStatus(statusId, `OK (${(data.entries || []).length})`);
}

function goUp(kind) {
  if (kind === 'auto') {
    const root = AUTO_ROOT;
    if (autoPath === root) return;
    const p = autoPath.split('/').filter(Boolean);
    p.pop();
    autoPath = '/' + p.join('/');
    if (!autoPath.startsWith(root)) autoPath = root;
    refreshList('auto').catch(err => setStatus('autoStatus', String(err)));
    return;
  }

  // manual (DB-backed)
  goUpManual();
}

function setLibraryTab(which) {
  if (which === "auto") refreshList("auto").catch(err => setStatus("autoStatus", String(err)));
}

async function restartNow() {
  const btn = document.getElementById('btnRestartTop');
  const old = btn.textContent;
  btn.textContent = 'Reiniciando...';
  btn.disabled = true;
  try {
    await apiPostJson('/api/v1/restart', {});
  } catch (e) {
    alert(String(e));
  } finally {
    setTimeout(() => { btn.textContent = old; btn.disabled = false; }, 6000);
  }
}

// HEALTH (NZB Repair)
const HEALTH_ROOT = '';
let healthPath = HEALTH_ROOT;
let healthSelected = null;

async function refreshHealthScan() {
  const box = document.getElementById('healthList');
  const st = document.getElementById('healthStatus');
  const summary = document.getElementById('healthSummary');
  const set = (t) => { if (st) st.textContent = t || ''; };
  if (!box) return;

  const fmtUnix = (u) => {
    const n = Number(u || 0);
    if (!Number.isFinite(n) || n <= 0) return '—';
    return fmtTime(new Date(n * 1000).toISOString());
  };

  set('Cargando… (Loading)');
  box.innerHTML = '';

  try {
    const data = await apiGet('/api/v1/health/scan');
    const entries = (data && data.entries) ? data.entries : [];
    const sum = (data && data.summary) ? data.summary : {};

    const cbox = document.getElementById('healthCrumbs');
    if (cbox) {
      cbox.innerHTML = '';
      const mk = (txt, target) => {
        const b = el('button', { class: 'crumb', type: 'button', text: txt });
        b.onclick = () => { healthPath = target; refreshHealthScan().catch(err => set('Error: ' + String(err))); };
        return b;
      };
      cbox.appendChild(mk('nzb', ''));
      const segs = (healthPath || '').split('/').filter(Boolean);
      let acc = '';
      for (const s of segs) {
        cbox.appendChild(el('span', { class: 'crumbSep', text: '›' }));
        acc = acc ? `${acc}/${s}` : s;
        cbox.appendChild(mk(s, acc));
      }
    }

    const childrenDirs = new Set();
    const files = [];
    for (const e of entries) {
      const rel = String(e.rel_path || '').replaceAll('\\', '/');
      if (!rel) continue;
      if (!healthPath) {
        const p = rel.split('/');
        if (p.length > 1) childrenDirs.add(p[0]); else files.push(e);
      } else {
        const pref = healthPath + '/';
        if (!rel.startsWith(pref)) continue;
        const tail = rel.slice(pref.length);
        if (!tail) continue;
        const p = tail.split('/');
        if (p.length > 1) childrenDirs.add(p[0]); else files.push(e);
      }
    }

    if (summary) {
      summary.innerHTML = '';
      summary.appendChild(el('div', { class: 'muted', text: `Actual: ${sum.checked_in_current_run || 0}/${sum.total || entries.length} · Última completa: ${fmtUnix(sum.last_full_run_at)}` }));
      if (healthSelected) {
        const lbl = `${healthSelected.rel_path} · check: ${fmtUnix(healthSelected.last_checked_at)} · repair: ${fmtUnix(healthSelected.last_repaired_at)} · estado: ${healthSelected.last_repair_outcome || healthSelected.status || 'unknown'}`;
        summary.appendChild(el('div', { class: 'mono', text: lbl }));
      }
    }

    const dirs = Array.from(childrenDirs).sort((a,b)=>a.localeCompare(b));
    for (const d of dirs) {
      const row = el('div', { class: 'listRow' });
      row.appendChild(el('div', { class: 'name' }, [el('div', { class: 'icon', text: 'DIR' }), el('div', { text: d })]));
      row.appendChild(el('div', { class: 'mono muted', text: '' }));
      row.appendChild(el('div', { class: 'mono muted', text: '' }));
      row.onclick = () => {
        healthPath = healthPath ? `${healthPath}/${d}` : d;
        refreshHealthScan().catch(err => set('Error: ' + String(err)));
      };
      box.appendChild(row);
    }

    files.sort((a,b)=>String(a.rel_path||'').localeCompare(String(b.rel_path||'')));
    for (const e of files) {
      const name = String(e.rel_path || '').split('/').pop() || e.path;
      const row = el('div', { class: 'listRow' });
      row.appendChild(el('div', { class: 'name' }, [el('div', { class: 'icon', text: 'NZB' }), el('div', { class: 'mono', text: name })]));
      row.appendChild(el('div', { class: 'mono muted', text: fmtSize(e.size) }));
      row.appendChild(el('div', { class: 'mono muted', text: fmtTime(e.mod_time) }));
      row.onclick = () => {
        healthSelected = e;
        refreshHealthScan().catch(err => set('Error: ' + String(err)));
      };
      box.appendChild(row);
    }

    if (healthSelected) {
      const act = el('div', { class: 'row', style: 'padding:8px 12px; gap:8px;' });
      const btn = el('button', { class: 'btn', type: 'button', text: 'Reparar seleccionado' });
      btn.onclick = async () => {
        set('Encolando reparación…');
        try {
          await apiPostJson('/api/v1/jobs/enqueue/health-repair', { path: healthSelected.path });
          set('OK: job encolado');
        } catch (err) {
          set('Error: ' + String(err));
        }
      };
      act.appendChild(btn);
      box.appendChild(act);
    }

    set(`OK (${dirs.length + files.length})`);
  } catch (e) {
    set('Error: ' + String(e));
  }
}

function goUpHealth() {
  if (!healthPath) return;
  const p = healthPath.split('/').filter(Boolean);
  p.pop();
  healthPath = p.join('/');
  refreshHealthScan().catch(err => setStatus('healthStatus', String(err)));
}

// LOGS (Jobs)
function _safe(s) {
  return (s == null) ? '' : String(s);
}

async function refreshLogsJobs() {
  const box = document.getElementById('logsJobs');
  const st = document.getElementById('logsStatus');
  const set = (t) => { if (st) st.textContent = t || ''; };
  if (!box) return;

  set('Cargando… (Loading)');
  box.innerHTML = '';
  try {
    const filterText = (document.getElementById('logsFilter')?.value || '').trim().toLowerCase();
    const stateFilter = (document.getElementById('logsStateFilter')?.value || '').trim().toLowerCase();
    const limit = stateFilter ? 500 : 200;
    const qs = new URLSearchParams({ limit: String(limit) });
    if (stateFilter) qs.set('state', stateFilter);
    const jobs = await apiGet('/api/v1/jobs?' + qs.toString());

    let shown = 0;
    for (const j of jobs) {
      const pathVal = _safe(((j.params || {}).path || (j.payload || {}).path || ''));
      const pathTxt = pathVal.toLowerCase();
      const typeTxt = _safe(j.type).toLowerCase();
      const stateTxt = _safe(j.state).toLowerCase();
      if (filterText && !pathTxt.includes(filterText) && !typeTxt.includes(filterText) && !_safe(j.id).toLowerCase().includes(filterText) && !stateTxt.includes(filterText)) continue;
      const row = el('div', { class: 'listRow' });
      row.style.gridTemplateColumns = '90px 120px 110px 1fr 110px';

      row.appendChild(el('div', { class: 'mono', text: _safe(j.id).slice(0, 8) }));
      row.appendChild(el('div', { text: _safe(j.type) }));
      row.appendChild(el('div', { text: _safe(j.state) }));
      row.appendChild(el('div', { class: 'mono muted', text: pathVal }));

      const btn = el('button', { class: 'btn', type: 'button', text: 'Ver (View)' });
      btn.onclick = async (ev) => {
        ev.stopPropagation();
        await loadJobLogs(j.id);
      };
      const cell = el('div');
      cell.appendChild(btn);
      row.appendChild(cell);

      row.onclick = () => loadJobLogs(j.id).catch(() => {});

      box.appendChild(row);
      shown++;
    }
    set(`OK (${shown}/${(jobs || []).length})`);
  } catch (e) {
    set('Error: ' + String(e));
  }
}

async function resetMediaRequeueMarks() {
  const ok = confirm('Esto reinicia el estado de encolado de media para que se vuelva a escanear.\n\nNO borra archivos MKV ni contenido de /media.\n\n¿Continuar?');
  if (!ok) return;
  const typed = prompt('Escribe REQUEUE para confirmar');
  if ((typed || '').trim().toUpperCase() !== 'REQUEUE') return;

  const st = document.getElementById('logsStatus');
  if (st) st.textContent = 'Reiniciando marcas de encolado…';
  const r = await apiPostJson('/api/v1/watch/media/requeue', {});
  if (st) st.textContent = `OK requeue marks reset (${r.deleted || 0}) · NO media deleted`;
  await refreshLogsJobs();
}

async function loadJobLogs(jobId) {
  const out = document.getElementById('logsOut');
  const title = document.getElementById('logsTitle');
  const limit = document.getElementById('logsLimit');
  if (!out) return;

  const n = limit ? parseInt(limit.value || '400', 10) : 400;
  const lim = Number.isFinite(n) ? n : 400;
  out.textContent = 'Cargando logs…';
  if (title) title.textContent = `Job ${String(jobId).slice(0, 8)} (limit=${lim})`;

  const resp = await apiGet(`/api/v1/jobs/${jobId}/logs?limit=${encodeURIComponent(lim)}`);
  const lines = (resp && resp.lines) ? resp.lines : [];
  // API returns newest-first; show oldest-first.
  out.textContent = lines.slice().reverse().join('\n');
}

// SETTINGS (Ajustes) - Upload
function _val(id) {
  const n = document.getElementById(id);
  if (!n) return '';
  return String(n.value || '').trim();
}
function _int(id, defV) {
  const s = _val(id);
  const n = parseInt(s, 10);
  return Number.isFinite(n) ? n : defV;
}
function _bool(id) {
  const n = document.getElementById(id);
  return !!(n && n.checked);
}
function _valKeep(id, current) {
  const v = _val(id);
  return v === '' ? (current || '') : v;
}

async function loadUploadSettings() {
  const st = document.getElementById('setStatus');
  const set = (t) => { if (st) st.textContent = t || ''; };
  set('Cargando… (Loading)');
  const cfg = await apiGet('/api/v1/config');

  // Watch (Media uploads)
  document.getElementById('setWatchMediaEnabled').checked = (cfg.watch && cfg.watch.media && cfg.watch.media.enabled != null) ? !!cfg.watch.media.enabled : true;
  document.getElementById('setWatchMediaDir').value = (cfg.watch && cfg.watch.media && cfg.watch.media.dir) ? cfg.watch.media.dir : '';
  document.getElementById('setWatchMediaRecursive').checked = !!(cfg.watch && cfg.watch.media && cfg.watch.media.recursive);

  // Watch (NZB import)
  document.getElementById('setWatchNZBEnabled').checked = (cfg.watch && cfg.watch.nzb && cfg.watch.nzb.enabled != null) ? !!cfg.watch.nzb.enabled : true;
  document.getElementById('setWatchNZBDir').value = (cfg.watch && cfg.watch.nzb && cfg.watch.nzb.dir) ? cfg.watch.nzb.dir : '';
  document.getElementById('setWatchNZBRecursive').checked = !!(cfg.watch && cfg.watch.nzb && cfg.watch.nzb.recursive);

  // Provider fixed to ngpost in this UI version.

  // Upload NNTP (NgPost config reused for Nyuu)
  const n = (cfg.ngpost || {});
  document.getElementById('setNntpHost').value = n.host || '';
  document.getElementById('setNntpPort').value = (n.port != null) ? n.port : 563;
  document.getElementById('setNntpSSL').checked = (n.ssl !== false);
  document.getElementById('setNntpUser').value = n.user || '';
  document.getElementById('setNntpPass').value = n.pass || '';
  document.getElementById('setNntpConnections').value = (n.connections != null) ? n.connections : 20;
  document.getElementById('setNntpThreads').value = (n.threads != null) ? n.threads : 2;
  document.getElementById('setNntpGroups').value = n.groups || '';

  // Library-auto templates (display + preview)
  const L = (cfg.library || {});
  // library-auto is treated as always enabled.
  const libEn = document.getElementById('setLibEnabled');
  if (libEn) {
    libEn.checked = true;
    libEn.disabled = true;
  }
  document.getElementById('setLibUpper').checked = !!L.uppercase_folders;

  const setText = (id, t) => {
    const el = document.getElementById(id);
    if (el) el.textContent = String(t || '');
  };
  const setVal = (id, t) => {
    const el = document.getElementById(id);
    if (el) el.value = String(t || '').trim();
  };

  setText('tplMovieDir', L.movie_dir_template || '');
  setText('tplMovieFile', L.movie_file_template || '');
  setText('tplSeriesDir', L.series_dir_template || '');
  setText('tplSeasonDir', L.season_folder_template || '');
  setText('tplSeriesFile', L.series_file_template || '');

  setVal('setLibMovieDirT', L.movie_dir_template || '');
  setVal('setLibMovieFileT', L.movie_file_template || '');
  setVal('setLibSeriesDirT', L.series_dir_template || '');
  setVal('setLibSeasonT', L.season_folder_template || '');
  setVal('setLibSeriesFileT', L.series_file_template || '');

  // Copy buttons
  const bindCopy = (btnId, srcId) => {
    const b = document.getElementById(btnId);
    if (!b) return;
    b.onclick = async () => {
      const src = document.getElementById(srcId);
      const txt = src ? (src.textContent || '') : '';
      try { await navigator.clipboard.writeText(txt); } catch (_) {}
    };
  };
  bindCopy('btnCopyMovieDir', 'tplMovieDir');
  bindCopy('btnCopyMovieFile', 'tplMovieFile');
  bindCopy('btnCopySeriesDir', 'tplSeriesDir');
  bindCopy('btnCopySeasonDir', 'tplSeasonDir');
  bindCopy('btnCopySeriesFile', 'tplSeriesFile');

  // Preview (realistic examples)
  try {
    const prev = await apiGet('/api/v1/library/templates/preview');
    setText('tplPrevMovie', (prev && prev.movie && prev.movie.example_file) ? prev.movie.example_file : '');
    setText('tplPrevSeries', (prev && prev.series && prev.series.example_file) ? prev.series.example_file : '');
  } catch (e) {
    // ignore preview failure
  }

  // Plex (solo library-auto)
  const p = (cfg.plex || {});
  document.getElementById('setPlexAutoRefresh').checked = !!(p.enabled || p.refresh_on_import);
  document.getElementById('setPlexBaseURL').value = p.base_url || '';
  document.getElementById('setPlexToken').value = p.token || '';
  document.getElementById('setPlexRoot').value = p.plex_root || '';

  // Health
  const h = (cfg.health || {});
  document.getElementById('setHealthEnabled').checked = !!h.enabled;
  const hs = (h.scan || {});
  const hl = (h.lock || {});
  document.getElementById('setHealthScanEnabled').checked = !!hs.enabled;
  document.getElementById('setHealthAutoRepair').checked = (hs.auto_repair !== false);
  document.getElementById('setHealthMaxDurationMins').value = (hs.max_duration_minutes != null) ? hs.max_duration_minutes : 180;
  document.getElementById('setHealthChunkEveryHours').value = (hs.chunk_every_hours != null) ? hs.chunk_every_hours : 24;
  document.getElementById('setHealthIntervalHours').value = (hs.interval_hours != null) ? hs.interval_hours : 24;
  document.getElementById('setHealthLockTTLHours').value = (hl.lock_ttl_hours != null) ? hl.lock_ttl_hours : 6;

  // Backups
  const b = (cfg.backups || {});
  document.getElementById('setBackupsEnabled').checked = !!b.enabled;
  document.getElementById('setBackupsCompress').checked = (b.compress_gz !== false);
  document.getElementById('setBackupsDir').value = b.dir || '/backups';
  document.getElementById('setBackupsEvery').value = (b.every_mins != null) ? b.every_mins : 0;
  document.getElementById('setBackupsKeep').value = (b.keep != null) ? b.keep : 30;
  await refreshBackupsList();

  // Download NNTP
  const d = (cfg.download || {});
  document.getElementById('setDL_ENABLED').checked = !!d.enabled;
  document.getElementById('setDL_HOST').value = d.host || '';
  document.getElementById('setDL_PORT').value = (d.port != null) ? d.port : 563;
  document.getElementById('setDL_SSL').checked = (d.ssl !== false);
  document.getElementById('setDL_USER').value = d.user || '';
  document.getElementById('setDL_PASS').value = d.pass || '';
  document.getElementById('setDL_CONN').value = (d.connections != null) ? d.connections : 20;
  document.getElementById('setDL_PREFETCH').value = (d.prefetch_segments != null) ? d.prefetch_segments : 8;
  updateDLStreamsHint();
  setTimeout(updateDLStreamsHint, 0);

  // TMDB
  const t = ((cfg.metadata || {}).tmdb || {});
  document.getElementById('setTMDBEnabled').checked = !!t.enabled;
  document.getElementById('setTMDBApiKey').value = t.api_key || '';
  document.getElementById('setTMDBLanguage').value = t.language || 'es-ES';

  // Rename / FileBot (mandatory)
  const rn = (cfg.rename || {});
  const fb = (rn.filebot || {});
  document.getElementById('setFileBotDB').value = fb.db || 'TheMovieDB';
  document.getElementById('setFileBotLanguage').value = fb.language || 'es';

  set('');
}

async function saveUploadSettings() {
  const st = document.getElementById('setStatus');
  const set = (t) => { if (st) st.textContent = t || ''; };
  const btn = document.getElementById('btnSetSave');
  const old = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = 'Guardando…'; }

  try {
    set('Validando…');
    const cfg = await apiGet('/api/v1/config');

    // Watch media
    cfg.watch = cfg.watch || {};
    cfg.watch.media = cfg.watch.media || {};
    cfg.watch.media.enabled = _bool('setWatchMediaEnabled');
    cfg.watch.media.dir = _val('setWatchMediaDir');
    cfg.watch.media.recursive = _bool('setWatchMediaRecursive');

    // Watch NZB
    cfg.watch.nzb = cfg.watch.nzb || {};
    cfg.watch.nzb.enabled = _bool('setWatchNZBEnabled');
    cfg.watch.nzb.dir = _val('setWatchNZBDir');
    cfg.watch.nzb.recursive = _bool('setWatchNZBRecursive');

    // Provider fixed to nyuu
    cfg.upload = cfg.upload || {};
    cfg.upload.provider = 'nyuu';

    // NNTP upload settings (ngpost section)
    cfg.ngpost = cfg.ngpost || {};
    cfg.ngpost.enabled = true;
    cfg.ngpost.host = _val('setNntpHost');
    cfg.ngpost.port = _int('setNntpPort', 563);
    cfg.ngpost.ssl = _bool('setNntpSSL');
    cfg.ngpost.user = _val('setNntpUser');
    cfg.ngpost.pass = _val('setNntpPass');
    cfg.ngpost.connections = _int('setNntpConnections', 20);
    cfg.ngpost.threads = _int('setNntpThreads', 2);
    cfg.ngpost.groups = _val('setNntpGroups');

    // Library-auto
    cfg.library = cfg.library || {};
    // library-auto always enabled (ignore UI checkbox)
    cfg.library.enabled = true;
    cfg.library.uppercase_folders = _bool('setLibUpper');
    cfg.library.movie_dir_template = _valKeep('setLibMovieDirT', cfg.library.movie_dir_template);
    cfg.library.movie_file_template = _valKeep('setLibMovieFileT', cfg.library.movie_file_template);
    cfg.library.series_dir_template = _valKeep('setLibSeriesDirT', cfg.library.series_dir_template);
    cfg.library.season_folder_template = _valKeep('setLibSeasonT', cfg.library.season_folder_template);
    cfg.library.series_file_template = _valKeep('setLibSeriesFileT', cfg.library.series_file_template);

    // Plex (solo library-auto)
    cfg.plex = cfg.plex || {};
    cfg.plex.enabled = _bool('setPlexAutoRefresh');
    cfg.plex.refresh_on_import = _bool('setPlexAutoRefresh');
    cfg.plex.base_url = _val('setPlexBaseURL');
    cfg.plex.token = _val('setPlexToken');
    cfg.plex.plex_root = _val('setPlexRoot');

    // Health
    cfg.health = cfg.health || {};
    cfg.health.enabled = _bool('setHealthEnabled');
    cfg.health.scan = cfg.health.scan || {};
    cfg.health.scan.enabled = _bool('setHealthScanEnabled');
    cfg.health.scan.auto_repair = _bool('setHealthAutoRepair');
    cfg.health.scan.max_duration_minutes = _int('setHealthMaxDurationMins', 180);
    cfg.health.scan.chunk_every_hours = _int('setHealthChunkEveryHours', 24);
    cfg.health.scan.interval_hours = _int('setHealthIntervalHours', 24);
    cfg.health.lock = cfg.health.lock || {};
    cfg.health.lock.lock_ttl_hours = _int('setHealthLockTTLHours', 6);

    // Backups
    cfg.backups = cfg.backups || {};
    cfg.backups.enabled = _bool('setBackupsEnabled');
    cfg.backups.compress_gz = _bool('setBackupsCompress');
    cfg.backups.dir = _val('setBackupsDir') || '/backups';
    cfg.backups.every_mins = _int('setBackupsEvery', 0);
    cfg.backups.keep = _int('setBackupsKeep', 30);

    // Download NNTP
    cfg.download = cfg.download || {};
    cfg.download.enabled = _bool('setDL_ENABLED');
    cfg.download.host = _val('setDL_HOST');
    cfg.download.port = _int('setDL_PORT', 563);
    cfg.download.ssl = _bool('setDL_SSL');
    cfg.download.user = _val('setDL_USER');
    cfg.download.pass = _val('setDL_PASS');
    cfg.download.connections = _int('setDL_CONN', 20);
    cfg.download.prefetch_segments = _int('setDL_PREFETCH', 8);

    // TMDB
    cfg.metadata = cfg.metadata || {};
    cfg.metadata.tmdb = cfg.metadata.tmdb || {};
    cfg.metadata.tmdb.enabled = _bool('setTMDBEnabled');
    cfg.metadata.tmdb.api_key = _val('setTMDBApiKey');
    cfg.metadata.tmdb.language = _val('setTMDBLanguage') || 'es-ES';

    // Rename / FileBot (mandatory, used in phase 1 and phase 2)
    cfg.rename = cfg.rename || {};
    cfg.rename.provider = 'filebot';
    cfg.rename.filebot = cfg.rename.filebot || {};
    cfg.rename.filebot.enabled = true;
    cfg.rename.filebot.binary = '/usr/local/bin/filebot';
    cfg.rename.filebot.license_path = '/config/filebot/license.psm';
    cfg.rename.filebot.db = _val('setFileBotDB') || 'TheMovieDB';
    cfg.rename.filebot.language = _val('setFileBotLanguage') || 'es';
    cfg.rename.filebot.action = 'test';

    set('Guardando… (Saving)');
    await apiPutJson('/api/v1/config', cfg);

    set('Aplicado. Reiniciando… (Restarting)');
    await apiPostJson('/api/v1/restart', {});
    set('Reiniciando…');
  } catch (e) {
    set('Error: ' + String(e));
    throw e;
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = old || 'Guardar y reiniciar'; }
  }
}

let reviewSelected = null;

function showReviewBox(show) {
  const box = document.getElementById('reviewBox');
  if (!box) return;
  box.style.display = show ? '' : 'none';
}

function qualityFromGuess(q) {
  const low = String(q || '').toLowerCase();
  if (low.includes('2160') || low.includes('4k')) return '2160';
  return '1080';
}

async function refreshImports() {
  const list = document.getElementById('importsList');
  if (!list) return;
  list.innerHTML = '';
  const items = await apiGet('/api/v1/catalog/imports');
  if (!items || items.length === 0) {
    list.innerHTML = '<div class="muted" style="padding:10px">No hay imports.</div>';
    return;
  }
  for (const it of items) {
    const row = el('div', { class: 'listRow' });
    const name = (it.path || '').split('/').pop() || it.id.slice(0, 8);
    row.appendChild(el('div', { class: 'mono', text: name }));
    row.appendChild(el('div', { class: 'mono muted', text: fmtSize(it.total_bytes || 0) }));
    row.appendChild(el('div', { class: 'mono muted', text: fmtTime(it.imported_at) }));

    const cell = el('div');
    const btnDel = el('button', { class: 'btn danger', text: 'Borrar global' });
    btnDel.onclick = async (ev) => {
      ev.stopPropagation();
      const ok = confirm('¿Eliminar este import de la biblioteca (global)?\n\n- Desaparece de library\n- NO borra el NZB ni PAR2 del disco\n\n¿Continuar?');
      if (!ok) return;
      await apiPostJson('/api/v1/catalog/imports/delete', { id: it.id });
      await refreshImports();
      await refreshList('auto');
    };

    const btnFull = el('button', { class: 'btn danger', text: 'Borrado completo' });
    btnFull.onclick = async (ev) => {
      ev.stopPropagation();
      const ok = confirm('⚠ Borrado completo (irreversible)\n\n- Borra de la BD\n- Mueve NZB a /host/inbox/.trash\n- Mueve PAR2 a /host/inbox/.trash\n\n¿Continuar?');
      if (!ok) return;
      const typed = prompt('Escribe BORRAR para confirmar');
      if ((typed || '').trim().toUpperCase() !== 'BORRAR') return;
      await apiPostJson('/api/v1/catalog/imports/delete_full', { id: it.id });
      await refreshImports();
      await refreshList('auto');
    };

    cell.appendChild(btnDel);
    cell.appendChild(btnFull);
    row.appendChild(cell);

    list.appendChild(row);
  }
}

async function refreshReview() {
  try {
    const r = await apiGet('/api/v1/library/review');
    const items = (r.items || []);
    const list = document.getElementById('reviewList');
    list.innerHTML = '';

    // Keep the review box visible so users know where to look,
    // even when there are no pending items.
    showReviewBox(true);
    if (items.length === 0) {
      list.innerHTML = '<div class="muted" style="padding:10px">No hay elementos pendientes de revisión.</div>';
      return;
    }

    for (const it of items) {
      const row = el('div', { class: 'listRow' });
      row.appendChild(el('div', { class: 'name' }, [
        el('div', { class: 'icon', text: '⚠' }),
        el('div', { class: 'mono', text: it.filename || '(file)' })
      ]));
      row.appendChild(el('div', { class: 'mono muted', text: `${it.guess_title || ''} ${it.guess_year || ''} ${it.guess_quality || ''}`.trim() }));

      const btnFix = el('button', { class: 'btn', text: 'Corregir' });
      btnFix.onclick = () => {
        reviewSelected = it;
        document.getElementById('fixSel').textContent = `import=${it.import_id.slice(0,8)} idx=${it.file_idx}`;
        document.getElementById('fixTitle').value = (it.guess_title || '').replace(/\s+\d{4}\b/, '').trim() || '';
        document.getElementById('fixYear').value = (it.guess_year && it.guess_year > 0) ? it.guess_year : '';
        const q = qualityFromGuess(it.guess_quality);
        document.getElementById('fixQuality').value = q;
        document.getElementById('fixApplyAll').checked = false;
        document.getElementById('fixQualityCustomWrap').style.display = 'none';
        document.getElementById('fixQualityCustom').value = '';
        document.getElementById('fixStatus').textContent = '';
      };

      const btnDismiss = el('button', { class: 'btn danger', text: 'Ignorar' });
      btnDismiss.onclick = async () => {
        if (!confirm('¿Ignorar este aviso? (Dismiss)')) return;
        try {
          await apiPostJson('/api/v1/library/review/dismiss', { import_id: it.import_id, file_idx: it.file_idx });
          await refreshReview();
        } catch (e) {
          alert(String(e));
        }
      };

      const td = el('div', {}, []);
      td.appendChild(btnFix);
      td.appendChild(btnDismiss);
      row.appendChild(td);
      list.appendChild(row);
    }
  } catch (e) {
    // hide box on errors, but don’t break UI
    showReviewBox(false);
  }
}

async function applyFix() {
  const st = document.getElementById('fixStatus');
  if (!reviewSelected) {
    st.textContent = 'Selecciona un archivo primero.';
    return;
  }
  const title = (document.getElementById('fixTitle').value || '').trim();
  const year = Number(document.getElementById('fixYear').value || 0);
  const qSel = document.getElementById('fixQuality').value;
  let quality = qSel;
  if (qSel === 'custom') {
    quality = (document.getElementById('fixQualityCustom').value || '').trim();
  }
  if (!title || !quality) {
    st.textContent = 'Title y Quality son obligatorios.';
    return;
  }
  st.textContent = 'Aplicando...';
  try {
    const applyAll = !!document.getElementById('fixApplyAll').checked;
    if (applyAll) {
      await apiPostJson('/api/v1/library/override/import', {
        import_id: reviewSelected.import_id,
        title,
        year: isFinite(year) ? year : 0,
        quality,
        tmdb_id: 0,
      });
      st.textContent = 'OK. Aplicado a todo el import.';
    } else {
      await apiPostJson('/api/v1/library/override', {
        import_id: reviewSelected.import_id,
        file_idx: reviewSelected.file_idx,
        kind: 'movie',
        title,
        year: isFinite(year) ? year : 0,
        quality,
        tmdb_id: 0,
      });
      st.textContent = 'OK. Ya debería aparecer en library-auto.';
    }
    // refresh review + auto list
    reviewSelected = null;
    document.getElementById('fixSel').textContent = '(selecciona un archivo arriba)';
    await refreshReview();
    await refreshList('auto');
  } catch (e) {
    st.textContent = 'Error: ' + String(e);
  }
}

// Import explorer (NZB inbox)
const IMP_ROOT = '/inbox/nzb';
let impPath = IMP_ROOT;
let impSelected = '';

async function refreshImport() {
  const statusId = 'impStatus';
  setStatus(statusId, 'Cargando...');
  renderCrumbs('impCrumbs', impPath, IMP_ROOT, (picked) => {
    if (!picked || !String(picked).startsWith(IMP_ROOT)) picked = IMP_ROOT;
    impPath = picked;
    refreshImport().catch(err => setStatus(statusId, String(err)));
  });

  const data = await apiGet(`/api/v1/hostfs/list?path=${encodeURIComponent(impPath)}`);
  const list = document.getElementById('impList');
  list.innerHTML = '';
  const filterText = (document.getElementById('impFilter')?.value || '').trim().toLowerCase();

  for (const e of (data.entries || [])) {
    if (filterText && !String(e.name || '').toLowerCase().includes(filterText)) continue;
    const row = el('div', { class: 'listRow' });
    const icon = e.is_dir ? 'DIR' : 'NZB';
    row.appendChild(el('div', { class: 'name' }, [
      el('div', { class: 'icon', text: icon }),
      el('div', { class: e.is_dir ? '' : 'mono', text: e.name })
    ]));
    row.appendChild(el('div', { class: 'mono muted', text: e.is_dir ? '' : fmtSize(e.size) }));
    row.appendChild(el('div', { class: 'mono muted', text: fmtTime(e.mod_time) }));

    if (e.is_dir) {
      row.onclick = () => {
        impPath = e.path;
        refreshImport().catch(err => setStatus(statusId, String(err)));
      };
    } else {
      row.onclick = () => {
        impSelected = e.path;
        document.getElementById('impSel').textContent = `Seleccionado (Selected): ${e.name}`;
        const btn = document.getElementById('btnImpEnqueue');
        btn.disabled = !e.name.toLowerCase().endsWith('.nzb');
      };
    }

    list.appendChild(row);
  }

  if (!impPath.startsWith(IMP_ROOT)) impPath = IMP_ROOT;
  setStatus(statusId, `OK (${(data.entries || []).length})`);
}

function goUpImport() {
  if (impPath === IMP_ROOT) return;
  const p = impPath.split('/').filter(Boolean);
  p.pop();
  impPath = '/' + p.join('/');
  if (!impPath.startsWith(IMP_ROOT)) impPath = IMP_ROOT;
  refreshImport().catch(err => setStatus('impStatus', String(err)));
}

async function enqueueSelectedImport() {
  if (!impSelected) return;
  const btn = document.getElementById('btnImpEnqueue');
  btn.disabled = true;
  try {
    await apiPostJson('/api/v1/jobs/enqueue/import', { path: impSelected });
    setStatus('impStatus', 'Encolado (Queued)');
  } catch (e) {
    alert(String(e));
  } finally {
    btn.disabled = false;
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
    row.appendChild(el('div', { class: 'name' }, [el('div', { class: 'icon', text: 'UP' }), el('div', { class: 'mono', text: it.path || it.id })]));
    row.appendChild(el('div', { class: 'mono muted', text: String(it.progress || 0) + '%' }));
    row.appendChild(el('div', { class: 'mono muted', text: `${it.state}${it.phase ? ' · ' + it.phase : ''}` }));
    box.appendChild(row);
  };

  active.forEach(it => renderItem(activeBox, it));
  recent.forEach(it => renderItem(recentBox, it));

  setStatus('upActiveStatus', `Activos: ${active.length}`);
  setStatus('upRecentStatus', `Últimos: ${recent.length}`);
}

window.addEventListener('DOMContentLoaded', () => {
  (async () => {
    await initLibraryRoots();
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
    showPage('library');

  // Tabs
  refreshList('auto').catch(err => setStatus('autoStatus', String(err)));

  // Download tuning hint
  const dlConn = document.getElementById('setDL_CONN');
  const dlPref = document.getElementById('setDL_PREFETCH');
  if (dlConn) dlConn.addEventListener('input', updateDLStreamsHint);
  if (dlPref) dlPref.addEventListener('input', updateDLStreamsHint);

  // Imports UI
  if (document.getElementById('btnImportsRefresh')) {
    document.getElementById('btnImportsRefresh').onclick = () => refreshImports().catch(() => {});
  }

  // Review UI
  document.getElementById('btnReviewRefresh').onclick = () => refreshReview().catch(() => {});
  document.getElementById('btnFixApply').onclick = () => applyFix().catch(() => {});
  document.getElementById('fixQuality').onchange = () => {
    const v = document.getElementById('fixQuality').value;
    document.getElementById('fixQualityCustomWrap').style.display = (v === 'custom') ? '' : 'none';
  };

  // Controls
  document.getElementById('btnAutoRefresh').onclick = () => refreshList('auto').catch(err => setStatus('autoStatus', String(err)));
  document.getElementById('btnAutoUp').onclick = () => goUp('auto');
  const autoFilter = document.getElementById('autoFilter');
  if (autoFilter) autoFilter.oninput = () => refreshList('auto').catch(err => setStatus('autoStatus', String(err)));

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
    document.getElementById('btnUpMediaEnqueue').onclick = () => enqueueSelectedUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
    const f = document.getElementById('upMediaFilter');
    if (f) f.oninput = () => refreshUpMedia().catch(err => setStatus('upMediaStatus', String(err)));
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
    const btnRequeue = document.getElementById('btnLogsRequeueMedia');
    if (btnRequeue) btnRequeue.onclick = () => resetMediaRequeueMarks().catch(err => alert(String(err)));
  }

  // Load imports + review initially
  refreshImports().catch(() => {});
  })().catch(err => {
    console.error(err);
    alert(String(err));
  });
});
