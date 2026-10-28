// TaptapBox WebUI — ala TaptapBox APK
const $ = s => document.querySelector(s);
const $$ = s => [...document.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const S = { servers: [], subs: [], settings: {}, builder: {}, status: {}, view: 'servers', search: '' };
S.tab = localStorage.getItem('nb_tab') || 'server'; // 'server' | 'dash'

async function api(path, method = 'GET', body) {
  const o = { method, headers: {} };
  if (body !== undefined) { o.headers['Content-Type'] = 'application/json'; o.body = JSON.stringify(body); }
  const r = await fetch(path, o);
  const t = await r.text();
  let j; try { j = JSON.parse(t); } catch { throw new Error('Respon bukan JSON'); }
  if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
  return j;
}
function toast(m) { const t = $('#toast'); t.textContent = m; t.classList.remove('hidden'); clearTimeout(t._h); t._h = setTimeout(() => t.classList.add('hidden'), 2200); }
// coreName: nama core aktif untuk pemberitahuan — 'sing-box' bila
// core sing-box, 'mihomo' bila core mihomo. Jangan hardcode 'mihomo'
// di pemberitahuan generik (restart, simpan, validasi), karena saat
// sing-box aktif pemberitahuan itu salah dan membingungkan.
function coreName() { return 'sing-box'; }
function modal(title, html) { $('#modalTitle').textContent = title; $('#modalBody').innerHTML = html; $('#modal').classList.remove('hidden'); }
$('#modalX').onclick = () => $('#modal').classList.add('hidden');

async function reload() {
  const [st, sv, sub, set, bld] = await Promise.all([
    api('/api/status'), api('/api/servers'), api('/api/subscriptions'), api('/api/settings'), api('/api/builder'),
  ]);
  S.status = st; S.servers = sv || []; S.subs = sub || []; S.settings = set; S.builder = bld || {};
  $('#dver').textContent = st.version || '';
  syncFab();
  applyBg();
  // v1.0.49: pulihkan animasi bila auto-switch tertinggal 0 (mis. reload/reboot
  // saat stream mati tanpa sempat restore); dilewati bila stream aktif (flag).
  if (!scBgOff && (S.settings.animasi_bg || 0) === 0) {
    const stored = scBgStoredPrev();
    if (stored) {
      api('/api/settings', 'PUT', { animasi_bg: stored }).then(() => { S.settings.animasi_bg = stored; scBgStorePrev(0); applyBg(); }).catch(() => {});
    }
  }
}
function syncFab() {
  const f = $('#fab'), on = S.status.running;
  f.classList.toggle('on', on);
  f.innerHTML = on ? '<svg class="ic" viewBox="0 0 24 24"><rect x="6.5" y="5" width="4" height="14" rx="1.4"/><rect x="13.5" y="5" width="4" height="14" rx="1.4"/></svg>' : '<svg class="ic" viewBox="0 0 24 24"><path d="M8 5.6v12.8a1 1 0 0 0 1.5.86l10.2-6.4a1 1 0 0 0 0-1.72L9.5 4.74A1 1 0 0 0 8 5.6z"/></svg>';
  f.title = on ? 'Disconnect' : 'Connect';
}
$('#fab').onclick = async () => {
  try {
    if (S.status.running) { await api('/api/disconnect', 'POST'); toast('Terputus'); }
    else { toast('Menghubungkan…'); await api('/api/connect', 'POST'); toast('Terhubung'); }
    await reload(); render();
  } catch (e) { toast(e.message); }
};

// ---------- drawer & nav ----------
const TITLES = { servers: 'TaptapBox', groups: 'Grup', routes: 'Rute', settings: 'Pengaturan', config: 'Config', logs: 'Logs', layanan: 'Layanan', scrcpy: 'Scrcpy', about: 'Tentang' };
function nav(view) {
  S.view = view; S.editing = false;
  $('#title').innerHTML = TITLES[view];
  $('#drawer').classList.remove('open'); $('#scrim').classList.add('hidden');
  $$('#drawer button').forEach(b => b.classList.toggle('on', b.dataset.view === view));
  $('#btnSearch').style.display = view === 'servers' ? '' : 'none';
  $('#btnAdd').style.display = view === 'servers' ? '' : 'none';
  $('#btnMore').style.display = view === 'servers' ? '' : 'none';
  render();
}
$('#btnDrawer').onclick = () => { $('#drawer').classList.add('open'); $('#scrim').classList.remove('hidden'); };
$('#scrim').onclick = () => { $('#drawer').classList.remove('open'); $('#scrim').classList.add('hidden'); closeMenu(); };
$$('#drawer button').forEach(b => b.onclick = () => nav(b.dataset.view));

// ---------- galeri animasi: logo TaptapBox = pemicu ----------
// File di webui/animasi/ dipanggil BERDASARKAN NOMOR (bukan nama),
// mis. "1. jaringan saraf ai.html" dipanggil sebagai nomor 1.
let aniList = null;
async function openAnimasi(n) {
  try { if (!aniList) aniList = (await api('/api/animasi')).files || []; }
  catch (e) { return toast(e.message); }
  if (!aniList.length) return toast('Folder animasi kosong — taruh file <nomor>.html di webui/animasi/');
  const nums0 = [0, ...aniList]; // 0 = tanpa animasi
  const applied = () => S.settings.animasi_bg || 0;
  let cur = nums0.includes(n) ? n : (nums0.includes(applied()) ? applied() : aniList[0]);
  const idx = () => nums0.indexOf(cur);
  modal('Animasi', `
    <div class="row" style="margin-bottom:8px;align-items:center">
      <button class="btn small" id="aniPrev">‹</button>
      <b id="aniNum" style="min-width:40px;text-align:center"></b>
      <button class="btn small" id="aniNext">›</button>
      <span class="sp"></span>
      <button class="btn small" id="aniClose">Tutup</button>
    </div>
    <div class="row" id="aniNums" style="margin-bottom:8px;flex-wrap:wrap"></div>
    <iframe id="aniFrame" title="animasi"></iframe>
    <div class="row" style="margin-top:10px"><button class="btn primary" id="aniApply" style="flex:1">Terapkan sbg background</button></div>
    <div class="sect">Kecerahan background</div>
    <div><label class="lbl">Transparan <span class="mut" id="aniAlphaV"></span></label><input type="range" id="aniAlpha" min="10" max="95" step="1"></div>
    <div class="hint">Background tampil di belakang seluruh WebUI. Angka <b>0</b> = tanpa animasi.</div>`);
  // ---- slider transparan (live, tersimpan otomatis) ----
  const aniAlphaEl = $('#aniAlpha');
  aniAlphaEl.value = S.settings.animasi_alpha ?? 55;
  const aniCfgLabel = () => {
    $('#aniAlphaV').textContent = aniAlphaEl.value + '%';
  };
  aniCfgLabel();
  let aniCfgT = null;
  const aniCfgSave = () => {
    clearTimeout(aniCfgT);
    aniCfgT = setTimeout(async () => {
      try {
        await api('/api/settings', 'PUT', { animasi_alpha: +aniAlphaEl.value });
        S.settings.animasi_alpha = +aniAlphaEl.value;
      } catch (e) { toast(e.message); }
    }, 600);
  };
  aniAlphaEl.oninput = () => { document.body.style.setProperty('--aniA', aniAlphaEl.value / 100); aniCfgLabel(); aniCfgSave(); };
  const nums = () => {
    $('#aniNums').innerHTML = nums0.map(x => `<button class="btn small${x === cur ? ' primary' : ''}" data-ani="${x}">${x}${x === applied() ? ' ★' : ''}</button>`).join('');
    $('#aniNums').querySelectorAll('[data-ani]').forEach(b => b.onclick = () => { cur = +b.dataset.ani; draw(); });
  };
  const draw = () => {
    $('#aniNum').textContent = cur;
    const fr = $('#aniFrame');
    if (cur === 0) {
      fr.removeAttribute('src');
      fr.srcdoc = '<div style="display:flex;height:100%;align-items:center;justify-content:center;color:#8791a3;font:14px system-ui">0 — tanpa animasi</div>';
    } else {
      fr.removeAttribute('srcdoc');
      fr.src = '/api/animasi/file?n=' + cur;
    }
    const isCur = applied() === cur;
    $('#aniApply').textContent = cur === 0 ? (isCur ? 'Tanpa animasi ✓' : 'Terapkan: tanpa animasi') : (isCur ? '✓ Terpasang — ketuk untuk lepas' : 'Terapkan sbg background');
    nums();
  };
  $('#aniPrev').onclick = () => { cur = nums0[(idx() - 1 + nums0.length) % nums0.length]; draw(); };
  $('#aniNext').onclick = () => { cur = nums0[(idx() + 1) % nums0.length]; draw(); };
  $('#aniClose').onclick = () => $('#modal').classList.add('hidden');
  $('#aniApply').onclick = async () => {
    const isCur = applied() === cur;
    const target = (isCur && cur !== 0) ? 0 : cur;
    try {
      await api('/api/settings', 'PUT', { animasi_bg: target });
      S.settings.animasi_bg = target;
      scBgStorePrev(0); // pilihan sadar menghapus simpanan auto-switch
      applyBg(); draw();
      toast(target ? 'Background animasi #' + target + ' diterapkan' : 'Background dimatikan');
    } catch (e) { toast(e.message); }
  };
  draw();
}
// v1.0.49: saat Scrcpy aktif, pindah ke tanpa-animasi ASLI: setting animasi_bg
// benar-benar diubah ke 0 (persis seperti memilih 0 di galeri), bukan sekadar
// hitam tiruan — lalu dikembalikan saat Scrcpy mati. Pilihan user tidak boleh
// hilang: prev disimpan di memori + localStorage (tahan reload/reboot HP);
// pilihan sadar lewat galeri selalu menghapus simpanan itu.
let scBgOff = false;
let scPrevBg = 0;
const SC_PREV_BG_KEY = 'taptapbox-sc-prev-bg';
function scBgStorePrev(v) {
  try {
    if (v) localStorage.setItem(SC_PREV_BG_KEY, String(v));
    else localStorage.removeItem(SC_PREV_BG_KEY);
  } catch (e) {}
}
function scBgStoredPrev() {
  try { return parseInt(localStorage.getItem(SC_PREV_BG_KEY) || '0', 10) || 0; } catch (e) { return 0; }
}
// Kembalikan animasi pilihan bila auto-switch masih berlaku (setting 0 + prev ada).
// Bila user sempat ganti animasi saat Scrcpy nyala (setting != 0), pilihan barunya
// dihormati — tidak ditimpa prev.
function scBgRestore() {
  scBgOff = false;
  const prev = scPrevBg; scPrevBg = 0;
  if ((S.settings.animasi_bg || 0) === 0 && prev) {
    api('/api/settings', 'PUT', { animasi_bg: prev }).then(() => { S.settings.animasi_bg = prev; scBgStorePrev(0); applyBg(); }).catch(() => applyBg());
  } else {
    scBgStorePrev(0);
    applyBg();
  }
}
// Background animasi di belakang WebUI (iframe fixed, transparan, non-interaktif).
function applyBg() {
  const s = S.settings || {};
  const n = scBgOff ? 0 : (s.animasi_bg || 0);
  document.body.style.setProperty('--aniA', (s.animasi_alpha ?? 55) / 100);
  let bg = document.getElementById('aniBg');
  if (!n) {
    if (bg) bg.remove();
    document.body.classList.remove('hasAniBg');
    return;
  }
  if (!bg) {
    bg = document.createElement('iframe');
    bg.id = 'aniBg';
    bg.title = 'background';
    bg.setAttribute('aria-hidden', 'true');
    document.body.prepend(bg);
  }
  if (bg.dataset.n != n) { bg.dataset.n = n; bg.src = '/api/animasi/file?n=' + n; }
  document.body.classList.add('hasAniBg');
}
document.querySelector('.dhead').onclick = () => { $('#drawer').classList.remove('open'); $('#scrim').classList.add('hidden'); openAnimasi(); };
document.querySelector('.dhead').style.cursor = 'pointer';

function render() {
  ({ servers: vServers, groups: vGroups, routes: vRoutes, settings: vSettings, config: vConfig, logs: vLogs, layanan: vLayanan, scrcpy: vScrcpy, about: vAbout }[S.view] || vServers)();
}

// Terapkan config; tampilkan peringatan (mis. rule-set gagal diunduh) bila ada.
async function doApply() {
  const r = await api('/api/apply', 'POST');
  const w = (r && r.warnings) || [];
  toast(w.length ? 'Diterapkan — ⚠ ' + w.join('; ') : 'Diterapkan');
}

// ---------- popup menu ----------
function openMenu(x, y, items) {
  const m = $('#menu');
  m.innerHTML = items.map((it, i) => it.sep ? '<div class="sep"></div>' : `<button data-i="${i}">${it.label}</button>`).join('');
  m.classList.remove('hidden');
  // muat di layar: ukur dulu, lalu geser/batasi tinggi agar tidak keluar tepi
  m.style.maxHeight = (innerHeight - 16) + 'px';
  m.style.maxWidth = (innerWidth - 16) + 'px';
  m.style.top = Math.max(8, Math.min(y, innerHeight - m.offsetHeight - 8)) + 'px';
  m.style.left = Math.max(8, Math.min(x, innerWidth - m.offsetWidth - 8)) + 'px';
  m.onclick = ev => {
    const b = ev.target.closest('[data-i]'); if (!b) return;
    closeMenu(); items[+b.dataset.i].fn && items[+b.dataset.i].fn();
  };
}
function closeMenu() { $('#menu').classList.add('hidden'); }
document.addEventListener('click', e => { if (!e.target.closest('#menu') && !e.target.closest('#btnMore') && !e.target.closest('#btnAdd')) closeMenu(); });

// ---------- search ----------
$('#btnSearch').onclick = () => { const sb = $('#searchBar'); sb.classList.toggle('hidden'); if (!sb.classList.contains('hidden')) $('#searchInput').focus(); };
$('#searchInput').oninput = e => { S.search = e.target.value.toLowerCase(); vServers(); };

// ================= SERVERS (Konfigurasi) =================
function flagOf(name) {
  const m = name.match(/🇦🇱|🇩🇿|🇦🇷|🇦🇲|🇦🇺|🇦🇹|🇦🇿|🇧🇭|🇧🇩|🇧🇾|🇧🇪|🇧🇷|🇧🇬|🇰🇭|🇨🇲|🇨🇦|🇨🇱|🇨🇳|🇨🇴|🇨🇷|🇭🇷|🇨🇺|🇨🇾|🇨🇿|🇩🇰|🇪🇬|🇪🇪|🇪🇹|🇫🇮|🇫🇷|🇬🇪|🇩🇪|🇬🇭|🇬🇷|🇭🇰|🇭🇺|🇮🇸|🇮🇳|🇮🇩|🇮🇷|🇮🇶|🇮🇪|🇮🇱|🇮🇹|🇯🇵|🇰🇿|🇰🇪|🇰🇷|🇰🇼|🇱🇻|🇱🇧|🇱🇹|🇱🇺|🇲🇾|🇲🇽|🇲🇩|🇲🇳|🇲🇦|🇲🇲|🇳🇵|🇳🇱|🇳🇿|🇳🇬|🇲🇰|🇳🇴|🇵🇰|🇵🇭|🇵🇱|🇵🇹|🇶🇦|🇷🇴|🇷🇺|🇸🇦|🇸🇬|🇿🇦|🇪🇸|🇱🇰|🇸🇪|🇨🇭|🇹🇼|🇹🇭|🇹🇷|🇺🇦|🇦🇪|🇬🇧|🇺🇸|🇺🇿|🇻🇳|🇾🇪/);
  return m ? m[0] : '🌐';
}
// Ping di daftar Server bisa ditekan, seperti di zashboard:
// ketuk angka ping (atau Timeout / -) untuk tes ulang server itu
// saja, tanpa tes semua. Tombol ini ditangani di onclick
// vServersList lewat data-act="ping" — ketuk ping TIDAK memilih
// server, hanya mengetesnya.
function statHtml(s) {
  if (s.delay_ms === -2) return '<button class="stat" data-act="ping" title="Ketuk untuk tes ulang server ini"><span class="mut">-</span></button>';
  if (s.delay_ms === -1) return '<button class="stat" data-act="ping" title="Ketuk untuk tes ulang server ini"><span class="bad">Timeout</span></button>';
  if (s.delay_ms === -3) return '<button class="stat" data-act="ping" title="Ketuk untuk tes ulang server ini"><span class="bad">Unavailable</span></button>';
  return `<button class="stat" data-act="ping" title="Ketuk untuk tes ulang server ini"><span class="ms">${s.delay_ms}ms</span></button>`;
}
function vServers() {
  if ((S.tab || 'server') === 'dash') return vServersDash();
  return vServersList();
}
function tabHtml() {
  const t = S.tab || 'server';
  return `<div class="tabs">
    <button class="tab ${t === 'server' ? 'on' : ''}" data-tab="server"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="3.5" width="16" height="7" rx="2"/><rect x="4" y="13.5" width="16" height="7" rx="2"/><path d="M8 7h.01M8 17h.01"/></svg>Server</button>
    <button class="tab ${t === 'dash' ? 'on' : ''}" data-tab="dash"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="3.5" y="3.5" width="7.5" height="7.5" rx="2"/><rect x="13" y="3.5" width="7.5" height="7.5" rx="2"/><rect x="3.5" y="13" width="7.5" height="7.5" rx="2"/><rect x="13" y="13" width="7.5" height="7.5" rx="2"/></svg>Dashboard</button>
  </div>`;
}
function onTabClick(ev) {
  const b = ev.target.closest('[data-tab]');
  if (!b) return false;
  S.tab = b.dataset.tab;
  try { localStorage.setItem('nb_tab', S.tab); } catch (e) {}
  vServers();
  return true;
}
function vServersDash() {
  const port = S.settings.clash_port || 9090;
  let hint = `<div class="hint">Clash API: 127.0.0.1:${port}${S.settings.secret ? ' (secret: ' + esc(S.settings.secret) + ')' : ' (tanpa password)'}<br>Pilih akun / grup lewat grup proksi di dashboard.</div>`;
  if (S.settings.web_listen_all) {
    hint += `<div class="hint"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="7" y="2.5" width="10" height="19" rx="2.4"/><path d="M11 18.5h2"/></svg>Dashboard dari HP/PC lain (satu hotspot/WiFi): buka <b>http://IP-HP-ini:${port}/ui/</b> — mis. <b>http://192.168.56.1:${port}/ui/</b> saat HP ini jadi hotspot.</div>`;
  }
  if (!S.status.running) {
    $('#view').innerHTML = tabHtml() + `<div class="card"><div class="mut">${isBoxCore() ? 'Dashboard zashboard via Clash API sing-box — tekan Connect dulu.' : 'sing-box belum berjalan — tekan Connect, lalu pilih akun lewat dashboard.'}</div></div>`;
  } else if (!['127.0.0.1', 'localhost', '[::1]'].includes(location.hostname) && !S.settings.web_listen_all) {
    $('#view').innerHTML = tabHtml() + `<div class="card"><div class="mut">Dashboard zashboard (port ${port}) hanya bisa dibuka dari HP ini (http://127.0.0.1). Aktifkan "Bisa dibuka dari HP lain (LAN)" di Pengaturan untuk akses dari HP lain.</div></div>` + hint;
  } else {
    // zashboard disajikan sing-box sendiri (external_ui) di port Clash API.
    $('#view').innerHTML = tabHtml() + `<iframe class="dash" src="http://${location.hostname}:${port}/ui/"></iframe>` + hint;
  }
  $('#view').onclick = ev => { onTabClick(ev); };
}
async function vServersList() {
  const q = S.search;
  const list = S.servers.filter(s => !q || s.name.toLowerCase().includes(q));
  const sel = S.settings.selected_id;
  let h = tabHtml();
  if (!S.servers.length) h += '<div class="card"><div class="mut">Belum ada server. Tekan <svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>untuk import.</div></div>';
  // Hint "Tahan lalu geser / ketuk ping" dihapus 2026-10-27 atas
  // permintaan user — yang lain (kartu kosong, daftar, tombol)
  // tidak diubah.
  for (const s of list) {
    h += `<div class="srv ${s.id === sel ? 'sel' : ''}" data-id="${s.id}">
      <span class="dragh" title="Tahan & geser untuk mengurutkan"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="5.5" r="2.4"/><circle cx="12" cy="12" r="2.4"/><circle cx="12" cy="18.5" r="2.4"/></svg></span>
      <div class="info"><div class="nm">${esc(s.name)}</div><div class="meta"><span class="proto">${esc(s.proto.toUpperCase())}</span>${statHtml(s)}</div></div>
      <button class="iconbtn" data-act="edit" title="Edit"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16z"/><path d="m13.5 6.5 4 4"/></svg></button>
      <button class="iconbtn" data-act="share" title="Bagikan"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 15V3M8 7l4-4 4 4M5 13v6a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6"/></svg></button>
      <button class="iconbtn" data-act="del" title="Hapus"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg></button>
    </div>`;
  }
  $('#view').innerHTML = h;
  $('#view').onclick = async ev => {
    if (suppressSrvClick) { suppressSrvClick = false; return; }
    if (onTabClick(ev)) return;
    const row = ev.target.closest('.srv'); if (!row) return;
    const id = row.dataset.id;
    const btn = ev.target.closest('[data-act]');
    try {
      if (btn) {
        const act = btn.dataset.act;
        if (act === 'del' && confirm('Hapus server ini?')) await api('/api/servers/' + id, 'DELETE');
        else if (act === 'edit') { openEditor(id); return; }
        else if (act === 'share') {
          const s = S.servers.find(x => x.id === id);
          await navigator.clipboard?.writeText(s.link || '');
          toast('Link disalin'); return;
        } else if (act === 'ping') {
          // Tes satu server saja, seperti ketuk ping di zashboard.
          // Core jalan -> URL Test (lewat core, bisa pakai koneksi
          // yang sudah terbuka); core belum jalan -> TCP Ping.
          // Ketuk ping tidak memilih server.
          const kind = (S.status && S.status.running) ? 'url' : 'tcp';
          toast('Mengetes…');
          await api('/api/servers/test', 'POST', { ids: [id], kind });
          await reload(); vServers(); return;
        } else return;
      } else {
        // tap = pilih server
        await api('/api/servers/pick', 'POST', { id });
        toast('Server dipilih');
      }
      await reload(); vServers();
    } catch (e) { toast(e.message); }
  };
  srvDragInit();
}

// ---- Geser untuk mengurutkan server (ala v2rayNG) ----
// HP: tahan gagang ⋮⋮ ~0.4 detik lalu geser. Mouse: tahan & geser langsung.
let DG = null, suppressSrvClick = false;
function srvDragInit() {
  if (S.search) return; // nonaktif saat hasil difilter
  document.querySelectorAll('#view .srv .dragh').forEach(h => {
    h.onpointerdown = srvDragDown;
    h.oncontextmenu = e => e.preventDefault();
  });
}
function srvDragDown(ev) {
  const row = ev.target.closest('.srv');
  if (!row || DG) return;
  ev.preventDefault();
  DG = { row, pid: ev.pointerId, x0: ev.clientX, y0: ev.clientY, curY: ev.clientY,
         active: false, timer: 0, armed: ev.pointerType === 'mouse', changed: false, h: 0 };
  if (!DG.armed) DG.timer = setTimeout(() => srvDragBegin(), 380); // long-press
  window.addEventListener('pointermove', srvDragMove);
  window.addEventListener('pointerup', srvDragUp, { once: true });
  window.addEventListener('pointercancel', srvDragUp, { once: true });
}
function srvDragBegin() {
  if (!DG || DG.active) return;
  DG.active = true;
  const row = DG.row, r = row.getBoundingClientRect();
  DG.h = r.height;
  row.classList.add('dragging');
  Object.assign(row.style, { width: r.width + 'px', left: r.left + 'px', top: (DG.curY - r.height / 2) + 'px' });
  try { row.setPointerCapture(DG.pid); } catch (e) {}
  try { navigator.vibrate && navigator.vibrate(25); } catch (e) {}
}
function srvDragMove(ev) {
  if (!DG || ev.pointerId !== DG.pid) return;
  DG.curY = ev.clientY;
  const dx = ev.clientX - DG.x0, dy = ev.clientY - DG.y0;
  if (!DG.active) {
    if (DG.timer && (Math.abs(dx) > 10 || Math.abs(dy) > 10)) { clearTimeout(DG.timer); DG.timer = 0; }
    else if (DG.armed && Math.hypot(dx, dy) > 6) srvDragBegin();
    if (!DG.active) return;
  }
  ev.preventDefault();
  DG.row.style.top = (ev.clientY - DG.h / 2) + 'px';
  DG.row.style.visibility = 'hidden';
  const el = document.elementFromPoint(ev.clientX, ev.clientY);
  DG.row.style.visibility = '';
  const target = el && el.closest ? el.closest('#view .srv:not(.dragging)') : null;
  document.querySelectorAll('#view .srv.dropmark').forEach(x => x.classList.remove('dropmark'));
  if (target && target !== DG.row) {
    const tr = target.getBoundingClientRect();
    if (ev.clientY < tr.top + tr.height / 2) target.parentNode.insertBefore(DG.row, target);
    else target.parentNode.insertBefore(DG.row, target.nextSibling);
    DG.changed = true;
    target.classList.add('dropmark');
  }
  const m = 90; // auto-scroll di tepi layar
  if (ev.clientY < m) window.scrollBy(0, -14);
  else if (ev.clientY > window.innerHeight - m) window.scrollBy(0, 14);
}
async function srvDragUp(ev) {
  window.removeEventListener('pointermove', srvDragMove);
  const st = DG; DG = null;
  if (!st) return;
  if (st.timer) clearTimeout(st.timer);
  if (!st.active) return;
  suppressSrvClick = true;
  const row = st.row;
  row.classList.remove('dragging');
  row.removeAttribute('style');
  document.querySelectorAll('#view .srv.dropmark').forEach(x => x.classList.remove('dropmark'));
  if (st.changed) {
    const ids = [...document.querySelectorAll('#view .srv')].map(x => x.dataset.id);
    try {
      await api('/api/servers/order', 'POST', { ids });
      toast('Urutan tersimpan');
      await reload(); vServers();
    } catch (e) { toast(e.message); vServers(); }
  }
}

// ➕ menu
$('#btnAdd').onclick = e => {
  const r = (e.currentTarget || e.target).getBoundingClientRect();
  openMenu(r.left - 200, r.bottom + 6, [
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="4" width="14" height="17" rx="2.5"/><path d="M9 4h6v3H9zM9 12h6M9 16h4"/></svg>Import dari Clipboard', fn: async () => {
      try { const t = await navigator.clipboard.readText(); if (!t.trim()) return toast('Clipboard kosong');
        const r = await api('/api/servers/import', 'POST', { text: t });
        toast(`Import: ${r.added} ok, ${r.failed} gagal`); await reload(); vServers();
      } catch (e) { toast(e.message); } } },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>Import dari File', fn: () => {
      const inp = document.createElement('input'); inp.type = 'file'; inp.accept = '.txt,.json,.yaml,.yml';
      inp.onchange = async () => { const t = await inp.files[0].text();
        const r = await api('/api/servers/import', 'POST', { text: t });
        toast(`Import: ${r.added} ok`); await reload(); vServers(); };
      inp.click(); } },
    { sep: true },
    ...Object.entries(PROTO_LABEL).filter(([k]) => (S.settings && S.settings.core === 'singbox') || (k !== 'hysteria' && k !== 'anytls')).map(([k, v]) => ({ label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>Manual: ' + v, fn: () => openEditor(null, k) })),
  ]);
};

