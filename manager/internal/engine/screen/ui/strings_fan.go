package ui

// FanStrings is the Fan page (System › Fan, FAN.md §5): the temperature the fan control reads, the main fan's speed
// and the second fan's switch. The same words as the web's card (lang-*.js fan.*), shorter where a line of the screen
// is narrower. Its tables, here; Strings carries them.
type FanStrings struct {
	TempF   string // %s the temperature (its decimal separator already in), %d the main fan's speed in %
	NoData  string // the app does not report the fans (the fan control is not running)
	None    string // a board without the second fan
	Second  string // the second fan's row
	AutoOnF string // automatic and on now; %s where it switches on by itself
	AutoF   string // automatic and off now; %s the same
	OffF    string // switched off; %s where it switches on all the same
	OffOnF  string // switched off, but on: hot, or the temperature could not be read; %s the same
	Confirm string // the first tap to switch it off
	HelpF   string // automatic; %s where it switches on
	HelpOff string // switched off, or about to be: what it risks and until when
}

var (
	fanES = FanStrings{
		TempF:   "Temperatura %s °C · ventilador principal al %d %%",
		NoData:  "El control del ventilador no está en marcha",
		None:    "Este aparato no tiene segundo ventilador",
		Second:  "Segundo ventilador",
		AutoOnF: "Automático · encendido (desde %s °C)",
		AutoF:   "Automático · se enciende a %s °C",
		OffF:    "Desactivado hasta reiniciar · se enciende igualmente a %s °C",
		OffOnF:  "Desactivado, pero encendido: %s °C o más, o sin lectura",
		Confirm: "Toca otra vez para desactivarlo",
		HelpF:   "Se enciende solo a partir de %s °C y se apaga cuando la placa se enfría.",
		HelpOff: "Desactivado, la placa se calienta más y con mucha carga puede bajar su rendimiento. " +
			"Se enciende igualmente si se calienta demasiado. Vuelve a automático al reiniciar.",
	}
	fanEN = FanStrings{
		TempF:   "Temperature %s °C · main fan at %d%%",
		NoData:  "The fan control is not running",
		None:    "This device has no second fan",
		Second:  "Second fan",
		AutoOnF: "Automatic · on (from %s °C)",
		AutoF:   "Automatic · switches on at %s °C",
		OffF:    "Off until the next restart · still switches on at %s °C",
		OffOnF:  "Off, but on: %s °C or more, or no reading",
		Confirm: "Tap again to switch it off",
		HelpF:   "It switches on by itself from %s °C and off when the board cools down.",
		HelpOff: "Switched off, the board runs hotter and under heavy load it may slow down. " +
			"It still switches on if the board gets too hot. Back to automatic at the next restart.",
	}
	fanFR = FanStrings{
		TempF:   "Température %s °C · ventilateur principal à %d %%",
		NoData:  "Le contrôle du ventilateur n’est pas en marche",
		None:    "Cet appareil n’a pas de second ventilateur",
		Second:  "Second ventilateur",
		AutoOnF: "Automatique · allumé (dès %s °C)",
		AutoF:   "Automatique · s’allume à %s °C",
		OffF:    "Désactivé jusqu’au redémarrage · s’allume quand même à %s °C",
		OffOnF:  "Désactivé, mais allumé : %s °C ou plus, ou sans mesure",
		Confirm: "Touchez encore pour le désactiver",
		HelpF:   "Il s’allume tout seul à partir de %s °C et s’éteint quand la carte refroidit.",
		HelpOff: "Désactivé, la carte chauffe davantage et, très chargée, elle peut ralentir. " +
			"Il s’allume quand même si elle chauffe trop. Retour en automatique au redémarrage.",
	}
)
