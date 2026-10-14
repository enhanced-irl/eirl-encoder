package ui

// NetStrings is the Wi-Fi page's channel and new password, and the Network cameras page (its switches, adding one, a camera's page) (MIXER.md §21). Its tables, here; Strings carries them.
type NetStrings struct {
	// The Wi-Fi page: the password's field, the 2.4 GHz channel and a new password
	Channel         string // the field's title and the picker's
	ChannelAuto     string
	ChannelAutoSub  string
	ChannelNumF     string // %d = a channel (the picker's rows)
	ChannelInUseF   string // %s = the saved one (ChannelAuto or a number), %d = the one in use
	ChannelApplying string // the notice once one is chosen
	NewPassword     string
	NewPasswordAsk  string // the first tap's note (the second one changes it)
	NewPasswordDone string
	Applying        string // a button while its change is under way
	// The Network cameras page: how they come in (two switches)
	ViaWifi, ViaCable string
	ViaOff            string
	ViaAPOff          string // «Crear mi wifi» off: nothing comes in over the Wi-Fi
	ViaReceivingF     string // %s = the Wi-Fi's name
	ViaReceiving      string // the cable, receiving
	StateErrorF       string // %s = the app's reason
	NoIP              string // the cable has no address
	CableNoIPF        string // %s = the interface (eth0)
	CableWhenAdd      string // on, waiting for the first camera
	ViaWifiOffAsk     string // turning one off with cameras: the first tap's note
	ViaCableOffAsk    string
	// The list of cameras
	CamWaiting, CamConnected string
	CamByWifi, CamByCable    string
	CamLostF                 string // %s = the percentage (decimal separator of the language)
	AddCamera, Adding        string
	CamNameTitle             string // the keyboard's title (adding one, renaming it)
	CamNameHelp, CamNameBad  string
	CamsFullF                string // %d = the most there can be
	AddedF                   string // %s = its name
	TapCamera                string
	AddNeedsReception        string // adding a camera with the reception off: the app refuses it
	Removed                  string
	// A camera's own view
	CamName             string // its name's field
	NewKey, NewKeyAsk   string
	NewKeyDone          string
	Remove, RemoveAsk   string
	RemoveLastAsk       string
	WayWifi, WayCable   string // the selector between its two ways in
	SRTTitle, RTMPTitle string
	Reading             string // an eye opened: the app is asked
	StepsWifiF          string // %s = the Wi-Fi's name
	StepsWifiOff        string // «Crear mi wifi» off
	StepsCable          string
	QRClosed            string // the QR's place with the SRT eye closed
	CamGone             string // the camera is no longer there (removed from the web)
}