// ⋮ menu
$('#btnMore').onclick = e => {
  const r = (e.currentTarget || e.target).getBoundingClientRect();
  openMenu(r.left - 200, r.bottom + 6, [
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14-4M4 5v3h3M4 13a8 8 0 0 0 14 4m2 2v-3h-3"/></svg>Kelola Subscription', fn: () => openSubs() },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14-4M4 5v3h3M4 13a8 8 0 0 0 14 4m2 2v-3h-3"/></svg>Update Subscription', fn: async () => { toast('Memperbarui…'); const r = await api('/api/subscriptions/update', 'POST'); toast('Selesai'); await reload(); vServers(); } },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4l6 6M12.5 8.5 4 17l3 3 8.5-8.5M4 17l-1 4 4-1"/></svg>Hapus duplikat', fn: async () => { const r = await api('/api/servers/dedup', 'POST'); toast(`Dihapus ${r.removed}`); await reload(); vServers(); } },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4l6 6M12.5 8.5 4 17l3 3 8.5-8.5M4 17l-1 4 4-1"/></svg>Hapus server gagal tes', fn: async () => { if (!confirm('Hapus semua server yang hasil tesnya gagal (timeout)?')) return; const r = await api('/api/servers/clear-unavailable', 'POST'); toast(`Dihapus ${r.removed}`); await reload(); vServers(); } },
    { sep: true },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v8"/><path d="M6.3 6.3a8 8 0 1 0 11.4 0"/></svg>Restart service', fn: async () => { if (!confirm('Restart ' + coreName() + '?')) return; try { toast('Me-restart…'); await api('/api/restart', 'POST'); toast(coreName() + ' di-restart'); } catch (e) { toast(e.message); } } },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M13 2 4 14h7l-1 8 9-12h-7z"/></svg>TCP Ping semua', fn: () => testAll('tcp') },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3.2 3 14.8 0 18M12 3c-3 3.2-3 14.8 0 18"/></svg>URL Test semua', fn: () => testAll('url') },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 15V3M8 7l4-4 4 4M5 13v6a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6"/></svg>Export semua akun', fn: exportAll },
    { sep: true },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 4v16M3.5 16.5 7 20l3.5-3.5M17 20V4M13.5 7.5 17 4l3.5 3.5"/></svg>Urutkan: by Delay', fn: async () => { await api('/api/servers/order', 'POST', { by: 'delay' }); await reload(); vServers(); } },
    { label: '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 4v16M3.5 16.5 7 20l3.5-3.5M17 20V4M13.5 7.5 17 4l3.5 3.5"/></svg>Urutkan: by Nama', fn: async () => { await api('/api/servers/order', 'POST', { by: 'name' }); await reload(); vServers(); } },
  ]);
};
async function testAll(kind) {
  toast('Mengetes semua…');
  try { await api('/api/servers/test', 'POST', { kind }); toast('Selesai'); }
  catch (e) { toast(e.message); }
  await reload(); vServers();
}
async function exportAll() {
  try {
    const r = await fetch('/api/servers/export');
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const t = (await r.text()).trim();
    if (!t) return toast('Tidak ada akun untuk diekspor');
    await navigator.clipboard.writeText(t);
    toast(t.split('\n').length + ' akun disalin');
  } catch (e) { toast(e.message); }
}

// ================= SERVER EDITOR ala TaptapBox =================
const PROTO_LABEL = { vmess: 'VMess', vless: 'VLESS', trojan: 'Trojan', ss: 'Shadowsocks', hy2: 'Hysteria2', hysteria: 'Hysteria', tuic: 'TUIC', wireguard: 'WireGuard', socks: 'SOCKS', http: 'HTTP', anytls: 'AnyTLS', ssh: 'SSH' };
const HAS_TRANSPORT = ['vmess', 'vless', 'trojan'];
const HAS_TLS = ['vmess', 'vless', 'trojan', 'hy2', 'hysteria', 'tuic', 'anytls', 'socks', 'http'];
const TLS_FORCED = ['trojan', 'hy2', 'hysteria', 'tuic', 'anytls'];
const SEC_VMESS = [['auto', 'auto'], ['aes-128-gcm', 'aes-128-gcm'], ['chacha20-poly1305', 'chacha20-poly1305'], ['none', 'none'], ['zero', 'zero']];
const SS_METHODS = ['aes-256-gcm', 'aes-128-gcm', 'chacha20-ietf-poly1305', 'xchacha20-ietf-poly1305', '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305'].map(m => [m, m]);
// [key param, label, tipe ('i' input | 's' select), opsi/placeholder]
const FIELDS = {
  vmess: [['uuid', 'id (UUID)', 'i'], ['alter_id', 'alterId', 'i', '0'], ['security', 'security', 's', SEC_VMESS]],
  vless: [['uuid', 'id (UUID)', 'i'], ['flow', 'flow', 's', [['', '(kosong)'], ['xtls-rprx-vision', 'xtls-rprx-vision']]]],
  trojan: [['password', 'password', 'i']],
  ss: [['method', 'method', 's', SS_METHODS], ['password', 'password', 'i'], ['plugin', 'plugin (obfs-local / v2ray-plugin)', 'i'], ['plugin_opts', 'plugin opts', 'i', 'obfs=http;obfs-host=example.com']],
  hy2: [['auth', 'password / auth', 'i'], ['obfs', 'obfs', 's', [['', '(mati)'], ['salamander', 'salamander']]], ['obfs_password', 'obfs password', 'i'], ['server_ports', 'port hopping (mis. 5000-6000)', 'i'], ['up_mbps', 'up (Mbps)', 'i'], ['down_mbps', 'down (Mbps)', 'i']],
  hysteria: [['auth', 'auth', 'i'], ['obfs', 'obfs', 'i'], ['up_mbps', 'up (Mbps)', 'i', '20'], ['down_mbps', 'down (Mbps)', 'i', '100']],
  tuic: [['uuid', 'uuid', 'i'], ['password', 'password', 'i'], ['congestion_control', 'congestion control', 's', [['', '(default)'], ['bbr', 'bbr'], ['cubic', 'cubic'], ['new_reno', 'new_reno']]], ['udp_relay_mode', 'udp relay mode', 's', [['', '(default)'], ['native', 'native'], ['quic', 'quic']]]],
  wireguard: [['private_key', 'private key', 'i'], ['peer_public_key', 'peer public key', 'i'], ['pre_shared_key', 'pre-shared key', 'i'], ['local_address', 'local address (koma)', 'i', '172.16.0.2/32'], ['mtu', 'mtu', 'i', '1280'], ['reserved', 'reserved (a,b,c)', 'i']],
  socks: [['username', 'username', 'i'], ['password', 'password', 'i']],
  http: [['username', 'username', 'i'], ['password', 'password', 'i']],
  anytls: [['password', 'password', 'i']],
  ssh: [['username', 'user', 'i', 'root'], ['password', 'password', 'i'], ['private_key', 'private key (PEM)', 'i']],
};
function openEditor(id, newProto) {
  const s = id ? S.servers.find(x => x.id === id) : null;
  const p = Object.assign({}, (s && s.params) || {});
  const proto = s ? s.proto : newProto;
  if (!FIELDS[proto]) { toast('Protokol belum didukung editor: ' + proto); return; }
  S.editing = true;
  const F = (fid, label, val, ph) => `<label class="lbl">${label}</label><input id="${fid}" value="${esc(val || '')}" placeholder="${esc(ph || '')}">`;
  const FS = (fid, label, val, opts) => `<label class="lbl">${label}</label><select id="${fid}">` +
    opts.map(o => `<option value="${esc(o[0])}" ${(val || '') === o[0] ? 'selected' : ''}>${esc(o[1])}</option>`).join('') + `</select>`;
  const defPort = { ssh: '22', socks: '1080', wireguard: '51820' }[proto] || '443';

  let h = `<div class="card"><h3>${s ? 'Edit' : 'Tambah'} Server <span class="proto">${esc((PROTO_LABEL[proto] || proto).toUpperCase())}</span></h3>
    ${F('eName', 'remarks', s ? s.name : '')}
    <div class="row2"><div>${F('eAddr', 'address', p.server)}</div><div>${F('ePort', 'port', p.port || defPort)}</div></div>`;
  for (const [k, label, t, o] of FIELDS[proto]) {
    h += t === 's' ? FS('f_' + k, label, p[k] || (Array.isArray(o) && o[0] ? o[0][0] : ''), o) : F('f_' + k, label, p[k], o);
  }
  if (proto === 'ssh') {
    const sshOn = p.ssh_payload_on === '1';
    const sshTls = p.ssh_tls === '1';
    const sshPayload = p.ssh_payload || 'GET / HTTP/1.1[crlf]Host: [host][crlf]Connection: Upgrade[crlf]User-Agent: [ua][crlf]Upgrade: websocket[crlf][crlf]';
    const chk = (id, label, on) => `<label class="chk"><div class="t">${label}</div><input type="checkbox" id="${id}" ${on ? 'checked' : ''}></label>`;
    h += `<div class="sect">SSH — gaya HTTP Custom</div>
      <div class="hint">Transport di sini Standard (bukan HCR). SSH jalan lewat jembatan lokal: core bicara SSH ke jembatan, jembatan yang kirim payload ke proxy jarak jauh. SlowDNS jalan sendiri, bukan bagian SSH ini.</div>
      ${chk('eSshOn', 'Gunakan payload', sshOn)}
      ${chk('eSshEnh', 'Enhanced (payload mentah, seperti ditulis)', p.ssh_enhanced === '1' || sshOn)}
      <div id="eSshPayloadSec">
        ${F('eSshProxy', 'proxy jarak jauh (host:port)', p.ssh_proxy, 'contoh: d123.cloudfront.net:443')}
        <label class="lbl">payload kustom ([host] = address SSH, [protocol] = HTTP/1.1, [ua] = User-Agent, [crlf] = ganti baris)</label><textarea id="eSshPayload" rows="5">${esc(sshPayload)}</textarea>
        ${FS('eSshMode', 'sesudah payload (101)', (p.ssh_mode || (p.ssh_raw === '1' ? 'raw' : 'ws')), [['ws', 'WebSocket (frame, standar)'], ['raw', 'Pipa mentah (tanpa frame, seperti injector)']])}
        <div class="hint">Bila tes tetap Timeout di mode WebSocket, coba Pipa mentah, lalu restart core dan tes lagi. Mode WebSocket menambahkan Sec-WebSocket-Key otomatis.</div>
        ${chk('eSshTls', 'TLS (SNI ke proxy jarak jauh)', sshTls)}
        <div id="eSshTlsSec">${F('eSshSni', 'SNI (kosong = address SSH)', p.ssh_sni)}
        ${FS('eSshSniVer', 'versi SNI', p.ssh_sni_ver || '', [['', 'Default']])}
        ${chk('eSshInsec', 'Izinkan koneksi tidak aman', p.ssh_insecure === '1' || p.ssh_insecure === 'true')}</div>
      </div>`;
  }
  if (HAS_TRANSPORT.includes(proto)) {
    const net = p.net || 'tcp';
    h += `<div class="sect">Transport</div>`
      + FS('eNet', 'network', net, [['tcp', 'tcp'], ['ws', 'ws'], ['h2', 'h2 (http)'], ['grpc', 'grpc'], ['httpupgrade', 'httpupgrade'], ['quic', 'quic']])
      + `<div id="eNetTcp" class="netsec">${FS('eHeadType', 'head type', p.header_type || 'none', [['none', 'none'], ['http', 'http']])}</div>`
      + `<div id="eNetHp" class="netsec">${F('eHost', 'host', p.host)}${F('ePath', 'path', p.path, '/')}</div>`
      + `<div id="eNetGrpc" class="netsec">${F('eGrpcSvc', 'serviceName', p.service_name)}</div>`;
  }
  if (HAS_TLS.includes(proto)) {
    const forced = TLS_FORCED.includes(proto);
    h += `<div class="sect">TLS</div>`;
    if (!forced) h += FS('eTls', 'TLS', p.tls === '1' ? 'tls' : '', [['', '(mati)'], ['tls', 'tls']]);
    h += `<div id="eTlsSec">` + F('eSni', 'SNI', p.sni);
    if (proto !== 'hy2' && proto !== 'hysteria' && proto !== 'tuic')
      h += FS('eFp', 'fingerprint', p.fp || '', [['', '(kosong)'], ['chrome', 'chrome'], ['firefox', 'firefox'], ['safari', 'safari'], ['ios', 'ios'], ['android', 'android'], ['edge', 'edge'], ['random', 'random']]);
    h += F('eAlpn', 'alpn (pisah koma)', p.alpn, 'h2,http/1.1')
      + FS('eInsec', 'allowInsecure', (p.insecure === '1' || p.insecure === 'true') ? '1' : '', [['', 'false'], ['1', 'true']]);
    if (proto === 'vless') h += `<div class="row2"><div>${F('ePbk', 'reality public key', p.pbk)}</div><div>${F('eSid', 'reality short id', p.sid)}</div></div>`;
    h += `</div>`;
  }
  h += `<div class="row" style="margin-top:12px">
      <button class="btn primary" id="eSave"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 4h11l3 3v13H5z"/><path d="M8 4v5h7V4M8 20v-6h8v6"/></svg>Simpan</button>
      <button class="btn" id="eBack">Kembali</button>
    </div></div>`;

  $('#view').innerHTML = h;
  const syncNet = () => {
    const el = document.getElementById('eNet'); if (!el) return;
    const v = el.value;
    for (const [sec, keys] of [['eNetTcp', ['tcp']], ['eNetHp', ['ws', 'h2', 'httpupgrade']], ['eNetGrpc', ['grpc']]])
      document.getElementById(sec).style.display = keys.includes(v) ? '' : 'none';
  };
  const tlsOn = () => TLS_FORCED.includes(proto) || (document.getElementById('eTls') || {}).value === 'tls';
  const syncTls = () => { const el = document.getElementById('eTlsSec'); if (el) el.style.display = tlsOn() ? '' : 'none'; };
  if ($('#eNet')) $('#eNet').onchange = syncNet;
  if ($('#eTls')) $('#eTls').onchange = syncTls;
  const syncSsh = () => {
    const on = document.getElementById('eSshOn');
    const tls = document.getElementById('eSshTls');
    const sec = document.getElementById('eSshPayloadSec');
    const tsec = document.getElementById('eSshTlsSec');
    if (sec) sec.style.display = (on && on.checked) ? '' : 'none';
    if (tsec) tsec.style.display = (tls && tls.checked) ? '' : 'none';
  };
  if ($('#eSshOn')) $('#eSshOn').onchange = syncSsh;
  if ($('#eSshTls')) $('#eSshTls').onchange = syncSsh;
  syncNet(); syncTls(); syncSsh();

  $('#eBack').onclick = () => { S.editing = false; vServers(); };
  $('#eSave').onclick = async () => {
    const gv = fid => { const el = document.getElementById(fid); return el ? el.value.trim() : ''; };
    const params = { server: gv('eAddr'), port: gv('ePort') || defPort };
    for (const [k] of FIELDS[proto]) params[k] = gv('f_' + k);
    if (HAS_TRANSPORT.includes(proto)) {
      const n = gv('eNet'); params.net = n;
      params.header_type = n === 'tcp' ? gv('eHeadType') : '';
      params.host = ['ws', 'h2', 'httpupgrade'].includes(n) ? gv('eHost') : '';
      params.path = ['ws', 'h2', 'httpupgrade'].includes(n) ? gv('ePath') : '';
      params.service_name = n === 'grpc' ? gv('eGrpcSvc') : '';
    }
    if (HAS_TLS.includes(proto)) {
      if (tlsOn()) {
        params.tls = '1'; params.sni = gv('eSni') || gv('eAddr');
        params.fp = gv('eFp'); params.alpn = gv('eAlpn'); params.insecure = gv('eInsec');
        if (proto === 'vless') { params.pbk = gv('ePbk'); params.sid = gv('eSid'); params.reality = params.pbk ? '1' : ''; }
      } else {
        // TLS dimatikan: kosongkan semua field TLS (nilai kosong = dihapus di backend)
        Object.assign(params, { tls: '', sni: '', fp: '', alpn: '', insecure: '', reality: '', pbk: '', sid: '' });
      }
    }
    if (proto === 'ssh') {
      const gcb = fid => { const el = document.getElementById(fid); return el ? (el.checked ? '1' : '') : ''; };
      params.ssh_payload_on = gcb('eSshOn');
      params.ssh_enhanced = gcb('eSshEnh');
      params.ssh_tls = gcb('eSshTls');
      params.ssh_proxy = gv('eSshProxy');
      params.ssh_payload = gv('eSshPayload');
      params.ssh_mode = gv('eSshMode') || 'ws';
      params.ssh_raw = params.ssh_mode === 'raw' ? '1' : '';
      params.ssh_sni = gv('eSshSni');
      params.ssh_sni_ver = gv('eSshSniVer');
      params.ssh_insecure = gcb('eSshInsec');
    }
    if (!params.server) return toast('Isi address');
    try {
      if (s) await api('/api/servers/' + id, 'PUT', { name: gv('eName'), params });
      else await api('/api/servers', 'POST', { name: gv('eName'), proto, params });
      S.editing = false;
      toast('Tersimpan'); await reload(); vServers();
    } catch (e) { toast(e.message); }
  };
}

