package ui

// ModemStrings is a modem's page (Streaming › Connections › a modem, MODEMS.md §7). Its tables, here; Strings carries
// them.
type ModemStrings struct {
	RSRP, SINR, RSRQ, NoSignal                  string // the signal tiles
	SIMLabel, NetLabel                          string // the facts line's labels
	SIMState, Reg                               map[string]string
	Network, APN, Roaming, Name                 string // the buttons' labels
	Modes                                       map[string]string
	APNAutoF, APNAutoNet                        string // %s the operator's APN
	RoamingOn, RoamingOff                       string
	NameHelp, APNHelp                           string // the keyboards' help lines
	Unlock, PINTitle, PINHelp, PINRemoveHelp    string
	UnlockOnly, UnlockRemove                    string // the unlock picker: just unlock, or unlock and turn the PIN off
	UnlockKeep, UnlockOnce                      string // what happens next time (ModemManager / HiLink)
	Reset, ResetConfirm, Resetting              string
	Subnet, SubnetConfirm, SubnetDone           string
	Saved, Unlocked, ForceConfirm, NotSupported string
}

var (
	modemES = ModemStrings{
		RSRP: "Potencia (RSRP)", SINR: "Ruido (SINR)", RSRQ: "Calidad (RSRQ)", NoSignal: "Sin datos de señal",
		SIMLabel: "SIM", NetLabel: "Red",
		SIMState: map[string]string{"ready": "lista", "pin": "pide el PIN", "puk": "bloqueada (PUK)", "missing": "sin SIM",
			"error": "error", "unknown": "desconocida"},
		Reg: map[string]string{"home": "en casa", "roaming": "en itinerancia", "searching": "buscando", "denied": "rechazada",
			"idle": "sin registrar", "unknown": "desconocida"},
		Network: "Red", APN: "APN", Roaming: "Itinerancia", Name: "Nombre",
		Modes:    map[string]string{"auto": "Automática", "4g": "Solo 4G", "5g": "4G + 5G"},
		APNAutoF: "automático (%s)", APNAutoNet: "automático",
		RoamingOn: "permitida", RoamingOff: "apagada",
		NameHelp: "Hasta 12 caracteres · vacío: el del operador", APNHelp: "Letras, números, puntos y guiones · vacío: automático",
		Unlock: "Desbloquear la SIM", PINTitle: "PIN de la SIM", PINHelp: "De 4 a 8 cifras",
		PINRemoveHelp: "No volverá a pedirlo", UnlockOnly: "Desbloquear", UnlockRemove: "Desbloquear y quitar el PIN",
		UnlockKeep: "Se guarda: se desbloqueará sola al arrancar", UnlockOnce: "Este módem lo volverá a pedir al encenderse",
		Reset: "Reiniciar módem", ResetConfirm: "Toca otra vez: sale del reparto ~30 s", Resetting: "Reiniciando el módem",
		Subnet: "Separar las redes", SubnetConfirm: "Toca otra vez: el otro módem igual cambia de red y se corta unos segundos",
		SubnetDone: "Separando las redes",
		Saved:      "Guardado", ForceConfirm: "Es la única conexión al aire: toca otra vez para hacerlo igualmente",
		Unlocked: "SIM desbloqueada", NotSupported: "Este módem no deja hacer eso desde aquí",
	}
	modemEN = ModemStrings{
		RSRP: "Power (RSRP)", SINR: "Noise (SINR)", RSRQ: "Quality (RSRQ)", NoSignal: "No signal data",
		SIMLabel: "SIM", NetLabel: "Network",
		SIMState: map[string]string{"ready": "ready", "pin": "asks for its PIN", "puk": "blocked (PUK)", "missing": "no SIM",
			"error": "error", "unknown": "unknown"},
		Reg: map[string]string{"home": "home", "roaming": "roaming", "searching": "searching", "denied": "refused",
			"idle": "not registered", "unknown": "unknown"},
		Network: "Network", APN: "APN", Roaming: "Roaming", Name: "Name",
		Modes:    map[string]string{"auto": "Automatic", "4g": "4G only", "5g": "4G + 5G"},
		APNAutoF: "automatic (%s)", APNAutoNet: "automatic",
		RoamingOn: "allowed", RoamingOff: "off",
		NameHelp: "Up to 12 characters · empty: the operator's", APNHelp: "Letters, digits, dots and hyphens · empty: automatic",
		Unlock: "Unlock the SIM", PINTitle: "SIM PIN", PINHelp: "4 to 8 digits",
		PINRemoveHelp: "It will not ask for it again", UnlockOnly: "Unlock", UnlockRemove: "Unlock and remove the PIN",
		UnlockKeep: "Kept: it will unlock itself at start", UnlockOnce: "This modem will ask for it again when it powers on",
		Reset: "Restart modem", ResetConfirm: "Tap again: it leaves the share for ~30 s", Resetting: "Restarting the modem",
		Subnet: "Separate the networks", SubnetConfirm: "Tap again: the other, identical modem changes network and drops for a few seconds",
		SubnetDone: "Separating the networks",
		Saved:      "Saved", ForceConfirm: "It is the only connection on air: tap again to do it anyway",
		Unlocked: "SIM unlocked", NotSupported: "This modem does not allow that from here",
	}
	modemFR = ModemStrings{
		RSRP: "Puissance (RSRP)", SINR: "Bruit (SINR)", RSRQ: "Qualité (RSRQ)", NoSignal: "Pas de données de signal",
		SIMLabel: "SIM", NetLabel: "Réseau",
		SIMState: map[string]string{"ready": "prête", "pin": "demande son PIN", "puk": "bloquée (PUK)", "missing": "pas de SIM",
			"error": "erreur", "unknown": "inconnue"},
		Reg: map[string]string{"home": "nominal", "roaming": "en itinérance", "searching": "recherche", "denied": "refusée",
			"idle": "non enregistrée", "unknown": "inconnue"},
		Network: "Réseau", APN: "APN", Roaming: "Itinérance", Name: "Nom",
		Modes:    map[string]string{"auto": "Automatique", "4g": "4G seulement", "5g": "4G + 5G"},
		APNAutoF: "automatique (%s)", APNAutoNet: "automatique",
		RoamingOn: "autorisée", RoamingOff: "désactivée",
		NameHelp: "Jusqu'à 12 caractères · vide : celui de l'opérateur", APNHelp: "Lettres, chiffres, points et tirets · vide : automatique",
		Unlock: "Déverrouiller la SIM", PINTitle: "PIN de la SIM", PINHelp: "4 à 8 chiffres",
		PINRemoveHelp: "Il ne sera plus demandé", UnlockOnly: "Déverrouiller", UnlockRemove: "Déverrouiller et retirer le PIN",
		UnlockKeep: "Gardée : elle se déverrouillera seule au démarrage", UnlockOnce: "Ce modem le redemandera à l'allumage",
		Reset: "Redémarrer le modem", ResetConfirm: "Touchez encore : il quitte la répartition ~30 s", Resetting: "Redémarrage du modem",
		Subnet: "Séparer les réseaux", SubnetConfirm: "Touchez encore : l'autre modem identique change de réseau et coupe quelques secondes",
		SubnetDone: "Séparation des réseaux",
		Saved:      "Enregistré", ForceConfirm: "C'est la seule connexion à l'antenne : touchez encore pour le faire quand même",
		Unlocked: "SIM déverrouillée", NotSupported: "Ce modem ne permet pas cela d'ici",
	}
)
