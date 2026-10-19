package notes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hridyeshh/stickynotes-backend/internal/config"
	"github.com/hridyeshh/stickynotes-backend/internal/db"
)

func TestPickLinksTakesTurnsAndSkipsWhatWasRead(t *testing.T) {
	l := func(u string) Link { return Link{URL: u} }
	results := [][]Link{
		{l("https://a.example/1"), l("https://www.a.example/2/"), l("https://a.example/3"), {URL: "https://c.example/p", Title: "Load Balancing | Request PDF"}},
		{l("https://source.example/read"), l("https://a.example/2"), l("https://b.example/x?y=1#top"), {URL: "https://d.example/p", Title: "load balancing - Springer"}},
		{{URL: "https://mirror.example/read", Title: "The Source - Mirror"}}, // the source on another site
		nil, // a failed search
	}
	read := map[string]bool{urlKey("http://www.source.example/read/"): true, titleKey("The Source | Its Site"): true}

	var got []string
	for _, x := range pickLinks(results, read, 5) {
		got = append(got, x.URL)
	}
	want := []string{"https://a.example/1", "https://www.a.example/2/", "https://a.example/3", "https://b.example/x?y=1#top", "https://c.example/p"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pickLinks = %v, want %v", got, want)
	}
	if len(read) != 2 {
		t.Fatal("pickLinks wrote to the sources it was given")
	}
	if titleKey("") == titleKey(" ") {
		t.Fatal("untitled pages must never count as the same page")
	}
}

func TestNormTags(t *testing.T) {
	got := normTags([]string{" #Distributed   Systems ", "distributed systems", "", "Go", strings.Repeat("x", 40), "a", "b"}, 4)
	want := []string{"distributed systems", "go", strings.Repeat("x", 32), "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normTags = %q, want %q", got, want)
	}
}

// TestAskGeminiThenSearch runs a build's two calls against stand-ins for Gemini and Brave.
func TestAskGeminiThenSearch(t *testing.T) {
	gemini := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/busy-model:generateContent":
			http.Error(w, "high demand", http.StatusServiceUnavailable)
		case r.URL.Path != "/test-model:generateContent" || r.Header.Get("x-goog-api-key") != "g-key":
			http.Error(w, "wrong model or key", http.StatusNotFound)
		case strings.Contains(string(body), strings.Repeat("y", noteChars+1)):
			http.Error(w, "a note was sent whole", http.StatusBadRequest)
		case !strings.Contains(string(body), "Topic: Consistent hashing"):
			http.Error(w, "no topic", http.StatusBadRequest)
		default:
			reply := `{"queries": ["consistent  hashing ring", "", "jump hash paper", "rendezvous hashing", "vnodes", "a fifth"], "tags": ["Distributed Systems", "hashing"]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": reply}}}}}})
		}
	}))
	defer gemini.Close()
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "b-key" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query().Get("q")
		if q == "jump hash paper" {
			http.Error(w, "busy", http.StatusTooManyRequests) // one failed search doesn't sink the rest
			return
		}
		_, _ = io.WriteString(w, `{"web": {"results": [
			{"title": "Ring &amp; nodes: `+q+`", "url": "https://`+strings.ReplaceAll(q, " ", "-")+`.example/", "meta_url": {"hostname": "www.ring.example"}},
			{"title": "Bad", "url": "javascript:alert(1)", "profile": {"name": "Evil"}}]}}`)
	}))
	defer brave.Close()

	h := &Handler{cfg: &config.Config{
		GeminiAPIKey: "g-key", GeminiModels: []string{"busy-model", "test-model"}, GeminiURL: gemini.URL + "/",
		BraveAPIKey: "b-key", BraveURL: brave.URL,
	}}
	notes := []db.NewestTopicNotesRow{{ID: uuid.New(), Text: "Each key goes to the first server clockwise."}, {ID: uuid.New(), Text: strings.Repeat("y", 900)}}
	queries, tags, err := h.askGemini(context.Background(), "Consistent hashing", notes)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"consistent hashing ring", "jump hash paper", "rendezvous hashing", "vnodes"}; !reflect.DeepEqual(queries, want) {
		t.Fatalf("queries = %q, want %q", queries, want)
	}
	if want := []string{"distributed systems", "hashing"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %q, want %q", tags, want)
	}

	results, err := h.search(context.Background(), queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(results[1]) != 0 || len(results[0]) != 1 {
		t.Fatalf("results = %+v: want the failed search empty and javascript: links dropped", results)
	}
	if got := results[0][0]; got.Title != "Ring & nodes: consistent hashing ring" || got.Site != "ring.example" {
		t.Fatalf("link = %+v", got)
	}

	// When no model answers it is an error, so the build pauses rather than saving nothing.
	h.cfg.GeminiModels = []string{"busy-model", "retired-model"}
	if _, _, err := h.askGemini(context.Background(), "Consistent hashing", notes); err == nil {
		t.Fatal("want an error from a failed Gemini call")
	}
}