// ================= GROUPS (Langganan + grup mihomo) =================
function subHtml() {
  const rows = (S.subs || []).map(x => `<div class="chk"><div class="t">${esc(x.name)}<small>${x.count || 0} server • ${x.updated_at ? new Date(x.updated_at * 1000).toLocaleString() : 'belum diupdate'}${x.enabled ? '' : ' • nonaktif'}</small></div>
    <button class="iconbtn" data-sa="upd" data-id="${x.id}" title="Update"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14-4M4 5v3h3M4 13a8 8 0 0 0 14 4m2 2v-3h-3"/></svg></button>
    <button class="iconbtn" data-sa="edit" data-id="${x.id}" title="Edit"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16z"/><path d="m13.5 6.5 4 4"/></svg></button>
    <button class="iconbtn" data-sa="del" data-id="${x.id}" title="Hapus"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg></button></div>`).join('');
  return rows || '<div class="mut">Belum ada langganan.</div>';
}
function subModal(x, after) {
  after = after || (() => vGroups());
  modal(x ? 'Edit langganan' : 'Tambah langganan', `
    <label class="lbl">Nama</label><input id="mSubName" value="${esc(x ? x.name : '')}">
    <label class="lbl">URL</label><input id="mSubUrl" value="${esc(x ? x.url : '')}" placeholder="https://…">
    ${x ? `<label class="chk"><div class="t">Aktif (ikut Update Subscription)</div><input type="checkbox" id="mSubOn" ${x.enabled ? 'checked' : ''}></label>` : ''}
    <div class="row" style="margin-top:10px"><button class="btn primary" id="mSubOk">Simpan</button></div>`);
  $('#mSubOk').onclick = async () => {
    const name = $('#mSubName').value.trim(), url = $('#mSubUrl').value.trim();
    if (!/^https?:\/\//.test(url)) return toast('URL harus diawali http(s)://');
    try {
      if (x) await api('/api/subscriptions/' + x.id, 'PUT', { name: name || url, url, enabled: $('#mSubOn').checked });
      else {
        const r = await api('/api/subscriptions', 'POST', { name: name || url, url });
        $('#modal').classList.add('hidden'); toast('Mengunduh…');
        const u = await api('/api/subscriptions/update', 'POST', { id: r.id });
        toast(u[0] && u[0].error ? u[0].error : `${u[0] ? u[0].added : 0} server ditambah`);
        await reload(); after(); return;
      }
      $('#modal').classList.add('hidden'); toast('Tersimpan'); await reload(); after();
    } catch (e) { toast(e.message); }
  };
}
// ---- Kelola Subscription (modal, ala v1.1.0) ----
function openSubs() {
  modal('Kelola Subscription', `
    <div id="osBox">${subHtml()}</div>
    <div class="row" style="margin-top:10px"><button class="btn small primary" id="osAdd">Tambah langganan</button>
    <button class="btn small" id="osAll">Update semua</button></div>`);
  const done = async () => { await reload(); $('#osBox').innerHTML = subHtml(); vServers(); };
  $('#osAdd').onclick = () => subModal(null, () => openSubs());
  $('#osAll').onclick = async () => {
    toast('Memperbarui…');
    try {
      const r = await api('/api/subscriptions/update', 'POST', {});
      const bad = r.filter(x => x.error);
      toast(bad.length ? bad[0].name + ': ' + bad[0].error : 'Selesai');
      await done();
    } catch (e) { toast(e.message); }
  };
  $('#osBox').onclick = async ev => {
    const b = ev.target.closest('[data-sa]'); if (!b) return;
    const x = (S.subs || []).find(z => z.id === b.dataset.id); if (!x) return;
    try {
      if (b.dataset.sa === 'upd') {
        toast('Memperbarui…');
        const r = await api('/api/subscriptions/update', 'POST', { id: x.id });
        toast(r[0] && r[0].error ? r[0].error : `${r[0] ? r[0].added : 0} server`);
      } else if (b.dataset.sa === 'edit') { subModal(x, () => openSubs()); return; }
      else if (b.dataset.sa === 'del') {
        if (!confirm('Hapus langganan beserta server-nya?')) return;
        await api('/api/subscriptions/' + x.id, 'DELETE'); toast('Dihapus');
      } else return;
      await done();
    } catch (e) { toast(e.message); }
  };
}
// ---- Grup proxy custom ----
const gid = () => 'g' + Date.now().toString(36) + Math.floor(Math.random() * 1e3).toString(36);
function grpSummary(g) {
  const parts = [];
  const nm = (g.members || []).length; if (nm) parts.push(nm + ' dipilih');
  if ((g.sub_ids || []).length) parts.push('langganan: ' + g.sub_ids.map(id => id === 'manual' ? 'manual' : ((S.subs.find(x => x.id === id) || {}).name || id)).join(', '));
  if (g.filter) parts.push('filter: ' + g.filter);
  if (g.direct) parts.push('+DIRECT');
  return parts.join(' • ') || 'kosong (dilewati)';
}
function grpCardHtml() {
  const gs = S.builder.groups || [];
  const rows = gs.map((g, i) => `<div class="chk"><div class="t">${esc(g.name || '(tanpa nama)')} <span class="proto">${({selector:'PILIH',urltest:'AUTO',loadbalance:'BALANCE',fallback:'CADANGAN',relay:'RELAY'})[g.type] || 'PILIH'}</span><small>${esc(grpSummary(g))}</small></div>
    <button class="iconbtn" data-gc="up" data-id="${g.id}" title="Naik">↑</button>
    <button class="iconbtn" data-gc="dn" data-id="${g.id}" title="Turun">↓</button>
    <button class="iconbtn" data-gc="edit" data-id="${g.id}" title="Edit"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16z"/><path d="m13.5 6.5 4 4"/></svg></button>
    <button class="iconbtn" data-gc="del" data-id="${g.id}" title="Hapus"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg></button></div>`).join('');
  return rows || '<div class="mut">Belum ada grup custom. Buat grup (mis. "Singapore", "Streaming") untuk dipilih dari dashboard.</div>';
}
async function saveGroups(msg) {
  const r = await api('/api/builder', 'PUT', S.builder);
  if (msg) toast(msg);
  if (r && r.override_warn) toast('⚠ Override: ' + r.override_warn);
}
function groupModal(g0) {
  S.builder.groups = S.builder.groups || [];
  const idx = g0 ? S.builder.groups.findIndex(x => x.id === g0.id) : S.builder.groups.length;
  const g = g0 ? JSON.parse(JSON.stringify(g0)) : { id: gid(), name: '', type: 'selector', members: [], sub_ids: [], filter: '', direct: false };
  g.members = g.members || []; g.sub_ids = g.sub_ids || [];
  const earlier = S.builder.groups.slice(0, idx).filter(x => x.id !== g.id);
  const chkRow = (val, title, sub, checked, attr) => `<label class="chk" data-q="${esc((title + ' ' + sub).toLowerCase())}"><div class="t">${esc(title)}<small>${esc(sub)}</small></div><input type="checkbox" ${attr}="${esc(val)}" ${checked ? 'checked' : ''}></label>`;
  const subRows = [{ id: 'manual', name: 'Server buatan sendiri' }].concat(S.subs.map(x => ({ id: x.id, name: x.name })))
    .map(x => chkRow(x.id, x.name, 'anggota otomatis dari sini', g.sub_ids.includes(x.id), 'data-sub')).join('');
  const srvRows = S.servers.map(x => chkRow(x.id, x.name, x.proto, g.members.includes(x.id), 'data-m')).join('');
  const grpRows = earlier.map(x => chkRow(x.id, '⇢ grup: ' + x.name, 'grup lain', g.members.includes(x.id), 'data-m')).join('');
  // Tipe grup per core: load-balance/fallback/relay hanya ada di mihomo;
  // di sing-box grup non-urltest selalu jadi selector, jadi disembunyikan.
  // Grup lama bertipe khusus mihomo tetap ditampilkan saat diedit di mode
  // sing-box (bertanda) agar simpan tidak mengubah tipenya diam-diam.
  const boxCore = S.settings && S.settings.core === 'singbox';
  const GT_LBL = { selector: 'Pilih manual (selector)', urltest: 'Otomatis, ping terendah (url-test)', loadbalance: 'Bagi beban (load-balance)', fallback: 'Cadangan berurutan (fallback)', relay: 'Rantai berurutan (relay)' };
  const gtList = Object.keys(GT_LBL).filter(v => !boxCore || v === 'selector' || v === 'urltest');
  if (g.type && !gtList.includes(g.type)) gtList.push(g.type);
  const gtOpts = gtList.map(v => `<option value="${v}" ${g.type === v || (!g.type && v === 'selector') ? 'selected' : ''}>${GT_LBL[v]}${boxCore && v !== 'selector' && v !== 'urltest' ? ' — khusus mihomo' : ''}</option>`).join('');
  modal(g0 ? 'Edit grup' : 'Tambah grup', `
    <label class="lbl">Nama grup</label><input id="gName" value="${esc(g.name)}" placeholder="mis. Singapore">
    <label class="lbl">Tipe</label><select id="gType">${gtOpts}</select>
    <div id="gStrategyWrap" style="${g.type === 'loadbalance' ? '' : 'display:none'}">
      <label class="lbl">Strategi load-balance</label><select id="gStrategy">
        <option value="consistent-hashing" ${g.strategy !== 'round-robin' ? 'selected' : ''}>consistent-hashing (koneksi stabil)</option>
        <option value="round-robin" ${g.strategy === 'round-robin' ? 'selected' : ''}>round-robin (giliran)</option></select>
    </div>
    <div class="hint" id="gTypeHint"></div>
    <label class="lbl">Filter nama (regex, mis. SG|Singapore)</label><input id="gFilter" value="${esc(g.filter || '')}" placeholder="kosong = tanpa filter">
    <div class="sect">Anggota otomatis dari langganan</div>
    <div class="hint">Bila ada langganan dicentang dan/atau filter terisi, semua akun yang cocok masuk grup otomatis (ikut berubah saat Update langganan).</div>
    <div id="gSubs">${subRows}</div>
    <div class="sect">Pilih akun manual</div>
    <input id="gQ" placeholder="cari akun…">
    <div class="row" style="margin:6px 0"><button class="btn small" id="gAllShown">Pilih yang tampil</button><button class="btn small" id="gNone">Kosongkan</button></div>
    <div id="gList" style="max-height:34vh;overflow:auto">${chkRow('DIRECT', 'DIRECT', 'langsung tanpa proksi', g.members.includes('DIRECT'), 'data-m')}${grpRows}${srvRows || '<div class="mut">Belum ada server.</div>'}</div>
    <label class="chk"><div class="t">Tambahkan DIRECT juga</div><input type="checkbox" id="gDirect" ${g.direct ? 'checked' : ''}></label>
    <div class="row" style="margin-top:10px"><button class="btn primary" id="gOk">Simpan</button></div>`);
  const typeHints = {
    selector: 'Pilih manual dari panel utama / Zashboard.',
    urltest: 'Otomatis memilih anggota dengan ping terendah (cek tiap 10 menit).',
    loadbalance: 'Trafik dibagi ke semua anggota sesuai strategi.',
    fallback: 'Pakai anggota pertama yang sehat; pindah ke berikutnya bila mati.',
    relay: 'Trafik dirantai lewat tiap anggota berurutan (mis. SG lalu US).',
  };
  const updType = () => {
    const t = $('#gType').value;
    $('#gStrategyWrap').style.display = t === 'loadbalance' ? '' : 'none';
    $('#gTypeHint').textContent = typeHints[t] || '';
  };
  $('#gType').onchange = updType; updType();
  $('#gQ').oninput = () => {
    const q = $('#gQ').value.trim().toLowerCase();
    document.querySelectorAll('#gList [data-q]').forEach(el => { el.style.display = !q || el.dataset.q.includes(q) ? '' : 'none'; });
  };
  $('#gAllShown').onclick = () => document.querySelectorAll('#gList [data-q]').forEach(el => { if (el.style.display !== 'none') el.querySelector('input').checked = true; });
  $('#gNone').onclick = () => document.querySelectorAll('#gList input').forEach(i => { i.checked = false; });
  $('#gOk').onclick = async () => {
    g.name = $('#gName').value.trim();
    if (!g.name) return toast('Isi nama grup');
    g.type = $('#gType').value; g.strategy = $('#gType').value === 'loadbalance' ? $('#gStrategy').value : ''; g.filter = $('#gFilter').value.trim(); g.direct = $('#gDirect').checked;
    g.members = [...document.querySelectorAll('#gList input[data-m]:checked')].map(i => i.getAttribute('data-m'));
    g.sub_ids = [...document.querySelectorAll('#gSubs input[data-sub]:checked')].map(i => i.getAttribute('data-sub'));
    if (g0) S.builder.groups[idx] = g; else S.builder.groups.push(g);
    try {
      // tulis di atas data terbaru agar tidak menimpa perubahan sesi lain
      const bld = await api('/api/builder'); bld.groups = S.builder.groups; S.builder = bld;
      await saveGroups('Grup tersimpan — tekan Terapkan'); $('#modal').classList.add('hidden'); vGroups();
    }
    catch (e) { toast(e.message); }
  };
}
async function vGroups() {
  $('#view').innerHTML = `<div class="card"><h3>Grup utama</h3>
    <label class="lbl">Nama grup utama (tampil di dashboard)</label>
    <input id="mgName" value="${esc(S.settings.main_group_name || 'Taptap')}" placeholder="Taptap">
    <div class="row" style="margin-top:10px"><button class="btn small primary" id="mgSave">Simpan</button></div>
    <div class="hint">Menyimpan me-restart ${coreName()} bila sedang terhubung (nama dipakai di config).</div></div>
    <div class="card"><h3>Grup custom</h3><div id="gcBox">${grpCardHtml()}</div>
    <div class="row" style="margin-top:10px"><button class="btn small primary" id="gcAdd">Tambah grup</button>
    <button class="btn small" id="gcApply">Terapkan (restart)</button></div>
    <div class="hint">Grup tampil di dashboard. Perubahan grup otomatis masuk ke config override bila override aktif (tak perlu "kembali ke otomatis" dulu). Grup kosong dilewati otomatis.</div></div>
    <div class="card"><h3>Langganan</h3><div id="subBox">${subHtml()}</div>
    <div class="row" style="margin-top:10px"><button class="btn small primary" id="subAdd">Tambah langganan</button>
    <button class="btn small" id="subAll">Update semua</button></div></div>
    <div class="card"><h3>Grup mihomo</h3><div id="grpBox"><div class="mut">Memuat…</div></div></div>`;
  $('#gcAdd').onclick = () => groupModal(null);
  $('#mgSave').onclick = async () => {
    const v = $('#mgName').value.trim();
    if (!v) return toast('Isi nama grup utama');
    try {
      toast('Menyimpan…');
      await api('/api/settings', 'PUT', { main_group_name: v });
      toast('Nama grup utama tersimpan'); await reload(); vGroups();
    } catch (e) { toast(e.message); }
  };
  $('#gcApply').onclick = async () => {
    try { toast('Menerapkan…'); await doApply(); await reload(); vGroups(); }
    catch (e) { toast(e.message); }
  };
  $('#gcBox').onclick = async ev => {
    const b = ev.target.closest('[data-gc]'); if (!b) return;
    const gs = S.builder.groups = S.builder.groups || [];
    const i = gs.findIndex(x => x.id === b.dataset.id); if (i < 0) return;
    try {
      if (b.dataset.gc === 'edit') return groupModal(gs[i]);
      if (b.dataset.gc === 'del') {
        if (!confirm('Hapus grup "' + gs[i].name + '"?')) return;
        const id = gs[i].id; gs.splice(i, 1);
        (S.builder.rules || []).forEach(r => { if (r.outbound === id) r.outbound = 'Proxy'; });
        if (S.builder.final === id) S.builder.final = '';
        gs.forEach(x => { x.members = (x.members || []).filter(m => m !== id); });
      } else if (b.dataset.gc === 'up' && i > 0) [gs[i - 1], gs[i]] = [gs[i], gs[i - 1]];
      else if (b.dataset.gc === 'dn' && i < gs.length - 1) [gs[i + 1], gs[i]] = [gs[i], gs[i + 1]];
      await saveGroups(); vGroups();
    } catch (e) { toast(e.message); }
  };
  $('#subAdd').onclick = () => subModal(null);
  $('#subAll').onclick = async () => {
    toast('Memperbarui…');
    try {
      const r = await api('/api/subscriptions/update', 'POST', {});
      const bad = r.filter(x => x.error);
      toast(bad.length ? bad[0].name + ': ' + bad[0].error : 'Selesai');
      await reload(); vGroups();
    } catch (e) { toast(e.message); }
  };
  $('#subBox').onclick = async ev => {
    const b = ev.target.closest('[data-sa]'); if (!b) return;
    const x = (S.subs || []).find(z => z.id === b.dataset.id); if (!x) return;
    try {
      if (b.dataset.sa === 'upd') {
        toast('Memperbarui…');
        const r = await api('/api/subscriptions/update', 'POST', { id: x.id });
        toast(r[0] && r[0].error ? r[0].error : `${r[0] ? r[0].added : 0} server`);
        await reload(); vGroups();
      } else if (b.dataset.sa === 'edit') subModal(x);
      else if (b.dataset.sa === 'del' && confirm('Hapus langganan beserta server-nya?')) {
        await api('/api/subscriptions/' + x.id, 'DELETE'); await reload(); vGroups();
      }
    } catch (e) { toast(e.message); }
  };
  try {
    const g = await api('/api/clash/proxies');
    const groups = Object.entries(g.proxies || {}).filter(([_, v]) => v.type === 'Selector' || v.type === 'URLTest');
    $('#grpBox').innerHTML = groups.map(([name, v]) =>
      `<details><summary>${esc(name)} <span class="mut">→ ${esc(v.now || '')}</span></summary><div class="dbody">` +
      (v.all || []).map(n => `<button class="btn small" style="margin:2px" data-g="${esc(name)}" data-n="${esc(n)}">${esc(n)}</button>`).join('') +
      `</div></details>`
    ).join('') || '<div class="mut">Tidak ada grup.</div>';
    $('#grpBox').onclick = async ev => {
      const b = ev.target.closest('[data-n]'); if (!b) return;
      try { await api('/api/clash/proxies/' + encodeURIComponent(b.dataset.g), 'PUT', { name: b.dataset.n }); toast('Dialihkan ke ' + b.dataset.n); vGroups(); }
      catch (e) { toast(e.message); }
    };
  } catch (e) { $('#grpBox').innerHTML = `<div class="mut">${isBoxCore() ? 'Butuh mihomo jalan — saat ini core sing-box aktif, ganti ke mihomo dulu untuk melihat grup ini: ' : 'Butuh mihomo jalan (tekan Connect): '}${esc(e.message)}</div>`; }
}

// ================= ROUTES =================
const GEO = 'https://raw.githubusercontent.com/SagerNet/';
const isBoxCore = () => !!(S.settings && S.settings.core === 'singbox');
// "geosite:google" / "geoip:id" -> tag ternormalisasi "geosite-google" / "geoip-id"
function rsGeoTag(tag) {
  const m = /^(geosite|geoip)[-:](.+)$/i.exec((tag || '').trim());
  return m ? m[1].toLowerCase() + '-' + m[2].toLowerCase() : null;
}
// URL otomatis rule-set geo. Hanya sing-box (.srs resmi SagerNet); di mihomo
// tag geo dibaca dari geodata lokal bawaan -> tanpa URL ('').
function rsShortcut(tag) {
  if (!isBoxCore()) return '';
  const gt = rsGeoTag(tag);
  if (!gt) return '';
  return gt.startsWith('geosite') ? `${GEO}sing-geosite/rule-set/${gt}.srs` : `${GEO}sing-geoip/rule-set/${gt}.srs`;
}
// teks kecil daftar rule-set: di mihomo tag geo = geodata lokal (URL tersimpan diabaikan)
const rsSub = r => (!isBoxCore() && rsGeoTag(r.tag)) ? 'geodata lokal bawaan' : (r.url || r.path || '');
function ensureRuleSet(tag, url) {
  S.builder.rule_sets = S.builder.rule_sets || [];
  if (!S.builder.rule_sets.find(x => x.tag === tag))
    S.builder.rule_sets.push({ id: 'x' + Date.now().toString(36) + Math.floor(Math.random() * 999), tag, type: 'remote', format: 'binary', url, detour: 'DIRECT' });
}
function destLabel(o) {
  if (o === 'DIRECT') return 'Bypass';
  if (o === 'REJECT') return 'Blokir';
  const g = (S.builder.groups || []).find(x => x.id === o);
  return g ? 'Grup: ' + g.name : 'Proksi';
}
function destOptions(sel) {
  const gs = (S.builder.groups || []).map(g => `<option value="${esc(g.id)}" ${sel === g.id ? 'selected' : ''}>Grup: ${esc(g.name)}</option>`).join('');
  return `<option value="Proxy" ${sel === 'Proxy' || !sel ? 'selected' : ''}>Proksi</option><option value="DIRECT" ${sel === 'DIRECT' ? 'selected' : ''}>Bypass</option><option value="REJECT" ${sel === 'REJECT' ? 'selected' : ''}>Blokir</option>` + gs;
}
function ruleModal(r0) {
  const r = r0 || {};
  const j = v => (v || []).join(', ');
  modal(r0 ? 'Edit aturan' : 'Tambah aturan', `
    <label class="lbl">Domain (koma)</label><input id="mDom" value="${esc(j(r.domain_suffix))}" placeholder="example.com">
    <label class="lbl">IP CIDR (koma)</label><input id="mIp" value="${esc(j(r.ip_cidr))}" placeholder="1.2.3.0/24">
    <label class="lbl">Rule-set (tag, koma)</label><input id="mRs" value="${esc(j(r.rule_sets))}" list="rsTags" placeholder="geoip-id">
    <datalist id="rsTags">${(S.builder.rule_sets || []).map(x => `<option value="${esc(x.tag)}">`).join('')}</datalist>
    <div class="row2"><div><label class="lbl">Port (koma)</label><input id="mPort" value="${esc(j(r.port))}" placeholder="443, 8443"></div>
    <div><label class="lbl">Protokol (koma)</label><input id="mProto" value="${esc(j(r.protocol))}" placeholder="bittorrent, quic"></div></div>
    <label class="lbl">Tujuan</label><select id="mOut">${destOptions(r.outbound)}</select>
    <label class="lbl">Aplikasi</label>
    <div class="row"><button class="btn small" id="mAppsBtn">Pilih aplikasi…</button><span class="mut" id="mAppsN">${(r.package_names || []).length ? (r.package_names.length + ' dipilih') : ''}</span></div>
    <div id="mAppsPanel" class="hidden">
      <input id="mAppsQ" placeholder="Cari paket… (mis. chrome)" style="margin-top:8px">
      <div class="row" style="margin:6px 0"><button class="btn small" data-af="all">Semua</button><button class="btn small" data-af="user">Pengguna</button><button class="btn small" data-af="sys">Sistem</button></div>
      <div id="mAppsList" class="applist"><div class="mut">Memuat…</div></div>
    </div>
    <div class="hint">Domain / IP / rule-set dalam satu aturan berlaku ATAU. Port, protokol & aplikasi membatasi semuanya (hanya mode TUN).</div>
    <div class="row" style="margin-top:10px"><button class="btn primary" id="mOk">${r0 ? 'Simpan' : 'Tambah'}</button></div>`);
  // ---- pemilih aplikasi (per-app routing) ----
  let selApps = (r.package_names || []).slice(), pkgsCache = null, afMode = 'all';
  const renderApps = () => {
    const q = ($('#mAppsQ').value || '').toLowerCase();
    const list = (pkgsCache || []).filter(p => (afMode === 'all' || (afMode === 'user') !== !!p.system) && (!q || p.name.toLowerCase().includes(q)));
    // Aplikasi terpilih selalu di atas (abjad di dalam tiap kelompok),
    // agar langsung terlihat mana saja yang dipilih di rute ini — dan
    // yang terpilih tak pernah tersembunyi di balik batas 400 baris.
    const sel = new Set(selApps);
    list.sort((a, b) => ((sel.has(b.name) ? 1 : 0) - (sel.has(a.name) ? 1 : 0)) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
    $('#mAppsList').innerHTML = list.slice(0, 400).map(p =>
      `<label class="chk"><div class="t">${esc(p.name)}<small>${p.system ? 'Sistem' : 'Pengguna'}</small></div><input type="checkbox" data-ap="${esc(p.name)}" ${selApps.includes(p.name) ? 'checked' : ''}></label>`).join('')
      || '<div class="mut">Tidak ada. Coba kata kunci lain.</div>';
    if (list.length > 400) $('#mAppsList').innerHTML += `<div class="mut">…${list.length - 400} lainnya, persempit pencarian.</div>`;
    $('#mAppsList').querySelectorAll('[data-ap]').forEach(c => c.onchange = () => {
      const n = c.dataset.ap;
      if (c.checked && !selApps.includes(n)) selApps.push(n);
      if (!c.checked) selApps = selApps.filter(x => x !== n);
      $('#mAppsN').textContent = selApps.length ? selApps.length + ' dipilih' : '';
      // Gambar ulang agar yang baru dipilih langsung naik ke atas
      // (posisi scroll dipertahankan).
      const el = $('#mAppsList'), st = el.scrollTop;
      renderApps();
      el.scrollTop = st;
    });
  };
  $('#mAppsBtn').onclick = async () => {
    const p = $('#mAppsPanel'); p.classList.toggle('hidden');
    if (p.classList.contains('hidden') || pkgsCache) return;
    try { pkgsCache = await api('/api/packages'); }
    catch (e) { $('#mAppsList').innerHTML = `<div class="mut">${esc(e.message)}</div>`; return; }
    renderApps();
  };
  $('#mAppsQ').oninput = renderApps;
  $('#mAppsPanel').querySelectorAll('[data-af]').forEach(b => b.onclick = () => { afMode = b.dataset.af; renderApps(); });
  $('#mOk').onclick = async () => {
    const sp = v => v.split(',').map(x => x.trim()).filter(Boolean);
    const out = { id: r.id || ('x' + Date.now().toString(36)), outbound: $('#mOut').value };
    const dm = sp($('#mDom').value), ip = sp($('#mIp').value), rs = sp($('#mRs').value), pr = sp($('#mPort').value).map(Number).filter(n => n > 0), pt = sp($('#mProto').value);
    if (!dm.length && !ip.length && !rs.length && !pr.length && !pt.length && !selApps.length) return toast('Isi minimal satu kondisi');
    if (dm.length) out.domain_suffix = dm; if (ip.length) out.ip_cidr = ip; if (rs.length) out.rule_sets = rs;
    if (pr.length) out.port = pr; if (pt.length) out.protocol = pt;
    if (selApps.length) out.package_names = selApps;
    if (r.disabled) out.disabled = true;
    if (out.rule_sets && !out.rule_sets.length) delete out.rule_sets;
    try {
      // tulis di atas data TERBARU dari server (bukan S.builder yang bisa basi)
      const bld = await api('/api/builder'); S.builder = bld || {};
      // rule-set shortcut (geosite:xx / geoip:xx) otomatis dibuat di data segar
      if (out.rule_sets) out.rule_sets = out.rule_sets.map(t => { const gt = rsGeoTag(t); if (gt && !/^https?:/.test(t)) { ensureRuleSet(gt, rsShortcut(t)); return gt; } return t; });
      if (out.rule_sets && !out.rule_sets.length) delete out.rule_sets;
      S.builder.rules = S.builder.rules || [];
      if (r0) { const i = S.builder.rules.findIndex(x => x.id === r0.id); S.builder.rules[i] = out; } else S.builder.rules.push(out);
      await api('/api/builder', 'PUT', S.builder);
      $('#modal').classList.add('hidden'); toast('Tersimpan'); vRoutes();
    } catch (e) { toast(e.message); }
  };
}
function vRoutes() {
  const b = S.builder, s = S.settings;
  const rl = (b.rules || []).map(r => {
    const parts = [];
    if (r.rule_sets?.length) parts.push(r.rule_sets.join(','));
    if (r.domain_suffix?.length) parts.push(r.domain_suffix.join(','));
    if (r.ip_cidr?.length) parts.push(r.ip_cidr.join(','));
    if (r.port?.length) parts.push('port ' + r.port.join(','));
    if (r.protocol?.length) parts.push(r.protocol.join(','));
    if (r.package_names?.length) parts.push(r.package_names.length + ' aplikasi');
    return `<div class="chk"><div class="t">${esc(parts.join(' • ') || '(semua)')}<small>→ ${esc(destLabel(r.outbound))}</small></div>
      <input type="checkbox" data-rd="${r.id}" ${r.disabled ? '' : 'checked'} title="Aktif">
      <button class="iconbtn" data-ru="${r.id}" title="Naik">↑</button><button class="iconbtn" data-rn="${r.id}" title="Turun">↓</button>
      <button class="iconbtn" data-re="${r.id}" title="Edit"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16z"/><path d="m13.5 6.5 4 4"/></svg></button><button class="iconbtn" data-rl="${r.id}" title="Hapus"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg></button></div>`;
  }).join('');
  const mgName = S.settings.main_group_name || 'Taptap';
  $('#view').innerHTML = `<div class="card"><h3>Rute</h3>
    <div class="mut" style="margin-bottom:8px">Aturan dievaluasi berurutan dari atas.</div>
    <label class="chk"><div class="t">Blokir Iklan<small>GEOSITE category-ads-all → REJECT (geosite.dat)</small></div><input type="checkbox" id="rAds" ${s.block_ads ? 'checked' : ''}></label>
    <label class="chk"><div class="t">Traffic Sniffing<small>baca domain dari TLS/HTTP</small></div><input type="checkbox" id="rSniff" ${b.no_sniff ? '' : 'checked'}></label>
    <label class="chk"><div class="t">IP Privat → Bypass<small>GEOIP,private → DIRECT (geoip.dat)</small></div><input type="checkbox" id="rPriv" ${s.bypass_private ? 'checked' : ''}></label>
    <div class="hint">Rute akhir (trafik yang tidak cocok aturan): <b>${esc(mgName)}</b> — selalu grup utama.</div>
    <div class="sect">Aturan custom</div>
    ${rl || '<div class="mut">Belum ada.</div>'}
    <div class="row" style="margin-top:10px"><button class="btn small primary" id="rSave">Simpan</button>
    <button class="btn small" id="rAdd">Tambah aturan</button>
    <button class="btn small" id="rApply">Terapkan (restart)</button></div>
  </div>
  <div class="card"><h3>Rule-set</h3><div id="rsBox">${(b.rule_sets || []).map(r =>
    `<div class="chk"><div class="t">${esc(r.tag)}<small>${esc(r.type)} • ${esc(rsSub(r))}</small></div>
     <button class="iconbtn" data-rs="${r.id}"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg></button></div>`).join('') || '<div class="mut">Belum ada.</div>'}</div>
    <button class="btn small" id="rsAdd" style="margin-top:8px">Tambah rule-set</button>
    <div class="hint">${isBoxCore() ? 'Tag <code>geosite:nama</code> atau <code>geoip:id</code> otomatis memakai URL .srs resmi SagerNet bila URL dikosongkan.' : 'Tag <code>geosite:nama</code> atau <code>geoip:id</code> memakai geodata lokal bawaan mihomo (tanpa unduh). Rule-set lain: isi URL file .mrs / .yaml.'}</div>
    <div class="sect" style="margin-top:14px">File di folder rulesets</div>
    <div id="rsFilesBox"><div class="mut">Memuat…</div></div>
    <div class="hint">Copy manual file (.srs / .mrs / .yaml / dll) ke <code id="rsFilesDir">…</code> — file langsung tampil di sini tanpa unduh, lalu tekan Pakai agar bisa dipakai aturan. Hapus di sini ikut menghapus file di folder.</div></div>`;

  const saveAll = async () => {
    // ambil builder terbaru dulu agar tidak menimpa perubahan dari sesi lain
    const bld = await api('/api/builder'); S.builder = bld || {};
    S.builder.no_sniff = !$('#rSniff').checked;
    delete S.builder.final; // dihapus: MATCH akhir selalu grup utama
    await api('/api/builder', 'PUT', S.builder);
    await api('/api/settings', 'PUT', { block_ads: $('#rAds').checked, bypass_private: $('#rPriv').checked });
  };
  $('#rSave').onclick = async () => { try { await saveAll(); toast('Rute tersimpan — tekan Terapkan'); await reload(); } catch (e) { toast(e.message); } };
  $('#rApply').onclick = async () => { try { await saveAll(); toast('Menerapkan…'); await doApply(); await reload(); } catch (e) { toast(e.message); } };
  $('#rAdd').onclick = () => ruleModal(null);
  $('#rsAdd').onclick = () => {
    modal('Tambah rule-set', `
      <label class="lbl">Tag</label><input id="mTag" placeholder="geosite:google atau geoip:id">
      <label class="lbl">${isBoxCore() ? 'URL (.srs) — kosong = otomatis dari tag' : 'URL (.mrs / .yaml) — tag geosite:/geoip: boleh kosong (geodata lokal)'}</label><input id="mUrl" placeholder="${isBoxCore() ? 'https://…/geosite-id.srs' : 'https://…/geosite-id.mrs'}">
      <div class="row" style="margin-top:10px"><button class="btn primary" id="mOk">Tambah</button></div>`);
    $('#mOk').onclick = async () => {
      let tag = $('#mTag').value.trim(); if (!tag) return toast('Isi tag');
      let url = $('#mUrl').value.trim();
      if (!url) { const gt = rsGeoTag(tag); if (!gt) return toast(isBoxCore() ? 'Isi URL atau pakai tag geosite:xxx / geoip:xx' : 'Isi URL (.mrs/.yaml) atau pakai tag geosite:xxx / geoip:xx'); tag = gt; url = rsShortcut(tag); }
      try {
        const bld = await api('/api/builder'); S.builder = bld || {};
        ensureRuleSet(tag, url);
        await api('/api/builder', 'PUT', S.builder); $('#modal').classList.add('hidden'); toast('Ditambah'); vRoutes();
      } catch (e) { toast(e.message); }
    };
  };
  // Daftar file di folder rulesets core aktif: isi folder apa adanya
  // (termasuk hasil copy manual) + tombol Pakai (daftar sebagai lokal)
  // dan hapus (ikut menghapus file di disk).
  const rsCore = isBoxCore() ? 'singbox' : 'clash';
  const rsUsable = f => isBoxCore() ? ['.srs', '.json', '.yaml', '.yml'].includes(f.ext) : ['.mrs', '.yaml', '.yml', '.txt', '.list'].includes(f.ext);
  const rsFmt = n => n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KB';
  const loadRsFiles = async () => {
    const box = $('#rsFilesBox'); if (!box) return;
    try {
      const d = await api('/api/ruleset-files?core=' + rsCore);
      const dirEl = $('#rsFilesDir'); if (dirEl) dirEl.textContent = d.dir || '';
      box.innerHTML = (d.files || []).map(f =>
        `<div class="chk"><div class="t">${esc(f.name)}<small>${rsFmt(f.size)} • ${f.registered ? 'terdaftar' : 'belum terdaftar'}</small></div>
         ${!f.registered && rsUsable(f) ? `<button class="btn small" data-rsuse="${esc(f.name)}">Pakai</button>` : ''}
         <button class="iconbtn" data-rsfile="${esc(f.name)}"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg></button></div>`).join('') || '<div class="mut">Folder kosong.</div>';
    } catch (e) { box.innerHTML = '<div class="mut">Gagal memuat: ' + esc(e.message) + '</div>'; }
  };
  loadRsFiles();
  $('#view').onclick = async ev => {
    const t = ev.target;
    const q = k => t.closest('[' + k + ']');
    let el;
    // edit hanya membaca (modal menyimpan sendiri dengan data segar)
    if ((el = q('data-re'))) { const r = S.builder.rules.find(x => x.id === el.dataset.re); if (r) ruleModal(r); return; }
    // Operasi tulis: ambil builder TERBARU dari server dulu. S.builder bisa basi
    // (mis. aturan ditambah dari tab/sesi lain, atau halaman direstore setelah
    // reboot) — PUT dengan data basi akan MENGHAPUS aturan yang sudah tersimpan.
    let bld;
    try { bld = await api('/api/builder'); S.builder = bld = bld || {}; }
    catch (e) { toast(e.message); return; }
    const save = async () => { try { await api('/api/builder', 'PUT', bld); vRoutes(); } catch (e) { toast(e.message); } };
    const move = (id, d) => { const rs = bld.rules; const i = rs.findIndex(x => x.id === id), j = i + d; if (i < 0 || j < 0 || j >= rs.length) return false; [rs[i], rs[j]] = [rs[j], rs[i]]; return true; };
    if ((el = q('data-rd'))) { const r = bld.rules.find(x => x.id === el.dataset.rd); if (r) { r.disabled = !t.checked; await api('/api/builder', 'PUT', bld).catch(e => toast(e.message)); } return; }
    if ((el = q('data-ru'))) { if (move(el.dataset.ru, -1)) await save(); return; }
    if ((el = q('data-rn'))) { if (move(el.dataset.rn, 1)) await save(); return; }
    if ((el = q('data-rl'))) { if (confirm('Hapus aturan?')) { bld.rules = bld.rules.filter(x => x.id !== el.dataset.rl); await save(); } return; }
    if ((el = q('data-rs'))) {
      const ent = bld.rule_sets.find(x => x.id === el.dataset.rs);
      if (!ent) { vRoutes(); return; }
      const used = (bld.rules || []).some(r => (r.rule_sets || []).includes(ent.tag));
      if (confirm('Hapus rule-set?' + (used ? ' Masih dipakai ' + (bld.rules || []).filter(r => (r.rule_sets || []).includes(ent.tag)).length + ' aturan.' : '') + ' File cache-nya di folder ikut terhapus.')) {
        try { await api('/api/ruleset-files', 'DELETE', { core: rsCore, tag: ent.tag }); } catch (e) { toast(e.message); }
        vRoutes();
      }
      return;
    }
    if ((el = q('data-rsfile'))) {
      if (confirm('Hapus file ' + el.dataset.rsfile + ' dari folder? Entri rule-set yang memakainya ikut dicabut.')) {
        try { await api('/api/ruleset-files', 'DELETE', { core: rsCore, name: el.dataset.rsfile }); } catch (e) { toast(e.message); }
        vRoutes();
      }
      return;
    }
    if ((el = q('data-rsuse'))) {
      const name = el.dataset.rsuse;
      try {
        const d = await api('/api/ruleset-files?core=' + rsCore);
        const f = (d.files || []).find(x => x.name === name);
        if (!f) return toast('File tidak ditemukan');
        const push = async (behavior) => {
          const bld2 = await api('/api/builder'); S.builder = bld2 || {};
          S.builder.rule_sets = S.builder.rule_sets || [];
          if (S.builder.rule_sets.find(x => x.tag === f.tag)) return toast('Sudah terdaftar');
          const ent2 = { id: 'x' + Date.now().toString(36) + Math.floor(Math.random() * 999), tag: f.tag, type: 'local', format: 'binary', path: f.relpath };
          if (behavior) ent2.behavior = behavior;
          S.builder.rule_sets.push(ent2);
          await api('/api/builder', 'PUT', S.builder); toast('Ditambah sebagai rule-set lokal'); vRoutes();
        };
        if (!isBoxCore()) {
          modal('Pakai file rule-set', `<div class="mut">File <b>${esc(name)}</b> didaftarkan sebagai rule-set lokal mihomo (tag: <b>${esc(f.tag)}</b>).</div>
            <label class="lbl">Jenis isi</label><select id="mBehav"><option value="domain">Domain</option><option value="ipcidr">IP CIDR</option></select>
            <div class="row" style="margin-top:10px"><button class="btn primary" id="mOk">Pakai</button></div>`);
          $('#mOk').onclick = async () => { $('#modal').classList.add('hidden'); try { await push($('#mBehav').value); } catch (e) { toast(e.message); } };
        } else {
          await push('');
        }
      } catch (e) { toast(e.message); }
      return;
    }
  };
}

// ================= SETTINGS =================
function vSettings() {
  const s = S.settings;
  const lblClash = 'Clash (mihomo' + ((S.status && S.status.mihomo_version) ? ' ' + S.status.mihomo_version : '') + ')';
  const lblBox = 'sing-box' + ((S.status && S.status.singbox_version) ? ' ' + S.status.singbox_version : '');
  $('#view').innerHTML = `<div class="card"><h3><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h9M19 7h1M4 17h1M11 17h9"/><circle cx="16" cy="7" r="2.2"/><circle cx="8" cy="17" r="2.2"/></svg>Pengaturan</h3>
    <div class="sect">VPN</div>
    <div class="hint">Core: <b>sing-box</b> (satu-satunya di TaptapBox, library tertanam). Config/override/manual ada di menu Config.</div>
    <label class="lbl">Mode proxy</label><select id="sPMode">
      ${[
        ['tun', 'TUN Mode (semua TCP+UDP)'],
        ['tproxy', 'TProxy (Recommended, TCP+UDP)'],
        ['iptables', 'Redirect (iptables TCP)'],
        ['mixed', 'Mixed (TCP Redirect + UDP TProxy)'],
        ['enhance', 'Enhance (TUN + proteksi penuh)'],
      ].map(([v, t]) => `<option value="${v}" ${(s.proxy_mode || 'tun') === v ? 'selected' : ''}>${t}</option>`).join('')}
    </select>
    <div class="hint" id="pmHint"></div>
    <label class="lbl">MTU TUN</label><input id="sMtu" type="number" value="${s.tun_mtu || 9000}">
    <label class="lbl">Kecualikan aplikasi (koma)</label><input id="sExcl" value="${esc(s.exclude_packages || '')}" placeholder="com.bca.mobile">
    ${(s.core || 'clash') === 'singbox' ? '<div class="hint">Di core sing-box, pengecualian aplikasi mode TUN didukung.</div>' : '<div class="hint">Catatan: core mihomo tidak mendukung pengecualian aplikasi di mode TUN — pengaturan ini diabaikan.</div>'}
    <label class="chk"><div class="t">Auto-start saat boot</div><input type="checkbox" id="sAuto" ${s.auto_start ? 'checked' : ''}></label>
    <label class="chk"><div class="t">Transparent proxy</div><input type="checkbox" id="sTp" ${s.transparent_proxy ? 'checked' : ''}></label>
    <label class="lbl">Level log core</label><select id="sLog">
      ${['warn', 'error', 'info', 'debug'].map(l => `<option value="${l}" ${(s.log_level || 'warn') === l ? 'selected' : ''}>${l}${l === 'warn' ? ' (disarankan, hemat baterai)' : l === 'info' ? ' (catat tiap koneksi — boros)' : ''}</option>`).join('')}
    </select>

    <div class="sect">DNS</div>
    <label class="lbl">Remote DNS (via proksi)</label><input id="sDnsR" value="${esc(s.dns_remote || '')}">
    <label class="lbl">Direct DNS</label><input id="sDnsD" value="${esc(s.dns_direct || '')}">
    <label class="lbl">Domain bypass (koma)</label><input id="sBypass" value="${esc(s.bypass_domains || '')}" placeholder="googlevideo.com">

    <div class="sect">Inbound</div>
    <div class="row2"><div><label class="lbl">Port SOCKS5 (mixed)</label><input id="sMixed" type="number" value="${s.mixed_port || 10809}"></div>
    <div><label class="lbl">Port Clash API</label><input id="sCPort" type="number" value="${s.clash_port || 9090}"></div></div>
    <label class="lbl">Secret Clash API (kosong = tanpa password)</label><input id="sSecret" value="${esc(s.secret || '')}">

    <div class="sect">BadVPN (UDP lewat SSH)</div>
    <label class="chk"><div class="t">UDP lewat SSH via BadVPN<small>untuk server SSH saja (game/QUIC); butuh badvpn-udpgw jalan di server SSH</small></div><input type="checkbox" id="sBadvpn" ${s.badvpn_enabled ? 'checked' : ''}></label>
    <label class="lbl">Port UDPGW server</label><input id="sBadvpnPort" type="number" value="${s.badvpn_udpgw_port || 7300}">
    <div class="hint">Hanya berlaku bila server terpilih berprotokol SSH dan mode config OTOMATIS (bukan MANUAL). Di mode iptables (REDIRECT TCP saja) UDP tidak lewat core sehingga tidak berlaku. Simpan & Terapkan agar core restart dengan aturan baru; log BadVPN ada di Logs.</div>

    <div class="sect">Lainnya</div>
    <label class="lbl">URL tes koneksi</label><input id="sHealth" value="${esc(s.health_url || '')}">
    <label class="chk"><div class="t">Auto-test berkala<small>tiap 3 menit, hanya saat WebUI sedang dibuka (hemat baterai)</small></div><input type="checkbox" id="sAT" ${s.auto_test ? 'checked' : ''}></label>
    <label class="chk"><div class="t">DNS hijack<small>UDP/53 → stub lokal (level netfilter, berlaku juga untuk config manual)</small></div><input type="checkbox" id="sDnsH" ${s.dns_hijack ? 'checked' : ''}></label>
    <label class="chk"><div class="t">Blokir QUIC (UDP 443)<small>paksa fallback ke TCP (level netfilter)</small></div><input type="checkbox" id="sQuic" ${s.block_quic ? 'checked' : ''}></label>
    <label class="chk"><div class="t">Blokir IPv6<small>cegah bocor IPv6 saat VPN aktif (level netfilter)</small></div><input type="checkbox" id="sV6" ${s.block_ipv6 ? 'checked' : ''}></label>

    <div class="row" style="margin-top:12px"><button class="btn primary" id="sSave"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 4h11l3 3v13H5z"/><path d="M8 4v5h7V4M8 20v-6h8v6"/></svg>Simpan</button>
    <button class="btn" id="sApply"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 15l-3-3a20 20 0 0 1 2-4 12 12 0 0 1 9-5c0 3-1 7-5 11a22 22 0 0 1-4 2z"/><path d="M9 12H4s.5-3 2-4c1.3-.9 4 0 4 0M12 15v5s3-.5 4-2c.9-1.3 0-4 0-4"/></svg>Simpan & Terapkan</button></div>
    <div class="hint">Mengganti mode/MTU/port/bypass butuh restart ${coreName()}.</div></div>`;

  const get = () => ({
    core: 'singbox', proxy_mode: $('#sPMode').value, tun_mtu: +$('#sMtu').value || 9000,
    exclude_packages: $('#sExcl').value.trim(), auto_start: $('#sAuto').checked,
    transparent_proxy: $('#sTp').checked, dns_remote: $('#sDnsR').value.trim(),
    block_ipv6: $('#sV6').checked, dns_hijack: $('#sDnsH').checked,
    dns_direct: $('#sDnsD').value.trim(), bypass_domains: $('#sBypass').value.trim(),
    mixed_port: +$('#sMixed').value || 10809, clash_port: +$('#sCPort').value || 9090,
    secret: $('#sSecret').value, health_url: $('#sHealth').value.trim(),
    auto_test: $('#sAT').checked, block_quic: $('#sQuic').checked,
    log_level: $('#sLog').value,
    badvpn_enabled: $('#sBadvpn').checked, badvpn_udpgw_port: +$('#sBadvpnPort').value || 7300,
  });
  const modeHints = {
    tun: 'Semua trafik lewat tun0.',
    tproxy: 'TCP+UDP via iptables TPROXY \u2014 tanpa TUN.',
    iptables: 'iptables REDIRECT klasik (TCP 80/443).',
    mixed: 'TCP via Redirect + UDP via TProxy.',
    enhance: 'TUN + proteksi dipaksa penuh (DNS hijack, blokir QUIC & IPv6).',
  };
  const updPmHint = async () => {
    const m = $('#sPMode').value;
    const el = $('#pmHint'); if (el) el.textContent = modeHints[m] || '';
  };
  $('#sPMode').onchange = updPmHint; updPmHint();
  $('#sSave').onclick = async () => { try { const r = await api('/api/settings', 'PUT', get()); toast(r.restart_backend ? 'Tersimpan — restart backend agar port/bind baru berlaku' : 'Tersimpan'); await reload(); } catch (e) { toast(e.message); } };
  $('#sApply').onclick = async () => { try { const r = await api('/api/settings', 'PUT', get()); if (r && r.override_warn) toast('⚠ Override: ' + r.override_warn); await doApply(); await reload(); } catch (e) { toast(e.message); } };
}

// ================= LOGS =================
async function vLogs() {
  $('#view').innerHTML = `<div class="card"><h3><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 3h8l4 4v14H7z"/><path d="M15 3v4h4M10 12h6M10 16h6"/></svg>Logs</h3>
    <div class="row" style="margin-bottom:8px">${(S.settings && S.settings.core === 'singbox') ? '<button class="btn small" id="lX">sing-box</button>' : '<button class="btn small" id="lS">mihomo</button>'}
    <button class="btn small" id="lB">backend</button>
    <button class="btn small" id="lH">SSH</button>
    <button class="btn small" id="lR"><svg class="ic solo" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14-4M4 5v3h3M4 13a8 8 0 0 0 14 4m2 2v-3h-3"/></svg></button></div>
    <div class="hint" id="sshHint" style="display:none">SSH beda dari vmess/trojan/vless: dia lewat jembatan, bukan langsung core. Di bawah ini semua akun SSH, bukan cuma yang terpilih. Reconnect otomatis aktif — kalau putus, jembatan mencoba lagi sendiri sampai 3 kali, tidak perlu tekan apa pun.</div>
    <div id="sshSum" style="display:none;margin-bottom:8px"></div>
    <div class="logbox" id="logBox">Memuat…</div></div>`;
  let type = (S.settings && S.settings.core === 'singbox') ? 'singbox' : 'mihomo';
  const load = async () => {
    const sum = $('#sshSum'), hint = $('#sshHint');
    if (sum) sum.style.display = type === 'ssh' ? '' : 'none';
    if (hint) hint.style.display = type === 'ssh' ? '' : 'none';
    if (type === 'ssh') {
      try {
        const r = await api('/api/ssh-log');
        const acc = r.accounts || [];
        if (!acc.length) { if (sum) sum.innerHTML = '<div class="mut">Belum ada akun SSH.</div>'; }
        else if (sum) sum.innerHTML = acc.map(a => {
          const last = a.last;
          const st = last ? (last.ok ? '<span class="ms">Terhubung</span>' : '<span class="bad">Gagal</span>') : '<span class="mut">Belum pernah dicoba</span>';
          const det = last ? esc(last.detail || '') + (last.time ? ' · ' + esc(last.time) : '') : '';
          return `<div style="padding:6px 0;border-bottom:1px solid #222"><b>${esc(a.name)}</b> · ${esc(a.mode)}${a.bridge ? ' · jembatan 127.0.0.1:' + a.bridge_port : ''}<br>${st}${det ? ' · ' + det : ''}</div>`;
        }).join('');
      } catch (e) { if (sum) sum.innerHTML = '<div class="mut">' + esc(e.message) + '</div>'; }
    }
    try { const r = await api('/api/logs?type=' + type); $('#logBox').textContent = r.logs || '(kosong)'; $('#logBox').scrollTop = 1e9; }
    catch (e) { $('#logBox').textContent = e.message; }
  };
  if ($('#lS')) $('#lS').onclick = () => { type = 'mihomo'; load(); };
  if ($('#lX')) $('#lX').onclick = () => { type = 'singbox'; load(); };
  $('#lB').onclick = () => { type = 'backend'; load(); };
  $('#lH').onclick = () => { type = 'ssh'; load(); };
  $('#lR').onclick = load;
  await load();
}

// ================= CONFIG JSON (config kustom sing-box) =================
async function vConfig() {
  const isManual = !!S.settings.manual_config;
  $('#view').innerHTML = `<div class="card"><h3>Config sing-box</h3>
    <div class="hint" id="cfgMode">Mode: <b>${isManual ? 'MANUAL penuh' : 'OTOMATIS (dibuat dari server + rute + pengaturan)'}</b><br>File aktif: <code>${esc(S.status.config_file || '')}</code>${isManual ? '<br>Mode manual tidak digabung dengan server/grup/rute — isi file dipakai apa adanya.' : ''}</div>
    <textarea id="cfgBox" class="logbox" style="width:100%;min-height:320px;margin-top:8px;font-family:monospace" spellcheck="false">Memuat…</textarea>
    <div class="row" style="margin-top:10px"><button class="btn small" id="cfgLoad">Muat config aktif</button>
    ${isManual ? '' : '<button class="btn small primary" id="cfgOverride">Terapkan override</button>'}
    <button class="btn small ${isManual ? 'primary' : ''}" id="cfgApply">Terapkan sebagai manual</button>
    <button class="btn small" id="cfgAuto">Kembali ke otomatis</button>
    <button class="btn small" id="cfgCheck">Validasi</button>
    <button class="btn small" id="cfgCopy">Salin</button></div>
    <div class="hint">${isManual ? 'Config manual divalidasi dengan validasi library sing-box sebelum dipakai.' : 'Mode otomatis: config di bawah <b>bisa langsung diedit</b> (mis. ubah DNS) lalu tekan <b>Terapkan override</b> — tetap mode otomatis, bukan manual penuh.<br>Edit grup/rute/server tetap bisa saat override aktif: bagian <code>outbounds</code>/<code>route</code> otomatis disegarkan dari builder tanpa menghapus editan lain. Inbound otomatis mengikuti mode proxy aktif — aman ganti mode saat override aktif.<br>"Kembali ke otomatis" menghapus override dan memunculkan lagi config bawaan (tetap bisa diedit lagi).'} Perubahan baru berlaku langsung bila sedang terhubung.</div></div>
    <div class="card"><h3>Backup &amp; Restore</h3>
    <textarea id="bkBox" class="logbox" style="width:100%;min-height:140px;font-family:monospace" spellcheck="false" placeholder="Tekan Ambil backup, atau tempel backup lalu Restore"></textarea>
    <div class="row" style="margin-top:10px"><button class="btn small" id="bkGet">Ambil backup</button>
    <button class="btn small" id="bkCopy">Salin</button>
    <button class="btn small primary" id="bkPut">Restore dari teks</button></div>
    <div class="hint">Berisi server, langganan, pengaturan, grup, dan rute.</div></div>`;
  const copyText = async (el, label) => {
    try { await navigator.clipboard.writeText(el.value); toast(label + ' disalin'); }
    catch (e) { el.select(); toast('Tekan lama lalu Salin'); }
  };
  $('#cfgCopy').onclick = () => copyText($('#cfgBox'), 'Config');
  $('#cfgCheck').onclick = async () => {
    const yml = $('#cfgBox').value;
    if (!yml.trim()) return toast('Config kosong');
    try { toast('Memvalidasi…'); await api('/api/config/check', 'POST', { yaml: yml }); toast('Config valid ✓'); }
    catch (e) { toast(e.message); }
  };
  $('#bkGet').onclick = async () => { try { $('#bkBox').value = JSON.stringify(await api('/api/backup'), null, 2); } catch (e) { toast(e.message); } };
  $('#bkCopy').onclick = () => copyText($('#bkBox'), 'Backup');
  $('#bkPut').onclick = async () => {
    let d; try { d = JSON.parse($('#bkBox').value); } catch (e) { return toast('JSON tidak valid'); }
    if (!confirm('Timpa server, langganan, pengaturan, grup & rute dengan backup ini?')) return;
    try { const r = await api('/api/backup', 'POST', d); toast('Restore selesai'); if (r && r.override_warn) toast('⚠ Override: ' + r.override_warn); await reload(); vConfig(); } catch (e) { toast(e.message); }
  };
  const load = async () => {
    try {
      const r = await api('/api/config/raw');
      $('#cfgBox').value = r.yaml || '';
      if (r.override && !S.settings.manual_config) {
        const m = $('#cfgMode');
        if (m) m.innerHTML = 'Mode: <b>OTOMATIS + override</b> (hasil editan — tetap bisa diedit)<br>File aktif: <code>' + esc(S.status.config_file || '') + '</code>';
      }
    }
    catch (e) { $('#cfgBox').value = e.message; }
  };
  $('#cfgLoad').onclick = load;
  $('#cfgApply').onclick = async () => {
    const yml = $('#cfgBox').value;
    if (!yml.trim()) return toast('Config kosong');
    try { toast('Memvalidasi…'); await api('/api/config/manual', 'PUT', { enabled: true, yaml: yml }); toast('Config manual aktif'); await reload(); vConfig(); }
    catch (e) { toast(e.message); }
  };
  const ovBtn = $('#cfgOverride');
  if (ovBtn) ovBtn.onclick = async () => {
    const yml = $('#cfgBox').value;
    if (!yml.trim()) return toast('Config kosong — isi dulu atau tekan "Kembali ke otomatis" untuk hapus override');
    try { toast('Memvalidasi…'); const r = await api('/api/config/override', 'PUT', { yaml: yml }); toast(r.override ? 'Override aktif — tetap mode otomatis' : 'Override dihapus'); await reload(); vConfig(); }
    catch (e) { toast(e.message); }
  };
  $('#cfgAuto').onclick = async () => {
    try { await api('/api/config/manual', 'PUT', { enabled: false }); toast('Mode otomatis'); await reload(); vConfig(); }
    catch (e) { toast(e.message); }
  };
  await load();
}

// ================= ABOUT =================
function vAbout() {
  $('#view').innerHTML = `<div class="card"><h3><span class="dlogo sm"><svg class="ic" viewBox="0.8 0.6 22.4 22.4" aria-hidden="true"><use href="#cklogo"/></svg></span>TaptapBox</h3>
    <div class="kv"><span>Versi</span><b>${esc((S.status.version || '').replace(/ \([^)]*\)$/, ''))}</b></div>
    <div class="kv"><span>Core</span><b>sing-box ${S.status.singbox_version || '1.14.3'} (library tertanam, aktif)</b></div>
    <div class="lnks"><a class="btn small primary" href="https://t.me/TapTap_56" target="_blank" rel="noopener"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M2 21l21-9L2 3v7l15 2-15 2z" transform="translate(12 12) rotate(-45) scale(0.85) translate(-12 -12)" fill="currentColor" stroke="none"/></svg>Profil</a><a class="btn small primary" href="https://t.me/Taptapapp_56" target="_blank" rel="noopener"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M2 21l21-9L2 3v7l15 2-15 2z" transform="translate(12 12) rotate(-45) scale(0.85) translate(-12 -12)" fill="currentColor" stroke="none"/></svg>Grup</a></div>
    <div class="hint">Modul Magisk — transparent proxy TUN/iptables + WebUI.</div></div>
  <div class="card"><h3>Rilis terbaru</h3><div id="relWrap"><div class="hint">Memeriksa rilis terbaru…</div></div></div>
  <div class="card"><h3>Download</h3><div id="dlWrap"><div class="hint">Memeriksa versi baru…</div></div></div>`;
  loadUpdates(false);
  loadSelfRelease();
}

// ---------- kartu Rilis terbaru di Tentang (mirip halaman Release GitHub,
// tombolnya gaya TaptapBox) ----------
// Data dari /api/release/latest (backend yang cek GitHub, cache 1 jam
// seperti Download, ?refresh=1 memaksa cek segar); tombol Download
// mengarah ke aset zip instalasi rilis terbaru, tombol Lihat di GitHub
// mengarah ke halaman rilis itu. Bila cek gagal, tetap tampil tombol
// ke https://github.com/Taptap56/Taptapapp/releases/latest, yang selalu
// meneruskan ke rilis terbaru — jadi tombol tidak pernah buntu.
async function loadSelfRelease(force) {
  const wrap = $('#relWrap');
  if (!wrap) return;
  if (force) wrap.innerHTML = `<div class="hint">Memeriksa rilis terbaru…</div>`;
  // Periksa lagi (mirip Download): di semua keadaan — berhasil, gagal,
  // maupun error fetch — tombol ini selalu ada, jadi tidak perlu
  // keluar-masuk Tentang untuk cek ulang.
  const attachRefresh = () => { const b = wrap.querySelector('#relRefresh'); if (b) b.onclick = () => loadSelfRelease(true); };
  const fallback = `<div class="hint">Gagal memeriksa rilis terbaru.</div><div class="lnks"><a class="btn small primary" href="https://github.com/Taptap56/Taptapapp/releases/latest" target="_blank" rel="noopener">Lihat rilis terbaru</a><button class="btn small" id="relRefresh">Periksa lagi</button></div>`;
  try {
    const d = await api('/api/release/latest' + (force ? '?refresh=1' : ''));
    if (!d || d.error || !d.tag) { wrap.innerHTML = fallback; attachRefresh(); return; }
    const title = d.name || d.tag;
    const date = (d.published_at || '').slice(0, 10);
    const sizeMB = d.asset_size ? (d.asset_size / 1048576).toFixed(2) + ' MB' : '';
    const isLatest = (d.current || '') === d.tag;
    const body = (d.body || '').trim();
    const bodyHtml = body ? `<div class="hint">${esc(body.length > 300 ? body.slice(0, 300) + '…' : body)}</div>` : '';
    const assetRow = d.asset_name ? `<div class="kv"><span>Aset</span><b>${esc(d.asset_name)}${sizeMB ? ' · ' + sizeMB : ''}</b></div>` : '';
    const btns = [];
    if (d.asset_url) btns.push(`<a class="btn small primary" href="${esc(d.asset_url)}" target="_blank" rel="noopener">Download</a>`);
    if (d.html_url) btns.push(`<a class="btn small" href="${esc(d.html_url)}" target="_blank" rel="noopener">Lihat di GitHub</a>`);
    btns.push(`<a class="btn small" href="https://github.com/Taptap56/Taptapapp/releases/latest" target="_blank" rel="noopener">Rilis terbaru</a>`);
    // Semua tombol di satu baris .lnks yang sama (flex:1), jadi lebarnya
    // sama rata — termasuk Periksa lagi, bukan tombol kecil terpisah.
    btns.push(`<button class="btn small" id="relRefresh">Periksa lagi</button>`);
    wrap.innerHTML = `<div class="kv"><span>Rilis</span><b>${esc(title)}</b></div>`
      + `<div class="kv"><span>Tag</span><b>${esc(d.tag)}</b></div>`
      + (date ? `<div class="kv"><span>Tanggal</span><b>${esc(date)}</b></div>` : '')
      + assetRow
      + `<div class="hint">${isLatest ? 'Ini sudah rilis terbaru.' : 'Versi terpasang ' + esc(d.current || '?') + ' · rilis terbaru ' + esc(d.tag) + '.'}</div>`
      + bodyHtml
      + `<div class="lnks">${btns.join('')}</div>`;
    attachRefresh();
  } catch (e) { wrap.innerHTML = fallback; attachRefresh(); }
}

// ---------- kotak Download di Tentang (urutan: mihomo, sing-box, geodata, zashboard, scrcpy) ----------
function updBtn(item, name, ver, state, note) {
  if (state === 'update') return `<button class="btn small primary" data-dl="${item}" data-name="${esc(name)}" data-ver="${esc(ver)}" data-note="${esc(note)}">Download</button>`;
  if (state === 'latest') return `<button class="btn small" disabled>Sudah terbaru</button>`;
  return `<button class="btn small" disabled title="Versi baru belum diketahui">Download</button>`;
}
function updSection(title, curRow, newRow, btnHtml) {
  return `<div class="sect">${title}</div>${curRow}${newRow}<div class="kv"><span></span>${btnHtml}</div>`;
}
function updItemRow(label, val, attrs) { return `<div class="kv"${attrs ? ' ' + attrs : ''}><span>${label}</span><b>${esc(val)}</b></div>`; }
async function loadUpdates(force) {
  const wrap = $('#dlWrap');
  if (!wrap) return;
  try {
    const d = await api('/api/updates' + (force ? '?refresh=1' : ''));
    const active = S.status.core === 'singbox' ? 'singbox' : 'clash';
    const coreNote = it => (it === active ? ' Core aktif akan di-restart.' : ' Berlaku saat core ini dipakai.');
    const coreSection = (id, title, prod, o, note) => updSection(title,
      updItemRow('Versi saat ini', o.installed === false ? 'Belum terpasang' : prod + ' ' + (o.current || '?')),
      updItemRow('Versi baru', o.latest ? prod + ' ' + o.latest : (o.error ? 'Gagal memeriksa' : '—'),
        o.latest ? `data-verlist="${id}" data-name="${esc(title)}" data-current="${esc(o.current || '')}" data-latest="${esc(o.latest)}" data-note="${esc(note)}" data-pinned="${o.pinned ? 1 : 0}" title="Ketuk untuk memilih versi"` : ''),
      updBtn(id, title, o.latest, o.update ? 'update' : (o.latest ? 'latest' : 'unknown'), note))
      + (o.pinned ? `<div class="hint">Unduhan pertama dikunci ke ${esc(prod)} ${esc(o.latest)} (versi teruji).</div>` : '');
    const geoBtn = d.geodata.latest
      ? `<button class="btn small primary" data-dl="geodata" data-name="Geodata" data-ver="${esc(d.geodata.latest)}" data-note=" Core aktif akan di-restart.">Download</button>`
      : `<button class="btn small" disabled title="Versi baru belum diketahui">Download</button>`;
    // Urutan final (koreksi user): mihomo, sing-box, geodata, zashboard, scrcpy.
    // Bila ada item gagal dicek, tampilkan keterangan error-nya + tombol
    // Periksa lagi (selalu ada di bawah kartu, memaksa cek ulang).
    // Error Go diawali 'Get "URL": ' — buang prefix URL-nya agar hint
    // tidak sepanjang layar; penyebab yang sama cukup tampil sekali.
    const shortErr = e => e.replace(/^Get\s+"[^"]+":\s*/, '');
    const errs = [...new Set([d.singbox.error, d.scrcpy.error, d.geodata.error, d.zashboard.error].filter(Boolean).map(shortErr))];
    let tail = '';
    if (errs.length) tail += `<div class="hint">Gagal memeriksa: ${esc(errs.join('; '))}</div>`;
    tail += `<div class="kv"><span></span><button class="btn small" id="dlRefresh">Periksa lagi</button></div>`;
    wrap.innerHTML = updSection('Core sing-box (library)', updItemRow('Versi saat ini', 'sing-box ' + (d.singbox.current || '1.14.3') + ' · tertanam di taptapbox'), updItemRow('Versi baru', '— (ikut update modul, tidak diunduh terpisah)'), '')
      + updSection('Geodata', updItemRow('Geoip.dat', d.geodata.geoip.size === '-' ? 'Belum terpasang' : d.geodata.geoip.size + ' · ' + d.geodata.geoip.date)
        + updItemRow('Geosite.dat', d.geodata.geosite.size === '-' ? 'Belum terpasang' : d.geodata.geosite.size + ' · ' + d.geodata.geosite.date),
        updItemRow('Versi baru', d.geodata.latest ? 'Rilis ' + d.geodata.latest : (d.geodata.error ? 'Gagal memeriksa' : '—'),
          d.geodata.latest ? `data-verlist="geodata" data-name="Geodata" data-current="" data-latest="${esc(d.geodata.latest)}" data-note=" Core aktif akan di-restart." data-pinned="0" title="Ketuk untuk memilih versi"` : ''),
        geoBtn)
      + updSection('Zashboard', updItemRow('Versi saat ini', d.zashboard.installed === false ? 'Belum terpasang' : (d.zashboard.current ? 'zashboard ' + d.zashboard.current : 'Terpasang (bawaan modul)')),
        updItemRow('Versi baru', d.zashboard.latest ? 'zashboard ' + d.zashboard.latest : (d.zashboard.error ? 'Gagal memeriksa' : '—'),
          d.zashboard.latest ? `data-verlist="zashboard" data-name="Zashboard" data-current="${esc(d.zashboard.current || '')}" data-latest="${esc(d.zashboard.latest)}" data-note=" Core aktif akan di-restart." data-pinned="0" title="Ketuk untuk memilih versi"` : ''),
        updBtn('zashboard', 'Zashboard', d.zashboard.latest, d.zashboard.update ? 'update' : (d.zashboard.latest ? 'latest' : 'unknown'), ' Core aktif akan di-restart.'))
      + coreSection('scrcpy', 'Scrcpy', 'scrcpy', d.scrcpy, ' Berlaku di sesi Scrcpy berikutnya.')
      + tail;
    wrap.querySelectorAll('[data-dl]').forEach(b => b.onclick = () => doDownload(b.dataset.dl, b.dataset.name, b.dataset.ver, b.dataset.note));
    wrap.querySelectorAll('[data-verlist]').forEach(el => el.addEventListener('click', ev => {
      // hanya kotak hijau yang bisa ditekan; label/baris di sekitarnya diabaikan
      const b = ev.target.closest('b');
      if (!b || !el.contains(b)) return;
      ev.stopPropagation(); showVersionPicker(el, ev.clientX, ev.clientY);
    }));
    const rb = wrap.querySelector('#dlRefresh');
    if (rb) rb.onclick = () => loadUpdates(true);
  } catch (e) {
    // Cari tombol di dalam wrap (bukan dokumen): view bisa sudah pindah
    // saat fetch gagal, dan query dokumen akan null lalu melempar.
    wrap.innerHTML = `<div class="hint">Gagal memeriksa versi baru: ${esc(e.message)}</div><button class="btn small" id="dlRetry">Periksa lagi</button>`;
    const rb = wrap.querySelector('#dlRetry');
    if (rb) rb.onclick = () => loadUpdates(true);
  }
}
// ---------- pemilih versi (ketuk baris "Versi baru" di kotak Download) ----------
// Daftar = rilis final terbaru dari GitHub (cap 20, geodata 10), terbaru
// dulu. Tanda: ✓ terpasang (versi yang sedang dipakai), terbaru (rilis
// latest). Pilih versi di bawah yang terpasang = downgrade eksplisit.
async function showVersionPicker(el, x, y) {
  const d = el.dataset;
  try {
    toast('Memuat daftar versi…');
    const r = await api('/api/updates/versions?item=' + encodeURIComponent(d.verlist));
    const vers = r.versions || [];
    if (!vers.length) return toast('Tidak ada versi');
    const curIdx = vers.findIndex(v => v.version === d.current);
    const items = vers.map((v, i) => {
      let label = `${d.name} ${v.version}` + (v.date ? ` · ${v.date}` : '');
      if (v.version === d.current) label += ' ✓ terpasang';
      if (v.version === d.latest) label += ' · terbaru';
      if (d.pinned === '1' && v.version === d.latest) label += ' (versi teruji)';
      return { label, fn: () => doDownload(d.verlist, d.name, v.version, d.note, v.tag, curIdx >= 0 && i > curIdx) };
    });
    openMenu(x, y, items);
  } catch (e) { toast(e.message); }
}
async function doDownload(item, name, ver, note, tag, older) {
  if (!confirm(`Unduh dan pasang ${name} ${ver}?${older ? ' Versi ini lebih lama dari yang terpasang.' : ''}${note || ''}`)) return;
  try {
    toast('Mengunduh…');
    const r = await api('/api/updates/download', 'POST', tag ? { item, tag } : { item });
    toast(`${name} ${r.version} terpasang`);
    await reload(); vAbout();
  } catch (e) { toast(e.message); }
}

// ---------- polling status (FAB ikut berubah bila mihomo mati/hidup sendiri) ----------
let pollBusy = false;
async function pollStatus() {
  if (document.hidden || pollBusy) return; // hemat baterai: tab tak terlihat = tak perlu polling
  pollBusy = true;
  try {
    const st = await api('/api/status');
    const changed = st.running !== S.status.running;
    S.status = st; syncFab();
    if (changed && !S.editing && S.view === 'servers') { await reload(); render(); }
  } catch (e) { /* backend sibuk */ }
  pollBusy = false;
}
setInterval(pollStatus, 5000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) pollStatus(); });

