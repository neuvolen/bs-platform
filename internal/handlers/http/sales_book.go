package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R75: деньги вокруг лидов и рефералов, которые раньше жили только на словах.
//
//   - Реферальные бонусы (server doc ref_payouts): запись «к выплате» на
//     каждого, кто стал резидентом по приглашению или по сарафанному радио
//     (резидент неизвестен: «Кто привёл?» в карточке резидента). Сумма из
//     настроек продаж (refBonus, по умолчанию 100 000 ₸). «Выплачено» пишет
//     расход в ДДС (статья «Реферальный бонус») и закрывает запись. Через
//     2 дня после начисления команда получает одно напоминание в бот.
//   - Оплаты лида (bs_crm, lead.payments): что лид уже заплатил и за какой
//     продукт. «Перенести оплату»: платёж засчитывается в другой продукт
//     (строка ДДС меняет статью, в комментарии остаётся откуда). «Особая
//     цена»: цена продукта для этого лида (и в его записи на разбор).
//
// Team only (/api/v1/club):
//
//	GET  /referral/payouts                    {bonus, items}
//	POST /referral/payouts/:id/referrer       {referrer}
//	POST /referral/payouts/:id/paid           {date?, account?}
//	POST /referral/pay-lead                   {leadId}: the lead's bonus, paid
//	GET  /referral/who?resident=              {referrer, payout}
//	POST /referral/who                        {resident, referrer}
//	GET  /leads/:id/payments                  {payments, deals}
//	POST /leads/:id/payments                  {payId?, product, amount, date?, note?}
//	POST /leads/:id/payments/move             {payId, to, note?}
//	POST /leads/:id/special-price             {product, price, reason}

const (
	refPayoutsDoc  = "ref_payouts" // scope server
	RefBonusCat    = "Реферальный бонус"
	refRemindAfter = 2 * 24 * time.Hour
)

// LeadProduct: what a lead can pay for, with its ДДС income category.
type LeadProduct struct {
	ID, Name, Cat string
	Price         int64
}

var LeadProducts = []LeadProduct{
	{ID: "breakfast", Name: "Бизнес-завтрак", Cat: "БХ МК/Завтрак", Price: 10000},
	{ID: "razbor", Name: "Экспресс-разбор", Cat: "БХ Экспресс разбор", Price: razborPrice},
}

func leadProduct(id string) (LeadProduct, bool) {
	for _, p := range LeadProducts {
		if p.ID == id {
			return p, true
		}
	}
	return LeadProduct{}, false
}

// salesLedger: the cash journal (ClubRepo).
type salesLedger interface {
	Master(ctx context.Context) (string, error)
	DDSRows(ctx context.Context) ([]pg.DDSRow, error)
	DDSApply(ctx context.Context, ops []pg.DDSOp, who string, now time.Time) (*pg.DDSResult, error)
}

// SalesBook keeps the payouts and the leads' payments.
type SalesBook struct {
	Docs      funnelDocs
	Ledger    salesLedger
	Residents func(ctx context.Context) ([]club.Resident, error)
	Send      func(ctx context.Context, chat int64, text string, kb map[string]any) error
	Admins    []int64
	Now       func() time.Time
	Refresh   func(ctx context.Context) // the platform's club data after a ДДС write
}

func (b *SalesBook) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// ErrBook is a request the book does not take (shown to the team as is).
type ErrBook struct{ Msg string }

func (e *ErrBook) Error() string { return e.Msg }

func bkErr(msg string) error { return &ErrBook{msg} }

func docMutate(ctx context.Context, docs funnelDocs, scope, key, by string, fn func(doc map[string]any) bool) error {
	for try := 0; try < 8; try++ {
		doc := map[string]any{}
		base := 0
		d, err := docs.GetDoc(ctx, scope, key)
		if err != nil {
			return err
		}
		if d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &doc)
			}
			if doc == nil {
				doc = map[string]any{}
			}
		}
		if !fn(doc) {
			return nil
		}
		val, _ := json.Marshal(doc)
		if _, err = docs.PutDoc(ctx, scope, key, base, string(val), false, by); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(5+try*7) * time.Millisecond):
		}
	}
	return pg.ErrPlatformConflict
}

