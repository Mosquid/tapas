package main

import (
	"io"
	"os"
	"strconv"
	"text/tabwriter"

	"tapas/internal/vault"
)

// maskPreview returns a recognizable fragment without exposing the full
// credential. At most one quarter of the characters are visible, capped at
// eight. Prefix characters are prioritized so common formats such as sk_ can
// be recognized; any remainder comes from the suffix.
func maskPreview(value string) (preview string, characters, visible int) {
	runes := []rune(value)
	characters = len(runes)
	visible = characters / 4
	if visible > 8 {
		visible = 8
	}
	prefixLength := visible
	if prefixLength > 4 {
		prefixLength = 4
	}
	suffixLength := visible - prefixLength
	preview = string(runes[:prefixLength]) + "…"
	if suffixLength > 0 {
		preview += string(runes[characters-suffixLength:])
	}
	return
}

// terminal reports whether a stream is attached to a screen rather than a pipe.
// Humans get a table; an agent or a script gets JSON.
func terminal(f *os.File) bool {
	info, e := f.Stat()
	return e == nil && info.Mode()&os.ModeCharDevice != 0
}

// listCredentials prints vault metadata. It never decrypts.
func listCredentials(s *vault.Store, asJSON bool) error {
	snap, e := s.Discover()
	if e != nil {
		return e
	}
	if asJSON || !terminal(os.Stdout) {
		emit(snap)
		return nil
	}
	count := strconv.Itoa(len(snap.Credentials)) + " credential"
	if len(snap.Credentials) != 1 {
		count += "s"
	}
	_, _ = io.WriteString(os.Stdout, "STORE  "+snap.Store+"   "+count+"\n\n")
	if len(snap.Credentials) == 0 {
		_, _ = io.WriteString(os.Stdout, "Add one with: tapas add --name <name> --service <service> --environment <environment>\n")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = io.WriteString(w, "NAME\tSERVICE\tENVIRONMENT\tVARIABLE\tID\n")
	for _, c := range snap.Credentials {
		_, _ = io.WriteString(w, c.Name+"\t"+c.Service+"\t"+c.Environment+"\t"+c.SuggestedEnv+"\t"+c.ID+"\n")
	}
	return w.Flush()
}