// ---------- boot ----------
(async () => {
  try { await reload(); }
  catch (e) { $('#view').innerHTML = `<div class="card">Gagal konek ke backend: ${esc(e.message)}</div>`; return; }
  nav('servers');
})();

// ================= JMUXER (pustaka pemutar MPEG-TS, ditempel utuh) =================
!function(e,t){"object"==typeof exports&&"undefined"!=typeof module?module.exports=t(require("stream")):"function"==typeof define&&define.amd?define(["stream"],t):(e="undefined"!=typeof globalThis?globalThis:e||self).JMuxer=t(e.stream)}(this,function(e){"use strict";function t(e,t){(null==t||t>e.length)&&(t=e.length);for(var r=0,n=Array(t);r<t;r++)n[r]=e[r];return n}function r(e,t,r){return t=u(t),function(e,t){if(t&&("object"==typeof t||"function"==typeof t))return t;if(void 0!==t)throw new TypeError("Derived constructors may only return object or undefined");return function(e){if(void 0===e)throw new ReferenceError("this hasn't been initialised - super() hasn't been called");return e}(e)}(e,d()?Reflect.construct(t,r||[],u(e).constructor):t.apply(e,r))}function n(e,t){if(!(e instanceof t))throw new TypeError("Cannot call a class as a function")}function i(e,t){for(var r=0;r<t.length;r++){var n=t[r];n.enumerable=n.enumerable||!1,n.configurable=!0,"value"in n&&(n.writable=!0),Object.defineProperty(e,p(n.key),n)}}function a(e,t,r){return t&&i(e.prototype,t),r&&i(e,r),Object.defineProperty(e,"prototype",{writable:!1}),e}function s(e,t){var r="undefined"!=typeof Symbol&&e[Symbol.iterator]||e["@@iterator"];if(!r){if(Array.isArray(e)||(r=v(e))||t){r&&(e=r);var n=0,i=function(){};return{s:i,n:function(){return n>=e.length?{done:!0}:{done:!1,value:e[n++]}},e:function(e){throw e},f:i}}throw new TypeError("Invalid attempt to iterate non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method.")}var a,s=!0,o=!1;return{s:function(){r=r.call(e)},n:function(){var e=r.next();return s=e.done,e},e:function(e){o=!0,a=e},f:function(){try{s||null==r.return||r.return()}finally{if(o)throw a}}}}function o(e,t,r){return(t=p(t))in e?Object.defineProperty(e,t,{value:r,enumerable:!0,configurable:!0,writable:!0}):e[t]=r,e}function u(e){return u=Object.setPrototypeOf?Object.getPrototypeOf.bind():function(e){return e.__proto__||Object.getPrototypeOf(e)},u(e)}function c(e,t){if("function"!=typeof t&&null!==t)throw new TypeError("Super expression must either be null or a function");e.prototype=Object.create(t&&t.prototype,{constructor:{value:e,writable:!0,configurable:!0}}),Object.defineProperty(e,"prototype",{writable:!1}),t&&l(e,t)}function d(){try{var e=!Boolean.prototype.valueOf.call(Reflect.construct(Boolean,[],function(){}))}catch(e){}return(d=function(){return!!e})()}function l(e,t){return l=Object.setPrototypeOf?Object.setPrototypeOf.bind():function(e,t){return e.__proto__=t,e},l(e,t)}function f(e,t){return function(e){if(Array.isArray(e))return e}(e)||function(e,t){var r=null==e?null:"undefined"!=typeof Symbol&&e[Symbol.iterator]||e["@@iterator"];if(null!=r){var n,i,a,s,o=[],u=!0,c=!1;try{if(a=(r=r.call(e)).next,0===t);else for(;!(u=(n=a.call(r)).done)&&(o.push(n.value),o.length!==t);u=!0);}catch(e){c=!0,i=e}finally{try{if(!u&&null!=r.return&&(s=r.return(),Object(s)!==s))return}finally{if(c)throw i}}return o}}(e,t)||v(e,t)||function(){throw new TypeError("Invalid attempt to destructure non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method.")}()}function h(e){return function(e){if(Array.isArray(e))return t(e)}(e)||function(e){if("undefined"!=typeof Symbol&&null!=e[Symbol.iterator]||null!=e["@@iterator"])return Array.from(e)}(e)||v(e)||function(){throw new TypeError("Invalid attempt to spread non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method.")}()}function p(e){var t=function(e,t){if("object"!=typeof e||!e)return e;var r=e[Symbol.toPrimitive];if(void 0!==r){var n=r.call(e,t);if("object"!=typeof n)return n;throw new TypeError("@@toPrimitive must return a primitive value.")}return String(e)}(e,"string");return"symbol"==typeof t?t:t+""}function y(e){return y="function"==typeof Symbol&&"symbol"==typeof Symbol.iterator?function(e){return typeof e}:function(e){return e&&"function"==typeof Symbol&&e.constructor===Symbol&&e!==Symbol.prototype?"symbol":typeof e},y(e)}function v(e,r){if(e){if("string"==typeof e)return t(e,r);var n={}.toString.call(e).slice(8,-1);return"Object"===n&&e.constructor&&(n=e.constructor.name),"Map"===n||"Set"===n?Array.from(e):"Arguments"===n||/^(?:Ui|I)nt(?:8|16|32)(?:Clamped)?Array$/.test(n)?t(e,r):void 0}}var m,g;function k(e){if(m){for(var t=arguments.length,r=new Array(t>1?t-1:0),n=1;n<t;n++)r[n-1]=arguments[n];m.apply(void 0,[e].concat(r))}}function b(e){if(g){for(var t=arguments.length,r=new Array(t>1?t-1:0),n=1;n<t;n++)r[n-1]=arguments[n];g.apply(void 0,[e].concat(r))}}var U=function(){return a(function e(t){n(this,e),this.listener={},this.type=""|t},[{key:"on",value:function(e,t){return this.listener[e]||(this.listener[e]=[]),this.listener[e].push(t),!0}},{key:"off",value:function(e,t){if(this.listener[e]){var r=this.listener[e].indexOf(t);return r>-1&&this.listener[e].splice(r,1),!0}return!1}},{key:"offAll",value:function(){this.listener={}}},{key:"dispatch",value:function(e){for(var t=arguments.length,r=new Array(t>1?t-1:0),n=1;n<t;n++)r[n-1]=arguments[n];return!!this.listener[e]&&(this.listener[e].map(function(e){e.apply(null,r)}),!0)}}])}(),S=function(){function e(){n(this,e)}return a(e,null,[{key:"init",value:function(){var t;for(t in e.types={avc1:[],avcC:[],btrt:[],dinf:[],dref:[],esds:[],ftyp:[],hdlr:[],hev1:[],hvcC:[],mdat:[],mdhd:[],mdia:[],mfhd:[],minf:[],moof:[],moov:[],mp4a:[],mvex:[],mvhd:[],sdtp:[],stbl:[],stco:[],stsc:[],stsd:[],stsz:[],stts:[],tfdt:[],tfhd:[],traf:[],trak:[],trun:[],trex:[],tkhd:[],vmhd:[],smhd:[]},e.types)e.types.hasOwnProperty(t)&&(e.types[t]=[t.charCodeAt(0),t.charCodeAt(1),t.charCodeAt(2),t.charCodeAt(3)]);var r=new Uint8Array([0,0,0,0,0,0,0,0,118,105,100,101,0,0,0,0,0,0,0,0,0,0,0,0,86,105,100,101,111,72,97,110,100,108,101,114,0]),n=new Uint8Array([0,0,0,0,0,0,0,0,115,111,117,110,0,0,0,0,0,0,0,0,0,0,0,0,83,111,117,110,100,72,97,110,100,108,101,114,0]);e.HDLR_TYPES={video:r,audio:n};var i=new Uint8Array([0,0,0,0,0,0,0,1,0,0,0,12,117,114,108,32,0,0,0,1]),a=new Uint8Array([0,0,0,0,0,0,0,0]);e.STTS=e.STSC=e.STCO=a,e.STSZ=new Uint8Array([0,0,0,0,0,0,0,0,0,0,0,0]),e.VMHD=new Uint8Array([0,0,0,1,0,0,0,0,0,0,0,0]),e.SMHD=new Uint8Array([0,0,0,0,0,0,0,0]),e.STSD=new Uint8Array([0,0,0,0,0,0,0,1]);var s=new Uint8Array([105,115,111,109]),o=new Uint8Array([97,118,99,49]),u=new Uint8Array([0,0,0,1]);e.FTYP=e.box(e.types.ftyp,s,u,s,o),e.DINF=e.box(e.types.dinf,e.box(e.types.dref,i))}},{key:"box",value:function(e){for(var t=arguments.length,r=new Array(t>1?t-1:0),n=1;n<t;n++)r[n-1]=arguments[n];for(var i,a=8,s=r.length,o=s;s--;)a+=r[s].byteLength;for((i=new Uint8Array(a))[0]=a>>24&255,i[1]=a>>16&255,i[2]=a>>8&255,i[3]=255&a,i.set(e,4),s=0,a=8;s<o;++s)i.set(r[s],a),a+=r[s].byteLength;return i}},{key:"hdlr",value:function(t){return e.box(e.types.hdlr,e.HDLR_TYPES[t])}},{key:"mdat",value:function(t){return e.box(e.types.mdat,t)}},{key:"mdhd",value:function(t,r){return e.box(e.types.mdhd,new Uint8Array([0,0,0,0,0,0,0,2,0,0,0,3,t>>24&255,t>>16&255,t>>8&255,255&t,r>>>24&255,r>>>16&255,r>>>8&255,255&r,85,196,0,0]))}},{key:"mdia",value:function(t){return e.box(e.types.mdia,e.mdhd(t.timescale,t.duration),e.hdlr(t.type),e.minf(t))}},{key:"mfhd",value:function(t){return e.box(e.types.mfhd,new Uint8Array([0,0,0,0,t>>24,t>>16&255,t>>8&255,255&t]))}},{key:"minf",value:function(t){return"audio"===t.type?e.box(e.types.minf,e.box(e.types.smhd,e.SMHD),e.DINF,e.stbl(t)):e.box(e.types.minf,e.box(e.types.vmhd,e.VMHD),e.DINF,e.stbl(t))}},{key:"moof",value:function(t,r,n){return e.box(e.types.moof,e.mfhd(t),e.traf(n,r))}},{key:"moov",value:function(t,r,n){for(var i=t.length,a=[];i--;)a[i]=e.trak(t[i]);return e.box.apply(null,[e.types.moov,e.mvhd(n,r)].concat(a).concat(e.mvex(t)))}},{key:"mvex",value:function(t){for(var r=t.length,n=[];r--;)n[r]=e.trex(t[r]);return e.box.apply(null,[e.types.mvex].concat(n))}},{key:"mvhd",value:function(t,r){var n=new Uint8Array([0,0,0,0,0,0,0,1,0,0,0,2,t>>24&255,t>>16&255,t>>8&255,255&t,r>>>24&255,r>>>16&255,r>>>8&255,255&r,0,1,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,64,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,255,255,255,255]);return e.box(e.types.mvhd,n)}},{key:"sdtp",value:function(t){var r,n,i=t.samples||[],a=new Uint8Array(4+i.length);for(n=0;n<i.length;n++)r=i[n].flags,a[n+4]=r.dependsOn<<4|r.isDependedOn<<2|r.hasRedundancy;return e.box(e.types.sdtp,a)}},{key:"stbl",value:function(t){return e.box(e.types.stbl,e.stsd(t),e.box(e.types.stts,e.STTS),e.box(e.types.stsc,e.STSC),e.box(e.types.stsz,e.STSZ),e.box(e.types.stco,e.STCO))}},{key:"avc1",value:function(t){var r,n,i,a=[],s=[];for(r=0;r<t.sps.length;r++)i=(n=t.sps[r]).byteLength,a.push(i>>>8&255),a.push(255&i),a=a.concat(Array.prototype.slice.call(n));for(r=0;r<t.pps.length;r++)i=(n=t.pps[r]).byteLength,s.push(i>>>8&255),s.push(255&i),s=s.concat(Array.prototype.slice.call(n));var o=e.box(e.types.avcC,new Uint8Array([1,a[3],a[4],a[5],255,224|t.sps.length].concat(a).concat([t.pps.length]).concat(s))),u=t.width,c=t.height;return e.box(e.types.avc1,new Uint8Array([0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,u>>8&255,255&u,c>>8&255,255&c,0,72,0,0,0,72,0,0,0,0,0,0,0,1,18,98,105,110,101,108,112,114,111,46,114,117,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,24,17,17]),o,e.box(e.types.btrt,new Uint8Array([0,28,156,128,0,45,198,192,0,45,198,192])))}},{key:"hev1",value:function(t){for(var r,n,i=[],a=[],s=[],o=0;o<((null===(u=t.vps)||void 0===u?void 0:u.length)||0);o++){var u;n=(r=t.vps[o]).byteLength,i.push(n>>>8&255,255&n),i=i.concat(Array.prototype.slice.call(r))}for(var c=0;c<((null===(d=t.sps)||void 0===d?void 0:d.length)||0);c++){var d;n=(r=t.sps[c]).byteLength,a.push(n>>>8&255,255&n),a=a.concat(Array.prototype.slice.call(r))}for(var l=0;l<((null===(f=t.pps)||void 0===f?void 0:f.length)||0);l++){var f;n=(r=t.pps[l]).byteLength,s.push(n>>>8&255,255&n),s=s.concat(Array.prototype.slice.call(r))}var p=t.hvcC,y=p.profile_space,v=p.tier_flag,m=p.profile_idc,g=p.profile_compatibility_flags,k=p.constraint_indicator_flags,b=p.level_idc,U=p.chroma_format_idc,S=e.box(e.types.hvcC,new Uint8Array([1,y<<6|v<<5|m,g>>24&255,g>>16&255,g>>8&255,255&g].concat(h(k),[b,240,0,252,252|U,248,248,0,0,3,3,32,0,1],h(i),[33,0,1],h(a),[34,0,1],h(s)))),B=t.width,w=t.height;return e.box(e.types.hev1,new Uint8Array([0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,B>>8&255,255&B,w>>8&255,255&w,0,72,0,0,0,72,0,0,0,0,0,0,0,1,18,98,105,110,101,108,112,114,111,46,114,117,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,24,17,17]),S,e.box(e.types.btrt,new Uint8Array([0,28,156,128,0,45,198,192,0,45,198,192])))}},{key:"esds",value:function(e){var t=e.config.byteLength,r=new Uint8Array(26+t+3);return r.set([0,0,0,0,3,23+t,0,1,0,4,15+t,64,21,0,0,0,0,0,0,0,0,0,0,0,5,t]),r.set(e.config,26),r.set([6,1,2],26+t),r}},{key:"mp4a",value:function(t){var r=t.audiosamplerate;return e.box(e.types.mp4a,new Uint8Array([0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,t.channelCount,0,16,0,0,0,0,r>>8&255,255&r,0,0]),e.box(e.types.esds,e.esds(t)))}},{key:"stsd",value:function(t){return"audio"===t.type?e.box(e.types.stsd,e.STSD,e.mp4a(t)):t.codec.startsWith("hvc1")?e.box(e.types.stsd,e.STSD,e.hev1(t)):e.box(e.types.stsd,e.STSD,e.avc1(t))}},{key:"tkhd",value:function(t){var r=t.id,n=t.duration,i=t.width,a=t.height,s=t.volume;return e.box(e.types.tkhd,new Uint8Array([0,0,0,7,0,0,0,0,0,0,0,0,r>>24&255,r>>16&255,r>>8&255,255&r,0,0,0,0,n>>>24&255,n>>>16&255,n>>>8&255,255&n,0,0,0,0,0,0,0,0,0,0,0,0,255&s,s%1*10&255,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,64,0,0,0,i>>8&255,255&i,0,0,a>>8&255,255&a,0,0]))}},{key:"traf",value:function(t,r){var n=e.sdtp(t),i=t.id;return e.box(e.types.traf,e.box(e.types.tfhd,new Uint8Array([0,0,0,0,i>>24,i>>16&255,i>>8&255,255&i])),e.box(e.types.tfdt,new Uint8Array([0,0,0,0,r>>24,r>>16&255,r>>8&255,255&r])),e.trun(t,n.length+16+16+8+16+8+8),n)}},{key:"trak",value:function(t){return t.duration=t.duration||4294967295,e.box(e.types.trak,e.tkhd(t),e.mdia(t))}},{key:"trex",value:function(t){var r=t.id;return e.box(e.types.trex,new Uint8Array([0,0,0,0,r>>24,r>>16&255,r>>8&255,255&r,0,0,0,1,0,0,0,0,0,0,0,0,0,1,0,1]))}},{key:"trun",value:function(t,r){var n,i,a,s,o,u,c=t.samples||[],d=c.length,l=12+16*d,f=new Uint8Array(l);for(r+=8+l,f.set([0,0,15,1,d>>>24&255,d>>>16&255,d>>>8&255,255&d,r>>>24&255,r>>>16&255,r>>>8&255,255&r],0),n=0;n<d;n++)a=(i=c[n]).duration,s=i.size,o=i.flags,u=i.cts,f.set([a>>>24&255,a>>>16&255,a>>>8&255,255&a,s>>>24&255,s>>>16&255,s>>>8&255,255&s,o.isLeading<<2|o.dependsOn,o.isDependedOn<<6|o.hasRedundancy<<4|o.paddingValue<<1|o.isNonSync,61440&o.degradPrio,15&o.degradPrio,u>>>24&255,u>>>16&255,u>>>8&255,255&u],12+16*n);return e.box(e.types.trun,f)}},{key:"initSegment",value:function(t,r,n){e.types||e.init();var i,a=e.moov(t,r,n);return(i=new Uint8Array(e.FTYP.byteLength+a.byteLength)).set(e.FTYP),i.set(a,e.FTYP.byteLength),i}}])}(),B=function(){function e(){n(this,e)}return a(e,null,[{key:"samplingRateMap",get:function(){return[96e3,88200,64e3,48e3,44100,32e3,24e3,22050,16e3,12e3,11025,8e3,7350]}},{key:"getHeaderLength",value:function(e){return 1&e[1]?7:9}},{key:"getFrameLength",value:function(e){return(3&e[3])<<11|e[4]<<3|(224&e[5])>>>5}},{key:"isAACPattern",value:function(e){return!(255!==e[0]||240&~e[1]||6&e[1])}},{key:"extractAAC",value:function(t){var r,n,i=0,a=t.byteLength,s=[];if(!e.isAACPattern(t))return b("Invalid ADTS audio format"),{valid:!1};r=e.getHeaderLength(t);for(var o=t.subarray(0,r);i<a;)n=e.getFrameLength(t),s.push(t.subarray(r,n)),t=t.slice(n),i+=n;return{valid:!0,header:o,slices:s}}}])}(),w=1,E=function(e){function t(){return n(this,t),r(this,t,arguments)}return c(t,e),a(t,[{key:"flush",value:function(){this.mp4track.len=0,this.mp4track.samples=[]}},{key:"isReady",value:function(){return!(!this.readyToDecode||!this.samples.length)||null}}],[{key:"getTrackID",value:function(){return w++}}])}(U),A=function(e){function t(e,i,a){var s;return n(this,t),(s=r(this,t,["AACRemuxer"])).frameDuration=a,s.readyToDecode=!1,s.header=null,s.nextDts=0,s.dts=0,s.mp4track={id:E.getTrackID(),type:"audio",channelCount:0,len:0,fragmented:!0,timescale:e,duration:i,samples:[],config:"",codec:""},s.samples=[],s}return c(t,e),a(t,[{key:"resetTrack",value:function(){this.readyToDecode=!1,this.header=null,this.mp4track.codec="",this.mp4track.channelCount="",this.mp4track.config="",this.mp4track.timescale=this.timescale,this.nextDts=0,this.dts=0}},{key:"feed",value:function(e,t){var r=B.extractAAC(e),n=r.valid,i=r.header,a=r.slices;return this.header||(this.header=i),n&&a.length>0?(this.remux(this.getAudioFrames(a,t)),!0):(b("Failed to extract audio data from:",e),this.dispatch("outOfData"),!1)}},{key:"getAudioFrames",value:function(e,t){var r,n,i=[],a=0,o=s(e);try{for(o.s();!(n=o.n()).done;){var u=n.value;i.push({units:u})}}catch(e){o.e(e)}finally{o.f()}return r=t?t/i.length|0:this.frameDuration,a=t?t-r*i.length:0,i.map(function(e){e.duration=r,a>0&&(e.duration++,a--)}),i}},{key:"remux",value:function(e){if(e.length>0)for(var t=0;t<e.length;t++){var r=e[t],n=r.units,i=n.byteLength;this.samples.push({units:n,size:i,duration:r.duration}),this.mp4track.len+=i,this.readyToDecode||this.setAACConfig()}}},{key:"getPayload",value:function(){if(!this.isReady())return null;var e,t,r=new Uint8Array(this.mp4track.len),n=0,i=this.mp4track.samples;for(this.dts=this.nextDts;this.samples.length;){var a=this.samples.shift();a.units,(t=a.duration)<=0?(k("remuxer: invalid sample duration at DTS: ".concat(this.nextDts," :").concat(t)),this.mp4track.len-=a.size):(this.nextDts+=t,e={size:a.size,duration:t,cts:0,flags:{isLeading:0,isDependedOn:0,hasRedundancy:0,degradPrio:0,dependsOn:1}},r.set(a.units,n),n+=a.size,i.push(e))}return i.length?new Uint8Array(r.buffer,0,this.mp4track.len):null}},{key:"setAACConfig",value:function(){var e,t,r,n=new Uint8Array(2);this.header&&(e=((192&this.header[2])>>>6)+1,t=(60&this.header[2])>>>2,r=(1&this.header[2])<<2,r|=(192&this.header[3])>>>6,n[0]=e<<3,n[0]|=(14&t)>>1,n[1]|=(1&t)<<7,n[1]|=r<<3,this.mp4track.codec="mp4a.40."+e,this.mp4track.channelCount=r,this.mp4track.config=n,this.readyToDecode=!0)}}])}(E),D=function(){return a(function e(t){n(this,e),this.data=t,this.index=0,this.bitLength=8*t.byteLength},[{key:"setData",value:function(e){this.data=e,this.index=0,this.bitLength=8*e.byteLength}},{key:"bitsAvailable",get:function(){return this.bitLength-this.index}},{key:"skipBits",value:function(e){if(this.bitsAvailable<e)return!1;this.index+=e}},{key:"readBits",value:function(e){var t=!(arguments.length>1&&void 0!==arguments[1])||arguments[1];return this.getBits(e,this.index,t)}},{key:"getBits",value:function(e,t){var r=!(arguments.length>2&&void 0!==arguments[2])||arguments[2];if(this.bitsAvailable<e)return 0;var n=t%8,i=this.data[t/8|0]&255>>>n,a=8-n;if(a>=e)return r&&(this.index+=e),i>>a-e;r&&(this.index+=a);var s=e-a;return i<<s|this.getBits(s,t+a,r)}},{key:"skipLZ",value:function(){var e;for(e=0;e<this.bitLength-this.index;++e)if(0!==this.getBits(1,this.index+e,!1))return this.index+=e,e;return e}},{key:"skipUEG",value:function(){this.skipBits(1+this.skipLZ())}},{key:"skipEG",value:function(){this.skipBits(1+this.skipLZ())}},{key:"readUEG",value:function(){var e=this.skipLZ();return this.readBits(e+1)-1}},{key:"readEG",value:function(){var e=this.readUEG();return 1&e?1+e>>>1:-1*(e>>>1)}},{key:"readBoolean",value:function(){return 1===this.readBits(1)}},{key:"readUByte",value:function(){var e=arguments.length>0&&void 0!==arguments[0]?arguments[0]:1;return this.readBits(8*e)}},{key:"readUShort",value:function(){return this.readBits(16)}},{key:"readUInt",value:function(){return this.readBits(32)}}])}(),x=function(){function e(){n(this,e)}return a(e,null,[{key:"extractNALu",value:function(e){for(var t=0,r=e.byteLength,n=[],i=0,a=0;t<r;){var s=e[t++];if(0===s)a++;else if(1===s&&a>=2){var o=a+1;i!==t-o&&n.push(e.subarray(i,t-o)),i=t,a=0}else a=0}var u=null;return i<r&&(u=e.subarray(i,r)),[n,u]}},{key:"skipScalingList",value:function(e,t){for(var r=8,n=8,i=0;i<t;i++)0!==n&&(n=(r+e.readEG()+256)%256),r=0===n?r:n}},{key:"readSPS",value:function(t){var r,n,i,a,s,o,u=new D(t),c=0,d=0,l=0,f=0,h=1,p=0;u.readUByte();for(var y=[],v=t.byteLength,m=1;m<v;m++)m+2<v&&3===u.readBits(24,!1)?(y.push(u.readBits(8)),y.push(u.readBits(8)),m+=2,u.readBits(8)):y.push(u.readBits(8));if(u.setData(new Uint8Array(y)),r=u.readUByte(),u.readBits(5),u.skipBits(3),u.readUByte(),u.skipUEG(),100===r||110===r||122===r||244===r||44===r||83===r||86===r||118===r||128===r){var g=u.readUEG();if(3===g&&u.skipBits(1),u.skipUEG(),u.skipUEG(),u.skipBits(1),u.readBoolean()){o=3!==g?8:12;for(var k=0;k<o;++k)u.readBoolean()&&(k<6?e.skipScalingList(u,16):e.skipScalingList(u,64))}}u.skipUEG();var b=u.readUEG();if(0===b)u.readUEG();else if(1===b){u.skipBits(1),u.skipEG(),u.skipEG(),n=u.readUEG();for(var U=0;U<n;++U)u.skipEG()}if(u.skipUEG(),u.skipBits(1),i=u.readUEG(),a=u.readUEG(),0===(s=u.readBits(1))&&u.skipBits(1),u.skipBits(1),u.readBoolean()&&(c=u.readUEG(),d=u.readUEG(),l=u.readUEG(),f=u.readUEG()),u.readBoolean()){if(u.readBoolean()){var S;switch(u.readUByte()){case 1:S=[1,1];break;case 2:S=[12,11];break;case 3:S=[10,11];break;case 4:S=[16,11];break;case 5:S=[40,33];break;case 6:S=[24,11];break;case 7:S=[20,11];break;case 8:S=[32,11];break;case 9:S=[80,33];break;case 10:S=[18,11];break;case 11:S=[15,11];break;case 12:S=[64,33];break;case 13:S=[160,99];break;case 14:S=[4,3];break;case 15:S=[3,2];break;case 16:S=[2,1];break;case 255:S=[u.readUByte()<<8|u.readUByte(),u.readUByte()<<8|u.readUByte()]}S&&S[0]>0&&S[1]>0&&(h=S[0]/S[1])}if(u.readBoolean()&&u.skipBits(1),u.readBoolean()&&(u.skipBits(4),u.readBoolean()&&u.skipBits(24)),u.readBoolean()&&(u.skipUEG(),u.skipUEG()),u.readBoolean()){var B=u.readUInt(),w=u.readUInt();u.readBoolean(),p=w/(2*B)}}return{fps:p>0?p:void 0,width:Math.ceil((16*(i+1)-2*c-2*d)*h),height:(2-s)*(a+1)*16-(s?2:4)*(l+f)}}}])}(),C=function(){function e(t){n(this,e),this.payload=t,this.nri=(96&this.payload[0])>>5,this.nalUnitType=31&this.payload[0],this._sliceType=null,this._isFirstSlice=!1}return a(e,[{key:"toString",value:function(){return"".concat(e.TYPES[this.type()]||"UNKNOWN",": NRI: ").concat(this.getNri())}},{key:"getNri",value:function(){return this.nri}},{key:"type",value:function(){return this.nalUnitType}},{key:"isKeyframe",get:function(){return this.nalUnitType===e.IDR}},{key:"isVCL",get:function(){return this.nalUnitType==e.IDR||this.nalUnitType==e.NDR}},{key:"parseHeader",value:function(){var e=new D(this.getPayload());e.readUByte(),this._isFirstSlice=0===e.readUEG(),this._sliceType=e.readUEG()}},{key:"isFirstSlice",get:function(){return this._isFirstSlice||this.parseHeader(),this._isFirstSlice}},{key:"sliceType",get:function(){return this._sliceType||this.parseHeader(),this._sliceType}},{key:"getPayload",value:function(){return this.payload}},{key:"getPayloadSize",value:function(){return this.payload.byteLength}},{key:"getSize",value:function(){return 4+this.getPayloadSize()}},{key:"getData",value:function(){var e=new Uint8Array(this.getSize());return new DataView(e.buffer).setUint32(0,this.getSize()-4),e.set(this.getPayload(),4),e}}],[{key:"NDR",get:function(){return 1}},{key:"IDR",get:function(){return 5}},{key:"SEI",get:function(){return 6}},{key:"SPS",get:function(){return 7}},{key:"PPS",get:function(){return 8}},{key:"AUD",get:function(){return 9}},{key:"TYPES",get:function(){return o(o(o(o(o(o({},e.IDR,"IDR"),e.SEI,"SEI"),e.SPS,"SPS"),e.PPS,"PPS"),e.NDR,"NDR"),e.AUD,"AUD")}}])}();function P(e,t){var r=new Uint8Array((0|e.byteLength)+(0|t.byteLength));return r.set(e,0),r.set(t,0|e.byteLength),r}function T(e){var t,r,n,i="";return t=Math.floor(e),(r=parseInt(t/3600,10)%24)>0&&(i+=(r<10?"0"+r:r)+":"),i+=((n=parseInt(t/60,10)%60)<10?"0"+n:n)+":"+((t=t<0?0:t%60)<10?"0"+t:t)}var L=function(e){function t(e,i,a){var s;return n(this,t),(s=r(this,t,["H264Remuxer"])).frameDuration=a,s.readyToDecode=!1,s.nextDts=0,s.dts=0,s.mp4track={id:E.getTrackID(),type:"video",len:0,fragmented:!0,sps:"",pps:"",fps:30,width:0,height:0,timescale:e,duration:i,samples:[]},s.samples=[],s.remainingData=new Uint8Array,s.kfCounter=0,s.pendingUnits={},s}return c(t,e),a(t,[{key:"resetTrack",value:function(){this.readyToDecode=!1,this.mp4track.sps="",this.mp4track.pps="",this.nextDts=0,this.dts=0,this.remainingData=new Uint8Array,this.kfCounter=0,this.pendingUnits={}}},{key:"feed",value:function(e,t,r){var n,i=arguments.length>3&&void 0!==arguments[3]&&arguments[3],a=[];e=P(this.remainingData,e);var s=f(x.extractNALu(e),2);return a=s[0],(n=s[1])?i?(a.push(n),this.remainingData=new Uint8Array):this.remainingData=n:this.remainingData=new Uint8Array,a.length>0?(this.remux(this.getVideoFrames(a,t,r)),!0):(k("Failed to extract any NAL units from video data:",n),this.dispatch("outOfData"),!1)}},{key:"getVideoFrames",value:function(e,t,r){var n,i=this,a=[],o=[],u=0,c=!1,d=!1;this.pendingUnits.units&&(a=this.pendingUnits.units,d=this.pendingUnits.vcl,c=this.pendingUnits.keyFrame,this.pendingUnits={});var l,f=s(e);try{for(f.s();!(l=f.n()).done;){var h=l.value,p=new C(h);this.parseNAL(p)&&(a.length&&d&&(p.isFirstSlice||!p.isVCL)&&(o.push({units:a,keyFrame:c}),a=[],c=!1,d=!1),a.push(p),c=c||p.isKeyframe,d=d||p.isVCL)}}catch(e){f.e(e)}finally{f.f()}if(a.length)if(t)if(d)o.push({units:a,keyFrame:c});else{var y=o.length-1;y>=0&&(o[y].units=o[y].units.concat(a))}else this.pendingUnits={units:a,keyFrame:c,vcl:d};return n=t?t/o.length|0:this.frameDuration,u=t?t-n*o.length:0,o.map(function(e){e.duration=n,e.compositionTimeOffset=r,u>0&&(e.duration++,u--),i.kfCounter++,e.keyFrame&&i.dispatch("keyframePosition",i.kfCounter*n/1e3)}),k("jmuxer: No. of H264 frames of the last chunk: ".concat(o.length)),o}},{key:"remux",value:function(e){var t,r=s(e);try{for(r.s();!(t=r.n()).done;){var n=t.value,i=n.units.reduce(function(e,t){return e+t.getSize()},0);n.units.length>0&&this.readyToDecode&&(this.mp4track.len+=i,this.samples.push({units:n.units,size:i,keyFrame:n.keyFrame,duration:n.duration,compositionTimeOffset:n.compositionTimeOffset}))}}catch(e){r.e(e)}finally{r.f()}}},{key:"getPayload",value:function(){if(!this.isReady())return null;var e,t,r=new Uint8Array(this.mp4track.len),n=0,i=this.mp4track.samples;for(this.dts=this.nextDts;this.samples.length;){var a=this.samples.shift(),o=a.units;if((t=a.duration)<=0)k("remuxer: invalid sample duration at DTS: ".concat(this.nextDts," :").concat(t)),this.mp4track.len-=a.size;else{this.nextDts+=t,e={size:a.size,duration:t,cts:a.compositionTimeOffset||0,flags:{isLeading:0,isDependedOn:0,hasRedundancy:0,degradPrio:0,isNonSync:a.keyFrame?0:1,dependsOn:a.keyFrame?2:1}};var u,c=s(o);try{for(c.s();!(u=c.n()).done;){var d=u.value;r.set(d.getData(),n),n+=d.getSize()}}catch(e){c.e(e)}finally{c.f()}i.push(e)}}return i.length?new Uint8Array(r.buffer,0,this.mp4track.len):null}},{key:"parseSPS",value:function(e){var t=x.readSPS(new Uint8Array(e));this.mp4track.fps=t.fps||this.mp4track.fps,this.mp4track.width=t.width,this.mp4track.height=t.height,this.mp4track.sps=[new Uint8Array(e)],this.mp4track.codec="avc1.";for(var r=new DataView(e.buffer,e.byteOffset+1,4),n=0;n<3;++n){var i=r.getUint8(n).toString(16);i.length<2&&(i="0"+i),this.mp4track.codec+=i}}},{key:"parsePPS",value:function(e){this.mp4track.pps=[new Uint8Array(e)]}},{key:"parseNAL",value:function(e){if(!e)return!1;if(e.isVCL)return!0;var t=!1;switch(e.type()){case C.PPS:this.mp4track.pps||this.parsePPS(e.getPayload()),t=!0;break;case C.SPS:this.mp4track.sps||this.parseSPS(e.getPayload()),t=!0;break;case C.AUD:k("AUD - ignoing");break;case C.SEI:k("SEI - ignoing")}return!this.readyToDecode&&this.mp4track.pps&&this.mp4track.sps&&(this.readyToDecode=!0),t}}])}(E),_=function(){return a(function e(){n(this,e)},null,[{key:"extractNALu",value:function(e){for(var t=0,r=e.byteLength,n=[],i=0,a=0;t<r;){var s=e[t++];if(0===s)a++;else if(1===s&&a>=2){var o=a+1;i!==t-o&&n.push(e.subarray(i,t-o)),i=t,a=0}else a=0}var u=null;return i<r&&(u=e.subarray(i,r)),[n,u]}},{key:"removeEmulationPreventionBytes",value:function(e){for(var t=[],r=0,n=0;n<e.length;n++){var i=e[n];2!==r||3!==i?(t.push(i),0===i?r++:r=0):r=0}return new Uint8Array(t)}},{key:"readSPS",value:function(e){var t=new D(e);t.readUByte(),t.readUByte(),t.readBits(4);var r=t.readBits(3);t.readBits(1);for(var n=t.readBits(2),i=t.readBits(1),a=t.readBits(5),s=t.readUInt(),o=new Uint8Array(6),u=0;u<6;u++)o[u]=t.readUByte();var c=t.readUByte();t.readUEG();var d=t.readUEG();3===d&&t.readBits(1);var l=t.readUEG(),f=t.readUEG(),h=0,p=0,y=0,v=0;t.readBoolean()&&(h=t.readUEG(),p=t.readUEG(),y=t.readUEG(),v=t.readUEG()),t.readUEG(),t.readUEG(),t.readUEG();for(var m=t.readBits(1)?0:r;m<=r;m++)t.readUEG(),t.readUEG(),t.readUEG();if((t.readUEG(),t.readUEG(),t.readUEG(),t.readUEG(),t.readUEG(),t.readUEG(),t.readBits(1))&&t.readBits(1))for(var g=0;g<4;g++)for(var k=0;k<(3===g?2:6);k++){if(t.readBits(1)){var b=Math.min(64,1<<4+(g<<1));g>1&&t.readEG();for(var U=0;U<b;U++)t.readEG()}else t.readUEG()}t.readBits(1),t.readBits(1),t.readBits(1)&&(t.readBits(4),t.readBits(4),t.readUEG(),t.readUEG(),t.readBits(1));for(var S=t.readUEG(),B=0;B<S;B++){var w=!1;if(0!==B&&(w=t.readBoolean()),w)t.readUEG(),t.readBits(1),t.readUEG();else{for(var E=t.readUEG(),A=t.readUEG(),x=0;x<E;x++)t.readUEG(),t.readBits(1);for(var C=0;C<A;C++)t.readUEG(),t.readBits(1)}}if(t.readBits(1))for(var P=t.readUEG(),T=0;T<P;T++)t.readUEG(),t.readBits(1);t.readBits(1),t.readBits(1);var L=null;if(t.readBoolean()){if(t.readBoolean())255===t.readUByte()&&(t.readUShort(),t.readUShort());if(t.readBoolean()&&t.readBoolean(),t.readBoolean())t.readBits(3),t.readBoolean(),t.readBoolean()&&(t.readUByte(),t.readUByte(),t.readUByte());if(t.readBoolean()&&(t.readUEG(),t.readUEG()),t.readBoolean(),t.readBoolean(),t.readBoolean(),t.readBoolean()&&(t.readUEG(),t.readUEG(),t.readUEG(),t.readUEG()),t.readBoolean()){var _=t.readUInt(),G=t.readUInt();if(t.readBoolean()&&t.readUEG(),t.readBoolean()){var I=!1,R=!1,O=!1;I=t.readBoolean(),R=t.readBoolean(),(I||R)&&((O=t.readBoolean())&&(t.readBits(8),t.readBits(5),t.readBits(1),t.readBits(5)),t.readBits(4),t.readBits(4),O&&t.readBits(4),t.readBits(5),t.readBits(5),t.readBits(5));for(var F=0;F<=r;F++){var M=!1,N=0;if(!!t.readBoolean()||t.readBoolean()?t.readUEG():M=t.readBoolean(),M||(N=t.readUEG()),I)for(var z=0;z<=N;z++)t.readUEG(),t.readUEG(),O&&(t.readUEG(),t.readUEG()),t.readBits(1);if(R)for(var j=0;j<=N;j++)t.readUEG(),t.readUEG(),O&&(t.readUEG(),t.readUEG()),t.readBits(1)}}_>0&&G>0&&(L=G/_)}t.readBoolean()&&(t.readBoolean(),t.readBoolean(),t.readBoolean(),t.readUEG(),t.readUEG(),t.readUEG(),t.readUEG(),t.readUEG())}return{width:l-(1===d||2===d?2:1)*(p+h),height:f-(1===d?2:1)*(y+v),profile_space:n,tier_flag:i,profile_idc:a,profile_compatibility_flags:s,constraint_indicator_flags:o,level_idc:c,chroma_format_idc:d,fps:L}}}])}(),G=function(){function e(t){n(this,e),this.payload=t,this.nalUnitType=(126&t[0])>>1,this.nuhLayerId=(1&t[0])<<5|(248&t[1])>>3,this.nuhTemporalIdPlus1=7&t[1],this._isFirstSlice=null,this._sliceType=null}return a(e,[{key:"toString",value:function(){return"".concat(e.TYPES[this.type()]||"UNKNOWN ("+this.type()+")",": Layer: ").concat(this.nuhLayerId,", Temporal Id: ").concat(this.nuhTemporalIdPlus1)}},{key:"type",value:function(){return this.nalUnitType}},{key:"isKeyframe",get:function(){return[e.IDR_W_RADL,e.IDR_N_LP,e.CRA].includes(this.nalUnitType)}},{key:"isVCL",get:function(){return this.nalUnitType<=31}},{key:"parseHeader",value:function(){var e=new D(this.getPayload());e.readUByte(),e.readUByte(),this._isFirstSlice=e.readBoolean(),this.isKeyframe&&e.readBits(1),e.readUEG(),this._sliceType=e.readUEG()}},{key:"isFirstSlice",get:function(){return this._isFirstSlice||this.parseHeader(),this._isFirstSlice}},{key:"sliceType",get:function(){return this._sliceType||this.parseHeader(),this._sliceType}},{key:"getPayload",value:function(){return this.payload}},{key:"getPayloadSize",value:function(){return this.payload.byteLength}},{key:"getSize",value:function(){return 4+this.getPayloadSize()}},{key:"getData",value:function(){var e=new Uint8Array(this.getSize());return new DataView(e.buffer).setUint32(0,this.getSize()-4),e.set(this.getPayload(),4),e}}],[{key:"TRAIL_N",get:function(){return 0}},{key:"TRAIL_R",get:function(){return 1}},{key:"IDR_W_RADL",get:function(){return 19}},{key:"IDR_N_LP",get:function(){return 20}},{key:"CRA",get:function(){return 21}},{key:"VPS",get:function(){return 32}},{key:"SPS",get:function(){return 33}},{key:"PPS",get:function(){return 34}},{key:"AUD",get:function(){return 35}},{key:"SEI",get:function(){return 39}},{key:"SEI2",get:function(){return 40}},{key:"TYPES",get:function(){var t;return o(o(o(o(o(o(o(o(o(o(t={},e.TRAIL_N,"TRAIL_N"),e.TRAIL_R,"TRAIL_R"),e.IDR_W_RADL,"IDR"),e.IDR_N_LP,"IDR2"),e.CRA,"CRA"),e.VPS,"VPS"),e.SPS,"SPS"),e.PPS,"PPS"),e.AUD,"AUD"),e.SEI,"SEI"),o(t,e.SEI2,"SEI2")}}])}(),I=function(e){function t(e,i,a){var s;return n(this,t),(s=r(this,t,["H264Remuxer"])).frameDuration=a,s.readyToDecode=!1,s.nextDts=0,s.dts=0,s.mp4track={id:E.getTrackID(),type:"video",len:0,fragmented:!0,vps:"",sps:"",pps:"",hvcC:{},fps:30,width:0,height:0,timescale:e,duration:i,samples:[]},s.samples=[],s.remainingData=new Uint8Array,s.kfCounter=0,s.pendingUnits={},s}return c(t,e),a(t,[{key:"resetTrack",value:function(){this.readyToDecode=!1,this.mp4track.vps="",this.mp4track.sps="",this.mp4track.pps="",this.mp4track.hvcC={},this.nextDts=0,this.dts=0,this.remainingData=new Uint8Array,this.kfCounter=0,this.pendingUnits={}}},{key:"feed",value:function(e,t,r){var n,i=arguments.length>3&&void 0!==arguments[3]&&arguments[3],a=[];e=P(this.remainingData,e);var s=f(_.extractNALu(e),2);return a=s[0],(n=s[1])?i?(a.push(n),this.remainingData=new Uint8Array):this.remainingData=n:this.remainingData=new Uint8Array,a.length>0?(this.remux(this.getVideoFrames(a,t,r)),!0):(k("Failed to extract any NAL units from video data:",n),this.dispatch("outOfData"),!1)}},{key:"getVideoFrames",value:function(e,t,r){var n,i=this,a=[],o=[],u=0,c=!1,d=!1;this.pendingUnits.units&&(a=this.pendingUnits.units,d=this.pendingUnits.vcl,c=this.pendingUnits.keyFrame,this.pendingUnits={});var l,f=s(e);try{for(f.s();!(l=f.n()).done;){var h=l.value,p=new G(h);this.parseNAL(p)&&(a.length&&d&&(p.isFirstSlice||!p.isVCL)&&(o.push({units:a,keyFrame:c}),a=[],c=!1,d=!1),a.push(p),c=c||p.isKeyframe,d=d||p.isVCL)}}catch(e){f.e(e)}finally{f.f()}if(a.length)if(t)if(d)o.push({units:a,keyFrame:c});else{var y=o.length-1;y>=0&&(o[y].units=o[y].units.concat(a))}else this.pendingUnits={units:a,keyFrame:c,vcl:d};return n=t?t/o.length|0:this.frameDuration,u=t?t-n*o.length:0,o.map(function(e){e.duration=n,e.compositionTimeOffset=r,u>0&&(e.duration++,u--),i.kfCounter++,e.keyFrame&&i.dispatch("keyframePosition",i.kfCounter*n/1e3)}),k("jmuxer: No. of H265 frames of the last chunk: ".concat(o.length)),o}},{key:"remux",value:function(e){var t,r=s(e);try{for(r.s();!(t=r.n()).done;){var n=t.value,i=n.units.reduce(function(e,t){return e+t.getSize()},0);n.units.length>0&&this.readyToDecode&&(this.mp4track.len+=i,this.samples.push({units:n.units,size:i,keyFrame:n.keyFrame,duration:n.duration,compositionTimeOffset:n.compositionTimeOffset}))}}catch(e){r.e(e)}finally{r.f()}}},{key:"getPayload",value:function(){if(!this.isReady())return null;var e,t,r=new Uint8Array(this.mp4track.len),n=0,i=this.mp4track.samples;for(this.dts=this.nextDts;this.samples.length;){var a=this.samples.shift(),o=a.units;if((t=a.duration)<=0)k("remuxer: invalid sample duration at DTS: ".concat(this.nextDts," :").concat(t)),this.mp4track.len-=a.size;else{this.nextDts+=t,e={size:a.size,duration:t,cts:a.compositionTimeOffset||0,flags:{isLeading:0,isDependedOn:0,hasRedundancy:0,degradPrio:0,isNonSync:a.keyFrame?0:1,dependsOn:a.keyFrame?2:1}};var u,c=s(o);try{for(c.s();!(u=c.n()).done;){var d=u.value;r.set(d.getData(),n),n+=d.getSize()}}catch(e){c.e(e)}finally{c.f()}i.push(e)}}return i.length?new Uint8Array(r.buffer,0,this.mp4track.len):null}},{key:"parseSPS",value:function(e){this.mp4track.sps=[new Uint8Array(e)],e=_.removeEmulationPreventionBytes(e);var t=_.readSPS(new Uint8Array(e));this.mp4track.fps=t.fps||this.mp4track.fps,this.mp4track.width=t.width,this.mp4track.height=t.height,this.mp4track.codec="hvc1."+(t.profile_space?String.fromCharCode(64+t.profile_space):"")+t.profile_idc+"."+function(e){for(var t=0,r=0;r<32;r++)t<<=1,t|=1&e,e>>>=1;return t>>>0}(t.profile_compatibility_flags).toString(16)+"."+(t.tier_flag?"H":"L")+t.level_idc+"."+t.constraint_indicator_flags.map(function(e){return e.toString(16)}).join(".").toUpperCase().replace(/(?:\.0)+$/,""),this.mp4track.hvcC={profile_space:t.profile_space,tier_flag:t.tier_flag,profile_idc:t.profile_idc,profile_compatibility_flags:t.profile_compatibility_flags,constraint_indicator_flags:t.constraint_indicator_flags,level_idc:t.level_idc,chroma_format_idc:t.chroma_format_idc}}},{key:"parsePPS",value:function(e){this.mp4track.pps=[e]}},{key:"parseVPS",value:function(e){this.mp4track.vps=[e]}},{key:"parseNAL",value:function(e){if(!e)return!1;if(e.isVCL)return!0;var t=!1;switch(e.type()){case G.VPS:this.mp4track.vps||this.parseVPS(e.getPayload()),t=!0;break;case G.SPS:this.mp4track.sps||this.parseSPS(e.getPayload()),t=!0;break;case G.PPS:this.mp4track.pps||this.parsePPS(e.getPayload()),t=!0;break;case G.AUD:k("AUD - ignoing");break;case G.SEI:case G.SEI2:k("SEI - ignoing")}return!this.readyToDecode&&this.mp4track.vps&&this.mp4track.sps&&this.mp4track.pps&&(this.readyToDecode=!0),t}}])}(E),R=function(e){function t(e,i,a,s){var o;return n(this,t),(o=r(this,t,["remuxer"])).videoCodec=a,o.frameDuration=s,o.initialized=!1,o.tracks={},o.seq=1,o.env=e,o.timescale=1e3,o.mediaDuration=i?4294967295:0,o}return c(t,e),a(t,[{key:"addTrack",value:function(e){var t=this;if("video"!==e&&"both"!==e||("H265"==this.videoCodec?this.tracks.video=new I(this.timescale,this.mediaDuration,this.frameDuration):this.tracks.video=new L(this.timescale,this.mediaDuration,this.frameDuration),this.tracks.video.on("outOfData",function(){t.dispatch("missingVideoFrames")}),this.tracks.video.on("keyframePosition",function(e){t.dispatch("keyframePosition",e)})),"audio"===e||"both"===e){var r=new A(this.timescale,this.mediaDuration,this.frameDuration);this.tracks.audio=r,this.tracks.audio.on("outOfData",function(){t.dispatch("missingAudioFrames")})}}},{key:"reset",value:function(){for(var e in this.tracks)this.tracks[e].resetTrack();this.initialized=!1}},{key:"destroy",value:function(){this.tracks={},this.offAll()}},{key:"flush",value:function(){if(!this.initialized){if(!this.isReady())return;this.dispatch("ready"),this.initSegment(),this.initialized=!0}for(var e in this.tracks){var t=this.tracks[e],r=t.getPayload();if(r&&r.byteLength){var n={type:e,payload:P(S.moof(this.seq,t.dts,t.mp4track),S.mdat(r)),dts:t.dts};"video"===e&&(n.fps=t.mp4track.fps),this.dispatch("buffer",n);var i=T(t.dts/this.timescale);k("put segment (".concat(e,"): dts: ").concat(t.dts," frames: ").concat(t.mp4track.samples.length," second: ").concat(i)),t.flush(),this.seq++}}}},{key:"initSegment",value:function(){var e=[];for(var t in this.tracks){var r=this.tracks[t];if("browser"==this.env){var n={type:t,payload:S.initSegment([r.mp4track],this.mediaDuration,this.timescale)};this.dispatch("buffer",n)}else e.push(r.mp4track)}if("node"==this.env){var i={type:"all",payload:S.initSegment(e,this.mediaDuration,this.timescale)};this.dispatch("buffer",i)}k("Initial segment generated.")}},{key:"isReady",value:function(){for(var e in this.tracks)if(!this.tracks[e].readyToDecode||!this.tracks[e].samples.length)return!1;return!0}},{key:"feed",value:function(e){var t=!1;e.video&&this.tracks.video&&(t|=this.tracks.video.feed(e.video,e.duration,e.compositionTimeOffset,e.isLastVideoFrameComplete)),e.audio&&this.tracks.audio&&(t|=this.tracks.audio.feed(e.audio,e.duration)),t?this.flush():b("Input object must have video and/or audio property. Make sure it is a valid typed array")}}])}(U),O=function(e){function t(e,i){var a;return n(this,t),(a=r(this,t,["buffer"])).type=i,a.queue=new Uint8Array,a.cleaning=!1,a.pendingCleaning=0,a.cleanOffset=30,a.cleanRanges=[],a.sourceBuffer=e,a.sourceBuffer.addEventListener("updateend",function(){a.pendingCleaning>0&&(a.initCleanup(a.pendingCleaning),a.pendingCleaning=0),a.cleaning=!1,a.cleanRanges.length&&a.doCleanup()}),a.sourceBuffer.addEventListener("error",function(){a.dispatch("error",{type:a.type,name:"buffer",error:"buffer error"})}),a}return c(t,e),a(t,[{key:"destroy",value:function(){this.queue=null,this.sourceBuffer=null,this.offAll()}},{key:"doCleanup",value:function(){if(this.cleanRanges.length){var e=this.cleanRanges.shift();k("".concat(this.type," remove range [").concat(e[0]," - ").concat(e[1],")")),this.cleaning=!0,this.sourceBuffer.remove(e[0],e[1])}else this.cleaning=!1}},{key:"initCleanup",value:function(e){try{if(this.sourceBuffer.updating)return void(this.pendingCleaning=e);if(this.sourceBuffer.buffered&&this.sourceBuffer.buffered.length&&!this.cleaning){for(var t=0;t<this.sourceBuffer.buffered.length;++t){var r=this.sourceBuffer.buffered.start(t),n=this.sourceBuffer.buffered.end(t);e-r>this.cleanOffset&&r<(n=e-this.cleanOffset)&&this.cleanRanges.push([r,n])}this.doCleanup()}}catch(e){b("Error occured while cleaning ".concat(this.type," buffer - ").concat(e.name,": ").concat(e.message))}}},{key:"doAppend",value:function(){if(this.queue.length&&this.sourceBuffer&&!this.sourceBuffer.updating)try{this.sourceBuffer.appendBuffer(this.queue),this.queue=new Uint8Array}catch(t){var e="unexpectedError";"QuotaExceededError"===t.name?(k("".concat(this.type," buffer quota full")),e="QuotaExceeded"):(b("Error occured while appending ".concat(this.type," buffer - ").concat(t.name,": ").concat(t.message)),e="InvalidStateError"),this.dispatch("error",{type:this.type,name:e,error:"buffer error"})}}},{key:"feed",value:function(e){this.queue=P(this.queue,e)}}])}(U);return function(t){function i(e){var t;n(this,i),(t=r(this,i,["jmuxer"])).isReset=!1;var a={node:"",mode:"both",videoCodec:"H264",flushingTime:500,maxDelay:500,clearBuffer:!0,fps:30,readFpsFromTrack:!1,debug:!1,onReady:function(){},onData:function(){},onError:function(){},onUnsupportedCodec:function(){},onMissingVideoFrames:function(){},onMissingAudioFrames:function(){},onKeyframePosition:function(){},onLoggerLog:console.log,onLoggerErr:console.error};return t.options=Object.assign({},a,e),t.env="object"===("undefined"==typeof process?"undefined":y(process))&&"undefined"==typeof window?"node":"browser",t.options.debug&&function(e,t){m=e,g=t}(t.options.onLoggerLog,t.options.onLoggerErr),t.options.fps||(t.options.fps=30),t.frameDuration=1e3/t.options.fps|0,t.remuxController=new R(t.env,e.live,t.options.videoCodec,t.frameDuration),t.remuxController.addTrack(t.options.mode),t.initData(),t.remuxController.on("buffer",t.onBuffer.bind(t)),"browser"==t.env&&(t.remuxController.on("ready",t.createBuffer.bind(t)),t.initBrowser()),t.remuxController.on("missingVideoFrames",function(){"function"==typeof t.options.onMissingVideoFrames&&t.options.onMissingVideoFrames.call(null)}),t.remuxController.on("missingAudioFrames",function(){"function"==typeof t.options.onMissingAudioFrames&&t.options.onMissingAudioFrames.call(null)}),t.clearBuffer&&t.remuxController.on("keyframePosition",function(e){t.kfPosition.push(e)}),"function"==typeof t.options.onKeyframePosition&&t.remuxController.on("keyframePosition",function(e){t.options.onKeyframePosition.call(null,e)}),t}return c(i,t),a(i,[{key:"initData",value:function(){this.lastCleaningTime=Date.now(),this.kfPosition=[],this.pendingUnits={},this.remainingData=new Uint8Array,this.startInterval()}},{key:"initBrowser",value:function(){"string"==typeof this.options.node&&""==this.options.node&&b("no video element were found to render, provide a valid video element"),this.node="string"==typeof this.options.node?document.getElementById(this.options.node):this.options.node,this.mseReady=!1,this.setupMSE()}},{key:"createStream",value:function(){var t=this.feed.bind(this),r=this.destroy.bind(this);return this.stream=new e.Duplex({writableObjectMode:!0,read:function(e){},write:function(e,r,n){t(e),n()},final:function(e){r(),e()}}),this.stream}},{key:"setupMSE",value:function(){if(window.MediaSource=window.MediaSource||window.WebKitMediaSource||window.ManagedMediaSource,!window.MediaSource)throw"Oops! Browser does not support Media Source Extension or Managed Media Source (IOS 17+).";if(this.isMSESupported=!!window.MediaSource,this.mediaSource=new window.MediaSource,this.url=URL.createObjectURL(this.mediaSource),window.MediaSource===window.ManagedMediaSource)try{this.node.removeAttribute("src"),this.node.disableRemotePlayback=!0;var e=document.createElement("source");e.type="video/mp4",e.src=this.url,this.node.appendChild(e),this.node.load()}catch(e){this.node.src=this.url}else this.node.src=this.url;this.mseEnded=!1,this.mediaSource.addEventListener("sourceopen",this.onMSEOpen.bind(this)),this.mediaSource.addEventListener("sourceclose",this.onMSEClose.bind(this)),this.mediaSource.addEventListener("webkitsourceopen",this.onMSEOpen.bind(this)),this.mediaSource.addEventListener("webkitsourceclose",this.onMSEClose.bind(this))}},{key:"endMSE",value:function(){if(!this.mseEnded)try{this.mseEnded=!0,this.mediaSource.endOfStream()}catch(e){b("mediasource is not available to end")}}},{key:"feed",value:function(e){e&&this.remuxController&&(e.duration=e.duration?parseInt(e.duration):0,this.remuxController.feed(e))}},{key:"destroy",value:function(){if(this.stopInterval(),this.stream&&(this.remuxController.flush(),this.stream.push(null),this.stream=null),this.remuxController&&(this.remuxController.destroy(),this.remuxController=null),this.bufferControllers){for(var e in this.bufferControllers)this.bufferControllers[e].destroy();this.bufferControllers=null,this.endMSE()}this.node=!1,this.mseReady=!1,this.videoStarted=!1,this.mediaSource=null}},{key:"reset",value:function(){if(this.stopInterval(),this.isReset=!0,this.node.pause(),this.remuxController&&this.remuxController.reset(),this.bufferControllers){for(var e in this.bufferControllers)this.bufferControllers[e].destroy();this.bufferControllers=null,this.endMSE()}this.initData(),"browser"==this.env&&this.initBrowser(),k("JMuxer was reset")}},{key:"createBuffer",value:function(){if(this.mseReady&&this.remuxController&&this.remuxController.isReady()&&!this.bufferControllers)for(var e in this.bufferControllers={},this.remuxController.tracks){var t=this.remuxController.tracks[e];if(!i.isSupported("".concat(e,'/mp4; codecs="').concat(t.mp4track.codec,'"')))return b("Browser does not support codec: ".concat(e,'/mp4; codecs="').concat(t.mp4track.codec,'"')),"function"==typeof this.options.onUnsupportedCodec&&this.options.onUnsupportedCodec.call(null,t.mp4track.codec),!1;var r=this.mediaSource.addSourceBuffer("".concat(e,'/mp4; codecs="').concat(t.mp4track.codec,'"'));this.bufferControllers[e]=new O(r,e),this.bufferControllers[e].on("error",this.onBufferError.bind(this))}}},{key:"startInterval",value:function(){var e=this;this.interval=setInterval(function(){e.options.flushingTime?e.applyAndClearBuffer():e.bufferControllers&&e.cancelDelay()},this.options.flushingTime||1e3)}},{key:"stopInterval",value:function(){this.interval&&clearInterval(this.interval)}},{key:"cancelDelay",value:function(){if(this.node.buffered&&this.node.buffered.length>0&&!this.node.seeking){var e=this.node.buffered.end(0);e-this.node.currentTime>this.options.maxDelay/1e3&&(k("delay"),this.node.paused&&this.node.play().catch(b),this.node.currentTime=e-.001)}}},{key:"releaseBuffer",value:function(){for(var e in this.bufferControllers)this.bufferControllers[e].doAppend()}},{key:"applyAndClearBuffer",value:function(){this.bufferControllers&&(this.releaseBuffer(),this.clearBuffer())}},{key:"getSafeClearOffsetOfBuffer",value:function(e){for(var t,r="audio"===this.options.mode&&e||0,n=0;n<this.kfPosition.length&&!(this.kfPosition[n]>=e);n++)t=this.kfPosition[n];return t&&(this.kfPosition=this.kfPosition.filter(function(e){return e<t&&(r=e),e>=t})),r}},{key:"clearBuffer",value:function(){if(this.options.clearBuffer&&Date.now()-this.lastCleaningTime>1e4){for(var e in this.bufferControllers){var t=this.getSafeClearOffsetOfBuffer(this.node.currentTime);this.bufferControllers[e].initCleanup(t)}this.lastCleaningTime=Date.now()}}},{key:"onBuffer",value:function(e){this.options.readFpsFromTrack&&void 0!==e.fps&&this.options.fps!=e.fps&&(this.options.fps=e.fps,this.frameDuration=Math.ceil(1e3/e.fps),k("JMuxer changed FPS to ".concat(e.fps," from track data"))),"browser"==this.env?this.bufferControllers&&this.bufferControllers[e.type]&&this.bufferControllers[e.type].feed(e.payload):this.stream&&this.stream.push(e.payload),this.options.onData&&this.options.onData(e.payload),0===this.options.flushingTime&&this.applyAndClearBuffer()}},{key:"onMSEOpen",value:function(){this.mseReady=!0,URL.revokeObjectURL(this.url),"function"==typeof this.options.onReady&&this.options.onReady.call(null,this.isReset,this.mediaSource)}},{key:"onMSEClose",value:function(){this.mseReady=!1,this.videoStarted=!1}},{key:"onBufferError",value:function(e){if("QuotaExceeded"==e.name)return k("JMuxer cleaning ".concat(e.type," buffer due to QuotaExceeded error")),void this.bufferControllers[e.type].initCleanup(this.node.currentTime);"InvalidStateError"==e.name?(k("JMuxer is reseting due to InvalidStateError"),this.reset()):this.endMSE(),"function"==typeof this.options.onError&&this.options.onError.call(null,e)}}],[{key:"isSupported",value:function(e){return window.MediaSource&&window.MediaSource.isTypeSupported(e)}}])}(U)});


