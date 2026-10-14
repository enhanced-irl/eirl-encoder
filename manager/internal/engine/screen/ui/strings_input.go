package ui

// InputStrings is the Sources group: the Input tab (the main source, how it is captured, EDID, audio) and the slots' Hide (MIXER.md §21). Its tables, here; Strings carries them.
type InputStrings struct {
	// The Slots tab: each slot's Hide / Show (the mixer's, live), the slot's «hidden» under its number, and the rule
	Hide, Show, Hidden string
	HideHelp           string
	// The Input tab: its fields' titles
	Source, CaptureMode, EDID, Audio string
	// «Scan» (GET /sources again) and what it says after
	Scan, Scanned string
	// The source's choices besides the catalogue's: any USB, a saved USB that is not there, the board's HDMI saved
	// but not detected, the test pattern (and its line)
	USBAuto, USBSaved, HDMIMissing string
	Test, TestSub                  string
	AutoNowF                       string // %s = the source «USB: the first one found» takes now
	USB3, USB2, USB                string
	// The capture mode: automatic (with what it takes, or without a catalogue), a choice's uncompressed format
	ModeAutoF   string // %s = the automatic choice (1920×1080 @ 30 · MJPEG)
	ModeAuto    string
	Raw         string
	SavedF      string // %s = a saved value the catalogue does not list
	ResizedF    string // %s = the choice, %s = what it is streamed at (… → 1280×720)
	ChoiceF     string // %s = width×height, %s = fps, %s = format
	HasAudioF   string // %s = the device's audio
	BusShareF   string // %d = percent of the USB bus
	CaptureHint string
	// EDID: the recommended one, and what it means
	EDIDRecommended, EDIDHint string
	// Audio: automatic (the source's), none, a saved one that is not connected
	AudioAuto, AudioNone string
	NotConnectedF        string // %s = the saved audio's name
	// What runs now, if it is not what is saved
	InUseF      string // %s = name, %s = format, %s = width×height
	WaitingUSB  string
	EDIDActiveF string // %s = the EDID in use
	// What the main source is (with the mixer every source is captured at once)
	MixerHint string
	// Why a USB source may not work well (GET /sources warnings: usb2, only_4k, no_format, unreadable)
	Warn map[string]string
	// Saving: under way; the destination is wrong (the whole configuration is saved at once)
	Saving     string
	NeedDest   string
	CheckDestF string // %s = the app's message
}

