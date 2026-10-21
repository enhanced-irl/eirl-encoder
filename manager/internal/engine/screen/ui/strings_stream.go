package ui

// StreamStrings is the Stream group: Destination, Uplinks and Pause (MIXER.md §21). Its tables, here; Strings
// carries them.
type StreamStrings struct {
	// Destination
	DestURL, DestURLHelp, DestNone, DestURLInvalid  string
	DestModeSrtla, DestModeCaller, DestModeListener string
	DestStreamID, DestStreamIDHelp                  string
	DestPassphrase, DestPassphraseHelp              string
	DestPassphraseBadF                              string // %d min, %d max
	DestLatency, DestAudio, DestVideo, DestVideoMin string
	DestABR, DestABROn, DestABROff                  string
	// Uplinks
	UplinksSrtla, UplinksNoSrtla                    string
	UplinksNone, UplinksNoneSub                     string
	UplinksConflict, UplinksNoneUsed, UplinksSingle string
	UplinksHint, UplinksLastConfirm                 string
	UplinkDupIP, UplinkDisabled, UplinkNoInternet   string
	UplinkRateF                                     string // %s Mbps
	UplinkStatsF                                    string // RTT %d ms, %s NAKs per second (SRTLA.md §9.4)
	// UplinkBond: why the SRTLA sender holds a link back (bond: stalled | weak | down)
	UplinkBond map[string]string
	UplinkKind map[string]string
	// UplinkHealth: why a connection has its dot (the app's healthCode, MODEMS.md §5)
	UplinkHealth map[string]string
	// Pause
	PauseText, PauseTextHelp, PauseTextBad string
	PauseTextDefault                       string // what the card says while none is set (the engine's default)
	PauseMute, PauseMuteOn, PauseMuteOff   string
	PauseHelpOn, PauseHelpOff              string
	PauseUnsupported                       string
}

