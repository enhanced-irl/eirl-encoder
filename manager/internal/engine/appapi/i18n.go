package appapi

import (
	"fmt"
	"sync/atomic"
)

// The client's own user-visible texts, per language (CONTRACT §1): the app renders its messages in the language
// of Accept-Language, and these few are what the screen says when the app does not say anything (it does not
// answer, or its answer does not fit). Never logged.
var texts = map[string]map[string]string{
	"not_responding": {"es": "La app no responde", "en": "The app is not responding",
		"fr": "L'app ne répond pas"},
	"failed": {"es": "No se ha podido (%s)", "en": "Could not do it (%s)", "fr": "Impossible (%s)"},
	"encoder_app_not_responding": {"es": "La app del codificador no responde",
		"en": "The encoder app is not responding", "fr": "L'app de l'encodeur ne répond pas"},
	"no_network_state": {"es": "La app no da el estado de la red: %s",
		"en": "The app does not give the network state: %s", "fr": "L'app ne donne pas l'état du réseau : %s"},
	"unexpected": {"es": "respuesta inesperada", "en": "unexpected answer", "fr": "réponse inattendue"},
	"no_scan": {"es": "La app no da la lista de redes", "en": "The app does not give the list of networks",
		"fr": "L'app ne donne pas la liste des réseaux"},
	"saved_not_applied": {"es": "Guardado, pero no se ha podido aplicar", "en": "Saved, but it could not be applied",
		"fr": "Enregistré, mais impossible de l'appliquer"},
	"forgotten": {"es": "Red olvidada: %s", "en": "Network forgotten: %s", "fr": "Réseau oublié : %s"},
	"renamed": {"es": "Wifi renombrada: une de nuevo las cámaras y los móviles a «%s»",
		"en": "Wi-Fi renamed: join the cameras and phones to “%s” again",
		"fr": "Wi-Fi renommé : reconnectez les caméras et les téléphones à « %s »"},
	"renamed_off": {"es": "Guardado: la wifi se llamará «%s» al encenderla",
		"en": "Saved: the Wi-Fi will be called “%s” when it is on",
		"fr": "Enregistré : le Wi-Fi s'appellera « %s » une fois activé"},
}

// DefaultLanguage is the language before the app says the device's (the screen's default too).
const DefaultLanguage = "en"

// language is the device language as the client knows it, shared by NetState and DeviceState: it goes in
// Accept-Language (the app's messages come back in it) and picks the texts above.
type language struct{ v atomic.Value }

func (l *language) get() string {
	if l == nil {
		return DefaultLanguage
	}
	if s, ok := l.v.Load().(string); ok && s != "" {
		return s
	}
	return DefaultLanguage
}

func (l *language) set(code string) {
	if l != nil && knownLanguages[code] {
		l.v.Store(code)
	}
}

var knownLanguages = set("es", "en", "fr")

// text is the client's text key in lang (English if the language or the key is missing), with its arguments.
func text(lang, key string, a ...any) string {
	t := texts[key][lang]
	if t == "" {
		t = texts[key][DefaultLanguage]
	}
	if len(a) > 0 {
		return fmt.Sprintf(t, a...)
	}
	return t
}
