// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package onboarding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-webengine/engine"
	"github.com/go-webengine/engine/dom"
)

// point is a resolved element's center, in the window's own coordinate
// space — what a real host would compute from wherever it laid the element
// out on screen; tests compute it from the SAME element index Window
// itself uses (live.Elements()), not by guessing pixel positions.
type point struct {
	cx, cy int
}

// findByID walks the window's current document for id, returning the
// center point of its rect in the element index — the coordinate a test
// feeds to MouseDown, exactly mirroring go-webengine's own
// TestEndToEndLoginByCoordinate technique one layer up.
func findByID(t *testing.T, win *Window, id string) point {
	t.Helper()
	var target *dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if target != nil || n == nil {
			return
		}
		if n.Type == dom.Element && n.Attr["id"] == id {
			target = n
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(win.live.Document().Root)
	if target == nil {
		t.Fatalf("fixture element #%s not found", id)
	}
	r, ok := win.live.Elements()[target]
	if !ok {
		t.Fatalf("#%s missing from the element index", id)
	}
	return point{cx: int(r.X + r.W/2), cy: int(r.Y + r.H/2)}
}

// typeString feeds win one Key call per rune, the way a real keyboard
// delivers keystrokes one at a time — not a single bulk call — matching
// how a real application.Handler consumer drives Key.
func typeString(win *Window, s string) {
	for _, r := range s {
		win.Key("", r)
	}
}

// loginFixture is a small login-shaped page: an email/password field and a
// submit button whose script sets a cookie via document.cookie... actually
// document.cookie is not modeled by this engine's JS binding — the fixture
// instead relies on the SERVER setting a real Set-Cookie header once the
// form is (natively) submitted, the same mechanism a real login page uses.
const loginFixtureHTML = `<html><body>
	<form id="f" method="POST" action="/login">
		<input id="email" name="email" style="width:200px;height:24px">
		<input id="password" name="password" type="password" style="width:200px;height:24px">
		<button id="submit" type="submit" style="width:100px;height:30px">Log in</button>
	</form>
</body></html>`

func newLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login-form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(loginFixtureHTML))
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("email") == "agent@go-aiquota.test" && r.PostForm.Get("password") == "correct-horse-battery-staple" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "real-session-not-a-real-secret", Path: "/"})
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>Welcome</body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOnboardingWindowFullLoginFlow(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	buf, w, h, changed := win.Frame()
	if w != 400 || h != 400 || len(buf) != 400*400*4 {
		t.Fatalf("Frame shape = %dx%d, %d bytes; want 400x400, %d bytes", w, h, len(buf), 400*400*4)
	}
	if !changed {
		t.Fatal("first Frame() after Open: want changed=true")
	}

	el := findByID(t, win, "email")
	win.MouseDown(el.cx, el.cy)
	typeString(win, "agent@go-aiquota.test")

	pw := findByID(t, win, "password")
	win.MouseDown(pw.cx, pw.cy)
	typeString(win, "correct-horse-battery-staple")

	sub := findByID(t, win, "submit")
	win.MouseDown(sub.cx, sub.cy)

	if got := win.CurrentURL(); got == srv.URL+"/login-form" {
		t.Fatalf("CurrentURL after submit = %q, want it to have navigated away from the login form", got)
	}

	host := srv.Listener.Addr().String() // httptest server's host:port, the cookie's actual domain
	cookies := win.Cookies(host)
	if cookies["session"] != "real-session-not-a-real-secret" {
		t.Fatalf("Cookies(%q) = %v, want the session cookie the server set after a correct login", host, cookies)
	}
}

