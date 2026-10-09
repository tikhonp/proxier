package generations

// Commands are the two lines pasted into the new router's terminal: fetch
// the file under the script's slug, then import it. This is the only place
// they are written.
func Commands(fetchURL, slug string) (fetch, imp string) {
	file := slug + ".rsc"
	return `/tool fetch url="` + fetchURL + `" dst-path=` + file, "/import " + file
}
