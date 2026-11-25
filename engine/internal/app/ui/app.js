'use strict';
// Interface de Synkro : parle à l'API locale du moteur (synkrod).

// ---------- Jeton d'accès ----------
(function initToken() {
  const m = location.hash.match(/token=([^&]+)/);
  if (m) {
    sessionStorage.setItem('synkro-token', m[1]);
    history.replaceState(null, '', location.pathname);
  }
})();
const TOKEN = sessionStorage.getItem('synkro-token') || '';
const native = window.synkro || null; // pont Electron (preload.js), absent dans un navigateur

async function api(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: { 'X-Synkro-Token': TOKEN, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    throw new Error("Le moteur Synkro ne répond pas : il s'est arrêté ou sa fenêtre a été fermée. Relancez Synkro. (" + e.message + ')');
  }
  const type = res.headers.get('Content-Type') || '';
  const data = type.includes('json') ? await res.json() : await res.text();
  if (!res.ok) {
    if (path !== '/api/clientlog') clientLog(`API ${method} ${path} → ${res.status} : ${(data && data.error) || res.statusText}`);
    throw new Error((data && data.error) || res.statusText);
  }
  return data;
}

// Les erreurs de l'interface sont envoyées au journal du moteur (analyse à distance).
function clientLog(msg) {
  try { fetch('/api/clientlog', { method: 'POST', headers: { 'X-Synkro-Token': TOKEN }, body: String(msg).slice(0, 3000) }); } catch {}
}
// Blocage de l'interface (plus de 2 s sans pouvoir répondre) : noté dans le
// journal du moteur, avec ce qu'affichait la fenêtre, pour comprendre a posteriori.
try {
  new PerformanceObserver(list => {
    for (const e of list.getEntries()) {
      if (e.duration < 2000) continue;
      const j = typeof jobs !== 'undefined' && jobs.find(x => x.id === selected);
      clientLog(`interface bloquée ${Math.round(e.duration)} ms · tâche « ${j ? j.name : '?'} » (${j ? j.status.state : '?'}) · ` +
        `${(details.plan || []).length} actions, ${sameRows ? sameRows.length : 0} inchangés affichés, vue ${view}, suivi ${$('#follow').checked}, ` +
        `${rowQueue.fns.length} lignes, dernier rendu complet ${Math.round(lastRenderMs)} ms`);
    }
  }).observe({ type: 'longtask', buffered: true });
} catch {}
let lastRenderMs = 0;
addEventListener('error', e => clientLog(`erreur JS : ${e.message} (${e.filename}:${e.lineno}:${e.colno})`));
addEventListener('unhandledrejection', e => clientLog(`promesse rejetée : ${e.reason && (e.reason.stack || e.reason.message) || e.reason}`));

// ---------- Utilitaires ----------
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v; // via CSSOM : autorisé par la politique de sécurité (CSP)
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) el.setAttribute(k, v);
  }
  for (const k of kids.flat()) if (k !== null && k !== undefined) el.append(k);
  return el;
}
// tailles : en KB/MB/GB (réglage par défaut) ou en octets (Options)
let sizeUnits = (() => { try { return localStorage.getItem('synkro-units') || 'kb'; } catch { return 'kb'; } })();
const LANG = (() => { try { return localStorage.getItem('synkro-lang') || 'fr'; } catch { return 'fr'; } })();
function bytes(n) {
  const dec = LANG === 'en' ? '.' : ',';
  if (sizeUnits === 'bytes') return (n || 0).toLocaleString(LANG === 'en' ? 'en-US' : 'fr-FR') + (LANG === 'en' ? ' bytes' : ' octets');
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1).replace('.', dec) : n) + ' ' + u[i];
}
// Les deux côtés du job, présentés de la même façon quel que soit leur type :
// type (local, clé USB, SFTP), dossier, lecteur, système de fichiers, espace libre.
function sidePanel(e, dv, who, rm, need) {
  const body = [];
  let icon, kind, path, info = [], off = false;
  if (e.type === 'sftp' && e.sftp) {
    icon = '🔒'; kind = 'SFTP';
    path = e.sftp.path || '/';
    info.push(h('span', {}, `${e.sftp.user}@${e.sftp.host}${e.sftp.port && e.sftp.port !== 22 ? ':' + e.sftp.port : ''}`));
    // relevé au dernier passage : nom de la machine, protocole, logiciel du serveur
    if (rm) {
      if (rm.host) info.push(h('span', { title: 'Nom de la machine' }, h('code', {}, rm.host)));
      info.push(h('span', {}, rm.protocol));
      if (rm.software) info.push(h('span', { title: 'Logiciel du serveur' }, rm.software));
      if (rm.total) dv = { plugged: true, drive: { free: rm.free, total: rm.total } };
    }
  } else if (e.type === 'usb' && e.volume) {
    icon = '🔌';
    const sub = e.path && e.path !== '/' && e.path !== '\\' ? (/^[\\/]/.test(e.path) ? e.path : '\\' + e.path) : '\\';
    if (dv && dv.plugged) {
      const d = dv.drive;
      kind = d.removable ? 'Clé USB' : 'Disque externe';
      path = dv.dir;
      info.push(h('span', {}, h('code', {}, d.root.replace(/\\$/, '')), ` « ${d.label || '(sans nom)'} »`));
      info.push(h('span', {}, 'n° ', h('code', {}, d.serial || '—')));
      if (d.fs) info.push(h('span', {}, d.fs));
    } else {
      off = true; kind = 'Clé USB — non branchée';
      path = `« ${e.volume.label || e.volume.serial} » ${sub}`;
      info.push(h('span', {}, `n° ${e.volume.serial || '—'}`));
      info.push(h('span', {}, 'Le job la retrouvera dès qu\'elle sera branchée, quelle que soit sa lettre.'));
    }
  } else {
    icon = '💻'; kind = 'Local'; path = e.path;
    if (dv && dv.drive) {
      const d = dv.drive;
      info.push(h('span', {}, h('code', {}, d.root.replace(/\\$/, '')), d.label ? ` « ${d.label} »` : ''));
      if (d.fs) info.push(h('span', {}, d.fs));
    }
  }
  const d = dv && dv.plugged && dv.drive;
  let space = null;
  if (d && d.total) {
    const used = Math.round((1 - d.free / d.total) * 100);
    space = h('div', { class: 'side-space', title: `${used} % utilisé` },
      h('span', { class: 'minibar' }, h('i', { style: `width:${used}%` })),
      ` Libre : ${bytes(d.free)} sur ${bytes(d.total)}`);
  } else if (!off) {
    space = h('div', { class: 'side-space muted' }, e.type === 'sftp'
      ? (rm ? 'Espace libre non communiqué par le serveur' : 'Serveur distant · infos après une analyse') : 'Espace inconnu');
  }
  // place nécessaire de ce côté (fichiers à y copier), en rouge si elle manque
  if (space && need) {
    const short = d && d.total && need > d.free;
    space.append(h('span', { class: 'need' + (short ? ' short' : ''), title: 'Total des fichiers à copier de ce côté' },
      ` · À copier : ${bytes(need)}${short ? ' — place insuffisante' : ''}`));
  }
  return h('div', { class: 'side' + (off ? ' off' : '') },
    h('div', { class: 'side-head' }, h('span', { class: 'side-icon' }, icon),
      h('b', {}, kind), h('span', { class: 'muted' }, ' · ' + who)),
    h('div', { class: 'side-path', title: path }, path),
    h('div', { class: 'side-info' }, ...info),
    space);
}
// Octets à copier vers chaque côté d'après l'analyse affichée (sens choisis compris).
let needCache = { key: '', v: [0, 0] };
function neededBytes() {
  const plan = details.plan || [];
  const key = selected + '|' + plan.length + '|' + (plan[0] && plan[0].path) + '|' + ovVer;
  if (needCache.key === key) return needCache.v;
  let toL = 0, toR = 0;
  for (const a of plan) {
    const k = effKind(a);
    if (k === 'copy→') toR += (a.left && a.left.size) || 0;
    else if (k === 'copy←') toL += (a.right && a.right.size) || 0;
  }
  needCache = { key, v: [toL, toR] };
  return needCache.v;
}
function renderSides(j) {
  const rm = j.remote || {}, [toL, toR] = neededBytes();
  $('#job-sides').replaceChildren(
    sidePanel(j.left, j.drives && j.drives.left, 'gauche', rm.left, toL),
    h('div', { class: 'side-arrow', title: MODES[j.options.mode][1] }, MODES[j.options.mode][0]),
    sidePanel(j.right, j.drives && j.drives.right, 'droite', rm.right, toR));
}

function epLabel(e, dv) {
  if (e.type === 'usb' && e.volume) {
    const sub = e.path && e.path !== '/' && e.path !== '\\' ? (/^[\\/]/.test(e.path) ? e.path : '\\' + e.path) : '\\';
    const name = e.volume.label || e.volume.serial;
    if (dv && dv.plugged) return `🔌 ${dv.drive.root.replace(/\\$/, '')} « ${name} » ${sub}`;
    if (dv) return `🔌 « ${name} » (non branchée) ${sub}`;
    return `🔌 « ${name} » ${sub}`;
  }
  if (e.type === 'sftp' && e.sftp) return `sftp://${e.sftp.user}@${e.sftp.host}${e.sftp.port && e.sftp.port !== 22 ? ':' + e.sftp.port : ''}${e.sftp.path || ''}`;
  return e.path;
}
const MODES = { 'two-way': ['⇄', 'Bidirectionnel'], mirror: ['→', 'Miroir'], backup: ['→', 'Sauvegarde'] };
const KINDS = {
  'copy→': ['Copier →', ''], 'copy←': ['← Copier', ''], 'delete←': ['← Supprimer', 'del'], 'delete→': ['Supprimer →', 'del'],
  'mkdir←': ['← Dossier', ''], 'mkdir→': ['Dossier →', ''], 'skip': ['Ignoré', 'skip'],
  'same': ['Identique', 'same'],
  'leave': ['Laissé tel quel', 'same'],
};
const fmtDate = d => (!d || d.startsWith('0001')) ? 'jamais' : new Date(d).toLocaleString('fr-FR');

// ---------- État ----------
let jobs = [];
let selected = localStorage.getItem('synkro-selected') || null;
let details = { plan: [], marks: { marks: '', errors: {} } };
let view = localStorage.getItem('synkro-view') || 'tree';
let expanded = new Set(['']); // dossiers dépliés (chemins)
let expandedFor = null;
let knownDirs = new Set(); // dossiers déjà affichés (leur état ouvert/fermé est conservé)
let showSame = localStorage.getItem('synkro-same') === '1';
let sameRows = null; // éléments identiques, chargés à la demande

let lastState = {};
let tab = 'actions';
let rate = { job: null, start: null, samples: [] };
function duration(sec) {
  sec = Math.round(sec);
  if (sec < 60) return sec + ' s';
  if (sec < 3600) return Math.floor(sec / 60) + ' min ' + String(sec % 60).padStart(2, '0') + ' s';
  return Math.floor(sec / 3600) + ' h ' + String(Math.floor(sec % 3600 / 60)).padStart(2, '0') + ' min';
}

async function refresh() {
  try {
    [jobs, groups, plans] = await Promise.all([api('GET', '/api/jobs'), api('GET', '/api/groups'), api('GET', '/api/schedules')]);
  } catch (e) {
    $('#sidebar-foot').textContent = 'Moteur injoignable : ' + e.message;
    return;
  }
  if (selected && !jobs.find(j => j.id === selected)) selected = null;
  if (!selected && jobs.length) selected = jobs[0].id;
  checkSeriesEnd();
  if (followSeries()) return; // la sélection a suivi le job en cours (déjà affiché)
  renderList();
  renderJob();
}

// Lancement en série : la sélection suit le job en cours. Si l'utilisateur choisit
// lui-même un autre job, on le laisse tranquille jusqu'au démarrage du suivant.
let seriesOn = false, followOff = null;
let lastSelBusy = null; // job affiché qui était en cours au rafraîchissement précédent
function followSeries() {
  const st = j => j.status.state;
  const running = j => ['analyzing', 'syncing', 'conflicts'].includes(st(j));
  // Le job affiché vient d'être annulé alors qu'un autre tourne (série ou lancé
  // à part) : on passe à celui-ci.
  const cur = jobs.find(j => j.id === selected);
  const wasBusy = lastSelBusy === selected;
  lastSelBusy = cur && isBusy(cur) ? cur.id : null;
  if (cur && wasBusy && !isBusy(cur) && /annul/i.test(cur.status.message || '')) {
    const other = jobs.find(running) || jobs.find(j => isBusy(j));
    if (other) {
      followOff = null;
      select(other.id);
      lastSelBusy = other.id;
      return true;
    }
  }
  if (jobs.some(j => st(j) === 'queued')) seriesOn = true;
  if (!seriesOn) return false;
  const run = jobs.find(j => ['analyzing', 'syncing', 'conflicts'].includes(st(j)));
  if (!run && !jobs.some(j => ['queued', 'waiting'].includes(st(j)))) { seriesOn = false; followOff = null; return false; }
  if (!run || run.id === selected || run.id === followOff) return false;
  followOff = null;
  select(run.id);
  // garder le job visible dans la liste
  const li = $(`#job-list li[data-id="${run.id}"]`);
  if (li) li.scrollIntoView({ block: 'nearest' });
  return true;
}

const BUSY = ['analyzing', 'syncing', 'waiting', 'queued'];
const isBusy = j => j && BUSY.includes(j.status.state);
let checked = new Set(); // jobs cochés pour un lancement en série

function jobPct(j) {
  const p = j.status.progress || {};
  if (j.status.state !== 'syncing' || !p.totalActions) return null;
  return Math.floor(syncPct(p));
}

// Ordre des jobs : glisser-déposer dans la liste (pas de rafraîchissement pendant le geste)
let dragId = null;
function dropPos(li, e) {
  const r = li.getBoundingClientRect();
  return e.clientY < r.top + r.height / 2 ? 'before' : 'after';
}
async function moveJob(id, targetId, where) {
  const ids = jobs.map(j => j.id).filter(x => x !== id);
  let i = targetId ? ids.indexOf(targetId) : ids.length;
  if (where === 'after') i++;
  ids.splice(i, 0, id);
  jobs.sort((a, b) => ids.indexOf(a.id) - ids.indexOf(b.id)); // tout de suite à l'écran
  renderList();
  try { await api('PUT', '/api/jobs-order', ids); } catch (e) { toast('Ordre non enregistré : ' + e.message); }
  refresh();
}
// Groupes de tâches : « Tout » contient toutes les tâches ; chaque groupe créé
// contient les siennes (une tâche est dans un groupe au plus).
let groups = [];
let groupsClosed = new Set((() => { try { return JSON.parse(localStorage.getItem('synkro-groups-closed') || '[]'); } catch { return []; } })());
const ALL = '*'; // clé du groupe « Tout »
function saveGroupsClosed() {
  const v = JSON.stringify([...groupsClosed]);
  try { localStorage.setItem('synkro-groups-closed', v); } catch {}
  api('PUT', '/api/settings', { groupsClosed: v }).catch(() => {});
}
api('GET', '/api/settings').then(st => { if (st.groupsClosed) { try { groupsClosed = new Set(JSON.parse(st.groupsClosed)); renderList(); } catch {} } }).catch(() => {});
// une tâche peut être dans plusieurs groupes : on l'ajoute ou on la retire d'un groupe
const inGroup = (j, g) => (j && j.groups || []).includes(g);
async function groupOp(id, action, group, quiet) {
  try { await api('POST', `/api/jobs/${id}/group`, { action, group }); } catch (e) { toast(e.message); }
  if (!quiet) refresh();
}
let dragFrom = null; // groupe d'où part la tâche glissée (null = « Tout »)

