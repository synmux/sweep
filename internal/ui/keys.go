package ui

import "charm.land/bubbles/v2/key"

// keyMap holds the bindings the model reacts to directly. Scrolling is handled
// by the viewport's own key map; scroll exists only to document it in help.
type keyMap struct {
	confirm key.Binding
	decline key.Binding
	quit    key.Binding
	scroll  key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		confirm: key.NewBinding(key.WithKeys("y", "Y"), key.WithHelp("y", "remove")),
		decline: key.NewBinding(key.WithKeys("n", "N", "esc", "q"), key.WithHelp("n/esc", "cancel")),
		quit:    key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		scroll:  key.NewBinding(key.WithKeys("up", "down", "pgup", "pgdown"), key.WithHelp("↑↓/pgup/pgdn", "scroll")),
	}
}
