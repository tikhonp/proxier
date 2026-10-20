package provision

import "testing"

func TestDrawnSSHPortIsInRangeAndAvoided(t *testing.T) {
	var avoid []int
	for p := sshPortMin; p <= sshPortMax; p++ {
		if p != 20000 {
			avoid = append(avoid, p)
		}
	}
	for range 3 {
		if got := drawSSHPort(avoid); got != 20000 {
			t.Fatalf("drew %d, the only port left is 20000", got)
		}
	}
	for range 1000 {
		if got := drawSSHPort([]int{22}); got < sshPortMin || got > sshPortMax {
			t.Fatalf("drew %d, outside %d–%d", got, sshPortMin, sshPortMax)
		}
	}
}
