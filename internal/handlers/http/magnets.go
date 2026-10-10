package http

// R76: лид-магниты как постоянная рубрика Threads.
//
// Владелец (10.10): «Threads пишу, и что-то нет результата. Нужно, чтобы
// цепляло, вот как про 99 бесплатных чек-листов: "бесплатно собрал для вас
// 1000+ бизнес-идей". Такое должно быть постоянно».
//
// Цифры за 14 дней до R76 (лог «funnel stats 14d» 09.10): 10 лидов в боте,
// 9 из Threads, из них по ссылке поста 1 (остальные из шапки профиля); выбрали
// боль 1, открыли гайд 1, записались 1. С 03.10 ИИ не написал ни одного поста
// (лимит, ключ, фильтр качества): все посты дня шли готовыми текстами
// библиотеки, ссылка на 99 чек-листов стояла в 1 посте из 4.
//
// Магнит: конкретная польза, которую человек забирает сразу. Пост
// «Бесплатно собрал для тебя …» заканчивается ссылкой app.bxclub.kz/m/<код>:
// сервер считает клик и ведёт в бот t.me/bsurgery_bot?start=mg_<магнит>_p<пост>,
// бот сразу отдаёт материал (страница без входа или PDF), дальше обычный
// прогрев, и в CRM видно, какой магнит и какой пост привёл лида.
// Маркетинг → SMM → «Лид-магниты»: посты → клики → старты → лиды → разборы.
//
// Тексты постов написаны вручную (3 варианта на магнит) и не зависят от ИИ:
// рубрика выходит даже в день, когда бесплатный лимит ИИ кончился.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
)

type leadMagnet struct {
	ID     string
	Title  string // «1 059 бизнес-идей» (placeholders allowed)
	Short  string // one line under the title (lead home, bot menu)
	Icon   string
	Kind   string // page, pdf, quiz, app
	Path   string // page: the open page; pdf: the library tool id
	Weight int    // how often the rubric takes it
	CTA    string // the post's last line before the link
	Posts  [3]string
	Give   string // the bot's message with the material
	Warm   string // the first warm-up touch starts with it
}

