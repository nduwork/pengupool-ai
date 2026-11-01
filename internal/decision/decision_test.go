package decision

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/store"
)

func TestValidAnswers(t *testing.T) {
	qs := []Question{{Name: "pick", Type: Choice, Options: map[string]string{"a": "", "b": ""}}}
	if validAnswers(qs, map[string]Answer{"pick": {Type: Choice, Choice: "c"}}) == nil {
		t.Fatal("a choice outside the options was accepted")
	}
	if validAnswers(qs, map[string]Answer{"pick": {Type: Choice, Choice: "a", Confidence: 0.9}}) != nil {
		t.Fatal("a valid choice was refused")
	}
	if got := Redact(map[string]any{"x": "token=abcdefghijklmnopqrstu"}).(map[string]any)["x"]; got != "[redacted]" {
		t.Fatalf("secret not redacted: %v", got)
	}
}

// TestOllamaSystemOne runs against a local Ollama when PENGUPOOL_TEST_OLLAMA is set.
func TestOllamaSystemOne(t *testing.T) {
	if os.Getenv("PENGUPOOL_TEST_OLLAMA") == "" {
		t.Skip("set PENGUPOOL_TEST_OLLAMA=1 to call a local Ollama")
	}
	res, items, err := Check(context.Background(), store.ProviderConfig{Type: "ollama", Format: "ollama-systemone",
		Address: "http://127.0.0.1:11434", Model: cmp.Or(os.Getenv("PENGUPOOL_TEST_OLLAMA_MODEL"), "tev1:4b")})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v %+v", res, items)
	for _, c := range items {
		if !c.OK {
			t.Errorf("%s: want %s, got %s", c.Name, c.Want, c.Got)
		}
	}
}

func TestSecretShapes(t *testing.T) {
	for _, s := range []string{
		"AWS_SECRET_ACCESS_KEY=zzFAKEsecretKEY0123456789abcdef",
		"sk_live_zzFAKE0123456789abcdefgh", "rk_test_zzFAKE0123456789abcdefgh",
		"github_pat_11ZZFAKE0123456789_abcdefghijklmnop", "ghp_zzFAKE0123456789abcdefghijklmnopqr",
		"AIzaSyZZFAKE0123456789abcdefghijklmnopq",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ6emZha2UifQ.zzFAKEsig0123456789",
		"xoxs-0123456789-zzfake", `{"password": "zzFakePassw0rdValue"}`, "DB_TOKEN: zzfake12345678",
		"-----BEGIN PRIVATE KEY-----\nzzfake\n-----END PRIVATE KEY-----",
	} {
		if !SecretRe.MatchString(s) {
			t.Errorf("not recognised: %q", s)
		}
	}
	for _, s := range []string{"the token count is high", "fix the password field", "token: short", "secret santa: everyone",
		"max_tokens=2048", "sk_live is a prefix", "password = os.Getenv(\"X\")"} {
		if SecretRe.MatchString(s) {
			t.Errorf("prose redacted: %q", s)
		}
	}
}

// TestDecideRedactsEverythingSent: the questions leave the host redacted too.
func TestDecideRedactsEverythingSent(t *testing.T) {
	const sec = "sk_live_zzFAKE0123456789abcdefgh"
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		io.WriteString(w, `{"answers":{"q":{"choice":"a"}}}`)
	}))
	defer srv.Close()
	pc := store.ProviderConfig{Type: "ollama", Format: "ollama-systemone", Address: srv.URL, Model: "m"}
	p := &httpProvider{cfg: pc, client: srv.Client()}
	_, _ = p.Decide(context.Background(), Request{State: map[string]any{}, Questions: []Question{
		{Name: "q", Type: Choice, Instructions: "line " + sec, Options: map[string]string{"a": "opt " + sec, "b": ""}}}})
	if len(bodies) != 1 {
		t.Fatalf("%d requests", len(bodies))
	}
	for _, b := range bodies {
		if strings.Contains(b, "zzFAKE") {
			t.Errorf("secret sent: %s", b)
		}
	}
}

// Structured instructions (a helper's `decide` may send any JSON) keep their shape, redacted.
func TestRedactQuestionsKeepsStructuredInstructions(t *testing.T) {
	qs := redactQuestions([]Question{{Name: "q", Type: Predicate, Instructions: map[string]any{"ask": "is it ok?", "key": "sk_live_zzFAKE0123456789abcdefgh"}}})
	m, ok := qs[0].Instructions.(map[string]any)
	if !ok || m["ask"] != "is it ok?" || strings.Contains(fmt.Sprint(m), "zzFAKE") {
		t.Fatalf("%#v", qs[0].Instructions)
	}
}
