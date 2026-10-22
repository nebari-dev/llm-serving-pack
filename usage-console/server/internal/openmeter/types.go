package openmeter

import "github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"

// The console's response types. JSON field names match
// console/src/data/catalog.ts and mock.ts, which the console reads them as.

// Session is what the console's shell shows about where it is and who's
// using it: tenant, environment, the caller, and the versions in play.
type Session struct {
	Tenant      SessionTenant `json:"tenant"`
	Environment string        `json:"environment"`
	Actor       SessionActor  `json:"actor"`
	// Auth is how people sign in here (dev mode or OIDC), and where.
	Auth SessionAuth `json:"auth"`
	// Permissions is, per action, whether the caller may do it and which
	// roles may: the console disables what they can't, with "Needs role …".
	Permissions map[Action]Permission `json:"permissions"`
	Versions    struct {
		ControlPlane string `json:"controlPlane"`
	} `json:"versions"`
	// Warden is nil: this pack has no Warden in the request path.
	Warden *SessionWarden `json:"warden"`
	// GatewayURL is the OpenAI-compatible base callers point at, "" if unknown.
	GatewayURL string `json:"gatewayUrl"`
}

type SessionTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionActor struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	// Roles are most powerful first; Role is the first ("" with none).
	Roles []string `json:"roles"`
	Role  string   `json:"role,omitempty"`
	// Authenticated is false in dev mode, where every caller is the dev user
	// without signing in.
	Authenticated bool `json:"authenticated"`
}

type SessionWarden struct {
	Connected          bool    `json:"connected"`
	Version            string  `json:"version,omitempty"`
	SnapshotAgeSeconds float64 `json:"snapshotAgeSeconds,omitempty"`
	Passthrough        bool    `json:"passthrough"`
}

type SessionAuth struct {
	// Mode is "oidc" when people sign in, "dev" when everyone is the dev user.
	Mode string `json:"mode"`
	// SignInURL and SignOutURL are the sign-in endpoints (empty in dev mode).
	SignInURL  string `json:"signInUrl,omitempty"`
	SignOutURL string `json:"signOutUrl,omitempty"`
	// AccountURL is the user's Keycloak account page; GroupsURL the realm's
	// groups in Keycloak's admin console, where roles are assigned.
	AccountURL string `json:"accountUrl,omitempty"`
	GroupsURL  string `json:"groupsUrl,omitempty"`
	// GroupPrefix: the Keycloak group for role r is GroupPrefix+r.
	GroupPrefix string `json:"groupPrefix,omitempty"`
}

// Action is a kind of write the console asks permission for; it reads which
// ones the caller may do from GET /session.
type Action string

const (
	ActRead         Action = "read"
	ActDraftRules   Action = "rules.draft"
	ActPublishRules Action = "rules.publish"
	ActKillSwitch   Action = "killswitch"
	ActCapture      Action = "capture"
	ActPrices       Action = "prices"
	ActBudgets      Action = "budgets"
	ActRouting      Action = "routing"
	ActProjects     Action = "projects"
	ActDetectors    Action = "detectors"
	ActOwnKeys      Action = "keys.own"
	ActAnyKeys      Action = "keys.any"
	ActMembers      Action = "members"
)

// Permission is whether the caller may do an action, and who may.
type Permission struct {
	Allowed bool `json:"allowed"`
	// Roles may do it besides owner; empty for owner only.
	Roles []string `json:"roles"`
}

// WindowTotals sums requests over [from, to). SpendUSD leaves out the
// Unpriced requests, which have no price yet.
type WindowTotals struct {
	Requests int     `json:"requests"`
	Blocked  int     `json:"blocked"`
	Redacted int     `json:"redacted"`
	SpendUSD float64 `json:"spendUsd"`
	Unpriced int     `json:"unpriced"`
}

// Summary is the Overview's three numbers for a range, the same span before
// it for the deltas, and what's driving spend.
type Summary struct {
	Range    string       `json:"range"`
	From     int64        `json:"from"`
	To       int64        `json:"to"`
	Current  WindowTotals `json:"current"`
	Previous WindowTotals `json:"previous"`
	// TopTeamIncrease is the team whose spend rose most against the previous
	// window, or nil when none rose.
	TopTeamIncrease *TeamIncrease `json:"topTeamIncrease"`
	KeyAnomalies    []KeyAnomaly  `json:"keyAnomalies"`
	// UnpricedPairs are the (model, backend) pairs that served requests in
	// the range with no price, for the attention list.
	UnpricedPairs []UnpricedPair `json:"unpricedPairs"`
}