// leadMagnets keeps the order of the bot menu and the lead home.
var leadMagnetList = []leadMagnet{
	{ID: "ideas", Title: "{ideas} бизнес-идей", Short: "Бюджет, окупаемость, команда и первые шаги по каждой", Icon: "💡", Kind: "page", Path: "/ideas", Weight: 3,
		CTA: "Каталог бесплатно: ",
		Posts: [3]string{
			"Бесплатно собрал для тебя {ideas} бизнес-идей для Казахстана.\n\nПо каждой посчитано: стартовый бюджет в ₸, окупаемость, маржа, кто нужен в команду, первые шаги и главные риски.\n\n12 направлений, от еды и услуг до агро и B2B. Фильтры по бюджету, формату и «можно начать одному».\n\nСохрани, пригодится, когда спросят, чем заняться.",
			"Хочешь своё дело, но пока не выбрал какое? Вот {ideas} вариантов с цифрами.\n\nПо каждой идее расчёт: сколько вложить, когда окупится, сколько людей нанять и что сделать в первую неделю.\n\n{ideas1m} идей стартуют с бюджетом до 1 млн ₸.\n\nКаталог открыт бесплатно, без регистрации.",
			"Цех пельменей для магазинов у дома: старт от 3,5 млн ₸, окупаемость 10-18 месяцев.\n\nЭто одна карточка из {ideas}. В каждой бюджет, маржа, команда, что купить, первые шаги и риски.\n\nМы собрали их в один каталог с фильтрами и отдаём бесплатно. Открой и найди свою за 5 минут.",
		},
		Give: "💡 Каталог «{ideas} бизнес-идей» открыт для вас.\n\nПо каждой идее: старт в ₸, окупаемость, маржа, команда, первые шаги и риски. Фильтры по бюджету, формату и направлению, готовые подборки ниже.",
		Warm: "Вы забрали каталог бизнес-идей. Совет: отберите 3 идеи и по каждой пройдите первые шаги из карточки на бумаге, до вложений."},
	{ID: "ideas1m", Title: "{ideas1m} идей со стартом до 1 млн ₸", Short: "Что можно запустить на свои накопления", Icon: "💰", Kind: "page", Path: "/ideas?pack=1m", Weight: 2,
		CTA: "Подборка бесплатно: ",
		Posts: [3]string{
			"{ideas1m} бизнес-идей, где на старт хватит 1 млн ₸.\n\nВыбрал из каталога на {ideas} идей те, что можно запустить на свои накопления: услуги, онлайн, производство из дома, сезонная торговля.\n\nВ каждой: бюджет, окупаемость, первые шаги и риски.\n\nПодборка бесплатная, открывается с телефона.",
			"Есть миллион тенге. Что можно открыть?\n\nМы посчитали: {ideas1m} вариантов, у которых стартовый бюджет укладывается в 1 млн ₸. Мастер на час стартует со 150 тыс ₸, сборка мебели с маркетплейсов со 100 тыс ₸.\n\nПо каждой окупаемость, маржа и сколько людей нужно.\n\nЗабирай подборку, она бесплатная.",
			"Самая дорогая ошибка новичка: вложить всё в первую идею без расчёта.\n\nПоэтому мы собрали {ideas1m} идей со стартом до 1 млн ₸ и к каждой написали риски и первые 3 шага.\n\nПроверить идею на бумаге дешевле, чем на своих деньгах. Подборка бесплатная.",
		},
		Give: "💰 Подборка «{ideas1m} идей со стартом до 1 млн ₸» открыта для вас.\n\nПо каждой: бюджет, окупаемость, маржа, команда, первые шаги и риски. Весь каталог на {ideas} идей там же.",
		Warm: "Вы забрали подборку идей до 1 млн ₸. Совет: отберите 3 идеи и по каждой пройдите первые шаги из карточки на бумаге, до вложений."},
	{ID: "almaty", Title: "{almaty} бизнес-идей для Алматы", Short: "Где город и область уже в расчёте", Icon: "🏔", Kind: "page", Path: "/ideas?pack=almaty", Weight: 2,
		CTA: "Подборка бесплатно: ",
		Posts: [3]string{
			"{almaty} бизнес-идей, где Алматы и область уже в расчёте.\n\nДесерты для кофеен, у которых нет своего кондитера. Микрозелень для ресторанов. Травяные сборы из горных трав для туристов.\n\nВ каждой карточке бюджет, окупаемость, кто клиент и первые шаги.\n\nСобрали подборку из каталога на {ideas} идей. Бесплатно.",
			"Живёшь в Алматы и думаешь о своём деле? Начни с того, что город уже покупает.\n\nМы отобрали {almaty} идей с привязкой к Алматы и области: местные клиенты, сырьё, сезон и площадки.\n\nПо каждой посчитан старт в ₸ и срок окупаемости.\n\nПодборка открыта бесплатно.",
			"Алматинская область даёт дешёвое сырьё в сезон: яблоки, томаты, горные травы.\n\nИз этого вырастают пастила, соусы под своей маркой, травяные чаи. Старт от 600 тыс ₸.\n\nТаких идей с привязкой к городу и области у нас {almaty}, с бюджетом, рисками и первыми шагами.\n\nЗабирай подборку, она бесплатная.",
		},
		Give: "🏔 Подборка «{almaty} бизнес-идей для Алматы и области» открыта для вас.\n\nПо каждой: кто клиент, старт в ₸, окупаемость, первые шаги и риски. Весь каталог на {ideas} идей там же.",
		Warm: "Вы забрали подборку идей для Алматы. Совет: отберите 3 идеи и по каждой пройдите первые шаги из карточки на бумаге, до вложений."},
	{ID: "diag", Title: "{diag} диагнозов и проверка за 2 минуты", Short: "6 вопросов да/нет: что болит и сколько это стоит", Icon: "🔬", Kind: "quiz", Path: "/free/map", Weight: 2,
		CTA: "Проверка бесплатно: ",
		Posts: [3]string{
			"{diag} диагнозов бизнеса. Твой найдётся за 2 минуты.\n\nКассовые разрывы. Продажи держатся на собственнике. Люди уходят через полгода. Прибыль есть только на бумаге.\n\nБот задаст 6 вопросов да/нет прямо в Telegram и покажет, сколько денег в месяц уходит и какой шаг сделать первым.\n\nПроверка бесплатная.",
			"Бизнес болеет так же, как человек: сначала симптом, потом диагноз.\n\nМы описали {diag} диагнозов: признаки, причины, цена в ₸ и первые шаги. По ним клуб ведёт разборы.\n\nНачни с проверки на 2 минуты: 6 вопросов, и бот покажет, что болит сильнее всего. Бесплатно.",
			"Выручка растёт, а денег больше не становится? Это один из {diag} диагнозов в нашей библиотеке.\n\nПроверь свой бизнес за 2 минуты: 6 вопросов да/нет в Telegram, в конце расчёт, сколько ты теряешь в месяц.\n\nПосле проверки откроется карта всех диагнозов. Всё бесплатно.",
		},
		Give: "🔬 {diag} диагнозов бизнеса. Начнём с проверки на 2 минуты прямо здесь: выберите, что болит сильнее всего.\n\nКарта всех диагнозов по органам бизнеса откроется по кнопке ниже.",
		Warm: "Вы проверяли бизнес по карте диагнозов. Если узнали у себя 3 и больше, они почти всегда связаны: лечить нужно корень."},
	{ID: "map", Title: "Карта {diag} диагнозов", Short: "Все проблемы бизнеса по органам на одной странице", Icon: "🗺", Kind: "page", Path: "/free/map", Weight: 1,
		CTA: "Карта бесплатно: ",
		Posts: [3]string{
			"Карта диагнозов бизнеса: {diag} проблем на одной странице.\n\nРазложены по органам: стратегия, маркетинг, продажи, команда, финансы, процессы, аналитика.\n\nНажимаешь на диагноз и видишь признаки, причины, самопроверку и первые шаги.\n\nПо этой карте мы ведём разборы. Открыли её бесплатно.",
			"Где твой бизнес теряет деньги? Пройди по карте и отметь, что узнал у себя.\n\n{diag} диагнозов по органам бизнеса, у каждого признаки, цена проблемы в ₸ и первые шаги.\n\nУзнал три и больше: значит, они связаны, и лечить нужно корень.\n\nКарта бесплатная, сохрани и пройди с командой.",
			"Собственник видит симптомы: денег нет, люди уходят, заявки пропадают.\n\nЗа каждым симптомом стоит диагноз. Мы собрали {diag} таких диагнозов в одну карту по органам бизнеса.\n\nОткрой, найди свои и посмотри первые шаги. Бесплатно, без регистрации.",
		},
		Give: "🗺 Карта {diag} диагнозов бизнеса открыта для вас.\n\nПройдите по органам и отметьте, что узнали у себя. Каждая карточка открывает признаки, причины, цену проблемы и первые шаги.",
		Warm: "Вы смотрели карту диагнозов. Если узнали у себя 3 и больше, они почти всегда связаны: лечить нужно корень."},
	{ID: "paycal", Title: "Платёжный календарь на 8 недель", Short: "Шаблон PDF: кассовый разрыв видно заранее", Icon: "📅", Kind: "pdf", Path: "seed_tl_0", Weight: 1,
		CTA: "Шаблон бесплатно: ",
		Posts: [3]string{
			"Бесплатно отдаю шаблон платёжного календаря на 8 недель.\n\nВнутри: движение денег по дням, постоянные платежи, минимальный остаток и порядок обновления по понедельникам за 25 минут.\n\nКрасная строка покажет кассовый разрыв за недели до него, пока ещё можно сдвинуть платёж.\n\nPDF придёт в Telegram сразу.",
			"Кассовый разрыв почти всегда видно заранее. Нужна одна таблица.\n\nПлатёжный календарь показывает остаток денег на каждый день следующих 8 недель. Красный день видно раньше, чем поставщик позвонит с вопросом об оплате.\n\nОтдаём наш шаблон, по которому работают резиденты клуба. Бесплатно, в PDF.",
			"Занимал у родственников в пятницу вечером, чтобы закрыть зарплату?\n\nЭто лечится одной таблицей на 25 минут в неделю. Платёжный календарь: все поступления и платежи на 8 недель вперёд и остаток на каждый день.\n\nШаблон в PDF, бесплатно. Заполни в понедельник.",
		},
		Give: "📅 Платёжный календарь на 8 недель: шаблон в PDF выше.\n\nПостоянные платежи заполните один раз, поступления и остаток обновляйте по понедельникам, это 25 минут.",
		Warm: "Вы забрали платёжный календарь. Заполните его в понедельник: 25 минут, и красные дни станут видны заранее."},
	{ID: "script", Title: "Скрипт продаж на одной странице", Short: "Шаблон PDF: опорные точки, возражения, проверка звонка", Icon: "📞", Kind: "pdf", Path: "seed_tl_3", Weight: 1,
		CTA: "Шаблон бесплатно: ",
		Posts: [3]string{
			"Бесплатно отдаю шаблон скрипта продаж на одной странице.\n\nОпорные точки разговора, возражения с ответами и лист проверки звонка.\n\nСобираешь его из речи своего лучшего продавца, и остальные менеджеры начинают продавать ближе к его уровню.\n\nPDF придёт в Telegram.",
			"Клиент говорит «дорого». Что отвечает твой менеджер?\n\nЕсли каждый раз по-разному, продажи зависят от настроения. Скрипт на одной странице фиксирует опорные точки разговора и ответы на частые возражения.\n\nШаблон бесплатный, в PDF. Заполняется за вечер вместе с лучшим продавцом.",
			"Один менеджер закрывает каждую третью сделку, другой каждую десятую. Разница почти всегда в разговоре.\n\nСкрипт продаж собирает разговор лучшего в одну страницу: этапы, вопросы, возражения, проверка звонка.\n\nОтдаём шаблон бесплатно, PDF придёт в Telegram.",
		},
		Give: "📞 Скрипт продаж на одной странице: шаблон в PDF выше.\n\nСоберите его из одного записанного звонка вашего лучшего продавца: опорные точки, возражения, проверка звонка.",
		Warm: "Вы забрали скрипт продаж. Запишите один звонок лучшего продавца и разложите его по опорным точкам шаблона."},
	{ID: "org", Title: "Оргструктура: функции и хозяева", Short: "Шаблон PDF: кто за что отвечает вместо вас", Icon: "🧩", Kind: "pdf", Path: "seed_tl_6", Weight: 1,
		CTA: "Шаблон бесплатно: ",
		Posts: [3]string{
			"Бесплатно отдаю шаблон оргструктуры: функции и хозяева.\n\nСхема как есть, все функции бизнеса с одним ответственным на каждую, блоки, руководители и план запуска.\n\nСразу видно, сколько вопросов замыкается на тебе.\n\nPDF придёт в Telegram.",
			"Посчитай, сколько вопросов за день приходит лично тебе.\n\nЕсли их десятки, бизнес держится на собственнике, и отпуск превращается в работу по телефону.\n\nОргструктура на одной странице раздаёт каждую функцию одному хозяину. Шаблон бесплатный, в PDF.",
			"Уехать на 10 дней без ежедневных звонков. Для этого нужна одна страница.\n\nОргструктура: какие функции есть в бизнесе, кто хозяин каждой и к кому идти с вопросом вместо тебя.\n\nОтдаём наш шаблон бесплатно. PDF придёт в Telegram сразу.",
		},
		Give: "🧩 Оргструктура «функции и хозяева»: шаблон в PDF выше.\n\nНачните со схемы «как есть»: выпишите, кто сейчас за что отвечает и какие вопросы приходят к вам.",
		Warm: "Вы забрали шаблон оргструктуры. Начните со схемы «как есть»: выпишите, кто сейчас за что отвечает."},
	{ID: "guides", Title: "99 чек-листов для собственника", Short: "Деньги, продажи, команда: по 15 минут на каждый", Icon: "📘", Kind: "app", Path: "checklists", Weight: 2,
		CTA: "Забрать бесплатно: ",
		Posts: [3]string{
			"99 чек-листов для собственника, бесплатно.\n\nДеньги, продажи, найм, маркетинг, команда, кризис. В каждом история с цифрами, шаги и чек-лист, который проходишь за 15 минут.\n\nЛежат в приложении BS в Telegram, читаются с телефона, прогресс сохраняется.",
			"После 700 разборов мы упаковали самые частые решения в 99 чек-листов.\n\nКак найти кассовый разрыв, поднять средний чек, нанять без ошибок, выйти из операционки.\n\nКаждый проходится за 15 минут. Бесплатно, в Telegram.",
			"Проверь бизнес по чек-листу за 15 минут. Их у нас 99.\n\nОтмечаешь пункты и сразу видишь, где теряются деньги: финансы, продажи, команда, маркетинг.\n\nОтдаём бесплатно, открываются в Telegram с телефона.",
		},
		Give: "", Warm: ""},
	{ID: "gallup", Title: "34 таланта Gallup для собственника", Short: "Продажи, команда, деньги, найм и слепые зоны каждого таланта", Icon: "🧬", Kind: "page", Path: "/free/gallup", Weight: 1,
		CTA: "Справочник бесплатно: ",
		Posts: [3]string{
			"34 таланта Gallup глазами собственника. Бесплатно.\n\nКак каждый талант проявляется в продажах, команде, деньгах и найме, в чём его слепая зона и что выжигает.\n\nПлюс эксперимент на 10 дней под каждый талант.\n\nЕсли у тебя есть отчёт CliftonStrengths, найди свою пятёрку и сохрани.",
			"Твоя сильная сторона может тормозить бизнес.\n\n«Достигатор» закрывает 30 задач за день, а главная сдвигается на неделю. Темп есть, приоритета нет.\n\nМы разобрали все 34 таланта Gallup для собственника: продажи, команда, деньги, найм, слепые зоны и эксперимент на 10 дней.\n\nСправочник бесплатный.",
			"Кого нанять в пару к себе, подсказывает твой отчёт Gallup.\n\nСлабое Исполнение: рядом нужен операционный директор, который доводит до результата без напоминаний. Слабое стратегическое мышление: аналитик, который приносит цифры и сценарии.\n\nМы собрали справочник по 34 талантам и 4 доменам для собственника. Бесплатно.",
		},
		Give: "🧬 Справочник «34 таланта Gallup для собственника» открыт для вас.\n\nНайдите свою пятёрку: как каждый талант работает в продажах, команде, деньгах и найме, его слепая зона и эксперимент на 10 дней.",
		Warm: "Вы смотрели справочник по талантам Gallup. Выберите эксперимент на 10 дней для своего первого таланта и отметьте результат."},
	{ID: "plan10", Title: "Шаблон плана на 10 дней", Short: "Цикл резидентов клуба: одна цифра, 2-3 задачи, разбор", Icon: "🗓", Kind: "page", Path: "/free/plan10", Weight: 1,
		CTA: "Шаблон бесплатно: ",
		Posts: [3]string{
			"Бесплатно отдаю шаблон плана на 10 дней, по которому работают резиденты клуба.\n\nОдна цель с цифрой, 2-3 задачи, которые её двигают, ответственный и срок на каждую, отметка по дням и разбор на 10-й день.\n\nРаспечатай или заполни с телефона. Внутри пример заполнения.",
			"Почему планы на год редко доживают до весны? Год слишком длинный, чтобы вовремя заметить ошибку.\n\nМы работаем циклами по 10 дней: одна цифра, 2-3 задачи, ежедневная отметка, разбор.\n\nОтдаём шаблон такого цикла бесплатно, с примером кофейни, которая поднимала средний чек.",
			"За 10 дней можно сдвинуть одну цифру бизнеса: средний чек, конверсию заявок, остаток денег.\n\nДля этого нужна одна страница: цель, 2-3 задачи с ответственными, отметка по дням и разбор в конце.\n\nШаблон бесплатный. Начни цикл в понедельник.",
		},
		Give: "🗓 Шаблон плана на 10 дней открыт для вас: распечатайте или сохраните в PDF прямо со страницы.\n\nОдна цифра, 2-3 задачи с ответственными, отметка по дням и разбор на 10-й день. Внутри пример заполнения.",
		Warm: "Вы забрали шаблон плана на 10 дней. Выберите одну цифру и начните цикл в понедельник."},
}