func readDoc(ctx context.Context, docs funnelDocs, scope, key string) map[string]any {
	out := map[string]any{}
	if d, err := docs.GetDoc(ctx, scope, key); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// RefBonusAmount: the bonus from the sales settings (refBonus), else RefBonus.
func RefBonusAmount(ctx context.Context, docs funnelDocs) int64 {
	if docs == nil {
		return RefBonus
	}
	if v := anyInt(readDoc(ctx, docs, "server", salesSettingsKey)["refBonus"]); v > 0 {
		return v
	}
	return RefBonus
}

// ── реферальные бонусы ──

// RefPayout is one bonus: due («к выплате») or paid.
type RefPayout struct {
	ID         string `json:"id"`
	Key        string `json:"key,omitempty"` // the event that made it (once per key)
	Resident   string `json:"resident"`      // who became a resident
	LeadID     string `json:"leadId,omitempty"`
	Referrer   string `json:"referrer"` // who brought them; "" = unknown («Кто привёл?»)
	ReferrerTg int64  `json:"referrerTg,omitempty"`
	Amount     int64  `json:"amount"`
	Status     string `json:"status"` // due | paid
	Note       string `json:"note,omitempty"`
	CreatedAt  string `json:"createdAt"`
	RemindAt   string `json:"remindAt,omitempty"` // 2006-01-02 (Almaty)
	Reminded   string `json:"reminded,omitempty"`
	PaidAt     string `json:"paidAt,omitempty"`
	PaidBy     string `json:"paidBy,omitempty"`
	DDSID      int64  `json:"ddsId,omitempty"`
}

type refBook struct {
	Items []RefPayout       `json:"items"`
	Who   map[string]string `json:"who"` // NormName(resident) → who brought them
}

func loadRefBook(doc map[string]any) *refBook {
	b := &refBook{Who: map[string]string{}}
	raw, _ := json.Marshal(doc)
	_ = json.Unmarshal(raw, b)
	if b.Who == nil {
		b.Who = map[string]string{}
	}
	if b.Items == nil {
		b.Items = []RefPayout{}
	}
	return b
}

func (rb *refBook) store(doc map[string]any) {
	raw, _ := json.Marshal(rb)
	for k := range doc {
		delete(doc, k)
	}
	_ = json.Unmarshal(raw, &doc)
}

func (rb *refBook) find(id string) *RefPayout {
	for i := range rb.Items {
		if rb.Items[i].ID == id {
			return &rb.Items[i]
		}
	}
	return nil
}

// same: the payout is about the same new resident (by lead, or by name).
func (p RefPayout) same(q RefPayout) bool {
	if p.Key != "" && p.Key == q.Key {
		return true
	}
	if p.LeadID != "" && p.LeadID == q.LeadID {
		return true
	}
	return p.Resident != "" && club.NormName(p.Resident) == club.NormName(q.Resident)
}

// EnsurePayout adds the payout once (by key, lead or resident). A known
// referrer fills an existing record that had none. created: a new record.
func EnsurePayout(ctx context.Context, docs funnelDocs, p RefPayout, now time.Time) (got RefPayout, created bool, err error) {
	if p.Amount <= 0 {
		p.Amount = RefBonusAmount(ctx, docs)
	}
	err = docMutate(ctx, docs, "server", refPayoutsDoc, "server:referral", func(doc map[string]any) bool {
		created = false
		rb := loadRefBook(doc)
		for i := range rb.Items {
			it := &rb.Items[i]
			if !it.same(p) {
				continue
			}
			got = *it
			if it.Referrer == "" && p.Referrer != "" {
				it.Referrer, it.ReferrerTg = p.Referrer, p.ReferrerTg
				if it.LeadID == "" {
					it.LeadID = p.LeadID
				}
				got = *it
				rb.store(doc)
				return true
			}
			return false
		}
		if p.Referrer == "" {
			p.Referrer = rb.Who[club.NormName(p.Resident)]
		}
		p.ID = fmt.Sprintf("rp%d", now.UnixNano()%1e12)
		p.Status = "due"
		p.CreatedAt = now.UTC().Format(time.RFC3339)
		if p.RemindAt == "" {
			p.RemindAt = now.Add(refRemindAfter).In(club.Almaty).Format("2006-01-02")
		}
		rb.Items = append(rb.Items, p)
		rb.store(doc)
		got, created = p, true
		return true
	})
	return
}

// Payouts: due first (oldest first), then paid (newest first).
func (b *SalesBook) Payouts(ctx context.Context) []RefPayout {
	rb := loadRefBook(readDoc(ctx, b.Docs, "server", refPayoutsDoc))
	out := append([]RefPayout{}, rb.Items...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == "paid") != (out[j].Status == "paid") {
			return out[i].Status != "paid"
		}
		if out[i].Status == "paid" {
			return out[i].PaidAt > out[j].PaidAt
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// SetReferrer: «Кто привёл?» on a payout (and for the resident).
func (b *SalesBook) SetReferrer(ctx context.Context, id, referrer string) (RefPayout, error) {
	referrer = clip(strings.TrimSpace(referrer), 120)
	var got RefPayout
	found := false
	err := docMutate(ctx, b.Docs, "server", refPayoutsDoc, "server:referral", func(doc map[string]any) bool {
		rb := loadRefBook(doc)
		p := rb.find(id)
		if p == nil {
			found = false
			return false
		}
		found = true
		p.Referrer = referrer
		if p.Resident != "" {
			if referrer == "" {
				delete(rb.Who, club.NormName(p.Resident))
			} else {
				rb.Who[club.NormName(p.Resident)] = referrer
			}
		}
		got = *p
		rb.store(doc)
		return true
	})
	if err == nil && !found {
		err = bkErr("выплата не найдена")
	}
	return got, err
}

// SetWho: «Кто привёл?» on the resident card; the resident's open payout gets it too.
func (b *SalesBook) SetWho(ctx context.Context, resident, referrer string) error {
	resident, referrer = strings.TrimSpace(resident), clip(strings.TrimSpace(referrer), 120)
	if resident == "" {
		return bkErr("нужен резидент")
	}
	return docMutate(ctx, b.Docs, "server", refPayoutsDoc, "server:referral", func(doc map[string]any) bool {
		rb := loadRefBook(doc)
		k := club.NormName(resident)
		if referrer == "" {
			delete(rb.Who, k)
		} else {
			rb.Who[k] = referrer
		}
		for i := range rb.Items {
			if club.NormName(rb.Items[i].Resident) == k && rb.Items[i].Status != "paid" {
				rb.Items[i].Referrer = referrer
			}
		}
		rb.store(doc)
		return true
	})
}

// Who: who brought the resident and their payout.
func (b *SalesBook) Who(ctx context.Context, resident string) (string, *RefPayout) {
	rb := loadRefBook(readDoc(ctx, b.Docs, "server", refPayoutsDoc))
	k := club.NormName(resident)
	var p *RefPayout
	for i := range rb.Items {
		if club.NormName(rb.Items[i].Resident) == k {
			x := rb.Items[i]
			p = &x
		}
	}
	who := rb.Who[k]
	if who == "" && p != nil {
		who = p.Referrer
	}
	return who, p
}

func (b *SalesBook) editable(ctx context.Context) error {
	if b.Ledger == nil {
		return bkErr("ДДС недоступен")
	}
	if _, ok := ddsMaster(ctx, b.Ledger); !ok {
		return bkErr("ДДС пока только для просмотра")
	}
	return nil
}

func ddsDateArg(s string, now time.Time) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.In(club.Almaty).Format("02.01.2006"), nil
	}
	t, ok := pg.ParseDDSDate(s, now)
	if !ok {
		return "", bkErr("дата «" + s + "»: нужна в виде 10.10.2026")
	}
	return t.Format("02.01.2006"), nil
}

func rawJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// PayPayout: «Выплачено»: the expense goes into ДДС (once), the payout is closed.
func (b *SalesBook) PayPayout(ctx context.Context, id, date, account, who string) (RefPayout, error) {
	if err := b.editable(ctx); err != nil {
		return RefPayout{}, err
	}
	now := b.now()
	day, err := ddsDateArg(date, now)
	if err != nil {
		return RefPayout{}, err
	}
	var p *RefPayout
	for _, x := range b.Payouts(ctx) {
		if x.ID == id {
			x := x
			p = &x
		}
	}
	if p == nil {
		return RefPayout{}, bkErr("выплата не найдена")
	}
	if p.Status == "paid" {
		return *p, nil
	}
	if p.Amount <= 0 {
		return *p, bkErr("сумма бонуса не указана")
	}
	referrer := p.Referrer
	if referrer == "" {
		return *p, bkErr("сначала укажите, кто привёл")
	}
	comment := "Реферальный бонус: " + referrer + " за " + dashName(p.Resident)
	set := map[string]json.RawMessage{"date": rawJSON(day), "expense": rawJSON(p.Amount), "expenseCat": rawJSON(RefBonusCat), "comment": rawJSON(comment)}
	if a := strings.TrimSpace(account); a != "" {
		set["account"] = rawJSON(clip(a, 100))
	}
	res, err := b.Ledger.DDSApply(ctx, []pg.DDSOp{{Op: "insert", CID: "ref", Set: set}}, who, now)
	if err != nil {
		return *p, err
	}
	ddsID := res.IDs["ref"]
	var got RefPayout
	err = docMutate(ctx, b.Docs, "server", refPayoutsDoc, "server:referral", func(doc map[string]any) bool {
		rb := loadRefBook(doc)
		x := rb.find(id)
		if x == nil {
			return false
		}
		x.Status, x.PaidAt, x.PaidBy, x.DDSID = "paid", now.UTC().Format(time.RFC3339), who, ddsID
		got = *x
		rb.store(doc)
		return true
	})
	if err != nil {
		log.Printf("referral: payout %s paid (ДДС #%d), the record: %v", id, ddsID, err)
		return got, err
	}
	if got.LeadID != "" {
		_ = docMutate(ctx, b.Docs, "club", "bs_crm", "server:referral", func(crm map[string]any) bool {
			l := leadByID(crm, got.LeadID)
			if l == nil {
				return false
			}
			l["refPaid"], l["refPaidAt"] = true, now.UTC().Format(time.RFC3339)
			addLog(l, now, fmt.Sprintf("Реферальный бонус %s выплачен: %s (ДДС, статья «%s»)", tenge(got.Amount), referrer, RefBonusCat))
			return true
		})
	}
	if b.Refresh != nil {
		b.Refresh(ctx)
	}
	return got, nil
}

// PayLead: «Выплачено» on a referred lead of the Рефералы page.
func (b *SalesBook) PayLead(ctx context.Context, leadID, date, who string) (RefPayout, error) {
	crm := readDoc(ctx, b.Docs, "club", "bs_crm")
	l := leadByID(crm, leadID)
	if l == nil {
		return RefPayout{}, bkErr("лид не найден")
	}
	if sStr(l, "col") != "won" {
		return RefPayout{}, bkErr("бонус только за того, кто стал резидентом")
	}
	ref := sStr(l, "refName")
	if ref == "" && l["ref"] != nil {
		ref = "id " + strconv.FormatInt(anyInt(l["ref"]), 10)
	}
	p, _, err := EnsurePayout(ctx, b.Docs, RefPayout{Key: "lead:" + leadID, Resident: sStr(l, "name"), LeadID: leadID, Referrer: ref, ReferrerTg: anyInt(l["ref"])}, b.now())
	if err != nil {
		return p, err
	}
	return b.PayPayout(ctx, p.ID, date, "", who)
}

// RemindOnce: one message to the team per payout due since RemindAt (10:00-20:00).
func (b *SalesBook) RemindOnce(ctx context.Context) int {
	now := b.now()
	if b.Send == nil || len(b.Admins) == 0 || !salesWindow(now) {
		return 0
	}
	today := now.In(club.Almaty).Format("2006-01-02")
	var due []RefPayout
	err := docMutate(ctx, b.Docs, "server", refPayoutsDoc, "server:referral", func(doc map[string]any) bool {
		due = nil
		rb := loadRefBook(doc)
		for i := range rb.Items {
			it := &rb.Items[i]
			if it.Status == "paid" || it.Reminded != "" || it.RemindAt == "" || it.RemindAt > today {
				continue
			}
			it.Reminded = now.UTC().Format(time.RFC3339)
			due = append(due, *it)
		}
		if len(due) == 0 {
			return false
		}
		rb.store(doc)
		return true
	})
	if err != nil {
		log.Printf("referral: remind: %v", err)
		return 0
	}
	for _, p := range due {
		who := p.Referrer
		if who == "" {
			who = "не указано: впишите «Кто привёл?» в карточке резидента"
		}
		text := fmt.Sprintf("⏰ Реферальный бонус к выплате: %s.\nНовый резидент: %s\nПривёл: %s\nПосле выплаты: Продажи → Рефералы → «Выплачено» (расход сам попадёт в ДДС).", tenge(p.Amount), dashName(p.Resident), who)
		for _, a := range b.Admins {
			if err := b.Send(ctx, a, text, nil); err != nil {
				log.Printf("referral: remind %d: %v", a, err)
			}
		}
	}
	return len(due)
}

// RemindLoop: hourly.
func (b *SalesBook) RemindLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if n := b.RemindOnce(c); n > 0 {
			log.Printf("referral: %d payout reminders sent", n)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func dashName(s string) string {
	if strings.TrimSpace(s) == "" {
		return "без имени"
	}
	return s
}

// ── оплаты лида ──

func leadByID(crm map[string]any, id string) map[string]any {
	leads, _ := crm["leads"].([]any)
	for _, x := range leads {
		if m, _ := x.(map[string]any); m != nil && fmt.Sprint(m["id"]) == id {
			return m
		}
	}
	return nil
}

func leadPays(l map[string]any) []map[string]any {
	arr, _ := l["payments"].([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		if m, _ := x.(map[string]any); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func setLeadPays(l map[string]any, ps []map[string]any) {
	arr := make([]any, len(ps))
	for i, p := range ps {
		arr[i] = p
	}
	l["payments"] = arr
}

func leadPay(l map[string]any, id string) map[string]any {
	for _, p := range leadPays(l) {
		if fmt.Sprint(p["id"]) == id {
			return p
		}
	}
	return nil
}

func leadDeals(l map[string]any) map[string]any {
	d, _ := l["deals"].(map[string]any)
	if d == nil {
		d = map[string]any{}
		l["deals"] = d
	}
	return d
}

// LeadDeal: what the lead owes for one product.
type LeadDeal struct {
	Product  string `json:"product"`
	Name     string `json:"name"`
	Price    int64  `json:"price"`             // the list price
	Special  int64  `json:"special,omitempty"` // «Особая цена», 0: none
	Reason   string `json:"reason,omitempty"`
	Paid     int64  `json:"paid"`
	Due      int64  `json:"due"`
	Over     int64  `json:"over,omitempty"`
	Unknown  int    `json:"unknown,omitempty"` // payments whose sum nobody entered yet
	Price2Go int64  `json:"toPay"`             // the price that counts
}

// LeadSummary: per product the lead paid for or has a special price on.
func LeadSummary(l map[string]any) []LeadDeal {
	deals := map[string]*LeadDeal{}
	get := func(id string) *LeadDeal {
		if d := deals[id]; d != nil {
			return d
		}
		p, ok := leadProduct(id)
		if !ok {
			p = LeadProduct{ID: id, Name: id}
		}
		d := &LeadDeal{Product: id, Name: p.Name, Price: p.Price}
		deals[id] = d
		return d
	}
	ds, _ := l["deals"].(map[string]any)
	for id, v := range ds {
		m, _ := v.(map[string]any)
		d := get(id)
		if pr := anyInt(m["price"]); pr > 0 {
			d.Price = pr
		}
		if _, ok := m["special"]; ok && m["special"] != nil {
			d.Special = anyInt(m["special"])
			d.Reason, _ = m["reason"].(string)
		}
	}
	for _, p := range leadPays(l) {
		d := get(sStr(p, "product"))
		if sStr(p, "status") == "placeholder" {
			d.Unknown++
			continue
		}
		d.Paid += anyInt(p["amount"])
	}
	out := []LeadDeal{}
	for _, p := range LeadProducts {
		if d := deals[p.ID]; d != nil {
			out = append(out, *d)
			delete(deals, p.ID)
		}
	}
	for _, d := range deals {
		out = append(out, *d)
	}
	for i := range out {
		d := &out[i]
		d.Price2Go = d.Price
		if d.Special > 0 {
			d.Price2Go = d.Special
		}
		if d.Paid >= d.Price2Go {
			d.Over = d.Paid - d.Price2Go
		} else {
			d.Due = d.Price2Go - d.Paid
		}
	}
	return out
}

// MovePayment: the payment counts toward another product from now on.
func MovePayment(l map[string]any, payID, to, note string, now time.Time) (map[string]any, string, error) {
	p := leadPay(l, payID)
	if p == nil {
		return nil, "", bkErr("оплата не найдена")
	}
	dst, ok := leadProduct(to)
	if !ok {
		return nil, "", bkErr("неизвестный продукт")
	}
	from := sStr(p, "product")
	if from == to {
		return nil, "", bkErr("оплата уже засчитана в «" + dst.Name + "»")
	}
	src, _ := leadProduct(from)
	if src.Name == "" {
		src.Name = from
	}
	p["product"] = to
	p["movedFrom"] = from
	p["movedAt"] = now.UTC().Format(time.RFC3339)
	if n := strings.TrimSpace(note); n != "" {
		p["moveNote"] = clip(n, 300)
	}
	txt := fmt.Sprintf("Оплата %s перенесена: «%s» → «%s»", payAmountText(p), src.Name, dst.Name)
	if n := strings.TrimSpace(note); n != "" {
		txt += ". " + n
	}
	addLog(l, now, txt)
	return p, src.Name, nil
}

func payAmountText(p map[string]any) string {
	if sStr(p, "status") == "placeholder" {
		return "(сумма не указана)"
	}
	return tenge(anyInt(p["amount"]))
}

// SetSpecialPrice: «Особая цена» of a product for this lead; price 0 removes it.
func SetSpecialPrice(l map[string]any, product string, price int64, reason, who string, now time.Time) error {
	p, ok := leadProduct(product)
	if !ok {
		return bkErr("неизвестный продукт")
	}
	if price < 0 {
		return bkErr("цена не может быть меньше нуля")
	}
	reason = clip(strings.TrimSpace(reason), 300)
	ds := leadDeals(l)
	d, _ := ds[product].(map[string]any)
	if d == nil {
		d = map[string]any{"price": p.Price}
	}
	if price == 0 {
		delete(d, "special")
		delete(d, "reason")
		addLog(l, now, "Особая цена на «"+p.Name+"» снята: снова "+tenge(p.Price))
	} else {
		if reason == "" {
			return bkErr("напишите причину особой цены")
		}
		d["special"], d["reason"], d["setAt"], d["setBy"] = price, reason, now.UTC().Format(time.RFC3339), who
		addLog(l, now, fmt.Sprintf("Особая цена на «%s»: %s вместо %s. %s", p.Name, tenge(price), tenge(p.Price), reason))
	}
	ds[product] = d
	return nil
}

// AddPayment adds a payment, or fills a placeholder (payID) with its sum.
func AddPayment(l map[string]any, payID, product string, amount int64, day, note string, ddsID int64, now time.Time) (map[string]any, error) {
	pr, ok := leadProduct(product)
	if !ok {
		return nil, bkErr("неизвестный продукт")
	}
	if amount <= 0 {
		return nil, bkErr("сумма: целое число тенге больше нуля")
	}
	ps := leadPays(l)
	var p map[string]any
	if payID != "" {
		p = leadPay(l, payID)
		if p == nil {
			return nil, bkErr("оплата не найдена")
		}
		if sStr(p, "status") != "placeholder" {
			return nil, bkErr("сумма этой оплаты уже указана")
		}
	} else {
		p = map[string]any{"id": fmt.Sprintf("pay%d", now.UnixNano()%1e12)}
		ps = append(ps, p)
	}
	p["product"], p["amount"], p["date"], p["status"] = product, amount, day, "paid"
	if ddsID > 0 {
		p["ddsId"] = ddsID
	}
	if n := strings.TrimSpace(note); n != "" {
		p["note"] = clip(n, 300)
	}
	setLeadPays(l, ps)
	addLog(l, now, fmt.Sprintf("Оплата %s за «%s» от %s", tenge(amount), pr.Name, day))
	return p, nil
}

func (b *SalesBook) crmLead(ctx context.Context, id string, fn func(l map[string]any) error) (map[string]any, error) {
	var out map[string]any
	var ferr error
	err := docMutate(ctx, b.Docs, "club", "bs_crm", "server:sales", func(crm map[string]any) bool {
		out, ferr = nil, nil
		l := leadByID(crm, id)
		if l == nil {
			ferr = bkErr("лид не найден")
			return false
		}
		if ferr = fn(l); ferr != nil {
			return false
		}
		out = l
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, ferr
}

// ddsRow: one row of the journal by id.
func (b *SalesBook) ddsRow(ctx context.Context, id int64) (*pg.DDSRow, error) {
	rows, err := b.Ledger.DDSRows(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// Move: «Перенести оплату», with the ДДС row (its income category) when linked.
func (b *SalesBook) Move(ctx context.Context, leadID, payID, to, note, who string) (map[string]any, error) {
	now := b.now()
	crm := readDoc(ctx, b.Docs, "club", "bs_crm")
	l := leadByID(crm, leadID)
	if l == nil {
		return nil, bkErr("лид не найден")
	}
	p := leadPay(l, payID)
	if p == nil {
		return nil, bkErr("оплата не найдена")
	}
	dst, ok := leadProduct(to)
	if !ok {
		return nil, bkErr("неизвестный продукт")
	}
	if sStr(p, "product") == to {
		return nil, bkErr("оплата уже засчитана в «" + dst.Name + "»")
	}
	if id := anyInt(p["ddsId"]); id > 0 {
		if err := b.editable(ctx); err != nil {
			return nil, err
		}
		if err := b.moveDDS(ctx, id, dst, note, who, now); err != nil {
			return nil, err
		}
	}
	return b.crmLead(ctx, leadID, func(l map[string]any) error {
		_, _, err := MovePayment(l, payID, to, note, now)
		return err
	})
}

func (b *SalesBook) moveDDS(ctx context.Context, id int64, dst LeadProduct, note, who string, now time.Time) error {
	row, err := b.ddsRow(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return bkErr(fmt.Sprintf("строки ДДС #%d нет", id))
	}
	if row.IncomeCat == dst.Cat {
		return nil
	}
	c := strings.TrimSpace(row.Comment)
	add := "перенесено с «" + row.IncomeCat + "»"
	if n := strings.TrimSpace(note); n != "" {
		add += ": " + n
	}
	if c != "" {
		c += " · "
	}
	c = clip(c+add, 1000)
	_, err = b.Ledger.DDSApply(ctx, []pg.DDSOp{{Op: "update", ID: id, Set: map[string]json.RawMessage{"incomeCat": rawJSON(dst.Cat), "comment": rawJSON(c)}}}, who, now)
	if err == nil && b.Refresh != nil {
		b.Refresh(ctx)
	}
	return err
}

// Pay: a payment of the lead (or a placeholder's sum) with its ДДС income row.
func (b *SalesBook) Pay(ctx context.Context, leadID, payID, product string, amount int64, date, note, who string) (map[string]any, error) {
	now := b.now()
	pr, ok := leadProduct(product)
	if !ok {
		return nil, bkErr("неизвестный продукт")
	}
	if amount <= 0 {
		return nil, bkErr("сумма: целое число тенге больше нуля")
	}
	day, err := ddsDateArg(date, now)
	if err != nil {
		return nil, err
	}
	crm := readDoc(ctx, b.Docs, "club", "bs_crm")
	l := leadByID(crm, leadID)
	if l == nil {
		return nil, bkErr("лид не найден")
	}
	if payID != "" {
		p := leadPay(l, payID)
		if p == nil {
			return nil, bkErr("оплата не найдена")
		}
		if sStr(p, "status") != "placeholder" {
			return nil, bkErr("сумма этой оплаты уже указана")
		}
	}
	if err := b.editable(ctx); err != nil {
		return nil, err
	}
	comment := "Лид: " + dashName(sStr(l, "name")) + ", " + strings.ToLower(pr.Name)
	if n := strings.TrimSpace(note); n != "" {
		comment += ". " + n
	}
	res, err := b.Ledger.DDSApply(ctx, []pg.DDSOp{{Op: "insert", CID: "lp", Set: map[string]json.RawMessage{
		"date": rawJSON(day), "income": rawJSON(amount), "incomeCat": rawJSON(pr.Cat), "comment": rawJSON(clip(comment, 1000))}}}, who, now)
	if err != nil {
		return nil, err
	}
	ddsID := res.IDs["lp"]
	if b.Refresh != nil {
		b.Refresh(ctx)
	}
	return b.crmLead(ctx, leadID, func(l map[string]any) error {
		_, err := AddPayment(l, payID, product, amount, day, note, ddsID, now)
		return err
	})
}

// Special: «Особая цена», also on the lead's booked разбор.
func (b *SalesBook) Special(ctx context.Context, leadID, product string, price int64, reason, who string) (map[string]any, error) {
	now := b.now()
	l, err := b.crmLead(ctx, leadID, func(l map[string]any) error {
		return SetSpecialPrice(l, product, price, reason, who, now)
	})
	if err != nil || l == nil || product != "razbor" {
		return l, err
	}
	if slotID := sStr(l, "razborSlot"); slotID != "" {
		_ = docMutate(ctx, b.Docs, "club", slotsDoc, "server:sales", func(doc map[string]any) bool {
			list, _ := doc["slots"].([]any)
			for _, v := range list {
				s, ok := readSlot(v)
				if !ok || s.ID != slotID || s.booking() == nil {
					continue
				}
				bk := s.booking()
				if price > 0 {
					bk["specialPrice"], bk["specialReason"] = price, clip(strings.TrimSpace(reason), 300)
				} else {
					delete(bk, "specialPrice")
					delete(bk, "specialReason")
				}
				return true
			}
			return false
		})
	}
	return l, nil
}

// bookingPrice: the booking's «Особая цена», else the slots' price.
func bookingPrice(b map[string]any, price int64) int64 {
	if b != nil {
		if p := anyInt(b["specialPrice"]); p > 0 {
			return p
		}
	}
	return price
}

// ── HTTP ──

func (h *ClubHandler) book() *SalesBook {
	if h.Book != nil {
		return h.Book
	}
	if h.platform == nil {
		return nil
	}
	return &SalesBook{Docs: h.platform, Ledger: h.repo, Refresh: func(ctx context.Context) {
		if err := RefreshPlatformSeed(ctx, h.repo, h.platform, h.StaticSeed); err != nil {
			log.Printf("platform seed: %v", err)
		}
	}}
}

func registerSalesBook(g *gin.RouterGroup, h *ClubHandler) {
	g.GET("/referral/payouts", h.RefPayouts)
	g.POST("/referral/payouts/:id/referrer", h.RefPayoutReferrer)
	g.POST("/referral/payouts/:id/paid", h.RefPayoutPaid)
	g.POST("/referral/pay-lead", h.RefPayLead)
	g.GET("/referral/who", h.RefWho)
	g.POST("/referral/who", h.RefWhoSet)
	g.GET("/leads/:id/payments", h.LeadPayments)
	g.POST("/leads/:id/payments", h.LeadPayAdd)
	g.POST("/leads/:id/payments/move", h.LeadPayMove)
	g.POST("/leads/:id/special-price", h.LeadSpecial)
}

func (h *ClubHandler) bookOr503(c *gin.Context) *SalesBook {
	b := h.book()
	if b == nil || b.Docs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable", "detail": "хранилище платформы недоступно"})
		return nil
	}
	return b
}

func bookFail(c *gin.Context, err error, what string) {
	var be *ErrBook
	var de *pg.ErrDDSInput
	switch {
	case errors.As(err, &be):
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": be.Msg})
	case errors.As(err, &de):
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": de.Msg})
	default:
		log.Printf("%s: %v", what, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}

func (h *ClubHandler) logBook(c *gin.Context, action string, p map[string]string) {
	if h.repo == nil {
		return
	}
	if err := h.repo.LogOp(c.Request.Context(), pg.ClubOp{Source: "platform", Who: platformUser(c), Action: action, Params: p, OK: true}); err != nil {
		log.Printf("%s log: %v", action, err)
	}
}

// RefPayouts godoc
// @Summary  Referral bonuses: due and paid
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/referral/payouts [get]
func (h *ClubHandler) RefPayouts(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	ctx := c.Request.Context()
	items := b.Payouts(ctx)
	var due int64
	for _, p := range items {
		if p.Status != "paid" {
			due += p.Amount
		}
	}
	c.JSON(http.StatusOK, gin.H{"bonus": RefBonusAmount(ctx, b.Docs), "items": items, "due": due, "cat": RefBonusCat})
}

type refReferrerReq struct {
	Referrer string `json:"referrer"`
	Resident string `json:"resident"`
}

// RefPayoutReferrer godoc
// @Summary  «Кто привёл?» on a referral bonus
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/referral/payouts/{id}/referrer [post]
func (h *ClubHandler) RefPayoutReferrer(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req refReferrerReq
	_ = c.ShouldBindJSON(&req)
	p, err := b.SetReferrer(c.Request.Context(), c.Param("id"), req.Referrer)
	if err != nil {
		bookFail(c, err, "referral referrer")
		return
	}
	h.logBook(c, "refReferrer", map[string]string{"id": p.ID, "resident": p.Resident, "referrer": p.Referrer})
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": p})
}

type refPaidReq struct {
	Date    string `json:"date"`
	Account string `json:"account"`
	LeadID  string `json:"leadId"`
}

// RefPayoutPaid godoc
// @Summary  «Выплачено»: the bonus expense goes into ДДС (статья «Реферальный бонус»)
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/referral/payouts/{id}/paid [post]
func (h *ClubHandler) RefPayoutPaid(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req refPaidReq
	_ = c.ShouldBindJSON(&req)
	p, err := b.PayPayout(c.Request.Context(), c.Param("id"), req.Date, req.Account, platformUser(c))
	if err != nil {
		bookFail(c, err, "referral paid")
		return
	}
	h.logBook(c, "refPaid", map[string]string{"id": p.ID, "resident": p.Resident, "referrer": p.Referrer, "amount": strconv.FormatInt(p.Amount, 10), "dds": strconv.FormatInt(p.DDSID, 10)})
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": p})
}

// RefPayLead godoc
// @Summary  «Выплачено» on a referred lead that became a resident
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/referral/pay-lead [post]
func (h *ClubHandler) RefPayLead(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req refPaidReq
	_ = c.ShouldBindJSON(&req)
	p, err := b.PayLead(c.Request.Context(), strings.TrimSpace(req.LeadID), req.Date, platformUser(c))
	if err != nil {
		bookFail(c, err, "referral pay lead")
		return
	}
	h.logBook(c, "refPaid", map[string]string{"id": p.ID, "resident": p.Resident, "referrer": p.Referrer, "amount": strconv.FormatInt(p.Amount, 10), "dds": strconv.FormatInt(p.DDSID, 10)})
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": p})
}

// RefWho godoc
// @Summary  «Кто привёл?» of a resident, with their referral bonus
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/referral/who [get]
func (h *ClubHandler) RefWho(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	who, p := b.Who(c.Request.Context(), c.Query("resident"))
	c.JSON(http.StatusOK, gin.H{"referrer": who, "payout": p})
}

// RefWhoSet godoc
// @Summary  Set «Кто привёл?» of a resident
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/referral/who [post]
func (h *ClubHandler) RefWhoSet(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req refReferrerReq
	_ = c.ShouldBindJSON(&req)
	if err := b.SetWho(c.Request.Context(), req.Resident, req.Referrer); err != nil {
		bookFail(c, err, "referral who")
		return
	}
	h.logBook(c, "refWho", map[string]string{"resident": req.Resident, "referrer": req.Referrer})
	who, p := b.Who(c.Request.Context(), req.Resident)
	c.JSON(http.StatusOK, gin.H{"ok": true, "referrer": who, "payout": p})
}

func leadView(l map[string]any) gin.H {
	ps := leadPays(l)
	if ps == nil {
		ps = []map[string]any{}
	}
	return gin.H{"id": l["id"], "col": l["col"], "payments": ps, "deals": LeadSummary(l), "lead": l}
}

func productsView() []gin.H {
	out := []gin.H{}
	for _, p := range LeadProducts {
		out = append(out, gin.H{"id": p.ID, "name": p.Name, "cat": p.Cat, "price": p.Price})
	}
	return out
}

// LeadPayments godoc
// @Summary  The lead's payments and what is left to pay per product
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/leads/{id}/payments [get]
func (h *ClubHandler) LeadPayments(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	l := leadByID(readDoc(c.Request.Context(), b.Docs, "club", "bs_crm"), c.Param("id"))
	if l == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "detail": "лид не найден"})
		return
	}
	v := leadView(l)
	v["products"] = productsView()
	c.JSON(http.StatusOK, v)
}

type leadPayReq struct {
	PayID   string `json:"payId"`
	Product string `json:"product"`
	To      string `json:"to"`
	Amount  any    `json:"amount"`
	Price   any    `json:"price"`
	Date    string `json:"date"`
	Note    string `json:"note"`
	Reason  string `json:"reason"`
}

func moneyArg(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), x >= 0
	case string:
		if strings.TrimSpace(x) == "" {
			return 0, true
		}
		return club.Money(x)
	case nil:
		return 0, true
	}
	return 0, false
}

// LeadPayAdd godoc
// @Summary  A payment of the lead (or a placeholder's sum), with its ДДС income row
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/leads/{id}/payments [post]
func (h *ClubHandler) LeadPayAdd(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req leadPayReq
	_ = c.ShouldBindJSON(&req)
	amount, ok := moneyArg(req.Amount)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "сумма: только цифры"})
		return
	}
	l, err := b.Pay(c.Request.Context(), c.Param("id"), strings.TrimSpace(req.PayID), strings.TrimSpace(req.Product), amount, req.Date, req.Note, platformUser(c))
	if err != nil {
		bookFail(c, err, "lead pay")
		return
	}
	h.logBook(c, "leadPay", map[string]string{"lead": c.Param("id"), "product": req.Product, "amount": strconv.FormatInt(amount, 10)})
	c.JSON(http.StatusOK, gin.H{"ok": true, "view": leadView(l)})
}

// LeadPayMove godoc
// @Summary  «Перенести оплату»: the payment counts toward another product
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/leads/{id}/payments/move [post]
func (h *ClubHandler) LeadPayMove(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req leadPayReq
	_ = c.ShouldBindJSON(&req)
	l, err := b.Move(c.Request.Context(), c.Param("id"), strings.TrimSpace(req.PayID), strings.TrimSpace(req.To), req.Note, platformUser(c))
	if err != nil {
		bookFail(c, err, "lead pay move")
		return
	}
	h.logBook(c, "leadPayMove", map[string]string{"lead": c.Param("id"), "pay": req.PayID, "to": req.To})
	c.JSON(http.StatusOK, gin.H{"ok": true, "view": leadView(l)})
}

// LeadSpecial godoc
// @Summary  «Особая цена» of a product for the lead (0 removes it)
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/leads/{id}/special-price [post]
func (h *ClubHandler) LeadSpecial(c *gin.Context) {
	b := h.bookOr503(c)
	if b == nil {
		return
	}
	var req leadPayReq
	_ = c.ShouldBindJSON(&req)
	price, ok := moneyArg(req.Price)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "цена: только цифры"})
		return
	}
	l, err := b.Special(c.Request.Context(), c.Param("id"), strings.TrimSpace(req.Product), price, req.Reason, platformUser(c))
	if err != nil {
		bookFail(c, err, "lead special price")
		return
	}
	h.logBook(c, "leadSpecialPrice", map[string]string{"lead": c.Param("id"), "product": req.Product, "price": strconv.FormatInt(price, 10)})
	c.JSON(http.StatusOK, gin.H{"ok": true, "view": leadView(l)})
}
