
function onOpen(){
  var ui=SpreadsheetApp.getUi();
  if(bsIsCopy()){
    // Даже в копии оставляем одну кнопку. вдруг метка встала по ошибке
    ui.createMenu("BS").addItem("Снять метку копии","bsClearCopyFlag").addToUi();
    return;
  }
  ui.createMenu("BS")
    .addItem("Починить дебет","bsFixDebet")
    .addItem("Пересчитать PL","bsRebuildPL")
    .addSeparator()
    .addItem("Диагностика бота","bsDiagnose")
    .addItem("Журнал системы", "showSysNotes")
    .addItem("Бот: принимать сообщения через сервер","bsBotViaServer")
    .addItem("Бот: что делает сервер","bsServerShow")
    .addItem("Бот: всё вернуть таблице (аварийно)","bsForceSheet")
    .addItem("Бот: снова доверить серверу","bsTrustServer")
    .addItem("Переподключить бота напрямую (откат)","bsReconnectBot")
    .addItem("Убрать лишние триггеры","bsCleanTriggers")
    .addItem("Переустановить триггеры","bsSetupTriggers")
    .addItem("Обновить скрипт","bsSelfUpdateMenu")
    .addSeparator()
    .addItem("Платформа: обновить список резидентов","bsPushResidentsToPlatform")
    .addItem("Платформа: проверить перенос данных","bsClubImportCheck")
    .addItem("Платформа: перенести данные","bsClubImportNow")
    .addSeparator()
    .addItem("Календарь: открыть доступ","bsFixCalendarAccess")
    .addItem("Перенести расписание в календарь","bsSyncScheduleToCalendar")
    .addItem("Календарь: убрать дубли и собрать офлайн","bsCalRepairMenu")
    .addSeparator()
    .addItem("Снять штрафы за дни простоя бота","bsCancelBlindFines")
    .addToUi();
}

// Снимает штрафы «Не сдан отчёт» за дни, когда бот не получил ни одного отчёта.
// Спрашивает подтверждение и показывает список до удаления
function bsCancelBlindFines(){
  var ui=null; try{ ui=SpreadsheetApp.getUi(); }catch(e){}
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsF=ss.getSheetByName("Штрафы");
  var wsL=ss.getSheetByName("Лог отчётов");
  if(!wsF || wsF.getLastRow()<3){ if(ui)ui.alert("Штрафов нет."); return; }

  // Дни, за которые в логе есть хоть один отчёт
  var daysWithReports={};
  if(wsL && wsL.getLastRow()>1){
    var ll=wsL.getLastRow();
    wsL.getRange(2,1,ll-1,1).getValues().forEach(function(r){
      if(r[0] instanceof Date) daysWithReports[bsDayStr(r[0])]=true;
    });
  }
  // Штрафы за отчёт, выставленные в дни полной тишины
  var blind={}, rowsByDay={};
  for(var r=3;r<=wsF.getLastRow();r++){
    var nm=String(wsF.getRange(r,2).getValue()||"").trim();
    var rs=String(wsF.getRange(r,3).getValue()||"").trim();
    var dt=wsF.getRange(r,5).getValue();
    if(!nm || rs!=="Не сдан отчёт") continue;
    var ds=dt instanceof Date ? bsDayStr(dt) : String(dt).trim();
    if(daysWithReports[ds]) continue;          // в этот день бот что-то слышал. штраф честный
    blind[ds]=(blind[ds]||0)+1;
    (rowsByDay[ds]=rowsByDay[ds]||[]).push(nm);
  }
  var days=Object.keys(blind);
  if(!days.length){ if(ui)ui.alert("Слепых штрафов нет: за каждый оштрафованный день в логе есть отчёты."); return; }

  var preview=days.map(function(d){ return d+" — "+blind[d]+" шт: "+rowsByDay[d].join(", "); }).join("\n");
  var total=days.reduce(function(a,d){return a+blind[d];},0);
  if(ui){
    var ans=ui.alert("Снять "+total+" штрафов?",
      "В эти дни бот не получил НИ ОДНОГО отчёта ни от кого. Значит молчал бот, а не резиденты:\n\n"+
      preview+"\n\nУдалить эти штрафы и пересчитать дебет?", ui.ButtonSet.YES_NO);
    if(ans!==ui.Button.YES) return;
  }
  var removed=bsCancelFinesForDates(days);
  var text="Снято штрафов: "+removed.length+"\n\n"+removed.join("\n");
  if(ui)ui.alert(text);
  try{ bsSysNote("↩️ Сняты штрафы за дни, когда бот не получал сообщений:\n\n"+preview); }catch(e){}
  return text;
}

function forceRefreshApp(){
  // Сбрасывает все кеши, приложение подтянет свежие данные
  try{
    var out = [];
    try{ _invalidateBundleCache(); out.push("Кеш приложения сброшен"); }
    catch(e){ out.push("Кеш приложения: " + e); }
    try{ refreshBotCache(); out.push("Кеш бота обновлён"); }
    catch(e){ out.push("Кеш бота: " + e); }
    try{
      CacheService.getScriptCache().removeAll(["botCache","miniData","bundle_meta_all"]);
      out.push("Общий кеш очищен");
    }catch(e){}

    // Проверяем что данные читаются
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("BS - резиденты дебет");
    if(ws && ws.getLastRow() >= 3){
      var d = ws.getRange(3, 2, ws.getLastRow()-2, 20).getValues();
      var active = 0, offline = 0, online = 0, withChat = 0;
      d.forEach(function(r){
        if(!r[RI.name]) return;
        if(r[RI.former] === "Да") return;
        active++;
        if(String(r[RI.format]) === "Онлайн") online++; else offline++;
        if(String(r[RI.chat]||"").trim()) withChat++;
      });
      out.push("");
      out.push("Активных резидентов: " + active);
      out.push("Офлайн: " + offline + ", онлайн: " + online);
      out.push("С Chat ID: " + withChat);
    }
    return _alert(out.join("\n"));
  }catch(e){ return _alert("Ошибка: " + e); }
}

// BS. BUSINESS SURGERY v21
// Все тексты бота редактируются в листе "Настройки". без кода!
// Webhook: https://script.google.com/macros/s/AKfycbx8TX2nJwAUykLdSUFs34IxgJfxJLSgHrUlR4bhdfrM4kl1Jk9kR_js3qkAXQ1SMiT9Ag/exec
// ═══════════════════════════════════════════════════════════════════════════

var BOT_TOKEN        = "__BS_BOT_TOKEN__";
var ADMIN_ID         = "453800951";
var ADMIN_IDS        = ["453800951","1285596249"]; // Добавьте ID партнёра и ассистента сюда
var SS_ID            = "1D-D4P5G9cmX1tdyluWe88sNTVGGqJqQWA4NvrvTbrYE";
var ORIG_SS_ID       = "1vqF9tCd8TziHu_2FRfgInEztnV-2Y16d0IlY2WigtdU";
var FINE_AMT         = 10000;
var KASPI_LINK       = "https://pay.kaspi.kz/pay/ri6h2lj5";
var WEBHOOK_URL      = "https://script.google.com/macros/s/AKfycbwADG90lTo4KotevW077lKglODs4ftHpVr60O9p98S0b0ptxfnzf0AvHVsORYrpBkcgJQ/exec";
// Адрес, заданный через меню «Переподключить бота», важнее зашитого
try{
  var _bsHook = PropertiesService.getScriptProperties().getProperty("BS_WEBHOOK_URL");
  if(_bsHook) WEBHOOK_URL = _bsHook;
}catch(_e){}
var WEBAPP_BASE_URL  = "https://neuvolen.github.io/bs-app/"; // Mini App
var GROUP_CHAT_ID    = "-1002494126345";

// Текст политики. редактируется здесь (без перезапуска скрипта не обновляется в боте)
var CONTENT_POSTS=[
    {cat:"Маркетинг",text:"🎯 Маркетинг без стратегии. это просто трата денег.\n\nБольшинство предпринимателей думают что маркетинг = реклама.\nНет. Реклама. это усилитель. Усиливает то что есть.\n\nЕсли продукт слабый. реклама ускорит провал.\nЕсли продукт сильный. реклама ускорит рост.\n\nСначала упакуй. Потом усиливай.\n\nВопрос дня: ты знаешь точно почему клиенты выбирают тебя, а не конкурента?"},
    {cat:"Маркетинг",text:"📱 Личный бренд. это не про красивые фото.\n\nЭто про доверие. А доверие строится через последовательность.\n\nПост раз в неделю в течение года > 10 постов в день в течение недели и тишина.\n\nАлгоритм простой:\n  Пиши о том что реально знаешь\n  Показывай процесс, не только результат\n  Будь последователен даже когда не хочется\n\nЧерез год тебя будут знать. Через два. рекомендовать."},
    {cat:"Маркетинг",text:"💬 Самый дешёвый маркетинг. довольный клиент.\n\nОн расскажет троим. Недовольный. десятерым.\n\nПоэтому сервис. это не расходы. Это инвестиция в сарафан.\n\nОдин WOW-момент в работе с клиентом окупается в 10 раз.\nЧто ты делаешь чтобы клиент сказал WOW?"},
    {cat:"Энергия",text:"⚡️ Энергия. главный ресурс предпринимателя. Не деньги. Не время.\n\nМожно иметь миллион рублей и лежать без сил.\nМожно не иметь ничего но гореть и двигаться.\n\nТри вещи которые убивают энергию незаметно:\n  Незакрытые разговоры (долги, конфликты, недосказанность)\n  Дела которые ты делаешь но не должен\n  Люди рядом которые тянут вниз\n\nПроверь своё окружение. Оно либо заряжает, либо сливает."},
    {cat:"Энергия",text:"😴 Сон. это не слабость. Это стратегия.\n\nНедосыпающий предприниматель принимает решения как пьяный.\nЭто не метафора. исследования подтверждают.\n\n6 часов сна в течение двух недель = состояние человека после суток без сна.\n\nХочешь принимать лучшие решения?\nЛожись спать раньше. Серьёзно."},
    {cat:"Фокус",text:"🎯 Фокус. это не про то что делать. Это про то что НЕ делать.\n\nУ тебя 24 часа. Как у всех.\nРазница между теми кто растёт и теми кто стоит на месте. в выборе.\n\nОдна большая задача > десять мелких.\nОдин проект до конца > пять на половине.\n\nСегодня: выбери одно. Сделай до конца."},
    {cat:"Фокус",text:"📵 Телефон утром. это чужие приоритеты вместо своих.\n\nПервый час после пробуждения определяет тональность всего дня.\nЕсли первое что ты делаешь. скроллишь ленту, ты уже проиграл утро.\n\nПопробуй 7 дней: первый час без телефона.\nТолько своя голова, свои мысли, свои задачи.\n\nРезультат удивит."},
    {cat:"Финансы",text:"💰 Выручка. это тщеславие. Прибыль. это реальность.\n\nМногие предприниматели гордятся оборотом.\n«У меня миллион в месяц». и 950к расходов.\n\nСчитай маржу. Считай чистую прибыль.\nСчитай сколько ты берёшь домой.\n\nБизнес должен кормить владельца. Если нет. это дорогое хобби."},
    {cat:"Финансы",text:"📊 Если ты не знаешь свои цифры. ты не управляешь бизнесом. Бизнес управляет тобой.\n\nМинимум что нужно знать каждую неделю:\n  Выручка\n  Расходы\n  Сколько заработал лично\n\nВсё остальное. потом. Но это. обязательно.\n\nЦифры не врут. Ощущения. врут."},
    {cat:"Цели",text:"🏔 Большая цель не мотивирует. Она пугает.\n\nПоэтому большинство её не ставит.\nИли ставит и забывает через неделю.\n\nСекрет прост: разбей на 90 дней.\nЧто конкретно ты сделаешь за 90 дней чтобы приблизиться?\n\nНе «хочу миллион». А «за 90 дней я сделаю X, Y, Z».\n\nКонкретика. это и есть мотивация."},
    {cat:"Цели",text:"📅 Год заканчивается не 31 декабря. Он заканчивается сегодня.\n\nКаждый день ты либо приближаешься к цели, либо отдаляешься.\nНейтрального не существует.\n\nОдин вопрос который стоит задавать каждый вечер:\n«Что я сегодня сделал что приближает меня к цели?»\n\nЕсли ответить нечем. завтра начни иначе."},
    {cat:"Окружение",text:"👥 Покажи мне пятерых с кем ты проводишь больше всего времени. и я скажу кем ты станешь.\n\nЭто не красивая цитата. Это математика.\n\nТы усредняешься по своему окружению автоматически.\nБез усилий. Просто находясь рядом.\n\nХочешь расти. нужно окружение которое уже там где ты хочешь быть."},
    {cat:"Автоматизация",text:"🤖 Если ты делаешь одно и то же больше трёх раз. это нужно автоматизировать.\n\nПредприниматель должен думать, решать, создавать.\nНе выполнять рутину которую может сделать система.\n\nОдин час вложенный в автоматизацию экономит десятки часов в будущем.\n\nЧто ты делаешь каждую неделю руками, что можно автоматизировать?"},
    {cat:"Личное время",text:"⏰ Отдых. это не награда за работу. Это условие продуктивной работы.\n\nПредприниматель который не отдыхает. это машина которую не обслуживают.\nЕдет. До поломки.\n\nПланируй отдых так же жёстко как встречи.\nБлок в календаре. Неприкосновенный.\n\nТы единственный актив в своём бизнесе которого нельзя заменить."},
    {cat:"Личное время",text:"🌱 Личное развитие. это не трата времени. Это умножение всего остального.\n\nЧас чтения в день = 12-15 книг в год.\nЭто больше чем читает 99% предпринимателей.\n\nЗнания которые ты получаешь. конвертируются в решения.\nЛучшие решения = лучшие результаты.\n\nЧто ты читаешь прямо сейчас?"}
];

var ONBOARDING_MSGS=[
  "День 1️⃣\\n\\nПривет! Добро пожаловать в Business Surgery! 🎉\\n\\nЯ бот проекта. В ближайшие 5 дней буду присылать тебе всё что нужно знать чтобы получить максимум.\\n\\nС чего начать прямо сейчас:\\n✅ Напиши первый отчёт сегодня (шаблон: /help)\\n✅ Запиши кружочек о себе во вкладке РЕЗИДЕНТЫ в нашей группе. расскажи кто ты, чем занимаешься и какая твоя главная цель\\n✅ Познакомься с резидентами\\n\\nМы рады что ты с нами! 💪\\n\\nbxclub.kz",
  "День 2️⃣\\n\\nКак писать отчёт чтобы это работало на тебя, а не просто для галочки.\\n\\nОтчёт. это не контроль. Это инструмент для тебя самого.\\n\\nКогда ты фиксируешь:\\n  Что сделал\\n  Что не получилось\\n  Что завтра\\n\\nТы начинаешь видеть паттерны. Где теряешь время. Где растёшь.\\n\\nШаблон: /help\\n\\nВажно: отчёт до 23:59. За пропуск. штраф 10 000 тг.",
  "День 3️⃣\\n\\nТрекинг встреча. главный инструмент проекта.\\n\\nКак подготовиться:\\n1. Запиши 3 главных задачи которые хочешь разобрать\\n2. Принеси цифры: выручка, расходы, конверсии\\n3. Будь готов говорить честно. здесь нет места для красивых историй\\n\\nВстречи каждые 10 дней. Следи за расписанием в группе.\\n\\nОпоздание = штраф 10 000 тг.",
  "День 4️⃣\\n\\nИстория одного резидента.\\n\\nОн зашёл в проект с выручкой 80к в месяц.\\nЧерез 3 месяца. 380к.\\n\\nЧто изменилось? Не волшебная таблетка.\\nПросто: ежедневные отчёты + честный разбор на трекинге + окружение которое требует роста.\\n\\nТы уже в правильном месте.\\nОстальное. твои действия. 💪",
  "День 5️⃣\\n\\nТы уже 5 дней в системе. Это больше чем делают многие.\\n\\nНапоминание о главном:\\n📝 Отчёт каждый день до 23:59\\n🗓 Трекинг встреча каждые 10 дней\\n💰 Штраф за пропуск: 10 000 тг\\n\\nЕсли есть вопросы или трудности. мы здесь. Пиши куратору или в группу.\\n\\nРады что ты с нами. Давай сделаем результат! 🚀\\n\\nbxclub.kz"
];

var POLICY_TEXT = "📋 Перед использованием сервиса необходимо ознакомиться с документами:\n\n📄 Публичная оферта. https://bxclub.kz/oferta\n🔒 Политика конфиденциальности. https://bxclub.kz/privacy\n\nНажимая кнопку «✅ Принять и продолжить», вы:\n• подтверждаете ознакомление с условиями Оферты\n• принимаете условия Договора\n• даете согласие на обработку персональных данных\n• соглашаетесь с электронным способом заключения договора\n• подтверждаете, что ваши действия являются аналогом собственноручной подписи";
const OFERTA_VERSION = "1.0";
const PRIVACY_VERSION = "1.0";



var REPORTS_TOPIC_ID = "9"; // топик ОТЧЕТЫ
var IMPORTANT_TOPIC_ID = "1980"; // топик ВАЖНОЕ
var USEFUL_TOPIC_ID = "2"; // топик ПОЛЕЗНОЕ
var ANTHROPIC_API_KEY = "вставьте_сюда"; // не используется
var GEMINI_API_KEY = "вставьте_gemini_ключ"; // aistudio.google.com

var BLK="#000000",WHT="#FFFFFF",G1="#F5F5F5",G2="#E8E8E8",GX="#8C8C8C",BRD="#D0D0D0";
var RED="#CC0000",GRN="#1A7A1A",GRN_BG="#E8F5E9",RED_BG="#FFEBEE";

// ── Утилиты ──────────────────────────────────────────────────────────────
function H(r,bg,fg,sz){
  if(!r)return;
  r.setBackground(bg||BLK).setFontColor(fg||WHT).setFontFamily("Montserrat")
   .setFontSize(sz||10).setFontWeight("bold")
   .setHorizontalAlignment("center").setVerticalAlignment("middle").setWrap(true);
}
function B(r){if(!r)return;r.setBorder(true,true,true,true,true,true,BRD,SpreadsheetApp.BorderStyle.SOLID);}
function TITROW(ws,row,nc,txt){
  ws.setRowHeight(row,40);
  ws.getRange(row,1,1,nc).setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
    .setFontSize(12).setFontWeight("bold").setHorizontalAlignment("center").setVerticalAlignment("middle");
  ws.getRange(row,1).setValue(txt);
}
function TITMERGE(r,txt){
  r.merge().setValue(txt).setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
   .setFontSize(12).setFontWeight("bold").setHorizontalAlignment("center").setVerticalAlignment("middle");
}
function dvDate(){return SpreadsheetApp.newDataValidation().requireDate().setAllowInvalid(true).build();}
function toast(t){SpreadsheetApp.getActiveSpreadsheet().toast(t,"🏥 BS",8);}

// ── Чтение текста из листа Настройки ─────────────────────────────────────
// Кэш текстов настроек в памяти скрипта
var _settingsCache = null;

function _loadSettings(){
  if(_settingsCache)return _settingsCache;
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Настройки");
    if(!ws)return {};
    var data=ws.getRange(2,1,ws.getLastRow()-1,2).getValues();
    _settingsCache={};
    for(var i=0;i<data.length;i++){
      if(data[i][0])_settingsCache[String(data[i][0])]=String(data[i][1]);
    }
  }catch(e){Logger.log("_loadSettings: "+e);_settingsCache={};}
  return _settingsCache;
}

// Fallback тексты. если в листе "Настройки" нет ключа, берём отсюда
var FALLBACK_TEXTS = {
  "visit_1": "{имя}, кайфую от первой встречи цикла. Главное. внедрить то что решили. Жду тебя на следующей встрече с прогрессом",
  "visit_2": "{имя}, прошла половина цикла. Если что-то идёт не по плану, лучше написать сейчас. корректнее завернём в следующем разборе",
  "visit_3": "{имя}, третья встреча прошла. Финишная прямая по этому циклу. Подумай заранее с какими цифрами и результатами придёшь к финалу",
  "visit_4": "{имя}, четвёртая встреча. Цикл завершается. Если есть что докрутить. пиши прямо сейчас, не откладывай",
  "visit_complete_3": "🎉 {имя}, поздравляю с завершением цикла из 3 встреч!\n\nТы с нами уже {месяцев}. Это много, и каждый цикл даёт результат.\n\nПродление: {сумма} ₸\nKaspi: {kaspi}\n\nБудем рады отметке в Instagram @business.surgery и рекомендации твоим знакомым 🤝",
  "visit_complete_4": "🎉 {имя}, поздравляю с завершением цикла из 4 встреч!\n\nТы с нами уже {месяцев}. Это много, и каждый цикл даёт результат.\n\nПродление: {сумма} ₸\nKaspi: {kaspi}\n\nБудем рады отметке в Instagram @business.surgery и рекомендации твоим знакомым 🤝"
};

function getText(key, vars){
  try{
    var settings=_loadSettings();
    var txt=settings[key];
    if(!txt) txt=FALLBACK_TEXTS[key]; // если в Настройках нет. берём fallback
    if(!txt)return null;
    if(vars){
      Object.keys(vars).forEach(function(k){
        txt=txt.replace(new RegExp("{"+k+"}","g"),vars[k]);
      });
    }
    return txt;
  }catch(e){Logger.log("getText: "+e);}
  return null;
}

// ── Telegram API ──────────────────────────────────────────────────────────
function tgSend(chatId, text, extra){
  if(!chatId||!BOT_TOKEN)return false;
  try{
    var payload={chat_id:chatId, text:text, disable_web_page_preview:true};
    if(extra) Object.keys(extra).forEach(function(k){payload[k]=extra[k];});
    var resp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
      method:"post", contentType:"application/json",
      payload:JSON.stringify(payload), muteHttpExceptions:true
    });
    var res=JSON.parse(resp.getContentText());
    if(!res.ok) Logger.log("tgSend error: "+resp.getContentText());
    return res.ok;
  }catch(e){Logger.log("tgSend exception: "+e);return false;}
}

// Отправка с inline-кнопкой
function tgSendButton(chatId, text, buttonText, buttonUrl){
  return tgSend(chatId, text, {
    reply_markup: JSON.stringify({
      inline_keyboard:[[{text:buttonText, url:buttonUrl}]]
    })
  });
}

// Отправка с кнопками "Принимаю" (callback)
function tgSendAccept(chatId, text){
  return tgSend(chatId, text, {
    reply_markup: JSON.stringify({
      inline_keyboard:[
        [{text:"📄 Оферта", url:"https://bxclub.kz/oferta"}],
        [{text:"🔒 Политика", url:"https://bxclub.kz/privacy"}],
        [{text:"✅ Принять и продолжить", callback_data:"accept_terms"}]
      ]
    })
  });
}

// ── Защита от дублирования (кэш) ─────────────────────────────────────────
var _processedUpdates = {};
function isProcessed(updateId){
  if(_processedUpdates[updateId]) return true;
  _processedUpdates[updateId]=true;
  return false;
}

// ═══════════════════════════════════════════════════════════════════════════
// ГЛАВНАЯ
// ═══════════════════════════════════════════════════════════════════════════


// Вторая часть. запустите если setupAllSheets завершилась с таймаутом
function setupPart2(){
  deleteAllOldTriggers();
  var ss=SpreadsheetApp.openById(SS_ID);
  try{buildPL(ss);}catch(e){Logger.log("PL: "+e);}
  SpreadsheetApp.flush();
  try{buildSchedule(ss);}catch(e){Logger.log("Sched: "+e);}
  try{buildAnalytics(ss);}catch(e){Logger.log("Analytics: "+e);}
  try{buildContent(ss);}catch(e){Logger.log("Content: "+e);}
  SpreadsheetApp.flush();
  try{buildSettings(ss);}catch(e){Logger.log("Settings: "+e);}
  SpreadsheetApp.flush();
  try{recalcVisitMonths(ss);}catch(e){Logger.log("Months: "+e);}
  setupEditTrigger();
  setupDailyTrigger();
  setupReminderTrigger();
  setupScheduleTrigger();
  setupMonthsTrigger();
  setupNPSTrigger();
  setupBackupTrigger();
  setupStartReminderTrigger();
  setupWebhookWatchdog();
  setupDataCacheTrigger();
  refreshBotCache();
  try{setupContentTrigger();}catch(e){}
  try{setupOnboardingTrigger();}catch(e){}
  try{setupMeetCheckTrigger();}catch(e){}
  // Порядок вкладок
  ["BS - резиденты дебет","PL","Учет ДДС","Штрафы","BS - посещения","Расписание","Прогноз","Аналитика","Контент","Профили","Настройки","Бывшие резиденты","Лог отчётов"]
    .forEach(function(nm,i){var w=ss.getSheetByName(nm);if(w){ss.setActiveSheet(w);ss.moveActiveSheet(i+1);}});
  addDDSResidentDV();
  toast("✅ Часть 2 завершена!");
}


function rebuildVisits(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsV=ss.getSheetByName("BS - посещения");
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(!wsV||!wsR){buildVisits(ss);recalcVisitMonths(null);return;}
  // Добавляем только тех кого нет в посещениях
  var lrR=wsR.getLastRow();
  var added=0;
  for(var r=3;r<=lrR;r++){
    var nm=wsR.getRange(r, RC.name).getValue();if(!nm)continue;
    if(wsR.getRange(r, RC.former).getValue()==="Да")continue;
    _addResidentToVisits(nm);
    added++;
  }
  // Прогноз считается в блоке unit-экономики ниже
  recalcVisitMonths(null);
  toast("✅ Проверено "+added+" резидентов. Новые добавлены в Посещения. Существующие галочки сохранены!");
}

function updateStructure(){
  var ss=SpreadsheetApp.openById(SS_ID);
  // СНАЧАЛА удаляем ВСЕ старые триггеры. обязательно ПЕРЕД созданием новых
  deleteAllOldTriggers();
  // Пересоздаём листы
  // Пересоздаём только расчётные листы. С данными не трогаем
  var SAFE_TO_REBUILD=["Аналитика","Дашборд","Прогноз","Рекомендации"];
  SAFE_TO_REBUILD.forEach(function(n){
    var w=ss.getSheetByName(n);if(w)try{ss.deleteSheet(w);}catch(e){}
  });
  // Настройки и Лог отчётов содержат данные, их не удаляем
  Logger.log("updateStructure: пересозданы только расчётные листы");
  // PL не пересоздаётся. данные сохраняются
  try{buildSettings(ss);}catch(settingsErr){Logger.log("buildSettings error: "+settingsErr);}
  SpreadsheetApp.flush();
  buildLog(ss);
  buildForecast(ss);
  buildAnalytics(ss);
  buildRecommendations(ss);
  buildContent(ss);
  // Создаём триггеры. каждый только ОДИН раз
  setupEditTrigger();
  setupDailyTrigger();
  setupReminderTrigger();
  setupScheduleTrigger();
  setupMonthsTrigger();
  setupNPSTrigger();
  setupBackupTrigger();
  setupStartReminderTrigger();
  setupWebhookWatchdog();
  setupContentTrigger();
  try{setupAutoContentTrigger();}catch(e){}
  setupAutoContentTrigger();
  setupAutoRecsTrigger();
  setupOnboardingTrigger();
  setupMeetCheckTrigger();
  setupDataCacheTrigger();
  addDDSResidentDV();
  try{reorderSheets();}catch(re){}
  toast("✅ Структура обновлена! Данные сохранены, триггеры пересозданы.");
}



function reorderSheets(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var order=["BS - резиденты дебет","PL","Учет ДДС","Штрафы","BS - посещения","Расписание","Прогноз","Аналитика","Контент","Профили","Настройки","Рекомендации","Бывшие резиденты","Лог отчётов"];
  for(var i=0;i<order.length;i++){
    var w=ss.getSheetByName(order[i]);
    if(w){
      ss.setActiveSheet(w);
      ss.moveActiveSheet(i+1);
    }
  }
  toast("✅ Порядок вкладок исправлен!");
}

function addCyclesColumns(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("BS - резиденты дебет");
  if(!ws){toast("Лист Резиденты не найден");return;}
  // Проверяем. есть ли уже Q (col 17)
  var headerQ=ws.getRange(2, RC.granted).getValue();
  if(headerQ&&String(headerQ).indexOf("Циклов")>=0){
    toast("Столбцы Q/R уже существуют");
    return;
  }
  // Добавляем заголовки в строку 2
  ws.getRange(2, RC.granted).setValue("Циклов\nоплачено")
    .setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold")
    .setFontFamily("Montserrat").setFontSize(10).setHorizontalAlignment("center")
    .setVerticalAlignment("middle").setWrap(true);
  ws.getRange(2, RC.done).setValue("Циклов\nисп.")
    .setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold")
    .setFontFamily("Montserrat").setFontSize(10).setHorizontalAlignment("center")
    .setVerticalAlignment("middle").setWrap(true);
  ws.setColumnWidth(17,80);
  ws.setColumnWidth(18,80);

  // Заполняем для всех существующих резидентов
  var lr=ws.getLastRow();
  for(var r=3;r<=lr;r++){
    var name=ws.getRange(r, RC.name).getValue();
    if(!name)continue;
    ws.getRange(r, RC.granted).setValue(1).setNumberFormat("0").setBackground("#FFF9C4")
      .setHorizontalAlignment("center");
    ws.getRange(r, RC.done).setValue(0).setNumberFormat("0")
      .setHorizontalAlignment("center");
  }
  toast("✅ Столбцы Q (циклов оплачено) и R (использовано) добавлены!\n\nЗаполните Q вручную: 1. помесячная оплата, 3. на 3 мес, 12. год");
}

function setupAllSheets(){
  var ss=SpreadsheetApp.openById(SS_ID);
  ["BS - резиденты дебет","PL","Учет ДДС","Штрафы","BS - посещения","Расписание","Прогноз","Аналитика","Контент","Профили","Настройки","Бывшие резиденты","Лог отчётов"].forEach(function(n){
    var w=ss.getSheetByName(n);if(w)try{ss.deleteSheet(w);}catch(e){}
  });
  buildFines(ss);
  SpreadsheetApp.flush();
  // buildResidents(ss); // отключено: портит структуру
  buildDDS(ss);
  SpreadsheetApp.flush();
  // buildPL(ss); // отключено: ломает структуру
  buildSchedule(ss);
  buildVisits(ss);
  buildFormer(ss);
  SpreadsheetApp.flush();
  try{
    buildSettings(ss);
  }catch(settingsErr){
    Logger.log("buildSettings error: "+settingsErr);
    toast("⚠️ Настройки не созданы. запустите 🔄 Обновить структуру");
  }
  SpreadsheetApp.flush();
  buildLog(ss);
  ["BS - резиденты дебет","PL","Учет ДДС","Штрафы","BS - посещения","Расписание","Прогноз","Аналитика","Контент","Профили","Настройки","Бывшие резиденты","Лог отчётов"]
    .forEach(function(nm,i){var w=ss.getSheetByName(nm);if(w){ss.setActiveSheet(w);ss.moveActiveSheet(i+1);}});
  deleteAllOldTriggers();
  setupEditTrigger();
  setupDailyTrigger();
  setupReminderTrigger();
  setupScheduleTrigger();
  setupMonthsTrigger();
  setupNPSTrigger();
  setupBackupTrigger();
  setupStartReminderTrigger();
  setupWebhookWatchdog();
  setupContentTrigger();
  setupAutoContentTrigger();
  setupAutoRecsTrigger();
  setupOnboardingTrigger();
  setupScheduleTrigger();
  setupMeetCheckTrigger();
  toast("✅ Листы созданы!\n\nДальше:\n1. 📋 Скопировать ОДДС\n2. 📊 Обновить PL\n3. 🤖 Webhook\n1. Развернуть → Веб-приложение → Доступ: Все\n2. 🤖 Webhook\n3. 📋 Скопировать ОДДС\n4. Тексты. лист 'Настройки'");
}

// ── Удаление ВСЕХ старых триггеров ───────────────────────────────────────

// ── Уведомление резидента о Цене слова ───────────────────────────────────


function recalcVisitMonths(ssParam){
  var ss=ssParam||SpreadsheetApp.openById(SS_ID);
  var wsV=ss.getSheetByName("BS - посещения");
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(!wsV||!wsR)return;
  var today=new Date();
  var lrV=wsV.getLastRow();
  var lrR=wsR.getLastRow();
  if(lrR<3||lrV<3)return;
  var resData=wsR.getRange(3, 2, lrR-2, 20).getValues();
  var dateMap={};
  for(var i=0;i<resData.length;i++){
    var nm=resData[i][0];
    var dt=resData[i][7];
    if(nm&&dt instanceof Date&&dt.getFullYear()>2000) dateMap[nm]=dt;
  }
  for(var r=3;r<=lrV;r++){
    var name=wsV.getRange(r,2).getValue();
    if(!name){wsV.getRange(r,8).setValue("");continue;}
    var dateIn=dateMap[name];
    if(!dateIn){wsV.getRange(r,8).setValue("");continue;}
    var months=(today.getFullYear()-dateIn.getFullYear())*12+(today.getMonth()-dateIn.getMonth());
    wsV.getRange(r,8).setValue(months>=0?months:0);
  }
}
function _sendAdminMenu(cid){
  tgSend(cid,"🏥 BS. Панель управления",{
    reply_markup:JSON.stringify({inline_keyboard:[
      [{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl()}}],
      [{text:"📋 Кто сдал отчёт сегодня",callback_data:"admin_today_reports"}],
      [{text:"📅 Расписание на месяц",callback_data:"admin_schedule"}],
      [{text:"🗓 Поставить расписание",callback_data:"sched_step1"}],
      [{text:"🎬 Мероприятие",callback_data:"event_step1"}],
      [{text:"⚠️ Выставить штраф",callback_data:"fine_step1"}],
      [{text:"💰 Приход/Расход",callback_data:"payment_step1"}],
      [{text:"📊 Дебет",callback_data:"admin_debet"}]
    ]})
  });
}
function _sendAnalytics(cid){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsF=ss.getSheetByName("Штрафы");
  var wsL=ss.getSheetByName("Лог отчётов");
  var wsDDS=ss.getSheetByName("Учет ДДС");
  if(!wsR)return;

  var lr=wsR.getLastRow();
  var totalRes=0,totalDebt=0,totalFines=0,noChat=0,online=0,offline=0,admins=0;
  for(var r=3;r<=lr;r++){
    var nm=wsR.getRange(r, RC.name).getValue();if(!nm)continue;
    if(wsR.getRange(r, RC.former).getValue()==="Да")continue;
    totalRes++;
    totalDebt+=Number(wsR.getRange(r, RC.total).getValue())||0;
    totalFines+=Number(wsR.getRange(r, RC.fine).getValue())||0;
    if(!wsR.getRange(r, RC.chat).getValue())noChat++;
    var fmt=String(wsR.getRange(r, RC.format)?wsR.getRange(r, RC.format).getValue():"Офлайн");
    if(fmt==="Онлайн")online++;else offline++;
    if(wsR.getRange(r, RC.admin)&&wsR.getRange(r, RC.admin).getValue()==="Да")admins++;
  }

  // Отчёты за последние 7 дней
  var reportsByDay={};
  if(wsL){
    var ll=wsL.getLastRow();
    for(var r=2;r<=ll;r++){
      var ld=wsL.getRange(r,1).getValue();
      if(!ld)continue;
      var d=new Date(ld);
      var now=new Date();
      if((now-d)>7*24*3600*1000)continue;
      var dayKey=Utilities.formatDate(d,"Asia/Almaty","dd.MM");
      reportsByDay[dayKey]=(reportsByDay[dayKey]||0)+1;
    }
  }
  var reportTrend=Object.keys(reportsByDay).sort().map(function(k){return k+":"+reportsByDay[k];}).join(" ");

  // Доходы/расходы за текущий месяц
  var monthInc=0,monthExp=0;
  var curMonth=new Date().getMonth()+1;
  var curYear=new Date().getFullYear();
  if(wsDDS){
    var ld2=wsDDS.getLastRow();
    if(ld2>1){
      var ddsData=wsDDS.getRange(2,1,ld2-1,3).getValues();
      ddsData.forEach(function(row){
        if(!row[0]||!(row[0] instanceof Date))return;
        var m=parseInt(Utilities.formatDate(row[0],"Asia/Almaty","M"));
        var y=parseInt(Utilities.formatDate(row[0],"Asia/Almaty","yyyy"));
        if(m===curMonth&&y===curYear){
          monthInc+=Number(row[1])||0;
          monthExp+=Number(row[2])||0;
        }
      });
    }
  }
  var monthProfit=monthInc-monthExp;

  // Топ должников
  var debtors=[];
  for(var r=3;r<=lr;r++){
    var nm=wsR.getRange(r, RC.name).getValue();if(!nm)continue;
    if(wsR.getRange(r, RC.former).getValue()==="Да")continue;
    var d=Number(wsR.getRange(r, RC.total).getValue())||0;
    if(d>0)debtors.push({name:nm,debt:d});
  }
  debtors.sort(function(a,b){return b.debt-a.debt;});
  var topDebtors=debtors.slice(0,3).map(function(x){return x.name.split(" ")[0]+": "+x.debt.toLocaleString()+" тг";}).join("\n");

  var msg="📈 АНАЛИТИКА Business Surgery\n\n";
  msg+="👥 Резидентов: "+totalRes+" ("+offline+" офлайн, "+online+" онлайн)\n";
  msg+="💰 Общий долг: "+totalDebt.toLocaleString()+" тг\n";
  msg+="⚠️ Штрафы к оплате: "+totalFines.toLocaleString()+" тг\n";
  if(noChat>0)msg+="❗ Без Chat ID: "+noChat+" чел\n";
  msg+="\n📅 Этот месяц:\n";
  msg+="  Доход: "+monthInc.toLocaleString()+" тг\n";
  msg+="  Расход: "+monthExp.toLocaleString()+" тг\n";
  msg+="  Прибыль: "+monthProfit.toLocaleString()+" тг\n";
  if(topDebtors){msg+="\n🔴 Топ должников:\n"+topDebtors+"\n";}
  if(reportTrend){msg+="\n📝 Отчёты (7 дней): "+reportTrend+"\n";}
  msg+="\n💡 Рекомендации:\n";
  if(totalFines>50000)msg+="  • Провести сбор штрафов ("+totalFines.toLocaleString()+" тг)\n";
  if(noChat>0)msg+="  • Попросить "+noChat+" резидентов написать /start боту\n";
  if(online>0&&offline>0)msg+="  • Проверить расписание онлайн-встреч ("+online+" онлайн-резидентов)\n";
  if(monthProfit<0)msg+="  • Расходы превышают доходы в этом месяце!\n";

  tgSend(cid||ADMIN_ID, msg, {
    reply_markup:JSON.stringify({inline_keyboard:[[{text:"◀️ Назад в меню",callback_data:"admin_menu"}]]})
  });
}

function _adminAddFine(cid, rawText){
  if(bsMasterRefuse(cid)) return;
  // /штраф Асет опоздание 10000
  var parts=rawText.trim().split(/\s+/);
  if(parts.length<4){
    tgSend(cid,"❌ Формат: /штраф [Имя] [Тип] [Сумма]\nПример: /штраф Асет опоздание 10000");
    return;
  }
  var name=parts[1];
  var typeRaw=parts[2].toLowerCase();
  var amount=Number(parts[3].replace(/[^0-9]/g,""));
  if(!amount){tgSend(cid,"❌ Сумма должна быть числом");return;}
  // Маппинг типа
  var typeMap={"отчет":"Не сдан отчёт","отчёт":"Не сдан отчёт","опоздание":"Опоздание",
               "слово":"Цена слова","нарушение":"Нарушение правил","пропуск":"Пропуск посещения"};
  var fineType=typeMap[typeRaw]||"Прочее";
  // Ищем полное имя в таблице
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var fullName=name;
  if(wsR){
    var lr=wsR.getLastRow();
    for(var r=3;r<=lr;r++){
      var nm=wsR.getRange(r, RC.name).getValue();
      if(nm&&nm.toLowerCase().indexOf(name.toLowerCase())>=0){fullName=nm;break;}
    }
  }
  // Добавляем штраф
  var wsF=ss.getSheetByName("Штрафы");
  if(!wsF){tgSend(cid,"❌ Лист Штрафы не найден");return;}
  var fRow=_findFreeRow(wsF,3,2);
  wsF.getRange(fRow,2).setValue(fullName);
  wsF.getRange(fRow,3).setValue(fineType);
  wsF.getRange(fRow,4).setValue(amount).setNumberFormat("#,##0");
  wsF.getRange(fRow,5).setValue(new Date()).setNumberFormat("DD.MM.YYYY");
  wsF.getRange(fRow,6).setValue("Не оплатил");
  _renumFines();
  recalcResidents();
  // Уведомляем резидента
  var chatId=_getChatId(fullName);
  if(chatId){
    var msg=getText("fine_reminder",{"имя":fullName.split(" ")[0],"дата":Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy"),"kaspi":KASPI_LINK});
    if(!msg)msg=fullName.split(" ")[0]+": штраф "+amount.toLocaleString()+" тг ("+fineType+"). Оплатить: "+KASPI_LINK;
    tgSendButton(chatId,msg,"Оплатить",KASPI_LINK);
  }
  tgSend(cid,"✅ Штраф выставлен:\n👤 "+fullName+"\n💰 "+amount.toLocaleString()+" тг\n📋 "+fineType+(chatId?"\n✉️ Резидент уведомлён":"\n⚠️ Chat ID не найден"));
  // Если цена слова. дополнительное уведомление
  if(fineType==="Цена слова") _notifyWordPrice(fRow);
}

function _getResidentMonths(name){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("BS - резиденты дебет");if(!ws)return "";
    var lr=ws.getLastRow();
    for(var r=3;r<=lr;r++){
      if(ws.getRange(r,2).getValue()===name){
        var dateIn=ws.getRange(r,9).getValue();
        if(!dateIn||!(dateIn instanceof Date))return "";
        var today=new Date();
        var months=(today.getFullYear()-dateIn.getFullYear())*12+(today.getMonth()-dateIn.getMonth());
        var days=Math.floor((today-dateIn)/(1000*60*60*24));
        return days+"д ("+months+"мес)";
      }
    }
  }catch(e){}
  return "";
}

function _notifyWordPrice(row){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsFines=ss.getSheetByName("Штрафы");if(!wsFines)return;
    var name=wsFines.getRange(row,2).getValue();
    if(!name)return;
    Utilities.sleep(500);
    var amount=wsFines.getRange(row,4).getValue();
    var note=wsFines.getRange(row,6)?wsFines.getRange(row,6).getValue():"";
    var chatId=_getChatId(name);
    var amtStr=amount?(Number(amount).toLocaleString()+" тг"):"";
    var msg=getText("word_price_msg",{"имя":name.split(" ")[0],"сумма":amtStr,"kaspi":KASPI_LINK});
    if(!msg)msg=name.split(" ")[0]+": Цена слова "+amtStr+". Оплатить: "+KASPI_LINK;
    if(chatId)tgSendButton(chatId,msg,"Оплатить",KASPI_LINK);
    ADMIN_IDS.forEach(function(id){
      tgSend(id,"Штраф/Цена слова: "+name+" - "+amtStr+(note?" ("+note+")":""));
    });
  }catch(e){Logger.log("_notifyWordPrice: "+e);}
}


function addDDSResidentDV(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsDDS=ss.getSheetByName("Учет ДДС");
  if(!wsR||!wsDDS)return;
  var lrR=Math.max(wsR.getLastRow(),3);
  var dvRes=SpreadsheetApp.newDataValidation()
    .requireValueInRange(wsR.getRange("B3:B"+lrR),true)
    .setAllowInvalid(true).build();
  wsDDS.getRange(2,5,500,1).setDataValidation(dvRes);
  Logger.log("DV ОДДС добавлен");
}

function hideDDSRowsBefore(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Учет ДДС");if(!ws)return;
  var cutoff=new Date(2026,3,1); // 1 апреля 2026
  var lr=ws.getLastRow();
  var toHide=[];
  for(var r=2;r<=lr;r++){
    var dt=ws.getRange(r,1).getValue();
    if(dt instanceof Date&&dt<cutoff)toHide.push(r);
  }
  if(toHide.length>0){
    // Скрываем группами для скорости
    ws.hideRows(toHide[0],toHide[toHide.length-1]-toHide[0]+1);
    toast("✅ Скрыто строк до 01.04.2026: "+toHide.length);
  } else {
    toast("Строк до 01.04.2026 не найдено");
  }
}

function deleteAllOldTriggers(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    ScriptApp.deleteTrigger(t); // удаляем ВСЕ
  });
}

// ═══════════════════════════════════════════════════════════════════════════
// ЛИСТ НАСТРОЙКИ. все тексты бота редактируются здесь
// ═══════════════════════════════════════════════════════════════════════════
function buildSettings(ss){
  var ws=ss.getSheetByName("Настройки");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Настройки", ss.getNumSheets());
  ws.setTabColor("#1A3A5C");ws.setFrozenRows(2);
  ws.setRowHeight(1,40);
  TITMERGE(ws.getRange("A1:B1"),"\u2699\uFE0F НАСТРОЙКИ БОТА. редактируйте только колонку Б");
  ws.setRowHeight(2,36);
  ws.getRange(2,1).setValue("Ключ").setBackground(G2).setFontWeight("bold").setHorizontalAlignment("center").setVerticalAlignment("middle");
  ws.getRange(2,2).setValue("Текст сообщения (редактируйте здесь)").setBackground(G2).setFontWeight("bold").setHorizontalAlignment("left").setVerticalAlignment("middle");
  B(ws.getRange(2,1,1,2));
  ws.setColumnWidth(1,220);ws.setColumnWidth(2,700);

  // Все данные одним массивом. намного быстрее
  var data=[
    ["── ПРИВЕТСТВИЕ ──",""],
    ["start_policy","📋 ПРАВИЛА «Business Surgery»\n\n🧬 Приветствую резиденты проекта «Business Surgery»\n🔔 На этом канале вы будете получать важные анонсы, расписание, полезные материалы.\n\n☑️ ПРАВИЛА:\n1. Опоздание. 10.000 тенге\n2. Несдача отчёта (до 00.00). 10.000 тенге\n3. Невыполнение заданий (цена слова). 100.000 тенге\n\n☑️ ЦЕННОСТИ:\n▫️ Результативность и эффективность\n▫️ Нет конкуренции. есть поддержка и достигаторство\n▫️ Максимально легко, просто и конкретно\n\nНажмите кнопку ниже ↓"],
    ["start_welcome","✅ Отлично, {имя}! Добро пожаловать в Business Surgery!\n\n📱 Вступите в нашу группу: https://t.me/+M88HVtcZNghjNTM6\n\nКоманды:\n/status. ваш долг и штрафы\n/help. шаблон ежедневного отчёта"],
    ["new_resident_welcome","🎉 Добро пожаловать в Business Surgery, {имя}!\n\n✅ Вы добавлены в систему\n\n📝 Каждый день до 23:59. отчёт в топик ОТЧЕТЫ\n💰 Штраф за пропуск: 10 000 тг\n\n📸 @qabden @erniyazov.bereke @business.surgery"],
    ["── ПОСЕЩЕНИЯ ──",""],
    ["visit_1","✅ {имя}, первая встреча отмечена!\n\nОтличное начало 💪\n\nВы с нами {месяцев} мес.\n\n📸 Отметьте нас в Instagram:\n@qabden @erniyazov.bereke @business.surgery"],
    ["visit_2","✅ {имя}, вторая встреча отмечена!\n\nВы на полпути. так держать! 🔥\n\nВы с нами {месяцев} мес.\n\n📸 Отметьте нас: @qabden @erniyazov.bereke @business.surgery"],
    ["visit_3","✅ {имя}, третья встреча отмечена!\n\nОсталась одна встреча до продления! 🎯\n\nВы с нами {месяцев} мес.\n\n📸 Отметьте нас: @qabden @erniyazov.bereke @business.surgery"],
    ["visit_complete_4","🎉 {имя}, все 4 встречи выполнены!\n\n✅ Начислено продление: {сумма} тг\nВы с нами {месяцев} мес.\n\n📸 Отметьте нас: @qabden @erniyazov.bereke @business.surgery"],
    ["visit_complete_3","🎉 {имя}, все 3 встречи по тарифу выполнены!\n\n✅ Начислено продление: {сумма} тг\nВы с нами {месяцев} мес.\n\n📸 Отметьте нас: @qabden @erniyazov.bereke @business.surgery"],
    ["── ШТРАФЫ И ОТЧЁТЫ ──",""],
    ["fine_reminder","⚠️ {имя}, штраф за несдачу отчёта!\n\nСумма: 10 000 тг\nДата: {дата}\n\nОплатить: {kaspi}"],
    ["report_reminder","⏰ {имя}, напоминание!\n\nДо конца дня немного. не забудьте сдать отчёт!\n\nШтраф за пропуск: 10 000 тг"],
    ["missing_3days","{имя}, мы заметили что вас не было 3 дня подряд...\n\nВсё в порядке? 🤔\n\nМы здесь и всегда готовы помочь! Если возникли трудности. просто напишите, вместе разберёмся 💪\n\nС уважением, команда Business Surgery"],
    ["── ЗАВЕРШЕНИЕ ──",""],
    ["goodbye","{имя}, спасибо за время в Business Surgery! 🙏\n\nЖелаем дальнейших успехов!\n\n📸 @qabden @erniyazov.bereke @business.surgery 🚀"],
    ["word_price_msg","{имя}, вам выставлена ЦЕНА СЛОВА на сумму {сумма}.\nСтарайтесь прикладывать больше усилий для своего будущего! С уважением, команда Business Surgery.\nОплатить: {kaspi}"],
    ["── КОМАНДЫ ──",""],
    ["help_msg","📋 Шаблон ежедневного отчёта Business Surgery:\n\n{дата}\n{имя}\n\nТочка А:\nТочка Б:\n\nЦель: _____ тг\n\nИз старых задач осталось:\n\nЗадачи на 10 дней:\n\nЕжедневные задачи:\n\nОтчёт за день:\n\nПлан на завтра:\n\n❌ Штраф за пропуск: 10 000 тг"],
    ["── РАСПИСАНИЕ ──",""],
    ["offline_address","г.Алматы, Достык 44"],
    ["admin_emails","imbahirr@gmail.com"],
    ["── NPS ──",""],
    ["nps_q1","Что в Business Surgery самое ценное для вас?"],
    ["nps_q2","Что бы вы хотели улучшить или чего вам не хватает?"],
    ["nps_q3","Если бы вы рекомендовали BS другу. чем бы его зацепили?"],
    ["schedule_3days","🗓 {имя}, через 3 дня встреча!\n\n📅 {дата}\n📍 {адрес}"],
    ["schedule_1day","⏰ {имя}, встреча ЗАВТРА!\n\n📅 {дата}\n📍 {адрес}\n\nДо встречи! 💪"],
  ];

  // Batch запись всех данных
  var startRow=3;
  var values=[];
  var rowHeights=[];
  for(var i=0;i<data.length;i++){
    values.push([data[i][0],data[i][1]]);
    rowHeights.push(data[i][0].toString().startsWith("──")?24:70);
  }
  ws.getRange(startRow,1,values.length,2).setValues(values)
    .setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("top").setWrap(true);
  // Стили заголовков секций
  for(var i=0;i<data.length;i++){
    var row=startRow+i;
    ws.setRowHeight(row,rowHeights[i]);
    if(data[i][0].toString().startsWith("──")){
      ws.getRange(row,1,1,2).merge().setBackground(G2).setFontColor(GX).setFontWeight("bold")
        .setFontStyle("italic").setHorizontalAlignment("left");
    } else {
      ws.getRange(row,1).setBackground("#EEF2FF").setFontWeight("bold")
        .setFontFamily("Courier New").setFontSize(9).setHorizontalAlignment("left");
      ws.getRange(row,2).setBackground(WHT).setHorizontalAlignment("left");
    }
    B(ws.getRange(row,1,1,2));
  }
}

function buildFines(ss){
  var ws=ss.getSheetByName("Штрафы");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Штрафы", ss.getNumSheets());
  ws.setTabColor("#CC3333");ws.setFrozenRows(2);
  ws.setRowHeight(1,40);TITMERGE(ws.getRange("A1:F1"),"BS. РЕЕСТР ШТРАФОВ");
  ws.setRowHeight(2,40);
  var heads=["№","Имя резидента","Тип нарушения","Сумма, тг","Дата","Статус"];
  var wids=[40,200,230,130,120,140];
  for(var i=0;i<6;i++){var c=ws.getRange(2,i+1);c.setValue(heads[i]);H(c);B(c);ws.setColumnWidth(i+1,wids[i]);}
  ws.getRange(2,6).setBackground("#1A3A1A").setFontColor("#66FF66");
  var dvTp=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Не сдан отчёт","Цена слова","Опоздание","Нарушение правил","Пропуск посещения","Прочее"],true)
    .setAllowInvalid(false).build();
  var dvSt=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Не оплатил","Оплатил"],true).setAllowInvalid(false).build();
  var DS=3,ROWS=300;
  // Batch стили для Штрафов
  ws.getRange(DS,1,ROWS,6).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
  ws.getRange(DS,1,ROWS,1).setHorizontalAlignment("center").setFontColor(GX);
  ws.getRange(DS,2,ROWS,1).setHorizontalAlignment("left");
  ws.getRange(DS,3,ROWS,1).setDataValidation(dvTp).setHorizontalAlignment("left");
  ws.getRange(DS,4,ROWS,1).setNumberFormat("#,##0").setHorizontalAlignment("center");
  ws.getRange(DS,5,ROWS,1).setNumberFormat("DD.MM.YYYY").setDataValidation(dvDate()).setHorizontalAlignment("center");
  ws.getRange(DS,6,ROWS,1).setDataValidation(dvSt).setHorizontalAlignment("center");
  B(ws.getRange(DS,1,ROWS,6));
  // Чередующиеся цвета
  for(var ri=0;ri<ROWS;ri++) ws.getRange(DS+ri,1,1,6).setBackground(ri%2===0?WHT:G1);
  var rules=[];
  rules.push(SpreadsheetApp.newConditionalFormatRule().whenTextEqualTo("Не оплатил")
    .setBackground(RED_BG).setFontColor(RED).setRanges([ws.getRange("F"+DS+":F"+(DS+ROWS-1))]).build());
  rules.push(SpreadsheetApp.newConditionalFormatRule().whenTextEqualTo("Оплатил")
    .setBackground(GRN_BG).setFontColor(GRN).setRanges([ws.getRange("F"+DS+":F"+(DS+ROWS-1))]).build());
  ws.setConditionalFormatRules(rules);
}

// ═══════════════════════════════════════════════════════════════════════════
// РЕЗИДЕНТЫ
// ═══════════════════════════════════════════════════════════════════════════
function buildResidents(ss){
  var NM="BS - резиденты дебет";
  var ws=ss.getSheetByName(NM);if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet(NM);
  ws.setTabColor(BLK);ws.setFrozenRows(2);ws.setFrozenColumns(2);
  var NC=18;
  TITROW(ws,1,NC,"BS. РЕЗИДЕНТЫ | УЧЁТ ДЕБИТОРСКОЙ ЗАДОЛЖЕННОСТИ");
  ws.getRange(1,1).setValue("").setBackground(BLK); // A1 пустой
  ws.getRange(1,2).setValue("BS. ДЕБЕТ").setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat").setFontSize(11).setFontWeight("bold").setHorizontalAlignment("left").setVerticalAlignment("middle");
  ws.setRowHeight(2,42);
  var heads=["№","Имя резидента","Тариф тг","Встреч\nоплачено","Встреч\nпроведено","Осталось\nвстреч",
             "Оплачено\n(вход) тг","Остаток\n(вход) тг","Долг\nпродление тг","Штрафы тг","ОБЩИЙ\nДОЛГ тг",
             "Формат","Chat ID","Бывший","Исключение","Админ","Источник","Дата входа",
             "Месяцев\nв проекте","Примечание","Партнёр"];
  var wids=[40,210,120,120,120,110,120,100,110,155,85,140,165,120,90,80,80,80];
  var heads=["№","Имя резидента","Тариф тг","Встреч\nоплачено","Встреч\nпроведено","Осталось\nвстреч",
             "Оплачено\n(вход) тг","Остаток\n(вход) тг","Долг\nпродление тг","Штрафы тг","ОБЩИЙ\nДОЛГ тг",
             "Формат","Chat ID","Бывший","Исключение","Админ","Источник","Дата входа",
             "Месяцев\nв проекте","Примечание","Партнёр"];
  for(var i=0;i<NC;i++){var c=ws.getRange(2,i+1);c.setValue(heads[i]);H(c);B(c);ws.setColumnWidth(i+1,wids[i]);}
  ws.getRange(2, RC.chat).setBackground("#1A3A1A").setFontColor("#66FF66");
  ws.getRange(2, RC.format).setBackground("#1A3A5C").setFontColor("#AADDFF");
  ws.getRange(2, RC.admin).setBackground("#3A1A3A").setFontColor("#FFAAFF");
  var dvT=SpreadsheetApp.newDataValidation()
    .requireValueInList(["0","100000","150000","200000","250000","300000","500000","1000000"],true)
    .setAllowInvalid(true).build();
  var dvBool=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Нет","Да"],true).setAllowInvalid(false).build();
  var dvFormat=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Офлайн","Онлайн"],true).setAllowInvalid(false).build();
  var dvAdmin=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Нет","Да"],true).setAllowInvalid(false).build();
  var DATA=[
    ["Тест",               0,0,          0,     "Тест",               "0",    "453800951"],
    ["Тест2",              0,0,          0,     "Тест",               "0",    "1285596249"],
    ["Елена",              300000,0,     0,     "ЛБ Рустам",          "100000",""],
    ["Альтаир",            100000,0,     0,     "ЛБ Рустам",          "100000","490685605"],
    ["Асет",               100000,0,     0,     "ЛБ Береке",          "100000",""],
    ["Дмитрий Цой",        100000,0,     820000,"ЛБ Береке и Рустам", "100000",""],
    ["Мирас",              150000,0,     150000,"ЛБ Береке и Рустам", "150000",""],
    ["Азамат TV",          800000,0,     0,     "ЛБ Береке",          "0",    ""],
    ["Казбек K9",          1000000,0,    0,     "ЛБ Береке",          "0",    ""],
    ["Евгений Бонд",       500000,0,     0,     "ЛБ Береке и Рустам", "100000",""],
    ["Ербол",              500000,0,     0,     "ЛБ Береке и Рустам", "500000",""],
    ["Дильшат",            500000,0,     0,     "ЛБ Береке и Рустам", "500000",""],
    ["Дамил",              500000,0,     0,     "ЛБ Береке и Рустам", "500000",""],
    ["Олжас",              500000,0,     0,     "ЛБ Береке и Рустам", "500000",""],
    ["Даулет Сайты",       200000,0,     0,     "Экспресс разбор",    "200000",""],
    ["Даниил Raskrutov",   100000,400000,0,     "Экспресс разбор",    "500000",""],
    ["Бiржан",              500000,0,     0,     "Экспресс разбор",    "500000",""],
    ["Зарина",              500000,0,     0,     "Экспресс разбор",    "500000",""],
    ["Арлан",              500000,0,     0,     "Экспресс разбор",    "0",    ""],
    ["Рамиль",             150000,0,     150000,"Экспресс разбор",    "150000",""]
  ];
  var DS=3,N=DATA.length,EMPTY=30,TOTAL=N+EMPTY;
  var defaultDateIn=new Date(2026,0,1); // 01.01.2026. замените на реальные даты
  for(var ri=0;ri<N;ri++){
    var row=DS+ri,r=DATA[ri];
    ws.getRange(row,1).setValue(ri+1);
    ws.getRange(row,2).setValue(r[0]);
    ws.getRange(row, RC.paid).setValue(r[1]);
    ws.getRange(row, RC.rest).setValue(r[2]);
    ws.getRange(row, RC.renew).setValue(r[3]);
    ws.getRange(row, RC.fine).setValue(0);
    ws.getRange(row, RC.total).setValue(r[1]+r[2]+r[3]);
    ws.getRange(row, RC.tariff).setValue(r[5]);
    ws.getRange(row, RC.date).setValue(defaultDateIn).setNumberFormat("DD.MM.YYYY");
    ws.getRange(row, RC.source).setValue(r[4]);
    ws.getRange(row, RC.former).setValue("Нет");
    // col12 значение устанавливается ниже через isExcl
    if(r[6])ws.getRange(row, RC.chat).setValue(r[6]);
    // Исключения и Админы
    var isExcl=["Тест2","Елена","Казбек K9","Даниил Raskrutov","Евгений Бонд","Ербол","Дильшат"].indexOf(r[0])>=0;
    ws.getRange(row, RC.except).setValue(isExcl?"Да":"Нет");
    var isAdm=["Тест","Тест2"].indexOf(r[0])>=0;
    ws.getRange(row, RC.admin).setValue(isAdm?"Да":"Нет");
    ws.getRange(row, RC.done).setValue(1).setNumberFormat("0");
    ws.getRange(row, RC.granted).setValue(0).setNumberFormat("0");
  }
  // Batch стили. сначала весь диапазон, потом детали
  ws.getRange(DS,1,TOTAL,NC).setFontFamily("Montserrat").setFontSize(10)
    .setFontWeight("normal").setVerticalAlignment("middle").setBackground(G1);
  // Чётные строки. белый
  for(var ri=0;ri<TOTAL;ri+=2){
    ws.getRange(DS+ri,1,1,NC).setBackground(WHT);
    ws.setRowHeight(DS+ri,24);
  }
  for(var ri=1;ri<TOTAL;ri+=2) ws.setRowHeight(DS+ri,24);
  // Детальные стили
  for(var ri=0;ri<TOTAL;ri++){
    var row=DS+ri,bg=ri%2===0?WHT:G1;
    ws.getRange(row,1).setHorizontalAlignment("center");
    ws.getRange(row,2).setHorizontalAlignment("left");
    ws.getRange(row,3,1,5).setNumberFormat("#,##0").setHorizontalAlignment("center");
    ws.getRange(row, RC.fine).setFontColor(RED);
    ws.getRange(row, RC.total).setFontWeight("bold");
    ws.getRange(row, RC.tariff).setNumberFormat("#,##0").setDataValidation(dvT);
    ws.getRange(row, RC.date).setNumberFormat("DD.MM.YYYY").setDataValidation(dvDate());
    var dvSrc2=SpreadsheetApp.newDataValidation()
      .requireValueInList(["ЛБ Рустам","ЛБ Береке","ЛБ Береке и Рустам",
        "Сарафанное радио","Экспресс разбор","Тест"],true)
      .setAllowInvalid(true).build();
    ws.getRange(row, RC.source).setHorizontalAlignment("left").setDataValidation(dvSrc2);
    ws.getRange(row, RC.former).setValue(ri<N?"Нет":"Нет").setDataValidation(dvBool);
    ws.getRange(row, RC.except).setDataValidation(dvBool); // значение. в цикле данных
    ws.getRange(row, RC.notes).setHorizontalAlignment("left");
    ws.getRange(row, RC.chat).setBackground("#E8F5E9").setFontColor(GRN).setFontWeight("bold");
    ws.getRange(row, RC.format).setValue("Офлайн").setDataValidation(dvFormat).setHorizontalAlignment("center");
    ws.getRange(row, RC.admin).setDataValidation(dvAdmin).setHorizontalAlignment("center");
     // значение в цикле данных
    B(ws.getRange(row,1,1,NC));
  }
  var DE=DS+TOTAL-1,TR=DE+1;
  ws.setRowHeight(TR,28);
  ws.getRange(TR,1,1,NC).setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
    .setFontSize(11).setFontWeight("bold").setHorizontalAlignment("center").setVerticalAlignment("middle");
  ws.getRange(TR,1).setValue("ИТОГО");B(ws.getRange(TR,1,1,NC));
  var rules=[];
  rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberGreaterThan(0)
    .setBackground(RED_BG).setFontColor(RED).setRanges([ws.getRange("G"+DS+":G"+DE)]).build());
  rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberLessThanOrEqualTo(0)
    .setBackground(GRN_BG).setFontColor(GRN).setRanges([ws.getRange("G"+DS+":G"+DE)]).build());
  rules.push(SpreadsheetApp.newConditionalFormatRule().whenFormulaSatisfied('=$K'+DS+'="Да"')
    .setBackground("#DDDDDD").setFontColor("#999999").setStrikethrough(true)
    .setRanges([ws.getRange("A"+DS+":N"+DE)]).build());
  ws.setConditionalFormatRules(rules);
  var wsFines=ss.getSheetByName("Штрафы");
  if(wsFines){
    var dvRes=SpreadsheetApp.newDataValidation()
      .requireValueInRange(ws.getRange("B"+DS+":B"+DE),true).setAllowInvalid(true).build();
    wsFines.getRange(3,2,300,1).setDataValidation(dvRes);
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// УЧЕТ ДДС
// ═══════════════════════════════════════════════════════════════════════════
function buildDDS(ss){
  var ws=ss.getSheetByName("Учет ДДС");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Учет ДДС", ss.getNumSheets());
  ws.setTabColor("#3D3D3D");ws.setFrozenRows(1);ws.setRowHeight(1,36);
  var defs=[{h:"Дата",w:110},{h:"Приход, тенге",w:130},{h:"Расход, тенге",w:130},
            {h:"Источник",w:220},{h:"Категория +",w:180},{h:"Категория -",w:180}];
  var hdrColors=[
    {bg:"#78909C",fg:WHT},   // A Дата бесцветный
    {bg:"#1B5E20",fg:"#A5D6A7"}, // B Приход
    {bg:"#B71C1C",fg:"#FFCDD2"}, // C Расход
    {bg:"#455A64",fg:WHT},   // D Источник
    {bg:"#90A4AE",fg:WHT},       // E Категория+ бесцветный
    {bg:"#B71C1C",fg:"#FFCDD2"}, // F Категория-
  ];
  for(var i=0;i<defs.length;i++){
    ws.setColumnWidth(i+1,defs[i].w);
    var c=ws.getRange(1,i+1);c.setValue(defs[i].h);
    var hc=hdrColors[i]||{bg:BLK,fg:WHT};
    H(c,hc.bg,hc.fg,9);B(c);
  }
  var dvSrc=SpreadsheetApp.newDataValidation()
    .requireValueInList(["БХ Трекинг продление","БХ Трекинг год","БХ Штраф",
                         "БХ МК/Завтрак","БХ Экспресс разбор","Прочие доходы"],true)
    .setAllowInvalid(true).build();
  var dvCatM=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Комиссия+налог","SMM","Маркет.бюджет","Таргетолог","Офис","CRM",
                         "Tilda + домен","Бухгалтер","Юрист","IT","Ассистент","Дивиденды",
                         "Canva","Съемки проф","Съемки доп","HH объявление","Симка тариф",
                         "Оборудование","Прочие расходы:","Аренда:","Бонус резидента"],true)
    .setAllowInvalid(true).build();
  var ROWS=500,DS=2;
  // rowHeight batch
  for(var ri=0;ri<ROWS;ri++)ws.setRowHeight(DS+ri,22);
  ws.getRange(DS,1,ROWS,6).setBackground(G1)
      .setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
  // Цвета строк чередуются
  // Цвета батчем. намного быстрее чем 500 циклов
  ws.getRange(DS,1,ROWS,1).setBackground(WHT);
  ws.getRange(DS,2,ROWS,1).setBackground("#E8F5E9");
  ws.getRange(DS,3,ROWS,1).setBackground("#FFEBEE");
  ws.getRange(DS,4,ROWS,1).setBackground(WHT);
  ws.getRange(DS,5,ROWS,1).setBackground(WHT);
  ws.getRange(DS,6,ROWS,1).setBackground("#FFEBEE");
  ws.getRange(DS,1,ROWS,1).setNumberFormat("DD.MM.YYYY").setDataValidation(dvDate())
    .setFontColor(BLK).setFontWeight("normal");
  ws.getRange(DS,2,ROWS,1).setNumberFormat("#,##0").setFontColor(BLK).setFontWeight("normal");
  ws.getRange(DS,3,ROWS,1).setNumberFormat("#,##0").setFontColor(BLK).setFontWeight("normal");
  ws.getRange(DS,4,ROWS,3).setHorizontalAlignment("left");
  ws.getRange(DS,4,ROWS,1).setDataValidation(dvSrc);
  // Столбец E. список резидентов
  var wsResForDDS=ss.getSheetByName("BS - резиденты дебет");
  if(wsResForDDS){
    var dvResForDDS=SpreadsheetApp.newDataValidation()
      .requireValueInRange(wsResForDDS.getRange("B3:B60"),true)
      .setAllowInvalid(true).build();
    ws.getRange(DS,5,ROWS,1).setDataValidation(dvResForDDS);
  }
  ws.getRange(DS,6,ROWS,1).setDataValidation(dvCatM);

  B(ws.getRange(DS,1,ROWS,6));
  // Скрываем строки с датой до 1 апреля 2026
  var cutoff=new Date(2026,3,1); // апрель = месяц 3 (0-indexed)
  // (скрытие применяется после copyDDSFromOriginal через hideDDSRowsBefore)
  // Условное форматирование столбца D. цвета по источнику
  var srcRules=[];
  var srcColors=[
    ["БХ Трекинг продление","#0a53a8","#FFFFFF"],
    ["БХ Штраф","#b10202","#FFFFFF"],
    ["БХ Трекинг год","#215a6c","#FFFFFF"],
    ["БХ Экспресс разбор","#bfe1f6","#0a3a5c"],
    ["БХ МК/Завтрак","#d4edbc","#2d5a1b"],
    ["Прочие доходы","#ffc8aa","#7a3a1a"],
  ];
  srcColors.forEach(function(sc){
    srcRules.push(SpreadsheetApp.newConditionalFormatRule()
      .whenTextEqualTo(sc[0]).setBackground(sc[1]).setFontColor(sc[2])
      .setRanges([ws.getRange(DS,4,ROWS,1)]).build());
  });
  ws.setConditionalFormatRules(srcRules);
}

function copyDDSFromOriginal(){
  try{
    var origSS=SpreadsheetApp.openById(ORIG_SS_ID);
    var origSheet=origSS.getSheetByName("Учет ДДС")||origSS.getSheets()[0];
    var destSS=SpreadsheetApp.openById(SS_ID);
    var destSheet=destSS.getSheetByName("Учет ДДС");
    if(!destSheet){toast("Сначала запустите setupAllSheets!");return;}
    var lastRow=origSheet.getLastRow();
    if(lastRow<2){toast("Нет данных в оригинале.");return;}
    var srcData=origSheet.getRange(2,1,lastRow-1,6).getValues();
    destSheet.getRange(2,1,srcData.length,6).setValues(srcData);
    toast("✅ Скопировано "+srcData.length+" строк.");
    // Скрываем строки до 1 апреля 2026
    try{hideDDSRowsBefore();}catch(he){Logger.log("hide: "+he);}
  }catch(e){toast("Ошибка: "+e.message);}
}

// ═══════════════════════════════════════════════════════════════════════════
// PL
// ═══════════════════════════════════════════════════════════════════════════
function buildPL(ss){
  var ws=ss.getSheetByName("PL");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("PL", ss.getNumSheets());ws.setTabColor("#1A3A1A");ws.setFrozenRows(3);ws.setFrozenColumns(1);
  var NC=14;
  ws.setRowHeight(1,36);
  ws.getRange(1,1,1,NC).setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
    .setFontSize(11).setFontWeight("bold").setHorizontalAlignment("center").setVerticalAlignment("middle");
  ws.getRange(1,1).setValue("ОПИУ").setHorizontalAlignment("left");
  for(var m=1;m<=12;m++)ws.getRange(1,1+m).setValue(2026);
  ws.getRange(1,14).setValue("ГОД");
  ws.setRowHeight(2,18);
  ws.getRange(2,1,1,NC).setBackground(G2).setFontColor(GX).setFontFamily("Montserrat")
    .setFontSize(9).setHorizontalAlignment("center").setVerticalAlignment("middle");
  ws.getRange(2,1).setValue("Статья").setHorizontalAlignment("left");
  for(var m=1;m<=12;m++)ws.getRange(2,1+m).setValue(m);
  ws.getRange(2,14).setValue(" ");
  ws.setRowHeight(3,36);ws.setColumnWidth(1,220);
  H(ws.getRange(3,1));ws.getRange(3,1).setValue("Статья").setHorizontalAlignment("left");B(ws.getRange(3,1));
  var mN=["Январь","Февраль","Март","Апрель","Май","Июнь","Июль","Август","Сентябрь","Октябрь","Ноябрь","Декабрь"];
  for(var m=0;m<12;m++){ws.setColumnWidth(2+m,90);var cm=ws.getRange(3,2+m);cm.setValue(mN[m]);H(cm);B(cm);}
  ws.setColumnWidth(14,105);H(ws.getRange(3,14),"#1A5C1A","#FFFFFF");ws.getRange(3,14).setValue("ИТОГО");B(ws.getRange(3,14));
  var items=[
    [4,"hdr","ДОХОДЫ"],[5,"inc","БХ Трекинг продление"],[6,"inc","БХ Трекинг год"],
    [7,"inc","БХ Штраф"],[8,"inc","БХ Экспресс разбор"],[9,"inc","БХ МК/Завтрак"],
    [10,"inc","Прочие доходы"],[11,"toti","ИТОГО ДОХОДЫ"],[12,"sep",""],
    [13,"hdr","РАСХОДЫ"],[14,"exp","SMM"],[15,"exp","Маркет.бюджет"],
    [16,"exp","Таргетолог"],[17,"exp","Комиссия+налог"],[18,"exp","Tilda + домен"],
    [19,"exp","CRM"],[20,"exp","Canva"],[21,"exp","Съемки проф"],[22,"exp","Съемки доп"],
    [23,"exp","HH объявление"],[24,"exp","Симка тариф"],[25,"exp","Оборудование"],
    [26,"exp","Офис"],[27,"exp","Ассистент"],[28,"exp","Бухгалтер"],
    [29,"exp","Юрист"],[30,"exp","IT"],[31,"exp","Прочие расходы:"],
    [32,"tote","ИТОГО РАСХОДЫ"],[33,"gp","ЧИСТАЯ ПРИБЫЛЬ"],
    [34,"div","Дивиденды"],[35,"np","На кассе"]
  ];
  items.forEach(function(it){
    var row=it[0],type=it[1],label=it[2];
    ws.setRowHeight(row,type==="sep"?6:22);
    if(type==="sep"){ws.getRange(row,1,1,NC).setBackground(G1);return;}
    var isTot=type==="toti"||type==="tote"||type==="gp"||type==="np";
    var bg=isTot?BLK:(type==="hdr"?G2:(row%2===0?WHT:G1));
    ws.getRange(row,1,1,NC).setBackground(bg).setFontColor(isTot?WHT:BLK).setFontFamily("Montserrat")
      .setFontSize(10).setVerticalAlignment("middle");
    ws.getRange(row,1).setValue(label).setHorizontalAlignment("left")
      .setFontWeight(isTot||type==="hdr"?"bold":"normal");
    for(var m=0;m<12;m++){
      var cell=ws.getRange(row,2+m);
      cell.setNumberFormat(type==="mrg"?"0%":"#,##0").setHorizontalAlignment("right");
      if(isTot)cell.setFontWeight("bold");
      if(type==="exp"||type==="div")cell.setFontColor(RED);
      if(type==="gp"||type==="np")cell.setFontColor(WHT);
      if(type==="mrg")cell.setBackground(G2).setFontColor(BLK);
      if(type==="div"){cell.setBackground(G1);ws.getRange(row,1).setBackground(G1).setFontColor(BLK).setFontWeight("normal");}
    }
    var cY=ws.getRange(row,14);
    cY.setNumberFormat(type==="mrg"?"0%":"#,##0").setHorizontalAlignment("right");
    if(isTot)cY.setFontWeight("bold");
    if(type==="exp"||type==="div")cY.setFontColor(RED);
    if(type==="gp"||type==="np")cY.setFontColor(WHT);
    if(type==="mrg")cY.setBackground(G2).setFontColor(BLK);
    if(type==="div")cY.setBackground(G1);
    B(ws.getRange(row,1,1,NC));
  });
}

function recalcPL(){
  // Один пересчёт PL на всё: тот же, что из меню. Старый код падал на каждом вызове,
  // поэтому PL ждал часового триггера
  return bsRebuildPL(true);
}

function buildVisits(ss){
  var ws=ss.getSheetByName("BS - посещения");
  if(!ws) return;if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("BS - посещения", ss.getNumSheets());
  ws.setTabColor("#3D3D3D");ws.setFrozenRows(2);ws.setFrozenColumns(2);
  var NC=10;
  ws.setRowHeight(1,40);
  ws.getRange(1,1,1,NC).setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
    .setFontSize(12).setFontWeight("bold").setHorizontalAlignment("center").setVerticalAlignment("middle");
  ws.getRange(1,1).setValue("");
  ws.getRange(1,2).setValue("BS. УЧЁТ ПОСЕЩЕНИЙ").setHorizontalAlignment("left");
  ws.setRowHeight(2,42);
  var heads=["№","Имя резидента","Тариф\n(3/4)","Пос. 1","Пос. 2","Пос. 3","Пос. 4","Мес.\nв проекте","Сумма\nпродл. тг",""];
  var wids=[40,240,80,80,80,80,80,90,120,1];
  for(var i=0;i<NC;i++){ws.setColumnWidth(i+1,wids[i]);var c=ws.getRange(2,i+1);c.setValue(heads[i]);H(c);B(c);}
  ws.getRange(2,4).setBackground("#1A3A1A").setFontColor("#66FF66").setValue("Пос. 1");
  ws.getRange(2,5).setBackground("#1A3A1A").setFontColor("#66FF66").setValue("Пос. 2");
  ws.getRange(2,6).setBackground("#1A3A1A").setFontColor("#66FF66").setValue("Пос. 3");
  ws.getRange(2,7).setBackground("#1A3A1A").setFontColor("#66FF66").setValue("Пос. 4");
  ws.getRange(2,8).setBackground("#1A3A5C").setFontColor("#66AAFF").setValue("Мес.\nв проекте");
  ws.getRange(2,9).setBackground("#3A1A2A").setFontColor("#FF88CC").setValue("Сумма\nпродл. тг");
  ws.hideColumns(10);
  var dvTar=SpreadsheetApp.newDataValidation().requireValueInList(["3","4"],true).setAllowInvalid(false).build();
  var names=[
    "Тест","Тест2","Елена","Альтаир","Асет","Дмитрий Цой","Мирас",
    "Азамат TV","Казбек K9","Евгений Бонд",
    "Ербол","Дильшат","Дамил","Олжас",
    "Даулет Сайты","Даниил Raskrutov","Арлан","Рамиль","Бiржан","Зарина"
  ];
  var DS=3,N=names.length;
  for(var ri=0;ri<N;ri++){
    var row=DS+ri,bg=ri%2===0?WHT:G1;
    ws.setRowHeight(row,26);
    ws.getRange(row,1,1,NC).setBackground(bg).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
    ws.getRange(row,1).setValue(ri+1).setHorizontalAlignment("center");
    ws.getRange(row,2).setValue(names[ri]).setHorizontalAlignment("left").setFontWeight("bold");
    ws.getRange(row,3).setValue("4").setDataValidation(dvTar).setHorizontalAlignment("center");
    ws.getRange(row,4,1,3).insertCheckboxes();
    ws.getRange(row,7).insertCheckboxes();
    ws.getRange(row,8).setValue("").setHorizontalAlignment("center").setFontColor("#0000CC").setFontWeight("bold");
    ws.getRange(row,9).setHorizontalAlignment("center").setFontColor("#8B0000").setFontWeight("bold").setNumberFormat("#,##0");
    ws.getRange(row,10).setValue(0);
    B(ws.getRange(row,1,1,NC));
  }
  for(var ri=N;ri<N+20;ri++){
    var row=DS+ri;
    ws.setRowHeight(row,26);
    ws.getRange(row,1,1,NC).setBackground(ri%2===0?WHT:G1).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
    ws.getRange(row,3).setDataValidation(dvTar);ws.getRange(row,10).setValue(0);
    B(ws.getRange(row,1,1,NC));
  }
}

function buildFormer(ss){
  var ws=ss.getSheetByName("Бывшие резиденты");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Бывшие резиденты", ss.getNumSheets());
  ws.setTabColor(GX);ws.setFrozenRows(3);
  ws.setRowHeight(1,40);TITMERGE(ws.getRange("A1:K1"),"BS. БЫВШИЕ РЕЗИДЕНТЫ | АРХИВ");
  ws.setRowHeight(2,30);
  TITMERGE(ws.getRange("A2:K2"),"Бывший=Да → автоперенос. Активный → возврат.");
  ws.getRange("A2:K2").setFontSize(9).setFontStyle("italic").setBackground("#FFFDE7").setFontColor("#5D4037").setHorizontalAlignment("left");
  ws.setRowHeight(3,40);
  var heads=["№","Имя","Оплачено тг","Остаток тг","Долг прод. тг","Тариф тг","Источник","Дата входа","Дата выхода","Примечание","Статус"];
  var wids=[40,200,115,110,110,100,165,108,108,170,110];
  for(var i=0;i<11;i++){var c=ws.getRange(3,i+1);c.setValue(heads[i]);H(c);B(c);ws.setColumnWidth(i+1,wids[i]);}
  ws.getRange(3,11).setBackground("#1A3A1A").setFontColor("#66FF66");
  var dvSt=SpreadsheetApp.newDataValidation().requireValueInList(["Бывший","Активный"],true).setAllowInvalid(false).build();
  var former=[
    ["Гульназ бухгалтерия",250000,0,0,"","Экспресс разбор"],
    ["Рахат HappyMoney","","","","","ЛБ Береке"],["Павел","","","","","ЛБ Береке"],
    ["Елхан","","","","","ЛБ Береке"],["Адом","","","","","ЛБ Рустам"],
    ["Роман","","","","","Сарафанное радио"],["Дионис","","","","","Сарафанное радио"],
    ["Алмас","","","","","ЛБ Рустам"],["Борис Литвинов","",200000,"","","ЛБ Рустам"],
    ["Жанна","","","","","ЛБ Береке"],["Амир мобилограф","","","","","ЛБ Рустам"],
    ["Бахыт","","","","","Сарафанное радио"],["Рамазан IT",270000,"","","","Сарафанное радио"],
    ["Раушан",300000,"","","","ЛБ Береке"],["Даниал","",600000,"","","ЛБ Береке и Рустам"],
    ["Дмитрий П","",550000,"","","ЛБ Береке"],["Данияр Denny","","","","","ЛБ Рустам"],
    ["Асхат","","","","","Сарафанное радио"],["Азиз","","","","","ЛБ Береке"],
    ["Ораш","","","","","ЛБ Береке"],["Касымхан","","","","","ЛБ Рустам"],
    ["Тогжан","","","","","ЛБ Береке"],["Сая","","","","","Сарафанное радио"],
    ["Сабина",500000,"","","","ЛБ Рустам"],["Акжолтай",300000,"","","","Экспресс разбор"],
    ["Сергей Жаур",140000,180000,"","","ЛБ Береке и Рустам"],["Рамазан",100000,180000,"","","ЛБ Рустам"],
    ["Темирлан Чу",150000,"","","","ЛБ Рустам"],["Гульзира",100000,"","","","Сарафанное радио"],
    ["Бекежан",100000,"","","","Экспресс разбор"],["Ассемгуль",100000,"","","","ЛБ Рустам"],
    ["Зульфия",50000,"","","","Сарафанное радио"],["Марк",530000,250000,100000,"","Экспресс разбор"],
    ["Жанерке",150000,"",150000,"","ЛБ Рустам"],["Даниал 2",500000,"","","","Экспресс разбор"],
    ["Эльвира",150000,100000,150000,"","Экспресс разбор"],["Рината аренда",150000,150000,150000,"","Экспресс разбор"],
    ["Жасулан PapaDesigner","","","","","ЛБ Береке"],["Артем Kaspi рыбалка",300000,100000,100000,"","ЛБ Береке"],
    ["Ербулан",200000,"","","","Экспресс разбор"],["Азат WB",150000,"",150000,"","ЛБ Береке и Рустам"],
    ["Гани Таргетолог","","","","","ЛБ Береке"],["Исфандияр",500000,100000,100000,"","Бизнес-разбор"],
    ["Балгуль Химия",150000,"",150000,"","Экспресс разбор"],["Ксения Centre lor",100000,"",100000,"","ЛБ Рустам"]
  ];
  var DS=4,N=former.length;
  for(var ri=0;ri<N;ri++){
    var row=DS+ri,r=former[ri],bg=ri%2===0?G1:WHT;ws.setRowHeight(row,22);
    ws.getRange(row,1,1,11).setValues([[ri+1,r[0]||" ",r[1]||" ",r[2]||" ",r[3]||" ",r[4]||" ",r[5]||" "," "," "," ","Бывший"]])
      .setBackground(bg).setFontColor(GX).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
    ws.getRange(row,2).setHorizontalAlignment("left");ws.getRange(row,7).setHorizontalAlignment("left");
    ws.getRange(row,3,1,4).setNumberFormat("#,##0");ws.getRange(row,8,1,2).setNumberFormat("DD.MM.YYYY");
    ws.getRange(row,11).setDataValidation(dvSt).setHorizontalAlignment("center");B(ws.getRange(row,1,1,11));
  }
}

function buildLog(ss){
  var ws=ss.getSheetByName("Лог отчётов");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Лог отчётов", ss.getNumSheets());ws.setTabColor(GX);ws.hideSheet();
  ["Дата","Username","Имя","Текст","Timestamp","User ID","Thread ID"].forEach(function(h,i){
    var c=ws.getRange(1,i+1);c.setValue(h);H(c,GX);B(c);
  });ws.setFrozenRows(1);
}

// ═══════════════════════════════════════════════════════════════════════════
// ТРИГГЕРЫ
// ═══════════════════════════════════════════════════════════════════════════
function setupEditTrigger(){
  ScriptApp.newTrigger("onTableEdit").forSpreadsheet(SS_ID).onEdit().create();
}



function setupMeetCheckTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="checkMeetingsCompleted")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("checkMeetingsCompleted").timeBased().everyMinutes(30).create();
}

// Через 1,5 часа после начала спрашивает команду, прошла ли встреча.
// Не спрашивает, если встреча уже отмечена («✅ Проведена», зелёная строка
// или запись в «Лог встреч»). Встречи в одно время (офлайн-день) = один вопрос
function checkMeetingsCompleted(){
  if(bsIsCopy()) return;
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");if(!ws)return;
  var now=new Date();
  var lr=ws.getLastRow();if(lr<3)return;
  var cache=CacheService.getScriptCache();
  var rows=ws.getRange(3,1,lr-2,8).getValues();
  var bgs=ws.getRange(3,1,lr-2,1).getBackgrounds();
  var logged=_bsMeetLoggedSet(ss);
  var slots={}, order=[];
  for(var i=0;i<rows.length;i++){
    var resName=String(rows[i][1]||"").trim(); if(!resName) continue;
    var evDate=rows[i][2]; if(!(evDate instanceof Date)) continue;
    var evTime=rows[i][3];
    var st=String(rows[i][6]||"")+" "+String(rows[i][7]||"");
    var dayStr=Utilities.formatDate(evDate,"Asia/Almaty","dd.MM.yyyy");
    if(st.indexOf("Проведена")>=0 || String(bgs[i][0]).toLowerCase()==="#e8f5e9" || logged[resName+"|"+dayStr]) continue;
    var meetStart=new Date(evDate);
    var timeStr="";
    if(evTime instanceof Date) timeStr=Utilities.formatDate(evTime,"Asia/Almaty","HH:mm");
    else if(/^\d{1,2}:\d{2}/.test(String(evTime||"").trim())) timeStr=String(evTime).trim().slice(0,5);
    if(timeStr){
      var tp=timeStr.split(":");
      meetStart.setHours(parseInt(tp[0],10),parseInt(tp[1],10),0,0);
    }
    var askTime=new Date(meetStart.getTime()+90*60*1000);
    if(!(now>=askTime && now<new Date(askTime.getTime()+35*60*1000))) continue;
    var k=dayStr+" "+timeStr;
    if(!slots[k]){ slots[k]={day:dayStr,time:timeStr,names:[]}; order.push(k); }
    slots[k].names.push(resName);
  }
  order.forEach(function(k){
    var sl=slots[k];
    var key="mchk2_"+k.replace(/[^0-9]/g,"");
    if(cache.get(key)) return;
    cache.put(key,"1",21600);
    var text;
    if(sl.names.length>1){
      text="📋 Офлайн день "+sl.day+(sl.time?" в "+sl.time:"")+" прошёл?\n\n"+sl.names.length+" резидентов: "+sl.names.join(", ")+
        "\n\nОтметьте присутствие в приложении: Встречи → встреча → «Встреча прошла».";
    } else {
      text="📋 Встреча "+sl.day+(sl.time?" в "+sl.time:"")+" с "+sl.names[0]+" прошла?\n\nПодтвердите в приложении: Встречи → встреча → «Встреча прошла».";
    }
    ADMIN_IDS.forEach(function(aid){
      try{
        UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
          method:"post",contentType:"application/json",
          payload:JSON.stringify({chat_id:aid,text:text,
            reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("schedule")}}]]})}),
          muteHttpExceptions:true
        });
      }catch(e){}
    });
  });
}

// Кто уже отмечен в «Лог встреч»: ключи «Имя|дд.ММ.гггг» за последние 3 дня
function _bsMeetLoggedSet(ss){
  var out={};
  try{
    var wl=ss.getSheetByName("Лог встреч");
    if(!wl || wl.getLastRow()<2) return out;
    var n=Math.min(300, wl.getLastRow()-1);
    var since=new Date(Date.now()-3*86400000);
    wl.getRange(wl.getLastRow()-n+1,1,n,2).getValues().forEach(function(r){
      if(r[0] instanceof Date && r[0]>=since && r[1]) out[String(r[1]).trim()+"|"+Utilities.formatDate(r[0],"Asia/Almaty","dd.MM.yyyy")]=1;
    });
  }catch(e){ Logger.log("meetLogged: "+e); }
  return out;
}

// Сводки команде без спама: тот же текст повторно не шлётся (раз в неделю
// как напоминание), новый текст не чаще раза в 20 часов
function _bsDigestSend(key, msg){
  var props=PropertiesService.getScriptProperties();
  var h=Utilities.base64Encode(Utilities.computeDigest(Utilities.DigestAlgorithm.MD5, msg, Utilities.Charset.UTF_8));
  var last=props.getProperty("BS_DIG_"+key)||"";
  var p=last.split("|"), lastH=p[0]||"", lastAt=Number(p[1]||0);
  var now=Date.now();
  if(h===lastH && now-lastAt<7*86400000) return false;
  if(now-lastAt<20*3600000) return false;
  props.setProperty("BS_DIG_"+key, h+"|"+now);
  try{ tgSendAdmins(msg); }catch(e){ Logger.log("digest "+key+": "+e); }
  return true;
}

function _addCheckForResident(resName){
  // Отмечает проведённую встречу прямо в листе дебета
  try{
    var addCache=CacheService.getScriptCache();
    var addKey="addcheck_"+resName;
    if(addCache.get(addKey)){ Logger.log("Дубль заблокирован: "+addKey); return; }
    addCache.put(addKey,"1",30);

    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("BS - резиденты дебет");
    if(!ws) return;
    var lr=ws.getLastRow();
    if(lr<3) return;

    var names=ws.getRange(3, RC.name, lr-2, 1).getValues();
    for(var i=0;i<names.length;i++){
      if(String(names[i][0]||"").trim()!==resName) continue;
      var row=3+i;
      var done=Number(ws.getRange(row, RC.done).getValue())||0;
      var granted=Number(ws.getRange(row, RC.granted).getValue())||0;
      var tariff=Number(ws.getRange(row, RC.tariff).getValue())||0;

      ws.getRange(row, RC.done).setValue(done+1);
      _bsSyncVisits(resName, done+1);
      SpreadsheetApp.flush();

      // Пакет закончился. добавляем продление
      if(granted>0 && done+1>=granted && tariff>0){
        var curDebt=Number(ws.getRange(row, RC.renew).getValue())||0;
        var note=ws.getRange(row, RC.renew).getNote()||"";
        var mark="продление при "+granted;
        if(note.indexOf(mark)<0){
          ws.getRange(row, RC.renew).setValue(curDebt+tariff).setNote(mark);
          try{
            ADMIN_IDS.forEach(function(a){
              tgSend(a, resName+": использованы все "+granted+" встреч.\n"+
                "В долг продления добавлено "+_fmtMoney(tariff));
            });
          }catch(e){}
        }
      }

      // Уведомляем резидента
      try{
        var chatId=_normId(ws.getRange(row, RC.chat).getValue());
        if(chatId){
          tgSend(chatId, "Встреча отмечена как проведённая.\n" +
            "Использовано " + (done+1) + " из " + granted + " встреч." +
            (granted-(done+1)<=1 ? "\n\nПакет заканчивается, скоро потребуется продление." : ""));
        }
      }catch(e){ Logger.log("notify res: "+e); }
      break;
    }

    try{ _invalidateBSCaches(); }catch(e){}
  }catch(e){ Logger.log("_addCheckForResident: "+e); }
}

function sendNPS(){
  // Защита от двойной отправки: не чаще раза в 30 дней
  var props=PropertiesService.getScriptProperties();
  var lastSent=props.getProperty("NPS_LAST_SENT");
  if(lastSent){
    var lastDate=new Date(lastSent);
    var daysSince=(new Date()-lastDate)/(1000*60*60*24);
    if(daysSince<30){
      Logger.log("sendNPS пропущен: последняя отправка "+Math.round(daysSince)+" дней назад");
      return;
    }
  }
  
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(!wsR)return;
  var lr=wsR.getLastRow();
  var q1=getText("nps_q1",null)||"Что в Business Surgery самое ценное для вас?";
  var q2=getText("nps_q2",null)||"Что бы вы хотели улучшить или чего вам не хватает?";
  var q3=getText("nps_q3",null)||"Если бы вы рекомендовали BS другу. чем бы его зацепили?";
  var msg="📋 *Быстрый опрос BS* (3 вопроса)\n\n1️⃣ "+q1+"\n\n2️⃣ "+q2+"\n\n3️⃣ "+q3+
    "\n\nОтветьте прямо здесь. нам очень важно ваше мнение!";
  
  var sentCount=0;
  for(var r=3;r<=lr;r++){
    var nm=wsR.getRange(r, RC.name).getValue();if(!nm)continue;
    if(wsR.getRange(r, RC.former).getValue()==="Да")continue;
    if(wsR.getRange(r, RC.except).getValue()==="Да")continue;
    var chatId=String(wsR.getRange(r, RC.chat).getValue()||"");
    if(chatId){
      tgSend(chatId,msg);
      // Помечаем что NPS ожидает ответа от этого чата (24 часа)
      try{
        props.setProperty("NPS_PENDING_"+chatId, new Date().toISOString());
      }catch(e){}
      sentCount++;
    }
  }
  
  // Запишем дату последней отправки
  props.setProperty("NPS_LAST_SENT", new Date().toISOString());
  
  // Уведомление админам
  ADMIN_IDS.forEach(function(aid){
    try{
      bsSysNoteTo(aid, "📋 NPS опрос отправлен "+sentCount+" резидентам.\n\nОтветы будут переслены сюда автоматически.");
    }catch(e){}
  });
  
  Logger.log("NPS отправлен "+sentCount+" резидентам");
}



function setupStartReminderTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="remindPendingUsers")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("remindPendingUsers").timeBased().everyMinutes(10).create();
}

function remindPendingUsers(){
  var cache=CacheService.getScriptCache();
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  // Список ожидающих из кэша
  var pendingRaw=cache.get("pending_users");
  if(!pendingRaw)return;
  var pending=JSON.parse(pendingRaw);
  var stillPending={};
  Object.keys(pending).forEach(function(cid){
    // Проверяем. может уже стал резидентом
    var isResident=false;
    if(wsR){
      var lr=wsR.getLastRow();
      for(var r=3;r<=lr;r++){
        if(String(wsR.getRange(r, RC.chat).getValue())===cid){isResident=true;break;}
      }
    }
    if(!isResident){
      var data=pending[cid];
      // Не напоминаем дольше 24 часов
      if(Date.now()-data.ts<24*60*60*1000){
        var policy=POLICY_TEXT.replace("{имя}",data.name||"");
        tgSendAccept(cid,policy);
        stillPending[cid]=data;
      }
    }
  });
  if(Object.keys(stillPending).length>0){
    cache.put("pending_users",JSON.stringify(stillPending),25*60*60);
  } else {
    cache.remove("pending_users");
  }
}

function setupBackupTrigger(){
  // Триггер копий отключён: удаляем, если остался с прошлых версий
  try{
    ScriptApp.getProjectTriggers().forEach(function(t){
      if(t.getHandlerFunction() === "weeklyBackup") ScriptApp.deleteTrigger(t);
    });
  }catch(e){}
}


function _adminAddFineFromBot(cid,name,type,amount){
  if(bsMasterRefuse(cid)) return;
  var typeMap={"отчет":"Не сдан отчёт","опоздание":"Опоздание","слово":"Цена слова","пропуск":"Пропуск посещения","нарушение":"Нарушение правил","другое":"Прочее"};
  var fineType=typeMap[type]||"Прочее";
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsF=ss.getSheetByName("Штрафы");if(!wsF){tgSend(cid,"Ошибка");return;}
    // Находим следующую свободную строку
    var fr=_findFreeRow(wsF,3,2);
    wsF.getRange(fr,2).setValue(name);
    wsF.getRange(fr,3).setValue(fineType);
    wsF.getRange(fr,4).setValue(amount).setNumberFormat("#,##0");
    wsF.getRange(fr,5).setValue(new Date()).setNumberFormat("DD.MM.YYYY");
    wsF.getRange(fr,6).setValue("Не оплатил");
    _renumFines();recalcResidents();
    // Уведомляем резидента
    var chatId=_getChatId(name);
    if(chatId){
      var msg=getText("fine_reminder",{"имя":name.split(" ")[0],"дата":Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy"),"kaspi":KASPI_LINK});
      if(!msg)msg=name.split(" ")[0]+": штраф "+amount.toLocaleString()+" тг ("+fineType+")";
      tgSendButton(chatId,msg,"Оплатить",KASPI_LINK);
    }
    if(fineType==="Цена слова") _notifyWordPrice(fr);
    tgSend(cid,"✅ Штраф выставлен: "+name+". "+amount.toLocaleString()+" тг ("+fineType+")"+(chatId?"\n✉️ Резидент уведомлён":"\n⚠️ Нет Chat ID"),
      {reply_markup:JSON.stringify({inline_keyboard:[[{text:"◀️ Меню",callback_data:"admin_menu"}]]})});
  }catch(e){tgSend(cid,"Ошибка: "+e.message);}
}

function _saveScheduleFromBot(cid,resName,dateStr,time){
  if(bsMasterRefuse(cid)) return;
  try{
    // Защита от дублей: одна и та же комбинация админ+резидент+дата+время. блок на 60 сек
    var schedGuardCache=CacheService.getScriptCache();
    var schedGuardKey="sched_create_"+cid+"_"+resName+"_"+dateStr+"_"+(time||"none");
    if(schedGuardCache.get(schedGuardKey)){
      Logger.log("Дубль создания встречи заблокирован: "+schedGuardKey);
      tgSend(cid,"⚠️ Эта встреча только что создана. Если нужна ещё одна. подожди минуту");
      return;
    }
    schedGuardCache.put(schedGuardKey,"1",60);
    
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Расписание");
    if(!ws){tgSend(cid,"Лист Расписание не найден");return;}
    
    // Проверим что такой записи ещё нет в листе
    try{
      var lrCheck=ws.getLastRow();
      if(lrCheck>=3){
        var existing=ws.getRange(3, 2, lrCheck-2, 20).getValues();
        for(var ei=0;ei<existing.length;ei++){
          var exName=existing[ei][0];
          var exDate=existing[ei][1];
          var exTime=existing[ei][2];
          if(exName!==resName)continue;
          var exDateStr="";
          if(exDate instanceof Date){
            exDateStr=Utilities.formatDate(exDate,"Asia/Almaty","dd.MM.yyyy");
          } else if(exDate){
            exDateStr=String(exDate);
          }
          if(exDateStr!==dateStr)continue;
          // Имя и дата совпадают
          var exTimeStr="";
          if(exTime instanceof Date){
            exTimeStr=Utilities.formatDate(exTime,"Asia/Almaty","HH:mm");
          } else {
            exTimeStr=String(exTime||"").trim();
          }
          if(exTimeStr===String(time||"").trim()){
            Logger.log("Встреча уже существует: "+resName+" "+dateStr+" "+time);
            tgSend(cid,"⚠️ Встреча с "+resName+" на "+dateStr+(time?" в "+time:"")+" уже есть в расписании");
            return;
          }
        }
      }
    }catch(checkE){Logger.log("dup-check: "+checkE);}

    // Парсим дату и время
    var dp=String(dateStr||"").split(".");
    if(dp.length<2){tgSend(cid,"Неверный формат даты");return;}
    var year=parseInt(dp[2]||new Date().getFullYear());
    var month=parseInt(dp[1]);
    var day=parseInt(dp[0]);
    var dStr=year+"-"+String(month).padStart(2,"0")+"-"+String(day).padStart(2,"0");
    var dt=Utilities.parseDate(dStr+" 12:00:00","Asia/Almaty","yyyy-MM-dd HH:mm:ss");
    var tp=String(time||"12:00").split(":");
    var hh=parseInt(tp[0])||12, mm=parseInt(tp[1])||0;
    // Время через parseDate Asia/Almaty (как в _miniAddSchedule)
    var hhStr=String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
    var todayStr=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
    var evTimeValue=Utilities.parseDate(todayStr+" "+hhStr,"Asia/Almaty","yyyy-MM-dd HH:mm:ss");

    // Ищем первую СВОБОДНУЮ строку
    var lr=ws.getLastRow();
    var newRow=-1;
    for(var r=3;r<=lr;r++){
      if(!ws.getRange(r,2).getValue()){newRow=r;break;}
    }
    if(newRow<0)newRow=Math.max(lr+1,3);

    // Блокируем onTableEdit через кэш
    var schedCache=CacheService.getScriptCache();
    schedCache.put("sched_meet_"+newRow+"_"+new Date().toDateString(),"1",3600);

    // Записываем
    ws.getRange(newRow,1).setValue(newRow-2);
    ws.getRange(newRow,2).setValue(resName);
    ws.getRange(newRow,3).setValue(dt).setNumberFormat("DD.MM.YYYY");
    ws.getRange(newRow,4).setValue(evTimeValue).setNumberFormat("HH:MM");
    SpreadsheetApp.flush();

    // Создаём Meet
    try{_autoCreateMeet(newRow);}catch(meErr){Logger.log("autoCreateMeet from bot: "+meErr);}
    var link=ws.getRange(newRow,6).getValue()||"";

    tgSend(cid,"✅ Расписание добавлено:\n👤 "+resName+"\n📅 "+dateStr+" "+time+(link?"\n🔗 "+link:""),
      {reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("schedule")}}],
        [{text:"◀️ Меню",callback_data:"admin_menu"}]
      ]})});
  }catch(e){
    Logger.log("_saveScheduleFromBot ERR: "+e+" stack="+(e.stack||""));
    tgSend(cid,"Ошибка при создании: "+e.message);
  }
}

function _recordPaymentFromBot(cid,amount,src,type,isCash,resName){
  if(bsMasterRefuse(cid)) return;
  // Если приход за штраф. закрываем неоплаченные штрафы резидента
  if(type==="income" && src && src.indexOf("Штраф")>=0 && resName){
    try{
      var ssF=SpreadsheetApp.openById(SS_ID);
      var wsFCl=ssF.getSheetByName("Штрафы");
      if(wsFCl){
        var lrF=wsFCl.getLastRow();
        if(lrF>=3){
          var fineRows=wsFCl.getRange(3,2,lrF-2,5).getValues();
          var remainAmt=Number(amount)||0;
          // Идём с конца чтобы удаление не сбивало индексы
          var rowsToDelete=[];
          for(var fi=0;fi<fineRows.length && remainAmt>0;fi++){
            var fName=fineRows[fi][0];
            var fAmt=Number(fineRows[fi][2])||0;
            var fStatus=fineRows[fi][4];
            if(fName===resName && fStatus!=="Оплатил" && fAmt>0){
              if(remainAmt>=fAmt){
                rowsToDelete.push(3+fi);
                remainAmt-=fAmt;
              }
            }
          }
          // Удаляем строки с конца
          rowsToDelete.sort(function(a,b){return b-a;}).forEach(function(rNum){
            try{
              wsFCl.getRange(rNum,1,1,6).setBackground("#E8F5E9"); // зелёная подсветка
              SpreadsheetApp.flush();
              Utilities.sleep(500);
              wsFCl.deleteRow(rNum);
            }catch(delE){Logger.log("del fine row "+rNum+": "+delE);}
          });
          if(rowsToDelete.length>0){
            try{_renumFines();}catch(e){}
            try{recalcResidents();}catch(e){}
          }
        }
      }
    }catch(closeFineErr){Logger.log("closeFine: "+closeFineErr);}
  }
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Учет ДДС");if(!ws){ if(cid==="mini") throw new Error("ОДДС не найден"); tgSend(cid,"ОДДС не найден");return;}
    var r=ws.getLastRow()+1;
    ws.showRows(r,1);
    var todayD=new Date();
    var dateOnly=new Date(todayD.getFullYear(),todayD.getMonth(),todayD.getDate());
    ws.getRange(r,1).setValue(dateOnly).setNumberFormat("DD.MM.YYYY");
    if(type==="income"){
      ws.getRange(r, 2).setValue(amount).setNumberFormat("#,##0");  // B приход
      ws.getRange(r, 4).setValue(src);                              // D источник
      if(resName)ws.getRange(r, 5).setValue(resName);               // E категория +
      if(!isCash){
        var commission=Math.round(amount*0.04);
        ws.getRange(r, 3).setValue(commission).setNumberFormat("#,##0"); // C расход
        ws.getRange(r, 6).setValue("Комиссия+налог");                  // F категория -
      }
      // ОБНОВЛЯЕМ ДЕБЕТ РЕЗИДЕНТА.
      // ВАЖНО: оплата штрафа НЕ уменьшает долг за продление. Раньше любой приход
      // списывался из колонки E, поэтому оплата штрафа закрывала долг по тарифу
      var isFinePayment = src && String(src).indexOf("Штраф") >= 0;
      if(resName && !isFinePayment){
        try{
          var wsResD=ss.getSheetByName("BS - резиденты дебет");
          if(wsResD){
            // v32: раньше сюда писались колонки 3 и 5 (Тариф и Встреч проведено).
            // Долг гасит bsApplyPaymentsFromDDS по этой строке ДДС (остаток входа,
            // затем долг продления), пакет продлевает addMeetingsOnPayment
            recalcResidents();
          }
        }catch(debetErr){Logger.log("debet update: "+debetErr);}
      } else if(resName && isFinePayment){
        // Оплата штрафа: штрафы уже закрыты выше, дебет не трогаем
        try{ recalcResidents(); }catch(e){}
        Logger.log("Оплата штрафа от "+resName+": долг продления не изменён");
      }
    } else {
      ws.getRange(r, 3).setValue(amount).setNumberFormat("#,##0");  // C расход
      ws.getRange(r, 6).setValue(src);                              // F категория -
    }
    try{recalcPL();}catch(plE){Logger.log("recalcPL: "+plE);}
    var msg="✅ Записано в ОДДС:\n"+(type==="income"?"📈 Приход":"📉 Расход")+": "+amount.toLocaleString()+" тг\n📋 "+src+(isCash?"\n💵 Наличные":"\n🏦 Банк (−4% = "+Math.round(amount*0.04).toLocaleString()+" тг)");
    if(cid!=="mini") tgSend(cid,msg,{reply_markup:JSON.stringify({inline_keyboard:[[{text:"◀️ Меню",callback_data:"admin_menu"}]]})});
  }catch(e){ if(cid==="mini") throw e; tgSend(cid,"Ошибка: "+e.message);}
}

// Обработка текстовых сообщений от админа (сумма для штрафа/приход)
function _handleAdminText(cid,text){
  var amount=parseInt(text.replace(/[^0-9]/g,""));
  if(!amount)return false;
  var cache=CacheService.getScriptCache();
  // Проверяем ожидающий штраф
  var finePend=cache.get("fine_pending_"+cid);
  if(finePend){
    var fp=JSON.parse(finePend);
    cache.remove("fine_pending_"+cid);
    _adminAddFineFromBot(cid,fp.name,fp.type,amount);
    return true;
  }
  // Проверяем ожидающий приход
  var payPend=cache.get("pay_pending_"+cid);
  if(payPend){
    cache.put("pay_amount_"+cid,String(amount),300);
    tgSend(cid,"Сумма: "+amount.toLocaleString()+" тг\nВыберите тип оплаты выше 👆");
    return true;
  }
  return false;
}

function weeklyBackup(){
  // Отключено по решению: копии таблицы больше не создаются.
  // Копия тянула за собой скрипт с зашитым адресом и писала в рабочие данные
  return;
}

function setupNPSTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="sendNPS")ScriptApp.deleteTrigger(t);
  });
  // Каждые 15 дней в 10:00
  // Опрос NPS каждые 60 дней (раз в 2 месяца) в 11:00
  ScriptApp.newTrigger("sendNPS").timeBased().everyDays(60).atHour(11).create();
}

function setupMonthsTrigger(){
  ScriptApp.newTrigger("recalcVisitMonths").timeBased().atHour(6).nearMinute(0).everyDays(1).create();
}

function setupDailyTrigger(){
  // Ночная проверка в 00:05 (отчёты за день)
  ScriptApp.newTrigger("dailyCheck").timeBased().atHour(10).nearMinute(0).everyDays(1).inTimezone("Asia/Almaty").create();
}

function setupReminderTrigger(){
  // Напоминание в 22:00
  ScriptApp.newTrigger("eveningReminder").timeBased().atHour(22).nearMinute(0).everyDays(1).create();
}

// ── onEdit ────────────────────────────────────────────────────────────────
function onTableEdit(e){
  if(bsIsCopy()) return;
  // Данные клуба ведёт сервер: правки в листах клуба ничего не пересчитывают
  if(bsServerIsMaster()){
    try{
      var _sh = e && e.range ? e.range.getSheet().getName() : "";
      if(BS_CLUB_SHEETS.indexOf(_sh) >= 0 && BS_APP_SHEETS.indexOf(_sh) < 0)
        SpreadsheetApp.getActiveSpreadsheet().toast("Данные клуба ведутся на платформе. Эта правка будет перезаписана копией с сервера.", "BS", 8);
    }catch(te){}
    return;
  }
  try{ onEditDebet(e); }catch(err){ Logger.log("debet edit: "+err); }
  if(!e||!e.range)return;
  var ws=e.range.getSheet(),col=e.range.getColumn(),row=e.range.getRow(),val=e.range.getValue();
  var sh=ws.getName();
  // Учет ДДС. обновляем Дебет резидента когда заполнены ОБА: сумма прихода + имя
  if(sh==="Учет ДДС"&&row>=2){
    try{
      var rowInc=Number(ws.getRange(row,2).getValue())||0;
      var rowRes=String(ws.getRange(row,5).getValue()||"").trim();
      // Уникальный ключ ОДНОГО списания: строка + сумма + имя
      var unqKey="dds_applied_"+row+"_"+rowInc+"_"+rowRes;
      var ddsCache=CacheService.getScriptCache();
      // Применяем только если ещё не применяли эту комбинацию
      if(rowInc>0&&rowRes&&!ddsCache.get(unqKey)){
        ddsCache.put(unqKey,"1",3600); // защита от повторного применения
        var ssDDS=SpreadsheetApp.openById(SS_ID);
        var wsResD=ssDDS.getSheetByName("BS - резиденты дебет");
        if(wsResD){
          var lrD=wsResD.getLastRow();
          for(var rr=3;rr<=lrD;rr++){
            if(String(wsResD.getRange(rr,2).getValue()).trim()===rowRes){
              // Обновляем E (Долг продление). уменьшаем на сумму прихода
              var debtNow=Number(wsResD.getRange(rr,5).getValue())||0;
              wsResD.getRange(rr,5).setValue(Math.max(debtNow-rowInc,0));
              break;
            }
          }
        }
      }
      // Флаг для отложенного recalcPL (через минуту триггер запустит)
      PropertiesService.getScriptProperties().setProperty("pl_needs_recalc","1");
    }catch(ddsErr){Logger.log("DDS edit: "+ddsErr);}
    return;
  }
  if(sh==="Штрафы"&&col===2){recalcResidents();return;}
    // Смена тарифа (колонка H): пересчитываем размер пакета встреч
  if(sh==="BS - резиденты дебет" && col===8 && row>=3){
    try{ onTariffChanged(row); }catch(e){ Logger.log("tariff hook: "+e); }
    return;
  }
  // Смена количества встреч в месяц (Посещения, колонка C)
  if(sh==="BS - посещения" && col===3 && row>=3){
    try{
      var vName=String(ws.getRange(row,2).getValue()||"").trim();
      if(vName){
        var wsRt=SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
        if(wsRt){
          var lrRt=wsRt.getLastRow();
          for(var rt=3;rt<=lrRt;rt++){
            if(String(wsRt.getRange(rt,2).getValue()||"").trim()===vName){ onTariffChanged(rt); break; }
          }
        }
      }
    }catch(e){ Logger.log("visits tariff hook: "+e); }
    return;
  }

if(sh==="BS - резиденты дебет"&&col===11&&row>=3&&val==="Да"){
    // Автоудаление отключено: строки смещались и удалялись не те резиденты.
    // Отмечаем строку и просим запустить перенос вручную через меню
    try{
      var fName=String(ws.getRange(row,2).getValue()||"").trim();
      ws.getRange(row,1,1,11).setBackground("#3a2a1a");
      var msg="Резидент "+fName+" отмечен как бывший.\n\n" +
        "Запустите: меню BS → Служебное → Перенести отмеченных в Бывшие";
      try{ SpreadsheetApp.getActiveSpreadsheet().toast(msg, "Отмечено", 8); }catch(e){}
    }catch(e){ Logger.log("mark former: "+e); }
    return;
  }
  // Множественное редактирование колонки Бывший (вставка/протяжка нескольких "Да")
  if(sh==="BS - резиденты дебет"&&col===11&&row>=3&&val===undefined&&e.range&&e.range.getNumRows()>1){
    try{
      var rng=e.range, startRow=rng.getRow(), nRows=rng.getNumRows();
      var vals=ws.getRange(startRow,11,nRows,1).getValues();
      var namesBatch=ws.getRange(startRow,2,nRows,1).getValues();
      var toMove=[];
      for(var bi=0;bi<nRows;bi++){
        if(String(vals[bi][0]||"").trim()==="Да"){
          var bn=String(namesBatch[bi][0]||"").trim();
          if(bn)toMove.push(bn);
        }
      }
      // Только подсветка. Перенос запускается вручную из меню
      if(toMove.length){
        try{ SpreadsheetApp.getActiveSpreadsheet().toast(
          "Отмечено: "+toMove.length+".\nЗапустите перенос из меню BS → Служебное", "Отмечено", 8); }catch(e){}
      }
    }catch(multiE){Logger.log("multi toFormer: "+multiE);}
    return;
  }
  if(sh==="BS - резиденты дебет"&&col===2&&row>=3){
    _renumRes();
    // Если добавлено новое имя. добавляем в посещения
    if(val){
      Utilities.sleep(300);
      _addResidentToVisits(String(val));
    }
    return;
  }
  if(sh==="BS - резиденты дебет"&&col===9&&row>=3){
    // Изменилась дата входа. обновляем месяцы в посещениях
    _updateVisitMonthsRow(row);
    return;
  }
  if(sh==="BS - посещения"&&col===2&&row>=3){
    Utilities.sleep(300);
    var visitName=ws.getRange(row,2).getValue();
    if(visitName)_updateVisitMonthsByName(visitName);
    return;
  }
  if(sh==="BS - резиденты дебет"&&(col===3||col===4||col===5)&&row>=3){_updateResRow(row);return;}
  if(sh==="Бывшие резиденты"&&col===11&&row>=4&&val==="Активный"){_toActive(row);return;}
  if(sh==="BS - посещения"&&col>=4&&col<=7&&row>=3&&val===true){_onCheckbox(ws,row,col);return;}
  if(sh==="BS - посещения"&&col===3&&row>=3){_setTariff(ws,row,String(val));return;}
  if(sh==="Штрафы"&&col===2&&row>=3){_renumFines();return;}
  // Новая запись в Штрафах. уведомляем резидента
  if(sh==="Штрафы"&&col===4&&row>=3){
    var fName=ws.getRange(row,2).getValue();
    var fAmt=Number(ws.getRange(row,4).getValue())||0;
    var fType=ws.getRange(row,3).getValue()||"Штраф";
    if(fName&&fAmt>0){
      // Дата выставления
      if(!ws.getRange(row,5).getValue()){
        ws.getRange(row,5).setValue(new Date()).setNumberFormat("DD.MM.YYYY");
      }
      // Статус по умолчанию
      if(!ws.getRange(row,6).getValue()){
        ws.getRange(row,6).setValue("Не оплатил");
      }
      // Если "Цена слова". особое уведомление
      if(fType==="Цена слова"){
        _notifyWordPrice(row);
      } else {
        // Обычный штраф. уведомляем резидента
        try{
          var resChat=_getChatId(fName);
          if(resChat){
            var fineMsg="⚠️ Тебе выставлен штраф\n\n👤 "+fName.split(" ")[0]+"\n💰 "+fAmt.toLocaleString()+" ₸\n📋 "+fType+"\n📅 "+Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy")+"\n\n💳 Оплатить: "+KASPI_LINK;
            tgSendButton(resChat,fineMsg,"💳 Оплатить",KASPI_LINK);
          } else {
            Logger.log("Штраф для "+fName+": Chat ID не найден");
          }
        }catch(notifyE){Logger.log("Notify fine: "+notifyE);}
      }
      recalcResidents();
    }
    return;
  }  // Расписание: Meet создаётся только при изменении col 2,3,4
  // Защита от дублирования через CacheService
  if(sh==="Расписание"&&(col===2||col===3||col===4)&&row>=3){
    if(col===2&&val)ws.getRange(row,1).setValue(row-2);
    if(col===4){ // только при заполнении времени
      var hasRes=ws.getRange(row,2).getValue();
      var hasDate=ws.getRange(row,3).getValue();
      var hasLink=ws.getRange(row,6).getValue();
      if(hasRes&&hasDate&&!hasLink){
        // Защита от дублирования
        var schedCache=CacheService.getScriptCache();
        var schedKey="sched_meet_"+row+"_"+new Date().toDateString();
        if(!schedCache.get(schedKey)){
          schedCache.put(schedKey,"1",3600);
          SpreadsheetApp.flush();
          _autoCreateMeet(row);
        }
      }
    }
    return;
  }

  if(sh==="Штрафы"&&col===6&&row>=3&&val==="Оплатил"){
    // Защита от тройного срабатывания
    var paidCache=CacheService.getScriptCache();
    var paidKey="fine_paid_"+row+"_"+Utilities.formatDate(new Date(),"Asia/Almaty","yyyyMMdd");
    if(paidCache.get(paidKey)){Logger.log("Штраф уже обработан: "+row);return;}
    paidCache.put(paidKey,"1",300);
    // Читаем данные через spreadsheet ID
    var ssF=SpreadsheetApp.openById(SS_ID);
    var wsFines=ssF.getSheetByName("Штрафы");
    var fName=wsFines.getRange(row,2).getValue();
    var fAmount=Number(wsFines.getRange(row,4).getValue())||0;
    var fType=wsFines.getRange(row,3).getValue()||"Штраф";
    Logger.log("Оплатил: name="+fName+" amount="+fAmount+" type="+fType);
    if(!fName){Logger.log("Нет имени. выход");return;}
    // Подсветка
    wsFines.getRange(row,1,1,6).setBackground(GRN_BG);
    SpreadsheetApp.flush();
    Utilities.sleep(1500);
    // Удаляем строку
    wsFines.deleteRow(row);
    _renumFines();
    recalcResidents();
    // Пишем в ОДДС
    _writeToDDS(fName,fAmount,fType);
    return;
  }
}

// ── Пересчёт штрафов и G ─────────────────────────────────────────────────
function recalcResidents(){
  if(bsIsCopy()) return;
  // Делегируем безопасной функции с уникальным именем
  try{ bsSafeRecalcDebet(); }catch(e){ Logger.log("recalcResidents: "+e); }
}

function _addResidentToVisits(resName){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsV=ss.getSheetByName("BS - посещения");
    if(!wsV) return; // лист удалён, данные в дебете
    // Проверяем нет ли уже такого резидента
    var lrV=wsV.getLastRow();
    for(var r=3;r<=lrV;r++){
      if(wsV.getRange(r,2).getValue()===resName)return; // уже есть
    }
    // Находим первую пустую строку
    var newRow=lrV+1;
    for(var r=3;r<=lrV;r++){
      if(!wsV.getRange(r,2).getValue()){newRow=r;break;}
    }
    var dvTar=SpreadsheetApp.newDataValidation().requireValueInList(["3","4"],true).setAllowInvalid(false).build();
    var bg=(newRow-3)%2===0?WHT:G1;
    wsV.setRowHeight(newRow,26);
    wsV.getRange(newRow,1,1,10).setBackground(bg).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
    wsV.getRange(newRow,1).setValue(newRow-2).setHorizontalAlignment("center");
    wsV.getRange(newRow,2).setValue(resName).setHorizontalAlignment("left").setFontWeight("bold");
    wsV.getRange(newRow,3).setValue("4").setDataValidation(dvTar).setHorizontalAlignment("center");
    wsV.getRange(newRow,4,1,3).insertCheckboxes();
    wsV.getRange(newRow,7).insertCheckboxes();
    wsV.getRange(newRow,8).setValue("").setHorizontalAlignment("center").setFontColor("#0000CC").setFontWeight("bold");
    wsV.getRange(newRow,9).setHorizontalAlignment("center").setFontColor("#8B0000").setFontWeight("bold").setNumberFormat("#,##0");
    wsV.getRange(newRow,10).setValue(0);
    B(wsV.getRange(newRow,1,1,10));
    Logger.log("Добавлен в посещения: "+resName);
  }catch(e){Logger.log("_addResidentToVisits: "+e);}
}

function _updateResRow(row){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("BS - резиденты дебет");if(!ws)return;
  var d=Number(ws.getRange(row, RC.rest).getValue())||0;
  var e=Number(ws.getRange(row, RC.renew).getValue())||0;
  var f=Number(ws.getRange(row, RC.fine).getValue())||0;
  ws.getRange(row, RC.total).setValue(d+e+f);
}

function restoreDamil(){
  // Восстановление Дамила: пропал из дебета и не попал в Бывшие (баг каскадного удаления).
  // Данные восстановлены из Лога отчётов, Посещений, ДДС и Расписания
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR)return "Лист не найден";
    
    // Уже есть?
    var lr=wsR.getLastRow();
    for(var r=3;r<=lr;r++){
      if(String(wsR.getRange(r, RC.name).getValue()||"").trim()==="Дамил"){
        return "Дамил уже в списке (строка "+r+")";
      }
    }
    
    // Вставляем перед Олжасом (партнёры рядом), иначе в конец
    var insertAt=lr+1;
    for(var r2=3;r2<=lr;r2++){
      if(String(wsR.getRange(r2, RC.name).getValue()||"").trim()==="Олжас"){insertAt=r2+1;break;}
    }
    wsR.insertRowAfter(insertAt-1);
    
    var row=insertAt;
    wsR.getRange(row, RC.name).setValue("Дамил");
    wsR.getRange(row, RC.paid).setValue(450000);      // оплачено (ДДС 17.04 продление)
    wsR.getRange(row, RC.rest).setValue(0);           // остаток
    wsR.getRange(row, RC.renew).setValue(0);           // долг продление
    wsR.getRange(row, RC.tariff).setValue(450000);      // тариф
    wsR.getRange(row, RC.date).setValue(new Date(2026,3,17)).setNumberFormat("DD.MM.YYYY"); // дата входа
    wsR.getRange(row, RC.source).setValue("Восстановлен");
    wsR.getRange(row, RC.former).setValue("Нет");      // Бывший
    wsR.getRange(row, RC.except).setValue("Нет");      // Исключение
    wsR.getRange(row, RC.chat).setValue("853315734"); // Chat ID из Лога отчётов
    wsR.getRange(row, RC.format).setValue("Офлайн");
    wsR.getRange(row, RC.admin).setValue("Нет");      // Админ
    wsR.getRange(row, RC.done).setValue(1);          // циклов оплачено
    wsR.getRange(row, RC.granted).setValue(0);
    wsR.getRange(row, RC.partner).setValue("Олжас");    // партнёр
    
    // Партнёрство обоюдное
    for(var r3=3;r3<=wsR.getLastRow();r3++){
      if(String(wsR.getRange(r3, RC.name).getValue()||"").trim()==="Олжас"){
        wsR.getRange(r3, RC.partner).setValue("Дамил");
        break;
      }
    }
    
    _renumRes();
    try{_addResidentToVisits("Дамил");}catch(e){}
    try{recalcResidents();}catch(e){}
    
    var msg="✅ Дамил восстановлен\n\nСтрока: "+row+"\nChat ID: 853315734\nПартнёр: Олжас (взаимно)\nОплачено: 450 000 (ДДС 17.04)\n\nПроверь оплаты и тариф вручную.";
    ADMIN_IDS.forEach(function(aid){try{tgSend(aid,msg);}catch(e){}});
    return msg;
  }catch(e){
    return "❌ restoreDamil: "+e;
  }
}

function cleanupAllData(silent){
  // Одна кнопка вместо пяти: убирает дубли встреч, чинит Бывших, колонки и нумерацию
  var report=["🧹 Наведение порядка",""];
  try{ report.push(removeDuplicateMeetings(true).split("\n")[2]||"Встречи: ок"); }catch(e){ report.push("Встречи: "+e); }
  try{ _ensurePartnerColumn(); report.push("Колонка Партнёр: ок"); }catch(e){}
  try{ fixAdminPartnerColumns(); report.push("Колонки Админ/Партнёр: ок"); }catch(e){}
  try{
    var r=fixFormerResidents(true);
    var dupLine=(r.match(/Удалено дублей: \d+/)||["Бывшие: ок"])[0];
    report.push(dupLine);
  }catch(e){ report.push("Бывшие: "+e); }
  try{ _renumRes(); report.push("Нумерация: ок"); }catch(e){}
  try{ syncVisitsOrder(); report.push("Порядок посещений: ок"); }catch(e){}
  var msg=report.join("\n");
  if(!silent){
    ADMIN_IDS.forEach(function(aid){try{tgSend(aid,msg);}catch(e){}});
  }
  Logger.log(msg);
  return msg;
}

function fixPartnerLinks(){
  // Приводит ссылки партнёров к одной: если у пары на одну дату разные Meet,
  // ставим обоим ту, что создана раньше, и уведомляем обоих
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var sheet=ss.getSheetByName("Расписание");
    if(!sheet)return "Нет листа Расписание";
    var lr=sheet.getLastRow();
    if(lr<3)return "Расписание пусто";

    var data=sheet.getRange(3, 1, lr-2, 21).getValues();
    var report=[], fixed=0;

    for(var i=0;i<data.length;i++){
      var name=String(data[i][1]||"").trim();
      if(!name)continue;
      var partner=_getPartnerName(name);
      if(!partner)continue;

      var d=data[i][2];
      var dStr=d instanceof Date?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d);
      var myLink=String(data[i][5]||"").trim();

      // Ищем партнёра на ту же дату
      for(var j=0;j<data.length;j++){
        if(j===i)continue;
        if(String(data[j][1]||"").trim()!==partner)continue;
        var pd=data[j][2];
        var pdStr=pd instanceof Date?Utilities.formatDate(pd,"Asia/Almaty","dd.MM.yyyy"):String(pd);
        if(pdStr!==dStr && pdStr.substring(0,5)!==dStr.substring(0,5))continue;

        var pLink=String(data[j][5]||"").trim();
        if(myLink && pLink && myLink!==pLink){
          // Разные ссылки: оставляем ту, что у строки выше (создана раньше)
          var keep = i < j ? myLink : pLink;
          sheet.getRange(3+i,6).setValue(keep);
          sheet.getRange(3+j,6).setValue(keep);
          report.push(name+" и "+partner+" на "+dStr+": ссылки объединены");
          fixed++;
          // Уведомляем обоих
          [name, partner].forEach(function(nm){
            try{
              var cid=_getChatId(nm);
              if(cid)tgSend(cid,"🤝 Партнёрская встреча "+dStr+"\n\n🔗 "+keep+"\n\nСсылка одна на двоих.");
            }catch(e){}
          });
        } else if(myLink && !pLink){
          sheet.getRange(3+j,6).setValue(myLink);
          report.push(partner+" на "+dStr+": добавлена ссылка");
          fixed++;
          try{
            var cid2=_getChatId(partner);
            if(cid2)tgSend(cid2,"🤝 Партнёрская встреча "+dStr+"\n\n🔗 "+myLink+"\n\nСсылка одна на двоих.");
          }catch(e){}
        }
        break;
      }
    }

    var msg="🤝 Партнёрские ссылки\n\nИсправлено: "+fixed;
    if(report.length)msg+="\n\n"+report.join("\n");
    else msg+="\n\nВсё уже синхронизировано";
    Logger.log(msg);
    ADMIN_IDS.forEach(function(aid){try{tgSend(aid,msg);}catch(e){}});
    return msg;
  }catch(e){
    return "Ошибка fixPartnerLinks: "+e;
  }
}

function removeDuplicateMeetings(silent){
  // Удаляет дубли встреч в Расписании (один резидент, одна дата, одно время)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Расписание");
    if(!ws)return "Лист не найден";
    var lr=ws.getLastRow();
    if(lr<3)return "Расписание пусто";
    
    var data=ws.getRange(3, 1, lr-2, 21).getValues();
    var seen={}, dupRows=[], report=[];
    for(var i=0;i<data.length;i++){
      var name=String(data[i][1]||"").trim();
      if(!name)continue;
      var d=data[i][2];
      var t=data[i][3];
      var dStr=d instanceof Date?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d).trim();
      var tStr=t instanceof Date?Utilities.formatDate(t,"Asia/Almaty","HH:mm"):String(t).trim();
      var key=name+"|"+dStr+"|"+tStr;
      if(seen[key]){
        dupRows.push(3+i);
        report.push(name+" "+dStr+" "+tStr+" (строка "+(3+i)+")");
      } else {
        seen[key]=3+i;
      }
    }
    
    // Удаляем снизу вверх
    dupRows.sort(function(a,b){return b-a;});
    dupRows.forEach(function(r){
      try{ws.deleteRow(r);}catch(e){}
    });
    
    // Перенумерация
    var lr2=ws.getLastRow();
    if(lr2>=3){
      var nums=[];
      for(var n=0;n<lr2-2;n++)nums.push([n+1]);
      ws.getRange(3, 1, nums.length, 21).setValues(nums);
    }
    
    var msg="🗓 Очистка дублей в Расписании\n\nУдалено: "+dupRows.length;
    if(report.length)msg+="\n\n"+report.join("\n");
    else msg+="\n\nДублей не найдено";
    Logger.log(msg);
    if(!silent)ADMIN_IDS.forEach(function(aid){try{tgSend(aid,msg);}catch(e){}});
    return msg;
  }catch(e){
    return "❌ removeDuplicateMeetings: "+e;
  }
}

function fixFormerResidents(silent){
  // РЕМОНТ: убирает дубли в "Бывшие резиденты", перенумеровывает,
  // и показывает кого можно вернуть в актив (если удалили случайно)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsF=ss.getSheetByName("Бывшие резиденты");
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsF||!wsR)return "Листы не найдены";
    
    var lrF=wsF.getLastRow();
    if(lrF<4)return "Лист Бывших пуст";
    
    var data=wsF.getRange(4,1,lrF-3,11).getValues();
    var seen={}, dupRows=[], kept=[];
    for(var i=0;i<data.length;i++){
      var nm=String(data[i][1]||"").trim();
      if(!nm){dupRows.push(4+i);continue;}
      if(seen[nm]){dupRows.push(4+i);continue;} // дубль
      seen[nm]=true;
      kept.push(nm);
    }
    
    // Удаляем дубли снизу вверх
    dupRows.sort(function(a,b){return b-a;});
    dupRows.forEach(function(dr){
      try{wsF.deleteRow(dr);}catch(e){}
    });
    
    // Перенумерация Бывших
    var lrF2=wsF.getLastRow();
    if(lrF2>=4){
      var nums=[];
      for(var n=0;n<lrF2-3;n++)nums.push([n+1]);
      wsF.getRange(4,1,nums.length,1).setValues(nums);
    }
    
    // Кто есть и в Бывших, и в активных (конфликт)
    var conflicts=[];
    var lrR=wsR.getLastRow();
    if(lrR>=3){
      var activeNames=wsR.getRange(3, 2, lrR-2, 20).getValues();
      activeNames.forEach(function(an){
        var a=String(an[0]||"").trim();
        if(a&&seen[a])conflicts.push(a);
      });
    }
    
    _renumRes();
    
    var msg="🔧 Ремонт Бывших резидентов\n\n";
    msg+="Удалено дублей: "+dupRows.length+"\n";
    msg+="Осталось в Бывших: "+kept.length+"\n";
    if(conflicts.length)msg+="\n⚠️ Есть и в активных, и в Бывших: "+conflicts.join(", ")+"\n(проверь вручную)";
    msg+="\n\nСписок Бывших:\n"+kept.map(function(k,i){return (i+1)+". "+k;}).join("\n");
    
    Logger.log(msg);
    if(!silent)ADMIN_IDS.forEach(function(aid){try{tgSend(aid,msg);}catch(e){}});
    return msg;
  }catch(e){
    var err="❌ fixFormerResidents: "+e;
    Logger.log(err);
    return err;
  }
}

function restoreFormerToActive(){
  // Возврат резидента из Бывших в активные по имени (запрашивает через диалог)
  try{
    var ui=SpreadsheetApp.getUi();
    var resp=ui.prompt("Вернуть резидента","Имя резидента из листа Бывшие (точно как написано):",ui.ButtonSet.OK_CANCEL);
    if(resp.getSelectedButton()!==ui.Button.OK)return;
    var name=String(resp.getResponseText()||"").trim();
    if(!name)return;
    
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsF=ss.getSheetByName("Бывшие резиденты");
    if(!wsF)return;
    var lrF=wsF.getLastRow();
    for(var r=4;r<=lrF;r++){
      if(String(wsF.getRange(r,2).getValue()||"").trim()===name){
        wsF.getRange(r,11).setValue("Активный");
        _toActive(r);
        ui.alert("✅ "+name+" возвращён в активные");
        return;
      }
    }
    ui.alert("Не найден: "+name);
  }catch(e){
    Logger.log("restoreFormerToActive: "+e);
  }
}

function _renumRes(){
  // Нумерация: админы и бывшие без номера, счёт с первого активного резидента.
  // Раньше номера сбивались после перевода в Бывшие
  try{
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws)return;
    var lr=ws.getLastRow();
    if(lr<3)return;
    var data=ws.getRange(3, 1, lr-2, 21).getValues();
    var nums=[], n=0;
    for(var i=0;i<data.length;i++){
      var name=String(data[i][1]||"").trim();
      var isAdmin=String(data[i][15]||"").trim()==="Да";
      var isFormer=String(data[i][10]||"").trim()==="Да";
      if(!name || isAdmin || isFormer) nums.push([""]);
      else nums.push([++n]);
    }
    ws.getRange(3, 1, nums.length, 21).setValues(nums);
    Logger.log("_renumRes: активных "+n);
  }catch(e){ Logger.log("_renumRes: "+e); }
}
function _fixFinesHeader(){
  // Восстанавливает заголовки листа "Штрафы" если они затёрты
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Штрафы");
    if(!ws)return;
    
    // Строка 1 - заголовок таблицы (merged)
    var titleRange=ws.getRange("A1:F1");
    if(!titleRange.isPartOfMerge()){
      titleRange.merge();
    }
    titleRange.setValue("BS · РЕЕСТР ШТРАФОВ")
      .setBackground("#000000").setFontColor("#FFFFFF")
      .setFontFamily("Montserrat").setFontSize(12).setFontWeight("bold")
      .setHorizontalAlignment("center").setVerticalAlignment("middle");
    ws.setRowHeight(1,40);
    
    // Строка 2 - заголовки колонок
    var heads=["№","Имя резидента","Тип нарушения","Сумма, тг","Дата","Статус"];
    ws.getRange(2,1,1,6).setValues([heads])
      .setBackground("#000000").setFontColor("#FFFFFF")
      .setFontFamily("Montserrat").setFontSize(10).setFontWeight("bold")
      .setHorizontalAlignment("center").setVerticalAlignment("middle").setWrap(true);
    
    // Зелёный фон у "Статус"
    ws.getRange(2,6).setBackground("#1A3A1A").setFontColor("#66FF66");
    ws.setRowHeight(2,40);
    ws.setFrozenRows(2);
    
  }catch(e){Logger.log("_fixFinesHeader: "+e);}
}

function _renumFines(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Штрафы");if(!ws)return;
  var lr=ws.getLastRow(),num=0;
  for(var r=3;r<=lr;r++)ws.getRange(r,1).setValue(ws.getRange(r,2).getValue()?++num:"");
}
function _finePaid(ws,row){
  // Вызывается из триггера. данные читаются там
  Logger.log("_finePaid called for row "+row);
}



function fixAdminPartnerColumns(){
  // Структура BS - резиденты дебет:
  // P=16=Админ, Q=17=Циклов оплачено, R=18=Циклов использовано, S=19=Партнёр
  // Эта функция восстанавливает заголовки и данные после моей путаницы
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR){
      var msg="❌ Лист BS - резиденты дебет не найден";
      ADMIN_IDS.forEach(function(a){tgSend(a,msg);});
      return msg;
    }
    
    var col16Header=String(wsR.getRange(2, RC.admin).getValue()||"").trim();
    var col17Header=String(wsR.getRange(2, RC.done).getValue()||"").trim();
    var col19Header=String(wsR.getRange(2, RC.partner).getValue()||"").trim();
    
    var report=["🔧 Восстановление колонок резидентов",""];
    report.push("До: P=\""+col16Header+"\", Q=\""+col17Header+"\", S=\""+col19Header+"\"");
    report.push("");
    
    var lr=wsR.getLastRow();
    
    // Сценарий A: P стал "Партнёр" - перенесём данные в S (19) и вернём заголовок Админ в P
    if(col16Header.indexOf("Партнёр")>=0){
      report.push("⚠️ В P был неправильный заголовок \"Партнёр\"");
      if(lr>=3){
        var dataP=wsR.getRange(3,16,lr-2,1).getValues();
        var movedCount=0;
        for(var i=0;i<dataP.length;i++){
          var val=dataP[i][0];
          if(val){
            // Переносим в S (19) только если там пусто
            if(!wsR.getRange(3+i, RC.partner).getValue()){
              wsR.getRange(3+i, RC.partner).setValue(val);
              movedCount++;
            }
          }
        }
        report.push("✅ Перенесено "+movedCount+" значений из P в S");
        wsR.getRange(3,16,lr-2,1).clearContent();
        report.push("🧹 Очищена колонка P (под Админ)");
      }
      wsR.getRange(2, RC.admin).setValue("Админ").setFontWeight("bold");
      report.push("✅ Заголовок \"Админ\" в P восстановлен");
    } else if(!col16Header){
      wsR.getRange(2, RC.admin).setValue("Админ").setFontWeight("bold");
      report.push("✅ Заголовок \"Админ\" в P установлен");
    } else {
      report.push("✅ P (Админ) уже в порядке");
    }
    
    // Сценарий B: в Q (17) могло быть переписано если я ставил Партнёр туда
    // Но Q=Циклов оплачено - это числа! Если там вдруг текст имени - исправим
    if(col17Header.indexOf("Партнёр")>=0){
      report.push("⚠️ В Q был \"Партнёр\" вместо \"Циклов оплачено\"");
      if(lr>=3){
        var dataQ=wsR.getRange(3,17,lr-2,1).getValues();
        var movedCountQ=0;
        for(var iq=0;iq<dataQ.length;iq++){
          var valQ=dataQ[iq][0];
          // Если это текст имени - перенесём в S
          if(valQ && isNaN(Number(valQ)) && !wsR.getRange(3+iq, RC.partner).getValue()){
            wsR.getRange(3+iq, RC.partner).setValue(valQ);
            wsR.getRange(3+iq, RC.done).setValue(""); // очистим Q
            movedCountQ++;
          }
        }
        report.push("✅ Перенесено "+movedCountQ+" имён из Q в S");
      }
      wsR.getRange(2, RC.done).setValue("Циклов оплачено").setFontWeight("bold");
      report.push("✅ Заголовок \"Циклов оплачено\" в Q восстановлен");
    }
    
    // Заголовок S (Партнёр)
    if(!col19Header || col19Header.indexOf("Партнёр")<0){
      wsR.getRange(2, RC.partner).setValue("Партнёр").setFontWeight("bold");
      report.push("✅ Заголовок \"Партнёр\" в S установлен");
    } else {
      report.push("✅ S (Партнёр) уже в порядке");
    }
    
    report.push("");
    report.push("📋 Финальная структура:");
    report.push("P=Админ, Q=Циклов оплачено, R=Циклов использовано, S=Партнёр");
    
    var fullReport=report.join("\n");
    ADMIN_IDS.forEach(function(a){
      try{tgSend(a,fullReport);}catch(e){}
    });
    return fullReport;
  }catch(e){
    var err="❌ fixAdminPartnerColumns: "+e;
    ADMIN_IDS.forEach(function(a){try{tgSend(a,err);}catch(e2){}});
    return err;
  }
}

function _ensurePartnerColumn(){
  // Гарантирует существование колонки "Партнёр" (колонка S = 19) в "BS - резиденты дебет"
  // ВАЖНО: P=16=Админ, Q=17=Циклов оплачено, R=18=Циклов использовано - НЕ ТРОГАЕМ
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR)return false;
    
    // Заголовок Партнёр в S=19
    var headerVal=wsR.getRange(2, RC.partner).getValue();
    if(!headerVal || String(headerVal).indexOf("Партнёр")<0){
      wsR.getRange(2, RC.partner).setValue("Партнёр").setFontWeight("bold");
    }
    return true;
  }catch(e){
    Logger.log("_ensurePartnerColumn: "+e);
    return false;
  }
}

function _miniRenewMeetings(p){
  // Ручное продление из приложения: обнулить счётчик и выдать новый пакет
  try{
    var name=String(p.name||"").trim();
    if(!name) return {error:"Нет имени"};
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws) return {error:"Нет листа"};
    var lr=ws.getLastRow();
    for(var r=3;r<=lr;r++){
      if(String(ws.getRange(r,2).getValue()||"").trim()!==name) continue;
      var tariffSum=Number(ws.getRange(r, RC.tariff).getValue())||0;
      var pack=_meetingsPerPackage(name, tariffSum);
      var done=Number(ws.getRange(r, RC.done).getValue())||0;
      var granted=Number(ws.getRange(r, RC.granted).getValue())||0;
      var left=granted-done;
      ws.getRange(r, RC.done).setValue(0);
      ws.getRange(r, RC.granted).setValue(Math.max(0, pack+left));
      _invalidateBundleCache();
        try{
    bsNotifyResident(p.name || p.res, "Пакет продлён" +
      (p.count ? " на " + p.count + " встреч" : "") + ". Хорошей работы");
  }catch(nE){ Logger.log("notify renew: " + nE); }
  return {ok:true, pack:pack, carried:left, total:Math.max(0,pack+left)};
    }
    return {error:"Резидент не найден"};
  }catch(e){ return {error:String(e)}; }
}

// Поля резидента, которые можно исправить одной ячейкой
var BS_RESIDENT_FIELDS = {tariff: "tariff", meetingsGranted: "granted", meetingsDone: "done",
  paidEntry: "paid", restEntry: "rest", renewDebt: "renew", months: "months", chatId: "chat",
  partner: "partner", note: "notes"};

function _miniSetResidentField(p){
  // Исправление одной ячейки резидента (проверка данных на платформе)
  try{
    var name = String(p.name||"").trim(), field = String(p.field||"").trim();
    var value = String(p.value === undefined ? "" : p.value).trim();
    if(!name) return {error: "Нет имени"};
    var key = BS_RESIDENT_FIELDS[field];
    if(!key) return {error: "Поле «" + field + "» не меняется"};
    var text = (field === "partner" || field === "note" || field === "chatId");
    var v = value;
    if(!text){
      v = value === "" ? 0 : Number(value.replace(/[\s,]/g, ""));
      if(isNaN(v) || v < 0) return {error: "«" + value + "» не число"};
    } else if(field === "chatId" && value !== "" && !/^\d+$/.test(value)){
      return {error: "Chat ID «" + value + "» не число"};
    }
    var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws) return {error: "Нет листа"};
    var lr = ws.getLastRow();
    for(var r = 3; r <= lr; r++){
      if(String(ws.getRange(r, RC.name).getValue()||"").trim() === name){
        if(field === "months" && v === 0) ws.getRange(r, RC.months).setValue("");
        else ws.getRange(r, RC[key]).setValue(v);
        if(field === "tariff" || field === "restEntry" || field === "renewDebt"){
          try{ bsSafeRecalcDebet(); }catch(e){}
        }
        _invalidateBundleCache();
        return {ok: true};
      }
    }
    return {error: "Резидент не найден"};
  }catch(e){ return {error: String(e)}; }
}

function _miniSetMeetings(p){
  // Ручная правка счётчиков встреч из приложения
  try{
    var name=String(p.name||"").trim();
    var done=parseInt(p.done);
    var granted=parseInt(p.granted);
    if(!name) return {error:"Нет имени"};
    if(isNaN(done)||isNaN(granted)) return {error:"Нужны числа"};

    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws) return {error:"Нет листа"};
    var lr=ws.getLastRow();
    for(var r=3;r<=lr;r++){
      if(String(ws.getRange(r,2).getValue()||"").trim()===name){
        ws.getRange(r, RC.done).setValue(Math.max(0,done));
        ws.getRange(r, RC.granted).setValue(Math.max(0,granted));
        _invalidateBundleCache();
        return {ok:true};
      }
    }
    return {error:"Резидент не найден"};
  }catch(e){ return {error:String(e)}; }
}

function _applyRowStyle(ws, row, lastCol, altRow){
  // Единое оформление строки: шрифт Montserrat, чередование фона, границы.
  // Строки, созданные скриптом, раньше приходили Arial и выбивались из таблицы
  try{
    var rng = ws.getRange(row, 1, 1, lastCol);
    rng.setFontFamily("Montserrat").setFontSize(10).setFontWeight("normal")
       .setVerticalAlignment("middle");
    rng.setBackground(altRow ? "#F5F5F5" : "#FFFFFF");
    rng.setBorder(true, true, true, true, true, true, "#D9D9D9", SpreadsheetApp.BorderStyle.SOLID);
    ws.setRowHeight(row, 26);
  }catch(e){ Logger.log("_applyRowStyle: " + e); }
}

function setResidentTariff(){
  // Смена тарифа резидента через диалог: сумма и пакет встреч пересчитываются вместе
  try{
    var ui = SpreadsheetApp.getUi();
    var ss = SpreadsheetApp.openById(SS_ID);
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    if(!wsR) return;

    var resp = ui.prompt("Смена тарифа", "Имя резидента (как в таблице):", ui.ButtonSet.OK_CANCEL);
    if(resp.getSelectedButton() !== ui.Button.OK) return;
    var name = resp.getResponseText().trim();
    if(!name) return;

    var lr = wsR.getLastRow(), row = -1;
    for(var r = 3; r <= lr; r++){
      if(String(wsR.getRange(r, RC.name).getValue()||"").trim() === name){ row = r; break; }
    }
    if(row < 0){ ui.alert("Резидент " + name + " не найден"); return; }

    var cur = Number(wsR.getRange(row, RC.tariff).getValue()) || 0;
    var resp2 = ui.prompt("Тариф для " + name,
      "Сейчас: " + cur.toLocaleString() + " тг\n\n" +
      "Введите новую сумму:\n" +
      "1250000 или 1000000 = год (36 встреч)\n" +
      "500000 = 3 месяца (9 встреч)\n" +
      "150000 = месяц (3 встречи)",
      ui.ButtonSet.OK_CANCEL);
    if(resp2.getSelectedButton() !== ui.Button.OK) return;
    var newTariff = Number(String(resp2.getResponseText()).replace(/[^0-9]/g,"")) || 0;
    if(newTariff <= 0){ ui.alert("Некорректная сумма"); return; }

    wsR.getRange(row, RC.tariff).setValue(newTariff);
    var pack = _meetingsPerPackage(name, newTariff);
    var done = Number(wsR.getRange(row,RC.done).getValue()) || 0;
    wsR.getRange(row,RC.granted).setValue(pack);
    _invalidateBundleCache();

    var msg = "🔁 " + name + "\n\n" +
      "Тариф: " + cur.toLocaleString() + " → " + newTariff.toLocaleString() + " тг\n" +
      "Пакет встреч: " + pack + "\n" +
      "Проведено: " + done;
    ui.alert(msg);
    ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, msg); }catch(e){} });
  }catch(e){
    try{ SpreadsheetApp.getUi().alert("Ошибка: " + e); }catch(x){}
  }
}

function _fixKnownTariffs(){
  // Разовые исправления тарифов, которые были записаны неверно.
  // Жасик перешёл на год, но в таблице осталась квартальная сумма
  var fixes = {"Жасик Астана": 1000000};
  var done = [];
  try{
    var wsR = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!wsR) return done;
    var lr = wsR.getLastRow();
    for(var r = 3; r <= lr; r++){
      var nm = String(wsR.getRange(r, RC.name).getValue()||"").trim();
      if(!fixes[nm]) continue;
      var cur = Number(wsR.getRange(r, RC.tariff).getValue()) || 0;
      if(cur !== fixes[nm]){
        wsR.getRange(r, RC.tariff).setValue(fixes[nm]);
        done.push(nm + ": тариф " + cur.toLocaleString() + " → " + fixes[nm].toLocaleString());
      }
    }
  }catch(e){ Logger.log("_fixKnownTariffs: " + e); }
  return done;
}






function _alert(m){
  try{ SpreadsheetApp.getUi().alert(m); }catch(e){}
  return m;
}




// ═══════════════════════════════════════════════════════════════
// ВОССТАНОВЛЕНИЕ И ПЕРЕСЧЁТ
// ═══════════════════════════════════════════════════════════════
// Месяц, с которого считается касса. 3 = апрель, счёт был обнулён
var CASH_START_MONTH = 3;
var BS_VERSION = "2026-10-02-34";

// ═══════════════════════════════════════════════════════════════
// КАРТА КОЛОНОК ЛИСТА РЕЗИДЕНТОВ
// Номера колонок (1 = A). Индексы от B используются в getRange(3,2,...)
// При изменении порядка меняется только эта карта
// ═══════════════════════════════════════════════════════════════
var RC = {
  num:1, name:2, tariff:3, granted:4, done:5, left:6,
  paid:7, rest:8, renew:9, fine:10, total:11,
  format:12, chat:13, former:14, except:15, admin:16,
  source:17, date:18, months:19,
  notes:20, partner:21
};
// Индексы для массива, прочитанного с колонки B
var RI = {
  name:0, tariff:1, granted:2, done:3, left:4,
  paid:5, rest:6, renew:7, fine:8, total:9,
  format:10, chat:11, former:12, except:13, admin:14,
  source:15, date:16, months:17,
  notes:18, partner:19
};
var RC_LAST = 21;
// Индексы для чтения с колонки A (getRange(3,1,...))
var RA = {
  num:0, name:1, tariff:2, granted:3, done:4, left:5,
  paid:6, rest:7, renew:8, fine:9, total:10,
  format:11, chat:12, former:13, except:14, admin:15,
  source:16, date:17, months:18, notes:19, partner:20
};


var RESIDENTS_BACKUP = [{"n": "Рустам", "t": 0, "g": 0, "dn": 0, "r": 0, "rn": 0, "f": 30000, "fm": "Онлайн", "c": "4538009510", "b": "Нет", "ex": "Да", "ad": "Да", "src": "", "d": "2024-12-28", "m": 21, "nt": "", "pt": "", "p": 0}, {"n": "Береке", "t": 0, "g": 0, "dn": 0, "r": 0, "rn": 0, "f": 30000, "fm": "Онлайн", "c": "12855962490", "b": "Нет", "ex": "Да", "ad": "Да", "src": "", "d": "2024-12-28", "m": 21, "nt": "", "pt": "", "p": 0}, {"n": "Альтаир", "t": 100000, "g": 3, "dn": 2, "r": 0, "rn": 0, "f": 0, "fm": "Офлайн", "c": "4906856050", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Знакомый", "d": "2024-12-28", "m": 21, "nt": "", "pt": "", "p": 200000}, {"n": "Дмитрий Цой", "t": 100000, "g": 3, "dn": 1, "r": 0, "rn": 700000, "f": 0, "fm": "Офлайн", "c": "8741090460", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Знакомый", "d": "2025-01-01", "m": 20, "nt": "", "pt": "", "p": 200000}, {"n": "Асет", "t": 100000, "g": 3, "dn": 2, "r": 0, "rn": 0, "f": 10000, "fm": "Офлайн", "c": "4787575020", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Знакомый", "d": "2025-03-03", "m": 18, "nt": "", "pt": "", "p": 100000}, {"n": "Елена", "t": 300000, "g": 4, "dn": 1, "r": 0, "rn": 100000, "f": 30000, "fm": "Онлайн", "c": "11043387590", "b": "Нет", "ex": "Да", "ad": "Нет", "src": "Выступление", "d": "2025-06-02", "m": 15, "nt": "", "pt": "", "p": 100000}, {"n": "Азамат TV", "t": 1000000, "g": 36, "dn": 4, "r": 0, "rn": 0, "f": 10000, "fm": "Офлайн", "c": "8560157080", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Другое", "d": "2025-08-20", "m": 13, "nt": "", "pt": "", "p": 800000}, {"n": "Даулет Сайты", "t": 200000, "g": 3, "dn": 0, "r": 0, "rn": 200000, "f": 0, "fm": "Онлайн", "c": "2797320740", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Сарафан", "d": "2026-01-17", "m": 8, "nt": "", "pt": "", "p": 400000}, {"n": "Арлан", "t": 1000000, "g": 36, "dn": 4, "r": 800000, "rn": 0, "f": 10000, "fm": "Офлайн", "c": "13199003160", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Сарафан", "d": "2026-02-19", "m": 7, "nt": "", "pt": "", "p": 0}, {"n": "Ербол", "t": 100000, "g": 3, "dn": 1, "r": 0, "rn": 0, "f": 30000, "fm": "Онлайн", "c": "6080171820", "b": "Нет", "ex": "Да", "ad": "Нет", "src": "Знакомый", "d": "2026-04-01", "m": 5, "nt": "", "pt": "", "p": 500000}, {"n": "Дамил", "t": 500000, "g": 9, "dn": 2, "r": 0, "rn": 0, "f": 30000, "fm": "Онлайн", "c": "8533157340", "b": "Нет", "ex": "Да", "ad": "Нет", "src": "Сарафан", "d": "2026-04-17", "m": 5, "nt": "", "pt": "Олжас", "p": 500000}, {"n": "Олжас", "t": 500000, "g": 9, "dn": 2, "r": 0, "rn": 150000, "f": 30000, "fm": "Онлайн", "c": "4839016950", "b": "Нет", "ex": "Да", "ad": "Нет", "src": "Сарафан", "d": "2026-04-18", "m": 5, "nt": "", "pt": "Дамил", "p": 500000}, {"n": "Жасик Астана", "t": 1000000, "g": 36, "dn": 14, "r": 1250000, "rn": 0, "f": 0, "fm": "Онлайн", "c": "50000200270", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Знакомый", "d": "2026-05-16", "m": 4, "nt": "", "pt": "", "p": 0}, {"n": "Даниил Raskrutov", "t": 500000, "g": 9, "dn": 0, "r": 400000, "rn": 0, "f": 30000, "fm": "Онлайн", "c": "", "b": "Нет", "ex": "Да", "ad": "Нет", "src": "Выступление", "d": "2026-06-01", "m": 3, "nt": "", "pt": "", "p": 100000}, {"n": "Мади Актобе", "t": 1000000, "g": 36, "dn": 4, "r": 0, "rn": 0, "f": 0, "fm": "Онлайн", "c": "3547884460", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Реферал", "d": "2026-06-06", "m": 3, "nt": "", "pt": "", "p": 1250000}, {"n": "Бакытжан", "t": 500000, "g": 9, "dn": 3, "r": 250000, "rn": 0, "f": 10000, "fm": "Онлайн", "c": "3344356440", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Реферал", "d": "2026-08-29", "m": 1, "nt": "", "pt": "", "p": 250000}, {"n": "Алишер", "t": 500000, "g": 9, "dn": 3, "r": 250000, "rn": 0, "f": 10000, "fm": "Офлайн", "c": "7808507100", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Реферал", "d": "2026-08-30", "m": 1, "nt": "", "pt": "", "p": 250000}, {"n": "Азат", "t": 1000000, "g": 36, "dn": 2, "r": 0, "rn": 0, "f": 10000, "fm": "Офлайн", "c": "4452166870", "b": "Нет", "ex": "Нет", "ad": "Нет", "src": "Знакомый", "d": "2026-09-02", "m": 0, "nt": "", "pt": "", "p": 1500000}];

function onEditDebet(e){
  if(bsIsCopy()) return;
  try{
    if(!e || !e.range) return;
    var ws = e.range.getSheet();
    if(ws.getName() !== "BS - резиденты дебет") return;
    var row = e.range.getRow(), col = e.range.getColumn();
    if(row < 3) return;

    // Защита колонки Оплачено вход: туда иногда пишут сумму встреч
    if(col === RC.paid){
      var nv = Number(e.range.getValue()) || 0;
      var ov = Number(e.oldValue) || 0;
      if(nv > 0 && nv < 1000 && ov >= 1000){
        e.range.setValue(ov);
        SpreadsheetApp.getActiveSpreadsheet().toast(
          "Оплачено вход не может быть " + nv + ". Вернул " + ov, "Защита", 6);
      }
      return;
    }

    // Бывший: перенос в архив и обратно
    if(col === RC.former){
      var val = String(e.range.getValue()||"").trim();
      var who = String(ws.getRange(row, RC.name).getValue()||"").trim();
      if(!who) return;
      if(val === "Да"){
        SpreadsheetApp.getActiveSpreadsheet().toast("Переношу в Бывшие резиденты", who, 8);
        try{ _toFormer(row, who); }catch(fe){ Logger.log("toFormer: "+fe); }
        try{ bsSafeRecalcDebet(); }catch(e2){}
        try{ _invalidateBSCaches(); }catch(e2){}
      }
      return;
    }

    if(col !== RC.done) return;

    var done = Number(e.range.getValue()) || 0;
    var granted = Number(ws.getRange(row, RC.granted).getValue()) || 0;
    var tariff = Number(ws.getRange(row, RC.tariff).getValue()) || 0;
    var name = String(ws.getRange(row, RC.name).getValue()||"").trim();
    if(!name) return;

    if(done > granted + 5){
      ws.getRange(row, RC.done).setValue(granted);
      SpreadsheetApp.getActiveSpreadsheet().toast(
        "Проведено не может быть больше оплаченного", name, 6);
      return;
    }

    if(granted > 0 && done >= granted && tariff > 0){
      var curDebt = Number(ws.getRange(row, RC.renew).getValue()) || 0;
      var note = ws.getRange(row, RC.renew).getNote() || "";
      var mark = "продление при " + granted;
      if(note.indexOf(mark) < 0){
        ws.getRange(row, RC.renew).setValue(curDebt + tariff).setNote(mark);
        SpreadsheetApp.getActiveSpreadsheet().toast(
          "Пакет закончился. В долг продления добавлено " + _fmtMoney(tariff), name, 8);
        try{
          // уведомление убрано
        }catch(err){}
      }
    } else if(granted > 0 && done < granted){
      if((ws.getRange(row, RC.renew).getNote()||"").indexOf("продление при") >= 0)
        ws.getRange(row, RC.renew).setNote("");
    }

    try{
      var wv = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - посещения");
      if(wv && wv.getLastRow() >= 3){
        var vd = wv.getRange(3, 2, wv.getLastRow()-2, 1).getValues();
        for(var vi = 0; vi < vd.length; vi++){
          if(String(vd[vi][0]||"").trim() === name){
            var vr = 3 + vi;
            wv.getRange(vr, 8).setValue(done);
            for(var ck = 1; ck <= 4; ck++) wv.getRange(vr, 3+ck).setValue(done >= ck);
            break;
          }
        }
      }
    }catch(err){ Logger.log("sync visits: "+err); }
  }catch(err){ Logger.log("onEditDebet: " + err); }
}

function renewResident(){
  // Продление: встаньте на строку резидента и нажмите
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = SpreadsheetApp.getActiveSheet();
    if(ws.getName() !== "BS - резиденты дебет")
      return _alert("Откройте лист BS - резиденты дебет и встаньте на строку резидента");
    var row = SpreadsheetApp.getActiveRange().getRow();
    if(row < 3) return _alert("Встаньте на строку резидента");

    var name = String(ws.getRange(row, 2).getValue()||"").trim();
    if(!name) return _alert("В этой строке нет резидента");
    var tariff = Number(ws.getRange(row, 3).getValue()) || 0;
    var granted = Number(ws.getRange(row, 4).getValue()) || 0;
    var done = Number(ws.getRange(row, 5).getValue()) || 0;

    var ui = SpreadsheetApp.getUi();
    var add = 3;
    if(tariff >= 1000000) add = 36;
    else if(tariff >= 500000) add = 9;
    else if(tariff >= 200000) add = 3;

    var resp = ui.alert(name + "\n\nСейчас: " + done + " из " + granted +
      "\nТариф: " + _fmtMoney(tariff) +
      "\n\nДобавить " + add + " встреч и обнулить долг продления?",
      ui.ButtonSet.YES_NO);
    if(resp !== ui.Button.YES) return "Отменено";

    ws.getRange(row, 4).setValue(granted + add);
    ws.getRange(row, 9).setValue(0).setNote("");
    SpreadsheetApp.flush();
    return _alert(name + ": добавлено " + add + " встреч.\n" +
      "Теперь " + done + " из " + (granted + add) + "\nДолг продления обнулён");
  }catch(e){ return _alert("Ошибка: " + e); }
}


function bsFixDebet(){
  if(bsIsCopy()) return;
  // Восстанавливает ВСЕ колонки из копии и ставит формулы. Данные не теряются
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("BS - резиденты дебет");
    if(!ws) return _alert("Нет листа резидентов");
    var lr = ws.getLastRow();
    if(lr < 3) return _alert("Список пуст");
    var rows = lr - 2;
    var CL = function(n){ return String.fromCharCode(64+n); };

    var BK = {};
    RESIDENTS_BACKUP.forEach(function(r){ BK[r.n] = r; });

    var data = ws.getRange(3, 1, rows, 21).getValues();
    var fixed = [], num = 0;

    for(var i = 0; i < data.length; i++){
      var nm = String(data[i][RA.name]||"").trim();
      if(!nm) continue;
      var row = 3 + i;
      var b = BK[nm];
      var isAdm = String(data[i][RA.admin]||"").trim() === "Да";

      // Номер: админы без номера
      ws.getRange(row, RC.num).setValue(isAdm ? "" : (++num));

      // Оплачено вход: если там встречи или мусор, берём из копии
      var curPaid = Number(data[i][RA.paid]) || 0;
      var g0 = Number(data[i][RA.granted])||0, d0 = Number(data[i][RA.done])||0;
      var meets0 = g0 + d0;
      var junk = curPaid > 0 && (
        (meets0 > 0 && curPaid === meets0) ||
        (curPaid < 1000) ||
        (meets0 > 0 && curPaid > 10000 && (curPaid - meets0) >= 10000 && (curPaid - meets0) % 10000 === 0));
      if(b && curPaid !== b.p && (junk || (b.p > 1000 && curPaid < 100000))){
        ws.getRange(row, RC.paid).setValue(b.p);
        fixed.push(nm + ": оплата " + b.p);
      }

      // Формулы
      ws.getRange(row, RC.left).setFormula("=" + CL(RC.granted) + row + "-" + CL(RC.done) + row);
      ws.getRange(row, RC.total).setFormula(
        "=" + CL(RC.rest) + row + "+" + CL(RC.renew) + row + "+" + CL(RC.fine) + row);
    }

    try{ bsSafeRecalcDebet(); }catch(e){ fixed.push("Пересчёт: " + e); }

    // Списки
    var dvSrc = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Сарафан","Instagram","Threads","Выступление","Реферал",
        "Партнёр","Сайт","Таргет","Знакомый","Другое"], true).setAllowInvalid(true).build();
    ws.getRange(3, RC.source, rows, 1).setDataValidation(dvSrc);
    var names = data.map(function(r){ return String(r[RA.name]||""); }).filter(Boolean);
    if(names.length){
      ws.getRange(3, RC.partner, rows, 1).setDataValidation(
        SpreadsheetApp.newDataValidation().requireValueInList(names, true)
          .setAllowInvalid(true).build());
    }
    var dvB = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Нет","Да"], true).setAllowInvalid(false).build();
    ws.getRange(3, RC.former, rows, 3).setDataValidation(dvB);
    ws.getRange(3, RC.format, rows, 1).setDataValidation(
      SpreadsheetApp.newDataValidation().requireValueInList(["Офлайн","Онлайн"], true)
        .setAllowInvalid(false).build());

    // Подсветка
    var rules = [];
    var rngD = ws.getRange(CL(RC.total)+"3:"+CL(RC.total)+lr);
    rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberGreaterThan(0)
      .setBackground("#FFEBEE").setFontColor("#C62828").setRanges([rngD]).build());
    rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberLessThanOrEqualTo(0)
      .setBackground("#E8F5E9").setFontColor("#1B7F3B").setRanges([rngD]).build());
    [RC.former, RC.except, RC.admin].forEach(function(cn){
      rules.push(SpreadsheetApp.newConditionalFormatRule().whenTextEqualTo("Да")
        .setBackground("#0a53a8").setFontColor("#FFFFFF").setBold(true)
        .setRanges([ws.getRange(3, cn, rows, 1)]).build());
    });
    rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberLessThanOrEqualTo(0)
      .setBackground("#FFF3E0").setFontColor("#E65100")
      .setRanges([ws.getRange(CL(RC.left)+"3:"+CL(RC.left)+lr)]).build());
    ws.setConditionalFormatRules(rules);

    // Форматы
    ws.getRange(3, RC.tariff, rows, 1).setNumberFormat("#,##0");
    ws.getRange(3, RC.paid, rows, 5).setNumberFormat("#,##0");
    ws.getRange(3, RC.granted, rows, 3).setNumberFormat("0").setHorizontalAlignment("center");
    ws.getRange(3, RC.date, rows, 1).setNumberFormat("dd.MM.yyyy");

    try{ _invalidateBSCaches(); }catch(e){}
    SpreadsheetApp.flush();

    var after = ws.getRange(3, 1, rows, 21).getValues();
    var debt = 0, act = 0, exc = 0;
    after.forEach(function(r){
      if(!String(r[RA.name]||"").trim()) return;
      if(String(r[RA.former]).trim() === "Да") return;
      if(String(r[RA.admin]).trim() === "Да") return;
      act++;
      if(String(r[RA.except]).trim() === "Да") exc++;
      debt += Number(r[RA.total]) || 0;
    });

    return _alert("Дебет приведён в порядок\n\n" +
      "Резидентов: " + act + " (из них исключений: " + exc + ")\n" +
      "Общий долг: " + _fmtMoney(debt) +
      (fixed.length ? "\n\nВосстановлено:\n" + fixed.join("\n") : ""));
  }catch(e){
    Logger.log("bsFixDebet: " + e);
    return _alert("Ошибка: " + e);
  }
}

function bsCleanSchedule(){
  if(bsIsCopy()) return;
  // Удаляет прошедшие встречи и мусорные строки из расписания
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("Расписание");
    if(!ws) return _alert("Нет листа Расписание");
    var lr = ws.getLastRow();
    if(lr < 3) return _alert("Расписание пусто");

    var today = new Date(); today.setHours(0,0,0,0);
    var data = ws.getRange(3, 1, lr-2, 8).getValues();

    // Архивируем прошедшие в Лог встреч
    var wsLog = ss.getSheetByName("Лог встреч");
    if(!wsLog){
      wsLog = ss.insertSheet("Лог встреч");
      wsLog.getRange(1,1,1,5).setValues([["Дата","Резидент","Время","Место","Формат"]])
        .setFontWeight("bold").setBackground("#0D0D0D").setFontColor("#FFFFFF");
      wsLog.setFrozenRows(1);
    }

    var keep = [], archived = 0, junk = 0;
    for(var i = 0; i < data.length; i++){
      var nm = String(data[i][1]||"").trim();
      var d = data[i][2];
      var tm = data[i][3];
      if(!nm || !d){ junk++; continue; }
      if(!tm && !data[i][4] && !data[i][5]){ junk++; continue; }
      if(d instanceof Date){
        var dd = new Date(d); dd.setHours(0,0,0,0);
        if(dd < today){
          // В архив
          var tStr = (tm instanceof Date)
            ? Utilities.formatDate(tm,"Asia/Almaty","HH:mm") : String(tm||"");
          wsLog.appendRow([d, nm, tStr, String(data[i][4]||data[i][5]||""), ""]);
          archived++;
          continue;
        }
      }
      keep.push([keep.length+1, nm, d, tm, data[i][4], data[i][5], data[i][6], data[i][7]]);
    }

    ws.getRange(3, 1, lr-2, 8).clearContent();
    if(keep.length){
      ws.getRange(3, 1, keep.length, 8).setValues(keep);
      ws.getRange(3, 3, keep.length, 1).setNumberFormat("DD.MM.YYYY");
      ws.getRange(3, 4, keep.length, 1).setNumberFormat("HH:MM");
    }

    try{ _invalidateBSCaches(); }catch(e){}
    SpreadsheetApp.flush();

    return _alert("Расписание очищено\n\n" +
      "Осталось будущих встреч: " + keep.length + "\n" +
      "Убрано прошедших: " + archived + " (в лист Лог встреч)\n" +
      "Удалено мусорных строк: " + junk);
  }catch(e){
    Logger.log("bsCleanSchedule: " + e);
    return _alert("Ошибка: " + e);
  }
}

function bsSystemCheck(){
  // Полная проверка. Что работает, что нет
  var out = [];
  var ss = SpreadsheetApp.openById(SS_ID);

  function add(t){ out.push(t); }
  function ok(t){ add("OK   " + t); }
  function bad(t){ add("НЕТ  " + t); }

  // 1. Листы
  add("=== ЛИСТЫ ===");
  var need = ["BS - резиденты дебет","Учет ДДС","PL","Штрафы","Расписание",
              "Лог отчётов","Настройки"];
  need.forEach(function(n){
    var w = ss.getSheetByName(n);
    if(w) ok(n + " (" + w.getLastRow() + " строк)");
    else bad(n + " отсутствует");
  });

  // 2. Структура резидентов
  add("");
  add("=== РЕЗИДЕНТЫ ===");
  var wsR = ss.getSheetByName("BS - резиденты дебет");
  if(wsR && wsR.getLastRow() >= 3){
    var lc = wsR.getLastColumn();
    var head = wsR.getRange(2,1,1,lc).getValues()[0]
      .map(function(h){ return String(h||"").replace(/\n/g," ").trim(); });
    var EXPECT = [
      [RC.name, "Имя"], [RC.tariff, "Тариф"], [RC.granted, "Встреч оплачено"],
      [RC.done, "Встреч проведено"], [RC.total, "ОБЩИЙ"], [RC.format, "Формат"],
      [RC.chat, "Chat ID"], [RC.former, "Бывший"], [RC.date, "Дата"]
    ];
    var mismatch = [];
    EXPECT.forEach(function(e){
      var h = String(head[e[0]-1]||"");
      if(h.toLowerCase().indexOf(e[1].toLowerCase()) < 0)
        mismatch.push("колонка " + e[0] + ": ожидалось " + e[1] + ", стоит " + (h||"пусто"));
    });
    if(mismatch.length){
      bad("Порядок колонок сбит:");
      mismatch.forEach(function(x){ add("     " + x); });
      add("     Запустите: Восстановить дебет");
    } else ok("Порядок колонок верный");

    var d = wsR.getRange(3, 1, wsR.getLastRow()-2, Math.min(lc, 21)).getValues();
    var active = 0, debt = 0, noChat = [], badMeet = [], off = 0, onl = 0;
    d.forEach(function(r){
      var nm = String(r[RA.name]||"").trim();
      if(!nm) return;
      if(String(r[RA.former]).trim() === "Да") return;
      active++;
      debt += Number(r[RA.total]) || 0;
      if(!String(r[RA.chat]||"").trim()) noChat.push(nm);
      var g = Number(r[RA.granted])||0, dn = Number(r[RA.done])||0;
      if(g > 60 || dn > 60 || dn > g + 1) badMeet.push(nm + " " + dn + "/" + g);
      if(String(r[RA.format]) === "Онлайн") onl++; else off++;
    });
    ok("Активных: " + active + " (офлайн " + off + ", онлайн " + onl + ")");
    if(debt > 0) ok("Общий долг: " + _fmtMoney(debt));
    else bad("Общий долг 0. Проверьте формулы в колонке " + RC.total);
    if(noChat.length) bad("Без Chat ID: " + noChat.join(", "));
    else ok("Chat ID у всех");
    var exc = [], adm = [];
    d.forEach(function(r){
      var nm = String(r[RA.name]||"").trim();
      if(!nm) return;
      if(String(r[RA.except]).trim() === "Да") exc.push(nm);
      if(String(r[RA.admin]).trim() === "Да") adm.push(nm);
    });
    if(exc.length) ok("Исключения (не штрафуются): " + exc.join(", "));
    else bad("Исключений не отмечено. Если кто-то не должен штрафоваться, поставьте Да в колонке " + RC.except);
    ok("Админы: " + (adm.length ? adm.join(", ") : "не отмечены"));
    if(badMeet.length) bad("Странные встречи: " + badMeet.join(", "));
    else ok("Встречи в норме");
  } else bad("Лист резидентов пуст");

  // 3. Расписание
  add("");
  add("=== РАСПИСАНИЕ ===");
  var wsS = ss.getSheetByName("Расписание");
  if(wsS && wsS.getLastRow() >= 3){
    var sd = wsS.getRange(3, 1, wsS.getLastRow()-2, 6).getValues();
    var good = 0, junk = 0, future = 0;
    var today = new Date(); today.setHours(0,0,0,0);
    sd.forEach(function(r){
      var nm = String(r[1]||"").trim(), dt = r[2], tm = r[3];
      if(!nm || !dt){ junk++; return; }
      if(!tm && !r[4] && !r[5]){ junk++; return; }
      good++;
      if(dt instanceof Date && dt >= today) future++;
    });
    ok("Встреч: " + good + " (будущих " + future + ")");
    if(junk) bad("Мусорных строк: " + junk);
  } else bad("Расписание пусто");

  // 4. Финансы
  add("");
  add("=== ФИНАНСЫ ===");
  var pl = ss.getSheetByName("PL");
  if(pl){
    var lrp = pl.getLastRow(), rowKassa = -1, rowProfit = -1, errs = 0;
    for(var r2 = 1; r2 <= lrp; r2++){
      var n2 = String(pl.getRange(r2,1).getValue()||"").trim();
      if(n2 === "На кассе") rowKassa = r2;
      if(n2 === "ЧИСТАЯ ПРИБЫЛЬ") rowProfit = r2;
    }
    if(rowKassa > 0){
      var last = null;
      for(var c = 2; c <= 13; c++){
        var v = pl.getRange(rowKassa, c).getValue();
        if(typeof v === "string" && v.indexOf("#") === 0) errs++;
        else if(typeof v === "number") last = v;
      }
      if(errs) bad("Ошибок в строке На кассе: " + errs + ". Запустите: Пересчитать PL");
      else if(last !== null) ok("На кассе: " + _fmtMoney(last));
      else bad("Касса пустая");
    } else bad("Строки На кассе нет");
    if(lrp > rowKassa && rowKassa > 0) bad("Ниже На кассе есть лишние строки");
  }

  // 5. Бот
  add("");
  add("=== БОТ ===");
  try{
    var wh = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",
      {muteHttpExceptions:true});
    var info = JSON.parse(wh.getContentText());
    if(info.ok && info.result){
      if(info.result.url) ok("Вебхук настроен");
      else bad("Вебхук не настроен");
      if(info.result.last_error_message)
        bad("Последняя ошибка: " + info.result.last_error_message);
      if(info.result.pending_update_count > 5)
        bad("Необработанных сообщений: " + info.result.pending_update_count);
    }
  }catch(e){ bad("Не удалось проверить вебхук: " + e); }

  var at = bsAuditTriggers();
  ok("Триггеров: " + at.total);
  if(at.bad.length) bad("Чужие триггеры: " + at.bad.join(", ") + ". Нажмите Переустановить триггеры");
  // Версия, на которой работает бот
  try{
    var whI = JSON.parse(UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",
      {muteHttpExceptions:true}).getContentText());
    var hookUrl = whI.result && whI.result.url;
    if(hookUrl){
      var vr = UrlFetchApp.fetch(hookUrl.split("?")[0] + "?action=bsVersion", {muteHttpExceptions:true, followRedirects:true});
      var live = "";
      try{ live = JSON.parse(vr.getContentText()).version || ""; }catch(pe){}
      if(live === BS_VERSION) ok("Бот работает на актуальной версии " + BS_VERSION);
      else bad("Telegram шлёт сообщения на другое развёртывание (версия: " + (live || "без номера") + "). " +
        "Нажмите Переподключить бота");
    }
  }catch(vE){ bad("Не удалось проверить версию бота: " + vE); }
  try{
    var gw = CacheService.getScriptCache().get("guardWarn");
    if(gw) bad("За последний час сторож чинил дебет: в таблицу пишет чужой скрипт");
  }catch(e){}
  if(bsIsCopy()) return _alert("Это резервная копия таблицы. Автоматика в ней отключена");
  var oldFns = bsOldCodePresent();
  if(oldFns.length) bad("Старая копия скрипта перебивает функции: " + oldFns.join(", ") +
    ". Откройте Apps Script, оставьте один файл со скриптом");
  else ok("Работает только актуальный код");

  // 6. Кеш
  add("");
  add("=== КЕШ ===");
  try{
    _invalidateBSCaches();
    ok("Кеш сброшен");
  }catch(e){ bad("Кеш: " + e); }

  var msg = out.join("\n");
  Logger.log(msg);
  try{ SpreadsheetApp.getUi().alert(msg.length > 3000 ? msg.slice(0,3000)+"\n\n...см. логи" : msg); }catch(e){}
  return msg;
}

function rebuildDebet(){
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var NM = "BS - резиденты дебет";

    var byName = {};
    RESIDENTS_BACKUP.forEach(function(r){ byName[r.n] = r; });

    var cur = ss.getSheetByName(NM);
    if(cur && cur.getLastRow() >= 3){
      try{
        var lc = cur.getLastColumn();
        var hd = cur.getRange(2,1,1,lc).getValues()[0]
          .map(function(h){ return String(h||"").replace(/\n/g," ").toLowerCase().trim(); });
        function cc(keys){
          for(var k=0;k<hd.length;k++) for(var n=0;n<keys.length;n++)
            if(hd[k].indexOf(keys[n])===0) return k;
          return -1;
        }
        var ci = {n:cc(["имя"]), chat:cc(["chat"]), g:cc(["встреч оплачено","циклов оплачено"]),
                  d:cc(["встреч проведено","циклов исп"]), f:cc(["штраф"]),
                  rn:cc(["долг продление","долг за продление"]), fm:cc(["формат"]),
                  ex:cc(["исключение"]), ad:cc(["админ"]), fr:cc(["бывший"])};
        cur.getRange(3,1,cur.getLastRow()-2,lc).getValues().forEach(function(row){
          var nm = ci.n>=0 ? String(row[ci.n]||"").trim() : "";
          if(!nm || !byName[nm]) return;
          var o = byName[nm];
          if(ci.chat>=0 && row[ci.chat]) o.c = String(row[ci.chat]).trim();
          if(ci.g>=0){ var gv = Number(row[ci.g]); if(gv>0 && gv<=60) o.g = gv; }
          if(ci.d>=0){ var dv = Number(row[ci.d]); if(dv>=0 && dv<=60) o.dn = dv; }
          if(ci.f>=0 && Number(row[ci.f])>=0) o.f = Number(row[ci.f]);
          if(ci.rn>=0 && Number(row[ci.rn])>=0) o.rn = Number(row[ci.rn]);
          if(ci.fm>=0 && row[ci.fm]) o.fm = String(row[ci.fm]).trim();
          if(ci.ex>=0) o.ex = String(row[ci.ex]||"Нет").trim();
          if(ci.ad>=0) o.ad = String(row[ci.ad]||"Нет").trim();
          if(ci.fr>=0) o.b = String(row[ci.fr]||"Нет").trim();
        });
      }catch(e){ Logger.log("merge: "+e); }
    }

    var list = Object.keys(byName).map(function(k){ return byName[k]; })
      .filter(function(r){ return r.b !== "Да"; });
    // Админы первыми, остальные по дате входа
    list.sort(function(a, b){
      var aa = (a.ad === "Да" || a.n === "Рустам" || a.n === "Береке") ? 0 : 1;
      var bb = (b.ad === "Да" || b.n === "Рустам" || b.n === "Береке") ? 0 : 1;
      if(aa !== bb) return aa - bb;
      return String(a.d||"").localeCompare(String(b.d||""));
    });

    if(cur) ss.deleteSheet(cur);
    var ws = ss.insertSheet(NM, 0);
    var BLK="#0D0D0D", WHT="#FFFFFF", GRN="#1B7F3B", RED="#C62828";
    var GRN_BG="#E8F5E9", RED_BG="#FFEBEE";

    // Порядок по карте RC. Дата и месяцы в конце
    var HEADS = ["№","Имя резидента","Тариф тг",
      "Встреч\nоплачено","Встреч\nпроведено","Осталось\nвстреч",
      "Оплачено\n(вход) тг","Остаток\n(вход) тг","Долг\nпродление тг","Штрафы тг","ОБЩИЙ\nДОЛГ тг",
      "Формат","Chat ID","Бывший","Исключение","Админ",
      "Источник","Дата входа","Месяцев\nв проекте","Примечание","Партнёр"];
    var WIDS = [40,190,105,95,95,95,115,115,115,100,115,90,120,80,95,80,140,105,95,150,130];
    var NC = HEADS.length;

    ws.setFrozenRows(2);
    ws.setFrozenColumns(2);
    ws.setRowHeight(1, 32);
    ws.setRowHeight(2, 46);
    ws.getRange(1,1,1,NC).setBackground(BLK);
    ws.getRange(1,2).setValue("BS. ДЕБЕТ").setFontColor(WHT)
      .setFontFamily("Montserrat").setFontSize(11).setFontWeight("bold")
      .setVerticalAlignment("middle");
    ws.getRange(2,1,1,NC).setValues([HEADS])
      .setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
      .setFontSize(10).setFontWeight("bold").setWrap(true)
      .setHorizontalAlignment("center").setVerticalAlignment("middle")
      .setBorder(true,true,true,true,true,true,"#333333",SpreadsheetApp.BorderStyle.SOLID);
    ws.getRange(2, RC.format).setBackground("#1A3A5C").setFontColor("#AADDFF");
    ws.getRange(2, RC.chat).setBackground("#1A3A1A").setFontColor("#66FF66");
    ws.getRange(2, RC.admin).setBackground("#3A1A3A").setFontColor("#FFAAFF");
    for(var w=0; w<NC; w++) ws.setColumnWidth(w+1, WIDS[w]);

    // Админы идут первыми без номера, резиденты нумеруются с единицы
    var num = 0;
    var rows = list.map(function(r){
      var dt = r.d ? new Date(r.d) : "";
      var isAdmin = r.ad || ((r.n === "Рустам" || r.n === "Береке") ? "Да" : "Нет");
      var isExc = r.ex || isAdmin;
      var no = (isAdmin === "Да") ? "" : (++num);
      return [no, r.n, r.t,
              r.g, r.dn, 0,
              r.p, r.r, r.rn, r.f, 0,
              r.fm || "Офлайн", r.c || "", r.b || "Нет", isExc, isAdmin,
              r.src || "", dt, r.m, r.nt || "", r.pt || ""];
    });
    if(!rows.length) return _alert("Нет данных");

    ws.getRange(3,1,rows.length,NC).setValues(rows)
      .setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle")
      .setBorder(true,true,true,true,true,true,"#DDDDDD",SpreadsheetApp.BorderStyle.SOLID);

    var CL = function(n){ return String.fromCharCode(64+n); };
    // Осталось встреч и общий долг. формулами по всему столбцу разом
    var lastRow = 2 + rows.length;
    var fLeft = [], fTotal = [];
    for(var fr = 3; fr <= lastRow; fr++){
      fLeft.push(["=" + CL(RC.granted) + fr + "-" + CL(RC.done) + fr]);
      fTotal.push(["=" + CL(RC.rest) + fr + "+" + CL(RC.renew) + fr + "+" + CL(RC.fine) + fr]);
    }
    ws.getRange(3, RC.left, rows.length, 1).setFormulas(fLeft);
    ws.getRange(3, RC.total, rows.length, 1).setFormulas(fTotal);

    ws.getRange(3, 1, rows.length, 21).setHorizontalAlignment("center");
    ws.getRange(3,RC.tariff,rows.length,1).setNumberFormat("#,##0");
    ws.getRange(3,RC.granted,rows.length,3).setHorizontalAlignment("center").setNumberFormat("0");
    ws.getRange(3,RC.paid,rows.length,5).setNumberFormat("#,##0");
    ws.getRange(3,RC.total,rows.length,1).setFontWeight("bold");
    ws.getRange(3,RC.format,rows.length,1).setHorizontalAlignment("center");
    ws.getRange(3,RC.chat,rows.length,1).setBackground(GRN_BG).setFontColor(GRN).setFontWeight("bold");
    ws.getRange(3,RC.former,rows.length,3).setHorizontalAlignment("center");
    ws.getRange(3,RC.date,rows.length,1).setNumberFormat("dd.MM.yyyy").setHorizontalAlignment("center");
    ws.getRange(3,RC.months,rows.length,1).setHorizontalAlignment("center");

    var dvT = SpreadsheetApp.newDataValidation()
      .requireValueInList(["0","100000","150000","200000","250000","300000","500000","1000000"],true)
      .setAllowInvalid(true).build();
    var dvB = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Нет","Да"],true).setAllowInvalid(false).build();
    var dvF = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Офлайн","Онлайн"],true).setAllowInvalid(false).build();
    ws.getRange(3,RC.tariff,rows.length,1).setDataValidation(dvT);
    ws.getRange(3,RC.format,rows.length,1).setDataValidation(dvF);
    ws.getRange(3,RC.former,rows.length,3).setDataValidation(dvB);

    // Источник: шаблоны на выбор
    var dvSrc = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Сарафан","Instagram","Threads","Выступление","Реферал",
        "Партнёр","Сайт","Таргет","Знакомый","Другое"], true)
      .setAllowInvalid(true).build();
    ws.getRange(3, RC.source, rows.length, 1).setDataValidation(dvSrc);

    // Партнёр: список резидентов
    var names = rows.map(function(x){ return String(x[1]); }).filter(Boolean);
    if(names.length){
      var dvPt = SpreadsheetApp.newDataValidation()
        .requireValueInList(names, true).setAllowInvalid(true).build();
      ws.getRange(3, RC.partner, rows.length, 1).setDataValidation(dvPt);
    }

    // Подсветка "Да" синим в колонках Бывший, Исключение, Админ
    var CLb = String.fromCharCode(64+RC.former);
    var CLe = String.fromCharCode(64+RC.except);
    var CLa = String.fromCharCode(64+RC.admin);

    var rules = [];
    var rngD = ws.getRange(CL(RC.total)+"3:"+CL(RC.total)+(2+rows.length));
    rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberGreaterThan(0)
      .setBackground(RED_BG).setFontColor(RED).setRanges([rngD]).build());
    rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberLessThanOrEqualTo(0)
      .setBackground(GRN_BG).setFontColor(GRN).setRanges([rngD]).build());
    var rngL = ws.getRange(CL(RC.left)+"3:"+CL(RC.left)+(2+rows.length));
    rules.push(SpreadsheetApp.newConditionalFormatRule().whenNumberLessThanOrEqualTo(0)
      .setBackground("#FFF3E0").setFontColor("#E65100").setRanges([rngL]).build());
    // Синяя заливка для "Да" в трёх колонках
    [RC.former, RC.except, RC.admin].forEach(function(cn){
      rules.push(SpreadsheetApp.newConditionalFormatRule()
        .whenTextEqualTo("Да")
        .setBackground("#0a53a8").setFontColor("#FFFFFF").setBold(true)
        .setRanges([ws.getRange(3, cn, rows.length, 1)]).build());
    });
    // Бывшие серым по всей строке
    rules.push(SpreadsheetApp.newConditionalFormatRule()
      .whenFormulaSatisfied('=$'+CL(RC.former)+'3="Да"')
      .setBackground("#DDDDDD").setFontColor("#999999").setStrikethrough(true)
      .setRanges([ws.getRange(3,1,rows.length,NC)]).build());
    ws.setConditionalFormatRules(rules);

    try{ syncVisitsFromDebet(ss, list); }catch(e){ Logger.log("visits: "+e); }

    // Сбрасываем кеш приложения, иначе оно покажет старые данные
    try{ _invalidateBundleCache(); }catch(e){ Logger.log("cache: "+e); }
    try{ refreshBotCache(); }catch(e){ Logger.log("botcache: "+e); }

    SpreadsheetApp.flush();
    return _alert("Дебет восстановлен\n\nРезидентов: " + rows.length +
      "\n\nПорядок: тариф, встречи, деньги, служебное,\nдата входа и месяцы в конце.\n\n" +
      "Все функции скрипта переведены на карту колонок RC.\n" +
      "Посещения синхронизированы.");
  }catch(e){
    Logger.log("rebuildDebet: " + e);
    return _alert("Ошибка: " + e);
  }
}

function syncVisitsFromDebet(ss, list){
  // Лист посещений собирается из дебета
  var NM = "BS - посещения";
  var old = ss.getSheetByName(NM);
  if(old) ss.deleteSheet(old);
  var ws = ss.insertSheet(NM, 2);
  var BLK = "#0D0D0D", WHT = "#FFFFFF";

  ws.setFrozenRows(2);
  ws.setRowHeight(2, 42);
  ws.getRange(1,1,1,9).setBackground(BLK);
  ws.getRange(1,2).setValue("BS. ПОСЕЩЕНИЯ").setFontColor(WHT)
    .setFontFamily("Montserrat").setFontSize(11).setFontWeight("bold");
  var H = ["№","Имя резидента","Встреч\nоплачено","Пос. 1","Пос. 2","Пос. 3","Пос. 4",
           "Проведено","Осталось"];
  ws.getRange(2,1,1,9).setValues([H])
    .setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
    .setFontSize(10).setFontWeight("bold").setWrap(true)
    .setHorizontalAlignment("center").setVerticalAlignment("middle");
  [40,200,100,70,70,70,70,95,95].forEach(function(w,i){ ws.setColumnWidth(i+1,w); });

  var rows = list.map(function(r, i){
    var d = r.dn || 0;
    return [i+1, r.n, r.g || 0,
            d>=1, d>=2, d>=3, d>=4, d, (r.g||0) - d];
  });
  if(rows.length){
    ws.getRange(3, 1, rows.length, 21).setValues(rows)
      .setFontFamily("Montserrat").setFontSize(10)
      .setBorder(true,true,true,true,true,true,"#DDDDDD",SpreadsheetApp.BorderStyle.SOLID);
    ws.getRange(3,4,rows.length,4).insertCheckboxes();
    ws.getRange(3, 1, rows.length, 21).setHorizontalAlignment("center");
    ws.getRange(3,3,rows.length,1).setHorizontalAlignment("center");
    ws.getRange(3,8,rows.length,2).setHorizontalAlignment("center");
  }
}

function _fmtMoney(v){
  return String(Math.round(v)).replace(/\B(?=(\d{3})+(?!\d))/g, " ") + " тг";
}

function cleanDDS(){
  // Удаляет строки, где есть дата, но нет ни прихода, ни расхода
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("Учет ДДС");
    if(!ws) return _alert("Нет листа ДДС");
    var lr = ws.getLastRow();
    if(lr < 2) return _alert("ДДС пуст");

    var data = ws.getRange(2, 1, lr-1, 6).getValues();
    var bad = [];
    for(var i = 0; i < data.length; i++){
      var d = data[i][0];
      var inc = Number(data[i][1]) || 0;
      var exp = Number(data[i][2]) || 0;
      if(d && !inc && !exp) bad.push(2+i);
    }
    if(!bad.length) return _alert("Пустых строк нет.\n\nЗаписей: " + (lr-1));

    bad.sort(function(a,b){ return b-a; });
    bad.forEach(function(r){ try{ ws.deleteRow(r); }catch(e){} });
    SpreadsheetApp.flush();
    try{ recalcPL(); }catch(e){}
    return _alert("Удалено пустых строк: " + bad.length +
      "\nСтроки: " + bad.slice(0,10).reverse().join(", ") +
      "\n\nЗапустите Починить PL");
  }catch(e){ return _alert("Ошибка: " + e); }
}

function bsRepairPL(){
  if(bsIsCopy()) return;
  // Приводит PL к эталону: 31 статей расхода, затем ИТОГО, ПРИБЫЛЬ, Дивиденды, На кассе
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var pl = ss.getSheetByName("PL");
    var dds = ss.getSheetByName("Учет ДДС");
    if(!pl) return _alert("Нет листа PL");

    var EXP_ITEMS = ["SMM","Маркет.бюджет","Таргетолог","Комиссия+налог","Tilda + домен",
      "CRM","Canva","Съемки проф","Съемки доп","HH объявление","Симка тариф",
      "Оборудование","Офис","Ассистент","Бухгалтер","Юрист","IT","Прочие расходы:"];
    var log = [];

    // 1. Удаляем лишние строки в блоке расходов (всё, чего нет в эталоне)
    var lr = pl.getLastRow();
    var rExpH = -1;
    for(var r = 1; r <= lr; r++){
      if(String(pl.getRange(r,1).getValue()||"").trim() === "РАСХОДЫ"){ rExpH = r; break; }
    }
    if(rExpH < 0) return _alert("Не нашёл блок РАСХОДЫ");

    // Строки статей не удаляем: в них деньги из ДДС. Новые статьи PL создаёт сам
    var rExpT = -1, lastItem = rExpH;
    for(var r2 = rExpH + 1; r2 <= lr; r2++){
      var nm = String(pl.getRange(r2,1).getValue()||"").trim();
      if(nm === "ИТОГО РАСХОДЫ"){ rExpT = r2; break; }
      if(nm === "ЧИСТАЯ ПРИБЫЛЬ" || nm === "Дивиденды" || nm === "На кассе") break;
      if(nm) lastItem = r2;
    }
    if(rExpT < 0) rExpT = lastItem + 1;
    var names = ["ИТОГО РАСХОДЫ","ЧИСТАЯ ПРИБЫЛЬ","Дивиденды","На кассе"];
    for(var n = 0; n < names.length; n++){
      var rr = rExpT + n;
      var was = String(pl.getRange(rr,1).getValue()||"").trim();
      if(was !== names[n]){
        pl.getRange(rr,1).setValue(names[n]);
        log.push(rr + ": " + (was||"пусто") + " -> " + names[n]);
      }
    }
    var rProfit = rExpT+1, rDiv = rExpT+2, rKassa = rExpT+3;

    // 3. Всё ниже На кассе удаляем
    if(pl.getLastRow() > rKassa){
      var del = pl.getLastRow() - rKassa;
      pl.deleteRows(rKassa+1, del);
      log.push("Удалено строк ниже: " + del);
    }

    // 4. Дивиденды из ДДС
    var rIncT = -1;
    for(var r3 = 1; r3 < rExpH; r3++){
      if(String(pl.getRange(r3,1).getValue()||"").trim() === "ИТОГО ДОХОДЫ"){ rIncT = r3; break; }
    }
    if(dds && dds.getLastRow() > 1){
      var divM = {}, nowY = new Date().getFullYear();
      dds.getRange(2,1,dds.getLastRow()-1,6).getValues().forEach(function(x){
        var dt = x[0];
        if(!(dt instanceof Date) || dt.getFullYear() !== nowY) return;
        var ex = Number(x[2]) || 0;
        if(ex && String(x[5]||"").toLowerCase().indexOf("дивиденд") >= 0)
          divM[dt.getMonth()] = (divM[dt.getMonth()] || 0) + ex;
      });
      var dv = [];
      for(var m = 0; m < 12; m++) dv.push(divM[m] || 0);
      pl.getRange(rDiv, 2, 1, 12).setValues([dv]);
      log.push("Дивиденды из ДДС");
    }

    // 5. Формулы
    var COLS = "BCDEFGHIJKLM".split("");
    for(var ci = 0; ci < 12; ci++){
      var col = COLS[ci], c2 = ci + 2;
      pl.getRange(rExpT, c2).setFormula("=SUM("+col+(rExpH+1)+":"+col+(rExpT-1)+")");
      pl.getRange(rProfit, c2).setFormula("="+col+rIncT+"-"+col+rExpT);
      if(ci < CASH_START_MONTH) pl.getRange(rKassa, c2).clearContent();
      else if(ci === CASH_START_MONTH)
        pl.getRange(rKassa, c2).setFormula("="+col+rProfit+"-"+col+rDiv);
      else pl.getRange(rKassa, c2).setFormula(
        "="+COLS[ci-1]+rKassa+"+"+col+rProfit+"-"+col+rDiv);
    }
    log.push("Формулы восстановлены");

    // 6. Оформление
    pl.getRange(rIncT,1,1,14).setBackground("#1A5C1A").setFontColor("#FFFFFF").setFontWeight("bold");
    pl.getRange(rExpT,1,1,14).setBackground("#5C1A1A").setFontColor("#FFFFFF").setFontWeight("bold");
    pl.getRange(rProfit,1,1,14).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
    pl.getRange(rDiv,1,1,14).setBackground("#f3f3f3").setFontColor("#000000");
    pl.getRange(rKassa,1,1,14).setBackground("#B8860B").setFontColor("#FFFFFF").setFontWeight("bold");
    pl.getRange(rExpT,2,4,12).setNumberFormat("#,##0");

    SpreadsheetApp.flush();
    var cash = null;
    for(var c3 = 13; c3 >= 2; c3--){
      var cv = pl.getRange(rKassa, c3).getValue();
      if(typeof cv === "number" && cv !== 0){ cash = cv; break; }
    }

    return _alert("PL приведён к эталону\n\n" +
      rExpT + " ИТОГО РАСХОДЫ\n" + rProfit + " ЧИСТАЯ ПРИБЫЛЬ\n" +
      rDiv + " Дивиденды\n" + rKassa + " На кассе\n\n" +
      (cash !== null ? "НА КАССЕ: " + _fmtMoney(cash) + "\n\n" : "") +
      log.join("\n"));
  }catch(e){
    Logger.log("bsRepairPL: " + e);
    return _alert("Ошибка: " + e);
  }
}

function rebuildPLFromDDS(){
  if(bsIsCopy()) return;
  // Пересчёт PL из ДДС. Ничего лишнего не добавляет, всё ниже На кассе удаляет
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var dds = ss.getSheetByName("Учет ДДС");
    var pl = ss.getSheetByName("PL");
    if(!dds || !pl) return _alert("Нет листов");

    var nowY = new Date().getFullYear();
    var nowM = new Date().getMonth();
    var MONTHS_TO_FILL = nowM + 1;

    // Читаем ДДС
    var lr = dds.getLastRow();
    if(lr < 2) return _alert("ДДС пуст");
    var data = dds.getRange(2, 1, lr-1, 6).getValues();
    var incByType = {}, expByCat = {}, divByMonth = {}, skipped = 0;

    data.forEach(function(r){
      var d = r[0];
      if(!(d instanceof Date) || d.getFullYear() !== nowY) return;
      var mi = d.getMonth();
      if(mi > nowM) return;
      var inAmt = Number(r[1]) || 0, exAmt = Number(r[2]) || 0;
      if(inAmt){
        var t = String(r[3]||"").trim() || "Прочие доходы";
        incByType[t] = incByType[t] || {};
        incByType[t][mi] = (incByType[t][mi] || 0) + inAmt;
      }
      if(exAmt){
        var ce = String(r[5]||"").trim();
        if(!ce){ skipped++; return; }
        if(ce.toLowerCase().indexOf("дивиденд") >= 0){
          divByMonth[mi] = (divByMonth[mi] || 0) + exAmt;
        } else {
          ce = ce.replace(/\s+/g," ").replace(" + ","+");
          expByCat[ce] = expByCat[ce] || {};
          expByCat[ce][mi] = (expByCat[ce][mi] || 0) + exAmt;
        }
      }
    });

    // Находим строки PL
    var plLr = pl.getLastRow();
    var rowMap = {}, rIncH = -1, rExpH = -1, rIncT = -1, rExpT = -1,
        rProfit = -1, rDiv = -1, rKassa = -1;
    for(var r2 = 1; r2 <= plLr; r2++){
      var nm = String(pl.getRange(r2,1).getValue()||"").trim();
      if(!nm) continue;
      if(nm === "ДОХОДЫ") rIncH = r2;
      else if(nm === "РАСХОДЫ") rExpH = r2;
      else if(nm === "ИТОГО ДОХОДЫ") rIncT = r2;
      else if(nm === "ИТОГО РАСХОДЫ") rExpT = r2;
      else if(nm === "ЧИСТАЯ ПРИБЫЛЬ") rProfit = r2;
      else if(nm === "Дивиденды") rDiv = r2;
      else if(nm === "На кассе" && rKassa < 0) rKassa = r2;
      else if(!rowMap[nm]) rowMap[nm.replace(/\s+/g," ").replace(" + ","+")] = r2;
    }
    if(rIncT < 0 || rExpT < 0 || rKassa < 0)
      return _alert("В PL нет строк ИТОГО или На кассе");

    // ВСЁ НИЖЕ На кассе удаляем
    if(plLr > rKassa){
      pl.deleteRows(rKassa+1, plLr - rKassa);
      plLr = rKassa;
    }

    // Заполняем только существующие строки
    var missing = [];
    Object.keys(incByType).forEach(function(t){
      var rr = rowMap[t];
      if(!rr){ missing.push("доход: " + t); return; }
      var vals = [];
      for(var m = 0; m < MONTHS_TO_FILL; m++) vals.push(incByType[t][m] || 0);
      pl.getRange(rr, 2, 1, MONTHS_TO_FILL).setValues([vals]);
    });
    Object.keys(expByCat).forEach(function(c){
      var rr = rowMap[c];
      if(!rr){ missing.push("расход: " + c); return; }
      var vals = [];
      for(var m = 0; m < MONTHS_TO_FILL; m++) vals.push(expByCat[c][m] || 0);
      pl.getRange(rr, 2, 1, MONTHS_TO_FILL).setValues([vals]);
    });
    if(rDiv > 0){
      var dv = [];
      for(var m2 = 0; m2 < MONTHS_TO_FILL; m2++) dv.push(divByMonth[m2] || 0);
      pl.getRange(rDiv, 2, 1, MONTHS_TO_FILL).setValues([dv]);
    }

    // Чистим будущие месяцы
    if(MONTHS_TO_FILL < 12){
      for(var rr2 = rIncH+1; rr2 <= rKassa; rr2++){
        pl.getRange(rr2, 2+MONTHS_TO_FILL, 1, 12-MONTHS_TO_FILL).clearContent();
      }
    }

    // Формулы. ИТОГО РАСХОДЫ считает только статьи, без дивидендов
    var COLS = "BCDEFGHIJKLM".split("");
    for(var ci = 0; ci < MONTHS_TO_FILL; ci++){
      var col = COLS[ci], c2 = ci + 2;
      pl.getRange(rIncT, c2).setFormula("=SUM("+col+(rIncH+1)+":"+col+(rIncT-1)+")");
      pl.getRange(rExpT, c2).setFormula("=SUM("+col+(rExpH+1)+":"+col+(rExpT-1)+")");
      if(rProfit > 0) pl.getRange(rProfit, c2).setFormula("="+col+rIncT+"-"+col+rExpT);
      if(ci < CASH_START_MONTH) pl.getRange(rKassa, c2).clearContent();
      else if(ci === CASH_START_MONTH)
        pl.getRange(rKassa, c2).setFormula("="+col+rProfit+"-"+col+rDiv);
      else pl.getRange(rKassa, c2).setFormula(
        "="+COLS[ci-1]+rKassa+"+"+col+rProfit+"-"+col+rDiv);
    }

    SpreadsheetApp.flush();

    var totI = 0, totE = 0, totD = 0;
    Object.keys(incByType).forEach(function(t){
      for(var m3 = 0; m3 < MONTHS_TO_FILL; m3++) totI += (incByType[t][m3] || 0);
    });
    Object.keys(expByCat).forEach(function(c){
      if(!rowMap[c]) return;
      for(var m4 = 0; m4 < MONTHS_TO_FILL; m4++) totE += (expByCat[c][m4] || 0);
    });
    for(var m5 = CASH_START_MONTH; m5 < MONTHS_TO_FILL; m5++) totD += (divByMonth[m5] || 0);
    var cash = 0;
    for(var m6 = CASH_START_MONTH; m6 < MONTHS_TO_FILL; m6++){
      var im = 0, em = 0;
      Object.keys(incByType).forEach(function(t){ im += (incByType[t][m6] || 0); });
      Object.keys(expByCat).forEach(function(c){ if(rowMap[c]) em += (expByCat[c][m6] || 0); });
      cash += im - em - (divByMonth[m6] || 0);
    }

    var msg = "PL пересчитан\n\n" +
      "Доходы: " + _fmtMoney(totI) + "\n" +
      "Расходы: " + _fmtMoney(totE) + "\n" +
      "ПРИБЫЛЬ: " + _fmtMoney(totI - totE) + "\n" +
      "Дивиденды: " + _fmtMoney(totD) + "\n" +
      "НА КАССЕ: " + _fmtMoney(cash) +
      "\n\nСтрок ниже На кассе удалено: " + Math.max(0, plLr - rKassa);
    if(missing.length) msg += "\n\nНет строки в PL (пропущено):\n" + missing.slice(0,8).join("\n");
    if(skipped) msg += "\n\nОпераций без категории: " + skipped;
    return _alert(msg);
  }catch(e){
    Logger.log("rebuildPLFromDDS: " + e);
    return _alert("Ошибка: " + e);
  }
}





function fixSheetsFormatting(){
  // Наводит порядок в оформлении: выравнивание, формат чисел, жирный на итогах.
  // Разные строки приходили из разных мест и выглядели по-разному
  var report = [];
  try{
    var ss = SpreadsheetApp.openById(SS_ID);

    // ── ШТРАФЫ ──
    var wsF = ss.getSheetByName("Штрафы");
    if(wsF && wsF.getLastRow() >= 3){
      var lrF = wsF.getLastRow();
      var rows = lrF - 2;
      var rng = wsF.getRange(3, 1, rows, 21);
      rng.setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");

      // Номер, сумма, дата, статус по центру. Имя и тип по левому краю
      wsF.getRange(3, 1, rows, 21).setHorizontalAlignment("center").setFontWeight("normal");
      wsF.getRange(3, 2, rows, 2).setHorizontalAlignment("left").setFontWeight("normal");
      wsF.getRange(3, 4, rows, 1).setHorizontalAlignment("center")
         .setNumberFormat("#,##0").setFontWeight("normal");
      wsF.getRange(3, 5, rows, 1).setHorizontalAlignment("center")
         .setNumberFormat("DD.MM.YYYY").setFontWeight("normal");
      wsF.getRange(3, 6, rows, 1).setHorizontalAlignment("center").setFontWeight("normal");

      // Чередование фона и границы
      for(var rf = 3; rf <= lrF; rf++){
        if(!String(wsF.getRange(rf,2).getValue()||"").trim()) continue;
        wsF.getRange(rf,1,1,6).setBackground((rf-3)%2===0 ? "#FFFFFF" : "#F5F5F5")
          .setBorder(true,true,true,true,true,true,"#D9D9D9",SpreadsheetApp.BorderStyle.SOLID);
        wsF.setRowHeight(rf, 26);
      }
      // Выпадающие списки в колонках C (тип) и F (статус).
      // При записи из скрипта валидация слетала, значения вставлялись текстом
      try{
        var dvType = SpreadsheetApp.newDataValidation()
          .requireValueInList(["Не сдан отчёт","Цена слова","Опоздание","Нарушение правил","Пропуск посещения","Прочее"], true)
          .setAllowInvalid(false).build();
        var dvStatus = SpreadsheetApp.newDataValidation()
          .requireValueInList(["Не оплатил","Оплатил"], true)
          .setAllowInvalid(false).build();
        var span = Math.max(rows, 300);
        wsF.getRange(3, 3, span, 1).setDataValidation(dvType);
        wsF.getRange(3, 6, span, 1).setDataValidation(dvStatus);
      }catch(dvE){ Logger.log("fines validation: " + dvE); }

      report.push("Штрафы: " + rows + " строк выровнено, списки восстановлены");
    }

    // ── ДЕБЕТ ──
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    if(wsR && wsR.getLastRow() >= 3){
      var lrR = wsR.getLastRow();
      var rowsR = lrR - 2;

      // Денежные колонки: C, D, E, F, G, H
      [3,4,5,6,7,8].forEach(function(col){
        wsR.getRange(3, col, rowsR, 1)
           .setNumberFormat("#,##0").setHorizontalAlignment("center");
      });
      // Все денежные колонки обычным шрифтом
      [3,4,5,6,7,8].forEach(function(col){
        wsR.getRange(3, col, rowsR, 1).setFontWeight("normal");
      });
      // Номер и счётчики встреч по центру
      wsR.getRange(3, 1, rowsR, 21).setHorizontalAlignment("center");
      wsR.getRange(3, 17, rowsR, 2).setHorizontalAlignment("center").setNumberFormat("0");
      // Имя по левому краю
      wsR.getRange(3, 2, rowsR, 20).setHorizontalAlignment("left");
      // Дата входа
      wsR.getRange(3, 9, rowsR, 1).setNumberFormat("DD.MM.YYYY").setHorizontalAlignment("center");

      report.push("Дебет: суммы с пробелами, общий долг жирным");
    }

    // ── PL: строка Бонус резидента ──
    try{
      // Блок дописывания строк в PL убран: он ломал структуру
    }catch(e){}

    // ── ПОСЕЩЕНИЯ ──
    var wsV = ss.getSheetByName("BS - посещения");
  if(!wsV) return;
    if(wsV && wsV.getLastRow() >= 3){
      var lrV = wsV.getLastRow();
      var rowsV = lrV - 2;
      wsV.getRange(3, 1, rowsV, 21).setHorizontalAlignment("center");
      wsV.getRange(3, 2, rowsV, 1).setHorizontalAlignment("left");
      wsV.getRange(3, 3, rowsV, 1).setHorizontalAlignment("center");
      report.push("Посещения: выравнивание приведено к единому виду");
    }

    var msg = "🎨 Оформление\n\n" + report.join("\n");
    Logger.log(msg);
    return msg;
  }catch(e){
    return "Ошибка оформления: " + e;
  }
}

function recalcMonthsInProject(){
  // Месяцев в проекте: считается от даты входа до сегодня.
  // У части резидентов колонка была пустой
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    var wsV = ss.getSheetByName("BS - посещения");
  if(!wsV) return;
    if(!wsR || !wsV) return "Нет листов";

    // Даты входа по именам
    var dates = {};
    var lrR = wsR.getLastRow();
    for(var r = 3; r <= lrR; r++){
      var nm = String(wsR.getRange(r, RC.name).getValue()||"").trim();
      var d = wsR.getRange(r, RC.date).getValue();
      if(nm && d instanceof Date && d.getFullYear() > 2000) dates[nm] = d;
    }

    var today = new Date();
    var lrV = wsV.getLastRow();
    var filled = 0, missing = [];
    for(var v = 3; v <= lrV; v++){
      var name = String(wsV.getRange(v,2).getValue()||"").trim();
      if(!name) continue;
      var din = dates[name];
      if(!din){ missing.push(name); wsV.getRange(v,8).setValue(""); continue; }
      var months = (today.getFullYear()-din.getFullYear())*12 + (today.getMonth()-din.getMonth());
      if(today.getDate() < din.getDate()) months--;
      wsV.getRange(v,8).setValue(Math.max(0, months));
      filled++;
    }
    wsV.getRange(3,8,lrV-2,1).setHorizontalAlignment("center").setNumberFormat("0");

    var msg = "📅 Месяцев в проекте: заполнено " + filled;
    if(missing.length) msg += "\nБез даты входа: " + missing.join(", ");
    Logger.log(msg);
    return msg;
  }catch(e){ return "Ошибка расчёта месяцев: " + e; }
}

function removeVisitsColumnI(){
  // Убирает пустую колонку "Сумма продл. тг" из Посещений
  try{
    var wsV = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - посещения");
    if(!wsV) return "Нет листа";
    var head = String(wsV.getRange(2,9).getValue()||"").trim();
    if(head.indexOf("Сумма") >= 0 || head.indexOf("продл") >= 0 || !head){
      wsV.deleteColumn(9);
      return "Колонка I удалена (была: " + (head || "пустая") + ")";
    }
    return "Колонка I содержит '" + head + "', не трогаю";
  }catch(e){ return "Ошибка удаления колонки: " + e; }
}

function restoreVisitsAndFormat(){
  // ПОЛНОЕ ВОССТАНОВЛЕНИЕ за один запуск:
  // добавляет пропущенных резидентов в Посещения, чинит нумерацию,
  // чистит счётчики админов, пересчитывает пакеты встреч по тарифу,
  // приводит оформление всех строк к единому виду
  var report = [];
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    var wsV = ss.getSheetByName("BS - посещения");
  if(!wsV) return;
    if(!wsR) return "Нет листа дебета";

    // ── 0. Известные ошибки в тарифах правим до пересчёта пакетов ──
    try{
      var tfixes = _fixKnownTariffs();
      tfixes.forEach(function(t){ report.push(t); });
    }catch(e){}

    // ── 1. Активные резиденты в порядке дебета ──
    var lrR = wsR.getLastRow();
    var order = [], admins = [];
    for(var r = 3; r <= lrR; r++){
      var nm = String(wsR.getRange(r, RC.name).getValue()||"").trim();
      if(!nm) continue;
      var isAdmin = String(wsR.getRange(r, RC.admin).getValue()||"").trim() === "Да";
      var isFormer = String(wsR.getRange(r, RC.former).getValue()||"").trim() === "Да";
      if(isAdmin){ admins.push({name:nm, row:r}); continue; }
      if(isFormer) continue;
      order.push({name:nm, row:r});
    }
    report.push("Активных резидентов: " + order.length);

    // ── 2. Нумерация в дебете: админы без номера, счёт с первого резидента ──
    var num = 0;
    for(var r2 = 3; r2 <= lrR; r2++){
      var nm2 = String(wsR.getRange(r2, RC.name).getValue()||"").trim();
      if(!nm2){ wsR.getRange(r2,1).setValue(""); continue; }
      if(String(wsR.getRange(r2, RC.admin).getValue()||"").trim() === "Да"){
        wsR.getRange(r2,1).setValue("");
        // Счётчики встреч у админов не нужны
        wsR.getRange(r2,RC.done).setValue("");
        wsR.getRange(r2,RC.granted).setValue("");
        continue;
      }
      if(String(wsR.getRange(r2, RC.former).getValue()||"").trim() === "Да"){
        wsR.getRange(r2,1).setValue("");
        continue;
      }
      wsR.getRange(r2,1).setValue(++num);
    }
    report.push("Нумерация дебета: 1.." + num);

    // ── 3. Посещения: добавляем пропущенных, ставим порядок как в дебете ──
    if(wsV){
      var lrV = wsV.getLastRow();
      var lastColV = Math.max(wsV.getLastColumn(), 11);
      var existing = {};
      if(lrV >= 3){
        var vData = wsV.getRange(3, 1, lrV-2, lastColV).getValues();
        vData.forEach(function(row){
          var nm = String(row[1]||"").trim();
          if(nm) existing[nm] = row;
        });
      }

      var out = [], added = [];
      order.forEach(function(o, idx){
        var row = existing[o.name];
        if(!row){
          row = new Array(lastColV).fill("");
          row[1] = o.name;
          row[2] = 3;   // встреч в месяц по умолчанию
          added.push(o.name);
        }
        row[0] = idx + 1;
        out.push(row);
      });

      // Чистим лист и пишем заново
      if(lrV >= 3) wsV.getRange(3, 1, lrV-2, lastColV).clearContent();
      if(out.length){
        wsV.getRange(3, 1, out.length, lastColV).setValues(out);
        for(var vr = 0; vr < out.length; vr++){
          _applyRowStyle(wsV, 3 + vr, lastColV, vr % 2 === 1);
        }
      }
      report.push("В Посещениях: " + out.length + " строк");
      if(added.length) report.push("Добавлены: " + added.join(", "));
    }

    // ── 4. Пакеты встреч по тарифу ──
    var fixedPacks = [];
    order.forEach(function(o){
      var tariffSum = Number(wsR.getRange(o.row, RC.tariff).getValue()) || 0;
      if(tariffSum <= 0) return;
      var pack = _meetingsPerPackage(o.name, tariffSum);
      var cur = Number(wsR.getRange(o.row, RC.granted).getValue()) || 0;
      if(cur !== pack){
        wsR.getRange(o.row, RC.granted).setValue(pack);
        fixedPacks.push(o.name + ": " + cur + " → " + pack);
      }
      // Проведённые встречи из галочек
      var done = 0;
      try{
        if(wsV && wsV.getLastRow() >= 3){
          var vv = wsV.getRange(3, 2, wsV.getLastRow()-2, 32).getValues();
          for(var vi = 0; vi < vv.length; vi++){
            if(String(vv[vi][0]||"").trim() === o.name){
              for(var ci = 2; ci < vv[vi].length; ci++){
                if(vv[vi][ci] === true) done++;
              }
              break;
            }
          }
        }
      }catch(e){}
      wsR.getRange(o.row, RC.done).setValue(done);
    });
    if(fixedPacks.length) report.push("Пакеты встреч: " + fixedPacks.join("; "));

    // ── 5. Оформление дебета ──
    var lastColR = Math.max(wsR.getLastColumn(), 19);
    var styleIdx = 0;
    for(var r3 = 3; r3 <= lrR; r3++){
      if(!String(wsR.getRange(r3, RC.name).getValue()||"").trim()) continue;
      _applyRowStyle(wsR, r3, lastColR, styleIdx % 2 === 1);
      styleIdx++;
    }
    report.push("Оформление дебета приведено к единому виду");

    // ── 6. Оформление штрафов ──
    try{
      var wsF = ss.getSheetByName("Штрафы");
      if(wsF && wsF.getLastRow() >= 3){
        var lrF = wsF.getLastRow();
        var lastColF = Math.max(wsF.getLastColumn(), 6);
        var fIdx = 0, fNum = 0;
        for(var rf = 3; rf <= lrF; rf++){
          if(!String(wsF.getRange(rf,2).getValue()||"").trim()) continue;
          wsF.getRange(rf,1).setValue(++fNum);
          _applyRowStyle(wsF, rf, lastColF, fIdx % 2 === 1);
          fIdx++;
        }
        report.push("Штрафы: оформлено и перенумеровано " + fNum + " строк");
      }
    }catch(e){ report.push("Штрафы: " + e); }

    // ── 7. Колонка I, месяцы в проекте, оформление ──
    try{ report.push(removeVisitsColumnI()); }catch(e){}
    try{ report.push(recalcMonthsInProject()); }catch(e){}
    try{
      var fmtRes = // fixSheetsFormatting(); // ломает PL
      fmtRes.split("\n").forEach(function(l){ if(l.trim() && l.indexOf("🎨")<0) report.push(l); });
    }catch(e){}

    try{ _invalidateBundleCache(); }catch(e){}

    var msg = "🔧 Восстановление завершено\n\n" + report.join("\n");
    try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
    ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, msg); }catch(e){} });
    Logger.log(msg);
    return msg;
  }catch(e){
    var err = "Ошибка восстановления: " + e;
    Logger.log(err);
    try{ SpreadsheetApp.getUi().alert(err); }catch(x){}
    return err;
  }
}

function setupMeetingSystem(){
  // ОДНА КНОПКА: перестраивает таблицу под учёт встреч.
  // 1. Переименовывает колонки циклов в дебете
  // 2. Синхронизирует порядок резидентов в Посещениях с Дебетом
  // 3. Убирает админов из нумерации, счёт с первого резидента
  // 4. Ставит лист Посещения перед Штрафами
  var report = [];
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    var wsV = ss.getSheetByName("BS - посещения");
  if(!wsV) return;
    if(!wsR) return "Лист дебета не найден";

    // ── 1. Заголовки колонок Q и R ──
    wsR.getRange(2, RC.done).setValue("Встреч проведено");
    wsR.getRange(2, RC.granted).setValue("Встреч оплачено");
    wsR.getRange(2, RC.done, 1, 2).setFontWeight("bold");
    report.push("Колонки Q и R переименованы");

    // ── 2. Порядок резидентов из Дебета ──
    var lrR = wsR.getLastRow();
    var order = [];        // имена по порядку, без админов
    var adminNames = {};
    for(var r = 3; r <= lrR; r++){
      var nm = String(wsR.getRange(r, RC.name).getValue()||"").trim();
      if(!nm) continue;
      if(String(wsR.getRange(r, RC.admin).getValue()||"").trim() === "Да"){
        adminNames[nm] = true;
        continue;
      }
      if(String(wsR.getRange(r, RC.former).getValue()||"").trim() === "Да") continue; // бывший
      order.push(nm);
    }
    report.push("Активных резидентов: " + order.length);

    // ── 3. Пересобираем лист Посещений в том же порядке ──
    if(wsV){
      var lrV = wsV.getLastRow();
      var lastCol = Math.max(wsV.getLastColumn(), 11);
      var oldRows = {};
      var adminRows = [];
      if(lrV >= 3){
        var vData = wsV.getRange(3, 1, lrV-2, lastCol).getValues();
        vData.forEach(function(row){
          var nm = String(row[1]||"").trim();
          if(!nm) return;
          if(adminNames[nm]){ adminRows.push(row); return; }
          oldRows[nm] = row;
        });
      }

      // Собираем новый порядок: сначала резиденты как в дебете, потом админы
      var newData = [];
      var num = 0;
      order.forEach(function(nm){
        var row = oldRows[nm];
        if(!row){
          // Резидента не было в посещениях. создаём пустую строку
          row = new Array(lastCol).fill("");
          row[1] = nm;
          row[2] = 4;
          report.push("Добавлен в посещения: " + nm);
        }
        row[0] = ++num;             // номер по порядку, только резиденты
        newData.push(row);
      });
      // Админы в конец без номера
      adminRows.forEach(function(row){
        row[0] = "";
        newData.push(row);
      });

      if(newData.length){
        // Чистим старые строки и пишем новые
        if(lrV >= 3) wsV.getRange(3, 1, lrV-2, lastCol).clearContent();
        wsV.getRange(3, 1, newData.length, lastCol).setValues(newData);
        report.push("Порядок в Посещениях синхронизирован с Дебетом");
        report.push("Нумерация: 1.." + num + ", админы без номера");
      }
    } else {
      report.push("Лист Посещений не найден");
    }

    // ── 4. Ставим Посещения перед Штрафами ──
    try{
      var sheets = ss.getSheets();
      var idxFines = -1;
      for(var s = 0; s < sheets.length; s++){
        if(sheets[s].getName() === "Штрафы"){ idxFines = s + 1; break; }
      }
      if(idxFines > 0 && wsV){
        ss.setActiveSheet(wsV);
        ss.moveActiveSheet(idxFines);
        report.push("Лист Посещения поставлен перед Штрафами");
      }
    }catch(mvE){ report.push("Перемещение листа: " + mvE); }

    // ── 5. Пересчёт счётчиков ──
    try{
      autoRecalcCycles();
      report.push("Счётчики встреч пересчитаны");
    }catch(e){}

    var msg = "🔧 Система учёта встреч настроена\n\n" + report.join("\n") +
      "\n\nДальше проверьте вкладку Посещения в приложении и поправьте вторую цифру там, где нужно.";
    try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
    ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, msg); }catch(e){} });
    Logger.log(msg);
    return msg;
  }catch(e){
    var err = "Ошибка настройки: " + e;
    Logger.log(err);
    try{ SpreadsheetApp.getUi().alert(err); }catch(x){}
    return err;
  }
}

function syncVisitsOrder(){
  // Быстрая синхронизация порядка без остальных шагов.
  // Запускается сама после добавления или удаления резидента
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    var wsV = ss.getSheetByName("BS - посещения");
  if(!wsV) return;
    if(!wsR || !wsV) return;

    var lrR = wsR.getLastRow();
    var order = [], adminNames = {};
    for(var r = 3; r <= lrR; r++){
      var nm = String(wsR.getRange(r, RC.name).getValue()||"").trim();
      if(!nm) continue;
      if(String(wsR.getRange(r, RC.admin).getValue()||"").trim() === "Да"){ adminNames[nm] = true; continue; }
      if(String(wsR.getRange(r, RC.former).getValue()||"").trim() === "Да") continue;
      order.push(nm);
    }
    if(!order.length) return;

    var lrV = wsV.getLastRow();
    if(lrV < 3) return;
    var lastCol = Math.max(wsV.getLastColumn(), 11);
    var vData = wsV.getRange(3, 1, lrV-2, lastCol).getValues();
    var byName = {}, admins = [];
    vData.forEach(function(row){
      var nm = String(row[1]||"").trim();
      if(!nm) return;
      if(adminNames[nm]){ admins.push(row); return; }
      byName[nm] = row;
    });

    var out = [], num = 0;
    order.forEach(function(nm){
      var row = byName[nm];
      if(!row){
        row = new Array(lastCol).fill("");
        row[1] = nm; row[2] = 4;
      }
      row[0] = ++num;
      out.push(row);
    });
    admins.forEach(function(row){ row[0] = ""; out.push(row); });

    wsV.getRange(3, 1, lrV-2, lastCol).clearContent();
    wsV.getRange(3, 1, out.length, lastCol).setValues(out);
    Logger.log("syncVisitsOrder: порядок обновлён, резидентов " + num);
  }catch(e){ Logger.log("syncVisitsOrder: " + e); }
}

function _bsSyncVisits(name, done){
  // Лист «BS - посещения» повторяет счётчик дебета: H = проведено, D–G = галочки
  try{
    var wv = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - посещения");
    if(!wv || wv.getLastRow() < 3) return;
    var vd = wv.getRange(3, 2, wv.getLastRow()-2, 1).getValues();
    for(var vi = 0; vi < vd.length; vi++){
      if(String(vd[vi][0]||"").trim() !== String(name).trim()) continue;
      var line = [done>=1, done>=2, done>=3, done>=4, done];
      wv.getRange(3+vi, 4, 1, 5).setValues([line]);
      return;
    }
  }catch(e){ Logger.log("_bsSyncVisits: " + e); }
}

function autoRecalcCycles(){
  // УЧЁТ ВСТРЕЧ. Главный счётчик: дебет «проведено» (E). Лист посещений его повторяет.
  // Галочка, поставленная руками в посещениях, только ДОБАВЛЯЕТ встречу и никогда
  // не уменьшает счётчик: раньше отметки из приложения откатывались назад
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    var wsV=ss.getSheetByName("BS - посещения");
    if(!wsR || !wsV) return {ok:false};

    var visits={};
    if(wsV.getLastRow()>=3){
      wsV.getRange(3,1,wsV.getLastRow()-2,34).getValues().forEach(function(row){
        var nm=String(row[RA.name]||"").trim();
        if(!nm) return;
        var ch=0;
        for(var c=3;c<row.length;c++) if(row[c]===true) ch++;
        visits[nm]=Math.max(ch, Number(row[7])||0);
      });
    }

    var lrR=wsR.getLastRow();
    if(lrR<3) return {ok:true, changed:[]};
    var rows=wsR.getRange(3,1,lrR-2,RC_LAST).getValues();
    var changed=[];
    rows.forEach(function(row, i){
      var r=3+i;
      var name=String(row[RA.name]||"").trim();
      if(!name) return;
      if(String(row[RA.admin]||"").trim()==="Да") return;
      var curDone=Number(row[RA.done])||0;
      var v=visits[name];
      if(v!==undefined && v>curDone){
        wsR.getRange(r,RC.done).setValue(v);
        changed.push(name+": проведено "+curDone+" → "+v);
        curDone=v;
      }
      if(v!==undefined && v<curDone) _bsSyncVisits(name, curDone);
      var granted=Number(row[RA.granted])||0;
      if(granted<=0){
        var pack=_meetingsPerPackage(name, Number(row[RA.tariff])||0);
        wsR.getRange(r,RC.granted).setValue(pack);
        changed.push(name+": оплачено встреч установлено "+pack);
      }
    });
    if(changed.length) Logger.log("autoRecalcCycles: "+changed.join("; "));
    return {ok:true, changed:changed};
  }catch(e){
    Logger.log("autoRecalcCycles: "+e);
    return {ok:false, error:String(e)};
  }
}

function addMeetingsOnPayment(resName, amount){
  // Оплата продления: счётчик обнуляется, неиспользованные встречи переносятся.
  // Было 8 из 9, оплатил квартал. стало 0 из 10 (девять новых плюс одна в остатке).
  // Ходил в долг 11 из 9. стало 0 из 7 (девять новых минус два перебора)
  try{
    if(!resName || !amount) return;
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR) return;

    var lr=wsR.getLastRow();
    for(var r=3;r<=lr;r++){
      if(String(wsR.getRange(r, RC.name).getValue()||"").trim()!==String(resName).trim()) continue;

      var tariffSum=Number(wsR.getRange(r, RC.tariff).getValue())||0;
      if(tariffSum<=0) return;
      var periods=Math.floor(amount/tariffSum);
      if(periods<1) return;

      var pack=_meetingsPerPackage(resName, tariffSum) * periods;
      var done=Number(wsR.getRange(r,RC.done).getValue())||0;
      var granted=Number(wsR.getRange(r,RC.granted).getValue())||0;
      var left=granted-done;   // остаток, может быть отрицательным

      wsR.getRange(r,RC.done).setValue(0);
      _bsSyncVisits(resName, 0);
      wsR.getRange(r,RC.granted).setValue(Math.max(0, pack+left));

      Logger.log("Продление "+resName+": пакет "+pack+", перенос "+left+
        ", итого "+Math.max(0, pack+left));
      return;
    }
  }catch(e){ Logger.log("addMeetingsOnPayment: "+e); }
}

function _meetingsPerPackage(resName, tariffSum){
  // Сколько встреч даёт один оплаченный период.
  // Тариф от миллиона это год, от четырёхсот тысяч квартал
  var perMonth=3;
  try{
    var wsV=SpreadsheetApp.openById(SS_ID).getSheetByName("BS - посещения");
    if(wsV && wsV.getLastRow()>=3){
      var vv=wsV.getRange(3,2,wsV.getLastRow()-2,2).getValues();
      for(var i=0;i<vv.length;i++){
        if(String(vv[i][0]||"").trim()===String(resName).trim()){
          perMonth=parseInt(vv[i][1])||3;
          break;
        }
      }
    }
  }catch(e){}
  return _tariffToMonths(tariffSum) * perMonth;
}

function onTariffChanged(rowNum){
  // Смена тарифа в дебете: пересчитываем размер пакета встреч.
  // Остаток сохраняется пропорционально: было 4 из 9, тариф стал годовым. станет 4 из 36
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR) return;
    var name=String(wsR.getRange(rowNum, RC.name).getValue()||"").trim();
    if(!name) return;
    if(String(wsR.getRange(rowNum, RC.admin).getValue()||"").trim()==="Да") return;

    var tariffSum=Number(wsR.getRange(rowNum, RC.tariff).getValue())||0;
    if(tariffSum<=0) return;

    var newPack=_meetingsPerPackage(name, tariffSum);
    var done=Number(wsR.getRange(rowNum, RC.done).getValue())||0;
    var oldGranted=Number(wsR.getRange(rowNum,RC.granted).getValue())||0;
    if(newPack===oldGranted) return;

    wsR.getRange(rowNum,RC.granted).setValue(newPack);
    // Уведомление не шлём: смена тарифа видна в таблице, лишний шум не нужен
    Logger.log("Тариф изменён: "+name+", пакет "+oldGranted+" → "+newPack+", проведено "+done);
    _invalidateBundleCache();
  }catch(e){ Logger.log("onTariffChanged: "+e); }
}

function checkMeetingBalance(){
  // Кто заканчивает пакет встреч и кто ушёл в минус
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR) return;
    var lr=wsR.getLastRow();
    if(lr<3) return;

    var data=wsR.getRange(3, 1, lr-2, 21).getValues();
    var ending=[], debtors=[];

    data.forEach(function(row){
      var name=String(row[RA.name]||"").trim();
      if(!name) return;
      if(String(row[RA.former]||"").trim()==="Да") return;
      if(String(row[RA.admin]||"").trim()==="Да") return;

      var done=Number(row[RA.done])||0;
      var granted=Number(row[RA.granted])||0;
      if(granted<=0) return;
      var left=granted-done;

      if(left<0) debtors.push(name+": "+done+" / "+granted+" (перебор "+Math.abs(left)+")");
      else if(left<=2) ending.push(name+": "+done+" / "+granted+" (осталось "+left+")");
    });

    if(!ending.length && !debtors.length) return;
    var msg="📊 Встречи\n";
    if(debtors.length) msg+="\n❗️ Ходят в долг\n"+debtors.join("\n")+"\n";
    if(ending.length)  msg+="\n⏳ Заканчивается пакет\n"+ending.join("\n");
    _bsDigestSend("meetbal", msg);
    Logger.log(msg);
  }catch(e){ Logger.log("checkMeetingBalance: "+e); }
}

function _recalcCyclesUsed(){
  // Автоматически пересчитывает "Циклов использовано" по количеству галочек в Посещениях.
  // Логика: Использовано = floor(всего галочек / тариф)
  // Также возвращает массив {name, totalChecks, tariff, used, available, left}
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    var wsV=ss.getSheetByName("BS - посещения");
  if(!wsV) return;
    if(!wsR || !wsV) return {ok:false, error:"sheets missing"};
    
    var lrV=wsV.getLastRow();
    var lrR=wsR.getLastRow();
    
    // Соберём данные по галочкам по каждому имени
    var checksByName={};
    if(lrV>=3){
      var vData=wsV.getRange(3,1,lrV-2,30).getValues();
      vData.forEach(function(row){
        var name=String(row[RA.name]||"").trim();
        if(!name)return;
        var tariff=parseInt(row[RA.paid])||4;
        var checked=0;
        // галочки начинаются с колонки D (индекс 3) до конца
        for(var c=3;c<row.length;c++){
          if(row[c]===true)checked++;
        }
        checksByName[name]={tariff:tariff, total:checked};
      });
    }
    
    // Обновляем "Циклов использовано" в Резиденты
    var summary=[];
    if(lrR>=3){
      for(var r=3;r<=lrR;r++){
        var name=String(wsR.getRange(r, RC.name).getValue()||"").trim();
        if(!name)continue;
        var ch=checksByName[name];
        if(!ch)continue;
        
        var paidCycles=Number(wsR.getRange(r,RC.done).getValue())||1;
        var tariff=ch.tariff;
        var totalChecks=ch.total;
        
        // Циклов использовано = floor(всего галочек / тариф)
        var usedCycles=Math.floor(totalChecks/tariff);
        // Не больше чем оплачено
        if(usedCycles>paidCycles)usedCycles=paidCycles;
        
        // Записываем
        var currentUsed=Number(wsR.getRange(r,RC.granted).getValue())||0;
        if(currentUsed!==usedCycles){
          wsR.getRange(r,RC.done).setValue(usedCycles);
        }
        
        var available=paidCycles*tariff;
        var left=Math.max(0, available-totalChecks);
        summary.push({
          name: name,
          tariff: tariff,
          paidCycles: paidCycles,
          totalChecks: totalChecks,
          usedCycles: usedCycles,
          available: available,
          left: left
        });
      }
    }
    
    return {ok:true, summary:summary};
  }catch(e){
    Logger.log("_recalcCyclesUsed: "+e);
    return {ok:false, error:e.toString()};
  }
}


function _getPartnerName(resName){
  // Возвращает имя партнёра резидента (из колонки S=19) или null
  try{
    if(!resName)return null;
    _ensurePartnerColumn();
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR)return null;
    var lr=wsR.getLastRow();
    for(var r=3;r<=lr;r++){
      if(String(wsR.getRange(r, RC.name).getValue()||"").trim()===String(resName).trim()){
        var partner=String(wsR.getRange(r, RC.partner).getValue()||"").trim();
        // В ячейке может стоять служебное значение вместо имени. это не партнёр
        var lowered = partner.toLowerCase();
        if(!partner || lowered==="нет" || lowered==="да" || lowered==="-" || lowered==="-") return null;
        if(partner === String(resName).trim()) return null; // сам себе не партнёр
        return partner;
      }
    }
    return null;
  }catch(e){
    Logger.log("_getPartnerName: "+e);
    return null;
  }
}

function _findPartnerMeetLink(partnerName, eventDate, eventTime){
  // Ищет существующую встречу партнёра на ту же дату/время в Расписании
  // Возвращает {meetLink, calEventId} если найдено, иначе null
  try{
    if(!partnerName || !eventDate)return null;
    var ss=SpreadsheetApp.openById(SS_ID);
    var sheet=ss.getSheetByName("Расписание");
    if(!sheet)return null;
    var lr=sheet.getLastRow();
    if(lr<3)return null;
    
    var data=sheet.getRange(3, 1, lr-2, 21).getValues();
    var targetDateStr=eventDate instanceof Date 
      ? Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy") 
      : String(eventDate);
    var targetTimeStr=eventTime instanceof Date 
      ? Utilities.formatDate(eventTime,"Asia/Almaty","HH:mm") 
      : (eventTime ? String(eventTime) : "");
    
    for(var i=0;i<data.length;i++){
      var pName=String(data[i][1]||"").trim();
      var pDate=data[i][2];
      var pTime=data[i][3];
      var pMeet=String(data[i][5]||"").trim();
      
      if(pName!==partnerName)continue;
      if(!pMeet || pMeet==="https://meet.google.com/new")continue;
      
      var pDateStr=pDate instanceof Date 
        ? Utilities.formatDate(pDate,"Asia/Almaty","dd.MM.yyyy") 
        : String(pDate);
      var pTimeStr=pTime instanceof Date 
        ? Utilities.formatDate(pTime,"Asia/Almaty","HH:mm") 
        : (pTime ? String(pTime) : "");
      
      // Совпадение даты (dd.MM.yyyy или dd.MM)
      var dateMatch = pDateStr===targetDateStr || pDateStr.substring(0,5)===targetDateStr.substring(0,5);
      if(!dateMatch)continue;
      
      // Дата совпала. время НЕ сравниваем строго: у пары встреча одна,
      // а форматы времени (Date vs строка) уже ловили нас на несовпадении
      
      Logger.log("Найден партнёр "+partnerName+" с Meet ссылкой "+pMeet);
      return {meetLink:pMeet, row:3+i};
    }
    return null;
  }catch(e){
    Logger.log("_findPartnerMeetLink: "+e);
    return null;
  }
}

function _autoCreateMeet(row){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var sheet=ss.getSheetByName("Расписание");
    if(!sheet)return;
    var eventDate=sheet.getRange(row,3).getValue();
    var eventTime=sheet.getRange(row,4).getValue();
    var resName=sheet.getRange(row,2).getValue()||"";
    var desc=sheet.getRange(row,5).getValue()||"Business Surgery";
    if(!eventDate||!resName)return;
    
    // ЗАЩИТА 3: если в строке уже есть Meet ссылка. событие уже создано, не дублируем
    var existingMeet=String(sheet.getRange(row,6).getValue()||"").trim();
    if(existingMeet && existingMeet.indexOf("http")>=0){
      Logger.log("_autoCreateMeet: строка "+row+" уже имеет Meet, пропускаем");
      return;
    }
    
    // ЗАЩИТА 4: lock по резидент+дата на 90 сек (параллельные вызовы триггера и action)
    var meetLockCache=CacheService.getScriptCache();
    var dateLockStr=eventDate instanceof Date?Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy"):String(eventDate);
    var meetLockKey="cremeet_"+String(resName).trim()+"_"+dateLockStr;
    if(meetLockCache.get(meetLockKey)){
      Logger.log("_autoCreateMeet: lock активен "+meetLockKey);
      return;
    }
    meetLockCache.put(meetLockKey,"1",90);
    
    // ПАРТНЁРСТВО: если у резидента указан партнёр и у него уже есть встреча
    // на ту же дату/время - копируем Meet ссылку, не создаём новое Calendar событие
    try{
      var partnerName=_getPartnerName(resName);
      if(partnerName){
        var partnerInfo=_findPartnerMeetLink(partnerName, eventDate, eventTime);
        if(partnerInfo && partnerInfo.meetLink){
          // Просто копируем ссылку и выходим
          sheet.getRange(row,6).setValue(partnerInfo.meetLink);
          sheet.getRange(row,1).setValue(row-3);
          
          // Уведомляем резидента
          var resChatIdP=_getChatId(resName);
          var dateStrP=Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy");
          var timeStrP=eventTime instanceof Date?Utilities.formatDate(eventTime,"Asia/Almaty","HH:mm"):"";
          var notifP="🤝 Партнёрская встреча с "+partnerName+"\n📅 "+dateStrP+(timeStrP?" "+timeStrP:"")+"\n🔗 "+partnerInfo.meetLink;
          if(resChatIdP)tgSend(resChatIdP, notifP, {
            reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS App",web_app:{url:getWebAppUrl("schedule")}}]]})
          });
          ADMIN_IDS.forEach(function(id){
            try{bsSysNoteTo(id, "🤝 Партнёрская встреча\n👥 "+resName+" + "+partnerName+"\n📅 "+dateStrP+(timeStrP?" "+timeStrP:"")+"\n🔗 "+partnerInfo.meetLink);}catch(e){}
          });
          Logger.log("Партнёрская встреча: "+resName+" + "+partnerName+", ссылка скопирована");
          return; // НЕ создаём дублирующее событие
        }
      }
    }catch(partErr){Logger.log("partner check: "+partErr);}
    // Определяем формат резидента из Дебета
    var resFmt="Офлайн";
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(wsR){
      var lrR=wsR.getLastRow();
      for(var r=3;r<=lrR;r++){
        if(wsR.getRange(r, RC.name).getValue()===resName){
          resFmt=String(wsR.getRange(r, RC.format)?wsR.getRange(r, RC.format).getValue():"Офлайн");
          break;
        }
      }
    }
    // Офлайн. записываем адрес вместо Meet
    if(resFmt==="Офлайн"){
      var offAddr=getText("offline_address",null)||"г.Алматы, Достык 44";
      sheet.getRange(row,6).setValue(offAddr);
      sheet.getRange(row,1).setValue(row-3);
      // Отправляем в топик ВАЖНОЕ
      var dStr2=Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy");
      var tStr2=eventTime instanceof Date?Utilities.formatDate(eventTime,"Asia/Almaty","HH:mm"):"12:00";
      var importantMsg="📌Трекинг встреча\nДата: "+dStr2+"\nВремя: "+tStr2+"\nАдрес: "+offAddr+
        "\n\n⚠️ Подготовьте задачи заранее, выложитесь чтобы выполнить их на 100%\n⚠️ За опоздание предусмотрен штраф 10 000 тенге";
      if(GROUP_CHAT_ID&&IMPORTANT_TOPIC_ID){
        try{
          UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
            method:"post",contentType:"application/json",
            payload:JSON.stringify({
              chat_id:GROUP_CHAT_ID,
              message_thread_id:parseInt(IMPORTANT_TOPIC_ID),
              text:importantMsg
            }),muteHttpExceptions:true
          });
        }catch(ie){Logger.log("ВАЖНОЕ: "+ie);}
      }
      Logger.log("Офлайн: "+resName+" "+offAddr);
      return;
    }
    // Онлайн. создаём Meet
    // Точная Almaty UTC+5 через Date.UTC (вычитаем 5 часов чтобы получить UTC)
    var dStr=Utilities.formatDate(eventDate,"Asia/Almaty","yyyy-MM-dd");
    // Берём ОТОБРАЖАЕМОЕ время из ячейки (как видит пользователь)
    var hStr=sheet.getRange(row,4).getDisplayValue()||"12:00";
    Logger.log("autoCreateMeet: date="+dStr+", time(display)="+hStr);
    var dateParts=dStr.split("-");
    var timeParts=hStr.split(":");
    var year=parseInt(dateParts[0]);
    var month=parseInt(dateParts[1])-1;
    var day=parseInt(dateParts[2]);
    var hour=parseInt(timeParts[0])||0;
    var minute=parseInt(timeParts[1])||0;
    // Создаём событие через Calendar Advanced API с явной TZ Almaty
    var title="Встреча BS. "+resName;
    // Используем отдельный календарь "Business Surgery Meetings"
    var cal=null;
    var calProps=PropertiesService.getScriptProperties();
    var bsCalId=calProps.getProperty("BS_CALENDAR_ID");
    if(bsCalId){
      try{cal=CalendarApp.getCalendarById(bsCalId);}catch(getCalErr){cal=null;}
    }
    if(!cal){
      var allCals=CalendarApp.getAllOwnedCalendars();
      for(var ci=0;ci<allCals.length;ci++){
        if(allCals[ci].getName()==="Business Surgery Meetings"){
          cal=allCals[ci];
          calProps.setProperty("BS_CALENDAR_ID",cal.getId());
          break;
        }
      }
    }
    if(!cal){
      cal=CalendarApp.createCalendar("Business Surgery Meetings",{
        summary:"Встречи Business Surgery. трекинги и разборы",
        color:CalendarApp.Color.RED
      });
      calProps.setProperty("BS_CALENDAR_ID",cal.getId());
      var newCalId=cal.getId();
      // Расшариваем через Calendar Advanced API (cal.addEditor не существует)
      ["baraka.erniyazov@gmail.com","business.hirurgiya@gmail.com"].forEach(function(emShare){
        try{
          Calendar.Acl.insert({scope:{type:"user",value:emShare},role:"writer"}, newCalId);
        }catch(shErr){Logger.log("share "+emShare+": "+shErr);}
      });
      // Красный цвет
      try{
        // Для календаря 11 = Citron (жёлтый). Tomato (красный) для календаря = colorRgbFormat
        Calendar.CalendarList.patch({
          backgroundColor:"#dc2127",
          foregroundColor:"#ffffff"
        }, newCalId, {colorRgbFormat:true});
      }catch(colErr){Logger.log("calendar color: "+colErr);}
    }
    var calId=cal.getId();
    // ISO 8601 формат с явной TZ
    var startISO=Utilities.formatString("%04d-%02d-%02dT%02d:%02d:00+05:00",year,month+1,day,hour,minute);
    var endHour=hour+1;
    var endDay=day;
    var endMonth=month+1;
    var endYear=year;
    if(endHour>=24){endHour-=24;endDay++;}
    var endISO=Utilities.formatString("%04d-%02d-%02dT%02d:%02d:00+05:00",endYear,endMonth,endDay,endHour,minute);
    Logger.log("Event: "+startISO+" → "+endISO);
    var event;
    try{
      // Улучшенное описание встречи
      var betterDesc="🎯 Трекинг Business Surgery\n\n"+
        "👤 Резидент: "+resName+"\n"+
        "🗓 Дата: "+Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy")+"\n"+
        "⏰ Время: "+hStr+" (Almaty)\n"+
        "⏱ Длительность: 60 мин\n\n"+
        "📋 Подготовиться:\n"+
        "• Цифры за последние 10 дней\n"+
        "• Что сделал из плана прошлого трекинга\n"+
        "• Главный вызов сейчас\n"+
        "• Гипотезы на следующие 10 дней\n\n"+
        "🔗 Резидентский бот: t.me/bsurgery_bot\n"+
        "📱 Mini App с приложением и расписанием\n\n"+
        " \nBusiness Surgery. Хирургия бизнеса\n@business.surgery";
      // Все email основателей. добавляем как attendees с responseStatus:accepted
      // Это решает проблему "Meet просит подтверждение от организатора"
      // когда заходишь под любым из этих аккаунтов
      // imbahirr@gmail.com. владелец календаря, добавлять не нужно
      // (Google автоматически копирует событие в его default календарь = дубль)
      var attendeesList=[
        {email:"business.hirurgiya@gmail.com", responseStatus:"accepted"},
        {email:"baraka.erniyazov@gmail.com", responseStatus:"accepted"}
      ];
      var eventResource={
        summary:title,
        description:betterDesc,
        start:{dateTime:startISO,timeZone:"Asia/Almaty"},
        end:{dateTime:endISO,timeZone:"Asia/Almaty"},
        attendees:attendeesList,
        guestsCanModify:true,
        guestsCanInviteOthers:false,
        colorId:"11", // 11 = Tomato (красный) для событий
        conferenceData:{
          createRequest:{
            requestId:"bs-"+row+"-"+Date.now(),
            conferenceSolutionKey:{type:"hangoutsMeet"}
          }
        },
        reminders:{
          useDefault:false,
          overrides:[
            {method:"popup", minutes:60},
            {method:"email", minutes:60}
          ]
        }
      };
      var insertedEvent=Calendar.Events.insert(eventResource,calId,{conferenceDataVersion:1,sendUpdates:"all"});
      event=cal.getEventById(insertedEvent.id);
    }catch(advErr){
      Logger.log("Advanced API err: "+advErr);
      // НЕ создаём fallback событие (это давало дубль в default календаре)
      // Лучше пусть встреча не создастся в календаре, чем будет дубль
      throw advErr;
    }
    // Цвет события (красный/томат)
    try{if(event)event.setColor(CalendarApp.EventColor.TOMATO);}catch(ce){}
    var meetLink="";
    try{
      // Получаем Meet ссылку из созданного события
      if(insertedEvent && insertedEvent.conferenceData && insertedEvent.conferenceData.entryPoints){
        for(var ei=0;ei<insertedEvent.conferenceData.entryPoints.length;ei++){
          if(insertedEvent.conferenceData.entryPoints[ei].entryPointType==="video"){
            meetLink=insertedEvent.conferenceData.entryPoints[ei].uri;
            break;
          }
        }
      }
      // Если не получили. перезапросим через patch
      if(!meetLink){
        Utilities.sleep(1000);
        var refreshed=Calendar.Events.get(calId,insertedEvent.id);
        if(refreshed.conferenceData && refreshed.conferenceData.entryPoints){
          for(var ei2=0;ei2<refreshed.conferenceData.entryPoints.length;ei2++){
            if(refreshed.conferenceData.entryPoints[ei2].entryPointType==="video"){
              meetLink=refreshed.conferenceData.entryPoints[ei2].uri;
              break;
            }
          }
        }
      }
    }catch(meetErr){Logger.log("MeetLink: "+meetErr);}
    if(!meetLink)meetLink="https://meet.google.com/new";
    sheet.getRange(row,6).setValue(meetLink);
    sheet.getRange(row,1).setValue(row-3);
    
    // ПАРТНЁРСТВО (двусторонняя синхронизация): если у партнёра есть строка на ту же дату
    // БЕЗ ссылки. копируем ему нашу ссылку (решает случай "добавили в обратном порядке")
    try{
      var partnerSync=_getPartnerName(resName);
      if(partnerSync && meetLink.indexOf("meet.google.com/new")<0){
        var lrSync=sheet.getLastRow();
        var myDateStr=Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy");
        var myTimeStr=eventTime instanceof Date?Utilities.formatDate(eventTime,"Asia/Almaty","HH:mm"):String(eventTime||"12:00");
        var partnerRow=-1;

        // Ищем строку партнёра на эту дату
        for(var ps=3;ps<=lrSync;ps++){
          if(String(sheet.getRange(ps,2).getValue()||"").trim()!==partnerSync)continue;
          var pd=sheet.getRange(ps,3).getValue();
          var pdStr=pd instanceof Date?Utilities.formatDate(pd,"Asia/Almaty","dd.MM.yyyy"):String(pd);
          if(pdStr===myDateStr || pdStr.substring(0,5)===myDateStr.substring(0,5)){ partnerRow=ps; break; }
        }

        if(partnerRow>0){
          // Строка есть: ВСЕГДА ставим общую ссылку, даже если у партнёра была своя.
          // Раньше перезапись шла только для пустой ячейки, отсюда разные ссылки у пары
          var pMeetCur=String(sheet.getRange(partnerRow,6).getValue()||"").trim();
          if(pMeetCur!==meetLink){
            sheet.getRange(partnerRow,6).setValue(meetLink);
            Logger.log("Партнёрство: ссылка синхронизирована для "+partnerSync+" (строка "+partnerRow+")");
          }
        } else {
          // Строки нет: создаём её, чтобы встреча была видна обоим
          var newPRow=sheet.getLastRow()+1;
          sheet.getRange(newPRow,1).setValue(newPRow-2);
          sheet.getRange(newPRow,2).setValue(partnerSync);
          sheet.getRange(newPRow,3).setValue(eventDate).setNumberFormat("dd.MM.yyyy");
          sheet.getRange(newPRow,4).setValue(myTimeStr);
          sheet.getRange(newPRow,5).setValue(sheet.getRange(row,5).getValue());
          sheet.getRange(newPRow,6).setValue(meetLink);
          partnerRow=newPRow;
          Logger.log("Партнёрство: создана строка для "+partnerSync);
        }

        // Уведомляем партнёра той же ссылкой
        try{
          var pChatId=_getChatId(partnerSync);
          if(pChatId){
            var notifCache=CacheService.getScriptCache();
            var nKey="pnotif_"+partnerSync+"_"+myDateStr;
            if(!notifCache.get(nKey)){
              notifCache.put(nKey,"1",3600);
              tgSend(pChatId,"🤝 Партнёрская встреча с "+resName+"\n\n📅 "+myDateStr+" в "+myTimeStr+"\n🔗 "+meetLink+"\n\nСсылка одна на двоих.");
            }
          }
        }catch(pnE){Logger.log("partner notify: "+pnE);}
      }
    }catch(syncE){Logger.log("partner sync: "+syncE);}
    // Защита от двойного уведомления
    var notifCacheO=CacheService.getScriptCache();
    var notifKeyO="meet_notif_"+row+"_"+eventDate.getTime();
    if(notifCacheO.get(notifKeyO)){return;}
    notifCacheO.put(notifKeyO,"1",24*3600);
    // Уведомляем при создании онлайн встречи
    var resChatIdO=_getChatId(resName);
    var dateStrO=Utilities.formatDate(eventDate,"Asia/Almaty","dd.MM.yyyy");
    var timeStrO=eventTime instanceof Date?Utilities.formatDate(eventTime,"Asia/Almaty","HH:mm"):"";
    var notifO="📌 Встреча запланирована!\n👤 "+resName+"\n📅 "+dateStrO+(timeStrO?" "+timeStrO:"")+"\n🔗 "+meetLink;
    if(resChatIdO)tgSend(resChatIdO,"📌 Встреча запланирована!\n📅 "+dateStrO+(timeStrO?" "+timeStrO:"")+"\n🔗 "+meetLink,{
      reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS App",web_app:{url:getWebAppUrl("schedule")}}]]})
    });
    ADMIN_IDS.forEach(function(id){bsSysNoteTo(id,notifO);});
    Logger.log("Онлайн Meet: "+meetLink);
  }catch(e){Logger.log("_autoCreateMeet: "+e);}
}
function _updateVisitMonthsRow(rowInResidents){
  // rowInResidents = строка в листе Резидентов (при изменении даты)
  // или строка в листе Посещений (при вводе имени)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsV=ss.getSheetByName("BS - посещения");
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsV||!wsR)return;
    // Определяем имя резидента из строки Резидентов
    var resName=wsR.getRange(rowInResidents, RC.name).getValue();
    if(!resName)return;
    var dateIn=wsR.getRange(rowInResidents, RC.date).getValue();
    if(!dateIn||!(dateIn instanceof Date)||dateIn.getFullYear()<2000)return;
    var today=new Date();
    var months=(today.getFullYear()-dateIn.getFullYear())*12+(today.getMonth()-dateIn.getMonth());
    // Находим эту строку в Посещениях
    var lrV=wsV.getLastRow();
    for(var r=3;r<=lrV;r++){
      if(wsV.getRange(r,2).getValue()===resName){
        wsV.getRange(r,8).setValue(months>=0?months:0);
        return;
      }
    }
  }catch(e){Logger.log("_updateVisitMonthsRow: "+e);}
}

function _updateVisitMonthsByName(name){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsV=ss.getSheetByName("BS - посещения");
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsV||!wsR)return;
    var lrR=wsR.getLastRow();
    var dateIn=null;
    for(var r=3;r<=lrR;r++){
      if(wsR.getRange(r, RC.name).getValue()===name){
        dateIn=wsR.getRange(r, RC.date).getValue();
        break;
      }
    }
    if(!dateIn||!(dateIn instanceof Date)||dateIn.getFullYear()<2000)return;
    var today=new Date();
    var months=(today.getFullYear()-dateIn.getFullYear())*12+(today.getMonth()-dateIn.getMonth());
    var lrV=wsV.getLastRow();
    for(var r=3;r<=lrV;r++){
      if(wsV.getRange(r,2).getValue()===name){
        wsV.getRange(r,8).setValue(months>=0?months:0);
        return;
      }
    }
  }catch(e){Logger.log("_updateVisitMonthsByName: "+e);}
}


function testWriteToDDS(){
  _writeToDDS("Тест",10000,"Штраф");
  SpreadsheetApp.getActiveSpreadsheet().toast("Готово. проверьте ОДДС последнюю строку");
}

function _writeToDDS(name,amount,fineType){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Учет ДДС");
    if(!ws)return;
    var r=ws.getLastRow()+1;
    ws.showRows(r,1); // показываем строку если скрыта
    ws.getRange(r,1,1,6).setValues([[new Date(),amount||0,Math.round((amount||0)*0.04),"БХ Штраф",name||"","Комиссия+налог"]]);
    ws.getRange(r,1).setNumberFormat("DD.MM.YYYY");
    ws.getRange(r,2).setNumberFormat("#,##0");
    ws.getRange(r,3).setNumberFormat("#,##0");
  }catch(e){Logger.log("writeDDS:"+e);}
  try{recalcPL();}catch(plE){Logger.log("recalcPL: "+plE);}
}

function _setTariff(ws,row,tariff){
  var g=ws.getRange(row,7);
  g.clearDataValidations();g.clearContent();
  if(tariff==="3"){
    g.setValue(" ").setBackground("#CCCCCC").setFontColor("#AAAAAA")
     .setHorizontalAlignment("center").setFontFamily("Montserrat").setFontSize(10);
    g.setDataValidation(SpreadsheetApp.newDataValidation()
      .requireValueInList([" "],true).setAllowInvalid(false).build());
    ws.getRange(row, RC.tariff).setValue(0);ws.getRange(row, RC.granted).setValue(0);ws.getRange(row,9).clearContent();
  } else {
    g.setBackground((row-3)%2===0?WHT:G1).setFontColor(BLK);
    g.insertCheckboxes();ws.getRange(row, RC.tariff).setValue(0);ws.getRange(row, RC.granted).setValue(0);
  }
}

// ── Обработка нажатия галочки посещения ──────────────────────────────────
function _onCheckbox(ws, row, col){
  var tariff=parseInt(ws.getRange(row, RC.paid).getValue()||"4");
  var name=ws.getRange(row, RC.name).getValue();if(!name)return;
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");if(!wsR)return;
  var chatId="",tariffRub=0,resRow=-1;
  var lr=wsR.getLastRow();
  for(var r=3;r<=lr;r++){
    if(wsR.getRange(r, RC.name).getValue()===name){
      chatId=String(wsR.getRange(r, RC.chat).getValue()||"");
      tariffRub=Number(wsR.getRange(r, RC.tariff).getValue())||0;
      resRow=r;break;
    }
  }
  // Номер нажатой галочки
  var checkNum=col-3; // col4=1, col5=2, col6=3, col7=4

  // Логируем встречу ДАТОЙ ИЗ РАСПИСАНИЯ, а не датой клика.
  // Раньше галочка, поставленная на следующий день, записывала встречу сегодняшним
  // числом, и в календаре приложения она появлялась не в тот день
  try{
    var meetDateForLog="";
    try{
      var wsSchLog=SpreadsheetApp.openById(SS_ID).getSheetByName("Расписание");
      if(wsSchLog && wsSchLog.getLastRow()>=3){
        var lrSL=wsSchLog.getLastRow();
        var sData=wsSchLog.getRange(3,2,lrSL-2,2).getValues();
        var todayMs=new Date().getTime();
        var bestDiff=-1;
        for(var si=0;si<sData.length;si++){
          if(String(sData[si][0]||"").trim()!==String(name).trim())continue;
          var sd=sData[si][1];
          if(!(sd instanceof Date))continue;
          var diff=Math.abs(todayMs-sd.getTime());
          // Берём ближайшую к сегодня встречу в пределах трёх суток
          if(diff<=3*24*3600*1000 && (bestDiff<0 || diff<bestDiff)){
            bestDiff=diff;
            meetDateForLog=Utilities.formatDate(sd,"Asia/Almaty","dd.MM.yyyy");
          }
        }
      }
    }catch(sErr){}
    _logMeetingHappened(name, meetDateForLog);
  }catch(logE){Logger.log("log meeting: "+logE);}

  // Считаем сколько галочек отмечено
  var count=0;
  for(var c=4;c<4+tariff;c++)if(ws.getRange(row,c).getValue()===true)count++;

  // Отправляем сообщение за каждую галочку (кроме финального)
  var isComplete=(count>=tariff);

  if(!isComplete){
    if(chatId){
      // Промежуточное сообщение за конкретную галочку
      var key="visit_"+checkNum;
      var monthsCount=_getResidentMonths(name);
      var msg=getText(key,{
        "имя":name.split(" ")[0],
        "месяцев":monthsCount
      });
      if(msg){
        try{
          tgSend(chatId,msg);
          Logger.log("✅ Уведомление о встрече отправлено: "+name+" (chatId="+chatId+", check="+checkNum+")");
        }catch(sendErr){
          Logger.log("❌ Не удалось отправить уведомление "+name+": "+sendErr);
          try{bsSysNote("⚠️ Не удалось отправить уведомление резиденту "+name+" (chatId="+chatId+"). Ошибка: "+sendErr);}catch(e){}
        }
      } else {
        Logger.log("⚠️ Пустой шаблон сообщения для "+key);
      }
    } else {
      // Chat ID отсутствует у резидента. сообщаем админам
      Logger.log("❌ У резидента "+name+" нет Chat ID в столбце N листа \"BS - резиденты дебет\"");
      try{
        bsSysNote("⚠️ <b>Не отправлено уведомление о завершённой встрече</b>\n\n👤 "+name+"\n📋 Галочка №"+checkNum+"\n\n🔴 Причина: <b>не привязан Chat ID</b> в листе \"BS - резиденты дебет\" (колонка N)\n\nКак исправить:\n1. Открой лист \"BS - резиденты дебет\"\n2. Найди строку с резидентом "+name+"\n3. В колонку N (Chat ID) вставь его Telegram ID\n\nПодсказка: ID можно посмотреть в листе \"Подписчики канала\" (если он раньше писал боту)", {parse_mode:"HTML"});
      }catch(e){}
    }
  }

  if(isComplete){
    var flag=parseInt(ws.getRange(row, RC.source).getValue()||0);
    // Сбрасываем все галочки
    for(var c=4;c<=7;c++){
      var cv=ws.getRange(row,c).getValue();
      if(cv===true||cv===false)ws.getRange(row,c).setValue(false);
    }
    if(flag===1){ws.getRange(row, RC.source).setValue(0);ws.getRange(row, RC.former).setValue(0);return;}

    // Начисляем продление. Циклов использовано пересчитается автоматически через _recalcCyclesUsed
    if(tariffRub>0&&resRow>0){
      var cur=Number(wsR.getRange(resRow, RC.renew).getValue())||0;
      wsR.getRange(resRow, RC.renew).setValue(cur+tariffRub);
      _updateResRow(resRow);
      ws.getRange(row, RC.date).setValue(tariffRub);
      ws.getRange(row, RC.source).setValue(1);
    }

    // Зелёная подсветка
    ws.getRange(row,1,1,9).setBackground(GRN_BG);
    SpreadsheetApp.flush();Utilities.sleep(2000);
    ws.getRange(row,1,1,9).setBackground((row-3)%2===0?WHT:G1);

    // Запускаем дайджест через Gemini
    try{_sendCycleDigest(name,chatId,tariff);}catch(de){}
    // НЕ удаляем из расписания автоматически. пользователь делает это вручную
    // через Mini App "Встреча прошла" чтобы не было неожиданных удалений
    // Финальное сообщение БЕЗ кнопки оплаты (по запросу: убрать продление автоматически)
    // Резидент сам решает продолжать или нет, без давления
    if(chatId){
      var completeMsg=name.split(" ")[0]+", цикл из "+tariff+" встреч завершён.\n\n"+
        "Спасибо за работу. Если захочешь продолжить, напиши Береке или Рустаму напрямую.";
      tgSend(chatId, completeMsg);
    }
  }
}

// ── Перенос в бывшие ─────────────────────────────────────────────────────
function _toFormer(row, expectedName){
  // ЗАЩИТА ОТ КАСКАДА: удаление строки смещает нумерацию, из-за чего повторные вызовы
  // удаляли НЕ ТЕХ резидентов. Теперь строка проверяется по имени и по значению "Да"
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет"),wsF=ss.getSheetByName("Бывшие резиденты"),wsV=ss.getSheetByName("BS - посещения");
  if(!wsR||!wsF)return;
  var name=wsR.getRange(row, RC.name).getValue();if(!name)return;
  
  // Сверка: если ожидали конкретное имя, а в строке другое. строка сместилась, ищем правильную
  if(expectedName && String(name).trim()!==String(expectedName).trim()){
    var lrFix=wsR.getLastRow();
    var found=-1;
    for(var fr=3;fr<=lrFix;fr++){
      if(String(wsR.getRange(fr, RC.name).getValue()||"").trim()===String(expectedName).trim()){found=fr;break;}
    }
    if(found<0){Logger.log("_toFormer: строка для "+expectedName+" не найдена, отмена");return;}
    row=found;
    name=expectedName;
  }
  
  // Проверка: в этой строке действительно стоит "Да" в колонке Бывший
  if(String(wsR.getRange(row, RC.former).getValue()||"").trim()!=="Да"){
    Logger.log("_toFormer: в строке "+row+" ("+name+") нет отметки Да, отмена");
    return;
  }
  
  // ИДЕМПОТЕНТНОСТЬ: если это имя уже в Бывших. не создаём дубль
  try{
    var lrF0=wsF.getLastRow();
    if(lrF0>=4){
      var exNames=wsF.getRange(4,2,lrF0-3,1).getValues();
      for(var ei=0;ei<exNames.length;ei++){
        if(String(exNames[ei][0]||"").trim()===String(name).trim()){
          Logger.log("_toFormer: "+name+" уже в Бывших. только удаляем из дебета");
          if(wsV){var vl0=wsV.getLastRow();for(var r0=vl0;r0>=3;r0--){if(String(wsV.getRange(r0,2).getValue()||"").trim()===String(name).trim()){wsV.deleteRow(r0);break;}}}
          wsR.deleteRow(row);_renumRes();
    try{syncVisitsOrder();}catch(e){}
          return;
        }
      }
    }
  }catch(dupE){Logger.log("_toFormer dup: "+dupE);}
  var paid=wsR.getRange(row, RC.paid).getValue()||0,dIn=wsR.getRange(row, RC.rest).getValue()||0,dExt=wsR.getRange(row, RC.renew).getValue()||0;
  var tariff=wsR.getRange(row, RC.tariff).getValue()||0,source=wsR.getRange(row, RC.source).getValue()||"";
  var dateIn=wsR.getRange(row, RC.date).getValue()||"",notes=wsR.getRange(row, RC.notes).getValue()||"";
  var chatId=String(wsR.getRange(row, RC.chat).getValue()||"");
  var dvSt=SpreadsheetApp.newDataValidation().requireValueInList(["Бывший","Активный"],true).setAllowInvalid(false).build();
  var nr=wsF.getLastRow()+1,bg=(nr-4)%2===0?G1:WHT;wsF.setRowHeight(nr,22);
  wsF.getRange(nr,1,1,11).setValues([[nr-3,name,paid||" ",dIn||" ",dExt||" ",tariff||" ",source||" ",dateIn||"",new Date(),notes||" ","Бывший"]])
     .setBackground(bg).setFontColor(GX).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
  wsF.getRange(nr,2).setHorizontalAlignment("left");wsF.getRange(nr,7).setHorizontalAlignment("left");
  wsF.getRange(nr,3,1,4).setNumberFormat("#,##0");wsF.getRange(nr,8,1,2).setNumberFormat("DD.MM.YYYY");
  wsF.getRange(nr,11).setDataValidation(dvSt).setHorizontalAlignment("center");B(wsF.getRange(nr,1,1,11));
  if(wsV){var vl=wsV.getLastRow();for(var r=vl;r>=3;r--){if(wsV.getRange(r,2).getValue()===name){wsV.deleteRow(r);break;}}}
  wsR.deleteRow(row);_renumRes();
  // Сообщение о прощании с кнопкой Kaspi
  if(chatId){
    var msg=getText("goodbye",{"имя":name.split(" ")[0]})||"👋 "+name.split(" ")[0]+", спасибо!";
    tgSendButton(chatId,msg,"💳 Погасить задолженность",KASPI_LINK);
  }
  // Уведомление админу
  ADMIN_IDS.forEach(function(id){tgSend(id,"📤 Резидент переведён в бывшие:\n👤 "+name+"\n💰 Долг: "+(Number(dIn)+Number(dExt)).toLocaleString()+" тг");});
}

// ── Возврат из бывших ─────────────────────────────────────────────────────
function _toActive(row){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsF=ss.getSheetByName("Бывшие резиденты"),wsR=ss.getSheetByName("BS - резиденты дебет"),wsV=ss.getSheetByName("BS - посещения");
  if(!wsF||!wsR)return;
  var name=wsF.getRange(row,2).getValue();if(!name||name===" ")return;
  function v(x){return(x===" "||x==="")?0:x;}
  var paid=v(wsF.getRange(row,3).getValue()),dIn=v(wsF.getRange(row,4).getValue()),dExt=v(wsF.getRange(row,5).getValue());
  var tariff=v(wsF.getRange(row,6).getValue()),source=v(wsF.getRange(row,7).getValue());
  var dateIn=wsF.getRange(row,8).getValue(),notes=v(wsF.getRange(row,10).getValue());
  var rl=wsR.getLastRow(),tr=3;
  for(var r=3;r<=rl+1;r++){if(!wsR.getRange(r, RC.name).getValue()){tr=r;break;}}
  var dvT=SpreadsheetApp.newDataValidation().requireValueInList(["0","100000","150000","200000","250000","300000","500000","1000000"],true).setAllowInvalid(true).build();
  var dvB=SpreadsheetApp.newDataValidation().requireValueInList(["Нет","Да"],true).setAllowInvalid(false).build();
  var bg=(tr-3)%2===0?WHT:G1;wsR.setRowHeight(tr,24);
  wsR.getRange(tr,1,1,14).setBackground(bg).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");B(wsR.getRange(tr,1,1,14));
  wsR.getRange(tr, RC.name).setValue(name).setFontWeight("bold").setHorizontalAlignment("left");
  if(paid)wsR.getRange(tr, RC.paid).setValue(paid).setNumberFormat("#,##0");
  if(dIn)wsR.getRange(tr, RC.rest).setValue(dIn).setNumberFormat("#,##0");
  if(dExt)wsR.getRange(tr, RC.renew).setValue(dExt).setNumberFormat("#,##0");
  wsR.getRange(tr, RC.fine).setValue(0).setNumberFormat("#,##0").setFontColor(RED);
  wsR.getRange(tr, RC.total).setValue(Number(dIn)+Number(dExt)).setNumberFormat("#,##0").setFontWeight("bold");
  if(tariff)wsR.getRange(tr, RC.tariff).setValue(tariff).setNumberFormat("#,##0").setDataValidation(dvT);
  if(dateIn)wsR.getRange(tr, RC.date).setValue(dateIn).setNumberFormat("DD.MM.YYYY").setDataValidation(dvDate());
  if(source)wsR.getRange(tr, RC.source).setValue(source).setHorizontalAlignment("left");
  wsR.getRange(tr, RC.former).setValue("Нет").setDataValidation(dvB);wsR.getRange(tr, RC.except).setValue("Нет").setDataValidation(dvB);
  if(notes)wsR.getRange(tr, RC.notes).setValue(notes).setHorizontalAlignment("left");
  wsR.getRange(tr, RC.chat).setBackground("#E8F5E9").setFontColor(GRN).setFontWeight("bold");
  _renumRes();
  if(wsV){
    var vl=wsV.getLastRow()+1;wsV.setRowHeight(vl,26);
    wsV.getRange(vl,1,1,11).setBackground((vl-3)%2===0?WHT:G1).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
    wsV.getRange(vl,1).setValue(vl-2).setHorizontalAlignment("center");
    wsV.getRange(vl,2).setValue(name).setFontWeight("bold").setHorizontalAlignment("left");
    wsV.getRange(vl,3).setValue("4").setHorizontalAlignment("center").setDataValidation(
      SpreadsheetApp.newDataValidation().requireValueInList(["3","4"],true).setAllowInvalid(false).build());
    wsV.getRange(vl,4,1,3).insertCheckboxes();wsV.getRange(vl,7).insertCheckboxes();
    wsV.getRange(vl,8).setValue("").setHorizontalAlignment("center").setFontColor("#0000CC").setFontWeight("bold");
    wsV.getRange(vl,10).setValue(0);wsV.getRange(vl,11).setValue(0);B(wsV.getRange(vl,1,1,11));
  }
  wsF.deleteRow(row);
  var fl=wsF.getLastRow();for(var r=4;r<=fl;r++)wsF.getRange(r,1).setValue(r-3);
  recalcResidents();
}

// ═══════════════════════════════════════════════════════════════════════════
// НОЧНАЯ ПРОВЕРКА ОТЧЁТОВ
// ═══════════════════════════════════════════════════════════════════════════

function _sendInstagramReminder(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(!wsR)return;
  var lr=wsR.getLastRow();
  if(lr<3)return;
  var resData=wsR.getRange(3, 2, lr-2, 20).getValues();
  resData.forEach(function(row){
    var name=row[0];if(!name)return;
    if(row[RI.except]==="Да"||row[RI.chat]==="Да")return;
    var chatId=String(row[RI.chat]||"");
    if(!chatId)return;
    var msg="📸 "+name.split(" ")[0]+", не забудь отметить @bsurgery в сторис!\n\n"+
      "В приложении это можно сделать в 2 клика. есть готовые шаблоны:\n"+
      "• Я в Business Surgery\n"+
      "• Приглашаю в клуб\n"+
      "• Мои результаты";
    tgSend(chatId,msg,{
      reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Создать сторис в BS",web_app:{url:getWebAppUrl()}}]]})
    });
  });
}




function manualReinstallWebhook(){
  // Ручная переустановка webhook через меню таблицы
  try{
    _reinstallWebhookKeepPending();
    Utilities.sleep(1500);
    var info=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",{muteHttpExceptions:true});
    var res=JSON.parse(info.getContentText()).result;
    var msg="✅ Webhook переустановлен\n\nURL: "+res.url+"\nPending: "+(res.pending_update_count||0)+"\nLast error: "+(res.last_error_message||"нет");
    SpreadsheetApp.getUi().alert(msg);
  }catch(e){
    SpreadsheetApp.getUi().alert("Ошибка: "+e.message);
  }
}

function restoreMissingMeetings(){
  // Восстановление встреч пропавших из Расписания
  var meetings = [
    {res:"Даулет Сайты", date:"16.05.2026", time:"13:00"},
    {res:"Мирас", date:"16.05.2026", time:"12:00"},
    {res:"Жасик Астана", date:"16.05.2026", time:"15:00"}
  ];
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");
  if(!ws){SpreadsheetApp.getUi().alert("Лист Расписание не найден");return;}
  var added=[],skipped=[];
  meetings.forEach(function(m){
    // Проверим. уже есть?
    var lr=ws.getLastRow();
    if(lr>=3){
      var data=ws.getRange(3, 1, lr-2, 21).getValues();
      var exists=false;
      for(var i=0;i<data.length;i++){
        if(String(data[i][1]).trim()===m.res){
          var d=data[i][2];
          if(d instanceof Date){
            var dStr=Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy");
            if(dStr===m.date){exists=true;break;}
          }
        }
      }
      if(exists){skipped.push(m.res+" "+m.date+" "+m.time);return;}
    }
    // Находим свободную строку
    var newRow=-1;
    for(var r=3;r<=ws.getLastRow();r++){
      if(!ws.getRange(r,2).getValue()){newRow=r;break;}
    }
    if(newRow<0)newRow=Math.max(ws.getLastRow()+1,3);
    // Парсим
    var dp=m.date.split(".");
    var year=parseInt(dp[2]),month=parseInt(dp[1]),day=parseInt(dp[0]);
    var dStr2=year+"-"+String(month).padStart(2,"0")+"-"+String(day).padStart(2,"0");
    var dt=Utilities.parseDate(dStr2+" 12:00:00","Asia/Almaty","yyyy-MM-dd HH:mm:ss");
    var tp=m.time.split(":");
    var hh=parseInt(tp[0]),mm=parseInt(tp[1])||0;
    var hhStr=String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
    var todayStr=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
    var evTimeValue=Utilities.parseDate(todayStr+" "+hhStr,"Asia/Almaty","yyyy-MM-dd HH:mm:ss");
    // Защита
    CacheService.getScriptCache().put("sched_meet_"+newRow+"_"+new Date().toDateString(),"1",3600);
    // Запись
    ws.getRange(newRow,1).setValue(newRow-2);
    ws.getRange(newRow,2).setValue(m.res);
    ws.getRange(newRow,3).setValue(dt).setNumberFormat("DD.MM.YYYY");
    ws.getRange(newRow,4).setValue(evTimeValue).setNumberFormat("HH:MM");
    SpreadsheetApp.flush();
    try{_autoCreateMeet(newRow);}catch(e){Logger.log("autoCreate: "+e);}
    added.push(m.res+" "+m.date+" "+m.time);
  });
  var msg="✅ Добавлено:\n"+added.join("\n");
  if(skipped.length)msg+="\n\n⏭ Уже существуют:\n"+skipped.join("\n");
  SpreadsheetApp.getUi().alert(msg);
}

function testDailyCheck(){
  // СИМУЛЯТОР ночной проверки. НЕ выставляет штрафы, только показывает что было бы
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsL=ss.getSheetByName("Лог отчётов");
  if(!wsR){SpreadsheetApp.getUi().alert("Нет листа Резиденты");return;}
  var lr=wsR.getLastRow();

  // Проверяем ВЧЕРАШНИЙ день
  var yesterdayDate=new Date();
  yesterdayDate.setDate(yesterdayDate.getDate()-1);
  var checkDate=Utilities.formatDate(yesterdayDate,"Asia/Almaty","dd.MM.yyyy");

  // Сегодняшний день. для проверки текущей ситуации
  var todayDate=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy");

  // Собираем кто сдал ВЧЕРА и СЕГОДНЯ
  var submittedYesterday={};
  var submittedToday={};
  if(wsL&&wsL.getLastRow()>1){
    var ll=wsL.getLastRow();
    var logData=wsL.getRange(2,1,ll-1,8).getValues();
    logData.forEach(function(row){
      if(!row[0]||!(row[0] instanceof Date))return;
      // Отчёт после полуночи не засчитывается ни за какой день
      if(String(row[7]||"").trim()==="Да")return;
      var dStr=Utilities.formatDate(new Date(row[0]),"Asia/Almaty","dd.MM.yyyy");
      var uid=String(row[5]||"");
      if(!uid)return;
      if(dStr===checkDate)submittedYesterday[uid]={name:row[2]||"",time:Utilities.formatDate(new Date(row[0]),"Asia/Almaty","HH:mm")};
      if(dStr===todayDate)submittedToday[uid]={name:row[2]||"",time:Utilities.formatDate(new Date(row[0]),"Asia/Almaty","HH:mm")};
    });
  }

  var resData=wsR.getRange(3, 2, lr-2, 20).getValues();
  var doneY=[],missY=[];
  var doneT=[],missT=[];
  resData.forEach(function(row){
    var name=row[0];if(!name)return;
    if(row[RI.except]==="Да"||row[RI.chat]==="Да")return;
    var chatId=String(row[RI.chat]||"");
    if(submittedYesterday[chatId]){
      doneY.push(name+" ("+submittedYesterday[chatId].time+")");
    } else {
      missY.push(name+" [ChatID: "+chatId+"]");
    }
    if(submittedToday[chatId]){
      doneT.push(name+" ("+submittedToday[chatId].time+")");
    } else {
      missT.push(name);
    }
  });

  var report="🧪 СИМУЛЯТОР ночной проверки\n\n";
  report+="📅 ВЧЕРА ("+checkDate+"):\n";
  report+="  ✅ Сдали ("+doneY.length+"):\n";
  doneY.forEach(function(n){report+="    • "+n+"\n";});
  report+="  ❌ Не сдали ("+missY.length+"). БУДУТ оштрафованы:\n";
  missY.forEach(function(n){report+="    • "+n+"\n";});
  report+="\n📅 СЕГОДНЯ ("+todayDate+"):\n";
  report+="  ✅ Уже сдали ("+doneT.length+")\n";
  report+="  ⏳ Ещё не сдали ("+missT.length+")\n";

  Logger.log(report);
  // Отправляем себе в Telegram
  tgSend(ADMIN_IDS[0],report);
  SpreadsheetApp.getUi().alert("Отчёт отправлен в Telegram. Также смотрите Logger.log");
}



function bsDayStr(d){
  // День как он выглядит в таблице, без пересчёта часовых поясов
  if(!(d instanceof Date) || isNaN(d)) return "";
  var dd = String(d.getDate()).padStart(2,"0");
  var mm = String(d.getMonth()+1).padStart(2,"0");
  return dd + "." + mm + "." + d.getFullYear();
}

function bsTZ(){
  // Часовой пояс скрипта: в нём записываются даты в листы.
  // Сравнивать нужно в нём же, иначе вечерние отчёты уезжают на следующий день
  try{ return Session.getScriptTimeZone() || "Asia/Almaty"; }catch(e){ return "Asia/Almaty"; }
}

function _normId(v){
  // Chat ID может прийти как 478757502, "478757502.0" или "478757502"
  return String(v == null ? "" : v).replace(/[^0-9]/g, "");
}

// ═══════════════════════════════════════════════════════════════
// БЕЗОПАСНЫЙ ПЕРЕСЧЁТ ДЕБЕТА
// Уникальное имя: его не может перебить старая копия скрипта в проекте
// ═══════════════════════════════════════════════════════════════
// ═══════════════════════════════════════════════════════════════
// ЗАЩИТА ОТ КОПИЙ ТАБЛИЦЫ
// Копия таблицы копирует и скрипт. В нём зашит адрес боевой таблицы,
// поэтому копия способна писать в рабочие данные. Здесь мы это пресекаем
// ═══════════════════════════════════════════════════════════════
function bsIsCopy(){
  try{
    var props = PropertiesService.getScriptProperties();
    if(props.getProperty("BS_IS_COPY") === "1") return true;

    var act = null;
    try{ act = SpreadsheetApp.getActiveSpreadsheet(); }catch(e){}
    if(act && act.getId() && act.getId() !== SS_ID){
      // Скрипт живёт в копии: запоминаем это навсегда и обезвреживаем
      props.setProperty("BS_IS_COPY", "1");
      try{
        ScriptApp.getProjectTriggers().forEach(function(t){ ScriptApp.deleteTrigger(t); });
      }catch(e){}
      try{
        act.toast("Это резервная копия. Автоматика отключена, рабочие данные не тронуты",
          "Копия таблицы", 30);
      }catch(e){}
      return true;
    }
  }catch(e){}
  return false;
}

function bsArgSep(){
  // В русской локали аргументы формул разделяются точкой с запятой, в английской запятой.
  // Определяем на пробной формуле и запоминаем
  try{
    var props = PropertiesService.getScriptProperties();
    var saved = props.getProperty("BS_ARG_SEP");
    if(saved === "," || saved === ";") return saved;

    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("BS - резиденты дебет");
    if(!ws) return ";";
    var probe = ws.getRange(1, 26);           // дальняя пустая ячейка
    var keep = probe.getFormula() || probe.getValue();
    probe.setFormula("=SUM(1,2)");
    SpreadsheetApp.flush();
    var v = probe.getValue();
    var sep = (v === 3 || v === "3") ? "," : ";";
    probe.clearContent();
    if(keep) probe.setValue(keep);
    props.setProperty("BS_ARG_SEP", sep);
    return sep;
  }catch(e){ return ";"; }
}

// ═══════════════════════════════════════════════════════════════════════════
// ПЛАТФОРМА. список резидентов для входа, кнопка в боте
// ═══════════════════════════════════════════════════════════════════════════
// Сервер платформы. Этот адрес работает всегда, даже после подключения своего домена
var PLATFORM_API = "https://bs-platform-production.up.railway.app";

// Адрес, который видят резиденты. Свой домен: лист «Настройки», ключ platform_url
function bsPlatformUrl(){
  var u = "";
  try{ u = String(getText("platform_url", null) || "").trim(); }catch(e){}
  return /^https:\/\//.test(u) ? u.replace(/\/+$/, "") : PLATFORM_API;
}

function bsResidentNameByChat(cid){
  try{
    var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws || ws.getLastRow() < 3) return "";
    var d = ws.getRange(3, 2, ws.getLastRow()-2, 20).getValues();
    for(var i = 0; i < d.length; i++){
      if(d[i][RI.name] && d[i][RI.former] !== "Да" && _normId(d[i][RI.chat]) === _normId(cid)) return String(d[i][RI.name]).trim();
    }
  }catch(e){}
  return "";
}

// ═══ ПЕРЕНОС ДАННЫХ КЛУБА НА СЕРВЕР ═══
// Листы уходят как есть (то, что видно в ячейках), с подписью токеном бота.
// Сервер раскладывает их по своим таблицам и сверяет свои расчёты долгов
// и PL с цифрами таблицы. Пока таблица главная, перенос можно повторять
// Разделы приложения (колесо, лиды, задачи, СММ, контент, чек-листы): с v33 сервер
// собирает их сам из этих листов и свойств скрипта
var BS_APP_SHEETS = ["Колесо","CRM Лиды","Стоимость проблем","Задачи резидентов","СММ план",
                     "Контент","Лид-магниты","История лид-магнитов"];
var BS_CLUB_SHEETS = ["BS - резиденты дебет","Учет ДДС","PL","Штрафы","Расписание",
                      "Лог отчётов","Лог встреч","Настройки","Бывшие резиденты","Профили"].concat(BS_APP_SHEETS);

// Свойства скрипта, нужные разделам приложения: названия осей колеса резидентов
// и очередь чек-листов. Уходят строками [ключ, значение] под именем "_props"
function bsAppProps(){
  var all = PropertiesService.getScriptProperties().getProperties(), rows = [];
  Object.keys(all).sort().forEach(function(k){
    if(k.indexOf("WHEEL_AXES_") === 0 || k === "USEFUL_CL_IDX") rows.push([k, String(all[k])]);
  });
  return rows;
}

function bsClubImport(dry){
  var ss = SpreadsheetApp.openById(SS_ID);
  var sheets = {};
  BS_CLUB_SHEETS.forEach(function(n){
    var ws = ss.getSheetByName(n);
    if(ws) sheets[n] = ws.getDataRange().getDisplayValues();
  });
  try{ sheets["_props"] = bsAppProps(); }catch(e){ Logger.log("app props: " + e); }
  var body = JSON.stringify({ts: Math.floor(Date.now() / 1000), sheets: sheets});
  var sig = Utilities.computeHmacSha256Signature(body, BOT_TOKEN, Utilities.Charset.UTF_8).map(function(b){
    var h = (b < 0 ? b + 256 : b).toString(16); return h.length < 2 ? "0" + h : h;
  }).join("");
  var resp = UrlFetchApp.fetch(PLATFORM_API + "/api/v1/club/import" + (dry ? "?dry=1" : ""), {
    method: "post", contentType: "application/json", payload: body,
    headers: {"X-BS-Signature": sig}, muteHttpExceptions: true
  });
  var code = resp.getResponseCode(), j = {};
  try{ j = JSON.parse(resp.getContentText()); }catch(e){}
  return {code: code, j: j, raw: resp.getContentText()};
}

function bsClubImportReport(r, dry){
  if(r.code !== 200){
    return "Сервер не принял данные: " + r.code + "\n" + String(r.j.detail || r.j.error || r.raw).slice(0, 400);
  }
  var j = r.j, c = j.counts || {};
  var lines = [];
  lines.push(dry ? "ПРОВЕРКА ПЕРЕНОСА (ничего не записано)" : "ДАННЫЕ ПЕРЕНЕСЕНЫ НА СЕРВЕР");
  lines.push("");
  lines.push("Резиденты: " + (c.residents||0) + ", бывшие: " + (c.former||0));
  lines.push("ДДС: " + (c.payments||0) + " строк, штрафы: " + (c.fines||0) + ", встречи: " + (c.meetings||0));
  lines.push("Отчёты: " + (c.reports||0) + ", лог встреч: " + (c.meetingLog||0) + ", тексты бота: " + (c.settings||0));
  lines.push("");
  var dm = j.debetMismatch || [], pm = j.plMismatch || [];
  lines.push("Сверено чисел: " + (j.checked||0));
  if(!dm.length && !pm.length){
    lines.push("✅ Долги и PL, посчитанные сервером, совпадают с таблицей");
  } else {
    lines.push("❌ Расхождений: " + (dm.length + pm.length));
    dm.concat(pm).slice(0, 15).forEach(function(m){
      lines.push("• " + m.where + ": таблица " + m.sheet + ", сервер " + m.server);
    });
  }
  (j.unplaced || []).forEach(function(u){
    if(u === null) return;
    lines.push("Без статьи: " + u.date + " " + u.amount + " «" + (u.category||"пусто") + "» → " + (u.placedIn || "не учтено в PL"));
  });
  if((j.warnings || []).length){
    lines.push("");
    lines.push("Строки, которые не прочитались (" + j.warnings.length + "):");
    j.warnings.slice(0, 10).forEach(function(w){ lines.push("• " + w); });
  }
  if(!dry && j.platformResidents) lines.push("\nДоступ к платформе: " + j.platformResidents + " резидентов");
  return lines.join("\n");
}

// Раз в час данные клуба уходят на сервер, чтобы он работал с сегодняшними цифрами.
// Пока таблица главная, сервер каждый раз заменяет свои данные на её
function bsClubImportHourly(){
  if(bsIsCopy()) return;
  var r = bsClubImport(false);
  if(r.code === 200 || r.code === 409) return r.code;   // 409: сервер уже главный, таблица не нужна
  Logger.log("clubImport: " + r.code + " " + String(r.raw).slice(0, 200));
  var c = CacheService.getScriptCache();
  if(!c.get("clubImportWarn")){
    c.put("clubImportWarn", "1", 22*3600);
    try{ bsSysNote("⚠️ Данные клуба не ушли на сервер: " + r.code + " " + String(r.j.detail || r.j.error || r.raw).slice(0, 200)); }catch(e){}
  }
  return r.code;
}

function bsClubImportCheck(){
  var t = bsClubImportReport(bsClubImport(true), true);
  try{ SpreadsheetApp.getUi().alert(t); }catch(e){}
  return t;
}

function bsClubImportNow(){
  var ui = null; try{ ui = SpreadsheetApp.getUi(); }catch(e){}
  if(ui){
    var a = ui.alert("Перенести данные на сервер?",
      "Резиденты, ДДС, штрафы, расписание и логи уйдут на сервер. Таблица при этом не меняется и остаётся главной, пока бот не переедет.",
      ui.ButtonSet.YES_NO);
    if(a !== ui.Button.YES) return;
  }
  var t = bsClubImportReport(bsClubImport(false), false);
  if(ui) ui.alert(t);
  try{ tgSendAdmins(t); }catch(e){}
  return t;
}

// ═══ САМООБНОВЛЕНИЕ СКРИПТА ═══
// Раз в час (bsHourly) и из меню «Обновить скрипт» таблица спрашивает сервер,
// какая версия скрипта последняя. Если новее своей, сама заменяет код проекта
// через Apps Script API, создаёт версию и переводит на неё веб-приложение
// (то же развёртывание, адрес /exec не меняется). Код больше не вставляют руками.
// Нужно один раз: включить «Google Apps Script API» в script.google.com/home/usersettings.
// Сбой ничего не ломает: остаётся старый код, владельцу одно сообщение в день.
var BS_SU_API = "https://script.googleapis.com/v1/projects/";
// В коде на сервере токена бота нет: на его месте метка, скрипт вставляет свой токен
// (собрана из частей, чтобы сама строка не заменялась при обновлении)
var BS_SU_TOKEN_MARK = "__BS_BOT_" + "TOKEN__";

// a новее b: "2026-10-02-32" > "2026-10-02-4" > "2026-10-01-1"
function bsVersionNewer(a, b){
  var pa = String(a||"").split(/[^0-9]+/), pb = String(b||"").split(/[^0-9]+/);
  for(var i = 0; i < Math.max(pa.length, pb.length); i++){
    var x = parseInt(pa[i]||"0", 10) || 0, y = parseInt(pb[i]||"0", 10) || 0;
    if(x !== y) return x > y;
  }
  return false;
}

function bsHmacHex(s){
  return Utilities.computeHmacSha256Signature(s, BOT_TOKEN, Utilities.Charset.UTF_8).map(function(b){
    var h = (b < 0 ? b + 256 : b).toString(16); return h.length < 2 ? "0" + h : h;
  }).join("");
}

// Подписанный GET: подпись = HMAC("GET " + путь + "?" + запрос, токен бота)
function bsSignedGet(path, params){
  params = params || {};
  params.ts = Math.floor(Date.now() / 1000);
  var q = Object.keys(params).sort().map(function(k){
    return encodeURIComponent(k) + "=" + encodeURIComponent(String(params[k]));
  }).join("&");
  var resp = UrlFetchApp.fetch(PLATFORM_API + path + "?" + q, {
    method: "get", headers: {"X-BS-Signature": bsHmacHex("GET " + path + "?" + q)}, muteHttpExceptions: true
  });
  var j = {};
  try{ j = JSON.parse(resp.getContentText()); }catch(e){}
  return {code: resp.getResponseCode(), j: j, raw: resp.getContentText()};
}

// Вызов Apps Script API от имени владельца
function _bsSuApi(method, path, body){
  var p = {method: method, muteHttpExceptions: true,
           headers: {Authorization: "Bearer " + ScriptApp.getOAuthToken()}};
  if(body !== undefined){ p.contentType = "application/json"; p.payload = JSON.stringify(body); }
  var r = UrlFetchApp.fetch(BS_SU_API + ScriptApp.getScriptId() + path, p);
  var code = r.getResponseCode(), j = {};
  try{ j = JSON.parse(r.getContentText()); }catch(e){}
  if(code < 200 || code >= 300){
    var msg = (j.error && j.error.message) || String(r.getContentText()).slice(0, 300);
    if(code === 403 && /Apps Script API/i.test(msg))
      msg = "не включён Google Apps Script API (script.google.com/home/usersettings)";
    throw new Error("Apps Script API " + method.toUpperCase() + " " + path + ": " + code + " " + msg);
  }
  return j;
}

// Манифест: из новой версии, но доступ веб-приложения и часовой пояс — как сейчас,
// разрешения и расширенные сервисы — объединение. extra: разрешения, которых ещё нет
function _bsSuManifest(newSrc, curSrc){
  var n = JSON.parse(newSrc), c = {};
  try{ c = JSON.parse(curSrc || "{}"); }catch(e){ c = {}; }
  if(c.webapp) n.webapp = c.webapp;
  if(c.timeZone) n.timeZone = c.timeZone;
  var cs = ((c.dependencies||{}).enabledAdvancedServices) || [];
  if(cs.length){
    n.dependencies = n.dependencies || {};
    var ns = n.dependencies.enabledAdvancedServices = n.dependencies.enabledAdvancedServices || [];
    cs.forEach(function(s){
      if(!ns.some(function(x){ return x.userSymbol === s.userSymbol; })) ns.push(s);
    });
  }
  var extra = [];
  if(c.oauthScopes && n.oauthScopes){
    extra = n.oauthScopes.filter(function(s){ return c.oauthScopes.indexOf(s) < 0; });
    c.oauthScopes.forEach(function(s){ if(n.oauthScopes.indexOf(s) < 0) n.oauthScopes.push(s); });
  }
  return {source: JSON.stringify(n, null, 2), extra: extra};
}

// Развёртывание веб-приложения, на которое идут сообщения бота и приложение
function _bsSuDeployment(target){
  var id = (String(target||"").match(/\/s\/([^\/]+)\/exec/) || [])[1] || "";
  var tok = "", guard = 0;
  do{
    var j = _bsSuApi("get", "/deployments?pageSize=50" + (tok ? "&pageToken=" + encodeURIComponent(tok) : ""));
    var list = j.deployments || [];
    for(var i = 0; i < list.length; i++){
      var d = list[i];
      if(id && d.deploymentId === id) return d;
      var eps = d.entryPoints || [];
      for(var k = 0; k < eps.length; k++){
        var u = eps[k].webApp && eps[k].webApp.url;
        if(eps[k].entryPointType === "WEB_APP" && u && target && u.split("?")[0] === String(target).split("?")[0]) return d;
      }
    }
    tok = j.nextPageToken || "";
  }while(tok && ++guard < 10);
  return null;
}

// Новая версия проекта из текущего кода и перевод на неё веб-приложения
function _bsSuDeploy(target, version){
  var dep = _bsSuDeployment(target);
  if(!dep) throw new Error("не найдено развёртывание веб-приложения " + target);
  var v = _bsSuApi("post", "/versions", {description: "BS " + version + " (автообновление)"});
  if(!v.versionNumber) throw new Error("Apps Script API не вернул номер версии");
  _bsSuApi("put", "/deployments/" + dep.deploymentId, {deploymentConfig: {
    scriptId: ScriptApp.getScriptId(), versionNumber: v.versionNumber,
    manifestFileName: "appsscript", description: "BS " + version
  }});
  return {deploymentId: dep.deploymentId, versionNumber: v.versionNumber};
}

// Версия, которую сейчас отдаёт веб-приложение ("" — не ответило)
function _bsSuLiveVersion(target){
  try{
    var r = UrlFetchApp.fetch(String(target).split("?")[0] + "?action=bsVersion", {muteHttpExceptions: true, followRedirects: true});
    return JSON.parse(r.getContentText()).version || "";
  }catch(e){ return ""; }
}

function _bsSuRun(manual){
  var latest = bsSignedGet("/api/v1/script/latest", {version: BS_VERSION});
  if(latest.code !== 200 || !latest.j.version)
    throw new Error("сервер не ответил о версии: " + latest.code + " " + String(latest.j.error || latest.raw).slice(0, 200));
  var L = latest.j;
  var target = L.relay || WEBHOOK_URL;

  if(!bsVersionNewer(L.version, BS_VERSION)){
    // Код уже новый, но веб-приложение могло остаться на старой версии (сбой на прошлом шаге)
    var live = _bsSuLiveVersion(target);
    if(live && bsVersionNewer(BS_VERSION, live)){
      var dp = _bsSuDeploy(target, BS_VERSION);
      bsSignedPost("/api/v1/script/updated", {version: BS_VERSION, from: live,
        deploymentId: dp.deploymentId, versionNumber: dp.versionNumber});
      return {ok: true, updated: true, deployOnly: true, version: BS_VERSION, from: live};
    }
    return {ok: true, upToDate: true, version: BS_VERSION, latest: L.version};
  }

  var files = L.files || [];
  if(!files.length) throw new Error("сервер не прислал файлы версии " + L.version);
  var cur = _bsSuApi("get", "/content");
  var have = cur.files || [];
  var main = null, manifest = null;
  have.forEach(function(f){
    if(f.type === "JSON" && f.name === "appsscript") manifest = f;
    if(f.type === "SERVER_JS" && !main && String(f.source||"").indexOf("var BS_VERSION") >= 0) main = f;
  });
  var out = have.map(function(f){ return {name: f.name, type: f.type, source: f.source}; });
  var extra = [];
  files.forEach(function(nf){
    var source = String(nf.source || "");
    var name = nf.name;
    if(nf.type === "SERVER_JS"){
      source = source.split('"' + BS_SU_TOKEN_MARK + '"').join(JSON.stringify(BOT_TOKEN));
      if(source.indexOf(BS_SU_TOKEN_MARK) >= 0) throw new Error("в новой версии осталась метка токена");
      if(source.indexOf("var BS_VERSION") >= 0){
        if(source.indexOf('var BS_VERSION = "' + L.version + '"') < 0 || source.indexOf("function doGet") < 0)
          throw new Error("файл новой версии не похож на скрипт BS " + L.version);
        if(main) name = main.name;               // «Код», как назван у владельца
      }
    }
    if(nf.type === "JSON" && nf.name === "appsscript"){
      var m = _bsSuManifest(source, manifest && manifest.source);
      source = m.source; extra = m.extra;
    }
    var hit = false;
    out.forEach(function(f){ if(f.name === name && f.type === nf.type){ f.source = source; hit = true; } });
    if(!hit) out.push({name: name, type: nf.type, source: source});
  });
  if(extra.length && !manual)
    throw new Error("новой версии нужны разрешения (" + extra.join(", ") + "): меню BS → «Обновить скрипт»");

  _bsSuApi("put", "/content", {scriptId: ScriptApp.getScriptId(), files: out});
  var dp2 = _bsSuDeploy(target, L.version);
  try{
    bsSignedPost("/api/v1/script/updated", {version: L.version, from: BS_VERSION,
      deploymentId: dp2.deploymentId, versionNumber: dp2.versionNumber});
  }catch(e){ Logger.log("selfUpdate report: " + e); }
  return {ok: true, updated: true, version: L.version, from: BS_VERSION, deploymentId: dp2.deploymentId,
          versionNumber: dp2.versionNumber, needsAuth: extra};
}

// Владельцу — не чаще раза в сутки
function _bsSuFail(err){
  Logger.log("selfUpdate: " + err);
  try{ bsSysNote("Обновление скрипта не удалось: " + err); }catch(e){}
  try{ bsSignedPost("/api/v1/script/updated", {version: BS_VERSION, error: String(err).slice(0, 500)}); }catch(e){}
  try{
    var p = PropertiesService.getScriptProperties();
    var day = Utilities.formatDate(new Date(), "Asia/Almaty", "yyyy-MM-dd");
    if(p.getProperty("BS_SU_NOTE_DAY") === day) return;
    p.setProperty("BS_SU_NOTE_DAY", day);
    tgSend(ADMIN_ID, "⚠️ Скрипт BS не обновился сам: " + String(err).slice(0, 400) +
      "\n\nБот работает на версии " + BS_VERSION + ". Повтор через час, вручную: меню BS → «Обновить скрипт».");
  }catch(e){}
}

function bsSelfUpdate(manual){
  if(bsIsCopy()) return {ok: false, error: "копия"};
  manual = manual === true;
  var lock = LockService.getScriptLock();
  if(!lock.tryLock(10000)) return {ok: false, busy: true};
  var res;
  try{ res = _bsSuRun(manual); }
  catch(e){ res = {ok: false, error: String(e && e.message || e)}; }
  finally{ try{ lock.releaseLock(); }catch(e){} }
  if(!res.ok) _bsSuFail(res.error);
  else if(res.updated){
    try{ bsSysNote("Скрипт обновлён: " + res.from + " → " + res.version); }catch(e){}
    try{ tgSend(ADMIN_ID, "✅ Скрипт BS обновлён сам: " + res.from + " → " + res.version +
      (res.needsAuth && res.needsAuth.length ? "\nНужно разрешение: меню BS → «Обновить скрипт» → «Разрешить»." : "")); }catch(e){}
  }
  return res;
}

function bsSelfUpdateMenu(){
  var r = bsSelfUpdate(true);
  var t = r.busy ? "Скрипт занят другой задачей, попробуйте через минуту."
    : !r.ok ? "Не обновилось: " + r.error + "\n\nСейчас работает версия " + BS_VERSION + "."
    : r.upToDate ? "Установлена последняя версия " + BS_VERSION + "."
    : "Обновлено: " + r.from + " → " + r.version + ". Адрес веб-приложения прежний." +
      (r.needsAuth && r.needsAuth.length ? "\n\nНовой версии нужны разрешения: запустите «Обновить скрипт» ещё раз и нажмите «Разрешить»." : "");
  return _alert(t);
}

// Подписанный запрос к серверу: тело подписывается токеном бота
function bsSignedPost(path, obj){
  obj = obj || {};
  obj.ts = Math.floor(Date.now() / 1000);
  var body = JSON.stringify(obj);
  var sig = Utilities.computeHmacSha256Signature(body, BOT_TOKEN, Utilities.Charset.UTF_8).map(function(b){
    var h = (b < 0 ? b + 256 : b).toString(16); return h.length < 2 ? "0" + h : h;
  }).join("");
  var resp = UrlFetchApp.fetch(PLATFORM_API + path, {
    method: "post", contentType: "application/json", payload: body,
    headers: {"X-BS-Signature": sig}, muteHttpExceptions: true
  });
  var j = {};
  try{ j = JSON.parse(resp.getContentText()); }catch(e){}
  return {code: resp.getResponseCode(), j: j, raw: resp.getContentText()};
}

// ═══ БОТ ЧЕРЕЗ СЕРВЕР ═══
// Telegram шлёт сообщения на сервер платформы. Сервер сразу отвечает Telegram,
// хранит каждое сообщение и передаёт его сюда, в doPost. Бот работает как раньше,
// но Telegram больше не ждёт медленную таблицу, а сообщения не теряются,
// пока таблица занята или недоступна. Откат: «Переподключить бота напрямую»
function bsBotViaServer(silent){
  if(bsIsCopy()) return;
  if(silent === true){
    var rs = bsSignedPost("/api/v1/bot/connect", {relay: WEBHOOK_URL, version: BS_VERSION});
    return rs.code === 200;
  }
  PropertiesService.getScriptProperties().deleteProperty("BS_FORCE_SHEET");
  var ui = null; try{ ui = SpreadsheetApp.getUi(); }catch(e){}
  if(ui){
    var a = ui.alert("Принимать сообщения бота через сервер?",
      "Telegram будет слать сообщения на сервер платформы, а сервер сразу передаст их сюда.\n\n" +
      "Бот, отчёты, штрафы и команды работают как раньше. Если что-то пойдёт не так: " +
      "меню BS → «Переподключить бота напрямую (откат)».",
      ui.ButtonSet.YES_NO);
    if(a !== ui.Button.YES) return;
  }
  var relay = WEBHOOK_URL;
  var r = bsSignedPost("/api/v1/bot/connect", {relay: relay, version: BS_VERSION});
  var t;
  if(r.code === 200){
    var w = r.j.webhook || {};
    t = "✅ Бот принимает сообщения через сервер\n\n" +
        "Telegram → " + (w.url || "сервер") + "\n" +
        "Сервер → таблица: версия " + (r.j.version || "?") + "\n" +
        "В очереди Telegram: " + (w.pending_update_count || 0) + "\n\n" +
        "Проверка: напишите боту /start. Ответ придёт как обычно.";
  } else if(r.code === 409){
    t = "Сервер не подключил бота: " + (r.j.detail || r.raw) + "\n\n" +
        "Сначала: Развернуть → Управление развёртываниями → карандаш → Версия: Новая → Развернуть.";
  } else {
    t = "Сервер не подключил бота (" + r.code + "): " + String(r.j.detail || r.j.error || r.raw).slice(0, 300) +
        "\n\nБот продолжает работать напрямую, ничего не изменилось.";
  }
  if(ui) ui.alert(t);
  try{ tgSendAdmins(t); }catch(e){}
  return t;
}

// Как идут сообщения через сервер: очередь, ошибки, версия скрипта за сервером
function bsServerBotStatus(){
  try{
    var r = bsSignedPost("/api/v1/bot/status", {});
    return r.code === 200 ? r.j : null;
  }catch(e){ return null; }
}

// ═══ ЧТО СЕРВЕР ДЕЛАЕТ САМ ═══
// Решает сервер: он включает функции по одной, когда проверил их на живых данных.
// Таблица раз в 5 минут спрашивает сервер, что он делает сам, и не делает этого же.
// Код скрипта для этого менять больше не нужно.
//   report_feedback   огонёк на отчёт, 👍 на кружочек, сообщения о коротком отчёте и т.п.
//   evening_reminder  напоминание 22:00
//   daily_check       ночная проверка отчётов и штрафы (штрафы сервер пишет в лист «Штрафы»)
//   meeting_reminders напоминания о встречах за 3 дня, за день и за час
//   bot_private       личные сообщения и кнопки бота
//   report_log        запись отчётов
// master = "server": данные клуба ведутся на сервере, таблица только копия ДДС и PL.
var BS_FEATURE_NAMES = {
  report_feedback:"ответы на отчёты", evening_reminder:"напоминание 22:00",
  daily_check:"ночная проверка и штрафы", meeting_reminders:"напоминания о встречах",
  bot_private:"личные сообщения бота", report_log:"запись отчётов"
};

function bsServerControl(){
  var props = PropertiesService.getScriptProperties();
  if(props.getProperty("BS_FORCE_SHEET") === "1") return {features: [], master: "sheet", forced: true};
  var cache = CacheService.getScriptCache();
  var v = cache.get("srvCtl");
  if(v){ try{ return JSON.parse(v); }catch(e){} }
  var ctl = null;
  try{
    var r = bsSignedPost("/api/v1/bot/control", {version: BS_VERSION});
    if(r.code === 200 && r.j && r.j.features) ctl = {features: r.j.features, master: r.j.master || "sheet", at: Date.now()};
  }catch(e){}
  if(ctl){
    props.setProperty("BS_CTL_LAST", JSON.stringify(ctl));
    cache.put("srvCtl", JSON.stringify(ctl), 300);
    return ctl;
  }
  // Сервер не ответил: сутки верим последнему ответу, потом всё делает таблица
  var last = null;
  try{ last = JSON.parse(props.getProperty("BS_CTL_LAST") || "null"); }catch(e){}
  if(last && Date.now() - last.at < 24*3600*1000){
    cache.put("srvCtl", JSON.stringify(last), 120);
    return last;
  }
  return {features: [], master: "sheet"};
}

function bsServerOwns(f){
  try{ return (bsServerControl().features || []).indexOf(f) >= 0; }catch(e){ return false; }
}

function bsServerIsMaster(){
  try{ return bsServerControl().master === "server"; }catch(e){ return false; }
}

function bsServerShow(){
  try{ CacheService.getScriptCache().remove("srvCtl"); }catch(e){}
  var c = bsServerControl();
  var t = c.forced ? "Сервер отключён вручную: всё делает таблица.\nВернуть: меню BS → «Бот: снова доверить серверу»."
    : ("Сервер делает сам: " + ((c.features || []).length ? c.features.map(function(f){ return BS_FEATURE_NAMES[f] || f; }).join(", ") : "пока ничего") +
       "\nДанные клуба ведёт: " + (c.master === "server" ? "сервер (таблица — копия ДДС и PL)" : "таблица"));
  try{ SpreadsheetApp.getUi().alert(t); }catch(e){}
  return t;
}

// Действия приложения, которые пишут данные клуба в таблицу
var BS_CLUB_WRITE_ACTIONS = ["addFine","addSchedule","addPayment","confirmMeeting","setPartner","setMeetings",
  "renewMeetings","addResident","deleteSchedule","updateMeeting","addOfflineGroup","updateFine","deleteFine",
  "markAttendance","approveResident","convertToResident","saveProfit","setResidentField"];

// Приложение в Telegram ходит через сервер: сервер проверяет, кто зашёл, и подписывает
// запрос. Когда сервер включит app_gateway, таблица отвечает только на подписанные запросы,
// и никто не сможет вызвать её от чужого имени
function bsAppSigOk(p, action){
  try{
    var ts = String(p._srv_ts || ""), sig = String(p._srv_sig || "");
    if(!ts || !sig) return false;
    if(Math.abs(Date.now()/1000 - Number(ts)) > 600) return false;
    var want = Utilities.computeHmacSha256Signature("app|" + action + "|" + String(p.chatId || "") + "|" + ts, BOT_TOKEN, Utilities.Charset.UTF_8)
      .map(function(b){ var h = (b < 0 ? b + 256 : b).toString(16); return h.length < 2 ? "0" + h : h; }).join("");
    return want === sig;
  }catch(e){ return false; }
}

function bsAppRefused(){
  return ContentService.createTextOutput(JSON.stringify({error: "Закройте приложение и откройте его снова из бота."}))
    .setMimeType(ContentService.MimeType.JSON);
}

// Запись данных клуба из бота в таблицу, когда их ведёт сервер, запрещена
function bsMasterRefuse(cid){
  if(!bsServerIsMaster()) return false;
  try{ tgSend(cid, "Данные клуба теперь ведутся на платформе. Внесите это там: " + bsPlatformUrl()); }catch(e){}
  return true;
}

// Бот сам подключается к серверу: раз в час проверяем, что Telegram шлёт сообщения
// на сервер, и если нет, подключаем. Не трогаем, если бота аварийно вернули таблице
function bsAutoServer(){
  if(bsIsCopy()) return;
  if(PropertiesService.getScriptProperties().getProperty("BS_FORCE_SHEET") === "1") return;
  var st = bsServerBotStatus();
  if(!st || st.viaServer) return;           // сервер не ответил или уже всё в порядке
  var ok = false;
  try{ ok = bsBotViaServer(true); }catch(e){ Logger.log("autoServer: " + e); }
  Logger.log("autoServer: " + (ok ? "подключён" : "не удалось"));
  if(ok){ try{ bsSysNote("✅ Бот сам подключился к серверу: сообщения идут через сервер, таблица работает как раньше."); }catch(e){} }
  else {
    var c = CacheService.getScriptCache();
    if(!c.get("autoSrvWarn")){
      c.put("autoSrvWarn", "1", 22*3600);
      try{ bsSysNote("⚠️ Бот не смог подключиться к серверу. Скорее всего не выпущена новая версия: Развернуть → Управление развёртываниями → карандаш → Версия: Новая → Развернуть."); }catch(e){}
    }
  }
}

// Аварийно: таблица снова делает всё сама, сервер перестаёт
function bsForceSheet(silent){
  var props = PropertiesService.getScriptProperties();
  props.setProperty("BS_FORCE_SHEET", "1");
  try{ CacheService.getScriptCache().remove("srvCtl"); }catch(e){}
  var r = null;
  try{ r = bsSignedPost("/api/v1/bot/features", {on: []}); }catch(e){}
  var t = "Всё снова делает таблица" + ((r && r.code === 200) ? ", сервер остановлен" : " (сервер не ответил, но таблица уже работает сама)");
  if(silent !== true){ try{ SpreadsheetApp.getUi().alert(t); }catch(e){} try{ tgSendAdmins("⚠️ " + t); }catch(e){} }
  return t;
}

function bsTrustServer(){
  PropertiesService.getScriptProperties().deleteProperty("BS_FORCE_SHEET");
  try{ CacheService.getScriptCache().remove("srvCtl"); }catch(e){}
  try{ bsSignedPost("/api/v1/bot/features", {clear: true}); }catch(e){}
  try{ bsAutoServer(); }catch(e){}
  return bsServerShow();
}

// Совместимость с v27: откат через «Переподключить бота напрямую»
function bsServerGiveBack(silent){ return bsForceSheet(silent); }

// ═══ ЗАПИСЬ С СЕРВЕРА В ТАБЛИЦУ ═══
// Пока таблица главная, сервер пишет в неё через этот вход (подпись токеном бота)
function bsServerWrite(u){
  var out = ContentService.createTextOutput();
  out.setMimeType(ContentService.MimeType.JSON);
  var res;
  try{
    var data = String(u.data || "");
    var sig = Utilities.computeHmacSha256Signature(data, BOT_TOKEN, Utilities.Charset.UTF_8).map(function(b){
      var h = (b < 0 ? b + 256 : b).toString(16); return h.length < 2 ? "0" + h : h;
    }).join("");
    if(sig !== String(u.sig || "")) throw new Error("bad_signature");
    var d = JSON.parse(data);
    if(Math.abs(Date.now()/1000 - (d.ts || 0)) > 3600) throw new Error("stale_request");
    if(d.op === "ping") res = {ok: true, version: BS_VERSION};
    else if(d.op === "addFines") res = {ok: true, added: bsAddFines(d.fines || [])};
    else throw new Error("unknown op " + d.op);
  }catch(e){ res = {ok: false, error: String(e && e.message || e)}; }
  out.setContent(JSON.stringify(res));
  return out;
}

// Добавляет штрафы в лист «Штрафы». Дубль (то же имя, тип и дата) не пишется
function bsAddFines(list){
  var ss = SpreadsheetApp.openById(SS_ID);
  var ws = ss.getSheetByName("Штрафы");
  if(!ws) throw new Error("нет листа Штрафы");
  var lock = LockService.getScriptLock();
  try{ lock.waitLock(20000); }catch(e){}
  var added = [];
  try{
    var have = {};
    if(ws.getLastRow() >= 3){
      ws.getRange(3, 2, ws.getLastRow()-2, 4).getValues().forEach(function(r){
        var d = r[3] instanceof Date ? bsDayStr(r[3]) : String(r[3]).trim();
        have[String(r[0]).trim() + "|" + String(r[1]).trim() + "|" + d] = 1;
      });
    }
    list.forEach(function(f){
      var key = String(f.name).trim() + "|" + String(f.type).trim() + "|" + f.date;
      if(have[key]) return;
      have[key] = 1;
      var p = String(f.date).split(".");
      var fr = _findFreeRow(ws, 3, 2);
      ws.getRange(fr, 1, 1, 6).setValues([[fr-2, f.name, f.type, Number(f.amount)||0,
        new Date(Number(p[2]), Number(p[1])-1, Number(p[0]), 12, 0, 0), "Не оплатил"]]);
      ws.getRange(fr, 4).setNumberFormat("#,##0");
      ws.getRange(fr, 5).setNumberFormat("DD.MM.YYYY");
      try{ _applyRowStyle(ws, fr, 6, (fr-3)%2===1); }catch(e){}
      added.push(f.name);
    });
    if(added.length){
      try{ _renumFines(); }catch(e){}
      try{ _fixFinesHeader(); }catch(e){}
      try{ recalcResidents(); }catch(e){}
      try{ _invalidateBSCaches(); }catch(e){}
    }
  } finally { try{ lock.releaseLock(); }catch(e){} }
  return added;
}

// ═══ КОПИЯ ДДС И PL С СЕРВЕРА ═══
// Работает, только когда данные клуба ведёт сервер. Раз в час переписывает
// «Учет ДДС» и цифры «PL» тем, что на сервере
function bsPullFromServer(){
  if(bsIsCopy() || !bsServerIsMaster()) return;
  var r = bsSignedPost("/api/v1/club/export", {});
  if(r.code !== 200 || !r.j) { Logger.log("pull: " + r.code + " " + String(r.raw).slice(0, 200)); return; }
  var ss = SpreadsheetApp.openById(SS_ID);
  // ДДС
  var wd = ss.getSheetByName("Учет ДДС");
  var rows = r.j.payments || [];
  if(wd && rows.length){
    var vals = wd.getDataRange().getDisplayValues();
    var h = -1;
    for(var i = 0; i < Math.min(vals.length, 10); i++){ if(vals[i].indexOf("Дата") >= 0){ h = i; break; } }
    if(h >= 0){
      var head = vals[h].map(function(x){ return String(x).toLowerCase().trim(); });
      var col = function(n){ for(var k = 0; k < head.length; k++){ if(head[k].indexOf(n) === 0) return k; } return -1; };
      var cDate = col("дата"), cIn = col("приход"), cOut = col("расход"), cSrc = col("источник"), cPlus = col("категория +"), cMinus = col("категория -");
      var width = Math.max(cDate, cIn, cOut, cSrc, cPlus, cMinus) + 2;
      var grid = rows.map(function(p){
        var line = []; for(var k = 0; k < width; k++) line.push("");
        var dp = String(p.date).split(".");
        line[cDate] = new Date(Number(dp[2]), Number(dp[1])-1, Number(dp[0]), 12, 0, 0);
        line[cIn] = p.income || ""; line[cOut] = p.expense || "";
        line[cSrc] = p.incomeCat || ""; line[cPlus] = p.resident || ""; line[cMinus] = p.expenseCat || "";
        line[cMinus + 1] = p.applied ? "учтено" : "";
        return line;
      });
      var first = h + 2, lastRow = wd.getLastRow();
      if(lastRow >= first) wd.getRange(first, 1, lastRow - first + 1, width).clearContent();
      wd.getRange(first, 1, grid.length, width).setValues(grid);
      wd.getRange(first, cDate + 1, grid.length, 1).setNumberFormat("DD.MM.YYYY");
    }
  }
  // PL: цифры по названиям строк, месяцы в колонках B..M
  var wp = ss.getSheetByName("PL");
  var pl = r.j.pl || [];
  if(wp && pl.length){
    var names = wp.getRange(1, 1, wp.getLastRow(), 1).getDisplayValues().map(function(x){ return String(x[0]).trim().toLowerCase(); });
    pl.forEach(function(line){
      var at = names.indexOf(String(line.name).trim().toLowerCase());
      if(at < 2) return;
      wp.getRange(at + 1, 2, 1, 12).setValues([line.values.map(function(v){ return v === null ? "" : v; })]);
    });
  }
  PropertiesService.getScriptProperties().setProperty("BS_LAST_PULL", new Date().toISOString());
}

// Отправляет на сервер всех резидентов с Chat ID. Бывшие теряют доступ.
// Подпись: HMAC-SHA256 тела запроса с токеном бота. Сервер знает тот же токен
function bsPushResidentsToPlatform(silent){
  if(bsIsCopy()) return;
  var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
  if(!ws || ws.getLastRow() < 3) return;
  var d = ws.getRange(3, 2, ws.getLastRow()-2, 20).getValues();
  var list = [], seen = {};
  d.forEach(function(r){
    var name = String(r[RI.name] || "").trim();
    var tg = _normId(r[RI.chat]);
    if(!name || !/^\d{5,15}$/.test(tg) || seen[tg]) return;
    if(r[RI.admin] === "Да") return;           // команда входит отдельно, не как резидент
    seen[tg] = 1;
    list.push({tg: Number(tg), name: name, active: r[RI.former] !== "Да"});
  });
  var body = JSON.stringify({ts: Math.floor(Date.now() / 1000), residents: list});
  var sig = Utilities.computeHmacSha256Signature(body, BOT_TOKEN, Utilities.Charset.UTF_8).map(function(b){
    var h = (b < 0 ? b + 256 : b).toString(16); return h.length < 2 ? "0" + h : h;
  }).join("");
  var resp = UrlFetchApp.fetch(PLATFORM_API + "/api/v1/platform/residents/sync", {
    method: "post", contentType: "application/json", payload: body,
    headers: {"X-BS-Signature": sig}, muteHttpExceptions: true
  });
  var code = resp.getResponseCode(), txt = resp.getContentText();
  var ok = code === 200;
  var result = ok ? ("Платформа: резидентов с доступом " + list.filter(function(x){return x.active;}).length +
                     " из " + list.length + " с Chat ID")
                  : ("Платформа не приняла список: " + code + " " + txt.slice(0, 200));
  Logger.log(result);
  if(!ok){
    // Не чаще раза в сутки, чтобы не заспамить
    var c = CacheService.getScriptCache();
    if(!c.get("platformPushWarn")){ c.put("platformPushWarn", "1", 22*3600); try{ bsSysNote("⚠️ " + result); }catch(e){} }
  }
  if(!silent){ try{ SpreadsheetApp.getUi().alert(result); }catch(e){} }
  return {ok: ok, code: code, count: list.length, body: body, sig: sig};
}

// ═══════════════════════════════════════════════════════════════════════════
// СТОРОЖ БОТА. ловит немого бота в тот же день, а не через неделю штрафов
// ═══════════════════════════════════════════════════════════════════════════
function bsWatchBot(){
  if(bsIsCopy()) return;
  try{
    var hour=parseInt(Utilities.formatDate(new Date(),"Asia/Almaty","H"),10);
    if(hour<11 || hour>23) return;              // ночью тишина. это норма

    var props=PropertiesService.getScriptProperties();
    var now=new Date().getTime();
    var lastUpd=parseInt(props.getProperty("BS_LAST_UPDATE_TS")||"0",10);
    var silentH = lastUpd ? Math.floor((now-lastUpd)/3600000) : -1;

    // Ночью и утром тишина нормальна. Тревожимся, только если молчание
    // перекрыло целые сутки: живая группа за это время пишет хоть что-то
    if(silentH>=0 && silentH<18) return;

    var cache=CacheService.getScriptCache();
    if(cache.get("botSilentWarn")) return;       // не долбим чаще раза в сутки
    cache.put("botSilentWarn","1",22*3600);

    var info=bsWebhookState();
    var txt="🚨 БОТ МОЛЧИТ\n\n";
    txt += (silentH<0 ? "Бот ни разу не получал сообщений из Telegram с момента установки этой версии.\n"
                      : "Последнее сообщение из Telegram было "+silentH+" ч назад.\n");
    txt += "\n"+info.text+"\n\nПока это не починить, отчёты не записываются, а ночная проверка штрафы выставлять не будет.";
    bsSysNote(txt);
    Logger.log("bsWatchBot: тревога отправлена, тишина "+silentH+" ч");
  }catch(e){ Logger.log("bsWatchBot: "+e); }
}

// Состояние вебхука одной функцией. используется и сторожем, и диагностикой
function bsWebhookState(){
  var out={ok:false,text:"",url:"",current:""};
  var current="";
  try{ current=ScriptApp.getService().getUrl()||""; }catch(e){}
  out.current=current;
  try{
    var r=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",
      {muteHttpExceptions:true});
    var j=JSON.parse(r.getContentText());
    if(!j.ok){
      out.text="❌ Telegram не принял токен бота.\nОтвет: "+(j.description||"без описания")+
               "\n\nЗначит BOT_TOKEN в скрипте неверный или бот удалён.";
      return out;
    }
    var w=j.result||{};
    out.url=w.url||"";
    if(!w.url){
      out.text="❌ Вебхук не установлен. Бот не получает ни одного сообщения.\n"+
               "Лечится пунктом меню BS → «Переподключить бота».";
      return out;
    }
    // Адреса развёртываний сравнивать бессмысленно: getService().getUrl() отдаёт
    // служебный адрес проекта, а не тот, что опубликован. Единственная честная
    // проверка. спросить у самого вебхука, какая версия кода по нему отвечает
    var liveVer="";
    try{
      var vr=UrlFetchApp.fetch(w.url.split("?")[0]+"?action=bsVersion",
        {muteHttpExceptions:true, followRedirects:true});
      liveVer=(JSON.parse(vr.getContentText())||{}).version||"";
    }catch(e){}
    out.live=liveVer;
    out.ok = !w.last_error_message && liveVer===BS_VERSION;
    // Сообщения идут через сервер платформы: смотрим и его очередь
    var viaServer = w.url.indexOf("/api/v1/bot/webhook") >= 0;
    out.viaServer = viaServer;
    var srvText = "";
    if(viaServer){
      var st = bsServerBotStatus();
      var q = (st && st.queue) || null;
      if(!q){
        out.ok = false;
        srvText = "\n❌ Сервер платформы не ответил на проверку";
      } else {
        var stuck = q.waiting > 0 && q.oldestWaitingSec > 900;
        if(stuck || q.gaveUp72h > 0) out.ok = false;
        srvText = "\nЧерез сервер платформы: за сутки получено " + q.received24h + ", передано в таблицу " + q.relayed24h +
          (q.avgRelayMs24h ? " (таблица отвечает в среднем за " + (Math.round(q.avgRelayMs24h/100)/10) + " с)" : "") +
          (q.waiting ? "\n" + (stuck ? "❌" : "⏳") + " Ждут передачи: " + q.waiting + ", самое старое " + Math.round(q.oldestWaitingSec/60) + " мин" : "") +
          (q.gaveUp72h ? "\n❌ Не передано после 10 попыток: " + q.gaveUp72h + (q.lastError ? " (" + q.lastError + ")" : "") : "") +
          ((st.features || []).length ? "\nСервер делает сам: " + st.features.map(function(f){ return BS_FEATURE_NAMES[f] || f; }).join(", ") : "");
      }
    }
    out.text = (viaServer ? "Вебхук: сервер платформы → таблица\n" : "")+"Вебхук: "+w.url+
      "\nВ очереди: "+(w.pending_update_count||0)+
      (w.last_error_message ? "\n❌ Последняя ошибка Telegram: "+w.last_error_message+
        " ("+(w.last_error_date?Utilities.formatDate(new Date(w.last_error_date*1000),"Asia/Almaty","dd.MM HH:mm"):"?")+")"
        : "\n✅ Ошибок доставки нет")+
      (liveVer===BS_VERSION
        ? "\n✅ По этому адресу отвечает актуальный код "+BS_VERSION
        : (liveVer
            ? "\n❌ По адресу вебхука работает СТАРЫЙ код «"+liveVer+"», а в таблице уже "+BS_VERSION+
              ".\nРазвернуть → Управление развёртываниями → карандаш → Версия: Новая → Развернуть."
            : "\n⚠️ Версию по адресу вебхука узнать не удалось. Если отчёты приходят, значит связь есть."))+
      srvText;
    return out;
  }catch(e){
    out.text="Не удалось спросить Telegram: "+e;
    return out;
  }
}

// Полная диагностика. пункт меню
function bsDiagnose(){
  var lines=[];
  lines.push("ДИАГНОСТИКА BS. версия скрипта "+BS_VERSION);
  lines.push("");

  // 1. Вебхук
  var w=bsWebhookState();
  lines.push("1) СВЯЗЬ С TELEGRAM");
  lines.push(w.text);
  lines.push("");

  // 2. Пульс
  var props=PropertiesService.getScriptProperties();
  var lastUpd=parseInt(props.getProperty("BS_LAST_UPDATE_TS")||"0",10);
  lines.push("2) ПОСЛЕДНЕЕ СООБЩЕНИЕ ОТ TELEGRAM");
  lines.push(lastUpd
    ? Utilities.formatDate(new Date(lastUpd),"Asia/Almaty","dd.MM.yyyy HH:mm")+
      " ("+Math.floor((new Date().getTime()-lastUpd)/3600000)+" ч назад)"
    : "не зафиксировано ни одного");
  lines.push("");

  // 3. Лог отчётов
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsL=ss.getSheetByName("Лог отчётов");
  lines.push("3) ЛОГ ОТЧЁТОВ");
  if(wsL && wsL.getLastRow()>1){
    var ll=wsL.getLastRow();
    var from=Math.max(2,ll-30);
    var tail=wsL.getRange(from,1,ll-from+1,3).getValues();
    var newest=null,newestName="";
    tail.forEach(function(r){
      if(r[0] instanceof Date && (!newest || r[0]>newest)){ newest=r[0]; newestName=String(r[2]||""); }
    });
    lines.push("Всего записей: "+(ll-1));
    lines.push(newest
      ? "Последний отчёт: "+Utilities.formatDate(newest,"Asia/Almaty","dd.MM.yyyy HH:mm")+
        " — "+newestName+" ("+Math.floor((new Date().getTime()-newest.getTime())/3600000)+" ч назад)"
      : "даты не читаются");
    // Сколько отчётов за вчера
    var y=new Date(); y.setDate(y.getDate()-1);
    var yStr=bsDayStr(y), cnt=0;
    var all=wsL.getRange(2,1,ll-1,3).getValues();
    all.forEach(function(r){ if(r[0] instanceof Date && bsDayStr(r[0])===yStr) cnt++; });
    lines.push("Отчётов за вчера ("+yStr+"): "+cnt);
  } else lines.push("лист пуст");
  lines.push("");

  // 4. Триггеры
  lines.push("4) ТРИГГЕРЫ");
  try{
    var ts=ScriptApp.getProjectTriggers();
    var names={};
    ts.forEach(function(t){ var h=t.getHandlerFunction(); names[h]=(names[h]||0)+1; });
    var human={
      onTableEdit:"правки в таблице",
      bsEvery5Min:"PL и кеш приложения",
      bsHourly:"сторож бота, штрафы, ДДС, напоминания",
      bsDaily:"проверка отчётов и штрафы (14:30)",
      eveningReminder:"напоминание сдать отчёт (22:00)"
    };
    BS_DISPATCHERS.forEach(function(need){
      lines.push((names[need]?"✅ ":"❌ НЕТ ")+need+" — "+human[need]+
        (names[need]>1?"  ⚠️ дубль x"+names[need]:""));
    });
    var extra=Object.keys(names).filter(function(n){ return BS_DISPATCHERS.indexOf(n)<0; });
    if(extra.length) lines.push("Лишние триггеры: "+extra.join(", ")+
      "\n→ Они дублируют работу диспетчеров. Меню BS → «Убрать лишние триггеры»");
    lines.push("Всего триггеров: "+ts.length+" из 20 разрешённых");
    if(!names.bsDaily) lines.push("→ Ночная проверка не запустится. Меню BS → «Переустановить триггеры»");
  }catch(e){ lines.push("не прочитать: "+e); }
  lines.push("");

  // 5. Резиденты без Chat ID
  lines.push("5) РЕЗИДЕНТЫ БЕЗ CHAT ID");
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var noId=[];
  if(wsR && wsR.getLastRow()>2){
    var d=wsR.getRange(3,2,wsR.getLastRow()-2,20).getValues();
    d.forEach(function(r){
      if(!r[RI.name]) return;
      if(r[RI.former]==="Да"||r[RI.except]==="Да"||r[RI.admin]==="Да") return;
      if(!_normId(r[RI.chat])) noId.push(String(r[RI.name]));
    });
  }
  lines.push(noId.length ? noId.join(", ")+"\n(их бот не видит и не штрафует)" : "нет, у всех проставлен");
  lines.push("");

  // 6. Копия таблицы
  lines.push("6) ЭТО КОПИЯ ТАБЛИЦЫ?");
  lines.push(props.getProperty("BS_IS_COPY")==="1"
    ? "❌ ДА. вся автоматика отключена. Если это рабочая таблица, нажмите меню BS → «Снять метку копии»"
    : "нет, рабочая таблица");

  var text=lines.join("\n");
  try{ SpreadsheetApp.getUi().alert(text); }catch(e){}
  try{ tgSendAdmins(text); }catch(e){}
  return text;
}

// Если метка копии проставилась по ошибке на рабочей таблице
function bsClearCopyFlag(){
  PropertiesService.getScriptProperties().deleteProperty("BS_IS_COPY");
  try{ SpreadsheetApp.getUi().alert("Метка копии снята. Не забудьте заново включить триггеры: меню BS → «Диагностика бота» покажет, каких не хватает."); }catch(e){}
}

// Восстанавливает пять диспетчеров, на которых держится вся автоматика.
// Отдельных триггеров для dailyCheck и bsWatchBot быть не должно: они вызываются
// внутри bsDaily и bsHourly. Иначе упираемся в лимит 20 триггеров на проект
var BS_DISPATCHERS = ["onTableEdit","bsEvery5Min","bsHourly","bsDaily","eveningReminder"];

// Убирает только лишнее: чужие обработчики и дубли диспетчеров.
// Рабочие пять не трогает, поэтому безопаснее полной переустановки
function bsCleanTriggers(){
  if(bsIsCopy()) return;
  var ui=null; try{ ui=SpreadsheetApp.getUi(); }catch(e){}
  var seen={}, removed=[];
  ScriptApp.getProjectTriggers().forEach(function(t){
    var h=t.getHandlerFunction();
    var extra = BS_DISPATCHERS.indexOf(h)<0;   // не диспетчер
    var dup   = !extra && seen[h];             // второй такой же диспетчер
    if(extra || dup){
      try{ ScriptApp.deleteTrigger(t); removed.push(h+(dup?" (дубль)":"")); }catch(e){}
    } else seen[h]=true;
  });
  var missing=BS_DISPATCHERS.filter(function(d){ return !seen[d]; });
  var text = removed.length
    ? "Убрано лишних триггеров: "+removed.length+"\n• "+removed.join("\n• ")
    : "Лишних триггеров нет.";
  if(missing.length) text += "\n\n⚠️ Не хватает: "+missing.join(", ")+
    "\nНажмите «Переустановить триггеры».";
  else text += "\n\nВсе пять рабочих триггеров на месте.";
  if(ui) ui.alert(text);
  return text;
}

function bsSetupTriggers(){
  if(bsIsCopy()){ try{SpreadsheetApp.getUi().alert("Это копия таблицы, триггеры не ставим.");}catch(e){} return; }
  var ui=null; try{ ui=SpreadsheetApp.getUi(); }catch(e){}
  var before=ScriptApp.getProjectTriggers().map(function(t){return t.getHandlerFunction();});

  ScriptApp.getProjectTriggers().forEach(function(t){
    try{ ScriptApp.deleteTrigger(t); }catch(e){}
  });
  ScriptApp.newTrigger("onTableEdit").forSpreadsheet(SS_ID).onEdit().create();
  ScriptApp.newTrigger("bsEvery5Min").timeBased().everyMinutes(5).create();
  ScriptApp.newTrigger("bsHourly").timeBased().everyHours(1).create();
  ScriptApp.newTrigger("bsDaily").timeBased().atHour(14).nearMinute(30).everyDays(1).inTimezone("Asia/Almaty").create();
  ScriptApp.newTrigger("eveningReminder").timeBased().atHour(22).nearMinute(0).everyDays(1).inTimezone("Asia/Almaty").create();

  var after=ScriptApp.getProjectTriggers().map(function(t){return t.getHandlerFunction();});
  var text="Триггеры переустановлены\n\nБыло ("+before.length+"): "+(before.join(", ")||"пусто")+
    "\nСтало ("+after.length+"): "+after.join(", ")+
    "\n\nЧто внутри:\n"+
    "• bsDaily 14:30 → ночная проверка отчётов и штрафы\n"+
    "• bsHourly каждый час → сторож бота, штрафы, ДДС, напоминания о встречах\n"+
    "• bsEvery5Min → PL, кеш приложения\n"+
    "• eveningReminder 22:00 → напоминание сдать отчёт";
  if(ui) ui.alert(text);
  return text;
}

// Снимает штрафы за дни, когда бот был нем. По одному дню или диапазону
function bsCancelFinesForDates(dates){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsF=ss.getSheetByName("Штрафы");
  if(!wsF || wsF.getLastRow()<3) return 0;
  var set={}; (dates||[]).forEach(function(d){ set[String(d).trim()]=true; });
  var removed=[];
  for(var r=wsF.getLastRow(); r>=3; r--){
    var nm=String(wsF.getRange(r,2).getValue()||"").trim();
    var rs=String(wsF.getRange(r,3).getValue()||"").trim();
    var dt=wsF.getRange(r,5).getValue();
    var ds=dt instanceof Date ? bsDayStr(dt) : String(dt).trim();
    if(nm && rs==="Не сдан отчёт" && set[ds]){
      wsF.deleteRow(r);
      removed.push(nm+" за "+ds);
    }
  }
  if(removed.length){
    try{ _renumFines(); }catch(e){}
    try{ bsSafeRecalcDebet(); }catch(e){}
  }
  return removed;
}

function bsSafeRecalcDebet(){
  if(bsIsCopy()) return;
  var ss = SpreadsheetApp.openById(SS_ID);
  var ws = ss.getSheetByName("BS - резиденты дебет");
  var wsF = ss.getSheetByName("Штрафы");
  if(!ws || ws.getLastRow() < 3) return;
  var lr = ws.getLastRow(), rows = lr - 2;
  var CL = function(n){ return String.fromCharCode(64+n); };

  // Сумма неоплаченных штрафов по именам
  var fines = {};
  if(wsF && wsF.getLastRow() >= 3){
    wsF.getRange(3, 1, wsF.getLastRow()-2, 6).getValues().forEach(function(r){
      var nm = String(r[1]||"").trim();
      if(!nm || String(r[5]||"").trim() === "Оплатил") return;
      fines[nm] = (fines[nm]||0) + (Number(r[3])||0);
    });
  }

  var names = ws.getRange(3, RC.name, rows, 1).getValues();
  var fineCol = [], leftF = [], totalF = [];
  var FN = "'Штрафы'", S = bsArgSep();
  for(var i = 0; i < rows; i++){
    var r = 3 + i, nm = String(names[i][0]||"").trim();
    // Формула: сумма неоплаченных штрафов по имени. Удалили штраф - колонка обновилась сразу
    fineCol.push([nm ? ("=SUMIFS(" + FN + "!D:D" + S + FN + "!B:B" + S + CL(RC.name) + r + S +
      FN + "!F:F" + S + "\"<>Оплатил\")") : ""]);
    leftF.push(["=" + CL(RC.granted) + r + "-" + CL(RC.done) + r]);
    totalF.push(["=" + CL(RC.rest) + r + "+" + CL(RC.renew) + r + "+" + CL(RC.fine) + r]);
  }
  ws.getRange(3, RC.fine, rows, 1).setFormulas(fineCol);

  // Отрицательных значений не бывает: ноль значит ноль
  var neg = ws.getRange(3, RC.paid, rows, 4).getValues();   // G,H,I,J
  var fixed = neg.map(function(r){
    return r.map(function(v, ix){
      if(ix === 3) return v;                                // J это формула, не трогаем
      var n = Number(v);
      return (typeof v === "number" && n < 0) ? 0 : v;
    });
  });
  ws.getRange(3, RC.paid, rows, 3).setValues(fixed.map(function(r){ return r.slice(0,3); }));
  ws.getRange(3, RC.left, rows, 1).setFormulas(leftF);
  ws.getRange(3, RC.total, rows, 1).setFormulas(totalF);
  SpreadsheetApp.flush();
}

function bsNotifyResident(name, text){
  // Находит Chat ID по имени и пишет резиденту. Одна точка для всех уведомлений
  try{
    if(!name || !text) return false;
    var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws || ws.getLastRow() < 3) return false;
    var d = ws.getRange(3, 2, ws.getLastRow()-2, 20).getValues();
    for(var i = 0; i < d.length; i++){
      if(String(d[i][RI.name]||"").trim() !== String(name).trim()) continue;
      var cid = _normId(d[i][RI.chat]);
      if(!cid) return false;
      tgSend(cid, text);
      return true;
    }
  }catch(e){ Logger.log("bsNotifyResident: " + e); }
  return false;
}

function bsNormCat(s){
  // Приводим название статьи к общему виду: регистр, пробелы, плюс, двоеточие
  return String(s||"").toLowerCase().replace(/ё/g,"е")
    .replace(/\s*\+\s*/g,"+").replace(/[:.\s]+$/,"").replace(/\s+/g," ").trim();
}

function bsPLName(s){
  return String(s||"").trim().replace(/[:.\s]+$/,"");
}

function bsRebuildPL(silent){
  // Пересчитывает PL из ДДС. Новые статьи расходов сами появляются строкой над «Прочие расходы»
  if(bsIsCopy()) return;
  var lock = null;
  try{ lock = LockService.getScriptLock(); if(!lock.tryLock(25000)) lock = null; }catch(le){ lock = null; }
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var dds = ss.getSheetByName("Учет ДДС");
    var pl = ss.getSheetByName("PL");
    if(!dds || !pl || dds.getLastRow() < 2) return;

    var M;
    function mapRows(){
      var lr = pl.getLastRow(), colA = pl.getRange(1, 1, lr, 1).getValues();
      var m = {lr:lr, names:[], rows:{}, rIncH:-1, rIncT:-1, rExpH:-1, rExpT:-1, rProfit:-1, rDiv:-1, rKassa:-1, rOther:-1, rOtherInc:-1};
      for(var r = 1; r <= lr; r++){
        var nm = String(colA[r-1][0]||"").trim();
        m.names[r] = nm;
        if(!nm) continue;
        if(nm === "ДОХОДЫ") m.rIncH = r;
        else if(nm === "ИТОГО ДОХОДЫ") m.rIncT = r;
        else if(nm === "РАСХОДЫ") m.rExpH = r;
        else if(nm === "ИТОГО РАСХОДЫ") m.rExpT = r;
        else if(nm === "ЧИСТАЯ ПРИБЫЛЬ") m.rProfit = r;
        else if(nm === "Дивиденды") m.rDiv = r;
        else if(nm === "На кассе" && m.rKassa < 0) m.rKassa = r;
        else if(m.rows[bsNormCat(nm)] === undefined) m.rows[bsNormCat(nm)] = r;
        if(bsNormCat(nm) === "прочие расходы") m.rOther = r;
        if(bsNormCat(nm) === "прочие доходы") m.rOtherInc = r;
      }
      return m;
    }
    M = mapRows();
    if(M.rIncH < 0 || M.rIncT < 0 || M.rExpH < 0 || M.rExpT < 0 || M.rProfit < 0){
      Logger.log("bsRebuildPL: структура PL сломана"); return;
    }

    var nowY = new Date().getFullYear(), nowM = new Date().getMonth();
    var MONTHS = nowM + 1;
    var data = dds.getRange(2, 1, dds.getLastRow()-1, 6).getValues();
    function inYear(x){
      var d = x[0];
      return (d instanceof Date) && d.getFullYear() === nowY && d.getMonth() <= nowM;
    }

    // Новые статьи расходов: строка в PL создаётся сама, без сообщений в бот
    var addNames = [], seen = {};
    data.forEach(function(x){
      if(!inYear(x) || !(Number(x[2]) > 0)) return;
      var cat = bsNormCat(x[5]);
      if(!cat || cat.indexOf("дивиденд") >= 0) return;
      if(M.rows[cat] !== undefined || seen[cat]) return;
      seen[cat] = 1;
      addNames.push(bsPLName(x[5]));
    });
    if(addNames.length){
      var at = (M.rOther > M.rExpH && M.rOther < M.rExpT) ? M.rOther : M.rExpT;
      pl.insertRowsBefore(at, addNames.length);
      try{ pl.getRange(at + addNames.length, 1, 1, 14).copyTo(pl.getRange(at, 1, addNames.length, 14), {formatOnly:true}); }catch(fe){}
      pl.getRange(at, 1, addNames.length, 1).setValues(addNames.map(function(n){ return [n]; }));
      bsSysNote("PL: добавлены статьи " + addNames.join(", "));
      M = mapRows();
    }

    var vals = {}, divM = {};
    function put(rowIdx, m, v){
      if(!(rowIdx > 0)) return;
      vals[rowIdx] = vals[rowIdx] || [];
      vals[rowIdx][m] = (vals[rowIdx][m] || 0) + v;
    }
    data.forEach(function(x){
      if(!inYear(x)) return;
      var m = x[0].getMonth();
      var inc = Number(x[1]) || 0, exp = Number(x[2]) || 0;
      if(inc){
        var ri = M.rows[bsNormCat(x[3]) || "прочие доходы"];
        if(!(ri > M.rIncH && ri < M.rIncT)) ri = M.rOtherInc;
        put(ri, m, inc);
      }
      if(exp){
        var cat = bsNormCat(x[5]);
        if(cat.indexOf("дивиденд") >= 0){ divM[m] = (divM[m]||0) + exp; return; }
        var re = cat ? M.rows[cat] : undefined;
        if(!(re > M.rExpH && re < M.rExpT)) re = M.rOther;
        put(re, m, exp);
      }
    });

    // Месяцы пишем блоками: доходы и расходы, по одной записи на блок
    function writeBlock(r1, r2){
      if(r2 < r1) return;
      var blk = pl.getRange(r1, 2, r2 - r1 + 1, MONTHS);
      var cur = blk.getValues();
      for(var r = r1; r <= r2; r++){
        if(!M.names[r]) continue;
        for(var m = 0; m < MONTHS; m++) cur[r - r1][m] = (vals[r] && vals[r][m]) || 0;
      }
      blk.setValues(cur);
    }
    writeBlock(M.rIncH + 1, M.rIncT - 1);
    writeBlock(M.rExpH + 1, M.rExpT - 1);
    if(M.rDiv > 0){
      var dv = [];
      for(var md = 0; md < MONTHS; md++) dv.push(divM[md] || 0);
      pl.getRange(M.rDiv, 2, 1, MONTHS).setValues([dv]);
    }

    // Формулы итогов и кассы: одной записью на строку
    var COLS = "BCDEFGHIJKLM".split("");
    var fInc = [], fExp = [], fPr = [], fK = [];
    for(var ci = 0; ci < 12; ci++){
      var col = COLS[ci];
      fInc.push("=SUM(" + col + (M.rIncH+1) + ":" + col + (M.rIncT-1) + ")");
      fExp.push("=SUM(" + col + (M.rExpH+1) + ":" + col + (M.rExpT-1) + ")");
      fPr.push("=" + col + M.rIncT + "-" + col + M.rExpT);
      var dvRef = M.rDiv > 0 ? "-" + col + M.rDiv : "";
      if(ci < CASH_START_MONTH) fK.push("");
      else if(ci === CASH_START_MONTH) fK.push("=" + col + M.rProfit + dvRef);
      else fK.push("=" + COLS[ci-1] + M.rKassa + "+" + col + M.rProfit + dvRef);
    }
    pl.getRange(M.rIncT, 2, 1, 12).setFormulas([fInc]);
    pl.getRange(M.rExpT, 2, 1, 12).setFormulas([fExp]);
    pl.getRange(M.rProfit, 2, 1, 12).setFormulas([fPr]);
    if(M.rKassa > 0) pl.getRange(M.rKassa, 2, 1, 12).setFormulas([fK]);
    SpreadsheetApp.flush();
    try{ _invalidateBundleCache(); }catch(ie){}

    if(!silent) _alert("PL пересчитан из ДДС" +
      (addNames.length ? "\n\nДобавлены статьи: " + addNames.join(", ") : ""));
    Logger.log("bsRebuildPL: готово, новых статей " + addNames.length);
  }catch(e){
    Logger.log("bsRebuildPL: " + e);
    if(!silent) _alert("Ошибка: " + e);
  }finally{
    try{ if(lock) lock.releaseLock(); }catch(e){}
  }
}

function bsWatchDDS(){
  // Раз в час: если ДДС менялся, пересчитываем PL
  if(bsIsCopy()) return;
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var dds = ss.getSheetByName("Учет ДДС");
    if(!dds || dds.getLastRow() < 2) return;
    var d = dds.getRange(2, 1, dds.getLastRow()-1, 6).getValues();
    var sum = 0, cnt = 0;
    d.forEach(function(x){
      var v = (Number(x[1])||0) + (Number(x[2])||0);
      if(v){ sum += v; cnt++; }
    });
    var sig = cnt + ":" + sum;
    var props = PropertiesService.getScriptProperties();
    if(props.getProperty("BS_DDS_SIG") === sig) return;
    props.setProperty("BS_DDS_SIG", sig);
    bsRebuildPL(true);
    Logger.log("bsWatchDDS: ДДС изменился, PL пересчитан");
  }catch(e){ Logger.log("bsWatchDDS: " + e); }
}

function bsApplyPaymentsFromDDS(){
  // Разносит оплаты из ДДС по резидентам: гасит остаток входа, затем долг продления.
  // Каждая строка ДДС учитывается один раз, отметка ставится в колонке G
  if(bsIsCopy()) return {applied:0};
  var ss = SpreadsheetApp.openById(SS_ID);
  var dds = ss.getSheetByName("Учет ДДС");
  var ws = ss.getSheetByName("BS - резиденты дебет");
  if(!dds || !ws || dds.getLastRow() < 2 || ws.getLastRow() < 3) return {applied:0};

  var lrR = ws.getLastRow(), rowsR = lrR - 2;
  var rd = ws.getRange(3, 1, rowsR, 21).getValues();
  var idx = {};
  for(var i = 0; i < rowsR; i++){
    var nm = String(rd[i][RA.name]||"").trim();
    if(nm) idx[nm.toLowerCase()] = i;
  }

  var lrD = dds.getLastRow();
  var dd = dds.getRange(2, 1, lrD-1, 7).getValues();
  var applied = [], marks = [];

  // Первый запуск: всё, что было до включения, помечаем учтённым и долги не трогаем.
  // Иначе оплаты за весь год разнесутся заново и обнулят долги
  var props = PropertiesService.getScriptProperties();
  if(!props.getProperty("BS_DDS_START")){
    var init = dd.map(function(row){
      return [String(row[6]||"").trim() || (Number(row[1]) ? "учтено" : "")];
    });
    dds.getRange(2, 7, init.length, 1).setValues(init);
    try{ dds.getRange(1,7).setValue("Разнесено по долгам").setFontWeight("bold"); }catch(e){}
    props.setProperty("BS_DDS_START", new Date().toISOString());
    SpreadsheetApp.flush();
    Logger.log("bsApplyPaymentsFromDDS: первый запуск, прошлые оплаты помечены");
    return {applied:0, init:init.length};
  }

  for(var j = 0; j < dd.length; j++){
    var mark = String(dd[j][6]||"").trim();
    var inc = Number(dd[j][1]) || 0;
    var who = String(dd[j][4]||"").trim();           // Категория + = имя резидента
    if(mark === "учтено" || !inc || !who){ marks.push([mark]); continue; }
    // Оплата штрафа закрывает штрафы, долг входа и продления не трогает
    if(String(dd[j][3]||"").indexOf("Штраф") >= 0){ marks.push([mark]); continue; }

    var k = idx[who.toLowerCase()];
    if(k === undefined){ marks.push([mark]); continue; }

    var rest = Number(rd[k][RA.rest]) || 0;
    var renew = Number(rd[k][RA.renew]) || 0;
    var left = inc;

    var payRest = Math.min(rest, left);
    rest -= payRest; left -= payRest;
    var payRenew = Math.min(renew, left);
    renew -= payRenew; left -= payRenew;

    if(payRest || payRenew){
      rd[k][RA.rest] = rest;
      rd[k][RA.renew] = renew;
      applied.push({name:who, sum:payRest + payRenew, rest:rest, renew:renew,
                    row:k, extra:left});
      marks.push(["учтено"]);
    } else {
      marks.push([mark]);
    }
  }

  if(!applied.length) return {applied:0};

  // Записываем новые остатки и отметки
  applied.forEach(function(a){
    ws.getRange(3 + a.row, RC.rest).setValue(a.rest);
    ws.getRange(3 + a.row, RC.renew).setValue(a.renew);
  });
  dds.getRange(2, 7, marks.length, 1).setValues(marks);
  try{ bsSafeRecalcDebet(); }catch(e){}
  try{ _invalidateBSCaches(); }catch(e){}
  SpreadsheetApp.flush();

  // Сообщаем резиденту и админам
  applied.forEach(function(a){
    try{
      var cid = _normId(rd[a.row][RA.chat]);
      var total = a.rest + a.renew;
      if(cid) tgSend(cid, "Оплата " + _fmtMoney(a.sum) + " получена.\n" +
        (total > 0 ? "Остаток долга: " + _fmtMoney(total) : "Долг закрыт полностью"));
    }catch(e){}
  });
  try{
    bsSysNote("Разнесены оплаты из ДДС: " + applied.length + "\n" +
      applied.map(function(a){ return a.name + " " + _fmtMoney(a.sum) +
        (a.extra ? " (переплата " + _fmtMoney(a.extra) + ")" : ""); }).join("\n"));
  }catch(e){}

  return {applied:applied.length, list:applied};
}

function bsGuardDebet(){
  if(bsIsCopy()) return;
  // Сторож дебета: каждые 5 минут проверяет колонки и чинит, если чужой код их испортил
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("BS - резиденты дебет");
    if(!ws || ws.getLastRow() < 3) return;
    var lr = ws.getLastRow(), rows = lr - 2;
    var data = ws.getRange(3, 1, rows, 21).getValues();

    var BK = {};
    RESIDENTS_BACKUP.forEach(function(r){ BK[r.n] = r; });

    var broken = [];
    for(var i = 0; i < rows; i++){
      var nm = String(data[i][RA.name]||"").trim();
      if(!nm) continue;
      var g = Number(data[i][RA.granted])||0, d = Number(data[i][RA.done])||0;
      var left = Number(data[i][RA.left])||0, paid = Number(data[i][RA.paid])||0;
      // Встреч не бывает больше сотни: значит в колонке оказались деньги
      if(Math.abs(left) > 100){
        broken.push({i:i, nm:nm, why:"осталось встреч " + left});
        continue;
      }
      // Пустые строки и нулевые значения не трогаем: у админов так и должно быть
      if(paid <= 0) continue;
      // Мусор от старой формулы: оплачено равно сумме встреч или сумме встреч со штрафом
      var meets = g + d;
      var junk = (meets > 0 && paid === meets) ||
                 (paid < 1000) ||
                 (meets > 0 && paid > 10000 && (paid - meets) >= 10000 && (paid - meets) % 10000 === 0);
      if(junk) broken.push({i:i, nm:nm, why:"оплачено вход " + paid});
    }
    if(!broken.length) return;

    var CL = function(n){ return String.fromCharCode(64+n); };
    broken.forEach(function(b){
      var row = 3 + b.i;
      ws.getRange(row, RC.left).setFormula("=" + CL(RC.granted) + row + "-" + CL(RC.done) + row);
      var bk = BK[b.nm];
      if(bk) ws.getRange(row, RC.paid).setValue(bk.p || 0);
    });
    bsSafeRecalcDebet();
    try{ _invalidateBSCaches(); }catch(e){}

    // Предупреждаем не чаще раза в час
    var c = CacheService.getScriptCache();
    if(!c.get("guardWarn")){
      c.put("guardWarn", "1", 20*3600);
      try{
        bsSysNote("Дебет чинился автоматически: " + broken.length + " строк\n" +
          broken.slice(0,5).map(function(b){ return b.nm + ": " + b.why; }).join("\n") +
          "\n\nПишет чужой скрипт. Откройте script.google.com/home и удалите триггеры старого проекта");
      }catch(e){}
    }
    Logger.log("bsGuardDebet: починено " + broken.length);
  }catch(e){ Logger.log("bsGuardDebet: " + e); }
}

function bsOldCodePresent(){
  // Если одноимённые функции подменены старой копией, их текст будет другим
  var out = [];
  try{ if(String(recalcResidents).indexOf("bsSafeRecalcDebet") < 0) out.push("recalcResidents"); }catch(e){}
  try{ if(String(dailyCheck).indexOf("realCount") < 0) out.push("dailyCheck"); }catch(e){}
  try{ if(String(_getMiniAppData).indexOf("RA.total") < 0) out.push("_getMiniAppData"); }catch(e){}
  try{ if(String(onTableEdit).indexOf("onEditDebet") < 0) out.push("onTableEdit"); }catch(e){}
  return out;
}

// ═══════════════════════════════════════════════════════════════
// ЗАЩИТА ОТ ОШИБОЧНЫХ ШТРАФОВ
// ═══════════════════════════════════════════════════════════════
var ALLOWED_TRIGGERS = ["onTableEdit","bsEvery5Min","bsHourly","bsDaily","eveningReminder"];

function bsRemoveFinesForDay(){
  if(bsIsCopy()) return;
  // Снимает штрафы за отчёт за выбранный день. Для случаев, когда бот не записал отчёты
  var ui = SpreadsheetApp.getUi();
  var resp = ui.prompt("Снять штрафы за отчёт",
    "За какой день отчёта? Формат ДД.ММ.ГГГГ\nНапример: 20.09.2026", ui.ButtonSet.OK_CANCEL);
  if(resp.getSelectedButton() !== ui.Button.OK) return;
  var day = String(resp.getResponseText()||"").trim();
  if(!/^\d{2}\.\d{2}\.\d{4}$/.test(day)) return _alert("Нужен формат ДД.ММ.ГГГГ");

  var ss = SpreadsheetApp.openById(SS_ID);
  var wsF = ss.getSheetByName("Штрафы");
  var wsR = ss.getSheetByName("BS - резиденты дебет");
  if(!wsF || wsF.getLastRow() < 3) return _alert("Штрафов нет");
  var chat = {};
  if(wsR && wsR.getLastRow() >= 3){
    wsR.getRange(3, 2, wsR.getLastRow()-2, 20).getValues().forEach(function(r){
      var nm = String(r[RI.name]||"").trim(); if(nm) chat[nm] = _normId(r[RI.chat]);
    });
  }
  var tz = "Asia/Almaty", stz = tz;
  try{ stz = Session.getScriptTimeZone() || tz; }catch(e){}
  var data = wsF.getRange(3, 1, wsF.getLastRow()-2, 6).getValues();
  var hit = [];
  for(var i = 0; i < data.length; i++){
    var nm = String(data[i][1]||"").trim(), kind = String(data[i][2]||""), dt = data[i][4];
    if(!nm || kind.indexOf("отч") < 0 || !(dt instanceof Date)) continue;
    var forDay;
    if(dt.getMinutes() === 0 && dt.getSeconds() === 0) forDay = Utilities.formatDate(dt, stz, "dd.MM.yyyy");
    else {
      var b = new Date(dt.getTime());
      if(parseInt(Utilities.formatDate(dt, tz, "H")) < 14) b.setDate(b.getDate()-1);
      forDay = Utilities.formatDate(b, tz, "dd.MM.yyyy");
    }
    if(forDay === day) hit.push({row:3+i, name:nm});
  }
  if(!hit.length) return _alert("Штрафов за отчёт за " + day + " нет");
  var ok = ui.alert("Снять " + hit.length + " штрафов за " + day + "?",
    hit.map(function(x){ return x.name; }).join(", ") +
    "\n\nРезидентам уйдёт сообщение, что штраф снят.", ui.ButtonSet.YES_NO);
  if(ok !== ui.Button.YES) return;
  hit.sort(function(a,b){ return b.row - a.row; }).forEach(function(x){ try{ wsF.deleteRow(x.row); }catch(e){} });
  try{ _renumFines(); }catch(e){}
  try{ bsSafeRecalcDebet(); }catch(e){}
  try{ _invalidateBSCaches(); }catch(e){}
  hit.forEach(function(x){
    if(chat[x.name]){ try{ tgSend(chat[x.name], "Штраф за отчёт за " + day + " снят. Извините за ошибку системы."); }catch(e){} }
  });
  _alert("Снято штрафов: " + hit.length);
}

function bsPurgeFines(silent){
  if(bsIsCopy()) return;
  // Снимает штрафы за отчёт, которые не должны были появиться:
  // админам, исключениям, бывшим, тем кто сдал отчёт или был на встрече.
  // Работает независимо от того, какой код их выписал
  var removed = [];
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var wsF = ss.getSheetByName("Штрафы");
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    var wsL = ss.getSheetByName("Лог отчётов");
    if(!wsF || !wsR || wsF.getLastRow() < 3) return removed;

    // Кто не подлежит штрафу вообще
    var exempt = {}, chatByName = {};
    wsR.getRange(3, 2, wsR.getLastRow()-2, 20).getValues().forEach(function(r){
      var nm = String(r[RI.name]||"").trim();
      if(!nm) return;
      chatByName[nm] = _normId(r[RI.chat]);
      if(String(r[RI.admin]).trim()==="Да" || String(r[RI.except]).trim()==="Да" ||
         String(r[RI.former]).trim()==="Да") exempt[nm] = true;
    });

    // Кто сдал отчёт в какой день (поздние не считаются)
    var sent = {};
    if(wsL && wsL.getLastRow() > 1){
      wsL.getRange(2, 1, wsL.getLastRow()-1, 8).getValues().forEach(function(r){
        if(!(r[0] instanceof Date)) return;
        if(String(r[7]||"").trim()==="Да") return;
        var day = bsDayStr(r[0]);
        var nm = String(r[2]||"").trim().toLowerCase();
        var id = _normId(r[5]);
        if(nm) sent[nm+"|"+day] = true;
        if(id) sent["id"+id+"|"+day] = true;
      });
    }

    var tz = "Asia/Almaty";
    var data = wsF.getRange(3, 1, wsF.getLastRow()-2, 6).getValues();
    var toDel = [];
    for(var i = 0; i < data.length; i++){
      var nm = String(data[i][1]||"").trim();
      var kind = String(data[i][2]||"");
      var dt = data[i][4];
      var status = String(data[i][5]||"");
      if(!nm || kind.indexOf("отч") < 0) continue;
      if(status === "Оплатил") continue;

      // За какой день штраф: если время не полночь, значит выписан ночью за вчера
      var forDay = "";
      if(dt instanceof Date){
        if(dt.getMinutes() === 0 && dt.getSeconds() === 0){
          // Ровное время: штраф выписан на конкретный день. Берём дату в поясе скрипта
          var stz = "Asia/Almaty";
          try{ stz = Session.getScriptTimeZone() || stz; }catch(e){}
          forDay = Utilities.formatDate(dt, stz, "dd.MM.yyyy");
        } else {
          // Время с минутами и секундами: штраф выписан ночью, значит за вчера
          var h = parseInt(Utilities.formatDate(dt, tz, "H"));
          var base = new Date(dt.getTime());
          if(h < 14) base.setDate(base.getDate()-1);
          forDay = Utilities.formatDate(base, tz, "dd.MM.yyyy");
        }
      }

      var why = "";
      if(exempt[nm]) why = "исключение или админ";
      else if(forDay && (sent[nm.toLowerCase()+"|"+forDay] ||
              (chatByName[nm] && sent["id"+chatByName[nm]+"|"+forDay]))) why = "отчёт сдан";
      else if(forDay){
        try{
          var met = _getResidentsWithMeetingOn(forDay) || [];
          if(met.indexOf(nm) >= 0) why = "была встреча";
        }catch(e){}
      }
      if(why) toDel.push({row:3+i, name:nm, day:forDay, why:why});
    }

    toDel.sort(function(a,b){ return b.row - a.row; });
    toDel.forEach(function(x){
      try{ wsF.deleteRow(x.row); removed.push(x); }catch(e){}
    });

    if(removed.length){
      try{ _renumFines(); }catch(e){}
      try{ bsSafeRecalcDebet(); }catch(e){}
      try{ _invalidateBSCaches(); }catch(e){}
      // Извиняемся перед резидентами, которым ушло уведомление о штрафе
      removed.forEach(function(x){
        var cid = chatByName[x.name];
        if(cid && !exempt[x.name]){
          try{ tgSend(cid, "Штраф за " + x.day + " снят: отчёт был засчитан. Извините за ошибочное уведомление."); }catch(e){}
        }
      });
      try{
        bsSysNote("Снято ошибочных штрафов: " + removed.length + "\n" +
          removed.map(function(x){ return x.name + " за " + x.day + " (" + x.why + ")"; }).join("\n"));
      }catch(e){}
    }
  }catch(e){ Logger.log("bsPurgeFines: " + e); }

  if(!silent){
    _alert(removed.length ? ("Снято ошибочных штрафов: " + removed.length + "\n\n" +
      removed.map(function(x){ return x.name + " за " + x.day + " (" + x.why + ")"; }).join("\n"))
      : "Ошибочных штрафов нет");
  }
  return removed;
}

function bsAuditTriggers(){
  // Показывает все триггеры проекта и находит чужие
  var list = ScriptApp.getProjectTriggers();
  var bad = [], ok = [];
  list.forEach(function(t){
    var h = t.getHandlerFunction();
    if(ALLOWED_TRIGGERS.indexOf(h) >= 0) ok.push(h); else bad.push(h);
  });
  // Проверка, что работает актуальная версия ночной проверки
  var src = "";
  try{ src = String(dailyCheck); }catch(e){}
  var fresh = src.indexOf("realCount") >= 0 && src.indexOf("RI.except") >= 0;
  return {ok:ok, bad:bad, freshDaily:fresh, total:list.length};
}

function bsReconnectBot(){
  if(bsIsCopy()) return;
  // Направляет Telegram на то развёртывание, которое вы обновляете
  var ui = SpreadsheetApp.getUi();
  var resp = ui.prompt("Переподключить бота",
    "Откройте Развернуть → Управление развёртываниями.\n" +
    "Скопируйте URL веб-приложения (заканчивается на /exec) и вставьте сюда:",
    ui.ButtonSet.OK_CANCEL);
  if(resp.getSelectedButton() !== ui.Button.OK) return;
  var url = String(resp.getResponseText()||"").trim();
  if(!/^https:\/\/script\.google\.com\/macros\/s\/[\w-]+\/exec$/.test(url))
    return _alert("Нужен адрес вида https://script.google.com/macros/s/.../exec");

  // Проверяем, что по адресу работает этот код
  var live = "";
  try{
    var vr = UrlFetchApp.fetch(url + "?action=bsVersion", {muteHttpExceptions:true, followRedirects:true});
    live = JSON.parse(vr.getContentText()).version || "";
  }catch(e){}
  if(live !== BS_VERSION){
    return _alert("По этому адресу работает версия «" + (live || "без номера") + "», а нужна " + BS_VERSION + ".\n\n" +
      "Сначала: Развернуть → Управление развёртываниями → карандаш у этого развёртывания → " +
      "Версия: Новая версия → Развернуть. Доступ: Все. Потом повторите.");
  }

  PropertiesService.getScriptProperties().setProperty("BS_WEBHOOK_URL", url);
  // Напрямую сервер сообщений не видит: всё, что он делал, снова делает таблица
  try{ bsForceSheet(true); }catch(e){}
  var r = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/setWebhook", {
    method:"post", contentType:"application/json", muteHttpExceptions:true,
    payload: JSON.stringify({url:url, secret_token:"bs2026secret", drop_pending_updates:false,
      allowed_updates:["message","edited_message","callback_query","message_reaction"]})
  });
  var ok = false;
  try{ ok = JSON.parse(r.getContentText()).ok; }catch(e){}
  Utilities.sleep(4000);
  var info = {};
  try{ info = JSON.parse(UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",
    {muteHttpExceptions:true}).getContentText()).result || {}; }catch(e){}
  _alert((ok ? "Бот переподключён к версии " + BS_VERSION : "Telegram не принял адрес") + "\n\n" +
    "Сообщений в очереди: " + (info.pending_update_count || 0) + "\n" +
    (info.last_error_message ? "Последняя ошибка: " + info.last_error_message +
      "\n(старая ошибка остаётся в статусе, пока не придёт новое сообщение)" : "Ошибок нет") +
    "\n\nНакопленные отчёты придут в течение минуты и засчитаются за день отправки.");
}

function bsReinstallTriggers(){
  if(bsIsCopy()) return;
  var before = bsAuditTriggers();
  try{ setupAllTriggers(); }catch(e){ return _alert("Ошибка: " + e); }
  var after = bsAuditTriggers();
  _alert("Триггеры переустановлены\n\n" +
    "Было: " + before.total + (before.bad.length ? " (чужие: " + before.bad.join(", ") + ")" : "") + "\n" +
    "Стало: " + after.total + "\n\n" +
    (after.freshDaily ? "Ночная проверка актуальная" :
      "ВНИМАНИЕ: в проекте есть старая копия dailyCheck.\n" +
      "Откройте Apps Script, слева список файлов. Оставьте один файл со скриптом, остальные удалите."));
}

function dailyCheck(){
  if(bsIsCopy()) return;
  if(bsServerOwns("daily_check")) return;   // проверку и штрафы делает сервер
  try{
  var cache=CacheService.getScriptCache();
  // Запускается в 02:00. поздние отчёты успевают записаться
  // (отчёты должны быть сданы ДО полуночи)
  var yesterdayDate=new Date();
  yesterdayDate.setDate(yesterdayDate.getDate()-1);
  var checkDate=bsDayStr(yesterdayDate);
  Logger.log("dailyCheck для даты: "+checkDate);
  // ДВОЙНОЙ ДЕДУП: кеш (быстрый) + Properties (надёжный, переживает сбои кеша)
  // Причина двойных штрафов: CacheService не гарантирован + возможны дубли триггеров
  var props=PropertiesService.getScriptProperties();
  if(cache.get("daily_"+checkDate) || props.getProperty("DAILY_DONE_"+checkDate)){
    Logger.log("dailyCheck уже выполнен для "+checkDate+". пропускаю");
    return;
  }
  cache.put("daily_"+checkDate,"1",24*3600);
  props.setProperty("DAILY_DONE_"+checkDate, new Date().toISOString());
  // Чистим старые ключи (старше 7 дней) чтобы Properties не разрастались
  try{
    var allProps=props.getProperties();
    var weekAgo=new Date();weekAgo.setDate(weekAgo.getDate()-7);
    Object.keys(allProps).forEach(function(k){
      if(k.indexOf("DAILY_DONE_")===0){
        var v=new Date(allProps[k]);
        if(v<weekAgo)props.deleteProperty(k);
      }
    });
  }catch(e){}
  Logger.log("dailyCheck СТАРТ для "+checkDate);
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsL=ss.getSheetByName("Лог отчётов");
  if(!wsR)return;
  var lr=wsR.getLastRow();
  if(lr<3)return;

  // Читаем лог отчётов. chatId → true для тех кто сдал ВЧЕРА
  var submittedChatIds={};
  if(wsL&&wsL.getLastRow()>1){
    var ll=wsL.getLastRow();
    var logData=wsL.getRange(2,1,ll-1,8).getValues();
    logData.forEach(function(row){
      if(!row[0]||!(row[0] instanceof Date))return;
      // Отчёт после полуночи не засчитывается ни за какой день
      if(String(row[7]||"").trim()==="Да")return;
      // День отчёта уже посчитан при записи по правилу 14:00, берём как есть
      var dStr=bsDayStr(new Date(row[0]));
      if(dStr===checkDate){
        var uid=_normId(row[5]);
        if(uid)submittedChatIds[uid]=true;
        // Дублируем по имени: если Chat ID сбился, найдём по имени
        var rnm=String(row[2]||"").trim();
        if(rnm)submittedChatIds["name:"+rnm.toLowerCase()]=true;
      }
    });
  }
  var sentCount=Object.keys(submittedChatIds).filter(function(k){return k.indexOf("name:")!==0;}).length;
  Logger.log("Сдали отчёт "+checkDate+": "+sentCount);

  // ── СТОП-КРАН: отличаем «никто не сдал» от «бот ничего не получил» ──────
  // Свежесть лога: когда в него вообще последний раз что-то писали
  var lastLogTs=0;
  if(wsL&&wsL.getLastRow()>1){
    var _ll=wsL.getLastRow();
    var _from=Math.max(2,_ll-40);
    var _tail=wsL.getRange(_from,1,_ll-_from+1,1).getValues();
    _tail.forEach(function(r){
      if(r[0] instanceof Date && r[0].getTime()>lastLogTs) lastLogTs=r[0].getTime();
    });
  }
  var hoursSilent = lastLogTs ? Math.floor((new Date().getTime()-lastLogTs)/3600000) : 9999;
  // Когда бот последний раз вообще получал апдейт от Telegram
  var lastUpd=parseInt(props.getProperty("BS_LAST_UPDATE_TS")||"0",10);
  var updSilentH = lastUpd ? Math.floor((new Date().getTime()-lastUpd)/3600000) : -1;

  var _actives=wsR.getRange(3,2,lr-2,20).getValues().filter(function(r){
    return r[RI.name] && r[RI.former]!=="Да" && r[RI.except]!=="Да" && r[RI.admin]!=="Да";
  }).length;

  // Ноль отчётов при живых резидентах. это не прогул десяти человек, это поломка
  // Проверка идёт в 14:30, поэтому пороги с запасом: ночью и утром тишина. это норма
  var systemDown = (_actives>=3 && sentCount===0) || hoursSilent>48 || updSilentH>24;
  if(systemDown){
    var why=[];
    if(_actives>=3 && sentCount===0) why.push("за "+checkDate+" в логе ноль отчётов, а активных резидентов "+_actives);
    if(hoursSilent>48) why.push("последняя запись в логе отчётов была "+(hoursSilent>=9999?"очень давно":hoursSilent+" ч назад"));
    if(updSilentH>24) why.push("бот не получал сообщений из Telegram "+updSilentH+" ч");
    var alarm="🚨 ШТРАФЫ НЕ ВЫСТАВЛЕНЫ. ПОХОЖЕ НА СБОЙ\n\n"+
      "Ночная проверка за "+checkDate+" остановлена.\n\n"+
      "Почему:\n• "+why.join("\n• ")+"\n\n"+
      "Штрафовать вслепую нельзя: если бот не получает сообщения, резиденты не виноваты.\n\n"+
      "Что сделать: в таблице меню BS → «Диагностика бота». Там видно, жив ли вебхук.";
    Logger.log("dailyCheck ОСТАНОВЛЕН: "+why.join("; "));
    try{ bsSysNote(alarm); }catch(e){}
    // Снимаем отметку «день обработан», чтобы после починки проверку можно было прогнать заново
    try{
      cache.remove("daily_"+checkDate);
      props.deleteProperty("DAILY_DONE_"+checkDate);
    }catch(e){}
    return;
  }

  // Резиденты у которых была встреча вчера. их НЕ штрафуем за несданный отчёт
  var hadMeetingYesterday=_getResidentsWithMeetingOn(checkDate);
  Logger.log("Был трекинг "+checkDate+": "+JSON.stringify(hadMeetingYesterday));
  var today=checkDate; // для сообщений ниже

  var resData=wsR.getRange(3, 2, lr-2, 20).getValues();
  var fined=[],missing=[],noChatId=[];
  var realCount=0;
  resData.forEach(function(row,i){
    var name=row[0];if(!name)return;
    if(row[RI.former]==="Да")return; // бывший
    if(row[RI.except]==="Да")return; // исключение
    if(row[RI.admin]==="Да")return; // админ не считается резидентом
    realCount++;
    var chatId=_normId(row[RI.chat]);
    var nameKey="name:"+String(name).trim().toLowerCase();
    if((chatId&&submittedChatIds[chatId]) || submittedChatIds[nameKey]){
      return; // отчёт сдан
    }
    // Без Chat ID бот физически не видит отчёты этого человека. штрафовать нечестно
    if(!chatId){
      noChatId.push(name);
      Logger.log("Не штрафуем "+name+": в таблице пустой Chat ID");
      return;
    }
    // В день встречи отчёт не требуется
    if(hadMeetingYesterday && hadMeetingYesterday.indexOf(name) >= 0) return;
    // Был трекинг вчера. не штрафуем
    if(_hadMeeting(name, hadMeetingYesterday)){
      Logger.log("Пропускаем "+name+". был трекинг "+checkDate);
      return;
    }
    // Отчёт не найден. штраф
    missing.push(name);
    var resRow=3+i;
    // Добавляем штраф
    var wsF=ss.getSheetByName("Штрафы");
    // ИДЕМПОТЕНТНОСТЬ: если штраф этому имени за эту дату уже есть. НЕ дублируем
    // (окончательное решение двойных штрафов: проверка по данным, не по кешу)
    if(wsF){
      try{
        var lrF=wsF.getLastRow();
        if(lrF>=3){
          var fData=wsF.getRange(3,2,lrF-2,4).getValues(); // B=имя, C=причина, D=сумма, E=дата
          for(var fi=0;fi<fData.length;fi++){
            var fName=String(fData[fi][0]||"").trim();
            var fReason=String(fData[fi][1]||"").trim();
            var fDate=fData[fi][3];
            var fDateStr=fDate instanceof Date?bsDayStr(fDate):String(fDate);
            if(fName===name && fReason==="Не сдан отчёт" && fDateStr===checkDate){
              Logger.log("Штраф "+name+" за "+checkDate+" уже существует. пропускаем дубль");
              wsF=null; // не пишем
              break;
            }
          }
        }
      }catch(dupE){Logger.log("fine dup check: "+dupE);}
    }
    if(wsF){
      // Находим следующую свободную строку
      var fr=_findFreeRow(wsF,3,2);
      // Номер
      wsF.getRange(fr,1).setValue(fr-2);
      // Имя
      wsF.getRange(fr,2).setValue(name);
      // Причина
      wsF.getRange(fr,3).setValue("Не сдан отчёт");
      // Сумма
      wsF.getRange(fr,4).setValue(FINE_AMT).setNumberFormat("#,##0");
      // Дата штрафа = за какой день он (yesterday)
      // Полдень: дата не съедет ни в каком часовом поясе
      var fineDate=new Date(yesterdayDate.getFullYear(),yesterdayDate.getMonth(),yesterdayDate.getDate(),12,0,0);
      wsF.getRange(fr,5).setValue(fineDate).setNumberFormat("DD.MM.YYYY");
      // Статус
      wsF.getRange(fr,6).setValue("Не оплатил");
      // Единое оформление через общую функцию, чтобы все строки выглядели одинаково
      try{
        _applyRowStyle(wsF, fr, 6, (fr-3)%2===1);
        wsF.getRange(fr,1).setHorizontalAlignment("center");
        wsF.getRange(fr,2,1,2).setHorizontalAlignment("left");
        wsF.getRange(fr,4).setHorizontalAlignment("center").setNumberFormat("#,##0");
        wsF.getRange(fr,5,1,2).setHorizontalAlignment("center");
        wsF.getRange(fr,2,1,2).setHorizontalAlignment("left");
        wsF.setRowHeight(fr,22);
      }catch(fmtE){Logger.log("fmt fine: "+fmtE);}
      _renumFines();
      _fixFinesHeader();
    }
    // Уведомляем резидента
    if(chatId){
      var fineMsg=getText("fine_reminder",{"имя":name.split(" ")[0],"дата":today,"kaspi":KASPI_LINK});
      if(!fineMsg)fineMsg=name.split(" ")[0]+", выставлен штраф "+FINE_AMT.toLocaleString()+" тг за несдачу отчёта.\nОплатить: "+KASPI_LINK;
      tgSend(chatId,fineMsg,{reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("fines")}}]]})});
    }
    fined.push(name);
  });

  recalcResidents();
  try{ bsPurgeFines(true); }catch(pE){ Logger.log("purge: "+pE); }

  // Сводка админам
  var summary="📋 Ночная проверка за "+checkDate+"\n\n";
  // Считаем только тех, кого реально проверяли: без бывших, исключений и админов
  var totalActive=resData.filter(function(r){
    return r[RI.name] && r[RI.former]!=="Да" && r[RI.except]!=="Да" && r[RI.admin]!=="Да";
  }).length;
  var doneCount=totalActive-fined.length-noChatId.length;
  var excCount=resData.filter(function(r){
    return r[RI.name] && r[RI.former]!=="Да" && (r[RI.except]==="Да"||r[RI.admin]==="Да");
  }).length;
  summary+="Сдали отчёт: "+doneCount+" из "+totalActive+"\n";
  if(excCount) summary+="Не проверялись (исключения и админы): "+excCount+"\n";
  summary+="\n";
  if(fined.length>0){
    summary+="❌ Не сдали (штраф "+FINE_AMT.toLocaleString()+" тг):\n";
    fined.forEach(function(n){summary+="  • "+n+"\n";});
  } else {
    summary+="✅ Все сдали отчёт!";
  }
  if(noChatId.length){
    summary+="\n\n⚠️ Не проверены. в таблице пустой Chat ID:\n";
    noChatId.forEach(function(n){summary+="  • "+n+"\n";});
    summary+="Пока Chat ID пуст, бот не видит их отчёты и не штрафует.";
  }
  Logger.log("dailyCheck ИТОГ: сдали="+doneCount+", штрафы="+fined.length+", сообщение админам отправляется");
  try{
    tgSendAdmins(summary,"reports");
    Logger.log("dailyCheck: сообщение админам отправлено успешно");
  }catch(sendE){
    Logger.log("dailyCheck FAILED отправка админам: "+sendE);
    try{bsSysNote("⚠️ Ночная проверка прошла, но отправка отчёта упала: "+sendE);}catch(e){}
  }
  }catch(globalErr){
    var critMsg="❌ КРИТИЧЕСКАЯ ОШИБКА в ночной проверке:\n\n"+globalErr.toString()+"\n\nStack:\n"+(globalErr.stack||"нет");
    try{bsSysNote(critMsg);}catch(e){}
    Logger.log("dailyCheck CRITICAL: "+globalErr);
  }
}
function eveningReminder(){
  if(bsIsCopy()) return;
  if(bsServerOwns("evening_reminder")) return;   // напоминание шлёт сервер
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsL=ss.getSheetByName("Лог отчётов");
  if(!wsR)return;
  var today=new Date();
  var ds=Utilities.formatDate(today,"Asia/Almaty","dd.MM.yyyy");
  var lr=wsR.getLastRow(),rep=[];
  if(wsL){
    var ll=wsL.getLastRow();
    for(var r=2;r<=ll;r++){
      var ld=wsL.getRange(r,1).getValue();
      if(ld&&Utilities.formatDate(new Date(ld),"Asia/Almaty","dd.MM.yyyy")===ds)
        rep.push(wsL.getRange(r,3).getValue());
    }
  }
  // Отправляем напоминание тем кто ещё не сдал
  for(var r=3;r<=lr;r++){
    var nm=wsR.getRange(r, RC.name).getValue();if(!nm)continue;
    if(wsR.getRange(r, RC.former).getValue()==="Да")continue;
    if(wsR.getRange(r, RC.except).getValue()==="Да")continue;
    var f=nm.split(" ")[0].toLowerCase();
    var wrote=rep.some(function(x){return x.toLowerCase().indexOf(f)>=0;});
    if(!wrote){
      var chatId=String(wsR.getRange(r, RC.chat).getValue()||"");
      if(chatId){
        var msg=getText("report_reminder",{"имя":nm.split(" ")[0]});
        if(!msg)msg="⏰ "+nm.split(" ")[0]+", напоминание! Не забудьте сдать отчёт до 23:59\nШтраф за пропуск: 10 000 тг";
        tgSend(chatId,msg);
      }
    }
  }
}



function _startOnboarding(chatId,name){
  var cache=CacheService.getScriptCache();
  cache.put("onb_"+chatId,JSON.stringify({day:1,name:name}),6*24*3600);
}

function setupOnboardingTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="sendOnboardingMessages")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("sendOnboardingMessages").timeBased().atHour(10).nearMinute(0).everyDays(1).create();
}

function sendOnboardingMessages(){
  // ЗАЩИТА: не шлём ничего ночью (22:00 - 09:00 по Алматы)
  var hour=parseInt(Utilities.formatDate(new Date(),"Asia/Almaty","H"));
  if(hour<9 || hour>=22) return;
  
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");if(!wsR)return;
  var lr=wsR.getLastRow();
  var cache=CacheService.getScriptCache();
  for(var r=3;r<=lr;r++){
    var chatId=String(wsR.getRange(r, RC.chat).getValue()||"");if(!chatId)continue;
    var raw=cache.get("onb_"+chatId);if(!raw)continue;
    try{
      var data=JSON.parse(raw);
      var day=data.day||1;
      var name=(data.name||wsR.getRange(r, RC.name).getValue()||"").split(" ")[0];
      if(day<=ONBOARDING_MSGS.length){
        var msg=ONBOARDING_MSGS[day-1];
        if(msg){tgSend(chatId,msg.replace("{имя}",name));}
        data.day=day+1;
        if(data.day<=ONBOARDING_MSGS.length){
          cache.put("onb_"+chatId,JSON.stringify(data),6*24*3600);
        } else {
          cache.remove("onb_"+chatId);
        }
      }
    }catch(e){Logger.log("onboarding: "+e);}
  }
}


function _askAbsentList(adminCid,meetRow){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");if(!wsR)return;
  var lr=wsR.getLastRow();
  var buttons=[];var row_btns=[];
  for(var r=3;r<=lr;r++){
    var nm=wsR.getRange(r, RC.name).getValue();if(!nm)continue;
    if(wsR.getRange(r, RC.former).getValue()==="Да")continue;
    if(wsR.getRange(r, RC.except).getValue()==="Да")continue;
    var fmt=String(wsR.getRange(r, RC.format)?wsR.getRange(r, RC.format).getValue():"Офлайн");
    if(fmt!=="Офлайн")continue;
    row_btns.push({text:"\u274C "+nm.split(" ")[0],callback_data:"absent_"+meetRow+"_"+nm});
    if(row_btns.length===3){buttons.push(row_btns);row_btns=[];}
  }
  if(row_btns.length>0)buttons.push(row_btns);
  buttons.push([{text:"\u2705 Все присутствовали",callback_data:"absent_all"}]);
  UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
    method:"post",contentType:"application/json",
    payload:JSON.stringify({
      chat_id:adminCid,
      text:"Кто НЕ был на встрече? Нажмите кого не было:",
      reply_markup:JSON.stringify({inline_keyboard:buttons})
    }),muteHttpExceptions:true
  });
}


function _adminAddIncome(cid,rawText){
  if(bsMasterRefuse(cid)) return;
  var parts=rawText.trim().split(/\s+/);
  if(parts.length<3){tgSend(cid,"❌ Формат: /приход [сумма] [источник] [имя]\nПример: /приход 150000 трекинг Асет");return;}
  var amount=Number(parts[1].replace(/[^0-9]/g,""));
  var src=parts[2].toLowerCase();
  var name=parts.slice(3).join(" ")||"";
  var srcMap={"трекинг":"БХ Трекинг продление","год":"БХ Трекинг год","штраф":"БХ Штраф","мк":"БХ МК/Завтрак","экспресс":"БХ Экспресс разбор","прочее":"Прочие доходы"};
  var srcFull=srcMap[src]||"Прочие доходы";
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Учет ДДС");if(!ws){tgSend(cid,"❌ ОДДС не найден");return;}
    var r=ws.getLastRow()+1;
    ws.showRows(r,1);
    var todayD=new Date();
    var dateOnly=new Date(todayD.getFullYear(),todayD.getMonth(),todayD.getDate());
    ws.getRange(r,1).setValue(dateOnly).setNumberFormat("DD.MM.YYYY");
    ws.getRange(r,2).setValue(amount).setNumberFormat("#,##0");
    ws.getRange(r,4).setValue(srcFull);
    if(name)ws.getRange(r,5).setValue(name);

    var warn2 = "";
    if(!srcMap[src]) warn2 = "\n\nИсточник «" + src + "» не распознан, записал как Прочие доходы.\n" +
      "Доступные: трекинг, год, штраф, мк, экспресс, прочее";
    tgSend(cid,"Приход записан\n" + amount.toLocaleString() + " тг\n" + srcFull +
      (name ? "\n" + name : "") + warn2);
  }catch(e){tgSend(cid,"❌ Ошибка: "+e.message);}
}

function _adminAddExpense(cid,rawText){
  if(bsMasterRefuse(cid)) return;
  var parts=rawText.trim().split(/\s+/);
  if(parts.length<3){tgSend(cid,"❌ Формат: /расход [сумма] [категория]\nПример: /расход 50000 SMM");return;}
  var amount=Number(parts[1].replace(/[^0-9]/g,""));
  var cat=parts.slice(2).join(" ");
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Учет ДДС");if(!ws){tgSend(cid,"❌ ОДДС не найден");return;}
    var r=ws.getLastRow()+1;
    ws.showRows(r,1);
    var todayD=new Date();
    var dateOnly=new Date(todayD.getFullYear(),todayD.getMonth(),todayD.getDate());
    ws.getRange(r,1).setValue(dateOnly).setNumberFormat("DD.MM.YYYY");
    ws.getRange(r,3).setValue(amount).setNumberFormat("#,##0");

    // Сверяем категорию со строками PL, иначе расход не попадёт в отчёт
    var catFinal = cat, warn = "";
    try{
      var pl = ss.getSheetByName("PL");
      if(pl){
        var plNames = [];
        for(var pr = 1; pr <= pl.getLastRow(); pr++){
          var pn = String(pl.getRange(pr,1).getValue()||"").trim();
          if(pn && pn !== pn.toUpperCase()) plNames.push(pn);
        }
        var low = cat.toLowerCase();
        var exact = plNames.filter(function(x){ return x.toLowerCase() === low; })[0];
        if(exact) catFinal = exact;
        else {
          var near = plNames.filter(function(x){
            return x.toLowerCase().indexOf(low) >= 0 || low.indexOf(x.toLowerCase()) >= 0;
          })[0];
          if(near){ catFinal = near; warn = "\n\nЗаписал как «" + near + "»"; }
          else warn = "\n\nТакой статьи нет в PL. Расход записан, но в отчёт не попадёт.\n" +
            "Доступные: " + plNames.slice(0,12).join(", ");
        }
      }
    }catch(plE){ Logger.log("PL check: "+plE); }

    ws.getRange(r,6).setValue(catFinal);
    tgSend(cid,"Расход записан\n" + amount.toLocaleString() + " тг\n" + catFinal + warn);
  }catch(e){tgSend(cid,"❌ Ошибка: "+e.message);}
}



// ── Пошаговый выбор штрафа ──────────────────────────────────────────────

function _getCachedResidents(){
  var cache=CacheService.getScriptCache();
  var cached=cache.get("residents_list");
  if(cached){
    try{return JSON.parse(cached);}catch(e){}
  }
  // Читаем из таблицы
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(!wsR)return[];
  var lr=wsR.getLastRow();
  var names=[];
  if(lr>2){
    var data=wsR.getRange(3, 2, lr-2, 20).getValues();
    for(var i=0;i<data.length;i++){
      var nm=data[i][0];if(!nm)continue;
      if(data[i][10]==="Да")continue; // бывший
      if(data[i][11]==="Да")continue; // исключение. всё же показываем для штрафов
      names.push(nm);
    }
  }
  cache.put("residents_list",JSON.stringify(names),600); // 10 минут
  return names;
}

function _invalidateResidentsCache(){
  CacheService.getScriptCache().remove("residents_list");
}


// Обновляем кэш каждые 10 минут триггером
function setupDataCacheTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="refreshBotCache")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("refreshBotCache").timeBased().everyMinutes(10).create();
}

function refreshBotCache(){
  var cache=CacheService.getScriptCache();
  var ss=SpreadsheetApp.openById(SS_ID);
  
  // Кэш резидентов
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(wsR){
    var lr=wsR.getLastRow();
    var names=[];
    if(lr>2){
      var data=wsR.getRange(3, 2, lr-2, 20).getValues();
      for(var i=0;i<data.length;i++){
        var nm=data[i][0];if(!nm)continue;
        if(data[i][10]==="Да")continue;
        // Пропускаем Тест и Тест2
        if(nm==="Тест"||nm==="Тест2")continue;
        names.push(nm);
      }
    }
    cache.put("bot_residents",JSON.stringify(names),60);
  }
  
  // Кэш дебет (PL данные)
  var wsPL=ss.getSheetByName("PL");
  var wsF=ss.getSheetByName("Штрафы");
  var curM=new Date().getMonth()+1;
  var col=1+curM;
  var debet={netProfit:0,residual:0,unpaidFines:0,unpaidCount:0};
  if(wsPL){
    // Ищем строки по названию, а не по номеру
    var plLr=wsPL.getLastRow(), rProfit=-1, rKassa=-1;
    for(var pr=1; pr<=plLr; pr++){
      var pn=String(wsPL.getRange(pr,1).getValue()||"").trim();
      if(pn==="ЧИСТАЯ ПРИБЫЛЬ") rProfit=pr;
      if(pn==="На кассе") rKassa=pr;
    }
    if(rProfit>0) debet.netProfit=Number(wsPL.getRange(rProfit,col).getValue())||0;
    if(rKassa>0) debet.residual=Number(wsPL.getRange(rKassa,col).getValue())||0;
  }
  if(wsF&&wsF.getLastRow()>2){
    var fd=wsF.getRange(3, 1, wsF.getLastRow()-2, 21).getValues();
    fd.forEach(function(r){
      // Лист Штрафы: 4 сумма (индекс 3), 6 статус (индекс 5)
      if(String(r[5]||"").trim()==="Не оплатил"){
        debet.unpaidFines+=Number(r[3])||0; debet.unpaidCount++;
      }
    });
  }
  cache.put("bot_debet",JSON.stringify(debet),60);
  
  // Кэш расписания на месяц
  var ws=ss.getSheetByName("Расписание");
  var schedList=[];
  if(ws&&ws.getLastRow()>2){
    var now=new Date();
    var monthEnd=new Date(now.getFullYear(),now.getMonth()+1,0);
    var sd=ws.getRange(3, 1, ws.getLastRow()-2, 21).getValues();
    sd.forEach(function(r){
      if(!r[1]||!r[RI.rest])return;
      if(!(r[RI.rest] instanceof Date))return;
      if(r[RI.rest]<now||r[RI.rest]>monthEnd)return;
      var tStr=r[RI.renew] instanceof Date?Utilities.formatDate(r[RI.renew],"Asia/Almaty","HH:mm"):"";
      schedList.push({
        res:r[1],
        date:Utilities.formatDate(r[RI.rest],"Asia/Almaty","dd.MM"),
        time:tStr,
        link:r[RI.total]||""
      });
    });
  }
  cache.put("bot_schedule",JSON.stringify(schedList),60);
  
  Logger.log("Bot cache refreshed: "+names.length+" residents, "+schedList.length+" events");
}

function _getCachedDebet(){
  var cache=CacheService.getScriptCache();
  var raw=cache.get("bot_debet");
  if(raw){try{return JSON.parse(raw);}catch(e){}}
  refreshBotCache();
  raw=cache.get("bot_debet");
  if(raw){try{return JSON.parse(raw);}catch(e){}}
  return{netProfit:0,residual:0,unpaidFines:0,unpaidCount:0};
}

function _getCachedSchedule(){
  var cache=CacheService.getScriptCache();
  var raw=cache.get("bot_schedule");
  if(raw){try{return JSON.parse(raw);}catch(e){}}
  refreshBotCache();
  raw=cache.get("bot_schedule");
  if(raw){try{return JSON.parse(raw);}catch(e){}}
  return[];
}

function _sendFineStep1(cid){
  var names=_getCachedResidents();
  var buttons=[];var row_btns=[];
  names.forEach(function(nm){
    row_btns.push({text:nm.split(" ")[0],callback_data:"fine_res_"+nm});
    if(row_btns.length===3){buttons.push(row_btns);row_btns=[];}
  });
  if(row_btns.length>0)buttons.push(row_btns);
  buttons.push([{text:"◀️ Назад",callback_data:"admin_menu"}]);
  tgSend(cid,"⚠️ Выберите резидента:",{reply_markup:JSON.stringify({inline_keyboard:buttons})});
}

function _sendFineStep2(cid,resName){
  var buttons=[
    [{text:"Не сдан отчёт. 10 000",callback_data:"fine_type_отчет_10000_"+resName}],
    [{text:"Опоздание. 10 000",callback_data:"fine_type_опоздание_10000_"+resName}],
    [{text:"Цена слова. 100 000",callback_data:"fine_type_слово_100000_"+resName}],
    [{text:"Пропуск. 10 000",callback_data:"fine_type_пропуск_10000_"+resName}],
    [{text:"Другое",callback_data:"fine_type_другое_0_"+resName}],
    [{text:"◀️ Назад",callback_data:"fine_step1"}]
  ];
  tgSend(cid,"⚠️ "+resName+". выберите тип штрафа:",{reply_markup:JSON.stringify({inline_keyboard:buttons})});
}

// ── Пошаговое расписание ──────────────────────────────────────────────────
function _sendSchedStep1(cid){
  var names=_getCachedResidents();
  var buttons=[];var row_btns=[];
  names.forEach(function(nm){
    row_btns.push({text:nm.split(" ")[0],callback_data:"sched_res_"+nm});
    if(row_btns.length===3){buttons.push(row_btns);row_btns=[];}
  });
  if(row_btns.length>0)buttons.push(row_btns);
  buttons.push([{text:"◀️ Назад",callback_data:"admin_menu"}]);
  tgSend(cid,"🗓 Выберите резидента:",{reply_markup:JSON.stringify({inline_keyboard:buttons})});
}

function _sendSchedStep2(cid,resName){
  // Даты на 14 дней вперёд
  var buttons=[];var row_btns=[];
  var today=new Date();
  for(var d=0;d<14;d++){
    var dt=new Date(today.getTime()+d*24*60*60*1000);
    var dStr=Utilities.formatDate(dt,"Asia/Almaty","dd.MM");
    var dVal=Utilities.formatDate(dt,"Asia/Almaty","dd.MM.yyyy");
    row_btns.push({text:dStr,callback_data:"sched_date_"+dVal+"_"+resName});
    if(row_btns.length===4){buttons.push(row_btns);row_btns=[];}
  }
  if(row_btns.length>0)buttons.push(row_btns);
  buttons.push([{text:"◀️ Назад",callback_data:"sched_step1"}]);
  tgSend(cid,"🗓 "+resName+". выберите дату:",{reply_markup:JSON.stringify({inline_keyboard:buttons})});
}

function _sendSchedStep3(cid,date,resName){
  var times=["09:00","10:00","11:00","12:00","13:00","14:00","15:00","16:00","17:00","18:00","19:00","20:00"];
  var buttons=[];var row_btns=[];
  times.forEach(function(t){
    row_btns.push({text:t,callback_data:"sched_time_"+t+"_"+date+"_"+resName});
    if(row_btns.length===4){buttons.push(row_btns);row_btns=[];}
  });
  if(row_btns.length>0)buttons.push(row_btns);
  buttons.push([{text:"◀️ Назад",callback_data:"sched_step1"}]);
  tgSend(cid,"🗓 "+resName+" "+date+". выберите время:",{reply_markup:JSON.stringify({inline_keyboard:buttons})});
}

// ── Пошаговый приход/расход ───────────────────────────────────────────────
function _sendPaymentStep1(cid){
  tgSend(cid,"💰 Приход или расход?",{reply_markup:JSON.stringify({inline_keyboard:[
    [{text:"📈 Приход",callback_data:"pay_type_income"},{text:"📉 Расход",callback_data:"pay_type_expense"}],
    [{text:"◀️ Назад",callback_data:"admin_menu"}]
  ]})});
}

function _sendPaymentStep2(cid,type){
  if(type==="income"){
    tgSend(cid,"💰 Выберите источник:",{reply_markup:JSON.stringify({inline_keyboard:[
      [{text:"БХ Трекинг продление",callback_data:"pay_src_БХ Трекинг продление_income"}],
      [{text:"БХ Трекинг год",callback_data:"pay_src_БХ Трекинг год_income"}],
      [{text:"БХ Штраф",callback_data:"pay_src_БХ Штраф_income"}],
      [{text:"БХ Экспресс разбор",callback_data:"pay_src_БХ Экспресс разбор_income"}],
      [{text:"БХ МК/Завтрак",callback_data:"pay_src_БХ МК/Завтрак_income"}],
      [{text:"Прочие доходы",callback_data:"pay_src_Прочие доходы_income"}],
      [{text:"◀️ Назад",callback_data:"payment_step1"}]
    ]})});
  } else {
    tgSend(cid,"💸 Выберите категорию расхода:",{reply_markup:JSON.stringify({inline_keyboard:[
      [{text:"SMM",callback_data:"pay_src_SMM_expense"},{text:"Маркетинг",callback_data:"pay_src_Маркет.бюджет_expense"}],
      [{text:"Офис",callback_data:"pay_src_Офис_expense"},{text:"Зарплата",callback_data:"pay_src_Ассистент_expense"}],
      [{text:"CRM",callback_data:"pay_src_CRM_expense"},{text:"IT",callback_data:"pay_src_IT_expense"}],
      [{text:"Прочие",callback_data:"pay_src_Прочие расходы:_expense"}],
      [{text:"◀️ Назад",callback_data:"payment_step1"}]
    ]})});
  }
}


function _sendTodayReports(cid){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsL=ss.getSheetByName("Лог отчётов");
  if(!wsR){tgSend(cid,"Лист резидентов не найден");return;}
  var today=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy");

  // Кто сдал отчёт сегодня (по Chat ID из лога)
  var submitted={};
  if(wsL&&wsL.getLastRow()>1){
    var ll=wsL.getLastRow();
    var logData=wsL.getRange(2,1,ll-1,8).getValues();
    logData.forEach(function(row){
      if(!row[0]||!(row[0] instanceof Date))return;
      // Отчёт после полуночи не засчитывается ни за какой день
      if(String(row[7]||"").trim()==="Да")return;
      var dStr=Utilities.formatDate(new Date(row[0]),"Asia/Almaty","dd.MM.yyyy");
      if(dStr===today){
        var uid=String(row[5]||"");
        if(uid)submitted[uid]={name:row[2],time:Utilities.formatDate(new Date(row[0]),"Asia/Almaty","HH:mm")};
      }
    });
  }

  var lr=wsR.getLastRow();
  var done=[],pending=[];
  if(lr>=3){
    var resData=wsR.getRange(3, 2, lr-2, 20).getValues();
    resData.forEach(function(row){
      var name=row[0];if(!name)return;
      if(row[RI.former]==="Да")return; // бывший
      if(row[RI.chat]==="Да")return; // исключение
      var chatId=String(row[RI.chat]||"");
      if(chatId&&submitted[chatId]){
        done.push({name:name,time:submitted[chatId].time});
      } else {
        pending.push(name);
      }
    });
  }

  var msg="📋 Отчёты на "+today+"\n\n";
  msg+="✅ Сдали ("+done.length+"):\n";
  if(done.length>0){
    done.forEach(function(d){msg+="• "+d.name+" ("+d.time+")\n";});
  } else {
    msg+="(пока никто)\n";
  }
  msg+="\n⏳ Ещё не сдали ("+pending.length+"):\n";
  if(pending.length>0){
    pending.forEach(function(n){msg+="• "+n+"\n";});
  } else {
    msg+="✅ Все сдали!\n";
  }
  tgSend(cid,msg,{reply_markup:JSON.stringify({inline_keyboard:[
    [{text:"🔄 Обновить",callback_data:"admin_today_reports"}],
    [{text:"◀️ Меню",callback_data:"admin_menu"}]
  ]})});
}

function _sendDebetInfo(cid){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsPL=ss.getSheetByName("PL");
  var wsF=ss.getSheetByName("Штрафы");
  var curM=new Date().getMonth()+1;
  var col=1+curM;
  var netProfit=0,residual=0;
  if(wsPL){
    netProfit=Number(wsPL.getRange(33,col).getValue())||0; // ЧИСТАЯ ПРИБЫЛЬ
    residual=Number(wsPL.getRange(35,col).getValue())||0;  // ОСТАТОК
  }
  var unpaidFines=0,unpaidCount=0;
  if(wsF){
    var lf=wsF.getLastRow();
    if(lf>=3){
      var finesData=wsF.getRange(3,2,lf-2,5).getValues();
      finesData.forEach(function(row){
        if(row[4]==="Не оплатил"){
          unpaidFines+=Number(row[2])||0;
          unpaidCount++;
        }
      });
    }
  }
  var mNames=["","Январь","Февраль","Март","Апрель","Май","Июнь","Июль","Август","Сентябрь","Октябрь","Ноябрь","Декабрь"];
  var msg="📊 Дебет. "+mNames[curM]+"\n\n"+
    "💵 Чистая прибыль: "+netProfit.toLocaleString()+" тг\n"+
    "💰 Остаток: "+residual.toLocaleString()+" тг\n"+
    "⚠️ Штрафов к получению: "+unpaidFines.toLocaleString()+" тг ("+unpaidCount+" шт)";
  tgSend(cid,msg,{reply_markup:JSON.stringify({inline_keyboard:[[{text:"◀️ Меню",callback_data:"admin_menu"}]]})});
}

function _sendSchedule10Days(cid){
  // Читаем напрямую из таблицы (не из кэша. кэш может устареть)
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");
  var msg="📅 Расписание на месяц:\n\n";
  if(!ws||ws.getLastRow()<3){
    tgSend(cid,msg+"Встреч нет",{reply_markup:JSON.stringify({inline_keyboard:[[{text:"◀️ Меню",callback_data:"admin_menu"}]]})});
    return;
  }
  var now=new Date();
  var todayStart=new Date(now.getFullYear(),now.getMonth(),now.getDate(),0,0,0);
  var monthEnd=new Date(now.getFullYear(),now.getMonth()+1,0,23,59,59);
  var lr=ws.getLastRow();
  var data=ws.getRange(3, 1, lr-2, 21).getValues();
  var found=0;
  data.forEach(function(row){
    var res=row[1],dt=row[2],tm=row[3],link=row[5];
    if(!res||!dt||!(dt instanceof Date))return;
    // Показываем сегодня и до конца месяца (не отсекаем по 00:00 today)
    if(dt<todayStart||dt>monthEnd)return;
    var tmStr=tm instanceof Date?Utilities.formatDate(tm,"Asia/Almaty","HH:mm"):"";
    msg+="📌 "+Utilities.formatDate(dt,"Asia/Almaty","dd.MM")+(tmStr?" "+tmStr:"")+". "+res;
    if(link&&link.indexOf("meet")>=0)msg+="\n🔗 "+link;
    else if(link)msg+="\n📍 "+link;
    msg+="\n";
    found++;
  });
  if(!found)msg+="Встреч нет";
  tgSend(cid,msg,{reply_markup:JSON.stringify({inline_keyboard:[[{text:"◀️ Меню",callback_data:"admin_menu"}]]})});
}

function _check3DaysMissing(ss,activeList,todayRep){
  var wsL=ss.getSheetByName("Лог отчётов");
  if(!wsL)return;
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  activeList.forEach(function(name){
    var f=name.split(" ")[0].toLowerCase();
    // Проверяем последние 3 дня
    var missed=0;
    for(var d=1;d<=3;d++){
      var chk=new Date();chk.setDate(chk.getDate()-d);
      var chkStr=Utilities.formatDate(chk,"Asia/Almaty","dd.MM.yyyy");
      var ll=wsL.getLastRow();
      var found=false;
      for(var r=2;r<=ll;r++){
        var ld=wsL.getRange(r,1).getValue();
        if(!ld)continue;
        var ldStr=Utilities.formatDate(new Date(ld),"Asia/Almaty","dd.MM.yyyy");
        if(ldStr===chkStr){
          var nm=(wsL.getRange(r,3).getValue()||"").toLowerCase();
          if(nm.indexOf(f)>=0){found=true;break;}
        }
      }
      if(!found)missed++;
    }
    if(missed>=3){
      var chatId=_getChatId(name);
      if(chatId){
        var msg=getText("missing_3days",{"имя":name.split(" ")[0]})||
          name.split(" ")[0]+", мы заметили что вас не было 3 дня. Всё в порядке? Если нужна помощь. мы всегда рядом!";
        tgSend(chatId,msg);
      }
    }
  });
}

function _getChatId(name){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("BS - резиденты дебет");
    var lr=ws.getLastRow();
    for(var r=3;r<=lr;r++){
      if(ws.getRange(r,2).getValue()===name)return String(ws.getRange(r,14).getValue()||"");
    }
  }catch(e){}
  return "";
}


function _checkReportQuality(chatId,msgId,text,name,tid){
  // Ищем раздел "сделал/отчёт за день". после слов "сегодня","отчёт","сделал","выполнил"
  var lower=text.toLowerCase();
  var reportStart=Math.max(
    lower.indexOf("отчёт за день"),
    lower.indexOf("сегодня"),
    lower.indexOf("сделал"),
    lower.indexOf("выполнил"),
    lower.indexOf("результат")
  );
  var reportPart=reportStart>=0?text.substring(reportStart):text;
  if(reportPart.length<150&&text.length<300){
    // Отвечаем в группу
    try{
      var payload={
        chat_id:chatId,
        message_thread_id:parseInt(tid),
        reply_to_message_id:msgId,
        text:name.split(" ")[0]+", отчёт слишком короткий. Опишите подробнее что сделали сегодня. минимум 150 символов в разделе результатов."
      };
      UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
        method:"post",contentType:"application/json",
        payload:JSON.stringify(payload),muteHttpExceptions:true
      });
    }catch(e){Logger.log("reportQuality: "+e);}
  }
}

function _logMsg(name,uname,text,uid,date,tid){
  var ss=SpreadsheetApp.openById(SS_ID),w=ss.getSheetByName("Лог отчётов");if(!w)return;
  var r=w.getLastRow()+1;
  w.getRange(r,1).setValue(date).setNumberFormat("DD.MM.YYYY");w.getRange(r,2).setValue(uname);
  w.getRange(r,3).setValue(name);w.getRange(r,4).setValue(text);
  w.getRange(r,5).setValue(date.getTime());w.getRange(r,6).setValue(uid);w.getRange(r,7).setValue(tid||"");
}

// ═══════════════════════════════════════════════════════════════════════════
// TELEGRAM BOT
// ═══════════════════════════════════════════════════════════════════════════

function isAdmin(cid){
  return ADMIN_IDS.indexOf(String(cid))>=0;
}

var CHANNEL_ID = "@bsurgery_kz";
var _reqChatId = "";

function doGet(e){
  try{
    if(e && e.parameter && e.parameter.action === "bsVersion")
      return ContentService.createTextOutput(JSON.stringify({version: BS_VERSION}))
        .setMimeType(ContentService.MimeType.JSON);
  }catch(vE){}
  // Auto-fix webhook УБРАН. он сам ломал webhook

  // Mini App API
  var action=e&&e.parameter?e.parameter.action:"";
  // Приложение ходит только через сервер: чужие и старые вызовы не принимаем
  if(action && bsServerOwns("app_gateway") && !bsAppSigOk(e.parameter, action)) return bsAppRefused();
  // Данные клуба ведёт сервер: запись в таблицу из приложения была бы потеряна
  if(action && BS_CLUB_WRITE_ACTIONS.indexOf(action) >= 0 && bsServerIsMaster()){
    return ContentService.createTextOutput(JSON.stringify({error: "Данные клуба теперь ведутся на платформе. Обновите приложение."}))
      .setMimeType(ContentService.MimeType.JSON);
  }
  if(action){
    var output=ContentService.createTextOutput();
    output.setMimeType(ContentService.MimeType.JSON);
    var result={};
    try{
      if(action==="getBotCache"){
        var forceFresh = String(e.parameter.fresh||"")==="1";
        if(forceFresh){
          _invalidateBundleCache();
          _reqChatId = String(e.parameter.chatId||"");
          result=_getMiniAppData();
        } else {
          result=_getMiniAppDataCached(String(e.parameter.chatId||""));
        }
      }
      else if(action==="addFine"){result=_miniAddFine(e.parameter);_invalidateBSCaches();}
      else if(action==="addSchedule"){result=_miniAddSchedule(e.parameter);_invalidateBSCaches();}
      else if(action==="addPayment"){
        result=_miniAddPayment(e.parameter);
        _invalidateBSCaches();
      }
      else if(action==="saveProfile"){result=_miniSaveProfile(e.parameter);}
      else if(action==="confirmMeeting"){result=_miniConfirmMeeting(e.parameter);}
      else if(action==="createEvent"){result=_miniCreateEvent(e.parameter);}
      else if(action==="getTodos"){result=_miniGetTodos(e.parameter);}
      else if(action==="generateIdeas"){result=_miniGenerateIdeas(e.parameter);}
      else if(action==="getLeadmagnetsList"){result=_miniGetLeadmagnetsList(e.parameter);}
      else if(action==="updateLeadmagnet"){result=_miniUpdateLeadmagnet(e.parameter);}
      else if(action==="swapLeadmagnetFiles"){result=_miniSwapLeadmagnetFiles(e.parameter);}
      else if(action==="banFromChannel"){result=_miniBanFromChannel(e.parameter);}
      else if(action==="setPartner"){result=_miniSetPartner(e.parameter);}
      else if(action==="getCustdevResponses"){result=_miniGetCustdevResponses(e.parameter);}
      else if(action==="checkChannelMembership"){result=_miniCheckChannelMembership(e.parameter);}
      else if(action==="addCustdevResponse"){result=_miniAddCustdevResponse(e.parameter);}
      else if(action==="runNPS"){result=_miniRunNPS(e.parameter);}
      else if(action==="saveWheel"){result=_miniSaveWheel(e.parameter);_invalidateBSCaches();}
      else if(action==="getWheel"){result=_miniGetWheel(e.parameter);}
      else if(action==="saveWheelAxes"){result=_miniSaveWheelAxes(e.parameter);}
      else if(action==="getWheelSummary"){result=_miniGetWheelSummary(e.parameter);}
      else if(action==="addResTask"){result=_miniAddResTask(e.parameter);_invalidateBSCaches();}
      else if(action==="getResTasks"){result=_miniGetResTasks(e.parameter);}
      else if(action==="setResTaskStatus"){result=_miniSetResTaskStatus(e.parameter);_invalidateBSCaches();}
      else if(action==="addOwnTask"){result=_miniAddOwnTask(e.parameter);_invalidateBSCaches();}
      else if(action==="editOwnTask"){result=_miniEditOwnTask(e.parameter);_invalidateBSCaches();}
      else if(action==="deleteOwnTask"){result=_miniDeleteOwnTask(e.parameter);_invalidateBSCaches();}
      else if(action==="saveProblem"){result=_miniSaveProblem(e.parameter);}
      else if(action==="getProblems"){result=_miniGetProblems(e.parameter);}
      else if(action==="getContentPlan"){result=_miniGetContentPlan(e.parameter);}
      else if(action==="updateContentPost"){result=_miniUpdateContentPost(e.parameter);}
      else if(action==="deleteContentPost"){result=_miniDeleteContentPost(e.parameter);}
      else if(action==="addContentPost"){result=_miniAddContentPost(e.parameter);}
      else if(action==="getSmm"){result=_miniGetSmm(e.parameter);}
      else if(action==="generateSmm"){result=_miniGenerateSmm(e.parameter);}
      else if(action==="analyzeWheel"){result=_miniAnalyzeWheel(e.parameter);}
      else if(action==="saveSmm"){result=_miniSaveSmm(e.parameter);}
      else if(action==="deleteSmm"){result=_miniDeleteSmm(e.parameter);}
      else if(action==="publishSmm"){result=publishSmmToTelegram(e.parameter);}
      else if(action==="publishCarousel"){
        try{
          var urls=JSON.parse(e.parameter.urls||"[]");
          result=postCarouselToThreads(urls, e.parameter.caption||"");
        }catch(pcE){ result={ok:false, error:String(pcE)}; }
      }
      else if(action==="setMeetings"){result=_miniSetMeetings(e.parameter);_invalidateBSCaches();}
      else if(action==="setResidentField"){result=_miniSetResidentField(e.parameter);_invalidateBSCaches();}
      else if(action==="renewMeetings"){result=_miniRenewMeetings(e.parameter);_invalidateBSCaches();}
      else if(action==="addLead"){
        result=addLead({
          name:e.parameter.name, phone:e.parameter.phone, telegram:e.parameter.telegram,
          source:e.parameter.source, campaign:e.parameter.campaign,
          niche:e.parameter.niche, revenue:e.parameter.revenue,
          request:e.parameter.request, comment:e.parameter.comment
        });
      }
      else if(action==="getLeads"){result=_miniGetLeads(e.parameter);}
      else if(action==="setLeadStatus"){result=_miniSetLeadStatus(e.parameter);}
      else if(action==="requestPhone"){
        try{
          requestPhoneFromLead(String(e.parameter.chatId||""), String(e.parameter.reason||""));
          result={ok:true};
        }catch(rpE){ result={error:String(rpE)}; }
      }
      else if(action==="setProblemStatus"){result=_miniSetProblemStatus(e.parameter);}
      else if(action==="deleteProblem"){result=_miniDeleteProblem(e.parameter);}
      else if(action==="saveProfit"){result=_miniSaveProfit(e.parameter);_invalidateBSCaches();}
      else if(action==="getProfit"){result=_miniGetProfit(e.parameter);}
      else if(action==="checkChannelMembership"){result=_miniCheckChannelMembership();}
      else if(action==="fixFinesHeader"){_fixFinesHeader(); result={ok:true};}
      else if(action==="testLeadmagnet"){result=_miniTestLeadmagnet(e.parameter);}
      else if(action==="requestLeadmagnet"){result=_miniRequestLeadmagnet(e.parameter);}
      else if(action==="submitDiagnosticRequest"){result=_miniSubmitDiagnostic(e.parameter);}
      else if(action==="saveDiagnosticResult"){result=_miniSaveDiagnostic(e.parameter);}
      else if(action==="generateBatch"){result=_miniGenerateBatch(e.parameter);}
      else if(action==="generatePost"){result=_miniGeneratePost(e.parameter);}
      else if(action==="publishToChannel"){result=_miniPublishPost(e.parameter);}
      else if(action==="approveResident"){result=_miniApproveResident(e.parameter);}
      else if(action==="getPendingResidents"){result=_miniGetPendingResidents(e.parameter);}
      else if(action==="addTodo"){result=_miniAddTodo(e.parameter);}
      else if(action==="toggleTodo"){result=_miniToggleTodo(e.parameter);}
      else if(action==="updateTodo"){result=_miniUpdateTodo(e.parameter);}
      else if(action==="deleteTodo"){result=_miniDeleteTodo(e.parameter);}
      else if(action==="addResident"){result=_miniAddResident(e.parameter);_invalidateBSCaches();}
      else if(action==="deleteSchedule"){result=_miniDeleteSchedule(e.parameter);_invalidateBSCaches();}
      else if(action==="saveAvatar"){result=_miniSaveAvatar(e.parameter);}
      else if(action==="updateMeeting"){result=_miniUpdateMeeting(e.parameter);_invalidateBSCaches();}
      else if(action==="addOfflineGroup"){result=_miniAddOfflineGroup(e.parameter);_invalidateBSCaches();}
      else if(action==="updateFine"){result=_miniUpdateFine(e.parameter);_invalidateBSCaches();}
      else if(action==="deleteFine"){result=_miniDeleteFine(e.parameter);_invalidateBSCaches();}
      else if(action==="markAttendance"){result=_miniMarkAttendance(e.parameter);_invalidateBSCaches();}
      else if(action==="getOfflineResidents"){result=_miniGetOfflineResidents();}
      else if(action==="getSubscribers"){result=_miniGetSubscribers();}
      else if(action==="getMonthlyPL"){result=_miniGetMonthlyPL();}
      else if(action==="checkUserRole"){result=_miniCheckUserRole(e.parameter);}
      else if(action==="requestResident"){result=_miniRequestResident(e.parameter);}
      else if(action==="updateSubscriber"){result=_miniUpdateSubscriber(e.parameter);}
      else if(action==="convertToResident"){result=_miniConvertToResident(e.parameter);}
      else{result={error:"Unknown action"};}
    }catch(ae){result={error:String(ae)};}
    // CORS headers
    output.setContent(JSON.stringify(result));
    return output;
  }

  return ContentService.createTextOutput("BS Bot OK. "+new Date().toString());
}

function _invalidateBundleCache(){
  // Сброс кеша пакета: вызывается после любого изменения данных
  try{
    var cache = CacheService.getScriptCache();
    var keysToRemove = ["bundle_meta_all"];
    for(var i = 0; i < 12; i++) keysToRemove.push("bundle_all_" + i);
    cache.removeAll(keysToRemove);
    // Персональные пакеты чистим по списку известных chatId
    var props = PropertiesService.getScriptProperties();
    var known = props.getProperty("BUNDLE_KEYS") || "";
    known.split(",").forEach(function(cid){
      if(!cid) return;
      var ks = ["bundle_meta_" + cid];
      for(var j = 0; j < 12; j++) ks.push("bundle_" + cid + "_" + j);
      try{ cache.removeAll(ks); }catch(e){}
    });
  }catch(e){ Logger.log("_invalidateBundleCache: " + e); }
}

function _invalidateBSCaches(){
  try{ _invalidateBundleCache(); }catch(e){}
  // Сброс всех кешей после изменения данных. чтобы приложение сразу видело свежее
  try{
    var c=CacheService.getScriptCache();
    c.remove("all_streaks");
    c.remove("miniapp_url_base");
    // Кеши ролей и стриков по именам чистятся своим TTL (60 сек / 30 мин)
  }catch(e){}
}

function warmupBundleCache(){
  // Прогрев: раз в 5 минут пакет пересобирается заранее, чтобы пользователь
  // всегда попадал в готовый кеш и не ждал сборки
  try{
    _invalidateBundleCache();
    _getMiniAppDataCached("");
    Logger.log("warmupBundleCache: пакет обновлён");
  }catch(e){ Logger.log("warmupBundleCache: " + e); }
}

function _tariffToMonths(tariffSum){
  // Длительность одного оплаченного цикла по сумме тарифа
  var s = Number(tariffSum) || 0;
  if(s >= 1000000) return 12;   // годовой тариф: 1 млн и выше
  if(s >= 400000)  return 3;    // квартальный
  if(s > 0)        return 1;    // помесячная оплата
  return 3;                     // по умолчанию квартал
}

function _calcNextRenewal(startDate, cyclesPaid, tariffSum){
  // Дата, до которой оплачено: дата входа + циклы, умноженные на длительность тарифа
  try{
    if(!(startDate instanceof Date) || startDate.getFullYear() < 2000) return "";
    var months = _tariffToMonths(tariffSum) * (Number(cyclesPaid) || 1);
    var d = new Date(startDate.getTime());
    d.setMonth(d.getMonth() + months);
    return Utilities.formatDate(d, "Asia/Almaty", "dd.MM.yyyy");
  }catch(e){ return ""; }
}

function checkRenewals(){
  // Напоминание о продлении за 14 и за 3 дня
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("BS - резиденты дебет");
    if(!ws) return;
    var lr = ws.getLastRow();
    if(lr < 3) return;

    var data = ws.getRange(3, 1, lr-2, 21).getValues();
    var today = new Date();
    today.setHours(0,0,0,0);
    var soon = [], urgent = [], overdue = [];

    data.forEach(function(row){
      var name = String(row[RA.name]||"").trim();
      if(!name) return;
      if(String(row[RA.former]||"").trim() === "Да") return;  // бывший
      if(String(row[RA.admin]||"").trim() === "Да") return;  // админ

      var startD = row[RA.date];
      if(!(startD instanceof Date)) return;
      var months = _tariffToMonths(Number(row[RA.tariff])||0) * (Number(row[RA.granted])||1);
      var due = new Date(startD.getTime());
      due.setMonth(due.getMonth() + months);
      due.setHours(0,0,0,0);

      var days = Math.round((due - today) / 86400000);
      var line = name + ": " + Utilities.formatDate(due, "Asia/Almaty", "dd.MM.yyyy");
      if(days < 0) overdue.push(line + " (просрочено на " + Math.abs(days) + " дн)");
      else if(days <= 3) urgent.push(line + " (через " + days + " дн)");
      else if(days <= 14) soon.push(line + " (через " + days + " дн)");
    });

    if(!soon.length && !urgent.length && !overdue.length) return;

    var msg = "🔄 Продления\n";
    if(overdue.length) msg += "\n❗️ Просрочены\n" + overdue.join("\n") + "\n";
    if(urgent.length)  msg += "\n⏰ На этой неделе\n" + urgent.join("\n") + "\n";
    if(soon.length)    msg += "\n📅 В ближайшие 2 недели\n" + soon.join("\n");

    _bsDigestSend("renew", msg);
    Logger.log(msg);
  }catch(e){ Logger.log("checkRenewals: " + e); }
}

function _getMiniAppDataCached(chatId){
  // Пакет данных кешируется на 90 секунд. Повторные открытия отдаются мгновенно,
  // без пересборки из таблиц. CacheService держит 100 КБ на ключ, поэтому режем на части
  var cache = CacheService.getScriptCache();
  var CHUNK = 90000;
  var metaKey = "bundle_meta_" + (chatId || "all");

  try{
    var meta = cache.get(metaKey);
    if(meta){
      var parts = parseInt(meta) || 0;
      var keys = [];
      for(var i = 0; i < parts; i++) keys.push("bundle_" + (chatId || "all") + "_" + i);
      var got = cache.getAll(keys);
      var joined = "";
      var complete = true;
      for(var j = 0; j < parts; j++){
        var piece = got["bundle_" + (chatId || "all") + "_" + j];
        if(piece === null || piece === undefined){ complete = false; break; }
        joined += piece;
      }
      if(complete && joined){
        var parsed = JSON.parse(joined);
        parsed._cached = true;
        return parsed;
      }
    }
  }catch(e){ Logger.log("bundle cache read: " + e); }

  // Запомним chatId чтобы потом чистить его персональный кеш
  try{
    if(chatId){
      var pr = PropertiesService.getScriptProperties();
      var lst = pr.getProperty("BUNDLE_KEYS") || "";
      if(lst.split(",").indexOf(String(chatId)) < 0){
        var arr = lst ? lst.split(",") : [];
        arr.push(String(chatId));
        if(arr.length > 60) arr = arr.slice(-60);
        pr.setProperty("BUNDLE_KEYS", arr.join(","));
      }
    }
  }catch(e){}

  // Кеша нет. собираем заново
  _reqChatId = String(chatId || "");
  var result = _getMiniAppData();

  try{
    var json = JSON.stringify(result);
    var parts2 = Math.ceil(json.length / CHUNK);
    if(parts2 <= 12){ // ~1 МБ максимум, дальше не кешируем
      var toPut = {};
      for(var k = 0; k < parts2; k++){
        toPut["bundle_" + (chatId || "all") + "_" + k] = json.substring(k * CHUNK, (k + 1) * CHUNK);
      }
      cache.putAll(toPut, 330);
      cache.put(metaKey, String(parts2), 330);
    }
  }catch(e){ Logger.log("bundle cache write: " + e); }

  return result;
}

function _getMiniAppData(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsF=ss.getSheetByName("Штрафы");
  var wsL=ss.getSheetByName("Лог отчётов");
  var wsPL=ss.getSheetByName("PL");
  var wsSch=ss.getSheetByName("Расписание");
  var result={residents:[],fines:[],logs:[],schedule:[],debet:{},totalDebt:0};

  // ═══ РЕЗИДЕНТЫ ═══
  if(wsR&&wsR.getLastRow()>2){
    var rData=wsR.getRange(3, 1, wsR.getLastRow()-2, 21).getValues();
    rData.forEach(function(row){
      var nm=String(row[RA.name]||"").trim();
      if(!nm)return;
      var dt = row[RA.date];
      result.residents.push({
        name:nm,
        paid:Number(row[RA.paid])||0,
        balance:Number(row[RA.rest])||0,
        debtRenew:Number(row[RA.renew])||0,
        fines:Number(row[RA.fine])||0,
        debt:Number(row[RA.total])||0,
        tariff:Number(row[RA.tariff])||0,
        dateIn:(dt instanceof Date)?Utilities.formatDate(dt,"Asia/Almaty","dd.MM.yyyy"):"",
        source:String(row[RA.source]||""),
        isFired:String(row[RA.former]||"").trim()==="Да",
        isExcluded:String(row[RA.except]||"").trim()==="Да",
        chatId:_normId(row[RA.chat]),
        format:String(row[RA.format]||"Офлайн").trim(),
        isAdmin:String(row[RA.admin]||"").trim()==="Да",
        meetingsDone:Number(row[RA.done])||0,
        meetingsGranted:Number(row[RA.granted])||0,
        meetingsLeft:(Number(row[RA.granted])||0)-(Number(row[RA.done])||0),
        cyclesPaid:Number(row[RA.granted])||1,
        cyclesUsed:Number(row[RA.done])||0,
        partner:String(row[RA.partner]||""),
        note:String(row[RA.notes]||""),
        months:Number(row[RA.months])||((dt instanceof Date)?
          Math.floor((new Date()-dt)/(30.44*24*3600*1000)):0),
        startDate:(dt instanceof Date)?(["января","февраля","марта","апреля","мая","июня",
          "июля","августа","сентября","октября","ноября","декабря"][dt.getMonth()]+
          " "+dt.getFullYear()):""
      });
    });
  }

  // Общий долг: без админов и бывших
  result.residents.forEach(function(r){
    if(r.isAdmin||r.isFired)return;
    result.totalDebt += r.debt;
  });

  // ═══ ШТРАФЫ ═══
  if(wsF&&wsF.getLastRow()>2){
    var fData=wsF.getRange(3,1,wsF.getLastRow()-2,6).getValues();
    fData.forEach(function(r,i){
      var nm=String(r[1]||"").trim();
      if(!nm)return;
      var d=r[4];
      result.fines.push({
        row:3+i, name:nm, kind:String(r[2]||""),
        amount:Number(r[3])||0,
        date:(d instanceof Date)?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d||""),
        status:String(r[5]||"Не оплатил")
      });
    });
  }

  // ═══ ОТЧЁТЫ за 30 дней ═══
  if(wsL&&wsL.getLastRow()>1){
    var since=new Date(); since.setDate(since.getDate()-30);
    var lData=wsL.getRange(2,1,wsL.getLastRow()-1,8).getValues();
    lData.forEach(function(r){
      if(!(r[0] instanceof Date)||r[0]<since)return;
      result.logs.push({
        date:Utilities.formatDate(r[0],"Asia/Almaty","dd.MM.yyyy"),
        time:Utilities.formatDate(r[0],"Asia/Almaty","HH:mm"),
        name:String(r[2]||""), text:String(r[3]||"").slice(0,200),
        chatId:_normId(r[5]), late:String(r[7]||"").trim()==="Да"
      });
    });
  }

  // ═══ РАСПИСАНИЕ: только сегодня и будущее ═══
  if(wsSch&&wsSch.getLastRow()>2){
    var today0=new Date(); today0.setHours(0,0,0,0);
    var sData=wsSch.getRange(3,1,wsSch.getLastRow()-2,8).getValues();
    sData.forEach(function(r,i){
      var nm=String(r[1]||"").trim();
      var d=r[2];
      if(!nm||!d)return;
      var tm=r[3];
      var timeStr="";
      if(tm instanceof Date) timeStr=Utilities.formatDate(tm,"Asia/Almaty","HH:mm");
      else if(tm) timeStr=String(tm);
      if(!timeStr && !r[4] && !r[5])return; // мусорная строка
      // Прошедшие встречи в приложение не отдаём
      if(d instanceof Date){
        var dd0=new Date(d); dd0.setHours(0,0,0,0);
        if(dd0 < today0) return;
      }
      var res=result.residents.filter(function(x){ return x.name===nm; })[0];
      result.schedule.push({
        row:3+i, res:nm, name:nm,
        date:(d instanceof Date)?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d),
        time:timeStr, place:String(r[4]||""), link:String(r[5]||""),
        format:res?res.format:"Офлайн",
        done:String(r[6]||"").indexOf("Проведена")>=0 || String(r[7]||"").trim()==="Да"
      });
    });
  }

  // ═══ ФИНАНСЫ ═══
  if(wsPL){
    var lrp=wsPL.getLastRow(), rProfit=-1, rKassa=-1, rDiv=-1;
    for(var pr=1; pr<=lrp; pr++){
      var pn=String(wsPL.getRange(pr,1).getValue()||"").trim();
      if(pn==="ЧИСТАЯ ПРИБЫЛЬ") rProfit=pr;
      else if(pn==="На кассе") rKassa=pr;
      else if(pn==="Дивиденды") rDiv=pr;
    }
    var curM=new Date().getMonth()+1, colM=1+curM;
    result.debet={
      netProfit: rProfit>0?(Number(wsPL.getRange(rProfit,colM).getValue())||0):0,
      residual: rKassa>0?(Number(wsPL.getRange(rKassa,colM).getValue())||0):0,
      dividends: rDiv>0?(Number(wsPL.getRange(rDiv,colM).getValue())||0):0,
      unpaidFines:0, unpaidCount:0
    };
    result.fines.forEach(function(f){
      if(f.status!=="Оплатил"){ result.debet.unpaidFines+=f.amount; result.debet.unpaidCount++; }
    });
  }

  // ═══ ОСТАЛЬНЫЕ РАЗДЕЛЫ ПРИЛОЖЕНИЯ ═══
  var chatIdReq = (typeof _reqChatId !== "undefined") ? _reqChatId : "";
  try{ result.monthlyPL = _miniGetMonthlyPL({}); }catch(e){ result.monthlyPL = null; }
  try{ result.wheelSummary = _miniGetWheelSummary({}); }catch(e){ result.wheelSummary = null; }
  try{ result.wheelAll = _miniGetWheelAll(); }catch(e){ result.wheelAll = null; }
  try{ result.leads = _miniGetLeads({}); }catch(e){ result.leads = {leads:[]}; }
  try{ result.problems = _miniGetProblems({}); }catch(e){ result.problems = {problems:[], total:0}; }
  try{ result.resTasks = _miniGetResTasks({}); }catch(e){ result.resTasks = {tasks:[]}; }
  try{ result.smm = _miniGetSmm({}); }catch(e){ result.smm = null; }
  try{ result.leadmagnets = _miniGetLeadmagnetsList({chatId: chatIdReq}); }catch(e){ result.leadmagnets = {items:[]}; }
  try{ result.contentPlan = _miniGetContentPlan({}); }catch(e){ result.contentPlan = {items:[]}; }

  // График чек-листов
  try{
    var lmList = (result.leadmagnets && result.leadmagnets.items) || [];
    var ready = lmList.filter(function(x){ return x.fileId; });
    var idxNow = parseInt(PropertiesService.getScriptProperties().getProperty("USEFUL_CL_IDX")||"0");
    var clSched = [];
    if(ready.length){
      var dd = new Date(); dd.setHours(11,0,0,0);
      while(dd.getDay() !== 1 || dd <= new Date()) dd.setDate(dd.getDate()+1);
      for(var s = 0; s < Math.min(ready.length, 6); s++){
        var it = ready[(idxNow + s) % ready.length];
        clSched.push({name: it.name || it.key, key: it.key,
          date: Utilities.formatDate(new Date(dd.getTime() + s*7*86400000), "Asia/Almaty", "dd.MM.yyyy"),
          next: s === 0});
      }
    }
    result.checklistSchedule = clSched;
  }catch(e){ result.checklistSchedule = []; }

  // Состоявшиеся встречи из Лога встреч, чтобы календарь не пустел
  try{
    var wsMLog = ss.getSheetByName("Лог встреч");
    var doneMeets = [];
    if(wsMLog && wsMLog.getLastRow() >= 2){
      wsMLog.getRange(2, 1, wsMLog.getLastRow()-1, 3).getValues().forEach(function(row){
        var nm = String(row[1]||"").trim();
        if(!nm || !(row[0] instanceof Date)) return;
        doneMeets.push({res: nm, name: nm,
          date: Utilities.formatDate(row[0], "Asia/Almaty", "dd.MM.yyyy"),
          time: String(row[2]||""), done: true});
      });
    }
    result.doneMeetings = doneMeets.slice(-300);
  }catch(e){ result.doneMeetings = []; }

  // Профили админов
  try{
    var wsProf = ss.getSheetByName("Профили");
    var adminProfiles = [];
    if(wsProf && wsProf.getLastRow() > 1){
      wsProf.getRange(2,1,wsProf.getLastRow()-1,7).getValues().forEach(function(pr){
        if(!pr[0]) return;
        adminProfiles.push({chatId:String(pr[0]), niche:String(pr[1]||""), bio:String(pr[2]||""),
          help:String(pr[3]||""), instagram:String(pr[4]||""), phone:String(pr[5]||""),
          avatar:String(pr[6]||"")});
      });
    }
    result.adminProfiles = adminProfiles;
  }catch(e){ result.adminProfiles = []; }

  try{
    result.myAvatar = (chatIdReq && typeof _getUserAvatarBase64 === "function")
      ? _getUserAvatarBase64(chatIdReq) : "";
  }catch(e){ result.myAvatar = ""; }

  result.ts=new Date().getTime();
  return result;
}

function _getUserAvatarBase64(chatId){
  // Аватар из Telegram в base64. Нужен для генерации картинок:
  // прямая ссылка на фото не отдаёт CORS-заголовки и ломает canvas
  try{
    if(!chatId) return "";
    var cache = CacheService.getScriptCache();
    var ck = "avatar_b64_" + chatId;
    var cached = cache.get(ck);
    if(cached) return cached === "none" ? "" : cached;

    var resp = UrlFetchApp.fetch("https://api.telegram.org/bot" + BOT_TOKEN +
      "/getUserProfilePhotos?user_id=" + chatId + "&limit=1", {muteHttpExceptions: true});
    var data = JSON.parse(resp.getContentText());
    if(!data.ok || !data.result || !data.result.photos || !data.result.photos.length){
      cache.put(ck, "none", 3600);
      return "";
    }
    var sizes = data.result.photos[0];
    var photo = sizes[sizes.length - 1]; // самое крупное
    var fResp = UrlFetchApp.fetch("https://api.telegram.org/bot" + BOT_TOKEN +
      "/getFile?file_id=" + photo.file_id, {muteHttpExceptions: true});
    var fData = JSON.parse(fResp.getContentText());
    if(!fData.ok || !fData.result || !fData.result.file_path){
      cache.put(ck, "none", 3600);
      return "";
    }
    var blob = UrlFetchApp.fetch("https://api.telegram.org/file/bot" + BOT_TOKEN + "/" + fData.result.file_path).getBlob();
    var b64 = "data:image/jpeg;base64," + Utilities.base64Encode(blob.getBytes());
    if(b64.length < 95000) cache.put(ck, b64, 21600);
    return b64;
  }catch(e){
    Logger.log("_getUserAvatarBase64: " + e);
    return "";
  }
}

function _miniGetWheelAll(){
  // Колёса всех резидентов одним проходом: {имя: {dnaAxes, lifeAxes, businesses, life}}
  try{
    var ws=_getWheelSheet();
    var lr=ws.getLastRow();
    if(lr<2)return {};
    var data=ws.getRange(2,1,lr-1,12).getValues();
    var byName={};
    for(var i=data.length-1;i>=0;i--){
      var nm=String(data[i][1]||"").trim();
      if(!nm)continue;
      var tp=String(data[i][2]||"").trim();
      var vals=[];
      for(var j=3;j<11;j++)vals.push(Number(data[i][j])||0);
      var dt=data[i][0] instanceof Date?Utilities.formatDate(data[i][0],"Asia/Almaty","dd.MM.yyyy HH:mm"):String(data[i][0]);
      if(!byName[nm]){
        var custom=_getResidentWheelAxes(nm);
        byName[nm]={
          dnaAxes:WHEEL_DNA_AXES,
          lifeAxes:custom?["Бизнес"].concat(custom):WHEEL_LIFE_AXES,
          bizMap:{}, bizOrder:[], life:[]
        };
      }
      var rec={date:dt, values:vals};
      if(tp==="ДНК"){
        var bn=String(data[i][11]||"Основной").trim()||"Основной";
        if(!byName[nm].bizMap[bn]){byName[nm].bizMap[bn]=[];byName[nm].bizOrder.push(bn);}
        byName[nm].bizMap[bn].push(rec);
      } else if(tp==="Личное"){
        byName[nm].life.push(rec);
      }
    }
    var out={};
    Object.keys(byName).forEach(function(nm){
      var b=byName[nm];
      out[nm]={
        dnaAxes:b.dnaAxes,
        lifeAxes:b.lifeAxes,
        businesses:b.bizOrder.map(function(bn){return {name:bn, rows:b.bizMap[bn]};}),
        dna:b.bizOrder.length?b.bizMap[b.bizOrder[0]]:[],
        life:b.life
      };
    });
    return out;
  }catch(e){Logger.log("_miniGetWheelAll: "+e);return {};}
}

function _miniAddFine(p){
  var name=p.name,type=p.type||"Штраф",amount=Number(p.amount)||10000;
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsF=ss.getSheetByName("Штрафы");if(!wsF)return{error:"No fines sheet"};
  // Находим следующую свободную строку
    var fr=_findFreeRow(wsF,3,2);
  wsF.getRange(fr,2).setValue(name);
  wsF.getRange(fr,3).setValue(type);
  wsF.getRange(fr,4).setValue(amount).setNumberFormat("#,##0");
  wsF.getRange(fr,5).setValue(new Date()).setNumberFormat("DD.MM.YYYY");
  wsF.getRange(fr,6).setValue("Не оплатил");
  // Выравнивание и списки сразу, чтобы строка не отличалась от соседних
  try{
    var dvT = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Не сдан отчёт","Цена слова","Опоздание","Нарушение правил","Пропуск посещения","Прочее"], true)
      .setAllowInvalid(false).build();
    var dvS = SpreadsheetApp.newDataValidation()
      .requireValueInList(["Не оплатил","Оплатил"], true)
      .setAllowInvalid(false).build();
    wsF.getRange(fr,3).setDataValidation(dvT);
    wsF.getRange(fr,6).setDataValidation(dvS);
    wsF.getRange(fr,1).setHorizontalAlignment("center");
    wsF.getRange(fr,2,1,2).setHorizontalAlignment("left");
    wsF.getRange(fr,4).setHorizontalAlignment("center").setNumberFormat("#,##0");
    wsF.getRange(fr,5,1,2).setHorizontalAlignment("center");
  }catch(e){}
  // Оформление как у остальных строк: без этого строка приходила Arial
  try{ _applyRowStyle(wsF, fr, Math.max(wsF.getLastColumn(),6), (fr % 2 === 0)); }catch(e){}
  _renumFines();recalcResidents();
  var chatId=_getChatId(name);
  if(chatId){
    var msg=name.split(" ")[0]+", выставлен штраф "+amount.toLocaleString()+" тг ("+type+").\nОплатить: "+KASPI_LINK;
    tgSend(chatId,msg);
  }
  try{syncVisitsOrder();}catch(e){}
  return{ok:true};
}


// ═══════════════════════════════════════════════════════════════════════════
// КАЛЕНДАРЬ. один вход, доступ выдаётся принудительно, а не только при создании
// ═══════════════════════════════════════════════════════════════════════════

// Почты команды: лист «Настройки», ключ team_emails (через запятую).
// Если ключа нет. берём проверенный список
function bsTeamEmails(){
  var list=[];
  try{
    var s=getText("team_emails",null);
    if(s) list=String(s).split(/[,;\s]+/).filter(function(x){return x.indexOf("@")>0;});
  }catch(e){}
  if(!list.length) list=["baraka.erniyazov@gmail.com","business.hirurgiya@gmail.com"];
  // Владельца календаря добавлять не надо. у него и так есть
  var owner="";
  try{ owner=Session.getEffectiveUser().getEmail()||""; }catch(e){}
  return list.filter(function(e){ return e && e.toLowerCase()!==String(owner).toLowerCase(); });
}

// Находит календарь BS, создаёт при необходимости и ВСЕГДА подтверждает доступ команде
function bsGetCal(){
  var props=PropertiesService.getScriptProperties();
  var cal=null;
  var id=props.getProperty("BS_CALENDAR_ID");
  if(id){ try{ cal=CalendarApp.getCalendarById(id); }catch(e){ cal=null; } }
  if(!cal){
    var owned=[]; try{ owned=CalendarApp.getAllOwnedCalendars(); }catch(e){}
    for(var i=0;i<owned.length;i++){
      if(owned[i].getName()==="Business Surgery Meetings"){ cal=owned[i]; break; }
    }
  }
  if(!cal){
    cal=CalendarApp.createCalendar("Business Surgery Meetings",{
      summary:"Встречи Business Surgery. трекинги и разборы",
      color:CalendarApp.Color.RED
    });
    props.setProperty("BS_CALENDAR_ID_FRESH","1");
  }
  if(!cal) throw new Error("календарь не получилось ни найти, ни создать");
  props.setProperty("BS_CALENDAR_ID",cal.getId());
  // Доступ подтверждаем не чаще раза в сутки, но подтверждаем всегда,
  // а не только в момент создания календаря. Раньше это была главная причина,
  // почему у партнёра расписание не появлялось
  try{
    var c=CacheService.getScriptCache();
    if(!c.get("calAclOk")){
      c.put("calAclOk","1",20*3600);
      bsApplyCalAcl(cal.getId());
    }
  }catch(e){ Logger.log("cal acl: "+e); }
  return cal;
}

function bsApplyCalAcl(calId){
  var res={ok:[],err:[]};
  bsTeamEmails().forEach(function(em){
    try{
      Calendar.Acl.insert({scope:{type:"user",value:em},role:"writer"}, calId);
      res.ok.push(em);
    }catch(e){
      // «уже есть такое правило». это не ошибка
      var m=String(e.message||e);
      if(/already|duplicate|exists/i.test(m)) res.ok.push(em+" (уже был)");
      else res.err.push(em+": "+m);
    }
  });
  return res;
}

// Ссылка «добавить этот календарь себе». Работает после выдачи доступа
function bsCalSubscribeLink(calId){
  return "https://calendar.google.com/calendar/u/0/r?cid="+encodeURIComponent(calId);
}

// Пункт меню: чинит доступ и рассылает ссылку основателям в Telegram
function bsFixCalendarAccess(){
  var out=[];
  var cal=null;
  try{ cal=bsGetCal(); }catch(e){
    var msg="❌ Календарь недоступен: "+e+
      "\n\nЧаще всего это значит, что в Apps Script не включена служба Calendar API.\n"+
      "Редактор скрипта → Службы (+) → Google Calendar API → Добавить.";
    try{ SpreadsheetApp.getUi().alert(msg); }catch(u){}
    return msg;
  }
  var calId=cal.getId();
  out.push("Календарь: "+cal.getName());
  out.push("ID: "+calId);

  var acl=bsApplyCalAcl(calId);
  if(acl.ok.length) out.push("\n✅ Доступ выдан:\n• "+acl.ok.join("\n• "));
  if(acl.err.length) out.push("\n❌ Не удалось:\n• "+acl.err.join("\n• "));

  // Сколько встреч впереди. если ноль, дело не в доступе, а в том что событий нет
  var ahead=0;
  try{
    var from=new Date(), to=new Date(); to.setDate(to.getDate()+30);
    ahead=cal.getEvents(from,to).length;
  }catch(e){}
  out.push("\nВстреч в календаре на ближайшие 30 дней: "+ahead);
  if(!ahead) out.push("Событий нет. Значит расписание в таблицу заводили без создания встречи в календаре.\nПункт меню BS → «Перенести расписание в календарь» это исправит.");

  var link=bsCalSubscribeLink(calId);
  out.push("\nСсылка «добавить календарь себе»:\n"+link);

  // Отправляем основателям личным сообщением, чтобы не пересылать вручную
  try{
    ADMIN_IDS.forEach(function(aid){
      tgSend(aid,"📅 Календарь Business Surgery\n\n"+
        "Открой ссылку с того же Google-аккаунта, на котором сидишь в календаре, и нажми «Добавить»:\n"+link+
        "\n\nЕсли Google скажет «нет доступа». значит почта в настройках другая. Напиши, какую поставить.");
    });
    out.push("\nСсылка отправлена основателям в Telegram.");
  }catch(e){}

  var text=out.join("\n");
  try{ SpreadsheetApp.getUi().alert(text); }catch(e){}
  return text;
}

// ═══ КАЛЕНДАРЬ ═══
// Офлайн-встреча это одно общее событие «Офлайн день BS», а не событие на каждого.
// Команда (bsTeamEmails) стоит гостем в каждом событии: встреча сама появляется
// в их личном календаре, подписываться на общий календарь не нужно

function bsOfflineNames(){
  var set={};
  try{
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws || ws.getLastRow()<3) return set;
    ws.getRange(3,2,ws.getLastRow()-2,20).getValues().forEach(function(r){
      var n=String(r[0]||"").trim();
      if(!n || r[RI.former]==="Да" || r[RI.except]==="Да") return;
      if(String(r[RI.format]||"Офлайн")==="Офлайн") set[n.toLowerCase()]=n;
    });
  }catch(e){ Logger.log("offlineNames: "+e); }
  return set;
}

function bsCalIso(d){
  return Utilities.formatDate(d,"Asia/Almaty","yyyy-MM-dd")+"T"+Utilities.formatDate(d,"Asia/Almaty","HH:mm:ss")+"+05:00";
}

function bsCalTeamGuests(){
  return bsTeamEmails().map(function(e){ return {email:e, responseStatus:"accepted"}; });
}

function bsCalOfflineDesc(names, addr, start, end){
  return "🎯 Офлайн день Business Surgery\n\n📍 "+addr+"\n⏱ "+
    Utilities.formatDate(start,"Asia/Almaty","HH:mm")+". "+Utilities.formatDate(end,"Asia/Almaty","HH:mm")+
    "\n\n👥 Резиденты ("+names.length+"):\n• "+names.join("\n• ")+
    "\n\n📋 Что нужно:\n• Цифры за 10 дней\n• Прогресс по плану\n• Главный вызов\n\nBusiness Surgery";
}

// Одно событие офлайн-дня на 5 часов со всеми именами в описании
function bsCalCreateOffline(calId, start, names, addr){
  var end=new Date(start.getTime()+5*3600000);
  return Calendar.Events.insert({
    summary:"Офлайн день BS. "+names.length+" резидентов",
    description:bsCalOfflineDesc(names, addr, start, end),
    location:addr,
    start:{dateTime:bsCalIso(start),timeZone:"Asia/Almaty"},
    end:{dateTime:bsCalIso(end),timeZone:"Asia/Almaty"},
    attendees:bsCalTeamGuests(),
    colorId:"11",
    reminders:{useDefault:false,overrides:[{method:"popup",minutes:60}]}
  }, calId, {sendUpdates:"none"});
}

function bsCalList(calId, from, to){
  var items=[], token=null, guard=0;
  do{
    var o={timeMin:from.toISOString(),timeMax:to.toISOString(),singleEvents:true,maxResults:250,showDeleted:false};
    if(token) o.pageToken=token;
    var r=Calendar.Events.list(calId,o)||{};
    (r.items||[]).forEach(function(ev){ if(ev && ev.status!=="cancelled" && ev.start && ev.start.dateTime) items.push(ev); });
    token=r.nextPageToken; guard++;
  }while(token && guard<20);
  return items;
}

function bsCalIsGroup(ev){ return /^Офлайн день/.test(String(ev.summary||"")); }
function bsCalPerson(ev){
  var m=String(ev.summary||"").match(/^Встреча BS\.\s*(.+)$/);
  return m ? m[1].trim() : "";
}
function bsCalHasMeet(ev){ return !!(ev.hangoutLink || ev.conferenceData); }

// Чинит календарь: дубли, офлайн по одному, команда в гостях. Повторный запуск ничего не меняет
function bsCalRepair(){
  var cal=bsGetCal(), calId=cal.getId();
  var now=new Date();
  var from=new Date(now.getTime()-30*86400000), to=new Date(now.getTime()+180*86400000);
  var evs=bsCalList(calId, from, to);
  var offline=bsOfflineNames();
  var addr=getText("offline_address",null)||"г.Алматы, Достык 44";
  var team=bsTeamEmails().map(function(e){return e.toLowerCase();});
  var gone={}, rep={dups:0, merged:[], created:0, shared:0};
  function drop(ev){
    if(gone[ev.id]) return;
    try{ Calendar.Events.remove(calId, ev.id, {sendUpdates:"none"}); gone[ev.id]=1; }
    catch(e){ Logger.log("calRepair remove: "+e); }
  }

  // 1. Точные дубли: то же название и то же время. Оставляем событие с Meet, иначе самое раннее
  var byKey={};
  evs.forEach(function(ev){
    var k=String(ev.summary||"").trim()+"|"+Date.parse(ev.start.dateTime);
    (byKey[k]=byKey[k]||[]).push(ev);
  });
  Object.keys(byKey).forEach(function(k){
    var g=byKey[k]; if(g.length<2) return;
    g.sort(function(x,y){
      var mx=bsCalHasMeet(x)?0:1, my=bsCalHasMeet(y)?0:1;
      if(mx!==my) return mx-my;
      return String(x.created||"").localeCompare(String(y.created||""));
    });
    g.slice(1).forEach(function(ev){ drop(ev); rep.dups++; });
  });

  // 2. Офлайн-резиденты отдельными событиями в одно время: заменяем одним офлайн-днём
  var byStart={};
  evs.forEach(function(ev){
    if(gone[ev.id]) return;
    var t=Date.parse(ev.start.dateTime);
    var slot=byStart[t]=byStart[t]||{groups:[],persons:[]};
    if(bsCalIsGroup(ev)) slot.groups.push(ev);
    else {
      var n=bsCalPerson(ev);
      if(n && offline[n.toLowerCase()] && !bsCalHasMeet(ev)) slot.persons.push({ev:ev,name:n});
    }
  });
  Object.keys(byStart).forEach(function(t){
    var slot=byStart[t];
    if(!slot.persons.length) return;
    if(slot.persons.length<2 && !slot.groups.length) return;   // одна личная офлайн-встреча остаётся как есть
    var names=slot.persons.map(function(p){return p.name;});
    if(!slot.groups.length){
      try{ var ne=bsCalCreateOffline(calId, new Date(Number(t)), names, addr); rep.created++; if(ne) evs.push(ne); }
      catch(e){ Logger.log("calRepair create: "+e); return; }
    }
    slot.persons.forEach(function(p){ drop(p.ev); });
    rep.merged.push(Utilities.formatDate(new Date(Number(t)),"Asia/Almaty","dd.MM HH:mm")+": "+names.join(", "));
  });

  // 3. Команда в гостях каждого события: так оно видно в личном календаре без подписки
  evs.forEach(function(ev){
    if(gone[ev.id] || !ev.id) return;
    var have=(ev.attendees||[]).map(function(a){return String(a.email||"").toLowerCase();});
    var miss=team.filter(function(e){return have.indexOf(e)<0;});
    if(!miss.length) return;
    var att=(ev.attendees||[]).slice();
    miss.forEach(function(e){ att.push({email:e, responseStatus:"accepted"}); });
    try{ Calendar.Events.patch({attendees:att}, calId, ev.id, {sendUpdates:"none"}); rep.shared++; }
    catch(e){ Logger.log("calRepair patch: "+e); }
  });
  try{ bsApplyCalAcl(calId); }catch(e){}
  rep.team=bsTeamEmails();
  return rep;
}

function bsCalRepairText(r){
  var t=["📅 Календарь приведён в порядок"];
  if(r.dups) t.push("Удалено дублей: "+r.dups);
  if(r.merged.length) t.push("Офлайн-резиденты собраны в один офлайн-день:\n• "+r.merged.join("\n• "));
  if(r.shared) t.push("Команда добавлена гостем во встречи: "+r.shared);
  t.push("\nВстречи сами видны в личном календаре у: "+r.team.join(", "));
  return t.join("\n");
}

function bsCalRepairMenu(){
  var t;
  try{ t=bsCalRepairText(bsCalRepair()); }catch(e){ t="❌ Календарь: "+e; }
  try{ SpreadsheetApp.getUi().alert(t); }catch(e){}
  return t;
}

// Раз в сутки само, без кнопок. Админам пишем, только если что-то исправили
function bsCalRepairDaily(){
  var props=PropertiesService.getScriptProperties();
  var day=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
  if(props.getProperty("BS_CALFIX_DAY")===day) return;
  props.setProperty("BS_CALFIX_DAY",day);
  var r=bsCalRepair();
  if(r.dups || r.merged.length || r.shared){ try{ bsSysNote(bsCalRepairText(r)); }catch(e){} }
}

// Переносит встречи из листа «Расписание» в календарь, для тех строк, где события нет.
// Офлайн-резиденты в одно время попадают в одно событие «Офлайн день BS»
function bsSyncScheduleToCalendar(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsS=ss.getSheetByName("Расписание");
  if(!wsS || wsS.getLastRow()<3){
    try{ SpreadsheetApp.getUi().alert("Лист «Расписание» пуст."); }catch(e){}
    return;
  }
  var cal;
  try{ cal=bsGetCal(); }catch(e){
    try{ SpreadsheetApp.getUi().alert("Календарь недоступен: "+e); }catch(u){}
    return;
  }
  var calId=cal.getId();
  var offline=bsOfflineNames();
  var addr=getText("offline_address",null)||"г.Алматы, Достык 44";
  var lr=wsS.getLastRow();
  var rows=wsS.getRange(3,1,lr-2,8).getValues();
  var made=[],skipped=[],offSlots={};
  function eventsNear(start){
    try{ return cal.getEvents(new Date(start.getTime()-30*60000), new Date(start.getTime()+90*60000)); }catch(e){ return []; }
  }
  rows.forEach(function(r){
    var name=String(r[1]||"").trim();
    var d=r[2], t=r[3];
    if(!name || !(d instanceof Date)) return;
    var hh=12, mm=0;
    if(t instanceof Date){ hh=t.getHours(); mm=t.getMinutes(); }
    else if(String(t).indexOf(":")>0){ var p=String(t).split(":"); hh=parseInt(p[0],10)||12; mm=parseInt(p[1],10)||0; }
    var start=new Date(d.getFullYear(),d.getMonth(),d.getDate(),hh,mm,0);
    var place=String(r[5]||r[4]||"").trim();
    var isOff=!!offline[name.toLowerCase()] && place.indexOf("meet.google")<0;
    if(isOff){
      var k=start.getTime();
      (offSlots[k]=offSlots[k]||[]).push(name);
      return;
    }
    var exists=eventsNear(start).some(function(ev){
      var tt=String(ev.getTitle()||"");
      var ds=""; try{ ds=String(ev.getDescription()||""); }catch(e){}
      return tt.indexOf(name)>=0 || ds.indexOf(name)>=0;
    });
    if(exists){ skipped.push(name); return; }
    var startISO=bsCalIso(start), endISO=bsCalIso(new Date(start.getTime()+3600000));
    try{
      Calendar.Events.insert({
        summary:"Встреча BS. "+name,
        description:"🎯 Трекинг Business Surgery\n\n👤 Резидент: "+name+
          (place?"\n📍 "+place:"")+"\n\nBusiness Surgery",
        location:place,
        start:{dateTime:startISO,timeZone:"Asia/Almaty"},
        end:{dateTime:endISO,timeZone:"Asia/Almaty"},
        attendees:bsCalTeamGuests(),
        colorId:"11"
      }, calId, {sendUpdates:"none"});
      made.push(name+" "+bsDayStr(start)+" "+String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0"));
    }catch(e){ skipped.push(name+" (ошибка: "+e+")"); }
  });
  Object.keys(offSlots).forEach(function(k){
    var start=new Date(Number(k)), names=offSlots[k];
    var near=eventsNear(start);
    var has=near.some(function(ev){ return /^Офлайн день/.test(String(ev.getTitle()||"")); });
    if(has){ skipped.push("офлайн-день "+bsDayStr(start)); return; }
    // Одна личная офлайн-встреча, уже внесённая по имени, не дублируется
    if(names.length===1 && near.some(function(ev){ return String(ev.getTitle()||"").indexOf(names[0])>=0; })){ skipped.push(names[0]); return; }
    try{ bsCalCreateOffline(calId, start, names, addr); made.push("Офлайн день "+bsDayStr(start)+": "+names.join(", ")); }
    catch(e){ skipped.push("офлайн-день "+bsDayStr(start)+" (ошибка: "+e+")"); }
  });
  var text="Перенос расписания в календарь\n\nСоздано: "+made.length+
    (made.length?"\n• "+made.join("\n• "):"")+
    "\n\nПропущено (уже есть или ошибка): "+skipped.length+
    (skipped.length?"\n• "+skipped.join("\n• "):"");
  try{ SpreadsheetApp.getUi().alert(text); }catch(e){}
  return text;
}

function shareBSCalendarToAll(){
  // Принудительно расшаривает календарь BS на всех нужных
  var calProps=PropertiesService.getScriptProperties();
  var bsCalId=calProps.getProperty("BS_CALENDAR_ID");
  var cal=null;
  if(bsCalId){
    try{cal=CalendarApp.getCalendarById(bsCalId);}catch(e){cal=null;}
  }
  if(!cal){
    var allCals=CalendarApp.getAllOwnedCalendars();
    for(var ci=0;ci<allCals.length;ci++){
      if(allCals[ci].getName()==="Business Surgery Meetings"){
        cal=allCals[ci];
        calProps.setProperty("BS_CALENDAR_ID",cal.getId());
        break;
      }
    }
  }
  if(!cal){
    SpreadsheetApp.getUi().alert("❌ Календарь Business Surgery Meetings не найден на этом аккаунте.\n\nСоздайте первую встречу через Mini App. календарь создастся автоматически.");
    return;
  }
  var calId=cal.getId();
  var emails=["baraka.erniyazov@gmail.com","business.hirurgiya@gmail.com"];
  var ok=[],err=[];
  emails.forEach(function(em){
    try{
      // Через Calendar Advanced API
      Calendar.Acl.insert({
        scope:{type:"user",value:em},
        role:"writer"
      }, calId);
      ok.push(em);
    }catch(e){
      err.push(em+": "+e.message);
    }
  });
  // Меняем цвет на красный
  try{
    Calendar.CalendarList.patch({
      backgroundColor:"#dc2127",
      foregroundColor:"#ffffff"
    }, calId, {colorRgbFormat:true})
  }catch(colorErr){Logger.log("colorErr: "+colorErr);}
  var msg="📅 Календарь: "+cal.getName()+"\n\n✅ Расшарено:\n"+ok.join("\n");
  if(err.length)msg+="\n\n❌ Ошибки:\n"+err.join("\n");
  SpreadsheetApp.getUi().alert(msg);
}


function _miniAddOfflineGroup(p){
  // Массовая офлайн встреча. для всех резидентов формат="Офлайн"
  var date=String(p.date||""), time=String(p.time||"");
  if(!date||!time)return{error:"No date/time"};
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsS=ss.getSheetByName("Расписание");
  if(!wsR||!wsS)return{error:"No sheets"};
  // Собираем офлайн-резидентов (не бывшие, не исключение)
  var lr=wsR.getLastRow();
  if(lr<3)return{error:"No residents"};
  var resData=wsR.getRange(3, 2, lr-2, 20).getValues();
  var offlineNames=[];
  resData.forEach(function(row){
    var name=row[0];
    if(!name)return;
    if(row[RI.former]==="Да")return; // K=Бывший (index 9)
    if(row[RI.except]==="Да")return; // L=Исключение (index 10)
    var fmt=String(row[RI.format]||"Офлайн"); // O=Формат (index 13)
    if(fmt==="Офлайн")offlineNames.push(name);
  });

  if(!offlineNames.length)return{error:"No offline residents"};

  // Парсим дату и время
  var dp=date.split(".");
  var year=parseInt(dp[2]||new Date().getFullYear());
  var month=parseInt(dp[1]);
  var day=parseInt(dp[0]);
  var dStr=year+"-"+String(month).padStart(2,"0")+"-"+String(day).padStart(2,"0");
  var dt=Utilities.parseDate(dStr+" 12:00:00","Asia/Almaty","yyyy-MM-dd HH:mm:ss");
  var tp=time.split(":");
  var hh=parseInt(tp[0])||12, mm=parseInt(tp[1])||0;
  var hhStr=String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
  var todayStr=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
  var evTimeValue=Utilities.parseDate(todayStr+" "+hhStr,"Asia/Almaty","yyyy-MM-dd HH:mm:ss");

  // Записываем встречу для каждого
  var offAddr=getText("offline_address",null)||"г.Алматы, Достык 44";
  var added=[];
  offlineNames.forEach(function(name){
    // Найти свободную строку
    var lrS=wsS.getLastRow();
    var newRow=-1;
    for(var r=3;r<=lrS;r++){
      if(!wsS.getRange(r,2).getValue()){newRow=r;break;}
    }
    if(newRow<0)newRow=Math.max(lrS+1,3);
    CacheService.getScriptCache().put("sched_meet_"+newRow+"_"+new Date().toDateString(),"1",3600);
    wsS.getRange(newRow,1).setValue(newRow-2);
    wsS.getRange(newRow,2).setValue(name);
    wsS.getRange(newRow,3).setValue(dt).setNumberFormat("DD.MM.YYYY");
    wsS.getRange(newRow,4).setValue(evTimeValue).setNumberFormat("HH:MM");
    wsS.getRange(newRow,6).setValue(offAddr);
    added.push(name);
    SpreadsheetApp.flush();
  });

  // Создаём ОДНО событие в календаре BS на 5 часов с всеми гостями
  try{
    var calProps=PropertiesService.getScriptProperties();
    var bsCalId=calProps.getProperty("BS_CALENDAR_ID");
    var cal=null;
    if(bsCalId){try{cal=CalendarApp.getCalendarById(bsCalId);}catch(e){}}
    if(!cal){
      var allCals=CalendarApp.getAllOwnedCalendars();
      for(var ci=0;ci<allCals.length;ci++){
        if(allCals[ci].getName()==="Business Surgery Meetings"){cal=allCals[ci];break;}
      }
    }
    if(cal){
      var calId=cal.getId();
      var startISO=Utilities.formatString("%04d-%02d-%02dT%02d:%02d:00+05:00",year,month,day,hh,mm);
      var endHour=hh+5;
      var endISO=Utilities.formatString("%04d-%02d-%02dT%02d:%02d:00+05:00",year,month,day,endHour,mm);
      var desc="🎯 Офлайн день Business Surgery\n\n📍 "+offAddr+"\n⏱ "+hhStr.substring(0,5)+". "+String(endHour).padStart(2,"0")+":"+String(mm).padStart(2,"0")+"\n\n👥 Резиденты ("+added.length+"):\n• "+added.join("\n• ")+"\n\n📋 Что нужно:\n• Цифры за 10 дней\n• Прогресс по плану\n• Главный вызов\n\n \nBusiness Surgery";
      // Гости. добавляем chatId для резидентов? Нет, только организаторы
      var eventResource={
        summary:"Офлайн день BS. "+added.length+" резидентов",
        description:desc,
        location:offAddr,
        start:{dateTime:startISO,timeZone:"Asia/Almaty"},
        end:{dateTime:endISO,timeZone:"Asia/Almaty"},
        attendees:[
          {email:"business.hirurgiya@gmail.com", responseStatus:"accepted"},
          {email:"baraka.erniyazov@gmail.com", responseStatus:"accepted"}
        ],
        colorId:"11",
        reminders:{
          useDefault:false,
          overrides:[
            {method:"popup", minutes:60},
            {method:"email", minutes:60}
          ]
        }
      };
      Calendar.Events.insert(eventResource,calId,{sendUpdates:"all"});
    }
  }catch(calErr){Logger.log("Offline cal: "+calErr);}

  // Уведомляем резидентов
  var importantMsg="📌 Офлайн день BS\n📅 "+date+"\n⏰ "+time+"\n📍 "+offAddr+"\n\n👥 Все офлайн-резиденты:\n• "+added.join("\n• ")+"\n\n⚠️ За опоздание штраф 10 000 тг";
  if(GROUP_CHAT_ID&&IMPORTANT_TOPIC_ID){
    try{
      UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
        method:"post",contentType:"application/json",
        payload:JSON.stringify({
          chat_id:GROUP_CHAT_ID,
          message_thread_id:parseInt(IMPORTANT_TOPIC_ID),
          text:importantMsg
        }),muteHttpExceptions:true
      });
    }catch(ie){Logger.log("offline group: "+ie);}
  }
  // Личные уведомления резидентам
  added.forEach(function(name){
    var chatId=_getChatId(name);
    if(chatId){
      tgSend(chatId,"📌 Офлайн встреча!\n📅 "+date+" "+time+"\n📍 "+offAddr,{
        reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("schedule")}}]]})
      });
    }
  });

  return{ok:true,count:added.length,names:added};
}


function _bsFindFineRow(ws, p){
  // Сначала по номеру строки из приложения (с проверкой имени), затем по данным
  var name=String(p.name||"").trim();
  var date=String(p.date||"").trim();
  var type=String(p.kind||p.type||"").trim();
  if(type==="undefined") type="";
  var amount=Number(p.amount)||0;
  var lr=ws.getLastRow();
  if(lr<3) return -1;
  var row=parseInt(p.row,10);
  if(row>=3 && row<=lr && String(ws.getRange(row,2).getValue()||"").trim()===name) return row;
  var data=ws.getRange(3, 2, lr-2, 5).getValues();
  for(var i=0;i<data.length;i++){
    if(String(data[i][0]||"").trim()!==name) continue;
    if(type && String(data[i][1]||"").trim()!==type) continue;
    if(amount && (Number(data[i][2])||0)!==amount) continue;
    var rDate=data[i][3];
    if(date){
      var rDateStr=(rDate instanceof Date)?Utilities.formatDate(rDate,"Asia/Almaty","dd.MM.yyyy"):String(rDate||"").trim();
      if(rDateStr && rDateStr!==date) continue;
    }
    return 3+i;
  }
  return -1;
}

function _miniUpdateFine(p){
  var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Штрафы");
  if(!ws) return{error:"Нет листа Штрафы"};
  var row=_bsFindFineRow(ws, p);
  if(row<0) return{error:"Штраф не найден"};
  ws.getRange(row,6).setValue(String(p.status||"Оплатил"));
  SpreadsheetApp.flush();
  try{ recalcResidents(); }catch(e){ Logger.log("fine upd recalc: "+e); }
  return{ok:true, row:row};
}

function _miniDeleteFine(p){
  var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Штрафы");
  if(!ws) return{error:"Нет листа Штрафы"};
  var row=_bsFindFineRow(ws, p);
  if(row<0) return{error:"Штраф не найден"};
  var name=String(ws.getRange(row,2).getValue()||"").trim();
  ws.deleteRow(row);
  SpreadsheetApp.flush();
  try{ _renumFines(); }catch(e){}
  try{ recalcResidents(); }catch(e){ Logger.log("fine del recalc: "+e); }
  try{ if(name) bsNotifyResident(name, "Штраф снят. Долг пересчитан"); }catch(nE){ Logger.log("notify fine del: " + nE); }
  return{ok:true, row:row};
}

function requestPhoneFromLead(chatId, reason){
  // Запрос телефона: Telegram сам подставляет номер из профиля, вводить не нужно
  try{
    var text = reason || "Чтобы отправить результат диагностики и связаться с вами, нужен номер телефона.\n\nНажмите кнопку ниже. номер подставится автоматически из вашего профиля Telegram.";
    tgSend(chatId, text, {
      reply_markup: JSON.stringify({
        keyboard: [[{text: "📱 Отправить мой номер", request_contact: true}]],
        resize_keyboard: true,
        one_time_keyboard: true
      })
    });
  }catch(e){ Logger.log("requestPhoneFromLead: " + e); }
}

function _savePhoneToSubscriber(chatId, phone, name){
  // Сохраняет телефон в лист Подписчики канала
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("Подписчики канала");
    if(!ws) return false;
    var lr = ws.getLastRow();
    var normalized = String(phone).replace(/[^0-9+]/g, "");
    if(normalized && normalized.indexOf("+") !== 0) normalized = "+" + normalized;

    // Колонка Телефон: ищем или создаём
    var headers = ws.getRange(1, 1, 1, Math.max(ws.getLastColumn(), 12)).getValues()[0];
    var phoneCol = -1;
    for(var h = 0; h < headers.length; h++){
      if(String(headers[h]).trim() === "Телефон"){ phoneCol = h + 1; break; }
    }
    if(phoneCol < 0){
      phoneCol = ws.getLastColumn() + 1;
      ws.getRange(1, phoneCol).setValue("Телефон").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    }

    for(var r = 2; r <= lr; r++){
      if(String(ws.getRange(r, 2).getValue() || "").trim() === String(chatId)){
        ws.getRange(r, phoneCol).setValue(normalized);
        return true;
      }
    }
    // Не нашли. добавляем новую строку
    _addSubscriber(chatId, "", name || "");
    var lr2 = ws.getLastRow();
    ws.getRange(lr2, phoneCol).setValue(normalized);
    return true;
  }catch(e){ Logger.log("_savePhoneToSubscriber: " + e); return false; }
}

function _addSubscriber(chatId, username, name){
  // Записывает нового подписчика в лист "Подписчики канала"
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Подписчики канала");
  if(!ws){
    ws=_insertSheetAtEnd(ss,"Подписчики канала");
    var headers=["Дата подписки","Chat ID","Username","Имя","Источник","Лид-магнит","Ниша","Доход (млн ₸)","Главная цель","Статус","Дата конверсии","Примечание"];
    ws.getRange(1,1,1,headers.length).setValues([headers]);
    ws.getRange(1,1,1,headers.length).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.setFrozenRows(1);
    ws.setColumnWidth(1,140);
    ws.setColumnWidth(4,160);
    ws.setColumnWidth(7,160);
    ws.setColumnWidth(9,260);
    ws.setColumnWidth(10,160);
  }
  // Проверка. уже подписан?
  var lr=ws.getLastRow();
  if(lr>=2){
    var ids=ws.getRange(2,2,lr-1,1).getValues();
    for(var i=0;i<ids.length;i++){
      if(String(ids[i][0])===String(chatId))return; // уже есть
    }
  }
  // Источник: если пришёл по реферальной ссылке. берём из кеша
  var source="Telegram /start";
  try{
    var cacheSrc=CacheService.getScriptCache();
    var refSrc=cacheSrc.get("ref_source_"+chatId);
    if(refSrc){
      source="Реферал: "+refSrc;
    } else {
      var rawSrc=cacheSrc.get("ref_source_raw_"+chatId);
      if(rawSrc) source=rawSrc;
    }
  }catch(e){}
  
  // Добавляем
  ws.appendRow([
    new Date(),
    String(chatId),
    username,
    name,
    source,
    "", // лид-магнит
    "", "", "", // ниша, доход, цель
    "Подписчик",
    "",
    ""
  ]);
  ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY HH:mm");
}

function _getSubscribersCount(){
  // Возвращает количество РЕАЛЬНЫХ подписчиков канала @bsurgery_kz через Telegram API.
  // Кеш 60 минут чтобы не спамить API.
  try{
    var cache=CacheService.getScriptCache();
    var cached=cache.get("real_subs_count");
    if(cached) return parseInt(cached);
    
    // Используем getChatMemberCount для @bsurgery_kz
    var resp=UrlFetchApp.fetch(
      "https://api.telegram.org/bot"+BOT_TOKEN+"/getChatMemberCount?chat_id=@bsurgery_kz",
      {muteHttpExceptions:true}
    );
    var data=JSON.parse(resp.getContentText());
    var count=0;
    if(data.ok){
      count=parseInt(data.result)||0;
      // Минус сам бот
      count=Math.max(0, count-1);
    }
    
    try{ cache.put("real_subs_count", String(count), 3600); }catch(e){}
    return count;
  }catch(e){
    Logger.log("_getSubscribersCount: "+e);
    return 0;
  }
}


function _isSubscriberOrResident(chatId){
  // Возвращает: "resident", "subscriber", "none"
  var ss=SpreadsheetApp.openById(SS_ID);
  // Сначала резиденты
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  if(wsR){
    var lrR=wsR.getLastRow();
    if(lrR>=3){
      var rIds=wsR.getRange(3,14,lrR-2,1).getValues();
      for(var i=0;i<rIds.length;i++){
        if(String(rIds[i][0]).trim()===String(chatId))return "resident";
      }
    }
  }
  // Подписчики
  var wsS=ss.getSheetByName("Подписчики канала");
  if(wsS){
    var lrS=wsS.getLastRow();
    if(lrS>=2){
      var sIds=wsS.getRange(2,2,lrS-1,1).getValues();
      for(var j=0;j<sIds.length;j++){
        if(String(sIds[j][0]).trim()===String(chatId))return "subscriber";
      }
    }
  }
  return "none";
}

function _setSubscriberField(chatId, columnName, value){
  // Обновляет одно поле в листе Подписчики канала
  var colMap={"Лид-магнит":6,"Ниша":7,"Доход (млн ₸)":8,"Главная цель":9,"Статус":10,"Дата конверсии":11,"Примечание":12};
  var col=colMap[columnName];
  if(!col)return;
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Подписчики канала");
  if(!ws)return;
  var lr=ws.getLastRow();
  if(lr<2)return;
  var ids=ws.getRange(2,2,lr-1,1).getValues();
  for(var i=0;i<ids.length;i++){
    if(String(ids[i][0]).trim()===String(chatId)){
      ws.getRange(2+i,col).setValue(value);
      return;
    }
  }
}


function _checkAllSubscribersMembership(){
  // Принудительная проверка статуса всех подписчиков в канале
  // Запускается через action или вручную из меню
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Подписчики канала");
    if(!ws)return{ok:false, error:"no sheet"};
    var lr=ws.getLastRow();
    if(lr<2)return{ok:true, checked:0};
    
    var data=ws.getRange(2,1,lr-1,12).getValues();
    var cache=CacheService.getScriptCache();
    var checked=0, left=0, kicked=0, member=0;
    
    data.forEach(function(row){
      if(!row[1])return;
      var chatId=String(row[1]);
      try{
        var resp=UrlFetchApp.fetch(
          "https://api.telegram.org/bot"+BOT_TOKEN+"/getChatMember?chat_id=@bsurgery_kz&user_id="+chatId,
          {muteHttpExceptions:true}
        );
        var d=JSON.parse(resp.getContentText());
        if(d.ok){
          var status=d.result.status;
          cache.put("memb_"+chatId, status, 3600);
          checked++;
          if(status==="left")left++;
          else if(status==="kicked")kicked++;
          else member++;
        }
      }catch(e){}
      Utilities.sleep(50);
    });
    
    return{ok:true, checked:checked, member:member, left:left, kicked:kicked};
  }catch(e){return{ok:false, error:e.toString()};}
}

function _miniCheckChannelMembership(p){
  return _checkAllSubscribersMembership();
}

function _miniGetSubscribers(){
  // Возвращает список РЕАЛЬНЫХ подписчиков канала @bsurgery_kz
  // ВАЖНО: показываем только тех, чей статус подтверждён как member/creator/administrator/restricted
  // Если статуса нет в кэше или статус left/kicked/unknown - НЕ показываем
  // Чтобы появились в списке - админ должен нажать "Проверить канал"
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Подписчики канала");
  if(!ws)return{subscribers:[], needCheck:true};
  var lr=ws.getLastRow();
  if(lr<2)return{subscribers:[], needCheck:true};
  var data=ws.getRange(2,1,lr-1,12).getValues();
  var list=[];
  var hasAnyCachedStatus=false;
  
  var cache=CacheService.getScriptCache();
  
  data.forEach(function(row,i){
    if(!row[1])return;
    var chatId=String(row[1]);
    
    // Проверяем кэш membership
    var status=cache.get("memb_"+chatId);
    if(status) hasAnyCachedStatus=true;
    
    // СТРОГИЙ ФИЛЬТР: пропускаем только подтверждённых членов канала
    // member, creator, administrator, restricted (может писать) = в канале
    // left, kicked = вышел или забанен
    // null/unknown = ещё не проверяли через Telegram
    if(status!=="member" && status!=="creator" && status!=="administrator" && status!=="restricted"){
      return;
    }
    
    list.push({
      row: 2+i,
      date: row[0] instanceof Date ? Utilities.formatDate(row[0],"Asia/Almaty","dd.MM HH:mm") : "",
      chatId: String(row[1]),
      username: row[2]||"",
      name: row[3]||"",
      source: row[4]||"",
      leadmagnet: row[5]||"",
      niche: row[6]||"",
      revenue: row[7]||"",
      goal: row[8]||"",
      channelStatus: status,
      status: row[9]||"Подписчик",
      convertDate: row[10] instanceof Date ? Utilities.formatDate(row[10],"Asia/Almaty","dd.MM") : (row[10]||""),
      note: row[11]||""
    });
  });
  // Сортируем. новые сверху
  list.reverse();
  return{subscribers:list, needCheck: !hasAnyCachedStatus && data.length>0};
}

function _miniBanFromChannel(p){
  // Банит пользователя в канале (kick + бан)
  try{
    var chatId=String(p.chatId||"").trim();
    if(!chatId)return{error:"Нет chatId"};
    
    // Бан в канале
    var resp=UrlFetchApp.fetch(
      "https://api.telegram.org/bot"+BOT_TOKEN+"/banChatMember?chat_id=@bsurgery_kz&user_id="+chatId,
      {muteHttpExceptions:true, method:"post"}
    );
    var data=JSON.parse(resp.getContentText());
    if(!data.ok)return{error:"TG API: "+(data.description||"unknown")};
    
    // Сразу очищаем кэш для этого пользователя
    try{ CacheService.getScriptCache().remove("memb_"+chatId); }catch(e){}
    
    // Помечаем в таблице как Удалён
    try{
      var ss=SpreadsheetApp.openById(SS_ID);
      var ws=ss.getSheetByName("Подписчики канала");
      if(ws){
        var lr=ws.getLastRow();
        if(lr>=2){
          var d=ws.getRange(2,1,lr-1,12).getValues();
          for(var i=0;i<d.length;i++){
            if(String(d[i][1])===chatId){
              ws.getRange(i+2,10).setValue("Удалён с канала");
              break;
            }
          }
        }
      }
    }catch(e){}
    
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniSetPartner(p){
  // Устанавливает партнёра для резидента + двустороннюю связь
  try{
    var name=String(p.name||"").trim();
    var partner=String(p.partner||"").trim();
    if(!name)return{error:"Нет имени"};
    
    _ensurePartnerColumn();
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR)return{error:"Нет листа"};
    
    var lr=wsR.getLastRow();
    var row1=-1, row2=-1;
    for(var r=3;r<=lr;r++){
      var nm=String(wsR.getRange(r, RC.name).getValue()||"").trim();
      if(nm===name) row1=r;
      if(partner && nm===partner) row2=r;
    }
    
    if(row1<0)return{error:"Резидент не найден"};
    
    // Если partner пустой - удаляем партнёрство
    if(!partner){
      // Найдём кто был партнёром у name и тоже очистим
      var oldPartner=String(wsR.getRange(row1, RC.partner).getValue()||"").trim();
      wsR.getRange(row1, RC.partner).setValue("");
      if(oldPartner){
        for(var rr=3;rr<=lr;rr++){
          if(String(wsR.getRange(rr, RC.name).getValue()||"").trim()===oldPartner){
            if(String(wsR.getRange(rr, RC.partner).getValue()||"").trim()===name){
              wsR.getRange(rr, RC.partner).setValue("");
            }
            break;
          }
        }
      }
      return{ok:true};
    }
    
    if(row2<0)return{error:"Партнёр не найден в списке"};
    
    // Устанавливаем двустороннюю связь
    wsR.getRange(row1, RC.partner).setValue(partner);
    wsR.getRange(row2, RC.partner).setValue(name);
    
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniGetCustdevResponses(p){
  // Возвращает все NPS/CustDev ответы (опционально для конкретного резидента)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("NPS ответы");
    if(!ws)return{responses:[], byResident:{}};
    var lr=ws.getLastRow();
    if(lr<2)return{responses:[], byResident:{}};
    
    var data=ws.getRange(2,1,lr-1,4).getValues();
    var all=[];
    var byResident={};
    
    data.forEach(function(row){
      if(!row[1])return; // нет имени
      var item={
        date: row[0] instanceof Date 
          ? Utilities.formatDate(row[0],"Asia/Almaty","dd.MM.yyyy HH:mm")
          : String(row[0]||""),
        name: String(row[1]||""),
        chatId: String(row[2]||""),
        text: String(row[3]||"")
      };
      all.push(item);
      if(!byResident[item.name]) byResident[item.name]=[];
      byResident[item.name].push(item);
    });
    
    return{responses:all, byResident:byResident};
  }catch(e){return{error:e.toString()};}
}


function _miniAddCustdevResponse(p){
  // Ручное добавление ответа в CustDev (для старых ответов которые не были записаны автоматически)
  try{
    var name=String(p.name||"").trim();
    var text=String(p.text||"").trim();
    if(!name)return{error:"Нет имени"};
    if(!text)return{error:"Нет текста"};
    
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("NPS ответы");
    if(!ws){
      ws=_insertSheetAtEnd(ss, "NPS ответы");
      ws.getRange(1,1,1,4).setValues([["Дата","Резидент","Chat ID","Ответ"]]);
      ws.getRange(1,1,1,4).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
      ws.setFrozenRows(1);
    }
    
    // Получим Chat ID резидента если есть
    var chatId=p.chatId||"";
    if(!chatId){
      try{ chatId=_getChatId(name); }catch(e){}
    }
    
    ws.appendRow([new Date(), name, String(chatId||""), text]);
    ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY HH:mm");
    
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}


function _miniRunNPS(p){
  // Запуск NPS опроса админом из Mini App
  // Защита: не чаще раза в 30 дней (та же что в sendNPS)
  try{
    sendNPS(); // встроена защита
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

// ══════════════════ КОЛЕСО БАЛАНСА ══════════════════
// Два колеса: "ДНК" (бизнес, 8 осей) и "Личное" (7 осей + Бизнес авто)
// Бизнес в личном колесе = среднее всех осей ДНК, считается автоматически

var WHEEL_DNA_AXES=["Стратегия","Финансы","Команда","Процессы","Продажи","Маркетинг","Продукт","Делегирование"];
var WHEEL_LIFE_AXES=["Бизнес","Здоровье","Семья","Окружение","Личные финансы","Развитие","Отдых","Смысл"];

function _getWheelSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Колесо");
  if(!ws){
    ws=ss.insertSheet("Колесо", ss.getNumSheets());
    var header=["Дата","Резидент","Тип"];
    for(var i=1;i<=8;i++)header.push("В"+i);
    header.push("Бизнес"); // колонка 12: название направления (для нескольких ДНК)
    ws.getRange(1,1,1,header.length).setValues([header]);
    ws.getRange(1,1,1,header.length).setBackground("#000").setFontColor("#fff").setFontWeight("bold");
    ws.setFrozenRows(1);
  } else {
    // Автодобавление колонки Бизнес в существующий лист
    try{
      var h12=String(ws.getRange(1,12).getValue()||"").trim();
      if(!h12){
        ws.getRange(1,12).setValue("Бизнес").setBackground("#000").setFontColor("#fff").setFontWeight("bold");
      }
    }catch(e){}
  }
  return ws;
}

function _miniSaveWheel(p){
  // Сохраняет замер колеса. type: "dna" | "life"
  // values: строка "7,5,8,6,4,7,5,6" (8 чисел для dna, 7 для life - Бизнес авто)
  try{
    var name=String(p.name||"").trim();
    var type=String(p.type||"").trim();
    var valuesStr=String(p.values||"").trim();
    if(!name)return{error:"Нет имени"};
    if(type!=="dna"&&type!=="life")return{error:"Неверный тип"};
    
    var values=valuesStr.split(",").map(function(v){
      var n=parseInt(v);
      if(isNaN(n)||n<1)n=1;
      if(n>10)n=10;
      return n;
    });
    
    var ws=_getWheelSheet();
    
    if(type==="dna"){
      if(values.length!==8)return{error:"Нужно 8 значений"};
      // Несколько бизнесов: каждый замер помечается названием направления
      var bizName=String(p.bizName||"Основной").trim().substring(0,30)||"Основной";
      ws.appendRow([new Date(), name, "ДНК"].concat(values).concat([bizName]));
    } else {
      // life: 7 значений от пользователя, Бизнес считаем автоматически
      if(values.length!==7)return{error:"Нужно 7 значений"};
      // Бизнес = СРЕДНЕЕ последних замеров ВСЕХ направлений ДНК резидента
      var bizAvg=5;
      var lr=ws.getLastRow();
      if(lr>=2){
        var data=ws.getRange(2,1,lr-1,12).getValues();
        var lastByBiz={}; // последний замер каждого бизнеса
        for(var i=data.length-1;i>=0;i--){
          if(String(data[i][1]).trim()!==name)continue;
          if(String(data[i][2]).trim()!=="ДНК")continue;
          var bn=String(data[i][11]||"Основной").trim()||"Основной";
          if(lastByBiz[bn])continue;
          var sum=0;
          for(var j=3;j<11;j++)sum+=Number(data[i][j])||0;
          lastByBiz[bn]=sum/8;
        }
        var bizNames=Object.keys(lastByBiz);
        if(bizNames.length){
          var total=0;
          bizNames.forEach(function(bn2){total+=lastByBiz[bn2];});
          bizAvg=Math.round(total/bizNames.length*10)/10;
        }
      }
      ws.appendRow([new Date(), name, "Личное", bizAvg].concat(values).concat([""]));
    }
    
    ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY HH:mm");
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _getResidentWheelAxes(name){
  // Кастомные названия 7 личных осей резидента (без Бизнеса). null если не настроены
  try{
    var raw=PropertiesService.getScriptProperties().getProperty("WHEEL_AXES_"+name);
    if(!raw)return null;
    var arr=JSON.parse(raw);
    if(Array.isArray(arr)&&arr.length===7)return arr;
    return null;
  }catch(e){return null;}
}

function _miniSaveWheelAxes(p){
  // Сохраняет кастомные названия 7 личных осей (Бизнес не редактируется)
  try{
    var name=String(p.name||"").trim();
    var axesStr=String(p.axes||"").trim();
    if(!name)return{error:"Нет имени"};
    var axes=axesStr.split("|").map(function(a){return String(a).trim().substring(0,20);}).filter(Boolean);
    if(axes.length!==7)return{error:"Нужно 7 названий"};
    PropertiesService.getScriptProperties().setProperty("WHEEL_AXES_"+name, JSON.stringify(axes));
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniGetWheelSummary(p){
  // Для админской вкладки Колесо: последние замеры всех резидентов
  // [{name, dnaAvg, dnaDate, dnaCount(бизнесов), lifeAvg, lifeDate, total(замеров)}]
  try{
    var ws=_getWheelSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{summary:[]};
    
    var data=ws.getRange(2,1,lr-1,12).getValues();
    var byRes={}; // name -> {bizLast:{bn:{avg,date}}, lifeLast:{avg,date}, total}
    
    data.forEach(function(row){
      var nm=String(row[1]||"").trim();
      var tp=String(row[2]||"").trim();
      if(!nm)return;
      if(!byRes[nm])byRes[nm]={bizLast:{}, lifeLast:null, total:0};
      byRes[nm].total++;
      var sum=0;
      for(var j=3;j<11;j++)sum+=Number(row[j])||0;
      var avg=Math.round(sum/8*10)/10;
      var dt=row[0] instanceof Date?Utilities.formatDate(row[0],"Asia/Almaty","dd.MM.yyyy"):String(row[0]);
      if(tp==="ДНК"){
        var bn=String(row[11]||"Основной").trim()||"Основной";
        byRes[nm].bizLast[bn]={avg:avg, date:dt}; // перезапишется более новым (идём сверху вниз по датам)
      } else if(tp==="Личное"){
        byRes[nm].lifeLast={avg:avg, date:dt};
      }
    });
    
    var summary=[];
    Object.keys(byRes).forEach(function(nm){
      var r=byRes[nm];
      var bizNames=Object.keys(r.bizLast);
      var dnaAvg=null, dnaDate="";
      if(bizNames.length){
        var t=0;
        bizNames.forEach(function(bn){t+=r.bizLast[bn].avg; if(r.bizLast[bn].date>dnaDate)dnaDate=r.bizLast[bn].date;});
        dnaAvg=Math.round(t/bizNames.length*10)/10;
      }
      summary.push({
        name:nm,
        dnaAvg:dnaAvg,
        dnaDate:dnaDate,
        dnaCount:bizNames.length,
        lifeAvg:r.lifeLast?r.lifeLast.avg:null,
        lifeDate:r.lifeLast?r.lifeLast.date:"",
        total:r.total
      });
    });
    
    summary.sort(function(a,b){return a.name.localeCompare(b.name);});
    return{summary:summary};
  }catch(e){return{error:e.toString()};}
}


// ══════════════════ ЗАДАЧИ РЕЗИДЕНТОВ ══════════════════
// Workflow: Открыта → На проверке (резидент сдал) → Принята / Возвращена (трекер решает)

function _getResTasksSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Задачи резидентов");
  if(!ws){
    ws=ss.insertSheet("Задачи резидентов", ss.getNumSheets());
    ws.getRange(1,1,1,7).setValues([["Дата","Резидент","Задача","Статус","Комментарий","Обновлено","Кто создал"]]);
    ws.getRange(1,1,1,7).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.setFrozenRows(1);
  }
  return ws;
}

function _miniAddResTask(p){
  // Админ создаёт задачу резиденту
  try{
    var name=String(p.name||"").trim();
    var task=String(p.task||"").trim();
    var author=String(p.author||"Админ").trim();
    if(!name||!task)return{error:"Нет данных"};
    
    var dedupCache=CacheService.getScriptCache();
    var dk="addtask_"+name+"_"+task.substring(0,40);
    if(dedupCache.get(dk))return{ok:true, deduplicated:true};
    dedupCache.put(dk,"1",30);
    
    var ws=_getResTasksSheet();
    ws.appendRow([new Date(), name, task, "Открыта", "", new Date(), author]);
    ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY");
    ws.getRange(ws.getLastRow(),6).setNumberFormat("DD.MM.YYYY HH:mm");
    
    // Уведомление резиденту
    try{
      var cid=_getChatId(name);
      if(cid)tgSend(cid,"📌 Новая задача от трекера:\n\n"+task+"\n\nСдай на проверку в приложении когда сделаешь.",{
        reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Мои задачи",web_app:{url:getWebAppUrl("mystatus")}}]]})
      });
    }catch(e){}
    
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniGetResTasks(p){
  // Задачи резидента (или все открытые/на проверке для админа если name пустой)
  try{
    var name=String(p.name||"").trim();
    var ws=_getResTasksSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{tasks:[]};
    
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var tasks=[];
    data.forEach(function(row,i){
      var rn=String(row[1]||"").trim();
      if(!rn)return;
      if(name && rn!==name)return;
      var status=String(row[3]||"Открыта").trim();
      // Нормализация старых статусов (проверка убрана)
      if(status==="Принята"||status==="На проверке")status="Выполнена";
      if(status==="Возвращена")status="Открыта";
      tasks.push({
        row:i+2,
        date:row[0] instanceof Date?Utilities.formatDate(row[0],"Asia/Almaty","dd.MM.yyyy"):String(row[0]),
        name:rn,
        task:String(row[2]||""),
        status:status,
        comment:String(row[4]||""),
        recurring:String(row[4]||"").indexOf("Постоянная")>=0,
        author:String(row[6]||"Админ").trim(),
        isOwn:String(row[6]||"").trim()==="Резидент",
        updated:row[5] instanceof Date?Utilities.formatDate(row[5],"Asia/Almaty","dd.MM"):String(row[5]||"")
      });
    });
    tasks.reverse(); // новые сверху
    return{tasks:tasks};
  }catch(e){return{error:e.toString()};}
}

function _miniSetResTaskStatus(p){
  // Резидент сам отмечает выполнение. Без проверки трекером. статусы смотрят на трекинге в карте пациента
  try{
    var row=parseInt(p.row);
    var status=String(p.status||"").trim();
    if(!row||row<2)return{error:"Bad row"};
    var allowed=["Открыта","Выполнена"];
    if(allowed.indexOf(status)<0)return{error:"Bad status"};
    
    var ws=_getResTasksSheet();
    var name=String(ws.getRange(row,2).getValue()||"").trim();
    if(!name)return{error:"Задача не найдена"};
    
    ws.getRange(row,4).setValue(status);
    ws.getRange(row,6).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
    
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniAddOwnTask(p){
  // Резидент ставит задачу себе сам. author = "Резидент", такие можно менять и удалять
  try{
    var name=String(p.name||"").trim();
    var task=String(p.task||"").trim();
    var recurring=String(p.recurring||"")==="1";
    if(!name||!task)return{error:"Нет данных"};
    var ws=_getResTasksSheet();
    ws.appendRow([new Date(), name, task, "Открыта", recurring?"Постоянная":"", new Date(), "Резидент"]);
    ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY");
    ws.getRange(ws.getLastRow(),6).setNumberFormat("DD.MM.YYYY HH:mm");
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniEditOwnTask(p){
  // Правка своей задачи. Задачи от админа резидент менять не может
  try{
    var row=parseInt(p.row);
    var name=String(p.name||"").trim();
    var task=String(p.task||"").trim();
    if(!row||row<2||!task)return{error:"Нет данных"};
    var ws=_getResTasksSheet();
    var rowName=String(ws.getRange(row,2).getValue()||"").trim();
    var author=String(ws.getRange(row,7).getValue()||"").trim();
    if(rowName!==name)return{error:"Чужая задача"};
    if(author!=="Резидент")return{error:"Задачу от трекера менять нельзя"};
    ws.getRange(row,3).setValue(task);
    ws.getRange(row,6).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniDeleteOwnTask(p){
  try{
    var row=parseInt(p.row);
    var name=String(p.name||"").trim();
    if(!row||row<2)return{error:"Bad row"};
    var ws=_getResTasksSheet();
    var rowName=String(ws.getRange(row,2).getValue()||"").trim();
    var author=String(ws.getRange(row,7).getValue()||"").trim();
    if(rowName!==name)return{error:"Чужая задача"};
    if(author!=="Резидент")return{error:"Задачу от трекера удалять нельзя"};
    ws.deleteRow(row);
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

// ══════════ СТОИМОСТЬ НЕРЕШЁННОЙ ПРОБЛЕМЫ ══════════
function _getProblemsSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Стоимость проблем");
  if(!ws){
    ws=ss.insertSheet("Стоимость проблем", ss.getNumSheets());
    ws.getRange(1,1,1,5).setValues([["Дата","Резидент","Проблема","Стоимость в месяц","Статус"]]);
    ws.getRange(1,1,1,5).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.setFrozenRows(1);
  }
  return ws;
}

function _miniSaveProblem(p){
  try{
    var name=String(p.name||"").trim();
    var text=String(p.problem||"").trim();
    var cost=Number(String(p.cost||"0").replace(/[^0-9]/g,""))||0;
    if(!name||!text)return{error:"Нет данных"};
    var ws=_getProblemsSheet();
    ws.appendRow([new Date(), name, text, cost, "Открыта"]);
    ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY");
    ws.getRange(ws.getLastRow(),4).setNumberFormat("#,##0");
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniGetProblems(p){
  try{
    var name=String(p.name||"").trim();
    var ws=_getProblemsSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{problems:[], total:0};
    var data=ws.getRange(2,1,lr-1,5).getValues();
    var problems=[], total=0;
    data.forEach(function(row,i){
      var rn=String(row[1]||"").trim();
      if(!rn)return;
      if(name && rn!==name)return;
      var status=String(row[4]||"Открыта").trim();
      var cost=Number(row[3])||0;
      if(status==="Открыта")total+=cost;
      problems.push({
        row:i+2, name:rn,
        date:row[0] instanceof Date?Utilities.formatDate(row[0],"Asia/Almaty","dd.MM.yyyy"):String(row[0]),
        problem:String(row[2]||""), cost:cost, status:status
      });
    });
    problems.reverse();
    return{problems:problems, total:total};
  }catch(e){return{error:e.toString()};}
}

function _miniSetProblemStatus(p){
  try{
    var row=parseInt(p.row);
    var status=String(p.status||"").trim();
    if(!row||row<2)return{error:"Bad row"};
    if(["Открыта","Решена"].indexOf(status)<0)return{error:"Bad status"};
    var ws=_getProblemsSheet();
    ws.getRange(row,5).setValue(status);
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniDeleteProblem(p){
  try{
    var row=parseInt(p.row);
    var name=String(p.name||"").trim();
    if(!row||row<2)return{error:"Bad row"};
    var ws=_getProblemsSheet();
    if(String(ws.getRange(row,2).getValue()||"").trim()!==name)return{error:"Чужая запись"};
    ws.deleteRow(row);
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}


// ══════════════════ СТРИК ОТЧЁТОВ ══════════════════
function _getAllReportStreaks(){
  // Стрики ВСЕХ резидентов одним проходом по листу (не по 21 чтению!)
  // Кеш 30 минут
  try{
    var cache=CacheService.getScriptCache();
    var cached=cache.get("all_streaks");
    if(cached){
      try{return JSON.parse(cached);}catch(e){}
    }
    
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsL=ss.getSheetByName("Лог отчётов");
    if(!wsL)return {};
    var lr=wsL.getLastRow();
    if(lr<2)return {};
    
    var data=wsL.getRange(2,1,lr-1,3).getValues();
    var daysByName={};
    data.forEach(function(row){
      var nm=String(row[2]||"").trim();
      if(!nm)return;
      var d=row[0];
      if(!(d instanceof Date))return;
      if(!daysByName[nm])daysByName[nm]={};
      daysByName[nm][Utilities.formatDate(d,"Asia/Almaty","yyyy-MM-dd")]=true;
    });
    
    var streaks={};
    var todayKey=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
    Object.keys(daysByName).forEach(function(nm){
      var days=daysByName[nm];
      var streak=0;
      var cursor=new Date();
      if(!days[todayKey])cursor.setDate(cursor.getDate()-1);
      for(var i=0;i<365;i++){
        var key=Utilities.formatDate(cursor,"Asia/Almaty","yyyy-MM-dd");
        if(days[key]){streak++;cursor.setDate(cursor.getDate()-1);}
        else break;
      }
      streaks[nm]=streak;
    });
    
    try{cache.put("all_streaks", JSON.stringify(streaks), 1800);}catch(e){}
    return streaks;
  }catch(e){Logger.log("_getAllReportStreaks: "+e);return {};}
}

function _getReportStreak(name){
  // Дней подряд с отчётами (по листу "Лог отчётов": col1=дата, col3=имя)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsL=ss.getSheetByName("Лог отчётов");
    if(!wsL)return 0;
    var lr=wsL.getLastRow();
    if(lr<2)return 0;
    
    var data=wsL.getRange(2,1,lr-1,3).getValues();
    var days={};
    data.forEach(function(row){
      if(String(row[2]||"").trim()!==name)return;
      var d=row[0];
      if(!(d instanceof Date))return;
      days[Utilities.formatDate(d,"Asia/Almaty","yyyy-MM-dd")]=true;
    });
    
    // Считаем подряд от сегодня (или вчера, если сегодня ещё не сдан)
    var streak=0;
    var cursor=new Date();
    var todayKey=Utilities.formatDate(cursor,"Asia/Almaty","yyyy-MM-dd");
    if(!days[todayKey])cursor.setDate(cursor.getDate()-1); // сегодня ещё не сдал. не рвём стрик
    for(var i=0;i<365;i++){
      var key=Utilities.formatDate(cursor,"Asia/Almaty","yyyy-MM-dd");
      if(days[key]){streak++;cursor.setDate(cursor.getDate()-1);}
      else break;
    }
    return streak;
  }catch(e){Logger.log("_getReportStreak: "+e);return 0;}
}

// ══════════════════ ПРИБЫЛЬ РЕЗИДЕНТА (график в ДНК) ══════════════════
function _getProfitSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Прибыль резидентов");
  if(!ws){
    ws=ss.insertSheet("Прибыль резидентов", ss.getNumSheets());
    ws.getRange(1,1,1,4).setValues([["Дата","Резидент","Выручка","Прибыль"]]);
    ws.getRange(1,1,1,4).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.setFrozenRows(1);
  }
  return ws;
}

function _miniSaveProfit(p){
  try{
    var name=String(p.name||"").trim();
    var revenue=Number(String(p.revenue||"0").replace(/[^0-9]/g,""))||0;
    var profit=Number(String(p.profit||"0").replace(/[^0-9]/g,""))||0;
    if(!name)return{error:"Нет имени"};
    if(!revenue&&!profit)return{error:"Введи цифры"};
    
    var dedupCache=CacheService.getScriptCache();
    var dk="profit_"+name+"_"+revenue+"_"+profit;
    if(dedupCache.get(dk))return{ok:true, deduplicated:true};
    dedupCache.put(dk,"1",30);
    
    var ws=_getProfitSheet();
    ws.appendRow([new Date(), name, revenue, profit]);
    ws.getRange(ws.getLastRow(),1).setNumberFormat("DD.MM.YYYY");
    ws.getRange(ws.getLastRow(),3,1,2).setNumberFormat("#,##0");
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniGetProfit(p){
  // Вся история прибыли резидента (для графика). Ничего не удаляется
  try{
    var name=String(p.name||"").trim();
    if(!name)return{records:[]};
    var ws=_getProfitSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{records:[]};
    var data=ws.getRange(2,1,lr-1,4).getValues();
    var records=[];
    data.forEach(function(row){
      if(String(row[1]||"").trim()!==name)return;
      records.push({
        date:row[0] instanceof Date?Utilities.formatDate(row[0],"Asia/Almaty","dd.MM.yy"):String(row[0]),
        revenue:Number(row[2])||0,
        profit:Number(row[3])||0
      });
    });
    return{records:records};
  }catch(e){return{error:e.toString()};}
}

// ══════════════════ КАРТА ПАЦИЕНТА ══════════════════
function sendPatientCard(resName){
  // Сводка по резиденту для админов перед встречей: колесо, задачи, посещения, прибыль, CustDev
  try{
    if(!resName)return;
    var lines=["🩺 КАРТА ПАЦИЕНТА: "+resName,""];
    
    // Колесо ДНК: слабые зоны
    try{
      var wheel=_miniGetWheel({name:resName});
      if(wheel.dna && wheel.dna.length){
        var vals=wheel.dna[0].values;
        var axes=wheel.dnaAxes;
        var sum=0;vals.forEach(function(v){sum+=v;});
        lines.push("🧬 ДНК: средний "+(Math.round(sum/vals.length*10)/10)+" ("+wheel.dna[0].date.split(" ")[0]+")");
        var weak=[];
        axes.forEach(function(ax,i){if(vals[i]<=5)weak.push(ax+" "+vals[i]);});
        if(weak.length)lines.push("⚠️ Слабые зоны: "+weak.join(", "));
        else lines.push("Все зоны выше 5");
      } else {
        lines.push("🧬 ДНК: замеров нет. попроси заполнить колесо");
      }
    }catch(e){}
    lines.push("");
    
    // Задачи
    try{
      var tasksRes=_miniGetResTasks({name:resName});
      var tasks=(tasksRes.tasks||[]);
      var open=tasks.filter(function(t){return t.status==="Открыта";});
      var done=tasks.filter(function(t){return t.status==="Выполнена";});
      lines.push("📌 Задачи: выполнено "+done.length+" · в работе "+open.length);
      open.slice(0,4).forEach(function(t){lines.push("  ⬜ "+t.task.substring(0,60));});
      done.slice(0,4).forEach(function(t){lines.push("  ✅ "+t.task.substring(0,60));});
    }catch(e){}
    lines.push("");
    
    // Посещения
    try{
      var ss=SpreadsheetApp.openById(SS_ID);
      var wsV=ss.getSheetByName("BS - посещения");
  if(!wsV) return;
      if(wsV){
        var lrV=wsV.getLastRow();
        for(var r=3;r<=lrV;r++){
          if(String(wsV.getRange(r,2).getValue()||"").trim()!==resName)continue;
          var tariff=parseInt(wsV.getRange(r,3).getValue())||4;
          var checked=0;
          for(var c=4;c<4+30;c++){
            if(wsV.getRange(r,c).getValue()===true)checked++;
          }
          lines.push("🗓 Посещения: "+checked+" галочек, тариф "+tariff);
          break;
        }
      }
    }catch(e){}
    
    // Прибыль: последние 2 замера
    try{
      var profitRes=_miniGetProfit({name:resName});
      var recs=(profitRes.records||[]);
      if(recs.length){
        var last=recs[recs.length-1];
        var lineP="💰 Прибыль: "+last.profit.toLocaleString()+" ("+last.date+")";
        if(recs.length>=2){
          var prev=recs[recs.length-2];
          var diff=last.profit-prev.profit;
          lineP+=" "+(diff>=0?"↑":"↓")+Math.abs(diff).toLocaleString()+" к прошлому";
        }
        lines.push(lineP);
      }
    }catch(e){}
    
    // Последний CustDev ответ
    try{
      var cd=_miniGetCustdevResponses({});
      var mine=(cd.byResident&&cd.byResident[resName])||[];
      if(mine.length){
        var lastCd=mine[mine.length-1];
        lines.push("");
        lines.push("💬 Последний CustDev ("+lastCd.date.split(" ")[0]+"): "+lastCd.text.substring(0,150)+(lastCd.text.length>150?"...":""));
      }
    }catch(e){}
    
    var msg=lines.join("\n");
    ADMIN_IDS.forEach(function(aid){
      try{tgSend(aid,msg);}catch(e){}
    });
  }catch(e){Logger.log("sendPatientCard: "+e);}
}




function _miniGetWheel(p){
  // Возвращает замеры колеса резидента.
  // По умолчанию: последние 2 каждого типа. p.history==="1": ВСЕ замеры (история, ничего не удаляется).
  // Плюс среднее по клубу (для админа), если p.club==="1"
  try{
    var name=String(p.name||"").trim();
    var wantHistory=String(p.history||"")==="1";
    var ws=_getWheelSheet();
    var lr=ws.getLastRow();
    
    // Личные оси: кастомные если настроены
    var lifeAxes=WHEEL_LIFE_AXES.slice();
    if(name){
      var custom=_getResidentWheelAxes(name);
      if(custom) lifeAxes=["Бизнес"].concat(custom);
    }
    
    var result={dnaAxes:WHEEL_DNA_AXES, lifeAxes:lifeAxes, dna:[], life:[], businesses:[]};
    if(lr<2)return result;
    
    var data=ws.getRange(2,1,lr-1,12).getValues();
    
    if(name){
      var maxRows=wantHistory?9999:2;
      var lifeRows=[];
      var bizMap={}; // bizName -> rows[]
      var bizOrder=[]; // порядок появления
      for(var i=data.length-1;i>=0;i--){
        var rowName=String(data[i][1]).trim();
        var rowType=String(data[i][2]).trim();
        if(rowName!==name)continue;
        var vals=[];
        for(var j=3;j<11;j++)vals.push(Number(data[i][j])||0);
        var dt=data[i][0] instanceof Date ? Utilities.formatDate(data[i][0],"Asia/Almaty","dd.MM.yyyy HH:mm") : String(data[i][0]);
        if(rowType==="ДНК"){
          var bn=String(data[i][11]||"Основной").trim()||"Основной";
          if(!bizMap[bn]){bizMap[bn]=[];bizOrder.push(bn);}
          if(bizMap[bn].length<maxRows)bizMap[bn].push({date:dt, values:vals});
        }
        if(rowType==="Личное" && lifeRows.length<maxRows) lifeRows.push({date:dt, values:vals});
      }
      // businesses: [{name, rows:[последний, предыдущий, ...]}]
      result.businesses=bizOrder.map(function(bn2){return {name:bn2, rows:bizMap[bn2]};});
      // Совместимость: dna = замеры первого бизнеса
      result.dna=bizOrder.length?bizMap[bizOrder[0]]:[];
      result.life=lifeRows;
    }
    
    // Среднее по клубу (все последние замеры ДНК каждого резидента)
    if(String(p.club||"")==="1"){
      var lastDnaByName={};
      for(var k=data.length-1;k>=0;k--){
        var nm=String(data[k][1]).trim();
        if(String(data[k][2]).trim()!=="ДНК")continue;
        if(lastDnaByName[nm])continue;
        var vv=[];
        for(var jj=3;jj<11;jj++)vv.push(Number(data[k][jj])||0);
        lastDnaByName[nm]=vv;
      }
      var names=Object.keys(lastDnaByName);
      if(names.length){
        var avg=[0,0,0,0,0,0,0,0];
        names.forEach(function(n){
          lastDnaByName[n].forEach(function(v,idx){avg[idx]+=v;});
        });
        result.clubAvg=avg.map(function(s){return Math.round(s/names.length*10)/10;});
        result.clubCount=names.length;
      }
    }
    
    return result;
  }catch(e){return{error:e.toString()};}
}





function _miniGetMonthlyPL(){
  // Авто-восстановление шапки листа Штрафы (только раз)
  try{
    var props=PropertiesService.getScriptProperties();
    if(props.getProperty("FIX_FINES_HEADER_DONE")!=="1"){
      _fixFinesHeader();
      props.setProperty("FIX_FINES_HEADER_DONE","1");
    }
  }catch(e){ Logger.log("fix fines header auto: "+e); }
  
  // САЛЬДО создаётся вручную через запуск ensureSaldoRowInPL() из редактора
  // НЕ автоматически здесь, чтобы не тормозить загрузку Mini App

  // Возвращает массив месяцев с финансовыми показателями для отображения в сводке
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("PL");
    if(!ws)return{ok:false, error:"Лист PL не найден"};
    
    var lr=ws.getLastRow();
    if(lr<5)return{ok:false, error:"Лист PL пустой"};
    
    var data=ws.getRange(1,1,lr,15).getValues();
    
    // Структура листа PL:
    // Строка 1: ,2026,2026,2026,...,2026,ГОД
    // Строка 2: Статья,1,2,3,...,12,-
    // Строка 3: Статья,Январь,Февраль,...,Декабрь,ИТОГО
    // Строка 4: ДОХОДЫ (заголовок секции)
    // Строки 5-10: подкатегории доходов
    // Строка ~11: ИТОГО ДОХОДЫ
    // ... РАСХОДЫ, ИТОГО РАСХОДЫ, ЧИСТАЯ ПРИБЫЛЬ
    
    var monthNames=["Январь","Февраль","Март","Апрель","Май","Июнь","Июль","Август","Сентябрь","Октябрь","Ноябрь","Декабрь"];
    
    // Находим нужные строки
    var rowRevenue=-1, rowExpenses=-1, rowProfit=-1, rowMargin=-1, rowDiv=-1;
    for(var r=0;r<data.length;r++){
      var label=String(data[r][0]||"").trim().toUpperCase();
      if(label.indexOf("ИТОГО ДОХОДЫ")>=0) rowRevenue=r;
      else if(label.indexOf("ИТОГО РАСХОДЫ")>=0) rowExpenses=r;
      else if(label.indexOf("ЧИСТАЯ ПРИБЫЛЬ")>=0) rowProfit=r;
      else if(label.indexOf("РЕНТАБЕЛЬНОСТЬ")>=0) rowMargin=r;
      else if(label.indexOf("ДИВИДЕНД")>=0) rowDiv=r;
    }
    
    var months=[];
    for(var m=0;m<12;m++){
      var col=m+1; // колонки 1-12 = месяцы
      var rev=rowRevenue>=0 ? Number(data[rowRevenue][col])||0 : 0;
      var exp=rowExpenses>=0 ? Number(data[rowExpenses][col])||0 : 0;
      var profit=rowProfit>=0 ? Number(data[rowProfit][col])||0 : 0;
      var marginRaw=rowMargin>=0 ? data[rowMargin][col] : 0;
      var margin=0;
      if(typeof marginRaw==="number") margin=marginRaw>1 ? Math.round(marginRaw) : Math.round(marginRaw*100);
      else if(typeof marginRaw==="string"){
        var match=marginRaw.match(/-?\d+/);
        if(match) margin=parseInt(match[0]);
      }
      
      var div=rowDiv>=0 ? Number(data[rowDiv][col])||0 : 0;
      months.push({
        idx: m,
        name: monthNames[m],
        short: monthNames[m].substring(0,3),
        revenue: rev,
        expenses: exp,
        profit: profit,
        dividends: div,
        margin: margin,
        hasData: rev>0 || exp>0
      });
    }
    
    // ОСТАТОК: приложение показывает ровно строку "ОСТАТОК" из PL (прибыль - дивиденды за месяц).
    // ОДИН ИСТОЧНИК ИСТИНЫ: цифра в приложении = цифра в таблице, без своих расчётов.
    // Накопительная касса живёт отдельной строкой "На кассе" в PL и отдаётся как kassa
    var rowOstatok=-1, rowKassa=-1;
    for(var kr=0;kr<data.length;kr++){
      var lbl=String(data[kr][0]||"").trim().toUpperCase();
      if(lbl==="ОСТАТОК")rowOstatok=kr;
      if(lbl==="НА КАССЕ")rowKassa=kr;
    }
    for(var sm=0;sm<12;sm++){
      // saldo = ОСТАТОК месяца (то что видно в PL)
      if(rowOstatok>=0){
        var ov=data[rowOstatok][sm+1];
        months[sm].saldo=(ov===""||ov==="-"||ov===null||ov===undefined)?null:(Number(ov)||0);
      } else {
        months[sm].saldo=months[sm].profit-months[sm].dividends;
      }
      // kassa = накопительный остаток на КОНЕЦ месяца (строка На кассе)
      if(rowKassa>=0){
        var kv=data[rowKassa][sm+1];
        months[sm].kassa=(kv===""||kv==="-"||kv===null||kv===undefined)?null:(Number(kv)||0);
      } else {
        months[sm].kassa=null;
      }
    }
    // kassaStart = остаток на НАЧАЛО месяца = касса на конец предыдущего
    for(var sm2=0;sm2<12;sm2++){
      if(sm2===0){
        months[sm2].kassaStart=null;
      } else {
        months[sm2].kassaStart=(months[sm2-1].kassa===null||months[sm2-1].kassa===undefined)?null:months[sm2-1].kassa;
      }
    }
    
    // Год - последняя колонка (ИТОГО)
    var yearCol=13;
    var yearRevenue=rowRevenue>=0 ? Number(data[rowRevenue][yearCol])||0 : 0;
    var yearExpenses=rowExpenses>=0 ? Number(data[rowExpenses][yearCol])||0 : 0;
    var yearProfit=rowProfit>=0 ? Number(data[rowProfit][yearCol])||0 : 0;
    var yearMarginRaw=rowMargin>=0 ? data[rowMargin][yearCol] : 0;
    var yearMargin=0;
    if(typeof yearMarginRaw==="number") yearMargin=yearMarginRaw>1 ? Math.round(yearMarginRaw) : Math.round(yearMarginRaw*100);
    else if(typeof yearMarginRaw==="string"){
      var match2=yearMarginRaw.match(/-?\d+/);
      if(match2) yearMargin=parseInt(match2[0]);
    }
    
    var yearDiv=rowDiv>=0 ? Number(data[rowDiv][yearCol])||0 : 0;

    // Итоги года считаем из месяцев: колонка ИТОГО могла остаться без формул
    var sR=0, sE=0, sP=0, sD=0;
    months.forEach(function(mm){ sR+=mm.revenue; sE+=mm.expenses; sP+=mm.profit; sD+=mm.dividends; });
    yearRevenue=sR; yearExpenses=sE; yearProfit=sP; yearDiv=sD;
    yearMargin = sR ? Math.round(sP/sR*100) : 0;
    
    // Финальное сальдо за год - последний месяц с данными
    var finalSaldo=null;
    for(var i=11;i>=0;i--){
      if(months[i].saldo!==null && months[i].hasData){
        finalSaldo=months[i].saldo;
        break;
      }
    }
    
    var lastKassa = null;
    for(var lk=11; lk>=0; lk--){ if(typeof months[lk].kassa === "number"){ lastKassa = months[lk].kassa; break; } }
    return{
      ok:true,
      kassa: lastKassa,
      months: months,
      year: {
        revenue: yearRevenue,
        expenses: yearExpenses,
        profit: yearProfit,
        dividends: yearDiv,
        margin: yearMargin,
        saldo: finalSaldo
      },
      currentMonthIdx: new Date().getMonth()
    };
  }catch(e){
    Logger.log("_miniGetMonthlyPL: "+e);
    return{ok:false, error:e.toString()};
  }
}

function ensureSaldoRowInPL(){
  // Добавляет в лист PL строку "САЛЬДО / ОСТАТОК" под строкой ДИВИДЕНДЫ.
  // С формулами: начиная с апреля (колонка E) накопительно считает остаток.
  // Январь.Март = пусто.
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("PL");
    if(!ws)return{error:"Лист PL не найден"};
    
    var lr=ws.getLastRow();
    if(lr<5)return{error:"Лист PL пустой"};
    
    var data=ws.getRange(1,1,lr,15).getValues();
    
    // Ищем существующую строку САЛЬДО или ОСТАТОК
    var rowSaldo=-1, rowDiv=-1;
    for(var r=0;r<data.length;r++){
      var label=String(data[r][0]||"").trim().toUpperCase();
      if(label.indexOf("НА КАССЕ")>=0){
        rowSaldo=r+1; // 1-based
        break;
      }
      if(label.indexOf("ДИВИДЕНД")>=0) rowDiv=r+1;
    }
    
    if(rowSaldo>0){
      Logger.log("Сальдо уже существует на строке "+rowSaldo);
      // Просто обновим формулы (на случай если они стёрты)
      _writeSaldoFormulas(ws, rowSaldo);
      return{ok:true, row:rowSaldo, action:"updated"};
    }
    
    if(rowDiv<0)return{error:"Строка ДИВИДЕНДЫ не найдена. Не могу добавить САЛЬДО"};
    
    // Вставляем новую строку ПОСЛЕ строки Дивиденды
    ws.insertRowAfter(rowDiv);
    rowSaldo=rowDiv+1;
    
    // Заголовок столбец A
    ws.getRange(rowSaldo, 1).setValue("САЛЬДО / ОСТАТОК")
      .setFontWeight("bold").setBackground("#1A3A5C").setFontColor("#FFFFFF")
      .setFontFamily("Montserrat").setFontSize(10);
    
    // Записываем формулы
    _writeSaldoFormulas(ws, rowSaldo);
    
    Logger.log("Строка САЛЬДО добавлена на позиции "+rowSaldo);
    return{ok:true, row:rowSaldo, action:"inserted"};
  }catch(e){
    Logger.log("ensureSaldoRowInPL: "+e);
    return{error:e.toString()};
  }
}

function _writeSaldoFormulas(ws, rowSaldo){
  // Находим строки прибыли и дивидендов
  var data=ws.getRange(1,1,ws.getLastRow(),1).getValues();
  var rowProfit=-1, rowDiv=-1;
  for(var r=0;r<data.length;r++){
    var label=String(data[r][0]||"").trim().toUpperCase();
    if(label.indexOf("ЧИСТАЯ ПРИБЫЛЬ")>=0) rowProfit=r+1;
    else if(label.indexOf("ДИВИДЕНД")>=0 && label.indexOf("САЛЬДО")<0) rowDiv=r+1;
  }
  if(rowProfit<0 || rowDiv<0){
    Logger.log("Не найдены строки прибыль/дивиденды");
    return;
  }
  
  // Колонки: B=Январь, C=Февраль, D=Март, E=Апрель, F=Май, ..., M=Декабрь, N=ИТОГО
  // Январь.Март (B, C, D) = пусто
  ws.getRange(rowSaldo, 2, 1, 3).setValue("");
  
  // Апрель = E. Сальдо(апрель) = Прибыль(E) - Дивиденды(E)
  ws.getRange(rowSaldo, 5).setFormula(
    "=" + ws.getRange(rowProfit, 5).getA1Notation() + "-" + ws.getRange(rowDiv, 5).getA1Notation()
  );
  
  // Май.Декабрь (F..M) = Сальдо предыдущего + Прибыль - Дивиденды
  for(var col=6; col<=13; col++){
    var prevSaldo = ws.getRange(rowSaldo, col-1).getA1Notation();
    var profitCell = ws.getRange(rowProfit, col).getA1Notation();
    var divCell = ws.getRange(rowDiv, col).getA1Notation();
    ws.getRange(rowSaldo, col).setFormula(
      "=" + prevSaldo + "+" + profitCell + "-" + divCell
    );
  }
  
  // ИТОГО (колонка N = 14) = последнее сальдо (декабрь)
  ws.getRange(rowSaldo, 14).setFormula("=" + ws.getRange(rowSaldo, 13).getA1Notation());
  
  // Форматирование
  ws.getRange(rowSaldo, 2, 1, 13).setNumberFormat("#,##0").setFontWeight("bold");
  // Подсветка золотым
  ws.getRange(rowSaldo, 2, 1, 13).setBackground("#FFF9C4");
  
  SpreadsheetApp.flush();
}


function sendMeetingReminders(){
  if(bsIsCopy()) return;
  if(bsServerOwns("meeting_reminders")) return;   // напоминания шлёт сервер
  // Запускается триггером каждый час. шлёт напоминания за 3 дня / 1 день / 1 час
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Расписание");
    if(!ws)return;
    
    var lr=ws.getLastRow();
    if(lr<3)return;
    
    // Колонки: A=№, B=Резидент, C=Дата, D=Время, E=Адрес, F=Meet, G=3day, H=1day
    // I=1hour (добавлю если нет)
    var data=ws.getRange(3, 1, lr-2, 21).getValues();
    var now=new Date();
    
    // Получим резидентов с Chat ID
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    var residentsChatMap={};
    if(wsR && wsR.getLastRow()>=3){
      var rd=wsR.getRange(3, 2, wsR.getLastRow()-2, 20).getValues(); // B (name) ... N (chatId)
      rd.forEach(function(r){
        var name=String(r[0]||"").trim();
        var chatId=String(r[12]||"").trim();
        if(name && chatId) residentsChatMap[name]=chatId;
      });
    }
    
    var sent3=0, sent1=0, sentH=0;
    
    for(var i=0;i<data.length;i++){
      var name=String(data[i][1]||"").trim();
      var dateVal=data[i][2];
      var timeVal=data[i][3];
      var meet=String(data[i][5]||"").trim();
      var flag3=data[i][6];
      var flag1=data[i][7];
      var flagH=data[i][8];
      
      if(!name || !dateVal) continue;
      var chatId=residentsChatMap[name];
      if(!chatId) continue;
      
      // Дату парсим
      var meetDate;
      if(dateVal instanceof Date) meetDate=new Date(dateVal.getTime());
      else {
        var s=String(dateVal).trim();
        var p=s.split(".");
        if(p.length<3) continue;
        meetDate=new Date(parseInt(p[2]), parseInt(p[1])-1, parseInt(p[0]));
      }
      // Время
      var hh=9, mm=0;
      if(timeVal){
        if(timeVal instanceof Date){
          hh=timeVal.getHours(); mm=timeVal.getMinutes();
        } else {
          var ts=String(timeVal).trim().split(":");
          if(ts.length>=2){ hh=parseInt(ts[0]); mm=parseInt(ts[1]); }
        }
      }
      meetDate.setHours(hh, mm, 0, 0);
      
      var dateStr=Utilities.formatDate(meetDate, "Asia/Almaty", "dd.MM.yyyy");
      var timeStr=Utilities.formatDate(meetDate, "Asia/Almaty", "HH:mm");
      var address=meet || "Онлайн (Google Meet)";
      
      // Считаем разницу в днях по календарным датам (без времени)
      var meetDateOnly=new Date(meetDate.getFullYear(), meetDate.getMonth(), meetDate.getDate());
      var nowDateOnly=new Date(now.getFullYear(), now.getMonth(), now.getDate());
      var daysDiff=Math.round((meetDateOnly.getTime()-nowDateOnly.getTime())/86400000);
      var nowHour=now.getHours();
      
      // ЗА 3 ДНЯ: если до встречи ровно 3 календарных дня И сейчас 9:00 или позже
      // Пример: встреча 10.06, сегодня 07.06, время >= 09:00 → отправить
      if(!flag3 && daysDiff===3 && nowHour>=9){
        var msg3=getText("schedule_3days", {"имя":name.split(" ")[0], "дата":dateStr+" "+timeStr, "адрес":address});
        try{
          tgSend(chatId, msg3);
          ws.getRange(i+3, 7).setValue("✅");
          sent3++;
        }catch(e){ Logger.log("3day send: "+e); }
      }
      
      // ЗА 1 ДЕНЬ: если до встречи ровно 1 календарный день И сейчас 9:00 или позже
      // Пример: встреча 10.06, сегодня 09.06, время >= 09:00 → отправить
      if(!flag1 && daysDiff===1 && nowHour>=9){
        var msg1=getText("schedule_1day", {"имя":name.split(" ")[0], "дата":dateStr+" "+timeStr, "адрес":address});
        try{
          // За день до встречи просим обновить колесо баланса. это повестка для трекинга
          tgSend(chatId, msg1 + "\n\n🎯 Перед встречей обнови Колесо баланса. это займёт 2 минуты и даст нам точную повестку.", {
            reply_markup: JSON.stringify({inline_keyboard:[[{text:"🧬 Обновить колесо", web_app:{url:getWebAppUrl("wheel")}}]]})
          });
          ws.getRange(i+3, 8).setValue("✅");
          sent1++;
        }catch(e){ Logger.log("1day send: "+e); }
      }
      
      // ЗА 1 ЧАС: если до встречи от 30 до 90 минут (без ограничения по времени суток)
      var diffMs=meetDate.getTime()-now.getTime();
      var diffMin=diffMs/60000;
      if(!flagH && daysDiff===0 && diffMin>=30 && diffMin<=90){
        var msgH="⏰ "+name.split(" ")[0]+", встреча через час!\n\n📅 "+dateStr+" "+timeStr+"\n";
        if(meet && meet.indexOf("http")>=0) msgH+="🔗 "+meet;
        else msgH+="📍 "+address;
        try{
          tgSend(chatId, msgH);
          ws.getRange(i+3, 9).setValue("✅");
          sentH++;
        }catch(e){ Logger.log("1hour send: "+e); }
      }
    }
    
    if(sent3+sent1+sentH>0){
      Logger.log("sendMeetingReminders: 3d="+sent3+" 1d="+sent1+" 1h="+sentH);
    }
    
  }catch(e){
    Logger.log("sendMeetingReminders: "+e);
  }
}

function _setupMeetingReminderTrigger(){
  // Создаст триггер каждый час для sendMeetingReminders. Запускать один раз вручную из редактора
  var trigs=ScriptApp.getProjectTriggers();
  for(var i=0;i<trigs.length;i++){
    if(trigs[i].getHandlerFunction()==="sendMeetingReminders"){
      ScriptApp.deleteTrigger(trigs[i]);
    }
  }
  ScriptApp.newTrigger("sendMeetingReminders")
    .timeBased()
    .everyHours(1)
    .create();
  Logger.log("Триггер sendMeetingReminders установлен (каждый час)");
  return "ok";
}



function _miniUpdateSubscriber(p){
  // Обновляет поле подписчика
  var chatId=String(p.chatId||"").trim();
  var field=String(p.field||"").trim();
  var value=String(p.value||"").trim();
  if(!chatId||!field)return{error:"Missing params"};
  try{
    _setSubscriberField(chatId, field, value);
    return{ok:true};
  }catch(e){return{error:String(e)}}
}

function _miniConvertToResident(p){
  // Переводит подписчика в резидента
  var chatId=String(p.chatId||"").trim();
  var name=String(p.name||"").trim();
  var tariff=String(p.tariff||"Стандарт").trim();
  var debt=Number(p.debt)||0;
  var format=String(p.format||"Офлайн").trim();
  var visits=Number(p.visits)||3;
  if(!chatId||!name)return{error:"Missing params"};
  // Используем существующий _miniAddResident
  var addRes=_miniAddResident({
    name: name,
    debt: String(debt),
    visits: visits,
    source: "Telegram канал",
    format: format,
    tariff: String(debt),
    residentChatId: chatId
  });
  // Обновляем подписчика
  try{
    _setSubscriberField(chatId,"Статус","Резидент");
    _setSubscriberField(chatId,"Дата конверсии", new Date());
  }catch(se){}
  return addRes;
}

function _miniGetOfflineResidents(){
  // Онлайн первыми, затем офлайн. Приложение рисует разделитель между группами
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("BS - резиденты дебет");
  if(!ws) return {ok:false,error:"Нет листа"};
  var lr=ws.getLastRow();
  if(lr<3) return {ok:true,residents:[]};
  var data=ws.getRange(3, 2, lr-2, 20).getValues();
  var onl=[], off=[];
  data.forEach(function(r){
    var nm=String(r[RI.name]||"").trim();
    if(!nm) return;
    if(String(r[RI.former]).trim()==="Да") return;
    if(String(r[RI.except]).trim()==="Да") return;
    if(String(r[RI.admin]).trim()==="Да") return;
    var fmt=String(r[RI.format]||"Офлайн").trim();
    var item={name:nm, res:nm, format:fmt, chatId:_normId(r[RI.chat])};
    if(fmt==="Онлайн") onl.push(item); else off.push(item);
  });
  onl.sort(function(a,b){ return a.name.localeCompare(b.name); });
  off.sort(function(a,b){ return a.name.localeCompare(b.name); });
  return {ok:true, residents:onl.concat(off),
          onlineCount:onl.length, offlineCount:off.length};
}

function _miniMarkAttendance(p){
  var names=String(p.names||"").split("|").map(function(s){return s.trim();}).filter(Boolean);
  var dateStr=String(p.date||"").trim();
  if(p.chatId && !isAdmin(p.chatId)) return{error:"Отмечать встречи может только команда"};
  var timeStr=String(p.time||"").trim();
  Logger.log("MARK ATTENDANCE: "+names.length+" residents, date="+dateStr);
  if(!names.length)return{error:"No names"};
  
  // ДЕДУП уровень 1: кеш 10 минут по набору имён+дате
  var dedupCache=CacheService.getScriptCache();
  var dedupKey="mark_att_"+dateStr+"_"+names.slice().sort().join(",");
  if(dedupCache.get(dedupKey)){
    Logger.log("_miniMarkAttendance: дубль заблокирован "+dedupKey);
    return{ok:true, deduplicated:true, marked:names};
  }
  dedupCache.put(dedupKey, "1", 600);
  
  // ДЕДУП уровень 2 (окончательный, по ДАННЫМ): если по всем именам уже есть запись
  // в "Лог встреч" на эту дату. это повторная обработка. выходим ТИХО, без сообщений
  try{
    var ssChk=SpreadsheetApp.openById(SS_ID);
    var wsLogChk=ssChk.getSheetByName("Лог встреч");
    if(wsLogChk && wsLogChk.getLastRow()>=2){
      var normDate=dateStr;
      if(normDate.split(".").length===2)normDate=normDate+"."+(new Date().getFullYear());
      var logDataChk=wsLogChk.getRange(2,1,wsLogChk.getLastRow()-1,2).getValues();
      var loggedNames={};
      logDataChk.forEach(function(row){
        var d=row[0];
        var dS=d instanceof Date?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d);
        if(dS===normDate)loggedNames[String(row[1]||"").trim()]=true;
      });
      var allAlready=names.every(function(n){return loggedNames[n];});
      if(allAlready){
        Logger.log("_miniMarkAttendance: все уже обработаны на "+normDate+". тихий выход");
        return{ok:true, alreadyDone:true, marked:names};
      }
    }
  }catch(chkE){Logger.log("mark idempotency: "+chkE);}
  
  // ПАРТНЁРЫ: если у резидента есть партнёр (S=19) и его нет в списке. добавляем.
  // галочка, лог и удаление из расписания сработают для ОБОИХ
  try{
    var addedPartners=[];
    names.slice().forEach(function(n){
      var pn=_getPartnerName(n);
      if(pn && names.indexOf(pn)<0 && addedPartners.indexOf(pn)<0){
        addedPartners.push(pn);
      }
    });
    if(addedPartners.length){
      names=names.concat(addedPartners);
      Logger.log("_miniMarkAttendance: добавлены партнёры "+addedPartners.join(", "));
    }
  }catch(prtE){Logger.log("mark partners: "+prtE);}

  var ss=SpreadsheetApp.openById(SS_ID);
  var wsV=ss.getSheetByName("BS - посещения");
  var wsS=ss.getSheetByName("Расписание");
  // Листа посещений может не быть: счётчик встреч живёт в дебете

  var marked=[];
  names.forEach(function(name){
    try{
      _addCheckForResident(name);
      try{ _logMeetingHappened(name, dateStr); }catch(lgE){ Logger.log("log "+name+": "+lgE); }
      marked.push(name);
    }catch(e){Logger.log("addCheck "+name+": "+e);}
  });

  // Удаляем встречи на эту дату из Расписания (и из календаря)
  // Сначала запомним дату для удаления из календаря (даже офлайн дня)
  var meetingDateForCal=null;
  if(wsS && dateStr){
    var lr=wsS.getLastRow();
    if(lr>=3){
      var data=wsS.getRange(3, 1, lr-2, 21).getValues();
      for(var i=data.length-1;i>=0;i--){
        var rRes=String(data[i][1]||"").trim();
        var rDate=data[i][2];
        if(!(rDate instanceof Date))continue;
        if(names.indexOf(rRes)<0)continue;
        var rDateFull=Utilities.formatDate(rDate,"Asia/Almaty","dd.MM.yyyy");
        var rDateShort=Utilities.formatDate(rDate,"Asia/Almaty","dd.MM");
        if(rDateFull===dateStr || rDateShort===dateStr){
          if(!meetingDateForCal) meetingDateForCal=new Date(rDate.getTime());
          // Календарь: событие не удаляем, а красим зелёным с «✅»
          try{ paintMeetingDone(rRes, rDateFull, String(data[i][9]||"")); }catch(cE){Logger.log("cal paint: "+cE);}
          // Строку не удаляем: красим и помечаем как проведённую
          wsS.getRange(3+i,1,1,10).setBackground("#E8F5E9");
          var st=String(wsS.getRange(3+i,7).getValue()||"");
          if(st.indexOf("✅")<0) wsS.getRange(3+i,7).setValue("✅ Проведена");
        }
      }
      SpreadsheetApp.flush();
    }
  }
  
  // Офлайн день: общее событие тоже красим, а не удаляем
  if(dateStr){
    try{ paintOfflineDayDone(dateStr); }catch(off2E){Logger.log("offline paint: "+off2E);}
  }

  // Уведомления резидентам
  marked.forEach(function(name){
    var cid=_getChatId(name);
    if(cid){
      tgSend(cid,"✅ Встреча "+dateStr+" подтверждена\nГалочка в посещениях поставлена",{
        reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("schedule")}}]]})
      });
    }
  });
  
  return{ok:true,marked:marked};
}

function _miniAddSchedule(p){
  var res=p.res,date=p.date,time=p.time;
  
  // ЗАЩИТА 1: дедуп по res+date+time на 120 сек (сетевые ретраи создавали 2-3 встречи)
  var dedupCache=CacheService.getScriptCache();
  var dedupKey="addsched_"+String(res)+"_"+String(date)+"_"+String(time);
  if(dedupCache.get(dedupKey)){
    Logger.log("_miniAddSchedule: дубль заблокирован "+dedupKey);
    return{ok:true, deduplicated:true};
  }
  dedupCache.put(dedupKey,"1",120);
  
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");if(!ws)return{error:"No schedule sheet"};
  
  // ЗАЩИТА 2: если встреча с тем же резидентом на ту же дату+время уже есть в таблице. не создаём
  try{
    var lrChk=ws.getLastRow();
    if(lrChk>=3){
      var chkData=ws.getRange(3, 1, lrChk-2, 21).getValues();
      var tgtDate=String(date||"").trim();
      var tgtTime=String(time||"").trim();
      for(var ci=0;ci<chkData.length;ci++){
        if(String(chkData[ci][1]||"").trim()!==String(res).trim())continue;
        var cDate=chkData[ci][2];
        var cDateStr=cDate instanceof Date?Utilities.formatDate(cDate,"Asia/Almaty","dd.MM.yyyy"):String(cDate);
        // ЖЁСТКОЕ ПРАВИЛО: у резидента не может быть ДВУХ встреч в один день.
        // Время не сравниваем. это и есть причина дублей (создали через список и через календарь)
        if(cDateStr===tgtDate || cDateStr.substring(0,5)===tgtDate.substring(0,5)){
          Logger.log("_miniAddSchedule: встреча у "+res+" на "+tgtDate+" уже есть, строка "+(ci+3));
          return{ok:true, duplicate:true, row:ci+3, message:"У резидента уже есть встреча в этот день"};
        }
      }
    }
  }catch(chkE){Logger.log("dup check: "+chkE);}

  // Парсим дату и время
  var dp=String(date||"").split(".");
  if(dp.length<2)return{error:"Bad date"};
  var year=parseInt(dp[2]||new Date().getFullYear());
  var month=parseInt(dp[1]);
  var day=parseInt(dp[0]);
  if(!month||!day)return{error:"Bad date parts"};
  var dStr2=year+"-"+String(month).padStart(2,"0")+"-"+String(day).padStart(2,"0");
  var dt=Utilities.parseDate(dStr2+" 12:00:00","Asia/Almaty","yyyy-MM-dd HH:mm:ss");
  var tp=String(time||"12:00").split(":");
  var hh=parseInt(tp[0])||12, mm=parseInt(tp[1])||0;
  // Парсим время с явной TZ Asia/Almaty
  var hhStr=String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
  var todayStrTime=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
  var evTimeValue=Utilities.parseDate(todayStrTime+" "+hhStr,"Asia/Almaty","yyyy-MM-dd HH:mm:ss");

  // Ищем первую СВОБОДНУЮ строку (с пустым именем в col B)
  var lr=ws.getLastRow();
  var newRow=-1;
  for(var r=3;r<=lr;r++){
    if(!ws.getRange(r,2).getValue()){newRow=r;break;}
  }
  if(newRow<0)newRow=Math.max(lr+1,3);

  var schedCache=CacheService.getScriptCache();
  schedCache.put("sched_meet_"+newRow+"_"+new Date().toDateString(),"1",3600);

  // Записываем
  ws.getRange(newRow,1).setValue(newRow-2);
  ws.getRange(newRow,2).setValue(res);
  ws.getRange(newRow,3).setValue(dt).setNumberFormat("DD.MM.YYYY");
  ws.getRange(newRow,4).setValue(evTimeValue).setNumberFormat("HH:MM");
  SpreadsheetApp.flush();

  // Создаём Meet асинхронно. но возвращаем ok сразу для скорости
  try{_autoCreateMeet(newRow);}catch(me){Logger.log("autoCreateMeet: "+me);}

    // Уведомляем резидента о назначенной встрече
  try{
    var _lnk = String((p && p.link) || ws.getRange(newRow, 6).getValue() || "").trim();
    var _plc = String((p && p.place) || ws.getRange(newRow, 5).getValue() || "").trim();
    bsNotifyResident(res, "Назначена встреча\n\n" +
      "Дата: " + date + "\nВремя: " + time +
      (_lnk ? "\nФормат: онлайн\nСсылка: " + _lnk : (_plc ? "\nФормат: офлайн\nМесто: " + _plc : "")) +
      "\n\nЕсли время не подходит, напишите заранее");
  }catch(nE){ Logger.log("notify meeting: "+nE); }

  return{ok:true,row:newRow};
}



function _miniAddResident(p){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("BS - резиденты дебет");
  if(!ws)return{error:"No residents sheet"};
  // Первая СВОБОДНАЯ строка
  var lr=ws.getLastRow();
  var newRow=_findFreeRow(ws,3,2);
  // Chat ID берём ТОЛЬКО из явного поля формы. Приложение подставляет chatId
  // вызывающего во все запросы, из-за чего новому резиденту записывался ваш ID
  var chatIdParam = String(p.residentChatId||"").trim();
  var name=String(p.name||"");
  var debt=Number(p.debt)||0;
  var visits=parseInt(p.visits)||3; // 3 или 4 встречи в месяц
  var tariff=debt; // сумма платежа = долг
  var source=String(p.source||"");
  var format=String(p.format||"Офлайн");
  // Определяем циклов оплачено по сумме тарифа
  // 500 000 → 3 мес, 1 250 000 → 12 мес, прочие (100-200к) → 1 мес
  var cyclesPaid=1;
  if(tariff>=1200000)cyclesPaid=12;
  else if(tariff>=400000)cyclesPaid=3;
  // Резидент в Дебете. заполняем ВСЕ столбцы
  // A=№, B=Имя, C=Оплачено, D=Остаток, E=Долг продление
  ws.getRange(newRow, RC.name).setValue(name);
  ws.getRange(newRow, RC.paid).setValue(0);
  ws.getRange(newRow, RC.rest).setValue(debt);
  ws.getRange(newRow, RC.renew).setValue(0);
  // H=Тариф, I=Дата входа, J=Источник
  ws.getRange(newRow, RC.tariff).setValue(tariff);
  ws.getRange(newRow, RC.date).setValue(new Date()).setNumberFormat("DD.MM.YYYY");
  ws.getRange(newRow, RC.source).setValue(source);
  // K=Бывший, L=Исключение, M=Примечание
  ws.getRange(newRow, RC.former).setValue("Нет");
  ws.getRange(newRow, RC.except).setValue("Нет");
  // M пусто
  // N=Chat ID пусто
  // O=Формат, P=Админ
  ws.getRange(newRow, RC.format).setValue(format);
  if(chatIdParam) ws.getRange(newRow, RC.chat).setValue(chatIdParam);
  // P пусто
  // Q=Встреч проведено, R=Встреч оплачено
  var visitsPerMonth = parseInt(p.visits) || 3;
  var monthsForTariff = _tariffToMonths(tariff);
  ws.getRange(newRow, RC.granted).setValue(monthsForTariff * visitsPerMonth);
  ws.getRange(newRow, RC.done).setValue(0);
  // Оформление новой строки как у остальных: шрифт Montserrat, чередование, границы
  try{
    var lastColNew=Math.max(ws.getLastColumn(), RC.months);
    _applyRowStyle(ws, newRow, lastColNew, (newRow % 2 === 0));
  }catch(e){ Logger.log("style new resident: "+e); }

  // S=Партнёр (если указан)
  var partner=String(p.partner||"").trim();
  if(partner){
    try{_ensurePartnerColumn();}catch(e){}
    ws.getRange(newRow, RC.partner).setValue(partner);
    // Двусторонняя связь: партнёру тоже впишем этого резидента
    try{
      var lr2=ws.getLastRow();
      for(var rp=3;rp<=lr2;rp++){
        if(String(ws.getRange(rp, RC.name).getValue()||"").trim()===partner){
          if(!ws.getRange(rp, RC.partner).getValue()){
            ws.getRange(rp, RC.partner).setValue(name);
          }
          break;
        }
      }
    }catch(bpE){Logger.log("bind partner: "+bpE);}
  }
  // Делаем шрифт обычным (не жирным). как у других резидентов
  ws.getRange(newRow,1,1, RC.months).setFontWeight("normal");
  // Резидент в Посещения
  try{
    var wsV=ss.getSheetByName("BS - посещения");
    if(wsV){
      var lrV=wsV.getLastRow();
      var newVRow=-1;
      for(var rv=3;rv<=lrV;rv++){
        if(!wsV.getRange(rv,2).getValue()){newVRow=rv;break;}
      }
      if(newVRow<0)newVRow=lrV+1;
      wsV.getRange(newVRow,1).setValue(newVRow-2);
      wsV.getRange(newVRow,2).setValue(name);
      wsV.getRange(newVRow,3).setValue(visits); // 3 или 4 в столбце C
    }
  }catch(ve){Logger.log("Add to visits: "+ve);}
  recalcResidents();
  // Нумерация по активным резидентам и синхронизация порядка посещений
  try{_renumRes();}catch(e){}
  try{syncVisitsOrder();}catch(e){}
  ADMIN_IDS.forEach(function(id){
    bsSysNoteTo(id,"🆕 Новый резидент добавлен:\n👤 "+name+"\n💰 "+debt.toLocaleString()+" тг\n📅 "+visits+" встреч/мес\n📍 "+format+" | "+source,{
      reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("residents")}}]]})
    });
  });
    try{
    bsNotifyResident(p.name, "Добро пожаловать в Business Surgery\n\n" +
      "Ваш профиль создан. Отчёты и задачи появятся в приложении после первого разбора");
  }catch(nE){ Logger.log("notify new: " + nE); }
  return{ok:true,row:newRow};
}


function _miniDeleteSchedule(p){
  var resName=String(p.res||"").trim();
  var dateStr=String(p.date||"").trim();
  var timeStr=String(p.time||"").trim();
  
  // ЗАЩИТА ОТ ДУБЛЕЙ: один и тот же запрос на удаление за 60 секунд блокируется
  var delGuardCache=CacheService.getScriptCache();
  var delGuardKey="del_sched_"+resName+"_"+dateStr+"_"+timeStr;
  if(delGuardCache.get(delGuardKey)){
    Logger.log("Дубль _miniDeleteSchedule заблокирован: "+delGuardKey);
    return{ok:true, duplicate:true};
  }
  delGuardCache.put(delGuardKey,"1",60);
  
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");
  if(!ws)return{error:"No schedule"};
  var lr=ws.getLastRow();
  if(lr<3)return{error:"Empty"};
  Logger.log("===DELETE=== res=["+resName+"], date=["+dateStr+"], time=["+timeStr+"]");

  var data=ws.getRange(3, 1, lr-2, 21).getValues();
  var disp=ws.getRange(3, 1, lr-2, 21).getDisplayValues();

  var foundRow=-1;
  for(var i=0;i<data.length;i++){
    var rRes=String(data[i][1]||"").trim();
    var rDate=data[i][2];
    if(rRes!==resName)continue;
    if(!(rDate instanceof Date))continue;
    var rDateFull=Utilities.formatDate(rDate,"Asia/Almaty","dd.MM.yyyy");
    var rDateShort=Utilities.formatDate(rDate,"Asia/Almaty","dd.MM");
    var rTimeStr=String(disp[i][3]||"").trim();
    Logger.log("Row "+(3+i)+": ["+rRes+"] ["+rDateFull+"/"+rDateShort+"] ["+rTimeStr+"]");

    var dateMatch=(rDateFull===dateStr)||(rDateShort===dateStr);
    if(!dateMatch){continue;}

    if(timeStr){
      var rHM=rTimeStr.split(":").slice(0,2).join(":");
      var cHM=timeStr.split(":").slice(0,2).join(":");
      if(rHM!==cHM){continue;}
    }

    foundRow=3+i;
    break;
  }

  if(foundRow<0){
    Logger.log("NOT FOUND");
    return{error:"Not found"};
  }

  // Удаляем из календаря
  try{
    // Используем календарь BS Meetings + удаляем через Advanced API
    var calProps2=PropertiesService.getScriptProperties();
    var bsCalId2=calProps2.getProperty("BS_CALENDAR_ID");
    var rDateForCal=data[foundRow-3][2];
    if(bsCalId2){
      // Удаляем через Advanced Calendar API (искать в окне ±24 часа)
      var startSearch=new Date(rDateForCal.getTime()-86400000);
      var endSearch=new Date(rDateForCal.getTime()+86400000);
      try{
        var eventsList=Calendar.Events.list(bsCalId2,{
          timeMin:startSearch.toISOString(),
          timeMax:endSearch.toISOString(),
          singleEvents:true
        });
        if(eventsList.items){
          eventsList.items.forEach(function(ev){
            if(ev.summary && ev.summary.indexOf(resName)>=0){
              Logger.log("Calendar delete: "+ev.summary);
              try{Calendar.Events.remove(bsCalId2,ev.id);}catch(rmErr){Logger.log("rm err: "+rmErr);}
            }
          });
        }
      }catch(listErr){Logger.log("list events: "+listErr);}
    }
  }catch(ce){Logger.log("Calendar err: "+ce);}

  var rDateStrFinal=Utilities.formatDate(data[foundRow-3][2],"Asia/Almaty","dd.MM.yyyy");
  var rTimeStrFinal=String(disp[foundRow-3][3]||"");

  ws.deleteRow(foundRow);
  SpreadsheetApp.flush();
  Logger.log("Row deleted: "+foundRow);
  try{ _invalidateBSCaches(); }catch(ce2){ Logger.log("cache: "+ce2); }

  var resChatId=_getChatId(resName);
  var deletedMsg="❌ Встреча отменена\n👤 "+resName+"\n📅 "+rDateStrFinal+(rTimeStrFinal?" "+rTimeStrFinal:"");
  // Резиденту. короткое сообщение без своего имени
  if(resChatId){
    var resMsg="❌ Встреча отменена\n📅 "+rDateStrFinal+(rTimeStrFinal?" "+rTimeStrFinal:"");
    tgSend(resChatId,resMsg,{
      reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("schedule")}}]]})
    });
  }
  // Админам. одним общим уведомлением через tgSendAdmins
  try{bsSysNote(deletedMsg,"schedule");}catch(e){}
  return{ok:true};
}


function _miniSaveAvatar(p){
  var chatId=String(p.chatId||"");
  var avatar=String(p.avatar||"");
  if(!chatId||!avatar)return{ok:false};
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Профили");
  if(!ws){
    ws=ss.insertSheet("Профили", ss.getNumSheets());
    ws.appendRow(["Chat ID","Ниша","О себе","Чем поможет","Instagram","Телефон","Аватар URL"]);
    ws.getRange(1,1,1,7).setFontWeight("bold").setBackground("#1A3A5C").setFontColor("#FFFFFF");
    ws.setFrozenRows(1);
  }
  var lr=ws.getLastRow();
  var foundRow=-1;
  if(lr>1){
    var data=ws.getRange(2,1,lr-1,1).getValues();
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===chatId){foundRow=i+2;break;}
    }
  }
  if(foundRow>0){
    ws.getRange(foundRow,7).setValue(avatar);
  } else {
    ws.appendRow([chatId,"","","","","",avatar]);
  }
  return{ok:true};
}

function _miniUpdateMeeting(p){
  // Перенос встречи: меняет в таблице И в Google Календаре
  var oldRes=String(p.oldRes||"").trim();
  var oldDate=String(p.oldDate||"").trim();
  var oldTime=String(p.oldTime||"").trim();
  var newDate=String(p.newDate||"").trim();
  var newTime=String(p.newTime||"").trim();
  Logger.log("=== UPDATE MEETING === res="+oldRes+" "+oldDate+" "+oldTime+" → "+newDate+" "+newTime);

  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");
  if(!ws)return{error:"No schedule"};
  var lr=ws.getLastRow();
  if(lr<3)return{error:"Empty"};
  var data=ws.getRange(3, 1, lr-2, 21).getValues();
  var sDisplay=ws.getRange(3, 1, lr-2, 21).getDisplayValues();

  var foundRow=-1;
  for(var i=0;i<data.length;i++){
    if(String(data[i][1]).trim()!==oldRes)continue;
    if(!(data[i][2] instanceof Date))continue;
    var rDateFull=Utilities.formatDate(data[i][2],"Asia/Almaty","dd.MM.yyyy");
    var rDateShort=Utilities.formatDate(data[i][2],"Asia/Almaty","dd.MM");
    var rTimeStr=String(sDisplay[i][3]||"").trim();
    var dateMatch=(rDateFull===oldDate)||(rDateShort===oldDate);
    if(!dateMatch)continue;
    if(oldTime){
      var rHM=rTimeStr.split(":").slice(0,2).join(":");
      var cHM=oldTime.split(":").slice(0,2).join(":");
      if(rHM!==cHM)continue;
    }
    foundRow=3+i;
    break;
  }
  if(foundRow<0){
    Logger.log("Meeting not found for update");
    return{error:"Not found"};
  }

  var oldEventDate=data[foundRow-3][2];

  // Парсим новую дату
  var newDt=null;
  if(newDate){
    var dp=newDate.split(".");
    var year=parseInt(dp[2]||new Date().getFullYear());
    var month=parseInt(dp[1]);
    var day=parseInt(dp[0]);
    var dStr=year+"-"+String(month).padStart(2,"0")+"-"+String(day).padStart(2,"0");
    newDt=Utilities.parseDate(dStr+" 12:00:00","Asia/Almaty","yyyy-MM-dd HH:mm:ss");
    ws.getRange(foundRow,3).setValue(newDt).setNumberFormat("DD.MM.YYYY");
  }
  // Парсим новое время
  if(newTime){
    var tp=newTime.split(":");
    var hh=parseInt(tp[0])||0,mm=parseInt(tp[1])||0;
    var hhStr=String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
    var todayStr=Utilities.formatDate(new Date(),"Asia/Almaty","yyyy-MM-dd");
    var evTimeValue=Utilities.parseDate(todayStr+" "+hhStr,"Asia/Almaty","yyyy-MM-dd HH:mm:ss");
    ws.getRange(foundRow,4).setValue(evTimeValue).setNumberFormat("HH:MM");
  }
  SpreadsheetApp.flush();

  // ОБНОВЛЯЕМ СОБЫТИЕ В КАЛЕНДАРЕ BS
  try{
    var calProps=PropertiesService.getScriptProperties();
    var bsCalId=calProps.getProperty("BS_CALENDAR_ID");
    if(bsCalId){
      // Ищем событие в окне ±24ч от старой даты
      var startSearch=new Date(oldEventDate.getTime()-86400000);
      var endSearch=new Date(oldEventDate.getTime()+86400000);
      var eventsList=Calendar.Events.list(bsCalId,{
        timeMin:startSearch.toISOString(),
        timeMax:endSearch.toISOString(),
        singleEvents:true
      });
      if(eventsList.items){
        for(var ei=0;ei<eventsList.items.length;ei++){
          var ev=eventsList.items[ei];
          if(ev.summary && ev.summary.indexOf(oldRes)>=0){
            // Нашли. обновляем дату/время
            var finalDt=newDt||oldEventDate;
            var finalYear=finalDt.getFullYear();
            var finalMonth=finalDt.getMonth()+1;
            var finalDay=finalDt.getDate();
            var tpFinal=(newTime||oldTime||"12:00").split(":");
            var fHour=parseInt(tpFinal[0])||12;
            var fMin=parseInt(tpFinal[1])||0;
            var startISO=Utilities.formatString("%04d-%02d-%02dT%02d:%02d:00+05:00",finalYear,finalMonth,finalDay,fHour,fMin);
            var endH=fHour+1, endD=finalDay, endM=finalMonth, endY=finalYear;
            if(endH>=24){endH-=24;endD++;}
            var endISO=Utilities.formatString("%04d-%02d-%02dT%02d:%02d:00+05:00",endY,endM,endD,endH,fMin);
            Calendar.Events.patch({
              start:{dateTime:startISO,timeZone:"Asia/Almaty"},
              end:{dateTime:endISO,timeZone:"Asia/Almaty"}
            }, bsCalId, ev.id, {sendUpdates:"all"});
            Logger.log("Calendar event updated: "+ev.id);
            break;
          }
        }
      }
    }
  }catch(calErr){Logger.log("Calendar update err: "+calErr);}

  // Уведомления
  var resChatId=_getChatId(oldRes);
  var msg="🔄 Встреча перенесена\n👤 "+oldRes+"\n📅 Было: "+oldDate+" "+oldTime+"\n📅 Стало: "+newDate+" "+newTime;
  var kbBS={reply_markup:JSON.stringify({inline_keyboard:[[{text:"📱 Открыть в BS",web_app:{url:getWebAppUrl("schedule")}}]]})};
  if(resChatId)tgSend(resChatId,msg,kbBS);
  ADMIN_IDS.forEach(function(id){bsSysNoteTo(id,msg,kbBS);});

    try{
    bsNotifyResident(resName || p.res, "Встреча перенесена\n\nНовая дата: " + (p.newDate || p.date || "") +
      (p.newTime || p.time ? "\nВремя: " + (p.newTime || p.time) : ""));
  }catch(nE){ Logger.log("notify move: " + nE); }
  return{ok:true};
}

function _miniSaveProfile(p){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Профили");
  if(!ws){
    ws=ss.insertSheet("Профили", ss.getNumSheets());
    ws.appendRow(["Chat ID","Ниша","О себе","Чем поможет","Instagram","Телефон","Аватар URL"]);
    ws.getRange(1,1,1,7).setFontWeight("bold").setBackground("#1A3A5C").setFontColor("#FFFFFF");
    ws.setFrozenRows(1);
  }
  var chatId=String(p.chatId||"");
  if(!chatId)return{error:"No chatId"};
  // Ищем строку
  var lr=ws.getLastRow();
  var foundRow=-1;
  if(lr>1){
    var data=ws.getRange(2,1,lr-1,1).getValues();
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===chatId){foundRow=i+2;break;}
    }
  }
  var row=foundRow>0?foundRow:lr+1;
  ws.getRange(row,1,1,7).setValues([[
    chatId, p.niche||"", p.bio||"", p.help||"",
    p.instagram||"", p.phone||"", p.avatar||""
  ]]);
  return{ok:true};
}

function diagnoseCalendar(){
  // Показывает: какой календарь используется, какие события в нём есть,
  // работает ли покраска. Запускать при любых сомнениях
  var out = ["📅 Диагностика календаря", ""];
  try{
    var props = PropertiesService.getScriptProperties();
    var bsCalId = props.getProperty("BS_CALENDAR_ID");
    out.push("ID в настройках: " + (bsCalId || "не задан"));

    var cal = null;
    if(bsCalId){
      try{ cal = CalendarApp.getCalendarById(bsCalId); }catch(e){}
    }
    if(cal) out.push("Календарь: " + cal.getName() + " ✅");
    else {
      out.push("По ID не открылся, ищем по названию");
      var owned = CalendarApp.getAllOwnedCalendars();
      out.push("Всего своих календарей: " + owned.length);
      owned.forEach(function(c){ out.push("  • " + c.getName()); });
      for(var i=0;i<owned.length;i++){
        if(owned[i].getName()==="Business Surgery Meetings"){
          cal = owned[i];
          props.setProperty("BS_CALENDAR_ID", cal.getId());
          out.push("Найден и записан в настройки ✅");
          break;
        }
      }
    }
    if(!cal){ cal = CalendarApp.getDefaultCalendar(); out.push("Используем календарь по умолчанию"); }

    // События за последнюю неделю
    var to = new Date();
    var from = new Date(to.getTime() - 7*24*3600*1000);
    var evts = cal.getEvents(from, to);
    out.push("");
    out.push("События за 7 дней: " + evts.length);
    evts.slice(0,12).forEach(function(ev){
      var d = Utilities.formatDate(ev.getStartTime(), "Asia/Almaty", "dd.MM HH:mm");
      var col = "";
      try{ col = ev.getColor() || "по умолчанию"; }catch(e){ col = "?"; }
      out.push("  " + d + " · " + ev.getTitle() + " · цвет " + col);
    });

    // Пробная покраска последнего события
    out.push("");
    if(evts.length){
      var test = evts[evts.length-1];
      try{
        var before = test.getColor() || "по умолчанию";
        test.setColor(CalendarApp.EventColor.GREEN);
        var after = test.getColor();
        out.push("Тест покраски: " + before + " → " + after + (after === "10" ? " ✅" : " ⚠️"));
        out.push("Событие: " + test.getTitle());
        out.push("(цвет изменён, при необходимости верните вручную)");
      }catch(pe){
        out.push("❌ Покраска не работает: " + pe);
      }
    } else {
      out.push("Событий нет, тест покраски пропущен");
    }
  }catch(e){
    out.push("Ошибка: " + e);
  }
  var msg = out.join("\n");
  Logger.log(msg);
  try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
  ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, msg); }catch(e){} });
  return msg;
}

function paintMeetingDone(resName, ddmmyyyy, eventId){
  // Красит событие в Базилик через CalendarApp.
  // Раньше использовался Advanced Calendar API, который может быть не подключён
  // в проекте. вызов падал, ошибка гасилась, а система рапортовала успех
  try{
    var calProps=PropertiesService.getScriptProperties();
    var bsCalId=calProps.getProperty("BS_CALENDAR_ID");
    var cal=null;
    if(bsCalId){
      try{ cal=CalendarApp.getCalendarById(bsCalId); }catch(e){ cal=null; }
    }
    if(!cal){
      var owned=CalendarApp.getAllOwnedCalendars();
      for(var ci=0;ci<owned.length;ci++){
        if(owned[ci].getName()==="Business Surgery Meetings"){ cal=owned[ci]; break; }
      }
    }
    if(!cal) cal=CalendarApp.getDefaultCalendar();
    if(!cal){ Logger.log("Календарь: не найден"); return false; }

    // Разбор даты
    var parts=String(ddmmyyyy||"").split(".");
    if(parts.length<2){ Logger.log("Календарь: плохая дата "+ddmmyyyy); return false; }
    var dd=parseInt(parts[0]), mo=parseInt(parts[1])-1;
    var yr=(parts.length>2 && parts[2]) ? parseInt(parts[2]) : new Date().getFullYear();
    if(String(yr).length===2) yr=2000+yr;

    // Событие по ID
    if(eventId){
      try{
        var evById=cal.getEventById(String(eventId));
        if(evById){
          evById.setColor(CalendarApp.EventColor.GREEN);
          var t=evById.getTitle();
          if(t.indexOf("✅")!==0) evById.setTitle("✅ "+t);
          Logger.log("Календарь: покрашено по ID. "+t);
          return true;
        }
      }catch(e1){ Logger.log("Календарь: по ID не вышло ("+e1+")"); }
    }

    // Поиск СТРОГО в этот день и СТРОГО по имени резидента.
    // Раньше окно было плюс-минус сутки, а условие ловило любую встречу BS,
    // из-за чего красился следующий резидент по списку
    var from=new Date(yr,mo,dd,0,0,0);
    var to=new Date(yr,mo,dd,23,59,59);
    var events=cal.getEvents(from,to);
    Logger.log("Календарь: событий в этот день "+events.length+", ищем "+resName);

    var target=String(resName||"").trim().toLowerCase();
    var firstName=target.split(" ")[0];
    for(var i=0;i<events.length;i++){
      var ev=events[i];
      var title=String(ev.getTitle()||"");
      if(title.indexOf("✅")===0) continue;
      var low=title.toLowerCase();
      // Только точное вхождение имени. Никаких общих слов вроде "трекинг"
      var hit = (target.length>2 && low.indexOf(target)>=0) ||
                (firstName.length>3 && low.indexOf(firstName)>=0);
      if(hit){
        ev.setColor(CalendarApp.EventColor.GREEN);
        ev.setTitle("✅ "+title);
        Logger.log("Календарь: покрашено. "+title);
        return true;
      }
    }
    Logger.log("Календарь: событие для "+resName+" на "+ddmmyyyy+" не найдено среди "+events.length);
    return false;
  }catch(e){
    Logger.log("paintMeetingDone: "+e);
    return false;
  }
}


function paintOfflineDayDone(dateStr){
  // Красит все «Офлайн» события этого дня: зелёный цвет и «✅» в начале
  var parts=String(dateStr||"").split(".");
  if(parts.length<2) return 0;
  var dd=parseInt(parts[0]), mo=parseInt(parts[1])-1;
  var yr=(parts.length>2 && parts[2]) ? parseInt(parts[2]) : new Date().getFullYear();
  if(yr<100) yr+=2000;
  var bsCalId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
  var cal=bsCalId ? CalendarApp.getCalendarById(bsCalId) : null;
  if(!cal) return 0;
  var n=0;
  cal.getEvents(new Date(yr,mo,dd,0,0,0), new Date(yr,mo,dd,23,59,59)).forEach(function(ev){
    var t=String(ev.getTitle()||"");
    if(t.indexOf("Офлайн")<0 || t.indexOf("✅")===0) return;
    ev.setColor(CalendarApp.EventColor.GREEN);
    ev.setTitle("✅ "+t);
    n++;
  });
  return n;
}

function _miniConfirmMeeting(p){
  // Mini App "Встреча прошла". ставит галочку, логирует, удаляет из Расписания
  // Все ошибки и шаги отправляются админу в Telegram
  var resName=String(p.res||"");
  if(!resName)return{error:"No res name"};
  if(p.chatId && !isAdmin(p.chatId)) return{error:"Отмечать встречи может только команда"};
  
  // Защита от двойного вызова (Telegram WebApp может слать дубли)
  var confirmCache=CacheService.getScriptCache();
  var confirmKey="confirm_"+resName+"_"+(p.date||"")+"_"+(p.time||"");
  if(confirmCache.get(confirmKey)){
    Logger.log("Дубль _miniConfirmMeeting заблокирован: "+confirmKey);
    return{ok:true, duplicate:true};
  }
  confirmCache.put(confirmKey,"1",60); // блок на 60 секунд
  
  var status = {check:false, log:false, delete:false, calendarDeleted:false, residentNotified:false};
  var errors = [];
  var ss=SpreadsheetApp.openById(SS_ID);
  
  // 1. Ставим галочку в Посещения и инкрементируем циклы
  try{
    _addCheckForResident(resName);
    status.check=true;
  }catch(e){errors.push("Галочка: "+e.message);}
  
  // 2. Логируем встречу ДАТОЙ ВСТРЕЧИ (не датой нажатия)
  try{
    _logMeetingHappened(resName, String(p.date||""));
    status.log=true;
  }catch(e){errors.push("Лог встреч: "+e.message);}
  
  // 3. Удаляем запись из Расписания
  // Mini App шлёт date в формате "dd.MM" (без года). Сравниваем именно так.
  try{
    var wsSch=ss.getSheetByName("Расписание");
    if(wsSch){
      var lr=wsSch.getLastRow();
      if(lr>=3){
        var data=wsSch.getRange(3,2,lr-2,3).getValues();
        var targetDate=String(p.date||"").trim();
        // Извлекаем dd.MM из любого формата (dd.MM, dd.MM.yyyy, yyyy-MM-dd)
        var extractDDMM=function(s){
          if(!s)return "";
          // dd.MM.yyyy → dd.MM
          var m1=String(s).match(/^(\d{1,2})\.(\d{1,2})/);
          if(m1){
            var d=parseInt(m1[1]), mn=parseInt(m1[2]);
            return (d<10?"0":"")+d+"."+(mn<10?"0":"")+mn;
          }
          // yyyy-MM-dd → dd.MM
          var m2=String(s).match(/^(\d{4})-(\d{2})-(\d{2})/);
          if(m2){
            return m2[3]+"."+m2[2];
          }
          return "";
        };
        var targetDDMM=extractDDMM(targetDate);
        var targetTime=String(p.time||"").trim();
        
        var foundRow=-1;
        var debug=[];
        for(var i=0;i<data.length;i++){
          var rowName=String(data[i][0]||"").trim();
          if(rowName!==resName)continue;
          var rowDate=data[i][1];
          var rowTime=data[i][2];
          var rowDDMM="";
          if(rowDate instanceof Date){
            rowDDMM=Utilities.formatDate(rowDate,"Asia/Almaty","dd.MM");
          } else {
            rowDDMM=extractDDMM(String(rowDate||""));
          }
          // Время: может быть Date или строка
          var rowTimeStr="";
          if(rowTime instanceof Date){
            rowTimeStr=Utilities.formatDate(rowTime,"Asia/Almaty","HH:mm");
          } else {
            rowTimeStr=String(rowTime||"").trim();
          }
          debug.push("стр"+(i+3)+": "+rowName+" "+rowDDMM+" "+rowTimeStr);
          // Совпадение по дате обязательно, по времени желательно
          if(rowDDMM===targetDDMM){
            // Если есть точное время. сверяем
            if(targetTime && rowTimeStr){
              if(rowTimeStr===targetTime){
                foundRow=3+i;
                break;
              }
            } else {
              foundRow=3+i;
              break;
            }
          }
        }
        if(foundRow>0){
          // Перед удалением запоминаем event_id (столбец 9 = K)
          var eventId="";
          try{
            eventId=String(wsSch.getRange(foundRow,10).getValue()||"");
          }catch(idErr){}
          
          // Строка остаётся в Расписании как история: красим зелёным и ставим галочку.
          // Раньше строка удалялась, и вместе с ней терялся ID события календаря
          wsSch.getRange(foundRow,1,1,10).setBackground("#E8F5E9");
          var curStatus=String(wsSch.getRange(foundRow,7).getValue()||"");
          if(curStatus.indexOf("✅")<0) wsSch.getRange(foundRow,7).setValue("✅ Проведена");
          SpreadsheetApp.flush();
          status.delete=true;
          
          // Удаляем событие из Google Calendar
          try{
            var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
            if(!calId){
              calId=CalendarApp.getDefaultCalendar().getId();
            }
            // Красим событие: одна функция, один путь, честный результат
            var painted = paintMeetingDone(resName, targetDDMM, eventId || "");
            status.calendarDeleted = painted;
            if(!painted) errors.push("Календарь: событие не найдено");
          }catch(calE){
            var errMsg=String(calE.message||calE);
            // Not Found / 404 / Resource has been deleted = событие уже удалено, это НЕ ошибка
            // Больше не помечаем успех при ошибке: раньше система рапортовала
            // об успешной покраске, когда событие не трогалось вовсе
            status.calendarDeleted=false;
            errors.push("Календарь: "+errMsg);
          }
        } else {
          // Строки в Расписании нет, но событие в календаре может быть. красим его
          try{
            if(paintMeetingDone(resName, targetDDMM, "")) status.calendarDeleted=true;
          }catch(pe){Logger.log("paint fallback: "+pe);}
          Logger.log("Расписание: строка не найдена для "+resName+" "+targetDDMM);
        }
      }
    }
  }catch(e){errors.push("Удаление: "+e.message);}
  
  // 4. Сообщение резиденту уже отправлено из _addCheckForResident → _onCheckbox
  // Здесь только помечаем как успех (дубль не шлём)
  status.residentNotified=true;
  
  // 5. Уведомляем админов с полным статусом
  var summary="✅ Встреча с "+resName+" "+(p.date||"")+(p.time?" "+p.time:"")+". обработка завершена\n\n";
  summary+=(status.check?"✅":"❌")+" Галочка поставлена в Посещения\n";
  summary+=(status.log?"✅":"❌")+" Записано в Лог встреч (защита от штрафа)\n";
  summary+=(status.delete?"✅":"❌")+" Отмечено в Расписании\n";
  summary+=(status.calendarDeleted?"✅":"⚠️")+" Отмечено зелёным в Google Calendar\n";
  summary+=(status.residentNotified?"✅":"❌")+" Сообщение резиденту отправлено\n";
  if(errors.length>0){
    summary+="\n⚠️ Ошибки:\n"+errors.map(function(e){return "• "+e;}).join("\n");
  }
  if(errors.length) bsSysNote(summary);
  return{ok:true, status:status, errors:errors};
}

function _miniAddPayment(p){
  var type=p.type,src=p.src,amount=Number(p.amount)||0,isCash=p.isCash==="true";
  var resident=p.resident||"";
  
  // ЗАЩИТА 1: кеш-дедуп 120 сек (было 10. человек кликал повторно через 15-30 сек не веря что сработало)
  var dedupKey="pay_dedup_"+type+"_"+src+"_"+amount+"_"+resident+"_"+(isCash?"1":"0");
  var dedupCache=CacheService.getScriptCache();
  if(dedupCache.get(dedupKey)){
    Logger.log("_miniAddPayment: дубль заблокирован "+dedupKey);
    return{ok:true, deduplicated:true};
  }
  dedupCache.put(dedupKey,"1",120);
  
  // ЗАЩИТА 2 (окончательная, по ДАННЫМ ТАБЛИЦЫ, реальная структура ДДС):
  // A=дата (без времени!), B=приход, C=расход, D=источник, E=Кат+, F=Кат-
  // БЫЛО СЛОМАНО: старая проверка сверяла не те колонки и никогда не срабатывала.
  // Дата в ДДС пишется БЕЗ времени, поэтому окно "минут" невозможно. правило:
  // идентичная запись (тот же тип, сумма, источник/категория) ЗА СЕГОДНЯ = дубль
  try{
    var ssPay=SpreadsheetApp.openById(SS_ID);
    var wsD=ssPay.getSheetByName("Учет ДДС");
    if(wsD){
      var lrD=wsD.getLastRow();
      if(lrD>=2){
        var startD=Math.max(2,lrD-19); // последние 20 строк
        var recent=wsD.getRange(startD,1,lrD-startD+1,6).getValues();
        var todayStr=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy");
        var isIncome=(type==="income");
        for(var ri=0;ri<recent.length;ri++){
          var rDate=recent[ri][0];
          if(!(rDate instanceof Date))continue;
          if(Utilities.formatDate(rDate,"Asia/Almaty","dd.MM.yyyy")!==todayStr)continue;
          var rInc=Number(recent[ri][1])||0;
          var rExp=Number(recent[ri][2])||0;
          var rSrc=String(recent[ri][3]||"").trim();   // источник (для прихода)
          var rCatM=String(recent[ri][5]||"").trim();  // категория расхода
          if(isIncome){
            if(rInc===amount && rSrc===String(src).trim()){
              Logger.log("_miniAddPayment: приход-дубль за сегодня (строка "+(startD+ri)+")");
              return{ok:true, deduplicated:true, byData:true};
            }
          } else {
            if(rExp===amount && rCatM===String(src).trim()){
              Logger.log("_miniAddPayment: расход-дубль за сегодня (строка "+(startD+ri)+")");
              return{ok:true, deduplicated:true, byData:true};
            }
          }
        }
      }
    }
  }catch(payChkE){Logger.log("pay data check: "+payChkE);}
  
  _recordPaymentFromBot("mini",amount,src,type,isCash,resident);
  try{
    if(type==="income" && resident && String(src||"").indexOf("Штраф")<0){
      addMeetingsOnPayment(resident, amount);
    }
  }catch(cyE){Logger.log("cycles after payment: "+cyE);}
  return{ok:true};
}

function doPost(e){
  if(bsIsCopy()) return;
  // КЛАССИЧЕСКАЯ простая версия. без очередей, без PropertiesService
  var output=ContentService.createTextOutput("OK");
  if(!e||!e.postData||!e.postData.contents)return output;
  try{
    var u=JSON.parse(e.postData.contents);

    // ── Запись с сервера платформы ──
    if(u && u.bsAction === "srv") return bsServerWrite(u);

    // Картинки из приложения: только через сервер, когда он это требует
    if(u && (u.bsAction === "uploadImage" || u.bsAction === "sendImage") && bsServerOwns("app_gateway") && !bsAppSigOk(u, u.bsAction))
      return bsAppRefused();

    // ── Загрузка картинки для карусели ──
    if(u && u.bsAction === "uploadImage"){
      var outImg=ContentService.createTextOutput();
      outImg.setMimeType(ContentService.MimeType.JSON);
      try{
        var upRes=uploadImageToGithub(u.image, u.fileName||("bs_"+Date.now()+".png"));
        outImg.setContent(JSON.stringify(upRes));
      }catch(ue){
        outImg.setContent(JSON.stringify({ok:false, error:String(ue)}));
      }
      return outImg;
    }

    // ── Лид из Tilda или рекламного кабинета ──
    // Tilda: в настройках формы добавить вебхук на адрес приложения
    if(u && (u.bsAction === "lead" || u.tilda || u.formname || u.Phone || u.phone)){
      var outLead = ContentService.createTextOutput();
      outLead.setMimeType(ContentService.MimeType.JSON);
      try{
        // Tilda шлёт поля как есть, названия могут отличаться
        var lead = {
          name: u.name || u.Name || u.imya || u.Имя || "",
          phone: u.phone || u.Phone || u.tel || u.Телефон || "",
          telegram: u.telegram || u.tg || "",
          source: u.source || u.utm_source || (u.formname ? ("Tilda: "+u.formname) : "Сайт"),
          campaign: u.utm_campaign || u.campaign || "",
          niche: u.niche || u.nisha || u.Ниша || "",
          revenue: u.revenue || u.oborot || u.Оборот || "",
          request: u.request || u.comment || u.message || u.Комментарий || "",
          comment: [u.utm_medium, u.utm_content, u.utm_term].filter(Boolean).join(" · ")
        };
        var resLead = addLead(lead);
        outLead.setContent(JSON.stringify(resLead));
      }catch(le){
        outLead.setContent(JSON.stringify({ok:false, error:String(le)}));
      }
      return outLead;
    }

    // ── Картинка из приложения: бот отправляет её пользователю в чат ──
    // В Telegram WebView скачивание файлов заблокировано, а shareToStory требует
    // публичный URL. Поэтому картинку отправляет бот. дальше человек делится ей одним касанием
    if(u && u.bsAction === "sendImage"){
      var out2 = ContentService.createTextOutput();
      out2.setMimeType(ContentService.MimeType.JSON);
      try{
        var cid = String(u.chatId||"").trim();
        var b64 = String(u.image||"");
        var caption = String(u.caption||"");
        if(!cid || !b64){ out2.setContent(JSON.stringify({error:"Нет данных"})); return out2; }
        b64 = b64.replace(/^data:image\/\w+;base64,/, "");
        var blob = Utilities.newBlob(Utilities.base64Decode(b64), "image/jpeg", "bs_story.jpg");
        var resp = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendPhoto", {
          method: "post",
          payload: {
            chat_id: cid,
            photo: blob,
            caption: caption,
            parse_mode: "HTML"
          },
          muteHttpExceptions: true
        });
        var res = JSON.parse(resp.getContentText());
        out2.setContent(JSON.stringify(res.ok ? {ok:true} : {error:String(res.description||"Telegram error")}));
      }catch(imgE){
        out2.setContent(JSON.stringify({error:String(imgE)}));
      }
      return out2;
    }
    // Ответ Telegram: HtmlService отдаёт 200 сразу.
    // ContentService отдаёт редирект 302, Telegram считает доставку неудачной и шлёт снова
    var tgOk=HtmlService.createHtmlOutput("ok");

    // Старые сообщения: отчёты из группы принимаем до 30 часов (Telegram хранит очередь сутки),
    // остальное старше часа пропускаем, чтобы не выполнять устаревшие команды
    var msgObj=u.message||u.edited_message||null;
    var msgDate=(msgObj&&msgObj.date)||0;
    var isGroupMsg=!!(msgObj&&msgObj.chat&&(msgObj.chat.type==="group"||msgObj.chat.type==="supergroup"));
    var ageSec=msgDate?Math.floor(Date.now()/1000)-msgDate:0;
    // Отчёт из группы принимаем до 72 часов: он ложится в лог по своей дате,
    // повтор отсекает update_id. Потерять отчёт хуже, чем обработать поздно
    if(msgDate && ageSec>(isGroupMsg?72*3600:3600)) return tgOk;

    // Пульс: отмечаем, что Telegram до нас достучался. По этой метке ночная
    // проверка понимает, жив ли вебхук, и не штрафует вслепую
    if(u.update_id){
      try{
        var _hb=CacheService.getScriptCache();
        if(!_hb.get("hb")){                       // не чаще раза в 10 минут
          _hb.put("hb","1",600);
          PropertiesService.getScriptProperties()
            .setProperty("BS_LAST_UPDATE_TS", String(new Date().getTime()));
        }
      }catch(hbE){}
    }

    // Защита от повторов: помечаем обновление до обработки, чтобы повтор во время обработки не задвоил
    if(u.update_id){
      var cache=CacheService.getScriptCache();
      if(cache.get("upok"+u.update_id) || cache.get("upgo"+u.update_id)) return tgOk;
      cache.put("upgo"+u.update_id,"1",600);
    }
    _upd(u);
    if(u.update_id)CacheService.getScriptCache().put("upok"+u.update_id,"1",21600);
    return tgOk;
  }catch(err){
    Logger.log("doPost ERROR: "+err);
    try{ return HtmlService.createHtmlOutput("ok"); }catch(e2){}
  }
  return output;
}

function processUpdateQueue(){
  // Триггер каждую минуту. обрабатывает накопленные обновления
  var props=PropertiesService.getScriptProperties();
  var all=props.getProperties();
  var keys=Object.keys(all).filter(function(k){return k.indexOf("upd_")===0;}).sort();
  if(!keys.length)return;
  Logger.log("processUpdateQueue: "+keys.length+" updates");
  var cache=CacheService.getScriptCache();
  // Обрабатываем не более 20 за раз (защита от таймаута)
  var maxBatch=Math.min(keys.length,20);
  for(var i=0;i<maxBatch;i++){
    var k=keys[i];
    var body=all[k];
    try{
      var u=JSON.parse(body);
      // Игнорируем старые (>1 час)
      var msgDate=(u.message&&u.message.date)||(u.edited_message&&u.edited_message.date)||0;
      if(msgDate&&Math.floor(Date.now()/1000)-msgDate>3600){
        props.deleteProperty(k);continue;
      }
      // Дедупликация по update_id
      if(u.update_id){
        var okKey="upok"+u.update_id;
        if(cache.get(okKey)){props.deleteProperty(k);continue;}
      }
      // Обработка
      _upd(u);
      if(u.update_id)cache.put("upok"+u.update_id,"1",21600);
      props.deleteProperty(k);
    }catch(procErr){
      Logger.log("processUpdate "+k+": "+procErr);
      // Если ошибка обработки. удаляем чтобы не зацикливаться
      props.deleteProperty(k);
    }
  }
}

function _upd(u){
  if(bsIsCopy()) return;
  // Личные сообщения и кнопки: если их ведёт сервер, таблица молчит
  if(bsServerOwns("bot_private")){
    var _pm = u.message || u.edited_message;
    if(u.callback_query || (_pm && _pm.chat && _pm.chat.type === "private")) return;
  }
  // Обработка callback (кнопки inline)
  if(u.callback_query){
    var cb=u.callback_query;
    var cid=String(cb.message.chat.id);
    var data=cb.data;
    var from=cb.from;
    var name=(from.first_name||"")+(from.last_name?" "+from.last_name:"");
    // ПЕРВЫМ делом отвечаем на callback. убирает "часики" и не даёт Telegram повторять
    try{
      UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/answerCallbackQuery",{
        method:"post",contentType:"application/json",
        payload:JSON.stringify({callback_query_id:cb.id}),
        muteHttpExceptions:true
      });
    }catch(aq){}
    // Дедупликация callback по cb.id. блокируем повторные нажатия одной кнопки
    var cbCache=CacheService.getScriptCache();
    var cbKey="cb_"+cb.id;
    if(cbCache.get(cbKey)){return ContentService.createTextOutput("OK");}
    cbCache.put(cbKey,"1",30);
    if(data==="accept_terms"){
      // Защита от дублей. если за 60 секунд этот юзер уже акцептовал, игнорируем
      try{
        var cAcc=CacheService.getScriptCache();
        var accGuardKey="accept_guard_"+cid;
        if(cAcc.get(accGuardKey)){
          // Только подтверждаем callback и выходим
          try{UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/answerCallbackQuery",{method:"post",contentType:"application/json",payload:JSON.stringify({callback_query_id:cb.id}),muteHttpExceptions:true});}catch(e){}
          return;
        }
        cAcc.put(accGuardKey,"1",60);
      }catch(gE){}
      // Сохраняем акцепт в лист "Акцепты"
      try{
        var ssAcc=SpreadsheetApp.openById(SS_ID);
        var wsAcc=ssAcc.getSheetByName("Акцепты");
        if(!wsAcc){
          // Создаём лист если не существует
          wsAcc=_insertSheetAtEnd(ssAcc,"Акцепты");
          wsAcc.getRange(1,1,1,7).setValues([["Дата/время","Chat ID","Username","First Name","Версия оферты","Версия политики","Источник"]]);
          wsAcc.getRange(1,1,1,7).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
          wsAcc.setFrozenRows(1);
        }
        // Проверяем что ещё не записан
        var lrAcc=wsAcc.getLastRow();
        var alreadyAccepted=false;
        if(lrAcc>=2){
          var ids=wsAcc.getRange(2,2,lrAcc-1,1).getValues();
          for(var ai=0;ai<ids.length;ai++){
            if(String(ids[ai][0])===String(cid)){alreadyAccepted=true;break;}
          }
        }
        if(!alreadyAccepted){
          var acceptDate=new Date();
          wsAcc.appendRow([
            acceptDate,
            String(cid),
            from.username||"",
            name||"",
            OFERTA_VERSION,
            PRIVACY_VERSION,
            "Telegram /start"
          ]);
          wsAcc.getRange(wsAcc.getLastRow(),1).setNumberFormat("DD.MM.YYYY HH:mm:ss");
        }
      }catch(accErr){Logger.log("Accept save: "+accErr);}

      // Убираем из напоминаний
      try{
        var aC=CacheService.getScriptCache();
        var aP=aC.get("pending_users");
        if(aP){var aPObj=JSON.parse(aP);delete aPObj[cid];aC.put("pending_users",JSON.stringify(aPObj),25*60*60);}
      }catch(ae){}

      // Сообщение об успешном акцепте
      var dateStr=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy HH:mm");

      // Проверяем. это уже резидент?
      var isResident=false;
      try{
        var ssCh=SpreadsheetApp.openById(SS_ID);
        var wsResCh=ssCh.getSheetByName("BS - резиденты дебет");
        if(wsResCh){
          var lrRC=wsResCh.getLastRow();
          if(lrRC>=3){
            var resIds=wsResCh.getRange(3,14,lrRC-2,1).getValues(); // N=Chat ID
            for(var ri=0;ri<resIds.length;ri++){
              if(String(resIds[ri][0]).trim()===String(cid)){isResident=true;break;}
            }
          }
        }
      }catch(rE){Logger.log("check resident: "+rE);}

      if(isResident){
        // Существующий резидент. просто приветствие
        tgSend(cid,"✅ Условия приняты\n\nДата: "+dateStr+"\nВерсия: "+OFERTA_VERSION+"\n\nДобро пожаловать обратно 🧬");
        _startOnboarding(cid,name);
      } else {
        // НОВЫЙ подписчик. записываем в "Подписчики канала"
        try{
          _addSubscriber(cid, from.username||"", name||"");
        }catch(sE){Logger.log("addSub: "+sE);}

        // Welcome для подписчика + счётчик подписчиков + лид-магниты
        var subscribersCount = _getSubscribersCount();
        var counterLine = subscribersCount > 0 
          ? "\n\n🔥 Уже "+subscribersCount+" владельцев бизнеса с нами"
          : "";
        tgSend(cid,
          "✅ Условия успешно приняты\n\nДата: "+dateStr+"\nВерсия оферты: "+OFERTA_VERSION+"\nВерсия политики: "+PRIVACY_VERSION+"\n\n🧬 Добро пожаловать в Business Surgery"+counterLine+"\n\nВыбери что интересует:",
          {reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"📚 Получить чек-листы", callback_data:"sub_leadmagnets"}],
            [{text:"📞 Записаться на разбор (50 000 ₸)", callback_data:"sub_express"}],
            [{text:"📅 Узнать про BS трекинг", callback_data:"sub_about"}]
          ]})}
        );

        // Через 30 минут запустится квал-вопрос. отложенная задача через свойство
        try{
          PropertiesService.getScriptProperties().setProperty("qual_q_"+cid, JSON.stringify({
            name: name, username: from.username||"", scheduledAt: Date.now()+30*60*1000
          }));
        }catch(qE){}
      }

      // Уведомляем админов
      var statusLabel = isResident ? "(резидент)" : "(подписчик)";
      ADMIN_IDS.forEach(function(aid){
        if(String(aid)!==String(cid)){
          tgSend(aid,"✅ "+name+(from.username?" @"+from.username:"")+" "+statusLabel+"\n🆔 "+cid+"\nВерсия оферты: "+OFERTA_VERSION);
        }
      });
    }
    // Callback подписчика: лид-магниты
    else if(data==="sub_leadmagnets"){
      tgSend(cid,"📚 Выбери чек-лист:",{reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"🔬 Реанимация продаж", callback_data:"lm_sales"}],
        [{text:"🧬 Анатомия бизнеса", callback_data:"lm_unit"}],
        [{text:"🩺 Хирургия рутины", callback_data:"lm_delegate"}],
        [{text:"🤝 Найм команды", callback_data:"lm_hire"}],
        [{text:"📊 Маркетинг без бюджета", callback_data:"lm_marketing"}],
        [{text:"💰 Финансы под контролем", callback_data:"lm_cashflow"}],
        [{text:"💬 Скрипты для WhatsApp", callback_data:"lm_scripts"}],
        [{text:"🧬 Команда без выгорания", callback_data:"lm_team_culture"}],
        [{text:"📊 Метрики собственника", callback_data:"lm_metrics"}],
        [{text:"🚨 Кризис в бизнесе", callback_data:"lm_crisis"}]
      ]})});
      return;
    }
    else if(data==="sub_express"){
      tgSend(cid,"📞 Экспресс-разбор. 50 000 ₸\n\n• 60 минут с фаундером BS (Рустам или Береке)\n• Разбираем твою воронку и финмодель\n• Получаешь дорожную карту следующих 30 дней\n\nОплата через Kaspi: "+KASPI_LINK+"\n\nПосле оплаты пришли скриншот сюда. назначим время",{reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"💳 Оплатить через Kaspi", url:KASPI_LINK}],
        [{text:"💬 Связаться с фаундером", url:"https://t.me/qabden"}]
      ]})});
      try{_setSubscriberField(cid,"Статус","Записался на разбор");}catch(e){}
      ADMIN_IDS.forEach(function(aid){if(String(aid)!==String(cid))tgSend(aid,"⚡ "+name+(from.username?" @"+from.username:"")+" жмёт на разбор 50к");});
      return;
    }
    else if(data==="sub_not_relevant"){
      try{_setSubscriberField(cid,"Статус","Отказ");}catch(e){}
      tgSend(cid,"Понял. Если что. канал @bsurgery_kz открыт, заходи когда захочешь");
      ADMIN_IDS.forEach(function(aid){if(String(aid)!==String(cid))tgSend(aid,"❌ Отказ: "+name+(from.username?" @"+from.username:""));});
      return;
    }
    else if(data==="sub_about"){
      tgSend(cid,"📅 Business Surgery. закрытый клуб для предпринимателей 2-20 млн ₸\n\n🎯 Что делаем:\n• Трекинг каждые 10 дней\n• Хирургическая точность в работе с твоим бизнесом\n• Среда предпринимателей с прибылью 2-20 млн ₸\n• х3 средний рост резидента\n\n💰 Тарифы:\n• Стандарт 3 мес. 500 000 ₸\n• Годовой. 1 250 000 ₸\n\nНачни с экспресс-разбора чтобы понять подходит ли тебе",{reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"📞 Записаться на разбор (50 000 ₸)", callback_data:"sub_express"}],
        [{text:"🌐 Сайт", url:"https://bxclub.kz"}],
        [{text:"💬 Связаться", url:"https://t.me/qabden"}]
      ]})});
      return;
    }
    // Загрузка лид-магнита. выбор слота для file_id
    else if(data.indexOf("upl_")===0){
      if(!isAdmin(cid)){
        tgSend(cid,"Доступ только администраторам");
        return;
      }
      if(data==="upl_cancel"){
        CacheService.getScriptCache().remove("pending_file_"+cid);
        tgSend(cid,"❌ Загрузка отменена");
        return;
      }
      // Формат: upl_<key>_<short_file_id>. но мы храним полный file_id в cache
      var rest=data.substring(4);
      var sepIdx=rest.indexOf("_");
      var lmKey=sepIdx>0?rest.substring(0,sepIdx):rest;
      var fullFileId=CacheService.getScriptCache().get("pending_file_"+cid);
      if(!fullFileId){
        tgSend(cid,"⚠️ Файл устарел. Пришли PDF заново.");
        return;
      }
      var ok=_saveLeadmagnetFileId(lmKey,fullFileId,name);
      CacheService.getScriptCache().remove("pending_file_"+cid);
      var lmInfo=_getLeadmagnetByKey(lmKey);
      if(ok && lmInfo){
        tgSend(cid,"✅ <b>"+lmInfo.title+"</b> загружен!\n\nПодписчики теперь получат этот PDF при выборе соответствующей кнопки в боте.",{parse_mode:"HTML"});
      } else {
        tgSend(cid,"❌ Не удалось сохранить. Возможно ключ '"+lmKey+"' не найден.");
      }
      return;
    }
    // Лид-магниты. выдача PDF через file_id
    else if(data.indexOf("lm_")===0){
      var lmKey=data.substring(3);
      var lm=_getLeadmagnetByKey(lmKey);
      if(!lm){tgSend(cid,"Чек-лист не найден");return;}
      if(!lm.fileId){
        tgSend(cid,"📄 "+lm.title+"\n\nЭтот чек-лист ещё готовится. Подпишись на канал @bsurgery_kz. пришлём первым");
        return;
      }
      var caption="📚 <b>"+lm.title+"</b>\n\n💡 Применил из чек-листа? Запишись на экспресс-разбор. разберём как внедрить в твой бизнес";
      var sent=_sendLeadmagnetDocument(cid,lm.fileId,caption);
      if(sent){
        // Дополнительное сообщение с кнопками
        tgSend(cid,"Получил пользу? Что дальше:",{reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"📞 Записаться на разбор (50 000 ₸)", callback_data:"sub_express"}],
          [{text:"📚 Другие чек-листы", callback_data:"sub_leadmagnets"}]
        ]})});
      } else {
        tgSend(cid,"⚠️ Не удалось отправить файл. Попробуй позже");
      }
      try{_setSubscriberField(cid,"Лид-магнит",lm.title);}catch(e){}
      return;
    }
    // Квал-вопросы. ответы
    else if(data.indexOf("qual_niche_")===0){
      var niche=data.substring(11);
      try{_setSubscriberField(cid,"Ниша",niche);}catch(e){}
      tgSend(cid,"📊 Какой текущий месячный доход?",{reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"до 1 млн ₸", callback_data:"qual_rev_lt1"}],
        [{text:"1-2 млн ₸", callback_data:"qual_rev_1_2"}],
        [{text:"2-5 млн ₸", callback_data:"qual_rev_2_5"}],
        [{text:"5-10 млн ₸", callback_data:"qual_rev_5_10"}],
        [{text:"10-20 млн ₸", callback_data:"qual_rev_10_20"}],
        [{text:"20+ млн ₸", callback_data:"qual_rev_20p"}]
      ]})});
      return;
    }
    else if(data.indexOf("qual_rev_")===0){
      var rev=data.substring(9);
      var revMap={"lt1":"<1","1_2":"1-2","2_5":"2-5","5_10":"5-10","10_20":"10-20","20p":"20+"};
      try{_setSubscriberField(cid,"Доход (млн ₸)",revMap[rev]||rev);}catch(e){}
      tgSend(cid,"🎯 Что главное хочешь изменить за 90 дней?\n\nКоротко в одном сообщении. увеличить выручку, упорядочить продажи, нанять команду, и т.д.");
      try{
        var props=PropertiesService.getScriptProperties();
        props.setProperty("qual_await_goal_"+cid,"1");
      }catch(e){}
      // Уведомляем админов о квалификации
      ADMIN_IDS.forEach(function(aid){if(String(aid)!==String(cid))tgSend(aid,"📊 Квал: "+name+(from.username?" @"+from.username:"")+"\nДоход: "+(revMap[rev]||rev)+" млн ₸");});
      return;
    }
    // Публикация поста через /post
    else if(data==="publish_post"){
      if(!isAdmin(cid)){return;}
      var cacheP2=CacheService.getScriptCache();
      var postText=cacheP2.get("pending_post_"+cid);
      if(!postText){tgSend(cid,"Пост устарел (10 мин). Попробуй /post снова");return;}
      var pubResult=_sendToChannel(postText);
      if(pubResult&&pubResult.ok){
        tgSend(cid,"✅ Опубликовано в "+CHANNEL_USERNAME);
        cacheP2.remove("pending_post_"+cid);
      } else {
        tgSend(cid,"❌ Ошибка публикации: "+JSON.stringify(pubResult));
      }
      return;
    }
    else if(data==="regen_post"){
      tgSend(cid,"Отправь /post <идея> заново. сгенерирую другой вариант");
      return;
    }
    else if(data==="cancel_post"){
      var cacheP3=CacheService.getScriptCache();
      cacheP3.remove("pending_post_"+cid);
      tgSend(cid,"❌ Отменено");
      return;
    }
    // Меню администратора. callback
    else if(data==="admin_summary"){_sendSummary();return;}
    else if(data==="admin_fines"){_sendFines();return;}
    else if(data==="admin_residents"){_sendResidents();return;}
    else if(data==="admin_analytics"){_sendAnalytics(String(cb.message.chat.id));return;}
    else if(data==="admin_debet"){_sendDebetInfo(String(cb.message.chat.id));return;}
    else if(data==="admin_today_reports"){_sendTodayReports(String(cb.message.chat.id));return;}
    else if(data==="sub_diagnostic_request"){
      tgSend(cid,"📞 <b>Записаться на разбор</b>\n\nНапиши коротко:\n1. Имя\n2. Телефон/WhatsApp\n3. Ниша бизнеса\n4. Главный запрос\n\nИли набери @imbahirr в Telegram. отвечу сразу.",{parse_mode:"HTML"});
      try{
        ADMIN_IDS.forEach(function(aid){
          tgSend(aid,"⚡ Клиент 🆔"+cid+" ("+name+") нажал 'Записаться на разбор' после диагностики. Свяжись с ним.");
        });
      }catch(e){}
      return;
    }
    else if(data.indexOf("diag_start")===0){
      _startBotDiagnostic(cid, name);
      return;
    }
    else if(data==="want_diag"){
      // Заявка на разбор из прогрева: сразу в CRM и уведомление админам
      try{
        var un=from&&from.username?("@"+from.username):"";
        addLead({
          name:name, telegram:un, source:"Бот: прогрев",
          request:"Нажал «Хочу разбор» в прогреве",
          comment:"chatId "+cid
        });
      }catch(e){}
      tgSend(cid,"Отлично. Свяжемся в ближайшее время и подберём удобное время.\n\n"+
        "Чтобы разговор был предметным, подготовьте:\n"+
        "выручку и прибыль за три месяца,\n"+
        "сколько человек в команде,\n"+
        "главный вопрос, который хотите решить.");
      ADMIN_IDS.forEach(function(aid){
        try{ tgSend(aid,"🔥 Заявка на разбор\n\n"+name+(from&&from.username?" @"+from.username:"")+"\n🆔 "+cid+"\n\nИсточник: прогрев после чек-листа"); }catch(e){}
      });
      return;
    }
    else if(data==="sub_menu"){
      _sendSubscriberWelcome(cid, String(name||"").split(" ")[0]);
      return;
    }
    else if(data.indexOf("diag_a_")===0){
      // diag_a_<areaIdx>_<questionIdx>_<score>
      var parts=data.substring("diag_a_".length).split("_");
      _processDiagnosticAnswer(cid, name, parseInt(parts[0]), parseInt(parts[1]), parseInt(parts[2]));
      return;
    }
    else if(data==="diag_restart"){
      _resetDiagnosticState(cid);
      _startBotDiagnostic(cid, name);
      return;
    }
    else if(data==="i_am_resident"){
      // Пользователь нажал "Я резидент BS". Шлём заявку админам
      var firstName = "";
      try{
        var u = update.callback_query.from;
        firstName = (u.first_name || "") + (u.last_name ? " " + u.last_name : "");
      }catch(e){}
      var userTg = "";
      try{ userTg = update.callback_query.from.username || ""; }catch(e){}
      
      var result = _miniRequestResident({
        chatId: String(cid),
        userName: firstName,
        userTg: userTg
      });
      
      if(result.alreadyResident){
        tgSend(cid, "✅ Ты уже резидент BS. Можно открыть приложение и пользоваться.");
        return;
      }
      if(result.alreadyPending){
        tgSend(cid, "⏳ Заявка уже отправлена. Береке и Рустам подтвердят в течение дня.");
        return;
      }
      if(result.error){
        tgSend(cid, "❌ " + result.error);
        return;
      }
      // ok=true. подтверждение отправилось пользователю внутри _miniRequestResident
      return;
    }
    else if(data.indexOf("approve_res_")===0){
      var targetCid=data.substring("approve_res_".length);
      var result=_miniApproveResident({chatId:targetCid});
      if(result.ok){
        tgSend(cid,"✅ Подтверждено. Оферта отправлена резиденту.");
      } else {
        tgSend(cid,"❌ "+(result.error||"Ошибка"));
      }
      return;
    }
    else if(data.indexOf("reject_res_")===0){
      var targetCid2=data.substring("reject_res_".length);
      try{
        var ss=SpreadsheetApp.openById(SS_ID);
        var ws=ss.getSheetByName("Подписчики канала");
        if(ws){
          var lr=ws.getLastRow();
          if(lr>=2){
            var d=ws.getRange(2,1,lr-1,2).getValues();
            for(var i=0;i<d.length;i++){
              if(String(d[i][1])===targetCid2){
                ws.getRange(i+2, RC.source).setValue("rejected");
                break;
              }
            }
          }
        }
        tgSend(targetCid2,"К сожалению, мы не нашли тебя в списке резидентов BS. Если ты считаешь что это ошибка. напиши админу.");
        tgSend(cid,"❌ Отклонено. Сообщение отправлено пользователю.");
      }catch(e){tgSend(cid,"Ошибка: "+e.message);}
      return;
    }
    else if(data==="admin_menu"){_sendAdminMenu(String(cb.message.chat.id));return;}
    // Штраф пошагово
    else if(data==="fine_step1"){_sendFineStep1(String(cb.message.chat.id));return;}
    else if(data.indexOf("fine_res_")===0){
      var fRes=data.substring(9);
      _sendFineStep2(String(cb.message.chat.id),fRes);return;
    }
    else if(data.indexOf("fine_type_")===0){
      var fParts=data.substring(10).split("_");
      var fType=fParts[0],fAmt=parseInt(fParts[1]),fName=fParts.slice(2).join("_");
      var fCid=String(cb.message.chat.id);
      if(fAmt>0){
        _adminAddFineFromBot(fCid,fName,fType,fAmt);
      } else {
        // Другое. запрашиваем сумму
        var cache3=CacheService.getScriptCache();
        cache3.put("fine_pending_"+fCid,JSON.stringify({name:fName,type:fType}),300);
        tgSend(fCid,"Введите сумму штрафа цифрами:");
      }
      return;
    }
    // Расписание пошагово
    else if(data==="event_step1"){
      // Выбор типа мероприятия
      if(!isAdmin(cid))return;
      tgSend(cid,"🎬 Выбери тип мероприятия:",{reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"🤝 Встреча",callback_data:"event_type_Встреча"}],
        [{text:"🎥 Съёмки",callback_data:"event_type_Съёмки"}],
        [{text:"🎙 Подкаст",callback_data:"event_type_Подкаст"}],
        [{text:"✏️ Другое (свой вариант)",callback_data:"event_type_Другое"}],
        [{text:"◀️ Отмена",callback_data:"admin_menu"}]
      ]})});
      return;
    }
    else if(data.indexOf("event_type_")===0){
      if(!isAdmin(cid))return;
      var eType=data.substring(11);
      var eCache=CacheService.getScriptCache();
      eCache.put("event_type_"+cid,eType,3600);
      if(eType==="Другое"){
        eCache.put("event_await_name_"+cid,"1",3600);
        tgSend(cid,"📝 Напиши название мероприятия одним сообщением:");
      } else {
        eCache.put("event_name_"+cid,eType,3600);
        eCache.put("event_await_date_"+cid,"1",3600);
        tgSend(cid,"📅 Введи дату мероприятия в формате <b>ДД.ММ.ГГГГ</b>\n\nНапример: 15.06.2026",{parse_mode:"HTML"});
      }
      return;
    }
    else if(data==="sched_step1"){_sendSchedStep1(String(cb.message.chat.id));return;}
    else if(data.indexOf("sched_res_")===0){
      var sRes=data.substring(10);
      _sendSchedStep2(String(cb.message.chat.id),sRes);return;
    }
    else if(data.indexOf("sched_date_")===0){
      var sParts=data.substring(11).split("_");
      var sDate=sParts[0],sName=sParts.slice(1).join("_");
      _sendSchedStep3(String(cb.message.chat.id),sDate,sName);return;
    }
    else if(data.indexOf("sched_time_")===0){
      var stParts=data.substring(11).split("_");
      var sTime=stParts[0],sDate2=stParts[1],sName2=stParts.slice(2).join("_");
      _saveScheduleFromBot(String(cb.message.chat.id),sName2,sDate2,sTime);return;
    }
    // Приход/расход пошагово
    else if(data==="payment_step1"){_sendPaymentStep1(String(cb.message.chat.id));return;}
    else if(data==="pay_type_income"){_sendPaymentStep2(String(cb.message.chat.id),"income");return;}
    else if(data==="pay_type_expense"){_sendPaymentStep2(String(cb.message.chat.id),"expense");return;}
    else if(data.indexOf("pay_src_")===0){
      var pParts=data.substring(8).split("_");
      var pSrc=pParts.slice(0,-1).join("_"),pType2=pParts[pParts.length-1];
      var pCache=CacheService.getScriptCache();
      pCache.put("pay_pending_"+String(cb.message.chat.id),JSON.stringify({src:pSrc,type:pType2}),300);
      tgSend(String(cb.message.chat.id),
        "Введите сумму (только цифры):\n\nЗатем выберите: Наличные или Банк",
        {reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"💵 Наличные",callback_data:"pay_cash_"+pSrc+"_"+pType2}],
          [{text:"🏦 Банк (−4%)",callback_data:"pay_bank_"+pSrc+"_"+pType2}]
        ]})});
      return;
    }
    else if(data.indexOf("pay_cash_")===0||data.indexOf("pay_bank_")===0){
      var isCash=data.indexOf("pay_cash_")===0;
      var payParts=data.substring(isCash?9:9).split("_");
      // Сумма в кэше
      var payCid=String(cb.message.chat.id);
      var payCache2=CacheService.getScriptCache();
      var payPend=payCache2.get("pay_amount_"+payCid);
      var payAmount=payPend?parseInt(payPend):0;
      var paySrc2=payParts.slice(0,-1).join("_");
      var payType3=payParts[payParts.length-1];
      if(payAmount>0){
        _recordPaymentFromBot(payCid,payAmount,paySrc2,payType3,isCash);
      } else {
        tgSend(payCid,"Сначала введите сумму, затем выберите тип оплаты");
      }
      return;
    }
    else if(data==="admin_schedule"){_sendSchedule10Days(String(cb.message.chat.id));return;}
    else if(data==="admin_add_schedule"){
      tgSend(String(cb.message.chat.id),
        "📅 Чтобы внести расписание:\n\n1. Откройте Google Таблицу\n2. Лист 'Расписание'\n3. Добавьте строку: Резидент, Дата, Время\n\nMeet и уведомления создадутся автоматически!\n\n🔗 Ссылка на таблицу:\nhttps://docs.google.com/spreadsheets/d/"+SS_ID);
      return;
    }
    else if(data==="admin_add_fine"){
      tgSend(String(cb.message.chat.id),
        "⚠️ Выставить штраф:\n\nОтправьте команду в формате:\n/штраф [Имя] [тип] [сумма]\n\nПримеры:\n/штраф Асет опоздание 10000\n/штраф Мирас отчет 10000\n/штраф Дамил слово 100000\n\nТипы: отчет, опоздание, слово, нарушение, пропуск");
      return;
    }
    else if(data==="admin_add_payment"){
      tgSend(String(cb.message.chat.id),
        "💰 Записать приход/расход:\n\nОтправьте в формате:\n/приход [сумма] [источник] [имя]\n/расход [сумма] [категория]\n\nПримеры:\n/приход 150000 трекинг Асет\n/расход 50000 SMM");
      return;
    }
    else if(data==="admin_menu"){_sendAdminMenu(String(cb.message.chat.id));return;}
    else if(data.indexOf("meet_yes_")===0){
      var mp=data.split("_");var mRow=parseInt(mp[2]);var mRes=mp.slice(3).join("_");
      var mCid=String(cb.message.chat.id);
      // Блокируем двойное нажатие
      var mCache=CacheService.getScriptCache();
      var mKey="meet_done_"+mRow;
      // Ставим followup напоминание
      try{var mDateNow=new Date();_scheduleFollowUpReminder(mRes,mDateNow);}catch(sfe){}
      if(mCache.get(mKey)){return;} // молча игнорируем
      mCache.put(mKey,"1",24*3600);
      if(mRes==="ВСЕ РЕЗИДЕНТЫ ОФФЛАЙН"){
        _askAbsentList(mCid,mRow);
      } else {
        _addCheckForResident(mRes);
        tgSend(mCid,"\u2705 Галочка: "+mRes);
      }
      return;
    }
    else if(data.indexOf("meet_no_")===0){tgSend(String(cb.message.chat.id),"Понял, встреча не состоялась.");return;}
    else if(data.indexOf("absent_")===0){
      var ap=data.split("_");var aRow=ap[1];var aRes=ap.slice(2).join("_");
      var aCid=String(cb.message.chat.id);
      // Ставим всем офлайн кроме этого
      var ass2=SpreadsheetApp.openById(SS_ID);
      var wsR2=ass2.getSheetByName("BS - резиденты дебет");
      if(wsR2){
        var lr2=wsR2.getLastRow();var cnt=0;
        for(var rr=3;rr<=lr2;rr++){
          var nm2=wsR2.getRange(rr,2).getValue();if(!nm2)continue;
          if(wsR2.getRange(rr,11).getValue()==="Да")continue;
          if(wsR2.getRange(rr,12).getValue()==="Да")continue;
          var fmt2=String(wsR2.getRange(rr,15)?wsR2.getRange(rr,15).getValue():"Офлайн");
          if(fmt2!=="Офлайн")continue;
          if(nm2===aRes)continue;
          _addCheckForResident(nm2);cnt++;
        }
        tgSend(aCid,"\u2705 Галочки: "+cnt+" резидентам. "+aRes.split(" ")[0]+". пропущен.");
      }
      return;
    }
    else if(data==="absent_all"){
      var allSS=SpreadsheetApp.openById(SS_ID);
      var allWS=allSS.getSheetByName("BS - резиденты дебет");
      if(allWS){var alr=allWS.getLastRow();var ac=0;
        for(var ar=3;ar<=alr;ar++){
          var anm=allWS.getRange(ar,2).getValue();if(!anm)continue;
          if(allWS.getRange(ar,11).getValue()==="Да")continue;
          if(allWS.getRange(ar,12).getValue()==="Да")continue;
          var afmt=String(allWS.getRange(ar,15)?allWS.getRange(ar,15).getValue():"Офлайн");
          if(afmt!=="Офлайн")continue;
          _addCheckForResident(anm);ac++;
        }
        tgSend(String(cb.message.chat.id),"\u2705 Все "+ac+" офлайн-резидентов отмечены!");
      }
      return;
    }
    return;
  }

  if(!u.message)return;
  var msg=u.message,cid=String(msg.chat.id),ct=msg.chat.type;
  var from=msg.from;
  var name=(from.first_name||"")+(from.last_name?" "+from.last_name:"");
  var tid=msg.message_thread_id?String(msg.message_thread_id):"";
  var rawText=(msg.text||"").trim();
  var text=rawText.replace(/@\w+/,"").trim();

  // Служебные команды
  if(text==="/chatid"){tgSend(cid,"🆔 Chat ID: "+cid+(tid?" | Topic ID: "+tid:""));return;}
  if(text==="/topicid"){tgSend(cid,"📌 Topic ID: "+tid+"\nGroup: "+cid);return;}

  // Группа. ОТДЕЛЬНО обрабатываем video_note (кружочки) и кружочки + видео
  if((ct==="group"||ct==="supergroup") && (msg.video_note || msg.video)){
    if(bsServerOwns("report_feedback")) return;   // реакцию ставит сервер
    // Авто-реакция 👍 на любой кружочек в любом топике группы
    try{
      UrlFetchApp.fetch(
        "https://api.telegram.org/bot"+BOT_TOKEN+"/setMessageReaction",
        {
          method:"post",
          contentType:"application/json",
          payload:JSON.stringify({
            chat_id: cid,
            message_id: msg.message_id,
            reaction: [{type:"emoji", emoji:"👍"}]
          }),
          muteHttpExceptions:true
        }
      );
    }catch(reactErr){
      Logger.log("video_note react: "+reactErr);
    }
    return;
  }
  
  // Группа. логируем отчёты ТОЛЬКО от резидентов с текстом от 100 символов
  if((ct==="group"||ct==="supergroup") && bsServerOwns("report_log")) return;   // отчёты записывает сервер
  if(ct==="group"||ct==="supergroup"){
    var tidStr=String(tid||"");
    var okT=!REPORTS_TOPIC_ID||tidStr===String(REPORTS_TOPIC_ID);
    if(!rawText||rawText[0]==="/")return;

    // Длинный текст от резидента в чужом топике. молча терять нельзя:
    // человек считает, что отчёт сдан, а ночью получает штраф
    if(!okT){
      if(rawText.length>=100 && !bsServerOwns("report_feedback")){
        try{
          var _wrongSs=SpreadsheetApp.openById(SS_ID);
          var _wrongWs=_wrongSs.getSheetByName("BS - резиденты дебет");
          if(_wrongWs&&_wrongWs.getLastRow()>2){
            var _wl=_wrongWs.getLastRow();
            var _wd=_wrongWs.getRange(3,2,_wl-2,20).getValues();
            for(var _wi=0;_wi<_wd.length;_wi++){
              if(_normId(_wd[_wi][RI.chat])===_normId(from.id)){
                var _wc=CacheService.getScriptCache();
                if(!_wc.get("wrongtopic_"+from.id)){
                  _wc.put("wrongtopic_"+from.id,"1",6*3600);
                  tgSend(String(from.id),
                    "⚠️ Это похоже на отчёт, но он не в том разделе.\n\n"+
                    "Отчёт засчитывается только в топике «ОТЧЁТЫ».\n"+
                    "Здесь он не сохранён. Отправьте его в нужный топик, иначе ночью будет штраф.");
                }
                break;
              }
            }
          }
        }catch(_we){ Logger.log("wrong topic notice: "+_we); }
      }
      return;
    }

    var ssGr=SpreadsheetApp.openById(SS_ID);

    // 1. Проверяем что отправитель. резидент
    var senderId=String(from.id);
    var isResident=false;
    var residentName="";
    // Если ID не записан, пробуем привязать по имени прямо сейчас
    try{
      var senderFullName=((from.first_name||"")+" "+(from.last_name||"")).trim();
      var linked=_autoLinkChatId(senderId, senderFullName, from.username||"");
      if(linked){ isResident=true; residentName=linked; }
    }catch(alE){ Logger.log("autolink: "+alE); }
    var wsR=ssGr.getSheetByName("BS - резиденты дебет");
    if(wsR&&wsR.getLastRow()>2){
      var lrR=wsR.getLastRow();
      var resData=wsR.getRange(3, 2, lrR-2, 20).getValues();
      for(var ri=0;ri<resData.length;ri++){
        var cellId=_normId(resData[ri][RI.chat]);
        if(cellId===_normId(senderId)){
          isResident=true;
          residentName=resData[ri][RI.name];
          break;
        }
      }
    }
    if(!isResident)return;
    // Слишком короткий текст отчётом не считается, но человек должен узнать
    // об этом сразу, а не ночью из штрафа
    if(rawText.length<100){
      if(bsServerOwns("report_feedback")) return;   // предупреждение шлёт сервер
      try{
        var _sc=CacheService.getScriptCache();
        if(!_sc.get("shortrep_"+senderId)){
          _sc.put("shortrep_"+senderId,"1",4*3600);
          tgSend(senderId,
            "⚠️ Сообщение слишком короткое ("+rawText.length+" симв.), как отчёт оно не засчитано.\n\n"+
            "Нужно минимум 100 символов: что сделал, что не получилось, что завтра.\n"+
            "Шаблон — команда /help. Отправьте полный отчёт, иначе ночью будет штраф.");
        }
      }catch(_se){}
      return;
    }

    // Создаём лог если нет
    var wsLog=ssGr.getSheetByName("Лог отчётов");
    if(!wsLog){
      wsLog=ssGr.insertSheet("Лог отчётов", ssGr.getNumSheets());
      wsLog.hideSheet();
      ["Дата","Username","Имя","Текст","Timestamp","User ID","Thread ID","Поздний"].forEach(function(h,i){
        wsLog.getRange(1,i+1).setValue(h).setFontWeight("bold");
      });
    }

    // Записываем в лог
    // Правило: отчёт засчитывается за ТЕКУЩИЙ день, дедлайн 23:59.
    // После полуночи отчёт идёт за новый день, вчера остаётся незакрытым
    var lock=LockService.getScriptLock();
    var gotLock=false;
    try{
      try{ gotLock=lock.tryLock(15000); }catch(lkE){ gotLock=false; }
      // Пишем даже без блокировки: потерять отчёт хуже, чем рискнуть строкой
      var newRow=wsLog.getLastRow()+1;
      // Время отправки из Telegram: если сообщение задержалось, отчёт всё равно засчитается за свой день
      var nowDate=(msg && msg.date) ? new Date(msg.date*1000) : new Date();
      // Какой час сейчас по Алматы
      // Отчёт всегда относится к дню отправки. Дедлайн 23:59 по Алматы
      var reportDate=new Date(nowDate.getTime());
      var almatyHour=parseInt(Utilities.formatDate(nowDate,"Asia/Almaty","H"));
      var isLate = (almatyHour < 6);  // с 00:00 до 06:00 считается опозданием
      var shortText=rawText.length>200?rawText.substring(0,200)+"...":rawText;
      wsLog.getRange(newRow,1,1,8).setValues([[
        reportDate, from.username||"", residentName, shortText,
        nowDate.getTime(), senderId, tidStr, isLate ? "Да" : ""
      ]]);
      wsLog.getRange(newRow,1).setNumberFormat("DD.MM.YYYY HH:mm");
      if(isLate) wsLog.getRange(newRow,1,1,8).setFontColor("#999999");
      SpreadsheetApp.flush();
    }catch(le){
      Logger.log("ОШИБКА ЗАПИСИ ОТЧЁТА от "+residentName+": "+le);
      try{
        ADMIN_IDS.forEach(function(a){
          bsSysNoteTo(a, "Не записался отчёт от "+residentName+"\nПричина: "+le);
        });
      }catch(ne){}
    }
    finally{ if(gotLock){try{lock.releaseLock();}catch(rle){}} }

    // Отчёт после полуночи: вчерашний день остаётся незакрытым
    if(isLate && !bsServerOwns("report_feedback")){
      try{
        tgSend(senderId, "Отчёт пришёл после полуночи и не засчитан.\n" +
          "За вчера будет штраф. Сегодняшний отчёт нужно отправить до 23:59.");
      }catch(lateE){}
    }

    // Штраф снимаем только если отчёт пришёл вовремя
    if(!isLate){ // снимаем штраф за этот день
    // Страховка на случай, когда отчёт пришёл после ночной проверки
    try{
      var wsFineChk=ssGr.getSheetByName("Штрафы");
      if(wsFineChk && wsFineChk.getLastRow()>=3){
        var repDateStr=Utilities.formatDate(reportDate,"Asia/Almaty","dd.MM.yyyy");
        var lrFC=wsFineChk.getLastRow();
        var fData=wsFineChk.getRange(3,2,lrFC-2,5).getValues();
        for(var fx=fData.length-1;fx>=0;fx--){
          var fNm=String(fData[fx][0]||"").trim();
          var fRs=String(fData[fx][1]||"").trim();
          var fDt=fData[fx][3];
          var fDtStr=String(fDt).trim();
          if(fDt instanceof Date){
            if(fDt.getMinutes()===0 && fDt.getSeconds()===0){
              var _stz="Asia/Almaty"; try{ _stz=Session.getScriptTimeZone()||_stz; }catch(e){}
              fDtStr=Utilities.formatDate(fDt,_stz,"dd.MM.yyyy");
            } else {
              var _b=new Date(fDt.getTime());
              if(parseInt(Utilities.formatDate(fDt,"Asia/Almaty","H"))<14) _b.setDate(_b.getDate()-1);
              fDtStr=Utilities.formatDate(_b,"Asia/Almaty","dd.MM.yyyy");
            }
          }
          if(fNm===residentName && fRs==="Не сдан отчёт" && fDtStr===repDateStr){
            wsFineChk.deleteRow(3+fx);
            try{_renumFines();}catch(e){}
            try{bsSafeRecalcDebet();}catch(e){}
            try{
              tgSend(senderId,"✅ Отчёт за "+repDateStr+" принят.\n\nШтраф 10 000 тг снят автоматически.");
            }catch(e){}
            ADMIN_IDS.forEach(function(aid){
              try{tgSend(aid,"↩️ Штраф снят: "+residentName+" прислал отчёт за "+repDateStr);}catch(e){}
            });
            Logger.log("Штраф снят: "+residentName+" за "+repDateStr);
            break;
          }
        }
      }
    }catch(fineUndoE){Logger.log("fine undo: "+fineUndoE);}
    } // конец снятия штрафа

    // Реакция на отчёт: всегда огонёк. Если её ставит сервер, здесь пропускаем
    if(msg.message_id && !bsServerOwns("report_feedback")){
      try{
        var _rr = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/setMessageReaction",{
          method:"post",contentType:"application/json",
          payload:JSON.stringify({
            chat_id:String(msg.chat.id),
            message_id:msg.message_id,
            reaction:[{type:"emoji",emoji:"\uD83D\uDD25"}]
          }),muteHttpExceptions:true
        });
        var _ro = {};
        try{ _ro = JSON.parse(_rr.getContentText()); }catch(pe){}
        if(!_ro.ok){
          Logger.log("REACTION FAIL: " + String(_ro.description || "").slice(0,150));
          try{
            var _c = CacheService.getScriptCache();
            if(!_c.get("reactWarn")){
              _c.put("reactWarn","1",86400);
              bsSysNote("Бот не смог поставить реакцию на отчёт.\n" +
                "Причина: " + (_ro.description || "неизвестна") +
                "\n\nПроверьте, что бот админ группы");
            }
          }catch(we){}
        }
      }catch(re){ Logger.log("reaction err: "+re); }
    }

    return;
  }

  // Админ присылает PDF документ для загрузки лид-магнита
  if(msg.document){
    Logger.log("DOC received: ct="+ct+", isAdmin="+isAdmin(cid)+", cid="+cid+", filename="+(msg.document.file_name||"?")+", mime="+(msg.document.mime_type||"?"));
  }
  if(ct==="private" && isAdmin(cid) && msg.document){
    var doc=msg.document;
    var fileName=doc.file_name||"";
    var mimeType=doc.mime_type||"";
    var fileId=doc.file_id;
    // Проверим что это PDF (по расширению или mime-типу)
    var isPdf = fileName.toLowerCase().endsWith(".pdf") || mimeType==="application/pdf";
    if(!isPdf){
      tgSend(cid,"⚠️ Это не PDF.\n\nЛид-магниты должны быть в формате PDF.\nПолучен: <code>"+fileName+"</code> ("+mimeType+")",{parse_mode:"HTML"});
      return;
    }
    // Получим режим загрузки из cache (что админ выбрал ранее)
    var uplCache=CacheService.getScriptCache();
    var uploadKey=uplCache.get("upload_lm_"+cid);
    if(!uploadKey){
      // Админ просто прислал документ без выбора слота
      // Покажем выбор
      tgSend(cid,"📄 Документ получен. Выбери для какого чек-листа сохранить file_id:\n\n<code>"+fileName+"</code>",{
        parse_mode:"HTML",
        reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"🔬 Реанимация продаж", callback_data:"upl_sales_"+fileId.substring(0,40)}],
          [{text:"🧬 Анатомия бизнеса", callback_data:"upl_unit_"+fileId.substring(0,40)}],
          [{text:"🩺 Хирургия рутины", callback_data:"upl_delegate_"+fileId.substring(0,40)}],
          [{text:"🤝 Найм команды", callback_data:"upl_hire_"+fileId.substring(0,40)}],
          [{text:"💰 Финансы под контролем", callback_data:"upl_cashflow_"+fileId.substring(0,40)}],
          [{text:"💬 Скрипты для WhatsApp", callback_data:"upl_scripts_"+fileId.substring(0,40)}],
          [{text:"🧬 Команда без выгорания", callback_data:"upl_team_culture_"+fileId.substring(0,40)}],
          [{text:"📊 Метрики собственника", callback_data:"upl_metrics_"+fileId.substring(0,40)}],
          [{text:"🚨 Кризис в бизнесе", callback_data:"upl_crisis_"+fileId.substring(0,40)}],
          [{text:"📊 Маркетинг без бюджета", callback_data:"upl_marketing_"+fileId.substring(0,40)}],
          [{text:"❌ Отмена", callback_data:"upl_cancel"}]
        ]})
      });
      // Сохраним полный file_id в кеш на 10 минут
      uplCache.put("pending_file_"+cid,fileId,600);
      return;
    } else {
      // Админ выбрал слот заранее (/upload_X). сохраняем
      var ok=_saveLeadmagnetFileId(uploadKey,fileId,name);
      uplCache.remove("upload_lm_"+cid);
      var lm=_getLeadmagnetByKey(uploadKey);
      if(ok && lm){
        tgSend(cid,"✅ Чек-лист загружен!\n\n<b>"+lm.title+"</b>\nFile ID сохранён в лист Лид-магниты.\n\nТеперь подписчики получат этот PDF при выборе кнопки.",{parse_mode:"HTML"});
      } else {
        tgSend(cid,"❌ Не удалось сохранить");
      }
      return;
    }
  }

  // Админ команда: показать список чек-листов
  if(text==="/leadmagnets" && isAdmin(cid)){
    var lmWs=_getLeadmagnetsSheet();
    var lrLm=lmWs.getLastRow();
    var listMsg="📚 <b>Лид-магниты</b>\n\n";
    if(lrLm<2){
      listMsg+="Пока ничего нет.";
    } else {
      var lmData=lmWs.getRange(2,1,lrLm-1,5).getValues();
      lmData.forEach(function(r){
        var key=r[0], title=r[1], fileId=r[2], date=r[3], who=r[4];
        var status=fileId?"✅":"⚠️";
        listMsg+=status+" <b>"+title+"</b>\n";
        listMsg+="   ключ: <code>"+key+"</code>\n";
        if(fileId){
          listMsg+="   загрузил: "+who+"\n";
        } else {
          listMsg+="   <i>не загружен</i>\n";
        }
        listMsg+="\n";
      });
      listMsg+="\n💡 Чтобы загрузить. отправь PDF в чат, выбери слот";
    }
    tgSend(cid,listMsg,{parse_mode:"HTML"});
    return;
  }

  // Источник перехода: "/start ig", "/start threads", "/start car_finansy" и любой другой.
  // Ссылка вида t.me/bsurgery_bot?start=ig ставится в шапку профиля и в карусели,
  // человеку достаточно нажать Старт, писать ничего не нужно
  if(ct==="private" && text.indexOf("/start ")===0 && text.indexOf("/start ref_")!==0){
    try{
      var srcParam=text.substring(7).trim().substring(0,40);
      if(srcParam){
        var srcMap={
          "ig":"Instagram профиль",
          "igbio":"Instagram шапка",
          "threads":"Threads",
          "tt":"TikTok",
          "site":"Сайт",
          "speech":"Выступление",
          "qr":"QR офлайн"
        };
        var srcLabel=srcMap[srcParam] || null;
        if(!srcLabel && srcParam.indexOf("car_")===0){
          srcLabel="Карусель: "+srcParam.substring(4).replace(/_/g," ");
        }
        if(!srcLabel && srcParam.indexOf("ad_")===0){
          srcLabel="Реклама: "+srcParam.substring(3).replace(/_/g," ");
        }
        if(!srcLabel) srcLabel="Ссылка: "+srcParam;
        CacheService.getScriptCache().put("ref_source_raw_"+cid, srcLabel, 900);
        Logger.log("Источник перехода "+cid+": "+srcLabel);
      }
    }catch(e){ Logger.log("start source: "+e); }
    // Дальше идём обычным путём: приветствие с кнопками
    text="/start";
  }

  // Реферальный /start: "/start ref_<chatId пригласившего>"
  if(ct==="private" && text.indexOf("/start ref_")===0){
    try{
      var refId=text.substring(11).trim().replace(/[^0-9]/g,"");
      if(refId && refId!==String(cid)){
        // Найдём имя пригласившего резидента
        var refName="";
        try{
          var ssRef=SpreadsheetApp.openById(SS_ID);
          var wsRRef=ssRef.getSheetByName("BS - резиденты дебет");
          if(wsRRef){
            var lrRef=wsRRef.getLastRow();
            for(var rr=3;rr<=lrRef;rr++){
              if(String(wsRRef.getRange(rr,14).getValue()||"").trim()===refId){
                refName=String(wsRRef.getRange(rr,2).getValue()||"").trim();
                break;
              }
            }
          }
        }catch(e){}
        // Сохраняем источник в кеш. запишется в "Подписчики канала" при регистрации
        try{
          CacheService.getScriptCache().put("ref_source_"+cid, refName||("ID "+refId), 21600);
        }catch(e){}
        // Уведомляем админов + пригласившего
        var newUserName=name||"Новый пользователь";
        ADMIN_IDS.forEach(function(aid){
          try{tgSend(aid,"🤝 РЕФЕРАЛ: "+newUserName+" пришёл по приглашению от "+(refName||("ID "+refId))+"\n\nЕсли дойдёт до резидентства. бонус пригласившему 100 000 тг.");}catch(e){}
        });
        if(refName){
          try{tgSend(refId,"🔥 "+newUserName+" перешёл по твоей ссылке и запустил бота!\n\nЕсли он станет резидентом. твой бонус 100 000 тг.");}catch(e){}
        }
      }
    }catch(refE){Logger.log("ref start: "+refE);}
    // Продолжаем как обычный /start
    text="/start";
  }

  // Пользователь поделился номером телефона
  if(msg && msg.contact && msg.contact.phone_number){
    try{
      var contactPhone = String(msg.contact.phone_number);
      var contactName = ((msg.contact.first_name||"") + " " + (msg.contact.last_name||"")).trim() || name;
      _savePhoneToSubscriber(cid, contactPhone, contactName);
      tgSend(cid, "Спасибо! Номер сохранён.\n\nМы свяжемся с вами и разберём результат диагностики.", {
        reply_markup: JSON.stringify({remove_keyboard: true})
      });
      ADMIN_IDS.forEach(function(aid){
        try{
          tgSend(aid, "📱 Новый контакт\n\n" + contactName + "\n" + contactPhone + "\n\nПрошёл диагностику. Позвоните в ближайшее время.");
        }catch(e){}
      });
    }catch(e){ Logger.log("contact: " + e); }
    return;
  }

  // Диагностика версии приложения
  if(text==="/version" && isAdmin(cid)){
    tgSend(cid, checkAppVersionOnServer());
    return;
  }
  // Куда ведут кнопки приложения и что там лежит
  if(text==="/routing" && isAdmin(cid)){
    tgSend(cid, diagnoseAppRouting());
    return;
  }

  // Личный чат
  if(text==="/start"||text==="/menu"){
    if(isAdmin(cid)){
      _sendAdminMenu(cid);
      return;
    }
    // Rate limiting: один /start в 60 секунд на пользователя
    var startCache=CacheService.getScriptCache();
    var startKey="start_"+cid;
    if(startCache.get(startKey)){
      return; // игнорируем повторный /start
    }
    startCache.put(startKey,"1",60);
    
    var firstName=name.split(" ")[0];
    
    // Проверяем кто это: резидент или подписчик канала
    var ssR=SpreadsheetApp.openById(SS_ID);
    var wsR=ssR.getSheetByName("BS - резиденты дебет");
    var isResidentUser=false;
    if(wsR){
      var lrR=wsR.getLastRow();
      for(var rr=3;rr<=lrR;rr++){
        if(String(wsR.getRange(rr, RC.chat).getValue())===cid){
          isResidentUser=true;
          break;
        }
      }
    }
    
    if(isResidentUser){
      // Резидент. нужен акцепт оферты (юридическое соглашение)
      var policy=POLICY_TEXT.replace("{имя}",firstName);
      tgSendAccept(cid,policy);
    } else {
      // Подписчик канала. приветствие БЕЗ акцепта, сразу к чек-листам
      _sendSubscriberWelcome(cid, firstName);
    }
    
    // Уведомляем админов асинхронно
    ADMIN_IDS.forEach(function(aid){
      if(String(aid)!==String(cid)){
        tgSend(aid,"Новый: "+name+(from.username?" @"+from.username:"")+" 🆔"+cid);
      }
    });
    return;
  }
  // Автоматический сбор CustDev: любое содержательное сообщение от резидента в личку боту
  // записывается в "NPS ответы". Условия:
  // - личка (не группа)
  // - не админ
  // - не команда (/start, /help)
  // - длина >= 30 символов
  // - есть имя резидента (значит он в таблице)
  // - не оплата (исключим ключевые слова)
  // - не сообщение про мероприятие/событие
  if(ct==="private" && !isAdmin(cid) && !text.startsWith("/") && text.length>=30 && name){
    try{
      var lowText=text.toLowerCase();
      var isPayment=/оплат|перевел|перевела|kaspi|каспи|деньги отправ|перечислил/.test(lowText);
      var isEvent=/мк|мастеркласс|мастер.класс|завтрак|нетворкинг|событие|меропри/.test(lowText) && text.length<100;
      
      if(!isPayment && !isEvent){
        var nps_ss=SpreadsheetApp.openById(SS_ID);
        var npsLog=nps_ss.getSheetByName("NPS ответы");
        if(!npsLog){
          npsLog=_insertSheetAtEnd(nps_ss, "NPS ответы");
          npsLog.getRange(1,1,1,4).setValues([["Дата","Резидент","Chat ID","Ответ"]]);
          npsLog.getRange(1,1,1,4).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
          npsLog.setFrozenRows(1);
        }
        
        // Защита от дублей: один и тот же текст за 60 секунд не пишем
        var dedupCache=CacheService.getScriptCache();
        var dedupKey="custdev_"+cid+"_"+text.substring(0,40);
        if(!dedupCache.get(dedupKey)){
          dedupCache.put(dedupKey, "1", 60);
          
          npsLog.appendRow([new Date(), name, cid, text]);
          npsLog.getRange(npsLog.getLastRow(),1).setNumberFormat("DD.MM.YYYY HH:mm");
          
          // Уведомление админам только если был NPS_PENDING (значит это ответ на конкретный опрос)
          var npsProps=PropertiesService.getScriptProperties();
          var npsPending=npsProps.getProperty("NPS_PENDING_"+cid);
          if(npsPending){
            var npsMsg="📋 *Ответ на NPS опрос*\n\n👤 От: "+name+"\n\n📝 Ответ:\n"+text;
            ADMIN_IDS.forEach(function(aid){
              try{ tgSend(aid, npsMsg, {parse_mode:"Markdown"}); }catch(e){}
            });
            tgSend(cid, "✅ Спасибо за ответ! Очень ценно для нас.");
            npsProps.deleteProperty("NPS_PENDING_"+cid);
          }
        }
      }
    }catch(npsE){Logger.log("CustDev auto: "+npsE);}
  }
  
  // Проверяем оплату Kaspi. резидент пишет "оплатил" или похожее
  if(ct==="private"&&!isAdmin(cid)&&!text.startsWith("/")){
    var payWords=["оплатил","оплатила","оплатили","оплата","заплатил","перевел","перевела","оплачено"];
    var lowerText=text.toLowerCase();
    var isPayment=payWords.some(function(w){return lowerText.indexOf(w)>=0;});
    if(isPayment&&_handleKaspiPayment(cid,text,from))return;
  }
  // Платформа: кнопка открывает её прямо в Telegram, вход без пароля
  if(ct==="private" && (text==="/platform" || text==="/платформа")){
    var pUrl = bsPlatformUrl();
    var known = isAdmin(cid) || !!bsResidentNameByChat(cid);
    if(!known){ tgSend(cid, "Платформа открыта для резидентов клуба. Если вы резидент, напишите куратору: он проверит ваш Chat ID в таблице."); return; }
    tgSend(cid, "📱 Платформа Business Surgery\n\nВаш разбор, задачи и замеры. Открывается прямо здесь.\n\nС компьютера: " + pUrl, {
      reply_markup: JSON.stringify({inline_keyboard: [[{text: "Открыть платформу", web_app: {url: pUrl}}]]})
    });
    return;
  }
  if(text==="/help"){
    var today=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM");
    var firstName=name.split(" ")[0];
    var helpMsg="📋 Шаблон отчёта Business Surgery:\n\n"+today+"\n"+firstName+
      "\n\nТочка А:\nТочка Б:\n\nЦель: _____ тг\n\nИз старых задач осталось:\n\nЗадачи на 10 дней:\n\nЕжедневные задачи:\n\nОтчёт за день:\n\nПлан на завтра:\n\n❌ Штраф за пропуск: 10 000 тг";
    tgSend(cid,helpMsg);return;
  }
  if(text==="/status"){tgSend(cid,_getStatByChatId(cid));return;}

    // Админ заполняет мероприятие
  if(ct==="private" && isAdmin(cid) && !text.startsWith("/")){
    var ecCache=CacheService.getScriptCache();
    // Имя мероприятия (для "Другое")
    if(ecCache.get("event_await_name_"+cid)){
      ecCache.remove("event_await_name_"+cid);
      ecCache.put("event_name_"+cid,rawText,3600);
      ecCache.put("event_await_date_"+cid,"1",3600);
      tgSend(cid,"📅 Введи дату мероприятия в формате <b>ДД.ММ.ГГГГ</b>",{parse_mode:"HTML"});
      return;
    }
    // Дата
    if(ecCache.get("event_await_date_"+cid)){
      var dParts=rawText.trim().split(".");
      if(dParts.length<2){tgSend(cid,"❌ Неверный формат. Используй ДД.ММ.ГГГГ");return;}
      ecCache.remove("event_await_date_"+cid);
      ecCache.put("event_date_"+cid,rawText.trim(),3600);
      ecCache.put("event_await_time_"+cid,"1",3600);
      tgSend(cid,"⏰ Введи время в формате <b>ЧЧ:ММ</b>\n\nНапример: 18:00",{parse_mode:"HTML"});
      return;
    }
    // Время
    if(ecCache.get("event_await_time_"+cid)){
      var timeStr=rawText.trim();
      if(!/^\d{1,2}:\d{2}$/.test(timeStr)){tgSend(cid,"❌ Неверный формат. Используй ЧЧ:ММ");return;}
      ecCache.remove("event_await_time_"+cid);
      var eName=ecCache.get("event_name_"+cid)||"Мероприятие";
      var eDate=ecCache.get("event_date_"+cid);
      var eType=ecCache.get("event_type_"+cid)||eName;
      ecCache.remove("event_name_"+cid);
      ecCache.remove("event_date_"+cid);
      ecCache.remove("event_type_"+cid);
      // Создаём событие в календаре
      try{
        var dp=eDate.split(".");
        var y=parseInt(dp[2]||new Date().getFullYear());
        var m=parseInt(dp[1]);
        var d=parseInt(dp[0]);
        var tp=timeStr.split(":");
        var hh=parseInt(tp[0]), mm=parseInt(tp[1])||0;
        var startStr=y+"-"+String(m).padStart(2,"0")+"-"+String(d).padStart(2,"0")+"T"+String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
        var startDate=new Date(startStr);
        var endDate=new Date(startDate.getTime()+90*60*1000); // +1.5 часа
        var startISO=Utilities.formatDate(startDate,"Asia/Almaty","yyyy-MM-dd'T'HH:mm:ss");
        var endISO=Utilities.formatDate(endDate,"Asia/Almaty","yyyy-MM-dd'T'HH:mm:ss");
        var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
        if(!calId){
          var cal=CalendarApp.getDefaultCalendar();
          calId=cal.getId();
        }
        var eventResource={
          summary:"🎬 "+eName,
          description:"Тип: "+eType+"\nСоздано через BS бота",
          start:{dateTime:startISO,timeZone:"Asia/Almaty"},
          end:{dateTime:endISO,timeZone:"Asia/Almaty"},
          attendees:[
            {email:"business.hirurgiya@gmail.com",responseStatus:"accepted"},
            {email:"baraka.erniyazov@gmail.com",responseStatus:"accepted"}
          ],
          colorId:"5",
          reminders:{
            useDefault:false,
            overrides:[
              {method:"popup",minutes:60},
              {method:"email",minutes:60}
            ]
          }
        };
        Calendar.Events.insert(eventResource,calId,{sendUpdates:"all"});
        // Уведомляем всех админов
        var notifyMsg="🎬 <b>Мероприятие создано</b>\n\n📌 "+eName+"\n📅 "+eDate+" в "+timeStr+"\n⏱ Длительность: 1.5ч\n🔔 Напоминание за 1 час";
        tgSendAdmins(notifyMsg);
      }catch(eErr){
        tgSend(cid,"❌ Ошибка создания: "+eErr.message);
        Logger.log("event create: "+eErr);
      }
      return;
    }
  }

  // Подписчик ответил на квал-вопрос "цель"
  if(ct==="private"&&!isAdmin(cid)&&!text.startsWith("/")&&text.length>3){
    try{
      var awaitGoal=PropertiesService.getScriptProperties().getProperty("qual_await_goal_"+cid);
      if(awaitGoal){
        _setSubscriberField(cid,"Главная цель",rawText);
        PropertiesService.getScriptProperties().deleteProperty("qual_await_goal_"+cid);
        tgSend(cid,"✅ Спасибо! Учли твою цель.\n\nТеперь у нас есть полная картина. Если хочешь обсудить как BS поможет. записывайся на разбор",{reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"📞 Записаться на разбор", callback_data:"sub_express"}],
          [{text:"📚 Получить чек-листы", callback_data:"sub_leadmagnets"}]
        ]})});
        // Уведомление админам с полной квал-информацией
        try{
          var ssFin=SpreadsheetApp.openById(SS_ID);
          var wsSubFin=ssFin.getSheetByName("Подписчики канала");
          if(wsSubFin){
            var lrSF=wsSubFin.getLastRow();
            for(var sfi=2;sfi<=lrSF;sfi++){
              if(String(wsSubFin.getRange(sfi,2).getValue()).trim()===String(cid)){
                var subRow=wsSubFin.getRange(sfi,1,1,12).getValues()[0];
                var qualMsg="📊 КВАЛ ЗАВЕРШЕН: "+subRow[3]+(subRow[2]?" @"+subRow[2]:"")+"\nНиша: "+subRow[6]+"\nДоход: "+subRow[7]+" млн ₸\nЦель: "+subRow[8];
                tgSendAdmins(qualMsg,"subscribers");
                break;
              }
            }
          }
        }catch(qfE){}
        return;
      }
    }catch(qe){}
  }

  // Команды админа
  if(isAdmin(cid)){
    if(text==="/menu"){_sendAdminMenu(cid);return;}
    if(text==="/check"){_sendSummary();return;}
    // Обработка текста от админа (суммы для штрафа/прихода)
    if(!text.startsWith("/")&&_handleAdminText(cid,text)){return;}
    if(text.toLowerCase().startsWith("/приход ")){_adminAddIncome(cid,rawText);return;}
    if(text.toLowerCase().startsWith("/расход ")){_adminAddExpense(cid,rawText);return;}
    if(text==="/fines"){_sendFines();return;}
    if(text==="/residents"){_sendResidents();return;}
    // Команда /штраф Имя Тип Сумма
    // Пример: /штраф Асет опоздание 10000
    if(text.toLowerCase().startsWith("/штраф ")||text.toLowerCase().startsWith("/shtraf ")){
      _adminAddFine(cid,rawText);return;
    }
    // /диагностика. пройти диагностику бизнеса через бота
    if(text.toLowerCase()==="/диагностика" || text.toLowerCase()==="/diag" || text.toLowerCase()==="/diagnostic"){
      _startBotDiagnostic(cid, name);
      return;
    }
    
    // /резидент. пользователь сам активирует акцепт оферты как резидент
    // (используется когда админ пригласил человека в BS. он жмёт /резидент)
    if(text.toLowerCase()==="/резидент" || text.toLowerCase()==="/resident"){
      var firstName2=name.split(" ")[0];
      var policy2=POLICY_TEXT.replace("{имя}",firstName2);
      tgSendAccept(cid,policy2);
      // Уведомляем админов
      ADMIN_IDS.forEach(function(aid){
        if(String(aid)!==String(cid)){
          tgSend(aid,"📥 Новый возможный резидент активировал акцепт: "+name+(from.username?" @"+from.username:"")+" 🆔"+cid);
        }
      });
      return;
    }
    
    // /post <идея>. создаёт пост через AI и публикует в канал
    if(text.toLowerCase().startsWith("/post ")){
      var idea=rawText.substring(6).trim();
      if(!idea){tgSend(cid,"Использование: /post <идея для поста>");return;}
      tgSend(cid,"⏳ Генерирую пост...");
      var generatedPost=generatePostFromIdea(idea, "Идея от админа");
      // Сохраняем превью и спрашиваем подтверждение
      var cacheP=CacheService.getScriptCache();
      cacheP.put("pending_post_"+cid, generatedPost, 600);
      tgSend(cid,"📝 Превью поста:\n\n"+generatedPost,{reply_markup:JSON.stringify({inline_keyboard:[
        [{text:"✅ Опубликовать в канал", callback_data:"publish_post"}],
        [{text:"🔄 Переписать", callback_data:"regen_post"}],
        [{text:"❌ Отмена", callback_data:"cancel_post"}]
      ]})});
      return;
    }
    
    // /batch <тема>. генерирует 7 единиц контента из одной идеи и кладёт в Контент-план
    if(text.toLowerCase().startsWith("/batch ")){
      var batchIdea=rawText.substring(7).trim();
      if(!batchIdea){tgSend(cid,"Использование: /batch <тема>\nНапример: /batch Почему 90% курсов не помогают предпринимателям");return;}
      tgSend(cid,"⏳ Генерирую 7 единиц контента из одной идеи. Займёт 1-2 минуты...");
      try{
        var batchResult=generateContentBatch(batchIdea);
        if(batchResult.error){
          tgSend(cid,"❌ "+batchResult.error);
          return;
        }
        // Записываем в Контент-план
        var addedCount=addBatchToContentPlan(batchResult.items);
        var summary="✅ <b>Batch создан</b>\n\nИдея: "+batchIdea+"\n\nДобавлено в Контент-план: "+addedCount+" единиц\n\n";
        batchResult.items.forEach(function(item,i){
          summary+=(i+1)+". <b>"+item.type+"</b>. "+item.title+"\n";
        });
        summary+="\n👉 Открой лист 'Контент-план' чтобы отредактировать и подтвердить даты публикации.";
        tgSend(cid,summary,{parse_mode:"HTML"});
      }catch(e){
        tgSend(cid,"❌ Ошибка: "+e.message);
      }
      return;
    }
    
    // /idea. AI предложит 5 идей для постов на основе кейсов резидентов
    if(text.toLowerCase()==="/idea" || text.toLowerCase()==="/идея"){
      tgSend(cid,"⏳ Генерирую идеи. Это занимает 10-30 секунд из-за работы AI. Подожди...");
      try{
        var ideas=generateContentIdeas();
        if(!ideas || ideas.length<10){
          tgSend(cid,"❌ AI вернул пустой ответ. Возможно нет кредитов на Anthropic. Проверь console.anthropic.com");
          return;
        }
        if(ideas.indexOf("❌")===0){
          tgSend(cid,ideas);
          return;
        }
        // Telegram ограничивает длину сообщения 4096. Если больше. режем
        var safeIdeas = ideas.length>3500 ? ideas.substring(0,3500)+"\n\n[обрезано]" : ideas;
        tgSend(cid,"💡 <b>5 идей для постов</b>\n\n"+safeIdeas+"\n\n👉 Выбери идею и отправь:\n<code>/batch текст идеи</code>",{parse_mode:"HTML"});
      }catch(e){
        tgSend(cid,"❌ Ошибка: "+(e.message||e.toString()).substring(0,300));
      }
      return;
    }
    if(text==="/помощь"||text==="/help_admin"){
      tgSend(cid,"📋 Команды администратора:\n\n/штраф [Имя] [Тип] [Сумма]\nПример: /штраф Асет опоздание 10000\n\nТипы: отчет, опоздание, слово, нарушение, пропуск\n\n/check. сводка за день\n/fines. неоплаченные штрафы\n/residents. список резидентов");
      return;
    }
  }
}

function _sendCycleDigest(resName,chatId,tariff){
  if(!GEMINI_API_KEY||GEMINI_API_KEY==="вставьте_gemini_ключ")return;
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsL=ss.getSheetByName("Лог отчётов");
    if(!wsL)return;
    // Собираем отчёты за последние 30 дней
    var now=new Date();
    var cutoff=new Date(now.getTime()-30*24*60*60*1000);
    var ll=wsL.getLastRow();
    var reports=[];
    for(var r=2;r<=ll;r++){
      var ld=wsL.getRange(r,1).getValue();
      if(!ld||!(ld instanceof Date)||ld<cutoff)continue;
      var nm=wsL.getRange(r,3).getValue();
      if(!nm||nm.toLowerCase().indexOf(resName.split(" ")[0].toLowerCase())<0)continue;
      reports.push(wsL.getRange(r,4).getValue());
    }
    if(reports.length===0)return;
    var prompt="Проанализируй отчёты резидента бизнес-клуба '"+resName+"' за последние 30 дней.\n\n"+
      "Отчёты:\n"+reports.slice(-15).join("\n---\n")+"\n\n"+
      "Напиши короткий (5-7 предложений) мотивирующий дайджест на русском:\n"+
      "1. Главные достижения за период\n2. Что улучшилось\n3. На что обратить внимание\n4. Мотивирующий финал\n\n"+
      "Стиль: деловой, тёплый, конкретный. Без воды.";
    var resp=UrlFetchApp.fetch(
      "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key="+GEMINI_API_KEY,
      {method:"post",contentType:"application/json",
       payload:JSON.stringify({contents:[{parts:[{text:prompt}]}]}),
       muteHttpExceptions:true}
    );
    var result=JSON.parse(resp.getContentText());
    if(!result.candidates||!result.candidates[0])return;
    var digest=result.candidates[0].content.parts[0].text;
    var msg="🎯 Дайджест за цикл, "+resName.split(" ")[0]+"!\n\n"+digest+
      "\n\nВы с нами. и это главное! 💪";
    if(chatId)tgSend(chatId,msg);
    ADMIN_IDS.forEach(function(id){
      tgSend(id,"📊 Дайджест "+resName+":\n\n"+digest.substring(0,300)+"...");
    });
  }catch(e){Logger.log("_sendCycleDigest: "+e);}
}

function _handleKaspiPayment(cid,text,from){
  // Резидент написал любое сообщение в личку. проверяем есть ли неоплаченные штрафы
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsF=ss.getSheetByName("Штрафы");
  if(!wsR||!wsF)return false;
  // Ищем резидента по chatId
  var resName="";
  var lr=wsR.getLastRow();
  for(var r=3;r<=lr;r++){
    if(String(wsR.getRange(r, RC.chat).getValue())===String(cid)){
      resName=wsR.getRange(r, RC.name).getValue();break;
    }
  }
  if(!resName)return false;
  // Ищем неоплаченные штрафы
  var lf=wsF.getLastRow();
  var paid=[];
  for(var r=3;r<=lf;r++){
    if(wsF.getRange(r,2).getValue()===resName&&wsF.getRange(r,6).getValue()==="Не оплатил"){
      var amt=Number(wsF.getRange(r,4).getValue())||0;
      wsF.getRange(r,6).setValue("Оплатил");
      wsF.getRange(r,1,1,6).setBackground(GRN_BG);
      paid.push({row:r,name:resName,amount:amt});
      _writeToDDS(resName,amt,"Kaspi оплата");
      break; // закрываем один штраф
    }
  }
  if(paid.length>0){
    _renumFines();recalcResidents();
    tgSend(cid,"✅ Оплата принята! Штраф "+paid[0].amount.toLocaleString()+" тг закрыт.\n\nСпасибо!");
    ADMIN_IDS.forEach(function(id){
      tgSend(id,"💰 "+resName+" сообщил об оплате штрафа "+paid[0].amount.toLocaleString()+" тг\nШтраф закрыт автоматически.");
    });
    return true;
  }
  return false;
}

function _getStatByChatId(cid){
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    var lr=wsR.getLastRow();
    for(var r=3;r<=lr;r++){
      if(String(wsR.getRange(r, RC.chat).getValue())===String(cid)){
        var nm=wsR.getRange(r, RC.name).getValue();
        var debt=Number(wsR.getRange(r, RC.total).getValue())||0;
        var fines=Number(wsR.getRange(r, RC.fine).getValue())||0;
        return "📊 "+nm+"\n💰 Общий долг: "+debt.toLocaleString()+" тг\n⚠️ Штрафы: "+fines.toLocaleString()+" тг\n"+(debt>0?"❗ Есть задолженность":"✅ Всё в порядке!");
      }
    }
    return "Вы не найдены в системе. Обратитесь к куратору.";
  }catch(e){return "Ошибка: "+e.message;}
}

function _getStat(fromName){
  try{
    var ss=SpreadsheetApp.openById(SS_ID),wsR=ss.getSheetByName("BS - резиденты дебет");
    var lr=wsR.getLastRow(),f=fromName.split(" ")[0].toLowerCase(),found=false;
    var debt=0,fines=0,name="";
    for(var r=3;r<=lr;r++){
      var rn=(wsR.getRange(r, RC.name).getValue()||"").toLowerCase();
      if(rn.indexOf(f)>=0){
        name=wsR.getRange(r, RC.name).getValue();
        debt=Number(wsR.getRange(r, RC.total).getValue())||0;
        fines=Number(wsR.getRange(r, RC.fine).getValue())||0;
        found=true;break;
      }
    }
    if(!found)return "Резидент не найден. Обратитесь к куратору.";
    return "📊 "+name+"\n💰 Общий долг: "+debt.toLocaleString()+" тг\n⚠️ Штрафы: "+fines.toLocaleString()+" тг\n"+(debt>0?"❗ Есть задолженность":"✅ Всё в порядке!");
  }catch(e){return "Ошибка: "+e.message;}
}

function _sendSummary(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var wsL=ss.getSheetByName("Лог отчётов");
  var wsF=ss.getSheetByName("Штрафы");
  var ds=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy");
  var lr=wsR.getLastRow(),active=[],totalDebt=0,totalFines=0;
  if(lr>=3){
    var resData=wsR.getRange(3, 2, lr-2, 20).getValues();
    resData.forEach(function(row){
      var nm=row[0];
      if(!nm)return;
      if(row[RI.former]==="Да")return; // K=11 → index 9
      if(row[RI.except]==="Да")return; // L=12 → index 10
      active.push(nm);
      totalDebt+=Number(row[RI.total])||0; // G=7 → index 5
      totalFines+=Number(row[RI.fine])||0; // F=6 → index 4
    });
  }
  var w=[];
  if(wsL){
    var ll=wsL.getLastRow();
    if(ll>=2){
      var logData=wsL.getRange(2,1,ll-1,3).getValues();
      logData.forEach(function(row){
        var ld=row[0];
        if(ld&&Utilities.formatDate(new Date(ld),"Asia/Almaty","dd.MM.yyyy")===ds)
          w.push(row[2]);
      });
    }
  }
  function wrote(n){var f=n.split(" ")[0].toLowerCase();return w.some(function(x){return x.toLowerCase().indexOf(f)>=0;});}
  var nW=active.filter(function(n){return !wrote(n);});
  var dW=active.filter(function(n){return wrote(n);});
  // Неоплаченные штрафы
  var unpaidFines=0,unpaidCount=0;
  if(wsF){
    var lf=wsF.getLastRow();
    if(lf>=3){
      var finesData=wsF.getRange(3,2,lf-2,5).getValues();
      finesData.forEach(function(row){
        if(row[RI.fine]==="Не оплатил"){
          unpaidFines+=Number(row[RI.rest])||0;
          unpaidCount++;
        }
      });
    }
  }
  var pct=active.length>0?Math.round(dW.length/active.length*100):0;
  var msg="📊 СВОДКА за "+ds+"\n\n";
  msg+="✅ Написали ("+dW.length+"/"+active.length+" = "+pct+"%): "+(dW.join(", ")||" ")+"\n";
  msg+="❌ Не написали ("+nW.length+"): "+(nW.join(", ")||" ")+"\n\n";
  msg+="💰 Общий долг резидентов: "+totalDebt.toLocaleString()+" тг\n";
  msg+="⚠️ Неоплаченных штрафов: "+unpaidCount+" шт = "+unpaidFines.toLocaleString()+" тг\n\n";
  // Рекомендации
  if(pct<50)msg+="💡 Рекомендация: менее половины сдали отчёт. напомните резидентам о важности дисциплины\n";
  if(unpaidFines>100000)msg+="💡 Рекомендация: накопилось "+unpaidFines.toLocaleString()+" тг штрафов. пора провести сбор\n";
  if(nW.length>0)msg+="\n📋 Будет начислено штрафов: "+(nW.length*FINE_AMT).toLocaleString()+" тг";
  ADMIN_IDS.forEach(function(id){tgSend(id,msg);});
}

function _sendFines(){
  var ss=SpreadsheetApp.openById(SS_ID),ws=ss.getSheetByName("Штрафы"),lr=ws.getLastRow();
  var txt="💸 НЕОПЛАЧЕННЫЕ ШТРАФЫ:\n\n",total=0,cnt=0;
  for(var r=3;r<=lr;r++){
    var nm=ws.getRange(r,2).getValue(),amt=ws.getRange(r,4).getValue(),st=ws.getRange(r,6).getValue(),tp=ws.getRange(r,3).getValue();
    if(nm&&st==="Не оплатил"){txt+="• "+nm+". "+Number(amt).toLocaleString()+" тг ("+tp+")\n";total+=Number(amt)||0;cnt++;}
  }
  if(!cnt)txt+="Нет неоплаченных 🎉\n";
  txt+="\n💰 Итого: "+total.toLocaleString()+" тг";
  ADMIN_IDS.forEach(function(id){tgSend(id,txt);});
}

function _sendResidents(){
  var ss=SpreadsheetApp.openById(SS_ID),ws=ss.getSheetByName("BS - резиденты дебет"),lr=ws.getLastRow();
  var txt="👥 РЕЗИДЕНТЫ:\n\n",cnt=0;
  for(var r=3;r<=lr;r++){
    var nm=ws.getRange(r,2).getValue(),dt=ws.getRange(r,7).getValue(),ex=ws.getRange(r,12).getValue(),chatId=ws.getRange(r,14).getValue();
    if(!nm)continue;
    txt+=(++cnt)+". "+nm;
    if(Number(dt)>0)txt+=" ⚠️ "+Number(dt).toLocaleString()+" тг";
    if(ex==="Да")txt+=" 🔕";
    if(!chatId)txt+=" ❗нет ChatID";
    txt+="\n";
  }
  txt+="\nВсего: "+cnt;
  ADMIN_IDS.forEach(function(id){tgSend(id,txt);});
}

// ═══════════════════════════════════════════════════════════════════════════
// WEBHOOK
// ═══════════════════════════════════════════════════════════════════════════

function setupWebhookWatchdog(){
  // Удаляем старый watchdog
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="watchdogWebhook")ScriptApp.deleteTrigger(t);
  });
  // Запускаем каждые 5 минут
  ScriptApp.newTrigger("watchdogWebhook").timeBased().everyMinutes(5).create();
  Logger.log("Webhook watchdog установлен");
}


function setupQueueProcessor(){
  // Устанавливает триггер для processUpdateQueue каждую минуту
  // Сначала удаляем старые
  var triggers=ScriptApp.getProjectTriggers();
  triggers.forEach(function(t){
    if(t.getHandlerFunction()==="processUpdateQueue")ScriptApp.deleteTrigger(t);
  });
  // Создаём новый. каждую минуту
  ScriptApp.newTrigger("processUpdateQueue")
    .timeBased()
    .everyMinutes(1)
    .create();
  SpreadsheetApp.getUi().alert("✅ Триггер processUpdateQueue установлен (каждую минуту)");
}





function processSubscriberFollowups(){
  // ЗАЩИТА: не шлём ночью (22:00 - 09:00)
  var nightHr=parseInt(Utilities.formatDate(new Date(),"Asia/Almaty","H"));
  if(nightHr<9 || nightHr>=22) return;

  // Запускается каждый час. Идёт по подписчикам, шлёт касания по дням от подписки.
  // Дни: 1 (24ч) → 3 → 7 → 14 → 30
  // Поле "Примечание" хранит уже отправленные касания: "1|3|7"
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Подписчики канала");
    if(!ws)return;
    var lr=ws.getLastRow();
    if(lr<2)return;
    var data=ws.getRange(2,1,lr-1,12).getValues();
    var now=Date.now();
    var DAY=86400000;

    data.forEach(function(row, idx){
      var subDate=row[0];
      var chatId=String(row[1]||"").trim();
      var name=row[3]||"";
      var status=String(row[9]||"Подписчик").trim();
      var sent=String(row[11]||""); // поле "Примечание"
      if(!chatId||!subDate)return;
      // Если уже резидент. не шлём
      if(status==="Резидент"||status==="Отказ")return;
      if(!(subDate instanceof Date))return;

      var daysSince=Math.floor((now-subDate.getTime())/DAY);
      var sentSet={};
      sent.split("|").forEach(function(d){if(d)sentSet[d]=1;});

      // 24 часа. мягкое касание. Просто спрашиваем, открыл ли. БЕЗ давления на разбор
      if(daysSince>=1 && !sentSet["1"]){
        tgSend(chatId,"👋 Привет!\n\nВчера ты получил чек-лист от Business Surgery. Успел открыть?\n\nЕсли пока нет. сохрани и посмотри на выходных. Если уже посмотрел. напиши что зацепило больше всего, разберу подробнее.",{
          reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"📚 Посмотреть другие чек-листы", callback_data:"sub_leadmagnets"}]
          ]})
        });
        ws.getRange(2+idx,12).setValue(sent+(sent?"|":"")+"1");
        return;
      }
      // 3 дня. кейс
      if(daysSince>=3 && !sentSet["3"]){
        tgSend(chatId,"💼 Кейс резидента BS\n\nДаулет, ниша услуги. Пришёл с выручкой 2.4 млн ₸/мес.\nЗа 90 дней в BS. 7.1 млн ₸/мес.\n\nЧто сделали:\n• Поменяли воронку продаж (+40% конверсии)\n• Подняли средний чек в 1.8 раза через 3 тарифа\n• Внедрили дожимы 7 касаний\n• Уволили слабого менеджера, наняли двух нормальных\n\nЭто не магия. Это система. И эта система внутри BS.",{
          reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"📞 Хочу так же. записаться", callback_data:"sub_express"}],
            [{text:"📅 Узнать тарифы BS", callback_data:"sub_about"}]
          ]})
        });
        ws.getRange(2+idx,12).setValue(sent+(sent?"|":"")+"3");
        return;
      }
      // 7 дней. приглашение на бесплатный мини-эфир / вопросы
      if(daysSince>=7 && !sentSet["7"]){
        tgSend(chatId,"🎙 Эй, "+(name.split(" ")[0])+"\n\nТы уже неделю с нами. Расскажи. что у тебя сейчас главное в бизнесе?\n\nНапиши в одном сообщении. отвечу лично что я об этом думаю. Без воды, по делу.\n\n(Это не автомат. реально читаю)\n  Рустам",{
          reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"💬 Написать Рустаму", url:"https://t.me/qabden"}]
          ]})
        });
        ws.getRange(2+idx,12).setValue(sent+(sent?"|":"")+"7");
        return;
      }
      // 14 дней. спецпредложение на разбор
      if(daysSince>=14 && !sentSet["14"]){
        tgSend(chatId,"⚡ Специальное предложение\n\nДо конца этой недели. экспресс-разбор за 20 000 ₸ вместо 50 000 ₸.\n\nЧто внутри:\n• 60 минут с фаундером BS\n• Разбираем твою воронку и финмодель\n• Дорожная карта следующих 30 дней\n\nПромокод выдаётся только подписчикам канала. Если интересно. пиши.",{
          reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"⚡ Забрать со скидкой", url:"https://t.me/qabden"}],
            [{text:"📞 Обычный разбор", callback_data:"sub_express"}]
          ]})
        });
        ws.getRange(2+idx,12).setValue(sent+(sent?"|":"")+"14");
        return;
      }
      // 30 дней. реактивация
      if(daysSince>=30 && !sentSet["30"]){
        tgSend(chatId,"📍 Месяц с момента подписки\n\nХочется спросить. как сейчас твой бизнес?\n\nЕсли всё растёт. отлично, продолжай и читай канал @bsurgery_kz\n\nЕсли застой или хочется ускорить. поговорим:",{
          reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"📞 Записаться на разбор", callback_data:"sub_express"}],
            [{text:"❌ Не актуально", callback_data:"sub_not_relevant"}]
          ]})
        });
        ws.getRange(2+idx,12).setValue(sent+(sent?"|":"")+"30");
        return;
      }
    });
    SpreadsheetApp.flush();
  }catch(e){Logger.log("processSubscriberFollowups: "+e);}
}

function sendScheduledQualQuestions(){
  // ЗАЩИТА: не шлём ночью (22:00 - 09:00)
  var nightHour=parseInt(Utilities.formatDate(new Date(),"Asia/Almaty","H"));
  if(nightHour<9 || nightHour>=22) return;

  // Запускается каждые 5 минут. Шлёт квал-вопросы тем, кто акцептовал 30+ минут назад.
  try{
    var props=PropertiesService.getScriptProperties();
    var allProps=props.getProperties();
    var now=Date.now();
    Object.keys(allProps).forEach(function(k){
      if(k.indexOf("qual_q_")!==0)return;
      try{
        var info=JSON.parse(allProps[k]);
        if(now<info.scheduledAt)return;
        var cid=k.substring(7);
        // Шлём первый квал-вопрос. ниша
        tgSend(cid,"👋 Привет!\n\nЧтобы понять как точнее тебе помочь. пара вопросов.\n\nВ какой нише твой бизнес?",
          {reply_markup:JSON.stringify({inline_keyboard:[
            [{text:"🛍 Розница / E-commerce", callback_data:"qual_niche_Розница"}],
            [{text:"🏢 B2B услуги", callback_data:"qual_niche_B2B услуги"}],
            [{text:"👥 B2C услуги", callback_data:"qual_niche_B2C услуги"}],
            [{text:"🏭 Производство", callback_data:"qual_niche_Производство"}],
            [{text:"🌐 IT / SaaS", callback_data:"qual_niche_IT"}],
            [{text:"🍽 HoReCa / общепит", callback_data:"qual_niche_HoReCa"}],
            [{text:"💼 Другое", callback_data:"qual_niche_Другое"}]
          ]})}
        );
        // Удаляем флаг
        props.deleteProperty(k);
      }catch(eq){
        // Если ошибка парсинга. удаляем флаг
        try{props.deleteProperty(k);}catch(e){}
      }
    });
  }catch(e){Logger.log("sendScheduledQualQuestions: "+e);}
}


function publishToChannel(){
  // Запускается по расписанию. Берёт следующий пост из листа "Контент-план" и публикует.
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Контент-план");
    if(!ws){
      _createContentPlanSheet();
      ws=ss.getSheetByName("Контент-план");
      if(!ws)return;
    }
    var lr=ws.getLastRow();
    if(lr<2){Logger.log("Контент-план пустой");return;}

    // Структура: A=Дата запланирована, B=Время, C=Тема, D=Текст, E=Опубликовано(Да/Нет), F=Дата публикации, G=Message ID, H=Источник (план/идея)
    var data=ws.getRange(2,1,lr-1,8).getValues();
    var now=new Date();
    var nowTime=now.getTime();

    // Ищем пост который нужно опубликовать СЕЙЧАС (дата сегодня и время прошло)
    for(var i=0;i<data.length;i++){
      var pubFlag=String(data[i][4]||"").trim().toLowerCase();
      if(pubFlag==="да")continue;
      var plannedDate=data[i][0];
      if(!(plannedDate instanceof Date))continue;
      // Проверяем что дата сегодня (или раньше. пропущенные публикуем сразу)
      var planTime=plannedDate.getTime();
      var plannedTimeStr=String(data[i][1]||"").trim();
      // Если в столбце B есть HH:MM. добавляем к дате
      if(plannedTimeStr.indexOf(":")>0){
        var parts=plannedTimeStr.split(":");
        var fullDate=new Date(plannedDate);
        fullDate.setHours(Number(parts[0]),Number(parts[1]),0,0);
        planTime=fullDate.getTime();
      }
      if(planTime>nowTime+60000)continue; // ещё рано (с минутой запаса)
      
      // Защита: не публикуем посты с прошлой датой (если триггер пропустил окно, не догоняем)
      var todayStart=new Date(); todayStart.setHours(0,0,0,0);
      if(planTime<todayStart.getTime()){
        Logger.log("Пост "+(i+2)+" с прошлой датой ("+plannedDate+"), помечаю как просроченный");
        ws.getRange(2+i,5).setValue("Просрочен");
        continue;
      }

      var postText=String(data[i][3]||"").trim();
      if(!postText){Logger.log("Пост "+(i+2)+" без текста");continue;}
      
      // Защита от мусора: длина >= 100 символов и нет признаков карусели/сториз
      if(postText.length<100){Logger.log("Пост "+(i+2)+" слишком короткий, пропускаю");continue;}
      if(postText.indexOf("---")>=0){Logger.log("Пост "+(i+2)+" содержит '---', это карусель, пропускаю");continue;}
      // Признаки сценария Reels. пропускаем
      if(/\bкрюч[ое]к\b|\bкрюк\b|\bсцена\b|\b\d+\s*сек\b/i.test(postText)){
        Logger.log("Пост "+(i+2)+" похож на сценарий Reels, пропускаю");
        continue;
      }

      // Публикуем
      var result=_sendToChannel(postText);
      if(result&&result.ok){
        ws.getRange(2+i,5).setValue("Да");
        ws.getRange(2+i,6).setValue(new Date());
        ws.getRange(2+i,6).setNumberFormat("DD.MM.YYYY HH:mm");
        if(result.result&&result.result.message_id){
          ws.getRange(2+i,7).setValue(result.result.message_id);
        }
        SpreadsheetApp.flush();
        Logger.log("Опубликован пост "+(i+2)+": "+data[i][2]);
        // Уведомляем админов
        ADMIN_IDS.forEach(function(aid){
          bsSysNoteTo(aid,"📢 Опубликован пост в "+CHANNEL_USERNAME+":\n"+data[i][2]);
        });
        return; // публикуем только 1 пост за вызов
      } else {
        Logger.log("Ошибка публикации: "+JSON.stringify(result));
      }
    }
  }catch(e){Logger.log("publishToChannel: "+e);}
}

function _sendToChannel(text){
  // Отправка сообщения в канал
  try{
    var resp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
      method:"post",
      contentType:"application/json",
      payload:JSON.stringify({
        chat_id:CHANNEL_CHAT_ID,
        text:text,
        parse_mode:"HTML",
        disable_web_page_preview:false
      }),
      muteHttpExceptions:true
    });
    return JSON.parse(resp.getContentText());
  }catch(e){
    Logger.log("_sendToChannel: "+e);
    return null;
  }
}

function _createContentPlanSheet(){
  // Создаёт лист "Контент-план" с заготовленными 30 постами
  var ss=SpreadsheetApp.openById(SS_ID);
  var existing=ss.getSheetByName("Контент-план");
  if(existing)return;
  var ws=_insertSheetAtEnd(ss,"Контент-план");
  var headers=["Дата","Время","Тема","Текст поста","Опубликовано","Дата публикации","Message ID","Источник"];
  ws.getRange(1,1,1,8).setValues([headers]);
  ws.getRange(1,1,1,8).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
  ws.setFrozenRows(1);
  ws.setColumnWidth(1,100);
  ws.setColumnWidth(2,70);
  ws.setColumnWidth(3,250);
  ws.setColumnWidth(4,500);
  ws.setColumnWidth(5,100);
  ws.setColumnWidth(6,140);
  ws.setColumnWidth(7,100);
  ws.setColumnWidth(8,90);

  // Заготавливаем 30 постов на 6 недель
  var now=new Date();
  var startDate=new Date(now);
  // Идём со следующего понедельника
  while(startDate.getDay()!==1)startDate.setDate(startDate.getDate()+1);

  var schedule=[
    {dayOffset:0, time:"19:00", theme:"Манифест канала", source:"план"},
    {dayOffset:1, time:"11:00", theme:"Чек-лист: 5 цифр которые ты должен знать каждое утро", source:"план"},
    {dayOffset:3, time:"11:00", theme:"Кейс: Даулет, услуги, 2.4 → 7.1 млн ₸", source:"план"},
    {dayOffset:4, time:"18:00", theme:"Ошибка #1: «лиды дорогие». что на самом деле", source:"план"},
    {dayOffset:6, time:"21:00", theme:"Дайджест недели", source:"план"},
    // Неделя 2
    {dayOffset:7, time:"19:00", theme:"Кейс: стоматолог +180% за 60 дней", source:"план"},
    {dayOffset:8, time:"11:00", theme:"Чек-лист: расчёт LTV за 10 минут", source:"план"},
    {dayOffset:10, time:"11:00", theme:"Разбор: 3 уровня делегирования", source:"план"},
    {dayOffset:11, time:"18:00", theme:"Что я понял за 12 месяцев BS. Рустам", source:"план"},
    {dayOffset:13, time:"21:00", theme:"Дайджест недели", source:"план"},
    // Неделя 3
    {dayOffset:14, time:"19:00", theme:"Кейс: B2B услуги +50% к чеку", source:"план"},
    {dayOffset:15, time:"11:00", theme:"Чек-лист: 7 касаний до клиента", source:"план"},
    {dayOffset:17, time:"11:00", theme:"Ошибка #2: маркетинг не работает", source:"план"},
    {dayOffset:18, time:"18:00", theme:"Инсайт: почему 80% бизнесов умирают на 3-м году", source:"план"},
    {dayOffset:20, time:"21:00", theme:"Дайджест недели", source:"план"},
    // Неделя 4. команда
    {dayOffset:21, time:"19:00", theme:"Кейс: уволил себя с операционки за 60 дней", source:"план"},
    {dayOffset:22, time:"11:00", theme:"Чек-лист: первый ассистент за 10к", source:"план"},
    {dayOffset:24, time:"11:00", theme:"Разбор: 3 признака что нужен COO", source:"план"},
    {dayOffset:25, time:"18:00", theme:"Инсайт: почему ты не делегируешь", source:"план"},
    {dayOffset:27, time:"21:00", theme:"Дайджест недели", source:"план"},
    // Неделя 5. финансы
    {dayOffset:28, time:"19:00", theme:"Кейс: упорядочил финансы. нашёл 40% слива", source:"план"},
    {dayOffset:29, time:"11:00", theme:"Чек-лист: фин.модель на 1 листе", source:"план"},
    {dayOffset:31, time:"11:00", theme:"Ошибка: путает выручку с прибылью", source:"план"},
    {dayOffset:32, time:"18:00", theme:"Инсайт: 3 закона денег предпринимателя", source:"план"},
    {dayOffset:34, time:"21:00", theme:"Дайджест недели", source:"план"},
    // Неделя 6. стратегия
    {dayOffset:35, time:"19:00", theme:"Кейс: выбор ниши изменил всё", source:"план"},
    {dayOffset:36, time:"11:00", theme:"Чек-лист: выбор ниши за 7 шагов", source:"план"},
    {dayOffset:38, time:"11:00", theme:"Разбор: горизонты планирования 1-3-12 мес", source:"план"},
    {dayOffset:39, time:"18:00", theme:"Инсайт: как я думаю про следующие 3 года", source:"план"},
    {dayOffset:41, time:"21:00", theme:"Дайджест недели", source:"план"}
  ];

  var rows=schedule.map(function(item){
    var d=new Date(startDate);
    d.setDate(d.getDate()+item.dayOffset);
    return [d, item.time, item.theme, "", "", "", "", item.source];
  });

  ws.getRange(2,1,rows.length,8).setValues(rows);
  ws.getRange(2,1,rows.length,1).setNumberFormat("DD.MM.YYYY");
  Logger.log("Лист Контент-план создан с "+rows.length+" темами");
}

function setupContentPublishingTrigger(){
  // Триггер каждые 15 минут. публикует посты по расписанию
  var triggers=ScriptApp.getProjectTriggers();
  triggers.forEach(function(t){
    if(t.getHandlerFunction()==="publishToChannel")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("publishToChannel").timeBased().everyMinutes(15).create();
  SpreadsheetApp.getUi().alert("✅ Триггер автопостинга в канал установлен (каждые 15 минут)");
}

function generatePostFromIdea(idea, theme){
  // Использует Anthropic API для генерации поста по идее
  // Если ANTHROPIC_API_KEY не настроен. возвращает шаблон
  var props=PropertiesService.getScriptProperties();
  var apiKey=props.getProperty("ANTHROPIC_API_KEY");
  if(!apiKey){
    // Без API. возвращаем заготовку с идеей
    return "📌 "+theme+"\n\n"+idea+"\n\n  Business Surgery";
  }
  try{
    var prompt="Ты пишешь пост для Telegram-канала Business Surgery. закрытый клуб для предпринимателей с доходом 2-20 млн ₸ в Казахстане.\n\nСтиль постов:\n- Без воды, конкретика, цифры\n- Хук в первой строке (вопрос или провокация)\n- 600-1200 знаков\n- Личный тон, обращение на 'ты'\n- Никаких клише типа 'друзья', 'успехов всем'\n- Bullet points для перечислений (•)\n- В конце CTA если уместен: 'Сохрани', 'Перешли кому пригодится', 'Записывайся в BS: @bsurgery_bot'\n- Используй переносы строк для воздуха\n\nТема поста: "+theme+"\n\nИдея/детали для поста: "+idea+"\n\nНапиши готовый пост. только текст, без объяснений и без оборачивания в кавычки.";

    var resp=UrlFetchApp.fetch("https://api.anthropic.com/v1/messages",{
      method:"post",
      contentType:"application/json",
      headers:{
        "x-api-key":apiKey,
        "anthropic-version":"2023-06-01"
      },
      payload:JSON.stringify({
        model:"claude-sonnet-4-5",
        max_tokens:1500,
        messages:[{role:"user",content:prompt}]
      }),
      muteHttpExceptions:true
    });
    var data=JSON.parse(resp.getContentText());
    if(data.content&&data.content[0]&&data.content[0].text){
      return data.content[0].text;
    }
    Logger.log("Anthropic API: "+JSON.stringify(data));
    return "📌 "+theme+"\n\n"+idea;
  }catch(e){
    Logger.log("generatePostFromIdea: "+e);
    return "📌 "+theme+"\n\n"+idea;
  }
}


function sendWeeklyReport(){
  // Понедельник 9:00. еженедельный отчёт админам
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var now=new Date();
    var weekAgo=new Date(now.getTime()-7*86400000);
    var twoWeeksAgo=new Date(now.getTime()-14*86400000);

    // Подписчики
    var wsSub=ss.getSheetByName("Подписчики канала");
    var subsThisWeek=0, subsPrevWeek=0, registered=0, converted=0, leadmagnetsDl=0;
    if(wsSub&&wsSub.getLastRow()>=2){
      var sData=wsSub.getRange(2,1,wsSub.getLastRow()-1,12).getValues();
      sData.forEach(function(row){
        var d=row[0];
        if(!(d instanceof Date))return;
        if(d>=weekAgo){
          subsThisWeek++;
          if(row[5])leadmagnetsDl++; // лид-магнит
          if(row[9]==="Записался на разбор")registered++;
          if(row[9]==="Резидент")converted++;
        } else if(d>=twoWeeksAgo){
          subsPrevWeek++;
        }
      });
    }

    // Посты
    var wsCP=ss.getSheetByName("Контент-план");
    var postsThisWeek=0;
    if(wsCP&&wsCP.getLastRow()>=2){
      var cpData=wsCP.getRange(2,1,wsCP.getLastRow()-1,8).getValues();
      cpData.forEach(function(row){
        if(String(row[4]||"").toLowerCase()==="да"){
          var pubD=row[5];
          if(pubD instanceof Date && pubD>=weekAgo)postsThisWeek++;
        }
      });
    }

    // Отчёты резидентов
    var wsL=ss.getSheetByName("Лог отчётов");
    var reportsThisWeek=0;
    if(wsL&&wsL.getLastRow()>=2){
      var lData=wsL.getRange(2,1,wsL.getLastRow()-1,3).getValues();
      lData.forEach(function(row){
        var d=row[0];
        if(d instanceof Date && d>=weekAgo)reportsThisWeek++;
      });
    }

    // Финансы за неделю
    var wsDDS=ss.getSheetByName("Учет ДДС");
    var incWeek=0, expWeek=0;
    if(wsDDS&&wsDDS.getLastRow()>=2){
      var dData=wsDDS.getRange(2,1,wsDDS.getLastRow()-1,3).getValues();
      dData.forEach(function(row){
        var d=row[0];
        if(!(d instanceof Date)||d<weekAgo)return;
        incWeek+=Number(row[1])||0;
        expWeek+=Number(row[2])||0;
      });
    }

    // Штрафы за неделю
    var wsF=ss.getSheetByName("Штрафы");
    var finesWeek=0, finesAmt=0;
    if(wsF&&wsF.getLastRow()>=3){
      var fData=wsF.getRange(3, 1, wsF.getLastRow()-2, 21).getValues();
      fData.forEach(function(row){
        var d=row[4];
        if(d instanceof Date && d>=weekAgo){
          finesWeek++;
          finesAmt+=Number(row[3])||0;
        }
      });
    }

    var growth=subsPrevWeek>0?Math.round((subsThisWeek-subsPrevWeek)/subsPrevWeek*100):0;

    var msg="📊 ЕЖЕНЕДЕЛЬНЫЙ ОТЧЁТ BS\n";
    msg+="("+Utilities.formatDate(weekAgo,"Asia/Almaty","dd.MM")+". "+Utilities.formatDate(now,"Asia/Almaty","dd.MM")+")\n\n";

    msg+="📢 КАНАЛ @bsurgery_kz\n";
    msg+="• Постов опубликовано: "+postsThisWeek+"\n";
    msg+="• Новых подписчиков: "+subsThisWeek+(growth!==0?" ("+(growth>0?"+":"")+growth+"% к прошлой неделе)":"")+"\n";
    msg+="• Скачали лид-магниты: "+leadmagnetsDl+"\n";
    msg+="• Записались на разбор: "+registered+"\n";
    msg+="• Стали резидентами: "+converted+"\n\n";

    msg+="🧬 РЕЗИДЕНТЫ\n";
    msg+="• Отчётов сдано: "+reportsThisWeek+"\n";
    msg+="• Штрафов выставлено: "+finesWeek+" (на "+_fmtMoney(finesAmt)+")\n\n";

    msg+="💰 ФИНАНСЫ\n";
    msg+="• Поступления: "+_fmtMoney(incWeek)+"\n";
    msg+="• Расходы: "+_fmtMoney(expWeek)+"\n";
    msg+="• Прибыль: "+_fmtMoney(incWeek-expWeek)+"\n\n";

    msg+="📈 Полная аналитика. лист Аналитика";

    tgSendAdmins(msg,"dashboard");
  }catch(e){Logger.log("sendWeeklyReport: "+e);}
}



function _insertSheetAtEnd(ss, name){
  // Создаёт лист в конце таблицы. не нарушает порядок существующих
  var ws=ss.insertSheet(name);
  try{ss.moveActiveSheet(ss.getNumSheets());}catch(e){}
  return ws;
}


function bsSysNote(text){
  // Системные события (сбои, автопочинка, эхо действий из приложения) не идут в бот.
  // Пишутся в журнал выполнения и в короткий список: меню BS → «Журнал системы»
  try{
    Logger.log("SYS: " + String(text).slice(0, 1000));
    var p = PropertiesService.getScriptProperties(), arr = [];
    try{ arr = JSON.parse(p.getProperty("SYS_NOTES") || "[]"); }catch(e){ arr = []; }
    arr.unshift(Utilities.formatDate(new Date(), "Asia/Almaty", "dd.MM HH:mm") + " " +
      String(text).replace(/<[^>]+>/g, "").slice(0, 200));
    p.setProperty("SYS_NOTES", JSON.stringify(arr.slice(0, 15)));
  }catch(e){}
}
function bsSysNoteTo(aid, text){ bsSysNote(text); }
function showSysNotes(){
  var arr = [];
  try{ arr = JSON.parse(PropertiesService.getScriptProperties().getProperty("SYS_NOTES") || "[]"); }catch(e){}
  return _alert(arr.length ? arr.join("\n\n") : "Журнал пуст");
}

function tgSendAdmins(text, page){
  // Шлёт сообщение всем админам с кнопкой "Открыть в BS"
  // page (необязательно): какую вкладку открыть в Mini App
  var kb = {
    parse_mode: "HTML",
    reply_markup: JSON.stringify({inline_keyboard:[[
      {text:"📱 Открыть в BS",web_app:{url:getWebAppUrl(page)}}
    ]]})
  };
  ADMIN_IDS.forEach(function(aid){
    try{tgSend(aid,text,kb);}catch(e){Logger.log("tgSendAdmins: "+e);}
  });
}


function manualDailyCheck(){
  // Запуск ночной проверки вручную. с полной диагностикой в Telegram
  var startMsg="🧪 Тест ночной проверки запущен в "+Utilities.formatDate(new Date(),"Asia/Almaty","HH:mm:ss");
  try{tgSendAdmins(startMsg);}catch(e){}
  
  try{
    // Сбрасываем кеш чтобы не пропустилось
    var c=CacheService.getScriptCache();
    var yesterdayDate=new Date();
    yesterdayDate.setDate(yesterdayDate.getDate()-1);
    var checkDate=Utilities.formatDate(yesterdayDate,"Asia/Almaty","dd.MM.yyyy");
    c.remove("daily_"+checkDate);
    
    // Запускаем основную функцию
    dailyCheck();
    
    // Если до сюда дошли. всё ок
    return "✅ Ночная проверка запущена. Проверь Telegram.";
  }catch(e){
    var errMsg="❌ Ночная проверка УПАЛА с ошибкой:\n\n"+e.toString()+"\n\nStack:\n"+(e.stack||"нет");
    try{tgSendAdmins(errMsg);}catch(sendE){}
    return "❌ Ошибка: "+e.toString();
  }
}


function fixStuckVisits(){
  // Ручная проверка. для каждого резидента, если все галочки заполнены по тарифу,
  // запускаем _onCheckbox чтобы закрыть цикл
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsV=ss.getSheetByName("BS - посещения");
    if(!wsV) return "Нет листа посещений";
    var lr=wsV.getLastRow();
    var fixed=[];
    for(var r=3;r<=lr;r++){
      var name=wsV.getRange(r,2).getValue();
      if(!name)continue;
      var tariff=parseInt(wsV.getRange(r,3).getValue()||"4");
      if(!tariff||tariff<1)continue;
      // Считаем галочки
      var count=0;
      var lastCheckedCol=-1;
      for(var c=4;c<4+tariff;c++){
        if(wsV.getRange(r,c).getValue()===true){
          count++;
          lastCheckedCol=c;
        }
      }
      // Если все галочки на тарифе. цикл должен быть закрыт
      if(count>=tariff && lastCheckedCol>0){
        try{
          _onCheckbox(wsV,r,lastCheckedCol);
          fixed.push(name);
        }catch(e){Logger.log("fixStuckVisits "+name+": "+e);}
      }
    }
    var msg=fixed.length>0?"✅ Закрыто циклов: "+fixed.length+"\n"+fixed.join(", "):"✅ Зависших циклов нет";
    SpreadsheetApp.getUi().alert(msg);
    return msg;
  }catch(e){return "Ошибка: "+e.toString();}
}


function getWebAppUrl(page){
  // Ссылка на Mini App со свежей меткой: Telegram не отдаст закешированную сборку.
  // Метка меняется раз в 10 минут. этого хватает, чтобы обновления доходили сразу,
  // и при этом Telegram успевает переиспользовать сессию
  // Загрузчик index.html сам качает свежее приложение, метки в ссылке не нужны
  var url = WEBAPP_BASE_URL;
  if(page) url += "?p=" + encodeURIComponent(page);
  return url;
}

// Совместимость со старым кодом. getWebAppUrl() остаётся как функция



function testCriticalFlows(){
  // Глубокая проверка работоспособности. все критичные функции и листы
  var report=[];
  var ss=SpreadsheetApp.openById(SS_ID);
  
  // === 1. Webhook ===
  try{
    var r=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",{muteHttpExceptions:true});
    var w=JSON.parse(r.getContentText());
    if(w.ok && w.result.url){
      report.push("✅ Webhook подключён");
      if(w.result.pending_update_count>0){
        report.push("  ⚠️ В очереди "+w.result.pending_update_count+" необработанных сообщений");
      }
      if(w.result.last_error_message){
        report.push("  ⚠️ Последняя ошибка: "+w.result.last_error_message);
      }
    } else {
      report.push("❌ Webhook не установлен (нажми Подключить Webhook)");
    }
  }catch(e){report.push("❌ Webhook: "+e.message);}
  
  // === 2. Листы ===
  var critical=["BS - резиденты дебет","Расписание","Штрафы","BS - посещения","Учет ДДС","PL","Подписчики канала","Акцепты","Лог отчётов","Лог встреч","Лид-магниты","Профили","Контент-план","Бывшие резиденты","Настройки"];
  var sheets=ss.getSheets().map(function(s){return s.getName();});
  var missing=critical.filter(function(n){return sheets.indexOf(n)<0;});
  if(missing.length===0){
    report.push("✅ Все "+critical.length+" критичных листов есть");
  } else {
    report.push("❌ Не хватает листов: "+missing.join(", "));
  }
  
  // === 3. Триггеры ===
  try{
    var triggers=ScriptApp.getProjectTriggers();
    var fnNames=triggers.map(function(t){return t.getHandlerFunction();});
    var critTrig=["dailyCheck","onTableEdit","checkScheduleReminders","processSubscriberFollowups","publishToChannel","updateAnalytics","sendWeeklyReport","eveningReminder","deferredRecalcPL","refreshBotCache","checkMeetingsCompleted","sendScheduledContent","sendOnboardingMessages","recalcVisitMonths","sendNPS","weeklyBackup","generateNewRecommendations","sendScheduledQualQuestions"];
    var missingT=critTrig.filter(function(n){return fnNames.indexOf(n)<0;});
    if(missingT.length===0){
      report.push("✅ Все 18 триггеров на месте");
    } else {
      report.push("❌ Не хватает триггеров ("+missingT.length+"): "+missingT.slice(0,5).join(", ")+(missingT.length>5?"...":""));
      report.push("  → Нажми ⚙️ Системное → Установить ВСЕ триггеры");
    }
  }catch(e){report.push("❌ Triggers: "+e.message);}
  
  // === 4. Mini App URL и version ===
  try{
    var url=getWebAppUrl();
    if(url && url.indexOf("https://")===0){
      report.push("✅ Mini App URL рабочий");
      // Версия сборки берётся из самого HTML
      var verResp=UrlFetchApp.fetch(WEBAPP_BASE_URL+"app.html",{muteHttpExceptions:true});
      if(verResp.getResponseCode()===200){
        var vm=verResp.getContentText().match(/APP_VERSION\s*=\s*"([^"]+)"/);
        report.push(vm?("  ✅ Версия приложения: "+vm[1]):"  ⚠️ Версия не найдена в сборке");
      } else {
        report.push("  ⚠️ Приложение недоступно (код "+verResp.getResponseCode()+")");
      }
    } else {
      report.push("❌ Mini App URL некорректен");
    }
  }catch(e){report.push("❌ getWebAppUrl: "+e.message);}
  
  // === 5. Critical functions defined ===
  var critFuncs=["_findFreeRow","_addCheckForResident","_onCheckbox","_logMeetingHappened","_getResidentsWithMeetingOn","_saveScheduleFromBot","_miniConfirmMeeting","_adminAddFine","_recordPaymentFromBot","tgSendAdmins","getWebAppUrl","_getLeadmagnetsSheet","_sendLeadmagnetDocument","_renumFines"];
  var missingF=[];
  critFuncs.forEach(function(fn){
    try{
      if(typeof eval(fn)!=="function")missingF.push(fn);
    }catch(e){missingF.push(fn);}
  });
  if(missingF.length===0){
    report.push("✅ Все "+critFuncs.length+" критичных функций определены");
  } else {
    report.push("❌ НЕ ОПРЕДЕЛЕНЫ функции: "+missingF.join(", "));
  }
  
  // === 6. Бот может писать админу ===
  try{
    var testResp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getMe",{muteHttpExceptions:true});
    var me=JSON.parse(testResp.getContentText());
    if(me.ok){
      report.push("✅ Бот @"+me.result.username+" активен");
    } else {
      report.push("❌ Бот не отвечает");
    }
  }catch(e){report.push("❌ Telegram API: "+e.message);}
  
  // === 7. ANTHROPIC_API_KEY (для /post) ===
  try{
    var key=PropertiesService.getScriptProperties().getProperty("ANTHROPIC_API_KEY");
    report.push(key?"✅ ANTHROPIC_API_KEY (для /post)":"ℹ️ ANTHROPIC_API_KEY не установлен (нужен только для /post)");
  }catch(e){}
  
  // === 8. Лид-магниты загружены ===
  try{
    var wsLM=ss.getSheetByName("Лид-магниты");
    if(wsLM && wsLM.getLastRow()>1){
      var lmData=wsLM.getRange(2,1,wsLM.getLastRow()-1,3).getValues();
      var loaded=0,total=lmData.length;
      lmData.forEach(function(r){if(r[2])loaded++;});
      if(loaded===total){
        report.push("✅ Все "+total+" чек-листов загружены");
      } else {
        report.push("⚠️ Чек-листов загружено "+loaded+" из "+total);
      }
    } else {
      report.push("ℹ️ Лист Лид-магниты пуст или не создан");
    }
  }catch(e){}
  
  // === 9. Проверка калькуляции PL ===
  try{
    var wsPL=ss.getSheetByName("PL");
    if(wsPL && wsPL.getRange(2,2).getValue()){
      report.push("✅ PL рассчитан");
    } else {
      report.push("⚠️ PL пустой. нажми 📊 Обновить PL");
    }
  }catch(e){}
  
  // === 10. Проверка количества подписчиков и резидентов ===
  try{
    var wsRes=ss.getSheetByName("BS - резиденты дебет");
    var wsSub=ss.getSheetByName("Подписчики канала");
    var resCount=0, subCount=0;
    if(wsRes && wsRes.getLastRow()>2){
      var rd=wsRes.getRange(3,2,wsRes.getLastRow()-2,20).getValues();
      rd.forEach(function(r){if(r[0] && r[9]!=="Да")resCount++;});
    }
    if(wsSub && wsSub.getLastRow()>1){
      subCount=wsSub.getLastRow()-1;
    }
    report.push("📊 Резидентов: "+resCount+", подписчиков канала: "+subCount);
  }catch(e){}
  
  var msg=report.join("\n");
  SpreadsheetApp.getUi().alert("🩺 Диагностика BS",msg,SpreadsheetApp.getUi().ButtonSet.OK);
  
  // Также шлём результат админу в Telegram
  try{tgSendAdmins("🩺 Результат диагностики:\n\n"+msg);}catch(e){}
  
  return msg;
}



function _getLeadmagnetsSheet(){
  // Возвращает лист "Лид-магниты", создаёт если нет
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Лид-магниты");
  if(!ws){
    ws=ss.insertSheet("Лид-магниты", ss.getNumSheets());
    // Шапка
    ws.getRange(1,1,1,5).setValues([["Ключ","Название","File ID","Дата загрузки","Кто загрузил"]]);
    ws.getRange(1,1,1,5).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold").setHorizontalAlignment("center");
    ws.setFrozenRows(1);
    ws.setColumnWidth(1,180);
    ws.setColumnWidth(2,280);
    ws.setColumnWidth(3,400);
    ws.setColumnWidth(4,140);
    ws.setColumnWidth(5,200);
    // Преднастроенные ключи (можно пополнять)
    var defaults=[
      ["sales","Реанимация продаж","","",""],
      ["unit","Анатомия бизнеса","","",""],
      ["delegate","Хирургия рутины","","",""],
      ["hire","Найм команды","","",""],
      ["marketing","Маркетинг без бюджета","","",""],
      ["cashflow","Финансы под контролем","","",""],
      ["scripts","Скрипты для WhatsApp","","",""],
      ["team_culture","Команда без выгорания","","",""],
      ["metrics","Метрики собственника","","",""],
      ["crisis","Кризис в бизнесе","","",""]
    ];
    ws.getRange(2,1,defaults.length,5).setValues(defaults);
  }
  
  // АВТО-ДОЗАПИСЬ: если лист уже существует, добавляем недостающие ключи новых чек-листов
  try{
    var requiredSlots=[
      ["cashflow","Финансы под контролем"],
      ["scripts","Скрипты для WhatsApp"],
      ["team_culture","Команда без выгорания"],
      ["metrics","Метрики собственника"],
      ["crisis","Кризис в бизнесе"]
    ];
    var lrLm=ws.getLastRow();
    var existingKeys={};
    if(lrLm>=2){
      var keysData=ws.getRange(2,1,lrLm-1,1).getValues();
      keysData.forEach(function(kr){
        var k=String(kr[0]||"").trim();
        if(k)existingKeys[k]=true;
      });
    }
    requiredSlots.forEach(function(slot){
      if(!existingKeys[slot[0]]){
        ws.appendRow([slot[0], slot[1], "", "", ""]);
        Logger.log("Лид-магнит слот добавлен: "+slot[0]);
      }
    });
  }catch(slotE){Logger.log("auto slots: "+slotE);}
  
  return ws;
}

function _getLeadmagnetByKey(key){
  // Возвращает {title, fileId} по ключу или null
  var ws=_getLeadmagnetsSheet();
  var lr=ws.getLastRow();
  if(lr<2)return null;
  var data=ws.getRange(2,1,lr-1,3).getValues();
  for(var i=0;i<data.length;i++){
    if(String(data[i][0]).trim()===key){
      var fileId=String(data[i][2]||"").trim();
      return {title:data[i][1], fileId:fileId, row:i+2};
    }
  }
  return null;
}

function _saveLeadmagnetFileId(key, fileId, adminName){
  // Сохраняет file_id в лист по ключу
  var ws=_getLeadmagnetsSheet();
  var lr=ws.getLastRow();
  for(var r=2;r<=lr;r++){
    if(String(ws.getRange(r,1).getValue()).trim()===key){
      ws.getRange(r,3).setValue(fileId);
      ws.getRange(r,4).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
      ws.getRange(r,5).setValue(adminName||"");
      return true;
    }
  }
  return false;
}

function _sendLeadmagnetDocument(chatId, fileId, caption){
  // Отправляет PDF по file_id
  try{
    var resp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendDocument",{
      method:"post",
      contentType:"application/json",
      payload:JSON.stringify({
        chat_id:chatId,
        document:fileId,
        caption:caption||"",
        parse_mode:"HTML"
      }),
      muteHttpExceptions:true
    });
    var r=JSON.parse(resp.getContentText());
    return r.ok===true;
  }catch(e){
    Logger.log("_sendLeadmagnetDocument: "+e);
    return false;
  }
}


function setGroupTitles(){
  // Ставит подписи всем участникам группы: Хирург админам, Резидент остальным
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    if(!wsR) return _alert("Нет листа резидентов");
    var lr=wsR.getLastRow();
    if(lr<3) return _alert("Список пуст");

    var gid=null;
    try{
      var st=ss.getSheetByName("Настройки");
      if(st){
        var sl=st.getLastRow();
        for(var r=1;r<=sl;r++){
          var k=String(st.getRange(r,1).getValue()||"").toLowerCase();
          if(k.indexOf("group")>=0||k.indexOf("групп")>=0){
            gid=String(st.getRange(r,2).getValue()||"").trim();
            if(gid) break;
          }
        }
      }
    }catch(e){}
    if(!gid && typeof GROUP_ID !== "undefined") gid = GROUP_ID;
    if(!gid) return _alert("Не найден ID группы в настройках");

    var data=wsR.getRange(3, 2, lr-2, 20).getValues();
    var okCount=0, failed=[], skipped=0;

    data.forEach(function(row){
      var name=String(row[RI.name]||"").trim();
      if(!name) return;
      if(String(row[RI.former]).trim()==="Да"){ skipped++; return; }
      var chatId=_normId(row[RI.chat]);
      if(!chatId){ failed.push(name+" (нет Chat ID)"); return; }

      var title = String(row[RI.admin]).trim()==="Да" ? "Хирург" : "Резидент";
      try{
        var resp=UrlFetchApp.fetch(
          "https://api.telegram.org/bot"+BOT_TOKEN+"/setChatAdministratorCustomTitle",{
          method:"post", contentType:"application/json",
          payload:JSON.stringify({chat_id:gid, user_id:Number(chatId), custom_title:title}),
          muteHttpExceptions:true});
        var res=JSON.parse(resp.getContentText());
        if(res.ok){ okCount++; }
        else {
          // Подпись даётся только администраторам. повышаем и пробуем снова
          var pr=UrlFetchApp.fetch(
            "https://api.telegram.org/bot"+BOT_TOKEN+"/promoteChatMember",{
            method:"post", contentType:"application/json",
            payload:JSON.stringify({chat_id:gid, user_id:Number(chatId),
              can_manage_chat:true, can_invite_users:true,
              can_pin_messages:false, can_delete_messages:false,
              can_restrict_members:false, can_promote_members:false,
              can_change_info:false}),
            muteHttpExceptions:true});
          var pres=JSON.parse(pr.getContentText());
          if(pres.ok){
            Utilities.sleep(300);
            var r2=UrlFetchApp.fetch(
              "https://api.telegram.org/bot"+BOT_TOKEN+"/setChatAdministratorCustomTitle",{
              method:"post", contentType:"application/json",
              payload:JSON.stringify({chat_id:gid, user_id:Number(chatId), custom_title:title}),
              muteHttpExceptions:true});
            if(JSON.parse(r2.getContentText()).ok) okCount++;
            else failed.push(name);
          } else failed.push(name+" ("+(pres.description||"").slice(0,40)+")");
        }
        Utilities.sleep(250);
      }catch(e){ failed.push(name+" ("+e+")"); }
    });

    var msg="Подписи проставлены: "+okCount;
    if(skipped) msg+="\nПропущено бывших: "+skipped;
    if(failed.length) msg+="\n\nНе удалось:\n"+failed.join("\n") +
      "\n\nЧастая причина: участник не заходил в группу или бот не админ";
    return _alert(msg);
  }catch(e){ return _alert("Ошибка: "+e); }
}

function _autoLinkChatId(senderId, senderName, senderUsername){
  // Если человек пишет в группу, а его Chat ID не записан. пытаемся сопоставить по имени.
  // Раньше такой отчёт не засчитывался и человек получал штраф
  try{
    if(!senderId || !senderName) return "";
    var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws || ws.getLastRow() < 3) return "";

    var lr = ws.getLastRow();
    var data = ws.getRange(3, 2, lr-2, 20).getValues();
    var sName = _normResName(senderName);
    var sFirst = sName.split(" ")[0];

    for(var i = 0; i < data.length; i++){
      var resName = String(data[i][RI.name]||"").trim();
      if(!resName) continue;
      var existingId = String(data[i][RI.chat]||"").trim().replace(/[^0-9]/g,"");
      if(existingId) continue;  // уже заполнен, не трогаем

      var rName = _normResName(resName);
      var rFirst = rName.split(" ")[0];
      var match = (rName === sName) ||
                  (rFirst.length > 2 && rFirst === sFirst) ||
                  (sName.indexOf(rFirst) === 0) ||
                  (rName.indexOf(sFirst) === 0);
      if(match){
        ws.getRange(3+i, RC.chat).setValue(String(senderId));
        Logger.log("Chat ID привязан: " + resName + " = " + senderId);
        // Уведомление админам убрано по просьбе
        try{ _invalidateBundleCache(); }catch(e){}
        return resName;
      }
    }
    return "";
  }catch(e){ Logger.log("_autoLinkChatId: " + e); return ""; }
}




function reportDayFor(dt){
  // Единое правило: отчёт до 14:00 по Алматы относится к прошедшему дню
  var d = new Date(dt || new Date());
  var h = parseInt(Utilities.formatDate(d, "Asia/Almaty", "H"), 10);
  if(h < 14){
    d.setDate(d.getDate() - 1);
    d.setHours(23, 59, 0, 0);
  }
  return d;
}
function reportDayKey(dt){
  return Utilities.formatDate(reportDayFor(dt), "Asia/Almaty", "dd.MM.yyyy");
}




function getSecret(name){
  // Читает секрет: сначала из свойств, потом из листа
  try{
    var v = PropertiesService.getScriptProperties().getProperty(name);
    if(v) return v;
  }catch(e){}
  try{
    var st = SpreadsheetApp.openById(SS_ID).getSheetByName("Настройки");
    if(!st) return "";
    var lr = st.getLastRow();
    for(var r = 1; r <= lr; r++){
      if(String(st.getRange(r,1).getValue()||"").trim() === name)
        return String(st.getRange(r,2).getValue()||"").trim();
    }
  }catch(e){}
  return "";
}

function checkMissingChatIds(){
  // Показывает, у кого из активных резидентов не заполнен Chat ID.
  // Именно они не могут сдать отчёт и получают штрафы
  try{
    var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("BS - резиденты дебет");
    if(!ws || ws.getLastRow() < 3) return "Нет данных";
    var lr = ws.getLastRow();
    var data = ws.getRange(3, 2, lr-2, 20).getValues();
    var missing = [];
    data.forEach(function(row){
      var name = String(row[0]||"").trim();
      if(!name) return;
      if(String(row[RI.admin]||"").trim() === "Да") return;   // админ
      if(String(row[RI.former]||"").trim() === "Да") return;    // бывший
      var cid = _normId(row[RI.chat]);
      if(!cid) missing.push(name);
    });

    var msg;
    if(!missing.length){
      msg = "✅ Chat ID заполнен у всех активных резидентов";
    } else {
      msg = "⚠️ Нет Chat ID у " + missing.length + " резидентов\n\n" +
        missing.join("\n") +
        "\n\nИх отчёты не засчитываются и им приходят штрафы.\n" +
        "ID заполнится сам, когда человек напишет в группу отчётом.";
    }
    Logger.log(msg);
    try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
    ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, msg); }catch(e){} });
    return msg;
  }catch(e){ return "Ошибка: " + e; }
}

function _logMeetingHappened(residentName, meetingDateStr){
  // Записывает в "Лог встреч" дату ВСТРЕЧИ (не дату нажатия!)
  // БАГ БЫЛ: писали new Date() = момент нажатия. Отметили после полуночи или на другой день.
  // ночная проверка искала вчерашнюю дату, не находила и ШТРАФОВАЛА в день трекинга
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Лог встреч");
    if(!ws){
      ws=ss.insertSheet("Лог встреч", ss.getNumSheets());
      ws.getRange(1,1,1,3).setValues([["Дата","Резидент","Дата+время записи"]]);
      ws.getRange(1,1,1,3).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
      ws.setFrozenRows(1);
      ws.setColumnWidth(1,120);
      ws.setColumnWidth(2,260);
      ws.setColumnWidth(3,180);
    }
    var now=new Date();
    // Дата встречи из параметра ("dd.MM" или "dd.MM.yyyy"), иначе сегодня
    var dateStr;
    if(meetingDateStr){
      var md=String(meetingDateStr).trim();
      if(md.split(".").length===2)md=md+"."+now.getFullYear();
      dateStr=md;
    } else {
      dateStr=Utilities.formatDate(now,"Asia/Almaty","dd.MM.yyyy");
    }
    
    // Идемпотентность: если запись имя+дата уже есть. не дублируем
    var lrChk=ws.getLastRow();
    if(lrChk>=2){
      var chkData=ws.getRange(Math.max(2,lrChk-50),1,Math.min(51,lrChk-1),2).getValues();
      for(var ci=0;ci<chkData.length;ci++){
        var cd=chkData[ci][0];
        var cdStr=cd instanceof Date?Utilities.formatDate(cd,"Asia/Almaty","dd.MM.yyyy"):String(cd);
        if(cdStr===dateStr && String(chkData[ci][1]||"").trim()===String(residentName).trim()){
          return; // уже залогировано
        }
      }
    }
    var lr=ws.getLastRow();
    ws.getRange(lr+1,1,1,3).setValues([[dateStr,residentName,now]]);
    ws.getRange(lr+1,1).setNumberFormat("DD.MM.YYYY");
    ws.getRange(lr+1,3).setNumberFormat("DD.MM.YYYY HH:mm");
  }catch(e){Logger.log("_logMeetingHappened: "+e);}
}

function _normResName(s){
  // Нормализация имени: регистр, пробелы, е/ё.
  // В Расписании имя может быть "Даулет Сайты", а в резидентах просто "Даулет"
  return String(s||"").trim().toLowerCase().replace(/\s+/g," ").replace(/[её]/g,"е");
}

function _hadMeeting(residentName, meetingList){
  // Был ли у резидента трекинг. Сравнение устойчиво к разному написанию имени.
  // Раньше сравнивалось точное совпадение строк, и защита не срабатывала
  var target=_normResName(residentName);
  if(!target) return false;
  for(var i=0;i<meetingList.length;i++){
    var m=_normResName(meetingList[i]);
    if(!m) continue;
    if(m===target) return true;
    if(m.indexOf(target)===0 || target.indexOf(m)===0) return true;
    var mFirst=m.split(" ")[0], tFirst=target.split(" ")[0];
    if(mFirst.length>2 && mFirst===tFirst) return true;
  }
  return false;
}

function _getResidentsWithMeetingOn(dateStr){
  // Возвращает массив имён резидентов у которых была встреча в указанную дату
  // dateStr формат: "dd.MM.yyyy"
  // Защита тройная: 1) Лог встреч, 2) Расписание на эту дату, 3) Календарь Google
  var result=[];
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    
    // 1) Лог встреч (если галочки были проставлены)
    var ws=ss.getSheetByName("Лог встреч");
    if(ws&&ws.getLastRow()>=2){
      var data=ws.getRange(2,1,ws.getLastRow()-1,2).getValues();
      data.forEach(function(row){
        var d=row[0];
        var n=row[1]; // Лог встреч: колонка 2 = имя
        if(!d||!n)return;
        var dStr=(d instanceof Date)?bsDayStr(d):String(d);
        if(dStr===dateStr && result.indexOf(n)<0){
          result.push(String(n).trim());
        }
      });
    }
    
    // 2) Расписание на эту дату (запланированные встречи)
    // Защищает от случая когда трекинг прошёл, но галочки не нажали вручную
    try{
      var wsS=ss.getSheetByName("Расписание");
      if(wsS && wsS.getLastRow()>=3){
        var lr=wsS.getLastRow();
        // Колонки в "Расписание": B=имя резидента, C=дата
        var schedData=wsS.getRange(3,2,lr-2,2).getValues();
        // Конвертация даты к dd.MM.yyyy
        var targetDate=String(dateStr).split(".");
        var targetDDMM=targetDate[0]+"."+targetDate[1]; // dd.MM
        schedData.forEach(function(row){
          var name=row[0];
          var schDate=row[1];
          if(!name||!schDate)return;
          // Дата может быть Date или строкой
          var schDDMM="";
          if(schDate instanceof Date){
            schDDMM=Utilities.formatDate(schDate,"Asia/Almaty","dd.MM");
          } else {
            var s=String(schDate).trim();
            // dd.MM.yyyy → dd.MM
            var parts=s.split(".");
            if(parts.length>=2) schDDMM=parts[0]+"."+parts[1];
          }
          if(schDDMM===targetDDMM && result.indexOf(name)<0){
            result.push(name);
            Logger.log("Защита от штрафа: "+name+" был в Расписании на "+dateStr);
          }
        });
      }
    }catch(schE){Logger.log("_getResidentsWithMeetingOn schedule: "+schE);}
    
    // 3) Google Calendar события на эту дату (если есть Calendar API)
    try{
      var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
      if(!calId)calId=CalendarApp.getDefaultCalendar().getId();
      
      var dParts=dateStr.split(".");
      var day=parseInt(dParts[0]);
      var mon=parseInt(dParts[1])-1;
      var yr=parseInt(dParts[2])||new Date().getFullYear();
      var dayStart=new Date(yr,mon,day,0,0,0);
      var dayEnd=new Date(yr,mon,day,23,59,59);
      
      var events=Calendar.Events.list(calId,{
        timeMin:dayStart.toISOString(),
        timeMax:dayEnd.toISOString(),
        singleEvents:true,
        maxResults:50
      });
      
      if(events && events.items){
        // Загрузим всех активных резидентов чтобы матчить по имени
        var wsRes=ss.getSheetByName("BS - резиденты дебет");
        var residentNames=[];
        if(wsRes && wsRes.getLastRow()>=3){
          var rd=wsRes.getRange(3,2,wsRes.getLastRow()-2,1).getValues();
          rd.forEach(function(r){ if(r[0]) residentNames.push(String(r[0])); });
        }
        events.items.forEach(function(ev){
          var summary=String(ev.summary||"");
          residentNames.forEach(function(name){
            if(summary.indexOf(name)>=0 && result.indexOf(name)<0){
              result.push(name);
              Logger.log("Защита от штрафа: "+name+" был в Calendar на "+dateStr);
            }
          });
        });
      }
    }catch(calE){Logger.log("_getResidentsWithMeetingOn calendar: "+calE);}
    
  }catch(e){Logger.log("_getResidentsWithMeetingOn: "+e);}
  return result;
}


function _findFreeRow(ws, startRow, checkCol){
  // Возвращает номер первой свободной строки начиная с startRow
  // checkCol. какую колонку проверять на пустоту (например 2 = имя резидента)
  if(!ws)return startRow;
  startRow = startRow || 2;
  checkCol = checkCol || 1;
  var lr = ws.getLastRow();
  // Если лист пустой
  if(lr < startRow)return startRow;
  // Ищем первую пустую строку
  for(var r = startRow; r <= lr; r++){
    var val = ws.getRange(r, checkCol).getValue();
    if(val === "" || val === null || val === undefined)return r;
  }
  // Все заполнены. следующая после последней
  return lr + 1;
}


function _miniCreateEvent(p){
  // Создаёт мероприятие в Google Calendar + добавляет в лист Расписание (как обычная встреча)
  try{
    var name=String(p.name||"Мероприятие");
    var type=String(p.type||"Другое");
    var dateStr=String(p.date||"");
    var timeStr=String(p.time||"");
    
    if(!dateStr||!timeStr)return{error:"Нет даты или времени"};
    
    var dp=dateStr.split(".");
    if(dp.length<3)return{error:"Неверный формат даты, нужен ДД.ММ.ГГГГ"};
    var y=parseInt(dp[2]), m=parseInt(dp[1]), d=parseInt(dp[0]);
    
    var tp=timeStr.split(":");
    if(tp.length<2)return{error:"Неверный формат времени, нужен ЧЧ:ММ"};
    var hh=parseInt(tp[0]), mm=parseInt(tp[1])||0;
    
    var startStr=y+"-"+String(m).padStart(2,"0")+"-"+String(d).padStart(2,"0")+"T"+String(hh).padStart(2,"0")+":"+String(mm).padStart(2,"0")+":00";
    var startDate=new Date(startStr);
    var endDate=new Date(startDate.getTime()+90*60*1000); // 1.5 часа
    var startISO=Utilities.formatDate(startDate,"Asia/Almaty","yyyy-MM-dd'T'HH:mm:ss");
    var endISO=Utilities.formatDate(endDate,"Asia/Almaty","yyyy-MM-dd'T'HH:mm:ss");
    
    var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
    if(!calId){
      var cal=CalendarApp.getDefaultCalendar();
      calId=cal.getId();
    }
    
    var eventResource={
      summary:"🎬 "+name,
      description:"Тип: "+type+"\nСоздано через BS Mini App",
      start:{dateTime:startISO,timeZone:"Asia/Almaty"},
      end:{dateTime:endISO,timeZone:"Asia/Almaty"},
      attendees:[
        {email:"business.hirurgiya@gmail.com",responseStatus:"accepted"},
        {email:"baraka.erniyazov@gmail.com",responseStatus:"accepted"}
      ],
      colorId:"5",
      reminders:{
        useDefault:false,
        overrides:[
          {method:"popup",minutes:60},
          {method:"email",minutes:60}
        ]
      }
    };
    var createdEvent=Calendar.Events.insert(eventResource,calId,{sendUpdates:"all"});
    var eventId=createdEvent.id||"";
    
    // Добавляем запись в лист Расписание чтобы видно было в Mini App
    try{
      var ss=SpreadsheetApp.openById(SS_ID);
      var wsSch=ss.getSheetByName("Расписание");
      if(wsSch){
        // Структура листа: A=№, B=имя/название, C=дата, D=время, E=пусто, F=ссылка/тип, ...
        var lr=wsSch.getLastRow();
        var newRow=lr+1;
        // Имя = название мероприятия с префиксом типа
        var displayName="🎬 "+name;
        wsSch.getRange(newRow,1).setValue(newRow-2); // №
        wsSch.getRange(newRow,2).setValue(displayName);
        wsSch.getRange(newRow,3).setValue(startDate).setNumberFormat("DD.MM.YYYY");
        wsSch.getRange(newRow,4).setValue(timeStr);
        wsSch.getRange(newRow,6).setValue("Мероприятие: "+type);
        wsSch.getRange(newRow,10).setValue(eventId); // event_id для удаления
      }
    }catch(schE){Logger.log("Sched event row: "+schE);}
    
    // Уведомляем админов
    var notifyMsg="🎬 <b>Мероприятие создано</b>\n\n📌 "+name+"\n📅 "+dateStr+" в "+timeStr+"\n⏱ Длительность: 1.5ч\n🔔 Напоминание за 1 час\n\nЕсть в Расписании в Mini App";
    try{bsSysNote(notifyMsg);}catch(e){}
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniCreateEvent: "+e);
    return{error:e.toString()};
  }
}


// ─── ToDo / Задачи BS ────────────────────────────────────────────────────
// Источник истины: Google Doc + лист "Задачи BS" (синхронизованы)

var TODO_DOC_ID = "1mIQJCfcNLeJmi08ZXfBhn1H8bis9sGBY1GInEwKCzzw";
var TODO_SECTION_HEADER = "BS Задачи";


function authDocumentApp(){
  // ВЫПОЛНИ ЭТУ ФУНКЦИЮ ВРУЧНУЮ через меню Run, чтобы получить разрешение на Google Docs
  // После авторизации функция importTodosFromDoc и автосинхронизация задач заработают
  try{
    var doc=DocumentApp.openById(TODO_DOC_ID);
    var name=doc.getName();
    SpreadsheetApp.getUi().alert(
      "✅ Авторизация прошла",
      "Доступ к Google Doc получен.\n\nИмя документа: "+name+"\n\nТеперь можно использовать импорт задач и синхронизацию.",
      SpreadsheetApp.getUi().ButtonSet.OK
    );
    return "OK: "+name;
  }catch(e){
    SpreadsheetApp.getUi().alert(
      "❌ Не получилось",
      "Ошибка: "+e.message+"\n\nПроверь что:\n1) TODO_DOC_ID правильный\n2) Документ доступен этому аккаунту\n3) При запросе разрешений нажал Разрешить",
      SpreadsheetApp.getUi().ButtonSet.OK
    );
    return "ERR: "+e.toString();
  }
}

function _getTodoSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Задачи BS");
  if(!ws){
    ws=ss.insertSheet("Задачи BS", ss.getNumSheets());
    var headers=["ID","Текст","Статус","Дата создания","Дата выполнения","Дата дедлайна","Событие в календаре"];
    ws.getRange(1,1,1,headers.length).setValues([headers]);
    ws.getRange(1,1,1,headers.length).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
    ws.setFrozenRows(1);
    ws.setColumnWidth(1,80);
    ws.setColumnWidth(2,420);
    ws.setColumnWidth(3,100);
    ws.setColumnWidth(4,140);
    ws.setColumnWidth(5,140);
    ws.setColumnWidth(6,140);
    ws.setColumnWidth(7,260);
  }
  return ws;
}

function _miniGetTodos(p){
  // Возвращает все задачи для админа
  try{
    var ws=_getTodoSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{ok:true, tasks:[]};
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var tasks=[];
    data.forEach(function(row,i){
      if(!row[1])return;
      tasks.push({
        id: String(row[0]||(i+2)),
        text: String(row[1]),
        status: String(row[2]||"open"),
        createdAt: row[3] instanceof Date ? row[3].toISOString() : "",
        doneAt: row[4] instanceof Date ? row[4].toISOString() : "",
        deadline: row[5] instanceof Date ? row[5].toISOString() : "",
        eventId: String(row[6]||""),
        row: i+2
      });
    });
    return{ok:true, tasks:tasks};
  }catch(e){
    Logger.log("_miniGetTodos: "+e);
    return{error:e.toString()};
  }
}

function _miniAddTodo(p){
  // Добавляет задачу в лист + Google Doc
  try{
    var text=String(p.text||"").trim();
    if(!text)return{error:"Пустой текст"};
    var deadline=p.deadline||""; // YYYY-MM-DD от Mini App
    
    var ws=_getTodoSheet();
    var newRow=ws.getLastRow()+1;
    var nowDate=new Date();
    var id="t"+nowDate.getTime();
    
    var deadlineDate=null;
    if(deadline){
      try{ deadlineDate=new Date(deadline+"T09:00:00"); }catch(de){}
    }
    
    ws.getRange(newRow,1).setValue(id);
    ws.getRange(newRow,2).setValue(text);
    ws.getRange(newRow,3).setValue("open");
    ws.getRange(newRow,4).setValue(nowDate).setNumberFormat("DD.MM.YYYY HH:mm");
    if(deadlineDate){
      ws.getRange(newRow,6).setValue(deadlineDate).setNumberFormat("DD.MM.YYYY");
    }
    
    // Создаём событие в Google Calendar как "BS - <задача>"
    var eventId="";
    if(deadlineDate){
      try{
        var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
        if(!calId)calId=CalendarApp.getDefaultCalendar().getId();
        var startDate=new Date(deadlineDate.getTime());
        startDate.setHours(9,0,0,0);
        var endDate=new Date(startDate.getTime()+30*60*1000);
        var startISO=Utilities.formatDate(startDate,"Asia/Almaty","yyyy-MM-dd'T'HH:mm:ss");
        var endISO=Utilities.formatDate(endDate,"Asia/Almaty","yyyy-MM-dd'T'HH:mm:ss");
        var ev=Calendar.Events.insert({
          summary:"BS - "+text,
          description:"Задача из BS",
          start:{dateTime:startISO,timeZone:"Asia/Almaty"},
          end:{dateTime:endISO,timeZone:"Asia/Almaty"},
          colorId:"9", // синий
          reminders:{useDefault:false, overrides:[{method:"popup",minutes:60}]}
        }, calId);
        eventId=ev.id||"";
        ws.getRange(newRow,7).setValue(eventId);
      }catch(calE){Logger.log("Cal task: "+calE);}
    }
    
    // Добавляем в Google Doc
    try{_syncTodoToDoc(text,"add");}catch(docE){Logger.log("Doc add: "+docE);}
    
    return{ok:true, id:id, eventId:eventId};
  }catch(e){
    Logger.log("_miniAddTodo: "+e);
    return{error:e.toString()};
  }
}

function _miniToggleTodo(p){
  // Меняет статус задачи (open/done)
  try{
    var id=String(p.id||"");
    var newStatus=String(p.status||"done"); // done или open
    if(!id)return{error:"Нет id"};
    
    var ws=_getTodoSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{error:"Нет задач"};
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var foundRow=-1, taskText="", eventId="";
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===id){
        foundRow=i+2;
        taskText=String(data[i][1]||"");
        eventId=String(data[i][6]||"");
        break;
      }
    }
    if(foundRow<0)return{error:"Задача не найдена"};
    
    ws.getRange(foundRow,3).setValue(newStatus);
    if(newStatus==="done"){
      ws.getRange(foundRow,5).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
      // Удаляем событие из календаря
      if(eventId){
        try{
          var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
          if(!calId)calId=CalendarApp.getDefaultCalendar().getId();
          Calendar.Events.remove(calId,eventId);
        }catch(e){}
      }
      // Зачёркиваем в Google Doc
      try{_syncTodoToDoc(taskText,"done");}catch(e){}
    } else {
      ws.getRange(foundRow,5).clearContent();
      // Раз-зачёркиваем в Doc
      try{_syncTodoToDoc(taskText,"open");}catch(e){}
    }
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniToggleTodo: "+e);
    return{error:e.toString()};
  }
}

function _miniUpdateTodo(p){
  // Обновляет текст задачи и синхронизирует с Google Doc
  try{
    var id=String(p.id||"");
    var newText=String(p.text||"").trim();
    if(!id)return{error:"Нет id"};
    if(!newText)return{error:"Пустой текст"};
    
    var ws=_getTodoSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{error:"Нет задач"};
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var foundRow=-1, oldText="";
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===id){
        foundRow=i+2;
        oldText=String(data[i][1]||"");
        break;
      }
    }
    if(foundRow<0)return{error:"Задача не найдена"};
    
    // Обновим текст в таблице (колонка B)
    ws.getRange(foundRow,2).setValue(newText);
    
    // Обновим в Google Doc: найдём старый текст и заменим
    try{
      var doc=DocumentApp.openById(TODO_DOC_ID);
      var body=doc.getBody();
      var elements=body.getNumChildren();
      for(var k=0;k<elements;k++){
        var ch=body.getChild(k);
        if(ch.getType()===DocumentApp.ElementType.PARAGRAPH){
          var txt=ch.asParagraph().getText();
          if(txt.indexOf(oldText)>=0){
            // Заменяем сохранив форматирование чекбокса
            var newTxt=txt.replace(oldText,newText);
            ch.asParagraph().setText(newTxt);
            break;
          }
        }
      }
      doc.saveAndClose();
    }catch(docErr){
      Logger.log("Sync update todo to doc: "+docErr);
    }
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniUpdateTodo: "+e);
    return{error:e.toString()};
  }
}


function _miniDeleteTodo(p){
  try{
    var id=String(p.id||"");
    if(!id)return{error:"Нет id"};
    var ws=_getTodoSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{error:"Нет задач"};
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var foundRow=-1, taskText="", eventId="";
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===id){
        foundRow=i+2;
        taskText=String(data[i][1]||"");
        eventId=String(data[i][6]||"");
        break;
      }
    }
    if(foundRow<0)return{error:"Задача не найдена"};
    
    // Удаляем событие из календаря
    if(eventId){
      try{
        var calId=PropertiesService.getScriptProperties().getProperty("BS_CALENDAR_ID");
        if(!calId)calId=CalendarApp.getDefaultCalendar().getId();
        Calendar.Events.remove(calId,eventId);
      }catch(e){}
    }
    
    // Удаляем из Doc
    try{_syncTodoToDoc(taskText,"delete");}catch(e){}
    
    ws.deleteRow(foundRow);
    return{ok:true};
  }catch(e){
    Logger.log("_miniDeleteTodo: "+e);
    return{error:e.toString()};
  }
}

function _syncTodoToDoc(taskText, action){
  // Синхронизирует задачу в Google Doc
  // action: add | done | open | delete
  // Если нет разрешения. тихо пропускаем, задача остаётся только в Sheets
  try{
    if(typeof DocumentApp === "undefined"){
      Logger.log("_syncTodoToDoc: DocumentApp недоступен");
      return;
    }
    var doc=DocumentApp.openById(TODO_DOC_ID);
    var body=doc.getBody();
    
    // Ищем секцию "BS Задачи"
    var sectionFound=false;
    var sectionParaIdx=-1;
    var numChildren=body.getNumChildren();
    for(var i=0;i<numChildren;i++){
      var ch=body.getChild(i);
      if(ch.getType()===DocumentApp.ElementType.PARAGRAPH){
        var txt=ch.asParagraph().getText();
        // Ищем заголовок секции "BS Задачи" в нескольких возможных вариантах
        var trimmedTxt = txt.trim();
        if(trimmedTxt === TODO_SECTION_HEADER ||
           trimmedTxt === "BS Задачи" ||
           trimmedTxt === "BS задачи" ||
           trimmedTxt === "Задачи BS" ||
           trimmedTxt.toLowerCase() === "bs задачи"){
          // Дополнительная проверка: это должен быть заголовок (Heading), не обычный параграф
          var headingType = ch.asParagraph().getHeading();
          if(headingType === DocumentApp.ParagraphHeading.HEADING1 ||
             headingType === DocumentApp.ParagraphHeading.HEADING2 ||
             headingType === DocumentApp.ParagraphHeading.HEADING3){
            sectionFound=true;
            sectionParaIdx=i;
            break;
          } else if(!sectionFound){
            // Если не нашли как Heading, запомним как fallback
            sectionFound=true;
            sectionParaIdx=i;
          }
        }
      }
    }
    
    // Если секции нет. создаём в конце
    if(!sectionFound){
      var titlePara=body.appendParagraph(TODO_SECTION_HEADER);
      titlePara.setHeading(DocumentApp.ParagraphHeading.HEADING2);
      sectionParaIdx=body.getNumChildren()-1;
    }
    
    // Действие
    if(action==="add"){
      // Добавляем строку под секцией
      var newPara=body.insertParagraph(sectionParaIdx+1, "☐ "+taskText);
      newPara.setHeading(DocumentApp.ParagraphHeading.NORMAL);
    } else if(action==="done" || action==="open"){
      // Меняем статус существующей задачи
      var endIdx=body.getNumChildren();
      for(var j=sectionParaIdx+1;j<endIdx;j++){
        var p=body.getChild(j);
        if(p.getType()!==DocumentApp.ElementType.PARAGRAPH)continue;
        var t=p.asParagraph().getText();
        // Очистка от маркеров
        var cleaned=t.replace(/^[☐☑✓\-\s\u00a0]+/g,"").trim();
        if(cleaned===taskText.trim()){
          var prefix = action==="done" ? "☑ " : "☐ ";
          p.asParagraph().setText(prefix+taskText);
          // Зачёркивание для done
          var para=p.asParagraph();
          if(para.getNumChildren()>0){
            var firstChild=para.getChild(0);
            if(firstChild.getType()===DocumentApp.ElementType.TEXT){
              firstChild.asText().setStrikethrough(0, prefix.length+taskText.length-1, action==="done");
            }
          }
          break;
        }
      }
    } else if(action==="delete"){
      var endIdx2=body.getNumChildren();
      for(var k=sectionParaIdx+1;k<endIdx2;k++){
        var p2=body.getChild(k);
        if(p2.getType()!==DocumentApp.ElementType.PARAGRAPH)continue;
        var t2=p2.asParagraph().getText();
        var cleaned2=t2.replace(/^[☐☑✓\-\s\u00a0]+/g,"").trim();
        if(cleaned2===taskText.trim()){
          body.removeChild(p2);
          break;
        }
      }
    }
    
    doc.saveAndClose();
  }catch(e){Logger.log("_syncTodoToDoc: "+e);}
}

function importTodosFromDoc(){
  // Ручной импорт задач из Google Doc в лист (читает секцию BS Задачи)
  // ВАЖНО: при первом запуске Apps Script запросит разрешение на DocumentApp
  // Нажми "Просмотреть разрешения" → выбери аккаунт → "Дополнительно" → "Перейти на Код (небезопасно)" → "Разрешить"
  try{
    var doc=DocumentApp.openById(TODO_DOC_ID);
    var body=doc.getBody();
    var inSection=false;
    var tasks=[];
    var numChildren=body.getNumChildren();
    for(var i=0;i<numChildren;i++){
      var ch=body.getChild(i);
      if(ch.getType()!==DocumentApp.ElementType.PARAGRAPH)continue;
      var txt=ch.asParagraph().getText().trim();
      if(!txt)continue;
      if(txt.indexOf(TODO_SECTION_HEADER)>=0){
        inSection=true;
        continue;
      }
      // Если попали в другой заголовок. выходим из секции
      var heading=ch.asParagraph().getHeading();
      if(inSection && heading && heading !== DocumentApp.ParagraphHeading.NORMAL){
        inSection=false;
      }
      if(inSection){
        var isDone = txt.indexOf("☑")===0 || txt.indexOf("✓")===0;
        var cleaned=txt.replace(/^[☐☑✓\-\s\u00a0]+/g,"").trim();
        if(cleaned){
          tasks.push({text:cleaned, done:isDone});
        }
      }
    }
    
    // Заливаем в лист. только новые
    var ws=_getTodoSheet();
    var lr=ws.getLastRow();
    var existing={};
    if(lr>=2){
      var existingData=ws.getRange(2,1,lr-1,2).getValues();
      existingData.forEach(function(r){
        if(r[1]) existing[r[1].toString().trim()]=true;
      });
    }
    var addedCount=0;
    tasks.forEach(function(t){
      if(existing[t.text])return;
      var newRow=ws.getLastRow()+1;
      var id="t"+Date.now()+"_"+addedCount;
      ws.getRange(newRow,1).setValue(id);
      ws.getRange(newRow,2).setValue(t.text);
      ws.getRange(newRow,3).setValue(t.done?"done":"open");
      ws.getRange(newRow,4).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
      addedCount++;
    });
    
    SpreadsheetApp.getUi().alert("📋 Импорт задач","Импортировано: "+addedCount+" задач\nВсего в Doc: "+tasks.length,SpreadsheetApp.getUi().ButtonSet.OK);
    return "Импортировано: "+addedCount;
  }catch(e){
    SpreadsheetApp.getUi().alert("Ошибка: "+e.message);
    return e.toString();
  }
}


function sendWarmupMessages(){
  // Прогрев после чек-листа. Человек скачал материал и пропал.
  // Через день напоминаем о себе, через четыре зовём на диагностику,
  // через десять последнее касание. Всё без участия менеджера
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Подписчики канала");
    if(!ws || ws.getLastRow()<2) return;

    var lastCol=Math.max(ws.getLastColumn(), 12);
    var headers=ws.getRange(1,1,1,lastCol).getValues()[0];
    var warmCol=-1;
    for(var h=0;h<headers.length;h++){
      if(String(headers[h]).trim()==="Прогрев"){ warmCol=h+1; break; }
    }
    if(warmCol<0){
      warmCol=ws.getLastColumn()+1;
      ws.getRange(1,warmCol).setValue("Прогрев").setFontWeight("bold")
        .setBackground("#000").setFontColor("#fff");
    }

    var lr=ws.getLastRow();
    var data=ws.getRange(2,1,lr-1,Math.max(lastCol,warmCol)).getValues();
    var now=new Date().getTime();
    var sent=0;

    for(var i=0;i<data.length;i++){
      var row=data[i];
      var d=row[0];
      if(!(d instanceof Date)) continue;
      var cid=String(row[1]||"").trim();
      if(!cid) continue;
      var status=String(row[9]||"").trim();
      // Резидентов и тех, кто уже дошёл до диагностики, не трогаем
      if(status==="Резидент" || status==="Диагностика") continue;

      var stage=parseInt(row[warmCol-1])||0;
      if(stage>=3) continue;

      var days=(now-d.getTime())/86400000;
      var firstName=String(row[3]||"").split(" ")[0]||"";
      var msg="", kb=null, newStage=stage;

      if(stage===0 && days>=1){
        msg=(firstName?firstName+", ":"")+"как вам чек-лист?\n\n"+
          "Если дошли до конца, скорее всего нашли пару мест, где теряются деньги. "+
          "Обычно так и бывает: проблема видна, а что с ней делать, непонятно.\n\n"+
          "У меня есть ещё материалы, загляните в меню.";
        kb={reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"📚 Другие чек-листы", callback_data:"sub_menu"}],
          [{text:"🔬 Пройти диагностику", callback_data:"diag_start"}]
        ]})};
        newStage=1;
      }
      else if(stage===1 && days>=4){
        msg=(firstName?firstName+", ":"")+"один вопрос.\n\n"+
          "Что из чек-листа вы уже внедрили?\n\n"+
          "Спрашиваю не просто так. Между «прочитал» и «сделал» обычно стоит операционка, "+
          "которая съедает неделю за неделей. Разбор нужен ровно для этого: "+
          "час, ваши цифры, план на десять дней.\n\n"+
          "Проводим вдвоём с Береке. Пятьдесят тысяч тенге.";
        kb={reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"📅 Записаться на разбор", callback_data:"want_diag"}],
          [{text:"🔬 Сначала диагностику в приложении", callback_data:"diag_start"}]
        ]})};
        newStage=2;
      }
      else if(stage===2 && days>=10){
        msg=(firstName?firstName+", ":"")+"последнее сообщение от меня.\n\n"+
          "Если тема сейчас не актуальна, просто игнорируйте, я больше не пишу.\n\n"+
          "Если актуальна, но что-то останавливает, напишите одно слово, что именно. "+
          "Отвечу лично.";
        kb={reply_markup:JSON.stringify({inline_keyboard:[
          [{text:"📅 Хочу разбор", callback_data:"want_diag"}]
        ]})};
        newStage=3;
      }

      if(msg){
        try{
          tgSend(cid, msg, kb);
          ws.getRange(i+2, warmCol).setValue(newStage);
          sent++;
          Utilities.sleep(300);
        }catch(e){ Logger.log("warmup "+cid+": "+e); }
      }
      if(sent>=20) break; // не больше 20 за запуск
    }

    if(sent) Logger.log("sendWarmupMessages: отправлено "+sent);
  }catch(e){ Logger.log("sendWarmupMessages: "+e); }
}

function _sendSubscriberWelcome(cid, firstName){
  // Приветствие подписчика. без акцепта, сразу предложение чек-листов из листа
  // Кнопки генерируются ДИНАМИЧЕСКИ. можно добавлять новые чек-листы прямо в таблицу
  
  // Эмодзи по умолчанию для известных ключей, остальные получают 📄
  var emojiMap={"sales":"💉","unit":"🩺","delegate":"⚙️","hire":"👥","marketing":"📢","cashflow":"💰","scripts":"💬","team_culture":"🧬","metrics":"📊","crisis":"🚨"};
  
  var inline_keyboard=[];
  // Главная кнопка. диагностика бизнеса (золотая, выделенная)
  inline_keyboard.push([{text:"🔬 Пройти диагностику бизнеса", callback_data:"diag_start"}]);
  var leadmagnetCount=0;
  try{
    var lmWs=_getLeadmagnetsSheet();
    var lrLm=lmWs.getLastRow();
    if(lrLm>=2){
      var lmData=lmWs.getRange(2,1,lrLm-1,5).getValues();
      lmData.forEach(function(r){
        var key=String(r[0]||"").trim();
        var title=String(r[1]||"").trim();
        var fileId=String(r[2]||"").trim();
        if(!key||!title)return;
        // Показываем кнопку только если файл загружен (есть file_id)
        if(!fileId)return;
        var emoji=emojiMap[key]||"📄";
        inline_keyboard.push([{text:emoji+" "+title, callback_data:"lm_"+key}]);
        leadmagnetCount++;
      });
    }
  }catch(e){Logger.log("welcome leadmagnets: "+e);}
  
  // Если ни одного чек-листа не загружено. показываем заглушку
  if(leadmagnetCount===0){
    inline_keyboard.push([{text:"📚 Узнать больше о BS", callback_data:"sub_about"}]);
  }
  
  // Всегда добавляем кнопку "Я резидент"
  inline_keyboard.push([{text:"✋ Я резидент BS. принять оферту", callback_data:"i_am_resident"}]);
  
  var countWord;
  if(leadmagnetCount===0)countWord="";
  else if(leadmagnetCount===1)countWord="<b>1 чек-лист</b>";
  else if(leadmagnetCount<5)countWord="<b>"+leadmagnetCount+" чек-листа</b>";
  else countWord="<b>"+leadmagnetCount+" чек-листов</b>";
  
  var msg = "Привет, "+firstName+"! 👋\n\n";
  msg += "Я бот <b>Business Surgery</b>. Помогаю предпринимателям выстраивать систему в бизнесе.\n\n";
  if(leadmagnetCount>0){
    msg += "У меня есть "+countWord+" про разные стороны бизнеса. Каждый. реальная история с цифрами и формулами.\n\nВыбери что зацепило:";
  } else {
    msg += "Скоро будут материалы для скачивания. А пока. могу рассказать про BS.";
  }
  
  var kb = {
    parse_mode: "HTML",
    reply_markup: JSON.stringify({inline_keyboard: inline_keyboard})
  };
  tgSend(cid, msg, kb);
  
  // Записываем подписчика в лист "Подписчики канала"
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsSub=ss.getSheetByName("Подписчики канала");
    if(wsSub){
      // Проверим что ещё не записан
      var lr=wsSub.getLastRow();
      var exists=false;
      if(lr>=2){
        var subData=wsSub.getRange(2,1,lr-1,1).getValues();
        for(var i=0;i<subData.length;i++){
          if(String(subData[i][0])===String(cid)){exists=true;break;}
        }
      }
      if(!exists){
        var newRow=lr+1;
        wsSub.getRange(newRow,1).setValue(cid);
        wsSub.getRange(newRow,2).setValue(firstName);
        wsSub.getRange(newRow,3).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
        wsSub.getRange(newRow,4).setValue("new");
      }
    }
  }catch(e){Logger.log("subscriber save: "+e);}
}


function generateContentBatch(idea){
  // Из одной идеи генерирует 7 единиц контента в разных форматах для разных каналов
  var apiKey=PropertiesService.getScriptProperties().getProperty("ANTHROPIC_API_KEY");
  if(!apiKey)apiKey=ANTHROPIC_API_KEY;
  if(!apiKey || apiKey==="вставьте_сюда"){
    return{error:"ANTHROPIC_API_KEY не установлен. Добавь его в Project Settings → Script Properties."};
  }
  
  var prompt = "Ты главный SMM-стратег Business Surgery (BS). клуба бизнес-трекинга в Алматы.\n\n";
  prompt += "Сайт bxclub.kz. Основатели: Кабден Рустам и Ерниязов Береке. 700+ разборов, 20 резидентов, средний рост ×3 к чистой прибыли.\n\n";
  prompt += "Уникальная концепция BS: бизнес как живой организм. Команда и продажи. руки и ноги. Финансы. кровь. Стратегия. мозг. Работаем с причиной на уровне ДНК, а не с симптомами. Метафора медицины и здоровья (но без агрессивной хирургии).\n\n";
  prompt += "Тариф входа: экспресс-разбор за 50 000 ₸ (со скидкой 50%). Тарифы основные: 3 месяца. 500 000 ₸, 1 год. 1 250 000 ₸. Цикл. каждые 10 дней диагностика и план задач.\n\n";
  prompt += "ICP: предприниматели 28-40 лет с чистой прибылью 2-20 млн ₸/мес. Прошёл курсы. Не внедряет. Бизнес держится на нём. Деньги есть, жизненной энергии нет. Команда фантомная. всё держится на нём. Стратегии много, внедрения. ноль. Думает много, делает мало. Обучается, не внедряет.\n\n";
  prompt += "Кейсы для упоминания (только реальные):\n";
  prompt += "- Исфандияр: с 200 тысяч до 2 млн чистыми. Сменили ЦА, подняли средний чек, кэв-воронка.\n";
  prompt += "- Казбек К9: организационная структура, найм фин директора, статус Astana Hub (1% налог). Чистая прибыль 15-20 млн/мес.\n";
  prompt += "- Артём: чистая прибыль ×3. Грант на новую нишу (магазин на Kaspi).\n";
  prompt += "- Даулет Ногайбек: ниша сайтов. ×3 дохода, найм команды, купил квартиру в Астане.\n";
  prompt += "- Елена Дружинина: 3 филиала языкового центра, опер директор, ×средний чек.\n";
  prompt += "- Дмитрий Цой: запуск частной школы 100 учеников с нуля под ключ.\n\n";
  prompt += "СТИЛЬ. Простой язык, конкретный, без воды. Короткие предложения. Можно 'И', 'Но'. Конкретные цифры.\n";
  prompt += "ЗАПРЕЩЕНО: тире ( ); AI-фразы ('погрузиться', 'раскрыть', 'давайте', 'прокачай', 'гарантированно', 'секреты успеха', 'хирург', 'операция'); ";
  prompt += "конструкции 'не X, а Y' и 'Это не просто X, а...'; больше 2 метафор на текст; больше 2 'X это Y'; ";
  prompt += "симметричные предложения и абзацы одинаковой длины; лестничный стиль (одиночные строки, дробление мысли); ";
  prompt += "размытые прилагательные ('нормальный', 'хороший') без конкретики; канцеляризмы ('важно понимать', 'стоит отметить', 'в современном мире', 'таким образом'); ";
  prompt += "абстрактные выводы без фактов; рваные 1-2 слова; конструкции 'Без X. Без Y.'\n";
  prompt += "ДА: метафора живого организма, ДНК, диагностика, корень проблемы, окружение, точный план.\n";
  prompt += "Реальные слоганы BS: 'Точная работа с бизнесом на уровне его ДНК', 'Каждые 10 дней. точная диагностика', 'Работаем с причиной на уровне ДНК, а не с симптомами', 'Отсутствие внешних проблем. это ещё не здоровье'.\n\n";
  prompt += "Тема для контент-батча: \""+idea+"\"\n\n";
  prompt += "Создай 7 единиц контента из этой одной темы. Верни ТОЛЬКО валидный JSON массив без markdown-обёртки. Формат каждого элемента:\n";
  prompt += "{\n";
  prompt += "  \"type\": \"telegram_post | instagram_carousel | reel | story | thread\",\n";
  prompt += "  \"title\": \"короткое название для понимания\",\n";
  prompt += "  \"text\": \"полный текст готовый к публикации\",\n";
  prompt += "  \"hook\": \"первая фраза-крючок\",\n";
  prompt += "  \"cta\": \"призыв к действию в конце\"\n";
  prompt += "}\n\n";
  prompt += "Что генерируем:\n";
  prompt += "1. telegram_post. пост 150-250 слов для Telegram канала. Боль → история → решение → CTA.\n";
  prompt += "2. instagram_carousel. текст для карусели Instagram (8-10 слайдов). Каждый слайд через '---'. Первый слайд. крючок 4-7 слов.\n";
  prompt += "3. reel. текст-скрипт Reels 30-45 сек. Структура: крючок (3 сек) → проблема (10 сек) → решение (20 сек) → CTA (5 сек).\n";
  prompt += "4. reel. второй вариант Reels по той же теме другим углом.\n";
  prompt += "5. story. текст для Instagram Stories (1 экран, до 50 слов).\n";
  prompt += "6. story. второй Stories с другим углом.\n";
  prompt += "7. thread. пост-thread для Threads 3-4 коротких реплики через '\\n\\n'.\n\n";
  prompt += "Конец каждого CTA должен подталкивать к разбору за 50 000 ₸ через сайт bxclub.kz или к чек-листам у бота.";
  
  var resp=UrlFetchApp.fetch("https://api.anthropic.com/v1/messages",{
    method:"post",
    contentType:"application/json",
    headers:{"x-api-key":apiKey, "anthropic-version":"2023-06-01"},
    payload:JSON.stringify({
      model:"claude-sonnet-4-5",
      max_tokens:8000,
      messages:[{role:"user", content:prompt}]
    }),
    muteHttpExceptions:true
  });
  
  var respText=resp.getContentText();
  var data;
  try{data=JSON.parse(respText);}catch(e){return{error:"Не удалось распарсить ответ API"};}
  
  if(!data.content || !data.content[0] || !data.content[0].text){
    return{error:"Anthropic API: "+JSON.stringify(data).substring(0,200)};
  }
  
  var aiText=data.content[0].text;
  // Уберём markdown обёртки если есть
  aiText=aiText.replace(/^```json\s*/i,"").replace(/```\s*$/,"").trim();
  
  var items;
  try{items=JSON.parse(aiText);}catch(parseE){
    Logger.log("Batch parse error. Text: "+aiText.substring(0,500));
    return{error:"AI вернул не-JSON. Попробуй ещё раз."};
  }
  
  if(!Array.isArray(items) || items.length===0){
    return{error:"AI вернул пустой массив"};
  }
  
  return{items:items};
}

function addBatchToContentPlan(items){
  // Распределение по листам:
  // telegram_post → "Контент-план" (автопостинг в канал)
  // instagram_*, reel, story, thread → "Контент-генератор" (идеи для ручной публикации)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    
    // Лист "Контент-план". только telegram_post
    var wsTg=ss.getSheetByName("Контент-план");
    if(!wsTg){_createContentPlanSheet(); wsTg=ss.getSheetByName("Контент-план");}
    
    // Лист "Контент-генератор". все остальные форматы
    var wsGen=ss.getSheetByName("Контент-генератор");
    if(!wsGen){
      wsGen=ss.insertSheet("Контент-генератор", ss.getNumSheets());
      var headers=["Дата создания","Формат","Заголовок","Текст","Статус","Платформа","Опубликован","Дата публикации"];
      wsGen.getRange(1,1,1,headers.length).setValues([headers]);
      wsGen.getRange(1,1,1,headers.length).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
      wsGen.setFrozenRows(1);
      wsGen.setColumnWidth(1,120);
      wsGen.setColumnWidth(2,150);
      wsGen.setColumnWidth(3,300);
      wsGen.setColumnWidth(4,500);
      wsGen.setColumnWidth(5,120);
      wsGen.setColumnWidth(6,150);
      wsGen.setColumnWidth(7,100);
      wsGen.setColumnWidth(8,140);
    }
    
    // Дата старта. первый ближайший слот публикации
    var startDate=new Date();
    startDate.setDate(startDate.getDate()+1);
    startDate.setHours(19,0,0,0);
    
    var addedToPlan=0;
    var addedToGen=0;
    var tgIndex=0;
    
    items.forEach(function(item){
      var type=String(item.type||"").toLowerCase();
      
      if(type==="telegram_post"){
        // В Контент-план. с датой публикации (по 1 в день)
        var newRow=wsTg.getLastRow()+1;
        var pubDate=new Date(startDate.getTime() + tgIndex*24*60*60*1000);
        tgIndex++;
        
        wsTg.getRange(newRow,1).setValue(pubDate).setNumberFormat("DD.MM.YYYY");
        wsTg.getRange(newRow,2).setValue("19:00");
        wsTg.getRange(newRow,3).setValue(item.title||"");
        wsTg.getRange(newRow,4).setValue(item.text);
        wsTg.getRange(newRow,5).setValue("Нет");
        wsTg.getRange(newRow,8).setValue("batch_ai");
        addedToPlan++;
      } else {
        // В Контент-генератор. без даты публикации (черновик)
        var newGen=wsGen.getLastRow()+1;
        var platform="Instagram";
        if(type.indexOf("thread")>=0)platform="Threads";
        if(type.indexOf("reel")>=0)platform="Instagram Reels";
        if(type.indexOf("story")>=0)platform="Instagram Stories";
        if(type.indexOf("carousel")>=0)platform="Instagram карусель";
        
        wsGen.getRange(newGen,1).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
        wsGen.getRange(newGen,2).setValue(item.type||"");
        wsGen.getRange(newGen,3).setValue(item.title||"");
        wsGen.getRange(newGen,4).setValue(item.text);
        wsGen.getRange(newGen,5).setValue("Черновик");
        wsGen.getRange(newGen,6).setValue(platform);
        wsGen.getRange(newGen,7).setValue("Нет");
        addedToGen++;
      }
    });
    
    return addedToPlan + addedToGen;
  }catch(e){
    Logger.log("addBatchToContentPlan: "+e);
    return 0;
  }
}

function generateContentIdeas(){
  // Генерирует 5 идей для постов на основе кейсов резидентов BS
  var apiKey=PropertiesService.getScriptProperties().getProperty("ANTHROPIC_API_KEY");
  if(!apiKey)apiKey=ANTHROPIC_API_KEY;
  if(!apiKey || apiKey==="вставьте_сюда"){
    return "❌ ANTHROPIC_API_KEY не установлен";
  }
  
  var prompt = "Ты главный SMM-стратег Business Surgery. клуба бизнес-трекинга в Алматы.\n\n";
  prompt += "Уникальная концепция BS: бизнес как живой организм. Команда. руки и ноги. Финансы. кровь. Стратегия. мозг. ДНК бизнеса. Каждые 10 дней разбор.\n\n";
  prompt += "ICP: предприниматели 28-40 лет, прибыль 2-20 млн ₸/мес. Прошёл курсы, не внедряет. Деньги есть, энергии нет.\n\n";
  prompt += "Кейсы: Исфандияр (200к→2млн), Казбек К9 (15-20млн чистой), Артём (×3), Даулет (×3 доход + квартира).\n\n";
  prompt += "ЗАПРЕЩЕНО: тире ( ), AI-фразы, 'прокачай', 'гарантированно', 'секреты успеха', 'хирург', 'операция'.\n";
  prompt += "ДА: метафоры органа, ДНК, диагностика, корень, окружение.\n\n";
  prompt += "Предложи 5 свежих тем для постов в @bsurgery_kz и Instagram @business.surgery которые:\n";
  prompt += "1. Бьют в боль ICP (узнавание себя)\n";
  prompt += "2. Дают конкретный инсайт\n";
  prompt += "3. Опираются на реальную практику разборов BS\n\n";
  prompt += "Формат ответа. нумерованный список:\n";
  prompt += "1. [Заголовок темы]\n";
  prompt += "   Угол: краткое описание угла подачи\n\n";
  prompt += "Темы должны быть разными. про метрики, делегирование, найм, продажи, маркетинг.";
  
  var resp=UrlFetchApp.fetch("https://api.anthropic.com/v1/messages",{
    method:"post",
    contentType:"application/json",
    headers:{"x-api-key":apiKey, "anthropic-version":"2023-06-01"},
    payload:JSON.stringify({
      model:"claude-sonnet-4-5",
      max_tokens:2000,
      messages:[{role:"user", content:prompt}]
    }),
    muteHttpExceptions:true
  });
  
  var data=JSON.parse(resp.getContentText());
  if(data.content && data.content[0] && data.content[0].text){
    return data.content[0].text;
  }
  return "Не удалось сгенерировать";
}


function _miniGenerateIdeas(p){
  try{
    var ideas=generateContentIdeas();
    if(!ideas || ideas.indexOf("ANTHROPIC")>=0){
      return{error:"ANTHROPIC_API_KEY не установлен или нет кредита"};
    }
    return{ok:true, ideas:ideas};
  }catch(e){return{error:e.toString()};}
}

function _miniGenerateBatch(p){
  try{
    var idea=String(p.idea||"").trim();
    if(!idea)return{error:"Пустая идея"};
    var batch=generateContentBatch(idea);
    if(batch.error)return{error:batch.error};
    var added=addBatchToContentPlan(batch.items);
    return{ok:true, added:added, items:batch.items};
  }catch(e){return{error:e.toString()};}
}

function _miniGeneratePost(p){
  try{
    var idea=String(p.idea||"").trim();
    if(!idea)return{error:"Пустая идея"};
    var post=generatePostFromIdea(idea, "Идея от админа");
    return{ok:true, post:post};
  }catch(e){return{error:e.toString()};}
}

function _miniPublishPost(p){
  try{
    var text=String(p.text||"").trim();
    if(!text)return{error:"Пустой текст"};
    var pubResult=_sendToChannel(text);
    if(pubResult && pubResult.ok)return{ok:true};
    return{error:"Не удалось опубликовать"};
  }catch(e){return{error:e.toString()};}
}

function _miniGetPendingResidents(p){
  // Возвращает список тех кто нажал "Я резидент BS". ждёт подтверждение админа
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Подписчики канала");
    if(!ws)return{ok:true, pending:[]};
    var lr=ws.getLastRow();
    if(lr<2)return{ok:true, pending:[]};
    var data=ws.getRange(2,1,lr-1,12).getValues();
    var pending=[];
    data.forEach(function(row, idx){
      var chatId=String(row[1]||"").trim();
      var name=row[3]||"";
      var status=String(row[9]||"").trim();
      if(status==="pending_resident"){
        pending.push({chatId:chatId, name:name, row:idx+2});
      }
    });
    return{ok:true, pending:pending};
  }catch(e){return{error:e.toString()};}
}

function _miniApproveResident(p){
  // Админ подтверждает резидентство. Шлём welcome пакет: оферту → доступ к приложению → группа → инструкции
  try{
    var chatId=String(p.chatId||"").trim();
    if(!chatId)return{error:"Нет chatId"};
    
    var ss=SpreadsheetApp.openById(SS_ID);
    var foundName="";
    var userTg="";
    
    // Сначала ищем в Подписчиках канала (старая логика)
    var ws=ss.getSheetByName("Подписчики канала");
    if(ws){
      var lr=ws.getLastRow();
      if(lr>=2){
        var data=ws.getRange(2,1,lr-1,4).getValues();
        for(var i=0;i<data.length;i++){
          if(String(data[i][1])===chatId){
            foundName=String(data[i][3]||"Резидент");
            // Меняем статус на approved_resident
            ws.getRange(i+2,10).setValue("approved_resident");
            break;
          }
        }
      }
    }
    
    // Если в подписчиках не нашли, ищем в заявках на резидентство
    if(!foundName){
      var wsReq=ss.getSheetByName("Заявки на резидентство");
      if(wsReq){
        var lrReq=wsReq.getLastRow();
        if(lrReq>=2){
          var dataReq=wsReq.getRange(2,1,lrReq-1,7).getValues();
          for(var i=0;i<dataReq.length;i++){
            if(String(dataReq[i][1])===chatId){
              foundName=String(dataReq[i][2]||"Резидент");
              userTg=String(dataReq[i][3]||"").replace("@","");
              // Меняем статус
              wsReq.getRange(i+2,5).setValue("Подтверждено");
              wsReq.getRange(i+2,7).setValue(new Date());
              break;
            }
          }
        }
      }
    }
    
    if(!foundName) foundName = "Резидент";
    var firstName = foundName.split(" ")[0];
    
    // Сбрасываем кэш заявки
    try{
      CacheService.getScriptCache().remove("pending_resident_"+chatId);
    }catch(e){}
    
    // ШАГ 1. Шлём приветствие
    var welcome = "🎉 <b>Добро пожаловать в Business Surgery, " + firstName + "!</b>\n\n";
    welcome += "Я подтвердил твоё резидентство. Теперь у тебя есть доступ ко всем материалам клуба.\n\n";
    welcome += "<b>Что дальше:</b>\n";
    welcome += "1. Прими оферту (следующее сообщение)\n";
    welcome += "2. Получи ссылку на закрытый чат резидентов\n";
    welcome += "3. Открой Mini App для управления встречами\n\n";
    welcome += "Если есть вопросы. пиши прямо в этот чат.";
    
    try{ tgSend(chatId, welcome, {parse_mode:"HTML"}); }catch(e){ Logger.log("welcome: "+e); }
    
    // ШАГ 2. Через секунду шлём оферту
    Utilities.sleep(800);
    var policy = POLICY_TEXT.replace("{имя}", firstName);
    try{ tgSendAccept(chatId, policy); }catch(e){ Logger.log("policy: "+e); }
    
    // ШАГ 3. Шлём ссылки на группу и Mini App
    Utilities.sleep(800);
    var groupInfo = "🔗 <b>Доступы резидента BS</b>\n\n";
    groupInfo += "💬 <b>Закрытый чат резидентов</b> ниже по кнопке. Внутри. знакомства, обсуждения, договорённости.\n\n";
    groupInfo += "📱 <b>Mini App клуба:</b> твои встречи, штрафы, отчёты, список других резидентов, контент.\n\n";
    groupInfo += "📋 <b>Что от тебя ждём:</b>\n";
    groupInfo += "• Отчёты по средам и пятницам (бот напомнит)\n";
    groupInfo += "• Присутствие на разборах каждые 10 дней\n";
    groupInfo += "• Активность в чате клуба\n\n";
    groupInfo += "Добро пожаловать в семью BS 🤝";
    
    var keyboard = {
      inline_keyboard: [
        [{text:"👥 Вступить в чат резидентов", url:"https://t.me/+M88HVtcZNghjNTM6"}],
        [{text:"📱 Открыть Mini App", web_app:{url:getWebAppUrl()}}]
      ]
    };
    
    try{ 
      tgSend(chatId, groupInfo, {parse_mode:"HTML", reply_markup:JSON.stringify(keyboard)}); 
    }catch(e){ Logger.log("groupInfo: "+e); }
    
    // ШАГ 4. Уведомим админов что подтверждение прошло
    var adminMsg = "✅ <b>Новый резидент</b>\n\n";
    adminMsg += "👤 " + foundName + "\n";
    adminMsg += "🆔 " + chatId + "\n\n";
    adminMsg += "Welcome пакет отправлен. Не забудь добавить в закрытый чат резидентов.";
    
    try{ tgSendAdmins(adminMsg, "subscribers"); }catch(e){}
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniApproveResident: "+e);
    return{error:e.toString()};
  }
}



function cleanContentPlanGarbage(){
  // Удаляет из Контент-плана записи которые не telegram_post (старый формат с [reel] [story] и т.п.)
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Контент-план");
    if(!ws){SpreadsheetApp.getUi().alert("Нет листа Контент-план");return;}
    var lr=ws.getLastRow();
    if(lr<2){SpreadsheetApp.getUi().alert("Контент-план пустой");return;}
    
    var data=ws.getRange(2,1,lr-1,8).getValues();
    var deletedRows=[];
    var moved=0;
    
    // Проверим есть ли лист "Контент-генератор", создадим если нет
    var wsGen=ss.getSheetByName("Контент-генератор");
    if(!wsGen){
      wsGen=ss.insertSheet("Контент-генератор", ss.getNumSheets());
      var headers=["Дата создания","Формат","Заголовок","Текст","Статус","Платформа","Опубликован","Дата публикации"];
      wsGen.getRange(1,1,1,headers.length).setValues([headers]);
      wsGen.getRange(1,1,1,headers.length).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
      wsGen.setFrozenRows(1);
    }
    
    for(var i=data.length-1;i>=0;i--){
      var title=String(data[i][2]||"");
      var text=String(data[i][3]||"");
      var pubFlag=String(data[i][4]||"").trim().toLowerCase();
      
      // Если уже опубликован. оставляем (история)
      if(pubFlag==="да")continue;
      
      // Мусор: содержит [reel], [story], [thread], [carousel], [instagram_*]
      // или текст содержит '---' (карусель) или короче 100 символов
      var isMusor=false;
      if(/\[(reel|story|thread|instagram_)/i.test(title))isMusor=true;
      else if(text.indexOf("---")>=0)isMusor=true;
      else if(text.length<100)isMusor=true;
      else if(/\bкрючок\b|\b\d+\s*сек\b/i.test(text))isMusor=true;
      
      if(isMusor){
        // Переносим в Контент-генератор
        var newGen=wsGen.getLastRow()+1;
        var type="мусор";
        var titleMatch=title.match(/\[([^\]]+)\]/);
        if(titleMatch)type=titleMatch[1];
        var cleanTitle=title.replace(/\[[^\]]+\]\s*/,"").trim();
        
        wsGen.getRange(newGen,1).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
        wsGen.getRange(newGen,2).setValue(type);
        wsGen.getRange(newGen,3).setValue(cleanTitle);
        wsGen.getRange(newGen,4).setValue(text);
        wsGen.getRange(newGen,5).setValue("Черновик");
        wsGen.getRange(newGen,6).setValue("Instagram");
        wsGen.getRange(newGen,7).setValue("Нет");
        
        ws.deleteRow(i+2);
        moved++;
      }
    }
    
    SpreadsheetApp.flush();
    SpreadsheetApp.getUi().alert("✅ Очистка Контент-плана","Перенесено в Контент-генератор: "+moved+" записей\n\nВ Контент-плане теперь только посты для Telegram канала.",SpreadsheetApp.getUi().ButtonSet.OK);
  }catch(e){
    SpreadsheetApp.getUi().alert("Ошибка: "+e.message);
  }
}


function _miniGetLeadmagnetsList(p){
  // Одноразовая миграция: поменять fileId между unit и marketing (исправление перепутанных файлов)
  try{ _oneTimeFixLeadmagnetSwap(); }catch(e){ Logger.log("auto-fix: "+e); }

  // Возвращает список чек-листов + что пользователь уже забрал
  try{
    var ws=_getLeadmagnetsSheet();
    var lr=ws.getLastRow();
    var items=[];
    if(lr>=2){
      // Колонки: key, title, fileId, description, category
      var data=ws.getRange(2,1,lr-1,5).getValues();
      data.forEach(function(r){
        items.push({
          key: String(r[0]||""),
          title: String(r[1]||""),
          fileId: String(r[2]||""),
          description: String(r[3]||""),
          category: String(r[4]||"").toLowerCase()
        });
      });
    }
    
    // Что уже забрал. смотрим в листе "История лид-магнитов"
    var taken={};
    try{
      var chatId=String(p.chatId||"");
      if(chatId){
        var ss=SpreadsheetApp.openById(SS_ID);
        var wsH=ss.getSheetByName("История лид-магнитов");
        if(wsH){
          var lrH=wsH.getLastRow();
          if(lrH>=2){
            var hData=wsH.getRange(2,1,lrH-1,3).getValues();
            hData.forEach(function(row){
              if(String(row[0])===chatId){
                taken[String(row[1])]=true;
              }
            });
          }
        }
      }
    }catch(takenE){Logger.log("taken: "+takenE);}
    
    return{ok:true, items:items, taken:taken};
  }catch(e){return{error:e.toString()};}
}

function _miniUpdateLeadmagnet(p){
  // Обновляет данные чек-листа: title, description, category
  try{
    var key=String(p.key||"").trim();
    var newTitle=String(p.title||"").trim();
    var newDesc=String(p.description||"").trim();
    var newCat=String(p.category||"").trim().toLowerCase();
    if(!key)return{error:"Нет key"};
    if(!newTitle)return{error:"Нет названия"};
    
    var ws=_getLeadmagnetsSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{error:"Лист пуст"};
    
    var data=ws.getRange(2,1,lr-1,5).getValues();
    var foundRow=-1;
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===key){
        foundRow=i+2;
        break;
      }
    }
    if(foundRow<0)return{error:"Чек-лист не найден"};
    
    // Обновляем колонки B (title), D (description), E (category)
    ws.getRange(foundRow,2).setValue(newTitle);
    ws.getRange(foundRow,4).setValue(newDesc);
    ws.getRange(foundRow,5).setValue(newCat);
    
    // Гарантируем что есть заголовки колонок D и E
    var headers=ws.getRange(1,1,1,5).getValues()[0];
    if(!headers[3]) ws.getRange(1,4).setValue("Описание");
    if(!headers[4]) ws.getRange(1,5).setValue("Категория");
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniUpdateLeadmagnet: "+e);
    return{error:e.toString()};
  }
}


function _oneTimeFixLeadmagnetSwap(){
  // Разовая миграция. Меняет fileId местами между двумя ключами если флаг не стоит.
  // Запускается из _miniGetLeadmagnetsList. Выполняется максимум один раз благодаря Script Properties.
  var props=PropertiesService.getScriptProperties();
  if(props.getProperty("FIX_LM_UNIT_MARKETING_DONE")==="1") return;
  
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Лид-магниты");
    if(!ws)return;
    var lr=ws.getLastRow();
    if(lr<2)return;
    
    var data=ws.getRange(2,1,lr-1,3).getValues();
    var rowUnit=-1, rowMkt=-1;
    for(var i=0;i<data.length;i++){
      var key=String(data[i][0]||"").toLowerCase().trim();
      if(key==="unit") rowUnit=i+2;
      else if(key==="marketing") rowMkt=i+2;
    }
    if(rowUnit<0 || rowMkt<0)return;
    
    var fileUnit=ws.getRange(rowUnit,3).getValue();
    var fileMkt=ws.getRange(rowMkt,3).getValue();
    
    ws.getRange(rowUnit,3).setValue(fileMkt);
    ws.getRange(rowMkt,3).setValue(fileUnit);
    
    props.setProperty("FIX_LM_UNIT_MARKETING_DONE","1");
    Logger.log("Разовый своп выполнен: unit↔marketing");
  }catch(e){
    Logger.log("_oneTimeFixLeadmagnetSwap: "+e);
  }
}


function _miniSwapLeadmagnetFiles(p){
  // Меняет местами fileId двух чек-листов. Решает проблему "перепутаны"
  try{
    var keyA=String(p.keyA||"").trim();
    var keyB=String(p.keyB||"").trim();
    if(!keyA || !keyB)return{error:"Нужны оба ключа"};
    if(keyA===keyB)return{error:"Выбраны одинаковые"};
    
    var ws=_getLeadmagnetsSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{error:"Лист пуст"};
    
    var data=ws.getRange(2,1,lr-1,5).getValues();
    var rowA=-1, rowB=-1;
    for(var i=0;i<data.length;i++){
      if(String(data[i][0])===keyA) rowA=i+2;
      if(String(data[i][0])===keyB) rowB=i+2;
    }
    if(rowA<0 || rowB<0)return{error:"Чек-лист не найден"};
    
    var fileA=ws.getRange(rowA,3).getValue();
    var fileB=ws.getRange(rowB,3).getValue();
    ws.getRange(rowA,3).setValue(fileB);
    ws.getRange(rowB,3).setValue(fileA);
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniSwapLeadmagnetFiles: "+e);
    return{error:e.toString()};
  }
}

function _miniTestLeadmagnet(p){
  // Отправляет чек-лист админу для проверки (себе)
  try{
    var key=String(p.key||"").trim();
    var chatId=String(p.chatId||"").trim();
    if(!key || !chatId)return{error:"Не определены параметры"};
    
    // Только для админов
    if(ADMIN_IDS.indexOf(chatId)<0)return{error:"Только админам"};
    
    var ws=_getLeadmagnetsSheet();
    var lr=ws.getLastRow();
    var lm=null;
    if(lr>=2){
      var data=ws.getRange(2,1,lr-1,5).getValues();
      for(var i=0;i<data.length;i++){
        if(String(data[i][0])===key){
          lm={key:data[i][0], title:data[i][1], fileId:data[i][2]};
          break;
        }
      }
    }
    if(!lm)return{error:"Не найден"};
    if(!lm.fileId)return{error:"fileId пуст"};
    
    var caption="🔍 <b>ТЕСТ:</b> "+lm.title+"\n\nПроверь что содержимое PDF соответствует названию. Если нет. используй кнопку 🔄 Поменять местами в Mini App.";
    var sent=_sendLeadmagnetDocument(chatId, lm.fileId, caption);
    
    if(sent)return{ok:true};
    return{error:"Не удалось отправить"};
  }catch(e){return{error:e.toString()};}
}



function _miniRequestLeadmagnet(p){
  // Отправляет чек-лист в Telegram пользователю
  try{
    var key=String(p.key||"").trim();
    var chatId=String(p.chatId||"").trim();
    if(!key)return{error:"Нет ключа"};
    if(!chatId)return{error:"Не определён пользователь"};
    
    var ws=_getLeadmagnetsSheet();
    var lr=ws.getLastRow();
    var lm=null;
    if(lr>=2){
      var data=ws.getRange(2,1,lr-1,5).getValues();
      for(var i=0;i<data.length;i++){
        if(String(data[i][0])===key){
          lm={key:data[i][0], title:data[i][1], fileId:data[i][2]};
          break;
        }
      }
    }
    if(!lm)return{error:"Чек-лист не найден"};
    if(!lm.fileId)return{error:"Чек-лист ещё не загружен"};
    
    var caption="📚 <b>"+lm.title+"</b>\n\nЧитай вдумчиво. Применяй. Через пару дней напишу узнать как пошло.";
    var sent=_sendLeadmagnetDocument(chatId, lm.fileId, caption);
    
    if(sent){
      // Запоминаем что отправили
      try{
        _setSubscriberField(chatId,"Получил_чек_лист",new Date().toISOString());
        _setSubscriberField(chatId,"Последний_чек_лист",lm.title);
        
        // Запись в "Историю лид-магнитов". для отображения статуса в Mini App
        var ss=SpreadsheetApp.openById(SS_ID);
        var wsH=ss.getSheetByName("История лид-магнитов");
        if(!wsH){
          wsH=ss.insertSheet("История лид-магнитов", ss.getNumSheets());
          wsH.getRange(1,1,1,4).setValues([["Chat ID","Ключ","Дата","Название"]]);
          wsH.getRange(1,1,1,4).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
          wsH.setFrozenRows(1);
        }
        wsH.appendRow([chatId, key, new Date(), lm.title]);
      }catch(setE){Logger.log("setField: "+setE);}
      return{ok:true};
    }
    return{error:"Не удалось отправить"};
  }catch(e){return{error:e.toString()};}
}


function _miniSubmitDiagnostic(p){
  // Приём заявки на диагностику из Mini App
  try{
    var name=String(p.name||"").trim();
    var phone=String(p.phone||"").trim();
    var niche=String(p.niche||"").trim();
    var request=String(p.request||"").trim();
    var chatId=String(p.chatId||"").trim();
    
    if(!name)return{error:"Введи имя"};
    if(!phone)return{error:"Введи телефон"};
    
    // Сохраняем в лист "Заявки на диагностику"
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Заявки на диагностику");
    if(!ws){
      ws=ss.insertSheet("Заявки на диагностику", ss.getNumSheets());
      var headers=["Дата","Имя","Телефон","Ниша","Запрос","Chat ID","Статус"];
      ws.getRange(1,1,1,headers.length).setValues([headers]);
      ws.getRange(1,1,1,headers.length).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
      ws.setFrozenRows(1);
      ws.setColumnWidth(1,140);
      ws.setColumnWidth(2,180);
      ws.setColumnWidth(3,160);
      ws.setColumnWidth(4,200);
      ws.setColumnWidth(5,400);
      ws.setColumnWidth(6,140);
      ws.setColumnWidth(7,120);
    }
    var newRow=ws.getLastRow()+1;
    ws.getRange(newRow,1).setValue(new Date()).setNumberFormat("DD.MM.YYYY HH:mm");
    ws.getRange(newRow,2).setValue(name);
    ws.getRange(newRow,3).setValue(phone);
    ws.getRange(newRow,4).setValue(niche);
    ws.getRange(newRow,5).setValue(request);
    ws.getRange(newRow,6).setValue(chatId);
    ws.getRange(newRow,7).setValue("Новая");
    
    // Уведомляем админов
    var notifyMsg="⚡ <b>Новая заявка на диагностику</b>\n\n";
    notifyMsg+="👤 "+name+"\n";
    notifyMsg+="📞 "+phone+"\n";
    if(niche)notifyMsg+="📌 "+niche+"\n";
    if(request)notifyMsg+="\n💬 "+request+"\n";
    notifyMsg+="\n🆔 "+chatId;
    tgSendAdmins(notifyMsg,"subscribers");
    
    // Ответное сообщение пользователю в Telegram
    if(chatId){
      try{
        tgSend(chatId,"✅ "+name+", заявка принята!\n\nМы свяжемся с тобой в течение дня по WhatsApp "+phone+" чтобы согласовать время диагностики.\n\nЭкспресс-разбор 50 000 ₸ ведут оба основателя (Рустам и Береке). 1 час, точный диагноз и план задач на 10 дней.");
      }catch(tgE){Logger.log("notify user: "+tgE);}
    }
    
    return{ok:true};
  }catch(e){
    Logger.log("_miniSubmitDiagnostic: "+e);
    return{error:e.toString()};
  }
}


function _miniCheckUserRole(p){
  // Возвращает реальную роль пользователя: lead | resident | admin
  // ИСПРАВЛЕНО: раньше искал несуществующий лист "Резиденты". правильный лист "BS - резиденты дебет", Chat ID в колонке N (14)
  try{
    var chatId = String(p.chatId||"").trim();
    if(!chatId){
      return {role:"lead"};
    }
    
    // Проверка админ?
    if(ADMIN_IDS.indexOf(chatId) >= 0){
      return {role:"admin"};
    }
    
    // Кеш роли на 10 минут (быстрый повторный вход)
    var roleCache=CacheService.getScriptCache();
    var cached=roleCache.get("role_"+chatId);
    if(cached){
      return {role:cached, cached:true};
    }
    // Кеш роли короткий (60 сек): резидента добавили. доступ появляется почти сразу
    
    // Проверка резидент: лист "BS - резиденты дебет", имя в B (2), Chat ID в N (14)
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("BS - резиденты дебет");
    if(ws){
      var lr = ws.getLastRow();
      if(lr >= 3){
        var ids = ws.getRange(3, 14, lr-2, 1).getValues();  // колонка N
        var fired = ws.getRange(3, 11, lr-2, 1).getValues(); // колонка K (Бывший)
        for(var i=0; i<ids.length; i++){
          if(String(ids[i][0]||"").trim() === chatId){
            // Бывших резидентов считаем лидами
            if(String(fired[i][0]||"").trim()==="Да"){
              roleCache.put("role_"+chatId, "lead", 60);
              return {role:"lead", former:true};
            }
            roleCache.put("role_"+chatId, "resident", 60);
            return {role:"resident", row:i+3};
          }
        }
      }
    }
    
    roleCache.put("role_"+chatId, "lead", 60);
    return {role:"lead"};
    
  }catch(e){
    Logger.log("_miniCheckUserRole: "+e);
    return {role:"lead", error:e.toString()};
  }
}

function _miniRequestResident(p){
  // Заявка от пользователя на резидентство. Шлёт админам на подтверждение.
  try{
    var chatId = String(p.chatId||"").trim();
    var userName = String(p.userName||"").trim();
    var userTg = String(p.userTg||"").trim();
    
    if(!chatId){
      return {error:"Не определён пользователь"};
    }
    
    // Проверим, не резидент ли уже
    var roleCheck = _miniCheckUserRole(p);
    if(roleCheck.role === "resident"){
      return {alreadyResident:true};
    }
    if(roleCheck.role === "admin"){
      return {error:"Вы админ, не нужно подавать заявку"};
    }
    
    // Проверим, нет ли уже активной заявки
    var cache = CacheService.getScriptCache();
    var pendingKey = "pending_resident_"+chatId;
    var existing = cache.get(pendingKey);
    if(existing){
      return {alreadyPending:true};
    }
    
    // Сохраним заявку в кэш на 24 часа
    cache.put(pendingKey, JSON.stringify({
      chatId: chatId,
      userName: userName,
      userTg: userTg,
      requestedAt: new Date().toISOString()
    }), 86400);
    
    // Сохраним в лист "Заявки на резидентство"
    try{
      var ss = SpreadsheetApp.openById(SS_ID);
      var ws = ss.getSheetByName("Заявки на резидентство");
      if(!ws){
        ws = ss.insertSheet("Заявки на резидентство", ss.getNumSheets());
        ws.getRange(1,1,1,7).setValues([["Дата","Chat ID","Имя","Telegram","Статус","Кто решил","Дата решения"]]);
        ws.getRange(1,1,1,7).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
        ws.setFrozenRows(1);
      }
      ws.appendRow([
        new Date(), chatId, userName, userTg ? "@"+userTg : "",
        "Ожидает подтверждения", "", ""
      ]);
    }catch(saveE){ Logger.log("Save request: "+saveE); }
    
    // Уведомление админам с кнопками
    var msg = "🙋 <b>Заявка на резидентство</b>\n\n";
    msg += "👤 " + (userName || "Без имени");
    if(userTg) msg += " @" + userTg;
    msg += "\n🆔 " + chatId + "\n\n";
    msg += "Подтверждаешь резидентство?";
    
    var keyboard = {
      inline_keyboard: [[
        {text:"✅ Подтвердить", callback_data:"approve_res_"+chatId},
        {text:"❌ Отклонить",   callback_data:"reject_res_"+chatId}
      ]]
    };
    
    ADMIN_IDS.forEach(function(adminId){
      try{
        tgSend(adminId, msg, {parse_mode:"HTML", reply_markup:JSON.stringify(keyboard)});
      }catch(e){ Logger.log("tg admin: "+e); }
    });
    
    // Ответ резиденту что заявка отправлена
    try{
      tgSend(chatId, 
        "📩 <b>Заявка отправлена</b>\n\nМы получили твой запрос на резидентство. Береке и Рустам подтвердят в течение дня и пришлют тебе все материалы.\n\nЕсли нужно срочно. напиши в WhatsApp +7 702 403 50 36",
        {parse_mode:"HTML"}
      );
    }catch(e){}
    
    return {ok:true};
    
  }catch(e){
    Logger.log("_miniRequestResident: "+e);
    return {error:e.toString()};
  }
}



// === ДИАГНОСТИКА БИЗНЕСА (через бот) ===

function _getDiagnosticAreas(){
  // ВСЕ 7 областей. синхронизировано с Mini App
  return [
    {id:"brain", organ:"Мозг", label:"Стратегия", icon:"🧠", questions:[
      {q:"Есть письменный план развития на 12 мес с конкретными цифрами выручки и прибыли?", a:["Да. План с целями по месяцам и KPI","Есть направление, без жёстких цифр","Нет, живём от месяца к месяцу"]},
      {q:"Стратегические сессии собственник+ключевые сотрудники хотя бы раз в квартал?", a:["Да. Регулярная сессия с пересмотром планов","Иногда собираемся без структуры","Нет. Стратегией занимаюсь сам"]},
      {q:"Понимаешь на чём именно зарабатывает бизнес (какой продукт даёт основной кэш)?", a:["Да. Знаю долю каждого продукта","Примерно представляю","Зарабатываем со всего понемногу"]},
      {q:"Понимаешь главную точку роста на ближайшие 3 месяца?", a:["Да. Один фокус подчинено всё","Есть 3-5 направлений","Тушим пожары"]},
      {q:"Понимаешь ради чего лично строишь бизнес (личное видение)?", a:["Да. Чёткое видение и понимание","Когда то было, сейчас размылось","Нет"]}
    ]},
    {id:"heart", organ:"Сердце", label:"Маркетинг", icon:"🫀", questions:[
      {q:"Прописан портрет идеального клиента (возраст, доход, боли)?", a:["Да. Прописан с конкретикой","Примерно понимаю","Продаём всем кто платит"]},
      {q:"Знаешь CAC и LTV?", a:["Да. Считаю по каждому каналу","Знаю одно из двух","Не считаю"]},
      {q:"Сколько каналов привлечения работают системно?", a:["3 и более с понятным CPL","1-2 канала","Сарафан и удача"]},
      {q:"Ведёшь личный бренд в соцсетях регулярно?", a:["Да. Контент-план, 2+ раза в неделю","Иногда что-то выкладываю","Нет"]},
      {q:"Есть воронка прогрева (лид-магнит → касания → продажа)?", a:["Да. Автоматизирована","Что-то есть без замеров","Нет. Только в лоб"]}
    ]},
    {id:"hands", organ:"Руки и ноги", label:"Продажи", icon:"💪", questions:[
      {q:"Есть CRM где видны все заявки и сделки?", a:["Да. Дисциплина у менеджеров","Есть но игнорируют","Всё в WhatsApp"]},
      {q:"Знаешь конверсии воронки и где утечка?", a:["Да. Считаю недельно","Знаю общую","Не считаю"]},
      {q:"Прописаны скрипты продаж и есть обучение?", a:["Да. Скрипты и контроль","Скрипты есть но не работают","Нет"]},
      {q:"Есть РОП или ты сам управляешь?", a:["РОП есть","Один сильный продажник","Сам как умею"]},
      {q:"Среднее время первого ответа на заявку?", a:["Меньше 5 минут","До часа","Час и больше"]}
    ]},
    {id:"spine", organ:"Костяк", label:"Команда", icon:"🦴", questions:[
      {q:"Прописана оргструктура (кто кому подчиняется)?", a:["Да. У каждого зона","Понимаем но не записано","Все делают всё"]},
      {q:"У ключевых сотрудников прописаны KPI и регламент?", a:["Да у всех ключевых","Есть у нескольких","Нет"]},
      {q:"Если уедешь на месяц бизнес выживет?", a:["Да. Есть замы","Тяжело но выживет","Всё на мне"]},
      {q:"Есть система найма?", a:["Да. Воронка работает","Когда припрёт по знакомым","Нет системы"]},
      {q:"Лучшие сотрудники получают выше рынка?", a:["Сильно выше рынка","По рынку","Лучшие уходят"]}
    ]},
    {id:"blood", organ:"Кровь", label:"Финансы", icon:"🩸", questions:[
      {q:"Ведёшь ОПиУ ежемесячно?", a:["Да. Закрытие до 5 числа","Иногда смотрим","Считаю в голове"]},
      {q:"Ведёшь ДДС и видишь кассовые разрывы заранее?", a:["Да. Прогноз на 30+ дней","Знаю остатки на сегодня","Узнаю когда случается"]},
      {q:"Знаешь маржинальность по каждому продукту?", a:["Да. По каждому SKU","Среднюю по бизнесу","Не разделяю"]},
      {q:"Отделены личные деньги от бизнеса?", a:["Получаю ЗП и дивиденды","Беру по необходимости","Касса = карман"]},
      {q:"Знаешь точку безубыточности?", a:["Да. Сверяюсь ежемесячно","Примерно","Не считал"]}
    ]},
    {id:"dna", organ:"ДНК", label:"Процессы", icon:"🧬", questions:[
      {q:"Есть SOP на ключевые процессы?", a:["Да. У каждого процесса","На некоторые есть","Всё в голове"]},
      {q:"Автоматизированы рутинные задачи?", a:["Да на всех","Кое-что","Всё руками"]},
      {q:"Сколько часов в день на операционку?", a:["Меньше 2 часов","4-6 часов","Весь день"]},
      {q:"Когда последний раз улучшал процесс?", a:["В этом месяце","Несколько месяцев назад","Не помню"]},
      {q:"Есть ответственный за процессы (опер директор)?", a:["Да","Зам частично","Это я"]}
    ]},
    {id:"eyes", organ:"Зрение", label:"Аналитика", icon:"👁", questions:[
      {q:"Есть дашборд с ключевыми метриками?", a:["Да. Автообновление","Сводка вручную","Нет"]},
      {q:"Знаешь сейчас без проверки выручку прибыль и средний чек за прошлый месяц?", a:["Да. Всегда в голове","Знаю выручку","Надо смотреть"]},
      {q:"Крупные решения принимаешь по цифрам?", a:["Да. С расчётом окупаемости","Цифры смотрю но интуитивно","По ощущениям"]},
      {q:"Видишь воронку маркетинга и продаж в одном месте?", a:["Да. С конверсиями","Частично","Только постфактум"]},
      {q:"Регулярный разбор результатов с командой?", a:["Понедельная планёрка","Иногда","Сам в голове"]}
    ]}
  ];
}

function _startBotDiagnostic(cid, name){
  // Инициализируем состояние диагностики
  var cache = CacheService.getScriptCache();
  cache.put("diag_state_"+cid, JSON.stringify({
    areaIdx: 0,
    questionIdx: 0,
    answers: {}
  }), 3600);
  
  var firstName = (name||"").split(" ")[0] || "друг";
  
  var msg = "🔬 <b>Диагностика бизнеса</b>\n\n";
  msg += firstName + ", сейчас замерим здоровье твоего бизнеса по 7 областям.\n\n";
  msg += "<b>Сейчас доступна 1 область из 7</b> (прототип). 5 вопросов, 2 минуты.\n\n";
  msg += "Без рекомендаций. только точный замер. Решения разбираем на разборе вживую.";
  
  tgSend(cid, msg, {
    parse_mode: "HTML",
    reply_markup: JSON.stringify({inline_keyboard: [
      [{text:"▶ Начать", callback_data:"diag_a_0_0_-1"}]
    ]})
  });
}

function _processDiagnosticAnswer(cid, name, areaIdx, questionIdx, score){
  var cache = CacheService.getScriptCache();
  var stateStr = cache.get("diag_state_"+cid);
  var state;
  
  if(!stateStr){
    // Состояние истекло. начинаем заново
    _startBotDiagnostic(cid, name);
    return;
  }
  
  try{ state = JSON.parse(stateStr); }catch(e){ _startBotDiagnostic(cid, name); return; }
  
  var areas = _getDiagnosticAreas();
  var area = areas[areaIdx];
  
  // Если score >= 0. сохраняем ответ
  if(score >= 0){
    if(!state.answers[area.id]) state.answers[area.id] = [];
    state.answers[area.id][questionIdx] = score;
  }
  
  // Следующий вопрос?
  var nextQ = (score >= 0) ? questionIdx + 1 : 0;
  
  if(nextQ < area.questions.length){
    // Показываем следующий вопрос
    state.questionIdx = nextQ;
    cache.put("diag_state_"+cid, JSON.stringify(state), 3600);
    
    var q = area.questions[nextQ];
    var msg = area.icon + " <b>" + area.organ + " · " + area.label + "</b>\n";
    msg += "Вопрос " + (nextQ + 1) + " из " + area.questions.length + "\n\n";
    msg += "<b>" + q.q + "</b>";
    
    tgSend(cid, msg, {
      parse_mode: "HTML",
      reply_markup: JSON.stringify({inline_keyboard: [
        [{text:"✓ " + q.a[0], callback_data:"diag_a_"+areaIdx+"_"+nextQ+"_3"}],
        [{text:"! " + q.a[1], callback_data:"diag_a_"+areaIdx+"_"+nextQ+"_1"}],
        [{text:"× " + q.a[2], callback_data:"diag_a_"+areaIdx+"_"+nextQ+"_0"}]
      ]})
    });
  } else {
    // Закончили область. финал (прототип на 1 области)
    _finishBotDiagnostic(cid, name, state);
  }
}

function _finishBotDiagnostic(cid, name, state){
  var areas = _getDiagnosticAreas();
  var results = [];
  var totalScore = 0;
  var totalMax = 0;
  var weakItems = [];
  
  areas.forEach(function(area){
    var answers = state.answers[area.id] || [];
    var maxScore = area.questions.length * 3;
    var actual = 0;
    for(var i=0;i<answers.length;i++){
      var sc = answers[i] || 0;
      actual += sc;
      if(sc === 0){
        weakItems.push({area:area.organ+" ("+area.label+")", question:area.questions[i].q});
      }
    }
    var pct = Math.round((actual / maxScore) * 100);
    results.push({organ:area.organ, label:area.label, icon:area.icon, pct:pct, score:actual, maxScore:maxScore, answers:answers});
    totalScore += actual;
    totalMax += maxScore;
  });
  
  var totalPct = Math.round((totalScore / totalMax) * 100);
  var totalLabel = totalPct >= 70 ? "🟢 Здоровый бизнес" : (totalPct >= 40 ? "🟡 Есть слабые места" : "🔴 Требует внимания");
  
  var sorted = results.slice().sort(function(a,b){ return a.pct - b.pct; });
  var weakest = sorted[0];
  var strongest = sorted[sorted.length-1];
  
  // Сохраняем в лист
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("Диагностика");
    if(!ws){
      ws = ss.insertSheet("Диагностика", ss.getNumSheets());
      var hdrs = ["Дата","Chat ID","Имя","Telegram","Общий %","Слабый орган","Сильный орган","Ответы JSON","Источник","Рекомендации AI"];
      ws.getRange(1,1,1,hdrs.length).setValues([hdrs]);
      ws.getRange(1,1,1,hdrs.length).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
      ws.setFrozenRows(1);
    }
    ws.appendRow([
      new Date(), cid, name, "",
      totalPct + "%",
      weakest.organ + " " + weakest.pct + "%",
      strongest.organ + " " + strongest.pct + "%",
      JSON.stringify(state.answers),
      "Telegram бот", ""
    ]);
  }catch(e){ Logger.log("Diag save: " + e); }
  
  // Уведомление админам (короткое)
  var adminMsg = "🔬 <b>Новая диагностика (бот)</b>\n\n";
  adminMsg += "👤 " + name + " 🆔 " + cid + "\n";
  adminMsg += "📊 Общий: <b>" + totalPct + "%</b> " + totalLabel + "\n\n";
  results.forEach(function(r){
    var emoji = r.pct >= 70 ? "🟢" : (r.pct >= 40 ? "🟡" : "🔴");
    adminMsg += emoji + " " + r.icon + " " + r.organ + ": " + r.pct + "%\n";
  });
  adminMsg += "\n⏳ Генерирую AI-рекомендации...";
  tgSendAdmins(adminMsg, "subscribers");
  
  // AI рекомендации
  try{
    _generateDiagnosticRecommendations(cid, name, results, weakItems);
  }catch(recE){
    Logger.log("AI: "+recE);
  }
  
  // Сообщение клиенту (краткое, БЕЗ рекомендаций)
  var clientMsg = "✅ <b>Диагностика завершена</b>\n\n";
  clientMsg += "📊 Общий индекс: <b>" + totalPct + "%</b>\n" + totalLabel + "\n\n";
  clientMsg += "<b>По 7 органам бизнеса:</b>\n";
  results.forEach(function(r){
    var emoji = r.pct >= 70 ? "🟢" : (r.pct >= 40 ? "🟡" : "🔴");
    clientMsg += emoji + " " + r.icon + " " + r.organ + " " + r.label + " <b>" + r.pct + "%</b>\n";
  });
  clientMsg += "\n⚠ <b>Слабее всего:</b> " + weakest.icon + " " + weakest.organ + " (" + weakest.pct + "%)\n\n";
  clientMsg += "На разборе разберём слабые места и составим план. Открой Mini App чтобы увидеть полную визуализацию.";
  
  tgSend(cid, clientMsg, {
    parse_mode: "HTML",
    reply_markup: JSON.stringify({inline_keyboard: [
      [{text:"📱 Открыть результат в Mini App", web_app:{url:getWebAppUrl("diagnostic")}}],
      [{text:"📞 Записаться на разбор", callback_data:"sub_diagnostic_request"}]
    ]})
  });
  
  CacheService.getScriptCache().remove("diag_state_"+cid);
}

function _resetDiagnosticState(cid){
  CacheService.getScriptCache().remove("diag_state_"+cid);
}

// Action для Mini App
function _miniSaveDiagnostic(p){
  try{
    var chatId = String(p.chatId||"").trim();
    var answersStr = String(p.answers||"{}");
    var userName = String(p.userName||"") || String(p.name||"");
    var userTg = String(p.userTg||"");
    
    var answers;
    try{ answers = JSON.parse(answersStr); }catch(e){ answers = {}; }
    
    // 7 областей. должны совпадать с DIAGNOSTIC_AREAS в Mini App
    var areas = _getDiagnosticAreasFull();
    var results = [];
    var totalScore = 0;
    var totalMax = 0;
    var weakItems = []; // конкретные слабые ответы (для AI)
    
    areas.forEach(function(area){
      var aArr = answers[area.id] || [];
      var maxScore = area.questions.length * 3;
      var actual = 0;
      for(var i=0;i<aArr.length;i++){
        var sc = aArr[i] || 0;
        actual += sc;
        if(sc === 0){
          weakItems.push({area:area.organ+" ("+area.label+")", question:area.questions[i].q});
        }
      }
      var pct = maxScore > 0 ? Math.round((actual / maxScore) * 100) : 0;
      results.push({organ:area.organ, label:area.label, icon:area.icon, pct:pct, score:actual, maxScore:maxScore, answers:aArr});
      totalScore += actual;
      totalMax += maxScore;
    });
    
    var totalPct = totalMax > 0 ? Math.round((totalScore / totalMax) * 100) : 0;
    var totalLabel = totalPct >= 70 ? "🟢 Здоровый бизнес" : (totalPct >= 40 ? "🟡 Есть слабые места" : "🔴 Требует внимания");
    
    // Сохраняем в лист
    var sortedByPct = results.slice().sort(function(a,b){return a.pct - b.pct;});
    var weakest = sortedByPct[0];
    var strongest = sortedByPct[sortedByPct.length-1];
    
    try{
      var ss = SpreadsheetApp.openById(SS_ID);
      var ws = ss.getSheetByName("Диагностика");
      if(!ws){
        ws = ss.insertSheet("Диагностика", ss.getNumSheets());
        var hdrs = ["Дата","Chat ID","Имя","Telegram","Общий %","Слабый орган","Сильный орган","Ответы JSON","Источник","Рекомендации AI"];
        ws.getRange(1,1,1,hdrs.length).setValues([hdrs]);
        ws.getRange(1,1,1,hdrs.length).setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontWeight("bold");
        ws.setFrozenRows(1);
      }
      ws.appendRow([
        new Date(), chatId, userName, userTg ? "@"+userTg : "",
        totalPct+"%",
        weakest.organ+" "+weakest.pct+"%",
        strongest.organ+" "+strongest.pct+"%",
        answersStr, "Mini App", ""
      ]);
    }catch(saveE){ Logger.log("Save diag: "+saveE); }
    
    // Уведомление админам с подробной разбивкой
    var adminMsg = "🔬 <b>Новая диагностика бизнеса</b>\n\n";
    adminMsg += "👤 " + (userName || "Без имени");
    if(userTg) adminMsg += " @" + userTg;
    adminMsg += "\n🆔 " + chatId + "\n";
    adminMsg += "📊 Общий: <b>" + totalPct + "%</b> " + totalLabel + "\n\n";
    adminMsg += "<b>По 7 областям:</b>\n";
    results.forEach(function(r){
      var emoji = r.pct >= 70 ? "🟢" : (r.pct >= 40 ? "🟡" : "🔴");
      adminMsg += emoji + " " + r.icon + " <b>" + r.organ + "</b> ("+r.label+"): " + r.pct + "%\n";
    });
    adminMsg += "\n<b>Слабое:</b> " + weakest.organ + " " + weakest.pct + "%\n";
    adminMsg += "<b>Сильное:</b> " + strongest.organ + " " + strongest.pct + "%\n\n";
    adminMsg += "⏳ Генерирую AI-рекомендации...";
    
    tgSendAdmins(adminMsg, "subscribers");
    
    // AI рекомендации (асинхронно, без задержки клиенту)
    try{
      _generateDiagnosticRecommendations(chatId, userName, results, weakItems);
    }catch(recE){
      Logger.log("AI recommendations: "+recE);
    }
    
    return {ok:true, totalPct:totalPct};
  }catch(e){
    Logger.log("_miniSaveDiagnostic: "+e);
    return {error:e.toString()};
  }
}

function _getDiagnosticAreasFull(){
  // 7 областей с вопросами. синхронизировано с Mini App
  return [
    {id:"brain", organ:"Мозг", label:"Стратегия", icon:"🧠", questions:[
      {q:"Письменный план развития на 12 мес с конкретными цифрами"},
      {q:"Стратегические сессии раз в квартал минимум"},
      {q:"Понимает на чём именно зарабатывает бизнес"},
      {q:"Понимает точку роста на 3 месяца вперёд"},
      {q:"Понимает ради чего лично строит бизнес"}
    ]},
    {id:"heart", organ:"Сердце", label:"Маркетинг", icon:"🫀", questions:[
      {q:"Прописан портрет идеального клиента"},
      {q:"Считает CAC и LTV"},
      {q:"3+ работающих канала привлечения"},
      {q:"Ведёт личный бренд в соцсетях"},
      {q:"Есть воронка прогрева"}
    ]},
    {id:"hands", organ:"Руки и ноги", label:"Продажи", icon:"💪", questions:[
      {q:"CRM-система"},
      {q:"Знает конверсии воронки"},
      {q:"Скрипты продаж и обучение менеджеров"},
      {q:"Есть РОП"},
      {q:"Скорость ответа менее 5 минут"}
    ]},
    {id:"spine", organ:"Костяк", label:"Команда", icon:"🦴", questions:[
      {q:"Прописана оргструктура"},
      {q:"KPI и регламенты у сотрудников"},
      {q:"Бизнес работает без собственника"},
      {q:"Есть система найма"},
      {q:"Лучшие сотрудники получают выше рынка"}
    ]},
    {id:"blood", organ:"Кровь", label:"Финансы", icon:"🩸", questions:[
      {q:"Ведёт ОПиУ ежемесячно"},
      {q:"Ведёт ДДС с прогнозом"},
      {q:"Знает маржу по каждому продукту"},
      {q:"Отделены личные деньги от бизнеса"},
      {q:"Знает точку безубыточности"}
    ]},
    {id:"dna", organ:"ДНК", label:"Процессы", icon:"🧬", questions:[
      {q:"SOP на ключевые процессы"},
      {q:"Автоматизация рутинных задач"},
      {q:"Меньше 2 часов в день на операционку"},
      {q:"Регулярные улучшения процессов"},
      {q:"Есть опер директор/COO"}
    ]},
    {id:"eyes", organ:"Зрение", label:"Аналитика", icon:"👁", questions:[
      {q:"Есть дашборд с метриками"},
      {q:"Знает выручку/прибыль/чек без проверки"},
      {q:"Решения опираются на цифры"},
      {q:"Видит воронку в одном месте"},
      {q:"Регулярные разборы результатов с командой"}
    ]}
  ];
}

function _generateDiagnosticRecommendations(chatId, userName, results, weakItems){
  // Генерирует AI-рекомендации админу через Anthropic
  var apiKey = PropertiesService.getScriptProperties().getProperty("ANTHROPIC_API_KEY");
  if(!apiKey) apiKey = ANTHROPIC_API_KEY;
  if(!apiKey || apiKey === "вставьте_сюда"){
    bsSysNote("⚠️ Не могу сгенерировать AI-рекомендации: ANTHROPIC_API_KEY не установлен", "subscribers");
    return;
  }
  
  // Формируем компактный профиль результатов
  var summary = "Клиент: " + (userName || "Без имени") + ".\n\n";
  summary += "Здоровье по 7 областям бизнеса:\n";
  results.forEach(function(r){
    summary += "- " + r.organ + " (" + r.label + "): " + r.pct + "%\n";
  });
  
  if(weakItems.length > 0){
    summary += "\nКонкретные слабые места:\n";
    weakItems.forEach(function(w, i){
      summary += (i+1) + ". " + w.area + ": " + w.question + "\n";
    });
  }
  
  var prompt = "Ты эксперт по бизнес-трекингу из Business Surgery (Алматы). Только что клиент прошёл диагностику бизнеса. ";
  prompt += "Твоя задача дать <b>рекомендации Рустаму и Береке</b> (основателям BS) на что обратить внимание при разборе с этим клиентом.\n\n";
  prompt += summary + "\n";
  prompt += "Напиши краткий разбор (250-400 слов) в формате:\n";
  prompt += "1. Главная боль (1-2 предложения)\n";
  prompt += "2. Что разобрать в первую очередь на встрече (3-4 пункта по 1 строке)\n";
  prompt += "3. Какие инструменты из практики BS подойдут (упомяни 1-2 конкретных, например ОПиУ, ДДС, CAC/LTV, SOP, оргструктуру)\n";
  prompt += "4. Сценарий разбора (как структурировать разговор)\n\n";
  prompt += "Стиль письма Business Surgery: короткие предложения, конкретно, простой язык, без AI-фраз. ";
  prompt += "ЗАПРЕЩЕНО использовать тире ( ), фразы 'погрузиться', 'раскрыть', 'давайте', 'прокачать', 'гарантированно', 'секреты успеха', 'хирург', 'операция'. ";
  prompt += "Разрешено начинать с 'И' и 'Но'. Можно использовать форматирование Telegram HTML (<b>, <i>).";
  
  var resp = UrlFetchApp.fetch("https://api.anthropic.com/v1/messages", {
    method: "post",
    contentType: "application/json",
    headers: {"x-api-key": apiKey, "anthropic-version": "2023-06-01"},
    payload: JSON.stringify({
      model: "claude-sonnet-4-5",
      max_tokens: 2000,
      messages: [{role:"user", content:prompt}]
    }),
    muteHttpExceptions: true
  });
  
  var data;
  try{ data = JSON.parse(resp.getContentText()); }catch(e){
    bsSysNote("⚠️ AI рекомендации недоступны: "+e.toString().substring(0,200), "subscribers");
    return;
  }
  
  if(data.error){
    bsSysNote("⚠️ Anthropic API: "+(data.error.message||"").substring(0,200), "subscribers");
    return;
  }
  
  var aiText = "";
  if(data.content && data.content[0] && data.content[0].text){
    aiText = data.content[0].text;
  }
  
  if(!aiText){
    bsSysNote("⚠️ AI вернул пустой ответ", "subscribers");
    return;
  }
  
  // Очищаем от тире на всякий случай
  aiText = aiText.replace(/\s \s/g, '. ').replace(/ /g, ' ');
  
  // Шлём в Telegram админам
  var msg = "🤖 <b>AI рекомендации к разбору</b>\n";
  msg += "Клиент: " + (userName || "Без имени") + " 🆔" + chatId + "\n\n";
  msg += aiText;
  
  // Telegram лимит 4096
  if(msg.length > 4000){
    msg = msg.substring(0, 4000) + "\n\n[обрезано]";
  }
  
  bsSysNote(msg, "subscribers");
  
  // Сохраним в лист
  try{
    var ss = SpreadsheetApp.openById(SS_ID);
    var ws = ss.getSheetByName("Диагностика");
    if(ws){
      var lr = ws.getLastRow();
      ws.getRange(lr, 10).setValue(aiText);
    }
  }catch(e){}
}

function bsEvery5Min(){
  if(bsIsCopy()) return;
  // Защита от наложения запусков: если предыдущий ещё идёт. пропускаем
  var lock = LockService.getScriptLock();
  if(!lock.tryLock(1000)) return;
  try{ _bsEvery5MinBody(); }
  catch(e){ Logger.log("every5min: "+e); }
  finally{ try{lock.releaseLock();}catch(e){} }
}

function _bsDailyCleanup(){
  // Раз в сутки убираем прошедшие встречи
  try{ bsCleanSchedule(); }catch(e){ Logger.log("cleanup: "+e); }
}

function _bsEvery5MinBody(){
  if(bsIsCopy()) return;
  // Раз в час: бот должен идти через сервер. Первый раз — в течение 5 минут после установки
  try{
    var _c5 = CacheService.getScriptCache();
    if(!_c5.get("autoSrv")){ _c5.put("autoSrv", "1", 55*60); bsAutoServer(); }
  }catch(e){ Logger.log("autoServer: "+e); }
  var _master = bsServerIsMaster();   // данные клуба ведёт сервер: таблицу не пересчитываем
  if(!_master){
  try{ bsGuardDebet(); }catch(e){ Logger.log("guard: "+e); }
  try{ bsApplyPaymentsFromDDS(); }catch(e){ Logger.log("ddsPay: "+e); }
  }
  // Диспетчер частых задач. Один триггер вместо шести:
  // Apps Script разрешает максимум 20 триггеров на скрипт

  // Ссылка синей кнопки обновляется каждые 5 минут: Telegram не может показать
  // закешированную сборку, потому что адрес каждый раз новый
  try{ updateMenuButton(); }catch(e){ Logger.log("d.menuBtn: "+e); }
  if(!_master){ try{ deferredRecalcPL(); }catch(e){ Logger.log("d.recalcPL: "+e); } }
  try{ warmupBundleCache(); }catch(e){ Logger.log("d.warmup: "+e); }
  try{ sendScheduledQualQuestions(); }catch(e){ Logger.log("d.qual: "+e); }

  try{ bsCalRepairDaily(); }catch(e){ Logger.log("d.calRepair: "+e); }

  var min = new Date().getMinutes();
  if(min % 10 < 5){
    try{ refreshBotCache(); }catch(e){ Logger.log("d.botCache: "+e); }
    try{ sendOnboardingMessages(); }catch(e){ Logger.log("d.onboard: "+e); }
  }
  if(min % 15 < 5){
    try{ publishToChannel(); }catch(e){ Logger.log("d.publish: "+e); }
  }
  if(min % 30 < 5){
    try{ checkMeetingsCompleted(); }catch(e){ Logger.log("d.meetings: "+e); }
    try{ checkScheduleReminders(); }catch(e){ Logger.log("d.schedRem: "+e); }
  }
}

function bsHourly(){
  if(bsIsCopy()) return;
  // Новая версия скрипта с сервера (быстро, если обновлять нечего)
  try{ bsSelfUpdate(); }catch(e){ Logger.log("h.selfUpdate: "+e); }
  // Диспетчер часовых задач
  if(bsServerIsMaster()){
    // Данные клуба ведёт сервер: таблица только получает копию ДДС и PL
    try{ bsPullFromServer(); }catch(e){ Logger.log("h.pull: "+e); }
  } else {
  try{ bsPurgeFines(true); }catch(e){ Logger.log("h.purge: "+e); }
  try{ bsPushResidentsToPlatform(true); }catch(e){ Logger.log("h.platform: "+e); }
  try{ bsClubImportHourly(); }catch(e){ Logger.log("h.clubImport: "+e); }
  try{ bsWatchDDS(); }catch(e){ Logger.log("h.dds: "+e); }
  }
  try{ bsWatchBot(); }catch(e){ Logger.log("h.watchbot: "+e); }
  try{ sendMeetingReminders(); }catch(e){ Logger.log("h.meetRem: "+e); }
  try{ processSubscriberFollowups(); }catch(e){ Logger.log("h.followups: "+e); }
  try{ updateAnalytics(); }catch(e){ Logger.log("h.analytics: "+e); }

  var h = parseInt(Utilities.formatDate(new Date(), "Asia/Almaty", "H"));
  if(h === 0 || h === 12){
    try{ checkContentQueue(); }catch(e){ Logger.log("h.contentQ: "+e); }
  }
}

function bsDaily(){
  if(bsIsCopy()) return;
  // Диспетчер ежедневных задач. Запускается в 14:30 по Алматы.
  // Почему не утром: отчёт, присланный до 14:00, засчитывается за вчерашний день.
  // При проверке в 10:00 резидент получал штраф, хотя ещё мог дослать отчёт
  var _master = bsServerIsMaster();
  if(!_master){ try{ dailyCheck(); }catch(e){ Logger.log("day.check: "+e); } }
  try{ publishScheduledSmm(); }catch(e){ Logger.log("day.smm: "+e); }
  if(!_master){
  try{ recalcVisitMonths(); }catch(e){ Logger.log("day.visits: "+e); }
  try{ autoRecalcCycles(); }catch(e){ Logger.log("day.cycles: "+e); }
  }
  try{ crmFollowups(); }catch(e){ Logger.log("day.crm: "+e); }
  try{ sendWarmupMessages(); }catch(e){ Logger.log("day.warmup: "+e); }

  var day = new Date().getDay(); // 0 вс, 1 пн, 2 вт, 4 чт
  if(day === 1){
    try{ sendChecklistToUseful(); }catch(e){ Logger.log("day.checklist: "+e); }
    if(!_master){
    try{ checkRenewals(); }catch(e){ Logger.log("day.renewals: "+e); }
    try{ checkMeetingBalance(); }catch(e){ Logger.log("day.balance: "+e); }
    }
  }
  if(day === 2){
    try{ generateNewRecommendations(); }catch(e){ Logger.log("day.recs: "+e); }
  }
  if(day === 4){
    try{ sendNPS(); }catch(e){ Logger.log("day.nps: "+e); }
  }
  if(day === 0){
    try{ weeklyBackup(); }catch(e){ Logger.log("day.backup: "+e); }
  }
  // Контент в Полезное. через день
  var dayOfYear = Math.floor((new Date() - new Date(new Date().getFullYear(), 0, 0)) / 86400000);
  if(dayOfYear % 2 === 0){
    try{ sendScheduledContent(); }catch(e){ Logger.log("day.content: "+e); }
  }
}

function forceDeleteAllTriggers(){
  // Аварийная очистка: удаляет ВСЕ триггеры проекта.
  // Нужна если упёрлись в лимит и setupAllTriggers не может создать новые
  var all = ScriptApp.getProjectTriggers();
  var n = all.length;
  all.forEach(function(t){
    try{ ScriptApp.deleteTrigger(t); }catch(e){}
  });
  var msg = "🧹 Удалено триггеров: " + n + "\n\nТеперь запустите: Установить ВСЕ триггеры";
  try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
  Logger.log(msg);
  return msg;
}

function setupAllTriggers(){
  // Apps Script разрешает максимум 20 триггеров на скрипт. Раньше создавалось 22
  // и установка падала с ошибкой. Теперь всё работает через 5 диспетчеров
  try{ cleanupAllData(true); }catch(e){ Logger.log("cleanup on setup: "+e); }

  // Удаляем ВСЕ триггеры проекта без фильтра. дубли не выживут
  var existing = ScriptApp.getProjectTriggers();
  existing.forEach(function(t){
    try{ ScriptApp.deleteTrigger(t); }catch(e){ Logger.log("del trigger: "+e); }
  });
  Logger.log("Удалено триггеров: " + existing.length);

  // ── 5 триггеров вместо 22 ──
  // 1. Правки в таблице
  ScriptApp.newTrigger("onTableEdit").forSpreadsheet(SS_ID).onEdit().create();
  // 2. Каждые 5 минут: пересчёт PL, прогрев кеша, квал-вопросы, кеш бота, онбординг, публикации, встречи
  ScriptApp.newTrigger("bsEvery5Min").timeBased().everyMinutes(5).create();
  // 3. Каждый час: напоминания о встречах, дожим подписчиков, аналитика, очередь Полезного
  ScriptApp.newTrigger("bsHourly").timeBased().everyHours(1).create();
  // 4. Ежедневно в 10:00: проверка отчётов, SMM, посещения, недельные задачи по дням
  ScriptApp.newTrigger("bsDaily").timeBased().atHour(14).nearMinute(30).everyDays(1).inTimezone("Asia/Almaty").create();
  // 5. Вечернее напоминание об отчёте в 22:00
  ScriptApp.newTrigger("eveningReminder").timeBased().atHour(22).nearMinute(0).everyDays(1).inTimezone("Asia/Almaty").create();

  // Обновляем кнопку меню бота на свежую версию приложения
  try{ updateMenuButton(); }catch(e){ Logger.log("menu btn: "+e); }

  var allTriggers = ScriptApp.getProjectTriggers();
  var summary = "✅ Система настроена\n\n" +
    "Триггеров активно: " + allTriggers.length + " из 20 разрешённых\n" +
    "Данные проверены, дубли убраны\n" +
    "Кеш приложения прогревается каждые 5 минут\n" +
    "Кнопка приложения обновлена";
  try{ SpreadsheetApp.getUi().alert(summary); }catch(e){}
  ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, summary); }catch(e){} });
}

function deferredRecalcPL(){
  // Триггер каждую минуту. пересчитывает PL только если был флаг
  var props=PropertiesService.getScriptProperties();
  if(props.getProperty("pl_needs_recalc")==="1"){
    props.deleteProperty("pl_needs_recalc");
    try{recalcPL();Logger.log("Deferred recalcPL executed");}catch(e){Logger.log("deferredRecalcPL err: "+e);}
  }
}

function setupDeferredRecalc(){
  // Устанавливает триггер deferredRecalcPL каждую минуту
  var triggers=ScriptApp.getProjectTriggers();
  triggers.forEach(function(t){
    if(t.getHandlerFunction()==="deferredRecalcPL")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("deferredRecalcPL").timeBased().everyMinutes(1).create();
  SpreadsheetApp.getUi().alert("✅ Триггер автопересчёта PL установлен (каждую минуту)");
}

function watchdogWebhook(){
  // ОТКЛЮЧЕН. этот watchdog сам ломал webhook через ScriptApp.getService().getUrl()
  // который возвращал URL архивированных deployments.
  // Webhook теперь ставится только ВРУЧНУЮ через setWebhook ссылку.
  return;
}


function _reinstallWebhookKeepPending(){
  // Переустановка webhook на ЗАХАРДКОЖЕННЫЙ URL (WEBHOOK_URL константа в начале файла)
  // НЕ используем ScriptApp.getService().getUrl(). он возвращает старый URL
  try{
    UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/deleteWebhook",{muteHttpExceptions:true});
    Utilities.sleep(500);
    var params={
      url:WEBHOOK_URL,
      secret_token:"bs2026secret",
      allowed_updates:["message","edited_message","callback_query","message_reaction"]
    };
    UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/setWebhook",{
      method:"post",
      contentType:"application/json",
      payload:JSON.stringify(params),
      muteHttpExceptions:true
    });
    Logger.log("Webhook → "+WEBHOOK_URL);
  }catch(e){Logger.log("reinstall err: "+e);}
}

function _reinstallWebhook(){
  try{
    // Сначала удаляем
    UrlFetchApp.fetch(
      "https://api.telegram.org/bot"+BOT_TOKEN+"/deleteWebhook?drop_pending_updates=true",
      {muteHttpExceptions:true}
    );
    Utilities.sleep(1000);
    // Устанавливаем заново с secret_token
    var resp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/setWebhook",{
      method:"post",
      contentType:"application/json",
      payload:JSON.stringify({
        url:WEBHOOK_URL,
        allowed_updates:["message","edited_message","callback_query","message_reaction"],
        drop_pending_updates:false,
        secret_token:"bs2026secret"
      }),
      muteHttpExceptions:true
    });
    var res=JSON.parse(resp.getContentText());
    Logger.log("Webhook восстановлен: "+res.ok);
    // Уведомляем админа (тихо. только лог)
  }catch(e){
    Logger.log("_reinstallWebhook error: "+e);
  }
}

function showAppVersionCheck(){
  var res = checkAppVersionOnServer();
  try{ SpreadsheetApp.getUi().alert(res); }catch(e){}
  ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, res); }catch(e){} });
  return res;
}

function diagnoseAppRouting(){
  // ГЛУБОКАЯ ДИАГНОСТИКА: куда ведут все кнопки приложения и что там лежит
  var out = ["🔎 Куда ведут кнопки приложения", ""];

  // 1. Кнопка меню бота (setChatMenuButton)
  try{
    var r = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getChatMenuButton", {muteHttpExceptions:true});
    var d = JSON.parse(r.getContentText());
    if(d.ok && d.result){
      var mb = d.result;
      out.push("КНОПКА МЕНЮ БОТА");
      out.push("  Тип: " + (mb.type || "?"));
      out.push("  Название: " + (mb.text || "по умолчанию"));
      if(mb.web_app && mb.web_app.url){
        out.push("  Адрес: " + mb.web_app.url);
      } else {
        out.push("  ⚠️ Адрес не задан через API.");
        out.push("  Значит кнопка настроена в BotFather вручную,");
        out.push("  и скрипт её адрес поменять не может.");
      }
    }
  }catch(e){ out.push("Кнопка меню: ошибка " + e); }
  out.push("");

  // 2. Проверяем все возможные адреса приложения
  var paths = ["", "index.html", "app.html", "bs_miniapp.html", "miniapp.html", "bs.html", "bsapp.html"];
  out.push("ЧТО ЛЕЖИТ ПО АДРЕСАМ");
  paths.forEach(function(p){
    var url = WEBAPP_BASE_URL + p + "?nc=" + Date.now();
    try{
      var resp = UrlFetchApp.fetch(url, {muteHttpExceptions:true, followRedirects:true});
      var codeH = resp.getResponseCode();
      if(codeH !== 200){
        out.push("  " + (p || "/") + " → нет файла (" + codeH + ")");
        return;
      }
      var txt = resp.getContentText();
      var isLoader = txt.indexOf("app.html?v=") >= 0 && txt.length < 8000;
      var vm = txt.match(/APP_VERSION\s*=\s*"([^"]+)"/);
      if(isLoader){
        out.push("  " + (p || "/") + " → загрузчик ✅");
      } else if(vm){
        out.push("  " + (p || "/") + " → приложение, версия " + vm[1]);
      } else {
        out.push("  " + (p || "/") + " → ⚠️ СТАРЫЙ файл без версии (" + Math.round(txt.length/1024) + " КБ)");
      }
    }catch(e){
      out.push("  " + (p || "/") + " → ошибка");
    }
  });

  out.push("");
  out.push("ЧТО ДОЛЖНО БЫТЬ");
  out.push("  / и index.html → загрузчик");
  out.push("  app.html → приложение со свежей версией");
  out.push("  bs_miniapp.html → загрузчик (для старых ссылок)");
  out.push("");
  out.push("━━━━━━━━━━━━━━━");
  out.push("СИНЯЯ КНОПКА В СПИСКЕ ЧАТОВ");
  out.push("");
  out.push("Это отдельная настройка Telegram: Main Mini App.");
  out.push("Её адрес НЕ виден через API и скриптом не меняется.");
  out.push("Задаётся только вручную:");
  out.push("");
  out.push("1. Открыть @BotFather");
  out.push("2. /mybots → выбрать своего бота");
  out.push("3. Bot Settings → Configure Mini App");
  out.push("4. Посмотреть текущий адрес");
  out.push("5. Если он отличается от");
  out.push("   " + WEBAPP_BASE_URL);
  out.push("   нажать Edit Mini App URL и вписать этот адрес");
  out.push("");
  out.push("После этого синяя кнопка будет открывать свежую версию.");

  var msg = out.join("\n");
  Logger.log(msg);
  ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid, msg); }catch(e){} });
  return msg;
}

function checkAppVersionOnServer(){
  // Диагностика обновления: что реально лежит на GitHub Pages прямо сейчас
  try{
    var url = WEBAPP_BASE_URL + "app.html?nocache=" + Date.now();
    var resp = UrlFetchApp.fetch(url, {muteHttpExceptions: true, headers: {"Cache-Control": "no-cache"}});
    var codeHttp = resp.getResponseCode();
    if(codeHttp !== 200){
      return "❌ Приложение недоступно. Код ответа: " + codeHttp + "\n\nПроверьте что файл залит в репозиторий bs-app";
    }
    var txt = resp.getContentText();
    var vm = txt.match(/APP_VERSION\s*=\s*"([^"]+)"/);
    var version = vm ? vm[1] : "не найдена";

    // Проверяем наличие свежих блоков
    var marks = [
      ["Чипы месяцев в Сводке", txt.indexOf("pl-mchip") >= 0],
      ["Вкладка SMM", txt.indexOf("label:'SMM'") >= 0],
      ["Вкладка Полезное", txt.indexOf("label:'Полезное'") >= 0],
      ["Правила клуба", txt.indexOf("rule-block") >= 0],
      ["Календарь с заливкой", txt.indexOf("cal-fill-offline") >= 0],
      ["Цена нерешённого", txt.indexOf("problemsCard") >= 0],
      ["Самообновление", txt.indexOf("bsCheckVersion") >= 0],
      ["Аватар для Stories", txt.indexOf("cache.myAvatar") >= 0],
      ["Кнопка обновления", txt.indexOf("hardReload") >= 0]
    ];
    var lines = ["📱 Что сейчас на GitHub", "", "Версия сборки: " + version, "Размер файла: " + Math.round(txt.length/1024) + " КБ", ""];
    marks.forEach(function(mk){
      lines.push((mk[1] ? "✅ " : "❌ ") + mk[0]);
    });
    var missing = marks.filter(function(mk){ return !mk[1]; }).length;
    lines.push("");
    if(missing === 0){
      lines.push("✅ На GitHub свежая сборка.");
      lines.push("");
      lines.push("Если в приложении всё ещё старый вид:");
      lines.push("откройте вкладку Правила клуба и нажмите кнопку Обновить приложение внизу.");
      lines.push("Это нужно один раз, дальше обновления приходят сами.");
    } else {
      lines.push("⚠️ На GitHub старый файл: не хватает " + missing + " блоков.");
      lines.push("Залейте bs_miniapp.html заново и подождите 2 минуты.");
    }
    return lines.join("\n");
  }catch(e){
    return "❌ Ошибка проверки: " + e;
  }
}

function updateMenuButton(){
  // Синяя кнопка в списке чатов ведёт на фиксированный URL и показывала старую версию.
  // Обновляем её при каждой установке триггеров, чтобы открывалась свежая сборка
  try{
    CacheService.getScriptCache().remove("miniapp_url_base");
    var url = getWebAppUrl();
    // Название кнопки не трогаем: берём то, что задано в BotFather
    var currentText = "Приложение";
    try{
      var cur = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getChatMenuButton", {muteHttpExceptions: true});
      var curData = JSON.parse(cur.getContentText());
      if(curData.ok && curData.result && curData.result.text) currentText = curData.result.text;
    }catch(e){}
    var resp = UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/setChatMenuButton", {
      method: "post",
      contentType: "application/json",
      payload: JSON.stringify({
        menu_button: {
          type: "web_app",
          text: currentText,
          web_app: {url: url}
        }
      }),
      muteHttpExceptions: true
    });
    Logger.log("updateMenuButton: " + resp.getContentText().substring(0, 200));
    return url;
  }catch(e){
    Logger.log("updateMenuButton: " + e);
    return "";
  }
}

function setupWebhook(){
  var token="https://api.telegram.org/bot"+BOT_TOKEN+"/";
  // Удаляем ВСЕ накопленные обновления
  UrlFetchApp.fetch(token+"deleteWebhook?drop_pending_updates=true",{muteHttpExceptions:true});
  Utilities.sleep(1000);
  // Очищаем кэш дедупликации
  try{CacheService.getScriptCache().removeAll(["upd_cache"]);}catch(ex){}
  var resp=UrlFetchApp.fetch(token+"setWebhook",{
    method:"post",contentType:"application/json",
    payload:JSON.stringify({url:WEBHOOK_URL,allowed_updates:["message","edited_message","callback_query","message_reaction"],drop_pending_updates:false,secret_token:"bs2026secret"}),
    muteHttpExceptions:true
  });
  var res=JSON.parse(resp.getContentText());
  if(res.ok)toast("✅ Webhook установлен!\n\nВАЖНО: при изменении кода всегда делайте:\nРазвернуть → Управление → карандаш → Версия: Новая → Сохранить\n\nИначе работает старый код!");
  else toast("❌ Ошибка:\n"+resp.getContentText());
}

function checkBotToken(){
  try{
    var r=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getMe",{muteHttpExceptions:true});
    var res=JSON.parse(r.getContentText());
    if(res.ok)toast("✅ Токен работает!\nБот: @"+res.result.username+"\n\nТеперь нажмите: 🤖 Webhook");
    else toast("❌ Токен не работает!\n"+r.getContentText().substring(0,150));
  }catch(e){toast("Ошибка: "+e.message);}
}

function checkWebhook(){
  var r=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/getWebhookInfo",{muteHttpExceptions:true});
  var info=JSON.parse(r.getContentText()).result;
  var match=(info.url===WEBHOOK_URL);
  toast((match?"✅ Совпадает!":"❌ НЕ совпадает. нажмите 🤖")+
    "\nWebhook: ..."+info.url.substring(50,90)+
    "\nОшибок: "+(info.last_error_message||"нет")+
    "\nПending: "+(info.pending_update_count||0));
}

// ═══════════════════════════════════════════════════════════════════════════
// МЕНЮ
// ═══════════════════════════════════════════════════════════════════════════


function buildContent(ss){
  // Не пересоздаём если уже есть посты
  var ws=ss.getSheetByName("Контент");
  if(ws&&ws.getLastRow()>3){
    Logger.log("Контент уже существует. не пересоздаём");
    return;
  }
  if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Контент", ss.getNumSheets());
  ws.setTabColor("#FF6D00");ws.setFrozenRows(2);
  ws.setRowHeight(1,40);
  TITMERGE(ws.getRange("A1:E1"),"\uD83D\uDCE2 КОНТЕНТ-ПЛАН. автопостинг в ПОЛЕЗНОЕ каждые 2 дня");
  ws.setRowHeight(2,36);
  var heads=["\u2116","Категория","Текст поста","Дата отправки","Статус"];
  var wids=[35,120,500,130,100];
  for(var i=0;i<5;i++){var c=ws.getRange(2,i+1);c.setValue(heads[i]);H(c);B(c);ws.setColumnWidth(i+1,wids[i]);}
  var dvSt=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Ожидает","Отправлен","Пропущен"],true).setAllowInvalid(false).build();
  // Начинаем с сегодня (первый пост уже отправлен вручную)
  var tomorrow=new Date();
  tomorrow.setHours(9,0,0,0);
  for(var i=0;i<CONTENT_POSTS.length;i++){
    var row=3+i;
    var postDate=new Date(tomorrow.getTime()+i*2*24*60*60*1000);
    ws.setRowHeight(row,80);
    ws.getRange(row,1).setValue(i+1).setHorizontalAlignment("center");
    ws.getRange(row,2).setValue(CONTENT_POSTS[i].cat).setHorizontalAlignment("center");
    ws.getRange(row,3).setValue(CONTENT_POSTS[i].text).setWrap(true).setVerticalAlignment("top");
    ws.getRange(row,4).setValue(postDate).setNumberFormat("DD.MM.YYYY").setHorizontalAlignment("center");
    ws.getRange(row,5).setValue("Ожидает").setDataValidation(dvSt).setHorizontalAlignment("center");
    B(ws.getRange(row,1,1,5));
  }
  var rules=[];
  rules.push(SpreadsheetApp.newConditionalFormatRule().whenTextEqualTo("Отправлен")
    .setBackground(GRN_BG).setFontColor(GRN).setRanges([ws.getRange("E3:E"+(3+CONTENT_POSTS.length))]).build());
  ws.setConditionalFormatRules(rules);
}


function setupAutoContentTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="checkContentQueue")ScriptApp.deleteTrigger(t);
  });
  // Проверка очереди контента 2 раза в день
  ScriptApp.newTrigger("checkContentQueue").timeBased().everyHours(12).create();
  // Прогрев кеша приложения каждые 5 минут: открытие всегда мгновенное
  ScriptApp.newTrigger("warmupBundleCache").timeBased().everyMinutes(5).create();
  // Автопубликация готовых постов SMM в канал (каждый день в 10:30)
  ScriptApp.newTrigger("publishScheduledSmm").timeBased().atHour(10).nearMinute(30).everyDays(1).inTimezone("Asia/Almaty").create();
  // Чек-лист в Полезное раз в 2 недели (по понедельникам, чётные недели проверяются внутри)
  ScriptApp.newTrigger("sendChecklistToUseful").timeBased().onWeekDay(ScriptApp.WeekDay.MONDAY).atHour(11).inTimezone("Asia/Almaty").create();
}

function setupContentTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="sendScheduledContent")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("sendScheduledContent").timeBased().atHour(9).nearMinute(0).everyDays(1).create();
}

function diagnoseUsefulContent(){
  // Диагностика системы Полезное: что не работает
  var ss=SpreadsheetApp.openById(SS_ID);
  var report=["🔍 *Диагностика Полезное*",""];
  
  // 1. Проверка триггеров
  var triggers=ScriptApp.getProjectTriggers();
  var trigSendCount=0, trigCheckCount=0;
  triggers.forEach(function(t){
    var fn=t.getHandlerFunction();
    if(fn==="sendScheduledContent")trigSendCount++;
    if(fn==="checkContentQueue")trigCheckCount++;
  });
  report.push("📋 Триггеры:");
  report.push("  • sendScheduledContent: "+(trigSendCount>0?"✅ "+trigSendCount:"❌ нет"));
  report.push("  • checkContentQueue: "+(trigCheckCount>0?"✅ "+trigCheckCount:"❌ нет"));
  report.push("");
  
  // 2. Проверка листа Контент
  var ws=ss.getSheetByName("Контент");
  if(!ws){
    report.push("📄 Лист Контент: ❌ НЕ НАЙДЕН");
  } else {
    var lr=ws.getLastRow();
    var waiting=0, sent=0, skipped=0, nextDate=null;
    for(var r=3;r<=lr;r++){
      var st=ws.getRange(r,5).getValue();
      var dt=ws.getRange(r,4).getValue();
      if(st==="Ожидает"){
        waiting++;
        if(!nextDate && dt instanceof Date) nextDate=dt;
      }
      else if(st==="Отправлен") sent++;
      else if(st==="Пропущен") skipped++;
    }
    report.push("📄 Лист Контент: ✅");
    report.push("  • Ожидает: "+waiting);
    report.push("  • Отправлено: "+sent);
    report.push("  • Пропущено: "+skipped);
    if(nextDate){
      report.push("  • Следующий пост: "+Utilities.formatDate(nextDate,"Asia/Almaty","dd.MM.yyyy HH:mm"));
    }
    if(waiting===0){
      report.push("  ⚠️ ПУСТАЯ ОЧЕРЕДЬ - нечего публиковать");
    }
  }
  report.push("");
  
  // 3. Gemini API key
  var hasGemini=typeof GEMINI_API_KEY!=="undefined" && GEMINI_API_KEY && GEMINI_API_KEY!=="вставьте_gemini_ключ";
  report.push("🤖 Gemini API: "+(hasGemini?"✅":"❌ ключ не установлен"));
  report.push("");
  
  // 4. USEFUL_TOPIC_ID + GROUP_CHAT_ID
  report.push("💬 Топик Полезное:");
  report.push("  • USEFUL_TOPIC_ID: "+(USEFUL_TOPIC_ID||"❌ не установлен"));
  report.push("  • GROUP_CHAT_ID: "+(GROUP_CHAT_ID||"❌ не установлен"));
  report.push("");
  
  var fullReport=report.join("\n");
  Logger.log(fullReport);
  
  ADMIN_IDS.forEach(function(aid){
    try{ tgSend(aid, fullReport, {parse_mode:"Markdown"}); }catch(e){}
  });
  
  return fullReport;
}

function restoreUsefulSystem(){
  // Полное восстановление системы Полезное
  var report=["🔧 *Восстановление Полезное*",""];
  
  // 1. Удаляем старые триггеры
  var deleted=0;
  ScriptApp.getProjectTriggers().forEach(function(t){
    var fn=t.getHandlerFunction();
    if(fn==="sendScheduledContent"||fn==="checkContentQueue"){
      try{ ScriptApp.deleteTrigger(t); deleted++; }catch(e){}
    }
  });
  report.push("🗑 Удалено старых триггеров: "+deleted);
  
  // 2. Устанавливаем новые
  try{
    ScriptApp.newTrigger("sendScheduledContent")
      .timeBased().atHour(9).nearMinute(0).everyDays(1)
      .inTimezone("Asia/Almaty").create();
    report.push("✅ Триггер sendScheduledContent: ежедневно в 9:00");
  }catch(e){report.push("❌ sendScheduledContent: "+e);}
  
  try{
    ScriptApp.newTrigger("checkContentQueue")
      .timeBased().everyHours(12).create();
    report.push("✅ Триггер checkContentQueue: каждые 12 часов");
  }catch(e){report.push("❌ checkContentQueue: "+e);}
  
  report.push("");
  
  // 3. Проверим очередь
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Контент");
    if(ws){
      var lr=ws.getLastRow();
      var waiting=0;
      for(var r=3;r<=lr;r++){
        if(ws.getRange(r,5).getValue()==="Ожидает")waiting++;
      }
      report.push("📊 В очереди: "+waiting+" постов");
      if(waiting<3){
        report.push("⚠️ Мало постов. Запусти generateNewContent если есть GEMINI_API_KEY");
        report.push("   Или добавь посты вручную в лист Контент");
      } else {
        report.push("✅ Очередь в порядке");
      }
    } else {
      report.push("❌ Лист Контент не найден. Запусти buildContent");
    }
  }catch(e){report.push("Ошибка проверки очереди: "+e);}
  
  report.push("");
  report.push("🚀 Система восстановлена. Следующая публикация: завтра 9:00 (если есть посты в Ожидает)");
  
  var fullReport=report.join("\n");
  Logger.log(fullReport);
  ADMIN_IDS.forEach(function(aid){
    try{ tgSend(aid, fullReport, {parse_mode:"Markdown"}); }catch(e){}
  });
  
  return fullReport;
}

function sendScheduledContent(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Контент");if(!ws)return;
  if(!USEFUL_TOPIC_ID||!GROUP_CHAT_ID){Logger.log("USEFUL_TOPIC_ID не настроен");return;}
  var now=new Date();
  var today=Utilities.formatDate(now,"Asia/Almaty","dd.MM.yyyy");
  var lr=ws.getLastRow();
  for(var r=3;r<=lr;r++){
    var status=ws.getRange(r,5).getValue();if(status!=="Ожидает")continue;
    var postDate=ws.getRange(r,4).getValue();if(!postDate||!(postDate instanceof Date))continue;
    var postDateStr=Utilities.formatDate(postDate,"Asia/Almaty","dd.MM.yyyy");
    // Отправляем если дата сегодня ИЛИ дата в прошлом (пропущенные)
    var postDateTime=new Date(postDate);
    var isPast=postDateTime<=now;
    if(postDateStr===today||isPast){
      var text=ws.getRange(r,3).getValue();if(!text)continue;
      try{
        UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
          method:"post",contentType:"application/json",
          payload:JSON.stringify({chat_id:GROUP_CHAT_ID,message_thread_id:parseInt(USEFUL_TOPIC_ID),text:text}),
          muteHttpExceptions:true
        });
        ws.getRange(r,5).setValue("Отправлен");
        // Дублируем в Threads: текст адаптируется под лимит 500 знаков
        try{
          var thr=publishUsefulToThreads(text);
          if(thr && thr.ok){
            var curNote=String(ws.getRange(r,5).getValue()||"");
            ws.getRange(r,5).setValue(curNote+" + Threads");
          }
        }catch(thrE){Logger.log("threads dup: "+thrE);}
      }catch(e){Logger.log("sendContent: "+e);}
      break;
    }
  }
}



function setupAutoRecsTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="generateNewRecommendations")ScriptApp.deleteTrigger(t);
  });
  // Каждые 3 дня в 11:00
  ScriptApp.newTrigger("generateNewRecommendations").timeBased().everyDays(3).atHour(11).create();
}

function generateNewRecommendations(){
  if(!GEMINI_API_KEY||GEMINI_API_KEY==="вставьте_gemini_ключ"){
    Logger.log("GEMINI_API_KEY не установлен");return;
  }
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Рекомендации");
  if(!ws){buildRecommendations(ss);ws=ss.getSheetByName("Рекомендации");}
  // Собираем уже существующие рекомендации (чтобы не повторяться)
  var lr=ws.getLastRow();
  var existing=[];
  if(lr>2){
    var data=ws.getRange(3, 2, lr-2, 20).getValues();
    data.forEach(function(row){if(row[0])existing.push(String(row[0]).substring(0,80));});
  }
  // Промпт
  var prompt="Ты консультант для бизнес-клуба Business Surgery (Алматы, 21 резидент-предприниматель)."+
    "Сгенерируй 5 НОВЫХ рекомендаций по развитию проекта.\n\n"+
    "ВАЖНО: НЕ повторяй эти уже существующие идеи:\n"+
    existing.slice(-30).map(function(e,i){return (i+1)+". "+e;}).join("\n")+"\n\n"+
    "Требования к новым рекомендациям:\n"+
    "- Конкретные действия которые можно внедрить за 1-2 недели\n"+
    "- Основанные на практиках мировых бизнес-сообществ (YC, EO, YPO, Vistage)\n"+
    "- Не банальные, не про привлечение клиентов через бот\n"+
    "- Фокус на удержание, монетизацию существующих, качество продукта\n\n"+
    "Формат строго JSON массив:\n"+
    "[{\"category\":\"Категория (1-2 слова)\",\"title\":\"Название\",\"text\":\"Описание 1-2 предложения\",\"priority\":\"Высокий/Средний/Низкий\"}]\n"+
    "Только JSON.";
  try{
    var resp=UrlFetchApp.fetch(
      "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key="+GEMINI_API_KEY,
      {method:"post",contentType:"application/json",
       payload:JSON.stringify({contents:[{parts:[{text:prompt}]}]}),
       muteHttpExceptions:true}
    );
    var result=JSON.parse(resp.getContentText());
    if(!result.candidates||!result.candidates[0]){
      Logger.log("Gemini ошибка: "+resp.getContentText().substring(0,200));return;
    }
    var text=result.candidates[0].content.parts[0].text.replace(/```json/g,"").replace(/```/g,"").trim();
    var newRecs=JSON.parse(text);
    if(!Array.isArray(newRecs)||!newRecs.length)return;
    // Добавляем в конец листа
    var dvP=SpreadsheetApp.newDataValidation()
      .requireValueInList(["Высокий","Средний","Низкий"],true).setAllowInvalid(false).build();
    var dvS=SpreadsheetApp.newDataValidation()
      .requireValueInList(["Не начато","В работе","Готово","Отложено"],true).setAllowInvalid(false).build();
    var startRow=lr+1;
    // Заголовок-разделитель с датой
    var dateStr=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy");
    ws.setRowHeight(startRow,24);
    ws.getRange(startRow,1,1,4).merge().setValue("── НОВЫЕ ОТ "+dateStr+" ──")
      .setBackground("#1A3A5C").setFontColor("#FFFFFF").setFontFamily("Montserrat")
      .setFontSize(10).setFontWeight("bold").setHorizontalAlignment("left");
    var nextRow=startRow+1;
    newRecs.forEach(function(rec,i){
      var row=nextRow+i;
      ws.setRowHeight(row,50);
      var bg=i%2===0?"#FFFFFF":"#F5F5F5";
      ws.getRange(row,1).setValue(lr-2+i+1).setBackground(bg).setHorizontalAlignment("center")
        .setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
      ws.getRange(row,2).setValue((rec.category||"")+": "+(rec.title||"")+". "+(rec.text||""))
        .setBackground(bg).setWrap(true).setFontFamily("Montserrat").setFontSize(9).setVerticalAlignment("middle");
      var prio=rec.priority||"Средний";
      var prioBg=prio==="Высокий"?"#FFEBEE":prio==="Средний"?"#FFF9C4":"#E8F5E9";
      var prioFc=prio==="Высокий"?"#C62828":prio==="Средний"?"#F57F17":"#1A7A1A";
      ws.getRange(row,3).setValue(prio).setDataValidation(dvP).setHorizontalAlignment("center")
        .setBackground(prioBg).setFontColor(prioFc).setFontFamily("Montserrat").setFontSize(10);
      ws.getRange(row,4).setValue("Не начато").setDataValidation(dvS).setHorizontalAlignment("center")
        .setFontFamily("Montserrat").setFontSize(10).setBackground(bg);
    });
    Logger.log("Добавлено "+newRecs.length+" новых рекомендаций");
    // Уведомляем админов
    ADMIN_IDS.forEach(function(id){
      /* рекомендации: только в лист, без сообщения в бот */
    });
  }catch(e){Logger.log("generateNewRecommendations: "+e);}
}

function generateNewContent(){
  if(!GEMINI_API_KEY||GEMINI_API_KEY==="вставьте_gemini_ключ"){
    toast("Заполните GEMINI_API_KEY в начале кода! Получите на aistudio.google.com");
    return;
  }
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Контент");
  if(!ws){toast("Лист Контент не найден");return;}
  toast("Генерирую посты через Gemini...");
  var prompt="Создай 15 постов для Telegram канала бизнес-клуба Business Surgery (Алматы)."+
    "Аудитория: предприниматели 100-500к тг/мес."+
    "Категории: Маркетинг, Энергия, Фокус, Финансы, Цели, Окружение, Автоматизация, Личное время."+
    "Стиль: коротко, конкретно, без воды, без длинных тире. Короткие абзацы. Заканчивай вопросом."+
    "Формат: JSON массив [{cat:...,text:...}]. Только JSON.";
  try{
    var resp=UrlFetchApp.fetch(
      "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key="+GEMINI_API_KEY,
      {method:"post",contentType:"application/json",
       payload:JSON.stringify({contents:[{parts:[{text:prompt}]}]}),
       muteHttpExceptions:true}
    );
    var result=JSON.parse(resp.getContentText());
    Logger.log("Gemini: "+resp.getContentText().substring(0,200));
    if(!result.candidates||!result.candidates[0]){
      toast("Ошибка Gemini: "+resp.getContentText().substring(0,150));return;
    }
    var text=result.candidates[0].content.parts[0].text;
    text=text.replace(/```json/g,"").replace(/```/g,"").trim();
    var posts=JSON.parse(text);
    if(!posts||!posts.length){toast("Не удалось распарсить посты");return;}
    var dvSt=SpreadsheetApp.newDataValidation()
      .requireValueInList(["Ожидает","Отправлен","Пропущен"],true).setAllowInvalid(false).build();
    var lastRow=ws.getLastRow();
    var lastDate=ws.getRange(lastRow,4).getValue();
    var startDate=lastDate instanceof Date?new Date(lastDate.getTime()+2*24*60*60*1000):new Date();
    startDate.setHours(9,0,0,0);
    for(var i=0;i<posts.length;i++){
      var row=lastRow+1+i;
      var postDate=new Date(startDate.getTime()+i*2*24*60*60*1000);
      ws.setRowHeight(row,80);
      ws.getRange(row,1).setValue(lastRow-2+i);
      ws.getRange(row,2).setValue(posts[i].cat||"Контент").setHorizontalAlignment("center");
      ws.getRange(row,3).setValue(posts[i].text||"").setWrap(true).setVerticalAlignment("top");
      ws.getRange(row,4).setValue(postDate).setNumberFormat("DD.MM.YYYY").setHorizontalAlignment("center");
      ws.getRange(row,5).setValue("Ожидает").setDataValidation(dvSt).setHorizontalAlignment("center");
      B(ws.getRange(row,1,1,5));
    }
    toast("Добавлено "+posts.length+" постов в лист Контент!");
  }catch(e){toast("Ошибка: "+e.message);Logger.log("generateNewContent: "+e);}
}
var BS_CONTENT_LIBRARY = [
  {cat:"Финансы", text:"💰 Выручка выросла, а денег на счету нет.\n\nЗнакомо? Это классика: путают выручку с прибылью.\n\nВыручка это сколько пришло. Прибыль это сколько осталось после всех расходов. Касса это сколько лежит прямо сейчас.\n\nТри разные цифры. Смотреть надо на все три, а решения принимать по третьей.\n\nВы знаете свою кассу на сегодня?"},
  {cat:"Финансы", text:"📊 Простое правило: три счёта вместо одного.\n\nСчёт А. Операционка: зарплаты, аренда, реклама.\nСчёт Б. Налоги: 25% с каждого поступления сразу сюда.\nСчёт В. Резерв: 10% от чистой прибыли.\n\nСо счёта В не берут никогда. Кроме реального кризиса.\n\nВнедряется за один день. Снимает 80% паники в конце квартала."},
  {cat:"Финансы", text:"🧮 Считаете ли вы себестоимость понедельника?\n\nВозьмите месячные постоянные расходы и разделите на 4. Это сумма, которую бизнес должен зарабатывать каждую неделю просто чтобы не уйти в минус.\n\nПовесьте эту цифру на стену.\n\nБольшинство предпринимателей впервые видят её и понимают: три недели из четырёх они работают в убыток."},
  {cat:"Продажи", text:"💬 Клиент написал «дорого». Что делать?\n\nНе оправдываться. Не сбрасывать цену.\n\nСпросить: «Дорого по сравнению с чем именно?»\n\nЭтот вопрос снимает 60% возражений. Клиент сам называет, с чем сравнивает, и дальше разговор идёт про ценность, а не про скидку."},
  {cat:"Продажи", text:"⏱ Скорость ответа решает больше, чем скрипт.\n\nОтвет в течение 5 минут: клиент ваш.\nОтвет через час: клиент уже говорит с конкурентом.\n\nДо 80% лидов уходят просто из-за медленной реакции.\n\nЗа сколько минут ваш менеджер отвечает на новую заявку? Проверьте сегодня."},
  {cat:"Продажи", text:"🎯 Не спрашивайте «Что думаете?» в конце разговора.\n\nЭтот вопрос даёт клиенту выход.\n\nДавайте план: «Отправляю счёт сегодня, оплата до пятницы, стартуем в понедельник. Подходит?»\n\nКонкретные даты вместо размытых вопросов поднимают конверсию в оплату почти вдвое."},
  {cat:"Команда", text:"👥 Сколько человек подчиняется вам напрямую?\n\nЕсли больше семи, вы бутылочное горлышко. Все решения идут через вас, и вы физически не успеваете.\n\nНорма: 3-5 прямых подчинённых. Остальные через руководителей направлений.\n\nЭто не про статус. Это про то, останется ли у вас время на стратегию."},
  {cat:"Команда", text:"📋 У каждого в команде должно быть 3-5 ключевых результатов.\n\nНе должностная инструкция на три страницы. Именно результаты с цифрами.\n\nМенеджер по продажам: 30 встреч в месяц, конверсия 30%, средний чек от 500 000.\n\nБез этого сотрудники сами решают, что важно. И решают не то."},
  {cat:"Команда", text:"🚪 Если вы месяц думаете об увольнении сотрудника, увольняйте.\n\nПока вы думаете, команда видит: слабый результат допустим. Сильные это замечают первыми.\n\nОдин человек не на своём месте тянет вниз пятерых хороших.\n\nЖалость здесь дороже обходится, чем честный разговор."},
  {cat:"Фокус", text:"🎯 Фокус это не про то, что делать. Это про то, чего НЕ делать.\n\nВыпишите всё, чем занимались на этой неделе. Отметьте три дела, которые реально двигали бизнес.\n\nОстальное либо делегируйте, либо остановите.\n\nОбычно оказывается, что 70% недели ушло на дела, которые ничего не меняют."},
  {cat:"Фокус", text:"⏰ Правило 90 минут.\n\nОдин блок в день на самое важное стратегическое дело. Без чата, без почты, без встреч.\n\nЗа 90 минут глубокой работы делается больше, чем за пять часов между делами.\n\nПоставьте в календарь как встречу. С самим собой это тоже встреча."},
  {cat:"Фокус", text:"📵 Телефон в первый час после пробуждения это чужие приоритеты.\n\nВы открываете мессенджер и день сразу принадлежит не вам.\n\nПервый час себе: план, важное дело, тишина. Потом уже мир.\n\nОдна привычка, которая меняет весь день."},
  {cat:"Делегирование", text:"🔄 Простой аудит недели.\n\nЗапишите каждое дело длиннее 15 минут. В конце недели разбейте на четыре группы:\nА. Только вы\nБ. Может кто-то другой\nВ. Не нужно никому\nГ. Отдых\n\nЦель: 60% времени в А и Г. Если меньше, вы работаете менеджером, а не собственником."},
  {cat:"Делегирование", text:"⚙️ Делегирование ломается на одном месте: нет описанного процесса.\n\nПока задача живёт только в вашей голове, любой исполнитель сделает не так.\n\nОпишите один процесс в неделю. Через два месяца у вас будет система, а не героизм.\n\nНачните с того, что делаете чаще всего."},
  {cat:"Стратегия", text:"🗺 Стратегия без трекинга это просто красивый документ.\n\nПлан на год, написанный в январе и открытый в декабре, не работает.\n\nЦикл нужен короткий: поставили гипотезу, проверили за 10 дней, скорректировали.\n\nСкорость итерации важнее качества первоначального плана."},
  {cat:"Стратегия", text:"📈 Рост это не «работать больше».\n\nЕсли бизнес растёт только когда вы работаете больше часов, это не рост. Это самозанятость с наёмными сотрудниками.\n\nНастоящий рост: выручка растёт, а ваше участие уменьшается.\n\nПроверьте по прошлому году: часов стало больше или меньше?"},
  {cat:"Метрики", text:"📉 Что не измеряете, тем не управляете.\n\nПять цифр, которые владелец должен видеть каждое утро:\nвыручка вчера, остаток на счетах, новые лиды, просроченные проекты, кто на работе.\n\nПять минут в день. Проблема видна за сутки, а не за месяц."},
  {cat:"Метрики", text:"🔍 LTV делённое на CAC должно быть больше трёх.\n\nLTV: сколько денег приносит клиент за всё время.\nCAC: сколько стоит его привлечь.\n\nЕсли меньше единицы, вы платите за каждого клиента больше, чем зарабатываете. И чем больше клиентов, тем быстрее конец.\n\nСчитайте по каждому каналу отдельно."},
  {cat:"Энергия", text:"⚡️ Энергия предпринимателя это актив компании.\n\nВыгоревший собственник принимает плохие решения. Плохие решения стоят дороже любого отпуска.\n\nСон 7+ часов, движение, один выходной без работы в неделю.\n\nЭто не про баланс. Это про качество решений, которые вы принимаете завтра."},
  {cat:"Энергия", text:"😴 Сон это не слабость, а инструмент.\n\nПосле пяти часов сна вы теряете примерно треть когнитивных способностей. При этом уверенность в своих решениях остаётся прежней.\n\nСамая опасная комбинация: плохо соображаешь и не замечаешь этого."},
  {cat:"Окружение", text:"👥 Покажите пятерых, с кем проводите больше всего времени.\n\nВаш уровень задач это среднее от их уровня задач.\n\nЕсли рядом никто не решает задачи сложнее ваших, расти будет некуда. Не потому что вы не способны, а потому что не видно, что бывает иначе."},
  {cat:"Окружение", text:"🤝 Одиночество собственника это не романтика, а риск.\n\nНекому сказать честно: «Мне страшно, я не понимаю, что делать».\n\nСотрудникам нельзя, семье не хочется грузить, конкурентам не станешь.\n\nСреда, где можно говорить прямо, экономит годы и миллионы."},
  {cat:"Автоматизация", text:"🤖 Если делаете одно и то же больше трёх раз в неделю, это должно быть автоматизировано.\n\nНе обязательно сложно. Таблица, бот, шаблон.\n\nСчитайте так: час экономии в неделю это шесть рабочих дней в год.\n\nЧто вы делаете руками прямо сейчас, что можно описать инструкцией?"},
  {cat:"Автоматизация", text:"📱 Автоматизация начинается не с технологий, а с процесса.\n\nСначала опишите шаги. Потом уберите лишние. И только потом автоматизируйте то, что осталось.\n\nАвтоматизировать хаос значит получить быстрый хаос."},
  {cat:"Цели", text:"🏔 Большая цель не мотивирует. Она парализует.\n\n«Вырасти в три раза за год» звучит вдохновляюще ровно один день.\n\nРаботает другое: что нужно сделать за ближайшие 10 дней, чтобы приблизиться.\n\nЦель задаёт направление. Движение дают короткие циклы."},
  {cat:"Цели", text:"📅 Год заканчивается не 31 декабря.\n\nОн заканчивается тогда, когда вы перестаёте что-то менять.\n\nВозьмите оставшиеся месяцы и распишите по 10-дневным отрезкам. Сколько таких отрезков осталось? Обычно это отрезвляет сильнее любого дедлайна."},
  {cat:"Ошибки", text:"⚠️ Самая дорогая ошибка: думать «и так справлюсь».\n\nПока вы справляетесь, конкурент строит систему. Через год он масштабируется, а вы всё ещё справляетесь.\n\nЦена бездействия почти всегда выше цены изменений. Просто она размазана по месяцам и потому незаметна."},
  {cat:"Ошибки", text:"🔧 Нанять РОПа не решает проблему продаж.\n\nЕсли нет скриптов, воронки и понятных метрик, новый человек просто продолжит хаос, но за большую зарплату.\n\nСначала система, потом человек в неё. Не наоборот."},
  {cat:"Ошибки", text:"📚 Ещё один курс вас не спасёт.\n\nБольшинство предпринимателей знают достаточно. Проблема не в знаниях, а во внедрении.\n\nПроверьте: сколько идей из последнего обучения вы реально внедрили?\n\nЕсли ответ «ни одной», следующий курс ничего не изменит."},
  {cat:"Процессы", text:"📋 Три вопроса, которые вскрывают состояние команды.\n\nСпросите каждого лично:\n1. Что работает хорошо?\n2. Что бесит?\n3. Что бы вы поменяли первым делом, будь вы на моём месте?\n\nПересекающиеся ответы это ваш реальный список задач на квартал."},
  {cat:"Процессы", text:"🗓 Ритуалы держат команду лучше мотивации.\n\nПонедельник 9:30 стендап 15 минут.\nСреда один на один с прямыми подчинёнными.\nПятница пятиминутка благодарностей.\n\nБез ритуалов культура распадается за полгода. С ними появляется ритм, который не зависит от настроения."},
  {cat:"Клиенты", text:"📞 Самый дешёвый рост находится в старой базе.\n\nВыгрузите клиентов за последние два года. Разделите на активных, спящих и потерянных.\n\nПозвоните спящим с конкретным поводом. Не «как дела», а «пришло то, что вам подходит».\n\nОбычно возвращается каждый третий."},
  {cat:"Клиенты", text:"💬 Спросите пять клиентов: за что они на самом деле платят?\n\nОтветы почти всегда отличаются от того, что вы думаете. И именно из них получается сильный оффер.\n\nПять разговоров по 20 минут стоят дешевле любого исследования рынка."},
];

function _miniGetContentPlan(p){
  // Очередь Полезного для приложения
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Контент");
    if(!ws)return{items:[]};
    var lr=ws.getLastRow();
    if(lr<3)return{items:[]};
    var data=ws.getRange(3, 1, lr-2, 21).getValues();
    var items=[];
    data.forEach(function(row,i){
      var text=String(row[2]||"").trim();
      if(!text)return;
      var d=row[3];
      items.push({
        row:i+3,
        num:row[0],
        cat:String(row[1]||""),
        text:text,
        date:d instanceof Date?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy HH:mm"):String(d||""),
        ts:d instanceof Date?d.getTime():0,
        status:String(row[4]||"Ожидает").trim()
      });
    });
    // Ожидающие сверху по дате, отправленные ниже
    items.sort(function(a,b){
      if(a.status===b.status)return a.ts-b.ts;
      return a.status==="Ожидает"?-1:1;
    });
    return{items:items};
  }catch(e){return{error:e.toString()};}
}

function _miniUpdateContentPost(p){
  try{
    var row=parseInt(p.row);
    if(!row||row<3)return{error:"Bad row"};
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Контент");
    if(!ws)return{error:"Нет листа"};
    if(p.text!==undefined&&String(p.text).trim())ws.getRange(row,3).setValue(String(p.text).trim());
    if(p.date){
      var parts=String(p.date).split(" ");
      var dp=parts[0].split(".");
      var tp=(parts[1]||"09:00").split(":");
      var dt=new Date(parseInt(dp[2]),parseInt(dp[1])-1,parseInt(dp[0]),parseInt(tp[0]),parseInt(tp[1]));
      ws.getRange(row,4).setValue(dt).setNumberFormat("DD.MM.YYYY HH:mm");
    }
    if(p.status)ws.getRange(row,5).setValue(String(p.status));
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniDeleteContentPost(p){
  try{
    var row=parseInt(p.row);
    if(!row||row<3)return{error:"Bad row"};
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Контент");
    if(!ws)return{error:"Нет листа"};
    ws.deleteRow(row);
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function _miniAddContentPost(p){
  try{
    var text=String(p.text||"").trim();
    var cat=String(p.cat||"Прочее").trim();
    if(!text)return{error:"Пустой текст"};
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Контент");
    if(!ws)return{error:"Нет листа"};
    var lr=ws.getLastRow();
    var when=new Date();
    if(p.date){
      var parts=String(p.date).split(" ");
      var dp=parts[0].split(".");
      var tp=(parts[1]||"09:00").split(":");
      when=new Date(parseInt(dp[2]),parseInt(dp[1])-1,parseInt(dp[0]),parseInt(tp[0]),parseInt(tp[1]));
    } else {
      when=new Date(when.getTime()+2*24*3600*1000);
      when.setHours(9,0,0,0);
    }
    ws.appendRow([lr-1, cat, text, when, "Ожидает"]);
    ws.getRange(ws.getLastRow(),4).setNumberFormat("DD.MM.YYYY HH:mm");
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

// ══════════ СММ: план публикаций для соцсетей ══════════
function _getSmmSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("СММ план");
  if(!ws){
    ws=ss.insertSheet("СММ план", ss.getNumSheets());
    ws.getRange(1,1,1,7).setValues([["Дата","Площадка","Рубрика","Заголовок","Текст","Статус","Ссылка"]]);
    ws.getRange(1,1,1,7).setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.setFrozenRows(1);
    ws.setColumnWidth(5,420);
  }
  return ws;
}

function publishSmmToTelegram(p){
  // Публикация поста SMM в канал BS напрямую из приложения
  try{
    var row=parseInt(p.row);
    if(!row||row<2)return{error:"Bad row"};
    var ws=_getSmmSheet();
    var title=String(ws.getRange(row,4).getValue()||"").trim();
    var text=String(ws.getRange(row,5).getValue()||"").trim();
    if(!text&&!title)return{error:"Пустой пост"};
    var msg=(title?"<b>"+title+"</b>\n\n":"")+text;
    var resp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendMessage",{
      method:"post", contentType:"application/json",
      payload:JSON.stringify({
        chat_id:CHANNEL_ID||"@bsurgery_kz",
        text:msg,
        parse_mode:"HTML",
        disable_web_page_preview:true
      }),
      muteHttpExceptions:true
    });
    var res=JSON.parse(resp.getContentText());
    if(res.ok){
      ws.getRange(row,6).setValue("Опубликовано");
      _invalidateBundleCache();
      return{ok:true};
    }
    return{error:String(res.description||"Ошибка Telegram")};
  }catch(e){return{error:e.toString()};}
}

function publishScheduledSmm(){
  // Автопубликация: посты со статусом Готово и сегодняшней датой уходят в канал
  try{
    var ws=_getSmmSheet();
    var lr=ws.getLastRow();
    if(lr<2)return;
    var today=Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM.yyyy");
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var published=0;
    for(var i=0;i<data.length;i++){
      var status=String(data[i][5]||"").trim();
      if(status!=="Готово")continue;
      var d=data[i][0];
      var dStr=d instanceof Date?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d);
      if(dStr!==today)continue;
      var platform=String(data[i][1]||"").trim();
      if(platform!=="Telegram")continue; // автоматически только в свой канал
      var r=publishSmmToTelegram({row:i+2});
      if(r&&r.ok)published++;
    }
    if(published){
      ADMIN_IDS.forEach(function(aid){
        try{bsSysNoteTo(aid,"📢 Опубликовано в канал: "+published+" "+(published===1?"пост":"поста"));}catch(e){}
      });
    }
  }catch(e){Logger.log("publishScheduledSmm: "+e);}
}

function _getSetting(key){
  try{
    var ws = SpreadsheetApp.openById(SS_ID).getSheetByName("Настройки");
    if(!ws) return "";
    var lr = ws.getLastRow();
    if(lr < 1) return "";
    var data = ws.getRange(1, 1, lr, 2).getValues();
    for(var i = 0; i < data.length; i++){
      if(String(data[i][0]||"").trim() === key) return String(data[i][1]||"").trim();
    }
    return "";
  }catch(e){ return ""; }
}

function _askClaude(prompt, maxTokens){
  // Запрос к Claude с сервера. Ключ лежит в листе Настройки, строка anthropic_key
  var key = _getSetting("anthropic_key");
  if(!key) return {error: "no_key"};
  try{
    var resp = UrlFetchApp.fetch("https://api.anthropic.com/v1/messages", {
      method: "post",
      contentType: "application/json",
      headers: {
        "x-api-key": key,
        "anthropic-version": "2023-06-01"
      },
      payload: JSON.stringify({
        model: "claude-sonnet-4-20250514",
        max_tokens: maxTokens || 1200,
        messages: [{role: "user", content: prompt}]
      }),
      muteHttpExceptions: true
    });
    var codeH = resp.getResponseCode();
    var body = resp.getContentText();
    if(codeH !== 200){
      Logger.log("Claude " + codeH + ": " + body.substring(0, 300));
      return {error: "api_" + codeH};
    }
    var data = JSON.parse(body);
    var text = (data.content || []).map(function(c){ return c.type === "text" ? c.text : ""; }).join("").trim();
    return {text: text};
  }catch(e){
    Logger.log("_askClaude: " + e);
    return {error: String(e)};
  }
}

// Заготовки постов на случай, если ключ не подключён
var SMM_TEMPLATES = {
  "Кейс резидента": [
    {t:"Исфандияр: с 200 тысяч до 2 млн за 4 месяца",
     x:"Пришёл с оборотом, который не превращался в деньги. Разобрали воронку и увидели: 60% заявок терялись на первом касании, менеджеры отвечали через час.\n\nПоставили регламент ответа за 5 минут. Переписали три скрипта. Ввели ежедневный отчёт по конверсии.\n\nЧерез месяц прибыль выросла вдвое. Через четыре месяца стало два миллиона чистыми.\n\nМы не добавили ему клиентов. Починили то, что уже работало наполовину."},
    {t:"Артём: прибыль втрое и второй бизнес",
     x:"Артём приходил на встречи и говорил одно и то же: не хватает времени. При этом сам согласовывал каждую закупку и каждый макет.\n\nСоставили список того, что он делает за неделю. Из 34 дел только 6 требовали именно его.\n\nОстальное передали за месяц. Освободилось 15 часов, и он потратил их на переговоры с сетями.\n\nЧерез полгода прибыль выросла втрое, а он открыл второе направление."}
  ],
  "Диагностика боли": [
    {t:"Уедешь на три недели. что будет с бизнесом?",
     x:"Ответьте честно, не для соцсетей.\n\nЕсли всё встанет, у вас не бизнес. У вас работа, которую вы сами себе придумали. Просто платят больше и спросить не с кого.\n\nСемьсот разборов показали одну и ту же картину: человек знает, что болит. Не знает почему.\n\nЗа шестьдесят минут мы находим причину. Дальше вы решаете сами."},
    {t:"Деньги есть, а удовлетворения нет",
     x:"Знакомо: обороты растут, а внутри ощущение, что топчешься на месте.\n\nОбычно дело не в деньгах. Дело в том, что вы не видите куда движетесь. Есть выручка, есть суета, нет направления.\n\nПервое, что мы делаем на разборе: раскладываем бизнес на пять систем и смотрим, какая тянет вниз. Обычно это не та, о которой вы думали."}
  ],
  "Личное мнение": [
    {t:"Ещё один курс вас не спасёт",
     x:"Сколько курсов вы прошли за три года? И сколько из этого внедрили?\n\nВот в этом и дело. Вы уже знаете достаточно. Между «знаю» и «сделал» стоит операционка, которая съедает неделю за неделей.\n\nПоэтому мы не даём знания. Каждые десять дней садимся с резидентом, смотрим цифры и говорим: вот это делаем, вот это не трогаем. Через десять дней проверяем что вышло."},
    {t:"Честно про бизнес-клубы Алматы",
     x:"Нетворкинг без работы это тусовка. Приятная, полезная для настроения, бесполезная для цифр.\n\nСвязи не растят бизнес сами по себе. Растит система: понятная стратегия, работающие процессы, команда, которая справляется без вас.\n\nСреда важна, но она работает только когда рядом есть кто-то, кто спрашивает про результат."}
  ],
  "Разбор ошибки": [
    {t:"Нанять РОПа не решит проблему продаж",
     x:"Частая история: продажи не идут, собственник ищет руководителя отдела.\n\nЧеловек приходит, а системы нет. Нет скриптов, нет воронки, нет метрик. Он начинает строить всё с нуля, тратит полгода и уходит.\n\nСначала система, потом человек в неё. Иначе вы платите зарплату за то, что и так не работает."},
    {t:"Ошибка на 500 тысяч, которую не замечают",
     x:"Скорость ответа на заявку.\n\nОтвет в течение пяти минут: клиент ваш. Ответ через час: он уже говорит с конкурентом.\n\nПо разным нишам теряется от 40 до 80 процентов заявок просто из-за медленной реакции. Деньги уже потрачены на рекламу, лид пришёл, а его никто не подхватил.\n\nПроверьте сегодня, за сколько минут отвечает ваш менеджер."}
  ],
  "Закулисье BS": [
    {t:"Как проходит разбор",
     x:"Шестьдесят минут, два трекера, один предприниматель и его цифры на экране.\n\nПервые двадцать минут вопросы: что происходит, где болит, что уже пробовали. Дальше раскладываем бизнес по системам и ищем, какая тянет вниз.\n\nПоследние пятнадцать минут собираем план на десять дней. Не список идей, а три конкретных действия с датами.\n\nЧеловек уходит с бумагой, где написано что делать завтра."}
  ],
  "Инструмент": [
    {t:"Три счёта вместо одного",
     x:"Простое правило, которое снимает половину финансовой тревоги.\n\nСчёт А: операционка. Зарплаты, аренда, реклама.\nСчёт Б: налоги. 25% с каждого поступления сразу сюда.\nСчёт В: резерв. 10% от чистой прибыли.\n\nСо счёта В не берут никогда, кроме реального кризиса.\n\nВнедряется за день. Работает годами."}
  ]
};

function _miniAnalyzeWheel(p){
  // Разбор динамики колеса. С ключом через Claude, без ключа. по правилам
  try{
    var axes = String(p.axes || "").split("|");
    var was = String(p.was || "").split(",").map(Number);
    var now = String(p.now || "").split(",").map(Number);
    var days = parseInt(p.days) || 0;
    var kind = String(p.kind || "dna");
    if(!axes.length || axes.length !== now.length) return {error: "Нет данных"};

    var changes = axes.map(function(a, i){
      return {axis: a, was: was[i], now: now[i], diff: Math.round((now[i] - was[i]) * 10) / 10};
    });
    var avgWas = Math.round(was.reduce(function(s,v){return s+v;}, 0) / was.length * 10) / 10;
    var avgNow = Math.round(now.reduce(function(s,v){return s+v;}, 0) / now.length * 10) / 10;

    var prompt = "Ты бизнес-трекер Business Surgery. Резидент дважды заполнил колесо " +
      (kind === "dna" ? "ДНК бизнеса" : "личного баланса") + ". Прошло " + days + " дней.\n\n" +
      "Динамика по осям (было. стало):\n" +
      changes.map(function(c){ return c.axis + ": " + c.was + " → " + c.now + " (" + (c.diff > 0 ? "+" : "") + c.diff + ")"; }).join("\n") +
      "\n\nСредний балл: " + avgWas + " → " + avgNow + "\n\n" +
      "Дай краткий разбор на русском, три блока по 1-2 предложения:\n" +
      "РОСТ: где выросло и что это значит\n" +
      "ПРОСЕДАНИЕ: где упало или стоит и почему это опасно\n" +
      "ФОКУС: одна зона на следующие 10 дней и первый шаг\n\n" +
      "Пиши прямо, без воды, без длинных тире, обращайся на ты. " +
      "Формат строго: РОСТ: текст, затем ПРОСЕДАНИЕ: текст, затем ФОКУС: текст.";

    var ai = _askClaude(prompt, 700);
    if(ai.text) return {text: ai.text, source: "ai"};

    // Без ключа собираем разбор по цифрам
    var grew = changes.filter(function(c){ return c.diff > 0; }).sort(function(a,b){ return b.diff - a.diff; });
    var fell = changes.filter(function(c){ return c.diff < 0; }).sort(function(a,b){ return a.diff - b.diff; });
    var weak = changes.filter(function(c){ return c.now <= 5; }).sort(function(a,b){ return a.now - b.now; });

    var out = [];
    if(grew.length){
      out.push("РОСТ: " + grew.slice(0,2).map(function(c){ return c.axis.toLowerCase() + " с " + c.was + " до " + c.now; }).join(", ") +
        ". Это то, во что вложились за " + days + " дней.");
    } else {
      out.push("РОСТ: за " + days + " дней ни одна зона не выросла. Значит работа шла вширь, а не вглубь.");
    }
    if(fell.length){
      out.push("ПРОСЕДАНИЕ: " + fell.slice(0,2).map(function(c){ return c.axis.toLowerCase() + " упало на " + Math.abs(c.diff); }).join(", ") +
        ". Обычно это цена за рост в другом месте. Проверь, осознанный ли это размен.");
    } else if(weak.length){
      out.push("ПРОСЕДАНИЕ: " + weak[0].axis.toLowerCase() + " держится на " + weak[0].now + " и не двигается. Стоячая зона тормозит остальные.");
    } else {
      out.push("ПРОСЕДАНИЕ: явных провалов нет, все зоны выше пяти.");
    }
    var focus = weak.length ? weak[0] : (fell.length ? fell[0] : changes.slice().sort(function(a,b){ return a.now - b.now; })[0]);
    out.push("ФОКУС: " + focus.axis.toLowerCase() + " (сейчас " + focus.now + "). Возьми одну задачу по этой зоне на ближайшие 10 дней и вынеси её на трекинг.");

    return {text: out.join("\n"), source: "rules"};
  }catch(e){
    return {error: String(e)};
  }
}

function _miniGenerateSmm(p){
  // Генерация поста для соцсетей. С ключом. через Claude, без ключа. из заготовок
  try{
    var platform = String(p.platform || "Instagram");
    var format = String(p.format || "Пост");
    var rubric = String(p.rubric || "Кейс резидента");

    var ss = SpreadsheetApp.openById(SS_ID);
    var wsR = ss.getSheetByName("BS - резиденты дебет");
    var resCount = 0;
    if(wsR && wsR.getLastRow() >= 3){
      var names = wsR.getRange(3, 2, wsR.getLastRow()-2, 20).getValues();
      var fired = wsR.getRange(3, 11, wsR.getLastRow()-2, 1).getValues();
      var admins = wsR.getRange(3, 16, wsR.getLastRow()-2, 1).getValues();
      for(var i = 0; i < names.length; i++){
        if(String(names[i][0]||"").trim() &&
           String(fired[i][0]||"").trim() !== "Да" &&
           String(admins[i][0]||"").trim() !== "Да") resCount++;
      }
    }

    var formatBrief = {
      "Пост": "Один связный пост на 120-180 слов.",
      "Карусель": "Карусель из 7 слайдов. Для каждого слайда короткий заголовок до 6 слов и 1-2 предложения текста. Первый слайд цепляет, последний ведёт на разбор. Отдельно текст под публикацией на 60-90 слов.",
      "Сторис": "Три коротких экрана сторис, каждый по 1-2 предложения.",
      "Reels": "Сценарий Reels на 40 секунд: реплики и что показывать в кадре. Первые 3 секунды должны останавливать."
    }[format] || "Один пост.";

    var platformBrief = {
      "Instagram": "Instagram: живой разговорный тон, абзацы по 1-2 предложения, без хэштегов.",
      "Threads": "Threads: коротко и остро, максимум 400 знаков.",
      "Telegram": "Telegram-канал: спокойный экспертный тон, можно длиннее.",
      "TikTok": "TikTok: динамично, простые слова, короткие фразы."
    }[platform] || "";

    var prompt = "Ты пишешь контент для Business Surgery. Это клуб системного трекинга бизнеса в Алматы: " +
      "разбор бизнеса каждые 10 дней с двумя основателями, Рустамом и Береке. Не курс, не инфобизнес. " +
      "Сейчас " + resCount + " резидентов, проведено больше 700 разборов, средний рост резидента втрое за год. " +
      "Вход через разбор бизнеса за 50 000 тенге: 60 минут, диагностика, план на 10 дней.\n\n" +
      "Кейсы: Исфандияр с 200 тысяч до 2 млн чистыми за 4 месяца. Казбек 15-20 млн прибыли в месяц. " +
      "Артём прибыль втрое и второй бизнес. Даулет рост втрое, купил квартиру.\n\n" +
      "Рубрика: " + rubric + "\nПлощадка: " + platformBrief + "\nФормат: " + formatBrief + "\n\n" +
      "ТРЕБОВАНИЯ:\nПиши как живой предприниматель, не как маркетолог. Запрещено: прокачай, секреты успеха, " +
      "гарантированно, хочешь так же, лови подборку. Не используй длинное тире, только точки и запятые. " +
      "Не используй конструкцию не X а Y. Не начинай с вопроса-заманухи, начинай с конкретной ситуации. " +
      "Конкретика вместо абстракций: цифры, ситуации, реплики. Максимум одна метафора. " +
      "Заканчивай спокойным приглашением на разбор, без давления.\n\n" +
      "Верни СТРОГО JSON без markdown:\n" +
      (format === "Карусель"
        ? '{"title":"заголовок","slides":[{"h":"заголовок слайда","t":"текст"}],"caption":"текст под публикацией"}'
        : '{"title":"короткий заголовок","text":"полный текст"}');

    var ai = _askClaude(prompt, 1400);

    if(ai.text){
      var raw = ai.text.replace(/```json|```/g, "").trim();
      try{
        var parsed = JSON.parse(raw);
        parsed.source = "ai";
        return parsed;
      }catch(pe){ Logger.log("parse: " + pe); }
    }

    // Ключа нет или ответ не разобрался. берём заготовку
    var pool = SMM_TEMPLATES[rubric] || SMM_TEMPLATES["Диагностика боли"];
    var pick = pool[Math.floor(Math.random() * pool.length)];
    var res = {title: pick.t, text: pick.x, source: "template"};
    if(ai.error === "no_key") res.note = "Ключ Claude не подключён. взята заготовка";
    if(format === "Карусель"){
      var parts = pick.x.split("\n\n");
      res.slides = parts.slice(0, 7).map(function(t, i){
        return {h: i === 0 ? pick.t : ("Шаг " + i), t: t};
      });
      res.caption = "Подробности на разборе. bxclub.kz";
    }
    return res;
  }catch(e){
    return {error: String(e)};
  }
}

function _miniGetSmm(p){
  try{
    var ws=_getSmmSheet();
    var lr=ws.getLastRow();
    if(lr<2)return{items:[]};
    var data=ws.getRange(2,1,lr-1,7).getValues();
    var items=[];
    data.forEach(function(row,i){
      var title=String(row[3]||"").trim();
      if(!title&&!String(row[4]||"").trim())return;
      var d=row[0];
      items.push({
        row:i+2,
        date:d instanceof Date?Utilities.formatDate(d,"Asia/Almaty","dd.MM.yyyy"):String(d||""),
        ts:d instanceof Date?d.getTime():0,
        platform:String(row[1]||"Instagram"),
        rubric:String(row[2]||""),
        title:title,
        text:String(row[4]||""),
        status:String(row[5]||"Идея").trim(),
        link:String(row[6]||"")
      });
    });
    items.sort(function(a,b){return a.ts-b.ts;});
    return{items:items};
  }catch(e){return{error:e.toString()};}
}

function _miniSaveSmm(p){
  try{
    var ws=_getSmmSheet();
    var row=parseInt(p.row||0);
    var dt=null;
    if(p.date){
      var dp=String(p.date).split(".");
      if(dp.length===3)dt=new Date(parseInt(dp[2]),parseInt(dp[1])-1,parseInt(dp[0]));
    }
    var vals=[dt||new Date(), String(p.platform||"Instagram"), String(p.rubric||""),
              String(p.title||""), String(p.text||""), String(p.status||"Идея"), String(p.link||"")];
    if(row>=2){
      ws.getRange(row,1,1,7).setValues([vals]);
    } else {
      ws.appendRow(vals);
      row=ws.getLastRow();
    }
    ws.getRange(row,1).setNumberFormat("DD.MM.YYYY");
    return{ok:true, row:row};
  }catch(e){return{error:e.toString()};}
}

function _miniDeleteSmm(p){
  try{
    var row=parseInt(p.row);
    if(!row||row<2)return{error:"Bad row"};
    _getSmmSheet().deleteRow(row);
    return{ok:true};
  }catch(e){return{error:e.toString()};}
}

function refillContentQueue(){
  // Пополняет очередь Полезного из встроенной библиотеки.
  // Работает БЕЗ внешних сервисов: Gemini-ключ не заполнен, из-за этого очередь встала в июне
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Контент");
    if(!ws)return "Лист Контент не найден";
    var lr=ws.getLastRow();
    
    // Какие тексты уже были (чтобы не повторяться)
    var used={};
    var lastDate=new Date();
    if(lr>=3){
      var data=ws.getRange(3,3,lr-2,2).getValues();
      data.forEach(function(row){
        var t=String(row[0]||"").trim();
        if(t)used[t.substring(0,60)]=true;
        var d=row[1];
        if(d instanceof Date && d>lastDate)lastDate=d;
      });
    }
    
    // Свободные посты из библиотеки
    var fresh=BS_CONTENT_LIBRARY.filter(function(p){
      return !used[p.text.substring(0,60)];
    });
    
    if(!fresh.length){
      // Библиотека закончилась: начинаем цикл заново, самые старые посты снова в очередь
      fresh=BS_CONTENT_LIBRARY.slice();
      Logger.log("refillContentQueue: библиотека пройдена полностью, стартуем новый цикл");
    }
    
    // Перемешиваем чтобы категории не шли подряд
    fresh.sort(function(){return Math.random()-0.5;});
    var toAdd=fresh.slice(0,15);
    
    // Даты: каждые 2 дня в 09:00, начиная с завтра или после последней запланированной
    var cursor=new Date(Math.max(lastDate.getTime(), Date.now()));
    var rows=[];
    var startNum=lr>=3?lr-1:1;
    toAdd.forEach(function(p,i){
      cursor=new Date(cursor.getTime()+2*24*3600*1000);
      cursor.setHours(9,0,0,0);
      rows.push([startNum+i, p.cat, p.text, new Date(cursor), "Ожидает"]);
    });
    
    if(rows.length){
      ws.getRange(lr+1,1,rows.length,5).setValues(rows);
      ws.getRange(lr+1,4,rows.length,1).setNumberFormat("DD.MM.YYYY HH:mm");
    }
    
    var msg="📢 Очередь Полезного пополнена: +"+rows.length+" постов\nБлижайший: "+
      Utilities.formatDate(rows[0][3],"Asia/Almaty","dd.MM HH:mm")+
      "\nПоследний: "+Utilities.formatDate(rows[rows.length-1][3],"Asia/Almaty","dd.MM HH:mm");
    Logger.log(msg);
    return msg;
  }catch(e){
    var err="❌ refillContentQueue: "+e;
    Logger.log(err);
    return err;
  }
}

// ══════════════════ CRM: ЕДИНАЯ БАЗА ЛИДОВ ══════════════════

function _getCrmSheet(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("CRM Лиды");
  if(!ws){
    ws=ss.insertSheet("CRM Лиды", 3);
    var head=["Дата","Имя","Телефон","Telegram","Источник","Кампания",
              "Ниша","Оборот","Запрос","Статус","Ответственный","Комментарий",
              "След. касание","Сумма сделки"];
    ws.getRange(1,1,1,head.length).setValues([head]);
    ws.getRange(1,1,1,head.length).setFontWeight("bold")
      .setBackground("#000").setFontColor("#fff");
    ws.setFrozenRows(1);
    ws.setColumnWidth(1,130);
    ws.setColumnWidth(2,150);
    ws.setColumnWidth(3,140);
    ws.setColumnWidth(9,260);
    ws.setColumnWidth(12,280);

    // Статусы выпадающим списком
    var statuses=["Новый","Взят в работу","Недозвон","Квалифицирован",
                  "Записан на диагностику","Диагностика проведена",
                  "Резидент","Платформа","Отказ","Не целевой"];
    var rule=SpreadsheetApp.newDataValidation().requireValueInList(statuses,true).build();
    ws.getRange(2,10,500,1).setDataValidation(rule);

    // Цвета статусов
    var rules=[];
    var colorMap={"Новый":"#FFF3CD","Взят в работу":"#D1ECF1","Недозвон":"#F8D7DA",
                  "Квалифицирован":"#D4EDDA","Записан на диагностику":"#CCE5FF",
                  "Диагностика проведена":"#E2D9F3","Резидент":"#C3E6CB",
                  "Платформа":"#D6E9C6","Отказ":"#F5C6CB","Не целевой":"#E9ECEF"};
    Object.keys(colorMap).forEach(function(st){
      rules.push(SpreadsheetApp.newConditionalFormatRule()
        .whenTextEqualTo(st)
        .setBackground(colorMap[st])
        .setRanges([ws.getRange(2,10,500,1)])
        .build());
    });
    ws.setConditionalFormatRules(rules);
  }
  return ws;
}

function addLead(data){
  // Единая точка входа для всех источников.
  // data: {name, phone, telegram, source, campaign, niche, revenue, request, comment}
  try{
    var ws=_getCrmSheet();
    var name=String(data.name||"").trim();
    var phone=String(data.phone||"").replace(/[^0-9+]/g,"");
    var tg=String(data.telegram||"").trim();
    if(phone && phone.indexOf("+")!==0) phone="+"+phone;
    if(!name && !phone && !tg) return {ok:false, error:"Пустой лид"};

    // Дедуп: тот же телефон или телеграм за последние 30 дней
    var lr=ws.getLastRow();
    if(lr>=2){
      var existing=ws.getRange(2,1,lr-1,4).getValues();
      var monthAgo=new Date().getTime()-30*24*3600*1000;
      for(var i=0;i<existing.length;i++){
        var d=existing[i][0];
        if(d instanceof Date && d.getTime()<monthAgo) continue;
        var ep=String(existing[i][2]||"").replace(/[^0-9+]/g,"");
        var et=String(existing[i][3]||"").trim();
        if(phone && ep && ep===phone) return {ok:true, duplicate:true, row:i+2};
        if(tg && et && et===tg) return {ok:true, duplicate:true, row:i+2};
      }
    }

    ws.appendRow([
      new Date(),
      name,
      phone,
      tg,
      String(data.source||"Не указан"),
      String(data.campaign||""),
      String(data.niche||""),
      String(data.revenue||""),
      String(data.request||""),
      "Новый",
      "",
      String(data.comment||""),
      "",
      ""
    ]);
    var newRow=ws.getLastRow();
    ws.getRange(newRow,1).setNumberFormat("DD.MM.YYYY HH:mm");

    // Уведомление админам
    try{
      var msg="🔥 Новый лид\n\n"+
        (name?"👤 "+name+"\n":"")+
        (phone?"📱 "+phone+"\n":"")+
        (tg?"💬 "+tg+"\n":"")+
        "📍 Источник: "+String(data.source||"не указан")+
        (data.campaign?"\n🎯 "+data.campaign:"")+
        (data.niche?"\n💼 "+data.niche:"")+
        (data.request?"\n\n📝 "+String(data.request).substring(0,200):"");
      ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid,msg); }catch(e){} });
    }catch(e){}

    return {ok:true, row:newRow};
  }catch(e){
    Logger.log("addLead: "+e);
    return {ok:false, error:String(e)};
  }
}

function _miniGetLeads(p){
  // Лиды для приложения
  try{
    var ws=_getCrmSheet();
    var lr=ws.getLastRow();
    if(lr<2) return {leads:[]};
    var data=ws.getRange(2,1,lr-1,14).getValues();
    var leads=[];
    data.forEach(function(row,i){
      var name=String(row[1]||"").trim();
      var phone=String(row[2]||"").trim();
      if(!name && !phone) return;
      leads.push({
        row:i+2,
        date:row[0] instanceof Date?Utilities.formatDate(row[0],"Asia/Almaty","dd.MM.yyyy"):"",
        ts:row[0] instanceof Date?row[0].getTime():0,
        name:name, phone:phone,
        telegram:String(row[3]||""),
        source:String(row[4]||""),
        campaign:String(row[5]||""),
        niche:String(row[6]||""),
        revenue:String(row[7]||""),
        request:String(row[8]||""),
        status:String(row[9]||"Новый").trim(),
        owner:String(row[10]||""),
        comment:String(row[11]||"")
      });
    });
    leads.sort(function(a,b){ return b.ts-a.ts; });
    return {leads:leads};
  }catch(e){ return {error:String(e)}; }
}

function _miniSetLeadStatus(p){
  try{
    var row=parseInt(p.row);
    var status=String(p.status||"").trim();
    if(!row||row<2) return {error:"Bad row"};
    var ws=_getCrmSheet();
    ws.getRange(row,10).setValue(status);
    if(p.comment) ws.getRange(row,12).setValue(String(p.comment));
    if(p.owner) ws.getRange(row,11).setValue(String(p.owner));
    _invalidateBundleCache();
    return {ok:true};
  }catch(e){ return {error:String(e)}; }
}

function showLeadWebhookUrl(){
  // Показывает адрес, который нужно вставить в Tilda и рекламные кабинеты
  var url = ScriptApp.getService().getUrl();
  var msg = "🔗 Приём лидов\n\n" +
    "Адрес вебхука:\n" + url + "\n\n" +
    "КУДА ВСТАВИТЬ\n\n" +
    "Tilda: настройки формы, Сервисы, Webhook. вставить адрес, метод POST.\n\n" +
    "Простая ссылка для любого сервиса:\n" +
    url + "?action=addLead&name=ИМЯ&phone=ТЕЛЕФОН&source=ИСТОЧНИК\n\n" +
    "ПОЛЯ, КОТОРЫЕ ПОНИМАЕТ СИСТЕМА\n" +
    "name, phone, telegram, source, campaign, niche, revenue, request\n\n" +
    "utm_source и utm_campaign подхватываются автоматически.\n" +
    "Дубли за 30 дней отсекаются по телефону.";
  try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
  ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid,msg); }catch(e){} });
  return msg;
}

function crmStats(){
  // Сводка по воронке
  try{
    var ws=_getCrmSheet();
    var lr=ws.getLastRow();
    if(lr<2) return "CRM пуста";

    var data=ws.getRange(2,1,lr-1,10).getValues();
    var byStatus={}, bySource={}, total=0, week=0, month=0;
    var now=new Date().getTime();

    data.forEach(function(row){
      var d=row[0];
      if(!(d instanceof Date)) return;
      total++;
      var age=now-d.getTime();
      if(age<=7*24*3600*1000) week++;
      if(age<=30*24*3600*1000) month++;
      var st=String(row[9]||"Новый").trim();
      byStatus[st]=(byStatus[st]||0)+1;
      var src=String(row[4]||"Не указан").trim();
      bySource[src]=(bySource[src]||0)+1;
    });

    var out=["📊 CRM. Воронка","",
      "Всего лидов: "+total,
      "За неделю: "+week,
      "За месяц: "+month,""];

    out.push("ПО СТАТУСАМ");
    ["Новый","Взят в работу","Недозвон","Квалифицирован","Записан на диагностику",
     "Диагностика проведена","Резидент","Платформа","Отказ","Не целевой"].forEach(function(st){
      if(byStatus[st]) out.push("  "+st+": "+byStatus[st]);
    });

    out.push("");
    out.push("ПО ИСТОЧНИКАМ");
    Object.keys(bySource).sort(function(a,b){ return bySource[b]-bySource[a]; })
      .slice(0,8).forEach(function(s){ out.push("  "+s+": "+bySource[s]); });

    // Конверсии
    var diag=(byStatus["Диагностика проведена"]||0)+(byStatus["Резидент"]||0)+(byStatus["Платформа"]||0);
    var closed=(byStatus["Резидент"]||0)+(byStatus["Платформа"]||0);
    out.push("");
    out.push("КОНВЕРСИЯ");
    if(total) out.push("  Лид → диагностика: "+Math.round(diag/total*100)+"%");
    if(diag)  out.push("  Диагностика → продажа: "+Math.round(closed/diag*100)+"%");

    var msg=out.join("\n");
    Logger.log(msg);
    try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
    ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid,msg); }catch(e){} });
    return msg;
  }catch(e){ return "Ошибка: "+e; }
}

function crmFollowups(){
  // Кому пора позвонить: новые без работы больше суток и просроченные касания
  try{
    var ws=_getCrmSheet();
    var lr=ws.getLastRow();
    if(lr<2) return;
    var data=ws.getRange(2,1,lr-1,13).getValues();
    var now=new Date(); now.setHours(0,0,0,0);
    var stale=[], due=[];

    data.forEach(function(row,i){
      var st=String(row[9]||"").trim();
      if(st==="Резидент"||st==="Платформа"||st==="Отказ"||st==="Не целевой") return;
      var name=String(row[1]||"").trim()||String(row[2]||"");
      var d=row[0];

      // Новый лид старше суток
      if(st==="Новый" && d instanceof Date){
        var hours=(new Date()-d)/3600000;
        if(hours>24) stale.push(name+" ("+Math.round(hours/24)+" дн без работы)");
      }
      // Просроченное касание
      var nxt=row[12];
      if(nxt instanceof Date){
        var nd=new Date(nxt); nd.setHours(0,0,0,0);
        if(nd<=now) due.push(name+" ("+Utilities.formatDate(nxt,"Asia/Almaty","dd.MM")+")");
      }
    });

    if(!stale.length && !due.length) return;
    var msg="📞 CRM. Требуют внимания\n";
    if(due.length)   msg+="\n⏰ Назначено касание\n"+due.join("\n")+"\n";
    if(stale.length) msg+="\n❗️ Новые без работы\n"+stale.join("\n");
    _bsDigestSend("crm", msg);
    Logger.log(msg);
  }catch(e){ Logger.log("crmFollowups: "+e); }
}

function importLeadsFromSubscribers(){
  // Разовый перенос: подписчики бота становятся лидами в CRM
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var wsS=ss.getSheetByName("Подписчики канала");
    if(!wsS || wsS.getLastRow()<2) return "Подписчиков нет";

    var lastCol=wsS.getLastColumn();
    var data=wsS.getRange(2,1,wsS.getLastRow()-1,lastCol).getValues();
    var headers=wsS.getRange(1,1,1,lastCol).getValues()[0];
    var phoneCol=-1;
    for(var h=0;h<headers.length;h++){
      if(String(headers[h]).trim()==="Телефон"){ phoneCol=h; break; }
    }

    var added=0;
    data.forEach(function(row){
      var name=String(row[3]||"").trim();
      var tg=String(row[2]||"").trim();
      var phone=phoneCol>=0 ? String(row[phoneCol]||"").trim() : "";
      var src=String(row[4]||"Telegram бот").trim();
      var niche=String(row[6]||"").trim();
      var rev=String(row[7]||"").trim();
      var goal=String(row[8]||"").trim();
      if(!name && !phone && !tg) return;
      var res=addLead({
        name:name, phone:phone, telegram:tg?("@"+tg.replace("@","")):"",
        source:src, niche:niche, revenue:rev, request:goal,
        comment:"Импорт из подписчиков"
      });
      if(res.ok && !res.duplicate) added++;
    });

    var msg="📥 Импорт из подписчиков\n\nДобавлено новых лидов: "+added;
    try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
    Logger.log(msg);
    return msg;
  }catch(e){ return "Ошибка импорта: "+e; }
}

function _getThreadsCreds(){
  // Токен и ID берутся из листа Настройки: threads_token и threads_user_id
  try{
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Настройки");
    if(!ws) return null;
    var lr=ws.getLastRow();
    var data=ws.getRange(1,1,lr,2).getValues();
    var token="", userId="";
    data.forEach(function(row){
      var k=String(row[0]||"").trim();
      if(k==="threads_token") token=String(row[1]||"").trim();
      if(k==="threads_user_id") userId=String(row[1]||"").trim();
    });
    if(!token || !userId) return null;
    return {token:token, userId:userId};
  }catch(e){ return null; }
}

function _getGithubCreds(){
  // Токен GitHub из листа Настройки: github_token
  try{
    var ws=SpreadsheetApp.openById(SS_ID).getSheetByName("Настройки");
    if(!ws) return null;
    var data=ws.getRange(1,1,ws.getLastRow(),2).getValues();
    var token="";
    data.forEach(function(row){
      if(String(row[0]||"").trim()==="github_token") token=String(row[1]||"").trim();
    });
    return token || null;
  }catch(e){ return null; }
}

function uploadImageToGithub(base64Data, fileName){
  // Кладёт картинку в тот же репозиторий, где лежит приложение,
  // и возвращает прямую ссылку. Google Drive для этого не годится:
  // он отдаёт превью вместо файла, и соцсети такие ссылки не принимают
  try{
    var token=_getGithubCreds();
    if(!token) return {ok:false, error:"Нет github_token в Настройках"};

    var clean=String(base64Data||"").replace(/^data:image\/\w+;base64,/, "");
    if(!clean) return {ok:false, error:"Пустая картинка"};

    var repo="neuvolen/bs-app";
    var path="img/"+fileName;
    var url="https://api.github.com/repos/"+repo+"/contents/"+path;

    // Если файл уже есть, нужен его sha для перезаписи
    var sha=null;
    try{
      var chk=UrlFetchApp.fetch(url,{
        headers:{Authorization:"Bearer "+token, Accept:"application/vnd.github+json"},
        muteHttpExceptions:true
      });
      if(chk.getResponseCode()===200){
        sha=JSON.parse(chk.getContentText()).sha;
      }
    }catch(e){}

    var payload={message:"BS: картинка "+fileName, content:clean};
    if(sha) payload.sha=sha;

    var res=UrlFetchApp.fetch(url,{
      method:"put",
      headers:{Authorization:"Bearer "+token, Accept:"application/vnd.github+json"},
      contentType:"application/json",
      payload:JSON.stringify(payload),
      muteHttpExceptions:true
    });
    var code=res.getResponseCode();
    if(code!==200 && code!==201){
      return {ok:false, error:"GitHub "+code+": "+res.getContentText().substring(0,150)};
    }
    var publicUrl="https://neuvolen.github.io/bs-app/img/"+fileName;
    Logger.log("Картинка загружена: "+publicUrl);
    return {ok:true, url:publicUrl};
  }catch(e){
    return {ok:false, error:String(e)};
  }
}

function postCarouselToThreads(imageUrls, caption){
  // Карусель в Threads: контейнер на каждую картинку, потом общий, потом публикация
  try{
    var creds=_getThreadsCreds();
    if(!creds) return {ok:false, error:"Нет ключей Threads"};
    if(!imageUrls || !imageUrls.length) return {ok:false, error:"Нет картинок"};
    if(imageUrls.length>10) imageUrls=imageUrls.slice(0,10);

    var base="https://graph.threads.net/v1.0/"+creds.userId;
    var childIds=[];

    // Контейнер на каждую картинку
    for(var i=0;i<imageUrls.length;i++){
      var u=base+"/threads?media_type=IMAGE&is_carousel_item=true"+
        "&image_url="+encodeURIComponent(imageUrls[i])+
        "&access_token="+encodeURIComponent(creds.token);
      var r=UrlFetchApp.fetch(u,{method:"post", muteHttpExceptions:true});
      var d=JSON.parse(r.getContentText());
      if(!d.id) return {ok:false, error:"Слайд "+(i+1)+": "+(d.error&&d.error.message||"ошибка")};
      childIds.push(d.id);
      Utilities.sleep(800);
    }

    // Общий контейнер карусели
    var text=String(caption||"").substring(0,480);
    var cu=base+"/threads?media_type=CAROUSEL"+
      "&children="+childIds.join(",")+
      "&text="+encodeURIComponent(text)+
      "&access_token="+encodeURIComponent(creds.token);
    var cr=UrlFetchApp.fetch(cu,{method:"post", muteHttpExceptions:true});
    var cd=JSON.parse(cr.getContentText());
    if(!cd.id) return {ok:false, error:"Карусель: "+(cd.error&&cd.error.message||"ошибка")};

    Utilities.sleep(3000);

    var pu=base+"/threads_publish?creation_id="+cd.id+
      "&access_token="+encodeURIComponent(creds.token);
    var pr=UrlFetchApp.fetch(pu,{method:"post", muteHttpExceptions:true});
    var pd=JSON.parse(pr.getContentText());
    if(!pd.id) return {ok:false, error:"Публикация: "+(pd.error&&pd.error.message||"ошибка")};

    return {ok:true, id:pd.id, slides:imageUrls.length};
  }catch(e){
    return {ok:false, error:String(e)};
  }
}

function postToThreads(text, opts){
  // Публикация в Threads. Два шага по требованию API: создать контейнер, потом опубликовать
  opts = opts || {};
  try{
    var creds=_getThreadsCreds();
    if(!creds) return {ok:false, error:"Нет ключей threads_token и threads_user_id в Настройках"};

    var body=String(text||"").trim();
    if(!body) return {ok:false, error:"Пустой текст"};
    // Лимит Threads 500 знаков
    if(body.length>500) body=body.substring(0,497)+"...";

    var base="https://graph.threads.net/v1.0/"+creds.userId;

    // Шаг 1: контейнер
    var createUrl=base+"/threads?media_type=TEXT"+
      "&text="+encodeURIComponent(body)+
      "&access_token="+encodeURIComponent(creds.token);
    var r1=UrlFetchApp.fetch(createUrl,{method:"post", muteHttpExceptions:true});
    var d1=JSON.parse(r1.getContentText());
    if(!d1.id) return {ok:false, error:"Контейнер не создан: "+(d1.error && d1.error.message || r1.getContentText().substring(0,150))};

    // Threads требует паузу перед публикацией
    Utilities.sleep(3000);

    // Шаг 2: публикация
    var pubUrl=base+"/threads_publish?creation_id="+d1.id+
      "&access_token="+encodeURIComponent(creds.token);
    var r2=UrlFetchApp.fetch(pubUrl,{method:"post", muteHttpExceptions:true});
    var d2=JSON.parse(r2.getContentText());
    if(!d2.id) return {ok:false, error:"Не опубликовано: "+(d2.error && d2.error.message || r2.getContentText().substring(0,150))};

    Logger.log("Threads: опубликовано, id "+d2.id);
    return {ok:true, id:d2.id};
  }catch(e){
    return {ok:false, error:String(e)};
  }
}

function _adaptForThreads(text){
  // Пост из Полезного длиннее лимита Threads. Берём первый смысловой блок
  // и добавляем приглашение. Эмодзи в начале убираем, там они лишние
  var t=String(text||"").trim();
  t=t.replace(/^[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]\s*/u,"");

  if(t.length<=460) return t;

  // Режем по абзацам, пока помещается
  var parts=t.split(/\n\n+/);
  var out="";
  for(var i=0;i<parts.length;i++){
    if((out+parts[i]).length>440) break;
    out+=(out?"\n\n":"")+parts[i];
  }
  if(!out) out=t.substring(0,440);
  // Обрезаем по последней точке, чтобы не рвать фразу
  var lastDot=out.lastIndexOf(".");
  if(lastDot>200) out=out.substring(0,lastDot+1);
  return out;
}

function publishUsefulToThreads(text){
  // Дублирует пост из Полезного в Threads
  try{
    var adapted=_adaptForThreads(text);
    var res=postToThreads(adapted);
    if(res.ok){
      Logger.log("Threads: пост продублирован");
    } else {
      Logger.log("Threads: "+res.error);
      // Молча не проглатываем: раз в сутки предупреждаем админов
      var cache=CacheService.getScriptCache();
      if(!cache.get("threads_err_notified")){
        cache.put("threads_err_notified","1",86400);
        ADMIN_IDS.forEach(function(aid){
          try{ bsSysNoteTo(aid,"⚠️ Threads не публикует\n\n"+res.error+"\n\nПроверьте threads_token в Настройках."); }catch(e){}
        });
      }
    }
    return res;
  }catch(e){ return {ok:false, error:String(e)}; }
}

function testThreadsPost(){
  // Проверка связки: публикует тестовый пост
  var creds=_getThreadsCreds();
  if(!creds){
    var msg="❌ Нет ключей Threads\n\nДобавьте в лист Настройки две строки:\nthreads_token\nthreads_user_id";
    try{ SpreadsheetApp.getUi().alert(msg); }catch(e){}
    return msg;
  }
  var res=postToThreads("Тест публикации из системы Business Surgery. "+Utilities.formatDate(new Date(),"Asia/Almaty","dd.MM HH:mm"));
  var out=res.ok ? ("✅ Опубликовано в Threads\nID: "+res.id) : ("❌ Ошибка\n\n"+res.error);
  try{ SpreadsheetApp.getUi().alert(out); }catch(e){}
  ADMIN_IDS.forEach(function(aid){ try{ tgSend(aid,out); }catch(e){} });
  return out;
}

function sendChecklistToUseful(){
  // Раз в 2 недели отправляет в топик ПОЛЕЗНОЕ один из загруженных чек-листов
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=_getLeadmagnetsSheet();
    var lr=ws.getLastRow();
    if(lr<2)return;
    
    var data=ws.getRange(2,1,lr-1,3).getValues();
    var available=[];
    data.forEach(function(row){
      var key=String(row[0]||"").trim();
      var name=String(row[1]||"").trim();
      var fid=String(row[2]||"").trim();
      if(key&&name&&fid)available.push({key:key,name:name,fileId:fid});
    });
    if(!available.length){
      Logger.log("sendChecklistToUseful: нет загруженных чек-листов");
      return;
    }
    
    // Ротация: следующий по кругу
    var props=PropertiesService.getScriptProperties();
    var idx=parseInt(props.getProperty("USEFUL_CL_IDX")||"0");
    if(idx>=available.length)idx=0;
    var cl=available[idx];
    props.setProperty("USEFUL_CL_IDX", String(idx+1));
    
    var descriptions={
      "sales":"Пошагово: где теряются деньги в продажах и как вернуть их за 30 дней.",
      "unit":"Разбор бизнеса по системам. Находите слабое звено за час.",
      "delegate":"Как вырезать из недели 15 часов и перестать быть операционным директором.",
      "hire":"Найм без ошибок: от вакансии до первых 90 дней сотрудника.",
      "marketing":"Привлечение клиентов без бюджета. Работает на любом этапе.",
      "cashflow":"Три счёта, платёжный календарь и еженедельный пульс кассы.",
      "scripts":"Готовые формулы сообщений для продаж в переписке.",
      "team_culture":"Как удержать сильных людей и снизить текучку.",
      "metrics":"Семь цифр, которые собственник читает каждое утро.",
      "crisis":"90 дней реструктуризации, когда выручка падает."
    };
    var desc=descriptions[cl.key]||"Практический чек-лист для предпринимателя.";
    
    var caption="📕 <b>"+cl.name+"</b>\n\n"+desc+"\n\nСохраните и пройдите по пунктам на этой неделе.";
    
    var payload={
      chat_id:GROUP_CHAT_ID,
      message_thread_id:USEFUL_TOPIC_ID,
      document:cl.fileId,
      caption:caption,
      parse_mode:"HTML"
    };
    var resp=UrlFetchApp.fetch("https://api.telegram.org/bot"+BOT_TOKEN+"/sendDocument",{
      method:"post", contentType:"application/json",
      payload:JSON.stringify(payload), muteHttpExceptions:true
    });
    Logger.log("sendChecklistToUseful "+cl.name+": "+resp.getContentText().substring(0,150));
  }catch(e){
    Logger.log("sendChecklistToUseful: "+e);
  }
}

function checkContentQueue(){
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Контент");if(!ws)return;
  var lr=ws.getLastRow();
  var waiting=0;
  for(var r=3;r<=lr;r++){
    if(ws.getRange(r,5).getValue()==="Ожидает")waiting++;
  }
  Logger.log("Осталось постов в очереди: "+waiting);
  if(waiting<=3){
    // Библиотека постов внутри скрипта. не зависим от внешних API и ключей
    Logger.log("Мало постов. пополняем очередь из библиотеки BS");
    try{
      var res=refillContentQueue();
      _bsDigestSend("content", String(res));
    }catch(e){Logger.log("checkContentQueue refill: "+e);}
  }
}



function buildRecommendations(ss){
  var ws=ss.getSheetByName("Рекомендации");if(ws)return; // не пересоздаём
  ws=ss.insertSheet("Рекомендации", ss.getNumSheets());
  ws.setTabColor("#6A1B9A");
  ws.setFrozenRows(2);
  ws.setRowHeight(1,44);
  TITMERGE(ws.getRange("A1:D1"),"💡 РЕКОМЕНДАЦИИ ПО РАЗВИТИЮ BS. на основе анализа рынка");
  ws.setRowHeight(2,36);
  var heads=["№","Рекомендация","Приоритет","Статус"];
  var wids=[40,600,120,120];
  for(var i=0;i<4;i++){var c=ws.getRange(2,i+1);c.setValue(heads[i]);H(c);B(c);ws.setColumnWidth(i+1,wids[i]);}

  var recs=[
    ["УДЕРЖАНИЕ И ВОВЛЕЧЁННОСТЬ","","",""],
    ["Личный кабинет резидента в боте","/status показывает сколько отчётов сдано, место в рейтинге, динамику выручки из отчётов. мотивирует не пропускать","Высокий","Не начато"],
    ["Peer-to-peer созвоны","Раз в 2 недели бот случайно соединяет двух резидентов для 15-минутного созвона. Строит комьюнити без усилий куратора (практика YCombinator)","Высокий","Не начато"],
    ["Трекинг выручки из отчётов","Парсить число из поля 'выручка' в отчёте. Через месяц. реальная статистика роста. Автопоздравление при достижении цели","Высокий","Не начато"],
    ["Чек-лист после трекинга","После встречи бот спрашивает: '3 главных договорённости?' Ответы видны на следующем трекинге. куратор приходит подготовленным","Высокий","Не начато"],
    ["Еженедельный рейтинг в группе","Каждый понедельник топ-3 по отчётам и топ-3 по выручке. Создаёт здоровую конкуренцию (практика AppSumo, YC-батчей)","Средний","Не начато"],
    ["МОНЕТИЗАЦИЯ","","",""],
    ["Апселл внутри проекта","После 2 месяцев резидентам с ростом выручки. автопредложение перейти на тариф год (скидка 20%). Конверсия выше когда есть результат","Высокий","Не начато"],
    ["VIP-трекинг","Отдельный тариф с еженедельной встречей вместо раз в 10 дней. Ограниченные места (max 5 человек). Цена в 3x от стандарта","Средний","Не начато"],
    ["Разбор как продукт","Экспресс-разборы продавать публично за 30-50к тг. Лучшие кейсы → контент для привлечения. Модель: Alex Hormozi 'free content, paid implementation'","Средний","Не начато"],
    ["КАЧЕСТВО ПРОДУКТА","","",""],
    ["Автоматический разбор отчётов","Перед трекингом Gemini анализирует все отчёты резидента: топ-3 достижения, топ-3 проблемы. Куратор приходит подготовленным за 0 минут","Высокий","Не начато"],
    ["Библиотека ресурсов","Лист в таблице или топик в группе с лучшими инсайтами с трекингов. Анонимно. Резиденты учатся друг у друга. эффект mastermind","Средний","Не начато"],
    ["Горячая линия между трекингами","Резидент может написать боту вопрос → куратор отвечает в течение 24ч. Логируется в таблицу. Повышает ценность продукта","Средний","Не начато"],
    ["ДАННЫЕ И АНАЛИТИКА","","",""],
    ["Когортный анализ","Группировать резидентов по месяцу входа. Смотреть: какие когорты дают лучший LTV, какие. больший отток. Оптимизировать онбординг","Высокий","Не начато"],
    ["NPS → действия","Сейчас NPS собирается но не обрабатывается. Если оценка <7 → автозвонок куратора в течение 48ч. Спасает уходящих резидентов","Высокий","Не начато"],
    ["Прогноз оттока","Резидент не сдал 5+ отчётов за месяц + долг растёт = риск ухода 80%. Автоалерт куратору с предложением срочно созвониться","Высокий","Не начато"],
    ["МИРОВЫЕ ПРАКТИКИ","","",""],
    ["Hot seat формат","Раз в месяц один резидент представляет бизнес всей группе (15 мин). остальные дают фидбек. Формат EO (Entrepreneurs Organization) и YPO","Средний","Не начато"],
    ["Mastermind пары","Разбить резидентов на пары по схожим нишам. Они встречаются сами раз в 2 недели. Куратор только фасилитирует начало","Средний","Не начато"],
    ["Accountability partner","Каждый резидент выбирает партнёра по ответственности. Бот каждое утро напоминает им поделиться главной задачей дня. практика Commit Action","Низкий","Не начато"],
    ["Dashboard для резидента","Каждый резидент видит свою страницу: выручка, цели, прогресс. Как Notion + Стрик. визуализация прогресса удерживает лучше текста","Средний","Не начато"],
  ];

  var dvPriority=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Высокий","Средний","Низкий"],true).setAllowInvalid(false).build();
  var dvStatus=SpreadsheetApp.newDataValidation()
    .requireValueInList(["Не начато","В работе","Готово","Отложено"],true).setAllowInvalid(false).build();

  var num=0;
  for(var i=0;i<recs.length;i++){
    var row=3+i;
    ws.setRowHeight(row,recs[i][1]?50:24);
    if(!recs[i][1]){
      ws.getRange(row,1,1,4).merge().setValue(recs[i][0])
        .setBackground("#1A3A5C").setFontColor(WHT).setFontFamily("Montserrat")
        .setFontSize(10).setFontWeight("bold").setHorizontalAlignment("left")
        .setVerticalAlignment("middle");
    } else {
      num++;
      var bg=i%2===0?WHT:G1;
      ws.getRange(row,1).setValue(num).setBackground(bg).setHorizontalAlignment("center")
        .setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
      ws.getRange(row,2).setValue(recs[i][0]+": "+recs[i][1]).setBackground(bg)
        .setWrap(true).setFontFamily("Montserrat").setFontSize(9).setVerticalAlignment("middle");
      ws.getRange(row,3).setValue(recs[i][2]).setDataValidation(dvPriority)
        .setHorizontalAlignment("center").setFontFamily("Montserrat").setFontSize(10)
        .setBackground(recs[i][2]==="Высокий"?RED_BG:recs[i][2]==="Средний"?"#FFF9C4":GRN_BG)
        .setFontColor(recs[i][2]==="Высокий"?RED:recs[i][2]==="Средний"?"#F57F17":GRN);
      ws.getRange(row,4).setValue(recs[i][3]).setDataValidation(dvStatus)
        .setHorizontalAlignment("center").setFontFamily("Montserrat").setFontSize(10);
      B(ws.getRange(row,1,1,4));
    }
  }
}


function buildSchedule(ss){
  // Сохраняем существующие встречи перед пересозданием
  var saved=[];
  var old=ss.getSheetByName("Расписание");
  if(old){
    try{
      var lr=old.getLastRow();
      if(lr>=3) saved=old.getRange(3, 1, lr-2, 21).getValues()
        .filter(function(r){ return r[1]||r[2]; });
      Logger.log("Расписание: сохранено "+saved.length+" встреч перед пересозданием");
    }catch(e){ Logger.log("Ошибка сохранения расписания: "+e); }
    ss.deleteSheet(old);
  }
  var ws=ss.insertSheet("Расписание", ss.getNumSheets());
  ws.setTabColor("#1A3A5C");ws.setFrozenRows(2);
  ws.setRowHeight(1,40);
  TITMERGE(ws.getRange("A1:I1"),"📅 РАСПИСАНИЕ BS. заполните Резидент+Дата+Время, Meet создастся автоматически");
  ws.setRowHeight(2,40);
  var heads=["№","Резидент","Дата","Время","Адрес (офлайн)","Meet-ссылка","3 дня","1 день",""];
  var wids=[35,200,110,80,200,220,65,65,1];
  for(var i=0;i<9;i++){var c=ws.getRange(2,i+1);c.setValue(heads[i]);H(c);B(c);ws.setColumnWidth(i+1,wids[i]);}
  ws.getRange(2,7).setBackground("#1A5C1A").setFontColor("#FFFFFF");
  ws.getRange(2,8).setBackground("#1A5C1A").setFontColor("#FFFFFF");
  ws.hideColumns(9);
  var wsR=ss.getSheetByName("BS - резиденты дебет");
  var dvRes=wsR?SpreadsheetApp.newDataValidation()
    .requireValueInRange(wsR.getRange("B3:B60"),true).setAllowInvalid(true).build():null;
  var dvTime=SpreadsheetApp.newDataValidation()
    .requireValueInList(["09:00","10:00","11:00","12:00","13:00","14:00","15:00","16:00","17:00","18:00","19:00","20:00"],true)
    .setAllowInvalid(true).build();
  var DS=3,ROWS=200;
  for(var ri=0;ri<ROWS;ri++){
    var row=DS+ri,bg=ri%2===0?WHT:G1;
    ws.setRowHeight(row,26);
    ws.getRange(row,1,1,9).setBackground(bg).setFontFamily("Montserrat").setFontSize(10).setVerticalAlignment("middle");
    ws.getRange(row,1).setValue(ri+1).setHorizontalAlignment("center").setFontColor(GX);
    if(dvRes)ws.getRange(row,2).setDataValidation(dvRes).setHorizontalAlignment("left");
    ws.getRange(row,3).setNumberFormat("DD.MM.YYYY").setDataValidation(dvDate()).setHorizontalAlignment("center");
    ws.getRange(row,4).setNumberFormat("HH:MM").setDataValidation(dvTime).setHorizontalAlignment("center");
    ws.getRange(row,5).setHorizontalAlignment("left");
    ws.getRange(row,6).setHorizontalAlignment("left").setFontColor("#1155CC");
    ws.getRange(row,7).setHorizontalAlignment("center").setFontColor(GRN);
    ws.getRange(row,8).setHorizontalAlignment("center").setFontColor(GRN);
    B(ws.getRange(row,1,1,9));
  }

  // Возвращаем сохранённые встречи
  if(saved.length){
    try{
      ws.getRange(3, 1, saved.length, 21).setValues(saved);
      Logger.log("Расписание: восстановлено "+saved.length+" встреч");
    }catch(e){ Logger.log("Ошибка восстановления расписания: "+e); }
  }
}



function updateAnalytics(){
  // ПОЛНАЯ Аналитика. 5 секций. Запускается по триггеру (раз в час) и вручную из меню.
  try{
    var ss=SpreadsheetApp.openById(SS_ID);
    var ws=ss.getSheetByName("Аналитика");
    if(!ws){
      buildAnalytics(ss);
      ws=ss.getSheetByName("Аналитика");
    }
    if(!ws)return;

    // Очищаем содержимое (заголовки сохраняем)
    var lastRow=ws.getLastRow();
    if(lastRow>2){
      ws.getRange(3, 1, lastRow-2, 21).clear();
    }

    var now=new Date();
    var monthStart=new Date(now.getFullYear(),now.getMonth(),1);
    var weekAgo=new Date(now.getTime()-7*86400000);

    // Читаем все нужные листы
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    var wsF=ss.getSheetByName("Штрафы");
    var wsL=ss.getSheetByName("Лог отчётов");
    var wsDDS=ss.getSheetByName("Учет ДДС");
    var wsSub=ss.getSheetByName("Подписчики канала");
    var wsPL=ss.getSheetByName("PL");
    var wsCP=ss.getSheetByName("Контент-план");

    // Резиденты
    var residents=[];
    if(wsR&&wsR.getLastRow()>=3){
      var rData=wsR.getRange(3, 1, wsR.getLastRow()-2, 21).getValues();
      rData.forEach(function(row){
        if(!row[1])return;
        residents.push({
          name:row[1], paid:Number(row[2])||0, balance:Number(row[3])||0,
          debtRenew:Number(row[4])||0, fines:Number(row[5])||0, debt:Number(row[6])||0,
          tariff:Number(row[7])||0, dateIn:row[8],
          isFired:row[10]==="Да", isExcluded:row[11]==="Да",
          chatId:String(row[13]||""), format:row[14]||"Офлайн"
        });
      });
    }
    var active=residents.filter(function(r){return !r.isFired&&!r.isExcluded;});

    // ДДС за текущий месяц
    var ddsRevByResident={};
    var ddsTotalIncMonth=0, ddsTotalExpMonth=0;
    if(wsDDS&&wsDDS.getLastRow()>=2){
      var dData=wsDDS.getRange(2,1,wsDDS.getLastRow()-1,6).getValues();
      dData.forEach(function(row){
        var d=row[0];
        if(!(d instanceof Date))return;
        var inc=Number(row[1])||0, exp=Number(row[2])||0;
        var resName=String(row[4]||"");
        // По резиденту. за ВСЁ время (LTV)
        if(inc>0&&resName){
          ddsRevByResident[resName]=(ddsRevByResident[resName]||0)+inc;
        }
        // За текущий месяц
        if(d>=monthStart){
          ddsTotalIncMonth+=inc;
          ddsTotalExpMonth+=exp;
        }
      });
    }

    // Штрафы по резидентам за последний месяц
    var finesByResMonth={};
    if(wsF&&wsF.getLastRow()>=3){
      var fData=wsF.getRange(3, 1, wsF.getLastRow()-2, 21).getValues();
      fData.forEach(function(row){
        if(!row[1])return;
        var d=row[4];
        if(d instanceof Date&&d>=monthStart){
          finesByResMonth[row[1]]=(finesByResMonth[row[1]]||0)+1;
        }
      });
    }

    // Отчёты резидентов за последние 30 дней
    var reportsByRes={};
    var monthStart30=new Date(now.getTime()-30*86400000);
    if(wsL&&wsL.getLastRow()>=2){
      var lData=wsL.getRange(2,1,wsL.getLastRow()-1,7).getValues();
      lData.forEach(function(row){
        var d=row[0];
        if(!(d instanceof Date)||d<monthStart30)return;
        var nm=String(row[2]||"");
        if(nm)reportsByRes[nm]=(reportsByRes[nm]||0)+1;
      });
    }

    // Подписчики канала
    var subs=[];
    if(wsSub&&wsSub.getLastRow()>=2){
      var sData=wsSub.getRange(2,1,wsSub.getLastRow()-1,12).getValues();
      sData.forEach(function(row){
        if(!row[1])return;
        subs.push({
          date:row[0], chatId:String(row[1]), name:row[3]||"",
          source:row[4]||"", leadmagnet:row[5]||"", niche:row[6]||"",
          revenue:row[7]||"", status:String(row[9]||"Подписчик")
        });
      });
    }

    var row=3;

    // ═══════════════════════════════════════════
    // СЕКЦИЯ A: ОБЩИЕ ПОКАЗАТЕЛИ
    // ═══════════════════════════════════════════
    ws.getRange(row,1).setValue("━━━ A. ОБЩИЕ ━━━").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.getRange(row,1,1,4).merge();
    row++;
    var offlineCount=active.filter(function(r){return r.format==="Офлайн";}).length;
    var onlineCount=active.filter(function(r){return r.format==="Онлайн";}).length;
    var withoutChatId=active.filter(function(r){return !r.chatId;}).length;

    var aRows=[
      ["Активных резидентов", active.length, ""],
      ["  Офлайн", offlineCount, Math.round(offlineCount/Math.max(active.length,1)*100)+"%"],
      ["  Онлайн", onlineCount, Math.round(onlineCount/Math.max(active.length,1)*100)+"%"],
      ["  Без Chat ID (не привязан в Telegram)", withoutChatId, ""],
      ["Подписчиков канала", subs.length, ""],
      ["  Записались на разбор", subs.filter(function(s){return s.status==="Записался на разбор";}).length, ""],
      ["  Стали резидентами", subs.filter(function(s){return s.status==="Резидент";}).length, ""],
      ["  Отказались", subs.filter(function(s){return s.status==="Отказ";}).length, ""],
      ["Выручка за текущий месяц", ddsTotalIncMonth, ""],
      ["Расходы за текущий месяц", ddsTotalExpMonth, ""],
      ["Прибыль за текущий месяц", ddsTotalIncMonth-ddsTotalExpMonth, ""],
      ["Маржа %", ddsTotalIncMonth>0 ? Math.round((ddsTotalIncMonth-ddsTotalExpMonth)/ddsTotalIncMonth*100)+"%" : "0%", ""]
    ];
    aRows.forEach(function(r){
      ws.getRange(row,1).setValue(r[0]);
      ws.getRange(row,2).setValue(r[1]).setNumberFormat(typeof r[1]==="number"?"#,##0":"@");
      if(r[2])ws.getRange(row,3).setValue(r[2]);
      row++;
    });
    row++;

    // ═══════════════════════════════════════════
    // СЕКЦИЯ B: ROI РЕЗИДЕНТОВ (LTV)
    // ═══════════════════════════════════════════
    ws.getRange(row,1).setValue("━━━ B. РЕЗИДЕНТЫ. LTV И АКТИВНОСТЬ ━━━").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.getRange(row,1,1,5).merge();
    row++;
    ws.getRange(row,1,1,5).setValues([["Имя","Месяцев","Принёс всего","Долг","Отчётов за 30 дней"]]).setFontWeight("bold").setBackground("#f0f0f0");
    row++;
    // Сортируем. кто больше принёс наверху
    var sortedRes=active.slice().sort(function(a,b){
      return (ddsRevByResident[b.name]||0) - (ddsRevByResident[a.name]||0);
    });
    sortedRes.forEach(function(r){
      var months=(r.dateIn instanceof Date)?Math.floor((now-r.dateIn)/(30.44*86400000)):0;
      var brought=ddsRevByResident[r.name]||0;
      var reportsCount=reportsByRes[r.name]||0;
      ws.getRange(row,1).setValue(r.name);
      ws.getRange(row,2).setValue(months);
      ws.getRange(row,3).setValue(brought).setNumberFormat("#,##0");
      ws.getRange(row,4).setValue(r.debt).setNumberFormat("#,##0");
      ws.getRange(row,5).setValue(reportsCount);
      // Подсветка
      if(brought<50000)ws.getRange(row,3).setBackground("#fff5e6");
      if(brought>=500000)ws.getRange(row,3).setBackground("#e6ffe6");
      if(r.debt>100000)ws.getRange(row,4).setBackground("#ffe6e6");
      if(reportsCount<5)ws.getRange(row,5).setBackground("#ffe6e6");
      if(reportsCount>=15)ws.getRange(row,5).setBackground("#e6ffe6");
      row++;
    });
    // Итого
    var totalBrought=Object.keys(ddsRevByResident).reduce(function(s,k){return s+ddsRevByResident[k];},0);
    ws.getRange(row,1).setValue("ИТОГО").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.getRange(row,3).setValue(totalBrought).setNumberFormat("#,##0").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    row+=2;

    // ═══════════════════════════════════════════
    // СЕКЦИЯ C: НА ГРАНИ ОТЧИСЛЕНИЯ ⚠️
    // ═══════════════════════════════════════════
    ws.getRange(row,1).setValue("━━━ C. НА ГРАНИ ОТЧИСЛЕНИЯ ⚠️ ━━━").setFontWeight("bold").setBackground("#c00").setFontColor("#fff");
    ws.getRange(row,1,1,5).merge();
    row++;
    ws.getRange(row,1,1,5).setValues([["Имя","Штрафов (мес)","Отчётов (30д)","Долг","Причина"]]).setFontWeight("bold").setBackground("#f0f0f0");
    row++;
    var atRisk=active.filter(function(r){
      var fc=finesByResMonth[r.name]||0;
      var rc=reportsByRes[r.name]||0;
      return fc>=3 || rc<5 || r.debt>100000;
    });
    if(atRisk.length===0){
      ws.getRange(row,1).setValue("✅ Все резиденты в норме").setFontStyle("italic").setFontColor("#666");
      row++;
    } else {
      atRisk.forEach(function(r){
        var fc=finesByResMonth[r.name]||0;
        var rc=reportsByRes[r.name]||0;
        var reasons=[];
        if(fc>=3)reasons.push("3+ штрафа");
        if(rc<5)reasons.push("мало отчётов");
        if(r.debt>100000)reasons.push("большой долг");
        ws.getRange(row,1).setValue(r.name);
        ws.getRange(row,2).setValue(fc);
        ws.getRange(row,3).setValue(rc);
        ws.getRange(row,4).setValue(r.debt).setNumberFormat("#,##0");
        ws.getRange(row,5).setValue(reasons.join(", "));
        ws.getRange(row,1,1,5).setBackground("#fff5f5");
        row++;
      });
    }
    row++;

    // ═══════════════════════════════════════════
    // СЕКЦИЯ D: ВОРОНКА ПОДПИСЧИКОВ КАНАЛА
    // ═══════════════════════════════════════════
    ws.getRange(row,1).setValue("━━━ D. ВОРОНКА ПОДПИСЧИКОВ ━━━").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.getRange(row,1,1,4).merge();
    row++;
    var totalSubs=subs.length;
    var withLeadmagnet=subs.filter(function(s){return s.leadmagnet;}).length;
    var registered=subs.filter(function(s){return s.status==="Записался на разбор";}).length;
    var converted=subs.filter(function(s){return s.status==="Резидент";}).length;
    var pct=function(n){return totalSubs>0?Math.round(n/totalSubs*100)+"%":"0%";};
    var dRows=[
      ["Подписалось всего", totalSubs, "100%"],
      ["Скачали лид-магнит", withLeadmagnet, pct(withLeadmagnet)],
      ["Записались на разбор", registered, pct(registered)],
      ["Стали резидентами", converted, pct(converted)]
    ];
    dRows.forEach(function(r){
      ws.getRange(row,1).setValue(r[0]);
      ws.getRange(row,2).setValue(r[1]);
      ws.getRange(row,3).setValue(r[2]);
      row++;
    });
    row++;
    // По нишам
    ws.getRange(row,1).setValue("Подписчики по нишам:").setFontWeight("bold");row++;
    var byNiche={};
    subs.forEach(function(s){
      if(!s.niche)return;
      byNiche[s.niche]=(byNiche[s.niche]||0)+1;
    });
    Object.keys(byNiche).sort(function(a,b){return byNiche[b]-byNiche[a];}).forEach(function(n){
      ws.getRange(row,1).setValue("  "+n);
      ws.getRange(row,2).setValue(byNiche[n]);
      row++;
    });
    if(Object.keys(byNiche).length===0){
      ws.getRange(row,1).setValue("  (пока никто не прошёл квалификацию)").setFontStyle("italic").setFontColor("#666");
      row++;
    }
    row++;

    // ═══════════════════════════════════════════
    // СЕКЦИЯ E: КАНАЛ И КОНТЕНТ
    // ═══════════════════════════════════════════
    ws.getRange(row,1).setValue("━━━ E. КАНАЛ И КОНТЕНТ ━━━").setFontWeight("bold").setBackground("#000").setFontColor("#fff");
    ws.getRange(row,1,1,4).merge();
    row++;
    var postsTotal=0, postsThisWeek=0;
    if(wsCP&&wsCP.getLastRow()>=2){
      var cpData=wsCP.getRange(2,1,wsCP.getLastRow()-1,8).getValues();
      cpData.forEach(function(cprow){
        var pubFlag=String(cprow[4]||"").toLowerCase();
        if(pubFlag==="да"){
          postsTotal++;
          var pubDate=cprow[5];
          if(pubDate instanceof Date && pubDate>=weekAgo)postsThisWeek++;
        }
      });
    }
    // Подписчики канала за неделю
    var subsThisWeek=subs.filter(function(s){return s.date instanceof Date && s.date>=weekAgo;}).length;

    var eRows=[
      ["Постов опубликовано всего", postsTotal, ""],
      ["Постов за последнюю неделю", postsThisWeek, ""],
      ["Новых подписчиков за неделю", subsThisWeek, ""],
      ["Канал в Telegram", "@bsurgery_kz", ""]
    ];
    eRows.forEach(function(r){
      ws.getRange(row,1).setValue(r[0]);
      ws.getRange(row,2).setValue(r[1]);
      if(r[2])ws.getRange(row,3).setValue(r[2]);
      row++;
    });

    // Time stamp
    row++;
    ws.getRange(row,1).setValue("Обновлено: "+Utilities.formatDate(now,"Asia/Almaty","dd.MM.yyyy HH:mm")).setFontStyle("italic").setFontColor("#666");

    SpreadsheetApp.flush();
  }catch(e){Logger.log("updateAnalytics: "+e);}
}
function checkScheduleReminders(){
  if(bsServerOwns("meeting_reminders")) return;
  try{checkScheduleFollowUps();}catch(sfe){}
  var ss=SpreadsheetApp.openById(SS_ID);
  var ws=ss.getSheetByName("Расписание");if(!ws)return;
  var now=new Date();
  var lr=ws.getLastRow();if(lr<3)return;
  var data=ws.getRange(3, 1, lr-2, 21).getValues();
  data.forEach(function(row,i){
    var resName=row[1],evDate=row[2],evTime=row[3];
    var evAddr=row[4],evLink=row[5],sent3=row[6],sent1=row[7];
    if(!evDate||!resName||!(evDate instanceof Date))return;
    var rowNum=3+i;
    var meetStart=new Date(evDate);
    if(evTime instanceof Date){
      var tStr=Utilities.formatDate(evTime,"Asia/Almaty","HH:mm");
      var tp=tStr.split(":");
      meetStart.setHours(parseInt(tp[0]),parseInt(tp[1]),0,0);
    }
    var diffMs=meetStart.getTime()-now.getTime();
    var diffDays=diffMs/(1000*60*60*24);
    var diffMins=diffMs/(1000*60);
    var dateStr=Utilities.formatDate(meetStart,"Asia/Almaty","dd.MM.yyyy HH:mm");
    var offAddr=evAddr||(getText("offline_address",null)||"г.Алматы, Достык 44");
    var wsR=ss.getSheetByName("BS - резиденты дебет");
    var resFmt="Офлайн";
    if(wsR){
      var lrR=wsR.getLastRow();
      for(var r=3;r<=lrR;r++){
        if(wsR.getRange(r, RC.name).getValue()===resName){
          resFmt=String(wsR.getRange(r, RC.format)?wsR.getRange(r, RC.format).getValue():"Офлайн");break;
        }
      }
    }
    if(!sent3&&diffDays<=3.0&&diffDays>2.5){
      _sendScheduleNotification(ss,meetStart,resFmt,offAddr,evLink,resName,3);
      ws.getRange(rowNum,7).setValue("✅");
    }
    if(!sent1&&diffDays<=1.0&&diffDays>0.5){
      _sendScheduleNotification(ss,meetStart,resFmt,offAddr,evLink,resName,1);
      ws.getRange(rowNum,8).setValue("✅");
    }
    if(diffMins<=31&&diffMins>25){
      _sendScheduleNotification(ss,meetStart,resFmt,offAddr,evLink,resName,0.5);
    }
    if(diffMins<=6&&diffMins>1){
      _sendScheduleNotification(ss,meetStart,resFmt,offAddr,evLink,resName,0);
    }
  });
}

function buildAnalytics(ss){
  var ws=ss.getSheetByName("Аналитика");if(ws)ss.deleteSheet(ws);
  ws=_insertSheetAtEnd(ss,"Аналитика");
  ws.setTabColor("#4A235A");ws.setFrozenRows(2);
  ws.setRowHeight(1,42);
  ws.getRange(1,1,1,2).setBackground(BLK).setFontColor(WHT).setFontFamily("Montserrat")
    .setFontSize(12).setFontWeight("bold").setHorizontalAlignment("left").setVerticalAlignment("middle");
  ws.getRange(1,1).setValue("📈 АНАЛИТИКА. обновляется кнопкой 📊 Обновить PL");
  ws.setRowHeight(2,32);
  ws.getRange(2,1).setValue("Показатель").setBackground(G2).setFontWeight("bold").setFontFamily("Montserrat");
  ws.getRange(2,2).setValue("Значение").setBackground(G2).setFontWeight("bold").setFontFamily("Montserrat");
  ws.setColumnWidth(1,280);ws.setColumnWidth(2,200);
  var rows=[
    ["── РЕЗИДЕНТЫ ──",""],["Всего активных"," "],["Офлайн"," "],["Онлайн"," "],["Без Chat ID"," "],
    ["── ФИНАНСЫ (текущий месяц) ──",""],["Доход"," "],["Расход"," "],["Прибыль"," "],["Рентабельность"," "],
    ["── ДОЛГИ ──",""],["Общий долг резидентов"," "],["Штрафов к получению"," "],
    ["── АКТИВНОСТЬ ──",""],["Отчётов за 7 дней"," "],["% сдачи отчётов (сегодня)"," "],
    ["── РЕКОМЕНДАЦИИ ──",""],[""," "],[""," "],[""," "],
  ];
  for(var i=0;i<rows.length;i++){
    var row=3+i;
    ws.setRowHeight(row,rows[i][0].startsWith("──")?24:26);
    if(rows[i][0].startsWith("──")){
      ws.getRange(row,1,1,2).setBackground(G2).setFontColor(GX).setFontWeight("bold")
        .setFontStyle("italic").setFontFamily("Montserrat").setFontSize(9);
      ws.getRange(row,1).setValue(rows[i][0]);
    } else {
      ws.getRange(row,1).setValue(rows[i][0]).setBackground(WHT).setFontFamily("Montserrat")
        .setFontSize(10).setFontWeight("bold").setHorizontalAlignment("left");
      ws.getRange(row,2).setValue(rows[i][1]).setBackground(G1).setFontFamily("Montserrat")
        .setFontSize(10).setHorizontalAlignment("right");
      B(ws.getRange(row,1,1,2));
    }
  }
}

function setupScheduleTrigger(){
  ScriptApp.getProjectTriggers().forEach(function(t){
    if(t.getHandlerFunction()==="checkScheduleReminders")ScriptApp.deleteTrigger(t);
  });
  ScriptApp.newTrigger("checkScheduleReminders").timeBased().everyMinutes(30).create();
}

function buildForecast(ss){
  var ws=ss.getSheetByName("Прогноз");if(ws)ss.deleteSheet(ws);
  ws=ss.insertSheet("Прогноз", ss.getNumSheets());
  ws.setTabColor("#1565C0");ws.setFrozenRows(2);

  ws.setRowHeight(1,40);
  ws.getRange(1,1,1,4).setBackground("#000000").setFontColor("#FFFFFF")
    .setFontFamily("Montserrat").setFontSize(12).setFontWeight("bold")
    .setHorizontalAlignment("left").setVerticalAlignment("middle");
  ws.getRange(1,1).setValue("📊 ПРОГНОЗ. меняйте жёлтые ячейки в столбце B");

  ws.setRowHeight(2,30);
  var hdr=["Параметр","Значение","Формула","Комментарий"];
  for(var i=0;i<4;i++){
    ws.getRange(2,i+1).setValue(hdr[i]).setBackground("#1A3A5C").setFontColor("#FFFFFF")
      .setFontWeight("bold").setFontFamily("Montserrat").setFontSize(10)
      .setHorizontalAlignment(i===0?"left":"center").setVerticalAlignment("middle");
  }

  ws.setColumnWidth(1,260);
  ws.setColumnWidth(2,140);
  ws.setColumnWidth(3,200);
  ws.setColumnWidth(4,300);

  // Все строки: row, type (inp/calc/hdr), label, value, formula, format, comment
  // ВАЖНО: формулы передаются с запятыми. Google Sheets сам конвертирует
  var rows=[
    [3,"hdr","ВХОДНЫЕ ДАННЫЕ",null,null,null,null],
    [4,"inp","Активных резидентов",21,null,"#,##0","Сколько сейчас платит"],
    [5,"inp","Средний тариф (тг/мес)",78571,null,"#,##0","Месячная плата"],
    [6,"inp","Расходы в месяц (тг)",180000,null,"#,##0","SMM, аренда, зарплата"],
    [7,"inp","CAC. стоимость 1 резидента (тг)",15000,null,"#,##0","Цена привлечения"],
    [8,"inp","Среднее время жизни (мес)",6,null,"0","Сколько месяцев платит"],
    [9,"inp","Новых в месяц",2,null,"0","Прогноз привлечения"],
    [10,"inp","Отток в месяц",2,null,"0","Сколько уходит"],
    [11,"hdr","РАСЧЁТЫ",null,null,null,null],
    [12,"calc","Выручка в месяц (тг)",null,"=B4*B5","#,##0","Резиденты × тариф"],
    [13,"calc","Прибыль в месяц (тг)",null,"=B12-B6","#,##0","Выручка − Расходы"],
    [14,"calc","Рентабельность",null,"=ЕСЛИ(B12=0;0;B13/B12)","0%","% от выручки"],
    [15,"calc","LTV (ценность клиента, тг)",null,"=B5*B8","#,##0","Тариф × мес"],
    [16,"calc","LTV / CAC",null,"=ЕСЛИ(B7=0;0;B15/B7)","0.00","Норма >3"],
    [17,"calc","Точка безубыточности (резидентов)",null,"=ЕСЛИ(B5=0;0;ОКРУГЛВВЕРХ(B6/B5;0))","0","Минимум для нуля"],
    [18,"hdr","СЦЕНАРИИ",null,null,null,null],
    [19,"calc","+5 резидентов → прибыль",null,"=(B4+5)*B5-B6","#,##0","Возможность"],
    [20,"calc","−5 резидентов → прибыль",null,"=(B4-5)*B5-B6","#,##0","Риск"],
    [21,"calc","Тариф +20% → прибыль",null,"=B4*(B5*1.2)-B6","#,##0","Повышение"],
    [22,"calc","Тариф −20% → прибыль",null,"=B4*(B5*0.8)-B6","#,##0","Скидка"],
    [23,"hdr","ПРОГНОЗ 3 МЕС",null,null,null,null],
    [24,"calc","М+1: резидентов",null,"=B4+B9-B10","0","С учётом оттока"],
    [25,"calc","М+1: прибыль",null,"=B24*B5-B6","#,##0",""],
    [26,"calc","М+2: резидентов",null,"=B24+B9-B10","0",""],
    [27,"calc","М+2: прибыль",null,"=B26*B5-B6","#,##0",""],
    [28,"calc","М+3: резидентов",null,"=B26+B9-B10","0",""],
    [29,"calc","М+3: прибыль",null,"=B28*B5-B6","#,##0",""],
    [30,"hdr","ИТОГИ ГОДА",null,null,null,null],
    [31,"calc","Годовая выручка",null,"=B12*12","#,##0","Если стабильно"],
    [32,"calc","Годовая прибыль",null,"=B13*12","#,##0","Чистая за год"],
  ];

  rows.forEach(function(r){
    var row=r[0],type=r[1],label=r[2],val=r[3],formula=r[4],fmt=r[5],comment=r[6];
    ws.setRowHeight(row,28);
    if(type==="hdr"){
      ws.getRange(row,1,1,4).setBackground("#1A3A5C").setFontColor("#FFFFFF")
        .setFontWeight("bold").setFontFamily("Montserrat").setFontSize(10)
        .setHorizontalAlignment("left").setVerticalAlignment("middle");
      ws.getRange(row,1).setValue("── "+label+" ──");
      return;
    }
    ws.getRange(row,1).setValue(label).setBackground("#FFFFFF")
      .setFontFamily("Montserrat").setFontSize(10).setFontWeight("bold")
      .setHorizontalAlignment("left").setVerticalAlignment("middle");
    if(type==="inp"){
      ws.getRange(row,2).setValue(val).setBackground("#FFF9C4")
        .setFontFamily("Montserrat").setFontSize(11).setFontWeight("bold")
        .setHorizontalAlignment("right").setNumberFormat(fmt||"#,##0");
    } else if(type==="calc"){
      ws.getRange(row,2).setFormula(formula).setBackground("#E8F5E9")
        .setFontFamily("Montserrat").setFontSize(11).setFontWeight("bold")
        .setFontColor("#1A7A1A").setHorizontalAlignment("right")
        .setNumberFormat(fmt||"#,##0");
      ws.getRange(row,3).setValue(formula.replace("=","")).setBackground("#F5F5F5")
        .setFontFamily("Courier New").setFontSize(9).setFontColor("#666666")
        .setHorizontalAlignment("left");
    }
    if(comment){
      ws.getRange(row,4).setValue(comment).setBackground("#FFFFFF")
        .setFontFamily("Montserrat").setFontSize(9).setFontColor("#666666")
        .setFontStyle("italic").setHorizontalAlignment("left");
    }
    ws.getRange(row,1,1,4).setBorder(true,true,true,true,false,false,"#E0E0E0",SpreadsheetApp.BorderStyle.SOLID);
  });

  // Условное форматирование
  var rules=[];
  rules.push(SpreadsheetApp.newConditionalFormatRule()
    .whenNumberGreaterThan(0.3).setBackground("#E8F5E9").setFontColor("#1A7A1A")
    .setRanges([ws.getRange("B14")]).build());
  rules.push(SpreadsheetApp.newConditionalFormatRule()
    .whenNumberLessThan(0).setBackground("#FFEBEE").setFontColor("#C62828")
    .setRanges([ws.getRange("B14")]).build());
  rules.push(SpreadsheetApp.newConditionalFormatRule()
    .whenNumberGreaterThanOrEqualTo(3).setBackground("#E8F5E9").setFontColor("#1A7A1A")
    .setRanges([ws.getRange("B16")]).build());
  rules.push(SpreadsheetApp.newConditionalFormatRule()
    .whenNumberLessThan(3).setBackground("#FFEBEE").setFontColor("#C62828")
    .setRanges([ws.getRange("B16")]).build());
  ws.setConditionalFormatRules(rules);
}

// Telegram канал
var CHANNEL_USERNAME = "@bsurgery_kz";
var CHANNEL_CHAT_ID = "@bsurgery_kz"; // можно использовать username


// BS. BUSINESS SURGERY v21
// Все тексты бота редактируются в листе "Настройки". без кода!
// Webhook: https://script.google.com/macros/s/AKfycbx8TX2nJwAUykLdSUFs34IxgJfxJLSgHrUlR4bhdfrM4kl1Jk9kR_js3qkAXQ1SMiT9Ag/exec
// ═══════════════════════════════════════════════════════════════════════════

var BOT_TOKEN        = "__BS_BOT_TOKEN__";
var ADMIN_ID         = "453800951";
var ADMIN_IDS        = ["453800951","1285596249"]; // Добавьте ID партнёра и ассистента сюда
var SS_ID            = "1D-D4P5G9cmX1tdyluWe88sNTVGGqJqQWA4NvrvTbrYE";
var ORIG_SS_ID       = "1vqF9tCd8TziHu_2FRfgInEztnV-2Y16d0IlY2WigtdU";
var FINE_AMT         = 10000;
var KASPI_LINK       = "https://pay.kaspi.kz/pay/ri6h2lj5";
// var WEBHOOK_URL      = "https://script.google.com/macros/s/AKfycbwADG90lTo4KotevW077lKglODs4ftHpVr60O9p98S0b0ptxfnzf0AvHVsORYrpBkcgJQ/exec"; // дубль отключён
var WEBAPP_BASE_URL  = "https://neuvolen.github.io/bs-app/"; // Mini App
var GROUP_CHAT_ID    = "-1002494126345";

// Текст политики. редактируется здесь (без перезапуска скрипта не обновляется в боте)
var CONTENT_POSTS=[
    {cat:"Маркетинг",text:"🎯 Маркетинг без стратегии. это просто трата денег.\n\nБольшинство предпринимателей думают что маркетинг = реклама.\nНет. Реклама. это усилитель. Усиливает то что есть.\n\nЕсли продукт слабый. реклама ускорит провал.\nЕсли продукт сильный. реклама ускорит рост.\n\nСначала упакуй. Потом усиливай.\n\nВопрос дня: ты знаешь точно почему клиенты выбирают тебя, а не конкурента?"},
    {cat:"Маркетинг",text:"📱 Личный бренд. это не про красивые фото.\n\nЭто про доверие. А доверие строится через последовательность.\n\nПост раз в неделю в течение года > 10 постов в день в течение недели и тишина.\n\nАлгоритм простой:\n  Пиши о том что реально знаешь\n  Показывай процесс, не только результат\n  Будь последователен даже когда не хочется\n\nЧерез год тебя будут знать. Через два. рекомендовать."},
    {cat:"Маркетинг",text:"💬 Самый дешёвый маркетинг. довольный клиент.\n\nОн расскажет троим. Недовольный. десятерым.\n\nПоэтому сервис. это не расходы. Это инвестиция в сарафан.\n\nОдин WOW-момент в работе с клиентом окупается в 10 раз.\nЧто ты делаешь чтобы клиент сказал WOW?"},
    {cat:"Энергия",text:"⚡️ Энергия. главный ресурс предпринимателя. Не деньги. Не время.\n\nМожно иметь миллион рублей и лежать без сил.\nМожно не иметь ничего но гореть и двигаться.\n\nТри вещи которые убивают энергию незаметно:\n  Незакрытые разговоры (долги, конфликты, недосказанность)\n  Дела которые ты делаешь но не должен\n  Люди рядом которые тянут вниз\n\nПроверь своё окружение. Оно либо заряжает, либо сливает."},
    {cat:"Энергия",text:"😴 Сон. это не слабость. Это стратегия.\n\nНедосыпающий предприниматель принимает решения как пьяный.\nЭто не метафора. исследования подтверждают.\n\n6 часов сна в течение двух недель = состояние человека после суток без сна.\n\nХочешь принимать лучшие решения?\nЛожись спать раньше. Серьёзно."},
    {cat:"Фокус",text:"🎯 Фокус. это не про то что делать. Это про то что НЕ делать.\n\nУ тебя 24 часа. Как у всех.\nРазница между теми кто растёт и теми кто стоит на месте. в выборе.\n\nОдна большая задача > десять мелких.\nОдин проект до конца > пять на половине.\n\nСегодня: выбери одно. Сделай до конца."},
    {cat:"Фокус",text:"📵 Телефон утром. это чужие приоритеты вместо своих.\n\nПервый час после пробуждения определяет тональность всего дня.\nЕсли первое что ты делаешь. скроллишь ленту, ты уже проиграл утро.\n\nПопробуй 7 дней: первый час без телефона.\nТолько своя голова, свои мысли, свои задачи.\n\nРезультат удивит."},
    {cat:"Финансы",text:"💰 Выручка. это тщеславие. Прибыль. это реальность.\n\nМногие предприниматели гордятся оборотом.\n«У меня миллион в месяц». и 950к расходов.\n\nСчитай маржу. Считай чистую прибыль.\nСчитай сколько ты берёшь домой.\n\nБизнес должен кормить владельца. Если нет. это дорогое хобби."},
    {cat:"Финансы",text:"📊 Если ты не знаешь свои цифры. ты не управляешь бизнесом. Бизнес управляет тобой.\n\nМинимум что нужно знать каждую неделю:\n  Выручка\n  Расходы\n  Сколько заработал лично\n\nВсё остальное. потом. Но это. обязательно.\n\nЦифры не врут. Ощущения. врут."},
    {cat:"Цели",text:"🏔 Большая цель не мотивирует. Она пугает.\n\nПоэтому большинство её не ставит.\nИли ставит и забывает через неделю.\n\nСекрет прост: разбей на 90 дней.\nЧто конкретно ты сделаешь за 90 дней чтобы приблизиться?\n\nНе «хочу миллион». А «за 90 дней я сделаю X, Y, Z».\n\nКонкретика. это и есть мотивация."},
    {cat:"Цели",text:"📅 Год заканчивается не 31 декабря. Он заканчивается сегодня.\n\nКаждый день ты либо приближаешься к цели, либо отдаляешься.\nНейтрального не существует.\n\nОдин вопрос который стоит задавать каждый вечер:\n«Что я сегодня сделал что приближает меня к цели?»\n\nЕсли ответить нечем. завтра начни иначе."},
    {cat:"Окружение",text:"👥 Покажи мне пятерых с кем ты проводишь больше всего времени. и я скажу кем ты станешь.\n\nЭто не красивая цитата. Это математика.\n\nТы усредняешься по своему окружению автоматически.\nБез усилий. Просто находясь рядом.\n\nХочешь расти. нужно окружение которое уже там где ты хочешь быть."},
    {cat:"Автоматизация",text:"🤖 Если ты делаешь одно и то же больше трёх раз. это нужно автоматизировать.\n\nПредприниматель должен думать, решать, создавать.\nНе выполнять рутину которую может сделать система.\n\nОдин час вложенный в автоматизацию экономит десятки часов в будущем.\n\nЧто ты делаешь каждую неделю руками, что можно автоматизировать?"},
    {cat:"Личное время",text:"⏰ Отдых. это не награда за работу. Это условие продуктивной работы.\n\nПредприниматель который не отдыхает. это машина которую не обслуживают.\nЕдет. До поломки.\n\nПланируй отдых так же жёстко как встречи.\nБлок в календаре. Неприкосновенный.\n\nТы единственный актив в своём бизнесе которого нельзя заменить."},
    {cat:"Личное время",text:"🌱 Личное развитие. это не трата времени. Это умножение всего остального.\n\nЧас чтения в день = 12-15 книг в год.\nЭто больше чем читает 99% предпринимателей.\n\nЗнания которые ты получаешь. конвертируются в решения.\nЛучшие решения = лучшие результаты.\n\nЧто ты читаешь прямо сейчас?"}
];

var ONBOARDING_MSGS=[
  "День 1️⃣\\n\\nПривет! Добро пожаловать в Business Surgery! 🎉\\n\\nЯ бот проекта. В ближайшие 5 дней буду присылать тебе всё что нужно знать чтобы получить максимум.\\n\\nС чего начать прямо сейчас:\\n✅ Напиши первый отчёт сегодня (шаблон: /help)\\n✅ Запиши кружочек о себе во вкладке РЕЗИДЕНТЫ в нашей группе. расскажи кто ты, чем занимаешься и какая твоя главная цель\\n✅ Познакомься с резидентами\\n\\nМы рады что ты с нами! 💪\\n\\nbxclub.kz",
  "День 2️⃣\\n\\nКак писать отчёт чтобы это работало на тебя, а не просто для галочки.\\n\\nОтчёт. это не контроль. Это инструмент для тебя самого.\\n\\nКогда ты фиксируешь:\\n  Что сделал\\n  Что не получилось\\n  Что завтра\\n\\nТы начинаешь видеть паттерны. Где теряешь время. Где растёшь.\\n\\nШаблон: /help\\n\\nВажно: отчёт до 23:59. За пропуск. штраф 10 000 тг.",
  "День 3️⃣\\n\\nТрекинг встреча. главный инструмент проекта.\\n\\nКак подготовиться:\\n1. Запиши 3 главных задачи которые хочешь разобрать\\n2. Принеси цифры: выручка, расходы, конверсии\\n3. Будь готов говорить честно. здесь нет места для красивых историй\\n\\nВстречи каждые 10 дней. Следи за расписанием в группе.\\n\\nОпоздание = штраф 10 000 тг.",
  "День 4️⃣\\n\\nИстория одного резидента.\\n\\nОн зашёл в проект с выручкой 80к в месяц.\\nЧерез 3 месяца. 380к.\\n\\nЧто изменилось? Не волшебная таблетка.\\nПросто: ежедневные отчёты + честный разбор на трекинге + окружение которое требует роста.\\n\\nТы уже в правильном месте.\\nОстальное. твои действия. 💪",
  "День 5️⃣\\n\\nТы уже 5 дней в системе. Это больше чем делают многие.\\n\\nНапоминание о главном:\\n📝 Отчёт каждый день до 23:59\\n🗓 Трекинг встреча каждые 10 дней\\n💰 Штраф за пропуск: 10 000 тг\\n\\nЕсли есть вопросы или трудности. мы здесь. Пиши куратору или в группу.\\n\\nРады что ты с нами. Давай сделаем результат! 🚀\\n\\nbxclub.kz"
];

var POLICY_TEXT = "📋 Перед использованием сервиса необходимо ознакомиться с документами:\n\n📄 Публичная оферта. https://bxclub.kz/oferta\n🔒 Политика конфиденциальности. https://bxclub.kz/privacy\n\nНажимая кнопку «✅ Принять и продолжить», вы:\n• подтверждаете ознакомление с условиями Оферты\n• принимаете условия Договора\n• даете согласие на обработку персональных данных\n• соглашаетесь с электронным способом заключения договора\n• подтверждаете, что ваши действия являются аналогом собственноручной подписи";
// const OFERTA_VERSION = "1.0";
// const PRIVACY_VERSION = "1.0";



var REPORTS_TOPIC_ID = "9"; // топик ОТЧЕТЫ
var IMPORTANT_TOPIC_ID = "1980"; // топик ВАЖНОЕ
var USEFUL_TOPIC_ID = "2"; // топик ПОЛЕЗНОЕ
var ANTHROPIC_API_KEY = "вставьте_сюда"; // не используется
var GEMINI_API_KEY = "вставьте_gemini_ключ"; // aistudio.google.com

var BLK="#000000",WHT="#FFFFFF",G1="#F5F5F5",G2="#E8E8E8",GX="#8C8C8C",BRD="#D0D0D0";
var RED="#CC0000",GRN="#1A7A1A",GRN_BG="#E8F5E9",RED_BG="#FFEBEE";

// ── Утилиты ──────────────────────────────────────────────────────────────

// ── Чтение текста из листа Настройки ─────────────────────────────────────
// Кэш текстов настроек в памяти скрипта
var _settingsCache = null;


// Fallback тексты. если в листе "Настройки" нет ключа, берём отсюда
var FALLBACK_TEXTS = {
  "visit_1": "{имя}, кайфую от первой встречи цикла. Главное. внедрить то что решили. Жду тебя на следующей встрече с прогрессом",
  "visit_2": "{имя}, прошла половина цикла. Если что-то идёт не по плану, лучше написать сейчас. корректнее завернём в следующем разборе",
  "visit_3": "{имя}, третья встреча прошла. Финишная прямая по этому циклу. Подумай заранее с какими цифрами и результатами придёшь к финалу",
  "visit_4": "{имя}, четвёртая встреча. Цикл завершается. Если есть что докрутить. пиши прямо сейчас, не откладывай",
  "visit_complete_3": "🎉 {имя}, поздравляю с завершением цикла из 3 встреч!\n\nТы с нами уже {месяцев}. Это много, и каждый цикл даёт результат.\n\nПродление: {сумма} ₸\nKaspi: {kaspi}\n\nБудем рады отметке в Instagram @business.surgery и рекомендации твоим знакомым 🤝",
  "visit_complete_4": "🎉 {имя}, поздравляю с завершением цикла из 4 встреч!\n\nТы с нами уже {месяцев}. Это много, и каждый цикл даёт результат.\n\nПродление: {сумма} ₸\nKaspi: {kaspi}\n\nБудем рады отметке в Instagram @business.surgery и рекомендации твоим знакомым 🤝"
};


// ── Telegram API ──────────────────────────────────────────────────────────

// Отправка с inline-кнопкой

// Отправка с кнопками "Принимаю" (callback)

// ── Защита от дублирования (кэш) ─────────────────────────────────────────
var _processedUpdates = {};

// ═══════════════════════════════════════════════════════════════════════════
// ГЛАВНАЯ
// ═══════════════════════════════════════════════════════════════════════════


// Вторая часть. запустите если setupAllSheets завершилась с таймаутом









// ── Удаление ВСЕХ старых триггеров ───────────────────────────────────────

// ── Уведомление резидента о Цене слова ───────────────────────────────────










// ═══════════════════════════════════════════════════════════════════════════
// ЛИСТ НАСТРОЙКИ. все тексты бота редактируются здесь
// ═══════════════════════════════════════════════════════════════════════════


// ═══════════════════════════════════════════════════════════════════════════
// РЕЗИДЕНТЫ
// ═══════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════
// УЧЕТ ДДС
// ═══════════════════════════════════════════════════════════════════════════


// ═══════════════════════════════════════════════════════════════════════════
// PL
// ═══════════════════════════════════════════════════════════════════════════





// ═══════════════════════════════════════════════════════════════════════════
// ТРИГГЕРЫ
// ═══════════════════════════════════════════════════════════════════════════
















// Обработка текстовых сообщений от админа (сумма для штрафа/приход)






// ── onEdit ────────────────────────────────────────────────────────────────

// ── Пересчёт штрафов и G ─────────────────────────────────────────────────

