func TestOnboardingWindowWrongPasswordNoCookie(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	el := findByID(t, win, "email")
	win.MouseDown(el.cx, el.cy)
	typeString(win, "agent@go-aiquota.test")
	pw := findByID(t, win, "password")
	win.MouseDown(pw.cx, pw.cy)
	typeString(win, "wrong-password")
	sub := findByID(t, win, "submit")
	win.MouseDown(sub.cx, sub.cy)

	host := srv.Listener.Addr().String()
	if cookies := win.Cookies(host); cookies["session"] != "" {
		t.Fatalf("Cookies after a wrong-password submit = %v, want no session cookie", cookies)
	}
}

func TestOnboardingWindowBackspaceFixesATypo(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	el := findByID(t, win, "email")
	win.MouseDown(el.cx, el.cy)
	typeString(win, "wrong@typo.test")
	for range "@typo.test" {
		win.Key("Backspace", 0)
	}
	typeString(win, "@go-aiquota.test")

	n, ok := win.Focused()
	if !ok || n.Attr["value"] != "wrong@go-aiquota.test" {
		var got string
		if ok {
			got = n.Attr["value"]
		}
		t.Fatalf("email value after typo-fix = %q, want %q", got, "wrong@go-aiquota.test")
	}
}

func TestOnboardingWindowKeyBeforeAnyClickIsDropped(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	if _, ok := win.Focused(); ok {
		t.Fatal("nothing should be focused before any MouseDown")
	}
	win.Key("", 'x') // must not panic
}

func TestOnboardingWindowScrollClampsToContent(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	win.Scroll(-1000)
	if win.scrollY != 0 {
		t.Fatalf("scrollY after scrolling up past the top = %d, want 0", win.scrollY)
	}
	win.Scroll(1000000)
	if win.scrollY < 0 {
		t.Fatalf("scrollY after scrolling far past the bottom = %d, want >= 0", win.scrollY)
	}
}

func TestOnboardingWindowResizeIsANoop(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	win.Resize(999, 999, 2.0) // must not panic or change the fixed viewport
	_, w, h, _ := win.Frame()
	if w != 400 || h != 400 {
		t.Fatalf("viewport after Resize = %dx%d, want it unchanged at 400x400", w, h)
	}
}

func TestOnboardingWindowMouseUpAndMoveAreNoops(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)
	win.MouseUp(10, 10)
	win.MouseMove(10, 10) // must not panic
}

func TestOpenInvalidURL(t *testing.T) {
	if _, err := Open(context.Background(), "http://127.0.0.1:1/nope", 100, 100); err == nil {
		t.Fatal("Open against an unreachable address: want an error, got nil")
	}
}

// TestOnboardingWindowDefaultTypeButtonSubmits covers the common real-world
// shape submitFormFor's own doc comment names: a <button> with NO type
// attribute at all defaults to submit per the HTML spec — most real forms'
// buttons are written exactly this way (loginFixtureHTML's own button
// explicitly sets type="submit", so this fixture is what actually proves
// the default applies, not just the explicit case).
func TestOnboardingWindowDefaultTypeButtonSubmits(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><form id="f" method="POST" action="/submitted">
			<button id="go" style="width:80px;height:30px">Go</button>
		</form></body></html>`))
	})
	mux.HandleFunc("/submitted", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>done</body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	win, err := openWith(context.Background(), engine.New(), srv.URL+"/form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	btn := findByID(t, win, "go")
	win.MouseDown(btn.cx, btn.cy)

	if got := win.CurrentURL(); got != srv.URL+"/submitted" {
		t.Fatalf("CurrentURL after clicking a default-type button = %q, want %q", got, srv.URL+"/submitted")
	}
}

// TestOnboardingWindowMouseDownMiss covers a click that resolves to no
// element at all (must not panic, must not change focus).
func TestOnboardingWindowMouseDownMiss(t *testing.T) {
	srv := newLoginServer(t)
	win, err := openWith(context.Background(), engine.New(), srv.URL+"/login-form", 400, 400)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(win.Close)

	win.MouseDown(399, 399) // far corner, past every fixture element
	if _, ok := win.Focused(); ok {
		t.Fatal("a miss must not focus anything")
	}
}