var leadMagnetByID = func() map[string]*leadMagnet {
	m := map[string]*leadMagnet{}
	for i := range leadMagnetList {
		m[leadMagnetList[i].ID] = &leadMagnetList[i]
	}
	return m
}()

// mgFill puts the library's real counts into a text.
func mgFill(s string) string {
	d, t, i := content.Scale()
	return strings.NewReplacer("{ideas}", content.Num(i), "{ideas1m}", content.Num(web.IdeaPackCount("1m")),
		"{almaty}", content.Num(web.IdeaPackCount("almaty")), "{diag}", content.Num(d), "{tools}", content.Num(t)).Replace(s)
}

func (m *leadMagnet) title() string { return mgFill(m.Title) }

// ── Links and codes ──

var (
	mgParamRe = regexp.MustCompile(`^mg_([a-z0-9]+)(?:_(p\d{10}))?$`)
	mgCodeRe  = regexp.MustCompile(`^([a-z0-9]+)(?:-(p\d{10}))?$`)
	mgURLRe   = regexp.MustCompile(`(?:https?://)?[a-z0-9.-]+/m/[a-z0-9]+(?:-p\d{10})?`)
)

// parseMagnetParam: mg_ideas_p2610101230 → ideas, p2610101230.
func parseMagnetParam(p string) (*leadMagnet, string, bool) {
	m := mgParamRe.FindStringSubmatch(p)
	if m == nil || leadMagnetByID[m[1]] == nil {
		return nil, "", false
	}
	return leadMagnetByID[m[1]], m[2], true
}

