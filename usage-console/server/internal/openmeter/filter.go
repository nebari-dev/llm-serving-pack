package openmeter

import (
	"net/url"
	"slices"
	"strconv"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// receiptFilter is Traffic's query: any of the values named for a
// dimension, every named dimension, and the receipt time in [since, before)
// (epoch ms; 0 leaves that end open).
type receiptFilter struct {
	key, team, project, model, verdict []string
	since, before                      int64
}

func filterOf(q url.Values) receiptFilter {
	ms := func(name string) int64 {
		v, _ := strconv.ParseInt(q.Get(name), 10, 64)
		return v
	}
	return receiptFilter{
		key: q["key"], team: q["team"], project: q["project"], model: q["model"], verdict: q["verdict"],
		since: ms("since"), before: ms("before"),
	}
}

// fields is what a filter compares, for a receipt or a meter row.
type fields struct {
	key, team, project, model, verdict string
}

func (f receiptFilter) match(v fields) bool {
	in := func(vals []string, v string) bool { return len(vals) == 0 || slices.Contains(vals, v) }
	return in(f.key, v.key) && in(f.team, v.team) && in(f.project, v.project) && in(f.model, v.model) && in(f.verdict, v.verdict)
}

func (f receiptFilter) matchReceipt(r model.Receipt) bool {
	return f.match(fields{key: r.KeyID, team: r.Team, project: r.ProjectID, model: r.ResolvedModel, verdict: r.Verdict})
}

// inWindow says where ms falls: before the window (older than since), in
// it, or after it (at or past before).
func (f receiptFilter) inWindow(ms int64) (older, in bool) {
	if f.since > 0 && ms < f.since {
		return true, false
	}
	return false, f.before == 0 || ms < f.before
}
