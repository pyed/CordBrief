package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFallbackRedirectIsolation(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		calls := 0
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("redirect destination contacted") }))
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+"/other", code) }))
		client, err := NewClient(source.URL, "model", "endpoint-bound-secret", source.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Complete(context.Background(), []Message{{Content: "private"}}); err == nil {
			t.Fatal("redirect accepted")
		}
		if _, err := client.ListModels(context.Background()); err == nil {
			t.Fatal("models redirect accepted")
		}
		source.Close()
		target.Close()
		if calls != 0 {
			t.Fatal("key crossed endpoint")
		}
	}
}

func TestFallbackProviderBodyNeverInErrors(t *testing.T) {
	for _, body := range []string{`{"choices":[{"message":{"content":123}}],"private":"RAW_BODY"}`, `{"data":[{"id":123}],"private":"RAW_BODY"}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c, _ := NewClient(s.URL, "model", "secret", nil)
		_, err := c.Complete(context.Background(), []Message{{Content: "private"}})
		if err != nil && strings.Contains(err.Error(), "RAW_BODY") {
			t.Fatal("raw completion body leaked")
		}
		_, err = c.ListModels(context.Background())
		if err != nil && strings.Contains(err.Error(), "RAW_BODY") {
			t.Fatal("raw model body leaked")
		}
		s.Close()
	}
}