// ================= LAYANAN (pindahan dari Pengaturan) =================
// Menu Layanan (drawer, di atas Scrcpy) menampung seksi yang soal
// perangkat lain mengakses HP ini: Hotspot, FTP Server, WebUI.
// Dipindah dari vSettings apa adanya (ID elemen & logika sama) agar
// perilaku tak berubah; simpan memakai PUT /api/settings yang di
// backend bersifat merge (field tak dikirim = nilai lama dipertahankan),
// jadi tombol Simpan di sini aman walau hanya mengirim field Layanan.
function vLayanan() {
  const s = S.settings;
  $('#view').innerHTML = `<div class="card"><h3><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.2 10.8l7.6-3.6M8.2 13.2l7.6 3.6"/></svg>Layanan</h3>
    <div class="sect">Hotspot</div>
    <label class="chk"><div class="t">Bagikan VPN ke Hotspot / Tethering<small>mode Hybrid/TProxy: klien hotspot ikut terproxy otomatis; mode TUN: ikut lewat tun0 (butuh Connect)</small></div><input type="checkbox" id="sHs" ${s.hotspot ? 'checked' : ''}></label>
    <label class="chk"><div class="t">Kunci IP hotspot<small>IP interface hotspot (AP/tethering) dikunci ke nilai di bawah</small></div><input type="checkbox" id="sHsLock" ${s.hotspot_lock_ip ? 'checked' : ''}></label>
    <label class="lbl">IP hotspot</label><input id="sHsIp" value="${esc(s.hotspot_ip || '192.168.56.1')}" placeholder="192.168.56.1">
    <div class="hint" id="hsStat">Memuat status hotspot…</div>

    <div class="sect">FTP Server</div>
    <label class="chk"><div class="t">FTP Server<small>bagikan file ke HP lain via FTP — buka dengan MT Manager (butuh hotspot/tethering aktif)</small></div><input type="checkbox" id="sFtp" ${s.ftp_enabled ? 'checked' : ''}></label>
    <div class="row2"><div><label class="lbl">Port FTP</label><input id="sFtpPort" type="number" value="${s.ftp_port || 2121}"></div>
    <div><label class="lbl">Folder yang dibagikan</label><input id="sFtpRoot" value="${esc(s.ftp_root || '/')}" placeholder="/"></div></div>
    <label class="chk"><div class="t">Izinkan tulis<small>HP lain bisa upload/hapus file di folder ini</small></div><input type="checkbox" id="sFtpW" ${s.ftp_write ? 'checked' : ''}></label>
    <div class="hint" id="ftpStat">Memuat status FTP…</div>

    <div class="sect">WebUI</div>
    <div class="row2"><div><label class="lbl">Port WebUI</label><input id="sWPort" type="number" value="${s.web_port || 5556}"></div></div>
    <label class="chk"><div class="t">Bisa dibuka dari HP lain (LAN)<small>buka http://IP-HP-ini:PORT dari HP lain di WiFi/hotspot yang sama</small></div><input type="checkbox" id="sWLan" ${s.web_listen_all ? 'checked' : ''}></label>
    <label class="lbl">Password akses dari HP lain (tanpa username, langsung password)</label><input id="sWPass" value="${esc(s.web_pass || '')}" autocomplete="off" placeholder="bawaan: 12345">
    <div class="hint">Dari HP ini (127.0.0.1) tanpa password. Dari HP lain di WiFi/hotspot, browser menampilkan halaman login — cukup masukkan password di atas, sekali saja.</div>
    <div class="hint" id="webuiUrl">…</div>

    <div class="row" style="margin-top:12px"><button class="btn primary" id="sSaveL"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 4h11l3 3v13H5z"/><path d="M8 4v5h7V4M8 20v-6h8v6"/></svg>Simpan</button></div>
    <div class="hint">Mengganti port/bind WebUI butuh restart backend (reboot / matikan-nyalakan modul).</div></div>`;

  const get = () => ({
    web_port: +$('#sWPort').value || 5556, web_listen_all: $('#sWLan').checked,
    hotspot: $('#sHs').checked,
    hotspot_lock_ip: $('#sHsLock').checked,
    hotspot_ip: $('#sHsIp').value.trim(),
    ftp_enabled: $('#sFtp').checked,
    ftp_port: +$('#sFtpPort').value || 2121,
    ftp_root: $('#sFtpRoot').value.trim() || '/',
    ftp_write: $('#sFtpW').checked,
    web_pass: $('#sWPass').value.trim(),
  });
  const ftpStat = async () => {
    try {
      const f = await api('/api/ftp');
      const el = $('#ftpStat');
      if (!el) return;
      if (f.running) {
        const addr = f.addr ? `ftp://${f.addr}:${f.port}` : `port ${f.port}`;
        el.innerHTML = `<i class="dot on"></i>Berjalan — buka di MT Manager: <b>${addr}</b> (anonim${f.write ? ', bisa tulis' : ', baca saja'})<br>Siapa pun yang terhubung ke hotspot bisa mengakses folder <code>${esc(f.root)}</code>.`;
      } else {
        el.innerHTML = '<i class="dot off"></i>Mati — centang lalu Simpan untuk menyalakan.';
      }
    } catch (e) { const el = $('#ftpStat'); if (el) el.textContent = 'Status FTP: ' + e.message; }
  };
  ftpStat();
  const hsStat = async () => {
    try {
      const hs = await api('/api/hotspot');
      const el = $('#hsStat'); if (!el) return;
      if (!hs.active) { el.textContent = 'Hotspot VPN mati — centang "Bagikan VPN ke Hotspot" lalu Simpan.'; return; }
      if (!hs.lock_ip) { el.textContent = 'Kunci IP nonaktif.'; return; }
      const ifs = (hs.ap_ifaces || []).join(', ') || '—';
      if ((hs.applied || []).length) {
        el.innerHTML = `<i class="dot on"></i>IP dikunci <b>${esc(hs.ip)}</b> di: <b>${esc(hs.applied.join(', '))}</b><br>Interface hotspot terdeteksi: ${esc(ifs)}`;
      } else {
        el.innerHTML = `<i class="dot off"></i>IP <b>${esc(hs.ip)}</b> belum terpasang.<br>Interface hotspot terdeteksi: ${esc(ifs)} — nyalakan hotspot HP dulu, lalu tunggu ±10 detik.`;
      }
    } catch (e) { const el = $('#hsStat'); if (el) el.textContent = 'Status hotspot: ' + e.message; }
  };
  hsStat();
  const wUrl = () => {
    const el = $('#webuiUrl'); if (!el) return;
    const port = +$('#sWPort').value || 5556;
    if ($('#sWLan').checked && S.status.lan_ip) {
      el.innerHTML = `<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="7" y="2.5" width="10" height="19" rx="2.4"/><path d="M11 18.5h2"/></svg>Buka di HP lain: <b>http://${esc(S.status.lan_ip)}:${port}</b> (satu WiFi/hotspot) — halaman login + password`;
    } else if ($('#sWLan').checked) {
      el.textContent = 'Aktifkan WiFi/hotspot, lalu buka http://IP-HP-ini:' + port + ' dari HP lain.';
    } else {
      el.textContent = 'Hanya bisa dibuka dari HP ini (127.0.0.1).';
    }
  };
  wUrl();
  $('#sWLan').onchange = wUrl; $('#sWPort').oninput = wUrl;
  $('#sSaveL').onclick = async () => { try { const r = await api('/api/settings', 'PUT', get()); toast(r.restart_backend ? 'Tersimpan — restart backend agar port/bind baru berlaku' : 'Tersimpan'); await reload(); } catch (e) { toast(e.message); } };
}

