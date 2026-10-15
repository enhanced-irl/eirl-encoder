package ui

// The pages added after the first seven (MIXER.md §21: everything the web configures, on the screen) each live in
// their own file and register here, so a page is one file plus its strings: its layout (its buttons' rectangles),
// its drawing, its buttons on view, its taps and what it needs from the app.

// pageDef is one Settings page. Every function is optional but draw.
type pageDef struct {
	// layout places the page's buttons in l.Buttons (keys s_<something>_…), inside l.SettingsPage
	layout func(l *Layout)
	// draw draws the page's content (its title row is drawn by Settings; title may override it in a sub-view)
	draw func(u *UI, d *dc, app *AppSnapshot)
	// title is the title row in a sub-view of the page: its title and whether it has ← (ok false: the tabs or the
	// page's name, as usual)
	title func(u *UI) (title string, back, ok bool)
	// keys are the page's buttons on view, in the order they are tried
	keys func(u *UI, app *AppSnapshot) []string
	// touch is a tap on one of them; false if it was not the page's
	touch func(u *UI, what string) bool
	// pickAt is the item a row's button shows when the finger goes down on it (ok false: none): a list that
	// reorders itself before the finger is lifted still acts on what was seen. touch reads it in u.tapPick
	pickAt func(u *UI, key string) (string, bool)
	// watch says what the page needs from the app while it is on the screen
	watch func(u *UI, w *AppWatch)
	// leave runs when the page stops being on view (another page, Settings closed, the screen locked): its drafts
	// and anything secret it shows go
	leave func(u *UI)
}

var pageDefs = map[string]*pageDef{}

// registerPage adds a page (from its file's init).
func registerPage(id string, p *pageDef) {
	if _, dup := pageDefs[id]; dup {
		panic("ui: page registered twice: " + id)
	}
	pageDefs[id] = p
}

// pageLayouts places every registered page's buttons (settingsLayout calls it).
func (l *Layout) pageLayouts() {
	for _, id := range SettingsPages {
		if p := pageDefs[id]; p != nil && p.layout != nil {
			p.layout(l)
		}
	}
}

// leavePage runs the page's leave, if it has one.
func (u *UI) leavePage(page string) {
	if p := pageDefs[page]; p != nil && p.leave != nil {
		p.leave(u)
	}
}
