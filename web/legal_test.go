package web

import (
	"strings"
	"testing"
)

// Google Auth Platform (Branding): the home page app.bxclub.kz links the
// privacy policy and the terms; both open without a login on the same domain;
// the policy says what Google user data is used, why, how it is stored and
// deleted, that it is not shared, and carries the Limited Use statement.
func TestLegalPagesForGoogleOAuth(t *testing.T) {
	r := publicRouter()
	pv := get(t, r, "/privacy")
	if pv.Code != 200 || !strings.HasPrefix(pv.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/privacy: %d", pv.Code)
	}
	body := pv.Body.String()
	for _, want := range []string{
		"Политика конфиденциальности",
		`<link rel="canonical" href="https://app.bxclub.kz/privacy">`,
		"https://www.googleapis.com/auth/calendar.events",
		"https://www.googleapis.com/auth/calendar.readonly",
		"https://www.googleapis.com/auth/calendar.app.created",
		"Limited Use",
		"Google API Services User Data Policy",
		"https://developers.google.com/terms/api-services-user-data-policy",
		"Отключить Google Календарь",
		"myaccount.google.com/permissions",
		"Мы не передаём данные Google третьим лицам",
		"не передаём в модели искусственного интеллекта",
		`href="/terms"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/privacy lacks %q", want)
		}
	}
	tm := get(t, r, "/terms")
	if tm.Code != 200 || !strings.Contains(tm.Body.String(), "Условия использования") || !strings.Contains(tm.Body.String(), `href="/privacy#google"`) {
		t.Fatalf("/terms: %d", tm.Code)
	}
	for _, p := range []*string{&body} {
		if strings.Contains(*p, "—") {
			t.Error("em dash on a legal page")
		}
	}
	if strings.Contains(tm.Body.String(), "—") {
		t.Error("em dash on /terms")
	}
	// the home page (login page for a visitor) links both
	home := get(t, r, "/")
	if home.Code != 200 || !strings.Contains(home.Body.String(), `href="/privacy"`) || !strings.Contains(home.Body.String(), `href="/terms"`) {
		t.Fatalf("home page must link the policy and the terms: %d", home.Code)
	}
	// /about's footer too; robots and sitemap know them; invite and personal links stay closed
	if a := get(t, r, "/about").Body.String(); !strings.Contains(a, `href="/privacy"`) {
		t.Error("/about footer lacks the policy")
	}
	rb := get(t, r, "/robots.txt").Body.String()
	for _, want := range []string{"Allow: /privacy", "Allow: /terms", "Disallow: /assist/", "Disallow: /in/"} {
		if !strings.Contains(rb, want) {
			t.Errorf("robots.txt lacks %q", want)
		}
	}
	if sm := get(t, r, "/sitemap.xml").Body.String(); !strings.Contains(sm, "https://app.bxclub.kz/privacy") || !strings.Contains(sm, "https://app.bxclub.kz/terms") {
		t.Error("sitemap lacks the legal pages")
	}
}

func TestLegalContactEmail(t *testing.T) {
	t.Setenv("PUBLIC_CONTACT_EMAIL", "")
	if strings.Contains(legalContacts(), "mailto:") {
		t.Fatal("no e-mail without PUBLIC_CONTACT_EMAIL")
	}
	t.Setenv("PUBLIC_CONTACT_EMAIL", "privacy@example.com")
	if !strings.Contains(legalContacts(), "mailto:privacy@example.com") {
		t.Fatal("e-mail from PUBLIC_CONTACT_EMAIL")
	}
}