var (
	streamES = StreamStrings{
		UplinksSrtla:       "SRTLA: la emisión se reparte entre los enlaces encendidos",
		UplinksNoSrtla:     "Sin SRTLA: solo se usa la conexión por defecto (srtla:// en el destino)",
		UplinksNone:        "No hay ningún enlace a internet",
		UplinksNoneSub:     "Conecta un cable, un módem USB o una wifi",
		UplinksConflict:    "Dos enlaces con la misma IP (p. ej. dos módems iguales): solo se usa uno. Cambia la red de uno de ellos.",
		UplinksNoneUsed:    "Ningún enlace activo: con SRTLA la emisión no puede salir",
		UplinksSingle:      "Un solo enlace: sin bonding. Si se cae, se cae la emisión",
		UplinksHint:        "Con SRTLA la emisión se reparte entre todos los enlaces encendidos. Apagar uno lo saca del reparto sin cortar.",
		UplinksLastConfirm: "Sin ningún enlace la emisión SRTLA se corta: toca otra vez para apagarlo",
		UplinkDupIP:        "IP repetida",
		UplinkDisabled:     "Fuera del reparto",
		UplinkNoInternet:   "Sin internet",
		UplinkRateF:        "↑ %s Mbps",
		UplinkStatsF:       "RTT %d ms · %s NAK/s",
		UplinkBond:         map[string]string{"stalled": "atascado: fuera del reparto", "weak": "débil: lleva poco", "down": "no responde"},
		UplinkKind:         map[string]string{"ethernet": "Ethernet", "usb": "USB (módem o móvil)", "wifi": "Wifi", "modem": "Módem"},
		UplinkHealth: map[string]string{"ok": "bien", "off": "apagada", "ip_conflict": "IP repetida",
			"disconnected": "desconectada", "searching": "buscando red", "sim_missing": "sin SIM",
			"sim_pin": "la SIM pide el PIN", "sim_puk": "SIM bloqueada (PUK)", "sim_error": "error de la SIM",
			"modem_failed": "módem en error", "denied": "la red rechaza la SIM", "roaming_off": "itinerancia apagada",
			"no_internet": "sin internet", "signal_minimal": "señal mínima", "signal_weak": "señal débil",
			"slow_tech": "red lenta (2G/3G)", "connecting": "conectando", "link_weak": "enlace flojo"},
		DestURL:            "URL SRT o SRTLA",
		DestURLHelp:        "La de tu clave de EnhancedIRL tal cual: si lleva la clave, se guarda aparte. srtla:// reparte la emisión entre los enlaces",
		DestNone:           "Sin destino",
		DestURLInvalid:     "No válida: vuelve a escribirla",
		DestModeSrtla:      "SRTLA: reparte la emisión entre los enlaces",
		DestModeCaller:     "SRT al relay",
		DestModeListener:   "SRT en escucha",
		DestStreamID:       "Stream ID / clave",
		DestStreamIDHelp:   "Solo se ven puntos (el ojo la enseña) · vacía: se quita",
		DestPassphrase:     "Passphrase",
		DestPassphraseHelp: "De 10 a 79 caracteres · solo se ven puntos (el ojo la enseña) · vacía: se quita",
		DestPassphraseBadF: "La passphrase tiene de %d a %d caracteres",
		DestLatency:        "Latencia (ms)",
		DestAudio:          "Audio (kbps)",
		DestVideo:          "Vídeo (kbps)",
		DestVideoMin:       "Vídeo mínimo (kbps)",
		DestABR:            "Bitrate adaptativo",
		DestABROn:          "Baja el vídeo cuando la red no da para más y lo sube despacio",
		DestABROff:         "El vídeo va siempre al mismo bitrate",
		PauseText:          "Texto del cartel",
		PauseTextHelp:      "Hasta 60 caracteres: letras, números, espacios y . , : ; ! ? ' \" ( ) - / + # @ % $ *",
		PauseTextBad:       "Solo letras, números, espacios y . , : ; ! ? ' \" ( ) - / + # @ % $ *",
		PauseTextDefault:   "Volvemos enseguida",
		PauseMute:          "Silenciar el audio durante la pausa",
		PauseMuteOn:        "Con la pausa puesta, el programa no suena",
		PauseMuteOff:       "Con la pausa puesta, el audio sigue sonando",
		PauseHelpOn:        "La pausa está puesta: el cartel tapa el programa. Se quita con el botón «Pausa» del mezclador.",
		PauseHelpOff:       "El cartel tapa el programa mientras la pausa está puesta. Se pone y se quita con el botón «Pausa» del mezclador.",
		PauseUnsupported:   "Este motor no tiene pausa",
	}
	streamEN = StreamStrings{
		UplinksSrtla:       "SRTLA: the stream is shared among the uplinks that are on",
		UplinksNoSrtla:     "No SRTLA: only the default connection is used (srtla:// in the destination)",
		UplinksNone:        "There is no internet uplink",
		UplinksNoneSub:     "Plug in a cable, a USB modem or join a Wi-Fi",
		UplinksConflict:    "Two uplinks with the same IP (e.g. two identical modems): only one is used. Change the network of one of them.",
		UplinksNoneUsed:    "No active uplink: with SRTLA the stream cannot go out",
		UplinksSingle:      "A single uplink: no bonding. If it drops, the stream drops",
		UplinksHint:        "With SRTLA the stream is shared among all the uplinks that are on. Turning one off takes it out of the share without cutting.",
		UplinksLastConfirm: "Without any uplink the SRTLA stream is cut: tap again to turn it off",
		UplinkDupIP:        "Repeated IP",
		UplinkDisabled:     "Out of the share",
		UplinkNoInternet:   "No internet",
		UplinkRateF:        "↑ %s Mbps",
		UplinkStatsF:       "RTT %d ms · %s NAK/s",
		UplinkBond:         map[string]string{"stalled": "stalled: out of the share", "weak": "weak: carries little", "down": "not answering"},
		UplinkKind:         map[string]string{"ethernet": "Ethernet", "usb": "USB (modem or phone)", "wifi": "Wi-Fi", "modem": "Modem"},
		UplinkHealth: map[string]string{"ok": "fine", "off": "off", "ip_conflict": "repeated IP",
			"disconnected": "disconnected", "searching": "searching for a network", "sim_missing": "no SIM",
			"sim_pin": "the SIM asks for its PIN", "sim_puk": "SIM blocked (PUK)", "sim_error": "SIM error",
			"modem_failed": "modem error", "denied": "the network refuses the SIM", "roaming_off": "roaming is off",
			"no_internet": "no internet", "signal_minimal": "minimal signal", "signal_weak": "weak signal",
			"slow_tech": "slow network (2G/3G)", "connecting": "connecting", "link_weak": "struggling link"},
		DestURL:            "SRT or SRTLA URL",
		DestURLHelp:        "Your EnhancedIRL key's URL as is: a key in it is kept apart. srtla:// spreads the stream over the uplinks",
		DestNone:           "No destination",
		DestURLInvalid:     "Not valid: type it again",
		DestModeSrtla:      "SRTLA: spreads the stream over the uplinks",
		DestModeCaller:     "SRT to the relay",
		DestModeListener:   "SRT listening",
		DestStreamID:       "Stream ID / key",
		DestStreamIDHelp:   "Only dots show (the eye shows it) · empty: it is removed",
		DestPassphrase:     "Passphrase",
		DestPassphraseHelp: "10 to 79 characters · only dots show (the eye shows it) · empty: it is removed",
		DestPassphraseBadF: "The passphrase has %d to %d characters",
		DestLatency:        "Latency (ms)",
		DestAudio:          "Audio (kbps)",
		DestVideo:          "Video (kbps)",
		DestVideoMin:       "Minimum video (kbps)",
		DestABR:            "Adaptive bitrate",
		DestABROn:          "Lowers the video when the network cannot keep up, and raises it slowly",
		DestABROff:         "The video always goes at the same bitrate",
		PauseText:          "Card text",
		PauseTextHelp:      "Up to 60 characters: letters, digits, spaces and . , : ; ! ? ' \" ( ) - / + # @ % $ *",
		PauseTextBad:       "Only letters, digits, spaces and . , : ; ! ? ' \" ( ) - / + # @ % $ *",
		PauseTextDefault:   "Volvemos enseguida",
		PauseMute:          "Mute the audio during the pause",
		PauseMuteOn:        "While paused, the program is silent",
		PauseMuteOff:       "While paused, the audio keeps playing",
		PauseHelpOn:        "The pause is on: the card covers the program. Take it off with the mixer's “Pause” button.",
		PauseHelpOff:       "The card covers the program while the pause is on. Put it on and off with the mixer's “Pause” button.",
		PauseUnsupported:   "This engine has no pause",
	}
	streamFR = StreamStrings{
		UplinksSrtla:       "SRTLA : la diffusion se répartit sur les liaisons actives",
		UplinksNoSrtla:     "Sans SRTLA : seule la connexion par défaut est utilisée (srtla:// dans la destination)",
		UplinksNone:        "Aucune liaison à Internet",
		UplinksNoneSub:     "Branchez un câble, un modem USB ou rejoignez un Wi-Fi",
		UplinksConflict:    "Deux liaisons avec la même IP (p. ex. deux modems identiques) : une seule est utilisée. Changez le réseau de l'un d'eux.",
		UplinksNoneUsed:    "Aucune liaison active : avec SRTLA, la diffusion ne peut pas sortir",
		UplinksSingle:      "Une seule liaison : pas d'agrégation. Si elle tombe, la diffusion tombe",
		UplinksHint:        "Avec SRTLA, la diffusion se répartit sur toutes les liaisons actives. En couper une la retire de la répartition sans coupure.",
		UplinksLastConfirm: "Sans aucune liaison, la diffusion SRTLA est coupée : touchez encore pour la couper",
		UplinkDupIP:        "IP en double",
		UplinkDisabled:     "Hors répartition",
		UplinkNoInternet:   "Pas d'Internet",
		UplinkRateF:        "↑ %s Mbit/s",
		UplinkStatsF:       "RTT %d ms · %s NAK/s",
		UplinkBond:         map[string]string{"stalled": "bloquée : hors du partage", "weak": "faible : transporte peu", "down": "ne répond pas"},
		UplinkKind:         map[string]string{"ethernet": "Ethernet", "usb": "USB (modem ou téléphone)", "wifi": "Wi-Fi", "modem": "Modem"},
		UplinkHealth: map[string]string{"ok": "OK", "off": "désactivée", "ip_conflict": "IP en double",
			"disconnected": "déconnectée", "searching": "recherche du réseau", "sim_missing": "pas de SIM",
			"sim_pin": "la SIM demande son PIN", "sim_puk": "SIM bloquée (PUK)", "sim_error": "erreur de SIM",
			"modem_failed": "modem en erreur", "denied": "le réseau refuse la SIM", "roaming_off": "itinérance désactivée",
			"no_internet": "pas d'internet", "signal_minimal": "signal minimal", "signal_weak": "signal faible",
			"slow_tech": "réseau lent (2G/3G)", "connecting": "connexion…", "link_weak": "liaison faible"},
		DestURL:            "URL SRT ou SRTLA",
		DestURLHelp:        "Celle de votre clé EnhancedIRL telle quelle : la clé qu'elle contient est gardée à part. srtla:// répartit la diffusion sur les liaisons",
		DestNone:           "Pas de destination",
		DestURLInvalid:     "Non valide : saisissez-la à nouveau",
		DestModeSrtla:      "SRTLA : répartit la diffusion sur les liaisons",
		DestModeCaller:     "SRT vers le relais",
		DestModeListener:   "SRT en écoute",
		DestStreamID:       "Stream ID / clé",
		DestStreamIDHelp:   "Seuls des points s'affichent (l'œil la montre) · vide : elle est retirée",
		DestPassphrase:     "Passphrase",
		DestPassphraseHelp: "De 10 à 79 caractères · seuls des points s'affichent (l'œil la montre) · vide : elle est retirée",
		DestPassphraseBadF: "La passphrase a de %d à %d caractères",
		DestLatency:        "Latence (ms)",
		DestAudio:          "Audio (kbit/s)",
		DestVideo:          "Vidéo (kbit/s)",
		DestVideoMin:       "Vidéo minimale (kbit/s)",
		DestABR:            "Débit adaptatif",
		DestABROn:          "Baisse la vidéo quand le réseau ne suit pas, et la remonte doucement",
		DestABROff:         "La vidéo garde toujours le même débit",
		PauseText:          "Texte du carton",
		PauseTextHelp:      "Jusqu'à 60 caractères : lettres, chiffres, espaces et . , : ; ! ? ' \" ( ) - / + # @ % $ *",
		PauseTextBad:       "Seulement lettres, chiffres, espaces et . , : ; ! ? ' \" ( ) - / + # @ % $ *",
		PauseTextDefault:   "Volvemos enseguida",
		PauseMute:          "Couper l'audio pendant la pause",
		PauseMuteOn:        "En pause, le programme est muet",
		PauseMuteOff:       "En pause, l'audio continue",
		PauseHelpOn:        "La pause est active : le carton couvre le programme. Retirez-la avec le bouton « Pause » du mixeur.",
		PauseHelpOff:       "Le carton couvre le programme pendant la pause. Activez-la et retirez-la avec le bouton « Pause » du mixeur.",
		PauseUnsupported:   "Ce moteur n'a pas de pause",
	}
)
