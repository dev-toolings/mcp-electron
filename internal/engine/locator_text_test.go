package engine

import (
	"strings"
	"testing"
)

func TestGetByTextKeepsInnermostMatch(t *testing.T) {
	_, page := newIsolatedPage(t)
	page.MustNavigate(dataURL(`<main><div><section>
		<h1><span>Choisissez vos projets</span></h1>
		<button id="go"><span>Continuer</span></button>
		<p>Continuer plus tard</p>
		<button id="other">Autre</button>
	</section></div></main>`)).MustWaitLoad()

	el, err := ResolveTarget(page, `getByText("Choisissez vos projets")`, nil)
	if err != nil {
		t.Fatalf("heading: %v", err)
	}
	if tag := el.MustEval("() => this.tagName").String(); tag != "SPAN" {
		t.Fatalf("innermost element expected, got %s", tag)
	}

	// Two innermost matches, one exact: the exact one wins.
	el, err = ResolveTarget(page, `getByText("Continuer")`, nil)
	if err != nil {
		t.Fatalf("exact preference: %v", err)
	}
	if id := el.MustEval("() => this.closest('button')?.id").String(); id != "go" {
		t.Fatalf("expected the button's span, got closest button %q", id)
	}

	_, err = ResolveTarget(page, `getByText("Co")`, nil)
	if err == nil || !strings.Contains(err.Error(), "matched 2") {
		t.Fatalf("ambiguous substring must fail with the count, got %v", err)
	}
	_, err = ResolveTarget(page, `getByText("absent")`, nil)
	if err == nil || !strings.Contains(err.Error(), "matched 0") {
		t.Fatalf("missing text must report 0 matches, got %v", err)
	}
}
