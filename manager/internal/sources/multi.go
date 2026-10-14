package sources

import (
	"math"
	"slices"
	"strconv"
)

// --- Several sources at once (the mixer) -----------------------------------------------------
//
// Choose gives the whole bus to a single source. With several open at once the bus is shared: on the
// Nano the four USB 3 ports go through a single link behind the hub, and a Cam Link in NV12 1080p60
// already takes ~186 MB/s of the 300. ChooseAll picks for all of them together with the same "auto" rule.

// WarnNoBus: it could be captured alone, but with the others open there is no bus left in any mode.
const WarnNoBus = "no_bus"

// Keep: a source already being captured and with what. ChooseAll does not change it while the source
// still offers it: a camera on air does not change format because another one is plugged in. With an
// empty Choice it only gets priority (the one about to be opened: so the limit does not leave it out).
type Keep struct {
	ID     string
	Choice Choice
}

// Pick: what ChooseAll decides for a source. OK=false if it is not captured: the reasons go in
// Src.Warnings (WarnNoBus if it is the bus; the usual ones if it cannot be captured even alone).
type Pick struct {
	Src    Source
	Choice Choice
	OK     bool
	Share  float64 // fraction of its bus budget it takes (0 if compressed)
}

// ChooseAll picks how to capture, at the same time, up to limit USB sources of the catalogue (the
// board's HDMI does not count: it goes over CSI). Order and priority: first the keep ones still
// connected, in the given order; then the rest in catalogue order, capturable ones first. Each one
// gets the best mode by the "auto" rule that leaves room on its bus for the cheapest mode of the ones
// that come after it (so a camera with MJPEG does not leave out a Cam Link, which is uncompressed
// only); failing that, the best that fits with what has already been shared out. MJPEG counts as 0:
// the bus is limited by what moves uncompressed.
func ChooseAll(cat Catalog, keep []Keep, limit int) []Pick {
	byID := map[string]int{}
	for i, s := range cat.Sources {
		if s.Kind == KindUSB {
			byID[s.ID] = i
		}
	}
	var order []int
	in := map[int]bool{}
	keepBy := map[int]Choice{}
	for _, k := range keep {
		if i, ok := byID[k.ID]; ok && !in[i] {
			order, in[i], keepBy[i] = append(order, i), true, k.Choice
		}
	}
	for _, capturable := range []bool{true, false} {
		for i, s := range cat.Sources {
			if s.Kind == KindUSB && !in[i] && (s.Error == "" && len(candidates(s)) > 0) == capturable {
				order, in[i] = append(order, i), true
			}
		}
	}
	if limit >= 0 && len(order) > limit {
		order = order[:limit]
	}

	n := len(order)
	picks := make([]Pick, n)
	cands := make([][]candidate, n)
	minCost := make([]float64, n)
	bus := make([]string, n)
	budget := map[string]float64{}
	for k, i := range order {
		src := cat.Sources[i]
		src.Warnings = slices.Clone(src.Warnings) // no_bus is added without touching the catalogue
		picks[k].Src = src
		if src.Error == "" {
			cands[k] = candidates(src)
		}
		minCost[k] = math.Inf(1)
		for _, c := range cands[k] {
			minCost[k] = math.Min(minCost[k], rawBytes(c.Choice))
		}
		bus[k] = busKey(src)
		budget[bus[k]] = math.Max(budget[bus[k]], busBudget(speedOf(src)))
	}
	used := map[string]float64{}
	take := func(k int, c Choice) {
		cost := rawBytes(c)
		used[bus[k]] += cost
		picks[k].Choice, picks[k].OK, picks[k].Share = c, true, cost/budget[bus[k]]
	}

	// The ones already being captured, as they are, if they still offer it and it fits
	for k, i := range order {
		want, ok := keepBy[i]
		if !ok {
			continue
		}
		for _, c := range cands[k] {
			if c.Choice == want && used[bus[k]]+rawBytes(want) <= budget[bus[k]] {
				take(k, want)
				break
			}
		}
	}
	for k := range order {
		if picks[k].OK || len(cands[k]) == 0 {
			continue
		}
		b := bus[k]
		// The minimum the ones after it on the same bus need (only those that fit now)
		reserve := 0.0
		for j := k + 1; j < n; j++ {
			if !picks[j].OK && bus[j] == b && used[b]+minCost[j] <= budget[b] {
				reserve += minCost[j]
			}
		}
		chosen := -1
		for _, r := range []float64{reserve, 0} {
			for ci, c := range cands[k] {
				if used[b]+rawBytes(c.Choice)+r <= budget[b] {
					chosen = ci
					break
				}
			}
			if chosen >= 0 {
				break
			}
		}
		if chosen < 0 {
			picks[k].Src.Warnings = append(picks[k].Src.Warnings, WarnNoBus)
			continue
		}
		take(k, cands[k][chosen].Choice)
	}
	return picks
}

// rawBytes: bytes/s an uncompressed capture moves over the bus (0 if compressed).
func rawBytes(c Choice) float64 {
	bpp, raw := bytesPerPixel[c.Format]
	if !raw {
		return 0
	}
	return float64(c.Width*c.Height) * bpp * c.Rate.FPS()
}

// busKey: devices with the same busnum share bandwidth (on the Nano, bus 2 = USB 3 and bus 1 = USB 2,
// both behind the same hub). Without busnum, all together: better to fall short.
func busKey(s Source) string {
	if s.USB != nil && s.USB.Bus > 0 {
		return strconv.Itoa(s.USB.Bus)
	}
	return "?"
}

func speedOf(s Source) int {
	if s.USB != nil && s.USB.SpeedMbps > 0 {
		return s.USB.SpeedMbps
	}
	return 5000 // like candidates
}
