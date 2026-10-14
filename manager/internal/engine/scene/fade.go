package scene

import "time"

// Fade is the length of a fade transition.
const Fade = 400 * time.Millisecond

// Role is what a slot does during a fade (see FadePlan).
type Role string

const (
	RoleIdle      Role = "idle"
	RoleStatic    Role = "static"
	RoleIn        Role = "in"
	RoleBase      Role = "base"
	RoleOut       Role = "out"
	RoleLaterFade Role = "later-fade"
	RoleLaterCut  Role = "later-cut"
	roleLater     Role = "later" // only while planning
)

func covers(a, b Rect) bool {
	return a.X <= b.X && a.Y <= b.Y && a.X+a.W >= b.X+b.W && a.Y+a.H >= b.Y+b.H
}

// FadePlan says how to fade from one scene (SceneRects) to another with ONE pad per source: a
// layer cannot be in two places at once. Role of each slot:
//
//	static: the same in both · idle: invisible in both;
//	in: appears or moves → at its new place, fading in on top;
//	base: disappears but something coming in covers it → stays below, opaque, until the end (a
//	      true crossfade: B comes in over A);
//	out: disappears and nothing covers it (a PiP going away, the Pause removed) → fades out;
//	later-*: leaves the full canvas for a smaller place (swap in PiP, full → side by side): it is
//	      the base while the other comes in and then goes to its place, fading if something is
//	      under it (later-fade) or in one cut (later-cut).
func FadePlan(old, new map[int]Layer, w, h int) map[int]Role {
	full := Rect{0, 0, w, h}
	roles := make(map[int]Role, len(new))
	keys := make(map[int]struct{}, len(old)+len(new))
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range new {
		keys[k] = struct{}{}
	}
	for k := range keys {
		o, hasO := old[k]
		n, hasN := new[k]
		vo, vn := hasO && o.Alpha > 0, hasN && n.Alpha > 0
		switch {
		case !vo && !vn:
			roles[k] = RoleIdle
		case vo && vn && o.Rect == n.Rect:
			roles[k] = RoleStatic
		case vo && !vn:
			roles[k] = RoleOut
		case vo && covers(o.Rect, full) && !covers(n.Rect, full):
			roles[k] = roleLater
		default:
			roles[k] = RoleIn
		}
	}
	var incoming []Rect
	for k, r := range roles {
		if r == RoleIn {
			incoming = append(incoming, new[k].Rect)
		}
	}
	for k, r := range roles {
		switch r {
		case RoleOut:
			for _, g := range incoming {
				if covers(g, old[k].Rect) {
					roles[k] = RoleBase
					break
				}
			}
		case roleLater:
			under := false
			for j, nj := range new {
				if j != k && nj.Alpha > 0 && nj.Z < new[k].Z && covers(nj.Rect, new[k].Rect) {
					under = true
					break
				}
			}
			if under {
				roles[k] = RoleLaterFade
			} else {
				roles[k] = RoleLaterCut
			}
		}
	}
	return roles
}

func hasLaterFade(roles map[int]Role) bool {
	for _, r := range roles {
		if r == RoleLaterFade {
			return true
		}
	}
	return false
}

// FadeLength is the fade's duration: a third longer with a layer that goes to its place afterwards.
func FadeLength(roles map[int]Role) time.Duration {
	if hasLaterFade(roles) {
		return Fade * 3 / 2
	}
	return Fade
}

// FadeRects are the rects at fraction t (0…1) of a FadePlan fade. While fading, everything coming
// in goes one zorder above what stays below (base, later), and what goes out, above everything.
func FadeRects(old, new map[int]Layer, roles map[int]Role, t float64) map[int]Layer {
	two := hasLaterFade(roles)
	// The float64() conversions stop the compiler from fusing x*y+z into one FMA (arm64 does), which
	// would round differently from Python
	var ta, tb float64
	if two {
		ta = min(1.0, float64(t*1.5)) // first part: 2/3 of the total with two parts
		tb = max(0.0, min(1.0, float64(float64(t*1.5)-1.0)*2))
	} else {
		ta = min(1.0, t)
		tb = 1.0
	}
	out := make(map[int]Layer, len(roles))
	for k, role := range roles {
		o, n := old[k], new[k]
		switch {
		case role == RoleIdle:
			if _, ok := new[k]; ok {
				out[k] = n
			}
		case role == RoleStatic:
			out[k] = Layer{n.Rect, n.Alpha, n.Z + 1}
		case role == RoleIn:
			out[k] = Layer{n.Rect, n.Alpha * ta, n.Z + 1}
		case role == RoleBase:
			out[k] = Layer{o.Rect, o.Alpha, 1}
		case role == RoleOut:
			out[k] = Layer{o.Rect, o.Alpha * float64(1.0-ta), o.Z + 20}
		case ta < 1.0: // later-*: first part, as the base at its old place
			out[k] = Layer{o.Rect, o.Alpha, 1}
		case role == RoleLaterCut:
			out[k] = Layer{n.Rect, n.Alpha, n.Z + 1}
		default:
			out[k] = Layer{n.Rect, n.Alpha * tb, n.Z + 1}
		}
	}
	return out
}
