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
	page := `[{"post":{"caption":{"text":"Часть 1"},"user":{"username":"au.thor"}}},` +
		`{"post":{"caption":{"text":"Спасибо!"},"user":{"username":"fan"}}},` +
		`{"post":{"caption":{"text":"Часть 2"},"user":{"username":"au.thor"}}}]`
	got := threadsAuthorChain(page, "au.thor")
	if len(got) != 2 || got[0] != "Часть 1" || got[1] != "Часть 2" {
		t.Fatalf("chain: %q", got)
	}
	if threadsAuthorChain(page, "") != nil {
		t.Fatal("no author")
	}
}
