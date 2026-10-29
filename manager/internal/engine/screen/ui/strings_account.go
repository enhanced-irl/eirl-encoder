package ui

// AccountStrings is Device → Name & password (the device name, the web password) (MIXER.md §21). Its tables, here;
// Strings carries them.
type AccountStrings struct {
	Name                  string
	Password, PasswordSub string
	PasswordNone          string
	Help                  string
	Current, CurrentHelp  string
	New, NewHelp          string
	NewShortF             string // %d
	PasswordChanged       string
	// The rescue (MANAGER.md §6): the setup code behind its eye, and a new web password without the current one
	SetupCode, SetupCodeSub        string
	Forgot, ForgotValue, ForgotSub string
}

var (
	accountES = AccountStrings{
		Name:            "Nombre del codificador",
		Password:        "Contraseña de la web",
		PasswordSub:     "Cambiarla cierra las sesiones abiertas en la web",
		PasswordNone:    "Aún no tiene: toca para ponerla",
		Help:            "El nombre es el que se ve en tu red y en EnhancedIRL. La contraseña protege la web del codificador.",
		Current:         "Contraseña actual de la web",
		CurrentHelp:     "Solo se ven puntos (el ojo la enseña)",
		New:             "Contraseña nueva de la web",
		NewHelp:         "Al menos 10 caracteres · solo se ven puntos (el ojo la enseña)",
		NewShortF:       "La contraseña tiene al menos %d caracteres",
		PasswordChanged: "Contraseña cambiada: se han cerrado las sesiones de la web",
		SetupCode:       "Código de configuración",
		SetupCodeSub:    "Si olvidas la contraseña, con él pones otra desde la web",
		Forgot:          "¿Has olvidado la contraseña de la web?",
		ForgotValue:     "Poner una nueva sin la actual",
		ForgotSub:       "Se cierran todas las sesiones de la web",
	}
	accountEN = AccountStrings{
		Name:            "Encoder name",
		Password:        "Web password",
		PasswordSub:     "Changing it closes the web's open sessions",
		PasswordNone:    "None yet: tap to set it",
		Help:            "The name is how it shows on your network and in EnhancedIRL. The password protects the encoder's web page.",
		Current:         "Current web password",
		CurrentHelp:     "Only dots show (the eye shows it)",
		New:             "New web password",
		NewHelp:         "At least 10 characters · only dots show (the eye shows it)",
		NewShortF:       "The password has at least %d characters",
		PasswordChanged: "Password changed: the web's sessions have been closed",
		SetupCode:       "Setup code",
		SetupCodeSub:    "If you forget the password, it sets a new one from the web",
		Forgot:          "Forgot the web password?",
		ForgotValue:     "Set a new one without the current one",
		ForgotSub:       "Every web session is closed",
	}
	accountFR = AccountStrings{
		Name:            "Nom de l'encodeur",
		Password:        "Mot de passe de la page web",
		PasswordSub:     "Le changer ferme les sessions ouvertes sur la page web",
		PasswordNone:    "Pas encore défini : touchez pour le définir",
		Help:            "Le nom est celui qui apparaît sur votre réseau et dans EnhancedIRL. Le mot de passe protège la page web de l'encodeur.",
		Current:         "Mot de passe actuel de la page web",
		CurrentHelp:     "Seuls des points s'affichent (l'œil le montre)",
		New:             "Nouveau mot de passe de la page web",
		NewHelp:         "Au moins 10 caractères · seuls des points s'affichent (l'œil le montre)",
		NewShortF:       "Le mot de passe a au moins %d caractères",
		PasswordChanged: "Mot de passe changé : les sessions de la page web ont été fermées",
		SetupCode:       "Code de configuration",
		SetupCodeSub:    "En cas d'oubli du mot de passe, il en définit un nouveau depuis la page web",
		Forgot:          "Mot de passe de la page web oublié ?",
		ForgotValue:     "En définir un nouveau sans l'actuel",
		ForgotSub:       "Toutes les sessions de la page web sont fermées",
	}
)
