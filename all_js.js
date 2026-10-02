
// ══ САМООБНОВЛЕНИЕ ══
// Telegram кеширует Mini App очень агрессивно: при входе с кнопки в чатах
// открывалась старая сборка. Приложение скачивает свой же файл в обход кеша,
// сравнивает версию и перезагружается. Отдельный version.json не нужен
var APP_VERSION = "202610021400";
function bsCheckVersion(silentOk){
  try{
    var base = location.href.split('?')[0].split('#')[0].replace(/[^/]*$/, '');
    return fetch(base + 'app.html?nocache=' + Date.now(), {
      cache: 'no-store',
      headers: {'Cache-Control': 'no-cache, no-store', 'Pragma': 'no-cache'}
    })
      .then(function(r){ return r.ok ? r.text() : null; })
      .then(function(txt){
        if(!txt) return null;
        var m = txt.match(/APP_VERSION\s*=\s*"([^"]+)"/);
        if(!m) return null;
        var serverVersion = m[1];
        if(serverVersion === APP_VERSION){
          if(!silentOk && window.showToast) showToast('Установлена последняя версия');
          return serverVersion;
        }
        var flag = 'bs_reload_' + serverVersion;
        if(sessionStorage.getItem(flag)) return serverVersion;
        sessionStorage.setItem(flag, '1');
        try{ localStorage.removeItem('bs_cache'); }catch(e){}
        var params = new URLSearchParams(location.search);
        params.set('v', serverVersion);
        params.delete('nocache');
        location.replace(base + '?' + params.toString());
        return serverVersion;
      })
      .catch(function(){ return null; });
  }catch(e){ return Promise.resolve(null); }
}

// Проверка при запуске, повторно через 4 секунды (на случай медленного GitHub)
// и при каждом возврате в приложение
bsCheckVersion(true);
setTimeout(function(){ bsCheckVersion(true); }, 4000);
document.addEventListener('visibilitychange', function(){
  if(document.visibilityState === 'visible') bsCheckVersion(true);
});

;

const WEBAPP_URL = 'https://script.google.com/macros/s/AKfycbwBbU7pJMyIptJoOtJ5hctU2rYbh3AioA-ScM14Y3dwuIF0UFNYrp7HiWqhOiQX74NpPA/exec';
// Сервер платформы: проверяет по подписи Telegram, кто открыл приложение, и держит
// готовые данные, чтобы экран открывался сразу. Если сервер недоступен, приложение
// идёт в таблицу напрямую, как раньше
const APP_SERVER = 'https://bs-platform-production.up.railway.app/api/v1/app/';
function bsInitData(){
  try{ return (window.Telegram && Telegram.WebApp && Telegram.WebApp.initData) || ''; }catch(e){ return ''; }
}
async function appGet(params){
  var init = bsInitData();
  var qs = new URLSearchParams(params || {});
  if(init){
    try{
      var sq = new URLSearchParams(params || {}); sq.set('_tg', init);
      var r = await fetch(APP_SERVER + 'call?' + sq.toString());
      if(r.ok) return await r.json();
      if(r.status === 401 || r.status === 400) console.warn('app server refused', r.status);
    }catch(e){ console.warn('app server unreachable', e); }
  }
  var r2 = await fetch(WEBAPP_URL + '?' + qs.toString());
  return await r2.json();
}
async function appPost(obj){
  var init = bsInitData();
  if(init){
    try{
      var r = await fetch(APP_SERVER + 'post', {method:'POST', headers:{'Content-Type':'text/plain;charset=utf-8'},
        body: JSON.stringify(Object.assign({}, obj, {_tg: init}))});
      if(r.ok) return await r.json();
    }catch(e){ console.warn('app server unreachable', e); }
  }
  var r2 = await fetch(WEBAPP_URL, {method:'POST', headers:{'Content-Type':'text/plain;charset=utf-8'}, body: JSON.stringify(obj)});
  return await r2.json();
}
const ADMIN_IDS = ['453800951','1285596249'];

// ── Глобальные функции для onclick на карточках ──
window.openResByIdx = function(idx){
  var list = idx >= 10000 ? (window._teamResidentsList || []) : (window._currentResidentsList || []);
  var r = list[idx >= 10000 ? idx - 10000 : idx];
  if(r && typeof openResidentProfile === 'function'){
    openResidentProfile(r);
  }
};

window.openMeetByIdx = function(idx){
  var active = document.querySelector('.page.active');
  var pageId = active ? active.id : '';
  var meetList = pageId === 'page-mymeetings' 
    ? (window._currentMyMeetingsList || [])
    : (window._currentScheduleList || []);
  var ev = meetList[idx];
  if(ev && typeof showMeetingDetail === 'function'){
    showMeetingDetail(ev);
  }
};



const tg = window.Telegram?.WebApp;
let user = null;
let isRealAdmin = false;
let viewAs = 'admin';
let cache = {};
let allResidents = [];
let selFineType = 'Не сдан отчёт';
let selDate = '';
let selTime = '';
let payType = 'income';
let payMethod = 'cash';

// ═══ НАВИГАЦИЯ v2: нижнее меню по ролям, разделы внутри вкладки ═══
const NAV_ICONS = {
  home:'<path d="M4 11l8-7 8 7v9h-5v-6H9v6H4z"/>',
  board:'<path d="M6 3v6h6v6h6v6"/><rect x="6" y="9" width="6" height="6"/>',
  tasks:'<rect x="4" y="4" width="16" height="16" rx="2"/><path d="M8 12l3 3 5-6"/>',
  club:'<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c1-3.5 3.5-5 6.5-5s5.5 1.5 6.5 5"/><path d="M16 5a3.5 3.5 0 0 1 0 7M18 15c2 .6 3.2 2.2 3.8 5"/>',
  me:'<circle cx="12" cy="8" r="4"/><path d="M4 21c1.5-4 4.5-6 8-6s6.5 2 8 6"/>',
  sum:'<path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/>',
  cal:'<rect x="4" y="5" width="16" height="15" rx="2"/><path d="M8 3v4M16 3v4M4 10h16"/>',
  more:'<rect x="4" y="4" width="6.5" height="6.5" rx="1.5"/><rect x="13.5" y="4" width="6.5" height="6.5" rx="1.5"/><rect x="4" y="13.5" width="6.5" height="6.5" rx="1.5"/><rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.5"/>',
  doc:'<path d="M5 4h10l4 4v12H5z"/><path d="M9 12h6M9 16h4"/>',
  plus:'<path d="M12 5v14M5 12h14"/>',
  chev:'<path d="M9 5l7 7-7 7"/>',
  msg:'<path d="M4 5h16v11H9l-5 4z"/>',
  back:'<path d="M15 5l-7 7 7 7"/>',
  alert:'<circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16.5v.5"/>',
  search:'<circle cx="11" cy="11" r="6.5"/><path d="M16 16l4 4"/>',
  x:'<path d="M6 6l12 12M18 6L6 18"/>',
  pen:'<path d="M4 20h4L19 9l-4-4L4 16z"/>',
  minus:'<path d="M5 12h14"/>',
  check:'<path d="M5 12l5 5 9-10"/>',
  book:'<path d="M5 4.5A1.5 1.5 0 0 1 6.5 3H19v15H6.5A1.5 1.5 0 0 0 5 19.5z"/><path d="M5 19.5A1.5 1.5 0 0 0 6.5 21H19"/><path d="M9 7.5h6M9 11h4"/>',
  cl:'<rect x="4" y="3.5" width="16" height="17" rx="2.5"/><path d="M8 8.5l1.5 1.5 2.5-2.8M8 14.5l1.5 1.5 2.5-2.8M14.5 9h2M14.5 15h2"/>'
};
function navIcon(k, size, w){
  return '<svg width="'+(size||24)+'" height="'+(size||24)+'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="'+(w||1.8)+'" stroke-linecap="round" stroke-linejoin="round">'+(NAV_ICONS[k]||'')+'</svg>';
}
const NAV = {
  admin: [
    {id:'sum',  label:'Сводка',    icon:'sum',  pages:[{id:'dashboard', label:'Сводка'}]},
    {id:'meet', label:'Встречи',   icon:'cal',  pages:[{id:'schedule', label:'Расписание'}]},
    {id:'plus', plus:true},
    {id:'res',  label:'Резиденты', icon:'club', pages:[{id:'residents', label:'Резиденты'},{id:'reports', label:'Отчёты'},{id:'visits', label:'Посещения'},{id:'fines', label:'Штрафы'},{id:'restasks', label:'Задачи'}]},
    {id:'more', label:'Меню',      icon:'more', pages:[{id:'more', label:'Ещё'},{id:'about', label:'О проекте'},{id:'crm', label:'CRM'},{id:'subscribers', label:'Подписчики'},{id:'custdev', label:'CustDev'},{id:'content', label:'Полезное'},{id:'leadmagnets', label:'Материалы'},{id:'wheeladmin', label:'Колесо'},{id:'rules', label:'Правила', hidden:true},{id:'diagnostic', label:'Диагностика'},{id:'myprofile', label:'Профиль'},{id:'todos', label:'Задачи команды'},{id:'library', label:'Библиотека'},{id:'wheel', label:'Мои замеры', hidden:true},{id:'guides', label:'99 гайдов', hidden:true},{id:'guide', label:'Гайд', hidden:true},{id:'referral', label:'Приглашение', hidden:true},{id:'leadrazbor', label:'Экспресс-разбор', hidden:true}]}
  ],
  resident: [
    {id:'home',  label:'Главная', icon:'home',  pages:[{id:'home', label:'Главная'},{id:'library', label:'Библиотека'},{id:'guides', label:'99 гайдов', hidden:true},{id:'guide', label:'Гайд', hidden:true},{id:'referral', label:'Приглашение', hidden:true}]},
    {id:'board', label:'Разбор',  icon:'board', pages:[{id:'myboard', label:'Мой разбор'},{id:'wheel', label:'Колесо'},{id:'diagnostic', label:'Диагностика'}]},
    {id:'tasks', label:'Задачи',  icon:'tasks', pages:[{id:'mytasks', label:'Задачи'},{id:'mymeetings', label:'Встречи'}]},
    {id:'club',  label:'Клуб',    icon:'club',  pages:[{id:'residents', label:'Резиденты'},{id:'leadmagnets', label:'Материалы'},{id:'about', label:'О проекте'},{id:'rules', label:'Правила', hidden:true}]},
    {id:'me',    label:'Профиль', icon:'me',    pages:[{id:'mystatus', label:'Статус'},{id:'myprofile', label:'Данные'}]}
  ],
  lead: [
    {id:'home', label:'Главная',     icon:'home',  pages:[{id:'about', label:'Главная'}]},
    {id:'cl',   label:'Гайды',       icon:'book',  pages:[{id:'guides', label:'99 гайдов'},{id:'guide', label:'Гайд', hidden:true}]},
    {id:'diag', label:'Диагностика', icon:'board', pages:[{id:'diagnostic', label:'Диагностика'}]},
    {id:'razbor', label:'Разбор',    icon:'cal',   pages:[{id:'leadrazbor', label:'Разбор'}]}
  ]
};
function navFlat(role){
  var out = [];
  NAV[role].forEach(function(g){ (g.pages||[]).forEach(function(pg){ out.push(pg); }); });
  return out;
}
// Плоские списки остаются: по ним работают блокировки, глубокие ссылки и проверки доступа
const ADMIN_TABS = navFlat('admin');
const LEAD_TABS = navFlat('lead');
const RESIDENT_TABS = navFlat('resident');
function navRole(){ return (isRealAdmin && viewAs === 'admin') ? 'admin' : (viewAs === 'lead' ? 'lead' : 'resident'); }
function navGroupOf(pageId, role){
  var gs = NAV[role || navRole()];
  for(var i = 0; i < gs.length; i++){
    if((gs[i].pages||[]).some(function(pg){ return pg.id === pageId; })) return gs[i];
  }
  return null;
}
window._navGroup = null;
window._navLast = {};

const FINE_TYPES = [
  {label:'Отчёт', emoji:'', name:'Не сдан отчёт', amount:10000},
  {label:'Опоздание', emoji:'', name:'Опоздание', amount:10000},
  {label:'Слово', emoji:'', name:'Цена слова', amount:100000},
  {label:'Пропуск', emoji:'', name:'Пропуск', amount:10000},
  {label:'Нарушение', emoji:'', name:'Нарушение', amount:10000},
  {label:'Прочее', emoji:'', name:'Прочее', amount:0}
];

const INCOME_SRC = ['БХ Трекинг продление','БХ Трекинг год','БХ Штраф','БХ Экспресс разбор','БХ МК/Завтрак','Прочие доходы'];
const EXPENSE_SRC = ['Дивиденды','Бонус резидента','Аренда','Комиссия+налог','Маркет.бюджет','SMM','Таргетолог','Tilda + домен','CRM','Canva','Съемки проф','Съемки доп','HH объявление','Симка тариф','Оборудование','Офис','Ассистент','Бухгалтер','Юрист','IT','Прочие расходы:'];


// Глобальный обработчик кликов по карточкам резидентов
document.body.addEventListener('click', function(e){
  var card = e.target.closest('.res-card');
  if(!card) return;
  if(e.target.closest('a')) return;
  var idx = parseInt(card.dataset.residx);
  if(isNaN(idx)) return;
  var list = window._currentResidentsList || [];
  var r = list[idx];
  if(r && typeof openResidentProfile === 'function'){
    openResidentProfile(r);
  }
}, true);


// ── Глобальный обработчик кликов по карточкам резидентов ──
function handleGlobalClick(e){
  var el = e.target;
  while(el && el !== document.body){
    if(el.classList && el.classList.contains('res-card')){
      if(e.target.closest('a')) return;
      var idx = parseInt(el.getAttribute('data-residx'));
      if(isNaN(idx)) return;
      var list = window._currentResidentsList || [];
      var r = list[idx];
      if(r && typeof openResidentProfile === 'function'){
        e.preventDefault();
        openResidentProfile(r);
      }
      return;
    }
    if(el.classList && el.classList.contains('meeting') && el.hasAttribute('data-meetidx')){
      var midx = parseInt(el.getAttribute('data-meetidx'));
      if(isNaN(midx)) return;
      // Берём из текущего активного списка
      var active = document.querySelector('.page.active');
      var pageId = active ? active.id : '';
      var meetList = pageId === 'page-mymeetings' 
        ? (window._currentMyMeetingsList || [])
        : (window._currentScheduleList || []);
      var ev = meetList[midx];
      if(ev && typeof showMeetingDetail === 'function'){
        e.preventDefault();
        showMeetingDetail(ev);
      }
      return;
    }
    el = el.parentElement;
  }
}

document.addEventListener('click', handleGlobalClick);

function init(){
  try {
    if(tg){
      tg.ready();
      tg.expand();
    // Края обрезались: Telegram не поддерживает env(safe-area-inset) в WebView.
    // Берём отступы из самого Telegram и применяем как CSS-переменные
    try{
      var applySafe = function(){
        var si = tg.safeAreaInset || {};
        var ci = tg.contentSafeAreaInset || {};
        var top = (si.top || 0) + (ci.top || 0);
        var bottom = (si.bottom || 0) + (ci.bottom || 0);
        document.documentElement.style.setProperty('--tg-safe-top', top + 'px');
        document.documentElement.style.setProperty('--tg-safe-bottom', bottom + 'px');
      };
      applySafe();
      if(tg.onEvent){
        tg.onEvent('safeAreaChanged', applySafe);
        tg.onEvent('contentSafeAreaChanged', applySafe);
        tg.onEvent('viewportChanged', applySafe);
      }
    }catch(e){}

  // Telegram Mini App: при открытии клавиатуры скроллим к активному инпуту в модалке
  // Это решает проблему когда клавиатура закрывает поле ввода или кнопки
  if(window.visualViewport){
    window.visualViewport.addEventListener('resize', function(){
      var active = document.activeElement;
      if(!active) return;
      var modal = active.closest('.modal');
      if(modal){
        // Скроллим инпут в видимую область с отступом
        setTimeout(function(){
          active.scrollIntoView({block:'center', behavior:'smooth'});
        }, 100);
      }
    });
  }
  
  // При фокусе на input/textarea внутри модалки - скроллим к нему
  document.addEventListener('focusin', function(e){
    var t = e.target;
    if(!t || !t.tagName) return;
    if(t.tagName !== 'INPUT' && t.tagName !== 'TEXTAREA' && t.tagName !== 'SELECT') return;
    if(!t.closest('.modal')) return;
    setTimeout(function(){
      try{ t.scrollIntoView({block:'center', behavior:'smooth'}); }catch(e){}
    }, 300);
  });

      // Header/Background color. hex поддерживается с API 6.9+, иначе используем bg_color
      try {
        var ver = parseFloat(tg.version || '6.0');
        if(ver >= 6.9){
          tg.setHeaderColor('#0a0a0a');
          tg.setBackgroundColor('#0a0a0a');
        } else {
          // Старые версии (macOS desktop 6.4). только предопределённые ключи
          tg.setHeaderColor('secondary_bg_color');
          tg.setBackgroundColor('secondary_bg_color');
        }
      } catch(colorErr){ console.log('Color err:', colorErr); }
      user = tg.initDataUnsafe?.user;
    }
    isRealAdmin = user ? ADMIN_IDS.includes(String(user.id)) : false;
    
    // Определяем начальную роль (для НЕ-админов)
    if(!isRealAdmin){
      // 1) МГНОВЕННО из локального кеша данных: если чат ID есть в списке резидентов. это резидент
      // (раньше ждали ответ сервера и резидент видел интерфейс лида, иногда минутами)
      viewAs = 'lead';
      try{
        var cachedRaw = localStorage.getItem('bs_cache');
        if(cachedRaw && user && user.id){
          var cachedData = JSON.parse(cachedRaw);
          var myUid = String(user.id);
          var meCached = (cachedData.residents||[]).find(function(r){
            return String(r.chatId||'') === myUid;
          });
          if(meCached && !meCached.isFired) viewAs = 'resident';
        }
      }catch(e){}
      
      // 2) Сервер подтверждает/уточняет (подстраховка на случай нового резидента без кеша)
      if(user && user.id){
        try{
          callAction('checkUserRole', {}).then(function(res){
            if(res && res.role){
              // Свежий список резидентов надёжнее закешированного ответа о роли:
              // активный резидент не должен откатываться в режим лида
              try{
                var meNow = (cache && cache.residents || []).find(function(r){ return String(r.chatId||'') === String(user.id); });
                if(res.role === 'lead' && meNow && !meNow.isFired) return;
              }catch(e){}
              var oldRole = viewAs;
              viewAs = res.role;
              try{ localStorage.setItem('bs_view_as', viewAs); }catch(e){}
              if(oldRole !== viewAs){
                renderTabs();
                renderAllPages();
                // Попробуем применить deep-link с учётом новой роли
                var newPage = null;
                if(window._bsResolveDeep) newPage = window._bsResolveDeep(viewAs);
                if(newPage){
                  v11openDeep(newPage);
                  return;
                }
                // Роль сменилась: открываем стартовую страницу новой роли
                showPage(viewAs === 'lead' ? 'diagnostic' : 'home');
              }
            }
          }).catch(function(err){
            console.log('checkUserRole failed:', err);
          });
        }catch(checkE){ console.log('checkUserRole error:', checkE); }
      }
    } else {
      // Админ. восстановим сохранённый режим или admin по умолчанию
      var adminSavedRole = null;
      try{ adminSavedRole = localStorage.getItem('bs_view_as'); }catch(e){}
      viewAs = adminSavedRole || 'admin';
      
      var rt = document.getElementById('roleToggle');
      if(rt){
        rt.classList.add('show');
        // Активируем правильную кнопку
        rt.querySelectorAll('button').forEach(function(b){
          b.classList.toggle('active', b.getAttribute('data-role') === viewAs);
        });
        // Привязываем обработчики (через data-role)
        rt.querySelectorAll('button').forEach(function(b){
          b.addEventListener('click', function(e){
            e.stopPropagation();
            try{ this.blur(); }catch(blurE){}
            setRole(b.getAttribute('data-role'));
          });
        });
      }
    }
    renderTabs();
    // Расписание всегда открывается календарём (переключатель на список остаётся)
    window._scheduleViewMode = 'calendar';
    
    // Deep-link: определяем начальную вкладку из параметра URL или Telegram startParam
    var initialPage = null;
    try {
      // 1. Из Telegram WebApp init data (передаётся через web_app_data при открытии)
      if (tg && tg.initDataUnsafe && tg.initDataUnsafe.start_param) {
        initialPage = String(tg.initDataUnsafe.start_param);
      }
      // 2. Из URL параметра ?p=schedule
      if (!initialPage) {
        var urlParams = new URLSearchParams(window.location.search);
        initialPage = urlParams.get('p') || urlParams.get('page') || urlParams.get('tab');
      }
      // 3. Из hash #schedule
      if (!initialPage && window.location.hash) {
        initialPage = window.location.hash.substring(1).split('?')[0];
      }
    } catch(e){ console.log('deep-link parse:', e); }
    
    // Алиасы для глубоких ссылок: какая вкладка показывается для какой роли
    // Например, ссылка "schedule" для админа = вкладка "schedule", для резидента = "mymeetings"
    var PAGE_ALIASES = {
      // Расписание встреч
      'schedule':     {admin:'schedule',    resident:'mymeetings',  lead:'about'},
      'meetings':     {admin:'schedule',    resident:'mymeetings',  lead:'about'},
      'mymeetings':   {admin:'schedule',    resident:'mymeetings',  lead:'about'},
      // Отчёты
      'reports':      {admin:'reports',     resident:'mystatus',    lead:'about'},
      // Штрафы
      'fines':        {admin:'fines',       resident:'mystatus',    lead:'about'},
      // Резиденты
      'residents':    {admin:'residents',   resident:'residents',   lead:'about'},
      'subscribers':  {admin:'subscribers', resident:'about',       lead:'about'},
      // Дашборд / сводка
      'dashboard':    {admin:'dashboard',   resident:'home',        lead:'about'},
      'home':         {admin:'dashboard',   resident:'home',        lead:'about'},
      'mystatus':     {admin:'dashboard',   resident:'mystatus',    lead:'about'},
      // Профиль
      'myprofile':    {admin:'myprofile',   resident:'myprofile',   lead:'leadrazbor'},
      'profile':      {admin:'myprofile',   resident:'myprofile',   lead:'leadrazbor'},
      'razbor':       {admin:'diagnostic',  resident:'myboard',     lead:'leadrazbor'},
      // Материалы / чек-листы
      // У лида «Материалы» слились с гайдами: старые ссылки ведут в каталог
      'leadmagnets':  {admin:'leadmagnets', resident:'leadmagnets', lead:'guides'},
      'materials':    {admin:'leadmagnets', resident:'leadmagnets', lead:'guides'},
      // Запись на экспресс-разбор
      'book':         {admin:'leadrazbor',  resident:'myboard',     lead:'leadrazbor'},
      'booking':      {admin:'leadrazbor',  resident:'myboard',     lead:'leadrazbor'},
      'leadrazbor':   {admin:'leadrazbor',  resident:'myboard',     lead:'leadrazbor'},
      // Пригласить предпринимателя
      'referral':     {admin:'referral',    resident:'referral',    lead:'about'},
      'invite':       {admin:'referral',    resident:'referral',    lead:'about'},
      'ref':          {admin:'referral',    resident:'referral',    lead:'about'},
      // Диагностика
      'diagnostic':   {admin:'diagnostic',  resident:'about',       lead:'diagnostic'},
      // Задачи (только админ)
      'todos':        {admin:'todos',       resident:'about',       lead:'about'},
      'tasks':        {admin:'todos',       resident:'about',       lead:'about'},
      // BS главная
      'about':        {admin:'about',       resident:'about',       lead:'about'},
      // Правила живут внутри «О проекте»: открываем О проекте, затем Правила
      'rules':        {admin:'rules',       resident:'rules',       lead:'about'},
      'main':         {admin:'about',       resident:'about',       lead:'about'},
      // Платформа
      'platform':     {admin:'about',       resident:'myboard',     lead:'about'},
      'board':        {admin:'about',       resident:'myboard',     lead:'about'},
      // 99 гайдов: для всех ролей (старые ссылки на чек-листы ведут в каталог)
      'guides':       {admin:'guides',      resident:'guides',      lead:'guides'},
      'checklists':   {admin:'guides',      resident:'guides',      lead:'guides'},
      'checklist':    {admin:'guides',      resident:'guides',      lead:'guides'},
      '99':           {admin:'guides',      resident:'guides',      lead:'guides'}
    };
    
    function _resolveInitialPage(rawPage, role){
      if(!rawPage) return null;
      // #guide_g012 и start_param guide_g012: каталог, затем ридер гайда
      var gdm = /^(?:guides?|g)[_-](g?\d{1,3})$/i.exec(String(rawPage));
      if(gdm && v14normId(gdm[1])){ window._gdDeep = v14normId(gdm[1]); return 'guides'; }
      // Старые #checklist_c012: просто каталог гайдов
      if(/^(?:checklists?|cl)[_-]c\d{1,4}$/i.test(String(rawPage))) return 'guides';
      var aliases = PAGE_ALIASES[String(rawPage).toLowerCase()];
      if(aliases && aliases[role]) return aliases[role];
      // Если алиас не найден, проверим что такая страница есть в доступных
      var availableIds;
      if(role === 'admin') availableIds = ADMIN_TABS.map(function(t){return t.id;});
      else if(role === 'lead') availableIds = LEAD_TABS.map(function(t){return t.id;});
      else availableIds = RESIDENT_TABS.map(function(t){return t.id;});
      if(availableIds.indexOf(rawPage) >= 0) return rawPage;
      return null;
    }
    
    window._bsResolveDeep = function(role){ return initialPage ? _resolveInitialPage(initialPage, role) : null; };
    // Эффективная роль для маршрутизации
    var effectiveRole = (isRealAdmin && viewAs === 'admin') ? 'admin' : viewAs;
    var resolvedPage = _resolveInitialPage(initialPage, effectiveRole);
    
    if (resolvedPage) {
      console.log('Deep-link', initialPage, '→', resolvedPage, '(role:', effectiveRole, ')');
      v11openDeep(resolvedPage);
    } else {
      // Стартовая по умолчанию для каждой роли
      if (effectiveRole === 'admin') {
        showPage('dashboard');
      } else if (effectiveRole === 'lead') {
        showPage('about');
      } else {
        showPage('home');
      }
    }
    
    loadAllData();
    setupAutoRefresh();
    setTimeout(preloadBsLogo, 800);
    setTimeout(function(){
      // Аватар в base64 из бандла: canvas не загрязняется, картинка собирается всегда
      try{
        var au = cache.myAvatar || '';
        if(au && !window._bsAvatarImg){
          var ai = new Image();
          ai.onload = function(){ window._bsAvatarImg = ai; };
          ai.src = au;
        }
      }catch(e){}
    }, 1200);
    // Автосохранение аватара из Telegram (для других пользователей)
    if(user && user.photo_url && user.id){
      setTimeout(function(){
        callAction('saveAvatar', {chatId: String(user.id), avatar: user.photo_url});
      }, 2000);
    }
  } catch(e){
    console.error('init error:', e);
    document.body.innerHTML = '<div style="padding:40px;color:#fff;text-align:center;font-family:sans-serif"><h2>Ошибка</h2><pre style="font-size:11px;color:#999;margin-top:10px;text-align:left;background:#222;padding:12px;border-radius:8px;overflow:auto">'+e.message+'\n'+(e.stack||'')+'</pre></div>';
  }
}

function setRole(role){
  viewAs = role;
  // Обновляем active кнопку
  const buttons = document.querySelectorAll('.role-toggle button');
  buttons.forEach(b => {
    if(b.getAttribute('data-role') === role){
      b.classList.add('active');
    } else {
      b.classList.remove('active');
    }
  });
  // Перерисовываем страницы под новую роль (иначе остаются кнопки админа) и переходим на первую
  try{ renderAllPages(); }catch(e){ console.error('role render:', e); }
  renderTabs();
  setTimeout(() => {
    window._navGroup = null;
    var firstTab = NAV[navRole()][0].pages[0].id;
    showPage(firstTab);
  }, 50);
  // Сохраняем выбор админа
  try{ localStorage.setItem('bs_view_as', role); }catch(e){}
}


function renderTabs(){
  // Нижнее меню роли + разделы текущей вкладки сверху
  var role = navRole();
  var actEl = document.querySelector('.page.active');
  var actG = actEl ? navGroupOf(actEl.id.replace('page-',''), role) : null;
  if(actG) window._navGroup = actG;
  else if(window._navGroup && NAV[role].indexOf(window._navGroup) < 0) window._navGroup = null;
  var bn = document.getElementById('bnav');
  if(bn){
    bn.innerHTML = NAV[role].map(function(g){
      if(g.plus) return '<button class="bnav-plus" aria-label="Быстрый ввод" onclick="openQuickMenu()"><span>'+navIcon('plus',22,2.2)+'</span></button>';
      var on = window._navGroup && window._navGroup.id === g.id;
      return '<button class="bnav-i'+(on?' on':'')+'" data-group="'+g.id+'" onclick="navGo(\''+g.id+'\')">'+navIcon(g.icon)+'<span>'+g.label+'</span></button>';
    }).join('');
  }
  var back = document.getElementById('v4BackAdmin');
  if(back) back.classList.toggle('show', !!(isRealAdmin && viewAs !== 'admin'));
  var grp = window._navGroup && NAV[role].indexOf(window._navGroup) >= 0 ? window._navGroup : NAV[role][0];
  var el = document.getElementById('tabs');
  var wrap = el ? el.parentNode : null;
  if(!el) return;
  var pages = grp.pages || [];
  var act = document.querySelector('.page.active');
  var actId = act ? act.id.replace('page-','') : '';
  var hideChips = pages.filter(function(t){ return !t.hidden; }).length < 2 ||
    (role === 'resident' && (grp.id === 'tasks' || actId === 'residents' || actId === 'mystatus' || actId === 'about')) ||
    (role === 'admin' && actId === 'more');
  if(wrap) wrap.classList.toggle('single', hideChips);
  var hdr = document.querySelector('.header');
  var noHead = hideChips && !isRealAdmin;
  if(hdr) hdr.classList.toggle('v3-empty', noHead);
  document.body.classList.toggle('v3-nohead', noHead);
  el.innerHTML = pages.filter(function(t){ return !t.hidden || t.id === actId; }).map(function(t){
    var lockedCls = isTabLocked(t) ? ' locked' : '';
    return '<div class="tab'+(t.id===actId?' active':'')+lockedCls+'" data-page="'+t.id+'" onclick="showPage(\''+t.id+'\')">'+t.label+'</div>';
  }).join('');
}

// Нажатие на вкладку нижнего меню: открываем последний раздел этой вкладки
function navGo(gid){
  var g = NAV[navRole()].find(function(x){ return x.id === gid; });
  if(!g || !g.pages) return;
  if(gid === 'res') window._resFilter = 'all';
  // Верхних чипов больше нет: вкладка нижнего меню всегда открывает свой главный экран
  var target = g.pages[0].id;
  if(navRole() === 'lead' && gid === 'diag') target = 'diagnostic';
  showPage(target, {root: true});
}

function isTabLocked(tab){
  // Возвращает true если вкладку нужно показать заблокированной
  if(!tab.locked) return false;
  if(isRealAdmin && viewAs === 'admin') return false; // админ видит всё
  // Проверяем по типу locked
  if(tab.locked === 'diagnostic'){
    // Профиль доступен только после прохождения диагностики
    try{
      var done = localStorage.getItem('bs_diagnostic_done');
      return !done;
    }catch(e){ return true; }
  }
  return false;
}




function showLockedPage(pageId, tab){
  // Активируем эту вкладку визуально и показываем заглушку с CTA
  document.querySelectorAll('.tab').forEach(t => t.classList.toggle('active', t.getAttribute('data-page') === pageId));
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  var el = document.getElementById('page-' + pageId);
  if(!el) return;
  el.classList.add('active');
  try{ renderTabs(); }catch(e){}
  
  if(tab.locked === 'diagnostic'){
    el.innerHTML = `
      <div class="locked-page">
        <div class="locked-page-icon"></div>
        <div class="locked-page-title">Сначала диагностика</div>
        <div class="locked-page-text">Эта страница откроется после прохождения диагностики бизнеса. Это займёт 5 минут.</div>
        <button class="cta-button" onclick="showPage('diagnostic')">
          <div class="cta-button-icon">
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="9 18 15 12 9 6"/></svg>
          </div>
          <div class="cta-button-text">
            <div class="cta-button-title">Пройти диагностику</div>
            <div class="cta-button-sub">5 минут, 35 вопросов</div>
          </div>
          <div class="cta-button-arrow">→</div>
        </button>
      </div>
    `;
  }
}


function _showPageCore(id){
  var role = navRole();
  var tabsLocal = navFlat(role);
  const tab = tabsLocal.find(t => t.id === id);
  var grp = navGroupOf(id, role);
  if(grp){ window._navGroup = grp; window._navLast[grp.id] = id; }
  if(tab && isTabLocked(tab)){
    renderTabs();
    showLockedPage(id, tab);
    return;
  }
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  const p = document.getElementById('page-'+id);
  if(p) p.classList.add('active');
  renderTabs();
  const t = document.querySelector('#tabs [data-page="'+id+'"]');
  if(t){ try{ t.scrollIntoView({behavior:'smooth', inline:'center', block:'nearest'}); }catch(e){} }
  if(id === 'home'){ try{ renderResHome(); }catch(e){ console.error('home:', e); } }
  if(id === 'myboard'){ try{ renderMyBoard(); }catch(e){ console.error('myboard:', e); } }
  if(id === 'residents'){ try{ renderResidents(); }catch(e){ console.error('residents:', e); } }
  if(id === 'more'){ try{ renderAdminMore(); }catch(e){ console.error('more:', e); } }
  if(id === 'about'){ try{ renderAbout(); }catch(e){ console.error('about:', e); } }
  if(id === 'dashboard'){ try{ renderDashboard(); }catch(e){ console.error('dashboard:', e); } }
  if(id === 'mystatus'){ try{ renderMyStatus(); }catch(e){ console.error('mystatus:', e); } }
  if(id === 'mytasks'){ try{ loadMyTasks(); }catch(e){ console.error('mytasks:', e); } }
  if(id === 'mymeetings'){ try{ renderMyMeetings(); }catch(e){ console.error('mymeetings:', e); } }
  if(id === 'myprofile'){ try{ renderMyProfile(); }catch(e){ console.error('myprofile:', e); } }
  if(id === 'leadmagnets'){ try{ renderLeadmagnets(); }catch(e){ console.error('lm:', e); } }
  if(id === 'leadrazbor'){ try{ renderLeadRazbor(); v15load(); }catch(e){ console.error('razbor:', e); } }
  if(id === 'referral'){ try{ v15renderRef(); v15refLoad(); }catch(e){ console.error('referral:', e); } }
  if(id === 'library'){ try{ renderLibrary(); }catch(e){ console.error('library:', e); } }
  if(id === 'guides'){ try{ renderGuides(); }catch(e){ console.error('guides:', e); } }
  if(id === 'guide'){ try{ renderGuide(); }catch(e){ console.error('guide:', e); } }
  try{ if(typeof v14barSync === 'function') v14barSync(); }catch(e){}
}

// ══════════ НАВИГАЦИЯ: ИСТОРИЯ, «НАЗАД», ПРОКРУТКА ══════════
// Каждый переход запоминает, откуда пришли. Корневые экраны вкладок историю сбрасывают.
// Кнопка «‹ Меню / ‹ Главная» сверху и системная «Назад» Telegram ведут на шаг назад
window._navStack = [];
function v9rootPages(role){
  var out = {};
  (NAV[role] || []).forEach(function(g){ if(g.pages && g.pages[0]) out[g.pages[0].id] = 1; });
  if(role === 'lead') out.diagnostic = 1;
  return out;
}
function v9title(id){
  var role = navRole();
  if(role === 'lead' && id === 'about') return 'Главная';
  if(role !== 'admin' && id === 'residents') return 'Клуб';
  if(role !== 'admin' && id === 'myprofile') return 'Мои данные';
  if(role !== 'admin' && id === 'mystatus') return 'Профиль';
  if(role === 'admin' && id === 'myprofile') return 'Мой профиль';
  var T = {dashboard:'Сводка', schedule:'Встречи', residents:'Резиденты', more:'Меню', home:'Главная', myboard:'Разбор', mytasks:'Задачи',
    mymeetings:'Встречи', mystatus:'Профиль', about:'О проекте', rules:'Правила', wheel:'Колесо', diagnostic:'Диагностика', leadmagnets:'Материалы',
    library:'Библиотека', reports:'Отчёты', fines:'Штрафы', visits:'Посещения', restasks:'Задачи резидентов', crm:'CRM', subscribers:'Подписчики',
    custdev:'CustDev', content:'Полезное', wheeladmin:'Колесо клуба', todos:'Задачи команды', leadrazbor:'Разбор',
    guides:'99 гайдов', guide:'Гайд', referral:'Приглашение'};
  return T[id] || 'Назад';
}
function v9activeId(){ var a = document.querySelector('.page.active'); return a ? a.id.replace('page-', '') : ''; }
function v9scrollTop(){
  try{ window.scrollTo(0, 0); }catch(e){}
  try{ if(document.scrollingElement) document.scrollingElement.scrollTop = 0; }catch(e){}
  try{ document.documentElement.scrollTop = 0; document.body.scrollTop = 0; }catch(e){}
  var a = document.querySelector('.page.active'); if(a) a.scrollTop = 0;
}
function showPage(id, opts){
  opts = opts || {};
  // Лид: отдельной страницы «Материалы» больше нет, PDF из бота живут в гайдах
  if(id === 'leadmagnets' && navRole() === 'lead') id = 'guides';
  var st = window._navStack;
  var cur = v9activeId();
  var roots = v9rootPages(navRole());
  if(opts.root || (roots[id] && !opts.stack)) st.length = 0;
  else if(!opts.back && cur && cur !== id){ st.push(cur); if(st.length > 20) st.shift(); }
  var k = st.lastIndexOf(id); if(k >= 0) st.length = k;
  var dv = document.getElementById('detailView'); if(dv && dv.classList.contains('show')) dv.classList.remove('show');
  // Расписание при каждом открытии показывается календарём
  var v10sched = id === 'schedule' && cur !== 'schedule';
  if(v10sched){ window._scheduleViewMode = 'calendar'; window._calMonthOffset = 0; }
  _showPageCore(id);
  if(v10sched){ try{ renderSchedule(); }catch(e){ console.error('schedule:', e); } }
  v9scrollTop();
  setTimeout(v9scrollTop, 0);
  v9updateBack();
}
// Глубокая ссылка: Правила открываются поверх «О проекте», чтобы «Назад» вёл туда
function v11openDeep(id){
  if(id === 'rules'){ showPage('about'); window._navStack.length = 0; showPage('rules'); return; }
  if(id === 'guides'){
    var r0 = navRole();
    if(r0 !== 'lead'){ showPage(r0 === 'admin' ? 'more' : 'home'); window._navStack.length = 0; }
    showPage('guides');
    if(window._gdDeep){ var gid = window._gdDeep; window._gdDeep = null; v14open(gid); }
    return;
  }
  showPage(id);
}
function v9detailOpen(){ var d = document.getElementById('detailView'); return !!(d && d.classList.contains('show')); }
function v9updateBack(){
  var st = window._navStack || [];
  var cur = v9activeId();
  var bar = document.getElementById('v9Back');
  var show = st.length > 0 && !!cur;
  if(bar){
    bar.classList.toggle('show', show);
    var l = document.getElementById('v9BackLbl'); if(l && show) l.textContent = v9title(st[st.length - 1]);
  }
  document.body.classList.toggle('v9-hasback', show);
  try{
    if(tg && tg.BackButton){
      if(show || v9detailOpen()) tg.BackButton.show(); else tg.BackButton.hide();
    }
  }catch(e){}
}
function v9goBack(){
  if(v9detailOpen()){ closeDetail(); return; }
  var m = document.querySelector('.modal-bg.show');
  if(m){ m.classList.remove('show'); v9updateBack(); return; }
  var ov = document.querySelector('.add-menu-overlay'); if(ov){ ov.remove(); v9updateBack(); return; }
  var st = window._navStack || [];
  var prev = st.pop();
  if(prev) showPage(prev, {back: true});
  else v9updateBack();
}
function v9backToAdmin(e){
  try{ if(e){ e.preventDefault(); e.stopPropagation(); } }catch(x){}
  try{ closeDetail(); }catch(x){}
  document.querySelectorAll('.modal-bg.show').forEach(function(m){ m.classList.remove('show'); });
  window._navStack = [];
  setRole('admin');
}
(function(){
  // Системная кнопка «Назад» Telegram: один обработчик на всё приложение
  function bind(){
    try{ if(tg && tg.BackButton && !window._v9TgBackBound){ window._v9TgBackBound = true; tg.BackButton.onClick(v9goBack); } }catch(e){}
  }
  if(document.readyState === 'loading') document.addEventListener('DOMContentLoaded', bind); else setTimeout(bind, 0);
})()

// ══════════ АВТООБНОВЛЕНИЕ ДАННЫХ ══════════
// Раньше приходилось ждать и вручную перезагружать страницу. теперь свежие данные приходят сами
let _lastDataLoad = 0;
let _autoRefreshTimer = null;

function setupAutoRefresh(){
  // 1) Возврат в приложение (свернул/развернул, переключил вкладку) → обновляем если данные старше 10 сек
  document.addEventListener('visibilitychange', function(){
    if(document.visibilityState === 'visible' && Date.now() - _lastDataLoad > 10000){
      loadAllData();
    }
  });
  window.addEventListener('focus', function(){
    if(Date.now() - _lastDataLoad > 10000) loadAllData();
  });
  
  // 2) Периодическое обновление каждые 30 секунд пока приложение открыто
  if(_autoRefreshTimer) clearInterval(_autoRefreshTimer);
  _autoRefreshTimer = setInterval(function(){
    if(document.visibilityState === 'visible') loadAllData();
  }, 60000);
  
  // 3) Свайп вниз на верху страницы = принудительное обновление
  let touchStartY = 0;
  document.addEventListener('touchstart', function(e){
    touchStartY = e.touches[0].clientY;
  }, {passive:true});
  document.addEventListener('touchend', function(e){
    const dy = e.changedTouches[0].clientY - touchStartY;
    if(dy > 90 && window.scrollY < 5){
      loadAllData({fresh: true});
    }
  }, {passive:true});
}

function _bsHash(str){
  var h = 5381;
  for(var i = 0; i < str.length; i++) h = ((h << 5) + h + str.charCodeAt(i)) | 0;
  return h;
}
async function loadAllData(opts){
  // Мгновенный старт: рисуем из локального кеша сразу, сервер догоняет в фоне.
  // Перерисовка только если данные реально изменились (сравниваем отпечаток)
  opts = opts || {};
  try {
    var cached = localStorage.getItem('bs_cache');
    if(cached && !window._renderedFromCache){
      cache = JSON.parse(cached);
      hideDoneMeetings(cache);
      window._renderedFromCache = true;
      renderAllPages();
    }
  } catch(e){}
  var dot = document.getElementById('syncDot');
  if(dot) dot.style.opacity = '0.9';
  try {
    var freshParam = opts.fresh ? '&fresh=1' : '';
    var bundleParams = {action: 'getBotCache'};
    if(user && user.id) bundleParams.chatId = user.id;
    if(opts.fresh) bundleParams.fresh = '1';
    var serverData = await appGet(bundleParams);
    // Сохраняем локальные изменения встреч
    var now = Date.now();
    var localPending = window._pendingMeetings || [];
    var localDeleted = window._deletedMeetingKeys || [];
    var serverSchedule = serverData.schedule || [];
    // Удаляем из серверных те что мы помечали удалить
    serverSchedule = serverSchedule.filter(function(s){
      return localDeleted.indexOf(s.res+'|'+s.date+'|'+s.time) < 0;
    });
    // ВАЖНО: встречи моложе 60 сек НИКОГДА не пропадают (защита от гонки)
    // Они могли быть созданы только что. сервер ещё не вернул их
    var recentlyCreated = (cache && cache.schedule ? cache.schedule : []).filter(function(s){
      return s._createdAt && (now - s._createdAt) < 60000;
    });
    // Берём union: serverSchedule + recentlyCreated (если их там нет)
    recentlyCreated.forEach(function(rc){
      var exists = serverSchedule.some(function(s){
        return s.res===rc.res && s.date===rc.date && s.time===rc.time;
      });
      if(!exists) serverSchedule.push(rc);
    });
    // Также pending (могут быть и без _createdAt из предыдущих версий)
    localPending.forEach(function(p){
      var exists = serverSchedule.some(function(s){
        return s.res===p.res && s.date===p.date && s.time===p.time;
      });
      if(!exists) serverSchedule.push(p);
    });
    serverData.schedule = serverSchedule;
    hideDoneMeetings(serverData);

    // Отпечаток: если данные не изменились. не трогаем интерфейс вообще
    var newStamp = '';
    try{
      newStamp = [
        (serverData.residents||[]).length,
        (serverSchedule||[]).length,
        (serverData.fines||[]).length,
        (serverData.reports||[]).length,
        ((serverData.resTasks||{}).tasks||[]).length,
        ((serverData.contentPlan||{}).items||[]).length,
        ((serverData.smm||{}).items||[]).length,
        ((serverData.problems||{}).problems||[]).length,
        _bsHash(JSON.stringify(serverData.monthlyPL||{})),
        _bsHash(JSON.stringify(serverData.fines||[])),
        _bsHash(JSON.stringify(serverData.residents||[])),
        _bsHash(JSON.stringify(serverSchedule||[])),
        JSON.stringify(serverData.wheelSummary||{}).length
      ].join('|');
    }catch(e){ newStamp = String(Date.now()); }

    var unchanged = (window._dataStamp === newStamp);
    window._dataStamp = newStamp;

    cache = serverData;
    try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
    if(dot) dot.style.opacity = '0';
    if(unchanged && window._renderedFromCache && !opts.force){
      _lastDataLoad = Date.now();
      return; // ничего не изменилось. интерфейс не дёргаем
    }
    
    _lastDataLoad = Date.now();
    
    // Пересчёт роли по свежим данным: если человек есть в резидентах. даём доступ немедленно
    // (решает случай "резидент видит интерфейс лида")
    if(!isRealAdmin && user && user.id){
      var myUid2 = String(user.id);
      var meFresh = (cache.residents||[]).find(function(r){ return String(r.chatId||'') === myUid2; });
      var freshRole = (meFresh && !meFresh.isFired) ? 'resident' : 'lead';
      if(freshRole !== viewAs){
        viewAs = freshRole;
        try{ localStorage.setItem('bs_view_as', viewAs); }catch(e){}
        renderTabs();
        // Роль сменилась: глубокая ссылка, если была, иначе стартовая страница роли
        var dl = window._bsResolveDeep ? window._bsResolveDeep(viewAs) : null;
        v11openDeep(dl || (viewAs === 'lead' ? 'diagnostic' : 'home'));
      }
    }
    
    renderAllPages();
  } catch(e){
    var d2 = document.getElementById('syncDot'); if(d2) d2.style.opacity = '0';
    console.error('loadAllData:', e);
    if(!cache || !cache.residents){
      cache = {residents:[], fines:[], logs:[], schedule:[], debet:{}};
      renderAllPages();
    }
  }
}

function renderAllPages(){
  // Раньше при каждом автообновлении (30 сек) перерисовывались ВСЕ страницы и заново
  // дёргались async-запросы. отсюда мигание, повторные загрузки материалов и колеса.
  // Теперь: синхронные страницы рисуем всегда (дёшево), async-блоки. только один раз
  // и по явному запросу пользователя
  try{renderAbout();}catch(e){console.error('about:',e);}
  try{ if(isRealAdmin) renderAdminMore(); }catch(e){console.error('more:',e);}
  try{renderRules();}catch(e){console.error('rules:',e);}
  try{renderDiagnostic();}catch(e){console.error('diag:',e);}
  try{renderDashboard();}catch(e){console.error('dashboard:',e);}
  try{renderSchedule();}catch(e){console.error('schedule:',e);}
  try{renderReports();}catch(e){console.error('reports:',e);}
  try{renderFines();}catch(e){console.error('fines:',e);}
  try{renderCustdev();}catch(e){console.error('custdev:',e);}
  try{renderResidents();}catch(e){console.error('residents:',e);}
  try{renderTodos();}catch(e){console.error('todos:',e);}
  try{renderMyStatus();}catch(e){console.error('mystatus:',e);}
  try{renderMyProfile();}catch(e){console.error('myprofile:',e);}
  try{renderMyMeetings();}catch(e){console.error('mymeetings:',e);}
  try{ if(navRole() !== 'admin') renderResHome(); }catch(e){console.error('home:',e);}
  try{ ensureAboutCard(); }catch(e){}

  // Всё приходит одним запросом и рисуется из кеша. дополнительных загрузок нет
  try{renderLeadmagnets();}catch(e){console.error('lm:',e);}
  try{renderLeadRazbor();}catch(e){console.error('razbor:',e);}
  try{renderSubscribers();}catch(e){console.error('subs:',e);}
  try{renderWheel();}catch(e){console.error('wheel:',e);}
  try{loadMyTasks();}catch(e){console.error('mytasks:',e);}
  try{loadProblems();}catch(e){console.error('problems:',e);}
  if(isRealAdmin){
    try{renderWheelAdmin();}catch(e){console.error('wheeladmin:',e);}
    try{renderAllResTasks();}catch(e){console.error('restasks:',e);}
    try{renderVisits();}catch(e){console.error('visits:',e);}
    try{renderCrm();}catch(e){console.error('crm:',e);}
    try{renderContentPlan();}catch(e){console.error('content:',e);}
    try{renderSmm();}catch(e){console.error('smm:',e);}
  }
}

// Загрузка «один раз за сессию». Повторно только через forceReload()
// Локальное обновление кеша: интерфейс меняется сразу, сервер догоняет в фоне
function patchCache(path, updater){
  try{
    var parts = path.split('.');
    var node = cache;
    for(var i = 0; i < parts.length - 1; i++){
      if(!node[parts[i]]) node[parts[i]] = {};
      node = node[parts[i]];
    }
    var last = parts[parts.length - 1];
    node[last] = updater(node[last]);
    try{ localStorage.setItem('bs_cache', JSON.stringify(cache)); }catch(e){}
  }catch(e){ console.error('patchCache', e); }
}

// Тихое обновление с сервера: без тостов и перерисовки если ничего не изменилось
function silentSync(){
  setTimeout(function(){ loadAllData({fresh: true}); }, 400);
}

// Логотип BS держим готовым в памяти: Stories собирается без ожидания
window._bsLogoImg = null;
function preloadBsLogo(){
  try{
    var li = document.querySelector('.hero-logo-real img');
    var src = (li && li.src) || window._bsLogoSrc;
    if(!src) return;
    window._bsLogoSrc = src;
    var im = new Image();
    im.onload = function(){ window._bsLogoImg = im; };
    im.src = src;
  }catch(e){}
}

function refreshData(){
  // Тихо: без тостов и кнопок, данные просто подтягиваются
  loadAllData({fresh: true, force: true});
}

window._loadedOnce = window._loadedOnce || {};
function loadOnce(key, fn){
  if(window._loadedOnce[key]) return;
  window._loadedOnce[key] = true;
  try{ fn(); }catch(e){ console.error('loadOnce ' + key, e); window._loadedOnce[key] = false; }
}
function forceReload(key, fn){
  window._loadedOnce[key] = false;
  loadOnce(key, fn);
}

// ═══ АВАТАРКИ: фото из профиля Telegram через сервер ═══
// Сервер берёт фото через бота. Если фото нет или профиль закрыт, остаётся буква
function avImg(chatId, fallback){
  var id = String(chatId||'').replace(/[^0-9]/g,'');
  var src = id ? APP_SERVER + 'avatar/' + id : (fallback || '');
  if(!src) return '';
  var fb = (fallback && id) ? String(fallback).replace(/'/g,'%27') : '';
  return '<img class="av-img" alt="" loading="lazy" src="'+v2esc(src)+'" onerror="'+(fb ? "if(!this.dataset.f){this.dataset.f=1;this.src='"+v2esc(fb)+"'}else{this.remove()}" : 'this.remove()')+'">';
}
function resByName(name){ return (cache.residents||[]).find(function(r){ return r.name === name; }); }

// Встречи, которые уже прошли и отмечены (лог встреч), в расписании не висят
function hideDoneMeetings(data){
  try{
    var done = {};
    (data.doneMeetings||[]).forEach(function(d){ done[String(d.res||d.name||'').trim()+'|'+String(d.date||'').slice(0,10)] = 1; });
    if(!Object.keys(done).length || !data.schedule) return;
    var now = Date.now();
    data.schedule = data.schedule.filter(function(ev){
      var k = String(ev.res||'').trim()+'|'+String(ev.date||'').slice(0,10);
      if(!done[k]) return true;
      return parseScheduleKey(ev) > now;   // отметка сегодня прячет только уже начавшуюся встречу
    });
  }catch(e){ console.error('hideDone', e); }
}

// ═══ РЕЗИДЕНТ: ГЛАВНАЯ ═══
function v2esc(t){ return String(t==null?'':t).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
function v2money(n){ return fmt(Math.round(Number(n)||0)); }
// ═══ ДИЗАЙН v3: общие элементы экранов ═══
var BS_MARK_SRC = 'data:image/png;base64,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';
var V3_CHECK = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12l5 5 9-10"/></svg>';
var V3_MON = ['ЯНВ','ФЕВ','МАР','АПР','МАЙ','ИЮН','ИЮЛ','АВГ','СЕН','ОКТ','НОЯ','ДЕК'];
function bsLogoSrc(){ var i = document.querySelector('.header .logo img'); return i ? i.src : ''; }
function v3icon(k, s, w){ return navIcon(k, s || 20, w); }
function v3mark(){ return '<img class="v3-mark" src="' + BS_MARK_SRC + '" alt="bs">'; }
function v3date(){
  var t = new Date().toLocaleDateString('ru-RU', {weekday:'long', day:'numeric', month:'long'});
  return t.charAt(0).toUpperCase() + t.slice(1);
}
function v3shortDate(d){
  var p = String(d || '').split('.');
  if(p.length < 2 || !V3_MON[Number(p[1]) - 1]) return String(d || '');
  return Number(p[0]) + ' ' + V3_MON[Number(p[1]) - 1];
}
function v3initials(name){
  var p = String(name || '').trim().split(/\s+/);
  var a = (p[0] || '').charAt(0), b = (p[1] || '').charAt(0);
  return (a + (/[А-ЯЁA-Z]/.test(b) ? b : '')).toUpperCase() || '·';
}
function v3plural(n, one, few, many){
  n = Math.abs(Number(n) || 0) % 100; var n1 = n % 10;
  if(n > 10 && n < 20) return many;
  if(n1 > 1 && n1 < 5) return few;
  if(n1 === 1) return one;
  return many;
}
function v3prof(r){
  if(!r || !r.chatId) return null;
  var id = String(r.chatId);
  return (cache.adminProfiles || []).find(function(p){ return String(p.chatId) === id; }) || null;
}
function v3av(r, size, inv){
  size = size || 40; r = r || {};
  var p = v3prof(r);
  return '<div class="v3-av' + (inv ? ' inv' : '') + '" style="width:' + size + 'px;height:' + size + 'px;font-size:' + (size < 50 ? 13 : 18) + 'px">' +
    v2esc(v3initials(r.name)) + avImg(r.chatId, r.avatar || (p && p.avatar)) + '</div>';
}
function v3isToday(d){
  if(!d) return false;
  var p = String(d).split('.'), t = new Date();
  return Number(p[0]) === t.getDate() && Number(p[1]) === t.getMonth() + 1 && (!p[2] || Number(p[2]) === t.getFullYear());
}
function v3dateTs(d, time){
  var p = String(d || '').split('.');
  var x = new Date(Number(p[2]) || new Date().getFullYear(), (Number(p[1]) || 1) - 1, Number(p[0]) || 1);
  if(time){ var q = String(time).split(':'); x.setHours(Number(q[0]) || 0, Number(q[1]) || 0); }
  return x.getTime();
}
function v3isEvent(ev){ return String((ev && ev.res) || '').indexOf('🎬') === 0; }
function v3isOnline(ev){ var l = String((ev && ev.link) || ''); return l.indexOf('meet') >= 0 || l.indexOf('zoom') >= 0; }
// Чат в Telegram: по нику, если он есть, иначе по chatId
function v3chatUrl(r){
  var u = v11tgUser(r);
  if(u) return 'https://t.me/' + u;
  var id = String((r && r.chatId) || '').replace(/[^0-9]/g, '');
  return id ? 'tg://user?id=' + id : '';
}
// ═══ v11: «НАПИСАТЬ». Есть ник: открываем чат в Telegram. Нет ника: сообщение через бота BS ═══
// tg://user?id=… внутри Telegram у людей без ника не открывается, поэтому пишем через сервер
var V11_TEAM_IDS = {'453800951': 'Рустам', '1285596249': 'Береке'};
function v11tgUser(r){
  if(!r) return '';
  var p = v3prof(r) || {};
  var id = String(r.chatId || '');
  var s = id ? ((window._subscribers || []).filter(function(x){ return String(x.chatId) === id; })[0] || {}) : {};
  var c = [r.username, r.userTg, r.tgUsername, r.tg, r.telegram, p.username, p.userTg, p.tg, p.telegram, s.username];
  for(var i = 0; i < c.length; i++){
    var u = String(c[i] || '').trim().replace(/^https?:\/\/(www\.)?t(elegram)?\.me\//i, '').replace(/^@/, '').split(/[/?#\s]/)[0];
    if(/^[A-Za-z][A-Za-z0-9_]{3,31}$/.test(u)) return u;
  }
  return '';
}
function v11chatId(r){
  if(!r) return '';
  var id = String(r.chatId || '').replace(/[^0-9]/g, '');
  if(!id && r.name){ V4_TEAM.forEach(function(t){ if(t.re.test(String(r.name))) id = t.id; }); }
  return id;
}
// Админ пишет любому; резидент и лид только команде клуба
function v11canCompose(r){
  var id = v11chatId(r);
  if(!id) return false;
  return navRole() === 'admin' || !!V11_TEAM_IDS[id];
}
function v11canWrite(r){ return !!(v11tgUser(r) || v11canCompose(r)); }
function v11write(r){
  if(typeof r === 'string') r = resByName(r) || {name: r};
  if(!r) return;
  var u = v11tgUser(r);
  if(u){
    var url = 'https://t.me/' + u;
    try{ if(tg && tg.openTelegramLink){ tg.openTelegramLink(url); return; } }catch(e){}
    window.open(url, '_blank');
    return;
  }
  if(!v11canCompose(r)){ showToast(v11chatId(r) ? 'Написать можно только команде клуба' : 'Нет контакта в Telegram'); return; }
  v11compose(r);
}
function v11compose(r){
  var id = v11chatId(r);
  var old = document.getElementById('v11msg'); if(old) old.remove();
  var m = document.createElement('div');
  m.className = 'add-menu-overlay show';
  m.id = 'v11msg';
  m.onclick = function(e){ if(e.target === m) m.remove(); };
  var who = navRole() === 'admin' ? (/берек/i.test(String((user && user.first_name) || '')) || String((user && user.id) || '') === '1285596249' ? 'Береке' : 'Рустам') : '';
  m.innerHTML = '<div class="add-menu">' +
    '<div class="v3-row-sb" style="margin-bottom:4px"><div class="add-menu-title" style="margin:0;text-align:left">Сообщение ' + v2esc(String(r.name || V11_TEAM_IDS[id] || '').trim()) + '</div>' +
    '<button class="v3-sheet-x" data-x="1" aria-label="Закрыть">' + navIcon('x', 16, 2) + '</button></div>' +
    '<div class="v3-cap" style="margin:0 0 12px;line-height:1.45">' + (who ? 'Бот BS доставит в личные сообщения с подписью «Сообщение от ' + who + '»' : 'Бот BS доставит в личные сообщения от вашего имени') + '</div>' +
    '<textarea id="v11text" class="field-input" rows="4" maxlength="2000" placeholder="Текст сообщения" style="width:100%;resize:none;min-height:110px"></textarea>' +
    '<button class="v3-primary" data-s="1" style="margin-top:12px">Отправить</button>' +
  '</div>';
  document.body.appendChild(m);
  m.querySelector('[data-x]').onclick = function(){ m.remove(); };
  var btn = m.querySelector('[data-s]');
  btn.onclick = function(){
    var text = String(m.querySelector('#v11text').value || '').trim();
    if(!text){ showToast('Напишите текст сообщения'); return; }
    var init = bsInitData();
    if(!init){ showToast('Откройте приложение из Telegram'); return; }
    btn.disabled = true; btn.textContent = 'Отправляем...';
    fetch(APP_SERVER + 'message?' + new URLSearchParams({_tg: init}).toString(), {method: 'POST', headers: {'Content-Type': 'text/plain;charset=utf-8'}, body: JSON.stringify({chatId: id, text: text})})
      .then(function(res){ return res.json().catch(function(){ return {error: 'http ' + res.status}; }); })
      .then(function(j){
        if(j && j.ok){ m.remove(); showToast('Отправлено'); try{ tg.HapticFeedback.notificationOccurred('success'); }catch(x){} return; }
        throw new Error((j && j.error) || 'ошибка');
      })
      .catch(function(e){ btn.disabled = false; btn.textContent = 'Отправить'; showToast('Не отправлено: ' + String((e && e.message) || 'нет связи')); });
  };
  setTimeout(function(){ try{ m.querySelector('#v11text').focus(); }catch(e){} }, 80);
}
function v3write(r){ v11write(r); }
function v11writeSub(id){
  var s = (window._subscribers || []).filter(function(x){ return String(x.chatId) === String(id); })[0] || {chatId: id};
  v11write({name: s.name || 'подписчику', chatId: s.chatId, username: s.username});
}
function v3row(icon, title, sub, onclick, right){
  return '<div class="v2-row" onclick="' + onclick + '">' + (icon ? '<div class="ic">' + v3icon(icon, 20) + '</div>' : '') +
    '<div class="m"><b>' + title + '</b>' + (sub ? '<span>' + sub + '</span>' : '') + '</div>' +
    (right != null ? right : '<div class="ic" style="color:#6E6E6E">' + v3icon('chev', 16) + '</div>') + '</div>';
}
function v3headMark(sub, title, right){
  return '<div class="v3-head"><div class="l">' + v3mark() + '<div style="min-width:0"><div class="v3-title">' + title + '</div>' + (sub ? '<div class="v3-sub" style="margin-top:3px">' + sub + '</div>' : '') + '</div></div>' + (right || '') + '</div>';
}
function v3headBig(sub, title, right){
  return '<div class="v3-headbig"><div style="min-width:0"><div class="v3-title xl">' + title + '</div>' + (sub ? '<div class="v3-sub">' + sub + '</div>' : '') + '</div>' + (right || '') + '</div>';
}
function v3crumb(label, onclick){
  return '<div class="v3-crumb"><button class="v3-back" aria-label="Назад" onclick="' + onclick + '">' + v3icon('back', 18) + '</button><div>' + label + '</div></div>';
}
// Резиденты клуба и отчёты за день
function v3activeResidents(){
  return (cache.residents || []).filter(function(r){ return !r.isFired && !r.isAdmin && !r.isTeam && r.name !== 'Тест' && r.name !== 'Тест2'; });
}
function v3isMine(l, r){
  var id = String(r.chatId || '');
  return (id && String(l.chatId || l.userId || '') === id) || String(l.name || '').trim() === r.name;
}
function v3reportSent(r, date){
  date = date || new Date().toLocaleDateString('ru-RU');
  return (cache.logs || []).some(function(l){ return l.date === date && v3isMine(l, r); });
}
function v3missingReports(){
  return v3activeResidents().filter(function(r){ return !r.isExcluded && !v3reportSent(r); });
}
function v3reports30(r){
  var since = Date.now() - 30 * 86400000, days = {};
  (cache.logs || []).forEach(function(l){ if(v3isMine(l, r) && v3dateTs(l.date) >= since) days[l.date] = 1; });
  return Object.keys(days).length;
}
function v3needsRenew(r){
  return (Number(r.debtRenew) || 0) > 0 || ((Number(r.meetingsGranted) || 0) > 0 && (Number(r.meetingsLeft) || 0) <= 0);
}
function v3unpaidFines(name){
  return (cache.fines || []).filter(function(f){ return f.name === name && f.status === 'Не оплатил'; });
}
function v3debtCaption(r){
  var debt = Number(r.debt) || 0;
  if(debt <= 0) return 'оплачено';
  var renew = (Number(r.debtRenew) || 0) > 0, n = v3unpaidFines(r.name).length;
  if(renew && n) return 'продление, штрафы';
  if(renew) return 'продление';
  if(n) return n > 1 ? 'штрафы' : 'штраф';
  return 'долг';
}
function v3catLabel(c){
  return ({brain:'СТРАТЕГИЯ', heart:'МАРКЕТИНГ', hands:'ПРОДАЖИ', spine:'КОМАНДА', blood:'ФИНАНСЫ', dna:'ПРОЦЕССЫ', eyes:'АНАЛИТИКА'})[c] || 'МАТЕРИАЛ';
}
// Действия из карточки резидента: открываем прежние шторки с выбранным резидентом
function v3payFor(){
  var r = window._v3CardRes; if(!r) return;
  openModal('payModal'); initPayModal();
  try{
    var src = document.getElementById('paySource');
    src.value = ((Number(r.debtRenew) || 0) <= 0 && v3unpaidFines(r.name).length) ? 'БХ Штраф' : 'БХ Трекинг продление';
    updatePayResidentVisibility();
    document.getElementById('payResident').value = r.name;
    lockPayHeight(true);
  }catch(e){}
}
function v3fineFor(){
  var r = window._v3CardRes; if(!r) return;
  openModal('fineModal'); initFineModal();
  try{ document.getElementById('fineResident').value = r.name; }catch(e){}
}
function v3meetFor(){
  var r = window._v3CardRes; if(!r) return;
  openModal('schedModal'); initSchedModal();
  try{ document.getElementById('schedResident').value = r.name; }catch(e){}
}

var V10_REPORT_URL = 'https://t.me/c/2494126345/9';
function v3sendReport(){
  // Отчёт пишется в группе клуба, тема «ОТЧЁТЫ» (чат -1002494126345, тема 9)
  var url = V10_REPORT_URL;
  try{ if(tg && tg.openTelegramLink){ tg.openTelegramLink(url); return; } }catch(e){}
  window.open(url, '_blank');
}
function renderResHome(){
  var el = document.getElementById('homeContent');
  if(!el) return;
  var me = getMyData();
  if(!me){
    el.innerHTML = v3headMark('', 'Главная') + '<div class="v2-card v2-empty" style="margin-top:16px">Данные ещё загружаются. Если это надолго, напишите Рустаму или Береке.</div>';
    return;
  }
  var today = new Date().toLocaleDateString('ru-RU');
  var sent = v3reportSent(me, today);
  var tasks = ((cache.resTasks && cache.resTasks.tasks) || []).filter(function(t){ return t.name === me.name; });
  var done = tasks.filter(function(t){ return t.status === 'Выполнена'; }).length;
  var now = Date.now();
  var meets = (cache.schedule||[]).filter(function(ev){ return ev.res === me.name && parseScheduleKey(ev) >= now - 3600000; });
  meets.sort(function(a,b){ return parseScheduleKey(a) - parseScheduleKey(b); });
  var next = meets[0];
  var debt = Number(me.debt)||0;
  var b = window._myBoard && window._myBoard.board;

  var pill = '';
  if(b && b.cycle) pill = '<div class="v3-pill">ЦИКЛ ' + v2esc(b.cycle) + '</div>';
  else if(me.reportStreak > 0) pill = '<div class="v3-pill gold">СЕРИЯ ' + me.reportStreak + '</div>';
  var h = v3headMark('', v2esc(String(me.name).split(' ')[0]), pill);
  if(sent){
    h += '<div class="v2-card" style="margin-top:18px;padding:16px 18px;display:flex;align-items:center;gap:12px">' +
           '<div style="width:34px;height:34px;border-radius:10px;background:#fff;color:#000;display:flex;align-items:center;justify-content:center;flex-shrink:0">' + V3_CHECK + '</div>' +
           '<div><div style="font-size:15px;font-weight:800">Отчёт за сегодня сдан</div><div style="font-size:12px;color:#9A9A9A;margin-top:2px">Завтра до 23:59 следующий</div></div></div>';
  } else {
    h += '<div class="v2-hero" style="margin-top:18px"><div class="k"><span>ОТЧЁТ ЗА СЕГОДНЯ</span><span>ДО 23:59</span></div>' +
           '<div class="t">Что сделали по задачам цикла?</div>' +
           '<div class="s">Отчёт пишется в группе клуба, в теме «ОТЧЁТЫ». Без отчёта в 14:30 следующего дня приходит штраф 10 000 ₸.</div>' +
           '<button onclick="v3sendReport()">Сдать отчёт</button></div>';
  }
  // Библиотека BS: крупно, сразу после отчёта
  h += v11homeLib();
  h += '<div class="v2-stats">' +
         '<div class="v2-stat" onclick="showPage(\'mytasks\')"><div class="l">Задачи</div><div class="v num">' + done + '<small>/' + tasks.length + '</small></div></div>' +
         '<div class="v2-stat" onclick="showPage(\'mystatus\')"><div class="l">Отчёты</div><div class="v num">' + v3reports30(me) + '<small>/30</small></div></div>' +
         '<div class="v2-stat' + (debt > 0 ? ' bad' : '') + '" onclick="showPage(\'mystatus\')"><div class="l">Долг</div><div class="v num">' + v2money(debt) + '</div></div>' +
       '</div>';
  h += v15homeCard();
  if(!window._v15refTried && window._ref){ window._v15refTried = true; setTimeout(function(){ v15refLoad(); }, 600); }
  if(debt > 0){
    h += '<a href="https://pay.kaspi.kz/pay/ri6h2lj5" target="_blank" style="display:block;margin-top:8px;text-decoration:none"><button class="v3-primary" style="height:50px;border-radius:12px">Оплатить ' + v2money(debt) + ' ₸ через Kaspi</button></a>';
  }
  h += '<div class="v2-h">Следующая встреча' + (meets.length ? ' <span class="lnk" onclick="showPage(\'mymeetings\')">Все ' + meets.length + '</span>' : '') + '</div>';
  if(next){
    var dp = String(next.date).split('.');
    var online = v3isOnline(next);
    var g = Number(me.meetingsGranted)||0, dn = Number(me.meetingsDone)||0;
    h += '<div class="v2-card" style="padding:14px 16px;display:flex;align-items:center;gap:14px">' +
           '<div style="text-align:center;width:44px"><div class="num" style="font-size:22px;font-weight:800">' + v2esc(String(dp[0]).padStart(2, '0')) + '</div><div style="font-size:10px;font-weight:800;color:#9A9A9A;letter-spacing:1px">' + (V3_MON[Number(dp[1])-1]||'') + '</div></div>' +
           '<div style="width:1px;align-self:stretch;background:#262626"></div>' +
           '<div style="flex:1;min-width:0"><div style="font-size:15px;font-weight:800">' + (online ? 'Онлайн-трекинг' : 'Офлайн-встреча') + '</div>' +
           '<div style="font-size:12px;color:#9A9A9A;margin-top:2px">' + v2esc(next.time||'') + ' · ' + (online ? 'Google Meet' : v2esc(next.link||'Место уточняется')) + '</div></div>' +
           (online ? '<a href="' + v2esc(next.link) + '" target="_blank" class="v3-btn w" style="text-decoration:none">Meet</a>' : (g ? '<div style="font-size:12px;font-weight:800;white-space:nowrap">' + dn + ' из ' + g + '</div>' : '')) +
         '</div>';
  } else {
    h += '<div class="v2-card v2-empty">Встреч в расписании пока нет</div>';
  }
  if(b){
    var upd = b.updated ? new Date(b.updated).toLocaleDateString('ru-RU', {day:'numeric', month:'short'}) : '';
    h += '<div class="v2-h">Мой разбор</div><div class="v2-card">' + '<div class="v2-row" onclick="showPage(\'myboard\')"><div class="ic">' + navIcon('board',20) + '</div><div class="m"><b>Обновлён разбор · цикл ' + b.cycle + '</b><span>' + (b.diagnoses.length ? v2esc(b.diagnoses.map(function(d){return d.title;}).slice(0,2).join(', ')) : 'Доска с трекером') + (upd ? ' · ' + upd : '') + '</span></div><div class="ic">' + navIcon('chev',16) + '</div></div>' + '</div>';
  }
  el.innerHTML = h;
  if(!window._myBoard && !window._v3BoardTried){
    window._v3BoardTried = true;
    loadMyBoard(false).then(function(){ if(document.getElementById('page-home').classList.contains('active')) renderResHome(); });
  }
}

// ═══ ОРГАНЫ: подписи для ключей с платформы (бывают русские названия, английские и наши id) ═══
var V9_ORGAN_ORDER = ['brain', 'heart', 'hands', 'spine', 'blood', 'dna', 'eyes'];
var V9_ORGAN_LABEL = {brain:'Стратегия', heart:'Маркетинг', hands:'Продажи', spine:'Команда', blood:'Финансы', dna:'Процессы', eyes:'Аналитика'};
var V9_ORGAN_ALIAS = {
  brain:'brain', strategy:'brain', 'стратегия':'brain', 'мозг':'brain',
  heart:'heart', marketing:'heart', 'маркетинг':'heart', 'сердце':'heart',
  hands:'hands', sales:'hands', 'продажи':'hands', 'руки':'hands', 'руки и ноги':'hands',
  spine:'spine', team:'spine', 'команда':'spine', 'костяк':'spine', 'скелет':'spine',
  blood:'blood', finance:'blood', finances:'blood', 'финансы':'blood', 'кровь':'blood',
  dna:'dna', process:'dna', processes:'dna', 'процессы':'dna', 'днк':'dna',
  eyes:'eyes', eye:'eyes', analytics:'eyes', 'аналитика':'eyes', 'зрение':'eyes', 'глаза':'eyes'
};
function v9organId(k){ return V9_ORGAN_ALIAS[String(k || '').trim().toLowerCase()] || ''; }
function v9organLabel(k){
  var id = v9organId(k);
  if(id) return V9_ORGAN_LABEL[id];
  var s = String(k || '').replace(/[_-]+/g, ' ').trim();
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : '';
}
function v9open(url){
  if(!url) return;
  url = String(url);
  try{
    if(/^https?:\/\/t\.me\//i.test(url) && tg && tg.openTelegramLink){ tg.openTelegramLink(url); return; }
    if(tg && tg.openLink){ tg.openLink(url); return; }
  }catch(e){}
  window.open(url, '_blank');
}

// ═══ БИБЛИОТЕКА: база знаний платформы (книги, инструменты, диагнозы, ресурсы) ═══
var V9_LIB_KINDS = [['all', 'Все'], ['book', 'Книги'], ['tool', 'Инструменты'], ['diag', 'Диагнозы'], ['resource', 'Ресурсы']];
var V9_LIB_ONE = {book:'Книга', tool:'Инструмент', diag:'Диагноз', resource:'Ресурс'};
var V9_LIB_ICONS = {
  book:'<path d="M4 5.5A1.5 1.5 0 0 1 5.5 4H11v16H5.5A1.5 1.5 0 0 1 4 18.5z"/><path d="M20 5.5A1.5 1.5 0 0 0 18.5 4H13v16h5.5a1.5 1.5 0 0 0 1.5-1.5z"/>',
  tool:'<path d="M14.7 6.3a4 4 0 0 0 5 5l-8.4 8.4a2.1 2.1 0 0 1-3-3z"/><path d="M14.7 6.3L17 4l3 3-2.3 2.3"/>',
  diag:'<path d="M3 12h4l2-5 4 10 2-5h6"/>',
  resource:'<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>'
};
function v9libIcon(kind, size){ return '<svg width="' + (size || 18) + '" height="' + (size || 18) + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' + (V9_LIB_ICONS[kind] || V9_LIB_ICONS.resource) + '</svg>'; }
function v9libKind(x){ var k = String((x && x.kind) || '').toLowerCase(); return V9_LIB_ONE[k] ? k : 'resource'; }
window._lib = null;
function v9libCached(){ try{ var c = JSON.parse(localStorage.getItem('bs_lib') || 'null'); return Array.isArray(c) ? c : null; }catch(e){ return null; } }
function loadLibrary(force){
  if(!force && window._lib && !window._lib.error && Date.now() - window._lib.at < 300000) return Promise.resolve(window._lib);
  if(window._libLoading) return window._libLoading;
  var init = bsInitData();
  if(!init){ window._lib = {items: v9libCached() || [], error: 'noinit', at: Date.now()}; return Promise.resolve(window._lib); }
  window._libLoading = fetch(APP_SERVER + 'library?' + new URLSearchParams({_tg: init}).toString())
    .then(function(r){
      if(!r.ok) throw new Error('http ' + r.status);
      return r.json();
    })
    .then(function(j){
      var items = Array.isArray(j) ? j : ((j && j.items) || []);
      items = items.filter(function(x){ return x && String(x.title || '').trim(); });
      window._lib = {items: items, at: Date.now()};
      try{ localStorage.setItem('bs_lib', JSON.stringify(items)); }catch(e){}
      return window._lib;
    })
    .catch(function(e){
      var c = (window._lib && window._lib.items && window._lib.items.length) ? window._lib.items : v9libCached();
      window._lib = {items: c || [], error: String(e && e.message || 'network'), stale: !!(c && c.length), at: Date.now()};
      return window._lib;
    })
    .then(function(l){ window._libLoading = null; return l; });
  return window._libLoading;
}
function v9libTile(x, i){
  var k = v9libKind(x);
  return '<div class="v2-card v9-lib-tile" onclick="openLibItem(window._libHome[' + i + '])">' +
    '<div class="k">' + v9libIcon(k, 14) + V9_LIB_ONE[k] + '</div>' +
    '<div class="t">' + v2esc(x.title) + '</div>' +
    (x.organ ? '<div class="o">' + v2esc(v9organLabel(x.organ)) + '</div>' : '') + '</div>';
}
// Блок «Библиотека» на главной резидента
function v9homeLibrary(){
  var L = window._lib;
  var h = '<div class="v2-h">Библиотека <span class="lnk" onclick="showPage(\'library\')">Все' + (L && L.items && L.items.length ? ' ' + L.items.length : '') + '</span></div>';
  if(!L){
    if(!window._v9LibTried){
      window._v9LibTried = true;
      loadLibrary(false).then(function(){ if(v9activeId() === 'home') renderResHome(); });
    }
    return h + '<div class="v2-card v2-empty">Загружаем библиотеку...</div>';
  }
  var items = L.items || [];
  if(!items.length){
    if(L.error) return h + '<div class="v2-card v2-empty">Библиотека сейчас недоступна.<br><span class="v3-txtbtn" style="color:#fff" onclick="window._lib=null;window._v9LibTried=false;renderResHome()">Повторить</span></div>';
    return h + '<div class="v2-card v2-empty">Скоро здесь появятся книги, инструменты и разборы клуба</div>';
  }
  window._libHome = items.slice(0, 4);
  return h + '<div class="v9-lib-grid">' + window._libHome.map(v9libTile).join('') + '</div>';
}
// Карточка «Библиотека BS» на главной резидента: белая, счётчики и три обложки последних книг
function v11libDate(x){ var t = Date.parse(x.updated || x.updatedAt || x.created || x.createdAt || x.date || ''); return isFinite(t) ? t : 0; }
function v11homeLib(){
  var L = window._lib;
  if(!L && !window._v9LibTried){
    window._v9LibTried = true;
    loadLibrary(false).then(function(){ if(v9activeId() === 'home') renderResHome(); });
  }
  var items = (L && L.items) || [];
  var cnt = {}; items.forEach(function(x){ var k = v9libKind(x); cnt[k] = (cnt[k] || 0) + 1; });
  var parts = [];
  if(cnt.book) parts.push(cnt.book + ' ' + v3plural(cnt.book, 'книга', 'книги', 'книг'));
  if(cnt.tool) parts.push(cnt.tool + ' ' + v3plural(cnt.tool, 'инструмент', 'инструмента', 'инструментов'));
  var rest = items.length - (cnt.book || 0) - (cnt.tool || 0);
  if(rest > 0) parts.push(rest + ' ' + v3plural(rest, 'материал', 'материала', 'материалов'));
  var sub = !L ? 'Загружаем...' : (items.length ? parts.join(' · ') : (L.error ? 'Сейчас недоступна, откройте позже' : 'Скоро появятся книги и инструменты'));
  // Последние книги: по дате, если она есть, иначе в порядке платформы
  var books = items.map(function(x, i){ return {x: x, i: i}; }).filter(function(o){ return v9libKind(o.x) === 'book'; });
  if(books.some(function(o){ return v11libDate(o.x); })) books.sort(function(a, b){ return v11libDate(b.x) - v11libDate(a.x); });
  var pick = books.map(function(o){ return o.x; });
  if(pick.length < 3) pick = pick.concat(items.filter(function(x){ return v9libKind(x) !== 'book'; })).slice(0, 3);
  pick = pick.slice(0, 3);
  window._libHome = pick;
  var tiles = '';
  if(pick.length){
    tiles = '<div class="v11-covers">' + pick.map(function(x, i){
      var k = v9libKind(x);
      return '<div class="v11-cover" onclick="event.stopPropagation();openLibItem(window._libHome[' + i + '])"><div class="k">' + v9libIcon(k, 12) + V9_LIB_ONE[k] + '</div><div class="t">' + v2esc(x.title) + '</div>' +
        (x.organ ? '<div class="o">' + v2esc(v9organLabel(x.organ)) + '</div>' : '') + '</div>';
    }).join('') + '</div>';
  }
  return '<div class="v11-lib" onclick="showPage(\'library\')"><div class="v3-row-sb" style="align-items:flex-start;gap:12px"><div style="min-width:0">' +
    '<div class="k">БАЗА ЗНАНИЙ КЛУБА</div><div class="t">Библиотека BS</div><div class="s">' + v2esc(sub) + '</div></div>' +
    (items.length ? '<div class="n num">' + items.length + '</div>' : '') + '</div>' + tiles +
    '<div class="v12-libcl" onclick="event.stopPropagation();v14openCat()"><div>+ 99 гайдов <span>разборы с цифрами и чек-листами</span></div>' + v3icon('chev', 16) + '</div>' +
    '<button onclick="event.stopPropagation();showPage(\'library\')">Открыть</button></div>';
}
function v9libFiltered(){
  var items = (window._lib && window._lib.items) || [];
  var f = window._libKind || 'all', q = String(window._libQ || '').toLowerCase().trim();
  return items.filter(function(x){
    if(f !== 'all' && v9libKind(x) !== f) return false;
    if(q){
      var hay = [x.title, x.short, x.why, x.organ, v9organLabel(x.organ), x.cat, x.example].concat(Array.isArray(x.how) ? x.how : [x.how]).join(' ').toLowerCase();
      if(hay.indexOf(q) < 0) return false;
    }
    return true;
  });
}
function v9libListHTML(){
  var list = v9libFiltered();
  window._libShown = list;
  if(!list.length) return '<div class="v2-empty">' + (window._libQ ? 'Ничего не нашли' : 'В этом разделе пока пусто') + '</div>';
  return list.map(function(x, i){
    var k = v9libKind(x);
    var meta = [V9_LIB_ONE[k], x.organ ? v9organLabel(x.organ) : '', x.cat || ''].filter(Boolean).join(' · ');
    return '<div class="v9-lib-row" onclick="openLibItem(window._libShown[' + i + '])"><div class="ic">' + v9libIcon(k, 18) + '</div>' +
      '<div class="m"><i>' + v2esc(meta) + '</i><b>' + v2esc(x.title) + '</b>' + (x.short ? '<span>' + v2esc(x.short) + '</span>' : '') + '</div></div>';
  }).join('');
}
function v9setLibKind(k){ window._libKind = k; renderLibrary(); }
function v9libSearch(v){ window._libQ = v; var el = document.getElementById('libList'); if(el) el.innerHTML = v9libListHTML(); }
function renderLibrary(){
  var el = document.getElementById('libraryContent');
  if(!el) return;
  // Библиотека только для резидентов и команды
  if(navRole() === 'lead'){ el.innerHTML = ''; return; }
  var L = window._lib;
  var n = L && L.items ? L.items.length : 0;
  var h = v3headBig(n ? n + ' ' + v3plural(n, 'материал', 'материала', 'материалов') + ' · синхронно с платформой' : 'База знаний клуба', 'Библиотека');
  h += v14libFeatured();
  if(!L){
    el.innerHTML = h + '<div class="loading"><div class="spinner"></div></div>';
    loadLibrary(false).then(function(){ if(v9activeId() === 'library') renderLibrary(); });
    return;
  }
  if(!n){
    el.innerHTML = h + '<div class="v2-card v2-empty" style="margin-top:16px">' + (L.error ? 'Не получилось загрузить библиотеку.<br><button class="v3-outline" style="margin-top:12px" onclick="window._lib=null;renderLibrary()">Повторить</button>'
      : '<div style="font-size:15px;font-weight:800;color:#fff;margin-bottom:4px">Пока пусто</div>Книги, инструменты и диагнозы появятся здесь, как только трекеры добавят их на платформе.') + '</div>';
    return;
  }
  var counts = {}; L.items.forEach(function(x){ var k = v9libKind(x); counts[k] = (counts[k] || 0) + 1; });
  var f = window._libKind || 'all';
  h += '<div class="v3-search" style="margin-top:16px">' + navIcon('search', 18) + '<input type="text" placeholder="Название, орган, тема" aria-label="Поиск" value="' + v2esc(window._libQ || '') + '" oninput="v9libSearch(this.value)"></div>';
  h += '<div class="v9-chips">' + V9_LIB_KINDS.filter(function(c){ return c[0] === 'all' || counts[c[0]]; }).map(function(c){
    return '<button class="' + (f === c[0] ? 'on' : '') + '" onclick="v9setLibKind(\'' + c[0] + '\')">' + c[1] + (c[0] !== 'all' ? ' · ' + counts[c[0]] : '') + '</button>';
  }).join('') + '</div>';
  if(L.error && L.stale) h += '<div class="v3-cap" style="margin:10px 4px 0">Показана сохранённая версия: сервер сейчас не ответил</div>';
  h += '<div class="v2-card" id="libList" style="margin-top:12px">' + v9libListHTML() + '</div>';
  el.innerHTML = h;
}
function v9libFileUrl(id){
  var s = String(id || '');
  if(/^https?:\/\//i.test(s)) return s;
  return APP_SERVER + 'file/' + encodeURIComponent(s) + '?' + new URLSearchParams({_tg: bsInitData()}).toString();
}
function v9list(v){
  if(!v) return [];
  if(Array.isArray(v)) return v.map(function(x){ return typeof x === 'string' ? x : (x && (x.text || x.title)) || ''; }).filter(function(x){ return String(x).trim(); });
  return String(v).split(/\n+/).map(function(x){ return x.replace(/^\s*(\d+[.)]|[-•])\s*/, '').trim(); }).filter(Boolean);
}
// Книга открывается сразу файлом; если файла ещё нет, показываем описание
function v10openBook(x){
  var f = x.file || x.fileId || x.file_id || '';
  var url = f ? v9libFileUrl(f) : (x.link || x.url || '');
  if(!url) return false;
  try{ if(tg && tg.openLink){ tg.openLink(url); return true; } }catch(e){}
  window.open(url, '_blank');
  return true;
}
function openLibItem(x, opts){
  if(!x) return;
  window._libItem = x;
  var k = v9libKind(x);
  if(k === 'book' && !(opts && opts.detail)){
    if(v10openBook(x)) return;
    showToast('Книга скоро появится');
  }
  document.getElementById('detailTitle').textContent = 'Библиотека';
  var h = '<div class="v3-lbl" style="color:#8C8C8C;display:flex;align-items:center;gap:8px">' + v9libIcon(k, 14) + v2esc([V9_LIB_ONE[k], x.organ ? v9organLabel(x.organ) : '', x.cat || ''].filter(Boolean).join(' · ').toUpperCase()) + '</div>' +
    '<h1 class="v3-h1" style="margin-top:8px;font-size:26px">' + v2esc(x.title) + '</h1>';
  if(x.short) h += '<p class="v3-p" style="margin-top:8px;font-size:15px">' + v2esc(x.short) + '</p>';
  if(k === 'book' && !(x.file || x.fileId || x.file_id || x.link || x.url)) h += '<div class="v2-card v2-empty" style="margin-top:14px">Книга скоро появится: файл добавят в библиотеку на платформе</div>';
  if(x.why) h += '<div class="v9-sec"><div class="h">Зачем</div><div class="v2-card v3-text" style="font-size:14px;line-height:1.5">' + v2esc(x.why) + '</div></div>';
  var how = v9list(x.how);
  if(how.length) h += '<div class="v9-sec"><div class="h">Как применять</div><div class="v2-card">' + how.map(function(s, i){ return '<div class="v9-step"><div class="n">' + (i + 1) + '</div><div>' + v2esc(s) + '</div></div>'; }).join('') + '</div></div>';
  if(x.example) h += '<div class="v9-sec"><div class="h">Пример</div><div class="v2-card v3-text" style="font-size:14px;line-height:1.5;white-space:pre-line">' + v2esc(x.example) + '</div></div>';
  var chk = v9list(x.check);
  if(chk.length) h += '<div class="v9-sec"><div class="h">Чек-лист</div><div class="v2-card">' + chk.map(function(s){ return '<div class="v9-step"><div class="c"></div><div>' + v2esc(s) + '</div></div>'; }).join('') + '</div></div>';
  var mats = [];
  var link = x.link || x.url;
  if(link) mats.push(['link', 'Ссылка', link]);
  if(x.video) mats.push(['video', 'Видео', x.video]);
  if(x.sample && /^https?:\/\//i.test(String(x.sample))) mats.push(['sample', 'Образец', x.sample]);
  if(x.file) mats.push(['file', x.fileName ? 'Файл' : 'Файл', v9libFileUrl(x.file)]);
  window._libMats = mats;
  if(x.sample && !/^https?:\/\//i.test(String(x.sample))) h += '<div class="v9-sec"><div class="h">Образец</div><div class="v2-card v3-text" style="font-size:14px;line-height:1.5;white-space:pre-line">' + v2esc(x.sample) + '</div></div>';
  if(mats.length){
    h += '<div class="v9-sec"><div class="h">Материалы</div><div class="v9-mats">' + mats.map(function(m, i){
      return '<button class="' + (i === 0 ? 'w' : '') + '" onclick="v9open(window._libMats[' + i + '][2])">' + v2esc(m[1]) + '</button>';
    }).join('') + '</div>' + (x.file && x.fileName ? '<div class="v3-cap" style="margin:8px 4px 0">Файл: ' + v2esc(x.fileName) + '</div>' : '') + '</div>';
  }
  if(!x.why && !how.length && !x.example && !chk.length && !mats.length && !x.short) h += '<div class="v2-card v2-empty" style="margin-top:16px">Описание появится позже</div>';
  document.getElementById('detailContent').innerHTML = h;
  showDetail();
}

// ═══ ЗАМЕРЫ: Gallup, диагностика по 7 органам, здоровье бизнеса (с платформы) ═══
function v9tests(){
  var d = window._myBoard || {};
  var b = d.board || {};
  return {tests: d.tests || b.tests || null, health: d.health || b.health || null};
}
function v9num(v){ var n = Number(String(v == null ? '' : v).replace(',', '.')); return isFinite(n) ? n : null; }
function v9fmt1(n){ return (Math.round(n * 10) / 10).toString().replace('.', ','); }
function v9barsHTML(obj, opts){
  opts = opts || {};
  var keys = Object.keys(obj || {}).filter(function(k){ return v9num(obj[k]) !== null; });
  if(!keys.length) return '';
  keys.sort(function(a, b){
    var ia = V9_ORGAN_ORDER.indexOf(v9organId(a)), ib = V9_ORGAN_ORDER.indexOf(v9organId(b));
    if(ia < 0) ia = 99; if(ib < 0) ib = 99;
    return ia - ib;
  });
  var vals = keys.map(function(k){ return v9num(obj[k]); });
  var max = opts.max || (Math.max.apply(null, vals) > 10 ? 100 : 10);
  return keys.map(function(k, i){
    var v = vals[i], pct = Math.max(0, Math.min(100, v / max * 100)), weak = pct < 50;
    return '<div class="v9-bar"><div class="l">' + v2esc(v9organLabel(k)) + '</div><div class="b"><i class="' + (weak ? 'r' : '') + '" style="width:' + pct + '%"></i></div>' +
      '<div class="v' + (weak ? ' red' : '') + '">' + (max === 10 ? v9fmt1(v) : Math.round(v)) + '</div></div>';
  }).join('');
}
function v9measuresHTML(){
  var T = v9tests(), tests = T.tests || {}, health = T.health;
  var none = '<div class="v9-none">Пока нет: пройдёте на разборе</div>';
  var loading = !window._myBoard;
  var h = '';
  // Здоровье бизнеса: с платформы, иначе собственный тест резидента
  var hb = health && typeof health === 'object' ? v9barsHTML(health) : '';
  var avg = '';
  if(hb){
    var hv = Object.keys(health).map(function(k){ return v9num(health[k]); }).filter(function(x){ return x !== null; });
    if(hv.length){ var mx = Math.max.apply(null, hv) > 10 ? 100 : 10; avg = v9fmt1(hv.reduce(function(s, x){ return s + x; }, 0) / hv.length) + (mx === 10 ? ' из 10' : ' из 100'); }
  } else {
    try{
      var dr = JSON.parse(localStorage.getItem('bs_diagnostic_result') || 'null');
      if(dr && dr.results && dr.results.length){
        var o = {}; dr.results.forEach(function(r){ o[r.id] = r.pct; });
        hb = v9barsHTML(o, {max: 100}); avg = dr.totalPct + ' из 100 · ваш тест';
      }
    }catch(e){}
  }
  h += '<div class="v2-card" style="margin-top:10px;cursor:pointer" onclick="v10openHealth()"><div class="v9-mhead v10-link"><b>Здоровье бизнеса</b><span>' + (avg || 'по органам') + navIcon('chev', 14) + '</span></div>' +
    (hb ? '<div class="v9-bars">' + hb + '</div>' : (loading ? '<div class="v9-none">Загружаем...</div>' : none)) + '</div>';
  // Диагностика по 7 органам
  var dg = tests.diag && typeof tests.diag === 'object' ? v9barsHTML(tests.diag.axes || tests.diag) : '';
  h += '<div class="v2-card" style="margin-top:10px"><div class="v9-mhead"><b>' + (tests.diag && (('Продукт' in tests.diag) || ('Делегирование' in tests.diag)) ? 'Диагностика по зонам' : 'Диагностика по 7 органам') + '</b><span>с платформы</span></div>' +
    (dg ? '<div class="v9-bars">' + dg + '</div>' : (loading ? '<div class="v9-none">Загружаем...</div>' : none)) + '</div>';
  // Gallup
  var g = tests.gallup || null;
  var tal = g ? (Array.isArray(g) ? g : (g.talents || [])) : [];
  tal = tal.map(function(t){ return typeof t === 'string' ? t : (t && (t.name || t.title)) || ''; }).filter(Boolean);
  var gh = '';
  if(tal.length){
    gh = '<div class="v9-talents">' + tal.map(function(t, i){ return '<span class="' + (i < 5 ? 'top' : '') + '"><i>' + (i + 1) + '</i>' + v2esc(t) + '</span>'; }).join('') + '</div>';
    if(g.note) gh += '<div class="v9-none" style="color:#BDBDBD;line-height:1.45;margin-top:-6px">' + v2esc(g.note) + '</div>';
  }
  h += '<div class="v2-card" style="margin-top:10px"><div class="v9-mhead"><b>Gallup · таланты</b><span>' + (tal.length ? 'топ-' + Math.min(5, tal.length) + ' выделены' : 'CliftonStrengths') + '</span></div>' +
    (gh || (loading ? '<div class="v9-none">Загружаем...</div>' : none)) + '</div>';
  return h;
}

// ═══ РАБОЧИЙ КАЛЕНДАРЬ: задачи и их сроки (доска цикла + задания + свои) ═══
var V9_MONTHS = ['Январь','Февраль','Март','Апрель','Май','Июнь','Июль','Август','Сентябрь','Октябрь','Ноябрь','Декабрь'];
function v9dkey(s){
  s = String(s || '').trim();
  if(!s) return '';
  var m = s.match(/^(\d{4})-(\d{2})-(\d{2})/);
  if(m) return m[1] + '-' + m[2] + '-' + m[3];
  m = s.match(/^(\d{1,2})\.(\d{1,2})(?:\.(\d{2,4}))?/);
  if(m){
    var y = m[3] ? (m[3].length === 2 ? '20' + m[3] : m[3]) : String(new Date().getFullYear());
    return y + '-' + String(m[2]).padStart(2, '0') + '-' + String(m[1]).padStart(2, '0');
  }
  return '';
}
function v9keyOf(d){ return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0'); }
function v9workItems(me){
  var out = [];
  ((cache.resTasks && cache.resTasks.tasks) || []).filter(function(t){ return t.name === me.name; }).forEach(function(t){
    var due = t.due || t.deadline || '';
    out.push({text: t.task, key: v9dkey(due || t.date), hasDue: !!due, done: t.status === 'Выполнена', src: t.isOwn ? 'своя' : 'от трекера', row: t.row});
  });
  var b = window._myBoard && window._myBoard.board;
  ((b && b.tasks) || []).forEach(function(t){
    var due = t.due || t.deadline || t.date || '';
    out.push({text: t.title, key: v9dkey(due), hasDue: !!due, done: !!t.done, src: 'цикл ' + ((b && b.cycle) || ''), organ: t.organ || '', cycle: true});
  });
  return out;
}
function v9calShift(n){
  var c = window._v9calM || new Date();
  window._v9calM = new Date(c.getFullYear(), c.getMonth() + n, 1);
  loadMyTasks();
}
function v9calPick(k){ window._v9calSel = k; loadMyTasks(); }
function v9workCalHTML(me){
  var items = v9workItems(me);
  var today = v9keyOf(new Date());
  var byDay = {};
  items.forEach(function(it){ if(it.key){ (byDay[it.key] = byDay[it.key] || []).push(it); } });
  var base = window._v9calM || new Date();
  var y = base.getFullYear(), mo = base.getMonth();
  var first = new Date(y, mo, 1), start = (first.getDay() + 6) % 7, days = new Date(y, mo + 1, 0).getDate();
  var sel = window._v9calSel || today;
  var h = '<div class="v2-card v9-cal" style="margin-top:14px"><div class="v9-cal-head">' +
    '<button aria-label="Предыдущий месяц" onclick="v9calShift(-1)">' + navIcon('back', 16) + '</button><b>' + V9_MONTHS[mo] + ' ' + y + '</b>' +
    '<button aria-label="Следующий месяц" onclick="v9calShift(1)">' + navIcon('chev', 16) + '</button></div><div class="v9-cal-grid">' +
    ['ПН','ВТ','СР','ЧТ','ПТ','СБ','ВС'].map(function(w){ return '<div class="wd">' + w + '</div>'; }).join('');
  for(var i = 0; i < start; i++) h += '<div class="v9-cal-d out"></div>';
  for(var d = 1; d <= days; d++){
    var k = y + '-' + String(mo + 1).padStart(2, '0') + '-' + String(d).padStart(2, '0');
    var list = byDay[k] || [];
    var dots = list.slice(0, 3).map(function(it){ return '<i class="' + (it.done ? 'g' : (k < today ? 'r' : '')) + '"></i>'; }).join('');
    h += '<div class="v9-cal-d' + (k === today ? ' today' : '') + (k === sel ? ' sel' : '') + '" onclick="v9calPick(\'' + k + '\')"><span>' + d + '</span><div class="dots">' + dots + '</div></div>';
  }
  h += '</div><div class="v9-legend"><span><i></i>в работе</span><span><i class="r"></i>просрочено</span><span><i class="g"></i>сделано</span></div></div>';
  // Задачи выбранного дня
  var sp = sel.split('-');
  var dl = byDay[sel] || [];
  window._v9DayTasks = dl;
  h += '<div class="v2-h">' + Number(sp[2]) + ' ' + (V3_MON[Number(sp[1]) - 1] || '').toLowerCase() + (sel === today ? ' · сегодня' : '') + ' · ' + dl.length + ' ' + v3plural(dl.length, 'задача', 'задачи', 'задач') + '</div>';
  if(dl.length){
    h += '<div class="v2-card">' + dl.map(function(it){
      var meta = (it.hasDue ? 'срок' : 'поставлена') + ' · ' + it.src + (it.organ ? ' · ' + it.organ : '');
      var canCheck = !it.done && !it.cycle && it.row;
      return v3taskRow(it.done, it.text, meta, canCheck ? 'markTaskDone(' + it.row + ')' : null);
    }).join('') + '</div>';
  } else {
    h += '<div class="v2-card v2-empty">На этот день задач нет</div>';
  }
  // Задачи цикла без срока
  var nod = items.filter(function(it){ return !it.key && !it.done; });
  if(nod.length){
    h += '<div class="v2-h">Без срока · ' + nod.length + '</div><div class="v2-card">' + nod.map(function(it){ return v3taskRow(false, it.text, it.src + (it.organ ? ' · ' + it.organ : ''), null); }).join('') + '</div>';
    h += '<div class="v3-cap" style="margin:10px 4px 0;line-height:1.45">Сроки задачам цикла ставит трекер на платформе. Как только срок появится, задача встанет в календарь.</div>';
  }
  return h;
}

// ═══ v10: ОДНИ ДАННЫЕ С ПЛАТФОРМОЙ: тесты резидента и рабочий календарь ═══
// Админ в режиме «как резидент» только смотрит: записи на платформу не уходят
function v10preview(){ return !!(isRealAdmin && viewAs === 'resident'); }
function v10q(){
  var q = new URLSearchParams({_tg: bsInitData()});
  if(isRealAdmin){ var me = getMyData(); if(me) q.set('name', me.name); }
  return q.toString();
}
function v10dateLabel(){ return new Date().toLocaleDateString('ru-RU', {day: '2-digit', month: 'short'}); }
function v10score(v){ var n = Number(v); if(!isFinite(n)) return null; return Math.round(Math.max(0, Math.min(10, n)) * 10) / 10; }

// Оси колеса баланса: подписи приложения → названия платформы
var V10_LIFE_MAP = {
  'бизнес':'Бизнес', 'здоровье':'Здоровье', 'спорт':'Спорт', 'семья':'Семья', 'окружение':'Окружение',
  'личные финансы':'Личные финансы', 'деньги':'Личные финансы', 'развитие':'Личностное развитие', 'личностное развитие':'Личностное развитие',
  'саморазвитие':'Личностное развитие', 'хобби':'Хобби', 'энергия':'Энергия', 'отдых':'Отдых', 'смысл':'Смысл'
};
var V10_DIAG_AXES = ['Стратегия', 'Финансы', 'Команда', 'Процессы', 'Продажи', 'Маркетинг', 'Продукт', 'Делегирование'];
function v10axesObj(axes, values, map){
  var o = {}, n = 0;
  (axes || []).forEach(function(a, i){
    var key = map ? map[String(a || '').trim().toLowerCase()] : String(a || '').trim();
    var v = v10score(values && values[i]);
    if(key && v !== null){ o[key] = v; n++; }
  });
  return n ? o : null;
}

// Очередь неотправленных тестов: повторяем при следующем открытии
function v10queue(){ try{ var a = JSON.parse(localStorage.getItem('bs_tests_queue') || '[]'); return Array.isArray(a) ? a : []; }catch(e){ return []; } }
function v10queueSave(a){ try{ if(a.length) localStorage.setItem('bs_tests_queue', JSON.stringify(a.slice(-20))); else localStorage.removeItem('bs_tests_queue'); }catch(e){} }
// 'ok' | 'drop' (сервер отказал навсегда) | 'retry'
function v10sendTests(body){
  return fetch(APP_SERVER + 'mytests?' + v10q(), {method: 'POST', headers: {'Content-Type': 'text/plain;charset=utf-8'}, body: JSON.stringify(body)})
    .then(function(r){
      if(r.ok) return r.json().then(function(j){ return j && j.ok ? 'ok' : 'retry'; }, function(){ return 'ok'; });
      return (r.status === 400 || r.status === 403) ? 'drop' : 'retry';
    })
    .catch(function(){ return 'retry'; });
}
function v10postTests(tests){
  if(navRole() !== 'resident' || !tests || !Object.keys(tests).length) return Promise.resolve(false);
  if(v10preview()){ showToast('Режим просмотра: на платформу не отправлено'); return Promise.resolve(false); }
  var body = {tests: tests, date: v10dateLabel()};
  if(!bsInitData()){ var q0 = v10queue(); q0.push(body); v10queueSave(q0); return Promise.resolve(false); }
  return v10sendTests(body).then(function(res){
    if(res === 'ok'){
      showToast('Сохранено, тренер видит на платформе');
      window._myBoardAt = 0;
      loadMyBoard(true);
      return true;
    }
    if(res === 'retry'){
      var q = v10queue(); q.push(body); v10queueSave(q);
      showToast('Сохранено. На платформу отправим при следующем открытии');
    }
    return false;
  });
}
function v10flushTests(){
  if(window._v10flushing || navRole() !== 'resident' || v10preview() || !bsInitData()) return;
  var q = v10queue();
  if(!q.length) return;
  window._v10flushing = true;
  var keep = [];
  q.reduce(function(p, body){
    return p.then(function(){ return v10sendTests(body).then(function(res){ if(res === 'retry') keep.push(body); }); });
  }, Promise.resolve()).then(function(){
    v10queueSave(keep);
    window._v10flushing = false;
    if(keep.length < q.length){ showToast('Сохранено, тренер видит на платформе'); window._myBoardAt = 0; }
  });
}
// Колесо из формы → платформа
function v10syncWheel(type){
  try{
    var d = _wheelData || {};
    if(type === 'dna'){
      var biz = (d.businesses || []).find(function(b){ return b.name === (window._wheelFormBiz || 'Основной'); }) || (d.businesses || [])[0];
      var vals = biz && biz.rows && biz.rows[0] ? biz.rows[0].values : window._v10lastForm;
      var o = v10axesObj(WHEEL_DNA_AXES, vals, null);
      if(o) return v10postTests({diag: o});
    } else {
      var axes = d.lifeAxes || WHEEL_LIFE_AXES;
      var lv = d.life && d.life[0] ? d.life[0].values : null;
      if(!lv){
        // Сервер не вернул свежий замер: ось «Бизнес» как среднее всех направлений
        var avgs = (d.businesses || []).map(function(b){ return b.rows && b.rows[0] ? wheelAvg(b.rows[0].values) : null; }).filter(function(x){ return x !== null; });
        lv = [avgs.length ? avgs.reduce(function(s, x){ return s + x; }, 0) / avgs.length : null].concat(window._v10lastForm || []);
      }
      var o2 = v10axesObj(axes, lv, V10_LIFE_MAP);
      if(o2) return v10postTests({wheel: o2});
    }
  }catch(e){ console.error('v10syncWheel', e); }
  return Promise.resolve(false);
}
// Диагностика по 7 органам → платформа: здоровье бизнеса и диагностика
function v10diagTests(){
  var dr = null;
  try{ dr = JSON.parse(localStorage.getItem('bs_diagnostic_result') || 'null'); }catch(e){}
  if(!dr || !dr.results || !dr.results.length) return null;
  var h = {};
  dr.results.forEach(function(r){ var lbl = V9_ORGAN_LABEL[r.id]; var v = v10score(Number(r.pct) / 10); if(lbl && v !== null) h[lbl] = v; });
  if(!Object.keys(h).length) return null;
  // Только здоровье органов: «diag» на платформе занят колесом бизнеса, не затираем его
  return {health: h};
}
function v10saveDiag(btn){
  var t = v10diagTests();
  if(!t){ showToast('Нет результата диагностики'); return; }
  if(btn){ btn.disabled = true; btn.textContent = 'Сохраняем...'; }
  v10postTests(t).then(function(ok){
    if(ok) window._v10diagSaved = 1;
    if(btn){ btn.disabled = false; btn.textContent = ok ? 'Сохранено в разбор' : 'Сохранить в разбор'; }
  });
}

// ═══ ПРОФИЛЬ: ДНК-причина и здоровье бизнеса ═══
function v10dnaCause(){
  var b = window._myBoard && window._myBoard.board;
  var list = ((b && b.diagnoses) || []).filter(function(x){ return x && String(x.type || '').toLowerCase() === 'dna' && String(x.title || '').trim(); });
  if(!list.length) return '';
  return '<div class="v2-card v10-dna">' + list.map(function(x){
    return '<div class="v10-dna-i"><div class="v3-lbl" style="color:#8C8C8C">ДНК ПРИЧИНА</div><div class="t">' + v2esc(x.title) + '</div>' +
      (x.desc ? '<div class="d">' + v2esc(x.desc) + '</div>' : '') + '</div>';
  }).join('') + '</div>';
}
function v10healthData(){
  var T = v9tests(), tests = T.tests || {};
  var o = null, src = '';
  if(T.health && typeof T.health === 'object' && Object.keys(T.health).length){ o = T.health; src = 'с платформы'; }
  else if(tests.diag && typeof tests.diag === 'object'){
    var dd = tests.diag.axes || tests.diag;
    if(Object.keys(dd).some(function(k){ return v9organId(k); })){ o = dd; src = 'диагностика с разбора'; }
  }
  if(!o){
    try{
      var dr = JSON.parse(localStorage.getItem('bs_diagnostic_result') || 'null');
      if(dr && dr.results && dr.results.length){ o = {}; dr.results.forEach(function(r){ o[r.id] = Number(r.pct) / 10; }); src = 'ваш тест'; }
    }catch(e){}
  }
  if(!o) return null;
  var keys = Object.keys(o).filter(function(k){ return v9num(o[k]) !== null; });
  if(!keys.length) return null;
  var max = Math.max.apply(null, keys.map(function(k){ return v9num(o[k]); })) > 10 ? 100 : 10;
  var norm = {};
  keys.forEach(function(k){ norm[k] = v9num(o[k]) * 10 / max; });
  var avg = keys.reduce(function(s, k){ return s + norm[k]; }, 0) / keys.length;
  var worst = keys.slice().sort(function(a, b){ return norm[a] - norm[b]; })[0];
  return {o: norm, avg: avg, worst: worst, src: src};
}
function v10openHealth(){
  var H = v10healthData();
  document.getElementById('detailTitle').textContent = 'Здоровье бизнеса';
  var h = '<div class="v3-lbl" style="color:#8C8C8C">ЗДОРОВЬЕ БИЗНЕСА</div><h1 class="v3-h1" style="margin-top:6px;font-size:26px">По органам</h1>';
  if(!H){
    h += '<div class="v2-card v2-empty" style="margin-top:16px">Замера пока нет. Пройдите диагностику или дождитесь разбора с трекером.</div>' +
      '<button class="v3-primary" style="margin-top:12px" onclick="closeDetail();showPage(\'diagnostic\')">Пройти диагностику</button>';
  } else {
    var pct = Math.round(H.avg * 10), vd = v9verdict(pct);
    h += '<div class="v2-card v9-score" style="margin-top:14px"><div class="big num' + (pct < 40 ? ' red' : '') + '">' + pct + '<small>/100</small></div>' +
      '<div style="min-width:0"><div class="vt">' + v2esc(vd[0]) + '</div><div class="vs">' + v2esc(vd[1]) + '</div></div></div>';
    h += '<div class="v2-card" style="margin-top:10px"><div class="v9-mhead"><b>Органы</b><span>' + v2esc(H.src) + '</span></div><div class="v9-bars">' + v9barsHTML(H.o, {max: 10}) + '</div></div>';
    var wv = H.o[H.worst];
    h += '<div class="v2-card v9-card" style="margin-top:10px' + (wv < 5 ? ';border-color:rgba(255,107,111,0.35)' : '') + '"><div class="tag' + (wv < 5 ? ' red' : '') + '">Слабое место</div>' +
      '<div class="ttl"><div class="v9-oico' + (wv < 5 ? ' r' : '') + '">' + v9organIcon(v9organId(H.worst) || 'brain', 22) + '</div><div>' + v2esc(v9organLabel(H.worst)) + '</div><div class="sc num' + (wv < 5 ? ' red' : '') + '">' + v9fmt1(wv) + '</div></div>' +
      '<div class="txt">Начните с этого органа: он сильнее всего тормозит остальные. Разберите его с трекером на ближайшей встрече.</div></div>';
    var T = v9tests(), dg = T.tests && T.tests.diag;
    if(dg && typeof dg === 'object' && H.src === 'с платформы'){
      var bars = v9barsHTML(dg.axes || dg);
      if(bars) h += '<div class="v2-card" style="margin-top:10px"><div class="v9-mhead"><b>Диагностика</b><span>с разбора</span></div><div class="v9-bars">' + bars + '</div></div>';
    }
    h += '<button class="v3-outline" style="margin-top:12px" onclick="closeDetail();showPage(\'diagnostic\')">Пройти диагностику</button>';
  }
  document.getElementById('detailContent').innerHTML = h;
  showDetail();
}

// ═══ РАБОЧИЙ КАЛЕНДАРЬ (= «Мой календарь» на платформе) ═══
var V10_TYPES = [['work', 'Работа', '#FFFFFF', '#000'], ['meet', 'Встреча', '#8B8B8B', '#fff'], ['focus', 'Фокус', '#3DD68C', '#000'], ['rest', 'Отдых', '#B9B9B9', '#000'], ['busy', 'Занято', '#FF4D4D', '#fff']];
var V10_T = {}; V10_TYPES.forEach(function(t){ V10_T[t[0]] = t; });
var V10_H0 = 7, V10_H1 = 23;
var V10_WD = ['ПН', 'ВТ', 'СР', 'ЧТ', 'ПТ', 'СБ', 'ВС'];
var V10_MS = ['янв', 'фев', 'мар', 'апр', 'мая', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];
window._v10cal = null;
function v10parse(str){
  var o = null;
  try{ o = typeof str === 'string' ? JSON.parse(str || '{}') : str; }catch(e){}
  if(!o || typeof o !== 'object' || Array.isArray(o)) o = {};
  if(!o.slots || typeof o.slots !== 'object' || Array.isArray(o.slots)) o.slots = {};
  if(!o.notes || typeof o.notes !== 'object' || Array.isArray(o.notes)) o.notes = {};
  return o;
}
function v10apply(v, op){
  if('t' in op){ if(op.t) v.slots[op.k] = op.t; else delete v.slots[op.k]; }
  if('n' in op){ if(op.n) v.notes[op.k] = op.n; else delete v.notes[op.k]; }
}
function v10calState(){
  var me = getMyData();
  var key = 'bs_mycal_' + (me ? me.name : '');
  var S = window._v10cal;
  if(!S || S.key !== key){
    var c = null; try{ c = JSON.parse(localStorage.getItem(key) || 'null'); }catch(e){}
    S = window._v10cal = {key: key, value: v10parse(c && c.value), version: (c && Number(c.version)) || 0, ops: (c && Array.isArray(c.ops)) ? c.ops : [], loadedAt: 0};
  }
  return S;
}
function v10calStore(){
  var S = window._v10cal; if(!S) return;
  try{ localStorage.setItem(S.key, JSON.stringify({value: S.value, version: S.version, ops: S.ops})); }catch(e){}
}
function v10calLoad(force){
  var S = v10calState();
  if(S.loading) return S.loading;
  if(!force && S.loadedAt && Date.now() - S.loadedAt < 30000) return Promise.resolve(S);
  if(!bsInitData()){ S.loadedAt = Date.now(); S.error = 'noinit'; return Promise.resolve(S); }
  S.loading = fetch(APP_SERVER + 'mycal?' + v10q())
    .then(function(r){ if(!r.ok) throw new Error('http ' + r.status); return r.json(); })
    .then(function(j){
      var v = v10parse(j.value);
      S.ops.forEach(function(op){ v10apply(v, op); });
      S.value = v; S.version = Number(j.version) || 0; S.error = null;
      v10calStore();
      (S.pv || []).forEach(function(op){ v10apply(S.value, op); });
      if(S.ops.length && !v10preview()) v10calSave();
    })
    .catch(function(e){ S.error = String((e && e.message) || 'network'); })
    .then(function(){ S.loading = null; S.loadedAt = Date.now(); v10calRefresh(); return S; });
  return S.loading;
}
function v10put(S){
  return fetch(APP_SERVER + 'mycal?' + v10q(), {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({value: JSON.stringify(S.value), version: S.version})});
}
async function v10calSave(){
  var S = window._v10cal;
  if(!S || v10preview() || !S.ops.length) return;
  if(S.saving){ S.again = true; return; }
  S.saving = true; v10calStatus();
  var sent = S.ops.length;
  try{
    var r = await v10put(S);
    if(r.status === 409){
      // Календарь изменили на платформе: берём их версию и заново кладём свои правки
      var j = {}; try{ j = await r.json(); }catch(e){}
      var v = v10parse(j.value);
      S.ops.forEach(function(op){ v10apply(v, op); });
      S.value = v; S.version = Number(j.version) || 0; sent = S.ops.length;
      r = await v10put(S);
    }
    if(r.ok){
      var j2 = {}; try{ j2 = await r.json(); }catch(e){}
      if(j2 && j2.version != null) S.version = Number(j2.version);
      S.ops = S.ops.slice(sent); S.error = null;
    } else S.error = 'http ' + r.status;
  }catch(e){ S.error = 'network'; }
  S.saving = false;
  v10calStore();
  if(S.again){ S.again = false; if(S.ops.length && !S.error){ v10calLater(); return; } }
  // Перерисовка: после слияния могли появиться правки с платформы
  if(!_v10p && !document.querySelector('.add-menu-overlay')) v10calRefresh(); else v10calStatus();
}
function v10calLater(){ var S = window._v10cal; if(!S) return; clearTimeout(S.timer); S.timer = setTimeout(v10calSave, 800); }
function v10calStatusText(){
  var S = window._v10cal;
  if(v10preview()) return 'Режим просмотра: правки видны только здесь, на платформу не уходят';
  if(!S || (!S.loadedAt && S.loading)) return 'Загружаем с платформы...';
  if(S.saving || (S.ops.length && !S.error)) return 'Сохраняем...';
  if(S.error && S.ops.length) return 'Не сохранено, повторим автоматически';
  if(S.error) return 'Нет связи с платформой, показана сохранённая версия';
  return 'Синхронно с платформой · Мой календарь';
}
function v10calStatus(){ var s = document.getElementById('v10calSt'); if(s){ s.textContent = v10calStatusText(); s.classList.toggle('red', !!(window._v10cal && window._v10cal.error && window._v10cal.ops.length)); } }
function v10dk(d){ return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0'); }
function v10fromKey(k){ var p = String(k).split('-'); return new Date(Number(p[0]), Number(p[1]) - 1, Number(p[2])); }
// Виды: день, неделя, 10 дней (цикл с сегодняшнего дня), месяц
var V10_MN = ['Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь', 'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь'];
function v10viewName(){ var v = window._v10view; return (v === 'day' || v === 'ten' || v === 'month') ? v : 'week'; }
function v10days(){
  var a = v10fromKey(window._v10day || v10dk(new Date()));
  var view = v10viewName(), out = [], i, d;
  if(view === 'day') return [a];
  if(view === 'ten'){ for(i = 0; i < 10; i++){ d = new Date(a); d.setDate(a.getDate() + i); out.push(d); } return out; }
  if(view === 'month'){
    var n = new Date(a.getFullYear(), a.getMonth() + 1, 0).getDate();
    for(i = 1; i <= n; i++) out.push(new Date(a.getFullYear(), a.getMonth(), i));
    return out;
  }
  var m = new Date(a); m.setDate(m.getDate() - (m.getDay() + 6) % 7);
  for(i = 0; i < 7; i++){ d = new Date(m); d.setDate(m.getDate() + i); out.push(d); }
  return out;
}
// Встречи с трекером из расписания клуба: «YYYY-MM-DD|H» → встреча
function v10meets(me){
  var o = {};
  (cache.schedule || []).forEach(function(ev){
    if(!ev || ev.res !== me.name) return;
    var dk = v9dkey(ev.date), h = parseInt(String(ev.time || ''), 10);
    if(dk && isFinite(h)) o[dk + '|' + h] = ev;
  });
  return o;
}
function v10dueByDay(me){
  var o = {};
  v9workItems(me).forEach(function(it){ if(it.key && it.hasDue) (o[it.key] = o[it.key] || []).push(it); });
  return o;
}
function v10calShift(n){
  var a = v10fromKey(window._v10day || v10dk(new Date()));
  var view = v10viewName();
  if(view === 'month') a = new Date(a.getFullYear(), a.getMonth() + n, 1);
  else a.setDate(a.getDate() + n * (view === 'day' ? 1 : (view === 'ten' ? 10 : 7)));
  window._v10day = v10dk(a); v10calRefresh();
}
function v10calToday(){ window._v10day = v10dk(new Date()); v10calRefresh(); }
function v10calView(v){
  // 10 дней: цикл всегда от сегодняшнего дня
  if(v === 'ten' || (v !== 'day' && v !== v10viewName())) window._v10day = v10dk(new Date());
  window._v10view = v; window._v10from = null; v10calRefresh();
}
function v10calOpenDay(k){ window._v10from = v10viewName() === 'month' ? 'month' : null; window._v10day = k; window._v10view = 'day'; v10calRefresh(); }
function v10calBackMonth(){ window._v10view = 'month'; window._v10from = null; v10calRefresh(); }
function v10calType(t){ window._v10type = t; v10calRefresh(); }
// Тап (click) ставит/снимает выбранный тип; долгое нажатие (таймер или contextmenu): заметка.
// Пока палец на ячейке, календарь не перерисовывается: иначе ячейка под пальцем подменяется и тап теряется
var _v10p = null, _v10lp = null, _v10pend = false;
function v10pd(e, k){
  if(e && e.button > 0) return;
  if(_v10p) clearTimeout(_v10p.t);
  _v10p = {k: k, x: e ? e.clientX : 0, y: e ? e.clientY : 0};
  _v10p.t = setTimeout(function(){
    if(!_v10p || _v10p.k !== k) return;
    _v10p = null;
    v10longPress(k);
  }, 520);
}
function v10pm(e){ if(_v10p && (Math.abs(e.clientX - _v10p.x) > 10 || Math.abs(e.clientY - _v10p.y) > 10)){ clearTimeout(_v10p.t); _v10p = null; } }
function v10pu(){
  if(_v10p){ clearTimeout(_v10p.t); _v10p = null; }
  // click приходит сразу после pointerup; отложенная перерисовка только после него
  setTimeout(v10flushPend, 400);
}
function v10flushPend(){ if(_v10pend && !_v10p){ _v10pend = false; v10calRefresh(); } }
function v10longPress(k){
  _v10lp = {k: k, at: Date.now()};
  try{ tg.HapticFeedback.impactOccurred('medium'); }catch(x){}
  v10noteSheet(k);
}
function v10ctx(e, k){
  try{ e.preventDefault(); }catch(x){}
  if(_v10p){ clearTimeout(_v10p.t); _v10p = null; }
  if(!(_v10lp && _v10lp.k === k && Date.now() - _v10lp.at < 1500)) v10longPress(k);
  return false;
}
function v10click(e, k){
  if(_v10lp && _v10lp.k === k && Date.now() - _v10lp.at < 2500){ _v10lp = null; return; }
  _v10lp = null;
  if(_v10p){ clearTimeout(_v10p.t); _v10p = null; }
  v10tap(k);
}
function v10tap(k){
  var me = getMyData(); if(!me) return;
  var ev = v10meets(me)[k];
  if(ev){ showToast('Встреча BS · ' + (ev.time || '') + (v3isOnline(ev) ? ' · онлайн' : ' · офлайн')); return; }
  var S = v10calState();
  var sel = window._v10type || 'work';
  var op = {k: k, t: S.value.slots[k] === sel ? null : sel};
  v10apply(S.value, op);
  if(v10preview()){
    // Админ смотрит глазами резидента: правки видны на экране, но на платформу не уходят
    (S.pv = S.pv || []).push(op);
    if(!S.pvTold){ S.pvTold = true; showToast('Режим просмотра: правки видны только здесь'); }
  } else { S.ops.push(op); v10calStore(); }
  try{ tg.HapticFeedback.selectionChanged(); }catch(x){}
  _v10pend = false;
  v10calRefresh();
  if(!v10preview()) v10calLater();
}
function v10noteSheet(k){
  if(document.querySelector('.add-menu-overlay')) return;
  var me = getMyData(); if(me && v10meets(me)[k]){ v10tap(k); return; }
  var S = v10calState();
  var p = k.split('|'), d = v10fromKey(p[0]);
  var cur = S.value.notes[k] || '';
  var t = S.value.slots[k];
  var m = document.createElement('div');
  m.className = 'add-menu-overlay show';
  m.onclick = function(e){ if(e.target === m) m.remove(); };
  m.innerHTML = '<div class="add-menu">' +
    '<div class="v3-row-sb" style="margin-bottom:12px"><div class="add-menu-title" style="margin:0;text-align:left">Заметка · ' + d.getDate() + ' ' + V10_MS[d.getMonth()] + ', ' + p[1] + ':00' + (t && V10_T[t] ? ' · ' + V10_T[t][1] : '') + '</div>' +
    '<button class="v3-sheet-x" data-x="1" aria-label="Закрыть">' + navIcon('x', 16, 2) + '</button></div>' +
    '<textarea id="v10note" class="field-input" rows="3" maxlength="200" placeholder="Например: созвон с поставщиком" style="width:100%;resize:none;min-height:84px">' + v2esc(cur) + '</textarea>' +
    '<button class="v3-primary" data-s="1" style="margin-top:12px">Сохранить</button>' +
    (cur ? '<button class="v3-outline" data-d="1" style="margin-top:8px">Удалить заметку</button>' : '') +
  '</div>';
  document.body.appendChild(m);
  function done(n){
    var op = {k: k, n: n};
    v10apply(S.value, op);
    if(v10preview()) (S.pv = S.pv || []).push(op); else { S.ops.push(op); v10calStore(); }
    m.remove(); v10calRefresh(); if(!v10preview()) v10calLater();
  }
  m.querySelector('[data-x]').onclick = function(){ m.remove(); };
  m.querySelector('[data-s]').onclick = function(){ done(String(m.querySelector('#v10note').value || '').trim().slice(0, 200)); };
  var del = m.querySelector('[data-d]'); if(del) del.onclick = function(){ done(''); };
  setTimeout(function(){ try{ m.querySelector('#v10note').focus(); }catch(e){} }, 80);
}
function v10cellAttrs(k){
  return ' data-k="' + k + '" onclick="v10click(event,\'' + k + '\')" onpointerdown="v10pd(event,\'' + k + '\')" onpointerup="v10pu()" onpointermove="v10pm(event)" onpointercancel="v10pu()" oncontextmenu="return v10ctx(event,\'' + k + '\')"';
}
// Часы по типам за день: для месяца и счётчиков
function v10dayStat(S, meets, dk){
  var o = {bs: 0};
  for(var h = V10_H0; h <= V10_H1; h++){
    var k = dk + '|' + h;
    if(meets[k]){ o.bs++; continue; }
    var t = S.value.slots[k]; if(t && V10_T[t]) o[t] = (o[t] || 0) + 1;
  }
  return o;
}
function v10gridHTML(S, days, meets, due, today){
  var n = days.length;
  var h = '<div class="v2-card v10-grid' + (n > 7 ? ' ten' : '') + '" style="grid-template-columns:' + (n > 7 ? '26px' : '30px') + ' repeat(' + n + ',minmax(0,1fr))"><div class="v10-hd"></div>' + days.map(function(d){
    var dk = v10dk(d), c = (due[dk] || []).length, open = (due[dk] || []).filter(function(it){ return !it.done; }).length;
    return '<div class="v10-hd' + (dk === today ? ' today' : '') + '" onclick="v10calOpenDay(\'' + dk + '\')"><span>' + V10_WD[(d.getDay() + 6) % 7] + '</span><b>' + d.getDate() + '</b>' +
      '<em>' + (c ? '<i class="' + (open ? (dk < today ? 'r' : '') : 'g') + '">' + c + '</i>' : '') + '</em></div>';
  }).join('');
  for(var hr = V10_H0; hr <= V10_H1; hr++){
    h += '<div class="v10-hr">' + hr + '</div>';
    days.forEach(function(d){
      var k = v10dk(d) + '|' + hr, t = S.value.slots[k], ev = meets[k], note = S.value.notes[k];
      if(ev){ h += '<div class="v10-c bs"' + v10cellAttrs(k) + '>BS</div>'; return; }
      var T = t && V10_T[t];
      h += '<div class="v10-c' + (T ? ' f' : '') + '"' + (T ? ' style="background:' + T[2] + ';color:' + T[3] + '"' : '') + v10cellAttrs(k) + '>' + (note ? '<s></s>' : '') + '</div>';
    });
  }
  return h + '</div>';
}
function v10monthHTML(S, days, meets, due, today){
  var first = days[0], lead = (first.getDay() + 6) % 7;
  var h = '<div class="v2-card v10-month"><div class="v10-mw">' + V10_WD.map(function(w){ return '<span>' + w + '</span>'; }).join('') + '</div><div class="v10-mg">';
  for(var i = 0; i < lead; i++) h += '<div class="v10-md empty"></div>';
  days.forEach(function(d){
    var dk = v10dk(d), st = v10dayStat(S, meets, dk);
    var dl = due[dk] || [], open = dl.filter(function(it){ return !it.done; }).length;
    var bars = V10_TYPES.filter(function(t){ return st[t[0]]; }).map(function(t){
      return '<i style="background:' + t[2] + ';width:' + Math.min(100, Math.round(st[t[0]] / 6 * 100)) + '%"></i>';
    });
    if(st.bs) bars.unshift('<i class="bs" style="width:' + Math.min(100, Math.round(st.bs / 6 * 100)) + '%"></i>');
    var hrs = 0; V10_TYPES.forEach(function(t){ if(t[0] !== 'rest') hrs += st[t[0]] || 0; }); hrs += st.bs;
    h += '<div class="v10-md' + (dk === today ? ' today' : '') + (dk < today ? ' past' : '') + '" onclick="v10calOpenDay(\'' + dk + '\')">' +
      '<div class="t"><b>' + d.getDate() + '</b>' + (dl.length ? '<em class="' + (open ? (dk < today ? 'r' : '') : 'g') + '">' + dl.length + '</em>' : '') + '</div>' +
      '<div class="bars">' + bars.slice(0, 4).join('') + '</div>' + (hrs ? '<div class="h">' + hrs + ' ч</div>' : '') + '</div>';
  });
  return h + '</div></div>';
}
function v10calInner(me){
  var S = v10calState();
  var view = v10viewName();
  var days = v10days(), today = v10dk(new Date());
  var meets = v10meets(me), due = v10dueByDay(me);
  var sel = window._v10type || 'work';
  // Счётчики за видимый период
  var busyH = 0, focusH = 0;
  days.forEach(function(d){
    var st = v10dayStat(S, meets, v10dk(d));
    busyH += st.bs; V10_TYPES.forEach(function(t){ if(t[0] !== 'rest') busyH += st[t[0]] || 0; });
    focusH += st.focus || 0;
  });
  var a = days[0], z = days[days.length - 1];
  var range = view === 'day'
    ? V10_WD[(a.getDay() + 6) % 7].charAt(0) + V10_WD[(a.getDay() + 6) % 7].charAt(1).toLowerCase() + ', ' + a.getDate() + ' ' + V10_MS[a.getMonth()]
    : (view === 'month' ? V10_MN[a.getMonth()] + ' ' + a.getFullYear()
    : a.getDate() + (a.getMonth() !== z.getMonth() ? ' ' + V10_MS[a.getMonth()] : '') + ' – ' + z.getDate() + ' ' + V10_MS[z.getMonth()]);
  var h = '<div class="v10-st' + (S.error && S.ops.length ? ' red' : '') + '" id="v10calSt">' + v2esc(v10calStatusText()) + '</div>';
  h += '<div class="v3-seg v10-vseg">' + [['day', 'День'], ['week', 'Неделя'], ['ten', '10 дней'], ['month', 'Месяц']].map(function(x){
    return '<button class="' + (view === x[0] ? 'on' : '') + '" onclick="v10calView(\'' + x[0] + '\')">' + x[1] + '</button>';
  }).join('') + '</div>';
  h += '<div class="v10-bar"><div class="v10-sum"><b>' + v2esc(range) + '</b><span>Занято ' + busyH + ' ч · фокус ' + focusH + ' ч</span></div>' +
    '<div class="v10-nav"><button aria-label="Назад" onclick="v10calShift(-1)">' + navIcon('back', 16) + '</button><button class="t" onclick="v10calToday()">Сегодня</button><button aria-label="Вперёд" onclick="v10calShift(1)">' + navIcon('chev', 16) + '</button></div></div>';
  if(view === 'day' && window._v10from === 'month') h += '<div class="v3-txtbtn v10-tomonth" onclick="v10calBackMonth()">‹ ' + V10_MN[a.getMonth()] + '</div>';
  if(view !== 'month'){
    h += '<div class="v10-types">' + V10_TYPES.map(function(t){
      return '<button class="' + (sel === t[0] ? 'on' : '') + '" onclick="v10calType(\'' + t[0] + '\')"><i style="background:' + t[2] + '"></i>' + t[1] + '</button>';
    }).join('') + '</div>';
  }
  if(view === 'week' || view === 'ten'){
    h += v10gridHTML(S, days, meets, due, today);
  } else if(view === 'month'){
    h += v10monthHTML(S, days, meets, due, today);
  } else {
    var dk0 = v10dk(days[0]);
    h += '<div class="v2-card v10-day">';
    for(var hd = V10_H0; hd <= V10_H1; hd++){
      var k2 = dk0 + '|' + hd, t2 = S.value.slots[k2], ev2 = meets[k2], note2 = S.value.notes[k2];
      var T2 = t2 && V10_T[t2];
      h += '<div class="v10-dr"><div class="v10-hr">' + String(hd).padStart(2, '0') + ':00</div>';
      if(ev2) h += '<div class="v10-c bs"' + v10cellAttrs(k2) + '><b>BS</b><span>' + (v3isOnline(ev2) ? 'Онлайн-трекинг' : 'Офлайн-встреча') + ' · ' + v2esc(ev2.time || '') + '</span></div>';
      else h += '<div class="v10-c' + (T2 ? ' f' : '') + '"' + (T2 ? ' style="background:' + T2[2] + ';color:' + T2[3] + '"' : '') + v10cellAttrs(k2) + '>' +
        (T2 ? '<b>' + T2[1] + '</b>' : '') + (note2 ? '<span>' + v2esc(note2) + '</span>' : '') + '</div>';
      h += '</div>';
    }
    h += '</div>';
    var dl = due[dk0] || [];
    if(dl.length){
      h += '<div class="v2-h">Срок в этот день · ' + dl.length + '</div><div class="v2-card">' + dl.map(function(it){
        return v3taskRow(it.done, it.text, it.src + (it.organ ? ' · ' + it.organ : ''), (!it.done && !it.cycle && it.row) ? 'markTaskDone(' + it.row + ')' : null);
      }).join('') + '</div>';
    }
  }
  if(view === 'month'){
    h += '<div class="v10-legend">' + V10_TYPES.map(function(t){ return '<span><i class="sq" style="background:' + t[2] + '"></i>' + t[1] + '</span>'; }).join('') + '<span><i class="bs">BS</i>встреча</span></div>';
    h += '<div class="v3-cap" style="margin:8px 4px 0;line-height:1.45">Полоски показывают часы по типам. Нажмите на день, чтобы расписать его по часам.</div>';
  } else {
    h += '<div class="v10-legend"><span><i class="bs">BS</i>встреча клуба</span><span><i class="dot"></i>срок задачи</span><span><i class="nt"></i>заметка</span></div>';
    h += '<div class="v3-cap" style="margin:8px 4px 0;line-height:1.45">Выберите тип и нажмите на час, повторное нажатие снимает. Долгое нажатие добавляет заметку. Всё сразу видно на платформе в «Мой календарь».</div>';
  }
  return h;
}
function v10calHTML(me){
  v10calState();
  v10calLoad(false);
  return '<div id="v10cal" style="margin-top:14px">' + v10calInner(me) + '</div>';
}
function v10calRefresh(){
  var el = document.getElementById('v10cal'); if(!el) return;
  if(_v10p){ _v10pend = true; return; }
  var me = getMyData(); if(!me) return;
  el.innerHTML = v10calInner(me);
}
// При открытии приложения: досылаем тесты и календарь, которые не ушли (ждём, пока загрузятся данные)
(function(){
  var tries = 0;
  var iv = setInterval(function(){
    tries++;
    var me = null; try{ me = getMyData(); }catch(e){}
    if(!me && tries < 20) return;
    clearInterval(iv);
    if(!me) return;
    try{ v10flushTests(); }catch(e){}
    try{ if(navRole() === 'resident' && !v10preview()){ var S = v10calState(); if(S.ops.length) v10calLoad(true); } }catch(e){}
  }, 3000);
})();

// ═══ РЕЗИДЕНТ: МОЙ РАЗБОР (доска с платформы) ═══
window._myBoard = null;
window._myBoardAt = 0;
async function loadMyBoard(force){
  if(!force && window._myBoard && Date.now() - window._myBoardAt < 60000) return window._myBoard;
  var init = bsInitData();
  if(!init) return window._myBoard;
  var q = new URLSearchParams({_tg: init});
  if(isRealAdmin){ var me = getMyData(); if(me) q.set('name', me.name); }
  try{
    var r = await fetch(APP_SERVER + 'myboard?' + q.toString());
    if(r.ok){ window._myBoard = await r.json(); window._myBoardAt = Date.now(); }
    else window._myBoard = window._myBoard || {error: r.status};
  }catch(e){ window._myBoard = window._myBoard || {error: 'network'}; }
  return window._myBoard;
}
function v3toggleTool(i){
  var d = document.getElementById('v3tool' + i);
  if(d) d.style.display = d.style.display === 'none' ? 'block' : 'none';
}
function renderMyBoard(){
  var el = document.getElementById('myboardContent');
  if(!el) return;
  var d = window._myBoard;
  var head = function(sub){ return v3headBig(sub, 'Мой разбор'); };
  if(!d){
    el.innerHTML = head('Доска с трекером') + '<div class="loading"><div class="spinner"></div></div>';
    loadMyBoard(false).then(function(){ if(window._myBoard) renderMyBoard(); });
    return;
  }
  if(d.error){
    el.innerHTML = head('Доска с трекером') + '<div class="v2-card v2-empty" style="margin-top:16px">Не получилось загрузить разбор.<br><button class="v3-outline" style="margin-top:12px" onclick="window._myBoard=null;renderMyBoard()">Повторить</button></div>';
    return;
  }
  var b = d.board;
  if(!b){
    el.innerHTML = head('Доска с трекером') + '<div class="v2-card v2-empty" style="margin-top:16px"><div style="font-size:16px;font-weight:800;color:#fff;margin-bottom:6px">Разбора пока нет</div>Он появится после первой встречи с трекером: диагноз, стратегия, инструменты и задачи на цикл.</div>';
    return;
  }
  var upd = b.updated ? new Date(b.updated).toLocaleDateString('ru-RU', {day:'numeric', month:'long'}) : '';
  var dn = b.tasks.filter(function(t){ return t.done; }).length;
  var on = b.tasks.length ? Math.round(dn / b.tasks.length * 10) : 0;
  var h = head('Цикл ' + b.cycle + (upd ? ' · обновлён ' + upd : ''));
  h += '<div class="v2-seg" style="margin:16px 0 0">' + Array.apply(null, Array(10)).map(function(_, i){ return '<i' + (i < on ? ' class="on"' : '') + '></i>'; }).join('') + '</div>';
  if(b.pointA || b.pointB){
    h += '<div class="v2-card" style="display:grid;grid-template-columns:1fr 1fr;margin-top:16px">' +
           '<div style="padding:14px 16px;border-right:1px solid #1F1F1F"><div class="v3-lbl" style="color:#8C8C8C">ТОЧКА А</div><div style="margin-top:6px;font-size:14px;font-weight:700;line-height:1.35">' + v2esc(b.pointA||'·') + '</div></div>' +
           '<div style="padding:14px 16px"><div class="v3-lbl" style="color:#8C8C8C">ТОЧКА Б</div><div style="margin-top:6px;font-size:14px;font-weight:700;line-height:1.35">' + v2esc(b.pointB||'·') + '</div></div>' +
         '</div>';
  }
  if(b.diagnoses.length){
    var organs = []; b.diagnoses.forEach(function(x){ if(x.organ && organs.indexOf(x.organ) < 0) organs.push(x.organ); });
    h += '<div class="v2-h">Диагноз</div><div class="v2-card" style="padding:16px 18px">' +
         (organs.length ? '<div>' + organs.map(function(o,i){ return '<span class="v2-chip' + (i === 0 ? ' on' : '') + '">' + v2esc(o) + '</span>'; }).join('') + '</div>' : '') +
         b.diagnoses.map(function(x){ return '<div style="margin-top:8px"><div style="font-size:18px;font-weight:800;line-height:1.3">' + v2esc(x.title) + '</div>' + (x.desc ? '<div style="font-size:13px;color:#9A9A9A;line-height:1.5;margin-top:3px">' + v2esc(x.desc) + '</div>' : '') + '</div>'; }).join('') +
         '</div>';
  }
  if(b.strategy){ h += '<div class="v2-h">Стратегия цикла</div><div class="v2-strat">' + v2esc(b.strategy) + '</div>'; }
  if(b.tools.length){
    h += '<div class="v2-h">Инструменты</div><div style="display:flex;flex-direction:column;gap:6px">' + b.tools.map(function(x, i){
      return '<div class="v2-card v2-row" style="border-bottom:1px solid #1F1F1F" onclick="v3toggleTool(' + i + ')"><div class="num" style="font-size:11px;color:#9A9A9A;width:18px">' + String(i+1).padStart(2,'0') + '</div>' +
        '<div class="m"><b>' + v2esc(x.title) + '</b>' + (x.desc ? '<span id="v3tool' + i + '" style="display:none;line-height:1.45">' + v2esc(x.desc) + '</span>' : '') + '</div>' +
        '<div class="ic" style="color:#6E6E6E">' + navIcon('chev',16) + '</div></div>';
    }).join('') + '</div>';
  }
  if(!b.diagnoses.length && !b.strategy && !b.tools.length && !b.tasks.length){
    h += '<div class="v2-card v2-empty" style="margin-top:16px">Трекер ещё заполняет доску. Диагноз и стратегия появятся здесь после встречи.</div>';
  }
  h += '<button class="v3-outline" style="margin-top:12px" onclick="window._v3TaskSeg=\'tasks\';showPage(\'mytasks\')">Задачи цикла' + (b.tasks.length ? ' · ' + dn + ' из ' + b.tasks.length : '') + '</button>';
  el.innerHTML = h;
}

// Колесо и диагностика резидента: раньше открывались чипами сверху, теперь ссылками внизу «Разбора»
function v4boardLinks(){
  return '<div class="v2-h">Замеры</div><div class="v2-card">' +
    v3row('sum', 'Колесо баланса', 'Замеры бизнеса и жизни', "showPage('wheel')") +
    v3row('board', 'Диагностика', 'Тест здоровья бизнеса, 7 органов', "showPage('diagnostic')") +
  '</div>';
}
(function(){
  var base = renderMyBoard;
  renderMyBoard = function(){
    base.apply(this, arguments);
    var el = document.getElementById('myboardContent');
    if(el && !el.querySelector('.loading') && !el.querySelector('.v4-blinks')){
      var d = document.createElement('div'); d.className = 'v4-blinks'; d.innerHTML = v4boardLinks(); el.appendChild(d);
    }
  };
})();

// ═══ АДМИН: БЫСТРЫЙ ВВОД «+» ═══
function openQuickMenu(){
  if(document.querySelector('.add-menu-overlay')) return;
  var menu = document.createElement('div');
  menu.className = 'add-menu-overlay';
  menu.onclick = function(e){ if(e.target === menu) closeAddMenu(); };
  function item(act, icon, t, sub, on){
    return '<button class="v3-qrow' + (on ? ' on' : '') + '" data-q="' + act + '">' + navIcon(icon, 22, 2) + '<div class="m"><b>' + t + '</b><span>' + sub + '</span></div>' + navIcon('chev', 16) + '</button>';
  }
  menu.innerHTML = '<div class="add-menu">' +
    '<div class="v3-row-sb" style="margin-bottom:14px"><div class="add-menu-title" style="margin:0">Быстрый ввод</div>' +
    '<button class="v3-sheet-x" data-q="cancel" aria-label="Закрыть">' + navIcon('x', 16, 2) + '</button></div>' +
    item('in', 'plus', 'Приход и расход', 'Оплата резидента или трата, тип выберите внутри', true) +
    item('fine', 'alert', 'Штраф', 'Резиденту, с суммой и причиной') +
    item('meet', 'cal', 'Встреча', 'Онлайн, офлайн-день или мероприятие') +
    item('res', 'club', 'Новый резидент', 'Добавить в клуб') +
    '</div>';
  menu.querySelectorAll('[data-q]').forEach(function(btn){
    btn.addEventListener('click', function(e){
      e.stopPropagation(); e.preventDefault();
      var a = btn.getAttribute('data-q');
      closeAddMenu();
      if(a === 'cancel') return;
      setTimeout(function(){
        if(a === 'in'){ openModal('payModal'); initPayModal(); }
        else if(a === 'out'){ openModal('payModal'); initPayModal(); setPayType('expense'); lockPayHeight(true); }
        else if(a === 'fine'){ openModal('fineModal'); initFineModal(); }
        else if(a === 'meet'){ openAddMenu(); }
        else if(a === 'res'){ openModal('addResidentModal'); initAddResident(); }
      }, 240);
    });
  });
  document.body.appendChild(menu);
  setTimeout(function(){ menu.classList.add('show'); }, 10);
}

// ═══ КЛУБ: карточка «О проекте» над списком резидентов ═══
// Карточка «О проекте» теперь рисуется внутри экрана «Клуб»
function ensureAboutCard(){
  var card = document.getElementById('v2AboutCard');
  if(card) card.remove();
}

function renderDashboard(){
  var box = document.getElementById('dashboardContent');
  if(!box) return;
  const residents = v3activeResidents();
  const fines = cache.fines||[];
  const unpaid = fines.filter(f=>f.status==='Не оплатил');
  const unpaidSum = unpaid.reduce((s,f)=>s+(Number(f.amount)||0),0);
  const today = new Date().toLocaleDateString('ru-RU');

  // ИСТОЧНИК ФИНАНСОВ: свежие данные из пакета всегда важнее локального кеша
  if(cache.monthlyPL && cache.monthlyPL.months){
    window._monthlyPL = cache.monthlyPL;
    try{ localStorage.setItem('bs_monthly_pl', JSON.stringify(cache.monthlyPL)); }catch(e){}
  } else if(!window._monthlyPL){
    try{
      var cachedPL = localStorage.getItem('bs_monthly_pl');
      if(cachedPL) window._monthlyPL = JSON.parse(cachedPL);
    }catch(e){}
    if(!window._plLoading){
      window._plLoading = true;
      callAction('getMonthlyPL', {}).then(function(r){
        window._plLoading = false;
        if(r && r.ok){
          window._monthlyPL = r;
          try{ localStorage.setItem('bs_monthly_pl', JSON.stringify(r)); }catch(e){}
          renderDashboard();
        }
      });
    }
  }

  var h = v3headMark('', 'Сводка');

  const mp = window._monthlyPL;
  if(mp && mp.months){
    var defIdx = (typeof mp.currentMonthIdx === 'number') ? mp.currentMonthIdx : new Date().getMonth();
    const curIdx = (window._selectedMonth != null) ? window._selectedMonth : defIdx;
    const m = mp.months[curIdx] || mp.months[defIdx] || mp.months[0];
    var chips = mp.months.map(function(mm, i){
      var isNeg = mm.profit < 0, val;
      if(!mm.hasData) val = '·';
      else if(Math.abs(mm.profit) >= 1000000) val = (isNeg ? '−' : '') + (Math.abs(mm.profit)/1000000).toFixed(1).replace('.', ',') + 'M';
      else val = (isNeg ? '−' : '') + Math.round(Math.abs(mm.profit)/1000) + 'K';
      return '<div class="v3-mchip' + (i === curIdx ? ' on' : '') + (mm.hasData ? '' : ' empty') + (isNeg && mm.hasData ? ' neg' : '') + '" onclick="selectMonth(' + i + ')">' +
        '<div class="n">' + v2esc(String(mm.short||'').substring(0,3).toUpperCase()) + '</div><div class="v num">' + val + '</div></div>';
    }).join('');
    var debtors = residents.filter(function(r){ return (Number(r.debt)||0) > 0; }).length;
    var totalDebt = Number(cache.totalDebt)||0;
    h += '<div class="v2-card v3-month" style="margin-top:14px">' +
      '<div class="v3-row-sb"><div style="font-size:15px;font-weight:800" id="v3MonName">' + v2esc(m.name) + ' ' + (typeof mp.year === 'number' ? mp.year : new Date().getFullYear()) + '</div>' +
        '<div style="display:flex;gap:6px"><button class="v3-btn" onclick="window.open(\'https://docs.google.com/spreadsheets/d/1D-D4P5G9cmX1tdyluWe88sNTVGGqJqQWA4NvrvTbrYE/edit\', \'_blank\')">Таблица</button></div></div>' +
      '<div class="v3-grid2" style="margin-top:14px">' +
        '<div><div class="v3-k">Оборот</div><div class="v3-v num" id="v3Rev">' + fmt(m.revenue) + '</div></div>' +
        '<div><div class="v3-k">Чистая прибыль</div><div class="v3-v num' + (m.profit < 0 ? ' red' : '') + '" id="v3Profit">' + fmt(m.profit) + '</div></div>' +
        '<div><div class="v3-k">Дивиденды</div><div class="v3-v num" id="v3Div">' + fmt(m.dividends||0) + '</div></div>' +
        '<div><div class="v3-k">На кассе</div><div class="v3-v num' + ((m.kassa != null && m.kassa < 0) ? ' red' : '') + '" id="v3Kassa">' + ((m.kassa === null || m.kassa === undefined) ? '·' : fmt(m.kassa)) + '</div></div>' +
      '</div>' +
      '<div class="v3-mchips" id="plMonthsRow">' + chips + '</div>' +
      '<div class="v3-debt" onclick="v3openResidents(\'debt\')"><span>Дебет · ' + debtors + ' ' + v3plural(debtors, 'резидент', 'резидента', 'резидентов') + '</span>' +
        '<span class="num' + (totalDebt > 0 ? ' red' : '') + '">' + fmt(totalDebt) + '</span></div>' +
    '</div>';
    setTimeout(function(){
      var row = document.getElementById('plMonthsRow');
      var act = row && row.querySelector('.v3-mchip.on');
      if(act) row.scrollLeft = act.offsetLeft - row.offsetLeft - row.clientWidth/2 + act.clientWidth/2;
    }, 50);
  }

  // Кто не сдал отчёт за сегодня
  var need = residents.filter(function(r){ return !r.isExcluded; });
  var missing = v3missingReports();
  var todayMeets = (cache.schedule||[]).filter(function(ev){ return v3isToday(ev.date); })
    .sort(function(a,b){ return parseScheduleKey(a) - parseScheduleKey(b); });
  window._v3TodayMeets = todayMeets;
  var meetAt = {}; todayMeets.forEach(function(ev){ meetAt[ev.res] = ev.time || 'сегодня'; });
  if(missing.length){
    var shown = missing.slice(0, 6);
    window._v3Miss = shown;
    h += '<div class="v2-card v3-alert" style="margin-top:10px;padding:14px 16px">' +
      '<div class="v3-row-sb" style="align-items:baseline"><div class="v3-lbl red lnkable" onclick="showPage(\'reports\')">НЕ СДАЛИ ОТЧЁТ · ' + missing.length + ' ›</div><div class="v3-cap">до 23:59 · штраф в 14:30</div></div>' +
      shown.map(function(r, i){
        var note = meetAt[r.name] ? 'встреча ' + meetAt[r.name] : '';
        return '<div class="v3-miss">' + v3av(r, 32) + '<div class="m" onclick="openResidentProfile(window._v3Miss[' + i + '])">' + v2esc(r.name) + (note ? ' <span>' + v2esc(note) + '</span>' : '') + '</div></div>';
      }).join('') +
      (missing.length > shown.length ? '<div class="v3-more" onclick="v3openResidents(\'noreport\')">Ещё ' + (missing.length - shown.length) + ' · весь список</div>' : '') +
    '</div>';
  } else if(need.length){
    h += '<div class="v2-card" style="margin-top:10px;padding:14px 16px;display:flex;align-items:center;gap:12px" onclick="showPage(\'reports\')">' +
      '<div style="width:28px;height:28px;border-radius:8px;background:#fff;color:#000;display:flex;align-items:center;justify-content:center;flex-shrink:0">' + V3_CHECK + '</div>' +
      '<div style="flex:1"><div class="v3-lbl">ОТЧЁТЫ ЗА СЕГОДНЯ</div><div style="font-size:14px;font-weight:700;margin-top:2px">Сдали все ' + need.length + '</div></div></div>';
  }

  h += '<div class="v2-h">Встречи сегодня · ' + todayMeets.length + ' <span class="lnk" onclick="showPage(\'schedule\')">Все</span></div><div class="v2-card">' +
    (todayMeets.length ? todayMeets.map(function(ev, i){
      var kind = v3isEvent(ev) ? 'событие' : (v3isOnline(ev) ? 'онлайн' : 'офлайн');
      return '<div class="v2-row" style="padding:11px 16px" onclick="showMeetingDetail(window._v3TodayMeets[' + i + '])"><div class="num" style="width:46px;font-size:13px;font-weight:700">' + v2esc(ev.time||'') + '</div>' +
        '<div class="m"><b>' + v2esc(ev.res) + '</b></div><div class="v3-cap">' + kind + '</div></div>';
    }).join('') : '<div class="v2-empty">Сегодня встреч нет</div>') + '</div>';

  h += '<div class="v2-h">Клуб</div><div class="v2-card">' +
    v3row('club', 'Резиденты', residents.length + ' ' + v3plural(residents.length, 'активный', 'активных', 'активных'), "showPage('residents')") +
    v3row('doc', 'Отчёты сегодня', (need.length - missing.length) + ' из ' + need.length, "showPage('reports')") +
    v3row('alert', 'Штрафы к получению', unpaid.length + ' шт', "showPage('fines')", '<div class="num' + (unpaidSum > 0 ? ' red' : '') + '" style="font-size:14px;font-weight:800">' + fmt(unpaidSum) + '</div>') +
  '</div>';
  box.innerHTML = h;
}

function renderSchedule(){
  // Сортировка по дате+времени (раньше выше)
  const list = (cache.schedule||[]).slice().sort(function(a,b){
    var aKey = parseScheduleKey(a);
    var bKey = parseScheduleKey(b);
    return aKey - bKey;
  });
  window._currentScheduleList = list;
  const el = document.getElementById('scheduleContent');
  const viewMode = window._scheduleViewMode || 'calendar'; // list | calendar
  
  // Кнопка + пропадала: шапка рисуется один раз, а роль админа определяется позже.
  // Поэтому наличие кнопки синхронизируем при КАЖДОМ рендере
  (function syncAddBtn(){
    var hdr = el.querySelector('.sched-header');
    if(!hdr) return;
    var box = hdr.querySelector('div[style*="display:flex"]');
    if(!box) return;
    var existing = document.getElementById('schedAddBtn');
    var shouldShow = isRealAdmin && viewAs === 'admin';
    if(shouldShow && !existing){
      var b = document.createElement('button');
      b.className = 'icon-btn';
      b.id = 'schedAddBtn';
      b.title = 'Добавить встречу';
      b.innerHTML = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>';
      b.onclick = function(ev){ ev.stopPropagation(); ev.preventDefault(); openAddMenu(); };
      box.insertBefore(b, box.firstChild);
    } else if(!shouldShow && existing){
      existing.remove();
    } else if(shouldShow && existing){
      existing.onclick = function(ev){ ev.stopPropagation(); ev.preventDefault(); openAddMenu(); };
    }
  })();
  
  // Шапка рисуется один раз, потом обновляется только содержимое
  if(!el.querySelector('.sched-header')){
    el.innerHTML = '';
    var headerEl = document.createElement('div');
    headerEl.className = 'sched-header section-head';
    headerEl.innerHTML = 
      '<div class="section-title">Расписание</div>' +
      '<div style="display:flex;gap:8px;align-items:center">' +
        ((isRealAdmin && viewAs==='admin') ? 
          '<button class="icon-btn" id="schedAddBtn" title="Добавить">' +
            '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>' +
          '</button>' 
          : '') +
        '<button class="icon-btn" id="schedViewBtn" title="Сменить вид" data-mode="list">' +
          '<svg class="ico-cal" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="18" rx="2" ry="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/></svg>' +
          '<svg class="ico-list" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/><line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/></svg>' +
        '</button>' +
      '</div>';
    el.appendChild(headerEl);
    
    var bodyEl = document.createElement('div');
    bodyEl.className = 'sched-body';
    el.appendChild(bodyEl);
    
    // Обработчики кнопок
    var addBtn = el.querySelector('#schedAddBtn');
    if(addBtn) addBtn.addEventListener('click', openAddMenu);
    var viewBtn = el.querySelector('#schedViewBtn');
    if(viewBtn) viewBtn.addEventListener('click', function(e){
      try{ this.blur(); }catch(blurE){}
      e.preventDefault();
      setScheduleView(window._scheduleViewMode === 'list' ? 'calendar' : 'list');
    });
  }
  
  // Обновляем только класс переключателя. без перерисовки innerHTML (не мигает)
  var viewBtn = el.querySelector('#schedViewBtn');
  if(viewBtn){
    viewBtn.title = viewMode === 'list' ? 'Календарь' : 'Список';
    viewBtn.setAttribute('data-mode', viewMode);
  }
  
  // Обновляем тело
  var bodyEl = el.querySelector('.sched-body');
  if(!list.length && viewMode !== 'calendar'){
    bodyEl.innerHTML = '<div class="empty"><div class="em-icon"></div><div class="em-title">Встреч нет</div><div class="em-text">Нажмите кнопку выше</div></div>';
    return;
  }
  if(viewMode === 'calendar'){
    bodyEl.innerHTML = renderScheduleCalendar(list);
  } else {
    bodyEl.innerHTML = list.map(function(ev,i){ return meetingCard(ev,i); }).join('');
  }
}

function setScheduleView(mode){
  window._scheduleViewMode = mode;
  try{localStorage.setItem('bs_schedule_view', mode);}catch(e){}
  renderSchedule();
}

function renderScheduleCalendar(list){
  // Календарный вид: сетка 7×N клеток с днями
  // Текущий месяц + следующий месяц (или 2 месяца если нужно)
  var now = new Date();
  // Берём смещение если пользователь листал
  var monthOffset = window._calMonthOffset || 0;
  var displayMonth = new Date(now.getFullYear(), now.getMonth() + monthOffset, 1);
  
  var year = displayMonth.getFullYear();
  var month = displayMonth.getMonth();
  var monthName = ['Январь','Февраль','Март','Апрель','Май','Июнь','Июль','Август','Сентябрь','Октябрь','Ноябрь','Декабрь'][month];
  
  // Группируем встречи по дате. ev.date может быть "dd.MM" или "dd.MM.yyyy". нормализуем до "dd.MM"
  var byDate = {};
  list.forEach(function(ev){
    var s = String(ev.date || '');
    var match = s.match(/^(\d{1,2})\.(\d{1,2})/);
    if(!match) return;
    var dd = match[1].padStart(2,'0');
    var mm = match[2].padStart(2,'0');
    var key = dd + '.' + mm;
    if(!byDate[key]) byDate[key] = [];
    byDate[key].push(ev);
  });
  
  // Начало месяца. какой день недели (0=вс, 1=пн.... в нашей сетке пн первый)
  var firstDay = new Date(year, month, 1);
  var weekStart = firstDay.getDay(); // 0=вс, 1=пн
  weekStart = weekStart === 0 ? 6 : weekStart - 1; // переводим в 0=пн ... 6=вс
  
  var daysInMonth = new Date(year, month + 1, 0).getDate();
  var todayStr = now.getDate() + '.' + (now.getMonth()+1) + '.' + now.getFullYear();
  todayStr = (now.getDate()<10?'0':'') + now.getDate() + '.' + (now.getMonth()+1<10?'0':'') + (now.getMonth()+1) + '.' + now.getFullYear();
  
  var html = '<div class="cal-wrap">';
  
  // Хедер с навигацией
  html += '<div class="cal-nav">';
  html += '<button class="cal-nav-btn" onclick="calPrevMonth()">◀</button>';
  html += '<div class="cal-month-title">' + monthName + ' ' + year + '</div>';
  html += '<button class="cal-nav-btn" onclick="calNextMonth()">▶</button>';
  html += '</div>';
  
  // Дни недели
  html += '<div class="cal-weekdays">';
  ['ПН','ВТ','СР','ЧТ','ПТ','СБ','ВС'].forEach(function(w){
    html += '<div class="cal-wd">' + w + '</div>';
  });
  html += '</div>';
  
  // Сетка дней
  html += '<div class="cal-grid">';
  
  // Пустые клетки до начала месяца
  for(var i=0; i<weekStart; i++){
    html += '<div class="cal-day cal-day-empty"></div>';
  }
  
  // Дни месяца
  for(var d=1; d<=daysInMonth; d++){
    var ddPad = (d<10?'0':'') + d;
    var mmPad = (month+1<10?'0':'') + (month+1);
    var dStr = ddPad + '.' + mmPad + '.' + year; // для click handler
    var lookupKey = ddPad + '.' + mmPad; // ключ для поиска в byDate
    var dayMeets = byDate[lookupKey] || [];
    var isToday = dStr === todayStr;
    var isPast = (new Date(year, month, d) < new Date(now.getFullYear(), now.getMonth(), now.getDate()));
    
    // Состоявшиеся встречи этого дня (из Лога встреч)
    // Сравниваем полную дату с годом. Раньше сверялись только день и месяц,
    // и встречи прошлых лет попадали не в те клетки
    var doneToday = (cache.doneMeetings || []).filter(function(dm){
      return String(dm.date || '').trim() === dStr;
    });

    // Заливка: зелёный если встреча прошла, иначе цвет формата
    var fillCls = '';
    if(doneToday.length && !dayMeets.length){
      fillCls = 'cal-fill-done';
    } else if(dayMeets.length){
      var kinds = {};
      dayMeets.forEach(function(ev){ kinds[meetKind(ev)] = true; });
      var uniq = Object.keys(kinds);
      if(uniq.length > 1) fillCls = 'cal-fill-mixed';
      else if(uniq[0] === 'offline') fillCls = 'cal-fill-offline';
      else if(uniq[0] === 'online') fillCls = 'cal-fill-online';
      else fillCls = 'cal-fill-other';
    }
    var dayCount = dayMeets.length || doneToday.length;
    html += '<div class="cal-day ' + (isToday?'cal-today ':'') + (isPast?'cal-past ':'') + fillCls + '"';
    html += ' onclick="calDayClick(\''+ dStr +'\')"';
    html += '>';
    html += '<div class="cal-day-num">' + d + '</div>';
    if(dayCount > 1){
      html += '<div class="cal-day-cnt">' + dayCount + '</div>';
    } else if(doneToday.length && !dayMeets.length){
      html += '<div class="cal-day-cnt">✓</div>';
    }
    html += '</div>';
  }
  
  html += '</div>';
  
  // Легенда форматов
  html += '<div class="cal-legend">' +
    '<div class="cal-leg-item"><span class="cal-leg-dot" style="background:#e57373"></span> Офлайн</div>' +
    '<div class="cal-leg-item"><span class="cal-leg-dot" style="background:#64b5f6"></span> Онлайн</div>' +
    '<div class="cal-leg-item"><span class="cal-leg-dot" style="background:#D9D9D9"></span> Прочее</div>' +
    '<div class="cal-leg-item"><span class="cal-leg-dot" style="background:#4caf7d"></span> Прошла</div>' +
    '</div>';
  
  html += '</div>';
  return html;
}

function meetKind(ev){
  var link = String(ev.link || ev.meet || '').toLowerCase();
  if(link.indexOf('meet.google') >= 0 || link.indexOf('zoom') >= 0) return 'online';
  if(link && link.indexOf('http') < 0) return 'offline';
  return 'other';
}

function meetDotClass(ev){
  var link = String(ev.link || ev.meet || '').toLowerCase();
  if(link.indexOf('meet.google') >= 0 || link.indexOf('zoom') >= 0) return 'cal-dot-online';
  if(link && link.indexOf('http') < 0) return 'cal-dot-offline';
  if(!link) return 'cal-dot-unknown';
  return 'cal-dot-online';
}

function meetFormatClass(ev){
  // Определяем формат встречи по ссылке/адресу
  var link = String(ev.link || ev.meet || '').toLowerCase();
  if(link.indexOf('meet.google') >= 0 || link.indexOf('zoom') >= 0) return 'cal-meet-online';
  if(link && link.indexOf('http') < 0) return 'cal-meet-offline'; // указан адрес
  if(!link) return 'cal-meet-unknown';
  return 'cal-meet-online';
}

function calDayClick(dStr){
  // Клик по дню календаря: есть встречи. показать список, иначе. создать встречу на эту дату
  var list = (cache.schedule || []);
  var parts = dStr.split('.');
  var key = parts[0] + '.' + parts[1];
  var dayMeets = list.filter(function(ev){
    var s = String(ev.date || '');
    var m = s.match(/^(\d{1,2})\.(\d{1,2})/);
    if(!m) return false;
    return (m[1].padStart(2,'0') + '.' + m[2].padStart(2,'0')) === key;
  });
  // Всегда открываем карточку дня: с встречами. детали, без встреч. предложение создать
  showDayMeetings(dStr);
}

function calPrevMonth(){
  window._calMonthOffset = (window._calMonthOffset||0) - 1;
  renderSchedule();
}
function calNextMonth(){
  window._calMonthOffset = (window._calMonthOffset||0) + 1;
  renderSchedule();
}
function showDayMeetings(dateStr){
  // Детали встреч дня. Раньше вызывалось showDetail(ev) с аргументом, который функция игнорирует.
  // отсюда пустое окно. Теперь рисуем полноценную карточку дня
  var ddmm = dateStr.split('.').slice(0,2).join('.');
  var list = (cache.schedule||[]).filter(function(ev){
    var evDdmm = String(ev.date||'').split('.').slice(0,2).join('.');
    return evDdmm === ddmm;
  });
  list.sort(function(a,b){ return String(a.time||'').localeCompare(String(b.time||'')); });

  var titleEl = document.getElementById('dayMeetsTitle');
  if(titleEl) titleEl.textContent = dateStr + ' · ' + list.length + (list.length===1?' встреча':(list.length<5?' встречи':' встреч'));

  var body = document.getElementById('dayMeetsBody');
  if(!body) return;

  // Состоявшиеся встречи этого дня
  var doneList = (cache.doneMeetings || []).filter(function(dm){
    return String(dm.date || '').trim() === dateStr;
  });

  if(!list.length){
    var h0 = '';
    if(doneList.length){
      h0 += '<div style="font-size:11px;color:#4caf7d;font-weight:800;letter-spacing:1px;text-transform:uppercase;margin-bottom:10px">Встречи прошли</div>';
      doneList.forEach(function(dm){
        h0 += '<div style="padding:13px 14px;margin-bottom:8px;background:var(--card2);border-radius:11px;border-left:3px solid #4caf7d;display:flex;align-items:center;gap:10px">' +
          '<span style="font-size:16px"></span>' +
          '<span style="font-size:14px;font-weight:700;color:#fff">' + dm.res + '</span></div>';
      });
    } else {
      h0 += '<div style="text-align:center;padding:24px 0;color:#888;font-size:13px">В этот день встреч нет</div>';
    }
    if(isRealAdmin && viewAs === 'admin'){
      h0 += '<button class="btn btn-primary" style="margin-top:6px" onclick="closeModal(\'dayMeetsModal\');calCreateOn(\''+dateStr+'\')">Создать встречу</button>';
    }
    body.innerHTML = h0;
    openModal('dayMeetsModal');
    return;
  }

  var h = '';
  list.forEach(function(ev){
    var link = String(ev.link || ev.meet || '');
    var isOnline = link.indexOf('meet.google') >= 0 || link.indexOf('zoom') >= 0;
    var isOffline = link && !isOnline && link.indexOf('http') < 0;
    var color = isOnline ? '#64b5f6' : (isOffline ? '#e57373' : '#D9D9D9');
    var fmtLabel = isOnline ? 'Онлайн' : (isOffline ? 'Офлайн' : 'Прочее');

    h += '<div style="padding:14px;margin-bottom:10px;background:var(--card2);border-radius:12px;border-left:3px solid ' + color + '">';
    h += '<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:10px;margin-bottom:8px">';
    h += '<div style="min-width:0"><div style="font-size:15px;font-weight:800;color:#fff">' + (ev.res||'Без имени') + '</div>';
    h += '<div style="font-size:12px;color:' + color + ';font-weight:700;margin-top:2px">' + fmtLabel + '</div></div>';
    h += '<div style="font-size:17px;font-weight:800;color:#FFFFFF;flex-shrink:0">' + (ev.time||'') + '</div>';
    h += '</div>';

    if(isOnline){
      h += '<div style="font-size:12px;color:#888;word-break:break-all;margin-bottom:10px">' + link + '</div>';
      h += '<a href="' + link + '" target="_blank"><button class="btn btn-primary" style="width:100%;margin:0;padding:10px;font-size:13px">Открыть Google Meet</button></a>';
    } else if(isOffline){
      h += '<div style="font-size:13px;color:#ccc;margin-bottom:4px">' + link + '</div>';
    } else {
      h += '<div style="font-size:12px;color:#888">Ссылка или адрес не указаны</div>';
    }

    if(isRealAdmin && viewAs === 'admin'){
      h += '<div style="display:flex;gap:8px;margin-top:10px">';
      h += '<button onclick="markAttendanceFromDay(\'' + String(ev.res||'').replace(/'/g,"\\'") + '\',\'' + dateStr + '\')" style="flex:1;background:rgba(76,175,125,0.15);border:1px solid #4caf7d;color:#4caf7d;padding:9px;border-radius:9px;font-size:12px;font-weight:800;font-family:inherit">Отметить</button>';
      h += '</div>';
    }
    h += '</div>';
  });

  body.innerHTML = h;
  openModal('dayMeetsModal');
}

function calCreateOn(dateStr){
  // Создание встречи на конкретную дату из календаря
  openModal('schedModal');
  initSchedModal();
  setTimeout(function(){
    var key = dateStr.split('.').slice(0,2).join('.');
    var btns = document.querySelectorAll('#dateGrid .pick-btn[data-date]');
    var found = false;
    btns.forEach(function(b){
      var bd = b.dataset.date || '';
      if(bd === dateStr || bd.substring(0,5) === key){
        btns.forEach(function(x){ x.classList.remove('selected'); });
        b.classList.add('selected');
        selDate = bd;
        found = true;
        if(typeof updateAvailableTimes === 'function') updateAvailableTimes();
      }
    });
    if(!found) showToast('Выбери дату в списке');
  }, 120);
}

function markAttendanceFromDay(resName, dateStr){
  if(!confirm('Отметить посещение: ' + resName + ' ' + dateStr + '?')) return;
  showToast('Отмечаем...');
  callAction('markAttendance', {names: resName, date: dateStr}).then(function(r){
    if(r && (r.ok || r.alreadyDone)){
      showToast('Отмечено');
      closeModal('dayMeetsModal');
    } else {
      showToast('' + ((r && r.error) || 'Ошибка'));
    }
  });
}

function meetingCard(ev, idx){
  const link = String(ev.link || '');
  const isOnline = link.indexOf('meet') >= 0 || link.indexOf('zoom') >= 0;
  const isEvent = (ev.res || '').indexOf('🎬') === 0;
  const isOffline = !isEvent && link && !isOnline;
  const cd = countdownText(ev.date, ev.time);
  const title = isEvent ? ev.res : ev.res;

  // Формат видно сразу: цветная полоса слева и метка справа
  var accent = isEvent ? '#ab8fd4' : (isOnline ? '#64b5f6' : (isOffline ? '#e57373' : '#777777'));
  var badge = isEvent ? 'СОБЫТИЕ' : (isOnline ? 'ОНЛАЙН' : (isOffline ? 'ОФЛАЙН' : '?'));
  var place = isEvent ? (link || 'Мероприятие')
            : (isOnline ? 'Google Meet' : (isOffline ? link : 'Место не указано'));

  return `
    <div class="meeting ${cd.urgent?'upcoming':''}" data-meetidx="${idx}" onclick="openMeetByIdx(${idx})"
         style="border-left:3px solid ${accent}">
      ${cd.urgent?`<div class="meeting-countdown">${cd.text}</div>`:''}
      <div style="display:flex;justify-content:space-between;align-items:flex-start;gap:10px">
        <div style="min-width:0;flex:1">
          <div class="meeting-title">${title}</div>
          <div class="meeting-when">${ev.date} ${ev.time||''}</div>
        </div>
        <span style="flex-shrink:0;font-size:9.5px;font-weight:800;letter-spacing:0.8px;
              color:${accent};background:${accent}1f;border:1px solid ${accent}55;
              padding:4px 9px;border-radius:11px">${badge}</span>
      </div>
      <div class="meeting-row" style="margin-top:6px;${!link && !isEvent ? 'opacity:0.5' : ''}">${place}</div>
    </div>
  `;
}


function parseScheduleKey(ev){
  if(!ev || !ev.date) return 9999999999;
  try {
    var dp = ev.date.split('.');
    var y = new Date().getFullYear();
    var d = new Date(y, parseInt(dp[1])-1, parseInt(dp[0]));
    if(ev.time){
      var tp = ev.time.split(':');
      d.setHours(parseInt(tp[0]), parseInt(tp[1]||0));
    }
    return d.getTime();
  } catch(e){ return 9999999999; }
}

function countdownText(date, time){
  if(!date) return {text:'', urgent:false};
  try {
    const [d,m] = date.split('.').map(Number);
    const y = new Date().getFullYear();
    const [h,min] = (time||'00:00').split(':').map(Number);
    const dt = new Date(y, m-1, d, h, min);
    const diff = dt - new Date();
    if(diff < 0) return {text:'', urgent:false};
    const days = Math.floor(diff / 86400000);
    const hours = Math.floor((diff % 86400000) / 3600000);
    if(days === 0 && hours < 24){
      if(hours < 1) return {text:'Скоро!', urgent:true};
      return {text:`Через ${hours} ч`, urgent:true};
    }
    if(days < 7) return {text:`Через ${days} дн`, urgent:days<=2};
    return {text:'', urgent:false};
  } catch(e){ return {text:'', urgent:false}; }
}

function renderReports(){
  const logs = cache.logs||[];
  const residents = (cache.residents||[]).filter(r=>!r.isFired&&!r.isExcluded&&!r.isAdmin&&!r.isTeam&&r.name!=='Тест'&&r.name!=='Тест2');
  const today = new Date().toLocaleDateString('ru-RU');
  const submitted = new Set(logs.filter(l=>l.date===today).map(l=>String(l.chatId||l.userId||'')));
  const done = residents.filter(r=>submitted.has(String(r.chatId)));
  const missing = residents.filter(r=>!submitted.has(String(r.chatId)));

  document.getElementById('reportsContent').innerHTML = `
    <div class="section-head">
      <div class="section-title">Отчёты</div>
      <span class="badge b-gray">${today}</span>
    </div>
    <div class="card">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
        <div class="card-title" style="margin:0">Сдали сегодня</div>
        <span class="badge b-green">${done.length} / ${residents.length}</span>
      </div>
      ${done.length?done.map(r=>`<div class="list-item">
        <div class="list-avatar" style="background:rgba(255,255,255,0.2);color:var(--green)">${r.name[0]}${avImg(r.chatId, r.avatar)}</div>
        <div class="list-content"><div class="list-name">${r.name}</div></div>
        <div></div>
      </div>`).join(''):'<div class="list-sub" style="text-align:center;padding:12px">Пока никто</div>'}
    </div>
    ${missing.length?`<div class="card">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
        <div class="card-title" style="margin:0">Не сдали</div>
        <span class="badge b-red">${missing.length}</span>
      </div>
      ${missing.map(r=>`<div class="list-item">
        <div class="list-avatar" style="background:rgba(255,107,111,0.2);color:var(--red)">${r.name[0]}${avImg(r.chatId, r.avatar)}</div>
        <div class="list-content"><div class="list-name">${r.name}</div></div>
        <span class="badge b-red">нет</span>
      </div>`).join('')}
    </div>`:''}
    <div class="card">
      <div class="card-title">Последние отчёты</div>
      ${logs.slice(0,10).map(l=>`<div class="list-item">
        <div class="list-avatar">${(l.name||'U')[0]}${avImg(l.chatId, (resByName(l.name)||{}).avatar)}</div>
        <div class="list-content">
          <div class="list-name">${l.name||l.username}</div>
          <div class="list-sub">${(l.text||'').substring(0,80)}...</div>
        </div>
        <div class="list-right"><div class="list-date">${l.date} ${l.time||''}</div></div>
      </div>`).join('')}
    </div>
  `;
}

function renderFines(){
  const fines = (cache.fines||[]).filter(f=>f.status==='Не оплатил');
  const el = document.getElementById('finesContent');
  el.innerHTML = `
    <div class="section-head">
      <div class="section-title">Штрафы</div>
      <div class="section-action" onclick="openModal('fineModal');initFineModal()">+ Выставить</div>
    </div>
  `;
  if(!fines.length){
    el.innerHTML += `<div class="empty"><div class="em-icon"></div><div class="em-title">Штрафов нет</div><div class="em-text">Все оплачены</div></div>`;
    return;
  }
  var canManageFines = isRealAdmin && viewAs==='admin';
  window._currentFinesList = fines;
  var finesHtml = '<div class="card">';
  fines.forEach(function(f,i){
    var clickAttr = canManageFines ? ('onclick="openFineActions('+i+')" style="cursor:pointer"') : '';
    finesHtml += '<div class="list-item" '+clickAttr+'>'+
      '<div class="list-avatar" style="background:rgba(255,107,111,0.2);color:var(--red)">!</div>'+
      '<div class="list-content">'+
        '<div class="list-name">'+f.name+'</div>'+
        '<div class="list-sub">'+(f.kind||f.type||'')+'</div>'+
      '</div>'+
      '<div class="list-right">'+
        '<div class="list-amount">'+fmt(f.amount)+' ₸</div>'+
        '<div class="list-date">'+f.date+'</div>'+
      '</div>'+
    '</div>';
  });
  finesHtml += '</div>';
  el.innerHTML += finesHtml;
}

// ══════════ ПОЛЕЗНОЕ: очередь публикаций ══════════
function renderContentPlan(){
  var el = document.getElementById('contentPlanBox');
  if(!el) return;
  try{
    var items = (cache.contentPlan && cache.contentPlan.items) || [];
    var waiting = items.filter(function(i){ return i.status === 'Ожидает'; });
    var sent = items.filter(function(i){ return i.status !== 'Ожидает'; });

    var h = '<div class="card" style="padding:14px 16px;margin-bottom:14px;display:grid;grid-template-columns:1fr 1fr;gap:10px;text-align:center">' +
      '<div><div style="font-size:22px;font-weight:800;color:#FFFFFF;line-height:1">' + waiting.length + '</div>' +
      '<div style="font-size:10px;color:#888;letter-spacing:0.5px;margin-top:4px">В ОЧЕРЕДИ</div></div>' +
      '<div style="border-left:1px solid rgba(255,255,255,0.07)"><div style="font-size:22px;font-weight:800;color:#fff;line-height:1">' + sent.length + '</div>' +
      '<div style="font-size:10px;color:#888;letter-spacing:0.5px;margin-top:4px">ОПУБЛИКОВАНО</div></div></div>';

    // График чек-листов: они уходят в Полезное по понедельникам
    var clSched = cache.checklistSchedule || [];
    if(clSched.length){
      h += '<div style="font-size:11px;color:#FFFFFF;font-weight:800;letter-spacing:1px;text-transform:uppercase;margin:4px 0 10px">Чек-листы по понедельникам</div>';
      h += '<div class="card" style="padding:8px 16px 12px;margin-bottom:16px">';
      clSched.forEach(function(cl){
        h += '<div style="display:flex;justify-content:space-between;align-items:center;gap:10px;padding:10px 0;border-top:1px solid rgba(255,255,255,0.05)">' +
          '<div style="min-width:0"><div style="font-size:13.5px;color:' + (cl.next ? '#FFFFFF' : '#fff') + ';font-weight:' + (cl.next ? '800' : '600') + ';line-height:1.35">' + cl.name + '</div>' +
          (cl.next ? '<div style="font-size:11px;color:#888;margin-top:2px">следующий</div>' : '') + '</div>' +
          '<span style="font-size:12px;color:#888;flex-shrink:0;font-weight:700">' + cl.date + '</span></div>';
      });
      h += '<div style="font-size:11.5px;color:#666;margin-top:10px;line-height:1.4">Отправляются автоматически. Порядок по кругу, загрузить новые можно во вкладке Материалы.</div>';
      h += '</div>';
    }

    if(waiting.length <= 3){
      h += '<div class="card" style="padding:12px 14px;margin-bottom:14px;border:1px solid rgba(255,107,111,0.3);background:rgba(255,107,111,0.05);font-size:12.5px;color:#FF6B6F;line-height:1.45">' +
        'Очередь заканчивается. Система добавит новые посты автоматически, либо нажмите + чтобы написать свой.</div>';
    }

    if(!items.length){
      h += '<div class="empty"><div class="em-icon"></div><div class="em-title">Очередь пуста</div><div class="em-text">Нажмите + чтобы добавить пост</div></div>';
      el.innerHTML = h;
      return;
    }

    h += '<div style="font-size:11px;color:#FFFFFF;font-weight:800;letter-spacing:1px;text-transform:uppercase;margin:4px 0 10px">Ближайшие</div>';
    waiting.slice(0, 12).forEach(function(it){
      h += contentCard(it, true);
    });

    if(sent.length){
      h += '<div style="font-size:11px;color:#888;font-weight:800;letter-spacing:1px;text-transform:uppercase;margin:20px 0 10px">Опубликовано</div>';
      sent.slice(-8).reverse().forEach(function(it){
        h += contentCard(it, false);
      });
    }
    el.innerHTML = h;
  }catch(e){
    el.innerHTML = '<div class="card"><div style="color:var(--red);font-size:13px">Не удалось загрузить</div></div>';
  }
}

function contentCard(it, editable){
  var preview = it.text.length > 150 ? it.text.substring(0, 150) + '...' : it.text;
  var dateShort = it.date.split(' ')[0];
  var timeShort = (it.date.split(' ')[1] || '').substring(0,5);
  return '<div class="card" style="padding:14px 16px;margin-bottom:10px;' + (editable ? 'border-left:3px solid #FFFFFF' : 'opacity:0.6') + '">' +
    '<div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:8px">' +
      '<span style="font-size:11px;font-weight:800;color:#FFFFFF;letter-spacing:0.5px;text-transform:uppercase">' + it.cat + '</span>' +
      '<span style="font-size:12px;color:#888;font-weight:700;flex-shrink:0">' + dateShort + (timeShort ? ' · ' + timeShort : '') + '</span>' +
    '</div>' +
    '<div style="font-size:13px;color:rgba(255,255,255,0.82);line-height:1.5;white-space:pre-wrap">' + preview + '</div>' +
    (editable ?
      '<div style="display:flex;gap:10px;margin-top:11px;padding-top:10px;border-top:1px solid rgba(255,255,255,0.06)">' +
        '<button onclick="editContentPost(' + it.row + ')" style="flex:1;background:rgba(255,255,255,0.12);border:1px solid rgba(255,255,255,0.4);color:#FFFFFF;padding:8px;border-radius:8px;font-size:12px;font-weight:800;font-family:inherit">Изменить</button>' +
        '<button onclick="deleteContentPost(' + it.row + ')" style="background:rgba(255,107,111,0.1);border:1px solid rgba(255,107,111,0.3);color:#FF6B6F;padding:8px 14px;border-radius:8px;font-size:12px;font-weight:800;font-family:inherit">Удалить</button>' +
      '</div>' : '') +
    '</div>';
}

var _contentEditRow = null;
function openContentModal(){
  _contentEditRow = null;
  var t = document.getElementById('contentText'); if(t) t.value = '';
  var c = document.getElementById('contentCat'); if(c) c.value = 'Финансы';
  var d = document.getElementById('contentDate');
  if(d){
    var when = new Date(Date.now() + 2*24*3600*1000);
    d.value = String(when.getDate()).padStart(2,'0') + '.' + String(when.getMonth()+1).padStart(2,'0') + '.' + when.getFullYear() + ' 09:00';
  }
  var ttl = document.getElementById('contentModalTitle');
  if(ttl) ttl.textContent = 'Новый пост в Полезное';
  openModal('contentModal');
}

function editContentPost(row){
  var it = ((cache.contentPlan && cache.contentPlan.items) || []).find(function(x){ return x.row === row; });
  if(!it) return;
  _contentEditRow = row;
  document.getElementById('contentText').value = it.text;
  document.getElementById('contentCat').value = it.cat;
  document.getElementById('contentDate').value = it.date;
  document.getElementById('contentModalTitle').textContent = 'Изменить пост';
  openModal('contentModal');
}

var _contentInFlight = false;
async function submitContentPost(){
  if(_contentInFlight) return;
  var text = document.getElementById('contentText').value.trim();
  var cat = document.getElementById('contentCat').value;
  var date = document.getElementById('contentDate').value.trim();
  if(!text){ showToast('Напишите текст'); return; }
  _contentInFlight = true;
  closeModal('contentModal');
  try{
    var r;
    if(_contentEditRow){
      r = await callAction('updateContentPost', {row: _contentEditRow, text: text, date: date});
    } else {
      r = await callAction('addContentPost', {text: text, cat: cat, date: date});
    }
    if(r && r.ok){ showToast('Сохранено'); refreshData(); }
    else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(function(){ _contentInFlight = false; }, 1200); }
}

async function deleteContentPost(row){
  if(!confirm('Удалить пост из очереди?')) return;
  try{
    var r = await callAction('deleteContentPost', {row: row});
    if(r && r.ok){ showToast('Удалено'); refreshData(); }
  }catch(e){}
}

// ══════════ СММ ══════════
var _smmFilter = 'all';
function setSmmFilter(f){
  _smmFilter = f;
  renderSmm();
}

function renderSmm(){
  var el = document.getElementById('smmBox');
  if(!el) return;
  try{
    var allItems = (cache.smm && cache.smm.items) || [];
    var items = _smmFilter === 'all' ? allItems : allItems.filter(function(i){ return i.platform === _smmFilter; });

    var byStatus = {'Идея': [], 'В работе': [], 'Готово': [], 'Опубликовано': []};
    items.forEach(function(it){
      if(!byStatus[it.status]) byStatus[it.status] = [];
      byStatus[it.status].push(it);
    });

    var h = '';
    // Площадки
    var platforms = [['all','Все'],['Instagram','Instagram'],['Threads','Threads'],['Telegram','Telegram'],['TikTok','TikTok']];
    h += '<div style="display:flex;gap:7px;overflow-x:auto;padding:0 0 12px;margin:0 -16px 4px;padding-left:16px;padding-right:16px;scrollbar-width:none">';
    platforms.forEach(function(p){
      var act = _smmFilter === p[0];
      var cnt = p[0] === 'all' ? allItems.length : allItems.filter(function(i){ return i.platform === p[0]; }).length;
      h += '<button onclick="setSmmFilter(\'' + p[0] + '\')" style="flex:0 0 auto;padding:8px 14px;border-radius:20px;border:1.5px solid ' + (act ? '#FFFFFF' : 'var(--border)') + ';background:' + (act ? 'rgba(255,255,255,0.13)' : 'transparent') + ';color:' + (act ? '#FFFFFF' : '#888') + ';font-size:12.5px;font-weight:800;font-family:inherit;white-space:nowrap">' + p[1] + (cnt ? ' · ' + cnt : '') + '</button>';
    });
    h += '</div>';

    h += '<div class="card" style="padding:14px 16px;margin-bottom:14px;display:grid;grid-template-columns:repeat(4,1fr);gap:6px;text-align:center">';
    [['Идея','#888'],['В работе','#D9D9D9'],['Готово','#FFFFFF'],['Опубликовано','#FFFFFF']].forEach(function(s, i){
      h += '<div' + (i ? ' style="border-left:1px solid rgba(255,255,255,0.07)"' : '') + '>' +
        '<div style="font-size:19px;font-weight:800;color:' + s[1] + ';line-height:1">' + (byStatus[s[0]] || []).length + '</div>' +
        '<div style="font-size:9px;color:#888;letter-spacing:0.3px;margin-top:4px">' + s[0].toUpperCase() + '</div></div>';
    });
    h += '</div>';

    if(!items.length){
      h += '<div class="empty"><div class="em-icon"></div><div class="em-title">План пуст</div><div class="em-text">Нажмите + чтобы запланировать публикацию для @business.surgery</div></div>';
      el.innerHTML = h;
      return;
    }

    ['Идея','В работе','Готово','Опубликовано'].forEach(function(st){
      var list = byStatus[st] || [];
      if(!list.length) return;
      var color = st === 'Опубликовано' ? '#FFFFFF' : (st === 'Готово' ? '#FFFFFF' : (st === 'В работе' ? '#D9D9D9' : '#888'));
      h += '<div style="font-size:11px;color:' + color + ';font-weight:800;letter-spacing:1px;text-transform:uppercase;margin:16px 0 10px">' + st + '</div>';
      list.forEach(function(it){
        h += '<div class="card" style="padding:14px 16px;margin-bottom:10px;border-left:3px solid ' + color + ';cursor:pointer" onclick="editSmm(' + it.row + ')">' +
          '<div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:7px">' +
            '<span style="font-size:11px;font-weight:800;color:' + color + ';letter-spacing:0.5px">' + it.platform + (it.rubric ? ' · ' + it.rubric : '') + '</span>' +
            '<span style="font-size:12px;color:#888;flex-shrink:0">' + it.date + '</span>' +
          '</div>' +
          '<div style="font-size:14px;font-weight:800;color:#fff;line-height:1.35">' + it.title + '</div>' +
          (it.text ? '<div style="font-size:12.5px;color:rgba(255,255,255,0.6);line-height:1.45;margin-top:5px">' + (it.text.length > 120 ? it.text.substring(0,120) + '...' : it.text) + '</div>' : '') +
        '</div>';
      });
    });
    el.innerHTML = h;
  }catch(e){
    el.innerHTML = '<div class="card"><div style="color:var(--red);font-size:13px">Не удалось загрузить</div></div>';
  }
}

var _smmEditRow = 0;
// Картинка для поста в стиле BS: 1080x1350 для Instagram, 1080x1920 для Stories
async function generateSmmImage(){
  var title = document.getElementById('smmTitle').value.trim();
  var text = document.getElementById('smmText').value.trim();
  var rubric = document.getElementById('smmRubric').value;
  var platform = document.getElementById('smmPlatform').value;
  if(!title){ showToast('Нужен заголовок'); return; }

  var W = 1080, H = (platform === 'Instagram' || platform === 'Threads') ? 1350 : 1920;
  var canvas = document.createElement('canvas');
  canvas.width = W; canvas.height = H;
  var ctx = canvas.getContext('2d');
  var GOLD = '#FFFFFF';

  var grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, '#131313');
  grad.addColorStop(0.5, '#0a0a0a');
  grad.addColorStop(1, '#000000');
  ctx.fillStyle = grad; ctx.fillRect(0, 0, W, H);

  var glow = ctx.createRadialGradient(540, H*0.3, 40, 540, H*0.3, 700);
  glow.addColorStop(0, 'rgba(255,255,255,0.14)');
  glow.addColorStop(1, 'rgba(255,255,255,0)');
  ctx.fillStyle = glow; ctx.fillRect(0, 0, W, H);

  // Спираль ДНК справа
  ctx.save();
  ctx.globalAlpha = 0.1; ctx.strokeStyle = GOLD; ctx.lineWidth = 3;
  for(var s = 0; s < 2; s++){
    ctx.beginPath();
    for(var y = 0; y <= H; y += 8){
      var x = 1000 + Math.sin((y/150) + s*Math.PI) * 50;
      if(y === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  ctx.restore();

  // Логотип
  ctx.textAlign = 'center';
  var logoEl = window._bsLogoImg || document.querySelector('.hero-logo-real img');
  var top = 150;
  if(logoEl && logoEl.complete && logoEl.naturalWidth){
    try{
      var lw = 240, lh = lw * (logoEl.naturalHeight/logoEl.naturalWidth);
      ctx.drawImage(logoEl, 540 - lw/2, 90, lw, lh);
      top = 90 + lh + 50;
    }catch(e){}
  } else {
    ctx.fillStyle = '#fff'; ctx.font = '800 40px Manrope, Arial, sans-serif';
    ctx.letterSpacing = '5px'; ctx.fillText('BUSINESS SURGERY', 540, 150);
    ctx.letterSpacing = '0px'; top = 200;
  }

  // Рубрика
  ctx.fillStyle = GOLD; ctx.font = '700 24px Manrope, Arial, sans-serif';
  ctx.letterSpacing = '4px';
  ctx.fillText(rubric.toUpperCase(), 540, top);
  ctx.letterSpacing = '0px';

  // Заголовок
  ctx.fillStyle = '#ffffff';
  var titleSize = title.length > 60 ? 52 : (title.length > 35 ? 62 : 74);
  ctx.font = '900 ' + titleSize + 'px Manrope, Arial, sans-serif';
  var titleLines = wrapText(ctx, title, 880, 5);
  var ty = top + 110;
  titleLines.slice(0, 5).forEach(function(line, i){
    ctx.fillText(line, 540, ty + i * (titleSize + 14));
  });
  var afterTitle = ty + titleLines.slice(0,5).length * (titleSize + 14);

  // Текст
  if(text){
    var maxLines = H > 1500 ? 14 : 9;
    var bodySize = text.length > 420 ? 30 : 34;
    ctx.fillStyle = 'rgba(255,255,255,0.78)';
    ctx.font = '400 ' + bodySize + 'px Manrope, Arial, sans-serif';
    var bodyLines = wrapText(ctx, text.replace(/\s*\n+\s*/g, ' '), 860, maxLines);
    bodyLines.forEach(function(line, i){
      ctx.fillText(line, 540, afterTitle + 66 + i * (bodySize + 14));
    });
  }

  // Подвал
  ctx.fillStyle = 'rgba(255,255,255,0.18)';
  ctx.fillRect(140, H - 190, 800, 1);
  ctx.fillStyle = GOLD; ctx.font = '700 28px Manrope, Arial, sans-serif';
  ctx.fillText('Разбор бизнеса за 60 минут', 540, H - 130);
  ctx.fillStyle = '#ffffff'; ctx.font = '800 36px Manrope, Arial, sans-serif';
  ctx.fillText('BXCLUB.KZ', 540, H - 80);
  ctx.fillStyle = 'rgba(255,255,255,0.45)'; ctx.font = '400 24px Manrope, Arial, sans-serif';
  ctx.fillText('@business.surgery', 540, H - 40);

  canvas.toBlob(async function(blob){
    try{
      if(navigator.canShare){
        var file = new File([blob], 'bs_post.png', {type:'image/png'});
        if(navigator.canShare({files:[file]})){
          await navigator.share({files:[file], text: title});
          return;
        }
      }
    }catch(e){ if(e && e.name === 'AbortError') return; }
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url; a.download = 'bs_post_' + platform.toLowerCase() + '.png';
    document.body.appendChild(a); a.click(); document.body.removeChild(a);
    setTimeout(function(){ URL.revokeObjectURL(url); }, 1500);
    showToast('Картинка ' + W + 'x' + H + ' сохранена');
  }, 'image/png');
}

function copySmmText(){
  var title = document.getElementById('smmTitle').value.trim();
  var text = document.getElementById('smmText').value.trim();
  var full = (title ? title + '\n\n' : '') + text;
  if(!full){ showToast('Пусто'); return; }
  try{
    navigator.clipboard.writeText(full).then(function(){
      showToast('Текст скопирован');
    }, function(){ prompt('Скопируйте:', full); });
  }catch(e){ prompt('Скопируйте:', full); }
}

async function publishCarouselNow(){
  var title = document.getElementById('smmTitle').value.trim();
  var slidesRaw = (document.getElementById('smmSlides') || {}).value || '';
  var caption = document.getElementById('smmText').value.trim();
  if(!slidesRaw.trim()){ showToast('Сначала сгенерируйте слайды'); return; }
  if(!confirm('Опубликовать карусель в Threads?')) return;

  showToast('Собираем слайды...');

  // Разбор слайдов: "1. Заголовок\nТекст"
  var blocks = slidesRaw.split(/\n\s*\n/).filter(function(b){ return b.trim(); });
  if(!blocks.length){ showToast('Слайды не разобрались'); return; }
  if(blocks.length > 10) blocks = blocks.slice(0, 10);

  var urls = [];
  try{
    for(var i = 0; i < blocks.length; i++){
      var lines = blocks[i].split('\n');
      var head = lines[0].replace(/^\d+[.)]\s*/, '').trim();
      var body = lines.slice(1).join(' ').trim();
      var dataUrl = await buildSlideImage(head, body, i + 1, blocks.length);

      showToast('Загружаем ' + (i+1) + ' из ' + blocks.length);
      var res = await appPost({
        bsAction: 'uploadImage',
        image: dataUrl,
        fileName: 'car_' + Date.now() + '_' + (i+1) + '.png'
      });
      if(!res || !res.ok) throw new Error(res && res.error || 'Загрузка не удалась');
      urls.push(res.url);
    }

    showToast('Публикуем в Threads...');
    // Хостингу нужно время отдать файлы
    await new Promise(function(r){ setTimeout(r, 4000); });

    var pub = await callAction('publishCarousel', {
      urls: JSON.stringify(urls),
      caption: (title ? title + '\n\n' : '') + caption
    });
    if(pub && pub.ok){
      showToast('Карусель опубликована, слайдов ' + pub.slides);
      closeModal('smmModal');
      silentSync();
    } else {
      showToast('' + ((pub && pub.error) || 'Ошибка публикации'));
    }
  }catch(e){
    showToast('' + e.message);
  }
}

async function buildSlideImage(head, body, num, total){
  // Один слайд карусели в стиле BS, размер 1080x1350
  var W = 1080, H = 1350;
  var canvas = document.createElement('canvas');
  canvas.width = W; canvas.height = H;
  var ctx = canvas.getContext('2d');
  var GOLD = '#FFFFFF';

  var grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, '#131313'); grad.addColorStop(0.5, '#0a0a0a'); grad.addColorStop(1, '#000');
  ctx.fillStyle = grad; ctx.fillRect(0, 0, W, H);

  var glow = ctx.createRadialGradient(540, 400, 40, 540, 400, 700);
  glow.addColorStop(0, 'rgba(255,255,255,0.13)'); glow.addColorStop(1, 'rgba(255,255,255,0)');
  ctx.fillStyle = glow; ctx.fillRect(0, 0, W, H);

  // Спираль ДНК
  ctx.save(); ctx.globalAlpha = 0.1; ctx.strokeStyle = GOLD; ctx.lineWidth = 3;
  for(var s = 0; s < 2; s++){
    ctx.beginPath();
    for(var y = 0; y <= H; y += 8){
      var x = 1000 + Math.sin((y/150) + s*Math.PI) * 50;
      if(y === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  ctx.restore();

  ctx.textAlign = 'center';

  // Логотип
  var logo = window._bsLogoImg || document.querySelector('.hero-logo-real img');
  var top = 150;
  if(logo && logo.complete && logo.naturalWidth){
    try{
      var lw = 210, lh = lw * (logo.naturalHeight / logo.naturalWidth);
      ctx.drawImage(logo, 540 - lw/2, 80, lw, lh);
      top = 80 + lh + 60;
    }catch(e){}
  }

  // Номер слайда
  ctx.fillStyle = 'rgba(255,255,255,0.28)';
  ctx.font = '800 22px Manrope, Arial, sans-serif';
  ctx.letterSpacing = '3px';
  ctx.fillText(num + ' / ' + total, 540, top);
  ctx.letterSpacing = '0px';

  // Заголовок
  ctx.fillStyle = '#fff';
  var hs = head.length > 40 ? 62 : 76;
  ctx.font = '900 ' + hs + 'px Manrope, Arial, sans-serif';
  var hl = wrapText(ctx, head, 880, 4);
  var hy = top + 130;
  hl.forEach(function(l, i){ ctx.fillText(l, 540, hy + i * (hs + 14)); });
  var after = hy + hl.length * (hs + 14);

  // Текст
  if(body){
    ctx.fillStyle = 'rgba(255,255,255,0.75)';
    var bs = body.length > 260 ? 32 : 38;
    ctx.font = '400 ' + bs + 'px Manrope, Arial, sans-serif';
    var bl = wrapText(ctx, body, 860, 9);
    bl.forEach(function(l, i){ ctx.fillText(l, 540, after + 70 + i * (bs + 16)); });
  }

  // Подвал
  ctx.fillStyle = 'rgba(255,255,255,0.16)';
  ctx.fillRect(180, H - 150, 720, 1);
  ctx.fillStyle = GOLD;
  ctx.font = '700 26px Manrope, Arial, sans-serif';
  ctx.fillText(num === total ? 'Разбор бизнеса за 60 минут · bxclub.kz' : 'BUSINESS SURGERY', 540, H - 95);
  ctx.fillStyle = 'rgba(255,255,255,0.4)';
  ctx.font = '400 24px Manrope, Arial, sans-serif';
  ctx.fillText('@business.surgery', 540, H - 55);

  return canvas.toDataURL('image/png');
}

async function publishSmmNow(){
  if(!_smmEditRow){ showToast('Сначала сохраните'); return; }
  if(!confirm('Опубликовать пост в канал BS прямо сейчас?')) return;
  showToast('Публикуем...');
  try{
    var r = await callAction('publishSmm', {row: _smmEditRow});
    if(r && r.ok){
      showToast('Опубликовано в канале');
      closeModal('smmModal');
      silentSync();
    } else {
      showToast('' + ((r && r.error) || 'Ошибка'));
    }
  }catch(e){ showToast('' + e.message); }
}

function setSmmPlatform(p){
  document.getElementById('smmPlatform').value = p;
  document.querySelectorAll('[data-plat]').forEach(function(b){
    b.classList.toggle('selected', b.dataset.plat === p);
  });
  // Threads и Telegram: только текст, без карусели
  var fg = document.getElementById('smmFormatGroup');
  if(fg) fg.style.display = (p === 'Threads' || p === 'Telegram') ? 'none' : 'block';
  if(p === 'Threads' || p === 'Telegram') setSmmFormat('Пост');
  var pr = document.getElementById('smmPublishRow');
  if(pr) pr.style.display = (p === 'Telegram' && _smmEditRow) ? 'block' : 'none';
}

function setSmmFormat(f){
  document.getElementById('smmFormat').value = f;
  document.querySelectorAll('[data-fmt]').forEach(function(b){
    b.classList.toggle('selected', b.dataset.fmt === f);
  });
  var sg = document.getElementById('smmSlidesGroup');
  if(sg) sg.style.display = (f === 'Карусель') ? 'block' : 'none';
  var cr = document.getElementById('smmCarouselRow');
  if(cr) cr.style.display = (f === 'Карусель' && _smmEditRow) ? 'block' : 'none';
  var tl = document.getElementById('smmTextLabel');
  if(tl) tl.textContent = (f === 'Карусель') ? 'Текст под публикацией' : (f === 'Reels' ? 'Сценарий Reels' : 'Текст публикации');
}

var _smmGenInFlight = false;
async function generateSmmContent(){
  if(_smmGenInFlight) return;
  var platform = document.getElementById('smmPlatform').value;
  var format = document.getElementById('smmFormat').value;
  var rubric = document.getElementById('smmRubric').value;

  _smmGenInFlight = true;
  showToast('Пишем текст...');

  try{
    // Генерация идёт на сервере: браузер не может обращаться к модели напрямую
    var r = await callAction('generateSmm', {platform: platform, format: format, rubric: rubric});
    if(!r || r.error){
      showToast('' + ((r && r.error) || 'Не получилось'));
      return;
    }
    document.getElementById('smmTitle').value = r.title || '';
    if(format === 'Карусель' && r.slides){
      var slidesText = (r.slides || []).map(function(s, i){
        return (i+1) + '. ' + (s.h || '') + '\n' + (s.t || '');
      }).join('\n\n');
      var sl = document.getElementById('smmSlides');
      if(sl) sl.value = slidesText;
      document.getElementById('smmText').value = r.caption || '';
    } else {
      document.getElementById('smmText').value = r.text || '';
    }
    var ar = document.getElementById('smmActionsRow');
    if(ar) ar.style.display = 'flex';
    if(r.source === 'template'){
      showToast('Готово. Взята заготовка, подключите ключ для генерации');
    } else {
      showToast('Готово. Проверьте и сохраните');
    }
  }catch(e){
    showToast('Ошибка: ' + e.message);
  }finally{
    _smmGenInFlight = false;
  }
}

function openSmmModal(){
  _smmEditRow = 0;
  ['smmTitle','smmText','smmSlides'].forEach(function(id){ var e = document.getElementById(id); if(e) e.value = ''; });
  var d = document.getElementById('smmDate');
  if(d){ var n = new Date(); n.setDate(n.getDate()+1); d.value = String(n.getDate()).padStart(2,'0') + '.' + String(n.getMonth()+1).padStart(2,'0') + '.' + n.getFullYear(); }
  setSmmPlatform('Instagram');
  setSmmFormat('Пост');
  var rb = document.getElementById('smmRubric'); if(rb) rb.value = 'Кейс резидента';
  var st = document.getElementById('smmStatus'); if(st) st.value = 'Идея';
  document.getElementById('smmModalTitle').textContent = 'Новая публикация';
  ['smmDeleteRow','smmActionsRow','smmPublishRow'].forEach(function(id){
    var e = document.getElementById(id); if(e) e.style.display = 'none';
  });
  openModal('smmModal');
}

function editSmm(row){
  var it = ((cache.smm && cache.smm.items) || []).find(function(x){ return x.row === row; });
  if(!it) return;
  _smmEditRow = row;
  document.getElementById('smmTitle').value = it.title || '';
  document.getElementById('smmText').value = it.text || '';
  document.getElementById('smmDate').value = it.date || '';
  document.getElementById('smmRubric').value = it.rubric || 'Кейс резидента';
  document.getElementById('smmStatus').value = it.status || 'Идея';
  setSmmPlatform(it.platform || 'Instagram');
  var fmt = (it.link && it.link.indexOf('Карусель') === 0) ? 'Карусель' : 'Пост';
  setSmmFormat(fmt);
  if(fmt === 'Карусель'){
    var sl = document.getElementById('smmSlides');
    if(sl) sl.value = (it.link || '').replace(/^Карусель:\s*/, '');
  }
  document.getElementById('smmModalTitle').textContent = 'Публикация';
  var dr = document.getElementById('smmDeleteRow'); if(dr) dr.style.display = 'block';
  var ar = document.getElementById('smmActionsRow'); if(ar) ar.style.display = 'flex';
  var pr = document.getElementById('smmPublishRow');
  if(pr) pr.style.display = (it.platform === 'Telegram') ? 'block' : 'none';
  openModal('smmModal');
}

var _smmInFlight = false;
async function submitSmm(){
  if(_smmInFlight) return;
  var title = document.getElementById('smmTitle').value.trim();
  if(!title){ showToast('Сгенерируйте или впишите заголовок'); return; }
  _smmInFlight = true;
  var fmt = document.getElementById('smmFormat').value;
  var slides = (document.getElementById('smmSlides') || {}).value || '';
  closeModal('smmModal');
  try{
    var r = await callAction('saveSmm', {
      row: _smmEditRow,
      title: title,
      text: document.getElementById('smmText').value.trim(),
      link: fmt === 'Карусель' ? ('Карусель: ' + slides) : fmt,
      date: document.getElementById('smmDate').value.trim(),
      platform: document.getElementById('smmPlatform').value,
      rubric: document.getElementById('smmRubric').value,
      status: document.getElementById('smmStatus').value
    });
    if(r && r.ok){ showToast('Сохранено'); silentSync(); }
    else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(function(){ _smmInFlight = false; }, 1200); }
}

async function deleteSmm(){
  if(!_smmEditRow) return;
  if(!confirm('Удалить публикацию?')) return;
  closeModal('smmModal');
  try{
    var r = await callAction('deleteSmm', {row: _smmEditRow});
    if(r && r.ok){ showToast('Удалено'); refreshData(); }
  }catch(e){}
}

let _custdevCache = null;

async function renderCustdev(){
  const el = document.getElementById('custdevContent');
  if(!el) return;
  
  // Показываем загрузку только если нет кэша
  if(!_custdevCache){
    el.innerHTML = '<div class="card" style="text-align:center;padding:40px"><div style="font-size:14px;color:var(--text-muted)">Загружаем ответы...</div></div>';
  }
  
  try{
    const res = await callAction('getCustdevResponses', {});
    _custdevCache = res || {responses:[], byResident:{}};
  }catch(e){
    el.innerHTML = '<div class="card"><div style="color:var(--red)">Ошибка загрузки: '+e.message+'</div></div>';
    return;
  }
  
  const responses = _custdevCache.responses || [];
  const byRes = _custdevCache.byResident || {};
  
  // Получаем список всех резидентов из кэша для отображения тех у кого нет ответов
  const allResidents = (cache.residents || []).filter(r => r && r.name && !r.isAdmin && !r.isTeam);
  
  // Подсчёт общей статистики
  const totalRes = allResidents.length;
  const responded = Object.keys(byRes).length;
  const responseRate = totalRes > 0 ? Math.round(responded * 100 / totalRes) : 0;
  
  let html = '';
  
  // Карточка общей статистики + аналитика
  html += '<div class="card" style="margin-bottom:14px">';
  html += '<div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px;margin-bottom:14px">';
  html += '<div><div style="font-size:22px;font-weight:800;color:var(--gold)">'+totalRes+'</div><div style="font-size:11px;color:var(--text-muted);letter-spacing:1px;text-transform:uppercase">Резидентов</div></div>';
  html += '<div><div style="font-size:22px;font-weight:800;color:#fff">'+responded+'</div><div style="font-size:11px;color:var(--text-muted);letter-spacing:1px;text-transform:uppercase">Ответили</div></div>';
  html += '<div><div style="font-size:22px;font-weight:800;color:'+(responseRate>=50?'#FFFFFF':'#FF6B6F')+'">'+responseRate+'%</div><div style="font-size:11px;color:var(--text-muted);letter-spacing:1px;text-transform:uppercase">Конверсия</div></div>';
  html += '</div>';
  
  // Аналитика от AI (заглушка с реальным разбором текста)
  if(responses.length > 0){
    const analysis = analyzeCustdevResponses(responses);
    html += '<div style="padding:14px;background:rgba(255,255,255,0.06);border-left:3px solid var(--gold);border-radius:0 10px 10px 0;margin-top:10px">';
    html += '<div style="font-size:11px;font-weight:800;color:var(--gold);letter-spacing:1.5px;text-transform:uppercase;margin-bottom:6px">Аналитика</div>';
    html += '<div style="font-size:13px;color:#fff;font-weight:600;line-height:1.5">'+analysis+'</div>';
    html += '</div>';
  }
  
  html += '<button class="btn btn-secondary" style="margin-top:14px;width:100%" onclick="triggerNPS()">Запустить новый опрос</button>';
  html += '</div>';
  
  // Карточки резидентов с ответами
  if(allResidents.length === 0){
    html += '<div class="card" style="text-align:center;padding:30px"><div style="color:var(--text-muted)">Нет резидентов</div></div>';
  } else {
    allResidents.forEach(function(r){
      const myResp = byRes[r.name] || [];
      const hasResp = myResp.length > 0;
      
      html += '<div class="card" style="margin-bottom:10px;padding:0;overflow:hidden;cursor:pointer" onclick="toggleCustdevRes(this)">';
      html += '<div style="padding:16px 18px;display:flex;align-items:center;gap:14px">';
      html += '<div style="width:44px;height:44px;border-radius:50%;background:'+(hasResp?'#FFFFFF':'rgba(255,255,255,0.06)')+';color:'+(hasResp?'#000':'var(--text-muted)')+';display:flex;align-items:center;justify-content:center;font-weight:800;font-size:18px;flex-shrink:0">'+(r.name||'?')[0]+'</div>';
      html += '<div style="flex:1;min-width:0">';
      html += '<div style="font-weight:800;color:#fff;font-size:15px">'+escapeHtml(r.name||'')+'</div>';
      html += '<div style="font-size:12px;color:var(--text-muted);margin-top:2px">'+(hasResp ? myResp.length+' ответов · последний '+myResp[myResp.length-1].date : 'Нет ответов')+'</div>';
      html += '</div>';
      html += '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--text-muted)" stroke-width="2.5" stroke-linecap="round" class="custdev-arrow" style="transition:transform 0.2s"><polyline points="6 9 12 15 18 9"/></svg>';
      html += '</div>';
      
      if(hasResp){
        html += '<div class="custdev-responses" style="display:none;padding:0 18px 16px 18px;border-top:1px solid rgba(255,255,255,0.06)">';
        myResp.forEach(function(resp){
          html += '<div style="margin-top:14px;padding:14px 16px;background:rgba(0,0,0,0.3);border-radius:10px">';
          html += '<div style="font-size:11px;color:var(--gold);font-weight:800;letter-spacing:1.5px;text-transform:uppercase;margin-bottom:6px">'+resp.date+'</div>';
          html += '<div style="font-size:14px;color:#fff;font-weight:600;line-height:1.5;white-space:pre-wrap">'+escapeHtml(resp.text)+'</div>';
          html += '</div>';
        });
        html += '</div>';
      }
      html += '</div>';
    });
  }
  
  el.innerHTML = html;
}

function toggleCustdevRes(card){
  if(!card){console.log('toggleCustdev: no card'); return;}
  const resp = card.querySelector('.custdev-responses');
  const arrow = card.querySelector('.custdev-arrow');
  if(!resp){
    // У резидента нет ответов - показываем уведомление
    showToast('У этого резидента ещё нет ответов');
    return;
  }
  const isOpen = resp.style.display === 'block';
  resp.style.display = isOpen ? 'none' : 'block';
  if(arrow) arrow.style.transform = isOpen ? '' : 'rotate(180deg)';
}

function escapeHtml(s){
  if(!s) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function analyzeCustdevResponses(responses){
  // Простая аналитика: ищем повторяющиеся темы, тон, частые слова
  if(!responses || !responses.length) return 'Пока нет данных для анализа.';
  
  const totalChars = responses.reduce((s,r) => s + (r.text||'').length, 0);
  const avgLen = Math.round(totalChars / responses.length);
  
  // Топ слов (упрощённая ключевая аналитика)
  const allText = responses.map(r => (r.text||'').toLowerCase()).join(' ');
  const stopWords = new Set(['это','что','как','для','при','без','над','под','или','тоже','еще','уже','если','когда','очень','может','чтобы','свой','свою','свои','свое','моя','мой','мое','мои','наш','наша','наше','наши','ваш','ваша','ваше','ваши','быть','есть','была','были','будет','будем','будут','этого','этому','этим','этой','тебя','тебе','меня','мне','нет','да','же','ну','вот','так','тут','там','где','куда','откуда']);
  const words = allText.split(/[^а-яёa-z0-9]+/i).filter(w => w.length >= 4 && !stopWords.has(w));
  const wordCount = {};
  words.forEach(w => wordCount[w] = (wordCount[w]||0) + 1);
  const topWords = Object.entries(wordCount).sort((a,b) => b[1]-a[1]).slice(0,5).filter(([w,c]) => c >= 2);
  
  // Тон ответов (простой: позитивные vs негативные ключи)
  const positive = ['хорошо','супер','отлично','полезно','помогло','круто','спасибо','рад','довол','нрав','прекрасно','лучш','рост','прибыль','прорыв','прогресс'];
  const negative = ['плохо','не понял','непонятно','сложно','тяжело','устал','не успева','хуже','разочаров','не вижу','зря','скучно'];
  let pos = 0, neg = 0;
  responses.forEach(r => {
    const t = (r.text||'').toLowerCase();
    positive.forEach(k => { if(t.indexOf(k) >= 0) pos++; });
    negative.forEach(k => { if(t.indexOf(k) >= 0) neg++; });
  });
  const tone = pos > neg*2 ? 'Преимущественно позитивный' : (neg > pos ? 'Есть негативные сигналы' : 'Сбалансированный');
  
  let txt = responses.length + ' ответ' + (responses.length>1?'ов':'') + ' · средняя длина ' + avgLen + ' симв.';
  txt += '\n' + tone;
  if(topWords.length) txt += '\nЧастые темы: ' + topWords.map(([w,c]) => w+' ('+c+')').join(', ');
  
  return txt;
}

async function triggerNPS(){
  if(!confirm('Запустить NPS опрос для всех резидентов?\n\nЗащита от спама: повторный запуск возможен раз в 30 дней.')) return;
  showToast('Отправляем опрос...');
  try{
    const res = await callAction('runNPS', {});
    if(res && res.ok){
      showToast('Опрос отправлен');
    } else {
      showToast('' + (res && res.error || 'Не удалось отправить'));
    }
  }catch(e){
    showToast('' + e.message);
  }
}



function renderSubscribers(){
  var el=document.getElementById('page-subscribers');
  if(!el)return;
  if(!isRealAdmin||viewAs!=='admin'){el.innerHTML='';return;}

  el.innerHTML = `
    <div class="section-head">
      <div class="section-title">Подписчики канала</div>
      <button class="icon-btn" onclick="checkChannelMembership()" title="Обновить и сверить" style="width:36px;height:36px">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
      </button>
    </div>
    <div style="font-size:12px;color:#888;padding:0 4px 14px;line-height:1.45">
      Только реальные подписчики <b style="color:#fff">@bsurgery_kz</b>. Кнопка обновления сверяет список с Telegram.
    </div>
    <div id="subscribersList"><div class="empty">Загрузка...</div></div>
  `;
  reloadSubscribers();
}

async function checkChannelMembership(){
  showToast('Проверяем подписчиков...');
  try{
    var r = await callAction('checkChannelMembership', {});
    if(r && r.ok){
      showToast('Проверено '+r.checked+' · в канале '+r.member+' · вышло '+r.left);
      reloadSubscribers();
    } else {
      showToast('Ошибка проверки');
    }
  }catch(e){
    showToast(''+e.message);
  }
}

async function reloadSubscribers(){
  var box=document.getElementById('subscribersList');
  if(!box)return;
  box.innerHTML='<div class="empty">Загрузка...</div>';
  try{
    var r=await callAction('getSubscribers', {});
    var list=(r&&r.subscribers)||[];
    var needCheck=(r&&r.needCheck);
    window._subscribers=list;
    if(!list.length){
      if(needCheck){
        box.innerHTML='<div class="empty"><div class="em-icon"></div><div class="em-title">Список не проверен</div><div class="em-text">Нажми кнопку <b>Проверить канал</b> вверху чтобы Telegram сверил кто реально подписан на @bsurgery_kz</div></div>';
      } else {
        box.innerHTML='<div class="empty"><div class="em-icon"></div><div class="em-title">В канале никого</div><div class="em-text">Telegram проверил - подписчиков канала @bsurgery_kz нет</div></div>';
      }
      return;
    }
    // Группируем по статусу
    var groups={'Подписчик':[],'Записался на разбор':[],'Резидент':[],'Отказ':[]};
    list.forEach(function(s){
      var st=s.status||'Подписчик';
      if(!groups[st])groups[st]=[];
      groups[st].push(s);
    });
    var html='';
    var order=['Записался на разбор','Подписчик','Резидент','Отказ'];
    order.forEach(function(st){
      if(!groups[st]||!groups[st].length)return;
      html += '<div style="margin-top:14px;font-size:11px;letter-spacing:1.5px;color:var(--muted);text-transform:uppercase;padding:0 4px 8px">'+st+' · '+groups[st].length+'</div>';
      html += '<div class="card">';
      groups[st].forEach(function(s,i){
        var hasQual = s.niche || s.revenue || s.goal;
        var subInfo = [s.niche, s.revenue?s.revenue+' млн':'', s.leadmagnet].filter(Boolean).join(' · ');
        html += '<div class="list-item" onclick="openSubscriberDetail(\''+s.chatId+'\')" style="cursor:pointer">'+
          '<div class="list-avatar" style="background:rgba(255,255,255,0.08);color:var(--white)">'+(s.name?s.name[0].toUpperCase():'?')+'</div>'+
          '<div class="list-content">'+
            '<div class="list-name">'+(s.name||'?')+(s.username?' @'+s.username:'')+'</div>'+
            '<div class="list-sub">'+(subInfo||'Нет данных')+'</div>'+
          '</div>'+
          '<div class="list-right">'+
            '<div class="list-date">'+s.date+'</div>'+
            (hasQual?'<div class="list-amount" style="color:var(--green);font-size:11px">✓ КВАЛ</div>':'')+
          '</div>'+
        '</div>';
      });
      html += '</div>';
    });
    box.innerHTML=html;
  }catch(e){
    box.innerHTML='<div class="empty">Ошибка загрузки</div>';
  }
}

function openSubscriberDetail(chatId){
  var s=(window._subscribers||[]).find(function(x){return x.chatId===chatId});
  if(!s)return;
  document.getElementById('detailTitle').textContent=s.name||'Подписчик';
  document.getElementById('detailContent').innerHTML = `
    <div class="card">
      <div class="profile-row"><span>Дата подписки</span><strong>${s.date||'-'}</strong></div>
      <div class="profile-row"><span>Telegram</span><strong>${s.username?'@'+s.username:'-'}</strong></div>
      <div class="profile-row"><span>Chat ID</span><strong>${s.chatId}</strong></div>
      <div class="profile-row"><span>Источник</span><strong>${s.source||'-'}</strong></div>
      <div class="profile-row"><span>Лид-магнит</span><strong>${s.leadmagnet||'-'}</strong></div>
      <div class="profile-row"><span>Ниша</span><strong>${s.niche||'не указано'}</strong></div>
      <div class="profile-row"><span>Доход (млн ₸)</span><strong>${s.revenue||'не указано'}</strong></div>
      <div class="profile-row"><span>Цель</span><strong style="font-size:13px">${s.goal||'не указано'}</strong></div>
      <div class="profile-row"><span>Статус</span><strong>${s.status||'Подписчик'}</strong></div>
    </div>
    <button class="btn btn-secondary" onclick="v11writeSub('${String(s.chatId||'').replace(/[^0-9]/g,'')}')">Написать</button>
    ${s.status!=='Резидент' ? `<button class="btn btn-primary" onclick="openConvertModal('${s.chatId}','${(s.name||'').replace(/'/g,'')}')">Перевести в резиденты</button>` : ''}
    ${s.status!=='Резидент' ? `<button class="btn btn-danger" onclick="banSubscriberFromChannel('${s.chatId}', '${(s.name||'').replace(/'/g,'')}')">Удалить с канала</button>` : ''}
  `;
  showDetail();
}

function openConvertModal(chatId, name){
  // Открываем форму добавления резидента предзаполненную
  closeDetail();
  setTimeout(function(){
    openModal('addResidentModal');
    initAddResident();
    document.getElementById('arName').value = name;
    // Сохраняем chatId в скрытом поле. используем window
    window._convertingChatId = chatId;
    document.querySelector('#addResidentModal .modal-title').textContent = 'Перевод в резиденты';
  }, 200);
}

async function banSubscriberFromChannel(chatId, name){
  if(!confirm('Удалить ' + (name||'пользователя') + ' с канала?\n\nПосле бана он не сможет вернуться без разблокировки.'))return;
  var res = await callAction('banFromChannel', {chatId:chatId});
  if(res && res.ok){
    closeDetail();
    showToast('Удалён с канала');
    reloadSubscribers();
  } else {
    showToast('Ошибка: ' + (res && res.error || 'не удалось'));
  }
}

window._resFilter = window._resFilter || 'all';
window._resQuery = '';
function v3openResidents(f){ window._resFilter = f || 'all'; window._resQuery = ''; showPage('residents'); window.scrollTo(0, 0); }
function v3setResFilter(f){ window._resFilter = f; renderResidents(); }
function v3clubOf(){ return allResidents.filter(function(r){ return !r.isAdmin && !r.isTeam; }); }

function renderResidents(){
  allResidents = (cache.residents||[]).filter(r=>!r.isFired&&r.name!=='Тест'&&r.name!=='Тест2');
  var team = allResidents.filter(function(r){ return r.isAdmin || r.isTeam; });
  var club = v3clubOf();
  const el = document.getElementById('residentsContent');
  if(!el) return;
  window._teamResidentsList = team;
  el.innerHTML = navRole() === 'admin' ? v3adminResidents(team, club) : v3clubPage(team, club);
}

function v3resGroups(club){
  var miss = {}; v3missingReports().forEach(function(r){ miss[r.name] = 1; });
  return {
    debt: club.filter(function(r){ return (Number(r.debt)||0) > 0; }),
    noreport: club.filter(function(r){ return miss[r.name]; }),
    renew: club.filter(v3needsRenew)
  };
}
function v3filteredClub(club){
  var f = window._resFilter || 'all', q = String(window._resQuery || '').toLowerCase().trim();
  var list = club;
  if(f !== 'all'){
    var names = {}; (v3resGroups(club)[f] || []).forEach(function(r){ names[r.name] = 1; });
    list = list.filter(function(r){ return names[r.name]; });
  }
  if(q) list = list.filter(function(r){ var p = v3prof(r) || {}; return (r.name + ' ' + (r.niche || p.niche || '') + ' ' + (r.bio || p.bio || '')).toLowerCase().indexOf(q) >= 0; });
  if(f === 'debt') list = list.slice().sort(function(a,b){ return (Number(b.debt)||0) - (Number(a.debt)||0); });
  return list;
}
function v3resStatus(r, meet){
  var parts = [r.format || 'Офлайн'];
  var g = Number(r.meetingsGranted)||0, d = Number(r.meetingsDone)||0;
  if(g) parts.push(d + ' из ' + g + ' ' + v3plural(g, 'встречи', 'встреч', 'встреч'));
  if(meet) parts.push('встреча сегодня ' + (meet.time || ''));
  else if(!r.isExcluded && !v3reportSent(r)) parts.push('нет отчёта');
  return parts.join(' · ');
}
function v3resListHTML(club){
  var list = v3filteredClub(club);
  window._currentResidentsList = list;
  if(!list.length) return '<div class="v2-empty">Не найдено</div>';
  var meets = {}; (cache.schedule||[]).forEach(function(ev){ if(v3isToday(ev.date) && !meets[ev.res]) meets[ev.res] = ev; });
  return list.map(function(r, i){
    var debt = Number(r.debt)||0;
    return '<div class="v2-row" style="padding:12px 14px" onclick="openResByIdx(' + i + ')">' + v3av(r, 40) +
      '<div class="m"><b class="v3-n15">' + v2esc(r.name) + '</b><span>' + v2esc(v3resStatus(r, meets[r.name])) + '</span></div>' +
      '<div class="v3-amt"><div class="num' + (debt > 0 ? ' red' : '') + '">' + v2money(debt) + '</div><div class="v3-cap">' + v3debtCaption(r) + '</div></div></div>';
  }).join('');
}
function v3adminResidents(team, club){
  var g = v3resGroups(club), f = window._resFilter || 'all';
  var chips = [['all', 'Все'], ['debt', 'Должники · ' + g.debt.length], ['noreport', 'Без отчёта · ' + g.noreport.length], ['renew', 'Продление · ' + g.renew.length]];
  var h = v3headBig(club.length + ' ' + v3plural(club.length, 'активный', 'активных', 'активных'), 'Резиденты',
    '<button class="v3-btn" onclick="openModal(\'addResidentModal\');initAddResident()">' + navIcon('plus', 14, 2.2) + 'Добавить</button>');
  // Отчёты, штрафы, посещения и задачи открываются отсюда (в «Меню» их больше нет)
  var ufN = (cache.fines || []).filter(function(x){ return x.status === 'Не оплатил'; }).length;
  h += '<div class="v9-links">' +
    '<button onclick="showPage(\'reports\')">Отчёты' + (g.noreport.length ? ' <b>' + g.noreport.length + '</b>' : '') + '</button>' +
    '<button onclick="showPage(\'fines\')">Штрафы' + (ufN ? ' <b>' + ufN + '</b>' : '') + '</button>' +
    '<button onclick="showPage(\'visits\')">Посещения</button>' +
    '<button onclick="showPage(\'restasks\')">Задачи</button></div>';
  if(team.length){
    h += '<div class="v2-h">Команда</div><div class="v2-card">' + team.map(function(r, i){
      return '<div class="v2-row" style="padding:10px 14px" onclick="openResByIdx(' + (10000 + i) + ')">' + v3av(r, 32, true) + '<div class="m"><b>' + v2esc(r.name) + '</b></div><div class="v3-cap">' + v2esc(r.format || 'Команда') + '</div></div>';
    }).join('') + '</div>';
  }
  h += '<div class="v3-search" style="margin-top:16px">' + navIcon('search', 18) + '<input type="text" placeholder="Имя, бизнес или ниша" aria-label="Поиск" value="' + v2esc(window._resQuery || '') + '" oninput="filterRes(this.value)"></div>';
  if(f !== 'all'){
    var fl = chips.filter(function(x){ return x[0] === f; })[0];
    if(fl) h += '<div class="v3-row-sb" style="margin:12px 2px 0"><div class="v3-cap" style="font-size:13px">' + fl[1] + '</div><div class="v3-txtbtn" style="font-size:13px;color:#FFFFFF" onclick="v3setResFilter(\'all\')">Показать всех</div></div>';
  }
  h += '<div class="v2-card" id="resList" style="margin-top:12px">' + v3resListHTML(club) + '</div>';
  return h;
}
function v3clubListHTML(club){
  var q = String(window._resQuery || '').toLowerCase().trim();
  var list = q ? club.filter(function(r){ var p = v3prof(r) || {}; return (r.name + ' ' + (r.niche || p.niche || '')).toLowerCase().indexOf(q) >= 0; }) : club;
  window._currentResidentsList = list;
  if(!list.length) return '<div class="v2-empty">Не найдено</div>';
  return list.map(function(r, i){
    var p = v3prof(r) || {};
    return '<div class="v2-row" style="padding:12px 14px" onclick="openResByIdx(' + i + ')">' + v3av(r, 40) +
      '<div class="m"><b class="v3-n15">' + v2esc(r.name) + '</b><span>' + v2esc(r.niche || p.niche || r.format || 'Резидент BS') + '</span></div>' +
      '<div class="ic" style="color:#6E6E6E">' + navIcon('chev', 16) + '</div></div>';
  }).join('');
}
function v3clubPage(team, club){
  var logo = bsLogoSrc();
  var h = v3headBig(club.length + ' ' + v3plural(club.length, 'резидент', 'резидента', 'резидентов'), 'Клуб');
  h += '<div class="v2-card v2-about" style="margin-top:16px" onclick="showPage(\'about\')">' + (logo ? '<img src="' + logo + '" alt="business surgery">' : '') + '<div class="sep"></div>' +
    '<div style="flex:1;min-width:0"><div style="font-size:14px;font-weight:800">О проекте</div><div style="font-size:12px;color:#9A9A9A;margin-top:2px">Как всё устроено, правила клуба, пригласить</div></div>' +
    '<div style="color:#6E6E6E">' + navIcon('chev', 16) + '</div></div>';
  if(team.length){
    h += '<div class="v2-h">Команда клуба</div><div class="v2-card">' + team.map(function(r, i){
      var role = /берек/i.test(r.name) ? 'Трекер · материалы' : 'Трекер · разборы';
      return '<div class="v2-row" onclick="openResByIdx(' + (10000 + i) + ')">' + v3av(r, 40, true) + '<div class="m"><b>' + v2esc(r.name) + '</b><span>' + role + '</span></div>' +
        '<button class="v3-icbtn" aria-label="Написать" onclick="event.stopPropagation();v3write(window._teamResidentsList[' + i + '])">' + navIcon('msg', 20) + '</button></div>';
    }).join('') + '</div>';
  }
  h += '<div class="v2-h">Резиденты · ' + club.length + '</div>';
  h += '<div class="v3-search">' + navIcon('search', 18) + '<input type="text" placeholder="Имя или ниша" aria-label="Поиск" value="' + v2esc(window._resQuery || '') + '" oninput="filterRes(this.value)"></div>';
  h += '<div class="v2-card" id="resList" style="margin-top:10px">' + v3clubListHTML(club) + '</div>';
  return h;
}

// Совместимость: прежний список карточками
function residentsHTML(list, kind){
  if(kind === 'team'){ window._teamResidentsList = list; return v3clubListHTML(list); }
  return v3clubListHTML(list);
}

function v3feed(r){
  var items = [];
  (cache.logs || []).forEach(function(l){
    if(!v3isMine(l, r)) return;
    var txt = String(l.text || '').replace(/\s+/g, ' ').trim().slice(0, 70);
    items.push({t: v3dateTs(l.date, l.time), title: 'Отчёт сдан' + (l.time ? ' · ' + l.time : ''), sub: (v3isToday(l.date) ? 'Сегодня' : String(l.date || '')) + (txt ? ' · «' + txt + '…»' : ''), red: false});
  });
  (cache.fines || []).forEach(function(f){
    if(f.name !== r.name) return;
    items.push({t: v3dateTs(f.date), title: 'Штраф ' + fmt(f.amount) + ' · ' + String(f.kind || '').toLowerCase(), sub: String(f.date || '') + ' · ' + String(f.status || '').toLowerCase(), red: f.status === 'Не оплатил'});
  });
  items.sort(function(a, b){ return b.t - a.t; });
  if(!items.length) return '';
  return '<div class="v2-h">Лента</div><div class="v3-feed">' + items.slice(0, 5).map(function(x){
    return '<div class="it"><i' + (x.red ? ' class="r"' : '') + '></i><div style="min-width:0"><b>' + v2esc(x.title) + '</b><span>' + v2esc(x.sub) + '</span></div></div>';
  }).join('') + '</div>';
}

function openResidentProfile(r0){
  var r = Object.assign({}, v3prof(r0) || {}, r0);
  var adm = isRealAdmin && viewAs === 'admin';
  var team = !!(r.isAdmin || r.isTeam);
  window._v3CardRes = r0;
  document.getElementById('detailTitle').textContent = adm ? (team ? 'Команда' : 'Резиденты') : 'Клуб';
  var sub;
  if(adm && !team){
    sub = [r.format || 'Офлайн', r.tariff ? 'тариф ' + v2money(r.tariff) : '', r.cyclesPaid ? 'цикл ' + r.cyclesPaid : ''].filter(Boolean).join(' · ');
  } else {
    sub = r.niche || (team ? 'Команда Business Surgery' : 'Резидент Business Surgery');
  }
  var h = '<div class="v3-card-head">' + v3av(r, 60, true) + '<div style="min-width:0"><div class="v3-name">' + v2esc(r.name) + '</div><div class="v3-sub" style="margin-top:3px">' + v2esc(sub) + '</div></div></div>';
  if(adm && !team){
    h += '<div class="v3-acts">' +
      '<button class="on" onclick="v3payFor()">' + navIcon('plus', 20) + 'Оплата</button>' +
      '<button onclick="v3fineFor()">' + navIcon('alert', 20) + 'Штраф</button>' +
      '<button onclick="v3meetFor()">' + navIcon('cal', 20) + 'Встреча</button>' +
      '<button onclick="v3write(window._v3CardRes)">' + navIcon('msg', 20) + 'Написать</button></div>';
    var debt = Number(r.debt) || 0;
    var uf = v3unpaidFines(r.name);
    var debtCap = debt <= 0 ? 'оплачено' : ((Number(r.debtRenew) || 0) > 0 ? 'продление ' + v2money(r.debtRenew) : (uf.length ? 'штраф ' + String(uf[uf.length - 1].date || '').slice(0, 5) : 'долг'));
    var nm = (cache.schedule || []).filter(function(ev){ return ev.res === r.name && parseScheduleKey(ev) >= Date.now() - 3600000; })
      .sort(function(a, b){ return parseScheduleKey(a) - parseScheduleKey(b); })[0];
    var tasks = ((cache.resTasks && cache.resTasks.tasks) || []).filter(function(t){ return t.name === r.name; });
    var td = tasks.filter(function(t){ return t.status === 'Выполнена'; }).length;
    h += '<div class="v2-card" style="margin-top:10px;overflow:hidden"><div class="v3-grid2" style="border:none;margin:-1px 0 0 -1px">' +
      '<div onclick="closeDetail();showPage(\'fines\')" style="cursor:pointer"><div class="v3-k">Долг · штрафы ›</div><div class="v3-v num' + (debt > 0 ? ' red' : '') + '">' + v2money(debt) + '</div><div class="v3-cap">' + v2esc(debtCap) + '</div></div>' +
      '<div><div class="v3-k">Встречи</div><div class="v3-v num">' + (Number(r.meetingsDone) || 0) + ' из ' + (Number(r.meetingsGranted) || 0) + '</div><div class="v3-cap">' + (nm ? v2esc(String(nm.date).slice(0, 5) + ' ' + (nm.time || '')) : 'нет записи') + '</div></div>' +
      '<div><div class="v3-k">Отчёты · 30 дней</div><div class="v3-v num">' + v3reports30(r) + '/30</div><div class="v3-cap">' + (v3reportSent(r) ? 'сегодня сдан' : 'сегодня нет') + '</div></div>' +
      '<div onclick="closeDetail();showPage(\'restasks\')" style="cursor:pointer"><div class="v3-k">Задачи ›</div><div class="v3-v num">' + td + ' из ' + tasks.length + '</div><div class="v3-cap">' + (tasks.length - td) + ' в работе</div></div>' +
    '</div></div>';
  }
  if(r.nextRenewal){
    var rc = '';
    try{
      var p = String(r.nextRenewal).split('.');
      var days = Math.round((new Date(p[2], p[1] - 1, p[0]) - new Date()) / 86400000);
      rc = '<div class="num' + (days < 0 ? ' red' : '') + '" style="font-size:13px;font-weight:800">' + (days < 0 ? 'просрочено ' + Math.abs(days) + ' дн' : 'через ' + days + ' дн') + '</div>';
    }catch(e){}
    h += '<div class="v2-card v3-row-sb" style="margin-top:10px;padding:13px 16px"><div><div class="v3-lbl">ОПЛАЧЕНО ДО</div><div style="font-size:15px;font-weight:800;margin-top:3px">' + v2esc(r.nextRenewal) + '</div></div>' + rc + '</div>';
  }
  if(r.bio) h += '<div class="v2-h">О себе</div><div class="v2-card v3-text">' + v2esc(r.bio) + '</div>';
  if(r.help) h += '<div class="v2-h">Чем может помочь</div><div class="v2-card v3-text">' + v2esc(r.help) + '</div>';
  var canW = !adm && v11canWrite(r);
  if(r.instagram || r.phone || canW){
    h += '<div class="v2-h">Контакты</div><div class="v2-card">' +
      (canW ? v3row('msg', 'Написать', v11tgUser(r) ? 'Чат в Telegram' : 'Сообщение через бота BS', 'v11write(window._v3CardRes)') : '') +
      (r.instagram ? '<a class="v2-row" href="https://instagram.com/' + v2esc(String(r.instagram).replace('@', '')) + '" target="_blank"><div class="ic">' + navIcon('me', 20) + '</div><div class="m"><b>Instagram</b><span>' + v2esc(r.instagram) + '</span></div><div class="ic" style="color:#6E6E6E">' + navIcon('chev', 16) + '</div></a>' : '') +
      (r.phone ? '<a class="v2-row" href="https://wa.me/' + String(r.phone).replace(/[^0-9]/g, '') + '" target="_blank"><div class="ic">' + navIcon('msg', 20) + '</div><div class="m"><b>WhatsApp</b><span>' + v2esc(r.phone) + '</span></div><div class="ic" style="color:#6E6E6E">' + navIcon('chev', 16) + '</div></a>' : '') +
    '</div>';
  }
  if(!r.bio && !r.help && !r.instagram && !r.phone && !(adm && !team)){
    h += '<div class="v2-card v2-empty" style="margin-top:16px"><div style="font-size:14px;font-weight:800;color:#fff;margin-bottom:4px">Профиль не заполнен</div>Резидент пока не добавил информацию о себе</div>';
  }
  if(adm && !team) h += v3feed(r);
  h += '<div id="resTasksBlock"></div><div id="resWheelBlock"></div>';
  document.getElementById('detailContent').innerHTML = h;
  showDetail();
  // Задачи и колесо резидента (для админа): подгружаем асинхронно
  if(isRealAdmin && viewAs === 'admin' && !r.isAdmin){
    loadResidentTasks(r.name);
    loadResidentWheel(r.name);
  }
}

function loadResidentTasks(name){
  const el = document.getElementById('resTasksBlock');
  if(!el) return;
  try{
    window._v3TaskName = name;
    const tasks = ((cache.resTasks && cache.resTasks.tasks) || []).filter(function(t){ return t.name === name; });
    const active = tasks.filter(t => t.status !== 'Выполнена');
    const doneCount = tasks.length - active.length;
    let h = '<div class="v2-h">Задачи · ' + active.length + ' в работе <span class="lnk" onclick="promptAddTask(window._v3TaskName)">+ Задача</span></div><div class="v2-card">';
    if(!tasks.length){
      h += '<div class="v2-empty">Задач нет. Добавьте после встречи.</div>';
    } else {
      active.forEach(function(t){
        h += '<div class="v3-task"><div class="v3-chk" style="cursor:default"></div><div class="m" style="cursor:default"><b>' + v2esc(t.task) + '</b><span>' + v2esc(t.date) + (t.isOwn ? ' · своя' : '') + '</span></div></div>';
      });
      if(doneCount) h += '<div class="v3-task"><div class="v3-chk on">' + V3_CHECK + '</div><div class="m" style="cursor:default"><b class="done">Выполнено: ' + doneCount + '</b></div></div>';
    }
    h += '</div>';
    el.innerHTML = h;
  }catch(e){
    el.innerHTML = '';
  }
}

function promptAddTask(name){
  const task = prompt('Задача для ' + name + ':');
  if(!task || !task.trim()) return;
  showToast('Создаём...');
  callAction('addResTask', {name: name, task: task.trim(), author: (user && user.first_name) || 'Админ'}).then(function(r){
    if(r && r.ok){
      showToast('Задача создана. Резидент уведомлён');
      loadResidentTasks(name);
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  });
}

function adminTaskAction(row, status, name){
  showToast('...');
  callAction('setResTaskStatus', {row: row, status: status}).then(function(r){
    if(r && r.ok){
      showToast(status === 'Принята' ? 'Принята. Резидент уведомлён' : 'Готово');
      loadResidentTasks(name);
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  });
}

function adminTaskReturn(row, name){
  const comment = prompt('Что доработать? (комментарий резиденту)');
  if(comment === null) return;
  showToast('...');
  callAction('setResTaskStatus', {row: row, status: 'Возвращена', comment: comment || ''}).then(function(r){
    if(r && r.ok){
      showToast('↩ Возвращена. Резидент уведомлён');
      loadResidentTasks(name);
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  });
}

function loadResidentWheel(name){
  const el = document.getElementById('resWheelBlock');
  if(!el) return;
  try{
    const d = (cache.wheelAll && cache.wheelAll[name]) || {businesses:[], life:[]};
    const dna = (d.dna && d.dna[0]) || null;
    const dnaPrev = (d.dna && d.dna[1]) || null;
    const life = (d.life && d.life[0]) || null;
    const lifePrev = (d.life && d.life[1]) || null;
    if(!dna && !life){
      el.innerHTML = '<div class="card"><div class="card-title" style="color:#FFFFFF">Колесо баланса</div><div style="font-size:13px;color:#888;padding:8px 0">Резидент ещё не делал замеров</div></div>';
      return;
    }
    let h = '';
    if(dna){
      h += '<div class="card" style="margin-bottom:12px"><div class="card-title" style="color:#FFFFFF;font-weight:800">ДНК бизнеса</div>';
      h += '<div style="font-size:12px;color:#888;margin-bottom:8px">'+dna.date+' · средний: <b style="color:#FFFFFF">'+wheelAvg(dna.values)+'</b></div>';
      h += drawRadar(WHEEL_DNA_AXES, dna.values, dnaPrev ? dnaPrev.values : null, '#FFFFFF') + '</div>';
    }
    if(life){
      h += '<div class="card"><div class="card-title" style="color:#FFFFFF;font-weight:800">Колесо баланса</div>';
      h += '<div style="font-size:12px;color:#888;margin-bottom:8px">'+life.date+' · средний: <b style="color:#FFFFFF">'+wheelAvg(life.values)+'</b></div>';
      h += drawRadar(WHEEL_LIFE_AXES, life.values, lifePrev ? lifePrev.values : null, '#FFFFFF') + '</div>';
    }
    el.innerHTML = h;
  }catch(e){
    el.innerHTML = '';
  }
}

function filterRes(q){
  window._resQuery = q || '';
  var el = document.getElementById('resList');
  if(!el) return;
  var club = v3clubOf();
  el.innerHTML = navRole() === 'admin' ? v3resListHTML(club) : v3clubListHTML(club);
}

function showResidentDetail(name){
  const r = allResidents.find(x=>x.name===name);
  if(!r) return;
  document.getElementById('detailTitle').textContent = r.name;
  const isOnline = r.format==='Онлайн';
  document.getElementById('detailContent').innerHTML = `
    <div class="profile-hero">
      <div class="profile-avatar-lg" ${r.avatar?`style="background-image:url('${r.avatar}')"`:''}>${r.avatar?'':r.name[0]}</div>
      <div class="profile-name-lg">${r.name}</div>
      <div class="profile-niche">${r.niche||'Резидент Business Surgery'}</div>
      <div class="profile-status">${isOnline?'Онлайн':'Офлайн'} · ${r.months||0} мес в BS</div>
    </div>
    ${r.bio?`<div class="card">
      <div class="card-title">О резиденте</div>
      <div style="font-size:13px;line-height:1.6">${r.bio}</div>
    </div>`:''}
    ${r.help?`<div class="card">
      <div class="card-title">Чем может помочь</div>
      <div style="font-size:13px;line-height:1.6">${r.help}</div>
    </div>`:''}
    ${r.instagram||r.phone?`<div class="card">
      <div class="card-title">Контакты</div>
      ${r.instagram?`<a class="btn btn-secondary" href="https://instagram.com/${r.instagram.replace('@','')}" target="_blank">${r.instagram}</a>`:''}
      ${r.phone?`<a class="btn btn-secondary" href="https://wa.me/${(r.phone||'').replace(/[^0-9]/g,'')}" target="_blank">WhatsApp</a>`:''}
    </div>`:''}
  `;
  document.getElementById('detailView').classList.add('show');
}

function showMeetingDetail(ev){
  const isOnline = ev.link && ev.link.includes('meet');
  const cd = countdownText(ev.date, ev.time);
  document.getElementById('detailTitle').textContent = 'Встреча';
  var canEdit = isRealAdmin && viewAs === 'admin';
  var confirmKey = 'confirmed_'+ev.res+'_'+ev.date+'_'+ev.time;
  var alreadyConfirmed = localStorage.getItem(confirmKey);
  document.getElementById('detailContent').innerHTML = `
    <div class="meeting upcoming">
      ${cd.text?`<div class="meeting-countdown">${cd.text}</div>`:''}
      <div class="meeting-title">Трекинг</div>
      <div style="font-family: 'Manrope', sans-serif; font-weight: 800; letter-spacing: -0.5px;font-size:36px;letter-spacing:1px;margin:8px 0">${ev.date}</div>
      <div style="font-size:18px;color:var(--g400);margin-bottom:16px">${ev.time||''}</div>
      <div class="meeting-row">${isOnline?'Формат: Онлайн':'Формат: Офлайн'}</div>
      <div class="meeting-row">Резидент: ${ev.res}</div>
      ${ev.link?`<div class="meeting-row" style="word-break:break-all">${isOnline?'':''} ${ev.link}</div>`:''}
    </div>
    ${isOnline&&ev.link?`<a class="btn btn-primary" href="${ev.link}" target="_blank">Подключиться к Meet</a>`:''}
    ${!canEdit?`<button class="btn btn-success" onclick="confirmMeeting('${ev.res}','${ev.date}','${ev.time}')" ${alreadyConfirmed?'disabled style="opacity:0.6"':''}>${alreadyConfirmed?'✓ Подтверждено':'Подтвердить участие'}</button>`:''}
    <button class="btn btn-secondary" onclick="addToCalendar('${ev.date}','${ev.time}','${ev.res}','${ev.link||''}')">В Google Calendar</button>
    ${canEdit?`<button class="btn btn-success" onclick="onMeetingDoneClick('${ev.res}','${ev.date}','${ev.time}',${isOnline?'true':'false'})">Встреча прошла</button>`:''}
    ${canEdit?`<button class="btn btn-secondary" onclick="openEditMeeting('${ev.res}','${ev.date}','${ev.time}')">Перенести</button>`:''}
    ${canEdit?`<button class="btn btn-danger" onclick="deleteMeetingConfirm('${ev.res}','${ev.date}','${ev.time}')">Удалить</button>`:''}
  `;
  showDetail();
}

// Изменить встречу
function openEditMeeting(res, oldDate, oldTime){
  // Сохраняем что меняем
  window._editingMeeting = {res, oldDate, oldTime};
  // Открываем модал расписания, но в режиме редактирования
  openModal('schedModal');
  initSchedModal();
  // Меняем тексты
  document.querySelector('#schedModal .modal-title').textContent = 'Изменить встречу';
  document.getElementById('schedResident').value = res;
  document.querySelector('#schedModal .btn-primary').textContent = 'Сохранить изменения';
  document.querySelector('#schedModal .btn-primary').setAttribute('onclick','submitEditMeeting()');
  closeDetail();
}

async function submitEditMeeting(){
  var info = window._editingMeeting;
  if(!info) return;
  var newDate = selDate;
  var newTime = selTime;
  closeModal('schedModal');
  showToast('Время изменено');
  // Локально обновляем кэш
  if(cache.schedule){
    cache.schedule.forEach(function(e){
      if(e.res===info.res && e.date===info.oldDate && e.time===info.oldTime){
        // Преобразуем dd.MM.yyyy → dd.MM
        var parts = newDate.split('.');
        e.date = parts[0]+'.'+parts[1];
        e.time = newTime;
      }
    });
    try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
  }
  renderAllPages();
  // Восстанавливаем модал в исходное
  document.querySelector('#schedModal .modal-title').textContent = 'Поставить расписание';
  document.querySelector('#schedModal .btn-primary').textContent = 'Создать встречу';
  document.querySelector('#schedModal .btn-primary').setAttribute('onclick','submitSchedule()');
  await callAction('updateMeeting', {
    oldRes: info.res,
    oldDate: info.oldDate,
    oldTime: info.oldTime,
    newDate: newDate,
    newTime: newTime
  });
  window._editingMeeting = null;
  loadAllData();
}


function meetingDone(res, date, time){
  if(!confirm('Встреча с '+res+' прошла?')) return;
  if(!window._deletedMeetingKeys) window._deletedMeetingKeys = [];
  window._deletedMeetingKeys.push(res+'|'+date+'|'+time);
  if(cache.schedule){
    cache.schedule = cache.schedule.filter(function(e){
      return !(e.res===res && e.date===date && e.time===time);
    });
    try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
  }
  closeDetail();
  showToast('Отмечаем встречу...');
  renderAllPages();
  // Вызываем правильный action: confirmMeeting (ставит галочку, лог, удаляет из расписания)
  callAction('confirmMeeting', {res:res, date:date, time:time, userId: user?.id||''}).then(function(result){
    if(result && result.ok){
      showToast('Встреча прошла, галочка поставлена');
    } else {
      showToast('Ошибка обработки');
    }
    setTimeout(loadAllData, 3000);
  });
}

function deleteMeetingConfirm(res, date, time){
  if(!confirm('Удалить встречу с '+res+' '+date+' '+time+'?')) return;
  // Помечаем как удалённую. чтобы не вернулась при loadAllData
  if(!window._deletedMeetingKeys) window._deletedMeetingKeys = [];
  window._deletedMeetingKeys.push(res+'|'+date+'|'+time);
  // Удаляем из локального кэша
  if(cache.schedule){
    cache.schedule = cache.schedule.filter(function(e){
      return !(e.res===res && e.date===date && e.time===time);
    });
    try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
  }
  closeDetail();
  showToast('Удаляется...');
  renderAllPages();
  callAction('deleteSchedule', {res, date, time}).then(function(result){
    if(result && result.ok){
      showToast('Встреча удалена');
    } else {
      showToast('Не удалось. проверьте таблицу');
    }
    setTimeout(loadAllData, 5000);
  });
}

function confirmMeeting(res, date, time){
  var key = 'confirmed_'+res+'_'+date+'_'+time;
  if(localStorage.getItem(key)){
    showToast('✓ Уже подтверждено');
    return;
  }
  localStorage.setItem(key, '1');
  showToast('Участие подтверждено');
  // Скрываем кнопку
  var btn = document.querySelector('button[onclick*="confirmMeeting"]');
  if(btn){
    btn.disabled = true;
    btn.innerHTML = '✓ Подтверждено';
    btn.style.opacity = '0.6';
  }
  // Резидент подтверждает участие для себя. Отметить встречу проведённой может только команда
}

function addToCalendar(date, time, res, link){
  const [d,m] = date.split('.').map(Number);
  const [h,min] = (time||'00:00').split(':').map(Number);
  const y = new Date().getFullYear();
  const start = new Date(y, m-1, d, h, min);
  const end = new Date(start.getTime() + 60*60*1000);
  const fmt2 = (dt) => dt.toISOString().replace(/-|:|\.\d+/g,'').substring(0,15)+'Z';
  const url = `https://calendar.google.com/calendar/render?action=TEMPLATE&text=${encodeURIComponent('Трекинг. '+res)}&dates=${fmt2(start)}/${fmt2(end)}&details=${encodeURIComponent(link||'')}`;
  window.open(url, '_blank');
}

function closeDetail(){
  document.getElementById('detailView').classList.remove('show');
  try{ v9updateBack(); }catch(e){}
}

function showDetail(){
  var dv = document.getElementById('detailView');
  dv.classList.add('show');
  dv.scrollTop = 0;
  setTimeout(function(){ dv.scrollTop = 0; }, 0);
  try{ v9updateBack(); }catch(e){}
}

// ─── RESIDENT VIEW ─────────────────────────────────────────────────────────
function getMyData(){
  const uid = String(user?.id||'');
  const all = cache.residents||[];
  if(viewAs==='resident' && isRealAdmin){
    // Admin previewing. show first non-test resident
    return all.find(r=>r.name!=='Тест'&&r.name!=='Тест2'&&!r.isFired) || all[0];
  }
  return all.find(r=>r.chatId===uid);
}

// ══════════ КОЛЕСО БАЛАНСА ══════════
const WHEEL_DNA_AXES = ["Стратегия","Финансы","Команда","Процессы","Продажи","Маркетинг","Продукт","Делегирование"];
const WHEEL_LIFE_AXES = ["Бизнес","Здоровье","Семья","Окружение","Личные финансы","Развитие","Отдых","Смысл"];
let _wheelData = null;
let _wheelFormType = 'dna';

function drawRadar(axes, current, previous, color, big){
  // SVG радар. big=true: крупная версия для детального просмотра
  // Широкий viewBox с запасом чтобы подписи не обрезались
  const W = 420, H = 380, cx = W/2, cy = H/2 + 4, maxR = big ? 128 : 112;
  const n = axes.length;
  const angle = i => (Math.PI * 2 * i / n) - Math.PI/2;
  const pt = (i, val) => {
    const r = maxR * val / 10;
    return [cx + r * Math.cos(angle(i)), cy + r * Math.sin(angle(i))];
  };
  let svg = `<svg viewBox="0 0 ${W} ${H}" style="width:100%;display:block;margin:0 auto">`;
  for(let ring = 2; ring <= 10; ring += 2){
    let pts = [];
    for(let i = 0; i < n; i++) pts.push(pt(i, ring).join(','));
    svg += `<polygon points="${pts.join(' ')}" fill="none" stroke="rgba(255,255,255,0.08)" stroke-width="1"/>`;
  }
  for(let i = 0; i < n; i++){
    const [x, y] = pt(i, 10);
    svg += `<line x1="${cx}" y1="${cy}" x2="${x}" y2="${y}" stroke="rgba(255,255,255,0.08)" stroke-width="1"/>`;
  }
  if(previous && previous.length === n){
    let pts = [];
    for(let i = 0; i < n; i++) pts.push(pt(i, previous[i]).join(','));
    svg += `<polygon points="${pts.join(' ')}" fill="rgba(255,255,255,0.05)" stroke="rgba(255,255,255,0.3)" stroke-width="1.5" stroke-dasharray="4 3"/>`;
  }
  if(current && current.length === n){
    let pts = [];
    for(let i = 0; i < n; i++) pts.push(pt(i, current[i]).join(','));
    svg += `<polygon points="${pts.join(' ')}" fill="${color}22" stroke="${color}" stroke-width="2.5" stroke-linejoin="round"/>`;
    for(let i = 0; i < n; i++){
      const [x, y] = pt(i, current[i]);
      svg += `<circle cx="${x}" cy="${y}" r="4" fill="${color}"/>`;
    }
  }
  // Подписи: с умным анкором и укладкой в границы
  for(let i = 0; i < n; i++){
    const a = angle(i);
    const lr = maxR + 16;
    let x = cx + lr * Math.cos(a);
    let y = cy + lr * Math.sin(a);
    let anchor = 'middle';
    if(Math.cos(a) > 0.35){ anchor = 'start'; x += 2; }
    else if(Math.cos(a) < -0.35){ anchor = 'end'; x -= 2; }
    // Вертикальная поправка для верхней/нижней подписи
    if(Math.sin(a) < -0.8) y -= 14;
    if(Math.sin(a) > 0.8) y += 8;
    const val = current && current[i] !== undefined ? current[i] : '';
    const label = axes[i].length > 14 ? axes[i].substring(0,13)+'…' : axes[i];
    svg += `<text x="${x}" y="${y}" text-anchor="${anchor}" font-size="12" font-weight="700" fill="rgba(255,255,255,0.78)">${label}</text>`;
    if(val !== ''){
      svg += `<text x="${x}" y="${y+14}" text-anchor="${anchor}" font-size="12" font-weight="800" fill="${color}">${val}</text>`;
    }
  }
  svg += '</svg>';
  return svg;
}

function wheelAvg(values){
  if(!values || !values.length) return 0;
  return Math.round(values.reduce((a,b)=>a+b,0) / values.length * 10) / 10;
}

// ══════════ ЗАДАЧИ РЕЗИДЕНТА ══════════
function v3taskSeg(active){
  return '<div class="v3-seg" style="grid-template-columns:repeat(2,minmax(0,1fr));margin-top:14px">' +
    '<button class="' + (active === 'tasks' ? 'on' : '') + '" onclick="v3setTaskSeg(\'tasks\')">Задания</button>' +
    '<button class="' + (active === 'cal' ? 'on' : '') + '" onclick="v3setTaskSeg(\'cal\')">Календарь</button></div>';
}
function v3setTaskSeg(seg){
  window._v3TaskSeg = seg;
  var pg = document.getElementById('page-mytasks');
  if(pg && pg.classList.contains('active')) loadMyTasks();
  else showPage('mytasks');
}
function v3progress(done, total, right){
  var pct = total ? Math.round(done / total * 100) : 0;
  return '<div class="v3-prog"><div class="v3-row-sb" style="align-items:baseline"><b>' + done + ' из ' + total + ' готово</b><span class="v3-cap" style="font-size:12px">' + (right || ('осталось ' + (total - done))) + '</span></div>' +
    '<div class="v3-bar"><i style="width:' + pct + '%"></i></div></div>';
}
function v3taskRow(done, text, meta, onCheck, extra){
  var box = done ? '<div class="v3-chk on">' + V3_CHECK + '</div>'
                 : '<div class="v3-chk"' + (onCheck ? ' onclick="' + onCheck + '"' : ' style="cursor:default"') + '></div>';
  return '<div class="v3-task">' + box + '<div class="m"' + (onCheck && !done ? ' onclick="' + onCheck + '"' : ' style="cursor:default"') + '><b' + (done ? ' class="done"' : '') + '>' + v2esc(text) + '</b>' +
    (meta ? '<span>' + v2esc(meta) + '</span>' : '') + '</div>' + (extra || '') + '</div>';
}
function loadMyTasks(){
  const el = document.getElementById('myTasksList');
  if(!el) return;
  // Палец на ячейке календаря: полную перерисовку откладываем, иначе тап потеряется
  if(window._v3TaskSeg === 'cal' && _v10p && document.getElementById('v10cal')){ _v10pend = true; return; }
  const me = getMyData();
  if(!me) return;
  try{
    var bd = window._myBoard, b = bd && bd.board;
    var btasks = (b && b.tasks) || [];
    var seg = window._v3TaskSeg === 'cal' ? 'cal' : 'tasks';
    const tasks = ((cache.resTasks && cache.resTasks.tasks) || []).filter(function(t){ return t.name === me.name; });
    var h = v3headBig(b && b.cycle ? 'Цикл ' + b.cycle + ' · синхронно с платформой' : 'Синхронно с платформой', 'Задачи');
    h += v3taskSeg(seg);
    if(!bd && !window._v3BoardTried){
      window._v3BoardTried = true;
      loadMyBoard(false).then(function(){ loadMyTasks(); });
    }
    if(seg === 'cal'){
      // Рабочий планировщик недели: тот же «Мой календарь», что на платформе
      h += v10calHTML(me);
    } else {
      // Все задания: свои и от трекера + задачи с доски цикла (отметки по ним ставит трекер)
      var active = tasks.filter(function(t){ return t.status !== 'Выполнена'; });
      var done = tasks.filter(function(t){ return t.status === 'Выполнена'; });
      var bOpen = btasks.filter(function(t){ return !t.done; }), bDone = btasks.filter(function(t){ return t.done; });
      window._v3TaskText = {};
      tasks.forEach(function(t){ window._v3TaskText[t.row] = t.task; });
      if(!tasks.length && !btasks.length){
        h += '<div class="v2-card v2-empty" style="margin-top:14px">' + (!bd ? 'Загружаем задачи...' : 'Заданий пока нет. Поставьте себе задачу или дождитесь трекинга.') + '</div>';
      } else {
        h += v3progress(done.length + bDone.length, tasks.length + btasks.length);
        var bRow = function(t){
          var due = t.due ? ' · срок ' + (v9dkey(t.due) ? v9dkey(t.due).split('-').reverse().slice(0, 2).join('.') : t.due) : '';
          return v3taskRow(t.done, t.title, 'от трекера · доска цикла' + (t.organ ? ' · ' + t.organ : '') + due, null);
        };
        h += '<div class="v2-card" style="margin-top:12px">' +
          active.map(function(t){
            var meta = (t.date || '') + (t.isOwn ? ' · своя' : ' · от трекера') + (t.recurring ? ' · постоянная' : '');
            var extra = t.isOwn ? '<div class="v3-tact"><button class="v3-icbtn" aria-label="Изменить" onclick="editOwnTask(' + t.row + ', window._v3TaskText[' + t.row + '])">' + navIcon('pen', 16) + '</button>' +
              '<button class="v3-icbtn" aria-label="Удалить" onclick="deleteOwnTask(' + t.row + ')">' + navIcon('x', 16) + '</button></div>' : '';
            return v3taskRow(false, t.task, meta, 'markTaskDone(' + t.row + ')', extra);
          }).join('') +
          bOpen.map(bRow).join('') +
          bDone.map(bRow).join('') +
          done.slice(-10).reverse().map(function(t){ return v3taskRow(true, t.task, (t.updated || t.date || '') + (t.isOwn ? ' · своя' : ' · от трекера'), null); }).join('') +
        '</div>';
        if(btasks.length) h += '<div class="v3-cap" style="margin:10px 4px 0;line-height:1.45">Задачи с доски цикла отмечает трекер на платформе' + (b && b.cycle ? ' · цикл ' + b.cycle : '') + '.</div>';
      }
      h += '<button class="v3-outline" style="margin-top:12px" onclick="openOwnTaskModal()">+ Добавить</button>';
    }
    el.innerHTML = h;
  }catch(e){
    console.error('mytasks', e);
    el.innerHTML = '<div class="v2-empty">Не удалось загрузить</div>';
  }
}

// ══════════ ЦЕНА НЕРЕШЁННЫХ ПРОБЛЕМ ══════════
function loadProblems(){
  const el = document.getElementById('problemsBody');
  if(!el) return;
  const me = getMyData();
  if(!me) return;
  try{
    const list = ((cache.problems && cache.problems.problems) || []).filter(function(p){ return p.name === me.name; });
    const open = list.filter(p => p.status === 'Открыта');
    const total = open.reduce(function(s,p){ return s + (p.cost||0); }, 0);

    if(!list.length){
      el.innerHTML = '<div style="font-size:12.5px;color:#888;line-height:1.45">Что вы не делаете, и во сколько это обходится каждый месяц?<br><span style="color:#666">Например: не веду личный бренд. 3 000 000 ₸/мес</span></div>';
      return;
    }

    let h = '';
    if(total > 0){
      h += '<div style="margin-bottom:12px"><div style="font-size:24px;font-weight:800;color:#FF6B6F;line-height:1">' + fmt(total) + ' ₸</div>' +
        '<div style="font-size:11px;color:#888;margin-top:2px">теряете каждый месяц</div></div>';
    }
    open.forEach(p => {
      h += `<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:10px;padding:9px 0;border-top:1px solid rgba(255,255,255,0.05)">
        <div style="flex:1;min-width:0">
          <div style="font-size:13px;color:#fff;line-height:1.35">${p.problem}</div>
          <div style="font-size:11px;color:#FF6B6F;font-weight:700;margin-top:2px">${fmt(p.cost)} ₸/мес</div>
        </div>
        <div style="display:flex;gap:10px;flex-shrink:0">
          <span onclick="resolveProblem(${p.row})" title="Решено" class="v3-txtbtn">Решено</span>
          <span onclick="removeProblem(${p.row})" title="Удалить" class="v3-txtbtn">Удалить</span>
        </div>
      </div>`;
    });
    const solved = list.filter(p => p.status === 'Решена');
    if(solved.length){
      h += '<div style="font-size:11px;color:#FFFFFF;font-weight:800;margin-top:10px">Решено: ' + solved.length + ' на ' + fmt(solved.reduce((s,p)=>s+p.cost,0)) + ' ₸/мес</div>';
    }
    el.innerHTML = h;
  }catch(e){ el.innerHTML = ''; }
}

function openProblemModal(){
  var t = document.getElementById('problemText'); if(t) t.value = '';
  var c = document.getElementById('problemCost'); if(c) c.value = '';
  openModal('problemModal');
}

let _problemInFlight = false;
async function submitProblem(){
  if(_problemInFlight) return;
  const me = getMyData();
  if(!me) return;
  const text = document.getElementById('problemText').value.trim();
  const cost = document.getElementById('problemCost').value.replace(/[^0-9]/g,'');
  if(!text){ showToast('Опиши проблему'); return; }
  _problemInFlight = true;
  closeModal('problemModal');
  try{
    const r = await callAction('saveProblem', {name: me.name, problem: text, cost: cost || '0'});
    if(r && r.ok){ showToast('Записано'); loadProblems(); }
    else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(()=>{ _problemInFlight = false; }, 1200); }
}

async function resolveProblem(row){
  try{
    patchCache('problems.problems', function(list){
      return (list||[]).map(function(p){ return p.row === row ? Object.assign({}, p, {status:'Решена'}) : p; });
    });
    loadProblems();
    const r = await callAction('setProblemStatus', {row: row, status: 'Решена'});
    if(r && r.ok){ showToast('Проблема решена'); silentSync(); }
  }catch(e){}
}

async function removeProblem(row){
  if(!confirm('Удалить запись?')) return;
  const me = getMyData();
  try{
    patchCache('problems.problems', function(list){
      return (list||[]).filter(function(p){ return p.row !== row; });
    });
    loadProblems();
    const r = await callAction('deleteProblem', {row: row, name: me.name});
    if(r && r.ok){ showToast('Удалено'); silentSync(); }
  }catch(e){}
}

function openOwnTaskModal(){
  window._editingTaskRow = null;
  var ta = document.getElementById('ownTaskText');
  if(ta) ta.value = '';
  var rec = document.getElementById('ownTaskRecurring');
  if(rec) rec.classList.remove('selected');
  window._ownTaskRecurring = false;
  var ttl = document.getElementById('ownTaskTitle');
  if(ttl) ttl.textContent = 'Своя задача';
  openModal('ownTaskModal');
}

function toggleOwnTaskRecurring(){
  window._ownTaskRecurring = !window._ownTaskRecurring;
  var rec = document.getElementById('ownTaskRecurring');
  if(rec) rec.classList.toggle('selected', window._ownTaskRecurring);
}

function editOwnTask(row, text){
  window._editingTaskRow = row;
  var ta = document.getElementById('ownTaskText');
  if(ta) ta.value = text;
  var ttl = document.getElementById('ownTaskTitle');
  if(ttl) ttl.textContent = 'Изменить задачу';
  openModal('ownTaskModal');
}

let _ownTaskInFlight = false;
async function submitOwnTask(){
  if(_ownTaskInFlight) return;
  const me = getMyData();
  if(!me) return;
  const text = document.getElementById('ownTaskText').value.trim();
  if(!text){ showToast('Напиши задачу'); return; }
  _ownTaskInFlight = true;
  closeModal('ownTaskModal');
  try{
    let r;
    if(window._editingTaskRow){
      r = await callAction('editOwnTask', {row: window._editingTaskRow, name: me.name, task: text});
    } else {
      r = await callAction('addOwnTask', {name: me.name, task: text, recurring: window._ownTaskRecurring ? '1' : '0'});
    }
    if(r && r.ok){ showToast('Сохранено'); loadMyTasks(); }
    else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(()=>{ _ownTaskInFlight = false; }, 1200); }
}

async function deleteOwnTask(row){
  if(!confirm('Удалить задачу?')) return;
  const me = getMyData();
  if(!me) return;
  try{
    const r = await callAction('deleteOwnTask', {row: row, name: me.name});
    if(r && r.ok){ showToast('Удалено'); loadMyTasks(); }
    else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
}


let _taskSubmitInFlight = false;
async function markTaskDone(row){
  // Резидент сам отмечает выполнение. Проверки трекером нет, статусы смотрят на трекинге
  if(_taskSubmitInFlight) return;
  _taskSubmitInFlight = true;
  try{
    patchCache('resTasks.tasks', function(list){
      return (list||[]).map(function(t){ return t.row === row ? Object.assign({}, t, {status:'Выполнена'}) : t; });
    });
    loadMyTasks();
    const r = await callAction('setResTaskStatus', {row: row, status: 'Выполнена'});
    if(r && r.ok){
      showToast('Отмечено');
      silentSync();
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(()=>{ _taskSubmitInFlight = false; }, 1200); }
}

function copyReferralLink(){
  const me = getMyData();
  const myId = user && user.id ? String(user.id) : '';
  if(!myId){ showToast('Не удалось определить твой ID'); return; }
  const link = 'https://t.me/bsurgery_bot?start=ref_' + myId;
  const text = 'Привет! Я в Business Surgery. это системная работа с бизнесом каждые 10 дней, не курс и не клуб. Мне зашло. Начни с разбора твоего бизнеса, вот ссылка: ' + link;
  try{
    navigator.clipboard.writeText(text).then(function(){
      showToast('Приглашение скопировано. Отправь другу');
    }, function(){
      prompt('Скопируй вручную:', text);
    });
  }catch(e){
    prompt('Скопируй вручную:', text);
  }
}

function renderWheel(){
  const el = document.getElementById('wheelContent');
  if(!el) return;
  // Админ проходит колёса для себя: результаты хранятся на устройстве, на платформу не уходят
  const adm = v11adminWheel();
  const me = getMyData() || (adm ? {name: (user && user.first_name) || 'Админ'} : null);
  if(!me){ el.innerHTML = '<div class="empty">Профиль не найден</div>'; return; }

  _wheelData = adm ? v11wheelStore() : ((cache.wheelAll && cache.wheelAll[me.name]) || {businesses:[], life:[], dnaAxes:WHEEL_DNA_AXES, lifeAxes:WHEEL_LIFE_AXES});

  const d = _wheelData || {};
  const businesses = d.businesses || [];
  const life = (d.life && d.life[0]) || null;
  const lifePrev = (d.life && d.life[1]) || null;
  const lifeAxes = d.lifeAxes || WHEEL_LIFE_AXES;

  let h = '';

  // ── ДНК: карточка на каждый бизнес ──
  if(!businesses.length){
    h += '<div class="card" style="margin-bottom:14px">';
    h += '<div class="card-title" style="color:#FFFFFF;font-weight:800;letter-spacing:0.5px;margin-bottom:4px">ДНК бизнеса</div>';
    h += '<div style="font-size:13px;color:#888;padding:20px 0;text-align:center">Ещё нет замеров.<br>Нажми "Оценить" и поставь первые оценки.</div>';
    h += '</div>';
  } else {
    businesses.forEach(function(biz, bi){
      const cur = biz.rows[0];
      const prev = biz.rows[1] || null;
      h += '<div class="card" style="margin-bottom:14px;cursor:pointer" onclick="openWheelDetail(\'dna\', \''+biz.name.replace(/'/g,"\\'")+'\')">';
      h += '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:4px">';
      h += '<div class="card-title" style="color:#FFFFFF;font-weight:800;letter-spacing:0.5px;margin:0">ДНК: '+biz.name+'</div>';
      h += '<div style="font-size:12px;color:#888">Подробнее ›</div>';
      h += '</div>';
      h += '<div style="font-size:12px;color:#888;margin-bottom:8px">Замер: '+cur.date.split(' ')[0]+(prev?' · пунктир: '+prev.date.split(' ')[0]:'')+' · средний: <b style="color:#FFFFFF">'+wheelAvg(cur.values)+'</b></div>';
      h += drawRadar(WHEEL_DNA_AXES, cur.values, prev ? prev.values : null, '#FFFFFF');
      h += '</div>';
    });
  }

  // ── Личное колесо ──
  h += '<div class="card" style="cursor:pointer" onclick="openWheelDetail(\'life\', \'\')">';
  h += '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:4px">';
  h += '<div class="card-title" style="color:#FFFFFF;font-weight:800;letter-spacing:0.5px;margin:0">Колесо баланса</div>';
  h += '<div style="display:flex;gap:12px;align-items:center">';
  if(!adm) h += '<div style="font-size:13px;color:#888" onclick="event.stopPropagation();openAxesEditor()">Оси</div>';
  h += (life?'<div style="font-size:12px;color:#888">Подробнее ›</div>':'');
  h += '</div></div>';
  if(life){
    h += '<div style="font-size:12px;color:#888;margin-bottom:8px">Замер: '+life.date.split(' ')[0]+(lifePrev?' · пунктир: '+lifePrev.date.split(' ')[0]:'')+' · средний: <b style="color:#FFFFFF">'+wheelAvg(life.values)+'</b></div>';
    h += drawRadar(lifeAxes, life.values, lifePrev ? lifePrev.values : null, '#FFFFFF');
    h += '<div style="margin-top:10px;padding:10px 12px;background:var(--card2);border-radius:8px;font-size:12px;color:#888;line-height:1.4">Ось <b style="color:#fff">Бизнес</b> считается сама: среднее всех твоих направлений ДНК. Нажмите «Оси», чтобы переименовать остальные оси.</div>';
  } else {
    h += '<div style="font-size:13px;color:#888;padding:20px 0;text-align:center">Ещё нет замеров.<br>Сначала оцени ДНК бизнеса, потом личное колесо.</div>';
  }
  h += '</div>';
  if(adm) h += '<div class="v3-cap" style="margin:12px 4px 0;line-height:1.45">Ваши замеры хранятся только на этом устройстве и не попадают в замеры клуба.</div>';

  el.innerHTML = h;
}

// ══════════ КОЛЕСО У АДМИНА: все резиденты ══════════
function renderWheelAdmin(){
  const el = document.getElementById('wheelAdminList');
  if(!el) return;
  const summary = (cache.wheelSummary && cache.wheelSummary.summary) || [];
  
  // Все активные резиденты (включая тех кто ещё не делал замеров)
  const allRes = (cache.residents||[]).filter(r=>!r.isFired&&!r.isAdmin&&!r.isTeam&&r.name!=='Тест'&&r.name!=='Тест2');
  const byName = {};
  summary.forEach(s => byName[s.name] = s);
  
  // Средние по клубу
  const withDna = summary.filter(s => s.dnaAvg !== null);
  const withLife = summary.filter(s => s.lifeAvg !== null);
  const clubDna = withDna.length ? Math.round(withDna.reduce((a,s)=>a+s.dnaAvg,0)/withDna.length*10)/10 : null;
  const clubLife = withLife.length ? Math.round(withLife.reduce((a,s)=>a+s.lifeAvg,0)/withLife.length*10)/10 : null;
  
  let h = '';
  h += '<div class="card" style="margin-bottom:14px"><div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px">';
  h += '<div><div style="font-size:22px;font-weight:800;color:#FFFFFF">'+(clubDna!==null?clubDna:'-')+'</div><div style="font-size:11px;color:#888;letter-spacing:1px;text-transform:uppercase">ДНК клуба</div></div>';
  h += '<div><div style="font-size:22px;font-weight:800;color:#FFFFFF">'+(clubLife!==null?clubLife:'-')+'</div><div style="font-size:11px;color:#888;letter-spacing:1px;text-transform:uppercase">Баланс клуба</div></div>';
  h += '<div><div style="font-size:22px;font-weight:800;color:#fff">'+withDna.length+'/'+allRes.length+'</div><div style="font-size:11px;color:#888;letter-spacing:1px;text-transform:uppercase">Заполнили</div></div>';
  h += '</div></div>';
  
  allRes.sort((a,b)=>(a.name||'').localeCompare(b.name||''));
  allRes.forEach(r => {
    const s = byName[r.name];
    const has = !!s;
    h += `<div class="card" style="margin-bottom:10px;cursor:pointer;padding:14px 16px" onclick="openAdminResWheel('${r.name.replace(/'/g,"\\'")}')">
      <div style="display:flex;align-items:center;gap:14px">
        <div style="width:44px;height:44px;border-radius:50%;background:${has?'rgba(255,255,255,0.15)':'rgba(255,255,255,0.05)'};color:${has?'#FFFFFF':'#555'};display:flex;align-items:center;justify-content:center;font-weight:800;font-size:18px;flex-shrink:0">${(r.name||'?')[0]}</div>
        <div style="flex:1;min-width:0">
          <div style="font-weight:800;color:#fff;font-size:15px">${r.name}</div>
          <div style="font-size:12px;color:#888;margin-top:2px">${has ? (s.total+' замеров · последний '+(s.dnaDate||s.lifeDate)) : 'Ещё не заполнял колесо'}</div>
        </div>
        <div style="display:flex;gap:14px;flex-shrink:0">
          <div style="text-align:center"><div style="font-size:17px;font-weight:800;color:${has&&s.dnaAvg!==null?'#FFFFFF':'#444'}">${has&&s.dnaAvg!==null?s.dnaAvg:'-'}</div><div style="font-size:9px;color:#888;letter-spacing:1px">ДНК${has&&s.dnaCount>1?' ×'+s.dnaCount:''}</div></div>
          <div style="text-align:center"><div style="font-size:17px;font-weight:800;color:${has&&s.lifeAvg!==null?'#FFFFFF':'#444'}">${has&&s.lifeAvg!==null?s.lifeAvg:'-'}</div><div style="font-size:9px;color:#888;letter-spacing:1px">ЛИЧНОЕ</div></div>
        </div>
      </div>
    </div>`;
  });
  
  el.innerHTML = h;
}

function openAdminResWheel(name){
  // Просмотр колёс резидента админом: выбор что смотреть
  var d = (cache.wheelAll && cache.wheelAll[name]) || null;
  if(!d){ showToast(name + ' ещё не заполнял колесо'); return; }
  
  const businesses = (d && d.businesses) || [];
  const hasLife = d && d.life && d.life.length;
  
  if(!businesses.length && !hasLife){
    showToast(name + ' ещё не заполнял колесо');
    return;
  }
  
  // Если один вариант - открываем сразу. Иначе выбор
  const options = [];
  businesses.forEach(b => options.push({type:'dna', biz:b.name, label:'ДНК: '+b.name}));
  if(hasLife) options.push({type:'life', biz:'', label:'Колесо баланса'});
  
  if(options.length === 1){
    openWheelDetail(options[0].type, options[0].biz, name);
    return;
  }
  
  // Мини-выбор через модалку detail: покажем кнопки выбора
  openModal('wheelDetailModal');
  const titleEl = document.getElementById('wheelDetailTitle');
  if(titleEl) titleEl.textContent = name;
  const body = document.getElementById('wheelDetailBody');
  let h = '<div style="font-size:12px;color:#888;margin-bottom:14px">Что посмотреть:</div>';
  options.forEach(o => {
    h += `<div onclick="openWheelDetail('${o.type}','${o.biz.replace(/'/g,"\\'")}','${name.replace(/'/g,"\\'")}')" style="padding:14px 16px;margin-bottom:8px;background:var(--card2);border-radius:10px;cursor:pointer;font-size:14px;font-weight:700;color:#fff">${o.label} ›</div>`;
  });
  body.innerHTML = h;
}

// ── Рекомендации по осям ──
const WHEEL_TIPS = {
  "Стратегия": "Нет ясной цели на 12 месяцев. Сформулируй 1 главную цель года и 3 шага квартала. Разбери на встрече BS.",
  "Финансы": "Раздели счета (операционка/налоги/резерв), заведи платёжный календарь на 60 дней. Чек-лист Финансы под контролем в боте.",
  "Команда": "Опиши 3-5 ключевых результатов для каждой роли и введи еженедельные 1-на-1. Чек-лист Команда без выгорания.",
  "Процессы": "Выпиши 3 самых повторяющихся процесса и опиши их пошагово. Один процесс в неделю.",
  "Продажи": "Замерь конверсию каждого этапа воронки. Чини самый слабый этап, а не всё сразу. Чек-лист Скрипты для WhatsApp.",
  "Маркетинг": "Определи 2 канала которые уже приводят клиентов и усиль их. Не распыляйся на новые.",
  "Продукт": "Спроси 5 клиентов: что нравится, чего не хватает, за что готовы платить больше. Ответы = план продукта.",
  "Делегирование": "Выпиши все свои задачи за неделю. Категории Б (может делать другой) передай с дедлайном.",
  "Бизнес": "Средний балл твоего ДНК бизнеса низкий. Смотри рекомендации в колесе ДНК и разбери слабые зоны на встрече.",
  "Здоровье": "Сон 7+ часов, 2-3 тренировки в неделю, чекап раз в год. Энергия владельца = энергия бизнеса.",
  "Семья": "Заведи один вечер в неделю без телефона и работы. Защити его в календаре как встречу.",
  "Окружение": "Окружение тянет или тормозит. Добавь 1-2 регулярных контакта с предпринимателями твоего уровня и выше.",
  "Личные финансы": "Плати себе фиксированную зарплату из бизнеса. Личная подушка на 6 месяцев расходов.",
  "Развитие": "1 книга или курс в месяц по слабой зоне ДНК. Внедряй одно, а не собирай знания.",
  "Отдых": "Выходной без работы раз в неделю. Отпуск 2 раза в год. Выгоревший владелец рушит бизнес быстрее конкурентов.",
  "Смысл": "Ответь письменно: зачем тебе этот бизнес кроме денег. Если ответа нет, это тема для разбора."
};
function wheelTip(axis){
  return WHEEL_TIPS[axis] || "Зона проседает. Вынеси её как тему на ближайшую встречу BS.";
}

// ── Детальный просмотр колеса ──
let _wheelDetailType = 'dna';
let _wheelDetailBiz = '';
let _wheelDetailName = null; // null = текущий пользователь; имя = просмотр админом
let _wheelHistory = null;
let _wheelHistoryIdx = 0;

function openWheelDetail(type, biz, resName){
  _wheelDetailType = type;
  _wheelDetailBiz = biz || '';
  _wheelDetailName = resName || null;
  _wheelHistoryIdx = 0;
  let targetName = resName;
  const admOwn = !resName && v11adminWheel();
  if(!targetName && !admOwn){
    const me = getMyData();
    if(!me) return;
    targetName = me.name;
  }
  openModal('wheelDetailModal');
  // Вся история уже в кеше. открывается мгновенно
  _wheelHistory = admOwn ? v11wheelStore() : ((cache.wheelAll && cache.wheelAll[targetName]) || {businesses:[], life:[]});
  renderWheelDetail();
}

function renderWheelDetail(){
  const d = _wheelHistory || {};
  const isDna = _wheelDetailType === 'dna';
  const axes = isDna ? (d.dnaAxes || WHEEL_DNA_AXES) : (d.lifeAxes || WHEEL_LIFE_AXES);
  let rows;
  if(isDna){
    // Мульти-бизнес: история конкретного направления
    const businesses = d.businesses || [];
    let biz = businesses.find(b => b.name === _wheelDetailBiz);
    if(!biz && businesses.length) biz = businesses[0];
    rows = biz ? biz.rows : [];
  } else {
    rows = d.life || [];
  }
  const color = isDna ? '#FFFFFF' : '#FFFFFF';
  const titleEl = document.getElementById('wheelDetailTitle');
  if(titleEl){
    let t = isDna ? ('ДНК' + (_wheelDetailBiz ? ': ' + _wheelDetailBiz : ' бизнеса')) : 'Колесо баланса';
    if(_wheelDetailName) t = _wheelDetailName + ' · ' + t;
    titleEl.textContent = t;
  }

  const body = document.getElementById('wheelDetailBody');
  if(!rows.length){
    body.innerHTML = '<div style="text-align:center;padding:30px;color:#888;font-size:13px">Нет замеров</div>';
    return;
  }

  const idx = Math.min(_wheelHistoryIdx, rows.length - 1);
  const cur = rows[idx];
  const prev = rows[idx + 1] || null;

  let h = '';
  // Радар крупный
  h += '<div style="font-size:12px;color:#888;margin-bottom:6px;text-align:center">' + cur.date + (prev ? ' · пунктир: ' + prev.date.split(' ')[0] : '') + ' · средний: <b style="color:' + color + '">' + wheelAvg(cur.values) + '</b></div>';
  h += drawRadar(axes, cur.values, prev ? prev.values : null, color, true);

  // Список осей с баллами и дельтой
  h += '<div style="margin-top:14px">';
  axes.forEach((ax, i) => {
    const v = cur.values[i];
    const pv = prev ? prev.values[i] : null;
    let delta = '';
    if(pv !== null && pv !== undefined){
      const diff = Math.round((v - pv) * 10) / 10;
      if(diff > 0) delta = '<span style="color:#FFFFFF;font-weight:800;font-size:12px"> ↑' + diff + '</span>';
      else if(diff < 0) delta = '<span style="color:#FF6B6F;font-weight:800;font-size:12px"> ↓' + Math.abs(diff) + '</span>';
    }
    const barW = Math.round(v * 10);
    h += `<div style="margin-bottom:10px">
      <div style="display:flex;justify-content:space-between;margin-bottom:4px">
        <span style="font-size:13px;font-weight:700;color:#fff">${ax}</span>
        <span style="font-size:14px;font-weight:800;color:${color}">${v}${delta}</span>
      </div>
      <div style="height:6px;background:rgba(255,255,255,0.07);border-radius:3px"><div style="height:6px;width:${barW}%;background:${color};border-radius:3px"></div></div>
    </div>`;
  });
  h += '</div>';

  // Рекомендации по слабым зонам (балл <= 5)
  const weak = [];
  axes.forEach((ax, i) => { if(cur.values[i] <= 5) weak.push(ax); });
  if(weak.length){
    h += '<div style="margin-top:16px;padding:14px;background:rgba(255,255,255,0.06);border-left:3px solid #FFFFFF;border-radius:0 10px 10px 0">';
    h += '<div style="font-size:11px;font-weight:800;color:#FFFFFF;letter-spacing:1.5px;text-transform:uppercase;margin-bottom:8px">Рекомендации</div>';
    weak.forEach(ax => {
      h += '<div style="margin-bottom:10px"><b style="color:#fff;font-size:13px">' + ax + '</b><div style="font-size:13px;color:rgba(255,255,255,0.75);line-height:1.45;margin-top:2px">' + wheelTip(ax) + '</div></div>';
    });
    h += '</div>';
  } else {
    h += '<div style="margin-top:16px;padding:12px;background:rgba(255,255,255,0.08);border-radius:10px;font-size:13px;color:#FFFFFF;font-weight:700;text-align:center">Все зоны выше 5. Сильно! </div>';
  }

  // ИИ-анализ динамики (если между замерами больше недели)
  if(rows.length > 1){
    h += '<div id="wheelAiBox" style="margin-top:16px"></div>';
    setTimeout(analyzeWheelDynamics, 60);
  }

  // Блок прибыли (только для ДНК и только своё колесо)
  if(isDna && !_wheelDetailName){
    h += '<div id="profitBlock" style="margin-top:18px"><div style="font-size:12px;color:#888;text-align:center">Загружаем прибыль...</div></div>';
    setTimeout(loadProfitChart, 50);
  }

  // История: все замеры, ничего не удаляется
  if(rows.length > 1){
    h += '<div style="margin-top:18px"><div style="font-size:11px;font-weight:800;color:#888;letter-spacing:1.5px;text-transform:uppercase;margin-bottom:8px">История замеров (' + rows.length + ')</div>';
    rows.forEach((r, i) => {
      const active = i === idx;
      h += `<div onclick="_wheelHistoryIdx=${i};renderWheelDetail()" style="display:flex;justify-content:space-between;align-items:center;padding:11px 14px;margin-bottom:6px;background:${active?'rgba(255,255,255,0.1)':'var(--card2)'};border:1px solid ${active?'#FFFFFF':'transparent'};border-radius:10px;cursor:pointer">
        <span style="font-size:13px;font-weight:700;color:#fff">${r.date}</span>
        <span style="font-size:14px;font-weight:800;color:${color}">${wheelAvg(r.values)}</span>
      </div>`;
    });
    h += '</div>';
  }

  body.innerHTML = h;
}

// ── График прибыли (в детальном ДНК) ──
async function loadProfitChart(){
  const el = document.getElementById('profitBlock');
  if(!el) return;
  const me = getMyData();
  if(!me){ el.innerHTML=''; return; }
  try{
    const r = await callAction('getProfit', {name: me.name});
    const recs = (r && r.records) || [];
    let h = '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">';
    h += '<div style="font-size:11px;font-weight:800;color:#FFFFFF;letter-spacing:1.5px;text-transform:uppercase">Прибыль по месяцам</div>';
    h += '<div class="section-action" style="font-size:12px" onclick="promptAddProfit()">+ Месяц</div></div>';
    
    if(!recs.length){
      h += '<div style="font-size:13px;color:#888;padding:8px 0">Добавь первый замер: выручка и прибыль за месяц. График покажет твой рост в BS.</div>';
    } else {
      h += drawProfitChart(recs);
      const last = recs[recs.length-1];
      let sub = 'Последний: ' + last.date + ' · прибыль <b style="color:#FFFFFF">' + fmt(last.profit) + '</b>';
      if(recs.length >= 2){
        const first = recs[0];
        if(first.profit > 0){
          const growth = Math.round((last.profit - first.profit) / first.profit * 100);
          sub += ' · с первого замера ' + (growth >= 0 ? '<b style="color:#FFFFFF">+' + growth + '%</b>' : '<b style="color:#FF6B6F">' + growth + '%</b>');
        }
      }
      h += '<div style="font-size:12px;color:#888;margin-top:6px">' + sub + '</div>';
    }
    el.innerHTML = h;
  }catch(e){ el.innerHTML = ''; }
}

function drawProfitChart(recs){
  // Простой SVG line-chart: прибыль по замерам
  const W = 380, H = 150, padL = 8, padR = 8, padT = 14, padB = 22;
  const vals = recs.map(r => r.profit);
  const maxV = Math.max(...vals, 1);
  const minV = Math.min(...vals, 0);
  const range = (maxV - minV) || 1;
  const n = recs.length;
  const x = i => n === 1 ? W/2 : padL + (W - padL - padR) * i / (n - 1);
  const y = v => padT + (H - padT - padB) * (1 - (v - minV) / range);
  
  let svg = `<svg viewBox="0 0 ${W} ${H}" style="width:100%;display:block">`;
  // Нулевая линия если есть минус
  if(minV < 0){
    svg += `<line x1="${padL}" y1="${y(0)}" x2="${W-padR}" y2="${y(0)}" stroke="rgba(255,107,111,0.4)" stroke-width="1" stroke-dasharray="3 3"/>`;
  }
  // Линия
  let path = '';
  recs.forEach((r, i) => { path += (i === 0 ? 'M' : 'L') + x(i) + ',' + y(r.profit) + ' '; });
  svg += `<path d="${path}" fill="none" stroke="#FFFFFF" stroke-width="2.5" stroke-linejoin="round" stroke-linecap="round"/>`;
  // Точки + подписи дат
  recs.forEach((r, i) => {
    svg += `<circle cx="${x(i)}" cy="${y(r.profit)}" r="4" fill="#FFFFFF"/>`;
    if(n <= 8 || i === 0 || i === n-1 || i % Math.ceil(n/6) === 0){
      svg += `<text x="${x(i)}" y="${H-6}" text-anchor="middle" font-size="9" fill="rgba(255,255,255,0.5)">${r.date.substring(0,5)}</text>`;
    }
  });
  svg += '</svg>';
  return svg;
}

function promptAddProfit(){
  const rev = prompt('Выручка за месяц (₸):');
  if(rev === null) return;
  const prof = prompt('Прибыль за месяц (₸):');
  if(prof === null) return;
  const me = getMyData();
  if(!me) return;
  showToast('Сохраняем...');
  callAction('saveProfit', {name: me.name, revenue: rev, profit: prof}).then(function(r){
    if(r && r.ok){
      showToast('Записано');
      loadProfitChart();
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  });
}

// ── ИИ-анализ динамики между замерами ──
function parseWheelDate(s){
  // "dd.MM.yyyy HH:mm" → Date
  try{
    var p = String(s).split(' ')[0].split('.');
    return new Date(parseInt(p[2]), parseInt(p[1])-1, parseInt(p[0]));
  }catch(e){ return null; }
}

async function analyzeWheelDynamics(){
  const box = document.getElementById('wheelAiBox');
  if(!box) return;
  const d = _wheelHistory || {};
  const isDna = _wheelDetailType === 'dna';
  const axes = isDna ? (d.dnaAxes || WHEEL_DNA_AXES) : (d.lifeAxes || WHEEL_LIFE_AXES);
  let rows;
  if(isDna){
    const businesses = d.businesses || [];
    let biz = businesses.find(b => b.name === _wheelDetailBiz) || businesses[0];
    rows = biz ? biz.rows : [];
  } else {
    rows = d.life || [];
  }
  const idx = Math.min(_wheelHistoryIdx, rows.length - 1);
  const cur = rows[idx];
  const prev = rows[idx + 1];
  if(!cur || !prev){ box.innerHTML = ''; return; }

  const dCur = parseWheelDate(cur.date), dPrev = parseWheelDate(prev.date);
  if(!dCur || !dPrev){ box.innerHTML = ''; return; }
  const days = Math.round((dCur - dPrev) / 86400000);
  if(days < 7){
    box.innerHTML = '<div style="font-size:12px;color:#777;padding:10px 0">Между замерами ' + days + ' дн. Разбор появится когда пройдёт неделя и больше.</div>';
    return;
  }

  box.innerHTML = '<div style="font-size:12px;color:#888;padding:12px 0">Готовим разбор...</div>';

  try{
    // Разбор считается на сервере
    const r = await callAction('analyzeWheel', {
      axes: axes.join('|'),
      was: prev.values.join(','),
      now: cur.values.join(','),
      days: days,
      kind: isDna ? 'dna' : 'life'
    });
    let text = (r && r.text) || '';
    if(!text){ box.innerHTML = ''; return; }
    text = text.replace(/РОСТ:/g, '<b style="color:#FFFFFF">РОСТ:</b>')
               .replace(/ПРОСЕДАНИЕ:/g, '<b style="color:#FF6B6F">ПРОСЕДАНИЕ:</b>')
               .replace(/ФОКУС:/g, '<b style="color:#FFFFFF">ФОКУС:</b>')
               .replace(/\n/g, '<br>');
    box.innerHTML = '<div style="margin-top:4px;padding:14px;background:rgba(255,255,255,0.06);border-left:3px solid #FFFFFF;border-radius:0 10px 10px 0">' +
      '<div style="font-size:11px;font-weight:800;color:#FFFFFF;letter-spacing:1.5px;text-transform:uppercase;margin-bottom:8px">Разбор за ' + days + ' дней</div>' +
      '<div style="font-size:13px;color:rgba(255,255,255,0.82);line-height:1.5">' + text + '</div></div>';
  }catch(e){
    box.innerHTML = '';
  }
}

// ── Редактор личных осей ──
function openAxesEditor(){
  const d = _wheelData || {};
  const lifeAxes = (d.lifeAxes || WHEEL_LIFE_AXES).slice(1); // без Бизнеса
  const body = document.getElementById('axesEditorBody');
  let h = '<div style="font-size:12px;color:#888;margin-bottom:14px;line-height:1.4">Переименуй оси под свою жизнь. Ось <b style="color:#fff">Бизнес</b> не меняется, она считается из ДНК. История замеров сохраняется.</div>';
  lifeAxes.forEach((ax, i) => {
    h += `<div class="form-group" style="margin-bottom:10px">
      <input class="form-input" id="axEd_${i}" value="${ax.replace(/"/g,'&quot;')}" maxlength="20" placeholder="Название оси">
    </div>`;
  });
  body.innerHTML = h;
  openModal('axesEditorModal');
}

async function submitAxesEditor(){
  const me = getMyData();
  if(!me) return;
  const axes = [];
  for(let i = 0; i < 7; i++){
    const inp = document.getElementById('axEd_' + i);
    const v = inp ? inp.value.trim() : '';
    if(!v){ showToast('Заполни все 7 осей'); return; }
    axes.push(v);
  }
  closeModal('axesEditorModal');
  showToast('Сохраняем...');
  try{
    const r = await callAction('saveWheelAxes', {name: me.name, axes: axes.join('|')});
    if(r && r.ok){
      showToast('Оси обновлены');
      renderWheel();
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  }catch(e){ showToast('' + e.message); }
}

function openWheelForm(){
  _wheelFormType = 'dna';
  window._wheelFormBiz = null; // сброс: возьмётся первый бизнес
  renderWheelForm();
  openModal('wheelModal');
}

function switchWheelForm(type){
  // Свободное переключение между колёсами. Значения слайдеров текущего таба не сохраняются
  // при переключении, поэтому подскажем если были изменения? Просто переключаем.
  _wheelFormType = type;
  renderWheelForm();
}

function renderWheelForm(){
  const isDna = _wheelFormType === 'dna';
  const d = _wheelData || {};
  const lifeAxesAll = d.lifeAxes || WHEEL_LIFE_AXES;
  const axes = isDna ? WHEEL_DNA_AXES : lifeAxesAll.slice(1); // life: без Бизнеса (авто)
  const bodyEl = document.getElementById('wheelFormBody');

  // Табы переключения
  const tabsEl = document.getElementById('wheelFormTabs');
  if(tabsEl){
    tabsEl.innerHTML = `
      <button onclick="switchWheelForm('dna')" style="flex:1;padding:10px;border-radius:10px;border:1.5px solid ${isDna?'#FFFFFF':'var(--border)'};background:${isDna?'rgba(255,255,255,0.12)':'transparent'};color:${isDna?'#FFFFFF':'#888'};font-weight:800;font-size:13px;font-family:inherit">ДНК бизнеса</button>
      <button onclick="switchWheelForm('life')" style="flex:1;padding:10px;border-radius:10px;border:1.5px solid ${!isDna?'#FFFFFF':'var(--border)'};background:${!isDna?'rgba(255,255,255,0.12)':'transparent'};color:${!isDna?'#FFFFFF':'#888'};font-weight:800;font-size:13px;font-family:inherit">Личное</button>`;
  }

  // Мульти-бизнес: текущий выбранный бизнес формы
  const businesses = d.businesses || [];
  if(isDna && !window._wheelFormBiz){
    window._wheelFormBiz = businesses.length ? businesses[0].name : 'Основной';
  }

  // Предзаполнение последними значениями выбранного бизнеса
  let last = null;
  if(isDna){
    const curBiz = businesses.find(b => b.name === window._wheelFormBiz);
    last = curBiz && curBiz.rows[0] ? curBiz.rows[0].values : null;
  } else {
    last = d.life && d.life[0] ? d.life[0].values.slice(1) : null;
  }

  const accent = isDna ? '#FFFFFF' : '#FFFFFF';
  let h = '';

  // Селектор бизнеса (только для ДНК)
  if(isDna){
    h += '<div style="margin-bottom:14px"><div style="font-size:11px;font-weight:800;color:#888;letter-spacing:1px;text-transform:uppercase;margin-bottom:6px">Направление</div>';
    h += '<div style="display:flex;gap:8px;flex-wrap:wrap">';
    const allBiz = businesses.length ? businesses.map(b=>b.name) : ['Основной'];
    allBiz.forEach(bn => {
      const sel = bn === window._wheelFormBiz;
      h += `<button onclick="window._wheelFormBiz='${bn.replace(/'/g,"\\'")}';renderWheelForm()" style="padding:8px 14px;border-radius:10px;border:1.5px solid ${sel?'#FFFFFF':'var(--border)'};background:${sel?'rgba(255,255,255,0.12)':'transparent'};color:${sel?'#FFFFFF':'#888'};font-weight:800;font-size:13px;font-family:inherit">${bn}</button>`;
    });
    h += '<button onclick="addNewBusiness()" style="padding:8px 14px;border-radius:10px;border:1.5px dashed var(--border);background:transparent;color:#888;font-weight:800;font-size:13px;font-family:inherit">+ Бизнес</button>';
    h += '</div></div>';
  }

  h += '<div style="font-size:12px;color:#888;margin-bottom:14px;line-height:1.4">' +
    (isDna ? 'Оцени каждую зону бизнеса от 1 до 10. Честно, как есть сейчас.' : 'Теперь про тебя лично. Ось Бизнес добавится автоматически: среднее всех твоих направлений.') + '</div>';

  axes.forEach((ax, i) => {
    const val = last && last[i] ? last[i] : 5;
    // В личном колесе название оси кликабельно: тап = переименовать (интуитивно, прямо в форме)
    const axLabel = isDna
      ? `<span style="font-size:14px;font-weight:700;color:#fff">${ax}</span>`
      : `<span onclick="renameAxisInline(${i})" style="font-size:14px;font-weight:700;color:#fff;border-bottom:1px dashed rgba(255,255,255,0.3);padding-bottom:1px">${ax} <span style="opacity:0.5;display:inline-flex;vertical-align:-2px"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20h4L19 9l-4-4L4 16z"/></svg></span></span>`;
    h += `<div style="margin-bottom:14px">
      <div style="display:flex;justify-content:space-between;margin-bottom:6px">
        ${axLabel}
        <span id="wv_${i}" style="font-size:15px;font-weight:800;color:${accent}">${val}</span>
      </div>
      <input type="range" min="1" max="10" value="${val}" data-axis="${i}"
        style="width:100%;accent-color:${accent};height:26px"
        oninput="document.getElementById('wv_'+this.dataset.axis).textContent=this.value">
    </div>`;
  });
  if(!isDna){
    h += '<div style="font-size:11px;color:#666;margin-top:2px;margin-bottom:6px">Нажми на название оси чтобы переименовать под себя</div>';
  }
  if(bodyEl) bodyEl.innerHTML = h;

  const btnEl = document.getElementById('wheelSubmitBtn');
  if(btnEl) btnEl.textContent = isDna ? 'Сохранить ДНК' : 'Сохранить Личное';
}

async function renameAxisInline(idx){
  // Переименование оси личного колеса прямо в форме оценки
  const me = getMyData();
  if(!me) return;
  const d = _wheelData || {};
  const lifeAxes = (d.lifeAxes || WHEEL_LIFE_AXES).slice(1); // без Бизнеса
  const current = lifeAxes[idx] || '';
  const newName = prompt('Новое название оси:', current);
  if(!newName || !newName.trim() || newName.trim() === current) return;
  
  // Запомним текущие значения слайдеров чтобы не сбросились
  const savedVals = Array.from(document.querySelectorAll('#wheelFormBody input[type=range]')).map(inp => inp.value);
  
  lifeAxes[idx] = newName.trim().substring(0, 20);
  try{
    const r = await callAction('saveWheelAxes', {name: me.name, axes: lifeAxes.join('|')});
    if(r && r.ok){
      // Обновим локальный кэш и перерисуем форму
      if(_wheelData) _wheelData.lifeAxes = ['Бизнес'].concat(lifeAxes);
      renderWheelForm();
      // Восстановим значения слайдеров
      const inputs = document.querySelectorAll('#wheelFormBody input[type=range]');
      inputs.forEach((inp, i) => {
        if(savedVals[i] !== undefined){
          inp.value = savedVals[i];
          const wv = document.getElementById('wv_' + i);
          if(wv) wv.textContent = savedVals[i];
        }
      });
      showToast('Переименовано');
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  }catch(e){ showToast('' + e.message); }
}

function addNewBusiness(){
  const name = prompt('Название направления (например: Кофейня, Опт, Онлайн-школа):');
  if(!name || !name.trim()) return;
  window._wheelFormBiz = name.trim().substring(0, 30);
  renderWheelForm();
  showToast('Оцени зоны и нажми Сохранить');
}

let _wheelSaveInFlight = false;
async function submitWheelForm(){
  if(_wheelSaveInFlight) return;
  if(v11adminWheel()){
    // Замер админа: только локально, без таблицы и без mytests
    const vals = Array.from(document.querySelectorAll('#wheelFormBody input[type=range]')).map(inp => Number(inp.value));
    const t = _wheelFormType;
    _wheelData = v11wheelSave(t, vals, t === 'dna' ? (window._wheelFormBiz || 'Основной') : '');
    if(t === 'dna'){ _wheelFormType = 'life'; renderWheelForm(); showToast('Колесо бизнеса сохранено. Теперь баланс'); }
    else { closeModal('wheelModal'); showToast('Колесо баланса сохранено'); }
    renderWheel();
    try{ if(v9activeId() === 'more') renderAdminMore(); }catch(e){}
    return;
  }
  const me = getMyData();
  if(!me){ showToast('Профиль не найден'); return; }

  const inputs = document.querySelectorAll('#wheelFormBody input[type=range]');
  const values = Array.from(inputs).map(inp => inp.value).join(',');
  const type = _wheelFormType;
  window._v10lastForm = Array.from(inputs).map(inp => Number(inp.value));

  _wheelSaveInFlight = true;
  try{
    const r = await callAction('saveWheel', {name: me.name, type: type, values: values, bizName: (type==='dna' ? (window._wheelFormBiz || 'Основной') : '')});
    if(r && r.ok){
      // Обновим кэш (чтобы Бизнес в life считался от свежего ДНК)
      _wheelData = await callAction('getWheel', {name: me.name});
      // Тот же замер уходит на платформу: тренер видит его на доске
      v10syncWheel(type);
      if(type === 'dna'){
        _wheelFormType = 'life';
        renderWheelForm();
        showToast('ДНК сохранено. Теперь личное');
      } else {
        closeModal('wheelModal');
        showToast('Колесо баланса обновлено');
        renderWheel();
      }
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  }catch(e){
    showToast('' + e.message);
  }finally{
    setTimeout(() => { _wheelSaveInFlight = false; }, 1500);
  }
}

function renderMyStatus(){
  const me = getMyData();
  const el = document.getElementById('mystatusContent');
  if(!el) return;
  if(!me){
    el.innerHTML = v3headBig('Профиль', 'Не найден') + '<div class="v2-card v2-empty" style="margin-top:16px">Обратитесь к Рустаму или Береке</div>';
    return;
  }
  var debt = Number(me.debt) || 0;
  var since = me.startDate ? 'с ' + me.startDate : (me.dateIn ? 'с ' + me.dateIn : '');
  var h = '<div class="v3-card-head" style="margin-top:4px">' + v3av(me, 64, true) +
    '<div style="min-width:0;flex:1"><div class="v3-name">' + v2esc(me.name) + '</div><div class="v3-sub" style="margin-top:3px">' + ['Резидент', me.format || '', since].filter(Boolean).map(v2esc).join(' · ') + '</div></div></div>';
  var pills = '<span class="v3-pill w">' + (me.isFired ? 'НЕАКТИВЕН' : 'АКТИВЕН') + '</span>';
  if(me.nextRenewal) pills += '<span class="v3-pill">ОПЛАЧЕН ДО ' + v2esc(v3shortDate(me.nextRenewal)) + '</span>';
  if(me.reportStreak > 0) pills += '<span class="v3-pill gold">СЕРИЯ ' + me.reportStreak + '</span>';
  h += '<div class="v3-pills">' + pills + '</div>';
  if(debt > 0){
    var uf = v3unpaidFines(me.name), lf = uf[uf.length - 1];
    var cap = (Number(me.debtRenew) || 0) > 0 ? 'Продление участия' : (lf ? 'Штраф ' + String(lf.date || '').slice(0, 5) + ' · ' + String(lf.kind || '').toLowerCase() : '');
    h += '<div class="v2-card v3-alert v3-pay"><div style="flex:1;min-width:0"><div class="v3-lbl">К ОПЛАТЕ</div>' +
      '<div class="num red" style="margin-top:4px;font-size:26px;font-weight:800">' + v2money(debt) + ' ₸</div>' +
      (cap ? '<div style="font-size:12px;color:#9A9A9A">' + v2esc(cap) + '</div>' : '') + '</div>' +
      '<a href="https://pay.kaspi.kz/pay/ri6h2lj5" target="_blank" class="v3-btn w" style="height:44px;padding:0 16px;border-radius:12px;font-size:14px;text-decoration:none">Оплатить</a></div>';
  }
  var g = Number(me.meetingsGranted) || 0, d = Number(me.meetingsDone) || 0;
  var fc = (cache.fines || []).filter(function(f){ return f.name === me.name; }).length;
  h += '<div class="v2-stats" style="margin-top:8px">' +
    '<div class="v2-stat" onclick="showPage(\'mymeetings\')"><div class="l">Встречи</div><div class="v num" style="font-size:18px">' + d + '/' + g + '</div></div>' +
    '<div class="v2-stat"><div class="l">Отчёты</div><div class="v num" style="font-size:18px">' + Math.round(v3reports30(me) / 30 * 100) + '%</div></div>' +
    '<div class="v2-stat"><div class="l">Штрафы</div><div class="v num" style="font-size:18px">' + fc + '</div></div></div>';
  // ДНК-причина с доски трекера (если есть)
  h += v10dnaCause();
  // Замеры: колёса и диагностика (с платформы приоритетнее, она видит и разборы трекера)
  var w = (cache.wheelAll && cache.wheelAll[me.name]) || {};
  var PT = v9tests().tests || {};
  var mrow = function(name, cur, prev){
    var a = wheelAvg(cur.values), dl = prev ? Math.round((a - wheelAvg(prev.values)) * 10) / 10 : null;
    return '<div class="v2-row" onclick="showPage(\'wheel\')"><div class="m"><b>' + name + '</b></div><div class="num" style="font-size:15px;font-weight:800">' + String(a).replace('.', ',') + '</div>' +
      '<div class="num v3-cap" style="width:34px;text-align:right">' + (dl === null ? '' : (dl >= 0 ? '+' : '−') + String(Math.abs(dl)).replace('.', ',')) + '</div></div>';
  };
  var rows = '';
  var prow = function(name, obj, go){
    var vs = Object.keys(obj).map(function(k){ return v9num(obj[k]); }).filter(function(x){ return x !== null; });
    if(!vs.length) return '';
    return '<div class="v2-row" onclick="' + go + '"><div class="m"><b>' + name + '</b><span>с платформы</span></div><div class="num" style="font-size:15px;font-weight:800">' + v9fmt1(vs.reduce(function(a, x){ return a + x; }, 0) / vs.length) + '</div><div class="v3-cap" style="width:34px;text-align:right">/10</div></div>';
  };
  var pBiz = PT.diag && typeof PT.diag === 'object' && (('Продукт' in PT.diag) || ('Делегирование' in PT.diag)) ? prow('Колесо бизнеса', PT.diag, "showPage('wheel')") : '';
  var pLife = PT.wheel && typeof PT.wheel === 'object' ? prow('Колесо баланса', PT.wheel, "showPage('wheel')") : '';
  if(pBiz) rows += pBiz; else if(w.dna && w.dna[0]) rows += mrow('Колесо бизнеса', w.dna[0], w.dna[1]);
  if(pLife) rows += pLife; else if(w.life && w.life[0]) rows += mrow('Колесо баланса', w.life[0], w.life[1]);
  try{
    var dr = JSON.parse(localStorage.getItem('bs_diagnostic_result') || 'null');
    if(dr && dr.totalPct != null) rows += '<div class="v2-row" onclick="showPage(\'diagnostic\')"><div class="m"><b>Тест здоровья бизнеса</b></div><div class="num" style="font-size:15px;font-weight:800">' + dr.totalPct + '</div><div class="v3-cap" style="width:34px;text-align:right">/100</div></div>';
  }catch(e){}
  if(!rows) rows = v3row('', 'Колесо баланса', 'Сделайте первый замер', "showPage('wheel')");
  h += '<div class="v2-h">Замеры <span class="lnk" onclick="showPage(\'wheel\')">Колесо</span></div><div class="v2-card">' + rows + '</div>';
  h += v9measuresHTML();
  if(!window._myBoard && !window._v9MeasTried){
    window._v9MeasTried = true;
    loadMyBoard(false).then(function(){ if(v9activeId() === 'mystatus') renderMyStatus(); });
  }
  // Ближайшая встреча
  var next = (cache.schedule || []).filter(function(ev){ return ev.res === me.name && parseScheduleKey(ev) >= Date.now() - 3600000; })
    .sort(function(a, b){ return parseScheduleKey(a) - parseScheduleKey(b); })[0];
  if(next){
    var onl = v3isOnline(next);
    h += '<div class="v2-h">Ближайшая встреча</div><div class="v2-card v3-row-sb" style="padding:14px 16px"><div style="min-width:0"><div class="num" style="font-size:16px;font-weight:800">' + v2esc(next.date) + ' ' + v2esc(next.time || '') + '</div>' +
      '<div class="v3-cap" style="font-size:12px;margin-top:2px">' + (onl ? 'Онлайн · Google Meet' : 'Офлайн · ' + v2esc(next.link || 'место уточняется')) + '</div></div>' +
      (onl ? '<a href="' + v2esc(next.link) + '" target="_blank" class="v3-btn w" style="text-decoration:none">Meet</a>' : '') + '</div>';
  }
  if(me.partner) h += '<div class="v2-card v3-text" style="margin-top:10px;color:#9A9A9A">Партнёр: <b style="color:#fff">' + v2esc(me.partner) + '</b> · одна встреча на двоих</div>';
  h += '<div class="v2-h">Ещё</div><div class="v2-card">' +
    v3row('me', 'Мои данные', 'Ниша, о себе, контакты', "showPage('myprofile')") +
    v3row('club', 'Пригласить предпринимателя', '100\u00a0000 ₸ за приглашённого резидента', "showPage('referral')") +
    v3row('doc', 'Рассказать о BS в Stories', 'Готовая картинка с вашим фото', 'shareStory()') +
  '</div>';
  el.innerHTML = h;
}

function onMeetingDoneClick(res, date, time, isOnline){
  // Если онлайн. сразу как раньше через meetingDone
  if(isOnline){
    meetingDone(res, date, time);
    return;
  }
  // Если офлайн. открываем модал со списком офлайн-резидентов + галочки
  openAttendanceModal(date, time);
}

async function openAttendanceModal(date, time){
  showToast('Загружаем список...');
  // Получаем список офлайн-резидентов
  var r = await callAction('getOfflineResidents', {});
  var list = (r && r.residents) || [];
  if(!list.length){
    showToast('Нет офлайн-резидентов');
    return;
  }
  window._attendDate = date;
  window._attendTime = time;
  // Заранее отмечены те, кто записан на этот офлайн-слот. Остальных можно добавить
  var booked = {};
  (cache.schedule||[]).forEach(function(e){
    if(e.date === date && (!time || !e.time || e.time === time)) booked[e.res] = 1;
  });
  var anyBooked = Object.keys(booked).length > 0;
  // Заполняем checkboxes
  var box = document.getElementById('attendList');
  if(!box) return;
  box.innerHTML = (function(){
    var html = '', prevFmt = null;
    list.forEach(function(item, i){
      var name = (typeof item === 'string') ? item : item.name;
      var fmt = (typeof item === 'string') ? 'Офлайн' : (item.format || 'Офлайн');
      if(prevFmt !== null && fmt !== prevFmt){
        html += '<div class="attend-sep"><span>' + fmt + '</span></div>';
      }
      prevFmt = fmt;
      html += '<label class="attend-item">' +
        '<input type="checkbox" value="' + name + '" id="att_' + i + '"' +
        ((anyBooked ? booked[name] : fmt === 'Офлайн') ? ' checked' : '') + '>' +
        '<span>' + name + '</span>' +
        (fmt === 'Онлайн' ? '<span class="attend-tag">онлайн</span>' : '') +
        '</label>';
    });
    return html;
  })();
  closeDetail();
  openModal('attendModal');
}

async function submitAttendance(){
  if(window._submittingAttend){return;}
  window._submittingAttend = true;
  setTimeout(function(){window._submittingAttend = false;}, 5000);
  var checks = document.querySelectorAll('#attendList input[type=checkbox]:checked');
  var names = Array.from(checks).map(function(c){return c.value;});
  if(!names.length){
    showToast('Выберите хотя бы одного');
    window._submittingAttend = false;
    return;
  }
  closeModal('attendModal');
  showToast('Отмечаем...');
  // Оптимистично удаляем из cache.schedule встречи этих резидентов на дату
  if(cache.schedule){
    cache.schedule = cache.schedule.filter(function(e){
      if(e.date !== window._attendDate) return true;
      return names.indexOf(e.res) < 0;
    });
    try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
  }
  renderAllPages();
  var result = await callAction('markAttendance', {
    names: names.join('|'),
    date: window._attendDate,
    time: window._attendTime||''
  });
  if(result && result.ok){
    showToast('Отмечено: '+result.marked.length);
  } else {
    showToast('Ошибка');
  }
  setTimeout(loadAllData, 3000);
}

function shareStoryInvite(){
  // Открываем сразу сторис с шаблоном 'invite' без выбора
  generateStory('invite');
}

function shareStory(){
  // Один шаблон, без промежуточных окон. картинка собирается сразу
  generateStory('classic');
}

async function generateStory(template){
  closeModal('storyTemplateModal');
  var me = getMyData();
  if(!me){ showToast('⚠️ Профиль не найден'); return; }
  showToast('🎨 Собираем картинку...');

  var W = 1080, H = 1920;
  var canvas = document.createElement('canvas');
  canvas.width = W; canvas.height = H;
  var ctx = canvas.getContext('2d');
  var GOLD = '#FFFFFF';

  // Фон: глубокий чёрный с золотым свечением сверху
  var grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, '#111111');
  grad.addColorStop(0.45, '#0a0a0a');
  grad.addColorStop(1, '#000000');
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, W, H);

  var glow = ctx.createRadialGradient(540, 420, 40, 540, 420, 780);
  glow.addColorStop(0, 'rgba(255,255,255,0.16)');
  glow.addColorStop(1, 'rgba(255,255,255,0)');
  ctx.fillStyle = glow;
  ctx.fillRect(0, 0, W, H);

  // Фирменный элемент: спираль ДНК по правому краю
  ctx.save();
  ctx.globalAlpha = 0.12;
  ctx.strokeStyle = GOLD;
  ctx.lineWidth = 3;
  for(var s = 0; s < 2; s++){
    ctx.beginPath();
    for(var y = 0; y <= H; y += 8){
      var x = 990 + Math.sin((y / 150) + s * Math.PI) * 55;
      if(y === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  ctx.globalAlpha = 0.09;
  for(var y2 = 60; y2 < H; y2 += 96){
    var xa = 990 + Math.sin(y2 / 150) * 55;
    var xb = 990 + Math.sin((y2 / 150) + Math.PI) * 55;
    ctx.beginPath(); ctx.moveTo(xa, y2); ctx.lineTo(xb, y2); ctx.stroke();
  }
  ctx.restore();

  // Шапка: логотип BS как во вкладке BS
  ctx.textAlign = 'center';
  // Логотип: берём из вкладки BS, если она ещё не отрисована. грузим напрямую
  var logoDrawn = false;
  var headBottom = 175;
  var logoEl = window._bsLogoImg || document.querySelector('.hero-logo-real img');
  if(!logoEl || !logoEl.complete || !logoEl.naturalWidth){
    try{
      if(window._bsLogoSrc){
        logoEl = await new Promise(function(res, rej){
          var im = new Image();
          im.onload = function(){ window._bsLogoImg = im; res(im); };
          im.onerror = rej;
          im.src = window._bsLogoSrc;
          setTimeout(rej, 2000);
        });
      }
    }catch(e){ logoEl = null; }
  }
  if(logoEl && logoEl.complete && logoEl.naturalWidth){
    try{
      var lw = 270, lh = lw * (logoEl.naturalHeight / logoEl.naturalWidth);
      ctx.drawImage(logoEl, 540 - lw/2, 95, lw, lh);
      headBottom = 95 + lh;
      logoDrawn = true;
    }catch(e){}
  }
  if(!logoDrawn){
    ctx.fillStyle = '#ffffff';
    ctx.font = '800 44px Manrope, Arial, sans-serif';
    ctx.letterSpacing = '5px';
    ctx.fillText('BUSINESS SURGERY', 540, 160);
    ctx.letterSpacing = '0px';
    headBottom = 180;
  }
  ctx.fillStyle = GOLD;
  ctx.font = '600 21px Manrope, Arial, sans-serif';
  ctx.letterSpacing = '5px';
  ctx.fillText('РЕЗИДЕНЦИЯ', 540, headBottom + 42);
  ctx.letterSpacing = '0px';
  ctx.fillStyle = 'rgba(255,255,255,0.4)';
  ctx.fillRect(470, headBottom + 66, 140, 2);

  // Аватар из Telegram (круг в золотом кольце)
  // Аватар приходит с сервера в base64: прямая ссылка Telegram не отдаёт CORS
  // и загрязняет canvas, из-за чего картинка вообще не создавалась
  var avatarUrl = cache.myAvatar || '';
  var drewAvatar = false;
  if(window._bsAvatarImg){
    try{
      ctx.save();
      ctx.beginPath(); ctx.arc(540, 520, 165, 0, Math.PI*2); ctx.closePath(); ctx.clip();
      ctx.drawImage(window._bsAvatarImg, 375, 355, 330, 330);
      ctx.restore();
      drewAvatar = true;
    }catch(e){}
  }
  if(!drewAvatar && avatarUrl){
    try{
      var img = await new Promise(function(res, rej){
        var im = new Image();
        im.onload = function(){ res(im); };
        im.onerror = rej;
        im.src = avatarUrl;
        setTimeout(rej, 3000);
      });
      ctx.save();
      ctx.beginPath();
      ctx.arc(540, 520, 165, 0, Math.PI * 2);
      ctx.closePath();
      ctx.clip();
      ctx.drawImage(img, 375, 355, 330, 330);
      ctx.restore();
      drewAvatar = true;
      window._bsAvatarImg = img;
    }catch(e){}
  }
  if(!drewAvatar){
    ctx.beginPath();
    ctx.arc(540, 520, 165, 0, Math.PI * 2);
    ctx.fillStyle = 'rgba(255,255,255,0.14)';
    ctx.fill();
    ctx.fillStyle = GOLD;
    ctx.font = '800 130px Manrope, Arial, sans-serif';
    ctx.fillText(me.name[0].toUpperCase(), 540, 570);
  }
  ctx.beginPath();
  ctx.arc(540, 520, 168, 0, Math.PI * 2);
  ctx.strokeStyle = GOLD;
  ctx.lineWidth = 4;
  ctx.stroke();

  // Имя
  ctx.fillStyle = '#ffffff';
  ctx.font = '900 86px Manrope, Arial, sans-serif';
  ctx.fillText(me.name.split(' ')[0].toUpperCase(), 540, 800);

  var months = Number(me.months) || 0;
  var stazh = (function(n){
    if(!n) return 'Первый месяц';
    var y = Math.floor(n/12), mo = n%12, parts = [];
    if(y) parts.push(y + ' ' + (y===1?'год':(y>=2&&y<=4?'года':'лет')));
    if(mo) parts.push(mo + ' ' + (mo===1?'месяц':(mo>=2&&mo<=4?'месяца':'месяцев')));
    return parts.join(' ') + ' в клубе';
  })(months);

  // Единый сюжет: резидент говорит от себя и приглашает тех, кому актуально
  if(me.niche){
    ctx.fillStyle = 'rgba(255,255,255,0.5)';
    ctx.font = '400 32px Manrope, Arial, sans-serif';
    wrapText(ctx, me.niche, 780, 1).forEach(function(line){
      ctx.fillText(line, 540, 862);
    });
  }
  ctx.fillStyle = GOLD;
  ctx.font = '700 30px Manrope, Arial, sans-serif';
  ctx.fillText(stazh, 540, me.niche ? 912 : 880);

  var blockTop = me.niche ? 1010 : 980;

  // Что происходит
  ctx.fillStyle = '#ffffff';
  ctx.font = '800 50px Manrope, Arial, sans-serif';
  ctx.fillText('Каждые 10 дней', 540, blockTop);
  ctx.fillText('мой бизнес разбирают', 540, blockTop + 62);
  ctx.fillText('по цифрам', 540, blockTop + 124);

  ctx.fillStyle = 'rgba(255,255,255,0.62)';
  ctx.font = '400 32px Manrope, Arial, sans-serif';
  ctx.fillText('Два трекера, живая встреча,', 540, blockTop + 200);
  ctx.fillText('план на следующие 10 дней.', 540, blockTop + 244);

  // Разделитель
  ctx.fillStyle = 'rgba(255,255,255,0.3)';
  ctx.fillRect(390, blockTop + 300, 300, 1);

  // Приглашение
  ctx.fillStyle = GOLD;
  ctx.font = '800 44px Manrope, Arial, sans-serif';
  ctx.fillText('Если у тебя бизнес', 540, blockTop + 372);
  ctx.fillText('и он встал на месте', 540, blockTop + 426);

  ctx.fillStyle = '#ffffff';
  ctx.font = '600 34px Manrope, Arial, sans-serif';
  ctx.fillText('Напиши мне. Расскажу как это', 540, blockTop + 500);
  ctx.fillText('устроено изнутри.', 540, blockTop + 544);

  // Подвал
  ctx.fillStyle = 'rgba(255,255,255,0.16)';
  ctx.fillRect(180, 1706, 720, 1);
  ctx.fillStyle = 'rgba(255,255,255,0.9)';
  ctx.font = '800 34px Manrope, Arial, sans-serif';
  ctx.fillText('BUSINESS SURGERY', 540, 1768);
  ctx.fillStyle = 'rgba(255,255,255,0.42)';
  ctx.font = '400 27px Manrope, Arial, sans-serif';
  ctx.fillText('bxclub.kz · @business.surgery · Алматы', 540, 1812);

  // Публикация. В Telegram WebView скачивание заблокировано, поэтому:
  // 1) показываем картинку прямо в приложении (можно сохранить долгим нажатием)
  // 2) бот присылает её же в чат. оттуда в Stories одним касанием
  var dataUrl = '';
  try{
    dataUrl = canvas.toDataURL('image/jpeg', 0.9);
  }catch(secErr){
    // Canvas загрязнён внешней картинкой. закрашиваем аватар кругом с буквой и пробуем снова
    try{
      ctx.save();
      ctx.beginPath(); ctx.arc(540, 520, 168, 0, Math.PI*2); ctx.closePath();
      ctx.fillStyle = '#0d0d0d'; ctx.fill();
      ctx.fillStyle = 'rgba(255,255,255,0.14)'; ctx.fill();
      ctx.restore();
      ctx.fillStyle = GOLD;
      ctx.font = '800 130px Manrope, Arial, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(me.name[0].toUpperCase(), 540, 570);
      ctx.beginPath(); ctx.arc(540, 520, 168, 0, Math.PI*2);
      ctx.strokeStyle = GOLD; ctx.lineWidth = 4; ctx.stroke();
      dataUrl = canvas.toDataURL('image/jpeg', 0.9);
    }catch(e2){
      showToast('⚠️ Не удалось создать картинку');
      return;
    }
  }
  if(!dataUrl){ showToast('⚠️ Пустая картинка'); return; }
  showStoryResult(dataUrl);

  // Отправка через бота в фоне
  if(user && user.id){
    try{
      appPost({
        bsAction: 'sendImage',
        chatId: String(user.id),
        image: dataUrl,
        caption: 'Ваша картинка для Stories. Сохраните и выложите в Instagram или Telegram.'
      }).then(function(){
        showToast('✅ Картинка отправлена в чат с ботом');
      }).catch(function(){});
    }catch(e){}
  }
}

function showStoryResult(dataUrl){
  // Экран с готовой картинкой: сохранить долгим нажатием или поделиться
  var box = document.getElementById('storyResultBody');
  if(!box) return;
  box.innerHTML =
    '<img src="' + dataUrl + '" style="width:100%;border-radius:14px;display:block;margin-bottom:14px">' +
    '<div style="font-size:12.5px;color:#888;line-height:1.5;margin-bottom:14px;text-align:center">' +
      'Картинка отправлена в чат с ботом. Откройте чат и перешлите её в Stories.<br>' +
      'Можно также удержать палец на картинке выше и сохранить в галерею.' +
    '</div>' +
    '<button class="btn btn-primary" style="width:100%;margin-bottom:8px" onclick="shareStoryImage()">Поделиться</button>' +
    '<button class="btn btn-secondary" style="width:100%;margin:0" onclick="closeStoryResult()">Готово</button>';
  window._lastStoryData = dataUrl;
  openModal('storyResultModal');
}

function closeStoryResult(){
  try{
    var m = document.getElementById('storyResultModal');
    if(m) m.classList.remove('show');
    window._lastStoryData = null;
  }catch(e){}
}

async function shareStoryImage(){
  var dataUrl = window._lastStoryData;
  if(!dataUrl) return;
  try{
    var res = await fetch(dataUrl);
    var blob = await res.blob();
    if(navigator.canShare){
      var file = new File([blob], 'bs_story.jpg', {type: 'image/jpeg'});
      if(navigator.canShare({files: [file]})){
        await navigator.share({files: [file]});
        return;
      }
    }
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url; a.download = 'bs_story.jpg';
    document.body.appendChild(a); a.click(); document.body.removeChild(a);
    setTimeout(function(){ URL.revokeObjectURL(url); }, 1500);
  }catch(e){
    if(e && e.name === 'AbortError') return;
    showToast('Картинка в чате с ботом');
  }
}

function fallbackDownload(blob, fileName){
  var url = URL.createObjectURL(blob);
  var a = document.createElement('a');
  a.href = url;
  a.download = fileName;
  a.click();
  URL.revokeObjectURL(url);
  if(tg && tg.showAlert){
    tg.showAlert('Картинка сохранена\n\nЧтобы поделиться в Stories:\n1. Открой Instagram\n2. Свайп вправо (или + сверху)\n3. Выбери "Stories"\n4. Загрузи эту картинку из галереи');
  } else {
    alert('Картинка сохранена. Откройте Instagram → Stories → выберите её из галереи.');
  }
}

function wrapText(ctx, text, maxWidth, maxLines){
  var words = String(text).split(' ');
  var lines = [];
  var line = '';
  for(var i = 0; i < words.length; i++){
    var test = line + words[i] + ' ';
    if(ctx.measureText(test).width > maxWidth && i > 0){
      lines.push(line.trim());
      line = words[i] + ' ';
    } else {
      line = test;
    }
  }
  lines.push(line.trim());
  return lines.slice(0, maxLines || 3);
}

function renderMyProfile(){
  let me = getMyData();
  const el = document.getElementById('myprofileContent');
  
  // Для админа создаём профиль на основе данных из Telegram
  if(!me && isRealAdmin){
    me = {
      chatId: String(user?.id || ''),
      name: (user?.first_name || 'Админ') + (user?.last_name ? ' ' + user.last_name : ''),
      niche: '',
      bio: '',
      help: '',
      instagram: '',
      phone: '',
      avatar: user?.photo_url || ''
    };
    // Подгрузим существующий профиль если есть
    const adminProfile = (cache.adminProfiles || []).find(p => String(p.chatId) === me.chatId);
    if(adminProfile){
      me.niche = adminProfile.niche || '';
      me.bio = adminProfile.bio || '';
      me.help = adminProfile.help || '';
      me.instagram = adminProfile.instagram || '';
      me.phone = adminProfile.phone || '';
      me.avatar = adminProfile.avatar || me.avatar;
    }
  }
  
  if(!me){
    el.innerHTML = `<div class="empty"><div class="em-text">Не найден</div></div>`;
    return;
  }
  el.innerHTML = `
    ${v3headBig('Профиль', 'Мои данные')}
    <div class="v3-card-head" style="margin-top:16px">${v3av(me, 64, true)}<div style="min-width:0"><div class="v3-name">${v2esc(me.name)}</div><div class="v3-sub" style="margin-top:3px">${v2esc(me.niche || 'Заполните профиль ниже')}</div></div></div>
    <div style="height:14px"></div>
    <div class="card">
      <div class="form-group" style="display:none">
        <input id="pfAvatar" value="${me.avatar||''}">
      </div>
      <div class="form-group">
        <label class="form-label">Ниша / деятельность</label>
        <input class="form-input" id="pfNiche" value="${me.niche||''}" placeholder="Например: Маркетинг, Edu, IT">
      </div>
      <div class="form-group">
        <label class="form-label">О себе</label>
        <textarea class="form-textarea" id="pfBio" placeholder="Кратко о вашем бизнесе">${me.bio||''}</textarea>
      </div>
      <div class="form-group">
        <label class="form-label">Чем могу помочь другим</label>
        <textarea class="form-textarea" id="pfHelp" placeholder="Какие задачи можете решить">${me.help||''}</textarea>
      </div>
      <div class="form-group">
        <label class="form-label">Instagram</label>
        <input class="form-input" id="pfIG" value="${me.instagram||''}" placeholder="@username">
      </div>
      <div class="form-group">
        <label class="form-label">Телефон</label>
        <input class="form-input" id="pfPhone" value="${me.phone||''}" placeholder="+7 700 000 0000">
      </div>
      <button class="v3-primary" onclick="saveProfile('${me.chatId}')">Сохранить профиль</button>
    </div>
  `;
}

async function saveProfile(chatId){
  // Аватар автоматически из Telegram
  const tgPhoto = user?.photo_url || '';
  const existingAvatar = document.getElementById('pfAvatar')?.value || '';
  const data = {
    chatId: chatId || String(user?.id||''),
    avatar: tgPhoto || existingAvatar,
    niche: document.getElementById('pfNiche').value,
    bio: document.getElementById('pfBio').value,
    help: document.getElementById('pfHelp').value,
    instagram: document.getElementById('pfIG').value,
    phone: document.getElementById('pfPhone').value
  };
  await callAction('saveProfile', data);
  showToast('Профиль сохранён');
  loadAllData();
}

function renderMyMeetings(){
  const me = getMyData();
  const el = document.getElementById('mymeetingsContent');
  if(!el) return;
  var b = window._myBoard && window._myBoard.board;
  var head = v3headBig('С трекером и клубом', 'Встречи');
  if(!me){
    el.innerHTML = head + '<div class="v2-card v2-empty" style="margin-top:14px">Профиль не найден</div>';
    return;
  }
  const myMeetings = (cache.schedule||[]).filter(ev => ev.res === me.name);
  var g = Number(me.meetingsGranted) || 0, d = Number(me.meetingsDone) || 0;
  head += g ? v3progress(d, g, 'осталось ' + Math.max(0, g - d) + ' ' + v3plural(Math.max(0, g - d), 'встреча', 'встречи', 'встреч')).replace('готово', 'проведено') : '';
  if(!myMeetings.length){
    el.innerHTML = head + '<div class="v2-card v2-empty" style="margin-top:14px">Встреч пока нет. Напишите трекеру, чтобы записаться.</div>';
    return;
  }
  window._currentMyMeetingsList = myMeetings;
  el.innerHTML = head + '<div style="height:12px"></div>' + myMeetings.map((ev,i) => meetingCard(ev,i)).join('');
}

// ─── MODALS ────────────────────────────────────────────────────────────────
function initFineModal(){
  const residents = (cache.residents||[]).filter(r=>!r.isFired&&!r.isAdmin&&!r.isTeam&&r.name!=='Тест'&&r.name!=='Тест2');
  document.getElementById('fineResident').innerHTML = residents.map(r=>`<option value="${r.name}">${r.name}</option>`).join('');
  document.getElementById('fineTypeGrid').innerHTML = FINE_TYPES.map((t,i)=>`
    <button class="pick-btn ${i===0?'selected':''}" data-name="${t.name}" data-amount="${t.amount}" onclick="selectFineType(this)">
      <span class="e">${t.emoji}</span>${t.label}
    </button>
  `).join('');
  selFineType = FINE_TYPES[0].name;
  document.getElementById('fineAmount').value = FINE_TYPES[0].amount;
}

function selectFineType(btn){
  document.querySelectorAll('#fineTypeGrid .pick-btn').forEach(b=>b.classList.remove('selected'));
  btn.classList.add('selected');
  selFineType = btn.dataset.name;
  document.getElementById('fineAmount').value = btn.dataset.amount;
}

async function submitFine(){
  const name = document.getElementById('fineResident').value;
  const amount = getMoneyValue('fineAmount');
  if(!name||!amount){showToast('Заполните все поля');return;}
  // Оптимистично: закрываем модал и показываем успех СРАЗУ
  closeModal('fineModal');
  showToast('Штраф выставлен: '+name+'. '+fmt(amount)+' ₸');
  // Запрос в фоне
  callAction('addFine', {name, type: selFineType, amount}).then(function(result){
    if(!result||!result.ok){
      showToast('Ошибка. проверьте таблицу');
    }
    loadAllData();
  });
}

function initSchedModal(){
  const residents = (cache.residents||[]).filter(r=>!r.isFired&&!r.isAdmin&&!r.isTeam&&r.name!=='Тест'&&r.name!=='Тест2');
  var onl = residents.filter(function(r){ return String(r.format||'') === 'Онлайн'; });
  var offl = residents.filter(function(r){ return String(r.format||'') !== 'Онлайн'; });
  var opt = function(r){ return `<option value="${r.name}">${r.name}</option>`; };
  document.getElementById('schedResident').innerHTML =
    (onl.length ? '<optgroup label="Онлайн · '+onl.length+'">' + onl.map(opt).join('') + '</optgroup>' : '') +
    (offl.length ? '<optgroup label="Офлайн · '+offl.length+'">' + offl.map(opt).join('') + '</optgroup>' : '');
  renderDateGrid(10);
  // ПРАВИЛЬНЫЙ ПОРЯДОК: 1) дата 2) заполнить время 3) отсечь прошедшее
  selDate = new Date().toLocaleDateString('ru-RU');
  const tg2 = document.getElementById('timeGrid');
  const times = ['09:00','09:30','10:00','10:30','11:00','11:30','12:00','12:30','13:00','13:30','14:00','14:30','15:00','15:30','16:00','16:30','17:00','17:30','18:00','18:30','19:00','19:30','20:00','20:30','21:00','21:30','22:00','22:30','23:00'];
  tg2.innerHTML = times.map((t,i)=>`<button class="pick-btn ${i===0?'selected':''}" data-time="${t}" onclick="selectSchedTime(this)">${t}</button>`).join('');
  selTime = '09:00';
  // Если уже вечер и ВСЕ слоты сегодня в прошлом. автоматически выбираем ЗАВТРА
  var nowMin = new Date().getHours()*60 + new Date().getMinutes();
  var lastSlotMin = 23*60; // 23:00
  if(nowMin >= lastSlotMin){
    var dateBtns = document.querySelectorAll('#dateGrid .pick-btn[data-date]');
    if(dateBtns.length > 1){
      dateBtns[0].classList.remove('selected');
      dateBtns[1].classList.add('selected');
      selDate = dateBtns[1].dataset.date;
    }
  }
  updateAvailableTimes();
}


function renderDateGrid(count){
  const dg = document.getElementById('dateGrid');
  const days = ['вс','пн','вт','ср','чт','пт','сб'];
  let html = '';
  for(let i=0; i<count; i++){
    const d = new Date(); d.setDate(d.getDate()+i);
    const ds = d.toLocaleDateString('ru-RU');
    html += `<button class="pick-btn ${i===0?'selected':''}" data-date="${ds}" onclick="selectSchedDate(this)">
      <span class="e">${d.getDate()}</span>${days[d.getDay()]}
    </button>`;
  }
  if(count <= 10){
    html += `<button class="pick-btn" style="grid-column:span 2;font-size:10px" onclick="renderDateGrid(20)">Ещё 10 →</button>`;
  }
  dg.innerHTML = html;
}

function selectSchedDate(btn){
  document.querySelectorAll('#dateGrid .pick-btn').forEach(b=>b.classList.remove('selected'));
  btn.classList.add('selected');
  selDate = btn.dataset.date;
  updateAvailableTimes();
}

function updateAvailableTimes(){
  // Сегодня. отсекаем прошедшее время
  var today = new Date().toLocaleDateString('ru-RU');
  var isToday = selDate === today;
  var nowMinutes = isToday ? (new Date().getHours()*60 + new Date().getMinutes()) : -1;
  document.querySelectorAll('#timeGrid .pick-btn').forEach(function(btn){
    var t = btn.dataset.time;
    if(!t) return;
    var tp = t.split(':');
    var btnMin = parseInt(tp[0])*60 + parseInt(tp[1]);
    if(isToday && btnMin <= nowMinutes){
      btn.style.opacity = '0.3';
      btn.style.pointerEvents = 'none';
      btn.classList.remove('selected');
    } else {
      btn.style.opacity = '1';
      btn.style.pointerEvents = 'auto';
    }
  });
  // Если selTime в прошлом. выбираем первое доступное
  if(isToday && selTime){
    var tp = selTime.split(':');
    var selMin = parseInt(tp[0])*60 + parseInt(tp[1]);
    if(selMin <= nowMinutes){
      var firstAvailable = document.querySelector('#timeGrid .pick-btn[style*="opacity: 1"]') 
        || document.querySelector('#timeGrid .pick-btn:not([style*="opacity: 0.3"])');
      if(firstAvailable){
        document.querySelectorAll('#timeGrid .pick-btn').forEach(b=>b.classList.remove('selected'));
        firstAvailable.classList.add('selected');
        selTime = firstAvailable.dataset.time;
      }
    }
  }
}

function selectSchedTime(btn){
  document.querySelectorAll('#timeGrid .pick-btn').forEach(b=>b.classList.remove('selected'));
  btn.classList.add('selected');
  selTime = btn.dataset.time;
}



function openFineActions(idx){
  var f = (window._currentFinesList||[])[idx];
  if(!f) return;
  window._fineSel = f;
  document.getElementById('detailTitle').textContent = 'Штраф: '+f.name;
  document.getElementById('detailContent').innerHTML = `
    <div class="card">
      <div class="list-item">
        <div class="list-avatar" style="background:rgba(255,107,111,0.2);color:var(--red)">!</div>
        <div class="list-content">
          <div class="list-name">${f.name}</div>
          <div class="list-sub">${f.kind||f.type||''}</div>
        </div>
        <div class="list-right">
          <div class="list-amount">${fmt(f.amount)} ₸</div>
          <div class="list-date">${f.date}</div>
        </div>
      </div>
    </div>
    <button class="btn btn-success" onclick="markFinePaid()">Отметить оплаченным</button>
    <button class="btn btn-danger" onclick="deleteFine()">Удалить штраф</button>
  `;
  showDetail();
}

function _fineParams(f){
  return {row: f.row||'', name: f.name, date: f.date, kind: f.kind||f.type||'', amount: f.amount};
}
function _fineAction(action, f, extra, okText){
  // Убираем из списка сразу, сервер подтверждает. При ошибке штраф возвращается
  var same = function(x){ return x===f || (x.row && x.row===f.row && x.name===f.name); };
  if(cache.fines){
    cache.fines = cache.fines.filter(function(x){ return !same(x); });
    try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
  }
  closeDetail();
  renderAllPages();
  callAction(action, Object.assign(_fineParams(f), extra||{})).then(function(r){
    if(r && r.ok){ showToast(okText); }
    else { showToast('Не получилось: ' + ((r && r.error) || 'нет ответа')); }
    loadAllData({fresh:true, force:true});
  });
}
function markFinePaid(){
  var f = window._fineSel; if(!f) return;
  if(!confirm('Отметить штраф '+f.name+' '+f.date+' как оплаченный?')) return;
  _fineAction('updateFine', f, {status:'Оплатил'}, 'Отмечено оплаченным');
}
function deleteFine(){
  var f = window._fineSel; if(!f) return;
  if(!confirm('Удалить штраф '+f.name+' '+f.date+'?\n\nЭто удалит запись полностью из таблицы.')) return;
  _fineAction('deleteFine', f, null, 'Штраф удалён');
}

var offSelDate='', offSelTime='';
function openOfflineGroupModal(){
  openModal('offlineGroupModal');
  // Сетка дат
  var dg = document.getElementById('offDateGrid');
  var html = '';
  for(let i=0; i<14; i++){
    const d = new Date(); d.setDate(d.getDate()+i);
    const ds = d.toLocaleDateString('ru-RU');
    html += `<button class="pick-btn ${i===0?'selected':''}" data-date="${ds}" onclick="selectOffDate(this)">${d.getDate()}.${String(d.getMonth()+1).padStart(2,'0')}</button>`;
  }
  dg.innerHTML = html;
  offSelDate = new Date().toLocaleDateString('ru-RU');
  // Сетка времени (09:00 - 18:00 шаг 1 час)
  var tg = document.getElementById('offTimeGrid');
  var thtml = '';
  for(let h=9; h<=18; h++){
    var t = String(h).padStart(2,'0')+':00';
    thtml += `<button class="pick-btn ${h===10?'selected':''}" data-time="${t}" onclick="selectOffTime(this)">${t}</button>`;
  }
  tg.innerHTML = thtml;
  offSelTime = '10:00';
}
function selectOffDate(btn){
  document.querySelectorAll('#offDateGrid .pick-btn').forEach(b=>b.classList.remove('selected'));
  btn.classList.add('selected');
  offSelDate = btn.dataset.date;
}
function selectOffTime(btn){
  document.querySelectorAll('#offTimeGrid .pick-btn').forEach(b=>b.classList.remove('selected'));
  btn.classList.add('selected');
  offSelTime = btn.dataset.time;
}
async function submitOfflineGroup(){
  if(!offSelDate || !offSelTime){showToast('Выберите дату и время');return;}
  if(window._creatingOffline){showToast('Уже создаём...');return;}
  window._creatingOffline = true;
  setTimeout(function(){window._creatingOffline = false;}, 5000);
  closeModal('offlineGroupModal');
  showToast('Создаём офлайн день...');
  var result = await callAction('addOfflineGroup', {date: offSelDate, time: offSelTime});
  if(result && result.ok){
    showToast('Создано встреч: '+result.count);
    setTimeout(loadAllData, 2000);
  } else {
    showToast(''+(result && result.error || 'Ошибка'));
  }
}

async function submitSchedule(){
  const res = document.getElementById('schedResident').value;
  if(!res){showToast('Выберите резидента');return;}
  if(!selDate||!selTime){showToast('Выберите дату и время');return;}
  // Проверка. нельзя в прошлом
  var parts = selDate.split('.');
  var day = parseInt(parts[0]);
  var month = parseInt(parts[1])-1;
  var year = parseInt(parts[2]);
  if(!year || isNaN(year)) year = new Date().getFullYear();
  var tp = selTime.split(':');
  var selHour = parseInt(tp[0]);
  var selMin = parseInt(tp[1]||'0');
  var selDateTime = new Date(year, month, day, selHour, selMin, 0);
  var nowDt = new Date();
  // Проверка. нельзя в прошлом
  if(isNaN(selDateTime.getTime())){
    if(tg && tg.showAlert){tg.showAlert('Неверная дата');}else{alert('Неверная дата');}
    return;
  }
  if(selDateTime.getTime() - nowDt.getTime() < 60000){
    if(tg && tg.showAlert){tg.showAlert('Нельзя ставить встречу в прошлом');}else{alert('Нельзя ставить встречу в прошлом');}
    return;
  }
  closeModal('schedModal');
  var newMeeting = {res:res, date:parts[0]+'.'+parts[1], time:selTime, link:'', _createdAt: Date.now()};
  if(!cache.schedule) cache.schedule = [];
  cache.schedule.push(newMeeting);
  // Сохраняем как pending с timestamp (защита от гонки loadAllData)
  if(!window._pendingMeetings) window._pendingMeetings = [];
  window._pendingMeetings.push(newMeeting);
  try{localStorage.setItem('bs_cache', JSON.stringify(cache));}catch(e){}
  renderAllPages();
  showToast('Встреча: '+res+' '+selDate+' '+selTime);
  callAction('addSchedule', {res, date: selDate, time: selTime}).then(function(r){
    if(r && (r.duplicate || r.deduplicated)){ showToast('У ' + res + ' уже есть встреча в этот день'); }
    // Через 10 сек удаляем из pending. она должна быть на сервере
    setTimeout(function(){
      if(window._pendingMeetings){
        window._pendingMeetings = window._pendingMeetings.filter(function(p){
          return !(p.res===newMeeting.res && p.date===newMeeting.date && p.time===newMeeting.time);
        });
      }
      loadAllData();
    }, 10000);
  });
}

function initPayModal(){
  setPayType('income');
  setPayMethod('cash');
  document.getElementById('payAmount').value = '';
  // Заполняем список резидентов
  var residents = (cache.residents||[]).filter(r=>!r.isFired&&r.name!=='Тест'&&r.name!=='Тест2');
  document.getElementById('payResident').innerHTML = residents.map(r=>`<option value="${r.name}">${r.name}</option>`).join('');
  updatePayResidentVisibility();
  lockPayHeight(true);
}
// Шторка держит самую большую высоту: переключение приход/расход её не двигает
function lockPayHeight(reset){
  var m = document.querySelector('#payModal .modal');
  if(!m) return;
  if(reset){ m._maxH = 0; m.style.minHeight = ''; }
  requestAnimationFrame(function(){
    var h = m.offsetHeight;
    if(h > (m._maxH||0)){ m._maxH = h; m.style.minHeight = h + 'px'; }
  });
}

function updatePayResidentVisibility(){
  var src = document.getElementById('paySource').value || '';
  // Показываем выбор резидента только для категорий связанных с резидентами
  var needsResident = src.indexOf('БХ Трекинг')>=0 || src.indexOf('БХ Штраф')>=0 || src.indexOf('Экспресс')>=0;
  document.getElementById('payResidentGroup').style.display = (payType==='income'&&needsResident) ? 'block' : 'none';
}

function setPayType(type){
  payType = type;
  document.getElementById('payTypeIncome').classList.toggle('selected', type==='income');
  document.getElementById('payTypeExpense').classList.toggle('selected', type==='expense');
  const src = type==='income' ? INCOME_SRC : EXPENSE_SRC;
  document.getElementById('paySource').innerHTML = src.map(s=>`<option value="${s}">${s}</option>`).join('');
  // Комиссия банка 4% удерживается только с ПРИХОДА. для расхода способ не нужен
  var mg = document.getElementById('payMethodGroup');
  if(mg) mg.style.display = (type === 'expense') ? 'none' : '';
  if(type === 'expense') setPayMethod('cash');
  if(typeof updatePayResidentVisibility==='function')updatePayResidentVisibility();
  lockPayHeight(false);
}

function setPayMethod(method){
  payMethod = method;
  document.getElementById('payCash').classList.toggle('selected', method==='cash');
  document.getElementById('payBank').classList.toggle('selected', method==='bank');
}

// Оплаты: каждая запись уходит сразу, без ожидания предыдущей на экране.
// Запросы идут по очереди, данные обновляются один раз после последней
var _payQueue = Promise.resolve(), _payPending = 0, _payRecent = {};
function _optimisticPL(type, amount, isCash){
  try{
    var mp = window._monthlyPL; if(!mp || !mp.months) return;
    var m = mp.months[new Date().getMonth()]; if(!m) return;
    if(type === 'income'){
      var fee = isCash ? 0 : Math.round(amount*0.04);
      m.revenue += amount; m.expenses += fee; m.profit += amount - fee;
    } else { m.expenses += amount; m.profit -= amount; }
    m.hasData = true;
  }catch(e){}
}
function submitPayment(){
  const amount = Number(getMoneyValue('payAmount')) || 0;
  const src = document.getElementById('paySource').value;
  const resName = document.getElementById('payResidentGroup').style.display!=='none'
    ? document.getElementById('payResident').value : '';
  if(!amount){showToast('Введите сумму');return;}
  var t = payType, cash = payMethod==='cash';
  var key = [t, src, amount, resName].join('|');
  if(_payRecent[key] && Date.now() - _payRecent[key] < 4000){ showToast('Эта запись уже отправлена'); return; }
  _payRecent[key] = Date.now();
  closeModal('payModal');
  _optimisticPL(t, amount, cash);
  try{ renderAllPages(); }catch(e){}
  _payPending++;
  showToast('Записываем '+fmt(amount)+' ₸...');
  _payQueue = _payQueue.then(function(){
    return callAction('addPayment', {type: t, src: src, amount: amount, isCash: cash, resident: resName});
  }).then(function(r){
    if(r && r.deduplicated) showToast('Такая запись уже есть за сегодня. Дубль не создан');
    else if(r && r.ok) showToast('Записано: '+fmt(amount)+' ₸');
    else showToast('' + ((r && r.error) || 'Ошибка записи'));
  }).catch(function(){ showToast('Нет связи, запись не ушла'); })
  .then(function(){
    _payPending--;
    if(!_payPending) loadAllData({fresh: true, force: true});
  });
}


let arFormat = 'Офлайн';
let arVisits = 3;

function initAddResident(){
  document.getElementById('arName').value = '';
  var arDebt = document.getElementById('arDebt');
  arDebt.value = '';
  arDebt.dataset.raw = '0';
  arFormat = 'Офлайн';
  arVisits = 3;
  document.getElementById('arFmtOff').classList.add('selected');
  document.getElementById('arFmtOn').classList.remove('selected');
  document.getElementById('arT3').classList.add('selected');
  document.getElementById('arT4').classList.remove('selected');

  // Заполняем список партнёров (другие резиденты)
  try{
    var sel = document.getElementById('arPartner');
    if(sel){
      sel.innerHTML = '<option value="">- нет -</option>';
      var residents = (cache.residents || []).filter(function(r){
        return r && r.name && !r.isAdmin && !r.isTeam;
      });
      residents.sort(function(a,b){return (a.name||'').localeCompare(b.name||'');});
      residents.forEach(function(r){
        var opt = document.createElement('option');
        opt.value = r.name;
        opt.textContent = r.name;
        sel.appendChild(opt);
      });
    }
  }catch(prtErr){console.log('partner list err:', prtErr);}
}

function setArFormat(fmt){
  arFormat = fmt;
  document.getElementById('arFmtOff').classList.toggle('selected', fmt==='Офлайн');
  document.getElementById('arFmtOn').classList.toggle('selected', fmt==='Онлайн');
}

function setArVisits(n){
  arVisits = n;
  document.getElementById('arT3').classList.toggle('selected', n===3);
  document.getElementById('arT4').classList.toggle('selected', n===4);
}

async function submitAddResident(){
  var name = document.getElementById('arName').value.trim();
  if(!name){showToast('Введите имя');return;}
  var debt = getMoneyValue('arDebt') || '0';
  var source = document.getElementById('arSource').value;
  var partnerSel = document.getElementById('arPartner');
  var partner = partnerSel ? partnerSel.value : '';
  closeModal('addResidentModal');
  // Если это перевод подписчика. используем convertToResident
  if(window._convertingChatId){
    var chatId = window._convertingChatId;
    window._convertingChatId = null;
    showToast('"'+name+'" переведён в резиденты');
    callAction('convertToResident', {chatId:chatId, name:name, debt:debt, visits:arVisits, format:arFormat, tariff:debt, partner:partner}).then(function(){
      reloadSubscribers();
      loadAllData();
    });
    return;
  }
  showToast('Резидент "'+name+'" добавлен');
  callAction('addResident', {name, tariff: debt, debt, visits: arVisits, source, format: arFormat, partner:partner}).then(loadAllData);
}


function addToHomeScreen(){
  var ua = navigator.userAgent.toLowerCase();
  var isiOS = /iphone|ipad|ipod/.test(ua);
  var msg = isiOS 
    ? 'Как установить на iOS:\n\n1. Нажмите на ⋮ (три точки) сверху\n2. Выберите "Открыть в Safari"\n3. В Safari: кнопка Поделиться → "На экран Домой"'
    : 'Как установить на Android:\n\n1. Нажмите на ⋮ (три точки) сверху\n2. Выберите "Открыть в браузере"\n3. В Chrome: меню → "Добавить на главный экран"';
  if(tg && tg.showAlert){
    tg.showAlert(msg);
  } else {
    alert(msg);
  }
}


function setupMoneyInputs(){
  document.querySelectorAll('.money-input').forEach(function(inp){
    if(inp._moneySetup) return;
    inp._moneySetup = true;
    var fmt = function(){
      var raw = this.value.replace(/[^0-9]/g,'');
      this.dataset.raw = raw;
      if(raw){
        this.value = parseInt(raw).toLocaleString('ru-RU').replace(/,/g,' ');
      }
    };
    inp.addEventListener('input', fmt);
    inp.addEventListener('blur', fmt);
  });
}
function getMoneyValue(id){
  var el = document.getElementById(id);
  if(!el) return '';
  return el.dataset.raw || el.value.replace(/[^0-9]/g,'');
}

// Перехватываем openModal чтобы запускать setupMoneyInputs
(function(){
  var origOpen = window.openModal;
  window.openModal = function(id){
    if(origOpen) origOpen(id);
    else document.getElementById(id).classList.add('show');
    setTimeout(setupMoneyInputs, 100);
  };
})();

async function callAction(action, params){
  return (async function(){
    try {
      // ВСЕГДА добавляем chatId и имя пользователя если известны
      var paramsWithUser = Object.assign({}, params || {});
      // Для действий админа над подписчиком chatId — это тот, над кем действуют
      var keepTarget = ['banFromChannel','convertToResident','updateSubscriber'].indexOf(action) >= 0 && paramsWithUser.chatId;
      if(user && user.id){
        if(!keepTarget) paramsWithUser.chatId = String(user.id);
        if(user.first_name) paramsWithUser.userName = user.first_name + (user.last_name ? ' ' + user.last_name : '');
        if(user.username) paramsWithUser.userTg = user.username;
      }
      return await appGet(Object.assign({action: action}, paramsWithUser));
    } catch(e){
      console.error('callAction error:', e);
      return {error: String(e)};
    }
  })();
}

window.addEventListener('error', function(e){
  console.error('JS Error:', e.message, 'at', e.filename, ':', e.lineno);
});
window.addEventListener('unhandledrejection', function(e){
  console.error('Promise rejection:', e.reason);
});

function openModal(id){
  // Если открыто меню выбора. закрываем его, чтобы не было двух слоёв
  var am = document.querySelector('.add-menu-overlay');
  if(am && id === 'schedModal'){ /* меню закроется своим обработчиком */ }
  var mb = document.getElementById(id); mb.classList.add('show');
  try{ mb.scrollTop = 0; var mm = mb.querySelector('.modal'); if(mm) mm.scrollTop = 0; }catch(e){}
}
function closeModal(id){ document.getElementById(id).classList.remove('show'); }
function showToast(text){
  const t = document.getElementById('toast');
  t.textContent = text;
  t.classList.add('show');
  setTimeout(()=>t.classList.remove('show'), 2200);
}
function fmt(n){
  n = Number(n);
  if(!isFinite(n)) return '0';
  if(Math.round(n) !== n) return n.toLocaleString('ru-RU');
  return (n < 0 ? '\u2212' : '') + String(Math.abs(n)).replace(/\B(?=(\d{3})+(?!\d))/g, '\u00A0');
}

document.querySelectorAll('.modal-bg').forEach(m=>{
  m.addEventListener('click', e=>{ if(e.target===m) m.classList.remove('show'); });
});

init();

function openAddMenu(){
  // Меню добавления: онлайн встреча / офлайн встреча / мероприятие
  // Защита от повторного открытия (двойной клик, две кнопки на экране)
  if(document.querySelector('.add-menu-overlay')) return;
  var menu = document.createElement('div');
  menu.className = 'add-menu-overlay';
  menu.onclick = function(e){ if(e.target===menu) closeAddMenu(); };
  menu.innerHTML = 
    '<div class="add-menu">' +
      '<div class="add-menu-title">Что добавить?</div>' +
      '<button class="add-menu-item" data-action="online">' +
        '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg>' +
        '<div class="add-menu-item-text"><div class="add-menu-item-title">Онлайн встреча</div><div class="add-menu-item-sub">С резидентом, Google Meet</div></div>' +
      '</button>' +
      '<button class="add-menu-item" data-action="offline">' +
        '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/></svg>' +
        '<div class="add-menu-item-text"><div class="add-menu-item-title">Офлайн день</div><div class="add-menu-item-sub">Группа резидентов в офисе BS</div></div>' +
      '</button>' +
      '<button class="add-menu-item" data-action="event">' +
        '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polygon points="10 8 16 12 10 16 10 8"/></svg>' +
        '<div class="add-menu-item-text"><div class="add-menu-item-title">Мероприятие</div><div class="add-menu-item-sub">Съёмки, подкаст, другое</div></div>' +
      '</button>' +
      '<button class="add-menu-cancel" data-action="cancel">Отмена</button>' +
    '</div>';
  
  // Привязываем обработчики через data-action
  menu.querySelectorAll('[data-action]').forEach(function(btn){
    btn.addEventListener('click', function(e){
      e.stopPropagation();
      e.preventDefault();
      try{ btn.blur(); }catch(blurE){}
      var action = btn.getAttribute('data-action');
      if(action === 'online'){
        closeAddMenu();
        setTimeout(function(){
          openModal('schedModal');
          initSchedModal();
        }, 250);
      } else if(action === 'offline'){
        closeAddMenu();
        setTimeout(function(){ openOfflineGroupModal(); }, 250);
      } else if(action === 'event'){
        openEventMenu();
      } else if(action === 'cancel'){
        closeAddMenu();
      }
    });
  });
  
  document.body.appendChild(menu);
  setTimeout(function(){menu.classList.add('show');}, 10);
}

function closeAddMenu(){
  var menu = document.querySelector('.add-menu-overlay');
  if(!menu) return;
  menu.classList.remove('show');
  setTimeout(function(){menu.remove();}, 200);
}

function openEventMenu(){
  // Подменю выбора типа мероприятия
  closeAddMenu();
  setTimeout(function(){
    var menu = document.createElement('div');
    menu.className = 'add-menu-overlay';
    menu.onclick = function(e){ if(e.target===menu) closeAddMenu(); };
    menu.innerHTML = 
      '<div class="add-menu">' +
        '<div class="add-menu-title">Тип мероприятия</div>' +
        '<button class="add-menu-item" data-type="Встреча">' +
          '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/></svg>' +
          '<div class="add-menu-item-text"><div class="add-menu-item-title">Встреча</div></div>' +
        '</button>' +
        '<button class="add-menu-item" data-type="Съёмки">' +
          '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg>' +
          '<div class="add-menu-item-text"><div class="add-menu-item-title">Съёмки</div></div>' +
        '</button>' +
        '<button class="add-menu-item" data-type="Подкаст">' +
          '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/></svg>' +
          '<div class="add-menu-item-text"><div class="add-menu-item-title">Подкаст</div></div>' +
        '</button>' +
        '<button class="add-menu-item" data-type="Другое">' +
          '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/></svg>' +
          '<div class="add-menu-item-text"><div class="add-menu-item-title">Другое</div><div class="add-menu-item-sub">Введёшь название</div></div>' +
        '</button>' +
        '<button class="add-menu-cancel" data-cancel="1">Назад</button>' +
      '</div>';
    
    menu.querySelectorAll('[data-type]').forEach(function(btn){
      btn.addEventListener('click', function(e){
        e.stopPropagation();
        e.preventDefault();
        try{ btn.blur(); }catch(blurE){}
        var type = btn.getAttribute('data-type');
        createEvent(type);
      });
    });
    menu.querySelectorAll('[data-cancel]').forEach(function(btn){
      btn.addEventListener('click', function(e){
        e.stopPropagation();
        closeAddMenu();
      });
    });
    
    document.body.appendChild(menu);
    setTimeout(function(){menu.classList.add('show');}, 10);
  }, 220);
}

function createEvent(type){
  closeAddMenu();
  setTimeout(function(){ openEventCreateModal(type); }, 250);
}

function openEventCreateModal(type){
  // Полноценная модалка с полями вместо prompt() (prompt в Telegram WebApp не работает)
  var modal = document.createElement('div');
  modal.className = 'add-menu-overlay show';
  modal.onclick = function(e){ if(e.target===modal) modal.remove(); };
  
  var isOther = (type === 'Другое');
  var nameField = isOther 
    ? '<div class="field-group"><label class="field-label">Название</label><input type="text" id="evName" class="field-input" placeholder="Например, эфир с гостем"></div>'
    : '';
  
  modal.innerHTML = 
    '<div class="add-menu" style="max-height:90vh;overflow-y:auto">' +
      '<div class="add-menu-title">Новое мероприятие: ' + type + '</div>' +
      nameField +
      '<div class="field-group">' +
        '<label class="field-label">Дата</label>' +
        '<input type="date" id="evDate" class="field-input">' +
      '</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Время</label>' +
        '<input type="time" id="evTime" class="field-input" value="19:00">' +
      '</div>' +
      '<button class="btn btn-primary" id="evSubmit" style="width:100%;margin-top:8px">Создать мероприятие</button>' +
      '<button class="add-menu-cancel" id="evCancel">Отмена</button>' +
    '</div>';
  
  document.body.appendChild(modal);
  
  // Привязываем обработчики
  modal.querySelector('#evCancel').addEventListener('click', function(){ modal.remove(); });
  modal.querySelector('#evSubmit').addEventListener('click', function(){
    var name = type;
    if(isOther){
      var nameInput = modal.querySelector('#evName');
      if(!nameInput.value.trim()){ showToast('Введи название'); return; }
      name = nameInput.value.trim();
    }
    var dateRaw = modal.querySelector('#evDate').value; // YYYY-MM-DD
    var timeRaw = modal.querySelector('#evTime').value; // HH:MM
    if(!dateRaw){ showToast('Выбери дату'); return; }
    if(!timeRaw){ showToast('Выбери время'); return; }
    
    // Конвертируем YYYY-MM-DD → DD.MM.YYYY
    var dp = dateRaw.split('-');
    var dateStr = dp[2] + '.' + dp[1] + '.' + dp[0];
    
    modal.remove();
    showToast('Создаём мероприятие...');
    callAction('createEvent', {type:type, name:name, date:dateStr, time:timeRaw}).then(function(r){
      if(r && r.ok){
        showToast('Мероприятие создано в Google Calendar');
        setTimeout(loadAllData, 2000);
      } else {
        showToast('Ошибка: ' + (r && r.error || 'неизвестно'));
      }
    });
  });
  
  // Автофокус на нужное поле
  setTimeout(function(){
    var first = modal.querySelector(isOther ? '#evName' : '#evDate');
    if(first) first.focus();
  }, 100);
}


// ══════════ ЗАДАЧИ РЕЗИДЕНТОВ (вкладка Задачи, блок снизу) ══════════
var CRM_STATUSES = ['Новый','Взят в работу','Недозвон','Квалифицирован',
  'Записан на диагностику','Диагностика проведена','Резидент','Платформа','Отказ','Не целевой'];
var CRM_COLORS = {
  'Новый':'#D9D9D9','Взят в работу':'#9A9A9A','Недозвон':'#FF6B6F',
  'Квалифицирован':'#FFFFFF','Записан на диагностику':'#9A9A9A',
  'Диагностика проведена':'#9A9A9A','Резидент':'#FFFFFF','Платформа':'#FFFFFF',
  'Отказ':'#777','Не целевой':'#555'
};
var _crmFilter = 'Все';

function setCrmFilter(f){ _crmFilter = f; renderCrm(); }

function renderCrm(){
  var el = document.getElementById('crmBox');
  var fl = document.getElementById('crmFilters');
  if(!el) return;
  var all = (cache.leads && cache.leads.leads) || [];

  // Фильтры
  if(fl){
    var counts = {'Все': all.length};
    all.forEach(function(l){ counts[l.status] = (counts[l.status]||0) + 1; });
    var fh = '';
    ['Все','Новый','Взят в работу','Квалифицирован','Записан на диагностику','Резидент','Отказ'].forEach(function(s){
      var cnt = counts[s] || 0;
      if(s !== 'Все' && !cnt) return;
      var act = _crmFilter === s;
      fh += '<button onclick="setCrmFilter(\'' + s + '\')" style="flex:0 0 auto;padding:8px 13px;border-radius:18px;border:1.5px solid ' +
        (act ? '#FFFFFF' : 'var(--border)') + ';background:' + (act ? 'rgba(255,255,255,.13)' : 'transparent') +
        ';color:' + (act ? '#FFFFFF' : '#888') + ';font-size:12.5px;font-weight:800;font-family:inherit;white-space:nowrap">' +
        s + (cnt ? ' · ' + cnt : '') + '</button>';
    });
    fl.innerHTML = fh;
  }

  var list = _crmFilter === 'Все' ? all : all.filter(function(l){ return l.status === _crmFilter; });

  if(!all.length){
    el.innerHTML = '<div class="empty"><div class="em-icon"></div><div class="em-title">Лидов пока нет</div>' +
      '<div class="em-text">Подключите форму на сайте или импортируйте подписчиков из меню таблицы</div></div>';
    return;
  }
  if(!list.length){ el.innerHTML = '<div class="empty"><div class="em-text">В этом статусе пусто</div></div>'; return; }

  var h = '';
  list.slice(0, 60).forEach(function(l){
    var color = CRM_COLORS[l.status] || '#888';
    h += '<div class="card" style="padding:14px 16px;margin-bottom:8px;border-left:3px solid ' + color + ';cursor:pointer" onclick="openLead(' + l.row + ')">' +
      '<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:10px">' +
        '<div style="min-width:0;flex:1">' +
          '<div style="font-size:15px;font-weight:700;color:#fff">' + (l.name || l.phone || 'Без имени') + '</div>' +
          (l.phone ? '<div style="font-size:12.5px;color:#aaa;margin-top:2px">' + l.phone + '</div>' : '') +
          '<div style="font-size:11.5px;color:#777;margin-top:3px">' + l.source + (l.niche ? ' · ' + l.niche : '') + '</div>' +
        '</div>' +
        '<div style="text-align:right;flex-shrink:0">' +
          '<div style="font-size:10.5px;font-weight:800;color:' + color + ';white-space:nowrap">' + l.status + '</div>' +
          '<div style="font-size:10.5px;color:#666;margin-top:3px">' + l.date + '</div>' +
        '</div>' +
      '</div>' +
      (l.request ? '<div style="font-size:12px;color:#999;margin-top:8px;line-height:1.4">' + l.request.substring(0,110) + (l.request.length>110?'...':'') + '</div>' : '') +
    '</div>';
  });
  if(list.length > 60) h += '<div style="font-size:12px;color:#666;text-align:center;padding:10px">Показаны последние 60 из ' + list.length + '</div>';
  el.innerHTML = h;
}

function openLead(row){
  var l = ((cache.leads && cache.leads.leads) || []).find(function(x){ return x.row === row; });
  if(!l) return;
  window._leadRow = row;
  document.getElementById('leadTitle').textContent = l.name || l.phone || 'Лид';

  var info = '';
  if(l.phone) info += '<div style="margin-bottom:6px"><a href="tel:' + l.phone + '" style="color:#FFFFFF;font-size:16px;font-weight:700;text-decoration:none">' + l.phone + '</a></div>';
  if(l.telegram) info += '<div style="margin-bottom:6px"><a href="https://t.me/' + l.telegram.replace('@','') + '" target="_blank" style="color:#9A9A9A;font-size:14px;text-decoration:none">' + l.telegram + '</a></div>';
  info += '<div style="font-size:12.5px;color:#888;line-height:1.6;margin-top:8px">' +
    'Источник: ' + l.source + (l.campaign ? '<br>Кампания: ' + l.campaign : '') +
    (l.niche ? '<br>Ниша: ' + l.niche : '') + (l.revenue ? '<br>Оборот: ' + l.revenue : '') +
    '<br>Дата: ' + l.date + '</div>';
  if(l.request) info += '<div style="margin-top:10px;padding:11px 13px;background:var(--card2);border-radius:9px;font-size:13px;color:#ccc;line-height:1.45">' + l.request + '</div>';
  document.getElementById('leadInfo').innerHTML = info;

  var sel = document.getElementById('leadStatus');
  if(sel) sel.innerHTML = CRM_STATUSES.map(function(s){
    return '<option' + (s === l.status ? ' selected' : '') + '>' + s + '</option>';
  }).join('');
  var cm = document.getElementById('leadComment');
  if(cm) cm.value = l.comment || '';
  openModal('leadModal');
}

var _leadSaveInFlight = false;
async function submitLead(){
  if(_leadSaveInFlight) return;
  var row = window._leadRow;
  if(!row) return;
  var status = document.getElementById('leadStatus').value;
  var comment = document.getElementById('leadComment').value.trim();
  _leadSaveInFlight = true;
  closeModal('leadModal');
  try{
    var r = await callAction('setLeadStatus', {
      row: row, status: status, comment: comment,
      owner: (user && user.first_name) || ''
    });
    if(r && r.ok){
      patchCache('leads.leads', function(list){
        return (list||[]).map(function(x){
          return x.row === row ? Object.assign({}, x, {status: status, comment: comment}) : x;
        });
      });
      renderCrm();
      showToast('Сохранено');
      silentSync();
    } else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(function(){ _leadSaveInFlight = false; }, 1200); }
}

function renderVisits(){
  var el = document.getElementById('visitsBox');
  if(!el) return;
  var list = (cache.residents||[]).filter(function(r){
    return !r.isFired && !r.isAdmin && !r.isTeam && r.name!=='Тест' && r.name!=='Тест2';
  });
  if(!list.length){ el.innerHTML = '<div class="empty">Нет резидентов</div>'; return; }

  // Сортировка: сначала должники, потом заканчивающиеся, потом остальные
  list.sort(function(a,b){
    var la = (a.meetingsGranted||0) - (a.meetingsDone||0);
    var lb = (b.meetingsGranted||0) - (b.meetingsDone||0);
    return la - lb;
  });

  var debtors = 0, ending = 0;
  list.forEach(function(r){
    var left = (r.meetingsGranted||0) - (r.meetingsDone||0);
    if(!r.meetingsGranted) return;
    if(left < 0) debtors++;
    else if(left <= 2) ending++;
  });

  var h = '<div class="card" style="padding:14px 16px;margin-bottom:14px;display:grid;grid-template-columns:1fr 1fr 1fr;gap:8px;text-align:center">' +
    '<div><div style="font-size:20px;font-weight:800;color:#fff;line-height:1">' + list.length + '</div>' +
    '<div style="font-size:9.5px;color:#888;letter-spacing:0.5px;margin-top:4px">РЕЗИДЕНТОВ</div></div>' +
    '<div style="border-left:1px solid rgba(255,255,255,.07);border-right:1px solid rgba(255,255,255,.07)">' +
    '<div style="font-size:20px;font-weight:800;color:' + (ending?'#D9D9D9':'#FFFFFF') + ';line-height:1">' + ending + '</div>' +
    '<div style="font-size:9.5px;color:#888;letter-spacing:0.5px;margin-top:4px">ЗАКАНЧИВАЕТСЯ</div></div>' +
    '<div><div style="font-size:20px;font-weight:800;color:' + (debtors?'#FF6B6F':'#FFFFFF') + ';line-height:1">' + debtors + '</div>' +
    '<div style="font-size:9.5px;color:#888;letter-spacing:0.5px;margin-top:4px">В ДОЛГ</div></div></div>';

  list.forEach(function(r){
    var done = r.meetingsDone || 0;
    var granted = r.meetingsGranted || 0;
    var left = granted - done;
    var color = !granted ? '#777' : (left < 0 ? '#FF6B6F' : (left <= 2 ? '#D9D9D9' : '#FFFFFF'));
    var pct = granted ? Math.max(0, Math.min(100, done / granted * 100)) : 0;

    h += '<div class="card" style="padding:14px 16px;margin-bottom:8px;cursor:pointer" onclick="openVisitEdit(\'' +
      String(r.name).replace(/'/g,"\\'") + '\')">' +
      '<div style="display:flex;justify-content:space-between;align-items:center;gap:12px">' +
        '<div style="min-width:0">' +
          '<div style="font-size:15px;font-weight:700;color:#fff">' + r.name + '</div>' +
          (r.nextRenewal ? '<div style="font-size:11.5px;color:#777;margin-top:2px">оплачено до ' + r.nextRenewal + '</div>' : '') +
        '</div>' +
        '<div style="text-align:right;flex-shrink:0">' +
          '<div style="font-size:19px;font-weight:800;color:' + color + ';line-height:1;font-variant-numeric:tabular-nums">' +
            done + ' / ' + (granted || '?') + '</div>' +
          '<div style="font-size:9.5px;color:#888;letter-spacing:0.5px;margin-top:3px">ВСТРЕЧ</div>' +
        '</div>' +
      '</div>' +
      (granted ? '<div style="height:5px;background:#222;border-radius:3px;overflow:hidden;margin-top:10px">' +
        '<div style="height:100%;width:' + pct + '%;background:' + color + ';border-radius:3px"></div></div>' : '') +
    '</div>';
  });

  el.innerHTML = h;
}

function openVisitEdit(name){
  var r = (cache.residents||[]).find(function(x){ return x.name === name; });
  if(!r) return;
  window._visitEditName = name;
  document.getElementById('visitEditTitle').textContent = name;
  document.getElementById('visitDone').value = r.meetingsDone || 0;
  document.getElementById('visitGranted').value = r.meetingsGranted || 0;
  openModal('visitEditModal');
}

var _visitSaveInFlight = false;
async function submitVisitEdit(){
  if(_visitSaveInFlight) return;
  var name = window._visitEditName;
  var done = parseInt(document.getElementById('visitDone').value) || 0;
  var granted = parseInt(document.getElementById('visitGranted').value) || 0;
  if(!name) return;
  _visitSaveInFlight = true;
  closeModal('visitEditModal');
  showToast('Сохраняем...');
  try{
    var res = await callAction('setMeetings', {name: name, done: done, granted: granted});
    if(res && res.ok){
      patchCache('residents', function(list){
        return (list||[]).map(function(x){
          return x.name === name ? Object.assign({}, x, {meetingsDone: done, meetingsGranted: granted}) : x;
        });
      });
      renderVisits();
      showToast('Сохранено');
      silentSync();
    } else showToast('' + ((res && res.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(function(){ _visitSaveInFlight = false; }, 1200); }
}

async function renewMeetings(){
  var name = window._visitEditName;
  if(!name) return;
  if(!confirm('Продлить ' + name + '?\n\nСчётчик обнулится, неиспользованные встречи перенесутся в новый пакет.')) return;
  closeModal('visitEditModal');
  showToast('Продлеваем...');
  try{
    var r = await callAction('renewMeetings', {name: name});
    if(r && r.ok){
      patchCache('residents', function(list){
        return (list||[]).map(function(x){
          return x.name === name ? Object.assign({}, x, {meetingsDone: 0, meetingsGranted: r.total}) : x;
        });
      });
      renderVisits();
      var txt = 'Новый пакет: ' + r.pack;
      if(r.carried > 0) txt += ', перенесено ' + r.carried;
      if(r.carried < 0) txt += ', списано ' + Math.abs(r.carried);
      showToast('' + txt + '. Итого ' + r.total);
      silentSync();
    } else showToast('' + ((r && r.error) || 'Ошибка'));
  }catch(e){ showToast('' + e.message); }
}

function addOneMeeting(delta){
  var f = document.getElementById('visitDone');
  if(f) f.value = Math.max(0, (parseInt(f.value)||0) + delta);
}
function addGranted(delta){
  var f = document.getElementById('visitGranted');
  if(f) f.value = Math.max(0, (parseInt(f.value)||0) + delta);
}

function renderAllResTasks(){
  var el = document.getElementById('allResTasksBox');
  if(!el) return;
  try{
    var tasks = (cache.resTasks && cache.resTasks.tasks) || [];
    var open = tasks.filter(function(t){ return t.status !== 'Выполнена'; });
    var done = tasks.filter(function(t){ return t.status === 'Выполнена'; });
    
    var h = '';
    
    if(!tasks.length){
      h += '<div class="card"><div style="font-size:13px;color:#888;text-align:center;padding:14px">Задач нет. Нажми + чтобы поставить задачу резиденту после трекинга.</div></div>';
      el.innerHTML = h;
      return;
    }
    
    // Группировка по резиденту
    var byRes = {};
    open.forEach(function(t){
      if(!byRes[t.name]) byRes[t.name] = [];
      byRes[t.name].push(t);
    });
    
    Object.keys(byRes).sort().forEach(function(nm){
      h += '<div class="card" style="margin-bottom:10px;padding:14px 16px">';
      h += '<div style="font-weight:800;color:#FFFFFF;font-size:14px;margin-bottom:10px">' + nm + '</div>';
      byRes[nm].forEach(function(t){
        h += '<div style="display:flex;align-items:flex-start;gap:10px;padding:8px 0;border-top:1px solid rgba(255,255,255,0.05)">' +
          '<div style="width:16px;height:16px;border:2px solid #555;border-radius:4px;flex-shrink:0;margin-top:2px"></div>' +
          '<div style="flex:1"><div style="font-size:13.5px;color:#fff;line-height:1.4">' + t.task + '</div>' +
          '<div style="font-size:11px;color:#777;margin-top:3px">' + t.date + '</div></div></div>';
      });
      h += '</div>';
    });
    
    if(done.length){
      h += '<div style="font-size:12px;color:#FFFFFF;font-weight:800;padding:4px 4px 10px">Выполнено задач: ' + done.length + '</div>';
    }
    el.innerHTML = h;
  }catch(e){
    el.innerHTML = '';
  }
}

function openAddResTaskModal(){
  var residents = (cache.residents||[]).filter(function(r){
    return !r.isFired && !r.isAdmin && !r.isTeam && r.name!=='Тест' && r.name!=='Тест2';
  });
  if(!residents.length){ showToast('Нет резидентов'); return; }
  var sel = document.getElementById('resTaskResident');
  if(sel) sel.innerHTML = residents.map(function(r){ return '<option value="'+r.name+'">'+r.name+'</option>'; }).join('');
  var ta = document.getElementById('resTaskText');
  if(ta) ta.value = '';
  openModal('addResTaskModal');
}

let _addResTaskInFlight = false;
async function submitAddResTask(){
  if(_addResTaskInFlight) return;
  var name = document.getElementById('resTaskResident').value;
  var task = document.getElementById('resTaskText').value.trim();
  if(!task){ showToast('Напиши задачу'); return; }
  _addResTaskInFlight = true;
  closeModal('addResTaskModal');
  showToast('Создаём...');
  try{
    var r = await callAction('addResTask', {name: name, task: task, author: (user && user.first_name) || 'Админ'});
    if(r && r.ok){
      showToast('Задача поставлена. ' + name + ' получил уведомление');
      renderAllResTasks();
    } else {
      showToast('' + (r && r.error || 'Ошибка'));
    }
  }catch(e){ showToast('' + e.message); }
  finally{ setTimeout(function(){ _addResTaskInFlight = false; }, 1500); }
}

function renderTodos(){
  var el = document.getElementById('page-todos');
  if(!el) return;
  if(!isRealAdmin || viewAs !== 'admin'){ el.innerHTML = ''; return; }
  
  // Шапка рисуется один раз
  if(!el.querySelector('.todos-header')){
    el.innerHTML = '';
    var headerEl = document.createElement('div');
    headerEl.className = 'todos-header section-head';
    headerEl.innerHTML = 
      '<div class="section-title">Задачи команды</div>' +
      '<div style="display:flex;gap:8px;align-items:center">' +
        '<button class="icon-btn" id="todoSyncBtn" title="Импортировать из Google Doc">' +
          '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>' +
        '</button>' +
        '<button class="icon-btn" id="todoAddBtn" title="Добавить задачу">' +
          '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>' +
        '</button>' +
      '</div>';
    el.appendChild(headerEl);
    
    var bodyEl = document.createElement('div');
    bodyEl.className = 'todos-body';
    el.appendChild(bodyEl);
    
    el.querySelector('#todoAddBtn').addEventListener('click', function(e){
      try{this.blur();}catch(blurE){}
      openAddTodoModal();
    });
    el.querySelector('#todoSyncBtn').addEventListener('click', function(e){
      try{this.blur();}catch(blurE){}
      showToast('Открой таблицу → BS → Импорт задач из Google Doc');
    });
  }
  
  var bodyEl = el.querySelector('.todos-body');
  bodyEl.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  
  // Сначала показываем из кеша моментально
  function _renderTodosUI(tasks){
    if(!tasks || !tasks.length){
      bodyEl.innerHTML = '<div class="empty"><div class="em-icon"></div><div class="em-title">Нет задач</div><div class="em-text">Нажми + чтобы добавить</div></div>';
      return;
    }
    tasks.sort(function(a,b){
      if(a.status === b.status) return 0;
      return a.status === 'done' ? 1 : -1;
    });
    bodyEl.innerHTML = tasks.map(function(t){ return todoCard(t); }).join('');
    
    // Чекбокс
    bodyEl.querySelectorAll('.todo-check').forEach(function(box){
      box.addEventListener('click', function(e){
        e.stopPropagation();
        var id = box.getAttribute('data-id');
        var newStatus = box.getAttribute('data-status') === 'done' ? 'open' : 'done';
        // Оптимистично обновляем UI
        var card = box.closest('.todo-item');
        if(card) card.classList.toggle('done', newStatus === 'done');
        box.setAttribute('data-status', newStatus);
        callAction('toggleTodo', {id:id, status:newStatus}).then(function(rr){
          if(rr && rr.ok){
            // Обновим кеш
            try{
              var c = JSON.parse(localStorage.getItem('bs_todos_cache')||'[]');
              for(var i=0;i<c.length;i++) if(String(c[i].id)===String(id)) c[i].status = newStatus;
              localStorage.setItem('bs_todos_cache', JSON.stringify(c));
            }catch(e){}
          }
        });
      });
    });
    
    // Удаление
    bodyEl.querySelectorAll('.todo-del').forEach(function(btn){
      btn.addEventListener('click', function(e){
        e.stopPropagation();
        var id = btn.getAttribute('data-id');
        if(!confirm('Удалить задачу?')) return;
        // Оптимистично убираем
        var card = btn.closest('.todo-item');
        if(card) card.style.display = 'none';
        callAction('deleteTodo', {id:id}).then(function(rr){
          if(rr && rr.ok){
            try{
              var c = JSON.parse(localStorage.getItem('bs_todos_cache')||'[]');
              c = c.filter(function(t){ return String(t.id) !== String(id); });
              localStorage.setItem('bs_todos_cache', JSON.stringify(c));
            }catch(e){}
            renderTodos();
          }
        });
      });
    });
    
    // Inline редактирование (клик по тексту)
    bodyEl.querySelectorAll('.todo-text').forEach(function(span){
      span.addEventListener('click', function(e){
        e.stopPropagation();
        if(span.querySelector('input')) return;
        var id = span.getAttribute('data-id');
        var oldText = span.textContent.trim();
        var input = document.createElement('input');
        input.type = 'text';
        input.value = oldText;
        input.className = 'todo-edit-input';
        input.style.cssText = 'width:100%;padding:8px 10px;font-size:15px;font-weight:600;color:#fff;background:rgba(255,255,255,0.08);border:1px solid #FFFFFF;border-radius:8px;outline:none;font-family:inherit;';
        span.textContent = '';
        span.appendChild(input);
        input.focus();
        input.select();
        
        function save(){
          var newText = input.value.trim();
          if(!newText || newText === oldText){
            span.textContent = oldText;
            return;
          }
          span.textContent = newText;
          callAction('updateTodo', {id:id, text:newText}).then(function(rr){
            if(rr && rr.ok){
              try{
                var c = JSON.parse(localStorage.getItem('bs_todos_cache')||'[]');
                for(var i=0;i<c.length;i++) if(String(c[i].id)===String(id)) c[i].text = newText;
                localStorage.setItem('bs_todos_cache', JSON.stringify(c));
              }catch(e){}
              showToast('Сохранено');
            } else {
              span.textContent = oldText;
              showToast('Ошибка сохранения');
            }
          });
        }
        
        input.addEventListener('blur', save);
        input.addEventListener('keydown', function(ke){
          if(ke.key === 'Enter'){ ke.preventDefault(); input.blur(); }
          else if(ke.key === 'Escape'){ span.textContent = oldText; }
        });
      });
    });
  }
  
  // 1) Показываем из кеша сразу
  try{
    var cached = JSON.parse(localStorage.getItem('bs_todos_cache')||'[]');
    if(cached.length){
      _renderTodosUI(cached);
    } else {
      bodyEl.innerHTML = '<div class="empty"><div class="em-text">Загрузка...</div></div>';
    }
  }catch(e){
    bodyEl.innerHTML = '<div class="empty"><div class="em-text">Загрузка...</div></div>';
  }
  
  // 2) Запрашиваем свежие задачи с сервера и обновляем UI + кеш
  callAction('getTodos', {}).then(function(r){
    if(!r || !r.ok){
      // Если кеш пустой и сервер не отвечает. покажем ошибку
      try{
        var c2 = JSON.parse(localStorage.getItem('bs_todos_cache')||'[]');
        if(!c2.length) bodyEl.innerHTML = '<div class="empty"><div class="em-text">Не удалось загрузить</div></div>';
      }catch(e){}
      return;
    }
    var tasks = r.tasks || [];
    try{ localStorage.setItem('bs_todos_cache', JSON.stringify(tasks)); }catch(e){}
    _renderTodosUI(tasks);
  });
}

function todoCard(t){
  var isDone = t.status === 'done';
  var deadlineText = '';
  if(t.deadline){
    try{
      var d = new Date(t.deadline);
      deadlineText = '<div class="todo-deadline">' + 
        ('0'+d.getDate()).slice(-2) + '.' + ('0'+(d.getMonth()+1)).slice(-2) + '.' + d.getFullYear() +
        '</div>';
    }catch(e){}
  }
  return '<div class="todo-item ' + (isDone?'done':'') + '">' +
    '<div class="todo-check" data-id="' + t.id + '" data-status="' + t.status + '">' +
      (isDone 
        ? '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>'
        : '') +
    '</div>' +
    '<div class="todo-content">' +
      '<div class="todo-text" data-id="' + t.id + '" title="Кликни чтобы редактировать" style="cursor:pointer;">' + escapeHtml(t.text) + '</div>' +
      deadlineText +
    '</div>' +
    '<button class="todo-del" data-id="' + t.id + '" title="Удалить">' +
      '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>' +
    '</button>' +
  '</div>';
}

function escapeHtml(s){
  return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function openAddTodoModal(){
  var modal = document.createElement('div');
  modal.className = 'add-menu-overlay show';
  modal.onclick = function(e){ if(e.target===modal) modal.remove(); };
  
  modal.innerHTML = 
    '<div class="add-menu" style="max-height:90vh;overflow-y:auto">' +
      '<div class="add-menu-title">Новая задача</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Текст задачи</label>' +
        '<input type="text" id="todoTextInp" class="field-input" placeholder="Например, написать пост в канал">' +
      '</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Дедлайн (опционально)</label>' +
        '<input type="date" id="todoDeadlineInp" class="field-input">' +
      '</div>' +
      '<button class="btn btn-primary" id="todoSubmit" style="width:100%;margin-top:8px">Создать задачу</button>' +
      '<button class="add-menu-cancel" id="todoCancel">Отмена</button>' +
    '</div>';
  
  document.body.appendChild(modal);
  
  modal.querySelector('#todoCancel').addEventListener('click', function(){ modal.remove(); });
  modal.querySelector('#todoSubmit').addEventListener('click', function(){
    var textInp = modal.querySelector('#todoTextInp');
    var deadlineInp = modal.querySelector('#todoDeadlineInp');
    var text = textInp.value.trim();
    if(!text){ showToast('Введи текст'); return; }
    var deadline = deadlineInp.value || '';
    
    modal.remove();
    showToast('Создаём задачу...');
    callAction('addTodo', {text:text, deadline:deadline}).then(function(r){
      if(r && r.ok){
        showToast('Задача создана');
        renderTodos();
      } else {
        showToast('' + (r && r.error || 'Ошибка'));
      }
    });
  });
  
  setTimeout(function(){
    var inp = modal.querySelector('#todoTextInp');
    if(inp) inp.focus();
  }, 100);
}


function renderRules(){
  var el = document.getElementById('rulesContent');
  if(!el) return;
  if(el.dataset.done === '1') return; // статичная страница, рисуем один раз
  el.dataset.done = '1';

  var blocks = [
    {
      num: '01',
      title: 'Отчёт каждый день',
      items: [
        'Отчёт в группу до 22:00, минимум 150 символов',
        'Что сделал за день, какие цифры, где затык',
        'Пропуск без предупреждения. штраф 10 000 ₸',
        'Отчёт это не для нас. Это ваш способ видеть себя со стороны'
      ]
    },
    {
      num: '02',
      title: 'Трекинг каждые 10 дней',
      items: [
        'Встреча с обоими основателями: диагностика, стратегия, задачи',
        'Приходите с цифрами и подготовленными вопросами',
        'Отмена позже чем за 24 часа. встреча сгорает',
        'Опоздание больше 15 минут. штраф 10 000 ₸'
      ]
    },
    {
      num: '03',
      title: 'Задачи выполняются',
      items: [
        'После трекинга вы уходите с конкретным планом на 10 дней',
        'Задачи отмечаются в приложении по мере выполнения',
        'Невыполненная задача обсуждается на следующей встрече',
        'Мы не контролируем. Мы возвращаем вас к тому, что вы сами решили'
      ]
    },
    {
      num: '04',
      title: 'Оплата вовремя',
      items: [
        'Оплата до начала нового цикла',
        'Долг больше 10 дней. доступ к встречам приостанавливается',
        'Все начисления и остаток видны в приложении',
        'Вопросы по оплате решаются напрямую с Рустамом или Береке'
      ]
    },
    {
      num: '05',
      title: 'Конфиденциальность',
      items: [
        'Всё, что сказано в группе и на встречах, остаётся внутри',
        'Цифры других резидентов не обсуждаются за пределами клуба',
        'Нарушение. исключение без возврата средств',
        'Доверие внутри клуба это то, ради чего люди сюда приходят'
      ]
    },
    {
      num: '06',
      title: 'Среда важнее контента',
      items: [
        'Помогайте друг другу: связями, опытом, честной обратной связью',
        'Никаких продаж резидентам без их запроса',
        'Приводите сильных предпринимателей. за резидента бонус 100 000 ₸',
        'Клуб настолько силён, насколько сильны люди в нём'
      ]
    }
  ];

  var blocksHtml = blocks.map(function(b){
    return '<div class="rule-block">' +
      '<div class="rule-head">' +
        '<span class="rule-num">' + b.num + '</span>' +
        '<span class="rule-title">' + b.title + '</span>' +
      '</div>' +
      '<div class="rule-items">' +
        b.items.map(function(it){
          return '<div class="rule-item"><span class="rule-dot"></span><span>' + it + '</span></div>';
        }).join('') +
      '</div>' +
    '</div>';
  }).join('');

  el.innerHTML = `
    <div class="about-min">
      <div class="hero-min" style="padding-bottom:20px">
        <div class="hero-logo-real">
          <img src="data:image/png;base64,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" alt="BS">
        </div>
        <div class="hero-label">Резиденция</div>
        <h1 class="hero-title" style="font-size:34px">Правила клуба</h1>
        <p class="hero-sub">6 договорённостей, которые делают работу системной. Не бюрократия, а то, что отличает клуб от чата.</p>
      </div>
      <div class="rules-wrap">
        ${blocksHtml}
      </div>
      <div class="rules-docs">
        <div class="rules-docs-title">Документы</div>
        <a class="rules-doc" href="https://bxclub.kz/oferta" target="_blank">
          <span>Публичная оферта</span>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><polyline points="9 18 15 12 9 6"/></svg>
        </a>
        <a class="rules-doc" href="https://bxclub.kz/privacy" target="_blank">
          <span>Политика конфиденциальности</span>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><polyline points="9 18 15 12 9 6"/></svg>
        </a>
      </div>
      <div class="rules-foot">
        <div class="rules-foot-quote">Правила существуют не для контроля.<br>Они держат рамку, внутри которой бизнес растёт.</div>
        <div class="rules-foot-brand">BUSINESS SURGERY · BXCLUB.KZ</div>
      </div>
      <button class="v3-outline" style="margin-top:18px" onclick="v11rulesBack()">‹ О проекте</button>
    </div>
  `;
}

function v11rulesBack(){
  var st = window._navStack || [];
  if(st.length && st[st.length - 1] === 'about') v9goBack(); else showPage('about', {back: true});
}
function v3numrow(items){
  return '<div class="v3-grid3">' + items.map(function(x){
    return '<div><div class="num" style="font-size:22px;font-weight:800">' + x[0] + '</div><div style="margin-top:2px;font-size:11px;color:#9A9A9A;line-height:1.3">' + x[1] + '</div></div>';
  }).join('') + '</div>';
}
function v3socials(){
  return '<div class="v3-soc">' +
    '<a href="https://instagram.com/business.surgery" target="_blank">Instagram</a>' +
    '<a href="https://t.me/bsurgery_kz" target="_blank">Telegram</a>' +
    '<a href="https://www.youtube.com/@Businessurgery" target="_blank">YouTube</a>' +
    '<a href="https://bxclub.kz" target="_blank">Сайт</a></div>';
}
function renderAbout(){
  var el = document.getElementById('page-about');
  if(!el) return;
  var logo = bsLogoSrc();
  if(logo) window._bsLogoSrc = logo;   // логотип нужен для Stories
  var isLead = viewAs !== 'resident' && viewAs !== 'admin';
  var role = navRole();
  var h = '';
  if(isLead){
    // Главная лида: логотип, диагностика, экспресс-разбор, материалы
    var dr = null; try{ dr = JSON.parse(localStorage.getItem('bs_diagnostic_result') || 'null'); }catch(e){}
    // Список областей объявлен ниже по скрипту: при самом первом рисовании его ещё нет
    var AREAS = [], ds = null;
    try{ AREAS = DIAGNOSTIC_AREAS; ds = diagState; }catch(e){}
    var areas = AREAS.length || 7, ready = 0, nextArea = null;
    if(dr && dr.results) ready = areas;
    else if(ds && ds.started){
      AREAS.forEach(function(a){ var ans = ds.answers[a.id] || []; if(ans.length && ans.every(function(v){ return v !== null && v !== undefined; })) ready++; });
    }
    if(ready < areas) nextArea = AREAS[ready] || null;
    var diagText = ready >= areas ? 'Диагностика пройдена. Результат: ' + (dr && dr.totalPct != null ? dr.totalPct + ' из 100' : 'готов') + '.'
      : (ready && nextArea ? 'Готово органов: ' + ready + '. Дальше ' + nextArea.organ + ': ' + String(nextArea.label).toLowerCase() + '.' : 'Семь органов бизнеса, 5 минут. Покажем слабые места.');
    // Главный бесплатный оффер: 99 гайдов, сразу на первом экране
    h += v13leadHero();
    h += '<div class="v2-card" style="margin-top:12px;padding:16px 18px;cursor:pointer" onclick="showPage(\'diagnostic\')">' +
      '<div class="v3-row-sb" style="align-items:baseline"><div style="font-size:16px;font-weight:800">Диагностика бизнеса</div><div class="num" style="font-size:13px;color:#9A9A9A">' + ready + '/' + areas + '</div></div>' +
      '<div class="v3-dseg" style="margin-top:12px;grid-template-columns:repeat(' + areas + ',minmax(0,1fr))">' + Array.apply(null, Array(areas)).map(function(a, i){ return '<i' + (i < ready ? ' class="on"' : '') + '></i>'; }).join('') + '</div>' +
      '<div class="v3-row-sb" style="margin-top:12px"><div style="font-size:13px;color:#9A9A9A;line-height:1.45">' + v2esc(diagText) + '</div><div style="flex-shrink:0;font-size:13px;font-weight:800">' + (ready >= areas ? 'Результат →' : (ready ? 'Дальше →' : 'Начать →')) + '</div></div></div>';
    h += '<div class="v3-hero" style="margin-top:12px"><div class="k">ЭКСПРЕСС-РАЗБОР</div><div class="t">Разберём одну проблему до причины</div>' +
      '<div class="s">60 минут с Рустамом и Береке. Уходите с диагнозом и планом на 10 дней.</div>' +
      '<button id="ctaDiagnostic">Записаться на разбор</button></div>';
    h += '<div class="v13-brand"><div class="hero-logo-real" style="width:auto">' + (logo ? '<img class="v3-logo" src="' + logo + '" alt="business surgery">' : '') + '<div class="v3-gold">БИЗНЕС-ТРЕКИНГ</div></div>' +
      '<div class="v3-h1" style="font-size:30px;line-height:1.05;letter-spacing:-1px;margin-top:14px">Бизнес как организм</div>' +
      '<p class="v3-p" style="font-size:14px;max-width:310px;margin-top:10px">Находим причину, а не симптом. Каждые 10 дней: диагноз, стратегия, задачи.</p></div>';
    h += '<div class="v2-card" style="padding:16px;margin-top:14px"><div style="font-size:14px;color:#9A9A9A;line-height:1.45">Акселератор и трекинг-платформа с миссией укрепить здоровье бизнеса и энергию роста.</div>' +
      '<div style="margin-top:12px">' + v3numrow([['700+', 'разборов проведено'], ['20', 'резидентов в клубе'], ['×3', 'средний рост']]) + '</div></div>';
    h += '<div class="v3-quote" style="margin-top:10px">Работаем с причиной на уровне ДНК, а не с симптомами.</div>';
    h += v3socials();
    h += '<div class="v2-card" style="margin-top:10px">' + v3row('msg', 'WhatsApp', '+7 702 403 50 36', "window.open('https://wa.me/77024035036','_blank')") + '</div>';
  } else {
    h += '<div style="margin:22px 4px 0"><div class="hero-logo-real" style="width:auto">' + (logo ? '<img class="v3-logo" src="' + logo + '" alt="business surgery">' : '') + '</div>' +
      '<div class="v3-gold">БИЗНЕС-ТРЕКИНГ</div>' +
      '<h1 class="v3-h1">Хирургия бизнеса</h1>' +
      '<p class="v3-p">Акселератор и трекинг-платформа с миссией укрепить здоровье бизнеса и энергию роста.</p></div>';
    h += '<div style="margin-top:18px">' + v3numrow([['700+', 'разборов проведено'], ['20', 'резидентов в клубе'], ['×3', 'средний рост']]) + '</div>';
    h += '<div class="v3-quote" style="margin-top:10px">Выстраиваем стратегию. Внедряем систему. Трекерим результат.</div>';
    h += '<div class="v11-rules" onclick="showPage(\'rules\')"><div class="k">6 ДОГОВОРЁННОСТЕЙ</div>' +
      '<div class="t">Правила клуба →</div><div class="s">Отчёты, встречи, штрафы и оплата. Как устроена работа в клубе</div></div>';
    h += '<div class="v2-h">Клуб</div><div class="v2-card">' +
      (role === 'resident' ? v3row('board', 'Как пользоваться', 'Разбор, задачи, отчёт за 2 минуты', "showPage('myboard')") : v3row('board', 'Материалы', 'Шаблоны и гайды клуба', "showPage('leadmagnets')")) +
      (role === 'resident' || role === 'admin' ? v3row('club', 'Пригласить предпринимателя', '100\u00a0000 ₸ за приглашённого резидента', "showPage('referral')") : '') +
    '</div>';
    h += v3socials();
    h += '<div class="v2-h">Концепция</div><div class="v2-card">' +
      [['01', 'Мозг', 'Стратегия и видение. Куда идём и зачем'], ['02', 'Руки и ноги', 'Команда и продажи. Что делает бизнес каждый день'], ['03', 'Кровь', 'Финансы и цифры. Что течёт по сосудам бизнеса'], ['04', 'ДНК', 'Процессы и регламенты. На чём всё это держится']]
        .map(function(o){ return '<div class="v2-row" style="cursor:default"><div class="num" style="font-size:11px;color:#9A9A9A;width:18px">' + o[0] + '</div><div class="m"><b>' + o[1] + '</b><span>' + o[2] + '</span></div></div>'; }).join('') +
    '</div>';
    h += '<div class="v3-quote" style="margin-top:10px;font-weight:600;color:#CFCFCF">«Здоровье является состоянием полного физического, душевного и социального благополучия, а не только отсутствием болезней.» Наша миссия</div>';
  }
  el.innerHTML = h;
  var ctaBtn = el.querySelector('#ctaDiagnostic');
  if(ctaBtn){
    ctaBtn.addEventListener('click', function(){
      try{ this.blur(); }catch(blurE){}
      v15book();
    });
  }
}

// ═══ ЛИД: экспресс-разбор, запись и контакты команды ═══
var V4_TEAM = [
  {id:'453800951', name:'Рустам', re:/рустам/i, role:'Трекер · разборы'},
  {id:'1285596249', name:'Береке', re:/берек/i, role:'Трекер · материалы'}
];
function v4writeTeam(i){
  var t = V4_TEAM[i]; if(!t) return;
  var r = (cache.residents || []).filter(function(x){ return t.re.test(String(x.name || '')) && (x.isAdmin || x.isTeam || String(x.chatId || '') === t.id); })[0];
  v11write(Object.assign({}, r || {}, {name: t.name, chatId: (r && r.chatId) || t.id}));
}
// ═══ АДМИН: «ЕЩЁ», всё, что не в сводке ═══
function renderAdminMore(){
  var el = document.getElementById('moreContent');
  if(!el) return;
  var logo = bsLogoSrc();
  var act = v3activeResidents();
  var met = act.reduce(function(s, r){ return s + (Number(r.meetingsDone) || 0); }, 0);
  var closed = ((cache.resTasks && cache.resTasks.tasks) || []).filter(function(t){ return t.status === 'Выполнена'; }).length;
  var leads = (cache.leads || []).length;
  var h = '<div class="v3-headbig"><div class="v3-title xl">Меню</div></div>';
  h += '<div class="v2-card v4-brand" style="margin-top:14px">' + (logo ? '<img src="' + logo + '" alt="business surgery">' : '') +
    '<div style="min-width:0"><div style="font-size:9px;font-weight:800;letter-spacing:2.6px;color:#D4B886">БИЗНЕС-ТРЕКИНГ</div>' +
    '<div class="t" style="margin-top:4px">Здоровье бизнеса и энергия роста</div></div></div>';
  // Те же разделы и порядок, что в левом меню платформы. «↗» открывает раздел платформы в браузере
  var res = act.length, unpaid = (cache.fines || []).filter(function(f){ return f.status === 'Не оплатил'; }).length;
  var libN = (window._lib && window._lib.items) ? window._lib.items.length : 0;
  var S = V11_MENU(leads, res, unpaid, met, closed, libN);
  S.forEach(function(sec){
    h += '<div class="v2-h">' + sec[0] + '</div><div class="v2-card">' + sec[1].map(function(r){
      return r.ext ? v3row(r.icon, r.title, r.sub, "v11plat('" + r.ext + "')", '<div class="ic v11-ext" aria-label="Откроется в браузере">↗</div>')
                   : v3row(r.icon, r.title, r.sub, r.go);
    }).join('') + '</div>';
  });
  h += '<div class="v2-h">Мои замеры</div><div class="v2-card">' +
    v3row('sum', 'Колесо бизнеса', v11wheelSub('dna'), "v11myWheel('dna')") +
    v3row('sum', 'Колесо баланса', v11wheelSub('life'), "v11myWheel('life')") +
  '</div>';
  if(isRealAdmin){
    h += '<div class="v2-h">Режим просмотра</div><div class="v2-card" style="padding-top:14px">' +
      '<div style="font-size:13px;color:#9A9A9A;margin:0 14px 10px">Приложение глазами резидента или лида</div>' +
      '<div class="v4-seg">' + [['admin', 'Админ'], ['resident', 'Резидент'], ['lead', 'Лид']].map(function(x){
        return '<button class="' + (viewAs === x[0] ? 'on' : '') + '" onclick="setRole(\'' + x[0] + '\')">' + x[1] + '</button>';
      }).join('') + '</div></div>';
  }
  h += '<div class="v2-h">Приложение</div><div class="v2-card">' +
    v3row('', 'На главный экран', 'Иконка приложения на телефоне', 'addToHomeScreen()') +
  '</div>';
  el.innerHTML = h;
}

// Меню админа = структура платформы. go: страница приложения; ext: id раздела платформы
function V11_MENU(leads, res, unpaid, met, closed, libN){
  function pg(icon, title, sub, go){ return {icon: icon, title: title, sub: sub, go: go}; }
  function ext(icon, title, sub, id){ return {icon: icon, title: title, sub: sub, ext: id}; }
  return [
    ['Трекинг', [
      ext('board', 'Доска', 'Доска трекера на платформе', 'board'),
      ext('board', 'Доски резидентов', 'Разборы и циклы по каждому', 'boards'),
      pg('tasks', 'Задачи резидентов', (closed ? closed + ' закрыто · ' : '') + 'задания по циклам', "showPage('restasks')"),
      pg('sum', 'Колесо клуба', 'Замеры резидентов', "showPage('wheeladmin')"),
      pg('doc', 'Болезни', 'Диагнозы из библиотеки', "v11lib('diag')"),
      pg('doc', 'Инструменты', 'Инструменты из библиотеки', "v11lib('tool')"),
      ext('board', 'Карта диагнозов', 'Диагнозы по органам', 'diagMap'),
      ext('pen', 'Заметки', 'Заметки трекера', 'notes'),
      ext('doc', 'Стикеры', 'Стикеры на досках', 'stickers'),
      pg('doc', 'Ресурсы', (libN ? libN + ' в библиотеке · ' : '') + 'книги и материалы', "v11lib('all')"),
      ext('me', 'Контакты', 'Полезные контакты', 'contacts')
    ]],
    ['Клуб', [
      pg('cal', 'Посещения', (met ? met + ' встреч проведено · ' : '') + 'кто пришёл', "showPage('visits')"),
      pg('doc', 'Отчёты', 'Отчёты резидентов по дням', "showPage('reports')"),
      pg('alert', 'Штрафы', unpaid ? unpaid + ' не оплачено' : 'Все оплачены', "showPage('fines')"),
      pg('club', 'Пригласить предпринимателя', '100\u00a0000 ₸ за приглашённого резидента', "showPage('referral')"),
      ext('club', 'Пятёрки', 'Группы резидентов', 'sFive'),
      ext('cal', 'Мероприятия', 'События клуба', 'events')
    ]],
    ['Учёт', [
      ext('sum', 'PL и ДДС', 'Прибыль и движение денег', 'pl'),
      ext('sum', 'В цифрах', 'Ключевые показатели', 'aNumbers'),
      ext('sum', 'Анализ', 'Сводный анализ', 'summary'),
      ext('doc', 'Google Таблица', 'Исходная таблица клуба', 'gsheet')
    ]],
    ['Продажи', [
      pg('club', 'CRM лидов', (leads ? leads + ' · ' : '') + 'кто прошёл диагностику и не записался', "showPage('crm')"),
      pg('msg', 'Подписчики бота', 'Рассылки и дожимы', "showPage('subscribers')"),
      pg('board', 'Диагностика', 'Тест здоровья бизнеса', "showPage('diagnostic')"),
      pg('cal', 'Экспресс-разбор', 'Запись лида: окна, оплата Kaspi', "showPage('leadrazbor')"),
      pg('doc', 'Материалы', 'Чек-листы и гайды для лидов', "showPage('leadmagnets')"),
      ext('sum', 'Конверсии', 'Воронка CRM', 'crmFunnel'),
      ext('sum', 'План продаж', 'План и факт', 'salesPlan'),
      ext('doc', 'Скрипты', 'Скрипты продаж', 'scripts'),
      pg('board', 'CustDev', 'Интервью и выводы', "showPage('custdev')"),
      ext('sum', 'NPS', 'Оценки резидентов', 'nps'),
      ext('search', 'Парсер лидов', 'Сбор контактов', 'parser'),
      ext('msg', 'Промпты ИИ', 'Промпты для CRM', 'crmPrompts'),
      pg('cal', 'SMM планер', 'Контент-план и публикации', "showPage('content')"),
      ext('pen', 'Заметки маркетинга', 'Идеи и черновики', 'mNotes')
    ]],
    ['BS', [
      ext('doc', 'Полезное', 'Ссылки и материалы команды', 'useful'),
      pg('book', '99 гайдов', 'Каталог и ридер для лидов и резидентов', "showPage('guides')"),
      pg('tasks', 'Цели и задачи', 'Задачи команды: Рустам и Береке', "showPage('todos')"),
      ext('msg', 'Рекомендации ИИ', 'Подсказки по клубу', 'aiRec'),
      ext('doc', 'Google Doc', 'Документ проекта', 'gdoc'),
      pg('home', 'О проекте', 'Лендинг и правила клуба', "showPage('about')"),
      pg('me', 'Мой профиль', 'Ниша, о себе, контакты', "showPage('myprofile')")
    ]]
  ];
}
function v11plat(id){
  var url = 'https://app.bxclub.kz/#' + id;
  try{ if(tg && tg.openLink){ tg.openLink(url); return; } }catch(e){}
  window.open(url, '_blank');
}
function v11lib(kind){ window._libKind = kind; window._libQ = ''; showPage('library'); }

// ═══ МОИ ЗАМЕРЫ АДМИНА: те же колёса, что у резидента; результаты только на этом устройстве ═══
function v11adminWheel(){ return navRole() === 'admin'; }
function v11wheelStore(){
  var d = null; try{ d = JSON.parse(localStorage.getItem('bs_admin_wheel') || 'null'); }catch(e){}
  if(!d || typeof d !== 'object') d = {};
  if(!Array.isArray(d.businesses)) d.businesses = [];
  if(!Array.isArray(d.life)) d.life = [];
  d.dnaAxes = WHEEL_DNA_AXES; d.lifeAxes = d.lifeAxes || WHEEL_LIFE_AXES;
  return d;
}
function v11wheelSave(type, values, biz){
  var d = v11wheelStore();
  var row = {date: new Date().toLocaleDateString('ru-RU') + ' ' + new Date().toLocaleTimeString('ru-RU', {hour: '2-digit', minute: '2-digit'}), values: values};
  if(type === 'dna'){
    var b = d.businesses.filter(function(x){ return x.name === biz; })[0];
    if(!b){ b = {name: biz, rows: []}; d.businesses.push(b); }
    b.rows.unshift(row); b.rows = b.rows.slice(0, 12);
  } else {
    // Ось «Бизнес» считается сама: среднее последних замеров всех направлений
    var avgs = d.businesses.map(function(b){ return b.rows[0] ? wheelAvg(b.rows[0].values) : null; }).filter(function(x){ return x !== null; });
    row.values = [avgs.length ? Math.round(avgs.reduce(function(a, x){ return a + x; }, 0) / avgs.length * 10) / 10 : 0].concat(values);
    d.life.unshift(row); d.life = d.life.slice(0, 12);
  }
  try{ localStorage.setItem('bs_admin_wheel', JSON.stringify(d)); }catch(e){}
  return d;
}
function v11wheelSub(type){
  var d = v11wheelStore();
  var r = type === 'dna' ? (d.businesses[0] && d.businesses[0].rows[0]) : d.life[0];
  if(!r) return 'Пройти первый замер';
  return 'Замер ' + String(r.date).split(' ')[0] + ' · средний ' + wheelAvg(r.values);
}
function v11myWheel(type){
  showPage('wheel');
  var d = v11wheelStore();
  var has = type === 'dna' ? d.businesses.length : d.life.length;
  if(!has){ openWheelForm(); if(type !== 'dna') switchWheelForm(type); }
}

function openDiagnosticModal(opts){
  opts = opts || {};
  var modal = document.createElement('div');
  modal.className = 'add-menu-overlay show';
  modal.onclick = function(e){ if(e.target===modal) modal.remove(); };
  
  modal.innerHTML = 
    '<div class="add-menu" style="max-height:90vh;overflow-y:auto">' +
      '<div class="add-menu-title">' + v2esc(opts.title || 'Записаться на диагностику') + '</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Имя</label>' +
        '<input type="text" id="diagName" class="field-input" placeholder="Как к тебе обращаться">' +
      '</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Телефон / WhatsApp</label>' +
        '<input type="tel" id="diagPhone" class="field-input" placeholder="+7 ___ ___ __ __">' +
      '</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Чем занимается бизнес</label>' +
        '<input type="text" id="diagNiche" class="field-input" placeholder="Ниша и кратко суть">' +
      '</div>' +
      '<div class="field-group">' +
        '<label class="field-label">Главный запрос</label>' +
        '<textarea id="diagRequest" class="field-input" rows="3" placeholder="Что хочешь узнать на диагностике"></textarea>' +
      '</div>' +
      '<button class="btn btn-primary" id="diagSubmit" style="width:100%;margin-top:8px">Отправить заявку</button>' +
      '<button class="add-menu-cancel" id="diagCancel">Отмена</button>' +
    '</div>';
  
  document.body.appendChild(modal);
  
  modal.querySelector('#diagCancel').addEventListener('click', function(){ modal.remove(); });
  modal.querySelector('#diagSubmit').addEventListener('click', function(){
    var name = modal.querySelector('#diagName').value.trim();
    var phone = modal.querySelector('#diagPhone').value.trim();
    var niche = modal.querySelector('#diagNiche').value.trim();
    var request = modal.querySelector('#diagRequest').value.trim();
    
    if(!name){ showToast('Введи имя'); return; }
    if(!phone){ showToast('Введи телефон'); return; }
    
    modal.remove();
    showToast('Отправляю заявку...');
    callAction('submitDiagnosticRequest', {name:name, phone:phone, niche:niche, request:request}).then(function(r){
      if(r && r.ok){
        showToast('Заявка отправлена! Скоро свяжемся.');
      } else {
        showToast('' + (r && r.error || 'Ошибка'));
      }
    });
  });
  
  try{
    if(opts.request) modal.querySelector('#diagRequest').value = opts.request;
    if(opts.phone) modal.querySelector('#diagPhone').value = opts.phone;
    if(opts.niche) modal.querySelector('#diagNiche').value = opts.niche;
    if(!modal.querySelector('#diagName').value && user && user.first_name) modal.querySelector('#diagName').value = user.first_name;
  }catch(e){}
  setTimeout(function(){
    var inp = modal.querySelector('#diagName');
    if(inp && !inp.value) inp.focus();
  }, 100);
}


function renderLeadmagnets(){
  var el = document.getElementById('page-leadmagnets');
  if(!el) return;
  
  el.innerHTML = `
    <div class="lm-min">
      <div class="lm-min-head">
        <div class="hero-label">Материалы BS</div>
        <h1 class="hero-title">Гайды и подкасты</h1>
        <div class="hero-sub">Реальные кейсы резидентов с цифрами «было / стало». Концентрат того что мы делаем на разборах.</div>
      </div>
      ${v14promo('lm')}
      
      <div class="lm-section">
        <div class="section-min-label" id="lmCountLabel">PDF-материалы</div>
        
        <!-- Поиск -->
        <div class="lm-search-row">
          <input type="text" id="lmSearch" class="lm-search-input" placeholder="Найти материал..."/>
        </div>
        
        <!-- Категории-фильтры (dropdown с явным индикатором) -->
        <div class="lm-cat-wrap">
          <button class="lm-cat-trigger" id="lmCatTrigger" type="button">
            <span class="lm-cat-icon"></span>
            <span class="lm-cat-current" id="lmCatCurrent">Все категории</span>
            <span class="lm-cat-chevron">▾</span>
          </button>
          <div class="lm-cat-menu" id="lmCatMenu" style="display:none;">
            <div class="lm-cat-item active" data-cat="all">Все категории</div>
            <div class="lm-cat-item" data-cat="brain">Стратегия</div>
            <div class="lm-cat-item" data-cat="heart">Маркетинг</div>
            <div class="lm-cat-item" data-cat="hands">Продажи</div>
            <div class="lm-cat-item" data-cat="spine">Команда</div>
            <div class="lm-cat-item" data-cat="blood">Финансы</div>
            <div class="lm-cat-item" data-cat="dna">Процессы</div>
            <div class="lm-cat-item" data-cat="eyes">Аналитика</div>
          </div>
        </div>
        
        <div class="lm-cards-list" id="lmCardsList">
          <div class="loading"><div class="spinner"></div></div>
        </div>
      </div>
      
      <div class="lm-section">
        <div class="section-min-label">Подкасты</div>
        <h2 class="section-min-title">Разборы и интервью</h2>
        <a href="https://www.youtube.com/@Businessurgery" target="_blank" class="yt-card">
          <div class="yt-card-thumb">
            <div class="yt-thumb-grid">
              <div class="yt-thumb-block one"></div>
              <div class="yt-thumb-block two"></div>
              <div class="yt-thumb-block three"></div>
              <div class="yt-thumb-block four"></div>
            </div>
            <div class="yt-thumb-content">
              <div class="yt-thumb-tag">Подкаст · Эпизод 02</div>
              <div class="yt-thumb-title-overlay">Правильный<br>найм дал ×4<br>чистой прибыли</div>
            </div>
            <div class="yt-thumb-play">
              <svg viewBox="0 0 24 24" fill="#fff"><polygon points="8,5 19,12 8,19"/></svg>
            </div>
            <div class="yt-thumb-duration">54:21</div>
          </div>
          <div class="yt-card-meta">
            <div class="yt-card-avatar">
              <svg viewBox="0 0 60 60" fill="none">
                <rect x="2" y="2" width="56" height="56" rx="14" fill="#FFFFFF"/>
                <text x="30" y="40" text-anchor="middle" font-family="Manrope" font-size="24" font-weight="800" fill="#000">bs</text>
              </svg>
            </div>
            <div class="yt-card-info">
              <div class="yt-card-channel">Business Surgery</div>
              <div class="yt-card-handle">@Businessurgery</div>
            </div>
            <div class="yt-card-subscribe">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><path d="M23.498 6.186a3.016 3.016 0 0 0-2.122-2.136C19.505 3.545 12 3.545 12 3.545s-7.505 0-9.377.505A3.017 3.017 0 0 0 .502 6.186C0 8.07 0 12 0 12s0 3.93.502 5.814a3.016 3.016 0 0 0 2.122 2.136c1.871.505 9.376.505 9.376.505s7.505 0 9.377-.505a3.015 3.015 0 0 0 2.122-2.136C24 15.93 24 12 24 12s0-3.93-.502-5.814zM9.545 15.568V8.432L15.818 12l-6.273 3.568z"/></svg>
              Открыть канал
            </div>
          </div>
        </a>
      </div>
    </div>
  `;
  
  (function(){
    var r = cache.leadmagnets || {};
    var listEl = document.getElementById('lmCardsList');
    if(!listEl) return;
    if(!r || !r.items){
      listEl.innerHTML = '<div class="empty"><div class="em-text">Материалы появятся после обновления</div></div>';
      return;
    }
    var items = r.items.filter(function(x){ return x.fileId; });
    if(items.length === 0){
      listEl.innerHTML = '<div class="empty"><div class="em-icon"></div><div class="em-title">Скоро будут доступны</div></div>';
      return;
    }
    
    var emojiMap = {sales:'', unit:'', delegate:'', hire:'', marketing:''};
    var colorMap = {sales:'#FF6B6F', unit:'#FFFFFF', delegate:'#9A9A9A', hire:'#FFFFFF', marketing:'#9A9A9A'};
    var descMap = {
      sales: {hero:'Дамир, маркетинговое агентство', metric:'6 → 14 млн ₸/мес'},
      unit: {hero:'Айдар, барбершоп', metric:'2.3 → 4.2 млн ₸/мес'},
      delegate: {hero:'Бауржан, строительство', metric:'Работает 7 часов в день'},
      hire: {hero:'Жанна, ресторан', metric:'Текучка 120% → 35%'},
      marketing: {hero:'Айгуль, онлайн-школа', metric:'200К → 4 млн ₸ без рекламы'}
    };
    
    // Получим статус "забрал ли уже" из r.taken (или пусто)
    var taken = r.taken || {};
    
    // Сохраняем все элементы для фильтрации
    window._allLeadmagnets = items;
    window._lmFilter = window._lmFilter || 'all';
    window._lmSearch = window._lmSearch || '';
    
    // Привязка dropdown категорий (один раз)
    var triggerEl = document.getElementById('lmCatTrigger');
    var menuEl = document.getElementById('lmCatMenu');
    var wrapEl = triggerEl && triggerEl.parentElement;
    if(triggerEl && menuEl && !triggerEl.getAttribute('data-bound')){
      triggerEl.setAttribute('data-bound','1');
      triggerEl.addEventListener('click', function(e){
        e.stopPropagation();
        var isOpen = menuEl.style.display === 'block';
        menuEl.style.display = isOpen ? 'none' : 'block';
        if(wrapEl) wrapEl.classList.toggle('open', !isOpen);
      });
      // Закрытие по клику вне меню
      document.addEventListener('click', function(){
        menuEl.style.display = 'none';
        if(wrapEl) wrapEl.classList.remove('open');
      });
      menuEl.querySelectorAll('.lm-cat-item').forEach(function(it){
        it.addEventListener('click', function(e){
          e.stopPropagation();
          menuEl.querySelectorAll('.lm-cat-item').forEach(function(i){ i.classList.remove('active'); });
          it.classList.add('active');
          window._lmFilter = it.getAttribute('data-cat');
          // Обновим текст в кнопке
          var currentEl = document.getElementById('lmCatCurrent');
          if(currentEl) currentEl.textContent = it.textContent.replace(/^[^а-яА-Яa-zA-Z]+/, '').trim() || it.textContent;
          // Закроем меню
          menuEl.style.display = 'none';
          if(wrapEl) wrapEl.classList.remove('open');
          _redrawLeadmagnets();
        });
      });
    }
    var searchEl = document.getElementById('lmSearch');
    if(searchEl && !searchEl.getAttribute('data-bound')){
      searchEl.setAttribute('data-bound','1');
      searchEl.addEventListener('input', function(){
        window._lmSearch = String(searchEl.value || '').toLowerCase().trim();
        _redrawLeadmagnets();
      });
    }
    
    function _redrawLeadmagnets(){
      var all = window._allLeadmagnets || [];
      var cat = window._lmFilter || 'all';
      var q = window._lmSearch || '';
      var filtered = all.filter(function(it){
        // По категории (берём из item.category, если задан)
        if(cat !== 'all'){
          var itemCat = String(it.category || '').toLowerCase();
          if(itemCat !== cat) return false;
        }
        // По поиску
        if(q){
          var hay = ((it.title||'') + ' ' + (it.key||'') + ' ' + (it.description||'')).toLowerCase();
          if(hay.indexOf(q) < 0) return false;
        }
        return true;
      });
      
      // Обновим счётчик
      var lbl = document.getElementById('lmCountLabel');
      if(lbl) lbl.textContent = filtered.length + ' ' + v3plural(filtered.length, 'материал', 'материала', 'материалов');
      
      if(!filtered.length){
        listEl.innerHTML = '<div class="lm-empty-result">Ничего не найдено</div>';
        return;
      }
      _renderLmList(filtered);
    }
    
    function _renderLmList(items){
      listEl.innerHTML = items.map(function(item){
      var emoji = emojiMap[item.key] || '';
      var color = colorMap[item.key] || '#141414';
      var d = descMap[item.key] || {hero:'Кейс резидента', metric:'Реальные цифры'};
      var isTaken = !!taken[item.key];
      var badge = isTaken 
        ? '<div class="lm-min-badge taken">✓ Забрал</div>' 
        : '<div class="lm-min-badge new">Новый</div>';
      var btnText = isTaken ? 'Получить ещё раз' : 'Получить в Telegram';
      
      var adminBtn = (isRealAdmin && viewAs === 'admin') 
        ? '<button class="lm-min-edit" data-key="' + item.key + '" title="Редактировать">Редактировать</button>'
        : '';
      return '<div class="lm-min-card">' +
        '<div class="lm-min-card-head">' +
          '<div class="lm-min-icon" style="background:' + color + '">' + emoji + '</div>' +
          badge +
        '</div>' +
        '<div class="lm-min-title">' + item.title + '</div>' +
        '<div class="lm-min-hero">' + d.hero + '</div>' +
        '<div class="lm-min-metric">' + d.metric + '</div>' +
        '<button class="lm-min-btn" data-key="' + item.key + '">' + btnText + ' →</button>' +
        adminBtn +
      '</div>';
    }).join('');
    
    listEl.querySelectorAll('.lm-min-btn').forEach(function(btn){
      btn.addEventListener('click', function(e){
        e.stopPropagation();
        try{ this.blur(); }catch(blurE){}
        var key = btn.getAttribute('data-key');
        requestLeadmagnet(key);
      });
    });
    listEl.querySelectorAll('.lm-min-edit').forEach(function(btn){
      btn.addEventListener('click', function(e){
        e.stopPropagation();
        try{ this.blur(); }catch(blurE){}
        var key = btn.getAttribute('data-key');
        var item = (window._allLeadmagnets || []).filter(function(it){return it.key===key;})[0];
        if(item) _openEditLeadmagnet(item);
      });
    });
    }
    
    // Первичный рендер
    _redrawLeadmagnets();
  })();
}

function requestLeadmagnet(key){
  // Найдём кнопку и покажем что отправляем
  var btn = document.querySelector('.lm-min-btn[data-key="'+key+'"]');
  if(btn){
    btn.textContent = 'Отправляем...';
    btn.disabled = true;
  }
  callAction('requestLeadmagnet', {key:key}).then(function(r){
    if(r && r.ok){
      _showLeadmagnetSentModal(key, r.fileName || 'Чек-лист');
      setTimeout(function(){ renderLeadmagnets(); }, 500);
    } else {
      if(btn){ btn.disabled = false; btn.textContent = 'Получить →'; }
      showToast('' + (r && r.error || 'Не удалось'));
    }
  });
}

function _showLeadmagnetSentModal(key, fileName){
  // Большой явный modal: бот отправил PDF в Telegram
  var existing = document.getElementById('lmSentModal');
  if(existing) existing.remove();
  
  var modal = document.createElement('div');
  modal.id = 'lmSentModal';
  modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.92);z-index:9999;display:flex;align-items:center;justify-content:center;padding:24px;backdrop-filter:blur(8px);';
  
  modal.innerHTML = 
    '<div style="background:linear-gradient(135deg,#1a1a1a,#0a0a0a);border:2px solid rgba(255,255,255,0.4);border-radius:24px;padding:36px 28px;max-width:380px;width:100%;text-align:center;">' +
      '<div style="font-size:64px;margin-bottom:18px;"></div>' +
      '<div style="font-size:22px;font-weight:800;color:#fff;margin-bottom:10px;">Чек-лист отправлен</div>' +
      '<div style="font-size:15px;color:rgba(255,255,255,0.7);line-height:1.5;margin-bottom:24px;">Бот прислал тебе PDF файл прямо в чат Telegram. Открой чат с ботом чтобы посмотреть.</div>' +
      '<button id="lmOpenBot" style="width:100%;padding:16px 20px;background:#FFFFFF;color:#000;border:none;border-radius:14px;font-size:16px;font-weight:800;cursor:pointer;margin-bottom:10px;">Открыть чат с ботом →</button>' +
      '<button id="lmStay" style="width:100%;padding:14px 20px;background:transparent;color:rgba(255,255,255,0.6);border:1px solid rgba(255,255,255,0.15);border-radius:14px;font-size:14px;font-weight:600;cursor:pointer;">Остаться здесь</button>' +
    '</div>';
  
  document.body.appendChild(modal);
  
  document.getElementById('lmOpenBot').addEventListener('click', function(){
    if(window.Telegram && Telegram.WebApp){ Telegram.WebApp.close(); }
  });
  document.getElementById('lmStay').addEventListener('click', function(){
    modal.remove();
  });
}



// === ДИАГНОСТИКА БИЗНЕСА ===
const DIAGNOSTIC_AREAS = [
  {
    id: 'brain',
    organ: 'Мозг',
    label: 'Стратегия',
    icon: '',
    color: '#9A9A9A',
    questions: [
      { q: 'Я могу одним предложением сформулировать главную цель бизнеса на ближайший год' },
      { q: 'Решения о крупных вложениях я принимаю на основе цифр и расчётов, не интуиции' },
      { q: 'У меня есть письменный план развития бизнеса с конкретными датами' },
      { q: 'Каждую неделю я проверяю движение бизнеса относительно годового плана' },
      { q: 'Я знаю чем мой бизнес отличается от конкурентов и почему клиенты выбирают меня' },
      { q: 'У меня есть стратегия развития на 3-5 лет вперёд, не только на год' },
      { q: 'Я могу назвать топ-3 конкурентов и их сильные и слабые стороны' },
      { q: 'Стратегические решения я обсуждаю с партнёром, ментором или трекером' },
      { q: 'Я понимаю какой бизнес я хочу через 3 года и зачем мне это лично' }
    ]
  },
  {
    id: 'heart',
    organ: 'Сердце',
    label: 'Маркетинг',
    icon: '',
    color: '#9A9A9A',
    questions: [
      { q: 'Я знаю стоимость привлечения одного клиента (CAC) с точностью до тысячи тенге' },
      { q: 'У моего бизнеса работают минимум 3 канала привлечения клиентов одновременно' },
      { q: 'Я знаю портрет идеального клиента до уровня его болей, страхов и желаний' },
      { q: 'Я веду личный бренд в соцсетях и публикую контент минимум раз в неделю' },
      { q: 'Моя воронка прогрева работает на автомате, без моего участия' },
      { q: 'Я знаю LTV клиента и сколько он приносит за всё время жизни в бизнесе' },
      { q: 'У моего бренда есть чёткое уникальное обещание которое отличает нас от рынка' },
      { q: 'Я отслеживаю окупаемость каждого маркетингового канала по факту' },
      { q: 'У меня есть контент-план на месяц вперёд и я ему следую' }
    ]
  },
  {
    id: 'hands',
    organ: 'Руки и ноги',
    label: 'Продажи',
    icon: '',
    color: '#FF6B6F',
    questions: [
      { q: 'Каждая заявка от клиента попадает в CRM и не теряется' },
      { q: 'Я знаю конверсию воронки на каждом этапе (заявка, встреча, оплата)' },
      { q: 'Менеджеры работают по прописанным скриптам и я проверяю их звонки' },
      { q: 'Среднее время ответа на заявку клиента у меня меньше 15 минут' },
      { q: 'В моём бизнесе продают другие люди, не только я лично' },
      { q: 'У меня есть РОП который отвечает за выполнение плана продаж' },
      { q: 'Менеджеры обучены работе с возражениями и могут продать дорогой продукт' },
      { q: 'У меня прописана система мотивации продавцов привязанная к KPI' },
      { q: 'Я знаю где главная утечка в воронке и работаю над её устранением' }
    ]
  },
  {
    id: 'spine',
    organ: 'Костяк',
    label: 'Команда',
    icon: '',
    color: '#FFFFFF',
    questions: [
      { q: 'У каждого сотрудника прописаны должностные обязанности и KPI' },
      { q: 'Я могу уехать на месяц и бизнес продолжит работать без потерь' },
      { q: 'У меня есть прозрачная система найма: где искать, как фильтровать, как адаптировать' },
      { q: 'Лучшие сотрудники получают зарплату выше рынка и я не боюсь их потерять' },
      { q: 'Раз в квартал я провожу один на один с каждым ключевым сотрудником' },
      { q: 'У меня прописана организационная структура с зонами ответственности' },
      { q: 'В команде есть второй человек который может заменить меня на 80% задач' },
      { q: 'Я знаю стоимость часа каждого сотрудника и его маржинальность для бизнеса' },
      { q: 'Адаптация нового сотрудника проходит по чёткому регламенту за 30 дней' }
    ]
  },
  {
    id: 'blood',
    organ: 'Кровь',
    label: 'Финансы',
    icon: '',
    color: '#FF6B6F',
    questions: [
      { q: 'Я закрываю ОПиУ до 5 числа каждого месяца и знаю чистую прибыль точно' },
      { q: 'Я веду ДДС с прогнозом денежного потока на 30 дней вперёд' },
      { q: 'Я знаю маржинальность по каждому продукту или услуге отдельно' },
      { q: 'Я получаю фиксированную зарплату из бизнеса и не беру деньги хаотично' },
      { q: 'Я знаю точку безубыточности своего бизнеса в тенге выручки' },
      { q: 'У меня есть резервный фонд бизнеса на 3 месяца операционных расходов' },
      { q: 'Я знаю стоимость капитала и сравниваю прибыль с альтернативными инвестициями' },
      { q: 'Налоги планируются заранее и я не сталкиваюсь с кассовыми разрывами из-за них' },
      { q: 'У бизнеса есть финансовая модель на год с проверенными гипотезами роста' }
    ]
  },
  {
    id: 'dna',
    organ: 'ДНК',
    label: 'Процессы',
    icon: '',
    color: '#9A9A9A',
    questions: [
      { q: 'Ключевые процессы в бизнесе описаны регламентом и доступны команде' },
      { q: 'Я трачу меньше 2 часов в день на операционные вопросы' },
      { q: 'Рутинные задачи автоматизированы (отчёты, рассылки, напоминания)' },
      { q: 'В этом месяце я внедрил минимум одно улучшение в процессах' },
      { q: 'У меня есть человек который отвечает за операционную эффективность' },
      { q: 'Качество исполнения регламентов регулярно проверяется и оценивается' },
      { q: 'Я знаю время выполнения каждого ключевого процесса от начала до конца' },
      { q: 'IT-инфраструктура бизнеса работает стабильно и не отвлекает меня' },
      { q: 'Я регулярно отказываюсь от ненужных процессов и упрощаю работу команды' }
    ]
  },
  {
    id: 'eyes',
    organ: 'Зрение',
    label: 'Аналитика',
    icon: '',
    color: '#FFFFFF',
    questions: [
      { q: 'У меня есть дашборд с ключевыми метриками который обновляется автоматически' },
      { q: 'Прямо сейчас я могу назвать выручку, прибыль и средний чек прошлого месяца' },
      { q: 'Все мои решения о найме и инвестициях опираются на расчёт окупаемости' },
      { q: 'Я вижу путь клиента от первого касания до оплаты в одном месте' },
      { q: 'Каждую неделю я провожу разбор результатов с командой по цифрам' },
      { q: 'Я отслеживаю не только финансовые но и операционные метрики бизнеса' },
      { q: 'У меня есть прогнозная модель которая предсказывает результаты на месяц вперёд' },
      { q: 'Команда видит свои KPI в реальном времени и сама за них отвечает' },
      { q: 'Я знаю топ-5 метрик от которых зависит здоровье моего бизнеса' }
    ]
  }
];



// 5 вариантов ответа в стиле Likert scale (Gallup/Oxford методология)
const ANSWER_SCALE = [
  { score: 4, label: 'Полностью согласен', short: 'Так и есть', color: '#FFFFFF', emoji: '●' },
  { score: 3, label: 'Скорее согласен', short: 'Скорее да', color: '#BDBDBD', emoji: '●' },
  { score: 2, label: 'Затрудняюсь ответить', short: 'Не уверен', color: '#7A7A7A', emoji: '●' },
  { score: 1, label: 'Скорее не согласен', short: 'Скорее нет', color: '#4A4A4A', emoji: '●' },
  { score: 0, label: 'Полностью не согласен', short: 'Это не про меня', color: '#2A2A2A', emoji: '●' }
];

let diagState = {
  started: false,
  finished: false,
  currentArea: 0,
  currentQuestion: 0,
  answers: {} // {areaId: [3,1,0,3,1]}. баллы по каждому вопросу
};

function renderDiagnostic(){
  var el = document.getElementById('page-diagnostic');
  if(!el) return;
  try{ diagState; }catch(e){ return; }   // первый ранний вызов: состояние объявлено ниже по скрипту
  
  // Если есть сохранённый результат. восстановим
  if(!diagState.started && !diagState.finished){
    try{
      var savedResult = localStorage.getItem('bs_diagnostic_result');
      if(savedResult){
        var data = JSON.parse(savedResult);
        // Восстанавливаем answers из results
        if(data.results && Array.isArray(data.results)){
          diagState.answers = {};
          DIAGNOSTIC_AREAS.forEach(function(area){
            var resForArea = data.results.find(function(r){ return r.id === area.id; });
            if(resForArea && resForArea.score !== undefined){
              // Симулируем answers: одно значение = score / N
              var perQ = resForArea.score / area.questions.length;
              diagState.answers[area.id] = area.questions.map(function(){ return perQ; });
            }
          });
          diagState.finished = true;
          diagState.started = true;
        }
      }
    }catch(e){console.error('restore diag:', e);}
  }
  
  if(!diagState.started && !diagState.finished){
    // Стартовый экран
    el.innerHTML = `
      <div class="diag-page">
        <div class="diag-hero">
          <div class="hero-label">Диагностика бизнеса</div>
          <h1 class="hero-title">Узнай слабые<br>органы за 8 минут</h1>
          <div class="hero-sub">Точный замер по 7 органам бизнеса. В результате: главная проблема, сильная сторона и что сделать уже на этой неделе.</div>
        </div>
        
        <div class="diag-orgs-preview">
          <div class="diag-org-preview"><span>${v9organIcon('brain', 18)}</span><span>Стратегия</span></div>
          <div class="diag-org-preview"><span>${v9organIcon('heart', 18)}</span><span>Маркетинг</span></div>
          <div class="diag-org-preview"><span>${v9organIcon('hands', 18)}</span><span>Продажи</span></div>
          <div class="diag-org-preview"><span>${v9organIcon('spine', 18)}</span><span>Команда</span></div>
          <div class="diag-org-preview"><span>${v9organIcon('blood', 18)}</span><span>Финансы</span></div>
          <div class="diag-org-preview"><span>${v9organIcon('dna', 18)}</span><span>Процессы</span></div>
          <div class="diag-org-preview"><span>${v9organIcon('eyes', 18)}</span><span>Аналитика</span></div>
        </div>
        
        <div class="diag-meta">
          <div class="diag-meta-item">
            <div class="diag-meta-num">${DIAGNOSTIC_AREAS.reduce((s,a)=>s+a.questions.length,0)}</div>
            <div class="diag-meta-label">вопросов всего</div>
          </div>
          <div class="diag-meta-item">
            <div class="diag-meta-num">7</div>
            <div class="diag-meta-label">областей бизнеса</div>
          </div>
          <div class="diag-meta-item">
            <div class="diag-meta-num">8</div>
            <div class="diag-meta-label">минут пройти</div>
          </div>
        </div>
        
        <button class="cta-button" id="diagStartBtn">
          <div class="cta-button-icon">
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="9 18 15 12 9 6"/></svg>
          </div>
          <div class="cta-button-text">
            <div class="cta-button-title">Начать диагностику</div>
            <div class="cta-button-sub">7 областей бизнеса. 9 вопросов на каждую</div>
          </div>
          <div class="cta-button-arrow">→</div>
        </button>
        
        <div class="diag-note">
          <div class="diag-note-label">Что получишь в результате</div>
          <div class="diag-note-text">Индекс здоровья из 100, баллы по каждому органу, разбор главной проблемы, связи органов и шаги на неделю. Точные инструменты под ваш бизнес подбираем на разборе.</div>
        </div>
      </div>
    `;
    
    var btn = el.querySelector('#diagStartBtn');
    if(btn){
      btn.addEventListener('click', function(){
        try{ this.blur(); }catch(e){}
        diagState.started = true;
        diagState.currentArea = 0;
        diagState.currentQuestion = 0;
        diagState.answers = {};
        DIAGNOSTIC_AREAS.forEach(function(area){
          diagState.answers[area.id] = new Array(area.questions.length).fill(null);
        });
        renderDiagnostic();
      });
    }
  } else if(diagState.started && !diagState.finished){
    // Экран вопроса
    var area = DIAGNOSTIC_AREAS[diagState.currentArea];
    var question = area.questions[diagState.currentQuestion];
    var totalInArea = area.questions.length;
    var currentNum = diagState.currentQuestion + 1;
    
    el.innerHTML = `
      <div class="diag-page">
        <div class="diag-question-head">
          <div class="diag-area-tag" style="background:transparent;border:1px solid #2A2A2A;color:#FFFFFF">
            ${v9organIcon(area.id, 18)}
            ${area.organ} · ${area.label}
          </div>
          <div class="diag-progress">${currentNum} / ${totalInArea}</div>
        </div>
        
        <div class="diag-progress-bar">
          <div class="diag-progress-fill" style="width:${(currentNum/totalInArea)*100}%;background:#FFFFFF"></div>
        </div>
        
        <div class="diag-question">${question.q}</div>
        
        <div class="diag-answers">
          ${ANSWER_SCALE.map(function(opt){
            return '<button class="diag-answer" data-score="' + opt.score + '">' +
              '<div class="diag-answer-dot" style="background:' + opt.color + '"></div>' +
              '<div class="diag-answer-text">' + opt.label + '</div>' +
            '</button>';
          }).join('')}
        </div>
        
        ${diagState.currentQuestion > 0 ? '<button class="diag-back" id="diagBackBtn">← Предыдущий вопрос</button>' : ''}
      </div>
    `;
    
    el.querySelectorAll('.diag-answer').forEach(function(btn){
      btn.addEventListener('click', function(){
        try{ this.blur(); }catch(e){}
        var score = parseInt(btn.getAttribute('data-score'));
        diagState.answers[area.id][diagState.currentQuestion] = score;
        
        // Следующий вопрос или следующая область
        if(diagState.currentQuestion < area.questions.length - 1){
          diagState.currentQuestion++;
          renderDiagnostic();
        } else if(diagState.currentArea < DIAGNOSTIC_AREAS.length - 1){
          diagState.currentArea++;
          diagState.currentQuestion = 0;
          renderDiagnostic();
        } else {
          // Конец. финал
          diagState.finished = true;
          finishDiagnostic();
        }
      });
    });
    
    var backBtn = el.querySelector('#diagBackBtn');
    if(backBtn){
      backBtn.addEventListener('click', function(){
        if(diagState.currentQuestion > 0){
          diagState.currentQuestion--;
          renderDiagnostic();
        }
      });
    }
  } else {
    // Финал. показываем результаты
    renderDiagnosticResult();
  }
}

function finishDiagnostic(){
  // Отправляем результат на сервер
  showToast('Сохраняю результаты...');
  callAction('saveDiagnosticResult', {
    answers: JSON.stringify(diagState.answers)
  }).then(function(r){
    renderDiagnostic(); v9scrollTop(); v10afterDiag();
  }).catch(function(e){
    console.error(e);
    renderDiagnostic(); v9scrollTop(); v10afterDiag();
  });
}

// Резидент прошёл диагностику: результат сразу на платформу (здоровье бизнеса и диагностика)
function v10afterDiag(){
  if(navRole() !== 'resident') return;
  var t = v10diagTests(); if(!t) return;
  v10postTests(t).then(function(ok){
    if(!ok) return;
    window._v10diagSaved = 1;
    var b = document.getElementById('diagSaveBtn'); if(b) b.textContent = 'Сохранено в разбор';
  });
}
function requestLeadPhone(){
  // Telegram сам подставит номер из профиля: человеку достаточно нажать одну кнопку
  if(!user || !user.id){ showToast('Откройте приложение через бота'); return; }
  showToast('Отправляем запрос в чат...');
  callAction('requestPhone', {
    chatId: String(user.id),
    reason: 'Чтобы разобрать результат вашей диагностики, нам нужен номер телефона.\n\nНажмите кнопку ниже. номер подставится автоматически из профиля Telegram, вводить ничего не нужно.'
  }).then(function(r){
    if(r && r.ok){
      var btn = document.getElementById('leadPhoneBtn');
      if(btn){
        btn.textContent = 'Кнопка в чате с ботом ↓';
        btn.disabled = true;
        btn.style.opacity = '0.6';
      }
      showToast('Откройте чат с ботом и нажмите кнопку');
      try{ if(tg && tg.close) setTimeout(function(){ tg.close(); }, 1200); }catch(e){}
    } else {
      showToast('Не удалось. Напишите нам в чат');
    }
  });
}

var V4_ORGAN_ICONS = {};
function renderBodyAnatomy(results, weakIds){
  weakIds = weakIds || {};
  // Органы бизнеса: кольцо прогресса вокруг иконки, слабые красным
  var organHtml = results.map(function(r, idx){
    var bad = !!weakIds[r.id] || r.pct < 40;
    var color = bad ? '#FF6B6F' : '#FFFFFF';
    var circumference = 2 * Math.PI * 36;
    var dashOffset = circumference * (1 - r.pct/100);
    return '<div class="anatomy-card" data-organ="' + r.id + '">' +
        '<div class="anatomy-progress">' +
          '<svg viewBox="0 0 84 84" class="anatomy-svg">' +
            '<circle cx="42" cy="42" r="36" stroke="#1F1F1F" stroke-width="4" fill="none"/>' +
            '<circle cx="42" cy="42" r="36" stroke="' + color + '" stroke-width="4" fill="none" stroke-dasharray="' + circumference + '" stroke-dashoffset="' + dashOffset + '" stroke-linecap="round" transform="rotate(-90 42 42)" style="transition: stroke-dashoffset 0.8s ease ' + (idx * 0.1) + 's"/>' +
          '</svg>' +
          '<div class="anatomy-icon" style="color:' + color + '">' + v9organIcon(r.id, 26, 1.8) + '</div>' +
        '</div>' +
        '<div class="anatomy-pct num' + (bad ? ' red' : '') + '">' + r.pct + '</div>' +
        '<div class="anatomy-name">' + v2esc(r.organ) + '</div>' +
        '<div class="anatomy-label">' + v2esc(r.label) + '</div>' +
      '</div>';
  }).join('');
  return '<div class="anatomy-grid">' + organHtml + '</div>';
}


// ═══ ДИАГНОСТИКА v9: иконки органов и глубокий разбор результата ═══
// Единый набор линейных иконок: сетка 24, линия 1.8, скруглённые концы
var V9_ORGAN_ICONS = {
  brain:'<path d="M12 5.5a3 3 0 0 0-5.6-1.2A3 3 0 0 0 4 8a3 3 0 0 0-.6 5.2A3.2 3.2 0 0 0 6.5 18a3 3 0 0 0 5.5 1.2z"/><path d="M12 5.5a3 3 0 0 1 5.6-1.2A3 3 0 0 1 20 8a3 3 0 0 1 .6 5.2 3.2 3.2 0 0 1-3.1 4.8 3 3 0 0 1-5.5 1.2z"/><path d="M12 5.5v13.7"/><path d="M8.5 9.5a2 2 0 0 1 2 2M15.5 9.5a2 2 0 0 0-2 2M7.5 14.5a2.2 2.2 0 0 0 2.3-1M16.5 14.5a2.2 2.2 0 0 1-2.3-1"/>',
  heart:'<path d="M12 20.5s-8-4.9-8-10.8A4.6 4.6 0 0 1 12 6.6a4.6 4.6 0 0 1 8 3.1c0 5.9-8 10.8-8 10.8z"/><path d="M6.5 12.5h3l1.5-2.5 2 4.5 1.5-2h3"/>',
  hands:'<path d="M7 11.5V5.8a1.4 1.4 0 0 1 2.8 0v5"/><path d="M9.8 10.5V4.4a1.4 1.4 0 0 1 2.8 0v6.1"/><path d="M12.6 10.6V5.4a1.4 1.4 0 0 1 2.8 0v6"/><path d="M15.4 11V8a1.4 1.4 0 0 1 2.8 0v6a7 7 0 0 1-7 7h-.6a6 6 0 0 1-4.9-2.6l-2.3-3.5a1.5 1.5 0 0 1 2.4-1.8L7 15"/>',
  spine:'<rect x="8.5" y="2.5" width="7" height="3.5" rx="1.5"/><rect x="7.5" y="8" width="9" height="3.5" rx="1.5"/><rect x="7.5" y="13.5" width="9" height="3.5" rx="1.5"/><rect x="8.5" y="19" width="7" height="2.5" rx="1.2"/><path d="M12 6v2M12 11.5v2M12 17v2"/>',
  blood:'<path d="M12 3s6.5 7 6.5 11.5a6.5 6.5 0 0 1-13 0C5.5 10 12 3 12 3z"/><path d="M9 15a3 3 0 0 0 3 3"/>',
  dna:'<path d="M7 2.5c0 4.8 10 5.2 10 9.5s-10 4.7-10 9.5"/><path d="M17 2.5c0 4.8-10 5.2-10 9.5s10 4.7 10 9.5"/><path d="M8.6 6h6.8M10.2 9h3.6M10.2 15h3.6M8.6 18h6.8"/>',
  eyes:'<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3.2"/><circle cx="13.2" cy="10.8" r=".6" fill="currentColor"/>'
};
function v9organIcon(id, size, w){
  return '<svg width="' + (size || 24) + '" height="' + (size || 24) + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="' + (w || 1.8) + '" stroke-linecap="round" stroke-linejoin="round">' + (V9_ORGAN_ICONS[id] || '') + '</svg>';
}
// Старое имя: инфографика результата берёт иконки отсюда
V4_ORGAN_ICONS = V9_ORGAN_ICONS;

// Порог «слабого» органа и уровни
var V9_WEAK = 60;
function v9level(p){ return p < 40 ? 'low' : (p < 70 ? 'mid' : 'high'); }

// Тексты по органам: состояние на трёх уровнях, симптомы, цена, как использовать силу,
// что сделать на этой неделе и инструменты разбора
var V9_DIAG = {
  brain: {
    state: {
      low: 'Бизнес живёт без цели. Решения принимаются по ситуации, и каждый месяц начинается заново.',
      mid: 'Цель есть, но она в голове. Команда её не видит, а план не сверяется с фактом.',
      high: 'Вы понимаете, куда идёт бизнес, и решения подчинены цели.'
    },
    symptoms: ['Беретесь за всё, что приносит деньги прямо сейчас', 'Не можете за минуту объяснить, каким будет бизнес через год', 'Новые идеи постоянно сбивают с текущего плана'],
    cost: 'Ресурсы распыляются на 5 направлений сразу. Бизнес растёт медленнее рынка, хотя вы работаете больше всех.',
    use: 'Ясная цель помогает быстро говорить «нет» лишнему. Сделайте её видимой для команды: так она начнёт работать без вас.',
    week: ['Запишите одной фразой, какой результат в цифрах нужен через 12 месяцев', 'Выпишите все текущие направления и уберите одно, которое не ведёт к цели', 'Назначьте 30 минут каждую пятницу на сверку недели с целью'],
    tools: [['Точка Б на год', 'Цель в цифрах, от которой считаются все решения'], ['Фокус на одной нише', 'Один клиент, один продукт, один канал на старте'], ['План на 90 дней', 'Цель года, разбитая на 3 месяца и недельные шаги']]
  },
  heart: {
    state: {
      low: 'Клиенты приходят случайно: по сарафану или когда вы лично активны. Поток нельзя включить по желанию.',
      mid: 'Каналы есть, но вы не знаете, какой из них окупается, и поток скачет от месяца к месяцу.',
      high: 'Маркетинг даёт предсказуемый поток заявок, и вы понимаете его экономику.'
    },
    symptoms: ['Заявки идут волнами: месяц густо, месяц пусто', 'Не знаете, сколько стоит привести одного клиента', 'Клиенты сравнивают вас с конкурентами только по цене'],
    cost: 'Без своего потока вы зависите от сезона и сарафана. Каждый тихий месяц съедает прибыль прошлых.',
    use: 'Сильный поток заявок позволяет поднять цены и выбирать клиентов. Проверьте, не теряет ли их отдел продаж.',
    week: ['Опросите 5 последних клиентов: почему выбрали вас, а не других', 'Соберите оффер на одну страницу: для кого, какой результат, почему вы', 'Посчитайте, сколько заявок дал каждый канал за прошлый месяц'],
    tools: [['Оффер на одну страницу', 'Кому, какой результат и почему именно вы'], ['Контент-план', 'Регулярные публикации, которые приводят заявки'], ['Сарафан', 'Система рекомендаций от довольных клиентов']]
  },
  hands: {
    state: {
      low: 'Продаёте вы сами. Без вас заявки остывают, а выручка держится на вашем личном времени.',
      mid: 'Продажи идут, но по-разному у разных людей. Никто не знает, где теряется больше всего клиентов.',
      high: 'Продажи работают по системе и не зависят от одного человека.'
    },
    symptoms: ['Заявки теряются в мессенджерах и тетрадях', 'Менеджеры продают по-разному, результат не повторяется', 'Клиенту отвечают через час или на следующий день'],
    cost: 'Каждая потерянная заявка уже оплачена маркетингом. Обычно утечка в воронке забирает 20-40% выручки.',
    use: 'Сильные продажи дадут больше, если завести в них больше потока. Масштабируйте маркетинг: отдел продаж его переварит.',
    week: ['Заведите все заявки недели в одну таблицу или CRM', 'Запишите 3 разговора с клиентами и найдите, где они уходят', 'Поставьте правило: ответ на заявку за 15 минут'],
    tools: [['Скрипт продаж', 'Разговор по шагам от приветствия до оплаты'], ['Воронка', 'Конверсия каждого этапа и место главной утечки'], ['CRM', 'Ни одна заявка не теряется и не остывает']]
  },
  spine: {
    state: {
      low: 'Бизнес держится на вас. Команда ждёт указаний, и любое ваше отсутствие останавливает работу.',
      mid: 'Команда есть, но зоны ответственности размыты. Вы закрываете дыры за других.',
      high: 'Команда держит бизнес: роли понятны, люди отвечают за результат.'
    },
    symptoms: ['Без вас ничего не решается, телефон звонит и в отпуске', 'Сотрудники приходят и уходят, найм идёт по знакомству', 'Непонятно, кто за что отвечает, задачи падают между людьми'],
    cost: 'Ваш час стоит дорого, а уходит на работу сотрудника. Рост упирается в ваш предел: 24 часа в сутки.',
    use: 'Сильная команда позволяет вам заниматься стратегией. Освободите 1 день в неделю и вложите его в рост.',
    week: ['Нарисуйте оргструктуру: роли, люди и кто за что отвечает', 'Выпишите 5 задач, которые делаете вы, но мог бы сотрудник', 'Проведите встречу один на один с ключевым человеком команды'],
    tools: [['Оргструктура', 'Роли, зоны ответственности и подчинение на одной схеме'], ['Найм по профилю', 'Портрет кандидата, воронка найма, испытательный срок'], ['Регламенты ролей', 'Что делает человек и по каким цифрам его оценивать']]
  },
  blood: {
    state: {
      low: 'Деньги бизнеса и ваши смешаны. Вы не знаете точную прибыль и узнаёте о кассовом разрыве в день платежа.',
      mid: 'Учёт есть, но запаздывает. Решения о тратах принимаются по остатку на счёте, а не по прибыли.',
      high: 'Вы знаете прибыль, маржу и деньги на месяц вперёд. Финансы под контролем.'
    },
    symptoms: ['Выручка растёт, а денег больше не становится', 'Налоги и зарплаты каждый раз становятся сюрпризом', 'Берёте деньги из бизнеса, когда нужно, без системы'],
    cost: 'Бизнес может быть убыточным, а вы узнаете об этом через полгода. Кассовые разрывы стоят штрафов, займов и нервов.',
    use: 'Точные цифры дают право на смелые решения. Используйте их, чтобы найти самый прибыльный продукт и вложиться в него.',
    week: ['Отделите личные деньги: назначьте себе фиксированную выплату', 'Откройте отдельный счёт и откладывайте туда налоги с каждого поступления', 'Распишите все платежи и поступления на 4 недели вперёд'],
    tools: [['Платёжный календарь', 'Все платежи и поступления на 4 недели вперёд'], ['Управленческий учёт', 'Прибыль и маржа каждый месяц до 5 числа'], ['Отдельный счёт под налоги', 'Налоги откладываются с каждого поступления']]
  },
  dna: {
    state: {
      low: 'Каждый делает по-своему. Качество зависит от настроения сотрудника, ошибки повторяются.',
      mid: 'Часть процессов описана, но регламентам не следуют, и вы всё равно проверяете вручную.',
      high: 'Процессы описаны и работают. Результат повторяется без вашего контроля.'
    },
    symptoms: ['Одни и те же ошибки повторяются снова', 'Новый сотрудник учится месяцами и только у вас', 'Вы тратите на операционку больше 4 часов в день'],
    cost: 'Хаос съедает время и деньги на переделки. Вы не можете открыть второй филиал: нечего копировать.',
    use: 'Отлаженные процессы можно тиражировать. Это основа для второй точки, франшизы или нового продукта.',
    week: ['Выберите процесс, где чаще всего ошибаются, и опишите его по шагам', 'Сделайте чек-лист на 5-7 пунктов для одной ежедневной задачи', 'Передайте одну регулярную задачу сотруднику вместе с чек-листом'],
    tools: [['Регламенты', 'Ключевые процессы по шагам, понятные новичку'], ['Чек-листы', 'Короткие списки проверки для ежедневной работы'], ['Делегирование', 'Передача задач с результатом и контрольной точкой']]
  },
  eyes: {
    state: {
      low: 'Решения принимаются на ощущениях. Цифр нет или они собираются вручную раз в квартал.',
      mid: 'Цифры есть, но разбросаны по таблицам и смотрятся нерегулярно. Проблемы видно поздно.',
      high: 'Вы видите ключевые цифры каждую неделю и замечаете отклонения сразу.'
    },
    symptoms: ['Не можете сразу назвать выручку и прибыль прошлого месяца', 'Узнаёте о проблеме, когда она уже стоит денег', 'Спорите с командой о мнениях, а не о цифрах'],
    cost: 'Вы управляете вслепую. Деньги уходят в каналы и людей, которые не окупаются, и это видно слишком поздно.',
    use: 'Хорошая аналитика ускоряет все остальные органы. Ставьте команде цели в цифрах и разбирайте их каждую неделю.',
    week: ['Выберите 5 цифр, которые показывают здоровье бизнеса', 'Соберите их в одну таблицу за последние 4 недели', 'Назначьте 20 минут в понедельник на разбор цифр с командой'],
    tools: [['Дашборд 5 цифр', 'Главные показатели бизнеса на одном экране'], ['Еженедельный отчёт', 'Короткий разбор цифр недели с командой'], ['Юнит-экономика', 'Сколько приносит и стоит один клиент']]
  }
};

// Связь органов: пары слабых органов и что это значит вместе
var V9_PAIRS = [
  ['blood', 'eyes', 'Нет цифр для решений', 'Слабые финансы и аналитика вместе значат, что вы не видите, где бизнес зарабатывает и где теряет. Любой рост в такой ситуации идёт наугад.'],
  ['heart', 'hands', 'Клиенты не доходят до оплаты', 'Поток заявок нестабилен, а те, что приходят, теряются в продажах. Это двойная утечка: платите за рекламу и не получаете выручку.'],
  ['spine', 'dna', 'Всё держится на вас', 'Нет команды с ролями и нет процессов. Бизнес работает, пока работаете вы, и остановится, как только вы остановитесь.'],
  ['brain', 'blood', 'Рост без плана и без запаса', 'Нет ясной цели и нет контроля денег. Вложения делаются по настроению, а кассовые разрывы появляются в самый неудобный момент.'],
  ['hands', 'eyes', 'Не видно, где теряются продажи', 'Продажи без цифр по воронке не улучшить: непонятно, на каком этапе уходят клиенты и что чинить первым.'],
  ['brain', 'spine', 'Команда не знает, куда идём', 'Цель не сформулирована, а роли размыты. Люди заняты, но работа не складывается в результат.'],
  ['heart', 'brain', 'Маркетинг без позиционирования', 'Без чёткой цели и ниши маркетинг говорит со всеми сразу. Реклама дорожает, а клиенты выбирают по цене.'],
  ['dna', 'hands', 'Продажи не повторяются', 'Без процесса продаж каждый менеджер работает по-своему. Удачный месяц нельзя повторить, а провальный нельзя объяснить.'],
  ['blood', 'spine', 'Фонд оплаты труда вслепую', 'Не видно, сколько приносит каждый сотрудник. Найм идёт по ощущениям, а зарплаты съедают прибыль.'],
  ['heart', 'eyes', 'Реклама без окупаемости', 'Каналы не измеряются, поэтому бюджет уходит туда, где громче, а не туда, где окупается.'],
  ['dna', 'eyes', 'Сбои видно слишком поздно', 'Процессы не описаны и не измеряются. О проблеме вы узнаёте от недовольного клиента.']
];
function v9pairInsight(results){
  var by = {}; results.forEach(function(r){ by[r.id] = r; });
  var weak = results.filter(function(r){ return r.pct < V9_WEAK; });
  var hits = V9_PAIRS.filter(function(p){ return by[p[0]] && by[p[1]] && by[p[0]].pct < V9_WEAK && by[p[1]].pct < V9_WEAK; });
  hits.sort(function(a, b){ return (by[a[0]].pct + by[a[1]].pct) - (by[b[0]].pct + by[b[1]].pct); });
  if(hits.length) return {a: by[hits[0][0]], b: by[hits[0][1]], title: hits[0][2], text: hits[0][3]};
  var s = results.slice().sort(function(a, b){ return a.pct - b.pct; });
  var lo = s[0], hi = s[s.length - 1];
  if(!lo || !hi || lo.id === hi.id) return null;
  if(!weak.length) return {a: lo, b: hi, title: 'Слабое звено держит сильное', text: 'Разрыв между ' + lo.label.toLowerCase() + ' (' + lo.pct + ') и ' + hi.label.toLowerCase() + ' (' + hi.pct + ') небольшой, но бизнес растёт со скоростью самого слабого органа. Подтяните его, и сильные стороны дадут больше.'};
  return {a: lo, b: hi, title: 'Сильная сторона вытянет слабую', text: hi.label + ' (' + hi.pct + ') сейчас лучший ресурс бизнеса. Используйте его, чтобы закрыть провал в зоне «' + lo.label.toLowerCase() + '» (' + lo.pct + '), а не распыляйтесь на всё сразу.'};
}
function v9verdict(t){
  if(t < 40) return ['Бизнес в зоне риска', 'Несколько органов работают на износ. Без изменений бизнес будет зависеть от удачи и вашего личного ресурса.'];
  if(t < 60) return ['Бизнес держится на вас', 'База есть, но система не сложилась. Рост упирается в слабые органы и в ваше время.'];
  if(t < 80) return ['Крепкая база, есть узкие места', 'Большая часть работает. Одно-два слабых звена тормозят рост, и их видно.'];
  return ['Бизнес в хорошей форме', 'Органы работают слаженно. Задача сейчас: масштаб без потери управляемости.'];
}

function renderDiagnosticResult(){
  var el = document.getElementById('page-diagnostic');
  if(!el) return;
  var results = DIAGNOSTIC_AREAS.map(function(area){
    var answers = diagState.answers[area.id] || [];
    var maxScore = area.questions.length * 4;
    var actualScore = answers.reduce(function(s, v){ return s + (v || 0); }, 0);
    var pct = Math.round((actualScore / maxScore) * 100);
    return {id: area.id, organ: area.organ, label: area.label, icon: area.icon, color: area.color, body_pos: area.body_pos, pct: pct, score: actualScore, maxScore: maxScore};
  });
  var totalScore = results.reduce(function(s, r){ return s + r.score; }, 0);
  var totalMax = results.reduce(function(s, r){ return s + r.maxScore; }, 0);
  var totalPct = Math.round((totalScore / totalMax) * 100);
  var sorted = results.slice().sort(function(a, b){ return a.pct - b.pct; });
  var worst = sorted[0], best = sorted[sorted.length - 1];
  var weakList = sorted.filter(function(r){ return r.pct < V9_WEAK; });
  if(!weakList.length) weakList = [worst];
  var moreWeak = Math.max(0, weakList.length - 3);
  var weakIds = {}; weakList.forEach(function(w){ weakIds[w.id] = 1; });
  try{
    localStorage.setItem('bs_diagnostic_done', '1');
    localStorage.setItem('bs_diagnostic_result', JSON.stringify({totalPct: totalPct, results: results, timestamp: Date.now()}));
  }catch(e){}

  var vd = v9verdict(totalPct);
  var W = V9_DIAG[worst.id], B = V9_DIAG[best.id];
  var h = '<div style="display:flex;justify-content:flex-end;margin:0 2px">' + v3mark() + '</div>';
  h += '<div style="margin:8px 2px 0"><div class="v3-lbl" style="color:#8C8C8C">РЕЗУЛЬТАТ ДИАГНОСТИКИ</div>' +
    '<h1 class="v3-h1" style="margin-top:6px;font-size:28px">Здоровье бизнеса</h1></div>';
  // Общий балл и вердикт
  h += '<div class="v2-card v9-score" style="margin-top:14px"><div class="big num' + (totalPct < 40 ? ' red' : '') + '">' + totalPct + '<small>/100</small></div>' +
    '<div style="min-width:0"><div class="vt">' + v2esc(vd[0]) + '</div><div class="vs">' + v2esc(vd[1]) + '</div></div></div>';
  // Инфографика: кольца по органам
  h += '<div class="v2-card v4-anat" style="margin-top:10px">' + renderBodyAnatomy(results, weakIds) + '</div>';
  // Главная проблема
  h += '<div class="v2-card v9-card" style="margin-top:10px;border-color:rgba(255,107,111,0.35)">' +
    '<div class="tag red">Главная проблема</div>' +
    '<div class="ttl"><div class="v9-oico r">' + v9organIcon(worst.id, 22) + '</div><div>' + v2esc(worst.organ) + ' · ' + v2esc(worst.label) + '</div><div class="sc num red">' + worst.pct + '</div></div>' +
    '<div class="txt">' + v2esc(W.state[v9level(worst.pct)]) + '</div>' +
    '<div class="sub">Типичные симптомы</div>' + W.symptoms.map(function(x){ return '<div class="v9-li r">' + v2esc(x) + '</div>'; }).join('') +
    '<div class="v9-cost"><b>Чего это стоит.</b> ' + v2esc(W.cost) + '</div></div>';
  // Сильная сторона
  if(best.id !== worst.id){
    h += '<div class="v2-card v9-card" style="margin-top:10px">' +
      '<div class="tag">Сильная сторона</div>' +
      '<div class="ttl"><div class="v9-oico">' + v9organIcon(best.id, 22) + '</div><div>' + v2esc(best.organ) + ' · ' + v2esc(best.label) + '</div><div class="sc num">' + best.pct + '</div></div>' +
      '<div class="txt">' + v2esc(B.state[v9level(best.pct)]) + '</div>' +
      '<div class="sub">Как использовать</div><div class="v9-li">' + v2esc(B.use) + '</div></div>';
  }
  // Что сделать на этой неделе: по каждому слабому органу
  h += '<div class="v2-h">Что сделать на этой неделе</div>';
  h += weakList.slice(0, 3).map(function(r){
    var D = V9_DIAG[r.id];
    return '<div class="v2-card v9-card" style="margin-top:8px">' +
      '<div class="ttl" style="margin-top:0;font-size:16px"><div class="v9-oico' + (r.pct < V9_WEAK ? ' r' : '') + '" style="width:34px;height:34px;border-radius:10px">' + v9organIcon(r.id, 18) + '</div><div>' + v2esc(r.label) + '</div>' +
      '<div class="sc num' + (r.pct < V9_WEAK ? ' red' : '') + '">' + r.pct + '</div></div>' +
      D.week.map(function(x){ return '<div class="v9-li">' + v2esc(x) + '</div>'; }).join('') + '</div>';
  }).join('');
  if(moreWeak) h += '<div class="v3-cap" style="margin:10px 4px 0;line-height:1.45">Ещё ' + moreWeak + ' ' + v3plural(moreWeak, 'орган', 'органа', 'органов') + ' ниже ' + V9_WEAK + '. Начните с трёх самых слабых: остальные подтянутся вслед.</div>';
  // Связь органов
  var ins = v9pairInsight(results);
  if(ins){
    h += '<div class="v2-card v9-card" style="margin-top:10px"><div class="tag">Связь органов · ' + v2esc(ins.a.label) + ' + ' + v2esc(ins.b.label) + '</div>' +
      '<div class="ttl" style="font-size:17px">' + v2esc(ins.title) + '</div><div class="txt">' + v2esc(ins.text) + '</div></div>';
  }
  // Подводка к разбору: инструменты под слабые органы
  var tools = [], seen = {};
  var srcOrg = sorted.slice(0, 3);
  for(var round = 0; tools.length < 3 && round < 3; round++){
    srcOrg.forEach(function(r){
      var t = V9_DIAG[r.id].tools[round];
      if(t && tools.length < 3 && !seen[t[0]]){ seen[t[0]] = 1; tools.push([t[0], t[1], r.label]); }
    });
  }
  var isLeadView = viewAs !== 'resident';
  if(!isLeadView){
    // Резидент: без записи на экспресс-разбор, результат уходит трекеру в разбор
    h += '<div class="v2-card v9-card" style="margin-top:16px"><div class="tag">Инструменты под слабые органы</div>' +
      '<div class="v9-tools">' + tools.map(function(t, i){
        return '<div class="v9-tool"><div class="n">' + String(i + 1).padStart(2, '0') + '</div><div style="min-width:0"><b>' + v2esc(t[0]) + '</b><span>' + v2esc(t[1]) + ' · ' + v2esc(t[2].toLowerCase()) + '</span></div></div>';
      }).join('') + '</div>' +
      '<div class="txt">Результат сохраняется в ваш разбор: трекер видит его на платформе и разберёт с вами на встрече.</div></div>';
    h += '<div style="margin-top:12px;display:flex;flex-direction:column;gap:8px">' +
      '<button class="v3-primary" id="diagSaveBtn" onclick="v10saveDiag(this)">' + (window._v10diagSaved ? 'Сохранено в разбор' : 'Сохранить в разбор') + '</button>' +
      '<button class="v3-outline" id="diagRestartBtn">Пройти заново</button></div>';
  } else {
  h += '<div class="v2-card v9-card" style="margin-top:16px;background:#FFFFFF;color:#000;border-color:#FFFFFF">' +
    '<div class="tag" style="color:#6E6E6E">Экспресс-разбор · 60 минут</div>' +
    '<div class="ttl" style="font-size:20px">На экспресс-разборе разберём точные инструменты для вашего бизнеса</div>' +
    '<div class="v9-tools">' + tools.map(function(t, i){
      return '<div class="v9-tool" style="background:#F3F3F3;border-color:#E2E2E2"><div class="n">' + String(i + 1).padStart(2, '0') + '</div><div style="min-width:0"><b>' + v2esc(t[0]) + '</b><span style="color:#555">' + v2esc(t[1]) + ' · ' + v2esc(t[2].toLowerCase()) + '</span></div></div>';
    }).join('') + '</div>' +
    '<div class="txt" style="color:#333">Рустам и Береке найдут причину, а не симптом. Уйдёте с планом задач на 10 дней.</div></div>';
  h += '<div style="margin-top:12px;display:flex;flex-direction:column;gap:8px">' +
    (isLeadView ? '<button class="v3-primary" id="diagCtaBtn">Записаться на разбор</button>' : '') +
    '<button class="v3-outline" id="diagRestartBtn">Пройти заново</button></div>';
  }
  el.innerHTML = h;

  var ctaBtn = el.querySelector('#diagCtaBtn');
  if(ctaBtn){
    ctaBtn.addEventListener('click', function(){
      try{ this.blur(); }catch(e){}
      v15book('Прошёл диагностику: ' + totalPct + ' из 100. Слабее всего ' + String(worst.organ || '').toLowerCase() + ' (' + String(worst.label || '').toLowerCase() + ', ' + worst.pct + '). Хочу понять, с чего начать');
    });
  }
  var restartBtn = el.querySelector('#diagRestartBtn');
  if(restartBtn){
    restartBtn.addEventListener('click', function(){
      try{ this.blur(); }catch(e){}
      try{
        localStorage.removeItem('bs_diagnostic_done');
        localStorage.removeItem('bs_diagnostic_result');
      }catch(e){}
      diagState = {started: false, finished: false, currentArea: 0, currentQuestion: 0, answers: {}};
      window._v10diagSaved = 0;
      renderDiagnostic();
      v9scrollTop();
    });
  }
}


function _openEditLeadmagnet(item){
  var existing = document.getElementById('lmEditModal');
  if(existing) existing.remove();
  
  var CATEGORIES = [
    {id:'brain', label:'Стратегия'},
    {id:'heart', label:'Маркетинг'},
    {id:'hands', label:'Продажи'},
    {id:'spine', label:'Команда'},
    {id:'blood', label:'Финансы'},
    {id:'dna',   label:'Процессы'},
    {id:'eyes',  label:'Аналитика'}
  ];
  
  var optionsHtml = CATEGORIES.map(function(c){
    var sel = (item.category === c.id) ? ' selected' : '';
    return '<option value="'+c.id+'"'+sel+'>'+c.label+'</option>';
  }).join('');
  
  var modal = document.createElement('div');
  modal.id = 'lmEditModal';
  modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.92);z-index:9999;display:flex;align-items:center;justify-content:center;padding:20px;backdrop-filter:blur(8px);';
  
  modal.innerHTML = 
    '<div style="background:#1a1a1a;border:1px solid rgba(255,255,255,0.1);border-radius:20px;padding:28px 24px;max-width:420px;width:100%;">' +
      '<div style="font-size:20px;font-weight:800;color:#fff;margin-bottom:18px;">Редактировать чек-лист</div>' +
      
      '<label style="display:block;font-size:12px;color:rgba(255,255,255,0.55);margin-bottom:6px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;">Название</label>' +
      '<input type="text" id="lmEditTitle" value="'+(item.title||'').replace(/"/g,'&quot;')+'" style="width:100%;padding:12px 14px;background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.1);border-radius:10px;color:#fff;font-size:15px;font-family:inherit;margin-bottom:16px;outline:none;"/>' +
      
      '<label style="display:block;font-size:12px;color:rgba(255,255,255,0.55);margin-bottom:6px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;">Описание</label>' +
      '<textarea id="lmEditDesc" rows="3" style="width:100%;padding:12px 14px;background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.1);border-radius:10px;color:#fff;font-size:14px;font-family:inherit;margin-bottom:16px;outline:none;resize:vertical;">'+(item.description||'')+'</textarea>' +
      
      '<label style="display:block;font-size:12px;color:rgba(255,255,255,0.55);margin-bottom:6px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;">Категория</label>' +
      '<select id="lmEditCat" style="width:100%;padding:12px 14px;background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.1);border-radius:10px;color:#fff;font-size:15px;font-family:inherit;margin-bottom:22px;outline:none;">' + optionsHtml + '</select>' +
      
      // Блок: тест и swap файлы
      '<div style="margin-bottom:18px;padding:14px;background:rgba(255,107,111,0.06);border:1px solid rgba(255,107,111,0.25);border-radius:12px;">' +
        '<div style="font-size:11px;color:#FF6B6F;font-weight:800;letter-spacing:1.5px;text-transform:uppercase;margin-bottom:8px;">Если перепутан файл</div>' +
        '<button id="lmTestSelf" style="width:100%;padding:11px;background:transparent;color:#fff;border:1px solid rgba(255,255,255,0.2);border-radius:10px;font-size:14px;font-weight:700;cursor:pointer;font-family:inherit;margin-bottom:8px;">Прислать мне для проверки</button>' +
        '<select id="lmSwapTarget" style="width:100%;padding:11px 14px;background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.1);border-radius:10px;color:#fff;font-size:13px;font-family:inherit;margin-bottom:8px;outline:none;">' + 
          '<option value="">Поменять файл с...</option>' +
          (window._allLeadmagnets||[]).filter(function(it){return it.key !== item.key;}).map(function(it){
            return '<option value="'+it.key+'">'+ (it.title||it.key) +'</option>';
          }).join('') +
        '</select>' +
        '<button id="lmDoSwap" style="width:100%;padding:11px;background:#FF6B6F;color:#fff;border:none;border-radius:10px;font-size:13px;font-weight:800;cursor:pointer;font-family:inherit;">Поменять файлы местами</button>' +
      '</div>' +
      
      '<div style="display:flex;gap:10px;">' +
        '<button id="lmEditSave" style="flex:1;padding:14px;background:#FFFFFF;color:#000;border:none;border-radius:12px;font-size:15px;font-weight:800;cursor:pointer;font-family:inherit;">Сохранить</button>' +
        '<button id="lmEditCancel" style="padding:14px 18px;background:transparent;color:rgba(255,255,255,0.6);border:1px solid rgba(255,255,255,0.15);border-radius:12px;font-size:14px;font-weight:600;cursor:pointer;font-family:inherit;">Отмена</button>' +
      '</div>' +
    '</div>';
  
  document.body.appendChild(modal);
  
  document.getElementById('lmEditCancel').addEventListener('click', function(){ modal.remove(); });
  
  // Тест: прислать себе для проверки
  document.getElementById('lmTestSelf').addEventListener('click', function(){
    var btn = this;
    btn.disabled = true;
    btn.textContent = 'Отправляем в Telegram...';
    callAction('testLeadmagnet', {key:item.key}).then(function(rr){
      if(rr && rr.ok){
        btn.textContent = 'Отправлено! Проверь в чате с ботом';
        setTimeout(function(){
          btn.disabled = false;
          btn.textContent = 'Прислать мне для проверки';
        }, 4000);
      } else {
        btn.disabled = false;
        btn.textContent = 'Прислать мне для проверки';
        showToast('Ошибка: ' + (rr && rr.error || ''));
      }
    });
  });
  
  // Swap: поменять fileId с другим чек-листом
  document.getElementById('lmDoSwap').addEventListener('click', function(){
    var sel = document.getElementById('lmSwapTarget');
    var targetKey = sel.value;
    if(!targetKey){ showToast('Выбери второй чек-лист'); return; }
    var targetName = sel.options[sel.selectedIndex].text;
    if(!confirm('Поменять файлы:\n\n«'+item.title+'» ↔ «'+targetName+'»\n\nПодтверди.')) return;
    var btn = this;
    btn.disabled = true;
    btn.textContent = 'Меняю...';
    callAction('swapLeadmagnetFiles', {keyA:item.key, keyB:targetKey}).then(function(rr){
      if(rr && rr.ok){
        showToast('Файлы поменяны местами');
        modal.remove();
        renderLeadmagnets();
      } else {
        btn.disabled = false;
        btn.textContent = 'Поменять файлы местами';
        showToast('Ошибка: ' + (rr && rr.error || ''));
      }
    });
  });
  document.getElementById('lmEditSave').addEventListener('click', function(){
    var newTitle = document.getElementById('lmEditTitle').value.trim();
    var newDesc = document.getElementById('lmEditDesc').value.trim();
    var newCat = document.getElementById('lmEditCat').value;
    if(!newTitle){ showToast('Введи название'); return; }
    
    callAction('updateLeadmagnet', {
      key: item.key, title: newTitle, description: newDesc, category: newCat
    }).then(function(rr){
      if(rr && rr.ok){
        showToast('Сохранено');
        modal.remove();
        renderLeadmagnets();
      } else {
        showToast('Ошибка: ' + (rr && rr.error || ''));
      }
    });
  });
}

function selectMonth(idx){
  // Точечно обновляем карточку месяца: цифры не мигают, лента месяцев не прыгает
  window._selectedMonth = idx;
  var mp = window._monthlyPL;
  if(!mp || !mp.months || !mp.months[idx]) return;
  var m = mp.months[idx];
  var set = function(id, val, neg){
    var el = document.getElementById(id);
    if(!el) return;
    el.textContent = val;
    el.classList.toggle('red', !!neg);
  };
  set('v3MonName', m.name + ' ' + (typeof mp.year === 'number' ? mp.year : new Date().getFullYear()));
  set('v3Rev', fmt(m.revenue));
  set('v3Profit', fmt(m.profit), m.profit < 0);
  set('v3Div', fmt(m.dividends || 0));
  set('v3Kassa', (m.kassa === null || m.kassa === undefined) ? '·' : fmt(m.kassa), m.kassa != null && m.kassa < 0);
  document.querySelectorAll('#plMonthsRow .v3-mchip').forEach(function(c, i){ c.classList.toggle('on', i === idx); });
}

// ═══ 99 ГАЙДОВ: каталог, ридер, промо-блоки для всех ролей ═══
// Индекс: GET app/guides → {version, items}. Полный гайд: GET app/guide/<id>. PDF: POST app/guide/<id>/send или публичная ссылка.
// Кэш: bs_guides (индекс с версией), bs_guide_<id> (полные гайды, не больше 15, вытесняем самые давние).
// Отметки на устройстве: bs_gd_p_<id> = ["<номер шага>:<номер пункта>"], место чтения: bs_gd_pos = {id: {y, p, t}}
function V12_ORG(){ return [['Финансы', 'blood'], ['Стратегия', 'brain'], ['Маркетинг', 'heart'], ['Продажи', 'hands'], ['Команда', 'spine'], ['Процессы', 'dna'], ['Аналитика', 'eyes'], ['Собственник', 'owner']]; }
function V12_LV(){ return [['старт', 'Старт', 1], ['рост', 'Рост', 2], ['система', 'Система', 3]]; }
function V12_OWNER_ICON(){ return '<circle cx="10" cy="7" r="3.5"/><path d="M3.5 20.5c0-3.9 2.9-6.6 6.5-6.6 1.3 0 2.5.3 3.4.9"/><path d="M18.2 11.5l-2.7 4.5h3.6l-2.7 4.5"/>'; }
function v12organId(o){ for(var i = 0; i < V12_ORG().length; i++) if(V12_ORG()[i][0] === o) return V12_ORG()[i][1]; return 'brain'; }
function v12oIcon(organ, size, w){
  var id = v12organId(organ);
  return '<svg width="' + (size || 20) + '" height="' + (size || 20) + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="' + (w || 1.8) + '" stroke-linecap="round" stroke-linejoin="round">' + (id === 'owner' ? V12_OWNER_ICON() : (((typeof V9_ORGAN_ICONS === 'object' && V9_ORGAN_ICONS) || {})[id] || '')) + '</svg>';
}
function v12num(n){ return String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, '\u00a0'); }
function v12lvl(level){
  var L = V12_LV().filter(function(x){ return x[0] === String(level || '').toLowerCase(); })[0] || V12_LV()[0];
  return '<span class="v12-lvl l' + L[2] + '"><span class="bars"><i class="on"></i><i' + (L[2] > 1 ? ' class="on"' : '') + '></i><i' + (L[2] > 2 ? ' class="on"' : '') + '></i></span>' + L[1] + '</span>';
}
function v12clean(s){ return String(s == null ? '' : s).replace(/\s*[\u2014\u2013]\s*/g, ': ').trim(); }
// Цифры не разрываются: «1 450 000 ₸», «19 млн ₸», «32 %»
function v14nb(s){
  return String(s == null ? '' : s)
    .replace(/(\d) (?=\d{3}(?!\d))/g, '$1\u00a0')
    .replace(/(\d) (?=\d{3}(?!\d))/g, '$1\u00a0')
    .replace(/ ₸/g, '\u00a0₸')
    .replace(/₸\/(?=\S)/g, '₸/\u2060')
    .replace(/(\d) (млн|млрд|тыс\.?|%|п\.п\.)/g, '$1\u00a0$2');
}
function gE(s){ return v14nb(v2esc(v12clean(s))); }
function v14no(id){ return String(id || '').replace(/^g0*/, ''); }
function v14no2(id){ var n = v14no(id); return n.length < 2 ? '0' + n : n; }
function v14time(t){ return String(t || '').split(':')[0].trim(); }
function v14heroLine(h){
  if(!h || !h.name) return '';
  var b = String(h.business || '').trim();
  return v12clean(h.name) + (h.age ? ', ' + h.age : '') + (b ? ' · ' + v12clean(b) : '');
}
var V14_PDF_BASE = 'https://bs-platform-production.up.railway.app/api/v1/public/guide/';
var V14_SEND_ICON = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 3L10 14"/><path d="M21 3l-6.5 18-3.5-7-7-3.5z"/></svg>';
var V14_PDF_ICON = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3h8l5 5v13H6z"/><path d="M14 3v5h5"/><path d="M12 11v6M9.5 14.5L12 17l2.5-2.5"/></svg>';

// Старые короткие чек-листы (r12/r13) больше не используются: убираем их кэш один раз
(function(){
  try{
    if(localStorage.getItem('bs_gd_clean') === '1') return;
    var del = [];
    for(var i = 0; i < localStorage.length; i++){ var k = localStorage.key(i); if(k === 'bs_checklists' || /^bs_cl_p_/.test(k)) del.push(k); }
    del.forEach(function(k){ localStorage.removeItem(k); });
    localStorage.setItem('bs_gd_clean', '1');
  }catch(e){}
  try{ sessionStorage.removeItem('bs_cl_cur'); }catch(e){}
})();

// ── Индекс ──
window._gd = window._gd || null;
function v14idxCached(){ try{ var c = JSON.parse(localStorage.getItem('bs_guides') || 'null'); return c && Array.isArray(c.items) && c.items.length ? c : null; }catch(e){ return null; } }
function v14norm(j){
  var arr = Array.isArray(j) ? j : ((j && j.items) || []);
  var items = arr.filter(function(x){ return x && x.id && String(x.title || '').trim(); }).map(function(x){
    var h = x.hero || {};
    return {id: String(x.id), organ: String(x.organ || ''), title: v12clean(x.title), subtitle: v12clean(x.subtitle), promise: v12clean(x.promise),
      time: v12clean(x.time), level: String(x.level || 'старт').toLowerCase(), existing: !!x.existing,
      hero: {name: String(h.name || ''), age: h.age || '', business: String(h.business || ''), city: String(h.city || '')},
      steps: Array.isArray(x.steps) ? x.steps.map(v12clean) : [], items: Number(x.items) || 0, pdfKB: Number(x.pdfKB) || 0};
  });
  return {version: (j && !Array.isArray(j) && j.version != null) ? String(j.version) : '', items: items};
}
function v14data(){
  if(!window._gd){
    var c = v14idxCached();
    if(c) window._gd = {items: c.items, version: c.version || '', at: 0, cached: true};
  }
  return window._gd;
}
function v14items(){ return ((v14data() || {}).items) || []; }
function v14find(id){ return v14items().filter(function(x){ return x.id === id; })[0] || null; }
function loadGuides(force){
  var cur = v14data();
  if(!force && cur && !cur.error && cur.at && Date.now() - cur.at < 600000) return Promise.resolve(cur);
  if(window._gdLoading) return window._gdLoading;
  var init = bsInitData();
  if(!init){
    var c0 = v14idxCached();
    window._gd = {items: c0 ? c0.items : [], version: c0 ? c0.version : '', error: 'noinit', stale: !!c0, at: Date.now()};
    return Promise.resolve(window._gd);
  }
  window._gdLoading = fetch(APP_SERVER + 'guides?' + new URLSearchParams({_tg: init}).toString())
    .then(function(r){ if(!r.ok) throw new Error('http ' + r.status); return r.json(); })
    .then(function(j){
      var d = v14norm(j), c = v14idxCached();
      if(!d.items.length && c) d = {items: c.items, version: c.version};
      else if(d.items.length && (!c || String(c.version || '') !== d.version || !d.version || c.items.length !== d.items.length)){
        try{ localStorage.setItem('bs_guides', JSON.stringify({version: d.version, items: d.items, saved: Date.now()})); }catch(e){}
      }
      window._gd = {items: d.items, version: d.version, at: Date.now()};
      return window._gd;
    })
    .catch(function(e){
      var c = v14idxCached();
      var items = (window._gd && window._gd.items && window._gd.items.length) ? window._gd.items : (c ? c.items : []);
      window._gd = {items: items, version: c ? c.version : '', error: String(e && e.message || 'network'), stale: items.length > 0, at: Date.now()};
      return window._gd;
    })
    .then(function(l){ window._gdLoading = null; v14refresh(); return l; });
  return window._gdLoading;
}
// После загрузки: перерисовываем открытый экран и промо-блоки на месте
function v14refresh(){
  var a = v9activeId();
  if(a === 'guides'){ v14keep(renderGuides); return; }
  if(a === 'guide'){ v14keep(renderGuide); return; }
  document.querySelectorAll('[data-v14promo]').forEach(function(el){
    var t = document.createElement('div'); t.innerHTML = v14promo(el.getAttribute('data-v14promo'));
    if(t.firstChild) el.replaceWith(t.firstChild);
  });
  document.querySelectorAll('[data-v13hero]').forEach(function(el){
    var t = document.createElement('div'); t.innerHTML = v13leadHero();
    if(t.firstChild) el.replaceWith(t.firstChild);
  });
  document.querySelectorAll('[data-v14feat]').forEach(function(el){
    var t = document.createElement('div'); t.innerHTML = v14libFeatured();
    if(t.firstChild) el.replaceWith(t.firstChild);
  });
}
function v14keep(fn){ var y = window.scrollY; try{ fn(); }catch(e){ console.error('guides:', e); } try{ window.scrollTo(0, y); }catch(e){} }

// ── Полные гайды: память, затем localStorage, затем сеть ──
window._gfull = window._gfull || {};
function v14F(){ return window._gfull || (window._gfull = {}); }
function v14gCached(id){ try{ var c = JSON.parse(localStorage.getItem('bs_guide_' + id) || 'null'); return c && c.g && c.g.id ? c : null; }catch(e){ return null; } }
function v14lru(){ try{ var a = JSON.parse(localStorage.getItem('bs_guide_lru') || '[]'); return Array.isArray(a) ? a : []; }catch(e){ return []; } }
function v14gSave(id, g, v){
  var lru = v14lru().filter(function(x){ return x !== id; }); lru.push(id);
  while(lru.length > 15) try{ localStorage.removeItem('bs_guide_' + lru.shift()); }catch(e){}
  var val = JSON.stringify({v: v || '', at: Date.now(), g: g});
  for(var k = 0; k < 4; k++){
    try{ localStorage.setItem('bs_guide_' + id, val); break; }
    catch(e){ // место кончилось: вытесняем половину старых
      var drop = lru.splice(0, Math.max(1, Math.floor((lru.length - 1) / 2)));
      drop.forEach(function(x){ if(x !== id) try{ localStorage.removeItem('bs_guide_' + x); }catch(e2){} });
      lru = lru.filter(function(x){ return drop.indexOf(x) < 0 || x === id; }); if(lru.indexOf(id) < 0) lru.push(id);
    }
  }
  try{ localStorage.setItem('bs_guide_lru', JSON.stringify(lru)); }catch(e){}
}
function v14gTouch(id){ var lru = v14lru(); if(lru[lru.length - 1] === id) return; lru = lru.filter(function(x){ return x !== id; }); if(v14gCached(id)) lru.push(id); try{ localStorage.setItem('bs_guide_lru', JSON.stringify(lru)); }catch(e){} }
function v14gNorm(g){
  if(!g || !g.id || !Array.isArray(g.steps)) return null;
  g.steps = g.steps.filter(Boolean).map(function(s, i){ s.n = Number(s.n) || i + 1; s.body = Array.isArray(s.body) ? s.body : (s.body ? [String(s.body)] : []); s.checklist = Array.isArray(s.checklist) ? s.checklist : []; return s; });
  g.hero = g.hero || {}; g.method = g.method || {}; g.epilogue = g.epilogue || {};
  return g;
}
function v14guide(id){
  var m = v14F()[id];
  if(m && m.g) return m;
  var c = v14gCached(id);
  if(c){ v14F()[id] = {g: v14gNorm(c.g), v: c.v, at: 0, cached: true}; return v14F()[id]; }
  return null;
}
function loadGuide(id){
  var m = v14F()[id];
  if(m && m.at && !m.error && Date.now() - m.at < 3600000) return Promise.resolve(m);
  window._gLoading = window._gLoading || {};
  if(window._gLoading[id]) return window._gLoading[id];
  var init = bsInitData();
  if(!init){ var c0 = v14guide(id); v14F()[id] = {g: c0 && c0.g, error: 'noinit', at: Date.now()}; return Promise.resolve(v14F()[id]); }
  var p = fetch(APP_SERVER + 'guide/' + encodeURIComponent(id) + '?' + new URLSearchParams({_tg: init}).toString())
    .then(function(r){ if(r.status === 404) throw new Error('not_found'); if(!r.ok) throw new Error('http ' + r.status); return r.json(); })
    .then(function(j){
      var g = v14gNorm(j); if(!g) throw new Error('bad');
      var v = (v14data() || {}).version || '';
      v14gSave(id, g, v);
      v14F()[id] = {g: g, v: v, at: Date.now()};
      return v14F()[id];
    })
    .catch(function(e){
      var c = v14gCached(id);
      v14F()[id] = {g: c ? v14gNorm(c.g) : null, error: String(e && e.message || 'network'), stale: !!c, at: Date.now()};
      return v14F()[id];
    })
    .then(function(r){
      delete window._gLoading[id];
      if(v9activeId() === 'guide' && v14curId() === id && !document.getElementById('gdArt')) renderGuide();
      return r;
    });
  window._gLoading[id] = p;
  return p;
}

// ── Прогресс: отметки пунктов и место чтения ──
function v14prog(id){
  try{ var a = JSON.parse(localStorage.getItem('bs_gd_p_' + id) || '[]'); return Array.isArray(a) ? a.filter(function(k){ return typeof k === 'string' && /^\d+:\d+$/.test(k); }) : []; }catch(e){ return []; }
}
function v14progSave(id, arr){
  try{ if(arr.length) localStorage.setItem('bs_gd_p_' + id, JSON.stringify(arr)); else localStorage.removeItem('bs_gd_p_' + id); }catch(e){}
}
function v14keys(g){ var out = []; (g.steps || []).forEach(function(s){ s.checklist.forEach(function(t, i){ out.push(s.n + ':' + i); }); }); return out; }
// Без полного гайда считаем по индексу: число пунктов знает сервер
function v14stat(x){
  var f = v14F()[x.id];
  if(f && f.g){ var keys = v14keys(f.g), a = v14prog(x.id); return {d: keys.filter(function(k){ return a.indexOf(k) >= 0; }).length, n: keys.length}; }
  var n = Number(x.items) || 0;
  return {d: Math.min(v14prog(x.id).length, n), n: n};
}
function v14pos(id){ try{ var m = JSON.parse(localStorage.getItem('bs_gd_pos') || '{}'); return id ? (m[id] || null) : m; }catch(e){ return id ? null : {}; } }
function v14posSave(id, y, p){
  try{
    var m = v14pos() || {};
    m[id] = {y: Math.round(y), p: Math.round(p), t: Date.now()};
    var ks = Object.keys(m); if(ks.length > 60){ ks.sort(function(a, b){ return m[a].t - m[b].t; }); ks.slice(0, ks.length - 60).forEach(function(k){ delete m[k]; }); }
    localStorage.setItem('bs_gd_pos', JSON.stringify(m));
  }catch(e){}
}
// Начатый гайд: есть отметки или прочитан хотя бы на 5%
function v14started(x){ var s = v14stat(x); if(s.n && s.d >= s.n) return false; if(s.d) return true; var p = v14pos(x.id); return !!(p && p.p >= 5 && p.p < 97); }
function v12ring(done, total){
  var r = 19, c = 2 * Math.PI * r, p = total ? done / total : 0, full = total && done >= total;
  return '<div class="v12-ring' + (full ? ' full' : '') + '"><svg width="44" height="44" viewBox="0 0 44 44">' +
    (full ? '<circle cx="22" cy="22" r="21" fill="#FFFFFF"/>' :
      '<circle cx="22" cy="22" r="' + r + '" fill="none" stroke="#262626" stroke-width="3"/>' +
      '<circle cx="22" cy="22" r="' + r + '" fill="none" stroke="#FFFFFF" stroke-width="3" stroke-linecap="round" stroke-dasharray="' + c.toFixed(1) + '" stroke-dashoffset="' + (c * (1 - p)).toFixed(1) + '"/>') +
    '</svg><span class="num">' + (full ? (V3_CHECK || '') : Math.round(p * 100) + '%') + '</span></div>';
}

// ── Открытие ──
function v14openCat(organ){
  if(organ) window._gdOrgan = organ;
  showPage('guides');
}
function v14curId(){
  var id = window._gdCur;
  if(!id){ try{ id = sessionStorage.getItem('bs_gd_cur'); }catch(e){} }
  return id || '';
}
function v14open(id){
  id = String(id || '').toLowerCase();
  window._gdCur = id;
  try{ sessionStorage.setItem('bs_gd_cur', id); }catch(e){}
  showPage('guide');
  v14restore(id);
}
// Из промо: сначала каталог, потом гайд, чтобы «Назад» вёл в каталог
function v14openFrom(id){ window._gdOrgan = 'all'; showPage('guides'); v14open(id); }
// Глубокая ссылка guide_g12 → g012
function v14normId(s){ var m = /^g?0*(\d{1,3})$/i.exec(String(s || '').trim()); return m ? 'g' + ('00' + m[1]).slice(-3) : ''; }

// ── Каталог «99 гайдов» ──
function v14filtered(){
  var items = v14items();
  var o = window._gdOrgan || 'all', lv = window._gdLevel || 'all', q = String(window._gdQ || '').toLowerCase().trim();
  return items.filter(function(x){
    if(o !== 'all' && x.organ !== o) return false;
    if(lv !== 'all' && x.level !== lv) return false;
    if(q){ var hay = [x.title, x.subtitle, x.hero.business, x.hero.name, x.organ].join(' ').toLowerCase(); if(hay.indexOf(q) < 0) return false; }
    return true;
  });
}
function v14card(x){
  var s = v14stat(x), pos = v14pos(x.id), done = s.n && s.d >= s.n;
  var hl = v14heroLine(x.hero), st = x.steps.length;
  var meta = [];
  if(x.time) meta.push('<span>' + gE(v14time(x.time)) + '</span>');
  if(st) meta.push('<span class="num">' + st + ' ' + v3plural(st, 'шаг', 'шага', 'шагов') + '</span>');
  if(x.existing) meta.push('<span>PDF' + (x.pdfKB ? ' · ' + (x.pdfKB >= 1024 ? (x.pdfKB / 1024).toFixed(1).replace('.', ',') + '\u00a0МБ' : x.pdfKB + '\u00a0КБ') : '') + '</span>');
  if(done) meta.push('<span style="color:#FFFFFF">пройден</span>');
  else if(!s.d && pos && pos.p >= 5) meta.push('<span style="color:#CFCFCF">прочитано ' + Math.min(100, pos.p) + '%</span>');
  return '<div class="v12-card v14-card' + (done ? ' done' : '') + '" data-gid="' + v2esc(x.id) + '" onclick="v14open(\'' + v2esc(x.id) + '\')">' +
    '<div class="top"><div class="v12-org">' + v12oIcon(x.organ, 14, 2) + v2esc(x.organ) + '</div><div class="v14-tr">' + (x.existing ? '<span class="v14-pdfb">PDF</span>' : '') + '<span class="v14-no num">№\u00a0' + v14no2(x.id) + '</span></div></div>' +
    '<div class="row"><div class="m"><div class="t">' + gE(x.title) + '</div>' + (x.subtitle ? '<div class="s">' + gE(x.subtitle) + '</div>' : '') +
    (hl ? '<div class="v14-hl"><i>' + v2esc((x.hero.name || '?').charAt(0)) + '</i><span>' + gE(hl) + '</span></div>' : '') +
    '<div class="meta">' + v12lvl(x.level) + meta.join('<i></i>') + '</div></div>' +
    (s.d ? v12ring(s.d, s.n) : '') + '</div></div>';
}
function v14listHTML(){
  var list = v14filtered();
  var all = v14items();
  var filt = (window._gdOrgan && window._gdOrgan !== 'all') || (window._gdLevel && window._gdLevel !== 'all') || window._gdQ;
  var head = '<div class="v12-cnt"><span>' + (filt ? 'Найдено ' + v12num(list.length) : 'Все гайды') + '</span>' + (filt ? '<span class="lnk" onclick="v14reset()">Сбросить</span>' : '<span class="num">' + v12num(all.length) + '</span>') + '</div>';
  if(!list.length) return head + '<div class="v2-card v12-empty">Ничего не нашли.<br>Попробуйте другой орган, уровень или слово</div>';
  return head + list.map(v14card).join('');
}
// «Продолжить»: начатые гайды сразу под шапкой каталога, последние открытые сверху
function v14contHTML(){
  var started = v14items().filter(v14started);
  if(!started.length) return '';
  started.sort(function(a, b){ var pa = v14pos(a.id), pb = v14pos(b.id); return ((pb && pb.t) || 0) - ((pa && pa.t) || 0); });
  return '<div class="v12-cnt"><span>Продолжить</span><span class="num">' + started.length + '</span></div><div id="gdCont">' + started.slice(0, 5).map(v14card).join('') + '</div>';
}
function v14setOrgan(o){ window._gdOrgan = (window._gdOrgan === o && o !== 'all') ? 'all' : o; v14keep(renderGuides); }
function v14setLevel(l){ window._gdLevel = l; v14keep(renderGuides); }
function v14reset(){ window._gdOrgan = 'all'; window._gdLevel = 'all'; window._gdQ = ''; v14keep(renderGuides); }
function v14search(v){ window._gdQ = v; var el = document.getElementById('gdList'); if(el) el.innerHTML = v14listHTML(); }
function renderGuides(){
  var el = document.getElementById('guidesContent');
  if(!el) return;
  var L = v14data();
  if(!L || !L.at) loadGuides(false);
  var items = (L && L.items) || [];
  var total = items.length || 99;
  var steps = items.reduce(function(s, x){ return s + x.steps.length; }, 0);
  var started = 0, finished = 0;
  items.forEach(function(x){ var s = v14stat(x); if(s.d || v14started(x)) started++; if(s.n && s.d >= s.n) finished++; });
  var free = navRole() !== 'resident';
  var h = '<div class="v12-hero v14-chero"><div class="k">' + (free ? 'БИБЛИОТЕКА BS · БЕСПЛАТНО' : 'БИБЛИОТЕКА BS') + '</div>' +
    '<div class="top"><div class="n num">' + total + '</div><div class="t">' + v3plural(total, 'гайд', 'гайда', 'гайдов') + '<br>для вашего<br>бизнеса' + (free ? '.<br><b>Бесплатно</b>' : '') + '</div></div>' +
    '<div class="s">Разборы на 10-12 страниц: история с цифрами, метод, шаги с таблицами и чек-листами, итог в точке Б</div>' +
    '<div class="v12-stats">' +
      '<div><b class="num">' + (steps ? v12num(steps) : '500+') + '</b><span>' + (steps ? v3plural(steps, 'шаг', 'шага', 'шагов') : 'шагов') + ' внутри</span></div>' +
      '<div><b class="num">7+1</b><span>органов и собственник</span></div>' +
      (finished ? '<div><b class="num">' + finished + '<span style="display:inline;font-size:13px;color:#6E6E6E;margin:0">/' + started + '</span></b><span>пройдено у вас</span></div>'
               : (started ? '<div><b class="num">' + started + '</b><span>в работе у вас</span></div>' : '<div><b class="num">3</b><span>уровня глубины</span></div>')) +
    '</div></div>';
  if(!items.length){
    if(L && L.error) h += '<div class="v2-card v12-empty" style="margin-top:12px">Не получилось загрузить гайды.<br><button class="v3-outline" style="margin-top:12px" onclick="window._gd=null;loadGuides(true);renderGuides()">Повторить</button></div>';
    else h += '<div class="loading"><div class="spinner"></div></div>';
    el.innerHTML = h;
    return;
  }
  h += v14contHTML();
  h += v13softBanner(started);
  var cnt = {}, lcnt = {};
  var o = window._gdOrgan || 'all', lv = window._gdLevel || 'all';
  items.forEach(function(x){ cnt[x.organ] = (cnt[x.organ] || 0) + 1; if(o === 'all' || x.organ === o) lcnt[x.level] = (lcnt[x.level] || 0) + 1; });
  h += '<div class="v12-og">' + V12_ORG().map(function(g){
    return '<button class="' + (o === g[0] ? 'on' : '') + '" onclick="v14setOrgan(\'' + g[0] + '\')" aria-label="' + g[0] + '"><span class="i">' + v12oIcon(g[0], 22) + '</span><b class="num">' + (cnt[g[0]] || 0) + '</b><span class="l">' + g[0] + '</span></button>';
  }).join('') + '</div>';
  if(L.error && L.stale) h += '<div class="v3-cap" style="margin:10px 4px 0">Показана сохранённая версия: сервер сейчас не ответил</div>';
  h += '<div class="v3-search" style="margin-top:14px">' + navIcon('search', 18) + '<input type="text" placeholder="Найти: касса, найм, клиника" aria-label="Поиск по гайдам" value="' + v2esc(window._gdQ || '') + '" oninput="v14search(this.value)"></div>';
  h += '<div class="v9-chips v12-chips2">' + [['all', 'Любой уровень', 0]].concat(V12_LV().map(function(l){ return [l[0], l[1], lcnt[l[0]] || 0]; })).map(function(c){
    return '<button class="' + (lv === c[0] ? 'on' : '') + '" onclick="v14setLevel(\'' + c[0] + '\')">' + c[1] + (c[0] !== 'all' ? ' <span class="num" style="opacity:.55">' + c[2] + '</span>' : '') + '</button>';
  }).join('') + '</div>';
  h += '<div id="gdList">' + v14listHTML() + '</div>';
  el.innerHTML = h;
}

// ── Ридер ──
function v14tbl(t, cls, chg){
  if(!t || !Array.isArray(t.head) || !Array.isArray(t.rows)) return '';
  var ci = chg ? t.head.length - 1 : -1;
  return '<div class="v14-tbl' + (cls ? ' ' + cls : '') + '"><table><thead><tr>' + t.head.map(function(c){ return '<th>' + gE(c) + '</th>'; }).join('') + '</tr></thead><tbody>' +
    t.rows.map(function(r){
      return '<tr>' + (r || []).map(function(c, i){
        var neg = i === ci && /^\s*[-\u2212]\s*\d/.test(String(c));
        return '<td' + (i === ci ? ' class="chg' + (neg ? ' neg' : '') + '"' : '') + '>' + gE(c) + '</td>';
      }).join('') + '</tr>';
    }).join('') + '</tbody></table></div><div class="v14-tblh">Таблицу можно листать вбок →</div>';
}
// Точка Б: строки «показатель · до → после · изменение» карточками, без горизонтальной прокрутки
function v14epTbl(t){
  if(!t || !Array.isArray(t.head) || !Array.isArray(t.rows)) return '';
  if(t.head.length !== 4) return v14tbl(t, 'ep', true);
  return '<div class="v14-epl" role="table"><div class="hd" role="row"><span>' + gE(t.head[0]) + '</span><span>' + gE(t.head[1]) + ' → ' + gE(t.head[2]) + '</span><span>' + gE(t.head[3]) + '</span></div>' + t.rows.map(function(r){
    r = r || [];
    var neg = /^\s*[-\u2212]\s*\d/.test(String(r[3] || ''));
    return '<div class="r" role="row"><div class="m"><b>' + gE(r[0]) + '</b><div class="ba"><span class="b">' + gE(r[1]) + '</span><span class="ar">→</span><span class="a">' + gE(r[2]) + '</span></div></div>' +
      '<div class="chg' + (neg ? ' neg' : '') + '">' + gE(r[3]) + '</div></div>';
  }).join('') + '</div>';
}
function v14cover(x, g, s){
  var o = (g && g.organ) || (x && x.organ) || '', id = (g && g.id) || x.id;
  var title = (g && g.title) || x.title, sub = (g && g.subtitle) || x.subtitle;
  var time = (g && g.time) || (x && x.time) || '', level = (g && g.level) || (x && x.level) || '';
  var nsteps = g ? g.steps.length : (x ? x.steps.length : 0);
  var total = (v14items().length || 99);
  var h = '<header class="v14-cover" id="gdCover"><div class="bgn num" aria-hidden="true">' + v14no2(id) + '</div>' +
    '<div class="k"><span class="o">' + v12oIcon(o, 14, 2) + v2esc(o) + '</span><span class="num">№\u00a0' + v14no2(id) + '\u00a0/\u00a0' + total + '</span></div>' +
    '<h1>' + gE(title) + '</h1>' + (sub ? '<p class="sub">' + gE(sub) + '</p>' : '') +
    '<div class="meta">' + (time ? '<span class="v12-chip">' + navIcon('cal', 13, 2) + gE(v14time(time)) + '</span>' : '') + v12lvl(level) +
      (nsteps ? '<span class="v12-chip num">' + nsteps + ' ' + v3plural(nsteps, 'шаг', 'шага', 'шагов') + '</span>' : '') +
      (x && x.existing ? '<span class="v12-chip">PDF</span>' : '') + '</div>';
  if(s && s.n) h += '<div class="v14-cprog" id="gdCProg">' + v14cprog(s.d, s.n) + '</div>';
  return h + '</header>';
}
function v14cprog(d, n){
  var p = n ? Math.round(d / n * 100) : 0;
  return '<div class="r"><span>Чек-листы</span><b class="num">' + d + ' из ' + n + '</b></div><div class="bar"><i style="width:' + p + '%"></i></div>';
}
function v14pdfBtns(x){
  var kb = x && x.pdfKB ? (x.pdfKB >= 1024 ? (x.pdfKB / 1024).toFixed(1).replace('.', ',') + '\u00a0МБ' : x.pdfKB + '\u00a0КБ') : '';
  return '<div class="v14-pdf">' +
    '<button class="v14-b1" id="gdSend" onclick="v14send()">' + V14_SEND_ICON + '<span>Получить PDF в Telegram</span></button>' +
    '<button class="v14-b2" onclick="v14pdfOpen()">' + V14_PDF_ICON + '<span>Открыть PDF</span></button>' +
    '<div class="v14-pdfc">Бот пришлёт файл в чат' + (kb ? ' · PDF, ' + kb : '') + '</div></div>';
}
function v14cta(x, hide){
  var role = navRole();
  var h = '<div class="v14-cta" id="gdCta"' + (hide ? ' style="display:none"' : '') + '><div class="k">ВНЕДРЕНИЕ С ТРЕКЕРОМ</div><div class="t">Внедрить это в твоём бизнесе</div>';
  if(role === 'resident'){
    h += '<div class="s">Принесите гайд на разбор: трекер посмотрит ваши отметки и поможет довести шаги до результата.</div>' +
      '<button onclick="showPage(\'myboard\')">Обсудить на разборе с трекером</button>';
  } else {
    h += '<div class="s">60 минут с трекером: найдём, где этот гайд даст деньги первым, и соберём план на 10 дней.</div>' +
      '<button onclick="v13book()">Записаться на разбор</button>';
  }
  return h + '</div>';
}
function v14more(x){
  var more = v14items().filter(function(y){ return y.organ === x.organ && y.id !== x.id; });
  var k = more.findIndex(function(y){ return y.id > x.id; }); if(k > 0) more = more.slice(k).concat(more.slice(0, k));
  more = more.slice(0, 3);
  if(!more.length) return '';
  return '<div class="v2-h" style="margin-top:26px">Ещё: ' + v2esc(x.organ) + ' <span class="lnk" onclick="v14openCat(\'' + v2esc(x.organ) + '\')">Все</span></div><div class="v2-card">' + more.map(function(y){
    var s = v14stat(y);
    return '<div class="v12-mini" onclick="v14open(\'' + v2esc(y.id) + '\')"><div class="ic">' + v12oIcon(y.organ, 18) + '</div><div class="m"><b>' + gE(y.title) + '</b><span>' + gE(v14heroLine(y.hero) || v14time(y.time)) + (s.d ? ' · ' + Math.round(s.d / s.n * 100) + '%' : '') + '</span></div><div style="color:#6E6E6E">' + v3icon('chev', 16) + '</div></div>';
  }).join('') + '</div>';
}
function v14item(n, i, t, on){
  return '<div class="v14-it' + (on ? ' on' : '') + '" id="gdIt' + n + '_' + i + '" role="checkbox" tabindex="0" aria-checked="' + on + '" onclick="v14tick(' + n + ',' + i + ')"><div class="cb">' + (V3_CHECK || '') + '</div><div class="tx">' + gE(t) + '</div></div>';
}
function v14stepCnt(s, a){ return s.checklist.filter(function(t, i){ return a.indexOf(s.n + ':' + i) >= 0; }).length; }
function renderGuide(){
  var el = document.getElementById('guideContent');
  if(!el) return;
  var id = v14curId();
  var x = v14find(id);
  if(!x && !(v14data() || {}).at) loadGuides(false);
  // PDF-гайд: только обложка и кнопки
  if(x && x.existing){ el.innerHTML = v14pdfOnly(x); v14bar(null); return; }
  var G = v14guide(id);
  if(!G || !G.g){
    var gl = v14F()[id];
    if(gl && gl.error && !gl.g){
      el.innerHTML = (x ? v14cover(x, null, null) : '') + '<div class="v2-card v12-empty" style="margin-top:12px">' + (gl.error === 'not_found' ? 'Гайд не найден.' : 'Не получилось загрузить гайд. Проверьте связь.') + '<br>' +
        '<button class="v3-outline" style="margin-top:12px" onclick="delete v14F()[\'' + v2esc(id) + '\'];renderGuide()">Повторить</button>' +
        '<button class="v3-outline" style="margin-top:8px" onclick="showPage(\'guides\')">Все гайды</button></div>';
      v14bar(null);
      return;
    }
    el.innerHTML = (x ? v14cover(x, null, null) : '') + '<div class="loading"><div class="spinner"></div></div>';
    v14bar(null);
    loadGuide(id);
    return;
  }
  // Есть кэш: показываем сразу, свежую версию тянем в фоне, если индекс сменил версию
  var ver = (v14data() || {}).version || '';
  if(G.cached && bsInitData() && (!G.v || (ver && G.v !== ver))) loadGuide(id).then(function(r){ if(r && r.g && !r.error && v9activeId() === 'guide' && v14curId() === id) v14keep(renderGuide); });
  var g = G.g;
  x = x || {id: g.id, organ: g.organ, title: g.title, subtitle: g.subtitle, time: g.time, level: g.level, steps: g.steps.map(function(s){ return s.title; }), hero: g.hero, items: v14keys(g).length};
  var a = v14prog(id), keys = v14keys(g), d = keys.filter(function(k){ return a.indexOf(k) >= 0; }).length, n = keys.length;
  var hero = g.hero, me = g.method, ep = g.epilogue;
  var h = '<article class="v14" id="gdArt">' + v14cover(x, g, {d: d, n: n});
  if(g.promise) h += '<div class="v14-promise"><div class="k">ЧТО ВЫ ПОЛУЧИТЕ</div><p>' + gE(g.promise) + '</p></div>';
  // Пролог
  h += '<section class="v14-sec" id="gdA"><div class="v14-kick"><span>Пролог</span><i></i><span>Точка А</span></div>';
  if(hero.name){
    h += '<div class="v14-who"><div class="av">' + v2esc(String(hero.name).charAt(0)) + '</div><div class="m"><b>' + gE(hero.name) + (hero.age ? ', <span class="num">' + v2esc(hero.age) + '</span>' : '') + '</b>' +
      '<span>' + gE([hero.business, hero.city].filter(Boolean).join(' · ')) + '</span></div></div>';
  }
  (hero.story || []).forEach(function(p, i){ h += '<p class="v14-p' + (i === 0 ? ' first' : '') + '">' + gE(p) + '</p>'; });
  if(Array.isArray(hero.before) && hero.before.length){
    h += '<div class="v14-before"><div class="hd">Точка А в цифрах</div><div class="gr">' + hero.before.map(function(b){
      return '<div><b>' + gE(b.value) + '</b><span>' + gE(b.label) + '</span></div>';
    }).join('') + '</div></div>';
  }
  h += '<div class="v14-note">История собирательная, цифры типичные для разборов BS</div></section>';
  // Метод
  if(me.thesis || (me.stats && me.stats.length)){
    h += '<section class="v14-sec" id="gdM"><div class="v14-kick"><span>Метод</span></div>';
    if(me.thesis) h += '<p class="v14-thesis">' + gE(me.thesis) + '</p>';
    if(Array.isArray(me.stats) && me.stats.length) h += '<div class="v14-stats">' + me.stats.map(function(s){ return '<div><b class="num">' + gE(s.value) + '</b><span>' + gE(s.label) + '</span></div>'; }).join('') + '</div>';
    if(me.condition) h += '<div class="v14-cond"><div class="k">КОМУ ПОДХОДИТ</div><p>' + gE(me.condition) + '</p></div>';
    h += '</section>';
  }
  // Оглавление
  h += '<nav class="v14-toc" id="gdToc"><div class="hd"><span>План</span><span class="num">' + g.steps.length + ' ' + v3plural(g.steps.length, 'шаг', 'шага', 'шагов') + '</span></div>' + g.steps.map(function(s){
    var c = s.checklist.length, cd = v14stepCnt(s, a);
    return '<div class="r" onclick="v14go(\'gdS' + s.n + '\')"><span class="n num">' + (s.n < 10 ? '0' : '') + s.n + '</span><span class="t">' + gE(s.title) + '</span>' +
      (c ? '<span class="c num' + (cd >= c ? ' ok' : '') + '" id="gdTc' + s.n + '">' + (cd >= c ? (V3_CHECK || '') : cd + '/' + c) + '</span>' : '<span class="c">' + v3icon('chev', 14) + '</span>') + '</div>';
  }).join('') + '<div class="r ep" onclick="v14go(\'gdB\')"><span class="n">Б</span><span class="t">Точка Б: что изменилось</span><span class="c">' + v3icon('chev', 14) + '</span></div></nav>';
  // Шаги
  g.steps.forEach(function(s){
    h += '<section class="v14-step" id="gdS' + s.n + '"><div class="v14-sn"><span class="big num">' + (s.n < 10 ? '0' : '') + s.n + '</span><span class="of">Шаг ' + s.n + ' из ' + g.steps.length + '</span></div>' +
      '<h2>' + gE(s.title) + '</h2>' + (s.lead ? '<p class="v14-lead">' + gE(s.lead) + '</p>' : '');
    s.body.forEach(function(p){ h += '<p class="v14-p">' + gE(p) + '</p>'; });
    if(s.formula) h += '<div class="v14-formula"><div class="k">ФОРМУЛА</div><div class="f">' + gE(s.formula) + '</div></div>';
    if(s.table) h += v14tbl(s.table);
    if(s.checklist.length){
      var cd = v14stepCnt(s, a);
      h += '<div class="v14-cl"><div class="hd"><span>Чек-лист шага</span><b class="num" id="gdSc' + s.n + '">' + cd + '/' + s.checklist.length + '</b></div>' +
        s.checklist.map(function(t, i){ return v14item(s.n, i, t, a.indexOf(s.n + ':' + i) >= 0); }).join('') + '</div>';
    }
    if(s.case) h += '<div class="v14-case"><div class="k">КЕЙС' + (hero.name ? ' · ' + v2esc(String(hero.name).toUpperCase()) : '') + '</div><p>' + gE(s.case) + '</p></div>';
    if(s.quote && s.quote.text) h += '<blockquote class="v14-q"><p>' + gE(s.quote.text) + '</p>' + (s.quote.author ? '<cite>' + gE(s.quote.author) + '</cite>' : '') + '</blockquote>';
    h += '</section>';
  });
  // Точка Б
  h += '<section class="v14-sec v14-ep" id="gdB"><div class="v14-kick"><span>Эпилог</span><i></i><span>Точка Б</span></div>' +
    '<h2 class="v14-h2">' + (hero.name ? 'Что изменилось у ' + gE(v14gen(hero.name)) : 'Что изменилось') + '</h2>';
  if(ep.table) h += v14epTbl(ep.table);
  if(ep.quote) h += '<blockquote class="v14-q big"><p>' + gE(ep.quote) + '</p>' + (hero.name ? '<cite>' + gE(hero.name) + (hero.business ? ', ' + gE(hero.business) : '') + '</cite>' : '') + '</blockquote>';
  if(Array.isArray(ep.takeaways) && ep.takeaways.length){
    h += '<div class="v14-take"><div class="hd">Главное</div>' + ep.takeaways.map(function(t, i){ return '<div class="r"><span class="n num">' + (i + 1) + '</span><p>' + gE(t) + '</p></div>'; }).join('') + '</div>';
  }
  h += '</section>';
  h += '<div id="gdDone">' + (n && d >= n ? v13doneHTML(x) : '') + '</div>';
  h += v14cta(x, n && d >= n) + v14pdfBtns(x);
  if(navRole() === 'resident' && n) h += '<div class="v13-seen">' + navIcon('me', 13, 2) + 'Трекер видит ваш прогресс по чек-листам</div>';
  h += '<button class="v12-reset" id="gdReset" onclick="v14resetProg()" style="' + (d ? '' : 'display:none') + '">Сбросить отметки</button>';
  if(G.error && G.stale) h += '<div class="v3-cap" style="margin:12px 4px 0;text-align:center">Сохранённая версия: сервер сейчас не ответил</div>';
  h += v14more(x) + '</article>';
  el.innerHTML = h;
  v14tblMark();
  v14gTouch(id);
  v14bar(x, d, n);
}
function v14tblMark(){ document.querySelectorAll('#gdArt .v14-tbl').forEach(function(t){ t.classList.toggle('ovf', t.scrollWidth > t.clientWidth + 4); }); }
// Родительный падеж имени героя для заголовка «Что изменилось у …»
function v14gen(name){
  var s = String(name || '').trim();
  if(/[ая]$/.test(s)) return s.slice(0, -1) + (/[гкхжчшщ]а$/.test(s) ? 'и' : (/я$/.test(s) ? 'и' : 'ы'));
  if(/ь$/.test(s)) return s.slice(0, -1) + 'я';
  if(/й$/.test(s)) return s.slice(0, -1) + 'я';
  if(/[бвгджзклмнпрстфхцчшщ]$/.test(s)) return s + 'а';
  return s;
}
function v14pdfOnly(x){
  var h = '<article class="v14 v14-po" id="gdArt">' + v14cover(x, null, null);
  h += '<div class="v14-pobtn">' + v14pdfBtns(x) + '</div>';
  if(x.promise) h += '<div class="v14-promise"><div class="k">ВНУТРИ</div><p>' + gE(x.promise) + '</p></div>';
  var hl = v14heroLine(x.hero);
  if(hl) h += '<div class="v14-who solo"><div class="av">' + v2esc(String(x.hero.name).charAt(0)) + '</div><div class="m"><b>История героя</b><span>' + gE(hl) + '</span></div></div>';
  h += '<div class="v14-ponote">' + V14_PDF_ICON + '<span>Этот гайд выходит в формате PDF: полная версия с примерами и таблицами. Удобно читать с телефона и переслать команде.</span></div>';
  h += v14cta(x) + v14more(x) + '</article>';
  return h;
}

// ── Липкая панель: название, шаг, отметки и сколько прочитано ──
function v14bar(x, d, n){
  var f = document.getElementById('gdBar');
  if(!f){
    f = document.createElement('div'); f.id = 'gdBar'; f.className = 'v14-bar';
    document.body.appendChild(f);
    window.addEventListener('scroll', v14onScroll, {passive: true});
    window.addEventListener('resize', v14onScroll, {passive: true});
  }
  if(!x){ f.innerHTML = ''; f.classList.remove('show'); return; }
  f.innerHTML = '<button class="bk" onclick="v9goBack()" aria-label="Назад">' + v3icon('back', 20, 2.2) + '</button>' +
    '<div class="m"><div class="tt">' + gE(x.title) + '</div><div class="sb" id="gdBarSub"></div></div>' +
    '<div class="pc num" id="gdBarPct">0%</div><div class="ln"><i id="gdBarLn"></i></div>';
  f._d = d || 0; f._n = n || 0;
  v14onScroll();
}
function v14barSync(){ v14onScroll(); }
function v14onScroll(){
  var f = document.getElementById('gdBar'); if(!f) return;
  var art = document.getElementById('gdArt'), cov = document.getElementById('gdCover');
  var act = v9activeId() === 'guide' && art && f.innerHTML;
  if(!act){ f.classList.remove('show'); return; }
  var r = art.getBoundingClientRect(), vh = window.innerHeight;
  var span = Math.max(1, r.height - vh * 0.6);
  var p = Math.max(0, Math.min(100, Math.round(-r.top / span * 100)));
  f.classList.toggle('show', !!(cov && cov.getBoundingClientRect().bottom < 0));
  var pc = document.getElementById('gdBarPct'); if(pc) pc.textContent = p + '%';
  var ln = document.getElementById('gdBarLn'); if(ln) ln.style.width = p + '%';
  // Текущий раздел
  var sub = document.getElementById('gdBarSub');
  if(sub){
    var cur = '';
    document.querySelectorAll('#gdArt .v14-step, #gdArt .v14-sec').forEach(function(s){ if(s.getBoundingClientRect().top < 130){ var k = s.querySelector('.v14-sn .of, .v14-kick'); cur = k ? [].map.call(k.querySelectorAll('span'), function(z){ return z.textContent.trim(); }).filter(Boolean).join(' · ') || k.textContent.trim() : cur; } });
    sub.textContent = (cur || 'Обложка') + (f._n ? ' · отмечено ' + f._d + ' из ' + f._n : '');
  }
  // Место чтения запоминаем не чаще раза в 400 мс
  var id = v14curId();
  if(id && !window._gdRestoring){
    clearTimeout(window._gdPosT);
    window._gdPosT = setTimeout(function(){ if(v9activeId() === 'guide' && v14curId() === id && document.getElementById('gdArt')) v14posSave(id, window.scrollY, p); }, 400);
  }
}
// Возврат к месту, где остановились
function v14restore(id){
  var pos = v14pos(id);
  if(!pos || pos.y < 400) return;
  window._gdRestoring = true;
  var go = function(){ if(v9activeId() === 'guide' && v14curId() === id && document.getElementById('gdArt')) window.scrollTo(0, pos.y); };
  setTimeout(go, 30); setTimeout(function(){ go(); window._gdRestoring = false; v14onScroll(); if(window.scrollY > 300) showToast('Продолжаем с места, где вы остановились'); }, 120);
}
function v14go(elId){
  var e = document.getElementById(elId); if(!e) return;
  var y = e.getBoundingClientRect().top + window.scrollY - 72;
  try{ window.scrollTo({top: y, behavior: 'smooth'}); }catch(err){ window.scrollTo(0, y); }
}

// ── Отметки ──
function v14tick(sn, i){
  var id = v14curId(), G = v14guide(id); if(!G || !G.g) return;
  var g = G.g, key = sn + ':' + i;
  var a = v14prog(id), k = a.indexOf(key);
  if(k >= 0) a.splice(k, 1); else a.push(key);
  v14progSave(id, a);
  var keys = v14keys(g), n = keys.length, d = keys.filter(function(z){ return a.indexOf(z) >= 0; }).length;
  var it = document.getElementById('gdIt' + sn + '_' + i);
  if(it){ it.classList.toggle('on', k < 0); it.setAttribute('aria-checked', String(k < 0)); }
  var s = g.steps.filter(function(z){ return z.n === sn; })[0];
  if(s){
    var cd = v14stepCnt(s, a), c = s.checklist.length;
    var sc = document.getElementById('gdSc' + sn); if(sc) sc.textContent = cd + '/' + c;
    var tc = document.getElementById('gdTc' + sn); if(tc){ tc.innerHTML = cd >= c ? (V3_CHECK || '') : cd + '/' + c; tc.classList.toggle('ok', cd >= c); }
  }
  var cp = document.getElementById('gdCProg'); if(cp) cp.innerHTML = v14cprog(d, n);
  var f = document.getElementById('gdBar'); if(f){ f._d = d; f._n = n; v14onScroll(); }
  var rs = document.getElementById('gdReset'); if(rs) rs.style.display = d ? '' : 'none';
  var x = v14find(id) || {id: id, title: g.title, organ: g.organ, hero: g.hero};
  var dn = document.getElementById('gdDone'); if(dn) dn.innerHTML = (n && d >= n) ? v13doneHTML(x) : '';
  var ct = document.getElementById('gdCta'); if(ct) ct.style.display = (n && d >= n) ? 'none' : '';
  v13report({id: id, title: g.title, organ: g.organ, done: d, total: n});
  try{
    if(tg && tg.HapticFeedback){
      if(n && d >= n && k < 0) tg.HapticFeedback.notificationOccurred('success'); else tg.HapticFeedback.selectionChanged();
    }
  }catch(e){}
  if(n && d >= n && k < 0) showToast('Гайд пройден: все пункты отмечены');
}
function v14resetProg(){
  var id = v14curId(), G = v14guide(id); if(!G || !G.g) return;
  v14progSave(id, []);
  v14keep(renderGuide);
  v13report({id: id, title: G.g.title, organ: G.g.organ, done: 0, total: v14keys(G.g).length});
}

// ── PDF ──
function v14pdfUrl(id){ return V14_PDF_BASE + encodeURIComponent(id) + '.pdf'; }
function v14pdfOpen(){
  var url = v14pdfUrl(v14curId());
  try{ if(tg && tg.openLink){ tg.openLink(url); return; } }catch(e){}
  window.open(url, '_blank');
}
function v14send(){
  var id = v14curId(), init = bsInitData();
  var b = document.getElementById('gdSend');
  if(!init){ showToast('Откройте приложение в Telegram, чтобы получить PDF'); return; }
  if(b){ if(b.disabled) return; b.disabled = true; b.classList.add('busy'); var l = b.querySelector('span'); if(l) l.textContent = 'Отправляем…'; }
  var done = function(msg, ok){
    showToast(msg);
    var bb = document.getElementById('gdSend');
    if(bb){ bb.disabled = false; bb.classList.remove('busy'); var ll = bb.querySelector('span'); if(ll) ll.textContent = ok ? 'PDF отправлен' : 'Получить PDF в Telegram'; if(ok) bb.classList.add('sent'); }
    try{ if(tg && tg.HapticFeedback) tg.HapticFeedback.notificationOccurred(ok ? 'success' : 'error'); }catch(e){}
  };
  fetch(APP_SERVER + 'guide/' + encodeURIComponent(id) + '/send?' + new URLSearchParams({_tg: init}).toString(), {method: 'POST', headers: {'Content-Type': 'text/plain;charset=utf-8'}, body: '{}'})
    .then(function(r){ return r.json().catch(function(){ return {error: 'http ' + r.status}; }); })
    .then(function(j){
      if(j && j.ok) return done('PDF отправлен в чат с ботом', true);
      var e = String((j && j.error) || '');
      var map = {not_found: 'Гайд не найден', no_pdf: 'PDF этого гайда ещё готовится. Откройте его ниже', unauthorized: 'Откройте приложение заново из Telegram'};
      done(map[e] || (/^http /.test(e) || !e ? 'Не получилось отправить PDF. Попробуйте ещё раз' : e), false);
    })
    .catch(function(){ done('Нет связи. Попробуйте ещё раз', false); });
}

// ── Промо-блоки ──
// Три примера из разных органов, меняются раз в день
function v14examples(k){
  var items = v14items();
  if(!items.length) return [];
  var pref = ['Финансы', 'Продажи', 'Команда', 'Маркетинг', 'Стратегия', 'Процессы', 'Аналитика', 'Собственник'];
  var day = Math.floor(Date.now() / 86400000);
  var out = [];
  for(var j = 0; j < pref.length && out.length < (k || 3); j++){
    var org = pref[(j + day) % pref.length];
    var pool = items.filter(function(x){ return x.organ === org && !x.existing; });
    if(pool.length) out.push(pool[day % pool.length]);
  }
  return out;
}
function v14exMeta(x){ var hl = x.hero && x.hero.name ? v12clean(x.hero.name) + (x.hero.age ? ', ' + x.hero.age : '') : ''; return v2esc(x.organ) + (hl ? ' · история: ' + gE(hl) : ''); }
function v14promo(kind){
  var L = v14data();
  if(!L || !L.at) loadGuides(false);
  var items = (L && L.items) || [];
  var total = items.length || 99;
  var ex = v14examples(3);
  var compact = kind === 'lm';
  var h = '<div class="v12-promo' + (compact ? ' compact' : '') + '" data-v14promo="' + kind + '" onclick="v14openCat()">' +
    '<div class="k"><span>БИБЛИОТЕКА BS</span><span class="free">БЕСПЛАТНО</span></div>' +
    '<div class="hd"><div class="n num">' + total + '</div><div class="t">' + v3plural(total, 'гайд', 'гайда', 'гайдов') + '<br>для вашего бизнеса</div></div>' +
    '<div class="s">Гайды-чек-листы на 10-12 страниц: реальная история с цифрами, метод и шаги, которые можно внедрить за неделю.</div>';
  if(ex.length){
    h += '<div class="ex">' + ex.map(function(x){
      return '<div onclick="event.stopPropagation();v14openFrom(\'' + v2esc(x.id) + '\')"><span class="o">' + v12oIcon(x.organ, 16) + '</span><span class="x">' + gE(x.title) + '<i>' + v14exMeta(x) + '</i></span>' + v3icon('chev', 16) + '</div>';
    }).join('') + '</div>';
  } else {
    h += '<div class="icons">' + V12_ORG().map(function(g){ return '<span>' + v12oIcon(g[0], 16) + '</span>'; }).join('') + '</div>';
  }
  h += '<button onclick="event.stopPropagation();v14openCat()">Открыть ' + total + ' ' + v3plural(total, 'гайд', 'гайда', 'гайдов') + '</button></div>';
  return h;
}
// Карточка в Библиотеке
function v14libFeatured(){
  var L = v14data();
  if(!L || !L.at) loadGuides(false);
  var items = (L && L.items) || [];
  var total = items.length || 99;
  var started = items.filter(function(x){ return v14stat(x).d || v14started(x); }).length;
  return '<div class="v12-promo compact" data-v14feat="1" style="margin-top:16px" onclick="v14openCat()">' +
    '<div class="k"><span>НОВОЕ В БИБЛИОТЕКЕ</span><span class="free">' + (started ? 'В РАБОТЕ ' + started : V12_ORG().length + ' ОРГАНОВ') + '</span></div>' +
    '<div class="hd"><div class="n num">' + total + '</div><div class="t">' + v3plural(total, 'гайд', 'гайда', 'гайдов') + '<br>для вашего бизнеса</div></div>' +
    '<div class="s">История с цифрами, метод, шаги с таблицами и чек-листами. Отмечайте, что внедрили: трекер видит прогресс.</div>' +
    '<div class="icons">' + V12_ORG().map(function(g){ return '<span>' + v12oIcon(g[0], 16) + '</span>'; }).join('') + '</div>' +
    '<button onclick="event.stopPropagation();v14openCat()">Открыть гайды</button></div>';
}

// ═══ R13 → R14: 99 гайдов как главный бесплатный оффер лида, конверсия после гайда, прогресс на сервер ═══
// Примеры для героя: наборы по три из разных органов, наборы сменяются каждые 3.5 с
function v13exampleSets(){
  var items = v14items().filter(function(x){ return !x.existing; });
  if(!items.length) return [];
  var pref = ['Финансы', 'Продажи', 'Команда', 'Маркетинг', 'Стратегия', 'Процессы', 'Аналитика', 'Собственник'];
  var day = Math.floor(Date.now() / 86400000);
  var by = {}; items.forEach(function(x){ (by[x.organ] = by[x.organ] || []).push(x); });
  var flat = [], seen = {};
  for(var r = 0; r < 2; r++) pref.forEach(function(o, j){
    var pool = by[o]; if(!pool || !pool.length) return;
    var x = pool[(day + r * 5 + j) % pool.length];
    if(!seen[x.id]){ seen[x.id] = 1; flat.push(x); }
  });
  var sets = [];
  for(var i = 0; i + 3 <= flat.length && sets.length < 5; i += 3) sets.push(flat.slice(i, i + 3));
  if(!sets.length && flat.length) sets.push(flat);
  return sets;
}
function v13exRows(set){
  return (set || []).map(function(x){
    return '<div class="r" onclick="event.stopPropagation();v14openFrom(\'' + v2esc(x.id) + '\')"><span class="o">' + v12oIcon(x.organ, 16) + '</span><span class="x">' + gE(x.title) + '<i>' + v14exMeta(x) + '</i></span>' + v3icon('chev', 16) + '</div>';
  }).join('');
}
function v13dots(n, k){ var h = ''; for(var i = 0; i < n; i++) h += '<i' + (i === k ? ' class="on"' : '') + '></i>'; return h; }
function v13leadHero(){
  var L = v14data();
  if(!L || !L.at) loadGuides(false);
  var items = (L && L.items) || [];
  var total = items.length || 99;
  var steps = items.reduce(function(s, x){ return s + x.steps.length; }, 0);
  var started = items.filter(function(x){ return v14stat(x).d || v14started(x); }).length;
  var sets = v13exampleSets();
  var k = sets.length ? (window._v13ex || 0) % sets.length : 0;
  var h = '<div class="v13-hero" data-v13hero="1" onclick="v14openCat()">' +
    '<div class="k"><span>BUSINESS SURGERY</span><span class="free">БЕСПЛАТНО</span></div>' +
    '<div class="hd"><div class="n num">' + total + '</div><div class="t">' + v3plural(total, 'гайд', 'гайда', 'гайдов') + '<br>для вашего бизнеса.<br><b>Бесплатно</b></div></div>' +
    '<div class="s">Разборы реальных ситуаций с цифрами: метод, шаги с таблицами и чек-листами. Читаете и сразу внедряете.</div>';
  if(sets.length){
    h += '<div class="ex" id="v13ex" aria-live="polite">' + v13exRows(sets[k]) + '</div>' +
      (sets.length > 1 ? '<div class="dots" id="v13dots">' + v13dots(sets.length, k) + '</div>' : '');
  } else {
    h += '<div class="icons">' + V12_ORG().map(function(g){ return '<span>' + v12oIcon(g[0], 16) + '</span>'; }).join('') + '</div>';
  }
  h += '<button class="go" onclick="event.stopPropagation();v14openCat()">Открыть гайды' + v3icon('chev', 18, 2.4) + '</button>' +
    '<div class="ft">' + (started ? 'В работе у вас: <b class="num">' + started + '</b> ' + v3plural(started, 'гайд', 'гайда', 'гайдов')
      : '<span class="num">' + (steps ? v12num(steps) : '500+') + '</span> шагов · 7 органов и собственник · 3 уровня') + '</div></div>';
  if(sets.length > 1) v13rotStart();
  return h;
}
function v13rotStart(){
  if(window._v13rot) return;
  window._v13rot = setInterval(function(){
    var box = document.getElementById('v13ex');
    if(!box || document.visibilityState !== 'visible' || v9activeId() !== 'about') return;
    var sets = v13exampleSets(); if(sets.length < 2) return;
    window._v13ex = ((window._v13ex || 0) + 1) % sets.length;
    box.classList.add('out');
    setTimeout(function(){
      var b = document.getElementById('v13ex'); if(!b) return;
      b.innerHTML = v13exRows(sets[window._v13ex]);
      var d = document.getElementById('v13dots'); if(d) d.innerHTML = v13dots(sets.length, window._v13ex);
      b.classList.remove('out');
    }, 260);
  }, 3500);
}
function v13diagDone(){ try{ return !!localStorage.getItem('bs_diagnostic_result'); }catch(e){ return false; } }
// Мягкое приглашение в диагностику после 3 начатых гайдов (лид)
function v13softBanner(started){
  if(navRole() !== 'lead' || started < 3) return '';
  var done = v13diagDone();
  return '<div class="v13-soft" onclick="showPage(\'diagnostic\')"><div class="m"><b>' + (done ? 'Сверьте гайды с вашей диагностикой' : 'Вы начали ' + started + ' ' + v3plural(started, 'гайд', 'гайда', 'гайдов') + '. Пора увидеть картину целиком') + '</b>' +
    '<span>' + (done ? 'Результат покажет, какой орган тянет остальные и что внедрять первым' : 'Диагностика: семь органов за 5 минут. Покажем, какой орган тянет остальные и с чего начать') + '</span>' +
    '<em>' + (done ? 'Мой результат' : 'Пройти диагностику') + '</em></div><div class="go">' + v3icon('chev', 18, 2.2) + '</div></div>';
}
// Следующий гайд: сначала тот же орган, затем остальные, пройденные пропускаем
function v13nextCL(x){
  var items = v14items();
  var open = items.filter(function(y){ var s = v14stat(y); return y.id !== x.id && !(s.n && s.d >= s.n); });
  var same = open.filter(function(y){ return y.organ === x.organ; });
  var pick = function(arr){ var a = arr.filter(function(y){ return y.id > x.id; }); return a[0] || arr[0] || null; };
  return pick(same) || pick(open);
}
function v13clNext(){
  var x = v14find(v14curId()); if(!x){ v14openCat(); return; }
  var y = v13nextCL(x);
  if(y) v14open(y.id); else v14openCat(x.organ);
}
function v13book(){
  var x = v14find(v14curId()) || ((v14guide(v14curId()) || {}).g);
  v15book(x ? 'Прочитал гайд «' + x.title + '». Хочу разобрать, как внедрить это в моём бизнесе' : '');
}
function v13leadDoneHTML(x){
  var y = v13nextCL(x);
  return '<div class="v13-done" id="gdConv"><div class="k"><span class="ic">' + (V3_CHECK || '') + '</span>Гайд пройден.</div>' +
    '<div class="t">Следующий шаг: разобрать, как внедрить это в вашем бизнесе</div>' +
    '<div class="s">60 минут с трекером: найдём, где этот гайд даст деньги первым, и соберём план на 10 дней.</div>' +
    '<button class="b1" onclick="v13book()">Записаться на разбор</button>' +
    '<button class="b2" onclick="v13clNext()"><span class="l">Следующий гайд</span>' + (y ? '<span class="n">' + gE(y.title) + '</span>' : '<span class="n">Все гайды: ' + v2esc(x.organ) + '</span>') + '</button></div>';
}
function v13resDoneHTML(x){
  var y = v13nextCL(x);
  return '<div class="v13-done" id="gdConv"><div class="k"><span class="ic">' + (V3_CHECK || '') + '</span>Гайд пройден.</div>' +
    '<div class="t">Все пункты отмечены. Закрепите результат на разборе</div>' +
    '<div class="s">Трекер видит отметки и поможет довести внедрение до цифр в точке Б.</div>' +
    '<button class="b1" onclick="showPage(\'myboard\')">Обсудить на разборе с трекером</button>' +
    '<button class="b2" onclick="v13clNext()"><span class="l">Следующий гайд</span>' + (y ? '<span class="n">' + gE(y.title) + '</span>' : '<span class="n">Все гайды: ' + v2esc(x.organ) + '</span>') + '</button></div>';
}
function v13doneHTML(x){ return navRole() === 'resident' ? v13resDoneHTML(x) : v13leadDoneHTML(x); }

// Прогресс на сервер: трекер видит, что внедрил резидент; бот приглашает лида после завершения.
// Отправка без ожидания ответа, через 1.5 с после последней отметки, ошибки игнорируем.
window._v13rep = {};
function v13report(b){
  if(!b || !b.id || !bsInitData()) return;
  var R = window._v13rep;
  Object.keys(R).forEach(function(id){ if(id !== b.id) v13flush(id); });
  if(R[b.id]) clearTimeout(R[b.id].t);
  R[b.id] = {b: b, t: setTimeout(function(){ v13flush(b.id); }, 1500)};
}
function v13flush(id){
  var R = window._v13rep, e = R[id];
  if(!e) return;
  clearTimeout(e.t); delete R[id];
  var init = bsInitData(); if(!init) return;
  var b = e.b;
  var body = {id: b.id, title: b.title, organ: b.organ, done: b.done, total: b.total};
  try{
    fetch(APP_SERVER + 'ckprogress?_tg=' + encodeURIComponent(init), {method: 'POST', headers: {'Content-Type': 'text/plain;charset=utf-8'}, body: JSON.stringify(body), keepalive: true}).catch(function(){});
  }catch(err){}
}
document.addEventListener('visibilitychange', function(){
  if(document.visibilityState === 'hidden') Object.keys(window._v13rep || {}).forEach(v13flush);
});

// ══════════ R15: ЗАПИСЬ НА ЭКСПРЕСС-РАЗБОР ══════════
// GET slots → окна; POST book → запись; POST book/cancel → отмена. Время окон всегда по Алматы (+05:00)
var V15_WD = ['ВС', 'ПН', 'ВТ', 'СР', 'ЧТ', 'ПТ', 'СБ'];
var V15_WDL = ['воскресенье', 'понедельник', 'вторник', 'среда', 'четверг', 'пятница', 'суббота'];
var V15_MG = ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня', 'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'];
var V15_PRICE = 50000;
window._bk = window._bk || {st: 'idle', data: null, at: 0, day: '', slotId: '', form: null, busy: false, q: ''};
function v15nb(n){ return v2money(n).replace(/ /g, ' '); }
function v15t(iso){
  var ms = Date.parse(iso);
  if(!isFinite(ms)) return null;
  var d = new Date(ms + 5 * 3600000);
  var y = d.getUTCFullYear(), m = d.getUTCMonth(), dd = d.getUTCDate();
  return {ms: ms, y: y, m: m, d: dd, wd: d.getUTCDay(), hm: String(d.getUTCHours()).padStart(2, '0') + ':' + String(d.getUTCMinutes()).padStart(2, '0'),
    key: y + '-' + String(m + 1).padStart(2, '0') + '-' + String(dd).padStart(2, '0')};
}
function v15todayKey(off){ var t = v15t(new Date(Date.now() + (off || 0) * 86400000).toISOString()); return t.key; }
function v15dateLong(t){ return t ? t.d + ' ' + V15_MG[t.m] : ''; }
function v15end(s){ var t = v15t(s.start); if(!t) return ''; var e = v15t(new Date(t.ms + (Number(s.dur) || 60) * 60000).toISOString()); return e.hm; }
function v15online(s){ return /онлайн|online/i.test(String((s && s.format) || '')); }
function v15badge(s, cls){ return '<span class="v15-badge' + (cls ? ' ' + cls : '') + '"><i></i>' + (v15online(s) ? 'Онлайн' : 'Офлайн') + '</span>'; }
function v15form(){
  var b = window._bk;
  if(!b.form){
    var f = {}; try{ f = JSON.parse(localStorage.getItem('bs_bk_form') || '{}') || {}; }catch(e){}
    b.form = {phone: f.phone || '', niche: f.niche || '', question: ''};
  }
  if(b.q && !b.form.question){ b.form.question = b.q; }
  return b.form;
}
function v15saveForm(){ var f = window._bk.form || {}; try{ localStorage.setItem('bs_bk_form', JSON.stringify({phone: f.phone || '', niche: f.niche || ''})); }catch(e){} }
// Вход в запись из любого места: гайд, диагностика, главная. Вопрос подставляется в форму
function v15book(q){
  var b = window._bk;
  if(q){ b.q = q; if(b.form) b.form.question = q; }
  var cur = v9activeId();
  if(cur === 'leadrazbor'){ renderLeadRazbor(); v15load(); return; }
  showPage('leadrazbor', {stack: navRole() !== 'lead' || (cur !== 'about' && cur !== '')});
}
function v15load(force){
  var b = window._bk;
  if(b.st === 'loading') return;
  if(!force && b.data && Date.now() - b.at < 60000) return;
  var init = bsInitData();
  if(!init){ b.st = 'error'; b.err = 'noinit'; renderLeadRazbor(); return; }
  b.st = b.data ? 'refresh' : 'loading';
  if(!b.data) renderLeadRazbor();
  return fetch(APP_SERVER + 'slots?' + new URLSearchParams({_tg: init}).toString())
    .then(function(r){ if(!r.ok) throw new Error('http ' + r.status); return r.json(); })
    .then(function(j){
      if(!j || typeof j !== 'object' || j.error) throw new Error((j && j.error) || 'bad');
      b.data = j; b.at = Date.now(); b.st = 'ok';
      var fr = v15free();
      if(!fr.some(function(s){ return String(s.id) === b.slotId; })) b.slotId = '';
      if(!fr.some(function(s){ return v15t(s.start).key === b.day; })) b.day = fr.length ? v15t(fr[0].start).key : '';
    })
    .catch(function(e){ b.st = b.data ? 'ok' : 'error'; b.err = String((e && e.message) || 'network'); })
    .then(function(){ if(v9activeId() === 'leadrazbor') renderLeadRazbor(); });
}
function v15free(){
  var d = window._bk.data || {}, now = Date.now(), lim = Date.parse(v15todayKey(21) + 'T23:59:59+05:00');
  return (Array.isArray(d.slots) ? d.slots : []).filter(function(s){
    var t = s && s.id && v15t(s.start); return t && t.ms > now && t.ms <= lim && !s.taken && !s.busy;
  }).sort(function(a, b){ return Date.parse(a.start) - Date.parse(b.start); });
}
function v15price(){ var d = window._bk.data || {}; var p = Number(d.price); return p > 0 ? p : V15_PRICE; }
function v15offerHTML(){
  return '<div class="v15-offer"><div class="t">Разберём одну проблему вашего бизнеса до причины</div>' +
    '<div class="s">Рустам и Береке вдвоём смотрят ваши цифры и находят, где на самом деле болит. Уходите с диагнозом и планом на 10 дней.</div>' +
    '<div class="v15-facts"><div><b>60 мин</b><span>онлайн или офлайн</span></div><div><b>2 трекера</b><span>Рустам и Береке</span></div><div><b>' + v15nb(v15price()) + ' ₸</b><span>оплата через Kaspi</span></div></div></div>' +
    '<div class="v2-h">Что получите</div><div class="v2-card v15-get">' +
    [['Диагноз', 'Причину, а не симптом: какой орган бизнеса тянет вниз остальные'],
     ['План на 10 дней', 'Конкретные задачи с цифрами, которые можно начать завтра'],
     ['Взгляд двух трекеров', '700+ разборов: сразу видно, что сработает именно у вас']].map(function(x, i){
      return '<div class="r"><div class="n num">0' + (i + 1) + '</div><div><b>' + x[0] + '</b><span>' + x[1] + '</span></div></div>';
    }).join('') + '</div>';
}
function v15teamHTML(){
  return '<div class="v2-h">Написать команде</div><div class="v2-card">' + V4_TEAM.map(function(t, i){
    var r = (cache.residents || []).filter(function(x){ return t.re.test(String(x.name || '')) && (x.isAdmin || x.isTeam); })[0];
    return '<div class="v2-row" onclick="v4writeTeam(' + i + ')">' + (r ? v3av(r, 40, true) : '<div class="v3-av" style="width:40px;height:40px;font-size:15px">' + t.name.charAt(0) + '</div>') +
      '<div class="m"><b>' + t.name + '</b><span>' + t.role + '</span></div><button class="v3-btn" onclick="event.stopPropagation();v4writeTeam(' + i + ')">' + navIcon('msg', 14, 2) + 'Написать</button></div>';
  }).join('') + '</div>';
}
function v15pickerHTML(){
  var b = window._bk, fr = v15free();
  var days = {}, order = [];
  fr.forEach(function(s){ var k = v15t(s.start).key; if(!days[k]){ days[k] = []; order.push(k); } days[k].push(s); });
  if(!b.day || !days[b.day]) b.day = order[0];
  var h = '<div class="v2-h" id="bkPick">Выберите время <span class="lnk" style="color:#8C8C8C;font-weight:700;font-size:12px">по времени Алматы</span></div>';
  h += '<div class="v15-days" id="bkDays">' + order.map(function(k){
    var t = v15t(days[k][0].start), n = days[k].length;
    return '<button class="v15-day' + (k === b.day ? ' on' : '') + '" data-day="' + k + '" onclick="v15day(\'' + k + '\')"><span class="w">' + (k === v15todayKey(0) ? 'СЕГОДНЯ' : (k === v15todayKey(1) ? 'ЗАВТРА' : V15_WD[t.wd])) + '</span><span class="d num">' + t.d + '</span><span class="c">' + V3_MON[t.m].toLowerCase() + '</span></button>';
  }).join('') + '</div>';
  var list = days[b.day] || [], t0 = list[0] && v15t(list[0].start);
  h += '<div class="v15-dayhead"><b>' + (t0 ? V15_WDL[t0.wd].charAt(0).toUpperCase() + V15_WDL[t0.wd].slice(1) + ', ' + v15dateLong(t0) : '') + '</b><span>' + list.length + ' ' + v3plural(list.length, 'окно', 'окна', 'окон') + '</span></div>';
  h += '<div class="v15-times" id="bkTimes">' + list.map(function(s){
    return '<button class="v15-time' + (String(s.id) === b.slotId ? ' on' : '') + '" data-slot="' + v2esc(s.id) + '" onclick="v15slot(\'' + v2esc(String(s.id).replace(/'/g, '')) + '\')"><b class="num">' + v15t(s.start).hm + '</b><span>' + (v15online(s) ? 'онлайн' : 'офлайн') + '</span></button>';
  }).join('') + '</div>';
  h += '<div id="bkSel">' + v15selHTML() + '</div>';
  return h;
}
function v15selHTML(){
  var b = window._bk, s = v15free().filter(function(x){ return String(x.id) === b.slotId; })[0];
  if(!s) return '<div class="v3-cap" style="margin:10px 4px 0;font-size:12px">Нажмите на время, чтобы выбрать окно</div>';
  var t = v15t(s.start);
  return '<div class="v2-card v15-sel">' + v15badge(s, 'w') + '<div class="m"><b>' + v15dateLong(t) + ', ' + t.hm + '–' + v15end(s) + '</b><span>' +
    (v15online(s) ? 'Google Meet. Ссылка будет в подтверждении' : v2esc(s.place || 'Офлайн, адрес пришлём в Telegram')) + '</span></div></div>';
}
function v15formHTML(){
  var f = v15form(), b = window._bk, s = v15free().filter(function(x){ return String(x.id) === b.slotId; })[0];
  return '<div class="v2-h">Пара вопросов о вас</div><div class="v2-card v15-form">' +
    '<div class="v15-f" id="bkFPhone"><label for="bkPhone">Телефон</label><input id="bkPhone" type="tel" inputmode="tel" autocomplete="tel" placeholder="+7 7__ ___ __ __" value="' + v2esc(f.phone) + '" oninput="v15phone(this)"><div class="e">Нужен номер из 10 цифр после +7</div></div>' +
    '<div class="v15-f"><label for="bkNiche">Ниша</label><input id="bkNiche" type="text" maxlength="120" placeholder="Например, стоматология, 2 клиники" value="' + v2esc(f.niche) + '" oninput="window._bk.form.niche=this.value;v15saveForm()"></div>' +
    '<div class="v15-f"><label for="bkQ">Главный вопрос</label><textarea id="bkQ" maxlength="600" placeholder="Что хотите решить за 60 минут" oninput="window._bk.form.question=this.value">' + v2esc(f.question) + '</textarea></div>' +
    '<button class="v3-primary v15-submit" id="bkSubmit" onclick="v15submit()"' + (s ? '' : ' disabled') + '>' + v15submitLbl() + '</button>' +
  '</div><div class="v15-note">Оплата ' + v15nb(v15price()) + ' ₸ через Kaspi после записи. Перенести или отменить можно здесь же</div>';
}
function v15submitLbl(){
  var b = window._bk, s = v15free().filter(function(x){ return String(x.id) === b.slotId; })[0];
  if(b.busy) return 'Записываем...';
  if(!s) return 'Выберите время';
  var t = v15t(s.start);
  return 'Записаться на ' + v15dateLong(t) + ', ' + t.hm;
}
function v15day(k){
  var b = window._bk; b.day = k;
  var fr = v15free(); if(!fr.some(function(s){ return String(s.id) === b.slotId && v15t(s.start).key === k; })) b.slotId = '';
  var el = document.getElementById('bkPick'); if(!el){ renderLeadRazbor(); return; }
  var sx = (document.getElementById('bkDays') || {}).scrollLeft || 0;
  var wrap = document.getElementById('bkPickWrap'); wrap.innerHTML = v15pickerHTML();
  var d = document.getElementById('bkDays'); if(d) d.scrollLeft = sx;
  v15syncBtn();
  try{ tg.HapticFeedback.selectionChanged(); }catch(e){}
}
function v15slot(id){
  var b = window._bk; b.slotId = String(id);
  document.querySelectorAll('#bkTimes .v15-time').forEach(function(x){ x.classList.toggle('on', x.getAttribute('data-slot') === id); });
  var sel = document.getElementById('bkSel'); if(sel) sel.innerHTML = v15selHTML();
  v15syncBtn();
  try{ tg.HapticFeedback.selectionChanged(); }catch(e){}
}
function v15syncBtn(){
  var btn = document.getElementById('bkSubmit'); if(!btn) return;
  var b = window._bk, ok = v15free().some(function(x){ return String(x.id) === b.slotId; });
  btn.disabled = !ok || b.busy; btn.textContent = v15submitLbl();
}
// Маска телефона Казахстана: +7 7XX XXX XX XX
function v15phoneFmt(v){
  v = String(v || '');
  var d = v.replace(/\D/g, '');
  if(/^\s*\+7/.test(v)) d = d.slice(1);
  else if(d.length >= 11 && (d.charAt(0) === '7' || d.charAt(0) === '8')) d = d.slice(1);
  else if(d.charAt(0) === '8') d = d.slice(1);
  d = d.slice(0, 10);
  var o = '+7';
  if(d.length) o += ' ' + d.slice(0, 3);
  if(d.length > 3) o += ' ' + d.slice(3, 6);
  if(d.length > 6) o += ' ' + d.slice(6, 8);
  if(d.length > 8) o += ' ' + d.slice(8, 10);
  return o;
}
function v15phoneDigits(v){ return v15phoneFmt(v).replace(/\D/g, ''); }
function v15phone(inp){
  var raw = inp.value;
  var v = raw.replace(/\D/g, '') ? v15phoneFmt(raw) : '';
  if(v !== raw) inp.value = v;
  window._bk.form.phone = v; v15saveForm();
  var w = document.getElementById('bkFPhone'); if(w && v15phoneDigits(v).length === 11) w.classList.remove('bad');
}
function v15post(path, body){
  var init = bsInitData();
  if(!init) return Promise.reject(new Error('Откройте приложение из Telegram'));
  return fetch(APP_SERVER + path + '?' + new URLSearchParams({_tg: init}).toString(), {method: 'POST', headers: {'Content-Type': 'text/plain;charset=utf-8'}, body: JSON.stringify(body)})
    .then(function(r){ return r.json().catch(function(){ return {error: 'http ' + r.status}; }); });
}
function v15submit(){
  var b = window._bk, f = v15form();
  if(b.busy) return;
  var s = v15free().filter(function(x){ return String(x.id) === b.slotId; })[0];
  if(!s){ showToast('Выберите время'); return; }
  var ph = v15phoneDigits(f.phone);
  if(ph.length !== 11){
    var w = document.getElementById('bkFPhone'); if(w) w.classList.add('bad');
    try{ document.getElementById('bkPhone').focus(); }catch(e){}
    showToast('Проверьте телефон'); return;
  }
  b.busy = true; v15syncBtn();
  v15post('book', {slotId: s.id, phone: v15phoneFmt(f.phone), niche: String(f.niche || '').trim(), question: String(f.question || '').trim()})
    .then(function(j){
      b.busy = false;
      if(j && j.ok){
        var bk = j.booking || {};
        b.data = b.data || {};
        if(bk.price) b.data.price = bk.price;
        if(bk.kaspiLink) b.data.kaspiLink = bk.kaspiLink;
        b.data.mine = {slot: bk.slot || s, paid: false, phone: v15phoneFmt(f.phone), niche: f.niche, question: f.question};
        b.data.slots = (b.data.slots || []).filter(function(x){ return x.id !== s.id; });
        b.slotId = ''; b.q = ''; f.question = '';
        try{ tg.HapticFeedback.notificationOccurred('success'); }catch(e){}
        renderLeadRazbor(); v9scrollTop();
        showToast('Вы записаны');
        return;
      }
      var e = (j && j.error) || '';
      if(e === 'taken' || e === 'past'){
        showToast(e === 'taken' ? 'Это время только что заняли. Выберите другое' : 'Это время уже прошло. Выберите другое');
        try{ tg.HapticFeedback.notificationOccurred('error'); }catch(x){}
        b.slotId = ''; v15syncBtn(); v15load(true);
        return;
      }
      if(e === 'already'){ showToast('У вас уже есть запись'); v15load(true); return; }
      throw new Error(e || 'ошибка');
    })
    .catch(function(e){ b.busy = false; v15syncBtn(); showToast('Не получилось записаться: ' + String((e && e.message) || 'нет связи')); });
}
function v15gcal(s){
  var t = v15t(s.start); if(!t) return '';
  var z = function(ms){ return new Date(ms).toISOString().replace(/[-:]/g, '').replace(/\.\d{3}/, ''); };
  var q = new URLSearchParams({action: 'TEMPLATE', text: 'Экспресс-разбор BS', dates: z(t.ms) + '/' + z(t.ms + (Number(s.dur) || 60) * 60000),
    details: 'Рустам и Береке, 60 минут. Возьмите цифры за последние 3 месяца.' + (s.link ? '\n' + s.link : ''), location: v15online(s) ? (s.link || 'Онлайн') : (s.place || '')});
  return 'https://calendar.google.com/calendar/render?' + q.toString();
}
function v15mineHTML(){
  var b = window._bk, d = b.data || {}, m = d.mine || {}, s = m.slot || {}, t = v15t(s.start);
  var h = '<div class="v15-ok" id="bkMine"><div class="st"><div class="l"><span class="ic">' + V3_CHECK + '</span>Вы записаны</div>' + v15badge(s, 'dk') + '</div>';
  if(t){
    h += '<div class="dt">' + v15dateLong(t) + '</div><div class="tm num">' + V15_WDL[t.wd] + ', ' + t.hm + '–' + v15end(s) + ' по Алматы</div>';
  } else h += '<div class="dt" style="font-size:24px">Время уточним</div>';
  if(v15online(s)){
    h += '<div class="pl"><div class="ic" style="color:#000">' + navIcon('cal', 18) + '</div><div class="m">Онлайн, Google Meet. ' + (s.link ? '<a href="#" onclick="event.preventDefault();v9open(\'' + v2esc(String(s.link).replace(/'/g, '')) + '\')">Открыть ссылку на встречу</a>' : 'Ссылку пришлём в Telegram за час до начала') + '</div></div>';
  } else {
    h += '<div class="pl"><div class="ic" style="color:#000">' + navIcon('home', 18) + '</div><div class="m">' + v2esc(s.place || 'Офлайн. Адрес пришлём в Telegram') + '</div></div>';
  }
  if(m.paid){
    h += '<div class="pay"><span>Оплата</span><span class="v15-paid">' + V3_CHECK + 'Оплачено</span></div>';
  } else {
    h += '<div class="pay"><span>К оплате</span><b class="num">' + v15nb(v15price()) + ' ₸</b></div>';
    h += '<button class="b1" id="bkPay" onclick="v15pay()">Оплатить через Kaspi</button>';
  }
  h += '<button class="b2" id="bkCal" onclick="v15cal()">' + navIcon('cal', 17, 2) + 'Добавить в календарь</button>';
  h += '<div class="row2"><button class="b3" id="bkMove" onclick="v15ask(\'move\')">Перенести</button><button class="b3" id="bkCancel" onclick="v15ask(\'cancel\')">Отменить</button></div></div>';
  h += '<div class="v2-h">Что подготовить</div><div class="v2-card v15-prep">' +
    [['Цифры за 3 месяца', 'выручка, расходы, сколько осталось на руках'], ['Один главный вопрос', m.question ? '«' + v2esc(String(m.question).slice(0, 140)) + '»' : 'что хотите решить за 60 минут'], ['Час без отвлечений', 'телефон на беззвучный, рядом блокнот']].map(function(x, i){
      return '<div class="r"><i class="num">0' + (i + 1) + '</i><div><b style="color:#fff">' + x[0] + '.</b> ' + x[1] + '</div></div>';
    }).join('') + '</div>';
  return h;
}
function v15pay(){
  var d = (window._bk.data || {});
  var link = d.kaspiLink || 'https://pay.kaspi.kz/pay/ri6h2lj5';
  try{ if(tg && tg.openLink){ tg.openLink(link); return; } }catch(e){}
  window.open(link, '_blank');
}
function v15cal(){
  var m = (window._bk.data || {}).mine || {}; var u = v15gcal(m.slot || {});
  if(!u){ showToast('Время записи ещё не назначено'); return; }
  try{ if(tg && tg.openLink){ tg.openLink(u); return; } }catch(e){}
  window.open(u, '_blank');
}
// Перенос и отмена: своё окно подтверждения вместо confirm()
function v15ask(kind){
  var old = document.getElementById('v15sheet'); if(old) old.remove();
  var m = document.createElement('div'); m.className = 'add-menu-overlay show'; m.id = 'v15sheet';
  m.onclick = function(e){ if(e.target === m) m.remove(); };
  var mv = kind === 'move';
  m.innerHTML = '<div class="add-menu v15-sheet"><div class="t">' + (mv ? 'Перенести разбор?' : 'Отменить разбор?') + '</div>' +
    '<div class="s">' + (mv ? 'Текущее окно освободится, и вы сразу выберете новое время. Ваши ответы сохранятся.' : 'Окно освободится для других. Если уже оплатили, вернём деньги или перенесём оплату на новую дату: напишите нам.') + '</div>' +
    '<div class="bt">' + (mv ? '<button class="v3-primary" data-ok="1">Выбрать новое время</button>' : '<button class="v15-danger" data-ok="1">Отменить запись</button>') +
    '<button class="v3-outline" data-x="1">' + (mv ? 'Оставить как есть' : 'Не отменять') + '</button></div></div>';
  document.body.appendChild(m);
  m.querySelector('[data-x]').onclick = function(){ m.remove(); };
  var ok = m.querySelector('[data-ok]');
  ok.onclick = function(){
    var b = window._bk, mine = (b.data || {}).mine || {}, s = mine.slot || {};
    ok.disabled = true; ok.textContent = 'Секунду...';
    v15post('book/cancel', {slotId: s.id})
      .then(function(j){
        if(!(j && j.ok)) throw new Error((j && j.error) || 'ошибка');
        m.remove();
        if(mine.question && !(b.form && b.form.question)){ v15form().question = mine.question; }
        b.data.mine = null; b.slotId = '';
        renderLeadRazbor(); v15load(true);
        showToast(mv ? 'Выберите новое время' : 'Запись отменена');
        if(mv) setTimeout(function(){ var p = document.getElementById('bkPick'); if(p) window.scrollTo(0, p.getBoundingClientRect().top + scrollY - 70); }, 400);
      })
      .catch(function(e){ ok.disabled = false; ok.textContent = mv ? 'Выбрать новое время' : 'Отменить запись'; showToast('Не получилось: ' + String((e && e.message) || 'нет связи')); });
  };
}
function v15request(){
  var f = v15form();
  openDiagnosticModal({title: 'Заявка на экспресс-разбор', request: f.question || window._bk.q || '', phone: f.phone, niche: f.niche});
}
function renderLeadRazbor(){
  var el = document.getElementById('leadrazborContent');
  if(!el || typeof V4_TEAM === 'undefined' || !window._bk) return;
  var b = window._bk, d = b.data || {};
  var h = '<div class="v15-head"><div class="k">60 МИНУТ · РУСТАМ И БЕРЕКЕ ВДВОЁМ</div><div class="t">Экспресс-разбор</div></div>';
  if(d.mine && d.mine.slot){
    h += v15mineHTML() + v15teamHTML();
    el.innerHTML = h; return;
  }
  h += v15offerHTML();
  if(b.st === 'loading' || b.st === 'idle'){
    h += '<div class="v2-h">Выберите время</div><div style="display:flex;gap:8px;overflow:hidden">' + [1, 2, 3, 4, 5].map(function(){ return '<div class="v15-sk" style="width:60px;height:74px;flex-shrink:0"></div>'; }).join('') + '</div>' +
      '<div class="v15-times" style="margin-top:44px">' + [1, 2, 3].map(function(){ return '<div class="v15-sk" style="height:58px"></div>'; }).join('') + '</div>';
  } else if(b.st === 'error' && !b.data){
    h += '<div class="v2-h">Выберите время</div><div class="v2-card v15-empty" id="bkErr"><div class="ic">' + navIcon('cal', 24) + '</div><b>Не получилось загрузить окна</b>' +
      '<span>Проверьте интернет и попробуйте ещё раз. Или оставьте заявку: подберём время в Telegram.</span>' +
      '<div class="bt"><button class="v3-primary" onclick="v15load(true)">Повторить</button><button class="v3-outline" onclick="v15request()">Оставить заявку</button></div></div>';
  } else if(!v15free().length){
    h += '<div class="v2-h">Выберите время</div><div class="v2-card v15-empty" id="bkNone"><div class="ic">' + navIcon('cal', 24) + '</div><b>Свободных окон пока нет</b>' +
      '<span>Новые окна открываем каждую неделю. Напишите нам или оставьте заявку, подберём время лично.</span>' +
      '<div class="bt"><button class="v3-primary" onclick="v4writeTeam(0)">Написать нам</button><button class="v3-outline" onclick="v15request()">Оставить заявку</button></div></div>';
  } else {
    h += '<div id="bkPickWrap">' + v15pickerHTML() + '</div>' + v15formHTML();
  }
  h += v15teamHTML();
  el.innerHTML = h;
  // Выбранный день виден в ленте
  try{ var on = el.querySelector('.v15-day.on'), dy = document.getElementById('bkDays'); if(on && dy && on.offsetLeft > dy.clientWidth - 70) dy.scrollLeft = on.offsetLeft - 16; }catch(e){}
}

// ══════════ R15: ПРИГЛАСИТЬ ПРЕДПРИНИМАТЕЛЯ ══════════
// GET referral → {link, text, invited[], bonusTotal, bonusPaid}. Без сервера: ссылка с меткой строится на месте
window._ref = window._ref || {st: 'idle', data: null, at: 0};
var V15_STAGE = {'new': 'Новый', razbor: 'На разборе', resident: 'Резидент', lost: 'Отказ'};
var V15_BONUS = 100000;
function v15refFallback(){
  var id = user && user.id ? String(user.id) : '';
  var link = 'https://t.me/bsurgery_bot?start=ref_' + id;
  return {link: link, text: 'Привет! Я в Business Surgery: каждые 10 дней разбираем бизнес с трекерами и считаем цифры. Это не курс. Начни с экспресс-разбора своего бизнеса, ссылка ниже', invited: [], bonusTotal: 0, bonusPaid: 0, local: true};
}
function v15ref(){ var r = window._ref; return r.data || v15refFallback(); }
function v15refLoad(force){
  var r = window._ref;
  if(r.st === 'loading') return;
  if(!force && r.data && Date.now() - r.at < 60000) return;
  var init = bsInitData();
  if(!init){ r.st = 'error'; v15renderRef(); return; }
  r.st = 'loading';
  return fetch(APP_SERVER + 'referral?' + new URLSearchParams({_tg: init}).toString())
    .then(function(x){ if(!x.ok) throw new Error('http ' + x.status); return x.json(); })
    .then(function(j){ if(!j || !j.link) throw new Error('bad'); r.data = j; r.at = Date.now(); r.st = 'ok'; })
    .catch(function(){ r.st = 'error'; })
    .then(function(){ if(v9activeId() === 'referral') v15renderRef(); if(v9activeId() === 'home'){ var c = document.getElementById('v15homeRef'); if(c) c.outerHTML = v15homeCard(); } });
}
function v15share(){
  var d = v15ref();
  var u = 'https://t.me/share/url?url=' + encodeURIComponent(d.link) + '&text=' + encodeURIComponent(d.text || '');
  try{ if(tg && tg.openTelegramLink){ tg.openTelegramLink(u); return; } }catch(e){}
  window.open(u, '_blank');
}
function v15copy(text, ok){
  var done = function(){ showToast(ok || 'Скопировано'); try{ tg.HapticFeedback.notificationOccurred('success'); }catch(e){} };
  var fb = function(){
    try{
      var ta = document.createElement('textarea'); ta.value = text; ta.setAttribute('readonly', ''); ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
      document.body.appendChild(ta); ta.select(); ta.setSelectionRange(0, text.length);
      var r = document.execCommand('copy'); ta.remove();
      if(r) done(); else showToast('Не получилось скопировать. Нажмите «Поделиться»');
    }catch(e){ showToast('Не получилось скопировать. Нажмите «Поделиться»'); }
  };
  try{ if(navigator.clipboard && navigator.clipboard.writeText){ navigator.clipboard.writeText(text).then(done, fb); return; } }catch(e){}
  fb();
}
function v15copyLink(){ v15copy(v15ref().link, 'Ссылка скопирована'); }
function v15copyText(){ var d = v15ref(); v15copy((d.text ? d.text + '\n' : '') + d.link, 'Текст со ссылкой скопирован'); }
function v15when(s){
  var t = v15t(s); if(!t){ var m = /^(\d{1,2})\.(\d{1,2})/.exec(String(s || '')); return m ? Number(m[1]) + ' ' + V15_MG[Number(m[2]) - 1] : ''; }
  return t.d + ' ' + V15_MG[t.m];
}
function v15renderRef(){
  var el = document.getElementById('referralContent'); if(!el) return;
  var r = window._ref, d = v15ref();
  var inv = Array.isArray(d.invited) ? d.invited : [];
  var h = '<div class="v15-head"><div class="k">РЕКОМЕНДАЦИЯ</div><div class="t">Пригласить предпринимателя</div></div>';
  h += '<div class="v15-ref"><div class="k">БОНУС ЗА РЕЗИДЕНТА</div><div class="big num">' + v15nb(V15_BONUS) + '<small>₸</small></div>' +
    '<div class="t">за каждого приглашённого, который стал резидентом клуба</div>' +
    '<div class="v15-steps">' +
      '<div class="r"><div class="n">1</div><div class="m"><b>Отправьте ссылку</b> знакомому предпринимателю</div></div>' +
      '<div class="r"><div class="n">2</div><div class="m">Он проходит <b>экспресс-разбор</b> с Рустамом и Береке</div></div>' +
      '<div class="r"><div class="n">3</div><div class="m">Становится резидентом: <b>' + v15nb(V15_BONUS) + ' ₸ вам</b></div></div>' +
    '</div>' +
    '<button class="b1" id="refShare" onclick="v15share()">' + V14_SEND_ICON + 'Поделиться в Telegram</button>' +
    '<button class="b2" id="refCopy" onclick="v15copyLink()">Скопировать ссылку</button></div>';
  h += '<div class="v2-card v15-link"><div class="u" id="refLink">' + v2esc(String(d.link).replace(/^https?:\/\//, '')) + '</div><button class="v3-btn" onclick="v15copyLink()">Копировать</button></div>';
  h += '<div class="v2-h">Готовый текст</div><div class="v2-card"><div class="v15-txt" id="refText">' + v2esc(d.text || '') + '\n<span style="color:#fff;font-weight:700">' + v2esc(d.link) + '</span></div>' +
    '<div class="v15-txtb"><button class="v3-btn" onclick="v15copyText()">Скопировать текст</button></div></div>';
  if(r.st === 'loading' && !r.data){
    h += '<div class="v2-h">Ваши приглашения</div><div class="v15-sk" style="height:76px"></div><div class="v15-sk" style="height:120px;margin-top:10px"></div>';
  } else if(r.st === 'error' && !r.data){
    h += '<div class="v2-h">Ваши приглашения</div><div class="v2-card v15-empty" id="refErr"><b style="margin-top:0">Список не загрузился</b><span>Ссылка и текст работают. Приглашённые появятся здесь, когда связь восстановится.</span>' +
      '<div class="bt"><button class="v3-outline" onclick="v15refLoad(true)">Повторить</button></div></div>';
  } else {
    var res = inv.filter(function(x){ return x.stage === 'resident'; }).length;
    h += '<div class="v2-h">Ваши приглашения</div><div class="v2-card v15-tot" id="refTot">' +
      '<div><div class="l">Приглашено</div><div class="v num">' + inv.length + '</div></div>' +
      '<div><div class="l">Начислено</div><div class="v num">' + v15nb(Number(d.bonusTotal) || 0) + ' ₸</div></div>' +
      '<div><div class="l">Выплачено</div><div class="v num">' + v15nb(Number(d.bonusPaid) || 0) + ' ₸</div></div></div>';
    if(!inv.length){
      h += '<div class="v2-card v15-empty" style="margin-top:10px"><b style="margin-top:0;font-size:16px">Пока никого</b><span>Как только знакомый откроет бота по вашей ссылке, он появится здесь со статусом.</span></div>';
    } else {
      h += '<div class="v2-card" id="refList" style="margin-top:10px">' + inv.map(function(x){
        var st = V15_STAGE[x.stage] ? x.stage : 'new';
        var bn = '';
        if(Number(x.bonus) > 0) bn = '<div class="bn"><b class="num">' + v15nb(x.bonus) + ' ₸</b> · ' + (x.paid ? 'Выплачено' : 'Начислено') + '</div>';
        return '<div class="v15-inv"><div class="v3-av' + (st === 'resident' ? ' inv' : '') + '" style="width:40px;height:40px;font-size:13px">' + v2esc(v3initials(x.name || '?')) + '</div>' +
          '<div class="m"><b>' + v2esc(x.name || 'Без имени') + '</b><span>' + (x.at ? 'Пришёл ' + v15when(x.at) : 'Перешёл по ссылке') + '</span></div>' +
          '<div class="r"><span class="v15-st ' + st + '">' + V15_STAGE[st] + '</span>' + bn + '</div></div>';
      }).join('') + '</div>';
    }
    if(res) h += '<div class="v15-note">Бонус начисляется, когда приглашённый оплатил первый месяц в клубе. Выплата в течение 10 дней</div>';
  }
  el.innerHTML = h;
}
function v15homeCard(){
  var d = window._ref && window._ref.data, inv = d && Array.isArray(d.invited) ? d.invited : [];
  var sub = inv.length ? 'Приглашено ' + inv.length + (Number(d.bonusTotal) > 0 ? ' · начислено ' + v15nb(d.bonusTotal) + ' ₸' : '') : 'Ссылка и готовый текст. Бонус, когда друг станет резидентом';
  return '<div class="v2-card v15-home" id="v15homeRef" onclick="showPage(\'referral\')"><div class="m"><div class="k">ПРИГЛАСИТЬ ПРЕДПРИНИМАТЕЛЯ</div>' +
    '<div class="t"><span class="num">' + v15nb(V15_BONUS) + ' ₸</span> за приглашённого резидента</div><div class="s">' + v2esc(sub) + '</div></div>' +
    '<div class="go">' + navIcon('chev', 18, 2.4) + '</div></div>';
}
