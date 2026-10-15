package ui

// DestStrings is Stream › Destination with saved destinations (OUTPUTS.md §11): the list, the template picker and a
// destination's page. The fields it shares with the single destination (stream ID, passphrase, latency, bitrates)
// are StreamStrings'.
type DestStrings struct {
	// The list
	Add, AddTitle, AddFullF           string // AddFullF: %d destinations at most
	ListEmpty, ListEmptySub           string
	ListHint                          string
	ActiveTag, ManagedTag             string
	Activating, ActivateConfirm       string
	Created                           string
	Custom                            string // the Custom template's name (the others are proper names)
	TemplateSub                       string // the picker's line under a template with a server of its own
	Gone                              string // the open destination is not in the app's list any more
	Delete, DeleteConfirm, NewUnsaved string
	// The page's two sections
	SecConnection, SecVideo string
	// Connection
	Name, NameHelp         string
	Protocol               string
	ProtocolSub            map[string]string // by protocol: what it does, in the picker
	Server                 string
	ServerHelp             map[string]string // by protocol: the URL's form
	Key, KeyHelp           string
	Profile                string
	ProfileNames           map[string]string // simple | main
	ProfileSub             map[string]string
	Buffer                 string
	User, UserHelp         string
	Password, PasswordHelp string
	SingleLinkF            string // %s the protocol: it leaves through one link
	// Video
	Codec     string
	CodecSub  map[string]string // h264 | h265
	Size      string
	SizeNames map[string]string // program | 1280x720 | 960x540
	Fps       string
	FpsNames  map[string]string // full | half
}

