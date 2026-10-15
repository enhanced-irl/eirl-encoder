package ui

// BTStrings is the Bluetooth page (Settings › Wireless › Bluetooth, BLUETOOTH.md §3.6) and the headphones button's
// Bluetooth mark in the audio view. Its tables, here; Strings carries them. Never a name of the stack (BlueZ,
// BlueALSA, ALSA): what the user sees is the encoder.
type BTStrings struct {
	// Unavailable: the page's only line, by the app's reason (no_stack | no_adapter; no_audio from an older app)
	Unavailable map[string]string
	// NoAudioWarn: the warning over the controls when Bluetooth works but its audio does not (no_audio)
	NoAudioWarn string
	// PowerFailed: the switch row's line when the adapter does not follow it (blocked, or its firmware fails)
	PowerFailed string
	// The switch row
	Power        string // its title
	PowerOn      string // on, without a name
	PowerOnNameF string // %s = how the encoder shows to other devices
	PowerOff     string
	OffHelp      string // the page with Bluetooth off
	// Search and the visible window (D4)
	Search, Searching string
	MakeVisible       string
	VisibleF          string // %s = the time left (m:ss); the button while visible (a tap closes it)
	VisibleHelpF      string // %s = the encoder's name: what to do on the phone
	VisibleHelp       string // the same without a name
	// The list
	NoDevices     string // on, nothing paired and nothing found
	SearchingNone string // searching, nothing found yet
	TapDevice     string // the list's quiet note
	Pair          string // a found device's button
	Kind          map[string]string
	Playing       string // the monitor plays through it
	Connected     string
	NotReady      string // connected, its audio not up yet
	NotConnected  string
	NotPaired     string            // a found device
	Pending       map[string]string // an order running, by its name: pair | connect | disconnect | forget
	Errors        map[string]string // why the last order failed: rejected | timeout | not_reachable | gone | failed
	// A paired device's view
	Connect, Disconnect string
	DisconnectAsk       string            // its audio is on air: the first tap's note
	ForgetAsk           string            // the first tap's note (the second one forgets it)
	UsedFor             string            // the uses' title
	Uses                map[string]string // listen | mic | input
	UseHelp             map[string]string // a quiet line under each
	// Mark is the headphones button's mark when the monitor plays over Bluetooth
	Mark string
}

