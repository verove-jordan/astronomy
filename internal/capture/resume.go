package capture

import (
	"strings"

	"github.com/verove-jordan/astronomy/internal/filters"
)

// Finishing a night that stopped early.
//
// A session ends before its plan does more often than not — cloud, a cable, dawn, a mount that lost
// its adapter. What the observer is owed afterwards is not "the rest of a list" but a COUNT PER
// CHANNEL: sixty Hα were planned, forty landed, twenty are missing.
//
// That distinction is the whole reason this is not `plan[framesDone:]`. A position in the flattened
// plan is wrong the moment one frame fails: the loop skips it, every later entry shifts by one, and
// resuming by index then shoots the right NUMBER of frames in the wrong colours — an imbalance
// nobody notices until the stack is assembled. Counting is also the only rule that survives the
// other thing that really happens, which is frames deleted by hand between sessions.

// FrameTally is how many frames of one filter and type a session already holds. It is exactly the
// shape the database aggregates capture_frames into, because that is the only record that knows
// what actually landed rather than what was attempted.
type FrameTally struct {
	Filter string
	Type   string
	Count  int
}

// Remaining subtracts what has already been captured from the sequence that was being shot.
//
// Steps keep their order and every other setting — exposure, gain, offset, bin, dithering — because
// resuming means finishing THIS plan, not writing a new one. A step whose frames are all present is
// dropped. Two steps sharing a filter and type are filled in order, so 20 L + 20 L with 30 done
// leaves 10 on the second.
func Remaining(seq Sequence, done []FrameTally) Sequence {
	have := make(map[string]int, len(done))
	for _, t := range done {
		if t.Count > 0 {
			have[tallyKey(t.Filter, t.Type)] += t.Count
		}
	}

	out := seq
	out.Steps = make([]Step, 0, len(seq.Steps))
	for _, step := range seq.Steps {
		key := tallyKey(step.Filter, step.Type)
		if credit := have[key]; credit > 0 {
			if credit >= step.Count {
				have[key] = credit - step.Count
				continue
			}
			step.Count -= credit
			have[key] = 0
		}
		out.Steps = append(out.Steps, step)
	}
	return out
}

// tallyKey buckets a frame the way the tally does: by filter and type, canonicalised.
//
// Through internal/filters, so a step asking for "SII" matches frames recorded as "S2" — the same
// alias table the wheel is resolved with, because a resume that cannot recognise its own frames
// would cheerfully shoot the whole channel twice.
func tallyKey(filter, kind string) string {
	name := strings.TrimSpace(filter)
	if canon, ok := filters.Token(name); ok {
		name = canon
	} else {
		name = strings.ToUpper(name)
	}
	return name + "|" + normalizeFrameType(kind)
}

// normalizeFrameType folds the empty type onto "light", which is what an empty Type means everywhere
// else in this package and what the database has recorded for it.
func normalizeFrameType(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return "light"
	}
	return k
}
