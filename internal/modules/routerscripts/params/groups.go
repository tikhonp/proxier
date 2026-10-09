package params

// makeGroup gives a group its heading: when the only description in it
// belongs to its first item, the form and the panel show it over the group
// ("interface names" over lanIface, containerIface, vethName). The
// description still belongs to that item.
func makeGroup(items []Item) Group {
	g := Group{Items: items}
	n := 0
	for _, it := range items {
		if it.Description() != "" {
			n++
		}
	}
	if n == 1 && items[0].Description() != "" {
		g.Heading = items[0].Description()
	}
	return g
}