// UnpricedPair is a (model, backend) whose requests have no price: Requests
// of them in the range, the first at Since.
type UnpricedPair struct {
	Model    string `json:"model"`
	Backend  string `json:"backend"`
	Requests int    `json:"requests"`
	Since    int64  `json:"since"`
}

type TeamIncrease struct {
	Team     string  `json:"team"`
	DeltaUSD float64 `json:"deltaUsd"`
}

// KeyAnomaly is a key whose last 24 hours cost well over its daily average
// for the 7 days before.
type KeyAnomaly struct {
	KeyID       string  `json:"keyId"`
	KeyName     string  `json:"keyName"`
	SpendUSD    float64 `json:"spendUsd"`
	BaselineUSD float64 `json:"baselineUsd"`
	Ratio       float64 `json:"ratio"`
	// TopModel took the largest share of the key's last-24h spend.
	TopModel      string  `json:"topModel"`
	TopModelShare float64 `json:"topModelShare"`
}

// ReceiptCount is how many receipts match. Count is null when it can't be
// answered exactly, and Reason says why.
type ReceiptCount struct {
	Count  *int   `json:"count"`
	Since  int64  `json:"since"`
	Reason string `json:"reason,omitempty"`
}

type SpendView struct {
	Range string `json:"range"`
	By    string `json:"by"`
	// Totals and rows cover [from, to); the deltas compare with the same span
	// before it, [prevFrom, from).
	From     int64      `json:"from"`
	To       int64      `json:"to"`
	PrevFrom int64      `json:"prevFrom"`
	Rows     []SpendRow `json:"rows"`
	// Unpriced counts served requests in [from, to) whose (model, backend)
	// had no price: the totals leave them out until one is set.
	Unpriced int        `json:"unpriced"`
	Trend    SpendTrend `json:"trend"`
	Period   Projection `json:"period"`
}

// SpendRow is one group in the breakdown. ID is what Traffic filters on.
type SpendRow struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Sub          string  `json:"sub,omitempty"`
	SpendUSD     float64 `json:"spendUsd"`
	PrevSpendUSD float64 `json:"prevSpendUsd"`
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	// Unpriced counts the group's served requests with no price: in
	// Requests, not in SpendUSD.
	Unpriced int `json:"unpriced"`
	// NoDrill is set when Traffic has no filter for the group: requests
	// without a key identity, or refused before routing.
	NoDrill bool `json:"noDrill,omitempty"`
}

type SpendTrend struct {
	BucketMS int64        `json:"bucketMs"`
	Points   []TrendPoint `json:"points"`
	// Order ranks every group by its last 30 days of spend, so a group keeps
	// its color when the range changes.
	Order  []string          `json:"order"`
	Labels map[string]string `json:"labels"`
	// Unpriced counts requests over the charted buckets that have no price,
	// so aren't in the bars.
	Unpriced int `json:"unpriced"`
}

type TrendPoint struct {
	T      int64              `json:"t"`
	Values map[string]float64 `json:"values"`
}

// Projection is month-end spend and its basis: month to date plus the
// trailing daily average for each day left. The month is the UTC calendar
// month. TrailingDays is under projectionDays when there's less history.
// Unpriced counts the month's requests with no price, which it leaves out.
type Projection struct {
	PeriodStart      int64   `json:"periodStart"`
	PeriodEnd        int64   `json:"periodEnd"`
	MonthToDateUSD   float64 `json:"monthToDateUsd"`
	TrailingDailyUSD float64 `json:"trailingDailyUsd"`
	TrailingDays     float64 `json:"trailingDays"`
	RemainingDays    float64 `json:"remainingDays"`
	ProjectedUSD     float64 `json:"projectedUsd"`
	Unpriced         int     `json:"unpriced"`
}

