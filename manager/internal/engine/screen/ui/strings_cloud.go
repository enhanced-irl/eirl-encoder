package ui

// CloudStrings is the EnhancedIRL page (the link: state, pair, new code, cancel, unlink) (MIXER.md §21). Its tables,
// here; Strings carries them.
type CloudStrings struct {
	Intro, IntroHow      string
	Pair, Renew, Unlink  string
	PairingText, Waiting string
	Linked, LinkedToF    string // LinkedToF: %s = the account
	LinkedHint           string
	Online, Offline      string
	UnlinkConfirmF       string // %s = the account (or YourAccount)
	YourAccount          string
	Errors               map[string]string // the link's last error, by the app's code
}

var (
	cloudES = CloudStrings{
		Intro:          "Vincula el codificador a tu cuenta para configurarlo y verlo desde la app de EnhancedIRL, aunque no estés en esta red.",
		IntroHow:       "Al tocar «Vincular» sale un código con su QR: ábrelo en un móvil con tu sesión de EnhancedIRL y confírmalo.",
		Pair:           "Vincular",
		Renew:          "Nuevo código",
		Unlink:         "Desvincular",
		PairingText:    "Escanea el QR o abre el enlace en un móvil u ordenador con tu sesión de EnhancedIRL y confirma que el código es el mismo:",
		Waiting:        "Esperando confirmación…",
		Linked:         "Vinculado",
		LinkedToF:      "Vinculado a %s",
		LinkedHint:     "El destino lo gestiona tu cuenta desde la app de EnhancedIRL; lo demás se sigue cambiando aquí.",
		Online:         "Conectado a EnhancedIRL",
		Offline:        "Sin conexión con EnhancedIRL: se reintenta solo",
		UnlinkConfirmF: "¿Desvincular de %s? Dejará de verse en la app y, si el destino lo puso EnhancedIRL, se borrará y dejará de emitir.",
		YourAccount:    "tu cuenta",
		Errors: map[string]string{
			"limit_reached":         "Tu cuenta ya tiene el máximo de codificadores",
			"email_not_verified":    "Verifica el correo de tu cuenta de EnhancedIRL y vuelve a intentarlo",
			"subscription_required": "Hace falta una suscripción activa en EnhancedIRL",
			"feature_disabled":      "Los codificadores aún no están disponibles para tu cuenta",
			"hardware_claimed":      "Sigue vinculado a otra cuenta: desvincúlalo desde ella o escribe a soporte",
			"denied":                "La vinculación se ha rechazado",
			"expired":               "El código ha caducado sin confirmarse: pide uno nuevo",
			"revoked":               "Se ha desvinculado desde la app de EnhancedIRL",
			"unlinked":              "Se ha desvinculado desde la app de EnhancedIRL",
			"save_failed":           "No se pudo guardar la vinculación: se perderá al reiniciar el codificador",
		},
	}
	cloudEN = CloudStrings{
		Intro:          "Link the encoder to your account to set it up and watch it from the EnhancedIRL app, even when you are not on this network.",
		IntroHow:       "Tapping “Link” shows a code with its QR: open it on a phone signed in to EnhancedIRL and confirm it.",
		Pair:           "Link",
		Renew:          "New code",
		Unlink:         "Unlink",
		PairingText:    "Scan the QR or open the link on a phone or computer signed in to EnhancedIRL and check that the code is the same:",
		Waiting:        "Waiting for confirmation…",
		Linked:         "Linked",
		LinkedToF:      "Linked to %s",
		LinkedHint:     "Your account manages the destination from the EnhancedIRL app; everything else is still changed here.",
		Online:         "Connected to EnhancedIRL",
		Offline:        "No connection with EnhancedIRL: retrying by itself",
		UnlinkConfirmF: "Unlink from %s? It will no longer show in the app and, if EnhancedIRL set the destination, it is deleted and streaming stops.",
		YourAccount:    "your account",
		Errors: map[string]string{
			"limit_reached":         "Your account already has the maximum number of encoders",
			"email_not_verified":    "Verify the e-mail of your EnhancedIRL account and try again",
			"subscription_required": "An active EnhancedIRL subscription is needed",
			"feature_disabled":      "Encoders are not available for your account yet",
			"hardware_claimed":      "It is still linked to another account: unlink it from there or write to support",
			"denied":                "The link was rejected",
			"expired":               "The code expired without being confirmed: ask for a new one",
			"revoked":               "It was unlinked from the EnhancedIRL app",
			"unlinked":              "It was unlinked from the EnhancedIRL app",
			"save_failed":           "The link could not be saved: it will be lost when the encoder restarts",
		},
	}
	cloudFR = CloudStrings{
		Intro:          "Associez l'encodeur à votre compte pour le configurer et le suivre depuis l'app EnhancedIRL, même hors de ce réseau.",
		IntroHow:       "En touchant « Associer », un code apparaît avec son QR : ouvrez-le sur un téléphone connecté à EnhancedIRL et confirmez-le.",
		Pair:           "Associer",
		Renew:          "Nouveau code",
		Unlink:         "Dissocier",
		PairingText:    "Scannez le QR ou ouvrez le lien sur un téléphone ou un ordinateur connecté à EnhancedIRL et vérifiez que le code est le même :",
		Waiting:        "En attente de confirmation…",
		Linked:         "Associé",
		LinkedToF:      "Associé à %s",
		LinkedHint:     "Votre compte gère la destination depuis l'app EnhancedIRL ; tout le reste se change toujours ici.",
		Online:         "Connecté à EnhancedIRL",
		Offline:        "Pas de connexion avec EnhancedIRL : nouvel essai automatique",
		UnlinkConfirmF: "Dissocier de %s ? Il n'apparaîtra plus dans l'app et, si EnhancedIRL a défini la destination, elle est supprimée et la diffusion s'arrête.",
		YourAccount:    "votre compte",
		Errors: map[string]string{
			"limit_reached":         "Votre compte a déjà le nombre maximal d'encodeurs",
			"email_not_verified":    "Vérifiez l'adresse e-mail de votre compte EnhancedIRL et réessayez",
			"subscription_required": "Un abonnement EnhancedIRL actif est nécessaire",
			"feature_disabled":      "Les encodeurs ne sont pas encore disponibles pour votre compte",
			"hardware_claimed":      "Il est toujours associé à un autre compte : dissociez-le depuis ce compte ou écrivez au support",
			"denied":                "L'association a été refusée",
			"expired":               "Le code a expiré sans être confirmé : demandez-en un nouveau",
			"revoked":               "Il a été dissocié depuis l'app EnhancedIRL",
			"unlinked":              "Il a été dissocié depuis l'app EnhancedIRL",
			"save_failed":           "L'association n'a pas pu être enregistrée : elle sera perdue au redémarrage de l'encodeur",
		},
	}
)