var (
	destsES = DestStrings{
		Add:             "Añadir",
		AddTitle:        "Nuevo destino",
		AddFullF:        "Caben %d destinos como mucho: borra uno antes",
		ListEmpty:       "No hay destinos guardados",
		ListEmptySub:    "Toca «Añadir» para crear uno",
		ListHint:        "Toca un destino para emitir a él · › para verlo o cambiarlo",
		ActiveTag:       "En uso",
		ManagedTag:      "de tu cuenta",
		Activating:      "Cambiando…",
		ActivateConfirm: "Estás emitiendo: al cambiar, la emisión se corta 1-2 s. Toca otra vez",
		Created:         "Guardado: tócalo en la lista para emitir a él",
		Custom:          "Personalizado",
		TemplateSub:     "Su servidor ya viene puesto",
		Gone:            "Este destino ya no existe",
		Delete:          "Borrar",
		DeleteConfirm:   "Se borra este destino: toca otra vez",
		NewUnsaved:      "Sin guardar: «Guardar» lo añade a la lista",
		SecConnection:   "Conexión",
		SecVideo:        "Vídeo y audio",
		Name:            "Nombre",
		NameHelp:        "De 1 a 32 caracteres: cómo se llama en la lista",
		Protocol:        "Protocolo",
		ProtocolSub: map[string]string{"srt": "Al servidor por un enlace", "srtla": "Reparte la emisión entre los enlaces",
			"rtmp": "Una sola conexión", "rtmps": "Una sola conexión, cifrada", "rist": "Por un enlace, con reenvíos",
			"udp": "A la red local, sin reenvíos"},
		Server: "Servidor (URL)",
		ServerHelp: map[string]string{"srt": "srt://servidor:puerto · si lleva la clave, se guarda aparte",
			"srtla": "srtla://servidor:puerto · si lleva la clave, se guarda aparte",
			"rtmp":  "rtmp://servidor/aplicación · la clave va en su campo",
			"rtmps": "rtmps://servidor:443/aplicación · la clave va en su campo",
			"rist":  "rist://servidor:puerto", "udp": "udp://IP:puerto o rtp://IP:puerto (vale una IP multicast)"},
		Key:          "Clave de retransmisión",
		KeyHelp:      "La de tu plataforma · solo se ven puntos (el ojo la enseña) · vacía: se quita",
		Profile:      "Perfil RIST",
		ProfileNames: map[string]string{"simple": "Simple", "main": "Principal"},
		ProfileSub:   map[string]string{"simple": "Sin cifrado ni usuario", "main": "Con cifrado y usuario"},
		Buffer:       "Búfer (ms)",
		User:         "Usuario",
		UserHelp:     "Letras, números, punto, guion y guion bajo · vacío: sin usuario",
		Password:     "Contraseña",
		PasswordHelp: "Solo se ven puntos (el ojo la enseña) · vacía: se quita",
		SingleLinkF:  "%s sale por un solo enlace: no reparte la emisión entre los demás",
		Codec:        "Códec de vídeo",
		CodecSub:     map[string]string{"h264": "Lo admite todo el mundo", "h265": "La misma calidad con menos bitrate"},
		Size:         "Tamaño de salida",
		SizeNames:    map[string]string{"program": "El del programa", "1280x720": "1280×720 (720p)", "960x540": "960×540 (540p)"},
		Fps:          "Fotogramas",
		FpsNames:     map[string]string{"full": "Los del programa (50/60)", "half": "La mitad (25/30)"},
	}
	destsEN = DestStrings{
		Add:             "Add",
		AddTitle:        "New destination",
		AddFullF:        "There is room for %d destinations at most: delete one first",
		ListEmpty:       "No saved destinations",
		ListEmptySub:    "Touch “Add” to create one",
		ListHint:        "Touch a destination to stream to it · › to see or change it",
		ActiveTag:       "In use",
		ManagedTag:      "from your account",
		Activating:      "Switching…",
		ActivateConfirm: "You are streaming: switching cuts the stream for 1-2 s. Touch again",
		Created:         "Saved: touch it in the list to stream to it",
		Custom:          "Custom",
		TemplateSub:     "Its server is already set",
		Gone:            "This destination no longer exists",
		Delete:          "Delete",
		DeleteConfirm:   "This destination will be deleted: touch again",
		NewUnsaved:      "Not saved: “Save” adds it to the list",
		SecConnection:   "Connection",
		SecVideo:        "Video and audio",
		Name:            "Name",
		NameHelp:        "1 to 32 characters: what the list calls it",
		Protocol:        "Protocol",
		ProtocolSub: map[string]string{"srt": "To the server over one link", "srtla": "Spreads the stream over the links",
			"rtmp": "A single connection", "rtmps": "A single connection, encrypted", "rist": "Over one link, with resends",
			"udp": "To the local network, no resends"},
		Server: "Server (URL)",
		ServerHelp: map[string]string{"srt": "srt://server:port · a key in it is kept apart",
			"srtla": "srtla://server:port · a key in it is kept apart",
			"rtmp":  "rtmp://server/application · the key goes in its own field",
			"rtmps": "rtmps://server:443/application · the key goes in its own field",
			"rist":  "rist://server:port", "udp": "udp://IP:port or rtp://IP:port (a multicast IP works)"},
		Key:          "Stream key",
		KeyHelp:      "Your platform's · only dots show (the eye shows it) · empty: it is removed",
		Profile:      "RIST profile",
		ProfileNames: map[string]string{"simple": "Simple", "main": "Main"},
		ProfileSub:   map[string]string{"simple": "No encryption, no user", "main": "With encryption and a user"},
		Buffer:       "Buffer (ms)",
		User:         "User",
		UserHelp:     "Letters, digits, dot, hyphen and underscore · empty: no user",
		Password:     "Password",
		PasswordHelp: "Only dots show (the eye shows it) · empty: it is removed",
		SingleLinkF:  "%s leaves through a single link: it does not spread the stream over the others",
		Codec:        "Video codec",
		CodecSub:     map[string]string{"h264": "Taken everywhere", "h265": "The same quality with less bitrate"},
		Size:         "Output size",
		SizeNames:    map[string]string{"program": "The program's", "1280x720": "1280×720 (720p)", "960x540": "960×540 (540p)"},
		Fps:          "Frame rate",
		FpsNames:     map[string]string{"full": "The program's (50/60)", "half": "Half (25/30)"},
	}
	destsFR = DestStrings{
		Add:             "Ajouter",
		AddTitle:        "Nouvelle destination",
		AddFullF:        "%d destinations au plus : supprimez-en une avant",
		ListEmpty:       "Aucune destination enregistrée",
		ListEmptySub:    "Touchez « Ajouter » pour en créer une",
		ListHint:        "Touchez une destination pour y diffuser · › pour la voir ou la modifier",
		ActiveTag:       "Utilisée",
		ManagedTag:      "de votre compte",
		Activating:      "Changement…",
		ActivateConfirm: "Vous diffusez : changer coupe la diffusion 1 à 2 s. Touchez à nouveau",
		Created:         "Enregistrée : touchez-la dans la liste pour y diffuser",
		Custom:          "Personnalisée",
		TemplateSub:     "Son serveur est déjà renseigné",
		Gone:            "Cette destination n'existe plus",
		Delete:          "Supprimer",
		DeleteConfirm:   "Cette destination sera supprimée : touchez à nouveau",
		NewUnsaved:      "Non enregistrée : « Enregistrer » l'ajoute à la liste",
		SecConnection:   "Connexion",
		SecVideo:        "Vidéo et audio",
		Name:            "Nom",
		NameHelp:        "De 1 à 32 caractères : son nom dans la liste",
		Protocol:        "Protocole",
		ProtocolSub: map[string]string{"srt": "Vers le serveur par une liaison", "srtla": "Répartit la diffusion sur les liaisons",
			"rtmp": "Une seule connexion", "rtmps": "Une seule connexion, chiffrée", "rist": "Par une liaison, avec renvois",
			"udp": "Vers le réseau local, sans renvois"},
		Server: "Serveur (URL)",
		ServerHelp: map[string]string{"srt": "srt://serveur:port · la clé qu'elle contient est gardée à part",
			"srtla": "srtla://serveur:port · la clé qu'elle contient est gardée à part",
			"rtmp":  "rtmp://serveur/application · la clé va dans son champ",
			"rtmps": "rtmps://serveur:443/application · la clé va dans son champ",
			"rist":  "rist://serveur:port", "udp": "udp://IP:port ou rtp://IP:port (une IP multicast convient)"},
		Key:          "Clé de stream",
		KeyHelp:      "Celle de votre plateforme · seuls des points s'affichent (l'œil la montre) · vide : elle est retirée",
		Profile:      "Profil RIST",
		ProfileNames: map[string]string{"simple": "Simple", "main": "Principal"},
		ProfileSub:   map[string]string{"simple": "Sans chiffrement ni utilisateur", "main": "Avec chiffrement et utilisateur"},
		Buffer:       "Tampon (ms)",
		User:         "Utilisateur",
		UserHelp:     "Lettres, chiffres, point, tiret et tiret bas · vide : sans utilisateur",
		Password:     "Mot de passe",
		PasswordHelp: "Seuls des points s'affichent (l'œil le montre) · vide : il est retiré",
		SingleLinkF:  "%s sort par une seule liaison : il ne répartit pas la diffusion sur les autres",
		Codec:        "Codec vidéo",
		CodecSub:     map[string]string{"h264": "Accepté partout", "h265": "La même qualité avec moins de débit"},
		Size:         "Taille de sortie",
		SizeNames:    map[string]string{"program": "Celle du programme", "1280x720": "1280×720 (720p)", "960x540": "960×540 (540p)"},
		Fps:          "Images par seconde",
		FpsNames:     map[string]string{"full": "Celles du programme (50/60)", "half": "La moitié (25/30)"},
	}
)