func magnetStartParam(id, post string) string {
	if post != "" {
		return "mg_" + id + "_" + post
	}
	return "mg_" + id
}

// magnetSource: the CRM label of a magnet start.
func magnetSource(m *leadMagnet, post string) string {
	if l := thPostSource(post); l != "" {
		return strings.Replace(l, "Threads: пост", "Threads: магнит «"+m.title()+"», пост", 1)
	}
	return "Магнит: «" + m.title() + "»"
}

// magnetHost: app.bxclub.kz (the open pages' host, without the scheme).
func magnetHost() string {
	h := strings.TrimPrefix(strings.TrimPrefix(web.SiteURL, "https://"), "http://")
	return strings.TrimRight(h, "/")
}

// magnetURL: the post's link: the server counts the click, then the bot.
func magnetURL(id, post string) string {
	u := magnetHost() + "/m/" + id
	if post != "" {
		u += "-" + post
	}
	return u
}

// magnetize gives a magnet post its link for its slot (idempotent: the
// link moves with the post when «Другой пост» swaps slots).
func magnetize(it *contentItem) {
	m := leadMagnetByID[it.Magnet]
	if m == nil {
		return
	}
	post := strings.TrimPrefix(thPostParam(it), "th_")
	u := magnetURL(m.ID, post)
	if mgURLRe.MatchString(it.Text) {
		it.Text = mgURLRe.ReplaceAllString(it.Text, u)
	} else {
		it.Text = strings.TrimSpace(it.Text) + "\n\n" + m.CTA + u
	}
	it.Parts = nil
	it.Link = contentBotLink + magnetStartParam(m.ID, post)
	it.CTA = true
}

// magnetPost: the variant v of a magnet as a post (without the link yet).
func magnetPost(m *leadMagnet, v int) (body, full string, probs []string) {
	body = threadsClean(mgFill(m.Posts[v%3]))
	full = body + "\n\n" + m.CTA + magnetURL(m.ID, "p0000000000")
	probs = threadsQuality(body, full, nil)
	return
}

// ── The rubric in the day's plan ──

const (
	threadsMagnetDef = 38 // % of the day's posts: 4 a day → 1 or 2
	threadsMagnetMax = 50
	threadsMagnetCap = 4 // even 16 posts a day get at most 4 magnets
)