var (
	netES = NetStrings{
		Channel:           "Canal (2,4 GHz)",
		ChannelAuto:       "Automático",
		ChannelAutoSub:    "El codificador elige el canal más libre",
		ChannelNumF:       "Canal %d",
		ChannelInUseF:     "%s · en uso: %d",
		ChannelApplying:   "Cambiando el canal: las cámaras y los móviles se desconectan un momento",
		NewPassword:       "Nueva contraseña",
		NewPasswordAsk:    "Todas las cámaras y los móviles se desconectan y hay que volver a unirlos con la contraseña nueva. Toca otra vez para cambiarla",
		NewPasswordDone:   "Contraseña nueva: vuelve a unir las cámaras y los móviles con el QR",
		Applying:          "Aplicando…",
		ViaWifi:           "Por la wifi del codificador",
		ViaCable:          "Por cable",
		ViaOff:            "Apagado",
		ViaAPOff:          "«Crear mi wifi» está apagada: por aquí no entran",
		ViaReceivingF:     "Recibiendo · %s",
		ViaReceiving:      "Recibiendo",
		StateErrorF:       "Con error: %s",
		NoIP:              "sin IP",
		CableNoIPF:        "Sin IP en %s: conecta su cable de red",
		CableWhenAdd:      "al añadir una cámara",
		ViaWifiOffAsk:     "Se desconectan las cámaras que entran por la wifi del codificador (la wifi sigue encendida). Toca otra vez para apagarlo",
		ViaCableOffAsk:    "Se desconectan las cámaras que emiten por cable. Toca otra vez para apagarlo",
		CamWaiting:        "Esperando a la cámara",
		CamConnected:      "Conectada",
		CamByWifi:         "por wifi",
		CamByCable:        "por cable",
		CamLostF:          "%s %% perdido",
		AddCamera:         "Añadir cámara",
		Adding:            "Añadiendo…",
		CamNameTitle:      "Nombre de la cámara",
		CamNameHelp:       "De 1 a 32: letras, números, espacios y . _ ( ) ' -",
		CamNameBad:        "Solo letras, números, espacios y . _ ( ) ' -",
		CamsFullF:         "Ya hay %d: es el máximo de cámaras",
		AddedF:            "«%s» añadida: tócala para ver cómo conectarla",
		TapCamera:         "Toca una cámara para ver su URL y su clave",
		AddNeedsReception: "Enciende la recepción para añadir cámaras",
		Removed:           "Cámara quitada",
		CamName:           "Nombre",
		NewKey:            "Nueva clave",
		NewKeyAsk:         "La cámara que emita con la clave de ahora se desconecta y hay que ponerle la nueva. Toca otra vez para cambiarla",
		NewKeyDone:        "Clave nueva: abre un ojo para ver las URL nuevas",
		Remove:            "Quitar",
		RemoveAsk:         "Se borra la cámara con su clave. Toca otra vez para quitarla",
		RemoveLastAsk:     "Es la última: el codificador deja de recibir cámaras. Toca otra vez para quitarla",
		WayWifi:           "Por wifi",
		WayCable:          "Por cable",
		SRTTitle:          "SRT (recomendado, latencia 500 ms)",
		RTMPTitle:         "RTMP: servidor y clave",
		Reading:           "Leyendo…",
		StepsWifiF:        "Une la cámara a la wifi «%s» (en «Wifi», con su QR) y pon en el emisor una de estas URL.",
		StepsWifiOff:      "Por wifi no entran: enciende «Crear mi wifi» en «Wifi».",
		StepsCable:        "La cámara, en la misma red que el codificador (por cable o por la wifi de ese sitio). Pon en el emisor una de estas URL.",
		QRClosed:          "El QR con la URL SRT se ve al abrir su ojo",
		CamGone:           "Esta cámara ya no está",
	}
	netEN = NetStrings{
		Channel:           "Channel (2.4 GHz)",
		ChannelAuto:       "Automatic",
		ChannelAutoSub:    "The encoder picks the least busy channel",
		ChannelNumF:       "Channel %d",
		ChannelInUseF:     "%s · in use: %d",
		ChannelApplying:   "Changing the channel: cameras and phones disconnect for a moment",
		NewPassword:       "New password",
		NewPasswordAsk:    "Every camera and phone disconnects and has to join again with the new password. Tap again to change it",
		NewPasswordDone:   "New password: join the cameras and phones again with the QR",
		Applying:          "Applying…",
		ViaWifi:           "Over the encoder's Wi-Fi",
		ViaCable:          "By cable",
		ViaOff:            "Off",
		ViaAPOff:          "“Create my Wi-Fi” is off: nothing comes in this way",
		ViaReceivingF:     "Receiving · %s",
		ViaReceiving:      "Receiving",
		StateErrorF:       "Error: %s",
		NoIP:              "no IP",
		CableNoIPF:        "No IP on %s: plug in its network cable",
		CableWhenAdd:      "when a camera is added",
		ViaWifiOffAsk:     "Cameras coming in over the encoder's Wi-Fi disconnect (the Wi-Fi stays on). Tap again to turn it off",
		ViaCableOffAsk:    "Cameras streaming by cable disconnect. Tap again to turn it off",
		CamWaiting:        "Waiting for the camera",
		CamConnected:      "Connected",
		CamByWifi:         "over Wi-Fi",
		CamByCable:        "by cable",
		CamLostF:          "%s %% lost",
		AddCamera:         "Add camera",
		Adding:            "Adding…",
		CamNameTitle:      "Camera name",
		CamNameHelp:       "1 to 32: letters, digits, spaces and . _ ( ) ' -",
		CamNameBad:        "Only letters, digits, spaces and . _ ( ) ' -",
		CamsFullF:         "There are %d: that is the most cameras",
		AddedF:            "“%s” added: tap it to see how to connect it",
		TapCamera:         "Tap a camera to see its URL and key",
		AddNeedsReception: "Turn the reception on to add cameras",
		Removed:           "Camera removed",
		CamName:           "Name",
		NewKey:            "New key",
		NewKeyAsk:         "The camera streaming with the current key disconnects and needs the new one. Tap again to change it",
		NewKeyDone:        "New key: open an eye to see the new URLs",
		Remove:            "Remove",
		RemoveAsk:         "The camera and its key are deleted. Tap again to remove it",
		RemoveLastAsk:     "It is the last one: the encoder stops receiving cameras. Tap again to remove it",
		WayWifi:           "Over Wi-Fi",
		WayCable:          "By cable",
		SRTTitle:          "SRT (recommended, 500 ms latency)",
		RTMPTitle:         "RTMP: server and key",
		Reading:           "Reading…",
		StepsWifiF:        "Join the camera to the “%s” Wi-Fi (on the “Wi-Fi” page, with its QR) and put one of these URLs in the sender.",
		StepsWifiOff:      "Nothing comes in over Wi-Fi: turn on “Create my Wi-Fi” on the “Wi-Fi” page.",
		StepsCable:        "The camera on the same network as the encoder (by cable or over that place's Wi-Fi). Put one of these URLs in the sender.",
		QRClosed:          "The QR with the SRT URL shows when its eye is open",
		CamGone:           "This camera is no longer there",
	}
	netFR = NetStrings{
		Channel:           "Canal (2,4 GHz)",
		ChannelAuto:       "Automatique",
		ChannelAutoSub:    "L'encodeur choisit le canal le moins encombré",
		ChannelNumF:       "Canal %d",
		ChannelInUseF:     "%s · utilisé : %d",
		ChannelApplying:   "Changement de canal : caméras et téléphones se déconnectent un instant",
		NewPassword:       "Nouveau mot de passe",
		NewPasswordAsk:    "Toutes les caméras et tous les téléphones se déconnectent et doivent se reconnecter avec le nouveau mot de passe. Touchez encore pour le changer",
		NewPasswordDone:   "Nouveau mot de passe : reconnectez les caméras et les téléphones avec le QR",
		Applying:          "Application…",
		ViaWifi:           "Par le Wi-Fi de l'encodeur",
		ViaCable:          "Par câble",
		ViaOff:            "Coupé",
		ViaAPOff:          "« Créer mon Wi-Fi » est coupé : rien n'arrive par ici",
		ViaReceivingF:     "Réception · %s",
		ViaReceiving:      "Réception",
		StateErrorF:       "Erreur : %s",
		NoIP:              "sans IP",
		CableNoIPF:        "Pas d'IP sur %s : branchez son câble réseau",
		CableWhenAdd:      "à l'ajout d'une caméra",
		ViaWifiOffAsk:     "Les caméras qui arrivent par le Wi-Fi de l'encodeur se déconnectent (le Wi-Fi reste actif). Touchez encore pour le couper",
		ViaCableOffAsk:    "Les caméras qui diffusent par câble se déconnectent. Touchez encore pour le couper",
		CamWaiting:        "En attente de la caméra",
		CamConnected:      "Connectée",
		CamByWifi:         "par Wi-Fi",
		CamByCable:        "par câble",
		CamLostF:          "%s %% perdu",
		AddCamera:         "Ajouter une caméra",
		Adding:            "Ajout…",
		CamNameTitle:      "Nom de la caméra",
		CamNameHelp:       "De 1 à 32 : lettres, chiffres, espaces et . _ ( ) ' -",
		CamNameBad:        "Seulement lettres, chiffres, espaces et . _ ( ) ' -",
		CamsFullF:         "Il y en a %d : c'est le maximum de caméras",
		AddedF:            "« %s » ajoutée : touchez-la pour voir comment la connecter",
		TapCamera:         "Touchez une caméra pour voir son URL et sa clé",
		AddNeedsReception: "Activez la réception pour ajouter des caméras",
		Removed:           "Caméra retirée",
		CamName:           "Nom",
		NewKey:            "Nouvelle clé",
		NewKeyAsk:         "La caméra qui diffuse avec la clé actuelle se déconnecte et a besoin de la nouvelle. Touchez encore pour la changer",
		NewKeyDone:        "Nouvelle clé : ouvrez un œil pour voir les nouvelles URL",
		Remove:            "Retirer",
		RemoveAsk:         "La caméra et sa clé sont supprimées. Touchez encore pour la retirer",
		RemoveLastAsk:     "C'est la dernière : l'encodeur ne reçoit plus de caméras. Touchez encore pour la retirer",
		WayWifi:           "Par Wi-Fi",
		WayCable:          "Par câble",
		SRTTitle:          "SRT (recommandé, latence 500 ms)",
		RTMPTitle:         "RTMP : serveur et clé",
		Reading:           "Lecture…",
		StepsWifiF:        "Connectez la caméra au Wi-Fi « %s » (page « Wi-Fi », avec son QR) et mettez une de ces URL dans l'émetteur.",
		StepsWifiOff:      "Rien n'arrive par Wi-Fi : activez « Créer mon Wi-Fi » dans la page « Wi-Fi ».",
		StepsCable:        "La caméra sur le même réseau que l'encodeur (par câble ou par le Wi-Fi du lieu). Mettez une de ces URL dans l'émetteur.",
		QRClosed:          "Le QR de l'URL SRT apparaît quand son œil est ouvert",
		CamGone:           "Cette caméra n'existe plus",
	}
)
