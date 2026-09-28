package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetWardenPassthroughPostsTheKillSwitch(t *testing.T) {
	var got []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		w.Write([]byte("passthrough=true\n"))
	}))
	defer fake.Close()

	s := &Server{WardenURL: fake.URL + "/"}
	if err := s.setWardenPassthrough(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := s.setWardenPassthrough(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "POST /passthrough?on=true" || got[1] != "POST /passthrough?on=false" {
		t.Fatalf("requests %q", got)
	}
}

func TestSetWardenPassthroughFailsWhenWardenRefuses(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer fake.Close()
	if err := (&Server{WardenURL: fake.URL}).setWardenPassthrough(context.Background(), true); err == nil {
		t.Fatal("a 500 from Warden was taken as success")
	}

	fake.Close()
	if err := (&Server{WardenURL: fake.URL}).setWardenPassthrough(context.Background(), true); err == nil {
		t.Fatal("an unreachable Warden was taken as success")
	}
}