// threadsMagnet: the magnets' share, %: unset 38, 0 off, at most 50.
func (s contentSettings) threadsMagnet() int {
	p := s.Channels.Threads.MagnetPct
	if p == nil {
		return threadsMagnetDef
	}
	if *p <= 0 {
		return 0
	}
	if *p > threadsMagnetMax {
		return threadsMagnetMax
	}
	return *p
}

// threadsMagnetCount: magnets among n posts of the day key: the fraction
// goes to some days (4 a day at 38%: 1 or 2, about half the days 2).
func threadsMagnetCount(n int, key string, pct int) int {
	if pct <= 0 || n < 2 {
		return 0
	}
	x := float64(n*pct) / 100
	m := int(x)
	if frac := x - float64(m); frac > 0 && float64(hashN(key+"/mg", 1000))/1000 < frac {
		m++
	}
	if m < 1 {
		m = 1
	}
	if m > threadsMagnetCap {
		m = threadsMagnetCap
	}
	if m > n/2 {
		m = n / 2
	}
	return m
}

// threadsDayPlanMg: the day's plan with the magnet slots: the rest is the
// reach pyramid of n-m posts; a day with magnets has no other CTA (the
// magnets are the call). isMg marks the magnet slots.
func threadsDayPlanMg(n int, key string, pct, mgPct int) (formats []string, cta, isMg []bool) {
	m := threadsMagnetCount(n, key, mgPct)
	if m == 0 {
		f, c := threadsDayPlan(n, key, pct)
		return f, c, make([]bool, n)
	}
	isMg = make([]bool, n)
	if m == 1 {
		isMg[1+hashN(key+"/mgpos", n-1)%minI(2, n-1)] = true // the 2nd or the 3rd post
	} else {
		off := n / (2 * m)
		for k := 0; k < m; k++ {
			isMg[(k*n)/m+off] = true
		}
	}
	rest, _ := threadsDayPlan(n-m, key, pct)
	formats, cta = make([]string, n), make([]bool, n)
	j := 0
	for i := 0; i < n; i++ {
		if isMg[i] {
			formats[i] = "magnet"
			continue
		}
		formats[i] = rest[j]
		j++
	}
	return formats, cta, isMg
}

// pickMagnets: a magnet per slot: the longest unused weighted first, never
// twice a day; the variant used longest ago that passes the gate.
func pickMagnets(slots int, mem *threadsMemory, key string, now time.Time) []*contentItem {
	last := map[string]time.Time{}
	lastV := map[string]time.Time{}
	for src, t := range mem.src {
		rest, ok := strings.CutPrefix(src, "mg:")
		if !ok {
			continue
		}
		id, _, _ := strings.Cut(rest, "/")
		if t.After(last[id]) {
			last[id] = t
		}
		lastV[rest] = t
	}
	used := map[string]bool{}
	var out []*contentItem
	for k := 0; k < slots; k++ {
		var best *leadMagnet
		bestSc := math.Inf(-1) // R81: a magnet planned later in the week scores below zero
		for i := range leadMagnetList {
			m := &leadMagnetList[i]
			if used[m.ID] {
				continue
			}
			days := 60.0
			if t, ok := last[m.ID]; ok {
				days = now.Sub(t).Hours() / 24
				if days > 60 {
					days = 60
				}
			}
			sc := days*float64(m.Weight) + float64(hashN(key+m.ID, 100))/1000
			if sc > bestSc {
				best, bestSc = m, sc
			}
		}
		if best == nil {
			break
		}
		used[best.ID] = true
		vs := []int{0, 1, 2}
		sort.SliceStable(vs, func(a, b int) bool {
			ta, tb := lastV[fmt.Sprintf("%s/%d", best.ID, vs[a])], lastV[fmt.Sprintf("%s/%d", best.ID, vs[b])]
			if !ta.Equal(tb) {
				return ta.Before(tb)
			}
			return hashN(key+strconv.Itoa(vs[a]), 97) < hashN(key+strconv.Itoa(vs[b]), 97)
		})
		var it *contentItem
		for _, v := range vs {
			body, _, probs := magnetPost(best, v)
			if len(probs) > 0 {
				log.Printf("threads: magnet %s/%d refused: %s", best.ID, v, strings.Join(probs, "; "))
				continue
			}
			it = &contentItem{contentItemData: contentItemData{Src: fmt.Sprintf("mg:%s/%d", best.ID, v), V: v, Title: best.title(), Organ: "Лид-магнит",
				Text: body, Format: "magnet", Gen: "mg", Magnet: best.ID, CTA: true}}
			break
		}
		if it != nil {
			out = append(out, it)
		}
	}
	return out
}

// ── The click: GET /m/:code ──

var (
	mgClickMu   sync.Mutex
	mgClickSeen = map[string]time.Time{}
)

var mgBotUA = regexp.MustCompile(`(?i)bot|crawl|spider|preview|facebookexternalhit|meta-external|whatsapp|telegram|slack|discord|curl|wget|python|go-http`)

// MagnetClick counts a click of a post's link and sends the person to the bot.
func (f *LeadFunnel) MagnetClick(c *gin.Context) {
	code := strings.ToLower(c.Param("code"))
	mm := mgCodeRe.FindStringSubmatch(code)
	bot := "https://t.me/bsurgery_bot"
	if mm == nil || leadMagnetByID[mm[1]] == nil {
		c.Redirect(http.StatusFound, bot+"?start=threads")
		return
	}
	id, post := mm[1], mm[2]
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")
	ua := c.GetHeader("User-Agent")
	if c.Request.Method == http.MethodGet && !mgBotUA.MatchString(ua) && f.docs != nil {
		k := c.ClientIP() + "|" + code
		now := f.now()
		mgClickMu.Lock()
		dup := now.Sub(mgClickSeen[k]) < 30*time.Minute
		mgClickSeen[k] = now
		if len(mgClickSeen) > 5000 {
			for x, t := range mgClickSeen {
				if now.Sub(t) > time.Hour {
					delete(mgClickSeen, x)
				}
			}
		}
		mgClickMu.Unlock()
		if !dup {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := f.noteClick(ctx, id, post, now); err != nil {
					log.Printf("magnets: click %s: %v", code, err)
				}
			}()
		}
	}
	c.Redirect(http.StatusFound, bot+"?start="+magnetStartParam(id, post))
}

