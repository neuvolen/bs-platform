package http

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R75 at start: the owner's two events of 10.10.2026, each applied once.
//
//  1. «Плюс один клиент: Амирхан, пришёл по сарафанному радио, и мы бонус
//     резиденту выплатим». A referral bonus «к выплате» for Амирхан
//     (resident since 09.10). The referrer is taken from his CRM lead (the
//     ref link, ref/refName) or «Кто привёл?»; unknown: the record waits
//     for «Кто привёл?» in his resident card.
//  2. «Пришёл клиент от таргета на бизнес-завтрак, но мы его не успели
//     собрать. Он оплатил 10к, и мы предложили ему полноценный разбор за
//     50к, но за 10к, и по итогу он оставил предоплату». The breakfast
//     payment (ДДС 06.10, 10 000 ₸, «БХ МК/Завтрак», no name) moves to
//     «БХ Экспресс разбор»; the lead gets «Особая цена» 10 000 ₸ on the
//     разбор, the moved payment, a placeholder for the prepayment (sum
//     unknown) and the stage «Предоплата за разбор». The lead is the one
//     CRM lead from the target about the breakfast; none or several: a new
//     card «Клиент с таргета (бизнес-завтрак)» the team renames.

const (
	R75RefKey      = "r75-amirkhan-2026-10-10"
	R75RefResident = "Амирхан"
	r75Mark        = "r75_breakfast_case" // club meta: the breakfast case is done
	R75LeadID      = "l-r75-bb-target"
	R75PayMoved    = "r75-bb-pay"
	R75PayPrepay   = "r75-prepay"
)

// The breakfast payment as the logs show it (ДДС #355).
var r75Breakfast = struct {
	ID     int64
	Date   string
	Amount int64
	Cat    string
}{355, "2026-10-06", 10000, "БХ МК/Завтрак"}

type r75Meta interface {
	Meta(ctx context.Context, key string) string
	MetaSet(ctx context.Context, key, value string) error
}

// SalesOpsAtStart runs both, quietly when already done.
func SalesOpsAtStart(ctx context.Context, b *SalesBook, meta r75Meta) {
	if b == nil || b.Docs == nil {
		return
	}
	if b.Ledger != nil {
		if m, err := b.Ledger.Master(ctx); err != nil || m != "server" {
			return // the sheet keeps the data: nothing is written yet
		}
	}
	r75Referral(ctx, b)
	if meta != nil && meta.Meta(ctx, r75Mark) != "" {
		return
	}
	if done := r75BreakfastCase(ctx, b); done && meta != nil {
		if err := meta.MetaSet(ctx, r75Mark, b.now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("r75 breakfast: mark: %v", err)
		}
	}
}

// r75Referral: the payout for Амирхан, with the referrer when the CRM knows it.
func r75Referral(ctx context.Context, b *SalesBook) {
	now := b.now()
	var tg int64
	if b.Residents != nil {
		if rs, err := b.Residents(ctx); err == nil {
			for _, r := range rs {
				if club.NormName(r.Name) == club.NormName(R75RefResident) {
					tg = r.TgID
				}
			}
		}
	}
	crm := readDoc(ctx, b.Docs, "club", "bs_crm")
	leads, _ := crm["leads"].([]any)
	var lead map[string]any
	if tg != 0 {
		lead = findLeadByTg(leads, tg)
	}
	if lead == nil {
		for _, x := range leads {
			if m, _ := x.(map[string]any); m != nil && club.NormName(firstName(sStr(m, "name"))) == club.NormName(R75RefResident) {
				lead = m
				break
			}
		}
	}
	p := RefPayout{Key: R75RefKey, Resident: R75RefResident, Note: "Сарафанное радио: владелец 10.10.2026 «мы бонус резиденту выплатим». Резидент с 09.10.", RemindAt: now.In(club.Almaty).Format("2006-01-02")}
	src := "не найден в CRM"
	if lead != nil {
		p.LeadID = sStr(lead, "id")
		src = "лид в CRM найден"
		if lead["ref"] != nil && lead["refGuest"] != true {
			p.Referrer, p.ReferrerTg = sStr(lead, "refName"), anyInt(lead["ref"])
			if p.Referrer == "" {
				p.Referrer = fmt.Sprintf("id %d", p.ReferrerTg)
			}
		}
	}
	got, created, err := EnsurePayout(ctx, b.Docs, p, now)
	if err != nil {
		log.Printf("r75 referral: %v", err)
		return
	}
	if !created {
		return
	}
	who := got.Referrer
	if who == "" {
		who = "неизвестно, ждёт «Кто привёл?» в карточке резидента"
	} else {
		who = firstName(who)
	}
	log.Printf("r75 referral: %s (%s): бонус %s ₸ к выплате, кто привёл: %s", R75RefResident, src, club.FmtMoney(got.Amount), who)
	if p.LeadID != "" {
		_ = docMutate(ctx, b.Docs, "club", "bs_crm", "server:r75", func(crm map[string]any) bool {
			l := leadByID(crm, p.LeadID)
			if l == nil {
				return false
			}
			if sStr(l, "col") != "won" {
				l["col"] = "won"
			}
			l["wordOfMouth"] = true
			l["next"] = "Выплатить реферальный бонус"
			l["nextAt"] = got.RemindAt
			addLog(l, now, "Стал резидентом 09.10. Пришёл по сарафанному радио: реферальный бонус "+tenge(got.Amount)+" к выплате (Продажи → Рефералы)")
			return true
		})
	}
}

