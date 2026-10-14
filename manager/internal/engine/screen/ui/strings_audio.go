package ui

// AudioStrings is the audio view's settings (each input's delay, each camera's audio) (MIXER.md §21). Its tables, here; Strings carries them.
type AudioStrings struct {
	Button, Title string // the audio view's header button («… ›»); the sub-view's title
	// Delay column: its title, what it is for, and its range and long press (DelayRangeF: %d max ms, %d long step ms)
	Delay, DelayHint, DelayRangeF string
	MsF                           string // %d ms
	NoInputs                      string
	// Each camera's audio: the column's title, what it is for, and why it does nothing now (in the mix; follow off)
	Links, LinksHint         string
	LinksMix, LinksFollowOff string
	NoCameras                string
	// The choice: its title (PickTitleF: %s the camera, as its tile), «no audio of its own» and what it does, and an
	// input that is not there (GoneF: %s its id)
	PickTitleF                string
	NoOwnAudio, NoOwnAudioSub string
	GoneF                     string
}

var (
	audioES = AudioStrings{
		Button: "Ajustes del audio  ›", Title: "Ajustes del audio",
		Delay:          "Retardo",
		DelayHint:      "Si el sonido va por delante de la imagen, retrásalo.",
		DelayRangeF:    "De 0 a %d ms · mantén − o + para saltar %d ms",
		MsF:            "%d ms",
		NoInputs:       "No hay entradas de audio",
		Links:          "Audio de cada cámara",
		LinksHint:      "Es el que suena al ponerla al aire con «Sigue al vídeo».",
		LinksMix:       "Con «Mezcla» no se usa: suenan las marcadas.",
		LinksFollowOff: "«Sigue al vídeo» está apagado: ahora no se usa.",
		NoCameras:      "No hay cámaras en las fuentes",
		PickTitleF:     "Audio de %s",
		NoOwnAudio:     "Sin audio propio",
		NoOwnAudioSub:  "Sigue sonando el que estaba",
		GoneF:          "%s (desconectada)",
	}
	audioEN = AudioStrings{
		Button: "Audio settings  ›", Title: "Audio settings",
		Delay:          "Delay",
		DelayHint:      "If the sound is ahead of the picture, delay it here.",
		DelayRangeF:    "From 0 to %d ms · hold − or + to jump %d ms",
		MsF:            "%d ms",
		NoInputs:       "No audio inputs",
		Links:          "Each camera's audio",
		LinksHint:      "The one that plays when it goes on air with “Follows video”.",
		LinksMix:       "Not used with “Mix”: the checked ones play.",
		LinksFollowOff: "“Follows video” is off: not used now.",
		NoCameras:      "No cameras in the sources",
		PickTitleF:     "Audio for %s",
		NoOwnAudio:     "No audio of its own",
		NoOwnAudioSub:  "What was playing keeps playing",
		GoneF:          "%s (disconnected)",
	}
	audioFR = AudioStrings{
		Button: "Réglages audio  ›", Title: "Réglages audio",
		Delay:          "Retard",
		DelayHint:      "Si le son est en avance sur l'image, retardez-le ici.",
		DelayRangeF:    "De 0 à %d ms · maintenez − ou + pour sauter %d ms",
		MsF:            "%d ms",
		NoInputs:       "Aucune entrée audio",
		Links:          "Audio de chaque caméra",
		LinksHint:      "Celui qui sonne quand elle passe à l'antenne avec « Suit la vidéo ».",
		LinksMix:       "Inutilisé avec « Mixage » : les entrées cochées sonnent.",
		LinksFollowOff: "« Suit la vidéo » est désactivé : inutilisé pour l'instant.",
		NoCameras:      "Aucune caméra dans les sources",
		PickTitleF:     "Audio de %s",
		NoOwnAudio:     "Sans audio propre",
		NoOwnAudioSub:  "Le son en cours continue",
		GoneF:          "%s (déconnectée)",
	}
)
