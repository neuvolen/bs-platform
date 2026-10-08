package http

import (
	"context"
	"strings"
	"testing"
)

// R62: threads.com/share/<code> links from the app's «Поделиться» resolve to
// the canonical post (a redirect, or og:url), the rest of the text stays.
func TestThreadsShareLinks(t *testing.T) {
	got := ThreadsShareLinks("смотри threads.com/share/BBr-UwA_Ka/ и https://www.threads.net/share/OgOnly1 и ещё threads.com/share/BBr-UwA_Ka/")
	if len(got) != 2 || got[0] != "threads.com/share/BBr-UwA_Ka/" || got[1] != "https://www.threads.net/share/OgOnly1" {
		t.Fatalf("links: %q", got)
	}
	if _, ok := ParseThreadsURL("threads.com/share/BBr-UwA_Ka/"); ok {
		t.Fatal("a share link is not a post link")
	}
	f := newFakeThreads(t)
	tf := f.fetcher()
	ctx := context.Background()
	r, err := tf.ResolveShare(ctx, "threads.com/share/BBr-UwA_Ka/")
	if err != nil || r.URL != "https://www.threads.com/@my.twinkles/post/DcObaXSjPe_" || r.User != "my.twinkles" {
		t.Fatalf("redirect: %+v %v", r, err)
	}
	r, err = tf.ResolveShare(ctx, "https://www.threads.net/share/OgOnly1/")
	if err != nil || r.Code != "XYZ123abc_" {
		t.Fatalf("og:url: %+v %v", r, err)
	}
	if _, err := tf.ResolveShare(ctx, "threads.com/share/Nothing1/"); err == nil {
		t.Fatal("an empty page must not resolve")
	}
	txt := tf.ResolveShareLinks(ctx, "посмотри threads.com/share/BBr-UwA_Ka/ и threads.com/share/Nothing1/")
	if !strings.Contains(txt, "https://www.threads.com/@my.twinkles/post/DcObaXSjPe_") || !strings.Contains(txt, "threads.com/share/Nothing1/") {
		t.Fatalf("text: %s", txt)
	}
	if refs := ThreadsLinks(txt); len(refs) != 1 || refs[0].Code != "DcObaXSjPe_" {
		t.Fatalf("refs: %+v", refs)
	}
	var nilF *ThreadsFetcher
	if nilF.ResolveShareLinks(ctx, "x") != "x" {
		t.Fatal("nil fetcher")
	}
}

func TestThreadsPageCaptions(t *testing.T) {
	page := `{"post":{"caption":{"pk":"1","text":"Часть 1\nхук"}},"x":{"caption":{"text":"Часть 2 «цитата»"}},"y":{"caption":{"text":"Часть 1\nхук"}}}`
	got := threadsPageCaptions(page)
	if len(got) != 2 || got[0] != "Часть 1\nхук" || got[1] != "Часть 2 «цитата»" {
		t.Fatalf("captions: %q", got)
	}
}

func TestThreadsAuthorChain(t *testing.T) {
	// the real page: the username stands before the caption
	page := `[{"post":{"user":{"username":"au.thor"},"caption":{"text":"Часть 1"}}},` +
		`{"post":{"user":{"username":"fan"},"caption":{"text":"Спасибо!"}}},` +
		`{"post":{"user":{"username":"au.thor"},"caption":{"text":"Часть 2"}}}]`
	got := threadsAuthorChain(page, "au.thor")
	if len(got) != 2 || got[0] != "Часть 1" || got[1] != "Часть 2" {
		t.Fatalf("chain: %q", got)
	}
	if threadsAuthorChain(page, "") != nil {
		t.Fatal("no author")
	}
}

func TestWithChain(t *testing.T) {
	long := func(s string) string { return s + strings.Repeat(" шаг", 20) }
	chain := []string{"Лайфхак: как попасть в ответы ИИ\n1. откройте консоль", "🙂", long("2. добавьте фильтр"), long("3. отсортируйте по показам")}
	got := withChain("Лайфхак: как попасть в ответы ИИ\n1. откройте консоль", chain)
	if !strings.Contains(got, "2. добавьте фильтр") || !strings.Contains(got, "3. отсортируйте") || strings.Contains(got, "🙂") {
		t.Fatalf("chain: %s", got)
	}
	if withChain("другой текст", chain) != "другой текст" {
		t.Fatal("a chain of another post must not be glued")
	}
	// the fetcher glues the author's parts of a page
	page := `<html><head><meta property="og:title" content="A (@au.thor) on Threads"><meta property="og:description" content="` + chain[0] + `"></head><body><script>` +
		`[{"user":{"username":"au.thor"},"caption":{"text":"Лайфхак: как попасть в ответы ИИ\n1. откройте консоль"}},` +
		`{"user":{"username":"fan"},"caption":{"text":"` + long("Спасибо, полезно") + `"}},` +
		`{"user":{"username":"au.thor"},"caption":{"text":"` + long("2. добавьте фильтр") + `"}}]</script></body></html>`
	info, err := parseThreadsPage(page)
	if err != nil {
		t.Fatal(err)
	}
	full := withChain(info.Text, threadsAuthorChain(page, info.Author))
	if !strings.Contains(full, "2. добавьте фильтр") || strings.Contains(full, "Спасибо") {
		t.Fatalf("page chain: %s", full)
	}
}
