package ui

// RecStrings is the local recording (RECORDING.md §1, §6.5): the header's line, the REC button, what Go live says with
// the recording, and the Recording page (Stream › Recording). The same words as the web's card (lang-*.js rec.*), shorter
// where a line of the screen is narrower. Its tables, here; Strings carries them.
type RecStrings struct {
	// The header's second line: recording, starting, or why a recording stopped (short: by the engine's reason)
	HeadOnF      string // %02d:%02d:%02d the time recorded
	LeftF        string // %s = the time left (MinF…)
	HeadStarting string
	HeadSlow     string // instead of the time left while the disk drops fragments
	HeadStopped  map[string]string
	// Spans of time: 45 min, 3 h 20 min, 12 h
	MinF string // %d
	HMF  string // %d h, %d min
	HF   string // %d
	// The REC button: its name, what holding it does, and why it cannot start (its sub-line, short; what a tap says,
	// longer), by none | ejected | checking | full | bad; the engine's refusals, by its code
	Rec, Hold, HoldStop, Starting string
	Why, WhyTap                   map[string]string
	Refused                       map[string]string
	// Go live with REC_MODE=with_output: it records too; or why it will not (by none | full | bad)
	EmitRec   string
	EmitNoRec map[string]string
	// A disk plugged in while live (the board's supply is weak: RECORDING.md §3)
	PlugWhileLive string
	// The page: the mode, the quality, the disk
	Mode, Quality, Disk string
	Modes, ModeHints    map[string]string // by REC_MODE
	Qualities           map[string]string // by REC_QUALITY
	RateF               string            // %d Mbps
	GBhF                string            // %s GB in an hour (the decimal separator already in)
	QualityHint         string
	QualityLocked       string
	DiskStates          map[string]string // by the storage state
	NoneHint            string
	EjectedHint         string
	Unnamed, FSUnknown  string
	FreeOfF             string // %s free, %s size
	FreeF               string // %s free
	WritesF             string // %s MB/s
	TimeAtF             string // %s the time left, %s the quality (in a sentence)
	MBF, GBF, TBF       string // %s
	// Warnings (by the web's keys: ntfs, unknownFs, tooSmall, tooSmallPart, unformatted, readonly, checkFailed,
	// mountFailed, dirty, low, another, busyRec) and the FAT one (FatF: %s the filesystem)
	Warns map[string]string
	FatF  string
	// Why a recording stopped, in full (the web's), and what it does while REC is still on
	Reasons            map[string]string
	WaitDisk, Retrying string
	// Eject and Format, and the questions before formatting and before turning the recording off while it records
	Eject, Format, FormatYes string
	FormatAskF               string // %s = the disk (QuotedF, and its model and size)
	QuotedF                  string // %s
	Formatting, Formatted    string
	Ejecting, Ejected        string
	ModeOffAsk, ModeOffYes   string
}

