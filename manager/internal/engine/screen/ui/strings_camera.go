package ui

// CameraStrings is the Image tab (Sources → Image): a USB camera's image settings (SOURCES.md §7, MIXER.md §21).
// Its tables, here; Strings carries them.
type CameraStrings struct {
	// Group is each group of settings (its chip), by its id; Control, each setting by its id (SOURCES.md §7.2)
	Group   map[string]string
	Control map[string]string
	// PowerLine is the anti-flicker's choices, by value: 0 off, 1 50 Hz, 2 60 Hz, 3 auto
	PowerLine [4]string
	// Auto is the value of a setting whose automatic is on; InactiveF says which switch frees it (%s its name)
	Auto, InactiveF string
	// DefaultF is a row's factory value (%s, with its unit); On and Off, a switch's in it
	DefaultF, On, Off string
	// The units: exposure in ms (MsF: %s with one decimal), colour temperature in kelvin (%d), pan and tilt in
	// degrees (%s)
	MsF, KelvinF, DegreesF string
	// Reset is «Factory settings»; ResetConfirm, what its second tap does
	Reset, ResetConfirm string
	// Hint is the page's bottom line; Empty and EmptyHelp, the page without a camera to set
	Hint, Empty, EmptyHelp string
	// NoSettings is the engine's unsupported to a camera command: the camera has no settings now (hidden, unplugged)
	NoSettings string
}

var (
	cameraES = CameraStrings{
		Group: map[string]string{"image": "Imagen", "exposure": "Exposición", "white_balance": "Balance de blancos",
			"framing": "Enfoque y encuadre"},
		Control: map[string]string{"brightness": "Brillo", "contrast": "Contraste", "saturation": "Saturación",
			"hue": "Tono", "gamma": "Gamma", "sharpness": "Nitidez", "exposure_auto": "Exposición automática",
			"exposure": "Exposición", "gain": "Ganancia", "backlight": "Compensación de contraluz",
			"power_line": "Antiparpadeo", "wb_auto": "Balance de blancos automático",
			"wb_temperature": "Temperatura de color", "focus_auto": "Enfoque automático", "focus": "Enfoque",
			"zoom": "Zoom", "pan": "Encuadre horizontal", "tilt": "Encuadre vertical"},
		PowerLine:    [4]string{"Apagado", "50 Hz", "60 Hz", "Automático"},
		Auto:         "Automático",
		InactiveF:    "Apaga «%s» para cambiarlo",
		DefaultF:     "De fábrica: %s",
		On:           "activado",
		Off:          "desactivado",
		MsF:          "%s ms",
		KelvinF:      "%d K",
		DegreesF:     "%s°",
		Reset:        "Valores de fábrica",
		ResetConfirm: "Vuelve a poner todos sus ajustes como venían de fábrica: toca otra vez",
		Hint:         "Se aplica al momento, también emitiendo. La cámara lo recuerda aunque la desenchufes.",
		Empty:        "No hay cámaras con ajustes de imagen",
		NoSettings:   "esa cámara no tiene ajustes ahora (oculta o desenchufada)",
		EmptyHelp: "Conecta una cámara USB que tenga ajustes de imagen (brillo, exposición, enfoque…). La HDMI de la " +
			"placa, la mayoría de capturadoras y las cámaras de red se ajustan en el propio aparato.",
	}
	cameraEN = CameraStrings{
		Group: map[string]string{"image": "Image", "exposure": "Exposure", "white_balance": "White balance",
			"framing": "Focus & framing"},
		Control: map[string]string{"brightness": "Brightness", "contrast": "Contrast", "saturation": "Saturation",
			"hue": "Hue", "gamma": "Gamma", "sharpness": "Sharpness", "exposure_auto": "Auto exposure",
			"exposure": "Exposure", "gain": "Gain", "backlight": "Backlight compensation",
			"power_line": "Anti-flicker", "wb_auto": "Auto white balance",
			"wb_temperature": "Colour temperature", "focus_auto": "Auto focus", "focus": "Focus",
			"zoom": "Zoom", "pan": "Pan", "tilt": "Tilt"},
		PowerLine:    [4]string{"Off", "50 Hz", "60 Hz", "Auto"},
		Auto:         "Auto",
		InactiveF:    "Turn off “%s” to change it",
		DefaultF:     "Factory value: %s",
		On:           "on",
		Off:          "off",
		MsF:          "%s ms",
		KelvinF:      "%d K",
		DegreesF:     "%s°",
		Reset:        "Factory settings",
		ResetConfirm: "Puts every setting back as it came from the factory: tap again",
		Hint:         "It applies at once, also while streaming. The camera keeps it even if you unplug it.",
		Empty:        "No camera with image settings",
		NoSettings:   "that camera has no settings now (hidden or unplugged)",
		EmptyHelp: "Plug in a USB camera that has image settings (brightness, exposure, focus…). The board's HDMI, " +
			"most capture cards and network cameras are set on the device itself.",
	}
	cameraFR = CameraStrings{
		Group: map[string]string{"image": "Image", "exposure": "Exposition", "white_balance": "Balance des blancs",
			"framing": "Mise au point et cadrage"},
		Control: map[string]string{"brightness": "Luminosité", "contrast": "Contraste", "saturation": "Saturation",
			"hue": "Teinte", "gamma": "Gamma", "sharpness": "Netteté", "exposure_auto": "Exposition automatique",
			"exposure": "Exposition", "gain": "Gain", "backlight": "Compensation du contre-jour",
			"power_line": "Anti-scintillement", "wb_auto": "Balance des blancs automatique",
			"wb_temperature": "Température de couleur", "focus_auto": "Mise au point automatique",
			"focus": "Mise au point", "zoom": "Zoom", "pan": "Panoramique", "tilt": "Inclinaison"},
		PowerLine:    [4]string{"Désactivé", "50 Hz", "60 Hz", "Auto"},
		Auto:         "Auto",
		InactiveF:    "Désactivez « %s » pour le modifier",
		DefaultF:     "Valeur d'usine : %s",
		On:           "activé",
		Off:          "désactivé",
		MsF:          "%s ms",
		KelvinF:      "%d K",
		DegreesF:     "%s°",
		Reset:        "Réglages d'usine",
		ResetConfirm: "Remet tous ses réglages comme à la sortie d'usine : touchez encore",
		Hint:         "S'applique tout de suite, même en diffusion. La caméra le garde même débranchée.",
		Empty:        "Aucune caméra avec des réglages d'image",
		NoSettings:   "cette caméra n'a pas de réglages pour le moment (masquée ou débranchée)",
		EmptyHelp: "Branchez une caméra USB qui a des réglages d'image (luminosité, exposition, mise au point…). Le " +
			"HDMI de la carte, la plupart des cartes de capture et les caméras réseau se règlent sur l'appareil lui-même.",
	}
)
