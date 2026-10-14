package device

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHostname(t *testing.T) {
	for name, want := range map[string]string{
		"Estudio 2":                "estudio-2",
		"EIRL Encoder":             "eirl-encoder",
		"jetson":                   "jetson",
		"  Plató  Ñandú  ":         "plato-nandu",
		"2º plató":                 "eirl-2-plato",
		"Café-crème l'été":         "cafe-creme-l-ete",
		"Straße":                   "strasse",
		"---":                      "eirl-encoder",
		"日本":                       "eirl-encoder",
		"_x_":                      "x",
		strings.Repeat("abc ", 20): "abc-abc-abc-abc-abc-abc-abc-abc-abc-abc",
	} {
		got := Hostname(name)
		if got != want {
			t.Errorf("Hostname(%q) = %q, want %q", name, got, want)
		}
		if !reHostname.MatchString(got) {
			t.Errorf("Hostname(%q) = %q is not a valid hostname", name, got)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"EIRL Encoder", "Estudio 2", "Plató d'Ana", "Caméra_1.b", "日本 1", strings.Repeat("ñ", 40)} {
		if !ValidName(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range []string{"", "   ", strings.Repeat("a", 41), "a<b", "x\ny", "🙂", "a/b"} {
		if ValidName(n) {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestOpenMigration(t *testing.T) {
	now := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	// A board set up before the wizard: admin.hash exists → Spanish, done, name = hostname; written
	dir := t.TempDir()
	path := filepath.Join(dir, "device.json")
	st, origin, err := Open(path, true, "jetson", now)
	if err != nil || origin != Migrated {
		t.Fatalf("migrated: %v %v", origin, err)
	}
	s := st.Get()
	if s.Language != "es" || s.Name != "jetson" || !s.Setup.Done || s.Setup.CompletedAt == nil {
		t.Errorf("migrated settings: %+v", s)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("device.json not written 0640: %v %v", fi, err)
	}
	// Next start: loaded as is, even without the password any more
	st2, origin, err := Open(path, false, "other", now)
	if err != nil || origin != Loaded || st2.Get().Language != "es" || !st2.Get().Setup.Done {
		t.Errorf("reload: %v %v %+v", origin, err, st2.Get())
	}

	// Fresh: no password, no file → English, wizard to run; written so that a password set by the
	// wizard does not turn the next start into a migration
	dir = t.TempDir()
	path = filepath.Join(dir, "device.json")
	st, origin, err = Open(path, false, "jetson", now)
	if err != nil || origin != Fresh {
		t.Fatalf("fresh: %v %v", origin, err)
	}
	if s := st.Get(); s.Language != "en" || s.Name != DefaultName || s.Setup.Done {
		t.Errorf("fresh settings: %+v", s)
	}
	if _, origin, _ = Open(path, true, "jetson", now); origin != Loaded {
		t.Errorf("after the wizard set a password: %v, want loaded", origin)
	}

	// An odd hostname is not a valid display name: the default name
	dir = t.TempDir()
	st, _, _ = Open(filepath.Join(dir, "device.json"), true, "x/y", now)
	if st.Get().Name != DefaultName {
		t.Errorf("odd hostname: %q", st.Get().Name)
	}

	// Invalid file: reported, not overwritten, the migration rule in memory
	dir = t.TempDir()
	path = filepath.Join(dir, "device.json")
	os.WriteFile(path, []byte(`{"v":1,"language":"de","name":"x","setup":{}}`), 0o640)
	st, origin, err = Open(path, true, "jetson", now)
	if origin != Invalid || err == nil || st.Get().Language != "es" || !st.Get().Setup.Done {
		t.Errorf("invalid: %v %v %+v", origin, err, st.Get())
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"de"`) {
		t.Error("an invalid file must not be overwritten on open")
	}
}

func TestUpdateAndSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.json")
	st, _, err := Open(path, false, "jetson", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(func(s *Settings) error { s.Language = "de"; return nil }); err == nil {
		t.Error("an invalid language must be refused")
	}
	if st.Language() != "en" {
		t.Error("a refused update must change nothing")
	}
	if _, err := st.Update(func(s *Settings) error { return errors.New("no") }); err == nil {
		t.Error("fn error")
	}
	s, err := st.Update(func(s *Settings) error {
		s.Language = "fr"
		s.MarkStep(StepLanguage, "done")
		s.MarkStep(StepNetwork, "skipped")
		s.MarkStep(StepNetwork, "done") // the last mark wins
		s.MarkStep(StepCloud, "skipped")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{StepLanguage: "done", StepTime: "todo", StepNetwork: "done",
		StepPassword: "todo", StepName: "todo", StepCloud: "skipped"} {
		if got := s.StepState(id, false); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	if s.StepState(StepPassword, true) != "done" {
		t.Error("the password step is done once a password exists")
	}
	var f Settings
	b, _ := os.ReadFile(path)
	if json.Unmarshal(b, &f) != nil || f.Language != "fr" || !slices.Equal(f.Setup.Skipped, []string{StepCloud}) {
		t.Errorf("file: %s", b)
	}
	// Setup done: every step not skipped is done
	s.Setup.Done = true
	if s.StepState(StepTime, false) != "done" || s.StepState(StepCloud, false) != "skipped" {
		t.Error("done setup")
	}
}

func TestSetupCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup-code")
	created, err := EnsureCode(path)
	if err != nil || !created {
		t.Fatalf("EnsureCode: %v %v", created, err)
	}
	code, err := ReadCode(path)
	if err != nil || len(code) != CodeLen {
		t.Fatalf("ReadCode: %q %v", code, err)
	}
	for _, r := range code {
		if !strings.ContainsRune(CodeAlphabet, r) {
			t.Errorf("code %q has %q", code, r)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if created, _ := EnsureCode(path); created {
		t.Error("an existing code must be kept")
	}
	typed := strings.ToLower(code[:4]) + "-" + code[4:] + " "
	if ok, err := CheckCode(path, typed); !ok || err != nil {
		t.Errorf("typed with a dash, lowercase and a space: %v %v", ok, err)
	}
	if ok, _ := CheckCode(path, "AAAAAAAA"); ok && code != "AAAAAAAA" {
		t.Error("wrong code accepted")
	}
	if ok, _ := CheckCode(path, code+"X"); ok {
		t.Error("longer code accepted")
	}
	// An invalid file is an error, never replaced (the code may be on a label)
	os.WriteFile(path, []byte("hello\n"), 0o640)
	if _, err := EnsureCode(path); err == nil {
		t.Error("an invalid code file must be an error")
	}
	if _, err := CheckCode(path, "hello"); !errors.Is(err, ErrNoCode) {
		t.Errorf("CheckCode on an invalid file: %v", err)
	}
	// Different codes each time (crypto/rand)
	a, _ := NewCode()
	b, _ := NewCode()
	if a == b {
		t.Error("two equal codes in a row")
	}
}

func TestSystemdCommands(t *testing.T) {
	var calls []string
	props := "s \"Europe/Madrid\"\nb false\nb false\nt 0\n"
	sys := Systemd{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "busctl":
			return []byte(props), nil
		case name == "timedatectl" && slices.Contains(args, "list-timezones"):
			return []byte("Europe/Madrid\nUTC\nbad zone\n"), nil
		}
		return nil, nil
	}}
	ctx := context.Background()
	ti, err := sys.TimeInfo(ctx)
	if err != nil || ti.Timezone != "Europe/Madrid" || ti.NTP || ti.Synced || ti.RTC == nil || *ti.RTC {
		t.Fatalf("TimeInfo: %+v %v", ti, err)
	}
	sys.SetTimezone(ctx, "Europe/Paris")
	sys.SetNTP(ctx, false)
	sys.SetTime(ctx, time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC)) // Madrid is +02:00 in October
	sys.SetHostname(ctx, "estudio-2")
	zones, _ := sys.ListTimezones(ctx)
	want := []string{
		"timedatectl --no-ask-password set-timezone Europe/Paris",
		"timedatectl --no-ask-password set-ntp false",
		"timedatectl --no-ask-password set-time 2026-10-14 03:02:03",
		"hostnamectl --no-ask-password --static --transient set-hostname estudio-2",
	}
	var got []string
	for _, c := range calls {
		if !strings.HasPrefix(c, "busctl") && !strings.Contains(c, "list-timezones") {
			got = append(got, c)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !slices.Equal(zones, []string{"Europe/Madrid", "UTC"}) {
		t.Errorf("zones %v", zones)
	}
	// Nothing odd reaches a command line
	if sys.SetTimezone(ctx, "../../etc/passwd") == nil || sys.SetHostname(ctx, "-rf") == nil || sys.SetHostname(ctx, "Estudio") == nil {
		t.Error("invalid arguments must be refused before running anything")
	}
	if _, err := parseTimeProps([]byte("s \"x\"\n")); err == nil {
		t.Error("short busctl output")
	}
}

func TestZones(t *testing.T) {
	m := NewMemSystem("jetson")
	z := &Zones{Sys: m}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	list, err := z.List(context.Background(), now)
	if err != nil || len(list) != len(m.ZoneIDs) || list[0].ID != "America/Argentina/Buenos_Aires" {
		t.Fatalf("List: %v %v", list, err)
	}
	for _, zone := range list {
		switch zone.ID {
		case "Europe/Madrid":
			if zone.Offset != "+02:00" {
				t.Errorf("Madrid in October: %s", zone.Offset)
			}
		case "UTC":
			if zone.Offset != "+00:00" {
				t.Errorf("UTC: %s", zone.Offset)
			}
		}
	}
	// Cached: a later failure of the list does not matter
	m.ListFail = errors.New("gone")
	if ok, err := z.Valid(context.Background(), "Europe/Madrid"); !ok || err != nil {
		t.Errorf("Valid: %v %v", ok, err)
	}
	if ok, _ := z.Valid(context.Background(), "Mars/Olympus"); ok {
		t.Error("unknown zone accepted")
	}
	// Winter: the offset is recomputed
	list, _ = z.List(context.Background(), time.Date(2026, 12, 14, 12, 0, 0, 0, time.UTC))
	for _, zone := range list {
		if zone.ID == "Europe/Madrid" && zone.Offset != "+01:00" {
			t.Errorf("Madrid in December: %s", zone.Offset)
		}
	}
}

// Phone keyboards type typographic apostrophes: they become a plain ' before the rules apply
func TestNormalizeText(t *testing.T) {
	for in, want := range map[string]string{
		"  Plató d’Ana ":       "Plató d'Ana",
		"‘Cam’ 2":              "'Cam' 2",
		"OʼNeil":               "O'Neil",
		"x‛y":                  "x'y",
		"“quoted”":             "“quoted”", // double quotes: no rule allows them anyway
		"plain 'already' fine": "plain 'already' fine",
	} {
		if got := NormalizeText(in); got != want {
			t.Errorf("NormalizeText(%q) = %q, want %q", in, got, want)
		}
	}
	if !ValidName(NormalizeText("Plató d’Ana")) || ValidName("Plató d’Ana") {
		t.Error("the typographic apostrophe is valid only once normalized")
	}
}