var r75Target = regexp.MustCompile(`(?i)таргет|target|tilda|тильда|сайт|лендинг|utm|instagram|инстаграм|реклам`)

// r75FindLead: the one lead from the target that is about the breakfast.
func r75FindLead(crm map[string]any) (map[string]any, []string) {
	leads, _ := crm["leads"].([]any)
	var hits []map[string]any
	var names []string
	for _, x := range leads {
		m, _ := x.(map[string]any)
		if m == nil || sStr(m, "col") == "won" || sStr(m, "col") == "lost" {
			continue
		}
		if sStr(m, "id") == R75LeadID || leadPay(m, R75PayMoved) != nil {
			return m, nil
		}
		txt := sStr(m, "source") + " " + sStr(m, "note") + " " + sStr(m, "funnel") + " " + sStr(m, "goal")
		if lg, ok := m["log"].([]any); ok {
			for _, e := range lg {
				if em, _ := e.(map[string]any); em != nil {
					txt += " " + sStr(em, "text")
				}
			}
		}
		low := strings.ToLower(txt)
		if !strings.Contains(low, "завтрак") || !r75Target.MatchString(sStr(m, "source")+" "+sStr(m, "imp")) {
			continue
		}
		hits = append(hits, m)
		names = append(names, firstName(sStr(m, "name")))
	}
	if len(hits) == 1 {
		return hits[0], names
	}
	return nil, names
}

// r75BreakfastRow: the 10 000 ₸ breakfast payment of 06.10 (or the row already moved).
func r75BreakfastRow(rows []pg.DDSRow) *pg.DDSRow {
	razbor, _ := leadProduct("razbor")
	for i := range rows {
		r := &rows[i]
		if r.ID == r75Breakfast.ID && r.Income == r75Breakfast.Amount && (r.IncomeCat == r75Breakfast.Cat || r.IncomeCat == razbor.Cat) {
			return r
		}
	}
	var hit *pg.DDSRow
	n := 0
	for i := range rows {
		r := &rows[i]
		if r.Date == r75Breakfast.Date && r.Income == r75Breakfast.Amount && r.IncomeCat == r75Breakfast.Cat && strings.TrimSpace(r.Resident) == "" {
			hit = r
			n++
		}
	}
	if n == 1 {
		return hit
	}
	return nil
}

