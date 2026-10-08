package snapshot

// Diff is what changed between two sets, by name and form: a name that moved
// from exact to suffix is removed as exact and added as suffix.
type Diff struct{ AddedSuffix, AddedExact, RemovedSuffix, RemovedExact []string }

// Compare finds what new adds to old and what it drops.
func Compare(old, new Set) Diff {
	var d Diff
	d.AddedSuffix, d.RemovedSuffix = sides(old.Suffix, new.Suffix)
	d.AddedExact, d.RemovedExact = sides(old.Exact, new.Exact)
	return d
}

// sides walks two sorted lists once.
func sides(old, new []string) (added, removed []string) {
	i, j := 0, 0
	for i < len(old) || j < len(new) {
		switch {
		case j == len(new) || i < len(old) && old[i] < new[j]:
			removed = append(removed, old[i])
			i++
		case i == len(old) || new[j] < old[i]:
			added = append(added, new[j])
			j++
		default:
			i++
			j++
		}
	}
	return added, removed
}

func (d Diff) Added() int   { return len(d.AddedSuffix) + len(d.AddedExact) }
func (d Diff) Removed() int { return len(d.RemovedSuffix) + len(d.RemovedExact) }
