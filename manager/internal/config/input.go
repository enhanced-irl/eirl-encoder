package config

import (
	"regexp"
	"slices"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Input: which video source and which audio the stream uses (SOURCES.md). The same whitelists
// the engine checks: VIDEO_DEVICE ends up in `v4l2src device=…` and AUDIO_SOURCE in
// `alsasrc device=hw:CARD=…` inside the pipeline's `eval`.

var (
	Sources      = []string{"hdmi", "usb", "test"}
	VideoFormats = []string{"auto", "NV12", "YUY2", "I420", "UYVY", "MJPG"}

	// Only udev links (they do not change when replugged) and no names starting with "." (by-id/..
	// would be /dev/v4l)
	reVideoDevice = regexp.MustCompile(`^(auto|/dev/v4l/by-(id|path)/[A-Za-z0-9_:-][A-Za-z0-9_.:-]{0,199})$`)
	reVideoMode   = regexp.MustCompile(`^(auto|[0-9]{2,5}x[0-9]{2,5}@[0-9]{1,3})$`)
	reAudioSource = regexp.MustCompile(`^(auto|none|i2s|alsa:[A-Za-z0-9_-]{1,32})$`)
)

func (s Settings) validateInput(errs FieldErrors) {
	if !slices.Contains(Sources, s.Source) {
		errs["source"] = i18n.Ref("config.source")
	}
	if !reVideoDevice.MatchString(s.VideoDevice) {
		errs["device"] = i18n.Ref("config.device")
	}
	// videoFormat/videoMode and not format/mode: "mode" is already the destination's caller/listener
	if !slices.Contains(VideoFormats, s.VideoFormat) {
		errs["videoFormat"] = i18n.Ref("config.video_format")
	}
	if !reVideoMode.MatchString(s.VideoMode) {
		errs["videoMode"] = i18n.Ref("config.video_mode")
	}
	if !reAudioSource.MatchString(s.AudioSource) {
		errs["audio"] = i18n.Ref("config.audio")
	}
}

// InputPatch is the body of PUT /api/v1/input. Missing fields are left alone: a body with only
// edidPreferred (the earlier API) is still valid.
type InputPatch struct {
	Source        *string `json:"source"`
	Device        *string `json:"device"`
	Format        *string `json:"format"`
	Mode          *string `json:"mode"`
	Audio         *string `json:"audio"`
	EDIDPreferred *string `json:"edidPreferred"`
	Apply         bool    `json:"apply"`
}

func (p InputPatch) ApplyTo(cur Settings) (Settings, error) {
	s := cur
	set := func(v *string, dst *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(p.Source, &s.Source)
	set(p.Device, &s.VideoDevice)
	set(p.Format, &s.VideoFormat)
	set(p.Mode, &s.VideoMode)
	set(p.Audio, &s.AudioSource)
	set(p.EDIDPreferred, &s.EDIDPreferred)
	if err := s.Validate(); err != nil {
		return cur, err
	}
	return s, nil
}
