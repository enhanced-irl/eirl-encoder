// Package pipeline builds the engine's GStreamer descriptions (gst-launch syntax): the program with its
// branches, every kind of source, the stills, the thumbnails and the output. Pure strings, no GStreamer:
// the tests compare them with what the Python engine and its launcher build (testdata/).
//
// Nothing a user or the app can write goes into a description: values are validated numbers, fixed
// words or paths checked by the config (engine.env values are rejected, never sanitised). User text
// (the Pause text) is set as an element property, never written here.
package pipeline

// Platform holds the hardware-specific elements (ENGINE_GO.md §3: the "platform profile" left over
// from F5). Only Tegra (Jetson Nano, L4T R32.7) exists; another board would be another value.
type Platform struct {
	Name       string
	Mem        string // caps of a frame in GPU memory
	Convert    string // converter/scaler between memory types and formats (the VIC)
	Compositor string
	H264Enc    string
	H265Enc    string
	MJPEGDec   string // hardware JPEG decoder, as a full element with its properties
	Decoder    string // hardware H.264/H.265 decoder
	CSICapture string // capture element that delivers into GPU memory
	Display    string // the board's HDMI output (the touch screen), without a desktop
}

// Tegra is the Jetson Nano: NVMM buffers, the VIC through nvvidconv, NVENC/NVDEC through V4L2.
var Tegra = Platform{
	Name:       "tegra",
	Mem:        "video/x-raw(memory:NVMM)",
	Convert:    "nvvidconv",
	Compositor: "nvcompositor",
	H264Enc:    "nvv4l2h264enc",
	H265Enc:    "nvv4l2h265enc",
	MJPEGDec:   "nvv4l2decoder mjpeg=1", // never nvjpegdec (MIXER.md §3, trap 8)
	Decoder:    "nvv4l2decoder",
	CSICapture: "nvv4l2camerasrc",
	Display:    "nvdrmvideosink",
}