// r75BreakfastCase: true when the case is fully in the system.
func r75BreakfastCase(ctx context.Context, b *SalesBook) bool {
	now := b.now()
	if b.Ledger == nil {
		return false
	}
	rows, err := b.Ledger.DDSRows(ctx)
	if err != nil {
		log.Printf("r75 breakfast: ДДС: %v", err)
		return false
	}
	row := r75BreakfastRow(rows)
	if row == nil {
		log.Printf("r75 breakfast: строка ДДС 06.10 10 000 ₸ «%s» не найдена, ничего не меняю", r75Breakfast.Cat)
		return false
	}
	razbor, _ := leadProduct("razbor")
	note := "бизнес-завтрак не собрали, оплата засчитана в экспресс-разбор (особая цена 10 000 ₸)"
	if row.IncomeCat != razbor.Cat {
		if err := b.moveDDS(ctx, row.ID, razbor, note, "r75", now); err != nil {
			log.Printf("r75 breakfast: ДДС #%d: %v", row.ID, err)
			return false
		}
		log.Printf("r75 breakfast: ДДС #%d 06.10 10 000 ₸: «%s» → «%s»", row.ID, r75Breakfast.Cat, razbor.Cat)
	}
	var leadName, how string
	err = docMutate(ctx, b.Docs, "club", "bs_crm", "server:r75", func(crm map[string]any) bool {
		l, names := r75FindLead(crm)
		how = "найден лид с таргета"
		if l == nil {
			how = fmt.Sprintf("лидов с таргета про завтрак: %d (%s), заведена новая карточка", len(names), strings.Join(names, ", "))
			leads, _ := crm["leads"].([]any)
			l = map[string]any{"id": R75LeadID, "col": "new", "name": "Клиент с таргета (бизнес-завтрак)", "phone": "", "tg": "",
				"source": "Таргет: бизнес-завтрак", "niche": "", "sum": "", "date": now.In(club.Almaty).Format("02.01.2006"),
				"note": "Пришёл с таргета на бизнес-завтрак 08.10, оплатил 10 000 ₸ (Kaspi, 06.10). Завтрак не собрали: предложили полноценный экспресс-разбор (50 000 ₸) за 10 000 ₸. Оставил предоплату. Имя, телефон и сумму предоплаты впишите в карточку."}
			addLog(l, now, "Карточка заведена по словам владельца 10.10: клиент с таргета, оплатил бизнес-завтрак")
			crm["leads"] = append([]any{l}, leads...)
		}
		leadName = firstName(sStr(l, "name"))
		if leadPay(l, R75PayMoved) == nil {
			ps := leadPays(l)
			ps = append(ps, map[string]any{"id": R75PayMoved, "product": "breakfast", "amount": r75Breakfast.Amount, "date": "06.10.2026", "ddsId": row.ID, "status": "paid", "note": "Kaspi, бизнес-завтрак"})
			setLeadPays(l, ps)
			if _, _, err := MovePayment(l, R75PayMoved, "razbor", "бизнес-завтрак не собрали", now); err != nil {
				log.Printf("r75 breakfast: move: %v", err)
			}
		}
		if leadPay(l, R75PayPrepay) == nil {
			ps := leadPays(l)
			ps = append(ps, map[string]any{"id": R75PayPrepay, "product": "razbor", "amount": 0, "status": "placeholder", "note": "Предоплата за разбор: сумму и дату впишите в карточке («Указать сумму»)"})
			setLeadPays(l, ps)
			addLog(l, now, "Оставил предоплату за разбор: сумма пока не указана")
		}
		if ds, _ := l["deals"].(map[string]any); ds == nil || ds["razbor"] == nil {
			if err := SetSpecialPrice(l, "razbor", 10000, "Не успели собрать бизнес-завтрак: полноценный разбор за 10 000 ₸ вместо 50 000 ₸, оплата завтрака засчитана", "r75", now); err != nil {
				log.Printf("r75 breakfast: special: %v", err)
			}
		}
		if c := sStr(l, "col"); c != "won" && c != "meet" && c != "diag" && c != "decide" {
			l["col"] = "prepay"
		}
		if sStr(l, "next") == "" {
			l["next"], l["nextAt"] = "Вписать сумму предоплаты и назначить разбор", now.In(club.Almaty).Format("2006-01-02")
		}
		return true
	})
	if err != nil {
		log.Printf("r75 breakfast: CRM: %v", err)
		return false
	}
	log.Printf("r75 breakfast: лид «%s» (%s): «Предоплата за разбор», особая цена 10 000 ₸, оплата завтрака перенесена в разбор, предоплата ждёт суммы", leadName, how)
	return true
}