// ================= SCRCPY (mirror layar HP ke WebUI) =================
// Port halaman scrcpy AWDOS ke view TaptapBox, atas permintaan user:
// JS & CSS-nya DIGABUNG ke app.js/style.css (bukan file terpisah),
// kecuali jmuxer.min.js yang ditempel di atas blok ini (satu pustaka).
// Semua nama berawalan sc agar tak bentrok dengan kode utama.
// Backend: POST /api/scrcpy/* + WS /api/scrcpy/ws (origin sama, jadi
// tak ada lagi kolom Target IP seperti di halaman AWDOS — daemon lokal).
// Versi server: scrcpy 5.0.1 (jar resmi Genymobile).

const scS = { cfg: null, loaded: false, wsVideo: null, wsAudio: null, wsControl: null, jmVideo: null, jmAudio: null, rc: 0, frameCount: 0, intervals: false, keyInit: false, lastMove: {}, scrollX: 0, scrollY: 0, lastScroll: 0, scrollTo: null };
const scDef = { res: '1080', bitrate: '8000000', fps: '60', audio_bitrate: '128000', audio_buffer: '50', audio_enabled: false, keyboard: true, scroll: true, floating: false, fps_counter: true };

function vScrcpy() {
  $('#view').innerHTML = `<div class="card"><h3>Scrcpy</h3>
    <label class="chk" style="border-bottom:0"><span class="t">Mirror Layar<b id="scMirrorStateText" style="display:block;color:var(--acc);font-size:12px;font-weight:600;margin-top:2px">Start Mirroring</b><small>Aktifkan untuk menampilkan layar HP di sini</small></span><input type="checkbox" id="scMirrorToggle"></label>
  </div>
  <div class="card"><h3>Streaming Setup</h3>
    <label class="lbl">Resolusi</label><select id="scResOpt"><option value="480">480p</option><option value="720">720p</option><option value="1080">1080p</option><option value="0">Asli (native)</option></select>
    <label class="lbl">Bitrate</label><select id="scBitrateOpt"><option value="500000">500 Kbps</option><option value="1000000">1 Mbps</option><option value="2000000">2 Mbps</option><option value="4000000">4 Mbps</option><option value="8000000">8 Mbps</option><option value="12000000">12 Mbps</option><option value="16000000">16 Mbps</option></select>
    <label class="lbl">Framerate</label><select id="scFpsOpt"><option value="15">15 FPS</option><option value="30">30 FPS</option><option value="60">60 FPS</option><option value="0">Max</option></select>
    <label class="lbl">Bitrate Audio</label><select id="scAudioBitrateOpt"><option value="64000">64 Kbps</option><option value="128000">128 Kbps</option><option value="256000">256 Kbps</option><option value="320000">320 Kbps</option></select>
    <label class="lbl">Buffer Audio</label><select id="scAudioBufferOpt"><option value="0">Zero</option><option value="10">10 ms</option><option value="50">50 ms</option></select>
    <label class="chk"><span class="t">Forward Audio<small>Suara perangkat</small></span><input type="checkbox" id="scToggleAudio"></label>
  </div>
  <div class="card"><h3>Opsi Lanjutan</h3>
    <label class="chk"><span class="t">Keyboard Input<small>Ketik dari browser</small></span><input type="checkbox" id="scToggleKeyboard"></label>
    <label class="chk"><span class="t">Mouse Scroll<small>Roda mouse untuk swipe</small></span><input type="checkbox" id="scToggleScroll"></label>
    <label class="chk"><span class="t">Float Controls<small>Tombol di atas video</small></span><input type="checkbox" id="scToggleFloating"></label>
    <label class="chk"><span class="t">FPS Counter<small>Tampilkan performa</small></span><input type="checkbox" id="scToggleFPS"></label>
  </div>
  <div class="sc-video-col"><div class="sc-dev-wrap" id="scWrap">
    <div id="scLoader" class="sc-loader"><div class="sc-spinner"></div><p>Connecting...</p></div>
    <div id="scFpsCounter" class="sc-fps">0 FPS</div>
    <video id="scPlayer" autoplay playsinline></video>
    <canvas id="scCanvas"></canvas>
    <audio id="scAudioPlayer" autoplay playsinline style="display:none"></audio>
    <div class="sc-control-panel" id="scControlPanel">
      <button class="sc-control-btn" onclick="window.scSendKey(4)" title="Back"><svg viewBox="0 0 24 24"><path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z"/></svg></button>
      <button class="sc-control-btn" onclick="window.scSendKey(3)" title="Home"><svg viewBox="0 0 24 24"><path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z"/></svg></button>
      <button class="sc-control-btn" onclick="window.scSendKey(187)" title="Recent Apps"><svg viewBox="0 0 24 24"><path d="M19 3H5c-1.11 0-2 .9-2 2v14c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V5h14v14z"/></svg></button>
      <button class="sc-control-btn" onclick="window.scPastePrompt()" title="Kirim Teks (Paste)"><svg viewBox="0 0 24 24"><path d="M19 2h-4.18C14.4.84 13.3 0 12 0c-1.3 0-2.4.84-2.82 2H5c-1.1 0-2 .9-2 2v16c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-7 0c.55 0 1 .45 1 1s-.45 1-1 1-1-.45-1-1 .45-1 1-1zm7 18H5V4h2v3h10V4h2v16z"/></svg></button>
      <div class="sc-sep"></div>
      <button class="sc-control-btn" onclick="window.scSendKey(26)" title="Power"><svg viewBox="0 0 24 24"><path d="M13 3h-2v10h2V3zm4.83 2.17l-1.42 1.42C17.99 7.86 19 9.81 19 12c0 3.87-3.13 7-7 7s-7-3.13-7-7c0-2.19 1.01-4.14 2.58-5.42L6.17 5.17C4.23 6.82 3 9.26 3 12c0 4.97 4.03 9 9 9s9-4.03 9-9c0-2.74-1.23-5.18-3.17-6.83z"/></svg></button>
      <button class="sc-control-btn" onclick="window.scSendKey(25)" title="Vol Down"><svg viewBox="0 0 24 24"><path d="M18.5 12c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM5 9v6h4l5 5V4L9 9H5z"/></svg></button>
      <button class="sc-control-btn" onclick="window.scSendKey(24)" title="Vol Up"><svg viewBox="0 0 24 24"><path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/></svg></button>
    </div>
    <div class="sc-controls" id="scNavBar">
      <button class="sc-btn-nav" onclick="window.scPastePrompt()" title="Kirim Teks (Paste)"><svg viewBox="0 0 24 24"><path d="M19 2h-4.18C14.4.84 13.3 0 12 0c-1.3 0-2.4.84-2.82 2H5c-1.1 0-2 .9-2 2v16c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-7 0c.55 0 1 .45 1 1s-.45 1-1 1-1-.45-1-1 .45-1 1-1zm7 18H5V4h2v3h10V4h2v16z"/></svg></button>
      <button class="sc-btn-nav" onclick="window.scSendKey(4)" title="Back"><svg viewBox="0 0 24 24"><path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z"/></svg></button>
      <button class="sc-btn-nav" onclick="window.scSendKey(3)" title="Home"><svg viewBox="0 0 24 24"><path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z"/></svg></button>
      <button class="sc-btn-nav" onclick="window.scSendKey(187)" title="Recent Apps"><svg viewBox="0 0 24 24"><path d="M19 3H5c-1.11 0-2 .9-2 2v14c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V5h14v14z"/></svg></button>
      <button class="sc-btn-nav" onclick="window.scSendKey(26)" title="Power"><svg viewBox="0 0 24 24"><path d="M13 3h-2v10h2V3zm4.83 2.17l-1.42 1.42C17.99 7.86 19 9.81 19 12c0 3.87-3.13 7-7 7s-7-3.13-7-7c0-2.19 1.01-4.14 2.58-5.42L6.17 5.17C4.23 6.82 3 9.26 3 12c0 4.97 4.03 9 9 9s9-4.03 9-9c0-2.74-1.23-5.18-3.17-6.83z"/></svg></button>
    </div>
  </div></div>
  <textarea id="scHiddenPasteBin" class="sc-hidden-bin"></textarea>`;
  scBindView();
  scLoadConfig();
  scSyncRunning();
  scStartIntervals();
  scInitKeys();
}

