package bughunt

// Step is one frame of an episode and the evidence the predicate gave it.
type Step struct {
	Frame *Frame
	Ev    string
}

// Episode is one run of consecutive frames where a predicate held for one member: the Python checker's episodes()
// yields (name, t0, t1, [(frame, evidence)...]) and this is the same, with the frames kept.
type Episode struct {
	Bot   string
	T0    float64
	T1    float64
	Steps []Step
}

// Pred is an episode's condition: the evidence text when it holds, false when it does not. It is called for a
// subject's unit in a frame; a nil unit never reaches it.
type Pred func(f *Frame, bot string, u *Unit) (string, bool)

// Episodes finds every run of consecutive frames where pred holds for a member, lasting longer than minDur (at
// least minDur when strict is false), with no gap above MAX_GAP_S. A frame that does not carry the member, a frame
// where the predicate fails, and a gap all end the member's episode. The output is ordered by when each episode
// ended, as the Python's is.
func (r *Run) Episodes(pred Pred, minDur float64, strict bool) []Episode {
	var (
		cur   = map[string][]Step{}
		open  []string // the open bots, in the order they opened
		out   []Episode
		lastT float64
		haveT bool
	)
	closeBot := func(n string) {
		seq := cur[n]
		if len(seq) == 0 {
			return
		}
		delete(cur, n)
		for i, o := range open {
			if o == n {
				open = append(open[:i], open[i+1:]...)
				break
			}
		}
		dur := seq[len(seq)-1].Frame.T - seq[0].Frame.T
		if (strict && dur > minDur) || (!strict && dur >= minDur) {
			out = append(out, Episode{Bot: n, T0: seq[0].Frame.T, T1: seq[len(seq)-1].Frame.T, Steps: seq})
		}
	}
	closeAll := func() {
		for len(open) > 0 {
			closeBot(open[0])
		}
	}
	for _, f := range r.Frames {
		if haveT && f.T-lastT > MAX_GAP_S {
			closeAll()
		}
		lastT, haveT = f.T, true
		for _, n := range f.Order {
			u := f.Units[n]
			if !r.IsSubject(n) {
				closeBot(n)
				continue
			}
			if ev, ok := pred(f, n, u); ok {
				if _, isOpen := cur[n]; !isOpen {
					open = append(open, n)
				}
				cur[n] = append(cur[n], Step{Frame: f, Ev: ev})
			} else {
				closeBot(n)
			}
		}
		for _, n := range append([]string(nil), open...) {
			if _, ok := f.Units[n]; !ok {
				closeBot(n)
			}
		}
	}
	closeAll()
	return out
}
