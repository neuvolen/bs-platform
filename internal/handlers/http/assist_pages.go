package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Страницы приглашения ассистента (/assist/<токен>) и личной ссылки входа
// (/in/<токен>). Токен остаётся в адресе: страница не уходит в поиск
// (noindex), не кешируется, адрес не передаётся другим сайтам (referrer
// только с доменом). Сама страница токен не проверяет: это делает API при
// входе, превью ссылки в WhatsApp или Telegram ничего не тратит.

func serveAssistPage(c *gin.Context) { servePlainPage(c, assistPageHTML) }
func serveLinkPage(c *gin.Context)   { servePlainPage(c, linkPageHTML) }
func serveSwitchPage(c *gin.Context) { servePlainPage(c, switchPageHTML) }

func servePlainPage(c *gin.Context, body string) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Referrer-Policy", "strict-origin")
	c.Header("X-Frame-Options", "DENY")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

const plainPageHead = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<meta name="referrer" content="strict-origin">
<meta name="theme-color" content="#050505">
<link rel="icon" type="image/png" href="/site/logo.png">
<style>
:root{--bg:#050505;--card:#0E0E0E;--line:rgba(255,255,255,.12);--mute:#9A9A9A;--soft:#C9C9C9;--ok:#3DD68C;--err:#FF6B6F}
*{box-sizing:border-box;margin:0;padding:0}
html,body{background:var(--bg);color:#fff;font:16px/1.55 Manrope,-apple-system,'Segoe UI',Arial,sans-serif;-webkit-font-smoothing:antialiased}
main{min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px 16px}
.card{width:100%;max-width:440px;background:var(--card);border:1px solid var(--line);border-radius:22px;padding:28px 24px}
.brand{display:flex;align-items:center;gap:10px;font-weight:800;letter-spacing:.08em;text-transform:uppercase;font-size:12px;color:var(--soft)}
.brand img{width:34px;height:auto}
h1{font-size:23px;line-height:1.25;margin:20px 0 10px;letter-spacing:-.01em}
p{color:var(--soft);font-size:15px}
.facts{margin:16px 0 4px;border:1px solid var(--line);border-radius:14px}
.facts div{display:flex;justify-content:space-between;gap:12px;padding:11px 14px;font-size:14px;border-top:1px solid var(--line)}
.facts div:first-child{border-top:0}.facts span{color:var(--mute)}.facts b{text-align:right}
.w{margin-top:20px;min-height:48px;display:flex;flex-direction:column;align-items:center;gap:10px}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:8px;width:100%;min-height:50px;border-radius:999px;border:0;background:#fff;color:#050505;font:700 16px/1 inherit;font-family:inherit;cursor:pointer;text-decoration:none}
.btn.tg{background:#2AABEE;color:#fff}
.btn svg{width:20px;height:20px;fill:currentColor}
.msg{margin-top:14px;font-size:14px;color:var(--soft);white-space:pre-line;text-align:center;min-height:20px}
.msg.err{color:var(--err)}.msg.ok{color:var(--ok)}
.foot{margin-top:22px;font-size:12.5px;color:var(--mute);text-align:center}
.foot a{color:var(--soft)}
</style>
`

const plainPageFoot = `<div class="foot"><a href="/privacy">Политика конфиденциальности</a> · <a href="/terms">Условия использования</a></div>`

// Общий код: сохранить вход и открыть платформу с чистыми данными этого
// браузера (там могли остаться данные другого человека или кабинета).
const plainPageJS = `
function $(id){ return document.getElementById(id); }
function say(t, cls){ var m = $('m'); m.textContent = t || ''; m.className = 'msg' + (cls ? ' ' + cls : ''); }
function token(){ var p = location.pathname.split('/'); return p[p.length - 1] || ''; }
function wipe(){
  try{
    var ks = [];
    for(var i = 0; i < localStorage.length; i++){ var k = localStorage.key(i); if(k && (k.indexOf('bs_') === 0 || k.indexOf('bsx_') === 0 || k === 'r26_res_cache')) ks.push(k); }
    ks.forEach(function(k){ localStorage.removeItem(k); });
  }catch(e){}
  try{ if(window.indexedDB) indexedDB.deleteDatabase('bs_store'); }catch(e){}
}
function enter(j){
  wipe();
  try{
    localStorage.setItem('bsx_token', j.token);
    localStorage.setItem('bsx_user', JSON.stringify({user: j.user, team: j.team || {}}));
  }catch(e){}
  say('Вход выполнен, открываю платформу…', 'ok');
  setTimeout(function(){ location.replace('/'); }, 300);
}
function post(url, body){
  return fetch(url, {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)})
    .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(j){ return {s: r.status, j: j}; }); });
}
`

var assistPageHTML = plainPageHead + `<title>Приглашение ассистента · Business Surgery</title>
</head>
<body>
<main><div class="card">
  <div class="brand"><img src="/site/logo-white.png" alt="" width="34" height="16">Business Surgery</div>
  <h1 id="t">Приглашение на платформу</h1>
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
  function accept(proof){
    say('Проверяю…');
    proof.token = T;
    post('/api/v1/platform/assist/accept', proof).then(function(r){
      if(r.s === 200) return enter(r.j);
      say((r.j && r.j.detail) || ('Не получилось: ' + ((r.j && r.j.error) || r.s)), 'err');
    }, function(){ say('Нет связи с сервером. Проверьте интернет', 'err'); });
  }
  window.BSX_onTelegram = function(u){ accept({widget: u}); };
  function esc(s){ return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function login(){
    // возврат со страницы входа Telegram (запасной путь)
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
      if(!c.telegram){ say('Вход через Telegram ещё не настроен на сервере', 'err'); return; }
      var s = document.createElement('script');
      s.async = true; s.src = 'https://telegram.org/js/telegram-widget.js?22';
      s.setAttribute('data-telegram-login', c.bot); s.setAttribute('data-size', 'large'); s.setAttribute('data-radius', '20');
      s.setAttribute('data-request-access', 'write'); s.setAttribute('data-lang', 'ru'); s.setAttribute('data-onauth', 'BSX_onTelegram(user)');
      W.appendChild(s);
      setTimeout(function(){
        var fr = W.querySelector('iframe'); if((fr && fr.offsetHeight > 10) || !c.id) return;
        var a = document.createElement('a'); a.className = 'btn tg';
        a.href = 'https://oauth.telegram.org/auth?bot_id=' + encodeURIComponent(c.id) + '&origin=' + encodeURIComponent(location.origin) +
          '&request_access=write&return_to=' + encodeURIComponent(location.origin + location.pathname);
        a.innerHTML = TG_IC + 'Войти через Telegram'; W.appendChild(a);
      }, 3500);
    }).catch(function(){ say('Сервер недоступен. Обновите страницу', 'err'); });
  }
  fetch('/api/v1/platform/assist/invite-info?t=' + encodeURIComponent(T), {credentials: 'same-origin'})
    .then(function(r){ return r.json().then(function(j){ return {s: r.status, j: j}; }); })
    .then(function(r){
      if(r.s !== 200){
        $('t').textContent = 'Приглашение недействительно';
        $('s').textContent = r.s === 429 ? 'Слишком много попыток. Подождите 10 минут' : 'Ссылка истекла (она действует 72 часа), уже принята или отозвана. Попросите резидента прислать новую.';
        return;
      }
      var j = r.j, exp = j.expiresAt ? new Date(j.expiresAt) : null;
      $('t').textContent = j.resident + ' приглашает вас ассистентом';
      $('s').textContent = 'Вы будете работать в кабинете резидента на платформе клуба Business Surgery. Войдите своим Telegram, чтобы принять приглашение.';
      $('f').innerHTML = '<div><span>Права</span><b>' + esc(j.presetName) + '</b></div>' +
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

var linkPageHTML = plainPageHead + `<title>Вход на платформу · Business Surgery</title>
</head>
<body>
<main><div class="card">
  <div class="brand"><img src="/site/logo-white.png" alt="" width="34" height="16">Business Surgery</div>
  <h1>Вход на платформу</h1>
  <p>Личная ссылка входа без Telegram. Не пересылайте её никому: по ней открывается ваш кабинет.</p>
  <div class="w" id="w"><button class="btn" id="go" type="button">Войти на платформу</button></div>
  <div class="msg" id="m" role="status" aria-live="polite"></div>
  ` + plainPageFoot + `
</div></main>
<script>
` + plainPageJS + `
(function(){
  var busy = false;
  function go(){
    if(busy) return; busy = true; say('Вхожу…');
    post('/api/v1/platform/auth/link', {token: token()}).then(function(r){
      busy = false;
      if(r.s === 200) return enter(r.j);
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

// /switch: ассистент сменил кабинет (или вышел): данные прежнего кабинета
// стираются из браузера здесь, в обход синхронизации платформы (на странице
// платформы удаление ключей ушло бы на сервер), и платформа открывается заново.
var switchPageHTML = plainPageHead + `<title>Business Surgery</title>
</head>
<body>
<main><div class="card">
  <div class="brand"><img src="/site/logo-white.png" alt="" width="34" height="16">Business Surgery</div>
  <h1>Открываю кабинет…</h1>
  <div class="msg" id="m" role="status" aria-live="polite"></div>
</div></main>
<script>
` + plainPageJS + `
(function(){
  var j = null;
  try{ j = JSON.parse(sessionStorage.getItem('bsx_handoff') || 'null'); sessionStorage.removeItem('bsx_handoff'); }catch(e){}
  if(j && j.token) return enter(j);
  if(j && j.logout) wipe();
  location.replace('/');
})();
</script>
</body>
</html>
`