function scBindView() {
  const $id = id => document.getElementById(id);
  $id('scMirrorToggle').onchange = () => scTgc();
  ['scResOpt', 'scBitrateOpt', 'scFpsOpt', 'scAudioBitrateOpt', 'scAudioBufferOpt'].forEach(id => { $id(id).onchange = () => scSaveConfig(); });
  ['scToggleAudio', 'scToggleKeyboard', 'scToggleScroll', 'scToggleFloating', 'scToggleFPS'].forEach(id => { $id(id).onchange = () => scUpdateSettings(); });
  const bindSurf = p => {
  p.addEventListener('pointerdown', e => { if (e.pointerType === 'mouse' && e.button !== 0) return; try { p.setPointerCapture(e.pointerId); } catch (err) {} scSendControl(0, e); e.preventDefault(); });
  p.addEventListener('pointermove', e => { const now = Date.now(); if (!scS.lastMove[e.pointerId]) scS.lastMove[e.pointerId] = 0; if (now - scS.lastMove[e.pointerId] > 16) { scS.lastMove[e.pointerId] = now; scSendControl(2, e); } e.preventDefault(); });
  const up = e => { try { p.releasePointerCapture(e.pointerId); } catch (err) {} scSendControl(1, e); e.preventDefault(); };
  p.addEventListener('pointerup', up);
  p.addEventListener('pointercancel', up);
  p.addEventListener('contextmenu', e => { e.preventDefault(); window.scSendKey(4); });
  p.addEventListener('mousedown', e => { if (e.button === 1) { e.preventDefault(); window.scSendKey(3); } else if (e.button === 2) { e.preventDefault(); } });
  p.addEventListener('wheel', e => scOnWheel(e), { passive: false });
  };
  bindSurf($id('scPlayer')); bindSurf($id('scCanvas'));
}