const magnetDoc = "bs_magnets"

// noteClick: bs_magnets {clicks:{<id>:{n, days:{20261010:n}, posts:{p…:n}}}}.
func (f *LeadFunnel) noteClick(ctx context.Context, id, post string, now time.Time) error {
	day := now.In(almaty).Format("20060102")
	return f.mutate(ctx, magnetDoc, func(doc map[string]any) bool {
		clicks, _ := doc["clicks"].(map[string]any)
		if clicks == nil {
			clicks = map[string]any{}
			doc["clicks"] = clicks
		}
		m, _ := clicks[id].(map[string]any)
		if m == nil {
			m = map[string]any{}
			clicks[id] = m
		}
		m["n"] = anyInt(m["n"]) + 1
		days, _ := m["days"].(map[string]any)
		if days == nil {
			days = map[string]any{}
			m["days"] = days
		}
		days[day] = anyInt(days[day]) + 1
		if len(days) > 120 { // keep about four months
			var ks []string
			for k := range days {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks[:len(ks)-120] {
				delete(days, k)
			}
		}
		if post != "" {
			posts, _ := m["posts"].(map[string]any)
			if posts == nil {
				posts = map[string]any{}
				m["posts"] = posts
			}
			posts[post] = anyInt(posts[post]) + 1
			if len(posts) > 300 {
				var ks []string
				for k := range posts {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				for _, k := range ks[:len(ks)-300] {
					delete(posts, k)
				}
			}
		}
		return true
	})
}

// ── The bot: the material at once ──

// startMagnet answers /start mg_<id>[_p…]: the lead in the CRM with the
// magnet, the material, and for a new lead the 2-minute check.
func (f *LeadFunnel) startMagnet(ctx context.Context, st startInfo, m *leadMagnet, post string) bool {
	src := magnetSource(m, post)
	isNew, repeat, err := f.ensureLead(ctx, st.ChatID, st.FirstName, st.LastName, st.Username, src,
		"Пришёл за магнитом «"+m.title()+"» ("+src+")", "Снова пришёл за магнитом «"+m.title()+"» ("+src+")", true)
	if err != nil {
		log.Printf("funnel: crm: %v", err)
		f.replyFail(ctx, st.ChatID, "CRM недоступна: "+err.Error())
		return false
	}
	if repeat {
		f.markHandled(ctx, st.ChatID)
		return true
	}
	f.noteMagnet(ctx, st.ChatID, m.ID, post)
	if m.Kind == "app" { // the 99 checklists: the usual welcome is this magnet
		if err := f.sendWelcome(ctx, st.ChatID, st.FirstName, "99"); err != nil {
			f.replyFail(ctx, st.ChatID, err.Error())
			return false
		}
		if isNew {
			_ = f.sendPainAsk(ctx, st.ChatID)
		}
		f.replyOK(ctx, st.ChatID, "магнит «"+m.title()+"»")
		return true
	}
	if err := f.giveMagnet(ctx, st.ChatID, m); err != nil {
		log.Printf("funnel: magnet %s → %d: %v", m.ID, st.ChatID, err)
		f.replyFail(ctx, st.ChatID, err.Error())
		return false
	}
	if isNew || m.Kind == "quiz" { // the check in the chat: the diagnoses' magnet is it
		_ = f.sendPainAsk(ctx, st.ChatID)
	}
	f.replyOK(ctx, st.ChatID, "магнит «"+m.title()+"»")
	return true
}

type startInfo struct {
	ChatID                        int64
	FirstName, LastName, Username string
}

func magnetPageURL(m *leadMagnet) string { return web.SiteURL + m.Path }

// giveMagnet sends the material (a page button or the PDF).
func (f *LeadFunnel) giveMagnet(ctx context.Context, chatID int64, m *leadMagnet) error {
	text := mgFill(m.Give)
	more := row(map[string]any{"text": "🎁 Ещё бесплатные материалы", "callback_data": "mg_menu"})
	switch m.Kind {
	case "pdf":
		keys := kb(row(map[string]any{"text": "🧰 Все " + content.Num(scaleTools()) + " инструментов с шаблонами", "url": web.SiteURL + "/library"}), more)
		if f.Doc != nil {
			if t, pdf, _, err := TemplatePDF(m.Path); err == nil && t != nil && len(pdf) > 0 {
				_, name := tplFileNames(t.Template.Title)
				if err := f.Doc(ctx, chatID, "tpl_"+m.Path, name, pdf, "", text, keys); err == nil {
					return nil
				} else {
					log.Printf("funnel: magnet pdf %s: %v", m.Path, err)
				}
			}
		}
		keys = kb(row(map[string]any{"text": "📄 Открыть шаблон (PDF)", "url": web.SiteURL + "/t/" + m.Path + ".pdf"}), more)
		return f.send(ctx, chatID, strings.Replace(text, "в PDF выше", "в PDF по кнопке", 1), keys)
	case "quiz":
		return f.send(ctx, chatID, text, kb(row(map[string]any{"text": "🗺 Карта всех диагнозов", "url": magnetPageURL(m)})))
	}
	rows := [][]map[string]any{row(map[string]any{"text": m.Icon + " Открыть: " + m.title(), "url": magnetPageURL(m)})}
	if strings.HasPrefix(m.Path, "/ideas") {
		var r []map[string]any
		for _, p := range []struct{ pack, label string }{{"1m", "До 1 млн ₸"}, {"almaty", "Алматы"}, {"home", "Из дома"}} {
			if !strings.Contains(m.Path, "pack="+p.pack) {
				r = append(r, map[string]any{"text": p.label, "url": web.SiteURL + "/ideas?pack=" + p.pack})
			}
		}
		rows = append(rows, r)
	}
	rows = append(rows, more)
	return f.send(ctx, chatID, text, map[string]any{"inline_keyboard": rows})
}

func scaleTools() int { _, t, _ := content.Scale(); return t }

// magnetMenu: every magnet as a button (/start's «Ещё бесплатно»).
func (f *LeadFunnel) magnetMenu(ctx context.Context, chatID int64) error {
	var rows [][]map[string]any
	for i := range leadMagnetList {
		m := &leadMagnetList[i]
		b := map[string]any{"text": m.Icon + " " + m.title(), "callback_data": "mg_get_" + m.ID}
		if m.Kind == "app" {
			b = f.appBtn(m.Icon+" "+m.title(), "checklists")
		}
		rows = append(rows, row(b))
	}
	return f.send(ctx, chatID, "🎁 Бесплатные материалы Business Surgery. Выберите, что забрать:", map[string]any{"inline_keyboard": rows})
}

// magnetCallback: mg_menu and mg_get_<id>.
func (f *LeadFunnel) magnetCallback(ctx context.Context, chatID int64, first, username, data string) bool {
	if data == "mg_menu" {
		return f.magnetMenu(ctx, chatID) == nil
	}
	m := leadMagnetByID[strings.TrimPrefix(data, "mg_get_")]
	if m == nil {
		return f.magnetMenu(ctx, chatID) == nil
	}
	_, _, _ = f.ensureLead(ctx, chatID, first, "", username, magnetSource(m, ""), "Взял магнит «"+m.title()+"» из меню бота", "Взял магнит «"+m.title()+"» из меню бота", false)
	f.noteMagnet(ctx, chatID, m.ID, "")
	if m.Kind == "app" {
		return f.sendWelcome(ctx, chatID, first, "99") == nil
	}
	if err := f.giveMagnet(ctx, chatID, m); err != nil {
		log.Printf("funnel: magnet %s → %d: %v", m.ID, chatID, err)
		return false
	}
	if m.Kind == "quiz" {
		_ = f.sendPainAsk(ctx, chatID)
	}
	return true
}

// noteMagnet: the lead's magnets (mgAt: id → first time, mgPost: id → the
// post's code); the first one is the lead's magnet.
func (f *LeadFunnel) noteMagnet(ctx context.Context, chatID int64, id, post string) {
	now := f.now()
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		lead := findLeadByTg(asList(crm["leads"]), chatID)
		if lead == nil {
			return false
		}
		if s, _ := lead["magnet"].(string); s == "" {
			lead["magnet"] = id
		}
		at, _ := lead["mgAt"].(map[string]any)
		if at == nil {
			at = map[string]any{}
			lead["mgAt"] = at
		}
		if _, ok := at[id]; ok {
			return true
		}
		at[id] = now.UTC().Format(time.RFC3339)
		if post != "" {
			mp, _ := lead["mgPost"].(map[string]any)
			if mp == nil {
				mp = map[string]any{}
				lead["mgPost"] = mp
			}
			mp[id] = post
		}
		return true
	})
}