var (
	btES = BTStrings{
		NoAudioWarn: "El audio por Bluetooth no funciona ahora: puedes gestionar los dispositivos, pero no suenan",
		PowerFailed: "El adaptador no se enciende: está bloqueado o falla; se reintenta cada minuto",
		Unavailable: map[string]string{"no_adapter": "Este codificador no tiene módulo Bluetooth",
			"no_stack": "El Bluetooth no está disponible en este sistema",
			"no_audio": "El audio Bluetooth no está instalado: actualiza el codificador"},
		Power: "Bluetooth", PowerOn: "Encendido", PowerOnNameF: "Encendido · aparece como «%s»", PowerOff: "Apagado",
		OffHelp: "Enciéndelo para escuchar el programa con auriculares o usar un micrófono o un móvil como entrada",
		Search:  "Buscar", Searching: "Buscando…", MakeVisible: "Hacer visible", VisibleF: "Visible · %s",
		VisibleHelpF:  "Empareja desde el móvil: busca «%s» en su Bluetooth",
		VisibleHelp:   "Empareja desde el móvil: busca este codificador en su Bluetooth",
		NoDevices:     "Ningún dispositivo: toca «Buscar», o «Hacer visible» para emparejar un móvil",
		SearchingNone: "Buscando dispositivos de audio cerca…", TapDevice: "Toca un dispositivo para conectarlo o elegir su uso",
		Pair: "Emparejar",
		Kind: map[string]string{"headphones": "Auriculares", "headset": "Auriculares con micro", "speaker": "Altavoz",
			"phone": "Móvil", "computer": "Ordenador", "audio": "Audio"},
		Playing: "Suena el programa", Connected: "Conectado", NotReady: "Conectado, preparando el audio…",
		NotConnected: "No conectado", NotPaired: "Sin emparejar",
		Pending: map[string]string{"pair": "Emparejando…", "connect": "Conectando…", "disconnect": "Desconectando…",
			"forget": "Olvidando…"},
		Errors: map[string]string{"rejected": "Rechazó el emparejamiento", "timeout": "No respondió a tiempo",
			"not_reachable": "No responde: ¿está encendido y cerca?", "gone": "Ya no aparece: busca otra vez",
			"failed": "No se ha podido"},
		Connect: "Conectar", Disconnect: "Desconectar",
		DisconnectAsk: "Su audio está al aire: toca otra vez para desconectarlo",
		ForgetAsk:     "Toca otra vez para olvidarlo: habrá que emparejarlo de nuevo",
		UsedFor:       "Para qué se usa",
		Uses: map[string]string{"listen": "Escuchar el programa", "mic": "Micrófono (calidad de teléfono)",
			"input": "Entrada de audio"},
		UseHelp: map[string]string{"listen": "Los auriculares del codificador suenan por él, en estéreo",
			"mic":   "Su micro entra en el mezclador y el programa suena por su auricular",
			"input": "Lo que suena en él entra en el mezclador"},
		Mark: "BT",
	}
	btEN = BTStrings{
		NoAudioWarn: "Bluetooth audio is not working right now: you can manage the devices, but they are not heard",
		PowerFailed: "The adapter does not turn on: it is blocked or failing; retrying every minute",
		Unavailable: map[string]string{"no_adapter": "This encoder has no Bluetooth module",
			"no_stack": "Bluetooth is not available on this system",
			"no_audio": "Bluetooth audio is not installed: update the encoder"},
		Power: "Bluetooth", PowerOn: "On", PowerOnNameF: "On · shows as “%s”", PowerOff: "Off",
		OffHelp: "Turn it on to listen to the program on headphones, or to use a microphone or a phone as an input",
		Search:  "Search", Searching: "Searching…", MakeVisible: "Make visible", VisibleF: "Visible · %s",
		VisibleHelpF:  "Pair from your phone: look for “%s” in its Bluetooth",
		VisibleHelp:   "Pair from your phone: look for this encoder in its Bluetooth",
		NoDevices:     "No devices: tap “Search”, or “Make visible” to pair a phone",
		SearchingNone: "Looking for audio devices nearby…", TapDevice: "Tap a device to connect it or choose its use",
		Pair: "Pair",
		Kind: map[string]string{"headphones": "Headphones", "headset": "Headset", "speaker": "Speaker",
			"phone": "Phone", "computer": "Computer", "audio": "Audio"},
		Playing: "Playing the program", Connected: "Connected", NotReady: "Connected, getting the audio ready…",
		NotConnected: "Not connected", NotPaired: "Not paired",
		Pending: map[string]string{"pair": "Pairing…", "connect": "Connecting…", "disconnect": "Disconnecting…",
			"forget": "Forgetting…"},
		Errors: map[string]string{"rejected": "It refused the pairing", "timeout": "It did not answer in time",
			"not_reachable": "Not answering: is it on and nearby?", "gone": "No longer found: search again",
			"failed": "It did not work"},
		Connect: "Connect", Disconnect: "Disconnect",
		DisconnectAsk: "Its audio is on air: tap again to disconnect it",
		ForgetAsk:     "Tap again to forget it: it will have to be paired again",
		UsedFor:       "Used for",
		Uses: map[string]string{"listen": "Listen to the program", "mic": "Microphone (phone quality)",
			"input": "Audio input"},
		UseHelp: map[string]string{"listen": "The encoder's headphones play through it, in stereo",
			"mic":   "Its microphone goes into the mixer and the program plays in its earpiece",
			"input": "What it plays goes into the mixer"},
		Mark: "BT",
	}
	btFR = BTStrings{
		NoAudioWarn: "L'audio Bluetooth ne fonctionne pas pour l'instant : vous pouvez gérer les appareils, mais ils ne sont pas entendus",
		PowerFailed: "L'adaptateur ne s'allume pas : il est bloqué ou en panne ; nouvel essai chaque minute",
		Unavailable: map[string]string{"no_adapter": "Cet encodeur n'a pas de module Bluetooth",
			"no_stack": "Le Bluetooth n'est pas disponible sur ce système",
			"no_audio": "L'audio Bluetooth n'est pas installé : mettez à jour l'encodeur"},
		Power: "Bluetooth", PowerOn: "Activé", PowerOnNameF: "Activé · apparaît comme « %s »", PowerOff: "Désactivé",
		OffHelp: "Activez-le pour écouter le programme au casque, ou utiliser un micro ou un téléphone comme entrée",
		Search:  "Rechercher", Searching: "Recherche…", MakeVisible: "Rendre visible", VisibleF: "Visible · %s",
		VisibleHelpF:  "Associez depuis le téléphone : cherchez « %s » dans son Bluetooth",
		VisibleHelp:   "Associez depuis le téléphone : cherchez cet encodeur dans son Bluetooth",
		NoDevices:     "Aucun appareil : touchez « Rechercher », ou « Rendre visible » pour associer un téléphone",
		SearchingNone: "Recherche d'appareils audio à proximité…", TapDevice: "Touchez un appareil pour le connecter ou choisir son usage",
		Pair: "Associer",
		Kind: map[string]string{"headphones": "Casque", "headset": "Casque-micro", "speaker": "Enceinte",
			"phone": "Téléphone", "computer": "Ordinateur", "audio": "Audio"},
		Playing: "Diffuse le programme", Connected: "Connecté", NotReady: "Connecté, préparation de l'audio…",
		NotConnected: "Non connecté", NotPaired: "Non associé",
		Pending: map[string]string{"pair": "Association…", "connect": "Connexion…", "disconnect": "Déconnexion…",
			"forget": "Suppression…"},
		Errors: map[string]string{"rejected": "Association refusée", "timeout": "Pas de réponse à temps",
			"not_reachable": "Ne répond pas : est-il allumé et à proximité ?", "gone": "Introuvable : relancez la recherche",
			"failed": "Échec"},
		Connect: "Connecter", Disconnect: "Déconnecter",
		DisconnectAsk: "Son audio est à l'antenne : touchez encore pour le déconnecter",
		ForgetAsk:     "Touchez encore pour l'oublier : il faudra l'associer à nouveau",
		UsedFor:       "Utilisé pour",
		Uses: map[string]string{"listen": "Écouter le programme", "mic": "Micro (qualité téléphone)",
			"input": "Entrée audio"},
		UseHelp: map[string]string{"listen": "Le casque de l'encodeur passe par lui, en stéréo",
			"mic":   "Son micro entre dans le mixeur et le programme passe dans son écouteur",
			"input": "Ce qu'il joue entre dans le mixeur"},
		Mark: "BT",
	}
)
