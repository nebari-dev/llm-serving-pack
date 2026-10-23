package openmeter

// The console's response types. JSON field names match
// console/src/data/catalog.ts and mock.ts, which the console reads them as.

// Session is what the console's shell shows about where it is and who's
// using it: tenant, environment, the caller, and the versions in play.
type Session struct {
	Tenant      SessionTenant `json:"tenant"`
	Environment string        `json:"environment"`
	Actor       SessionActor  `json:"actor"`
	Auth        SessionAuth   `json:"auth"`
	Versions    struct {
		ControlPlane string `json:"controlPlane"`
	} `json:"versions"`
}

type SessionTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionActor struct {
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

type SessionAuth struct {
	// Mode is "dev": the gateway in front signs people in, so the console
	// has nothing to sign out of.
	Mode string `json:"mode"`
}

// WindowTotals sums requests over [from, to). SpendUSD leaves out the
// Unpriced requests, which have no price yet.
type WindowTotals struct {
	Requests int     `json:"requests"`
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
}

type TeamIncrease struct {
	Team     string  `json:"team"`
	DeltaUSD float64 `json:"deltaUsd"`
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