// magnetWarm: the first warm-up touch of a magnet lead opens with its line.
func magnetWarm(id string) string {
	if m := leadMagnetByID[id]; m != nil {
		return m.Warm
	}
	return ""
}

// LeadMagnets: the magnets for the lead's home (open links, no login).
func LeadMagnets(botName string) []gin.H {
	var out []gin.H
	for i := range leadMagnetList {
		m := &leadMagnetList[i]
		u := ""
		switch m.Kind {
		case "page", "quiz":
			u = magnetPageURL(m)
		case "pdf":
			u = web.SiteURL + "/t/" + m.Path + ".pdf"
		case "app":
			u = "https://t.me/" + botName + "?start=mg_guides"
		}
		out = append(out, gin.H{"id": m.ID, "title": m.title(), "short": m.Short, "icon": m.Icon, "url": u})
	}
	return out
}

// ── Маркетинг: посты → клики → старты → лиды → разборы ──

type MagnetVariant struct {
	V      int    `json:"v"`
	First  string `json:"first"`
	Posts  int    `json:"posts"`
	Pub    int    `json:"pub"`
	Clicks int    `json:"clicks"`
	Starts int    `json:"starts"`
}

type MagnetRow struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Icon     string          `json:"icon"`
	URL      string          `json:"url"`
	Posts    int             `json:"posts"`  // offered to the owner in the period (sent or planned in the past)
	Pub      int             `json:"pub"`    // published (marked)
	Clicks   int             `json:"clicks"` // clicks on the posts' links
	Starts   int             `json:"starts"` // took the magnet in the bot (new and known people)
	Leads    int             `json:"leads"`  // came to the bot with this magnet first
	Quiz     int             `json:"quiz"`   // of them started the 2-minute check
	Booked   int             `json:"booked"`
	Razbor   int             `json:"razbor"`
	Won      int             `json:"won"`
	Next     string          `json:"next,omitempty"` // the next planned post (RFC3339)
	Variants []MagnetVariant `json:"variants"`
}

type MagnetStat struct {
	Days   int         `json:"days"`
	Rows   []MagnetRow `json:"rows"`
	Total  MagnetRow   `json:"total"`
	Share  int         `json:"share"`  // % of Threads posts that are magnets (setting)
	PerDay int         `json:"perDay"` // manual posts a day
}

