package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// R83: the pages of the sales manager (/crm, /crm/join/<token>,
// /crm/in/<token>) and the cycle planner (/cycle/<id>). Plain pages, no
// platform bundle: a manager's browser never loads the club's code or data.

func serveManagerApp(c *gin.Context)  { serveMiniPage(c, managerAppHTML) }
func serveManagerJoin(c *gin.Context) { servePlainPage(c, managerJoinHTML) }
func serveManagerLink(c *gin.Context) { servePlainPage(c, managerLinkHTML) }
func serveCyclePage(c *gin.Context)   { serveMiniPage(c, cyclePageHTML) }

// serveMiniPage: as servePlainPage, but it may open inside Telegram (a Mini
// App button: Telegram Web shows it in a frame).
func serveMiniPage(c *gin.Context, body string) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Referrer-Policy", "strict-origin")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

var managerJoinHTML = plainPageHead + `<title>Приглашение в CRM · Business Surgery</title>
</head>
<body>
<main><div class="card">
  <div class="brand"><img src="/site/logo-white.png" alt="" width="34" height="16">Business Surgery</div>
  <h1 id="t">Приглашение в CRM</h1>
  <p id="s">Проверяю приглашение…</p>
  <div class="facts" id="f" hidden></div>
  <div class="w" id="w"></div>
  <div class="msg" id="m" role="status" aria-live="polite"></div>
  ` + plainPageFoot + `
</div></main>
<script>
` + plainPageJS + `
(function(){
  var T = token(), W = $('w');
  var TG_IC = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21.4 4.2 18.3 19c-.2 1-.9 1.3-1.7.8l-4.8-3.5-2.3 2.2c-.3.3-.5.5-1 .5l.3-4.9 8.9-8c.4-.3-.1-.5-.6-.2L6.1 12.8l-4.7-1.5c-1-.3-1-1 .2-1.5L20 3c.9-.3 1.6.2 1.4 1.2z"/></svg>';
  function crm(j){
    try{ localStorage.setItem('bsx_token', j.token); localStorage.setItem('bsx_user', JSON.stringify({user: j.user, team: {}})); }catch(e){}
    say('Готово, открываю CRM…', 'ok');
    setTimeout(function(){ location.replace('/crm'); }, 300);
  }
  function accept(proof){
    say('Проверяю…');
    proof.token = T;
    post('/api/v1/manager/accept', proof).then(function(r){
      if(r.s === 200) return crm(r.j);
      say((r.j && r.j.detail) || ('Не получилось: ' + ((r.j && r.j.error) || r.s)), 'err');
    }, function(){ say('Нет связи с сервером. Проверьте интернет', 'err'); });
  }
  window.BSX_onTelegram = function(u){ accept({widget: u}); };
  function esc(s){ return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function login(){
    var tgr = (location.hash.match(/tgAuthResult=([^&]+)/) || [])[1];
    if(tgr){
      try{
        var b64 = tgr.replace(/-/g, '+').replace(/_/g, '/'); while(b64.length % 4) b64 += '=';
        var u = JSON.parse(decodeURIComponent(escape(atob(b64))));
        history.replaceState(null, '', location.pathname);
        if(u && u.id && u.hash) return accept({widget: u});
      }catch(e){}
    }
    if(/tgWebAppData=/.test(location.hash)){
      var s0 = document.createElement('script'); s0.src = 'https://telegram.org/js/telegram-web-app.js';
      s0.onload = function(){ var wa = window.Telegram && window.Telegram.WebApp; if(wa && wa.initData){ try{ wa.ready(); }catch(e){} accept({initData: wa.initData}); } };
      document.head.appendChild(s0); return;
    }
    fetch('/api/v1/platform/config').then(function(r){ return r.json(); }).then(function(c){
      if(!c.telegram || !c.id){ say('Вход через Telegram ещё не настроен на сервере', 'err'); return; }
      var a = document.createElement('a'); a.className = 'btn tg';
      a.href = 'https://oauth.telegram.org/auth?bot_id=' + encodeURIComponent(c.id) + '&origin=' + encodeURIComponent(location.origin) +
        '&request_access=write&lang=ru&return_to=' + encodeURIComponent(location.origin + location.pathname);
      a.innerHTML = TG_IC + 'Войти через Telegram'; W.appendChild(a);
    }).catch(function(){ say('Сервер недоступен. Обновите страницу', 'err'); });
  }
  fetch('/api/v1/manager/invite-info?t=' + encodeURIComponent(T), {credentials: 'same-origin'})
    .then(function(r){ return r.json().then(function(j){ return {s: r.status, j: j}; }); })
    .then(function(r){
      if(r.s !== 200){
        $('t').textContent = 'Приглашение недействительно';
        $('s').textContent = r.s === 429 ? 'Слишком много попыток. Подождите 10 минут' : 'Ссылка истекла (она действует 72 часа), уже принята или отозвана. Попросите у владельца клуба новую.';
        return;
      }
      var j = r.j, exp = j.expiresAt ? new Date(j.expiresAt) : null;
      $('t').textContent = (j.name ? j.name + ', вас' : 'Вас') + ' приглашают в CRM';
      $('s').textContent = 'Business Surgery: рабочее место менеджера по продажам. Ваши лиды, план дня, скрипты и запись на экспресс-разбор. Войдите своим Telegram: бот будет присылать напоминания о звонках.';
      $('f').innerHTML = '<div><span>Роль</span><b>Менеджер по продажам</b></div>' +
        (exp ? '<div><span>Ссылка действует до</span><b>' + esc(exp.toLocaleString('ru-RU', {day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit'})) + '</b></div>' : '') +
        '<div><span>Ссылка</span><b>одноразовая</b></div>';
      $('f').hidden = false;
      login();
    }, function(){ say('Нет связи с сервером. Проверьте интернет', 'err'); });
})();
</script>
</body>
</html>
`

var managerLinkHTML = plainPageHead + `<title>Вход в CRM · Business Surgery</title>
</head>
<body>
<main><div class="card">
  <div class="brand"><img src="/site/logo-white.png" alt="" width="34" height="16">Business Surgery</div>
  <h1>Вход в CRM</h1>
  <p>Личная ссылка менеджера. Не пересылайте её никому.</p>
  <div class="w" id="w"><button class="btn" id="go" type="button">Войти</button></div>
  <div class="msg" id="m" role="status" aria-live="polite"></div>
  ` + plainPageFoot + `
</div></main>
<script>
` + plainPageJS + `
(function(){
  var busy = false;
  function go(){
    if(busy) return; busy = true; say('Вхожу…');
    post('/api/v1/manager/link', {token: token()}).then(function(r){
      busy = false;
      if(r.s === 200){
        try{ localStorage.setItem('bsx_token', r.j.token); localStorage.setItem('bsx_user', JSON.stringify({user: r.j.user, team: {}})); }catch(e){}
        say('Готово, открываю CRM…', 'ok');
        return setTimeout(function(){ location.replace('/crm'); }, 300);
      }
      say((r.j && r.j.detail) || ('Не получилось: ' + ((r.j && r.j.error) || r.s)), 'err');
    }, function(){ busy = false; say('Нет связи с сервером. Проверьте интернет', 'err'); });
  }
  $('go').onclick = go;
  go();
})();
</script>
</body>
</html>
`