var (
	inputES = InputStrings{
		Hide:            "Ocultar",
		Show:            "Mostrar",
		Hidden:          "oculta",
		HideHelp:        "«Ocultar» quita la fuente del mezclador al momento, sin «Guardar». La que está al aire no se puede ocultar.",
		Source:          "Fuente principal",
		CaptureMode:     "Modo de captura",
		EDID:            "Modo que pide el aparato (EDID)",
		Audio:           "Audio",
		Scan:            "Buscar",
		Scanned:         "Fuentes actualizadas",
		USBAuto:         "USB: la primera que haya",
		USBSaved:        "Dispositivo USB guardado (no conectado)",
		HDMIMissing:     "HDMI de la placa (no detectado)",
		Test:            "Patrón de prueba",
		TestSub:         "Sin cámara: imagen y tono de prueba",
		AutoNowF:        "Ahora: %s",
		USB3:            "USB 3",
		USB2:            "USB 2",
		USB:             "USB",
		ModeAutoF:       "Automático (%s)",
		ModeAuto:        "Automático (el mejor que ofrezca)",
		Raw:             "sin comprimir",
		SavedF:          "%s (guardado)",
		ResizedF:        "%s → %s",
		ChoiceF:         "%s @ %s · %s",
		HasAudioF:       "trae audio (%s)",
		BusShareF:       "ocupa el %d %% del USB: no caben dos así",
		CaptureHint:     "«Sin comprimir» da mejor imagen; MJPEG ocupa menos USB. Por encima de 1080p se emite reescalado a 1080p.",
		EDIDRecommended: "1080p30 (recomendado)",
		EDIDHint:        "La cámara puede elegir otro modo de la lista. Muchas solo dan 50 fps en PAL (50 Hz); en NTSC (60 Hz) se quedan en 1080p30. 1080p60 no es posible por esta entrada: usa una capturadora USB 3.",
		AudioAuto:       "Automático (el de la fuente)",
		AudioNone:       "Sin audio",
		NotConnectedF:   "%s (no conectada)",
		InUseF:          "En uso: %s · %s %s",
		WaitingUSB:      "La emisión está esperando a que se conecte la fuente USB.",
		EDIDActiveF:     "EDID en uso: %s. Lo guardado se aplicará al reiniciar la emisión.",
		MixerHint:       "El mezclador captura todas las fuentes a la vez: esta es la que va al aire al arrancar y la que da el audio. Lo que se emite se cambia en el mezclador, sin reiniciar.",
		Warn: map[string]string{
			"usb2":       "Va por USB 2: esta capturadora necesita USB 3 (puerto y cable USB 3) o dará pocos fps.",
			"only_4k":    "La cámara envía más de 1080p y eso no cabe sin comprimir por USB: ponla a 1080p.",
			"no_format":  "No ofrece ningún formato que este aparato sepa capturar.",
			"unreadable": "No se puede leer ahora mismo (¿se está reconectando?). Prueba «Buscar» en unos segundos.",
		},
		Saving:     "Guardando…",
		NeedDest:   "Falta el destino: configúralo en «Destino» o deja de emitir.",
		CheckDestF: "Revisa el destino: %s",
	}
	inputEN = InputStrings{
		Hide:            "Hide",
		Show:            "Show",
		Hidden:          "hidden",
		HideHelp:        "“Hide” takes the source out of the mixer at once, without “Save”. The one on air cannot be hidden.",
		Source:          "Main source",
		CaptureMode:     "Capture mode",
		EDID:            "Mode the device asks for (EDID)",
		Audio:           "Audio",
		Scan:            "Scan",
		Scanned:         "Sources updated",
		USBAuto:         "USB: the first one found",
		USBSaved:        "Saved USB device (not connected)",
		HDMIMissing:     "Board HDMI (not detected)",
		Test:            "Test pattern",
		TestSub:         "No camera: test picture and tone",
		AutoNowF:        "Now: %s",
		USB3:            "USB 3",
		USB2:            "USB 2",
		USB:             "USB",
		ModeAutoF:       "Automatic (%s)",
		ModeAuto:        "Automatic (the best it offers)",
		Raw:             "uncompressed",
		SavedF:          "%s (saved)",
		ResizedF:        "%s → %s",
		ChoiceF:         "%s @ %s · %s",
		HasAudioF:       "has audio (%s)",
		BusShareF:       "uses %d%% of the USB: two like this do not fit",
		CaptureHint:     "“Uncompressed” gives a better picture; MJPEG uses less USB. Above 1080p it is streamed scaled down to 1080p.",
		EDIDRecommended: "1080p30 (recommended)",
		EDIDHint:        "The camera may pick another mode of the list. Many only give 50 fps in PAL (50 Hz); in NTSC (60 Hz) they stay at 1080p30. 1080p60 is not possible through this input: use a USB 3 capture card.",
		AudioAuto:       "Automatic (the source's)",
		AudioNone:       "No audio",
		NotConnectedF:   "%s (not connected)",
		InUseF:          "In use: %s · %s %s",
		WaitingUSB:      "The stream is waiting for the USB source to be connected.",
		EDIDActiveF:     "EDID in use: %s. What is saved will apply when the stream restarts.",
		MixerHint:       "The mixer captures every source at once: this is the one on air at startup and the one that gives the audio. What is streamed is changed in the mixer, without restarting.",
		Warn: map[string]string{
			"usb2":       "It is on USB 2: this capture card needs USB 3 (USB 3 port and cable) or it will give few fps.",
			"only_4k":    "The camera sends more than 1080p and that does not fit uncompressed over USB: set it to 1080p.",
			"no_format":  "It offers no format this device can capture.",
			"unreadable": "It cannot be read right now (is it reconnecting?). Try “Scan” in a few seconds.",
		},
		Saving:     "Saving…",
		NeedDest:   "No destination: set it in “Destination” or stop streaming.",
		CheckDestF: "Check the destination: %s",
	}
	inputFR = InputStrings{
		Hide:            "Masquer",
		Show:            "Afficher",
		Hidden:          "masquée",
		HideHelp:        "« Masquer » retire la source du mixeur tout de suite, sans « Enregistrer ». Celle qui est à l'antenne ne peut pas être masquée.",
		Source:          "Source principale",
		CaptureMode:     "Mode de capture",
		EDID:            "Mode demandé par l'appareil (EDID)",
		Audio:           "Audio",
		Scan:            "Rechercher",
		Scanned:         "Sources mises à jour",
		USBAuto:         "USB : la première trouvée",
		USBSaved:        "Appareil USB enregistré (non connecté)",
		HDMIMissing:     "HDMI de la carte (non détecté)",
		Test:            "Mire de test",
		TestSub:         "Sans caméra : image et tonalité de test",
		AutoNowF:        "Actuellement : %s",
		USB3:            "USB 3",
		USB2:            "USB 2",
		USB:             "USB",
		ModeAutoF:       "Automatique (%s)",
		ModeAuto:        "Automatique (le meilleur proposé)",
		Raw:             "non compressé",
		SavedF:          "%s (enregistré)",
		ResizedF:        "%s → %s",
		ChoiceF:         "%s @ %s · %s",
		HasAudioF:       "a de l'audio (%s)",
		BusShareF:       "occupe %d %% de l'USB : deux comme celle-ci ne tiennent pas",
		CaptureHint:     "« Non compressé » donne une meilleure image ; MJPEG occupe moins l'USB. Au-dessus de 1080p, c'est diffusé redimensionné en 1080p.",
		EDIDRecommended: "1080p30 (recommandé)",
		EDIDHint:        "La caméra peut choisir un autre mode de la liste. Beaucoup ne donnent 50 i/s qu'en PAL (50 Hz) ; en NTSC (60 Hz), elles restent en 1080p30. Le 1080p60 n'est pas possible par cette entrée : utilisez une carte de capture USB 3.",
		AudioAuto:       "Automatique (celui de la source)",
		AudioNone:       "Sans audio",
		NotConnectedF:   "%s (non connectée)",
		InUseF:          "En cours : %s · %s %s",
		WaitingUSB:      "La diffusion attend que la source USB soit branchée.",
		EDIDActiveF:     "EDID en cours : %s. Ce qui est enregistré s'appliquera au redémarrage de la diffusion.",
		MixerHint:       "Le mixeur capture toutes les sources en même temps : celle-ci passe à l'antenne au démarrage et fournit l'audio. Ce qui est diffusé se change dans le mixeur, sans redémarrer.",
		Warn: map[string]string{
			"usb2":       "Elle est en USB 2 : cette carte de capture a besoin de l'USB 3 (port et câble USB 3), sinon elle donnera peu d'images par seconde.",
			"only_4k":    "La caméra envoie plus que du 1080p et cela ne passe pas non compressé par l'USB : réglez-la en 1080p.",
			"no_format":  "Elle ne propose aucun format que cet appareil sait capturer.",
			"unreadable": "Lecture impossible pour l'instant (se reconnecte-t-elle ?). Essayez « Rechercher » dans quelques secondes.",
		},
		Saving:     "Enregistrement…",
		NeedDest:   "Pas de destination : configurez-la dans « Destination » ou arrêtez la diffusion.",
		CheckDestF: "Vérifiez la destination : %s",
	}
)