var (
	recES = RecStrings{
		HeadOnF:      "REC %02d:%02d:%02d",
		LeftF:        "%s libres",
		HeadStarting: "REC · empezando…",
		HeadSlow:     "el disco no da abasto",
		HeadStopped: map[string]string{
			"no_disk":       "Grabación: no hay disco",
			"disk_removed":  "Se desconectó el disco mientras grababa",
			"disk_full":     "Disco lleno: la grabación se ha parado",
			"disk_slow":     "Disco lento: se perdió parte de la grabación",
			"write_error":   "No se ha podido escribir en el disco",
			"encoder_error": "Ha fallado el codificador de la grabación",
			"power_lost":    "La grabación se paró por un corte de corriente",
			"internal":      "La grabación se paró por un error interno",
		},
		MinF: "%d min", HMF: "%d h %d min", HF: "%d h",
		Rec: "REC", Hold: "mantén 1 s", HoldStop: "mantén para parar", Starting: "empezando…",
		Why: map[string]string{"none": "Conecta un disco USB", "ejected": "Disco expulsado", "checking": "Comprobando…",
			"full": "Disco lleno", "bad": "Disco no válido"},
		WhyTap: map[string]string{"none": "Conecta un disco USB para grabar",
			"ejected":  "Desconecta el disco y vuelve a conectarlo",
			"checking": "Espera: se está comprobando el disco",
			"full":     "El disco está lleno: libera espacio o usa otro",
			"bad":      "Este disco no sirve: mira por qué en Ajustes"},
		Refused: map[string]string{"disabled": "la grabación está desactivada", "no_disk": "no hay un disco listo",
			"disk_full": "el disco está lleno"},
		EmitRec: "EMITIR + GRABAR",
		EmitNoRec: map[string]string{"none": "sin disco: no se graba", "full": "disco lleno: no se graba",
			"bad": "disco no válido: no se graba"},
		PlugWhileLive: "Conectar un disco en directo puede dejar sin corriente al codificador: un disco duro, solo con hub alimentado",
		Mode:          "Modo", Quality: "Calidad", Disk: "Disco",
		Modes: map[string]string{"off": "Desactivada", "manual": "Independiente", "with_output": "Con el directo"},
		ModeHints: map[string]string{"off": "No se graba nada",
			"manual":      "La empiezas y la paras con REC, emitas o no",
			"with_output": "«Emitir» también empieza a grabar y al parar se paran las dos. Con REC grabas sin emitir"},
		Qualities:     map[string]string{"standard": "Estándar", "high": "Alta", "max": "Máxima", "efficient": "Eficiente"},
		RateF:         "%d Mbps",
		GBhF:          "%s GB/h",
		QualityHint:   "El programa con su audio, en MP4. «Eficiente» (H.265) ocupa menos, pero algunos editores antiguos no lo abren",
		QualityLocked: "La calidad no se puede cambiar mientras grabas",
		DiskStates: map[string]string{"none": "Sin disco", "checking": "Comprobando…", "ready": "Listo",
			"unsupported": "No se puede usar", "unformatted": "Sin formato", "error": "Con error", "ejected": "Expulsado"},
		NoneHint:    "Conecta un disco o un pendrive USB (exFAT, FAT32 o ext4). Hazlo antes de emitir, y un disco duro solo a través de un hub con alimentación propia: sin ella, el codificador puede reiniciarse.",
		EjectedHint: "Ya puedes desconectar el disco. Para volver a grabar en él, desconéctalo y vuelve a conectarlo.",
		Unnamed:     "Disco USB", FSUnknown: "Desconocido",
		FreeOfF: "%s libres de %s", FreeF: "%s libres", WritesF: "escribe a %s MB/s",
		TimeAtF: "Da para %s en calidad %s",
		MBF:     "%s MB", GBF: "%s GB", TBF: "%s TB",
		Warns: map[string]string{
			"ntfs":         "Los discos NTFS no se pueden usar: formatéalo en exFAT para grabar (se borra todo lo que tenga).",
			"unknownFs":    "El codificador no puede usar el formato de este disco: formatéalo en exFAT para grabar (se borra todo lo que tenga).",
			"tooSmall":     "El disco es demasiado pequeño: hace falta al menos 1 GB.",
			"tooSmallPart": "Su partición es de menos de 1 GB: formatéalo para usar el disco entero (se borra todo lo que tenga).",
			"unformatted":  "El disco no tiene formato: formatéalo para grabar.",
			"readonly":     "El disco está protegido contra escritura: si tiene un interruptor de bloqueo, muévelo y vuelve a conectarlo.",
			"checkFailed":  "El disco no ha pasado la comprobación (puede estar dañado o ser demasiado lento): prueba con otro o formatéalo.",
			"mountFailed":  "No se ha podido abrir el disco: desconéctalo y vuelve a conectarlo, o formatéalo.",
			"dirty":        "La última vez no se expulsó bien: compruébalo en un ordenador.",
			"low":          "Quedan menos de 10 minutos de grabación en el disco.",
			"another":      "Hay otro disco conectado: solo se usa uno a la vez.",
			"busyRec":      "Deja de grabar para expulsar o formatear el disco.",
		},
		FatF: "%s: las grabaciones se parten en archivos de 3,9 GB, uno detrás de otro. Formatéalo en exFAT para tener un solo archivo.",
		Reasons: map[string]string{
			"no_disk":       "No hay disco donde grabar",
			"disk_removed":  "Se desconectó el disco mientras grababa",
			"disk_full":     "El disco se ha llenado: la grabación se ha parado",
			"disk_slow":     "El disco es demasiado lento: se ha perdido parte de la grabación",
			"write_error":   "No se ha podido escribir en el disco",
			"encoder_error": "Ha fallado el codificador de la grabación",
			"power_lost":    "La grabación se paró por un corte de corriente",
			"internal":      "La grabación se ha parado por un error interno",
		},
		WaitDisk: "Seguirá en un archivo nuevo cuando vuelvas a conectar el disco.",
		Retrying: "Reintentando en un archivo nuevo…",
		Eject:    "Expulsar", Format: "Formatear", FormatYes: "Borrar y formatear",
		FormatAskF: "¿Formatear %s? Se borra todo lo que tenga, grabaciones incluidas, y queda en exFAT, listo para grabar.",
		QuotedF:    "«%s»",
		Formatting: "Formateando el disco… puede tardar hasta 2 minutos.", Formatted: "Disco formateado en exFAT.",
		Ejecting: "Expulsando el disco…", Ejected: "Disco expulsado: ya puedes desconectarlo.",
		ModeOffAsk: "Desactivar la grabación para la que está en marcha; el archivo se conserva. ¿Desactivar?",
		ModeOffYes: "Desactivar",
	}
	recEN = RecStrings{
		HeadOnF:      "REC %02d:%02d:%02d",
		LeftF:        "%s left",
		HeadStarting: "REC · starting…",
		HeadSlow:     "the disk cannot keep up",
		HeadStopped: map[string]string{
			"no_disk":       "Recording: no disk",
			"disk_removed":  "The disk was unplugged while recording",
			"disk_full":     "Disk full: the recording stopped",
			"disk_slow":     "Disk too slow: part of the recording was lost",
			"write_error":   "Could not write to the disk",
			"encoder_error": "The recording’s encoder failed",
			"power_lost":    "Recording stopped because of a power cut",
			"internal":      "Recording stopped by an internal error",
		},
		MinF: "%d min", HMF: "%d h %d min", HF: "%d h",
		Rec: "REC", Hold: "hold 1 s", HoldStop: "hold to stop", Starting: "starting…",
		Why: map[string]string{"none": "Connect a USB disk", "ejected": "Disk ejected", "checking": "Checking…",
			"full": "Disk full", "bad": "Disk not usable"},
		WhyTap: map[string]string{"none": "Connect a USB disk to record",
			"ejected":  "Unplug the disk and plug it in again",
			"checking": "Wait: the disk is being checked",
			"full":     "The disk is full: free some space or use another",
			"bad":      "This disk cannot be used: see why in Settings"},
		Refused: map[string]string{"disabled": "recording is turned off", "no_disk": "no disk is ready",
			"disk_full": "the disk is full"},
		EmitRec: "GO LIVE + RECORD",
		EmitNoRec: map[string]string{"none": "no disk: not recording", "full": "disk full: not recording",
			"bad": "disk not usable: not recording"},
		PlugWhileLive: "Plugging a disk in while live can cut the encoder’s power: a hard disk only through a powered hub",
		Mode:          "Mode", Quality: "Quality", Disk: "Disk",
		Modes: map[string]string{"off": "Off", "manual": "Independent", "with_output": "With the stream"},
		ModeHints: map[string]string{"off": "Nothing is recorded",
			"manual":      "You start and stop it with REC, whether you are live or not",
			"with_output": "“Go live” also starts recording and stopping stops both. With REC you record without streaming"},
		Qualities:     map[string]string{"standard": "Standard", "high": "High", "max": "Maximum", "efficient": "Efficient"},
		RateF:         "%d Mbps",
		GBhF:          "%s GB/h",
		QualityHint:   "The program with its audio, in MP4. “Efficient” (H.265) takes less room, but some older editors do not open it",
		QualityLocked: "The quality cannot change while recording",
		DiskStates: map[string]string{"none": "No disk", "checking": "Checking…", "ready": "Ready",
			"unsupported": "Cannot be used", "unformatted": "Not formatted", "error": "Error", "ejected": "Ejected"},
		NoneHint:    "Connect a USB disk or stick (exFAT, FAT32 or ext4). Do it before going live, and a hard disk only through a hub with its own power supply: without one, the encoder may restart.",
		EjectedHint: "You can unplug the disk now. To record on it again, unplug it and plug it in again.",
		Unnamed:     "USB disk", FSUnknown: "Unknown",
		FreeOfF: "%s free of %s", FreeF: "%s free", WritesF: "writes at %s MB/s",
		TimeAtF: "Room for %s at %s quality",
		MBF:     "%s MB", GBF: "%s GB", TBF: "%s TB",
		Warns: map[string]string{
			"ntfs":         "NTFS disks cannot be used: format it as exFAT to record (everything on it is erased).",
			"unknownFs":    "The encoder cannot use this disk’s format: format it as exFAT to record (everything on it is erased).",
			"tooSmall":     "The disk is too small: it needs at least 1 GB.",
			"tooSmallPart": "Its partition is under 1 GB: format it to use the whole disk (everything on it is erased).",
			"unformatted":  "The disk has no format: format it to record.",
			"readonly":     "The disk is write-protected: if it has a lock switch, slide it and plug the disk in again.",
			"checkFailed":  "The disk failed the check (it may be damaged or too slow): try another one or format it.",
			"mountFailed":  "The disk could not be opened: unplug it and plug it in again, or format it.",
			"dirty":        "It was not ejected properly last time: check it on a computer.",
			"low":          "Less than 10 minutes of recording left on the disk.",
			"another":      "Another disk is connected: only one is used at a time.",
			"busyRec":      "Stop recording to eject or format the disk.",
		},
		FatF: "%s: recordings are split into files of 3.9 GB, one after another. Format it as exFAT to get a single file.",
		Reasons: map[string]string{
			"no_disk":       "No disk to record to",
			"disk_removed":  "The disk was unplugged while recording",
			"disk_full":     "The disk filled up: the recording stopped",
			"disk_slow":     "The disk is too slow: part of the recording was lost",
			"write_error":   "Could not write to the disk",
			"encoder_error": "The recording’s encoder failed",
			"power_lost":    "Recording stopped because of a power cut",
			"internal":      "The recording stopped because of an internal error",
		},
		WaitDisk: "It will go on in a new file when you plug the disk back in.",
		Retrying: "Retrying in a new file…",
		Eject:    "Eject", Format: "Format", FormatYes: "Erase and format",
		FormatAskF: "Format %s? Everything on it is erased, recordings included, and it is left as exFAT, ready to record.",
		QuotedF:    "“%s”",
		Formatting: "Formatting the disk… it can take up to 2 minutes.", Formatted: "Disk formatted as exFAT.",
		Ejecting: "Ejecting the disk…", Ejected: "Disk ejected: you can unplug it now.",
		ModeOffAsk: "Turning recording off stops the one in progress; the file is kept. Turn it off?",
		ModeOffYes: "Turn off",
	}
	recFR = RecStrings{
		HeadOnF:      "REC %02d:%02d:%02d",
		LeftF:        "%s restantes",
		HeadStarting: "REC · démarrage…",
		HeadSlow:     "le disque ne suit pas",
		HeadStopped: map[string]string{
			"no_disk":       "Enregistrement : aucun disque",
			"disk_removed":  "Disque débranché pendant l’enregistrement",
			"disk_full":     "Disque plein : enregistrement arrêté",
			"disk_slow":     "Disque trop lent : une partie est perdue",
			"write_error":   "Impossible d’écrire sur le disque",
			"encoder_error": "L’encodeur de l’enregistrement a échoué",
			"power_lost":    "Enregistrement arrêté par une coupure de courant",
			"internal":      "Enregistrement arrêté par une erreur interne",
		},
		MinF: "%d min", HMF: "%d h %d min", HF: "%d h",
		Rec: "REC", Hold: "maintenez 1 s", HoldStop: "maintenez pour arrêter", Starting: "démarrage…",
		Why: map[string]string{"none": "Branchez un disque USB", "ejected": "Disque éjecté", "checking": "Vérification…",
			"full": "Disque plein", "bad": "Disque inutilisable"},
		WhyTap: map[string]string{"none": "Branchez un disque USB pour enregistrer",
			"ejected":  "Débranchez le disque puis rebranchez-le",
			"checking": "Patientez : le disque est en vérification",
			"full":     "Disque plein : libérez de l’espace ou changez-le",
			"bad":      "Ce disque est inutilisable : voir Réglages"},
		Refused: map[string]string{"disabled": "l’enregistrement est désactivé", "no_disk": "aucun disque prêt",
			"disk_full": "le disque est plein"},
		EmitRec: "DIFFUSER + ENREG.",
		EmitNoRec: map[string]string{"none": "sans disque : pas d’enreg.", "full": "disque plein : pas d’enreg.",
			"bad": "disque inutilisable : pas d’enreg."},
		PlugWhileLive: "Brancher un disque en direct peut couper le courant de l’encodeur : un disque dur, via un hub alimenté",
		Mode:          "Mode", Quality: "Qualité", Disk: "Disque",
		Modes: map[string]string{"off": "Désactivé", "manual": "Indépendant", "with_output": "Avec le direct"},
		ModeHints: map[string]string{"off": "Rien n’est enregistré",
			"manual":      "Vous le lancez et l’arrêtez avec REC, en direct ou non",
			"with_output": "« Diffuser » lance aussi l’enregistrement et l’arrêt arrête les deux. Avec REC, enregistrez sans diffuser"},
		Qualities:     map[string]string{"standard": "Standard", "high": "Haute", "max": "Maximale", "efficient": "Efficace"},
		RateF:         "%d Mb/s",
		GBhF:          "%s Go/h",
		QualityHint:   "Le programme avec son audio, en MP4. « Efficace » (H.265) prend moins de place, mais d’anciens logiciels de montage ne l’ouvrent pas",
		QualityLocked: "La qualité ne peut pas changer pendant l’enregistrement",
		DiskStates: map[string]string{"none": "Aucun disque", "checking": "Vérification…", "ready": "Prêt",
			"unsupported": "Inutilisable", "unformatted": "Non formaté", "error": "En erreur", "ejected": "Éjecté"},
		NoneHint:    "Branchez un disque ou une clé USB (exFAT, FAT32 ou ext4). Faites-le avant de diffuser, et un disque dur uniquement via un hub avec sa propre alimentation : sans elle, l’encodeur peut redémarrer.",
		EjectedHint: "Vous pouvez débrancher le disque. Pour y enregistrer à nouveau, débranchez-le puis rebranchez-le.",
		Unnamed:     "Disque USB", FSUnknown: "Inconnu",
		FreeOfF: "%s libres sur %s", FreeF: "%s libres", WritesF: "écrit à %s Mo/s",
		TimeAtF: "De quoi enregistrer %s en qualité %s",
		MBF:     "%s Mo", GBF: "%s Go", TBF: "%s To",
		Warns: map[string]string{
			"ntfs":         "Les disques NTFS ne sont pas pris en charge : formatez-le en exFAT pour enregistrer (tout son contenu est effacé).",
			"unknownFs":    "L’encodeur ne peut pas utiliser le format de ce disque : formatez-le en exFAT pour enregistrer (tout son contenu est effacé).",
			"tooSmall":     "Le disque est trop petit : il faut au moins 1 Go.",
			"tooSmallPart": "Sa partition fait moins de 1 Go : formatez-le pour utiliser tout le disque (tout son contenu est effacé).",
			"unformatted":  "Le disque n’est pas formaté : formatez-le pour enregistrer.",
			"readonly":     "Le disque est protégé en écriture : s’il a un verrou, déplacez-le et rebranchez le disque.",
			"checkFailed":  "Le disque n’a pas passé la vérification (il est peut-être endommagé ou trop lent) : essayez-en un autre ou formatez-le.",
			"mountFailed":  "Impossible d’ouvrir le disque : débranchez-le et rebranchez-le, ou formatez-le.",
			"dirty":        "Il n’a pas été éjecté correctement la dernière fois : vérifiez-le sur un ordinateur.",
			"low":          "Il reste moins de 10 minutes d’enregistrement sur le disque.",
			"another":      "Un autre disque est branché : un seul est utilisé à la fois.",
			"busyRec":      "Arrêtez l’enregistrement pour éjecter ou formater le disque.",
		},
		FatF: "%s : les enregistrements sont découpés en fichiers de 3,9 Go, qui se suivent. Formatez-le en exFAT pour avoir un seul fichier.",
		Reasons: map[string]string{
			"no_disk":       "Aucun disque où enregistrer",
			"disk_removed":  "Le disque a été débranché pendant l’enregistrement",
			"disk_full":     "Le disque est plein : l’enregistrement s’est arrêté",
			"disk_slow":     "Le disque est trop lent : une partie de l’enregistrement a été perdue",
			"write_error":   "Impossible d’écrire sur le disque",
			"encoder_error": "L’encodeur de l’enregistrement a échoué",
			"power_lost":    "Enregistrement arrêté à cause d’une coupure de courant",
			"internal":      "L’enregistrement s’est arrêté à cause d’une erreur interne",
		},
		WaitDisk: "Il reprendra dans un nouveau fichier quand vous rebrancherez le disque.",
		Retrying: "Nouvelle tentative dans un nouveau fichier…",
		Eject:    "Éjecter", Format: "Formater", FormatYes: "Effacer et formater",
		FormatAskF: "Formater %s ? Tout son contenu est effacé, enregistrements compris, et il reste en exFAT, prêt à enregistrer.",
		QuotedF:    "« %s »",
		Formatting: "Formatage du disque… cela peut prendre jusqu’à 2 minutes.", Formatted: "Disque formaté en exFAT.",
		Ejecting: "Éjection du disque…", Ejected: "Disque éjecté : vous pouvez le débrancher.",
		ModeOffAsk: "Désactiver l’enregistrement arrête celui en cours ; le fichier est conservé. Désactiver ?",
		ModeOffYes: "Désactiver",
	}
)
