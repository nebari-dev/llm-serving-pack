package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// RetentionView is GET /retention: the receipts database's real policy.
type RetentionView struct {
	HotDays           *float64             `json:"hotDays"`
	CompressAfterDays *float64             `json:"compressAfterDays"`
	Aggregates        []AggregateRetention `json:"aggregates"`
	OldestReceiptAt   *int64               `json:"oldestReceiptAt"`
}

type AggregateRetention struct {
	Name          string   `json:"name"`
	DropAfterDays *float64 `json:"dropAfterDays"`
}

func (s *Server) retention(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	rt, err := s.Store.Retention(r.Context(), t)
	if err != nil {
		return nil, err
	}
	out := RetentionView{HotDays: rt.HotDays, CompressAfterDays: rt.CompressAfterDays, Aggregates: []AggregateRetention{}}
	for _, a := range rt.Aggregates {
		out.Aggregates = append(out.Aggregates, AggregateRetention{Name: a.Name, DropAfterDays: a.DropAfterDays})
	}
	if rt.OldestReceipt != nil {
		ms := rt.OldestReceipt.UnixMilli()
		out.OldestReceiptAt = &ms
	}
	return out, nil
}

// setWardenPassthrough flips Warden's kill switch on its admin port (§9.3).
func (s *Server) setWardenPassthrough(ctx context.Context, on bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(s.WardenURL, "/")+"/passthrough?on="+strconv.FormatBool(on), nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("warden: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// setPassthrough is POST /warden/passthrough {"on": bool}. It writes the
// audit row and calls Warden in one transaction, so the row exists only if
// Warden took the change. Asking for the state Warden is already in is a
// no-op. Warden holds the flag in memory: a restart turns it back off.
func (s *Server) setPassthrough(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.On == nil {
		return nil, badRequest(`body must be {"on": true|false}`)
	}
	if s.WardenURL == "" {
		return nil, unavailable("this control plane doesn't know where Warden is")
	}
	h, err := s.fetchWarden(r.Context())
	if err != nil {
		return nil, unavailable("Warden isn't answering: " + err.Error())
	}
	on := *in.On
	if h.Passthrough == on {
		return map[string]bool{"passthrough": on}, nil
	}
	action := "Turned off Warden pass-through"
	if on {
		action = "Turned on Warden pass-through"
	}
	err = s.Store.Audited(r.Context(), t, actor(r), action, "Warden", "Kill switch",
		map[string]bool{"passthrough": h.Passthrough}, map[string]bool{"passthrough": on},
		func() error {
			if err := s.setWardenPassthrough(r.Context(), on); err != nil {
				return unavailable("Warden didn't take the change: " + err.Error())
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return map[string]bool{"passthrough": on}, nil
}