type SavingsView struct {
	// The window is [from, to); FirstAt is its earliest served request (0
	// when none), and Days how many days from it (or from) to to.
	From        int64   `json:"from"`
	To          int64   `json:"to"`
	FirstAt     int64   `json:"firstAt"`
	Days        float64 `json:"days"`
	OutputLimit int     `json:"outputLimit"`
	// Served counts the window's served requests; Unpriced those with no
	// price; Rerouted those that ran on a model other than the one asked
	// for (a policy, a fallback, or an alias since retargeted).
	Served        int                  `json:"served"`
	Unpriced      int                  `json:"unpriced"`
	Rerouted      int                  `json:"rerouted"`
	Opportunities []SavingsOpportunity `json:"opportunities"`
}

// SavingsOpportunity is one group's best move: its requests on Model moved
// to Target on TargetBackend. Alias is set when the requests came through
// an alias (retargeting it moves them); otherwise Key asked for Model by
// name.
type SavingsOpportunity struct {
	ID            string `json:"id"`
	Alias         string `json:"alias,omitempty"`
	Key           string `json:"key,omitempty"`
	KeyID         string `json:"keyId,omitempty"`
	Model         string `json:"model"`
	Target        string `json:"target"`
	TargetBackend string `json:"targetBackend"`
	// Over the counted requests: what they cost, what Target would have,
	// and the difference.
	Requests  int     `json:"requests"`
	ActualUSD float64 `json:"actualUsd"`
	TargetUSD float64 `json:"targetUsd"`
	SavedUSD  float64 `json:"savedUsd"`
	// Served is every served request in the group; Excluded says why the
	// rest weren't counted, each under its first reason.
	Served   int             `json:"served"`
	Excluded SavingsExcluded `json:"excluded"`
	// Keys are the counted requests' keys, by name. NotAllowedKeys don't
	// allow Target yet: moving them first needs it added, or they'd be
	// refused (403 model_not_allowed). NotAllowedRequests are theirs.
	Keys               []string `json:"keys"`
	NotAllowedKeys     []string `json:"notAllowedKeys"`
	NotAllowedRequests int      `json:"notAllowedRequests"`
}

type SavingsExcluded struct {
	Unpriced       int `json:"unpriced"`       // no price, so no cost to compare
	KeyInactive    int `json:"keyInactive"`    // the key is revoked, expired or gone
	LongOutput     int `json:"longOutput"`     // output over OutputLimit
	OverContext    int `json:"overContext"`    // input + output over Target's context
	TargetUnpriced int `json:"targetUnpriced"` // Target had no price for them
}

type ActivityView struct {
	Since   int64            `json:"since"`
	Until   int64            `json:"until"`
	Changes []ActivityChange `json:"changes"`
	Events  []TrafficEvent   `json:"events"`
}

// ActivityChange is an audit row with its computed effect in place of the
// stored one.
type ActivityChange struct {
	model.Change
	Impact ActivityImpact `json:"impact"`
}

// ActivityImpact compares the buckets either side of a change. The change's
// own bucket counts as after, so up to 5 minutes before it land there too.
type ActivityImpact struct {
	WindowMinutes int   `json:"windowMinutes"`
	Pivot         int64 `json:"pivot"` // start of the change's bucket, epoch ms
	Before        Agg   `json:"before"`
	After         Agg   `json:"after"`
	// Bins are requests per 5-minute bucket; the first Split are before.
	Bins       []int `json:"bins"`
	Split      int   `json:"split"`
	Comparable bool  `json:"comparable"`
}

// Agg is tenant traffic over one side of a change.
type Agg struct {
	Requests int `json:"requests"`
	// CostPerRequestUSD is over served requests with a price; null when
	// there were none. Unpriced counts the served ones it leaves out.
	CostPerRequestUSD    *float64 `json:"costPerRequestUsd"`
	Unpriced             int      `json:"unpriced"`
	ErrorRate            float64  `json:"errorRate"`            // 5xx and 429, share 0-1
	BlockedRedactedShare float64  `json:"blockedRedactedShare"` // share 0-1
}

// TrafficEvent is something traffic did on its own: a backend started or
// stopped failing, or a budget crossed a share of its cap.
type TrafficEvent struct {
	ID     string `json:"id"`
	TS     int64  `json:"ts"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Tone   string `json:"tone"`
	To     string `json:"to"`
}
