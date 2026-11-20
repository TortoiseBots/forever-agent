package bughunt

// Hit is one finding, with the Python checker's fields and JSON keys so a ticket file and a Python hits.jsonl read
// alike. Bot is the run's own identity for the member: a trace's guid text for a watch, the character key for a
// trials repeat.
type Hit struct {
	Rule   int      `json:"rule"`
	DS     string   `json:"ds"`
	Run    string   `json:"run"`
	T      float64  `json:"t"`
	Clock  string   `json:"clock"`
	Dur    float64  `json:"dur"`
	Bot    string   `json:"bot"`
	Cls    string   `json:"cls"`
	Role   string   `json:"role"`
	Ev     string   `json:"ev"`
	Key    []string `json:"key"`
	Deaths int      `json:"deaths"`
	Sparse bool     `json:"sparse"`
	Cost   float64  `json:"cost"`
	Note   string   `json:"note"`

	// At is the hit's wall time when the run knows its start, 0 otherwise. It is the ticket file's first_seen and
	// is not part of the hit's JSON, which keeps the Python checker's keys.
	At float64 `json:"-"`
}

// hitOpts carries hit()'s keyword arguments.
type hitOpts struct {
	sparse bool
	cost   *float64
	note   string
}

// HitOption sets one of hit()'s optional arguments.
type HitOption func(*hitOpts)

// Sparse marks a hit from a sparse recording (a walk's 2-14 s frames), as the Python's sparse= argument does.
func Sparse() HitOption { return func(o *hitOpts) { o.sparse = true } }

// Cost sets the hit's cost in seconds; without it the episode's duration is the cost.
func Cost(secs float64) HitOption { return func(o *hitOpts) { o.cost = &secs } }

// Note sets the hit's note.
func Note(s string) HitOption { return func(o *hitOpts) { o.note = s } }

// Hit builds one hit: the episode's start and duration, the member, the evidence and the stable key, plus the deaths
// of that member from just before the episode to fifteen seconds after it, which raise the cost.
func (r *Run) Hit(rule int, t, dur float64, bot, ev string, key []string, opts ...HitOption) Hit {
	var o hitOpts
	for _, opt := range opts {
		opt(&o)
	}
	deaths := 0
	for _, d := range r.Deaths {
		if d.Bot == bot && t-1 <= d.T && d.T <= t+dur+15 {
			deaths++
		}
	}
	base := dur
	if o.cost != nil {
		base = *o.cost
	}
	at := 0.0
	if r.T0 > 0 {
		at = r.T0 + t
	}
	return Hit{
		Rule:   rule,
		DS:     r.DS,
		Run:    r.Name,
		T:      pyRound(t, 2),
		Clock:  r.Clock(t),
		Dur:    pyRound(dur, 1),
		Bot:    bot,
		Cls:    r.Class(bot),
		Role:   r.Role(bot),
		Ev:     ev,
		Key:    append([]string(nil), key...),
		Deaths: deaths,
		Sparse: o.sparse,
		Cost:   pyRound(base+float64(deaths)*DEATH_COST_S, 1),
		Note:   o.note,
		At:     at,
	}
}