// ---------- Planifications : lancement d'une tâche ou d'un groupe à une date et une heure ----------
let plans = [];
const REPEATS = { '': 'une seule fois', daily: 'tous les jours', weekdays: 'du lundi au vendredi', weekly: 'toutes les semaines', monthly: 'tous les mois' };
const fmtPlan = d => new Date(d).toLocaleString('fr-FR', { weekday: 'short', day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' });
// prochain lancement prévu pour une tâche ou un groupe (null s'il n'y en a pas)
function nextPlan(kind, ref) {
  const p = plans.filter(x => x.enabled && x.kind === kind && x.ref === ref).sort((a, b) => new Date(a.at) - new Date(b.at))[0];
  return p ? p.at : null;
}
let plansFor = null;
function openPlans(kind, ref, label) {
  plansFor = { kind, ref, label };
  $('#plans-title').textContent = 'Planifier : ' + label;
  const now = new Date(); now.setMinutes(0, 0, 0); now.setHours(now.getHours() + 1);
  const pad = n => String(n).padStart(2, '0');
  $('#plan-date').value = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
  $('#plan-time').value = `${pad(now.getHours())}:00`;
  $('#plan-repeat').value = '';
  $('#plan-err').textContent = '';
  renderPlans();
  $('#dlg-plans').showModal();
}
function renderPlans() {
  if (!plansFor) return;
  const mine = plans.filter(p => p.kind === plansFor.kind && p.ref === plansFor.ref)
    .sort((a, b) => (b.enabled - a.enabled) || new Date(a.at) - new Date(b.at));
  const body = $('#plans-body');
  body.replaceChildren(...mine.map(p => {
    const box = h('input', { type: 'checkbox', title: 'Active' }); box.checked = p.enabled;
    box.onchange = async () => { try { await api('PUT', `/api/schedules/${p.id}`, { enabled: box.checked }); } catch (e) { toast(e.message); } await reloadPlans(); };
    return h('tr', { class: p.enabled ? '' : 'off' },
      h('td', {}, box),
      h('td', {}, p.enabled ? fmtPlan(p.at) : (p.repeat ? fmtPlan(p.at) : 'fait / terminé')),
      h('td', {}, REPEATS[p.repeat || ''] || p.repeat),
      h('td', { class: 'muted' }, p.note ? p.note + (p.lastRun && p.note === 'lancé' ? ' le ' + fmtPlan(p.lastRun) : '') : ''),
      h('td', {}, Object.assign(h('button', { type: 'button', class: 'ghost small', title: 'Supprimer cette planification' }, '×'),
        { onclick: async () => { try { await api('DELETE', `/api/schedules/${p.id}`); } catch (e) { toast(e.message); } await reloadPlans(); } })));
  }));
  $('#plans-empty').hidden = mine.length > 0;
}
async function reloadPlans() {
  plans = await api('GET', '/api/schedules').catch(() => plans);
  renderPlans(); renderList();
}
$('#plan-add').onclick = async () => {
  const d = $('#plan-date').value, t = $('#plan-time').value;
  $('#plan-err').textContent = '';
  if (!d || !t) { $('#plan-err').textContent = 'Indiquez la date et l\'heure.'; return; }
  const at = new Date(`${d}T${t}`);
  try {
    await api('POST', '/api/schedules', { kind: plansFor.kind, ref: plansFor.ref, at: at.toISOString(), repeat: $('#plan-repeat').value });
  } catch (e) { $('#plan-err').textContent = e.message; return; }
  await reloadPlans();
};
$('#plans-close').onclick = () => { $('#dlg-plans').close(); plansFor = null; };
function taskLi(j, gname) {
    const st = j.status.state;
    const dot = jobDot(j);
    let sub = MODES[j.options.mode][1] + autoLabel(j);
    const np = nextPlan('job', j.id);
    if (np) sub += ' · ⏰ ' + new Date(np).toLocaleString('fr-FR', { weekday: 'short', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });
    const pct = jobPct(j);
    if (st === 'queued') sub = '⏳ ' + (j.status.message || 'en file d\'attente').replace(/^En file d'attente/, 'en file d\'attente').replace(/\((\d+) sur (\d+)\)/, ': $1 sur $2');
    else if (st === 'waiting') sub = '⏳ en attente d\'un dossier';
    else if (st === 'syncing') sub = (j.status.paused ? '⏸ En pause' : 'Synchronisation…') + (pct !== null ? ` ${pct} %` : '');
    else if (st === 'analyzing') sub = 'Analyse en cours…';
    else if (st === 'conflicts') sub = '⚠ conflits à résoudre';
    const running = st === 'syncing' || st === 'analyzing';
    const pending = st === 'queued' || st === 'waiting';
    const box = h('input', { type: 'checkbox', class: 'pick', title: 'Sélectionner pour lancer plusieurs jobs à la suite (ordre de lancement = ordre de sélection)' });
    box.checked = checked.has(j.id);
    // un Set garde l'ordre d'insertion : 1er coché = 1er lancé
    box.onclick = e => { e.stopPropagation(); box.checked ? checked.add(j.id) : checked.delete(j.id); renderList(); };
    const rank = checked.has(j.id) ? [...checked].indexOf(j.id) + 1 : 0;
    return h('li', {
      'data-id': j.id,
      class: (j.id === selected && groupView === null ? 'sel ' : '') + (checked.has(j.id) ? 'checked ' : '') + (running ? 'running ' : '') + (pending ? 'pending' : ''),
      draggable: 'true', title: 'Double-clic : modifier la tâche · glisser pour changer l\'ordre',
      ondblclick: e => {
        if (e.target.closest('input')) return;
        if (selected !== j.id) select(j.id);
        if (isBusy(j)) { toast('Tâche en cours : modification possible une fois terminée.'); return; }
        window.getSelection()?.removeAllRanges();
        openForm(j);
      },
      ondragstart: e => { dragId = j.id; dragFrom = gname; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', j.id); e.currentTarget.classList.add('dragging'); },
      ondragend: e => { e.currentTarget.classList.remove('dragging'); $$('#job-list li').forEach(x => x.classList.remove('drop-before', 'drop-after')); dragId = null; },
      ondragover: e => {
        if (!dragId || dragId === j.id) return;
        e.preventDefault(); e.dataTransfer.dropEffect = 'move';
        const w = dropPos(e.currentTarget, e);
        e.currentTarget.classList.toggle('drop-before', w === 'before');
        e.currentTarget.classList.toggle('drop-after', w === 'after');
      },
      ondragleave: e => e.currentTarget.classList.remove('drop-before', 'drop-after'),
      ondrop: e => {
        e.preventDefault(); e.stopPropagation();
        const id = dragId, w = dropPos(e.currentTarget, e);
        dragId = null;
        // lâché dans la liste d'un autre groupe : la tâche y passe aussi
        if (id && gname !== null && !inGroup(jobs.find(x => x.id === id), gname)) dropInGroup(id, gname);
        if (id && id !== j.id) moveJob(id, j.id, w);
      },
      onclick: () => {
        if (seriesOn) { const run = jobs.find(x => ['analyzing', 'syncing', 'conflicts'].includes(x.status.state)); followOff = run && run.id !== j.id ? run.id : null; }
        select(j.id);
      },
      oncontextmenu: e => { e.preventDefault(); select(j.id); showJobMenu(j, e.clientX, e.clientY, gname); },
    }, box, running ? h('span', { class: 'spin', title: 'En cours' }) : h('span', { class: 'dot ' + dot }),
      h('span', { class: 'name' }, rank ? h('span', { class: 'rank', title: 'Ordre de lancement' }, String(rank)) : null, j.name),
      h('span', { class: 'sub' }, sub),
      running ? h('span', { class: 'jobbar' + (pct === null ? ' indet' : '') }, h('i', { style: `width:${pct ?? 30}%` })) : null);
}
// tâche lâchée dans un groupe : ajoutée à ce groupe ; si elle venait d'un autre
// groupe, elle le quitte (déplacement). Lâchée sur « Tout » : retirée de son groupe d'origine.
async function dropInGroup(id, target) {
  const from = dragFrom; dragFrom = null;
  if (target) await groupOp(id, 'add', target, true);
  if (from && from !== target) await groupOp(id, 'remove', from, true);
  refresh();
}
function groupHead(key, label, list) {
  const closed = groupsClosed.has(key);
  const gname = key === ALL ? '' : key;
  const running = list.filter(isBusy).length;
  return h('li', {
    class: 'ghead' + (closed ? ' closed' : '') + (groupView === key ? ' gsel' : ''), title: 'Clic : afficher le groupe · flèche : replier / déplier · clic droit : lancer, planifier…',
    onclick: e => {
      if (e.target.closest('.gtwist')) { closed ? groupsClosed.delete(key) : groupsClosed.add(key); saveGroupsClosed(); renderList(); return; }
      openGroup(key);
    },
    oncontextmenu: e => { e.preventDefault(); showGroupMenu(key, label, list, e.clientX, e.clientY); },
    // déposer une tâche sur l'en-tête : elle passe dans ce groupe (« Tout » = sans groupe)
    ondragover: e => { if (!dragId) return; e.preventDefault(); e.currentTarget.classList.add('drop-in'); },
    ondragleave: e => e.currentTarget.classList.remove('drop-in'),
    ondrop: e => { e.preventDefault(); e.currentTarget.classList.remove('drop-in'); const id = dragId; dragId = null; if (id) dropInGroup(id, gname || null); },
  }, h('span', { class: 'gtwist' }, closed ? '▸' : '▾'),
    h('span', { class: 'gname' }, (key === ALL ? '🗂 ' : '📁 ') + label),
    (np => np ? h('span', { class: 'gplan', title: 'Prochain lancement planifié : ' + fmtPlan(np) }, '⏰ ' + new Date(np).toLocaleString('fr-FR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })) : null)(nextPlan('group', key === ALL ? '*' : key)),
    h('span', { class: 'gcount' }, running ? `${running} en cours · ${list.length}` : String(list.length)));
}
// dernier passage réussi : « OK », « À jour, rien à faire », dates corrigées… (sans erreur)
// couleur de la pastille d'une tâche : le résultat du dernier passage fait foi
// (une analyse ne l'efface pas : « 1 erreur(s) » reste rouge jusqu'à la synchro suivante)
function jobDot(j) {
  const st = j.status.state, r = j.lastResult || '';
  if (st === 'analyzing' || st === 'syncing') return 'run';
  if (st === 'conflicts' || st === 'waiting' || st === 'queued') return 'wait';
  if (st === 'error') return 'err';
  if (r === 'Erreur : Annulé') return 'cancel'; // ancienne façon d'écrire une annulation
  if (r) return okResult(r) ? 'ok' : /^Annulé/.test(r) && !/erreur/.test(r) ? 'cancel' : 'err';
  return st === 'done' ? 'ok' : '';
}
function okResult(r) {
  return !!r && /^(OK|À jour|Dates|Le disque de droite n.enregistre)/.test(r) && !/erreur|impossible/i.test(r);
}
function renderList() {
  if (dragId) return;
  const ul = $('#job-list'); ul.replaceChildren();
  for (const id of [...checked]) if (!jobs.find(j => j.id === id)) checked.delete(id);
  const sections = [[ALL, 'Tout', jobs]].concat(groups.map(g => [g, g, jobs.filter(j => inGroup(j, g))]));
  for (const [key, label, list] of sections) {
    ul.append(groupHead(key, label, list));
    if (groupsClosed.has(key)) continue;
    if (!list.length && key !== ALL) ul.append(h('li', { class: 'gempty' }, 'Glissez une tâche ici, ou clic droit sur une tâche › Déplacer vers…'));
    for (const j of list) ul.append(taskLi(j, key === ALL ? null : key));
  }
  $('#sel-bar').hidden = checked.size === 0;
  $('#sel-count').textContent = `${checked.size} tâche(s) sélectionnée(s) · lancées dans l'ordre de sélection`;
}

$('#sel-clear').onclick = () => { checked.clear(); renderList(); };
// Série lancée d'ici (tâches cochées ou groupe) : résumé à la fin.
let seriesRun = null;
function startSeries(ids, label) { seriesRun = { ids, label, started: Date.now() }; }
// synchronisation lancée à la main (hors série) : résumé à la fin, comme pour une série
const soloRuns = new Map(); // id → { started, busySeen }
function startSolo(id) { if (!seriesRun || !seriesRun.ids.includes(id)) soloRuns.set(id, { started: Date.now(), busySeen: false }); }
function checkSoloEnd() {
  for (const [id, run] of soloRuns) {
    const j = jobs.find(x => x.id === id);
    if (!j) { soloRuns.delete(id); continue; }
    if (isBusy(j) || j.status.state === 'conflicts') { run.busySeen = true; continue; }
    if (!run.busySeen && Date.now() - run.started < 4000) continue; // le passage n'a pas encore démarré
    soloRuns.delete(id);
    if (j.status.state === 'conflicts') continue;
    showSeriesSummary({ started: run.started, label: j.name, solo: true }, [j]);
  }
}
function checkSeriesEnd() {
  checkSoloEnd();
  if (!seriesRun || Date.now() - seriesRun.started < 2500) return;
  const list = seriesRun.ids.map(id => jobs.find(j => j.id === id)).filter(Boolean);
  if (list.some(j => isBusy(j))) return;
  const run = seriesRun; seriesRun = null;
  showSeriesSummary(run, list);
}
function showSeriesSummary(run, list) {
  const kind = j => {
    const r = j.lastResult || '', when = j.lastRun ? new Date(j.lastRun).getTime() : 0;
    if (when < run.started - 1000) return ['skip', 'non lancée'];
    if (okResult(r)) return ['ok', r];
    if (/^Annulé/.test(r)) return /erreur/.test(r) ? ['err', r] : ['cancel', r];
    return ['err', r || 'erreur'];
  };
  const rows = list.map(j => [j, kind(j)]);
  const n = k => rows.filter(r => r[1][0] === k).length;
  const total = Math.round((Date.now() - run.started) / 1000);
  $('#sum-title').textContent = run.solo ? `Synchronisation terminée : ${run.label}` : run.label ? `Série terminée : ${run.label}` : 'Série de tâches terminée';
  $('#sum-head').textContent = run.solo ? `Durée ${duration(total)}` + (list[0].status.summary && list[0].status.summary.bytes ? ` · ${bytes(list[0].status.summary.bytes)} à copier au départ` : '')
    : `${list.length} tâche(s) · ${n('ok')} OK` + (n('cancel') ? ` · ${n('cancel')} annulée(s)` : '') +
    (n('err') ? ` · ${n('err')} en erreur` : '') + (n('skip') ? ` · ${n('skip')} non lancée(s)` : '') + ` · durée totale ${duration(total)}`;
  $('#sum-body').replaceChildren(...rows.map(([j, [k, label]]) => {
    const s = j.status.summary || {};
    const done = s.synced ? `${s.toRight + s.toLeft + s.delLeft + s.delRight || 0} action(s)` : '';
    return h('tr', { class: 'sum-' + k },
      h('td', {}, h('span', { class: 'dot ' + (k === 'ok' ? 'ok' : k === 'err' ? 'err' : k === 'cancel' ? 'cancel' : '') }), ' ', j.name),
      h('td', {}, label),
      h('td', { class: 'muted' }, j.status.message || done),
      h('td', { class: 'num muted' }, j.lastRun && k !== 'skip' ? new Date(j.lastRun).toLocaleTimeString('fr-FR') : ''));
  }));
  $('#dlg-summary').showModal();
}
$('#sum-close').onclick = () => $('#dlg-summary').close();

$('#sel-run').onclick = async () => {
  const ids = [...checked]; // dans l'ordre de sélection
  try { await api('POST', '/api/queue', { ids }); checked.clear(); seriesOn = true; followOff = null; startSeries(ids, ''); } catch (e) { alertBox(e.message); }
  setTimeout(refresh, 150);
};

// ---- menu du clic droit
// gname : groupe dans lequel on a cliqué (null = « Tout »)
function showJobMenu(j, x, y, gname = null) {
  const m = $('#ctx-menu');
  const busy = isBusy(j);
  const item = (label, fn, opts = {}) => h('button', { type: 'button', class: opts.cls || '', disabled: opts.disabled ? '' : null,
    onclick: () => { hideMenu(); fn(); } }, label);
  m.replaceChildren(...[
    h('div', { class: 'ctx-title' }, j.name),
    item('Analyser', () => act('analyze'), { disabled: busy }),
    item('Synchroniser', () => $('#btn-sync').onclick(), { disabled: busy }),
    busy ? item(j.status.state === 'queued' ? 'Retirer de la file d\'attente' : 'Annuler', () => act('cancel')) : null,
    h('hr'),
    item('Modifier…', () => openForm(j), { disabled: busy }),
    item('Dupliquer…', () => openForm({ ...j, id: null, name: j.name + ' (copie)', lastRun: null }, true), {}),
    h('hr'),
    item('Planifier…', () => openPlans('job', j.id, j.name)),
    h('hr'),
    groups.some(g => !inGroup(j, g)) ? h('div', { class: 'ctx-sub' }, 'Ajouter au groupe :') : null,
    ...groups.filter(g => !inGroup(j, g)).map(g => item('   📁 ' + g, () => groupOp(j.id, 'add', g))),
    !gname && (j.groups || []).length ? h('div', { class: 'ctx-sub' }, 'Retirer du groupe :') : null,
    ...(!gname ? (j.groups || []) : []).map(g => item('   📁 ' + g, () => groupOp(j.id, 'remove', g))),
    groups.length ? h('hr') : null,
    (() => { const i = jobs.findIndex(x => x.id === j.id); return item('Monter', () => moveJob(j.id, jobs[i - 1].id, 'before'), { disabled: i <= 0 }); })(),
    (() => { const i = jobs.findIndex(x => x.id === j.id); return item('Descendre', () => moveJob(j.id, jobs[i + 1].id, 'after'), { disabled: i >= jobs.length - 1 }); })(),
    h('hr'),
    // depuis un groupe : la tâche quitte le groupe et reste dans « Tout » ;
    // depuis « Tout » : la tâche est supprimée (et disparaît donc de son groupe)
    gname
      ? item(`Supprimer du groupe « ${gname} »`, () => groupOp(j.id, 'remove', gname), { cls: 'danger-text' })
      : item('Supprimer…', () => deleteJob(j), { cls: 'danger-text', disabled: busy }),
  ].filter(Boolean));
  m.hidden = false;
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(x, innerWidth - r.width - 8) + 'px';
  m.style.top = Math.min(y, innerHeight - r.height - 8) + 'px';
}
// clic droit sur un groupe : lancer toutes ses tâches, renommer, supprimer
function showGroupMenu(key, label, list, x, y) {
  const m = $('#ctx-menu');
  const item = (txt, fn, opts = {}) => h('button', { type: 'button', class: opts.cls || '', disabled: opts.disabled ? '' : null,
    onclick: () => { hideMenu(); fn(); } }, txt);
  const free = list.filter(j => !isBusy(j));
  const runAll = async () => {
    try { await api('POST', '/api/queue', { ids: free.map(j => j.id) }); seriesOn = true; followOff = null; startSeries(free.map(j => j.id), key === ALL ? 'Tout' : 'groupe « ' + label + ' »'); } catch (e) { alertBox(e.message); }
    setTimeout(refresh, 150);
  };
  m.replaceChildren(...[
    h('div', { class: 'ctx-title' }, (key === ALL ? 'Tout' : 'Groupe « ' + label + ' »') + ` · ${list.length} tâche(s)`),
    item(`Synchroniser les ${free.length} tâche(s) à la suite`, runAll, { disabled: !free.length }),
    item('Planifier…', () => openPlans('group', key === ALL ? '*' : label, key === ALL ? 'Tout' : 'groupe « ' + label + ' »')),
    key === ALL ? null : h('hr'),
    key === ALL ? null : item('Renommer…', async () => {
      const n = await askText('Renommer le groupe', 'Nouveau nom', label);
      if (n && n !== label) try { await api('POST', '/api/groups', { action: 'rename', name: label, newName: n }); } catch (e) { toast(e.message); }
      refresh();
    }),
    key === ALL ? null : item('Supprimer le groupe…', async () => {
      if (!await askConfirm(`Supprimer le groupe « ${label} » ?\nSes ${list.length} tâche(s) ne sont pas supprimées : elles restent dans « Tout ».`, { title: 'Supprimer le groupe', ok: 'Supprimer', danger: true })) return;
      try { await api('POST', '/api/groups', { action: 'delete', name: label }); } catch (e) { toast(e.message); }
      refresh();
    }, { cls: 'danger-text' }),
  ].filter(Boolean));
  placeMenu(m, x, y);
}
async function newGroup() {
  const n = await askText('Nouveau groupe', 'Nom du groupe', '');
  if (!n) return;
  try { await api('POST', '/api/groups', { action: 'create', name: n }); groupsClosed.delete(n); } catch (e) { toast(e.message); }
  refresh();
}
// petite fenêtre de saisie (window.prompt n'existe pas dans l'application)
// Confirmation dans une fenêtre de Synkro (la boîte du navigateur affiche
// « 127.0.0.1:… indique » et n'est pas traduite). Une ligne de texte par ligne du
// message. Danger : bouton rouge, et Entrée vise « Annuler » par prudence.
function askConfirm(text, { title = 'Confirmer', ok = 'Continuer', danger = false } = {}) {
  const d = $('#dlg-confirm');
  $('#confirm-title').textContent = title;
  $('#confirm-text').replaceChildren(...String(text).split('\n').map(l => l ? h('div', {}, l) : h('div', { class: 'gap' })));
  const yes = $('#confirm-yes'), no = $('#confirm-no');
  yes.textContent = ok; yes.className = danger ? 'danger' : 'primary';
  return new Promise(resolve => {
    const done = v => { d.close(); resolve(v); };
    yes.onclick = () => done(true);
    no.onclick = () => done(false);
    d.oncancel = () => resolve(false);
    d.showModal();
    (danger ? no : yes).focus();
  });
}
function askText(title, label, value) {
  const d = $('#dlg-text');
  $('#text-title').textContent = title; $('#text-label').textContent = label;
  const inp = $('#text-input'); inp.value = value || '';
  return new Promise(resolve => {
    const done = v => { d.close(); resolve(v); };
    $('#text-ok').onclick = () => done(inp.value.trim());
    $('#text-cancel').onclick = () => done(null);
    inp.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); done(inp.value.trim()); } };
    d.oncancel = () => resolve(null);
    d.showModal(); inp.select();
  });
}
function placeMenu(m, x, y) {
  placeMenu.at = Date.now();
  m.hidden = false;
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(x, innerWidth - r.width - 8) + 'px';
  m.style.top = Math.max(8, Math.min(y, innerHeight - r.height - 8)) + 'px';
}
// ---- clic droit sur un fichier ou un dossier de la liste
function showItemMenu(p, isDir, x, y) {
  const j = jobs.find(z => z.id === selected);
  if (!j) return;
  const busy = isBusy(j);
  const plan = allRows();
  const a = plan.find(z => z.path === p);
  const under = plan.filter(z => z.path === p || z.path.startsWith(p + '/'));
  const ex = s => a ? !!(a[s] && a[s].exists) : under.some(z => z[s] && z[s].exists);
  const name = p.split('/').pop();
  const dot = name.lastIndexOf('.');
  const ext = !isDir && dot > 0 ? name.slice(dot) : '';
  const what = isDir ? 'le dossier' : 'le fichier';
  const m = $('#ctx-menu');
  const item = (label, fn, opts = {}) => h('button', { type: 'button', class: opts.cls || '', disabled: opts.disabled ? '' : null,
    onclick: () => { hideMenu(); fn(); } }, label);
  const editable = canEdit() && under.some(z => z.kind !== 'same' && z.kind !== 'skip');
  const setAll = d => { under.filter(z => z.kind !== 'same' && z.kind !== 'skip').forEach(z => setDir(z, d)); renderActions(); refresh(); };
  const local = s => ['local', 'usb'].includes((s === 'left' ? j.left : j.right).type);
  const viewItems = isDir ? [] : [
    ex('left') ? item('👁 Voir le contenu (gauche)', () => viewFile(p, 'left')) : null,
    ex('right') ? item('👁 Voir le contenu (droite)', () => viewFile(p, 'right')) : null,
    ex('left') && ex('right') ? item('⇆ Comparer les deux côtés', () => compareFiles(p)) : null,
    ...['left', 'right'].filter(s => ex(s) && local(s)).map(s =>
      item(`↗ Ouvrir avec son application (${s === 'left' ? 'gauche' : 'droite'})`, () => openItem(p, s, false))),
    h('hr'),
  ];
  if (isDir) ['left', 'right'].filter(s => ex(s) && local(s)).forEach(s =>
    viewItems.push(item(`📂 Ouvrir dans l'explorateur (${s === 'left' ? 'gauche' : 'droite'})`, () => openItem(p, s, true))));
  if (isDir && viewItems.length) viewItems.push(h('hr'));
  m.replaceChildren(...[
    h('div', { class: 'ctx-title' }, p + (isDir ? '/' : '')),
    ...viewItems,
    editable ? item('➜ Copier vers la droite', () => setAll('right')) : null,
    editable ? item('⬅ Copier vers la gauche', () => setAll('left')) : null,
    editable ? item('= Ne rien faire', () => setAll('none')) : null,
    editable ? h('hr') : null,
    item(`🗑 Supprimer ${what} à gauche…`, () => deleteItem(p, isDir, 'left'), { cls: 'danger-text', disabled: busy || !ex('left') }),
    item(`🗑 Supprimer ${what} à droite…`, () => deleteItem(p, isDir, 'right'), { cls: 'danger-text', disabled: busy || !ex('right') }),
    item(`🗑 Supprimer ${what} des deux côtés…`, () => deleteItem(p, isDir, 'both'), { cls: 'danger-text', disabled: busy || !(ex('left') && ex('right')) }),
    h('hr'),
    item(`Exclure « /${p} »`, () => addFilter('exclude', '/' + p), { disabled: busy }),
    item(`Exclure tous les « ${name} »`, () => addFilter('exclude', name), { disabled: busy }),
    ext ? item(`Exclure tous les « *${ext} »`, () => addFilter('exclude', '*' + ext), { disabled: busy }) : null,
    h('hr'),
    item(`Inclure seulement « /${p} »`, () => addFilter('include', '/' + p), { disabled: busy }),
    ext ? item(`Inclure seulement les « *${ext} »`, () => addFilter('include', '*' + ext), { disabled: busy }) : null,
    isDir ? h('hr') : null,
    isDir ? item('Tout déplier dans ce dossier', () => {
      allRows().forEach(z => { if (z.path.startsWith(p + '/')) { const parts = z.path.split('/'); for (let k = 1; k < parts.length; k++) expanded.add(parts.slice(0, k).join('/')); } });
      expanded.add(p); renderActions();
    }) : null,
  ].filter(Boolean));
  placeMenu(m, x, y);
}
// ---- Voir le contenu d'un fichier (début du fichier : 2 Mo au plus) ----
const IMG_TYPES = { png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp', bmp: 'image/bmp', ico: 'image/x-icon', svg: 'image/svg+xml' };
let viewUrl = null;
async function openItem(p, side, reveal) {
  try { await api('POST', `/api/jobs/${selected}/open-item`, { path: p, side, reveal }); } catch (e) { alertBox(e.message); }
}
function hexDump(b, max) {
  const out = [];
  const n = Math.min(b.length, max);
  for (let o = 0; o < n; o += 16) {
    const row = b.subarray(o, Math.min(o + 16, n));
    const hex = [...row].map(x => x.toString(16).padStart(2, '0')).join(' ').padEnd(47);
    const asc = [...row].map(x => x >= 32 && x < 127 ? String.fromCharCode(x) : '.').join('');
    out.push(o.toString(16).padStart(8, '0') + '  ' + hex + '  ' + asc);
  }
  return out.join('\n');
}
function decodeText(b) {
  if (b[0] === 0xFF && b[1] === 0xFE) return new TextDecoder('utf-16le').decode(b);
  if (b[0] === 0xFE && b[1] === 0xFF) return new TextDecoder('utf-16be').decode(b);
  try { return new TextDecoder('utf-8', { fatal: true }).decode(b); }
  catch { // fichier coupé au milieu d'un caractère, ou ancien encodage Windows
    try { return new TextDecoder('utf-8', { fatal: true }).decode(b.subarray(0, Math.max(0, b.length - 3))); }
    catch { return new TextDecoder('windows-1252').decode(b); }
  }
}
// fichier précédent / suivant dans l'ordre de la liste affichée
let viewState = null; // { p, side, cmp }
function siblingFile(p, d, ok) {
  const rows = allRows();
  const byPath = new Map(rows.map(r => [r.path, r]));
  let order = rowQueue.fns.map(r => r[0]).filter(x => byPath.has(x));
  if (!order.includes(p)) order = rows.map(r => r.path); // dossier replié : ordre complet
  for (let i = order.indexOf(p) + d; i >= 0 && i < order.length; i += d) {
    const a = byPath.get(order[i]);
    if (a && !a.isDir && ok(a)) return a.path;
  }
  return null;
}
function viewOk(v) {
  return v.cmp ? a => a.left && a.left.exists && a.right && a.right.exists : a => a[v.side] && a[v.side].exists;
}
function setViewNav(v) {
  viewState = v;
  const prev = siblingFile(v.p, -1, viewOk(v)), next = siblingFile(v.p, 1, viewOk(v));
  $('#view-prev-file').disabled = !prev;
  $('#view-next-file').disabled = !next;
  $('#view-prev-file').onclick = () => prev && openView(prev, v);
  $('#view-next-file').onclick = () => next && openView(next, v);
  // la ligne correspondante est marquée dans la liste
  if (detailPath !== null) { detailPath = v.p; renderDetail(); } // le détail ouvert suit le fichier
  $$('#actions-body tr.picked').forEach(t => t.classList.remove('picked'));
  const tr = $(`#actions-body tr[data-path="${CSS.escape(v.p)}"]`); if (tr) tr.classList.add('picked');
}
function openView(p, v) { return v.cmp ? compareFiles(p) : viewFile(p, v.side); }
document.addEventListener('keydown', e => {
  if (!$('#dlg-view').open || e.target.closest('input, textarea, select')) return;
  if (e.key === 'ArrowLeft' && !$('#view-prev-file').disabled) { e.preventDefault(); $('#view-prev-file').click(); }
  if (e.key === 'ArrowRight' && !$('#view-next-file').disabled) { e.preventDefault(); $('#view-next-file').click(); }
});

async function viewFile(p, side) {
  const j = jobs.find(z => z.id === selected);
  if (!j) return;
  const dlg = $('#dlg-view'), body = $('#view-body');
  const name = p.split('/').pop();
  const where = side === 'left' ? 'gauche' : 'droite';
  setViewNav({ p, side, cmp: false });
  $('#view-title').textContent = name;
  $('#view-meta').textContent = `${p} · côté ${where} · lecture…`;
  body.replaceChildren(h('div', { class: 'muted view-wait' }, 'Lecture du fichier…'));
  const other = side === 'left' ? 'right' : 'left';
  const a = allRows().find(z => z.path === p);
  $('#view-other').hidden = !(a && a[other] && a[other].exists);
  $('#view-other').textContent = `Voir le côté ${other === 'left' ? 'gauche' : 'droit'}`;
  $('#view-other').onclick = () => viewFile(p, other);
  $('#view-both').hidden = $('#view-other').hidden;
  $('#view-both').onclick = () => compareFiles(p);
  $('#view-left').hidden = $('#view-right').hidden = true;
  $('#view-nav').hidden = true;
  $('#dlg-view').classList.remove('wide');
  const local = ['local', 'usb'].includes((side === 'left' ? j.left : j.right).type);
  $('#view-open').hidden = !local;
  $('#view-open').onclick = () => openItem(p, side, false);
  if (!dlg.open) dlg.showModal();
  if (viewUrl) { URL.revokeObjectURL(viewUrl); viewUrl = null; }
  let res;
  try {
    res = await fetch(`/api/jobs/${selected}/file?side=${side}&path=${encodeURIComponent(p)}`, { headers: { 'X-Synkro-Token': TOKEN } });
    if (!res.ok) { const e = await res.json().catch(() => ({})); throw new Error(e.error || res.statusText); }
  } catch (e) {
    $('#view-meta').textContent = `${p} · côté ${where}`;
    body.replaceChildren(h('div', { class: 'errmsg' }, 'Lecture impossible : ' + e.message));
    return;
  }
  const buf = new Uint8Array(await res.arrayBuffer());
  const size = +res.headers.get('X-Size') || buf.length;
  const cut = buf.length < size;
  const mod = res.headers.get('X-Modified');
  $('#view-meta').textContent = `${p} · côté ${where} · ${bytes(size)}` + (mod ? ' · modifié le ' + fmtCell(mod) : '') +
    (cut ? ` · début seulement (${bytes(buf.length)} affichés)` : '');
  const ext = (name.includes('.') ? name.split('.').pop() : '').toLowerCase();
  if (IMG_TYPES[ext] && !cut) {
    viewUrl = URL.createObjectURL(new Blob([buf], { type: IMG_TYPES[ext] }));
    body.replaceChildren(h('div', { class: 'view-img' }, h('img', { src: viewUrl, alt: name })));
    return;
  }
  const probe = buf.subarray(0, 8000);
  const utf16 = (buf[0] === 0xFF && buf[1] === 0xFE) || (buf[0] === 0xFE && buf[1] === 0xFF);
  const binary = !utf16 && probe.some(x => x === 0);
  if (!buf.length) { body.replaceChildren(h('div', { class: 'muted' }, 'Fichier vide.')); return; }
  if (binary) {
    const max = 64 * 1024;
    body.replaceChildren(h('div', { class: 'muted view-note' }, 'Fichier binaire : affichage en hexadécimal' + (buf.length > max ? ` (${bytes(max)} premiers octets)` : '') + '.'),
      h('pre', { class: 'view-text hex' }, hexDump(buf, max)));
    return;
  }
  body.replaceChildren(h('pre', { class: 'view-text' }, decodeText(buf)));
}
$('#view-close').onclick = () => $('#dlg-view').close();

// ---- Comparer les deux côtés : affichage côte à côte, différences surlignées ----
async function fetchSide(p, side) {
  try {
    const res = await fetch(`/api/jobs/${selected}/file?side=${side}&path=${encodeURIComponent(p)}`, { headers: { 'X-Synkro-Token': TOKEN } });
    if (!res.ok) { const e = await res.json().catch(() => ({})); throw new Error(e.error || res.statusText); }
    const buf = new Uint8Array(await res.arrayBuffer());
    const size = +res.headers.get('X-Size') || buf.length;
    return { buf, size, cut: buf.length < size, mod: res.headers.get('X-Modified') };
  } catch (e) { return { err: e.message }; }
}
// différences entre deux listes de lignes (algorithme de Myers, O((N+M)·D)) :
// renvoie les lignes alignées [gauche, droite, type] avec type '=', '-', '+'
function diffLines(A, B) {
  const N = A.length, M = B.length, MAX = N + M;
  if (MAX > 400000) return null;
  const V = new Int32Array(2 * MAX + 2), trace = [];
  let D = 0, found = false;
  for (; D <= MAX && !found; D++) {
    if (D > 3000) return null; // trop différents : comparaison ligne à ligne
    trace.push(V.slice());
    for (let k = -D; k <= D; k += 2) {
      let x = (k === -D || (k !== D && V[k - 1 + MAX] < V[k + 1 + MAX])) ? V[k + 1 + MAX] : V[k - 1 + MAX] + 1;
      let y = x - k;
      while (x < N && y < M && A[x] === B[y]) { x++; y++; }
      V[k + MAX] = x;
      if (x >= N && y >= M) { found = true; break; }
    }
  }
  // remontée du chemin
  const out = [];
  let x = N, y = M;
  for (let d = trace.length - 1; d >= 0; d--) {
    const v = trace[d], k = x - y;
    const prevK = (k === -d || (k !== d && v[k - 1 + MAX] < v[k + 1 + MAX])) ? k + 1 : k - 1;
    const px = v[prevK + MAX], py = px - prevK;
    while (x > px && y > py) { out.push([x - 1, y - 1, '=']); x--; y--; }
    if (d > 0) { if (x === px) out.push([-1, y - 1, '+']); else out.push([x - 1, -1, '-']); }
    x = px; y = py;
  }
  return out.reverse();
}
async function compareFiles(p) {
  const dlg = $('#dlg-view'), body = $('#view-body');
  const name = p.split('/').pop();
  $('#view-title').textContent = name + ' — gauche ⇆ droite';
  setViewNav({ p, cmp: true });
  $('#view-meta').textContent = p + ' · lecture des deux côtés…';
  $('#view-other').hidden = $('#view-both').hidden = $('#view-open').hidden = true;
  $('#view-left').hidden = $('#view-right').hidden = false;
  $('#view-left').onclick = () => viewFile(p, 'left');
  $('#view-right').onclick = () => viewFile(p, 'right');
  $('#view-nav').hidden = true;
  dlg.classList.add('wide');
  body.replaceChildren(h('div', { class: 'muted view-wait' }, 'Lecture des deux fichiers…'));
  if (!dlg.open) dlg.showModal();
  if (viewUrl) { URL.revokeObjectURL(viewUrl); viewUrl = null; }
  const [L, R] = await Promise.all([fetchSide(p, 'left'), fetchSide(p, 'right')]);
  const info = (x, w) => x.err ? `${w} : illisible` : `${w} : ${bytes(x.size)}` + (x.mod ? ', ' + fmtCell(x.mod) : '') + (x.cut ? ' (début seulement)' : '');
  $('#view-meta').textContent = p + ' · ' + info(L, 'gauche') + ' · ' + info(R, 'droite');
  if (L.err || R.err) {
    body.replaceChildren(h('div', { class: 'errmsg' }, 'Lecture impossible : ' + (L.err ? 'gauche — ' + L.err + ' ' : '') + (R.err ? 'droite — ' + R.err : '')));
    return;
  }
  const same = L.size === R.size && L.buf.length === R.buf.length && L.buf.every((b, i) => b === R.buf[i]);
  const ext = (name.includes('.') ? name.split('.').pop() : '').toLowerCase();
  const banner = h('div', { class: 'view-note ' + (same ? 'same' : 'diff') }, same
    ? (L.cut ? 'Les débuts des deux fichiers sont identiques.' : '✓ Contenus identiques.')
    : '≠ Contenus différents.');
  // images : l'une à côté de l'autre
  if (IMG_TYPES[ext] && !L.cut && !R.cut) {
    const u1 = URL.createObjectURL(new Blob([L.buf], { type: IMG_TYPES[ext] }));
    const u2 = URL.createObjectURL(new Blob([R.buf], { type: IMG_TYPES[ext] }));
    dlg.addEventListener('close', () => { URL.revokeObjectURL(u1); URL.revokeObjectURL(u2); }, { once: true });
    body.replaceChildren(banner, h('div', { class: 'cmp-imgs' },
      h('figure', {}, h('figcaption', {}, 'Gauche'), h('div', { class: 'view-img' }, h('img', { src: u1, alt: 'gauche' }))),
      h('figure', {}, h('figcaption', {}, 'Droite'), h('div', { class: 'view-img' }, h('img', { src: u2, alt: 'droite' })))));
    return;
  }
  const isBin = b => !((b[0] === 0xFF && b[1] === 0xFE) || (b[0] === 0xFE && b[1] === 0xFF)) && b.subarray(0, 8000).some(x => x === 0);
  const binary = isBin(L.buf) || isBin(R.buf);
  const MAXB = 256 * 1024;
  const toLines = b => binary ? hexDump(b, MAXB).split('\n') : decodeText(b).replace(/\r\n?/g, '\n').split('\n');
  const A = toLines(L.buf), B = toLines(R.buf);
  let rows = binary ? null : diffLines(A, B);
  if (!rows) { // binaire, ou trop de différences : ligne à ligne
    rows = [];
    for (let i = 0; i < Math.max(A.length, B.length); i++) rows.push([i < A.length ? i : -1, i < B.length ? i : -1, A[i] === B[i] ? '=' : i >= A.length ? '+' : i >= B.length ? '-' : '~']);
  }
  // un « supprimé » suivi d'un « ajouté » = ligne modifiée, mise en face
  const merged = [];
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i];
    if (r[2] === '-') {
      let j = i; const dels = [];
      while (j < rows.length && rows[j][2] === '-') dels.push(rows[j++]);
      const adds = [];
      while (j < rows.length && rows[j][2] === '+') adds.push(rows[j++]);
      for (let k = 0; k < Math.max(dels.length, adds.length); k++)
        merged.push([dels[k] ? dels[k][0] : -1, adds[k] ? adds[k][1] : -1, dels[k] && adds[k] ? '~' : dels[k] ? '-' : '+']);
      i = j - 1;
    } else merged.push(r);
  }
  const LIMIT = 20000;
  const shown = merged.slice(0, LIMIT);
  const nDiff = merged.filter(r => r[2] !== '=').length;
  const cell = (n, txt, cls) => [h('td', { class: 'ln' }, n >= 0 ? String(n + 1) : ''), h('td', { class: 'tx ' + cls }, txt ?? '')];
  const trs = shown.map(([i, j, t]) => h('tr', { class: 'c' + ({ '=': 'eq', '-': 'del', '+': 'add', '~': 'chg' })[t] },
    ...cell(i, i >= 0 ? A[i] : '', 'l'), ...cell(j, j >= 0 ? B[j] : '', 'r')));
  const table = h('table', { class: 'cmp' + (binary ? ' hex' : '') },
    h('colgroup', {}, h('col', { class: 'cln' }), h('col', {}), h('col', { class: 'cln' }), h('col', {})),
    h('thead', {}, h('tr', {}, h('th', { colspan: '2' }, 'Gauche'), h('th', { colspan: '2' }, 'Droite'))),
    h('tbody', {}, ...trs));
  banner.textContent = same ? banner.textContent : `≠ ${nDiff} ligne(s) différente(s)` + (binary ? ' (affichage hexadécimal)' : '') +
    (merged.length > LIMIT ? ` · ${LIMIT} premières lignes affichées` : '');
  body.replaceChildren(banner, table);
  // aller d'une différence à l'autre (blocs de lignes consécutives)
  const starts = [];
  trs.forEach((tr, k) => { if (!tr.classList.contains('ceq') && (k === 0 || trs[k - 1].classList.contains('ceq'))) starts.push(tr); });
  let cur = -1;
  const go = d => {
    if (!starts.length) return;
    cur = (cur + d + starts.length) % starts.length;
    trs.forEach(t => t.classList.remove('focus'));
    starts[cur].classList.add('focus');
    body.scrollTop = starts[cur].offsetTop - body.clientHeight / 3;
  };
  $('#view-nav').hidden = !starts.length;
  $('#view-prev').onclick = () => go(-1);
  $('#view-next').onclick = () => go(1);
  if (starts.length) go(1);
}
$('#dlg-view').addEventListener('close', () => { if (viewUrl) { URL.revokeObjectURL(viewUrl); viewUrl = null; } $('#view-body').replaceChildren(); });

async function deleteItem(p, isDir, side) {
  const j = jobs.find(z => z.id === selected);
  const where = { left: 'à gauche', right: 'à droite', both: 'des deux côtés' }[side];
  const trash = j && j.options.useTrash;
  const msg = `Supprimer ${isDir ? 'le dossier' : 'le fichier'} « ${p} » ${where}${isDir ? ' avec tout son contenu' : ''} ?\n\n` +
    (trash ? 'Il sera déplacé dans la corbeille .synkro-trash (récupérable).' : 'La corbeille est désactivée pour ce job : la suppression est DÉFINITIVE.');
  if (!await askConfirm(msg, { title: isDir ? 'Supprimer le dossier' : 'Supprimer le fichier', ok: 'Supprimer', danger: true })) return;
  try {
    const r = await api('POST', `/api/jobs/${selected}/delete-item`, { path: p, side });
    toast(r.message);
    act('analyze'); // la liste est recalculée
  } catch (e) { alertBox(e.message); }
}
async function addFilter(kind, pattern) {
  try {
    await api('POST', `/api/jobs/${selected}/filter`, { kind, pattern });
    toast(`${kind === 'exclude' ? 'Exclusion' : 'Inclusion'} « ${pattern} » ajoutée aux options avancées du job.`);
    await refresh();
    act('analyze');
  } catch (e) { alertBox(e.message); }
}
function toast(msg) {
  let t = $('#toast');
  if (!t) { t = h('div', { id: 'toast', class: 'toast' }); document.body.append(t); }
  t.textContent = msg; t.hidden = false;
  clearTimeout(toast.timer); toast.timer = setTimeout(() => { t.hidden = true; }, 4000);
}
$('#actions-body').addEventListener('contextmenu', e => {
  const tr = e.target.closest('tr[data-path]');
  if (!tr || tr.dataset.path === '') return;
  e.preventDefault();
  showItemMenu(tr.dataset.path, tr.dataset.dir === '1', e.clientX, e.clientY);
});
function hideMenu() { $('#ctx-menu').hidden = true; }
addEventListener('click', e => { if (!$('#ctx-menu').contains(e.target)) hideMenu(); });
addEventListener('keydown', e => { if (e.key === 'Escape') hideMenu(); });
addEventListener('blur', hideMenu);
// un défilement ferme le menu, sauf juste après son ouverture
addEventListener('scroll', () => { if (Date.now() - (placeMenu.at || 0) > 400) hideMenu(); }, true);

async function deleteJob(j) {
  const gs = j.groups || [];
  const grp = gs.length ? `\nElle disparaît aussi ${gs.length > 1 ? 'des groupes' : 'du groupe'} ${gs.map(g => '« ' + g + ' »').join(', ')}.` : '';
  if (!await askConfirm(`Supprimer la tâche « ${j.name} » ?${grp}\n(Vos fichiers ne sont pas touchés.)`, { title: 'Supprimer la tâche', ok: 'Supprimer', danger: true })) return;
  try { await api('DELETE', `/api/jobs/${j.id}`); if (selected === j.id) selected = null; refresh(); } catch (e) { alertBox(e.message); }
}

function select(id) {
  groupView = null;
  if (id !== selected) { $('#filter').value = ''; folded = new Set(); } // autre tâche : filtres effacés
  selected = id; localStorage.setItem('synkro-selected', id);
  details = { plan: [], marks: { marks: '', errors: {} } }; lastState = {}; expandedFor = null; sameRows = null; cardFilter = null;
  renderList(); renderJob(true);
}

// ---------- Vue d'un groupe : ses tâches en liste, avec un résumé ----------
let groupView = null;
let gvFilter = null; // pastille du résumé choisie pour filtrer la liste (null : tout) // clé du groupe affiché à droite (ALL = Tout), sinon null
function openGroup(key) { if (groupView !== key) gvFilter = null; groupView = key; renderList(); renderJob(true); }
function groupJobs(key) { return key === ALL ? jobs : jobs.filter(j => inGroup(j, key)); }
function epShort(e) {
  if (!e) return '';
  if (e.type === 'sftp' && e.sftp) return `sftp://${e.sftp.host}${e.sftp.port && e.sftp.port !== 22 ? ':' + e.sftp.port : ''}${e.sftp.path || '/'}`;
  if (e.type === 'usb') return `🔌 ${e.volume && e.volume.label ? e.volume.label : 'clé'} ${e.path || ''}`;
  return e.path;
}
async function jobAct(j, what) {
  if (what === 'cancel') return askCancel(j);
  if (what === 'sync') startSolo(j.id);
  try { await api('POST', `/api/jobs/${j.id}/${what}`); } catch (e) { toast(e.message); }
  setTimeout(refresh, 150);
}
// avancement d'une synchro : moitié actions, moitié octets (voir renderJob)
function syncPct(p) {
  const pa = (p.doneActions || 0) / Math.max(1, p.totalActions || 1);
  return Math.min(100, (p.totalBytes ? (pa + (p.doneBytes || 0) / p.totalBytes) / 2 : pa) * 100);
}
function renderGroup() {
  const key = groupView;
  if (key !== ALL && !groups.includes(key)) { groupView = null; return false; }
  const list = groupJobs(key);
  $('#empty').hidden = true; $('#job').hidden = true; $('#group-view').hidden = false;
  $('#gv-name').replaceChildren((key === ALL ? '🗂 Tout' : '📁 ' + key));
  const isNever = j => !j.lastRun || j.lastRun.startsWith('0001');
  const GV_TEST = { ok: j => jobDot(j) === 'ok', err: j => jobDot(j) === 'err', cancel: j => jobDot(j) === 'cancel',
    run: j => ['run', 'wait'].includes(jobDot(j)), never: isNever };
  const cnt = k => list.filter(GV_TEST[k]).length;
  if (gvFilter && !cnt(gvFilter)) gvFilter = null; // plus rien dans ce filtre : on montre tout
  const shown = gvFilter ? list.filter(GV_TEST[gvFilter]) : list;
  const np = nextPlan('group', key === ALL ? '*' : key);
  $('#gv-meta').textContent = (gvFilter ? `${shown.length} affichée(s) sur ${list.length} tâche(s)` : `${list.length} tâche(s)`) +
    (np ? ` · prochain lancement planifié : ${fmtPlan(np)}` : '');
  const card = (k, label) => {
    const n = cnt(k);
    return h('button', { type: 'button', class: 'gv-card ' + k + (n ? '' : ' zero') + (gvFilter === k ? ' on' : ''), disabled: n ? undefined : '',
      title: gvFilter === k ? 'Clic : afficher toutes les tâches' : 'Clic : n\'afficher que ces tâches',
      onclick: () => { gvFilter = gvFilter === k ? null : k; renderGroup(); } }, h('b', {}, String(n)), ' ', label);
  };
  $('#gv-cards').replaceChildren(card('ok', 'OK'), card('err', 'en erreur'), card('cancel', 'annulée(s)'),
    card('run', 'en cours / en attente'), card('never', 'jamais lancée(s)'));
  const free = shown.filter(j => !isBusy(j));
  $('#gv-analyze').textContent = gvFilter ? 'Analyser les tâches affichées' : 'Tout analyser';
  $('#gv-sync').textContent = gvFilter ? 'Synchroniser les tâches affichées' : 'Tout synchroniser à la suite';
  $('#gv-analyze').disabled = !free.length; $('#gv-sync').disabled = !free.length;
  $('#gv-sync').onclick = async () => {
    try { await api('POST', '/api/queue', { ids: free.map(j => j.id) }); seriesOn = true; followOff = null; startSeries(free.map(j => j.id), key === ALL ? 'Tout' : 'groupe « ' + key + ' »'); } catch (e) { alertBox(e.message); }
    setTimeout(refresh, 150);
  };
  $('#gv-analyze').onclick = () => free.forEach(j => jobAct(j, 'analyze'));
  $('#gv-body').replaceChildren(...shown.map(j => {
    const st = j.status, busy = isBusy(j), dot = jobDot(j);
    const p = st.progress || {};
    const live = st.state === 'syncing' && p.totalActions ? ` · ${Math.floor(syncPct(p))} %` : '';
    const result = busy ? (st.message || st.state) + live : ((j.lastResult === 'Erreur : Annulé' ? 'Annulé' : j.lastResult) || '—');
    const btn = (txt, what, cls) => h('button', { type: 'button', class: 'small-btn ' + (cls || 'ghost'), onclick: e => { e.stopPropagation(); jobAct(j, what); } }, txt);
    return h('tr', { class: 'gv-row' + (busy ? ' busy' : ''), title: 'Clic : ouvrir la tâche · double-clic : la modifier',
      onclick: () => select(j.id), ondblclick: e => { e.stopPropagation(); if (!busy) openForm(j); } },
      h('td', {}, h('span', { class: 'dot head-dot ' + dot }), h('b', {}, j.name)),
      h('td', { class: 'muted' }, MODES[j.options.mode][1]),
      h('td', { class: 'gv-path', title: epShort(j.left) + '  →  ' + epShort(j.right) }, epShort(j.left), h('span', { class: 'muted' }, '  →  '), epShort(j.right)),
      h('td', { class: 'num' }, j.lastRun ? fmtDate(j.lastRun) : 'jamais'),
      h('td', { class: 'gv-res ' + (busy ? 'run' : dot), title: result }, result),
      h('td', { class: 'gv-act' }, busy
        ? btn(st.state === 'queued' ? 'Retirer de la file' : 'Annuler', 'cancel', 'danger')
        : [btn('Analyser', 'analyze'), btn('Synchroniser', 'sync', 'primary')]));
  }));
  return true;
}
async function renderJob(force) {
  if (groupView !== null && renderGroup()) return;
  $('#group-view').hidden = true;
  const j = jobs.find(x => x.id === selected);
  $('#empty').hidden = !!j; $('#job').hidden = !j;
  if (!j) return;
  let fresh = false;
  const st = j.status;
  const hd = jobDot(j);
  $('#job-name').replaceChildren(h('span', { class: 'dot head-dot ' + hd, title: j.lastResult ? 'Dernier passage : ' + j.lastResult : '' }), j.name,
    st.state === 'syncing' && st.paused ? h('span', { class: 'live-badge wait' }, '⏸ En pause') :
    ['syncing', 'analyzing'].includes(st.state) ? h('span', { class: 'live-badge' }, st.state === 'syncing' ? 'Synchronisation en cours' : 'Analyse en cours') :
    ['queued', 'waiting'].includes(st.state) ? h('span', { class: 'live-badge wait' }, st.state === 'queued' ? 'En file d\'attente' : 'En attente') : '');
  renderSides(j);
  $('#job-meta').title = $('#job-meta').textContent = `${MODES[j.options.mode][1]} · dernier passage : ${fmtDate(j.lastRun)}${j.lastResult ? ' (' + j.lastResult + ')' : ''}` +
    autoLabel(j);

  const busy = isBusy(j);
  // fichiers identiques mais dates décalées de 1 ou 2 s : proposer de corriger
  const nfix = !busy && j.options.mode !== 'two-way' && datesDismissed[j.id] !== String(st.startedAt) && st.summary && st.summary.datesToFix || 0;
  $('#dates-bar').hidden = !nfix;
  if (nfix) $('#dates-text').textContent = `⏱ ${nfix.toLocaleString('fr-FR')} fichier(s) identique(s) ont une date décalée de 1 ou 2 s par rapport à la gauche.`;
  $('#btn-analyze').disabled = busy; $('#btn-sync').disabled = busy;
  $('#btn-cancel').hidden = !busy;
  $('#btn-pause').hidden = st.state !== 'syncing';
  // vitesse de la synchro en cours (le menu n'est pas touché pendant qu'on s'en sert)
  const sp = $('#sel-speed');
  sp.hidden = st.state !== 'syncing' || st.speedKBps == null;
  if (!sp.hidden && document.activeElement !== sp) setSpeedChoice(st.speedKBps);
  $('#btn-pause').textContent = st.paused ? '▶ Reprendre' : '⏸ Pause';
  $('#btn-pause').classList.toggle('primary', !!st.paused);
  $('#btn-cancel').textContent = st.state === 'queued' ? 'Retirer de la file' : 'Annuler';
  $('#status-box').classList.toggle('conflicts', ['conflicts', 'waiting', 'queued'].includes(st.state));

  const box = $('#status-box');
  box.classList.toggle('err', st.state === 'error');
  box.classList.toggle('conflicts', ['conflicts', 'waiting', 'queued'].includes(st.state));
  $('#btn-sync').textContent = st.state === 'conflicts' ? 'Résoudre les conflits…' : 'Synchroniser';
  // ouvre la fenêtre des conflits une seule fois par passage
  if (st.state === 'conflicts' && cfShownFor !== j.id + '|' + st.startedAt && !$('#dlg-conflicts').open) {
    cfShownFor = j.id + '|' + st.startedAt;
    openConflicts(j);
  }
  let msg = st.message || (st.state === 'idle' ? 'Prêt.' : '');
  // erreur « clé non branchée » alors que la clé est revenue : le dire clairement
  const allPlugged = j.drives && Object.values(j.drives).every(d => d.plugged);
  if (st.state === 'error' && /n'est pas branchée/.test(msg) && allPlugged) {
    msg = '✓ La clé est de nouveau branchée : vous pouvez relancer l\'analyse ou la synchronisation.';
    box.classList.remove('err');
  }
  // fin d'un passage : heure affichée et petit éclair, pour voir qu'il s'est bien
  // passé quelque chose même quand le résultat ne change pas
  if (['done', 'error'].includes(st.state) && st.startedAt) {
    msg += ' · ' + new Date(st.startedAt).toLocaleTimeString('fr-FR');
    const key = j.id + st.startedAt;
    if (lastFlash !== key) {
      if (lastFlash !== null) { box.classList.remove('flash'); void box.offsetWidth; box.classList.add('flash'); }
      lastFlash = key;
    }
  }
  $('#status-msg').textContent = msg;
  const p = st.progress || {};
  let pct = 0;
  // correction des dates en cours : la barre du bas suit les dates, pas la copie déjà finie
  const fx = st.fix && ['syncing', 'analyzing'].includes(st.state) && st.fix.total ? st.fix : null;
  if (fx) {
    pct = fx.done * 100 / fx.total;
  } else if (st.state === 'syncing' && p.totalActions) {
    // moitié actions, moitié octets : les suppressions et créations de dossiers (sans
    // octets, souvent faites en premier) font avancer la barre, les gros fichiers aussi
    const pa = p.doneActions / p.totalActions;
    pct = (p.totalBytes ? (pa + p.doneBytes / p.totalBytes) / 2 : pa) * 100;
  } else if (st.state === 'done' && st.summary && st.summary.synced) pct = 100;
  $('#bar-fill').style.width = Math.min(100, pct) + '%';
  renderScan(st);
  if (detailPath !== null) renderDetail();
  let speed = '';
  const syncing = !fx && st.state === 'syncing' && p.totalActions;
  if (fx) {
    const el = (fx.elapsedMs || 0) / 1000;
    const dps = el > 2 ? fx.done / el : 0;
    let remain = fx.done >= fx.total ? '0 s' : dps > 0 ? '≈ ' + duration(Math.round((fx.total - fx.done) / dps)) : 'calcul…';
    if (st.paused) remain = 'en pause';
    $('#status-msg').textContent = msg + ' · ' + Math.floor(Math.min(100, pct)) + ' %';
    $('#status-sub').textContent = `${nf(fx.done)} / ${nf(fx.total)} date(s) corrigée(s) à droite · écoulé ${duration(el)} · reste ${remain}` +
      (dps > 0 ? ` · ${dps >= 10 ? nf(Math.round(dps)) : dps.toFixed(1).replace('.', ',')} date(s)/s` : '') +
      ' · aucun fichier copié ni supprimé';
  } else if (syncing) {
    // débits sur les ~20 dernières secondes : fichiers/s et octets/s
    const now = Date.now();
    if (!rate.samples.length || rate.job !== j.id || rate.start !== st.startedAt) rate = { job: j.id, start: st.startedAt, samples: [], el: 0, elAt: now };
    rate.samples.push([now, p.doneBytes, p.doneActions]);
    rate.samples = rate.samples.filter(x => now - x[0] < 20000);
    // temps écoulé (pauses exclues) : donné par le moteur, complété entre deux mesures
    if ((p.elapsedMs || 0) !== rate.el) { rate.el = p.elapsedMs || 0; rate.elAt = now; }
    const elapsed = (rate.el + (st.paused ? 0 : now - rate.elAt)) / 1000;
    const [t0, b0, a0] = rate.samples[0];
    const secs = (now - t0) / 1000;
    const avgBps = elapsed > 1 ? p.doneBytes / elapsed : 0;
    let bps = 0, fps = 0;
    if (secs > 2.5) { bps = (p.doneBytes - b0) / secs; fps = (p.doneActions - a0) / secs; }
    const left = Math.max(0, p.totalBytes - p.doneBytes);
    const leftActs = Math.max(0, p.totalActions - p.doneActions);
    let remain;
    if (st.paused) remain = 'en pause';
    else if (!left && !leftActs) remain = '0 s';
    else {
      // le plus long des deux : octets restants au débit, actions restantes à la cadence
      const r = bps > 0 ? bps : avgBps; // débit récent, sinon moyen
      const fr = fps > 0 ? fps : elapsed > 3 ? p.doneActions / elapsed : 0;
      const tb = left && r > 0 ? left / r : 0, ta = leftActs && fr > 0 ? leftActs / fr : 0;
      remain = (tb || ta) && elapsed > 3 ? '≈ ' + duration(Math.max(tb, ta)) : 'calcul…';
    }
    speed = ` · écoulé ${duration(elapsed)} · reste ${remain}`;
    if (secs > 2.5) speed += ` · ${bytes(Math.round(bps))}/s`;
    if (avgBps > 0) speed += ` (moy. ${bytes(Math.round(avgBps))}/s)`;
    if (secs > 2.5) speed += ` · ${fps >= 10 ? Math.round(fps) : fps.toFixed(1).replace('.', ',')} fichier(s)/s`;
  }
  // pourcentage d'avancement à côté du nombre d'actions
  // le message de départ (« N action(s) à effectuer ») ne bouge pas : on montre l'avancement
  if (syncing) $('#status-msg').textContent = (/action\(s\) à effectuer$/.test(msg) ? `Synchronisation en cours : ${nf(p.doneActions)} / ${nf(p.totalActions)} action(s)` : msg) +
    ' · ' + Math.floor(Math.min(100, pct)) + ' %';
  if (!fx) $('#status-sub').textContent = syncing
    ? `${p.doneActions}/${p.totalActions} actions · ${bytes(p.doneBytes)} / ${bytes(p.totalBytes)}${speed}${p.note ? ' · ⚠ ' + p.note : ''}${p.current ? ' · ' + p.current : ''}`
    : '';

  renderCards(effSummary(st.summary));

  // Recharger le détail quand l'état change (fin d'analyse ou de synchro).
  const key = st.state + '|' + st.startedAt + '|' + (st.summary ? JSON.stringify(st.summary) : '');
  if (force || lastState[j.id] !== key) {
    lastState[j.id] = key;
    if (st.state === 'done' || st.state === 'error' || busy) {
      const before = planKey();
      details = await api('GET', `/api/jobs/${j.id}/details`).catch(() => ({ plan: [], marks: { marks: '', errors: {} } }));
      details.plan = details.plan || [];
      // option de la tâche : liste effacée après une synchro réussie, ou une analyse sans changement
      const sm = st.summary;
      if (j.autoClear && st.state === 'done' && sm && !sm.errors && !sm.conflicts && (sm.synced || !details.plan.length)) {
        details = { plan: [], marks: { marks: '', errors: {} }, cleared: true };
      }
      sameRows = null;
      resetOverridesIfNewPlan();
      if (showSame) await loadSame();
      fresh = force || planKey() !== before; // nouveau résultat : on repart du haut
    }
    renderActions(fresh);
    if (tab === 'log') loadLog();
  } else if (st.state === 'syncing' && details.plan.length) {
    // pendant la synchro : seul l'état des actions est rechargé (léger). La liste
    // complète (totaux des dossiers) n'est recalculée que toutes les 3 s ; entre
    // deux, seules les lignes visibles sont mises à jour.
    if (syncTick) return; // le passage précédent n'est pas fini
    syncTick = true;
    try {
      details.marks = await api('GET', `/api/jobs/${j.id}/marks`).catch(() => details.marks);
      // rendu complet (totaux des dossiers…) : toutes les 3 s, moins souvent si la
      // liste est énorme ou la machine lente (jamais plus de 10 % du temps)
      if (Date.now() - lastFullRender > Math.max(3000, 10 * (lastRenderMs || 0))) { lastFullRender = Date.now(); renderActions(); }
      else { lastCurPath = liveCurPath = curRowPath(); renderWindow(true); }
      followCurrent();
    } finally { syncTick = false; }
  }
}

// Avancement de la lecture des deux côtés (analyse), affiché au milieu de la liste.
// Le pourcentage s'appuie sur le nombre d'éléments lus à l'analyse précédente ;
// à la première analyse, le côté droit est estimé d'après le côté gauche.
const nf = n => n.toLocaleString('fr-FR');
function renderFix(st) {
  const fx = st.fix && ['syncing', 'analyzing'].includes(st.state) ? st.fix : null;
  $('#fix-panel').hidden = !fx;
  if (!fx) return;
  const pct = fx.total ? Math.min(100, Math.floor(fx.done * 100 / fx.total)) : 0;
  $('#fix-pct').textContent = pct + ' %';
  $('#fix-panel .bar > div').style.width = pct + '%';
  let eta = '';
  if (fx.done > 0 && fx.elapsedMs > 2000 && fx.done < fx.total) eta = ' · reste ≈ ' + duration(Math.round((fx.total - fx.done) * fx.elapsedMs / fx.done / 1000));
  $('#fix-n').textContent = `${nf(fx.done)} / ${nf(fx.total)} date(s)` + eta;
}
function renderScan(st) {
  renderFix(st);
  const sc = st.scan, panel = $('#scan-panel');
  $('#tab-actions').classList.toggle('scanning', !!(sc || (st.fix && ['syncing', 'analyzing'].includes(st.state))));
  panel.hidden = !sc;
  if (!sc) return;
  const L = sc.left.dirs + sc.left.files, R = sc.right.dirs + sc.right.files;
  const onRight = sc.side === 'right';
  const estL = onRight ? L : (sc.prev[0] || 0);
  const known = sc.prev[0] > 0 || sc.prev[1] > 0; // analyse précédente connue
  const estR = known ? sc.prev[1] : estL;
  const frac = (n, t) => t ? Math.min(0.99, n / t) : null;
  const fl = onRight ? 1 : frac(L, estL), fr = onRight ? frac(R, estR) : 0;
  const total = (known || onRight) && estL + estR > 0 ? Math.min(99, Math.floor((L + R) / (estL + estR) * 100)) : null;
  $('#scan-pct').textContent = total === null ? '…' : String(total).padStart(2, '0') + ' %';
  const cur = onRight ? sc.right.path : sc.left.path;
  $('#scan-path').textContent = cur ? 'Lecture ' + (onRight ? 'à droite' : 'à gauche') + ' : ' + cur : 'Lecture du côté ' + (onRight ? 'droit' : 'gauche') + '…';
  for (const [side, c, f, state] of [['left', sc.left, fl, onRight ? 'terminé ✓' : 'en cours…'], ['right', sc.right, fr, onRight ? 'en cours…' : 'en attente']]) {
    const box = $(`.scan-side[data-side=${side}]`, panel);
    box.classList.toggle('wait', state === 'en attente');
    $('em', box).textContent = state;
    const bar = $('.bar', box);
    bar.classList.toggle('indet', f === null);
    $('.bar > div', box).style.width = (f || 0) * 100 + '%';
    $('.scan-n', box).textContent = `${nf(c.dirs)} dossier(s) · ${nf(c.files)} fichier(s)`;
  }
}

// Filtre choisi en cliquant sur une case du bilan (null = tout afficher)
let cardFilter = null;
let lastFlash = null; // dernier passage signalé par l'éclair de la barre d'état
// dossiers repliés à la main quand la liste est filtrée (sinon tout y est ouvert)
let folded = new Set();
const CARD_FILTERS = {
  right: a => ['copy→', 'mkdir→'].includes(effKind(a)),
  left: a => ['copy←', 'mkdir←'].includes(effKind(a)),
  del: a => ['delete→', 'delete←'].includes(effKind(a)),
  conflict: a => a.conflict && !overrides.has(a.path),
  error: (a, i) => markOf(i) === 'x' || a.kind === 'skip',
  canceled: (a, i) => markOf(i) === '-',
  same: a => a.kind === 'same',
  // identiques mais date décalée (à la seconde) entre gauche et droite
  datefix: a => a.kind === 'same' && !a.isDir && a.left && a.right && a.left.exists && a.right.exists &&
    Math.floor(Date.parse(a.left.modTime) / 1000) !== Math.floor(Date.parse(a.right.modTime) / 1000),
};
function renderCards(s) {
  const c = $('#cards'); c.replaceChildren();
  if (!s) return;
  // pastilles compactes : icône + nombre, le libellé est dans l'info-bulle
  const card = (ic, n, label, cls = '', key = null) => {
    const el = h('div', { class: 'card ' + cls + (key ? ' clickable' : '') + (key && cardFilter === key ? ' active' : '') + (n === 0 || n === '0 o' ? ' zero' : ''),
      title: label + (key ? (cardFilter === key ? ' — cliquer pour tout réafficher' : ' — cliquer pour n\'afficher que ces éléments') : '') },
      icon(ic, 20), h('b', {}, String(n)), h('span', { class: 'card-lbl' }, label));
    if (key) el.onclick = async () => {
      cardFilter = cardFilter === key ? null : key;
      if (cardFilter === 'same' && !sameRows) await loadSame();
      renderCards(s); renderActions(true);
    };
    return el;
  };
  const dels = s.delLeft + s.delRight;
  c.append(...[card('right', s.toRight, 'vers la droite', '', 'right'), card('left', s.toLeft, 'vers la gauche', '', 'left'),
    card('trash', dels, 'suppressions', dels ? 'warn' : '', 'del'),
    card('conflict', s.conflicts, 'conflits', s.conflicts ? 'warn' : '', 'conflict'),
    card('fail', s.errors, 'erreurs', s.errors ? 'bad' : '', 'error'),
    s.canceled ? card('stop', s.canceled, 'annulés (non faits)', 'cancel', 'canceled') : null,
    card('none', s.unchanged, 'inchangés', '', 'same'), card('size', bytes(s.bytes), 'à transférer')].filter(Boolean));
}

// ---------- Sens choisi à la main (clic sur la flèche) ----------
let overrides = new Map(); // chemin → 'right' | 'left' | 'none'
let ovVer = 0; // change à chaque modification des choix (cache de l'arbre)
let overridesKey = '';
function planKey() { const p = details.plan || []; return selected + '|' + p.length + '|' + (p[0] ? p[0].path : '') + '|' + (p.length ? p[p.length - 1].path : ''); }
function resetOverridesIfNewPlan() { const k = planKey(); if (k !== overridesKey) { overridesKey = k; overrides = new Map(); ovVer++; } }
const ex = (a, side) => !!(a && a[side] && a[side].exists);
const DIR_OF = { 'copy→': 'right', 'mkdir→': 'right', 'delete→': 'right', 'copy←': 'left', 'mkdir←': 'left', 'delete←': 'left', leave: 'none' };
// action réellement prévue pour un élément, compte tenu du choix manuel
function effKind(a) {
  const d = overrides.get(a.path);
  if (!d) return a.kind;
  if (d === 'none') return 'leave';
  if (d === 'right') return ex(a, 'left') ? (a.isDir ? (ex(a, 'right') ? 'leave' : 'mkdir→') : 'copy→') : 'delete→';
  return ex(a, 'right') ? (a.isDir ? (ex(a, 'left') ? 'leave' : 'mkdir←') : 'copy←') : 'delete←';
}
// on ne change le sens qu'après une analyse, avant la synchronisation
function canEdit() {
  const j = jobs.find(x => x.id === selected);
  if (!j || isBusy(j) || j.status.state === 'conflicts' || !(details.plan || []).length) return false;
  const m = (details.marks && details.marks.marks) || '';
  // mémorisé : appelé pour chaque ligne affichée, et la chaîne peut compter 150 000 caractères
  if (canEdit.m !== m) { canEdit.m = m; canEdit.v = !/[^.]/.test(m); }
  return canEdit.v;
}
const editable = a => a && a.kind !== 'same' && a.kind !== 'skip' && a.kind !== 'excluded' && a.left && a.right;
const NEXT_DIR = { right: 'left', left: 'none', none: 'right' };
function setDir(a, d) {
  overrides.set(a.path, d); ovVer++;
  if (effKind(a) === a.kind && !a.conflict) overrides.delete(a.path); // revenu au choix d'origine
}
function cycleDir(a) {
  if (!canEdit() || !editable(a)) return;
  setDir(a, NEXT_DIR[DIR_OF[effKind(a)] || 'none']);
  renderActions(); renderCards(effSummary((jobs.find(x => x.id === selected) || { status: {} }).status.summary));
}
// clic sur un dossier : tout son contenu prend le sens suivant
function cycleFolder(rows) {
  if (!canEdit()) return;
  rows = rows.filter(editable);
  if (!rows.length) return;
  const dirs = new Set(rows.map(a => DIR_OF[effKind(a)] || 'none'));
  const next = dirs.size === 1 ? NEXT_DIR[[...dirs][0]] : 'right';
  rows.forEach(a => setDir(a, next));
  renderActions(); renderCards(effSummary((jobs.find(x => x.id === selected) || { status: {} }).status.summary));
}
// bilan recalculé avec les choix manuels
function effSummary(s) {
  if (!s || !overrides.size || s.synced) return s;
  const r = Object.assign({}, s, { toRight: 0, toLeft: 0, delLeft: 0, delRight: 0, bytes: 0, conflicts: 0 });
  for (const a of details.plan || []) {
    const k = effKind(a);
    if (k === 'copy→' || k === 'mkdir→') r.toRight++;
    if (k === 'copy←' || k === 'mkdir←') r.toLeft++;
    if (k === 'delete←') r.delLeft++;
    if (k === 'delete→') r.delRight++;
    if (k === 'copy→') r.bytes += a.left.size;
    if (k === 'copy←') r.bytes += a.right.size;
    if (a.conflict && !overrides.has(a.path)) r.conflicts++;
  }
  return r;
}
// Icônes dessinées en SVG : pastille colorée + symbole blanc
const SVGNS = 'http://www.w3.org/2000/svg';
const ICON_PATHS = {
  right: ['M6 12h11', 'M12.5 7l5 5-5 5'],
  left: ['M18 12H7', 'M11.5 7l-5 5 5 5'],
  delright: ['M4.5 12h7', 'M8.5 8.5L12 12l-3.5 3.5', 'M15 9l5 6', 'M20 9l-5 6'],
  delleft: ['M19.5 12h-7', 'M15.5 8.5L12 12l3.5 3.5', 'M9 9l-5 6', 'M4 9l5 6'],
  none: ['M7 9.5h10', 'M7 14.5h10'],
  done: ['M6.5 12.5l3.5 3.5 7.5-8'],
  fail: ['M8 8l8 8', 'M16 8l-8 8'],
  stop: ['M9 9h6v6H9z'],
  skip: ['M12 6.5v7', 'M12 17.3v.2'],
  trash: ['M7.5 8.5h9', 'M10.3 8.5V7h3.4v1.5', 'M8.8 8.5l.6 8.5h5.2l.6-8.5'],
  conflict: ['M5.5 9.5h9', 'M11.5 6.5l3 3-3 3', 'M18.5 14.5h-9', 'M12.5 11.5l-3 3 3 3'],
  size: ['M12 6v8', 'M8.5 10.5L12 14l3.5-3.5', 'M7 17.5h10'],
};
// double = deux pastilles collées (pour les dossiers)
function icon(name, size = 22, double = false) {
  const svg = document.createElementNS(SVGNS, 'svg');
  const w = double ? 33 : 24, dx = double ? 9 : 0;
  svg.setAttribute('viewBox', `0 0 ${w} 24`); svg.setAttribute('width', size * w / 24); svg.setAttribute('height', size);
  svg.setAttribute('class', 'dico dico-' + name + (double ? ' dico-double' : ''));
  const circle = (cx, cls) => {
    const c = document.createElementNS(SVGNS, 'circle');
    c.setAttribute('cx', cx); c.setAttribute('cy', 12); c.setAttribute('r', 11.2); c.setAttribute('class', cls);
    svg.append(c);
  };
  if (double) { circle(11.5, 'dico-bg dico-back'); circle(11.5, 'dico-shade'); }
  circle(12 + dx, 'dico-bg' + (double ? ' dico-front' : ''));
  const g = document.createElementNS(SVGNS, 'g');
  if (dx) g.setAttribute('transform', `translate(${dx} 0)`);
  for (const d of ICON_PATHS[name] || []) {
    const p = document.createElementNS(SVGNS, 'path'); p.setAttribute('d', d); p.setAttribute('class', 'dico-fg'); g.append(p);
  }
  svg.append(g);
  return svg;
}
const DIR_VIEW = {
  'copy→': ['right', 'to-r', 'Copier vers la droite'], 'mkdir→': ['right', 'to-r', 'Créer le dossier à droite'],
  'delete→': ['delright', 'del', 'Supprimer à droite'],
  'copy←': ['left', 'to-l', 'Copier vers la gauche'], 'mkdir←': ['left', 'to-l', 'Créer le dossier à gauche'],
  'delete←': ['delleft', 'del', 'Supprimer à gauche'],
  leave: ['none', 'none', 'Ne rien faire'], same: ['none', 'same', 'Identique des deux côtés'],
  excluded: ['fail', 'same', 'Exclu de la synchronisation'],
  skip: ['skip', 'skip', 'Fichier d\'un côté, dossier de l\'autre : à régler à la main'],
};
function dirCell(a, i) {
  const m = i !== undefined ? markOf(i) : '.';
  if (m === 'o' && a.kind !== 'leave') return h('span', { class: 'dirbtn done', title: 'Fait : les deux côtés sont identiques' }, icon('done'));
  if (m === 'x') return h('span', { class: 'dirbtn fail', title: 'Échec : ' + (errOf(i) || '') }, icon('fail'));
  const k = effKind(a);
  const [ic, cls, label] = DIR_VIEW[k] || ['none', '', k];
  const sym = icon(ic);
  const manual = overrides.has(a.path);
  const can = canEdit() && editable(a);
  const b = h('button', { type: 'button', class: `dirbtn ${cls}${manual ? ' manual' : ''}${a.conflict && !manual ? ' conflict' : ''}`,
    title: label + (manual ? ' (choix manuel)' : '') + (can ? ' — cliquer pour changer : → droite, ← gauche, = rien' : '') }, sym);
  if (can) b.onclick = e => { e.stopPropagation(); cycleDir(a); };
  else b.disabled = true;
  return b;
}
// sens d'un dossier, à partir des compteurs calculés une seule fois (buildTree)
function folderDirCell(c, getRows) {
  const can = canEdit() && c.edit > 0;
  const b = h('button', { type: 'button', class: 'dirbtn folder' + (c.manual ? ' manual' : ''),
    title: can ? 'Cliquer pour changer le sens de tout le contenu du dossier' : '' },
    c.toL ? h('span', { class: 'cnt to-l', title: 'vers la gauche' }, icon('left', 21, true), String(c.toL)) : null,
    c.toR ? h('span', { class: 'cnt to-r', title: 'vers la droite' }, icon('right', 21, true), String(c.toR)) : null,
    c.del ? h('span', { class: 'cnt del', title: 'suppressions' }, icon('trash', 21, true), String(c.del)) : null,
    c.none ? h('span', { class: 'cnt none', title: 'rien à faire' }, icon('none', 21, true), String(c.none)) : null);
  if (can) b.onclick = e => { e.stopPropagation(); cycleFolder(getRows()); };
  else b.disabled = true;
  return b;
}
const FMT_CELL = new Intl.DateTimeFormat('fr-FR', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
const fmtCell = d => FMT_CELL.format(new Date(d));
// une fois l'action réussie, on montre l'état obtenu : les deux côtés pareils
function doneSide(a, side) {
  const x = a[side];
  switch (a.kind) {
    case 'copy→': case 'mkdir→': return side === 'right' ? a.left : x;
    case 'copy←': case 'mkdir←': return side === 'left' ? a.right : x;
    case 'delete→': return side === 'right' ? { exists: false, deleted: true } : x;
    case 'delete←': return side === 'left' ? { exists: false, deleted: true } : x;
  }
  return x;
}
function sideCells(a, side, i) {
  const cls = side === 'left' ? 'lcol' : 'rcol';
  const x = a && (i !== undefined && markOf(i) === 'o' ? doneSide(a, side) : a[side]);
  if (x && x.deleted) return [h('td', { class: cls + ' num absent' }, 'supprimé'), h('td', { class: cls + ' num absent' }, '')];
  if (!x) return [h('td', { class: cls + ' num' }), h('td', { class: cls + ' num' })];
  if (!x.exists) return [h('td', { class: cls + ' num absent' }, 'absent'), h('td', { class: cls + ' num absent' }, '')];
  // un dossier : « 0 o » dans la colonne taille et « dossier » dans la colonne date (comme les autres dossiers)
  const size = h('td', { class: cls + ' num' }, a.isDir ? h('b', {}, '0 o') : bytes(x.size));
  const date = h('td', { class: cls + ' num' + (a.isDir ? ' muted' : '') }, a.isDir ? 'dossier' : fmtCell(x.modTime));
  return side === 'left' ? [size, date] : [date, size];
}

// ---------- Actions : arborescence ou liste, avec l'état de chaque élément ----------
const MARKS = {
  o: ['ok', '✓ OK'], x: ['err', '✗ Échec'], '-': ['cancel', 'annulé'], '=': ['same', '= identique'], e: ['excl', '⊘ exclu'],
};
function markOf(i) {
  const n = (details.plan || []).length;
  if (i >= n) return sameRows && sameRows[i - n] && sameRows[i - n].kind === 'excluded' ? 'e' : '='; // identique ou exclu
  return (details.marks && details.marks.marks || '')[i] || '.';
}
// lignes affichées : les actions, plus les éléments identiques (et exclus) si demandé
let rowsMemo = { key: null, rows: [] };
function allRows() {
  const withSame = (showSame || cardFilter === 'same' || cardFilter === 'datefix') && sameRows;
  const plan = details.plan || [];
  if (rowsMemo.plan === plan && rowsMemo.same === (withSame ? sameRows : null)) return rowsMemo.rows;
  rowsMemo = { plan, same: withSame ? sameRows : null, rows: withSame ? plan.concat(sameRows) : plan };
  return rowsMemo.rows;
}
async function loadSame() {
  if (!selected) return;
  sameRows = await api('GET', `/api/jobs/${selected}/same`).catch(() => []);
}
// ---------- Détail de la ligne cliquée ----------
let detailPath = null;
// dossier réel d'un côté : local, clé USB (lecteur actuel) ou serveur SFTP
function sideBase(j, side) {
  const e = j[side], dv = j.drives && j.drives[side];
  if (e.type === 'sftp' && e.sftp) {
    const port = e.sftp.port && e.sftp.port !== 22 ? ':' + e.sftp.port : '';
    return `sftp://${e.sftp.host}${port}${e.sftp.path || '/'}`;
  }
  if (dv && dv.dir) return dv.dir;
  if (e.type === 'usb') return `clé « ${e.volume ? e.volume.label || e.volume.serial : '?'} » (non branchée)${e.path || ''}`;
  return e.path;
}
function fullPath(j, side, rel) {
  const base = sideBase(j, side);
  if (!rel) return base;
  const win = /^[A-Za-z]:\\|\\/.test(base) && !base.startsWith('sftp://');
  const sep = win ? '\\' : '/';
  return base.replace(/[\\/]+$/, '') + sep + (win ? rel.replace(/\//g, '\\') : rel);
}
function rowIndexOf(path) {
  const rows = allRows();
  if (rowIndexOf.rows !== rows) { rowIndexOf.rows = rows; rowIndexOf.map = new Map(rows.map((a, i) => [a.path, i])); }
  return rowIndexOf.map.get(path);
}
function pickRow(path) {
  detailPath = detailPath === path ? null : path;
  $$('#actions-body tr.picked').forEach(t => t.classList.remove('picked'));
  if (detailPath !== null) { const tr = $(`#actions-body tr[data-path="${CSS.escape(detailPath)}"]`); if (tr) tr.classList.add('picked'); }
  renderDetail();
}
function renderDetail() {
  const box = $('#item-detail');
  const j = jobs.find(x => x.id === selected);
  const i = detailPath != null ? rowIndexOf(detailPath) : undefined;
  if (!j || detailPath == null) { box.hidden = true; return; }
  // dossier : avancement de son contenu (sinon, son action propre)
  if ((i === undefined || allRows()[i].isDir) && renderFolderDetail(j, box)) return;
  if (i === undefined) { box.hidden = true; return; }
  const a = allRows()[i], kind = effKind(a);
  const toR = /→$/.test(kind), toL = /←$/.test(kind);
  const src = toL ? 'right' : 'left', dst = toL ? 'left' : 'right';
  const S = a[src] || {}, D = a[dst] || {};
  const name = a.path.split('/').pop();
  const label = (DIR_VIEW[kind] || DIR_VIEW[a.kind] || [, , a.kind])[2];
  const m = markOf(i), act = m === '.' && activeFiles()[a.path];
  // avancement : en cours (octets copiés), fait, annulé, échec, en attente
  let pct = 0, cls = '', state;
  const size = a.isDir ? 0 : (S.size || a.size || 0);
  if (act) { pct = act[1] ? Math.min(100, Math.floor(act[0] * 100 / act[1])) : 0; state = `${pct} % · ${bytes(act[0])} / ${bytes(act[1])}`; }
  else if (m === 'o' || m === '=') { pct = 100; cls = 'ok'; state = m === '=' ? 'identique des deux côtés' : '100 % · terminé'; }
  else if (m === 'x') { cls = 'err'; state = 'échec'; }
  else if (m === '-') { cls = 'cancel'; state = 'annulé (non fait)'; }
  else if (m === 'e') { state = a.reason || 'exclu'; }
  else state = j.status.state === 'syncing' ? 'en attente' : 'à faire (lancez la synchronisation)';
  const del = /^delete/.test(kind);
  const where = side => (!a[side] || !a[side].exists) ? 'absent' : '';
  box.replaceChildren(
    h('div', { class: 'd-head' },
      h('span', {}, a.isDir ? '📁' : '📄'),
      h('span', { class: 'd-name', title: a.path }, name),
      h('span', { class: 'd-kind' }, '· ' + label),
      h('button', { class: 'd-close ghost', title: 'Fermer (Échap)', onclick: () => pickRow(detailPath) }, '×')),
    h('dl', {},
      // la date de modification suit le chemin, sur la même ligne
      h('dt', {}, del ? 'Élément' : 'Départ'), h('dd', {}, fullPath(j, del ? (kind === 'delete←' ? 'left' : 'right') : src, a.path), dateTag(S.modTime)),
      h('dt', {}, 'Taille'), h('dd', { class: 'n' }, a.isDir ? 'dossier' : bytes(size)),
      ...(del ? [] : [
        h('dt', {}, 'Destination'), h('dd', {}, fullPath(j, dst, a.path), dateTag(D.modTime)),
        h('dt', {}, 'Actuellement'), h('dd', { class: 'n' }, where(dst) || (D.size !== undefined && !a.isDir ? bytes(D.size) : '—'))])),
    h('div', { class: 'd-prog' },
      h('div', { class: 'd-bar ' + cls }, h('i', { style: `width:${pct}%` })),
      h('span', { class: 'd-pct' }, state)),
    errOf(i) ? h('div', { class: 'd-err' }, '✗ ' + errOf(i)) : '');
  box.hidden = false;
}
// date de modification affichée au bout d'un chemin (rien si inconnue)
function dateTag(t) { return t && !String(t).startsWith('0001') ? h('span', { class: 'd-date' }, fmtDate(t)) : null; }
// dossier sans action propre : chemins des deux côtés et avancement de son contenu
function renderFolderDetail(j, box) {
  const pre = detailPath ? detailPath + '/' : '', rows = allRows(), act = activeFiles();
  let n = 0, done = 0, total = 0, copied = 0, fail = 0, canc = 0;
  for (let i = 0; i < rows.length; i++) {
    const a = rows[i];
    if (!a.path.startsWith(pre) || a.isDir) continue;
    const m = markOf(i), k = effKind(a);
    if (m === '=' || m === 'e' || k === 'leave' || k === 'same') continue;
    const sz = (/←$/.test(k) ? a.right && a.right.size : a.left && a.left.size) || 0;
    n++; total += sz;
    if (m === 'o') { done++; copied += sz; } else if (m === 'x') fail++; else if (m === '-') canc++;
    else if (act[a.path]) copied += act[a.path][0];
  }
  if (!n) return false;
  const pct = total ? Math.floor(copied * 100 / total) : Math.floor(done * 100 / n);
  box.replaceChildren(
    h('div', { class: 'd-head' }, h('span', {}, detailPath ? '📁' : '🗂'),
      h('span', { class: 'd-name', title: detailPath }, detailPath ? detailPath.split('/').pop() : j.name),
      h('span', { class: 'd-kind' }, `· ${detailPath ? 'dossier' : 'tâche'} · ${n} fichier(s) à traiter`),
      h('button', { class: 'd-close ghost', title: 'Fermer (Échap)', onclick: () => pickRow(detailPath) }, '×')),
    h('dl', {},
      h('dt', {}, 'Gauche'), h('dd', {}, fullPath(j, 'left', detailPath)),
      h('dt', {}, 'Taille'), h('dd', { class: 'n' }, bytes(total)),
      h('dt', {}, 'Droite'), h('dd', {}, fullPath(j, 'right', detailPath)),
      h('dt', {}, 'Faits'), h('dd', { class: 'n' }, `${done} / ${n}` + (fail ? ` · ${fail} échec(s)` : '') + (canc ? ` · ${canc} annulé(s)` : ''))),
    h('div', { class: 'd-prog' }, h('div', { class: 'd-bar' + (done === n ? ' ok' : '') }, h('i', { style: `width:${pct}%` })),
      h('span', { class: 'd-pct' }, `${pct} % · ${bytes(copied)} / ${bytes(total)}`)));
  box.hidden = false;
  return true;
}
document.addEventListener('keydown', e => { if (e.key === 'Escape' && detailPath !== null && !document.querySelector('dialog[open]')) pickRow(detailPath); });

function errOf(i) { return details.marks && details.marks.errors ? details.marks.errors[i] : ''; }

// état affiché pour une action seule
// fichiers en cours de copie (fournis par le moteur pendant la synchro)
function activeFiles() {
  const j = jobs.find(x => x.id === selected);
  return (j && j.status.state === 'syncing' && j.status.progress && j.status.progress.active) || {};
}
function currentPath() {
  const j = jobs.find(x => x.id === selected);
  if (!j || j.status.state !== 'syncing') return '';
  const act = Object.keys(activeFiles());
  // on suit le fichier le plus gros en cours, sinon l'élément courant
  if (act.length) return act.sort((a, b) => activeFiles()[b][1] - activeFiles()[a][1])[0];
  return (j.status.progress && j.status.progress.current) || '';
}
function stateCell(i, a) {
  const m = markOf(i);
  if (a && a.kind === 'excluded') return h('span', { class: 'st excl', title: a.reason }, '⊘ ' + a.reason);
  const act = a && m === '.' && activeFiles()[a.path];
  if (act) {
    const pct = act[1] ? Math.min(100, Math.floor(act[0] * 100 / act[1])) : 0;
    return h('span', { class: 'st run filebar', title: `${bytes(act[0])} / ${bytes(act[1])}` },
      h('span', { class: 'minibar' }, h('i', { style: `width:${pct}%` })), `${pct} %`);
  }
  if (a && a.kind === 'leave' && m === 'o') return h('span', { class: 'st same' }, '= non modifié');
  // identiques, mais dates différentes de 1 ou 2 s : le dire (tolérance de comparaison)
  if (m === '=' && a && !a.isDir && a.left && a.right && a.left.exists && a.right.exists) {
    const d = Math.abs(Math.floor(Date.parse(a.left.modTime) / 1000) - Math.floor(Date.parse(a.right.modTime) / 1000));
    const jj = jobs.find(z => z.id === selected);
    const fix = jj && !jj.options.keepDates && jj.options.mode !== 'two-way';
    // disque de droite qui arrondit les dates à 2 s (FAT ; exFAT sous Windows, constaté
    // par Synkro en essayant de corriger) : l'écart ne peut pas disparaître
    const fsR = jj && jj.drives && jj.drives.right && jj.drives.right.fs || 'disque de droite';
    const coarse = d <= 2 && jj && (jj.rightCoarse || /^fat/i.test(fsR));
    if (d >= 1) return h('span', { class: 'st same', title: `Dates différentes de ${d} s : considérées comme identiques (Synkro tolère 2 s d'écart).` +
      (coarse ? ` Le disque de droite (${fsR}) n'enregistre les dates qu'à 2 s près : cet écart est normal et ne peut pas être corrigé.`
        : fix ? ' La date sera recopiée à droite à la prochaine synchronisation.'
        : ' Option « Corriger la date des fichiers identiques » dans les options avancées de la tâche.') },
      `= identique (±${d} s)` + (coarse ? ' · ' + fsR : fix ? ' · date à corriger' : ''));
  }
  if (MARKS[m]) {
    const [cls, label] = MARKS[m];
    const el = h('span', { class: 'st ' + cls }, label);
    const e = errOf(i);
    return e ? h('div', {}, el, h('span', { class: 'errmsg' }, e)) : el;
  }
  const j = jobs.find(x => x.id === selected);
  const syncing = j && j.status.state === 'syncing';
  if (syncing && a && a.path === currentPath()) return h('span', { class: 'st run' }, '⟳ en cours');
  if (syncing) return h('span', { class: 'st wait' }, '… en attente');
  if (a && overrides.has(a.path)) return h('span', { class: 'st manual' }, 'choix manuel');
  return h('span', { class: 'st todo', title: a ? a.reason : '' }, a && a.reason ? a.reason : 'à faire');
}

// construit l'arbre à partir des chemins des actions et agrège les états par dossier
function buildTree(plan) {
  const root = { name: '', path: '', kids: new Map(), idx: -1, isDir: true };
  plan.forEach((a, i) => {
    const parts = a.path.split('/');
    let n = root;
    parts.forEach((part, k) => {
      const path = parts.slice(0, k + 1).join('/');
      if (!n.kids.has(part)) n.kids.set(part, { name: part, path, kids: new Map(), idx: -1, isDir: true });
      n = n.kids.get(part);
    });
    n.idx = i; n.isDir = !!a.isDir;
  });
  (function agg(n) {
    n.c = { total: 0, ok: 0, err: 0, cancel: 0, pending: 0, conflict: 0, bytes: 0, same: 0, lb: 0, rb: 0, kept: 0,
      excl: 0, toR: 0, toL: 0, del: 0, none: 0, edit: 0, manual: 0 };
    if (n.idx >= 0) {
      const a = plan[n.idx], m = markOf(n.idx);
      if (m === 'e') { n.c.excl++; } else {
      if (m !== 'o' && m !== '=') { // compteurs du sens des dossiers : ce qui reste à faire
        const k = effKind(a);
        if (k === 'delete→' || k === 'delete←') n.c.del++;
        else if (k === 'copy→' || k === 'mkdir→') n.c.toR++;
        else if (k === 'copy←' || k === 'mkdir←') n.c.toL++;
        else n.c.none++;
      }
      if (editable(a)) n.c.edit++;
      if (overrides.has(a.path)) n.c.manual++;
      if (!a.isDir) {
        // tailles après la synchro pour ce qui est fait, avant pour le reste
        const L = m === 'o' ? doneSide(a, 'left') : a.left, R = m === 'o' ? doneSide(a, 'right') : a.right;
        n.c.lb += L && L.exists ? L.size : 0; n.c.rb += R && R.exists ? R.size : 0;
      }
      if (a.kind === 'leave' || overrides.get(a.path) === 'none') n.c.kept++;
      if (m === '=') {
        n.c.same++; // identique : compté à part, n'entre pas dans la progression
      } else {
        n.c.total++; n.c.bytes += a.isDir ? 0 : a.size;
        if (m === 'o') n.c.ok++; else if (m === 'x') n.c.err++; else if (m === '-') n.c.cancel++; else n.c.pending++;
      }
      if (a.conflict || a.kind === 'skip') n.c.conflict++;
      }
    }
    for (const k of n.kids.values()) { agg(k); for (const f in n.c) n.c[f] += k.c[f]; }
  })(root);
  return root;
}

function folderState(c) {
  if (c.total === 0) return c.same ? h('span', { class: 'st same' }, '= identique') : h('span', { class: 'st excl' }, c.excl ? '⊘ exclu' : '');
  const j = jobs.find(x => x.id === selected);
  const syncing = j && j.status.state === 'syncing';
  const done = c.ok + c.err + c.cancel;
  if (c.err) return h('span', { class: 'st err' }, `✗ ${c.err} échec${c.err > 1 ? 's' : ''}` + (c.ok ? ` · ${c.ok} OK` : ''));
  if (done === 0) return h('span', { class: 'st ' + (syncing ? 'wait' : 'todo') }, syncing ? `… ${c.total} en attente` : `${c.total} à faire`);
  if (c.ok === c.total) return h('span', { class: 'st ok' }, '✓ OK');
  if (c.pending && syncing) {
    return h('span', { class: 'st run' }, h('span', { class: 'minibar' }, h('i', { style: `width:${Math.round(done / c.total * 100)}%` })), `${done}/${c.total}`);
  }
  return h('span', { class: 'st cancel' }, `${c.ok}/${c.total} OK · ${c.cancel + c.pending} non fait${c.cancel + c.pending > 1 ? 's' : ''}`);
}

// ---------- Colonnes : largeur et tri, mémorisés pour chaque job ----------
const COLS = [
  { k: 'path', label: 'Élément', min: 160 },
  { k: 'lsize', label: 'Taille gauche', def: 95, min: 60, cls: 'lcol num' },
  { k: 'ldate', label: 'Date gauche', def: 140, min: 70, cls: 'lcol num' },
  { k: 'dir', label: 'Sens', def: 120, min: 60, cls: 'dircol' },
  { k: 'rdate', label: 'Date droite', def: 140, min: 70, cls: 'rcol num' },
  { k: 'rsize', label: 'Taille droite', def: 95, min: 60, cls: 'rcol num' },
  { k: 'state', label: 'État', def: 180, min: 80 },
];
function colPrefs() {
  try {
    const p = Object.assign({ w: {}, sort: null }, JSON.parse(localStorage.getItem('synkro-cols-' + selected) || '{}'));
    if (p.w.path && !p.pathSet) delete p.w.path; // largeur figée par l'ancienne version
    return p;
  }
  catch { return { w: {}, sort: null }; }
}
function saveColPrefs(p) { try { localStorage.setItem('synkro-cols-' + selected, JSON.stringify(p)); } catch {} }

function renderHead() {
  const prefs = colPrefs();
  const cols = $('#actions-cols'), head = $('#actions-head');
  cols.replaceChildren(...COLS.map(c => {
    const col = h('col', {});
    const w = prefs.w[c.k] || c.def;
    if (w) col.style.width = w + 'px';
    return col;
  }));
  head.replaceChildren(...COLS.map((c, i) => {
    const sorted = prefs.sort && prefs.sort.k === c.k;
    const th = h('th', { class: c.cls || '', title: 'Cliquer pour trier · glisser le bord pour redimensionner · double-clic sur le bord pour ajuster' },
      c.label, sorted ? h('span', { class: 'arrow-s' }, prefs.sort.dir > 0 ? '▲' : '▼') : null);
    th.onclick = e => {
      if (e.target.classList.contains('grip')) return;
      const p = colPrefs();
      // 1er clic : croissant, 2e : décroissant, 3e : ordre par défaut
      if (!p.sort || p.sort.k !== c.k) p.sort = { k: c.k, dir: 1 };
      else if (p.sort.dir > 0) p.sort.dir = -1;
      else p.sort = null;
      saveColPrefs(p); renderActions();
    };
    const grip = h('span', { class: 'grip' });
    grip.onmousedown = e => startResize(e, i);
    grip.ondblclick = e => { e.stopPropagation(); autoFit(i); };
    th.append(grip);
    return th;
  }));
}

function startResize(e, i) {
  e.preventDefault(); e.stopPropagation();
  const col = $('#actions-cols').children[i];
  const th = $('#actions-head').children[i];
  const x0 = e.clientX, w0 = th.getBoundingClientRect().width;
  // Rien n'est touché avant un vrai glisser : un simple clic (ou le 1er clic d'un
  // double-clic) ne redessine pas l'en-tête, sinon la poignée serait remplacée et
  // le double-clic d'ajustement ne se déclencherait jamais.
  let moved = false;
  const move = ev => {
    if (!moved) {
      if (Math.abs(ev.clientX - x0) < 3) return;
      moved = true;
      // pendant le glisser, les colonnes fixes gardent leur largeur ; « Chemin » s'adapte
      [...$('#actions-head').children].forEach((t, j) => { if (j !== i && j !== 0) $('#actions-cols').children[j].style.width = t.getBoundingClientRect().width + 'px'; });
      document.body.classList.add('resizing');
    }
    col.style.width = Math.max(COLS[i].min, w0 + ev.clientX - x0) + 'px';
  };
  const up = () => {
    removeEventListener('mousemove', move); removeEventListener('mouseup', up);
    if (!moved) return;
    document.body.classList.remove('resizing');
    // on ne mémorise que la colonne redimensionnée (les autres restent souples)
    const p = colPrefs();
    p.w[COLS[i].k] = parseInt(col.style.width, 10);
    if (i === 0) p.pathSet = true;
    saveColPrefs(p);
    renderHead();
  };
  addEventListener('mousemove', move); addEventListener('mouseup', up);
}

// largeur ajustée au contenu le plus large de la colonne (lignes affichées)
// Largeur du contenu lui-même (texte, icônes, retrait de l'arborescence) et non de
// la cellule, qui peut être plus large ; les lignes qui s'étendent sur plusieurs
// colonnes ne comptent pas.
function contentWidth(cell) {
  // seuls le texte et les petites icônes comptent : un élément qui s'étire avec la
  // cellule (barre de progression, conteneur) la ferait grandir à chaque double-clic
  const cs = getComputedStyle(cell), box = cell.getBoundingClientRect();
  const pl = parseFloat(cs.paddingLeft || 0), pr = parseFloat(cs.paddingRight || 0);
  let lo = Infinity, hi = -Infinity;
  const take = rc => { if (rc.width > 0) { lo = Math.min(lo, rc.left); hi = Math.max(hi, rc.right); } };
  const r = document.createRange();
  const walk = n => {
    for (const c of n.childNodes) {
      if (c.nodeType === 3) { if (c.textContent.trim()) { r.selectNodeContents(c); take(r.getBoundingClientRect()); } }
      else if (c.nodeType === 1) {
        const st = getComputedStyle(c);
        // la poignée de redimensionnement (et tout ce qui est posé par-dessus) ne compte pas
        if (st.display === 'none' || st.position === 'absolute' || c.classList.contains('grip')) continue;
        const rc = c.getBoundingClientRect();
        if (!c.children.length && !c.textContent.trim()) { if (rc.width <= 48) take(rc); } // icône, pastille
        else walk(c);
      }
    }
  };
  walk(cell);
  if (hi < lo) return pl + pr;
  // aligné à droite ou centré : la largeur du contenu ; à gauche : depuis le bord
  // gauche de la cellule (le retrait de l'arborescence compte) jusqu'au bout
  const a = cs.textAlign;
  if (a === 'right' || a === 'end' || a === 'center') return hi - lo + pl + pr;
  return hi - box.left + pr;
}
function autoFit(i) {
  const th = $('#actions-head').children[i];
  let w = contentWidth(th) + 10; // + la poignée et la flèche de tri
  let col = 0;
  for (const tr of $('#actions-body').children) {
    // retrouver la cellule de la colonne i en tenant compte des fusions
    col = 0;
    for (const td of tr.children) {
      if (col === i && td.colSpan === 1) { w = Math.max(w, contentWidth(td)); break; }
      col += td.colSpan || 1;
      if (col > i) break;
    }
  }
  const p = colPrefs();
  p.w[COLS[i].k] = Math.max(COLS[i].min, Math.ceil(w) + 4);
  if (i === 0) p.pathSet = true;
  saveColPrefs(p); renderActions();
}
$('#cols-reset').onclick = () => { try { localStorage.removeItem('synkro-cols-' + selected); } catch {} renderActions(); };

// ordre de tri d'un état : échecs d'abord, puis à faire, annulés, réussis, identiques
const MARK_RANK = { x: 0, '.': 1, '-': 2, o: 3, '=': 4 };
function nodeRank(n) {
  if (n.kids.size) return n.c.err ? 0 : n.c.pending ? 1 : n.c.cancel ? 2 : n.c.total ? 3 : 4;
  return MARK_RANK[markOf(n.idx)] ?? 1;
}
function kindLabel(a) { return a ? (KINDS[effKind(a)] || [effKind(a)])[0] : ''; }
const sideSize = (a, s) => a && a[s] && a[s].exists ? (a.isDir ? 0 : a[s].size) : -1;
const sideDate = (a, s) => a && a[s] && a[s].exists && !a.isDir ? Date.parse(a[s].modTime) : -1;
// ordre de copie de la tâche affichée, comme tri par défaut de la liste
function copyOrderSort() {
  const j = jobs.find(x => x.id === selected);
  const o = j && j.options && j.options.copyOrder || '';
  return o === 'path' ? null : { k: 'csize', dir: o === 'size-desc' ? -1 : 1 };
}
const ROW_VAL = {
  path: a => a.path, dir: a => kindLabel(a),
  csize: a => a.isDir ? 0 : (a.size || 0), // taille à copier
  lsize: a => sideSize(a, 'left'), ldate: a => sideDate(a, 'left'),
  rsize: a => sideSize(a, 'right'), rdate: a => sideDate(a, 'right'),
};

const LIMIT = 3000; // lignes affichées au maximum
// Suivi du fichier en cours pendant la synchro (suspendu quelques secondes
// quand l'utilisateur fait défiler la liste lui-même)
let followPausedUntil = 0;
for (const ev of ['wheel', 'mousedown', 'keydown', 'touchstart'])
  $('#tab-actions .table-wrap').addEventListener(ev, () => { followPausedUntil = Date.now() + 8000; }, { passive: true });
const followBox = $('#follow');
followBox.checked = localStorage.getItem('synkro-follow') !== '0';
followBox.onchange = () => { localStorage.setItem('synkro-follow', followBox.checked ? '1' : '0'); followCurrent(); };
function following() { return followBox.checked && Date.now() >= followPausedUntil; }
function followCurrent() {
  if (!currentPath() || !following() || !lastCurPath && lastCurPath !== '') return;
  scrollToRow(lastCurPath, false);
}

let treeCache = {};
let syncTick = false, lastFullRender = 0;
let liveCurPath = ''; // ligne surlignée comme « en cours » (fichier, ou dossier replié qui le contient)
// fichier en cours ; si son dossier est replié, on montre le dossier (sans le déplier)
function curRowPath() {
  let cur = currentPath();
  rootCur = false;
  if (cur && view === 'tree' && !$('#filter').value.trim() && !cardFilter && !$('#only-errors').checked) {
    if (!expanded.has('')) { rootCur = true; return ''; } // tâche repliée : on montre sa ligne
    const parts = cur.split('/');
    for (let k = 1; k < parts.length; k++) {
      const dir = parts.slice(0, k).join('/');
      if (!expanded.has(dir)) return dir;
    }
  }
  return cur;
}
// clic simple sur un dossier : replier / déplier, après un court délai pour
// laisser la place au double-clic (détail)
let foldTimer = null;
let rootCur = false; // la ligne de la tâche tient lieu de fichier en cours (tâche repliée)
const isCur = (p, cur) => p === cur && (p !== '' || rootCur);
// Liste virtuelle : seules les lignes visibles (plus une marge) existent dans la
// page ; deux lignes vides au-dessus et au-dessous donnent la bonne hauteur de
// défilement. Rapide quel que soit le nombre de lignes (150 000 et plus).
let rowQueue = { fns: [], index: new Map() };
let ROW_H = 33;            // hauteur d'une ligne, mesurée à l'affichage
const ROW_MARGIN = 60;     // lignes créées en plus au-dessus et au-dessous
let win = { first: -1, last: -1 };
function spacer(px) {
  const n = $('#actions-head').children.length || 1; // autant de colonnes que l'en-tête
  return h('tr', { class: 'vspace', 'aria-hidden': 'true' }, h('td', { colspan: String(n), style: `height:${px}px;padding:0;border:0;background:none` }));
}
function renderWindow(force, scrollTo) {
  const wrap = $('#tab-actions .table-wrap'), body = $('#actions-body');
  const n = rowQueue.fns.length;
  const head = $('#actions-head').offsetHeight || 0;
  const top = Math.max(0, (scrollTo ?? wrap.scrollTop) - head);
  // hauteur visible : jamais plus que la fenêtre (pendant un changement de mise en
  // page, la zone peut prendre un instant la hauteur de toute la liste : 80 000
  // lignes construites d'un coup, l'interface figée)
  const viewH = Math.min(wrap.clientHeight || 0, window.innerHeight || 1000);
  const first = Math.max(0, Math.floor(top / ROW_H) - ROW_MARGIN);
  const last = Math.min(n, Math.ceil((top + viewH) / ROW_H) + ROW_MARGIN);
  if (!force && first === win.first && last === win.last) return;
  win = { first, last };
  const rows = [];
  for (let i = first; i < last; i++) rows.push(rowQueue.fns[i][1]());
  body.replaceChildren(spacer(first * ROW_H), ...rows, spacer((n - last) * ROW_H));
  if (scrollTo != null) wrap.scrollTop = scrollTo;
  // hauteur réelle d'une ligne (fixée par la feuille de style ; varie avec le
  // zoom) : mesurée sur la plus haute, recalée UNE fois au plus (jamais en boucle)
  if (!rowHMeasured && rows.length) {
    rowHMeasured = true;
    const hgt = Math.max(...rows.slice(0, 30).map(x => x.getBoundingClientRect().height));
    if (hgt > 0 && Math.abs(hgt - ROW_H) > 0.5) { ROW_H = hgt; win = { first: -1, last: -1 }; renderWindow(true, wrap.scrollTop); }
  }
}
let rowHMeasured = false;
addEventListener('resize', () => { rowHMeasured = false; }); // zoom ou taille de police changés
// fait défiler la liste jusqu'à une ligne (chemin), centrée
function scrollToRow(path, smooth) {
  const k = rowQueue.index.get(path);
  if (k === undefined) return;
  const wrap = $('#tab-actions .table-wrap');
  const head = $('#actions-head').offsetHeight || 0;
  const target = Math.max(0, head + k * ROW_H - wrap.clientHeight / 2 + ROW_H / 2);
  if (Math.abs(wrap.scrollTop - target) > ROW_H) wrap.scrollTo({ top: target, behavior: smooth ? 'smooth' : 'auto' });
  renderWindow();
}
let winRaf = 0;
$('#tab-actions .table-wrap').addEventListener('scroll', () => {
  if (!winRaf) winRaf = requestAnimationFrame(() => { winRaf = 0; renderWindow(); });
}, { passive: true });
new ResizeObserver(() => renderWindow()).observe($('#tab-actions .table-wrap'));

// fresh : nouvelles données (analyse, changement de job…) → retour en haut de la liste ;
// sinon (dépliage, état en direct, choix du sens) la position est gardée.
let lastCurPath = '';
function renderActions(fresh) {
  const t0 = performance.now();
  try { return renderActionsInner(fresh); } finally { lastRenderMs = performance.now() - t0; }
}
// comparaisons de noms : un seul objet Intl.Collator (localeCompare avec options
// en recrée un à chaque appel : 15 fois plus lent, des secondes sur 80 000 lignes)
const CMP = new Intl.Collator('fr', { sensitivity: 'base' }).compare;
const CMPN = new Intl.Collator('fr', { sensitivity: 'base', numeric: true }).compare;
function renderActionsInner(fresh) {
  const keepPosition = !fresh;
  renderHead();
  // tri : colonne choisie par l'utilisateur, sinon l'ordre de copie de la tâche
  // (du plus petit au plus gros, du plus gros au plus petit, ou ordre de la liste)
  const sortBy = colPrefs().sort || copyOrderSort();
  const plan = allRows();
  let curPath = curRowPath();
  liveCurPath = curPath;
  const nActions = (details.plan || []).length;
  $('#show-same').classList.toggle('on', showSame);
  const nSame = (jobs.find(x => x.id === selected) || { status: {} }).status.summary;
  $('#show-same').textContent = (showSame ? 'Masquer' : 'Afficher') + ' les inchangés et exclus' + (nSame ? ` (${nSame.unchanged})` : '');
  const q = $('#filter').value.trim().toLowerCase();
  const only = $('#only-errors').checked;
  const wrap = $('#tab-actions .table-wrap');
  const keepScroll = keepPosition ? wrap.scrollTop : 0; // avant de vider la liste (sinon on remonte en haut)
  const body = $('#actions-body'); body.replaceChildren();
  $('#view-tree').classList.toggle('on', view === 'tree');
  $('#view-list').classList.toggle('on', view === 'list');
  $('#expand-all').hidden = $('#collapse-all').hidden = view !== 'tree';

  const byCard = cardFilter && CARD_FILTERS[cardFilter];
  const interesting = i => (!only || markOf(i) === 'x' || plan[i].conflict || plan[i].kind === 'skip') && (!byCard || byCard(plan[i], i));
  const matches = i => (!q || plan[i].path.toLowerCase().includes(q)) && interesting(i);
  let shown = 0;

  // légende : bilan global
  const c = { ok: 0, err: 0, cancel: 0 };
  for (let i = 0; i < nActions; i++) { const m = markOf(i); if (m === 'o') c.ok++; else if (m === 'x') c.err++; else if (m === '-') c.cancel++; }
  $('#legend').replaceChildren(...(plan.length ? [
    h('span', {}, `${nActions} action(s)`),
    h('span', { class: 'st ok' }, `✓ ${c.ok} OK`),
    h('span', { class: 'st err' }, `✗ ${c.err} échec(s)`),
    c.cancel ? h('span', { class: 'st cancel' }, `${c.cancel} annulé(s)`) : null,
    h('span', {}, `${nActions - c.ok - c.err - c.cancel} restant(s)`),
    showSame && sameRows ? h('span', { class: 'st same' }, (n => `= ${sameRows.length - n} identique(s)` + (n ? ` · ⊘ ${n} exclu(s)` : ''))(sameRows.reduce((t, a) => t + (a.kind === 'excluded'), 0))) : null,
    cardFilter ? h('span', { class: 'st manual' }, 'Filtre : ' + { right: 'vers la droite', left: 'vers la gauche', del: 'suppressions', conflict: 'conflits', error: 'erreurs', same: 'inchangés', datefix: 'dates décalées', canceled: 'annulés' }[cardFilter] + ' ',
      Object.assign(h('button', { type: 'button', class: 'link' }, 'tout afficher'), { onclick: () => { cardFilter = null; refresh(); renderActions(); } })) : null,
    overrides.size ? h('span', { class: 'st manual' }, `✎ ${overrides.size} choix manuel(s) `,
      Object.assign(h('button', { type: 'button', class: 'link' }, 'annuler les choix'), { onclick: () => { overrides = new Map(); ovVer++; renderActions(); refresh(); } })) : null,
    canEdit() ? h('span', { class: 'hint' }, 'Astuce : cliquez sur une flèche pour changer le sens (→ droite, ← gauche, = rien).') : null,
  ].filter(Boolean) : []));

  // Les lignes sont préparées (fonctions), puis créées par paquets au fil du défilement :
  // déplier, replier ou afficher un résultat reste rapide même avec des milliers d'éléments.
  const rowFns = []; // [chemin, fabrique de la ligne]
  if (view === 'list') {
    let order = [];
    for (let i = 0; i < plan.length; i++) if (matches(i)) order.push(i);
    if (sortBy) {
      const val = sortBy.k === 'state' ? i => MARK_RANK[markOf(i)] ?? 1 : i => (ROW_VAL[sortBy.k] || ROW_VAL.path)(plan[i]);
      order.sort((x, y) => { const a = val(x), b = val(y); return sortBy.dir * (typeof a === 'string' ? CMP(a, b) : a - b); });
    }
    for (const i of order) {
      const a = plan[i];
      rowFns.push([a.path, () => h('tr', { 'data-path': a.path, 'data-dir': a.isDir ? '1' : '0', class: (a.path === liveCurPath ? 'current ' : '') + (a.path === detailPath ? 'picked ' : '') + (markOf(i) === 'x' ? 'failed' : markOf(i) === '-' ? 'canceled' : a.conflict && !overrides.has(a.path) ? 'conflict' : ''), title: a.reason },
        h('td', { class: 'path', title: a.path }, a.path + (a.isDir ? '/' : '')),
        ...sideCells(a, 'left', i),
        h('td', { class: 'dircol' }, dirCell(a, i)),
        ...sideCells(a, 'right', i),
        h('td', {}, stateCell(i, a)))]);
    }
  } else {
    // l'arbre (et ses totaux) n'est recalculé que si les données changent, pas au dépliage
    const tkey = [plan.length, details.marks && details.marks.marks, ovVer, selected].join('|');
    if (treeCache.plan !== plan || treeCache.key !== tkey) treeCache = { plan, key: tkey, tree: buildTree(plan) };
    const tree = treeCache.tree;
    // Après une nouvelle analyse, tout est déplié. Ensuite l'état de chaque dossier est
    // gardé (affichage des inchangés, filtres…) ; seuls les dossiers jamais vus s'ouvrent.
    const ekey = selected + '|' + (details.plan || []).length + '|' + ((details.plan || [])[0] || {}).path;
    if (expandedFor !== ekey) {
      expandedFor = ekey;
      expanded = new Set(['']);
      knownDirs = new Set();
    }
    (function seen(n) {
      for (const k of n.kids.values()) {
        if (!k.kids.size) continue;
        if (!knownDirs.has(k.path)) { knownDirs.add(k.path); expanded.add(k.path); }
        seen(k);
      }
    })(tree);
    const filtering = !!q || only || !!byCard;
    // un nœud est visible s'il correspond ou si un de ses descendants correspond
    const visible = new Map();
    if (filtering) (function vis(n) {
      let v = n.idx >= 0 && matches(n.idx);
      for (const k of n.kids.values()) v = vis(k) || v;
      visible.set(n, v);
      return v;
    })(tree);
    const nodeVal = {
      path: n => n.name,
      dir: n => n.kids.size ? '' : kindLabel(n.idx >= 0 ? plan[n.idx] : null),
      lsize: n => n.kids.size ? n.c.lb : sideSize(plan[n.idx], 'left'),
      rsize: n => n.kids.size ? n.c.rb : sideSize(plan[n.idx], 'right'),
      ldate: n => n.kids.size ? -1 : sideDate(plan[n.idx], 'left'),
      rdate: n => n.kids.size ? -1 : sideDate(plan[n.idx], 'right'),
      state: nodeRank,
      csize: n => n.kids.size ? n.c.bytes : (plan[n.idx] && !plan[n.idx].isDir ? plan[n.idx].size || 0 : 0),
    };
    // toutes les actions contenues dans un dossier (calculé seulement au clic)
    const rowsUnder = n => { const out = []; (function g(x) { if (x.idx >= 0) out.push(plan[x.idx]); x.kids.forEach(g); })(n); return out; };
    const byName = (a, b) => CMPN(a.name, b.name);
    // ordre alphabétique, dossiers et fichiers mélangés
    const skey = sortBy ? sortBy.k + sortBy.dir : '';
    // tri mémorisé dans l'arbre : déplier / replier ne retrie pas
    const sorted = n => { if (n._sk !== skey) { n._sorted = sortKids(n); n._sk = skey; } return n._sorted; };
    const sortKids = n => [...n.kids.values()].sort((a, b) => {
      if (sortBy) {
        const x = nodeVal[sortBy.k](a), y = nodeVal[sortBy.k](b);
        const d = typeof x === 'string' ? CMPN(x, y) : x - y;
        if (d) return sortBy.dir * d;
      }
      return byName(a, b);
    });
    const rowOf = (k, depth, open) => () => {
      const hasKids = k.kids.size > 0;
      const a = k.idx >= 0 && !hasKids ? plan[k.idx] : null;
      const toggle = e => {
        if (!hasKids || (e && e.detail > 1)) return;
        const now = e && e.target.closest('.twist'); // la petite flèche : tout de suite
        clearTimeout(foldTimer);
        foldTimer = setTimeout(() => {
          if (filtering) folded.has(k.path) ? folded.delete(k.path) : folded.add(k.path); // liste filtrée : tout ouvert sauf replié
          else expanded.has(k.path) ? expanded.delete(k.path) : expanded.add(k.path);
          renderActions();
        }, now ? 0 : 230);
      };
      const isFolder = hasKids || k.isDir;
      return h('tr', {
        'data-path': k.path, 'data-dir': isFolder ? '1' : '0',
        class: (isCur(k.path, liveCurPath) ? 'current ' : '') + (k.path === detailPath ? 'picked ' : '') + (hasKids ? 'dirrow ' : '') + (k.c.err ? 'failed' : k.c.cancel ? 'canceled' : (a && a.conflict && !overrides.has(a.path)) ? 'conflict' : ''),
        title: a ? a.reason : '', onclick: hasKids ? toggle : null,
      },
        h('td', { class: 'tree', title: k.path, style: `padding-left:${8 + depth * 18}px` },
          h('span', { class: 'twist' + (hasKids ? '' : ' leaf') }, hasKids ? (open ? '▾' : '▸') : ''),
          h('span', { class: 'ico' }, k.path === '' ? '🗂' : isFolder ? (open ? '📂' : '📁') : '📄'),
          h('span', { class: 'nm' }, k.name)),
        ...(hasKids ? (() => {
          // dossier : total de ce qu'il contient, ou « absent » s'il n'existe pas de ce côté
          const own = k.idx >= 0 ? plan[k.idx] : null;
          const done = own && markOf(k.idx) === 'o';
          const oL = own && (done ? doneSide(own, 'left') : own.left), oR = own && (done ? doneSide(own, 'right') : own.right);
          const lAbs = oL && !oL.exists, rAbs = oR && !oR.exists;
          const onlyExcl = !k.c.total && !k.c.same && k.c.excl;
          return [
            h('td', { class: 'lcol num' + (lAbs ? ' absent' : '') }, lAbs ? '' : h('b', {}, bytes(k.c.lb))),
            h('td', { class: 'lcol num' + (lAbs ? ' absent' : ' muted') }, lAbs ? 'absent' : 'dossier'),
            h('td', { class: 'dircol' }, k.c.total && k.c.ok === k.c.total && !k.c.kept
              ? h('span', { class: 'dirbtn done folderdone', title: 'Tout est fait' }, icon('done', 22, true))
              : onlyExcl ? '' : folderDirCell(k.c, () => rowsUnder(k))),
            h('td', { class: 'rcol num' + (rAbs ? ' absent' : ' muted') }, rAbs ? 'absent' : 'dossier'),
            h('td', { class: 'rcol num' + (rAbs ? ' absent' : '') }, rAbs ? '' : h('b', {}, bytes(k.c.rb))),
          ];
        })() : [...sideCells(a, 'left', k.idx), h('td', { class: 'dircol' }, a ? dirCell(a, k.idx) : ''), ...sideCells(a, 'right', k.idx)]),
        h('td', {}, hasKids ? folderState(k.c) : stateCell(k.idx, a)));
    };
    const walk = (n, depth) => {
      for (const k of sorted(n)) {
        if (filtering && !visible.get(k)) continue;
        const open = k.kids.size > 0 && (filtering ? !folded.has(k.path) : expanded.has(k.path));
        rowFns.push([k.path, rowOf(k, depth, open)]);
        if (open) walk(k, depth + 1);
      }
    };
    // première ligne : la tâche elle-même, qu'on peut replier / déplier d'un coup
    if (tree.kids.size && (!filtering || visible.get(tree))) {
      const jj = jobs.find(x => x.id === selected);
      tree.name = jj ? jj.name : 'Tâche';
      const open = filtering ? !folded.has('') : expanded.has('');
      rowFns.push(['', rowOf(tree, 0, open)]);
      if (open) walk(tree, 1);
    }
  }
  // seules les lignes visibles sont créées (voir renderWindow)
  rowQueue = { fns: rowFns, index: new Map(rowFns.map((r, i) => [r[0], i])) };
  lastCurPath = curPath;
  // la liste a été vidée : on la reconstruit à la position gardée, puis on y
  // remet le défilement (sinon le navigateur ramène la liste en haut)
  renderWindow(true, keepScroll);
  shown = rowFns.length;
  $('#actions-more').hidden = true;
  $('#actions-empty').hidden = plan.length > 0;
  if (!plan.length && nActions === 0 && showSame && sameRows) $('#actions-empty').hidden = false;
  $('#actions-empty').textContent = 'Lancez une analyse pour voir ce qui sera fait.';
  $('#tab-actions .table-wrap').classList.toggle('no-rows', !rowFns.length);
  if (plan.length && !rowFns.length) {
    $('#actions-empty').hidden = false;
    $('#actions-empty').textContent = cardFilter ? 'Aucun élément dans cette catégorie.' : 'Aucun élément ne correspond au filtre.';
  }
  if (plan.length === 0 && lastState[selected] && lastState[selected].startsWith('done')) {
    $('#actions-empty').hidden = false;
    $('#actions-empty').textContent = details.cleared ? 'Terminé : liste effacée automatiquement (option de la tâche). ✓' : 'Rien à faire : les deux côtés sont identiques. ✓';
  }
}

$('#view-tree').onclick = () => { view = 'tree'; localStorage.setItem('synkro-view', view); renderActions(); };
$('#view-list').onclick = () => { view = 'list'; localStorage.setItem('synkro-view', view); renderActions(); };
$('#expand-all').onclick = () => {
  folded = new Set();
  (function all(n) { if (n.kids.size) { expanded.add(n.path); n.kids.forEach(all); } })(treeCache.tree || buildTree(allRows()));
  expanded.add('');
  renderActions();
};
$('#show-same').onclick = async () => {
  showSame = !showSame;
  localStorage.setItem('synkro-same', showSame ? '1' : '0');
  if (showSame && !sameRows) await loadSame();
  renderActions();
};
$('#collapse-all').onclick = () => {
  expanded = new Set(['']);
  // liste filtrée (recherche, case du bilan, dates décalées…) : on replie tous les dossiers sous la tâche
  folded = new Set();
  (function all(n) { if (n.kids.size) { if (n.path) folded.add(n.path); n.kids.forEach(all); } })(treeCache.tree || buildTree(allRows()));
  renderActions();
};

async function loadLog() {
  if (!selected) return;
  const txt = await api('GET', `/api/jobs/${selected}/log`).catch(e => e.message);
  const pre = $('#log');
  const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20;
  // en anglais : le journal (écrit en français par le moteur) est traduit ligne par ligne
  pre.textContent = txt ? (LANG === 'en' && window.trLog ? String(txt).split('\n').map(window.trLog).join('\n') : txt) : (LANG === 'en' ? 'Empty log.' : 'Journal vide.');
  if (atBottom) pre.scrollTop = pre.scrollHeight;
}

// ---------- Actions ----------
async function act(path, body) {
  try { await api('POST', `/api/jobs/${selected}/${path}`, body); } catch (e) { alertBox(e.message); }
  setTimeout(refresh, 150);
}
function alertBox(msg) { $('#status-msg').textContent = msg; $('#status-box').classList.add('err'); }

// anti double-clic : un clic de plus dans la seconde qui suit est ignoré
let lastAnalyzeClick = 0;
$('#btn-analyze').onclick = () => {
  if (Date.now() - lastAnalyzeClick < 1000) return;
  lastAnalyzeClick = Date.now();
  overrides = new Map(); ovVer++; act('analyze');
};
$('#btn-sync').onclick = async () => {
  const jj = jobs.find(x => x.id === selected);
  if (jj && jj.status.state === 'conflicts') { openConflicts(jj); return; }
  const j = jobs.find(x => x.id === selected);
  const s = j && effSummary(j.status.summary);
  const dels = s ? s.delLeft + s.delRight : 0;
  if (dels > 50 && !await askConfirm(`La dernière analyse prévoit ${dels} suppressions. Continuer ?`, { title: 'Beaucoup de suppressions' })) return;
  const nmod = s ? s.toRight + s.toLeft + s.delLeft + s.delRight : 0;
  const lim = j && j.options.maxChanges;
  if (lim && nmod > lim && !await askConfirm(`${nmod} modifications prévues, plus que la limite fixée pour cette tâche (${lim}). Synchroniser quand même ?`, { title: 'Limite de modifications dépassée', ok: 'Synchroniser quand même' })) return;
  // les choix manuels ne valent que pour l'analyse affichée
  const ov = canEdit() && overrides.size ? Object.fromEntries(overrides) : undefined;
  const order = userCopyOrder();
  startSolo(selected);
  act('sync', ov || order ? { overrides: ov, order } : undefined);
};
// liste triée par l'utilisateur (Élément, Date, Taille…) : la copie suit cet ordre
function userCopyOrder() {
  const sortBy = colPrefs().sort;
  const plan = details.plan || [];
  if (!sortBy || sortBy.k === 'state' || !plan.length) return undefined;
  const val = ROW_VAL[sortBy.k] || ROW_VAL.path;
  const idx = plan.map((_, i) => i);
  idx.sort((x, y) => { const a = val(plan[x]), b = val(plan[y]); return sortBy.dir * (typeof a === 'string' ? CMP(a, b) : a - b); });
  return idx.map(i => plan[i].path);
}
// Vitesse : choix rapides, plus « autre… » ; une valeur hors liste y est ajoutée.
function speedLabel(k) { return 'Vitesse : ' + (!k ? 'illimitée' : k % 1024 === 0 ? `${k / 1024} Mo/s` : `${k} Ko/s`); }
function setSpeedChoice(k) {
  const sp = $('#sel-speed'), v = String(k || 0);
  let o = [...sp.options].find(x => x.value === v);
  if (!o) {
    sp.querySelectorAll('option.extra').forEach(x => x.remove());
    o = h('option', { value: v, class: 'extra' }, speedLabel(k));
    sp.insertBefore(o, sp.querySelector('option[value="custom"]'));
  }
  sp.value = v; sp.dataset.cur = v;
}
$('#sel-speed').onchange = async e => {
  const sp = e.target, j = jobs.find(x => x.id === selected);
  let k = sp.value;
  if (k === 'custom') {
    const t = await askText('Vitesse de transfert', 'Limite en Ko/s (0 = illimitée)', sp.dataset.cur || '0');
    if (t == null || !/^\s*\d+\s*$/.test(t)) { setSpeedChoice(+sp.dataset.cur || 0); sp.blur(); return; }
    k = t.trim();
  }
  k = parseInt(k, 10) || 0;
  sp.blur();
  if (!j) return;
  try {
    await api('POST', `/api/jobs/${j.id}/speed`, { maxKBps: k });
    setSpeedChoice(k);
    j.options && (j.options.maxKBps = k);
    toast(speedLabel(k) + ' · appliquée sans interrompre la copie');
    setTimeout(refresh, 150);
  } catch (err) { toast(err.message); setSpeedChoice(+sp.dataset.cur || 0); }
};
$('#btn-pause').onclick = () => {
  const j = jobs.find(x => x.id === selected);
  act(j && j.status.paused ? 'resume' : 'pause');
};
// Annuler : s'il y a d'autres jobs qui attendent, on demande s'il faut aussi les annuler
let cancelTarget = null; // tâche visée par la fenêtre d'annulation
async function cancelJob(id) {
  try { await api('POST', `/api/jobs/${id}/cancel`); } catch (e) { alertBox(e.message); }
  setTimeout(refresh, 150);
}
$('#btn-cancel').onclick = () => askCancel(jobs.find(x => x.id === selected));
function askCancel(j) {
  if (!j) return;
  cancelTarget = j.id;
  // simple retrait de la file d'attente : rien en cours, pas de question
  if (['waiting', 'queued'].includes(j.status.state)) { cancelJob(j.id); return; }
  const others = jobs.filter(x => x.id !== j.id && ['waiting', 'queued'].includes(x.status.state));
  const p = j.status.progress;
  const what = j.status.state === 'analyzing' ? 'L\'analyse' : 'La synchronisation';
  $('#cancel-title').textContent = j.status.state === 'analyzing' ? 'Annuler l\'analyse ?' : 'Annuler la synchronisation ?';
  $('#cancel-text').replaceChildren(
    `${what} de « ${j.name} » est en cours` + (j.status.fix ? ` (correction des dates : ${nf(j.status.fix.done)} sur ${nf(j.status.fix.total)})`
      : p && p.totalActions ? ` (${p.doneActions || 0} action(s) faite(s) sur ${p.totalActions})` : '') +
    (j.status.state === 'analyzing' ? '. Elle sera arrêtée : rien n\'a encore été copié ni supprimé.' : '. Ce qui est déjà fait est gardé ; le reste ne sera pas fait.'),
    ...(others.length ? [h('br'), `${others.length} autre(s) tâche(s) attendent leur tour : `, h('b', {}, others.map(x => '« ' + x.name + ' »').join(', ')), '.'] : []));
  $('#cancel-next').textContent = others.length ? 'Annuler et passer à la suivante' : 'Oui, annuler';
  $('#cancel-all').hidden = !others.length;
  $('#dlg-cancel').showModal();
  $('#cancel-keep').focus(); // Entrée par erreur : on continue
}
$('#cancel-keep').onclick = () => $('#dlg-cancel').close();
$('#cancel-next').onclick = () => { $('#dlg-cancel').close(); if (cancelTarget) cancelJob(cancelTarget); };
$('#cancel-all').onclick = async () => {
  $('#dlg-cancel').close();
  try { await api('POST', '/api/cancel-all'); } catch (e) { alertBox(e.message); }
  refresh();
};
$('#filter').oninput = renderActions;
$('#only-errors').onchange = renderActions;
$$('.tabs button').forEach(b => b.onclick = () => {
  tab = b.dataset.tab;
  $$('.tabs button').forEach(x => x.classList.toggle('active', x === b));
  $('#tab-actions').hidden = tab !== 'actions'; $('#tab-log').hidden = tab !== 'log';
  if (tab === 'log') loadLog();
});

// ---------- Formulaire de job ----------
const dlg = $('#dlg-job');
const form = $('#job-form');
let editingId = null;

function buildEditor(fs, side) {
  const box = $('.ep-editor', fs);
  box.replaceChildren($('#tpl-ep').content.cloneNode(true));
  const radios = $$('.seg input', box);
  radios.forEach(r => { r.name = 'type-' + side; r.onchange = () => toggleType(box); });
  $('.pick', box).onclick = async () => {
    const cur = $('.local-path', box).value;
    const p = native ? await native.pickFolder(cur) : await browseFolder(cur);
    if (p) $('.local-path', box).value = p;
  };
  $('.u-refresh', box).onclick = () => loadDrives(box);
  $('.u-drive', box).onchange = () => { showUsbInfo(box); toggleType(box); };
  $('.u-pick', box).onclick = async () => {
    const o = $('.u-drive', box).selectedOptions[0];
    if (!o || !o.dataset.root) { $('.u-info', box).textContent = 'Branchez la clé et choisissez-la dans la liste.'; return; }
    const root = o.dataset.root;
    const sub = $('.u-path', box).value.trim().replace(/^[\\/]+/, '');
    const start = sub ? root.replace(/[\\/]$/, '') + (root.includes('\\') ? '\\' : '/') + sub : root;
    const p = native ? await native.pickFolder(start) : await browseFolder(start);
    if (!p) return;
    const norm = x => x.replace(/[\\/]+$/, '').toLowerCase();
    if (!norm(p).startsWith(norm(root)) ) { $('.u-info', box).textContent = '⚠ Choisissez un dossier situé sur la clé ' + root; return; }
    $('.u-path', box).value = '\\' + p.slice(root.replace(/[\\/]+$/, '').length).replace(/^[\\/]+/, '');
  };
  $('.s-pick', box).onclick = async () => {
    const ep = readEp(box), out = $('.test-result', box);
    if (!ep.sftp.host || !ep.sftp.user) { out.className = 'test-result bad'; out.textContent = '✗ Indiquez d\'abord l\'hôte et l\'utilisateur.'; return; }
    out.className = 'test-result'; out.textContent = '';
    const p = await browseFolder(ep.sftp.path, path => api('POST', '/api/browse-sftp', { jobId: editingId || '', side, endpoint: ep, path: path || '' }),
      `Choisir un dossier sur ${ep.sftp.user}@${ep.sftp.host}`);
    if (p) $('.s-path', box).value = p;
  };
  $('.test', box).onclick = async () => {
    const out = $('.test-result', box);
    out.className = 'test-result'; out.textContent = 'Connexion…';
    try {
      const r = await api('POST', '/api/test-endpoint', { jobId: editingId || '', side, endpoint: readEp(box) });
      out.className = 'test-result ' + (r.ok ? 'ok' : 'bad'); out.textContent = (r.ok ? '✓ ' : '✗ ') + r.message;
    } catch (e) { out.className = 'test-result bad'; out.textContent = '✗ ' + e.message; }
  };
}
function toggleType(box) {
  const t = $('.seg input:checked', box).value;
  $('.ep-local', box).hidden = t !== 'local'; $('.ep-sftp', box).hidden = t !== 'sftp';
  $('.ep-usb', box).hidden = t !== 'usb';
  if (t === 'usb' && !$('.u-drive', box).options.length) loadDrives(box);
  $('.test-result', box).textContent = '';
}

// Liste des lecteurs branchés ; garde la clé enregistrée même si elle est absente.
async function loadDrives(box) {
  const sel = $('.u-drive', box);
  const keep = sel.dataset.saved ? JSON.parse(sel.dataset.saved) : null;
  const cur = sel.selectedOptions[0] ? { serial: sel.selectedOptions[0].dataset.serial, label: sel.selectedOptions[0].dataset.label } : keep;
  const list = await api('GET', '/api/drives').catch(() => []);
  list.sort((a, b) => (b.removable - a.removable) || a.root.localeCompare(b.root));
  sel.replaceChildren(h('option', { value: '' }, list.length ? '— Choisir une clé ou un disque —' : '— Aucun lecteur détecté —'));
  let matched = false;
  for (const d of list) {
    const o = h('option', { value: d.serial || d.label }, `${d.root}  ${d.label || '(sans nom)'}  · ${d.removable ? 'amovible' : 'disque'} · ${bytes(d.total)}${d.serial ? '  · n° ' + d.serial : ''}`);
    Object.assign(o.dataset, { serial: d.serial || '', label: d.label || '', root: d.root });
    if (cur && ((cur.serial && cur.serial === d.serial) || (!cur.serial && cur.label && cur.label === d.label))) { o.selected = true; matched = true; }
    sel.append(o);
  }
  if (cur && (cur.serial || cur.label) && !matched) {
    const o = h('option', {}, `« ${cur.label || cur.serial} » — non branchée`);
    Object.assign(o.dataset, { serial: cur.serial || '', label: cur.label || '', root: '' });
    o.selected = true;
    sel.append(o);
  }
  showUsbInfo(box);
}
function showUsbInfo(box) {
  const o = $('.u-drive', box).selectedOptions[0];
  const info = $('.u-info', box);
  if (!o || (!o.dataset.serial && !o.dataset.label)) { info.textContent = 'Synkro reconnaîtra la clé par son numéro de série, quelle que soit sa lettre de lecteur.'; return; }
  info.textContent = o.dataset.root
    ? `Clé reconnue par son numéro de série${o.dataset.serial ? ' (' + o.dataset.serial + ')' : ''} : aujourd'hui en ${o.dataset.root}, demain peut-être sur une autre lettre, le job la retrouvera.`
    : `Clé « ${o.dataset.label || o.dataset.serial} » non branchée pour l'instant : le job la retrouvera dès qu'elle le sera.`;
}
function readEp(box) {
  const t = $('.seg input:checked', box).value;
  if (t === 'local') return { type: 'local', path: $('.local-path', box).value.trim() };
  if (t === 'usb') {
    const o = $('.u-drive', box).selectedOptions[0];
    const volume = o && (o.dataset.serial || o.dataset.label) ? { serial: o.dataset.serial || '', label: o.dataset.label || '' } : null;
    return { type: 'usb', path: $('.u-path', box).value.trim() || '\\', volume };
  }
  return { type: 'sftp', path: '', sftp: {
    host: $('.s-host', box).value.trim(), port: parseInt($('.s-port', box).value, 10) || 22,
    user: $('.s-user', box).value.trim(), password: $('.s-pass', box).value,
    keyFile: $('.s-key', box).value.trim(), keyPassphrase: $('.s-keypass', box).value,
    path: $('.s-path', box).value.trim(),
  } };
}
function fillEp(box, e) {
  $$('.seg input', box).forEach(r => r.checked = r.value === (e ? e.type : 'local'));
  toggleType(box);
  $('.local-path', box).value = e && e.type === 'local' ? e.path : '';
  const sel = $('.u-drive', box);
  sel.dataset.saved = e && e.type === 'usb' && e.volume ? JSON.stringify(e.volume) : '';
  sel.replaceChildren();
  $('.u-path', box).value = e && e.type === 'usb' ? (e.path === '\\' || e.path === '/' ? '' : e.path) : '';
  if (e && e.type === 'usb') loadDrives(box);
  const s = (e && e.sftp) || {};
  $('.s-host', box).value = s.host || ''; $('.s-port', box).value = s.port || '';
  $('.s-user', box).value = s.user || ''; $('.s-pass', box).value = s.password || '';
  $('.s-key', box).value = s.keyFile || ''; $('.s-keypass', box).value = s.keyPassphrase || '';
  $('.s-path', box).value = s.path || '';
  $('.s-hostkey', box).textContent = s.hostKey ? 'Empreinte mémorisée : ' + s.hostKey : '';
}

function openForm(job, isCopy) {
  editingId = job && !isCopy ? job.id : null;
  $('#dlg-title').textContent = isCopy ? 'Dupliquer la tâche' : job ? 'Modifier la tâche' : 'Nouvelle tâche';
  $$('fieldset.side').forEach(fs => buildEditor(fs, fs.dataset.side));
  form.name.value = job ? job.name : '';
  fillEp($('fieldset[data-side=left] .ep-editor'), job && job.left);
  fillEp($('fieldset[data-side=right] .ep-editor'), job && job.right);
  const o = job ? job.options : { mode: 'two-way', exclude: [], useTrash: true, conflictAsk: true, conflictDefault: 'newer',
    excludeHidden: true, excludeSystem: true, excludeTemp: true };
  form.conflictDefault.value = o.conflictDefault || 'newer';
  form.conflictAsk.checked = !!o.conflictAsk;
  $$('input[name=mode]', form).forEach(r => r.checked = r.value === o.mode);
  form.exclude.value = (o.exclude || []).join('\n');
  form.include.value = (o.include || []).join('\n');
  for (const k of ['excludeEmptyDirs', 'excludeHidden', 'excludeSystem', 'excludeTemp']) form[k].checked = !!o[k];
  form.useTrash.checked = !!o.useTrash;
  form.allowEmptySide.checked = !!o.allowEmptySide;
  const am = job ? job.autoMinutes || 0 : 0;
  form.periodic.checked = am > 0;
  form.autoH.value = am ? Math.floor(am / 60) : 1; form.autoM.value = am % 60;
  form.onChange.checked = !!(job && job.onChange);
  form.changeDelay.value = (job && job.changeDelay) || 10;
  form.onStart.checked = !!(job && job.onStart);
  form.onLogoff.checked = !!(job && job.onLogoff);
  form.autoClear.checked = !!(job && job.autoClear);
  form.maxChanges.value = o.maxChanges || 0;
  form.copyCreated.checked = !!o.copyCreated;
  form.copyAttrs.checked = !!o.copyAttrs;
  form.fixDates.checked = !o.keepDates;
  form.maxKBps.value = o.maxKBps || 0;
  form.parallel.value = o.parallel || 0;
  form.copyOrder.value = o.copyOrder || '';
  // 0 enregistré = valeur par défaut (60 s) ; négatif = jamais
  form.retrySeconds.value = o.retrySeconds < 0 ? 0 : (o.retrySeconds || 60);
  form.syncOnPlug.checked = !!(job && job.syncOnPlug);
  if (isCopy) {
    // les mots de passe ne sont jamais renvoyés en clair : à ressaisir pour la copie
    $$('.s-pass, .s-keypass', form).forEach(i => { if (i.value) { i.value = ''; i.placeholder = 'Ressaisir le mot de passe'; } });
  }
  toggleConflictOpts();
  $('#form-error').hidden = true;
  dlg.showModal();
}
$('#btn-new').onclick = () => openForm(null);
// bandeau fermé : il ne revient qu'à la prochaine analyse de la tâche
const datesDismissed = {};
$('#dates-close').onclick = () => {
  const j = jobs.find(x => x.id === selected);
  if (j) datesDismissed[j.id] = String(j.status.startedAt);
  $('#dates-bar').hidden = true;
};
$('#dates-fix').onclick = async () => {
  const j = jobs.find(x => x.id === selected);
  if (!j) return;
  try {
    await api('POST', `/api/jobs/${j.id}/fix-dates`);
    $('#dates-bar').hidden = true;
    // résultat affiché dès la fin (souvent immédiate quand il y a peu de fichiers)
    const wait = async () => {
      await refresh();
      const k = jobs.find(x => x.id === j.id);
      if (k && isBusy(k)) return setTimeout(wait, 700);
      if (k) toast(k.status.message);
    };
    wait();
  } catch (e) { alertBox(e.message); }
};
$('#dates-show').onclick = () => { // les fichiers identiques, où les dates décalées sont signalées « ±1 s »
  cardFilter = 'datefix';
  (sameRows ? Promise.resolve() : loadSame()).then(() => { renderActions(true); $('#expand-all').onclick(); });
};
// double-clic sur l'en-tête ou la configuration (côtés gauche / droit) : modifier la tâche
function editSelected(e) {
  if (e.target.closest('button, a, input, select')) return;
  const j = jobs.find(x => x.id === selected);
  if (!j) return;
  if (isBusy(j)) {
    toast('Tâche en cours : modification possible une fois terminée.');
    return;
  }
  window.getSelection()?.removeAllRanges();
  openForm(j);
}
$('#job-sides').addEventListener('dblclick', editSelected);
$('.job-head').addEventListener('dblclick', editSelected);
$('#job-sides').title = 'Double-clic : modifier la tâche';
$('#btn-new-group').onclick = newGroup;
$('#btn-new-2').onclick = () => openForm(null);
// la rubrique « Conflits » ne concerne que le mode bidirectionnel
function toggleConflictOpts() {
  const m = $('input[name=mode]:checked', form);
  $('#conflict-opts').hidden = !m || m.value !== 'two-way';
}
$$('input[name=mode]', form).forEach(r => r.addEventListener('change', toggleConflictOpts));
$('#btn-form-cancel').onclick = () => dlg.close();

form.onsubmit = async ev => {
  ev.preventDefault();
  const job = {
    name: form.name.value.trim(),
    left: readEp($('fieldset[data-side=left] .ep-editor')),
    right: readEp($('fieldset[data-side=right] .ep-editor')),
    options: {
      mode: $('input[name=mode]:checked', form).value,
      exclude: form.exclude.value.split('\n').map(s => s.trim()).filter(Boolean),
      include: form.include.value.split('\n').map(s => s.trim()).filter(Boolean),
      excludeEmptyDirs: form.excludeEmptyDirs.checked,
      excludeHidden: form.excludeHidden.checked,
      excludeSystem: form.excludeSystem.checked,
      excludeTemp: form.excludeTemp.checked,
      useTrash: form.useTrash.checked,
      conflictDefault: form.conflictDefault.value,
      conflictAsk: form.conflictAsk.checked,
      allowEmptySide: form.allowEmptySide.checked,
      maxKBps: Math.max(0, parseInt(form.maxKBps.value, 10) || 0),
      parallel: Math.max(0, parseInt(form.parallel.value, 10) || 0),
      copyOrder: form.copyOrder.value,
      retrySeconds: (v => v > 0 ? v : -1)(parseInt(form.retrySeconds.value, 10) || 0),
      maxChanges: Math.max(0, parseInt(form.maxChanges.value, 10) || 0),
      copyCreated: form.copyCreated.checked,
      copyAttrs: form.copyAttrs.checked,
      keepDates: !form.fixDates.checked,
    },
    autoMinutes: form.periodic.checked ? Math.max(1, (parseInt(form.autoH.value, 10) || 0) * 60 + (parseInt(form.autoM.value, 10) || 0)) : 0,
    syncOnPlug: form.syncOnPlug.checked,
    onChange: form.onChange.checked,
    changeDelay: Math.max(1, parseInt(form.changeDelay.value, 10) || 10),
    onStart: form.onStart.checked,
    onLogoff: form.onLogoff.checked,
    autoClear: form.autoClear.checked,
  };
  // Corbeille désactivée alors qu'elle contient des sauvegardes : on prévient et on
  // propose de les supprimer.
  let emptyTrash = false;
  const before = editingId && jobs.find(x => x.id === editingId);
  if (before && before.options.useTrash && !job.options.useTrash) {
    const info = await api('GET', `/api/jobs/${editingId}/trash`).catch(() => []);
    const full = info.filter(t => t.files > 0);
    if (full.length) {
      const files = full.reduce((n, t) => n + t.files, 0), size = full.reduce((n, t) => n + t.bytes, 0);
      $('#trash-text').textContent = `La corbeille ${'.synkro-trash'} contient ${files} fichier(s) sauvegardé(s), ${bytes(size)} `
        + `(${full.map(t => `côté ${t.side} : ${t.files}`).join(', ')}). Voulez-vous supprimer ces sauvegardes ?`;
      const choice = await askTrash();
      if (choice === 'back') return;
      emptyTrash = choice === 'empty';
    }
  }
  // conserver l'empreinte connue (le serveur la gère, on ne l'efface pas)
  try {
    if (emptyTrash) toast((await api('POST', `/api/jobs/${editingId}/trash/empty`)).message);
    const saved = editingId ? await api('PUT', `/api/jobs/${editingId}`, job) : await api('POST', '/api/jobs', job);
    dlg.close(); selected = saved.id; localStorage.setItem('synkro-selected', saved.id);
    await refresh(); renderJob(true);
  } catch (e) {
    $('#form-error').textContent = e.message; $('#form-error').hidden = false;
  }
};

// Fenêtre « Désactiver la corbeille ? » : renvoie "back", "keep" ou "empty".
function askTrash() {
  const d = $('#dlg-trash');
  return new Promise(resolve => {
    $$('button', d).forEach(b => b.onclick = () => { d.close(); resolve(b.dataset.v); });
    d.oncancel = () => resolve('back');
    d.showModal();
  });
}

// ---------- Sélecteur de dossier (navigateur) ----------
// Fenêtre « Choisir un dossier ». lister(p) renvoie { path, parent, dirs, links, note, error } ;
// par défaut, les dossiers de cet ordinateur. sep : séparateur des chemins affichés.
function browseFolder(start, lister, title) {
  const d = $('#dlg-browse');
  lister = lister || (p => api('GET', '/api/browse?path=' + encodeURIComponent(p || '')));
  $('h2', d).textContent = title || 'Choisir un dossier';
  return new Promise(resolve => {
    let cur = start, seq = 0;
    async function load(p) {
      const my = ++seq;
      const ul = $('#browse-list');
      ul.replaceChildren(h('li', { class: 'note' }, 'Lecture…'));
      try {
        const r = await lister(p);
        if (my !== seq) return; // une lecture plus récente a été demandée
        cur = r.path; $('#browse-path').value = r.path;
        $('#browse-up').onclick = () => load(r.parent);
        ul.replaceChildren();
        const links = new Set(r.links || []);
        const sep = r.path.includes('\\') ? '\\' : '/';
        const join = name => r.path.replace(/[\\/]$/, '') + sep + name;
        if (r.note) ul.append(h('li', { class: 'note' }, r.note));
        for (const name of r.dirs) {
          ul.append(h('li', { ondblclick: () => load(join(name)), onclick: e => {
            $$('#browse-list li').forEach(x => x.style.fontWeight = ''); e.currentTarget.style.fontWeight = '600';
            $('#browse-path').value = join(name);
          }, title: links.has(name) ? 'Jonction / lien vers un autre dossier' : '' }, (links.has(name) ? '🔗 ' : '📁 ') + name));
        }
        if (!r.dirs.length) ul.append(h('li', { class: 'note' }, 'Aucun sous-dossier.'));
        if (r.error) ul.append(h('li', { class: 'note err' }, 'Lecture incomplète : ' + r.error));
      } catch (e) {
        if (my !== seq) return;
        $('#browse-path').value = p || '';
        ul.replaceChildren(h('li', { class: 'note err' }, 'Impossible d\'ouvrir ce dossier : ' + e.message));
      }
    }
    $('#browse-path').onkeydown = e => { if (e.key === 'Enter') load($('#browse-path').value); };
    $('#browse-ok').onclick = () => { seq++; d.close(); resolve($('#browse-path').value || cur); };
    $('#browse-cancel').onclick = () => { seq++; d.close(); resolve(null); };
    d.oncancel = () => { seq++; resolve(null); };
    load(start); d.showModal();
  });
}

// double-clic sur une ligne de la liste : détail (le bouton de sens garde son propre rôle)
$('#actions-body').addEventListener('dblclick', e => {
  if (e.target.closest('.dircol, .dirbtn, .twist')) return;
  const tr = e.target.closest('tr[data-path]');
  if (!tr) return;
  clearTimeout(foldTimer); // le double-clic ne replie / déplie pas le dossier
  window.getSelection && window.getSelection().removeAllRanges();
  pickRow(tr.dataset.path);
});

// ---------- Thème : système, clair ou sombre ----------
const darkMQ = window.matchMedia('(prefers-color-scheme: dark)');
let themePref = (() => { try { return localStorage.getItem('synkro-theme') || 'system'; } catch { return 'system'; } })();
function applyTheme() {
  const dark = themePref === 'dark' || (themePref === 'system' && darkMQ.matches);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
}
darkMQ.addEventListener('change', applyTheme);
applyTheme();

// ---- Options générales ----
let settings = {};
let sysInfo = {};
function lsSet(k, v) { try { localStorage.setItem(k, v); } catch {} }
function syncLocalSettings(st) {
  // le réglage enregistré côté moteur fait foi (le navigateur l'oublie si le port change)
  settings = st;
  if (st.theme && st.theme !== themePref) { themePref = st.theme; lsSet('synkro-theme', themePref); applyTheme(); }
  if (st.units && st.units !== sizeUnits) { sizeUnits = st.units; lsSet('synkro-units', sizeUnits); refreshAll(); }
  if (st.lang && st.lang !== LANG) { lsSet('synkro-lang', st.lang); location.reload(); }
}
function refreshAll() { try { renderList(); refresh(); renderActions(); } catch {} }
api('GET', '/api/settings').then(syncLocalSettings).catch(() => {});
api('GET', '/api/info').then(i => {
  sysInfo = i;
  // tout premier lancement (pas encore de settings.json) : on présente les Options
  if (i.firstRun) setTimeout(() => { if (!$('#dlg-options').open) $('#btn-options').click(); }, 300);
}).catch(() => {});
// Options : les changements restent en attente (optPending) jusqu'à « Enregistrer » ;
// « Annuler » (ou Échap) les abandonne.
let optPending = {};
const OPT_DEF = { units: 'kb', theme: 'system', taskbar: 'on', tray: 'off', closeToTray: 'off', vssLocked: 'off', vssSnapshot: 'off', logs: 'off', logLevel: 'info' };
const optSaved = k => settings[k] || OPT_DEF[k];
const optVal = k => k in optPending ? optPending[k] : optSaved(k);
function setOpt(k, v) {
  if (v === optSaved(k)) delete optPending[k]; else optPending[k] = v;
  fillOptions();
}
function fillOptions() {
  const on = k => optVal(k) === 'on';
  $('#opt-units').value = optVal('units');
  $('#opt-theme').value = optVal('theme');
  $('#opt-taskbar').checked = on('taskbar');
  $('#opt-tray').checked = on('tray') || on('closeToTray') || !on('taskbar');
  $('#opt-tray').disabled = on('closeToTray') || !on('taskbar');
  $('#opt-closeToTray').checked = on('closeToTray');
  $('#opt-vssLocked').checked = on('vssLocked');
  $('#opt-vssSnapshot').checked = on('vssSnapshot');
  // journal : désactivé par défaut, niveau « normal » par défaut
  $('#opt-logs').checked = on('logs');
  $('#opt-logLevel').value = optVal('logLevel');
  $('#opt-logs').disabled = !!sysInfo.debugForced;
  $('#opt-logLevel').disabled = !!sysInfo.debugForced || !on('logs');
  if (sysInfo.debugForced) { $('#opt-logs').checked = true; $('#opt-logLevel').value = 'debug'; }
  $('#opt-log-note').textContent = sysInfo.debugForced
    ? '⚠ Journal détaillé imposé par le fichier « debug » placé à côté de Synkro.exe : supprimez ce fichier pour utiliser ces réglages.'
    : !on('logs') ? 'Aucun journal n\'est écrit (le journal de chaque tâche, onglet « Journal », est toujours tenu ; les plantages restent notés dans crash.log).'
    : 'Le journal de chaque tâche (onglet « Journal ») est toujours tenu, quel que soit ce réglage.';
  const win = sysInfo.os === 'windows';
  $$('#dlg-options .win-only').forEach(f => f.classList.toggle('disabled', !win));
  $$('#dlg-options .win-only input, #dlg-options .win-only button').forEach(x => { if (!win) x.disabled = true; });
  $('#opt-tray-note').textContent = !win ? 'Disponible seulement sous Windows.'
    : !sysInfo.window ? 'Ces réglages concernent la fenêtre Synkro (sans effet dans le navigateur).'
    : $('#opt-tray').disabled ? 'L\'icône près de l\'horloge reste affichée : c\'est le seul moyen de rouvrir Synkro.' : '';
  $('#opt-admin-note').textContent = !win ? 'Disponible seulement sous Windows.'
    : sysInfo.admin ? 'Synkro est lancé en administrateur : les instantanés sont possibles.'
    : '⚠ Volume Shadow Copy demande de lancer Synkro en tant qu\'administrateur (clic droit sur Synkro.exe › Exécuter en tant qu\'administrateur). Sinon, les fichiers verrouillés restent en échec.';
  updateOptClose();
}
$('#btn-options').onclick = async () => {
  try { settings = await api('GET', '/api/settings'); sysInfo = await api('GET', '/api/info'); } catch {}
  optPending = {};
  $('#opt-lang').value = LANG;
  fillOptions();
  $('#opt-shortcut-msg').textContent = '';
  if (sysInfo.os === 'windows') refreshShortcuts();
  $('#dlg-options').showModal();
};
// langue : appliquée à l'enregistrement (l'interface est rechargée)
function langPending() { return $('#opt-lang').value !== LANG; }
function optDirty() { return langPending() || Object.keys(optPending).length > 0; }
function updateOptClose() {
  const dirty = optDirty();
  $('#opt-close').textContent = dirty ? 'Enregistrer' : 'Fermer';
  $('#opt-cancel').hidden = !dirty;
  $('#opt-lang-note').hidden = !langPending();
}
$('#opt-close').onclick = async () => {
  if (!optDirty()) { $('#dlg-options').close(); return; }
  const ch = { ...optPending };
  if (langPending()) ch.lang = $('#opt-lang').value;
  try {
    const st = await api('PUT', '/api/settings', ch);
    optPending = {};
    if (ch.lang) {
      if (sysInfo.firstRun) await api('POST', '/api/first-run-done').catch(() => {}); // ne pas les rouvrir après le rechargement
      lsSet('synkro-lang', ch.lang); location.reload(); return;
    }
    syncLocalSettings(st);
    $('#dlg-options').close();
    toast('Options enregistrées.');
  } catch (e) { toast(e.message); }
};
$('#opt-cancel').onclick = () => $('#dlg-options').close();
$('#opt-lang').onchange = updateOptClose;
// fermée sans enregistrer (Annuler, Échap) : les changements sont abandonnés
$('#dlg-options').addEventListener('close', () => {
  optPending = {}; $('#opt-lang').value = LANG; updateOptClose();
  if (sysInfo.firstRun) { sysInfo.firstRun = false; api('POST', '/api/first-run-done').catch(() => {}); }
});
$('#opt-units').onchange = e => setOpt('units', e.target.value);
$('#opt-theme').onchange = e => setOpt('theme', e.target.value);
$('#opt-logLevel').onchange = e => setOpt('logLevel', e.target.value);
for (const k of ['taskbar', 'tray', 'closeToTray', 'vssLocked', 'vssSnapshot', 'logs']) $('#opt-' + k).onchange = e => setOpt(k, e.target.checked ? 'on' : 'off');
// raccourcis bureau / menu Démarrer : « Ajouter… » ou « Supprimer… » selon qu'il existe
const SC = { desktop: ['#opt-shortcut', 'Ajouter un raccourci sur le bureau', 'Supprimer le raccourci du bureau'],
  startmenu: ['#opt-startmenu', 'Ajouter au menu Démarrer', 'Retirer du menu Démarrer'] };
const scExists = {};
async function refreshShortcuts() {
  for (const where of Object.keys(SC)) {
    const [sel, add, del] = SC[where];
    try { scExists[where] = (await api('GET', '/api/shortcut?where=' + where)).exists; } catch { scExists[where] = false; }
    $(sel).textContent = scExists[where] ? del : add;
    $(sel).classList.toggle('danger-text', !!scExists[where]);
  }
}
async function toggleShortcut(where) {
  const m = $('#opt-shortcut-msg'); m.textContent = '…';
  try {
    if (scExists[where]) { const r = await api('DELETE', '/api/shortcut?where=' + where); m.textContent = '✓ Raccourci supprimé : ' + r.path; }
    else { const r = await api('POST', '/api/shortcut?where=' + where); m.textContent = '✓ Raccourci créé : ' + r.path; }
  } catch (e) { m.textContent = '✗ ' + e.message; }
  refreshShortcuts();
}
$('#opt-shortcut').onclick = () => toggleShortcut('desktop');
$('#opt-startmenu').onclick = () => toggleShortcut('startmenu');

// automatismes d'une tâche, en résumé
function autoLabel(j) {
  const a = [];
  if (j.autoMinutes) a.push(j.autoMinutes % 60 ? `toutes les ${Math.floor(j.autoMinutes / 60) ? Math.floor(j.autoMinutes / 60) + ' h ' : ''}${j.autoMinutes % 60} min` : `toutes les ${j.autoMinutes / 60} h`);
  if (j.onChange) a.push('sur changement');
  if (j.syncOnPlug) a.push('à la connexion');
  if (j.onStart) a.push('au démarrage');
  if (j.onLogoff) a.push('à la fermeture de session');
  return a.length ? ' · auto : ' + a.join(', ') : '';
}

// ---------- Largeur de la barre latérale (poignée à tirer, double-clic = largeur d'origine) ----------
const SIDE_DEFAULT = 260;
function setSideWidth(px, save) {
  px = Math.max(180, Math.min(700, Math.round(px)));
  document.documentElement.style.setProperty('--side-w', px + 'px');
  try { localStorage.setItem('synkro-side', px); } catch {}
  if (save) api('PUT', '/api/settings', { sideWidth: String(px) }).catch(() => {});
}
try { const w = +localStorage.getItem('synkro-side'); if (w) setSideWidth(w); } catch {}
api('GET', '/api/settings').then(s => { if (+s.sideWidth) setSideWidth(+s.sideWidth); }).catch(() => {});
$('#side-resize').addEventListener('pointerdown', e => {
  e.preventDefault();
  const grip = e.currentTarget; grip.setPointerCapture(e.pointerId); grip.classList.add('on');
  document.body.classList.add('resizing');
  const move = ev => setSideWidth(ev.clientX);
  const up = ev => {
    grip.removeEventListener('pointermove', move); grip.removeEventListener('pointerup', up);
    grip.classList.remove('on'); document.body.classList.remove('resizing');
    setSideWidth(ev.clientX, true);
  };
  grip.addEventListener('pointermove', move); grip.addEventListener('pointerup', up);
});
$('#side-resize').ondblclick = () => setSideWidth(SIDE_DEFAULT, true);

// ---------- Démarrage ----------
api('GET', '/api/info').then(i => {
  $('#sidebar-foot').textContent = `v${i.version} (${i.build}) · données : ${i.dataDir}`;
  if (i.warning) alertBox('La liste des jobs n\'a pas pu être lue : ' + i.warning + '. Remettez le settings.json d\'origine à côté pour la récupérer.');
}).catch(() => {});
refresh();
setInterval(() => {
  const j = jobs.find(x => x.id === selected);
  const busy = isBusy(j);
  refresh();
  if (busy && tab === 'log') loadLog();
}, 1000);

// ---------- Résolution des conflits ----------
let cfShownFor = null;
const fmtWhen = d => new Date(d).toLocaleString('fr-FR', { dateStyle: 'short', timeStyle: 'medium' });

async function openConflicts(j) {
  const dlg = $('#dlg-conflicts');
  const list = await api('GET', `/api/jobs/${j.id}/conflicts`).catch(() => []);
  if (!list.length) return;
  const choice = new Map(list.map(c => [c.path, c.default]));
  const rule = (j.options.conflictDefault || 'newer');
  const ruleLabel = { newer: 'la plus récente', left: 'celle de gauche', right: 'celle de droite' }[rule];
  $('#cf-intro').textContent = `${list.length} fichier(s) ont changé des deux côtés. Pour chacun, choisissez la version à garder : `
    + `elle remplacera l'autre (l'ancienne version va dans la corbeille si l'option est activée). Choix proposé par défaut : ${ruleLabel}.`;

  const newer = c => (!c.left.exists ? 'right' : !c.right.exists ? 'left'
    : new Date(c.right.modTime) > new Date(c.left.modTime) ? 'right' : 'left');

  function version(c, side) {
    const v = c[side];
    const ch = choice.get(c.path);
    const cls = ch === 'leave' ? '' : ch === side ? 'kept' : 'lost';
    if (!v.exists) return h('div', { class: 'cf-ver gone ' + cls }, 'supprimé de ce côté');
    const other = c[side === 'left' ? 'right' : 'left'];
    const tags = [];
    if (other.exists && new Date(v.modTime) > new Date(other.modTime)) tags.push('plus récent');
    if (other.exists && v.size > other.size) tags.push('plus gros');
    return h('div', { class: 'cf-ver ' + cls },
      h('div', {}, h('b', {}, bytes(v.size)), tags.map(t => h('span', { class: 'tag' }, t))),
      h('div', { class: 'muted' }, fmtWhen(v.modTime)));
  }

  function render() {
    const body = $('#cf-body'); body.replaceChildren();
    for (const c of list) {
      const ch = choice.get(c.path);
      const pick = (val, label, extra = '') => h('button', {
        type: 'button', class: (ch === val ? 'on ' : '') + extra,
        title: { left: 'Garder la version de gauche', right: 'Garder la version de droite', leave: 'Ne rien faire pour ce fichier' }[val],
        onclick: () => { choice.set(c.path, val); render(); },
      }, label);
      body.append(h('tr', {},
        h('td', { class: 'path' }, c.path),
        h('td', {}, version(c, 'left')),
        h('td', { class: 'center' }, h('span', { class: 'cf-pick' },
          pick('left', '← Gauche'), pick('leave', 'Rien', 'leave'), pick('right', 'Droite →'))),
        h('td', {}, version(c, 'right'))));
    }
    const n = { left: 0, right: 0, leave: 0 };
    choice.forEach(v => n[v]++);
    $('#cf-sum').textContent = `${n.left} version(s) de gauche, ${n.right} version(s) de droite, ${n.leave} laissé(s) tel(s) quel(s). `
      + 'Les autres fichiers (sans conflit) seront synchronisés normalement.';
  }

  $$('.cf-tools [data-all]').forEach(b => b.onclick = () => {
    for (const c of list) choice.set(c.path, b.dataset.all === 'newer' ? newer(c) : b.dataset.all);
    render();
  });
  $('#cf-ok').onclick = async () => {
    dlg.close();
    try { await api('POST', `/api/jobs/${j.id}/resolve`, { choices: Object.fromEntries(choice) }); } catch (e) { alertBox(e.message); }
    setTimeout(refresh, 150);
  };
  $('#cf-abort').onclick = async () => {
    dlg.close();
    try { await api('POST', `/api/jobs/${j.id}/resolve`, { abort: true }); } catch (e) { alertBox(e.message); }
    setTimeout(refresh, 150);
  };
  render();
  if (!dlg.open) dlg.showModal();
}

// ---------- Raccourcis clavier ----------
$('#dlg-cancel').addEventListener('keydown', e => {
  const k = e.key.toLowerCase();
  if (k === 'o') { e.preventDefault(); $('#cancel-next').click(); }
  else if (k === 'n') { e.preventDefault(); $('#cancel-keep').click(); }
  else if (k === 't' && !$('#cancel-all').hidden) { e.preventDefault(); $('#cancel-all').click(); }
});
$('#cancel-keep').dataset.key = 'N / Échap';
const saveBtn = $('#dlg-job button[type=submit]'); if (saveBtn) saveBtn.dataset.key = 'Ctrl S';
$('#cancel-next').dataset.key = 'O';
$('#cancel-all').dataset.key = 'T';
function showKeys() { if (!$('#dlg-keys').open) $('#dlg-keys').showModal(); }
$('#keys-close').onclick = () => $('#dlg-keys').close();
$('#opt-keys').onclick = () => { $('#dlg-options').close(); showKeys(); };
// raccourci affiché à côté du bouton (::after, garde son texte même s'il change)
const KEY_TIPS = { 'btn-analyze': 'F5', 'btn-sync': 'F6', 'btn-pause': 'F8', 'btn-cancel': 'Échap',
  'expand-all': 'Ctrl +', 'collapse-all': 'Ctrl -', 'show-same': 'Ctrl U', 'btn-new': 'Ctrl N', 'btn-new-group': 'Ctrl G', 'btn-options': 'Ctrl ,' };
for (const [id, k] of Object.entries(KEY_TIPS)) {
  const el = document.getElementById(id);
  if (el) { el.dataset.key = window.tr ? window.tr(k) : k; el.title = (el.title ? el.title + ' · ' : '') + 'Raccourci : ' + k; }
}
$('#filter').placeholder = 'Filtrer par chemin… (Ctrl F)';
function stepTask(d) {
  const ids = [...new Set($$('#job-list li[data-id]').map(li => li.dataset.id))];
  if (!ids.length) return;
  const i = ids.indexOf(selected);
  const n = ids[Math.max(0, Math.min(ids.length - 1, i < 0 ? 0 : i + d))];
  if (n && n !== selected) { select(n); $(`#job-list li[data-id="${CSS.escape(n)}"]`)?.scrollIntoView({ block: 'nearest' }); }
}
const clickIf = id => { const b = document.getElementById(id); if (b && !b.hidden && !b.disabled) b.click(); };
document.addEventListener('keydown', e => {
  const k = e.key, ctrl = e.ctrlKey || e.metaKey;
  const typing = !!e.target.closest('input, textarea, select, [contenteditable]');
  const anyDialog = $$('dialog').some(d => d.open);
  if (k === 'F1' || (k === '?' && !typing && !anyDialog)) { e.preventDefault(); showKeys(); return; }
  // touches du navigateur à neutraliser (F5 recharge la page, Ctrl+F ouvre sa recherche…)
  if (['F5', 'F6', 'F8', 'F2'].includes(k) || (ctrl && ['r', 'f', 'j', 'u', 'n', 'g', ','].includes(k.toLowerCase()))) e.preventDefault();
  if (anyDialog) {
    // fenêtre ouverte : Ctrl+S ou Ctrl+Entrée = son bouton principal (Enregistrer, Valider…)
    const dlg = $$('dialog').filter(d => d.open).pop();
    if (ctrl && (k.toLowerCase() === 's' || k === 'Enter')) {
      const btn = dlg && [...dlg.querySelectorAll('.dlg-buttons button.primary, button[type=submit]')].filter(b => !b.hidden && !b.disabled).pop();
      if (btn) { e.preventDefault(); btn.click(); }
    }
    return; // les autres touches restent celles de la fenêtre
  }
  const j = groupView === null ? jobs.find(x => x.id === selected) : null; // vue d'un groupe : pas de tâche visée
  if (ctrl && k.toLowerCase() === 'n') return clickIf('btn-new');
  if (ctrl && k === ',') return clickIf('btn-options');
  if (ctrl && k.toLowerCase() === 'g') return clickIf('btn-new-group');
  if (!j) return;
  if (k === 'F5' || (ctrl && k.toLowerCase() === 'r')) return clickIf('btn-analyze');
  if (k === 'F6' || (ctrl && k === 'Enter')) { e.preventDefault(); return clickIf('btn-sync'); }
  if (k === 'F8') return clickIf('btn-pause');
  if (k === 'F2') return editSelected({ target: document.body });
  // la même touche Échap fermerait aussitôt la fenêtre de confirmation : on l'ouvre juste après
  if (k === 'Escape' && !typing && isBusy(j)) { e.preventDefault(); setTimeout(() => clickIf('btn-cancel'), 0); return; }
  if (ctrl && k === 'ArrowUp') { e.preventDefault(); return stepTask(-1); }
  if (ctrl && k === 'ArrowDown') { e.preventDefault(); return stepTask(1); }
  if (ctrl && k.toLowerCase() === 'f') { $('#filter').focus(); $('#filter').select(); return; }
  if (ctrl && k.toLowerCase() === 'j') { const t = $$('.tabs button').find(b => !b.classList.contains('active')); if (t) t.click(); return; }
  if (ctrl && k.toLowerCase() === 'u') return clickIf('show-same');
  if (ctrl && (k === '+' || k === '=')) { e.preventDefault(); return clickIf('expand-all'); }
  if (ctrl && (k === '-' || k === '_')) { e.preventDefault(); return clickIf('collapse-all'); }
  if (k === 'Escape' && typing && e.target.id === 'filter') { e.target.value = ''; e.target.dispatchEvent(new Event('input')); e.target.blur(); }
});

// Vue d'un groupe : largeur des colonnes réglable (bord de l'en-tête), mémorisée ;
// double-clic sur le bord : ajustée au contenu. La colonne Actions prend le reste.
const GV_COLS_DEF = [280, 80, 420, 160, 300];
function gvCols() {
  try { const w = JSON.parse(localStorage.getItem('synkro-gv-cols')); if (Array.isArray(w) && w.length === 5) return w; } catch (e) { }
  return GV_COLS_DEF.slice();
}
function applyGvCols(w) {
  const cols = $$('.gv-table col');
  w.forEach((px, i) => cols[i].style.width = px + 'px');
  $('.gv-table').style.minWidth = (w.reduce((a, b) => a + b, 0) + 200) + 'px'; // place pour les boutons
}
function saveGvCols(w) { try { localStorage.setItem('synkro-gv-cols', JSON.stringify(w)); } catch (e) { } }
(function initGvCols() {
  applyGvCols(gvCols());
  $$('.gv-table thead th').forEach((th, i) => {
    if (i > 4) return;
    const g = h('span', { class: 'grip', title: 'Tirer pour élargir · double-clic : ajuster au contenu' });
    th.append(g);
    g.addEventListener('click', e => e.stopPropagation());
    g.addEventListener('dblclick', e => {
      // largeur du contenu le plus large de la colonne (en-tête compris)
      e.stopPropagation();
      let need = contentWidth(th) + 10;
      for (const tr of $('#gv-body').children) { const td = tr.children[i]; if (td && td.colSpan === 1) need = Math.max(need, contentWidth(td)); }
      const w = gvCols(); w[i] = Math.max(50, Math.ceil(need) + 6); applyGvCols(w); saveGvCols(w);
    });
    g.addEventListener('mousedown', e => {
      e.preventDefault(); e.stopPropagation();
      const w = gvCols(), x0 = e.clientX, w0 = w[i];
      g.classList.add('on'); document.body.classList.add('resizing');
      const move = ev => { w[i] = Math.max(50, Math.round(w0 + ev.clientX - x0)); applyGvCols(w); };
      const up = () => {
        document.removeEventListener('mousemove', move); document.removeEventListener('mouseup', up);
        g.classList.remove('on'); document.body.classList.remove('resizing'); saveGvCols(w);
      };
      document.addEventListener('mousemove', move); document.addEventListener('mouseup', up);
    });
  });
})();