// The manager's CRM: mobile first, the same black and white as the platform.
var managerAppHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="robots" content="noindex,nofollow">
<meta name="theme-color" content="#050505">
<link rel="manifest" href="/manifest.webmanifest">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">
<meta name="apple-mobile-web-app-title" content="BS CRM">
<link rel="apple-touch-icon" href="/pwa/apple-180.png">
<link rel="icon" type="image/png" href="/pwa/icon-192.png">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Manrope:wght@400;600;700;800&display=swap" rel="stylesheet" media="print" onload="this.media='all'">
<title>CRM менеджера · Business Surgery</title>
<style>
:root{--bg:#050505;--card:#0F0F10;--card2:#161618;--line:rgba(255,255,255,.12);--line2:rgba(255,255,255,.2);--tx:#fff;--t2:#C9C9C9;--t3:#8D8D8D;--ok:#3DD68C;--err:#FF6B6F;--warn:#E8C547;--gold:#D4B886;--nb:calc(64px + env(safe-area-inset-bottom))}
*{box-sizing:border-box;margin:0;padding:0;-webkit-tap-highlight-color:transparent}
html,body{background:var(--bg);color:var(--tx);font:15px/1.45 Manrope,-apple-system,'Segoe UI',Arial,sans-serif;-webkit-font-smoothing:antialiased}
button,input,select,textarea{font:inherit;color:inherit}
input,select,textarea{font-size:16px}
a{color:inherit}
.hd{position:sticky;top:0;z-index:20;background:rgba(5,5,5,.94);-webkit-backdrop-filter:blur(12px);backdrop-filter:blur(12px);border-bottom:1px solid var(--line);
  padding:calc(10px + env(safe-area-inset-top)) 16px 10px;display:flex;align-items:center;gap:12px}
.hd img{width:30px;height:30px;border-radius:8px}
.hd .tt{flex:1;min-width:0}
.hd .tt b{display:block;font-size:16px;font-weight:800;letter-spacing:-.01em}
.hd .tt span{display:block;font-size:12px;color:var(--t3);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.ib{border:1px solid var(--line);background:none;border-radius:999px;min-height:40px;padding:0 14px;font-weight:700;font-size:13px;cursor:pointer}
main{padding:14px 16px calc(var(--nb) + 24px);max-width:760px;margin:0 auto}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--t3);font-weight:800;margin:18px 2px 8px}
h2:first-child{margin-top:4px}
.empty{color:var(--t3);font-size:14px;padding:16px 4px}
.lc{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:13px 14px;margin-bottom:10px;cursor:pointer}
.lc.ov{border-color:rgba(255,107,111,.55)}
.lc .r1{display:flex;align-items:center;gap:8px}
.lc .nm{font-weight:800;font-size:16px;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pill{font-size:11.5px;font-weight:800;border:1px solid var(--line2);border-radius:999px;padding:3px 9px;white-space:nowrap;color:var(--t2)}
.pill.red{border-color:rgba(255,107,111,.6);color:var(--err)}
.pill.gold{border-color:rgba(212,184,134,.6);color:var(--gold)}
.lc .nx{margin-top:6px;font-size:14px;color:var(--t2)}
.lc .nx b{color:var(--tx)}
.lc .mt{margin-top:4px;font-size:12.5px;color:var(--t3)}
.qa{display:flex;gap:8px;margin-top:10px}
.qa a,.qa button{flex:1;display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:42px;border-radius:12px;border:1px solid var(--line);background:var(--card2);font-weight:700;font-size:13.5px;text-decoration:none;cursor:pointer}
.qa .pri{background:#fff;color:#050505;border-color:#fff}
.nb{position:fixed;left:0;right:0;bottom:0;z-index:30;height:var(--nb);padding:0 6px env(safe-area-inset-bottom);display:flex;background:rgba(5,5,5,.97);border-top:1px solid var(--line);-webkit-backdrop-filter:blur(12px);backdrop-filter:blur(12px)}
.nb button{all:unset;flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:3px;color:var(--t3);font-size:11px;font-weight:800;cursor:pointer;position:relative}
.nb button svg{width:22px;height:22px}
.nb button.on{color:#fff}
.nb button i{position:absolute;top:8px;left:calc(50% + 6px);font-style:normal;background:#fff;color:#050505;border-radius:999px;font-size:10px;padding:1px 5px}
.fab{position:fixed;right:16px;bottom:calc(var(--nb) + 16px);z-index:25;width:56px;height:56px;border-radius:50%;border:0;background:#fff;color:#050505;font-size:28px;font-weight:300;cursor:pointer;box-shadow:0 8px 24px rgba(0,0,0,.5)}
.chips{display:flex;gap:6px;overflow-x:auto;padding-bottom:4px;margin:0 -16px 8px;padding-left:16px;padding-right:16px;scrollbar-width:none}
.chips::-webkit-scrollbar{display:none}
.chip{border:1px solid var(--line);border-radius:999px;padding:7px 12px;font-size:13px;font-weight:700;white-space:nowrap;cursor:pointer;background:none}
.chip.on{background:#fff;color:#050505;border-color:#fff}
.inp{width:100%;background:var(--card2);border:1px solid var(--line);border-radius:12px;padding:12px 13px;outline:none}
.inp:focus{border-color:rgba(255,255,255,.45)}
textarea.inp{resize:vertical;min-height:84px}
.sh{position:fixed;inset:0;z-index:50;background:var(--bg);display:none;flex-direction:column}
.sh.on{display:flex}
.sh .shh{padding:calc(10px + env(safe-area-inset-top)) 16px 10px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:10px}
.sh .shh b{flex:1;min-width:0;font-size:17px;font-weight:800;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sh .shb{flex:1;overflow-y:auto;-webkit-overflow-scrolling:touch;padding:14px 16px calc(28px + env(safe-area-inset-bottom));max-width:760px;width:100%;margin:0 auto}
.x{border:1px solid var(--line);background:none;border-radius:50%;width:40px;height:40px;font-size:18px;cursor:pointer}
.lbl{display:block;font-size:12px;font-weight:800;color:var(--t3);letter-spacing:.04em;text-transform:uppercase;margin:14px 2px 6px}
.row2{display:grid;grid-template-columns:1.4fr 1fr;gap:8px}
.btn{display:inline-flex;align-items:center;justify-content:center;width:100%;min-height:50px;border-radius:999px;border:0;background:#fff;color:#050505;font-weight:800;font-size:15.5px;cursor:pointer;margin-top:14px}
.btn.sec{background:none;color:#fff;border:1px solid var(--line2)}
.btn:disabled{opacity:.5}
.err{color:var(--err);font-size:14px;margin-top:10px;min-height:18px;white-space:pre-line}
.box{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px;margin-top:12px}
.lg{font-size:13.5px;color:var(--t2);border-left:2px solid var(--line2);padding:2px 0 2px 10px;margin:8px 0}
.lg i{display:block;font-style:normal;font-size:11.5px;color:var(--t3)}
.slot{display:flex;justify-content:space-between;align-items:center;gap:10px;border:1px solid var(--line);border-radius:12px;padding:11px 12px;margin-top:8px;cursor:pointer}
.slot:active{border-color:#fff}
.msgs{display:flex;flex-direction:column;gap:6px;max-height:46vh;overflow-y:auto;margin-top:8px}
.bub{max-width:84%;padding:8px 11px;border-radius:14px;font-size:14px;white-space:pre-wrap;word-wrap:break-word}
.bub.in{align-self:flex-start;background:var(--card2);border:1px solid var(--line)}
.bub.out{align-self:flex-end;background:#fff;color:#050505}
.bub i{display:block;font-style:normal;font-size:10.5px;opacity:.6;margin-top:2px}
.tiles{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.tile{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px}
.tile b{display:block;font-size:28px;font-weight:800;letter-spacing:-.02em}
.tile span{font-size:12.5px;color:var(--t3)}
.sc{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px;margin-bottom:10px}
.sc b{font-size:15.5px}
.sc p{color:var(--t3);font-size:13px;margin-top:2px}
.sc ol{margin:10px 0 0 18px;color:var(--t2);font-size:14px}
.sc .ms{margin-top:10px;background:var(--card2);border-radius:12px;padding:10px 12px;font-size:14px;color:var(--t2)}
.toast{position:fixed;left:50%;bottom:calc(var(--nb) + 18px);transform:translateX(-50%);z-index:90;background:#fff;color:#050505;border-radius:999px;padding:10px 16px;font-weight:700;font-size:14px;max-width:calc(100% - 32px);text-align:center;opacity:0;pointer-events:none;transition:opacity .2s}
.toast.on{opacity:1}
.auth{min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:24px 16px;text-align:center;gap:14px}
.auth img{width:64px;height:64px;border-radius:16px}
.auth h1{font-size:22px}
.auth p{color:var(--t2);max-width:360px}
.auth .btn{max-width:360px;background:#2AABEE;color:#fff}
.srch{margin-bottom:10px}
.hint{font-size:13px;color:var(--t3);margin-top:6px}
@media(min-width:900px){main{padding-top:20px}}
</style>
</head>
<body>
<div id="app"></div>
<div class="toast" id="toast" role="status" aria-live="polite"></div>
<script src="/crm/scripts.js" defer></script>
<script>
(function(){
  var API = '/api/v1/manager', S = {me:null, cols:[], reasons:[], sources:[], wa:false, mine:[], pool:[], tab:'plan', f:'act', q:'', lead:null, slots:null, chat:null, today:''};
  var $ = function(id){ return document.getElementById(id); };
  var esc = function(s){ return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); };
  var tok = function(){ try{ return localStorage.getItem('bsx_token') || ''; }catch(e){ return ''; } };
  var toastT = 0;
  var toast = function(t){ var el = $('toast'); el.textContent = t; el.classList.add('on'); clearTimeout(toastT); toastT = setTimeout(function(){ el.classList.remove('on'); }, 2600); };
  var api = function(m, p, body){
    return fetch(API + p, {method: m, credentials: 'same-origin', headers: {'Authorization': 'Bearer ' + tok(), 'Content-Type': 'application/json'}, body: body ? JSON.stringify(body) : undefined})
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(j){
        if(r.status === 401){ authScreen(j.detail || ''); throw new Error('auth'); }
        if(r.status === 403 && j.reason === 'sales_only'){ authScreen('Эта страница для менеджера по продажам. Войдите его Telegram.'); throw new Error('auth'); }
        return {s: r.status, j: j};
      }); });
  };
  var digits = function(p){ var d = String(p || '').replace(/\D/g, ''); if(d.length === 11 && d[0] === '8') d = '7' + d.slice(1); if(d.length === 10) d = '7' + d; return d; };
  var phone = function(p){ var d = digits(p); if(!d) return ''; if(d.length === 11 && d[0] === '7') return '+7 ' + d.slice(1, 4) + ' ' + d.slice(4, 7) + ' ' + d.slice(7, 9) + ' ' + d.slice(9); return '+' + d; };
  var colName = function(id){ for(var i = 0; i < S.cols.length; i++) if(S.cols[i].id === id) return S.cols[i].n; return id || 'Новый'; };
  var closed = function(c){ return c === 'won' || c === 'lost'; };
  var dayRu = function(d){
    if(!d) return '';
    if(d === S.today) return 'сегодня';
    var t = new Date(S.today + 'T00:00:00'), x = new Date(d + 'T00:00:00'), n = Math.round((x - t) / 86400000);
    if(n === 1) return 'завтра';
    if(n === -1) return 'вчера';
    if(n < 0) return 'просрочено ' + (-n) + ' дн';
    return x.toLocaleDateString('ru-RU', {day: 'numeric', month: 'short'});
  };
  var money = function(n){ n = String(n || '').replace(/\D/g, ''); return n ? n.replace(/\B(?=(\d{3})+(?!\d))/g, ' ') + ' ₸' : ''; };
  var IC = {
    plan: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><rect x="4" y="5" width="16" height="15" rx="3"/><path d="M8 3v4M16 3v4M4 10h16M8 14h4"/></svg>',
    mine: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="9" cy="8" r="3.2"/><path d="M3.5 19c.8-3 3-4.6 5.5-4.6s4.7 1.6 5.5 4.6M16 6.5h5M16 10.5h5M17 14.5h4"/></svg>',
    pool: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M4 13l2.5-7h11L20 13v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z"/><path d="M4 13h4.5l1.5 2.5h4l1.5-2.5H20"/></svg>',
    scr: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 3h9l4 4v14H6z"/><path d="M9 11h7M9 15h7M9 7h3"/></svg>',
    st: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></svg>'
  };

  // ── вход ──
  function authScreen(why){
    try{ localStorage.removeItem('bsx_token'); }catch(e){}
    $('app').innerHTML = '<div class="auth"><img src="/pwa/icon-192.png" alt=""><h1>CRM менеджера</h1>' +
      '<p>Business Surgery. Войдите своим Telegram: тем же, с которым принимали приглашение.</p>' +
      (why ? '<p style="color:var(--err)">' + esc(why) + '</p>' : '') +
      '<div id="aw" style="width:100%;display:flex;justify-content:center"></div><p class="hint" id="am"></p></div>';
    var m = $('am');
    var login = function(proof){
      m.textContent = 'Проверяю…';
      fetch(API + '/login', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(proof)})
        .then(function(r){ return r.json().then(function(j){ return {s: r.status, j: j}; }); })
        .then(function(r){
          if(r.s === 200){ try{ localStorage.setItem('bsx_token', r.j.token); }catch(e){} location.replace('/crm'); return; }
          m.textContent = (r.j && r.j.detail) || 'Не получилось войти';
        }, function(){ m.textContent = 'Нет связи с сервером'; });
    };
    var tgr = (location.hash.match(/tgAuthResult=([^&]+)/) || [])[1];
    if(tgr){
      try{
        var b64 = tgr.replace(/-/g, '+').replace(/_/g, '/'); while(b64.length % 4) b64 += '=';
        var u = JSON.parse(decodeURIComponent(escape(atob(b64))));
        history.replaceState(null, '', location.pathname);
        if(u && u.id && u.hash){ login({widget: u}); return; }
      }catch(e){}
    }
    if(/tgWebAppData=/.test(location.hash)){
      var s0 = document.createElement('script'); s0.src = 'https://telegram.org/js/telegram-web-app.js';
      s0.onload = function(){ var wa = window.Telegram && window.Telegram.WebApp; if(wa && wa.initData){ try{ wa.ready(); wa.expand(); }catch(e){} login({initData: wa.initData}); } };
      document.head.appendChild(s0); return;
    }
    fetch('/api/v1/platform/config').then(function(r){ return r.json(); }).then(function(c){
      if(!c || !c.id){ m.textContent = 'Вход через Telegram ещё не настроен'; return; }
      var a = document.createElement('a'); a.className = 'btn';
      a.href = 'https://oauth.telegram.org/auth?bot_id=' + encodeURIComponent(c.id) + '&origin=' + encodeURIComponent(location.origin) +
        '&request_access=write&lang=ru&return_to=' + encodeURIComponent(location.origin + '/crm');
      a.textContent = 'Войти через Telegram'; $('aw').appendChild(a);
    }).catch(function(){ m.textContent = 'Сервер недоступен'; });
  }
  function logout(){
    fetch('/api/v1/platform/auth/logout', {method: 'POST', credentials: 'same-origin', headers: {'Authorization': 'Bearer ' + tok()}}).catch(function(){}).then(function(){
      try{ localStorage.removeItem('bsx_token'); localStorage.removeItem('bsx_user'); }catch(e){}
      authScreen('');
    });
  }

  // ── каркас ──
  function shell(){
    $('app').innerHTML = '<header class="hd"><img src="/pwa/icon-192.png" alt=""><div class="tt"><b>CRM</b><span id="hdS"></span></div>' +
      '<button class="ib" id="outB" type="button">Выйти</button></header><main id="mn"></main>' +
      '<button class="fab" id="fab" type="button" aria-label="Новый лид">+</button>' +
      '<nav class="nb" id="nb" aria-label="Разделы">' +
        [['plan', 'План дня'], ['mine', 'Мои лиды'], ['pool', 'Новые'], ['scr', 'Скрипты'], ['st', 'Цифры']].map(function(x){
          return '<button type="button" data-t="' + x[0] + '">' + IC[x[0]] + '<span>' + x[1] + '</span></button>';
        }).join('') + '</nav>' +
      '<section class="sh" id="sh" aria-modal="true" role="dialog"><div class="shh"><b id="shT"></b><button class="x" id="shX" type="button" aria-label="Закрыть">✕</button></div><div class="shb" id="shB"></div></section>';
    $('outB').onclick = logout;
    $('fab').onclick = newLead;
    $('shX').onclick = closeSheet;
    $('nb').onclick = function(e){ var b = e.target.closest('button'); if(!b) return; S.tab = b.getAttribute('data-t'); paint(); window.scrollTo(0, 0); };
  }
  function nbPaint(){
    [].forEach.call($('nb').children, function(b){
      var t = b.getAttribute('data-t'); b.classList.toggle('on', t === S.tab);
      var old = b.querySelector('i'); if(old) old.remove();
      var n = t === 'pool' ? S.pool.length : (t === 'plan' ? planCount() : 0);
      if(n) b.insertAdjacentHTML('beforeend', '<i>' + n + '</i>');
    });
    $('hdS').textContent = (S.me ? S.me.name + ' · ' : '') + S.mine.filter(function(l){ return !closed(l.col); }).length + ' в работе';
  }
  function planCount(){ return S.mine.filter(function(l){ return !closed(l.col) && l.day && l.day <= S.today; }).length; }
  function load(){
    return api('GET', '/leads').then(function(r){
      if(r.s !== 200){ toast('Не загрузилось, обновите'); return; }
      S.mine = r.j.mine || []; S.pool = r.j.pool || []; S.today = r.j.today || S.today;
      paint();
    });
  }
  function sortSteps(a, b){
    var da = a.day || '9999', db = b.day || '9999';
    if(da !== db) return da < db ? -1 : 1;
    return String(a.nextTime || '99').localeCompare(String(b.nextTime || '99'));
  }
  function card(l){
    var d = digits(l.phone), tg = String(l.tg || '').replace(/^@/, '');
    var when = l.day ? dayRu(l.day) + (l.nextTime ? ', ' + l.nextTime : '') : '';
    return '<div class="lc' + (l.overdue ? ' ov' : '') + '" data-id="' + esc(l.id) + '">' +
      '<div class="r1"><div class="nm">' + esc(l.name || phone(l.phone) || 'Без имени') + '</div>' +
        (l.hot ? '<span class="pill gold">горячий</span>' : '') + '<span class="pill' + (l.overdue ? ' red' : '') + '">' + esc(colName(l.col)) + '</span></div>' +
      (l.next ? '<div class="nx">→ <b>' + esc(l.next) + '</b>' + (when ? ' · ' + esc(when) : '') + '</div>' :
        (!closed(l.col) && l.col !== 'later' ? '<div class="nx" style="color:var(--warn)">Шаг не назначен</div>' : '')) +
      '<div class="mt">' + esc([phone(l.phone), l.source, l.niche].filter(Boolean).join(' · ')) + '</div>' +
      (!closed(l.col) && (d || tg) ? '<div class="qa">' +
        (d ? '<a href="tel:+' + d + '" data-touch="call" data-id="' + esc(l.id) + '">📞 Позвонить</a>' : '') +
        (d ? '<a href="https://wa.me/' + d + '" target="_blank" rel="noopener" data-touch="wa" data-id="' + esc(l.id) + '">💬 WhatsApp</a>' : '') +
        (!d && tg ? '<a href="https://t.me/' + esc(tg) + '" target="_blank" rel="noopener" data-touch="tg" data-id="' + esc(l.id) + '">✈️ Telegram</a>' : '') +
      '</div>' : '') +
    '</div>';
  }
  function poolCard(l){
    return '<div class="lc" data-pool="' + esc(l.id) + '"><div class="r1"><div class="nm">' + esc(l.name) + '</div>' + (l.hot ? '<span class="pill gold">горячий</span>' : '') +
      '<span class="pill">' + esc(colName(l.col)) + '</span></div>' +
      '<div class="mt">' + esc([l.source, l.niche, l.date].filter(Boolean).join(' · ')) + (l.refName ? '<br>Рекомендовал: ' + esc(l.refName) : '') + '</div>' +
      '<div class="mt">' + (l.hasPhone ? 'Есть телефон' : 'Без телефона') + (l.hasTg ? ' · есть Telegram' : '') + '</div>' +
      '<div class="qa"><button class="pri" type="button" data-take="' + esc(l.id) + '">Взять в работу</button></div></div>';
  }
  function paint(){
    nbPaint();
    var m = $('mn'), h = '';
    if(S.tab === 'plan'){
      var act = S.mine.filter(function(l){ return !closed(l.col); });
      var over = act.filter(function(l){ return l.overdue; }).sort(sortSteps);
      var today = act.filter(function(l){ return !l.overdue && l.day === S.today; }).sort(sortSteps);
      var none = act.filter(function(l){ return l.col !== 'later' && (!l.next || !l.day); });
      var soon = act.filter(function(l){ return l.day && l.day > S.today; }).sort(sortSteps).slice(0, 8);
      h += '<h2>Просрочено · ' + over.length + '</h2>' + (over.length ? over.map(card).join('') : '<div class="empty">Нет просроченных шагов</div>');
      h += '<h2>Сегодня · ' + today.length + '</h2>' + (today.length ? today.map(card).join('') : '<div class="empty">На сегодня шагов нет. Возьмите новых лидов во вкладке «Новые»</div>');
      if(none.length) h += '<h2>Без следующего шага · ' + none.length + '</h2>' + none.map(card).join('');
      if(soon.length) h += '<h2>Дальше</h2>' + soon.map(card).join('');
    } else if(S.tab === 'mine'){
      var F = [['act', 'В работе'], ['meet', 'Разбор'], ['later', 'Отложено'], ['won', 'Резиденты'], ['lost', 'Отказы']];
      h += '<input class="inp srch" id="q" placeholder="Поиск: имя, телефон, ниша" value="' + esc(S.q) + '">' +
        '<div class="chips">' + F.map(function(x){ return '<button type="button" class="chip' + (S.f === x[0] ? ' on' : '') + '" data-f="' + x[0] + '">' + x[1] + '</button>'; }).join('') + '</div>';
      var q = S.q.trim().toLowerCase(), qd = q.replace(/\D/g, '');
      var list = S.mine.filter(function(l){
        if(S.f === 'act' ? (closed(l.col) || l.col === 'later') : l.col !== S.f) return false;
        if(!q) return true;
        return String(l.name || '').toLowerCase().indexOf(q) >= 0 || String(l.niche || '').toLowerCase().indexOf(q) >= 0 || (qd && digits(l.phone).indexOf(qd) >= 0);
      }).sort(sortSteps);
      h += '<div id="ml">' + (list.length ? list.map(card).join('') : '<div class="empty">Здесь пока пусто</div>') + '</div>';
    } else if(S.tab === 'pool'){
      h += '<h2>Новые лиды без менеджера · ' + S.pool.length + '</h2><p class="hint" style="margin:0 2px 10px">Телефон откроется, когда возьмёте лида в работу.</p>' +
        (S.pool.length ? S.pool.map(poolCard).join('') : '<div class="empty">Новых лидов нет. Когда придёт заявка, она появится здесь</div>');
    } else if(S.tab === 'scr'){
      h += '<h2>Скрипты продаж</h2><div id="scl"><div class="empty">Загружаю…</div></div>';
      setTimeout(scripts, 0);
    } else if(S.tab === 'st'){
      h += '<h2>Мои цифры за 30 дней</h2><div id="stb"><div class="empty">Считаю…</div></div>';
      setTimeout(stats, 0);
    }
    m.innerHTML = h;
    $('fab').style.display = (S.tab === 'plan' || S.tab === 'mine') ? '' : 'none';
    var qi = $('q'); if(qi) qi.oninput = function(){ S.q = qi.value; var keep = qi.selectionStart; paint(); var n = $('q'); if(n){ n.focus(); try{ n.setSelectionRange(keep, keep); }catch(e){} } };
  }
  document.addEventListener('click', function(e){
    var t = e.target.closest('[data-touch]');
    if(t){ api('POST', '/leads/' + encodeURIComponent(t.getAttribute('data-id')) + '/touch', {kind: t.getAttribute('data-touch')}).then(function(r){ if(r.s === 200) upd(r.j.lead); }).catch(function(){}); return; }
    var tk = e.target.closest('[data-take]');
    if(tk){ take(tk.getAttribute('data-take')); return; }
    var f = e.target.closest('[data-f]');
    if(f){ S.f = f.getAttribute('data-f'); paint(); return; }
    var c = e.target.closest('.lc[data-id]');
    if(c && !e.target.closest('a,button')){ openLead(c.getAttribute('data-id')); return; }
    var cp = e.target.closest('[data-copy]');
    if(cp){ copy(cp.getAttribute('data-copy')); }
  });
  function upd(l){
    if(!l) return;
    var i = S.mine.findIndex(function(x){ return x.id === l.id; });
    var lite = Object.assign({}, l); delete lite.log;
    if(i >= 0) S.mine[i] = lite; else S.mine.unshift(lite);
    S.pool = S.pool.filter(function(x){ return x.id !== l.id; });
    if(S.lead && S.lead.id === l.id) S.lead = l;
  }
  function take(id){
    api('POST', '/leads/' + encodeURIComponent(id) + '/take').then(function(r){
      if(r.s !== 200){ toast((r.j && r.j.detail) || 'Не получилось'); load(); return; }
      upd(r.j.lead); toast('Лид ваш. Назначьте следующий шаг'); paint(); openLead(id);
    });
  }
  function copy(t){
    var done = function(){ toast('Скопировано'); };
    if(navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(t).then(done, function(){ toast('Выделите текст и скопируйте'); });
    else toast('Выделите текст и скопируйте');
  }

  // ── карточка лида ──
  function openSheet(title, html){ $('shT').textContent = title; $('shB').innerHTML = html; $('sh').classList.add('on'); $('shB').scrollTop = 0; }
  function closeSheet(){ $('sh').classList.remove('on'); S.lead = null; S.chat = null; if(/^#lead=/.test(location.hash)) history.replaceState(null, '', '/crm'); paint(); }
  function openLead(id){
    api('GET', '/leads/' + encodeURIComponent(id)).then(function(r){
      if(r.s !== 200){ toast((r.j && r.j.detail) || 'Лид не найден'); return; }
      var l = r.j.lead;
      if(l.pool){ openSheet(l.name, poolCard(l)); return; }
      S.lead = l; leadSheet();
      if(S.wa && digits(l.phone)) chatLoad();
    });
  }
  function opt(v, cur, n){ return '<option value="' + esc(v) + '"' + (v === cur ? ' selected' : '') + '>' + esc(n) + '</option>'; }
  function leadSheet(){
    var l = S.lead, d = digits(l.phone), tg = String(l.tg || '').replace(/^@/, '');
    var h = '<div class="mt" style="margin:0 2px">' + esc([colName(l.col), l.source, l.date ? 'с ' + l.date : '', l.refName ? 'рекомендовал ' + l.refName : ''].filter(Boolean).join(' · ')) + '</div>';
    h += '<div class="qa">' + (d ? '<a class="pri" href="tel:+' + d + '" data-touch="call" data-id="' + esc(l.id) + '">📞 Позвонить</a>' : '') +
      (d ? '<a href="https://wa.me/' + d + '" target="_blank" rel="noopener" data-touch="wa" data-id="' + esc(l.id) + '">💬 WhatsApp</a>' : '') +
      (tg ? '<a href="https://t.me/' + esc(tg) + '" target="_blank" rel="noopener" data-touch="tg" data-id="' + esc(l.id) + '">✈️ Telegram</a>' : '') + '</div>';
    if(d) h += '<div class="qa"><button type="button" data-touch="nocall" data-id="' + esc(l.id) + '">📵 Не дозвонился</button></div>';
    h += '<label class="lbl" for="fCol">Этап</label><select class="inp" id="fCol">' + S.cols.map(function(c){ return opt(c.id, l.col, c.n); }).join('') + '</select>';
    h += '<div id="fLostW"><label class="lbl" for="fLost">Причина отказа (обязательно)</label><select class="inp" id="fLost"><option value="">Выберите причину</option>' +
      S.reasons.map(function(x){ return opt(x, l.lostReason || '', x); }).join('') + '</select></div>';
    h += '<div id="fSumW"><label class="lbl" for="fSum">Сумма сделки, ₸</label><input class="inp" id="fSum" inputmode="numeric" value="' + esc(l.sum ? money(l.sum).replace(' ₸', '') : '') + '" placeholder="0"></div>';
    h += '<div id="fNextW"><label class="lbl" for="fNext" id="fNextL">Следующий шаг</label><input class="inp" id="fNext" value="' + esc(l.next || '') + '" placeholder="Позвонить, отправить кейс, напомнить о разборе">' +
      '<div class="row2" style="margin-top:8px"><input class="inp" id="fAt" type="date" value="' + esc(l.day || '') + '" aria-label="Дата шага"><input class="inp" id="fTm" type="time" value="' + esc(l.nextTime || '') + '" aria-label="Время шага"></div>' +
      '<div class="hint">Со временем бот напомнит за 10 минут.</div></div>';
    h += '<label class="lbl" for="fNote">Заметка к лиду</label><textarea class="inp" id="fNote" rows="3" placeholder="Что узнали: боль, бюджет, когда удобно"></textarea>';
    h += '<div class="err" id="fErr"></div><button class="btn" id="fSave" type="button">Сохранить</button>';
    h += '<details class="box"><summary style="cursor:pointer;font-weight:800">Контакты и ниша</summary>' +
      '<label class="lbl" for="fName">Имя</label><input class="inp" id="fName" value="' + esc(l.name || '') + '">' +
      '<label class="lbl" for="fPh">Телефон</label><input class="inp" id="fPh" type="tel" value="' + esc(l.phone || '') + '" placeholder="+7 700 000 00 00">' +
      '<label class="lbl" for="fTg">Telegram</label><input class="inp" id="fTg" value="' + esc(l.tg || '') + '" placeholder="@username">' +
      '<label class="lbl" for="fNi">Ниша</label><input class="inp" id="fNi" value="' + esc(l.niche || '') + '">' +
      '<button class="btn sec" id="fSave2" type="button">Сохранить контакты</button></details>';
    if(!closed(l.col)) h += '<div class="box"><b>Экспресс-разбор</b>' + (l.razborAt ? '<div class="hint">Записан: ' + esc(new Date(l.razborAt).toLocaleString('ru-RU', {weekday: 'short', day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit'})) + '</div>' : '') +
      '<div id="slw"><button class="btn sec" id="slB" type="button">' + (l.razborAt ? 'Перенести на другое время' : 'Записать на экспресс-разбор') + '</button></div></div>';
    if(d) h += '<div class="box"><b>WhatsApp</b><div id="chw">' + (S.wa ? '<div class="hint">Загружаю переписку…</div>' : '<div class="hint">WhatsApp клуба ещё не подключён к CRM: пишите кнопкой «WhatsApp» выше, отметка о касании сохранится.</div>') + '</div></div>';
    h += '<div class="box"><b>История</b>' + ((l.log || []).length ? l.log.map(function(x){ return '<div class="lg">' + esc(x.text) + '<i>' + esc(x.at ? new Date(x.at).toLocaleString('ru-RU', {day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'}) : '') + (x.by ? ' · ' + esc(x.by) : '') + '</i></div>'; }).join('') : '<div class="hint">Пока пусто</div>') + '</div>';
    openSheet(l.name || phone(l.phone) || 'Лид', h);
    location.hash = 'lead=' + l.id;
    var vis = function(){
      var c = $('fCol').value;
      $('fLostW').style.display = c === 'lost' ? '' : 'none';
      $('fSumW').style.display = (c === 'won' || c === 'decide' || c === 'diag') ? '' : 'none';
      $('fNextW').style.display = closed(c) ? 'none' : '';
      $('fNextL').textContent = c === 'later' ? 'Когда вернуться к лиду' : 'Следующий шаг (обязательно)';
    };
    $('fCol').onchange = vis; vis();
    $('fSave').onclick = save;
    $('fSave2').onclick = function(){ send({name: $('fName').value, phone: $('fPh').value, tg: $('fTg').value, niche: $('fNi').value}, 'Контакты сохранены'); };
    var sb = $('slB'); if(sb) sb.onclick = slots;
  }
  function save(){
    var c = $('fCol').value, p = {col: c, note: $('fNote').value};
    if(c === 'lost') p.lostReason = $('fLost').value;
    if(c === 'won' || c === 'decide' || c === 'diag') p.sum = $('fSum').value;
    if(!closed(c)){ p.next = $('fNext').value; p.nextAt = $('fAt').value; p.nextTime = $('fTm').value; if(c === 'later' && !p.next) p.next = 'Вернуться к лиду'; }
    if(c === 'lost' && !p.lostReason){ $('fErr').textContent = 'Отказ: выберите причину'; return; }
    if(c === 'won' && !String(p.sum || '').replace(/\D/g, '')){ $('fErr').textContent = 'Резидент: укажите сумму сделки'; return; }
    if(!closed(c) && c !== 'new' && (!String(p.next || '').trim() || !p.nextAt)){ $('fErr').textContent = c === 'later' ? 'Укажите дату, когда вернуться к лиду' : 'Укажите следующий шаг и дату'; return; }
    send(p, 'Сохранено');
  }
  function send(p, ok){
    var b = $('fSave'); if(b) b.disabled = true; $('fErr').textContent = '';
    api('POST', '/leads/' + encodeURIComponent(S.lead.id), p).then(function(r){
      if(b) b.disabled = false;
      if(r.s !== 200){ $('fErr').textContent = (r.j && r.j.detail) || 'Не сохранилось'; return; }
      upd(r.j.lead); toast(ok); leadSheet(); if(S.wa && digits(S.lead.phone)) chatLoad();
    }, function(){ if(b) b.disabled = false; });
  }
  function slots(){
    var w = $('slw'); w.innerHTML = '<div class="hint">Загружаю свободное время…</div>';
    api('GET', '/slots').then(function(r){
      var list = (r.j && r.j.slots) || [];
      if(!list.length){ w.innerHTML = '<div class="hint">Свободного времени на 3 недели вперёд нет. Напишите владельцу, он откроет слоты на платформе.</div>'; return; }
      w.innerHTML = '<div class="hint">Выберите время (' + money(r.j.price) + ', 60 минут с основателями):</div>' + list.map(function(s){
        return '<div class="slot" data-slot="' + esc(s.id) + '"><b>' + esc(s.when) + '</b><span class="pill">' + esc(s.format || '') + '</span></div>';
      }).join('');
      [].forEach.call(w.querySelectorAll('[data-slot]'), function(el){
        el.onclick = function(){
          el.style.opacity = '.5';
          api('POST', '/leads/' + encodeURIComponent(S.lead.id) + '/book', {slotId: el.getAttribute('data-slot')}).then(function(x){
            if(x.s !== 200){ toast((x.j && x.j.detail) || 'Не получилось'); el.style.opacity = ''; return; }
            upd(x.j.lead); toast('Записан: ' + x.j.when); leadSheet();
          });
        };
      });
    });
  }
  function chatLoad(){
    var l = S.lead; if(!l) return;
    api('GET', '/leads/' + encodeURIComponent(l.id) + '/wa').then(function(r){
      var w = $('chw'); if(!w || !S.lead || S.lead.id !== l.id) return;
      var list = (r.j && r.j.messages) || [];
      var tpl = (window.SCRIPTS_CUR || []).filter(function(s){ return s && s.msg; }).slice(0, 8);
      w.innerHTML = '<div class="msgs" id="msgs">' + (list.length ? list.map(function(m){
        return '<div class="bub ' + (m.dir === 'out' ? 'out' : 'in') + '">' + esc(m.text) + '<i>' + esc(m.at ? new Date(m.at).toLocaleString('ru-RU', {day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'}) : '') + '</i></div>';
      }).join('') : '<div class="hint">Переписки ещё нет</div>') + '</div>' +
        (tpl.length ? '<div class="chips" style="margin-top:8px">' + tpl.map(function(s, i){ return '<button type="button" class="chip" data-tpl="' + i + '">' + esc(s.t) + '</button>'; }).join('') + '</div>' : '') +
        '<textarea class="inp" id="waT" rows="3" placeholder="Сообщение в WhatsApp" style="margin-top:8px"></textarea><button class="btn" id="waS" type="button">Отправить</button>';
      var ms = $('msgs'); if(ms) ms.scrollTop = ms.scrollHeight;
      [].forEach.call(w.querySelectorAll('[data-tpl]'), function(b){ b.onclick = function(){ $('waT').value = String(tpl[+b.getAttribute('data-tpl')].msg).replace('[Имя]', (S.lead.name || '').split(' ')[0]); }; });
      $('waS').onclick = function(){
        var t = $('waT').value.trim(); if(!t) return;
        $('waS').disabled = true;
        api('POST', '/leads/' + encodeURIComponent(l.id) + '/wa', {text: t}).then(function(x){
          $('waS').disabled = false;
          if(!x.j || !x.j.ok){ toast((x.j && x.j.detail) || 'Не отправилось'); return; }
          toast('Отправлено'); chatLoad();
        });
      };
    });
  }
  function newLead(){
    openSheet('Новый лид', '<label class="lbl" for="nName">Имя</label><input class="inp" id="nName" placeholder="Как зовут">' +
      '<label class="lbl" for="nPh">Телефон</label><input class="inp" id="nPh" type="tel" placeholder="+7 700 000 00 00">' +
      '<label class="lbl" for="nSrc">Источник</label><select class="inp" id="nSrc">' + S.sources.map(function(s){ return opt(s, '', s); }).join('') + '</select>' +
      '<label class="lbl" for="nNi">Ниша</label><input class="inp" id="nNi" placeholder="Чем занимается">' +
      '<label class="lbl" for="nNx">Следующий шаг</label><input class="inp" id="nNx" placeholder="Первый звонок">' +
      '<div class="row2" style="margin-top:8px"><input class="inp" id="nAt" type="date" value="' + esc(S.today) + '"><input class="inp" id="nTm" type="time"></div>' +
      '<div class="err" id="fErr"></div><button class="btn" id="nGo" type="button">Создать</button>');
    $('nGo').onclick = function(){
      api('POST', '/leads', {name: $('nName').value, phone: $('nPh').value, source: $('nSrc').value, niche: $('nNi').value, next: $('nNx').value || 'Первый контакт', nextAt: $('nAt').value, nextTime: $('nTm').value}).then(function(r){
        if(r.s !== 200){ $('fErr').textContent = (r.j && r.j.detail) || 'Не получилось'; return; }
        upd(r.j.lead); toast('Лид создан'); S.lead = r.j.lead; leadSheet();
      });
    };
  }
  function scripts(){
    api('GET', '/scripts').then(function(r){
      var list = (r.j && r.j.scripts && r.j.scripts.length) ? r.j.scripts : (window.SCRIPTS_DEF || []);
      window.SCRIPTS_CUR = list;
      var w = $('scl'); if(!w) return;
      w.innerHTML = list.length ? list.map(function(s){
        return '<div class="sc"><b>' + esc(s.t) + '</b>' + (s.s ? '<p>' + esc(s.s) + '</p>' : '') +
          ((s.steps || []).length ? '<ol>' + s.steps.map(function(x){ return '<li>' + esc(x) + '</li>'; }).join('') + '</ol>' : '') +
          (s.msg ? '<div class="ms">' + esc(s.msg) + '</div><div class="qa"><button type="button" data-copy="' + esc(s.msg) + '">Скопировать сообщение</button></div>' : '') + '</div>';
      }).join('') : '<div class="empty">Скриптов пока нет</div>';
    });
  }
  function stats(){
    api('GET', '/stats').then(function(r){
      var s = (r.j && r.j.stats) || {}, w = $('stb'); if(!w) return;
      var t = function(n, l){ return '<div class="tile"><b>' + (n || 0) + '</b><span>' + l + '</span></div>'; };
      w.innerHTML = '<div class="tiles">' + t(s.active, 'лидов в работе') + t(s.today, 'шагов сегодня') + t(s.overdue, 'просрочено') + t(s.noStep, 'без шага') +
        t(s.calls7, 'звонков за 7 дней') + t(s.wa7, 'сообщений WhatsApp за 7 дней') + t(s.taken30, 'взято лидов') + t(s.booked30, 'записано на разбор') +
        t(s.won30, 'стали резидентами') + t(s.lost30, 'отказов') + '</div>' +
        '<div class="box">Конверсия в резидента: <b>' + Math.round((s.conv || 0) * 100) + '%</b>' + (s.wonSum30 ? '<br>Сумма сделок: <b>' + money(s.wonSum30) + '</b>' : '') + '</div>';
    });
  }

  // ── старт ──
  if(!tok()){ authScreen(''); return; }
  api('GET', '/me').then(function(r){
    if(r.s !== 200){ authScreen((r.j && r.j.detail) || ''); return; }
    S.me = r.j.manager; S.cols = r.j.cols || []; S.reasons = r.j.lostReasons || []; S.sources = r.j.sources || []; S.wa = !!r.j.wa; S.today = r.j.today || '';
    shell();
    var qs = new URLSearchParams(location.search);
    if(qs.get('v') === 'plan') S.tab = 'plan';
    load().then(function(){
      var m = location.hash.match(/^#lead=([^&]+)/), id = qs.get('lead') || (m && decodeURIComponent(m[1]));
      if(id) openLead(id);
      api('GET', '/scripts').then(function(x){ window.SCRIPTS_CUR = (x.j && x.j.scripts && x.j.scripts.length) ? x.j.scripts : (window.SCRIPTS_DEF || []); }).catch(function(){});
    });
    setInterval(function(){ if(!$('sh').classList.contains('on') && document.visibilityState === 'visible') load().catch(function(){}); }, 60000);
  }).catch(function(){});
  if('serviceWorker' in navigator && location.protocol === 'https:') navigator.serviceWorker.register('/sw.js').catch(function(){});
})();
</script>
</body>
</html>
`

// The cycle planner: «Другое время» of the bot's task. Opened in Telegram
// (initData proves the owner) or in a browser with the platform's session.
var cyclePageHTML = plainPageHead + `<title>Время следующих встреч · Business Surgery</title>
<style>
.card{max-width:560px}
.it{border:1px solid var(--line);border-radius:14px;padding:12px;margin-top:10px}
.it b{display:block;font-size:15px}
.it span{font-size:12.5px;color:var(--mute)}
.it .g{display:grid;grid-template-columns:1.3fr 1fr auto;gap:8px;margin-top:8px;align-items:center}
.it input[type=date],.it input[type=time]{width:100%;background:#151515;border:1px solid var(--line);border-radius:10px;color:#fff;padding:10px;font:16px inherit;font-family:inherit;color-scheme:dark}
.it label{display:flex;align-items:center;gap:6px;font-size:13px;color:var(--soft)}
.it input[type=checkbox]{width:20px;height:20px}
.btn.sec{background:none;color:#fff;border:1px solid var(--line)}
</style>
</head>
<body>
<main><div class="card">
  <div class="brand"><img src="/site/logo-white.png" alt="" width="34" height="16">Business Surgery</div>
  <h1 id="t">Время следующих встреч</h1>
  <p id="s">Загружаю…</p>
  <div id="l"></div>
  <div class="w" id="w"></div>
  <div class="msg" id="m" role="status" aria-live="polite"></div>
</div></main>
<script>
` + plainPageJS + `
(function(){
  var ID = token(), INIT = '', T = null;
  function esc(s){ return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function req(m, p, b){
    return fetch(p, {method: m, credentials: 'same-origin', headers: {'Content-Type': 'application/json', 'X-TG-Init': INIT}, body: b ? JSON.stringify(b) : undefined})
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(j){ return {s: r.status, j: j}; }); });
  }
  function paint(){
    var t = T;
    if(t.status !== 'pending'){
      $('s').textContent = t.status === 'approved' ? 'Уже утверждено' + (t.doneBy ? ' (' + t.doneBy + ')' : '') + '. Изменить встречи можно на платформе в «Расписании».' : 'Задача закрыта: «не нужно».';
      $('l').innerHTML = (t.approved || []).map(function(it){ return '<div class="it"><b>' + esc(it.res) + '</b><span>' + esc(it.date.split('-').reverse().join('.')) + ', ' + esc(it.time) + ' · ' + (it.kind === 'offline' ? 'офлайн' : 'онлайн') + (it.done ? '' : ' · не создана') + '</span></div>'; }).join('');
      $('w').innerHTML = ''; return;
    }
    $('s').textContent = 'После офлайн-разбора ' + t.offDate.split('-').reverse().join('.') + '. Поменяйте дату или время, снимите галочку, если встреча не нужна, и нажмите «Утвердить».';
    $('l').innerHTML = t.items.map(function(it, i){
      return '<div class="it"><b>' + esc(it.res) + '</b><span>' + (it.kind === 'offline' ? 'Офлайн-разбор (группа)' : 'Онлайн, ссылка на созвон платформы') + '</span>' +
        '<div class="g"><input type="date" data-i="' + i + '" data-k="date" value="' + esc(it.date) + '"><input type="time" data-i="' + i + '" data-k="time" value="' + esc(it.time) + '">' +
        '<label><input type="checkbox" data-i="' + i + '" data-k="on" checked>да</label></div></div>';
    }).join('');
    $('w').innerHTML = '<button class="btn" id="ok" type="button">✅ Утвердить</button><button class="btn sec" id="no" type="button">Не нужно</button>';
    $('ok').onclick = function(){
      var items = [];
      T.items.forEach(function(it, i){
        var on = document.querySelector('[data-i="' + i + '"][data-k="on"]').checked; if(!on) return;
        items.push({res: it.res, kind: it.kind, date: document.querySelector('[data-i="' + i + '"][data-k="date"]').value, time: document.querySelector('[data-i="' + i + '"][data-k="time"]').value});
      });
      say('Создаю встречи…');
      req('POST', '/api/v1/cycle/' + encodeURIComponent(ID) + '/approve', {items: items}).then(function(r){
        if(r.s !== 200){ say((r.j && r.j.detail) || 'Не получилось', 'err'); return; }
        T = r.j.task; say('Готово: встречи в расписании и в Google Календаре', 'ok'); paint();
      }, function(){ say('Нет связи с сервером', 'err'); });
    };
    $('no').onclick = function(){
      req('POST', '/api/v1/cycle/' + encodeURIComponent(ID) + '/dismiss').then(function(r){ if(r.s === 200){ T = r.j.task; paint(); say('Закрыто', 'ok'); } });
    };
  }
  function start(){
    req('GET', '/api/v1/cycle/' + encodeURIComponent(ID)).then(function(r){
      if(r.s === 401){ $('s').textContent = 'Откройте эту страницу кнопкой «Другое время» в боте или войдите на платформу командой.'; return; }
      if(r.s !== 200){ $('s').textContent = 'Задача не найдена.'; return; }
      T = r.j.task; paint();
    }, function(){ say('Нет связи с сервером', 'err'); });
  }
  if(/tgWebAppData=/.test(location.hash) || (window.Telegram && window.Telegram.WebApp)){
    var s0 = document.createElement('script'); s0.src = 'https://telegram.org/js/telegram-web-app.js';
    s0.onload = function(){ var wa = window.Telegram && window.Telegram.WebApp; if(wa){ INIT = wa.initData || ''; try{ wa.ready(); wa.expand(); }catch(e){} } start(); };
    s0.onerror = start;
    document.head.appendChild(s0);
  } else start();
})();
</script>
</body>
</html>
`