function scNum(sel, d) { const v = parseInt($(sel).value, 10); return isNaN(v) ? d : v; }
function scCfgFromDom() {
  return {
    res: scNum('#scResOpt', 1080), bitrate: scNum('#scBitrateOpt', 8000000), fps: scNum('#scFpsOpt', 60),
    audio_bitrate: scNum('#scAudioBitrateOpt', 128000), audio_buffer: scNum('#scAudioBufferOpt', 50),
    audio_enabled: $('#scToggleAudio').checked, keyboard: $('#scToggleKeyboard').checked,
    scroll: $('#scToggleScroll').checked, floating: $('#scToggleFloating').checked, fps_counter: $('#scToggleFPS').checked
  };
}
function scApplyCfg(c) {
  const d = Object.assign({}, scDef, c || {});
  const set = (id, v) => { const el = document.getElementById(id); if (el) el.value = String(v); };
  const chk = (id, v) => { const el = document.getElementById(id); if (el) el.checked = !!v; };
  set('scResOpt', d.res); set('scBitrateOpt', d.bitrate); set('scFpsOpt', d.fps);
  set('scAudioBitrateOpt', d.audio_bitrate); set('scAudioBufferOpt', d.audio_buffer);
  chk('scToggleAudio', d.audio_enabled); chk('scToggleKeyboard', d.keyboard); chk('scToggleScroll', d.scroll);
  chk('scToggleFloating', d.floating); chk('scToggleFPS', d.fps_counter);
  scS.cfg = d; scS.loaded = true;
}
function scSaveConfig() {
  if (!scS.loaded) return;
  const cfg = scCfgFromDom();
  try { localStorage.setItem('scrcpy_config', JSON.stringify(cfg)); } catch (e) {}
  api('/api/scrcpy/config', 'POST', cfg).catch(() => {});
  scS.cfg = cfg;
}
async function scLoadConfig() {
  let local = null;
  try { const s = localStorage.getItem('scrcpy_config'); if (s) { local = JSON.parse(s); scApplyCfg(local); } } catch (e) {}
  try {
    const cfg = await api('/api/scrcpy/config');
    if (cfg && Object.keys(cfg).length > 0) { scApplyCfg(cfg); try { localStorage.setItem('scrcpy_config', JSON.stringify(cfg)); } catch (e) {} }
    else if (!local) scApplyCfg(scDef);
  } catch (e) { if (!local) scApplyCfg(scDef); }
}
function scUpdateSettings() {
  const show = $('#scToggleFloating') && $('#scToggleFloating').checked;
  const cp = $('#scControlPanel'); if (cp) cp.style.display = show ? 'flex' : 'none';
  scSaveConfig();
}
async function scSyncRunning() {
  try {
    const st = await api('/api/scrcpy/status');
    if (st && st.running) { const t = $('#scMirrorToggle'); if (t) { t.checked = true; scSetBtn('on'); scConnectWS(); } }
    else if (st && !st.running) { const t = $('#scMirrorToggle'); if (t && !t.checked && scBgOff) scBgRestore(); }
  } catch (e) {}
}
function scSetBtn(state) {
  const t = $('#scMirrorToggle'), txt = $('#scMirrorStateText'), wrap = $('#scWrap'), player = $('#scPlayer'), cvs = $('#scCanvas'), loader = $('#scLoader'), cp = $('#scControlPanel'), nb = $('#scNavBar');
  if (!t) return;
  if (state === 'ing') {
    t.disabled = true; if (txt) txt.innerText = 'Connecting...';
    if (loader) loader.style.display = 'flex'; if (wrap) wrap.style.display = 'flex';
    if (player) player.style.opacity = 0; if (cvs) cvs.style.opacity = 0; if (cp) cp.style.display = 'none'; if (nb) nb.style.display = 'none';
  } else if (state === 'on') {
    t.disabled = false; t.checked = true; if (txt) txt.innerText = 'Mirroring Active';
    if (loader) loader.style.display = 'none'; if (player) player.style.opacity = 1; if (cvs) cvs.style.opacity = 1;
    if (wrap) { wrap.style.width = 'fit-content'; wrap.style.height = 'auto'; }
    if (cp) cp.style.display = ($('#scToggleFloating') && $('#scToggleFloating').checked) ? 'flex' : 'none';
    if (nb) nb.style.display = 'flex';
    if (!scBgOff) { // simpan pilihan + ubah setting ke 0 (asli, bukan tiruan)
      scPrevBg = S.settings.animasi_bg || 0;
      if (scPrevBg) {
        scBgStorePrev(scPrevBg);
        api('/api/settings', 'PUT', { animasi_bg: 0 }).then(() => { S.settings.animasi_bg = 0; }).catch(() => {});
      }
    }
    scBgOff = true; applyBg(); // Scrcpy nyala -> tanpa animasi asli
  } else {
    t.disabled = false; t.checked = false; if (txt) txt.innerText = 'Start Mirroring';
    if (wrap) wrap.style.display = 'none'; if (player) player.style.opacity = 0; if (cvs) cvs.style.opacity = 0;
    if (scS.wsVideo) { try { scS.wsVideo.close(); } catch (e) {} scS.wsVideo = null; }
    if (scS.wsAudio) { try { scS.wsAudio.close(); } catch (e) {} scS.wsAudio = null; }
    if (scS.wsControl) { try { scS.wsControl.close(); } catch (e) {} scS.wsControl = null; }
    if (scS.jmVideo) { try { scS.jmVideo.destroy(); } catch (e) {} scS.jmVideo = null; }
    if (scS.jmAudio) { try { scS.jmAudio.destroy(); } catch (e) {} scS.jmAudio = null; }
    scWcClose();
    api('/api/scrcpy/stop', 'POST').catch(() => {});
    scBgRestore(); // Scrcpy mati -> animasi pilihan kembali
  }
}
// ---- jalur decode video ----
// WebCodecs (VideoDecoder -> canvas): tanpa buffer MSE, tiap frame langsung
// ditampilkan = latensi minimal & gambar tajam 1:1. Syarat: secure context
// (https atau 127.0.0.1/localhost). Di luar itu / bila dekoder gagal -> jalur
// MSE lama (JMuxer) dengan durasi frame dari waktu kedatangan nyata.
function scSurf() {
  const c = $('#scCanvas');
  if (scS.useWC && c && c.width) return { el: c, w: c.width, h: c.height };
  const p = $('#scPlayer');
  return p && p.videoWidth ? { el: p, w: p.videoWidth, h: p.videoHeight } : null;
}
// Pindai NAL awal frame Annex B: ada SPS? keyframe (IDR)? Berhenti di slice pertama.
function scAnalyze(u) {
  let key = false, sps = -1;
  for (let i = 0; i + 3 < u.length; i++) {
    if (u[i] === 0 && u[i + 1] === 0 && u[i + 2] === 1) {
      const t = u[i + 3] & 31;
      if (t === 7 && sps < 0) sps = i + 3;
      else if (t === 5) { key = true; break; }
      else if (t === 1) break;
      i += 2;
    }
  }
  return { key, sps };
}
function scSpsKey(u, at) {
  let j = at + 1;
  while (j + 2 < u.length && !(u[j] === 0 && u[j + 1] === 0 && (u[j + 2] === 1 || (u[j + 2] === 0 && u[j + 3] === 1)))) j++;
  return Array.prototype.join.call(u.subarray(at, Math.min(j, u.length)), ',');
}
function scCodecFromSps(u, at) {
  const h = v => ('0' + v.toString(16)).slice(-2);
  return 'avc1.' + h(u[at + 1]) + h(u[at + 2]) + h(u[at + 3]);
}
function scWcOk() {
  let off = false; try { off = sessionStorage.getItem('sc_nowc') === '1'; } catch (e) {}
  return !off && typeof VideoDecoder === 'function' && typeof EncodedVideoChunk === 'function' && window.isSecureContext === true;
}
function scWcClose() {
  if (scS.raf) { cancelAnimationFrame(scS.raf); scS.raf = 0; }
  if (scS.wcFrame) { try { scS.wcFrame.close(); } catch (e) {} scS.wcFrame = null; }
  if (scS.wcDec) { try { scS.wcDec.close(); } catch (e) {} scS.wcDec = null; }
  scS.wcSps = ''; scS.wcGotKey = false; scS.wcCtx = null;
}
function scWcDraw() {
  scS.raf = 0;
  const f = scS.wcFrame; if (!f) return;
  scS.wcFrame = null;
  const c = $('#scCanvas');
  if (c) {
    const w = f.displayWidth, h = f.displayHeight;
    if (c.width !== w || c.height !== h) { c.width = w; c.height = h; scS.wcCtx = null; }
    if (!scS.wcCtx) scS.wcCtx = c.getContext('2d', { alpha: false, desynchronized: true });
    scS.wcCtx.drawImage(f, 0, 0);
    scS.frameCount++; // FPS counter = frame yang benar-benar ter-decode & tergambar
  }
  try { f.close(); } catch (e) {}
}
function scWcFail() {
  // Dekoder bermasalah: pindah ke jalur MSE & mulai ulang sesi (keyframe baru).
  try { sessionStorage.setItem('sc_nowc', '1'); } catch (e) {}
  const t = $('#scMirrorToggle'); if (!t || !t.checked) return;
  toast('Dekoder WebCodecs gagal, beralih ke mode kompatibel...');
  t.checked = false; scTgc();
  setTimeout(() => { const t2 = $('#scMirrorToggle'); if (t2) { t2.checked = true; scTgc(); } }, 900);
}
function scWcStart(codec) {
  scWcClose();
  const dec = new VideoDecoder({
    output: f => {
      if (scS.wcFrame) { try { scS.wcFrame.close(); } catch (e) {} } // buang frame usang, tampilkan yang terbaru
      scS.wcFrame = f;
      if (!scS.raf) scS.raf = requestAnimationFrame(scWcDraw);
    },
    error: () => scWcFail()
  });
  dec.configure({ codec: codec, optimizeForLatency: true });
  scS.wcDec = dec;
}
function scFeedVideo(u) {
  if (!scS.useWC) {
    // MSE: durasi frame = jeda kedatangan nyata (bukan 1000/fps tetap) agar
    // garis waktu buffer mengikuti jam dinding dan latensi tak menumpuk.
    const now = performance.now();
    let dur = scS.lastArr ? now - scS.lastArr : 16; scS.lastArr = now;
    dur = Math.max(1, Math.min(100, Math.round(dur)));
    scS.jmVideo.feed({ video: u, duration: dur });
    scS.frameCount++;
    return;
  }
  const a = scAnalyze(u);
  if (a.sps >= 0) {
    const k = scSpsKey(u, a.sps);
    if (k !== scS.wcSps) { // SPS baru (awal sesi / rotasi layar) -> konfigurasi ulang
      try { scWcStart(scCodecFromSps(u, a.sps)); } catch (e) { scWcFail(); return; }
      scS.wcSps = k;
      const c = $('#scCanvas'), p = $('#scPlayer');
      if (c) c.style.display = 'block';
      if (p) p.style.display = 'none';
    }
  }
  const dec = scS.wcDec;
  if (!dec || dec.state !== 'configured') return;
  if (!scS.wcGotKey) { if (!a.key) return; scS.wcGotKey = true; }
  try { dec.decode(new EncodedVideoChunk({ type: a.key ? 'key' : 'delta', timestamp: Math.round(performance.now() * 1000), data: u })); }
  catch (e) { scWcFail(); }
}
function scInitJmuxers(withAudio) {
  if (scS.jmVideo) { try { scS.jmVideo.destroy(); } catch (e) {} }
  if (scS.jmAudio) { try { scS.jmAudio.destroy(); } catch (e) {} }
  let fps = parseInt($('#scFpsOpt').value); if (fps === 0 || isNaN(fps)) fps = 60;
  if (!scS.useWC) scS.jmVideo = new JMuxer({ node: 'scPlayer', mode: 'video', flushingTime: 0, clearBuffer: true, fps: fps, debug: false });
  if (withAudio) {
    let ab = parseInt($('#scAudioBufferOpt').value); if (isNaN(ab)) ab = 50; if (ab === 0) ab = 10;
    scS.jmAudio = new JMuxer({ node: 'scAudioPlayer', mode: 'audio', flushingTime: ab, clearBuffer: false, debug: false });
  }
}
function scWsUrl() { return (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/scrcpy/ws'; }
function scConnectWS() {
  const audioOn = $('#scToggleAudio') && $('#scToggleAudio').checked;
  scWcClose();
  scS.useWC = scWcOk(); scS.lastArr = 0;
  { const c = $('#scCanvas'), p = $('#scPlayer'); if (c) c.style.display = 'none'; if (p) p.style.display = ''; }
  scInitJmuxers(audioOn);
  const url = scWsUrl();
  scS.wsVideo = new WebSocket(url);
  scS.wsVideo.binaryType = 'arraybuffer';
  scS.wsVideo.onopen = () => { scS.wsVideo.send('video'); scSetBtn('on'); scS.rc = 0; };
  scS.wsVideo.onmessage = e => { if (e.data instanceof ArrayBuffer && (scS.useWC || scS.jmVideo)) scFeedVideo(new Uint8Array(e.data)); };
  scS.wsVideo.onclose = () => { const t = $('#scMirrorToggle'); if (t && t.checked && scS.rc < 5) { scS.rc++; setTimeout(scConnectWS, 2000); } else scSetBtn('off'); };
  scS.wsVideo.onerror = () => { if (scS.wsVideo) scS.wsVideo.close(); };
  if (audioOn) {
    scS.wsAudio = new WebSocket(url);
    scS.wsAudio.binaryType = 'arraybuffer';
    scS.wsAudio.onopen = () => { scS.wsAudio.send('audio'); const ap = $('#scAudioPlayer'); if (ap) { ap.muted = false; ap.play().catch(() => {}); } };
    scS.wsAudio.onmessage = e => { if (e.data instanceof ArrayBuffer && scS.jmAudio) { scS.jmAudio.feed({ audio: new Uint8Array(e.data) }); } };
    scS.wsAudio.onclose = () => {};
    scS.wsAudio.onerror = () => { if (scS.wsAudio) scS.wsAudio.close(); };
  }
  scS.wsControl = new WebSocket(url);
  scS.wsControl.onopen = () => scS.wsControl.send('control');
  scS.wsControl.onmessage = e => {
    if (typeof e.data === 'string') {
      try { const m = JSON.parse(e.data); if (m.type === 'clipboard_device') scShowToast(m.text); } catch (err) {}
    }
  };
}
function scTgc() {
  const on = $('#scMirrorToggle').checked;
  if (on) {
    scSetBtn('ing');
    if (scS.wsControl) { try { scS.wsControl.close(); } catch (e) {} }
    const cfg = scCfgFromDom();
    const rawRes = parseInt(cfg.res) || 720; let tw = rawRes, th = Math.round(rawRes * (16 / 9));
    const maxH = window.innerHeight * 0.85;
    if (th > maxH) { const r = maxH / th; th = maxH; tw = tw * r; }
    const wrap = $('#scWrap'); if (wrap) { wrap.style.width = tw + 'px'; wrap.style.height = th + 'px'; }
    api('/api/scrcpy/start', 'POST', {
      res: parseInt(cfg.res), bitrate: parseInt(cfg.bitrate), fps: parseInt(cfg.fps),
      audio_bitrate: parseInt(cfg.audio_bitrate), audio_enabled: cfg.audio_enabled
    }).then(() => scConnectWS()).catch(() => scSetBtn('off'));
  } else scSetBtn('off');
}
function scSendControl(action, e) {
  if (!scS.wsControl || scS.wsControl.readyState !== WebSocket.OPEN) return;
  const S = scSurf(); if (!S) return;
  const rect = S.el.getBoundingClientRect();
  const vr = S.w / S.h, er = rect.width / rect.height;
  let dw, dh, ox, oy;
  if (er > vr) { dh = rect.height; dw = rect.height * vr; ox = (rect.width - dw) / 2; oy = 0; }
  else { dw = rect.width; dh = rect.width / vr; ox = 0; oy = (rect.height - dh) / 2; }
  let x = (e.clientX - rect.left - ox) * (S.w / dw);
  let y = (e.clientY - rect.top - oy) * (S.h / dh);
  x = Math.max(0, Math.min(S.w, x)); y = Math.max(0, Math.min(S.h, y));
  scS.wsControl.send(JSON.stringify({ type: 'touch', action: action, id: e.pointerId, x: Math.round(x), y: Math.round(y), w: S.w, h: S.h }));
}
function scOnWheel(e) {
  const scOn = $('#scToggleScroll') && $('#scToggleScroll').checked;
  if (!scOn) return;
  const S = scSurf(); if (!S) return;
  const rect = S.el.getBoundingClientRect();
  const vr = S.w / S.h, er = rect.width / rect.height;
  let dw, dh, ox, oy;
  if (er > vr) { dh = rect.height; dw = rect.height * vr; ox = (rect.width - dw) / 2; oy = 0; }
  else { dw = rect.width; dh = rect.width / vr; ox = 0; oy = (rect.height - dh) / 2; }
  let x = (e.clientX - rect.left - ox) * (S.w / dw);
  let y = (e.clientY - rect.top - oy) * (S.h / dh);
  x = Math.max(0, Math.min(S.w, x)); y = Math.max(0, Math.min(S.h, y));
  scS.scrollX += e.deltaX === 0 ? 0 : (e.deltaX > 0 ? -1.0 : 1.0);
  scS.scrollY += e.deltaY === 0 ? 0 : (e.deltaY > 0 ? -1.0 : 1.0);
  const now = Date.now();
  if (now - scS.lastScroll > 30) {
    if (scS.scrollTo) { clearTimeout(scS.scrollTo); scS.scrollTo = null; }
    scFlushScroll(x, y);
  } else if (!scS.scrollTo) {
    scS.scrollTo = setTimeout(() => { scFlushScroll(x, y); scS.scrollTo = null; }, 30);
  }
  e.preventDefault();
}
function scFlushScroll(x, y) {
  if (!scS.wsControl || scS.wsControl.readyState !== WebSocket.OPEN) return;
  if (scS.scrollX === 0 && scS.scrollY === 0) return;
  const S = scSurf(); if (!S) return;
  scS.wsControl.send(JSON.stringify({ type: 'scroll', x: Math.round(x), y: Math.round(y), w: S.w, h: S.h, hScroll: scS.scrollX, vScroll: scS.scrollY }));
  scS.scrollX = 0; scS.scrollY = 0; scS.lastScroll = Date.now();
}
function scSendKey(c) {
  if (scS.wsControl && scS.wsControl.readyState === WebSocket.OPEN) {
    scS.wsControl.send(JSON.stringify({ type: 'key', action: 0, keycode: c }));
    setTimeout(() => { if (scS.wsControl) scS.wsControl.send(JSON.stringify({ type: 'key', action: 1, keycode: c })); }, 50);
  }
}
window.scSendKey = scSendKey;
function scPastePrompt() {
  const t = prompt('Masukkan teks untuk diketik instan ke HP:');
  if (t) api('/api/scrcpy/paste', 'POST', { text: t }).catch(() => {});
}
window.scPastePrompt = scPastePrompt;
function scShowToast(text) {
  let box = document.getElementById('scToastBox');
  if (!box) { box = document.createElement('div'); box.id = 'scToastBox'; box.className = 'sc-toast-box'; document.body.appendChild(box); }
  const d = document.createElement('div'); d.className = 'sc-toast';
  const safe = String(text).replace(/'/g, "\\'");
  d.innerHTML = `<div class="sc-toast-t">Clipboard HP:</div><div class="sc-toast-b">${esc(text)}</div><button onclick="navigator.clipboard.writeText('${safe}').then(()=>this.innerText='Disalin!').catch(()=>this.innerText='Gagal (HTTP)')">Salin ke PC</button>`;
  box.appendChild(d);
  setTimeout(() => { d.style.opacity = 0; setTimeout(() => d.remove(), 300); }, 5000);
}
const scKeyMap = { 'Backspace': 67, 'Enter': 66, 'Space': 62, 'Escape': 4, 'Home': 3, 'ArrowUp': 19, 'ArrowDown': 20, 'ArrowLeft': 21, 'ArrowRight': 22, 'Tab': 61, 'Delete': 112, 'Minus': 69, 'Equal': 70, 'BracketLeft': 71, 'BracketRight': 72, 'Backslash': 73, 'Semicolon': 74, 'Quote': 75, 'Comma': 55, 'Period': 56, 'Slash': 76, 'Backquote': 68 };
for (let i = 0; i < 26; i++) scKeyMap['Key' + String.fromCharCode(65 + i)] = 29 + i;
for (let i = 1; i <= 9; i++) scKeyMap['Digit' + i] = 7 + i;
scKeyMap['Digit0'] = 7;
function scGetMeta(e) { let m = 0; if (e.shiftKey) m |= 1; if (e.altKey) m |= 2; if (e.ctrlKey) m |= 4096; if (e.metaKey) m |= 65536; return m; }
function scInitKeys() {
  if (scS.keyInit) return; scS.keyInit = true;
  document.addEventListener('keydown', e => {
    const kb = $('#scToggleKeyboard'); if (kb && !kb.checked) return;
    if (!scS.wsControl || scS.wsControl.readyState !== WebSocket.OPEN) return;
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT' || e.target.tagName === 'TEXTAREA') return;
    if (e.ctrlKey || e.metaKey) {
      if (e.code === 'KeyV') {
        const pb = document.getElementById('scHiddenPasteBin');
        if (pb && e.target !== pb) {
          pb.value = ''; pb.focus();
          setTimeout(() => {
            const text = pb.value;
            if (text) api('/api/scrcpy/paste', 'POST', { text: text }).then(() => toast('Teks dikirim ke HP')).catch(() => {});
            pb.blur();
          }, 50);
        }
        return;
      } else if (e.code === 'KeyC' || e.code === 'KeyX' || e.code === 'KeyA') return;
    }
    if (e.altKey) {
      let handled = true;
      switch (e.code) {
        case 'KeyH': window.scSendKey(3); break; case 'KeyB': window.scSendKey(4); break;
        case 'KeyS': window.scSendKey(187); break; case 'KeyM': window.scSendKey(82); break;
        case 'ArrowUp': window.scSendKey(24); break; case 'ArrowDown': window.scSendKey(25); break;
        case 'KeyP': window.scSendKey(26); break; default: handled = false;
      }
      if (handled) { e.preventDefault(); return; }
    }
    const code = scKeyMap[e.code];
    if (code) { scS.wsControl.send(JSON.stringify({ type: 'key', action: 0, keycode: code, meta: scGetMeta(e) })); e.preventDefault(); }
  });
  document.addEventListener('keyup', e => {
    const kb = $('#scToggleKeyboard'); if (kb && !kb.checked) return;
    if (!scS.wsControl || scS.wsControl.readyState !== WebSocket.OPEN) return;
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT' || e.target.tagName === 'TEXTAREA') return;
    if (e.ctrlKey || e.metaKey || e.code === 'KeyV' || e.code === 'KeyC' || e.code === 'KeyX' || e.code === 'KeyA') return;
    const code = scKeyMap[e.code];
    if (code) { scS.wsControl.send(JSON.stringify({ type: 'key', action: 1, keycode: code, meta: scGetMeta(e) })); e.preventDefault(); }
  });
  window.addEventListener('beforeunload', () => {
    const t = document.getElementById('scMirrorToggle');
    if (t && t.checked) { try { navigator.sendBeacon('/api/scrcpy/stop'); } catch (e) {} }
  });
}
function scStartIntervals() {
  if (scS.intervals) return; scS.intervals = true;
  setInterval(() => {
    const fc = $('#scFpsCounter'), t = $('#scMirrorToggle');
    if (fc && t && $('#scToggleFPS') && $('#scToggleFPS').checked && t.checked) { fc.innerText = scS.frameCount + ' FPS'; fc.style.display = 'block'; }
    else if (fc) fc.style.display = 'none';
    scS.frameCount = 0;
  }, 1000);
  setInterval(() => {
    const player = $('#scPlayer');
    if (!scS.useWC && player && !player.paused && player.buffered && player.buffered.length > 0) {
      const end = player.buffered.end(player.buffered.length - 1);
      const lat = end - player.currentTime;
      if (lat > 0.4) player.currentTime = end - 0.03;          // ketinggalan jauh: lompat ke tepi live
      else if (lat > 0.08) player.playbackRate = 1 + Math.min(lat, 0.5); // kejar halus
      else player.playbackRate = 1.0;
    }
  }, 150);
}