// MagnetStats: the magnets of the last days days.
func (f *LeadFunnel) MagnetStats(ctx context.Context, days int) (*MagnetStat, error) {
	if days <= 0 {
		days = 30
	}
	now := f.now()
	from := now.Add(-time.Duration(days) * 24 * time.Hour)
	fromDay := from.In(almaty).Format("20060102")
	rows := map[string]*MagnetRow{}
	postV := map[string]map[string]int{} // id → post code → variant
	var order []string
	for i := range leadMagnetList {
		m := &leadMagnetList[i]
		r := &MagnetRow{ID: m.ID, Title: m.title(), Icon: m.Icon, URL: magnetPageURL(m)}
		for v := 0; v < 3; v++ {
			body, _, _ := magnetPost(m, v)
			r.Variants = append(r.Variants, MagnetVariant{V: v, First: content.FirstLine(body, 90)})
		}
		rows[m.ID] = r
		postV[m.ID] = map[string]int{}
		order = append(order, m.ID)
	}
	st := &MagnetStat{Days: days}
	// posts
	if cd, err := f.docs.GetDoc(ctx, "club", contentKey); err == nil && cd != nil && !cd.Deleted {
		var d contentDoc
		if json.Unmarshal([]byte(cd.Value), &d) == nil {
			s := parseContentSettings(d.Settings)
			st.Share, st.PerDay = s.threadsMagnet(), s.manualPerDay()
			if threadsValueOnly {
				st.Share = threadsValueMgPct
			}
			for _, list := range [][]*contentItem{d.History, d.Queue} {
				for _, it := range list {
					r := rows[it.Magnet]
					if r == nil || it.Channel != "threads" || it.Gen == "val" { // R81: a «польза» post only links to the magnet
						continue
					}
					at, ok := parseContentAt(it.At)
					if !ok {
						continue
					}
					v := it.V % 3
					if code := strings.TrimPrefix(thPostParam(it), "th_"); code != "" {
						postV[it.Magnet][code] = v
					}
					if at.After(now) {
						if it.Status == "planned" || it.Status == "approved" {
							if r.Next == "" || it.At < r.Next {
								r.Next = it.At
							}
						}
						continue
					}
					if at.Before(from) || it.Status == "skipped" || it.Status == "failed" {
						continue
					}
					r.Posts++
					r.Variants[v].Posts++
					if it.Status == "published" {
						r.Pub++
						r.Variants[v].Pub++
					}
				}
			}
		}
	}
	// clicks
	if md, err := f.docs.GetDoc(ctx, "club", magnetDoc); err == nil && md != nil && !md.Deleted {
		var doc struct {
			Clicks map[string]struct {
				Days  map[string]int `json:"days"`
				Posts map[string]int `json:"posts"`
			} `json:"clicks"`
		}
		_ = json.Unmarshal([]byte(md.Value), &doc)
		for id, c := range doc.Clicks {
			r := rows[id]
			if r == nil {
				continue
			}
			for day, n := range c.Days {
				if day >= fromDay {
					r.Clicks += n
				}
			}
			for code, n := range c.Posts {
				if v, ok := postV[id][code]; ok && code[1:7] >= fromDay[2:] {
					r.Variants[v].Clicks += n
				}
			}
		}
	}
	// starts and leads
	if cd, err := f.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && cd != nil && !cd.Deleted {
		var crm map[string]any
		_ = json.Unmarshal([]byte(cd.Value), &crm)
		for _, x := range asList(crm["leads"]) {
			l, _ := x.(map[string]any)
			if l == nil {
				continue
			}
			at, _ := l["mgAt"].(map[string]any)
			mp, _ := l["mgPost"].(map[string]any)
			col := fmt.Sprint(l["col"])
			booked := l["razborSlot"] != nil || col == "meet" || col == "diag" || col == "decide" || col == "later" || col == "won"
			razbor := l["pz"] != nil || col == "diag" || col == "decide" || col == "later" || col == "won"
			for id, ts := range at {
				r := rows[id]
				t, err := time.Parse(time.RFC3339, fmt.Sprint(ts))
				if r == nil || err != nil || t.Before(from) {
					continue
				}
				r.Starts++
				if code, ok := mp[id].(string); ok {
					if v, ok := postV[id][code]; ok {
						r.Variants[v].Starts++
					}
				}
			}
			first, _ := l["magnet"].(string)
			r := rows[first]
			if r == nil {
				continue
			}
			if t, ok := leadStart(l); !ok || t.Before(from) || !strings.Contains(fmt.Sprint(l["source"]), "агнит") {
				continue
			}
			r.Leads++
			if qz, _ := l["qz"].(map[string]any); len(qz) > 0 {
				r.Quiz++
			}
			if booked {
				r.Booked++
			}
			if razbor {
				r.Razbor++
			}
			if col == "won" {
				r.Won++
			}
		}
	}
	for _, id := range order {
		r := rows[id]
		st.Rows = append(st.Rows, *r)
		t := &st.Total
		t.Posts += r.Posts
		t.Pub += r.Pub
		t.Clicks += r.Clicks
		t.Starts += r.Starts
		t.Leads += r.Leads
		t.Quiz += r.Quiz
		t.Booked += r.Booked
		t.Razbor += r.Razbor
		t.Won += r.Won
	}
	st.Total.Title = "Все магниты"
	return st, nil
}

// MagnetStatsHTTP: GET /api/v1/platform/sales/funnel/magnets?days=30
func (f *LeadFunnel) MagnetStatsHTTP(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days <= 0 || days > 366 {
		days = 30
	}
	s, err := f.MagnetStats(c.Request.Context(), days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, s)
}

// magnetPlanLine: the next days' plan in one log line (deploy check).
func magnetPlanLine(st contentSettings, now time.Time) string {
	var parts []string
	n := st.threadsPerDay()
	for d := 0; d < 4; d++ {
		day := now.AddDate(0, 0, d)
		if !st.dayOn("threads", day) {
			continue
		}
		f, _, _ := threadsDayPlanMg(n, contentDay(day), st.threadsReach(), st.threadsMagnet())
		if threadsValueOnly {
			f, _ = threadsValuePlan(n, contentDay(day))
		}
		parts = append(parts, contentDay(day)+" ["+strings.Join(f, " ")+"]")
	}
	return strings.Join(parts, "; ")
}

// splitHook: the AI's post whose first line runs past 120 signs gets its
// first sentence on a line of its own (the gate refused all of them on
// 08.10 and 09.10 for that).
func splitHook(s string) string {
	first, rest, _ := strings.Cut(s, "\n")
	if utf8.RuneCountInString(first) <= 120 {
		return s
	}
	r := []rune(first)
	for i := 20; i < len(r)-1 && i < 120; i++ {
		if (r[i] == '.' || r[i] == '?' || r[i] == '!' || r[i] == ':') && r[i+1] == ' ' {
			head := string(r[:i+1])
			out := strings.TrimSpace(head) + "\n\n" + strings.TrimSpace(string(r[i+2:]))
			if rest != "" {
				out += "\n" + rest
			}
			return out
		}
	}
	return s
}
