package openmeterapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/openmeterapi"
)

// A JSON request goes out with the token, and its JSON answer comes back.
func TestDoSendsJSONAndTheTokenAndDecodesTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/things" || r.URL.Query().Get("x") != "1" ||
			r.Header.Get("Authorization") != "Bearer t0k" || r.Header.Get("Content-Type") != "application/json" || in["name"] != "a" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"id": "1"}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	var out struct{ ID string }
	err := openmeterapi.New(srv.URL+"/", "t0k", nil).Do(context.Background(), http.MethodPost, "/api/v1/things", map[string][]string{"x": {"1"}}, map[string]string{"name": "a"}, &out)

	if err != nil || out.ID != "1" {
		t.Fatalf("err %v, out %+v", err, out)
	}
}

// A 404 is ErrNotFound; other failures say what failed and why.
func TestDoErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "meter not found in namespace", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := openmeterapi.New(srv.URL, "", nil)

	if err := c.Do(context.Background(), http.MethodGet, "/missing", nil, nil, nil); !errors.Is(err, openmeterapi.ErrNotFound) {
		t.Errorf("404: %v, want ErrNotFound", err)
	}
	err := c.Do(context.Background(), http.MethodGet, "/bad", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "GET /bad") || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "meter not found") {
		t.Errorf("400: %v", err)
	}
}

// ListAll reads every page: until a short page, or until the total is read.
func TestListAllReadsEveryPage(t *testing.T) {
	for _, withTotal := range []bool{true, false} {
		items := make([]int, 250)
		for i := range items {
			items[i] = i
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(r.URL.Query().Get("page[number]"))
			size, _ := strconv.Atoi(r.URL.Query().Get("page[size]"))
			if r.URL.Query().Get("filter") != "f" {
				http.Error(w, "filter dropped", http.StatusBadRequest)
				return
			}
			start, end := min((n-1)*size, len(items)), min(n*size, len(items))
			out := map[string]any{"data": items[start:end]}
			if withTotal {
				out["meta"] = map[string]any{"page": map[string]any{"total": len(items)}}
			}
			if err := json.NewEncoder(w).Encode(out); err != nil {
				t.Error(err)
			}
		}))
		got, err := openmeterapi.ListAll[int](context.Background(), openmeterapi.New(srv.URL, "", nil), "/list", map[string][]string{"filter": {"f"}})
		srv.Close()
		if err != nil || len(got) != 250 || got[249] != 249 {
			t.Fatalf("total %v: %d items, err %v", withTotal, len(got), err)
		}
	}
}
